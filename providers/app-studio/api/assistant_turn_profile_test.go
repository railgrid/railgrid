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

	einomodel "github.com/cloudwego/eino/components/model"
	einoschema "github.com/cloudwego/eino/schema"

	aiv1alpha1 "github.com/faroshq/provider-app-studio/apis/ai/v1alpha1"
	"github.com/faroshq/provider-app-studio/store"
)

func TestProjectAssistantTurnProfileClassifier(t *testing.T) {
	tests := []struct {
		name    string
		message string
		want    projectAssistantTurnProfile
	}{
		{name: "discussion", message: "I am thinking about whether this product direction makes sense", want: projectAssistantTurnProfileDiscussion},
		{name: "guidance", message: "How should I design authentication for this app?", want: projectAssistantTurnProfileGuidance},
		{name: "exploration", message: "What files are in my current app?", want: projectAssistantTurnProfileExploration},
		{name: "debugging", message: "The preview is not working and shows Failed to fetch", want: projectAssistantTurnProfileDebugging},
		{name: "debug fix", message: "Fix the failed fetch error and make it work", want: projectAssistantTurnProfileDebugFix},
		{name: "fix only fallback", message: "Please fix the login form", want: projectAssistantTurnProfileDebugFix},
		{name: "implementation", message: "Add a search field to the todo app", want: projectAssistantTurnProfileImplementation},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := classifyProjectAssistantTurnProfile([]store.Message{{
				Role:    aiv1alpha1.ProjectMessageRoleUser,
				Content: tt.message,
			}})
			if got != tt.want {
				t.Fatalf("profile = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestProjectAssistantSemanticTurnClassifierUsesStructuredModelDecision(t *testing.T) {
	model := &repositoryFlowEinoChatModel{Steps: []repositoryFlowEinoModelStep{{
		Message: einoschema.AssistantMessage(`{"profile":"debug_fix","requires_current_state":true,"requires_runtime_state":true,"requests_mutation":true,"confidence":"high"}`, nil),
	}}}

	decision, err := classifyProjectAssistantTurnWithModel(context.Background(), model, []store.Message{{
		Role:    aiv1alpha1.ProjectMessageRoleUser,
		Content: "Please make the sign-in flow behave again",
	}})
	if err != nil {
		t.Fatalf("classifyProjectAssistantTurnWithModel returned error: %v", err)
	}
	if decision.Profile != projectAssistantTurnProfileDebugFix {
		t.Fatalf("profile = %q, want debug_fix", decision.Profile)
	}
	if !decision.RequiresCurrentState || !decision.RequiresRuntimeState || !decision.RequestsMutation {
		t.Fatalf("decision = %#v, want structured state and mutation flags", decision)
	}
	if decision.Confidence != projectAssistantTurnConfidenceHigh {
		t.Fatalf("confidence = %q, want high", decision.Confidence)
	}
	if len(model.Inputs) != 1 {
		t.Fatalf("model inputs = %d, want 1", len(model.Inputs))
	}
	if got := model.Inputs[0].ToolChoice; got != "none" {
		t.Fatalf("tool choice = %q, want none for classifier", got)
	}
	if len(model.Inputs[0].Tools) != 0 {
		t.Fatalf("classifier tools = %#v, want none", model.Inputs[0].Tools)
	}
}

func TestProjectAssistantSemanticTurnClassifierFallsBackOnLowConfidence(t *testing.T) {
	model := &repositoryFlowEinoChatModel{Steps: []repositoryFlowEinoModelStep{{
		Message: einoschema.AssistantMessage(`{"profile":"discussion","confidence":"low"}`, nil),
	}}}

	decision, err := classifyProjectAssistantTurnWithModel(context.Background(), model, []store.Message{{
		Role:    aiv1alpha1.ProjectMessageRoleUser,
		Content: "Please fix the login form",
	}})
	if err != nil {
		t.Fatalf("classifyProjectAssistantTurnWithModel returned error: %v", err)
	}
	if decision.Profile != projectAssistantTurnProfileDebugFix {
		t.Fatalf("profile = %q, want fallback debug_fix", decision.Profile)
	}
}

func TestProjectAssistantSemanticTurnClassifierNormalizesInconsistentMutationDecision(t *testing.T) {
	model := &repositoryFlowEinoChatModel{Steps: []repositoryFlowEinoModelStep{{
		Message: einoschema.AssistantMessage(`{"profile":"discussion","requires_current_state":true,"requires_runtime_state":false,"requests_mutation":true,"confidence":"high"}`, nil),
	}}}

	decision, err := classifyProjectAssistantTurnWithModel(context.Background(), model, []store.Message{{
		Role:    aiv1alpha1.ProjectMessageRoleUser,
		Content: "Please fix the login form",
	}})
	if err != nil {
		t.Fatalf("classifyProjectAssistantTurnWithModel returned error: %v", err)
	}
	if decision.Profile != projectAssistantTurnProfileDebugFix {
		t.Fatalf("profile = %q, want debug_fix from mutation fallback", decision.Profile)
	}
	if !decision.RequestsMutation {
		t.Fatalf("decision = %#v, want mutation preserved", decision)
	}
}

func TestProjectAssistantSemanticTurnClassifierNormalizesRuntimeStateDecision(t *testing.T) {
	model := &repositoryFlowEinoChatModel{Steps: []repositoryFlowEinoModelStep{{
		Message: einoschema.AssistantMessage(`{"profile":"guidance","requires_current_state":true,"requires_runtime_state":true,"requests_mutation":false,"confidence":"high"}`, nil),
	}}}

	decision, err := classifyProjectAssistantTurnWithModel(context.Background(), model, []store.Message{{
		Role:    aiv1alpha1.ProjectMessageRoleUser,
		Content: "Show me the current preview URL",
	}})
	if err != nil {
		t.Fatalf("classifyProjectAssistantTurnWithModel returned error: %v", err)
	}
	if decision.Profile != projectAssistantTurnProfileExploration {
		t.Fatalf("profile = %q, want exploration", decision.Profile)
	}
	if !decision.RequiresRuntimeState {
		t.Fatalf("decision = %#v, want runtime state preserved", decision)
	}
	policy := projectAssistantTurnPolicyForDecision(decision)
	registry := projectAssistantLocalToolRegistry(nil)
	previewTool, ok := registry.Spec(projectToolGetPreviewURL)
	if !ok {
		t.Fatal("get_preview_url missing from registry")
	}
	if !policy.AllowsTool(previewTool) {
		t.Fatalf("policy %#v rejected get_preview_url", policy)
	}
	deployTool, ok := registry.Spec(projectToolDeployProjectRuntime)
	if !ok {
		t.Fatal("deploy_project_runtime missing from registry")
	}
	if policy.AllowsTool(deployTool) {
		t.Fatalf("policy %#v allowed deploy_project_runtime", policy)
	}
}

func TestProjectAssistantSemanticTurnClassifierRejectsToolCalls(t *testing.T) {
	model := &repositoryFlowEinoChatModel{Steps: []repositoryFlowEinoModelStep{{
		Message: einoschema.AssistantMessage("", []einoschema.ToolCall{{
			ID:   "call-1",
			Type: "function",
			Function: einoschema.FunctionCall{
				Name:      projectToolReadProjectFile,
				Arguments: `{"path":"src/App.tsx"}`,
			},
		}}),
	}}}

	decision, err := classifyProjectAssistantTurnWithModel(context.Background(), model, []store.Message{{
		Role:    aiv1alpha1.ProjectMessageRoleUser,
		Content: "What files are in this app?",
	}})
	if err != nil {
		t.Fatalf("classifyProjectAssistantTurnWithModel returned error: %v", err)
	}
	if decision.Profile != projectAssistantTurnProfileExploration {
		t.Fatalf("profile = %q, want fallback exploration", decision.Profile)
	}
}

func TestProjectAssistantTurnProfileClassifierUsesLatestUserMessage(t *testing.T) {
	got := classifyProjectAssistantTurnProfile([]store.Message{
		{Role: aiv1alpha1.ProjectMessageRoleUser, Content: "Add a dashboard"},
		{Role: aiv1alpha1.ProjectMessageRoleAssistant, Content: "I can do that."},
		{Role: aiv1alpha1.ProjectMessageRoleUser, Content: "Actually, how should I think about the design?"},
	})
	if got != projectAssistantTurnProfileGuidance {
		t.Fatalf("profile = %q, want latest user guidance", got)
	}
}

func TestProjectAssistantModePromptsKeepDiscussionAndGuidanceToolFree(t *testing.T) {
	project := projectWithRepository("demo-repo", "demo", "github")
	project.Name = "demo-project"
	project.Spec.DisplayName = "Demo Project"
	repository := &ProjectRepositoryView{Ref: "demo-repo", Name: "demo", Status: projectRepositoryStatusReady, Ready: true}

	for _, profile := range []projectAssistantTurnProfile{
		projectAssistantTurnProfileDiscussion,
		projectAssistantTurnProfileGuidance,
	} {
		t.Run(string(profile), func(t *testing.T) {
			prompt := projectSystemPrompt(project, repository, profile)
			for _, unwanted := range []string{
				projectToolCheckProjectReadiness,
				projectToolPrepareProjectDeployment,
				projectToolDeployProjectRuntime,
				projectToolGetRuntimeStatus,
				projectToolGetPreviewURL,
				projectToolListProjectFiles,
				projectToolReadProjectFile,
				projectToolSearchProjectFiles,
				projectToolWriteFile,
				projectToolApplyPatch,
				projectToolMkdir,
				projectToolCommitProjectFiles,
				"tool_search",
			} {
				if strings.Contains(prompt, unwanted) {
					t.Fatalf("%s prompt unexpectedly mentions %q:\n%s", profile, unwanted, prompt)
				}
			}
		})
	}
}

func TestProjectAssistantModePromptsPutBuilderGuidanceOnlyOnWriteProfiles(t *testing.T) {
	project := projectWithRepository("demo-repo", "demo", "github")
	project.Name = "demo-project"
	project.Spec.DisplayName = "Demo Project"
	repository := &ProjectRepositoryView{Ref: "demo-repo", Name: "demo", Status: projectRepositoryStatusReady, Ready: true}

	for _, profile := range []projectAssistantTurnProfile{
		projectAssistantTurnProfileImplementation,
		projectAssistantTurnProfileDebugFix,
	} {
		t.Run(string(profile), func(t *testing.T) {
			prompt := projectSystemPrompt(project, repository, profile)
			for _, want := range []string{
				projectToolCheckProjectReadiness,
				projectToolRequestProjectPlanApproval,
				projectToolWriteFile,
				projectToolApplyPatch,
				projectToolCommitProjectFiles,
				"tool_search",
			} {
				if !strings.Contains(prompt, want) {
					t.Fatalf("%s prompt missing %q:\n%s", profile, want, prompt)
				}
			}
		})
	}

	for _, profile := range []projectAssistantTurnProfile{
		projectAssistantTurnProfileDiscussion,
		projectAssistantTurnProfileGuidance,
		projectAssistantTurnProfileExploration,
		projectAssistantTurnProfileDebugging,
	} {
		t.Run(string(profile), func(t *testing.T) {
			prompt := projectSystemPrompt(project, repository, profile)
			if strings.Contains(prompt, projectToolRequestProjectPlanApproval) || strings.Contains(prompt, projectToolCommitProjectFiles) {
				t.Fatalf("%s prompt should not contain builder approval/commit guidance:\n%s", profile, prompt)
			}
		})
	}
}

func TestProjectAssistantPromptRequiresEvidenceForProductCapabilities(t *testing.T) {
	project := projectWithRepository("demo-repo", "demo", "github")
	project.Name = "demo-project"
	project.Spec.DisplayName = "Demo Project"
	repository := &ProjectRepositoryView{Ref: "demo-repo", Name: "demo", Status: projectRepositoryStatusReady, Ready: true}

	prompt := projectSystemPrompt(project, repository, projectAssistantTurnProfileExploration)
	for _, want := range []string{
		"Do not invent App Studio product capabilities",
		"UI tabs",
		"cloud providers",
		"infrastructure templates",
		"I don't see that capability available in this workspace",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing product capability guardrail %q:\n%s", want, prompt)
		}
	}
	for _, unsupported := range []string{
		"AWS App Runner",
		"Google Cloud Run",
		"Cloud Connections",
		"Deployments tab",
		"Environments tab",
	} {
		if strings.Contains(prompt, unsupported) {
			t.Fatalf("prompt should not contain unsupported product example %q:\n%s", unsupported, prompt)
		}
	}
}

func TestProjectAssistantPromptFramesAppStudioAsBusinessUserEasyButton(t *testing.T) {
	project := projectWithRepository("demo-repo", "demo", "github")
	project.Name = "demo-project"
	project.Spec.DisplayName = "Demo Project"
	repository := &ProjectRepositoryView{Ref: "demo-repo", Name: "demo", Status: projectRepositoryStatusReady, Ready: true}

	prompt := projectSystemPrompt(project, repository, projectAssistantTurnProfileImplementation)
	lowerPrompt := strings.ToLower(prompt)
	for _, want := range []string{
		"business users",
		"non-technical",
		"easy button",
		"live development sandbox",
		"source changes run in that sandbox",
		"translate technical choices into business outcomes",
		"do not ask the user to choose databases, networking, infrastructure templates, or deployment architecture",
		"do not recommend a full application or runtime template just to satisfy a smaller need like persistent data",
		"consult the template's agent.usage guidance",
		"separate development sandbox guidance from production launch guidance",
	} {
		if !strings.Contains(lowerPrompt, strings.ToLower(want)) {
			t.Fatalf("prompt missing business-user App Studio guidance %q:\n%s", want, prompt)
		}
	}
}

func TestProjectAssistantPromptExplainsTemplateAgentUsageFitDecision(t *testing.T) {
	project := projectWithRepository("demo-repo", "demo", "github")
	project.Name = "demo-project"
	project.Spec.DisplayName = "Demo Project"
	repository := &ProjectRepositoryView{Ref: "demo-repo", Name: "demo", Status: projectRepositoryStatusReady, Ready: true}

	prompt := projectSystemPrompt(project, repository, projectAssistantTurnProfileImplementation)
	lowerPrompt := strings.ToLower(prompt)
	for _, want := range []string{
		"agent.usage as the provider-authored operating contract",
		"do not recommend a template merely because it contains one thing the user asked for",
		"application template",
		"includes postgres",
		"full 3-tier web app",
		"frontend and backend container images",
		"production-style app deployment template",
		"not a simple add a database to my sandbox app option",
	} {
		if !strings.Contains(lowerPrompt, strings.ToLower(want)) {
			t.Fatalf("prompt missing template agent.usage fit guidance %q:\n%s", want, prompt)
		}
	}
}

func TestProjectAssistantTurnPolicyAllowsExpectedToolBundles(t *testing.T) {
	tests := []struct {
		name       string
		profile    projectAssistantTurnProfile
		wantAllow  []string
		wantReject []string
	}{
		{
			name:       "discussion",
			profile:    projectAssistantTurnProfileDiscussion,
			wantReject: []string{projectToolCheckProjectReadiness, projectToolReadProjectFile, projectToolGetRuntimeStatus, projectToolWriteFile, projectToolCommitProjectFiles, projectToolAskFollowUp},
		},
		{
			name:       "guidance",
			profile:    projectAssistantTurnProfileGuidance,
			wantReject: []string{projectToolCheckProjectReadiness, projectToolReadProjectFile, projectToolGetRuntimeStatus, projectToolWriteFile, projectToolCommitProjectFiles, projectToolAskFollowUp},
		},
		{
			name:       "exploration",
			profile:    projectAssistantTurnProfileExploration,
			wantAllow:  []string{projectToolPlanProjectChanges, projectToolCheckProjectReadiness, projectToolPrepareProjectDeployment, projectToolListProjectFiles, projectToolReadProjectFile, projectToolSearchProjectFiles, projectToolInfrastructureListTemplates, projectToolInfrastructureDescribeTemplate, projectToolInfrastructureListInstances, projectToolInfrastructureGetInstance},
			wantReject: []string{projectToolGetRuntimeStatus, projectToolGetPreviewURL, projectToolDeployProjectRuntime, projectToolWriteFile, projectToolCommitProjectFiles, projectToolAskFollowUp, projectToolInfrastructureProvision},
		},
		{
			name:       "debugging",
			profile:    projectAssistantTurnProfileDebugging,
			wantAllow:  []string{projectToolCheckProjectReadiness, projectToolReadProjectFile, projectToolSearchProjectFiles, projectToolGetRuntimeStatus, projectToolGetPreviewURL, projectToolInfrastructureListTemplates, projectToolInfrastructureDescribeTemplate, projectToolInfrastructureListInstances, projectToolInfrastructureGetInstance},
			wantReject: []string{projectToolDeployProjectRuntime, projectToolWriteFile, projectToolCommitProjectFiles, projectToolAskFollowUp, projectToolInfrastructureProvision},
		},
		{
			name:       "debug fix",
			profile:    projectAssistantTurnProfileDebugFix,
			wantAllow:  []string{projectToolCheckProjectReadiness, projectToolReadProjectFile, projectToolGetRuntimeStatus, projectToolDeployProjectRuntime, projectToolRequestProjectPlanApproval, projectToolWriteFile, projectToolCommitProjectFiles, projectToolAskFollowUp, projectToolInfrastructureProvision},
			wantReject: nil,
		},
		{
			name:       "implementation",
			profile:    projectAssistantTurnProfileImplementation,
			wantAllow:  []string{projectToolCheckProjectReadiness, projectToolReadProjectFile, projectToolGetRuntimeStatus, projectToolDeployProjectRuntime, projectToolRequestProjectPlanApproval, projectToolWriteFile, projectToolCommitProjectFiles, projectToolAskFollowUp, projectToolInfrastructureProvision},
			wantReject: nil,
		},
	}

	registry := newProjectAssistantToolRegistry(projectAssistantLocalToolRegistry(nil).Tools(true)...)
	for _, tool := range projectAssistantMCPToolsForSpecs([]projectMCPTool{
		{Name: projectToolInfrastructureListTemplates, Description: "List templates", InputSchema: json.RawMessage(`{"type":"object"}`)},
		{Name: projectToolInfrastructureDescribeTemplate, Description: "Describe template", InputSchema: json.RawMessage(`{"type":"object"}`)},
		{Name: projectToolInfrastructureListInstances, Description: "List instances", InputSchema: json.RawMessage(`{"type":"object"}`)},
		{Name: projectToolInfrastructureGetInstance, Description: "Get instance", InputSchema: json.RawMessage(`{"type":"object"}`)},
		{Name: projectToolInfrastructureProvision, Description: "Provision", InputSchema: json.RawMessage(`{"type":"object"}`)},
	}) {
		registry = newProjectAssistantToolRegistry(append(registry.Tools(true), tool)...)
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			policy := projectAssistantTurnPolicyForProfile(tt.profile)
			for _, name := range tt.wantAllow {
				spec, ok := registry.Spec(name)
				if !ok {
					t.Fatalf("tool %s missing from registry", name)
				}
				if !policy.AllowsTool(spec) {
					t.Fatalf("%s policy rejected %s", tt.profile, name)
				}
			}
			for _, name := range tt.wantReject {
				spec, ok := registry.Spec(name)
				if !ok {
					t.Fatalf("tool %s missing from registry", name)
				}
				if policy.AllowsTool(spec) {
					t.Fatalf("%s policy allowed %s", tt.profile, name)
				}
			}
		})
	}
}

func TestProjectAssistantTurnPolicyAllowsRuntimeReadsForRuntimeStateExploration(t *testing.T) {
	registry := projectAssistantLocalToolRegistry(nil)
	policy := projectAssistantTurnPolicy{
		profile:              projectAssistantTurnProfileExploration,
		requiresRuntimeState: true,
	}
	for _, name := range []string{projectToolGetRuntimeStatus, projectToolGetPreviewURL} {
		spec, ok := registry.Spec(name)
		if !ok {
			t.Fatalf("tool %s missing from registry", name)
		}
		if !policy.AllowsTool(spec) {
			t.Fatalf("runtime-state exploration policy rejected %s", name)
		}
	}
	for _, name := range []string{projectToolDeployProjectRuntime, projectToolWriteFile, projectToolCommitProjectFiles} {
		spec, ok := registry.Spec(name)
		if !ok {
			t.Fatalf("tool %s missing from registry", name)
		}
		if policy.AllowsTool(spec) {
			t.Fatalf("runtime-state exploration policy allowed mutating tool %s", name)
		}
	}
}

var _ einomodel.BaseChatModel = (*repositoryFlowEinoChatModel)(nil)
