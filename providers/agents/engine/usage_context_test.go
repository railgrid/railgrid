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
	"testing"

	"github.com/cloudwego/eino/schema"
)

func TestReportedUsageTriggersContextCompaction(t *testing.T) {
	user := Message{Role: RoleUser, Content: "continue"}
	messages := []*schema.Message{schema.UserMessage(user.Content), {
		Role: schema.Assistant, Content: "observed",
		ResponseMeta: &schema.ResponseMeta{Usage: &schema.TokenUsage{PromptTokens: 900, CompletionTokens: 200}},
	}}
	called := false
	_, _, compacted, err := compactBeforeRequest(context.Background(), messages, nil, 50, &user, TurnConfig{
		ContextBudgetTokens: 1000,
		ContextCompactor: func(_ context.Context, history []Message, estimate ContextEstimate) ([]Message, error) {
			called = true
			if estimate.TotalTokens != 1100 {
				t.Fatalf("usage should include schemas once: %+v", estimate)
			}
			return []Message{user, {Role: RoleAssistant, Content: "summary"}}, nil
		},
	})
	if err != nil || !called || !compacted {
		t.Fatalf("reported pressure did not compact: called=%v compacted=%v err=%v", called, compacted, err)
	}
}

func TestContextUsageAddsOnlyMessagesAfterLatestResponse(t *testing.T) {
	messages := []*schema.Message{
		schema.UserMessage("request"),
		{Role: schema.Assistant, ResponseMeta: &schema.ResponseMeta{Usage: &schema.TokenUsage{TotalTokens: 9000}}},
		{Role: schema.Assistant, ResponseMeta: &schema.ResponseMeta{Usage: &schema.TokenUsage{PromptTokens: 600, CompletionTokens: 50}}},
		schema.ToolMessage("new result", "call-1"),
	}
	got := requestContextEstimate(messages, 100, 1000)
	want := 650 + estimateMessageTokens(messages[3])
	if got.TotalTokens != want {
		t.Fatalf("total = %d, want %d", got.TotalTokens, want)
	}
	messages[2].ResponseMeta = nil
	messages[1].ResponseMeta = nil
	got = requestContextEstimate(messages, 100, 1000)
	if got.TotalTokens != estimateConversationTokens(messages)+100 {
		t.Fatalf("no-usage fallback = %+v", got)
	}
}

func TestCurrentRequestIdentityAllowsSequenceEnrichmentButNotContentChange(t *testing.T) {
	anchor := &Message{Role: RoleUser, ID: "request", Content: "preserve this request"}
	replacement := []Message{{Role: RoleUser, ID: anchor.ID, Sequence: 42, Content: anchor.Content}}
	if err := ensureLatestUserMessageRetained(replacement, anchor); err != nil {
		t.Fatal(err)
	}
	replacement[0].Content = "different request"
	if err := ensureLatestUserMessageRetained(replacement, anchor); err == nil {
		t.Fatal("changed request content was accepted")
	}
}
