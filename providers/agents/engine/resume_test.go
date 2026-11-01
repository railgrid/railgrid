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
	"github.com/railgrid/provider-agents/llm"
)

// gatedTool returns a tool that interrupts instead of executing, recording
// whether it was ever actually run.
func gatedTool(name string, executed *bool) Tool {
	return Tool{
		Name: name,
		Params: map[string]Param{
			"city": {Type: "string", Desc: "city", Required: true},
		},
		ExecRich: func(_ context.Context, args string) (Observation, error) {
			return Observation{}, &InterruptError{Tool: name, Args: args, RequestID: "inbox-1"}
		},
	}
}

// TestInterruptCheckpointsTheTurn: a gated tool pauses the loop and yields a
// checkpoint carrying the conversation so far plus the un-executed call.
func TestInterruptCheckpointsTheTurn(t *testing.T) {
	m := &toolMockModel{}
	executed := false
	res, err := New().StreamTurnWithTools(context.Background(), m,
		[]Message{{Role: RoleUser, Content: "weather in vilnius?"}},
		[]Tool{gatedTool("get_weather", &executed)},
		TurnConfig{MaxIters: 8}, Callbacks{})
	if err != nil {
		t.Fatalf("interrupt must not be an error: %v", err)
	}
	if res.Interrupt == nil {
		t.Fatal("want an Interrupt, got a completed turn")
	}
	if res.Interrupt.Tool != "get_weather" || res.Interrupt.RequestID != "inbox-1" {
		t.Fatalf("interrupt = %+v", res.Interrupt)
	}
	if res.Interrupt.Args != `{"city":"Vilnius"}` {
		t.Fatalf("checkpoint must keep the exact requested args, got %q", res.Interrupt.Args)
	}
	ck := res.Interrupt.Checkpoint
	if len(ck.Pending) != 1 || ck.Pending[0].Name != "get_weather" {
		t.Fatalf("pending calls = %+v, want the gated call first", ck.Pending)
	}
	// The conversation must include the user turn and the assistant's
	// tool-call message so a resume can continue the same exchange.
	if len(ck.Messages) < 2 {
		t.Fatalf("checkpoint messages = %+v", ck.Messages)
	}
	last := ck.Messages[len(ck.Messages)-1]
	if len(last.ToolCalls) != 1 || last.ToolCalls[0].Name != "get_weather" {
		t.Fatalf("last checkpoint message should carry the tool call: %+v", last)
	}
	if m.calls != 1 {
		t.Fatalf("model called %d times; the loop must stop at the gate", m.calls)
	}
	if !strings.Contains(res.Content, "Checking") {
		t.Fatalf("streamed content before the gate should be preserved, got %q", res.Content)
	}
}

// TestResumeApprovedExecutesAndContinues: approving resumes in place — the
// gated call runs and the model produces its final answer, without replaying
// the user's prompt.
func TestResumeApprovedExecutesAndContinues(t *testing.T) {
	m := &toolMockModel{}
	executed := false
	paused, err := New().StreamTurnWithTools(context.Background(), m,
		[]Message{{Role: RoleUser, Content: "weather in vilnius?"}},
		[]Tool{gatedTool("get_weather", &executed)},
		TurnConfig{MaxIters: 8}, Callbacks{})
	if err != nil || paused.Interrupt == nil {
		t.Fatalf("setup: %v %+v", err, paused)
	}

	// On resume the tool is pre-authorized, so it executes normally.
	gotArgs := ""
	approvedTool := Tool{
		Name:   "get_weather",
		Params: map[string]Param{"city": {Type: "string", Desc: "city", Required: true}},
		ExecRich: func(_ context.Context, args string) (Observation, error) {
			executed = true
			gotArgs = args
			return Observation{Text: "sunny, 24C"}, nil
		},
	}
	var events []ToolEvent
	res, err := New().ResumeTurnWithTools(context.Background(), m, paused.Interrupt.Checkpoint,
		[]Tool{approvedTool}, TurnConfig{MaxIters: 8}, true, "", Callbacks{OnTool: func(ev ToolEvent) { events = append(events, ev) }})
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if res.Interrupt != nil {
		t.Fatalf("resumed turn paused again: %+v", res.Interrupt)
	}
	if !executed {
		t.Fatal("approved tool did not execute on resume")
	}
	if gotArgs != `{"city":"Vilnius"}` {
		t.Fatalf("resumed call used args %q, want the originally requested ones", gotArgs)
	}
	if !strings.Contains(res.Content, "sunny in Vilnius") {
		t.Fatalf("final content %q", res.Content)
	}
	// Streamed content from before the pause must survive into the final answer.
	if !strings.Contains(res.Content, "Checking") {
		t.Fatalf("pre-pause content lost: %q", res.Content)
	}
	if len(events) != 1 || events[0].Name != "get_weather" {
		t.Fatalf("tool events = %+v", events)
	}
	if !m.sawToolMsg {
		t.Fatal("the tool observation was not fed back to the model on resume")
	}
}

