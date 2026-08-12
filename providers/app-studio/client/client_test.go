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
