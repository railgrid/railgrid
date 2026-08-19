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
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	aiv1alpha1 "github.com/faroshq/provider-app-studio/apis/ai/v1alpha1"
	asclient "github.com/faroshq/provider-app-studio/client"
	"github.com/faroshq/provider-app-studio/store"
)

type projectAssistantCheckpointState struct {
	AssistantMessageID               string                                              `json:"assistantMessageID,omitempty"`
	ToolCalls                        []chatToolCall                                      `json:"toolCalls"`
	CurrentIndex                     int                                                 `json:"currentIndex"`
	ProjectRepositoryRef             string                                              `json:"projectRepositoryRef,omitempty"`
	AgentOptimizationMode            string                                              `json:"agentOptimizationMode,omitempty"`
	DynamicToolCatalogDigest         string                                              `json:"dynamicToolCatalogDigest,omitempty"`
	SelectedDynamicToolNames         []string                                            `json:"selectedDynamicToolNames,omitempty"`
	TurnPolicy                       projectAssistantCheckpointTurnPolicy                `json:"turnPolicy"`
	Messages                         []chatMessage                                       `json:"messages,omitempty"`
	Turn                             int                                                 `json:"turn,omitempty"`
	SeenToolCalls                    map[string]int                                      `json:"seenToolCalls,omitempty"`
	ForceTextAnswer                  bool                                                `json:"forceTextAnswer,omitempty"`
	RepeatedToolLoop                 bool                                                `json:"repeatedToolLoop,omitempty"`
	LastToolMessages                 []chatMessage                                       `json:"lastToolMessages,omitempty"`
	CatalogDigest                    string                                              `json:"catalogDigest,omitempty"`
	SelectedSkillReceipts            []projectAssistantSkillReceipt                      `json:"selectedSkillReceipts,omitempty"`
	LoadedSkillReceipts              []projectAssistantSkillReceipt                      `json:"loadedSkillReceipts,omitempty"`
	SelectedContextResourceReceipts  []projectAssistantContextResourceReceipt            `json:"selectedContextResourceReceipts,omitempty"`
	ContentParts                     []projectAssistantContentPart                       `json:"contentParts,omitempty"`
	ApprovedPlan                     *projectAssistantApprovedPlan                       `json:"approvedPlan,omitempty"`
	ExecutionPlan                    *projectAssistantApprovedPlan                       `json:"executionPlan,omitempty"`
	PlanProgress                     projectAssistantPlanSnapshot                        `json:"planProgress,omitempty"`
	SourceMutationRevision           uint64                                              `json:"sourceMutationRevision,omitempty"`
	VerifiedMutationRevision         uint64                                              `json:"verifiedMutationRevision,omitempty"`
	DevelopmentSyncRevision          uint64                                              `json:"developmentSyncRevision,omitempty"`
	DevelopmentSyncStatus            string                                              `json:"developmentSyncStatus,omitempty"`
	DevelopmentSyncFailure           string                                              `json:"developmentSyncFailure,omitempty"`
	DevelopmentSyncRetry             uint64                                              `json:"developmentSyncRetry,omitempty"`
	CommitRequired                   bool                                                `json:"commitRequired,omitempty"`
	CommittedMutationRevision        uint64                                              `json:"committedMutationRevision,omitempty"`
	CommitAttemptedRevision          uint64                                              `json:"commitAttemptedRevision,omitempty"`
	VerifiedWorkspaceDigest          string                                              `json:"verifiedWorkspaceDigest,omitempty"`
	CommittedWorkspaceDigest         string                                              `json:"committedWorkspaceDigest,omitempty"`
	CheckedMutationRevision          uint64                                              `json:"checkedMutationRevision,omitempty"`
	VerificationAttempted            bool                                                `json:"verificationAttempted,omitempty"`
	VerificationOutcome              string                                              `json:"verificationOutcome,omitempty"`
	VerificationSummary              string                                              `json:"verificationSummary,omitempty"`
	VerificationBlockers             []string                                            `json:"verificationBlockers,omitempty"`
	PreviewEvidence                  projectAssistantPreviewEvidence                     `json:"previewEvidence,omitempty"`
	RepeatedActionSignature          string                                              `json:"repeatedActionSignature,omitempty"`
	RepeatedActionToolName           string                                              `json:"repeatedActionToolName,omitempty"`
	RepeatedActionCount              int                                                 `json:"repeatedActionCount,omitempty"`
	RuntimeWarmupAttempts            int                                                 `json:"runtimeWarmupAttempts,omitempty"`
	ModelCallOrdinal                 int                                                 `json:"modelCallOrdinal,omitempty"`
	AcceptedProgressCount            int                                                 `json:"acceptedProgressCount,omitempty"`
	LastAcceptedProgressModelCall    int                                                 `json:"lastAcceptedProgressModelCall,omitempty"`
	ProgressReminderKind             string                                              `json:"progressReminderKind,omitempty"`
	ProgressReminderAttempts         int                                                 `json:"progressReminderAttempts,omitempty"`
	ProgressReminderSilenceTriggered bool                                                `json:"progressReminderSilenceTriggered,omitempty"`
	CompletedReadCalls               map[string]uint64                                   `json:"completedReadCalls,omitempty"`
	ReadFileCoverage                 map[string][]projectAssistantCheckpointLineRange    `json:"readFileCoverage,omitempty"`
	ObservedReadFilePaths            []string                                            `json:"observedReadFilePaths,omitempty"`
	ReadFileVersions                 map[string]string                                   `json:"readFileVersions,omitempty"`
	SuccessfulMutationPaths          []string                                            `json:"successfulMutationPaths,omitempty"`
	MutationRecoveryAttempts         map[string]projectAssistantMutationRecoveryAttempt  `json:"mutationRecoveryAttempts,omitempty"`
	MutationRecoveryRefs             []string                                            `json:"mutationRecoveryRefs,omitempty"`
	MutationRecoveryIdentities       map[string]projectAssistantMutationRecoveryIdentity `json:"mutationRecoveryIdentities,omitempty"`
	SessionSnapshot                  *projectEinoAssistantSessionSnapshot                `json:"sessionSnapshot,omitempty"`
	RolloutBudget                    *projectAssistantRolloutBudgetState                 `json:"rolloutBudget,omitempty"`
	Eino                             *projectAssistantEinoCheckpointState                `json:"eino,omitempty"`
}

const (
	projectAssistantCheckpointMaxMessages = 256
	projectAssistantCheckpointMaxBytes    = 4 << 20
)

type projectAssistantCheckpointLineRange struct {
	Start int    `json:"start"`
	End   uint64 `json:"end"`
}

type projectAssistantCheckpointTurnPolicy struct {
	Profile projectAssistantTurnProfile `json:"profile"`
}

type projectAssistantEinoCheckpointState struct {
	CheckpointID  string `json:"checkpointID,omitempty"`
	Checkpoint    []byte `json:"checkpoint,omitempty"`
	InterruptID   string `json:"interruptID,omitempty"`
	InterruptType string `json:"interruptType,omitempty"`
	ToolCallID    string `json:"toolCallID,omitempty"`
	ToolName      string `json:"toolName,omitempty"`
}

type projectAssistantResumeRequest struct {
	RequestID          string                                    `json:"requestID"`
	Decision           string                                    `json:"decision,omitempty"`
	Answer             string                                    `json:"answer,omitempty"`
	Answers            map[string]projectAssistantFollowUpAnswer `json:"answers,omitempty"`
	AssistantMessageID string                                    `json:"-"`
	EditedArguments    map[string]any                            `json:"editedArguments,omitempty"`
}

type projectAssistantResumeResponse struct {
	RunID            string                             `json:"runID"`
	RequestID        string                             `json:"requestID"`
	Status           store.AssistantRunStatus           `json:"status"`
	Decision         projectAssistantPermissionDecision `json:"decision"`
	UIEvents         []projectAssistantUIEvent          `json:"uiEvents,omitempty"`
	AssistantMessage *aiv1alpha1.ProjectMessage         `json:"assistantMessage,omitempty"`
	ToolCall         *projectToolCallStreamEvent        `json:"-"`
	Permission       *projectAssistantPermission        `json:"-"`
	FollowUp         *projectAssistantFollowUp          `json:"-"`
	Checkpoint       *projectAssistantCheckpoint        `json:"-"`
	Progress         *projectAssistantProgressSnapshot  `json:"-"`
	AssistantContent string                             `json:"-"`
	Result           string                             `json:"-"`
	SuspensionReason string                             `json:"-"`
}

type projectAssistantRunAudit struct {
	Version                  int                                      `json:"version,omitempty"`
	StartRequestDigest       string                                   `json:"startRequestDigest,omitempty"`
	ActorDigest              string                                   `json:"actorDigest,omitempty"`
	ModelID                  string                                   `json:"modelID,omitempty"`
	ModelRevisionID          string                                   `json:"modelRevisionID,omitempty"`
	CatalogDigest            string                                   `json:"catalogDigest,omitempty"`
	SelectedSkills           []projectAssistantSkillReceipt           `json:"selectedSkills,omitempty"`
	SelectedContextResources []projectAssistantContextResourceReceipt `json:"selectedContextResources,omitempty"`
	ContentParts             []projectAssistantContentPart            `json:"contentParts,omitempty"`
	StopRequestID            string                                   `json:"stopRequestID,omitempty"`
	StopRequestDigest        string                                   `json:"stopRequestDigest,omitempty"`
	Provider                 string                                   `json:"provider,omitempty"`
	Model                    string                                   `json:"model,omitempty"`
	EffectiveSettings        *projectAssistantAuditEffectiveSettings  `json:"effectiveSettings,omitempty"`
	ApprovalMode             store.AssistantApprovalMode              `json:"approvalMode,omitempty"`
	Profile                  projectAssistantTurnProfile              `json:"profile,omitempty"`
	StartedAt                time.Time                                `json:"startedAt,omitempty"`
	Tools                    []projectAssistantAuditTool              `json:"tools,omitempty"`
	ModelCalls               []projectAssistantAuditModelCall         `json:"modelCalls,omitempty"`
	ModelCallStats           *projectAssistantAuditModelCallStats     `json:"modelCallStats,omitempty"`
	Compactions              []projectAssistantAuditCompaction        `json:"compactions,omitempty"`
	RolloutBudget            *projectAssistantRolloutBudgetState      `json:"rolloutBudget,omitempty"`
	Failure                  *projectAssistantAuditFailure            `json:"failure,omitempty"`
	Outcome                  projectAssistantAuditOutcome             `json:"outcome,omitempty"`
	DurationMS               int64                                    `json:"durationMs,omitempty"`
	Decisions                []projectAssistantPermissionAudit        `json:"decisions,omitempty"`
}

