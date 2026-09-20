// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package engagement

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"

	apiskcpv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"

	"sigs.k8s.io/yaml"

	"github.com/railgrid/provider-sdk/identityclient"
)

// fakeIdentityHub stands in for the hub identity service: it records every
// request and hands back a numbered token, so a test can tell a refresh from
// a re-mint and read back the rules that were asked for.
type fakeIdentityHub struct {
	mu      sync.Mutex
	posts   []map[string]any
	deletes []string
	mints   int
	// status, when non-zero, is returned instead of a token.
	status int
}

func (h *fakeIdentityHub) client(t *testing.T) *identityclient.Client {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc(identityclient.PathIdentities, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			h.mu.Lock()
			h.posts = append(h.posts, body)
			h.mints++
			mint, status := h.mints, h.status
			h.mu.Unlock()
			if status != 0 {
				w.WriteHeader(status)
				_ = json.NewEncoder(w).Encode(identityclient.Error{Code: "composition_not_granted", Message: "nobody accepted it"})
				return
			}
			writeJSON(w, identityclient.Token{
				Token: fmt.Sprintf("token-%d", mint), TokenType: "Bearer",
				ExpiresAt: time.Now().Add(time.Hour), ServiceAccount: "railgrid-si-abc", Name: "si-abc",
			})
		case http.MethodGet:
			writeJSON(w, struct {
				Items []identityclient.Identity `json:"items"`
			}{Items: []identityclient.Identity{{Name: "si-abc"}}})
		}
	})
	mux.HandleFunc(identityclient.PathIdentities+"/", func(w http.ResponseWriter, r *http.Request) {
		h.mu.Lock()
		h.deletes = append(h.deletes, r.URL.Path)
		h.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	client, err := identityclient.New(identityclient.Options{
		HubURL: server.URL, Provider: "kuery", Token: "provider-token",
	})
	if err != nil {
		t.Fatalf("identity client: %v", err)
	}
	return client
}

func (h *fakeIdentityHub) lastPost(t *testing.T) map[string]any {
	t.Helper()
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.posts) == 0 {
		t.Fatal("the hub was never asked for an identity")
	}
	return h.posts[len(h.posts)-1]
}

func (h *fakeIdentityHub) mintCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.mints
}

func writeJSON(w http.ResponseWriter, payload any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(payload)
}

// staticCredential is the test stand-in for a workspace identity: a fixed
// token and a record of what the watch observed.
type staticCredential struct {
	token       string
	err         error
	mu          sync.Mutex
	observed    []string
	forgotten   []string
	invalidated int
}

func (s *staticCredential) Token(context.Context) (string, error) { return s.token, s.err }
func (s *staticCredential) Invalidate() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.invalidated++
}

func (s *staticCredential) Observe(edge string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.observed = append(s.observed, edge)
}

func (s *staticCredential) Forget(edge string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.forgotten = append(s.forgotten, edge)
}

func (s *staticCredential) saw(edge string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Contains(s.observed, edge)
}

// observations counts how often the edge was named to the identity. Naming is
// the last thing that happens before a dial, so it counts engage ATTEMPTS.
func (s *staticCredential) observations(edge string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, seen := range s.observed {
		if seen == edge {
			n++
		}
	}
	return n
}

func (s *staticCredential) forgot(edge string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Contains(s.forgotten, edge)
}

func (s *staticCredential) invalidations() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.invalidated
}

// ruleFor finds the rule on (group, resource) with the given name scoping.
func ruleFor(rules []rbacv1.PolicyRule, resource string, named bool) (rbacv1.PolicyRule, bool) {
	for _, rule := range rules {
		if !slices.Contains(rule.Resources, resource) {
			continue
		}
		if named != (len(rule.ResourceNames) > 0) {
			continue
		}
		return rule, true
	}
	return rbacv1.PolicyRule{}, false
}

