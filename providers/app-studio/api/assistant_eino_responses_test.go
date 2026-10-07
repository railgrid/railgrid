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
	"encoding/base64"
	"encoding/gob"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/components"
	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

const projectResponsesFixtureText = `{"id":"resp_1","object":"response","status":"completed","model":"gpt-6.1-sol","output":[{"id":"msg_1","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"OK","annotations":[]}]}],"usage":{"input_tokens":12,"output_tokens":5,"total_tokens":17,"input_tokens_details":{"cached_tokens":3},"output_tokens_details":{"reasoning_tokens":2}}}`

func projectResponsesFixtureModel(t *testing.T, handler http.HandlerFunc) einomodel.BaseChatModel {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	model, err := newProjectEinoChatModel(context.Background(), projectLLMSettings{
		Provider: "openai-compatible", BaseURL: server.URL + "/v1", Model: "gpt-6.1-sol", APIKey: "test-key",
	})
	if err != nil {
		t.Fatalf("create Responses model: %v", err)
	}
	return model
}

func projectResponsesFixtureRequest(t *testing.T, req *http.Request) map[string]any {
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

func TestProjectResponsesGeneratePreservesImagesCallbacksAndBudget(t *testing.T) {
	requests := make(chan map[string]any, 1)
	model := projectResponsesFixtureModel(t, func(w http.ResponseWriter, req *http.Request) {
		requests <- projectResponsesFixtureRequest(t, req)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, projectResponsesFixtureText)
	})
	var starts, ends int
	handler := callbacks.NewHandlerBuilder().OnStartFn(func(ctx context.Context, _ *callbacks.RunInfo, input callbacks.CallbackInput) context.Context {
		if value := einomodel.ConvCallbackInput(input); value != nil && len(value.Messages) == 2 {
			starts++
		}
		return ctx
	}).OnEndFn(func(ctx context.Context, _ *callbacks.RunInfo, output callbacks.CallbackOutput) context.Context {
		if value := einomodel.ConvCallbackOutput(output); value != nil && value.Message != nil && value.Message.Content == "OK" {
			ends++
		}
		return ctx
	}).Build()
	ctx := callbacks.InitCallbacks(context.Background(), &callbacks.RunInfo{Component: components.ComponentOfChatModel}, handler)
	encoded := base64.StdEncoding.EncodeToString([]byte("fixture image"))
	message := schema.UserMessage("")
	message.UserInputMultiContent = []schema.MessageInputPart{
		{Type: schema.ChatMessagePartTypeText, Text: "Inspect this screenshot."},
		{Type: schema.ChatMessagePartTypeImageURL, Image: &schema.MessageInputImage{MessagePartCommon: schema.MessagePartCommon{Base64Data: &encoded, MIMEType: "image/png"}}},
	}
	opts := append(projectMaxTokensOptions("gpt-6.1-sol", 4096), einomodel.WithTools([]*schema.ToolInfo{{Name: "get_status", Desc: "Get project status"}}))
	response, err := model.Generate(ctx, []*schema.Message{schema.SystemMessage("Be concise."), message}, opts...)
	if err != nil {
		t.Fatal(err)
	}
	if response.Content != "OK" || response.ResponseMeta == nil || response.ResponseMeta.Usage == nil || response.ResponseMeta.Usage.TotalTokens != 17 {
		t.Fatalf("response = %#v, want text and token usage", response)
	}
	if starts != 1 || ends != 1 {
		t.Fatalf("Chat callbacks = %d starts, %d ends, want one each", starts, ends)
	}
	body := <-requests
	if body["store"] != false || body["max_output_tokens"] != float64(4096) {
		t.Fatalf("Responses storage and budget = %#v", body)
	}
	tools, ok := body["tools"].([]any)
	if !ok || len(tools) != 1 {
		t.Fatalf("missing zero-argument tool: %#v", body["tools"])
	}
	tool, _ := tools[0].(map[string]any)
	parameters, _ := tool["parameters"].(map[string]any)
	if tool["strict"] != false || parameters["type"] != "object" {
		t.Fatalf("zero-argument tool needs an object schema and explicit non-strict mode: %#v", tool)
	}
	for _, forbidden := range []string{"temperature", "max_tokens", "max_completion_tokens", "reasoning_effort", "previous_response_id"} {
		if value, ok := body[forbidden]; ok {
			t.Errorf("unexpected request field %s=%v", forbidden, value)
		}
	}
	raw, _ := json.Marshal(body)
	for _, expected := range []string{"reasoning.encrypted_content", "input_image", "data:image/png;base64," + encoded, "Inspect this screenshot.", "Be concise."} {
		if !strings.Contains(string(raw), expected) {
			t.Errorf("request omitted %q: %s", expected, raw)
		}
	}
}