// projectAssistantAuditEffectiveSettings records the bounded, server-selected
// settings that governed a terminal assistant segment. Values are deliberately
// limited to stable identifiers and digests; request payloads, credentials,
// URLs, and prompt contents never belong in an audit.
type projectAssistantAuditEffectiveSettings struct {
	Provider                 string `json:"provider,omitempty"`
	Model                    string `json:"model,omitempty"`
	OptimizationMode         string `json:"optimizationMode,omitempty"`
	ToolContractDigest       string `json:"toolContractDigest,omitempty"`
	DynamicToolCatalogDigest string `json:"dynamicToolCatalogDigest,omitempty"`
	InstructionDigest        string `json:"instructionDigest,omitempty"`
}

// projectAssistantAuditModelCallStats is the uncapped model-call rollup. The
// detailed ModelCalls slice is intentionally bounded, while these counters
// remain truthful across the full run (including calls evicted from that
// window). Token fields are populated only when a provider returns usage.
type projectAssistantAuditModelCallStats struct {
	TotalCalls         int   `json:"totalCalls"`
	RetainedCalls      int   `json:"retainedCalls"`
	DroppedCalls       int   `json:"droppedCalls,omitempty"`
	RetryAttempts      int   `json:"retryAttempts,omitempty"`
	InputBytes         int64 `json:"inputBytes,omitempty"`
	PromptTokens       int64 `json:"promptTokens,omitempty"`
	CachedPromptTokens int64 `json:"cachedPromptTokens,omitempty"`
	CompletionTokens   int64 `json:"completionTokens,omitempty"`
	TotalTokens        int64 `json:"totalTokens,omitempty"`
	MissingUsageCalls  int   `json:"missingUsageCalls,omitempty"`
}

type projectAssistantAuditCompaction struct {
	ID                       string `json:"id"`
	Trigger                  string `json:"trigger"`
	Status                   string `json:"status"`
	WindowNumber             uint64 `json:"windowNumber,omitempty"`
	PreviousWindowID         string `json:"previousWindowID,omitempty"`
	WindowID                 string `json:"windowID,omitempty"`
	PriorTokenEstimate       int    `json:"priorTokenEstimate,omitempty"`
	ReplacementTokenEstimate int    `json:"replacementTokenEstimate,omitempty"`
	IgnoredToolCallCount     int    `json:"ignoredToolCallCount,omitempty"`
	Error                    string `json:"error,omitempty"`
	AtOffsetMS               int64  `json:"atOffsetMs"`
	CompletedAtOffsetMS      *int64 `json:"completedAtOffsetMs,omitempty"`
}

func (s *Server) saveProjectAssistantRun(ctx context.Context, scope store.Scope, run store.AssistantRun) error {
	if accumulator := s.projectAssistantSupervisor().accumulatorFor(scope, run.ID); accumulator != nil {
		return accumulator.UpdateRun(ctx, func(current *store.AssistantRun) {
			current.Status = run.Status
			current.RequestID = run.RequestID
			current.Checkpoint = append([]byte(nil), run.Checkpoint...)
			current.Audit = append([]byte(nil), run.Audit...)
		})
	}
	return s.store.SaveAssistantRun(ctx, scope, run)
}

type projectAssistantPermissionAudit struct {
	RequestID       string                             `json:"requestID"`
	Decision        projectAssistantPermissionDecision `json:"decision"`
	Actor           string                             `json:"actor,omitempty"`
	ToolCallID      string                             `json:"toolCallID,omitempty"`
	ToolName        string                             `json:"toolName,omitempty"`
	Reason          string                             `json:"reason,omitempty"`
	Source          string                             `json:"source,omitempty"`
	ApprovalMode    store.AssistantApprovalMode        `json:"approvalMode,omitempty"`
	EditedArguments map[string]any                     `json:"-"`
	Result          string                             `json:"-"`
	Error           string                             `json:"-"`
	ResolvedAt      time.Time                          `json:"resolvedAt"`
}

func newProjectAssistantRunID() string {
	return "run-" + uuid.NewString()
}

func newProjectAssistantPermissionRequestID() string {
	return "perm-" + uuid.NewString()
}

func newProjectAssistantInputRequestID() string {
	return "input-" + uuid.NewString()
}

func appendProjectAssistantResumeResolvedUI(out *projectAssistantResumeResponse, assistantMessageID string, requestID string, toolCall *projectToolCallStreamEvent) {
	if out == nil {
		return
	}
	_ = toolCall
	if requestID != "" {
		out.UIEvents = append(out.UIEvents, projectAssistantUIResolvedInterruptEvent(assistantMessageID, requestID))
	}
}

func appendProjectAssistantResumePendingUI(out *projectAssistantResumeResponse, assistantMessageID string) {
	if out == nil || out.Checkpoint == nil {
		return
	}
	if out.FollowUp != nil {
		out.UIEvents = append(out.UIEvents,
			projectAssistantUIFollowUpInterruptRequestEvent(assistantMessageID, *out.FollowUp, *out.Checkpoint),
		)
		return
	}
	if out.Permission != nil {
		out.UIEvents = append(out.UIEvents,
			projectAssistantUIInterruptRequestEvent(assistantMessageID, *out.Permission, *out.Checkpoint),
		)
	}
}

func appendProjectAssistantResumeDevelopmentPreviewRefreshUI(out *projectAssistantResumeResponse, needed bool) {
	if out == nil || !needed {
		return
	}
	out.UIEvents = append(out.UIEvents, projectAssistantUIDevelopmentPreviewRefreshEvent())
}

func (s *Server) saveProjectAssistantEinoPermissionCheckpoint(
	ctx context.Context,
	req projectAssistantRunRequest,
	state projectAssistantCheckpointState,
	info *projectEinoPermissionInterruptInfo,
) (*projectAssistantPermissionRequiredError, projectAssistantPermission, projectAssistantCheckpoint, error) {
	if s.store == nil {
		return nil, projectAssistantPermission{}, projectAssistantCheckpoint{}, fmt.Errorf("project message store not configured")
	}
	if info == nil {
		return nil, projectAssistantPermission{}, projectAssistantCheckpoint{}, fmt.Errorf("assistant permission interrupt metadata missing")
	}
	if state.CurrentIndex < 0 || state.CurrentIndex >= len(state.ToolCalls) {
		return nil, projectAssistantPermission{}, projectAssistantCheckpoint{}, fmt.Errorf("assistant checkpoint index out of range")
	}
	if state.Eino == nil || len(state.Eino.Checkpoint) == 0 || strings.TrimSpace(state.Eino.CheckpointID) == "" || strings.TrimSpace(state.Eino.InterruptID) == "" {
		return nil, projectAssistantPermission{}, projectAssistantCheckpoint{}, fmt.Errorf("eino checkpoint missing")
	}
	requestID := newProjectAssistantPermissionRequestID()
	now := time.Now().UTC()
	state.ToolCalls = cloneProjectAssistantToolCalls(state.ToolCalls)
	state.ProjectRepositoryRef = strings.TrimSpace(state.ProjectRepositoryRef)
	state.Messages = cloneChatMessages(state.Messages)
	state.SeenToolCalls = cloneProjectAssistantSeenToolCalls(state.SeenToolCalls)
	state.LastToolMessages = cloneChatMessages(state.LastToolMessages)
	state.ApprovedPlan = cloneProjectAssistantApprovedPlan(state.ApprovedPlan)
	state.Eino = cloneProjectAssistantEinoCheckpointState(state.Eino)
	state.Messages = projectAssistantBoundCheckpointMessages(state.Messages)
	state.LastToolMessages = projectAssistantBoundCheckpointMessages(state.LastToolMessages)
	state.SeenToolCalls = projectEinoAssistantSanitizeSeenToolCalls(state.SeenToolCalls)
	if len(state.Eino.Checkpoint) > projectAssistantCheckpointMaxBytes {
		return nil, projectAssistantPermission{}, projectAssistantCheckpoint{}, fmt.Errorf("eino checkpoint exceeds %d bytes", projectAssistantCheckpointMaxBytes)
	}
	if state.AssistantMessageID == "" && req.AssistantRun != nil {
		state.AssistantMessageID = strings.TrimSpace(req.AssistantRun.ActiveMessageID)
	}
	if strings.TrimSpace(state.Eino.InterruptType) == "" {
		state.Eino.InterruptType = projectAssistantInterruptTypePermission
	}
	raw, err := json.Marshal(state)
	if err != nil {
		return nil, projectAssistantPermission{}, projectAssistantCheckpoint{}, fmt.Errorf("encode assistant checkpoint: %w", err)
	}
	run := store.AssistantRun{}
	if req.AssistantRun != nil {
		run = *req.AssistantRun
	}
	if strings.TrimSpace(run.ID) == "" {
		run.ID = strings.TrimSpace(state.Eino.CheckpointID)
	}
	if strings.TrimSpace(run.ID) == "" {
		run.ID = newProjectAssistantRunID()
	}
	if run.CreatedAt.IsZero() {
		run.CreatedAt = now
	}
	run.Status = store.AssistantRunStatusPendingPermission
	run.RequestID = requestID
	run.Checkpoint = raw
	run.UpdatedAt = now
	if err := projectAssistantExecutionAuthorityFor(s, req).PersistRun(ctx, run); err != nil {
		return nil, projectAssistantPermission{}, projectAssistantCheckpoint{}, err
	}
	req.AssistantRun = &run

	checkpointCreatedAt := now
	permission := projectAssistantPermissionForEinoInterrupt(requestID, state.ToolCalls[state.CurrentIndex], info)
	checkpoint := projectAssistantCheckpoint{
		ID:        run.ID,
		Reason:    "waiting_for_permission",
		CreatedAt: &checkpointCreatedAt,
	}
	return &projectAssistantPermissionRequiredError{
		RunID:     run.ID,
		RequestID: requestID,
		ToolName:  info.ToolName,
	}, permission, checkpoint, nil
}

