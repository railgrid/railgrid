// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package engagement

// The engagement identity: ONE hub-minted, TTL'd credential per workspace that
// enabled kuery, replacing the per-workspace ServiceAccount, ClusterRole,
// ClusterRoleBinding and never-expiring token Secret this provider used to
// write into every tenant workspace with its own claimed credentials.
//
//	Before                                   Now
//	------                                   ---
//	tenantaccess.EnsureIdentity wrote a      The hub mints it, against a policy
//	ServiceAccount, a ClusterRole, a         that checks every rule, records
//	binding and a legacy token Secret,       what it issued, and collects it
//	through serviceaccounts/secrets/         when the owning APIBinding is
//	clusterroles/clusterrolebindings         gone.
//	permission claims.
//
//	The rules were create-if-absent, so a    Every refresh re-states the rules,
//	grant never shrank — and could never     so an edge that disappears takes
//	be widened either, which is why the      its grant with it and a new one
//	data-plane grant had to be a second,     arrives without a second object.
//	separately named object.
//
//	The token never expired.                 TTL'd, re-minted at 80% of its
//	                                         life, dropped on a 401/403.
//
// Kuery therefore holds NO serviceaccounts, secrets, clusterroles or
// clusterrolebindings permission claims — a claim on those types is a contract
// violation, not a design choice (docs/provider-connectivity-contract.md
// §"Scoped identities").
//
// # Why not a permission claim on edges.railgrid.ai instead
//
// Because a claim on a first-party (*.railgrid.ai) group has to pin the
// serving APIExport's identityHash, and an export pins exactly one identity
// per claimed resource for EVERY consuming workspace at once (AGENTS.md §5.7).
// The moment one org self-hosts the edges provider while others use the
// platform copy, no single pin is correct and kcp silently serves the
// mismatching workspaces nothing. A COMPOSITION names the dependency by name,
// is resolved per workspace through whatever export is bound there, and is
// revocable by the tenant — which is what keeps an org-owned edges provider
// working. The CatalogEntry declares it (manifest.yaml and the chart's
// catalogentry.yaml, identically):
//
//	dependencies:
//	  - name: edges
//	    composes:
//	      - group: edges.railgrid.ai
//	        resource: kubernetesclusters
//	        verbs: [get, list, watch]
//
// and the hub's identity policy (pkg/hub/identity/policy.go, clause E) admits
// a rule only when that declaration covers it AND a workspace or org admin
// accepted it here.

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"

	rbacv1 "k8s.io/api/rbac/v1"
	utilnet "k8s.io/apimachinery/pkg/util/net"
	"k8s.io/client-go/rest"

	apiskcpv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"

	"github.com/railgrid/provider-sdk/identityclient"
)

const (
	// edgesAPIGroup is the edges provider's API group. It is a FOREIGN group:
	// nothing here is a claim on it.
	edgesAPIGroup = "edges.railgrid.ai"
	// edgesResource is the one edge kind kuery engages. LinuxServer and
	// MacOSServer edges carry no Kubernetes API to sync, so they are neither
	// watched (edgeGVK) nor composed.
	edgesResource = "kubernetesclusters"
	// edgeDataPlaneVerb is the edges provider's declared data-plane verb for
	// the per-edge Kubernetes API (providers/edges/manifest.yaml
	// spec.dataPlane.verbs). Clause C mints `create` on {resource}/{verb}
	// only for a verb the OWNING provider declares, so this string is checked
	// against that declaration on every mint rather than merely believed.
	edgeDataPlaneVerb = "k8s"
)

// The two rule shapes, because Kubernetes RBAC has two:
//
//   - COLLECTION verbs take no resourceNames at all — a rule with names and
//     verb `list` authorizes nothing — so the edge watch's list and watch are
//     unnamed, bounded instead by the declared (group, resource) pair and by
//     the one workspace this identity lives in.
//   - OBJECT verbs are name-scoped, to the edges this provider actually
//     engaged. `get` is not decoration: the edges data plane's first gate is a
//     real GET of the addressed edge AS THE CALLER, so an identity that cannot
//     see the object cannot invoke a verb on it.
var (
	edgeCollectionVerbs = []string{"list", "watch"}
	edgeObjectVerbs     = []string{"get"}
)

