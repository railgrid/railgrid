// Copyright 2026 The Railgrid Authors.
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
	"reflect"
	"strings"
	"time"

	"github.com/railgrid/provider-app-studio/store"
	"github.com/railgrid/provider-app-studio/workspace"
)

const (
	projectAssistantExecReceiptRecoveryPageSize   = 128
	projectAssistantExecReceiptSnapshotMaxRetries = 4
	projectAssistantExecReceiptThreadMaxPages     = 32
	projectAssistantExecReceiptThreadMaxRetries   = 8
)

var errProjectAssistantExecReceiptLookupLimit = errors.New("assistant exec receipt lookup exceeded its bounded event window")

type projectAssistantExecReceiptBatchDeadlineKey struct{}

func detachedProjectAssistantExecReceiptBatchContext(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	if timeout <= 0 {
		timeout = projectMessagePersistTimeout
	}
	deadline := time.Now().Add(timeout)
	if parentDeadline, ok := ctx.Deadline(); ok && parentDeadline.Before(deadline) {
		deadline = parentDeadline
	}
	base := context.WithoutCancel(ctx)
	bounded, cancel := context.WithDeadline(base, deadline)
	return context.WithValue(bounded, projectAssistantExecReceiptBatchDeadlineKey{}, deadline), cancel
}

// projectAssistantExecReceiptPersistenceContext shares a recovery batch's
// single deadline across all receipt writes. Standalone callback writes keep
// the normal bounded detached persistence budget.
func projectAssistantExecReceiptPersistenceContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx != nil {
		if deadline, ok := ctx.Value(projectAssistantExecReceiptBatchDeadlineKey{}).(time.Time); ok {
			return context.WithDeadline(context.WithoutCancel(ctx), deadline)
		}
	}
	return detachedProjectPersistenceContext(ctx)
}