// The rule set is the CatalogEntry's declaration, split the only two ways RBAC
// can express it: the collection verbs unnamed (resourceNames do not apply to
// a list or a watch, so a named one would authorize nothing), the object verb
// and the data-plane verb name-scoped to the edges actually engaged.
func TestIdentityRulesMirrorTheDeclaredComposition(t *testing.T) {
	rules := identityRules([]string{"edge-b", "edge-a", "edge-b", ""})

	collection, ok := ruleFor(rules, edgesResource, false)
	if !ok {
		t.Fatalf("no unnamed rule on %s: %+v", edgesResource, rules)
	}
	if !slices.Equal(collection.APIGroups, []string{edgesAPIGroup}) {
		t.Fatalf("collection rule group = %v, want %v", collection.APIGroups, edgesAPIGroup)
	}
	if !slices.Equal(collection.Verbs, []string{"list", "watch"}) {
		t.Fatalf("collection verbs = %v, want list+watch (clause E, unnamed)", collection.Verbs)
	}

	object, ok := ruleFor(rules, edgesResource, true)
	if !ok {
		t.Fatalf("no named rule on %s: %+v", edgesResource, rules)
	}
	if !slices.Equal(object.Verbs, []string{"get"}) {
		t.Fatalf("object verbs = %v, want get only — kuery never writes an edge", object.Verbs)
	}
	if !slices.Equal(object.ResourceNames, []string{"edge-a", "edge-b"}) {
		t.Fatalf("object names = %v, want the engaged edges, deduplicated and sorted", object.ResourceNames)
	}

	dataPlane, ok := ruleFor(rules, edgesResource+"/"+edgeDataPlaneVerb, true)
	if !ok {
		t.Fatalf("no rule on the %s/%s coordinate: %+v", edgesResource, edgeDataPlaneVerb, rules)
	}
	if !slices.Equal(dataPlane.Verbs, []string{"create"}) {
		t.Fatalf("data-plane verbs = %v, want create (clause C mints nothing else)", dataPlane.Verbs)
	}
	if !slices.Equal(dataPlane.ResourceNames, []string{"edge-a", "edge-b"}) {
		t.Fatalf("data-plane names = %v, want the engaged edges", dataPlane.ResourceNames)
	}

	// Nothing on serviceaccounts, secrets or the RBAC types: this provider
	// does not mint identities, and the hub refuses the core group outright.
	for _, rule := range rules {
		for _, group := range rule.APIGroups {
			if group != edgesAPIGroup {
				t.Fatalf("rule on a foreign group %q: %+v", group, rule)
			}
		}
	}
}

// A workspace with no engaged edges asks only for the collection half:
// watching a workspace for objects that are about to exist is not access to
// any particular one, and a named rule with no names would read as "all".
func TestIdentityRulesWithoutEdgesAreCollectionOnly(t *testing.T) {
	rules := identityRules(nil)
	if len(rules) != 1 {
		t.Fatalf("rules = %+v, want only the unnamed collection rule", rules)
	}
	if len(rules[0].ResourceNames) != 0 {
		t.Fatalf("collection rule is name-scoped: %+v", rules[0])
	}
}

// The owner is the TENANT's kuery APIBinding. The hub re-reads the owner in
// the tenant workspace before it mints anything, so it has to be an object
// that lives there — kuery's own Engagement kind is provider-private and
// would never be found.
func TestIdentityOwnerIsTheTenantAPIBinding(t *testing.T) {
	const cluster = "btykuuy2789iyolq"
	binding := &apiskcpv1alpha2.APIBinding{ObjectMeta: metav1.ObjectMeta{Name: "kuery", UID: "b-1"}}

	owner := bindingOwner(binding, cluster)
	if owner.Kind != "APIBinding" || owner.Resource != "apibindings" {
		t.Fatalf("owner kind/resource = %s/%s, want APIBinding/apibindings", owner.Kind, owner.Resource)
	}
	if owner.Group != "apis.kcp.io" || owner.Version != "v1alpha2" {
		t.Fatalf("owner GV = %s/%s, want apis.kcp.io/v1alpha2", owner.Group, owner.Version)
	}
	if owner.Name != "kuery" || owner.UID != "b-1" || owner.ClusterID != cluster {
		t.Fatalf("owner = %+v, want the tenant's binding in %s", owner, cluster)
	}

	// The UID is part of the source key, so a recreated binding never inherits
	// its predecessor's credential.
	recreated := bindingOwner(&apiskcpv1alpha2.APIBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "kuery", UID: "b-2"},
	}, cluster)
	if ownerKey(owner) == ownerKey(recreated) {
		t.Fatal("two bindings with different UIDs share an identity key")
	}
}