// bindingOwner is the tuple the hub verifies before it mints anything.
//
// The owner is the tenant's kuery APIBinding, not kuery's own Engagement:
// the hub re-reads the owner object in clusterID — the TENANT workspace — with
// its own kcp credential (pkg/hub/identity/attestation.go DynamicOwnerProbe),
// and the Engagement is a provider-private kind that lives in kuery's own
// workspace and would never be found there. The APIBinding is also exactly the
// right lifetime: it IS "this workspace has kuery enabled", it is cluster-
// scoped (one dynamic Get is enough), and Disable deletes it, at which point
// the hub's sweep collects the identity even if this provider never gets to
// call Release.
func bindingOwner(binding *apiskcpv1alpha2.APIBinding, clusterID string) identityclient.Owner {
	return identityclient.Owner{
		Kind:      "APIBinding",
		Group:     apiskcpv1alpha2.SchemeGroupVersion.Group,
		Version:   apiskcpv1alpha2.SchemeGroupVersion.Version,
		Resource:  "apibindings",
		Name:      binding.Name,
		UID:       string(binding.UID),
		ClusterID: clusterID,
	}
}

// identityRules builds the exact rules one workspace's engagement needs, and
// nothing else. Three, in two clauses:
//
//   - E (composition): unnamed `list`/`watch` on kubernetesclusters — the edge
//     watch, which is the authority on which edges exist and which are
//     connected — plus `get` on the edges actually engaged, by name.
//   - C (foreign verb): `create` on kubernetesclusters/k8s for those same
//     edges, by name. That is how the data plane expresses "may run this verb
//     on this one", and it is the whole authorization story for the per-edge
//     Kubernetes API.
//
// Nothing on kuery's own group: SavedViews and the private Engagement records
// are reached over kuery's own APIExport virtual workspace and its own
// workspace, where it is already the owner of the kind.
//
// A workspace with no engaged edges gets the unnamed half alone: watching a
// workspace for the objects that are about to exist is not access to any
// particular one.
func identityRules(edges []string) []rbacv1.PolicyRule {
	rules := []rbacv1.PolicyRule{{
		APIGroups: []string{edgesAPIGroup},
		Resources: []string{edgesResource},
		Verbs:     edgeCollectionVerbs,
	}}
	names := sortedNames(edges)
	if len(names) == 0 {
		return rules
	}
	return append(rules,
		rbacv1.PolicyRule{
			APIGroups:     []string{edgesAPIGroup},
			Resources:     []string{edgesResource},
			ResourceNames: names,
			Verbs:         edgeObjectVerbs,
		},
		rbacv1.PolicyRule{
			APIGroups:     []string{edgesAPIGroup},
			Resources:     []string{edgesResource + "/" + edgeDataPlaneVerb},
			ResourceNames: names,
			Verbs:         []string{"create"},
		},
	)
}

// credential is what the edge watch and the per-edge data path present: a
// token that is always fresh, plus the engaged-edge set whose names the
// token's rules carry. *workspaceIdentity is the production implementation;
// tests substitute a static one.
type credential interface {
	// Token returns a valid bearer, minting or refreshing through the hub.
	Token(ctx context.Context) (string, error)
	// Invalidate drops the token in hand so the next Token re-mints. Called
	// when the server rejects it.
	Invalidate()
	// Observe records that this workspace engages edge, so the next mint
	// names it.
	Observe(edge string)
	// Forget drops an edge that is gone, so the next mint stops naming it.
	Forget(edge string)
}

