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
	"bytes"
	"context"
	"encoding/gob"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	aiv1alpha1 "github.com/faroshq/provider-app-studio/apis/ai/v1alpha1"
	"github.com/faroshq/provider-app-studio/store"
	"github.com/faroshq/provider-app-studio/workspace"
)

func TestNormalizeProjectAssistantExecCommandInput(t *testing.T) {
	tests := []struct {
		name     string
		input    *projectAssistantExecCommandInput
		wantErr  string
		wantWork string
		wantTime int
	}{
		{name: "defaults", input: &projectAssistantExecCommandInput{Component: "backend", Argv: []string{"go", "test", "./..."}}, wantWork: "", wantTime: projectAssistantExecDefaultTimeout},
		{name: "cleans component relative workdir", input: &projectAssistantExecCommandInput{Component: "backend", Argv: []string{"go", "test"}, Workdir: "./internal"}, wantWork: "internal", wantTime: projectAssistantExecDefaultTimeout},
		{name: "rejects parent", input: &projectAssistantExecCommandInput{Component: "backend", Argv: []string{"go", "test"}, Workdir: "../other"}, wantErr: "under the selected component"},
		{name: "rejects absolute", input: &projectAssistantExecCommandInput{Component: "backend", Argv: []string{"go", "test"}, Workdir: "/workspace"}, wantErr: "relative path"},
		{name: "rejects windows separator", input: &projectAssistantExecCommandInput{Component: "backend", Argv: []string{"go", "test"}, Workdir: `internal\\test`}, wantErr: "relative path"},
		{name: "allows explicit interpreter argv", input: &projectAssistantExecCommandInput{Component: "backend", Argv: []string{"sh", "-c", "go test"}}, wantWork: "", wantTime: projectAssistantExecDefaultTimeout},
		{name: "rejects timeout", input: &projectAssistantExecCommandInput{Component: "backend", Argv: []string{"go", "test"}, TimeoutSeconds: projectAssistantExecMaxTimeout + 1}, wantErr: "timeoutSeconds"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, blockers := normalizeProjectAssistantExecCommandInput(tt.input)
			if tt.wantErr == "" {
				if len(blockers) != 0 {
					t.Fatalf("blockers = %v", blockers)
				}
				if got.Workdir != tt.wantWork || got.TimeoutSeconds != tt.wantTime {
					t.Fatalf("normalized input = %#v", got)
				}
				return
			}
			if !strings.Contains(strings.Join(blockers, "; "), tt.wantErr) {
				t.Fatalf("blockers = %v, want %q", blockers, tt.wantErr)
			}
		})
	}
}

func TestProjectAssistantExecCommandContractAndPolicy(t *testing.T) {
	spec, ok := projectAssistantWorkflowToolSpec(projectToolExecCommand)
	if !ok {
		t.Fatal("exec_command workflow spec is missing")
	}
	if spec.Risk != projectAssistantToolRiskRuntime || spec.ParallelSafe {
		t.Fatalf("exec spec = %#v, want effectful exclusive runtime tool", spec)
	}
	if !projectAssistantToolHasEffect(spec) {
		t.Fatal("exec_command must be effectful")
	}
	debugging := projectAssistantTurnPolicyForProfile(projectAssistantTurnProfileDebugging)
	if debugging.AllowsTool(spec) {
		t.Fatal("debugging tool catalog must not expose exec_command")
	}
	implementation := projectAssistantTurnPolicyForProfile(projectAssistantTurnProfileImplementation)
	if !implementation.AllowsTool(spec) {
		t.Fatal("implementation tool catalog must expose exec_command")
	}
	for _, tt := range []struct {
		mode store.AssistantApprovalMode
		want projectAssistantPermissionDecision
	}{
		{mode: "", want: projectAssistantPermissionAllow},
		{mode: store.AssistantApprovalModeOnRequest, want: projectAssistantPermissionAllow},
		{mode: store.AssistantApprovalModeAlwaysAsk, want: projectAssistantPermissionAsk},
		{mode: store.AssistantApprovalModeNever, want: projectAssistantPermissionDeny},
		// Existing runs may still carry this value while the legacy run API is
		// being retired; it remains equivalent to Allow but is not a new
		// preference API option.
		{mode: store.AssistantApprovalModeAutoApprove, want: projectAssistantPermissionAllow},
	} {
		if got := projectAssistantPermissionForV2(spec, tt.mode, nil, nil, false); got != tt.want {
			t.Fatalf("exec_command permission for %q = %q, want %q", tt.mode, got, tt.want)
		}
	}
	if got := projectAssistantToolsForCollaborationMode([]projectAssistantTool{projectAssistantToolFunc{spec: spec}}, projectAssistantCollaborationModePlan); len(got) != 0 {
		t.Fatal("plan mode must hide exec_command")
	}
}

