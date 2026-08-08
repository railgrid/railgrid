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

package api

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/cloudwego/eino/schema"
)

// OpenAI-compatible providers that read a missing content field as null reject
// the whole request, so the serialized payload must always carry a string.
func TestProjectEinoAssistantBackfillMessageContent(t *testing.T) {
	body := []byte(`{"model":"gpt-5","messages":[` +
		`{"role":"assistant","tool_calls":[{"id":"call-1","type":"function"}]},` +
		`{"role":"tool","content":null,"tool_call_id":"call-1"},` +
		`{"role":"user","content":"ship it"}]}`)

	modified, err := projectEinoAssistantBackfillMessageContent(context.Background(), nil, body)
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Model    string `json:"model"`
		Messages []struct {
			Role    string           `json:"role"`
			Content *json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(modified, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Model != "gpt-5" {
		t.Fatalf("model = %q, want gpt-5", payload.Model)
	}
	if len(payload.Messages) != 3 {
		t.Fatalf("messages = %#v", payload.Messages)
	}
	want := []string{`""`, `""`, `"ship it"`}
	for index, message := range payload.Messages {
		if message.Content == nil {
			t.Fatalf("message %d has no content: %s", index, modified)
		}
		if string(*message.Content) != want[index] {
			t.Fatalf("message %d content = %s, want %s", index, *message.Content, want[index])
		}
	}
}

func TestProjectEinoAssistantBackfillMessageContentLeavesOtherBodiesAlone(t *testing.T) {
	for name, body := range map[string]string{
		"already complete": `{"messages":[{"role":"user","content":"hi"}]}`,
		"not an object":    `["nope"]`,
		"no messages":      `{"model":"gpt-5"}`,
	} {
		modified, err := projectEinoAssistantBackfillMessageContent(context.Background(), nil, []byte(body))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if string(modified) != body {
			t.Fatalf("%s: body = %s, want %s", name, modified, body)
		}
	}
}

// The reduction rewrite is durable for the rest of the session, so an empty
// content here would fail every later request rather than a single turn.
func TestProjectEinoAssistantRewrittenMutationCallKeepsContent(t *testing.T) {
	toolCall := schema.ToolCall{
		ID:       "call-1",
		Type:     "function",
		Function: schema.FunctionCall{Name: projectToolCreateFile, Arguments: `{"path":"src/app.tsx"}`},
	}
	assistant := schema.AssistantMessage("", []schema.ToolCall{toolCall})
	response := schema.ToolMessage(
		`{"operation":"create_file","path":"src/app.tsx","changed":true}`,
		"call-1",
		schema.WithToolName(projectToolCreateFile),
	)

	rewritten, err := projectEinoAssistantRewriteWorkspaceMutations(context.Background(), assistant, []*schema.Message{response})
	if err != nil {
		t.Fatal(err)
	}
	// The compaction must NOT leave any tool-call objects behind: keeping them
	// with emptied arguments taught the model to emit mutation calls with no
	// arguments (replace_file({}) → "requires path"). The outcome is retained
	// as natural-language evidence instead.
	sawEvidence := false
	for _, message := range rewritten {
		if len(message.ToolCalls) != 0 {
			t.Fatalf("compacted history still carries tool-call objects the model can imitate: %#v", message)
		}
		if message.Role == schema.User && message.Content != "" {
			sawEvidence = true
		}
	}
	if !sawEvidence {
		t.Fatalf("compacted mutation outcome was not preserved as evidence: %#v", rewritten)
	}
}

// Tool groups must reach the provider unbroken: every tool response has to
// follow its assistant tool-call message before any other role appears.
func TestExpandTransientToolMessagesFlushesImagesAfterToolGroup(t *testing.T) {
	state := newProjectEinoAssistantRunState()
	placeholder := state.RegisterTransientPreviewImage(`{"status":"succeeded"}`, "aGVsbG8=", "image/png")

	previewMessage := schema.ToolMessage(placeholder, "call-preview")
	previewMessage.ToolName = projectToolInspectDevelopmentPreview
	otherMessage := schema.ToolMessage(`{"status":"succeeded"}`, "call-read")
	otherMessage.ToolName = projectToolReadFile

	input := []*schema.Message{
		schema.AssistantMessage("looking", nil),
		previewMessage,
		otherMessage,
		schema.UserMessage("continue"),
	}
	expanded := state.ExpandTransientToolMessages(input)
	if len(expanded) != 5 {
		t.Fatalf("expanded = %#v", expanded)
	}
	roles := []schema.RoleType{schema.Assistant, schema.Tool, schema.Tool, schema.User, schema.User}
	for index, role := range roles {
		if expanded[index].Role != role {
			t.Fatalf("message %d role = %s, want %s", index, expanded[index].Role, role)
		}
	}
	image := expanded[3]
	if len(image.UserInputMultiContent) != 2 || image.Content != "" {
		t.Fatalf("image message = %#v", image)
	}
	if expanded[4].Content != "continue" {
		t.Fatalf("trailing user message = %#v", expanded[4])
	}
}

// A parallel tool turn is persisted one item per call, with results appended as
// each call settles, so the durable order is call A, call B, result A, result B.
// Replayed verbatim, result A follows call B and providers reject the request.
func TestNormalizeProjectAssistantToolCallPairingRegroupsParallelCalls(t *testing.T) {
	messages := []chatMessage{
		{Role: "user", Content: "build it"},
		{Role: "assistant", ToolCalls: []chatToolCall{{ID: "call-a", Type: "function", Function: chatToolCallFunction{Name: "plan_project_changes"}}}},
		{Role: "assistant", ToolCalls: []chatToolCall{{ID: "call-b", Type: "function", Function: chatToolCallFunction{Name: "glob"}}}},
		{Role: "tool", Name: "plan_project_changes", ToolCallID: "call-a", Content: `{"summary":"plan"}`},
		{Role: "tool", Name: "glob", ToolCallID: "call-b", Content: "No files found"},
	}

	normalized := normalizeProjectAssistantToolCallPairing(messages)
	if len(normalized) != 4 {
		t.Fatalf("normalized = %#v", normalized)
	}
	if normalized[0].Role != "user" {
		t.Fatalf("first message = %#v", normalized[0])
	}
	group := normalized[1]
	if group.Role != "assistant" || len(group.ToolCalls) != 2 {
		t.Fatalf("merged tool-call message = %#v", group)
	}
	if group.ToolCalls[0].ID != "call-a" || group.ToolCalls[1].ID != "call-b" {
		t.Fatalf("merged call order = %#v", group.ToolCalls)
	}
	for offset, want := range []string{"call-a", "call-b"} {
		answer := normalized[2+offset]
		if answer.Role != "tool" || answer.ToolCallID != want {
			t.Fatalf("answer %d = %#v, want tool result for %s", offset, answer, want)
		}
	}
}

// An interrupted run leaves a call with no result; every call in a group must
// still be answered or the provider rejects the whole request.
func TestNormalizeProjectAssistantToolCallPairingAnswersUnsettledCalls(t *testing.T) {
	messages := []chatMessage{
		{Role: "assistant", ToolCalls: []chatToolCall{
			{ID: "call-a", Type: "function", Function: chatToolCallFunction{Name: "glob"}},
			{ID: "call-b", Type: "function", Function: chatToolCallFunction{Name: "read_file"}},
		}},
		{Role: "tool", Name: "glob", ToolCallID: "call-a", Content: "No files found"},
		{Role: "system", Content: "The prior assistant turn was interrupted by provider process loss."},
		{Role: "user", Content: "retry"},
	}

	normalized := normalizeProjectAssistantToolCallPairing(messages)
	if len(normalized) != 5 {
		t.Fatalf("normalized = %#v", normalized)
	}
	if normalized[2].ToolCallID != "call-b" || normalized[2].Content != projectAssistantUnsettledToolResult {
		t.Fatalf("unsettled answer = %#v", normalized[2])
	}
	if normalized[3].Role != "system" || normalized[4].Role != "user" {
		t.Fatalf("trailing messages = %#v", normalized[3:])
	}
}

// A result whose call is gone — trimmed by compaction, say — cannot be sent at
// all, so it must not survive into the replayed history.
func TestNormalizeProjectAssistantToolCallPairingDropsOrphanResults(t *testing.T) {
	messages := []chatMessage{
		{Role: "user", Content: "hi"},
		{Role: "tool", Name: "glob", ToolCallID: "call-gone", Content: "No files found"},
		{Role: "assistant", Content: "done"},
	}

	normalized := normalizeProjectAssistantToolCallPairing(messages)
	if len(normalized) != 2 || normalized[0].Role != "user" || normalized[1].Role != "assistant" {
		t.Fatalf("normalized = %#v", normalized)
	}
}
