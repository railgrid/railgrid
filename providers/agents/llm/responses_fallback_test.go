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
	"net/http"
	"strings"
	"testing"

	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

func responsesFallbackFixture(t *testing.T, wire string, backup *fakeModel) einomodel.BaseChatModel {
	t.Helper()
	primary := responsesFixtureModel(t, func(w http.ResponseWriter, req *http.Request) {
		_ = responsesFixtureRequest(t, req)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, wire)
	})
	return NewFallbackModel(
		[]einomodel.BaseChatModel{primary, backup},
		[]string{"responses", "backup"},
	)
}

func responsesWire(events ...string) string {
	var wire strings.Builder
	for _, event := range events {
		responsesFixtureEvent(&wire, event)
	}
	return wire.String()
}

func collectFallbackStream(sr *schema.StreamReader[*schema.Message]) ([]*schema.Message, error) {
	defer sr.Close()
	var messages []*schema.Message
	for {
		message, err := sr.Recv()
		if errors.Is(err, io.EOF) {
			return messages, nil
		}
		if err != nil {
			return messages, err
		}
		messages = append(messages, message)
	}
}

func streamedText(messages []*schema.Message) string {
	var text strings.Builder
	for _, message := range messages {
		text.WriteString(message.Content)
	}
	return text.String()
}

func TestFallbackStreamUsesBackupAfterResponsesSetupOrReasoningFailure(t *testing.T) {
	const created = `{"type":"response.created","response":{"id":"resp_setup","status":"in_progress","output":[]}}`
	const providerError = `{"type":"error","code":"server_error","message":"try backup"}`
	const reasoningItemAdded = `{"type":"response.output_item.added","output_index":0,"item":{"type":"reasoning","id":"rs_setup","summary":[]}}`
	const reasoningDelta = `{"type":"response.reasoning_summary_text.delta","item_id":"rs_setup","output_index":0,"summary_index":0,"delta":"private reasoning"}`
	const reasoningItemDone = `{"type":"response.output_item.done","output_index":0,"item":{"type":"reasoning","id":"rs_setup","summary":[{"type":"summary_text","text":"private reasoning"}],"encrypted_content":"opaque-signature"}}`

	for _, tc := range []struct {
		name   string
		events []string
	}{
		{name: "created then error", events: []string{created, providerError}},
		{name: "created then failed", events: []string{created, `{"type":"response.failed","response":{"id":"resp_setup","status":"failed","error":{"code":"server_error","message":"try backup"}}}`}},
		{name: "created then incomplete", events: []string{created, `{"type":"response.incomplete","response":{"id":"resp_setup","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"}}}`}},
		{name: "truncated after created", events: []string{created}},
		{name: "reasoning only then error", events: []string{created, reasoningItemAdded, reasoningDelta, reasoningItemDone, providerError}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wire := responsesWire(tc.events...)
			backup := &fakeModel{chunks: []string{"healthy ", "backup"}}
			model := responsesFallbackFixture(t, wire, backup)
			stream, err := model.Stream(context.Background(), []*schema.Message{schema.UserMessage("hello")})
			if err != nil {
				t.Fatalf("Stream: %v", err)
			}
			messages, err := collectFallbackStream(stream)
			if err != nil {
				t.Fatalf("Recv: %v", err)
			}
			if got := streamedText(messages); got != "healthy backup" {
				t.Fatalf("stream text = %q, want healthy backup", got)
			}
			if backup.streamCalls != 1 {
				t.Fatalf("backup Stream calls = %d, want 1", backup.streamCalls)
			}
		})
	}
}

func TestFallbackStreamDoesNotRetryAfterResponsesOutput(t *testing.T) {
	const created = `{"type":"response.created","response":{"id":"resp_partial","status":"in_progress","output":[]}}`
	const providerError = `{"type":"error","code":"server_error","message":"failed after output"}`

	for _, tc := range []struct {
		name        string
		events      []string
		wantContent string
		wantTool    bool
	}{
		{
			name: "text then error",
			events: []string{
				created,
				`{"type":"response.output_item.added","output_index":0,"item":{"type":"message","id":"msg_partial","role":"assistant","status":"in_progress","content":[]}}`,
				`{"type":"response.content_part.added","item_id":"msg_partial","output_index":0,"content_index":0,"part":{"type":"output_text","text":"","annotations":[]}}`,
				`{"type":"response.output_text.delta","item_id":"msg_partial","output_index":0,"content_index":0,"delta":"partial answer"}`,
				providerError,
			},
			wantContent: "partial answer",
		},
		{
			name: "tool call then error",
			events: []string{
				created,
				`{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"fc_partial","call_id":"call_partial","name":"lookup","arguments":"","status":"in_progress"}}`,
				`{"type":"response.function_call_arguments.delta","item_id":"fc_partial","output_index":0,"delta":"{}"}`,
				providerError,
			},
			wantTool: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			backup := &fakeModel{chunks: []string{"must not replace partial output"}}
			model := responsesFallbackFixture(t, responsesWire(tc.events...), backup)
			stream, err := model.Stream(context.Background(), []*schema.Message{schema.UserMessage("hello")})
			if err != nil {
				t.Fatalf("Stream: %v", err)
			}
			messages, err := collectFallbackStream(stream)
			if err == nil {
				t.Fatal("Recv ended successfully after an incomplete Responses output")
			}
			if got := streamedText(messages); got != tc.wantContent {
				t.Fatalf("stream text = %q, want %q", got, tc.wantContent)
			}
			var toolCalls int
			for _, message := range messages {
				toolCalls += len(message.ToolCalls)
			}
			if tc.wantTool && toolCalls == 0 {
				t.Fatal("stream did not expose the partial tool call before failing")
			}
			if backup.streamCalls != 0 {
				t.Fatalf("backup Stream calls = %d, want 0 after primary output", backup.streamCalls)
			}
		})
	}
}

