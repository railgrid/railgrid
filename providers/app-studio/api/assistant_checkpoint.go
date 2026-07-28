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
	"k8s.io/klog/v2"

	aiv1alpha1 "github.com/faroshq/provider-app-studio/apis/ai/v1alpha1"
	asclient "github.com/faroshq/provider-app-studio/client"
	"github.com/faroshq/provider-app-studio/store"
)

type projectAssistantCheckpointState struct {
	AssistantMessageID        string                               `json:"assistantMessageID,omitempty"`
	ToolCalls                 []chatToolCall                       `json:"toolCalls"`
	CurrentIndex              int                                  `json:"currentIndex"`
	ProjectRepositoryRef      string                               `json:"projectRepositoryRef,omitempty"`
	TurnPolicy                projectAssistantCheckpointTurnPolicy `json:"turnPolicy"`
	Messages                  []chatMessage                        `json:"messages,omitempty"`
	Turn                      int                                  `json:"turn,omitempty"`
	SeenToolCalls             map[string]int                       `json:"seenToolCalls,omitempty"`
	ForceTextAnswer           bool                                 `json:"forceTextAnswer,omitempty"`
	RepeatedToolLoop          bool                                 `json:"repeatedToolLoop,omitempty"`
	LastToolMessages          []chatMessage                        `json:"lastToolMessages,omitempty"`
	ApprovedPlan              *projectAssistantApprovedPlan        `json:"approvedPlan,omitempty"`
	ApprovedPlanGrantRevision string                               `json:"approvedPlanGrantRevision,omitempty"`
	SessionSnapshot           *projectEinoAssistantSessionSnapshot `json:"sessionSnapshot,omitempty"`
	Eino                      *projectAssistantEinoCheckpointState `json:"eino,omitempty"`
}

type projectAssistantCheckpointTurnPolicy struct {
	Profile              projectAssistantTurnProfile `json:"profile"`
	RequiresRuntimeState bool                        `json:"requiresRuntimeState,omitempty"`
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
	RequestID          string         `json:"requestID"`
	Decision           string         `json:"decision,omitempty"`
	Answer             string         `json:"answer,omitempty"`
	AssistantMessageID string         `json:"assistantMessageID,omitempty"`
	EditedArguments    map[string]any `json:"editedArguments,omitempty"`
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
	AssistantContent string                             `json:"-"`
	Result           string                             `json:"-"`
}

func projectAssistantCheckpointActiveMessageID(state projectAssistantCheckpointState) string {
	return strings.TrimSpace(state.AssistantMessageID)
}

func projectAssistantPausedRunInterruptMatchesMessage(run store.AssistantRun, message store.Message) bool {
	if message.Role != aiv1alpha1.ProjectMessageRoleAssistant {
		return false
	}
	if messageRunID, _ := message.Metadata[projectAssistantMetadataRunID].(string); messageRunID != "" && messageRunID != run.ID {
		return false
	}
	interrupt := projectAssistantUIInterruptFromMetadata(message.Metadata[projectMessageMetadataAssistantInterrupt])
	return interrupt != nil && interrupt.Action != nil && interrupt.Status == "pending" &&
		interrupt.Action.RunID == run.ID && interrupt.Action.RequestID == run.RequestID &&
		(interrupt.Action.AssistantMessageID == "" || interrupt.Action.AssistantMessageID == message.ID)
}

func (s *Server) findProjectAssistantPausedRunMessageByInterrupt(ctx context.Context, scope store.Scope, run store.AssistantRun) (store.Message, bool, error) {
	var candidate *store.Message
	cursor := ""
	for {
		page, err := s.store.ListMessages(ctx, scope, 250, cursor)
		if err != nil {
			return store.Message{}, false, err
		}
		for _, message := range page.Items {
			if !projectAssistantPausedRunInterruptMatchesMessage(run, message) {
				continue
			}
			if candidate != nil {
				return store.Message{}, false, nil
			}
			candidateMessage := message
			candidate = &candidateMessage
		}
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}
	if candidate == nil {
		return store.Message{}, false, nil
	}
	return *candidate, true, nil
}

