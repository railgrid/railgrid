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

func toolMsg(id, name, content string) *schema.Message {
	return schema.ToolMessage(content, id, schema.WithToolName(name))
}

func totalTokens(in []*schema.Message) int {
	n := 0
	for _, m := range in {
		n += estimateMessageTokens(m)
	}
	return n
}

func TestTrimConversation(t *testing.T) {
	big := strings.Repeat("x", 8000) // ~2000 tokens each

	build := func() []*schema.Message {
		return []*schema.Message{
			schema.SystemMessage("you are an agent"),
			schema.UserMessage("research this"),
			toolMsg("t1", "web_fetch", big),
			toolMsg("t2", "web_fetch", big),
			toolMsg("t3", "web_fetch", big),
			toolMsg("t4", "web_fetch", big),
			toolMsg("t5", "web_fetch", big),
			toolMsg("t6", "web_fetch", big),
		}
	}

	t.Run("a budget of zero disables trimming", func(t *testing.T) {
		in := build()
		before := totalTokens(in)
		if n := trimConversation(in, 0); n != 0 {
			t.Fatalf("clipped %d messages with no budget", n)
		}
		if totalTokens(in) != before {
			t.Fatal("conversation changed with trimming disabled")
		}
	})

	t.Run("a conversation under budget is left alone", func(t *testing.T) {
		in := build()
		before := totalTokens(in)
		if n := trimConversation(in, before*2); n != 0 {
			t.Fatalf("clipped %d messages while under budget", n)
		}
	})

	t.Run("over budget clips the oldest observations first", func(t *testing.T) {
		in := build()
		budget := totalTokens(in) / 2
		clipped := trimConversation(in, budget)
		if clipped == 0 {
			t.Fatal("expected some observations to be clipped")
		}
		if got := totalTokens(in); got > budget {
			t.Fatalf("still %d tokens after trimming, budget %d", got, budget)
		}
		// Oldest first: t1 goes before t6.
		if len(in[2].Content) >= len(big) {
			t.Fatal("the oldest observation should have been clipped")
		}
		if len(in[len(in)-1].Content) != len(big) {
			t.Fatal("the newest observation must be kept whole — it is what the model is reasoning about")
		}
	})

	t.Run("the system prompt and the user's turn are never touched", func(t *testing.T) {
		in := build()
		sys, user := in[0].Content, in[1].Content
		trimConversation(in, 10) // absurdly small: clip everything eligible
		if in[0].Content != sys {
			t.Fatal("the system prompt is policy and must survive trimming")
		}
		if in[1].Content != user {
			t.Fatal("the user's own turn must survive trimming")
		}
	})

	t.Run("message count and tool pairing are preserved", func(t *testing.T) {
		in := build()
		trimConversation(in, 10)
		if len(in) != 8 {
			t.Fatalf("len = %d, want 8 — dropping a tool message would break tool_call pairing", len(in))
		}
		for i := 2; i < len(in); i++ {
			if in[i].ToolCallID == "" {
				t.Fatalf("message %d lost its ToolCallID", i)
			}
		}
	})

	t.Run("a clipped observation says it was shortened", func(t *testing.T) {
		in := build()
		trimConversation(in, 10)
		if !strings.Contains(in[2].Content, "shortened") {
			t.Fatalf("a clipped observation must announce the gap, got: %q", in[2].Content)
		}
		// The tool is named so the model knows which result lost detail.
		if !strings.Contains(in[2].Content, "web_fetch") {
			t.Fatalf("expected the tool name in the notice, got: %q", in[2].Content)
		}
	})

	t.Run("nothing to clip is not an error", func(t *testing.T) {
		in := []*schema.Message{schema.SystemMessage("s"), schema.UserMessage(strings.Repeat("y", 100000))}
		if n := trimConversation(in, 10); n != 0 {
			t.Fatalf("clipped %d, but only a user message is oversized and those are never clipped", n)
		}
	})

	t.Run("short observations are not worth clipping", func(t *testing.T) {
		in := []*schema.Message{
			schema.SystemMessage("s"), schema.UserMessage("u"),
			toolMsg("t1", "x", "tiny"), toolMsg("t2", "x", "tiny"),
			toolMsg("t3", "x", "tiny"), toolMsg("t4", "x", "tiny"),
			toolMsg("t5", "x", "tiny"), toolMsg("t6", "x", "tiny"),
		}
		if n := trimConversation(in, 1); n != 0 {
			t.Fatalf("clipped %d already-short observations", n)
		}
	})
}

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
