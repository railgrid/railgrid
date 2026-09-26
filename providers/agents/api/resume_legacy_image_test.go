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
	"testing"
	"time"

	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"github.com/railgrid/provider-agents/engine"
	"github.com/railgrid/provider-agents/store"
)

func TestLegacyImageCheckpointUpgradeResumesWithDurableRequestAnchor(t *testing.T) {
	f, run, checkpoint, _, request := legacyImageResumeFixture(t, "anchor")
	f.s.upgradeLegacyImageCheckpoint(context.Background(), f.scope, run, &checkpoint)
	if !checkpoint.Messages[3].Ephemeral {
		t.Fatalf("verified legacy image note was not marked ephemeral: %#v", checkpoint.Messages[3])
	}
	if checkpoint.Messages[0].Ephemeral || checkpoint.Messages[0].ID != request.ID {
		t.Fatalf("durable request was changed during upgrade: %#v", checkpoint.Messages[0])
	}

	model := &finalResumeModel{}
	compactorCalls := 0
	result, err := engine.New().ResumeTurnWithTools(context.Background(), model, checkpoint, nil, engine.TurnConfig{
		MaxIters: 1, ContextBudgetTokens: 20,
		ContextCompactor: func(_ context.Context, history []engine.Message, _ engine.ContextEstimate) ([]engine.Message, error) {
			compactorCalls++
			var durableRequest *engine.Message
			for index := range history {
				message := &history[index]
				if message.Role == engine.RoleUser && !message.Ephemeral {
					durableRequest = message
				}
			}
			if durableRequest == nil || durableRequest.ID != request.ID || durableRequest.Content != run.Input {
				t.Fatalf("resume compactor did not receive the trusted request anchor: %#v", history)
			}
			if !history[3].Ephemeral {
				t.Fatalf("restored checkpoint lost image provenance: %#v", history[3])
			}
			return []engine.Message{*durableRequest}, nil
		},
	}, false, "", engine.Callbacks{})
	if err != nil {
		t.Fatalf("ResumeTurnWithTools() error = %v", err)
	}
	if compactorCalls != 1 || result.FinalContent == "" {
		t.Fatalf("resume compactions=%d result=%+v, want one compaction and a final answer", compactorCalls, result)
	}
	if len(model.input) != 1 || model.input[0].Role != schema.User || model.input[0].Content != run.Input {
		t.Fatalf("resumed model input = %#v, want the retained durable request", model.input)
	}
}

func TestLegacyImageCheckpointUpgradePreservesLiteralRequestAndSkipsUntrustedShapes(t *testing.T) {
	t.Run("literal request marker is not upgraded", func(t *testing.T) {
		_, run, checkpoint, rows, request := legacyImageResumeFixture(t, legacyResumeImagePlaceholder)
		if upgraded := markLegacyResumeImagePlaceholders(&checkpoint, run, rows); upgraded != 0 {
			t.Fatalf("upgraded %d messages even though the run input is the literal marker", upgraded)
		}
		if checkpoint.Messages[0].Ephemeral || checkpoint.Messages[0].Content != legacyResumeImagePlaceholder || checkpoint.Messages[0].ID != request.ID {
			t.Fatalf("literal durable request was changed: %#v", checkpoint.Messages[0])
		}
		if checkpoint.Messages[3].Ephemeral {
			t.Fatalf("marker collision should leave the checkpoint unchanged: %#v", checkpoint.Messages[3])
		}
	})

	t.Run("tool group must match this run's durable rows", func(t *testing.T) {
		_, run, checkpoint, rows, _ := legacyImageResumeFixture(t, "anchor")
		filtered := make([]store.Message, 0, len(rows))
		for _, row := range rows {
			if row.Role != "assistant" && row.Role != "tool" {
				filtered = append(filtered, row)
			}
		}
		if upgraded := markLegacyResumeImagePlaceholders(&checkpoint, run, filtered); upgraded != 0 || checkpoint.Messages[3].Ephemeral {
			t.Fatalf("unmatched tool group changed checkpoint: upgraded=%d image=%#v", upgraded, checkpoint.Messages[3])
		}
	})

	t.Run("duplicate durable request rows are ambiguous", func(t *testing.T) {
		_, run, checkpoint, rows, request := legacyImageResumeFixture(t, "anchor")
		rows = append(rows, store.Message{
			ID: "duplicate-request", RunID: run.ID, SessionID: run.SessionID,
			Role: "user", Content: run.Input, Sequence: request.Sequence + 1,
		})
		if upgraded := markLegacyResumeImagePlaceholders(&checkpoint, run, rows); upgraded != 0 || checkpoint.Messages[3].Ephemeral {
			t.Fatalf("ambiguous request changed checkpoint: upgraded=%d image=%#v", upgraded, checkpoint.Messages[3])
		}
	})
}

func legacyImageResumeFixture(t *testing.T, task string) (*compactFixture, store.Run, engine.Checkpoint, []store.Message, engine.Message) {
	t.Helper()
	f := newCompactFixture(t, 0, 0, "gpt-4o")
	const runID = "run-legacy-image-resume"
	_, history := appendAndAssembleCurrentTask(t, f, runID, task)
	request := history[len(history)-1]
	const callID = "call-legacy-image-resume"
	args := `{"camera":"front-door"}`
	call := schema.ToolCall{ID: callID, Type: "function", Function: schema.FunctionCall{Name: "camera_snapshot", Arguments: args}}
	at := time.Now().UTC()
	appendCompactMessage(t, f, assistantCallRow("assistant-legacy-image-resume", runID, callID, call.Function.Name, args, at))
	appendCompactMessage(t, f, toolResultRow("tool-legacy-image-resume", runID, callID, call.Function.Name, "snapshot fetched", at.Add(time.Second)))
	rows, err := f.s.loadAllSessionMessages(context.Background(), f.scope, "chat")
	if err != nil {
		t.Fatal(err)
	}
	checkpoint := engine.Checkpoint{Messages: []engine.CheckpointMessage{
		{Role: engine.RoleUser, Content: task, ID: request.ID, Sequence: request.Sequence},
		{Role: engine.RoleAssistant, ToolCalls: []engine.CheckpointToolCall{{ID: callID, Name: call.Function.Name, Args: args}}},
		{Role: engine.RoleTool, ToolCallID: callID, Name: call.Function.Name, Content: "snapshot fetched"},
		{Role: engine.RoleUser, Content: legacyResumeImagePlaceholder},
	}}
	run := store.Run{ID: runID, SessionID: "chat", Input: task}
	return f, run, checkpoint, rows, request
}

type finalResumeModel struct {
	input []*schema.Message
}

func (m *finalResumeModel) Generate(_ context.Context, input []*schema.Message, _ ...einomodel.Option) (*schema.Message, error) {
	m.input = input
	return schema.AssistantMessage("resumed", nil), nil
}

func (m *finalResumeModel) Stream(_ context.Context, input []*schema.Message, _ ...einomodel.Option) (*schema.StreamReader[*schema.Message], error) {
	m.input = input
	return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("resumed", nil)}), nil
}
