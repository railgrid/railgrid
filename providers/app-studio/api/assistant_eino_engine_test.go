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

func TestEinoAssistantEngineRunsBodyThroughADKRunner(t *testing.T) {
	engine := projectEinoAssistantEngine{
		body:      stubProjectAssistantBody(projectAssistantRunResult{Content: "from eino runner"}, nil),
		newRunner: newProjectEinoAssistantRunner,
	}
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
	if result.Content != "from eino runner" {
		t.Fatalf("content = %q, want Eino runner result", result.Content)
	}
}

func TestEinoAssistantEngineRequiresRunnerOutput(t *testing.T) {
	engine := projectEinoAssistantEngine{
		body: stubProjectAssistantBody(projectAssistantRunResult{Content: "unused"}, nil),
		newRunner: func(context.Context, adk.Agent) projectEinoAssistantRunner {
			return emptyProjectEinoAssistantRunner{}
		},
	}
	_, err := engine.StreamProjectAssistant(
		context.Background(),
		projectAssistantRunRequest{
			Project: &aiv1alpha1.Project{},
		},
		nil,
	)
	if err == nil || !strings.Contains(err.Error(), "eino runner completed without assistant output") {
		t.Fatalf("StreamProjectAssistant error = %v, want missing runner output error", err)
	}
}

func TestServerRebuildsDefaultEinoAssistantEngine(t *testing.T) {
	server := &Server{}
	if _, ok := server.projectAssistantEngine().(projectEinoAssistantEngine); !ok {
		t.Fatalf("engine = %T, want projectEinoAssistantEngine", server.projectAssistantEngine())
	}
}

func TestNewServerDefaultsToEinoAssistantEngine(t *testing.T) {
	server := NewWithWorkspace(nil, nil, nil, "", false)
	if _, ok := server.projectAssistantEngine().(projectEinoAssistantEngine); !ok {
		t.Fatalf("engine = %T, want projectEinoAssistantEngine", server.projectAssistantEngine())
	}
}

func stubProjectAssistantBody(result projectAssistantRunResult, err error) projectEinoAssistantBody {
	return func(context.Context, projectAssistantRunRequest, projectAssistantEventSink) (projectAssistantRunResult, error) {
		return result, err
	}
}

type emptyProjectEinoAssistantRunner struct{}

func (emptyProjectEinoAssistantRunner) Run(
	context.Context,
	[]adk.Message,
	...adk.AgentRunOption,
) *adk.AsyncIterator[*adk.AgentEvent] {
	iter, gen := adk.NewAsyncIteratorPair[*adk.AgentEvent]()
	gen.Close()
	return iter
}
