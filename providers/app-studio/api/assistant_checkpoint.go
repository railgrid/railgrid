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
	"time"

	"github.com/google/uuid"

	aiv1alpha1 "github.com/faroshq/provider-app-studio/apis/ai/v1alpha1"
	asclient "github.com/faroshq/provider-app-studio/client"
	"github.com/faroshq/provider-app-studio/store"
)

type projectAssistantCheckpointState struct {
	ToolCalls            []chatToolCall                       `json:"toolCalls"`
	CurrentIndex         int                                  `json:"currentIndex"`
	ProjectRepositoryRef string                               `json:"projectRepositoryRef,omitempty"`
	Messages             []chatMessage                        `json:"messages,omitempty"`
	Turn                 int                                  `json:"turn,omitempty"`
	SeenToolCalls        map[string]int                       `json:"seenToolCalls,omitempty"`
	ForceTextAnswer      bool                                 `json:"forceTextAnswer,omitempty"`
	RepeatedToolLoop     bool                                 `json:"repeatedToolLoop,omitempty"`
	LastToolMessages     []chatMessage                        `json:"lastToolMessages,omitempty"`
	Eino                 *projectAssistantEinoCheckpointState `json:"eino,omitempty"`
}

type projectAssistantEinoCheckpointState struct {
	CheckpointID string `json:"checkpointID,omitempty"`
	Checkpoint   []byte `json:"checkpoint,omitempty"`
	InterruptID  string `json:"interruptID,omitempty"`
	ToolCallID   string `json:"toolCallID,omitempty"`
	ToolName     string `json:"toolName,omitempty"`
}

type projectAssistantResumeRequest struct {
	RequestID          string         `json:"requestID"`
	Decision           string         `json:"decision"`
	AssistantMessageID string         `json:"assistantMessageID,omitempty"`
	EditedArguments    map[string]any `json:"editedArguments,omitempty"`
}

type projectAssistantResumeResponse struct {
	RunID            string                             `json:"runID"`
	RequestID        string                             `json:"requestID"`
	Status           store.AssistantRunStatus           `json:"status"`
	Decision         projectAssistantPermissionDecision `json:"decision"`
	ToolCall         *projectToolCallStreamEvent        `json:"toolCall,omitempty"`
	Permission       *projectAssistantPermission        `json:"permission,omitempty"`
	Checkpoint       *projectAssistantCheckpoint        `json:"checkpoint,omitempty"`
	Result           string                             `json:"result,omitempty"`
	AssistantMessage *aiv1alpha1.ProjectMessage         `json:"assistantMessage,omitempty"`
}

type projectAssistantRunAudit struct {
	Decisions []projectAssistantPermissionAudit `json:"decisions,omitempty"`
}

type projectAssistantPermissionAudit struct {
	RequestID       string                             `json:"requestID"`
	Decision        projectAssistantPermissionDecision `json:"decision"`
	Actor           string                             `json:"actor,omitempty"`
	ToolCallID      string                             `json:"toolCallID,omitempty"`
	ToolName        string                             `json:"toolName,omitempty"`
	EditedArguments map[string]any                     `json:"editedArguments,omitempty"`
	Result          string                             `json:"result,omitempty"`
	Error           string                             `json:"error,omitempty"`
	ResolvedAt      time.Time                          `json:"resolvedAt"`
}

func newProjectAssistantRunID() string {
	return "run-" + uuid.NewString()
}

