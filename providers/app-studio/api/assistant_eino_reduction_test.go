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
	"strings"
	"testing"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
)

func TestProjectEinoAssistantRewriteWorkspaceMutationsCompactsSuccessfulBatch(t *testing.T) {
	call := schema.AssistantMessage("", []schema.ToolCall{
		{
			ID:   "write-app",
			Type: "function",
			Function: schema.FunctionCall{
				Name:      projectToolWriteFile,
				Arguments: `{"path":"src/App.jsx","content":"super-secret-source"}`,
			},
		},
		{
			ID:   "patch-style",
			Type: "function",
			Function: schema.FunctionCall{
				Name:      projectToolApplyPatch,
				Arguments: `{"path":"src/style.css","oldText":"old-secret","newText":"new-secret"}`,
			},
		},
	})
	responses := []*schema.Message{
		schema.ToolMessage(`{"operation":"write_file","path":"src/App.jsx","size":42}`, "write-app", schema.WithToolName(projectToolWriteFile)),
		schema.ToolMessage(`{"operation":"apply_patch","path":"src/style.css","replacements":1}`, "patch-style", schema.WithToolName(projectToolApplyPatch)),
	}

	got, err := projectEinoAssistantRewriteWorkspaceMutations(context.Background(), call, responses)
	if err != nil {
		t.Fatalf("rewrite returned error: %v", err)
	}
	if len(got) != 4 || got[0].Role != schema.Assistant || got[1].Role != schema.Tool || got[2].Role != schema.Tool || got[3].Role != schema.User {
		t.Fatalf("rewritten messages = %#v, want compact tool evidence followed by one user reminder", got)
	}
	if len(got[0].ToolCalls) != 2 ||
		got[0].ToolCalls[0].Function.Name != projectToolWriteFile ||
		got[0].ToolCalls[1].Function.Name != projectToolApplyPatch {
		t.Fatalf("compacted tool calls = %#v, want write and patch phase evidence", got[0].ToolCalls)
	}
	for _, call := range got[0].ToolCalls {
		if call.Function.Arguments != `{}` {
			t.Fatalf("compacted %s arguments = %q, want empty object", call.Function.Name, call.Function.Arguments)
		}
	}
	if got[1].ToolName != projectToolWriteFile || got[1].Content != `{"operation":"write_file"}` {
		t.Fatalf("compacted write result = %#v, want machine-readable write evidence", got[1])
	}
	if got[2].ToolName != projectToolApplyPatch || got[2].Content != `{"operation":"apply_patch"}` {
		t.Fatalf("compacted patch result = %#v, want machine-readable patch evidence", got[2])
	}
	for _, want := range []string{"Workspace mutations succeeded", "write_file", "src/App.jsx", "apply_patch", "src/style.css"} {
		if !strings.Contains(got[3].Content, want) {
			t.Fatalf("reminder = %q, want %q", got[3].Content, want)
		}
	}
	for _, forbidden := range []string{"super-secret-source", "old-secret", "new-secret"} {
		for _, message := range got {
			if strings.Contains(message.Content, forbidden) || einoMessagesContainToolArguments([]*schema.Message{message}, forbidden) {
				t.Fatalf("compaction leaked workspace payload %q: %#v", forbidden, got)
			}
		}
	}
}

