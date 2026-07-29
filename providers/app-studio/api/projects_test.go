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
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	aiv1alpha1 "github.com/faroshq/provider-app-studio/apis/ai/v1alpha1"
	asclient "github.com/faroshq/provider-app-studio/client"
	"github.com/faroshq/provider-app-studio/store"
	"github.com/faroshq/provider-app-studio/workspace"
)

func TestProjectAssistantTurnDecisionForStreamStartUsesPrecomputedDecision(t *testing.T) {
	want := projectAssistantTurnDecision{
		Profile:              projectAssistantTurnProfileImplementation,
		RequiresCurrentState: true,
		RequestsMutation:     true,
		Confidence:           projectAssistantTurnConfidenceHigh,
	}
	called := false
	got, err := projectAssistantTurnDecisionForStreamStart(context.Background(), func(context.Context, projectAssistantTurnRouteRequest) (projectAssistantTurnDecision, error) {
		called = true
		return projectAssistantTurnDecision{}, nil
	}, projectAssistantTurnRouteRequest{}, &projectAssistantStreamStart{TurnDecision: &want})
	if err != nil {
		t.Fatalf("projectAssistantTurnDecisionForStreamStart returned error: %v", err)
	}
	if called {
		t.Fatal("stream start invoked the ordinary router despite a precomputed decision")
	}
	if got != want {
		t.Fatalf("decision = %#v, want %#v", got, want)
	}
}

func TestGenerateProjectAssistantStreamWithStartBypassesRouter(t *testing.T) {
	messages := store.NewMemoryStore()
	workspaces := workspace.NewFileStore(t.TempDir())
	server := NewWithWorkspace(nil, messages, workspaces, "", false)
	id := identity{orgUUID: "org-a", workspaceUUID: "ws-1", tenantPath: "root:org-a:ws-1"}
	project := &aiv1alpha1.Project{}
	project.Name = "demo"
	project.UID = "test-project-uid-demo"
	if err := appendProjectUserMessage(context.Background(), messages, testProjectMessageScope(id.orgUUID, id.workspaceUUID, project.Name), "Build a todo app"); err != nil {
		t.Fatalf("append user message: %v", err)
	}
	server.assistantTurnRouter = func(context.Context, projectAssistantTurnRouteRequest) (projectAssistantTurnDecision, error) {
		t.Fatal("ordinary router should not run for a fresh stream preflight")
		return projectAssistantTurnDecision{}, nil
	}
	engine := &capturingProjectAssistantEngine{}
	server.assistantEngine = engine
	settings := projectLLMSettings{Provider: defaultProjectLLMProvider, BaseURL: defaultProjectLLMBaseURL, Model: "test-model", APIKey: "test-key"}
	client := asclient.NewFromDynamic(projectSettingsDynamicClient{secret: projectLLMSettingsSecret(settings)})
	decision := projectAssistantTurnDecision{Profile: projectAssistantTurnProfileImplementation, RequestsMutation: true, Confidence: projectAssistantTurnConfidenceHigh}
	start := &projectAssistantStreamStart{TurnDecision: &decision, InitialApprovedPlan: ptrProjectAssistantApprovedPlan(projectAssistantInitialCreationPlan())}
	_, err := server.generateProjectAssistantStreamWithStart(httptest.NewRequest(http.MethodPost, "/", nil), id, client, project, projectAssistantStreamCallbacks{}, start)
	if err != nil {
		t.Fatalf("generateProjectAssistantStreamWithStart returned error: %v", err)
	}
	if engine.req.TurnPolicy.profile != projectAssistantTurnProfileImplementation {
		t.Fatalf("turn policy = %#v, want implementation", engine.req.TurnPolicy)
	}
	if engine.req.InitialApprovedPlan == nil {
		t.Fatal("initial stream request omitted the run-local creation grant")
	}
}

type capturingProjectAssistantEngine struct {
	req projectAssistantRunRequest
}

func (e *capturingProjectAssistantEngine) StreamProjectAssistant(_ context.Context, req projectAssistantRunRequest) (projectAssistantRunResult, error) {
	e.req = req
	return projectAssistantRunResult{Content: "done"}, nil
}

func (*capturingProjectAssistantEngine) ResumeProjectAssistant(context.Context, projectAssistantRunRequest, projectAssistantResumeRequest, projectAssistantCheckpointState) (projectAssistantRunResult, error) {
	return projectAssistantRunResult{}, nil
}

func TestAppendUniqueProjectMemoryEntries(t *testing.T) {
	got := appendUniqueProjectMemoryEntries(
		[]string{"Keep the existing goal", "  Preserve spacing after trim  ", "Keep the existing goal"},
		[]string{"Preserve spacing after trim", "Add a verified preview", "", " Add a verified preview "},
	)
	want := []string{"Keep the existing goal", "Preserve spacing after trim", "Add a verified preview"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("appendUniqueProjectMemoryEntries() = %#v, want %#v", got, want)
	}
}
