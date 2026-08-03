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
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/mux"

	aiv1alpha1 "github.com/faroshq/provider-app-studio/apis/ai/v1alpha1"
	asclient "github.com/faroshq/provider-app-studio/client"
	"github.com/faroshq/provider-app-studio/store"
)

const (
	assistantThreadEventThreadCreated         = "thread.created"
	assistantThreadEventThreadUpdated         = "thread.updated"
	assistantThreadEventTurnStarted           = "turn.started"
	assistantThreadEventTurnCompleted         = "turn.completed"
	assistantThreadEventTurnFailed            = "turn.failed"
	assistantThreadEventTurnInterrupted       = "turn.interrupted"
	assistantThreadEventItemStarted           = "item.started"
	assistantThreadEventItemDelta             = "item.delta"
	assistantThreadEventItemCompleted         = "item.completed"
	assistantThreadEventApprovalRequested     = "approval.requested"
	assistantThreadEventApprovalResolved      = "approval.resolved"
	assistantThreadEventUserInputRequested    = "input.requested"
	assistantThreadEventUserInputResolved     = "input.resolved"
	assistantThreadEventAssistantMessage      = "agentMessage"
	assistantThreadEventUserMessage           = "userMessage"
	assistantThreadEventAssistantMessageDelta = "agentMessageDelta"
	assistantThreadEventDynamicToolCall       = "dynamicToolCall"
	assistantThreadEventPlan                  = "plan"
)

type assistantThreadCreateRequest struct {
	ID    string `json:"id,omitempty"`
	Title string `json:"title,omitempty"`
}

type assistantThreadPatchRequest struct {
	Title    *string `json:"title,omitempty"`
	Archived *bool   `json:"archived,omitempty"`
}

type assistantThreadTurnCreateRequest struct {
	Content             string                 `json:"content"`
	ClientUserMessageID string                 `json:"clientUserMessageID"`
	CollaborationMode   store.AssistantRunMode `json:"collaborationMode,omitempty"`
}

type assistantThreadTurnStartResponse struct {
	Thread store.AssistantThread `json:"thread"`
	Turn   store.AssistantTurn   `json:"turn"`
}

type assistantThreadSteerRequest struct {
	Content             string `json:"content"`
	ClientUserMessageID string `json:"clientUserMessageID"`
}

type assistantThreadInterruptRequest struct {
	ClientRequestID string `json:"clientRequestID"`
}

type assistantThreadItem struct {
	ID                 string                 `json:"id"`
	TurnID             string                 `json:"turnID,omitempty"`
	Type               string                 `json:"type"`
	Status             string                 `json:"status"`
	Content            string                 `json:"content,omitempty"`
	Data               json.RawMessage        `json:"data,omitempty"`
	AssistantMessageID string                 `json:"assistantMessageID,omitempty"`
	Mode               store.AssistantRunMode `json:"mode,omitempty"`
	Revision           int64                  `json:"revision,omitempty"`
	Error              json.RawMessage        `json:"error,omitempty"`
	Sequence           int64                  `json:"sequence"`
	CreatedAt          time.Time              `json:"createdAt"`
}

// assistantThreadRunItemStatus translates the provider run state into the
// stable thread-item terminal vocabulary. In particular, an old aborted run
// is presented as interrupted so consumers do not need to understand the
// legacy provider-only state.
func assistantThreadRunItemStatus(status store.AssistantRunStatus) string {
	switch status {
	case store.AssistantRunStatusCompleted:
		return "completed"
	case store.AssistantRunStatusFailed:
		return "failed"
	case store.AssistantRunStatusInterrupted, store.AssistantRunStatusAborted:
		return "interrupted"
	default:
		return "in_progress"
	}
}

func assistantThreadAgentMessageItem(turn store.AssistantTurn, run store.AssistantRun, status, content string, createdAt time.Time, metadata map[string]any) assistantThreadItem {
	mode := run.Mode
	if mode == "" {
		mode = turn.Mode
	}
	item := assistantThreadItem{
		ID:                 run.ActiveMessageID,
		TurnID:             turn.ID,
		Type:               assistantThreadEventAssistantMessage,
		Status:             status,
		Content:            content,
		AssistantMessageID: run.ActiveMessageID,
		Mode:               mode,
		Revision:           run.Revision,
		CreatedAt:          createdAt,
	}
	if status == "failed" && len(run.Error) > 0 {
		item.Error = append(json.RawMessage(nil), run.Error...)
	}
	return assistantThreadItemWithMessagePresentation(item, metadata)
}

