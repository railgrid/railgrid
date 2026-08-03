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
	"sort"
	"strings"
	"time"

	"k8s.io/klog/v2"

	"github.com/faroshq/provider-app-studio/store"
)

const (
	assistantThreadMirrorPersistMaxAttempts = 8
	assistantThreadMirrorRetryBaseDelay     = 25 * time.Millisecond
)

// assistantThreadMirrorState is reconstructed from the canonical thread
// stream before subscribing to the in-process supervisor. A mirror can then
// be restarted after a process or persistence failure without emitting an
// already committed delta, item, request, plan, or terminal event.
type assistantThreadMirrorState struct {
	lastContent     string
	activeMessageID string
	actionStatuses  map[string]string
	openMessages    map[string]struct{}
	messageStarted  map[string]bool
	messageContent  map[string]string
	messageCreated  map[string]time.Time
	messageMode     map[string]store.AssistantRunMode
	messageRevision map[string]int64
	lastPlan        string
	lastRequestID   string
	lastRequestType string
	lastSequence    int64
	reconstructed   bool
	needsReload     bool
	terminalItem    bool
	terminalEvent   bool
}

// assistantThreadDynamicToolItemID scopes a provider action ID to the
// assistant message segment that emitted it. Providers may reuse a call ID
// after steering rotates the active assistant message; the thread item ID must
// remain distinct while the action payload retains the raw provider ID.
func assistantThreadDynamicToolItemID(activeMessageID, actionID string) string {
	return "tool-" + strings.TrimSpace(activeMessageID) + "-" + strings.TrimSpace(actionID)
}

func (s *Server) loadAssistantThreadMirrorState(ctx context.Context, scope store.Scope, threadID, activeMessageID, turnID string) (assistantThreadMirrorState, error) {
	events, err := s.loadAllAssistantThreadEvents(ctx, scope, threadID)
	if err != nil {
		return assistantThreadMirrorState{}, err
	}
	state := assistantThreadMirrorState{
		activeMessageID: activeMessageID,
		actionStatuses:  map[string]string{},
		openMessages:    map[string]struct{}{},
		messageStarted:  map[string]bool{},
		messageContent:  map[string]string{},
		messageCreated:  map[string]time.Time{},
		messageMode:     map[string]store.AssistantRunMode{},
		messageRevision: map[string]int64{},
		reconstructed:   true,
	}
	durableActiveMessageID := ""
	for _, event := range events {
		if event.Sequence > state.lastSequence {
			state.lastSequence = event.Sequence
		}
		if event.TurnID != "" && event.TurnID != turnID {
			continue
		}
		if event.Type == assistantThreadEventTurnCompleted || event.Type == assistantThreadEventTurnFailed || event.Type == assistantThreadEventTurnInterrupted {
			state.terminalEvent = true
		}
		var envelope struct {
			Item  assistantThreadItem `json:"item"`
			Delta string              `json:"delta"`
		}
		envelopeDecoded := json.Unmarshal(event.Payload, &envelope) == nil
		if event.ItemID != "" {
			messageID := event.ItemID
			if envelopeDecoded && envelope.Item.ID != "" {
				messageID = envelope.Item.ID
			}
			if event.Type == assistantThreadEventItemDelta {
				state.messageContent[messageID] += envelope.Delta
				state.openMessages[messageID] = struct{}{}
				if _, exists := state.messageCreated[messageID]; !exists {
					state.messageCreated[messageID] = event.CreatedAt
				}
			}
			if envelopeDecoded && envelope.Item.Type == assistantThreadEventAssistantMessage {
				if event.Type == assistantThreadEventItemStarted || event.Type == assistantThreadEventItemDelta {
					durableActiveMessageID = messageID
				}
				if envelope.Item.Mode != "" {
					state.messageMode[messageID] = envelope.Item.Mode
				}
				if envelope.Item.Revision != 0 {
					state.messageRevision[messageID] = envelope.Item.Revision
				}
				if !envelope.Item.CreatedAt.IsZero() {
					state.messageCreated[messageID] = envelope.Item.CreatedAt
				} else if _, exists := state.messageCreated[messageID]; !exists {
					state.messageCreated[messageID] = event.CreatedAt
				}
				switch event.Type {
				case assistantThreadEventItemStarted:
					state.messageStarted[messageID] = true
					state.openMessages[messageID] = struct{}{}
				case assistantThreadEventItemCompleted:
					state.messageContent[messageID] = envelope.Item.Content
					delete(state.openMessages, messageID)
				}
			}
		}
		if event.ItemID == activeMessageID {
			if envelopeDecoded {
				switch event.Type {
				case assistantThreadEventItemDelta:
					state.lastContent += envelope.Delta
				case assistantThreadEventItemCompleted:
					if envelope.Item.ID != "" {
						state.lastContent = envelope.Item.Content
						state.terminalItem = true
					}
				}
			}
		}
		if event.Type == assistantThreadEventItemCompleted && event.ItemID == "plan-"+turnID {
			if envelopeDecoded {
				state.lastPlan = string(envelope.Item.Data)
			}
		}
		if event.ItemID != "" && (event.Type == assistantThreadEventItemStarted || event.Type == assistantThreadEventItemCompleted) {
			if envelopeDecoded && envelope.Item.Type == assistantThreadEventDynamicToolCall {
				var action projectAssistantActionFeedItem
				if json.Unmarshal(envelope.Item.Data, &action) == nil && action.ID != "" &&
					event.ItemID == assistantThreadDynamicToolItemID(activeMessageID, action.ID) {
					state.actionStatuses[event.ItemID] = action.Status
				}
			}
		}
		switch event.Type {
		case assistantThreadEventApprovalRequested, assistantThreadEventUserInputRequested:
			state.lastRequestID = event.RequestID
			state.lastRequestType = event.Type
		case assistantThreadEventApprovalResolved, assistantThreadEventUserInputResolved:
			if event.RequestID == state.lastRequestID {
				state.lastRequestID, state.lastRequestType = "", ""
			}
		}
	}
	if durableActiveMessageID != "" {
		state.activeMessageID = durableActiveMessageID
	}
	return state, nil
}

