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
	"strings"
	"testing"
	"time"

	aiv1alpha1 "github.com/faroshq/provider-app-studio/apis/ai/v1alpha1"
	"github.com/faroshq/provider-app-studio/store"
	"github.com/faroshq/provider-app-studio/workspace"
)

// A replica that takes a project over rebuilds the tree from git. When the
// claim carried a source revision, uncommitted edits from earlier turns were
// dropped by that rebuild, yet the conversation still describes them. The
// rebuild used to be a log line only; the assistant then told the user the
// deck was "still present in the workspace" while the sandbox served an empty
// tree. Verification must lead with the rebuild until a mutation lands on the
// rebuilt tree.
func TestWorkspaceRebuildSurfacesAsVerificationBlockerUntilNextMutation(t *testing.T) {
	server := NewWithWorkspace(nil, store.NewMemoryStore(), workspace.NewFileStore(t.TempDir()), "", false)
	server.tenantWorkspaces = defaultTestWorkspaces.lookup
	id := identity{orgUUID: "org-a", workspaceUUID: "ws-1"}
	project := &aiv1alpha1.Project{}
	project.Name = "pitch"
	project.UID = "project-uid-pitch"
	runCtx := projectAssistantWorkflowRunContext{Server: server, Project: project, Identity: id}

	if got := projectAssistantWorkspaceRebuild(runCtx); got != "" {
		t.Fatalf("projectAssistantWorkspaceRebuild = %q, want empty before any rebuild", got)
	}

	server.recordWorkspaceRebuild(id, project, workspaceRebuildNotice{
		CommitSHA:       "e1cf71c618b43739445ee834c338b5bc396507ea",
		Files:           1,
		DroppedRevision: 7,
		At:              time.Date(2026, 9, 8, 15, 7, 19, 0, time.UTC),
		PreviousOwner:   "app-studio-594bb4bf9f-2j2pk_1",
	})
	blocker := projectAssistantWorkspaceRebuild(runCtx)
	for _, want := range []string{"e1cf71c618b4", "(1 files)", "2026-09-08T15:07:19Z", "revision 7", "Re-read files"} {
		if !strings.Contains(blocker, want) {
			t.Errorf("rebuild blocker lacks %q:\n%s", want, blocker)
		}
	}
	if strings.Contains(blocker, "e1cf71c618b43739") {
		t.Errorf("rebuild blocker should abbreviate the commit:\n%s", blocker)
	}

	readyRuntime := &projectAssistantRuntimeWorkflowResult{
		Status:     "ready",
		Summary:    "runtime is ready",
		PreviewURL: "https://pitch.example",
	}
	result, err := formatProjectAssistantRuntimeVerification(context.Background(), &projectAssistantRuntimeVerificationContext{
		RunContext: runCtx,
		Runtime:    readyRuntime,
	})
	if err != nil {
		t.Fatalf("formatProjectAssistantRuntimeVerification: %v", err)
	}
	if result.Status != "not_ready" {
		t.Errorf("status = %q, want not_ready — a ready sandbox serving the rebuilt tree is not running the conversation's work", result.Status)
	}
	if !strings.Contains(result.Summary, "rebuilt from git") {
		t.Errorf("summary = %q, want it to name the rebuild", result.Summary)
	}
	if len(result.Blockers) != 1 || result.Blockers[0] != blocker {
		t.Errorf("blockers = %q, want exactly the rebuild notice", result.Blockers)
	}

	// A failed sync on top of the rebuild lists both, rebuild first: it is the
	// older and larger loss.
	server.recordDevelopmentSyncFailure(id, project, "the last workspace sync after replace_file failed: no package.json")
	result, err = formatProjectAssistantRuntimeVerification(context.Background(), &projectAssistantRuntimeVerificationContext{
		RunContext: runCtx,
		Runtime:    readyRuntime,
	})
	if err != nil {
		t.Fatalf("formatProjectAssistantRuntimeVerification: %v", err)
	}
	if result.Status != "not_ready" || len(result.Blockers) != 2 {
		t.Fatalf("status=%q blockers=%q, want not_ready with rebuild and sync blockers", result.Status, result.Blockers)
	}
	if result.Blockers[0] != blocker || !strings.Contains(result.Blockers[1], "no package.json") {
		t.Errorf("blockers = %q, want rebuild notice then sync failure", result.Blockers)
	}
	if !strings.Contains(result.Summary, "rebuilt from git") || !strings.Contains(result.Summary, "last sync failed") {
		t.Errorf("summary = %q, want both causes named", result.Summary)
	}
	server.clearDevelopmentSyncFailure(id, project)

	// The next mutation on the rebuilt tree retires the notice: the assistant
	// is now editing what is actually on disk.
	server.developmentSyncAfterMutation = func(identity, *aiv1alpha1.Project, string) error { return nil }
	done := make(chan struct{})
	scheduled := server.scheduleDevelopmentSyncAfterMutationWithCompletion(id, project, "replace_file", func(error) { close(done) })
	if !scheduled {
		t.Fatal("scheduleDevelopmentSyncAfterMutationWithCompletion returned false for a mutating tool")
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("development sync completion did not fire")
	}
	if got := projectAssistantWorkspaceRebuild(runCtx); got != "" {
		t.Fatalf("rebuild notice survived a mutation: %q", got)
	}
	result, err = formatProjectAssistantRuntimeVerification(context.Background(), &projectAssistantRuntimeVerificationContext{
		RunContext: runCtx,
		Runtime:    readyRuntime,
	})
	if err != nil {
		t.Fatalf("formatProjectAssistantRuntimeVerification: %v", err)
	}
	if result.Status != "ready" {
		t.Errorf("status = %q after the mutation cleared the notice, want ready (blockers %q)", result.Status, result.Blockers)
	}
}

func TestWorkspaceRebuildNoticeToleratesMissingDetail(t *testing.T) {
	blocker := workspaceRebuildNotice{DroppedRevision: 3}.Blocker()
	if !strings.Contains(blocker, "the repository head (0 files)") || strings.Contains(blocker, " at ") {
		t.Errorf("blocker without commit or time = %q", blocker)
	}
	var server *Server
	server.recordWorkspaceRebuild(identity{}, &aiv1alpha1.Project{}, workspaceRebuildNotice{})
	server.clearWorkspaceRebuild(identity{}, &aiv1alpha1.Project{})
	if _, ok := server.lastWorkspaceRebuild(identity{}, &aiv1alpha1.Project{}); ok {
		t.Fatal("nil server reported a rebuild")
	}
	if got := projectAssistantWorkspaceRebuild(projectAssistantWorkflowRunContext{}); got != "" {
		t.Fatalf("empty run context reported a rebuild: %q", got)
	}
}
