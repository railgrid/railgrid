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
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/cloudwego/eino/adk"
	einomodel "github.com/cloudwego/eino/components/model"
	einotool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"github.com/google/uuid"

	aiv1alpha1 "github.com/faroshq/provider-app-studio/apis/ai/v1alpha1"
	"github.com/faroshq/provider-app-studio/workspace"
)

type projectEinoAssistantPhase string

type projectEinoAssistantNoProgressError struct {
	Phase            projectEinoAssistantPhase
	ToolName         string
	Calls            int
	Limit            int
	SourceRevision   uint64
	VerifiedRevision uint64
}

func (e *projectEinoAssistantNoProgressError) Error() string {
	if e == nil {
		return errProjectAssistantNoProgress.Error()
	}
	if toolName := projectToolBaseName(e.ToolName); toolName != "" {
		return fmt.Sprintf(
			"%s: phase %s repeated %s %d times",
			errProjectAssistantNoProgress,
			e.Phase,
			toolName,
			e.Limit,
		)
	}
	return fmt.Sprintf(
		"%s: phase %s completed %d consecutive model turns without new progress",
		errProjectAssistantNoProgress,
		e.Phase,
		e.Limit,
	)
}

func (e *projectEinoAssistantNoProgressError) Unwrap() error {
	return errProjectAssistantNoProgress
}

const (
	projectEinoAssistantPhasePlan     projectEinoAssistantPhase = "plan"
	projectEinoAssistantPhaseApproval projectEinoAssistantPhase = "approval"
	projectEinoAssistantPhaseMutate   projectEinoAssistantPhase = "mutate"
	projectEinoAssistantPhaseVerify   projectEinoAssistantPhase = "verify"
	projectEinoAssistantPhaseRepair   projectEinoAssistantPhase = "repair"
	projectEinoAssistantPhaseWarmup   projectEinoAssistantPhase = "warmup"
	projectEinoAssistantPhaseCommit   projectEinoAssistantPhase = "commit"
	projectEinoAssistantPhaseReport   projectEinoAssistantPhase = "report"
)

const (
	projectEinoAssistantWriteTodosTool = "write_todos"
	projectEinoAssistantToolSearchTool = "tool_search"
)

const (
	projectEinoAssistantTodoProgressMaxItems      = 50
	projectEinoAssistantTodoProgressMaxInputBytes = 64 * 1024
	projectEinoAssistantTodoProgressMaxLabelBytes = 120
)

const (
	projectEinoAssistantRepeatedActionWarnAt = 2
	projectEinoAssistantRepeatedActionLimit  = maxAssistantDeepIterations
)

type projectEinoAssistantTodoProgressInput struct {
	Todos []projectEinoAssistantTodoProgressItem `json:"todos"`
}

type projectEinoAssistantTodoProgressItem struct {
	Content    string `json:"content"`
	ActiveForm string `json:"activeForm"`
	Status     string `json:"status"`
}

type projectEinoAssistantPhaseFilterMiddleware struct {
	*adk.BaseChatModelAgentMiddleware

	req                  projectAssistantRunRequest
	runState             *projectEinoAssistantRunState
	toolInfos            []*schema.ToolInfo
	deferredToolInfos    []*schema.ToolInfo
	phase                projectEinoAssistantPhase
	approvedPlan         *projectAssistantApprovedPlan
	modelCallPhase       projectEinoAssistantPhase
	modelCallOrdinal     int
	approvalSearchClosed bool
	recoveryReadPaths    []string
	recoveryPatchPath    string
	recoveryReadComplete bool
	mustApplyPatch       bool
	missingMutationPaths []string
}

type projectEinoAssistantCompletionBarrierModel struct {
	einomodel.BaseChatModel

	verificationToolName string
	runState             *projectEinoAssistantRunState
}

func (m *projectEinoAssistantCompletionBarrierModel) Generate(
	ctx context.Context,
	input []*schema.Message,
	opts ...einomodel.Option,
) (*schema.Message, error) {
	message, err := m.BaseChatModel.Generate(ctx, input, opts...)
	if err != nil {
		return nil, err
	}
	return projectEinoAssistantCompletionBarrierMessage(message, m.verificationToolName, m.runState), nil
}

func (m *projectEinoAssistantCompletionBarrierModel) Stream(
	ctx context.Context,
	input []*schema.Message,
	opts ...einomodel.Option,
) (*schema.StreamReader[*schema.Message], error) {
	reader, err := m.BaseChatModel.Stream(ctx, input, opts...)
	if err != nil {
		return nil, err
	}
	message, err := schema.ConcatMessageStream(reader)
	if err != nil {
		return nil, fmt.Errorf("combine dirty assistant completion stream: %w", err)
	}
	if message == nil {
		return nil, errors.New("dirty assistant completion returned an empty model stream")
	}
	message = projectEinoAssistantCompletionBarrierMessage(message, m.verificationToolName, m.runState)
	return schema.StreamReaderFromArray([]*schema.Message{message}), nil
}

func projectEinoAssistantCompletionBarrierMessage(
	message *schema.Message,
	verificationToolName string,
	runState *projectEinoAssistantRunState,
) *schema.Message {
	if message == nil || len(message.ToolCalls) > 0 ||
		(strings.TrimSpace(message.Content) == "" &&
			strings.TrimSpace(message.ReasoningContent) == "" &&
			len(message.AssistantGenMultiContent) == 0) {
		return message
	}
	result := *message
	result.Role = schema.Assistant
	result.Content = ""
	result.MultiContent = nil
	result.UserInputMultiContent = nil
	result.AssistantGenMultiContent = nil
	result.Name = ""
	result.ToolCallID = ""
	result.ToolName = ""
	result.ReasoningContent = ""
	result.ToolCalls = []schema.ToolCall{{
		ID:   "call-" + uuid.NewString(),
		Type: "function",
		Function: schema.FunctionCall{
			Name:      strings.TrimSpace(verificationToolName),
			Arguments: `{}`,
		},
	}}
	adk.EnsureMessageID(&result)
	runState.RewriteCompletionAsVerification(&result)
	return &result
}

func projectEinoAssistantPhaseMiddleware(
	req projectAssistantRunRequest,
	runState *projectEinoAssistantRunState,
) adk.ChatModelAgentMiddleware {
	phase := projectEinoAssistantPhaseApproval
	if req.Continuation != nil {
		if messages, err := projectChatMessagesToEino(req.Continuation.Messages); err == nil {
			state := &adk.ChatModelAgentState{Messages: messages}
			phase = projectEinoAssistantPhaseForState(req, runState, state)
			// Asking permission for a commit atomically replaces the WorkItem
			// plan grant with a tombstone before checkpointing. Restore only the
			// execution phase for that exact interrupted commit after re-validating
			// write -> verification ordering from the persisted message history.
			if phase == projectEinoAssistantPhaseApproval &&
				req.Continuation.Eino != nil &&
				projectEinoAssistantCommitTool(req.Continuation.Eino.ToolName) {
				phase = projectEinoAssistantPhaseForStateWithApproval(req, runState, state, true)
			}
		}
	}
	return &projectEinoAssistantPhaseFilterMiddleware{
		BaseChatModelAgentMiddleware: &adk.BaseChatModelAgentMiddleware{},
		req:                          req,
		runState:                     runState,
		phase:                        phase,
		approvedPlan:                 cloneProjectAssistantApprovedPlan(projectEinoAssistantPhaseApprovedPlan(req, runState)),
	}
}

// BeforeAgent captures the full tool inventory after earlier middleware has
// augmented the agent. Eino persists rewritten ToolInfos across interrupts, so
// the next agent instance must not treat that prior phase-filtered slice as the
// canonical catalog.
func (m *projectEinoAssistantPhaseFilterMiddleware) BeforeAgent(
	ctx context.Context,
	runCtx *adk.ChatModelAgentContext,
) (context.Context, *adk.ChatModelAgentContext, error) {
	if runCtx == nil {
		return ctx, runCtx, nil
	}
	infos := make([]*schema.ToolInfo, 0, len(runCtx.Tools))
	for _, tool := range runCtx.Tools {
		if tool == nil {
			continue
		}
		info, err := tool.Info(ctx)
		if err != nil {
			return ctx, nil, fmt.Errorf("read phase tool info: %w", err)
		}
		infos = append(infos, info)
	}
	m.toolInfos = projectEinoAssistantPhaseMergeTools(m.toolInfos, infos)
	return ctx, runCtx, nil
}

