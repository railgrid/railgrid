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
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
)

func TestProjectEinoAssistantReductionRetainsSequentialReadResultsAndCompactsMutations(t *testing.T) {
	middleware, err := projectEinoAssistantReductionMiddleware(context.Background())
	if err != nil {
		t.Fatalf("create reduction middleware: %v", err)
	}

	// Four bounded read results exercise the age-based clear path: only the
	// latest two groups are retained by suffix, so the first two must survive
	// through ClearExcludeTools. The large mutation result forces reduction and
	// still has to be rewritten to compact workspace evidence.
	largeRequest := strings.Repeat("workspace context ", 4000)
	largeMutationMessage := strings.Repeat("mutation payload ", 5000)
	readContents := []string{
		"// first.jsx\n" + strings.Repeat("const firstReadMarker = true;\n", 120),
		"// second.jsx\n" + strings.Repeat("const secondReadMarker = true;\n", 120),
		"// third.jsx\n" + strings.Repeat("const thirdReadMarker = true;\n", 120),
		"// fourth.jsx\n" + strings.Repeat("const fourthReadMarker = true;\n", 120),
	}
	readPaths := []string{"src/first.jsx", "src/second.jsx", "src/third.jsx", "src/fourth.jsx"}
	readCallIDs := []string{"read-call-1", "read-call-2", "read-call-3", "read-call-4"}
	messages := []*schema.Message{
		schema.SystemMessage("You are an App Studio implementation assistant."),
		schema.UserMessage(largeRequest),
		schema.AssistantMessage("", []schema.ToolCall{{
			ID:   "mutation-call",
			Type: "function",
			Function: schema.FunctionCall{
				Name:      projectToolEditFile,
				Arguments: `{"path":"src/App.jsx","oldString":"before","newString":"after"}`,
			},
		}}),
		schema.ToolMessage(`{"operation":"edit_file","path":"src/App.jsx","changed":true,"message":`+quoteJSON(largeMutationMessage)+`}`, "mutation-call", schema.WithToolName(projectToolEditFile)),
	}
	for index, path := range readPaths {
		callID := readCallIDs[index]
		messages = append(messages,
			schema.AssistantMessage("", []schema.ToolCall{{
				ID:   callID,
				Type: "function",
				Function: schema.FunctionCall{
					Name:      projectToolReadFile,
					Arguments: `{"file_path":"` + path + `","limit":2000}`,
				},
			}}),
			schema.ToolMessage(`{"path":"`+path+`","content":`+quoteJSON(readContents[index])+`,"complete":true,"version":"sha256:read-`+callID+`"}`, callID, schema.WithToolName(projectToolReadFile)),
		)
	}

	_, rewritten, err := middleware.BeforeModelRewriteState(
		context.Background(),
		&adk.ChatModelAgentState{Messages: messages},
		&adk.ModelContext{},
	)
	if err != nil {
		t.Fatalf("reduce messages: %v", err)
	}
	modelVisible := make([]string, 0, len(rewritten.Messages))
	for _, message := range rewritten.Messages {
		modelVisible = append(modelVisible, message.Content)
	}
	joined := strings.Join(modelVisible, "\n")
	for index, callID := range readCallIDs {
		expectedArguments := `{"file_path":"` + readPaths[index] + `","limit":2000}`
		expectedVersion := "sha256:read-" + callID
		foundCall := false
		foundEnvelope := false
		for _, message := range rewritten.Messages {
			for _, toolCall := range message.ToolCalls {
				if toolCall.ID != callID {
					continue
				}
				if foundCall {
					t.Fatalf("read tool call %q appeared more than once", callID)
				}
				foundCall = true
				if toolCall.Function.Name != projectToolReadFile || toolCall.Function.Arguments != expectedArguments {
					t.Fatalf("read tool call %q = %#v; want exact read_file call and arguments", callID, toolCall)
				}
			}
			if message.Role != schema.Tool || message.ToolCallID != callID {
				continue
			}
			if foundEnvelope {
				t.Fatalf("read result %q appeared more than once", callID)
			}
			var envelope struct {
				Path     string `json:"path"`
				Content  string `json:"content"`
				Version  string `json:"version"`
				Complete bool   `json:"complete"`
			}
			if err := json.Unmarshal([]byte(message.Content), &envelope); err != nil {
				t.Fatalf("read result %q is not valid JSON: %v", callID, err)
			}
			foundEnvelope = true
			if envelope.Path != readPaths[index] || envelope.Content != readContents[index] || envelope.Version != expectedVersion || !envelope.Complete {
				t.Fatalf("read result %q = %#v; want unchanged versioned envelope", callID, envelope)
			}
		}
		if !foundCall || !foundEnvelope {
			t.Fatalf("read %q was not retained with its call and result: call=%t envelope=%t", callID, foundCall, foundEnvelope)
		}
	}
	for index, marker := range []string{"firstReadMarker", "secondReadMarker", "thirdReadMarker", "fourthReadMarker"} {
		if !strings.Contains(joined, marker) {
			t.Fatalf("reduced history lost sequential read %d (%q):\n%s", index+1, marker, joined)
		}
	}
	if strings.Contains(joined, "[Old tool result content cleared]") {
		t.Fatal("read result was replaced with the age-based clearing placeholder")
	}
	if !strings.Contains(joined, projectEinoAssistantWorkspaceMutationEvidencePrefix) {
		t.Fatalf("mutation compaction evidence missing from reduced history: %s", joined)
	}
	for _, marker := range []string{"const firstReadMarker = true;", "const secondReadMarker = true;", "const thirdReadMarker = true;", "const fourthReadMarker = true;"} {
		if !strings.Contains(joined, marker) {
			t.Fatalf("read body was not retained after reduction: %q", marker)
		}
	}
}

