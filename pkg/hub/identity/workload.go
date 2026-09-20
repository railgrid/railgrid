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
	record, err := s.upsertRecord(ctx, clusterID, owner, tenancyv1alpha1.ScopedIdentityAttestationWorkload, subject, shape)
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