func (s *Server) saveProjectAssistantEinoFollowUpCheckpoint(
	ctx context.Context,
	req projectAssistantRunRequest,
	state projectAssistantCheckpointState,
	info *projectEinoFollowUpInterruptInfo,
) (*projectAssistantInputRequiredError, projectAssistantFollowUp, projectAssistantCheckpoint, error) {
	if s.store == nil {
		return nil, projectAssistantFollowUp{}, projectAssistantCheckpoint{}, fmt.Errorf("project message store not configured")
	}
	if info == nil {
		return nil, projectAssistantFollowUp{}, projectAssistantCheckpoint{}, fmt.Errorf("assistant follow-up interrupt metadata missing")
	}
	if state.Eino == nil || len(state.Eino.Checkpoint) == 0 || strings.TrimSpace(state.Eino.CheckpointID) == "" || strings.TrimSpace(state.Eino.InterruptID) == "" {
		return nil, projectAssistantFollowUp{}, projectAssistantCheckpoint{}, fmt.Errorf("eino checkpoint missing")
	}
	requestID := newProjectAssistantInputRequestID()
	now := time.Now().UTC()
	state.ToolCalls = cloneProjectAssistantToolCalls(state.ToolCalls)
	state.ProjectRepositoryRef = strings.TrimSpace(state.ProjectRepositoryRef)
	state.Messages = cloneChatMessages(state.Messages)
	state.SeenToolCalls = cloneProjectAssistantSeenToolCalls(state.SeenToolCalls)
	state.LastToolMessages = cloneChatMessages(state.LastToolMessages)
	state.ApprovedPlan = cloneProjectAssistantApprovedPlan(state.ApprovedPlan)
	state.Eino = cloneProjectAssistantEinoCheckpointState(state.Eino)
	state.Messages = projectAssistantBoundCheckpointMessages(state.Messages)
	state.LastToolMessages = projectAssistantBoundCheckpointMessages(state.LastToolMessages)
	state.SeenToolCalls = projectEinoAssistantSanitizeSeenToolCalls(state.SeenToolCalls)
	if len(state.Eino.Checkpoint) > projectAssistantCheckpointMaxBytes {
		return nil, projectAssistantFollowUp{}, projectAssistantCheckpoint{}, fmt.Errorf("eino checkpoint exceeds %d bytes", projectAssistantCheckpointMaxBytes)
	}
	if state.AssistantMessageID == "" && req.AssistantRun != nil {
		state.AssistantMessageID = strings.TrimSpace(req.AssistantRun.ActiveMessageID)
	}
	state.Eino.InterruptType = projectAssistantInterruptTypeFollowUp
	raw, err := json.Marshal(state)
	if err != nil {
		return nil, projectAssistantFollowUp{}, projectAssistantCheckpoint{}, fmt.Errorf("encode assistant checkpoint: %w", err)
	}
	run := store.AssistantRun{}
	if req.AssistantRun != nil {
		run = *req.AssistantRun
	}
	if strings.TrimSpace(run.ID) == "" {
		run.ID = strings.TrimSpace(state.Eino.CheckpointID)
	}
	if strings.TrimSpace(run.ID) == "" {
		run.ID = newProjectAssistantRunID()
	}
	if run.CreatedAt.IsZero() {
		run.CreatedAt = now
	}
	run.Status = store.AssistantRunStatusPendingInput
	run.RequestID = requestID
	run.Checkpoint = raw
	run.UpdatedAt = now
	if err := projectAssistantExecutionAuthorityFor(s, req).PersistRun(ctx, run); err != nil {
		return nil, projectAssistantFollowUp{}, projectAssistantCheckpoint{}, err
	}
	req.AssistantRun = &run

	checkpointCreatedAt := now
	followUp := projectAssistantFollowUpForEinoInterrupt(requestID, info)
	checkpoint := projectAssistantCheckpoint{
		ID:        run.ID,
		Reason:    "waiting_for_input",
		CreatedAt: &checkpointCreatedAt,
	}
	return &projectAssistantInputRequiredError{
		RunID:     run.ID,
		RequestID: requestID,
	}, followUp, checkpoint, nil
}

func projectAssistantPermissionForEinoInterrupt(requestID string, tc chatToolCall, info *projectEinoPermissionInterruptInfo) projectAssistantPermission {
	permission := projectAssistantPermissionForCall(requestID, tc, projectAssistantToolSpec{
		Name: info.ToolName,
		Risk: info.Risk,
	})
	if reason := strings.TrimSpace(info.Reason); reason != "" {
		permission.Reason = reason
	}
	permission.Exec = cloneProjectAssistantExecMetadata(info.Exec)
	return permission
}

func projectAssistantFollowUpForEinoInterrupt(requestID string, info *projectEinoFollowUpInterruptInfo) projectAssistantFollowUp {
	if info == nil {
		return projectAssistantFollowUp{ID: requestID}
	}
	questions := normalizeProjectAssistantFollowUpQuestions(info.Questions)
	return projectAssistantFollowUp{
		ID:         requestID,
		ToolCallID: strings.TrimSpace(info.ToolCallID),
		Questions:  cloneProjectAssistantFollowUpQuestions(questions),
		Prompt:     strings.TrimSpace(info.Prompt),
	}
}

func projectAssistantPermissionReason(spec projectAssistantToolSpec) string {
	return projectAssistantPermissionReasonForArguments(spec, nil)
}

func projectAssistantPermissionReasonForArguments(spec projectAssistantToolSpec, args map[string]any) string {
	switch strings.TrimSpace(spec.Name) {
	case projectToolSelectTemplate:
		if template := projectToolString(args["template"]); template != "" {
			return fmt.Sprintf("Bind this project to development template %q. Switching templates tears down and re-provisions the development environment; workspace files and Git history are preserved.", template)
		}
		return "Bind this project to the selected development template. Switching templates tears down and re-provisions the development environment; workspace files and Git history are preserved."
	case projectToolRestartRuntime:
		if component := projectToolString(args["component"]); component != "" {
			return fmt.Sprintf("Restart development runtime component %q.", component)
		}
		return "Restart every component in this project's development runtime."
	case projectToolSetRuntimeEnv:
		count := 0
		if env, ok := args["env"].(map[string]any); ok {
			count = len(env)
		}
		if count > 0 {
			return fmt.Sprintf("Set %d non-secret development runtime environment variable(s) and apply the requested restart behavior.", count)
		}
		return "Change non-secret development runtime environment variables and apply the requested restart behavior."
	case projectToolExecCommand:
		component := projectToolString(args["component"])
		if component == "" {
			return "Run one bounded compiler, test, or lint command in the synchronized live development runtime."
		}
		return fmt.Sprintf("Run the approved bounded argv in live development component %q using application-container authority and the application network; no App Studio source writeback is allowed.", component)
	case projectToolRebuildProject:
		if ref := projectToolString(args["ref"]); ref != "" {
			return fmt.Sprintf("Re-run this project's build workflow for branch or ref %q without changing code.", ref)
		}
		return "Re-run this project's build workflow on the repository default branch without changing code."
	case projectToolPromoteProject:
		return "Deploy the current built project to its separate production environment."
	case projectToolInfrastructureProvision:
		template := projectToolString(args["template"])
		name := projectToolString(args["name"])
		switch {
		case template != "" && name != "":
			return fmt.Sprintf("Provision infrastructure instance %q from template %q.", name, template)
		case template != "":
			return fmt.Sprintf("Provision a new infrastructure instance from template %q.", template)
		default:
			return "Provision a new supporting infrastructure instance with the supplied configuration."
		}
	}
	switch spec.Risk {
	case projectAssistantToolRiskWrite:
		if targets, err := projectAssistantWriteTargetPaths(spec.Name, args); err == nil && len(targets) > 0 {
			quoted := make([]string, 0, len(targets))
			for _, target := range targets {
				quoted = append(quoted, fmt.Sprintf("%q", target))
			}
			return fmt.Sprintf("Allow this workspace edit to modify %s.", strings.Join(quoted, ", "))
		}
		return "This action will modify files in the App Studio workspace."
	case projectAssistantToolRiskPlan:
		if targets := projectAssistantApprovalTargetPaths(projectToolStringList(args["targetPaths"])); len(targets) > 0 {
			quoted := make([]string, 0, len(targets))
			for _, target := range targets {
				quoted = append(quoted, fmt.Sprintf("%q", target))
			}
			return fmt.Sprintf(
				"Allow App Studio to create or modify files in %s using workspace edit tools until the next commit request.",
				strings.Join(quoted, ", "),
			)
		}
		return "This plan will allow App Studio to modify the approved workspace paths until the next commit request."
	case projectAssistantToolRiskCommit:
		return "This action will commit App Studio workspace changes to the linked repository."
	case projectAssistantToolRiskRuntime:
		return "This action will request an App Studio runtime deployment handoff."
	default:
		return "This action requires approval."
	}
}

