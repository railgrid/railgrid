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
	"net/http"
	"net/http/httptest"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"

	aiv1alpha1 "github.com/faroshq/provider-app-studio/apis/ai/v1alpha1"
	"github.com/faroshq/provider-app-studio/tenant"
	"github.com/faroshq/provider-app-studio/tenant/tenanttest"
)

func scopedClient(t *testing.T, proxy *tenanttest.Server) *Client {
	t.Helper()
	scope, err := proxy.Client().For("cluster-id", "caller-token")
	if err != nil {
		t.Fatalf("create tenant scope: %v", err)
	}
	return NewFromScope(scope)
}

func TestStatusPatchReturnsCompleteProject(t *testing.T) {
	proxy := tenanttest.NewServer(t)
	proxy.Add(ProjectGVR, tenanttest.ObjectFromYAML(t, `apiVersion: ai.faros.sh/v1alpha1
kind: Project
metadata:
  name: complete-project
  resourceVersion: "43"
spec:
  displayName: Complete Project
  repository:
    repositoryRef: complete-project
  environments:
  - name: development
    mode: live
status:
  phase: Pending
`))
	client := scopedClient(t, proxy)

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
	if got.ResourceVersion == "" || got.ResourceVersion == "43" {
		t.Fatalf("ResourceVersion = %q, want a fresh server-assigned version", got.ResourceVersion)
	}
	patches := proxy.RequestsFor(http.MethodPatch, ProjectGVR)
	if len(patches) != 1 || patches[0].Subresource != "status" || patches[0].Path != "/clusters/cluster-id/apis/ai.faros.sh/v1alpha1/projects/complete-project/status" {
		t.Fatalf("patch requests = %#v, want one merge patch on the status subresource", patches)
	}
	if patches[0].Bearer != "caller-token" {
		t.Fatalf("patch bearer = %q, want caller token", patches[0].Bearer)
	}
	if stored := proxy.Get(ProjectGVR, "", "complete-project"); stored == nil || stored.Object["status"].(map[string]any)["phase"] != "Ready" {
		t.Fatalf("stored project = %#v, want status.phase Ready", stored)
	}
}

func TestResourceListForwardsLabelSelectorToServer(t *testing.T) {
	const selector = "code.faros.sh/repository=repo-a"
	packagesGVR := schema.GroupVersionResource{Group: "code.faros.sh", Version: "v1alpha1", Resource: "packages"}
	proxy := tenanttest.NewServer(t)
	proxy.Add(packagesGVR,
		tenanttest.ObjectFromYAML(t, "apiVersion: code.faros.sh/v1alpha1\nkind: Package\nmetadata:\n  name: app-a\n  labels:\n    code.faros.sh/repository: repo-a\nspec:\n  repositoryRef: repo-a\n"),
		tenanttest.ObjectFromYAML(t, "apiVersion: code.faros.sh/v1alpha1\nkind: Package\nmetadata:\n  name: app-b\n  labels:\n    code.faros.sh/repository: repo-b\nspec:\n  repositoryRef: repo-b\n"),
	)
	client := scopedClient(t, proxy)
	res := tenant.Resource{GVR: packagesGVR, Kind: "Package", Plural: "Packages"}
	got, err := client.Resource(res, "").List(context.Background(), metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		t.Fatalf("list packages: %v", err)
	}
	if len(got.Items) != 1 || got.Items[0].GetName() != "app-a" {
		t.Fatalf("packages = %#v, want only app-a", got.Items)
	}
	lists := proxy.RequestsFor(http.MethodGet, packagesGVR)
	if len(lists) != 1 || lists[0].Query.Get("labelSelector") != selector {
		t.Fatalf("list requests = %#v, want one list carrying labelSelector %q", lists, selector)
	}
}

func TestResourceCreateAndUpdateAreUpserts(t *testing.T) {
	proxy := tenanttest.NewServer(t)
	proxy.Register(ProjectGVR)
	client := scopedClient(t, proxy)
	projects := client.Resource(projectResource, "")

	// Update on a missing object creates it.
	first := tenanttest.ObjectFromYAML(t, "apiVersion: ai.faros.sh/v1alpha1\nkind: Project\nmetadata:\n  name: demo\nspec:\n  displayName: First\n")
	created, err := projects.Update(context.Background(), first, metav1.UpdateOptions{})
	if err != nil {
		t.Fatalf("update-as-create: %v", err)
	}
	if created.GetResourceVersion() == "" || created.GetUID() == "" {
		t.Fatalf("created object = %#v, want server-assigned resourceVersion and uid", created.Object)
	}

	// Create on an existing object updates it without a resourceVersion.
	second := tenanttest.ObjectFromYAML(t, "apiVersion: ai.faros.sh/v1alpha1\nkind: Project\nmetadata:\n  name: demo\nspec:\n  displayName: Second\n")
	updated, err := projects.Create(context.Background(), second, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("create-as-update: %v", err)
	}
	if updated.GetUID() != created.GetUID() || updated.GetResourceVersion() == created.GetResourceVersion() {
		t.Fatalf("upsert result = %#v, want same uid and a new resourceVersion", updated.Object)
	}
	if name, _, _ := unstructured.NestedString(proxy.Get(ProjectGVR, "", "demo").Object, "spec", "displayName"); name != "Second" {
		t.Fatalf("stored displayName = %q, want Second", name)
	}

	// A caller-supplied resourceVersion is honoured as a compare-and-swap.
	stale := second.DeepCopy()
	stale.SetResourceVersion(created.GetResourceVersion())
	if _, err := projects.Update(context.Background(), stale, metav1.UpdateOptions{}); !apierrors.IsConflict(err) {
		t.Fatalf("stale update error = %v, want Conflict", err)
	}
}

func TestProjectDeleteUsesNativeUIDPrecondition(t *testing.T) {
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
				w.Header().Set("Content-Type", "application/json")
				if tt.wantConflict {
					w.WriteHeader(http.StatusConflict)
					_, _ = w.Write([]byte(`{"kind":"Status","apiVersion":"v1","status":"Failure","message":"UID precondition failed","reason":"Conflict","code":409}`))
					return
				}
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"kind":"Status","apiVersion":"v1","status":"Success"}`))
			}))
			t.Cleanup(server.Close)

			scope, err := tenant.NewClient(server.URL, false).For("cluster-id", "caller-token")
			if err != nil {
				t.Fatalf("create tenant scope: %v", err)
			}
			client := NewFromScope(scope)
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
				t.Fatalf("delete calls = %d, want %d", deleteCalls, tt.wantDeletes)
			}
		})
	}
}