func (m *projectEinoAssistantPhaseFilterMiddleware) BeforeModelRewriteState(
	ctx context.Context,
	state *adk.ChatModelAgentState,
	_ *adk.ModelContext,
) (context.Context, *adk.ChatModelAgentState, error) {
	if state == nil {
		return ctx, state, nil
	}
	m.toolInfos = projectEinoAssistantPhaseMergeTools(m.toolInfos, state.ToolInfos)
	m.deferredToolInfos = projectEinoAssistantPhaseMergeTools(m.deferredToolInfos, state.DeferredToolInfos)
	// Read-only profiles already receive their exact tool set from turn-policy
	// filtering. They do not participate in the mutation lifecycle, but still
	// need the canonical static inventory after resuming a legacy checkpoint
	// whose persisted ToolInfos were phase-filtered.
	if !projectEinoAssistantPhaseLifecycleApplies(m.req, m.runState) {
		templateBootstrapAllowed := projectEinoAssistantPhaseTemplateBootstrapAllowed(m.req.Project)
		state.ToolInfos = projectEinoAssistantPhaseFilterReadOnlyTools(
			templateBootstrapAllowed,
			projectEinoAssistantPhaseVisibleTools(m.toolInfos, state.ToolInfos),
		)
		state.DeferredToolInfos = projectEinoAssistantPhaseFilterReadOnlyTools(
			templateBootstrapAllowed,
			projectEinoAssistantPhaseMergeTools(m.deferredToolInfos, state.DeferredToolInfos),
		)
		return ctx, state, nil
	}
	phase := projectEinoAssistantPhaseForState(m.req, m.runState, state)
	if phase == projectEinoAssistantPhaseWarmup {
		m.runState.BeginRuntimeWarmupModelCall()
	}
	warningInjected, err := m.enforceRepeatedActionProgress(state, phase)
	if err != nil {
		return ctx, nil, err
	}
	approvedPlan := projectEinoAssistantPhaseApprovedPlan(m.req, m.runState)
	sourceRevision, verifiedRevision := m.runState.SourceMutationRevisions()
	completedReadPaths := m.runState.CompletedReadFilePaths()
	observedReadPaths := m.runState.ObservedReadFilePaths()
	hideWriteFile := !projectAssistantInitialBuildActive(m.req, m.runState) &&
		projectEinoAssistantAllApprovedTargetsAreKnownExisting(approvedPlan, observedReadPaths)
	approvalSearchClosed := phase == projectEinoAssistantPhaseApproval &&
		len(completedReadPaths) > 0
	awaitingFirstMutation := phase == projectEinoAssistantPhaseMutate &&
		projectEinoAssistantAwaitingFirstApprovedMutation(m.runState, approvedPlan)
	recoveryReadPath, recoveryReadComplete := m.runState.PatchRecovery()
	if recoveryReadPath == "" &&
		(phase == projectEinoAssistantPhaseMutate || phase == projectEinoAssistantPhaseRepair) {
		recoveryReadPath = projectEinoAssistantLatestFailedPatchPath(state, approvedPlan)
		if recoveryReadPath != "" {
			m.runState.StartPatchRecovery(recoveryReadPath)
		}
	}
	mutationReadPaths := []string(nil)
	if awaitingFirstMutation {
		mutationReadPaths = projectEinoAssistantUnreadKnownExistingApprovedTargets(
			approvedPlan,
			observedReadPaths,
			completedReadPaths,
		)
	}
	if recoveryReadPath != "" && !recoveryReadComplete &&
		!slices.Contains(mutationReadPaths, recoveryReadPath) {
		mutationReadPaths = append(mutationReadPaths, recoveryReadPath)
	}
	mustApplyPatch := awaitingFirstMutation &&
		recoveryReadPath == "" &&
		len(mutationReadPaths) == 0 &&
		projectEinoAssistantAllApprovedTargetsAreKnownExisting(approvedPlan, observedReadPaths)
	missingMutationPaths := projectEinoAssistantMissingKnownExistingMutationTargets(
		approvedPlan,
		observedReadPaths,
		m.runState.SuccessfulMutationPaths(),
	)
	if phase == projectEinoAssistantPhaseApproval {
		projectEinoAssistantRemoveMutationProgressInstruction(state)
		projectEinoAssistantRemoveCommitProgressInstruction(state)
		projectEinoAssistantReplaceApprovalProgressInstruction(
			state,
			projectEinoAssistantPhaseProgressInstruction(
				phase,
				completedReadPaths,
			),
		)
	} else if phase == projectEinoAssistantPhaseMutate {
		projectEinoAssistantRemoveApprovalProgressInstruction(state)
		projectEinoAssistantRemoveCommitProgressInstruction(state)
		projectEinoAssistantReplaceMutationProgressInstruction(
			state,
			projectEinoAssistantMutationProgressInstruction(
				approvedPlan,
				sourceRevision,
				verifiedRevision,
				awaitingFirstMutation,
				observedReadPaths,
				len(mutationReadPaths) > 0,
				missingMutationPaths,
			),
		)
	} else if phase == projectEinoAssistantPhaseCommit {
		projectEinoAssistantRemoveApprovalProgressInstruction(state)
		projectEinoAssistantRemoveMutationProgressInstruction(state)
		projectEinoAssistantReplaceCommitProgressInstruction(
			state,
			"Runtime verification passed for the current workspace revision. Call commit_project_files now. Do not request reads, checkpoints, edits, runtime tools, or replanning in this phase.",
		)
	} else if phase == projectEinoAssistantPhaseReport {
		projectEinoAssistantRemoveApprovalProgressInstruction(state)
		projectEinoAssistantRemoveMutationProgressInstruction(state)
		projectEinoAssistantReplaceCommitProgressInstruction(
			state,
			"The approved implementation is committed and runtime verification passed. Respond to the user now with a concise summary of the completed changes and verification. Do not call tools, request approval, or start more work.",
		)
	} else if phase == projectEinoAssistantPhaseWarmup {
		projectEinoAssistantRemoveApprovalProgressInstruction(state)
		projectEinoAssistantRemoveCommitProgressInstruction(state)
		projectEinoAssistantReplaceMutationProgressInstruction(
			state,
			"The source edit is complete, but the runtime is still becoming available. Do not inspect or edit workspace files. Use only bounded runtime status, preview, log, or verification checks; source repair is allowed only after verification reports concrete build or application log blockers.",
		)
	} else {
		projectEinoAssistantRemoveApprovalProgressInstruction(state)
		projectEinoAssistantRemoveMutationProgressInstruction(state)
		projectEinoAssistantRemoveCommitProgressInstruction(state)
	}
	templateBootstrapAllowed := projectEinoAssistantPhaseTemplateBootstrapAllowed(m.req.Project)
	m.phase = phase
	m.approvedPlan = cloneProjectAssistantApprovedPlan(approvedPlan)
	m.approvalSearchClosed = approvalSearchClosed
	if m.req.auditRecorder != nil {
		m.req.auditRecorder.recordPhase(phase)
	}
	state.ToolInfos = projectEinoAssistantPhaseFilterTools(
		phase,
		approvedPlan,
		templateBootstrapAllowed,
		projectEinoAssistantPhaseVisibleTools(m.toolInfos, state.ToolInfos),
		projectAssistantInlinePromotionEnabled(m.req),
		normalizeProjectAssistantTurnPolicy(m.req.TurnPolicy, m.req.TurnProfile),
	)
	if awaitingFirstMutation {
		state.ToolInfos = projectEinoAssistantPhaseFilterPreMutationTools(
			state.ToolInfos,
			templateBootstrapAllowed,
		)
	}
	if approvalSearchClosed {
		state.ToolInfos = projectEinoAssistantPhaseFilterPostInspectionSearchTools(state.ToolInfos)
	}
	if hideWriteFile {
		state.ToolInfos = projectEinoAssistantPhaseWithoutWriteFile(state.ToolInfos)
	}
	if len(mutationReadPaths) > 0 {
		state.ToolInfos = projectEinoAssistantPhaseWithNamedTool(
			state.ToolInfos,
			m.toolInfos,
			projectToolReadFile,
		)
	}
	if recoveryReadPath != "" && !recoveryReadComplete {
		state.ToolInfos = projectEinoAssistantPhaseOnlyNamedTool(state.ToolInfos, projectToolReadFile)
	} else if recoveryReadPath != "" {
		state.ToolInfos = projectEinoAssistantPhaseOnlyNamedTool(state.ToolInfos, projectToolApplyPatch)
	} else if mustApplyPatch {
		state.ToolInfos = projectEinoAssistantPhaseOnlyNamedTool(state.ToolInfos, projectToolApplyPatch)
	} else if len(missingMutationPaths) > 0 {
		state.ToolInfos = projectEinoAssistantPhaseWithoutNamedTool(
			state.ToolInfos,
			projectToolVerifyDevelopmentRuntime,
		)
	}
	state.DeferredToolInfos = projectEinoAssistantPhaseFilterTools(
		phase,
		approvedPlan,
		templateBootstrapAllowed,
		m.deferredToolInfos,
		projectAssistantInlinePromotionEnabled(m.req),
		normalizeProjectAssistantTurnPolicy(m.req.TurnPolicy, m.req.TurnProfile),
	)
	if awaitingFirstMutation {
		state.DeferredToolInfos = projectEinoAssistantPhaseFilterPreMutationTools(
			state.DeferredToolInfos,
			templateBootstrapAllowed,
		)
	}
	if approvalSearchClosed {
		state.DeferredToolInfos = projectEinoAssistantPhaseFilterPostInspectionSearchTools(state.DeferredToolInfos)
	}
	if hideWriteFile {
		state.DeferredToolInfos = projectEinoAssistantPhaseWithoutWriteFile(state.DeferredToolInfos)
	}
	if len(mutationReadPaths) > 0 {
		state.DeferredToolInfos = projectEinoAssistantPhaseWithNamedTool(
			state.DeferredToolInfos,
			m.deferredToolInfos,
			projectToolReadFile,
		)
	}
	if recoveryReadPath != "" && !recoveryReadComplete {
		state.DeferredToolInfos = projectEinoAssistantPhaseOnlyNamedTool(
			state.DeferredToolInfos,
			projectToolReadFile,
		)
	} else if recoveryReadPath != "" {
		state.DeferredToolInfos = projectEinoAssistantPhaseOnlyNamedTool(
			state.DeferredToolInfos,
			projectToolApplyPatch,
		)
	} else if mustApplyPatch {
		state.DeferredToolInfos = projectEinoAssistantPhaseOnlyNamedTool(
			state.DeferredToolInfos,
			projectToolApplyPatch,
		)
	} else if len(missingMutationPaths) > 0 {
		state.DeferredToolInfos = projectEinoAssistantPhaseWithoutNamedTool(
			state.DeferredToolInfos,
			projectToolVerifyDevelopmentRuntime,
		)
	}
	if projectAssistantInlinePromotionEnabled(m.req) &&
		phase == projectEinoAssistantPhaseApproval {
		state.ToolInfos = projectEinoAssistantPhaseWithoutToolSearch(state.ToolInfos)
	}
	m.modelCallPhase = phase
	m.modelCallOrdinal = m.runState.NextModelCallOrdinal()
	m.recoveryReadPaths = append([]string(nil), mutationReadPaths...)
	m.recoveryPatchPath = recoveryReadPath
	m.recoveryReadComplete = recoveryReadComplete
	m.mustApplyPatch = mustApplyPatch
	m.missingMutationPaths = append([]string(nil), missingMutationPaths...)
	if m.req.auditRecorder != nil {
		if err := m.req.auditRecorder.recordModelCall(
			ctx,
			phase,
			m.modelCallOrdinal,
			sourceRevision,
			verifiedRevision,
			warningInjected,
			state.ToolInfos,
			state.DeferredToolInfos,
		); err != nil {
			return ctx, nil, fmt.Errorf("persist assistant model-call start: %w", err)
		}
	}
	return ctx, state, nil
}

