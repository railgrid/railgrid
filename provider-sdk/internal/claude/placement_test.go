// Copyright 2026 The Railgrid Authors. Local copy with a goroutine error-scope fix.

/*
 * Copyright 2024 CloudWeGo Authors
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package claude

import (
	"slices"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/cloudwego/eino/schema"
)

func TestPopulateInputPlacesGuidanceAfterUserTurn(t *testing.T) {
	input := []*schema.Message{
		schema.SystemMessage("Standing instructions"),
		schema.UserMessage("Previous task"),
		schema.AssistantMessage("Previous answer", nil),
		schema.SystemMessage("Enabled skill guidance"),
		schema.SystemMessage("Skill catalog"),
		schema.UserMessage("Create a document"),
		schema.AssistantMessage("", []schema.ToolCall{
			{ID: "toolu_01", Function: schema.FunctionCall{Name: "lookup", Arguments: "{}"}},
			{ID: "toolu_02", Function: schema.FunctionCall{Name: "lookup", Arguments: "{}"}},
		}),
		schema.ToolMessage("First result", "toolu_01"),
		schema.SystemMessage("Tool continuation guidance"),
		schema.ToolMessage("Second result", "toolu_02"),
	}
	top, history, err := preProcessMessages(input)
	if err != nil {
		t.Fatal(err)
	}
	var params anthropic.MessageNewParams
	if err := (&ChatModel{}).populateInput(&params, top, history, &options{}); err != nil {
		t.Fatal(err)
	}
	var roles []anthropic.MessageParamRole
	for _, message := range params.Messages {
		roles = append(roles, message.Role)
	}
	want := []anthropic.MessageParamRole{"user", "assistant", "user", "system", "assistant", "user", "system"}
	if !slices.Equal(roles, want) {
		t.Fatalf("message roles = %v, want %v", roles, want)
	}
	if len(params.System) != 1 || params.System[0].Text != "Standing instructions" {
		t.Fatalf("top-level system = %#v", params.System)
	}
	skills := params.Messages[3].Content
	if len(skills) != 2 || skills[0].OfText == nil || skills[0].OfText.Text != "Enabled skill guidance" ||
		skills[1].OfText == nil || skills[1].OfText.Text != "Skill catalog" {
		t.Fatalf("skill guidance changed: %#v", skills)
	}
	results := params.Messages[5].Content
	if len(results) != 2 || results[0].OfToolResult == nil || results[0].OfToolResult.ToolUseID != "toolu_01" ||
		results[1].OfToolResult == nil || results[1].OfToolResult.ToolUseID != "toolu_02" {
		t.Fatalf("tool results were split or reordered: %#v", results)
	}
	if input[3].Role != schema.System || input[5].Role != schema.User {
		t.Fatal("Anthropic conversion changed stored message order")
	}
}
