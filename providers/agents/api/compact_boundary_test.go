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
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"
	"github.com/railgrid/provider-agents/engine"
	"github.com/railgrid/provider-agents/store"
)

func TestContextCompactorResolvesCurrentRunToolGroupBeforeFolding(t *testing.T) {
	f := newCompactFixture(t, 4, 120, "gpt-4o")
	const (
		runID       = "run-current-wire"
		task        = "preserve this current durable request"
		callID      = "call-current-wire"
		assistantID = "assistant-current-wire"
		toolID      = "tool-current-wire"
	)
	run, history := appendAndAssembleCurrentTask(t, f, runID, task)

	call := schema.ToolCall{ID: callID, Type: "function", Function: schema.FunctionCall{Name: "read_order", Arguments: `{"orderID":"order-current"}`}}
	assistantAt := time.Now().UTC().Add(time.Second)
	appendCompactMessage(t, f, assistantCallRow(assistantID, runID, callID, call.Function.Name, call.Function.Arguments, assistantAt))
	resultText := strings.Repeat("order detail ", 6000)
	appendCompactMessage(t, f, toolResultRow(toolID, runID, callID, call.Function.Name, resultText, assistantAt.Add(time.Second)))

	// These are the live Eino messages emitted within the current run. The API
	// callback durably wrote the matching rows, but the engine wire objects have
	// not been annotated with their store identities.
	history = append(history,
		engine.Message{Role: engine.RoleAssistant, ToolCalls: []schema.ToolCall{call}},
		engine.Message{Role: engine.RoleTool, Name: call.Function.Name, ToolCallID: callID, Content: resultText},
	)
	result, err := runContextCompactor(t, f, run, history, 5000)
	if err != nil {
		t.Fatalf("contextCompactor() error = %v", err)
	}
	if engineHistoryTokens(result) > 5000 {
		t.Fatalf("replacement history uses %d tokens, want at most 5000", engineHistoryTokens(result))
	}
	if err := validateStoredHistoryToolPairing(result); err != nil {
		t.Fatalf("replacement history has invalid tool pairing: %v", err)
	}
	if containsHistoryID(result, assistantID) || containsHistoryID(result, toolID) {
		t.Fatalf("large current-run tool group should be folded into a summary: %#v", result)
	}
	if countHistoryMessage(result, "task-"+runID, task) != 1 {
		t.Fatalf("current durable request appears %d times after compaction, want once", countHistoryMessage(result, "task-"+runID, task))
	}

	loaded := loadReplayHistory(t, f)
	assertUniqueHistoryIDs(t, loaded)
	if containsHistoryID(loaded, assistantID) || containsHistoryID(loaded, toolID) {
		t.Fatalf("reloaded history replayed source rows already folded into the summary: %#v", loaded)
	}
	if countHistoryMessage(loaded, "task-"+runID, task) != 1 {
		t.Fatalf("reloaded current durable request appears %d times, want once", countHistoryMessage(loaded, "task-"+runID, task))
	}
}

func TestContextCompactorRejectsUnseenConcurrentRowInsideCheckpointBoundary(t *testing.T) {
	ctx := context.Background()
	f := newCompactFixture(t, 2, 80, "gpt-4o")
	run := f.run()
	run.RunID = "run-boundary"
	run.Task = "the current durable task"
	// This history is assembled before a concurrent run appends its row, as in
	// executeTask's load-then-append sequence.
	history, err := f.s.assembleTurnCtx(ctx, run, "chat", "", false)
	if err != nil {
		t.Fatal(err)
	}
	appendCompactMessage(t, f, store.Message{
		ID: "unseen-concurrent-row", RunID: "other-run", Role: "user",
		Content: "concurrent request between the loaded prefix and current task", CreatedAt: time.Now().UTC(),
	})
	taskRow := store.Message{
		ID: "task-run-boundary", RunID: run.RunID, Role: "user", Content: run.Task,
		CreatedAt: time.Now().UTC().Add(time.Second),
	}
	appendCompactMessage(t, f, taskRow)
	rows, err := f.s.loadAllSessionMessages(ctx, f.scope, "chat")
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.ID == taskRow.ID {
			taskRow = row
			break
		}
	}
	if len(history) == 0 || history[len(history)-1].Role != engine.RoleUser || history[len(history)-1].Content != run.Task {
		t.Fatalf("assembled history does not end with current task: %#v", history)
	}
	history[len(history)-1].ID = taskRow.ID
	history[len(history)-1].Sequence = taskRow.Sequence

	call := schema.ToolCall{ID: "call-boundary", Type: "function", Function: schema.FunctionCall{Name: "read_order", Arguments: `{}`}}
	assistantAt := taskRow.CreatedAt.Add(time.Second)
	appendCompactMessage(t, f, assistantCallRow("assistant-boundary", run.RunID, call.ID, call.Function.Name, call.Function.Arguments, assistantAt))
	appendCompactMessage(t, f, toolResultRow("tool-boundary", run.RunID, call.ID, call.Function.Name, strings.Repeat("large result ", 6000), assistantAt.Add(time.Second)))
	history = append(history,
		engine.Message{Role: engine.RoleAssistant, ToolCalls: []schema.ToolCall{call}},
		engine.Message{Role: engine.RoleTool, Name: call.Function.Name, ToolCallID: call.ID, Content: strings.Repeat("large result ", 6000)},
	)

	_, err = runContextCompactor(t, f, run, history, 5000)
	if !errors.Is(err, store.ErrSessionCheckpointStale) {
		t.Fatalf("contextCompactor() error = %v, want stale checkpoint boundary", err)
	}
	if _, ok, err := f.s.store.GetSessionSummary(ctx, f.scope, "chat"); err != nil || ok {
		t.Fatalf("stale compaction wrote a checkpoint: exists=%v err=%v", ok, err)
	}
}

func countHistoryMessage(history []engine.Message, id, content string) int {
	count := 0
	for _, message := range history {
		if message.ID == id && message.Content == content {
			count++
		}
	}
	return count
}