func (m *projectEinoAssistantPhaseFilterMiddleware) WrapModel(
	_ context.Context,
	base einomodel.BaseChatModel,
	modelCtx *adk.ModelContext,
) (einomodel.BaseChatModel, error) {
	if base == nil || m.runState == nil ||
		(m.phase != projectEinoAssistantPhaseMutate && m.phase != projectEinoAssistantPhaseRepair) {
		return base, nil
	}
	if !m.runState.NeedsCompletionVerification() {
		return base, nil
	}
	verificationToolName := ""
	if modelCtx != nil {
		for _, tool := range modelCtx.Tools {
			if tool != nil && projectToolBaseName(tool.Name) == projectToolVerifyDevelopmentRuntime {
				verificationToolName = strings.TrimSpace(tool.Name)
				break
			}
		}
	}
	if verificationToolName == "" {
		return base, nil
	}
	return &projectEinoAssistantCompletionBarrierModel{
		BaseChatModel:        base,
		verificationToolName: verificationToolName,
		runState:             m.runState,
	}, nil
}

func (m *projectEinoAssistantPhaseFilterMiddleware) AfterModelRewriteState(
	ctx context.Context,
	state *adk.ChatModelAgentState,
	_ *adk.ModelContext,
) (context.Context, *adk.ChatModelAgentState, error) {
	if m.req.auditRecorder != nil && m.modelCallPhase != "" && m.modelCallOrdinal > 0 {
		var response *schema.Message
		if state != nil && len(state.Messages) > 0 {
			response = state.Messages[len(state.Messages)-1]
		}
		if err := m.req.auditRecorder.recordModelResult(
			ctx,
			m.modelCallPhase,
			m.modelCallOrdinal,
			response,
		); err != nil {
			return ctx, nil, fmt.Errorf("persist assistant model-call result: %w", err)
		}
	}
	m.modelCallPhase = ""
	m.modelCallOrdinal = 0
	return ctx, state, nil
}

func (m *projectEinoAssistantPhaseFilterMiddleware) enforceRepeatedActionProgress(
	state *adk.ChatModelAgentState,
	phase projectEinoAssistantPhase,
) (bool, error) {
	_, repeats := m.runState.RepeatedCompletedAction()
	if repeats < projectEinoAssistantRepeatedActionWarnAt {
		return false, nil
	}
	if state != nil && phase != projectEinoAssistantPhaseApproval {
		instruction := projectEinoAssistantPhaseProgressInstruction(
			phase,
			m.runState.CompletedReadFilePaths(),
		)
		if recovery := projectEinoAssistantLatestEditRecoveryInstruction(state); recovery != "" {
			instruction = recovery
		}
		state.Messages = append(
			state.Messages,
			schema.SystemMessage(instruction),
		)
	}
	return true, nil
}

const projectEinoAssistantApprovalProgressPrefix = "[App Studio approval phase] "
const projectEinoAssistantMutationProgressPrefix = "[App Studio mutation phase] "
const projectEinoAssistantCommitProgressPrefix = "[App Studio commit phase] "

func projectEinoAssistantReplaceApprovalProgressInstruction(
	state *adk.ChatModelAgentState,
	instruction string,
) {
	if state == nil {
		return
	}
	projectEinoAssistantRemoveApprovalProgressInstruction(state)
	state.Messages = append(
		state.Messages,
		schema.SystemMessage(projectEinoAssistantApprovalProgressPrefix+instruction),
	)
}

func projectEinoAssistantRemoveApprovalProgressInstruction(state *adk.ChatModelAgentState) {
	if state == nil {
		return
	}
	messages := state.Messages[:0]
	for _, message := range state.Messages {
		if message != nil &&
			message.Role == schema.System &&
			strings.HasPrefix(message.Content, projectEinoAssistantApprovalProgressPrefix) {
			continue
		}
		messages = append(messages, message)
	}
	state.Messages = messages
}

func projectEinoAssistantReplaceMutationProgressInstruction(
	state *adk.ChatModelAgentState,
	instruction string,
) {
	if state == nil {
		return
	}
	projectEinoAssistantRemoveMutationProgressInstruction(state)
	state.Messages = append(
		state.Messages,
		schema.SystemMessage(projectEinoAssistantMutationProgressPrefix+instruction),
	)
}

func projectEinoAssistantRemoveMutationProgressInstruction(state *adk.ChatModelAgentState) {
	if state == nil {
		return
	}
	messages := state.Messages[:0]
	for _, message := range state.Messages {
		if message != nil &&
			message.Role == schema.System &&
			strings.HasPrefix(message.Content, projectEinoAssistantMutationProgressPrefix) {
			continue
		}
		messages = append(messages, message)
	}
	state.Messages = messages
}

func projectEinoAssistantReplaceCommitProgressInstruction(
	state *adk.ChatModelAgentState,
	instruction string,
) {
	if state == nil {
		return
	}
	projectEinoAssistantRemoveCommitProgressInstruction(state)
	state.Messages = append(
		state.Messages,
		schema.SystemMessage(projectEinoAssistantCommitProgressPrefix+instruction),
	)
}

func projectEinoAssistantRemoveCommitProgressInstruction(state *adk.ChatModelAgentState) {
	if state == nil {
		return
	}
	messages := state.Messages[:0]
	for _, message := range state.Messages {
		if message != nil &&
			message.Role == schema.System &&
			strings.HasPrefix(message.Content, projectEinoAssistantCommitProgressPrefix) {
			continue
		}
		messages = append(messages, message)
	}
	state.Messages = messages
}

func projectEinoAssistantPendingPermissionResult(content string) bool {
	return strings.HasPrefix(
		strings.ToLower(strings.TrimSpace(content)),
		"tool call skipped: waiting for approval",
	)
}

func projectEinoAssistantPhaseProgressInstruction(
	phase projectEinoAssistantPhase,
	completedReadPaths []string,
) string {
	switch phase {
	case projectEinoAssistantPhasePlan:
		return "Define the initial execution plan now with define_initial_project_plan. This is an internal, auto-authorized planning step; do not ask the user to approve it."
	case projectEinoAssistantPhaseApproval:
		if len(completedReadPaths) == 0 {
			return "Complete one bounded inspection batch for the current implementation task. Read known relevant files directly and batch independent reads. Then request one complete source-edit plan, ask the user for missing information, or report a concrete blocker."
		}
		const maxListedPaths = 12
		listedPaths := completedReadPaths
		if len(listedPaths) > maxListedPaths {
			listedPaths = listedPaths[:maxListedPaths]
		}
		encodedPaths, _ := json.Marshal(listedPaths)
		evidence := string(encodedPaths)
		if len(completedReadPaths) > len(listedPaths) {
			evidence += fmt.Sprintf(" and %d more", len(completedReadPaths)-len(listedPaths))
		}
		return "The initial source-inspection batch is complete. Already-read project file paths for the current workspace revision: " + evidence + ". Workspace discovery and search tools are now unavailable in plan approval. Reuse this evidence and call request_project_plan_approval now with complete target paths and acceptance criteria. A direct read_file remains available only for a concrete dependency path discovered from existing evidence; otherwise ask for specifically missing information or report a concrete blocker instead of searching again."
	case projectEinoAssistantPhaseMutate:
		return "A source-edit plan is already approved. Apply the approved workspace mutation now. Continue the edit batch until the change is coherent, then call verify_development_runtime or finish so App Studio can verify it. Ask the user only if required information is missing."
	case projectEinoAssistantPhaseRepair:
		return "Use the latest verification failure to make a targeted repair batch, then call verify_development_runtime or finish so App Studio can verify the new revision. Do not restart broad project inspection."
	default:
		return "Complete the current task phase now or report a concrete blocker."
	}
}

func projectEinoAssistantMutationProgressInstruction(
	approvedPlan *projectAssistantApprovedPlan,
	sourceRevision uint64,
	verifiedRevision uint64,
	awaitingFirstMutation bool,
	completedReadPaths []string,
	targetReadsPending bool,
	missingMutationPaths []string,
) string {
	targets := "the approved target-path grant"
	if approvedPlan != nil && len(approvedPlan.TargetPaths) > 0 {
		const maxListedPaths = 20
		listedPaths := approvedPlan.TargetPaths
		if len(listedPaths) > maxListedPaths {
			listedPaths = listedPaths[:maxListedPaths]
		}
		encodedPaths, _ := json.Marshal(listedPaths)
		targets = string(encodedPaths)
		if len(approvedPlan.TargetPaths) > len(listedPaths) {
			targets += fmt.Sprintf(" and %d more", len(approvedPlan.TargetPaths)-len(listedPaths))
		}
	}
	knownExisting := projectEinoAssistantKnownExistingApprovedTargets(approvedPlan, completedReadPaths)
	existingInstruction := ""
	if len(knownExisting) > 0 {
		encodedPaths, _ := json.Marshal(knownExisting)
		existingInstruction = " Known existing approved files: " + string(encodedPaths) +
			". Never use write_file for these paths; use small exact apply_patch replacements."
	}
	if awaitingFirstMutation {
		editInstruction := "Apply the approved workspace mutation now with write_file for a genuinely new path, apply_patch for an existing file, or mkdir."
		readInstruction := " Further workspace discovery and search are unavailable."
		if projectEinoAssistantAllApprovedTargetsAreKnownExisting(approvedPlan, completedReadPaths) {
			if targetReadsPending {
				editInstruction = "Use read_file now on the available approved existing target files whose exact current content you need, then apply the approved workspace mutation with apply_patch. Do not ask the user to paste file contents."
				readInstruction = " Only unread exact approved-target reads are available; workspace discovery and search remain unavailable."
			} else {
				editInstruction = "Apply the approved workspace mutation now with apply_patch using the exact current content already returned. Do not reread files or ask the user to paste file contents."
			}
		}
		return "Approved source target paths: " + targets + "." + existingInstruction + " " +
			editInstruction +
			" Verification, runtime inspection, repository actions, and replanning are unavailable until a successful source mutation advances the workspace revision." +
			readInstruction +
			" Use write_todos only to update progress, or ask_follow_up only when required implementation information cannot be obtained from the approved target files."
	}
	instruction := fmt.Sprintf(
		"Approved source target paths: %s.%s Workspace source revision %d is dirty and verified revision is %d. Continue the approved edit batch until the change is coherent, then call verify_development_runtime. Do not replan or broaden the objective.",
		targets,
		existingInstruction,
		sourceRevision,
		verifiedRevision,
	)
	if len(missingMutationPaths) > 0 {
		instruction += " Runtime verification is unavailable until these approved target files are successfully mutated: " +
			strings.Join(missingMutationPaths, ", ") + "."
	}
	return instruction
}

