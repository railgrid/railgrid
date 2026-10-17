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

package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/cloudwego/eino/schema"
)

func TestProjectEinoReasoningEffortWirePayloads(t *testing.T) {
	tests := []struct {
		name            string
		model           string
		effort          string
		transport       string
		effectiveEffort *string
		wantPath        string
		wantChatEffort  string
		wantRespEffort  string
		wantNoEffort    bool
	}{
		{name: "Chat Completions explicit effort", model: "gpt-5.1", effort: "high", wantPath: "/v1/chat/completions", wantChatEffort: "high"},
		{name: "Responses explicit GPT-5.6 effort", model: "gpt-5.6-luna", effort: "high", wantPath: "/v1/responses", wantRespEffort: "high"},
		{name: "GPT-5.6 explicit none stays Chat", model: "gpt-5.6-luna", effort: "none", wantPath: "/v1/chat/completions", wantChatEffort: "none"},
		{name: "GPT-5.6 automatic compatibility effort", model: "gpt-5.6-luna", wantPath: "/v1/chat/completions", wantChatEffort: "none"},
		{name: "frozen Chat snapshot preserves omitted effort", model: "gpt-5.6-luna", transport: projectLLMTransportChatCompletions, effectiveEffort: reasoningEffortPointer(""), wantPath: "/v1/chat/completions", wantNoEffort: true},
		{name: "frozen Responses snapshot preserves omitted effort", model: "gpt-5.6-luna", effort: "high", transport: projectLLMTransportResponses, effectiveEffort: reasoningEffortPointer(""), wantPath: "/v1/responses", wantNoEffort: true},
		{name: "GPT-6 retains Responses transport", model: "gpt-6-luna", wantPath: "/v1/responses"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			type capturedRequest struct {
				path string
				body []byte
			}
			requests := make(chan capturedRequest, 1)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				raw, err := io.ReadAll(req.Body)
				if err != nil {
					t.Errorf("read model request: %v", err)
				}
				requests <- capturedRequest{path: req.URL.Path, body: raw}
				w.Header().Set("Content-Type", "application/json")
				if req.URL.Path == "/v1/responses" {
					_, _ = io.WriteString(w, `{"id":"resp_reasoning","object":"response","status":"completed","model":"gpt-5.6-luna","output":[{"id":"msg_1","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"OK","annotations":[]}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`)
					return
				}
				_, _ = io.WriteString(w, `{"id":"chatcmpl_reasoning","object":"chat.completion","created":1,"model":"gpt-5.1","choices":[{"index":0,"message":{"role":"assistant","content":"OK"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
			}))
			target, err := url.Parse(upstream.URL)
			if err != nil {
				t.Fatalf("parse fixture endpoint: %v", err)
			}
			originalTransport := http.DefaultTransport
			http.DefaultTransport = &projectOfficialOpenAIInterceptTransport{base: originalTransport, target: target}
			t.Cleanup(func() { http.DefaultTransport = originalTransport })

			settings := projectLLMSettings{
				Provider:                 defaultProjectLLMProvider,
				BaseURL:                  defaultProjectLLMBaseURL,
				Model:                    tt.model,
				APIKey:                   "fixture-key",
				ReasoningEffort:          tt.effort,
				ReasoningTransport:       tt.transport,
				ReasoningEffectiveEffort: tt.effectiveEffort,
			}
			model, err := newProjectEinoChatModel(context.Background(), settings)
			if err != nil {
				t.Fatalf("create model: %v", err)
			}
			result, err := model.Generate(context.Background(), []*schema.Message{schema.UserMessage("Say OK.")})
			if err != nil {
				t.Fatalf("generate fixture response: %v", err)
			}
			if result == nil || result.Content != "OK" {
				t.Fatalf("model output = %#v, want OK", result)
			}
			captured := <-requests
			if captured.path != tt.wantPath {
				t.Fatalf("model request path = %q, want %q", captured.path, tt.wantPath)
			}
			var payload map[string]json.RawMessage
			if err := json.Unmarshal(captured.body, &payload); err != nil {
				t.Fatalf("decode request payload: %v; body=%s", err, captured.body)
			}
			if tt.wantChatEffort != "" {
				var effort string
				if err := json.Unmarshal(payload["reasoning_effort"], &effort); err != nil {
					t.Fatalf("decode Chat Completions reasoning_effort: %v; body=%s", err, captured.body)
				}
				if effort != tt.wantChatEffort {
					t.Fatalf("Chat Completions reasoning_effort = %q, want %q", effort, tt.wantChatEffort)
				}
				if _, exists := payload["reasoning"]; exists {
					t.Fatalf("Chat Completions payload unexpectedly contains Responses reasoning: %s", captured.body)
				}
			}
			if tt.wantRespEffort != "" {
				var reasoning struct {
					Effort string `json:"effort"`
				}
				if err := json.Unmarshal(payload["reasoning"], &reasoning); err != nil {
					t.Fatalf("decode Responses reasoning: %v; body=%s", err, captured.body)
				}
				if reasoning.Effort != tt.wantRespEffort {
					t.Fatalf("Responses reasoning.effort = %q, want %q", reasoning.Effort, tt.wantRespEffort)
				}
				if _, exists := payload["reasoning_effort"]; exists {
					t.Fatalf("Responses payload unexpectedly contains Chat Completions reasoning_effort: %s", captured.body)
				}
			}
			if tt.wantNoEffort {
				field := "reasoning_effort"
				if captured.path == "/v1/responses" {
					field = "reasoning"
				}
				if _, exists := payload[field]; exists {
					t.Fatalf("payload unexpectedly contains frozen-omitted %s: %s", field, captured.body)
				}
			}
			if bytes.Contains(captured.body, []byte("private reasoning")) {
				t.Fatalf("request exposed plaintext reasoning: %s", captured.body)
			}
		})
	}
}

type projectOfficialOpenAIInterceptTransport struct {
	base   http.RoundTripper
	target *url.URL
}

func (t *projectOfficialOpenAIInterceptTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL == nil || req.URL.Scheme != "https" || req.URL.Host != "api.openai.com" {
		return nil, fmt.Errorf("unexpected request endpoint %v", req.URL)
	}
	clone := req.Clone(req.Context())
	targetURL := *req.URL
	targetURL.Scheme = t.target.Scheme
	targetURL.Host = t.target.Host
	clone.URL = &targetURL
	clone.Host = t.target.Host
	return t.base.RoundTrip(clone)
}
