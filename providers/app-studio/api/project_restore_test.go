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
		{name: "bad encoding", checkout: checkoutToolResult{CommitSHA: requested, Files: []checkoutToolFile{{Path: "a.bin", Content: "x", Encoding: "hex"}}}, want: "unsupported file encoding"},
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
		Files:     []checkoutToolFile{{Path: "app.txt", Content: "restored\n"}},
	}, nil)
	defer upstream.Close()

	var syncs atomic.Int32
	server := &Server{
		tenantWorkspaces: staticWorkspaces{"cluster-a": testWorkspace("cluster-a", "org-a", "workspace-a")}.lookup,
		store:            store.NewMemoryStore(),
		workspaces:       workspaces,
		hubBase:          upstream.URL,
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

func TestCheckoutSkippedPaths(t *testing.T) {
	for _, test := range []struct {
		name     string
		skipped  []string
		want     []string
		complete bool
	}{
		{name: "none", complete: true},
		{
			name:     "reason suffixes",
			skipped:  []string{"public/logo.png (binary)", "assets/model (v2).glb (file too large)", "z.txt (file-count cap)", "y.txt (total-size cap)"},
			want:     []string{"public/logo.png", "assets/model (v2).glb", "z.txt", "y.txt"},
			complete: true,
		},
		{name: "capped list", skipped: []string{"a.png (binary)", "(more paths skipped)"}, want: []string{"a.png"}},
		{name: "truncated tree", skipped: []string{"(tree truncated by the host: repository has more entries than the tree API returns)"}},
		{name: "unknown shape", skipped: []string{"a.png (symlink)"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, complete := checkoutSkippedPaths(test.skipped)
			if strings.Join(got, "|") != strings.Join(test.want, "|") || complete != test.complete {
				t.Fatalf("checkoutSkippedPaths = %q, %v; want %q, %v", got, complete, test.want, test.complete)
			}
		})
	}
}

// Checkout skip entries carry a reason suffix ("path (binary)"); restore must
// match them to workspace paths or it deletes exactly the files it meant to keep.
func TestRestoreProjectWorkspaceKeepsSkippedFiles(t *testing.T) {
	logo := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 0x00, 0xff}
	for _, test := range []struct {
		name        string
		skipped     []string
		wantDeleted []string
	}{
		{
			name:        "listed skips",
			skipped:     []string{"public/logo.png (binary)", "data/big.json (file too large)"},
			wantDeleted: []string{"stale.txt"},
		},
		{
			// The list itself was capped: any omitted file may be a skipped
			// one, so nothing is deleted.
			name:    "capped skip list",
			skipped: []string{"public/logo.png (binary)", "(more paths skipped)"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			commitSHA := strings.Repeat("a", 40)
			project := projectForPromoteWithRepository("shop", "repo-a")
			project.UID = types.UID("project-uid")
			commit := releaseCommitForTest("restore", "repo-a", "Succeeded", commitSHA, metav1.Now().Time)
			client := newProjectBuildProvenanceClient(project, []*unstructured.Unstructured{commit}, nil)
			workspaces := workspace.NewFileStore(t.TempDir())
			scope := workspace.Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "shop", ProjectUID: string(project.UID)}
			if _, err := workspaces.PutFile(ctx, scope, workspace.PutOptions{Path: "public/logo.png", Data: logo}); err != nil {
				t.Fatal(err)
			}
			for _, file := range []workspace.WriteOptions{
				{Path: "data/big.json", Content: "{}\n"},
				{Path: "stale.txt", Content: "remove\n"},
			} {
				if _, err := workspaces.WriteFile(ctx, scope, file); err != nil {
					t.Fatal(err)
				}
			}
			expectedRevision, err := workspaces.SourceRevision(ctx, scope)
			if err != nil {
				t.Fatal(err)
			}
			upstream := restoreCheckoutServer(t, checkoutToolResult{
				CommitSHA: commitSHA,
				Files:     []checkoutToolFile{{Path: "app.txt", Content: "restored\n"}},
				Skipped:   test.skipped,
			}, nil)
			defer upstream.Close()
			server := &Server{
				tenantWorkspaces:             staticWorkspaces{"cluster-a": testWorkspace("cluster-a", "org-a", "workspace-a")}.lookup,
				store:                        store.NewMemoryStore(),
				workspaces:                   workspaces,
				hubBase:                      upstream.URL,
				projectClientFor:             func(identity) (*asclient.Client, error) { return client, nil },
				developmentSyncAfterMutation: func(identity, *aiv1alpha1.Project, string) error { return nil },
			}

			response := httptest.NewRecorder()
			server.restoreProjectWorkspace(response, restoreRequest(commitSHA, expectedRevision))
			if response.Code != http.StatusOK {
				t.Fatalf("response = %d %s", response.Code, response.Body.String())
			}
			var restored projectRestoreResponse
			if err := json.NewDecoder(response.Body).Decode(&restored); err != nil {
				t.Fatal(err)
			}
			if strings.Join(restored.Deleted, ",") != strings.Join(test.wantDeleted, ",") || strings.Join(restored.Skipped, ",") != strings.Join(test.skipped, ",") {
				t.Fatalf("restore response = %#v", restored)
			}
			for _, kept := range []string{"public/logo.png", "data/big.json"} {
				if exists, err := workspaces.FileExists(ctx, scope, kept); err != nil || !exists {
					t.Fatalf("skipped file %s was not preserved (exists=%v, err=%v)", kept, exists, err)
				}
			}
			if app, err := workspaces.ReadFile(ctx, scope, workspace.ReadOptions{Path: "app.txt"}); err != nil || app.Content != "restored\n" {
				t.Fatalf("restored app = %#v, err=%v", app, err)
			}
		})
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
		Files:     []checkoutToolFile{{Path: "app.txt", Content: "old commit\n"}},
	}, func() {
		if _, err := workspaces.WriteFile(context.Background(), scope, workspace.WriteOptions{Path: "app.txt", Content: "newer edit\n"}); err != nil {
			t.Errorf("concurrent edit: %v", err)
		}
	})
	defer upstream.Close()

	server := &Server{
		tenantWorkspaces: staticWorkspaces{"cluster-a": testWorkspace("cluster-a", "org-a", "workspace-a")}.lookup,
		store:            store.NewMemoryStore(),
		workspaces:       workspaces,
		hubBase:          upstream.URL,
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
		tenantWorkspaces: staticWorkspaces{"cluster-a": testWorkspace("cluster-a", "org-a", "workspace-a")}.lookup,
		store:            store.NewMemoryStore(),
		workspaces:       workspaces,
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
	request.Header.Set("X-Faros-Tenant", "cluster-a")
	request.Header.Set("Authorization", "Bearer test-token")
	request.Header.Set("X-Faros-Cluster", "cluster-a")
	return request
}

