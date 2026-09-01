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
	"time"

	"github.com/cloudwego/eino/callbacks"
	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"github.com/faroshq/provider-app-studio/store"
)

func assistantModelInputMessageForTest() *schema.Message {
	data := "UE5HIGJ5dGVz"
	message := schema.UserMessage("")
	message.Extra = map[string]any{
		projectAssistantAttachmentMessageKindKey:     true,
		projectAssistantAttachmentMessageIDKey:       "image-1",
		projectAssistantAttachmentMessageFilenameKey: "screen.png",
	}
	message.UserInputMultiContent = []schema.MessageInputPart{{
		Type:  schema.ChatMessagePartTypeImageURL,
		Image: &schema.MessageInputImage{MessagePartCommon: schema.MessagePartCommon{Base64Data: &data, MIMEType: "image/png"}},
	}}
	return message
}

func TestProjectEinoAssistantModelCallbackEmitsHonestImageLifecycle(t *testing.T) {
	runState := newProjectEinoAssistantRunState()
	runState.NextModelCallOrdinal()
	var events []projectAssistantModelInputEvent
	handler := newProjectEinoAssistantModelCallbackHandler(projectAssistantStreamCallbacks{
		OnModelInput: func(event projectAssistantModelInputEvent) { events = append(events, event) },
	}, runState, nil)
	ctx := handler.OnStart(context.Background(), nil, &einomodel.CallbackInput{
		Messages: []*schema.Message{assistantModelInputMessageForTest()},
	})
	handler.OnEnd(ctx, nil, &einomodel.CallbackOutput{Message: schema.AssistantMessage("I can see it.", nil)})
	if len(events) != 2 || events[0].Status != "started" || events[1].Status != "completed" {
		t.Fatalf("image lifecycle events = %#v, want started then completed", events)
	}
	if events[0].ID != events[1].ID || events[0].Filename != "screen.png" || events[0].ContentType != "image/png" {
		t.Fatalf("image lifecycle identity = %#v", events)
	}
	viewed := projectAssistantActionFeedItemFromModelInput(events[1])
	if viewed.Title != "Viewed image" || viewed.Status != projectAssistantActionFeedStatusSucceeded || viewed.MediaKind != projectAssistantActionFeedMediaImage {
		t.Fatalf("viewed image action = %#v", viewed)
	}
	encoded, err := json.Marshal(viewed)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "UE5H") || strings.Contains(string(encoded), "Base64Data") {
		t.Fatalf("durable image evidence leaked model bytes: %s", encoded)
	}
	if strings.Contains(string(encoded), `"outcome"`) {
		t.Fatalf("viewed image action exposed redundant outcome copy: %s", encoded)
	}

	runState = newProjectEinoAssistantRunState()
	runState.NextModelCallOrdinal()
	events = nil
	handler = newProjectEinoAssistantModelCallbackHandler(projectAssistantStreamCallbacks{
		OnModelInput: func(event projectAssistantModelInputEvent) { events = append(events, event) },
	}, runState, nil)
	ctx = handler.OnStart(context.Background(), nil, &einomodel.CallbackInput{
		Messages: []*schema.Message{assistantModelInputMessageForTest()},
	})
	handler.OnError(ctx, nil, errors.New("provider unavailable"))
	if len(events) != 2 || events[1].Status != "failed" {
		t.Fatalf("failed image lifecycle events = %#v, want terminal failure", events)
	}
	failed := projectAssistantActionFeedItemFromModelInput(events[1])
	if failed.Title == "Viewed image" || failed.Status != projectAssistantActionFeedStatusFailed || failed.Diagnostic == nil {
		t.Fatalf("failed image action = %#v, must not claim viewed", failed)
	}
}

func TestProjectEinoAssistantModelCallbackDoesNotRepeatCompletedImageLifecycle(t *testing.T) {
	runState := newProjectEinoAssistantRunState()
	var events []projectAssistantModelInputEvent
	handler := newProjectEinoAssistantModelCallbackHandler(projectAssistantStreamCallbacks{
		OnModelInput: func(event projectAssistantModelInputEvent) { events = append(events, event) },
	}, runState, nil)
	for call := 0; call < 2; call++ {
		runState.NextModelCallOrdinal()
		ctx := handler.OnStart(context.Background(), nil, &einomodel.CallbackInput{
			Messages: []*schema.Message{assistantModelInputMessageForTest()},
		})
		handler.OnEnd(ctx, nil, &einomodel.CallbackOutput{Message: schema.AssistantMessage("ok", nil)})
	}
	if len(events) != 2 || events[0].Status != "started" || events[1].Status != "completed" {
		t.Fatalf("repeated model calls emitted image lifecycle events = %#v, want one started/completed pair", events)
	}
}

