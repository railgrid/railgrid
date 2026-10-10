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
// runtime. A sweep must remove the identity instead of retaining its last
// known-good permissions. Resolver transport and client errors must not wrap
// this sentinel, so transient reads leave the identity intact.
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
	record, err := s.upsertRecord(ctx, clusterID, owner, tenancyv1alpha1.ScopedIdentityAttestationWorkload, subject, shape, nil)
	if err != nil {
		return nil, err
	}
	token, err := s.materialize(ctx, record)
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
	scope, err := resolver.ResolveRecord(ctx, record)
	if err != nil {
		return nil, false, fmt.Errorf("resolving current workload scope: %w", err)
	}
	if err := serviceaccounts.ValidateWorkloadScope(scope); err != nil {
		return nil, false, fmt.Errorf("%w: current workload scope is invalid: %v", ErrWorkloadScopeRevoked, err)
	}
	if scope.Project != record.Spec.Owner.Name || scope.ProjectUID != record.Spec.Owner.UID ||
		scope.TenantPath != record.Spec.Annotations[serviceaccounts.AnnotationWorkloadIdentityTenantPath] ||
		scope.Environment != record.Spec.Annotations[serviceaccounts.AnnotationWorkloadIdentityEnvironment] ||
		scope.Instance != record.Spec.Annotations[serviceaccounts.AnnotationWorkloadIdentityInstance] {
		return nil, false, fmt.Errorf("%w: current workload scope does not match the recorded Project identity", ErrWorkloadScopeRevoked)
	}

	owner := Owner{
		Provider: workloadOwnerProvider, Kind: workloadOwnerKind, Group: workloadOwnerGroup,
		Version: workloadOwnerVersion, Resource: workloadOwnerResource,
		Name: scope.Project, UID: scope.ProjectUID, ClusterID: record.Spec.ClusterID,
	}
	shape := serviceaccounts.WorkloadIdentityShape(scope)
	updated, err := s.upsertRecord(ctx, record.Spec.ClusterID, owner,
		tenancyv1alpha1.ScopedIdentityAttestationWorkload, record.Spec.Attestation.Subject, shape, nil)
	if err != nil {
		return nil, false, err
	}
	if updated.Generation == record.Generation {
		return updated, false, nil
	}
	if _, err := s.materialize(ctx, updated); err != nil {
		s.markFailed(ctx, updated, err)
		return updated, true, err
	}
	return updated, true, nil
}
