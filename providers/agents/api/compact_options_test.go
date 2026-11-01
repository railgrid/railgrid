// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package api

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"
	agentsv1alpha1 "github.com/railgrid/provider-agents/apis/v1alpha1"
	"github.com/railgrid/provider-agents/engine"
	"github.com/railgrid/provider-agents/llm"
)

func TestCompactionMaxTokensOptionPayload(t *testing.T) {
	tests := []struct {
		name            string
		model           string
		wantField       string
		unexpectedField string
	}{
		{
			name:            "GPT-5 uses max completion tokens",
			model:           "gpt-5.6-luna",
			wantField:       "max_completion_tokens",
			unexpectedField: "max_tokens",
		},
		{
			name:            "GPT-4o uses max tokens",
			model:           "gpt-4o",
			wantField:       "max_tokens",
			unexpectedField: "max_completion_tokens",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			f := newCompactFixture(t, 0, 0, tt.model)
			model, err := f.s.buildModelForPurpose(ctx, f.creds, f.agent, llm.PurposeCompaction)
			if err != nil {
				t.Fatalf("build compaction model: %v", err)
			}

			_, err = f.s.summarizeBatch(ctx, f.run(), model, tt.model, []engine.Message{{
				Role: engine.RoleUser, Content: "Summarize this conversation evidence.",
			}})
			if err != nil {
				t.Fatalf("summarize batch: %v", err)
			}

			requests := f.llm.requestSnapshot()
			if len(requests) != 1 {
				t.Fatalf("fake model received %d requests, want 1", len(requests))
			}
			request := requests[0]
			if got := request[tt.wantField]; got != float64(compactOutputTokens) {
				t.Errorf("request[%q] = %v, want %d", tt.wantField, got, compactOutputTokens)
			}
			if got, exists := request[tt.unexpectedField]; exists {
				t.Errorf("request unexpectedly includes %q = %v", tt.unexpectedField, got)
			}
		})
	}
}

func TestCompactionUsageCountsTowardDailyBudget(t *testing.T) {
	ctx := context.Background()
	f := newCompactFixture(t, 0, 0, "gpt-4o")
	f.agent.Spec.Budget = &agentsv1alpha1.AgentBudget{Window: "day", TokenLimit: 1}
	model, err := f.s.buildModelForPurpose(ctx, f.creds, f.agent, llm.PurposeCompaction)
	if err != nil {
		t.Fatalf("build compaction model: %v", err)
	}

	if _, err := f.s.summarizeBatch(ctx, f.run(), model, "gpt-4o", []engine.Message{{
		Role: engine.RoleUser, Content: "Summarize this conversation evidence.",
	}}); err != nil {
		t.Fatalf("summarize batch: %v", err)
	}

	usage, err := f.s.store.GetUsage(ctx, f.scope, f.agent.Name, time.Now().UTC(), budgetWindow(f.agent.Spec.Budget))
	if err != nil {
		t.Fatal(err)
	}
	if usage.InputTokens != 100 || usage.OutputTokens != 20 {
		t.Fatalf("daily usage = %+v, want compaction's 100/20 tokens", usage)
	}
	if err := f.s.checkBudget(ctx, f.scope, f.agent, time.Now().UTC()); !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("daily budget after compaction = %v, want ErrBudgetExceeded", err)
	}
}

