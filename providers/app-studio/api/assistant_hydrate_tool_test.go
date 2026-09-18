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
	"sync"
	"testing"
	"time"

	aiv1alpha1 "github.com/railgrid/provider-app-studio/apis/ai/v1alpha1"
	"github.com/railgrid/provider-app-studio/store"
	"github.com/railgrid/provider-app-studio/workspace"
)

// hydrate_workspace is the assistant's repo→workspace path. It is a runtime
// effect that always pauses for approval (it overwrites tracked files), is
// hidden from read-only collaboration modes and from debugging turns, and
// drops same-turn read versions for the files it replaced.
func TestAssistantRegistryExposesHydrateWorkspaceAsApprovedRuntimeEffect(t *testing.T) {
	registry := projectAssistantLocalToolRegistry(nil)
	spec, ok := registry.Spec(projectToolHydrateWorkspace)
	if !ok {
		t.Fatalf("%s is not model-visible", projectToolHydrateWorkspace)
	}
	if spec.Risk != projectAssistantToolRiskRuntime {
		t.Fatalf("risk = %q, want runtime", spec.Risk)
	}
	var schema map[string]any
	if err := json.Unmarshal(spec.Parameters, &schema); err != nil {
		t.Fatalf("decode schema: %v", err)
	}
	if schema["additionalProperties"] != false {
		t.Fatalf("schema permits additional properties: %s", spec.Parameters)
	}
	for _, want := range []string{"overwrites", "approval", "explicitly asks"} {
		if !strings.Contains(spec.Description, want) {
			t.Fatalf("description missing %q: %s", want, spec.Description)
		}
	}
	// Visible with or without the commit bridge: it depends on the Code
	// provider's checkout, not on commit_files.
	for _, tool := range registry.Tools(false) {
		if tool.Spec().Name == projectToolHydrateWorkspace {
			goto visible
		}
	}
	t.Fatal("hydrate_workspace hidden when the commit bridge is absent")
visible:
	for _, mode := range []store.AssistantApprovalMode{store.AssistantApprovalModeOnRequest, store.AssistantApprovalModeAlwaysAsk, store.AssistantApprovalModeAutoApprove} {
		if got := projectAssistantPermissionForV2(spec, mode, nil, nil, false); got != projectAssistantPermissionAsk {
			t.Fatalf("permission under %q = %q, want ask", mode, got)
		}
	}
	if got := projectAssistantPermissionForV2(spec, store.AssistantApprovalModeNever, nil, nil, false); got != projectAssistantPermissionDeny {
		t.Fatalf("permission under never = %q, want deny", got)
	}
	for _, mode := range []projectAssistantCollaborationMode{projectAssistantCollaborationModePlan, projectAssistantCollaborationModeReview} {
		for _, tool := range projectAssistantToolsForCollaborationMode(registry.Tools(false), mode) {
			if tool.Spec().Name == projectToolHydrateWorkspace {
				t.Fatalf("hydrate_workspace offered in read-only mode %q", mode)
			}
		}
	}
	if projectAssistantTurnPolicyForProfile(projectAssistantTurnProfileDebugging).AllowsTool(spec) {
		t.Fatal("hydrate_workspace offered on a debugging turn")
	}
	if !projectAssistantTurnPolicyForProfile(projectAssistantTurnProfileImplementation).AllowsTool(spec) {
		t.Fatal("hydrate_workspace missing from implementation turns")
	}
}