func (s *Server) mirrorAssistantRunIntoThread(scope store.Scope, threadID string, turn store.AssistantTurn, run store.AssistantRun) {
	ctx := context.Background()
	state, err := s.loadAssistantThreadMirrorStateWithRetry(ctx, scope, threadID, run.ActiveMessageID, turn.ID)
	if err != nil {
		s.reportAssistantThreadMirrorFailure(scope, turn, err)
		return
	}
	if state.terminalEvent {
		return
	}
	updates, unsubscribe, err := s.projectAssistantSupervisor().Subscribe(scope, run.ID, 0)
	if err != nil {
		if errors.Is(err, store.ErrAssistantRunNotFound) {
			// A very fast worker can terminalize and be removed from the
			// in-memory supervisor before this goroutine reaches Subscribe. The
			// durable run/message pair is still sufficient to finish the thread
			// projection, and the event stream/state reload keeps this fallback
			// idempotent when a prior mirror already committed the terminal event.
			currentRun, runErr := s.store.GetAssistantRun(ctx, scope, run.ID)
			if runErr != nil {
				s.reportAssistantThreadMirrorFailure(scope, turn, runErr)
				return
			}
			activeMessageID := strings.TrimSpace(currentRun.ActiveMessageID)
			if activeMessageID == "" {
				activeMessageID = strings.TrimSpace(run.ActiveMessageID)
			}
			message, messageErr := s.findProjectMessage(ctx, scope, activeMessageID)
			if messageErr != nil {
				s.reportAssistantThreadMirrorFailure(scope, turn, messageErr)
				return
			}
			if projectionErr := s.projectAssistantThreadSnapshotWithRetry(ctx, scope, threadID, turn, currentRun, &state, projectAssistantRunSnapshot{Run: currentRun, Message: message}); projectionErr != nil {
				s.reportAssistantThreadMirrorFailure(scope, turn, projectionErr)
			}
			return
		}
		s.reportAssistantThreadMirrorFailure(scope, turn, err)
		return
	}
	defer unsubscribe()
	for snapshot := range updates {
		if err := s.projectAssistantThreadSnapshotWithRetry(ctx, scope, threadID, turn, run, &state, snapshot); err != nil {
			s.reportAssistantThreadMirrorFailure(scope, turn, err)
			return
		}
		if state.terminalEvent {
			return
		}
	}
}

