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
	"fmt"
	"strings"
	"testing"
)

func TestProjectAssistantCheckpointBoundsModelToolPayloadsAndDedupeState(t *testing.T) {
	state := newProjectEinoAssistantRunState()
	for index := 0; index < projectAssistantCheckpointMaxMessages+40; index++ {
		state.messages = append(state.messages, chatMessage{
			Role: "tool",
			Name: projectToolReadFile,
			Content: fmt.Sprintf(
				`{"path":"src/%03d.ts","content":%q,"complete":true,"version":"sha256:%03d"}`,
				index,
				strings.Repeat("untrusted output ", 2000),
				index,
			),
		})
		state.RecordAssistantReply(projectAssistantReply{ToolCalls: []chatToolCall{{
			ID: fmt.Sprintf("call-%03d", index),
			Function: chatToolCallFunction{
				Name:      projectToolReadFile,
				Arguments: fmt.Sprintf(`{"file_path":"src/%03d.ts"}`, index),
			},
		}}})
	}

	checkpoint := state.CheckpointState()
	if len(checkpoint.Messages) > projectAssistantCheckpointMaxMessages {
		t.Fatalf("checkpoint messages = %d, want <= %d", len(checkpoint.Messages), projectAssistantCheckpointMaxMessages)
	}
	if len(checkpoint.SeenToolCalls) > projectEinoAssistantMaxTrackedReads {
		t.Fatalf("checkpoint seen calls = %d, want <= %d", len(checkpoint.SeenToolCalls), projectEinoAssistantMaxTrackedReads)
	}
	for _, message := range checkpoint.Messages {
		if message.Role != "tool" {
			continue
		}
		if len(message.Content) > projectEinoAssistantModelToolOutputMaxBytes {
			t.Fatalf("checkpoint tool result = %d bytes, want <= %d", len(message.Content), projectEinoAssistantModelToolOutputMaxBytes)
		}
		if _, ok := projectEinoAssistantCompleteReadFileReceiptKey(message.Content); ok {
			t.Fatalf("bounded malformed checkpoint result parsed as complete-read evidence: %s", message.Content)
		}
		if _, ok := projectEinoAssistantParseLiteralReadFileOutput(message.Content); ok {
			t.Fatalf("bounded malformed checkpoint result parsed as a literal receipt: %s", message.Content)
		}
	}
}

func TestProjectAssistantCheckpointProjectsTrustedLargeReadFileLiterally(t *testing.T) {
	content := "const route = `\\path <&> 雪`;\n" + strings.Repeat("const marker = `\\path <&> 雪`;\n", 1200)
	receipt, err := projectAssistantSourceReadResult(map[string]any{
		"path": "src/large.ts", "content": content, "size": len(content),
		"complete": true, "version": "sha256:large-checkpoint", "offset": 1, "limit": 2000,
	})
	if err != nil {
		t.Fatalf("encode complete read receipt: %v", err)
	}
	messages := []chatMessage{
		{Role: "assistant", ToolCalls: []chatToolCall{{
			ID: "read-large", Type: "function",
			Function: chatToolCallFunction{Name: projectToolReadFile, Arguments: `{"file_path":"src/large.ts"}`},
		}}},
		// Eino tool-result messages can rely on their exact linked local call.
		{Role: "tool", ToolCallID: "read-large", Content: receipt},
	}
	checkpoint := projectAssistantBoundCheckpointMessages(messages)
	if len(checkpoint) != 2 {
		t.Fatalf("checkpoint messages = %d, want two messages", len(checkpoint))
	}
	if checkpoint[1].Content == receipt || len(checkpoint[1].Content) > projectEinoAssistantModelToolOutputMaxBytes {
		t.Fatalf("checkpoint did not project/bound linked read result: len=%d receiptBytes=%d", len(checkpoint[1].Content), len(receipt))
	}
	projected, ok := projectEinoAssistantParseLiteralReadFileOutput(checkpoint[1].Content)
	if !ok || !projected.modelClipped || projected.complete || projected.version != "" || projected.selectedBytes != len(content) {
		t.Fatalf("checkpoint read projection = %#v, parsed=%v", projected, ok)
	}
	if _, ok := projectEinoAssistantCompleteReadFileReceiptKey(checkpoint[1].Content); ok {
		t.Fatal("checkpoint clipped read result retained complete-read authority")
	}
	if messages[1].Content != receipt {
		t.Fatal("checkpoint projection mutated the durable source receipt")
	}
}
