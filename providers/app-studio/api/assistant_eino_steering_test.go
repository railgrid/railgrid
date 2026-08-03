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
	"testing"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
)

func TestProjectEinoAssistantLifecycleDrainsSteeringBeforeModelCall(t *testing.T) {
	steering := make(chan projectAssistantSteeringInput, 1)
	steering <- projectAssistantSteeringInput{MessageID: "user-2", ClientRequestID: "steer-1", Content: "also add tests"}
	runState := newProjectEinoAssistantRunState()
	state := &adk.ChatModelAgentState{Messages: []*schema.Message{
		schema.SystemMessage("system"),
		schema.UserMessage("build it"),
		schema.ToolMessage("done", "call-1"),
	}}
	activated := false
	lifecycle := projectEinoAssistantLifecycleMiddleware(projectAssistantRunRequest{
		Steering: steering,
		ActivateSteering: func(_ context.Context, inputs []projectAssistantSteeringInput) error {
			if len(inputs) != 1 || inputs[0].ClientRequestID != "steer-1" {
				t.Fatalf("activated inputs = %#v", inputs)
			}
			if messages := runState.ModelMessages(); len(messages) != 0 {
				t.Fatalf("steering became model-visible before durable boundary activation: %#v", messages)
			}
			activated = true
			return nil
		},
	}, runState)
	_, rewritten, err := lifecycle.BeforeModelRewriteState(context.Background(), state, &adk.ModelContext{})
	if err != nil {
		t.Fatal(err)
	}
	if !activated {
		t.Fatal("steering boundary was not activated before the model rewrite")
	}
	last := rewritten.Messages[len(rewritten.Messages)-1]
	if last.Role != schema.User || last.Content != "also add tests" {
		t.Fatalf("last model message = %#v, want steered user input", last)
	}
	messages := runState.ModelMessages()
	if len(messages) != 1 || messages[0].Role != "user" || messages[0].Content != "also add tests" {
		t.Fatalf("run-state steering messages = %#v", messages)
	}
	select {
	case unexpected := <-steering:
		t.Fatalf("steering queue still contained %#v", unexpected)
	default:
	}
}

func TestProjectEinoAssistantLifecycleDefersSteeringForFirstPostCompactionContinuation(t *testing.T) {
	steering := make(chan projectAssistantSteeringInput, 1)
	steering <- projectAssistantSteeringInput{MessageID: "user-2", ClientRequestID: "steer-1", Content: "also add tests"}
	runState := newProjectEinoAssistantRunState()
	runState.DeferSteeringOnceAfterCompaction()
	state := &adk.ChatModelAgentState{Messages: []*schema.Message{
		schema.SystemMessage("system"),
		schema.UserMessage("build it"),
		schema.ToolMessage("done", "call-1"),
	}}
	lifecycle := projectEinoAssistantLifecycleMiddleware(projectAssistantRunRequest{Steering: steering}, runState)

	_, first, err := lifecycle.BeforeModelRewriteState(context.Background(), state, &adk.ModelContext{})
	if err != nil {
		t.Fatal(err)
	}
	if got := first.Messages[len(first.Messages)-1]; got.Role != schema.Tool {
		t.Fatalf("first post-compaction message = %#v, want completed tool result", got)
	}
	if messages := runState.ModelMessages(); len(messages) != 0 {
		t.Fatalf("run-state steering messages after deferred boundary = %#v, want none", messages)
	}

	_, second, err := lifecycle.BeforeModelRewriteState(context.Background(), first, &adk.ModelContext{})
	if err != nil {
		t.Fatal(err)
	}
	last := second.Messages[len(second.Messages)-1]
	if last.Role != schema.User || last.Content != "also add tests" {
		t.Fatalf("second post-compaction message = %#v, want steered user input", last)
	}
}
