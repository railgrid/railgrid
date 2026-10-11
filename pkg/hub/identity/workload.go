/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package identity

import (
	"context"
	"errors"
	"fmt"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	tenancyv1alpha1 "github.com/railgrid/railgrid/apis/tenancy/v1alpha1"
	"github.com/railgrid/railgrid/pkg/hub/serviceaccounts"
)

// The App Studio workload exchange is a thin adapter over this service, not a
// second minter. It keeps its own attestation (the infrastructure provider's
// pod review), its own scope derivation (the Project environment), and its own
// deterministic account name and annotations — all of which are contract with
// already-deployed runtimes and with the tenant resolver that reads those
// annotations back. What it no longer has is its own way of writing a
// ServiceAccount, its own RBAC materialization, or an identity nothing
// collects: it goes through EnsureWorkload, so a workload identity is now a
// recorded, garbage-collected ScopedIdentity like every other.

// workloadProjectGroup and friends mirror the Project coordinates the scope
// resolver verifies. The Project is the workload identity's OWNER, which is
// what gives these identities garbage collection for the first time: delete
// the Project and its runtime credentials go with it.
const (
	workloadOwnerProvider = "app-studio"
	workloadOwnerKind     = "Project"
	workloadOwnerGroup    = "ai.railgrid.ai"
	workloadOwnerVersion  = "v1alpha1"
	workloadOwnerResource = "projects"
)

// ErrWorkloadScopeRevoked marks a recorded Project environment identity whose
// current Project state can no longer authorize the recorded environment and
// runtime. A sweep narrows the recorded identity to no permissions, preserving
// the record as a durable retry point. Resolver transport and client errors
// must not wrap this sentinel, so transient reads leave the identity intact.
var ErrWorkloadScopeRevoked = errors.New("workload identity scope revoked")

// EnsureWorkload records and mints a pod-attested workload identity for a
// verified Project scope. clusterID is the tenant workspace (the workload
// exchange addresses it by path, which is a valid /clusters/{…} segment).
//
// The rules and the account name come from serviceaccounts.WorkloadIdentityShape,
// unchanged: they are derived from the verified Project, never from anything
// the caller sent, so the rule policy that governs provider-asserted requests
// does not apply and is deliberately not run here.
func (s *Service) EnsureWorkload(ctx context.Context, clusterID string, scope serviceaccounts.WorkloadIdentityScope, subject string) (*Token, error) {
	if s == nil || s.records == nil || s.clients == nil {
		return nil, fmt.Errorf("identity service is unavailable")
	}
	if err := serviceaccounts.ValidateWorkloadScope(scope); err != nil {
		return nil, Refusal{Code: CodeInvalidRequest, Reason: err.Error()}
	}
	revision, err := workloadOwnerRevision(scope)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(clusterID) == "" {
		return nil, Refusal{Code: CodeInvalidRequest, Reason: "clusterID is required"}
	}
	shape := serviceaccounts.WorkloadIdentityShape(scope)
	owner := Owner{
		Provider: workloadOwnerProvider, Kind: workloadOwnerKind,
		Group: workloadOwnerGroup, Version: workloadOwnerVersion,
		Resource: workloadOwnerResource, Name: scope.Project, UID: scope.ProjectUID,
		ClusterID: clusterID,
	}
	record, err := s.upsertRecord(ctx, clusterID, owner, tenancyv1alpha1.ScopedIdentityAttestationWorkload, subject, shape, revision, nil)
	if err != nil {
		return nil, err
	}
	token, err := s.materializeForRevision(ctx, record, revision)
	if err != nil {
		s.markFailed(ctx, record, err)
		return nil, err
	}
	return token, nil
}