func newProjectAssistantPermissionRequestID() string {
	return "perm-" + uuid.NewString()
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
	state.Eino = cloneProjectAssistantEinoCheckpointState(state.Eino)
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
	if err := s.store.SaveAssistantRun(ctx, req.MessageScope, run); err != nil {
		return nil, projectAssistantPermission{}, projectAssistantCheckpoint{}, err
	}

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

func projectAssistantPermissionReason(spec projectAssistantToolSpec) string {
	switch spec.Risk {
	case projectAssistantToolRiskWrite:
		return "This action will modify files in the App Studio workspace."
	case projectAssistantToolRiskCommit:
		return "This action will commit App Studio workspace changes to the linked repository."
	case projectAssistantToolRiskRuntime:
		return "This action will start a sandboxed App Studio runtime command."
	default:
		return "This action requires approval."
	}
}

func projectAssistantPermissionForCall(requestID string, tc chatToolCall, spec projectAssistantToolSpec) projectAssistantPermission {
	permissionInput := json.RawMessage(tc.Function.Arguments)
	if !json.Valid(permissionInput) {
		permissionInput = nil
	}
	return projectAssistantPermission{
		ID:         requestID,
		ToolCallID: tc.ID,
		ToolName:   spec.Name,
		Reason:     projectAssistantPermissionReason(spec),
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
	decision, err := parseProjectAssistantPermissionDecision(req.Decision)
	if err != nil {
		return projectAssistantResumeResponse{}, err
	}
	messageScope := projectMessageScope(id.orgUUID, id.workspaceUUID, p.Name)
	preflightRun, err := s.store.GetAssistantRun(ctx, messageScope, runID)
	if err != nil {
		return projectAssistantResumeResponse{}, err
	}
	if preflightRun.Status == store.AssistantRunStatusPendingPermission && preflightRun.RequestID == strings.TrimSpace(req.RequestID) {
		var preflightState projectAssistantCheckpointState
		if err := json.Unmarshal(preflightRun.Checkpoint, &preflightState); err != nil {
			return projectAssistantResumeResponse{}, fmt.Errorf("decode assistant checkpoint: %w", err)
		}
		if preflightState.Eino == nil {
			return projectAssistantResumeResponse{}, newValidationError("assistant checkpoint is not resumable")
		}
	}
	run, err := s.store.ClaimAssistantRun(ctx, messageScope, runID, strings.TrimSpace(req.RequestID), time.Now().UTC())
	if err != nil {
		if strings.Contains(err.Error(), "not waiting") || strings.Contains(err.Error(), "request id is required") {
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
	if state.Eino == nil && (state.CurrentIndex < 0 || state.CurrentIndex >= len(state.ToolCalls)) {
		return s.completeClaimedProjectAssistantRunAfterResumeError(ctx, messageScope, run, state, req, decision, id.user, out, nil, fmt.Errorf("assistant checkpoint index out of range"))
	}
	if projectAssistantCheckpointHasStaleRepositoryBinding(state, p) {
		staleBindingError := "Project repository binding changed after permission was requested"
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
		if saveErr := s.store.SaveAssistantRun(ctx, messageScope, run); saveErr != nil {
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

	assistantID := newMessageID()
	assistantContent := &strings.Builder{}
	var streamedToolCalls []projectToolCallStreamEvent
	var pendingPermissionToolCallID string
	emitAssistantEvent := func(event projectAssistantEvent) {
		switch event.Type {
		case projectAssistantEventPermissionNeeded:
			if event.Permission != nil && event.Permission.ToolCallID != "" {
				pendingPermissionToolCallID = event.Permission.ToolCallID
				out.Permission = event.Permission
				streamedToolCalls = upsertProjectToolCallStreamEvent(streamedToolCalls, projectToolCallStreamEvent{
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
					streamedToolCalls = upsertProjectToolCallStreamEvent(streamedToolCalls, projectToolCallStreamEvent{
						ID:         pendingPermissionToolCallID,
						Status:     "permission_required",
						Checkpoint: event.Checkpoint,
					})
				}
			}
		}
	}
	streamToolCall := func(toolCall projectToolCallStreamEvent) {
		if toolCall.ID == "" || toolCall.Status == "" {
			return
		}
		streamedToolCalls = upsertProjectToolCallStreamEvent(streamedToolCalls, toolCall)
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
		StreamCallbacks: projectAssistantStreamCallbacks{
			OnChunk: func(chunk string) {
				assistantContent.WriteString(chunk)
			},
			OnStatus: func(string) {},
			OnToolCall: func(toolCall projectToolCallStreamEvent) {
				streamToolCall(toolCall)
			},
			OnAssistantEvent: emitAssistantEvent,
		},
	}
	currentRequestID := run.RequestID
	currentToolCallID := strings.TrimSpace(state.Eino.ToolCallID)
	result, err := s.projectAssistantEngine().ResumeProjectAssistant(ctx, engineReq, resumeReq, state)
	currentToolCall := projectAssistantResumeToolCall(streamedToolCalls, currentToolCallID)
	out.ToolCall = currentToolCall
	out.Result = projectAssistantResumeToolResult(result.Content, currentToolCall)
	if err != nil {
		var permissionErr *projectAssistantPermissionRequiredError
		if !errors.As(err, &permissionErr) {
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
		if err := s.store.SaveAssistantRun(persistCtx, messageScope, pendingRun); err != nil {
			return projectAssistantResumeResponse{}, err
		}
		out.RunID = pendingRun.ID
		out.RequestID = pendingRun.RequestID
		out.Status = pendingRun.Status
		if err := s.updateProjectAssistantPermissionMessage(persistCtx, messageScope, strings.TrimSpace(resumeReq.AssistantMessageID), out); err != nil {
			return projectAssistantResumeResponse{}, err
		}
		return out, nil
	}

	persistCtx, cancelPersist := detachedProjectPersistenceContext(ctx)
	defer cancelPersist()
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
	if err := s.store.SaveAssistantRun(persistCtx, messageScope, run); err != nil {
		return projectAssistantResumeResponse{}, err
	}
	out.Status = run.Status
	if err := s.updateProjectAssistantPermissionMessage(persistCtx, messageScope, strings.TrimSpace(resumeReq.AssistantMessageID), out); err != nil {
		return projectAssistantResumeResponse{}, err
	}
	assistantReply := projectAssistantStoredContent(result.Content, assistantContent.String())
	if pending := projectAssistantUnstreamedContent(assistantReply, assistantContent.String()); pending != "" {
		assistantContent.WriteString(pending)
	}
	if strings.TrimSpace(assistantReply) != "" {
		assistantMessage, err := s.appendResumedProjectAssistantMessage(persistCtx, messageScope, assistantID, assistantReply, projectAssistantMessageMetadata("", streamedToolCalls))
		if err != nil {
			return projectAssistantResumeResponse{}, err
		}
		out.AssistantMessage = assistantMessage
	}
	return out, nil
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
	run = updatedRun
	if err := s.store.SaveAssistantRun(persistCtx, messageScope, run); err != nil {
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
	if run.Status != store.AssistantRunStatusPendingPermission {
		return projectAssistantResumeResponse{}, newValidationError("assistant run is not waiting for permission")
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
	if err := s.store.SaveAssistantRun(ctx, messageScope, run); err != nil {
		return projectAssistantResumeResponse{}, err
	}
	return projectAssistantResumeResponse{
		RunID:     run.ID,
		RequestID: run.RequestID,
		Status:    run.Status,
		Decision:  projectAssistantPermissionDeny,
	}, nil
}

func (s *Server) appendResumedProjectAssistantMessage(
	ctx context.Context,
	scope store.Scope,
	id string,
	content string,
	metadata map[string]any,
) (*aiv1alpha1.ProjectMessage, error) {
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
