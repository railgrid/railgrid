// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package model

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"github.com/railgrid/provider-agents/backend"
	"github.com/railgrid/provider-agents/engine"
	"github.com/railgrid/provider-agents/llm"
)

func TestResponsesToolContinuationSurvivesEngineTurnAndApprovalResume(t *testing.T) {
	requests := make(chan map[string]any, 4)
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		calls++
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Errorf("decode Responses request: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		requests <- body
		w.Header().Set("Content-Type", "text/event-stream")
		if calls == 1 {
			writeResponsesFixtureToolCall(w)
			return
		}
		writeResponsesFixtureEvent(w, `{"type":"response.completed","response":{"id":"resp_final","status":"completed","output":[],"usage":{"input_tokens":12,"output_tokens":3,"total_tokens":15}}}`)
	}))
	defer server.Close()

	model, err := llm.NewResponsesModel(context.Background(), llm.Profile{
		Provider: llm.ProviderOpenAICompatible, BaseURL: server.URL + "/v1", Model: "gpt-6.1-sol", APIKey: "test-key",
	})
	if err != nil {
		t.Fatal(err)
	}
	backendImpl := New(engine.New(), model, "gpt-6.1-sol", nil)
	sink := &fixtureEventSink{}
	limits := backend.Limits{MaxToolTurns: 4}
	gatedTool := backend.Tool{Name: "read_file", ExecRich: func(context.Context, string) (backend.Observation, error) {
		return backend.Observation{}, &backend.GateError{Tool: "read_file", Args: `{"path":"README.md"}`, RequestID: "approval-1"}
	}}
	paused, err := backendImpl.Turn(context.Background(), &backend.Run{ID: "run-1"}, backend.Input{
		Messages: []backend.Message{{Role: backend.RoleUser, Content: "Read README.md."}},
		Tools:    []backend.Tool{gatedTool}, Limits: limits,
	}, sink)
	if err != nil || paused.Parked == nil {
		t.Fatalf("Responses tool turn did not park: outcome=%+v err=%v", paused, err)
	}
	if !strings.Contains(string(paused.Parked.State), "opaque-signature") || strings.Contains(string(paused.Parked.State), "private reasoning summary") {
		t.Fatalf("approval state must retain only opaque continuation metadata: %s", paused.Parked.State)
	}
	if !strings.Contains(string(paused.Parked.State), "fc_1") || !strings.Contains(string(paused.Parked.State), "call_1") {
		t.Fatalf("approval state lost the original function call identity: %s", paused.Parked.State)
	}

	approvedTool := backend.Tool{Name: "read_file", ExecRich: func(_ context.Context, args string) (backend.Observation, error) {
		if args != `{"path":"README.md"}` {
			t.Errorf("approved tool args = %q", args)
		}
		return backend.Observation{Text: "README contents"}, nil
	}}
	resumed, err := backendImpl.Continue(context.Background(), &backend.Run{ID: "run-1"}, backend.Answer{
		State: paused.Parked.State, Decided: true, Approved: true,
		Tools: []backend.Tool{approvedTool}, Limits: limits,
	}, sink)
	if err != nil {
		t.Fatalf("resume approved tool call: %v", err)
	}
	if resumed.Status != backend.StatusCompleted {
		t.Fatalf("resumed outcome = %+v, want completed", resumed)
	}
	firstRequest, secondRequest := <-requests, <-requests
	if firstRequest["stream"] != true || secondRequest["stream"] != true {
		t.Fatalf("Responses requests should be streamed: first=%v second=%v", firstRequest["stream"], secondRequest["stream"])
	}
	input, err := json.Marshal(secondRequest["input"])
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"opaque-signature", "rs_1", "fc_1", "function_call_output", "call_1", "README contents"} {
		if !strings.Contains(string(input), expected) {
			t.Errorf("resumed Responses request omitted %q: %s", expected, input)
		}
	}
	if strings.Contains(string(input), "private reasoning summary") {
		t.Fatalf("resumed request exposed plaintext reasoning: %s", input)
	}
}

