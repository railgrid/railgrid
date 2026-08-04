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

	"github.com/faroshq/provider-app-studio/store"
	"github.com/faroshq/provider-app-studio/workspace"
)

func writeTestWorkspaceFiles(t *testing.T, ctx context.Context, workspaces *workspace.FileStore, scope workspace.Scope, files []workspace.File) {
	t.Helper()
	for _, file := range files {
		if _, err := workspaces.WriteFile(ctx, scope, workspace.WriteOptions{Path: file.Path, Content: file.Content}); err != nil {
			t.Fatalf("write workspace file %q: %v", file.Path, err)
		}
	}
}

func TestAssistantEditFileUsesLiveWorkspaceAndReturnsDiff(t *testing.T) {
	workspaces := workspace.NewFileStore(t.TempDir())
	scope := workspace.Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: "demo", ProjectUID: "test-project-uid"}
	writeTestWorkspaceFiles(t, context.Background(), workspaces, scope, []workspace.File{{
		Path:    "src/app.js",
		Content: "const theme = 'light'\n",
	}})
	edit, ok := projectAssistantLocalToolRegistry(&Server{workspaces: workspaces}).Get(projectToolEditFile)
	if !ok {
		t.Fatal("edit_file tool was not registered")
	}
	req := projectAssistantToolCallRequest{
		WorkspaceScope: scope,
		AssistantRunID: "run-1",
		RunState:       newProjectEinoAssistantRunState(),
		Arguments:      map[string]any{"path": "src/app.js", "oldString": "const theme = 'light'", "newString": "const theme = 'dark'"},
	}
	read, err := workspaces.ReadFile(context.Background(), scope, workspace.ReadOptions{Path: "src/app.js"})
	if err != nil {
		t.Fatal(err)
	}
	req.RunState.RecordObservedReadFileVersion(read.Path, read.Version)
	req.Arguments["expectedVersion"] = read.Version
	result, err := edit.Call(context.Background(), req)
	if err != nil {
		t.Fatalf("edit returned error: %v", err)
	}
	var decoded workspace.MutationResult
	if err := json.Unmarshal([]byte(result), &decoded); err != nil {
		t.Fatalf("decode mutation result: %v", err)
	}
	if decoded.Path != "src/app.js" || decoded.Additions != 1 || decoded.Deletions != 1 ||
		decoded.Replacements != 1 || !strings.Contains(decoded.Diff, "+++ b/src/app.js") {
		t.Fatalf("mutation result = %#v", decoded)
	}
	mutation := projectAssistantMutationFromResult(projectToolEditFile, result)
	if mutation == nil || mutation.Additions != 1 || mutation.Deletions != 1 || mutation.Replacements != 1 {
		t.Fatalf("mutation trace = %#v", mutation)
	}
	if !strings.Contains(mutation.Diff, "+++ b/src/app.js") || mutation.DiffTruncated {
		t.Fatalf("mutation diff = %#v", mutation)
	}
	summary := summarizeProjectToolResult(projectToolEditFile, result)
	item := projectAssistantActionFeedItemFromToolCall(projectToolCallStreamEvent{
		ID:        "edit-1",
		Name:      projectToolEditFile,
		Status:    "succeeded",
		Arguments: `{"path":"./src/app.js","oldString":"...","newString":"..."}`,
		Summary:   summary,
		Mutation:  mutation,
	})
	if item.Target != "src/app.js" {
		t.Fatalf("action target = %q, want server-normalized mutation path", item.Target)
	}
	if item.Outcome != "+1 -1" || strings.Contains(item.Outcome, "const theme") {
		t.Fatalf("action outcome = %q, want counts only", item.Outcome)
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
	messages, scope := newAssistantRunEventLedgerTestStore(t, "run-read-telemetry")
	calls := 0
	middleware := projectEinoAssistantFilesystemTelemetryMiddleware(projectAssistantRunRequest{
		AssistantRun: &store.AssistantRun{ID: "run-read-telemetry", Mode: store.AssistantRunModeDefault},
		eventLedger:  newProjectAssistantRunEventLedger(messages, scope, "run-read-telemetry"),
	}, state)
	wrapped, err := middleware.WrapInvokableToolCall(
		context.Background(),
		func(context.Context, string, ...einotool.Option) (string, error) {
			calls++
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

	secondMessages, secondScope := newAssistantRunEventLedgerTestStore(t, "run-read-telemetry-2")
	secondMiddleware := projectEinoAssistantFilesystemTelemetryMiddleware(projectAssistantRunRequest{
		AssistantRun: &store.AssistantRun{ID: "run-read-telemetry-2", Mode: store.AssistantRunModeDefault},
		eventLedger:  newProjectAssistantRunEventLedger(secondMessages, secondScope, "run-read-telemetry-2"),
	}, state)
	second, err := secondMiddleware.WrapInvokableToolCall(
		context.Background(),
		func(context.Context, string, ...einotool.Option) (string, error) {
			calls++
			return "     1\tconst theme = 'light'\n", nil
		},
		&adk.ToolContext{Name: projectToolReadFile},
	)
	if err != nil {
		t.Fatalf("second WrapInvokableToolCall returned error: %v", err)
	}
	if _, err := second(context.Background(), `{"file_path":"./src/app.js","limit":2000}`); err != nil {
		t.Fatalf("repeated read_file returned error: %v", err)
	}
	if calls != 2 {
		t.Fatalf("read endpoint calls = %d, want repeated reads dispatched", calls)
	}
}

func TestAssistantFilesystemFailureIsDurableModelFeedback(t *testing.T) {
	state := newProjectEinoAssistantRunState()
	messages, scope := newAssistantRunEventLedgerTestStore(t, "run-read-failure")
	calls := 0
	wrap := func(ledger *projectAssistantRunEventLedger) adk.InvokableToolCallEndpoint {
		middleware := projectEinoAssistantFilesystemTelemetryMiddleware(projectAssistantRunRequest{
			AssistantRun: &store.AssistantRun{ID: "run-read-failure", Mode: store.AssistantRunModeDefault},
			eventLedger:  ledger,
		}, state)
		wrapped, err := middleware.WrapInvokableToolCall(
			context.Background(),
			func(context.Context, string, ...einotool.Option) (string, error) {
				calls++
				return "", errors.New("source file is temporarily unavailable")
			},
			&adk.ToolContext{Name: projectToolReadFile},
		)
		if err != nil {
			t.Fatal(err)
		}
		return wrapped
	}
	arguments := `{"file_path":"src/app.js","limit":2000}`
	result, err := wrap(newProjectAssistantRunEventLedger(messages, scope, "run-read-failure"))(context.Background(), arguments)
	if err != nil || !strings.Contains(result, "Tool call failed: source file is temporarily unavailable") {
		t.Fatalf("read failure = (%q, %v), want model-visible feedback", result, err)
	}
	replayed, err := wrap(newProjectAssistantRunEventLedger(messages, scope, "run-read-failure"))(context.Background(), arguments)
	if err != nil || replayed != result {
		t.Fatalf("replayed read failure = (%q, %v), want %q", replayed, err, result)
	}
	if calls != 1 {
		t.Fatalf("read endpoint calls = %d, want durable failure replay without redispatch", calls)
	}
	events := listAssistantRunEventLedgerEvents(t, messages, scope, "run-read-failure")
	if len(events) != 2 {
		t.Fatalf("events = %#v, want call and failed result", events)
	}
	var payload projectAssistantRunToolResultPayload
	if err := json.Unmarshal(events[1].Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if !payload.Failed || payload.Result != result {
		t.Fatalf("durable read failure = %#v", payload)
	}
}