// identityCache holds one refreshing TokenSource per owner, rebuilt whenever
// the rules change. The fingerprint is what makes "the engaged-edge set
// changed" and "the token expired" two different events: a rule change
// rebuilds the source (so the next Token re-states the whole rule set to the
// hub), while an unchanged set just refreshes at 80% of the token's life.
type identityCache struct {
	client *identityclient.Client

	mu      sync.Mutex
	sources map[string]*cachedSource
}

type cachedSource struct {
	source      *identityclient.TokenSource
	fingerprint string
}

func newIdentityCache(client *identityclient.Client) *identityCache {
	if client == nil {
		return nil
	}
	return &identityCache{client: client, sources: map[string]*cachedSource{}}
}

func (c *identityCache) enabled() bool { return c != nil && c.client != nil }

func (c *identityCache) token(ctx context.Context, owner identityclient.Owner, rules []rbacv1.PolicyRule) (string, error) {
	if !c.enabled() {
		return "", fmt.Errorf("the hub identity service is not configured")
	}
	token, err := c.sourceFor(owner, rules).Token(ctx)
	if err != nil {
		return "", err
	}
	return token.Token, nil
}

func (c *identityCache) sourceFor(owner identityclient.Owner, rules []rbacv1.PolicyRule) *identityclient.TokenSource {
	key := ownerKey(owner)
	print := fingerprint(rules)

	c.mu.Lock()
	defer c.mu.Unlock()
	if existing, ok := c.sources[key]; ok && existing.fingerprint == print {
		return existing.source
	}
	source := identityclient.NewTokenSource(c.client, identityclient.Request{
		Owner:     owner,
		ClusterID: owner.ClusterID,
		Rules:     rules,
	})
	c.sources[key] = &cachedSource{source: source, fingerprint: print}
	return source
}

func (c *identityCache) invalidate(owner identityclient.Owner) {
	if !c.enabled() {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.sources, ownerKey(owner))
}

// release revokes an owner's identity now rather than waiting for the hub's
// sweep to notice the APIBinding is gone (up to one token TTL later).
func (c *identityCache) release(ctx context.Context, owner identityclient.Owner) error {
	if !c.enabled() {
		return nil
	}
	c.invalidate(owner)
	return c.client.Release(ctx, owner.ClusterID, owner)
}

// workspaceIdentity is one enabled workspace's engagement credential: the
// APIBinding it belongs to, the edges it currently engages, and the token the
// hub mints for that pair.
type workspaceIdentity struct {
	cache *identityCache
	owner identityclient.Owner

	mu    sync.Mutex
	edges map[string]bool
}

func newWorkspaceIdentity(cache *identityCache, owner identityclient.Owner) *workspaceIdentity {
	return &workspaceIdentity{cache: cache, owner: owner, edges: map[string]bool{}}
}

