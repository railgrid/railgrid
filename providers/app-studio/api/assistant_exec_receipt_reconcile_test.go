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
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/railgrid/provider-app-studio/store"
)

func TestReconcileInterruptedExecReceiptPagesPast128CompletedCalls(t *testing.T) {
	ctx := context.Background()
	const priorCallCount = 129
	const lateCallID = "late-exec-call"
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	args := map[string]any{"component": "workspace", "argv": []any{"go", "test"}}
	actions := make([]projectAssistantActionFeedItem, 0, priorCallCount+1)
	for index := 0; index < priorCallCount; index++ {
		callID := fmt.Sprintf("completed-exec-%03d", index)
		actionEvent := receiptTestExecEvent(callID, args, `{"status":"succeeded","summary":"Command completed.","exitCode":0}`, now.Add(time.Duration(index)*time.Millisecond))
		action := projectAssistantActionFeedItemFromToolCall(actionEvent)
		action.Sequence = index + 1
		actions = append(actions, action)
	}
	running := projectToolCallStreamEvent{
		ID: lateCallID, Name: projectToolExecCommand, Status: "running",
		Arguments: summarizeProjectToolArgumentsMap(projectToolExecCommand, args),
		Summary:   "Command running.",
		Exec:      projectAssistantExecMetadataForToolArguments(projectToolExecCommand, args, "", "running"),
	}
	unknown := projectAssistantActionFeedItemFromToolCall(running)
	unknown.Sequence = priorCallCount + 1
	unknown = finalizeProjectAssistantActionFeed([]projectAssistantActionFeedItem{unknown}, store.AssistantRunStatusInterrupted)[0]
	actions = append(actions, unknown)
	audit, err := json.Marshal(projectAssistantRunAudit{
		Version: projectAssistantAuditVersion,
		Tools:   []projectAssistantAuditTool{{ID: lateCallID, Name: projectToolExecCommand, Status: "running"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	server, messages, scope, run := newInterruptedExecReceiptFixture(t, "receipt-many", "request-many", "request-many", actions, audit)
	server.assistantSupervisor = newProjectAssistantSupervisor(ctx, messages)
	defer server.assistantSupervisor.Shutdown(context.Background())
	ledger := newProjectAssistantRunEventLedger(messages, scope, run.ID)
	spec := projectAssistantToolSpec{Name: projectToolExecCommand, Risk: projectAssistantToolRiskRead}
	for index := 0; index < priorCallCount; index++ {
		callID := fmt.Sprintf("completed-exec-%03d", index)
		decision, beginErr := ledger.BeginToolCall(ctx, callID, spec, args)
		if beginErr != nil {
			t.Fatalf("BeginToolCall(%s): %v", callID, beginErr)
		}
		if _, finishErr := ledger.FinishToolCall(ctx, decision.Token, `{"status":"succeeded","summary":"Command completed.","exitCode":0}`, nil); finishErr != nil {
			t.Fatalf("FinishToolCall(%s): %v", callID, finishErr)
		}
	}
	decision, err := ledger.BeginToolCall(ctx, lateCallID, spec, args)
	if err != nil {
		t.Fatalf("BeginToolCall(late): %v", err)
	}
	if _, err := ledger.FinishToolCall(ctx, decision.Token, `{"status":"succeeded","summary":"Command completed.","exitCode":0}`, nil); err != nil {
		t.Fatalf("FinishToolCall(late): %v", err)
	}

	if err := server.reconcileInterruptedProjectAssistantExecReceipts(ctx, scope, run.ID, false); err != nil {
		t.Fatalf("reconcile interrupted exec receipts: %v", err)
	}
	updatedRun, err := messages.GetAssistantRun(ctx, scope, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updatedRun.Revision != run.Revision+1 {
		t.Fatalf("run revision = %d, want one repair after %d prior results", updatedRun.Revision, priorCallCount)
	}
	updatedMessages, err := messages.GetMessagesByIDs(ctx, scope, []string{run.ActiveMessageID})
	if err != nil || len(updatedMessages) != 1 {
		t.Fatalf("GetMessagesByIDs = %d, err=%v", len(updatedMessages), err)
	}
	updatedActions := projectAssistantActionFeedFromMetadata(updatedMessages[0].Metadata[projectMessageMetadataAssistantActionFeed])
	if len(updatedActions) != len(actions) {
		t.Fatalf("action count = %d, want %d", len(updatedActions), len(actions))
	}
	for index := 0; index < priorCallCount; index++ {
		if !reflect.DeepEqual(updatedActions[index], actions[index]) {
			t.Fatalf("prior action %d changed during receipt scan:\n got %#v\nwant %#v", index, updatedActions[index], actions[index])
		}
	}
	settled, ok := projectAssistantInterruptedExecSettledAction(updatedMessages[0].Metadata, projectAssistantExecReceiptTestEvent(t, messages, scope, run.ID, lateCallID))
	if !ok || settled.Status != projectAssistantActionFeedStatusSucceeded || settled.Exec == nil || settled.Exec.ExitCode == nil || *settled.Exec.ExitCode != 0 || settled.Diagnostic != nil {
		t.Fatalf("late action = %#v, settled=%t; want authoritative success without synthetic diagnostic", settled, ok)
	}
}

func TestProjectAssistantExecReceiptIgnoresCancellationWithoutTerminalExit(t *testing.T) {
	ctx := context.Background()
	messages, scope := newAssistantRunEventLedgerTestStore(t, "receipt-canceled-no-exit")
	ledger := newProjectAssistantRunEventLedger(messages, scope, "receipt-canceled-no-exit")
	spec := projectAssistantToolSpec{Name: projectToolExecCommand, Risk: projectAssistantToolRiskRead}
	args := map[string]any{"component": "workspace", "argv": []any{"go", "test"}}
	decision, err := ledger.BeginToolCall(ctx, "canceled-call", spec, args)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.FinishToolCall(ctx, decision.Token, `{"status":"canceled","summary":"Command canceled."}`, context.Canceled); err != nil {
		t.Fatal(err)
	}
	restarted := newProjectAssistantRunEventLedger(messages, scope, "receipt-canceled-no-exit")
	if _, found, err := projectAssistantExecReceiptFromLedger(ctx, restarted, "canceled-call", projectToolExecCommand); err != nil || found {
		t.Fatalf("canceled receipt = found %t, err=%v; want unknown until exit evidence exists", found, err)
	}
}

func TestReconcileInterruptedExecReceiptRequiresExactTurnRequest(t *testing.T) {
	ctx := context.Background()
	args := map[string]any{"component": "workspace", "argv": []any{"go", "test"}}
	const callID = "wrong-request-call"
	running := projectToolCallStreamEvent{
		ID: callID, Name: projectToolExecCommand, Status: "running",
		Exec: projectAssistantExecMetadataForToolArguments(projectToolExecCommand, args, "", "running"),
	}
	unknown := finalizeProjectAssistantActionFeed([]projectAssistantActionFeedItem{projectAssistantActionFeedItemFromToolCall(running)}, store.AssistantRunStatusInterrupted)[0]
	unknown.Sequence = 1
	audit, err := json.Marshal(projectAssistantRunAudit{
		Version: projectAssistantAuditVersion,
		Tools:   []projectAssistantAuditTool{{ID: callID, Name: projectToolExecCommand, Status: "running"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	server, messages, scope, run := newInterruptedExecReceiptFixture(t, "receipt-wrong-request", "request-run", "different-turn-request", []projectAssistantActionFeedItem{unknown}, audit)
	ledger := newProjectAssistantRunEventLedger(messages, scope, run.ID)
	decision, err := ledger.BeginToolCall(ctx, callID, projectAssistantToolSpec{Name: projectToolExecCommand, Risk: projectAssistantToolRiskRead}, args)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.FinishToolCall(ctx, decision.Token, `{"status":"succeeded","summary":"Command completed.","exitCode":0}`, nil); err != nil {
		t.Fatal(err)
	}
	if err := server.reconcileInterruptedProjectAssistantExecReceipts(ctx, scope, run.ID, false); err != nil {
		t.Fatal(err)
	}
	updated, err := messages.GetAssistantRun(ctx, scope, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Revision != run.Revision {
		t.Fatalf("run revision = %d, want no mutation for mismatched turn request", updated.Revision)
	}
	storedMessages, err := messages.GetMessagesByIDs(ctx, scope, []string{run.ActiveMessageID})
	if err != nil || len(storedMessages) != 1 {
		t.Fatalf("GetMessagesByIDs = %d, err=%v", len(storedMessages), err)
	}
	got := projectAssistantActionFeedFromMetadata(storedMessages[0].Metadata[projectMessageMetadataAssistantActionFeed])
	if len(got) != 1 || got[0].Diagnostic == nil || got[0].Diagnostic.Code != "command_outcome_unobserved" {
		t.Fatalf("action feed = %#v, want unchanged unknown receipt", got)
	}
}

type assistantExecReceiptRaceStore struct {
	store.Store
	winnerEvent store.AssistantThreadEvent
	once        sync.Once
}

func (s *assistantExecReceiptRaceStore) ListAssistantThreadTurnEventsAfter(ctx context.Context, scope store.Scope, threadID, turnID, eventType string, afterSequence int64, limit int) ([]store.AssistantThreadEvent, error) {
	page, err := s.Store.ListAssistantThreadTurnEventsAfter(ctx, scope, threadID, turnID, eventType, afterSequence, limit)
	if err != nil {
		return nil, err
	}
	if eventType == assistantThreadEventItemCompleted && afterSequence > 0 {
		s.once.Do(func() {
			tail, tailErr := s.ListAssistantThreadEventsBefore(ctx, scope, threadID, 0, 1)
			if tailErr != nil {
				err = tailErr
				return
			}
			expected := int64(0)
			if len(tail) > 0 {
				expected = tail[len(tail)-1].Sequence
			}
			_, appendErr := s.AppendAssistantThreadEvent(ctx, scope, s.winnerEvent, expected)
			err = appendErr
		})
	}
	if err != nil {
		return nil, err
	}
	return page, nil
}

func TestAppendProjectAssistantExecReceiptCanonicalEventCASFindsConcurrentWinner(t *testing.T) {
	ctx := context.Background()
	base := store.NewMemoryStore()
	scope := store.Scope{OrgUUID: "org-race", WorkspaceUUID: "workspace-race", ProjectName: "project-race", ProjectUID: "uid-race"}
	now := time.Now().UTC()
	thread, err := base.CreateAssistantThread(ctx, scope, store.AssistantThread{ID: "thread-race", ActorID: "actor-race", CreatedAt: now, UpdatedAt: now}, nil)
	if err != nil {
		t.Fatal(err)
	}
	turn, err := base.CreateAssistantTurn(ctx, scope, store.AssistantTurn{
		ID: "turn-race", ThreadID: thread.ID, ActorID: thread.ActorID, ClientUserMessageID: "request-race",
		Mode: store.AssistantRunModeDefault, ApprovalMode: store.AssistantApprovalModeOnRequest,
		Status: store.AssistantTurnStatusInProgress, CreatedAt: now, UpdatedAt: now,
	}, []store.AssistantThreadEvent{{Type: assistantThreadEventTurnStarted, CreatedAt: now}})
	if err != nil {
		t.Fatal(err)
	}
	turn.Status = store.AssistantTurnStatusInterrupted
	turn.UpdatedAt = now.Add(time.Second)
	if err := base.SaveAssistantTurnWithEvent(ctx, scope, turn, store.AssistantThreadEvent{Type: assistantThreadEventTurnInterrupted, CreatedAt: turn.UpdatedAt}, 1); err != nil {
		t.Fatal(err)
	}
	args := map[string]any{"component": "workspace", "argv": []any{"go", "test"}}
	event := projectToolCallStreamEvent{ID: "race-call", Name: projectToolExecCommand, Status: "succeeded", Exec: projectAssistantExecMetadataForToolArguments(projectToolExecCommand, args, `{"status":"succeeded","summary":"Command completed.","exitCode":0}`, "succeeded")}
	action := projectAssistantActionFeedItemFromToolCall(event)
	action.Sequence = 1
	item := assistantThreadItem{
		ID: assistantThreadDynamicToolItemID("assistant-race", action.ID), TurnID: turn.ID, Type: assistantThreadEventDynamicToolCall,
		Status: "completed", Content: action.Title, AssistantMessageID: "assistant-race", Sequence: 1, CreatedAt: now,
	}
	item.Data, err = json.Marshal(action)
	if err != nil {
		t.Fatal(err)
	}
	winnerPayload, err := json.Marshal(map[string]any{"item": item, "receiptSettlement": true})
	if err != nil {
		t.Fatal(err)
	}
	raceStore := &assistantExecReceiptRaceStore{
		Store: base,
		winnerEvent: store.AssistantThreadEvent{
			ThreadID: thread.ID, TurnID: turn.ID, Type: assistantThreadEventItemCompleted, ItemID: item.ID, Payload: winnerPayload,
		},
	}
	server := &Server{store: raceStore}
	if err := server.appendProjectAssistantExecReceiptCanonicalEvent(ctx, scope, store.AssistantRun{ID: turn.ID, ThreadID: thread.ID}, item); err != nil {
		t.Fatalf("append reconciled receipt: %v", err)
	}
	terminal, err := base.ListAssistantThreadTurnEventsAfter(ctx, scope, thread.ID, turn.ID, assistantThreadEventTurnInterrupted, 0, 1)
	if err != nil || len(terminal) != 1 {
		t.Fatalf("terminal event = %#v, err=%v", terminal, err)
	}
	settlements, err := base.ListAssistantThreadTurnEventsAfter(ctx, scope, thread.ID, turn.ID, assistantThreadEventItemCompleted, terminal[0].Sequence, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(settlements) != 1 || !assistantThreadReceiptSettlementEvent(settlements[0]) || settlements[0].ItemID != item.ID {
		t.Fatalf("post-terminal receipt events = %#v, want exactly the competing winner", settlements)
	}
}

func receiptTestExecEvent(callID string, args map[string]any, result string, observedAt time.Time) projectToolCallStreamEvent {
	exec := projectAssistantExecMetadataForToolArguments(projectToolExecCommand, args, result, "succeeded")
	return projectToolCallStreamEvent{
		ID: callID, Name: projectToolExecCommand, Status: "succeeded",
		Arguments: summarizeProjectToolArgumentsMap(projectToolExecCommand, args),
		Summary:   summarizeProjectToolResult(projectToolExecCommand, result),
		Exec:      exec, ObservedAt: observedAt,
	}
}

func projectAssistantExecReceiptTestEvent(t *testing.T, messages store.Store, scope store.Scope, runID, callID string) projectToolCallStreamEvent {
	t.Helper()
	ledger := newProjectAssistantRunEventLedger(messages, scope, runID)
	event, found, err := projectAssistantExecReceiptFromLedger(context.Background(), ledger, callID, projectToolExecCommand)
	if err != nil || !found {
		t.Fatalf("read exec receipt %q: found=%t err=%v", callID, found, err)
	}
	return event
}

func newInterruptedExecReceiptFixture(
	t *testing.T,
	runID, runRequestID, turnRequestID string,
	actions []projectAssistantActionFeedItem,
	audit json.RawMessage,
) (*Server, *store.MemoryStore, store.Scope, store.AssistantRun) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	scope := store.Scope{OrgUUID: "org-receipt", WorkspaceUUID: "workspace-receipt", ProjectName: "project-receipt", ProjectUID: "uid-receipt"}
	messages := store.NewMemoryStore()
	thread, err := messages.CreateAssistantThread(ctx, scope, store.AssistantThread{
		ID: "thread-" + runID, ActorID: "actor-receipt", Status: store.AssistantThreadStatusIdle, CreatedAt: now, UpdatedAt: now,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	turn, err := messages.CreateAssistantTurn(ctx, scope, store.AssistantTurn{
		ID: runID, ThreadID: thread.ID, ActorID: thread.ActorID, ClientUserMessageID: turnRequestID,
		Mode: store.AssistantRunModeDefault, ApprovalMode: store.AssistantApprovalModeOnRequest,
		Status: store.AssistantTurnStatusInProgress, CreatedAt: now, UpdatedAt: now,
	}, []store.AssistantThreadEvent{{Type: assistantThreadEventTurnStarted, CreatedAt: now}})
	if err != nil {
		t.Fatal(err)
	}
	turn.Status = store.AssistantTurnStatusInterrupted
	turn.UpdatedAt = now.Add(time.Second)
	if err := messages.SaveAssistantTurn(ctx, scope, turn); err != nil {
		t.Fatal(err)
	}
	metadata := map[string]any{projectMessageMetadataAssistantActionFeed: actions}
	run := store.AssistantRun{
		ID: runID, ThreadID: thread.ID, ProjectName: scope.ProjectName, ProjectUID: scope.ProjectUID,
		Mode: store.AssistantRunModeDefault, ApprovalMode: store.AssistantApprovalModeOnRequest,
		Status: store.AssistantRunStatusInterrupted, ClientRequestID: runRequestID,
		UserMessageID: "user-" + runID, ActiveMessageID: "assistant-" + runID, Revision: 1,
		Audit: audit, CreatedAt: now, UpdatedAt: turn.UpdatedAt,
	}
	user := store.Message{ID: run.UserMessageID, ActorID: thread.ActorID, Role: "user", Content: "Run a command.", CreatedAt: now, UpdatedAt: now}
	assistant := store.Message{ID: run.ActiveMessageID, ActorID: thread.ActorID, Role: "assistant", Content: "Interrupted.", Metadata: metadata, CreatedAt: now.Add(time.Microsecond), UpdatedAt: now.Add(time.Microsecond)}
	if _, err := messages.CreateAssistantRun(ctx, scope, user, assistant, run); err != nil {
		t.Fatal(err)
	}
	return &Server{store: messages}, messages, scope, run
}
