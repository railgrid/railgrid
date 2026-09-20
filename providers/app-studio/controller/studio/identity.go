/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package studio

// The per-Studio identity: what the Studio reconciler acts as inside the
// tenant workspace when it converges the shared backends.
//
// It is the same mechanism as the per-project identity
// (controller/project/identity.go) and exists for the same reason: the shared
// search and browser instances belong to whichever infrastructure provider the
// WORKSPACE bound, and an APIExport permission claim could only ever pin one
// of them for every consuming workspace at once. The hub mints this instead,
// against a policy, with a TTL, and collects it when the Studio is deleted.
//
// What it holds is exactly the instance composition the CatalogEntry declares
// on the infrastructure dependency (manifest.yaml spec.dependencies, mirrored
// in internal/crossprovider/composition.go) — unnamed create/list/watch,
// name-scoped get/update/delete on the two fixed instances the Studio owns —
// and nothing else. No MCP grant: the Studio calls no tool. No APIBinding
// read: it resolves no data-plane coordinate. No Secret: the model credentials
// are this provider's own, read over its own virtual workspace.

import (
	"context"

	rbacv1 "k8s.io/api/rbac/v1"

	"github.com/railgrid/provider-sdk/identityclient"

	aiv1alpha1 "github.com/railgrid/provider-app-studio/apis/ai/v1alpha1"
	"github.com/railgrid/provider-app-studio/internal/crossprovider"
	"github.com/railgrid/provider-app-studio/internal/scopedidentity"
)

// studioOwner is the tuple the hub verifies before it mints anything: the
// Studio must exist in this workspace with this UID.
func studioOwner(st *aiv1alpha1.Studio, clusterName string) identityclient.Owner {
	return identityclient.Owner{
		Kind:      "Studio",
		Group:     aiv1alpha1.SchemeGroupVersion.Group,
		Version:   aiv1alpha1.SchemeGroupVersion.Version,
		Resource:  "studios",
		Name:      st.Name,
		UID:       string(st.UID),
		ClusterID: clusterName,
	}
}

// studioIdentityRules builds the Studio's rules: the declared instance
// composition, name-scoped to the backends this Studio actually owns.
//
// A Studio whose templates are not resolved yet still gets the unnamed half,
// because that is what the watch needs and because listing a kind is not
// access to any object of it. The named half appears as each reference
// resolves, so the grant tracks what exists rather than what might.
func studioIdentityRules(st *aiv1alpha1.Studio) []rbacv1.PolicyRule {
	rules := []rbacv1.PolicyRule{crossprovider.InstanceCollectionRule()}
	if names := studioInstanceNames(st); len(names) > 0 {
		if rule, ok := crossprovider.InstanceObjectRule(names); ok {
			rules = append(rules, rule)
		}
	}
	return rules
}

// studioInstanceNames lists the shared backends the Studio is bound to, sorted
// and deduplicated so an unchanged Studio produces an unchanged rule set (the
// fingerprint that decides whether to re-mint compares the rendering).
func studioInstanceNames(st *aiv1alpha1.Studio) []string {
	seen := map[string]bool{}
	var names []string
	for _, svc := range []service{searchService(st), browserService(st)} {
		ref := svc.ref
		if ref == nil || ref.Name == "" {
			continue
		}
		// A reference on a resource outside the declared composition names no
		// instance this provider may converge, so it is granted nothing.
		if ref.Resource != crossprovider.InstancesResource {
			continue
		}
		if seen[ref.Name] {
			continue
		}
		seen[ref.Name] = true
		names = append(names, ref.Name)
	}
	return scopedidentity.Sorted(names)
}

// identityToken returns the Studio's current token, minting or refreshing it
// through the hub. An empty token with no error means there is no hub to ask
// (REST-only dev); the caller skips the shared backends.
func (r *Reconciler) identityToken(ctx context.Context, clusterName string, st *aiv1alpha1.Studio) (string, error) {
	if !r.Identities.Enabled() {
		return "", nil
	}
	return r.Identities.Token(ctx, studioOwner(st, clusterName), studioIdentityRules(st))
}

// releaseIdentity revokes the Studio's identity on the delete path rather than
// waiting up to one token TTL for the hub's sweep to notice.
func (r *Reconciler) releaseIdentity(ctx context.Context, clusterName string, st *aiv1alpha1.Studio) error {
	if !r.Identities.Enabled() {
		return nil
	}
	return r.Identities.Release(ctx, studioOwner(st, clusterName))
}