func projectEinoAssistantUnreadKnownExistingApprovedTargets(
	approvedPlan *projectAssistantApprovedPlan,
	observedReadPaths []string,
	completedReadPaths []string,
) []string {
	knownExisting := projectEinoAssistantKnownExistingApprovedTargets(approvedPlan, observedReadPaths)
	if len(knownExisting) == 0 || len(completedReadPaths) == 0 {
		return knownExisting
	}
	completed := make(map[string]struct{}, len(completedReadPaths))
	for _, path := range completedReadPaths {
		clean, err := workspace.CleanProjectPath(path)
		if err == nil {
			completed[clean] = struct{}{}
		}
	}
	unread := make([]string, 0, len(knownExisting))
	for _, path := range knownExisting {
		if _, ok := completed[path]; !ok {
			unread = append(unread, path)
		}
	}
	return unread
}

func projectEinoAssistantMissingKnownExistingMutationTargets(
	approvedPlan *projectAssistantApprovedPlan,
	observedReadPaths []string,
	successfulMutationPaths []string,
) []string {
	if !projectEinoAssistantAllApprovedTargetsAreKnownExisting(approvedPlan, observedReadPaths) {
		return nil
	}
	mutated := make(map[string]struct{}, len(successfulMutationPaths))
	for _, path := range successfulMutationPaths {
		clean, err := workspace.CleanProjectPath(path)
		if err == nil {
			mutated[clean] = struct{}{}
		}
	}
	missing := make([]string, 0, len(approvedPlan.TargetPaths))
	for _, path := range approvedPlan.TargetPaths {
		clean, err := workspace.CleanProjectPath(path)
		if err != nil {
			continue
		}
		if _, ok := mutated[clean]; !ok {
			missing = append(missing, clean)
		}
	}
	return missing
}

func projectEinoAssistantKnownExistingApprovedTargets(
	approvedPlan *projectAssistantApprovedPlan,
	completedReadPaths []string,
) []string {
	if approvedPlan == nil || len(approvedPlan.TargetPaths) == 0 || len(completedReadPaths) == 0 {
		return nil
	}
	completed := make(map[string]struct{}, len(completedReadPaths))
	for _, path := range completedReadPaths {
		clean, err := workspace.CleanProjectPath(path)
		if err == nil {
			completed[clean] = struct{}{}
		}
	}
	known := make([]string, 0, len(approvedPlan.TargetPaths))
	for _, path := range approvedPlan.TargetPaths {
		if strings.HasSuffix(strings.TrimSpace(path), "/") {
			continue
		}
		clean, err := workspace.CleanProjectPath(path)
		if err != nil {
			continue
		}
		if _, ok := completed[clean]; ok {
			known = append(known, clean)
		}
	}
	return known
}

func projectEinoAssistantAllApprovedTargetsAreKnownExisting(
	approvedPlan *projectAssistantApprovedPlan,
	completedReadPaths []string,
) bool {
	if approvedPlan == nil || len(approvedPlan.TargetPaths) == 0 {
		return false
	}
	for _, path := range approvedPlan.TargetPaths {
		if strings.HasSuffix(strings.TrimSpace(path), "/") {
			return false
		}
	}
	return len(projectEinoAssistantKnownExistingApprovedTargets(approvedPlan, completedReadPaths)) ==
		len(approvedPlan.TargetPaths)
}

func projectEinoAssistantLatestEditRecoveryInstruction(state *adk.ChatModelAgentState) string {
	if state == nil {
		return ""
	}
	for index := len(state.Messages) - 1; index >= 0; index-- {
		message := state.Messages[index]
		if message == nil || message.Role != schema.Tool {
			continue
		}
		name := projectToolBaseName(message.ToolName)
		content := strings.ToLower(strings.TrimSpace(message.Content))
		if !strings.HasPrefix(content, "tool call failed:") {
			continue
		}
		switch {
		case name == projectToolWriteFile && strings.Contains(content, "create-only"):
			return "The previous write_file failed because the target is an existing file. Do not retry write_file for that path and do not verify yet. Use apply_patch with a small exact replacement."
		case name == projectToolApplyPatch && strings.Contains(content, "oldtext was not found"):
			return "The previous apply_patch failed because its exact oldText was not found. Do not switch to write_file and do not verify yet. Reread the relevant target section, copy a small unique exact anchor, and retry apply_patch."
		case name == projectToolApplyPatch && strings.Contains(content, "oldtext matched"):
			return "The previous apply_patch matched more than once. Do not switch to write_file and do not verify yet. Add surrounding context so oldText is unique, then retry apply_patch; use replaceAll only when every match should change."
		case name == projectToolApplyPatch && strings.Contains(content, "patch made no changes"):
			return "The previous apply_patch made no changes. Do not verify yet. Choose newText that actually implements the requested behavior and differs from oldText, then retry apply_patch."
		case name == projectToolApplyPatch:
			return "The previous apply_patch failed. Do not switch to write_file and do not verify yet. Use the failure details to make one smaller exact localized patch."
		}
		return ""
	}
	return ""
}

func projectEinoAssistantLatestFailedPatchPath(
	state *adk.ChatModelAgentState,
	approvedPlan *projectAssistantApprovedPlan,
) string {
	if state == nil || approvedPlan == nil {
		return ""
	}
	toolCallID := ""
	for index := len(state.Messages) - 1; index >= 0; index-- {
		message := state.Messages[index]
		if message == nil || message.Role != schema.Tool {
			continue
		}
		if projectToolBaseName(message.ToolName) != projectToolApplyPatch ||
			!strings.HasPrefix(strings.ToLower(strings.TrimSpace(message.Content)), "tool call failed:") {
			return ""
		}
		toolCallID = strings.TrimSpace(message.ToolCallID)
		break
	}
	if toolCallID == "" {
		return ""
	}
	for index := len(state.Messages) - 1; index >= 0; index-- {
		message := state.Messages[index]
		if message == nil || message.Role != schema.Assistant {
			continue
		}
		for _, call := range message.ToolCalls {
			if strings.TrimSpace(call.ID) != toolCallID ||
				projectToolBaseName(call.Function.Name) != projectToolApplyPatch {
				continue
			}
			arguments, err := projectEinoToolArguments(call.Function.Arguments)
			if err != nil || !projectAssistantApprovedPlanAllowsWrite(
				approvedPlan,
				projectToolApplyPatch,
				arguments,
			) {
				return ""
			}
			clean, err := workspace.CleanProjectPath(projectToolString(arguments["path"]))
			if err != nil {
				return ""
			}
			return clean
		}
	}
	return ""
}

func projectEinoAssistantAwaitingFirstApprovedMutation(
	runState *projectEinoAssistantRunState,
	approvedPlan *projectAssistantApprovedPlan,
) bool {
	if runState == nil || approvedPlan == nil {
		return false
	}
	sourceRevision, verifiedRevision := runState.SourceMutationRevisions()
	return sourceRevision == verifiedRevision
}

