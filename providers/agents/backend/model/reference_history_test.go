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
	"errors"
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

const (
	referenceHistoryToolName = "read_saved_artifact"
	referenceHistoryCallID   = "call_from_credential_a"
	referenceHistoryArgs     = `{"artifact_id":"artifact-a"}`
	referenceHistoryBody     = "authorized artifact contents: " + "line of protected text "
)

func TestFreshResponsesTurnReopensReferenceHistoryAfterCompaction(t *testing.T) {
	const oldHistory = "old unrelated conversation "
	resultText := strings.Repeat(referenceHistoryBody, 60)
	messages, ref := referenceHistoryMessages(t, strings.Repeat(oldHistory, 400))

	requests := make(chan map[string]any, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Errorf("decode Responses request: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		requests <- body
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_reference\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":80,\"output_tokens\":2,\"total_tokens\":82}}}\n\n")
	}))
	defer server.Close()

	model, err := llm.NewResponsesModel(context.Background(), llm.Profile{
		Provider: llm.ProviderOpenAICompatible, BaseURL: server.URL + "/v1", Model: "gpt-6.1-sol", APIKey: "credential-b",
	})
	if err != nil {
		t.Fatal(err)
	}
	reads := 0
	tool := referenceHistoryTool(ref, &reads, resultText)
	var compactions int
	sink := &referenceHistoryEventSink{}
	compactor := engine.ContextCompactionFunc(func(_ context.Context, history []backend.Message, estimate engine.ContextEstimate) ([]backend.Message, error) {
		compactions++
		if estimate.TotalTokens <= estimate.BudgetTokens {
			return nil, fmt.Errorf("compactor received an under-budget estimate: %+v", estimate)
		}
		foundProtected := false
		replacement := make([]backend.Message, 0, len(history))
		for _, message := range history {
			if message.ID == "old-user" {
				continue
			}
			if message.Role == backend.RoleTool && message.ToolCallID == referenceHistoryCallID {
				foundProtected = message.Content == backend.ToolResultReferencePlaceholder &&
					message.ResultReference != nil && message.ResultReference.Equal(ref) &&
					!strings.Contains(message.Content, resultText)
			}
			replacement = append(replacement, message)
		}
		if !foundProtected {
			return nil, errors.New("compactor did not receive a redacted reference-only tool result")
		}
		return replacement, nil
	})
	out, err := New(engine.New(), model, "gpt-6.1-sol", compactor).Turn(context.Background(), &backend.Run{ID: "run-reference-history"}, backend.Input{
		Messages: messages,
		Tools:    []backend.Tool{tool},
		Limits: backend.Limits{
			MaxToolTurns:        1,
			ContextBudgetTokens: 1000,
		},
	}, sink)
	if err != nil || out.Status != backend.StatusCompleted {
		t.Fatalf("fresh Responses turn after credential switch: outcome=%+v err=%v", out, err)
	}
	if compactions != 1 {
		t.Fatalf("compactions = %d, want one", compactions)
	}
	if reads != 2 {
		t.Fatalf("reference reopen count = %d, want initial authorization plus post-compaction authorization", reads)
	}
	if len(sink.checkpoints) != 1 {
		t.Fatalf("checkpoints = %d, want the compacted safe snapshot", len(sink.checkpoints))
	}
	state := string(sink.checkpoints[0])
	if strings.Contains(state, resultText) || strings.Contains(state, oldHistory) {
		t.Fatalf("checkpoint leaked protected result or removed history: %s", state)
	}
	var checkpoint engine.Checkpoint
	if err := json.Unmarshal(sink.checkpoints[0], &checkpoint); err != nil {
		t.Fatalf("decode checkpoint: %v", err)
	}
	var checkpointTool *engine.CheckpointMessage
	for index := range checkpoint.Messages {
		message := &checkpoint.Messages[index]
		if message.Role == backend.RoleTool && message.ToolCallID == referenceHistoryCallID {
			checkpointTool = message
		}
	}
	if checkpointTool == nil || checkpointTool.Content != backend.ToolResultReferencePlaceholder || checkpointTool.ResultReference == nil || !checkpointTool.ResultReference.Equal(ref) {
		t.Fatalf("checkpoint did not retain only the tool result reference: %+v", checkpointTool)
	}

	request := <-requests
	wire, err := json.Marshal(request["input"])
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"function_call", "function_call_output", referenceHistoryCallID, resultText, "Summarize this artifact."} {
		if !strings.Contains(string(wire), expected) {
			t.Errorf("Responses request omitted %q: %s", expected, wire)
		}
	}
	for _, forbidden := range []string{"fc_from_credential_a", "private reasoning from credential A", "credential-a", oldHistory} {
		if strings.Contains(string(wire), forbidden) {
			t.Errorf("fresh Responses request contained stale or compacted history %q: %s", forbidden, wire)
		}
	}
}

