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
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/components"
	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

const responsesFixtureText = `{"id":"resp_1","object":"response","status":"completed","model":"gpt-6.1-sol","output":[{"id":"msg_1","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"OK","annotations":[]}]}],"usage":{"input_tokens":12,"output_tokens":5,"total_tokens":17,"input_tokens_details":{"cached_tokens":3},"output_tokens_details":{"reasoning_tokens":2}}}`

func responsesFixtureModel(t *testing.T, handler http.HandlerFunc) einomodel.BaseChatModel {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	model, err := NewResponsesModel(context.Background(), Profile{
		Provider: ProviderOpenAICompatible, BaseURL: server.URL + "/v1", Model: "gpt-6.1-sol", APIKey: "test-key",
	})
	if err != nil {
		t.Fatalf("create Responses model: %v", err)
	}
	return model
}

func responsesFixtureRequest(t *testing.T, req *http.Request) map[string]any {
	t.Helper()
	if req.Method != http.MethodPost || req.URL.Path != "/v1/responses" {
		t.Errorf("request = %s %s, want POST /v1/responses", req.Method, req.URL.Path)
	}
	var body map[string]any
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		t.Errorf("decode request: %v", err)
	}
	return body
}

func TestResponsesGenerateUsesNativeResponsesAndReportsUsage(t *testing.T) {
	requests := make(chan map[string]any, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		requests <- responsesFixtureRequest(t, req)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, responsesFixtureText)
	}))
	defer server.Close()
	model, err := NewResponsesModel(context.Background(), Profile{
		Provider: ProviderOpenAICompatible, BaseURL: server.URL + "/v1", Model: "gpt-6.1-sol", APIKey: "test-key", ReasoningEffort: "high",
	})
	if err != nil {
		t.Fatal(err)
	}
	var starts, ends, failures int
	var usage *einomodel.TokenUsage
	handler := callbacks.NewHandlerBuilder().
		OnStartFn(func(ctx context.Context, _ *callbacks.RunInfo, input callbacks.CallbackInput) context.Context {
			if value := einomodel.ConvCallbackInput(input); value != nil && len(value.Messages) == 1 {
				starts++
			}
			return ctx
		}).
		OnEndFn(func(ctx context.Context, _ *callbacks.RunInfo, output callbacks.CallbackOutput) context.Context {
			if value := einomodel.ConvCallbackOutput(output); value != nil && value.Message != nil && value.Message.Content == "OK" {
				ends++
				usage = value.TokenUsage
			}
			return ctx
		}).
		OnErrorFn(func(ctx context.Context, _ *callbacks.RunInfo, _ error) context.Context {
			failures++
			return ctx
		}).Build()
	ctx := callbacks.InitCallbacks(context.Background(), &callbacks.RunInfo{Component: components.ComponentOfChatModel}, handler)
	response, err := model.Generate(ctx, []*schema.Message{schema.UserMessage("Say OK.")}, einomodel.WithMaxTokens(4096), einomodel.WithTools([]*schema.ToolInfo{{Name: "get_status", Desc: "Get status"}}), einomodel.WithToolChoice(schema.ToolChoiceAllowed))
	if err != nil {
		t.Fatal(err)
	}
	if response.Content != "OK" || response.ResponseMeta == nil || response.ResponseMeta.Usage == nil || response.ResponseMeta.Usage.TotalTokens != 17 {
		t.Fatalf("response = %#v, want text and usage", response)
	}
	if starts != 1 || ends != 1 || failures != 0 || usage == nil || usage.TotalTokens != 17 || usage.CompletionTokensDetails.ReasoningTokens != 2 || usage.PromptTokenDetails.CachedTokens != 3 {
		t.Fatalf("callbacks = %d starts, %d ends, %d errors, usage=%#v", starts, ends, failures, usage)
	}
	body := <-requests
	if body["store"] != false || body["max_output_tokens"] != float64(4096) {
		t.Fatalf("Responses storage and output budget = %#v", body)
	}
	if reasoning, ok := body["reasoning"].(map[string]any); !ok || reasoning["effort"] != "high" {
		t.Fatalf("reasoning effort = %#v, want high", body["reasoning"])
	}
	if body["tool_choice"] != "auto" {
		t.Fatalf("tool choice = %#v, want auto", body["tool_choice"])
	}
	tools, ok := body["tools"].([]any)
	if !ok || len(tools) != 1 {
		t.Fatalf("missing function tool: %#v", body["tools"])
	}
	tool, _ := tools[0].(map[string]any)
	parameters, _ := tool["parameters"].(map[string]any)
	if tool["strict"] != false || parameters["type"] != "object" {
		t.Fatalf("zero-argument tool schema must remain non-strict: %#v", tool)
	}
}