// Observe adds an edge to the set the rules name. A disconnected edge keeps
// its place: a name-scoped read of an edge that still exists is what kuery
// would be granted anyway, and dropping it would re-mint on every flap.
func (w *workspaceIdentity) Observe(edge string) {
	if w == nil || edge == "" {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.edges[edge] = true
}

// Forget removes an edge that is gone from the workspace entirely, so the next
// refresh re-states the rules without it. This is the half create-if-absent
// RBAC could never do: a grant that shrinks.
func (w *workspaceIdentity) Forget(edge string) {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	delete(w.edges, edge)
}

// Token returns a valid bearer for this workspace. The rules are recomputed
// from the current edge set on every call, so engaging or losing an edge
// rebuilds the source and the next token carries the new grant.
func (w *workspaceIdentity) Token(ctx context.Context) (string, error) {
	if w == nil {
		return "", fmt.Errorf("no engagement identity for this workspace")
	}
	w.mu.Lock()
	edges := make([]string, 0, len(w.edges))
	for edge := range w.edges {
		edges = append(edges, edge)
	}
	w.mu.Unlock()
	return w.cache.token(ctx, w.owner, identityRules(edges))
}

// Invalidate drops the cached token so the next Token re-Ensures with the hub.
func (w *workspaceIdentity) Invalidate() {
	if w == nil {
		return
	}
	w.cache.invalidate(w.owner)
}

// Release revokes the identity: the workspace disabled kuery, so the token
// should stop working now rather than at the end of its TTL.
func (w *workspaceIdentity) Release(ctx context.Context) error {
	if w == nil {
		return nil
	}
	return w.cache.release(ctx, w.owner)
}

// identityTransport wraps cfg so every request carries a token from the
// identity, and a rejected token is dropped rather than retried forever.
//
// The token is resolved PER REQUEST rather than pinned into
// rest.Config.BearerToken, because a hub-minted token expires: a long-running
// watch or an engaged edge's informer holding a one-hour bearer would simply
// stop working an hour in, which is exactly what the never-expiring
// ServiceAccount token used to hide. A 401 or 403 invalidates the source, so
// the next request re-Ensures — the identity may have been revoked, or the
// composition withdrawn, and re-minting is how this loop finds out.
func identityTransport(cfg *rest.Config, id credential) *rest.Config {
	out := rest.CopyConfig(cfg)
	out.Wrap(func(next http.RoundTripper) http.RoundTripper {
		return &identityRoundTripper{next: next, identity: id}
	})
	return out
}

type identityRoundTripper struct {
	next     http.RoundTripper
	identity credential
}

func (t *identityRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	token, err := t.identity.Token(req.Context())
	if err != nil {
		return nil, fmt.Errorf("minting the engagement identity token: %w", err)
	}
	// Clone before touching headers: a RoundTripper must not mutate the
	// request it is handed (client-go's own bearer transport does the same).
	req = utilnet.CloneRequest(req)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := t.next.RoundTrip(req)
	if err == nil && resp != nil &&
		(resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden) {
		t.identity.Invalidate()
	}
	return resp, err
}

// tenantRESTConfig is the workspace-scoped config the edge watch dials: the
// hub's kcp proxy at {hubBase}/clusters/{clusterID}, authenticated per request
// as the workspace's engagement identity. The proxy forwards to kcp as that
// identity, so the workspace's OWN edges binding serves the edges — whichever
// copy of the edges provider it bound.
func tenantRESTConfig(hubBase, clusterID string, id credential, insecure bool) (*rest.Config, error) {
	if strings.TrimSpace(hubBase) == "" {
		return nil, fmt.Errorf("hub base URL is empty (RAILGRID_HUB_URL)")
	}
	if strings.TrimSpace(clusterID) == "" {
		return nil, fmt.Errorf("workspace cluster id is empty")
	}
	cfg := &rest.Config{Host: strings.TrimRight(hubBase, "/") + "/clusters/" + clusterID}
	if insecure {
		cfg.TLSClientConfig = rest.TLSClientConfig{Insecure: true}
	}
	return identityTransport(cfg, id), nil
}

// ownerKey identifies one owner's source. The UID is part of it so a deleted
// and recreated APIBinding never inherits its predecessor's token.
func ownerKey(owner identityclient.Owner) string {
	return owner.ClusterID + "/" + owner.Kind + "/" + owner.Name + "/" + owner.UID
}

// fingerprint renders a rule set so two identical ones compare equal. Rules
// are built in a fixed order from a sorted name list, so this is a plain
// join rather than a canonicalization.
func fingerprint(rules []rbacv1.PolicyRule) string {
	parts := make([]string, 0, len(rules))
	for _, rule := range rules {
		parts = append(parts, strings.Join(rule.APIGroups, ",")+"|"+
			strings.Join(rule.Resources, ",")+"|"+
			strings.Join(rule.ResourceNames, ",")+"|"+
			strings.Join(rule.Verbs, ","))
	}
	return strings.Join(parts, ";")
}

func sortedNames(names []string) []string {
	seen := make(map[string]bool, len(names))
	out := make([]string, 0, len(names))
	for _, name := range names {
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}