func projectAssistantApprovalTargetPaths(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if target := projectAssistantApprovalTargetPath(value); target != "" {
			out = append(out, target)
		}
	}
	return normalizeProjectAssistantStringList(out)
}

func projectAssistantApprovalTargetPath(value string) string {
	value, err := projectAssistantCanonicalGrantTarget(value, false)
	if err != nil {
		return ""
	}
	return value
}

func projectAssistantPermissionForCall(requestID string, tc chatToolCall, spec projectAssistantToolSpec) projectAssistantPermission {
	permissionInput := json.RawMessage(tc.Function.Arguments)
	args := map[string]any{}
	if !json.Valid(permissionInput) {
		permissionInput = nil
	} else {
		_ = json.Unmarshal(permissionInput, &args)
	}
	permission := projectAssistantPermission{
		ID:         requestID,
		ToolCallID: tc.ID,
		ToolName:   spec.Name,
		Reason:     projectAssistantPermissionReasonForArguments(spec, args),
		Input:      permissionInput,
	}
	permission.Exec = projectAssistantExecMetadataForToolArguments(spec.Name, args, "", "permission_required")
	return permission
}

func preflightProjectAssistantResume(
	run store.AssistantRun,
	req projectAssistantResumeRequest,
) (projectAssistantCheckpointState, projectAssistantPermissionDecision, error) {
	requestID := strings.TrimSpace(req.RequestID)
	if requestID == "" {
		return projectAssistantCheckpointState{}, "", newValidationError("assistant run request id is required")
	}
	if requestID != run.RequestID {
		return projectAssistantCheckpointState{}, "", newValidationError(fmt.Sprintf("assistant run %q is not waiting for this request", run.ID))
	}
	var state projectAssistantCheckpointState
	if err := json.Unmarshal(run.Checkpoint, &state); err != nil {
		return projectAssistantCheckpointState{}, "", fmt.Errorf("decode assistant checkpoint: %w", err)
	}
	if run.Status == store.AssistantRunStatusPendingPermission || run.Status == store.AssistantRunStatusPendingInput {
		if state.Eino == nil || strings.TrimSpace(state.Eino.InterruptType) == "" {
			return projectAssistantCheckpointState{}, "", newValidationError("assistant checkpoint is not resumable")
		}
		switch {
		case run.Status == store.AssistantRunStatusPendingPermission && !projectAssistantEinoInterruptTypeIsPermission(state.Eino.InterruptType):
			return projectAssistantCheckpointState{}, "", newValidationError("assistant checkpoint is not resumable")
		case run.Status == store.AssistantRunStatusPendingInput && state.Eino.InterruptType != projectAssistantInterruptTypeFollowUp:
			return projectAssistantCheckpointState{}, "", newValidationError("assistant checkpoint is not resumable")
		}
	}
	if state.Eino != nil && state.Eino.InterruptType == projectAssistantInterruptTypeFollowUp {
		if strings.TrimSpace(req.Answer) == "" && len(req.Answers) == 0 {
			return projectAssistantCheckpointState{}, "", newValidationError("answer is required")
		}
		return state, "", nil
	}
	decision, err := parseProjectAssistantPermissionDecision(req.Decision)
	if err != nil {
		return projectAssistantCheckpointState{}, "", err
	}
	return state, decision, nil
}

func (s *Server) resumeProjectAssistantRunWithRepositoryAndClient(
	ctx context.Context,
	r *http.Request,
	id identity,
	c *asclient.Client,
	p *aiv1alpha1.Project,
	repository *ProjectRepositoryView,
	runID string,
	req projectAssistantResumeRequest,
) (projectAssistantResumeResponse, error) {
	if s.store == nil {
		return projectAssistantResumeResponse{}, fmt.Errorf("project message store not configured")
	}
	if p == nil || strings.TrimSpace(p.Name) == "" {
		return projectAssistantResumeResponse{}, fmt.Errorf("project is required")
	}
	messageScope := projectMessageScope(id.orgUUID, id.workspaceUUID, p)
	preflightRun, err := s.store.GetAssistantRun(ctx, messageScope, runID)
	if err != nil {
		return projectAssistantResumeResponse{}, err
	}
	if strings.TrimSpace(preflightRun.ActiveMessageID) == "" {
		return projectAssistantResumeResponse{}, store.ErrAssistantRunNotFound
	}
	_, decision, err := preflightProjectAssistantResume(preflightRun, req)
	if err != nil {
		return projectAssistantResumeResponse{}, err
	}
	accumulator := s.projectAssistantSupervisor().accumulatorFor(messageScope, runID)
	if accumulator == nil {
		return projectAssistantResumeResponse{}, store.ErrAssistantRunConflict
	}
	run, err := accumulator.ClaimPending(ctx, strings.TrimSpace(req.RequestID))
	if err != nil {
		if strings.Contains(err.Error(), "not waiting") || strings.Contains(err.Error(), "request id is required") {
			if clearErr := s.clearProjectAssistantPendingMessageForNonWaitingRun(ctx, messageScope, preflightRun, req); clearErr != nil {
				return projectAssistantResumeResponse{}, clearErr
			}
			return projectAssistantResumeResponse{}, newValidationError(err.Error())
		}
		return projectAssistantResumeResponse{}, err
	}
	out := projectAssistantResumeResponse{
		RunID:     run.ID,
		RequestID: run.RequestID,
		Decision:  decision,
	}
	var state projectAssistantCheckpointState
	if err := json.Unmarshal(run.Checkpoint, &state); err != nil {
		return s.completeClaimedProjectAssistantRunAfterResumeError(ctx, messageScope, run, state, req, decision, id.user, out, nil, fmt.Errorf("decode assistant checkpoint: %w", err))
	}
	if projectAssistantCheckpointHasStaleRepositoryBinding(state, p) {
		staleBindingError := "Project repository binding changed after the assistant paused"
		tc := state.ToolCalls[state.CurrentIndex]
		now := time.Now().UTC()
		run.Status = store.AssistantRunStatusFailed
		run.Error = projectAssistantRunErrorJSON(errors.New(staleBindingError), "stale_repository_binding")
		run.UpdatedAt = now
		run, err = appendProjectAssistantRunAudit(run, projectAssistantPermissionAudit{
			RequestID:  run.RequestID,
			Decision:   decision,
			Actor:      id.user,
			ToolCallID: tc.ID,
			ToolName:   tc.Function.Name,
			Error:      staleBindingError,
			ResolvedAt: now,
		})
		if err != nil {
			return projectAssistantResumeResponse{}, err
		}
		run, err = finalizeProjectAssistantRunAudit(run, projectAssistantAuditOutcomeFailed, now)
		if err != nil {
			return projectAssistantResumeResponse{}, err
		}
		if saveErr := s.saveProjectAssistantResumeTerminalPreparation(ctx, messageScope, run); saveErr != nil {
			return projectAssistantResumeResponse{}, saveErr
		}
		out.Status = run.Status
		out.Result = staleBindingError
		out.ToolCall = &projectToolCallStreamEvent{
			ID:     tc.ID,
			Name:   tc.Function.Name,
			Status: "failed",
			Error:  staleBindingError,
		}
		appendProjectAssistantResumeResolvedUI(&out, strings.TrimSpace(req.AssistantMessageID), out.RequestID, out.ToolCall)
		if err := s.updateProjectAssistantPermissionMessage(ctx, messageScope, strings.TrimSpace(req.AssistantMessageID), out); err != nil {
			return projectAssistantResumeResponse{}, err
		}
		return projectAssistantResumeResponse{}, newValidationError(staleBindingError)
	}
	if state.Eino == nil {
		return s.completeClaimedProjectAssistantRunAfterResumeError(ctx, messageScope, run, state, req, decision, id.user, out, nil, newValidationError("assistant checkpoint is not resumable"))
	}
	return s.resumeClaimedProjectAssistantRunWithEinoCheckpoint(ctx, r, id, c, p, repository, run, state, req, decision, out)
}

func projectAssistantEinoInterruptTypeIsPermission(interruptType string) bool {
	switch strings.TrimSpace(interruptType) {
	case projectAssistantInterruptTypePermission, projectAssistantInterruptTypeApproval:
		return true
	default:
		return false
	}
}

func (s *Server) clearProjectAssistantPendingMessageForNonWaitingRun(ctx context.Context, scope store.Scope, run store.AssistantRun, req projectAssistantResumeRequest) error {
	runID := strings.TrimSpace(run.ID)
	requestID := strings.TrimSpace(req.RequestID)
	assistantMessageID := strings.TrimSpace(req.AssistantMessageID)
	if runID == "" || requestID == "" || assistantMessageID == "" {
		return nil
	}
	if strings.TrimSpace(run.RequestID) != requestID {
		return nil
	}
	switch run.Status {
	case store.AssistantRunStatusPendingPermission, store.AssistantRunStatusPendingInput:
		return nil
	}
	return s.updateProjectAssistantPermissionMessage(ctx, scope, assistantMessageID, projectAssistantResumeResponse{
		RunID:     runID,
		RequestID: requestID,
		Status:    run.Status,
	})
}

