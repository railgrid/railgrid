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
	"encoding/gob"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	aiv1alpha1 "github.com/railgrid/provider-app-studio/apis/ai/v1alpha1"
)

func TestProjectEinoAnthropicReasoningEffortWirePayload(t *testing.T) {
	for _, tc := range []struct {
		name          string
		effort        string
		frozenEffort  *string
		wantEffort    string
		wantMaxTokens int
		wantStream    bool
	}{
		{name: "explicit high effort reaches output config", effort: "high", wantEffort: "high", wantMaxTokens: 16384},
		{name: "model default omits output config", wantMaxTokens: 16384},
		{name: "frozen empty effort stays omitted", effort: "high", frozenEffort: reasoningEffortPointer(""), wantMaxTokens: 16384},
		{name: "xhigh effort uses expanded token limit and stream", effort: "xhigh", wantEffort: "xhigh", wantMaxTokens: 65536, wantStream: true},
		{name: "max effort uses expanded token limit and stream", effort: "max", wantEffort: "max", wantMaxTokens: 65536, wantStream: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			type capturedRequest struct {
				path   string
				body   []byte
				stream bool
			}
			requests := make(chan capturedRequest, 1)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				raw, err := io.ReadAll(req.Body)
				if err != nil {
					t.Errorf("read model request: %v", err)
				}
				var requestPayload struct {
					Stream bool `json:"stream"`
				}
				if err := json.Unmarshal(raw, &requestPayload); err != nil {
					t.Errorf("decode request stream flag: %v; body=%s", err, raw)
				}
				requests <- capturedRequest{path: req.URL.Path, body: raw, stream: requestPayload.Stream}
				if requestPayload.Stream {
					writeProjectAnthropicTextStream(w, "OK")
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"id":"msg_fixture","type":"message","role":"assistant","model":"claude-haiku-5-5","content":[{"type":"text","text":"OK"}],"stop_reason":"end_turn","stop_sequence":null,"usage":{"input_tokens":1,"output_tokens":1}}`)
			}))
			defer upstream.Close()
			target, err := url.Parse(upstream.URL)
			if err != nil {
				t.Fatalf("parse fixture endpoint: %v", err)
			}
			originalTransport := http.DefaultTransport
			http.DefaultTransport = &projectOfficialAnthropicInterceptTransport{base: originalTransport, target: target}
			t.Cleanup(func() { http.DefaultTransport = originalTransport })

			settings := projectLLMSettings{
				Provider:           defaultProjectLLMProvider,
				BaseURL:            "https://api.anthropic.com/v1",
				Model:              "claude-haiku-5-5",
				APIKey:             "fixture-key",
				ReasoningEffort:    tc.effort,
				ReasoningTransport: projectLLMTransportAnthropic,
			}
			if tc.frozenEffort != nil {
				settings.ReasoningEffectiveEffort = tc.frozenEffort
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
			if captured.path != "/v1/messages" {
				t.Fatalf("model request path = %q, want /v1/messages", captured.path)
			}
			if captured.stream != tc.wantStream {
				t.Fatalf("model request stream = %v, want %v", captured.stream, tc.wantStream)
			}
			var payload map[string]json.RawMessage
			if err := json.Unmarshal(captured.body, &payload); err != nil {
				t.Fatalf("decode request payload: %v; body=%s", err, captured.body)
			}
			var maxTokens int
			if err := json.Unmarshal(payload["max_tokens"], &maxTokens); err != nil {
				t.Fatalf("decode Anthropic max_tokens: %v; body=%s", err, captured.body)
			}
			if maxTokens != tc.wantMaxTokens {
				t.Fatalf("max_tokens = %d, want %d", maxTokens, tc.wantMaxTokens)
			}
			if _, exists := payload["temperature"]; exists {
				t.Fatalf("Anthropic request unexpectedly contains temperature: %s", captured.body)
			}
			if tc.wantEffort != "" {
				var outputConfig struct {
					Effort string `json:"effort"`
				}
				if err := json.Unmarshal(payload["output_config"], &outputConfig); err != nil {
					t.Fatalf("decode Anthropic output_config: %v; body=%s", err, captured.body)
				}
				if outputConfig.Effort != tc.wantEffort {
					t.Fatalf("output_config.effort = %q, want %q", outputConfig.Effort, tc.wantEffort)
				}
			} else if _, exists := payload["output_config"]; exists {
				t.Fatalf("model-default request unexpectedly contains output_config: %s", captured.body)
			}
			if _, exists := payload["reasoning_effort"]; exists {
				t.Fatalf("Anthropic request unexpectedly contains OpenAI reasoning_effort: %s", captured.body)
			}
		})
	}
}

func writeProjectAnthropicTextStream(w http.ResponseWriter, text string) {
	w.Header().Set("Content-Type", "text/event-stream")
	for _, event := range []struct {
		name string
		data string
	}{
		{name: "message_start", data: `{"type":"message_start","message":{"id":"msg_stream_fixture","type":"message","role":"assistant","model":"claude-haiku-5-5","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":1,"output_tokens":0}}}`},
		{name: "content_block_start", data: `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`},
		{name: "content_block_delta", data: fmt.Sprintf(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":%q}}`, text)},
		{name: "content_block_stop", data: `{"type":"content_block_stop","index":0}`},
		{name: "message_delta", data: `{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":1}}`},
		{name: "message_stop", data: `{"type":"message_stop"}`},
	} {
		if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event.name, event.data); err != nil {
			return
		}
	}
}

