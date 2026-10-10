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
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	aiv1alpha1 "github.com/railgrid/provider-app-studio/apis/ai/v1alpha1"
	"github.com/railgrid/provider-app-studio/store"
)

func TestProjectEinoAssistantHistoryBoundsToolResultsAndRedactsLinkedNativeBrowserReceipts(t *testing.T) {
	ctx := context.Background()
	memory := store.NewMemoryStore()
	scope := store.Scope{OrgUUID: "org", WorkspaceUUID: "workspace", ProjectName: "demo", ProjectUID: "uid"}

	userContent := strings.Repeat("user history remains intact ", 500)
	if err := appendProjectAssistantConversationMessage(ctx, memory, scope, "run-1", "user", projectAssistantConversationUser, chatMessage{Role: "user", Content: userContent}); err != nil {
		t.Fatal(err)
	}
	browserReceipt := `{"status":302,"request":"https://preview.invalid/items?search=blue&sort=desc","redirect":"https://hub.invalid/auth/apps/authorize?client_id=app&state=AUTH_STATE_SENTINEL","callback":"https://preview.invalid/__railgrid/auth/callback?code=ONE_TIME_CODE_SENTINEL&state=CALLBACK_STATE_SENTINEL","handoff":"https://hub.invalid/auth/apps/preview-handoff?code=HANDOFF_CODE_SENTINEL","ordinaryApp":"https://preview.invalid/items?code=APPLICATION_CODE_SENTINEL"}`
	if err := appendProjectAssistantConversationMessage(ctx, memory, scope, "run-1", "browser-call", projectAssistantConversationToolCall, chatMessage{Role: "assistant", ToolCalls: []chatToolCall{{ID: "browser-call", Type: "function", Function: chatToolCallFunction{Name: "browser_network_requests", Arguments: `{}`}}}}); err != nil {
		t.Fatal(err)
	}
	// Legacy results may omit Name. The durable assistant call still gives the
	// projection a trustworthy tool name for targeted browser URL redaction.
	if err := appendProjectAssistantConversationMessage(ctx, memory, scope, "run-1", "browser-result", projectAssistantConversationToolResult, chatMessage{Role: "tool", ToolCallID: "browser-call", Content: browserReceipt}); err != nil {
		t.Fatal(err)
	}
	readContent := "literal <&> source ☃\n" + strings.Repeat("source-line-", 1400) + "\nend <tail>"
	readJSON, err := json.Marshal(map[string]any{
		"path":     "src/App.tsx",
		"content":  readContent,
		"size":     len(readContent),
		"complete": true,
		"version":  "opaque-version-1",
		"offset":   1,
		"limit":    2000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := appendProjectAssistantConversationMessage(ctx, memory, scope, "run-1", "read-call", projectAssistantConversationToolCall, chatMessage{Role: "assistant", ToolCalls: []chatToolCall{{ID: "read-call", Type: "function", Function: chatToolCallFunction{Name: projectToolReadFile, Arguments: `{"path":"src/App.tsx"}`}}}}); err != nil {
		t.Fatal(err)
	}
	if err := appendProjectAssistantConversationMessage(ctx, memory, scope, "run-1", "read-result", projectAssistantConversationToolResult, chatMessage{Role: "tool", Name: projectToolReadFile, ToolCallID: "read-call", Content: string(readJSON)}); err != nil {
		t.Fatal(err)
	}
	ordinaryMCPReceipt := `{"url":"https://hub.invalid/auth/apps/authorize?client_id=app&state=ORDINARY_MCP_STATE","query":"https://preview.invalid/items?search=blue&sort=desc"}`
	if err := appendProjectAssistantConversationMessage(ctx, memory, scope, "run-1", "mcp-call", projectAssistantConversationToolCall, chatMessage{Role: "assistant", ToolCalls: []chatToolCall{{ID: "mcp-call", Type: "function", Function: chatToolCallFunction{Name: "mcp_database_query", Arguments: `{}`}}}}); err != nil {
		t.Fatal(err)
	}
	if err := appendProjectAssistantConversationMessage(ctx, memory, scope, "run-1", "mcp-result", projectAssistantConversationToolResult, chatMessage{Role: "tool", Name: "mcp_database_query", ToolCallID: "mcp-call", Content: ordinaryMCPReceipt}); err != nil {
		t.Fatal(err)
	}
	if err := appendProjectAssistantConversationMessage(ctx, memory, scope, "run-1", "mcp-large-call", projectAssistantConversationToolCall, chatMessage{Role: "assistant", ToolCalls: []chatToolCall{{ID: "mcp-large-call", Type: "function", Function: chatToolCallFunction{Name: "mcp_database_query", Arguments: `{}`}}}}); err != nil {
		t.Fatal(err)
	}
	largeMCPReceipt := strings.Repeat("large external result ", 1000)
	if err := appendProjectAssistantConversationMessage(ctx, memory, scope, "run-1", "mcp-large-result", projectAssistantConversationToolResult, chatMessage{Role: "tool", Name: "mcp_database_query", ToolCallID: "mcp-large-call", Content: largeMCPReceipt}); err != nil {
		t.Fatal(err)
	}

	durable, err := loadProjectAssistantConversation(ctx, memory, scope)
	if err != nil {
		t.Fatal(err)
	}
	var durableBrowser, durableRead string
	for _, message := range durable {
		if message.Role != "tool" {
			continue
		}
		switch message.ToolCallID {
		case "browser-call":
			durableBrowser = message.Content
		case "read-call":
			durableRead = message.Content
		}
	}
	if durableBrowser != browserReceipt || durableRead != string(readJSON) {
		t.Fatal("model projection mutated durable browser or source-file receipts")
	}

	input, err := projectEinoAssistantInputMessages(ctx, projectAssistantRunRequest{Conversation: durable}, newProjectEinoAssistantRunState(), false)
	if err != nil {
		t.Fatal(err)
	}
	projected := projectEinoMessagesToChat(input)
	if len(projected) != len(durable) {
		t.Fatalf("projected message count = %d, durable message count = %d", len(projected), len(durable))
	}
	if projected[0].Content != userContent {
		t.Fatal("ordinary user history was truncated")
	}
	callIndexes := make(map[string]int)
	toolResults := make(map[string]chatMessage)
	for index, message := range projected {
		if message.Role == "assistant" {
			for _, call := range message.ToolCalls {
				callIndexes[call.ID] = index
			}
		}
		if message.Role == "tool" {
			toolResults[message.ToolCallID] = message
		}
	}
	for _, callID := range []string{"browser-call", "read-call", "mcp-call", "mcp-large-call"} {
		callIndex, exists := callIndexes[callID]
		if !exists {
			t.Fatalf("projected assistant tool call %q is missing: %#v", callID, projected)
		}
		if _, exists := toolResults[callID]; !exists {
			t.Fatalf("projected tool result %q is missing: %#v", callID, projected)
		}
		if callIndex+1 >= len(projected) || projected[callIndex+1].Role != "tool" || projected[callIndex+1].ToolCallID != callID {
			t.Fatalf("projected call/result pairing changed for %q: %#v", callID, projected)
		}
	}
	browserResult := toolResults["browser-call"]
	var browserContent map[string]any
	if err := json.Unmarshal([]byte(browserResult.Content), &browserContent); err != nil {
		t.Fatalf("projected browser receipt is not valid JSON: %v", err)
	}
	if browserContent["status"] != float64(302) {
		t.Fatalf("redirect status changed: %#v", browserContent["status"])
	}
	for _, secret := range []string{"AUTH_STATE_SENTINEL", "ONE_TIME_CODE_SENTINEL", "CALLBACK_STATE_SENTINEL", "HANDOFF_CODE_SENTINEL"} {
		if strings.Contains(browserResult.Content, secret) {
			t.Fatalf("projected native browser receipt retained %q", secret)
		}
	}
	for _, ordinary := range []string{"search=blue", "sort=desc", "client_id=app", "code=APPLICATION_CODE_SENTINEL"} {
		if !strings.Contains(browserResult.Content, ordinary) {
			t.Fatalf("projected browser receipt lost ordinary URL data %q: %s", ordinary, browserResult.Content)
		}
	}

	readResult := toolResults["read-call"]
	if len(readResult.Content) > projectEinoAssistantModelToolOutputMaxBytes {
		t.Fatalf("projected read_file result is %d bytes, limit %d", len(readResult.Content), projectEinoAssistantModelToolOutputMaxBytes)
	}
	projectedRead, ok := projectEinoAssistantParseLiteralReadFileOutput(readResult.Content)
	if !ok {
		t.Fatalf("projected read_file result is not a literal-source receipt:\n%s", readResult.Content)
	}
	if projectedRead.path != "src/App.tsx" || projectedRead.complete || projectedRead.version != "" || !projectedRead.truncated {
		t.Fatalf("projected read_file metadata = %#v", projectedRead)
	}
	if !utf8.ValidString(projectedRead.shown) || !strings.Contains(projectedRead.shown, "literal <&> source ☃") {
		t.Fatalf("projected read_file content lost valid literal source: %q", projectedRead.shown[:min(80, len(projectedRead.shown))])
	}
	if again := projectEinoAssistantConversationPayload(projected)[callIndexes["read-call"]+1].Content; again != readResult.Content {
		t.Fatal("literal read projection was not idempotent on a repeated history pass")
	}

	if toolResults["mcp-call"].Content != ordinaryMCPReceipt {
		t.Fatalf("ordinary MCP receipt was modified: %s", toolResults["mcp-call"].Content)
	}
	if len(toolResults["mcp-large-call"].Content) > projectEinoAssistantModelToolOutputMaxBytes {
		t.Fatalf("projected generic MCP result is %d bytes, limit %d", len(toolResults["mcp-large-call"].Content), projectEinoAssistantModelToolOutputMaxBytes)
	}
	if !reflect.DeepEqual(durable, mustLoadProjectAssistantConversation(t, ctx, memory, scope)) {
		t.Fatal("model projection changed the durable conversation ledger")
	}
}

func TestProjectEinoAssistantHistoryProjectionRedactsDirectlyNamedNativeBrowserResults(t *testing.T) {
	message := chatMessage{
		Role:    "tool",
		Name:    browserMCPToolSnapshot,
		Content: `{"status":302,"location":"https://hub.invalid/auth/apps/authorize?state=NAMED_STATE_SENTINEL"}`,
	}
	projected := projectEinoAssistantConversationPayload([]chatMessage{message})
	if len(projected) != 1 || strings.Contains(projected[0].Content, "NAMED_STATE_SENTINEL") || !strings.Contains(projected[0].Content, "[redacted]") {
		t.Fatalf("directly named native-browser result was not safely projected: %#v", projected)
	}
}

func TestProjectEinoAssistantHistoryProjectionAppliesToRestoredEinoMessages(t *testing.T) {
	browserReceipt := `{"status":302,"location":"https://hub.invalid/auth/apps/authorize?state=RESTORED_STATE_SENTINEL","handoff":"https://hub.invalid/auth/apps/preview-handoff?code=RESTORED_HANDOFF_SENTINEL"}`
	runState := newProjectEinoAssistantRunState()
	runState.RecordModelInput([]chatMessage{
		{Role: "user", Content: "continue from this checkpoint"},
		{Role: "assistant", ToolCalls: []chatToolCall{{ID: "restored-browser", Type: "function", Function: chatToolCallFunction{Name: browserMCPToolSnapshot, Arguments: `{}`}}}},
		{Role: "tool", ToolCallID: "restored-browser", Content: browserReceipt},
	})
	input, err := projectEinoAssistantInputMessages(context.Background(), projectAssistantRunRequest{}, runState, true)
	if err != nil {
		t.Fatal(err)
	}
	projected := projectEinoMessagesToChat(input)
	if len(projected) != 3 || projected[1].ToolCalls[0].ID != "restored-browser" || projected[2].ToolCallID != "restored-browser" {
		t.Fatalf("restored call/result pairing changed: %#v", projected)
	}
	if strings.Contains(projected[2].Content, "RESTORED_STATE_SENTINEL") || strings.Contains(projected[2].Content, "RESTORED_HANDOFF_SENTINEL") || !strings.Contains(projected[2].Content, "[redacted]") {
		t.Fatalf("restored native-browser receipt was not redacted: %s", projected[2].Content)
	}
}

func TestProjectEinoAssistantHistoryDeduplicatesCompleteReadFileResults(t *testing.T) {
	content := "literal source <&> ☃\n" + strings.Repeat("const duplicateRead = true;\n", 700)
	oldReceipt := projectHistoryCompleteReadReceipt(t, "src/App.tsx", content, "sha256:unchanged", int64(len(content)), 1, 2000)
	latestReceipt := projectHistoryCompleteReadReceiptReordered(t, "src/App.tsx", content, "sha256:unchanged", int64(len(content)), 1, 2000)
	messages := []chatMessage{
		{Role: "assistant", ToolCalls: []chatToolCall{{ID: "read-old", Type: "function", Function: chatToolCallFunction{Name: projectToolReadFile, Arguments: `{"file_path":"src/App.tsx","limit":2000}`}}}},
		{Role: "tool", ToolCallID: "read-old", Content: oldReceipt}, // Legacy name is inferred from the linked local call.
		{Role: "assistant", ToolCalls: []chatToolCall{{ID: "read-middle", Type: "function", Function: chatToolCallFunction{Name: projectToolReadFile, Arguments: `{"file_path":"src/App.tsx","limit":2000}`}}}},
		{Role: "tool", Name: projectToolReadFile, ToolCallID: "read-middle", Content: oldReceipt},
		{Role: "assistant", ToolCalls: []chatToolCall{{ID: "read-latest", Type: "function", Function: chatToolCallFunction{Name: projectToolReadFile, Arguments: `{"file_path":"src/App.tsx","limit":2000}`}}}},
		{Role: "tool", ToolCallID: "read-latest", Content: latestReceipt},
	}
	ctx := context.Background()
	memory := store.NewMemoryStore()
	scope := store.Scope{OrgUUID: "org", WorkspaceUUID: "workspace", ProjectName: "demo", ProjectUID: "uid"}
	itemIDs := []string{"call-old", "result-old", "call-middle", "result-middle", "call-latest", "result-latest"}
	itemTypes := []string{
		projectAssistantConversationToolCall, projectAssistantConversationToolResult,
		projectAssistantConversationToolCall, projectAssistantConversationToolResult,
		projectAssistantConversationToolCall, projectAssistantConversationToolResult,
	}
	for index, message := range messages {
		if err := appendProjectAssistantConversationMessage(ctx, memory, scope, "run-dedup", itemIDs[index], itemTypes[index], message); err != nil {
			t.Fatalf("append complete read history item: %v", err)
		}
	}
	durable := mustLoadProjectAssistantConversation(t, ctx, memory, scope)
	projected := projectEinoAssistantConversationPayload(durable)
	if len(projected) != len(messages) {
		t.Fatalf("projected message count = %d, want %d", len(projected), len(messages))
	}
	if !reflect.DeepEqual(durable, mustLoadProjectAssistantConversation(t, ctx, memory, scope)) {
		t.Fatal("history projection mutated durable complete read receipts")
	}
	if projected[0].Role != "assistant" || projected[1].Role != "tool" || projected[1].ToolCallID != "read-old" ||
		projected[2].Role != "assistant" || projected[3].Role != "tool" || projected[3].ToolCallID != "read-middle" ||
		projected[4].Role != "assistant" || projected[5].Role != "tool" || projected[5].ToolCallID != "read-latest" {
		t.Fatalf("deduplicated history changed call/result ordering or IDs: %#v", projected)
	}
	for _, oldIndex := range []int{1, 3} {
		var oldNotice map[string]json.RawMessage
		if err := json.Unmarshal([]byte(projected[oldIndex].Content), &oldNotice); err != nil {
			t.Fatalf("older read placeholder is not valid JSON: %v", err)
		}
		if len(oldNotice) != 4 || string(oldNotice["complete"]) != "false" ||
			string(oldNotice["modelProjectionDeduplicated"]) != "true" ||
			string(oldNotice["supersededByToolCallID"]) != `"read-latest"` ||
			string(oldNotice["path"]) != `"src/App.tsx"` {
			t.Fatalf("older complete read was not replaced by the minimal latest-read reference: %s", projected[oldIndex].Content)
		}
		if _, exists := oldNotice["content"]; exists {
			t.Fatal("deduplication placeholder retained source content")
		}
		if _, exists := oldNotice["version"]; exists {
			t.Fatal("deduplication placeholder retained complete-read version evidence")
		}
		if len(projected[oldIndex].Content) > projectEinoAssistantModelToolOutputMaxBytes {
			t.Fatalf("deduplication placeholder is %d bytes, over the output cap", len(projected[oldIndex].Content))
		}
	}
	latestRead, ok := projectEinoAssistantParseLiteralReadFileOutput(projected[5].Content)
	if !ok {
		t.Fatalf("latest read result is not a literal-source receipt: %s", projected[5].Content)
	}
	if len(projected[5].Content) > projectEinoAssistantModelToolOutputMaxBytes || latestRead.complete ||
		latestRead.version != "" || !latestRead.truncated || !utf8.ValidString(latestRead.shown) ||
		!strings.Contains(latestRead.shown, "literal source <&> ☃") {
		t.Fatalf("latest read did not remain the bounded source receipt: size=%d metadata=%#v", len(projected[5].Content), latestRead)
	}
	secondProjection := projectEinoAssistantConversationPayload(projected)
	if !reflect.DeepEqual(projected, secondProjection) {
		t.Fatalf("history projection was not idempotent:\nfirst=%#v\nsecond=%#v", projected, secondProjection)
	}
	if !reflect.DeepEqual(durable, mustLoadProjectAssistantConversation(t, ctx, memory, scope)) {
		t.Fatal("deduplication changed source receipts held by the durable history")
	}
}

func TestProjectEinoAssistantHistoryDeduplicatesOnlyTrustedCanonicalCompleteReads(t *testing.T) {
	base := projectHistoryCompleteReadReceipt(t, "src/App.tsx", "source", "sha256:same", int64(len("source")), 1, 2000)
	tests := []struct {
		name    string
		variant func(string) string
		tool    string
	}{
		{name: "different path", variant: projectHistoryReadReceiptVariant("path", "src/Other.tsx")},
		{name: "different content", variant: projectHistoryReadReceiptVariant("content", "changed")},
		{name: "different version", variant: projectHistoryReadReceiptVariant("version", "sha256:changed")},
		{name: "different size", variant: projectHistoryReadReceiptVariant("size", int64(7))},
		{name: "different offset", variant: projectHistoryReadReceiptVariant("offset", 2)},
		{name: "different limit", variant: projectHistoryReadReceiptVariant("limit", 1999)},
		{name: "partial receipt", variant: projectHistoryReadReceiptVariant("complete", false)},
		{name: "truncated receipt", variant: projectHistoryReadReceiptVariant("truncated", true)},
		{name: "binary receipt", variant: projectHistoryReadReceiptVariant("binary", true)},
		{name: "unknown metadata", variant: projectHistoryReadReceiptVariant("sourceRevision", uint64(3))},
		{name: "error receipt", variant: projectHistoryReadReceiptVariant("error", "read failed")},
		{name: "already truncated projection", variant: projectHistoryReadReceiptVariant("modelProjectionTruncated", true)},
		{name: "already deduplicated projection", variant: projectHistoryReadReceiptVariant("modelProjectionDeduplicated", true)},
		{name: "duplicate metadata key", variant: func(value string) string {
			return strings.Replace(value, `"size":6`, `"size":6,"size":7`, 1)
		}},
		{name: "invalid raw utf8", variant: func(value string) string {
			return strings.Replace(value, `"content":"source"`, `"content":"`+string([]byte{0xff})+`"`, 1)
		}},
		{name: "conflicting linked tool name", variant: func(value string) string { return value }, tool: "mcp__read_file"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			variant := test.variant(base)
			toolName := projectToolReadFile
			if test.tool != "" {
				toolName = test.tool
			}
			messages := []chatMessage{
				{Role: "assistant", ToolCalls: []chatToolCall{{ID: "read-one", Type: "function", Function: chatToolCallFunction{Name: toolName}}}},
				{Role: "tool", Name: projectToolReadFile, ToolCallID: "read-one", Content: base},
				{Role: "assistant", ToolCalls: []chatToolCall{{ID: "read-two", Type: "function", Function: chatToolCallFunction{Name: toolName}}}},
				{Role: "tool", Name: projectToolReadFile, ToolCallID: "read-two", Content: variant},
			}
			if duplicates := projectEinoAssistantDuplicateCompleteReadFileResults(messages); len(duplicates) != 0 {
				t.Fatalf("ineligible/different receipts were deduplicated: %#v", duplicates)
			}
		})
	}

	directMessages := []chatMessage{
		{Role: "tool", Name: projectToolReadFile, ToolCallID: "direct-one", Content: base},
		{Role: "tool", Name: projectToolReadFile, ToolCallID: "direct-two", Content: base},
	}
	if duplicates := projectEinoAssistantDuplicateCompleteReadFileResults(directMessages); len(duplicates) != 1 || duplicates[0] != "direct-two" {
		t.Fatalf("directly named trusted read_file results were not recognized: %#v", duplicates)
	}
	explicitFalseFlags := projectHistoryReadReceiptVariant("binary", false)(projectHistoryReadReceiptVariant("truncated", false)(base))
	canonicalMessages := []chatMessage{
		{Role: "tool", Name: projectToolReadFile, ToolCallID: "canonical-one", Content: base},
		{Role: "tool", Name: projectToolReadFile, ToolCallID: "canonical-two", Content: explicitFalseFlags},
	}
	if duplicates := projectEinoAssistantDuplicateCompleteReadFileResults(canonicalMessages); len(duplicates) != 1 || duplicates[0] != "canonical-two" {
		t.Fatalf("equivalent omitted/false receipt flags were not canonicalized: %#v", duplicates)
	}
}

func TestProjectEinoAssistantHistoryDeduplicationSurvivesRestoredCheckpoint(t *testing.T) {
	receipt := projectHistoryCompleteReadReceipt(t, "src/App.tsx", "same source", "sha256:same", int64(len("same source")), 1, 2000)
	messages := []chatMessage{
		{Role: "assistant", ToolCalls: []chatToolCall{{ID: "checkpoint-old", Type: "function", Function: chatToolCallFunction{Name: projectToolReadFile}}}},
		{Role: "tool", ToolCallID: "checkpoint-old", Content: receipt},
		{Role: "assistant", ToolCalls: []chatToolCall{{ID: "checkpoint-latest", Type: "function", Function: chatToolCallFunction{Name: projectToolReadFile}}}},
		{Role: "tool", ToolCallID: "checkpoint-latest", Content: receipt},
	}
	state := newProjectEinoAssistantRunState()
	state.RecordModelInput(messages)
	checkpoint := state.CheckpointState()
	restored := newProjectEinoAssistantRunState()
	restored.RestoreCheckpointState(checkpoint)
	input, err := projectEinoAssistantInputMessages(context.Background(), projectAssistantRunRequest{}, restored, true)
	if err != nil {
		t.Fatalf("project restored checkpoint input: %v", err)
	}
	projected := projectEinoMessagesToChat(input)
	if len(projected) != len(messages) || !strings.Contains(projected[1].Content, "checkpoint-latest") {
		t.Fatalf("restored checkpoint did not retain the latest read and its reference: %#v", projected)
	}
	if projected[3].ToolCallID != "checkpoint-latest" {
		t.Fatalf("latest restored read changed: %#v", projected[3])
	}
	latest, ok := projectEinoAssistantParseLiteralReadFileOutput(projected[3].Content)
	if !ok || !latest.complete || latest.content != "same source" {
		t.Fatalf("latest restored literal read changed: %#v, parsed=%v", projected[3], ok)
	}
	restored.RecordModelInput(projected)
	secondInput, err := projectEinoAssistantInputMessages(context.Background(), projectAssistantRunRequest{}, restored, true)
	if err != nil {
		t.Fatalf("project already-projected checkpoint input: %v", err)
	}
	if second := projectEinoMessagesToChat(secondInput); !reflect.DeepEqual(projected, second) {
		t.Fatalf("restored checkpoint projection changed on replay:\nfirst=%#v\nsecond=%#v", projected, second)
	}
}

func projectHistoryCompleteReadReceipt(t *testing.T, path, content, version string, size int64, offset, limit int) string {
	t.Helper()
	encoded, err := json.Marshal(map[string]any{
		"path": path, "content": content, "size": size, "version": version,
		"complete": true, "offset": offset, "limit": limit,
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func projectHistoryCompleteReadReceiptReordered(t *testing.T, path, content, version string, size int64, offset, limit int) string {
	t.Helper()
	encode := func(value any) string {
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		return string(encoded)
	}
	return `{"limit":` + encode(limit) + `,"offset":` + encode(offset) + `,"complete":true,"version":` + encode(version) +
		`,"size":` + encode(size) + `,"content":` + encode(content) + `,"path":` + encode(path) + `}`
}

func projectHistoryReadReceiptVariant(field string, value any) func(string) string {
	return func(receipt string) string {
		fields := make(map[string]any)
		if err := json.Unmarshal([]byte(receipt), &fields); err != nil {
			return receipt
		}
		fields[field] = value
		encoded, err := json.Marshal(fields)
		if err != nil {
			return receipt
		}
		return string(encoded)
	}
}

func TestProjectEinoAssistantCompactionSamplesProjectedHistoricalToolResults(t *testing.T) {
	t.Setenv(projectEinoAssistantModelContextTokensEnv, "128")
	ctx := context.Background()
	memory := store.NewMemoryStore()
	scope := store.Scope{OrgUUID: "org", WorkspaceUUID: "workspace", ProjectName: "demo", ProjectUID: "uid"}
	req := projectAssistantRunRequest{
		Project: &aiv1alpha1.Project{
			ObjectMeta: metav1.ObjectMeta{Name: "demo", UID: "uid"},
			Spec:       aiv1alpha1.ProjectSpec{DisplayName: "Demo"},
		},
		MessageScope:      scope,
		AssistantRun:      &store.AssistantRun{ID: "run-1"},
		CollaborationMode: projectAssistantCollaborationModeDefault,
	}
	content := "literal <&> source ☃\n" + strings.Repeat("large-source-", 1800)
	fullRead, err := projectAssistantSourceReadResult(map[string]any{
		"path":     "src/large.ts",
		"content":  content,
		"size":     len(content),
		"complete": true,
		"version":  "opaque-full-version",
		"offset":   1,
		"limit":    2000,
	})
	if err != nil {
		t.Fatal(err)
	}
	conversation := []chatMessage{
		{Role: "assistant", ToolCalls: []chatToolCall{{ID: "read-large", Type: "function", Function: chatToolCallFunction{Name: projectToolReadFile, Arguments: `{"path":"src/large.ts"}`}}}},
		{Role: "tool", Name: projectToolReadFile, ToolCallID: "read-large", Content: fullRead},
		{Role: "assistant", ToolCalls: []chatToolCall{{ID: "read-large-latest", Type: "function", Function: chatToolCallFunction{Name: projectToolReadFile, Arguments: `{"path":"src/large.ts"}`}}}},
		{Role: "tool", Name: projectToolReadFile, ToolCallID: "read-large-latest", Content: fullRead},
	}
	input, err := projectEinoAssistantInputMessages(ctx, projectAssistantRunRequest{Conversation: conversation}, newProjectEinoAssistantRunState(), false)
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < projectEinoAssistantCompactionTestMessages; index++ {
		input = append(input, schema.UserMessage("prior user history"))
	}
	model := &projectEinoAssistantCompactionTestModel{}
	middleware, err := projectEinoAssistantCompactionMiddleware(ctx, model, &Server{
		tenantWorkspaces: defaultTestWorkspaces.lookup,
		tenantActors:     defaultTestActors.lookup,
		tenantProviders:  defaultTestProviders,
		store:            memory,
	}, req, newProjectEinoAssistantRunState())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := middleware.BeforeModelRewriteState(ctx, &adk.ChatModelAgentState{Messages: input}, &adk.ModelContext{}); err != nil {
		t.Fatalf("compaction model boundary: %v", err)
	}
	sampledReads := make(map[string]*schema.Message, 2)
	for _, message := range model.input {
		if message != nil && message.Role == schema.Tool && (message.ToolCallID == "read-large" || message.ToolCallID == "read-large-latest") {
			sampledReads[message.ToolCallID] = message
		}
	}
	if len(sampledReads) != 2 {
		t.Fatalf("compaction model input lost deduplicated read_file call/result pairs: %#v", model.input)
	}
	var olderRead map[string]json.RawMessage
	if err := json.Unmarshal([]byte(sampledReads["read-large"].Content), &olderRead); err != nil {
		t.Fatalf("compaction sampled malformed older read placeholder: %v", err)
	}
	if string(olderRead["complete"]) != "false" || string(olderRead["supersededByToolCallID"]) != `"read-large-latest"` {
		t.Fatalf("compaction sampled an invalid duplicate-read placeholder: %s", sampledReads["read-large"].Content)
	}
	if _, exists := olderRead["content"]; exists {
		t.Fatal("compaction retained duplicate source content in the older receipt")
	}
	if _, exists := olderRead["version"]; exists {
		t.Fatal("compaction retained complete-read evidence in the older receipt")
	}
	sampledRead := sampledReads["read-large-latest"]
	if len(sampledRead.Content) > projectEinoAssistantModelToolOutputMaxBytes {
		t.Fatalf("compaction sampled %d-byte read_file result, limit %d", len(sampledRead.Content), projectEinoAssistantModelToolOutputMaxBytes)
	}
	projectedRead, ok := projectEinoAssistantParseLiteralReadFileOutput(sampledRead.Content)
	if !ok {
		t.Fatalf("compaction sampled malformed literal read_file projection: %s", sampledRead.Content)
	}
	if projectedRead.complete || projectedRead.version != "" || !projectedRead.truncated || !projectedRead.modelClipped ||
		projectedRead.selectedBytes != len(content) || projectedRead.shownBytes >= projectedRead.selectedBytes {
		t.Fatalf("compaction sampled full-read evidence or inaccurate clipping metadata: %#v", projectedRead)
	}
}

func mustLoadProjectAssistantConversation(t *testing.T, ctx context.Context, memory store.Store, scope store.Scope) []chatMessage {
	t.Helper()
	messages, err := loadProjectAssistantConversation(ctx, memory, scope)
	if err != nil {
		t.Fatal(err)
	}
	return messages
}