func TestFreshResponsesTurnStopsWhenReferenceReauthorizationFails(t *testing.T) {
	messages, ref := referenceHistoryMessages(t, "")
	requests := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests <- struct{}{}
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	model, err := llm.NewResponsesModel(context.Background(), llm.Profile{
		Provider: llm.ProviderOpenAICompatible, BaseURL: server.URL + "/v1", Model: "gpt-6.1-sol", APIKey: "credential-b",
	})
	if err != nil {
		t.Fatal(err)
	}
	reads, executions := 0, 0
	tool := referenceHistoryTool(ref, &reads, "caller-supplied content must not be trusted")
	tool.ExecRich = func(context.Context, string) (backend.Observation, error) {
		executions++
		return backend.Observation{}, errors.New("original tool must not execute")
	}
	tool.RehydrateResult = func(context.Context, string, backend.ToolResultReference) (backend.Observation, error) {
		reads++
		return backend.Observation{}, backend.ErrToolResultReferenceCallerAuthorizationUnavailable
	}
	_, err = New(engine.New(), model, "gpt-6.1-sol", nil).Turn(context.Background(), &backend.Run{ID: "run-denied-reference"}, backend.Input{
		Messages: messages,
		Tools:    []backend.Tool{tool},
		Limits:   backend.Limits{MaxToolTurns: 1},
	}, fixtureEventSink{})
	if !errors.Is(err, engine.ErrToolResultReferenceUnavailable) || !strings.Contains(err.Error(), "current caller authorization is unavailable") {
		t.Fatalf("authorization failure = %v, want a reference-unavailable error", err)
	}
	if reads != 1 || executions != 0 || len(requests) != 0 {
		t.Fatalf("reader/executor/model request counts = %d/%d/%d, want 1/0/0", reads, executions, len(requests))
	}
}

func TestFreshResponsesTurnKeepsGenericToolHistory(t *testing.T) {
	requests, server := newFreshResponsesFixture(t)
	model := newFreshResponsesModel(t, server.URL, "credential-b")
	callID := "generic-call-1"
	out, err := New(engine.New(), model, "gpt-6.1-sol", nil).Turn(context.Background(), &backend.Run{ID: "run-generic-history"}, backend.Input{
		Messages: []backend.Message{
			{Role: backend.RoleAssistant, ToolCalls: []schema.ToolCall{{
				ID: callID, Type: "function",
				Function: schema.FunctionCall{Name: "lookup_note", Arguments: `{"note_id":"note-a"}`},
			}}},
			{Role: backend.RoleTool, Name: "lookup_note", ToolCallID: callID, Content: "generic historical note"},
			{Role: backend.RoleUser, Content: "Summarize the note."},
		},
		Tools:  []backend.Tool{{Name: "lookup_note", Params: map[string]backend.Param{"note_id": {Type: "string", Required: true}}}},
		Limits: backend.Limits{MaxToolTurns: 1},
	}, fixtureEventSink{})
	if err != nil || out.Status != backend.StatusCompleted {
		t.Fatalf("fresh Responses turn with generic tool history: outcome=%+v err=%v", out, err)
	}
	wire := marshalResponsesInput(t, <-requests)
	for _, expected := range []string{"function_call", "function_call_output", callID, "lookup_note", "generic historical note", "Summarize the note."} {
		if !strings.Contains(wire, expected) {
			t.Errorf("Responses request omitted generic history %q: %s", expected, wire)
		}
	}
}

