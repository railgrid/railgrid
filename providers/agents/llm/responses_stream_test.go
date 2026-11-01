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
	"testing"

	"github.com/cloudwego/eino/schema"
)

func TestResponsesStreamRejectsMalformedTruncatedAndFailedEvents(t *testing.T) {
	for _, test := range []struct {
		name     string
		wire     string
		wantEOF  bool
		wantCode string
	}{
		{
			name:    "truncated before response.completed",
			wire:    "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\",\"status\":\"in_progress\",\"output\":[]}}\n\n",
			wantEOF: true,
		},
		{name: "malformed event", wire: "data: {not-json}\n\n"},
		{name: "structured failure", wire: "data: {\"type\":\"error\",\"code\":\"context_length_exceeded\",\"message\":\"input too large\"}\n\n", wantCode: "context_length_exceeded"},
	} {
		t.Run(test.name, func(t *testing.T) {
			model := responsesFixtureModel(t, func(w http.ResponseWriter, req *http.Request) {
				_ = responsesFixtureRequest(t, req)
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, test.wire)
			})
			stream, err := model.Stream(context.Background(), []*schema.Message{schema.UserMessage("Continue safely.")})
			if err != nil {
				t.Fatal(err)
			}
			defer stream.Close()
			for {
				_, recvErr := stream.Recv()
				if errors.Is(recvErr, io.EOF) {
					t.Fatal("unfinished or invalid Responses stream ended successfully")
				}
				if recvErr == nil {
					continue
				}
				if test.wantEOF && !errors.Is(recvErr, io.ErrUnexpectedEOF) {
					t.Errorf("truncated stream error = %v, want unexpected EOF", recvErr)
				}
				if test.wantCode != "" {
					var failure *responsesFailureError
					if !errors.As(recvErr, &failure) || failure.Code != test.wantCode {
						t.Errorf("structured failure = %#v, want code %q: %v", failure, test.wantCode, recvErr)
					}
				}
				break
			}
		})
	}
}

func TestResponsesOriginBindsContinuationToCredential(t *testing.T) {
	baseURL := "https://api.example.test/v1"
	first, err := NewResponsesModel(context.Background(), Profile{Provider: ProviderOpenAICompatible, BaseURL: baseURL, Model: "gpt-6.1-sol", APIKey: "test-key-a"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewResponsesModel(context.Background(), Profile{Provider: ProviderOpenAICompatible, BaseURL: baseURL, Model: "gpt-6.1-sol", APIKey: "test-key-b"})
	if err != nil {
		t.Fatal(err)
	}
	firstOrigin := first.(interface{ ResponsesOrigin() string }).ResponsesOrigin()
	secondOrigin := second.(interface{ ResponsesOrigin() string }).ResponsesOrigin()
	if firstOrigin == "" || firstOrigin == secondOrigin || firstOrigin == "test-key-a" || secondOrigin == "test-key-b" {
		t.Fatalf("credential fingerprints = %q and %q", firstOrigin, secondOrigin)
	}
	message := &schema.Message{Role: schema.Assistant, ToolCalls: []schema.ToolCall{{
		ID: "call_1", Type: "function", Function: schema.FunctionCall{Name: "read_file", Arguments: `{}`},
		Extra: map[string]any{ResponsesToolOriginExtraKey: secondOrigin},
	}}}
	if _, err := first.Generate(context.Background(), []*schema.Message{message}); err == nil {
		t.Fatal("expected continuation from another credential to be rejected")
	}
}