func projectResponsesFixtureEvent(w io.Writer, payload string) {
	_, _ = fmt.Fprintf(w, "data: %s\n\n", payload)
}

func projectResponsesFixtureToolStream(w io.Writer, terminal string) {
	projectResponsesFixtureEvent(w, `{"type":"response.created","response":{"id":"resp_tools","status":"in_progress","output":[]}}`)
	projectResponsesFixtureEvent(w, `{"type":"response.output_item.added","output_index":0,"item":{"type":"reasoning","id":"rs_1","summary":[]}}`)
	projectResponsesFixtureEvent(w, `{"type":"response.reasoning_summary_text.delta","item_id":"rs_1","output_index":0,"summary_index":0,"delta":"private reasoning summary"}`)
	projectResponsesFixtureEvent(w, `{"type":"response.output_item.done","output_index":0,"item":{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"private reasoning summary"}],"encrypted_content":"opaque-signature"}}`)
	projectResponsesFixtureEvent(w, `{"type":"response.output_item.added","output_index":1,"item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"read_file","arguments":"","status":"in_progress"}}`)
	projectResponsesFixtureEvent(w, `{"type":"response.function_call_arguments.delta","item_id":"fc_1","output_index":1,"delta":"{\"path\":"}`)
	projectResponsesFixtureEvent(w, `{"type":"response.function_call_arguments.delta","item_id":"fc_1","output_index":1,"delta":"\"README.md\"}"}`)
	projectResponsesFixtureEvent(w, `{"type":"response.output_item.done","output_index":1,"item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"read_file","arguments":"{\"path\":\"README.md\"}","status":"completed"}}`)
	if terminal != "" {
		projectResponsesFixtureEvent(w, terminal)
	}
}

