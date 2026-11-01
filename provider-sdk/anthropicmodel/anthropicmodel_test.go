// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package anthropicmodel

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"github.com/eino-contrib/jsonschema"
	"github.com/railgrid/provider-sdk/internal/claude"
)

func TestGenerateUsesNativeMessagesWireAndToolConversion(t *testing.T) {
	var requestBody map[string]any
	var requestPath string
	var requestHeaders http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestPath = r.URL.Path
		requestHeaders = r.Header.Clone()
		if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
			t.Errorf("decode request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, "{\"id\":\"msg_01\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-sonnet-5-5\",\"content\":[{\"type\":\"thinking\",\"thinking\":\"I should look this up.\",\"signature\":\"sig_native_123\"},{\"type\":\"tool_use\",\"id\":\"toolu_01\",\"name\":\"lookup\",\"input\":{\"city\":\"Paris\"}}],\"stop_reason\":\"tool_use\",\"stop_sequence\":null,\"usage\":{\"input_tokens\":17,\"output_tokens\":23}}")
	}))
	defer server.Close()

	t.Setenv("ANTHROPIC_API_KEY", "ambient-key-must-not-be-used")
	t.Setenv("ANTHROPIC_BASE_URL", "https://ambient.example.test/v1")
	chatModel, err := New(context.Background(), Config{
		BaseURL:     "https://api.anthropic.com/v1/",
		Model:       "claude-sonnet-5-5",
		APIKey:      "explicit-token",
		WorkspaceID: " wrkspc_test ",
		Effort:      "high",
		HTTPClient:  rewriteClient(server.URL),
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	withTools, err := chatModel.WithTools([]*schema.ToolInfo{lookupTool()})
	if err != nil {
		t.Fatalf("WithTools() error = %v", err)
	}
	got, err := withTools.Generate(context.Background(), []*schema.Message{schema.UserMessage("Find the weather in Paris.")})
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}

	if requestPath != "/v1/messages" {
		t.Errorf("request path = %q, want /v1/messages", requestPath)
	}
	if got := requestHeaders.Get("Authorization"); got != "Bearer explicit-token" {
		t.Errorf("Authorization = %q, want explicit bearer token", got)
	}
	if got := requestHeaders.Get("X-Api-Key"); got != "" {
		t.Errorf("X-Api-Key = %q, want empty", got)
	}
	if got := requestHeaders.Get("anthropic-workspace-id"); got != "wrkspc_test" {
		t.Errorf("anthropic-workspace-id = %q, want trimmed workspace ID", got)
	}
	if got := requestHeaders.Get("anthropic-version"); got != "2023-06-01" {
		t.Errorf("anthropic-version = %q, want 2023-06-01", got)
	}
	if got := requestHeaders.Get("anthropic-beta"); got != thinkingBindingControlsBeta {
		t.Errorf("anthropic-beta = %q, want thinking binding controls", got)
	}
	if got := requestBody["model"]; got != "claude-sonnet-5-5" {
		t.Errorf("request model = %v, want claude-sonnet-5-5", got)
	}
	if got := requestBody["max_tokens"]; got != float64(DefaultMaxTokens) {
		t.Errorf("max_tokens = %v, want default %d", got, DefaultMaxTokens)
	}
	if _, ok := requestBody["temperature"]; ok {
		t.Errorf("request unexpectedly includes temperature: %v", requestBody["temperature"])
	}
	outputConfig, ok := requestBody["output_config"].(map[string]any)
	if !ok || outputConfig["effort"] != "high" {
		t.Errorf("output_config = %#v, want native effort high", requestBody["output_config"])
	}
	assertAdaptiveThinkingBinding(t, requestBody)
	tools, ok := requestBody["tools"].([]any)
	if !ok || len(tools) != 1 {
		t.Fatalf("tools = %#v, want one native tool", requestBody["tools"])
	}
	tool, ok := tools[0].(map[string]any)
	if !ok || tool["name"] != "lookup" {
		t.Fatalf("tool = %#v, want lookup", tools[0])
	}
	inputSchema, ok := tool["input_schema"].(map[string]any)
	if !ok || inputSchema["type"] != "object" {
		t.Fatalf("tool input_schema = %#v, want object schema", tool["input_schema"])
	}
	properties, ok := inputSchema["properties"].(map[string]any)
	if !ok || properties["city"] == nil {
		t.Errorf("tool input schema properties = %#v, want city", inputSchema["properties"])
	}
	if got.Content != "" {
		t.Errorf("response content = %q, want no text before tool call", got.Content)
	}
	if got.ReasoningContent != "I should look this up." {
		t.Errorf("reasoning content = %q, want native thinking block", got.ReasoningContent)
	}
	if thinking, ok := claude.GetThinking(got); !ok || thinking != "I should look this up." {
		t.Errorf("native thinking metadata = %q, %v", thinking, ok)
	}
	if got.Extra["_eino_claude_thinking_signature"] != "sig_native_123" {
		t.Errorf("thinking signature metadata = %#v", got.Extra["_eino_claude_thinking_signature"])
	}
	if len(got.ToolCalls) != 1 {
		t.Fatalf("tool calls = %#v, want one call", got.ToolCalls)
	}
	if got.ToolCalls[0].ID != "toolu_01" || got.ToolCalls[0].Function.Name != "lookup" || got.ToolCalls[0].Function.Arguments != "{\"city\":\"Paris\"}" {
		t.Errorf("tool call = %#v, want lookup with Paris argument", got.ToolCalls[0])
	}
	if got.ResponseMeta == nil || got.ResponseMeta.FinishReason != "tool_use" {
		t.Errorf("response metadata = %#v, want finish reason tool_use", got.ResponseMeta)
	}
}

