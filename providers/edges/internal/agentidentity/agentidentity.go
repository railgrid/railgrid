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

// Package agentidentity holds the scoped identity an edge agent authenticates
// with: the rule set asked of the hub, the owner tuple it is bound to, and the
// lifetime it gets.
//
// It is its own package because both halves of the provider need it and they
// cannot import each other: the reconciler (internal/edgectrl) keeps the
// identity in existence and revokes it on delete, while the tunnel
// (internal/tunnel) mints the token on join and re-mints it on the agent-token
// verb. One definition, so the rules a reconcile asserts and the rules a
// refresh asserts can never drift.
package agentidentity

import (
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	edgeapi "github.com/railgrid/provider-edges/internal/edgeapi"
	"github.com/railgrid/provider-sdk/identityclient"
)

// An edge agent's credential is a SCOPED IDENTITY the hub mints, not something
// this provider creates.
//
// What it used to be: a ServiceAccount, a shared ClusterRole, a binding and a
// legacy kubernetes.io/service-account-token Secret, all written into the
// tenant workspace with the provider's own claimed credentials — a token that
// never expired, that nothing collected, and whose rules were create-if-absent
// so a narrowed grant never actually narrowed. That is review finding M7.
//
// What it is now: one POST to the hub identity service
// (docs/provider-connectivity-contract.md §"Scoped identities"). The hub
// verifies the owner exists with that UID, checks every rule against its
// policy, records what it accepted in a workspace no tenant can reach,
// TokenRequest-mints a TTL'd token, and garbage-collects the identity when the
// edge stops existing. The provider therefore holds no serviceaccounts,
// clusterroles or clusterrolebindings claims at all.

// TokenTTL is the lifetime asked for. The hub caps it at 24 hours; we ask
// for the cap deliberately.
//
// A shorter TTL is not more secure here, it is just more brittle: the token
// cannot be refreshed except through a tunnel the token itself authorizes, so
// the TTL is also the longest an agent may be powered off and still able to
// come back on its own. See docs/edges-agent-credentials.md.
const TokenTTL = 24 * 60 * 60 // seconds; converted where the request is built

// DataPlaneVerbs are the data-plane verbs an edge's own agent is granted
// on its own edge object, expressed the way the contract spells a verb:
// "create" on the virtual subresource {resource}/{verb}, never a
// provider-invented verb string. The hub materializes every data-plane grant
// as exactly that rule (pkg/hub/serviceaccounts/workload_identity.go), and its
// identity policy will only mint one for a verb the owning provider declares
// (clause C), so this list and manifest.yaml's spec.dataPlane.verbs are the
// same list seen from two sides.
//
//   - proxy            the agent presents it on every reconnect (class (f))
//   - agent-token      the agent refreshes its own credential with it
//   - ssh-credentials  a LinuxServer agent hands its SSH credentials over
//   - k8s, ssh, mcp    the verbs carried OVER the tunnel; a grant that
//     authorizes the tunnel but not its traffic fails on the first request
var DataPlaneVerbs = []string{"agent-token", "k8s", "mcp", "proxy", "ssh", "ssh-credentials"}

