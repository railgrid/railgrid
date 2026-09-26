// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package api

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"github.com/railgrid/provider-agents/engine"
)

func TestToolImageIsDroppedBeforeSessionCompactionCheckpoint(t *testing.T) {
	f := newCompactFixture(t, 4, 120, "gpt-4o")
	const (
		runID = "run-image-compaction"
		task  = "keep the current camera request"
		call  = "call-image-compaction"
	)
	run, history := appendAndAssembleCurrentTask(t, f, runID, task)
	model := &imageCompactionModel{callID: call}
	toolResult := "snapshot fetched"
	var compactionInputs, compactedHistory []engine.Message
	compactionCalls := 0
	var compactCheckpoint engine.Checkpoint

	_, err := engine.New().StreamTurnWithTools(context.Background(), model, history, []engine.Tool{{
		Name: "camera_snapshot", Desc: "fetches a camera snapshot",
		Params: map[string]engine.Param{"camera": {Type: "string", Desc: "camera name", Required: true}},
		ExecRich: func(context.Context, string) (engine.Observation, error) {
			return engine.Observation{
				Text:   toolResult,
				Images: []engine.ToolImage{{MIMEType: "image/jpeg", Data: []byte{0xff, 0xd8, 0xff, 0xd9}}},
			}, nil
		},
	}}, engine.TurnConfig{
		MaxIters: 3, ContextBudgetTokens: 3500,
		ContextCompactor: func(ctx context.Context, messages []engine.Message, estimate engine.ContextEstimate) ([]engine.Message, error) {
			compactionCalls++
			compactionInputs = append([]engine.Message(nil), messages...)
			replacement, compactErr := f.s.contextCompactor(run, "chat", "gpt-4o")(ctx, messages, estimate)
			compactedHistory = replacement
			return replacement, compactErr
		},
	}, engine.Callbacks{
		OnAssistantMessage: func(message engine.AssistantMessage) {
			if !message.HasToolCalls {
				return
			}
			if len(message.ToolCalls) != 1 || message.ToolCalls[0].ID != call {
				t.Errorf("assistant tool call = %#v, want persisted call %q", message.ToolCalls, call)
				return
			}
			at := time.Now().UTC()
			appendCompactMessage(t, f, assistantCallRow(
				"assistant-image-compaction", runID, call,
				message.ToolCalls[0].Function.Name, message.ToolCalls[0].Function.Arguments, at,
			))
		},
		OnTool: func(event engine.ToolEvent) {
			appendCompactMessage(t, f, toolResultRow(
				"tool-image-compaction", runID, event.ID, event.Name, event.Result, time.Now().UTC().Add(time.Second),
			))
		},
		OnCheckpoint: func(checkpoint engine.Checkpoint) {
			if compactCheckpoint.Messages == nil {
				compactCheckpoint = checkpoint
			}
		},
	})
	if err != nil {
		t.Fatalf("StreamTurnWithTools() error = %v", err)
	}
	if compactionCalls != 1 {
		t.Fatalf("context compactor calls = %d, want one after the tool returned an image", compactionCalls)
	}
	if len(compactionInputs) == 0 {
		t.Fatal("context compactor did not receive the model history")
	}
	sawEphemeralImage := false
	for _, message := range compactionInputs {
		if message.Ephemeral && message.Role == engine.RoleUser && strings.Contains(message.Content, "multimodal content") {
			sawEphemeralImage = true
		}
	}
	if !sawEphemeralImage {
		t.Fatalf("actual image follow-up did not carry ephemeral provenance into compaction: %#v", compactionInputs)
	}
	if countHistoryMessage(compactedHistory, "task-"+runID, task) != 1 {
		t.Fatalf("durable current request appears %d times after compaction, want once", countHistoryMessage(compactedHistory, "task-"+runID, task))
	}
	for _, message := range compactedHistory {
		if message.Ephemeral || strings.Contains(message.Content, "multimodal content from tool calls omitted during context compaction") {
			t.Fatalf("ephemeral image note survived in the replacement history: %#v", message)
		}
	}
	if compactCheckpoint.Messages == nil {
		t.Fatal("engine did not checkpoint the compacted history before the next model request")
	}
	for _, message := range compactCheckpoint.Messages {
		if message.Ephemeral || strings.Contains(message.Content, "multimodal content from tool calls omitted during context compaction") {
			t.Fatalf("ephemeral image note survived in the engine compaction checkpoint: %#v", message)
		}
	}
	if len(model.inputs) != 2 {
		t.Fatalf("model calls = %d, want initial tool call and post-compaction answer", len(model.inputs))
	}
	for _, message := range model.inputs[1] {
		if strings.Contains(message.Content, "multimodal content from tool calls omitted during context compaction") {
			t.Fatalf("post-compaction model input still contains the image placeholder: %#v", message)
		}
	}
	loaded := loadReplayHistory(t, f)
	if countHistoryMessage(loaded, "task-"+runID, task) != 1 {
		t.Fatalf("replayed durable current request appears %d times, want once", countHistoryMessage(loaded, "task-"+runID, task))
	}
	for _, message := range loaded {
		if strings.Contains(message.Content, "multimodal content from tool calls omitted during context compaction") {
			t.Fatalf("ephemeral image placeholder leaked into durable session replay: %#v", message)
		}
	}
}

type imageCompactionModel struct {
	callID string
	calls  int
	inputs [][]*schema.Message
}

func (m *imageCompactionModel) WithTools([]*schema.ToolInfo) (einomodel.ToolCallingChatModel, error) {
	return m, nil
}

func (m *imageCompactionModel) Generate(context.Context, []*schema.Message, ...einomodel.Option) (*schema.Message, error) {
	return nil, fmt.Errorf("Generate() is not used by the streaming engine")
}

func (m *imageCompactionModel) Stream(_ context.Context, input []*schema.Message, _ ...einomodel.Option) (*schema.StreamReader[*schema.Message], error) {
	m.calls++
	m.inputs = append(m.inputs, append([]*schema.Message(nil), input...))
	if m.calls == 1 {
		index := 0
		return schema.StreamReaderFromArray([]*schema.Message{{
			Role: schema.Assistant,
			ToolCalls: []schema.ToolCall{{
				Index: &index, ID: m.callID,
				Function: schema.FunctionCall{Name: "camera_snapshot", Arguments: `{"camera":"front-door"}`},
			}},
		}}), nil
	}
	return schema.StreamReaderFromArray([]*schema.Message{{
		Role: schema.Assistant, Content: "The snapshot was retrieved.",
		ResponseMeta: &schema.ResponseMeta{Usage: &schema.TokenUsage{PromptTokens: 40, CompletionTokens: 8}},
	}}), nil
}