// TestResumeDeniedFeedsRefusalBack: denying does not execute the tool; the
// model receives a denial observation and answers around it.
func TestResumeDeniedFeedsRefusalBack(t *testing.T) {
	m := &toolMockModel{}
	executed := false
	paused, err := New().StreamTurnWithTools(context.Background(), m,
		[]Message{{Role: RoleUser, Content: "weather in vilnius?"}},
		[]Tool{gatedTool("get_weather", &executed)},
		TurnConfig{MaxIters: 8}, Callbacks{})
	if err != nil || paused.Interrupt == nil {
		t.Fatalf("setup: %v", err)
	}
	tool := Tool{
		Name:   "get_weather",
		Params: map[string]Param{"city": {Type: "string", Desc: "city", Required: true}},
		ExecRich: func(context.Context, string) (Observation, error) {
			executed = true
			return Observation{Text: "should not run"}, nil
		},
	}
	res, err := New().ResumeTurnWithTools(context.Background(), m, paused.Interrupt.Checkpoint,
		[]Tool{tool}, TurnConfig{MaxIters: 8}, false, "not allowed in prod", Callbacks{})
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if executed {
		t.Fatal("denied tool executed")
	}
	if res.Interrupt != nil {
		t.Fatalf("denied resume paused again: %+v", res.Interrupt)
	}
	// The model must have seen a tool message explaining the denial.
	var toolMsg *schema.Message
	for _, msg := range m.gotIn {
		if msg.Role == schema.Tool {
			toolMsg = msg
		}
	}
	if toolMsg == nil {
		t.Fatal("no tool observation was fed back after the denial")
	}
	if !strings.Contains(toolMsg.Content, "denied") || !strings.Contains(toolMsg.Content, "not allowed in prod") {
		t.Fatalf("denial observation = %q, want the refusal and the note", toolMsg.Content)
	}
}

// TestCheckpointRoundTrip: the serializable form rebuilds an equivalent
// conversation (this is what gets stored as JSON in the run record).
func TestCheckpointRoundTrip(t *testing.T) {
	idx := 0
	original := []*schema.Message{
		schema.SystemMessage("be helpful"),
		schema.UserMessage("hi"),
		{Role: schema.Assistant, ToolCalls: []schema.ToolCall{{
			Index: &idx, ID: "tc-9", Type: "function",
			Extra: map[string]any{
				llm.ResponsesToolItemIDExtraKey:    "fc_9",
				llm.ResponsesToolReasoningExtraKey: `[{"id":"rs_9","encrypted_content":"opaque-signature"}]`,
				llm.ResponsesToolOriginExtraKey:    "sha256:credential-fingerprint",
			},
			Function: schema.FunctionCall{Name: "do_thing", Arguments: `{"a":1}`},
		}}},
		schema.ToolMessage("done", "tc-9", schema.WithToolName("do_thing")),
	}
	restored := restoreMessages(checkpointMessages(original))
	if len(restored) != len(original) {
		t.Fatalf("restored %d messages, want %d", len(restored), len(original))
	}
	for i := range original {
		if restored[i].Role != original[i].Role || restored[i].Content != original[i].Content {
			t.Fatalf("message %d: got %+v, want %+v", i, restored[i], original[i])
		}
	}
	tc := restored[2].ToolCalls
	if len(tc) != 1 || tc[0].ID != "tc-9" || tc[0].Function.Arguments != `{"a":1}` {
		t.Fatalf("tool call did not round-trip: %+v", tc)
	}
	if tc[0].Extra[llm.ResponsesToolItemIDExtraKey] != "fc_9" || !strings.Contains(tc[0].Extra[llm.ResponsesToolReasoningExtraKey].(string), "opaque-signature") || tc[0].Extra[llm.ResponsesToolOriginExtraKey] != "sha256:credential-fingerprint" {
		t.Fatalf("tool continuation metadata did not round-trip: %#v", tc[0].Extra)
	}
	if restored[3].ToolCallID != "tc-9" || restored[3].ToolName != "do_thing" {
		t.Fatalf("tool message did not round-trip: %+v", restored[3])
	}
}