// reconcileWorkloadScope updates a recorded workload identity when the
// Project's current environment grants have changed. It is called by the
// ordinary identity sweep after the owner UID is verified. Updating the
// record and ClusterRole removes revoked permissions while existing tokens
// continue to use the same ServiceAccount; it does not issue a new token.
func (s *Service) reconcileWorkloadScope(
	ctx context.Context,
	record *tenancyv1alpha1.ScopedIdentity,
	resolver WorkloadScopeResolver,
) (*tenancyv1alpha1.ScopedIdentity, bool, error) {
	if s == nil || s.records == nil || s.clients == nil || resolver == nil || record == nil {
		return nil, false, fmt.Errorf("workload identity reconciliation is unavailable")
	}
	if record.Spec.Attestation.Mode != tenancyv1alpha1.ScopedIdentityAttestationWorkload {
		return record, false, nil
	}
	var lastConflict error
	for attempt := 0; attempt < maxWorkloadReconcileAttempts; attempt++ {
		current, err := s.records.Get(ctx, record.Name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return nil, false, RevisionConflict{Code: CodeVersionConflict, Reason: "the workload identity was removed during reconciliation"}
		}
		if err != nil {
			return nil, false, fmt.Errorf("reading workload identity before reconciliation: %w", err)
		}
		if record.UID != "" && current.UID != record.UID {
			return nil, false, RevisionConflict{Code: CodeVersionConflict, Reason: "the workload identity was replaced during reconciliation"}
		}
		if current.Spec.Attestation.Mode != tenancyv1alpha1.ScopedIdentityAttestationWorkload {
			return current, false, nil
		}
		observedRevision, err := identityRecordRevisionOf(current)
		if err != nil {
			return nil, false, err
		}
		owner := workloadOwnerFromRecord(current)
		// A revoked result carries no scope revision. Bracket its live Project
		// read with this observation and upsertRecord's exact revision check;
		// never attach a revision observed after resolution to an old decision.
		ownerObservation, found, err := s.observeOwner(ctx, current.Spec.ClusterID, owner, true)
		if err != nil {
			return nil, false, fmt.Errorf("observing Project before workload scope reconciliation: %w", err)
		}
		if !found || owner.UID != "" && ownerObservation.UID != owner.UID {
			return nil, false, RevisionConflict{Code: CodeStaleOwner, Reason: "the recorded Project no longer exists"}
		}
		if ownerObservation.Generation < 1 || strings.TrimSpace(ownerObservation.ResourceVersion) == "" {
			return nil, false, RevisionConflict{Code: CodeVersionConflict, Reason: "the Project probe returned no complete revision"}
		}
		preObservedOwnerRevision := &OwnerRevision{
			Generation: ownerObservation.Generation, ResourceVersion: ownerObservation.ResourceVersion,
		}

		// Resolve against the latest record tuple on every retry. A scope from a
		// prior read is never reused after either the record or Project revision
		// has moved.
		scope, err := resolver.ResolveRecord(ctx, current)
		revoked := errors.Is(err, ErrWorkloadScopeRevoked)
		if err != nil && !revoked {
			return nil, false, fmt.Errorf("resolving current workload scope: %w", err)
		}
		if !revoked {
			revoked = serviceaccounts.ValidateWorkloadScope(scope) != nil ||
				scope.Project != current.Spec.Owner.Name || scope.ProjectUID != current.Spec.Owner.UID ||
				scope.TenantPath != current.Spec.Annotations[serviceaccounts.AnnotationWorkloadIdentityTenantPath] ||
				scope.Environment != current.Spec.Annotations[serviceaccounts.AnnotationWorkloadIdentityEnvironment] ||
				scope.Instance != current.Spec.Annotations[serviceaccounts.AnnotationWorkloadIdentityInstance]
		}

		var shape serviceaccounts.ScopedIdentityShape
		projectRevision := preObservedOwnerRevision
		if revoked {
			// Keep the deterministic account and audit record, but persist an
			// explicit no-permissions shape. The record and Project revisions
			// fence this write against a concurrent refresh or regrant.
			shape = Shape(current)
			shape.Rules = nil
		} else {
			shape = serviceaccounts.WorkloadIdentityShape(scope)
			projectRevision, err = workloadOwnerRevision(scope)
			if err != nil {
				return nil, false, err
			}
		}
		updated, err := s.upsertRecord(ctx, current.Spec.ClusterID, owner,
			tenancyv1alpha1.ScopedIdentityAttestationWorkload, current.Spec.Attestation.Subject,
			shape, projectRevision, &observedRevision)
		if err != nil {
			if isRevisionConflict(err) {
				lastConflict = err
				continue
			}
			return nil, false, err
		}
		changed := updated.Generation != current.Generation
		if changed {
			if _, err := s.materializeForRevision(ctx, updated, projectRevision); err != nil {
				if isRevisionConflict(err) {
					lastConflict = err
					continue
				}
				s.markFailed(ctx, updated, err)
				return updated, true, err
			}
		}
		return updated, changed, nil
	}
	if lastConflict == nil {
		lastConflict = RevisionConflict{Code: CodeVersionConflict, Reason: "the workload identity changed during every reconciliation attempt"}
	}
	return nil, false, lastConflict
}

func workloadOwnerFromRecord(record *tenancyv1alpha1.ScopedIdentity) Owner {
	return Owner{
		Provider: record.Spec.Owner.Provider, Kind: record.Spec.Owner.Kind,
		Group: record.Spec.Owner.Group, Version: record.Spec.Owner.Version,
		Resource: record.Spec.Owner.Resource, Name: record.Spec.Owner.Name,
		UID: record.Spec.Owner.UID, ClusterID: record.Spec.ClusterID,
	}
}

const maxWorkloadReconcileAttempts = 3

// identityRecordRevision is the record snapshot on which a resolved workload
// scope was based. ResourceVersion is compared only for equality, like the
// Project revision it accompanies.
type identityRecordRevision struct {
	UID             string
	Generation      int64
	ResourceVersion string
}

func identityRecordRevisionOf(record *tenancyv1alpha1.ScopedIdentity) (identityRecordRevision, error) {
	if record == nil || record.UID == "" || record.Generation < 1 || strings.TrimSpace(record.ResourceVersion) == "" {
		return identityRecordRevision{}, RevisionConflict{Code: CodeVersionConflict, Reason: "the workload identity record has no complete revision"}
	}
	return identityRecordRevision{
		UID: string(record.UID), Generation: record.Generation, ResourceVersion: record.ResourceVersion,
	}, nil
}

func identityRecordRevisionMatches(record *tenancyv1alpha1.ScopedIdentity, expected identityRecordRevision) bool {
	actual, err := identityRecordRevisionOf(record)
	return err == nil && actual == expected
}

func isRevisionConflict(err error) bool {
	var conflict RevisionConflict
	return errors.As(err, &conflict)
}