func TestProjectAssistantExecSnapshotRoutesSelectedComponent(t *testing.T) {
	root := t.TempDir()
	files := workspace.NewFileStore(root)
	scope := workspace.Scope{OrgUUID: "org", WorkspaceUUID: "workspace", ProjectName: "project", ProjectUID: "uid"}
	if err := files.ApplyFiles(context.Background(), scope, []workspace.File{
		{Path: "backend/main.go", Content: "package main\n"},
		{Path: "frontend/index.html", Content: "<!doctype html>\n"},
	}); err != nil {
		t.Fatal(err)
	}
	got, digest, revision, err := projectAssistantExecSnapshot(context.Background(), projectAssistantWorkflowRunContext{
		Workspace:      files,
		WorkspaceScope: scope,
	}, projectTemplateComponent{WorkspacePath: "backend"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if digest == "" || revision == 0 || len(got) != 1 || got[0].Path != "main.go" || got[0].Content != "package main\n" {
		t.Fatalf("snapshot = %#v, digest = %q, revision=%d", got, digest, revision)
	}
	wantDigest := projectSandboxSyncDigest([]projectSandboxSyncFile{{Path: "main.go", Content: "package main\n"}})
	if digest != wantDigest {
		t.Fatalf("snapshot digest = %q, component source digest = %q", digest, wantDigest)
	}
}

func TestProjectAssistantExecSyncEvidenceBootstrapsFreshProject(t *testing.T) {
	// A fresh App Studio project has source in the FileStore before the
	// assistant run state has observed a mutation. There is no runtime manifest
	// in this store; the provider-owned sync hook is the authority that must be
	// awaited before exec can address the live component workspace.
	files := workspace.NewFileStore(t.TempDir())
	scope := workspace.Scope{OrgUUID: "org", WorkspaceUUID: "workspace", ProjectName: "project", ProjectUID: "uid"}
	if err := files.ApplyFiles(context.Background(), scope, []workspace.File{{Path: "frontend/index.html", Content: "<!doctype html>\n"}}); err != nil {
		t.Fatal(err)
	}
	calls := make(chan string, 1)
	server := &Server{
		workspaces: files,
		developmentSyncAfterMutation: func(_ identity, _ *aiv1alpha1.Project, name string) error {
			calls <- name
			return nil
		},
	}
	state := newProjectEinoAssistantRunState()
	project := &aiv1alpha1.Project{}
	runCtx := projectAssistantWorkflowRunContext{
		Server:         server,
		Project:        project,
		RunState:       state,
		Workspace:      files,
		WorkspaceScope: scope,
	}

	revision, status, failure := projectAssistantExecSyncEvidence(context.Background(), runCtx)
	if revision != 1 || status != "succeeded" || failure != "" {
		t.Fatalf("fresh-project sync evidence = (%d, %q, %q), want (1, succeeded, empty)", revision, status, failure)
	}
	select {
	case name := <-calls:
		if name != projectActionWorkspaceSync {
			t.Fatalf("bootstrap sync tool = %q, want %q", name, projectActionWorkspaceSync)
		}
	default:
		t.Fatal("fresh-project exec evidence did not schedule initial workspace sync")
	}
	if sourceRevision, _ := state.SourceMutationRevisions(); sourceRevision != 1 {
		t.Fatalf("synthetic bootstrap source revision = %d, want 1", sourceRevision)
	}

	// Once the synthetic bootstrap is settled, another exec preparation in the
	// same run must reuse its evidence rather than enqueueing another sync.
	if revision, status, failure := projectAssistantExecSyncEvidence(context.Background(), runCtx); revision != 1 || status != "succeeded" || failure != "" {
		t.Fatalf("reused fresh-project sync evidence = (%d, %q, %q), want (1, succeeded, empty)", revision, status, failure)
	}
	select {
	case name := <-calls:
		t.Fatalf("fresh-project exec evidence enqueued duplicate sync %q", name)
	default:
	}
}

func TestProjectAssistantExecSnapshotRejectsChangedMutationRevision(t *testing.T) {
	root := t.TempDir()
	files := workspace.NewFileStore(root)
	scope := workspace.Scope{OrgUUID: "org", WorkspaceUUID: "workspace", ProjectName: "project", ProjectUID: "uid"}
	if err := files.ApplyFiles(context.Background(), scope, []workspace.File{{Path: "backend/main.go", Content: "package main\n"}}); err != nil {
		t.Fatal(err)
	}
	runState := newProjectEinoAssistantRunState()
	runState.RecordSourceMutation()
	_, _, _, err := projectAssistantExecSnapshot(context.Background(), projectAssistantWorkflowRunContext{
		Workspace:      files,
		WorkspaceScope: scope,
		RunState:       runState,
	}, projectTemplateComponent{WorkspacePath: "backend"}, 0)
	if !errors.Is(err, errProjectAssistantExecRevisionChanged) {
		t.Fatalf("snapshot error = %v, want mutation revision change", err)
	}
}

func TestProjectAssistantExecResultBoundsOutput(t *testing.T) {
	result := projectAssistantExecResult(projectSandboxExecResponse{SessionID: "session", State: "succeeded", Stdout: strings.Repeat("x", projectAssistantExecMaxOutput+1)}, "backend", 3, "sha256:digest", "succeeded", 2, "")
	if !result.OutputTruncated || len(result.Stdout) == 0 || result.Status != "succeeded" || result.SourceRevision != 3 {
		t.Fatalf("result = %#v", result)
	}
	empty := projectAssistantExecResult(projectSandboxExecResponse{State: "succeeded"}, "backend", 0, "sha256:empty", "succeeded", 0, "")
	if empty.OutputTruncated {
		t.Fatal("empty output must not be marked truncated")
	}
}

func TestProjectAssistantExecRequestIDStable(t *testing.T) {
	first := projectAssistantExecRequestID("run", "call")
	if first == "" || first != projectAssistantExecRequestID("run", "call") || first == projectAssistantExecRequestID("run", "other") {
		t.Fatalf("request IDs = %q", first)
	}
	if anonymous := projectAssistantExecRequestID("", ""); anonymous == "" || anonymous != projectAssistantExecRequestID("", "") {
		t.Fatalf("anonymous request ID = %q", anonymous)
	}
}

func TestProjectAssistantExecCommandInputCheckpointRegistration(t *testing.T) {
	var encoded bytes.Buffer
	original := any(&projectAssistantExecCommandInput{Component: "backend", Argv: []string{"go", "test"}})
	if err := gob.NewEncoder(&encoded).Encode(&original); err != nil {
		t.Fatalf("encode registered exec input: %v", err)
	}
	var decoded any
	if err := gob.NewDecoder(&encoded).Decode(&decoded); err != nil {
		t.Fatalf("decode registered exec input: %v", err)
	}
	got, ok := decoded.(*projectAssistantExecCommandInput)
	if !ok || got.Component != "backend" || len(got.Argv) != 2 {
		t.Fatalf("decoded exec input = %#v (%T)", decoded, decoded)
	}
}

func TestProjectAssistantExecMetadataIsStructuredAndDoesNotExposeSecrets(t *testing.T) {
	metadata := projectAssistantExecMetadataForToolArguments(projectToolExecCommand, map[string]any{
		"component":      "backend",
		"argv":           []any{"go", "test", "--token", "super-secret"},
		"workdir":        "internal",
		"timeoutSeconds": float64(42),
	}, `{"status":"failed","summary":"Command failed in component \"backend\".","exitCode":2,"durationMs":123}`, "failed")
	if metadata == nil || metadata.Component != "backend" || metadata.Workdir != "internal" || metadata.TimeoutSeconds != 42 ||
		metadata.NetworkProfile != "application-runtime" || metadata.AuthorityProfile != "application-container" || metadata.WritebackPolicy != "runtime-workspace-only" || metadata.ExitCode == nil || *metadata.ExitCode != 2 {
		t.Fatalf("metadata = %#v", metadata)
	}
	if len(metadata.Argv) != 4 || metadata.Argv[3] != "[redacted]" {
		t.Fatalf("metadata argv = %#v, want credential value redacted", metadata.Argv)
	}
	raw, err := json.Marshal(metadata)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "super-secret") {
		t.Fatalf("metadata leaked argv secret: %s", raw)
	}
}

func TestProjectAssistantExecRequestUsesPersistentWorkspaceAuthority(t *testing.T) {
	raw, err := json.Marshal(projectSandboxExecRequest{
		Action:         "start",
		RequestID:      "request-1",
		Argv:           []string{"go", "test", "./..."},
		Workdir:        "internal",
		TimeoutSeconds: 30,
		SourceRevision: 7,
		SourceDigest:   "digest-7",
	})
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]any
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatal(err)
	}
	if _, ok := wire["files"]; ok {
		t.Fatalf("persistent exec request unexpectedly carries source files: %s", raw)
	}
	if got, ok := wire["sourceRevision"].(float64); !ok || got != 7 {
		t.Fatalf("sourceRevision = %#v, want 7", wire["sourceRevision"])
	}
	if got, ok := wire["sourceDigest"].(string); !ok || got != "digest-7" {
		t.Fatalf("sourceDigest = %#v, want digest-7", wire["sourceDigest"])
	}
}

