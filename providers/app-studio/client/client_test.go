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

package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"

	aiv1alpha1 "github.com/faroshq/provider-app-studio/apis/ai/v1alpha1"
	"github.com/faroshq/provider-app-studio/tenant"
)

func TestGraphQLStatusPatchReturnsCompleteProject(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Query string `json:"query"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode GraphQL request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(req.Query, "applyStatusYaml"):
			_, _ = w.Write([]byte(`{"data":{"applyStatusYaml":"ok"}}`))
		case strings.Contains(req.Query, "ProjectYaml"):
			_, _ = w.Write([]byte(`{"data":{"ai_faros_sh":{"v1alpha1":{"ProjectYaml":"apiVersion: ai.faros.sh/v1alpha1\nkind: Project\nmetadata:\n  name: complete-project\n  resourceVersion: \"43\"\nspec:\n  displayName: Complete Project\n  repository:\n    repositoryRef: complete-project\n  environments:\n  - name: development\n    mode: live\nstatus:\n  phase: Ready\n"}}}}`))
		default:
			t.Fatalf("unexpected GraphQL query: %s", req.Query)
		}
	}))
	t.Cleanup(server.Close)

	graphQL := tenant.NewGraphQLClient(server.URL, false)
	scope, err := graphQL.For("cluster-id", "caller-token")
	if err != nil {
		t.Fatalf("create GraphQL scope: %v", err)
	}
	client := NewFromGraphQL(scope)

	got, err := client.Projects().Patch(
		context.Background(),
		"complete-project",
		types.MergePatchType,
		[]byte(`{"status":{"phase":"Ready"}}`),
		metav1.PatchOptions{},
		"status",
	)
	if err != nil {
		t.Fatalf("patch project status: %v", err)
	}
	if got.Spec.DisplayName != "Complete Project" {
		t.Fatalf("Spec.DisplayName = %q, want Complete Project", got.Spec.DisplayName)
	}
	if got.Spec.Repository == nil || got.Spec.Repository.RepositoryRef != "complete-project" {
		t.Fatalf("Spec.Repository = %#v, want complete-project", got.Spec.Repository)
	}
	if len(got.Spec.Environments) != 1 || got.Spec.Environments[0].Mode != aiv1alpha1.ProjectEnvironmentModeLive {
		t.Fatalf("Spec.Environments = %#v, want development live environment", got.Spec.Environments)
	}
	if got.Status.Phase != aiv1alpha1.ProjectPhaseReady {
		t.Fatalf("Status.Phase = %q, want %q", got.Status.Phase, aiv1alpha1.ProjectPhaseReady)
	}
	if got.ResourceVersion != "43" {
		t.Fatalf("ResourceVersion = %q, want 43", got.ResourceVersion)
	}
}

func TestGraphQLResourceListForwardsAndAppliesLabelSelector(t *testing.T) {
	const selector = "code.faros.sh/repository=repo-a"
	const listYAML = `- apiVersion: code.faros.sh/v1alpha1
  kind: Package
  metadata:
    name: app-a
    labels:
      code.faros.sh/repository: repo-a
  spec:
    repositoryRef: repo-a
- apiVersion: code.faros.sh/v1alpha1
  kind: Package
  metadata:
    name: app-b
    labels:
      code.faros.sh/repository: repo-b
  spec:
    repositoryRef: repo-b
`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode GraphQL request: %v", err)
		}
		if !strings.Contains(req.Query, "$labelSelector: String") ||
			!strings.Contains(req.Query, "PackagesYaml(labelselector: $labelSelector)") {
			t.Fatalf("query = %q, want labelselector argument", req.Query)
		}
		if got := req.Variables["labelSelector"]; got != selector {
			t.Fatalf("labelSelector variable = %#v, want %q", got, selector)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"data":{"code_faros_sh":{"v1alpha1":{"PackagesYaml":%q}}}}`, listYAML)
	}))
	t.Cleanup(server.Close)

	graphQL := tenant.NewGraphQLClient(server.URL, false)
	scope, err := graphQL.For("cluster-id", "caller-token")
	if err != nil {
		t.Fatalf("create GraphQL scope: %v", err)
	}
	client := NewFromGraphQL(scope)
	res := tenant.Resource{
		GVR:    schema.GroupVersionResource{Group: "code.faros.sh", Version: "v1alpha1", Resource: "packages"},
		Kind:   "Package",
		Plural: "Packages",
	}
	got, err := client.Resource(res, "").List(context.Background(), metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		t.Fatalf("list packages: %v", err)
	}
	if len(got.Items) != 1 || got.Items[0].GetName() != "app-a" {
		t.Fatalf("packages = %#v, want only app-a", got.Items)
	}
}

func TestGraphQLProjectDeleteUsesNativeUIDPrecondition(t *testing.T) {
	for _, tt := range []struct {
		name         string
		expectedUID  types.UID
		wantConflict bool
		wantDeletes  int
	}{
		{name: "matching identity deletes", expectedUID: "project-current", wantDeletes: 1},
		{name: "API server rejects a reused name", expectedUID: "project-old", wantConflict: true, wantDeletes: 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			deleteCalls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodDelete {
					t.Fatalf("method = %s, want DELETE", r.Method)
				}
				if got, want := r.URL.Path, "/clusters/cluster-id/apis/ai.faros.sh/v1alpha1/projects/demo"; got != want {
					t.Fatalf("delete path = %q, want %q", got, want)
				}
				if got := r.Header.Get("Authorization"); got != "Bearer caller-token" {
					t.Fatalf("Authorization = %q, want caller token", got)
				}
				var opts metav1.DeleteOptions
				if err := json.NewDecoder(r.Body).Decode(&opts); err != nil {
					t.Fatalf("decode delete options: %v", err)
				}
				if opts.Preconditions == nil || opts.Preconditions.UID == nil || *opts.Preconditions.UID != tt.expectedUID {
					t.Fatalf("delete preconditions = %#v, want UID %q", opts.Preconditions, tt.expectedUID)
				}
				deleteCalls++
				if tt.wantConflict {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusConflict)
					_, _ = w.Write([]byte(`{"kind":"Status","apiVersion":"v1","status":"Failure","message":"UID precondition failed","reason":"Conflict","code":409}`))
					return
				}
				w.WriteHeader(http.StatusOK)
			}))
			t.Cleanup(server.Close)

			graphQL := tenant.NewGraphQLClient(server.URL, false)
			scope, err := graphQL.For("cluster-id", "caller-token")
			if err != nil {
				t.Fatalf("create GraphQL scope: %v", err)
			}
			client := NewFromGraphQL(scope)
			err = client.Projects().Delete(context.Background(), "demo", metav1.DeleteOptions{
				Preconditions: &metav1.Preconditions{UID: &tt.expectedUID},
			})
			if tt.wantConflict {
				if !apierrors.IsConflict(err) {
					t.Fatalf("Delete error = %v, want conflict", err)
				}
			} else if err != nil {
				t.Fatalf("Delete: %v", err)
			}
			if deleteCalls != tt.wantDeletes {
				t.Fatalf("delete mutation calls = %d, want %d", deleteCalls, tt.wantDeletes)
			}
		})
	}
}
