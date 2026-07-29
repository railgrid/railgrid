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
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	asclient "github.com/faroshq/provider-app-studio/client"
	"github.com/faroshq/provider-app-studio/store"
	"github.com/faroshq/provider-app-studio/workspace"
)

func TestProjectAssistantStreamWriterMapsDeltaToUIDataModelUpdate(t *testing.T) {
	got, err := collectProjectAssistantStreamEvents(projectAssistantEvent{
		Type:  projectAssistantEventMessageDelta,
		Delta: "hello",
	})
	if err != nil {
		t.Fatalf("EmitProjectAssistantEvent returned error: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("events = %#v, want beginRendering, assistant shell, and dataModelUpdate", got)
	}
	if got[0].Type != "" || got[0].BeginRendering == nil || got[0].BeginRendering.SurfaceID != "assistant-1" {
		t.Fatalf("first event = %#v, want beginRendering UI event", got[0])
	}
	assertA2UICard(t, got[1], "assistant", "")
	if got[2].Type != "" || got[2].DataModelUpdate == nil || got[2].DataModelUpdate.SurfaceID != "assistant-1" {
		t.Fatalf("third event = %#v, want dataModelUpdate UI event", got[2])
	}
	contents := got[2].DataModelUpdate.Contents
	if len(contents) != 1 || contents[0].Key != "assistant-1/msg-0-text" || contents[0].ValueString != "hello" || !contents[0].Append {
		t.Fatalf("data model contents = %#v, want appended assistant content binding", contents)
	}
}

func TestProjectAssistantStreamWriterStreamsAssistantDeltasWithoutReplayingContent(t *testing.T) {
	got, err := collectProjectAssistantStreamEvents(
		projectAssistantEvent{
			Type:  projectAssistantEventMessageDelta,
			Delta: "hello",
		},
		projectAssistantEvent{
			Type:  projectAssistantEventMessageDelta,
			Delta: " world",
		},
	)
	if err != nil {
		t.Fatalf("EmitProjectAssistantEvent returned error: %v", err)
	}
	if len(got) != 4 {
		t.Fatalf("events = %#v, want beginRendering, shell, and two dataModelUpdates", got)
	}
	for i, want := range []string{"hello", " world"} {
		event := got[i+2]
		if event.DataModelUpdate == nil || len(event.DataModelUpdate.Contents) != 1 {
			t.Fatalf("event[%d] = %#v, want one dataModelUpdate content", i+2, event)
		}
		content := event.DataModelUpdate.Contents[0]
		if content.ValueString != want || !content.Append {
			t.Fatalf("event[%d] content = %#v, want append delta %q", i+2, content, want)
		}
	}
}

func TestProjectAssistantStreamWriterReplacesProvisionalContentAndCommitsOnce(t *testing.T) {
	var got []projectMessageStreamEvent
	writer := projectAssistantStreamWriter{
		assistantID: "assistant-1",
		write: func(event projectMessageStreamEvent) error {
			got = append(got, event)
			return nil
		},
	}

	if err := writer.WriteProvisionalAssistantContent(context.Background(), "hel"); err != nil {
		t.Fatalf("WriteProvisionalAssistantContent returned error: %v", err)
	}
	if err := writer.WriteProvisionalAssistantContent(context.Background(), "hello"); err != nil {
		t.Fatalf("WriteProvisionalAssistantContent returned error: %v", err)
	}
	if err := writer.WriteAcceptedAssistantContent(context.Background(), "hello"); err != nil {
		t.Fatalf("WriteAcceptedAssistantContent returned error: %v", err)
	}

	if len(got) != 4 {
		t.Fatalf("events = %#v, want begin, shell, and two provisional replacements only", got)
	}
	for i, want := range []string{"hel", "hello"} {
		content := got[i+2].DataModelUpdate.Contents[0]
		if content.ValueString != want || content.Append {
			t.Fatalf("event[%d] content = %#v, want replacement %q", i+2, content, want)
		}
	}
	if writer.acceptedAssistantContent != "hello" || writer.provisionalAssistantContent != "" {
		t.Fatalf("writer state = accepted %q provisional %q", writer.acceptedAssistantContent, writer.provisionalAssistantContent)
	}
}

func TestProjectAssistantStreamWriterResetsRejectedProvisionalContent(t *testing.T) {
	var got []projectMessageStreamEvent
	writer := projectAssistantStreamWriter{
		assistantID: "assistant-1",
		write: func(event projectMessageStreamEvent) error {
			got = append(got, event)
			return nil
		},
	}

	if err := writer.WriteAcceptedAssistantContent(context.Background(), "kept"); err != nil {
		t.Fatalf("WriteAcceptedAssistantContent returned error: %v", err)
	}
	if err := writer.WriteProvisionalAssistantContent(context.Background(), " rejected"); err != nil {
		t.Fatalf("WriteProvisionalAssistantContent returned error: %v", err)
	}
	if err := writer.ResetProvisionalAssistantContent(context.Background()); err != nil {
		t.Fatalf("ResetProvisionalAssistantContent returned error: %v", err)
	}

	if len(got) != 5 {
		t.Fatalf("events = %#v, want begin, shell, accepted append, provisional replacement, and reset", got)
	}
	provisional := got[3].DataModelUpdate.Contents[0]
	if provisional.ValueString != "kept rejected" || provisional.Append {
		t.Fatalf("provisional content = %#v, want full replacement", provisional)
	}
	reset := got[4].DataModelUpdate.Contents[0]
	if reset.ValueString != "kept" || reset.Append {
		t.Fatalf("reset content = %#v, want accepted replacement", reset)
	}
}

func TestProjectAssistantStreamWriterEmitsCanonicalLifecycleSequence(t *testing.T) {
	got, err := collectProjectAssistantStreamEvents(
		projectAssistantEvent{
			Type: projectAssistantEventToolCallStarted,
			ToolCall: &projectAssistantToolCall{
				ID:        "tool-1",
				Name:      projectToolWriteFile,
				Status:    "requested",
				Summary:   "Preparing change",
				Arguments: `{"path":"src/App.tsx","content":"content"}`,
			},
		},
		projectAssistantEvent{
			Type:   projectAssistantEventStatus,
			Status: "Planning update",
		},
		projectAssistantEvent{
			Type:  projectAssistantEventMessageDelta,
			Delta: "I'll patch this file now.",
		},
		projectAssistantEvent{
			Type: projectAssistantEventToolCallStarted,
			ToolCall: &projectAssistantToolCall{
				ID:      "tool-1",
				Name:    projectToolWriteFile,
				Status:  "running",
				Summary: "Applying patch",
			},
		},
		projectAssistantEvent{
			Type: projectAssistantEventToolCallFinished,
			ToolCall: &projectAssistantToolCall{
				ID:        "tool-1",
				Name:      projectToolWriteFile,
				Status:    "succeeded",
				Summary:   "Updated file",
				Arguments: `{"path":"src/App.tsx","content":"content"}`,
			},
		},
		projectAssistantEvent{
			Type: projectAssistantEventRunFinished,
		},
	)
	if err != nil {
		t.Fatalf("EmitProjectAssistantEvent returned error: %v", err)
	}

	wantTypes := []string{
		"dataModelUpdate",
		"beginRendering",
		"surfaceUpdate",
		"dataModelUpdate",
		"run_finished",
	}
	if len(got) != len(wantTypes) {
		t.Fatalf("events = %#v, want %d lifecycle events, got %d", got, len(wantTypes), len(got))
	}
	for i, want := range wantTypes {
		if got[i].streamType() != want {
			t.Fatalf("event[%d] type = %q, want %q; event = %#v", i, got[i].streamType(), want, got[i])
		}
	}

	hasContent := func(event projectMessageStreamEvent, key string) bool {
		if event.DataModelUpdate == nil {
			return false
		}
		for _, content := range event.DataModelUpdate.Contents {
			if content.Key == key {
				return true
			}
		}
		return false
	}

	if got[0].DataModelUpdate == nil || !hasContent(got[0], "assistant.status") {
		t.Fatalf("status event = %#v, want assistant.status update", got[0])
	}
	if got[1].BeginRendering == nil {
		t.Fatalf("second event = %#v, want beginRendering ui event", got[1])
	}
	assertA2UICard(t, got[2], "assistant", "")
	if got[3].DataModelUpdate == nil || !hasContent(got[3], "assistant-1/msg-0-text") {
		t.Fatalf("content event = %#v, want assistant content binding update", got[3])
	}
	if got[4].Type != string(projectAssistantEventRunFinished) || got[4].AssistantMessageID != "assistant-1" {
		t.Fatalf("final event = %#v, want run_finished", got[4])
	}
}

func TestProjectAssistantStreamWriterMapsStatusToUIDataModelUpdate(t *testing.T) {
	got, err := collectProjectAssistantStreamEvents(projectAssistantEvent{
		Type:   projectAssistantEventStatus,
		Status: "Preparing action",
	})
	if err != nil {
		t.Fatalf("EmitProjectAssistantEvent returned error: %v", err)
	}
	if len(got) != 1 || got[0].Type != "" || got[0].DataModelUpdate == nil {
		t.Fatalf("events = %#v, want one status dataModelUpdate", got)
	}
	contents := got[0].DataModelUpdate.Contents
	if len(contents) != 1 || contents[0].Key != "assistant.status" || contents[0].ValueString != "Preparing action" {
		t.Fatalf("data model contents = %#v, want assistant status", contents)
	}
}

func TestProjectAssistantStreamWriterDoesNotEmitToolCardsInMinimalDisclosure(t *testing.T) {
	prev := projectAssistantToolDisclosureMinimal
	projectAssistantToolDisclosureMinimal = true
	t.Cleanup(func() { projectAssistantToolDisclosureMinimal = prev })

	got, err := collectProjectAssistantStreamEvents(projectAssistantEvent{
		Type: projectAssistantEventToolCallFinished,
		ToolCall: &projectAssistantToolCall{
			ID:        "tool-1",
			Name:      "write_file",
			Status:    "succeeded",
			Summary:   "Wrote src/App.tsx",
			Arguments: `{"path":"src/App.tsx"}`,
			Error:     "warning only",
		},
	})
	if err != nil {
		t.Fatalf("EmitProjectAssistantEvent returned error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("events = %#v, want tool activity omitted from A2UI", got)
	}
}

func TestProjectAssistantStreamWriterDoesNotEmitToolCards(t *testing.T) {
	got, err := collectProjectAssistantStreamEvents(projectAssistantEvent{
		Type: projectAssistantEventToolCallFinished,
		ToolCall: &projectAssistantToolCall{
			ID:        "tool-1",
			Name:      "write_file",
			Status:    "succeeded",
			Summary:   "Wrote src/App.tsx",
			Arguments: `{"path":"src/App.tsx"}`,
			Error:     "warning only",
		},
	})
	if err != nil {
		t.Fatalf("EmitProjectAssistantEvent returned error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("events = %#v, want tool activity omitted from A2UI", got)
	}
}

func TestProjectAssistantActionFeedUsesSpecificSafeLabels(t *testing.T) {
	tests := []struct {
		name        string
		toolName    string
		status      string
		arguments   string
		summary     string
		wantTitle   string
		wantTarget  string
		wantOutcome string
		wantCount   int
	}{
		{
			name:       "read file",
			toolName:   projectToolReadFile,
			status:     "succeeded",
			arguments:  `{"file_path":"src/App.vue","offset":1,"limit":200}`,
			summary:    "file read",
			wantTitle:  "Read file",
			wantTarget: "src/App.vue",
		},
		{
			name:        "list files",
			toolName:    projectToolLS,
			arguments:   "path src",
			status:      "succeeded",
			summary:     "6 path(s); src/App.vue, src/style.css",
			wantTitle:   "Read project files",
			wantOutcome: "6 project files",
			wantCount:   6,
		},
		{
			name:        "glob files",
			toolName:    projectToolGlob,
			arguments:   "pattern **/*.vue; path src",
			status:      "succeeded",
			summary:     "4 path(s)",
			wantTitle:   "Read project files",
			wantOutcome: "4 project files",
			wantCount:   4,
		},
		{
			name:       "write file",
			toolName:   projectToolWriteFile,
			status:     "succeeded",
			arguments:  `{"path":"src/App.vue","content":"secret source"}`,
			summary:    "write_file; src/App.vue; 2048 bytes",
			wantTitle:  "Updated file",
			wantTarget: "src/App.vue",
		},
		{
			name:       "apply patch",
			toolName:   projectToolApplyPatch,
			status:     "running",
			arguments:  "path src/style.css; replaceAll",
			wantTitle:  "Updating file",
			wantTarget: "src/style.css",
		},
		{
			name:        "search files",
			toolName:    projectToolGrep,
			status:      "succeeded",
			summary:     "3 result line(s)",
			wantTitle:   "Searched project",
			wantOutcome: "3 references",
			wantCount:   3,
		},
		{
			name:      "verify preview",
			toolName:  projectToolVerifyDevelopmentRuntime,
			status:    "running",
			wantTitle: "Checking development preview",
		},
		{
			name:       "commit files",
			toolName:   projectToolCommitProjectFiles,
			status:     "succeeded",
			summary:    "commit abc123; 2 file(s): src/App.vue, src/style.css",
			wantTitle:  "Committed changes",
			wantTarget: "abc123",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			action := projectAssistantActionFeedItemFromAssistantToolCall(projectAssistantToolCall{
				ID:        "tool-1",
				Name:      tt.toolName,
				Status:    tt.status,
				Arguments: tt.arguments,
				Summary:   tt.summary,
			})
			if action.Title != tt.wantTitle || action.Target != tt.wantTarget ||
				action.Outcome != tt.wantOutcome || action.Count != tt.wantCount {
				t.Fatalf("action = %#v, want title=%q target=%q outcome=%q count=%d",
					action, tt.wantTitle, tt.wantTarget, tt.wantOutcome, tt.wantCount)
			}
			if strings.Contains(action.Title, "secret source") {
				t.Fatalf("label exposed raw mutation content: %q", action.Title)
			}
		})
	}
}

func TestProjectAssistantUICanonicalReadsAreInspectActions(t *testing.T) {
	for _, name := range []string{projectToolLS, projectToolReadFile, projectToolGlob, projectToolGrep} {
		t.Run(name, func(t *testing.T) {
			if got := projectAssistantActionFeedItemKind(name); got != projectAssistantActionFeedItemInspect {
				t.Fatalf("projectAssistantActionFeedItemKind(%q) = %q, want %q", name, got, projectAssistantActionFeedItemInspect)
			}
		})
	}
}

func TestProjectAssistantActionFeedSpecificLabelsRespectMinimalDisclosure(t *testing.T) {
	prev := projectAssistantToolDisclosureMinimal
	projectAssistantToolDisclosureMinimal = true
	t.Cleanup(func() { projectAssistantToolDisclosureMinimal = prev })

	action := projectAssistantActionFeedItemFromAssistantToolCall(projectAssistantToolCall{
		ID:        "tool-1",
		Name:      projectToolWriteFile,
		Status:    "succeeded",
		Arguments: `{"path":"src/App.vue","content":"secret source"}`,
		Summary:   "write_file; src/App.vue; 2048 bytes",
	})
	if action.Title != "Edited files" {
		t.Fatalf("minimal disclosure label = %q, want generic edit label", action.Title)
	}
	if action.Target != "" || action.Outcome != "" || action.Diagnostic != nil {
		t.Fatalf("minimal disclosure action exposed details: %#v", action)
	}
}

func TestProjectAssistantActionFeedFailsClosedForUnknownTools(t *testing.T) {
	action := projectAssistantActionFeedItemFromAssistantToolCall(projectAssistantToolCall{
		ID:        "tool-1",
		Name:      "some__mcp_tool",
		Status:    "succeeded",
		Arguments: `{"content":"secret file body","token":"abc123"}`,
		Summary:   "secret tool result",
	})
	if action.Title != "Completed action" || action.Target != "" || action.Outcome != "" {
		t.Fatalf("unknown action = %#v, want generic product presentation", action)
	}
	if encoded, err := json.Marshal(action); err != nil || strings.Contains(string(encoded), "secret") || strings.Contains(string(encoded), "mcp_tool") {
		t.Fatalf("unknown action JSON = %s, %v; want no raw tool detail", encoded, err)
	}
}

func TestProjectAssistantStreamWriterSkipsLowValueFinishedInspectionProgress(t *testing.T) {
	got, err := collectProjectAssistantStreamEvents(projectAssistantEvent{
		Type: projectAssistantEventToolCallFinished,
		ToolCall: &projectAssistantToolCall{
			ID:      "tool-1",
			Name:    projectToolReadFile,
			Status:  "succeeded",
			Summary: "Read src/App.tsx",
		},
	})
	if err != nil {
		t.Fatalf("EmitProjectAssistantEvent returned error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("events = %#v, want inspection activity omitted from A2UI", got)
	}
}

func TestProjectAssistantStreamWriterMapsPermissionCheckpointToInterruptRequest(t *testing.T) {
	got, err := collectProjectAssistantStreamEvents(projectAssistantEvent{
		Type: projectAssistantEventPermissionNeeded,
		Permission: &projectAssistantPermission{
			ID:         "perm-1",
			ToolCallID: "tool-1",
			ToolName:   "write_file",
			Reason:     "will write files",
			Input:      json.RawMessage(`{"path":"src/App.tsx","content":"secret"}`),
		},
	}, projectAssistantEvent{
		Type:       projectAssistantEventCheckpointSaved,
		Checkpoint: &projectAssistantCheckpoint{ID: "run-1", Reason: "waiting_for_permission"},
	})
	if err != nil {
		t.Fatalf("EmitProjectAssistantEvent returned error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("events = %#v, want beginRendering and interruptRequest", got)
	}
	interrupt := got[1].InterruptRequest
	if got[1].Type != "" || interrupt == nil {
		t.Fatalf("event = %#v, want interruptRequest UI event", got[1])
	}
	if interrupt.InterruptID != "perm-1" || interrupt.Description != "will write files" || interrupt.Status != "pending" {
		t.Fatalf("interrupt = %#v, want pending permission request", interrupt)
	}
	if interrupt.Action == nil || interrupt.Action.RunID != "run-1" || interrupt.Action.RequestID != "perm-1" {
		t.Fatalf("interrupt action = %#v, want opaque resume handle", interrupt.Action)
	}
	assertNoRawAssistantTrace(t, got, "src/App.tsx", "secret", "waiting_for_permission", "checkpoint_saved", "permission_required")
}

func TestProjectAssistantMessageMetadataStoresSafeUIModel(t *testing.T) {
	metadata := projectAssistantMessageMetadata(projectMessageStatusPendingPermission, []projectToolCallStreamEvent{{
		ID:        "tool-1",
		Name:      "write_file",
		Status:    "permission_required",
		Arguments: `{"path":"src/App.tsx","content":"secret"}`,
		Summary:   "Wrote src/App.tsx",
		Error:     "raw tool failure",
		Permission: &projectAssistantPermission{
			ID:         "perm-1",
			ToolCallID: "tool-1",
			ToolName:   "write_file",
			Reason:     "will write files",
			Input:      json.RawMessage(`{"path":"src/App.tsx","content":"secret"}`),
		},
		Checkpoint: &projectAssistantCheckpoint{ID: "run-1", Reason: "waiting_for_permission"},
	}})
	if _, ok := metadata["toolCalls"]; ok {
		t.Fatalf("metadata = %#v, should not persist raw toolCalls", metadata)
	}
	if _, ok := metadata["assistantActionFeed"]; !ok {
		t.Fatalf("metadata = %#v, want safe assistantActionFeed", metadata)
	}
	if _, ok := metadata["assistantInterrupt"]; !ok {
		t.Fatalf("metadata = %#v, want safe assistantInterrupt", metadata)
	}
	raw, err := json.Marshal(metadata)
	if err != nil {
		t.Fatalf("marshal metadata: %v", err)
	}
	payload := string(raw)
	// Tool name and summarized arguments/result are disclosed by design; the
	// permission Input payload (raw file contents / secrets), raw error text
	// behind a summary, and checkpoint internals must never leak.
	for _, value := range []string{"secret", "raw tool failure", "waiting_for_permission", "permission_required"} {
		if strings.Contains(payload, value) {
			t.Fatalf("metadata leaked %q in %s", value, payload)
		}
	}
}

func TestProjectAssistantEventTypeForToolCallStatus(t *testing.T) {
	tests := []struct {
		status string
		want   projectAssistantEventType
	}{
		{status: "requested", want: projectAssistantEventToolCallStarted},
		{status: "running", want: projectAssistantEventToolCallStarted},
		{status: "permission_required", want: projectAssistantEventToolCallFinished},
		{status: "succeeded", want: projectAssistantEventToolCallFinished},
		{status: "failed", want: projectAssistantEventToolCallFinished},
		{status: "rejected", want: projectAssistantEventToolCallFinished},
		{status: "", want: projectAssistantEventToolCallFinished},
	}
	for _, tt := range tests {
		t.Run(tt.status, func(t *testing.T) {
			if got := projectAssistantEventTypeForToolCallStatus(tt.status); got != tt.want {
				t.Fatalf("event type = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestProjectAssistantStreamWriterMapsTerminalEvents(t *testing.T) {
	got, err := collectProjectAssistantStreamEvents(projectAssistantEvent{
		Type:  projectAssistantEventRunFailed,
		Error: "boom",
	}, projectAssistantEvent{
		Type: projectAssistantEventRunFinished,
	})
	if err != nil {
		t.Fatalf("EmitProjectAssistantEvent returned error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("events = %#v, want run_failed and run_finished", got)
	}
	if got[0].Type != string(projectAssistantEventRunFailed) || got[0].Error != "boom" {
		t.Fatalf("first event = %#v, want run_failed", got[0])
	}
	if got[1].Type != string(projectAssistantEventRunFinished) || got[1].AssistantMessageID != "assistant-1" {
		t.Fatalf("second event = %#v, want assistant run_finished", got[1])
	}
}

func TestStreamProjectAssistantEmitsCanonicalLifecycleSequence(t *testing.T) {
	settings := projectLLMSettings{
		Provider: defaultProjectLLMProvider,
		BaseURL:  defaultProjectLLMBaseURL,
		Model:    "test-model",
		APIKey:   "test-key",
	}
	client := asclient.NewFromDynamic(projectSettingsDynamicClient{secret: projectLLMSettingsSecret(settings)})
	messages := store.NewMemoryStore()
	id := identity{tenantPath: "root:org-a:ws-1", orgUUID: "org-a", workspaceUUID: "ws-1"}
	messageScope := testProjectMessageScope(id.orgUUID, id.workspaceUUID, "demo")
	if err := appendProjectUserMessage(context.Background(), messages, messageScope, "write a file"); err != nil {
		t.Fatalf("appendProjectUserMessage returned error: %v", err)
	}
	server := NewWithWorkspace(nil, messages, workspace.NewFileStore(t.TempDir()), "", false)
	server.assistantEngine = projectAssistantSequenceEngine{}
	project := projectWithRepository("demo-repo", "demo", "github")
	project.Name = "demo"
	project.UID = "test-project-uid-demo"
	rr := httptest.NewRecorder()
	flusher, ok := startProjectMessageStream(rr)
	if !ok {
		t.Fatal("response recorder did not support streaming")
	}

	server.streamProjectAssistant(
		rr,
		flusher,
		httptest.NewRequest(http.MethodPost, "/", nil),
		client,
		id,
		project,
		messages,
		"assistant-sequence",
	)

	got := decodeProjectMessageStreamEvents(t, rr.Body.String())
	wantTypes := []string{
		"dataModelUpdate",
		"beginRendering",
		"surfaceUpdate",
		"dataModelUpdate",
		"dataModelUpdate",
		"run_finished",
	}
	if len(got) != len(wantTypes) {
		t.Fatalf("stream events = %#v, want %d events", got, len(wantTypes))
	}
	for i, want := range wantTypes {
		if got[i].streamType() != want {
			t.Fatalf("stream event[%d] type = %q, want %q; event = %#v", i, got[i].streamType(), want, got[i])
		}
	}
	if got[1].BeginRendering == nil || got[1].BeginRendering.SurfaceID != "assistant-sequence" {
		t.Fatalf("beginRendering = %#v, want assistant-sequence surface", got[1].BeginRendering)
	}
	if !projectMessageStreamEventsHaveContent([]projectMessageStreamEvent{got[4]}, projectAssistantUIDevelopmentPreviewRefreshKey) {
		t.Fatalf("preview refresh event = %#v, want preview refresh signal", got[4])
	}
	if got[5].Type != string(projectAssistantEventRunFinished) || got[5].AssistantMessageID != "assistant-sequence" {
		t.Fatalf("terminal event = %#v, want run_finished for assistant-sequence", got[5])
	}
}

func TestStreamProjectAssistantEmitsPreviewRefreshAfterMutatingToolCall(t *testing.T) {
	settings := projectLLMSettings{
		Provider: defaultProjectLLMProvider,
		BaseURL:  defaultProjectLLMBaseURL,
		Model:    "test-model",
		APIKey:   "test-key",
	}
	client := asclient.NewFromDynamic(projectSettingsDynamicClient{secret: projectLLMSettingsSecret(settings)})
	messages := store.NewMemoryStore()
	workspaces := workspace.NewFileStore(t.TempDir())
	id := identity{tenantPath: "root:org-a:ws-1", orgUUID: "org-a", workspaceUUID: "ws-1"}
	messageScope := testProjectMessageScope(id.orgUUID, id.workspaceUUID, "demo")
	if err := appendProjectUserMessage(context.Background(), messages, messageScope, "change the app"); err != nil {
		t.Fatalf("appendProjectUserMessage returned error: %v", err)
	}
	server := NewWithWorkspace(nil, messages, workspaces, "", false)
	server.assistantEngine = projectAssistantWorkspaceMutationEngine{}
	project := projectWithRepository("demo-repo", "demo", "github")
	project.Name = "demo"
	project.UID = "test-project-uid-demo"
	rr := httptest.NewRecorder()
	flusher, ok := startProjectMessageStream(rr)
	if !ok {
		t.Fatal("response recorder did not support streaming")
	}

	server.streamProjectAssistant(
		rr,
		flusher,
		httptest.NewRequest(http.MethodPost, "/", nil),
		client,
		id,
		project,
		messages,
		"assistant-digest",
	)

	got := decodeProjectMessageStreamEvents(t, rr.Body.String())
	if !projectMessageStreamEventsHaveContent(got, projectAssistantUIDevelopmentPreviewRefreshKey) {
		t.Fatalf("stream events = %#v, want preview refresh signal after workspace mutation", got)
	}
}

func TestProjectAssistantGenerationFailureMessageHidesEinoTraceForRateLimit(t *testing.T) {
	got := projectAssistantGenerationFailureMessage(errors.New(`[NodeRunError] LLM API returned 429 Too Many Requests: Resource exhausted. Please try again later.
------------------------
node path: [node_1, ChatModel]`))
	if strings.Contains(got, "NodeRunError") || strings.Contains(got, "node path") || strings.Contains(got, "ChatModel") {
		t.Fatalf("message = %q, want no raw Eino trace", got)
	}
	if !strings.Contains(got, "rate limited") {
		t.Fatalf("message = %q, want rate limit explanation", got)
	}
}

func TestProjectAssistantStreamWriterHonorsContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	writer := projectAssistantStreamWriter{write: func(projectMessageStreamEvent) error {
		t.Fatal("write should not run after context cancellation")
		return nil
	}}
	if err := writer.EmitProjectAssistantEvent(ctx, projectAssistantEvent{
		Type:  projectAssistantEventMessageDelta,
		Delta: "hello",
	}); !errors.Is(err, context.Canceled) {
		t.Fatalf("EmitProjectAssistantEvent error = %v, want context.Canceled", err)
	}
}

func collectProjectAssistantStreamEvents(events ...projectAssistantEvent) ([]projectMessageStreamEvent, error) {
	var got []projectMessageStreamEvent
	writer := projectAssistantStreamWriter{
		assistantID: "assistant-1",
		write: func(event projectMessageStreamEvent) error {
			got = append(got, event)
			return nil
		},
	}
	for _, event := range events {
		if err := writer.EmitProjectAssistantEvent(context.Background(), event); err != nil {
			return nil, err
		}
	}
	return got, nil
}

type projectAssistantSequenceEngine struct{}

func (projectAssistantSequenceEngine) StreamProjectAssistant(_ context.Context, req projectAssistantRunRequest) (projectAssistantRunResult, error) {
	req.StreamCallbacks.OnToolCall(projectToolCallStreamEvent{
		ID:      "tool-1",
		Name:    projectToolWriteFile,
		Status:  "requested",
		Summary: "Preparing change",
	})
	req.StreamCallbacks.OnStatus("Planning update")
	req.StreamCallbacks.OnChunk("I'll patch this file now.")
	req.StreamCallbacks.OnToolCall(projectToolCallStreamEvent{
		ID:      "tool-1",
		Name:    projectToolWriteFile,
		Status:  "running",
		Summary: "Applying patch",
	})
	req.StreamCallbacks.OnToolCall(projectToolCallStreamEvent{
		ID:      "tool-1",
		Name:    projectToolWriteFile,
		Status:  "succeeded",
		Summary: "Updated file",
	})
	return projectAssistantRunResult{Content: "I'll patch this file now."}, nil
}

func (projectAssistantSequenceEngine) ResumeProjectAssistant(context.Context, projectAssistantRunRequest, projectAssistantResumeRequest, projectAssistantCheckpointState) (projectAssistantRunResult, error) {
	return projectAssistantRunResult{}, nil
}

type projectAssistantWorkspaceMutationEngine struct{}

func (projectAssistantWorkspaceMutationEngine) StreamProjectAssistant(ctx context.Context, req projectAssistantRunRequest) (projectAssistantRunResult, error) {
	if _, err := req.Workspace.WriteFile(ctx, req.WorkspaceScope, workspace.WriteOptions{Path: "src/App.tsx", Content: "export function App() { return <main>Updated</main> }\n"}); err != nil {
		return projectAssistantRunResult{}, err
	}
	req.StreamCallbacks.OnToolCall(projectToolCallStreamEvent{
		ID:      "tool-1",
		Name:    projectToolWriteFile,
		Status:  "succeeded",
		Summary: "Updated file",
	})
	req.StreamCallbacks.OnChunk("Updated the app.")
	return projectAssistantRunResult{Content: "Updated the app."}, nil
}

func (projectAssistantWorkspaceMutationEngine) ResumeProjectAssistant(context.Context, projectAssistantRunRequest, projectAssistantResumeRequest, projectAssistantCheckpointState) (projectAssistantRunResult, error) {
	return projectAssistantRunResult{}, nil
}

func decodeProjectMessageStreamEvents(t *testing.T, body string) []projectMessageStreamEvent {
	t.Helper()
	blocks := strings.Split(strings.TrimSpace(body), "\n\n")
	out := make([]projectMessageStreamEvent, 0, len(blocks))
	for _, block := range blocks {
		var data string
		for _, line := range strings.Split(block, "\n") {
			if strings.HasPrefix(line, "data: ") {
				data = strings.TrimPrefix(line, "data: ")
			}
		}
		if data == "" {
			continue
		}
		var event projectMessageStreamEvent
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			t.Fatalf("unmarshal stream event %q: %v", data, err)
		}
		out = append(out, event)
	}
	return out
}

func projectMessageStreamEventHasContent(event projectMessageStreamEvent, key string) bool {
	if event.DataModelUpdate == nil {
		return false
	}
	for _, content := range event.DataModelUpdate.Contents {
		if content.Key == key {
			return true
		}
	}
	return false
}

func projectMessageStreamEventsHaveContent(events []projectMessageStreamEvent, key string) bool {
	for _, event := range events {
		if projectMessageStreamEventHasContent(event, key) {
			return true
		}
	}
	return false
}

func assertA2UICard(t *testing.T, event projectMessageStreamEvent, role, bodyContains string) {
	t.Helper()
	if event.SurfaceUpdate == nil {
		t.Fatalf("event = %#v, want surfaceUpdate", event)
	}
	components := map[string]projectAssistantUIComponent{}
	for _, component := range event.SurfaceUpdate.Components {
		components[component.ID] = component
	}
	for _, component := range event.SurfaceUpdate.Components {
		if component.Component.Card == nil {
			continue
		}
		texts := a2uiCardTexts(components, component.Component.Card.Children)
		if len(texts) == 0 || texts[0] != role {
			continue
		}
		if bodyContains == "" {
			return
		}
		body := strings.Join(texts[1:], "\n")
		if strings.Contains(body, bodyContains) {
			return
		}
		t.Fatalf("card role %q body = %q, want to contain %q", role, body, bodyContains)
	}
	t.Fatalf("components = %#v, want A2UI card with role %q", event.SurfaceUpdate.Components, role)
}

func assertSingleA2UICardID(t *testing.T, event projectMessageStreamEvent, role string) string {
	t.Helper()
	if event.SurfaceUpdate == nil {
		t.Fatalf("event = %#v, want surfaceUpdate", event)
	}
	var found []string
	components := map[string]projectAssistantUIComponent{}
	for _, component := range event.SurfaceUpdate.Components {
		components[component.ID] = component
	}
	for _, component := range event.SurfaceUpdate.Components {
		if component.Component.Card == nil {
			continue
		}
		texts := a2uiCardTexts(components, component.Component.Card.Children)
		if len(texts) > 0 && texts[0] == role {
			found = append(found, component.ID)
		}
	}
	if len(found) != 1 {
		t.Fatalf("components = %#v, want one A2UI card with role %q, got IDs %#v", event.SurfaceUpdate.Components, role, found)
	}
	return found[0]
}

func assertA2UIRootChildren(t *testing.T, event projectMessageStreamEvent) []string {
	t.Helper()
	if event.SurfaceUpdate == nil {
		t.Fatalf("event = %#v, want surfaceUpdate", event)
	}
	for _, component := range event.SurfaceUpdate.Components {
		if component.ID == projectAssistantUIRootComponentID && component.Component.Column != nil {
			return component.Component.Column.Children
		}
	}
	t.Fatalf("components = %#v, want root column component", event.SurfaceUpdate.Components)
	return nil
}

func countString(values []string, want string) int {
	var count int
	for _, value := range values {
		if value == want {
			count++
		}
	}
	return count
}

func a2uiCardTexts(components map[string]projectAssistantUIComponent, children []string) []string {
	var out []string
	for _, child := range children {
		component := components[child]
		switch {
		case component.Component.Text != nil:
			if component.Component.Text.DataKey != "" {
				out = append(out, component.Component.Text.DataKey)
			} else {
				out = append(out, component.Component.Text.Value)
			}
		case component.Component.Column != nil:
			out = append(out, a2uiCardTexts(components, component.Component.Column.Children)...)
		case component.Component.Row != nil:
			out = append(out, a2uiCardTexts(components, component.Component.Row.Children)...)
		case component.Component.Card != nil:
			out = append(out, a2uiCardTexts(components, component.Component.Card.Children)...)
		}
	}
	return out
}

func assertNoRawAssistantTrace(t *testing.T, events []projectMessageStreamEvent, forbidden ...string) {
	t.Helper()
	raw, err := json.Marshal(events)
	if err != nil {
		t.Fatalf("marshal events: %v", err)
	}
	payload := string(raw)
	for _, value := range forbidden {
		if strings.Contains(payload, value) {
			t.Fatalf("public UI events leaked %q in %s", value, payload)
		}
	}
}
