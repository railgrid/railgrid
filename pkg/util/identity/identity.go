/*
Copyright 2026 The Faros Authors.

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

// Package identity builds the cross-workspace ServiceAccount identity kedge
// authorizes foreign SAs under.
//
// kcp ServiceAccount usernames ("system:serviceaccount:{ns}:{name}") are
// logical-cluster-scoped — the same string means a DIFFERENT principal in
// every workspace, so binding RBAC to it in a tenant workspace would also
// match a same-named SA the tenant created themselves.
//
// kcp already defines the disambiguated form: the GlobalServiceAccount
// feature gate (beta, default-on since kube 1.35 in the kcp fork) makes
// kcp's RBAC resolution alias every SA to
//
//	system:kcp:serviceaccount:{cluster}:{ns}:{name}
//
// (see EffectiveUsers in the fork's pkg/registry/rbac/validation/kcp.go and
// kcp's e2e TestAPIResourceSchemaVirtualWorkspaceAuthorization, which binds
// exactly this subject across workspaces). Kedge emits the same format —
// after verifying the home cluster via TokenReview — so the grant objects
// created on tenant Enable use kcp-native subjects: the same binding that
// satisfies kedge's delegated SAR also authorizes the SA on kcp-native
// paths.
//
// Both sides — the SAR caller in pkg/virtual/builder and the grant writer in
// pkg/hub/kcp — MUST use these helpers so the encodings cannot drift.
package identity

import "strings"

// globalSAPrefix is kcp's cross-workspace ServiceAccount username prefix.
// kcp's authenticators never mint usernames under it directly (it is an
// RBAC-resolution alias), so kedge synthesizing it after a successful
// TokenReview cannot collide with or be forged through any token.
const globalSAPrefix = "system:kcp:serviceaccount:"

// QualifyServiceAccount converts a TokenReview-resolved ServiceAccount
// username ("system:serviceaccount:{ns}:{name}") plus its verified home
// logical cluster into kcp's global form
// "system:kcp:serviceaccount:{homeCluster}:{ns}:{name}".
//
// Returns false when saUsername is not a ServiceAccount username.
func QualifyServiceAccount(homeCluster, saUsername string) (string, bool) {
	rest, ok := strings.CutPrefix(saUsername, "system:serviceaccount:")
	if !ok || homeCluster == "" || rest == "" {
		return "", false
	}
	return globalSAPrefix + homeCluster + ":" + rest, true
}

// QualifiedServiceAccount builds the global username from parts. Used by
// the grant writer, which knows the SA's coordinates rather than holding a
// TokenReview result.
func QualifiedServiceAccount(homeCluster, namespace, name string) string {
	return globalSAPrefix + homeCluster + ":" + namespace + ":" + name
}
