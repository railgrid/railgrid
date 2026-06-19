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

	"github.com/cloudwego/eino/callbacks"
	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

func TestProjectEinoSortedToolCallsHandlesSparseIndexes(t *testing.T) {
	got := projectEinoSortedToolCalls(map[int]chatToolCall{
		2: {ID: "call-2"},
		0: {ID: "call-0"},
	})
	if len(got) != 2 {
		t.Fatalf("tool call count = %d, want 2: %#v", len(got), got)
	}
	if got[0].ID != "call-0" || got[1].ID != "call-2" {
		t.Fatalf("tool calls = %#v, want sorted sparse indexes without duplicates", got)
	}
}

func TestProjectEinoAssistantModelCallbackRecordsStreamedToolCalls(t *testing.T) {
	runState := newProjectEinoAssistantRunState()
	var chunks []string
	var statuses []string
	handler := newProjectEinoAssistantModelCallbackHandler(projectAssistantStreamCallbacks{
		OnChunk:  func(chunk string) { chunks = append(chunks, chunk) },
		OnStatus: func(status string) { statuses = append(statuses, status) },
	}, runState)

	ctx := handler.OnStart(context.Background(), nil, &einomodel.CallbackInput{
		Messages: []*schema.Message{schema.UserMessage("write src/App.tsx")},
	})
	index := 2
	stream := schema.StreamReaderFromArray([]callbacks.CallbackOutput{
		&einomodel.CallbackOutput{Message: schema.AssistantMessage("Working ", nil)},
		&einomodel.CallbackOutput{Message: schema.AssistantMessage("", []schema.ToolCall{{
			Index: &index,
			ID:    "call-write",
			Type:  "function",
			Function: schema.FunctionCall{
				Name:      projectToolWriteFile,
				Arguments: `{"path":`,
			},
		}})},
		&einomodel.CallbackOutput{Message: schema.AssistantMessage("", []schema.ToolCall{{
			Index: &index,
			Function: schema.FunctionCall{
				Arguments: `"src/App.tsx","content":"hi"}`,
			},
		}})},
	})
	handler.OnEndWithStreamOutput(ctx, nil, stream)

	state := runState.CheckpointState()
	if len(chunks) != 1 || chunks[0] != "Working " {
		t.Fatalf("chunks = %#v, want streamed content chunk", chunks)
	}
	if len(statuses) != 1 || statuses[0] != "Preparing action" {
		t.Fatalf("statuses = %#v, want one preparation status", statuses)
	}
	if len(state.Messages) != 2 || state.Messages[0].Role != "user" || !strings.Contains(state.Messages[1].Content, "Working") {
		t.Fatalf("checkpoint messages = %#v, want user input and streamed assistant reply", state.Messages)
	}
	if len(state.ToolCalls) != 1 {
		t.Fatalf("tool calls = %#v, want one merged tool call", state.ToolCalls)
	}
	call := state.ToolCalls[0]
	if call.ID != "call-write" || call.Function.Name != projectToolWriteFile || call.Function.Arguments != `{"path":"src/App.tsx","content":"hi"}` {
		t.Fatalf("tool call = %#v, want merged streamed function call", call)
	}
}
