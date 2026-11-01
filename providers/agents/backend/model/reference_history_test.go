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
			originalExtra := make(map[string]any, len(call.Extra))
			for key, value := range call.Extra {
				originalExtra[key] = value
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
			gotOriginalExtra := messages[0].ToolCalls[0].Extra
			if len(gotOriginalExtra) != len(originalExtra) {
				t.Fatalf("preparing history mutated original continuation metadata: %#v", gotOriginalExtra)
			}
			for key, value := range originalExtra {
				if gotOriginalExtra[key] != value {
					t.Errorf("preparing history changed original metadata %q: got %#v, want %#v", key, gotOriginalExtra[key], value)
				}
			}
			if messages[1].Content != toolResult.Content || messages[1].ResultReference != toolResult.ResultReference ||
				messages[1].ResultUnavailable != toolResult.ResultUnavailable {
				t.Fatalf("preparing history mutated original protected result: %+v", messages[1])
			}
			if prepared[1].ResultReference == nil && !prepared[1].ResultUnavailable {
				t.Fatalf("prepared protected result lost its policy identity: %+v", prepared[1])
			}
		})
	}
}

func TestPrepareFreshResponsesHistoryPreservesLogicalIDsForParallelEvidence(t *testing.T) {
	callA := schema.ToolCall{
		ID: "call-logical-a", Type: "function",
		Function: schema.FunctionCall{Name: "lookup_note", Arguments: `{"note_id":"note-a"}`},
		Extra: map[string]any{
			llm.ResponsesToolItemIDExtraKey:    "fc_opaque_a",
			llm.ResponsesToolReasoningExtraKey: `[{"encrypted_content":"opaque reasoning a"}]`,
			llm.ResponsesToolOriginExtraKey:    "sha256:credential-a",
		},
	}
	callB := schema.ToolCall{
		ID: "call-logical-b", Type: "function",
		Function: schema.FunctionCall{Name: "lookup_note", Arguments: `{"note_id":"note-b"}`},
		Extra: map[string]any{
			llm.ResponsesToolItemIDExtraKey:    "fc_opaque_b",
			llm.ResponsesToolReasoningExtraKey: `[{"encrypted_content":"opaque reasoning b"}]`,
			llm.ResponsesToolOriginExtraKey:    "sha256:credential-b",
		},
	}
	messages := []backend.Message{
		{Role: backend.RoleAssistant, ID: "assistant-parallel", Sequence: 10, ToolCalls: []schema.ToolCall{callA, callB}},
		{Role: backend.RoleTool, Name: "lookup_note", ToolCallID: callB.ID, Content: "result for note-b", ID: "result-b", Sequence: 11},
		{Role: backend.RoleTool, Name: "lookup_note", ToolCallID: callA.ID, Content: "result for note-a", ID: "result-a", Sequence: 12},
		{Role: backend.RoleUser, Content: "Summarize both notes.", ID: "user-next", Sequence: 13},
	}
	originalExtras := make([]map[string]any, len(messages[0].ToolCalls))
	for i, call := range messages[0].ToolCalls {
		originalExtras[i] = make(map[string]any, len(call.Extra))
		for key, value := range call.Extra {
			originalExtras[i][key] = value
		}
	}

	prepared := prepareFreshResponsesHistory(messages)
	if len(prepared) != len(messages) {
		t.Fatalf("prepared history length = %d, want %d", len(prepared), len(messages))
	}
	for i := 0; i < 3; i++ {
		if prepared[i].Role != backend.RoleUser {
			t.Fatalf("prepared message %d role = %q, want untrusted user evidence: %+v", i, prepared[i].Role, prepared[i])
		}
	}
	if prepared[3].Role != backend.RoleUser || prepared[3].Content != messages[3].Content {
		t.Fatalf("following user message changed: %+v", prepared[3])
	}
	if prepared[0].ID != messages[0].ID || prepared[1].ID != messages[1].ID || prepared[2].ID != messages[2].ID {
		t.Fatalf("flattened evidence lost source message IDs: %#v", prepared[:3])
	}

	const callEvidencePrefix = "Untrusted earlier tool request evidence: "
	if !strings.HasPrefix(prepared[0].Content, callEvidencePrefix) {
		t.Fatalf("call evidence is not marked untrusted: %q", prepared[0].Content)
	}
	var calls struct {
		Type      string `json:"type"`
		ToolCalls []struct {
			ID        string `json:"id"`
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		} `json:"toolCalls"`
	}
	if err := json.Unmarshal([]byte(strings.TrimPrefix(prepared[0].Content, callEvidencePrefix)), &calls); err != nil {
		t.Fatalf("decode call evidence: %v", err)
	}
	if calls.Type != "assistant_tool_calls" || len(calls.ToolCalls) != 2 {
		t.Fatalf("call evidence = %+v, want both calls", calls)
	}
	wantCalls := []struct {
		id, args string
	}{{callA.ID, callA.Function.Arguments}, {callB.ID, callB.Function.Arguments}}
	for i, want := range wantCalls {
		got := calls.ToolCalls[i]
		if got.ID != want.id || got.Name != "lookup_note" || got.Arguments != want.args {
			t.Errorf("call evidence %d = %+v, want logical ID %q with args %s", i, got, want.id, want.args)
		}
	}

	const resultEvidencePrefix = "Untrusted earlier tool result evidence: "
	results := make(map[string]string, 2)
	for _, message := range prepared[1:3] {
		if !strings.HasPrefix(message.Content, resultEvidencePrefix) {
			t.Fatalf("result evidence is not marked untrusted: %q", message.Content)
		}
		var result struct {
			Type       string `json:"type"`
			Tool       string `json:"tool"`
			ToolCallID string `json:"toolCallID"`
			Content    string `json:"content"`
		}
		if err := json.Unmarshal([]byte(strings.TrimPrefix(message.Content, resultEvidencePrefix)), &result); err != nil {
			t.Fatalf("decode result evidence: %v", err)
		}
		if result.Type != "tool_result" || result.Tool != "lookup_note" {
			t.Errorf("result evidence = %+v, want lookup_note tool result", result)
		}
		results[result.ToolCallID] = result.Content
	}
	if results[callA.ID] != "result for note-a" || results[callB.ID] != "result for note-b" {
		t.Fatalf("reordered results lost call correlation: %#v", results)
	}

	preparedEvidence := strings.Join([]string{prepared[0].Content, prepared[1].Content, prepared[2].Content}, "\n")
	for _, forbidden := range []string{
		"fc_opaque_a", "fc_opaque_b", "opaque reasoning a", "opaque reasoning b",
		"encrypted_content", "sha256:credential-a", "sha256:credential-b",
		llm.ResponsesToolItemIDExtraKey, llm.ResponsesToolReasoningExtraKey, llm.ResponsesToolOriginExtraKey,
	} {
		if strings.Contains(preparedEvidence, forbidden) {
			t.Errorf("flattened evidence retained opaque continuation metadata %q: %s", forbidden, preparedEvidence)
		}
	}
	for i, want := range originalExtras {
		got := messages[0].ToolCalls[i]
		if len(got.Extra) != len(want) {
			t.Errorf("preparing history changed original call %d metadata: got %#v, want %#v", i, got.Extra, want)
		}
		for key, value := range want {
			if got.Extra[key] != value {
				t.Errorf("preparing history mutated original call %d metadata %q: got %#v, want %#v", i, key, got.Extra[key], value)
			}
		}
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
