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
	"errors"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/cloudwego/eino/adk"
	einotool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"

	aiv1alpha1 "github.com/faroshq/provider-app-studio/apis/ai/v1alpha1"
	"github.com/faroshq/provider-app-studio/store"
	"github.com/faroshq/provider-app-studio/workspace"
)

func TestProjectEinoAssistantPhaseDerivation(t *testing.T) {
	tests := []struct {
		name     string
		req      projectAssistantRunRequest
		approved bool
		messages []*schema.Message
		want     projectEinoAssistantPhase
	}{
		{
			name: "no approved plan requires approval",
			want: projectEinoAssistantPhaseApproval,
		},
		{
			name:     "approved plan without workspace write mutates",
			approved: true,
			want:     projectEinoAssistantPhaseMutate,
		},
		{
			name:     "workspace write requires verification",
			approved: true,
			messages: []*schema.Message{projectEinoAssistantPhaseToolResult(projectToolWriteFile, `{"operation":"write_file"}`)},
			want:     projectEinoAssistantPhaseVerify,
		},
		{
			name:     "denied workspace write permits terminal report",
			approved: true,
			messages: []*schema.Message{projectEinoAssistantPhaseToolResult(projectToolWriteFile, "Tool call failed: permission denied: denied by user")},
			want:     projectEinoAssistantPhaseReport,
		},
		{
			name:     "non-ready verification requires repair",
			approved: true,
			messages: []*schema.Message{
				projectEinoAssistantPhaseToolResult(projectToolWriteFile, `{"operation":"write_file"}`),
				projectEinoAssistantPhaseToolResult(projectToolVerifyDevelopmentRuntime, `{"status":"provisioning"}`),
			},
			want: projectEinoAssistantPhaseRepair,
		},
		{
			name:     "reachable verification permits commit",
			approved: true,
			messages: []*schema.Message{
				projectEinoAssistantPhaseToolResult(projectToolWriteFile, `{"operation":"write_file"}`),
				projectEinoAssistantPhaseToolResult(projectToolVerifyDevelopmentRuntime, `{"status":"reachable"}`),
			},
			want: projectEinoAssistantPhaseCommit,
		},
		{
			name: "reachable verification reports during initial project creation",
			req: projectAssistantRunRequest{
				InitialApprovedPlan: &projectAssistantApprovedPlan{Steps: []string{"create project"}},
			},
			approved: true,
			messages: []*schema.Message{
				projectEinoAssistantPhaseToolResult(projectToolWriteFile, `{"operation":"write_file"}`),
				projectEinoAssistantPhaseToolResult(projectToolVerifyDevelopmentRuntime, `{"status":"available"}`),
			},
			want: projectEinoAssistantPhaseReport,
		},
		{
			name:     "successful commit reports completion",
			approved: true,
			messages: []*schema.Message{
				projectEinoAssistantPhaseToolResult(projectToolWriteFile, `{"operation":"write_file"}`),
				projectEinoAssistantPhaseToolResult(projectToolVerifyDevelopmentRuntime, `{"status":"ready"}`),
				projectEinoAssistantPhaseToolResult(projectToolCommitProjectFiles, `{"commitSHA":"abc123"}`),
			},
			want: projectEinoAssistantPhaseReport,
		},
		{
			name:     "successful direct runtime action reports completion",
			messages: []*schema.Message{projectEinoAssistantPhaseToolResult(projectToolRestartRuntime, `{"status":"restarted"}`)},
			want:     projectEinoAssistantPhaseReport,
		},
		{
			name:     "denied direct infrastructure action reports completion",
			messages: []*schema.Message{projectEinoAssistantPhaseToolResult(projectToolInfrastructureProvision, "Tool call failed: permission denied: denied by user")},
			want:     projectEinoAssistantPhaseReport,
		},
		{
			name:     "denied template selection reports completion",
			approved: true,
			messages: []*schema.Message{projectEinoAssistantPhaseToolResult(projectToolSelectTemplate, "Tool call failed: permission denied: denied by user")},
			want:     projectEinoAssistantPhaseReport,
		},
		{
			name:     "successful standalone template selection reports completion",
			messages: []*schema.Message{projectEinoAssistantPhaseToolResult(projectToolSelectTemplate, `{"template":"application"}`)},
			want:     projectEinoAssistantPhaseReport,
		},
		{
			name:     "successful template selection with persisted plan reports completion",
			approved: true,
			messages: []*schema.Message{projectEinoAssistantPhaseToolResult(projectToolSelectTemplate, `{"template":"application"}`)},
			want:     projectEinoAssistantPhaseReport,
		},
		{
			name:     "successful initial template selection continues source mutation",
			approved: true,
			req: projectAssistantRunRequest{
				InitialApprovedPlan: &projectAssistantApprovedPlan{Steps: []string{"create project"}},
			},
			messages: []*schema.Message{projectEinoAssistantPhaseToolResult(projectToolSelectTemplate, `{"template":"application"}`)},
			want:     projectEinoAssistantPhaseMutate,
		},
		{
			name:     "direct action after source write still requires verification",
			approved: true,
			messages: []*schema.Message{
				projectEinoAssistantPhaseToolResult(projectToolWriteFile, `{"operation":"write_file"}`),
				projectEinoAssistantPhaseToolResult(projectToolRestartRuntime, `{"status":"restarted"}`),
			},
			want: projectEinoAssistantPhaseVerify,
		},
		{
			name:     "later write invalidates earlier verification",
			approved: true,
			messages: []*schema.Message{
				projectEinoAssistantPhaseToolResult(projectToolWriteFile, `{"operation":"write_file"}`),
				projectEinoAssistantPhaseToolResult(projectToolVerifyDevelopmentRuntime, `{"status":"ready"}`),
				projectEinoAssistantPhaseToolResult(projectToolApplyPatch, `{"operation":"apply_patch"}`),
			},
			want: projectEinoAssistantPhaseVerify,
		},
		{
			name:     "later failed verification invalidates earlier reachable verification",
			approved: true,
			messages: []*schema.Message{
				projectEinoAssistantPhaseToolResult(projectToolWriteFile, `{"operation":"write_file"}`),
				projectEinoAssistantPhaseToolResult(projectToolVerifyDevelopmentRuntime, `{"status":"ready"}`),
				projectEinoAssistantPhaseToolResult(projectToolVerifyDevelopmentRuntime, "Tool call failed: runtime unavailable"),
			},
			want: projectEinoAssistantPhaseRepair,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runState := newProjectEinoAssistantRunState()
			if tt.approved {
				runState.ApprovePlan(projectAssistantApprovedPlan{Steps: []string{"implement change"}})
			}
			state := &adk.ChatModelAgentState{Messages: tt.messages}
			if got := projectEinoAssistantPhaseForState(tt.req, runState, state); got != tt.want {
				t.Fatalf("phase = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestProjectEinoAssistantPhaseAllowsDirectOperationalActions(t *testing.T) {
	tests := []struct {
		name  string
		phase projectEinoAssistantPhase
		tool  *schema.ToolInfo
		want  bool
	}{
		{
			name:  "approval allows restart runtime",
			phase: projectEinoAssistantPhaseApproval,
			tool:  projectEinoAssistantPhaseToolInfo(projectToolRestartRuntime, projectAssistantToolRiskRuntime, projectAssistantToolBundleRuntime),
			want:  true,
		},
		{
			name:  "mutate allows set runtime environment",
			phase: projectEinoAssistantPhaseMutate,
			tool:  projectEinoAssistantPhaseToolInfo(projectToolSetRuntimeEnv, projectAssistantToolRiskRuntime, projectAssistantToolBundleRuntime),
			want:  true,
		},
		{
			name:  "mutate allows promotion",
			phase: projectEinoAssistantPhaseMutate,
			tool:  projectEinoAssistantPhaseToolInfo(projectToolPromoteProject, projectAssistantToolRiskRuntime, projectAssistantToolBundleRuntime),
			want:  true,
		},
		{
			name:  "mutate allows build retry",
			phase: projectEinoAssistantPhaseMutate,
			tool:  projectEinoAssistantPhaseToolInfo(projectToolRebuildProject, projectAssistantToolRiskRuntime, projectAssistantToolBundleRuntime),
			want:  true,
		},
		{
			name:  "mutate allows infrastructure provision",
			phase: projectEinoAssistantPhaseMutate,
			tool:  projectEinoAssistantPhaseToolInfo(projectToolInfrastructureProvision, projectAssistantToolRiskRuntime, projectAssistantToolBundleInfrastructure),
			want:  true,
		},
		{
			name:  "approval allows runtime verification before action",
			phase: projectEinoAssistantPhaseApproval,
			tool:  projectEinoAssistantPhaseToolInfo(projectToolVerifyDevelopmentRuntime, projectAssistantToolRiskRead, projectAssistantToolBundleRuntime),
			want:  true,
		},
		{
			name:  "mutate allows build status before promotion",
			phase: projectEinoAssistantPhaseMutate,
			tool:  projectEinoAssistantPhaseToolInfo(projectToolCheckProjectBuild, projectAssistantToolRiskRead, projectAssistantToolBundleWorkflow),
			want:  true,
		},
		{
			name:  "mutate allows template discovery before provisioning",
			phase: projectEinoAssistantPhaseMutate,
			tool:  projectEinoAssistantPhaseToolInfo(projectToolInfrastructureListTemplates, projectAssistantToolRiskRead, projectAssistantToolBundleInfrastructure),
			want:  true,
		},
		{
			name:  "mutate rejects namespaced runtime lookalike",
			phase: projectEinoAssistantPhaseMutate,
			tool:  projectEinoAssistantPhaseToolInfo("provider__restart_runtime", projectAssistantToolRiskRuntime, projectAssistantToolBundleRuntime),
		},
		{
			name:  "mutate rejects wrong runtime metadata",
			phase: projectEinoAssistantPhaseMutate,
			tool:  projectEinoAssistantPhaseToolInfo(projectToolRestartRuntime, projectAssistantToolRiskWrite, projectAssistantToolBundleRuntime),
		},
		{
			name:  "mutate rejects action mislabeled as workspace read",
			phase: projectEinoAssistantPhaseMutate,
			tool:  projectEinoAssistantPhaseToolInfo(projectToolRestartRuntime, projectAssistantToolRiskRead, projectAssistantToolBundleWorkspaceRead),
		},
		{
			name:  "approval rejects namespaced action mislabeled as workspace read",
			phase: projectEinoAssistantPhaseApproval,
			tool:  projectEinoAssistantPhaseToolInfo("provider__restart_runtime", projectAssistantToolRiskRead, projectAssistantToolBundleWorkspaceRead),
		},
		{
			name:  "mutate rejects arbitrary runtime tool",
			phase: projectEinoAssistantPhaseMutate,
			tool:  projectEinoAssistantPhaseToolInfo("delete_runtime", projectAssistantToolRiskRuntime, projectAssistantToolBundleRuntime),
		},
		{
			name:  "repair rejects namespaced runtime action",
			phase: projectEinoAssistantPhaseRepair,
			tool:  projectEinoAssistantPhaseToolInfo("provider__restart_runtime", projectAssistantToolRiskRuntime, projectAssistantToolBundleRuntime),
		},
		{
			name:  "repair rejects arbitrary runtime action",
			phase: projectEinoAssistantPhaseRepair,
			tool:  projectEinoAssistantPhaseToolInfo("delete_runtime", projectAssistantToolRiskRuntime, projectAssistantToolBundleRuntime),
		},
		{
			name:  "repair allows canonical runtime action",
			phase: projectEinoAssistantPhaseRepair,
			tool:  projectEinoAssistantPhaseToolInfo(projectToolRestartRuntime, projectAssistantToolRiskRuntime, projectAssistantToolBundleRuntime),
			want:  true,
		},
		{
			name:  "repair allows bounded runtime read",
			phase: projectEinoAssistantPhaseRepair,
			tool:  projectEinoAssistantPhaseToolInfo(projectToolGetRuntimeStatus, projectAssistantToolRiskRead, projectAssistantToolBundleRuntime),
			want:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := projectEinoAssistantPhaseAllowsTool(tt.phase, nil, false, tt.tool, false, projectAssistantTurnPolicy{}); got != tt.want {
				t.Fatalf("allows %q = %t, want %t", tt.tool.Name, got, tt.want)
			}
		})
	}
}

func TestProjectEinoAssistantPhasePreservesInitialCreationReportAfterResume(t *testing.T) {
	runState := newProjectEinoAssistantRunState()
	runState.ApprovePlan(projectAssistantInitialCreationPlan())
	state := &adk.ChatModelAgentState{Messages: []*schema.Message{
		projectEinoAssistantPhaseToolResult(projectToolWriteFile, `{"operation":"write_file"}`),
		projectEinoAssistantPhaseToolResult(projectToolVerifyDevelopmentRuntime, `{"status":"reachable"}`),
	}}
	if got := projectEinoAssistantPhaseForState(projectAssistantRunRequest{}, runState, state); got != projectEinoAssistantPhaseReport {
		t.Fatalf("resumed initial-creation phase = %q, want report", got)
	}
}

func TestProjectEinoAssistantPhaseIgnoresUnsuccessfulToolResults(t *testing.T) {
	runState := newProjectEinoAssistantRunState()
	runState.ApprovePlan(projectAssistantApprovedPlan{Steps: []string{"edit"}})
	tests := []struct {
		name     string
		messages []*schema.Message
		want     projectEinoAssistantPhase
	}{
		{
			name:     "phase-unavailable write remains actionable",
			messages: []*schema.Message{projectEinoAssistantPhaseToolResult(projectToolWriteFile, "Tool call denied: write_file is unavailable in the current assistant phase")},
			want:     projectEinoAssistantPhaseMutate,
		},
		{
			name: "permission barrier verification does not advance write",
			messages: []*schema.Message{
				projectEinoAssistantPhaseToolResult(projectToolWriteFile, `{"operation":"write_file"}`),
				projectEinoAssistantPhaseToolResult(projectToolVerifyDevelopmentRuntime, "Tool call skipped: waiting for approval of a previous tool call"),
			},
			want: projectEinoAssistantPhaseVerify,
		},
		{
			name: "failed commit does not report completion",
			messages: []*schema.Message{
				projectEinoAssistantPhaseToolResult(projectToolWriteFile, `{"operation":"write_file"}`),
				projectEinoAssistantPhaseToolResult(projectToolVerifyDevelopmentRuntime, `{"status":"ready"}`),
				projectEinoAssistantPhaseToolResult(projectToolCommitProjectFiles, "Tool call failed: permission denied"),
			},
			want: projectEinoAssistantPhaseCommit,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state := &adk.ChatModelAgentState{Messages: tt.messages}
			if got := projectEinoAssistantPhaseForState(projectAssistantRunRequest{}, runState, state); got != tt.want {
				t.Fatalf("phase = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestProjectEinoAssistantPhaseMiddlewareFiltersTools(t *testing.T) {
	allTools := []*schema.ToolInfo{
		projectEinoAssistantPhaseToolInfo("read_workspace", projectAssistantToolRiskRead, projectAssistantToolBundleWorkspaceRead),
		projectEinoAssistantPhaseToolInfo(projectToolLS, projectAssistantToolRiskRead, projectAssistantToolBundleWorkspaceRead),
		projectEinoAssistantPhaseToolInfo(projectToolReadFile, projectAssistantToolRiskRead, projectAssistantToolBundleWorkspaceRead),
		projectEinoAssistantPhaseToolInfo(projectToolGlob, projectAssistantToolRiskRead, projectAssistantToolBundleWorkspaceRead),
		projectEinoAssistantPhaseToolInfo(projectToolGrep, projectAssistantToolRiskRead, projectAssistantToolBundleWorkspaceRead),
		projectEinoAssistantPhaseToolInfo("ask_for_input", projectAssistantToolRiskInput, projectAssistantToolBundleCollaboration),
		projectEinoAssistantPhaseToolInfo(projectToolAskFollowUp, projectAssistantToolRiskInput, projectAssistantToolBundleCollaboration),
		projectEinoAssistantPhaseToolInfo(projectToolRequestProjectPlanApproval, projectAssistantToolRiskPlan, projectAssistantToolBundleCollaboration),
		projectEinoAssistantPhaseToolInfo(projectToolPlanProjectChanges, projectAssistantToolRiskRead, projectAssistantToolBundleWorkflow),
		projectEinoAssistantPhaseToolInfo(projectToolCheckProjectReadiness, projectAssistantToolRiskRead, projectAssistantToolBundleWorkflow),
		projectEinoAssistantPhaseToolInfo(projectToolWriteFile, projectAssistantToolRiskWrite, projectAssistantToolBundleEdit),
		projectEinoAssistantPhaseToolInfo(projectToolApplyPatch, projectAssistantToolRiskWrite, projectAssistantToolBundleEdit),
		projectEinoAssistantPhaseToolInfo(projectToolGetRuntimeStatus, projectAssistantToolRiskRead, projectAssistantToolBundleRuntime),
		projectEinoAssistantPhaseToolInfo(projectToolRestartRuntime, projectAssistantToolRiskRuntime, projectAssistantToolBundleRuntime),
		projectEinoAssistantPhaseToolInfo(projectToolSetRuntimeEnv, projectAssistantToolRiskRuntime, projectAssistantToolBundleRuntime),
		projectEinoAssistantPhaseToolInfo(projectToolVerifyDevelopmentRuntime, projectAssistantToolRiskRead, projectAssistantToolBundleRuntime),
		projectEinoAssistantPhaseToolInfo(projectToolCommitProjectFiles, projectAssistantToolRiskCommit, projectAssistantToolBundleRepo),
		projectEinoAssistantPhaseToolInfo(projectToolCommitFiles, projectAssistantToolRiskCommit, projectAssistantToolBundleRepo),
		projectEinoAssistantPhaseToolInfo("invalid_runtime_commit", projectAssistantToolRiskCommit, projectAssistantToolBundleRuntime),
		{Name: projectEinoAssistantWriteTodosTool},
		{Name: projectEinoAssistantToolSearchTool},
	}

	tests := []struct {
		name         string
		req          projectAssistantRunRequest
		approvedPlan *projectAssistantApprovedPlan
		messages     []*schema.Message
		want         []string
	}{
		{
			name: "approval exposes reads plans and direct operational actions",
			want: []string{
				"read_workspace", projectToolLS, projectToolReadFile, projectToolGlob, projectToolGrep,
				"ask_for_input", projectToolAskFollowUp, projectToolRequestProjectPlanApproval,
				projectToolPlanProjectChanges, projectToolCheckProjectReadiness,
				projectToolGetRuntimeStatus, projectToolRestartRuntime, projectToolSetRuntimeEnv,
				projectToolVerifyDevelopmentRuntime, projectEinoAssistantToolSearchTool,
			},
		},
		{
			name:         "mutate exposes edits and direct operational tools",
			approvedPlan: &projectAssistantApprovedPlan{Steps: []string{"inspect", "edit"}},
			want: []string{
				projectToolAskFollowUp, projectToolWriteFile, projectToolApplyPatch,
				projectToolGetRuntimeStatus, projectToolRestartRuntime, projectToolSetRuntimeEnv,
				projectToolVerifyDevelopmentRuntime, projectEinoAssistantWriteTodosTool,
			},
		},
		{
			name:         "verify exposes edits verification and collaboration",
			approvedPlan: &projectAssistantApprovedPlan{Steps: []string{"inspect", "edit"}},
			messages: []*schema.Message{
				projectEinoAssistantPhaseToolResult(projectToolWriteFile, `{"operation":"write_file"}`),
			},
			want: []string{
				projectToolAskFollowUp, projectToolWriteFile, projectToolApplyPatch,
				projectToolVerifyDevelopmentRuntime, projectEinoAssistantWriteTodosTool,
			},
		},
		{
			name:         "repair exposes targeted reads edits runtime tools follow-up and todos",
			approvedPlan: &projectAssistantApprovedPlan{Steps: []string{"inspect", "repair"}},
			messages: []*schema.Message{
				projectEinoAssistantPhaseToolResult(projectToolWriteFile, `{"operation":"write_file"}`),
				projectEinoAssistantPhaseToolResult(projectToolVerifyDevelopmentRuntime, `{"status":"not_ready"}`),
			},
			want: []string{
				"read_workspace", projectToolLS, projectToolReadFile, projectToolGlob, projectToolGrep,
				projectToolAskFollowUp, projectToolWriteFile, projectToolApplyPatch,
				projectToolGetRuntimeStatus, projectToolRestartRuntime, projectToolSetRuntimeEnv,
				projectToolVerifyDevelopmentRuntime, projectEinoAssistantWriteTodosTool,
			},
		},
		{
			name:         "commit exposes only commit project files",
			approvedPlan: &projectAssistantApprovedPlan{Steps: []string{"edit"}},
			messages: []*schema.Message{
				projectEinoAssistantPhaseToolResult(projectToolWriteFile, `{"operation":"write_file"}`),
				projectEinoAssistantPhaseToolResult(projectToolVerifyDevelopmentRuntime, `{"status":"ready"}`),
			},
			want: []string{projectToolCommitProjectFiles},
		},
		{
			name: "report exposes no tools after initial creation verification",
			req: projectAssistantRunRequest{
				InitialApprovedPlan: &projectAssistantApprovedPlan{Steps: []string{"create"}},
			},
			approvedPlan: &projectAssistantApprovedPlan{Steps: []string{"create"}},
			messages: []*schema.Message{
				projectEinoAssistantPhaseToolResult(projectToolWriteFile, `{"operation":"write_file"}`),
				projectEinoAssistantPhaseToolResult(projectToolVerifyDevelopmentRuntime, `{"status":"ready"}`),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runState := newProjectEinoAssistantRunState()
			if tt.approvedPlan != nil {
				runState.ApprovePlan(*tt.approvedPlan)
			}
			state := &adk.ChatModelAgentState{
				Messages:          tt.messages,
				ToolInfos:         append([]*schema.ToolInfo(nil), allTools...),
				DeferredToolInfos: append([]*schema.ToolInfo(nil), allTools...),
			}
			middleware := projectEinoAssistantPhaseMiddleware(tt.req, runState)
			_, state, err := middleware.BeforeModelRewriteState(context.Background(), state, nil)
			if err != nil {
				t.Fatalf("BeforeModelRewriteState returned error: %v", err)
			}
			if got := projectEinoAssistantPhaseToolNames(state.ToolInfos); !projectEinoAssistantPhaseStringSlicesEqual(got, tt.want) {
				t.Fatalf("tool infos = %#v, want %#v", got, tt.want)
			}
			if got := projectEinoAssistantPhaseToolNames(state.DeferredToolInfos); !projectEinoAssistantPhaseStringSlicesEqual(got, tt.want) {
				t.Fatalf("deferred tool infos = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestProjectEinoAssistantPhaseRealFactoryInventoryAllowsCanonicalSourceAndOperationalTools(t *testing.T) {
	tools := projectEinoAssistantPhaseFactoryToolInfos(t)
	inventoryNames := projectEinoAssistantPhaseToolNames(tools)
	if !projectEinoAssistantPhaseToolNamesContain(inventoryNames, projectToolSelectTemplate) {
		t.Fatalf("factory inventory = %#v, want %s represented in the real inventory", inventoryNames, projectToolSelectTemplate)
	}
	if !projectEinoAssistantPhaseToolNamesContain(inventoryNames, projectToolHydrateWorkspace) {
		t.Fatalf("factory inventory = %#v, want %s represented in the real inventory", inventoryNames, projectToolHydrateWorkspace)
	}

	approvedPlan := &projectAssistantApprovedPlan{Steps: []string{"inspect", "edit"}}
	for _, tt := range []struct {
		phase projectEinoAssistantPhase
		want  []string
	}{
		{
			phase: projectEinoAssistantPhaseMutate,
			want: []string{
				projectToolAskFollowUp, projectToolWriteFile, projectToolApplyPatch, projectToolMkdir,
				projectToolInspectDevelopmentTemplates, projectToolSelectTemplate,
				projectToolVerifyDevelopmentRuntime, projectToolCheckProjectBuild,
				projectToolRestartRuntime, projectToolSetRuntimeEnv, projectToolPromoteProject, projectToolRebuildProject,
			},
		},
		{
			phase: projectEinoAssistantPhaseRepair,
			want:  []string{projectToolWriteFile, projectToolApplyPatch, projectToolMkdir, projectToolSelectTemplate},
		},
	} {
		t.Run(string(tt.phase), func(t *testing.T) {
			filtered := projectEinoAssistantPhaseFilterTools(tt.phase, approvedPlan, true, tools, false, projectAssistantTurnPolicy{})
			got := projectEinoAssistantPhaseToolNames(filtered)
			if projectEinoAssistantPhaseToolNamesContain(got, projectToolHydrateWorkspace) {
				t.Fatalf("%s tools = %#v, want %s excluded", tt.phase, got, projectToolHydrateWorkspace)
			}
			for _, want := range tt.want {
				if !projectEinoAssistantPhaseToolNamesContain(got, want) {
					t.Fatalf("%s tools = %#v, want canonical tool %s", tt.phase, got, want)
				}
			}
			for _, tool := range filtered {
				risk, bundle, ok := projectEinoAssistantPhaseToolMetadata(tool)
				if !ok {
					t.Fatalf("%s exposed tool %q without phase metadata", tt.phase, tool.Name)
				}
				if (bundle == projectAssistantToolBundleWorkspaceRead && risk == projectAssistantToolRiskRead) ||
					(projectEinoAssistantPhaseCanonicalEditTool(tool.Name) &&
						bundle == projectAssistantToolBundleEdit && risk == projectAssistantToolRiskWrite) ||
					projectEinoAssistantPhaseTemplateBootstrapTool(tool.Name, risk, bundle, true) ||
					projectEinoAssistantPhaseTemplateInspectionTool(tool.Name, risk, bundle, true) ||
					projectEinoAssistantPhaseDirectActionTool(tool.Name, risk, bundle) ||
					projectEinoAssistantPhaseOperationalReadTool(tool.Name, risk, bundle) ||
					(tool.Name == projectToolAskFollowUp && risk == projectAssistantToolRiskInput) {
					continue
				}
				t.Fatalf("%s exposed unexpected tool %q with risk %q bundle %q", tt.phase, tool.Name, risk, bundle)
			}
		})
	}
}

func TestProjectEinoAssistantPhaseInlineAdaptivePreregistersCommitWithoutInitialDisclosure(t *testing.T) {
	run := store.AssistantRun{
		ID:           "run-inline-auto",
		Mode:         store.AssistantRunModeAdaptive,
		ApprovalMode: store.AssistantApprovalModeAutoApprove,
	}
	req := projectAssistantRunRequest{
		WorkspaceScope: workspace.Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "project-a"},
		TurnProfile:    projectAssistantTurnProfileAdaptive,
		TurnPolicy:     projectAssistantTurnPolicyForProfile(projectAssistantTurnProfileAdaptive),
		ApprovalMode:   store.AssistantApprovalModeAutoApprove,
		AssistantRun:   &run,
	}
	infrastructureRead := projectAssistantToolFunc{spec: projectAssistantToolSpec{
		Name:       projectToolInfrastructureListTemplates,
		Risk:       projectAssistantToolRiskRead,
		Parameters: json.RawMessage(`{"type":"object"}`),
	}}
	tools := projectEinoAssistantPhaseFactoryToolInfosForRequestAndDiscovery(t, req, projectEinoAssistantToolDiscovery{
		IncludeCommitBridge: true,
		MCPTools:            []projectAssistantTool{infrastructureRead},
	})
	inventoryNames := projectEinoAssistantPhaseToolNames(tools)
	if !projectEinoAssistantPhaseToolNamesContain(inventoryNames, projectToolCommitProjectFiles) {
		t.Fatalf("factory inventory = %#v, want preregistered commit bridge", inventoryNames)
	}
	if !projectEinoAssistantPhaseToolNamesContain(inventoryNames, projectToolInfrastructureListTemplates) {
		t.Fatalf("factory inventory = %#v, want preregistered infrastructure read", inventoryNames)
	}

	approvalNames := projectEinoAssistantPhaseToolNames(projectEinoAssistantPhaseFilterTools(
		projectEinoAssistantPhaseApproval,
		nil,
		true,
		tools,
		true,
		req.TurnPolicy,
	))
	for _, hidden := range []string{
		projectToolCommitProjectFiles,
		projectToolWriteFile,
		projectToolRestartRuntime,
		projectToolSelectTemplate,
		projectToolInfrastructureListTemplates,
	} {
		if projectEinoAssistantPhaseToolNamesContain(approvalNames, hidden) {
			t.Fatalf("inline adaptive approval tools = %#v, must hide %s", approvalNames, hidden)
		}
	}
	if !projectEinoAssistantPhaseToolNamesContain(approvalNames, projectToolRequestProjectPlanApproval) {
		t.Fatalf("inline adaptive approval tools = %#v, want plan approval", approvalNames)
	}

	commitNames := projectEinoAssistantPhaseToolNames(projectEinoAssistantPhaseFilterTools(
		projectEinoAssistantPhaseCommit,
		&projectAssistantApprovedPlan{
			Steps:        []string{"update the app"},
			Capabilities: []string{projectAssistantCapabilityWorkspaceMutate},
		},
		false,
		tools,
		true,
		req.TurnPolicy,
	))
	if !projectEinoAssistantPhaseStringSlicesEqual(commitNames, []string{projectToolCommitProjectFiles}) {
		t.Fatalf("inline adaptive commit tools = %#v, want preregistered commit bridge only", commitNames)
	}

	mutateNames := projectEinoAssistantPhaseToolNames(projectEinoAssistantPhaseFilterTools(
		projectEinoAssistantPhaseMutate,
		&projectAssistantApprovedPlan{
			Steps:        []string{"update the app"},
			Capabilities: []string{projectAssistantCapabilityWorkspaceMutate},
		},
		false,
		tools,
		true,
		req.TurnPolicy,
	))
	if !projectEinoAssistantPhaseToolNamesContain(mutateNames, projectToolInfrastructureListTemplates) {
		t.Fatalf("inline adaptive mutate tools = %#v, want preregistered infrastructure read after approval", mutateNames)
	}
}

func TestProjectEinoAssistantPhaseMiddlewareReevaluatesTemplateBootstrapEligibility(t *testing.T) {
	project := &aiv1alpha1.Project{}
	runState := newProjectEinoAssistantRunState()
	runState.ApprovePlan(projectAssistantApprovedPlan{Steps: []string{"bind template", "build app"}})
	tools := projectEinoAssistantPhaseFactoryToolInfos(t)
	state := &adk.ChatModelAgentState{
		ToolInfos:         append([]*schema.ToolInfo(nil), tools...),
		DeferredToolInfos: append([]*schema.ToolInfo(nil), tools...),
	}
	middleware := projectEinoAssistantPhaseMiddleware(projectAssistantRunRequest{Project: project}, runState)

	_, state, err := middleware.BeforeModelRewriteState(context.Background(), state, nil)
	if err != nil {
		t.Fatalf("template-less filtering returned error: %v", err)
	}
	if got := projectEinoAssistantPhaseToolNames(state.ToolInfos); !projectEinoAssistantPhaseToolNamesContain(got, projectToolSelectTemplate) {
		t.Fatalf("template-less tools = %#v, want %s visible", got, projectToolSelectTemplate)
	}
	if got := projectEinoAssistantPhaseToolNames(state.ToolInfos); !projectEinoAssistantPhaseToolNamesContain(got, projectToolInspectDevelopmentTemplates) {
		t.Fatalf("template-less tools = %#v, want %s visible", got, projectToolInspectDevelopmentTemplates)
	}

	refreshProjectToolSnapshot(project, &aiv1alpha1.Project{
		Spec: aiv1alpha1.ProjectSpec{Template: &aiv1alpha1.ProjectTemplateSpec{Name: "application"}},
	})
	_, state, err = middleware.BeforeModelRewriteState(context.Background(), state, nil)
	if err != nil {
		t.Fatalf("bound-project filtering returned error: %v", err)
	}
	got := projectEinoAssistantPhaseToolNames(state.ToolInfos)
	if projectEinoAssistantPhaseToolNamesContain(got, projectToolSelectTemplate) {
		t.Fatalf("bound-project tools = %#v, want %s hidden after live project refresh", got, projectToolSelectTemplate)
	}
	if projectEinoAssistantPhaseToolNamesContain(got, projectToolInspectDevelopmentTemplates) {
		t.Fatalf("bound-project tools = %#v, want %s hidden after live project refresh", got, projectToolInspectDevelopmentTemplates)
	}
	if projectEinoAssistantPhaseToolNamesContain(got, projectToolHydrateWorkspace) {
		t.Fatalf("bound-project tools = %#v, want %s still hidden", got, projectToolHydrateWorkspace)
	}
}

func TestProjectEinoAssistantPhaseWrapperRechecksTemplateBootstrapEligibility(t *testing.T) {
	project := &aiv1alpha1.Project{}
	middleware := &projectEinoAssistantPhaseFilterMiddleware{
		BaseChatModelAgentMiddleware: &adk.BaseChatModelAgentMiddleware{},
		req:                          projectAssistantRunRequest{Project: project},
		phase:                        projectEinoAssistantPhaseMutate,
		approvedPlan:                 &projectAssistantApprovedPlan{Steps: []string{"bind template", "build app"}},
	}
	calls := 0
	wrapped, err := middleware.WrapInvokableToolCall(context.Background(), func(context.Context, string, ...einotool.Option) (string, error) {
		calls++
		return "template selected", nil
	}, &adk.ToolContext{Name: projectToolSelectTemplate})
	if err != nil {
		t.Fatalf("WrapInvokableToolCall returned error: %v", err)
	}

	result, err := wrapped(context.Background(), `{"template":"application"}`)
	if err != nil {
		t.Fatalf("template-less select returned error: %v", err)
	}
	if result != "template selected" || calls != 1 {
		t.Fatalf("template-less select result = %q calls = %d, want first selection executed", result, calls)
	}

	refreshProjectToolSnapshot(project, &aiv1alpha1.Project{
		Spec: aiv1alpha1.ProjectSpec{Template: &aiv1alpha1.ProjectTemplateSpec{Name: "application"}},
	})
	result, err = wrapped(context.Background(), `{"template":"application"}`)
	if err != nil {
		t.Fatalf("bound-project select returned error: %v", err)
	}
	if result != "Tool call denied: select_project_template is unavailable in the current assistant phase" || calls != 1 {
		t.Fatalf("bound-project select result = %q calls = %d, want second selection denied", result, calls)
	}
}

func TestProjectEinoAssistantPhaseTemplateBootstrapEligibility(t *testing.T) {
	tests := []struct {
		name    string
		project *aiv1alpha1.Project
		want    bool
	}{
		{name: "missing project fails closed"},
		{name: "nil template permits bootstrap", project: &aiv1alpha1.Project{}, want: true},
		{
			name: "blank template permits bootstrap",
			project: &aiv1alpha1.Project{
				Spec: aiv1alpha1.ProjectSpec{Template: &aiv1alpha1.ProjectTemplateSpec{}},
			},
			want: true,
		},
		{
			name: "whitespace template permits bootstrap",
			project: &aiv1alpha1.Project{
				Spec: aiv1alpha1.ProjectSpec{Template: &aiv1alpha1.ProjectTemplateSpec{Name: "  "}},
			},
			want: true,
		},
		{
			name: "bound template rejects bootstrap",
			project: &aiv1alpha1.Project{
				Spec: aiv1alpha1.ProjectSpec{Template: &aiv1alpha1.ProjectTemplateSpec{Name: "application"}},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := projectEinoAssistantPhaseTemplateBootstrapAllowed(tt.project); got != tt.want {
				t.Fatalf("template bootstrap allowed = %t, want %t", got, tt.want)
			}
		})
	}
}

func TestProjectEinoAssistantPhaseRequiresCanonicalExclusiveToolMetadata(t *testing.T) {
	tests := []struct {
		name                     string
		phase                    projectEinoAssistantPhase
		templateBootstrapAllowed bool
		tool                     *schema.ToolInfo
		want                     bool
	}{
		{
			name:  "mutate allows canonical write",
			phase: projectEinoAssistantPhaseMutate,
			tool:  projectEinoAssistantPhaseToolInfo(projectToolWriteFile, projectAssistantToolRiskWrite, projectAssistantToolBundleEdit),
			want:  true,
		},
		{
			name:  "mutate rejects namespaced write",
			phase: projectEinoAssistantPhaseMutate,
			tool:  projectEinoAssistantPhaseToolInfo("provider__write_file", projectAssistantToolRiskWrite, projectAssistantToolBundleEdit),
		},
		{
			name:  "mutate rejects hydrate workspace",
			phase: projectEinoAssistantPhaseMutate,
			tool:  projectEinoAssistantPhaseToolInfo(projectToolHydrateWorkspace, projectAssistantToolRiskWrite, projectAssistantToolBundleEdit),
		},
		{
			name:                     "mutate allows canonical template bootstrap for template-less project",
			phase:                    projectEinoAssistantPhaseMutate,
			templateBootstrapAllowed: true,
			tool:                     projectEinoAssistantPhaseToolInfo(projectToolSelectTemplate, projectAssistantToolRiskWrite, projectAssistantToolBundleWorkflow),
			want:                     true,
		},
		{
			name:                     "approval allows canonical template bootstrap to request permission",
			phase:                    projectEinoAssistantPhaseApproval,
			templateBootstrapAllowed: true,
			tool:                     projectEinoAssistantPhaseToolInfo(projectToolSelectTemplate, projectAssistantToolRiskWrite, projectAssistantToolBundleWorkflow),
			want:                     true,
		},
		{
			name:                     "approval rejects canonical template bootstrap mislabeled as read",
			phase:                    projectEinoAssistantPhaseApproval,
			templateBootstrapAllowed: true,
			tool:                     projectEinoAssistantPhaseToolInfo(projectToolSelectTemplate, projectAssistantToolRiskRead, projectAssistantToolBundleWorkspaceRead),
		},
		{
			name:                     "approval rejects namespaced template bootstrap mislabeled as read",
			phase:                    projectEinoAssistantPhaseApproval,
			templateBootstrapAllowed: true,
			tool:                     projectEinoAssistantPhaseToolInfo("provider__select_project_template", projectAssistantToolRiskRead, projectAssistantToolBundleWorkspaceRead),
		},
		{
			name:                     "mutate allows template inspection for template-less project",
			phase:                    projectEinoAssistantPhaseMutate,
			templateBootstrapAllowed: true,
			tool:                     projectEinoAssistantPhaseToolInfo(projectToolInspectDevelopmentTemplates, projectAssistantToolRiskRead, projectAssistantToolBundleWorkflow),
			want:                     true,
		},
		{
			name:  "mutate rejects template inspection for bound project",
			phase: projectEinoAssistantPhaseMutate,
			tool:  projectEinoAssistantPhaseToolInfo(projectToolInspectDevelopmentTemplates, projectAssistantToolRiskRead, projectAssistantToolBundleWorkflow),
		},
		{
			name:                     "mutate rejects template inspection with wrong bundle",
			phase:                    projectEinoAssistantPhaseMutate,
			templateBootstrapAllowed: true,
			tool:                     projectEinoAssistantPhaseToolInfo(projectToolInspectDevelopmentTemplates, projectAssistantToolRiskRead, projectAssistantToolBundleInfrastructure),
		},
		{
			name:                     "approval allows template inspection for template-less project",
			phase:                    projectEinoAssistantPhaseApproval,
			templateBootstrapAllowed: true,
			tool:                     projectEinoAssistantPhaseToolInfo(projectToolInspectDevelopmentTemplates, projectAssistantToolRiskRead, projectAssistantToolBundleWorkflow),
			want:                     true,
		},
		{
			name:  "approval rejects template inspection for bound project",
			phase: projectEinoAssistantPhaseApproval,
			tool:  projectEinoAssistantPhaseToolInfo(projectToolInspectDevelopmentTemplates, projectAssistantToolRiskRead, projectAssistantToolBundleWorkflow),
		},
		{
			name:                     "approval rejects namespaced template inspection",
			phase:                    projectEinoAssistantPhaseApproval,
			templateBootstrapAllowed: true,
			tool:                     projectEinoAssistantPhaseToolInfo("provider__inspect_development_templates", projectAssistantToolRiskRead, projectAssistantToolBundleWorkflow),
		},
		{
			name:  "mutate rejects canonical template bootstrap for bound project",
			phase: projectEinoAssistantPhaseMutate,
			tool:  projectEinoAssistantPhaseToolInfo(projectToolSelectTemplate, projectAssistantToolRiskWrite, projectAssistantToolBundleWorkflow),
		},
		{
			name:                     "mutate rejects namespaced template bootstrap",
			phase:                    projectEinoAssistantPhaseMutate,
			templateBootstrapAllowed: true,
			tool:                     projectEinoAssistantPhaseToolInfo("provider__select_project_template", projectAssistantToolRiskWrite, projectAssistantToolBundleWorkflow),
		},
		{
			name:                     "mutate rejects template bootstrap with infrastructure bundle",
			phase:                    projectEinoAssistantPhaseMutate,
			templateBootstrapAllowed: true,
			tool:                     projectEinoAssistantPhaseToolInfo(projectToolSelectTemplate, projectAssistantToolRiskWrite, projectAssistantToolBundleInfrastructure),
		},
		{
			name:                     "repair allows canonical template bootstrap for template-less project",
			phase:                    projectEinoAssistantPhaseRepair,
			templateBootstrapAllowed: true,
			tool:                     projectEinoAssistantPhaseToolInfo(projectToolSelectTemplate, projectAssistantToolRiskWrite, projectAssistantToolBundleWorkflow),
			want:                     true,
		},
		{
			name:  "repair rejects canonical template bootstrap for bound project",
			phase: projectEinoAssistantPhaseRepair,
			tool:  projectEinoAssistantPhaseToolInfo(projectToolSelectTemplate, projectAssistantToolRiskWrite, projectAssistantToolBundleWorkflow),
		},
		{
			name:                     "repair rejects case template bootstrap lookalike",
			phase:                    projectEinoAssistantPhaseRepair,
			templateBootstrapAllowed: true,
			tool:                     projectEinoAssistantPhaseToolInfo("SELECT_PROJECT_TEMPLATE", projectAssistantToolRiskWrite, projectAssistantToolBundleWorkflow),
		},
		{
			name:  "repair rejects namespaced mkdir",
			phase: projectEinoAssistantPhaseRepair,
			tool:  projectEinoAssistantPhaseToolInfo("provider__mkdir", projectAssistantToolRiskWrite, projectAssistantToolBundleEdit),
		},
		{
			name:  "verify allows canonical verifier metadata",
			phase: projectEinoAssistantPhaseVerify,
			tool:  projectEinoAssistantPhaseToolInfo(projectToolVerifyDevelopmentRuntime, projectAssistantToolRiskRead, projectAssistantToolBundleRuntime),
			want:  true,
		},
		{
			name:  "verify allows another edit before verification",
			phase: projectEinoAssistantPhaseVerify,
			tool:  projectEinoAssistantPhaseToolInfo(projectToolWriteFile, projectAssistantToolRiskWrite, projectAssistantToolBundleEdit),
			want:  true,
		},
		{
			name:  "verify rejects workspace reads before verification",
			phase: projectEinoAssistantPhaseVerify,
			tool:  projectEinoAssistantPhaseToolInfo(projectToolReadFile, projectAssistantToolRiskRead, projectAssistantToolBundleWorkspaceRead),
		},
		{
			name:  "verify rejects namespaced verifier",
			phase: projectEinoAssistantPhaseVerify,
			tool:  projectEinoAssistantPhaseToolInfo("provider__verify_development_runtime", projectAssistantToolRiskRead, projectAssistantToolBundleRuntime),
		},
		{
			name:  "verify rejects case lookalike",
			phase: projectEinoAssistantPhaseVerify,
			tool:  projectEinoAssistantPhaseToolInfo("VERIFY_DEVELOPMENT_RUNTIME", projectAssistantToolRiskRead, projectAssistantToolBundleRuntime),
		},
		{
			name:  "verify rejects wrong risk",
			phase: projectEinoAssistantPhaseVerify,
			tool:  projectEinoAssistantPhaseToolInfo(projectToolVerifyDevelopmentRuntime, projectAssistantToolRiskRuntime, projectAssistantToolBundleRuntime),
		},
		{
			name:  "verify rejects wrong bundle",
			phase: projectEinoAssistantPhaseVerify,
			tool:  projectEinoAssistantPhaseToolInfo(projectToolVerifyDevelopmentRuntime, projectAssistantToolRiskRead, projectAssistantToolBundleWorkspaceRead),
		},
		{
			name:  "commit allows canonical commit metadata",
			phase: projectEinoAssistantPhaseCommit,
			tool:  projectEinoAssistantPhaseToolInfo(projectToolCommitProjectFiles, projectAssistantToolRiskCommit, projectAssistantToolBundleRepo),
			want:  true,
		},
		{
			name:  "commit rejects namespaced commit",
			phase: projectEinoAssistantPhaseCommit,
			tool:  projectEinoAssistantPhaseToolInfo("code__commit_project_files", projectAssistantToolRiskCommit, projectAssistantToolBundleRepo),
		},
		{
			name:  "commit rejects case lookalike",
			phase: projectEinoAssistantPhaseCommit,
			tool:  projectEinoAssistantPhaseToolInfo("COMMIT_PROJECT_FILES", projectAssistantToolRiskCommit, projectAssistantToolBundleRepo),
		},
		{
			name:  "commit rejects wrong bundle",
			phase: projectEinoAssistantPhaseCommit,
			tool:  projectEinoAssistantPhaseToolInfo(projectToolCommitProjectFiles, projectAssistantToolRiskCommit, projectAssistantToolBundleRuntime),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := projectEinoAssistantPhaseAllowsTool(tt.phase, nil, tt.templateBootstrapAllowed, tt.tool, false, projectAssistantTurnPolicy{}); got != tt.want {
				t.Fatalf("allows %q = %t, want %t", tt.tool.Name, got, tt.want)
			}
		})
	}
}

func TestProjectEinoAssistantPhaseFiltersTemplateToolsForReadOnlyProfiles(t *testing.T) {
	tools := []*schema.ToolInfo{
		projectEinoAssistantPhaseToolInfo(projectToolReadFile, projectAssistantToolRiskRead, projectAssistantToolBundleWorkspaceRead),
		projectEinoAssistantPhaseToolInfo(projectToolGetRuntimeStatus, projectAssistantToolRiskRead, projectAssistantToolBundleRuntime),
		projectEinoAssistantPhaseToolInfo(projectToolInspectDevelopmentTemplates, projectAssistantToolRiskRead, projectAssistantToolBundleWorkflow),
		projectEinoAssistantPhaseToolInfo(projectToolInspectDevelopmentTemplates, projectAssistantToolRiskRead, projectAssistantToolBundleWorkspaceRead),
		projectEinoAssistantPhaseToolInfo(projectToolSelectTemplate, projectAssistantToolRiskWrite, projectAssistantToolBundleWorkflow),
		projectEinoAssistantPhaseToolInfo("provider__select_project_template", projectAssistantToolRiskRead, projectAssistantToolBundleWorkspaceRead),
		projectEinoAssistantPhaseToolInfo("provider__inspect_development_templates", projectAssistantToolRiskRead, projectAssistantToolBundleWorkflow),
		projectEinoAssistantPhaseToolInfo(projectToolRestartRuntime, projectAssistantToolRiskRuntime, projectAssistantToolBundleRuntime),
		projectEinoAssistantPhaseToolInfo("provider__restart_runtime", projectAssistantToolRiskRead, projectAssistantToolBundleWorkspaceRead),
		projectEinoAssistantPhaseToolInfo("delete_runtime", projectAssistantToolRiskRuntime, projectAssistantToolBundleRuntime),
		projectEinoAssistantPhaseToolInfo(projectToolGetRuntimeStatus, projectAssistantToolRiskRead, projectAssistantToolBundleWorkspaceRead),
		projectEinoAssistantPhaseToolInfo("provider__get_runtime_status", projectAssistantToolRiskRead, projectAssistantToolBundleRuntime),
	}
	tests := []struct {
		name    string
		project *aiv1alpha1.Project
		want    []string
	}{
		{
			name:    "template-less exploration can inspect but not select",
			project: &aiv1alpha1.Project{},
			want:    []string{projectToolReadFile, projectToolGetRuntimeStatus, projectToolInspectDevelopmentTemplates},
		},
		{
			name: "bound exploration hides template inspection and selection",
			project: &aiv1alpha1.Project{
				Spec: aiv1alpha1.ProjectSpec{Template: &aiv1alpha1.ProjectTemplateSpec{Name: "application"}},
			},
			want: []string{projectToolReadFile, projectToolGetRuntimeStatus},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := projectAssistantRunRequest{
				Project:     tt.project,
				TurnProfile: projectAssistantTurnProfileExploration,
				TurnPolicy:  projectAssistantTurnPolicyForProfile(projectAssistantTurnProfileExploration),
			}
			middleware := projectEinoAssistantPhaseMiddleware(req, newProjectEinoAssistantRunState())
			state := &adk.ChatModelAgentState{
				ToolInfos:         append([]*schema.ToolInfo(nil), tools...),
				DeferredToolInfos: append([]*schema.ToolInfo(nil), tools...),
			}
			_, state, err := middleware.BeforeModelRewriteState(context.Background(), state, nil)
			if err != nil {
				t.Fatalf("BeforeModelRewriteState returned error: %v", err)
			}
			if got := projectEinoAssistantPhaseToolNames(state.ToolInfos); !projectEinoAssistantPhaseStringSlicesEqual(got, tt.want) {
				t.Fatalf("tool infos = %#v, want %#v", got, tt.want)
			}
			if got := projectEinoAssistantPhaseToolNames(state.DeferredToolInfos); !projectEinoAssistantPhaseStringSlicesEqual(got, tt.want) {
				t.Fatalf("deferred tool infos = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestProjectEinoAssistantPhaseGatesTemplateToolInvocationForReadOnlyProfiles(t *testing.T) {
	tests := []struct {
		name      string
		project   *aiv1alpha1.Project
		tool      string
		toolInfo  *schema.ToolInfo
		wantCalls int
		want      string
	}{
		{
			name:      "template-less exploration can inspect",
			project:   &aiv1alpha1.Project{},
			tool:      projectToolInspectDevelopmentTemplates,
			toolInfo:  projectEinoAssistantPhaseToolInfo(projectToolInspectDevelopmentTemplates, projectAssistantToolRiskRead, projectAssistantToolBundleWorkflow),
			wantCalls: 1,
			want:      "called",
		},
		{
			name:     "template-less exploration rejects inspection with wrong metadata",
			project:  &aiv1alpha1.Project{},
			tool:     projectToolInspectDevelopmentTemplates,
			toolInfo: projectEinoAssistantPhaseToolInfo(projectToolInspectDevelopmentTemplates, projectAssistantToolRiskRead, projectAssistantToolBundleWorkspaceRead),
			want:     "Tool call denied: inspect_development_templates is unavailable in the current assistant phase",
		},
		{
			name:    "template-less exploration cannot select",
			project: &aiv1alpha1.Project{},
			tool:    projectToolSelectTemplate,
			want:    "Tool call denied: select_project_template is unavailable in the current assistant phase",
		},
		{
			name:     "template-less exploration cannot invoke namespaced template selection",
			project:  &aiv1alpha1.Project{},
			tool:     "provider__select_project_template",
			toolInfo: projectEinoAssistantPhaseToolInfo("provider__select_project_template", projectAssistantToolRiskRead, projectAssistantToolBundleWorkspaceRead),
			want:     "Tool call denied: select_project_template is unavailable in the current assistant phase",
		},
		{
			name: "bound exploration cannot inspect",
			project: &aiv1alpha1.Project{
				Spec: aiv1alpha1.ProjectSpec{Template: &aiv1alpha1.ProjectTemplateSpec{Name: "application"}},
			},
			tool: projectToolInspectDevelopmentTemplates,
			want: "Tool call denied: inspect_development_templates is unavailable in the current assistant phase",
		},
		{
			name: "bound exploration cannot invoke namespaced template inspection",
			project: &aiv1alpha1.Project{
				Spec: aiv1alpha1.ProjectSpec{Template: &aiv1alpha1.ProjectTemplateSpec{Name: "application"}},
			},
			tool:     "provider__inspect_development_templates",
			toolInfo: projectEinoAssistantPhaseToolInfo("provider__inspect_development_templates", projectAssistantToolRiskRead, projectAssistantToolBundleWorkflow),
			want:     "Tool call denied: inspect_development_templates is unavailable in the current assistant phase",
		},
		{
			name:     "exploration cannot invoke canonical runtime action",
			project:  &aiv1alpha1.Project{},
			tool:     projectToolRestartRuntime,
			toolInfo: projectEinoAssistantPhaseToolInfo(projectToolRestartRuntime, projectAssistantToolRiskRuntime, projectAssistantToolBundleRuntime),
			want:     "Tool call denied: restart_runtime is unavailable in the current assistant phase",
		},
		{
			name:     "exploration cannot invoke namespaced action mislabeled as read",
			project:  &aiv1alpha1.Project{},
			tool:     "provider__restart_runtime",
			toolInfo: projectEinoAssistantPhaseToolInfo("provider__restart_runtime", projectAssistantToolRiskRead, projectAssistantToolBundleWorkspaceRead),
			want:     "Tool call denied: restart_runtime is unavailable in the current assistant phase",
		},
		{
			name:     "exploration cannot invoke arbitrary runtime action",
			project:  &aiv1alpha1.Project{},
			tool:     "delete_runtime",
			toolInfo: projectEinoAssistantPhaseToolInfo("delete_runtime", projectAssistantToolRiskRuntime, projectAssistantToolBundleRuntime),
			want:     "Tool call denied: delete_runtime is unavailable in the current assistant phase",
		},
		{
			name:     "exploration rejects runtime status with wrong bundle",
			project:  &aiv1alpha1.Project{},
			tool:     projectToolGetRuntimeStatus,
			toolInfo: projectEinoAssistantPhaseToolInfo(projectToolGetRuntimeStatus, projectAssistantToolRiskRead, projectAssistantToolBundleWorkspaceRead),
			want:     "Tool call denied: get_runtime_status is unavailable in the current assistant phase",
		},
		{
			name:     "exploration rejects namespaced runtime status read",
			project:  &aiv1alpha1.Project{},
			tool:     "provider__get_runtime_status",
			toolInfo: projectEinoAssistantPhaseToolInfo("provider__get_runtime_status", projectAssistantToolRiskRead, projectAssistantToolBundleRuntime),
			want:     "Tool call denied: get_runtime_status is unavailable in the current assistant phase",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			middleware := &projectEinoAssistantPhaseFilterMiddleware{
				BaseChatModelAgentMiddleware: &adk.BaseChatModelAgentMiddleware{},
				req: projectAssistantRunRequest{
					Project:     tt.project,
					TurnProfile: projectAssistantTurnProfileExploration,
					TurnPolicy:  projectAssistantTurnPolicyForProfile(projectAssistantTurnProfileExploration),
				},
				toolInfos: []*schema.ToolInfo{tt.toolInfo},
			}
			calls := 0
			wrapped, err := middleware.WrapInvokableToolCall(
				context.Background(),
				func(context.Context, string, ...einotool.Option) (string, error) {
					calls++
					return "called", nil
				},
				&adk.ToolContext{Name: tt.tool},
			)
			if err != nil {
				t.Fatalf("WrapInvokableToolCall returned error: %v", err)
			}
			result, err := wrapped(context.Background(), `{}`)
			if err != nil {
				t.Fatalf("wrapped invocation returned error: %v", err)
			}
			if result != tt.want || calls != tt.wantCalls {
				t.Fatalf("result = %q calls = %d, want %q calls = %d", result, calls, tt.want, tt.wantCalls)
			}
		})
	}
}

func TestProjectEinoAssistantPhaseMiddlewareGatesHiddenToolExecution(t *testing.T) {
	tests := []struct {
		name         string
		phase        projectEinoAssistantPhase
		approvedPlan *projectAssistantApprovedPlan
		tool         *schema.ToolInfo
		wantCalls    int
		wantResult   string
	}{
		{
			name:       "approval rejects hidden write",
			phase:      projectEinoAssistantPhaseApproval,
			tool:       projectEinoAssistantPhaseToolInfo(projectToolWriteFile, projectAssistantToolRiskWrite, projectAssistantToolBundleEdit),
			wantResult: "Tool call denied: write_file is unavailable in the current assistant phase",
		},
		{
			name:       "approval rejects hidden todo",
			phase:      projectEinoAssistantPhaseApproval,
			tool:       &schema.ToolInfo{Name: projectEinoAssistantWriteTodosTool},
			wantResult: "Tool call denied: write_todos is unavailable in the current assistant phase",
		},
		{
			name:         "mutate rejects another workspace read",
			phase:        projectEinoAssistantPhaseMutate,
			approvedPlan: &projectAssistantApprovedPlan{Steps: []string{"inspect", "edit"}},
			tool:         projectEinoAssistantPhaseToolInfo(projectToolReadFile, projectAssistantToolRiskRead, projectAssistantToolBundleWorkspaceRead),
			wantResult:   "Tool call denied: read_file is unavailable in the current assistant phase",
		},
		{
			name:         "mutate rejects hidden workflow",
			phase:        projectEinoAssistantPhaseMutate,
			approvedPlan: &projectAssistantApprovedPlan{Steps: []string{"inspect", "edit"}},
			tool:         projectEinoAssistantPhaseToolInfo(projectToolHydrateWorkspace, projectAssistantToolRiskWrite, projectAssistantToolBundleWorkflow),
			wantResult:   "Tool call denied: hydrate_workspace is unavailable in the current assistant phase",
		},
		{
			name:       "verify rejects hidden commit",
			phase:      projectEinoAssistantPhaseVerify,
			tool:       projectEinoAssistantPhaseToolInfo(projectToolCommitProjectFiles, projectAssistantToolRiskCommit, projectAssistantToolBundleRepo),
			wantResult: "Tool call denied: commit_project_files is unavailable in the current assistant phase",
		},
		{
			name:       "verify rejects workspace read",
			phase:      projectEinoAssistantPhaseVerify,
			tool:       projectEinoAssistantPhaseToolInfo(projectToolReadFile, projectAssistantToolRiskRead, projectAssistantToolBundleWorkspaceRead),
			wantResult: "Tool call denied: read_file is unavailable in the current assistant phase",
		},
		{
			name:       "verify rejects transformed hidden commit",
			phase:      projectEinoAssistantPhaseVerify,
			tool:       projectEinoAssistantPhaseToolInfo(projectToolCodeCommitFiles, projectAssistantToolRiskCommit, projectAssistantToolBundleRepo),
			wantResult: "Tool call denied: commit_files is unavailable in the current assistant phase",
		},
		{
			name:       "commit executes verified commit",
			phase:      projectEinoAssistantPhaseCommit,
			tool:       projectEinoAssistantPhaseToolInfo(projectToolCommitProjectFiles, projectAssistantToolRiskCommit, projectAssistantToolBundleRepo),
			wantCalls:  1,
			wantResult: "todo recorded",
		},
		{
			name:       "commit rejects transformed commit",
			phase:      projectEinoAssistantPhaseCommit,
			tool:       projectEinoAssistantPhaseToolInfo(projectToolCodeCommitFiles, projectAssistantToolRiskCommit, projectAssistantToolBundleRepo),
			wantResult: "Tool call denied: commit_files is unavailable in the current assistant phase",
		},
		{
			name:       "commit rejects namespaced canonical lookalike",
			phase:      projectEinoAssistantPhaseCommit,
			tool:       projectEinoAssistantPhaseToolInfo("code__commit_project_files", projectAssistantToolRiskCommit, projectAssistantToolBundleRepo),
			wantResult: "Tool call denied: commit_project_files is unavailable in the current assistant phase",
		},
		{
			name:       "commit rejects case canonical lookalike",
			phase:      projectEinoAssistantPhaseCommit,
			tool:       projectEinoAssistantPhaseToolInfo("COMMIT_PROJECT_FILES", projectAssistantToolRiskCommit, projectAssistantToolBundleRepo),
			wantResult: "Tool call denied: commit_project_files is unavailable in the current assistant phase",
		},
		{
			name:       "initial creation report rejects hidden commit",
			phase:      projectEinoAssistantPhaseReport,
			tool:       projectEinoAssistantPhaseToolInfo(projectToolCommitProjectFiles, projectAssistantToolRiskCommit, projectAssistantToolBundleRepo),
			wantResult: "Tool call denied: commit_project_files is unavailable in the current assistant phase",
		},
		{
			name:  "one-step mutate rejects hidden todo",
			phase: projectEinoAssistantPhaseMutate,
			tool:  &schema.ToolInfo{Name: projectEinoAssistantWriteTodosTool},
			approvedPlan: &projectAssistantApprovedPlan{
				Steps: []string{"make the small change"},
			},
			wantResult: "Tool call denied: write_todos is unavailable in the current assistant phase",
		},
		{
			name:  "multi-step mutate executes todo",
			phase: projectEinoAssistantPhaseMutate,
			tool:  &schema.ToolInfo{Name: projectEinoAssistantWriteTodosTool},
			approvedPlan: &projectAssistantApprovedPlan{
				Steps: []string{"inspect", "edit"},
			},
			wantCalls:  1,
			wantResult: "todo recorded",
		},
		{
			name:  "multi-step repair executes todo",
			phase: projectEinoAssistantPhaseRepair,
			tool:  &schema.ToolInfo{Name: projectEinoAssistantWriteTodosTool},
			approvedPlan: &projectAssistantApprovedPlan{
				Steps: []string{"diagnose", "repair"},
			},
			wantCalls:  1,
			wantResult: "todo recorded",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			middleware := &projectEinoAssistantPhaseFilterMiddleware{
				BaseChatModelAgentMiddleware: &adk.BaseChatModelAgentMiddleware{},
				phase:                        tt.phase,
				approvedPlan:                 tt.approvedPlan,
				toolInfos:                    []*schema.ToolInfo{tt.tool},
			}
			calls := 0
			wrapped, err := middleware.WrapInvokableToolCall(context.Background(), func(context.Context, string, ...einotool.Option) (string, error) {
				calls++
				return "todo recorded", nil
			}, &adk.ToolContext{Name: tt.tool.Name})
			if err != nil {
				t.Fatalf("WrapInvokableToolCall returned error: %v", err)
			}
			result, err := wrapped(context.Background(), `{"todos":[]}`)
			if err != nil {
				t.Fatalf("wrapped %s returned error: %v", tt.tool.Name, err)
			}
			if result != tt.wantResult {
				t.Fatalf("wrapped %s result = %q, want %q", tt.tool.Name, result, tt.wantResult)
			}
			if calls != tt.wantCalls {
				t.Fatalf("inner %s calls = %d, want %d", tt.tool.Name, calls, tt.wantCalls)
			}
		})
	}
}

func TestProjectEinoAssistantPhaseWriteTodosEmitsSanitizedProgressAfterSuccess(t *testing.T) {
	var statuses []string
	var plans []projectAssistantPlanSnapshot
	runState := newProjectEinoAssistantRunState()
	middleware := &projectEinoAssistantPhaseFilterMiddleware{
		BaseChatModelAgentMiddleware: &adk.BaseChatModelAgentMiddleware{},
		req: projectAssistantRunRequest{StreamCallbacks: projectAssistantStreamCallbacks{
			OnStatus: func(status string) {
				statuses = append(statuses, status)
			},
			OnPlan: func(plan projectAssistantPlanSnapshot) {
				plans = append(plans, plan)
			},
		}},
		runState: runState,
		phase:    projectEinoAssistantPhaseMutate,
		approvedPlan: &projectAssistantApprovedPlan{
			Steps: []string{"inspect", "edit", "verify"},
		},
		toolInfos: []*schema.ToolInfo{{Name: projectEinoAssistantWriteTodosTool}},
	}
	wrapped, err := middleware.WrapInvokableToolCall(
		context.Background(),
		func(context.Context, string, ...einotool.Option) (string, error) {
			return "todo recorded", nil
		},
		&adk.ToolContext{Name: projectEinoAssistantWriteTodosTool},
	)
	if err != nil {
		t.Fatalf("WrapInvokableToolCall returned error: %v", err)
	}

	result, err := wrapped(context.Background(), `{"todos":[
		{"content":"Inspect files","activeForm":"Inspecting files","status":"completed"},
		{"content":"Update filters\n token=secret-value","activeForm":"Updating filters\n token=secret-value","status":"in_progress"},
		{"content":"Verify preview","activeForm":"Verifying preview","status":"pending"}
	]}`)
	if err != nil {
		t.Fatalf("write_todos returned error: %v", err)
	}
	if result != "todo recorded" {
		t.Fatalf("write_todos result = %q, want endpoint result", result)
	}
	if len(statuses) != 1 {
		t.Fatalf("statuses = %#v, want one progress update", statuses)
	}
	if got, want := statuses[0], "Updating filters token=[REDACTED] · 1 of 3 steps"; got != want {
		t.Fatalf("status = %q, want %q", got, want)
	}
	wantPlan := projectAssistantPlanSnapshot{Steps: []projectAssistantPlanStep{
		{Content: "Inspect files", ActiveForm: "Inspecting files", Status: "completed"},
		{Content: "Update filters token=[REDACTED]", ActiveForm: "Updating filters token=[REDACTED]", Status: "in_progress"},
		{Content: "Verify preview", ActiveForm: "Verifying preview", Status: "pending"},
	}}
	if len(plans) != 1 || !reflect.DeepEqual(plans[0], wantPlan) {
		t.Fatalf("plans = %#v, want %#v", plans, wantPlan)
	}
	if strings.Contains(statuses[0], "secret-value") || strings.Contains(statuses[0], `"todos"`) {
		t.Fatalf("status exposed raw todo data: %q", statuses[0])
	}
}

func TestProjectEinoAssistantPhaseWriteTodosProgressRespectsMinimalDisclosure(t *testing.T) {
	prev := projectAssistantToolDisclosureMinimal
	projectAssistantToolDisclosureMinimal = true
	t.Cleanup(func() { projectAssistantToolDisclosureMinimal = prev })

	var statuses []string
	var plans []projectAssistantPlanSnapshot
	runState := newProjectEinoAssistantRunState()
	middleware := &projectEinoAssistantPhaseFilterMiddleware{
		BaseChatModelAgentMiddleware: &adk.BaseChatModelAgentMiddleware{},
		req: projectAssistantRunRequest{StreamCallbacks: projectAssistantStreamCallbacks{
			OnStatus: func(status string) {
				statuses = append(statuses, status)
			},
			OnPlan: func(plan projectAssistantPlanSnapshot) {
				plans = append(plans, plan)
			},
		}},
		runState: runState,
		phase:    projectEinoAssistantPhaseMutate,
		approvedPlan: &projectAssistantApprovedPlan{
			Steps: []string{"inspect", "edit"},
		},
		toolInfos: []*schema.ToolInfo{{Name: projectEinoAssistantWriteTodosTool}},
	}
	wrapped, err := middleware.WrapInvokableToolCall(
		context.Background(),
		func(context.Context, string, ...einotool.Option) (string, error) {
			return "todo recorded", nil
		},
		&adk.ToolContext{Name: projectEinoAssistantWriteTodosTool},
	)
	if err != nil {
		t.Fatalf("WrapInvokableToolCall returned error: %v", err)
	}

	_, err = wrapped(context.Background(), `{"todos":[
		{"content":"Inspect payroll","activeForm":"Reading src/payroll.csv","status":"completed"},
		{"content":"Update payroll","activeForm":"Updating src/payroll.csv","status":"in_progress"}
	]}`)
	if err != nil {
		t.Fatalf("write_todos returned error: %v", err)
	}
	if len(statuses) != 1 || statuses[0] != "1 of 2 steps complete" {
		t.Fatalf("minimal-disclosure statuses = %#v, want count-only progress", statuses)
	}
	if strings.Contains(statuses[0], "payroll") {
		t.Fatalf("minimal-disclosure status exposed active todo: %q", statuses[0])
	}
	if len(plans) != 0 {
		t.Fatalf("minimal-disclosure plans = %#v, want none", plans)
	}
	if progress := runState.PlanProgress(); len(progress.Steps) != 2 ||
		progress.Steps[0].Status != "completed" ||
		progress.Steps[1].Status != "in_progress" {
		t.Fatalf("minimal-disclosure internal progress = %#v, want durable full progress", progress)
	}
}

func TestProjectEinoAssistantPhaseWriteTodosProgressValidation(t *testing.T) {
	tests := []struct {
		name        string
		phase       projectEinoAssistantPhase
		arguments   string
		endpointErr error
		wantStatus  string
		wantPlan    bool
	}{
		{
			name:       "completed list reports completion",
			phase:      projectEinoAssistantPhaseMutate,
			arguments:  `{"todos":[{"content":"Inspect","activeForm":"Inspecting","status":"completed"},{"content":"Edit","activeForm":"Editing","status":"completed"}]}`,
			wantStatus: "2 of 2 steps complete",
			wantPlan:   true,
		},
		{
			name:       "no active item reports coarse count",
			phase:      projectEinoAssistantPhaseVerify,
			arguments:  `{"todos":[{"content":"Inspect","activeForm":"Inspecting","status":"completed"},{"content":"Verify","activeForm":"Verifying","status":"pending"}]}`,
			wantStatus: "1 of 2 steps complete",
			wantPlan:   true,
		},
		{
			name:      "malformed JSON is ignored",
			phase:     projectEinoAssistantPhaseMutate,
			arguments: `{"todos":`,
		},
		{
			name:      "unsupported status is ignored",
			phase:     projectEinoAssistantPhaseMutate,
			arguments: `{"todos":[{"content":"Inspect","activeForm":"Inspecting","status":"blocked"},{"content":"Edit","activeForm":"Editing","status":"pending"}]}`,
		},
		{
			name:      "multiple active items are ignored",
			phase:     projectEinoAssistantPhaseMutate,
			arguments: `{"todos":[{"content":"Inspect","activeForm":"Inspecting","status":"in_progress"},{"content":"Edit","activeForm":"Editing","status":"in_progress"}]}`,
		},
		{
			name:      "empty list is ignored",
			phase:     projectEinoAssistantPhaseMutate,
			arguments: `{"todos":[]}`,
		},
		{
			name:      "empty sanitized required content is ignored",
			phase:     projectEinoAssistantPhaseMutate,
			arguments: `{"todos":[{"content":" \n ","activeForm":"Inspecting","status":"in_progress"},{"content":"Verify","activeForm":"Verifying","status":"pending"}]}`,
		},
		{
			name:      "more than fifty todos emits no plan",
			phase:     projectEinoAssistantPhaseMutate,
			arguments: `{"todos":[` + strings.Repeat(`{"content":"Inspect","activeForm":"Inspecting","status":"pending"},`, projectEinoAssistantTodoProgressMaxItems) + `{"content":"Inspect","activeForm":"Inspecting","status":"pending"}]}`,
		},
		{
			name:        "endpoint failure is ignored",
			phase:       projectEinoAssistantPhaseMutate,
			arguments:   `{"todos":[{"content":"Inspect","activeForm":"Inspecting","status":"in_progress"},{"content":"Edit","activeForm":"Editing","status":"pending"}]}`,
			endpointErr: errors.New("write failed"),
		},
		{
			name:      "denied phase is ignored",
			phase:     projectEinoAssistantPhaseApproval,
			arguments: `{"todos":[{"content":"Inspect","activeForm":"Inspecting","status":"in_progress"},{"content":"Edit","activeForm":"Editing","status":"pending"}]}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var statuses []string
			var plans []projectAssistantPlanSnapshot
			middleware := &projectEinoAssistantPhaseFilterMiddleware{
				BaseChatModelAgentMiddleware: &adk.BaseChatModelAgentMiddleware{},
				req: projectAssistantRunRequest{StreamCallbacks: projectAssistantStreamCallbacks{
					OnStatus: func(status string) {
						statuses = append(statuses, status)
					},
					OnPlan: func(plan projectAssistantPlanSnapshot) {
						plans = append(plans, plan)
					},
				}},
				phase: tt.phase,
				approvedPlan: &projectAssistantApprovedPlan{
					Steps: []string{"inspect", "edit"},
				},
				toolInfos: []*schema.ToolInfo{{Name: projectEinoAssistantWriteTodosTool}},
			}
			wrapped, err := middleware.WrapInvokableToolCall(
				context.Background(),
				func(context.Context, string, ...einotool.Option) (string, error) {
					return "todo recorded", tt.endpointErr
				},
				&adk.ToolContext{Name: projectEinoAssistantWriteTodosTool},
			)
			if err != nil {
				t.Fatalf("WrapInvokableToolCall returned error: %v", err)
			}

			_, _ = wrapped(context.Background(), tt.arguments)
			if tt.wantStatus == "" {
				if len(statuses) != 0 {
					t.Fatalf("statuses = %#v, want none", statuses)
				}
			} else if len(statuses) != 1 || statuses[0] != tt.wantStatus {
				t.Fatalf("statuses = %#v, want %q", statuses, tt.wantStatus)
			}
			if tt.wantPlan && len(plans) != 1 {
				t.Fatalf("plans = %#v, want one plan", plans)
			}
			if !tt.wantPlan && len(plans) != 0 {
				t.Fatalf("plans = %#v, want none", plans)
			}
		})
	}
}

func TestProjectEinoAssistantTodoProgressLabelBoundsUnicodeSafely(t *testing.T) {
	active := strings.Repeat("🧭", projectEinoAssistantTodoProgressMaxLabelBytes)
	_, status := projectEinoAssistantTodoProgress(`{"todos":[
		{"content":"Update","activeForm":"`+active+`","status":"in_progress"},
		{"content":"Verify","activeForm":"Verifying","status":"pending"}
	]}`, true)
	if !utf8.ValidString(status) {
		t.Fatalf("status is not valid UTF-8: %q", status)
	}
	label := strings.Split(status, " · ")[0]
	if len(label) > projectEinoAssistantTodoProgressMaxLabelBytes {
		t.Fatalf("label bytes = %d, want at most %d", len(label), projectEinoAssistantTodoProgressMaxLabelBytes)
	}
	if got := utf8.RuneCountInString(label); got > projectEinoAssistantTodoProgressMaxLabelBytes {
		t.Fatalf("label rune count = %d, want at most %d", got, projectEinoAssistantTodoProgressMaxLabelBytes)
	}
}

func TestProjectEinoAssistantPhaseMiddlewareRestoresVerifiedCommitPhaseForResume(t *testing.T) {
	runState := newProjectEinoAssistantRunState()
	middleware := projectEinoAssistantPhaseMiddleware(projectAssistantRunRequest{
		Continuation: &projectAssistantCheckpointState{Messages: []chatMessage{
			{Role: string(schema.Tool), Name: projectToolWriteFile, Content: `{"operation":"write_file"}`},
			{Role: string(schema.Tool), Name: projectToolVerifyDevelopmentRuntime, Content: `{"status":"ready"}`},
		}, Eino: &projectAssistantEinoCheckpointState{ToolName: projectToolCommitProjectFiles}},
	}, runState).(*projectEinoAssistantPhaseFilterMiddleware)
	if middleware.phase != projectEinoAssistantPhaseCommit {
		t.Fatalf("resumed phase = %q, want commit", middleware.phase)
	}
	if middleware.approvedPlan != nil || runState.ApprovedPlan() != nil {
		t.Fatalf("resumed approval = %#v, want commit resume without restoring the consumed plan", middleware.approvedPlan)
	}
	calls := 0
	wrapped, err := middleware.WrapInvokableToolCall(context.Background(), func(context.Context, string, ...einotool.Option) (string, error) {
		calls++
		return "commit requested", nil
	}, &adk.ToolContext{Name: projectToolCommitProjectFiles})
	if err != nil {
		t.Fatalf("WrapInvokableToolCall returned error: %v", err)
	}
	result, err := wrapped(context.Background(), `{}`)
	if err != nil {
		t.Fatalf("wrapped commit returned error: %v", err)
	}
	if result != "commit requested" || calls != 1 {
		t.Fatalf("resumed commit result = %q calls = %d, want execution", result, calls)
	}
}

func TestProjectEinoAssistantPhaseMiddlewareRejectsUnverifiedCommitResume(t *testing.T) {
	middleware := projectEinoAssistantPhaseMiddleware(projectAssistantRunRequest{
		Continuation: &projectAssistantCheckpointState{
			Messages: []chatMessage{
				{Role: string(schema.Tool), Name: projectToolWriteFile, Content: `{"operation":"write_file"}`},
			},
			Eino: &projectAssistantEinoCheckpointState{ToolName: projectToolCommitProjectFiles},
		},
	}, newProjectEinoAssistantRunState()).(*projectEinoAssistantPhaseFilterMiddleware)
	calls := 0
	wrapped, err := middleware.WrapInvokableToolCall(context.Background(), func(context.Context, string, ...einotool.Option) (string, error) {
		calls++
		return "commit requested", nil
	}, &adk.ToolContext{Name: projectToolCommitProjectFiles})
	if err != nil {
		t.Fatalf("WrapInvokableToolCall returned error: %v", err)
	}
	result, err := wrapped(context.Background(), `{}`)
	if err != nil {
		t.Fatalf("wrapped commit returned error: %v", err)
	}
	if result != "Tool call denied: commit_project_files is unavailable in the current assistant phase" || calls != 0 {
		t.Fatalf("unverified resumed commit result = %q calls = %d, want denial", result, calls)
	}
}

func TestProjectEinoAssistantPhaseMiddlewareRestoresToolsAfterApproval(t *testing.T) {
	runState := newProjectEinoAssistantRunState()
	middleware := projectEinoAssistantPhaseMiddleware(projectAssistantRunRequest{}, runState)
	state := &adk.ChatModelAgentState{ToolInfos: []*schema.ToolInfo{
		projectEinoAssistantPhaseToolInfo(projectToolRequestProjectPlanApproval, projectAssistantToolRiskPlan, projectAssistantToolBundleCollaboration),
		projectEinoAssistantPhaseToolInfo(projectToolWriteFile, projectAssistantToolRiskWrite, projectAssistantToolBundleEdit),
	}}

	_, state, err := middleware.BeforeModelRewriteState(context.Background(), state, nil)
	if err != nil {
		t.Fatalf("approval filtering returned error: %v", err)
	}
	if got := projectEinoAssistantPhaseToolNames(state.ToolInfos); !projectEinoAssistantPhaseStringSlicesEqual(got, []string{projectToolRequestProjectPlanApproval}) {
		t.Fatalf("approval tool infos = %#v, want only approval tool", got)
	}

	runState.ApprovePlan(projectAssistantApprovedPlan{Steps: []string{"edit"}})
	_, state, err = middleware.BeforeModelRewriteState(context.Background(), state, nil)
	if err != nil {
		t.Fatalf("mutate filtering returned error: %v", err)
	}
	if got := projectEinoAssistantPhaseToolNames(state.ToolInfos); !projectEinoAssistantPhaseStringSlicesEqual(got, []string{projectToolWriteFile}) {
		t.Fatalf("mutate tool infos = %#v, want recovered workspace write", got)
	}
}

func TestProjectEinoAssistantPhaseNoProgressWarnsThenStops(t *testing.T) {
	middleware := &projectEinoAssistantPhaseFilterMiddleware{
		runState: newProjectEinoAssistantRunState(),
	}
	state := &adk.ChatModelAgentState{}
	for call := 1; call <= projectEinoAssistantApprovalModelCallLimit; call++ {
		if err := middleware.enforceSemanticProgress(state, projectEinoAssistantPhaseApproval); err != nil {
			t.Fatalf("call %d returned error: %v", call, err)
		}
	}
	if len(state.Messages) != 1 ||
		!strings.Contains(state.Messages[0].Content, "Finish bounded inspection now") {
		t.Fatalf("messages = %#v, want one pre-limit progress warning", state.Messages)
	}
	if err := middleware.enforceSemanticProgress(state, projectEinoAssistantPhaseApproval); !errors.Is(err, errProjectAssistantNoProgress) {
		t.Fatalf("post-limit error = %v, want no-progress sentinel", err)
	}
}

func TestProjectEinoAssistantPhaseNoProgressResetsOnSemanticProgress(t *testing.T) {
	runState := newProjectEinoAssistantRunState()
	middleware := &projectEinoAssistantPhaseFilterMiddleware{runState: runState}
	state := &adk.ChatModelAgentState{}

	exhaustPhaseBudget := func(phase projectEinoAssistantPhase) {
		t.Helper()
		for call := 0; call < projectEinoAssistantPhaseModelCallLimit(phase); call++ {
			if err := middleware.enforceSemanticProgress(state, phase); err != nil {
				t.Fatalf("%s call %d returned error: %v", phase, call+1, err)
			}
		}
	}

	exhaustPhaseBudget(projectEinoAssistantPhaseApproval)
	if err := middleware.enforceSemanticProgress(state, projectEinoAssistantPhaseMutate); err != nil {
		t.Fatalf("phase transition did not reset budget: %v", err)
	}

	for call := 1; call < projectEinoAssistantMutateModelCallLimit; call++ {
		if err := middleware.enforceSemanticProgress(state, projectEinoAssistantPhaseMutate); err != nil {
			t.Fatalf("mutate call %d returned error: %v", call+1, err)
		}
	}
	runState.RecordSourceMutation()
	if err := middleware.enforceSemanticProgress(state, projectEinoAssistantPhaseMutate); err != nil {
		t.Fatalf("source mutation did not reset budget: %v", err)
	}
}

func TestProjectEinoAssistantPhaseNoProgressDoesNotSuspendAfterMutation(t *testing.T) {
	middleware := &projectEinoAssistantPhaseFilterMiddleware{runState: newProjectEinoAssistantRunState()}
	state := &adk.ChatModelAgentState{}
	for _, phase := range []projectEinoAssistantPhase{
		projectEinoAssistantPhaseVerify,
		projectEinoAssistantPhaseRepair,
	} {
		for call := 0; call < maxAssistantDeepIterations+1; call++ {
			if err := middleware.enforceSemanticProgress(state, phase); err != nil {
				t.Fatalf("%s call %d returned error: %v", phase, call+1, err)
			}
		}
	}
}

func TestProjectEinoAssistantPhaseNoProgressDoesNotApplyToReadOnlyTurns(t *testing.T) {
	middleware := projectEinoAssistantPhaseMiddleware(projectAssistantRunRequest{
		TurnProfile: projectAssistantTurnProfileExploration,
		TurnPolicy:  projectAssistantTurnPolicyForProfile(projectAssistantTurnProfileExploration),
	}, newProjectEinoAssistantRunState())
	state := &adk.ChatModelAgentState{ToolInfos: []*schema.ToolInfo{
		projectEinoAssistantPhaseToolInfo(projectToolReadFile, projectAssistantToolRiskRead, projectAssistantToolBundleWorkspaceRead),
	}}
	for call := 0; call < projectEinoAssistantApprovalModelCallLimit*2; call++ {
		var err error
		_, state, err = middleware.BeforeModelRewriteState(context.Background(), state, nil)
		if err != nil {
			t.Fatalf("read-only call %d returned error: %v", call+1, err)
		}
	}
}

func TestProjectEinoAssistantPhaseMiddlewareRestoresCanonicalToolsAfterResume(t *testing.T) {
	ctx := context.Background()
	runState := newProjectEinoAssistantRunState()
	tools := []einotool.BaseTool{
		projectEinoAssistantPhaseBaseTool(projectToolRequestProjectPlanApproval, projectAssistantToolRiskPlan, projectAssistantToolBundleCollaboration),
		projectEinoAssistantPhaseBaseTool(projectToolWriteFile, projectAssistantToolRiskWrite, projectAssistantToolBundleEdit),
	}
	runCtx := &adk.ChatModelAgentContext{Tools: tools}
	filesystemMiddleware, err := projectEinoAssistantFilesystemMiddleware(ctx, workspace.NewFileStore(t.TempDir()), projectAssistantRunRequest{
		WorkspaceScope: workspace.Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: "demo"},
		TurnPolicy:     projectAssistantTurnPolicyForProfile(projectAssistantTurnProfileImplementation),
	})
	if err != nil {
		t.Fatalf("projectEinoAssistantFilesystemMiddleware returned error: %v", err)
	}
	_, runCtx, err = filesystemMiddleware.BeforeAgent(ctx, runCtx)
	if err != nil {
		t.Fatalf("filesystem BeforeAgent returned error: %v", err)
	}
	persistedApprovalState := &adk.ChatModelAgentState{ToolInfos: []*schema.ToolInfo{
		projectEinoAssistantPhaseToolInfo(projectToolRequestProjectPlanApproval, projectAssistantToolRiskPlan, projectAssistantToolBundleCollaboration),
	}}

	resumedMiddleware := projectEinoAssistantPhaseMiddleware(projectAssistantRunRequest{}, runState)
	if _, _, err := resumedMiddleware.BeforeAgent(ctx, runCtx); err != nil {
		t.Fatalf("BeforeAgent returned error: %v", err)
	}
	runState.ApprovePlan(projectAssistantApprovedPlan{Steps: []string{"edit"}})
	_, state, err := resumedMiddleware.BeforeModelRewriteState(ctx, persistedApprovalState, nil)
	if err != nil {
		t.Fatalf("mutate filtering returned error: %v", err)
	}
	want := []string{projectToolWriteFile}
	if got := projectEinoAssistantPhaseToolNames(state.ToolInfos); !projectEinoAssistantPhaseStringSlicesEqual(got, want) {
		t.Fatalf("resumed mutate tool infos = %#v, want recovered registry write %#v", got, want)
	}
}

func TestProjectEinoAssistantPhaseMiddlewareRestoresReadOnlyPolicyToolsFromLegacyCheckpoint(t *testing.T) {
	tools := []einotool.BaseTool{
		projectEinoAssistantPhaseBaseTool(projectToolReadFile, projectAssistantToolRiskRead, projectAssistantToolBundleWorkspaceRead),
		projectEinoAssistantPhaseBaseTool(projectToolGetRuntimeStatus, projectAssistantToolRiskRead, projectAssistantToolBundleRuntime),
		projectEinoAssistantPhaseBaseTool(projectToolGetPreviewURL, projectAssistantToolRiskRead, projectAssistantToolBundleRuntime),
	}
	runCtx := &adk.ChatModelAgentContext{Tools: tools}
	persistedPrunedState := &adk.ChatModelAgentState{ToolInfos: []*schema.ToolInfo{
		projectEinoAssistantPhaseToolInfo(projectToolReadFile, projectAssistantToolRiskRead, projectAssistantToolBundleWorkspaceRead),
	}}
	middleware := projectEinoAssistantPhaseMiddleware(projectAssistantRunRequest{
		TurnPolicy: projectAssistantTurnPolicyForProfile(projectAssistantTurnProfileDebugging),
	}, newProjectEinoAssistantRunState())
	if _, _, err := middleware.BeforeAgent(context.Background(), runCtx); err != nil {
		t.Fatalf("BeforeAgent returned error: %v", err)
	}

	_, state, err := middleware.BeforeModelRewriteState(context.Background(), persistedPrunedState, nil)
	if err != nil {
		t.Fatalf("BeforeModelRewriteState returned error: %v", err)
	}
	want := []string{projectToolReadFile, projectToolGetRuntimeStatus, projectToolGetPreviewURL}
	if got := projectEinoAssistantPhaseToolNames(state.ToolInfos); !projectEinoAssistantPhaseStringSlicesEqual(got, want) {
		t.Fatalf("restored read-only tools = %#v, want legacy checkpoint restored to %#v", got, want)
	}
}

func TestProjectEinoAssistantPhaseAllowsToolSearchOnlyWhileDiscoveryCanAdvanceWork(t *testing.T) {
	toolSearch := &schema.ToolInfo{Name: "tool_search"}
	for _, tt := range []struct {
		phase projectEinoAssistantPhase
		want  bool
	}{
		{phase: projectEinoAssistantPhaseApproval, want: true},
		{phase: projectEinoAssistantPhaseMutate, want: false},
		{phase: projectEinoAssistantPhaseVerify, want: false},
		{phase: projectEinoAssistantPhaseRepair, want: false},
		{phase: projectEinoAssistantPhaseCommit, want: false},
		{phase: projectEinoAssistantPhaseReport, want: false},
	} {
		t.Run(string(tt.phase), func(t *testing.T) {
			if got := projectEinoAssistantPhaseAllowsTool(tt.phase, nil, false, toolSearch, false, projectAssistantTurnPolicy{}); got != tt.want {
				t.Fatalf("tool_search allowed = %t, want %t", got, tt.want)
			}
		})
	}
}

func TestProjectEinoAssistantPhaseFilesystemMetadataUsesCanonicalExactNames(t *testing.T) {
	for _, name := range []string{
		projectToolLS,
		projectToolReadFile,
		projectToolGlob,
		projectToolGrep,
		" " + projectToolReadFile + " ",
	} {
		t.Run(name, func(t *testing.T) {
			risk, bundle, ok := projectEinoAssistantPhaseToolMetadata(&schema.ToolInfo{Name: name})
			if !ok || risk != projectAssistantToolRiskRead || bundle != projectAssistantToolBundleWorkspaceRead {
				t.Fatalf("metadata for %q = (%q, %q, %t), want read/workspace_read/true", name, risk, bundle, ok)
			}
		})
	}

	for _, name := range []string{"provider__read_file", "edit_file", "execute", "metadata_free"} {
		t.Run(name, func(t *testing.T) {
			if risk, bundle, ok := projectEinoAssistantPhaseToolMetadata(&schema.ToolInfo{Name: name}); ok {
				t.Fatalf("metadata for %q = (%q, %q, true), want unclassified", name, risk, bundle)
			}
		})
	}

	explicit := &schema.ToolInfo{
		Name: projectToolReadFile,
		Extra: map[string]any{
			"risk":   string(projectAssistantToolRiskWrite),
			"bundle": string(projectAssistantToolBundleEdit),
		},
	}
	risk, bundle, ok := projectEinoAssistantPhaseToolMetadata(explicit)
	if !ok || risk != projectAssistantToolRiskWrite || bundle != projectAssistantToolBundleEdit {
		t.Fatalf("explicit metadata = (%q, %q, %t), want authoritative write/edit/true", risk, bundle, ok)
	}
}

func TestProjectEinoAssistantPhaseCanonicalReadsAreLimitedToRepair(t *testing.T) {
	tools := []*schema.ToolInfo{
		{Name: projectToolLS},
		{Name: projectToolReadFile},
		{Name: projectToolGlob},
		{Name: projectToolGrep},
		{Name: "provider__read_file"},
		{Name: "edit_file"},
		{Name: "execute"},
		{Name: "metadata_free"},
	}
	for _, tt := range []struct {
		phase projectEinoAssistantPhase
		want  []string
	}{
		{phase: projectEinoAssistantPhaseMutate},
		{phase: projectEinoAssistantPhaseVerify},
		{phase: projectEinoAssistantPhaseRepair, want: []string{projectToolLS, projectToolReadFile, projectToolGlob, projectToolGrep}},
	} {
		t.Run(string(tt.phase), func(t *testing.T) {
			got := projectEinoAssistantPhaseToolNames(projectEinoAssistantPhaseFilterTools(
				tt.phase,
				&projectAssistantApprovedPlan{Steps: []string{"inspect", "edit"}},
				false,
				tools,
				false,
				projectAssistantTurnPolicy{},
			))
			if !projectEinoAssistantPhaseStringSlicesEqual(got, tt.want) {
				t.Fatalf("%s filesystem tools = %#v, want %#v", tt.phase, got, tt.want)
			}
		})
	}
}

func TestProjectEinoAssistantPhaseHiddenToolInvocationDoesNotReachEndpoint(t *testing.T) {
	middleware := &projectEinoAssistantPhaseFilterMiddleware{
		BaseChatModelAgentMiddleware: &adk.BaseChatModelAgentMiddleware{},
		phase:                        projectEinoAssistantPhaseMutate,
		approvedPlan:                 &projectAssistantApprovedPlan{Steps: []string{"inspect", "edit"}},
		toolInfos:                    []*schema.ToolInfo{{Name: "provider__read_file"}},
	}
	calls := 0
	wrapped, err := middleware.WrapInvokableToolCall(
		context.Background(),
		func(context.Context, string, ...einotool.Option) (string, error) {
			calls++
			return "hidden content", nil
		},
		&adk.ToolContext{Name: "provider__read_file"},
	)
	if err != nil {
		t.Fatalf("WrapInvokableToolCall returned error: %v", err)
	}
	result, err := wrapped(context.Background(), `{"file_path":"README.md"}`)
	if err != nil {
		t.Fatalf("wrapped invocation returned error: %v", err)
	}
	if calls != 0 || result != "Tool call denied: read_file is unavailable in the current assistant phase" {
		t.Fatalf("hidden invocation result = %q calls = %d, want denied without endpoint call", result, calls)
	}
}

func TestProjectEinoAssistantPhaseVisibleToolsKeepsOnlySelectedSearchableTools(t *testing.T) {
	static := projectEinoAssistantPhaseToolInfo(projectToolReadFile, projectAssistantToolRiskRead, projectAssistantToolBundleWorkspaceRead)
	selected := projectEinoAssistantPhaseSearchableToolInfo("provider__selected")
	unselected := projectEinoAssistantPhaseSearchableToolInfo("provider__unselected")
	visible := projectEinoAssistantPhaseVisibleTools(
		[]*schema.ToolInfo{static, selected, unselected},
		[]*schema.ToolInfo{static, selected},
	)
	if got := projectEinoAssistantPhaseToolNames(visible); !projectEinoAssistantPhaseStringSlicesEqual(got, []string{projectToolReadFile, "provider__selected"}) {
		t.Fatalf("visible tools = %#v, want static tools and the selected searchable tool", got)
	}
}

func projectEinoAssistantPhaseToolResult(name, content string) *schema.Message {
	return schema.ToolMessage(content, "call-"+name, schema.WithToolName(name))
}

func projectEinoAssistantPhaseToolInfo(name string, risk projectAssistantToolRisk, bundle projectAssistantToolBundle) *schema.ToolInfo {
	return &schema.ToolInfo{Extra: map[string]any{
		"bundle": string(bundle),
		"risk":   string(risk),
	}, Name: name}
}

func projectEinoAssistantPhaseBaseTool(name string, risk projectAssistantToolRisk, bundle projectAssistantToolBundle) einotool.BaseTool {
	return projectEinoAssistantTool{tool: projectAssistantToolFunc{spec: projectAssistantToolSpec{
		Name:        name,
		Risk:        risk,
		Description: string(bundle),
	}}}
}

func projectEinoAssistantPhaseSearchableToolInfo(name string) *schema.ToolInfo {
	tool := projectEinoAssistantPhaseToolInfo(name, projectAssistantToolRiskRead, projectAssistantToolBundleWorkspaceRead)
	tool.Extra[projectEinoToolSearchableExtraKey] = true
	return tool
}

func projectEinoAssistantPhaseFactoryToolInfos(t *testing.T) []*schema.ToolInfo {
	t.Helper()
	req := projectAssistantRunRequest{
		WorkspaceScope: workspace.Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "project-a"},
		TurnProfile:    projectAssistantTurnProfileImplementation,
		TurnPolicy:     projectAssistantTurnPolicyForProfile(projectAssistantTurnProfileImplementation),
	}
	return projectEinoAssistantPhaseFactoryToolInfosForRequest(t, req)
}

func projectEinoAssistantPhaseFactoryToolInfosForRequest(t *testing.T, req projectAssistantRunRequest) []*schema.ToolInfo {
	t.Helper()
	return projectEinoAssistantPhaseFactoryToolInfosForRequestAndDiscovery(t, req, projectEinoAssistantToolDiscovery{IncludeCommitBridge: true})
}

func projectEinoAssistantPhaseFactoryToolInfosForRequestAndDiscovery(
	t *testing.T,
	req projectAssistantRunRequest,
	discovery projectEinoAssistantToolDiscovery,
) []*schema.ToolInfo {
	t.Helper()
	server := NewWithWorkspace(nil, store.NewMemoryStore(), workspace.NewFileStore(t.TempDir()), "", false)
	runState := newProjectEinoAssistantRunState()
	runState.SetToolDiscovery(discovery)
	tools, err := newProjectEinoAssistantToolsFactory(server)(context.Background(), req, runState)
	if err != nil {
		t.Fatalf("new factory tools returned error: %v", err)
	}
	filesystemMiddleware, err := projectEinoAssistantFilesystemMiddleware(context.Background(), server.workspaces, req)
	if err != nil {
		t.Fatalf("filesystem middleware returned error: %v", err)
	}
	_, runCtx, err := filesystemMiddleware.BeforeAgent(context.Background(), &adk.ChatModelAgentContext{Tools: tools})
	if err != nil {
		t.Fatalf("filesystem BeforeAgent returned error: %v", err)
	}
	infos := make([]*schema.ToolInfo, 0, len(runCtx.Tools))
	for _, tool := range runCtx.Tools {
		info, err := tool.Info(context.Background())
		if err != nil {
			t.Fatalf("factory tool Info returned error: %v", err)
		}
		infos = append(infos, info)
	}
	return infos
}

func projectEinoAssistantPhaseToolNames(tools []*schema.ToolInfo) []string {
	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		if tool != nil {
			names = append(names, tool.Name)
		}
	}
	return names
}

func projectEinoAssistantPhaseToolNamesContain(names []string, want string) bool {
	for _, name := range names {
		if name == want {
			return true
		}
	}
	return false
}

func projectEinoAssistantPhaseStringSlicesEqual(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func TestProjectEinoAssistantInitialBuildRequiresDefinedPlanAndCompletedProgress(t *testing.T) {
	authority := projectAssistantInitialCreationPlan("Build a swipe app for cat profiles.")
	req := projectAssistantRunRequest{InitialApprovedPlan: &authority}
	runState := newProjectEinoAssistantRunState()
	runState.ApprovePlan(authority)
	state := &adk.ChatModelAgentState{}

	if got := projectEinoAssistantPhaseForState(req, runState, state); got != projectEinoAssistantPhasePlan {
		t.Fatalf("initial phase = %q, want plan", got)
	}

	plan := normalizeProjectAssistantApprovedPlan(projectAssistantApprovedPlan{
		Goal:               authority.Goal,
		Summary:            "Build the app",
		Steps:              []string{"Create the app", "Verify the app"},
		TargetPaths:        []string{"src/"},
		Version:            projectAssistantApprovedPlanVersionWorkspaceMutation,
		Capabilities:       []string{projectAssistantCapabilityWorkspaceMutate},
		AcceptanceCriteria: []string{"Preview is ready"},
		ApprovalTool:       projectToolDefineInitialProjectPlan,
		RunLocal:           true,
	})
	runState.ApprovePlan(plan)
	runState.SetExecutionPlan(plan, "plan-1")
	if got := projectEinoAssistantPhaseForState(req, runState, state); got != projectEinoAssistantPhaseMutate {
		t.Fatalf("defined-plan phase = %q, want mutate", got)
	}

	state.Messages = []*schema.Message{
		projectEinoAssistantPhaseToolResult(projectToolWriteFile, `{"path":"src/App.tsx"}`),
		projectEinoAssistantPhaseToolResult(projectToolVerifyDevelopmentRuntime, `{"status":"ready"}`),
	}
	if got := projectEinoAssistantPhaseForState(req, runState, state); got != projectEinoAssistantPhaseMutate {
		t.Fatalf("verified but incomplete-plan phase = %q, want mutate", got)
	}

	runState.SetPlanProgress(projectAssistantPlanSnapshot{Steps: []projectAssistantPlanStep{
		{Content: "Create the app", Status: "completed"},
		{Content: "Verify the app", Status: "completed"},
	}})
	if got := projectEinoAssistantPhaseForState(req, runState, state); got != projectEinoAssistantPhaseReport {
		t.Fatalf("completed initial-build phase = %q, want report", got)
	}
}

func TestProjectEinoAssistantPlanPhaseExposesOnlyInternalPlanTool(t *testing.T) {
	tools := []*schema.ToolInfo{
		projectEinoAssistantPhaseToolInfo(projectToolDefineInitialProjectPlan, projectAssistantToolRiskPlan, projectAssistantToolBundleCollaboration),
		projectEinoAssistantPhaseToolInfo(projectToolRequestProjectPlanApproval, projectAssistantToolRiskPlan, projectAssistantToolBundleCollaboration),
		projectEinoAssistantPhaseToolInfo(projectToolWriteFile, projectAssistantToolRiskWrite, projectAssistantToolBundleEdit),
	}
	authority := projectAssistantInitialCreationPlan("Build a cat app.")
	got := projectEinoAssistantPhaseFilterTools(
		projectEinoAssistantPhasePlan,
		&authority,
		false,
		tools,
		false,
		projectAssistantTurnPolicyForProfile(projectAssistantTurnProfileImplementation),
	)
	if names := projectEinoAssistantPhaseToolNames(got); !projectEinoAssistantPhaseStringSlicesEqual(names, []string{projectToolDefineInitialProjectPlan}) {
		t.Fatalf("plan tools = %#v, want only internal plan definition", names)
	}
}