func TestAssistantHydrateWorkspaceToolLoadsRepositoryAndInvalidatesReads(t *testing.T) {
	checkout, _ := json.Marshal(checkoutToolResult{Ref: "feature/x", CommitSHA: "sha-1", Files: []checkoutToolFile{
		{Path: "index.html", Content: "<html>from git</html>\n"},
		{Path: "src/new.ts", Content: "export const fresh = true\n"},
	}, Skipped: []string{"assets/big.bin"}})
	hub := &codeBinaryHub{checkout: string(checkout)}
	upstream := hub.serve(t)
	f := newProjectFilesFixture(t)
	f.server.hubBase = upstream.URL
	// The sync hook runs on the goroutine hydrate schedules after the
	// mutation, so the record is guarded and the test waits for the first
	// call before reading it.
	var syncMu sync.Mutex
	var syncActions []string
	synced := make(chan struct{}, 1)
	f.server.developmentSyncAfterMutation = func(_ identity, _ *aiv1alpha1.Project, action string) error {
		syncMu.Lock()
		syncActions = append(syncActions, action)
		syncMu.Unlock()
		select {
		case synced <- struct{}{}:
		default:
		}
		return nil
	}
	ctx := context.Background()
	for path, content := range map[string]string{"index.html": "<html>local edit</html>\n", "notes.md": "workspace-only\n"} {
		if _, err := f.workspaces.PutFile(ctx, f.scope, workspace.PutOptions{Path: path, Data: []byte(content)}); err != nil {
			t.Fatal(err)
		}
	}
	read := func(path string) string {
		t.Helper()
		data, err := f.workspaces.ReadFileBytes(ctx, f.scope, path, 0)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		return string(data)
	}

	state := newProjectEinoAssistantRunState()
	state.RecordObservedReadFileVersion("index.html", "v-read-1")
	state.RecordObservedReadFileVersion("notes.md", "v-read-2")

	tool, ok := projectAssistantLocalToolRegistry(f.server).Get(projectToolHydrateWorkspace)
	if !ok {
		t.Fatal("hydrate_workspace missing from registry")
	}
	id := identity{tenant: "root:org-a:workspace-a", orgUUID: "org-a", workspaceUUID: "workspace-a", clusterID: "cluster-a"}
	raw, err := tool.Call(ctx, projectAssistantToolCallRequest{
		Identity:       id,
		Project:        f.project,
		WorkspaceScope: f.scope,
		HTTPRequest:    httptest.NewRequest(http.MethodPost, "/", nil),
		RunState:       state,
		Arguments:      map[string]any{"ref": "feature/x"},
	})
	if err != nil {
		t.Fatalf("hydrate_workspace: %v", err)
	}
	var resp projectHydrateResponse
	if err := json.Unmarshal([]byte(raw), &resp); err != nil {
		t.Fatalf("decode result %q: %v", raw, err)
	}
	if resp.Ref != "feature/x" || resp.CommitSHA != "sha-1" || strings.Join(resp.Written, ",") != "index.html,src/new.ts" || strings.Join(resp.Skipped, ",") != "assets/big.bin" {
		t.Fatalf("result = %#v", resp)
	}
	call := hub.calls[len(hub.calls)-1]
	args := call["arguments"].(map[string]any)
	if call["name"] != projectToolCodeCheckoutRepository || args["ref"] != "feature/x" || args["repositoryRef"] != f.project.Spec.Repository.RepositoryRef {
		t.Fatalf("checkout call = %#v", call)
	}
	if got := read("index.html"); got != "<html>from git</html>\n" {
		t.Fatalf("index.html = %q, want repository content", got)
	}
	if got := read("notes.md"); got != "workspace-only\n" {
		t.Fatalf("workspace-only file changed: %q", got)
	}
	if got := read("src/new.ts"); got != "export const fresh = true\n" {
		t.Fatalf("src/new.ts = %q", got)
	}
	if v := state.ReadFileVersion("index.html"); v != "" {
		t.Fatalf("replaced file kept read version %q", v)
	}
	if v := state.ReadFileVersion("notes.md"); v != "v-read-2" {
		t.Fatalf("untouched file lost read version: %q", v)
	}
	select {
	case <-synced:
	case <-time.After(5 * time.Second):
		t.Fatal("workspace sync was not scheduled after hydrate")
	}
	syncMu.Lock()
	got := strings.Join(syncActions, ",")
	syncMu.Unlock()
	if got != projectActionWorkspaceSync {
		t.Fatalf("sync actions = %v, want one workspace sync", got)
	}
}

func TestAssistantHydrateWorkspaceToolRequiresRepositoryAndProject(t *testing.T) {
	f := newProjectFilesFixture(t)
	tool, _ := projectAssistantLocalToolRegistry(f.server).Get(projectToolHydrateWorkspace)
	if _, err := tool.Call(context.Background(), projectAssistantToolCallRequest{WorkspaceScope: f.scope}); err == nil || !strings.Contains(err.Error(), "no project") {
		t.Fatalf("missing project error = %v", err)
	}
	f.project.Spec.Repository = nil
	_, err := tool.Call(context.Background(), projectAssistantToolCallRequest{
		Identity:       identity{clusterID: "cluster-a"},
		Project:        f.project,
		WorkspaceScope: f.scope,
		HTTPRequest:    httptest.NewRequest(http.MethodPost, "/", nil),
	})
	if err == nil || !strings.Contains(err.Error(), "no Code repository") {
		t.Fatalf("missing repository error = %v", err)
	}
}
