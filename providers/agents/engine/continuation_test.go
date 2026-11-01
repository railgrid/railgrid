// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package engine

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"github.com/railgrid/provider-agents/backend"
)

func TestClaudeContinuationSurvivesApprovalCheckpointAndResume(t *testing.T) {
	model := &claudeContinuationMockModel{}
	tool := Tool{Name: "read_file", ExecRich: func(context.Context, string) (Observation, error) {
		return Observation{}, &InterruptError{Tool: "read_file", Args: `{"path":"README.md"}`, RequestID: "approval-1"}
	}}
	var assistantMessages []AssistantMessage
	paused, err := New().StreamTurnWithTools(context.Background(), model,
		[]Message{{Role: RoleUser, Content: "Read README.md."}}, []Tool{tool}, TurnConfig{MaxIters: 4}, Callbacks{
			OnAssistantMessage: func(message AssistantMessage) { assistantMessages = append(assistantMessages, message) },
		})
	if err != nil || paused.Interrupt == nil {
		t.Fatalf("turn did not pause at the gated tool: result=%+v err=%v", paused, err)
	}
	if len(assistantMessages) != 1 || assistantMessages[0].Content != "" || assistantMessages[0].Continuation == nil ||
		assistantMessages[0].Continuation.ReasoningContent != "" ||
		assistantMessages[0].Continuation.ReasoningSignature != "opaque-signature-for-omitted-thinking" {
		t.Fatalf("assistant callback lost signature-only continuation or exposed prose: %#v", assistantMessages)
	}

	checkpoint := paused.Interrupt.Checkpoint
	if err := validateCheckpointContinuations(checkpoint.Messages); err != nil {
		t.Fatalf("checkpoint continuation invalid: %v", err)
	}
	last := checkpoint.Messages[len(checkpoint.Messages)-1]
	if last.Role != RoleAssistant || last.Continuation == nil || last.Continuation.ReasoningContent != "" ||
		last.Continuation.ReasoningSignature != assistantMessages[0].Continuation.ReasoningSignature {
		t.Fatalf("approval checkpoint lost signature-only continuation: %#v", last)
	}

	encoded, err := json.Marshal(checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "hidden thinking text") || !strings.Contains(string(encoded), "opaque-signature-for-omitted-thinking") {
		t.Fatalf("checkpoint should retain opaque continuation without inventing thinking prose: %s", encoded)
	}
	var decoded Checkpoint
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}

	approvedTool := Tool{Name: "read_file", ExecRich: func(_ context.Context, args string) (Observation, error) {
		if args != `{"path":"README.md"}` {
			t.Errorf("approved call args = %q", args)
		}
		return Observation{Text: "README contents"}, nil
	}}
	resumed, err := New().ResumeTurnWithTools(context.Background(), model, decoded, []Tool{approvedTool}, TurnConfig{MaxIters: 4}, true, "", Callbacks{})
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if resumed.Interrupt != nil || resumed.FinalContent != "Done" {
		t.Fatalf("resume result = %+v", resumed)
	}
	if len(model.inputs) != 2 {
		t.Fatalf("model requests = %d, want initial and resumed request", len(model.inputs))
	}
	var replayedAssistant, replayedTool *schema.Message
	for _, message := range model.inputs[1] {
		if message.Role == schema.Assistant && len(message.ToolCalls) > 0 {
			replayedAssistant = message
		}
		if message.Role == schema.Tool {
			replayedTool = message
		}
	}
	if replayedAssistant == nil || replayedAssistant.Extra["_eino_claude_thinking"] != "" ||
		replayedAssistant.Extra["_eino_claude_thinking_signature"] != "opaque-signature-for-omitted-thinking" {
		t.Fatalf("resumed model input lost exact signature-only continuation: %#v", replayedAssistant)
	}
	if replayedTool == nil || replayedTool.ToolCallID != "call-1" || replayedTool.Content != "README contents" {
		t.Fatalf("resumed model input lost matching tool result: %#v", replayedTool)
	}
}

func TestResumeRejectsInvalidContinuationMetadata(t *testing.T) {
	checkpoint := Checkpoint{Messages: []CheckpointMessage{{Role: RoleUser, Content: "hello"}, {
		Role: RoleAssistant, Continuation: &backend.ContinuationMetadata{ReasoningContent: "unsigned"},
	}}}
	_, err := New().ResumeTurnWithTools(context.Background(), &claudeContinuationMockModel{}, checkpoint, nil, TurnConfig{}, true, "", Callbacks{})
	if err == nil || !strings.Contains(err.Error(), "invalid continuation metadata") {
		t.Fatalf("resume error = %v, want invalid continuation rejection", err)
	}
}

type claudeContinuationMockModel struct {
	calls  int
	inputs [][]*schema.Message
}

func (m *claudeContinuationMockModel) Generate(context.Context, []*schema.Message, ...einomodel.Option) (*schema.Message, error) {
	return nil, errors.New("Generate is not used")
}

func (m *claudeContinuationMockModel) WithTools([]*schema.ToolInfo) (einomodel.ToolCallingChatModel, error) {
	return m, nil
}

func (m *claudeContinuationMockModel) Stream(_ context.Context, in []*schema.Message, _ ...einomodel.Option) (*schema.StreamReader[*schema.Message], error) {
	m.calls++
	m.inputs = append(m.inputs, in)
	if m.calls == 1 {
		index := 0
		return schema.StreamReaderFromArray([]*schema.Message{
			{Role: schema.Assistant, ToolCalls: []schema.ToolCall{{
				Index: &index, ID: "call-1", Type: "function",
				Function: schema.FunctionCall{Name: "read_file", Arguments: `{"path":"README.md"}`},
			}}},
			{Role: schema.Assistant, Extra: map[string]any{
				"_eino_claude_thinking":           "",
				"_eino_claude_thinking_signature": "opaque-signature-for-omitted-thinking",
			}},
		}), nil
	}
	return schema.StreamReaderFromArray([]*schema.Message{{Role: schema.Assistant, Content: "Done"}}), nil
}