// normalizeProjectAssistantPausedRun repairs legacy paused rows that predate
// durable message identity columns. The checkpoint is the authoritative source
// for its assistant message; do not infer a message by chronology or content.
// For checkpoints written before that field existed, a resume request may name
// the message only when its persisted interrupt binds the exact run and request.
func (s *Server) normalizeProjectAssistantPausedRun(
	ctx context.Context,
	scope store.Scope,
	run store.AssistantRun,
	resumeAssistantMessageID string,
) (store.AssistantRun, error) {
	if s == nil || s.store == nil || run.ActiveMessageID != "" ||
		(run.Status != store.AssistantRunStatusPendingPermission && run.Status != store.AssistantRunStatusPendingInput) {
		return run, nil
	}
	var checkpoint projectAssistantCheckpointState
	if err := json.Unmarshal(run.Checkpoint, &checkpoint); err != nil {
		return run, fmt.Errorf("decode assistant checkpoint for run normalization: %w", err)
	}
	assistantMessageID := projectAssistantCheckpointActiveMessageID(checkpoint)
	fromCheckpoint := assistantMessageID != ""
	var message store.Message
	if !fromCheckpoint {
		assistantMessageID = strings.TrimSpace(resumeAssistantMessageID)
		if assistantMessageID != "" {
			candidate, err := s.findProjectMessage(ctx, scope, assistantMessageID)
			if err != nil || !projectAssistantPausedRunInterruptMatchesMessage(run, candidate) {
				return run, nil
			}
			message = candidate
		} else {
			candidate, found, err := s.findProjectAssistantPausedRunMessageByInterrupt(ctx, scope, run)
			if err != nil {
				return run, fmt.Errorf("find interrupt-bound assistant message: %w", err)
			}
			if !found {
				return run, nil
			}
			assistantMessageID = candidate.ID
			message = candidate
		}
	}
	if fromCheckpoint {
		var err error
		message, err = s.findProjectMessage(ctx, scope, assistantMessageID)
		if err != nil {
			return run, fmt.Errorf("find checkpoint assistant message: %w", err)
		}
	}
	if message.Role != aiv1alpha1.ProjectMessageRoleAssistant {
		return run, fmt.Errorf("checkpoint assistant message %q is not an assistant message", assistantMessageID)
	}
	if messageRunID, _ := message.Metadata[projectAssistantMetadataRunID].(string); messageRunID != "" && messageRunID != run.ID {
		return run, fmt.Errorf("checkpoint assistant message %q belongs to run %q", assistantMessageID, messageRunID)
	}
	run.ActiveMessageID = assistantMessageID
	if err := s.store.SaveAssistantRun(ctx, scope, run); err != nil {
		return run, fmt.Errorf("save normalized assistant run: %w", err)
	}
	return run, nil
}

func (s *Server) saveProjectAssistantRun(ctx context.Context, scope store.Scope, run store.AssistantRun) error {
	if accumulator := s.projectAssistantSupervisor().accumulatorFor(scope, run.ID); accumulator != nil {
		return accumulator.UpdateRun(ctx, func(current *store.AssistantRun) {
			current.Status = run.Status
			current.RequestID = run.RequestID
			current.Checkpoint = run.Checkpoint
			current.Audit = run.Audit
		})
	}
	return s.store.SaveAssistantRun(ctx, scope, run)
}

type projectAssistantRunAudit struct {
	Version          int                               `json:"version,omitempty"`
	Provider         string                            `json:"provider,omitempty"`
	Model            string                            `json:"model,omitempty"`
	Profile          projectAssistantTurnProfile       `json:"profile,omitempty"`
	StartedAt        time.Time                         `json:"startedAt,omitempty"`
	PhaseTransitions []projectAssistantAuditPhase      `json:"phaseTransitions,omitempty"`
	Tools            []projectAssistantAuditTool       `json:"tools,omitempty"`
	Outcome          projectAssistantAuditOutcome      `json:"outcome,omitempty"`
	DurationMS       int64                             `json:"durationMs,omitempty"`
	Decisions        []projectAssistantPermissionAudit `json:"decisions,omitempty"`
}