func TestSummarizeBatchFlattensResponsesToolHistoryAsSafeEvidence(t *testing.T) {
	ctx := context.Background()
	f := newCompactFixture(t, 0, 0, "gpt-4o")
	model, err := f.s.buildModelForPurpose(ctx, f.creds, f.agent, llm.PurposeCompaction)
	if err != nil {
		t.Fatalf("build compaction model: %v", err)
	}

	messages := []engine.Message{
		{
			Role: engine.RoleAssistant,
			ToolCalls: []schema.ToolCall{
				{
					ID: "logical-call-a", Type: "function",
					Function: schema.FunctionCall{Name: "lookup_note", Arguments: `{"note_id":"note-a","api_key":"SECRET_ARGUMENT_VALUE_A"}`},
					Extra: map[string]any{
						llm.ResponsesToolItemIDExtraKey:    "fc_opaque_chat_a",
						llm.ResponsesToolReasoningExtraKey: `[{"id":"rs_a","encrypted_content":"ENCRYPTED_CHAT_PAYLOAD"}]`,
						llm.ResponsesToolOriginExtraKey:    "sha256:CHAT_CREDENTIAL_ORIGIN",
					},
				},
				{
					ID: "logical-call-b", Type: "function",
					Function: schema.FunctionCall{Name: "lookup_note", Arguments: `{"note_id":"note-b","api_key":"SECRET_ARGUMENT_VALUE_B"}`},
					Extra: map[string]any{
						llm.ResponsesToolItemIDExtraKey:    "fc_opaque_chat_b",
						llm.ResponsesToolReasoningExtraKey: `[{"id":"rs_b","encrypted_content":"ENCRYPTED_CHAT_PAYLOAD_B"}]`,
						llm.ResponsesToolOriginExtraKey:    "sha256:CHAT_CREDENTIAL_ORIGIN_B",
					},
				},
			},
		},
		{Role: engine.RoleTool, Name: "lookup_note", ToolCallID: "logical-call-b", Content: "result for note-b"},
		{Role: engine.RoleTool, Name: "lookup_note", ToolCallID: "logical-call-a", Content: "result for note-a"},
	}
	if _, err := f.s.summarizeBatch(ctx, f.run(), model, "gpt-4o", messages); err != nil {
		t.Fatalf("summarize batch: %v", err)
	}

	requests := f.llm.requestSnapshot()
	if len(requests) != 1 {
		t.Fatalf("fake model received %d requests, want 1", len(requests))
	}
	request := requests[0]
	wire, err := json.Marshal(request)
	if err != nil {
		t.Fatalf("marshal captured compaction request: %v", err)
	}
	var evidence []string
	rawMessages, ok := request["messages"].([]any)
	if !ok {
		t.Fatalf("captured request messages = %T, want []any", request["messages"])
	}
	for _, rawMessage := range rawMessages {
		message, ok := rawMessage.(map[string]any)
		if !ok {
			t.Fatalf("captured message = %T, want map[string]any", rawMessage)
		}
		content, _ := message["content"].(string)
		if !strings.Contains(content, "Untrusted transcript evidence:") {
			continue
		}
		if role, _ := message["role"].(string); role != "user" {
			t.Errorf("tool history evidence role = %q, want user", role)
		}
		evidence = append(evidence, content)
	}
	if len(evidence) != 3 {
		t.Fatalf("captured untrusted tool evidence count = %d, want call plus two results: %q", len(evidence), evidence)
	}

	const evidencePrefix = "Untrusted transcript evidence: "
	var callEvidence []struct {
		ID        string `json:"id"`
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	}
	resultByCallID := make(map[string]string, 2)
	for _, content := range evidence {
		if !strings.HasPrefix(content, evidencePrefix) {
			t.Fatalf("transcript evidence lacks its untrusted marker: %s", content)
		}
		var item struct {
			Type      string `json:"type"`
			ToolCalls []struct {
				ID        string `json:"id"`
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			} `json:"toolCalls"`
			ToolCallID string `json:"toolCallID"`
			Content    string `json:"content"`
		}
		if err := json.Unmarshal([]byte(strings.TrimPrefix(content, evidencePrefix)), &item); err != nil {
			t.Fatalf("decode transcript evidence: %v", err)
		}
		switch item.Type {
		case "assistant_tool_calls":
			if len(callEvidence) != 0 {
				t.Fatalf("captured more than one assistant tool-call group: %+v", callEvidence)
			}
			callEvidence = item.ToolCalls
		case "tool_result":
			resultByCallID[item.ToolCallID] = item.Content
		default:
			t.Errorf("unexpected transcript evidence type %q: %s", item.Type, content)
		}
	}
	callByID := make(map[string]struct {
		Name      string
		Arguments string
	}, len(callEvidence))
	for _, call := range callEvidence {
		callByID[call.ID] = struct {
			Name      string
			Arguments string
		}{Name: call.Name, Arguments: call.Arguments}
	}
	for _, callID := range []string{"logical-call-a", "logical-call-b"} {
		if _, ok := callByID[callID]; !ok {
			t.Errorf("assistant evidence lost logical call ID %q: %+v", callID, callEvidence)
		}
		if resultByCallID[callID] == "" {
			t.Errorf("tool result evidence lost correlation ID %q: %q", callID, evidence)
		}
	}
	if resultByCallID["logical-call-a"] != "result for note-a" || resultByCallID["logical-call-b"] != "result for note-b" {
		t.Errorf("reordered tool results lost their logical call pairing: %#v", resultByCallID)
	}
	for _, expected := range []struct{ callID, noteID string }{
		{callID: "logical-call-a", noteID: "note-a"},
		{callID: "logical-call-b", noteID: "note-b"},
	} {
		call, ok := callByID[expected.callID]
		if !ok {
			continue
		}
		if call.Name != "lookup_note" {
			t.Errorf("call %q name = %q, want lookup_note", expected.callID, call.Name)
		}
		var args map[string]any
		if err := json.Unmarshal([]byte(call.Arguments), &args); err != nil {
			t.Errorf("decode arguments for %q: %v", expected.callID, err)
			continue
		}
		if args["note_id"] != expected.noteID || args["api_key"] != "[redacted]" {
			t.Errorf("arguments for %q = %#v, want note_id %q and redacted api_key", expected.callID, args, expected.noteID)
		}
	}

	for _, forbidden := range []string{
		llm.ResponsesToolItemIDExtraKey, llm.ResponsesToolReasoningExtraKey, llm.ResponsesToolOriginExtraKey,
		"fc_opaque_chat_a", "fc_opaque_chat_b", "ENCRYPTED_CHAT_PAYLOAD", "CHAT_CREDENTIAL_ORIGIN",
		"SECRET_ARGUMENT_VALUE_A", "SECRET_ARGUMENT_VALUE_B",
	} {
		if strings.Contains(string(wire), forbidden) {
			t.Errorf("captured compaction request retained sensitive or provider-specific data %q: %s", forbidden, wire)
		}
	}
}
