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
	"reflect"
	"strings"
	"testing"
)

func TestProjectAssistantDeepIterationsConfiguration(t *testing.T) {
	maxInt := int(^uint(0) >> 1)
	tests := []struct {
		value string
		want  int
	}{
		{value: "", want: maxAssistantDeepIterations},
		{value: "48", want: 48},
		{value: " unlimited ", want: maxInt},
		{value: "UNLIMITED", want: maxInt},
		{value: "0", want: maxAssistantDeepIterations},
		{value: "-1", want: maxAssistantDeepIterations},
		{value: "invalid", want: maxAssistantDeepIterations},
	}
	for _, tt := range tests {
		t.Run(tt.value, func(t *testing.T) {
			if got := projectAssistantDeepIterationsForValue(tt.value); got != tt.want {
				t.Fatalf("iterations for %q = %d, want %d", tt.value, got, tt.want)
			}
		})
	}
}

func TestNewProjectEinoAssistantModelFactoryUsesNativeOpenAIModel(t *testing.T) {
	factory := newProjectEinoAssistantModelFactory(&Server{})
	model, err := factory(context.Background(), projectAssistantRunRequest{
		LLM: projectLLMSettings{
			Provider: defaultProjectLLMProvider,
			BaseURL:  "https://llm.example.test/v1",
			Model:    "test-model",
			APIKey:   "test-key",
		},
	}, newProjectEinoAssistantRunState())
	if err != nil {
		t.Fatalf("newProjectEinoAssistantModelFactory returned error: %v", err)
	}
	if got := reflect.TypeOf(model).String(); !strings.Contains(got, "openai.ChatModel") {
		t.Fatalf("model type = %s, want native Eino OpenAI chat model", got)
	}
}

func TestNewProjectEinoAssistantModelFactoryUsesNativeGeminiModel(t *testing.T) {
	factory := newProjectEinoAssistantModelFactory(&Server{})
	model, err := factory(context.Background(), projectAssistantRunRequest{
		LLM: projectLLMSettings{
			Provider: projectLLMProviderGoogle,
			BaseURL:  "https://generativelanguage.googleapis.com",
			Model:    "gemini-2.5-flash",
			APIKey:   "test-key",
		},
	}, newProjectEinoAssistantRunState())
	if err != nil {
		t.Fatalf("newProjectEinoAssistantModelFactory returned error: %v", err)
	}
	if got := reflect.TypeOf(model).String(); !strings.Contains(got, "gemini.ChatModel") {
		t.Fatalf("model type = %s, want native Eino Gemini chat model", got)
	}
}

func TestParseProjectCreatePreflight(t *testing.T) {
	got, err := parseProjectCreatePreflight("```json\n{\"displayName\":\"Task Desk\",\"repositoryName\":\"task-desk\",\"turn\":{\"profile\":\"implementation\",\"requires_current_state\":true,\"requires_runtime_state\":false,\"requests_mutation\":true,\"confidence\":\"high\"}}\n```")
	if err != nil {
		t.Fatalf("parseProjectCreatePreflight returned error: %v", err)
	}
	if got.Naming.DisplayName != "Task Desk" || got.Naming.RepositoryName != "task-desk" {
		t.Fatalf("naming = %#v, want Task Desk/task-desk", got.Naming)
	}
	if got.TurnDecision.Profile != projectAssistantTurnProfileImplementation || !got.TurnDecision.RequestsMutation {
		t.Fatalf("turn decision = %#v, want implementation mutation", got.TurnDecision)
	}
}