func TestEffortPreservesStructuredOutputConfig(t *testing.T) {
	var requestBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
			t.Errorf("decode request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"msg_structured","type":"message","role":"assistant","model":"claude-haiku-5-5","content":[{"type":"text","text":"{}"}],"stop_reason":"end_turn","stop_sequence":null,"usage":{"input_tokens":2,"output_tokens":2}}`)
	}))
	defer server.Close()

	chatModel, err := New(context.Background(), Config{
		BaseURL:    "https://api.anthropic.com/v1",
		Model:      "claude-haiku-5-5",
		APIKey:     "test-token",
		Effort:     "high",
		HTTPClient: rewriteClient(server.URL),
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	_, err = chatModel.Generate(context.Background(), []*schema.Message{schema.UserMessage("Return JSON.")}, claude.WithResponseFormat(&claude.ResponseFormat{
		Schema: &jsonschema.Schema{Type: "object"},
	}))
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	outputConfig, ok := requestBody["output_config"].(map[string]any)
	if !ok || outputConfig["effort"] != "high" {
		t.Fatalf("output_config = %#v, want effort and structured format", requestBody["output_config"])
	}
	format, ok := outputConfig["format"].(map[string]any)
	if !ok {
		t.Fatalf("output_config.format = %#v, want preserved structured format", outputConfig["format"])
	}
	formatSchema, ok := format["schema"].(map[string]any)
	if !ok || formatSchema["type"] != "object" {
		t.Errorf("output_config.format.schema = %#v, want object schema", format["schema"])
	}
}

func TestEffortAwareMaxTokenDefaultsAndOverrides(t *testing.T) {
	var requestMaxTokens []float64
	var requestBodies []map[string]any
	var requestHeaders []http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request body: %v", err)
		}
		requestBodies = append(requestBodies, body)
		requestHeaders = append(requestHeaders, r.Header.Clone())
		maxTokens, ok := body["max_tokens"].(float64)
		if !ok {
			t.Errorf("max_tokens = %#v, want number", body["max_tokens"])
		}
		requestMaxTokens = append(requestMaxTokens, maxTokens)
		writeTextStream(w, "Done.")
	}))
	defer server.Close()

	tests := []struct {
		name          string
		effort        string
		configMax     int
		callMax       int
		wantMaxTokens int
	}{
		{name: "model default keeps effort omitted", effort: "", wantMaxTokens: DefaultMaxTokens},
		{name: "low effort", effort: "low", wantMaxTokens: DefaultMaxTokens},
		{name: "medium effort", effort: "medium", wantMaxTokens: DefaultMaxTokens},
		{name: "high effort", effort: "high", wantMaxTokens: DefaultMaxTokens},
		{name: "xhigh defaults to large token ceiling", effort: "xhigh", wantMaxTokens: DefaultMaxTokensForHighEffort},
		{name: "max defaults to large token ceiling", effort: "max", wantMaxTokens: DefaultMaxTokensForHighEffort},
		{name: "explicit config limit overrides xhigh default", effort: "xhigh", configMax: 4096, wantMaxTokens: 4096},
		{name: "Eino call limit overrides configured max", effort: "max", configMax: 8192, callMax: 2048, wantMaxTokens: 2048},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			chatModel, err := New(context.Background(), Config{
				BaseURL:    "https://api.anthropic.com/v1",
				Model:      "claude-haiku-5-5",
				APIKey:     "test-token",
				Effort:     tt.effort,
				MaxTokens:  tt.configMax,
				HTTPClient: rewriteClient(server.URL),
			})
			if err != nil {
				t.Fatalf("New() error = %v", err)
			}
			var opts []model.Option
			if tt.callMax > 0 {
				opts = append(opts, model.WithMaxTokens(tt.callMax))
			}
			reader, err := chatModel.Stream(context.Background(), []*schema.Message{schema.UserMessage("Answer briefly.")}, opts...)
			if err != nil {
				t.Fatalf("Stream() error = %v", err)
			}
			drainMessages(t, reader)
		})
	}
	if len(requestMaxTokens) != len(tests) {
		t.Fatalf("captured %d requests, want %d", len(requestMaxTokens), len(tests))
	}
	for i, tt := range tests {
		if got := int(requestMaxTokens[i]); got != tt.wantMaxTokens {
			t.Errorf("%s max_tokens = %d, want %d", tt.name, got, tt.wantMaxTokens)
		}
		if got := requestHeaders[i].Get("anthropic-beta"); got != thinkingBindingControlsBeta {
			t.Errorf("%s anthropic-beta = %q, want thinking binding controls", tt.name, got)
		}
		assertAdaptiveThinkingBinding(t, requestBodies[i])
		if _, ok := requestBodies[i]["temperature"]; ok {
			t.Errorf("%s request unexpectedly includes temperature", tt.name)
		}
		if tt.effort == "" {
			if _, ok := requestBodies[i]["output_config"]; ok {
				t.Errorf("%s output_config = %#v, want omitted model default", tt.name, requestBodies[i]["output_config"])
			}
		} else {
			outputConfig, ok := requestBodies[i]["output_config"].(map[string]any)
			if !ok || outputConfig["effort"] != tt.effort {
				t.Errorf("%s output_config = %#v, want effort %s", tt.name, requestBodies[i]["output_config"], tt.effort)
			}
		}
	}
}

