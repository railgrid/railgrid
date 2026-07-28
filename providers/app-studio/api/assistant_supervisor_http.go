// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	aiv1alpha1 "github.com/faroshq/provider-app-studio/apis/ai/v1alpha1"
	asclient "github.com/faroshq/provider-app-studio/client"
	"github.com/faroshq/provider-app-studio/store"
	"github.com/faroshq/provider-app-studio/workspace"
	"github.com/google/uuid"
	"github.com/gorilla/mux"
)

type projectAssistantRunStartResponse struct {
	Run       store.AssistantRun         `json:"run"`
	User      *aiv1alpha1.ProjectMessage `json:"user,omitempty"`
	Assistant aiv1alpha1.ProjectMessage  `json:"assistant"`
}

type projectAssistantDurableStartResult struct {
	Run       store.AssistantRun
	User      store.Message
	Assistant store.Message
	Started   bool
}

// projectAssistantDurableFinalContent makes the engine's returned response
// authoritative when present. Chunk callbacks are progressive UI snapshots;
// they can be empty or partial and must never truncate or duplicate the final
// durable assistant message.
func projectAssistantDurableFinalContent(reply, streamed string) string {
	return projectAssistantStoredContent(reply, streamed)
}

// startProjectAssistantRunDurably is the one start boundary for every new
// conversation turn. It validates its durable inputs, reserves the project,
// atomically creates the user message, assistant placeholder and run, then
// hands the run to a server-owned worker. It deliberately accepts no response
// writer and never derives execution from the caller's request context.
func (s *Server) startProjectAssistantRunDurably(ctx context.Context, scope store.Scope, content, clientRequestID string, start func(store.AssistantRun, store.Message, bool) error) (projectAssistantDurableStartResult, error) {
	content = strings.TrimSpace(content)
	clientRequestID = strings.TrimSpace(clientRequestID)
	if content == "" || clientRequestID == "" {
		return projectAssistantDurableStartResult{}, newValidationError("content and clientRequestID are required")
	}
	if err := s.reconcileOrphanedProjectAssistantRun(ctx, scope); err != nil {
		return projectAssistantDurableStartResult{}, err
	}
	if prior, err := s.store.FindAssistantRunByClientRequestID(ctx, scope, clientRequestID); err == nil {
		return projectAssistantDurableStartResult{Run: prior}, nil
	} else if !errors.Is(err, store.ErrAssistantRunNotFound) {
		return projectAssistantDurableStartResult{}, err
	}
	supervisor := s.projectAssistantSupervisor()
	releaseReservation, err := supervisor.Reserve(scope)
	if err != nil {
		return projectAssistantDurableStartResult{}, err
	}
	defer releaseReservation()
	messages, err := s.store.ListMessages(ctx, scope, 1, "")
	if err != nil {
		return projectAssistantDurableStartResult{}, err
	}
	transcriptEmpty := len(messages.Items) == 0
	now := time.Now().UTC()
	user := store.Message{ID: newMessageID(), Role: aiv1alpha1.ProjectMessageRoleUser, Content: content, CreatedAt: now, UpdatedAt: now}
	assistant := store.Message{ID: newMessageID(), Role: aiv1alpha1.ProjectMessageRoleAssistant, CreatedAt: now, UpdatedAt: now}
	run := store.AssistantRun{ID: "run-" + uuid.NewString(), Status: store.AssistantRunStatusRunning, ClientRequestID: clientRequestID, UserMessageID: user.ID, ActiveMessageID: assistant.ID, Revision: 1, CreatedAt: now, UpdatedAt: now}
	assistant.Metadata = projectAssistantDurableMetadataForTransition(run, "Working", false, false, nil, nil)
	created, err := s.store.CreateAssistantRun(ctx, scope, user, assistant, run)
	if err != nil {
		return projectAssistantDurableStartResult{}, err
	}
	if err := start(created, assistant, transcriptEmpty); err != nil {
		return projectAssistantDurableStartResult{Run: created, User: user, Assistant: assistant}, err
	}
	return projectAssistantDurableStartResult{Run: created, User: user, Assistant: assistant, Started: true}, nil
}