// startAssistantThreadMirror owns one live provider-to-thread projection per
// turn. A thread start, approval resume, and input resume can all observe the
// same run; only the first caller should subscribe. The durable event stream
// remains the idempotency boundary, while this in-process guard avoids
// duplicate subscribers and the resulting retry churn. The guard is released
// when the mirror reaches a terminal event or otherwise stops, allowing a
// later restart/recovery attempt to reattach it.
func (s *Server) startAssistantThreadMirror(scope store.Scope, threadID string, turn store.AssistantTurn, run store.AssistantRun) {
	threadID = strings.TrimSpace(threadID)
	if s == nil || threadID == "" || strings.TrimSpace(turn.ID) == "" || strings.TrimSpace(run.ID) == "" {
		return
	}
	key := assistantThreadMirrorKey(scope, threadID, turn.ID)
	s.mu.Lock()
	if s.assistantThreadMirrors == nil {
		s.assistantThreadMirrors = map[string]struct{}{}
	}
	if _, exists := s.assistantThreadMirrors[key]; exists {
		s.mu.Unlock()
		return
	}
	s.assistantThreadMirrors[key] = struct{}{}
	s.mu.Unlock()

	go func() {
		defer func() {
			s.mu.Lock()
			delete(s.assistantThreadMirrors, key)
			s.mu.Unlock()
		}()
		s.mirrorAssistantRunIntoThread(scope, threadID, turn, run)
	}()
}

func assistantThreadMirrorKey(scope store.Scope, threadID, turnID string) string {
	return fmt.Sprintf("%d:%s%d:%s%d:%s%d:%s%d:%s%d:%s",
		len(scope.OrgUUID), scope.OrgUUID,
		len(scope.WorkspaceUUID), scope.WorkspaceUUID,
		len(scope.ProjectName), scope.ProjectName,
		len(scope.ProjectUID), scope.ProjectUID,
		len(threadID), threadID,
		len(turnID), turnID,
	)
}

func (s *Server) loadAssistantThreadMirrorStateWithRetry(ctx context.Context, scope store.Scope, threadID, activeMessageID, turnID string) (assistantThreadMirrorState, error) {
	var lastErr error
	for attempt := 0; attempt < assistantThreadMirrorPersistMaxAttempts; attempt++ {
		state, err := s.loadAssistantThreadMirrorState(ctx, scope, threadID, activeMessageID, turnID)
		if err == nil {
			return state, nil
		}
		lastErr = err
		if attempt+1 == assistantThreadMirrorPersistMaxAttempts {
			break
		}
		if err := waitForAssistantThreadMirrorRetry(ctx, attempt); err != nil {
			return assistantThreadMirrorState{}, err
		}
	}
	return assistantThreadMirrorState{}, fmt.Errorf("load assistant thread mirror state after %d attempts: %w", assistantThreadMirrorPersistMaxAttempts, lastErr)
}