func TestProjectEinoAssistantModelCallbackDoesNotRepeatImageLifecycleAfterCheckpointRestore(t *testing.T) {
	runState := newProjectEinoAssistantRunState()
	runState.RecordCompletedModelInput("image-input-image-1")
	restored := newProjectEinoAssistantRunState()
	restored.RestoreCheckpointState(runState.CheckpointState())
	restored.NextModelCallOrdinal()
	var events []projectAssistantModelInputEvent
	handler := newProjectEinoAssistantModelCallbackHandler(projectAssistantStreamCallbacks{
		OnModelInput: func(event projectAssistantModelInputEvent) { events = append(events, event) },
	}, restored, nil)
	ctx := handler.OnStart(context.Background(), nil, &einomodel.CallbackInput{
		Messages: []*schema.Message{assistantModelInputMessageForTest()},
	})
	handler.OnEnd(ctx, nil, &einomodel.CallbackOutput{Message: schema.AssistantMessage("ok", nil)})
	if len(events) != 0 {
		t.Fatalf("restored completed image emitted lifecycle events = %#v", events)
	}
}

func TestProjectEinoAssistantModelCallbackRetriesFailedImageLifecycle(t *testing.T) {
	runState := newProjectEinoAssistantRunState()
	var events []projectAssistantModelInputEvent
	handler := newProjectEinoAssistantModelCallbackHandler(projectAssistantStreamCallbacks{
		OnModelInput: func(event projectAssistantModelInputEvent) { events = append(events, event) },
	}, runState, nil)
	runState.NextModelCallOrdinal()
	ctx := handler.OnStart(context.Background(), nil, &einomodel.CallbackInput{Messages: []*schema.Message{assistantModelInputMessageForTest()}})
	handler.OnError(ctx, nil, errors.New("temporary provider failure"))
	runState.NextModelCallOrdinal()
	ctx = handler.OnStart(context.Background(), nil, &einomodel.CallbackInput{Messages: []*schema.Message{assistantModelInputMessageForTest()}})
	handler.OnEnd(ctx, nil, &einomodel.CallbackOutput{Message: schema.AssistantMessage("ok", nil)})
	if len(events) != 4 || events[1].Status != "failed" || events[2].Status != "started" || events[3].Status != "completed" {
		t.Fatalf("failed image retry lifecycle events = %#v", events)
	}
}

func TestProjectEinoSortedToolCallsHandlesSparseIndexes(t *testing.T) {
	got := projectEinoSortedToolCalls(map[int]chatToolCall{
		2: {ID: "call-2"},
		0: {ID: "call-0"},
	})
	if len(got) != 2 {
		t.Fatalf("tool call count = %d, want 2: %#v", len(got), got)
	}
	if got[0].ID != "call-0" || got[1].ID != "call-2" {
		t.Fatalf("tool calls = %#v, want sorted sparse indexes without duplicates", got)
	}
}

func TestProjectEinoAssistantModelCallbackRecordsStreamedToolCalls(t *testing.T) {
	runState := newProjectEinoAssistantRunState()
	run := &store.AssistantRun{ID: "run-stream-callback"}
	auditRecorder := newProjectAssistantRunAuditRecorder(projectAssistantRunRequest{}, run, time.Now().UTC())
	if err := auditRecorder.recordModelCall(
		context.Background(),
		1,
		0,
		0,
		nil,
		nil,
		nil,
	); err != nil {
		t.Fatal(err)
	}
	var chunks []string
	var statuses []string
	handler := newProjectEinoAssistantModelCallbackHandler(projectAssistantStreamCallbacks{
		OnChunk:  func(chunk string) { chunks = append(chunks, chunk) },
		OnStatus: func(status string) { statuses = append(statuses, status) },
	}, runState, auditRecorder)

	ctx := handler.OnStart(context.Background(), nil, &einomodel.CallbackInput{
		Messages: []*schema.Message{schema.UserMessage("write src/App.tsx")},
	})
	index := 2
	stream := schema.StreamReaderFromArray([]callbacks.CallbackOutput{
		&einomodel.CallbackOutput{Message: schema.AssistantMessage("Working ", nil)},
		&einomodel.CallbackOutput{Message: schema.AssistantMessage("", []schema.ToolCall{{
			Index: &index,
			ID:    "call-write",
			Type:  "function",
			Function: schema.FunctionCall{
				Name:      projectToolEditFile,
				Arguments: `{"path":`,
			},
		}})},
		&einomodel.CallbackOutput{Message: schema.AssistantMessage("", []schema.ToolCall{{
			Index: &index,
			Function: schema.FunctionCall{
				Arguments: `"src/App.tsx","oldString":"old","newString":"hi"}`,
			},
		}})},
	})
	handler.OnEndWithStreamOutput(ctx, nil, stream)

	state := runState.CheckpointState()
	if len(chunks) != 0 {
		t.Fatalf("chunks = %#v, want model callback to avoid publishing public content chunks", chunks)
	}
	if len(statuses) != 1 || statuses[0] != "Preparing action" {
		t.Fatalf("statuses = %#v, want one preparation status", statuses)
	}
	if len(state.Messages) != 2 || state.Messages[0].Role != "user" || !strings.Contains(state.Messages[1].Content, "Working") {
		t.Fatalf("checkpoint messages = %#v, want user input and streamed assistant reply", state.Messages)
	}
	if len(state.ToolCalls) != 1 {
		t.Fatalf("tool calls = %#v, want one merged tool call", state.ToolCalls)
	}
	call := state.ToolCalls[0]
	if call.ID != "call-write" || call.Function.Name != projectToolEditFile || call.Function.Arguments != `{"path":"src/App.tsx","oldString":"old","newString":"hi"}` {
		t.Fatalf("tool call = %#v, want merged streamed function call", call)
	}
	var audit projectAssistantRunAudit
	if err := json.Unmarshal(run.Audit, &audit); err != nil {
		t.Fatal(err)
	}
	modelCall := audit.ModelCalls[0]
	if modelCall.FirstResponseAtOffsetMS == nil ||
		modelCall.ToolCallStartedAtOffsetMS == nil ||
		modelCall.CompletedAtOffsetMS != nil {
		t.Fatalf("stream callback milestones = %#v", modelCall)
	}
	if strings.Contains(string(run.Audit), "src/App.tsx") || strings.Contains(string(run.Audit), `"content":"hi"`) {
		t.Fatalf("stream callback audit leaked tool arguments: %s", run.Audit)
	}
}