// forEachSettledProjectAssistantExecReceipt validates the exact run in
// ascending sequence order before visiting any receipt. The store query is
// paged and the caller's context bounds total scan time. The ledger retains
// state for each call, and receipts are staged until the full ledger validates,
// so memory grows with the run rather than only with one page.
func (s *Server) forEachSettledProjectAssistantExecReceipt(ctx context.Context, scope store.Scope, runID string, visit func(projectToolCallStreamEvent) error) error {
	if s == nil || s.store == nil || strings.TrimSpace(runID) == "" {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	runID = strings.TrimSpace(runID)
	ledger := newProjectAssistantRunEventLedger(s.store, scope, runID)
	receipts, err := func() ([]projectToolCallStreamEvent, error) {
		ledger.mu.Lock()
		defer ledger.mu.Unlock()
		ledger.loaded = true
		ledger.lastSequence = 0
		ledger.calls = map[string]*projectAssistantRunToolCallState{}

		var receipts []projectToolCallStreamEvent
		seen := map[string]struct{}{}
		afterSequence := int64(0)
		for {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			page, err := s.store.ListAssistantRunEvents(ctx, scope, runID, afterSequence, projectAssistantExecReceiptRecoveryPageSize)
			if err != nil {
				return nil, fmt.Errorf("list durable exec receipt events: %w", err)
			}
			if len(page) > projectAssistantExecReceiptRecoveryPageSize {
				return nil, fmt.Errorf("%w: store returned %d events for a page limit of %d", errProjectAssistantRunToolLedgerCorrupt, len(page), projectAssistantExecReceiptRecoveryPageSize)
			}
			if len(page) == 0 {
				break
			}
			for _, result := range page {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
				if result.RunID != runID || result.Sequence <= afterSequence {
					return nil, fmt.Errorf("%w: invalid event ordering or run identity", errProjectAssistantRunToolLedgerCorrupt)
				}
				if result.Sequence != ledger.lastSequence+1 {
					return nil, fmt.Errorf("%w: event sequence advanced from %d to %d", errProjectAssistantRunToolLedgerCorrupt, ledger.lastSequence, result.Sequence)
				}
				if err := ledger.applyEventLocked(result); err != nil {
					return nil, err
				}
				afterSequence = result.Sequence
				if result.Type != projectAssistantRunToolResultEventType || projectToolBaseName(result.ToolName) != projectToolExecCommand {
					continue
				}
				callID := strings.TrimSpace(result.CallID)
				if callID == "" {
					return nil, fmt.Errorf("%w: exec result event has no call ID", errProjectAssistantRunToolLedgerCorrupt)
				}
				if _, ok := seen[callID]; ok {
					continue
				}
				state := ledger.calls[callID]
				// A terminal tool response can be written before dispatch, for
				// example when policy or argument validation rejects a call. Such a
				// response is not an exec receipt and must not block later results.
				if state == nil || !state.Dispatched || state.Outcome == nil {
					continue
				}
				if state.Requested && state.RequestArgsDigest == "" {
					return nil, fmt.Errorf("%w: call %q request has no validated input digest", errProjectAssistantRunToolLedgerCorrupt, callID)
				}
				args := map[string]any{}
				if err := json.Unmarshal(state.Arguments, &args); err != nil {
					return nil, fmt.Errorf("%w: call %q arguments are invalid", errProjectAssistantRunToolLedgerCorrupt, callID)
				}
				outcome := *state.Outcome
				outcome.ObservedAt = state.OutcomeAt
				event, found := projectAssistantExecReceiptFromOutcome(callID, result.ToolName, args, outcome)
				if !found {
					continue
				}
				seen[callID] = struct{}{}
				receipts = append(receipts, event)
			}
			if len(page) < projectAssistantExecReceiptRecoveryPageSize {
				break
			}
		}
		return receipts, nil
	}()
	if err != nil {
		return err
	}
	for _, event := range receipts {
		if err := ctx.Err(); err != nil {
			return err
		}
		if visit != nil {
			if err := visit(event); err != nil {
				return err
			}
		}
	}
	return nil
}

func projectAssistantExecReceiptFromLedger(ctx context.Context, ledger *projectAssistantRunEventLedger, callID, toolName string) (projectToolCallStreamEvent, bool, error) {
	if ledger == nil || projectToolBaseName(toolName) != projectToolExecCommand || strings.TrimSpace(callID) == "" {
		return projectToolCallStreamEvent{}, false, nil
	}
	args, outcome, found, err := ledger.DispatchedSettledToolCall(ctx, callID, toolName)
	if err != nil || !found {
		return projectToolCallStreamEvent{}, found, err
	}
	event, found := projectAssistantExecReceiptFromOutcome(callID, toolName, args, outcome)
	return event, found, nil
}

func projectAssistantExecReceiptFromOutcome(callID, toolName string, args map[string]any, outcome projectAssistantRunToolCallOutcome) (projectToolCallStreamEvent, bool) {
	status := projectToolCallTerminalStatus(toolName, outcome.Result, outcome.Succeeded())
	exec := projectAssistantExecMetadataForToolArguments(toolName, args, outcome.Result, status)
	if exec == nil || (outcome.Canceled && exec.ExitCode == nil) || !projectAssistantExecCommandReceiptObserved(exec) {
		// App-side cancellation, a lost START response, and polling failures do
		// not prove what the process did. Preserve the synthetic unknown action
		// until the durable command result contains terminal process evidence.
		return projectToolCallStreamEvent{}, false
	}
	event := projectToolCallStreamEvent{
		ID:         callID,
		Name:       toolName,
		Status:     status,
		Arguments:  summarizeProjectToolArgumentsMap(toolName, args),
		Summary:    summarizeProjectToolResult(toolName, outcome.Result),
		Exec:       exec,
		ObservedAt: outcome.ObservedAt,
	}
	event.FailureDiagnostic = projectAssistantActionFeedDiagnosticFromTypedResult(callID, toolName, outcome.Result)
	if strings.TrimSpace(outcome.Error) != "" && !projectAssistantRunToolCancellation(errors.New(outcome.Error)) {
		event.Error = projectEinoAssistantSafeErrorText(errors.New(outcome.Error))
	}
	return event, true
}

// reconcileProjectAssistantExecReceipt handles a late result after the normal
// callback stream has closed. The call ID selects a candidate only; validated
// dispatched arguments and the durable result are always read from the ledger.
func (s *Server) reconcileProjectAssistantExecReceipt(ctx context.Context, scope store.Scope, runID, callID string) error {
	if s == nil || s.store == nil || strings.TrimSpace(runID) == "" || strings.TrimSpace(callID) == "" {
		return nil
	}
	persistCtx, cancel := detachedProjectAssistantExecReceiptBatchContext(ctx, projectMessagePersistTimeout)
	defer cancel()
	ledger := newProjectAssistantRunEventLedger(s.store, scope, strings.TrimSpace(runID))
	event, found, err := projectAssistantExecReceiptFromLedger(persistCtx, ledger, callID, projectToolExecCommand)
	if err != nil || !found {
		return err
	}
	return s.applyProjectAssistantInterruptedExecReceipt(persistCtx, scope, strings.TrimSpace(runID), event, true)
}

func (s *Server) reconcileProjectAssistantExecReceiptsBeforeFinalization(
	ctx context.Context,
	scope store.Scope,
	workspaceScope workspace.Scope,
	runID string,
	accumulator *projectAssistantSnapshotAccumulator,
	state *projectAssistantDurableMetadataState,
) error {
	persistCtx, cancel := detachedProjectAssistantExecReceiptBatchContext(ctx, projectMessagePersistTimeout)
	defer cancel()
	if accumulator != nil && state == nil {
		return errors.New("assistant exec receipt finalization requires durable metadata state")
	}
	return s.forEachSettledProjectAssistantExecReceipt(persistCtx, scope, runID, func(event projectToolCallStreamEvent) error {
		if err := persistCtx.Err(); err != nil {
			return err
		}
		if state != nil {
			state.upsertToolCall(event)
		}
		if accumulator == nil {
			return nil
		}
		return persistProjectAssistantToolCallSnapshot(persistCtx, func(operationCtx context.Context) error {
			return s.persistProjectAssistantStoppingToolMetadata(operationCtx, accumulator, workspaceScope, state, event)
		})
	})
}

func (s *Server) reconcileInterruptedProjectAssistantExecReceipts(ctx context.Context, scope store.Scope, runID string, repairCanonical bool) error {
	persistCtx, cancel := detachedProjectAssistantExecReceiptBatchContext(ctx, projectMessagePersistTimeout)
	defer cancel()
	return s.forEachSettledProjectAssistantExecReceipt(persistCtx, scope, runID, func(event projectToolCallStreamEvent) error {
		if err := persistCtx.Err(); err != nil {
			return err
		}
		return s.applyProjectAssistantInterruptedExecReceipt(persistCtx, scope, runID, event, repairCanonical)
	})
}

// applyProjectAssistantInterruptedExecReceipt repairs only the exact
// command_outcome_unobserved action created for this interrupted turn. It
// validates the canonical thread, turn, actor, request, and message identities
// before either accumulator mutation or revision-CAS persistence.
func (s *Server) applyProjectAssistantInterruptedExecReceipt(ctx context.Context, scope store.Scope, runID string, event projectToolCallStreamEvent, repairCanonical bool) error {
	if s == nil || s.store == nil || projectToolBaseName(event.Name) != projectToolExecCommand ||
		!projectAssistantToolCallCanSettleStopping(event.Status) || event.Exec == nil ||
		!projectAssistantExecCommandReceiptObserved(event.Exec) {
		return nil
	}
	persistCtx, cancel := projectAssistantExecReceiptPersistenceContext(ctx)
	defer cancel()
	for attempt := 0; attempt < projectAssistantExecReceiptSnapshotMaxRetries; attempt++ {
		run, turn, _, message, eligible, err := s.loadProjectAssistantExecReceiptTarget(persistCtx, scope, runID)
		if err != nil || !eligible {
			return err
		}
		if run.Status != store.AssistantRunStatusInterrupted || turn.Status != store.AssistantTurnStatusInterrupted {
			return nil
		}
		if !projectAssistantInterruptedExecActionCanSettle(message.Metadata, run.Audit, event) {
			return nil
		}
		if projectAssistantInterruptedExecActionAlreadySettled(message.Metadata, event) {
			if repairCanonical {
				return s.reconcileProjectAssistantInterruptedExecCanonicalItem(persistCtx, scope, run, event)
			}
			return nil
		}

		if accumulator := s.projectAssistantSupervisor().accumulatorFor(scope, run.ID, run.ThreadID); accumulator != nil {
			err := accumulator.UpdateStoppingToolSnapshot(persistCtx, event, func(*store.AssistantRun, *store.Message) {})
			if err != nil && !errors.Is(err, store.ErrAssistantRunNotFound) {
				return err
			}
			latestRun, latestTurn, _, latestMessage, latestEligible, readErr := s.loadProjectAssistantExecReceiptTarget(persistCtx, scope, runID)
			if readErr != nil {
				return readErr
			}
			if latestEligible && latestRun.Status == store.AssistantRunStatusInterrupted && latestTurn.Status == store.AssistantTurnStatusInterrupted &&
				projectAssistantInterruptedExecActionAlreadySettled(latestMessage.Metadata, event) {
				if repairCanonical {
					return s.reconcileProjectAssistantInterruptedExecCanonicalItem(persistCtx, scope, latestRun, event)
				}
				return nil
			}
			if errors.Is(err, store.ErrAssistantRunNotFound) || !latestEligible {
				return nil
			}
		}

		changed, err := s.saveProjectAssistantInterruptedExecReceipt(persistCtx, scope, run, &message, event)
		if errors.Is(err, store.ErrAssistantRunConflict) {
			continue
		}
		if err != nil {
			return err
		}
		if !changed {
			return nil
		}
		if repairCanonical {
			return s.reconcileProjectAssistantInterruptedExecCanonicalItem(persistCtx, scope, run, event)
		}
		return nil
	}
	return store.ErrAssistantRunConflict
}

func (s *Server) loadProjectAssistantExecReceiptTarget(ctx context.Context, scope store.Scope, runID string) (store.AssistantRun, store.AssistantTurn, store.AssistantThread, store.Message, bool, error) {
	run, err := s.store.GetAssistantRun(ctx, scope, strings.TrimSpace(runID))
	if errors.Is(err, store.ErrAssistantRunNotFound) {
		return store.AssistantRun{}, store.AssistantTurn{}, store.AssistantThread{}, store.Message{}, false, nil
	}
	if err != nil {
		return store.AssistantRun{}, store.AssistantTurn{}, store.AssistantThread{}, store.Message{}, false, err
	}
	if run.ID != strings.TrimSpace(runID) || strings.TrimSpace(run.ThreadID) == "" ||
		strings.TrimSpace(run.ClientRequestID) == "" || strings.TrimSpace(run.UserMessageID) == "" || strings.TrimSpace(run.ActiveMessageID) == "" ||
		// Both stores stamp the scope on every persisted run, so there is no
		// legacy empty-scope form to accept during receipt recovery.
		run.ProjectName != scope.ProjectName || run.ProjectUID != scope.ProjectUID {
		return store.AssistantRun{}, store.AssistantTurn{}, store.AssistantThread{}, store.Message{}, false, nil
	}
	thread, err := s.store.GetAssistantThread(ctx, scope, run.ThreadID)
	if errors.Is(err, store.ErrAssistantThreadNotFound) {
		return store.AssistantRun{}, store.AssistantTurn{}, store.AssistantThread{}, store.Message{}, false, nil
	}
	if err != nil {
		return store.AssistantRun{}, store.AssistantTurn{}, store.AssistantThread{}, store.Message{}, false, err
	}
	turn, err := s.store.GetAssistantTurn(ctx, scope, run.ThreadID, run.ID)
	if errors.Is(err, store.ErrAssistantTurnNotFound) {
		return store.AssistantRun{}, store.AssistantTurn{}, store.AssistantThread{}, store.Message{}, false, nil
	}
	if err != nil {
		return store.AssistantRun{}, store.AssistantTurn{}, store.AssistantThread{}, store.Message{}, false, err
	}
	if thread.ID != run.ThreadID || thread.ActorID == "" || turn.ID != run.ID || turn.ThreadID != run.ThreadID || turn.ActorID != thread.ActorID ||
		turn.ClientUserMessageID != run.ClientRequestID || (run.Mode != "" && turn.Mode != run.Mode) ||
		(run.ApprovalMode != "" && turn.ApprovalMode != run.ApprovalMode) {
		return store.AssistantRun{}, store.AssistantTurn{}, store.AssistantThread{}, store.Message{}, false, nil
	}
	messages, err := s.store.GetMessagesByIDs(ctx, scope, []string{run.UserMessageID, run.ActiveMessageID})
	if err != nil {
		return store.AssistantRun{}, store.AssistantTurn{}, store.AssistantThread{}, store.Message{}, false, err
	}
	byID := make(map[string]store.Message, len(messages))
	for _, message := range messages {
		byID[message.ID] = message
	}
	userMessage, userOK := byID[run.UserMessageID]
	activeMessage, activeOK := byID[run.ActiveMessageID]
	if !userOK || !activeOK || userMessage.Role != "user" || activeMessage.Role != "assistant" || userMessage.ActorID != thread.ActorID {
		return store.AssistantRun{}, store.AssistantTurn{}, store.AssistantThread{}, store.Message{}, false, nil
	}
	return run, turn, thread, activeMessage, true, nil
}

func (s *Server) saveProjectAssistantInterruptedExecReceipt(ctx context.Context, scope store.Scope, run store.AssistantRun, message *store.Message, event projectToolCallStreamEvent) (bool, error) {
	if message == nil || !projectAssistantInterruptedExecActionCanSettle(message.Metadata, run.Audit, event) {
		return false, nil
	}
	updatedAudit, ok := projectAssistantAuditInterruptedExecUpdate(run.Audit, event)
	if !ok {
		return false, nil
	}
	actions := projectAssistantActionFeedFromMetadata(message.Metadata[projectMessageMetadataAssistantActionFeed])
	want := projectAssistantActionFeedItemFromToolCall(event)
	for _, action := range actions {
		if action.ID == want.ID {
			want.Sequence = action.Sequence
			break
		}
	}
	metadata := make(map[string]any, len(message.Metadata)+1)
	for key, value := range message.Metadata {
		metadata[key] = value
	}
	metadata[projectMessageMetadataAssistantActionFeed] = projectAssistantReplaceActionFeedItem(actions, want)
	updatedRun := run
	updatedRun.Audit = updatedAudit
	updatedRun.Revision++
	updatedRun.UpdatedAt = time.Now().UTC()
	message.UpdatedAt = updatedRun.UpdatedAt
	message.Metadata = projectAssistantDurableMetadataFromExisting(updatedRun, "Interrupted", false, metadata)
	if err := s.store.SaveAssistantRunSnapshot(ctx, scope, updatedRun, []store.Message{*message}, run.Revision); err != nil {
		return false, err
	}
	return true, nil
}

// projectAssistantReplaceActionFeedItem removes the synthetic unknown-result
// diagnostic when the exact durable terminal receipt replaces that row.
func projectAssistantReplaceActionFeedItem(actions []projectAssistantActionFeedItem, next projectAssistantActionFeedItem) []projectAssistantActionFeedItem {
	if !projectAssistantActionFeedItemVisible(next) {
		filtered := actions[:0]
		for _, action := range actions {
			if action.ID != next.ID {
				filtered = append(filtered, action)
			}
		}
		return filtered
	}
	for index := range actions {
		if actions[index].ID == next.ID {
			actions[index] = next
			return filterProjectAssistantActionFeedItems(actions)
		}
	}
	return filterProjectAssistantActionFeedItems(append(actions, next))
}

func (s *Server) reconcileProjectAssistantInterruptedExecCanonicalItem(ctx context.Context, scope store.Scope, run store.AssistantRun, event projectToolCallStreamEvent) error {
	run, turn, _, message, eligible, err := s.loadProjectAssistantExecReceiptTarget(ctx, scope, run.ID)
	if err != nil || !eligible {
		return err
	}
	if run.Status != store.AssistantRunStatusInterrupted || turn.Status != store.AssistantTurnStatusInterrupted {
		return nil
	}
	action, actionSettled := projectAssistantInterruptedExecSettledAction(message.Metadata, event)
	if !actionSettled {
		return nil
	}
	threadID := run.ThreadID
	release, err := s.acquireAssistantThreadProjectionLockContext(ctx, scope, threadID, run.ID)
	if err != nil {
		return err
	}
	defer release()
	toolItems, err := s.loadAssistantThreadTurnToolItems(ctx, scope, threadID, run.ID)
	if err != nil {
		return err
	}
	itemID := assistantThreadDynamicToolItemID(run.ActiveMessageID, action.ID)
	item, exists := toolItems.items[itemID]
	if exists {
		if item.Type != assistantThreadEventDynamicToolCall || item.TurnID != run.ID || item.AssistantMessageID != run.ActiveMessageID {
			return nil
		}
		var prior projectAssistantActionFeedItem
		if json.Unmarshal(item.Data, &prior) != nil || prior.ID != action.ID || prior.Exec == nil ||
			!projectAssistantExecActionIdentityMatches(prior.Exec, event.Exec) || prior.Sequence != action.Sequence {
			return nil
		}
		settledItem := item
		settledItem.Status = projectAssistantThreadToolItemStatus(action.Status)
		settledItem.Content = action.Title
		settledItem.Data, err = json.Marshal(action)
		if err != nil {
			return fmt.Errorf("encode settled assistant thread exec item: %w", err)
		}
		settlementPayload, marshalErr := json.Marshal(map[string]any{"item": settledItem, "receiptSettlement": true})
		if marshalErr != nil {
			return fmt.Errorf("encode settled assistant thread exec event: %w", marshalErr)
		}
		settlementEvent := store.AssistantThreadEvent{
			ThreadID: run.ThreadID, TurnID: run.ID, Type: assistantThreadEventItemCompleted,
			ItemID: settledItem.ID, Payload: settlementPayload,
		}
		if !assistantThreadReceiptSettlementCanProjectToolItem(settlementEvent, run.ID, item, true) {
			return nil
		}
		item = settledItem
	} else {
		createdAt := event.ObservedAt
		if createdAt.IsZero() {
			createdAt = message.CreatedAt
		}
		if createdAt.IsZero() {
			createdAt = run.UpdatedAt
		}
		item = assistantThreadItem{
			ID: itemID, TurnID: run.ID, Type: assistantThreadEventDynamicToolCall,
			Status: projectAssistantThreadToolItemStatus(action.Status), Content: action.Title,
			AssistantMessageID: run.ActiveMessageID, Sequence: int64(action.Sequence), CreatedAt: createdAt,
		}
		item.Data, err = json.Marshal(action)
		if err != nil {
			return fmt.Errorf("encode reconstructed assistant thread exec item: %w", err)
		}
	}
	return s.appendProjectAssistantExecReceiptCanonicalEvent(ctx, scope, run, item)
}

func (s *Server) appendProjectAssistantExecReceiptCanonicalEvent(ctx context.Context, scope store.Scope, run store.AssistantRun, item assistantThreadItem) error {
	terminalEvents, err := s.store.ListAssistantThreadTurnEventsAfter(ctx, scope, run.ThreadID, run.ID, assistantThreadEventTurnInterrupted, 0, 1)
	if err != nil {
		return err
	}
	if len(terminalEvents) == 0 {
		return nil
	}
	terminalSequence := terminalEvents[0].Sequence
	if terminalSequence < 1 {
		return fmt.Errorf("assistant interrupted turn %q has an invalid terminal sequence", run.ID)
	}
	payload, err := json.Marshal(map[string]any{"item": item, "receiptSettlement": true})
	if err != nil {
		return fmt.Errorf("encode settled assistant thread exec event: %w", err)
	}
	want := store.AssistantThreadEvent{
		ThreadID: run.ThreadID,
		TurnID:   run.ID,
		Type:     assistantThreadEventItemCompleted,
		ItemID:   item.ID,
		Payload:  payload,
	}
	for attempt := 0; attempt < projectAssistantExecReceiptThreadMaxRetries; attempt++ {
		if attempt > 0 {
			if err := waitProjectAssistantExecReceiptCASRetry(ctx, attempt); err != nil {
				return err
			}
		}
		latest, err := s.store.ListAssistantThreadEventsBefore(ctx, scope, run.ThreadID, 0, 1)
		if err != nil {
			return err
		}
		expectedSequence := int64(0)
		if len(latest) > 0 {
			expectedSequence = latest[len(latest)-1].Sequence
		}
		// Read the tail before checking for a winning semantic append. If another
		// replica appends after this scan, the stale expected sequence makes our
		// CAS lose and the next pass observes that receipt instead of duplicating it.
		found, err := s.findProjectAssistantExecReceiptCanonicalEvent(ctx, scope, run.ThreadID, run.ID, terminalSequence, want)
		if err != nil || found {
			return err
		}
		_, appendErr := s.store.AppendAssistantThreadEvent(ctx, scope, want, expectedSequence)
		if appendErr == nil {
			return nil
		}
		// A failed response may be ambiguous if storage committed before the
		// connection broke. Check the exact semantic event before returning or
		// retrying, so concurrent replicas cannot append duplicate settlements.
		found, readErr := s.findProjectAssistantExecReceiptCanonicalEvent(ctx, scope, run.ThreadID, run.ID, terminalSequence, want)
		if readErr != nil {
			return errors.Join(appendErr, readErr)
		}
		if found {
			return nil
		}
		if !errors.Is(appendErr, store.ErrAssistantThreadEventConflict) {
			return appendErr
		}
	}
	return store.ErrAssistantThreadEventConflict
}

func waitProjectAssistantExecReceiptCASRetry(ctx context.Context, attempt int) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// Keep retries short while preventing immediate cross-replica CAS hammering.
	delay := 5 * time.Millisecond * time.Duration(1<<min(attempt-1, 3))
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return ctx.Err()
	}
}

