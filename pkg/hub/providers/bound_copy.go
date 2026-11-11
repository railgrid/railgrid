// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package providers

import (
	"context"
	"errors"
	"slices"
	"strings"

	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	conditionsv1alpha1 "github.com/kcp-dev/sdk/apis/third_party/conditions/apis/conditions/v1alpha1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
)

// ErrBoundCopyUnavailable means the workspace has no usable registered copy.
// It deliberately carries no tenant, registration, or upstream error details.
var ErrBoundCopyUnavailable = errors.New("provider copy is not available in the selected workspace")

// BoundCopyResolver selects a backend using the authenticated caller's verified
// workspace and its live APIBinding. It must never select from request headers
// or silently substitute the catalog's same-named organization copy.
type BoundCopyResolver func(ctx context.Context, tenantPath, cluster, name string) (Provider, error)

func (p *ProviderProxy) SetBoundCopyResolver(resolve BoundCopyResolver) {
	p.boundCopy = resolve
}

// BoundCopy matches the immutable coordinates of an actual APIBinding against
// a current registration visible to this organization. A same-named replacement
// with a new provider workspace is a different copy.
func (r *Registry) BoundCopy(org, name, path, export, cluster string) (Provider, bool) {
	if path == "" || export == "" || cluster == "" {
		return Provider{}, false
	}
	var selected Provider
	matched := false
	for _, entry := range r.List() {
		if entry.Name != name || (entry.OrgUUID != "" && entry.OrgUUID != org) || entry.APIExportPath != path || entry.APIExportName != export || entry.WorkspaceCluster != cluster {
			continue
		}
		if matched {
			return Provider{}, false
		}
		selected, matched = entry, true
	}
	return selected, matched
}

// Provider setup reserves the APIBinding before recording its grants.
const (
	ProviderSetupAnnotation = "railgrid.ai/provider-setup"
	ProviderSetupPending    = "pending"
	ProviderSetupComplete   = "complete"
)

// BindingReadiness is shared by routing and the enabled-provider projection.
// A Bound phase alone does not establish that the export and claims are usable.
func BindingReadiness(item *unstructured.Unstructured) (bool, string) {
	setup := item.GetAnnotations()[ProviderSetupAnnotation]
	if setup == ProviderSetupPending {
		return false, "Provider setup is waiting for permission decisions."
	}
	conditions, _, _ := unstructured.NestedSlice(item.Object, "status", "conditions")
	for _, raw := range conditions {
		c, ok := raw.(map[string]any)
		if !ok || c["status"] != "False" {
			continue
		}
		switch c["type"] {
		case "Ready", "APIExportValid", "BindingUpToDate", "InitialBindingCompleted", "PermissionClaimsValid", "PermissionClaimsApplied":
			message, _ := c["message"].(string)
			if message == "" {
				message, _ = c["reason"].(string)
			}
			if message == "" {
				message = "The provider binding is not ready."
			}
			return false, message
		}
	}
	phase, _, _ := unstructured.NestedString(item.Object, "status", "phase")
	if phase == "Bound" && setup == ProviderSetupComplete && !setupClaimsApplied(item) {
		return false, "Provider permission claims are still being applied."
	}
	return phase == "Bound", ""
}

func setupClaimsApplied(item *unstructured.Unstructured) bool {
	var binding apisv1alpha2.APIBinding
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(item.Object, &binding); err != nil {
		return false
	}
	applied := slices.ContainsFunc(binding.Status.Conditions, func(condition conditionsv1alpha1.Condition) bool {
		return condition.Type == apisv1alpha2.PermissionClaimsApplied && condition.Status == "True"
	})
	for _, claim := range binding.Spec.PermissionClaims {
		if claim.State != apisv1alpha2.ClaimAccepted {
			continue
		}
		if !applied || !slices.ContainsFunc(binding.Status.AppliedPermissionClaims, func(current apisv1alpha2.ScopedPermissionClaim) bool {
			return apiequality.Semantic.DeepEqual(current, claim.ScopedPermissionClaim)
		}) {
			return false
		}
	}
	return true
}

// Only authenticated, workspace-scoped MCP, OAuth and health requests use the
// native binding lookup. No-bearer probes and callers without a resolved workspace
// retain catalog resolution. Webhook credentials belong to the external sender;
// agent ingress has its own credential resolver. Neither uses this tenant path.
func usesBoundBackendCopy(path, prefix, name string) bool {
	rest := strings.TrimPrefix(path, prefix+"/"+name)
	return rest == "/mcp" || strings.HasPrefix(rest, "/mcp/") || rest == "/oauth" || strings.HasPrefix(rest, "/oauth/") || rest == "/healthz" || rest == "/readyz"
}