func (m *projectEinoAssistantPhaseFilterMiddleware) WrapInvokableToolCall(
	_ context.Context,
	endpoint adk.InvokableToolCallEndpoint,
	toolCtx *adk.ToolContext,
) (adk.InvokableToolCallEndpoint, error) {
	if toolCtx == nil {
		return endpoint, nil
	}
	rawName := toolCtx.Name
	name := projectToolBaseName(rawName)
	if !projectEinoAssistantPhaseLifecycleApplies(m.req, m.runState) {
		templateBootstrapAllowed := projectEinoAssistantPhaseTemplateBootstrapAllowed(m.req.Project)
		tool := m.toolInfoForInvocation(rawName)
		risk, bundle, hasMetadata := projectEinoAssistantPhaseToolMetadata(tool)
		rawName = strings.TrimSpace(rawName)
		templateInspection := projectEinoAssistantPhaseTemplateInspectionTool(
			rawName,
			risk,
			bundle,
			templateBootstrapAllowed,
		)
		operationalRead := projectEinoAssistantPhaseOperationalReadTool(rawName, risk, bundle)
		templateToolDenied := name == projectToolSelectTemplate ||
			(name == projectToolInspectDevelopmentTemplates && !templateInspection)
		operationalReadDenied := projectEinoAssistantPhaseReservedOperationalReadName(rawName) &&
			!operationalRead
		if !templateToolDenied &&
			!operationalReadDenied &&
			!projectEinoAssistantPhaseReservedDirectActionName(rawName) &&
			(!hasMetadata || risk != projectAssistantToolRiskRuntime) {
			return endpoint, nil
		}
		return func(context.Context, string, ...einotool.Option) (string, error) {
			return fmt.Sprintf("Tool call denied: %s is unavailable in the current assistant phase", name), nil
		}, nil
	}
	return func(ctx context.Context, argumentsInJSON string, opts ...einotool.Option) (string, error) {
		tool := m.toolInfoForInvocation(rawName)
		approvedPlan := projectEinoAssistantPhaseApprovedPlan(m.req, m.runState)
		if approvedPlan == nil {
			approvedPlan = m.approvedPlan
		}
		templateBootstrapAllowed := projectEinoAssistantPhaseTemplateBootstrapAllowed(m.req.Project)
		if name == projectEinoAssistantToolSearchTool &&
			projectAssistantInlinePromotionEnabled(m.req) &&
			m.phase == projectEinoAssistantPhaseApproval {
			m.recordNoProgressAction(name, argumentsInJSON)
			return fmt.Sprintf("Tool call denied: %s is unavailable in the current assistant phase", name), nil
		}
		recoveryReadAllowed := projectEinoAssistantRecoveryReadAllowed(
			name,
			argumentsInJSON,
			m.recoveryReadPaths,
		)
		if m.recoveryPatchPath != "" && !m.recoveryReadComplete && !recoveryReadAllowed {
			m.recordNoProgressAction(name, argumentsInJSON)
			return fmt.Sprintf(
				"Tool call denied: reread %q before any other action",
				m.recoveryPatchPath,
			), nil
		}
		if m.recoveryPatchPath != "" && m.recoveryReadComplete &&
			!projectEinoAssistantPatchTargetsPath(name, argumentsInJSON, m.recoveryPatchPath) {
			m.recordNoProgressAction(name, argumentsInJSON)
			return fmt.Sprintf(
				"Tool call denied: retry apply_patch for %q before any other action",
				m.recoveryPatchPath,
			), nil
		}
		if m.mustApplyPatch && name != projectToolApplyPatch {
			m.recordNoProgressAction(name, argumentsInJSON)
			return "Tool call denied: apply the approved existing-file change with apply_patch now", nil
		}
		if name == projectToolVerifyDevelopmentRuntime && len(m.missingMutationPaths) > 0 {
			m.recordNoProgressAction(name, argumentsInJSON)
			return fmt.Sprintf(
				"Tool call denied: complete the approved source edits for %s before runtime verification",
				strings.Join(m.missingMutationPaths, ", "),
			), nil
		}
		if !projectEinoAssistantPhaseAllowsTool(
			m.phase,
			approvedPlan,
			templateBootstrapAllowed,
			tool,
			projectAssistantInlinePromotionEnabled(m.req),
			normalizeProjectAssistantTurnPolicy(m.req.TurnPolicy, m.req.TurnProfile),
		) && !recoveryReadAllowed {
			m.recordNoProgressAction(name, argumentsInJSON)
			return fmt.Sprintf("Tool call denied: %s is unavailable in the current assistant phase", name), nil
		}
		if m.phase == projectEinoAssistantPhaseApproval &&
			m.approvalSearchClosed &&
			projectEinoAssistantPhaseApprovalSearchTool(tool) {
			m.recordNoProgressAction(name, argumentsInJSON)
			return fmt.Sprintf("Tool call denied: %s is unavailable after the initial approval inspection batch", name), nil
		}
		if m.phase == projectEinoAssistantPhaseMutate &&
			projectEinoAssistantAwaitingFirstApprovedMutation(m.runState, approvedPlan) &&
			!projectEinoAssistantPhasePreMutationToolAllowed(tool, templateBootstrapAllowed) &&
			!recoveryReadAllowed {
			m.recordNoProgressAction(name, argumentsInJSON)
			return fmt.Sprintf("Tool call denied: %s is unavailable until an approved source mutation succeeds", name), nil
		}
		callID := strings.TrimSpace(toolCtx.CallID)
		if name == projectToolVerifyDevelopmentRuntime {
			if callID == "" {
				callID = "call-" + uuid.NewString()
			}
			m.emitToolCall(projectToolCallStreamEvent{
				ID:        callID,
				Name:      name,
				Status:    "running",
				Arguments: strings.TrimSpace(argumentsInJSON),
			})
			if m.req.StreamCallbacks.OnStatus != nil {
				m.req.StreamCallbacks.OnStatus("Verifying development runtime")
			}
		}
		result, err := endpoint(ctx, argumentsInJSON, opts...)
		if err != nil {
			if m.runState != nil && !projectEinoAssistantFilesystemReadTool(name) {
				arguments := strings.TrimSpace(argumentsInJSON)
				if parsed, parseErr := projectEinoToolArguments(arguments); parseErr == nil {
					arguments = projectEinoToolArgumentsString(parsed)
				}
				m.runState.RecordCompletedAction(name, arguments, false)
				if name == projectToolApplyPatch {
					m.runState.RecordPatchResult(false)
					if parsed, parseErr := projectEinoToolArguments(arguments); parseErr == nil {
						path := projectToolString(parsed["path"])
						m.runState.ReopenReadFile(path)
						m.runState.StartPatchRecovery(path)
					}
				}
			}
			if name == projectToolVerifyDevelopmentRuntime {
				m.emitToolCall(projectToolCallStreamEvent{
					ID:        callID,
					Name:      name,
					Status:    "failed",
					Arguments: strings.TrimSpace(argumentsInJSON),
					Error:     projectEinoAssistantSafeErrorText(err),
				})
			}
			return result, err
		}
		if name == projectToolVerifyDevelopmentRuntime {
			m.emitToolCall(projectToolCallStreamEvent{
				ID:        callID,
				Name:      name,
				Status:    "succeeded",
				Arguments: strings.TrimSpace(argumentsInJSON),
				Summary:   projectEinoAssistantVerificationToolSummary(result),
			})
			if m.req.StreamCallbacks.OnStatus != nil {
				m.req.StreamCallbacks.OnStatus("Reviewing verification results")
			}
		}
		if m.runState != nil &&
			!projectEinoAssistantFilesystemReadTool(name) &&
			!projectEinoAssistantPendingPermissionResult(result) {
			arguments := strings.TrimSpace(argumentsInJSON)
			parsed, parseErr := projectEinoToolArguments(arguments)
			if parseErr == nil {
				arguments = projectEinoToolArgumentsString(parsed)
			}
			successful := projectEinoAssistantPhaseSuccessfulToolContent(result)
			if name == projectToolApplyPatch && !successful && parseErr == nil {
				path := projectToolString(parsed["path"])
				m.runState.ReopenReadFile(path)
				m.runState.StartPatchRecovery(path)
			}
			if name == projectToolApplyPatch {
				m.runState.RecordPatchResult(successful)
			}
			if name == projectToolApplyPatch && successful && parseErr == nil {
				path := projectToolString(parsed["path"])
				m.runState.RecordSuccessfulMutationPath(path)
				m.runState.ClearPatchRecovery(path)
			}
			m.runState.RecordCompletedAction(
				name,
				arguments,
				successful,
			)
			if projectEinoAssistantCommitTool(name) &&
				successful {
				m.runState.CompleteExecutionPlan()
			}
		}
		if name == projectEinoAssistantWriteTodosTool {
			plan, status := projectEinoAssistantTodoProgress(
				argumentsInJSON,
				!projectAssistantToolDisclosureMinimal,
			)
			internalPlan, _ := projectEinoAssistantTodoProgress(argumentsInJSON, true)
			if status != "" && m.req.StreamCallbacks.OnStatus != nil {
				m.req.StreamCallbacks.OnStatus(status)
			}
			if len(plan.Steps) > 0 && m.req.StreamCallbacks.OnPlan != nil {
				m.req.StreamCallbacks.OnPlan(plan)
			}
			if len(internalPlan.Steps) > 0 && m.runState != nil {
				m.runState.SetPlanProgress(internalPlan)
			}
		}
		return result, nil
	}, nil
}

func projectEinoAssistantRecoveryReadAllowed(name, argumentsInJSON string, recoveryReadPaths []string) bool {
	if projectToolBaseName(name) != projectToolReadFile || len(recoveryReadPaths) == 0 {
		return false
	}
	arguments, err := projectEinoToolArguments(argumentsInJSON)
	if err != nil {
		return false
	}
	readPath := projectToolString(arguments["file_path"])
	if readPath == "" {
		readPath = projectToolString(arguments["path"])
	}
	clean, err := workspace.CleanProjectPath(readPath)
	return err == nil && slices.Contains(recoveryReadPaths, clean)
}

func projectEinoAssistantPatchTargetsPath(name, argumentsInJSON, target string) bool {
	if projectToolBaseName(name) != projectToolApplyPatch {
		return false
	}
	arguments, err := projectEinoToolArguments(argumentsInJSON)
	if err != nil {
		return false
	}
	path, err := workspace.CleanProjectPath(projectToolString(arguments["path"]))
	return err == nil && path == target
}

func (m *projectEinoAssistantPhaseFilterMiddleware) recordNoProgressAction(name, arguments string) {
	if m == nil || m.runState == nil {
		return
	}
	arguments = strings.TrimSpace(arguments)
	if parsed, err := projectEinoToolArguments(arguments); err == nil {
		arguments = projectEinoToolArgumentsString(parsed)
	}
	m.runState.RecordCompletedAction(name, arguments, false)
}

func (m *projectEinoAssistantPhaseFilterMiddleware) emitToolCall(event projectToolCallStreamEvent) {
	if m == nil {
		return
	}
	if m.runState == nil {
		if m.req.StreamCallbacks.OnToolCall != nil {
			m.req.StreamCallbacks.OnToolCall(event)
		}
		return
	}
	m.runState.EmitToolCall(m.req.StreamCallbacks.OnToolCall, event)
}

func projectEinoAssistantVerificationToolSummary(result string) string {
	var verification projectAssistantRuntimeVerificationResult
	if json.Unmarshal([]byte(result), &verification) != nil {
		return "Development runtime verification completed."
	}
	status := strings.ToLower(strings.TrimSpace(verification.Status))
	if status == "" {
		return "Development runtime verification completed."
	}
	if verification.CheckedMutationRevision > 0 {
		return fmt.Sprintf(
			"Verification status %s for workspace revision %d.",
			status,
			verification.CheckedMutationRevision,
		)
	}
	return fmt.Sprintf("Verification status %s.", status)
}