func TestProjectEinoAssistantReductionProjectedReadRemainsBoundedAndNonAuthoritative(t *testing.T) {
	const maxBytes = projectEinoAssistantModelToolOutputMaxBytes
	content := "// projected App.jsx\n" + strings.Repeat("const projectedReadMarker = true;\n", 1200)
	rawRead := `{"path":"src/App.jsx","content":` + quoteJSON(content) + `,"size":100000,"version":"sha256:projected-app","complete":true}`

	projectedRead := projectEinoAssistantTruncateModelToolOutput(rawRead, maxBytes)
	if projectedRead == rawRead || len(projectedRead) > maxBytes {
		t.Fatalf("projected read is %d bytes; want a changed result within %d", len(projectedRead), maxBytes)
	}
	if !utf8.ValidString(projectedRead) {
		t.Fatal("projected read is not valid UTF-8")
	}
	var projected struct {
		Path                     string `json:"path"`
		Content                  string `json:"content"`
		Version                  string `json:"version"`
		Complete                 bool   `json:"complete"`
		ModelProjectionTruncated bool   `json:"modelProjectionTruncated"`
	}
	if err := json.Unmarshal([]byte(projectedRead), &projected); err != nil {
		t.Fatalf("projected read is not valid JSON: %v\n%s", err, projectedRead)
	}
	if projected.Path != "src/App.jsx" || projected.Version != "" || projected.Complete || !projected.ModelProjectionTruncated {
		t.Fatalf("projected read metadata = %#v; want bounded, non-authoritative evidence", projected)
	}
	if !strings.Contains(projected.Content, "projected App.jsx") ||
		!strings.Contains(projected.Content, projectEinoAssistantToolOutputTruncationNotice) {
		t.Fatalf("projected read content lost bounded evidence: %q", projected.Content)
	}

	// Put the projected read before the two retained suffix groups. Reduction
	// must still leave the exact bounded, non-authoritative envelope untouched
	// because read_file is excluded from age-based clearing.
	largeRequest := strings.Repeat("workspace context ", 4000)
	largeMutationMessage := strings.Repeat("mutation payload ", 5000)
	messages := []*schema.Message{
		schema.SystemMessage("You are an App Studio implementation assistant."),
		schema.UserMessage(largeRequest),
		schema.AssistantMessage("", []schema.ToolCall{{
			ID:   "mutation-call",
			Type: "function",
			Function: schema.FunctionCall{
				Name:      projectToolEditFile,
				Arguments: `{"path":"src/App.jsx","oldString":"before","newString":"after"}`,
			},
		}}),
		schema.ToolMessage(`{"operation":"edit_file","path":"src/App.jsx","changed":true,"message":`+quoteJSON(largeMutationMessage)+`}`, "mutation-call", schema.WithToolName(projectToolEditFile)),
		schema.AssistantMessage("", []schema.ToolCall{{
			ID:   "projected-read",
			Type: "function",
			Function: schema.FunctionCall{
				Name:      projectToolReadFile,
				Arguments: `{"file_path":"src/App.jsx","limit":2000}`,
			},
		}}),
		schema.ToolMessage(projectedRead, "projected-read", schema.WithToolName(projectToolReadFile)),
	}
	tailPaths := []string{"src/tail-one.jsx", "src/tail-two.jsx"}
	tailCallIDs := []string{"tail-read-1", "tail-read-2"}
	for index, path := range tailPaths {
		callID := tailCallIDs[index]
		messages = append(messages,
			schema.AssistantMessage("", []schema.ToolCall{{
				ID:   callID,
				Type: "function",
				Function: schema.FunctionCall{
					Name:      projectToolReadFile,
					Arguments: `{"file_path":"` + path + `","limit":2000}`,
				},
			}}),
			schema.ToolMessage(`{"path":"`+path+`","content":"tail","complete":true,"version":"sha256:`+callID+`"}`, callID, schema.WithToolName(projectToolReadFile)),
		)
	}
	middleware, err := projectEinoAssistantReductionMiddleware(context.Background())
	if err != nil {
		t.Fatalf("create reduction middleware: %v", err)
	}
	_, rewritten, err := middleware.BeforeModelRewriteState(
		context.Background(),
		&adk.ChatModelAgentState{Messages: messages},
		&adk.ModelContext{},
	)
	if err != nil {
		t.Fatalf("reduce projected read messages: %v", err)
	}
	foundProjectedRead := false
	for _, message := range rewritten.Messages {
		if message.Role != schema.Tool || message.ToolCallID != "projected-read" {
			continue
		}
		if foundProjectedRead {
			t.Fatal("projected read result appeared more than once")
		}
		foundProjectedRead = true
		if message.Content != projectedRead {
			t.Fatalf("projected read changed during reduction: got %d bytes, want exact %d-byte envelope", len(message.Content), len(projectedRead))
		}
	}
	if !foundProjectedRead {
		t.Fatal("projected read outside the suffix was not retained")
	}
}

func quoteJSON(value string) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return string(encoded)
}