func (s *Server) resumeClaimedProjectAssistantRunWithEinoCheckpoint(
	ctx context.Context,
	r *http.Request,
	id identity,
	c *asclient.Client,
	p *aiv1alpha1.Project,
	repository *ProjectRepositoryView,
	run store.AssistantRun,
	state projectAssistantCheckpointState,
	resumeReq projectAssistantResumeRequest,
	decision projectAssistantPermissionDecision,
	out projectAssistantResumeResponse,
) (projectAssistantResumeResponse, error) {
	messageScope := projectMessageScope(id.orgUUID, id.workspaceUUID, p)
	turn := newProjectAssistantTurnItem(projectAssistantTurnResume, id, p.Name)
	turn.ProjectUID = messageScope.ProjectUID
	turn.RunID = run.ID
	turn.RequestID = run.RequestID
	turn.AssistantMessageID = strings.TrimSpace(resumeReq.AssistantMessageID)
	ctx, finishTurn := s.projectAssistantRunManager().Begin(ctx, turn)
	defer finishTurn()
	if cause := context.Cause(ctx); cause != nil {
		return s.completeClaimedProjectAssistantRunAfterResumeError(
			ctx,
			messageScope,
			run,
			state,
			resumeReq,
			decision,
			id.user,
			out,
			nil,
			cause,
		)
	}
	if r != nil {
		r = r.WithContext(ctx)
	}
	if c == nil {
		return s.completeClaimedProjectAssistantRunAfterResumeError(ctx, messageScope, run, state, resumeReq, decision, id.user, out, nil, fmt.Errorf("project client is required for assistant resume"))
	}
	registry, err := readProjectLLMRegistry(ctx, c)
	if err != nil {
		return s.completeClaimedProjectAssistantRunAfterResumeError(ctx, messageScope, run, state, resumeReq, decision, id.user, out, nil, err)
	}
	modelID, modelRevisionID := projectAssistantModelReferenceFromRunAudit(run)
	settings, err := registry.selectedSettings(modelID, modelRevisionID)
	if err != nil {
		return s.completeClaimedProjectAssistantRunAfterResumeError(ctx, messageScope, run, state, resumeReq, decision, id.user, out, nil, err)
	}
	if err := normalizeProjectLLMSettings(&settings); err != nil {
		return s.completeClaimedProjectAssistantRunAfterResumeError(ctx, messageScope, run, state, resumeReq, decision, id.user, out, nil, err)
	}
	if strings.TrimSpace(settings.APIKey) == "" {
		return s.completeClaimedProjectAssistantRunAfterResumeError(ctx, messageScope, run, state, resumeReq, decision, id.user, out, nil, errProjectLLMNotConfigured)
	}

	assistantID := strings.TrimSpace(run.ActiveMessageID)
	if assistantID == "" {
		assistantID = newMessageID()
	}
	assistantContent := &strings.Builder{}
	accumulator := s.projectAssistantSupervisor().accumulatorFor(messageScope, run.ID)
	metadataState := &projectAssistantDurableMetadataState{
		status:             "Working",
		workSegmentStarted: time.Now().UTC(),
	}
	if existingMessage, findErr := s.findProjectMessage(ctx, messageScope, assistantID); findErr == nil {
		if progress, ok := projectAssistantProgressSnapshotFromMetadata(existingMessage.Metadata[projectAssistantMetadataProgress]); ok {
			metadataState.restoreTrace(progress, projectAssistantActionFeedFromMetadata(existingMessage.Metadata[projectMessageMetadataAssistantActionFeed]))
		} else {
			metadataState.restoreTrace(nil, projectAssistantActionFeedFromMetadata(existingMessage.Metadata[projectMessageMetadataAssistantActionFeed]))
		}
	}
	var snapshotErr error
	var snapshotErrMu sync.Mutex
	recordSnapshotErr := func(err error) {
		if err == nil {
			return
		}
		snapshotErrMu.Lock()
		if snapshotErr == nil {
			snapshotErr = err
		}
		snapshotErrMu.Unlock()
	}
	getSnapshotErr := func() error {
		snapshotErrMu.Lock()
		defer snapshotErrMu.Unlock()
		return snapshotErr
	}
	var callbackMu sync.Mutex
	callbacksClosed := false
	persistMetadata := func(ctx context.Context, runStatus *store.AssistantRunStatus) {
		if accumulator != nil {
			recordSnapshotErr(s.persistProjectAssistantDurableMetadata(ctx, accumulator, projectWorkspaceScope(id, p), metadataState, runStatus))
		}
	}
	var pendingPermissionToolCallID string
	var pendingFollowUpToolCallID string
	activeMessageID := assistantID
	syncSteeringSegment := func() {
		if accumulator == nil {
			return
		}
		messageID := accumulator.ActiveMessageID()
		if messageID == "" || messageID == activeMessageID {
			return
		}
		assistantContent.Reset()
		assistantID = messageID
		activeMessageID = messageID
		pendingPermissionToolCallID = ""
		pendingFollowUpToolCallID = ""
		metadataState = &projectAssistantDurableMetadataState{
			status:             "Working",
			workSegmentStarted: time.Now().UTC(),
		}
	}
	emitAssistantEvent := func(event projectAssistantEvent) {
		callbackMu.Lock()
		defer callbackMu.Unlock()
		if callbacksClosed {
			return
		}
		syncSteeringSegment()
		switch event.Type {
		case projectAssistantEventPermissionNeeded:
			if event.Permission != nil && event.Permission.ToolCallID != "" {
				pendingPermissionToolCallID = event.Permission.ToolCallID
				out.Permission = event.Permission
				metadataState.upsertToolCall(projectToolCallStreamEvent{
					ID:         event.Permission.ToolCallID,
					Name:       event.Permission.ToolName,
					Status:     "permission_required",
					Summary:    event.Permission.Reason,
					Permission: event.Permission,
				})
			}
		case projectAssistantEventCheckpointSaved:
			if event.Checkpoint != nil {
				out.Checkpoint = event.Checkpoint
				if pendingPermissionToolCallID != "" {
					metadataState.upsertToolCall(projectToolCallStreamEvent{
						ID:         pendingPermissionToolCallID,
						Status:     "permission_required",
						Checkpoint: event.Checkpoint,
					})
				}
				if pendingFollowUpToolCallID != "" {
					metadataState.upsertToolCall(projectToolCallStreamEvent{
						ID:         pendingFollowUpToolCallID,
						Status:     "input_required",
						Checkpoint: event.Checkpoint,
					})
				}
			}
		case projectAssistantEventInputNeeded:
			if event.FollowUp != nil && event.FollowUp.ToolCallID != "" {
				pendingFollowUpToolCallID = event.FollowUp.ToolCallID
				out.FollowUp = event.FollowUp
				metadataState.upsertToolCall(projectToolCallStreamEvent{
					ID:       event.FollowUp.ToolCallID,
					Name:     projectToolAskFollowUp,
					Status:   "input_required",
					Summary:  event.FollowUp.Prompt,
					FollowUp: event.FollowUp,
				})
			}
		case projectAssistantEventPlanUpdated:
			if event.Plan != nil && projectAssistantPlanSnapshotValid(*event.Plan) {
				plan := cloneProjectAssistantPlanSnapshot(*event.Plan)
				metadataState.plan = &plan
				metadataState.status = projectEinoAssistantPlanProgressStatus(plan)
			}
		}
		// OnPlan already persisted this accepted snapshot; the typed event is
		// live-only here and must not advance durable metadata twice.
		if event.Type != projectAssistantEventPlanUpdated {
			persistMetadata(ctx, nil)
		}
	}
	streamToolCall := func(toolCall projectToolCallStreamEvent) {
		if toolCall.ID == "" || toolCall.Status == "" {
			return
		}
		metadataState.upsertToolCall(toolCall)
	}
	resumeRun := run
	engineReq := projectAssistantRunRequest{
		Identity:                 id,
		ToolPort:                 newProjectAssistantHTTPToolPort(s, r),
		Client:                   c,
		Project:                  p,
		Repository:               repository,
		WorkspaceScope:           projectWorkspaceScope(id, p),
		Workspace:                s.workspaces,
		MessageScope:             messageScope,
		LLM:                      settings,
		MCPBaseURL:               s.hubBase,
		MCPInsecureSkipTLSVerify: s.mcpInsecureSkipTLSVerify,
		ApprovalMode:             projectAssistantApprovalModeFromRun(resumeRun),
		Continuation:             &state,
		AssistantRun:             &resumeRun,
		Steering:                 s.projectAssistantSupervisor().Steering(messageScope, resumeRun.ID),
		SealSteering: func() bool {
			return s.projectAssistantSupervisor().SealSteering(messageScope, resumeRun.ID)
		},
		ActivateSteering: func(activateCtx context.Context, inputs []projectAssistantSteeringInput) error {
			return s.projectAssistantSupervisor().ActivateSteering(activateCtx, messageScope, resumeRun.ID, inputs)
		},
		StreamCallbacks: projectAssistantStreamCallbacks{
			OnChunk: func(chunk string) {
				callbackMu.Lock()
				defer callbackMu.Unlock()
				if callbacksClosed {
					return
				}
				syncSteeringSegment()
				content := appendProjectAssistantStreamBlock(assistantContent, chunk)
				if accumulator != nil {
					recordSnapshotErr(accumulator.UpdateText(ctx, content, false))
				}
			},
			OnCommentary: func(message string) {
				callbackMu.Lock()
				defer callbackMu.Unlock()
				if callbacksClosed {
					return
				}
				syncSteeringSegment()
				if metadataState.appendProgress(message) {
					persistMetadata(ctx, nil)
				}
			},
			OnProgress: func(message string) {
				callbackMu.Lock()
				defer callbackMu.Unlock()
				if callbacksClosed {
					return
				}
				syncSteeringSegment()
				if metadataState.appendProgress(message) {
					persistMetadata(ctx, nil)
				}
			},
			OnProvisionalText: func(string) {
				callbackMu.Lock()
				defer callbackMu.Unlock()
				if callbacksClosed {
					return
				}
				syncSteeringSegment()
				metadataState.provisional = true
				persistMetadata(ctx, nil)
			},
			OnProvisionalReset: func() {
				callbackMu.Lock()
				defer callbackMu.Unlock()
				if callbacksClosed {
					return
				}
				syncSteeringSegment()
				metadataState.provisional = false
				persistMetadata(ctx, nil)
			},
			OnStatus: func(status string) {
				callbackMu.Lock()
				defer callbackMu.Unlock()
				if callbacksClosed {
					return
				}
				syncSteeringSegment()
				metadataState.status = status
				persistMetadata(ctx, nil)
			},
			OnPlan: func(plan projectAssistantPlanSnapshot) {
				callbackMu.Lock()
				defer callbackMu.Unlock()
				if callbacksClosed {
					return
				}
				syncSteeringSegment()
				metadataState.plan = &plan
				metadataState.status = projectEinoAssistantPlanProgressStatus(plan)
				persistMetadata(ctx, nil)
			},
			OnToolCall: func(toolCall projectToolCallStreamEvent) {
				callbackMu.Lock()
				defer callbackMu.Unlock()
				if callbacksClosed {
					return
				}
				syncSteeringSegment()
				streamToolCall(toolCall)
				persistMetadata(ctx, nil)
			},
			OnAssistantEvent: emitAssistantEvent,
		},
	}
	currentRequestID := run.RequestID
	currentToolCallID := projectAssistantCheckpointToolCallID(state)
	result, err := s.projectAssistantEngine().ResumeProjectAssistant(ctx, engineReq, resumeReq, state)
	callbackMu.Lock()
	syncSteeringSegment()
	callbacksClosed = true
	assistantText := assistantContent.String()
	metadataToolCalls := append([]projectToolCallStreamEvent(nil), metadataState.toolCalls...)
	out.Progress = metadataState.progressSnapshot(time.Now().UTC(), len(projectAssistantActionFeedFromToolCalls(metadataToolCalls)) > 0)
	callbackMu.Unlock()
	persistMetadata(ctx, nil)
	if persistErr := getSnapshotErr(); persistErr != nil {
		return projectAssistantResumeResponse{}, fmt.Errorf("persist resumed assistant snapshot: %w", persistErr)
	}
	run.Audit = append([]byte(nil), resumeRun.Audit...)
	currentToolCall := projectAssistantResumeToolCall(metadataToolCalls, currentToolCallID)
	currentToolName := projectAssistantResumeToolNameWithFallback(currentToolCall, projectAssistantCheckpointToolName(state))
	out.ToolCall = currentToolCall
	out.Result = projectAssistantResumeToolResult(result.Content, currentToolCall)
	previewRefreshNeeded := s.projectAssistantPreviewRefreshNeeded(ctx, engineReq.WorkspaceScope, "", false, metadataToolCalls)
	if err != nil {
		out.AssistantContent = projectAssistantDurableFinalContent(result.Content, assistantText)
		var permissionErr *projectAssistantPermissionRequiredError
		if !errors.As(err, &permissionErr) {
			var inputErr *projectAssistantInputRequiredError
			if errors.As(err, &inputErr) {
				persistCtx, cancelPersist := detachedProjectPersistenceContext(ctx)
				defer cancelPersist()
				pendingRun, getErr := s.store.GetAssistantRun(persistCtx, messageScope, inputErr.RunID)
				if getErr != nil {
					return projectAssistantResumeResponse{}, getErr
				}
				pendingRun, err = appendProjectAssistantRunAudit(pendingRun, projectAssistantPermissionAudit{
					RequestID:  currentRequestID,
					Decision:   decision,
					Actor:      id.user,
					ToolCallID: projectAssistantResumeToolCallID(currentToolCall, currentToolCallID),
					ToolName:   currentToolName,
					Result:     out.Result,
					Error:      projectAssistantResumeToolError(currentToolCall, out.Result),
					ResolvedAt: time.Now().UTC(),
				})
				if err != nil {
					return projectAssistantResumeResponse{}, err
				}
				if err := s.saveProjectAssistantRun(persistCtx, messageScope, pendingRun); err != nil {
					return projectAssistantResumeResponse{}, err
				}
				out.RunID = pendingRun.ID
				out.RequestID = pendingRun.RequestID
				out.Status = pendingRun.Status
				out.AssistantContent = projectAssistantStoredContent(result.Content, assistantText)
				assistantMessageID := strings.TrimSpace(resumeReq.AssistantMessageID)
				appendProjectAssistantResumeResolvedUI(&out, assistantMessageID, currentRequestID, currentToolCall)
				appendProjectAssistantResumePendingUI(&out, assistantMessageID)
				appendProjectAssistantResumeDevelopmentPreviewRefreshUI(&out, previewRefreshNeeded)
				messageUpdate := out
				messageUpdate.RunID = run.ID
				messageUpdate.RequestID = currentRequestID
				if err := s.updateProjectAssistantPermissionMessage(persistCtx, messageScope, assistantMessageID, messageUpdate); err != nil {
					return projectAssistantResumeResponse{}, err
				}
				assistantMessage, err := s.resumedPendingProjectAssistantMessage(persistCtx, messageScope, assistantMessageID, assistantID, out, metadataToolCalls)
				if err != nil {
					return projectAssistantResumeResponse{}, err
				}
				out.AssistantMessage = assistantMessage
				return out, nil
			}
			return s.completeClaimedProjectAssistantRunAfterResumeError(ctx, messageScope, run, state, resumeReq, decision, id.user, out, currentToolCall, err)
		}
		persistCtx, cancelPersist := detachedProjectPersistenceContext(ctx)
		defer cancelPersist()
		pendingRun, getErr := s.store.GetAssistantRun(persistCtx, messageScope, permissionErr.RunID)
		if getErr != nil {
			return projectAssistantResumeResponse{}, getErr
		}
		pendingRun, err = appendProjectAssistantRunAudit(pendingRun, projectAssistantPermissionAudit{
			RequestID:       currentRequestID,
			Decision:        decision,
			Actor:           id.user,
			ToolCallID:      projectAssistantResumeToolCallID(currentToolCall, currentToolCallID),
			ToolName:        currentToolName,
			EditedArguments: cloneProjectAssistantToolArguments(resumeReq.EditedArguments),
			Result:          out.Result,
			Error:           projectAssistantResumeToolError(currentToolCall, out.Result),
			ResolvedAt:      time.Now().UTC(),
		})
		if err != nil {
			return projectAssistantResumeResponse{}, err
		}
		if err := s.saveProjectAssistantRun(persistCtx, messageScope, pendingRun); err != nil {
			return projectAssistantResumeResponse{}, err
		}
		out.RunID = pendingRun.ID
		out.RequestID = pendingRun.RequestID
		out.Status = pendingRun.Status
		out.AssistantContent = projectAssistantStoredContent(result.Content, assistantText)
		assistantMessageID := strings.TrimSpace(resumeReq.AssistantMessageID)
		appendProjectAssistantResumeResolvedUI(&out, assistantMessageID, currentRequestID, currentToolCall)
		appendProjectAssistantResumePendingUI(&out, assistantMessageID)
		appendProjectAssistantResumeDevelopmentPreviewRefreshUI(&out, previewRefreshNeeded)
		messageUpdate := out
		messageUpdate.RunID = run.ID
		messageUpdate.RequestID = currentRequestID
		if err := s.updateProjectAssistantPermissionMessage(persistCtx, messageScope, assistantMessageID, messageUpdate); err != nil {
			return projectAssistantResumeResponse{}, err
		}
		assistantMessage, err := s.resumedPendingProjectAssistantMessage(persistCtx, messageScope, assistantMessageID, assistantID, out, metadataToolCalls)
		if err != nil {
			return projectAssistantResumeResponse{}, err
		}
		out.AssistantMessage = assistantMessage
		return out, nil
	}

	persistCtx, cancelPersist := detachedProjectPersistenceContext(ctx)
	defer cancelPersist()
	out.Status = store.AssistantRunStatusCompleted
	resultContent := projectAssistantDurableFinalContent(result.Content, assistantText)
	streamedContent := ""
	out.AssistantContent = resultContent
	appendProjectAssistantResumeResolvedUI(&out, strings.TrimSpace(resumeReq.AssistantMessageID), currentRequestID, currentToolCall)
	appendProjectAssistantResumeDevelopmentPreviewRefreshUI(&out, previewRefreshNeeded)
	if err := s.updateProjectAssistantPermissionMessage(persistCtx, messageScope, strings.TrimSpace(resumeReq.AssistantMessageID), out); err != nil {
		return projectAssistantResumeResponse{}, err
	}
	messageMetadata := projectAssistantMessageMetadata("", metadataToolCalls)
	displayStatus := projectAssistantRunDisplayStatus(out.Status, "")
	if displayStatus != "" || out.Progress != nil {
		if messageMetadata == nil {
			messageMetadata = map[string]any{}
		}
	}
	if displayStatus != "" {
		messageMetadata[projectAssistantMetadataWorkingStatus] = displayStatus
	}
	if out.Progress != nil {
		messageMetadata[projectAssistantMetadataProgress] = *out.Progress
	}
	messageMetadata[projectAssistantMetadataVerification] = projectAssistantVerificationFromCompletionEvidence(result.CompletionEvidence)
	if assistantMessage, err := s.appendResumedProjectAssistantMessageFromContent(persistCtx, messageScope, assistantID, resultContent, streamedContent, messageMetadata); err != nil {
		return projectAssistantResumeResponse{}, err
	} else if assistantMessage != nil {
		out.AssistantMessage = assistantMessage
	}
	if strings.TrimSpace(resultContent) != "" {
		if err := appendProjectAssistantConversationMessage(
			persistCtx,
			s.store,
			messageScope,
			run.ID,
			"assistant-"+assistantID,
			projectAssistantConversationAssistant,
			chatMessage{Role: "assistant", Content: resultContent},
		); err != nil {
			return projectAssistantResumeResponse{}, fmt.Errorf("persist resumed assistant conversation item: %w", err)
		}
	}
	run.Status = out.Status
	run.UpdatedAt = time.Now().UTC()
	run, err = appendProjectAssistantRunAudit(run, projectAssistantPermissionAudit{
		RequestID:       currentRequestID,
		Decision:        decision,
		Actor:           id.user,
		ToolCallID:      projectAssistantResumeToolCallID(currentToolCall, currentToolCallID),
		ToolName:        currentToolName,
		EditedArguments: cloneProjectAssistantToolArguments(resumeReq.EditedArguments),
		Result:          out.Result,
		Error:           projectAssistantResumeToolError(currentToolCall, out.Result),
		ResolvedAt:      time.Now().UTC(),
	})
	if err != nil {
		return projectAssistantResumeResponse{}, err
	}
	if accumulator != nil {
		if err := accumulator.UpdateRun(persistCtx, func(current *store.AssistantRun) {
			current.Audit = append([]byte(nil), run.Audit...)
		}); err != nil {
			return projectAssistantResumeResponse{}, err
		}
		// The HTTP supervisor publishes the terminal status with the same
		// accumulator after this helper returns. Do not save a terminal run here:
		// that would expose it before the message metadata transition.
		return out, nil
	}
	if err := s.saveProjectAssistantRun(persistCtx, messageScope, run); err != nil {
		return projectAssistantResumeResponse{}, err
	}
	out.Status = run.Status
	return out, nil
}