func TestProjectAssistantExecApprovalMetadataSurvivesMultiCallCheckpoint(t *testing.T) {
	state := projectAssistantCheckpointState{
		ToolCalls: []chatToolCall{
			{ID: "exec-1", Type: "function", Function: chatToolCallFunction{Name: projectToolExecCommand, Arguments: `{"component":"backend","argv":["go","test"]}`}},
			{ID: "exec-2", Type: "function", Function: chatToolCallFunction{Name: projectToolExecCommand, Arguments: `{"component":"frontend","argv":["npm","test"]}`}},
		},
		CurrentIndex: 1,
	}
	raw, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	var restored projectAssistantCheckpointState
	if err := json.Unmarshal(raw, &restored); err != nil {
		t.Fatal(err)
	}
	for index, call := range restored.ToolCalls {
		permission := projectAssistantPermissionForCall("permission-"+string(rune('1'+index)), call, projectAssistantToolSpec{Name: projectToolExecCommand})
		if permission.Exec == nil || permission.Exec.Component == "" || permission.Exec.AuthorityProfile != "application-container" || permission.Exec.NetworkProfile != "application-runtime" {
			t.Fatalf("checkpoint call %d permission metadata = %#v", index, permission)
		}
		if permission.Exec.Status != "permission_required" {
			t.Fatalf("checkpoint call %d permission status = %q", index, permission.Exec.Status)
		}
	}
}

