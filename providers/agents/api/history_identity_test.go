// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package api

import (
	"strings"
	"testing"

	"github.com/cloudwego/eino/schema"
	"github.com/railgrid/provider-agents/engine"
	"github.com/railgrid/provider-agents/store"
)

func TestEnrichRunHistoryIdentitiesMatchesOnlyPersistedRowsFromCurrentRun(t *testing.T) {
	calls := []schema.ToolCall{
		{ID: "call-a", Type: "function", Function: schema.FunctionCall{Name: "lookup"}},
		{ID: "call-b", Type: "function", Function: schema.FunctionCall{Name: "update"}},
		{ID: "call-unavailable", Type: "function", Function: schema.FunctionCall{Name: "later"}},
	}
	rows := []store.Message{
		{ID: "other-user", Sequence: 3, RunID: "other-run", Role: "user", Content: "do the task"},
		{ID: "other-call", Sequence: 4, RunID: "other-run", Role: "assistant", Content: "checking", Metadata: toolCallsMetadata(calls)},
		identityToolResultRow("other-result", 5, "other-run", "call-a", "wrong result"),
		{ID: "task-row", Sequence: 10, RunID: "run-1", Role: "user", Content: "do the task"},
		{ID: "call-row", Sequence: 11, RunID: "run-1", Role: "assistant", Content: "checking", Metadata: toolCallsMetadata(calls)},
		identityToolResultRow("result-b", 13, "run-1", "call-b", "second result"),
		identityToolResultRow("result-a", 12, "run-1", "call-a", "first result"),
	}
	history := []engine.Message{
		{Role: engine.RoleUser, Content: "do the task"},
		{Role: engine.RoleAssistant, Content: "checking", ToolCalls: calls},
		{Role: engine.RoleTool, ToolCallID: "call-b", Name: "update", Content: "second result"},
		{Role: engine.RoleTool, ToolCallID: "call-a", Name: "lookup", Content: "first result"},
		{Role: engine.RoleTool, ToolCallID: "call-unavailable", Name: "later", Content: unavailableToolResult},
	}

	got, err := enrichRunHistoryIdentities(history, rows, "run-1")
	if err != nil {
		t.Fatalf("enrichRunHistoryIdentities() error = %v", err)
	}
	if got[0].ID != "task-row" || got[0].Sequence != 10 {
		t.Fatalf("task identity = (%q, %d), want current-run source (task-row, 10)", got[0].ID, got[0].Sequence)
	}
	if got[1].ID != "call-row" || got[1].Sequence != 11 {
		t.Fatalf("assistant tool-call identity = (%q, %d), want (call-row, 11)", got[1].ID, got[1].Sequence)
	}
	if got[2].ID != "result-b" || got[2].Sequence != 13 || got[3].ID != "result-a" || got[3].Sequence != 12 {
		t.Fatalf("tool result identities = (%q, %d), (%q, %d), want call-ID-specific rows", got[2].ID, got[2].Sequence, got[3].ID, got[3].Sequence)
	}
	if got[4].ID != "" || got[4].Sequence != 0 {
		t.Fatalf("unavailable synthetic result unexpectedly acquired identity: %#v", got[4])
	}
	if history[0].ID != "" || history[2].ID != "" {
		t.Fatalf("input history was mutated: %#v", history)
	}
}

func TestEnrichRunHistoryIdentitiesValidatesExplicitSourceAndTaskContent(t *testing.T) {
	rows := []store.Message{{ID: "source", Sequence: 20, RunID: "run-1", Role: "user", Content: "original"}}
	for _, tc := range []struct {
		name    string
		history []engine.Message
		want    string
	}{
		{
			name:    "unknown explicit ID",
			history: []engine.Message{{Role: engine.RoleUser, Content: "original", ID: "missing"}},
			want:    "unknown source message ID",
		},
		{
			name:    "sequence mismatch",
			history: []engine.Message{{Role: engine.RoleUser, Content: "original", ID: "source", Sequence: 21}},
			want:    "does not match stored sequence",
		},
		{
			name:    "changed latest task",
			history: []engine.Message{{Role: engine.RoleUser, Content: "edited request"}},
			want:    "does not match the persisted request",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := enrichRunHistoryIdentities(tc.history, rows, "run-1")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("enrichRunHistoryIdentities() error = %v, want substring %q", err, tc.want)
			}
		})
	}

	got, err := enrichRunHistoryIdentities([]engine.Message{{Role: engine.RoleUser, Content: "original", ID: "source"}}, rows, "run-1")
	if err != nil {
		t.Fatalf("valid explicit source identity: %v", err)
	}
	if got[0].Sequence != 20 {
		t.Fatalf("explicit ID did not recover source sequence: got %d, want 20", got[0].Sequence)
	}

	checkpointed := []engine.Message{
		{Role: engine.RoleUser, Content: compactSummaryPrefix + "\n\nEarlier messages", Sequence: 18},
		{Role: engine.RoleUser, Content: "original", ID: "source"},
	}
	got, err = enrichRunHistoryIdentities(checkpointed, rows, "run-1")
	if err != nil {
		t.Fatalf("checkpoint summary and explicit latest task: %v", err)
	}
	if got[0].ID != "" || got[0].Sequence != 18 || got[1].Sequence != 20 {
		t.Fatalf("checkpoint identities = %#v, want synthetic summary boundary and source task", got)
	}
}

