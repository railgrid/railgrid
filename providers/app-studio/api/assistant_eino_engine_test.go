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

	aiv1alpha1 "github.com/faroshq/provider-app-studio/apis/ai/v1alpha1"
)

func TestEinoAssistantEngineRequiresProject(t *testing.T) {
	engine := NewEinoAssistantEngine(&Server{})
	_, err := engine.StreamProjectAssistant(
		context.Background(),
		projectAssistantRunRequest{},
		projectAssistantEventSinkFunc(func(context.Context, projectAssistantEvent) error {
			return nil
		}),
	)
	if err == nil || !strings.Contains(err.Error(), "project is required") {
		t.Fatalf("StreamProjectAssistant error = %v, want missing project error", err)
	}
}

func TestEinoAssistantEngineRunsBody(t *testing.T) {
	engine, ok := NewEinoAssistantEngine(&Server{}).(projectEinoAssistantEngine)
	if !ok {
		t.Fatalf("engine = %T, want projectEinoAssistantEngine", NewEinoAssistantEngine(&Server{}))
	}
	engine.body = stubProjectAssistantEngine{result: projectAssistantRunResult{Content: "body result"}}
	result, err := engine.StreamProjectAssistant(
		context.Background(),
		projectAssistantRunRequest{
			Project: &aiv1alpha1.Project{},
		},
		nil,
	)
	if err != nil {
		t.Fatalf("StreamProjectAssistant returned error: %v", err)
	}
	if result.Content != "body result" {
		t.Fatalf("content = %q, want body result", result.Content)
	}
}

type stubProjectAssistantEngine struct {
	result projectAssistantRunResult
	err    error
}

func (e stubProjectAssistantEngine) StreamProjectAssistant(
	context.Context,
	projectAssistantRunRequest,
	projectAssistantEventSink,
) (projectAssistantRunResult, error) {
	return e.result, e.err
}
