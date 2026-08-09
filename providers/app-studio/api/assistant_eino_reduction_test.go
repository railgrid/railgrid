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

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
)

func TestProjectEinoAssistantReductionRetainsLatestCompleteReadGroupsTogether(t *testing.T) {
	middleware, err := projectEinoAssistantReductionMiddleware(context.Background())
	if err != nil {
		t.Fatalf("create reduction middleware: %v", err)
	}

	// Keep an older read payload large enough to force clear while making the
	// two complete reads the latest groups. The regression is specifically about
	// preserving both source bodies before the next edit is generated.
	largeRequest := strings.Repeat("workspace context ", 4000)
	oldReadContent := strings.Repeat("stale historical source ", 5000)
	appContent := "// App.jsx\n" + strings.Repeat("const appMarker = true;\n", 500)
	appRereadContent := "// App.jsx reread\n" + strings.Repeat("const appRereadMarker = true;\n", 500)
	confettiContent := "// Confetti.jsx\n" + strings.Repeat("const confettiMarker = true;\n", 500)
	messages := []*schema.Message{
		schema.SystemMessage("You are an App Studio implementation assistant."),
		schema.UserMessage(largeRequest),
		schema.AssistantMessage("", []schema.ToolCall{{
			ID:   "call-read-old",
			Type: "function",
			Function: schema.FunctionCall{
				Name:      projectToolReadFile,
				Arguments: `{"file_path":"src/old.jsx","limit":2000}`,
			},
		}}),
		schema.ToolMessage(`{"path":"src/old.jsx","content":`+quoteJSON(oldReadContent)+`,"complete":true,"version":"sha256:old"}`, "call-read-old", schema.WithToolName(projectToolReadFile)),
		schema.AssistantMessage("", []schema.ToolCall{{
			ID:   "call-create-app",
			Type: "function",
			Function: schema.FunctionCall{
				Name:      projectToolCreateFile,
				Arguments: `{"path":"src/App.jsx","content":"..."}`,
			},
		}}),
		schema.ToolMessage(`{"operation":"create_file","path":"src/App.jsx","changed":true}`, "call-create-app", schema.WithToolName(projectToolCreateFile)),
		schema.AssistantMessage("", []schema.ToolCall{
			{
				ID:   "call-read-app",
				Type: "function",
				Function: schema.FunctionCall{
					Name:      projectToolReadFile,
					Arguments: `{"file_path":"src/App.jsx","limit":2000}`,
				},
			},
			{
				ID:   "call-read-confetti",
				Type: "function",
				Function: schema.FunctionCall{
					Name:      projectToolReadFile,
					Arguments: `{"file_path":"src/Confetti.jsx","limit":2000}`,
				},
			},
		}),
		schema.ToolMessage(`{"path":"src/App.jsx","content":`+quoteJSON(appContent)+`,"complete":true,"version":"sha256:app"}`, "call-read-app", schema.WithToolName(projectToolReadFile)),
		schema.ToolMessage(`{"path":"src/Confetti.jsx","content":`+quoteJSON(confettiContent)+`,"complete":true,"version":"sha256:confetti"}`, "call-read-confetti", schema.WithToolName(projectToolReadFile)),
		schema.AssistantMessage("", []schema.ToolCall{{
			ID:   "call-failed-edit",
			Type: "function",
			Function: schema.FunctionCall{
				Name:      projectToolEditFile,
				Arguments: `{"path":"src/App.jsx","oldString":"stale","newString":"attempt"}`,
			},
		}}),
		schema.ToolMessage(`{"operation":"edit_file","path":"src/App.jsx","status":"failed","message":"stale source"}`, "call-failed-edit", schema.WithToolName(projectToolEditFile)),
		schema.AssistantMessage("", []schema.ToolCall{{
			ID:   "call-read-app-reread",
			Type: "function",
			Function: schema.FunctionCall{
				Name:      projectToolReadFile,
				Arguments: `{"file_path":"src/App.jsx","limit":2000}`,
			},
		}}),
		schema.ToolMessage(`{"path":"src/App.jsx","content":`+quoteJSON(appRereadContent)+`,"complete":true,"version":"sha256:app-reread"}`, "call-read-app-reread", schema.WithToolName(projectToolReadFile)),
	}
	mutationRewrite, err := projectEinoAssistantRewriteWorkspaceMutations(context.Background(), messages[4], messages[5:6])
	if err != nil || len(mutationRewrite) != 1 || !strings.Contains(mutationRewrite[0].Content, projectEinoAssistantWorkspaceMutationEvidencePrefix) {
		t.Fatalf("mutation rewrite fixture = %#v, err=%v", mutationRewrite, err)
	}
	if mutationRewrite[0].Role != schema.User || len(mutationRewrite[0].ToolCalls) != 0 ||
		!strings.Contains(mutationRewrite[0].Content, "[compacted internal history]") ||
		!strings.Contains(mutationRewrite[0].Content, "Continue the original task") {
		t.Fatalf("mutation rewrite must be sole continuation evidence user message: %#v", mutationRewrite)
	}
	_, rewritten, err := middleware.BeforeModelRewriteState(
		context.Background(),
		&adk.ChatModelAgentState{Messages: messages},
		&adk.ModelContext{},
	)
	if err != nil {
		t.Fatalf("reduce messages: %v", err)
	}
	joined := make([]string, 0, len(rewritten.Messages))
	for _, message := range rewritten.Messages {
		joined = append(joined, message.Content)
	}
	modelVisible := strings.Join(joined, "\n")
	for _, marker := range []string{"// App.jsx reread", "const appRereadMarker = true;", "// Confetti.jsx", "const confettiMarker = true;"} {
		if !strings.Contains(modelVisible, marker) {
			t.Fatalf("reduced history lost %q:\n%s", marker, modelVisible)
		}
	}
	if !strings.Contains(modelVisible, projectEinoAssistantWorkspaceMutationEvidencePrefix) {
		shape := make([]string, 0, len(rewritten.Messages))
		for _, message := range rewritten.Messages {
			shape = append(shape, string(message.Role)+":"+message.Content[:min(len(message.Content), 48)])
		}
		t.Fatalf("mutation compaction evidence missing from reduced history: %#v", shape)
	}
	if strings.Contains(modelVisible, "stale historical source ") {
		t.Fatal("forced reduction left the older read payload model-visible")
	}
}

