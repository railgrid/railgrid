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
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/cloudwego/eino/adk"
	einomodel "github.com/cloudwego/eino/components/model"
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
			name:     "workspace write remains in mutation batch",
			approved: true,
			messages: []*schema.Message{projectEinoAssistantPhaseToolResult(projectToolWriteFile, `{"operation":"write_file"}`)},
			want:     projectEinoAssistantPhaseMutate,
		},
		{
			name:     "corrected todo file mistake remains in mutation batch",
			approved: true,
			messages: []*schema.Message{
				projectEinoAssistantPhaseToolResult(projectToolWriteFile, `{"operation":"write_file"}`),
				projectEinoAssistantPhaseToolResult(projectToolWriteFile, "Tool call failed: todo tracking must use write_todos; do not create todo.md or todos.md in the project workspace"),
			},
			want: projectEinoAssistantPhaseMutate,
		},
		{
			name:     "denied workspace write permits terminal report",
			approved: true,
			messages: []*schema.Message{projectEinoAssistantPhaseToolResult(projectToolWriteFile, "Tool call failed: permission denied: denied by user")},
			want:     projectEinoAssistantPhaseReport,
		},
		{
			name:     "provisioning verification stays in runtime warmup",
			approved: true,
			messages: []*schema.Message{
				projectEinoAssistantPhaseToolResult(projectToolWriteFile, `{"operation":"write_file"}`),
				projectEinoAssistantPhaseToolResult(projectToolVerifyDevelopmentRuntime, `{"status":"provisioning"}`),
			},
			want: projectEinoAssistantPhaseWarmup,
		},
		{
			name:     "legacy reachable verification reports without editing",
			approved: true,
			messages: []*schema.Message{
				projectEinoAssistantPhaseToolResult(projectToolWriteFile, `{"operation":"write_file"}`),
				projectEinoAssistantPhaseToolResult(projectToolVerifyDevelopmentRuntime, `{"status":"reachable"}`),
			},
			want: projectEinoAssistantPhaseReport,
		},
		{
			name: "legacy available verification reports without editing during initial project creation",
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
			name:     "direct action after source write remains in mutation batch",
			approved: true,
			messages: []*schema.Message{
				projectEinoAssistantPhaseToolResult(projectToolWriteFile, `{"operation":"write_file"}`),
				projectEinoAssistantPhaseToolResult(projectToolRestartRuntime, `{"status":"restarted"}`),
			},
			want: projectEinoAssistantPhaseMutate,
		},
		{
			name:     "later write invalidates earlier verification",
			approved: true,
			messages: []*schema.Message{
				projectEinoAssistantPhaseToolResult(projectToolWriteFile, `{"operation":"write_file"}`),
				projectEinoAssistantPhaseToolResult(projectToolVerifyDevelopmentRuntime, `{"status":"ready"}`),
				projectEinoAssistantPhaseToolResult(projectToolApplyPatch, `{"operation":"apply_patch"}`),
			},
			want: projectEinoAssistantPhaseMutate,
		},
		{
			name:     "later failed verification invalidates earlier reachable verification",
			approved: true,
			messages: []*schema.Message{
				projectEinoAssistantPhaseToolResult(projectToolWriteFile, `{"operation":"write_file"}`),
				projectEinoAssistantPhaseToolResult(projectToolVerifyDevelopmentRuntime, `{"status":"ready"}`),
				projectEinoAssistantPhaseToolResult(projectToolVerifyDevelopmentRuntime, "Tool call failed: runtime unavailable"),
			},
			want: projectEinoAssistantPhaseReport,
		},
		{
			name:     "missing workspace context reports without runtime polling or editing",
			approved: true,
			messages: []*schema.Message{
				projectEinoAssistantPhaseToolResult(projectToolWriteFile, `{"operation":"write_file"}`),
				projectEinoAssistantPhaseToolResult(projectToolVerifyDevelopmentRuntime, `{"status":"not_ready","readiness":{"status":"needs_workspace_context"},"runtime":{"status":"ready","previewURL":"https://preview.example"},"previewURL":"https://preview.example","blockers":["workspace context is required"]}`),
			},
			want: projectEinoAssistantPhaseReport,
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

func TestProjectEinoAssistantCompletionBarrierRewritesDirtyCompletion(t *testing.T) {
	state := newProjectEinoAssistantRunState()
	state.RecordSourceMutation()
	state.RecordAssistantReply(projectAssistantReply{Content: "The change is complete."})
	model := &projectEinoAssistantCompletionBarrierModel{
		BaseChatModel: &projectEinoAssistantCompletionBarrierTestModel{
			message: schema.AssistantMessage("The change is complete.", nil),
		},
		verificationToolName: projectToolVerifyDevelopmentRuntime,
		runState:             state,
	}

	message, err := model.Generate(context.Background(), nil)
	if err != nil {
		t.Fatalf("Generate returned error: %v", err)
	}
	if strings.TrimSpace(message.Content) != "" || len(message.ToolCalls) != 1 {
		t.Fatalf("message = %#v, want one synthetic verifier call", message)
	}
	call := message.ToolCalls[0]
	if call.ID == "" ||
		call.Function.Name != projectToolVerifyDevelopmentRuntime ||
		call.Function.Arguments != `{}` {
		t.Fatalf("tool call = %#v, want unique verify_development_runtime call", call)
	}
	checkpoint := state.CheckpointState()
	if len(checkpoint.Messages) != 1 ||
		checkpoint.Messages[0].Content != "" ||
		len(checkpoint.Messages[0].ToolCalls) != 1 ||
		checkpoint.Messages[0].ToolCalls[0].Function.Name != projectToolVerifyDevelopmentRuntime {
		t.Fatalf("checkpoint messages = %#v, want only synthetic verifier call", checkpoint.Messages)
	}
}

func TestProjectEinoAssistantCompletionBarrierBuffersDirtyStream(t *testing.T) {
	state := newProjectEinoAssistantRunState()
	state.RecordSourceMutation()
	model := &projectEinoAssistantCompletionBarrierModel{
		BaseChatModel: &projectEinoAssistantCompletionBarrierTestModel{
			stream: []*schema.Message{
				schema.AssistantMessage("The change ", nil),
				schema.AssistantMessage("is complete.", nil),
			},
		},
		verificationToolName: projectToolVerifyDevelopmentRuntime,
		runState:             state,
	}

	reader, err := model.Stream(context.Background(), nil)
	if err != nil {
		t.Fatalf("Stream returned error: %v", err)
	}
	message, err := schema.ConcatMessageStream(reader)
	if err != nil {
		t.Fatalf("combine barrier stream: %v", err)
	}
	if strings.TrimSpace(message.Content) != "" ||
		len(message.ToolCalls) != 1 ||
		message.ToolCalls[0].Function.Name != projectToolVerifyDevelopmentRuntime {
		t.Fatalf("message = %#v, want buffered synthetic verifier call", message)
	}
}

func TestProjectEinoAssistantCompletionBarrierPassesToolCallsAndEmptyResponses(t *testing.T) {
	state := newProjectEinoAssistantRunState()
	state.RecordSourceMutation()
	toolMessage := schema.AssistantMessage("I will make another edit.", []schema.ToolCall{{
		ID:   "call-write",
		Type: "function",
		Function: schema.FunctionCall{
			Name:      projectToolWriteFile,
			Arguments: `{"path":"src/App.tsx","content":"updated"}`,
		},
	}})
	if got := projectEinoAssistantCompletionBarrierMessage(
		toolMessage,
		projectToolVerifyDevelopmentRuntime,
		state,
	); got != toolMessage {
		t.Fatalf("dirty tool response was rewritten: %#v", got)
	}
	emptyMessage := schema.AssistantMessage("", nil)
	if got := projectEinoAssistantCompletionBarrierMessage(
		emptyMessage,
		projectToolVerifyDevelopmentRuntime,
		state,
	); got != emptyMessage {
		t.Fatalf("empty response was rewritten: %#v", got)
	}
}

func TestProjectEinoAssistantCompletionBarrierLeavesVerifiedAndReadOnlyModelsUnwrapped(t *testing.T) {
	base := &projectEinoAssistantCompletionBarrierTestModel{
		message: schema.AssistantMessage("stream normally", nil),
	}
	verifiedState := newProjectEinoAssistantRunState()
	verifiedState.RecordSourceMutation()
	verifiedState.RecordDevelopmentVerificationResult(`{"checkedMutationRevision":1,"status":"ready"}`)
	verifiedMiddleware := &projectEinoAssistantPhaseFilterMiddleware{
		BaseChatModelAgentMiddleware: &adk.BaseChatModelAgentMiddleware{},
		runState:                     verifiedState,
		phase:                        projectEinoAssistantPhaseMutate,
	}
	wrapped, err := verifiedMiddleware.WrapModel(context.Background(), base, &adk.ModelContext{
		Tools: []*schema.ToolInfo{{Name: projectToolVerifyDevelopmentRuntime}},
	})
	if err != nil {
		t.Fatalf("WrapModel for verified state returned error: %v", err)
	}
	if wrapped != base {
		t.Fatalf("verified model = %T, want original model", wrapped)
	}

	dirtyState := newProjectEinoAssistantRunState()
	dirtyState.RecordSourceMutation()
	readOnlyMiddleware := &projectEinoAssistantPhaseFilterMiddleware{
		BaseChatModelAgentMiddleware: &adk.BaseChatModelAgentMiddleware{},
		runState:                     dirtyState,
		phase:                        projectEinoAssistantPhaseReport,
	}
	wrapped, err = readOnlyMiddleware.WrapModel(context.Background(), base, &adk.ModelContext{
		Tools: []*schema.ToolInfo{{Name: projectToolVerifyDevelopmentRuntime}},
	})
	if err != nil {
		t.Fatalf("WrapModel for read-only phase returned error: %v", err)
	}
	if wrapped != base {
		t.Fatalf("read-only model = %T, want original model", wrapped)
	}
}

type projectEinoAssistantCompletionBarrierTestModel struct {
	message *schema.Message
	stream  []*schema.Message
}

func (m *projectEinoAssistantCompletionBarrierTestModel) Generate(
	context.Context,
	[]*schema.Message,
	...einomodel.Option,
) (*schema.Message, error) {
	return m.message, nil
}

func (m *projectEinoAssistantCompletionBarrierTestModel) Stream(
	context.Context,
	[]*schema.Message,
	...einomodel.Option,
) (*schema.StreamReader[*schema.Message], error) {
	if m.stream != nil {
		return schema.StreamReaderFromArray(m.stream), nil
	}
	return schema.StreamReaderFromArray([]*schema.Message{m.message}), nil
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
		projectEinoAssistantPhaseToolResult(projectToolVerifyDevelopmentRuntime, `{"status":"ready"}`),
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
			want: projectEinoAssistantPhaseMutate,
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
		dirty        bool
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
			name:         "mutate before first write exposes only edits follow-up and todos",
			approvedPlan: &projectAssistantApprovedPlan{Steps: []string{"inspect", "edit"}},
			want: []string{
				projectToolAskFollowUp, projectToolWriteFile, projectToolApplyPatch,
				projectEinoAssistantWriteTodosTool,
			},
		},
		{
			name:         "dirty mutation batch keeps edits runtime actions and verification available",
			approvedPlan: &projectAssistantApprovedPlan{Steps: []string{"inspect", "edit"}},
			dirty:        true,
			messages: []*schema.Message{
				projectEinoAssistantPhaseToolResult(projectToolWriteFile, `{"operation":"write_file"}`),
			},
			want: []string{
				projectToolAskFollowUp, projectToolWriteFile, projectToolApplyPatch,
				projectToolGetRuntimeStatus, projectToolRestartRuntime, projectToolSetRuntimeEnv,
				projectToolVerifyDevelopmentRuntime, projectEinoAssistantWriteTodosTool,
			},
		},
		{
			name:         "repair exposes targeted reads edits runtime tools follow-up and todos",
			approvedPlan: &projectAssistantApprovedPlan{Steps: []string{"inspect", "repair"}},
			messages: []*schema.Message{
				projectEinoAssistantPhaseToolResult(projectToolWriteFile, `{"operation":"write_file"}`),
				projectEinoAssistantPhaseToolResult(projectToolVerifyDevelopmentRuntime, `{"status":"not_ready","logs":{"status":"failed","blockers":["SyntaxError"]}}`),
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
			if tt.dirty {
				runState.RecordSourceMutation()
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

func TestProjectEinoAssistantPhaseHidesWriteFileWhenEveryApprovedTargetIsKnownExisting(t *testing.T) {
	tools := []*schema.ToolInfo{
		projectEinoAssistantPhaseToolInfo(projectToolWriteFile, projectAssistantToolRiskWrite, projectAssistantToolBundleEdit),
		projectEinoAssistantPhaseToolInfo(projectToolApplyPatch, projectAssistantToolRiskWrite, projectAssistantToolBundleEdit),
	}
	existingPlan := projectAssistantApprovedPlan{
		Steps:       []string{"update the existing app"},
		TargetPaths: []string{"src/App.jsx", "index.html"},
	}
	runState := newProjectEinoAssistantRunState()
	runState.ApprovePlan(existingPlan)
	runState.RecordObservedReadFile("src/App.jsx")
	runState.RecordObservedReadFile("index.html")
	state := &adk.ChatModelAgentState{
		ToolInfos:         append([]*schema.ToolInfo(nil), tools...),
		DeferredToolInfos: append([]*schema.ToolInfo(nil), tools...),
	}

	middleware := &projectEinoAssistantPhaseFilterMiddleware{
		BaseChatModelAgentMiddleware: &adk.BaseChatModelAgentMiddleware{},
		runState:                     runState,
	}
	_, state, err := middleware.BeforeModelRewriteState(context.Background(), state, nil)
	if err != nil {
		t.Fatalf("BeforeModelRewriteState returned error: %v", err)
	}
	for inventory, infos := range map[string][]*schema.ToolInfo{
		"tools":    state.ToolInfos,
		"deferred": state.DeferredToolInfos,
	} {
		names := projectEinoAssistantPhaseToolNames(infos)
		if projectEinoAssistantPhaseToolNamesContain(names, projectToolWriteFile) {
			t.Fatalf("%s = %#v, existing-only plan must hide write_file", inventory, names)
		}
		if !projectEinoAssistantPhaseToolNamesContain(names, projectToolApplyPatch) {
			t.Fatalf("%s = %#v, existing-only plan must keep apply_patch", inventory, names)
		}
	}

	for _, tt := range []struct {
		name string
		req  projectAssistantRunRequest
		plan projectAssistantApprovedPlan
	}{
		{
			name: "mixed existing and new targets",
			plan: projectAssistantApprovedPlan{
				Steps:       []string{"update and create"},
				TargetPaths: []string{"src/App.jsx", "src/theme.js"},
			},
		},
		{
			name: "directory target",
			plan: projectAssistantApprovedPlan{
				Steps:       []string{"update source"},
				TargetPaths: []string{"src/"},
			},
		},
		{
			name: "initial build",
			req: projectAssistantRunRequest{
				InitialApprovedPlan: &existingPlan,
			},
			plan: existingPlan,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			turnState := newProjectEinoAssistantRunState()
			turnState.ApprovePlan(tt.plan)
			turnState.RecordObservedReadFile("src/App.jsx")
			turnState.RecordObservedReadFile("index.html")
			modelState := &adk.ChatModelAgentState{
				ToolInfos:         append([]*schema.ToolInfo(nil), tools...),
				DeferredToolInfos: append([]*schema.ToolInfo(nil), tools...),
			}
			phaseMiddleware := projectEinoAssistantPhaseMiddleware(tt.req, turnState)
			_, modelState, err := phaseMiddleware.BeforeModelRewriteState(context.Background(), modelState, nil)
			if err != nil {
				t.Fatalf("BeforeModelRewriteState returned error: %v", err)
			}
			if names := projectEinoAssistantPhaseToolNames(modelState.ToolInfos); !projectEinoAssistantPhaseToolNamesContain(names, projectToolWriteFile) {
				t.Fatalf("tools = %#v, want write_file for %s", names, tt.name)
			}
		})
	}
}

func TestProjectEinoAssistantPhaseAllowsOneFreshApprovedTargetReadAfterApproval(t *testing.T) {
	const readArguments = `{"path":"src/App.jsx","offset":1,"limit":2000}`
	plan := projectAssistantApprovedPlan{
		Steps:        []string{"update existing app"},
		TargetPaths:  []string{"src/App.jsx"},
		Version:      projectAssistantApprovedPlanVersionWorkspaceMutation,
		Capabilities: []string{projectAssistantCapabilityWorkspaceMutate},
	}
	runState := newProjectEinoAssistantRunState()
	runState.RecordObservedReadFile("src/App.jsx")
	runState.RecordReadFileRange("src/App.jsx", 1, projectEinoAssistantReadThroughEOF)
	runState.RecordCompletedRead(projectToolReadFile, readArguments)
	runState.ApprovePlan(plan)
	if runState.ReadFileRangeCovered("src/App.jsx", 1, 10) ||
		runState.RepeatedCompletedRead(projectToolReadFile, readArguments) {
		t.Fatal("plan approval did not reopen one exact-target mutation read")
	}
	if !stringSliceContains(runState.ObservedReadFilePaths(), "src/App.jsx") {
		t.Fatal("plan approval lost known-existing target evidence")
	}

	state := &adk.ChatModelAgentState{ToolInfos: []*schema.ToolInfo{
		projectEinoAssistantPhaseToolInfo(projectToolReadFile, projectAssistantToolRiskRead, projectAssistantToolBundleWorkspaceRead),
		projectEinoAssistantPhaseToolInfo(projectToolLS, projectAssistantToolRiskRead, projectAssistantToolBundleWorkspaceRead),
		projectEinoAssistantPhaseToolInfo(projectToolWriteFile, projectAssistantToolRiskWrite, projectAssistantToolBundleEdit),
		projectEinoAssistantPhaseToolInfo(projectToolApplyPatch, projectAssistantToolRiskWrite, projectAssistantToolBundleEdit),
	}}
	middleware := &projectEinoAssistantPhaseFilterMiddleware{
		BaseChatModelAgentMiddleware: &adk.BaseChatModelAgentMiddleware{},
		runState:                     runState,
	}
	_, state, err := middleware.BeforeModelRewriteState(context.Background(), state, nil)
	if err != nil {
		t.Fatalf("BeforeModelRewriteState returned error: %v", err)
	}
	names := projectEinoAssistantPhaseToolNames(state.ToolInfos)
	for _, expected := range []string{projectToolReadFile, projectToolApplyPatch} {
		if !projectEinoAssistantPhaseToolNamesContain(names, expected) {
			t.Fatalf("tools = %#v, want %s after approval", names, expected)
		}
	}
	for _, forbidden := range []string{projectToolLS, projectToolWriteFile} {
		if projectEinoAssistantPhaseToolNamesContain(names, forbidden) {
			t.Fatalf("tools = %#v, must hide %s after approval", names, forbidden)
		}
	}
	instruction := state.Messages[len(state.Messages)-1].Content
	for _, expected := range []string{"Use read_file now", "Do not ask the user to paste file contents"} {
		if !strings.Contains(instruction, expected) {
			t.Fatalf("mutation instruction = %q, want %q", instruction, expected)
		}
	}
	if strings.Contains(instruction, "further workspace reads are unavailable") {
		t.Fatalf("mutation instruction = %q, must not contradict exposed target reads", instruction)
	}

	runState.RecordReadFileRange("src/App.jsx", 1, projectEinoAssistantReadThroughEOF)
	state = &adk.ChatModelAgentState{ToolInfos: []*schema.ToolInfo{
		projectEinoAssistantPhaseToolInfo(projectToolReadFile, projectAssistantToolRiskRead, projectAssistantToolBundleWorkspaceRead),
		projectEinoAssistantPhaseToolInfo(projectToolApplyPatch, projectAssistantToolRiskWrite, projectAssistantToolBundleEdit),
	}}
	_, state, err = middleware.BeforeModelRewriteState(context.Background(), state, nil)
	if err != nil {
		t.Fatalf("second BeforeModelRewriteState returned error: %v", err)
	}
	names = projectEinoAssistantPhaseToolNames(state.ToolInfos)
	if projectEinoAssistantPhaseToolNamesContain(names, projectToolReadFile) {
		t.Fatalf("tools = %#v, must hide the completed one-shot target read", names)
	}
	if !projectEinoAssistantPhaseToolNamesContain(names, projectToolApplyPatch) {
		t.Fatalf("tools = %#v, want apply_patch after target read", names)
	}
	if len(names) != 1 {
		t.Fatalf("tools = %#v, want only apply_patch for the first existing-file mutation", names)
	}
	instruction = state.Messages[len(state.Messages)-1].Content
	for _, expected := range []string{"Apply the approved workspace mutation now", "Do not reread files"} {
		if !strings.Contains(instruction, expected) {
			t.Fatalf("post-read mutation instruction = %q, want %q", instruction, expected)
		}
	}

	askCalls := 0
	wrappedAsk, err := middleware.WrapInvokableToolCall(
		context.Background(),
		func(context.Context, string, ...einotool.Option) (string, error) {
			askCalls++
			return "unexpected", nil
		},
		&adk.ToolContext{Name: projectToolAskFollowUp},
	)
	if err != nil {
		t.Fatalf("WrapInvokableToolCall returned error: %v", err)
	}
	result, err := wrappedAsk(context.Background(), `{"questions":["paste the file"]}`)
	if err != nil || askCalls != 0 || !strings.Contains(result, "apply_patch now") {
		t.Fatalf("forced-patch ask result = %q calls = %d error = %v", result, askCalls, err)
	}
}

func TestProjectEinoAssistantPhaseNoProgressWarningUsesLatestEditFailure(t *testing.T) {
	runState := newProjectEinoAssistantRunState()
	runState.ApprovePlan(projectAssistantApprovedPlan{
		Steps:       []string{"update app"},
		TargetPaths: []string{"src/App.jsx"},
	})
	const arguments = `{"path":"src/App.jsx","oldText":"missing","newText":"replacement"}`
	for range 2 {
		runState.NextModelCallOrdinal()
		runState.RecordCompletedAction(projectToolApplyPatch, arguments, false)
	}
	state := &adk.ChatModelAgentState{Messages: []*schema.Message{
		projectEinoAssistantPhaseToolResult(
			projectToolApplyPatch,
			`Tool call failed: oldText was not found in "src/App.jsx"`,
		),
	}}
	middleware := &projectEinoAssistantPhaseFilterMiddleware{
		BaseChatModelAgentMiddleware: &adk.BaseChatModelAgentMiddleware{},
		runState:                     runState,
	}

	warned, err := middleware.enforceRepeatedActionProgress(state, projectEinoAssistantPhaseMutate)
	if err != nil {
		t.Fatalf("enforceRepeatedActionProgress returned error: %v", err)
	}
	if !warned || len(state.Messages) != 2 {
		t.Fatalf("warning = %t messages = %#v, want one recovery instruction", warned, state.Messages)
	}
	instruction := state.Messages[1].Content
	for _, expected := range []string{"oldText was not found", "Do not switch to write_file", "Reread", "retry apply_patch"} {
		if !strings.Contains(instruction, expected) {
			t.Fatalf("instruction = %q, want %q", instruction, expected)
		}
	}
	if strings.Contains(instruction, "write_file, apply_patch, or mkdir") {
		t.Fatalf("instruction = %q, must not repeat generic mutation guidance", instruction)
	}

	for call := 3; call <= 5; call++ {
		runState.NextModelCallOrdinal()
		runState.RecordCompletedAction(projectToolApplyPatch, arguments, false)
	}
	if warned, err := middleware.enforceRepeatedActionProgress(state, projectEinoAssistantPhaseMutate); err != nil || !warned {
		t.Fatalf("continued warning after repeated failures = %t, error = %v", warned, err)
	}
}

func TestProjectEinoAssistantPhaseExposesOnlyFailedPatchTargetForRecoveryRead(t *testing.T) {
	const callID = "call-patch-app"
	plan := projectAssistantApprovedPlan{
		Steps:        []string{"update app"},
		TargetPaths:  []string{"src/App.jsx"},
		Version:      projectAssistantApprovedPlanVersionWorkspaceMutation,
		Capabilities: []string{projectAssistantCapabilityWorkspaceMutate},
	}
	runState := newProjectEinoAssistantRunState()
	runState.ApprovePlan(plan)
	runState.RecordObservedReadFile("src/App.jsx")
	state := &adk.ChatModelAgentState{
		Messages: []*schema.Message{
			schema.AssistantMessage("", []schema.ToolCall{{
				ID:   callID,
				Type: "function",
				Function: schema.FunctionCall{
					Name:      projectToolApplyPatch,
					Arguments: `{"path":"src/App.jsx","oldText":"missing","newText":"replacement"}`,
				},
			}}),
			schema.ToolMessage(
				`Tool call failed: oldText was not found in "src/App.jsx"`,
				callID,
				schema.WithToolName(projectToolApplyPatch),
			),
		},
		ToolInfos: []*schema.ToolInfo{
			projectEinoAssistantPhaseToolInfo(projectToolReadFile, projectAssistantToolRiskRead, projectAssistantToolBundleWorkspaceRead),
			projectEinoAssistantPhaseToolInfo(projectToolLS, projectAssistantToolRiskRead, projectAssistantToolBundleWorkspaceRead),
			projectEinoAssistantPhaseToolInfo(projectToolWriteFile, projectAssistantToolRiskWrite, projectAssistantToolBundleEdit),
			projectEinoAssistantPhaseToolInfo(projectToolApplyPatch, projectAssistantToolRiskWrite, projectAssistantToolBundleEdit),
		},
	}
	middleware := &projectEinoAssistantPhaseFilterMiddleware{
		BaseChatModelAgentMiddleware: &adk.BaseChatModelAgentMiddleware{},
		runState:                     runState,
	}

	_, state, err := middleware.BeforeModelRewriteState(context.Background(), state, nil)
	if err != nil {
		t.Fatalf("BeforeModelRewriteState returned error: %v", err)
	}
	names := projectEinoAssistantPhaseToolNames(state.ToolInfos)
	for _, expected := range []string{projectToolReadFile} {
		if !projectEinoAssistantPhaseToolNamesContain(names, expected) {
			t.Fatalf("tools = %#v, want %s for targeted patch recovery", names, expected)
		}
	}
	for _, forbidden := range []string{projectToolLS, projectToolWriteFile, projectToolApplyPatch} {
		if projectEinoAssistantPhaseToolNamesContain(names, forbidden) {
			t.Fatalf("tools = %#v, must hide %s during targeted patch recovery", names, forbidden)
		}
	}

	calls := 0
	wrapped, err := middleware.WrapInvokableToolCall(
		context.Background(),
		func(context.Context, string, ...einotool.Option) (string, error) {
			calls++
			return `{"path":"src/App.jsx"}`, nil
		},
		&adk.ToolContext{Name: projectToolReadFile},
	)
	if err != nil {
		t.Fatalf("WrapInvokableToolCall returned error: %v", err)
	}
	result, err := wrapped(context.Background(), `{"file_path":"src/App.jsx","offset":1,"limit":120}`)
	if err != nil || calls != 1 || !strings.Contains(result, "src/App.jsx") {
		t.Fatalf("approved recovery read result = %q calls = %d error = %v", result, calls, err)
	}
	result, err = wrapped(context.Background(), `{"file_path":"src/Other.jsx","offset":1,"limit":120}`)
	if err != nil || calls != 1 || !strings.Contains(result, "reread") {
		t.Fatalf("other-path recovery result = %q calls = %d error = %v, want denial", result, calls, err)
	}

	patchCalls := 0
	wrappedPatch, err := middleware.WrapInvokableToolCall(
		context.Background(),
		func(context.Context, string, ...einotool.Option) (string, error) {
			patchCalls++
			return `{"operation":"apply_patch"}`, nil
		},
		&adk.ToolContext{Name: projectToolApplyPatch},
	)
	if err != nil {
		t.Fatalf("WrapInvokableToolCall returned error: %v", err)
	}
	result, err = wrappedPatch(
		context.Background(),
		`{"path":"src/App.jsx","oldText":"missing","newText":"replacement"}`,
	)
	if err != nil || patchCalls != 0 || !strings.Contains(result, `reread "src/App.jsx"`) {
		t.Fatalf("patch recovery result = %q calls = %d error = %v, want reread denial", result, patchCalls, err)
	}
}

func TestProjectEinoAssistantRunStateReopensOnlyFailedPatchRead(t *testing.T) {
	runState := newProjectEinoAssistantRunState()
	runState.RecordReadFileRange("src/App.jsx", 1, projectEinoAssistantReadThroughEOF)
	runState.RecordReadFileRange("src/index.css", 1, projectEinoAssistantReadThroughEOF)
	runState.RecordCompletedRead(projectToolReadFile, `{"path":"src/App.jsx"}`)

	runState.ReopenReadFile("src/App.jsx")

	if runState.ReadFileRangeCovered("src/App.jsx", 1, 10) {
		t.Fatal("failed patch target remained covered")
	}
	if !runState.ReadFileRangeCovered("src/index.css", 1, 10) {
		t.Fatal("unrelated read coverage was cleared")
	}
	if runState.RepeatedCompletedRead(projectToolReadFile, `{"path":"src/App.jsx"}`) {
		t.Fatal("failed patch target read remained duplicate-suppressed")
	}
}

func TestProjectEinoAssistantPatchRecoveryPersistsAndRequiresExactRetry(t *testing.T) {
	runState := newProjectEinoAssistantRunState()
	runState.ApprovePlan(projectAssistantApprovedPlan{
		Steps:        []string{"update app"},
		TargetPaths:  []string{"src/App.jsx"},
		Version:      projectAssistantApprovedPlanVersionWorkspaceMutation,
		Capabilities: []string{projectAssistantCapabilityWorkspaceMutate},
	})
	runState.StartPatchRecovery("src/App.jsx")
	runState.RecordPatchRecoveryRead("src/App.jsx")

	restored := newProjectEinoAssistantRunState()
	restored.RestoreCheckpointState(runState.CheckpointState())
	path, readComplete := restored.PatchRecovery()
	if path != "src/App.jsx" || !readComplete {
		t.Fatalf("restored recovery = (%q, %t), want exact completed reread", path, readComplete)
	}

	state := &adk.ChatModelAgentState{ToolInfos: []*schema.ToolInfo{
		projectEinoAssistantPhaseToolInfo(projectToolReadFile, projectAssistantToolRiskRead, projectAssistantToolBundleWorkspaceRead),
		projectEinoAssistantPhaseToolInfo(projectToolApplyPatch, projectAssistantToolRiskWrite, projectAssistantToolBundleEdit),
		projectEinoAssistantPhaseToolInfo(projectToolVerifyDevelopmentRuntime, projectAssistantToolRiskRead, projectAssistantToolBundleRuntime),
	}}
	middleware := &projectEinoAssistantPhaseFilterMiddleware{
		BaseChatModelAgentMiddleware: &adk.BaseChatModelAgentMiddleware{},
		runState:                     restored,
	}
	_, state, err := middleware.BeforeModelRewriteState(context.Background(), state, nil)
	if err != nil {
		t.Fatalf("BeforeModelRewriteState returned error: %v", err)
	}
	if names := projectEinoAssistantPhaseToolNames(state.ToolInfos); !slices.Equal(names, []string{projectToolApplyPatch}) {
		t.Fatalf("recovery tools = %#v, want only apply_patch", names)
	}

	calls := 0
	wrapped, err := middleware.WrapInvokableToolCall(
		context.Background(),
		func(context.Context, string, ...einotool.Option) (string, error) {
			calls++
			return `{"operation":"apply_patch"}`, nil
		},
		&adk.ToolContext{Name: projectToolApplyPatch},
	)
	if err != nil {
		t.Fatalf("WrapInvokableToolCall returned error: %v", err)
	}
	if result, err := wrapped(context.Background(), `{"path":"src/Other.jsx"}`); err != nil || calls != 0 || !strings.Contains(result, "src/App.jsx") {
		t.Fatalf("other-path retry = %q calls = %d error = %v, want denial", result, calls, err)
	}
	if result, err := wrapped(context.Background(), `{"path":"src/App.jsx"}`); err != nil || calls != 1 || !strings.Contains(result, "apply_patch") {
		t.Fatalf("exact retry = %q calls = %d error = %v", result, calls, err)
	}
	if path, complete := restored.PatchRecovery(); path != "" || complete {
		t.Fatalf("successful retry left recovery = (%q, %t)", path, complete)
	}
}

func TestProjectEinoAssistantRuntimeWarmupHidesSourceToolsWithoutPreemptingGlobalCeiling(t *testing.T) {
	runState := newProjectEinoAssistantRunState()
	runState.ApprovePlan(projectAssistantApprovedPlan{Steps: []string{"update app"}})
	runState.RecordSourceMutation()
	runState.RecordDevelopmentVerificationResult(`{"status":"provisioning","blockers":["development runtime is still provisioning"]}`)

	state := &adk.ChatModelAgentState{
		Messages: []*schema.Message{
			projectEinoAssistantPhaseToolResult(projectToolWriteFile, `{"operation":"write_file"}`),
			projectEinoAssistantPhaseToolResult(projectToolVerifyDevelopmentRuntime, `{"status":"provisioning"}`),
		},
		ToolInfos: []*schema.ToolInfo{
			projectEinoAssistantPhaseToolInfo(projectToolReadFile, projectAssistantToolRiskRead, projectAssistantToolBundleWorkspaceRead),
			projectEinoAssistantPhaseToolInfo(projectToolWriteFile, projectAssistantToolRiskWrite, projectAssistantToolBundleEdit),
			projectEinoAssistantPhaseToolInfo(projectToolApplyPatch, projectAssistantToolRiskWrite, projectAssistantToolBundleEdit),
			projectEinoAssistantPhaseToolInfo(projectToolGetRuntimeStatus, projectAssistantToolRiskRead, projectAssistantToolBundleRuntime),
			projectEinoAssistantPhaseToolInfo(projectToolGetPreviewURL, projectAssistantToolRiskRead, projectAssistantToolBundleRuntime),
			projectEinoAssistantPhaseToolInfo(projectToolGetRuntimeLogs, projectAssistantToolRiskRead, projectAssistantToolBundleRuntime),
			projectEinoAssistantPhaseToolInfo(projectToolVerifyDevelopmentRuntime, projectAssistantToolRiskRead, projectAssistantToolBundleRuntime),
		},
	}
	middleware := &projectEinoAssistantPhaseFilterMiddleware{
		BaseChatModelAgentMiddleware: &adk.BaseChatModelAgentMiddleware{},
		runState:                     runState,
	}
	_, state, err := middleware.BeforeModelRewriteState(context.Background(), state, nil)
	if err != nil {
		t.Fatalf("BeforeModelRewriteState returned error: %v", err)
	}
	want := []string{
		projectToolGetRuntimeStatus,
		projectToolGetPreviewURL,
		projectToolGetRuntimeLogs,
		projectToolVerifyDevelopmentRuntime,
	}
	if names := projectEinoAssistantPhaseToolNames(state.ToolInfos); !slices.Equal(names, want) {
		t.Fatalf("warmup tools = %#v, want %#v", names, want)
	}

	for range 5 {
		if _, _, err := middleware.BeforeModelRewriteState(context.Background(), state, nil); err != nil {
			t.Fatalf("warmup call returned early error: %v", err)
		}
	}
	if attempts := runState.RuntimeWarmupAttempts(); attempts != 6 {
		t.Fatalf("runtime warmup attempts = %d, want 6", attempts)
	}
}

func TestProjectEinoAssistantTracksApprovedMutationCoverageAcrossCheckpoint(t *testing.T) {
	plan := &projectAssistantApprovedPlan{TargetPaths: []string{"index.html", "src/App.jsx"}}
	observed := []string{"index.html", "src/App.jsx"}
	runState := newProjectEinoAssistantRunState()
	runState.RecordSuccessfulMutationPath("index.html")
	runState.RecordPatchResult(false)
	runState.RecordPatchResult(false)

	missing := projectEinoAssistantMissingKnownExistingMutationTargets(
		plan,
		observed,
		runState.SuccessfulMutationPaths(),
	)
	if !slices.Equal(missing, []string{"src/App.jsx"}) {
		t.Fatalf("missing mutation targets = %#v, want src/App.jsx", missing)
	}

	restored := newProjectEinoAssistantRunState()
	restored.RestoreCheckpointState(runState.CheckpointState())
	if !slices.Equal(restored.SuccessfulMutationPaths(), []string{"index.html"}) {
		t.Fatalf("restored mutation paths = %#v, want index.html", restored.SuccessfulMutationPaths())
	}
	if restored.PatchFailureCount() != 2 {
		t.Fatalf("restored patch failures = %d, want 2", restored.PatchFailureCount())
	}
	restored.ApprovePlan(projectAssistantApprovedPlan{TargetPaths: []string{"index.html"}})
	if len(restored.SuccessfulMutationPaths()) != 0 {
		t.Fatalf("new plan inherited prior mutation paths: %#v", restored.SuccessfulMutationPaths())
	}
	if restored.PatchFailureCount() != 0 {
		t.Fatalf("new plan inherited patch failures: %d", restored.PatchFailureCount())
	}
	missing = projectEinoAssistantMissingKnownExistingMutationTargets(
		&projectAssistantApprovedPlan{TargetPaths: []string{"index.html"}},
		[]string{"index.html"},
		restored.SuccessfulMutationPaths(),
	)
	if !slices.Equal(missing, []string{"index.html"}) {
		t.Fatalf("new-plan missing targets = %#v, want index.html", missing)
	}
}

func TestProjectEinoAssistantPatchConflictsDoNotPreemptGlobalCeiling(t *testing.T) {
	runState := newProjectEinoAssistantRunState()
	for range 5 {
		runState.RecordPatchResult(false)
	}
	middleware := &projectEinoAssistantPhaseFilterMiddleware{
		BaseChatModelAgentMiddleware: &adk.BaseChatModelAgentMiddleware{},
		runState:                     runState,
	}
	_, _, err := middleware.BeforeModelRewriteState(
		context.Background(),
		&adk.ChatModelAgentState{},
		nil,
	)
	if err != nil {
		t.Fatalf("patch conflicts returned early error: %v", err)
	}
}

func TestProjectEinoAssistantCommitPhaseDirectsCommitAndCompletesExecutionPlan(t *testing.T) {
	plan := projectAssistantApprovedPlan{
		Steps:        []string{"Implement dark mode", "Verify runtime", "Commit changes"},
		TargetPaths:  []string{"src/App.jsx"},
		Version:      projectAssistantApprovedPlanVersionWorkspaceMutation,
		Capabilities: []string{projectAssistantCapabilityWorkspaceMutate},
	}
	runState := newProjectEinoAssistantRunState()
	runState.ApprovePlan(plan)
	runState.SetExecutionPlan(plan, "plan-1")
	state := &adk.ChatModelAgentState{
		Messages: []*schema.Message{
			projectEinoAssistantPhaseToolResult(projectToolApplyPatch, `{"operation":"apply_patch"}`),
			projectEinoAssistantPhaseToolResult(projectToolVerifyDevelopmentRuntime, `{"status":"ready"}`),
		},
		ToolInfos: []*schema.ToolInfo{
			projectEinoAssistantPhaseToolInfo(projectToolCommitProjectFiles, projectAssistantToolRiskCommit, projectAssistantToolBundleRepo),
		},
	}
	middleware := &projectEinoAssistantPhaseFilterMiddleware{
		BaseChatModelAgentMiddleware: &adk.BaseChatModelAgentMiddleware{},
		runState:                     runState,
	}

	_, state, err := middleware.BeforeModelRewriteState(context.Background(), state, nil)
	if err != nil {
		t.Fatalf("BeforeModelRewriteState returned error: %v", err)
	}
	if middleware.phase != projectEinoAssistantPhaseCommit {
		t.Fatalf("phase = %q, want commit", middleware.phase)
	}
	instruction := state.Messages[len(state.Messages)-1].Content
	for _, expected := range []string{"Call commit_project_files now", "Do not request reads", "edits"} {
		if !strings.Contains(instruction, expected) {
			t.Fatalf("commit instruction = %q, want %q", instruction, expected)
		}
	}

	wrapped, err := middleware.WrapInvokableToolCall(
		context.Background(),
		func(context.Context, string, ...einotool.Option) (string, error) {
			return `{"commitSHA":"abc123"}`, nil
		},
		&adk.ToolContext{Name: projectToolCommitProjectFiles},
	)
	if err != nil {
		t.Fatalf("WrapInvokableToolCall returned error: %v", err)
	}
	if _, err := wrapped(context.Background(), `{}`); err != nil {
		t.Fatalf("commit endpoint returned error: %v", err)
	}
	if !runState.ExecutionPlanComplete() {
		t.Fatal("successful commit did not complete the execution plan")
	}

	reportState := &adk.ChatModelAgentState{
		Messages: []*schema.Message{
			projectEinoAssistantPhaseToolResult(projectToolApplyPatch, `{"operation":"apply_patch"}`),
			projectEinoAssistantPhaseToolResult(projectToolVerifyDevelopmentRuntime, `{"status":"ready"}`),
			projectEinoAssistantPhaseToolResult(projectToolCommitProjectFiles, `{"commitSHA":"abc123"}`),
		},
		ToolInfos: []*schema.ToolInfo{
			projectEinoAssistantPhaseToolInfo(projectToolReadFile, projectAssistantToolRiskRead, projectAssistantToolBundleWorkspaceRead),
		},
	}
	_, reportState, err = middleware.BeforeModelRewriteState(context.Background(), reportState, nil)
	if err != nil {
		t.Fatalf("report BeforeModelRewriteState returned error: %v", err)
	}
	if middleware.phase != projectEinoAssistantPhaseReport {
		t.Fatalf("phase = %q, want report", middleware.phase)
	}
	reportInstruction := reportState.Messages[len(reportState.Messages)-1].Content
	for _, expected := range []string{"Respond to the user now", "Do not call tools"} {
		if !strings.Contains(reportInstruction, expected) {
			t.Fatalf("report instruction = %q, want %q", reportInstruction, expected)
		}
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

func TestProjectEinoAssistantPhasePreMutationGateReevaluatesAtInvocation(t *testing.T) {
	plan := projectAssistantApprovedPlan{
		Steps:       []string{"edit", "verify"},
		TargetPaths: []string{"app.js"},
	}
	runState := newProjectEinoAssistantRunState()
	runState.ApprovePlan(plan)
	verifyTool := projectEinoAssistantPhaseToolInfo(
		projectToolVerifyDevelopmentRuntime,
		projectAssistantToolRiskRead,
		projectAssistantToolBundleRuntime,
	)
	middleware := &projectEinoAssistantPhaseFilterMiddleware{
		BaseChatModelAgentMiddleware: &adk.BaseChatModelAgentMiddleware{},
		runState:                     runState,
		phase:                        projectEinoAssistantPhaseMutate,
		approvedPlan:                 &plan,
		toolInfos:                    []*schema.ToolInfo{verifyTool},
	}
	calls := 0
	wrapped, err := middleware.WrapInvokableToolCall(
		context.Background(),
		func(context.Context, string, ...einotool.Option) (string, error) {
			calls++
			return `{"status":"not_ready"}`, nil
		},
		&adk.ToolContext{Name: projectToolVerifyDevelopmentRuntime},
	)
	if err != nil {
		t.Fatal(err)
	}
	result, err := wrapped(context.Background(), `{}`)
	if err != nil {
		t.Fatal(err)
	}
	if result != "Tool call denied: verify_development_runtime is unavailable until an approved source mutation succeeds" || calls != 0 {
		t.Fatalf("pre-mutation verify = %q calls=%d", result, calls)
	}

	runState.RecordSourceMutation()
	result, err = wrapped(context.Background(), `{}`)
	if err != nil {
		t.Fatal(err)
	}
	if result != `{"status":"not_ready"}` || calls != 1 {
		t.Fatalf("post-mutation verify = %q calls=%d", result, calls)
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

func TestProjectEinoAssistantPhaseCorrectiveToolFailuresCountAsNoProgress(t *testing.T) {
	runState := newProjectEinoAssistantRunState()
	runState.NextModelCallOrdinal()
	plan := &projectAssistantApprovedPlan{
		Steps: []string{"update app.js", "verify the runtime"},
	}
	tool := projectEinoAssistantPhaseToolInfo(
		projectToolWriteFile,
		projectAssistantToolRiskWrite,
		projectAssistantToolBundleEdit,
	)
	middleware := &projectEinoAssistantPhaseFilterMiddleware{
		BaseChatModelAgentMiddleware: &adk.BaseChatModelAgentMiddleware{},
		runState:                     runState,
		phase:                        projectEinoAssistantPhaseMutate,
		approvedPlan:                 plan,
		toolInfos:                    []*schema.ToolInfo{tool},
	}
	wrapped, err := middleware.WrapInvokableToolCall(
		context.Background(),
		func(context.Context, string, ...einotool.Option) (string, error) {
			return "Tool call failed: todo tracking must use write_todos", nil
		},
		&adk.ToolContext{Name: projectToolWriteFile},
	)
	if err != nil {
		t.Fatalf("WrapInvokableToolCall returned error: %v", err)
	}
	for _, path := range []string{"todo.md", "todos.md"} {
		if _, err := wrapped(context.Background(), fmt.Sprintf(`{"path":%q,"content":"track"}`, path)); err != nil {
			t.Fatalf("wrapped write_file returned error: %v", err)
		}
	}
	if name, count := runState.ConsecutiveNoProgressModelCalls(); name != "" || count != 1 {
		t.Fatalf("no-progress model calls = %q/%d, want one failed model batch", name, count)
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
			phase:      projectEinoAssistantPhaseRepair,
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

func TestProjectEinoAssistantPhaseApprovalGuidanceInjectedEveryCall(t *testing.T) {
	runState := newProjectEinoAssistantRunState()
	middleware := projectEinoAssistantPhaseMiddleware(projectAssistantRunRequest{}, runState)
	state := &adk.ChatModelAgentState{ToolInfos: []*schema.ToolInfo{
		projectEinoAssistantPhaseToolInfo(projectToolRequestProjectPlanApproval, projectAssistantToolRiskPlan, projectAssistantToolBundleCollaboration),
		projectEinoAssistantPhaseToolInfo(projectToolAskFollowUp, projectAssistantToolRiskInput, projectAssistantToolBundleCollaboration),
		projectEinoAssistantPhaseToolInfo(projectToolLS, projectAssistantToolRiskRead, projectAssistantToolBundleWorkspaceRead),
		projectEinoAssistantPhaseToolInfo(projectToolReadFile, projectAssistantToolRiskRead, projectAssistantToolBundleWorkspaceRead),
		projectEinoAssistantPhaseToolInfo(projectToolGlob, projectAssistantToolRiskRead, projectAssistantToolBundleWorkspaceRead),
		projectEinoAssistantPhaseToolInfo(projectToolGrep, projectAssistantToolRiskRead, projectAssistantToolBundleWorkspaceRead),
	}, DeferredToolInfos: []*schema.ToolInfo{
		projectEinoAssistantPhaseToolInfo(projectToolRequestProjectPlanApproval, projectAssistantToolRiskPlan, projectAssistantToolBundleCollaboration),
		projectEinoAssistantPhaseToolInfo(projectToolAskFollowUp, projectAssistantToolRiskInput, projectAssistantToolBundleCollaboration),
		projectEinoAssistantPhaseToolInfo(projectToolLS, projectAssistantToolRiskRead, projectAssistantToolBundleWorkspaceRead),
		projectEinoAssistantPhaseToolInfo(projectToolReadFile, projectAssistantToolRiskRead, projectAssistantToolBundleWorkspaceRead),
		projectEinoAssistantPhaseToolInfo(projectToolGlob, projectAssistantToolRiskRead, projectAssistantToolBundleWorkspaceRead),
		projectEinoAssistantPhaseToolInfo(projectToolGrep, projectAssistantToolRiskRead, projectAssistantToolBundleWorkspaceRead),
	}}

	_, state, err := middleware.BeforeModelRewriteState(context.Background(), state, nil)
	if err != nil {
		t.Fatalf("first approval call: %v", err)
	}
	if got := state.Messages[len(state.Messages)-1].Content; !strings.Contains(got, "Complete one bounded inspection batch") {
		t.Fatalf("first approval guidance = %q", got)
	}

	runState.RecordReadFileRange("styles.css", 1, 200)
	runState.RecordReadFileRange("app.js", 1, 2000)
	_, state, err = middleware.BeforeModelRewriteState(context.Background(), state, nil)
	if err != nil {
		t.Fatalf("second approval call: %v", err)
	}
	got := state.Messages[len(state.Messages)-1].Content
	for _, want := range []string{
		`Already-read project file paths for the current workspace revision: ["app.js","styles.css"].`,
		"Workspace discovery and search tools are now unavailable in plan approval",
		"A direct read_file remains available only for a concrete dependency path",
		"call request_project_plan_approval now",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("second approval guidance = %q, want %q", got, want)
		}
	}
	guidanceMessages := 0
	for _, message := range state.Messages {
		if message != nil && strings.HasPrefix(message.Content, projectEinoAssistantApprovalProgressPrefix) {
			guidanceMessages++
		}
	}
	if guidanceMessages != 1 {
		t.Fatalf("approval guidance messages = %d, want one refreshed message", guidanceMessages)
	}
	for _, tools := range [][]*schema.ToolInfo{state.ToolInfos, state.DeferredToolInfos} {
		names := projectEinoAssistantPhaseToolNames(tools)
		for _, hidden := range []string{projectToolLS, projectToolGlob, projectToolGrep} {
			if projectEinoAssistantPhaseToolNamesContain(names, hidden) {
				t.Fatalf("post-inspection tools = %#v, want %s hidden", names, hidden)
			}
		}
		for _, retained := range []string{projectToolReadFile, projectToolRequestProjectPlanApproval, projectToolAskFollowUp} {
			if !projectEinoAssistantPhaseToolNamesContain(names, retained) {
				t.Fatalf("post-inspection tools = %#v, want %s retained", names, retained)
			}
		}
	}

	runState.ApprovePlan(projectAssistantApprovedPlan{
		Steps:       []string{"edit"},
		TargetPaths: []string{"app.js"},
	})
	_, state, err = middleware.BeforeModelRewriteState(context.Background(), state, nil)
	if err != nil {
		t.Fatalf("mutate call: %v", err)
	}
	for _, message := range state.Messages {
		if message != nil && strings.HasPrefix(message.Content, projectEinoAssistantApprovalProgressPrefix) {
			t.Fatalf("stale approval guidance remained after phase transition: %q", message.Content)
		}
	}
	mutationGuidance := state.Messages[len(state.Messages)-1].Content
	for _, want := range []string{
		projectEinoAssistantMutationProgressPrefix,
		`Approved source target paths: ["app.js"].`,
		"Apply the approved workspace mutation now",
		"unavailable until a successful source mutation",
	} {
		if !strings.Contains(mutationGuidance, want) {
			t.Fatalf("mutation guidance = %q, want %q", mutationGuidance, want)
		}
	}
}

func TestProjectEinoAssistantPhaseApprovalInspectionPreservesDirectDependencyReadsAndClosesSearch(t *testing.T) {
	runState := newProjectEinoAssistantRunState()
	readInfo := projectEinoAssistantPhaseToolInfo(
		projectToolReadFile,
		projectAssistantToolRiskRead,
		projectAssistantToolBundleWorkspaceRead,
	)
	middleware := projectEinoAssistantPhaseMiddleware(projectAssistantRunRequest{}, runState)
	state := &adk.ChatModelAgentState{ToolInfos: []*schema.ToolInfo{
		readInfo,
		projectEinoAssistantPhaseToolInfo(
			projectToolGrep,
			projectAssistantToolRiskRead,
			projectAssistantToolBundleWorkspaceRead,
		),
		projectEinoAssistantPhaseToolInfo(
			projectToolRequestProjectPlanApproval,
			projectAssistantToolRiskPlan,
			projectAssistantToolBundleCollaboration,
		),
	}}
	if _, _, err := middleware.BeforeModelRewriteState(context.Background(), state, nil); err != nil {
		t.Fatalf("initial approval model call: %v", err)
	}

	calls := 0
	wrapTool := func(name string) adk.InvokableToolCallEndpoint {
		wrapped, err := middleware.WrapInvokableToolCall(
			context.Background(),
			func(context.Context, string, ...einotool.Option) (string, error) {
				calls++
				return "source", nil
			},
			&adk.ToolContext{Name: name},
		)
		if err != nil {
			t.Fatalf("wrap %s: %v", name, err)
		}
		return wrapped
	}
	firstBatchRead := wrapTool(projectToolReadFile)
	runState.RecordReadFileRange("src/App.jsx", 1, 100)
	result, err := firstBatchRead(context.Background(), `{"file_path":"src/index.css"}`)
	if err != nil || result != "source" || calls != 1 {
		t.Fatalf("same-batch read result = %q calls = %d error = %v, want endpoint call", result, calls, err)
	}

	if _, state, err = middleware.BeforeModelRewriteState(context.Background(), state, nil); err != nil {
		t.Fatalf("post-inspection approval model call: %v", err)
	}
	names := projectEinoAssistantPhaseToolNames(state.ToolInfos)
	if !projectEinoAssistantPhaseToolNamesContain(names, projectToolReadFile) {
		t.Fatalf("post-inspection tools = %#v, want direct dependency reads retained", names)
	}
	if projectEinoAssistantPhaseToolNamesContain(names, projectToolGrep) {
		t.Fatalf("post-inspection tools = %#v, want search closed", names)
	}
	result, err = wrapTool(projectToolReadFile)(context.Background(), `{"file_path":"src/Imported.jsx"}`)
	if err != nil {
		t.Fatalf("dependency read returned error: %v", err)
	}
	if result != "source" || calls != 2 {
		t.Fatalf("dependency read result = %q calls = %d, want endpoint call", result, calls)
	}
	result, err = wrapTool(projectToolGrep)(context.Background(), `{"pattern":"other"}`)
	if err != nil {
		t.Fatalf("post-inspection grep returned error: %v", err)
	}
	if result != "Tool call denied: grep is unavailable after the initial approval inspection batch" ||
		calls != 2 {
		t.Fatalf("post-inspection grep result = %q calls = %d, want deterministic denial", result, calls)
	}
}

func TestProjectEinoAssistantPhaseApprovalGrepOnlyKeepsInspectionOpen(t *testing.T) {
	runState := newProjectEinoAssistantRunState()
	runState.RecordCompletedRead(projectToolGrep, `{"pattern":"App"}`)
	middleware := projectEinoAssistantPhaseMiddleware(projectAssistantRunRequest{}, runState)
	state := &adk.ChatModelAgentState{ToolInfos: []*schema.ToolInfo{
		projectEinoAssistantPhaseToolInfo(projectToolReadFile, projectAssistantToolRiskRead, projectAssistantToolBundleWorkspaceRead),
		projectEinoAssistantPhaseToolInfo(projectToolGrep, projectAssistantToolRiskRead, projectAssistantToolBundleWorkspaceRead),
	}}
	var err error
	if _, state, err = middleware.BeforeModelRewriteState(context.Background(), state, nil); err != nil {
		t.Fatalf("grep-only approval model call: %v", err)
	}
	names := projectEinoAssistantPhaseToolNames(state.ToolInfos)
	for _, retained := range []string{projectToolReadFile, projectToolGrep} {
		if !projectEinoAssistantPhaseToolNamesContain(names, retained) {
			t.Fatalf("grep-only tools = %#v, want %s retained", names, retained)
		}
	}
}

func TestProjectEinoAssistantPhaseDistinctCompletedActionsDoNotStop(t *testing.T) {
	runState := newProjectEinoAssistantRunState()
	middleware := &projectEinoAssistantPhaseFilterMiddleware{runState: runState}
	state := &adk.ChatModelAgentState{Messages: []*schema.Message{schema.UserMessage("inspect the project")}}
	for call := 1; call <= 12; call++ {
		runState.RecordCompletedAction(
			projectToolReadFile,
			fmt.Sprintf(`{"file_path":"src/file-%d.tsx","offset":1,"limit":200}`, call),
			true,
		)
		warned, err := middleware.enforceRepeatedActionProgress(state, projectEinoAssistantPhaseApproval)
		if err != nil || warned {
			t.Fatalf("distinct call %d returned warned=%t error=%v", call, warned, err)
		}
	}
}

func TestProjectEinoAssistantPhaseRepeatedActionWarnsWithoutStopping(t *testing.T) {
	runState := newProjectEinoAssistantRunState()
	runState.RecordSourceMutation()
	runState.RecordDevelopmentVerification(true)
	runState.RecordSourceMutation()
	middleware := &projectEinoAssistantPhaseFilterMiddleware{runState: runState}
	state := &adk.ChatModelAgentState{Messages: []*schema.Message{schema.UserMessage("make the change")}}
	for call := 1; call <= 5; call++ {
		runState.RecordCompletedAction(
			projectToolReadFile,
			`{"file_path":"src/App.tsx","offset":1,"limit":200}`,
			true,
		)
		warned, err := middleware.enforceRepeatedActionProgress(state, projectEinoAssistantPhaseMutate)
		switch call {
		case 1:
			if err != nil || warned {
				t.Fatalf("first call returned warned=%t error=%v", warned, err)
			}
		case projectEinoAssistantRepeatedActionWarnAt:
			if err != nil || !warned {
				t.Fatalf("second call returned warned=%t error=%v", warned, err)
			}
			last := state.Messages[len(state.Messages)-1]
			if last.Role != schema.System || !strings.Contains(last.Content, "Apply the approved workspace mutation now") {
				t.Fatalf("warning message = %#v", last)
			}
		default:
			if err != nil || !warned {
				t.Fatalf("repeated call %d returned warned=%t error=%v", call, warned, err)
			}
		}
	}
}

func TestProjectEinoAssistantPhaseRepeatedActionCanonicalizesArgumentsAndResets(t *testing.T) {
	runState := newProjectEinoAssistantRunState()
	middleware := &projectEinoAssistantPhaseFilterMiddleware{runState: runState}
	state := &adk.ChatModelAgentState{Messages: []*schema.Message{schema.UserMessage("inspect")}}
	first, err := projectEinoToolArguments(`{"limit":200,"file_path":"src/App.tsx"}`)
	if err != nil {
		t.Fatal(err)
	}
	second, err := projectEinoToolArguments(`{"file_path":"src/App.tsx","limit":200}`)
	if err != nil {
		t.Fatal(err)
	}
	runState.RecordCompletedAction(projectToolReadFile, projectEinoToolArgumentsString(first), true)
	runState.RecordCompletedAction(projectToolReadFile, projectEinoToolArgumentsString(second), true)
	warned, err := middleware.enforceRepeatedActionProgress(state, projectEinoAssistantPhaseApproval)
	if err != nil || !warned {
		t.Fatalf("canonical repeat returned warned=%t error=%v", warned, err)
	}

	runState.RecordCompletedAction(projectToolReadFile, `{"file_path":"src/Other.tsx","limit":200}`, true)
	warned, err = middleware.enforceRepeatedActionProgress(state, projectEinoAssistantPhaseApproval)
	if err != nil || warned {
		t.Fatalf("different action returned warned=%t error=%v", warned, err)
	}
}

func TestProjectEinoAssistantPhaseRepeatedActionUsesCallOrderForBatches(t *testing.T) {
	repeatedArgs := `{"file_path":"src/App.tsx","limit":200}`
	runState := newProjectEinoAssistantRunState()
	runState.RecordCompletedAction(projectToolReadFile, repeatedArgs, true)
	runState.RecordCompletedAction(projectToolReadFile, repeatedArgs, true)
	runState.RecordCompletedAction(projectToolReadFile, repeatedArgs, true)
	name, repeats := runState.RepeatedCompletedAction()
	if name != projectToolReadFile || repeats != 3 {
		t.Fatalf("repeated batch = (%q, %d), want (%q, 3)", name, repeats, projectToolReadFile)
	}

	runState.RecordCompletedAction(projectToolGrep, `{"pattern":"App"}`, true)
	runState.RecordCompletedAction(projectToolReadFile, repeatedArgs, true)
	name, repeats = runState.RepeatedCompletedAction()
	if name != projectToolReadFile || repeats != 1 {
		t.Fatalf("interleaved batch = (%q, %d), want trailing read streak 1", name, repeats)
	}
}

func TestProjectEinoAssistantPhaseRepeatedSkippedReadsWarnWithoutStopping(t *testing.T) {
	runState := newProjectEinoAssistantRunState()
	middleware := &projectEinoAssistantPhaseFilterMiddleware{runState: runState}
	state := &adk.ChatModelAgentState{Messages: []*schema.Message{schema.UserMessage("inspect")}}
	for batch := 1; batch <= 5; batch++ {
		runState.NextModelCallOrdinal()
		for index, name := range []string{projectToolReadFile, projectToolGrep, projectToolLS} {
			runState.RecordCompletedAction(name, fmt.Sprintf(`{"cycle":%d}`, index), false)
		}
		warned, err := middleware.enforceRepeatedActionProgress(state, projectEinoAssistantPhaseApproval)
		switch batch {
		case 1:
			if err != nil || warned {
				t.Fatalf("first skipped batch returned warned=%t error=%v", warned, err)
			}
		case projectEinoAssistantRepeatedActionWarnAt:
			if err != nil || !warned {
				t.Fatalf("second skipped batch returned warned=%t error=%v", warned, err)
			}
		default:
			if err != nil || !warned {
				t.Fatalf("skipped batch %d returned warned=%t error=%v", batch, warned, err)
			}
		}
	}
}

func TestProjectEinoAssistantPhaseAnyProgressMakesModelBatchProductive(t *testing.T) {
	runState := newProjectEinoAssistantRunState()
	runState.NextModelCallOrdinal()
	runState.RecordCompletedAction(projectToolReadFile, `{"file_path":"src/App.tsx"}`, false)
	runState.RecordCompletedAction(projectToolGrep, `{"pattern":"new evidence"}`, true)
	runState.RecordCompletedAction(projectToolLS, `{"path":"src"}`, false)
	if _, count := runState.ConsecutiveNoProgressModelCalls(); count != 0 {
		t.Fatalf("no-progress model calls = %d, want productive batch to reset the counter", count)
	}
}

func TestProjectEinoAssistantPhaseDeniedToolCountsAsNoProgress(t *testing.T) {
	runState := newProjectEinoAssistantRunState()
	runState.NextModelCallOrdinal()
	tool := projectEinoAssistantPhaseToolInfo(
		projectToolWriteFile,
		projectAssistantToolRiskWrite,
		projectAssistantToolBundleEdit,
	)
	middleware := &projectEinoAssistantPhaseFilterMiddleware{
		BaseChatModelAgentMiddleware: &adk.BaseChatModelAgentMiddleware{},
		req: projectAssistantRunRequest{
			TurnProfile: projectAssistantTurnProfileImplementation,
			TurnPolicy:  projectAssistantTurnPolicyForProfile(projectAssistantTurnProfileImplementation),
		},
		runState:  runState,
		phase:     projectEinoAssistantPhaseApproval,
		toolInfos: []*schema.ToolInfo{tool},
	}
	if !projectEinoAssistantPhaseLifecycleApplies(middleware.req, runState) {
		t.Fatal("mutation lifecycle did not apply")
	}
	if projectEinoAssistantPhaseAllowsTool(
		projectEinoAssistantPhaseApproval,
		nil,
		false,
		tool,
		false,
		middleware.req.TurnPolicy,
	) {
		t.Fatal("approval phase unexpectedly allowed runtime verifier")
	}
	wrapped, err := middleware.WrapInvokableToolCall(
		context.Background(),
		func(context.Context, string, ...einotool.Option) (string, error) {
			return "unexpected", nil
		},
		&adk.ToolContext{Name: projectToolWriteFile},
	)
	if err != nil {
		t.Fatal(err)
	}
	result, err := wrapped(context.Background(), `{}`)
	if err != nil || !strings.Contains(result, "unavailable in the current assistant phase") {
		t.Fatalf("denied tool result = (%q, %v)", result, err)
	}
	if _, count := runState.ConsecutiveNoProgressModelCalls(); count != 1 {
		t.Fatalf("denied tool model batch count = %d, want 1", count)
	}
}

func TestProjectEinoAssistantPhaseEmitsVerificationGraphToolLifecycle(t *testing.T) {
	runState := newProjectEinoAssistantRunState()
	runState.ApprovePlan(projectAssistantApprovedPlan{Steps: []string{"edit", "verify"}})
	runState.RecordSourceMutation()
	var events []projectToolCallStreamEvent
	var statuses []string
	tool := projectEinoAssistantPhaseToolInfo(
		projectToolVerifyDevelopmentRuntime,
		projectAssistantToolRiskRead,
		projectAssistantToolBundleRuntime,
	)
	middleware := &projectEinoAssistantPhaseFilterMiddleware{
		BaseChatModelAgentMiddleware: &adk.BaseChatModelAgentMiddleware{},
		req: projectAssistantRunRequest{StreamCallbacks: projectAssistantStreamCallbacks{
			OnToolCall: func(event projectToolCallStreamEvent) {
				events = append(events, event)
			},
			OnStatus: func(status string) {
				statuses = append(statuses, status)
			},
		}},
		runState:     runState,
		phase:        projectEinoAssistantPhaseMutate,
		approvedPlan: &projectAssistantApprovedPlan{Steps: []string{"edit", "verify"}},
		toolInfos:    []*schema.ToolInfo{tool},
	}
	wrapped, err := middleware.WrapInvokableToolCall(
		context.Background(),
		func(context.Context, string, ...einotool.Option) (string, error) {
			return `{"checkedMutationRevision":4,"status":"ready"}`, nil
		},
		&adk.ToolContext{Name: projectToolVerifyDevelopmentRuntime, CallID: "call-verify"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := wrapped(context.Background(), `{}`); err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 ||
		events[0].ID != "call-verify" ||
		events[0].Status != "running" ||
		events[1].Status != "succeeded" ||
		!strings.Contains(events[1].Summary, "revision 4") {
		t.Fatalf("verification events = %#v", events)
	}
	if !reflect.DeepEqual(statuses, []string{
		"Verifying development runtime",
		"Reviewing verification results",
	}) {
		t.Fatalf("verification statuses = %#v", statuses)
	}
}

func TestProjectEinoAssistantPhaseEmitsVerificationGraphToolFailure(t *testing.T) {
	runState := newProjectEinoAssistantRunState()
	runState.NextModelCallOrdinal()
	runState.RecordSourceMutation()
	plan := &projectAssistantApprovedPlan{Steps: []string{"verify"}}
	tool := projectEinoAssistantPhaseToolInfo(
		projectToolVerifyDevelopmentRuntime,
		projectAssistantToolRiskRead,
		projectAssistantToolBundleRuntime,
	)
	var events []projectToolCallStreamEvent
	middleware := &projectEinoAssistantPhaseFilterMiddleware{
		BaseChatModelAgentMiddleware: &adk.BaseChatModelAgentMiddleware{},
		req: projectAssistantRunRequest{StreamCallbacks: projectAssistantStreamCallbacks{
			OnToolCall: func(event projectToolCallStreamEvent) {
				events = append(events, event)
			},
		}},
		runState:     runState,
		phase:        projectEinoAssistantPhaseMutate,
		approvedPlan: plan,
		toolInfos:    []*schema.ToolInfo{tool},
	}
	wrapped, err := middleware.WrapInvokableToolCall(
		context.Background(),
		func(context.Context, string, ...einotool.Option) (string, error) {
			return "", errors.New("runtime token=secret-value failed")
		},
		&adk.ToolContext{Name: projectToolVerifyDevelopmentRuntime, CallID: "call-verify"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := wrapped(context.Background(), `{}`); err == nil {
		t.Fatal("verification error = nil")
	}
	if len(events) != 2 || events[0].Status != "running" || events[1].Status != "failed" {
		t.Fatalf("verification events = %#v", events)
	}
	if strings.Contains(events[1].Error, "secret-value") {
		t.Fatalf("verification event leaked secret: %#v", events[1])
	}
	if _, count := runState.ConsecutiveNoProgressModelCalls(); count != 1 {
		t.Fatalf("failed verification model batch count = %d, want 1", count)
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
	for call := 0; call < maxAssistantDeepIterations+1; call++ {
		var err error
		_, state, err = middleware.BeforeModelRewriteState(context.Background(), state, nil)
		if err != nil {
			t.Fatalf("read-only call %d returned error: %v", call+1, err)
		}
	}
}

func TestProjectEinoAssistantPhaseAuditsModelCallsWithoutPhaseBudget(t *testing.T) {
	runState := newProjectEinoAssistantRunState()
	runState.ApprovePlan(projectAssistantApprovedPlan{Steps: []string{"edit"}})
	runState.RecordSourceMutation()
	runState.RecordDevelopmentVerification(false)
	run := &store.AssistantRun{ID: "run-repair-audit"}
	recorder := newProjectAssistantRunAuditRecorder(projectAssistantRunRequest{}, run, time.Now().UTC())
	middleware := projectEinoAssistantPhaseMiddleware(projectAssistantRunRequest{auditRecorder: recorder}, runState)
	state := &adk.ChatModelAgentState{
		Messages: []*schema.Message{
			schema.UserMessage("make the change"),
			schema.ToolMessage(`{"operation":"write_file"}`, "write-1", schema.WithToolName(projectToolWriteFile)),
			schema.ToolMessage(`{"status":"not_ready","logs":{"status":"failed","blockers":["SyntaxError"]}}`, "verify-1", schema.WithToolName(projectToolVerifyDevelopmentRuntime)),
		},
		ToolInfos: []*schema.ToolInfo{
			projectEinoAssistantPhaseToolInfo(projectToolReadFile, projectAssistantToolRiskRead, projectAssistantToolBundleWorkspaceRead),
			projectEinoAssistantPhaseToolInfo(projectToolVerifyDevelopmentRuntime, projectAssistantToolRiskRead, projectAssistantToolBundleRuntime),
		},
	}
	_, _, err := middleware.BeforeModelRewriteState(context.Background(), state, nil)
	if err != nil {
		t.Fatalf("BeforeModelRewriteState returned error: %v", err)
	}
	var audit projectAssistantRunAudit
	if err := json.Unmarshal(run.Audit, &audit); err != nil {
		t.Fatalf("decode audit: %v", err)
	}
	if len(audit.ModelCalls) != 1 ||
		audit.ModelCalls[0].Phase != projectEinoAssistantPhaseRepair ||
		audit.ModelCalls[0].Ordinal != 1 {
		t.Fatalf("model calls = %#v, want one audited repair call", audit.ModelCalls)
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

func projectEinoAssistantAppendCompletedAction(
	state *adk.ChatModelAgentState,
	callID string,
	name string,
	arguments string,
) {
	state.Messages = append(
		state.Messages,
		schema.AssistantMessage("", []schema.ToolCall{{
			ID:   callID,
			Type: "function",
			Function: schema.FunctionCall{
				Name:      name,
				Arguments: arguments,
			},
		}}),
		schema.ToolMessage("ok", callID, schema.WithToolName(name)),
	)
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