func (s *Server) createProjectAssistantThread(w http.ResponseWriter, r *http.Request) {
	_, id, project, ok := s.requireProjectWithClient(w, r)
	if !ok {
		return
	}
	var request assistantThreadCreateRequest
	if !decodeStrictJSON(w, r, &request) {
		return
	}
	threadID := strings.TrimSpace(request.ID)
	if threadID == "" {
		threadID = "thread-" + uuid.NewString()
	}
	now := time.Now().UTC()
	thread := store.AssistantThread{ID: threadID, Title: request.Title, Status: store.AssistantThreadStatusIdle, ActorID: id.user, CreatedAt: now, UpdatedAt: now}
	payload, _ := json.Marshal(map[string]any{"thread": thread})
	created, err := s.store.CreateAssistantThread(r.Context(), projectMessageScope(id.orgUUID, id.workspaceUUID, project), thread, []store.AssistantThreadEvent{{Type: assistantThreadEventThreadCreated, Payload: payload, CreatedAt: now}})
	if err != nil {
		s.writeAssistantThreadError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

func (s *Server) listProjectAssistantThreads(w http.ResponseWriter, r *http.Request) {
	_, id, project, ok := s.requireProjectWithClient(w, r)
	if !ok {
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	includeArchived, _ := strconv.ParseBool(r.URL.Query().Get("includeArchived"))
	page, err := s.store.ListAssistantThreads(r.Context(), projectMessageScope(id.orgUUID, id.workspaceUUID, project), id.user, includeArchived, limit, r.URL.Query().Get("cursor"))
	if err != nil {
		s.writeAssistantThreadError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func (s *Server) patchProjectAssistantThread(w http.ResponseWriter, r *http.Request) {
	_, id, project, thread, ok := s.requireOwnedAssistantThread(w, r)
	if !ok {
		return
	}
	var request assistantThreadPatchRequest
	if !decodeStrictJSON(w, r, &request) {
		return
	}
	scope := projectMessageScope(id.orgUUID, id.workspaceUUID, project)
	updated, _, err := s.patchAssistantThreadWithEvent(r.Context(), scope, thread.ID, id.user, request)
	if err != nil {
		s.writeAssistantThreadError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

func (s *Server) listProjectAssistantThreadItems(w http.ResponseWriter, r *http.Request) {
	_, id, project, thread, ok := s.requireOwnedAssistantThread(w, r)
	if !ok {
		return
	}
	scope := projectMessageScope(id.orgUUID, id.workspaceUUID, project)
	events, err := s.loadAllAssistantThreadEvents(r.Context(), scope, thread.ID)
	if err != nil {
		s.writeAssistantThreadError(w, err)
		return
	}
	items, err := s.attachAssistantThreadMessagePresentation(r.Context(), scope, materializeAssistantThreadItems(events))
	if err != nil {
		s.writeAssistantThreadError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) startProjectAssistantThreadTurn(w http.ResponseWriter, r *http.Request) {
	c, id, project, thread, ok := s.requireOwnedAssistantThread(w, r)
	if !ok {
		return
	}
	var request assistantThreadTurnCreateRequest
	if !decodeStrictJSON(w, r, &request) {
		return
	}
	if _, err := request.publicAssistantThreadTurnMode(); err != nil {
		s.writeAssistantThreadError(w, err)
		return
	}
	s.startProjectAssistantThreadExecution(w, r, c, id, project, thread, request)
}

// startProjectAssistantThreadExecution is the single durable start path shared
// by ordinary turns and explicitly targeted review executions. Callers own
// strict decoding; this method owns validation, persistence, worker startup,
// and the canonical response.
func (s *Server) startProjectAssistantThreadExecution(w http.ResponseWriter, r *http.Request, c *asclient.Client, id identity, project *aiv1alpha1.Project, thread store.AssistantThread, request assistantThreadTurnCreateRequest) {
	if thread.Status == store.AssistantThreadStatusArchived {
		writeStatus(w, http.StatusConflict, "Conflict", "assistant thread is archived")
		return
	}
	request.Content = strings.TrimSpace(request.Content)
	request.ClientUserMessageID = strings.TrimSpace(request.ClientUserMessageID)
	if request.Content == "" || request.ClientUserMessageID == "" {
		writeProjectError(w, newValidationError("content and clientUserMessageID are required"))
		return
	}
	if request.CollaborationMode == "" {
		request.CollaborationMode = store.AssistantRunModeDefault
	}
	scope := projectMessageScope(id.orgUUID, id.workspaceUUID, project)
	initialBootstrap := false
	var err error
	if request.CollaborationMode != store.AssistantRunModeReview {
		initialBootstrap, err = s.consumeProjectInitialBootstrap(
			r.Context(),
			scope,
			id.user,
			request.Content,
			request.ClientUserMessageID,
		)
		if err != nil {
			s.writeAssistantThreadError(w, err)
			return
		}
	}
	if initialBootstrap {
		request.CollaborationMode = store.AssistantRunModeDefault
	}
	var canonicalTurn store.AssistantTurn
	started, err := s.startProjectAssistantRunDurablyWithMode(r.Context(), scope, id.user, request.Content, request.ClientUserMessageID, request.CollaborationMode,
		func(created store.AssistantRun, assistant store.Message, transcriptEmpty bool) error {
			var start *projectAssistantStreamStart
			if initialBootstrap && transcriptEmpty {
				plan := projectAssistantInitialCreationPlan(request.Content)
				start = &projectAssistantStreamStart{InitialApprovedPlan: cloneProjectAssistantApprovedPlan(&plan)}
			}
			now := time.Now().UTC()
			canonicalTurn = store.AssistantTurn{ID: created.ID, ThreadID: thread.ID, ActorID: id.user, ClientUserMessageID: request.ClientUserMessageID,
				Mode: created.Mode, ApprovalMode: created.ApprovalMode, Status: store.AssistantTurnStatusInProgress, CreatedAt: now, UpdatedAt: now}
			turnPayload, _ := json.Marshal(map[string]any{"turn": canonicalTurn})
			userItem := assistantThreadItem{ID: created.UserMessageID, TurnID: created.ID, Type: assistantThreadEventUserMessage, Status: "completed", Content: request.Content, CreatedAt: now}
			userPayload, _ := json.Marshal(map[string]any{"item": userItem})
			assistantItem := assistantThreadAgentMessageItem(canonicalTurn, created, "in_progress", "", now, nil)
			assistantPayload, _ := json.Marshal(map[string]any{"item": assistantItem})
			createdTurn, createErr := s.store.CreateAssistantTurn(r.Context(), scope, canonicalTurn, []store.AssistantThreadEvent{
				{Type: assistantThreadEventTurnStarted, Payload: turnPayload, CreatedAt: now},
				{Type: assistantThreadEventItemCompleted, ItemID: userItem.ID, Payload: userPayload, CreatedAt: now},
				{Type: assistantThreadEventItemStarted, ItemID: assistantItem.ID, Payload: assistantPayload, CreatedAt: now},
			})
			if createErr != nil {
				return createErr
			}
			canonicalTurn = createdTurn
			if err := s.projectAssistantSupervisor().Start(r.Context(), scope, created, assistant, func(ctx context.Context, accumulator *projectAssistantSnapshotAccumulator) {
				s.runProjectAssistantWorker(ctx, accumulator, r, id, c, project, created, start)
			}); err != nil {
				return err
			}
			go s.mirrorAssistantRunIntoThread(scope, thread.ID, canonicalTurn, created)
			return nil
		})
	if err != nil {
		s.writeAssistantThreadError(w, err)
		return
	}
	if !started.Started {
		canonicalTurn, err = s.store.FindAssistantTurnByClientUserMessageID(r.Context(), scope, thread.ID, request.ClientUserMessageID)
		if errors.Is(err, store.ErrAssistantTurnNotFound) {
			canonicalTurn, err = s.repairProjectAssistantThreadTurn(r.Context(), scope, thread, request, started.Run)
		}
		if err != nil {
			s.writeAssistantThreadError(w, err)
			return
		}
		if canonicalTurn.Status == store.AssistantTurnStatusInProgress && assistantRunTerminal(started.Run.Status) {
			if err := s.reconcileProjectAssistantThreadTurn(r.Context(), scope, canonicalTurn); err != nil {
				s.writeAssistantThreadError(w, err)
				return
			}
			canonicalTurn, err = s.store.GetAssistantTurn(r.Context(), scope, thread.ID, canonicalTurn.ID)
			if err != nil {
				s.writeAssistantThreadError(w, err)
				return
			}
		}
	}
	thread, _ = s.store.GetAssistantThread(r.Context(), scope, thread.ID)
	writeJSON(w, http.StatusAccepted, assistantThreadTurnStartResponse{Thread: thread, Turn: canonicalTurn})
}

// repairProjectAssistantThreadTurn reconstructs the canonical thread boundary
// when generic run creation committed but provider-specific turn creation did
// not. It is intentionally idempotent: CreateAssistantTurn returns an
// existing turn for the same client message, and a terminal run is projected
// through the normal mirror/reconciliation path before the replay responds.
func (s *Server) repairProjectAssistantThreadTurn(ctx context.Context, scope store.Scope, thread store.AssistantThread, request assistantThreadTurnCreateRequest, run store.AssistantRun) (store.AssistantTurn, error) {
	if strings.TrimSpace(run.ID) == "" {
		return store.AssistantTurn{}, store.ErrAssistantTurnNotFound
	}
	user, err := s.findProjectMessage(ctx, scope, run.UserMessageID)
	if err != nil {
		return store.AssistantTurn{}, err
	}
	assistant, err := s.findProjectMessage(ctx, scope, run.ActiveMessageID)
	if err != nil {
		return store.AssistantTurn{}, err
	}
	now := run.CreatedAt
	if now.IsZero() {
		now = time.Now().UTC()
	}
	turn := store.AssistantTurn{
		ID:                  run.ID,
		ThreadID:            thread.ID,
		ActorID:             thread.ActorID,
		ClientUserMessageID: request.ClientUserMessageID,
		Mode:                run.Mode,
		ApprovalMode:        run.ApprovalMode,
		Status:              store.AssistantTurnStatusInProgress,
		CreatedAt:           now,
		UpdatedAt:           now,
	}
	turnPayload, _ := json.Marshal(map[string]any{"turn": turn})
	userItem := assistantThreadItem{ID: user.ID, TurnID: turn.ID, Type: assistantThreadEventUserMessage, Status: "completed", Content: user.Content, CreatedAt: user.CreatedAt}
	userPayload, _ := json.Marshal(map[string]any{"item": userItem})
	assistantItem := assistantThreadAgentMessageItem(turn, run, "in_progress", assistant.Content, assistant.CreatedAt, assistant.Metadata)
	assistantPayload, _ := json.Marshal(map[string]any{"item": assistantItem})
	created, err := s.store.CreateAssistantTurn(ctx, scope, turn, []store.AssistantThreadEvent{
		{Type: assistantThreadEventTurnStarted, Payload: turnPayload, CreatedAt: now},
		{Type: assistantThreadEventItemCompleted, ItemID: userItem.ID, Payload: userPayload, CreatedAt: user.CreatedAt},
		{Type: assistantThreadEventItemStarted, ItemID: assistantItem.ID, Payload: assistantPayload, CreatedAt: assistant.CreatedAt},
	})
	if err != nil {
		return store.AssistantTurn{}, err
	}
	if assistantRunTerminal(run.Status) && created.Status == store.AssistantTurnStatusInProgress {
		if err := s.reconcileProjectAssistantThreadTurn(ctx, scope, created); err != nil {
			return store.AssistantTurn{}, err
		}
		return s.store.GetAssistantTurn(ctx, scope, thread.ID, created.ID)
	}
	return created, nil
}

func (s *Server) activeProjectAssistantThreadTurn(w http.ResponseWriter, r *http.Request) {
	_, id, project, thread, ok := s.requireOwnedAssistantThread(w, r)
	if !ok {
		return
	}
	turn, err := s.store.ActiveAssistantTurn(r.Context(), projectMessageScope(id.orgUUID, id.workspaceUUID, project), thread.ID)
	if errors.Is(err, store.ErrAssistantTurnNotFound) {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if err != nil {
		s.writeAssistantThreadError(w, err)
		return
	}
	if err := s.reconcileProjectAssistantThreadTurn(r.Context(), projectMessageScope(id.orgUUID, id.workspaceUUID, project), turn); err != nil {
		s.writeAssistantThreadError(w, err)
		return
	}
	turn, err = s.store.GetAssistantTurn(r.Context(), projectMessageScope(id.orgUUID, id.workspaceUUID, project), thread.ID, turn.ID)
	if err != nil {
		s.writeAssistantThreadError(w, err)
		return
	}
	if turn.Status != store.AssistantTurnStatusInProgress {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	writeJSON(w, http.StatusOK, turn)
}

func (s *Server) steerProjectAssistantThreadTurn(w http.ResponseWriter, r *http.Request) {
	_, id, project, thread, ok := s.requireOwnedAssistantThread(w, r)
	if !ok {
		return
	}
	scope := projectMessageScope(id.orgUUID, id.workspaceUUID, project)
	turn, err := s.store.GetAssistantTurn(r.Context(), scope, thread.ID, mux.Vars(r)["turn"])
	if err != nil || turn.ActorID != id.user || turn.Status != store.AssistantTurnStatusInProgress {
		writeStatus(w, http.StatusNotFound, "NotFound", "active assistant turn not found")
		return
	}
	var request assistantThreadSteerRequest
	if !decodeStrictJSON(w, r, &request) {
		return
	}
	request.Content = strings.TrimSpace(request.Content)
	request.ClientUserMessageID = strings.TrimSpace(request.ClientUserMessageID)
	if request.Content == "" || request.ClientUserMessageID == "" {
		writeProjectError(w, newValidationError("content and clientUserMessageID are required"))
		return
	}
	_, user, _, handled, err := s.projectAssistantSupervisor().EnqueueSteering(r.Context(), scope, turn.ID, id.user, request.Content, request.ClientUserMessageID, turn.Mode)
	if err != nil || !handled {
		if err == nil {
			err = store.ErrAssistantTurnConflict
		}
		s.writeAssistantThreadError(w, err)
		return
	}
	item := assistantThreadItem{ID: user.ID, TurnID: turn.ID, Type: assistantThreadEventUserMessage, Status: "completed", Content: user.Content, CreatedAt: user.CreatedAt}
	payload, _ := json.Marshal(map[string]any{"item": item})
	_, err = s.appendAssistantThreadEvent(r.Context(), scope, store.AssistantThreadEvent{ThreadID: thread.ID, TurnID: turn.ID, Type: assistantThreadEventItemCompleted, ItemID: item.ID, Payload: payload})
	if err != nil {
		s.writeAssistantThreadError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, turn)
}

func (s *Server) interruptProjectAssistantThreadTurn(w http.ResponseWriter, r *http.Request) {
	_, id, project, thread, ok := s.requireOwnedAssistantThread(w, r)
	if !ok {
		return
	}
	scope := projectMessageScope(id.orgUUID, id.workspaceUUID, project)
	turn, err := s.store.GetAssistantTurn(r.Context(), scope, thread.ID, mux.Vars(r)["turn"])
	if err != nil || turn.ActorID != id.user {
		writeStatus(w, http.StatusNotFound, "NotFound", "assistant turn not found")
		return
	}
	var request assistantThreadInterruptRequest
	if !decodeStrictJSON(w, r, &request) {
		return
	}
	request.ClientRequestID = strings.TrimSpace(request.ClientRequestID)
	if request.ClientRequestID == "" {
		writeProjectError(w, newValidationError("clientRequestID is required"))
		return
	}
	run, runErr := s.store.GetAssistantRun(r.Context(), scope, turn.ID)
	if runErr != nil || s.authorizeProjectAssistantRunActor(r.Context(), scope, run, id.user, false) != nil {
		writeStatus(w, http.StatusNotFound, "NotFound", "assistant turn not found")
		return
	}
	if run.Status == store.AssistantRunStatusPendingPermission || run.Status == store.AssistantRunStatusPendingInput {
		if err := s.reattachProjectAssistantPendingRun(r.Context(), scope, run); err != nil {
			s.writeAssistantThreadError(w, err)
			return
		}
	}
	if found, bindErr := s.projectAssistantSupervisor().BindStopRequest(r.Context(), scope, turn.ID, id.user, request.ClientRequestID); found {
		if bindErr != nil {
			s.writeAssistantThreadError(w, bindErr)
			return
		}
	} else {
		if bindErr := bindProjectAssistantStopRequest(&run, id.user, request.ClientRequestID); bindErr != nil {
			s.writeAssistantThreadError(w, bindErr)
			return
		}
		run.UpdatedAt = time.Now().UTC()
		if saveErr := s.store.SaveAssistantRun(r.Context(), scope, run); saveErr != nil {
			s.writeAssistantThreadError(w, saveErr)
			return
		}
	}
	stopped, found, err := s.projectAssistantSupervisor().Stop(scope, turn.ID)
	if err != nil {
		s.writeAssistantThreadError(w, err)
		return
	}
	if !found && !assistantRunTerminal(run.Status) {
		writeStatus(w, http.StatusConflict, "Conflict", "assistant turn is not active on this provider")
		return
	}
	if found {
		run = stopped
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"turnID": turn.ID, "status": run.Status})
}

// Approval and structured-input decisions use the same durable Eino checkpoint
// implementation during the cutover. Their public identity is the Turn ID.
func (s *Server) respondProjectAssistantThreadTurn(w http.ResponseWriter, r *http.Request) {
	_, id, project, thread, ok := s.requireOwnedAssistantThread(w, r)
	if !ok {
		return
	}
	turnID := mux.Vars(r)["turn"]
	turn, err := s.store.GetAssistantTurn(r.Context(), projectMessageScope(id.orgUUID, id.workspaceUUID, project), thread.ID, turnID)
	if err != nil || turn.ActorID != id.user || turn.Status != store.AssistantTurnStatusInProgress {
		writeStatus(w, http.StatusNotFound, "NotFound", "active assistant turn not found")
		return
	}
	vars := mux.Vars(r)
	vars["run"] = turnID
	s.resumeProjectAssistant(w, mux.SetURLVars(r, vars))
}

func (s *Server) streamProjectAssistantThreadEvents(w http.ResponseWriter, r *http.Request) {
	_, id, project, thread, ok := s.requireOwnedAssistantThread(w, r)
	if !ok {
		return
	}
	after := assistantThreadAfterSequence(r)
	scope := projectMessageScope(id.orgUUID, id.workspaceUUID, project)
	if active, err := s.store.ActiveAssistantTurn(r.Context(), scope, thread.ID); err == nil {
		if err := s.reconcileProjectAssistantThreadTurn(r.Context(), scope, active); err != nil {
			s.writeAssistantThreadError(w, err)
			return
		}
	} else if !errors.Is(err, store.ErrAssistantTurnNotFound) {
		s.writeAssistantThreadError(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeStatus(w, http.StatusInternalServerError, "InternalError", "streaming is not supported")
		return
	}
	poll := time.NewTicker(250 * time.Millisecond)
	keepalive := time.NewTicker(15 * time.Second)
	defer poll.Stop()
	defer keepalive.Stop()
	for {
		events, err := s.store.ListAssistantThreadEvents(r.Context(), scope, thread.ID, after, 500)
		if err != nil {
			return
		}
		for _, event := range events {
			data, _ := json.Marshal(event)
			if _, err := fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", event.Sequence, event.Type, data); err != nil {
				return
			}
			after = event.Sequence
			if s.assistantThreadTerminalEventEndsStream(r.Context(), scope, thread.ID, event) {
				flusher.Flush()
				return
			}
		}
		if len(events) > 0 {
			flusher.Flush()
			continue
		}
		select {
		case <-poll.C:
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

// assistantThreadTerminalEventEndsStream keeps a historical turn's terminal
// event from closing a thread stream while a newer turn is still active. The
// event log is shared by every turn, so terminality is only a stream boundary
// when the store confirms there is no different in-progress turn.
func (s *Server) assistantThreadTerminalEventEndsStream(ctx context.Context, scope store.Scope, threadID string, event store.AssistantThreadEvent) bool {
	switch event.Type {
	case assistantThreadEventTurnCompleted, assistantThreadEventTurnFailed, assistantThreadEventTurnInterrupted:
	default:
		return false
	}
	active, err := s.store.ActiveAssistantTurn(ctx, scope, threadID)
	if err == nil {
		return active.ID == event.TurnID
	}
	return errors.Is(err, store.ErrAssistantTurnNotFound)
}

// reconcileProjectAssistantThreadTurn closes the canonical projection when a
// provider restart orphaned the internal Eino run before its mirror goroutine
// could publish the terminal item and event.
func (s *Server) reconcileProjectAssistantThreadTurn(ctx context.Context, scope store.Scope, turn store.AssistantTurn) error {
	release := s.acquireAssistantThreadProjectionLock(scope, turn.ThreadID, turn.ID)
	defer release()

	if err := s.reconcileOrphanedProjectAssistantRun(ctx, scope); err != nil {
		return err
	}
	run, err := s.store.GetAssistantRun(ctx, scope, turn.ID)
	if err != nil || !assistantRunTerminal(run.Status) {
		return err
	}
	current, err := s.store.GetAssistantTurn(ctx, scope, turn.ThreadID, turn.ID)
	if err != nil || current.Status != store.AssistantTurnStatusInProgress {
		return err
	}
	message, err := s.findProjectMessage(ctx, scope, run.ActiveMessageID)
	if err != nil {
		return err
	}
	state, err := s.loadAssistantThreadMirrorState(ctx, scope, turn.ThreadID, run.ActiveMessageID, turn.ID)
	if err != nil {
		return err
	}
	if err := s.closeStaleAssistantThreadMessages(ctx, scope, turn.ThreadID, turn.ID, run.ActiveMessageID, &state); err != nil {
		return err
	}
	if state.lastRequestID != "" {
		resolution, err := assistantThreadPendingRequestResolution(turn.ID, state)
		if err != nil {
			return fmt.Errorf("encode orphaned assistant thread request resolution: %w", err)
		}
		resolution.ThreadID = turn.ThreadID
		if _, err := s.appendAssistantThreadEvent(ctx, scope, resolution); err != nil {
			return fmt.Errorf("resolve orphaned assistant thread request: %w", err)
		}
		state.lastRequestID, state.lastRequestType = "", ""
	}
	if !state.terminalItem {
		item := assistantThreadAgentMessageItem(turn, run, assistantThreadRunItemStatus(run.Status), message.Content, message.CreatedAt, message.Metadata)
		payload, _ := json.Marshal(map[string]any{"item": item})
		if _, err := s.appendAssistantThreadEvent(ctx, scope, store.AssistantThreadEvent{ThreadID: turn.ThreadID, TurnID: turn.ID, Type: assistantThreadEventItemCompleted, ItemID: item.ID, Payload: payload}); err != nil {
			return err
		}
		state.terminalItem = true
	}
	current.UpdatedAt = time.Now().UTC()
	terminalType := assistantThreadEventTurnCompleted
	switch run.Status {
	case store.AssistantRunStatusCompleted:
		current.Status = store.AssistantTurnStatusCompleted
	case store.AssistantRunStatusInterrupted, store.AssistantRunStatusAborted:
		current.Status = store.AssistantTurnStatusInterrupted
		terminalType = assistantThreadEventTurnInterrupted
	default:
		current.Status = store.AssistantTurnStatusFailed
		current.Error = run.Error
		terminalType = assistantThreadEventTurnFailed
	}
	if state.terminalEvent {
		return s.store.SaveAssistantTurn(ctx, scope, current)
	}
	turnPayload, _ := json.Marshal(map[string]any{"turn": current})
	return s.saveAssistantTurnWithEvent(ctx, scope, current, store.AssistantThreadEvent{ThreadID: turn.ThreadID, TurnID: turn.ID, Type: terminalType, Payload: turnPayload})
}

func (s *Server) requireOwnedAssistantThread(w http.ResponseWriter, r *http.Request) (*asclient.Client, identity, *aiv1alpha1.Project, store.AssistantThread, bool) {
	c, id, project, ok := s.requireProjectWithClient(w, r)
	if !ok {
		return nil, identity{}, nil, store.AssistantThread{}, false
	}
	thread, err := s.store.GetAssistantThread(r.Context(), projectMessageScope(id.orgUUID, id.workspaceUUID, project), mux.Vars(r)["thread"])
	if err != nil || thread.ActorID != id.user {
		writeStatus(w, http.StatusNotFound, "NotFound", "assistant thread not found")
		return nil, identity{}, nil, store.AssistantThread{}, false
	}
	return c, id, project, thread, true
}

func (s *Server) writeAssistantThreadError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrAssistantThreadNotFound), errors.Is(err, store.ErrAssistantTurnNotFound):
		writeStatus(w, http.StatusNotFound, "NotFound", err.Error())
	case errors.Is(err, store.ErrAssistantThreadConflict), errors.Is(err, store.ErrAssistantTurnConflict), errors.Is(err, store.ErrAssistantRunConflict):
		writeStatus(w, http.StatusConflict, "Conflict", err.Error())
	default:
		writeProjectError(w, err)
	}
}

func assistantThreadAfterSequence(r *http.Request) int64 {
	raw := strings.TrimSpace(r.URL.Query().Get("afterSequence"))
	if raw == "" {
		raw = strings.TrimSpace(r.Header.Get("Last-Event-ID"))
	}
	after, _ := strconv.ParseInt(raw, 10, 64)
	if after < 0 {
		return 0
	}
	return after
}

func (s *Server) appendAssistantThreadEvent(ctx context.Context, scope store.Scope, event store.AssistantThreadEvent) (store.AssistantThreadEvent, error) {
	for attempts := 0; attempts < 8; attempts++ {
		events, err := s.loadAllAssistantThreadEvents(ctx, scope, event.ThreadID)
		if err != nil {
			return store.AssistantThreadEvent{}, err
		}
		expected := int64(0)
		if len(events) > 0 {
			expected = events[len(events)-1].Sequence
		}
		created, err := s.store.AppendAssistantThreadEvent(ctx, scope, event, expected)
		if !errors.Is(err, store.ErrAssistantThreadEventConflict) {
			return created, err
		}
	}
	return store.AssistantThreadEvent{}, store.ErrAssistantThreadEventConflict
}

func (s *Server) saveAssistantTurnWithEvent(ctx context.Context, scope store.Scope, turn store.AssistantTurn, event store.AssistantThreadEvent) error {
	for attempts := 0; attempts < 8; attempts++ {
		events, err := s.loadAllAssistantThreadEvents(ctx, scope, turn.ThreadID)
		if err != nil {
			return err
		}
		expected := int64(0)
		if len(events) > 0 {
			expected = events[len(events)-1].Sequence
		}
		err = s.store.SaveAssistantTurnWithEvent(ctx, scope, turn, event, expected)
		if !errors.Is(err, store.ErrAssistantThreadEventConflict) {
			return err
		}
	}
	return store.ErrAssistantThreadEventConflict
}

func (s *Server) loadAllAssistantThreadEvents(ctx context.Context, scope store.Scope, threadID string) ([]store.AssistantThreadEvent, error) {
	all := make([]store.AssistantThreadEvent, 0)
	after := int64(0)
	for {
		page, err := s.store.ListAssistantThreadEvents(ctx, scope, threadID, after, 500)
		if err != nil {
			return nil, err
		}
		all = append(all, page...)
		if len(page) < 500 {
			return all, nil
		}
		after = page[len(page)-1].Sequence
	}
}

func materializeAssistantThreadItems(events []store.AssistantThreadEvent) []assistantThreadItem {
	items := make([]assistantThreadItem, 0)
	type assistantThreadItemKey struct {
		turnID string
		itemID string
	}
	indexes := map[assistantThreadItemKey]int{}
	turnModes := map[string]store.AssistantRunMode{}
	terminalTurns := map[string]string{}
	terminalTurnErrors := map[string]json.RawMessage{}
	for _, event := range events {
		var envelope struct {
			Item  assistantThreadItem `json:"item"`
			Turn  store.AssistantTurn `json:"turn"`
			Delta string              `json:"delta"`
		}
		_ = json.Unmarshal(event.Payload, &envelope)
		turnID := event.TurnID
		if turnID == "" {
			turnID = envelope.Item.TurnID
		}
		if turnID == "" {
			turnID = envelope.Turn.ID
		}
		if envelope.Turn.Mode != "" {
			turnModes[turnID] = envelope.Turn.Mode
		}
		switch event.Type {
		case assistantThreadEventTurnCompleted:
			terminalTurns[turnID] = "completed"
		case assistantThreadEventTurnFailed:
			terminalTurns[turnID] = "failed"
			if len(envelope.Turn.Error) > 0 {
				terminalTurnErrors[turnID] = append(json.RawMessage(nil), envelope.Turn.Error...)
			}
		case assistantThreadEventTurnInterrupted:
			terminalTurns[turnID] = "interrupted"
		}
		if event.ItemID == "" {
			continue
		}
		key := assistantThreadItemKey{turnID: turnID, itemID: event.ItemID}
		index, exists := indexes[key]
		if !exists {
			index = len(items)
			indexes[key] = index
			items = append(items, assistantThreadItem{ID: event.ItemID, TurnID: turnID, Status: "in_progress", Sequence: event.Sequence, CreatedAt: event.CreatedAt})
		}
		if envelope.Item.ID != "" {
			if envelope.Item.TurnID == "" {
				envelope.Item.TurnID = turnID
			}
			// Item creation time is stable across subsequent delta/completion
			// events. Event creation time remains available on the event itself.
			if !items[index].CreatedAt.IsZero() {
				envelope.Item.CreatedAt = items[index].CreatedAt
			} else if envelope.Item.CreatedAt.IsZero() {
				envelope.Item.CreatedAt = event.CreatedAt
			}
			envelope.Item.Sequence = event.Sequence
			items[index] = envelope.Item
		}
		if event.Type == assistantThreadEventItemDelta {
			items[index].Content += envelope.Delta
			items[index].Sequence = event.Sequence
		}
	}
	// A terminal turn cannot have an actionable request. This also repairs the
	// read projection for streams written by older restart recovery code that
	// terminalized an orphaned turn without first emitting request.resolved or
	// item.completed for every steered assistant message segment.
	for index := range items {
		if items[index].Type == assistantThreadEventAssistantMessage && items[index].Mode == "" {
			items[index].Mode = turnModes[items[index].TurnID]
		}
		if terminalStatus, terminal := terminalTurns[items[index].TurnID]; terminal && items[index].Status == "in_progress" {
			switch items[index].Type {
			case "approval", "input":
				items[index].Status = "completed"
			case assistantThreadEventAssistantMessage:
				items[index].Status = terminalStatus
				if terminalStatus == "failed" && len(items[index].Error) == 0 {
					items[index].Error = append(json.RawMessage(nil), terminalTurnErrors[items[index].TurnID]...)
				}
			}
		}
	}
	return items
}

// assistantThreadItemWithMessagePresentation carries the durable presentation
// fields needed to render an agent message through the canonical thread-item
// transport. Keep this deliberately narrower than the internal message metadata
// so the thread contract does not expose execution-only state.
func assistantThreadItemWithMessagePresentation(item assistantThreadItem, metadata map[string]any) assistantThreadItem {
	if item.Type != assistantThreadEventAssistantMessage {
		return item
	}
	progress, ok := projectAssistantProgressSnapshotFromMetadata(metadata[projectAssistantMetadataProgress])
	if !ok {
		return item
	}
	data := map[string]any{}
	if len(item.Data) > 0 {
		_ = json.Unmarshal(item.Data, &data)
	}
	data[projectAssistantMetadataProgress] = *progress
	encoded, err := json.Marshal(data)
	if err == nil {
		item.Data = encoded
	}
	return item
}

// attachAssistantThreadMessagePresentation is a compatibility bridge for
// thread events written before agent-message presentation data became part of
// the canonical item payload. It enriches the read model from the already
// durable assistant message without rewriting historical events.
func (s *Server) attachAssistantThreadMessagePresentation(ctx context.Context, scope store.Scope, items []assistantThreadItem) ([]assistantThreadItem, error) {
	wanted := map[string][]int{}
	for index, item := range items {
		if item.Type != assistantThreadEventAssistantMessage {
			continue
		}
		var data map[string]any
		if len(item.Data) > 0 && json.Unmarshal(item.Data, &data) == nil {
			if _, ok := projectAssistantProgressSnapshotFromMetadata(data[projectAssistantMetadataProgress]); ok {
				continue
			}
		}
		wanted[item.ID] = append(wanted[item.ID], index)
	}
	if len(wanted) == 0 {
		return items, nil
	}

	cursor := ""
	for len(wanted) > 0 {
		page, err := s.store.ListMessages(ctx, scope, 250, cursor)
		if err != nil {
			return nil, err
		}
		for _, message := range page.Items {
			indexes, ok := wanted[message.ID]
			if !ok {
				continue
			}
			for _, index := range indexes {
				items[index] = assistantThreadItemWithMessagePresentation(items[index], message.Metadata)
			}
			delete(wanted, message.ID)
		}
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}
	return items, nil
}

func assistantThreadInterruptMatchesPendingRequest(interrupt *projectAssistantUIInterruptRequest, eventType, runID, requestID string) bool {
	if interrupt == nil || interrupt.Status != "pending" || interrupt.Action == nil ||
		interrupt.Action.RunID != runID || interrupt.Action.RequestID != requestID {
		return false
	}
	if eventType == assistantThreadEventUserInputRequested {
		return interrupt.Kind == projectAssistantInterruptTypeFollowUp
	}
	return interrupt.Kind != projectAssistantInterruptTypeFollowUp
}
