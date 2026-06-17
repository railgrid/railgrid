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
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	aiv1alpha1 "github.com/faroshq/provider-app-studio/apis/ai/v1alpha1"
	"github.com/faroshq/provider-app-studio/store"
)

type projectAssistantCheckpointState struct {
	ToolCalls            []chatToolCall `json:"toolCalls"`
	CurrentIndex         int            `json:"currentIndex"`
	ProjectRepositoryRef string         `json:"projectRepositoryRef,omitempty"`
}

type projectAssistantResumeRequest struct {
	RequestID       string         `json:"requestID"`
	Decision        string         `json:"decision"`
	EditedArguments map[string]any `json:"editedArguments,omitempty"`
}

type projectAssistantResumeResponse struct {
	RunID      string                             `json:"runID"`
	RequestID  string                             `json:"requestID"`
	Status     store.AssistantRunStatus           `json:"status"`
	Decision   projectAssistantPermissionDecision `json:"decision"`
	ToolCall   *projectToolCallStreamEvent        `json:"toolCall,omitempty"`
	Permission *projectAssistantPermission        `json:"permission,omitempty"`
	Checkpoint *projectAssistantCheckpoint        `json:"checkpoint,omitempty"`
	Result     string                             `json:"result,omitempty"`
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

func (s *Server) saveProjectAssistantPermissionCheckpoint(
	ctx context.Context,
	req projectAssistantRunRequest,
	tool projectAssistantTool,
	tc chatToolCall,
	args map[string]any,
	projectRepositoryRef string,
) (*projectAssistantPermissionRequiredError, projectAssistantPermission, projectAssistantCheckpoint, error) {
	return s.saveProjectAssistantPermissionCheckpointForToolCalls(ctx, req, tool, []chatToolCall{tc}, 0, projectRepositoryRef)
}

func (s *Server) saveProjectAssistantPermissionCheckpointForToolCalls(
	ctx context.Context,
	req projectAssistantRunRequest,
	tool projectAssistantTool,
	toolCalls []chatToolCall,
	currentIndex int,
	projectRepositoryRef string,
) (*projectAssistantPermissionRequiredError, projectAssistantPermission, projectAssistantCheckpoint, error) {
	if s.store == nil {
		return nil, projectAssistantPermission{}, projectAssistantCheckpoint{}, fmt.Errorf("project message store not configured")
	}
	if currentIndex < 0 || currentIndex >= len(toolCalls) {
		return nil, projectAssistantPermission{}, projectAssistantCheckpoint{}, fmt.Errorf("assistant checkpoint index out of range")
	}
	spec := tool.Spec()
	runID := newProjectAssistantRunID()
	requestID := newProjectAssistantPermissionRequestID()
	now := time.Now().UTC()
	state := projectAssistantCheckpointState{
		ToolCalls:            cloneProjectAssistantToolCalls(toolCalls),
		CurrentIndex:         currentIndex,
		ProjectRepositoryRef: strings.TrimSpace(projectRepositoryRef),
	}
	raw, err := json.Marshal(state)
	if err != nil {
		return nil, projectAssistantPermission{}, projectAssistantCheckpoint{}, fmt.Errorf("encode assistant checkpoint: %w", err)
	}
	run := store.AssistantRun{
		ID:         runID,
		Status:     store.AssistantRunStatusPendingPermission,
		RequestID:  requestID,
		Checkpoint: raw,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	if err := s.store.SaveAssistantRun(ctx, req.MessageScope, run); err != nil {
		return nil, projectAssistantPermission{}, projectAssistantCheckpoint{}, err
	}

	checkpointCreatedAt := now
	permission := projectAssistantPermissionForCall(requestID, toolCalls[currentIndex], spec)
	checkpoint := projectAssistantCheckpoint{
		ID:        runID,
		Reason:    "waiting_for_permission",
		CreatedAt: &checkpointCreatedAt,
	}
	return &projectAssistantPermissionRequiredError{
		RunID:     runID,
		RequestID: requestID,
		ToolName:  spec.Name,
	}, permission, checkpoint, nil
}

func projectAssistantPermissionReason(spec projectAssistantToolSpec) string {
	switch spec.Risk {
	case projectAssistantToolRiskWrite:
		return "This action will modify files in the App Studio workspace."
	case projectAssistantToolRiskCommit:
		return "This action will commit App Studio workspace changes to the linked repository."
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

func (s *Server) resumeProjectAssistantRun(
	ctx context.Context,
	r *http.Request,
	id identity,
	p *aiv1alpha1.Project,
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
	run, err := s.store.ClaimAssistantRun(ctx, messageScope, runID, strings.TrimSpace(req.RequestID), time.Now().UTC())
	if err != nil {
		if strings.Contains(err.Error(), "not waiting") || strings.Contains(err.Error(), "request id is required") {
			return projectAssistantResumeResponse{}, newValidationError(err.Error())
		}
		return projectAssistantResumeResponse{}, err
	}
	var state projectAssistantCheckpointState
	if err := json.Unmarshal(run.Checkpoint, &state); err != nil {
		return projectAssistantResumeResponse{}, fmt.Errorf("decode assistant checkpoint: %w", err)
	}
	if state.CurrentIndex < 0 || state.CurrentIndex >= len(state.ToolCalls) {
		return projectAssistantResumeResponse{}, fmt.Errorf("assistant checkpoint index out of range")
	}

	out := projectAssistantResumeResponse{
		RunID:     run.ID,
		RequestID: run.RequestID,
		Decision:  decision,
	}
	if projectAssistantCheckpointHasStaleRepositoryBinding(state, p) {
		now := time.Now().UTC()
		run.Status = store.AssistantRunStatusCompleted
		run.UpdatedAt = now
		run, err = appendProjectAssistantRunAudit(run, projectAssistantPermissionAudit{
			RequestID:  run.RequestID,
			Decision:   decision,
			Actor:      id.user,
			ToolCallID: state.ToolCalls[state.CurrentIndex].ID,
			ToolName:   state.ToolCalls[state.CurrentIndex].Function.Name,
			Error:      "Project repository binding changed after permission was requested",
			ResolvedAt: now,
		})
		if err != nil {
			return projectAssistantResumeResponse{}, err
		}
		if saveErr := s.store.SaveAssistantRun(ctx, messageScope, run); saveErr != nil {
			return projectAssistantResumeResponse{}, saveErr
		}
		return projectAssistantResumeResponse{}, newValidationError("Project repository binding changed after permission was requested")
	}

	run, out, err = s.resolveClaimedProjectAssistantRun(ctx, r, id, p, run, state, req, decision, out)
	if err != nil {
		return projectAssistantResumeResponse{}, err
	}
	if err := s.store.SaveAssistantRun(ctx, messageScope, run); err != nil {
		return projectAssistantResumeResponse{}, err
	}
	out.Status = run.Status
	return out, nil
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

func (s *Server) resolveClaimedProjectAssistantRun(
	ctx context.Context,
	r *http.Request,
	id identity,
	p *aiv1alpha1.Project,
	run store.AssistantRun,
	state projectAssistantCheckpointState,
	req projectAssistantResumeRequest,
	decision projectAssistantPermissionDecision,
	out projectAssistantResumeResponse,
) (store.AssistantRun, projectAssistantResumeResponse, error) {
	index := state.CurrentIndex
	for index < len(state.ToolCalls) {
		tc := state.ToolCalls[index]
		currentDecision := decision
		editedArguments := req.EditedArguments
		if index != state.CurrentIndex {
			tool, ok := s.projectAssistantToolRegistry().Get(tc.Function.Name)
			if !ok {
				currentDecision = projectAssistantPermissionAllow
			} else {
				currentDecision = projectAssistantPermissionForTool(tool.Spec())
			}
			editedArguments = nil
		}
		switch currentDecision {
		case projectAssistantPermissionAllow:
			var err error
			tc, err = projectAssistantToolCallWithEditedArguments(tc, editedArguments)
			if err != nil {
				return run, out, err
			}
			result, toolCall, err := s.executeApprovedProjectAssistantToolCall(ctx, r, id, p, state.ProjectRepositoryRef, tc)
			if err != nil {
				return run, out, err
			}
			out.Result = result
			out.ToolCall = toolCall
			now := time.Now().UTC()
			run, err = appendProjectAssistantRunAudit(run, projectAssistantPermissionAudit{
				RequestID:       run.RequestID,
				Decision:        projectAssistantPermissionAllow,
				Actor:           id.user,
				ToolCallID:      tc.ID,
				ToolName:        tc.Function.Name,
				EditedArguments: cloneProjectAssistantToolArguments(editedArguments),
				Result:          result,
				Error:           projectAssistantToolResultError(result),
				ResolvedAt:      now,
			})
			if err != nil {
				return run, out, err
			}
			index++
		case projectAssistantPermissionDeny:
			msg := projectAssistantPermissionDeniedToolMessage(tc, "denied by user")
			out.Result = msg.Content
			now := time.Now().UTC()
			var err error
			run, err = appendProjectAssistantRunAudit(run, projectAssistantPermissionAudit{
				RequestID:       run.RequestID,
				Decision:        projectAssistantPermissionDeny,
				Actor:           id.user,
				ToolCallID:      tc.ID,
				ToolName:        tc.Function.Name,
				EditedArguments: cloneProjectAssistantToolArguments(editedArguments),
				Result:          msg.Content,
				ResolvedAt:      now,
			})
			if err != nil {
				return run, out, err
			}
			index++
		case projectAssistantPermissionAsk:
			tool, ok := s.projectAssistantToolRegistry().Get(tc.Function.Name)
			if !ok {
				index++
				continue
			}
			nextRun, permission, checkpoint, err := prepareProjectAssistantNextPermission(run, state, index, tool)
			if err != nil {
				return run, out, err
			}
			out.RequestID = nextRun.RequestID
			out.Status = nextRun.Status
			out.Permission = &permission
			out.Checkpoint = &checkpoint
			return nextRun, out, nil
		default:
			return run, out, newValidationError("decision must be allow or deny")
		}
	}
	run.Status = store.AssistantRunStatusCompleted
	run.UpdatedAt = time.Now().UTC()
	return run, out, nil
}

func (s *Server) executeApprovedProjectAssistantToolCall(
	ctx context.Context,
	r *http.Request,
	id identity,
	p *aiv1alpha1.Project,
	projectRepositoryRef string,
	tc chatToolCall,
) (string, *projectToolCallStreamEvent, error) {
	var streamed []projectToolCallStreamEvent
	messages, err := s.resolveProjectToolCalls(
		ctx,
		id,
		projectWorkspaceScope(id, p.Name),
		projectRepositoryRef,
		[]chatToolCall{tc},
		r,
		func(event projectToolCallStreamEvent) {
			streamed = upsertProjectToolCallStreamEvent(streamed, event)
		},
	)
	if err != nil {
		return "", nil, err
	}
	var toolCall *projectToolCallStreamEvent
	if len(streamed) > 0 {
		last := streamed[len(streamed)-1]
		toolCall = &last
	}
	var result string
	if len(messages) > 0 {
		result = messages[len(messages)-1].Content
	}
	return result, toolCall, nil
}

func prepareProjectAssistantNextPermission(
	run store.AssistantRun,
	state projectAssistantCheckpointState,
	index int,
	tool projectAssistantTool,
) (store.AssistantRun, projectAssistantPermission, projectAssistantCheckpoint, error) {
	if index < 0 || index >= len(state.ToolCalls) {
		return store.AssistantRun{}, projectAssistantPermission{}, projectAssistantCheckpoint{}, fmt.Errorf("assistant checkpoint index out of range")
	}
	requestID := newProjectAssistantPermissionRequestID()
	now := time.Now().UTC()
	state.CurrentIndex = index
	raw, err := json.Marshal(state)
	if err != nil {
		return store.AssistantRun{}, projectAssistantPermission{}, projectAssistantCheckpoint{}, fmt.Errorf("encode assistant checkpoint: %w", err)
	}
	run.Status = store.AssistantRunStatusPendingPermission
	run.RequestID = requestID
	run.Checkpoint = raw
	run.UpdatedAt = now
	createdAt := now
	return run, projectAssistantPermissionForCall(requestID, state.ToolCalls[index], tool.Spec()), projectAssistantCheckpoint{
		ID:        run.ID,
		Reason:    "waiting_for_permission",
		CreatedAt: &createdAt,
	}, nil
}

func projectAssistantToolCallWithEditedArguments(tc chatToolCall, editedArguments map[string]any) (chatToolCall, error) {
	if len(editedArguments) == 0 {
		return tc, nil
	}
	args := cloneProjectAssistantToolArguments(editedArguments)
	raw, err := json.Marshal(args)
	if err != nil {
		return chatToolCall{}, fmt.Errorf("encode edited arguments: %w", err)
	}
	tc.Function.Arguments = string(raw)
	return tc, nil
}

func projectAssistantToolResultError(result string) string {
	if strings.HasPrefix(result, "Tool call failed: ") {
		return strings.TrimPrefix(result, "Tool call failed: ")
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
	if len(src) == 0 {
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
	copy(dst, src)
	return dst
}