func TestResponsesContinuationMetadataSurvivesConcatToolExecutionAndResume(t *testing.T) {
	model := &responsesContinuationModel{}
	tool := Tool{Name: "read_file", ExecRich: func(context.Context, string) (Observation, error) {
		return Observation{}, &InterruptError{Tool: "read_file", Args: `{"path":"README.md"}`, RequestID: "approval-1"}
	}}
	paused, err := New().StreamTurnWithTools(context.Background(), model,
		[]Message{{Role: RoleUser, Content: "Read README.md."}}, []Tool{tool}, TurnConfig{MaxIters: 4}, Callbacks{
			HydrateToolCalls: llm.HydrateResponsesToolCalls,
		})
	if err != nil || paused.Interrupt == nil {
		t.Fatalf("turn did not park at the gated tool call: result=%+v err=%v", paused, err)
	}
	checkpoint := paused.Interrupt.Checkpoint
	last := checkpoint.Messages[len(checkpoint.Messages)-1]
	if len(last.ToolCalls) != 1 || last.ToolCalls[0].Extra[llm.ResponsesToolItemIDExtraKey] != "fc_1" ||
		last.ToolCalls[0].Extra[llm.ResponsesToolOriginExtraKey] != "sha256:credential-fingerprint" ||
		!strings.Contains(last.ToolCalls[0].Extra[llm.ResponsesToolReasoningExtraKey].(string), "opaque-signature") {
		t.Fatalf("approval checkpoint lost Responses continuation metadata: %#v", last.ToolCalls)
	}
	encodedCheckpoint, err := json.Marshal(checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encodedCheckpoint), "private reasoning summary") || !strings.Contains(string(encodedCheckpoint), "opaque-signature") {
		t.Fatalf("checkpoint must retain opaque continuation only: %s", encodedCheckpoint)
	}

	approved := Tool{Name: "read_file", ExecRich: func(_ context.Context, args string) (Observation, error) {
		if args != `{"path":"README.md"}` {
			t.Errorf("approved call args = %q", args)
		}
		return Observation{Text: "README contents"}, nil
	}}
	resumed, err := New().ResumeTurnWithTools(context.Background(), model, checkpoint, []Tool{approved}, TurnConfig{MaxIters: 4}, true, "", Callbacks{
		HydrateToolCalls: llm.HydrateResponsesToolCalls,
	})
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if resumed.Interrupt != nil || resumed.FinalContent != "Done" {
		t.Fatalf("resume result = %+v", resumed)
	}
	if len(model.inputs) != 2 {
		t.Fatalf("model requests = %d, want initial plus resumed continuation", len(model.inputs))
	}
	input := model.inputs[1]
	var call *schema.ToolCall
	var toolResult *schema.Message
	for _, message := range input {
		if message.Role == schema.Assistant && len(message.ToolCalls) == 1 {
			call = &message.ToolCalls[0]
		}
		if message.Role == schema.Tool {
			toolResult = message
		}
	}
	if call == nil || call.ID != "call_1" || call.Extra[llm.ResponsesToolItemIDExtraKey] != "fc_1" ||
		call.Extra[llm.ResponsesToolOriginExtraKey] != "sha256:credential-fingerprint" ||
		!strings.Contains(call.Extra[llm.ResponsesToolReasoningExtraKey].(string), "opaque-signature") {
		t.Fatalf("resumed model input lost original call metadata: %#v", input)
	}
	if toolResult == nil || toolResult.ToolCallID != "call_1" || toolResult.Content != "README contents" {
		t.Fatalf("resumed model input lost the matching tool result: %#v", input)
	}
}

type responsesContinuationModel struct {
	inputs [][]*schema.Message
	calls  int
}

func (m *responsesContinuationModel) Generate(context.Context, []*schema.Message, ...einomodel.Option) (*schema.Message, error) {
	return nil, errors.New("not used")
}

func (m *responsesContinuationModel) WithTools([]*schema.ToolInfo) (einomodel.ToolCallingChatModel, error) {
	return m, nil
}

func (m *responsesContinuationModel) Stream(_ context.Context, in []*schema.Message, _ ...einomodel.Option) (*schema.StreamReader[*schema.Message], error) {
	m.calls++
	m.inputs = append(m.inputs, in)
	if m.calls == 1 {
		index := 0
		return schema.StreamReaderFromArray([]*schema.Message{
			{Role: schema.Assistant, ToolCalls: []schema.ToolCall{{
				Index: &index, ID: "call_1", Type: "function",
				Function: schema.FunctionCall{Name: "read_file", Arguments: `{"path":"README.md"}`},
			}}},
			{Role: schema.Assistant, ToolCalls: []schema.ToolCall{{Index: &index}}, Extra: map[string]any{
				"railgrid_openai_responses_tool_state": `{"call_1":{"railgrid_openai_responses_item_id":"fc_1","railgrid_openai_responses_reasoning":"[{\"id\":\"rs_1\",\"encrypted_content\":\"opaque-signature\"}]","railgrid_openai_responses_origin":"sha256:credential-fingerprint"}}`,
			}},
		}), nil
	}
	return schema.StreamReaderFromArray([]*schema.Message{{Role: schema.Assistant, Content: "Done"}}), nil
}