type projectAssistantPermissionAudit struct {
	RequestID       string                             `json:"requestID"`
	Decision        projectAssistantPermissionDecision `json:"decision"`
	Actor           string                             `json:"actor,omitempty"`
	ToolCallID      string                             `json:"toolCallID,omitempty"`
	ToolName        string                             `json:"toolName,omitempty"`
	Reason          string                             `json:"reason,omitempty"`
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
	if req.snapshotAccumulator != nil {
		if err := req.snapshotAccumulator.UpdateRun(ctx, func(current *store.AssistantRun) {
			current.Status, current.RequestID, current.Checkpoint = run.Status, run.RequestID, run.Checkpoint
		}); err != nil {
			return nil, projectAssistantPermission{}, projectAssistantCheckpoint{}, err
		}
	} else if err := s.saveProjectAssistantRun(ctx, req.MessageScope, run); err != nil {
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
	if req.snapshotAccumulator != nil {
		if err := req.snapshotAccumulator.UpdateRun(ctx, func(current *store.AssistantRun) {
			current.Status, current.RequestID, current.Checkpoint = run.Status, run.RequestID, run.Checkpoint
		}); err != nil {
			return nil, projectAssistantFollowUp{}, projectAssistantCheckpoint{}, err
		}
	} else if err := s.saveProjectAssistantRun(ctx, req.MessageScope, run); err != nil {
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
	return permission
}

func projectAssistantFollowUpForEinoInterrupt(requestID string, info *projectEinoFollowUpInterruptInfo) projectAssistantFollowUp {
	if info == nil {
		return projectAssistantFollowUp{ID: requestID}
	}
	questions := normalizeProjectAssistantStringList(info.Questions)
	return projectAssistantFollowUp{
		ID:         requestID,
		ToolCallID: strings.TrimSpace(info.ToolCallID),
		Questions:  questions,
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
		if projectAssistantDirectApprovalGrantsWritePlan(spec.Name) {
			if target, err := projectAssistantWriteTargetPath(spec.Name, args); err == nil {
				return fmt.Sprintf(
					"Allow App Studio to create or modify %q using workspace edit tools until the next commit request.",
					target,
				)
			}
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
	return projectAssistantPermission{
		ID:         requestID,
		ToolCallID: tc.ID,
		ToolName:   spec.Name,
		Reason:     projectAssistantPermissionReasonForArguments(spec, args),
		Input:      permissionInput,
	}
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
	messageScope := projectMessageScope(id.orgUUID, id.workspaceUUID, p.Name)
	preflightRun, err := s.store.GetAssistantRun(ctx, messageScope, runID)
	if err != nil {
		return projectAssistantResumeResponse{}, err
	}
	preflightRun, err = s.normalizeProjectAssistantPausedRun(ctx, messageScope, preflightRun, req.AssistantMessageID)
	if err != nil {
		return projectAssistantResumeResponse{}, err
	}
	var preflightState projectAssistantCheckpointState
	if err := json.Unmarshal(preflightRun.Checkpoint, &preflightState); err != nil {
		return projectAssistantResumeResponse{}, fmt.Errorf("decode assistant checkpoint: %w", err)
	}
	if (preflightRun.Status == store.AssistantRunStatusPendingPermission || preflightRun.Status == store.AssistantRunStatusPendingInput) && preflightRun.RequestID == strings.TrimSpace(req.RequestID) {
		if preflightState.Eino == nil || strings.TrimSpace(preflightState.Eino.InterruptType) == "" {
			return projectAssistantResumeResponse{}, newValidationError("assistant checkpoint is not resumable")
		}
		switch {
		case preflightRun.Status == store.AssistantRunStatusPendingPermission && !projectAssistantEinoInterruptTypeIsPermission(preflightState.Eino.InterruptType):
			return projectAssistantResumeResponse{}, newValidationError("assistant checkpoint is not resumable")
		case preflightRun.Status == store.AssistantRunStatusPendingInput && preflightState.Eino.InterruptType != projectAssistantInterruptTypeFollowUp:
			return projectAssistantResumeResponse{}, newValidationError("assistant checkpoint is not resumable")
		}
	}
	var decision projectAssistantPermissionDecision
	if preflightState.Eino != nil && preflightState.Eino.InterruptType == projectAssistantInterruptTypeFollowUp {
		if strings.TrimSpace(req.Answer) == "" {
			return projectAssistantResumeResponse{}, newValidationError("answer is required")
		}
		decision = ""
	} else {
		decision, err = parseProjectAssistantPermissionDecision(req.Decision)
		if err != nil {
			return projectAssistantResumeResponse{}, err
		}
	}
	var run store.AssistantRun
	if accumulator := s.projectAssistantSupervisor().accumulatorFor(messageScope, runID); accumulator != nil {
		run, err = accumulator.ClaimPending(ctx, strings.TrimSpace(req.RequestID))
	} else {
		// Legacy direct callers do not have a server-owned worker. HTTP resume
		// always starts through the supervisor and therefore takes the serialized
		// branch above.
		run, err = s.store.ClaimAssistantRun(ctx, messageScope, runID, strings.TrimSpace(req.RequestID), time.Now().UTC())
	}
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
		run.Status = store.AssistantRunStatusCompleted
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
		if saveErr := s.saveProjectAssistantRun(ctx, messageScope, run); saveErr != nil {
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
	messageScope := projectMessageScope(id.orgUUID, id.workspaceUUID, p.Name)
	turn := newProjectAssistantTurnItem(projectAssistantTurnResume, id, p.Name)
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
	settings, err := readProjectLLMSettings(ctx, c)
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
	metadataState := &projectAssistantDurableMetadataState{status: "Working"}
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
	persistMetadata := func(ctx context.Context, runStatus *store.AssistantRunStatus) {
		if accumulator != nil {
			recordSnapshotErr(s.persistProjectAssistantDurableMetadata(ctx, accumulator, projectWorkspaceScope(id, p.Name), metadataState, runStatus))
		}
	}
	var pendingPermissionToolCallID string
	var pendingFollowUpToolCallID string
	emitAssistantEvent := func(event projectAssistantEvent) {
		switch event.Type {
		case projectAssistantEventPermissionNeeded:
			if event.Permission != nil && event.Permission.ToolCallID != "" {
				pendingPermissionToolCallID = event.Permission.ToolCallID
				out.Permission = event.Permission
				metadataState.toolCalls = upsertProjectToolCallStreamEvent(metadataState.toolCalls, projectToolCallStreamEvent{
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
					metadataState.toolCalls = upsertProjectToolCallStreamEvent(metadataState.toolCalls, projectToolCallStreamEvent{
						ID:         pendingPermissionToolCallID,
						Status:     "permission_required",
						Checkpoint: event.Checkpoint,
					})
				}
				if pendingFollowUpToolCallID != "" {
					metadataState.toolCalls = upsertProjectToolCallStreamEvent(metadataState.toolCalls, projectToolCallStreamEvent{
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
				metadataState.toolCalls = upsertProjectToolCallStreamEvent(metadataState.toolCalls, projectToolCallStreamEvent{
					ID:       event.FollowUp.ToolCallID,
					Name:     projectToolAskFollowUp,
					Status:   "input_required",
					Summary:  event.FollowUp.Prompt,
					FollowUp: event.FollowUp,
				})
			}
		}
		persistMetadata(ctx, nil)
	}
	streamToolCall := func(toolCall projectToolCallStreamEvent) {
		if toolCall.ID == "" || toolCall.Status == "" {
			return
		}
		metadataState.toolCalls = upsertProjectToolCallStreamEvent(metadataState.toolCalls, toolCall)
	}
	resumeRun := run
	engineReq := projectAssistantRunRequest{
		Identity:                 id,
		HTTPRequest:              r,
		Client:                   c,
		Project:                  p,
		Repository:               repository,
		WorkspaceScope:           projectWorkspaceScope(id, p.Name),
		Workspace:                s.workspaces,
		MessageScope:             messageScope,
		LLM:                      settings,
		MCPBaseURL:               s.hubBase,
		MCPInsecureSkipTLSVerify: s.mcpInsecureSkipTLSVerify,
		AutoApproveActions:       s.autoApproveAssistantActions(),
		Continuation:             &state,
		AssistantRun:             &resumeRun,
		snapshotAccumulator:      accumulator,
		StreamCallbacks: projectAssistantStreamCallbacks{
			OnChunk: func(chunk string) {
				assistantContent.WriteString(chunk)
				if accumulator != nil {
					recordSnapshotErr(accumulator.UpdateText(ctx, assistantContent.String(), false))
				}
			},
			OnProvisionalText:  func(string) { metadataState.provisional = true; persistMetadata(ctx, nil) },
			OnProvisionalReset: func() { metadataState.provisional = false; persistMetadata(ctx, nil) },
			OnStatus:           func(status string) { metadataState.status = status; persistMetadata(ctx, nil) },
			OnPlan: func(plan projectAssistantPlanSnapshot) {
				metadataState.plan = &plan
				persistMetadata(ctx, nil)
			},
			OnToolCall: func(toolCall projectToolCallStreamEvent) {
				streamToolCall(toolCall)
				persistMetadata(ctx, nil)
			},
			OnAssistantEvent: emitAssistantEvent,
		},
	}
	// A free-text resume answer is a fresh user instruction ("go for it",
	// "fix the login") — re-route it so the engine can escalate the run's
	// tool policy. Without this, a run that began as a discussion question
	// restores its toolless policy from the checkpoint on every resume, and
	// the model can only keep asking for access it will never get. Routing
	// failures fall through to the checkpoint policy (no escalation).
	if answer := strings.TrimSpace(resumeReq.Answer); answer != "" {
		if history, histErr := s.store.LoadRecentMessages(ctx, messageScope, 24); histErr == nil {
			// The answer is not persisted as a message yet — append it so the
			// router classifies the instruction, not the stale history.
			history = append(history, store.Message{Role: aiv1alpha1.ProjectMessageRoleUser, Content: answer})
			if turnDecision, routeErr := s.projectAssistantTurnRouter()(ctx, projectAssistantTurnRouteRequest{LLM: settings, History: history}); routeErr == nil {
				engineReq.TurnPolicy = projectAssistantTurnPolicyForDecision(turnDecision)
				engineReq.TurnProfile = engineReq.TurnPolicy.profile
				klog.FromContext(ctx).V(2).Info("assistant resume re-route",
					"project", p.Name, "profile", turnDecision.Profile, "confidence", turnDecision.Confidence,
					"mutation", turnDecision.RequestsMutation)
			}
		}
	}
	currentRequestID := run.RequestID
	currentToolCallID := strings.TrimSpace(state.Eino.ToolCallID)
	result, err := s.projectAssistantEngine().ResumeProjectAssistant(ctx, engineReq, resumeReq, state)
	if persistErr := getSnapshotErr(); persistErr != nil {
		return projectAssistantResumeResponse{}, fmt.Errorf("persist resumed assistant snapshot: %w", persistErr)
	}
	run.Audit = append([]byte(nil), resumeRun.Audit...)
	currentToolCall := projectAssistantResumeToolCall(metadataState.toolCalls, currentToolCallID)
	out.ToolCall = currentToolCall
	out.Result = projectAssistantResumeToolResult(result.Content, currentToolCall)
	previewRefreshNeeded := s.projectAssistantPreviewRefreshNeeded(ctx, engineReq.WorkspaceScope, "", false, metadataState.toolCalls)
	if err != nil {
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
					ToolName:   projectAssistantResumeToolName(currentToolCall),
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
				out.AssistantContent = projectAssistantStoredContent(result.Content, assistantContent.String())
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
				if strings.TrimSpace(out.AssistantContent) != "" {
					assistantMessage, err := s.resumedPendingProjectAssistantMessage(persistCtx, messageScope, assistantMessageID, assistantID, out, metadataState.toolCalls)
					if err != nil {
						return projectAssistantResumeResponse{}, err
					}
					out.AssistantMessage = assistantMessage
				}
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
			ToolName:        projectAssistantResumeToolName(currentToolCall),
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
		out.AssistantContent = projectAssistantStoredContent(result.Content, assistantContent.String())
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
		if strings.TrimSpace(out.AssistantContent) != "" {
			assistantMessage, err := s.resumedPendingProjectAssistantMessage(persistCtx, messageScope, assistantMessageID, assistantID, out, metadataState.toolCalls)
			if err != nil {
				return projectAssistantResumeResponse{}, err
			}
			out.AssistantMessage = assistantMessage
		}
		return out, nil
	}

	persistCtx, cancelPersist := detachedProjectPersistenceContext(ctx)
	defer cancelPersist()
	out.Status = store.AssistantRunStatusCompleted
	appendProjectAssistantResumeResolvedUI(&out, strings.TrimSpace(resumeReq.AssistantMessageID), currentRequestID, currentToolCall)
	appendProjectAssistantResumeDevelopmentPreviewRefreshUI(&out, previewRefreshNeeded)
	if err := s.updateProjectAssistantPermissionMessage(persistCtx, messageScope, strings.TrimSpace(resumeReq.AssistantMessageID), out); err != nil {
		return projectAssistantResumeResponse{}, err
	}
	if assistantMessage, err := s.appendResumedProjectAssistantMessageFromContent(persistCtx, messageScope, assistantID, result.Content, assistantContent.String(), projectAssistantMessageMetadata("", metadataState.toolCalls)); err != nil {
		return projectAssistantResumeResponse{}, err
	} else if assistantMessage != nil {
		out.AssistantMessage = assistantMessage
	}
	run.Status = store.AssistantRunStatusCompleted
	run.UpdatedAt = time.Now().UTC()
	run, err = appendProjectAssistantRunAudit(run, projectAssistantPermissionAudit{
		RequestID:       currentRequestID,
		Decision:        decision,
		Actor:           id.user,
		ToolCallID:      projectAssistantResumeToolCallID(currentToolCall, currentToolCallID),
		ToolName:        projectAssistantResumeToolName(currentToolCall),
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
		// The HTTP supervisor publishes the completed status with the same
		// accumulator after this helper returns. Do not save a terminal run here:
		// that would expose completion before the message metadata transition.
		out.Status = store.AssistantRunStatusCompleted
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
	if strings.TrimSpace(response.AssistantContent) == "" {
		return nil, nil
	}
	if candidateID != "" {
		msg, err := s.findProjectMessage(ctx, scope, candidateID)
		if err == nil {
			interrupt := projectAssistantUIInterruptFromMetadata(msg.Metadata[projectMessageMetadataAssistantInterrupt])
			if msg.Role == aiv1alpha1.ProjectMessageRoleAssistant && msg.Content == response.AssistantContent && projectAssistantPermissionMessageMatchesResume(msg.Metadata, interrupt, response) {
				apiMessage := projectMessageToAPI(msg)
				return &apiMessage, nil
			}
		} else if !errors.Is(err, errProjectAssistantMessageNotFound) {
			return nil, err
		}
	}
	return s.appendResumedProjectAssistantMessage(ctx, scope, fallbackID, response.AssistantContent, projectAssistantMessageMetadata(string(response.Status), toolCalls))
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
	run.Status = store.AssistantRunStatusCompleted
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
	run, auditErr = finalizeProjectAssistantRunAudit(updatedRun, projectAssistantAuditOutcomeFailed, now)
	if auditErr != nil {
		return projectAssistantResumeResponse{}, auditErr
	}
	if err := s.saveProjectAssistantRun(persistCtx, messageScope, run); err != nil {
		return projectAssistantResumeResponse{}, err
	}
	out.Status = run.Status
	if err := s.updateProjectAssistantPermissionMessage(persistCtx, messageScope, strings.TrimSpace(resumeReq.AssistantMessageID), out); err != nil {
		return projectAssistantResumeResponse{}, err
	}
	return out, cause
}

func (s *Server) abortProjectAssistantRun(
	ctx context.Context,
	id identity,
	p *aiv1alpha1.Project,
	runID string,
) (projectAssistantResumeResponse, error) {
	if s.store == nil {
		return projectAssistantResumeResponse{}, fmt.Errorf("project message store not configured")
	}
	if p == nil || strings.TrimSpace(p.Name) == "" {
		return projectAssistantResumeResponse{}, fmt.Errorf("project is required")
	}
	messageScope := projectMessageScope(id.orgUUID, id.workspaceUUID, p.Name)
	run, err := s.store.GetAssistantRun(ctx, messageScope, runID)
	if err != nil {
		return projectAssistantResumeResponse{}, err
	}
	run, err = s.normalizeProjectAssistantPausedRun(ctx, messageScope, run, "")
	if err != nil {
		return projectAssistantResumeResponse{}, err
	}
	switch run.Status {
	case store.AssistantRunStatusPendingPermission, store.AssistantRunStatusPendingInput:
	default:
		return projectAssistantResumeResponse{}, newValidationError("assistant run is not waiting for input")
	}
	if err := s.clearProjectAssistantApprovedPlan(ctx, messageScope); err != nil {
		return projectAssistantResumeResponse{}, fmt.Errorf("revoke App Studio workspace grant before abort: %w", err)
	}
	now := time.Now().UTC()
	run, err = s.store.ClaimAssistantRun(ctx, messageScope, run.ID, run.RequestID, now)
	if err != nil {
		if strings.Contains(err.Error(), "not waiting") {
			return projectAssistantResumeResponse{}, newValidationError(err.Error())
		}
		return projectAssistantResumeResponse{}, err
	}
	run.Status = store.AssistantRunStatusAborted
	run.UpdatedAt = now
	run, err = finalizeProjectAssistantRunAudit(run, projectAssistantAuditOutcomeAborted, now)
	if err != nil {
		return projectAssistantResumeResponse{}, err
	}
	run, err = appendProjectAssistantRunAudit(run, projectAssistantPermissionAudit{
		RequestID:  run.RequestID,
		Decision:   projectAssistantPermissionDeny,
		Actor:      id.user,
		Error:      "aborted by user",
		ResolvedAt: now,
	})
	if err != nil {
		return projectAssistantResumeResponse{}, err
	}
	if err := s.saveProjectAssistantRun(ctx, messageScope, run); err != nil {
		return projectAssistantResumeResponse{}, err
	}
	if err := s.clearProjectAssistantPendingMessagesForRun(ctx, messageScope, run.ID); err != nil {
		return projectAssistantResumeResponse{}, err
	}
	return projectAssistantResumeResponse{
		RunID:     run.ID,
		RequestID: run.RequestID,
		Status:    run.Status,
		Decision:  projectAssistantPermissionDeny,
	}, nil
}

func (s *Server) clearProjectAssistantPendingMessagesForRun(ctx context.Context, scope store.Scope, runID string) error {
	if s == nil || s.store == nil || strings.TrimSpace(runID) == "" {
		return nil
	}
	cursor := ""
	for {
		page, err := s.store.ListMessages(ctx, scope, 250, cursor)
		if err != nil {
			return err
		}
		for _, msg := range page.Items {
			if msg.Role != aiv1alpha1.ProjectMessageRoleAssistant {
				continue
			}
			interrupt := projectAssistantUIInterruptFromMetadata(msg.Metadata[projectMessageMetadataAssistantInterrupt])
			if interrupt == nil || interrupt.Action == nil || interrupt.Action.RunID != runID {
				continue
			}
			response := projectAssistantResumeResponse{
				RunID:     runID,
				RequestID: interrupt.Action.RequestID,
				Status:    store.AssistantRunStatusAborted,
				Decision:  projectAssistantPermissionDeny,
			}
			if err := s.updateProjectAssistantPermissionMessage(ctx, scope, msg.ID, response); err != nil {
				return err
			}
		}
		if page.NextCursor == "" {
			return nil
		}
		cursor = page.NextCursor
	}
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
	if audit.Version == 0 {
		audit.Version = projectAssistantAuditVersion
	}
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
	if len(src) == 0 {
		return nil
	}
	dst := make(map[string]int, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

func cloneProjectAssistantEinoCheckpointState(src *projectAssistantEinoCheckpointState) *projectAssistantEinoCheckpointState {
	if src == nil {
		return nil
	}
	clone := *src
	clone.Checkpoint = append([]byte(nil), src.Checkpoint...)
	return &clone
}