func responsesFixtureEvent(w io.Writer, payload string) {
	_, _ = io.WriteString(w, "data: "+payload+"\n\n")
}

func responsesFixtureToolStream(w io.Writer, terminal string) {
	responsesFixtureEvent(w, `{"type":"response.created","response":{"id":"resp_tools","status":"in_progress","output":[]}}`)
	responsesFixtureEvent(w, `{"type":"response.output_item.added","output_index":0,"item":{"type":"reasoning","id":"rs_1","summary":[]}}`)
	responsesFixtureEvent(w, `{"type":"response.reasoning_summary_text.delta","item_id":"rs_1","output_index":0,"summary_index":0,"delta":"private reasoning summary"}`)
	responsesFixtureEvent(w, `{"type":"response.output_item.done","output_index":0,"item":{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"private reasoning summary"}],"encrypted_content":"opaque-signature"}}`)
	responsesFixtureEvent(w, `{"type":"response.output_item.added","output_index":1,"item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"read_file","arguments":"","status":"in_progress"}}`)
	responsesFixtureEvent(w, `{"type":"response.function_call_arguments.delta","item_id":"fc_1","output_index":1,"delta":"{\"path\":"}`)
	responsesFixtureEvent(w, `{"type":"response.function_call_arguments.delta","item_id":"fc_1","output_index":1,"delta":"\"README.md\"}"}`)
	responsesFixtureEvent(w, `{"type":"response.output_item.done","output_index":1,"item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"read_file","arguments":"{\"path\":\"README.md\"}","status":"completed"}}`)
	if terminal != "" {
		responsesFixtureEvent(w, terminal)
	}
}