func (s *Server) appendResumedProjectAssistantMessageFromContent(
	ctx context.Context,
	scope store.Scope,
	id string,
	resultContent string,
	streamedContent string,
	metadata map[string]any,
) (*aiv1alpha1.ProjectMessage, error) {
	assistantReply := projectAssistantStoredContent(resultContent, streamedContent)
	if strings.TrimSpace(assistantReply) == "" {
		return nil, nil
	}
	return s.appendResumedProjectAssistantMessage(ctx, scope, id, assistantReply, metadata)
}

func (s *Server) resumedPendingProjectAssistantMessage(
	ctx context.Context,
	scope store.Scope,
	candidateID string,
	fallbackID string,
	response projectAssistantResumeResponse,
	toolCalls []projectToolCallStreamEvent,
) (*aiv1alpha1.ProjectMessage, error) {
	seen := map[string]struct{}{}
	for _, id := range []string{candidateID, fallbackID} {
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		msg, err := s.findProjectMessage(ctx, scope, id)
		if err == nil {
			interrupt := projectAssistantUIInterruptFromMetadata(msg.Metadata[projectMessageMetadataAssistantInterrupt])
			contentMatches := strings.TrimSpace(response.AssistantContent) == "" ||
				msg.Content == response.AssistantContent
			if msg.Role == aiv1alpha1.ProjectMessageRoleAssistant &&
				contentMatches &&
				projectAssistantPermissionMessageMatchesResume(msg.Metadata, interrupt, response) {
				apiMessage := projectMessageToAPI(msg)
				return &apiMessage, nil
			}
		} else if !errors.Is(err, errProjectAssistantMessageNotFound) {
			return nil, err
		}
	}
	metadata := projectAssistantMessageMetadata(string(response.Status), toolCalls)
	if response.Progress != nil {
		if metadata == nil {
			metadata = map[string]any{}
		}
		metadata[projectAssistantMetadataProgress] = *response.Progress
	}
	return s.appendResumedProjectAssistantMessage(
		ctx,
		scope,
		fallbackID,
		response.AssistantContent,
		metadata,
	)
}

