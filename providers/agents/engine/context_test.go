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
	"errors"
	"strings"
	"testing"

	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

func TestEstimateTokens(t *testing.T) {
	if estimateTokens("") != 0 {
		t.Fatal("the empty string is zero tokens")
	}
	// Four bytes per token, rounding up.
	if got := estimateTokens("abcd"); got != 1 {
		t.Fatalf("estimateTokens(4 bytes) = %d, want 1", got)
	}
	if got := estimateTokens("abcde"); got != 2 {
		t.Fatalf("estimateTokens(5 bytes) = %d, want 2", got)
	}
}

func TestEstimateToolSchemaTokens(t *testing.T) {
	tokens, err := estimateToolSchemaTokens([]Tool{{
		Name: "lookup", Desc: "search the tenant records",
		Params: map[string]Param{"query": {Type: "string", Desc: "search terms", Required: true}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if tokens == 0 {
		t.Fatal("tool name, description, and parameter schema should contribute to context estimate")
	}
}

func TestValidateToolMessagePairing(t *testing.T) {
	paired := []Message{
		{Role: RoleAssistant, ToolCalls: []schema.ToolCall{{ID: "call-1", Function: schema.FunctionCall{Name: "lookup", Arguments: `{}`}}}},
		{Role: RoleTool, ToolCallID: "call-1", Name: "lookup", Content: "done"},
	}
	if err := validateToolMessagePairing(paired); err != nil {
		t.Fatalf("valid tool group rejected: %v", err)
	}
	if err := validateToolMessagePairing(paired[:1]); err == nil {
		t.Fatal("unanswered assistant tool call should be rejected")
	}
	if err := validateToolMessagePairing(paired[1:]); err == nil {
		t.Fatal("tool result without matching call should be rejected")
	}
}

func TestOversizedToolResultsAreRetainedWithoutCompactionPressure(t *testing.T) {
	resultText := strings.Repeat("detail ", 800)
	model := &toolMockModel{}
	_, err := New().StreamTurnWithTools(context.Background(), model,
		[]Message{{Role: RoleUser, Content: "weather in vilnius?"}},
		[]Tool{{Name: "get_weather", Desc: "current weather", Exec: func(context.Context, string) (string, error) {
			return resultText, nil
		}}},
		TurnConfig{MaxIters: 8}, Callbacks{})
	if err != nil {
		t.Fatal(err)
	}
	if model.calls != 2 {
		t.Fatalf("model calls = %d, want tool response followed by final response", model.calls)
	}
	for _, message := range model.gotIn {
		if message.Role == schema.Tool {
			if message.Content != resultText {
				t.Fatalf("tool result changed without configured context pressure: got %d bytes, want %d", len(message.Content), len(resultText))
			}
			return
		}
	}
	t.Fatal("second model request did not contain the tool result")
}

func TestContextCompactorRunsBeforeOverBudgetModelRequest(t *testing.T) {
	resultText := strings.Repeat("large result ", 500)
	tools := []Tool{{Name: "get_weather", Desc: "current weather", Exec: func(context.Context, string) (string, error) {
		return resultText, nil
	}}}
	input := []Message{{Role: RoleUser, Content: "weather in vilnius?"}}
	wire, _ := toEinoWithIdentities(input)
	schemaTokens, err := estimateToolSchemaTokens(tools)
	if err != nil {
		t.Fatal(err)
	}
	budget := estimateConversationTokens(wire) + schemaTokens + 100
	model := &toolMockModel{}
	var estimates []ContextEstimate
	var checkpoints []Checkpoint
	_, err = New().StreamTurnWithTools(context.Background(), model, input, tools,
		TurnConfig{
			MaxIters:            8,
			ContextBudgetTokens: budget,
			ContextCompactor: func(_ context.Context, history []Message, estimate ContextEstimate) ([]Message, error) {
				estimates = append(estimates, estimate)
				if estimate.TotalTokens <= estimate.BudgetTokens || estimate.ToolSchemaTokens != schemaTokens {
					t.Fatalf("compactor received wrong context estimate: %+v, schemas = %d", estimate, schemaTokens)
				}
				replacement := append([]Message(nil), history...)
				foundCall, foundResult := false, false
				for i := range replacement {
					switch replacement[i].Role {
					case RoleAssistant:
						foundCall = len(replacement[i].ToolCalls) == 1 && replacement[i].ToolCalls[0].ID == "tc-1"
					case RoleTool:
						if replacement[i].ToolCallID == "tc-1" {
							foundResult = replacement[i].Content == resultText
							replacement[i].Content = "weather observation summarized: sunny"
						}
					}
				}
				if !foundCall || !foundResult {
					t.Fatalf("compactor did not receive the complete tool-call group: %+v", history)
				}
				return replacement, nil
			},
		},
		Callbacks{OnCheckpoint: func(checkpoint Checkpoint) { checkpoints = append(checkpoints, checkpoint) }})
	if err != nil {
		t.Fatal(err)
	}
	if len(estimates) != 1 {
		t.Fatalf("compactor calls = %d, want once before the over-budget second request", len(estimates))
	}
	if len(checkpoints) != 1 {
		t.Fatalf("checkpoint calls = %d, want an immediate checkpoint after compaction", len(checkpoints))
	}
	if err := validateToolMessagePairing([]Message{
		{Role: checkpoints[0].Messages[1].Role, ToolCalls: checkpointToolCalls(checkpoints[0].Messages[1].ToolCalls)},
		{Role: checkpoints[0].Messages[2].Role, ToolCallID: checkpoints[0].Messages[2].ToolCallID},
	}); err != nil {
		t.Fatalf("compaction checkpoint broke tool-call pairing: %v", err)
	}
	if model.calls != 2 {
		t.Fatalf("model calls = %d, want 2", model.calls)
	}
	for _, message := range model.gotIn {
		if message.Role == schema.Tool {
			if message.Content != "weather observation summarized: sunny" {
				t.Fatalf("model did not receive compacted result: %q", message.Content)
			}
			return
		}
	}
	t.Fatal("compacted second model request has no tool result")
}

func TestContextPressureWithoutCompactorReturnsTypedError(t *testing.T) {
	model := &mockModel{chunks: []*schema.Message{{Role: schema.Assistant, Content: "should not run"}}}
	_, err := New().StreamTurnWithTools(context.Background(), model,
		[]Message{{Role: RoleUser, Content: strings.Repeat("x", 1000)}}, nil,
		TurnConfig{MaxIters: 1, ContextBudgetTokens: 10}, Callbacks{})
	var budgetErr *ContextBudgetError
	if !errors.As(err, &budgetErr) {
		t.Fatalf("error = %v, want ContextBudgetError", err)
	}
	if model.gotIn != nil {
		t.Fatal("model request started despite context budget error")
	}
}

func TestContextCompactorErrorStopsModelRequest(t *testing.T) {
	model := &mockModel{chunks: []*schema.Message{{Role: schema.Assistant, Content: "should not run"}}}
	wantErr := errors.New("summary store unavailable")
	_, err := New().StreamTurnWithTools(context.Background(), model,
		[]Message{{Role: RoleUser, Content: strings.Repeat("x", 1000)}}, nil,
		TurnConfig{
			MaxIters: 1, ContextBudgetTokens: 10,
			ContextCompactor: func(context.Context, []Message, ContextEstimate) ([]Message, error) {
				return nil, wantErr
			},
		}, Callbacks{})
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want wrapped compactor error %v", err, wantErr)
	}
	if model.gotIn != nil {
		t.Fatal("model request started after compactor failed")
	}
}

// loopingModel asks for a tool call every round, so the iteration-driven
// behaviors (checkpointing, the iteration cap) can be exercised.
type loopingModel struct {
	mockModel
	calls int
}

func (m *loopingModel) WithTools([]*schema.ToolInfo) (einomodel.ToolCallingChatModel, error) {
	return m, nil
}

func (m *loopingModel) Stream(_ context.Context, in []*schema.Message, _ ...einomodel.Option) (*schema.StreamReader[*schema.Message], error) {
	m.calls++
	m.gotIn = in
	idx := 0
	return schema.StreamReaderFromArray([]*schema.Message{
		{Role: schema.Assistant, ToolCalls: []schema.ToolCall{{
			Index: &idx, ID: "tc", Function: schema.FunctionCall{Name: "noop", Arguments: `{}`},
		}}},
	}), nil
}

func TestOnCheckpointFiresPeriodically(t *testing.T) {
	noop := Tool{
		Name: "noop", Desc: "does nothing",
		Exec: func(context.Context, string) (string, error) { return "ok", nil },
	}

	t.Run("fires every N iterations with no pending call", func(t *testing.T) {
		var cks []Checkpoint
		_, err := New().StreamTurnWithTools(context.Background(), &loopingModel{},
			[]Message{{Role: RoleUser, Content: "go"}}, []Tool{noop},
			TurnConfig{MaxIters: 9, CheckpointEvery: 4},
			Callbacks{OnCheckpoint: func(ck Checkpoint) { cks = append(cks, ck) }})
		if err != nil {
			t.Fatal(err)
		}
		// Iterations 0..8; offers at 4 and 8.
		if len(cks) != 2 {
			t.Fatalf("got %d checkpoints, want 2 (at iterations 4 and 8)", len(cks))
		}
		if cks[0].Iter != 4 || cks[1].Iter != 8 {
			t.Fatalf("checkpoint iterations = %d, %d; want 4, 8", cks[0].Iter, cks[1].Iter)
		}
		for i, ck := range cks {
			// A recovery checkpoint must have no half-executed call: resume
			// re-asks the model rather than repeating a tool.
			if len(ck.Pending) != 0 {
				t.Fatalf("checkpoint %d carries %d pending call(s); recovery snapshots must be taken between rounds", i, len(ck.Pending))
			}
			if len(ck.Messages) == 0 {
				t.Fatalf("checkpoint %d has no messages to resume from", i)
			}
		}
	})

	t.Run("disabled by default", func(t *testing.T) {
		fired := 0
		_, err := New().StreamTurnWithTools(context.Background(), &loopingModel{},
			[]Message{{Role: RoleUser, Content: "go"}}, []Tool{noop},
			TurnConfig{MaxIters: 9},
			Callbacks{OnCheckpoint: func(Checkpoint) { fired++ }})
		if err != nil {
			t.Fatal(err)
		}
		if fired != 0 {
			t.Fatalf("fired %d times with CheckpointEvery unset", fired)
		}
	})

	t.Run("a resumed turn checkpoints from where it restarted", func(t *testing.T) {
		var cks []Checkpoint
		ck := Checkpoint{
			Messages: []CheckpointMessage{{Role: RoleUser, Content: "go"}},
			Iter:     3,
		}
		_, err := New().ResumeTurnWithTools(context.Background(), &loopingModel{}, ck, []Tool{noop},
			TurnConfig{MaxIters: 12, CheckpointEvery: 4}, false, "",
			Callbacks{OnCheckpoint: func(c Checkpoint) { cks = append(cks, c) }})
		if err != nil {
			t.Fatal(err)
		}
		// Offers are relative to where the resume started (3), so 7 and 11.
		if len(cks) != 2 || cks[0].Iter != 7 || cks[1].Iter != 11 {
			got := []int{}
			for _, c := range cks {
				got = append(got, c.Iter)
			}
			t.Fatalf("checkpoint iterations = %v, want [7 11]", got)
		}
	})
}
