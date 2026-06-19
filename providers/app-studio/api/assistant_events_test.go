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
)

func TestProjectAssistantStreamWriterMapsDeltaToUIDataModelUpdate(t *testing.T) {
	got, err := collectProjectAssistantStreamEvents(projectAssistantEvent{
		Type:  projectAssistantEventMessageDelta,
		Delta: "hello",
	})
	if err != nil {
		t.Fatalf("EmitProjectAssistantEvent returned error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("events = %#v, want beginRendering and dataModelUpdate", got)
	}
	if got[0].Type != "ui" || got[0].AssistantMessageID != "assistant-1" || got[0].UI == nil || got[0].UI.BeginRendering == nil {
		t.Fatalf("first event = %#v, want beginRendering UI event", got[0])
	}
	if got[1].Type != "ui" || got[1].AssistantMessageID != "assistant-1" || got[1].UI == nil || got[1].UI.DataModelUpdate == nil {
		t.Fatalf("second event = %#v, want dataModelUpdate UI event", got[1])
	}
	contents := got[1].UI.DataModelUpdate.Contents
	if len(contents) != 1 || contents[0].Key != "assistant.content" || contents[0].ValueString != "hello" || !contents[0].Append {
		t.Fatalf("data model contents = %#v, want assistant content append", contents)
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
	if len(got) != 1 || got[0].Type != "ui" || got[0].UI == nil || got[0].UI.DataModelUpdate == nil {
		t.Fatalf("events = %#v, want one status dataModelUpdate", got)
	}
	contents := got[0].UI.DataModelUpdate.Contents
	if len(contents) != 1 || contents[0].Key != "assistant.status" || contents[0].ValueString != "Preparing action" {
		t.Fatalf("data model contents = %#v, want assistant status", contents)
	}
}

func TestProjectAssistantStreamWriterMapsToolCallToSafeDisclosure(t *testing.T) {
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
	if len(got) != 2 {
		t.Fatalf("events = %#v, want beginRendering and safe surfaceUpdate", got)
	}
	event := got[1]
	if event.Type != "ui" || event.AssistantMessageID != "assistant-1" || event.UI == nil || event.UI.SurfaceUpdate == nil {
		t.Fatalf("event = %#v, want safe surfaceUpdate UI event", event)
	}
	components := event.UI.SurfaceUpdate.Components
	if len(components) != 1 || components[0].ToolDisclosure == nil {
		t.Fatalf("components = %#v, want one tool disclosure", components)
	}
	disclosure := components[0].ToolDisclosure
	if disclosure.ID != "tool-1" || disclosure.Kind != "edit" || disclosure.Status != "succeeded" || disclosure.Label != "Edited files" {
		t.Fatalf("disclosure = %#v, want safe edit disclosure", disclosure)
	}
	assertNoRawAssistantTrace(t, got, "src/App.tsx", "warning only", "write_file")
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
	if len(got) != 3 {
		t.Fatalf("events = %#v, want beginRendering, disclosure, interruptRequest", got)
	}
	interrupt := got[2].UI.InterruptRequest
	if got[2].Type != "ui" || got[2].AssistantMessageID != "assistant-1" || interrupt == nil {
		t.Fatalf("event = %#v, want interruptRequest UI event", got[2])
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
	if _, ok := metadata["assistantActions"]; !ok {
		t.Fatalf("metadata = %#v, want safe assistantActions", metadata)
	}
	if _, ok := metadata["assistantInterrupt"]; !ok {
		t.Fatalf("metadata = %#v, want safe assistantInterrupt", metadata)
	}
	raw, err := json.Marshal(metadata)
	if err != nil {
		t.Fatalf("marshal metadata: %v", err)
	}
	payload := string(raw)
	for _, value := range []string{"src/App.tsx", "secret", "raw tool failure", "waiting_for_permission", "permission_required"} {
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
