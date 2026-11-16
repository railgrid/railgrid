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
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	aiv1alpha1 "github.com/railgrid/provider-app-studio/apis/ai/v1alpha1"
	asclient "github.com/railgrid/provider-app-studio/client"
	"github.com/railgrid/provider-app-studio/internal/pullcredential"
	"github.com/railgrid/provider-sdk/claimscope"
)

// promotablePrivateProject is a built project whose images live in its
// Connection's registry, so promoting it needs a pull credential.
func promotablePrivateProject(t *testing.T, connectionRef string) (*asclient.Client, *aiv1alpha1.Project) {
	t.Helper()
	base := metav1.Now().Time
	project := projectForPromoteWithRepository("shop", "repo-a")
	project.Spec.Repository.ConnectionRef = connectionRef
	client := newProjectBuildProvenanceClient(project,
		[]*unstructured.Unstructured{repositoryCommitForBuildTest("current", "repo-a", "repo-a", "Succeeded", "commit-current", base)},
		[]*unstructured.Unstructured{
			projectBuildPackageForTest("repo-a", "frontend", "ghcr.io/acme/shop/frontend", map[string]any{"digest": "sha256:front", "tags": []any{"sha-commit-current"}}),
			projectBuildPackageForTest("repo-a", "backend", "ghcr.io/acme/shop/backend", map[string]any{"digest": "sha256:back", "tags": []any{"sha-commit-current"}}),
		})
	conn := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": codeConnectionsGVR.GroupVersion().String(),
		"kind":       "Connection",
		"metadata":   map[string]any{"name": "github", "uid": "conn-uid"},
	}}
	if _, err := client.Resource(codeConnectionResource, "").Create(context.Background(), conn, metav1.CreateOptions{}); err != nil {
		t.Fatalf("create connection: %v", err)
	}
	persisted, err := client.Projects().Get(context.Background(), project.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get project: %v", err)
	}
	return client, persisted
}

// mintServer answers Code's mint-registry-token verb with result, or with an
// envelope error when code is set.
func mintServer(t *testing.T, code string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/connections/github/"+pullcredential.MintVerb) {
			t.Errorf("unexpected call %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		if code != "" {
			w.WriteHeader(http.StatusUnprocessableEntity)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": code, "message": "the GitHub App installation is suspended"}})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{
			"registry": "ghcr.io", "username": "acme", "token": "pull-token", "expiresAt": "2026-11-15T23:00:00Z", "scoped": true,
		}})
	}))
	t.Cleanup(server.Close)
	return server
}

func promoteServer(callers providerCallers) *Server {
	return &Server{tenantWorkspaces: defaultTestWorkspaces.lookup, tenantActors: defaultTestActors.lookup, tenantProviders: defaultTestProviders, callers: callers}
}

// The regression: a promotion whose credential cannot be set up must say so
// instead of shipping a release production cannot pull.
func TestPromoteFailsWhenPullCredentialCannotBeMinted(t *testing.T) {
	for name, callers := range map[string]func(t *testing.T) providerCallers{
		"mint refused": func(t *testing.T) providerCallers {
			return newTestCallers(nil, mintServer(t, "installation_suspended").URL)
		},
		"no provider credential": func(*testing.T) providerCallers { return nil },
	} {
		t.Run(name, func(t *testing.T) {
			client, project := promotablePrivateProject(t, "github")
			_, _, err := promoteServer(callers(t)).promoteProject(context.Background(), client, identity{clusterID: "cluster-a"}, project, nil, nil)
			if err == nil || !strings.Contains(err.Error(), "registry pull credential") || !strings.Contains(err.Error(), `"github"`) {
				t.Fatalf("promote error = %v, want the pull credential failure, naming the connection", err)
			}
			stored, getErr := client.Projects().Get(context.Background(), project.Name, metav1.GetOptions{})
			if getErr != nil {
				t.Fatal(getErr)
			}
			if findProjectProductionBinding(stored) != nil {
				t.Fatal("a failed promotion still wrote the production binding")
			}
		})
	}
}

// A working connection: the credential is stored as App Studio's OWN Secret,
// under its own label (the only one its claim can write), and the production
// binding names it for the reconciler to hand over.
func TestPromoteRecordsPullCredentialUnderOwnLabel(t *testing.T) {
	client, project := promotablePrivateProject(t, "github")
	callers := newTestCallers(nil, mintServer(t, "").URL)
	updated, _, err := promoteServer(callers).promoteProject(context.Background(), client, identity{clusterID: "cluster-a"}, project, nil, nil)
	if err != nil {
		t.Fatalf("promote: %v", err)
	}
	production := findProjectProductionBinding(updated)
	wantName := pullcredential.SourceSecretName(projectTemplateProdInstanceName(project))
	if production == nil || production.ImagePullSecretRef == nil || production.ImagePullSecretRef.Name != wantName {
		t.Fatalf("production binding = %+v, want imagePullSecretRef %q", production, wantName)
	}
	secret, err := client.Resource(secretResource, pullcredential.Namespace).Get(context.Background(), wantName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("source Secret not written: %v", err)
	}
	if got := secret.GetLabels()[claimscope.OwnerLabel]; got != "app-studio" {
		t.Fatalf("owner label = %q, want app-studio: kcp refuses App Studio's write of any other", got)
	}
	doc, _, _ := unstructured.NestedString(secret.Object, "stringData", pullcredential.DockerConfigKey)
	if !strings.Contains(doc, `"ghcr.io"`) || !strings.Contains(doc, `"password":"pull-token"`) {
		t.Fatalf("dockerconfigjson = %q", doc)
	}
	if secret.GetAnnotations()[pullcredential.ExpiresAtAnnotation] != "2026-11-15T23:00:00Z" {
		t.Fatalf("expiry annotation = %v", secret.GetAnnotations())
	}
	// Renewal mints only from the Connection promote authorized as the caller.
	if name, uid := pullcredential.SourceConnection(secret); name != "github" || uid != "conn-uid" {
		t.Fatalf("recorded connection = %q/%q, want github/conn-uid", name, uid)
	}
	// The Project owns the credential, so kcp collects it with the Project.
	owners := secret.GetOwnerReferences()
	if len(owners) != 1 || owners[0].Kind != "Project" || owners[0].Name != project.Name || owners[0].UID == "" || owners[0].UID != project.UID {
		t.Fatalf("ownerReferences = %+v, want the Project %s/%s", owners, project.Name, project.UID)
	}

	// Promoting again replaces the credential in place.
	if _, _, err := promoteServer(callers).promoteProject(context.Background(), client, identity{clusterID: "cluster-a"}, updated, nil, nil); err != nil {
		t.Fatalf("re-promote: %v", err)
	}
}

// A project with no Connection pulls anonymously, as before: no credential
// is needed and none is asked for.
func TestPromoteWithoutConnectionNeedsNoPullCredential(t *testing.T) {
	client, project := promotablePrivateProject(t, "")
	updated, _, err := promoteServer(nil).promoteProject(context.Background(), client, identity{clusterID: "cluster-a"}, project, nil, nil)
	if err != nil {
		t.Fatalf("promote: %v", err)
	}
	if production := findProjectProductionBinding(updated); production == nil || production.ImagePullSecretRef != nil {
		t.Fatalf("production binding = %+v, want no pull credential", production)
	}
}