func TestEnrichRunHistoryIdentitiesSkipsEphemeralImagesButStillChecksRequestMismatch(t *testing.T) {
	rows := []store.Message{{ID: "source", Sequence: 20, RunID: "run-1", Role: "user", Content: "original request"}}
	imagePlaceholder := engine.Message{
		Role: engine.RoleUser, Content: "[multimodal content from tool calls omitted during context compaction]", Ephemeral: true,
	}
	got, err := enrichRunHistoryIdentities([]engine.Message{
		imagePlaceholder,
		{Role: engine.RoleUser, Content: "original request"},
	}, rows, "run-1")
	if err != nil {
		t.Fatalf("ephemeral image prevented durable request identity resolution: %v", err)
	}
	if got[0].ID != "" || got[1].ID != "source" || got[1].Sequence != 20 {
		t.Fatalf("resolved history = %#v, want an unbound image note and the durable request identity", got)
	}

	_, err = enrichRunHistoryIdentities([]engine.Message{
		imagePlaceholder,
		{Role: engine.RoleUser, Content: "edited request"},
	}, rows, "run-1")
	if err == nil || !strings.Contains(err.Error(), "latest unbound user message does not match the persisted request") {
		t.Fatalf("mismatched durable request error = %v, want the request identity guard", err)
	}
}

func TestEnrichRunHistoryIdentitiesRejectsAmbiguousAndUnpersistedCurrentMessages(t *testing.T) {
	toolCall := schema.ToolCall{ID: "call", Type: "function", Function: schema.FunctionCall{Name: "lookup"}}
	baseAssistant := store.Message{RunID: "run-1", Role: "assistant", Content: "", Metadata: toolCallsMetadata([]schema.ToolCall{toolCall})}
	tests := []struct {
		name    string
		history []engine.Message
		rows    []store.Message
		want    string
	}{
		{
			name:    "ambiguous assistant rows",
			history: []engine.Message{{Role: engine.RoleAssistant, ToolCalls: []schema.ToolCall{toolCall}}},
			rows:    []store.Message{{ID: "one", Sequence: 1, RunID: "run-1", Role: "assistant", Metadata: baseAssistant.Metadata}, {ID: "two", Sequence: 2, RunID: "run-1", Role: "assistant", Metadata: baseAssistant.Metadata}},
			want:    "2 matching assistant tool-call rows",
		},
		{
			name:    "unpersisted tool result",
			history: []engine.Message{{Role: engine.RoleTool, ToolCallID: "call", Content: "actual result"}},
			rows:    []store.Message{{ID: "other", Sequence: 1, RunID: "other-run", Role: "tool", Content: "actual result", Metadata: map[string]any{historyToolCallIDKey: "call"}}},
			want:    "no persisted tool result",
		},
		{
			name:    "tool result changed",
			history: []engine.Message{{Role: engine.RoleTool, ToolCallID: "call", Content: "edited result"}},
			rows:    []store.Message{identityToolResultRow("source", 1, "run-1", "call", "original result")},
			want:    "does not match its persisted tool result",
		},
		{
			name:    "ambiguous tool result rows",
			history: []engine.Message{{Role: engine.RoleTool, ToolCallID: "call", Content: "result"}},
			rows:    []store.Message{identityToolResultRow("one", 1, "run-1", "call", "result"), identityToolResultRow("two", 2, "run-1", "call", "result")},
			want:    "ambiguously matches 2 tool results",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := enrichRunHistoryIdentities(tc.history, tc.rows, "run-1")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("enrichRunHistoryIdentities() error = %v, want substring %q", err, tc.want)
			}
		})
	}
}

func toolCallsMetadata(calls []schema.ToolCall) map[string]any {
	return map[string]any{historyToolCallsKey: storedToolCalls(calls)}
}

func identityToolResultRow(id string, sequence int64, runID, callID, content string) store.Message {
	return store.Message{
		ID: id, Sequence: sequence, RunID: runID, Role: "tool", Content: content,
		Metadata: map[string]any{historyToolCallIDKey: callID},
	}
}
