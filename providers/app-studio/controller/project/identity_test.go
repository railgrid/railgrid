/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package project

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/railgrid/provider-sdk/identityclient"

	aiv1alpha1 "github.com/railgrid/provider-app-studio/apis/ai/v1alpha1"
	"github.com/railgrid/provider-app-studio/internal/scopedidentity"
)

// These are the expected capability coordinates, kept local to the tests so
// assertions do not share implementation constants with projectidentity.
const (
	infraAPIGroup = "infrastructure.railgrid.ai"
	codeAPIGroup  = "code.railgrid.ai"
)

var (
	instanceDataPlaneVerbs = []string{"env", "exec", "log", "process", "proxy", "restart", "sync", "workspace"}
	codeRepositoryActions  = []string{"commit", "stage-commit-bundle"}
)

// fakeIdentityHub stands in for the hub identity service: it records what it
// was asked for and hands back a token per request.
type fakeIdentityHub struct {
	mu           sync.Mutex
	posts        []map[string]any
	deletes      []string
	mints        int
	responses    []fakeIdentityHubResponse
	onStaleOwner func()
}

type fakeIdentityHubResponse struct {
	status  int
	code    string
	message string
}

func (h *fakeIdentityHub) server(t *testing.T) *identityclient.Client {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc(identityclient.PathIdentities, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			h.mu.Lock()
			h.posts = append(h.posts, body)
			var response fakeIdentityHubResponse
			if len(h.responses) > 0 {
				response = h.responses[0]
				h.responses = h.responses[1:]
			}
			if response.status != 0 {
				staleOwner := response.code == identityclient.ErrorCodeStaleOwner
				onStaleOwner := h.onStaleOwner
				h.mu.Unlock()
				if staleOwner && onStaleOwner != nil {
					onStaleOwner()
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(response.status)
				_ = json.NewEncoder(w).Encode(identityclient.Error{Code: response.code, Message: response.message})
				return
			}
			h.mints++
			mint := h.mints
			h.mu.Unlock()
			token := identityclient.Token{
				Token: fmt.Sprintf("token-%d", mint), TokenType: "Bearer",
				ExpiresAt: time.Now().Add(time.Hour), ServiceAccount: "railgrid-si-abc", Name: "si-abc",
			}
			if _, versioned := body["expectedOwnerGeneration"]; versioned {
				token.OwnerRevisionVerified = true
			}
			writeIdentityJSON(w, token)
		case http.MethodGet:
			writeIdentityJSON(w, struct {
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
		HubURL: server.URL, Provider: "app-studio", Token: "provider-token",
	})
	if err != nil {
		t.Fatalf("identity client: %v", err)
	}
	return client
}

func writeIdentityJSON(w http.ResponseWriter, payload any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(payload)
}

// boundProject is a project wired the way a created one is: one development
// instance, one production instance, a repository and the connection it was
// created from.
func boundProject() *aiv1alpha1.Project {
	values := func(name string) runtime.RawExtension {
		raw, _ := json.Marshal(map[string]any{"name": name})
		return runtime.RawExtension{Raw: raw}
	}
	ref := func(name string) *aiv1alpha1.ProjectProviderResourceReference {
		return &aiv1alpha1.ProjectProviderResourceReference{
			Name: name, APIVersion: "infrastructure.railgrid.ai/v1alpha1", Kind: "Instance", Resource: "instances",
		}
	}
	return &aiv1alpha1.Project{
		ObjectMeta: metav1.ObjectMeta{Name: "demo", UID: "project-uid", Generation: 1, ResourceVersion: "rv-1"},
		Spec: aiv1alpha1.ProjectSpec{
			Repository: &aiv1alpha1.ProjectRepositoryBinding{RepositoryRef: "demo-repo", ConnectionRef: "github-main"},
			Environments: []aiv1alpha1.ProjectEnvironmentSpec{
				{Name: "development", Bindings: []aiv1alpha1.ProjectProviderBindingSpec{{
					Name: "dev", Provider: "app-studio", Kind: aiv1alpha1.ProjectBindingKindProviderResource,
					ResourceRef: ref("demo-dev"), Values: values("demo-dev"),
				}}},
				{Name: "production", Bindings: []aiv1alpha1.ProjectProviderBindingSpec{{
					Name: "prod", Provider: "app-studio", Kind: aiv1alpha1.ProjectBindingKindProviderResource,
					ResourceRef: ref("demo-prod"), Values: values("demo-prod"),
				}}},
			},
		},
	}
}

// ruleFor finds the rule on (group, resource) with the given name scoping:
// named=false is the unnamed collection half of a composition, named=true the
// object half. Both exist on the same coordinate, and confusing them is the
// whole failure mode this file guards.
func ruleFor(rules []rbacv1.PolicyRule, group, resource string, named bool) (rbacv1.PolicyRule, bool) {
	for _, rule := range rules {
		if len(rule.APIGroups) != 1 || rule.APIGroups[0] != group {
			continue
		}
		if len(rule.Resources) != 1 || rule.Resources[0] != resource {
			continue
		}
		if (len(rule.ResourceNames) > 0) != named {
			continue
		}
		return rule, true
	}
	return rbacv1.PolicyRule{}, false
}

func verbs(rule rbacv1.PolicyRule) string { return strings.Join(rule.Verbs, ",") }

func namesOf(rule rbacv1.PolicyRule) string { return strings.Join(rule.ResourceNames, ",") }

// The rules are the contract with the hub's policy: anything outside the
// admitted shapes refuses the WHOLE request, so each one is pinned here.
//
// The two shapes a composition takes are the point. Kubernetes RBAC ignores
// resourceNames on a collection request, so `create`, `list` and `watch` can
// only ever be unnamed — bounded by the declared (group, resource) instead —
// while every object verb is name-scoped to what this project is bound to.
func TestProjectIdentityRulesCarryTheDeclaredComposition(t *testing.T) {
	rules := projectIdentityRules(boundProject())

	// Nothing on this provider's own group. The Project is read and written
	// over the APIExport virtual workspace, where this provider owns the kind.
	if rule, ok := ruleFor(rules, "ai.railgrid.ai", "projects", false); ok {
		t.Fatalf("the identity holds a rule on the provider's own group: %#v", rule)
	}
	if rule, ok := ruleFor(rules, "ai.railgrid.ai", "projects", true); ok {
		t.Fatalf("the identity holds a rule on the provider's own group: %#v", rule)
	}

	// Clause D: the aggregate's door, and where a dependency answers.
	mcp, ok := ruleFor(rules, "railgrid.ai", "mcpservers", true)
	if !ok || verbs(mcp) != "use" || namesOf(mcp) != "default" {
		t.Fatalf("clause D MCP grant = %#v (ok=%v)", mcp, ok)
	}
	apibindings, ok := ruleFor(rules, "apis.kcp.io", "apibindings", true)
	if !ok || verbs(apibindings) != "get" || namesOf(apibindings) != "code,infrastructure" {
		t.Fatalf("clause D APIBinding grant = %#v (ok=%v)", apibindings, ok)
	}

	// No composition rule of any shape: the composed kinds ride the export
	// virtual workspace as the provider. The identity keeps a clause-B named
	// read of each object whose data plane it calls, for that plane's gate 1.
	for _, tc := range []struct {
		group, resource, names string
	}{
		{infraAPIGroup, "instances", "demo-dev,demo-prod"},
		{codeAPIGroup, "repositories", "demo-repo"},
	} {
		if rule, ok := ruleFor(rules, tc.group, tc.resource, false); ok {
			t.Fatalf("%s carries an unnamed rule %#v; collection access rides the export virtual workspace, not the identity", tc.resource, rule)
		}
		object, ok := ruleFor(rules, tc.group, tc.resource, true)
		if !ok || verbs(object) != "get" || namesOf(object) != tc.names {
			t.Fatalf("%s named read = %#v (ok=%v), want get on %s", tc.resource, object, ok, tc.names)
		}
	}
	if rule, ok := ruleFor(rules, codeAPIGroup, "repositorycommits", false); ok {
		t.Fatalf("repositorycommits carries an unnamed rule %#v; the commit is read as the provider", rule)
	}

	// Clause C: one create per declared data-plane verb, on the bound
	// instances by name.
	for _, verb := range instanceDataPlaneVerbs {
		rule, ok := ruleFor(rules, infraAPIGroup, "instances/"+verb, true)
		if !ok || verbs(rule) != "create" {
			t.Fatalf("clause C rule for instances/%s = %#v (ok=%v)", verb, rule, ok)
		}
		if namesOf(rule) != "demo-dev,demo-prod" {
			t.Fatalf("instances/%s is not name-scoped: %#v", verb, rule.ResourceNames)
		}
	}

	// Clause C on the Repository: the two verbs a commit pass invokes. The
	// commit itself is one of them, which is why the composition on
	// repositorycommits still carries no create — the Code provider writes
	// that object once this grant is proven.
	for _, action := range codeRepositoryActions {
		rule, ok := ruleFor(rules, codeAPIGroup, "repositories/"+action, true)
		if !ok || verbs(rule) != "create" {
			t.Fatalf("clause C rule for repositories/%s = %#v (ok=%v)", action, rule, ok)
		}
		if namesOf(rule) != "demo-repo" {
			t.Fatalf("repositories/%s is not name-scoped: %#v", action, rule.ResourceNames)
		}
	}

	// Clause B and C on the Connection: read it, and ask it for a registry
	// token. Nothing composes a Connection — nothing here writes one.
	connection, ok := ruleFor(rules, codeAPIGroup, "connections", true)
	if !ok || verbs(connection) != "get" || namesOf(connection) != "github-main" {
		t.Fatalf("clause B connection read = %#v (ok=%v)", connection, ok)
	}
	mint, ok := ruleFor(rules, codeAPIGroup, "connections/mint-registry-token", true)
	if !ok || verbs(mint) != "create" || namesOf(mint) != "github-main" {
		t.Fatalf("clause C registry-token action = %#v (ok=%v)", mint, ok)
	}

	// The invariants that hold across every rule, whatever clause admitted it.
	for _, rule := range rules {
		group := rule.APIGroups[0]
		if group == "" {
			t.Fatalf("the core group is never mintable: %#v", rule)
		}
		for _, value := range append(append([]string{group}, rule.Resources...), rule.Verbs...) {
			if value == "*" {
				t.Fatalf("wildcard rule: %#v", rule)
			}
		}
		if len(rule.ResourceNames) > 0 {
			// A named rule may hold only object verbs: RBAC would ignore the
			// names on anything else and grant the whole collection.
			for _, verb := range rule.Verbs {
				switch verb {
				case "create":
					// Only on a subresource, which is how the data plane spells
					// "run this verb on this object".
					if !strings.Contains(rule.Resources[0], "/") {
						t.Fatalf("named create on a collection: %#v", rule)
					}
				case "list", "watch":
					t.Fatalf("named %s authorizes nothing: %#v", verb, rule)
				}
			}
			continue
		}
		// An unnamed rule is only ever the collection half of a declared
		// composition on a dependency's group.
		if group != infraAPIGroup && group != codeAPIGroup {
			t.Fatalf("unnamed rule outside a composed dependency group: %#v", rule)
		}
		for _, verb := range rule.Verbs {
			switch verb {
			case "create", "list", "watch":
			default:
				t.Fatalf("unnamed %s is not a collection verb: %#v", verb, rule)
			}
		}
	}
}

func TestProjectIdentityRulesScopeSelectedTemplateRead(t *testing.T) {
	p := boundProject()
	assertTemplateRead := func(project *aiv1alpha1.Project, want string) {
		t.Helper()
		rules := projectIdentityRules(project)
		template, ok := ruleFor(rules, infraAPIGroup, "templates", true)
		if want == "" {
			if ok {
				t.Fatalf("empty or missing template retained a template read: %#v", template)
			}
		} else if !ok || verbs(template) != "get" || namesOf(template) != want {
			t.Fatalf("selected template read = %#v (ok=%v), want get on %s", template, ok, want)
		}
		if rule, ok := ruleFor(rules, infraAPIGroup, "templates", false); ok {
			t.Fatalf("template read is not name-scoped: %#v", rule)
		}
	}

	p.Spec.Template = &aiv1alpha1.ProjectTemplateSpec{Name: " \tsimple-webapp\n"}
	assertTemplateRead(p, "simple-webapp")
	p.Spec.Template.Name = "other-template"
	assertTemplateRead(p, "other-template")
	p.Spec.Template.Name = " \t\n"
	assertTemplateRead(p, "")
	p.Spec.Template = nil
	assertTemplateRead(p, "")
	assertTemplateRead(nil, "")
}

// A project with no bindings still reaches the aggregate and can still resolve
// where its dependencies answer — and nothing else: no rule on a dependency's
// group at all, named or unnamed. Watching for the objects it is about to
// create rides the export virtual workspace, not this identity.
func TestProjectIdentityRulesWithoutBindings(t *testing.T) {
	rules := projectIdentityRules(&aiv1alpha1.Project{ObjectMeta: metav1.ObjectMeta{Name: "empty", UID: "u"}})
	for _, rule := range rules {
		if rule.APIGroups[0] == infraAPIGroup || rule.APIGroups[0] == codeAPIGroup {
			t.Fatalf("an unbound project holds a rule on a dependency's group: %#v", rule)
		}
	}
}

// The RepositoryCommit read appears only while there is one to follow up, and
// disappears again when it settles: restating the rules on every refresh is
// what makes a grant shrink.
func TestProjectIdentityRulesFollowThePendingCommit(t *testing.T) {
	p := boundProject()
	if _, ok := ruleFor(projectIdentityRules(p), codeAPIGroup, "repositorycommits", true); ok {
		t.Fatal("a project with no pending commit holds a named commit read")
	}
	p.Status.Workspace = &aiv1alpha1.ProjectWorkspaceStatus{
		PendingCommit: &aiv1alpha1.ProjectPendingCommit{Name: "commit-1"},
	}
	rule, ok := ruleFor(projectIdentityRules(p), codeAPIGroup, "repositorycommits", true)
	if !ok || verbs(rule) != "get" || namesOf(rule) != "commit-1" {
		t.Fatalf("pending-commit read = %#v (ok=%v)", rule, ok)
	}
	p.Status.Workspace.PendingCommit = nil
	if _, ok := ruleFor(projectIdentityRules(p), codeAPIGroup, "repositorycommits", true); ok {
		t.Fatal("the named commit read outlived the commit it was for")
	}
}

func TestProjectIdentityRulesScopeProviderReferencesToAuditedActiveActions(t *testing.T) {
	p := boundProject()
	grantedAt := metav1.Now()
	p.Spec.Environments[0].Bindings = append(p.Spec.Environments[0].Bindings, aiv1alpha1.ProjectProviderBindingSpec{
		Name: "orders-table", Provider: "databricks", Kind: aiv1alpha1.ProjectBindingKindProviderReference,
		ResourceRef: &aiv1alpha1.ProjectProviderResourceReference{
			Name: "orders", APIVersion: "databricks.railgrid.ai/v1alpha1", Kind: "Table", Resource: "tables",
		},
		AllowedActions: []aiv1alpha1.ProjectProviderActionSpec{
			{Name: "query_table", Version: "v1", SchemaDigest: "sha256:" + strings.Repeat("a", 64), GrantedBy: "alice", GrantedAt: &grantedAt},
			{Name: "drop_table", Version: "v1", SchemaDigest: "sha256:" + strings.Repeat("b", 64), GrantedBy: "alice", GrantedAt: &grantedAt, Revoked: true},
			{Name: "unreviewed", Version: "v1", SchemaDigest: "sha256:" + strings.Repeat("c", 64)},
		},
	})

	rules := projectIdentityRules(p)
	read, ok := ruleFor(rules, "databricks.railgrid.ai", "tables", true)
	if !ok || verbs(read) != "get" || namesOf(read) != "orders" {
		t.Fatalf("provider-reference parent read = %#v (ok=%v), want get on orders", read, ok)
	}
	query, ok := ruleFor(rules, "databricks.railgrid.ai", "tables/query_table", true)
	if !ok || verbs(query) != "create" || namesOf(query) != "orders" {
		t.Fatalf("active action rule = %#v (ok=%v), want create on tables/query_table named orders", query, ok)
	}
	for _, rule := range rules {
		if len(rule.APIGroups) != 1 || rule.APIGroups[0] != "databricks.railgrid.ai" {
			continue
		}
		if len(rule.ResourceNames) == 0 {
			t.Fatalf("provider-reference identity has a broad rule: %#v", rule)
		}
		if strings.Contains(strings.Join(rule.Resources, ","), "drop_table") || strings.Contains(strings.Join(rule.Resources, ","), "unreviewed") {
			t.Fatalf("revoked or unaudited action was granted: %#v", rule)
		}
	}

	p.Spec.Environments[0].Bindings[len(p.Spec.Environments[0].Bindings)-1].AllowedActions[0].Revoked = true
	for _, rule := range projectIdentityRules(p) {
		if len(rule.APIGroups) == 1 && rule.APIGroups[0] == "databricks.railgrid.ai" {
			t.Fatalf("revoking the only active action retained a provider-reference rule: %#v", rule)
		}
	}
}

func TestProjectIdentityTokenIsMintedOnceAndRebuiltWhenRulesChange(t *testing.T) {
	hub := &fakeIdentityHub{}
	r := &Reconciler{Identities: scopedidentity.New(hub.server(t))}
	ctx := context.Background()
	p := boundProject()

	token, err := r.identityToken(ctx, "cluster-a", p)
	if err != nil || token != "token-1" {
		t.Fatalf("identityToken = %q, %v", token, err)
	}
	// A second reconcile of an unchanged project reuses the live token: the
	// source refreshes at 80% of the TTL, not on every pass.
	if token, err = r.identityToken(ctx, "cluster-a", p); err != nil || token != "token-1" {
		t.Fatalf("second identityToken = %q, %v", token, err)
	}

	// Rebinding the repository must actually change what the identity may
	// reach, which means a fresh mint with the new rules.
	p.Spec.Repository.RepositoryRef = "other-repo"
	if token, err = r.identityToken(ctx, "cluster-a", p); err != nil || token != "token-2" {
		t.Fatalf("identityToken after rebinding = %q, %v", token, err)
	}
	// Adding the selected Template adds its name-scoped read and rebuilds the
	// cached token source with that new rule set.
	p.Spec.Template = &aiv1alpha1.ProjectTemplateSpec{Name: "simple-webapp"}
	if token, err = r.identityToken(ctx, "cluster-a", p); err != nil || token != "token-3" {
		t.Fatalf("identityToken after selecting a template = %q, %v", token, err)
	}

	hub.mu.Lock()
	defer hub.mu.Unlock()
	if len(hub.posts) != 3 {
		t.Fatalf("mints = %d, want one per distinct rule set", len(hub.posts))
	}
	owner, _ := hub.posts[0]["owner"].(map[string]any)
	if owner["kind"] != "Project" || owner["name"] != "demo" || owner["uid"] != "project-uid" {
		t.Fatalf("owner tuple = %#v", owner)
	}
	if hub.posts[0]["clusterID"] != "cluster-a" {
		t.Fatalf("clusterID = %v", hub.posts[0]["clusterID"])
	}
}

func TestProjectIdentityTokenRejectsStaleGenerationAfterRevocation(t *testing.T) {
	hub := &fakeIdentityHub{}
	r := &Reconciler{Identities: scopedidentity.New(hub.server(t))}
	old := boundProject()
	old.Generation = 4
	grantedAt := metav1.Now()
	old.Spec.Environments[0].Bindings = append(old.Spec.Environments[0].Bindings, aiv1alpha1.ProjectProviderBindingSpec{
		Name: "orders-table", Provider: "databricks", Kind: aiv1alpha1.ProjectBindingKindProviderReference,
		ResourceRef: &aiv1alpha1.ProjectProviderResourceReference{
			Name: "orders", APIVersion: "databricks.railgrid.ai/v1alpha1", Kind: "Table", Resource: "tables",
		},
		AllowedActions: []aiv1alpha1.ProjectProviderActionSpec{{
			Name: "query_table", Version: "v1", SchemaDigest: "sha256:" + strings.Repeat("a", 64),
			GrantedBy: "alice@example.com", GrantedAt: &grantedAt,
		}},
	})
	if token, err := r.identityToken(context.Background(), "cluster-a", old); err != nil || token != "token-1" {
		t.Fatalf("initial identity token = %q, %v", token, err)
	}

	revoked := old.DeepCopy()
	revoked.Generation++
	revoked.Spec.Environments[0].Bindings[len(revoked.Spec.Environments[0].Bindings)-1].AllowedActions[0].Revoked = true
	if token, err := r.identityToken(context.Background(), "cluster-a", revoked); err != nil || token != "token-2" {
		t.Fatalf("revoked identity token = %q, %v", token, err)
	}
	if _, err := r.identityToken(context.Background(), "cluster-a", old); err == nil {
		t.Fatal("stale Project generation refreshed a token with the revoked action")
	}

	hub.mu.Lock()
	defer hub.mu.Unlock()
	if len(hub.posts) != 2 {
		t.Fatalf("identity mints = %d, want the original and revoked rule sets only", len(hub.posts))
	}
}

func TestProjectIdentitySameGenerationStatusRulesUseLatestOwnerRevision(t *testing.T) {
	hub := &fakeIdentityHub{}
	r := &Reconciler{Identities: scopedidentity.New(hub.server(t))}
	p := boundProject()
	p.Generation = 8
	p.ResourceVersion = "rv-before-pending-commit"

	if token, err := r.identityToken(context.Background(), "cluster-a", p); err != nil || token != "token-1" {
		t.Fatalf("initial identity token = %q, %v", token, err)
	}

	// pendingCommit is status-derived authorization data. It can change while
	// Project generation stays the same, so the new rule set must be minted
	// with the resourceVersion from the exact observation that produced it.
	p.Status.Workspace = &aiv1alpha1.ProjectWorkspaceStatus{
		PendingCommit: &aiv1alpha1.ProjectPendingCommit{Name: "commit-8"},
	}
	p.ResourceVersion = "rv-with-pending-commit"
	if token, err := r.identityToken(context.Background(), "cluster-a", p); err != nil || token != "token-2" {
		t.Fatalf("status-updated identity token = %q, %v", token, err)
	}

	hub.mu.Lock()
	defer hub.mu.Unlock()
	if len(hub.posts) != 2 {
		t.Fatalf("mints = %d, want initial and status-derived rule set", len(hub.posts))
	}
	second := hub.posts[1]
	if second["expectedOwnerGeneration"] != float64(8) || second["expectedOwnerResourceVersion"] != "rv-with-pending-commit" {
		t.Fatalf("second request owner revision = %#v/%#v", second["expectedOwnerGeneration"], second["expectedOwnerResourceVersion"])
	}
	rules, ok := second["rules"].([]any)
	if !ok {
		t.Fatalf("second request rules = %#v", second["rules"])
	}
	found := false
	for _, raw := range rules {
		rule, _ := raw.(map[string]any)
		resources, _ := rule["resources"].([]any)
		resourceNames, _ := rule["resourceNames"].([]any)
		if len(resources) == 1 && resources[0] == "repositorycommits" && len(resourceNames) == 1 && resourceNames[0] == "commit-8" {
			found = true
		}
	}
	if !found {
		t.Fatalf("same-generation status rules did not include pending commit: %#v", rules)
	}
}

func TestProjectIdentityRetriesRevisionRaceWithFreshStatusRules(t *testing.T) {
	hub := &fakeIdentityHub{
		responses: []fakeIdentityHubResponse{{
			status: http.StatusConflict, code: identityclient.ErrorCodeStaleOwner, message: "owner revision changed",
		}},
	}
	r := &Reconciler{Identities: scopedidentity.New(hub.server(t))}
	p := boundProject()
	p.Generation = 11
	p.ResourceVersion = "rv-before"
	hub.onStaleOwner = func() {
		p.Status.Workspace = &aiv1alpha1.ProjectWorkspaceStatus{
			PendingCommit: &aiv1alpha1.ProjectPendingCommit{Name: "commit-after-race"},
		}
		p.ResourceVersion = "rv-after"
	}

	token, err := r.identityToken(context.Background(), "cluster-a", p)
	if err != nil || token != "token-1" {
		t.Fatalf("identityToken after fresh-read retry = %q, %v", token, err)
	}
	hub.mu.Lock()
	defer hub.mu.Unlock()
	if len(hub.posts) != 2 {
		t.Fatalf("request count = %d, want one stale observation and one fresh retry", len(hub.posts))
	}
	if hub.posts[0]["expectedOwnerResourceVersion"] != "rv-before" || hub.posts[1]["expectedOwnerResourceVersion"] != "rv-after" {
		t.Fatalf("retries did not use fresh owner revisions: %#v", hub.posts)
	}
	secondRules, ok := hub.posts[1]["rules"].([]any)
	if !ok {
		t.Fatalf("retry rules = %#v", hub.posts[1]["rules"])
	}
	foundPendingCommit := false
	for _, raw := range secondRules {
		rule, _ := raw.(map[string]any)
		resources, _ := rule["resources"].([]any)
		resourceNames, _ := rule["resourceNames"].([]any)
		if len(resources) == 1 && resources[0] == "repositorycommits" && len(resourceNames) == 1 && resourceNames[0] == "commit-after-race" {
			foundPendingCommit = true
		}
	}
	if !foundPendingCommit {
		t.Fatalf("retry did not derive rules from updated Project status: %#v", secondRules)
	}
}

func TestProjectIdentityRevisionRetryIsBoundedAndDoesNotRetryPolicyRefusal(t *testing.T) {
	t.Run("identical stale owner revision is not replayed", func(t *testing.T) {
		hub := &fakeIdentityHub{responses: []fakeIdentityHubResponse{{
			status: http.StatusConflict, code: identityclient.ErrorCodeStaleOwner, message: "owner revision changed",
		}}}
		r := &Reconciler{Identities: scopedidentity.New(hub.server(t))}
		if _, err := r.identityToken(context.Background(), "cluster-a", boundProject()); err == nil {
			t.Fatal("stale owner response unexpectedly succeeded")
		}
		hub.mu.Lock()
		defer hub.mu.Unlock()
		if len(hub.posts) != 1 {
			t.Fatalf("identical stale request replay count = %d, want 1", len(hub.posts))
		}
	})

	t.Run("different stale revisions stop after three attempts", func(t *testing.T) {
		hub := &fakeIdentityHub{responses: []fakeIdentityHubResponse{
			{status: http.StatusConflict, code: identityclient.ErrorCodeStaleOwner, message: "stale 1"},
			{status: http.StatusConflict, code: identityclient.ErrorCodeStaleOwner, message: "stale 2"},
			{status: http.StatusConflict, code: identityclient.ErrorCodeStaleOwner, message: "stale 3"},
		}}
		r := &Reconciler{Identities: scopedidentity.New(hub.server(t))}
		p := boundProject()
		p.Generation = 12
		p.ResourceVersion = "rv-1"
		version := 1
		hub.onStaleOwner = func() {
			version++
			p.ResourceVersion = fmt.Sprintf("rv-%d", version)
		}
		if _, err := r.identityToken(context.Background(), "cluster-a", p); err == nil {
			t.Fatal("repeated owner revision races unexpectedly succeeded")
		}
		hub.mu.Lock()
		defer hub.mu.Unlock()
		if len(hub.posts) != projectIdentityRevisionAttempts {
			t.Fatalf("stale requests = %d, want bounded attempts %d", len(hub.posts), projectIdentityRevisionAttempts)
		}
	})

	t.Run("policy refusal is not retried", func(t *testing.T) {
		hub := &fakeIdentityHub{responses: []fakeIdentityHubResponse{{
			status: http.StatusForbidden, code: "foreign_write_forbidden", message: "policy refused",
		}}}
		r := &Reconciler{Identities: scopedidentity.New(hub.server(t))}
		p := boundProject()
		if _, err := r.identityToken(context.Background(), "cluster-a", p); err == nil {
			t.Fatal("policy refusal unexpectedly succeeded")
		}
		hub.mu.Lock()
		defer hub.mu.Unlock()
		if len(hub.posts) != 1 {
			t.Fatalf("policy refusal requests = %d, want 1", len(hub.posts))
		}
	})
}

func TestReleaseIdentityRevokesAtTheHub(t *testing.T) {
	hub := &fakeIdentityHub{}
	r := &Reconciler{Identities: scopedidentity.New(hub.server(t))}
	if err := r.releaseIdentity(context.Background(), "cluster-a", boundProject()); err != nil {
		t.Fatalf("releaseIdentity: %v", err)
	}
	hub.mu.Lock()
	defer hub.mu.Unlock()
	if len(hub.deletes) != 1 || !strings.HasSuffix(hub.deletes[0], "/si-abc") {
		t.Fatalf("deletes = %v, want the owner's identity revoked", hub.deletes)
	}
}

// Without a hub there is no identity, and the reconciler says so by handing
// back an empty token rather than failing. A REST-only dev deployment then
// converges the Project itself and skips the cross-provider half, because
// there is no other path to a dependency's objects.
func TestIdentityTokenWithoutAHub(t *testing.T) {
	r := &Reconciler{}
	token, err := r.identityToken(context.Background(), "cluster-a", boundProject())
	if err != nil || token != "" {
		t.Fatalf("identityToken = %q, %v", token, err)
	}
}