func TestProjectEinoAssistantReductionRetainsLatestSequentialCompleteReads(t *testing.T) {
	middleware, err := projectEinoAssistantReductionMiddleware(context.Background())
	if err != nil {
		t.Fatalf("create reduction middleware: %v", err)
	}
	largeRequest := strings.Repeat("workspace context ", 4000)
	oldReadContent := strings.Repeat("stale sequential source ", 5000)
	appContent := "// sequential App.jsx\n" + strings.Repeat("const sequentialAppMarker = true;\n", 500)
	confettiContent := "// sequential Confetti.jsx\n" + strings.Repeat("const sequentialConfettiMarker = true;\n", 500)
	messages := []*schema.Message{
		schema.SystemMessage("You are an App Studio implementation assistant."),
		schema.UserMessage(largeRequest),
		schema.AssistantMessage("", []schema.ToolCall{{
			ID:   "seq-read-old",
			Type: "function",
			Function: schema.FunctionCall{
				Name:      projectToolReadFile,
				Arguments: `{"file_path":"src/old.jsx","limit":2000}`,
			},
		}}),
		schema.ToolMessage(`{"path":"src/old.jsx","content":`+quoteJSON(oldReadContent)+`,"complete":true,"version":"sha256:old-sequential"}`, "seq-read-old", schema.WithToolName(projectToolReadFile)),
		schema.AssistantMessage("", []schema.ToolCall{{
			ID:   "seq-read-app",
			Type: "function",
			Function: schema.FunctionCall{
				Name:      projectToolReadFile,
				Arguments: `{"file_path":"src/App.jsx","limit":2000}`,
			},
		}}),
		schema.ToolMessage(`{"path":"src/App.jsx","content":`+quoteJSON(appContent)+`,"complete":true,"version":"sha256:app-sequential"}`, "seq-read-app", schema.WithToolName(projectToolReadFile)),
		schema.AssistantMessage("", []schema.ToolCall{{
			ID:   "seq-read-confetti",
			Type: "function",
			Function: schema.FunctionCall{
				Name:      projectToolReadFile,
				Arguments: `{"file_path":"src/Confetti.jsx","limit":2000}`,
			},
		}}),
		schema.ToolMessage(`{"path":"src/Confetti.jsx","content":`+quoteJSON(confettiContent)+`,"complete":true,"version":"sha256:confetti-sequential"}`, "seq-read-confetti", schema.WithToolName(projectToolReadFile)),
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
	for _, marker := range []string{"sequential App.jsx", "sequentialAppMarker", "sequential Confetti.jsx", "sequentialConfettiMarker"} {
		if !strings.Contains(joined, marker) {
			t.Fatalf("sequential reduction lost %q", marker)
		}
	}
	if strings.Contains(joined, "stale sequential source ") {
		t.Fatal("forced reduction left the older sequential read payload model-visible")
	}
}

func TestProjectEinoAssistantReductionDoesNotProtectPartialOrFailedReads(t *testing.T) {
	messages := []*schema.Message{
		schema.AssistantMessage("", []schema.ToolCall{{
			ID:   "complete-read",
			Type: "function",
			Function: schema.FunctionCall{
				Name:      projectToolReadFile,
				Arguments: `{"file_path":"src/complete.jsx","limit":2000}`,
			},
		}}),
		schema.ToolMessage(`{"path":"src/complete.jsx","content":"complete","complete":true,"version":"sha256:complete"}`, "complete-read", schema.WithToolName(projectToolReadFile)),
		schema.AssistantMessage("", []schema.ToolCall{{
			ID:   "partial-read",
			Type: "function",
			Function: schema.FunctionCall{
				Name:      projectToolReadFile,
				Arguments: `{"file_path":"src/partial.jsx","offset":1,"limit":20}`,
			},
		}}),
		schema.ToolMessage(`{"path":"src/partial.jsx","content":"partial","complete":false,"version":"sha256:partial"}`, "partial-read", schema.WithToolName(projectToolReadFile)),
		schema.AssistantMessage("", []schema.ToolCall{{
			ID:   "failed-read",
			Type: "function",
			Function: schema.FunctionCall{
				Name:      projectToolReadFile,
				Arguments: `{"file_path":"src/failed.jsx","limit":2000}`,
			},
		}}),
		schema.ToolMessage(`{"error":"source unavailable"}`, "failed-read", schema.WithToolName(projectToolReadFile)),
	}
	protected := projectEinoAssistantLatestCompleteReadCallIDs(messages)
	if _, ok := protected["complete-read"]; !ok {
		t.Fatalf("complete read was not protected: %#v", protected)
	}
	for _, id := range []string{"partial-read", "failed-read"} {
		if _, ok := protected[id]; ok {
			t.Fatalf("%s unexpectedly protected: %#v", id, protected)
		}
	}
}

func TestProjectEinoAssistantReductionProtectionContextIsRequestLocal(t *testing.T) {
	controller := &projectEinoAssistantReductionController{
		ChatModelAgentMiddleware: &adk.BaseChatModelAgentMiddleware{},
	}
	stateForA := &adk.ChatModelAgentState{Messages: []*schema.Message{
		schema.AssistantMessage("", []schema.ToolCall{{
			ID:   "request-a",
			Type: "function",
			Function: schema.FunctionCall{
				Name:      projectToolReadFile,
				Arguments: `{"file_path":"src/A.jsx"}`,
			},
		}}),
		schema.ToolMessage(`{"path":"src/A.jsx","complete":true,"version":"sha256:a"}`, "request-a", schema.WithToolName(projectToolReadFile)),
	}}
	stateForB := &adk.ChatModelAgentState{Messages: []*schema.Message{
		schema.AssistantMessage("", []schema.ToolCall{{
			ID:   "request-b",
			Type: "function",
			Function: schema.FunctionCall{
				Name:      projectToolReadFile,
				Arguments: `{"file_path":"src/B.jsx"}`,
			},
		}}),
		schema.ToolMessage(`{"path":"src/B.jsx","complete":true,"version":"sha256:b"}`, "request-b", schema.WithToolName(projectToolReadFile)),
	}}
	ctxA, _, err := controller.BeforeModelRewriteState(context.Background(), stateForA, &adk.ModelContext{})
	if err != nil {
		t.Fatalf("request A reduction: %v", err)
	}
	ctxB, _, err := controller.BeforeModelRewriteState(context.Background(), stateForB, &adk.ModelContext{})
	if err != nil {
		t.Fatalf("request B reduction: %v", err)
	}
	protectedA, ok := ctxA.Value(projectEinoAssistantReductionProtectedReadCallIDsContextKey{}).(map[string]struct{})
	if !ok {
		t.Fatal("request A protection set missing")
	}
	protectedB, ok := ctxB.Value(projectEinoAssistantReductionProtectedReadCallIDsContextKey{}).(map[string]struct{})
	if !ok {
		t.Fatal("request B protection set missing")
	}
	if _, ok := protectedA["request-a"]; !ok {
		t.Fatalf("request A protection set = %#v", protectedA)
	}
	if _, ok := protectedA["request-b"]; ok {
		t.Fatalf("request A leaked request B ID: %#v", protectedA)
	}
	if _, ok := protectedB["request-b"]; !ok {
		t.Fatalf("request B protection set = %#v", protectedB)
	}
	if _, ok := protectedB["request-a"]; ok {
		t.Fatalf("request B leaked request A ID: %#v", protectedB)
	}
}

func TestProjectEinoAssistantReductionExcludesTruncatedReadAfterModelProjection(t *testing.T) {
	middleware, err := projectEinoAssistantReductionMiddleware(context.Background())
	if err != nil {
		t.Fatalf("create reduction middleware: %v", err)
	}
	largeRequest := strings.Repeat("workspace context ", 4000)
	largeContent := "// projected App.jsx\n" + strings.Repeat("const projectedReadMarker = true;\n", 1000)
	rawRead := `{"path":"src/App.jsx","content":` + quoteJSON(largeContent) + `,"size":100000,"version":"sha256:projected-app","complete":true}`
	projectedRead := projectEinoAssistantTruncateModelToolOutput(rawRead, projectEinoAssistantModelToolOutputMaxBytes)
	if projectedRead == rawRead || len(projectedRead) > projectEinoAssistantModelToolOutputMaxBytes {
		t.Fatalf("projection did not truncate read result: %d bytes", len(projectedRead))
	}
	protected := projectEinoAssistantLatestCompleteReadCallIDs([]*schema.Message{
		schema.AssistantMessage("", []schema.ToolCall{{
			ID:       "projected-read",
			Function: schema.FunctionCall{Name: projectToolReadFile, Arguments: `{"file_path":"src/App.jsx","limit":2000}`},
		}}),
		schema.ToolMessage(projectedRead, "projected-read", schema.WithToolName(projectToolReadFile)),
	})
	if _, ok := protected["projected-read"]; ok {
		t.Fatalf("truncated projected read retained complete-read protection: %s", projectedRead)
	}
	messages := []*schema.Message{
		schema.SystemMessage("You are an App Studio implementation assistant."),
		schema.UserMessage(largeRequest),
		schema.AssistantMessage("", []schema.ToolCall{{
			ID:   "projected-read",
			Type: "function",
			Function: schema.FunctionCall{
				Name:      projectToolReadFile,
				Arguments: `{"file_path":"src/App.jsx","limit":2000}`,
			},
		}}),
		schema.ToolMessage(projectedRead, "projected-read", schema.WithToolName(projectToolReadFile)),
		schema.AssistantMessage("", []schema.ToolCall{{
			ID:   "failed-edit-after-projection",
			Type: "function",
			Function: schema.FunctionCall{
				Name:      projectToolEditFile,
				Arguments: `{"path":"src/App.jsx","oldString":"stale","newString":"attempt"}`,
			},
		}}),
		schema.ToolMessage(`{"operation":"edit_file","path":"src/App.jsx","status":"failed","message":"stale source"}`, "failed-edit-after-projection", schema.WithToolName(projectToolEditFile)),
		schema.AssistantMessage("", []schema.ToolCall{{
			ID:   "small-read-after-projection",
			Type: "function",
			Function: schema.FunctionCall{
				Name:      projectToolReadFile,
				Arguments: `{"file_path":"src/Confetti.jsx","limit":2000}`,
			},
		}}),
		schema.ToolMessage(`{"path":"src/Confetti.jsx","content":"small","complete":true,"version":"sha256:confetti"}`, "small-read-after-projection", schema.WithToolName(projectToolReadFile)),
	}
	_, rewritten, err := middleware.BeforeModelRewriteState(
		context.Background(),
		&adk.ChatModelAgentState{Messages: messages},
		&adk.ModelContext{},
	)
	if err != nil {
		t.Fatalf("reduce projected read messages: %v", err)
	}
	joined := make([]string, 0, len(rewritten.Messages))
	for _, message := range rewritten.Messages {
		joined = append(joined, message.Content)
	}
	if strings.Contains(strings.Join(joined, "\n"), "projectedReadMarker") {
		t.Fatal("truncated read remained protected from reduction")
	}
}

func quoteJSON(value string) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return string(encoded)
}