func projectEinoAssistantTodoProgress(argumentsInJSON string, includeLabels bool) (projectAssistantPlanSnapshot, string) {
	if len(argumentsInJSON) == 0 || len(argumentsInJSON) > projectEinoAssistantTodoProgressMaxInputBytes {
		return projectAssistantPlanSnapshot{}, ""
	}
	var input projectEinoAssistantTodoProgressInput
	if err := json.Unmarshal([]byte(argumentsInJSON), &input); err != nil ||
		len(input.Todos) == 0 ||
		len(input.Todos) > projectEinoAssistantTodoProgressMaxItems {
		return projectAssistantPlanSnapshot{}, ""
	}

	completed := 0
	active := ""
	activeCount := 0
	planValid := true
	plan := projectAssistantPlanSnapshot{Steps: make([]projectAssistantPlanStep, 0, len(input.Todos))}
	for _, todo := range input.Todos {
		content := projectEinoAssistantTodoProgressLabel(todo.Content)
		activeForm := projectEinoAssistantTodoProgressLabel(todo.ActiveForm)
		if content == "" {
			planValid = false
		}
		switch todo.Status {
		case "pending":
		case "in_progress":
			activeCount++
			if activeCount > 1 {
				return projectAssistantPlanSnapshot{}, ""
			}
			active = activeForm
			if active == "" {
				active = content
			}
		case "completed":
			completed++
		default:
			return projectAssistantPlanSnapshot{}, ""
		}
		if includeLabels {
			plan.Steps = append(plan.Steps, projectAssistantPlanStep{
				Content:    content,
				ActiveForm: activeForm,
				Status:     todo.Status,
			})
		}
	}
	if !planValid {
		return projectAssistantPlanSnapshot{}, ""
	}

	total := len(input.Todos)
	noun := "steps"
	if total == 1 {
		noun = "step"
	}
	count := fmt.Sprintf("%d of %d %s", completed, total, noun)
	if includeLabels && active != "" {
		return plan, active + " · " + count
	}
	if includeLabels {
		return plan, count + " complete"
	}
	return projectAssistantPlanSnapshot{}, count + " complete"
}

func projectEinoAssistantTodoProgressLabel(value string) string {
	value = strings.Join(strings.Fields(value), " ")
	value = projectEinoAssistantSafeText(value)
	if len(value) <= projectEinoAssistantTodoProgressMaxLabelBytes {
		return value
	}
	end := projectEinoAssistantTodoProgressMaxLabelBytes - 3
	for end > 0 && !utf8.ValidString(value[:end]) {
		end--
	}
	return strings.TrimSpace(value[:end]) + "..."
}

func (m *projectEinoAssistantPhaseFilterMiddleware) toolInfoForInvocation(rawName string) *schema.ToolInfo {
	for _, tools := range [][]*schema.ToolInfo{m.toolInfos, m.deferredToolInfos} {
		for _, info := range tools {
			if info != nil && strings.TrimSpace(info.Name) == strings.TrimSpace(rawName) {
				return info
			}
		}
	}

	tool := &schema.ToolInfo{Name: rawName}
	switch projectToolBaseName(rawName) {
	case projectToolDefineInitialProjectPlan:
		tool.Extra = map[string]any{
			"bundle": string(projectAssistantToolBundleCollaboration),
			"risk":   string(projectAssistantToolRiskPlan),
		}
	case projectToolRequestProjectPlanApproval:
		tool.Extra = map[string]any{
			"bundle": string(projectAssistantToolBundleCollaboration),
			"risk":   string(projectAssistantToolRiskPlan),
		}
	case projectToolSelectTemplate:
		tool.Extra = map[string]any{
			"bundle": string(projectAssistantToolBundleWorkflow),
			"risk":   string(projectAssistantToolRiskWrite),
		}
	case projectToolCommitProjectFiles, projectToolCommitFiles:
		tool.Extra = map[string]any{
			"bundle": string(projectAssistantToolBundleRepo),
			"risk":   string(projectAssistantToolRiskCommit),
		}
	}
	return tool
}

func projectEinoAssistantPhaseLifecycleApplies(req projectAssistantRunRequest, runState *projectEinoAssistantRunState) bool {
	if projectAssistantInlinePromotionEnabled(req) {
		return true
	}
	if runState != nil && projectAssistantTurnProfileAllowsMutation(runState.TurnPolicy().profile) {
		return true
	}
	profile := req.TurnPolicy.profile
	if strings.TrimSpace(string(profile)) == "" {
		profile = req.TurnProfile
	}
	if strings.TrimSpace(string(profile)) == "" {
		// Phase-helper tests and callers outside the engine historically omit
		// turn policy. Keep lifecycle control enabled for that legacy shape;
		// the engine always supplies a normalized profile.
		return true
	}
	return projectAssistantTurnProfileAllowsMutation(profile)
}

func projectEinoAssistantPhaseVisibleTools(canonical, current []*schema.ToolInfo) []*schema.ToolInfo {
	visibleNames := make(map[string]struct{}, len(current))
	for _, tool := range current {
		if tool != nil {
			visibleNames[projectAssistantToolKey(tool.Name)] = struct{}{}
		}
	}
	visible := make([]*schema.ToolInfo, 0, len(canonical))
	for _, tool := range canonical {
		if tool == nil {
			continue
		}
		if !projectEinoAssistantPhaseSearchableTool(tool) {
			visible = append(visible, tool)
			continue
		}
		if _, selected := visibleNames[projectAssistantToolKey(tool.Name)]; selected {
			visible = append(visible, tool)
		}
	}
	return visible
}

func projectEinoAssistantPhaseFilterReadOnlyTools(
	templateBootstrapAllowed bool,
	tools []*schema.ToolInfo,
) []*schema.ToolInfo {
	filtered := make([]*schema.ToolInfo, 0, len(tools))
	for _, tool := range tools {
		if tool == nil {
			continue
		}
		risk, bundle, hasMetadata := projectEinoAssistantPhaseToolMetadata(tool)
		if projectEinoAssistantPhaseReservedDirectActionName(tool.Name) ||
			(hasMetadata && risk == projectAssistantToolRiskRuntime) {
			continue
		}
		if projectEinoAssistantPhaseReservedOperationalReadName(tool.Name) &&
			!projectEinoAssistantPhaseOperationalReadTool(tool.Name, risk, bundle) {
			continue
		}
		rawName := strings.TrimSpace(tool.Name)
		switch projectToolBaseName(rawName) {
		case projectToolSelectTemplate:
			continue
		case projectToolInspectDevelopmentTemplates:
			if !projectEinoAssistantPhaseTemplateInspectionTool(
				rawName,
				risk,
				bundle,
				templateBootstrapAllowed,
			) {
				continue
			}
		}
		filtered = append(filtered, tool)
	}
	return filtered
}

func projectEinoAssistantPhaseSearchableTool(tool *schema.ToolInfo) bool {
	if tool == nil || tool.Extra == nil {
		return false
	}
	searchable, _ := tool.Extra[projectEinoToolSearchableExtraKey].(bool)
	return searchable
}

func projectEinoAssistantPhaseForState(
	req projectAssistantRunRequest,
	runState *projectEinoAssistantRunState,
	state *adk.ChatModelAgentState,
) projectEinoAssistantPhase {
	approvedPlan := projectEinoAssistantPhaseApprovedPlan(req, runState)
	initialCreation := req.InitialApprovedPlan != nil || (approvedPlan != nil && approvedPlan.RunLocal)
	initialPlanningRequired := initialCreation && approvedPlan != nil &&
		strings.TrimSpace(approvedPlan.Goal) != ""
	if initialPlanningRequired {
		executionPlan, _ := runState.ExecutionPlan()
		if executionPlan == nil {
			return projectEinoAssistantPhasePlan
		}
	}
	history := projectEinoAssistantPhaseHistoryForState(state)
	phase := projectEinoAssistantPhaseForHistory(
		history,
		approvedPlan != nil,
		initialCreation,
	)
	sourceRevision, _ := runState.SourceMutationRevisions()
	if sourceRevision > 0 &&
		history.latestWrite >= 0 &&
		history.latestVerification > history.latestWrite &&
		history.verificationReady &&
		(runState == nil || !runState.SourceMutationVerified()) {
		phase = projectEinoAssistantPhaseRepair
	}
	if initialPlanningRequired && phase == projectEinoAssistantPhaseReport && !runState.ExecutionPlanComplete() {
		return projectEinoAssistantPhaseMutate
	}
	return phase
}

func projectEinoAssistantPhaseForStateWithApproval(
	req projectAssistantRunRequest,
	runState *projectEinoAssistantRunState,
	state *adk.ChatModelAgentState,
	approved bool,
) projectEinoAssistantPhase {
	history := projectEinoAssistantPhaseHistoryForState(state)
	phase := projectEinoAssistantPhaseForHistory(
		history,
		approved,
		req.InitialApprovedPlan != nil,
	)
	sourceRevision, _ := runState.SourceMutationRevisions()
	if sourceRevision > 0 &&
		history.latestWrite >= 0 &&
		history.latestVerification > history.latestWrite &&
		history.verificationReady &&
		(runState == nil || !runState.SourceMutationVerified()) {
		return projectEinoAssistantPhaseRepair
	}
	return phase
}

type projectEinoAssistantPhaseHistory struct {
	latestWrite             int
	latestDeniedWrite       int
	latestTemplateBootstrap int
	latestDirectAction      int
	latestVerification      int
	latestCommit            int
	verificationReady       bool
	verificationDisposition projectEinoAssistantVerificationDisposition
}

func projectEinoAssistantPhaseHistoryForState(state *adk.ChatModelAgentState) projectEinoAssistantPhaseHistory {
	history := projectEinoAssistantPhaseHistory{
		latestWrite:             -1,
		latestDeniedWrite:       -1,
		latestTemplateBootstrap: -1,
		latestDirectAction:      -1,
		latestVerification:      -1,
		latestCommit:            -1,
	}
	if state != nil {
		for index, message := range state.Messages {
			if message == nil {
				continue
			}
			rawName := strings.TrimSpace(message.ToolName)
			name := projectToolBaseName(message.ToolName)
			content := strings.ToLower(strings.TrimSpace(message.Content))
			if rawName == projectToolSelectTemplate {
				if projectEinoAssistantPhasePermissionDenied(content) {
					history.latestDeniedWrite = index
				} else if projectEinoAssistantPhaseSuccessfulToolResult(message) {
					history.latestTemplateBootstrap = index
				}
				continue
			}
			if projectEinoAssistantPhaseDirectActionName(rawName) {
				if projectEinoAssistantPhaseSuccessfulToolResult(message) ||
					projectEinoAssistantPhasePermissionDenied(content) {
					history.latestDirectAction = index
				}
				continue
			}
			if projectEinoAssistantPhaseCanonicalEditTool(name) &&
				projectEinoAssistantPhasePermissionDenied(content) {
				history.latestDeniedWrite = index
				continue
			}
			if name == projectToolVerifyDevelopmentRuntime && !strings.HasPrefix(strings.ToLower(strings.TrimSpace(message.Content)), "tool call skipped: waiting for approval") {
				history.latestVerification = index
				history.verificationReady = false
				history.verificationDisposition = projectEinoAssistantVerificationBlocked
				if projectEinoAssistantPhaseSuccessfulToolResult(message) {
					history.verificationDisposition = projectEinoAssistantPhaseVerificationDisposition(message.Content)
					history.verificationReady = history.verificationDisposition == projectEinoAssistantVerificationReadyDisposition
				}
				continue
			}
			if !projectEinoAssistantPhaseSuccessfulToolResult(message) {
				continue
			}
			switch name {
			case projectToolWriteFile, projectToolApplyPatch, projectToolMkdir:
				history.latestWrite = index
			case projectToolCommitProjectFiles, projectToolCommitFiles:
				history.latestCommit = index
			}
		}
	}
	return history
}

