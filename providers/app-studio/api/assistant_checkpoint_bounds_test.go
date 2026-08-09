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
		if strings.Contains(message.Content, `"complete":true`) || strings.Contains(message.Content, `"version":`) {
			t.Fatalf("bounded checkpoint retained full-read evidence: %s", message.Content)
		}
	}
}