func (s *Server) completeClaimedProjectAssistantRunAfterResumeError(
	ctx context.Context,
	messageScope store.Scope,
	run store.AssistantRun,
	state projectAssistantCheckpointState,
	resumeReq projectAssistantResumeRequest,
	decision projectAssistantPermissionDecision,
	actor string,
	out projectAssistantResumeResponse,
	toolCall *projectToolCallStreamEvent,
	cause error,
) (projectAssistantResumeResponse, error) {
	if cause == nil {
		cause = errors.New("assistant resume failed")
	}
	if strings.TrimSpace(out.AssistantContent) == "" {
		out.AssistantContent = projectAssistantDurableTerminalContent("", "", cause)
	}
	persistCtx, cancelPersist := detachedProjectPersistenceContext(ctx)
	defer cancelPersist()
	failure := strings.TrimSpace(cause.Error())
	if failure == "" {
		failure = "assistant resume failed"
	}
	if strings.TrimSpace(out.Result) == "" {
		out.Result = projectAssistantResumeToolResult("", toolCall)
	}
	if strings.TrimSpace(out.Result) == "" {
		out.Result = failure
	}
	if toolCall == nil {
		toolCall = &projectToolCallStreamEvent{
			ID:     projectAssistantCheckpointToolCallID(state),
			Name:   projectAssistantCheckpointToolName(state),
			Status: "failed",
			Error:  failure,
		}
	} else {
		copy := *toolCall
		toolCall = &copy
		switch toolCall.Status {
		case "succeeded", "rejected", "failed":
		default:
			toolCall.Status = "failed"
			toolCall.Error = failure
		}
	}
	out.ToolCall = toolCall
	appendProjectAssistantResumeResolvedUI(&out, strings.TrimSpace(resumeReq.AssistantMessageID), run.RequestID, toolCall)
	now := time.Now().UTC()
	if errors.Is(cause, context.Canceled) {
		run.Status = store.AssistantRunStatusInterrupted
		run.AbortReason = store.AssistantRunAbortReasonInterrupted
		run.Error = projectAssistantRunErrorJSON(cause, "interrupted")
	} else if projectEinoAssistantIterationLimited(cause) {
		run.Status = store.AssistantRunStatusFailed
		run.AbortReason = store.AssistantRunAbortReasonIterationLimited
		run.Error = projectAssistantRunErrorJSON(cause, "max_iterations_exceeded")
	} else if projectEinoAssistantBudgetLimited(cause) {
		run.Status = store.AssistantRunStatusFailed
		run.AbortReason = store.AssistantRunAbortReasonBudgetLimited
		run.Error = projectAssistantRunErrorJSON(cause, "session_budget_exceeded")
	} else {
		run.Status = store.AssistantRunStatusFailed
		run.Error = projectAssistantRunErrorJSON(cause, projectAssistantRunErrorInfo(cause))
	}
	run.UpdatedAt = now
	updatedRun, auditErr := appendProjectAssistantRunAudit(run, projectAssistantPermissionAudit{
		RequestID:       run.RequestID,
		Decision:        decision,
		Actor:           actor,
		ToolCallID:      projectAssistantResumeToolCallID(toolCall, projectAssistantCheckpointToolCallID(state)),
		ToolName:        projectAssistantResumeToolNameWithFallback(toolCall, projectAssistantCheckpointToolName(state)),
		EditedArguments: cloneProjectAssistantToolArguments(resumeReq.EditedArguments),
		Result:          out.Result,
		Error:           failure,
		ResolvedAt:      now,
	})
	if auditErr != nil {
		return projectAssistantResumeResponse{}, auditErr
	}
	run, auditErr = recordProjectAssistantRunAuditFailure(updatedRun, cause)
	if auditErr != nil {
		return projectAssistantResumeResponse{}, auditErr
	}
	run, auditErr = finalizeProjectAssistantRunAudit(run, projectAssistantAuditOutcomeFailed, now)
	if auditErr != nil {
		return projectAssistantResumeResponse{}, auditErr
	}
	if err := s.saveProjectAssistantResumeTerminalPreparation(persistCtx, messageScope, run); err != nil {
		return projectAssistantResumeResponse{}, err
	}
	out.Status = run.Status
	if err := s.updateProjectAssistantPermissionMessage(persistCtx, messageScope, strings.TrimSpace(resumeReq.AssistantMessageID), out); err != nil {
		return projectAssistantResumeResponse{}, err
	}
	messageID := strings.TrimSpace(run.ActiveMessageID)
	if messageID == "" {
		messageID = strings.TrimSpace(resumeReq.AssistantMessageID)
	}
	if messageID != "" && strings.TrimSpace(out.AssistantContent) != "" {
		metadata := map[string]any{}
		if current, err := s.findProjectMessage(persistCtx, messageScope, messageID); err == nil {
			metadata = cloneAnyMap(current.Metadata)
			delete(metadata, projectMessageMetadataAssistantInterrupt)
		} else if !errors.Is(err, errProjectAssistantMessageNotFound) {
			return projectAssistantResumeResponse{}, err
		}
		message, err := s.appendResumedProjectAssistantMessage(
			persistCtx,
			messageScope,
			messageID,
			out.AssistantContent,
			metadata,
		)
		if err != nil {
			return projectAssistantResumeResponse{}, err
		}
		out.AssistantMessage = message
	}
	return out, cause
}