func TestThinkingBindingControlsAreScopedToSupported55Models(t *testing.T) {
	var requestBodies []map[string]any
	var requestHeaders []http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request body: %v", err)
		}
		requestBodies = append(requestBodies, body)
		requestHeaders = append(requestHeaders, r.Header.Clone())
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"msg_binding_gate","type":"message","role":"assistant","model":"claude-haiku-5-5","content":[{"type":"text","text":"Done."}],"stop_reason":"end_turn","stop_sequence":null,"usage":{"input_tokens":2,"output_tokens":2}}`)
	}))
	defer server.Close()

	tests := []struct {
		modelName string
		effort    string
		wantBound bool
	}{
		{modelName: "claude-opus-5-5", wantBound: true},
		{modelName: "claude-sonnet-5-5", wantBound: true},
		{modelName: "claude-haiku-5-5", wantBound: true},
		{modelName: "claude-opus-5"},
		{modelName: "claude-opus-4-6", effort: "high"},
		{modelName: "claude-opus-4-5-20251101", effort: "high"},
	}
	for _, tt := range tests {
		t.Run(tt.modelName, func(t *testing.T) {
			chatModel, err := New(context.Background(), Config{
				BaseURL:    "https://api.anthropic.com/v1",
				Model:      tt.modelName,
				APIKey:     "test-token",
				Effort:     tt.effort,
				HTTPClient: rewriteClient(server.URL),
			})
			if err != nil {
				t.Fatalf("New() error = %v", err)
			}
			if _, err := chatModel.Generate(context.Background(), []*schema.Message{schema.UserMessage("Answer briefly.")}); err != nil {
				t.Fatalf("Generate() error = %v", err)
			}
		})
	}
	if len(requestBodies) != len(tests) {
		t.Fatalf("captured %d requests, want %d", len(requestBodies), len(tests))
	}
	for i, tt := range tests {
		gotHeader := requestHeaders[i].Get("anthropic-beta")
		if tt.wantBound {
			if gotHeader != thinkingBindingControlsBeta {
				t.Errorf("%s anthropic-beta = %q, want thinking binding controls", tt.modelName, gotHeader)
			}
			assertAdaptiveThinkingBinding(t, requestBodies[i])
			continue
		}
		if gotHeader != "" {
			t.Errorf("%s anthropic-beta = %q, want no binding beta", tt.modelName, gotHeader)
		}
		thinking, _ := requestBodies[i]["thinking"].(map[string]any)
		if thinking["block_binding"] != nil {
			t.Errorf("%s thinking unexpectedly contains block_binding: %#v", tt.modelName, thinking)
		}
	}
}

func TestGenerateStreamsForEffectiveTokenLimitsAndAggregatesResponse(t *testing.T) {
	var requestBodies []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request body: %v", err)
		}
		requestBodies = append(requestBodies, body)
		writeTextStream(w, "Done.")
	}))
	defer server.Close()

	tests := []struct {
		name          string
		modelName     string
		effort        string
		configMax     int
		callMax       int
		wantMaxTokens int
	}{
		{
			name:          "xhigh default",
			modelName:     "claude-haiku-5-5",
			effort:        "xhigh",
			wantMaxTokens: DefaultMaxTokensForHighEffort,
		},
		{
			name:          "per-call override",
			modelName:     "claude-haiku-5-5",
			effort:        "high",
			callMax:       65536,
			wantMaxTokens: 65536,
		},
		{
			name:          "config override",
			modelName:     "claude-haiku-5-5",
			effort:        "high",
			configMax:     65536,
			wantMaxTokens: 65536,
		},
		{
			name:          "native SDK model-specific limit",
			modelName:     "claude-opus-4-20250514",
			wantMaxTokens: DefaultMaxTokens,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			chatModel, err := New(context.Background(), Config{
				BaseURL:    "https://api.anthropic.com/v1",
				Model:      tt.modelName,
				APIKey:     "test-token",
				Effort:     tt.effort,
				MaxTokens:  tt.configMax,
				HTTPClient: rewriteClient(server.URL),
			})
			if err != nil {
				t.Fatalf("New() error = %v", err)
			}
			var opts []model.Option
			if tt.callMax > 0 {
				opts = append(opts, model.WithMaxTokens(tt.callMax))
			}
			got, err := chatModel.Generate(context.Background(), []*schema.Message{schema.UserMessage("Answer briefly.")}, opts...)
			if err != nil {
				t.Fatalf("Generate() error = %v", err)
			}
			if got.Content != "Done." {
				t.Errorf("Generate() content = %q, want aggregated stream text", got.Content)
			}
			if got.ResponseMeta == nil || got.ResponseMeta.FinishReason != "end_turn" {
				t.Errorf("Generate() response metadata = %#v, want end_turn", got.ResponseMeta)
			}
		})
	}
	if len(requestBodies) != len(tests) {
		t.Fatalf("captured %d requests, want %d", len(requestBodies), len(tests))
	}
	for i, tt := range tests {
		body := requestBodies[i]
		if body["stream"] != true {
			t.Errorf("%s stream = %#v, want true", tt.name, body["stream"])
		}
		gotMaxTokens, ok := body["max_tokens"].(float64)
		if !ok {
			t.Errorf("%s max_tokens = %#v, want number", tt.name, body["max_tokens"])
			continue
		}
		if got := int(gotMaxTokens); got != tt.wantMaxTokens {
			t.Errorf("%s max_tokens = %d, want %d", tt.name, got, tt.wantMaxTokens)
		}
	}
}

func TestGenerateStreamingFallbackAggregatesToolCalls(t *testing.T) {
	var requestBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
			t.Errorf("decode request body: %v", err)
		}
		writeToolCallStream(w)
	}))
	defer server.Close()

	chatModel, err := New(context.Background(), Config{
		BaseURL:    "https://api.anthropic.com/v1",
		Model:      "claude-haiku-5-5",
		APIKey:     "test-token",
		Effort:     "max",
		HTTPClient: rewriteClient(server.URL),
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	withTools, err := chatModel.WithTools([]*schema.ToolInfo{lookupTool()})
	if err != nil {
		t.Fatalf("WithTools() error = %v", err)
	}
	got, err := withTools.Generate(context.Background(), []*schema.Message{schema.UserMessage("Weather in Rome?")})
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if requestBody["stream"] != true {
		t.Errorf("request stream = %#v, want true", requestBody["stream"])
	}
	if len(got.ToolCalls) != 1 {
		t.Fatalf("Generate() tool calls = %#v, want one call", got.ToolCalls)
	}
	call := got.ToolCalls[0]
	if call.ID != "toolu_large" || call.Function.Name != "lookup" || call.Function.Arguments != `{"city":"Rome"}` {
		t.Errorf("Generate() tool call = %#v, want lookup with Rome argument", call)
	}
	if got.ResponseMeta == nil || got.ResponseMeta.FinishReason != "tool_use" {
		t.Errorf("Generate() response metadata = %#v, want tool_use", got.ResponseMeta)
	}
}

func TestStreamPreservesThinkingSignatureAndToolCalls(t *testing.T) {
	var requestBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
			t.Errorf("decode request body: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		writer := bufio.NewWriter(w)
		events := []struct {
			name string
			data string
		}{
			{"message_start", "{\"type\":\"message_start\",\"message\":{\"id\":\"msg_stream\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-sonnet-5-5\",\"content\":[],\"stop_reason\":null,\"stop_sequence\":null,\"usage\":{\"input_tokens\":3,\"output_tokens\":0}}}"},
			{"content_block_start", "{\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"thinking\",\"thinking\":\"\"}}"},
			{"content_block_delta", "{\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"thinking_delta\",\"thinking\":\"consider the request\"}}"},
			{"content_block_delta", "{\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"signature_delta\",\"signature\":\"sig_stream_456\"}}"},
			{"content_block_stop", "{\"type\":\"content_block_stop\",\"index\":0}"},
			{"content_block_start", "{\"type\":\"content_block_start\",\"index\":1,\"content_block\":{\"type\":\"tool_use\",\"id\":\"toolu_stream\",\"name\":\"lookup\",\"input\":{}}}"},
			{"content_block_delta", "{\"type\":\"content_block_delta\",\"index\":1,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"city\\\":\\\"Rome\\\"}\"}}"},
			{"content_block_stop", "{\"type\":\"content_block_stop\",\"index\":1}"},
			{"message_delta", "{\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\",\"stop_sequence\":null},\"usage\":{\"output_tokens\":8}}"},
			{"message_stop", "{\"type\":\"message_stop\"}"},
		}
		for _, event := range events {
			if _, err := fmt.Fprintf(writer, "event: %s\ndata: %s\n\n", event.name, event.data); err != nil {
				t.Errorf("write stream event: %v", err)
				return
			}
		}
		if err := writer.Flush(); err != nil {
			t.Errorf("flush stream: %v", err)
		}
	}))
	defer server.Close()

	chatModel, err := New(context.Background(), Config{
		BaseURL:    "https://api.anthropic.com/v1",
		Model:      "claude-sonnet-5-5",
		APIKey:     "test-token",
		HTTPClient: rewriteClient(server.URL),
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	withTools, err := chatModel.WithTools([]*schema.ToolInfo{lookupTool()})
	if err != nil {
		t.Fatalf("WithTools() error = %v", err)
	}
	reader, err := withTools.Stream(context.Background(), []*schema.Message{schema.UserMessage("Find the weather in Rome.")}, model.WithMaxTokens(4096))
	if err != nil {
		t.Fatalf("Stream() error = %v", err)
	}
	defer reader.Close()

	var chunks []*schema.Message
	for {
		chunk, err := reader.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("Recv() error = %v", err)
		}
		chunks = append(chunks, chunk)
	}
	if len(chunks) == 0 {
		t.Fatal("stream returned no message chunks")
	}
	combined, err := schema.ConcatMessages(chunks)
	if err != nil {
		t.Fatalf("ConcatMessages() error = %v", err)
	}
	if combined.ReasoningContent != "consider the request" {
		t.Errorf("stream reasoning content = %q", combined.ReasoningContent)
	}
	if thinking, ok := claude.GetThinking(combined); !ok || thinking != "consider the request" {
		t.Errorf("stream native thinking metadata = %q, %v", thinking, ok)
	}
	if got := combined.Extra["_eino_claude_thinking_signature"]; got != "sig_stream_456" {
		t.Errorf("stream thinking signature metadata = %#v", got)
	}
	if len(combined.ToolCalls) != 1 {
		t.Fatalf("stream tool calls = %#v, want one call", combined.ToolCalls)
	}
	call := combined.ToolCalls[0]
	if call.ID != "toolu_stream" || call.Function.Name != "lookup" || call.Function.Arguments != "{\"city\":\"Rome\"}" {
		t.Errorf("stream tool call = %#v", call)
	}
	if combined.ResponseMeta == nil || combined.ResponseMeta.FinishReason != "tool_use" {
		t.Errorf("stream response metadata = %#v, want tool_use finish reason", combined.ResponseMeta)
	}
	if got := requestBody["max_tokens"]; got != float64(4096) {
		t.Errorf("stream max_tokens = %v, want Eino override 4096", got)
	}
}

func TestGenerateReplaysThinkingSignatureAndToolResult(t *testing.T) {
	var calls int
	var replayBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		if calls == 1 {
			_, _ = io.WriteString(w, "{\"id\":\"msg_replay_1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-sonnet-5-5\",\"content\":[{\"type\":\"thinking\",\"thinking\":\"I need a current forecast.\",\"signature\":\"sig_replay_789\"},{\"type\":\"tool_use\",\"id\":\"toolu_replay\",\"name\":\"lookup\",\"input\":{\"city\":\"Rome\"}}],\"stop_reason\":\"tool_use\",\"stop_sequence\":null,\"usage\":{\"input_tokens\":7,\"output_tokens\":11}}")
			return
		}
		replayBody = body
		_, _ = io.WriteString(w, "{\"id\":\"msg_replay_2\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-sonnet-5-5\",\"content\":[{\"type\":\"text\",\"text\":\"It is sunny.\"}],\"stop_reason\":\"end_turn\",\"stop_sequence\":null,\"usage\":{\"input_tokens\":12,\"output_tokens\":4}}")
	}))
	defer server.Close()

	chatModel, err := New(context.Background(), Config{
		BaseURL:    "https://api.anthropic.com/v1",
		Model:      "claude-sonnet-5-5",
		APIKey:     "test-token",
		HTTPClient: rewriteClient(server.URL),
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	withTools, err := chatModel.WithTools([]*schema.ToolInfo{lookupTool()})
	if err != nil {
		t.Fatalf("WithTools() error = %v", err)
	}
	first, err := withTools.Generate(context.Background(), []*schema.Message{schema.UserMessage("Weather in Rome?")})
	if err != nil {
		t.Fatalf("first Generate() error = %v", err)
	}
	if thinking, ok := claude.GetThinking(first); !ok || thinking != "I need a current forecast." {
		t.Fatalf("first response thinking = %q, %v", thinking, ok)
	}
	if first.Extra["_eino_claude_thinking_signature"] != "sig_replay_789" {
		t.Fatalf("first response signature = %#v", first.Extra["_eino_claude_thinking_signature"])
	}

	_, err = withTools.Generate(context.Background(), []*schema.Message{
		schema.UserMessage("Weather in Rome?"),
		first,
		{Role: schema.Tool, ToolCallID: "toolu_replay", ToolName: "lookup", Content: "Sunny, 22 C."},
	})
	if err != nil {
		t.Fatalf("second Generate() error = %v", err)
	}
	if calls != 2 {
		t.Fatalf("request count = %d, want two turns", calls)
	}
	messages, ok := replayBody["messages"].([]any)
	if !ok || len(messages) != 3 {
		t.Fatalf("replayed messages = %#v, want user, assistant, tool result", replayBody["messages"])
	}
	assistant, ok := messages[1].(map[string]any)
	if !ok || assistant["role"] != "assistant" {
		t.Fatalf("replayed assistant message = %#v", messages[1])
	}
	blocks, ok := assistant["content"].([]any)
	if !ok || len(blocks) != 2 {
		t.Fatalf("replayed assistant content = %#v, want thinking and tool blocks", assistant["content"])
	}
	thinkingBlock, ok := blocks[0].(map[string]any)
	if !ok || thinkingBlock["type"] != "thinking" || thinkingBlock["thinking"] != "I need a current forecast." || thinkingBlock["signature"] != "sig_replay_789" {
		t.Errorf("replayed thinking block = %#v", blocks[0])
	}
	toolUseBlock, ok := blocks[1].(map[string]any)
	if !ok || toolUseBlock["type"] != "tool_use" || toolUseBlock["id"] != "toolu_replay" || toolUseBlock["name"] != "lookup" {
		t.Errorf("replayed tool block = %#v", blocks[1])
	}
	toolResultMessage, ok := messages[2].(map[string]any)
	if !ok || toolResultMessage["role"] != "user" {
		t.Fatalf("tool result message = %#v", messages[2])
	}
	resultBlocks, ok := toolResultMessage["content"].([]any)
	if !ok || len(resultBlocks) != 1 {
		t.Fatalf("tool result content = %#v", toolResultMessage["content"])
	}
	resultBlock, ok := resultBlocks[0].(map[string]any)
	if !ok || resultBlock["type"] != "tool_result" || resultBlock["tool_use_id"] != "toolu_replay" {
		t.Errorf("replayed tool result block = %#v", resultBlocks[0])
	}
}

func TestGenerateReplaysEmptySignedThinking(t *testing.T) {
	var calls int
	var replayBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		if calls == 1 {
			_, _ = io.WriteString(w, emptyThinkingToolResponse("sig_empty_generate"))
			return
		}
		replayBody = body
		_, _ = io.WriteString(w, "{\"id\":\"msg_empty_replay\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-haiku-5-5\",\"content\":[{\"type\":\"text\",\"text\":\"Sunny.\"}],\"stop_reason\":\"end_turn\",\"stop_sequence\":null,\"usage\":{\"input_tokens\":9,\"output_tokens\":4}}")
	}))
	defer server.Close()

	chatModel, err := New(context.Background(), Config{
		BaseURL:    "https://api.anthropic.com/v1",
		Model:      "claude-haiku-5-5",
		APIKey:     "test-token",
		HTTPClient: rewriteClient(server.URL),
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	withTools, err := chatModel.WithTools([]*schema.ToolInfo{lookupTool()})
	if err != nil {
		t.Fatalf("WithTools() error = %v", err)
	}
	first, err := withTools.Generate(context.Background(), []*schema.Message{schema.UserMessage("Weather in Rome?")})
	if err != nil {
		t.Fatalf("first Generate() error = %v", err)
	}
	if thinking, ok := claude.GetThinking(first); !ok || thinking != "" {
		t.Fatalf("first response thinking = %q, %v; want present empty string", thinking, ok)
	}
	if first.Extra[claudeThinkingSignatureExtraKey] != "sig_empty_generate" {
		t.Fatalf("first response signature = %#v", first.Extra[claudeThinkingSignatureExtraKey])
	}

	_, err = withTools.Generate(context.Background(), []*schema.Message{
		schema.UserMessage("Weather in Rome?"),
		first,
		{Role: schema.Tool, ToolCallID: "toolu_empty", ToolName: "lookup", Content: "Sunny, 22 C."},
	})
	if err != nil {
		t.Fatalf("second Generate() error = %v", err)
	}
	if calls != 2 {
		t.Fatalf("request count = %d, want two turns", calls)
	}
	assertEmptyThinkingReplay(t, replayBody, "sig_empty_generate")
}

func TestStreamReplaysEmptySignedThinking(t *testing.T) {
	var calls int
	var replayBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request body: %v", err)
		}
		if calls == 1 {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, emptyThinkingToolResponse("sig_empty_stream_request"))
			return
		}
		replayBody = body
		writeTextStream(w, "It is sunny.")
	}))
	defer server.Close()

	chatModel, err := New(context.Background(), Config{
		BaseURL:    "https://api.anthropic.com/v1",
		Model:      "claude-haiku-5-5",
		APIKey:     "test-token",
		HTTPClient: rewriteClient(server.URL),
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	withTools, err := chatModel.WithTools([]*schema.ToolInfo{lookupTool()})
	if err != nil {
		t.Fatalf("WithTools() error = %v", err)
	}
	first, err := withTools.Generate(context.Background(), []*schema.Message{schema.UserMessage("Weather in Rome?")})
	if err != nil {
		t.Fatalf("first Generate() error = %v", err)
	}
	reader, err := withTools.Stream(context.Background(), []*schema.Message{
		schema.UserMessage("Weather in Rome?"),
		first,
		{Role: schema.Tool, ToolCallID: "toolu_empty", ToolName: "lookup", Content: "Sunny, 22 C."},
	})
	if err != nil {
		t.Fatalf("second Stream() error = %v", err)
	}
	drainMessages(t, reader)

	if calls != 2 {
		t.Fatalf("request count = %d, want two turns", calls)
	}
	if replayBody["stream"] != true {
		t.Errorf("second request stream = %#v, want true", replayBody["stream"])
	}
	assertEmptyThinkingReplay(t, replayBody, "sig_empty_stream_request")
}

func TestStreamSignatureOnlyThinkingReplaysAsEmptyBlock(t *testing.T) {
	var calls int
	var replayBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request body: %v", err)
		}
		if calls == 1 {
			writeSignatureOnlyThinkingStream(w, "sig_stream_only")
			return
		}
		replayBody = body
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, "{\"id\":\"msg_stream_replay\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-haiku-5-5\",\"content\":[{\"type\":\"text\",\"text\":\"Done.\"}],\"stop_reason\":\"end_turn\",\"stop_sequence\":null,\"usage\":{\"input_tokens\":3,\"output_tokens\":2}}")
	}))
	defer server.Close()

	chatModel, err := New(context.Background(), Config{
		BaseURL:    "https://api.anthropic.com/v1",
		Model:      "claude-haiku-5-5",
		APIKey:     "test-token",
		HTTPClient: rewriteClient(server.URL),
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	reader, err := chatModel.Stream(context.Background(), []*schema.Message{schema.UserMessage("Think, then answer.")})
	if err != nil {
		t.Fatalf("first Stream() error = %v", err)
	}
	chunks := drainMessages(t, reader)
	if len(chunks) == 0 {
		t.Fatal("first stream returned no chunks")
	}
	assistant, err := schema.ConcatMessages(chunks)
	if err != nil {
		t.Fatalf("ConcatMessages() error = %v", err)
	}
	if signature := assistant.Extra[claudeThinkingSignatureExtraKey]; signature != "sig_stream_only" {
		t.Fatalf("stream signature metadata = %#v", signature)
	}
	if assistant.ReasoningContent != "" {
		t.Fatalf("stream reasoning content = %q, want empty", assistant.ReasoningContent)
	}
	// A signature_delta can survive stream aggregation without the matching
	// thinking Extra key when Anthropic emitted no thinking_delta.
	assistant.Extra = map[string]any{claudeThinkingSignatureExtraKey: "sig_stream_only"}

	if _, err := chatModel.Generate(context.Background(), []*schema.Message{
		schema.UserMessage("Think, then answer."),
		assistant,
		schema.UserMessage("What is the answer?"),
	}); err != nil {
		t.Fatalf("second Generate() error = %v", err)
	}
	if calls != 2 {
		t.Fatalf("request count = %d, want two turns", calls)
	}
	assertEmptyThinkingReplay(t, replayBody, "sig_stream_only")
}

func TestSignedThinkingReplayMapsOrRejectsAdjacentAssistantMessages(t *testing.T) {
	first := &schema.Message{Role: schema.Assistant, Content: "First assistant message."}
	second := &schema.Message{
		Role: schema.Assistant,
		Extra: map[string]any{
			claudeThinkingExtraKey:          "",
			claudeThinkingSignatureExtraKey: "sig_adjacent",
		},
	}
	ctx, err := withSignedThinkingReplay(context.Background(), []*schema.Message{first, second})
	if err != nil {
		t.Fatalf("withSignedThinkingReplay() error = %v", err)
	}
	replay, ok := ctx.Value(signedThinkingReplayContextKey{}).(signedThinkingReplayContext)
	if !ok {
		t.Fatal("signed thinking context was not set")
	}

	body := []byte("{\"messages\":[{\"role\":\"assistant\",\"content\":[{\"type\":\"text\",\"text\":\"First assistant message.\"}]},{\"role\":\"assistant\",\"content\":[{\"type\":\"text\",\"text\":\"\"}]}]}")
	restored, changed, err := restoreSignedThinking(body, replay)
	if err != nil {
		t.Fatalf("restoreSignedThinking() error = %v", err)
	}
	if !changed {
		t.Fatal("restoreSignedThinking() did not restore adjacent assistant block")
	}
	var restoredBody map[string]any
	if err := json.Unmarshal(restored, &restoredBody); err != nil {
		t.Fatalf("decode restored body: %v", err)
	}
	messages := restoredBody["messages"].([]any)
	secondContent := messages[1].(map[string]any)["content"].([]any)
	secondThinking := secondContent[0].(map[string]any)
	if secondThinking["type"] != "thinking" || secondThinking["thinking"] != "" || secondThinking["signature"] != "sig_adjacent" {
		t.Errorf("second assistant content = %#v", secondContent)
	}

	mergedBody := []byte("{\"messages\":[{\"role\":\"assistant\",\"content\":[{\"type\":\"text\",\"text\":\"First assistant message.\"}]}]}")
	if _, _, err := restoreSignedThinking(mergedBody, replay); err == nil {
		t.Fatal("restoreSignedThinking() accepted an ambiguous merged assistant position")
	}
}

func TestRestoreSignedThinkingRejectsMismatchedExistingBlocks(t *testing.T) {
	replay := assistantThinkingReplay{thinking: "", signature: "sig_saved"}
	tests := []struct {
		name    string
		content string
		wantErr bool
	}{
		{
			name:    "exact existing native block is preserved",
			content: `[{"type":"thinking","thinking":"","signature":"sig_saved"},{"type":"tool_use","id":"toolu_1","name":"lookup","input":{}}]`,
		},
		{
			name:    "different signature fails closed",
			content: `[{"type":"thinking","thinking":"","signature":"sig_other"}]`,
			wantErr: true,
		},
		{
			name:    "different thinking text fails closed",
			content: `[{"type":"thinking","thinking":"changed","signature":"sig_saved"}]`,
			wantErr: true,
		},
		{
			name:    "missing signature fails closed",
			content: `[{"type":"thinking","thinking":""}]`,
			wantErr: true,
		},
		{
			name:    "missing thinking text fails closed",
			content: `[{"type":"thinking","signature":"sig_saved"}]`,
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			message := map[string]json.RawMessage{"content": json.RawMessage(tt.content)}
			_, changed, err := restoreThinkingInAssistant(message, replay)
			if (err != nil) != tt.wantErr {
				t.Fatalf("restoreThinkingInAssistant() error = %v, want error %v", err, tt.wantErr)
			}
			if err == nil && changed {
				t.Fatal("restoreThinkingInAssistant() changed a message with an existing matching block")
			}
		})
	}
}

func TestSignedThinkingReplayPreservesMaximumSignature(t *testing.T) {
	signature := strings.Repeat("s", maxThinkingSignatureBytes)
	message := &schema.Message{
		Role: schema.Assistant,
		Extra: map[string]any{
			claudeThinkingExtraKey:          "",
			claudeThinkingSignatureExtraKey: signature,
		},
	}
	ctx, err := withSignedThinkingReplay(context.Background(), []*schema.Message{message})
	if err != nil {
		t.Fatalf("withSignedThinkingReplay() error = %v", err)
	}
	replay := ctx.Value(signedThinkingReplayContextKey{}).(signedThinkingReplayContext)
	body := []byte("{\"messages\":[{\"role\":\"assistant\",\"content\":[{\"type\":\"text\",\"text\":\"\"}]}]}")
	restored, changed, err := restoreSignedThinking(body, replay)
	if err != nil {
		t.Fatalf("restoreSignedThinking() error = %v", err)
	}
	if !changed {
		t.Fatal("restoreSignedThinking() did not insert the signed thinking block")
	}
	var request map[string]any
	if err := json.Unmarshal(restored, &request); err != nil {
		t.Fatalf("decode restored request: %v", err)
	}
	assistant := request["messages"].([]any)[0].(map[string]any)
	blocks := assistant["content"].([]any)
	thinking := blocks[0].(map[string]any)
	got, ok := thinking["signature"].(string)
	if !ok || got != signature {
		t.Fatalf("restored signature does not equal the complete %d-byte signature", len(signature))
	}

	message.Extra[claudeThinkingSignatureExtraKey] = signature + "s"
	if _, err := withSignedThinkingReplay(context.Background(), []*schema.Message{message}); err == nil {
		t.Fatal("withSignedThinkingReplay() accepted signature larger than its limit")
	}
}

func TestEffortConfigEnablesThinkingForOlderModels(t *testing.T) {
	tests := []struct {
		name      string
		modelName string
		wantMode  thinkingMode
	}{
		{"Opus 4.6 uses adaptive thinking", "claude-opus-4-6", thinkingAdaptive},
		{"Sonnet 4.6 uses adaptive thinking", "claude-sonnet-4-6", thinkingAdaptive},
		{"Opus 4.7 uses adaptive thinking", "claude-opus-4-7", thinkingAdaptive},
		{"Opus 4.8 uses adaptive thinking", "claude-opus-4-8", thinkingAdaptive},
		{"Opus 4.5 uses extended thinking", "claude-opus-4-5-20251101", thinkingExtended},
		{"Haiku 5.5 uses its adaptive default", "claude-haiku-5-5", thinkingNone},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := nativeThinkingMode(tt.modelName, "high"); got != tt.wantMode {
				t.Errorf("nativeThinkingMode() = %v, want %v", got, tt.wantMode)
			}
		})
	}
	if got := nativeThinkingMode("claude-opus-4-5", ""); got != thinkingNone {
		t.Errorf("empty Opus 4.5 effort mode = %v, want no forced thinking", got)
	}
}

func TestNewValidatesEffortEndpointAndCredentials(t *testing.T) {
	base := Config{BaseURL: "https://api.anthropic.com/v1", Model: "claude-sonnet-5-5", APIKey: "test-token"}
	tests := []struct {
		name string
		cfg  Config
		want bool
	}{
		{name: "known official effort", cfg: withEffort(base, "high"), want: true},
		{name: "empty effort keeps native default", cfg: base, want: true},
		{name: "unsupported official effort", cfg: withEffort(base, "none")},
		{name: "custom endpoint is not native Anthropic", cfg: withBaseAndEffort(base, "https://gateway.example.test/v1", "high")},
		{name: "custom endpoint without effort is not native Anthropic", cfg: withBaseURL(base, "https://gateway.example.test/v1")},
		{name: "missing key", cfg: Config{BaseURL: base.BaseURL, Model: base.Model}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := New(context.Background(), tt.cfg)
			if (err == nil) != tt.want {
				t.Fatalf("New() error = %v, want success %v", err, tt.want)
			}
		})
	}
}

func TestUsesEndpoint(t *testing.T) {
	tests := []struct {
		baseURL string
		want    bool
	}{
		{"https://api.anthropic.com/v1", true},
		{" HTTPS://API.ANTHROPIC.COM:443/v1/ ", true},
		{"https://api.anthropic.com:8443/v1", false},
		{"https://api.anthropic.com:/v1", false},
		{"https://api.anthropic.com:0443/v1", false},
		{"https://api.anthropic.com:abc/v1", false},
		{"http://api.anthropic.com/v1", false},
		{"https://api.anthropic.com.example.test/v1", false},
		{"https://gateway.api.anthropic.com/v1", false},
		{"https://api.anthropic.com/v1/messages", false},
		{"https://api.anthropic.com/%76%31", false},
		{"https://api.anthropic.com./v1", false},
		{"https://user@api.anthropic.com/v1", false},
		{"https://api.anthropic.com/v1?proxy=1", false},
		{"https://api.anthropic.com/v1#fragment", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(tt.baseURL, func(t *testing.T) {
			if got := UsesEndpoint(tt.baseURL); got != tt.want {
				t.Errorf("UsesEndpoint(%q) = %v, want %v", tt.baseURL, got, tt.want)
			}
		})
	}
}

func TestOpus45SizesThinkingBudgetForTokenOverride(t *testing.T) {
	var requestBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
			t.Errorf("decode request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, "{\"id\":\"msg_opus45\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-opus-4-5-20251101\",\"content\":[{\"type\":\"text\",\"text\":\"Done.\"}],\"stop_reason\":\"end_turn\",\"stop_sequence\":null,\"usage\":{\"input_tokens\":2,\"output_tokens\":3}}")
	}))
	defer server.Close()

	chatModel, err := New(context.Background(), Config{
		BaseURL:    "https://api.anthropic.com/v1",
		Model:      "claude-opus-4-5-20251101",
		APIKey:     "test-token",
		Effort:     "high",
		HTTPClient: rewriteClient(server.URL),
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if _, err := chatModel.Generate(context.Background(), []*schema.Message{schema.UserMessage("reason carefully")}, model.WithMaxTokens(4096)); err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	thinking, ok := requestBody["thinking"].(map[string]any)
	if !ok || thinking["type"] != "enabled" || thinking["budget_tokens"] != float64(3072) {
		t.Errorf("thinking = %#v, want enabled with budget below max_tokens", requestBody["thinking"])
	}
	outputConfig, ok := requestBody["output_config"].(map[string]any)
	if !ok || outputConfig["effort"] != "high" {
		t.Errorf("output_config = %#v, want high effort", requestBody["output_config"])
	}

	_, err = chatModel.Generate(context.Background(), []*schema.Message{schema.UserMessage("too small")}, model.WithMaxTokens(minimumThinkingBudget+minimumThinkingOutput-1))
	if err == nil {
		t.Fatal("Opus 4.5 accepted max_tokens without room for the minimum thinking and response budgets")
	}
}

func TestRedirectDoesNotForwardCredentialsAcrossOrigins(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		http.Redirect(w, r, "https://collector.example.test/steal", http.StatusFound)
	}))
	defer server.Close()

	chatModel, err := New(context.Background(), Config{
		BaseURL:    "https://api.anthropic.com/v1",
		Model:      "claude-sonnet-5-5",
		APIKey:     "sensitive-token",
		HTTPClient: rewriteClient(server.URL),
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	_, err = chatModel.Generate(context.Background(), []*schema.Message{schema.UserMessage("hello")})
	if err == nil {
		t.Fatal("Generate() unexpectedly accepted cross-origin redirect response")
	}
	if calls != 1 {
		t.Errorf("HTTP transport received %d requests, want one before redirect rejection", calls)
	}
}

func lookupTool() *schema.ToolInfo {
	return &schema.ToolInfo{
		Name: "lookup",
		Desc: "Look up current weather by city.",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"city": {Type: schema.String, Desc: "City name.", Required: true},
		}),
	}
}

func emptyThinkingToolResponse(signature string) string {
	return fmt.Sprintf(
		"{\"id\":\"msg_empty_thinking\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-haiku-5-5\",\"content\":[{\"type\":\"thinking\",\"thinking\":\"\",\"signature\":%q},{\"type\":\"tool_use\",\"id\":\"toolu_empty\",\"name\":\"lookup\",\"input\":{\"city\":\"Rome\"}}],\"stop_reason\":\"tool_use\",\"stop_sequence\":null,\"usage\":{\"input_tokens\":7,\"output_tokens\":9}}",
		signature,
	)
}

func assertEmptyThinkingReplay(t *testing.T, body map[string]any, signature string) {
	t.Helper()
	messages, ok := body["messages"].([]any)
	if !ok || len(messages) < 2 {
		t.Fatalf("replayed messages = %#v, want user and assistant", body["messages"])
	}
	assistant, ok := messages[1].(map[string]any)
	if !ok || assistant["role"] != "assistant" {
		t.Fatalf("replayed assistant message = %#v", messages[1])
	}
	blocks, ok := assistant["content"].([]any)
	if !ok || len(blocks) == 0 {
		t.Fatalf("replayed assistant content = %#v", assistant["content"])
	}
	thinking, ok := blocks[0].(map[string]any)
	if !ok || thinking["type"] != "thinking" || thinking["thinking"] != "" || thinking["signature"] != signature {
		t.Errorf("replayed first assistant block = %#v, want empty signed thinking block", blocks[0])
	}
}

func assertAdaptiveThinkingBinding(t *testing.T, body map[string]any) {
	t.Helper()
	thinking, ok := body["thinking"].(map[string]any)
	if !ok || thinking["type"] != "adaptive" {
		t.Errorf("thinking = %#v, want adaptive thinking", body["thinking"])
		return
	}
	binding, ok := thinking["block_binding"].(map[string]any)
	if !ok || binding["prefix_mismatch_behavior"] != "drop_block" {
		t.Errorf("thinking.block_binding = %#v, want drop_block", thinking["block_binding"])
	}
}

func writeTextStream(w http.ResponseWriter, text string) {
	w.Header().Set("Content-Type", "text/event-stream")
	writer := bufio.NewWriter(w)
	events := []struct {
		name string
		data string
	}{
		{"message_start", "{\"type\":\"message_start\",\"message\":{\"id\":\"msg_stream_text\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-haiku-5-5\",\"content\":[],\"stop_reason\":null,\"stop_sequence\":null,\"usage\":{\"input_tokens\":2,\"output_tokens\":0}}}"},
		{"content_block_start", "{\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}"},
		{"content_block_delta", fmt.Sprintf("{\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":%q}}", text)},
		{"content_block_stop", "{\"type\":\"content_block_stop\",\"index\":0}"},
		{"message_delta", "{\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\",\"stop_sequence\":null},\"usage\":{\"output_tokens\":2}}"},
		{"message_stop", "{\"type\":\"message_stop\"}"},
	}
	for _, event := range events {
		if _, err := fmt.Fprintf(writer, "event: %s\ndata: %s\n\n", event.name, event.data); err != nil {
			return
		}
	}
	_ = writer.Flush()
}

func writeToolCallStream(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	writer := bufio.NewWriter(w)
	events := []struct {
		name string
		data string
	}{
		{"message_start", `{"type":"message_start","message":{"id":"msg_large_tool","type":"message","role":"assistant","model":"claude-haiku-5-5","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":4,"output_tokens":0}}}`},
		{"content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_large","name":"lookup","input":{}}}`},
		{"content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"city\":\"Rome\"}"}}`},
		{"content_block_stop", `{"type":"content_block_stop","index":0}`},
		{"message_delta", `{"type":"message_delta","delta":{"stop_reason":"tool_use","stop_sequence":null},"usage":{"output_tokens":8}}`},
		{"message_stop", `{"type":"message_stop"}`},
	}
	for _, event := range events {
		if _, err := fmt.Fprintf(writer, "event: %s\ndata: %s\n\n", event.name, event.data); err != nil {
			return
		}
	}
	_ = writer.Flush()
}

func writeSignatureOnlyThinkingStream(w http.ResponseWriter, signature string) {
	w.Header().Set("Content-Type", "text/event-stream")
	writer := bufio.NewWriter(w)
	events := []struct {
		name string
		data string
	}{
		{"message_start", "{\"type\":\"message_start\",\"message\":{\"id\":\"msg_signature_only\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-haiku-5-5\",\"content\":[],\"stop_reason\":null,\"stop_sequence\":null,\"usage\":{\"input_tokens\":3,\"output_tokens\":0}}}"},
		{"content_block_start", "{\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"thinking\",\"thinking\":\"\"}}"},
		{"content_block_delta", fmt.Sprintf("{\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"signature_delta\",\"signature\":%q}}", signature)},
		{"content_block_stop", "{\"type\":\"content_block_stop\",\"index\":0}"},
		{"message_delta", "{\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\",\"stop_sequence\":null},\"usage\":{\"output_tokens\":4}}"},
		{"message_stop", "{\"type\":\"message_stop\"}"},
	}
	for _, event := range events {
		if _, err := fmt.Fprintf(writer, "event: %s\ndata: %s\n\n", event.name, event.data); err != nil {
			return
		}
	}
	_ = writer.Flush()
}

func drainMessages(t *testing.T, reader *schema.StreamReader[*schema.Message]) []*schema.Message {
	t.Helper()
	defer reader.Close()
	var messages []*schema.Message
	for {
		message, err := reader.Recv()
		if err == io.EOF {
			return messages
		}
		if err != nil {
			t.Fatalf("Recv() error = %v", err)
		}
		messages = append(messages, message)
	}
}

func withEffort(cfg Config, effort string) Config {
	cfg.Effort = effort
	return cfg
}

func withBaseAndEffort(cfg Config, baseURL, effort string) Config {
	cfg.BaseURL = baseURL
	cfg.Effort = effort
	return cfg
}

func withBaseURL(cfg Config, baseURL string) Config {
	cfg.BaseURL = baseURL
	return cfg
}

func rewriteClient(target string) *http.Client {
	targetURL, err := url.Parse(target)
	if err != nil {
		panic(err)
	}
	return &http.Client{Transport: rewriteTransport{target: targetURL}}
}

type rewriteTransport struct {
	target *url.URL
}

func (t rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	rewritten := req.Clone(req.Context())
	rewritten.URL = cloneURL(req.URL)
	rewritten.URL.Scheme = t.target.Scheme
	rewritten.URL.Host = t.target.Host
	rewritten.Host = t.target.Host
	return http.DefaultTransport.RoundTrip(rewritten)
}

func cloneURL(u *url.URL) *url.URL {
	cloned := *u
	return &cloned
}
