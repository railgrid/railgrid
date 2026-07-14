/*
Copyright 2026 The Faros Authors.

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
	streamErr    error    // error returned by Stream() itself
	firstRecvErr error    // error surfaced on the first Recv (post-Stream)
	chunks       []string // content chunks emitted by Stream
}

func (f *fakeModel) Generate(_ context.Context, _ []*schema.Message, _ ...einomodel.Option) (*schema.Message, error) {
	if f.genErr != nil {
		return nil, f.genErr
	}
	return &schema.Message{Role: schema.Assistant, Content: f.genContent}, nil
}

func (f *fakeModel) Stream(_ context.Context, _ []*schema.Message, _ ...einomodel.Option) (*schema.StreamReader[*schema.Message], error) {
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