func TestProjectEinoAssistantModelCallbackDoesNotPublishPublicContentChunks(t *testing.T) {
	runState := newProjectEinoAssistantRunState()
	var chunks []string
	handler := newProjectEinoAssistantModelCallbackHandler(projectAssistantStreamCallbacks{
		OnChunk: func(chunk string) { chunks = append(chunks, chunk) },
	}, runState, nil)

	ctx := handler.OnStart(context.Background(), nil, &einomodel.CallbackInput{
		Messages: []*schema.Message{schema.UserMessage("say thanks")},
	})
	stream := schema.StreamReaderFromArray([]callbacks.CallbackOutput{
		&einomodel.CallbackOutput{Message: schema.AssistantMessage("You are welcome.", nil)},
	})
	handler.OnEndWithStreamOutput(ctx, nil, stream)

	if len(chunks) != 0 {
		t.Fatalf("chunks = %#v, want model callback to avoid publishing public content chunks", chunks)
	}
	state := runState.CheckpointState()
	if len(state.Messages) != 2 || state.Messages[1].Content != "You are welcome." {
		t.Fatalf("checkpoint messages = %#v, want streamed assistant reply recorded", state.Messages)
	}
}

func TestProjectEinoAssistantModelCallbackCarriesLatestStreamUsageIntoAudit(t *testing.T) {
	runState := newProjectEinoAssistantRunState()
	run := &store.AssistantRun{ID: "run-stream-usage"}
	auditRecorder := newProjectAssistantRunAuditRecorder(projectAssistantRunRequest{}, run, time.Now().UTC())
	if err := auditRecorder.recordModelCall(context.Background(), 1, 0, 0, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	runState.NextModelCallOrdinal()
	handler := newProjectEinoAssistantModelCallbackHandler(projectAssistantStreamCallbacks{}, runState, auditRecorder)
	ctx := handler.OnStart(context.Background(), nil, &einomodel.CallbackInput{
		Messages: []*schema.Message{schema.UserMessage("inspect the project")},
	})
	usage := &schema.TokenUsage{
		PromptTokens:       100,
		PromptTokenDetails: schema.PromptTokenDetails{CachedTokens: 20},
		CompletionTokens:   10,
		TotalTokens:        110,
	}
	index := 0
	final := schema.AssistantMessage("I found it.", []schema.ToolCall{{
		Index: &index,
		ID:    "call-read",
		Type:  "function",
		Function: schema.FunctionCall{
			Name:      projectToolReadFile,
			Arguments: `{"file_path":"src/App.tsx"}`,
		},
	}})
	final.ResponseMeta = &schema.ResponseMeta{Usage: usage}
	stream := schema.StreamReaderFromArray([]callbacks.CallbackOutput{
		&einomodel.CallbackOutput{Message: schema.AssistantMessage("I found ", nil)},
		&einomodel.CallbackOutput{Message: final},
	})
	handler.OnEndWithStreamOutput(ctx, nil, stream)

	var audit projectAssistantRunAudit
	if err := json.Unmarshal(run.Audit, &audit); err != nil {
		t.Fatal(err)
	}
	if audit.ModelCallStats == nil {
		t.Fatal("model-call stats missing")
	}
	stats := audit.ModelCallStats
	if stats.PromptTokens != 100 || stats.CachedPromptTokens != 20 || stats.CompletionTokens != 10 || stats.TotalTokens != 110 || stats.MissingUsageCalls != 0 {
		t.Fatalf("stream usage rollup = %#v, want provider usage", stats)
	}
	if len(audit.ModelCalls) != 1 || audit.ModelCalls[0].PromptTokens != 100 || audit.ModelCalls[0].CachedPromptTokens != 20 || audit.ModelCalls[0].CompletionTokens != 10 || audit.ModelCalls[0].TotalTokens != 110 {
		t.Fatalf("stream usage detail = %#v, want provider usage", audit.ModelCalls)
	}
}
