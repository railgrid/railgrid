/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package llm

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// fakeModel is a scriptable ToolCallingChatModel for exercising FallbackModel.
type fakeModel struct {
	genErr       error
	genContent   string
	genCalls     int
	streamErr    error    // error returned by Stream() itself
	firstRecvErr error    // error surfaced on the first Recv (post-Stream)
	chunks       []string // content chunks emitted by Stream
	streamCalls  int
}

func (f *fakeModel) Generate(_ context.Context, _ []*schema.Message, _ ...einomodel.Option) (*schema.Message, error) {
	f.genCalls++
	if f.genErr != nil {
		return nil, f.genErr
	}
	return &schema.Message{Role: schema.Assistant, Content: f.genContent}, nil
}

func (f *fakeModel) Stream(_ context.Context, _ []*schema.Message, _ ...einomodel.Option) (*schema.StreamReader[*schema.Message], error) {
	f.streamCalls++
	if f.streamErr != nil {
		return nil, f.streamErr
	}
	sr, sw := schema.Pipe[*schema.Message](8)
	go func() {
		defer sw.Close()
		if f.firstRecvErr != nil {
			sw.Send(nil, f.firstRecvErr)
			return
		}
		for _, c := range f.chunks {
			if sw.Send(&schema.Message{Role: schema.Assistant, Content: c}, nil) {
				return
			}
		}
	}()
	return sr, nil
}

type originFakeModel struct {
	*fakeModel
	origin string
}

func (f *originFakeModel) ResponsesOrigin() string { return f.origin }

func (f *fakeModel) WithTools(_ []*schema.ToolInfo) (einomodel.ToolCallingChatModel, error) {
	return f, nil
}

func drain(t *testing.T, sr *schema.StreamReader[*schema.Message]) string {
	t.Helper()
	defer sr.Close()
	var b strings.Builder
	for {
		m, err := sr.Recv()
		if errors.Is(err, io.EOF) {
			return b.String()
		}
		if err != nil {
			t.Fatalf("Recv: %v", err)
		}
		b.WriteString(m.Content)
	}
}

