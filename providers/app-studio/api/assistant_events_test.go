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
	"errors"
	"testing"
)

func TestProjectAssistantStreamWriterMapsDeltaToChunk(t *testing.T) {
	got, err := collectProjectAssistantStreamEvents(projectAssistantEvent{
		Type:  projectAssistantEventMessageDelta,
		Delta: "hello",
	})
	if err != nil {
		t.Fatalf("EmitProjectAssistantEvent returned error: %v", err)
	}
	if len(got) != 1 || got[0].Type != "chunk" || got[0].Content != "hello" || got[0].AssistantMessageID != "assistant-1" {
		t.Fatalf("events = %#v, want one assistant chunk event", got)
	}
}

func TestProjectAssistantStreamWriterMapsStatus(t *testing.T) {
	got, err := collectProjectAssistantStreamEvents(projectAssistantEvent{
		Type:   projectAssistantEventStatus,
		Status: "Preparing action",
	})
	if err != nil {
		t.Fatalf("EmitProjectAssistantEvent returned error: %v", err)
	}
	if len(got) != 1 || got[0].Type != "status" || got[0].Status != "Preparing action" || got[0].AssistantMessageID != "" {
		t.Fatalf("events = %#v, want one status event without assistant id", got)
	}
}

func TestProjectAssistantStreamWriterMapsToolCall(t *testing.T) {
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
	if len(got) != 1 || got[0].Type != "tool_call" || got[0].AssistantMessageID != "assistant-1" {
		t.Fatalf("events = %#v, want one assistant tool_call event", got)
	}
	if got[0].ToolCall == nil {
		t.Fatalf("tool call event omitted payload: %#v", got[0])
	}
	if got[0].ToolCall.ID != "tool-1" || got[0].ToolCall.Name != "write_file" || got[0].ToolCall.Status != "succeeded" || got[0].ToolCall.Arguments != `{"path":"src/App.tsx"}` || got[0].ToolCall.Error != "warning only" {
		t.Fatalf("tool call = %#v, want preserved current SSE payload", got[0].ToolCall)
	}
}

func TestProjectAssistantStreamWriterMapsPermissionAndCheckpoint(t *testing.T) {
	got, err := collectProjectAssistantStreamEvents(projectAssistantEvent{
		Type: projectAssistantEventPermissionNeeded,
		Permission: &projectAssistantPermission{
			ID:         "perm-1",
			ToolCallID: "tool-1",
			ToolName:   "write_file",
			Reason:     "will write files",
		},
	}, projectAssistantEvent{
		Type:       projectAssistantEventCheckpointSaved,
		Checkpoint: &projectAssistantCheckpoint{ID: "run-1", Reason: "waiting_for_permission"},
	})
	if err != nil {
		t.Fatalf("EmitProjectAssistantEvent returned error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("events = %#v, want permission and checkpoint", got)
	}
	if got[0].Type != "permission_required" || got[0].Permission == nil || got[0].Permission.ID != "perm-1" || got[0].AssistantMessageID != "assistant-1" {
		t.Fatalf("permission event = %#v, want assistant permission payload", got[0])
	}
	if got[1].Type != "checkpoint_saved" || got[1].Checkpoint == nil || got[1].Checkpoint.ID != "run-1" || got[1].AssistantMessageID != "assistant-1" {
		t.Fatalf("checkpoint event = %#v, want assistant checkpoint payload", got[1])
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
		t.Fatalf("events = %#v, want error and done", got)
	}
	if got[0].Type != "error" || got[0].Error != "boom" {
		t.Fatalf("first event = %#v, want error", got[0])
	}
	if got[1].Type != "done" || got[1].AssistantMessageID != "assistant-1" {
		t.Fatalf("second event = %#v, want assistant done", got[1])
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