// Engaging an edge changes the rules, which rebuilds the source, which
// re-states the WHOLE rule set to the hub. That is the half create-if-absent
// RBAC never had: losing an edge shrinks the grant.
func TestEngagedEdgeSetRebuildsTheSource(t *testing.T) {
	ctx := context.Background()
	hub := &fakeIdentityHub{}
	cache := newIdentityCache(hub.client(t))
	identity := newWorkspaceIdentity(cache, bindingOwner(
		&apiskcpv1alpha2.APIBinding{ObjectMeta: metav1.ObjectMeta{Name: "kuery", UID: "b-1"}}, "btykuuy2789iyolq"))

	if _, err := identity.Token(ctx); err != nil {
		t.Fatalf("first token: %v", err)
	}
	if got := postedNames(t, hub.lastPost(t)); len(got) != 0 {
		t.Fatalf("first mint named %v, want no edges yet", got)
	}

	// An unchanged edge set refreshes rather than re-mints: the source is
	// still valid, so the cached token is handed back.
	if _, err := identity.Token(ctx); err != nil {
		t.Fatalf("second token: %v", err)
	}
	if got := hub.mintCount(); got != 1 {
		t.Fatalf("mints = %d, want 1 (an unchanged rule set reuses the source)", got)
	}

	identity.Observe("edge-1")
	token, err := identity.Token(ctx)
	if err != nil {
		t.Fatalf("token after observing an edge: %v", err)
	}
	if token != "token-2" {
		t.Fatalf("token = %q, want a freshly minted one", token)
	}
	if got := postedNames(t, hub.lastPost(t)); !slices.Equal(got, []string{"edge-1"}) {
		t.Fatalf("mint named %v, want [edge-1]", got)
	}

	identity.Observe("edge-2")
	if _, err := identity.Token(ctx); err != nil {
		t.Fatalf("token after a second edge: %v", err)
	}
	if got := postedNames(t, hub.lastPost(t)); !slices.Equal(got, []string{"edge-1", "edge-2"}) {
		t.Fatalf("mint named %v, want both edges", got)
	}

	identity.Forget("edge-1")
	if _, err := identity.Token(ctx); err != nil {
		t.Fatalf("token after forgetting an edge: %v", err)
	}
	if got := postedNames(t, hub.lastPost(t)); !slices.Equal(got, []string{"edge-2"}) {
		t.Fatalf("mint named %v, want only the edge still there", got)
	}
}

// postedNames reads the resourceNames of the named rule the request carried.
func postedNames(t *testing.T, post map[string]any) []string {
	t.Helper()
	rules, _ := post["rules"].([]any)
	var out []string
	for _, raw := range rules {
		rule, _ := raw.(map[string]any)
		resources, _ := rule["resources"].([]any)
		if len(resources) == 0 || resources[0] != edgesResource {
			continue
		}
		names, _ := rule["resourceNames"].([]any)
		for _, name := range names {
			if value, ok := name.(string); ok {
				out = append(out, value)
			}
		}
	}
	return out
}

// Release revokes the identity instead of leaving it to the hub's sweep, and
// drops the cached source so a workspace that re-enables kuery mints fresh.
func TestReleaseRevokesTheIdentity(t *testing.T) {
	ctx := context.Background()
	hub := &fakeIdentityHub{}
	cache := newIdentityCache(hub.client(t))
	identity := newWorkspaceIdentity(cache, bindingOwner(
		&apiskcpv1alpha2.APIBinding{ObjectMeta: metav1.ObjectMeta{Name: "kuery", UID: "b-1"}}, "btykuuy2789iyolq"))

	if _, err := identity.Token(ctx); err != nil {
		t.Fatalf("token: %v", err)
	}
	if err := identity.Release(ctx); err != nil {
		t.Fatalf("release: %v", err)
	}
	hub.mu.Lock()
	deletes := len(hub.deletes)
	hub.mu.Unlock()
	if deletes != 1 {
		t.Fatalf("deletes = %d, want 1", deletes)
	}
	if _, err := identity.Token(ctx); err != nil {
		t.Fatalf("token after release: %v", err)
	}
	if got := hub.mintCount(); got != 2 {
		t.Fatalf("mints = %d, want a fresh mint after release", got)
	}
}

