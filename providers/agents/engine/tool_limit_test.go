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
	"strings"
	"testing"

	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// repeatedToolModel keeps asking for a tool so the engine reaches its
// iteration limit without producing a final assistant boundary.
type repeatedToolModel struct {
	mockModel
	calls int
}

func (m *repeatedToolModel) WithTools([]*schema.ToolInfo) (einomodel.ToolCallingChatModel, error) {
	return m, nil
}

func (m *repeatedToolModel) Stream(_ context.Context, in []*schema.Message, _ ...einomodel.Option) (*schema.StreamReader[*schema.Message], error) {
	m.calls++
	m.gotIn = in
	idx := 0
	return schema.StreamReaderFromArray([]*schema.Message{
		{Role: schema.Assistant, Content: "planning"},
		{Role: schema.Assistant, ToolCalls: []schema.ToolCall{{
			Index: &idx, ID: "tc-limit", Function: schema.FunctionCall{Name: "noop", Arguments: `{}`},
		}}},
	}), nil
}

func TestToolCallLimitProvidesStandaloneFinalContent(t *testing.T) {
	model := &repeatedToolModel{}
	const notice = "[stopped: reached the tool-call limit for one turn]"
	result, err := New().StreamTurnWithTools(context.Background(), model,
		[]Message{{Role: RoleUser, Content: "keep trying"}},
		[]Tool{{Name: "noop", Desc: "does nothing", Exec: func(context.Context, string) (string, error) {
			return "ok", nil
		}}},
		TurnConfig{MaxIters: 2}, Callbacks{})
	if err != nil {
		t.Fatal(err)
	}
	if model.calls != 2 {
		t.Fatalf("model calls = %d, want 2 before the limit", model.calls)
	}
	if result.FinalContent != notice {
		t.Fatalf("FinalContent = %q, want standalone limit notice", result.FinalContent)
	}
	if strings.Count(result.Content, notice) != 1 {
		t.Fatalf("Content = %q, want exactly one limit notice", result.Content)
	}
	if strings.Count(result.Content, "planning") != 2 {
		t.Fatalf("Content = %q, want both classified commentary segments", result.Content)
	}
}