// closeStaleAssistantThreadMessages retires assistant message segments that
// were left open when steering rotated the active message or when a provider
// restart interrupted mirror delivery. The event stream is the source of
// truth, so writing an item.completed event makes both live and reload
// projections agree without mutating the old segment's identity.
func (s *Server) closeStaleAssistantThreadMessages(ctx context.Context, scope store.Scope, threadID, turnID, activeMessageID string, state *assistantThreadMirrorState) error {
	if state == nil {
		return errors.New("assistant thread mirror state is required")
	}
	if state.openMessages == nil {
		state.openMessages = map[string]struct{}{}
	}
	if state.messageStarted == nil {
		state.messageStarted = map[string]bool{}
	}
	if state.messageContent == nil {
		state.messageContent = map[string]string{}
	}
	if state.messageCreated == nil {
		state.messageCreated = map[string]time.Time{}
	}
	if state.messageMode == nil {
		state.messageMode = map[string]store.AssistantRunMode{}
	}
	if state.messageRevision == nil {
		state.messageRevision = map[string]int64{}
	}
	stale := make([]string, 0, len(state.openMessages))
	for messageID := range state.openMessages {
		if messageID != "" && messageID != activeMessageID {
			stale = append(stale, messageID)
		}
	}
	sort.Strings(stale)
	for _, messageID := range stale {
		content := state.messageContent[messageID]
		createdAt := state.messageCreated[messageID]
		var metadata map[string]any
		if message, err := s.findProjectMessage(ctx, scope, messageID); err == nil {
			if content == "" {
				content = message.Content
			}
			if createdAt.IsZero() {
				createdAt = message.CreatedAt
			}
			metadata = message.Metadata
		}
		if createdAt.IsZero() {
			createdAt = time.Now().UTC()
		}
		revision := state.messageRevision[messageID]
		if revision == 0 && metadata != nil {
			encodedRevision, marshalErr := json.Marshal(metadata[projectAssistantMetadataRevision])
			if marshalErr == nil {
				_ = json.Unmarshal(encodedRevision, &revision)
			}
		}
		item := assistantThreadItemWithMessagePresentation(assistantThreadItem{
			ID:                 messageID,
			TurnID:             turnID,
			Type:               assistantThreadEventAssistantMessage,
			Status:             "completed",
			Content:            content,
			AssistantMessageID: messageID,
			Mode:               state.messageMode[messageID],
			Revision:           revision,
			CreatedAt:          createdAt,
		}, metadata)
		payload, err := json.Marshal(map[string]any{"item": item})
		if err != nil {
			return fmt.Errorf("encode stale assistant thread terminal item: %w", err)
		}
		if _, err := s.appendAssistantThreadMirrorEvent(ctx, scope, state, store.AssistantThreadEvent{
			ThreadID: threadID,
			TurnID:   turnID,
			Type:     assistantThreadEventItemCompleted,
			ItemID:   messageID,
			Payload:  payload,
		}); err != nil {
			return fmt.Errorf("persist stale assistant thread terminal item %q: %w", messageID, err)
		}
		delete(state.openMessages, messageID)
		state.messageStarted[messageID] = true
	}
	return nil
}

// projectAssistantThreadSnapshotWithRetry is the mirror's persistence barrier.
// After every failed attempt, reload state from the durable stream before
// retrying. This resolves ambiguous commit errors: if the write committed but
// its acknowledgement was lost, the retry observes it instead of appending a
// duplicate semantic event.
// The bound prevents a permanently failing store from leaking a goroutine.
func (s *Server) projectAssistantThreadSnapshotWithRetry(ctx context.Context, scope store.Scope, threadID string, turn store.AssistantTurn, run store.AssistantRun, state *assistantThreadMirrorState, snapshot projectAssistantRunSnapshot) error {
	if state == nil {
		return errors.New("assistant thread mirror state is required")
	}
	release := s.acquireAssistantThreadProjectionLock(scope, threadID, turn.ID)
	defer release()

	var lastErr error
	for attempt := 0; attempt < assistantThreadMirrorPersistMaxAttempts; attempt++ {
		if !state.reconstructed || state.needsReload {
			activeMessageID := strings.TrimSpace(snapshot.Run.ActiveMessageID)
			if activeMessageID == "" {
				activeMessageID = strings.TrimSpace(run.ActiveMessageID)
			}
			durableState, err := s.loadAssistantThreadMirrorState(ctx, scope, threadID, activeMessageID, turn.ID)
			if err != nil {
				lastErr = errors.Join(lastErr, fmt.Errorf("reload assistant thread projection state: %w", err))
				if attempt+1 == assistantThreadMirrorPersistMaxAttempts {
					break
				}
				if err := waitForAssistantThreadMirrorRetry(ctx, attempt); err != nil {
					return err
				}
				continue
			}
			*state = durableState
		}
		if err := s.projectAssistantThreadSnapshot(ctx, scope, threadID, turn, run, state, snapshot); err == nil {
			return nil
		} else {
			lastErr = err
		}
		if attempt+1 == assistantThreadMirrorPersistMaxAttempts {
			break
		}
		if err := waitForAssistantThreadMirrorRetry(ctx, attempt); err != nil {
			return err
		}
	}
	return fmt.Errorf("assistant thread projection failed after %d attempts: %w", assistantThreadMirrorPersistMaxAttempts, lastErr)
}