// A hub refusal is surfaced, not swallowed: without an identity this
// workspace's edges cannot be read at all, and the reconcile must say so.
func TestTokenFailureIsReported(t *testing.T) {
	hub := &fakeIdentityHub{status: http.StatusForbidden}
	cache := newIdentityCache(hub.client(t))
	identity := newWorkspaceIdentity(cache, bindingOwner(
		&apiskcpv1alpha2.APIBinding{ObjectMeta: metav1.ObjectMeta{Name: "kuery", UID: "b-1"}}, "c"))

	if _, err := identity.Token(context.Background()); err == nil {
		t.Fatal("a refused mint must be an error")
	}

	// No identity client at all is the same answer, not a silent no-op: a
	// replica that cannot mint must not look like one syncing nothing.
	none := newWorkspaceIdentity(nil, identityclient.Owner{})
	if _, err := none.Token(context.Background()); err == nil {
		t.Fatal("a controller without a hub identity client must fail loudly")
	}
}

// Every request carries a freshly resolved token — an engaged edge's informers
// outlive any one of them — and a rejected token invalidates the source so the
// next request re-Ensures rather than replaying a credential the server has
// already refused.
func TestIdentityTransportRefreshesAndReEnsuresOnRejection(t *testing.T) {
	status := http.StatusOK
	var seen []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Header.Get("Authorization"))
		w.WriteHeader(status)
	}))
	t.Cleanup(server.Close)

	id := &staticCredential{token: "token-1"}
	cfg := identityTransport(&rest.Config{Host: server.URL}, id)
	client, err := rest.HTTPClientFor(cfg)
	if err != nil {
		t.Fatalf("http client: %v", err)
	}

	if _, err := client.Get(server.URL); err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(seen) != 1 || seen[0] != "Bearer token-1" {
		t.Fatalf("Authorization = %v, want the identity's token", seen)
	}
	if id.invalidations() != 0 {
		t.Fatal("a successful request must not invalidate the identity")
	}

	// The token rotates underneath the same config, with no re-dial.
	id.token = "token-2"
	if _, err := client.Get(server.URL); err != nil {
		t.Fatalf("get: %v", err)
	}
	if seen[1] != "Bearer token-2" {
		t.Fatalf("Authorization = %q, want the refreshed token", seen[1])
	}

	for _, code := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		status = code
		if _, err := client.Get(server.URL); err != nil {
			t.Fatalf("get: %v", err)
		}
	}
	if got := id.invalidations(); got != 2 {
		t.Fatalf("invalidations = %d, want one per rejection", got)
	}
}

// catalogEntry is the slice of the CatalogEntry these tests care about.
type catalogEntry struct {
	Spec struct {
		APIExport *struct {
			PermissionClaims []map[string]any `json:"permissionClaims"`
		} `json:"apiExport"`
		Dependencies []struct {
			Name     string `json:"name"`
			Composes []struct {
				Group    string   `json:"group"`
				Resource string   `json:"resource"`
				Verbs    []string `json:"verbs"`
			} `json:"composes"`
		} `json:"dependencies"`
	} `json:"spec"`
}