// projectAssistantLegacyStreamEvents adapts a complete durable snapshot to the
// historic UI event protocol.  It deliberately emits a replacement of the
// assistant body (rather than a token delta) because durable subscriptions are
// revision snapshots, not a replay log.
func projectAssistantLegacyStreamEvents(snapshot projectAssistantRunSnapshot) []projectMessageStreamEvent {
	return newProjectAssistantLegacyStreamAdapter().Events(snapshot)
}

// projectAssistantLegacyStreamAdapter translates durable, complete revisions
// into the historical UI stream. It is deliberately stateful: the legacy
// renderer needs stable component IDs, while the durable stream is a latest
// snapshot protocol rather than an append-only token log.
type projectAssistantLegacyStreamAdapter struct {
	writer       *projectAssistantStreamWriter
	lastRevision int64
	previewSent  bool
	terminalSent bool
}

func newProjectAssistantLegacyStreamAdapter() *projectAssistantLegacyStreamAdapter {
	return &projectAssistantLegacyStreamAdapter{}
}

func (a *projectAssistantLegacyStreamAdapter) Events(snapshot projectAssistantRunSnapshot) []projectMessageStreamEvent {
	if a == nil || snapshot.Message.ID == "" || snapshot.Run.Revision <= a.lastRevision {
		return nil
	}
	if a.writer == nil {
		a.writer = &projectAssistantStreamWriter{assistantID: snapshot.Message.ID}
	}
	if a.writer.assistantID != snapshot.Message.ID {
		return nil
	}
	events := make([]projectMessageStreamEvent, 0, 8)
	a.writer.write = func(event projectMessageStreamEvent) error {
		events = append(events, event)
		return nil
	}
	ctx := context.Background()
	_ = a.writer.WriteDurableAssistantContent(ctx, snapshot.Message.Content)
	if status, _ := snapshot.Message.Metadata[projectAssistantMetadataWorkingStatus].(string); status != "" {
		_ = a.writer.EmitProjectAssistantEvent(ctx, projectAssistantEvent{Type: projectAssistantEventStatus, Status: status})
	}
	if interrupt := projectAssistantUIInterruptFromMetadata(snapshot.Message.Metadata[projectMessageMetadataAssistantInterrupt]); interrupt != nil {
		_ = a.writer.writeAssistantUI(ctx, projectAssistantUIEvent{InterruptRequest: interrupt})
	}
	if preview, _ := snapshot.Message.Metadata[projectAssistantMetadataPreviewRefreshNeeded].(bool); preview && !a.previewSent {
		_ = a.writer.write(projectMessageStreamEventFromUI(projectAssistantUIDevelopmentPreviewRefreshEvent()))
		a.previewSent = true
	}
	if !a.terminalSent {
		switch snapshot.Run.Status {
		case store.AssistantRunStatusCompleted:
			_ = a.writer.EmitProjectAssistantEvent(ctx, projectAssistantEvent{Type: projectAssistantEventRunFinished})
			a.terminalSent = true
		case store.AssistantRunStatusFailed, store.AssistantRunStatusInterrupted, store.AssistantRunStatusAborted:
			_ = a.writer.EmitProjectAssistantEvent(ctx, projectAssistantEvent{Type: projectAssistantEventRunFailed, Error: projectAssistantRunDisplayStatus(snapshot.Run.Status, "Failed")})
			a.terminalSent = true
		}
	}
	a.lastRevision = snapshot.Run.Revision
	return events
}

type projectAssistantSupervisorRunContextKey struct{}

const (
	projectAssistantMetadataRunID                = "assistantRunID"
	projectAssistantMetadataRevision             = "assistantRevision"
	projectAssistantMetadataWorkingStatus        = "assistantStatus"
	projectAssistantMetadataProvisional          = "assistantProvisional"
	projectAssistantMetadataPreviewRefreshNeeded = "previewRefreshNeeded"
	projectAssistantMetadataPlan                 = "assistantPlan"
)

