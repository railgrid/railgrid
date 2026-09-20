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

// fakeIdentityHub stands in for the hub identity service: it records what it
// was asked for and hands back a token per request.
type fakeIdentityHub struct {
	mu      sync.Mutex
	posts   []map[string]any
	deletes []string
	mints   int
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
			h.mints++
			mint := h.mints
			h.mu.Unlock()
			writeIdentityJSON(w, identityclient.Token{
				Token: fmt.Sprintf("token-%d", mint), TokenType: "Bearer",
				ExpiresAt: time.Now().Add(time.Hour), ServiceAccount: "railgrid-si-abc", Name: "si-abc",
			})
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
		ObjectMeta: metav1.ObjectMeta{Name: "demo", UID: "project-uid"},
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

	// Clause E: the composition, per dependency kind.
	for _, tc := range []struct {
		group          string
		resource       string
		collection     string
		object         string
		names          string
		wantObjectRule bool
	}{
		{infraAPIGroup, "instances", "create,list,watch", "get,update,delete", "demo-dev,demo-prod", true},
		{codeAPIGroup, "repositories", "create,list,watch", "get,update,delete", "demo-repo", true},
		// Repositories are never deleted: they hold user code and outlive the
		// project, so no delete is asked for and none can be granted.
		{codeAPIGroup, "repositorycommits", "list,watch", "", "", false},
	} {
		collection, ok := ruleFor(rules, tc.group, tc.resource, false)
		if !ok || verbs(collection) != tc.collection {
			t.Fatalf("%s collection rule = %#v (ok=%v), want verbs %s", tc.resource, collection, ok, tc.collection)
		}
		object, ok := ruleFor(rules, tc.group, tc.resource, true)
		if ok != tc.wantObjectRule {
			t.Fatalf("%s object rule present = %v, want %v", tc.resource, ok, tc.wantObjectRule)
		}
		if !ok {
			continue
		}
		if verbs(object) != tc.object || namesOf(object) != tc.names {
			t.Fatalf("%s object rule = %#v, want verbs %s on %s", tc.resource, object, tc.object, tc.names)
		}
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
	if commits, ok := ruleFor(rules, codeAPIGroup, "repositorycommits", false); !ok || strings.Contains(verbs(commits), "create") {
		t.Fatalf("repositorycommits composition = %#v (ok=%v); the provider creates the commit, not this identity", commits, ok)
	}

	// Clause B and C on the Connection: read it, and ask it for a registry
	// token. Nothing composes a Connection — nothing here writes one.
	connection, ok := ruleFor(rules, codeAPIGroup, "connections", true)
	if !ok || verbs(connection) != "get" || namesOf(connection) != "github-main" {
		t.Fatalf("clause B connection read = %#v (ok=%v)", connection, ok)
	}
	mint, ok := ruleFor(rules, codeAPIGroup, "connections/mint_registry_token", true)
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

// A project with no bindings still reaches the aggregate, can still resolve
// where its dependencies answer, and can still watch the workspace for the
// objects it is about to create. None of that is access to any object.
func TestProjectIdentityRulesWithoutBindings(t *testing.T) {
	rules := projectIdentityRules(&aiv1alpha1.Project{ObjectMeta: metav1.ObjectMeta{Name: "empty", UID: "u"}})
	for _, rule := range rules {
		if len(rule.ResourceNames) == 0 {
			continue
		}
		if rule.APIGroups[0] == infraAPIGroup || rule.APIGroups[0] == codeAPIGroup {
			t.Fatalf("an unbound project names an object on a dependency: %#v", rule)
		}
	}
	if _, ok := ruleFor(rules, infraAPIGroup, "instances", false); !ok {
		t.Fatal("an unbound project cannot watch for the instance it is about to create")
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

func TestProjectIdentityTokenIsMintedOnceAndRebuiltWhenBindingsChange(t *testing.T) {
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

	hub.mu.Lock()
	defer hub.mu.Unlock()
	if len(hub.posts) != 2 {
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
