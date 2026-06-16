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
)

func TestProjectAssistantContractCanReferenceEinoADK(t *testing.T) {
	runner := adk.NewRunner(context.Background(), adk.RunnerConfig{
		EnableStreaming: true,
	})
	if runner == nil {
		t.Fatal("adk.NewRunner returned nil")
	}

	input := adk.AgentInput{EnableStreaming: true}
	if !input.EnableStreaming {
		t.Fatal("adk.AgentInput did not preserve streaming mode")
	}
}

func TestProjectAssistantEventSinkContract(t *testing.T) {
	sink := projectAssistantEventSink(projectAssistantEventSinkFunc(func(context.Context, projectAssistantEvent) error {
		return nil
	}))
	if err := sink.EmitProjectAssistantEvent(context.Background(), projectAssistantEvent{
		Type: projectAssistantEventRunStarted,
	}); err != nil {
		t.Fatalf("EmitProjectAssistantEvent returned error: %v", err)
	}
}

func TestProjectAssistantEventOmitsEmptyOptionalTimestamps(t *testing.T) {
	payload, err := json.Marshal(projectAssistantEvent{
		Type: projectAssistantEventCheckpointSaved,
		Checkpoint: &projectAssistantCheckpoint{
			ID: "checkpoint-1",
		},
	})
	if err != nil {
		t.Fatalf("Marshal returned error: %v", err)
	}
	if strings.Contains(string(payload), "createdAt") {
		t.Fatalf("event encoded empty createdAt: %s", payload)
	}
}

type projectAssistantEventSinkFunc func(context.Context, projectAssistantEvent) error

func (f projectAssistantEventSinkFunc) EmitProjectAssistantEvent(
	ctx context.Context,
	event projectAssistantEvent,
) error {
	return f(ctx, event)
}
