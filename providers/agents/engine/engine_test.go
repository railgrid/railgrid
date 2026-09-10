// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package engine

import (
	"context"
	"errors"
	"strings"
	"testing"

	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// mockModel streams a fixed set of chunks (the last carrying usage), so the
// engine's streaming loop and usage extraction can be exercised without a
// network call.
type mockModel struct {
	chunks []*schema.Message
	gotIn  []*schema.Message
}

func (m *mockModel) Generate(_ context.Context, in []*schema.Message, _ ...einomodel.Option) (*schema.Message, error) {
	m.gotIn = in
	return schema.AssistantMessage("", nil), nil
}

func (m *mockModel) Stream(_ context.Context, in []*schema.Message, _ ...einomodel.Option) (*schema.StreamReader[*schema.Message], error) {
	m.gotIn = in
	return schema.StreamReaderFromArray(m.chunks), nil
}

type errorStreamModel struct{}

func (errorStreamModel) Generate(context.Context, []*schema.Message, ...einomodel.Option) (*schema.Message, error) {
	return nil, errors.New("generate unavailable")
}

func (errorStreamModel) Stream(_ context.Context, _ []*schema.Message, _ ...einomodel.Option) (*schema.StreamReader[*schema.Message], error) {
	reader, writer := schema.Pipe[*schema.Message](2)
	go func() {
		defer writer.Close()
		writer.Send(&schema.Message{Role: schema.Assistant, Content: "partial"}, nil)
		writer.Send(nil, errors.New("upstream disconnected"))
	}()
	return reader, nil
}

func TestStreamTurn_AccumulatesDeltasAndUsage(t *testing.T) {
	m := &mockModel{chunks: []*schema.Message{
		{Role: schema.Assistant, Content: "Hello"},
		{Role: schema.Assistant, Content: ", world"},
		{Role: schema.Assistant, Content: "!", ResponseMeta: &schema.ResponseMeta{
			Usage: &schema.TokenUsage{PromptTokens: 12, CompletionTokens: 3},
		}},
	}}

	var deltas []string
	res, err := New().StreamTurn(context.Background(), m, []Message{
		{Role: RoleSystem, Content: "be brief"},
		{Role: RoleUser, Content: "hi"},
	}, func(d string) { deltas = append(deltas, d) })
	if err != nil {
		t.Fatalf("StreamTurn: %v", err)
	}
	if res.Content != "Hello, world!" {
		t.Fatalf("content = %q, want %q", res.Content, "Hello, world!")
	}
	if strings.Join(deltas, "") != "Hello, world!" {
		t.Fatalf("deltas joined = %q", strings.Join(deltas, ""))
	}
	if res.Usage.InputTokens != 12 || res.Usage.OutputTokens != 3 {
		t.Fatalf("usage = %+v, want {12 3}", res.Usage)
	}
	// System + user messages both forwarded to the model.
	if len(m.gotIn) != 2 || m.gotIn[0].Role != schema.System || m.gotIn[1].Role != schema.User {
		t.Fatalf("model received wrong messages: %+v", m.gotIn)
	}
}

func TestStreamTurnWithTools_ReportsCompleteAssistantBoundaries(t *testing.T) {
	m := &toolMockModel{}
	var messages []AssistantMessage
	res, err := New().StreamTurnWithTools(context.Background(), m,
		[]Message{{Role: RoleUser, Content: "weather in vilnius?"}},
		[]Tool{{
			Name: "get_weather", Desc: "current weather",
			Params: map[string]Param{"city": {Type: "string", Required: true}},
			Exec:   func(context.Context, string) (string, error) { return "sunny", nil },
		}},
		TurnConfig{MaxIters: 8}, Callbacks{OnAssistantMessage: func(message AssistantMessage) {
			messages = append(messages, message)
		}})
	if err != nil {
		t.Fatalf("StreamTurnWithTools: %v", err)
	}
	if len(messages) != 2 {
		t.Fatalf("assistant messages = %+v, want one tool-call response and one final response", messages)
	}
	if messages[0].Content != "Checking… " || !messages[0].HasToolCalls {
		t.Fatalf("first assistant message = %+v, want tool-call boundary", messages[0])
	}
	if !messages[0].Complete {
		t.Fatalf("first assistant message = %+v, want complete tool-call response", messages[0])
	}
	if messages[1].Content != "It is sunny in Vilnius." || messages[1].HasToolCalls {
		t.Fatalf("second assistant message = %+v, want final boundary", messages[1])
	}
	if !messages[1].Complete {
		t.Fatalf("second assistant message = %+v, want complete final response", messages[1])
	}
	if messages[0].Duration < 0 || messages[1].Duration < 0 {
		t.Fatalf("assistant durations must be non-negative: %+v", messages)
	}
	if res.Content != "Checking… It is sunny in Vilnius." {
		t.Fatalf("result content = %q, want concatenated downstream result", res.Content)
	}
}

func TestStreamTurnWithTools_ReportsFailedAssistantDurationWithoutCompletion(t *testing.T) {
	var messages []AssistantMessage
	var deltas []string
	_, err := New().StreamTurnWithTools(context.Background(), errorStreamModel{},
		[]Message{{Role: RoleUser, Content: "hello"}}, nil, TurnConfig{MaxIters: 1}, Callbacks{
			OnDelta:            func(delta string) { deltas = append(deltas, delta) },
			OnAssistantMessage: func(message AssistantMessage) { messages = append(messages, message) },
		})
	if err == nil || !strings.Contains(err.Error(), "upstream disconnected") {
		t.Fatalf("error = %v, want upstream stream error", err)
	}
	if strings.Join(deltas, "") != "partial" {
		t.Fatalf("deltas = %q, want partial output", strings.Join(deltas, ""))
	}
	if len(messages) != 1 {
		t.Fatalf("assistant messages = %+v, want failed attempt", messages)
	}
	if messages[0].Complete {
		t.Fatalf("failed assistant message = %+v, must not be complete", messages[0])
	}
	if messages[0].Duration < 0 {
		t.Fatalf("failed assistant duration = %s, must be non-negative", messages[0].Duration)
	}
}

func TestStreamTurn_NilModel(t *testing.T) {
	if _, err := New().StreamTurn(context.Background(), nil, []Message{{Role: RoleUser, Content: "x"}}, nil); err == nil {
		t.Fatal("expected error for nil model")
	}
}

// toolMockModel implements ToolCallingChatModel: the first response asks for a
// tool call, the second (after the observation) produces the final answer.
type toolMockModel struct {
	mockModel
	calls      int
	boundTools []*schema.ToolInfo
	sawToolMsg bool
}

func (m *toolMockModel) WithTools(tools []*schema.ToolInfo) (einomodel.ToolCallingChatModel, error) {
	m.boundTools = tools
	return m, nil
}

func (m *toolMockModel) Stream(_ context.Context, in []*schema.Message, _ ...einomodel.Option) (*schema.StreamReader[*schema.Message], error) {
	m.calls++
	m.gotIn = in
	for _, msg := range in {
		if msg.Role == schema.Tool {
			m.sawToolMsg = true
		}
	}
	if m.calls == 1 {
		idx := 0
		return schema.StreamReaderFromArray([]*schema.Message{
			{Role: schema.Assistant, Content: "Checking… "},
			{Role: schema.Assistant, ToolCalls: []schema.ToolCall{{
				Index: &idx, ID: "tc-1",
				Function: schema.FunctionCall{Name: "get_weather", Arguments: `{"city":"Vilnius"}`},
			}}},
		}), nil
	}
	return schema.StreamReaderFromArray([]*schema.Message{
		{Role: schema.Assistant, Content: "It is sunny in Vilnius.", ResponseMeta: &schema.ResponseMeta{
			Usage: &schema.TokenUsage{PromptTokens: 20, CompletionTokens: 6},
		}},
	}), nil
}

func TestStreamTurnWithTools_ExecutesToolAndContinues(t *testing.T) {
	m := &toolMockModel{}
	var toolEvents []ToolEvent
	var gotArgs string

	res, err := New().StreamTurnWithTools(context.Background(), m,
		[]Message{{Role: RoleUser, Content: "weather in vilnius?"}},
		[]Tool{{
			Name: "get_weather", Desc: "current weather",
			Params: map[string]Param{"city": {Type: "string", Desc: "city name", Required: true}},
			Exec: func(_ context.Context, args string) (string, error) {
				gotArgs = args
				return "sunny, 24C", nil
			},
		}},
		TurnConfig{MaxIters: 8}, Callbacks{OnTool: func(ev ToolEvent) { toolEvents = append(toolEvents, ev) }})
	if err != nil {
		t.Fatalf("StreamTurnWithTools: %v", err)
	}
	if m.calls != 2 {
		t.Fatalf("model called %d times, want 2 (tool round-trip)", m.calls)
	}
	if !m.sawToolMsg {
		t.Fatal("tool observation was not fed back to the model")
	}
	if gotArgs != `{"city":"Vilnius"}` {
		t.Fatalf("tool got args %q", gotArgs)
	}
	if len(toolEvents) != 1 || toolEvents[0].Name != "get_weather" || toolEvents[0].Err {
		t.Fatalf("tool events wrong: %+v", toolEvents)
	}
	if !strings.Contains(res.Content, "sunny in Vilnius") {
		t.Fatalf("final content %q", res.Content)
	}
	if len(m.boundTools) != 1 || m.boundTools[0].Name != "get_weather" {
		t.Fatalf("tools not bound: %+v", m.boundTools)
	}
	if res.Usage.OutputTokens != 6 {
		t.Fatalf("usage %+v", res.Usage)
	}
}

func TestStreamTurnWithTools_ExecRichFeedsImageAsUserMessage(t *testing.T) {
	m := &toolMockModel{}
	png := []byte{0x89, 0x50, 0x4e, 0x47} // arbitrary bytes; content is opaque here

	_, err := New().StreamTurnWithTools(context.Background(), m,
		[]Message{{Role: RoleUser, Content: "snapshot please"}},
		[]Tool{{
			Name: "get_weather", Desc: "returns a snapshot",
			Params: map[string]Param{"city": {Type: "string", Desc: "city", Required: true}},
			ExecRich: func(_ context.Context, _ string) (Observation, error) {
				return Observation{Images: []ToolImage{{MIMEType: "image/jpeg", Data: png}}}, nil
			},
		}},
		TurnConfig{MaxIters: 8}, Callbacks{})
	if err != nil {
		t.Fatalf("StreamTurnWithTools: %v", err)
	}
	// m.gotIn holds the SECOND model call's input: it must include a synthetic
	// user message carrying the image as vision input, right after the tool msg.
	var imgMsg *schema.Message
	for _, msg := range m.gotIn {
		if msg.Role == schema.User && len(msg.UserInputMultiContent) > 0 {
			imgMsg = msg
		}
	}
	if imgMsg == nil {
		t.Fatalf("no image user message fed back; got roles %v", rolesOf(m.gotIn))
	}
	var haveImage bool
	for _, p := range imgMsg.UserInputMultiContent {
		if p.Type == schema.ChatMessagePartTypeImageURL && p.Image != nil && p.Image.Base64Data != nil {
			haveImage = true
			if p.Image.MIMEType != "image/jpeg" {
				t.Fatalf("image MIME = %q, want image/jpeg", p.Image.MIMEType)
			}
		}
	}
	if !haveImage {
		t.Fatal("image user message had no base64 image part")
	}
	// The tool message itself must still carry a non-empty text observation.
	var toolText string
	for _, msg := range m.gotIn {
		if msg.Role == schema.Tool {
			toolText = msg.Content
		}
	}
	if strings.TrimSpace(toolText) == "" {
		t.Fatal("tool observation text was empty (should note images shown below)")
	}
}

func rolesOf(msgs []*schema.Message) []string {
	out := make([]string, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, string(m.Role))
	}
	return out
}

func TestStreamTurnWithTools_UnknownToolReportsError(t *testing.T) {
	m := &toolMockModel{}
	var evs []ToolEvent
	_, err := New().StreamTurnWithTools(context.Background(), m,
		[]Message{{Role: RoleUser, Content: "hi"}},
		[]Tool{{Name: "other_tool", Desc: "x", Exec: func(context.Context, string) (string, error) { return "", nil }}},
		TurnConfig{MaxIters: 8}, Callbacks{OnTool: func(ev ToolEvent) { evs = append(evs, ev) }})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(evs) != 1 || !evs[0].Err || !strings.Contains(evs[0].Result, "unknown tool") {
		t.Fatalf("expected unknown-tool error event, got %+v", evs)
	}
}