func TestResponsesToolContinuationSurvivesToolExecution(t *testing.T) {
	requests := make(chan map[string]any, 2)
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		calls++
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Errorf("decode Responses request: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		requests <- body
		w.Header().Set("Content-Type", "text/event-stream")
		if calls == 1 {
			writeResponsesFixtureToolCall(w)
			return
		}
		writeResponsesFixtureEvent(w, `{"type":"response.output_text.delta","item_id":"msg_final","output_index":0,"content_index":0,"delta":"Done"}`)
		writeResponsesFixtureEvent(w, `{"type":"response.completed","response":{"id":"resp_after_tool","status":"completed","output":[],"usage":{"input_tokens":18,"output_tokens":3,"total_tokens":21}}}`)
	}))
	defer server.Close()

	model, err := llm.NewResponsesModel(context.Background(), llm.Profile{
		Provider: llm.ProviderOpenAICompatible, BaseURL: server.URL + "/v1", Model: "gpt-6.1-sol", APIKey: "test-key",
	})
	if err != nil {
		t.Fatal(err)
	}
	turn := New(engine.New(), model, "gpt-6.1-sol", nil)
	tool := backend.Tool{Name: "read_file", ExecRich: func(_ context.Context, args string) (backend.Observation, error) {
		if args != `{"path":"README.md"}` {
			t.Errorf("tool args = %q", args)
		}
		return backend.Observation{Text: "README contents"}, nil
	}}
	out, err := turn.Turn(context.Background(), &backend.Run{ID: "run-tool-continuation"}, backend.Input{
		Messages: []backend.Message{{Role: backend.RoleUser, Content: "Read README.md."}},
		Tools:    []backend.Tool{tool}, Limits: backend.Limits{MaxToolTurns: 4},
	}, fixtureEventSink{})
	if err != nil || out.Status != backend.StatusCompleted || out.Final != "Done" {
		t.Fatalf("tool-enabled Responses turn = outcome %+v, err %v", out, err)
	}
	firstRequest, secondRequest := <-requests, <-requests
	if firstRequest["stream"] != true || secondRequest["stream"] != true {
		t.Fatalf("Responses requests should be streamed: first=%v second=%v", firstRequest["stream"], secondRequest["stream"])
	}
	input, err := json.Marshal(secondRequest["input"])
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"opaque-signature", "rs_1", "fc_1", "function_call_output", "call_1", "README contents"} {
		if !strings.Contains(string(input), expected) {
			t.Errorf("second Responses request omitted %q: %s", expected, input)
		}
	}
	if strings.Contains(string(input), "private reasoning summary") {
		t.Fatalf("second request exposed plaintext reasoning: %s", input)
	}
}