// Rules is the complete rule set an edge agent's identity
// carries. Every rule is clause A — a verb on a resource of edges.railgrid.ai,
// the group this provider itself exports — so the hub can admit the whole
// request without any of it being a foreign-group escalation.
//
// Two things are deliberately NOT here, and their absence is the point of
// §5.7:
//
//   - No core group. The agent used to hold get/create on namespaces and
//     get/create/update on secrets, so it could write its own SSH credentials
//     into the tenant workspace. Identity policy X-4 refuses to mint a Secrets
//     rule for anyone, and rightly: it is the one rule that turns a scoped
//     identity into a general-purpose credential thief. That write is now a
//     gated verb (ssh-credentials) the PROVIDER performs after both gates.
//   - No wildcard and no unnamed write. The per-edge verbs are name-scoped to
//     this edge alone, so one edge's agent cannot act on another's.
func Rules(gvr schema.GroupVersionResource, edgeName string) []rbacv1.PolicyRule {
	subresources := make([]string, 0, len(DataPlaneVerbs))
	for _, verb := range DataPlaneVerbs {
		subresources = append(subresources, gvr.Resource+"/"+verb)
	}
	return []rbacv1.PolicyRule{
		// Gate 1 of every data-plane call the agent makes is a real GET of its
		// own edge, so the identity must be able to read exactly that object
		// and no other.
		{
			APIGroups:     []string{gvr.Group},
			Resources:     []string{gvr.Resource},
			ResourceNames: []string{edgeName},
			Verbs:         []string{"get"},
		},
		// Gate 2: the declared verbs, name-scoped.
		{
			APIGroups:     []string{gvr.Group},
			Resources:     subresources,
			ResourceNames: []string{edgeName},
			Verbs:         []string{"create"},
		},
		// The agent reads its own edge and patches its status (the status
		// reporter heartbeats connected/agentVersion/…), so it needs the
		// /status subresource on its own object. list/watch cannot be
		// name-scoped by Kubernetes RBAC, so they are asked for on the kind:
		// the agent watches its own edge for spec changes and cannot express
		// "watch this one".
		{
			APIGroups: []string{gvr.Group},
			Resources: []string{gvr.Resource},
			Verbs:     []string{"list", "watch"},
		},
		{
			APIGroups:     []string{gvr.Group},
			Resources:     []string{gvr.Resource + "/status"},
			ResourceNames: []string{edgeName},
			Verbs:         []string{"get", "update", "patch"},
		},
		// Workload plane (KubernetesCluster edges): the agent's workload
		// reconciler watches its Placements and reads the referenced Workload;
		// the placement reporter patches Placement status from the local
		// Deployment.
		{
			APIGroups: []string{gvr.Group},
			Resources: []string{"placements", "placements/status"},
			Verbs:     []string{"get", "list", "watch", "update", "patch"},
		},
		{
			APIGroups: []string{gvr.Group},
			Resources: []string{"workloads", "workloads/status"},
			Verbs:     []string{"get", "list", "watch"},
		},
		// Add-on plane (host edges): READ ONLY on the objects themselves — an
		// agent must never be able to create or delete an Addon, because
		// creating one is the privileged act that turns a machine into a
		// code-execution host. See docs/edge-addons.md.
		{
			APIGroups: []string{gvr.Group},
			Resources: []string{"addons"},
			Verbs:     []string{"get", "list", "watch"},
		},
		{
			APIGroups: []string{gvr.Group},
			Resources: []string{"addons/status"},
			Verbs:     []string{"get", "update", "patch"},
		},
	}
}

// Owner is the tenant object the identity exists for. The UID is
// part of it on purpose: the hub hashes the owner tuple INCLUDING the UID into
// the ServiceAccount name, so an edge that is deleted and recreated under the
// same name never inherits its predecessor's credential.
func Owner(gvr schema.GroupVersionResource, kind string, edge edgeapi.Connectable) identityclient.Owner {
	return identityclient.Owner{
		Provider: ProviderName,
		Kind:     kind,
		Group:    gvr.Group,
		Version:  gvr.Version,
		Resource: gvr.Resource,
		Name:     edge.GetName(),
		UID:      string(edge.GetUID()),
	}
}

// OwnerFor is Owner for a caller that holds the name
// and UID rather than the object (the tunnel's join path, which read the edge
// as the caller).
func OwnerFor(gvr schema.GroupVersionResource, kind, name, uid string) identityclient.Owner {
	return identityclient.Owner{
		Provider: ProviderName,
		Kind:     kind,
		Group:    gvr.Group,
		Version:  gvr.Version,
		Resource: gvr.Resource,
		Name:     name,
		UID:      uid,
	}
}

// ProviderName is how this provider's CatalogEntry registers it, and therefore
// the name the hub identity service checks the provider's own bearer against.
const ProviderName = "edges"