func TestFallbackGenerateSkipsFailures(t *testing.T) {
	m := NewFallbackModel(
		[]einomodel.BaseChatModel{
			&fakeModel{genErr: errors.New("rate limited")},
			&fakeModel{genContent: "second wins"},
		},
		[]string{"primary", "backup"},
	)
	msg, err := m.Generate(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if msg.Content != "second wins" {
		t.Fatalf("got %q, want the backup's response", msg.Content)
	}
}

func TestFallbackGenerateAllFail(t *testing.T) {
	m := NewFallbackModel(
		[]einomodel.BaseChatModel{
			&fakeModel{genErr: errors.New("boom-1")},
			&fakeModel{genErr: errors.New("boom-2")},
		},
		[]string{"a", "b"},
	)
	if _, err := m.Generate(context.Background(), nil); err == nil {
		t.Fatal("expected an error when every model fails")
	}
}

func TestFallbackResponsesContinuationUsesOnlyMatchingMember(t *testing.T) {
	input := []*schema.Message{{Role: schema.Assistant, ToolCalls: []schema.ToolCall{{
		ID: "call-1", Extra: map[string]any{
			ResponsesToolItemIDExtraKey: "fc-1", ResponsesToolOriginExtraKey: "credential-a",
		},
	}}}}
	chatPrimary := &fakeModel{genContent: "wrong primary", chunks: []string{"wrong primary"}}
	responsesMatch := &originFakeModel{fakeModel: &fakeModel{genContent: "matching backup", chunks: []string{"matching backup"}}, origin: "credential-a"}
	model := NewFallbackModel([]einomodel.BaseChatModel{chatPrimary, responsesMatch}, []string{"chat", "responses-a"})

	generated, err := model.Generate(context.Background(), input)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if generated.Content != "matching backup" || chatPrimary.genCalls != 0 || responsesMatch.genCalls != 1 {
		t.Fatalf("Generate used the wrong member: content=%q chat calls=%d matching calls=%d", generated.Content, chatPrimary.genCalls, responsesMatch.genCalls)
	}
	stream, err := model.Stream(context.Background(), input)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if got := drain(t, stream); got != "matching backup" {
		t.Fatalf("Stream content = %q, want matching backup", got)
	}
	if chatPrimary.streamCalls != 0 || responsesMatch.streamCalls != 1 {
		t.Fatalf("Stream used the wrong member: chat calls=%d matching calls=%d", chatPrimary.streamCalls, responsesMatch.streamCalls)
	}
}

func TestFallbackResponsesContinuationHydratesAggregateMetadata(t *testing.T) {
	chatPrimary := &fakeModel{genContent: "must not run"}
	responsesMatch := &originFakeModel{fakeModel: &fakeModel{genContent: "matching backup"}, origin: "credential-a"}
	model := NewFallbackModel([]einomodel.BaseChatModel{chatPrimary, responsesMatch}, []string{"chat", "responses-a"})
	input := []*schema.Message{{Role: schema.Assistant, ToolCalls: []schema.ToolCall{{
		ID: "call-1", Type: "function", Function: schema.FunctionCall{Name: "lookup", Arguments: `{}`},
	}}, Extra: map[string]any{responsesToolStateKey: `{"call-1":{"railgrid_openai_responses_item_id":"fc-1","railgrid_openai_responses_origin":"credential-a"}}`}}}
	if _, err := model.Generate(context.Background(), input); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if chatPrimary.genCalls != 0 || responsesMatch.genCalls != 1 {
		t.Fatalf("aggregate metadata did not constrain fallback: chat calls=%d matching calls=%d", chatPrimary.genCalls, responsesMatch.genCalls)
	}
}

func TestFallbackResponsesContinuationRejectsMissingOrUnmatchedOrigin(t *testing.T) {
	for _, tc := range []struct {
		name string
		call schema.ToolCall
	}{
		{name: "missing origin", call: schema.ToolCall{ID: "call-1", Extra: map[string]any{ResponsesToolItemIDExtraKey: "fc-1"}}},
		{name: "no matching member", call: schema.ToolCall{ID: "call-1", Extra: map[string]any{ResponsesToolOriginExtraKey: "credential-a"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			chat := &fakeModel{genContent: "must not run"}
			other := &originFakeModel{fakeModel: &fakeModel{genContent: "must not run"}, origin: "credential-b"}
			model := NewFallbackModel([]einomodel.BaseChatModel{chat, other}, []string{"chat", "responses-b"})
			_, err := model.Generate(context.Background(), []*schema.Message{{Role: schema.Assistant, ToolCalls: []schema.ToolCall{tc.call}}})
			if err == nil {
				t.Fatal("Generate unexpectedly accepted an unusable Responses continuation")
			}
			if chat.genCalls != 0 || other.genCalls != 0 {
				t.Fatalf("invalid continuation reached a model: chat=%d Responses=%d", chat.genCalls, other.genCalls)
			}
		})
	}
}

// A provider that errors on the first Recv (not from Stream itself) must still
// fall back — this is the common HTTP-error-on-first-chunk case.
func TestFallbackStreamFallsBackOnFirstRecvError(t *testing.T) {
	m := NewFallbackModel(
		[]einomodel.BaseChatModel{
			&fakeModel{firstRecvErr: errors.New("429 after stream opened")},
			&fakeModel{chunks: []string{"hel", "lo"}},
		},
		[]string{"primary", "backup"},
	)
	sr, err := m.Stream(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := drain(t, sr); got != "hello" {
		t.Fatalf("got %q, want %q from the backup", got, "hello")
	}
}

// When the first model streams content, the reassembled reader must replay the
// peeked first chunk plus the remainder — no dropped or duplicated tokens.
func TestFallbackStreamUsesFirstAndReplaysPeek(t *testing.T) {
	m := NewFallbackModel(
		[]einomodel.BaseChatModel{
			&fakeModel{chunks: []string{"a", "b", "c"}},
			&fakeModel{chunks: []string{"should", "not", "run"}},
		},
		[]string{"primary", "backup"},
	)
	sr, err := m.Stream(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := drain(t, sr); got != "abc" {
		t.Fatalf("got %q, want %q", got, "abc")
	}
}

func TestFallbackStreamAllFail(t *testing.T) {
	m := NewFallbackModel(
		[]einomodel.BaseChatModel{
			&fakeModel{streamErr: errors.New("dial tcp: refused")},
			&fakeModel{firstRecvErr: errors.New("500")},
		},
		[]string{"a", "b"},
	)
	if _, err := m.Stream(context.Background(), nil); err == nil {
		t.Fatal("expected an error when every model fails to stream")
	}
}

func TestFallbackWithToolsBindsAllMembers(t *testing.T) {
	m := NewFallbackModel(
		[]einomodel.BaseChatModel{
			&fakeModel{genErr: errors.New("down")},
			&fakeModel{genContent: "ok"},
		},
		[]string{"a", "b"},
	)
	tcm, ok := m.(einomodel.ToolCallingChatModel)
	if !ok {
		t.Fatal("FallbackModel must implement ToolCallingChatModel")
	}
	bound, err := tcm.WithTools([]*schema.ToolInfo{{Name: "search"}})
	if err != nil {
		t.Fatalf("WithTools: %v", err)
	}
	msg, err := bound.Generate(context.Background(), nil)
	if err != nil {
		t.Fatalf("Generate after WithTools: %v", err)
	}
	if msg.Content != "ok" {
		t.Fatalf("got %q, want the backup's response through the bound chain", msg.Content)
	}
}

func TestNewFallbackModelSingleMemberUnwrapped(t *testing.T) {
	only := &fakeModel{genContent: "solo"}
	m := NewFallbackModel([]einomodel.BaseChatModel{only}, []string{"solo"})
	if _, isFallback := m.(*FallbackModel); isFallback {
		t.Fatal("a single member should be returned unwrapped, not decorated")
	}
}