// saveProjectAssistantResumeTerminalPreparation persists the audit and
// checkpoint bookkeeping produced while unwinding a resumed segment.
func (s *Server) saveProjectAssistantResumeTerminalPreparation(ctx context.Context, scope store.Scope, run store.AssistantRun) error {
	return s.saveProjectAssistantRun(ctx, scope, run)
}

func (s *Server) appendResumedProjectAssistantMessage(
	ctx context.Context,
	scope store.Scope,
	id string,
	content string,
	metadata map[string]any,
) (*aiv1alpha1.ProjectMessage, error) {
	if accumulator := s.projectAssistantSupervisor().accumulatorForActiveMessage(scope, id); accumulator != nil {
		if err := accumulator.UpdateSnapshot(ctx, func(run *store.AssistantRun, message *store.Message) {
			message.Content = content
			next := *run
			next.Revision++
			provisional, _ := message.Metadata[projectAssistantMetadataProvisional].(bool)
			message.Metadata = projectAssistantDurableMetadataFromExisting(next, projectAssistantRunDisplayStatus(run.Status, "Working"), provisional, message.Metadata)
		}); err != nil {
			return nil, err
		}
		msg, err := s.findProjectMessage(ctx, scope, id)
		if err != nil {
			return nil, err
		}
		apiMessage := projectMessageToAPI(msg)
		return &apiMessage, nil
	}
	if err := appendProjectAssistantMessage(ctx, s.store, scope, id, content, metadata); err != nil {
		return nil, err
	}
	msg, err := s.findProjectMessage(ctx, scope, id)
	if err != nil {
		return nil, err
	}
	apiMessage := projectMessageToAPI(msg)
	return &apiMessage, nil
}

func projectAssistantToolResultError(result string) string {
	if strings.HasPrefix(result, "Tool call failed: ") {
		return strings.TrimPrefix(result, "Tool call failed: ")
	}
	return ""
}

func projectAssistantResumeToolCall(events []projectToolCallStreamEvent, id string) *projectToolCallStreamEvent {
	id = strings.TrimSpace(id)
	var fallback *projectToolCallStreamEvent
	for i := range events {
		event := events[i]
		if event.Status == "requested" || event.Status == "running" || event.Status == "permission_required" {
			continue
		}
		if fallback == nil {
			copy := event
			fallback = &copy
		}
		if id != "" && event.ID == id {
			copy := event
			return &copy
		}
	}
	if id != "" {
		return nil
	}
	return fallback
}

func projectAssistantResumeToolResult(content string, toolCall *projectToolCallStreamEvent) string {
	if toolCall == nil {
		return strings.TrimSpace(content)
	}
	if strings.TrimSpace(toolCall.Error) != "" {
		return strings.TrimSpace(toolCall.Error)
	}
	if strings.TrimSpace(toolCall.Summary) != "" {
		return strings.TrimSpace(toolCall.Summary)
	}
	return strings.TrimSpace(content)
}

func projectAssistantResumeToolError(toolCall *projectToolCallStreamEvent, result string) string {
	if toolCall != nil && strings.TrimSpace(toolCall.Error) != "" {
		return strings.TrimSpace(toolCall.Error)
	}
	return projectAssistantToolResultError(result)
}

func projectAssistantResumeToolCallID(toolCall *projectToolCallStreamEvent, fallback string) string {
	if toolCall != nil && strings.TrimSpace(toolCall.ID) != "" {
		return strings.TrimSpace(toolCall.ID)
	}
	return strings.TrimSpace(fallback)
}

func projectAssistantResumeToolName(toolCall *projectToolCallStreamEvent) string {
	if toolCall == nil {
		return ""
	}
	return strings.TrimSpace(toolCall.Name)
}

func projectAssistantResumeToolNameWithFallback(toolCall *projectToolCallStreamEvent, fallback string) string {
	if name := projectAssistantResumeToolName(toolCall); name != "" {
		return name
	}
	return strings.TrimSpace(fallback)
}

func projectAssistantCheckpointToolCallID(state projectAssistantCheckpointState) string {
	if state.Eino != nil && strings.TrimSpace(state.Eino.ToolCallID) != "" {
		return strings.TrimSpace(state.Eino.ToolCallID)
	}
	if state.CurrentIndex >= 0 && state.CurrentIndex < len(state.ToolCalls) {
		return strings.TrimSpace(state.ToolCalls[state.CurrentIndex].ID)
	}
	return ""
}

func projectAssistantCheckpointToolName(state projectAssistantCheckpointState) string {
	if state.Eino != nil && strings.TrimSpace(state.Eino.ToolName) != "" {
		return strings.TrimSpace(state.Eino.ToolName)
	}
	if state.CurrentIndex >= 0 && state.CurrentIndex < len(state.ToolCalls) {
		return strings.TrimSpace(state.ToolCalls[state.CurrentIndex].Function.Name)
	}
	return ""
}

func projectAssistantCheckpointHasStaleRepositoryBinding(state projectAssistantCheckpointState, p *aiv1alpha1.Project) bool {
	if state.CurrentIndex < 0 || state.CurrentIndex >= len(state.ToolCalls) {
		return false
	}
	tc := state.ToolCalls[state.CurrentIndex]
	if projectToolBaseName(tc.Function.Name) != projectToolCommitProjectFiles {
		return false
	}
	return strings.TrimSpace(state.ProjectRepositoryRef) != projectLinkedRepositoryRef(p)
}

func appendProjectAssistantRunAudit(run store.AssistantRun, entry projectAssistantPermissionAudit) (store.AssistantRun, error) {
	var audit projectAssistantRunAudit
	if len(run.Audit) > 0 {
		if err := json.Unmarshal(run.Audit, &audit); err != nil {
			return store.AssistantRun{}, fmt.Errorf("decode assistant run audit: %w", err)
		}
	}
	if audit.Version < projectAssistantAuditVersion {
		audit.Version = projectAssistantAuditVersion
	}
	projectAssistantAuditRefreshEffectiveSettings(&audit)
	if strings.TrimSpace(entry.Reason) == "" {
		entry.Reason = projectAssistantAuditReason(entry.Error)
	}
	if len(audit.Decisions) >= projectAssistantAuditMaxDecisions {
		audit.Decisions = append(
			[]projectAssistantPermissionAudit(nil),
			audit.Decisions[len(audit.Decisions)-projectAssistantAuditMaxDecisions+1:]...,
		)
	}
	audit.Decisions = append(audit.Decisions, entry)
	raw, err := json.Marshal(audit)
	if err != nil {
		return store.AssistantRun{}, fmt.Errorf("encode assistant run audit: %w", err)
	}
	run.Audit = raw
	return run, nil
}

func cloneProjectAssistantToolArguments(src map[string]any) map[string]any {
	if src == nil {
		return nil
	}
	dst := make(map[string]any, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

func cloneProjectAssistantToolCalls(src []chatToolCall) []chatToolCall {
	if len(src) == 0 {
		return nil
	}
	dst := make([]chatToolCall, len(src))
	for i, tc := range src {
		dst[i] = cloneChatToolCall(tc)
	}
	return dst
}

func cloneChatMessages(src []chatMessage) []chatMessage {
	if len(src) == 0 {
		return nil
	}
	dst := make([]chatMessage, len(src))
	for i, msg := range src {
		dst[i] = msg
		dst[i].ToolCalls = cloneProjectAssistantToolCalls(msg.ToolCalls)
		dst[i].Extra = projectAssistantDurableMessageExtra(msg.Extra)
	}
	return dst
}

func cloneChatToolCall(src chatToolCall) chatToolCall {
	out := src
	if len(src.ExtraContent) > 0 {
		out.ExtraContent = make(map[string]any, len(src.ExtraContent))
		for k, v := range src.ExtraContent {
			out.ExtraContent[k] = v
		}
	}
	return out
}

func cloneProjectAssistantSeenToolCalls(src map[string]int) map[string]int {
	return projectEinoAssistantSanitizeSeenToolCalls(src)
}

func projectAssistantBoundCheckpointMessages(src []chatMessage) []chatMessage {
	if len(src) == 0 {
		return nil
	}
	start := max(len(src)-projectAssistantCheckpointMaxMessages, 0)
	// Never resume from an orphaned tool response when the retention window
	// lands in the middle of an assistant tool-call group. Eino's opaque
	// checkpoint remains the exact replay authority; this projection is the
	// bounded App Studio run-state fallback used by later steering.
	for start < len(src) && src[start].Role == "tool" {
		start++
	}
	// A model-input snapshot can legitimately contain a standalone tool
	// evidence message (for example, the scrubbed preview-console record used
	// by the no-leakage boundary). Do not turn an all-tool snapshot into an
	// empty checkpoint merely because there is no assistant call to anchor it.
	if start == len(src) {
		start = len(src) - 1
	}
	bounded := cloneChatMessages(src[start:])
	for index := range bounded {
		if bounded[index].Role == "tool" {
			bounded[index].Content = projectEinoAssistantTruncateModelToolOutput(
				bounded[index].Content,
				projectEinoAssistantModelToolOutputMaxBytes,
			)
		}
	}
	return bounded
}

func cloneProjectAssistantEinoCheckpointState(src *projectAssistantEinoCheckpointState) *projectAssistantEinoCheckpointState {
	if src == nil {
		return nil
	}
	clone := *src
	clone.Checkpoint = append([]byte(nil), src.Checkpoint...)
	return &clone
}