func TestProjectAssistantExecActionFeedMergesTerminalResultsAcrossCheckpoints(t *testing.T) {
	calls := []struct {
		id        string
		component string
		argv      []string
		result    string
		exitCode  int
		duration  int64
	}{
		{
			id:        "exec-install",
			component: "frontend",
			argv:      []string{"npm", "install"},
			result:    `{"status":"succeeded","summary":"Command succeeded in component \"frontend\".","exitCode":0,"durationMs":742,"outputTruncated":true}`,
			exitCode:  0,
			duration:  742,
		},
		{
			id:        "exec-test",
			component: "backend",
			argv:      []string{"go", "test", "./..."},
			result:    `{"status":"failed","summary":"Command failed in component \"backend\".","exitCode":1,"durationMs":1834}`,
			exitCode:  1,
			duration:  1834,
		},
	}
	var events []projectToolCallStreamEvent
	for _, call := range calls {
		args := map[string]any{"component": call.component, "argv": call.argv}
		permission := projectToolCallStreamEvent{
			ID:        call.id,
			Name:      projectToolExecCommand,
			Status:    "permission_required",
			Arguments: projectEinoToolArgumentsString(args),
			Permission: &projectAssistantPermission{
				ToolCallID: call.id,
				ToolName:   projectToolExecCommand,
				Exec:       projectAssistantExecMetadataForToolArguments(projectToolExecCommand, args, "", "permission_required"),
			},
		}
		events = upsertProjectToolCallStreamEvent(events, permission)
		// The checkpoint callback intentionally carries only lifecycle data.
		events = upsertProjectToolCallStreamEvent(events, projectToolCallStreamEvent{
			ID:         call.id,
			Status:     "permission_required",
			Checkpoint: &projectAssistantCheckpoint{ID: "checkpoint-" + call.id},
		})
		terminal := projectToolCallStreamEvent{
			ID:        call.id,
			Name:      projectToolExecCommand,
			Status:    map[bool]string{true: "succeeded", false: "failed"}[call.exitCode == 0],
			Arguments: projectEinoToolArgumentsString(args),
			Summary:   "terminal result",
			Exec:      projectAssistantExecMetadataForToolArguments(projectToolExecCommand, args, call.result, map[bool]string{true: "succeeded", false: "failed"}[call.exitCode == 0]),
		}
		events = upsertProjectToolCallStreamEvent(events, terminal)
	}
	actions := projectAssistantActionFeedFromToolCalls(events)
	if len(actions) != len(calls) {
		t.Fatalf("actions = %#v, want %d terminal exec actions", actions, len(calls))
	}
	for index, call := range calls {
		action := actions[index]
		if action.Status != map[bool]string{true: "succeeded", false: "failed"}[call.exitCode == 0] {
			t.Fatalf("action %d status = %q, want terminal status", index, action.Status)
		}
		if action.Exec == nil {
			t.Fatalf("action %d lost exec metadata: %#v", index, action)
		}
		if action.Exec.Component != call.component || action.Exec.AuthorityProfile != "application-container" ||
			action.Exec.NetworkProfile != "application-runtime" || action.Exec.WritebackPolicy != "runtime-workspace-only" {
			t.Fatalf("action %d request disclosure = %#v", index, action.Exec)
		}
		if action.Exec.Status != map[bool]string{true: "succeeded", false: "failed"}[call.exitCode == 0] ||
			action.Exec.ExitCode == nil || *action.Exec.ExitCode != call.exitCode || action.Exec.DurationMS != call.duration {
			t.Fatalf("action %d terminal disclosure = %#v", index, action.Exec)
		}
		if call.exitCode == 0 && !action.Exec.OutputTruncated {
			t.Fatalf("action %d lost output truncation flag", index)
		}
	}

	// A terminal outer action must never leave a stale permission lifecycle in
	// the nested disclosure when an older checkpoint did not produce a fresh
	// terminal callback.
	stale := projectAssistantActionFeedItemFromToolCall(projectToolCallStreamEvent{
		ID:     "exec-stale",
		Name:   projectToolExecCommand,
		Status: "permission_required",
		Exec:   projectAssistantExecMetadataForToolArguments(projectToolExecCommand, map[string]any{"component": "backend", "argv": []string{"go", "test"}}, "", "permission_required"),
	})
	stale.Status = projectAssistantActionFeedStatusSucceeded
	final := finalizeProjectAssistantActionFeed([]projectAssistantActionFeedItem{stale}, store.AssistantRunStatusCompleted)
	if len(final) != 1 || final[0].Exec == nil || final[0].Exec.Status != "succeeded" {
		t.Fatalf("finalized stale exec action = %#v, want succeeded nested status", final)
	}
	preserved := stale
	preserved.Exec = cloneProjectAssistantExecMetadata(stale.Exec)
	preserved.Exec.Status = "timed_out"
	final = finalizeProjectAssistantActionFeed([]projectAssistantActionFeedItem{preserved}, store.AssistantRunStatusCompleted)
	if len(final) != 1 || final[0].Exec == nil || final[0].Exec.Status != "timed_out" {
		t.Fatalf("finalized terminal exec action = %#v, want explicit timed_out status preserved", final)
	}
}
