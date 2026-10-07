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
	"os"
	"strings"
	"testing"
	"time"

	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// TestProjectEinoResponsesLiveToolRoundTrip is opt-in: it makes two billable
// requests using the supplied key. It exercises the same model factory,
// streaming, and durable transcript conversion as the App Studio assistant.
func TestProjectEinoResponsesLiveToolRoundTrip(t *testing.T) {
	apiKey := strings.TrimSpace(os.Getenv("RAILGRID_TEST_OPENAI_API_KEY"))
	if apiKey == "" {
		t.Skip("set RAILGRID_TEST_OPENAI_API_KEY to run the live Responses smoke test")
	}
	modelName := strings.TrimSpace(os.Getenv("RAILGRID_TEST_OPENAI_MODEL"))
	if modelName == "" {
		modelName = "gpt-6.1-sol"
	}
	if !projectModelUsesResponsesAPI(modelName) {
		t.Fatalf("model %q does not use the Responses adapter", modelName)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	model, err := newProjectEinoChatModel(ctx, projectLLMSettings{
		Provider: defaultProjectLLMProvider,
		BaseURL:  "https://api.openai.com/v1",
		Model:    modelName,
		APIKey:   apiKey,
	})
	if err != nil {
		t.Fatalf("create live model: %v", err)
	}
	const toolName = "read_smoke_value"
	parameters := map[string]*schema.ParameterInfo{
		"key": {Type: schema.String, Required: true, Enum: []string{"smoke"}},
	}
	prompt := "Read the smoke value and return it."
	expectedArguments := map[string]string{"key": "smoke"}
	if os.Getenv("RAILGRID_TEST_OPENAI_REASONING") == "1" {
		parameters["checksum"] = &schema.ParameterInfo{
			Type: schema.String, Required: true,
			Desc: "The computed checksum as a decimal string.",
		}
		prompt = "Before calling read_smoke_value, reason through (137 * 29) - (83 * 17) + 211. Pass the answer as the checksum decimal string with key smoke. Then return the tool's value."
		expectedArguments["checksum"] = "2773"
	}
	tool := &schema.ToolInfo{
		Name:        toolName,
		Desc:        "Read the value for the requested smoke-test key. This has no side effects.",
		ParamsOneOf: schema.NewParamsOneOfByParams(parameters),
	}
	// A synthetic, uncalled schema exercises optional and nested parameters
	// without sending project data or production tool descriptions upstream.
	tools := []*schema.ToolInfo{tool, {
		Name: "inspect_smoke_metadata",
		Desc: "Inspect optional metadata for a synthetic smoke test.",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"filter": {
				Type: schema.Object,
				SubParams: map[string]*schema.ParameterInfo{
					"tag":   {Type: schema.String},
					"limit": {Type: schema.Integer},
				},
			},
		}),
	}}
	history := []*schema.Message{
		schema.SystemMessage("Call read_smoke_value with key smoke exactly once. After receiving its result, reply with only the returned value, without quotes or commentary."),
		schema.UserMessage(prompt),
	}
	opts := append(projectMaxTokensOptions(modelName, 4096),
		einomodel.WithTools(tools),
		einomodel.WithToolChoice(schema.ToolChoiceForced, toolName),
	)
	stream, err := model.Stream(ctx, history, opts...)
	if err != nil {
		t.Fatalf("start live tool-call stream: %v", err)
	}
	first, err := schema.ConcatMessageStream(stream)
	if err != nil {
		t.Fatalf("consume live tool-call stream: %v", err)
	}
	if first == nil || len(first.ToolCalls) != 1 {
		t.Fatal("live model did not return exactly one tool call")
	}
	call := first.ToolCalls[0]
	if call.ID == "" || call.Function.Name != toolName {
		t.Fatal("live model returned an invalid tool-call ID or name")
	}
	var arguments map[string]string
	if err := json.Unmarshal([]byte(call.Function.Arguments), &arguments); err != nil {
		t.Fatalf("decode live tool arguments: %v", err)
	}
	if len(arguments) != len(expectedArguments) {
		t.Fatal("live model did not provide the expected smoke arguments")
	}
	for key, expected := range expectedArguments {
		if arguments[key] != expected {
			t.Fatalf("live model did not provide the expected %s argument", key)
		}
	}
	if first.ResponseMeta == nil || first.ResponseMeta.FinishReason != "tool_calls" {
		t.Fatal("live tool-call stream did not report a complete tool-call turn")
	}
	// JSON serialization covers the actual storage boundary as well as the
	// Eino/chat conversion; typed in-memory state alone is insufficient.
	history = append(history, first)
	chatHistory := projectEinoMessagesToChat(history)
	durable, err := json.Marshal(chatHistory)
	if err != nil {
		t.Fatalf("serialize live transcript: %v", err)
	}
	var persisted []chatMessage
	if err := json.Unmarshal(durable, &persisted); err != nil {
		t.Fatalf("deserialize live transcript: %v", err)
	}
	restored, err := projectChatMessagesToEino(persisted)
	if err != nil {
		t.Fatalf("restore live transcript: %v", err)
	}
	if len(restored) != len(history) || len(restored[len(restored)-1].ToolCalls) != 1 {
		t.Fatal("durable transcript lost the assistant tool call")
	}
	restoredCall := restored[len(restored)-1].ToolCalls[0]
	if len(restoredCall.Extra) == 0 {
		t.Fatal("restored live tool call has no opaque Responses continuation state")
	}
	if id, _ := restoredCall.Extra[projectEinoResponsesItemIDKey].(string); id == "" {
		t.Fatal("restored live tool call has no Responses item ID")
	}
	reasoning, _ := restoredCall.Extra[projectEinoResponsesReasoningKey].(string)
	if first.ResponseMeta.Usage != nil && first.ResponseMeta.Usage.CompletionTokensDetails.ReasoningTokens > 0 && reasoning == "" {
		t.Fatal("live model reported reasoning tokens without encrypted continuation state")
	}
	if reasoning != "" {
		var opaque []projectEinoResponsesOpaqueReasoning
		if err := json.Unmarshal([]byte(reasoning), &opaque); err != nil {
			t.Fatalf("decode opaque reasoning envelope: %v", err)
		}
		if len(opaque) == 0 {
			t.Fatal("live model returned an empty opaque reasoning envelope")
		}
		for _, item := range opaque {
			if item.EncryptedContent == "" {
				t.Fatal("live reasoning envelope contains no encrypted continuation state")
			}
		}
	}
	before, err := json.Marshal(chatHistory[len(chatHistory)-1].ToolCalls[0].ExtraContent)
	if err != nil {
		t.Fatalf("serialize original continuation state: %v", err)
	}
	after, err := json.Marshal(restoredCall.Extra)
	if err != nil {
		t.Fatalf("serialize restored continuation state: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("durable transcript changed opaque Responses continuation state")
	}

	const value = "railgrid-responses-smoke-7f93d52b"
	result, err := json.Marshal(map[string]string{"value": value})
	if err != nil {
		t.Fatalf("serialize smoke tool result: %v", err)
	}
	restored = append(restored, schema.ToolMessage(string(result), call.ID, schema.WithToolName(toolName)))
	opts = append(projectMaxTokensOptions(modelName, 4096),
		einomodel.WithTools([]*schema.ToolInfo{tool}),
		einomodel.WithToolChoice(schema.ToolChoiceForbidden),
	)
	stream, err = model.Stream(ctx, restored, opts...)
	if err != nil {
		t.Fatalf("start live tool-result continuation: %v", err)
	}
	final, err := schema.ConcatMessageStream(stream)
	if err != nil {
		t.Fatalf("consume live tool-result continuation: %v", err)
	}
	if final == nil || len(final.ToolCalls) != 0 || !strings.Contains(final.Content, value) {
		t.Fatal("live continuation did not return the tool's value without pending tool calls")
	}
	if final.ResponseMeta == nil || final.ResponseMeta.FinishReason != "stop" {
		t.Fatal("live final stream did not report completion")
	}
	if first.ResponseMeta.Usage == nil || first.ResponseMeta.Usage.TotalTokens <= 0 ||
		final.ResponseMeta.Usage == nil || final.ResponseMeta.Usage.TotalTokens <= 0 {
		t.Fatal("live Responses streams did not report token usage")
	}
	t.Logf("model=%s tool=%s durable_continuation=true encrypted_reasoning=%t first_tokens=%d final_tokens=%d",
		modelName, toolName, reasoning != "", first.ResponseMeta.Usage.TotalTokens, final.ResponseMeta.Usage.TotalTokens)
}