func TestProjectCreatePreflightAlwaysStartsAsImplementation(t *testing.T) {
	got, err := normalizeProjectCreatePreflight(projectCreatePreflight{
		Naming: projectNamingResult{DisplayName: "Task Desk", RepositoryName: "task-desk"},
		TurnDecision: projectAssistantTurnDecision{
			Profile:    projectAssistantTurnProfileDiscussion,
			Confidence: projectAssistantTurnConfidenceHigh,
		},
	}, "a todo list app")
	if err != nil {
		t.Fatalf("normalizeProjectCreatePreflight returned error: %v", err)
	}
	if got.TurnDecision.Profile != projectAssistantTurnProfileImplementation ||
		!got.TurnDecision.RequiresCurrentState ||
		!got.TurnDecision.RequestsMutation {
		t.Fatalf("turn decision = %#v, want deterministic implementation mutation", got.TurnDecision)
	}
}

func TestProjectCreatePreflightHonorsExplicitBlankProjectRequest(t *testing.T) {
	for _, prompt := range []string{"Create a blank project.", "Create an empty project.", "Create a project, but do not write code yet."} {
		t.Run(prompt, func(t *testing.T) {
			got, err := normalizeProjectCreatePreflight(projectCreatePreflight{
				Naming: projectNamingResult{DisplayName: "Blank Canvas", RepositoryName: "blank-canvas"},
			}, prompt)
			if err != nil {
				t.Fatalf("normalizeProjectCreatePreflight returned error: %v", err)
			}
			if got.TurnDecision.RequestsMutation || projectAssistantTurnProfileAllowsMutation(got.TurnDecision.Profile) {
				t.Fatalf("turn decision = %#v, want an explicit no-source-mutation start", got.TurnDecision)
			}
		})
	}
}

func TestProjectCreatePreflightDoesNotTreatScopedConstraintAsBlankProject(t *testing.T) {
	got, err := normalizeProjectCreatePreflight(projectCreatePreflight{
		Naming: projectNamingResult{DisplayName: "Task Desk", RepositoryName: "task-desk"},
	}, "Build a todo app, but don't build a backend.")
	if err != nil {
		t.Fatalf("normalizeProjectCreatePreflight returned error: %v", err)
	}
	if got.TurnDecision.Profile != projectAssistantTurnProfileImplementation || !got.TurnDecision.RequestsMutation {
		t.Fatalf("turn decision = %#v, want implementation for a scoped constraint", got.TurnDecision)
	}
}

func TestInitialCreationBuilderPromptSkipsPlanAndBatchesIndependentWrites(t *testing.T) {
	project := projectWithRepository("demo-repo", "demo", "github")
	prompt := projectSystemPromptForInitialPlan(project, &ProjectRepositoryView{Ref: "demo-repo", Ready: true}, projectAssistantTurnProfileImplementation, true)
	if strings.Contains(prompt, "Before source edits, call request_project_plan_approval") {
		t.Fatalf("initial prompt retained normal plan instruction:\n%s", prompt)
	}
	for _, want := range []string{
		"Do not call request_project_plan_approval before write_file, apply_patch, or mkdir",
		"Prefer a single response containing all independent write_file, apply_patch, and mkdir calls for the current step",
		"never wait for one result before another independent write",
		"verify the live development workspace before any repository commit",
		"Do not call commit_project_files in this initial run",
		"Workspace writes automatically synchronize and restart the development process",
		projectToolInspectDevelopmentTemplates,
		projectToolVerifyDevelopmentRuntime,
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("initial prompt missing %q:\n%s", want, prompt)
		}
	}
}

func TestBuilderPromptKeepsApprovalPolicyIndependentOfToolNames(t *testing.T) {
	project := projectWithRepository("demo-repo", "demo", "github")
	prompt := projectSystemPromptForInitialPlan(project, &ProjectRepositoryView{Ref: "demo-repo", Ready: true}, projectAssistantTurnProfileImplementation, false)

	if !strings.Contains(prompt, "target path envelope") {
		t.Fatalf("builder prompt missing path-scoped approval guidance:\n%s", prompt)
	}
	if strings.Contains(prompt, "allowed edit operations") || strings.Contains(prompt, "allowedOperations") {
		t.Fatalf("builder prompt exposed tool names as approval policy:\n%s", prompt)
	}
}
