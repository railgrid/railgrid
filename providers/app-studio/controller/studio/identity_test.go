/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package studio

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
			mint := h.mints
			h.mu.Unlock()
			writeJSON(w, identityclient.Token{
				Token: fmt.Sprintf("token-%d", mint), TokenType: "Bearer",
				ExpiresAt: time.Now().Add(time.Hour), ServiceAccount: "railgrid-si-studio", Name: "si-studio",
			})
		case http.MethodGet:
			writeJSON(w, struct {
				Items []identityclient.Identity `json:"items"`
			}{Items: []identityclient.Identity{{Name: "si-studio"}}})
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

func writeJSON(w http.ResponseWriter, payload any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(payload)
}

func provisionedStudio() *aiv1alpha1.Studio {
	ref := func(name string) *aiv1alpha1.ProjectProviderResourceReference {
		return &aiv1alpha1.ProjectProviderResourceReference{
			Name: name, APIVersion: "infrastructure.railgrid.ai/v1alpha1", Kind: "Instance", Resource: "instances",
		}
	}
	st := &aiv1alpha1.Studio{ObjectMeta: metav1.ObjectMeta{Name: "default", UID: "studio-uid"}}
	st.Spec.Search.ResourceRef = ref(SearchInstanceName)
	st.Spec.Browser.ResourceRef = ref(BrowserInstanceName)
	return st
}

func ruleFor(rules []rbacv1.PolicyRule, resource string, named bool) (rbacv1.PolicyRule, bool) {
	for _, rule := range rules {
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

// The Studio holds the instance composition and nothing else: it calls no MCP
// tool, resolves no data-plane coordinate, and reads its model credentials
// over this provider's own virtual workspace.
func TestStudioIdentityRulesAreTheInstanceComposition(t *testing.T) {
	rules := studioIdentityRules(provisionedStudio())
	if len(rules) != 2 {
		t.Fatalf("rules = %#v, want the collection and object halves of one composition", rules)
	}
	collection, ok := ruleFor(rules, "instances", false)
	if !ok || strings.Join(collection.Verbs, ",") != "create,list,watch" {
		t.Fatalf("collection rule = %#v (ok=%v)", collection, ok)
	}
	object, ok := ruleFor(rules, "instances", true)
	if !ok || strings.Join(object.Verbs, ",") != "get,update,delete" {
		t.Fatalf("object rule = %#v (ok=%v)", object, ok)
	}
	// Sorted and deduplicated, so an unchanged Studio does not re-mint.
	if got := strings.Join(object.ResourceNames, ","); got != "app-studio-browser,app-studio-search" {
		t.Fatalf("object names = %q", got)
	}
	for _, rule := range rules {
		if len(rule.APIGroups) != 1 || rule.APIGroups[0] != infraAPIGroup {
			t.Fatalf("rule outside the infrastructure dependency: %#v", rule)
		}
	}
}

// Before the API resolves the templates there is nothing to name, and the
// Studio holds only the half that names nothing.
func TestStudioIdentityRulesBeforeTemplatesResolve(t *testing.T) {
	rules := studioIdentityRules(&aiv1alpha1.Studio{ObjectMeta: metav1.ObjectMeta{Name: "default", UID: "u"}})
	if len(rules) != 1 {
		t.Fatalf("rules = %#v, want only the collection half", rules)
	}
	if len(rules[0].ResourceNames) != 0 {
		t.Fatalf("collection rule is name-scoped: %#v", rules[0])
	}
}

func TestStudioIdentityTokenIsMintedOnceAndRebuiltWhenBackendsChange(t *testing.T) {
	hub := &fakeIdentityHub{}
	r := &Reconciler{Identities: scopedidentity.New(hub.client(t))}
	ctx := context.Background()
	st := provisionedStudio()

	token, err := r.identityToken(ctx, "cluster-a", st)
	if err != nil || token != "token-1" {
		t.Fatalf("identityToken = %q, %v", token, err)
	}
	if token, err = r.identityToken(ctx, "cluster-a", st); err != nil || token != "token-1" {
		t.Fatalf("second identityToken = %q, %v", token, err)
	}

	// Disabling a backend must actually drop the grant it carried.
	st.Spec.Browser.ResourceRef = nil
	if token, err = r.identityToken(ctx, "cluster-a", st); err != nil || token != "token-2" {
		t.Fatalf("identityToken after disabling the browser = %q, %v", token, err)
	}

	hub.mu.Lock()
	defer hub.mu.Unlock()
	if len(hub.posts) != 2 {
		t.Fatalf("mints = %d, want one per distinct rule set", len(hub.posts))
	}
	owner, _ := hub.posts[0]["owner"].(map[string]any)
	if owner["kind"] != "Studio" || owner["name"] != "default" || owner["uid"] != "studio-uid" {
		t.Fatalf("owner tuple = %#v", owner)
	}
	if hub.posts[0]["clusterID"] != "cluster-a" {
		t.Fatalf("clusterID = %v", hub.posts[0]["clusterID"])
	}
}

func TestStudioReleaseIdentityRevokesAtTheHub(t *testing.T) {
	hub := &fakeIdentityHub{}
	r := &Reconciler{Identities: scopedidentity.New(hub.client(t))}
	if err := r.releaseIdentity(context.Background(), "cluster-a", provisionedStudio()); err != nil {
		t.Fatalf("releaseIdentity: %v", err)
	}
	hub.mu.Lock()
	defer hub.mu.Unlock()
	if len(hub.deletes) != 1 || !strings.HasSuffix(hub.deletes[0], "/si-studio") {
		t.Fatalf("deletes = %v, want the owner's identity revoked", hub.deletes)
	}
}

// Without a hub there is no identity and no token, and the reconciler says so
// rather than failing: the shared backends are then left alone.
func TestStudioIdentityTokenWithoutAHub(t *testing.T) {
	token, err := (&Reconciler{}).identityToken(context.Background(), "cluster-a", provisionedStudio())
	if err != nil || token != "" {
		t.Fatalf("identityToken = %q, %v", token, err)
	}
}