func TestFreshResponsesTurnKeepsMixedProtectedToolBatchPaired(t *testing.T) {
	requests, server := newFreshResponsesFixture(t)
	model := newFreshResponsesModel(t, server.URL, "credential-b")
	resultText := "authorized saved artifact"
	base, ref := referenceHistoryMessages(t, "")
	firstCall := base[1].ToolCalls[0]
	secondCallID := "call_ordinary_from_a"
	secondCall := schema.ToolCall{
		ID: secondCallID, Type: "function",
		Function: schema.FunctionCall{Name: "lookup_note", Arguments: `{"note_id":"note-a"}`},
		Extra: map[string]any{
			llm.ResponsesToolItemIDExtraKey:    "fc_ordinary_from_a",
			llm.ResponsesToolReasoningExtraKey: "private reasoning from credential A",
			llm.ResponsesToolOriginExtraKey:    "sha256:credential-a",
		},
	}
	messages := []backend.Message{
		{Role: backend.RoleAssistant, ToolCalls: []schema.ToolCall{firstCall, secondCall}},
		base[2],
		{Role: backend.RoleTool, Name: "lookup_note", ToolCallID: secondCallID, Content: "ordinary saved note"},
		{Role: backend.RoleUser, Content: "Summarize both saved items."},
	}
	reads := 0
	tools := []backend.Tool{
		referenceHistoryTool(ref, &reads, resultText),
		{Name: "lookup_note", Params: map[string]backend.Param{"note_id": {Type: "string", Required: true}}},
	}
	out, err := New(engine.New(), model, "gpt-6.1-sol", nil).Turn(context.Background(), &backend.Run{ID: "run-mixed-history"}, backend.Input{
		Messages: messages,
		Tools:    tools,
		Limits:   backend.Limits{MaxToolTurns: 1},
	}, fixtureEventSink{})
	if err != nil || out.Status != backend.StatusCompleted {
		t.Fatalf("fresh Responses turn with mixed protected tool batch: outcome=%+v err=%v", out, err)
	}
	if reads != 1 {
		t.Fatalf("reference reopen count = %d, want one", reads)
	}
	wire := marshalResponsesInput(t, <-requests)
	for _, expected := range []string{firstCall.ID, referenceHistoryToolName, "artifact-a", secondCallID, "lookup_note", "function_call_output", resultText, "ordinary saved note"} {
		if !strings.Contains(wire, expected) {
			t.Errorf("Responses request omitted paired batch content %q: %s", expected, wire)
		}
	}
	for _, forbidden := range []string{"fc_from_credential_a", "fc_ordinary_from_a", "private reasoning from credential A", "credential-a"} {
		if strings.Contains(wire, forbidden) {
			t.Errorf("Responses request retained continuation metadata %q: %s", forbidden, wire)
		}
	}
	firstID := strings.Index(wire, `"call_id":"`+firstCall.ID+`"`)
	secondID := strings.Index(wire, `"call_id":"`+secondCallID+`"`)
	firstResult := strings.LastIndex(wire, `"call_id":"`+firstCall.ID+`"`)
	secondResult := strings.LastIndex(wire, `"call_id":"`+secondCallID+`"`)
	if firstID < 0 || secondID < 0 || firstResult < 0 || secondResult < 0 || (firstID >= secondID || secondID >= firstResult || firstResult >= secondResult) {
		t.Fatalf("mixed tool calls and outputs lost their original pairing/order: %s", wire)
	}
}

func TestPrepareFreshResponsesHistoryPreservesProtectedGroupsOnly(t *testing.T) {
	for _, test := range []struct {
		name string
		set  func(*backend.Message)
	}{
		{
			name: "reference",
			set: func(message *backend.Message) {
				message.ResultReference = &backend.ToolResultReference{ToolName: referenceHistoryToolName, ToolCallID: referenceHistoryCallID}
			},
		},
		{
			name: "unavailable",
			set: func(message *backend.Message) {
				message.ResultUnavailable = true
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			call := schema.ToolCall{
				ID: referenceHistoryCallID, Type: "function",
				Function: schema.FunctionCall{Name: referenceHistoryToolName, Arguments: referenceHistoryArgs},
				Extra: map[string]any{
					llm.ResponsesToolItemIDExtraKey:    "fc_from_credential_a",
					llm.ResponsesToolReasoningExtraKey: "private reasoning from credential A",
					llm.ResponsesToolOriginExtraKey:    "credential-a",
					"application-extra":                "preserved",
				},
			}
			toolResult := backend.Message{
				Role: backend.RoleTool, Name: referenceHistoryToolName, ToolCallID: referenceHistoryCallID,
				Content: backend.ToolResultReferencePlaceholder,
			}
			test.set(&toolResult)
			messages := []backend.Message{
				{Role: backend.RoleAssistant, ToolCalls: []schema.ToolCall{call}},
				toolResult,
				{Role: backend.RoleUser, Content: "Summarize this artifact."},
			}
			prepared := prepareFreshResponsesHistory(messages)
			if len(prepared) != len(messages) || prepared[0].Role != backend.RoleAssistant || prepared[1].Role != backend.RoleTool {
				t.Fatalf("protected tool group was projected into prose: %+v", prepared)
			}
			extra := prepared[0].ToolCalls[0].Extra
			for _, key := range []string{llm.ResponsesToolItemIDExtraKey, llm.ResponsesToolReasoningExtraKey, llm.ResponsesToolOriginExtraKey} {
				if _, ok := extra[key]; ok {
					t.Errorf("prepared call retained credential-specific metadata %q: %#v", key, extra)
				}
			}
			if extra["application-extra"] != "preserved" {
				t.Errorf("prepared call lost unrelated metadata: %#v", extra)
			}
			if _, ok := messages[0].ToolCalls[0].Extra[llm.ResponsesToolOriginExtraKey]; !ok {
				t.Fatal("preparing history mutated the caller's input")
			}
			if prepared[1].ResultReference == nil && !prepared[1].ResultUnavailable {
				t.Fatalf("prepared protected result lost its policy identity: %+v", prepared[1])
			}
		})
	}
}