// The rules identityRules builds and the composition the CatalogEntry declares
// are one contract with two spellings. The hub admits a clause-E rule only
// when a declared composition covers it, so a verb asked for here that the
// manifest does not declare is an identity request refused in full — and a
// verb declared but never asked for is a grant nobody needed.
//
// The comparison is on the UNION of verbs across both rule shapes, because the
// split into a collection rule and a named one is an RBAC mechanic
// (resourceNames do not apply to a collection request), not part of the
// declaration. The clause-C subresource is deliberately not compared: it is
// the EDGES provider's declaration (spec.dataPlane.verbs), not kuery's.
func TestIdentityRulesMatchTheManifest(t *testing.T) {
	declared := declaredComposition(t)

	composed := map[string][]string{}
	for _, rule := range identityRules([]string{"edge-1"}) {
		key := rule.APIGroups[0] + "/" + rule.Resources[0]
		if strings.Contains(rule.Resources[0], "/") {
			continue // clause C, declared by the owning provider
		}
		composed[key] = append(composed[key], rule.Verbs...)
	}

	for key, verbs := range composed {
		want, ok := declared[key]
		if !ok {
			t.Fatalf("%s is composed in Go but not declared in manifest.yaml", key)
		}
		if got := normalizeVerbs(verbs); got != want {
			t.Fatalf("%s: Go composes [%s], the manifest declares [%s]", key, got, want)
		}
	}
	for key := range declared {
		if _, ok := composed[key]; !ok {
			t.Fatalf("%s is declared in manifest.yaml but nothing composes it", key)
		}
	}
}

// The manifest and the chart's copy are two renderings of one declaration, and
// the chart's is the one that reaches production. A composition that drifts
// between them is a consent prompt that does not match what the hub will mint.
func TestChartDeclaresTheSameComposition(t *testing.T) {
	chart := readFixture(t, "deploy", "chart", "templates", "catalogentry.yaml")
	for _, want := range []string{
		"      dependencies:",
		"        - name: edges",
		"          composes:",
		"            - group: edges.railgrid.ai",
		"              resource: kubernetesclusters",
		`              verbs: ["get", "list", "watch"]`,
	} {
		if !strings.Contains(chart, want) {
			t.Fatalf("the chart's CatalogEntry is missing %q; it must declare the same composition as manifest.yaml", want)
		}
	}
	// And neither may grow a claim back: a provider does not mint identities.
	// Comments are stripped first — the ones explaining WHY there are no
	// claims naturally name the types there are no claims on.
	for _, line := range strings.Split(chart, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		if strings.Contains(line, "permissionClaims") {
			t.Fatalf("the chart's CatalogEntry declares permissionClaims (%q); kuery claims nothing", strings.TrimSpace(line))
		}
	}
}

// Kuery claims nothing at all. A claim on serviceaccounts, secrets,
// clusterroles or clusterrolebindings is a contract violation
// (docs/provider-connectivity-contract.md §"Scoped identities"), and a
// first-party claim pins one identityHash for every consumer at once.
func TestManifestClaimsNothing(t *testing.T) {
	var entry catalogEntry
	if err := yaml.Unmarshal([]byte(readFixture(t, "manifest.yaml")), &entry); err != nil {
		t.Fatalf("decode manifest.yaml: %v", err)
	}
	if entry.Spec.APIExport == nil {
		t.Fatal("manifest.yaml declares no apiExport")
	}
	if len(entry.Spec.APIExport.PermissionClaims) != 0 {
		t.Fatalf("manifest.yaml claims %v; kuery must claim nothing", entry.Spec.APIExport.PermissionClaims)
	}
}

func declaredComposition(t *testing.T) map[string]string {
	t.Helper()
	var entry catalogEntry
	if err := yaml.Unmarshal([]byte(readFixture(t, "manifest.yaml")), &entry); err != nil {
		t.Fatalf("decode manifest.yaml: %v", err)
	}
	out := map[string]string{}
	for _, dependency := range entry.Spec.Dependencies {
		for _, composed := range dependency.Composes {
			if composed.Group == "" {
				t.Fatalf("dependency %q composes a core-group resource: %+v", dependency.Name, composed)
			}
			out[composed.Group+"/"+composed.Resource] = normalizeVerbs(composed.Verbs)
		}
	}
	if len(out) == 0 {
		t.Fatal("manifest.yaml declares no composition; the engagement identity would be refused in every workspace")
	}
	return out
}

func readFixture(t *testing.T, parts ...string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(append([]string{".."}, parts...)...))
	if err != nil {
		t.Fatalf("read %v: %v", parts, err)
	}
	return string(raw)
}

func normalizeVerbs(verbs []string) string {
	copied := append([]string(nil), verbs...)
	sort.Strings(copied)
	out := copied[:0]
	for i, verb := range copied {
		if i == 0 || verb != copied[i-1] {
			out = append(out, verb)
		}
	}
	return strings.Join(out, ",")
}
