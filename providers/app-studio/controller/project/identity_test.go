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

func ruleFor(rules []rbacv1.PolicyRule, group, resource string) (rbacv1.PolicyRule, bool) {
	for _, rule := range rules {
		if len(rule.APIGroups) == 1 && rule.APIGroups[0] == group &&
			len(rule.Resources) == 1 && rule.Resources[0] == resource {
			return rule, true
		}
	}
	return rbacv1.PolicyRule{}, false
}

// The rules are the contract with the hub's policy: anything outside the
// admitted shapes refuses the WHOLE request, so each one is pinned here.
func TestProjectIdentityRulesAreNameScopedAndReadOnlyOnForeignGroups(t *testing.T) {
	rules := projectIdentityRules(boundProject())

	// Nothing on this provider's own group. A project acting as itself does
	// not write Projects, and the reconciler that does holds the provider's
	// own credential over the APIExport virtual workspace instead.
	if rule, ok := ruleFor(rules, "ai.railgrid.ai", "projects"); ok {
		t.Fatalf("the identity holds a rule on the provider's own group: %#v", rule)
	}
	mcp, ok := ruleFor(rules, "railgrid.ai", "mcpservers")
	if !ok || mcp.Verbs[0] != "use" || mcp.ResourceNames[0] != "default" {
		t.Fatalf("clause D MCP grant = %#v", mcp)
	}
	bindings, ok := ruleFor(rules, "apis.kcp.io", "apibindings")
	if !ok || bindings.Verbs[0] != "get" || strings.Join(bindings.ResourceNames, ",") != "code,infrastructure" {
		t.Fatalf("clause D APIBinding grant = %#v", bindings)
	}
	instances, ok := ruleFor(rules, infraAPIGroup, "instances")
	if !ok || len(instances.Verbs) != 1 || instances.Verbs[0] != "get" {
		t.Fatalf("clause B instance read = %#v", instances)
	}
	if strings.Join(instances.ResourceNames, ",") != "demo-dev,demo-prod" {
		t.Fatalf("instances are not name-scoped to the project's own: %#v", instances.ResourceNames)
	}
	for _, verb := range instanceDataPlaneVerbs {
		rule, ok := ruleFor(rules, infraAPIGroup, "instances/"+verb)
		if !ok || len(rule.Verbs) != 1 || rule.Verbs[0] != "create" {
			t.Fatalf("clause C rule for instances/%s = %#v (ok=%v)", verb, rule, ok)
		}
		if strings.Join(rule.ResourceNames, ",") != "demo-dev,demo-prod" {
			t.Fatalf("instances/%s is not name-scoped: %#v", verb, rule.ResourceNames)
		}
	}
	repository, ok := ruleFor(rules, codeAPIGroup, "repositories")
	if !ok || repository.Verbs[0] != "get" || repository.ResourceNames[0] != "demo-repo" {
		t.Fatalf("clause B repository read = %#v", repository)
	}
	mint, ok := ruleFor(rules, codeAPIGroup, "connections/mint_registry_token")
	if !ok || mint.Verbs[0] != "create" || mint.ResourceNames[0] != "github-main" {
		t.Fatalf("clause C registry-token action = %#v", mint)
	}

	// Nothing may be a write on a foreign group, a wildcard, or unnamed: the
	// hub refuses the whole request over any one of them.
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
		if len(rule.ResourceNames) == 0 {
			t.Fatalf("unnamed rule outside the provider's own group: %#v", rule)
		}
		if group != infraAPIGroup && group != codeAPIGroup {
			continue
		}
		for _, verb := range rule.Verbs {
			if strings.Contains(rule.Resources[0], "/") && verb != "create" {
				t.Fatalf("only create is mintable on a subresource: %#v", rule)
			}
			if !strings.Contains(rule.Resources[0], "/") && verb != "get" {
				t.Fatalf("only get is mintable on a foreign resource: %#v", rule)
			}
		}
	}
}

// A project with nothing bound still reaches the aggregate and can still
// resolve where its dependencies answer: neither is access to anything.
func TestProjectIdentityRulesWithoutBindings(t *testing.T) {
	rules := projectIdentityRules(&aiv1alpha1.Project{ObjectMeta: metav1.ObjectMeta{Name: "empty", UID: "u"}})
	if len(rules) != 2 {
		t.Fatalf("rules = %#v, want only the MCP door and the bindings", rules)
	}
	for _, rule := range rules {
		if rule.APIGroups[0] == infraAPIGroup || rule.APIGroups[0] == codeAPIGroup {
			t.Fatalf("an unbound project holds nothing on a dependency: %#v", rule)
		}
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
// back an empty token rather than failing: the claimed-VW fallback carries a
// REST-only dev deployment.
func TestIdentityTokenWithoutAHub(t *testing.T) {
	r := &Reconciler{}
	token, err := r.identityToken(context.Background(), "cluster-a", boundProject())
	if err != nil || token != "" {
		t.Fatalf("identityToken = %q, %v", token, err)
	}
}
