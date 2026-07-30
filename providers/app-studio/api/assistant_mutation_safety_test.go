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
	"errors"
	"strings"
	"testing"

	"github.com/cloudwego/eino/adk"
	einotool "github.com/cloudwego/eino/components/tool"

	"github.com/faroshq/provider-app-studio/workspace"
)

func TestAssistantMutationToolsFenceExistingFiles(t *testing.T) {
	workspaces := workspace.NewFileStore(t.TempDir())
	scope := workspace.Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: "demo"}
	if err := workspaces.ApplyFiles(context.Background(), scope, []workspace.File{{
		Path:    "src/app.js",
		Content: "const theme = 'light'\n",
	}}); err != nil {
		t.Fatalf("ApplyFiles returned error: %v", err)
	}
	registry := projectAssistantLocalToolRegistry(&Server{workspaces: workspaces})
	write, ok := registry.Get(projectToolWriteFile)
	if !ok {
		t.Fatal("write_file tool was not registered")
	}
	if _, err := write.Call(context.Background(), projectAssistantToolCallRequest{
		WorkspaceScope:        scope,
		EnforceMutationSafety: true,
		Arguments: map[string]any{
			"path":    "src/app.js",
			"content": "const theme = 'dark'\n",
		},
	}); err == nil || !strings.Contains(err.Error(), "create-only") {
		t.Fatalf("follow-up write error = %v, want create-only rejection", err)
	}
	if _, err := write.Call(context.Background(), projectAssistantToolCallRequest{
		WorkspaceScope: scope,
		AssistantRunID: "run-write",
		InitialBuild:   true,
		Arguments: map[string]any{
			"path":    "src/app.js",
			"content": "const theme = 'dark'\n",
		},
	}); err != nil {
		t.Fatalf("initial-build write returned error: %v", err)
	}
	if _, err := workspaces.RestoreSnapshot(context.Background(), scope, "run-write"); !errors.Is(err, workspace.ErrSnapshotNotFound) {
		t.Fatalf("assistant write snapshot error = %v, want ErrSnapshotNotFound", err)
	}
}

func TestAssistantApplyPatchRequiresSameTurnReadAndReturnsDiff(t *testing.T) {
	workspaces := workspace.NewFileStore(t.TempDir())
	scope := workspace.Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: "demo"}
	if err := workspaces.ApplyFiles(context.Background(), scope, []workspace.File{{
		Path:    "src/app.js",
		Content: "const theme = 'light'\n",
	}}); err != nil {
		t.Fatalf("ApplyFiles returned error: %v", err)
	}
	patch, ok := projectAssistantLocalToolRegistry(&Server{workspaces: workspaces}).Get(projectToolApplyPatch)
	if !ok {
		t.Fatal("apply_patch tool was not registered")
	}
	req := projectAssistantToolCallRequest{
		WorkspaceScope:        scope,
		AssistantRunID:        "run-1",
		EnforceMutationSafety: true,
		Arguments: map[string]any{
			"path":    "src/app.js",
			"oldText": "'light'",
			"newText": "'dark'",
		},
	}
	if _, err := patch.Call(context.Background(), req); err == nil || !strings.Contains(err.Error(), "read_file") {
		t.Fatalf("patch error = %v, want read_file rejection", err)
	}
	req.ObservedReadFiles = []string{"src/app.js", "src/other.js"}
	result, err := patch.Call(context.Background(), req)
	if err != nil {
		t.Fatalf("patch after read returned error: %v", err)
	}
	var decoded workspace.MutationResult
	if err := json.Unmarshal([]byte(result), &decoded); err != nil {
		t.Fatalf("decode mutation result: %v", err)
	}
	if decoded.Path != "src/app.js" || decoded.Additions != 1 || decoded.Deletions != 1 ||
		decoded.Replacements != 1 || !strings.Contains(decoded.Patch, "+++ b/src/app.js") {
		t.Fatalf("mutation result = %#v", decoded)
	}
	mutation := projectAssistantMutationFromResult(projectToolApplyPatch, result)
	if mutation == nil || mutation.Additions != 1 || mutation.Deletions != 1 || mutation.Replacements != 1 {
		t.Fatalf("mutation trace = %#v", mutation)
	}
	if !strings.Contains(mutation.Patch, "+++ b/src/app.js") || mutation.PatchTruncated {
		t.Fatalf("mutation patch = %#v", mutation)
	}
	summary := summarizeProjectToolResult(projectToolApplyPatch, result)
	item := projectAssistantActionFeedItemFromToolCall(projectToolCallStreamEvent{
		ID:        "patch-1",
		Name:      projectToolApplyPatch,
		Status:    "succeeded",
		Arguments: `{"path":"src/app.js"}`,
		Summary:   summary,
		Mutation:  mutation,
	})
	if item.Outcome != "+1 -1" || strings.Contains(item.Outcome, "const theme") {
		t.Fatalf("action outcome = %q, want counts only", item.Outcome)
	}
	if _, err := workspaces.RestoreSnapshot(context.Background(), scope, req.AssistantRunID); !errors.Is(err, workspace.ErrSnapshotNotFound) {
		t.Fatalf("assistant patch snapshot error = %v, want ErrSnapshotNotFound", err)
	}
}

func TestAssistantObservedReadPathsSurviveMutationAndCheckpoint(t *testing.T) {
	state := newProjectEinoAssistantRunState()
	state.RecordObservedReadFile("src/app.js")
	state.RecordObservedReadFile("src/theme.js")
	state.RecordSourceMutation()
	if got := state.ObservedReadFilePaths(); strings.Join(got, ",") != "src/app.js,src/theme.js" {
		t.Fatalf("observed reads after mutation = %v", got)
	}

	restored := newProjectEinoAssistantRunState()
	restored.RestoreCheckpointState(state.CheckpointState())
	if got := restored.ObservedReadFilePaths(); strings.Join(got, ",") != "src/app.js,src/theme.js" {
		t.Fatalf("observed reads after checkpoint = %v", got)
	}
}

func TestAssistantFilesystemTelemetryRecordsObservedReadPath(t *testing.T) {
	state := newProjectEinoAssistantRunState()
	middleware := projectEinoAssistantFilesystemTelemetryMiddleware(projectAssistantRunRequest{}, state)
	wrapped, err := middleware.WrapInvokableToolCall(
		context.Background(),
		func(context.Context, string, ...einotool.Option) (string, error) {
			return "     1\tconst theme = 'light'\n", nil
		},
		&adk.ToolContext{Name: projectToolReadFile},
	)
	if err != nil {
		t.Fatalf("WrapInvokableToolCall returned error: %v", err)
	}
	if _, err := wrapped(context.Background(), `{"file_path":"./src/app.js","limit":2000}`); err != nil {
		t.Fatalf("read_file returned error: %v", err)
	}
	if got := state.ObservedReadFilePaths(); len(got) != 1 || got[0] != "src/app.js" {
		t.Fatalf("observed reads = %v, want src/app.js", got)
	}
}
