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

func TestStreamTurn_NilModel(t *testing.T) {
	if _, err := New().StreamTurn(context.Background(), nil, []Message{{Role: RoleUser, Content: "x"}}, nil); err == nil {
		t.Fatal("expected error for nil model")
	}
}