func projectAssistantDurableMetadataForTransition(run store.AssistantRun, status string, provisional, preview bool, toolCalls []projectToolCallStreamEvent, plan *projectAssistantPlanSnapshot) map[string]any {
	metadata := projectAssistantMessageMetadata(status, sanitizeProjectToolCallStreamEventsForMetadata(toolCalls))
	metadata[projectAssistantMetadataRunID] = run.ID
	metadata[projectAssistantMetadataRevision] = run.Revision
	metadata[projectAssistantMetadataWorkingStatus] = status
	metadata[projectAssistantMetadataProvisional] = provisional
	metadata[projectAssistantMetadataPreviewRefreshNeeded] = preview
	if plan, ok := projectAssistantPlanSnapshotFromMetadata(plan); ok {
		metadata[projectAssistantMetadataPlan] = *plan
	}
	return metadata
}

func projectAssistantDurableMetadataFromExisting(run store.AssistantRun, status string, provisional bool, existing map[string]any) map[string]any {
	metadata := map[string]any{}
	if actions := projectAssistantActionFeedFromMetadata(existing[projectMessageMetadataAssistantActionFeed]); len(actions) > 0 {
		metadata[projectMessageMetadataAssistantActionFeed] = actions
	}
	if interrupt := projectAssistantUIInterruptFromMetadata(existing[projectMessageMetadataAssistantInterrupt]); interrupt != nil {
		metadata[projectMessageMetadataAssistantInterrupt] = interrupt
	}
	if plan, ok := projectAssistantPlanSnapshotFromMetadata(existing[projectAssistantMetadataPlan]); ok {
		metadata[projectAssistantMetadataPlan] = *plan
	}
	preview, _ := existing[projectAssistantMetadataPreviewRefreshNeeded].(bool)
	metadata[projectAssistantMetadataRunID] = run.ID
	metadata[projectAssistantMetadataRevision] = run.Revision
	metadata[projectAssistantMetadataWorkingStatus] = status
	metadata[projectAssistantMetadataProvisional] = provisional
	metadata[projectAssistantMetadataPreviewRefreshNeeded] = preview
	return metadata
}