func waitForAssistantThreadMirrorRetry(ctx context.Context, attempt int) error {
	delay := assistantThreadMirrorRetryBaseDelay << attempt
	timer := time.NewTimer(delay)
	select {
	case <-ctx.Done():
		timer.Stop()
		return context.Cause(ctx)
	case <-timer.C:
		return nil
	}
}

func (s *Server) reportAssistantThreadMirrorFailure(scope store.Scope, turn store.AssistantTurn, err error) {
	if err == nil {
		return
	}
	klog.Background().Error(err, "assistant thread mirror stopped before durable projection", "org", scope.OrgUUID, "workspace", scope.WorkspaceUUID, "project", scope.ProjectName, "thread", turn.ThreadID, "turn", turn.ID)
}

func (s *Server) projectAssistantThreadSnapshot(ctx context.Context, scope store.Scope, threadID string, turn store.AssistantTurn, run store.AssistantRun, state *assistantThreadMirrorState, snapshot projectAssistantRunSnapshot) error {
	if state == nil {
		return errors.New("assistant thread mirror state is required")
	}
	activeMessageID := strings.TrimSpace(snapshot.Run.ActiveMessageID)
	if activeMessageID == "" {
		activeMessageID = strings.TrimSpace(run.ActiveMessageID)
	}
	if !state.reconstructed || state.needsReload {
		durableMessageID := activeMessageID
		if durableMessageID == "" {
			durableMessageID = run.ActiveMessageID
		}
		durableState, err := s.loadAssistantThreadMirrorState(ctx, scope, threadID, durableMessageID, turn.ID)
		if err != nil {
			return fmt.Errorf("reconstruct assistant thread mirror state: %w", err)
		}
		*state = durableState
	}
	if activeMessageID == "" {
		activeMessageID = state.activeMessageID
	}
	if state.openMessages == nil {
		state.openMessages = map[string]struct{}{}
	}
	if state.messageStarted == nil {
		state.messageStarted = map[string]bool{}
	}
	if state.messageContent == nil {
		state.messageContent = map[string]string{}
	}
	if state.messageCreated == nil {
		state.messageCreated = map[string]time.Time{}
	}
	if state.messageMode == nil {
		state.messageMode = map[string]store.AssistantRunMode{}
	}
	if state.messageRevision == nil {
		state.messageRevision = map[string]int64{}
	}
	if err := s.closeStaleAssistantThreadMessages(ctx, scope, threadID, turn.ID, activeMessageID, state); err != nil {
		return err
	}
	if state.terminalEvent {
		return nil
	}
	segmentRotated := activeMessageID != "" && state.activeMessageID != activeMessageID
	if segmentRotated {
		// Steering rotates the durable assistant target while preserving the
		// prior segment. Start deltas and terminalization against the snapshot's
		// target so repeated provider call IDs/content cannot bleed across
		// assistant segments within one turn.
		state.activeMessageID = activeMessageID
		state.lastContent = ""
		state.terminalItem = false
		state.actionStatuses = map[string]string{}
	}
	if segmentRotated && !state.messageStarted[activeMessageID] {
		item := assistantThreadAgentMessageItem(turn, snapshot.Run, "in_progress", "", snapshot.Message.CreatedAt, snapshot.Message.Metadata)
		if item.CreatedAt.IsZero() {
			item.CreatedAt = time.Now().UTC()
		}
		payload, err := json.Marshal(map[string]any{"item": item})
		if err != nil {
			return fmt.Errorf("encode assistant thread segment item: %w", err)
		}
		if _, err := s.appendAssistantThreadMirrorEvent(ctx, scope, state, store.AssistantThreadEvent{ThreadID: threadID, TurnID: turn.ID, Type: assistantThreadEventItemStarted, ItemID: activeMessageID, Payload: payload}); err != nil {
			return fmt.Errorf("persist assistant thread segment item: %w", err)
		}
		state.messageStarted[activeMessageID] = true
		state.messageCreated[activeMessageID] = item.CreatedAt
		state.messageMode[activeMessageID] = item.Mode
		state.messageRevision[activeMessageID] = item.Revision
		state.openMessages[activeMessageID] = struct{}{}
	}
	if state.actionStatuses == nil {
		state.actionStatuses = map[string]string{}
	}
	content := snapshot.Message.Content
	if strings.HasPrefix(content, state.lastContent) && len(content) > len(state.lastContent) {
		delta := content[len(state.lastContent):]
		payload, err := json.Marshal(map[string]any{"delta": delta})
		if err != nil {
			return fmt.Errorf("encode assistant thread message delta: %w", err)
		}
		if _, err := s.appendAssistantThreadMirrorEvent(ctx, scope, state, store.AssistantThreadEvent{ThreadID: threadID, TurnID: turn.ID, Type: assistantThreadEventItemDelta, ItemID: activeMessageID, Payload: payload}); err != nil {
			return fmt.Errorf("persist assistant thread message delta: %w", err)
		}
		state.lastContent = content
		state.messageContent[activeMessageID] = content
		state.openMessages[activeMessageID] = struct{}{}
	} else if content == state.lastContent {
		// The snapshot did not add durable content. Keep the state unchanged.
	} else if state.lastContent == "" && content == "" {
		state.lastContent = content
	}

	for _, action := range projectAssistantActionFeedFromMetadata(snapshot.Message.Metadata[projectMessageMetadataAssistantActionFeed]) {
		itemID := assistantThreadDynamicToolItemID(activeMessageID, action.ID)
		if state.actionStatuses[itemID] == action.Status {
			continue
		}
		status := "in_progress"
		eventType := assistantThreadEventItemStarted
		switch action.Status {
		case projectAssistantActionFeedStatusSucceeded, projectAssistantActionFeedStatusSkipped:
			status, eventType = "completed", assistantThreadEventItemCompleted
		case projectAssistantActionFeedStatusFailed, projectAssistantActionFeedStatusRejected:
			status, eventType = "failed", assistantThreadEventItemCompleted
		}
		data, err := json.Marshal(action)
		if err != nil {
			return fmt.Errorf("encode assistant thread action: %w", err)
		}
		item := assistantThreadItem{
			ID:                 itemID,
			TurnID:             turn.ID,
			Type:               assistantThreadEventDynamicToolCall,
			Status:             status,
			Content:            action.Title,
			Data:               data,
			AssistantMessageID: activeMessageID,
			CreatedAt:          time.Now().UTC(),
		}
		payload, err := json.Marshal(map[string]any{"item": item})
		if err != nil {
			return fmt.Errorf("encode assistant thread action item: %w", err)
		}
		if _, err := s.appendAssistantThreadMirrorEvent(ctx, scope, state, store.AssistantThreadEvent{ThreadID: threadID, TurnID: turn.ID, Type: eventType, ItemID: itemID, Payload: payload}); err != nil {
			return fmt.Errorf("persist assistant thread action %q: %w", action.ID, err)
		}
		state.actionStatuses[itemID] = action.Status
	}

	if planValue, exists := snapshot.Message.Metadata[projectAssistantMetadataPlan]; exists {
		planData, err := json.Marshal(planValue)
		if err != nil {
			return fmt.Errorf("encode assistant thread plan: %w", err)
		}
		if string(planData) != state.lastPlan {
			item := assistantThreadItem{ID: "plan-" + turn.ID, TurnID: turn.ID, Type: assistantThreadEventPlan, Status: "in_progress", Data: planData, AssistantMessageID: activeMessageID, CreatedAt: time.Now().UTC()}
			payload, err := json.Marshal(map[string]any{"item": item})
			if err != nil {
				return fmt.Errorf("encode assistant thread plan item: %w", err)
			}
			if _, err := s.appendAssistantThreadMirrorEvent(ctx, scope, state, store.AssistantThreadEvent{ThreadID: threadID, TurnID: turn.ID, Type: assistantThreadEventItemCompleted, ItemID: item.ID, Payload: payload}); err != nil {
				return fmt.Errorf("persist assistant thread plan: %w", err)
			}
			state.lastPlan = string(planData)
		}
	}

	requestStillPending := snapshot.Run.RequestID == state.lastRequestID &&
		((state.lastRequestType == assistantThreadEventApprovalRequested && snapshot.Run.Status == store.AssistantRunStatusPendingPermission) ||
			(state.lastRequestType == assistantThreadEventUserInputRequested && snapshot.Run.Status == store.AssistantRunStatusPendingInput))
	if state.lastRequestID != "" && !requestStillPending {
		resolution, err := assistantThreadPendingRequestResolution(turn.ID, *state)
		if err != nil {
			return fmt.Errorf("encode assistant thread request resolution: %w", err)
		}
		resolution.ThreadID = threadID
		if _, err := s.appendAssistantThreadMirrorEvent(ctx, scope, state, resolution); err != nil {
			return fmt.Errorf("persist assistant thread request resolution: %w", err)
		}
		state.lastRequestID, state.lastRequestType = "", ""
	}

	if snapshot.Run.RequestID != "" && snapshot.Run.RequestID != state.lastRequestID {
		eventType := ""
		switch snapshot.Run.Status {
		case store.AssistantRunStatusPendingPermission:
			eventType = assistantThreadEventApprovalRequested
		case store.AssistantRunStatusPendingInput:
			eventType = assistantThreadEventUserInputRequested
		}
		if eventType != "" {
			interrupt := projectAssistantUIInterruptFromMetadata(snapshot.Message.Metadata[projectMessageMetadataAssistantInterrupt])
			if !assistantThreadInterruptMatchesPendingRequest(interrupt, eventType, snapshot.Run.ID, snapshot.Run.RequestID) {
				return nil
			}
			interruptData, err := json.Marshal(interrupt)
			if err != nil {
				return fmt.Errorf("encode assistant thread pending request: %w", err)
			}
			itemType := "approval"
			if eventType == assistantThreadEventUserInputRequested {
				itemType = "input"
			}
			item := assistantThreadItem{ID: snapshot.Run.RequestID, TurnID: turn.ID, Type: itemType, Status: "in_progress", Data: interruptData, AssistantMessageID: activeMessageID, CreatedAt: time.Now().UTC()}
			payload, err := json.Marshal(map[string]any{"requestID": snapshot.Run.RequestID, "interrupt": interrupt, "item": item})
			if err != nil {
				return fmt.Errorf("encode assistant thread pending request item: %w", err)
			}
			if _, err := s.appendAssistantThreadMirrorEvent(ctx, scope, state, store.AssistantThreadEvent{ThreadID: threadID, TurnID: turn.ID, Type: eventType, ItemID: snapshot.Run.RequestID, RequestID: snapshot.Run.RequestID, Payload: payload}); err != nil {
				return fmt.Errorf("persist assistant thread pending request: %w", err)
			}
			state.lastRequestID = snapshot.Run.RequestID
			state.lastRequestType = eventType
		}
	}

	if !assistantRunTerminal(snapshot.Run.Status) {
		return nil
	}
	if !state.terminalItem {
		terminalItem := assistantThreadAgentMessageItem(turn, snapshot.Run, assistantThreadRunItemStatus(snapshot.Run.Status), content, snapshot.Message.CreatedAt, snapshot.Message.Metadata)
		item := terminalItem
		payload, err := json.Marshal(map[string]any{"item": item})
		if err != nil {
			return fmt.Errorf("encode assistant thread terminal item: %w", err)
		}
		if _, err := s.appendAssistantThreadMirrorEvent(ctx, scope, state, store.AssistantThreadEvent{ThreadID: threadID, TurnID: turn.ID, Type: assistantThreadEventItemCompleted, ItemID: item.ID, Payload: payload}); err != nil {
			return fmt.Errorf("persist assistant thread terminal item: %w", err)
		}
		state.terminalItem = true
		state.messageContent[activeMessageID] = content
		state.messageCreated[activeMessageID] = item.CreatedAt
		delete(state.openMessages, activeMessageID)
	}
	if state.terminalEvent {
		return nil
	}
	turn.UpdatedAt = time.Now().UTC()
	terminalType := assistantThreadEventTurnCompleted
	switch snapshot.Run.Status {
	case store.AssistantRunStatusCompleted:
		turn.Status = store.AssistantTurnStatusCompleted
	case store.AssistantRunStatusInterrupted, store.AssistantRunStatusAborted:
		turn.Status = store.AssistantTurnStatusInterrupted
		terminalType = assistantThreadEventTurnInterrupted
	default:
		turn.Status = store.AssistantTurnStatusFailed
		turn.Error = snapshot.Run.Error
		terminalType = assistantThreadEventTurnFailed
	}
	turnPayload, err := json.Marshal(map[string]any{"turn": turn})
	if err != nil {
		return fmt.Errorf("encode assistant thread terminal turn: %w", err)
	}
	if err := s.saveAssistantThreadTurnWithEvent(ctx, scope, state, turn, store.AssistantThreadEvent{ThreadID: threadID, TurnID: turn.ID, Type: terminalType, Payload: turnPayload}); err != nil {
		return fmt.Errorf("persist assistant thread terminal turn: %w", err)
	}
	state.terminalEvent = true
	return nil
}