func TestProjectEinoAssistantRewriteWorkspaceMutationsPreservesUnsafeGroups(t *testing.T) {
	tests := []struct {
		name      string
		call      *schema.Message
		responses []*schema.Message
	}{
		{
			name: "mixed read and write",
			call: schema.AssistantMessage("", []schema.ToolCall{
				{ID: "write", Type: "function", Function: schema.FunctionCall{Name: projectToolWriteFile, Arguments: `{"path":"src/App.jsx","content":"source"}`}},
				{ID: "read", Type: "function", Function: schema.FunctionCall{Name: projectToolReadFile, Arguments: `{"file_path":"src/App.jsx","offset":1,"limit":200}`}},
			}),
			responses: []*schema.Message{
				schema.ToolMessage(`{"operation":"write_file","path":"src/App.jsx","size":6}`, "write", schema.WithToolName(projectToolWriteFile)),
				schema.ToolMessage(`{"path":"src/App.jsx","content":"source"}`, "read", schema.WithToolName(projectToolReadFile)),
			},
		},
		{
			name: "failed write",
			call: schema.AssistantMessage("", []schema.ToolCall{
				{ID: "write", Type: "function", Function: schema.FunctionCall{Name: projectToolWriteFile, Arguments: `{"path":"src/App.jsx","content":"source"}`}},
			}),
			responses: []*schema.Message{
				schema.ToolMessage("Tool call failed: permission denied", "write", schema.WithToolName(projectToolWriteFile)),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := projectEinoAssistantRewriteWorkspaceMutations(context.Background(), tt.call, tt.responses)
			if err != nil {
				t.Fatalf("rewrite returned error: %v", err)
			}
			if len(got) != len(tt.responses)+1 || got[0] != tt.call {
				t.Fatalf("rewritten messages = %#v, want original call and responses", got)
			}
			for i := range tt.responses {
				if got[i+1] != tt.responses[i] {
					t.Fatalf("response %d was rewritten", i)
				}
			}
		})
	}
}

func TestProjectEinoAssistantReductionMiddlewareOnlyRewritesSuccessfulMutations(t *testing.T) {
	middleware, err := projectEinoAssistantReductionMiddleware(context.Background())
	if err != nil {
		t.Fatalf("projectEinoAssistantReductionMiddleware returned error: %v", err)
	}
	largeSource := strings.Repeat("source ", 9000)
	readResult := `{"path":"src/App.jsx","content":"current source must remain visible"}`
	latestResult := `{"status":"ready","summary":"latest group is retained"}`
	state := &adk.ChatModelAgentState{Messages: []*schema.Message{
		schema.SystemMessage("system"),
		schema.UserMessage("build the app"),
		schema.AssistantMessage("", []schema.ToolCall{{
			ID: "write", Type: "function",
			Function: schema.FunctionCall{Name: projectToolWriteFile, Arguments: `{"path":"src/App.jsx","content":"` + largeSource + `"}`},
		}}),
		schema.ToolMessage(`{"operation":"write_file","path":"src/App.jsx","size":63000}`, "write", schema.WithToolName(projectToolWriteFile)),
		schema.AssistantMessage("", []schema.ToolCall{{
			ID: "read", Type: "function",
			Function: schema.FunctionCall{Name: projectToolReadFile, Arguments: `{"file_path":"src/App.jsx","offset":1,"limit":200}`},
		}}),
		schema.ToolMessage(readResult, "read", schema.WithToolName(projectToolReadFile)),
		schema.AssistantMessage("", []schema.ToolCall{{
			ID: "latest", Type: "function",
			Function: schema.FunctionCall{Name: projectToolGetRuntimeStatus, Arguments: `{}`},
		}}),
		schema.ToolMessage(latestResult, "latest", schema.WithToolName(projectToolGetRuntimeStatus)),
	}}

	_, got, err := middleware.BeforeModelRewriteState(context.Background(), state, nil)
	if err != nil {
		t.Fatalf("BeforeModelRewriteState returned error: %v", err)
	}
	joined := ""
	for _, msg := range got.Messages {
		joined += msg.Content
	}
	if strings.Contains(joined, largeSource) {
		t.Fatal("successful write payload remained after reduction")
	}
	if !strings.Contains(joined, "Workspace mutations succeeded") {
		t.Fatalf("messages = %#v, want compact mutation reminder", got.Messages)
	}
	if !strings.Contains(joined, readResult) {
		t.Fatalf("read result was altered by default Eino clearing: %q", joined)
	}
	if !strings.Contains(joined, latestResult) {
		t.Fatalf("latest tool group was not retained: %q", joined)
	}
}
