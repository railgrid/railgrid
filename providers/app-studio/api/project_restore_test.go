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

package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/mux"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"

	aiv1alpha1 "github.com/faroshq/provider-app-studio/apis/ai/v1alpha1"
	asclient "github.com/faroshq/provider-app-studio/client"
	"github.com/faroshq/provider-app-studio/store"
	"github.com/faroshq/provider-app-studio/workspace"
)

func TestExactRestoreFilesRejectsWrongOrIncompleteCheckout(t *testing.T) {
	requested := strings.Repeat("a", 40)
	for _, test := range []struct {
		name     string
		checkout checkoutToolResult
		want     string
	}{
		{name: "wrong commit", checkout: checkoutToolResult{CommitSHA: strings.Repeat("b", 40)}, want: "instead of requested commit"},
		{name: "skipped path", checkout: checkoutToolResult{CommitSHA: requested, Skipped: []string{"asset.png"}}, want: "checkout omitted 1 path"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := exactRestoreFiles(requested, test.checkout); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("exactRestoreFiles error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestRestoreProjectWorkspaceReplacesExactTreeAndSchedulesDevelopmentSync(t *testing.T) {
	commitSHA := strings.Repeat("a", 40)
	project := projectForPromoteWithRepository("shop", "repo-a")
	project.UID = types.UID("project-uid")
	commit := releaseCommitForTest("restore", "repo-a", "Succeeded", commitSHA, metav1.Now().Time)
	client := newProjectBuildProvenanceClient(project, []*unstructured.Unstructured{commit}, nil)
	workspaces := workspace.NewFileStore(t.TempDir())
	scope := workspace.Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "shop", ProjectUID: string(project.UID)}
	if _, err := workspaces.WriteFile(context.Background(), scope, workspace.WriteOptions{Path: "stale.txt", Content: "remove\n"}); err != nil {
		t.Fatal(err)
	}
	expectedRevision, err := workspaces.SourceRevision(context.Background(), scope)
	if err != nil {
		t.Fatal(err)
	}

	upstream := restoreCheckoutServer(t, checkoutToolResult{
		CommitSHA: commitSHA,
		Files: []struct {
			Path    string `json:"path"`
			Content string `json:"content"`
		}{{Path: "app.txt", Content: "restored\n"}},
	}, nil)
	defer upstream.Close()

	var syncs atomic.Int32
	server := &Server{
		store:      store.NewMemoryStore(),
		workspaces: workspaces,
		hubBase:    upstream.URL,
		projectClientFor: func(identity) (*asclient.Client, error) {
			return client, nil
		},
		developmentSyncAfterMutation: func(_ identity, _ *aiv1alpha1.Project, action string) error {
			if action != projectActionRestoreWorkspace {
				t.Errorf("sync action = %q", action)
			}
			syncs.Add(1)
			return nil
		},
	}
	request := restoreRequest(commitSHA, expectedRevision)
	response := httptest.NewRecorder()
	server.restoreProjectWorkspace(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("response = %d %s", response.Code, response.Body.String())
	}
	var restored projectRestoreResponse
	if err := json.NewDecoder(response.Body).Decode(&restored); err != nil {
		t.Fatal(err)
	}
	if restored.CommitSHA != commitSHA || restored.SourceRevision != expectedRevision+1 || len(restored.Written) != 1 || restored.Written[0] != "app.txt" || len(restored.Deleted) != 1 || restored.Deleted[0] != "stale.txt" {
		t.Fatalf("restore response = %#v", restored)
	}
	app, err := workspaces.ReadFile(context.Background(), scope, workspace.ReadOptions{Path: "app.txt"})
	if err != nil || app.Content != "restored\n" {
		t.Fatalf("restored app = %#v, err=%v", app, err)
	}
	if _, err := workspaces.ReadFile(context.Background(), scope, workspace.ReadOptions{Path: "stale.txt"}); err == nil {
		t.Fatal("stale workspace-only file was not deleted")
	}
	for i := 0; i < 100 && syncs.Load() == 0; i++ {
		time.Sleep(time.Millisecond)
	}
	if syncs.Load() != 1 {
		t.Fatalf("development sync count = %d, want 1", syncs.Load())
	}
}

func TestRestoreProjectWorkspaceRejectsMutationDuringCheckout(t *testing.T) {
	commitSHA := strings.Repeat("a", 40)
	project := projectForPromoteWithRepository("shop", "repo-a")
	project.UID = types.UID("project-uid")
	commit := releaseCommitForTest("restore", "repo-a", "Succeeded", commitSHA, metav1.Now().Time)
	client := newProjectBuildProvenanceClient(project, []*unstructured.Unstructured{commit}, nil)
	workspaces := workspace.NewFileStore(t.TempDir())
	scope := workspace.Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "shop", ProjectUID: string(project.UID)}
	if _, err := workspaces.WriteFile(context.Background(), scope, workspace.WriteOptions{Path: "app.txt", Content: "before\n"}); err != nil {
		t.Fatal(err)
	}
	expectedRevision, err := workspaces.SourceRevision(context.Background(), scope)
	if err != nil {
		t.Fatal(err)
	}
	upstream := restoreCheckoutServer(t, checkoutToolResult{
		CommitSHA: commitSHA,
		Files: []struct {
			Path    string `json:"path"`
			Content string `json:"content"`
		}{{Path: "app.txt", Content: "old commit\n"}},
	}, func() {
		if _, err := workspaces.WriteFile(context.Background(), scope, workspace.WriteOptions{Path: "app.txt", Content: "newer edit\n"}); err != nil {
			t.Errorf("concurrent edit: %v", err)
		}
	})
	defer upstream.Close()

	server := &Server{
		store:      store.NewMemoryStore(),
		workspaces: workspaces,
		hubBase:    upstream.URL,
		projectClientFor: func(identity) (*asclient.Client, error) {
			return client, nil
		},
	}
	response := httptest.NewRecorder()
	server.restoreProjectWorkspace(response, restoreRequest(commitSHA, expectedRevision))
	if response.Code != http.StatusConflict {
		t.Fatalf("response = %d %s, want 409", response.Code, response.Body.String())
	}
	app, err := workspaces.ReadFile(context.Background(), scope, workspace.ReadOptions{Path: "app.txt"})
	if err != nil || app.Content != "newer edit\n" {
		t.Fatalf("app after conflict = %#v, err=%v", app, err)
	}
}

func TestRestoreProjectWorkspaceRejectsStaleHistorySelectionBeforeCheckout(t *testing.T) {
	commitSHA := strings.Repeat("a", 40)
	project := projectForPromoteWithRepository("shop", "repo-a")
	project.UID = types.UID("project-uid")
	commit := releaseCommitForTest("restore", "repo-a", "Succeeded", commitSHA, metav1.Now().Time)
	client := newProjectBuildProvenanceClient(project, []*unstructured.Unstructured{commit}, nil)
	workspaces := workspace.NewFileStore(t.TempDir())
	scope := workspace.Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "shop", ProjectUID: string(project.UID)}
	if _, err := workspaces.WriteFile(context.Background(), scope, workspace.WriteOptions{Path: "app.txt", Content: "newer edit\n"}); err != nil {
		t.Fatal(err)
	}
	currentRevision, err := workspaces.SourceRevision(context.Background(), scope)
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{
		store:      store.NewMemoryStore(),
		workspaces: workspaces,
		// No hubBase is deliberate: a stale request must fail before checkout.
		projectClientFor: func(identity) (*asclient.Client, error) { return client, nil },
	}
	response := httptest.NewRecorder()
	server.restoreProjectWorkspace(response, restoreRequest(commitSHA, currentRevision-1))
	if response.Code != http.StatusConflict {
		t.Fatalf("response = %d %s, want stale History 409", response.Code, response.Body.String())
	}
	app, readErr := workspaces.ReadFile(context.Background(), scope, workspace.ReadOptions{Path: "app.txt"})
	if readErr != nil || app.Content != "newer edit\n" {
		t.Fatalf("app after stale selection = %#v, err=%v", app, readErr)
	}
}

func restoreRequest(commitSHA string, expectedSourceRevision uint64) *http.Request {
	request := httptest.NewRequest(http.MethodPost, "/api/projects/shop/restore-workspace", strings.NewReader(fmt.Sprintf(`{"commitSHA":%q,"expectedSourceRevision":%d}`, commitSHA, expectedSourceRevision)))
	request = mux.SetURLVars(request, map[string]string{"project": "shop"})
	request.Header.Set("X-Faros-Tenant", "root:faros:tenants:org-a:workspace-a")
	request.Header.Set("X-Faros-Cluster", "cluster-a")
	return request
}

func restoreCheckoutServer(t *testing.T, checkout checkoutToolResult, beforeResponse func()) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Params struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			} `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode MCP request: %v", err)
		}
		if request.Params.Name != projectToolCodeCheckoutRepository || request.Params.Arguments["ref"] != checkout.CommitSHA {
			t.Errorf("checkout request = %#v", request.Params)
		}
		if beforeResponse != nil {
			beforeResponse()
		}
		raw, _ := json.Marshal(checkout)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"jsonrpc": "2.0",
			"id":      1,
			"result": map[string]any{
				"content": []map[string]any{{"type": "text", "text": string(raw)}},
			},
		})
	}))
}
