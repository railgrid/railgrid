// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	aiv1alpha1 "github.com/faroshq/provider-app-studio/apis/ai/v1alpha1"
	asclient "github.com/faroshq/provider-app-studio/client"
	"github.com/faroshq/provider-app-studio/store"
	"github.com/faroshq/provider-app-studio/workspace"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func TestProjectAdoptionPreservesRetainedSource(t *testing.T) {
	for _, state := range []string{"retained", "deleted", "no-git", "missing", "metadata-only", "stale", "new-incarnation"} {
		t.Run(state, func(t *testing.T) {
			ctx := context.Background()
			checkouts := 0
			hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				checkouts++
				w.Header().Set("Content-Type", "application/json")
				payload := `{"ref":"main","commitSHA":"git-sha","files":[{"path":"index.ts","content":"Git source"}]}`
				_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{"content": []any{map[string]any{"type": "text", "text": payload}}}})
			}))
			defer hub.Close()
			p := &aiv1alpha1.Project{ObjectMeta: metav1.ObjectMeta{Name: "demo", UID: types.UID("uid-1")}, Spec: aiv1alpha1.ProjectSpec{Repository: &aiv1alpha1.ProjectRepositoryBinding{RepositoryRef: "demo-repo"}}}
			if state == "no-git" {
				p.Spec.Repository = nil
			}
			c := newProjectCreationTestClient()
			p, err := c.Projects().Create(ctx, p, metav1.CreateOptions{})
			if err != nil {
				t.Fatal(err)
			}
			id := identity{orgUUID: "org-a", workspaceUUID: "ws-1", clusterID: "cluster-a"}
			root := t.TempDir()
			disk := workspace.NewFileStore(root)
			scope := projectWorkspaceScope(id, p)
			floor := uint64(1)
			if state == "metadata-only" {
				floor = 17
				if err := disk.EnsureSourceRevisionFloor(ctx, scope, floor); err != nil {
					t.Fatal(err)
				}
			} else if state != "missing" {
				if _, err := disk.WriteFile(ctx, scope, workspace.WriteOptions{Path: "index.ts", Content: "uncommitted source"}); err != nil {
					t.Fatal(err)
				}
				if state == "deleted" {
					file, err := disk.ReadFile(ctx, scope, workspace.ReadOptions{Path: "index.ts"})
					if err != nil {
						t.Fatal(err)
					}
					if _, err := disk.DeleteFile(ctx, scope, workspace.DeleteOptions{Path: "index.ts", ExpectedVersion: file.Version}); err != nil {
						t.Fatal(err)
					}
				}
				if _, err := disk.AddUncommittedPaths(ctx, scope, []string{"index.ts"}); err != nil {
					t.Fatal(err)
				}
				var err error
				floor, err = disk.SourceRevision(ctx, scope)
				if err != nil {
					t.Fatal(err)
				}
				if state == "stale" {
					floor++
				}
			}
			if state == "new-incarnation" {
				p.UID = "uid-2"
				var err error
				p, err = c.Projects().Update(ctx, p, metav1.UpdateOptions{})
				if err != nil {
					t.Fatal(err)
				}
				scope = projectWorkspaceScope(id, p)
			}
			// Reopen the same volume, with a different pod identity and IP.
			s := &Server{hubBase: hub.URL, workspaces: workspace.NewFileStore(root), projectClientFor: func(identity) (*asclient.Client, error) { return c, nil }}
			s.SetReplicaRouting("new-pod", "10.0.0.2:8091", "")
			req := httptest.NewRequest(http.MethodGet, "/api/projects/demo/files", nil)
			s.adoptProject(req, id, p.Name, store.ReplicaClaim{OwnerReplica: "old-pod", OwnerAddr: "10.0.0.1:8091"}, true, store.ReplicaClaim{Revision: int64(floor)})
			retained := state == "retained" || state == "deleted" || state == "no-git"
			if retained && checkouts != 0 {
				t.Fatalf("retained source triggered %d Git checkouts", checkouts)
			}
			if !retained && checkouts != 1 {
				t.Fatalf("missing/stale source triggered %d Git checkouts", checkouts)
			}
			got, err := s.workspaces.ReadFile(ctx, scope, workspace.ReadOptions{Path: "index.ts"})
			if state == "deleted" {
				if !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("deleted file restored: %#v %v", got, err)
				}
			} else {
				expected := "Git source"
				if retained {
					expected = "uncommitted source"
				}
				if err != nil || got.Content != expected {
					t.Fatalf("source=%#v err=%v; want %q", got, err, expected)
				}
			}
			if retained {
				paths, err := s.workspaces.UncommittedPaths(ctx, scope)
				if err != nil || len(paths) != 1 || paths[0] != "index.ts" {
					t.Fatalf("dirty source lost: %v %v", paths, err)
				}
			}
		})
	}
}