func TestResponsesStreamToolCallRoundTripsOpaqueContinuation(t *testing.T) {
	requests := make(chan map[string]any, 2)
	model := responsesFixtureModel(t, func(w http.ResponseWriter, req *http.Request) {
		body := responsesFixtureRequest(t, req)
		requests <- body
		if body["stream"] == true {
			w.Header().Set("Content-Type", "text/event-stream")
			responsesFixtureToolStream(w, `{"type":"response.completed","response":{"id":"resp_tools","status":"completed","output":[],"usage":{"input_tokens":10,"output_tokens":4,"total_tokens":14}}}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, responsesFixtureText)
	})
	toolBound, ok := model.(einomodel.ToolCallingChatModel)
	if !ok {
		t.Fatal("Responses model does not support Eino WithTools")
	}
	bound, err := toolBound.WithTools([]*schema.ToolInfo{{
		Name: "read_file", Desc: "Read a file", ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{"path": {Type: schema.String, Required: true}}),
	}})
	if err != nil {
		t.Fatal(err)
	}
	stream, err := bound.Stream(context.Background(), []*schema.Message{schema.UserMessage("Read README.md.")})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	var chunks []*schema.Message
	for {
		chunk, recvErr := stream.Recv()
		if errors.Is(recvErr, io.EOF) {
			break
		}
		if recvErr != nil {
			t.Fatal(recvErr)
		}
		chunks = append(chunks, chunk)
	}
	assistant, err := schema.ConcatMessages(chunks)
	if err != nil {
		t.Fatal(err)
	}
	if assistant.ResponseMeta == nil || assistant.ResponseMeta.FinishReason != "tool_calls" || len(assistant.ToolCalls) != 1 || assistant.ToolCalls[0].Function.Arguments != `{"path":"README.md"}` {
		t.Fatalf("completed streamed tool call = %#v", assistant)
	}
	checkpoint, err := json.Marshal(assistant)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(checkpoint), "private reasoning summary") || !strings.Contains(string(checkpoint), "opaque-signature") {
		t.Fatalf("checkpoint must retain opaque continuation only: %s", checkpoint)
	}
	var restored schema.Message
	if err := json.Unmarshal(checkpoint, &restored); err != nil {
		t.Fatal(err)
	}
	calls := HydrateResponsesToolCalls(&restored)
	if len(calls) != 1 || calls[0].Extra[ResponsesToolItemIDExtraKey] != "fc_1" {
		t.Fatalf("hydrated tool call item metadata = %#v", calls)
	}
	encodedReasoning, _ := calls[0].Extra[ResponsesToolReasoningExtraKey].(string)
	if !strings.Contains(encodedReasoning, "opaque-signature") || !strings.Contains(encodedReasoning, "rs_1") {
		t.Fatalf("hydrated reasoning metadata = %q", encodedReasoning)
	}
	modelOrigin := bound.(interface{ ResponsesOrigin() string }).ResponsesOrigin()
	if got, _ := calls[0].Extra[ResponsesToolOriginExtraKey].(string); got == "" || got != modelOrigin {
		t.Fatalf("hydrated credential origin = %q, want %q", got, modelOrigin)
	}
	if _, ok := bound.(interface {
		HydrateResponsesToolCalls(*schema.Message) []schema.ToolCall
	}); ok {
	} else {
		t.Fatal("WithTools clone does not expose continuation hydration")
	}
	if _, err := bound.Generate(context.Background(), []*schema.Message{
		schema.UserMessage("Read README.md."), &restored,
		schema.ToolMessage("README contents", "call_1", schema.WithToolName("read_file")),
	}, einomodel.WithTools([]*schema.ToolInfo{{Name: "runtime_override", Desc: "Per-call override"}})); err != nil {
		t.Fatalf("continue after streamed tool result: %v", err)
	}
	firstRequest, secondRequest := <-requests, <-requests
	if firstRequest["store"] != false {
		t.Fatalf("Responses storage = %#v", firstRequest["store"])
	}
	firstTools, _ := firstRequest["tools"].([]any)
	if len(firstTools) != 1 {
		t.Fatalf("WithTools-bound tool definitions = %#v", firstRequest["tools"])
	}
	firstTool, _ := firstTools[0].(map[string]any)
	if firstTool["name"] != "read_file" {
		t.Fatalf("WithTools-bound tool was not sent: %#v", firstRequest["tools"])
	}
	runtimeTools, _ := secondRequest["tools"].([]any)
	if len(runtimeTools) != 1 {
		t.Fatalf("per-call tool definitions = %#v", secondRequest["tools"])
	}
	runtimeTool, _ := runtimeTools[0].(map[string]any)
	if len(runtimeTools) != 1 || runtimeTool["name"] != "runtime_override" {
		t.Fatalf("per-call tools must override bound tools: %#v", secondRequest["tools"])
	}
	input, _ := json.Marshal(secondRequest["input"])
	for _, expected := range []string{"opaque-signature", "rs_1", "fc_1", "function_call_output", "call_1", "README contents"} {
		if !strings.Contains(string(input), expected) {
			t.Errorf("continued Responses input omitted %q: %s", expected, input)
		}
	}
	if strings.Contains(string(input), "private reasoning summary") {
		t.Fatalf("continuation restored discarded plaintext reasoning: %s", input)
	}
}

func TestResponsesGenerateErrorInvokesCallback(t *testing.T) {
	var failures int
	model := responsesFixtureModel(t, func(w http.ResponseWriter, req *http.Request) {
		_ = responsesFixtureRequest(t, req)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":{"message":"bad credential","type":"invalid_request_error","code":"invalid_api_key"}}`)
	})
	handler := callbacks.NewHandlerBuilder().OnErrorFn(func(ctx context.Context, _ *callbacks.RunInfo, err error) context.Context {
		if err != nil {
			failures++
		}
		return ctx
	}).Build()
	ctx := callbacks.InitCallbacks(context.Background(), &callbacks.RunInfo{Component: components.ComponentOfChatModel}, handler)
	if _, err := model.Generate(ctx, []*schema.Message{schema.UserMessage("Say OK.")}); err == nil {
		t.Fatal("expected provider error")
	}
	if failures != 1 {
		t.Fatalf("OnError callbacks = %d, want one", failures)
	}
}
