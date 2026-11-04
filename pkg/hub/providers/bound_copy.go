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

package providers

import (
	"context"
	"errors"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// ErrBoundCopyUnavailable means the selected workspace has no registered copy
// matching its live APIBinding. It deliberately reveals no provider or tenant
// details to the caller.
var ErrBoundCopyUnavailable = errors.New("provider copy is not available in the selected workspace")

// BoundCopyResolver selects a provider from the authenticated caller's live
// workspace APIBinding. It must not infer source from the organization catalog
// when the selected workspace has no matching binding.
type BoundCopyResolver func(ctx context.Context, tenantPath, cluster, name string) (Provider, error)

// BoundCopySnapshotResolver reads one selected workspace binding snapshot and
// returns only registered, ready provider copies present in that snapshot.
type BoundCopySnapshotResolver func(ctx context.Context, tenantPath string) (map[string]Provider, error)

// GetForExport resolves one registered provider copy from its exact APIExport
// coordinates, including a platform copy shadowed by a same-named org entry.
// Ambiguous registrations fail closed.
func (r *Registry) GetForExport(orgUUID, exportPath, exportName string) (Provider, bool) {
	if r == nil || orgUUID == "" || exportPath == "" || exportName == "" {
		return Provider{}, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	var selected Provider
	found := false
	for _, provider := range r.byKey {
		if provider.OrgUUID != "" && provider.OrgUUID != orgUUID {
			continue
		}
		if provider.APIExportPath != exportPath || provider.APIExportName != exportName {
			continue
		}
		if found {
			return Provider{}, false
		}
		selected = cloneProvider(*provider)
		found = true
	}
	return selected, found
}

// BoundCopy matches an APIBinding's complete source coordinates to one current
// registration visible to orgUUID. The issuer cluster is required because an
// APIExport can be rebound or recreated without changing its path/name.
func (r *Registry) BoundCopy(orgUUID, name, exportPath, exportName, issuerCluster string) (Provider, bool) {
	if name == "" || issuerCluster == "" {
		return Provider{}, false
	}
	provider, found := r.GetForExport(orgUUID, exportPath, exportName)
	if !found || provider.Name != name || provider.WorkspaceCluster == "" || provider.WorkspaceCluster != issuerCluster {
		return Provider{}, false
	}
	return provider, true
}

// BindingReadiness requires kcp to have completed the initial binding and to
// have no failed readiness, export, freshness or permission-claim conditions.
// A Bound phase by itself can leave an unusable provider visible during bind.
func BindingReadiness(binding *unstructured.Unstructured) (bool, string) {
	if binding == nil || binding.GetDeletionTimestamp() != nil {
		return false, "provider binding is unavailable"
	}
	conditions, _, _ := unstructured.NestedSlice(binding.Object, "status", "conditions")
	for _, raw := range conditions {
		condition, ok := raw.(map[string]any)
		if !ok || condition["status"] != "False" {
			continue
		}
		switch condition["type"] {
		case "Ready", "APIExportValid", "BindingUpToDate", "InitialBindingCompleted", "PermissionClaimsValid", "PermissionClaimsApplied":
			message, _ := condition["message"].(string)
			if message == "" {
				message, _ = condition["reason"].(string)
			}
			if message == "" {
				message = "provider binding is not ready"
			}
			return false, message
		}
	}
	phase, _, _ := unstructured.NestedString(binding.Object, "status", "phase")
	if phase != "Bound" {
		return false, "provider binding is not ready"
	}
	return true, ""
}