func restoreCheckoutServer(t *testing.T, checkout checkoutToolResult, beforeResponse func()) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Method string `json:"method"`
			Params struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			} `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode MCP request: %v", err)
		}
		if request.Method == "tools/list" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{"tools": []any{}}})
			return
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

// ProjectView reports sourceRevision as a number, but REST callers that carry
// it through shell variables or jq quote it; both spellings must decode.
func TestJSONRevisionAcceptsNumberOrNumericString(t *testing.T) {
	for _, test := range []struct {
		body    string
		want    uint64
		wantErr bool
	}{
		{body: `{"expectedSourceRevision":14}`, want: 14},
		{body: `{"expectedSourceRevision":"14"}`, want: 14},
		{body: `{"expectedSourceRevision":" 14 "}`, want: 14},
		{body: `{"expectedSourceRevision":"fourteen"}`, wantErr: true},
		{body: `{"expectedSourceRevision":""}`, wantErr: true},
		{body: `{"expectedSourceRevision":-1}`, wantErr: true},
		{body: `{"expectedSourceRevision":1.5}`, wantErr: true},
		{body: `{"expectedSourceRevision":true}`, wantErr: true},
	} {
		var req projectRestoreRequest
		err := json.Unmarshal([]byte(test.body), &req)
		if test.wantErr {
			if err == nil {
				t.Errorf("%s: decoded to %v, want an error", test.body, req.ExpectedSourceRevision)
			}
			continue
		}
		if err != nil || req.ExpectedSourceRevision == nil || uint64(*req.ExpectedSourceRevision) != test.want {
			t.Errorf("%s: revision = %v, err = %v; want %d", test.body, req.ExpectedSourceRevision, err, test.want)
		}
	}
	// null keeps the "required" path: the pointer stays nil.
	var req projectRestoreRequest
	if err := json.Unmarshal([]byte(`{"expectedSourceRevision":null}`), &req); err != nil || req.ExpectedSourceRevision != nil {
		t.Fatalf("null revision = %v, err = %v; want nil, nil", req.ExpectedSourceRevision, err)
	}
}

func TestRestoreProjectWorkspaceAcceptsQuotedSourceRevision(t *testing.T) {
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
	currentRevision, err := workspaces.SourceRevision(context.Background(), scope)
	if err != nil {
		t.Fatal(err)
	}
	upstream := restoreCheckoutServer(t, checkoutToolResult{
		CommitSHA: commitSHA,
		Files:     []checkoutToolFile{{Path: "app.txt", Content: "restored\n"}},
	}, nil)
	defer upstream.Close()
	server := &Server{
		tenantWorkspaces:             staticWorkspaces{"cluster-a": testWorkspace("cluster-a", "org-a", "workspace-a")}.lookup,
		store:                        store.NewMemoryStore(),
		workspaces:                   workspaces,
		hubBase:                      upstream.URL,
		projectClientFor:             func(identity) (*asclient.Client, error) { return client, nil },
		developmentSyncAfterMutation: func(identity, *aiv1alpha1.Project, string) error { return nil },
	}

	// A quoted stale revision is decoded, then refused on the mismatch —
	// the 409 semantics do not depend on the spelling.
	response := httptest.NewRecorder()
	server.restoreProjectWorkspace(response, restoreRequestWithBody(fmt.Sprintf(`{"commitSHA":%q,"expectedSourceRevision":"%d"}`, commitSHA, currentRevision-1)))
	if response.Code != http.StatusConflict {
		t.Fatalf("stale quoted revision: response = %d %s, want 409", response.Code, response.Body.String())
	}

	response = httptest.NewRecorder()
	server.restoreProjectWorkspace(response, restoreRequestWithBody(fmt.Sprintf(`{"commitSHA":%q,"expectedSourceRevision":"%d"}`, commitSHA, currentRevision)))
	if response.Code != http.StatusOK {
		t.Fatalf("quoted revision: response = %d %s, want 200", response.Code, response.Body.String())
	}
	var restored projectRestoreResponse
	if err := json.NewDecoder(response.Body).Decode(&restored); err != nil {
		t.Fatal(err)
	}
	if restored.SourceRevision != currentRevision+1 || len(restored.Written) != 1 || restored.Written[0] != "app.txt" {
		t.Fatalf("restore response = %#v", restored)
	}
}

func restoreRequestWithBody(body string) *http.Request {
	request := httptest.NewRequest(http.MethodPost, "/api/projects/shop/restore-workspace", strings.NewReader(body))
	request = mux.SetURLVars(request, map[string]string{"project": "shop"})
	request.Header.Set("X-Faros-Tenant", "cluster-a")
	request.Header.Set("Authorization", "Bearer test-token")
	request.Header.Set("X-Faros-Cluster", "cluster-a")
	return request
}
