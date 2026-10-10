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

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	aiv1alpha1 "github.com/railgrid/provider-app-studio/apis/ai/v1alpha1"
	asclient "github.com/railgrid/provider-app-studio/client"
	"github.com/railgrid/provider-app-studio/internal/scopedidentity"
	"github.com/railgrid/provider-sdk/identityclient"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic/fake"
	clienttesting "k8s.io/client-go/testing"
)

func TestProjectIdentityRetriesFreshStatusRevisionAfterStaleOwner(t *testing.T) {
	current := projectIdentityRevisionObject("rv-before", 6, "")
	var projectGets int
	provider := fake.NewSimpleDynamicClient(runtime.NewScheme())
	provider.PrependReactor("get", "projects", func(action clienttesting.Action) (bool, runtime.Object, error) {
		projectGets++
		return true, current.DeepCopy(), nil
	})

	var posts []map[string]any
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != identityclient.PathIdentities {
			http.NotFound(w, r)
			return
		}
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode identity request: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		posts = append(posts, request)
		w.Header().Set("Content-Type", "application/json")
		if request["expectedOwnerResourceVersion"] == "rv-before" {
			current = projectIdentityRevisionObject("rv-after", 6, "commit-after-race")
			w.WriteHeader(http.StatusConflict)
			_ = json.NewEncoder(w).Encode(identityclient.Error{
				Code: identityclient.ErrorCodeStaleOwner, Message: "owner revision changed",
			})
			return
		}
		if request["expectedOwnerResourceVersion"] != "rv-after" {
			t.Errorf("retry owner resourceVersion = %#v, want rv-after", request["expectedOwnerResourceVersion"])
			w.WriteHeader(http.StatusConflict)
			_ = json.NewEncoder(w).Encode(identityclient.Error{Code: identityclient.ErrorCodeStaleOwner, Message: "unexpected revision"})
			return
		}
		_ = json.NewEncoder(w).Encode(identityclient.Token{
			Token: "scoped-token", TokenType: "Bearer", ExpiresAt: time.Now().Add(time.Hour),
			ServiceAccount: "railgrid-si-test", Name: "si-test", OwnerRevisionVerified: true,
		})
	}))
	defer hub.Close()
	client, err := identityclient.New(identityclient.Options{HubURL: hub.URL, Provider: "app-studio", Token: "provider-token"})
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{
		projectIdentities: scopedidentity.New(client),
		// Selecting this seam preserves the dynamic provider client supplied in
		// identity while still exercising currentProjectForIdentity's GET.
		projectClientFor: func(identity) (*asclient.Client, error) { return nil, nil },
	}
	project := &aiv1alpha1.Project{ObjectMeta: metav1.ObjectMeta{Name: "demo", UID: "project-uid"}}
	id := identity{clusterID: "cluster-a", provider: provider}

	token, err := server.projectIdentityToken(context.Background(), id, project)
	if err != nil || token != "scoped-token" {
		t.Fatalf("projectIdentityToken = %q, %v", token, err)
	}
	if projectGets != 2 || len(posts) != 2 {
		t.Fatalf("fresh Project GETs/posts = %d/%d, want 2/2", projectGets, len(posts))
	}
	if posts[0]["expectedOwnerGeneration"] != float64(6) || posts[0]["expectedOwnerResourceVersion"] != "rv-before" {
		t.Fatalf("first request revision = %#v/%#v", posts[0]["expectedOwnerGeneration"], posts[0]["expectedOwnerResourceVersion"])
	}
	if posts[1]["expectedOwnerGeneration"] != float64(6) || posts[1]["expectedOwnerResourceVersion"] != "rv-after" {
		t.Fatalf("retry request revision = %#v/%#v", posts[1]["expectedOwnerGeneration"], posts[1]["expectedOwnerResourceVersion"])
	}
	rules, ok := posts[1]["rules"].([]any)
	if !ok {
		t.Fatalf("retry rules = %#v", posts[1]["rules"])
	}
	foundPendingCommit := false
	for _, raw := range rules {
		rule, _ := raw.(map[string]any)
		resources, _ := rule["resources"].([]any)
		resourceNames, _ := rule["resourceNames"].([]any)
		if len(resources) == 1 && resources[0] == "repositorycommits" && len(resourceNames) == 1 && resourceNames[0] == "commit-after-race" {
			foundPendingCommit = true
		}
	}
	if !foundPendingCommit {
		t.Fatalf("retried request did not use new status-derived rules: %#v", rules)
	}
}

func projectIdentityRevisionObject(resourceVersion string, generation int64, pendingCommit string) *unstructured.Unstructured {
	metadata := map[string]any{
		"name": "demo", "uid": "project-uid", "generation": generation,
		"resourceVersion": resourceVersion,
	}
	object := map[string]any{
		"apiVersion": aiv1alpha1.SchemeGroupVersion.String(),
		"kind":       "Project",
		"metadata":   metadata,
		"spec":       map[string]any{},
	}
	if pendingCommit != "" {
		object["status"] = map[string]any{
			"workspace": map[string]any{
				"pendingCommit": map[string]any{"name": pendingCommit},
			},
		}
	}
	return &unstructured.Unstructured{Object: object}
}