func TestProjectAnthropicThinkingContinuationSurvivesCheckpointResumeAndToolResult(t *testing.T) {
	requests := make(chan []byte, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, err := io.ReadAll(req.Body)
		if err != nil {
			t.Errorf("read model request: %v", err)
		}
		requests <- body
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"msg_fixture","type":"message","role":"assistant","model":"claude-haiku-5-5","content":[{"type":"text","text":"OK"}],"stop_reason":"end_turn","stop_sequence":null,"usage":{"input_tokens":1,"output_tokens":1}}`)
	}))
	defer upstream.Close()
	target, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatalf("parse fixture endpoint: %v", err)
	}
	originalTransport := http.DefaultTransport
	http.DefaultTransport = &projectOfficialAnthropicInterceptTransport{base: originalTransport, target: target}
	t.Cleanup(func() { http.DefaultTransport = originalTransport })

	callIndex := 0
	checkpointMessages := []*schema.Message{
		schema.UserMessage("Read README.md, then answer."),
		{
			Role:    schema.Assistant,
			Content: "I will inspect the file.",
			ToolCalls: []schema.ToolCall{{
				Index:    &callIndex,
				ID:       "call_readme",
				Type:     "function",
				Function: schema.FunctionCall{Name: "read_file", Arguments: `{"path":"README.md"}`},
			}},
			Extra: map[string]any{
				projectEinoAssistantAnthropicThinkingKey:          "",
				projectEinoAssistantAnthropicThinkingSignatureKey: "signed-thinking-block",
			},
		},
		schema.ToolMessage("README contents", "call_readme", schema.WithToolName("read_file")),
	}
	var checkpoint bytes.Buffer
	if err := gob.NewEncoder(&checkpoint).Encode(checkpointMessages); err != nil {
		t.Fatalf("serialize Eino checkpoint: %v", err)
	}
	var restoredMessages []*schema.Message
	if err := gob.NewDecoder(&checkpoint).Decode(&restoredMessages); err != nil {
		t.Fatalf("restore Eino checkpoint: %v", err)
	}
	for _, message := range projectEinoMessagesToChat(restoredMessages) {
		if message.Role == string(schema.Assistant) && (message.Extra[projectEinoAssistantAnthropicThinkingKey] != nil || message.Extra[projectEinoAssistantAnthropicThinkingSignatureKey] != nil) {
			t.Fatalf("Claude continuation metadata leaked into durable chat projection: %#v", message.Extra)
		}
	}

	settings := projectLLMSettings{
		Provider:           defaultProjectLLMProvider,
		BaseURL:            "https://api.anthropic.com/v1",
		Model:              "claude-haiku-5-5",
		APIKey:             "fixture-key",
		ReasoningTransport: projectLLMTransportAnthropic,
	}
	runState := newProjectEinoAssistantRunState()
	lifecycle := projectEinoAssistantLifecycleMiddleware(projectAssistantRunRequest{
		Project: &aiv1alpha1.Project{
			ObjectMeta: metav1.ObjectMeta{Name: "demo", UID: "project-1"},
			Spec:       aiv1alpha1.ProjectSpec{DisplayName: "Demo"},
		},
		LLM: settings,
	}, runState).(*projectEinoAssistantLifecycle)
	modelState := &adk.ChatModelAgentState{Messages: restoredMessages}
	if err := lifecycle.rewriteLiveContext(context.Background(), modelState); err != nil {
		t.Fatalf("rebuild live context after checkpoint restore: %v", err)
	}

	var replayedAssistant *schema.Message
	for _, message := range modelState.Messages {
		if message != nil && message.Role == schema.Assistant && len(message.ToolCalls) > 0 {
			replayedAssistant = message
			break
		}
	}
	var thinking, signature string
	var hasThinking, hasSignature bool
	if replayedAssistant != nil {
		thinking, hasThinking = replayedAssistant.Extra[projectEinoAssistantAnthropicThinkingKey].(string)
		signature, hasSignature = replayedAssistant.Extra[projectEinoAssistantAnthropicThinkingSignatureKey].(string)
	}
	if replayedAssistant == nil || !hasThinking || thinking != "" || !hasSignature || signature != "signed-thinking-block" {
		t.Fatalf("resume context rebuild lost Anthropic continuation metadata: %#v", replayedAssistant)
	}

	model, err := newProjectEinoChatModel(context.Background(), settings)
	if err != nil {
		t.Fatalf("create native Anthropic model: %v", err)
	}
	if _, err := model.Generate(context.Background(), modelState.Messages); err != nil {
		t.Fatalf("continue after restored tool result: %v", err)
	}
	body := <-requests
	var payload struct {
		Messages []struct {
			Role    string `json:"role"`
			Content []struct {
				Type      string  `json:"type"`
				Thinking  *string `json:"thinking"`
				Signature string  `json:"signature"`
				ID        string  `json:"id"`
				ToolUseID string  `json:"tool_use_id"`
			} `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("decode resumed Anthropic request: %v; body=%s", err, body)
	}
	var thinkingRestored, toolCallRestored, toolResultRestored bool
	for _, message := range payload.Messages {
		for _, block := range message.Content {
			if message.Role == "assistant" && block.Type == "thinking" && block.Thinking != nil && *block.Thinking == "" && block.Signature == "signed-thinking-block" {
				thinkingRestored = true
			}
			if message.Role == "assistant" && block.Type == "tool_use" && block.ID == "call_readme" {
				toolCallRestored = true
			}
			if message.Role == "user" && block.Type == "tool_result" && block.ToolUseID == "call_readme" {
				toolResultRestored = true
			}
		}
	}
	if !thinkingRestored || !toolCallRestored || !toolResultRestored {
		t.Fatalf("resumed Anthropic request lost signed thinking/tool continuation: thinking=%v tool_call=%v tool_result=%v body=%s", thinkingRestored, toolCallRestored, toolResultRestored, body)
	}
}

type projectOfficialAnthropicInterceptTransport struct {
	base   http.RoundTripper
	target *url.URL
}

func (t *projectOfficialAnthropicInterceptTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL == nil || req.URL.Scheme != "https" || req.URL.Host != "api.anthropic.com" || req.URL.Path != "/v1/messages" {
		return nil, fmt.Errorf("unexpected Anthropic request endpoint %v", req.URL)
	}
	clone := req.Clone(req.Context())
	targetURL := *req.URL
	targetURL.Scheme = t.target.Scheme
	targetURL.Host = t.target.Host
	clone.URL = &targetURL
	clone.Host = t.target.Host
	return t.base.RoundTrip(clone)
}