func assistantThreadPendingRequestResolution(turnID string, state assistantThreadMirrorState) (store.AssistantThreadEvent, error) {
	resolvedType := assistantThreadEventApprovalResolved
	itemType := "approval"
	if state.lastRequestType == assistantThreadEventUserInputRequested {
		resolvedType = assistantThreadEventUserInputResolved
		itemType = "input"
	}
	item := assistantThreadItem{ID: state.lastRequestID, TurnID: turnID, Type: itemType, Status: "completed", AssistantMessageID: state.activeMessageID, CreatedAt: time.Now().UTC()}
	payload, err := json.Marshal(map[string]any{"requestID": state.lastRequestID, "item": item})
	if err != nil {
		return store.AssistantThreadEvent{}, err
	}
	return store.AssistantThreadEvent{
		TurnID:    turnID,
		Type:      resolvedType,
		ItemID:    state.lastRequestID,
		RequestID: state.lastRequestID,
		Payload:   payload,
	}, nil
}

// appendAssistantThreadMirrorEvent uses the sequence reconstructed at the
// start of the mirror turn. A successful append advances it locally; failures
// mark the state for a durable reload before retrying, covering both ordinary
// errors and acknowledgements lost after a commit.
func (s *Server) appendAssistantThreadMirrorEvent(ctx context.Context, scope store.Scope, state *assistantThreadMirrorState, event store.AssistantThreadEvent) (store.AssistantThreadEvent, error) {
	if state == nil {
		return store.AssistantThreadEvent{}, errors.New("assistant thread mirror state is required")
	}
	created, err := s.store.AppendAssistantThreadEvent(ctx, scope, event, state.lastSequence)
	if err != nil {
		state.needsReload = true
		return store.AssistantThreadEvent{}, err
	}
	state.lastSequence = created.Sequence
	return created, nil
}

func (s *Server) saveAssistantThreadTurnWithEvent(ctx context.Context, scope store.Scope, state *assistantThreadMirrorState, turn store.AssistantTurn, event store.AssistantThreadEvent) error {
	if state == nil {
		return errors.New("assistant thread mirror state is required")
	}
	if err := s.store.SaveAssistantTurnWithEvent(ctx, scope, turn, event, state.lastSequence); err != nil {
		state.needsReload = true
		return err
	}
	state.lastSequence++
	return nil
}