func TestFallbackStreamPreservesCompletedResponsesTextAndUsage(t *testing.T) {
	wire := responsesWire(
		`{"type":"response.created","response":{"id":"resp_complete","status":"in_progress","output":[]}}`,
		`{"type":"response.output_item.added","output_index":0,"item":{"type":"message","id":"msg_complete","role":"assistant","status":"in_progress","content":[]}}`,
		`{"type":"response.content_part.added","item_id":"msg_complete","output_index":0,"content_index":0,"part":{"type":"output_text","text":"","annotations":[]}}`,
		`{"type":"response.output_text.delta","item_id":"msg_complete","output_index":0,"content_index":0,"delta":"primary answer"}`,
		`{"type":"response.output_text.done","item_id":"msg_complete","output_index":0,"content_index":0,"text":"primary answer"}`,
		`{"type":"response.output_item.done","output_index":0,"item":{"type":"message","id":"msg_complete","role":"assistant","status":"completed","content":[{"type":"output_text","text":"primary answer","annotations":[]}]}}`,
		`{"type":"response.completed","response":{"id":"resp_complete","status":"completed","output":[{"type":"message","id":"msg_complete","role":"assistant","status":"completed","content":[{"type":"output_text","text":"primary answer","annotations":[]}]}],"usage":{"input_tokens":12,"output_tokens":5,"total_tokens":17,"input_tokens_details":{"cached_tokens":3},"output_tokens_details":{"reasoning_tokens":2}}}}`,
	)
	backup := &fakeModel{chunks: []string{"backup must not run"}}
	model := responsesFallbackFixture(t, wire, backup)
	stream, err := model.Stream(context.Background(), []*schema.Message{schema.UserMessage("hello")})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	messages, err := collectFallbackStream(stream)
	if err != nil {
		t.Fatalf("Recv: %v", err)
	}
	if got := streamedText(messages); got != "primary answer" {
		t.Fatalf("stream text = %q, want primary answer", got)
	}
	if len(messages) == 0 || messages[len(messages)-1].ResponseMeta == nil || messages[len(messages)-1].ResponseMeta.Usage == nil {
		t.Fatalf("terminal message did not preserve Responses usage: %#v", messages)
	}
	usage := messages[len(messages)-1].ResponseMeta.Usage
	if usage.TotalTokens != 17 || usage.PromptTokenDetails.CachedTokens != 3 || usage.CompletionTokensDetails.ReasoningTokens != 2 {
		t.Fatalf("terminal usage = %#v, want totals and token details from completed event", usage)
	}
	if backup.streamCalls != 0 {
		t.Fatalf("backup Stream calls = %d, want 0 after primary completion", backup.streamCalls)
	}
}

func TestFallbackStreamAcceptsCompletedEmptyResponsesWithUsage(t *testing.T) {
	wire := responsesWire(
		`{"type":"response.created","response":{"id":"resp_empty","status":"in_progress","output":[]}}`,
		`{"type":"response.completed","response":{"id":"resp_empty","status":"completed","output":[],"usage":{"input_tokens":8,"output_tokens":0,"total_tokens":8}}}`,
	)
	backup := &fakeModel{chunks: []string{"backup must not run"}}
	model := responsesFallbackFixture(t, wire, backup)
	stream, err := model.Stream(context.Background(), []*schema.Message{schema.UserMessage("hello")})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	messages, err := collectFallbackStream(stream)
	if err != nil {
		t.Fatalf("Recv: %v", err)
	}
	if len(messages) == 0 {
		t.Fatal("completed empty Responses stream yielded no terminal message")
	}
	if got := streamedText(messages); got != "" {
		t.Fatalf("empty stream text = %q, want empty", got)
	}
	terminal := messages[len(messages)-1]
	if terminal.ResponseMeta == nil || terminal.ResponseMeta.Usage == nil || terminal.ResponseMeta.Usage.TotalTokens != 8 {
		t.Fatalf("empty stream terminal usage = %#v, want 8 total tokens", terminal.ResponseMeta)
	}
	if backup.streamCalls != 0 {
		t.Fatalf("backup Stream calls = %d, want 0 for completed empty response", backup.streamCalls)
	}
}