// projectAssistantPlanSnapshotFromMetadata is the durable metadata boundary
// for plans. Postgres rehydrates JSON values as generic maps, so decode them
// back into the public snapshot shape and retain only values the write_todos
// producer could have emitted. Validation deliberately does not sanitize or
// redact labels again: a retained plan must preserve its already-sanitized
// user-facing wording exactly.
func projectAssistantPlanSnapshotFromMetadata(value any) (*projectAssistantPlanSnapshot, bool) {
	if value == nil {
		return nil, false
	}
	raw, err := json.Marshal(value)
	if err != nil || !projectAssistantPlanMetadataKeysValid(raw) {
		return nil, false
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var plan projectAssistantPlanSnapshot
	if err := decoder.Decode(&plan); err != nil || !projectAssistantPlanSnapshotValid(plan) {
		return nil, false
	}
	return &plan, true
}

func projectAssistantPlanMetadataKeysValid(raw []byte) bool {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil || len(object) != 1 {
		return false
	}
	rawSteps, ok := object["steps"]
	if !ok {
		return false
	}
	var steps []map[string]json.RawMessage
	if err := json.Unmarshal(rawSteps, &steps); err != nil {
		return false
	}
	for _, step := range steps {
		if _, ok := step["content"]; !ok {
			return false
		}
		if _, ok := step["status"]; !ok {
			return false
		}
		for key := range step {
			switch key {
			case "content", "activeForm", "status":
			default:
				return false
			}
		}
	}
	return true
}

func projectAssistantPlanSnapshotValid(plan projectAssistantPlanSnapshot) bool {
	if len(plan.Steps) == 0 || len(plan.Steps) > projectEinoAssistantTodoProgressMaxItems {
		return false
	}
	inProgress := 0
	for _, step := range plan.Steps {
		if !projectAssistantPlanLabelValid(step.Content, true) || !projectAssistantPlanLabelValid(step.ActiveForm, false) {
			return false
		}
		switch step.Status {
		case "pending", "completed":
		case "in_progress":
			inProgress++
			if inProgress > 1 {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func projectAssistantPlanLabelValid(label string, required bool) bool {
	if !utf8.ValidString(label) || len(label) > projectEinoAssistantTodoProgressMaxLabelBytes {
		return false
	}
	if projectEinoAssistantTodoProgressLabel(label) != label {
		return false
	}
	return !required || strings.TrimSpace(label) != ""
}

type projectAssistantDurableMetadataState struct {
	status      string
	provisional bool
	toolCalls   []projectToolCallStreamEvent
	plan        *projectAssistantPlanSnapshot
}

func projectAssistantRunDisplayStatus(status store.AssistantRunStatus, fallback string) string {
	switch status {
	case store.AssistantRunStatusCompleted:
		return "Completed"
	case store.AssistantRunStatusAborted:
		return "Aborted"
	case store.AssistantRunStatusFailed:
		return "Failed"
	case store.AssistantRunStatusInterrupted:
		return "Interrupted"
	case store.AssistantRunStatusPendingPermission:
		return projectMessageStatusPendingPermission
	case store.AssistantRunStatusPendingInput:
		return projectMessageStatusPendingInput
	}
	return fallback
}

// persistProjectAssistantDurableMetadata is the one metadata write path for
// both a fresh run and a resumed continuation. It derives the metadata revision
// from the same transition that persists the run and message.
func (s *Server) persistProjectAssistantDurableMetadata(ctx context.Context, accumulator *projectAssistantSnapshotAccumulator, workspaceScope workspace.Scope, state *projectAssistantDurableMetadataState, runStatus *store.AssistantRunStatus) error {
	return accumulator.UpdateSnapshot(ctx, func(run *store.AssistantRun, message *store.Message) {
		if runStatus != nil {
			run.Status = *runStatus
		}
		if assistantRunTerminal(run.Status) {
			state.provisional = false
		}
		next := *run
		next.Revision++
		metadata := projectAssistantDurableMetadataForTransition(
			next,
			projectAssistantRunDisplayStatus(run.Status, state.status),
			state.provisional,
			s.projectAssistantPreviewRefreshNeeded(ctx, workspaceScope, "", false, state.toolCalls),
			state.toolCalls,
			state.plan,
		)
		// Resumed segments begin with durable actions from the previous segment.
		// Keep that history and only upsert new action updates.
		actions := projectAssistantActionFeedFromMetadata(message.Metadata[projectMessageMetadataAssistantActionFeed])
		for _, action := range projectAssistantActionFeedUpdatesFromToolCalls(state.toolCalls) {
			actions = applyProjectAssistantActionFeedUpdate(actions, action)
		}
		if len(actions) > 0 {
			metadata[projectMessageMetadataAssistantActionFeed] = actions
		}
		if preview, _ := message.Metadata[projectAssistantMetadataPreviewRefreshNeeded].(bool); preview {
			metadata[projectAssistantMetadataPreviewRefreshNeeded] = true
		}
		if _, ok := metadata[projectAssistantMetadataPlan]; !ok {
			if plan, ok := projectAssistantPlanSnapshotFromMetadata(message.Metadata[projectAssistantMetadataPlan]); ok {
				metadata[projectAssistantMetadataPlan] = *plan
			}
		}
		message.Metadata = metadata
	})
}

func (s *Server) startProjectAssistantRun(w http.ResponseWriter, r *http.Request) {
	c, id, project, ok := s.requireProjectWithClient(w, r)
	if !ok {
		return
	}
	var request CreateProjectMessageRequest
	if !decodeJSON(w, r, &request) {
		return
	}
	request.Content = strings.TrimSpace(request.Content)
	request.ClientRequestID = strings.TrimSpace(request.ClientRequestID)
	if request.Content == "" || request.ClientRequestID == "" {
		writeProjectError(w, newValidationError("content and clientRequestID are required"))
		return
	}
	scope := projectMessageScope(id.orgUUID, id.workspaceUUID, project.Name)
	supervisor := s.projectAssistantSupervisor()
	started, err := s.startProjectAssistantRunDurably(r.Context(), scope, request.Content, request.ClientRequestID, func(created store.AssistantRun, assistant store.Message, transcriptEmpty bool) error {
		var start *projectAssistantStreamStart
		if request.InitialProjectPrompt && transcriptEmpty {
			plan := projectAssistantInitialCreationPlan()
			start = &projectAssistantStreamStart{InitialApprovedPlan: cloneProjectAssistantApprovedPlan(&plan)}
		}
		return supervisor.Start(r.Context(), scope, created, assistant, func(ctx context.Context, accumulator *projectAssistantSnapshotAccumulator) {
			s.runProjectAssistantWorker(ctx, accumulator, r, id, c, project, created, start)
		})
	})
	if err != nil {
		if errors.Is(err, store.ErrAssistantRunConflict) {
			if _, latestErr := s.store.LatestAssistantRun(r.Context(), scope); latestErr == nil {
				s.writeProjectAssistantRunConflict(w, scope)
			} else {
				writeStatus(w, http.StatusConflict, "Conflict", "assistant run start is already in progress")
			}
			return
		}
		writeProjectError(w, err)
		return
	}
	s.writeProjectAssistantRunStart(w, http.StatusAccepted, scope, started.Run)
}

func (s *Server) runProjectAssistantWorker(ctx context.Context, accumulator *projectAssistantSnapshotAccumulator, request *http.Request, id identity, c *asclient.Client, project *aiv1alpha1.Project, run store.AssistantRun, start *projectAssistantStreamStart) {
	content := &strings.Builder{}
	state := &projectAssistantDurableMetadataState{status: "Working"}
	workspaceScope := projectWorkspaceScope(id, project.Name)
	persistMetadata := func(ctx context.Context, runStatus *store.AssistantRunStatus) error {
		return s.persistProjectAssistantDurableMetadata(ctx, accumulator, workspaceScope, state, runStatus)
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
	req := request.Clone(context.WithValue(ctx, projectAssistantSupervisorRunContextKey{}, run))
	reply, err := s.generateProjectAssistantStreamWithStart(req, id, c, project, projectAssistantStreamCallbacks{
		OnChunk: func(chunk string) {
			content.WriteString(chunk)
			recordSnapshotErr(accumulator.UpdateText(ctx, content.String(), false))
		},
		OnProvisionalText:  func(_ string) { state.provisional = true; recordSnapshotErr(persistMetadata(ctx, nil)) },
		OnProvisionalReset: func() { state.provisional = false; recordSnapshotErr(persistMetadata(ctx, nil)) },
		OnStatus:           func(nextStatus string) { state.status = nextStatus; recordSnapshotErr(persistMetadata(ctx, nil)) },
		OnPlan: func(plan projectAssistantPlanSnapshot) {
			state.plan = &plan
			recordSnapshotErr(persistMetadata(ctx, nil))
		},
		OnToolCall: func(event projectToolCallStreamEvent) {
			state.toolCalls = upsertProjectToolCallStreamEvent(state.toolCalls, event)
			recordSnapshotErr(persistMetadata(ctx, nil))
		},
		OnAssistantEvent: func(event projectAssistantEvent) {
			if event.Permission != nil && event.Permission.ToolCallID != "" {
				state.toolCalls = upsertProjectToolCallStreamEvent(state.toolCalls, projectToolCallStreamEvent{ID: event.Permission.ToolCallID, Name: event.Permission.ToolName, Status: "permission_required", Summary: event.Permission.Reason, Permission: event.Permission})
			}
			if event.FollowUp != nil && event.FollowUp.ToolCallID != "" {
				state.toolCalls = upsertProjectToolCallStreamEvent(state.toolCalls, projectToolCallStreamEvent{ID: event.FollowUp.ToolCallID, Name: projectToolAskFollowUp, Status: "input_required", Summary: event.FollowUp.Prompt, FollowUp: event.FollowUp})
			}
			if event.Checkpoint != nil {
				for i := range state.toolCalls {
					if state.toolCalls[i].Status == "permission_required" || state.toolCalls[i].Status == "input_required" {
						state.toolCalls[i].Checkpoint = event.Checkpoint
					}
				}
			}
			recordSnapshotErr(persistMetadata(ctx, nil))
		},
	}, start)
	finalContent := projectAssistantDurableFinalContent(reply, content.String())
	recordSnapshotErr(accumulator.UpdateText(ctx, finalContent, true))
	if getSnapshotErr() != nil {
		return
	}
	if err == nil {
		state.status = "Completed"
		runStatus := store.AssistantRunStatusCompleted
		transitionErr := persistMetadata(ctx, &runStatus)
		recordSnapshotErr(transitionErr)
		if transitionErr == nil {
			if committed, ok := accumulator.CommittedRun(); ok {
				accumulator.supervisor.log("completed", projectMessageScope(id.orgUUID, id.workspaceUUID, project.Name), committed)
			}
		}
		return
	}
	var permissionErr *projectAssistantPermissionRequiredError
	if errors.As(err, &permissionErr) {
		state.status = projectMessageStatusPendingPermission
		runStatus := store.AssistantRunStatusPendingPermission
		recordSnapshotErr(persistMetadata(context.Background(), &runStatus))
		return
	}
	var inputErr *projectAssistantInputRequiredError
	if errors.As(err, &inputErr) {
		state.status = projectMessageStatusPendingInput
		runStatus := store.AssistantRunStatusPendingInput
		recordSnapshotErr(persistMetadata(context.Background(), &runStatus))
		return
	}
	if errors.Is(err, context.Canceled) || errors.Is(context.Cause(ctx), context.Canceled) {
		state.status = "Aborted"
		runStatus := store.AssistantRunStatusAborted
		transitionErr := persistMetadata(context.Background(), &runStatus)
		recordSnapshotErr(transitionErr)
		if transitionErr == nil {
			if committed, ok := accumulator.CommittedRun(); ok {
				accumulator.supervisor.log("aborted", projectMessageScope(id.orgUUID, id.workspaceUUID, project.Name), committed)
			}
		}
		return
	}
	state.status = "Failed"
	runStatus := store.AssistantRunStatusFailed
	transitionErr := persistMetadata(context.Background(), &runStatus)
	recordSnapshotErr(transitionErr)
	if transitionErr == nil {
		if committed, ok := accumulator.CommittedRun(); ok {
			accumulator.supervisor.log("failed", projectMessageScope(id.orgUUID, id.workspaceUUID, project.Name), committed)
		}
	}
}

// startAndStreamLegacyProjectAssistant preserves the historical POST SSE
// transport while making it only a subscriber of the durable run. Closing its
// request therefore detaches this client and never owns or cancels execution.
func (s *Server) startAndStreamLegacyProjectAssistant(w http.ResponseWriter, r *http.Request, c *asclient.Client, id identity, project *aiv1alpha1.Project, content, clientRequestID string, start *projectAssistantStreamStart) {
	scope := projectMessageScope(id.orgUUID, id.workspaceUUID, project.Name)
	supervisor := s.projectAssistantSupervisor()
	started, err := s.startProjectAssistantRunDurably(r.Context(), scope, content, clientRequestID, func(run store.AssistantRun, assistant store.Message, _ bool) error {
		return supervisor.Start(r.Context(), scope, run, assistant, func(ctx context.Context, accumulator *projectAssistantSnapshotAccumulator) {
			s.runProjectAssistantWorker(ctx, accumulator, r, id, c, project, run, start)
		})
	})
	if err != nil {
		if errors.Is(err, store.ErrAssistantRunConflict) {
			s.writeProjectAssistantRunConflict(w, scope)
			return
		}
		writeProjectError(w, err)
		return
	}
	s.streamLegacyProjectAssistantEvents(w, r, scope, started.Run.ID)
}

func (s *Server) streamLegacyProjectAssistantEvents(w http.ResponseWriter, r *http.Request, scope store.Scope, runID string) {
	flusher, ok := startProjectMessageStream(w)
	if !ok {
		return
	}
	adapter := newProjectAssistantLegacyStreamAdapter()
	writeSnapshot := func(snapshot projectAssistantRunSnapshot) error {
		for _, event := range adapter.Events(snapshot) {
			if err := writeProjectMessageStreamEvent(w, flusher, event); err != nil {
				return err
			}
		}
		return nil
	}
	updates, unsubscribe, err := s.projectAssistantSupervisor().Subscribe(scope, runID, 0)
	if errors.Is(err, store.ErrAssistantRunNotFound) {
		run, loadErr := s.store.GetAssistantRun(r.Context(), scope, runID)
		if loadErr != nil {
			return
		}
		message, loadErr := s.findProjectMessage(r.Context(), scope, run.ActiveMessageID)
		if loadErr == nil {
			_ = writeSnapshot(projectAssistantRunSnapshot{Run: run, Message: message})
		}
		return
	}
	if err != nil {
		return
	}
	defer unsubscribe()
	keepalive := time.NewTicker(15 * time.Second)
	defer keepalive.Stop()
	for {
		select {
		case snapshot, open := <-updates:
			if !open || writeSnapshot(snapshot) != nil || assistantRunTerminal(snapshot.Run.Status) {
				return
			}
		case <-keepalive.C:
			if _, err := fmt.Fprint(w, ": keepalive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

func (s *Server) writeProjectAssistantRunStart(w http.ResponseWriter, status int, scope store.Scope, run store.AssistantRun) {
	message, err := s.findProjectMessage(context.Background(), scope, run.ActiveMessageID)
	if err != nil {
		writeProjectError(w, err)
		return
	}
	response := projectAssistantRunStartResponse{Run: run, Assistant: projectMessageToAPI(message)}
	if strings.TrimSpace(run.UserMessageID) != "" {
		user, err := s.findProjectMessage(context.Background(), scope, run.UserMessageID)
		if err != nil {
			writeProjectError(w, err)
			return
		}
		apiUser := projectMessageToAPI(user)
		response.User = &apiUser
	}
	writeJSON(w, status, response)
}

func (s *Server) writeProjectAssistantRunConflict(w http.ResponseWriter, scope store.Scope) {
	run, err := s.store.LatestAssistantRun(context.Background(), scope)
	if err != nil {
		writeProjectError(w, err)
		return
	}
	writeJSON(w, http.StatusConflict, projectAssistantRunSnapshot{Run: run})
}

func (s *Server) latestProjectAssistantRun(w http.ResponseWriter, r *http.Request) {
	_, id, project, ok := s.requireProjectWithClient(w, r)
	if !ok {
		return
	}
	scope := projectMessageScope(id.orgUUID, id.workspaceUUID, project.Name)
	if err := s.reconcileOrphanedProjectAssistantRun(r.Context(), scope); err != nil {
		writeProjectError(w, err)
		return
	}
	run, err := s.store.LatestAssistantRun(r.Context(), scope)
	if errors.Is(err, store.ErrAssistantRunNotFound) {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if err != nil {
		writeProjectError(w, err)
		return
	}
	message, err := s.findProjectMessage(r.Context(), scope, run.ActiveMessageID)
	if err != nil {
		writeProjectError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, projectAssistantRunSnapshot{Run: run, Message: message})
}

func (s *Server) streamProjectAssistantSnapshots(w http.ResponseWriter, r *http.Request) {
	_, id, project, ok := s.requireProjectWithClient(w, r)
	if !ok {
		return
	}
	scope := projectMessageScope(id.orgUUID, id.workspaceUUID, project.Name)
	runID := mux.Vars(r)["run"]
	if err := s.reconcileOrphanedProjectAssistantRun(r.Context(), scope); err != nil {
		writeProjectError(w, err)
		return
	}
	after := projectAssistantAfterRevision(r)
	updates, unsubscribe, err := s.projectAssistantSupervisor().Subscribe(scope, runID, after)
	if errors.Is(err, store.ErrAssistantRunNotFound) {
		run, loadErr := s.store.GetAssistantRun(r.Context(), scope, runID)
		if loadErr != nil {
			writeProjectError(w, loadErr)
			return
		}
		message, loadErr := s.findProjectMessage(r.Context(), scope, run.ActiveMessageID)
		if loadErr != nil {
			writeProjectError(w, loadErr)
			return
		}
		flusher, streamOK := startProjectMessageStream(w)
		if !streamOK {
			return
		}
		_ = writeProjectAssistantSnapshotSSE(w, flusher, projectAssistantRunSnapshot{Run: run, Message: message})
		return
	}
	if err != nil {
		writeProjectError(w, err)
		return
	}
	defer unsubscribe()
	flusher, streamOK := startProjectMessageStream(w)
	if !streamOK {
		return
	}
	keepalive := time.NewTicker(15 * time.Second)
	defer keepalive.Stop()
	for {
		select {
		case snapshot, open := <-updates:
			if !open {
				return
			}
			if err := writeProjectAssistantSnapshotSSE(w, flusher, snapshot); err != nil {
				return
			}
			if assistantRunTerminal(snapshot.Run.Status) {
				return
			}
		case <-keepalive.C:
			if _, err := fmt.Fprint(w, ": keepalive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

func projectAssistantAfterRevision(r *http.Request) int64 {
	raw := r.Header.Get("Last-Event-ID")
	if raw == "" {
		raw = r.URL.Query().Get("afterRevision")
	}
	value, _ := strconv.ParseInt(raw, 10, 64)
	return value
}

func writeProjectAssistantSnapshotSSE(w http.ResponseWriter, flusher http.Flusher, snapshot projectAssistantRunSnapshot) error {
	data, err := json.Marshal(snapshot)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "id: %d\nevent: snapshot\ndata: %s\n\n", snapshot.Run.Revision, data); err != nil {
		return err
	}
	flusher.Flush()
	return nil
}

func (s *Server) reconcileOrphanedProjectAssistantRun(ctx context.Context, scope store.Scope) error {
	run, err := s.store.LatestAssistantRun(ctx, scope)
	if err != nil {
		if errors.Is(err, store.ErrAssistantRunNotFound) {
			return nil
		}
		return err
	}
	if run.Status != store.AssistantRunStatusRunning {
		return nil
	}
	key := projectAssistantRunKey{OrgUUID: scope.OrgUUID, WorkspaceUUID: scope.WorkspaceUUID, ProjectName: scope.ProjectName}
	supervisor := s.projectAssistantSupervisor()
	if supervisor.reserved(scope) {
		return nil
	}
	supervisor.mu.Lock()
	active := supervisor.runs[key]
	supervisor.mu.Unlock()
	if active != nil && active.run.ID == run.ID {
		return nil
	}
	run.Status = store.AssistantRunStatusInterrupted
	run.UpdatedAt = time.Now().UTC()
	run.Revision++
	message, err := s.findProjectMessage(ctx, scope, run.ActiveMessageID)
	if err != nil {
		return err
	}
	message.UpdatedAt = run.UpdatedAt
	message.Metadata = projectAssistantDurableMetadataFromExisting(run, "Interrupted", false, message.Metadata)
	if err := s.store.SaveAssistantRunSnapshot(ctx, scope, run, []store.Message{message}, run.Revision-1); err != nil {
		return err
	}
	s.projectAssistantSupervisor().log("orphan_interrupted", scope, run)
	return nil
}