func (s *Server) findProjectAssistantExecReceiptCanonicalEvent(ctx context.Context, scope store.Scope, threadID, turnID string, afterSequence int64, want store.AssistantThreadEvent) (bool, error) {
	for pageIndex := 0; pageIndex < projectAssistantExecReceiptThreadMaxPages; pageIndex++ {
		page, err := s.store.ListAssistantThreadTurnEventsAfter(ctx, scope, threadID, turnID, assistantThreadEventItemCompleted, afterSequence, assistantThreadEventWindowPageSize)
		if err != nil {
			return false, err
		}
		for _, event := range page {
			if event.ItemID != want.ItemID {
				continue
			}
			if !assistantThreadReceiptSettlementEvent(event) || !projectAssistantReceiptEventMatches(event, want) {
				return false, fmt.Errorf("assistant thread item %q already has a conflicting post-terminal event", want.ItemID)
			}
			return true, nil
		}
		if len(page) < assistantThreadEventWindowPageSize {
			return false, nil
		}
		afterSequence = page[len(page)-1].Sequence
	}
	return false, fmt.Errorf("%w: assistant thread receipt lookup exceeded %d pages", errProjectAssistantExecReceiptLookupLimit, projectAssistantExecReceiptThreadMaxPages)
}

func projectAssistantReceiptEventMatches(event, want store.AssistantThreadEvent) bool {
	if event.ThreadID != want.ThreadID || event.TurnID != want.TurnID || event.Type != want.Type || event.ItemID != want.ItemID {
		return false
	}
	var gotEnvelope struct {
		Item              assistantThreadItem `json:"item"`
		ReceiptSettlement bool                `json:"receiptSettlement"`
	}
	var wantEnvelope struct {
		Item              assistantThreadItem `json:"item"`
		ReceiptSettlement bool                `json:"receiptSettlement"`
	}
	if json.Unmarshal(event.Payload, &gotEnvelope) != nil || json.Unmarshal(want.Payload, &wantEnvelope) != nil {
		return false
	}
	return gotEnvelope.ReceiptSettlement && wantEnvelope.ReceiptSettlement && reflect.DeepEqual(gotEnvelope.Item, wantEnvelope.Item)
}

func projectAssistantThreadToolItemStatus(actionStatus string) string {
	switch actionStatus {
	case projectAssistantActionFeedStatusSucceeded, projectAssistantActionFeedStatusSkipped, projectAssistantActionFeedStatusCanceled:
		return "completed"
	default:
		return "failed"
	}
}