func referenceHistoryMessages(t *testing.T, oldContent string) ([]backend.Message, backend.ToolResultReference) {
	t.Helper()
	ref, err := engine.BindToolResultReference(
		referenceHistoryToolName, referenceHistoryCallID, referenceHistoryArgs,
		json.RawMessage(`{"artifact":"artifact-a"}`), json.RawMessage(`{"read":"read-1"}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	call := schema.ToolCall{
		ID: referenceHistoryCallID, Type: "function",
		Function: schema.FunctionCall{Name: referenceHistoryToolName, Arguments: referenceHistoryArgs},
		Extra: map[string]any{
			llm.ResponsesToolItemIDExtraKey:    "fc_from_credential_a",
			llm.ResponsesToolReasoningExtraKey: "private reasoning from credential A",
			llm.ResponsesToolOriginExtraKey:    "sha256:credential-a",
		},
	}
	return []backend.Message{
		{Role: backend.RoleUser, Content: oldContent, ID: "old-user"},
		{Role: backend.RoleAssistant, ToolCalls: []schema.ToolCall{call}},
		{Role: backend.RoleTool, Name: referenceHistoryToolName, ToolCallID: referenceHistoryCallID,
			Content: backend.ToolResultReferencePlaceholder, ResultReference: &ref},
		{Role: backend.RoleUser, Content: "Summarize this artifact.", ID: "current-user"},
	}, ref
}

func newFreshResponsesFixture(t *testing.T) (<-chan map[string]any, *httptest.Server) {
	t.Helper()
	requests := make(chan map[string]any, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Errorf("decode Responses request: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		requests <- body
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_reference\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":80,\"output_tokens\":2,\"total_tokens\":82}}}\n\n")
	}))
	t.Cleanup(server.Close)
	return requests, server
}

func newFreshResponsesModel(t *testing.T, baseURL, apiKey string) einomodel.BaseChatModel {
	t.Helper()
	model, err := llm.NewResponsesModel(context.Background(), llm.Profile{
		Provider: llm.ProviderOpenAICompatible, BaseURL: baseURL + "/v1", Model: "gpt-6.1-sol", APIKey: apiKey,
	})
	if err != nil {
		t.Fatal(err)
	}
	return model
}

func marshalResponsesInput(t *testing.T, request map[string]any) string {
	t.Helper()
	wire, err := json.Marshal(request["input"])
	if err != nil {
		t.Fatal(err)
	}
	return string(wire)
}

func referenceHistoryTool(ref backend.ToolResultReference, reads *int, resultText string) backend.Tool {
	return backend.Tool{
		Name:         referenceHistoryToolName,
		Params:       map[string]backend.Param{"artifact_id": {Type: "string", Required: true}},
		ResultPolicy: backend.ToolResultReferenceOnly,
		ExecRich: func(context.Context, string) (backend.Observation, error) {
			return backend.Observation{}, errors.New("a prior result must not execute again")
		},
		RehydrateResult: func(_ context.Context, args string, got backend.ToolResultReference) (backend.Observation, error) {
			*reads = *reads + 1
			if args != referenceHistoryArgs || !got.Equal(ref) {
				return backend.Observation{}, errors.New("reader received a changed result binding")
			}
			return backend.Observation{Text: resultText, ResultReference: ref.Payload, Receipt: ref.Receipt}, nil
		},
	}
}

type referenceHistoryEventSink struct {
	checkpoints []json.RawMessage
}

func (*referenceHistoryEventSink) Delta(string)                       {}
func (*referenceHistoryEventSink) Assistant(backend.AssistantMessage) {}
func (*referenceHistoryEventSink) ToolStart(string, string, string)   {}
func (*referenceHistoryEventSink) ToolEnd(backend.ToolEvent)          {}
func (s *referenceHistoryEventSink) Checkpoint(state json.RawMessage) {
	s.checkpoints = append(s.checkpoints, append(json.RawMessage(nil), state...))
}
func (*referenceHistoryEventSink) Aborted(context.Context) error { return nil }

var _ backend.EventSink = (*referenceHistoryEventSink)(nil)