func projectEinoAssistantPhasePermissionDenied(content string) bool {
	content = strings.ToLower(strings.TrimSpace(content))
	return strings.HasPrefix(content, "tool call failed: permission denied:")
}

func projectEinoAssistantPhaseForHistory(
	history projectEinoAssistantPhaseHistory,
	approved bool,
	initialCreation bool,
) projectEinoAssistantPhase {
	// A completed commit is terminal even though the tool execution clears the
	// run-local approval grant before the next model call.
	if history.latestCommit > history.latestWrite {
		return projectEinoAssistantPhaseReport
	}
	if history.latestWrite < 0 && history.latestDeniedWrite >= 0 {
		return projectEinoAssistantPhaseReport
	}
	if history.latestWrite < 0 && history.latestTemplateBootstrap >= 0 {
		if approved && initialCreation {
			return projectEinoAssistantPhaseMutate
		}
		return projectEinoAssistantPhaseReport
	}
	if history.latestWrite < 0 && history.latestDirectAction >= 0 {
		return projectEinoAssistantPhaseReport
	}
	if !approved {
		return projectEinoAssistantPhaseApproval
	}
	if history.latestWrite < 0 {
		return projectEinoAssistantPhaseMutate
	}
	if history.latestVerification < 0 {
		return projectEinoAssistantPhaseMutate
	}
	if history.latestVerification > history.latestWrite {
		if !history.verificationReady {
			if history.verificationDisposition == projectEinoAssistantVerificationOperational {
				return projectEinoAssistantPhaseWarmup
			}
			if history.verificationDisposition == projectEinoAssistantVerificationBlocked {
				return projectEinoAssistantPhaseReport
			}
			return projectEinoAssistantPhaseRepair
		}
		if initialCreation {
			return projectEinoAssistantPhaseReport
		}
		return projectEinoAssistantPhaseCommit
	}
	if !history.verificationReady {
		return projectEinoAssistantPhaseRepair
	}
	return projectEinoAssistantPhaseMutate
}

func projectEinoAssistantCommitTool(name string) bool {
	switch projectToolBaseName(name) {
	case projectToolCommitProjectFiles, projectToolCommitFiles:
		return true
	default:
		return false
	}
}

func projectEinoAssistantPhaseApprovedPlan(
	req projectAssistantRunRequest,
	runState *projectEinoAssistantRunState,
) *projectAssistantApprovedPlan {
	if approvedPlan := runState.ApprovedPlan(); approvedPlan != nil {
		return approvedPlan
	}
	return req.InitialApprovedPlan
}

func projectEinoAssistantPhaseSuccessfulToolResult(message *schema.Message) bool {
	if message == nil || message.Role != schema.Tool || strings.TrimSpace(message.ToolName) == "" {
		return false
	}
	return projectEinoAssistantPhaseSuccessfulToolContent(message.Content)
}

func projectEinoAssistantPhaseSuccessfulToolContent(content string) bool {
	content = strings.ToLower(strings.TrimSpace(content))
	for _, prefix := range []string{
		"tool call failed:",
		"tool call denied:",
		"tool call skipped: waiting for approval",
		"permission denied:",
	} {
		if strings.HasPrefix(content, prefix) {
			return false
		}
	}
	return true
}

func projectEinoAssistantPhaseVerificationReady(content string) bool {
	return projectEinoAssistantPhaseVerificationDisposition(content) == projectEinoAssistantVerificationReadyDisposition
}

func projectEinoAssistantPhaseVerificationDisposition(content string) projectEinoAssistantVerificationDisposition {
	var result projectAssistantRuntimeVerificationResult
	if err := json.Unmarshal([]byte(strings.TrimSpace(content)), &result); err != nil {
		return projectEinoAssistantVerificationBlocked
	}
	return projectEinoAssistantRuntimeVerificationDisposition(result)
}

func projectEinoAssistantPhaseFilterTools(
	phase projectEinoAssistantPhase,
	approvedPlan *projectAssistantApprovedPlan,
	templateBootstrapAllowed bool,
	tools []*schema.ToolInfo,
	inlinePromotion bool,
	activePolicy projectAssistantTurnPolicy,
) []*schema.ToolInfo {
	if tools == nil {
		return nil
	}
	filtered := make([]*schema.ToolInfo, 0, len(tools))
	for _, tool := range tools {
		if projectEinoAssistantPhaseAllowsTool(phase, approvedPlan, templateBootstrapAllowed, tool, inlinePromotion, activePolicy) {
			filtered = append(filtered, tool)
		}
	}
	return filtered
}

func projectEinoAssistantPhaseFilterPreMutationTools(
	tools []*schema.ToolInfo,
	templateBootstrapAllowed bool,
) []*schema.ToolInfo {
	filtered := make([]*schema.ToolInfo, 0, len(tools))
	for _, tool := range tools {
		if projectEinoAssistantPhasePreMutationToolAllowed(tool, templateBootstrapAllowed) {
			filtered = append(filtered, tool)
		}
	}
	return filtered
}

func projectEinoAssistantPhasePreMutationToolAllowed(
	tool *schema.ToolInfo,
	templateBootstrapAllowed bool,
) bool {
	if tool == nil {
		return false
	}
	name := projectToolBaseName(tool.Name)
	if name == projectEinoAssistantWriteTodosTool || name == projectToolAskFollowUp {
		return true
	}
	risk, bundle, ok := projectEinoAssistantPhaseToolMetadata(tool)
	if !ok {
		return false
	}
	return (projectEinoAssistantPhaseCanonicalEditTool(tool.Name) &&
		bundle == projectAssistantToolBundleEdit &&
		risk == projectAssistantToolRiskWrite) ||
		projectEinoAssistantPhaseTemplateBootstrapTool(
			tool.Name,
			risk,
			bundle,
			templateBootstrapAllowed,
		) ||
		projectEinoAssistantPhaseTemplateInspectionTool(
			tool.Name,
			risk,
			bundle,
			templateBootstrapAllowed,
		)
}

func projectEinoAssistantPhaseMergeTools(existing, current []*schema.ToolInfo) []*schema.ToolInfo {
	if len(current) == 0 {
		return existing
	}
	merged := append([]*schema.ToolInfo(nil), existing...)
	for _, tool := range current {
		if tool == nil {
			continue
		}
		found := false
		for _, known := range merged {
			if known != nil && strings.EqualFold(strings.TrimSpace(known.Name), strings.TrimSpace(tool.Name)) {
				found = true
				break
			}
		}
		if !found {
			merged = append(merged, tool)
		}
	}
	return merged
}

func projectEinoAssistantPhaseFilterPostInspectionSearchTools(
	tools []*schema.ToolInfo,
) []*schema.ToolInfo {
	filtered := make([]*schema.ToolInfo, 0, len(tools))
	for _, tool := range tools {
		if projectEinoAssistantPhaseApprovalSearchTool(tool) {
			continue
		}
		filtered = append(filtered, tool)
	}
	return filtered
}

func projectEinoAssistantPhaseApprovalSearchTool(tool *schema.ToolInfo) bool {
	if tool == nil {
		return false
	}
	switch strings.TrimSpace(tool.Name) {
	case projectToolLS, projectToolGlob, projectToolGrep:
		return true
	default:
		return false
	}
}

func projectEinoAssistantPhaseWithoutToolSearch(tools []*schema.ToolInfo) []*schema.ToolInfo {
	filtered := make([]*schema.ToolInfo, 0, len(tools))
	for _, tool := range tools {
		if tool != nil && projectToolBaseName(tool.Name) == projectEinoAssistantToolSearchTool {
			continue
		}
		filtered = append(filtered, tool)
	}
	return filtered
}

func projectEinoAssistantPhaseWithoutWriteFile(tools []*schema.ToolInfo) []*schema.ToolInfo {
	return projectEinoAssistantPhaseWithoutNamedTool(tools, projectToolWriteFile)
}

func projectEinoAssistantPhaseWithoutNamedTool(tools []*schema.ToolInfo, name string) []*schema.ToolInfo {
	filtered := make([]*schema.ToolInfo, 0, len(tools))
	for _, tool := range tools {
		if tool != nil && projectToolBaseName(tool.Name) == name {
			continue
		}
		filtered = append(filtered, tool)
	}
	return filtered
}

func projectEinoAssistantPhaseOnlyNamedTool(tools []*schema.ToolInfo, name string) []*schema.ToolInfo {
	filtered := make([]*schema.ToolInfo, 0, 1)
	for _, tool := range tools {
		if tool != nil && projectToolBaseName(tool.Name) == name {
			filtered = append(filtered, tool)
			break
		}
	}
	return filtered
}

func projectEinoAssistantPhaseWithNamedTool(
	current []*schema.ToolInfo,
	available []*schema.ToolInfo,
	name string,
) []*schema.ToolInfo {
	for _, tool := range current {
		if tool != nil && projectToolBaseName(tool.Name) == name {
			return current
		}
	}
	for _, tool := range available {
		if tool != nil && projectToolBaseName(tool.Name) == name {
			return append(current, tool)
		}
	}
	return current
}