func TestResponsesContinuationFromDifferentCredentialIsNotSent(t *testing.T) {
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"resp_1","object":"response","status":"completed","model":"gpt-6.1-sol","output":[]}`)
	}))
	defer server.Close()
	model, err := llm.NewResponsesModel(context.Background(), llm.Profile{
		Provider: llm.ProviderOpenAICompatible, BaseURL: server.URL + "/v1", Model: "gpt-6.1-sol", APIKey: "credential-b",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = model.Generate(context.Background(), []*schema.Message{{Role: schema.Assistant, ToolCalls: []schema.ToolCall{{
		ID: "call_1", Type: "function", Function: schema.FunctionCall{Name: "read_file", Arguments: `{}`},
		Extra: map[string]any{llm.ResponsesToolOriginExtraKey: "sha256:credential-a"},
	}}}})
	if err == nil || !strings.Contains(err.Error(), "different model credential") {
		t.Fatalf("foreign continuation error = %v, want credential mismatch", err)
	}
	if requests != 0 {
		t.Fatalf("credential B received %d request(s) containing credential A continuation", requests)
	}
}

func TestFreshResponsesTurnTreatsEarlierCredentialCallsAsEvidence(t *testing.T) {
	requests := make(chan map[string]any, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Errorf("decode Responses request: %v", err)
		}
		requests <- body
		w.Header().Set("Content-Type", "text/event-stream")
		writeResponsesFixtureEvent(w, `{"type":"response.completed","response":{"id":"resp_new_turn","status":"completed","output":[],"usage":{"input_tokens":8,"output_tokens":2,"total_tokens":10}}}`)
	}))
	defer server.Close()
	model, err := llm.NewResponsesModel(context.Background(), llm.Profile{
		Provider: llm.ProviderOpenAICompatible, BaseURL: server.URL + "/v1", Model: "gpt-6.1-sol", APIKey: "credential-b",
	})
	if err != nil {
		t.Fatal(err)
	}
	call := schema.ToolCall{
		ID: "call_from_a", Type: "function",
		Function: schema.FunctionCall{Name: "read_file", Arguments: `{"path":"README.md"}`},
		Extra: map[string]any{
			llm.ResponsesToolItemIDExtraKey:    "fc_from_a",
			llm.ResponsesToolReasoningExtraKey: `[{"id":"rs_a","encrypted_content":"opaque-from-a"}]`,
			llm.ResponsesToolOriginExtraKey:    "sha256:credential-a",
		},
	}
	turn := New(engine.New(), model, "gpt-6.1-sol", nil)
	out, err := turn.Turn(context.Background(), &backend.Run{ID: "run-new"}, backend.Input{
		Messages: []backend.Message{
			{Role: backend.RoleAssistant, ToolCalls: []schema.ToolCall{call}},
			{Role: backend.RoleTool, Name: "read_file", ToolCallID: call.ID, Content: "README contents"},
			{Role: backend.RoleUser, Content: "Summarize the README."},
		},
		Limits: backend.Limits{MaxToolTurns: 1},
	}, fixtureEventSink{})
	if err != nil || out.Status != backend.StatusCompleted {
		t.Fatalf("fresh turn using credential B = outcome %+v, err %v", out, err)
	}
	body := <-requests
	input, err := json.Marshal(body["input"])
	if err != nil {
		t.Fatal(err)
	}
	for _, foreign := range []string{"opaque-from-a", "fc_from_a", "credential-a"} {
		if strings.Contains(string(input), foreign) {
			t.Errorf("fresh credential B request contains credential A continuation %q: %s", foreign, input)
		}
	}
	for _, expected := range []string{"call_from_a", "read_file", "README.md", "README contents", "Summarize the README."} {
		if !strings.Contains(string(input), expected) {
			t.Errorf("fresh request lost semantic history %q: %s", expected, input)
		}
	}
}

func TestApprovalResumeRejectsChangedResponsesCredential(t *testing.T) {
	requests := make(chan map[string]any, 2)
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		calls++
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Errorf("decode Responses request: %v", err)
		}
		requests <- body
		w.Header().Set("Content-Type", "text/event-stream")
		writeResponsesFixtureToolCall(w)
	}))
	defer server.Close()
	profile := llm.Profile{
		Provider: llm.ProviderOpenAICompatible, BaseURL: server.URL + "/v1", Model: "gpt-6.1-sol", APIKey: "credential-a",
		ResponsesOriginKey: []byte("durable-provider-signing-key"),
	}
	modelA, err := llm.NewResponsesModel(context.Background(), profile)
	if err != nil {
		t.Fatal(err)
	}
	limits := backend.Limits{MaxToolTurns: 4}
	gatedTool := backend.Tool{Name: "read_file", ExecRich: func(context.Context, string) (backend.Observation, error) {
		return backend.Observation{}, &backend.GateError{Tool: "read_file", Args: `{"path":"README.md"}`, RequestID: "approval-1"}
	}}
	paused, err := New(engine.New(), modelA, "gpt-6.1-sol", nil).Turn(context.Background(), &backend.Run{ID: "run-1"}, backend.Input{
		Messages: []backend.Message{{Role: backend.RoleUser, Content: "Read README.md."}},
		Tools:    []backend.Tool{gatedTool}, Limits: limits,
	}, fixtureEventSink{})
	if err != nil || paused.Parked == nil {
		t.Fatalf("setup approval checkpoint: outcome=%+v err=%v", paused, err)
	}
	approvedExecutions := 0
	approvedTool := backend.Tool{Name: "read_file", ExecRich: func(context.Context, string) (backend.Observation, error) {
		approvedExecutions++
		return backend.Observation{Text: "README contents"}, nil
	}}
	for _, test := range []struct {
		name   string
		change func(*llm.Profile)
	}{
		{name: "API key rotation under the same credential", change: func(p *llm.Profile) { p.APIKey = "credential-b" }},
		{name: "endpoint changed", change: func(p *llm.Profile) { p.BaseURL += "/other-account" }},
		{name: "model changed", change: func(p *llm.Profile) { p.Model = "gpt-6-luna" }},
		{name: "provider signing key changed", change: func(p *llm.Profile) { p.ResponsesOriginKey = []byte("new-provider-signing-key") }},
	} {
		t.Run(test.name, func(t *testing.T) {
			changed := profile
			test.change(&changed)
			modelB, err := llm.NewResponsesModel(t.Context(), changed)
			if err != nil {
				t.Fatal(err)
			}
			_, err = New(engine.New(), modelB, changed.Model, nil).Continue(t.Context(), &backend.Run{ID: "run-1"}, backend.Answer{
				State: paused.Parked.State, Decided: true, Approved: true,
				Tools: []backend.Tool{approvedTool}, Limits: limits,
			}, fixtureEventSink{})
			if err == nil || !strings.Contains(err.Error(), "different model credential") {
				t.Fatalf("resume with changed credential error = %v, want credential mismatch", err)
			}
			if approvedExecutions != 0 {
				t.Fatalf("approved tool executed %d time(s) before rejecting the changed credential", approvedExecutions)
			}
		})
	}
	if calls != 1 {
		t.Fatalf("credential B received %d Responses request(s); only credential A's initial call may be sent", calls)
	}
	if approvedExecutions != 0 {
		t.Fatalf("approved tool executed %d time(s) before rejecting the changed credential", approvedExecutions)
	}
}

func TestApprovalResumeUsesMatchingResponsesFallbackMember(t *testing.T) {
	requests := make(chan string, 4)
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		calls++
		requests <- request.Header.Get("Authorization")
		w.Header().Set("Content-Type", "text/event-stream")
		if calls == 1 {
			writeResponsesFixtureToolCall(w)
			return
		}
		writeResponsesFixtureEvent(w, `{"type":"response.output_text.delta","item_id":"msg_final","output_index":0,"content_index":0,"delta":"Done"}`)
		writeResponsesFixtureEvent(w, `{"type":"response.completed","response":{"id":"resp_after_tool","status":"completed","output":[],"usage":{"input_tokens":18,"output_tokens":3,"total_tokens":21}}}`)
	}))
	defer server.Close()
	profile := llm.Profile{Provider: llm.ProviderOpenAICompatible, BaseURL: server.URL + "/v1", Model: "gpt-6.1-sol"}
	profile.APIKey = "credential-a"
	modelA, err := llm.NewResponsesModel(context.Background(), profile)
	if err != nil {
		t.Fatal(err)
	}
	chatPrimary := &successfulChatFallbackModel{}
	limits := backend.Limits{MaxToolTurns: 4}
	gatedTool := backend.Tool{Name: "read_file", ExecRich: func(context.Context, string) (backend.Observation, error) {
		return backend.Observation{}, &backend.GateError{Tool: "read_file", Args: `{"path":"README.md"}`, RequestID: "approval-1"}
	}}
	paused, err := New(engine.New(), modelA, "gpt-6.1-sol", nil).Turn(context.Background(), &backend.Run{ID: "run-fallback"}, backend.Input{
		Messages: []backend.Message{{Role: backend.RoleUser, Content: "Read README.md."}},
		Tools:    []backend.Tool{gatedTool}, Limits: limits,
	}, fixtureEventSink{})
	if err != nil || paused.Parked == nil {
		t.Fatalf("setup approval checkpoint: outcome=%+v err=%v", paused, err)
	}
	toolExecutions := 0
	approvedTool := backend.Tool{Name: "read_file", ExecRich: func(context.Context, string) (backend.Observation, error) {
		toolExecutions++
		return backend.Observation{Text: "README contents"}, nil
	}}
	fallback := llm.NewFallbackModel([]einomodel.BaseChatModel{chatPrimary, modelA}, []string{"chat-primary", "credential-a"})
	resumed, err := New(engine.New(), fallback, "gpt-6.1-sol", nil).Continue(context.Background(), &backend.Run{ID: "run-fallback"}, backend.Answer{
		State: paused.Parked.State, Decided: true, Approved: true,
		Tools: []backend.Tool{approvedTool}, Limits: limits,
	}, fixtureEventSink{})
	if err != nil || resumed.Status != backend.StatusCompleted {
		t.Fatalf("resume through matching fallback member: outcome=%+v err=%v", resumed, err)
	}
	if toolExecutions != 1 {
		t.Fatalf("approved tool executions = %d, want 1", toolExecutions)
	}
	if calls != 2 {
		t.Fatalf("Responses HTTP requests = %d, want initial plus matching fallback only", calls)
	}
	if chatPrimary.streamCalls != 0 {
		t.Fatalf("Chat Completions primary received %d continuation request(s), want none", chatPrimary.streamCalls)
	}
	if first, second := <-requests, <-requests; first != "Bearer credential-a" || second != "Bearer credential-a" {
		t.Fatalf("fallback sent continuation to credentials %q and %q; want credential A for both", first, second)
	}
}

type successfulChatFallbackModel struct {
	streamCalls int
}

func (*successfulChatFallbackModel) GetType() string          { return "successful-chat-fallback" }
func (*successfulChatFallbackModel) IsCallbacksEnabled() bool { return true }

func (*successfulChatFallbackModel) Generate(context.Context, []*schema.Message, ...einomodel.Option) (*schema.Message, error) {
	return &schema.Message{Role: schema.Assistant, Content: "wrong Chat Completions response"}, nil
}

func (m *successfulChatFallbackModel) Stream(context.Context, []*schema.Message, ...einomodel.Option) (*schema.StreamReader[*schema.Message], error) {
	m.streamCalls++
	return schema.StreamReaderFromArray([]*schema.Message{{Role: schema.Assistant, Content: "wrong Chat Completions response"}}), nil
}

func (m *successfulChatFallbackModel) WithTools([]*schema.ToolInfo) (einomodel.ToolCallingChatModel, error) {
	return m, nil
}

func writeResponsesFixtureEvent(w io.Writer, payload string) {
	_, _ = fmt.Fprintf(w, "data: %s\n\n", payload)
}

func writeResponsesFixtureToolCall(w io.Writer) {
	writeResponsesFixtureEvent(w, `{"type":"response.created","response":{"id":"resp_tools","status":"in_progress","output":[]}}`)
	writeResponsesFixtureEvent(w, `{"type":"response.output_item.added","output_index":0,"item":{"type":"reasoning","id":"rs_1","summary":[]}}`)
	writeResponsesFixtureEvent(w, `{"type":"response.reasoning_summary_text.delta","item_id":"rs_1","output_index":0,"summary_index":0,"delta":"private reasoning summary"}`)
	writeResponsesFixtureEvent(w, `{"type":"response.output_item.done","output_index":0,"item":{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"private reasoning summary"}],"encrypted_content":"opaque-signature"}}`)
	writeResponsesFixtureEvent(w, `{"type":"response.output_item.added","output_index":1,"item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"read_file","arguments":"","status":"in_progress"}}`)
	writeResponsesFixtureEvent(w, `{"type":"response.function_call_arguments.delta","item_id":"fc_1","output_index":1,"delta":"{\"path\":"}`)
	writeResponsesFixtureEvent(w, `{"type":"response.function_call_arguments.delta","item_id":"fc_1","output_index":1,"delta":"\"README.md\"}"}`)
	writeResponsesFixtureEvent(w, `{"type":"response.output_item.done","output_index":1,"item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"read_file","arguments":"{\"path\":\"README.md\"}","status":"completed"}}`)
	writeResponsesFixtureEvent(w, `{"type":"response.completed","response":{"id":"resp_tools","status":"completed","output":[],"usage":{"input_tokens":10,"output_tokens":4,"total_tokens":14}}}`)
}

type fixtureEventSink struct{}

func (fixtureEventSink) Delta(string)                       {}
func (fixtureEventSink) Assistant(backend.AssistantMessage) {}
func (fixtureEventSink) ToolStart(string, string, string)   {}
func (fixtureEventSink) ToolEnd(backend.ToolEvent)          {}
func (fixtureEventSink) Checkpoint(json.RawMessage)         {}
func (fixtureEventSink) Aborted(context.Context) error      { return nil }

var _ backend.EventSink = fixtureEventSink{}