func TestProjectResponsesStreamToolContinuationSurvivesCheckpoint(t *testing.T) {
	requests := make(chan map[string]any, 3)
	model := projectResponsesFixtureModel(t, func(w http.ResponseWriter, req *http.Request) {
		body := projectResponsesFixtureRequest(t, req)
		requests <- body
		if body["stream"] == true {
			w.Header().Set("Content-Type", "text/event-stream")
			projectResponsesFixtureToolStream(w, `{"type":"response.completed","response":{"id":"resp_tools","status":"completed","output":[],"usage":{"input_tokens":10,"output_tokens":4,"total_tokens":14}}}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, projectResponsesFixtureText)
	})
	ctx := context.Background()
	input := []*schema.Message{schema.UserMessage("Read README.md.")}
	runState := newProjectEinoAssistantRunState()
	recorder := &projectEinoAssistantModelCallbackRecorder{runState: runState}
	callbackDone := make(chan struct{})
	handler := callbacks.NewHandlerBuilder().OnStartFn(func(ctx context.Context, _ *callbacks.RunInfo, input callbacks.CallbackInput) context.Context {
		recorder.recordModelInput(ctx, input)
		return ctx
	}).OnEndWithStreamOutputFn(func(ctx context.Context, _ *callbacks.RunInfo, output *schema.StreamReader[callbacks.CallbackOutput]) context.Context {
		recorder.recordModelStream(ctx, output)
		close(callbackDone)
		return ctx
	}).Build()
	ctx = callbacks.InitCallbacks(ctx, &callbacks.RunInfo{Component: components.ComponentOfChatModel}, handler)
	stream, err := model.Stream(ctx, input, einomodel.WithTools([]*schema.ToolInfo{{Name: "read_file", Desc: "Read a file", ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{"path": {Type: schema.String, Required: true}, "offset": {Type: schema.Integer}})}}))
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
	response, err := schema.ConcatMessages(chunks)
	if err != nil {
		t.Fatal(err)
	}
	if response.ResponseMeta == nil || response.ResponseMeta.FinishReason == "" || len(response.ToolCalls) != 1 || response.ToolCalls[0].Function.Arguments != `{"path":"README.md"}` {
		t.Fatalf("completed streamed tool call = %#v", response)
	}
	graphMessage, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(graphMessage), "private reasoning summary") {
		t.Fatalf("Eino graph checkpoint would retain plaintext reasoning: %s", graphMessage)
	}
	var graphCheckpoint bytes.Buffer
	if err := gob.NewEncoder(&graphCheckpoint).Encode(response); err != nil {
		t.Fatalf("serialize Responses message in an Eino graph checkpoint: %v", err)
	}
	if bytes.Contains(graphCheckpoint.Bytes(), []byte("private reasoning summary")) || !bytes.Contains(graphCheckpoint.Bytes(), []byte("opaque-signature")) {
		t.Fatal("graph checkpoint must retain only opaque provider continuation")
	}
	var graphRestored schema.Message
	if err := gob.NewDecoder(&graphCheckpoint).Decode(&graphRestored); err != nil {
		t.Fatalf("restore Responses graph message: %v", err)
	}
	graphHistory := append([]*schema.Message{}, input...)
	graphHistory = append(graphHistory, &graphRestored, schema.ToolMessage("README contents", "call_1", schema.WithToolName("read_file")))
	if _, err := model.Generate(context.Background(), graphHistory); err != nil {
		t.Fatalf("generate after graph checkpoint restore: %v", err)
	}
	// Exercise the production callback projection as well as Eino's concat:
	// interrupted runs resume from the recorder's chat messages, not AgenticMessage.
	select {
	case <-callbackDone:
	case <-time.After(5 * time.Second):
		t.Fatal("stream did not invoke the Chat model completion callback")
	}
	checkpoint, err := json.Marshal(runState.ModelMessages())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(checkpoint), "private reasoning summary") || !strings.Contains(string(checkpoint), "opaque-signature") {
		t.Fatalf("checkpoint must retain opaque continuation without plaintext reasoning: %s", checkpoint)
	}
	var restored []chatMessage
	if err := json.Unmarshal(checkpoint, &restored); err != nil {
		t.Fatal(err)
	}
	continued, err := projectChatMessagesToEino(restored)
	if err != nil {
		t.Fatal(err)
	}
	continued = append(continued, schema.ToolMessage("README contents", "call_1", schema.WithToolName("read_file")))
	if _, err := model.Generate(context.Background(), continued); err != nil {
		t.Fatalf("generate after checkpoint restore: %v", err)
	}
	firstRequest := <-requests
	tools, _ := firstRequest["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("missing streamed function tool: %#v", firstRequest["tools"])
	}
	tool, _ := tools[0].(map[string]any)
	if tool["strict"] != false {
		t.Fatalf("optional tool parameters require explicit non-strict mode: %#v", tool)
	}
	for _, history := range []string{"Eino graph", "App Studio conversation"} {
		body := <-requests
		raw, _ := json.Marshal(body["input"])
		for _, expected := range []string{"opaque-signature", "rs_1", "fc_1", "function_call_output", "call_1", "README contents"} {
			if !strings.Contains(string(raw), expected) {
				t.Errorf("%s continued request omitted %q: %s", history, expected, raw)
			}
		}
		if strings.Contains(string(raw), "private reasoning summary") {
			t.Fatalf("%s continued request restored discarded plaintext reasoning: %s", history, raw)
		}
	}
}

func TestProjectResponsesStreamRejectsUnfinishedToolBatches(t *testing.T) {
	for _, test := range []struct {
		name       string
		terminal   string
		wantRetry  bool
		wantWindow bool
	}{
		{name: "transport ended before completion", wantRetry: true},
		{name: "incomplete", terminal: `{"type":"response.incomplete","response":{"id":"resp_tools","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[]}}`},
		{name: "server failure", wantRetry: true, terminal: `{"type":"response.failed","response":{"id":"resp_tools","status":"failed","error":{"code":"server_error","message":"provider failed"},"output":[]}}`},
		{name: "rate limit", wantRetry: true, terminal: `{"type":"response.failed","response":{"id":"resp_tools","status":"failed","error":{"code":"rate_limit_exceeded","message":"try later"},"output":[]}}`},
		{name: "context overflow", wantWindow: true, terminal: `{"type":"response.failed","response":{"id":"resp_tools","status":"failed","error":{"code":"context_length_exceeded","message":"input is too large"},"output":[]}}`},
		{name: "error event server failure", wantRetry: true, terminal: `{"type":"error","code":"server_error","message":"provider failed"}`},
		{name: "error event rate limit", wantRetry: true, terminal: `{"type":"error","code":"rate_limit_exceeded","message":"try later"}`},
		{name: "error event context overflow", wantWindow: true, terminal: `{"type":"error","code":"context_length_exceeded","message":"input is too large"}`},
		{name: "error event invalid prompt", terminal: `{"type":"error","code":"invalid_prompt","message":"invalid request"}`},
		{name: "SDK error envelope", wantRetry: true, terminal: `{"error":{"code":"server_error","message":"provider failed","type":"server_error"}}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			base := projectResponsesFixtureModel(t, func(w http.ResponseWriter, req *http.Request) {
				_ = projectResponsesFixtureRequest(t, req)
				w.Header().Set("Content-Type", "text/event-stream")
				projectResponsesFixtureToolStream(w, test.terminal)
			})
			model := &projectEinoAssistantBoundedModel{BaseChatModel: base, firstResponseTimeout: 5 * time.Second, streamIdleTimeout: 5 * time.Second, requireCompletion: true}
			stream, err := model.Stream(context.Background(), []*schema.Message{schema.UserMessage("Read the file.")})
			if err != nil {
				t.Fatal(err)
			}
			defer stream.Close()
			for {
				message, recvErr := stream.Recv()
				if errors.Is(recvErr, io.EOF) {
					t.Fatal("unfinished tool batch ended successfully")
				}
				if recvErr != nil {
					if retry := projectEinoAssistantShouldRetryModelError(recvErr); retry != test.wantRetry {
						t.Errorf("retry = %v, want %v: %v", retry, test.wantRetry, recvErr)
					}
					if overflow := projectEinoAssistantContextWindowExceeded(recvErr); overflow != test.wantWindow {
						t.Errorf("context overflow = %v, want %v: %v", overflow, test.wantWindow, recvErr)
					}
					break
				}
				if message != nil && message.ResponseMeta != nil && message.ResponseMeta.FinishReason != "" {
					t.Fatalf("unfinished tool batch received completion marker %q", message.ResponseMeta.FinishReason)
				}
			}
		})
	}
}

func TestProjectResponsesFactoryRoutesGPT6Variants(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		_ = projectResponsesFixtureRequest(t, req)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, projectResponsesFixtureText)
	}))
	defer server.Close()
	for _, name := range []string{"gpt-6-luna", "gpt-6-sol", "gpt-6-astra", "gpt-6.1-sol", "openai/gpt-6.1-sol", "gpt-6.1-sol-2026-10-01", "gpt-6-luna-2026-10-01", "gpt6-sol"} {
		t.Run(name, func(t *testing.T) {
			model, err := newProjectEinoChatModel(context.Background(), projectLLMSettings{Provider: "openai-compatible", BaseURL: server.URL + "/v1", Model: name, APIKey: "test-key"})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := model.Generate(context.Background(), []*schema.Message{schema.UserMessage("OK")}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestProjectResponsesHTTPErrorClassification(t *testing.T) {
	for _, test := range []struct {
		name       string
		status     int
		message    string
		wantKind   projectLLMConnectionTestErrorKind
		wantRetry  bool
		wantWindow bool
	}{
		{name: "invalid parameter", status: http.StatusBadRequest, message: "unsupported parameter", wantKind: projectLLMConnectionTestRejected},
		{name: "invalid credential", status: http.StatusUnauthorized, message: "incorrect API key", wantKind: projectLLMConnectionTestRejected},
		{name: "rate limit", status: http.StatusTooManyRequests, message: "rate limit exceeded", wantKind: projectLLMConnectionTestRejected, wantRetry: true},
		{name: "upstream outage", status: http.StatusServiceUnavailable, message: "service temporarily unavailable", wantKind: projectLLMConnectionTestUpstream, wantRetry: true},
		{name: "context overflow", status: http.StatusBadRequest, message: "maximum context length exceeded", wantKind: projectLLMConnectionTestRejected, wantWindow: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			model := projectResponsesFixtureModel(t, func(w http.ResponseWriter, req *http.Request) {
				_ = projectResponsesFixtureRequest(t, req)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(test.status)
				_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"message": test.message, "type": "invalid_request_error", "code": "fixture_error"}})
			})
			ctx := context.Background()
			_, err := model.Generate(ctx, []*schema.Message{schema.UserMessage("OK")})
			if err == nil {
				t.Fatal("expected provider error")
			}
			var classified *projectLLMConnectionTestError
			if !errors.As(classifyProjectLLMConnectionTestError(ctx, err), &classified) || classified.Kind != test.wantKind {
				t.Errorf("connection error kind = %#v, want %s", classified, test.wantKind)
			}
			if retry := projectEinoAssistantShouldRetryModelError(err); retry != test.wantRetry {
				t.Errorf("retry = %v, want %v: %v", retry, test.wantRetry, err)
			}
			if overflow := projectEinoAssistantContextWindowExceeded(err); overflow != test.wantWindow {
				t.Errorf("context overflow = %v, want %v: %v", overflow, test.wantWindow, err)
			}
		})
	}
}

func TestProjectResponsesCompactionOmitsInheritedTools(t *testing.T) {
	requests := make(chan map[string]any, 1)
	base := projectResponsesFixtureModel(t, func(w http.ResponseWriter, req *http.Request) {
		requests <- projectResponsesFixtureRequest(t, req)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, projectResponsesFixtureText)
	})
	model := &projectEinoAssistantCompactionIsolatedModel{BaseChatModel: base}
	opts := append(projectMaxTokensOptions("gpt-6.1-sol", 4096), einomodel.WithTools([]*schema.ToolInfo{{Name: "read_file", Desc: "Read a file"}}))
	if _, err := model.Generate(context.Background(), []*schema.Message{schema.UserMessage(projectEinoAssistantCompactionPrompt)}, opts...); err != nil {
		t.Fatal(err)
	}
	body := <-requests
	if body["max_output_tokens"] != float64(4096) {
		t.Fatalf("compaction budget = %v", body["max_output_tokens"])
	}
	if tools, ok := body["tools"].([]any); ok && len(tools) > 0 {
		t.Fatalf("compaction inherited tools: %#v", tools)
	}
	if choice, ok := body["tool_choice"]; ok {
		t.Fatalf("compaction without tools sent tool_choice=%v", choice)
	}
}