func projectEinoAssistantPhaseAllowsTool(
	phase projectEinoAssistantPhase,
	approvedPlan *projectAssistantApprovedPlan,
	templateBootstrapAllowed bool,
	tool *schema.ToolInfo,
	inlinePromotion bool,
	activePolicy projectAssistantTurnPolicy,
) bool {
	if tool == nil {
		return false
	}
	name := projectToolBaseName(tool.Name)
	if strings.TrimSpace(tool.Name) == projectToolInspectDevelopmentTemplates &&
		!templateBootstrapAllowed {
		return false
	}
	if name == projectEinoAssistantToolSearchTool {
		return phase == projectEinoAssistantPhaseApproval
	}
	if name == projectToolDefineInitialProjectPlan {
		return phase == projectEinoAssistantPhasePlan ||
			(phase == projectEinoAssistantPhaseRepair &&
				approvedPlan != nil && approvedPlan.RunLocal)
	}
	if name == projectEinoAssistantWriteTodosTool {
		return (phase == projectEinoAssistantPhaseMutate ||
			phase == projectEinoAssistantPhaseRepair) &&
			approvedPlan != nil &&
			(approvedPlan.RunLocal || len(approvedPlan.Steps) > 1)
	}
	risk, bundle, ok := projectEinoAssistantPhaseToolMetadata(tool)
	if !ok {
		return false
	}
	templateBootstrap := projectEinoAssistantPhaseTemplateBootstrapTool(
		tool.Name,
		risk,
		bundle,
		templateBootstrapAllowed,
	)
	templateInspection := projectEinoAssistantPhaseTemplateInspectionTool(
		tool.Name,
		risk,
		bundle,
		templateBootstrapAllowed,
	)
	directAction := projectEinoAssistantPhaseDirectActionTool(tool.Name, risk, bundle)
	operationalRead := projectEinoAssistantPhaseOperationalReadTool(tool.Name, risk, bundle)
	if name == projectToolSelectTemplate && !templateBootstrap {
		return false
	}
	if name == projectToolInspectDevelopmentTemplates && !templateInspection {
		return false
	}
	if projectEinoAssistantPhaseReservedDirectActionName(tool.Name) && !directAction {
		return false
	}
	if projectEinoAssistantPhaseReservedOperationalReadName(tool.Name) && !operationalRead {
		return false
	}
	if phase == projectEinoAssistantPhaseApproval &&
		inlinePromotion &&
		!activePolicy.AllowsTool(projectAssistantToolSpec{Name: tool.Name, Risk: risk}) {
		return false
	}

	switch phase {
	case projectEinoAssistantPhasePlan:
		return name == projectToolDefineInitialProjectPlan
	case projectEinoAssistantPhaseApproval:
		if operationalRead {
			return true
		}
		if !inlinePromotion && (templateBootstrap || directAction) {
			return true
		}
		if bundle == projectAssistantToolBundleRuntime {
			return false
		}
		return risk == projectAssistantToolRiskRead ||
			risk == projectAssistantToolRiskInput ||
			risk == projectAssistantToolRiskPlan
	case projectEinoAssistantPhaseMutate:
		return (projectEinoAssistantPhaseCanonicalEditTool(tool.Name) &&
			bundle == projectAssistantToolBundleEdit && risk == projectAssistantToolRiskWrite) ||
			(name == projectToolVerifyDevelopmentRuntime &&
				bundle == projectAssistantToolBundleRuntime && risk == projectAssistantToolRiskRead) ||
			templateBootstrap ||
			templateInspection ||
			directAction ||
			operationalRead ||
			(name == projectToolAskFollowUp && risk == projectAssistantToolRiskInput)
	case projectEinoAssistantPhaseVerify:
		return (projectEinoAssistantPhaseCanonicalEditTool(tool.Name) &&
			bundle == projectAssistantToolBundleEdit && risk == projectAssistantToolRiskWrite) ||
			(tool.Name == projectToolVerifyDevelopmentRuntime &&
				bundle == projectAssistantToolBundleRuntime && risk == projectAssistantToolRiskRead) ||
			(name == projectToolAskFollowUp && risk == projectAssistantToolRiskInput)
	case projectEinoAssistantPhaseRepair:
		return (bundle == projectAssistantToolBundleWorkspaceRead && risk == projectAssistantToolRiskRead) ||
			(projectEinoAssistantPhaseCanonicalEditTool(tool.Name) &&
				bundle == projectAssistantToolBundleEdit && risk == projectAssistantToolRiskWrite) ||
			(name == projectToolVerifyDevelopmentRuntime &&
				bundle == projectAssistantToolBundleRuntime && risk == projectAssistantToolRiskRead) ||
			templateBootstrap ||
			templateInspection ||
			directAction ||
			operationalRead ||
			(name == projectToolAskFollowUp && risk == projectAssistantToolRiskInput)
	case projectEinoAssistantPhaseWarmup:
		return operationalRead &&
			(name == projectToolGetRuntimeStatus ||
				name == projectToolGetPreviewURL ||
				name == projectToolGetRuntimeLogs ||
				name == projectToolVerifyDevelopmentRuntime)
	case projectEinoAssistantPhaseCommit:
		return tool.Name == projectToolCommitProjectFiles &&
			bundle == projectAssistantToolBundleRepo &&
			risk == projectAssistantToolRiskCommit
	case projectEinoAssistantPhaseReport:
		return false
	default:
		return false
	}
}

func projectEinoAssistantPhaseReservedDirectActionName(name string) bool {
	switch projectToolBaseName(name) {
	case projectToolRestartRuntime,
		projectToolSetRuntimeEnv,
		projectToolPromoteProject,
		projectToolRebuildProject,
		"provision":
		return true
	default:
		return false
	}
}

func projectEinoAssistantPhaseDirectActionName(name string) bool {
	switch strings.TrimSpace(name) {
	case projectToolRestartRuntime,
		projectToolSetRuntimeEnv,
		projectToolPromoteProject,
		projectToolRebuildProject,
		projectToolInfrastructureProvision:
		return true
	default:
		return false
	}
}

func projectEinoAssistantPhaseDirectActionTool(
	name string,
	risk projectAssistantToolRisk,
	bundle projectAssistantToolBundle,
) bool {
	if risk != projectAssistantToolRiskRuntime || !projectEinoAssistantPhaseDirectActionName(name) {
		return false
	}
	switch strings.TrimSpace(name) {
	case projectToolInfrastructureProvision:
		return bundle == projectAssistantToolBundleInfrastructure
	default:
		return bundle == projectAssistantToolBundleRuntime
	}
}

func projectEinoAssistantPhaseOperationalReadTool(
	name string,
	risk projectAssistantToolRisk,
	bundle projectAssistantToolBundle,
) bool {
	if risk != projectAssistantToolRiskRead {
		return false
	}
	switch strings.TrimSpace(name) {
	case projectToolGetRuntimeStatus,
		projectToolGetPreviewURL,
		projectToolGetRuntimeLogs,
		projectToolVerifyDevelopmentRuntime:
		return bundle == projectAssistantToolBundleRuntime
	case projectToolCheckProjectBuild, projectToolGetBuildLogs:
		return bundle == projectAssistantToolBundleWorkflow
	case projectToolInfrastructureListTemplates,
		projectToolInfrastructureDescribeTemplate,
		projectToolInfrastructureListInstances,
		projectToolInfrastructureGetInstance:
		return bundle == projectAssistantToolBundleInfrastructure
	default:
		return false
	}
}

func projectEinoAssistantPhaseReservedOperationalReadName(name string) bool {
	switch projectToolBaseName(name) {
	case projectToolGetRuntimeStatus,
		projectToolGetPreviewURL,
		projectToolGetRuntimeLogs,
		projectToolVerifyDevelopmentRuntime,
		projectToolCheckProjectBuild,
		projectToolGetBuildLogs,
		"list_templates",
		"describe_template",
		"list_instances",
		"get_instance":
		return true
	default:
		return false
	}
}

func projectEinoAssistantPhaseTemplateInspectionTool(
	name string,
	risk projectAssistantToolRisk,
	bundle projectAssistantToolBundle,
	allowed bool,
) bool {
	return allowed &&
		strings.TrimSpace(name) == projectToolInspectDevelopmentTemplates &&
		risk == projectAssistantToolRiskRead &&
		bundle == projectAssistantToolBundleWorkflow
}

// Template selection is the one workflow/write exception because binding the
// initial development template is the first mutation for a template-less app.
func projectEinoAssistantPhaseTemplateBootstrapTool(
	name string,
	risk projectAssistantToolRisk,
	bundle projectAssistantToolBundle,
	allowed bool,
) bool {
	return allowed &&
		name == projectToolSelectTemplate &&
		risk == projectAssistantToolRiskWrite &&
		bundle == projectAssistantToolBundleWorkflow
}

func projectEinoAssistantPhaseTemplateBootstrapAllowed(project *aiv1alpha1.Project) bool {
	return project != nil &&
		(project.Spec.Template == nil || strings.TrimSpace(project.Spec.Template.Name) == "")
}

func projectEinoAssistantPhaseCanonicalEditTool(name string) bool {
	switch name {
	case projectToolWriteFile, projectToolApplyPatch, projectToolMkdir:
		return true
	default:
		return false
	}
}

func projectEinoAssistantPhaseToolMetadata(tool *schema.ToolInfo) (projectAssistantToolRisk, projectAssistantToolBundle, bool) {
	if tool == nil {
		return "", "", false
	}
	if tool.Extra != nil {
		risk, riskOK := tool.Extra["risk"].(string)
		bundle, bundleOK := tool.Extra["bundle"].(string)
		if riskOK && bundleOK {
			return projectAssistantToolRisk(strings.TrimSpace(risk)), projectAssistantToolBundle(strings.TrimSpace(bundle)), true
		}
	}
	if projectEinoAssistantFilesystemReadTool(tool.Name) {
		return projectAssistantToolRiskRead, projectAssistantToolBundleWorkspaceRead, true
	}
	return "", "", false
}
