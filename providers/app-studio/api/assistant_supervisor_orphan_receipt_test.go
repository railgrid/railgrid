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
	"sync"
	"testing"
	"time"

	"github.com/railgrid/provider-app-studio/store"
)

type orphanReceiptFailureStore struct {
	store.Store

	failure                    error
	terminalizeOnSecondRead    bool
	projectionClosed           func() bool
	mu                         sync.Mutex
	assistantRunReads          int
	receiptReads               int
	allReceiptReadsAfterClosed bool
	receiptReadEntered         chan struct{}
	receiptReadRelease         chan struct{}
	receiptBlockOnce           sync.Once
}

func (s *orphanReceiptFailureStore) GetAssistantRun(ctx context.Context, scope store.Scope, id string) (store.AssistantRun, error) {
	s.mu.Lock()
	s.assistantRunReads++
	readNumber := s.assistantRunReads
	s.mu.Unlock()

	if s.terminalizeOnSecondRead && readNumber == 2 {
		run, err := s.Store.GetAssistantRun(ctx, scope, id)
		if err != nil {
			return store.AssistantRun{}, err
		}
		if run.Status == store.AssistantRunStatusRunning {
			messages, err := s.GetMessagesByIDs(ctx, scope, []string{run.ActiveMessageID})
			if err != nil {
				return store.AssistantRun{}, err
			}
			if len(messages) != 1 {
				return store.AssistantRun{}, fmt.Errorf("get active assistant message: got %d messages", len(messages))
			}
			run.Status = store.AssistantRunStatusInterrupted
			run.AbortReason = store.AssistantRunAbortReasonInterrupted
			run.UpdatedAt = time.Now().UTC()
			run.Revision++
			message := messages[0]
			message.UpdatedAt = run.UpdatedAt
			message.Metadata = projectAssistantDurableMetadataFromExisting(run, "Interrupted", false, message.Metadata)
			if err := s.SaveAssistantRunSnapshot(ctx, scope, run, []store.Message{message}, run.Revision-1); err != nil {
				return store.AssistantRun{}, err
			}
		}
	}
	return s.Store.GetAssistantRun(ctx, scope, id)
}

func (s *orphanReceiptFailureStore) ListAssistantRunEvents(ctx context.Context, scope store.Scope, runID string, afterSequence int64, limit int) ([]store.AssistantRunEvent, error) {
	s.mu.Lock()
	s.receiptReads++
	if s.projectionClosed != nil && !s.projectionClosed() {
		s.allReceiptReadsAfterClosed = false
	}
	failure := s.failure
	s.mu.Unlock()
	if s.receiptReadEntered != nil {
		block := false
		s.receiptBlockOnce.Do(func() { block = true })
		if block {
			close(s.receiptReadEntered)
			select {
			case <-s.receiptReadRelease:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
	}
	if failure != nil {
		return nil, failure
	}
	return s.Store.ListAssistantRunEvents(ctx, scope, runID, afterSequence, limit)
}

func TestOrphanReceiptRepairFailureCannotStrandInterruptedTurn(t *testing.T) {
	failures := []struct {
		name string
		err  error
	}{
		{name: "corrupt ledger", err: fmt.Errorf("%w: injected corrupt event ordering", errProjectAssistantRunToolLedgerCorrupt)},
		{name: "deadline", err: context.DeadlineExceeded},
	}
	recoveryPaths := []struct {
		name                  string
		initialStatus         store.AssistantRunStatus
		terminalizeAfterFence bool
	}{
		{name: "already terminal", initialStatus: store.AssistantRunStatusInterrupted},
		{name: "terminal after recovery fence", initialStatus: store.AssistantRunStatusRunning, terminalizeAfterFence: true},
		{name: "fresh orphan transition", initialStatus: store.AssistantRunStatusRunning},
	}

	for _, failure := range failures {
		for _, path := range recoveryPaths {
			t.Run(failure.name+"/"+path.name, func(t *testing.T) {
				ctx := context.Background()
				base := store.NewMemoryStore()
				scope := store.Scope{OrgUUID: "org-receipt-recovery", WorkspaceUUID: "workspace-receipt-recovery", ProjectName: "project", ProjectUID: "project-uid"}
				now := time.Now().UTC()
				threadID := "thread-orphan-receipt"
				runID := "run-orphan-receipt"
				requestID := "request-orphan-receipt"
				thread := store.AssistantThread{ID: threadID, ActorID: "test-user", Status: store.AssistantThreadStatusIdle, CreatedAt: now, UpdatedAt: now}
				if _, err := base.CreateAssistantThread(ctx, scope, thread, nil); err != nil {
					t.Fatalf("CreateAssistantThread: %v", err)
				}

				run := store.AssistantRun{
					ID: runID, ThreadID: threadID, ProjectName: scope.ProjectName, ProjectUID: scope.ProjectUID,
					Mode: store.AssistantRunModeDefault, ApprovalMode: store.AssistantApprovalModeOnRequest,
					Status: path.initialStatus, ClientRequestID: requestID,
					UserMessageID: "user-orphan-receipt", ActiveMessageID: "assistant-orphan-receipt",
					Revision: 1, CreatedAt: now, UpdatedAt: now,
				}
				args := map[string]any{"component": "workspace", "argv": []any{"true"}}
				running := projectToolCallStreamEvent{
					ID: "exec-orphan-receipt", Name: projectToolExecCommand, Status: "running",
					Arguments: summarizeProjectToolArgumentsMap(projectToolExecCommand, args),
					Exec:      projectAssistantExecMetadataForToolArguments(projectToolExecCommand, args, "", "running"),
				}
				audit, err := json.Marshal(projectAssistantRunAudit{
					Version: projectAssistantAuditVersion,
					Tools:   []projectAssistantAuditTool{{ID: running.ID, Name: running.Name, Status: running.Status}},
				})
				if err != nil {
					t.Fatalf("marshal run audit: %v", err)
				}
				run.Audit = audit
				assistant := store.Message{
					ID: run.ActiveMessageID, ActorID: thread.ActorID, Role: "assistant", Content: "Working.",
					Metadata:  projectAssistantDurableMetadataForTransition(run, "Working", false, false, []projectToolCallStreamEvent{running}, nil),
					CreatedAt: now.Add(time.Microsecond), UpdatedAt: now.Add(time.Microsecond),
				}
				user := store.Message{ID: run.UserMessageID, ActorID: thread.ActorID, Role: "user", Content: "Run a command.", CreatedAt: now, UpdatedAt: now}
				if _, err := base.CreateAssistantRun(ctx, scope, user, assistant, run); err != nil {
					t.Fatalf("CreateAssistantRun: %v", err)
				}
				if _, err := base.CreateAssistantTurn(ctx, scope, store.AssistantTurn{
					ID: run.ID, ThreadID: threadID, ActorID: thread.ActorID,
					ClientUserMessageID: run.ClientRequestID, Mode: run.Mode, ApprovalMode: run.ApprovalMode,
					Status: store.AssistantTurnStatusInProgress, CreatedAt: now, UpdatedAt: now,
				}, []store.AssistantThreadEvent{{Type: assistantThreadEventTurnStarted, CreatedAt: now}}); err != nil {
					t.Fatalf("CreateAssistantTurn: %v", err)
				}

				brokenStore := &orphanReceiptFailureStore{
					Store: base, failure: failure.err,
					terminalizeOnSecondRead:    path.terminalizeAfterFence,
					allReceiptReadsAfterClosed: true,
				}
				brokenStore.projectionClosed = func() bool {
					turn, turnErr := base.GetAssistantTurn(ctx, scope, threadID, runID)
					thread, threadErr := base.GetAssistantThread(ctx, scope, threadID)
					items, itemsErr := base.ListAssistantConversationItems(ctx, scope, 0, 100)
					if turnErr != nil || threadErr != nil || itemsErr != nil || turn.Status != store.AssistantTurnStatusInterrupted || thread.Status != store.AssistantThreadStatusIdle {
						return false
					}
					for _, item := range items {
						if item.RunID == runID && item.Type == projectAssistantConversationInterruption {
							return true
						}
					}
					return false
				}
				server := NewWithWorkspace(nil, brokenStore, nil, "", false)
				server.tenantWorkspaces = defaultTestWorkspaces.lookup
				server.tenantActors = defaultTestActors.lookup
				t.Cleanup(func() { server.projectAssistantSupervisor().Shutdown(context.Background()) })

				if err := server.reconcileOrphanedProjectAssistantRun(ctx, scope, run.ID); err != nil {
					t.Fatalf("reconcile orphaned run: %v", err)
				}

				gotRun, err := base.GetAssistantRun(ctx, scope, run.ID)
				if err != nil || gotRun.Status != store.AssistantRunStatusInterrupted {
					t.Fatalf("run status = %q, err=%v; want interrupted", gotRun.Status, err)
				}
				gotTurn, err := base.GetAssistantTurn(ctx, scope, threadID, runID)
				if err != nil || gotTurn.Status != store.AssistantTurnStatusInterrupted {
					t.Fatalf("turn status = %q, err=%v; want interrupted", gotTurn.Status, err)
				}
				gotThread, err := base.GetAssistantThread(ctx, scope, threadID)
				if err != nil || gotThread.Status != store.AssistantThreadStatusIdle {
					t.Fatalf("thread status = %q, err=%v; want idle", gotThread.Status, err)
				}
				if !brokenStore.allReceiptReadsAfterClosed || brokenStore.receiptReads == 0 {
					t.Fatalf("receipt reads=%d all-after-close=%t; want a failed optional repair only after boundary and turn closure", brokenStore.receiptReads, brokenStore.allReceiptReadsAfterClosed)
				}

				started, err := server.startProjectAssistantRunDurablyForThread(
					ctx, scope, threadID, thread.ActorID, "continue after interruption", "request-next-turn",
					store.AssistantRunModeDefault, projectAssistantDurableSkillSelection{},
					func(store.AssistantRun, store.Message, bool) error { return nil },
				)
				if err != nil {
					t.Fatalf("start next turn after recovery: %v", err)
				}
				if !started.Started || started.Run.ThreadID != threadID {
					t.Fatalf("next turn = %#v; want a started turn on %q", started, threadID)
				}
			})
		}
	}
}

func TestInterruptedExecReceiptSnapshotDoesNotWaitForRunTransitionLock(t *testing.T) {
	ctx := context.Background()
	scope := store.Scope{OrgUUID: "org-receipt-lock", WorkspaceUUID: "workspace-receipt-lock", ProjectName: "project", ProjectUID: "project-uid"}
	now := time.Now().UTC()
	run := store.AssistantRun{
		ID: "run-receipt-lock", ThreadID: "thread-receipt-lock", Mode: store.AssistantRunModeDefault,
		Status: store.AssistantRunStatusInterrupted, ClientRequestID: "request-receipt-lock",
		UserMessageID: "user-receipt-lock", ActiveMessageID: "assistant-receipt-lock",
		Revision: 1, CreatedAt: now, UpdatedAt: now,
	}
	messages := store.NewMemoryStore()
	user := store.Message{ID: run.UserMessageID, Role: "user", ActorID: "test-user", CreatedAt: now, UpdatedAt: now}
	assistant := store.Message{ID: run.ActiveMessageID, Role: "assistant", CreatedAt: now, UpdatedAt: now}
	if _, err := messages.CreateAssistantRun(ctx, scope, user, assistant, run); err != nil {
		t.Fatalf("CreateAssistantRun: %v", err)
	}
	server := NewWithWorkspace(nil, messages, nil, "", false)
	supervisor := server.projectAssistantSupervisor()
	t.Cleanup(func() { supervisor.Shutdown(context.Background()) })
	accumulator, err := supervisor.Attach(scope, run, assistant)
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	if accumulator.owner == nil {
		t.Fatal("Attach returned no supervised run")
	}

	accumulator.owner.transitionMu.Lock()
	defer accumulator.owner.transitionMu.Unlock()
	batchCtx, cancel := detachedProjectAssistantExecReceiptBatchContext(ctx, time.Second)
	defer cancel()
	event := projectToolCallStreamEvent{
		ID: "exec-receipt-lock", Name: projectToolExecCommand, Status: "succeeded",
		Exec: &projectAssistantExecMetadata{Component: "workspace", Status: "succeeded", ExitCode: intPointer(0)},
	}
	done := make(chan error, 1)
	go func() {
		done <- accumulator.UpdateStoppingToolSnapshot(batchCtx, event, func(*store.AssistantRun, *store.Message) {})
	}()
	select {
	case err := <-done:
		if !errors.Is(err, store.ErrAssistantRunConflict) {
			t.Fatalf("receipt snapshot error = %v; want immediate assistant run conflict", err)
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("receipt snapshot waited for the in-flight run transition lock")
	}
}

func TestNewTurnCanStartWhileInterruptedReceiptRepairIsBlocked(t *testing.T) {
	ctx := context.Background()
	base := store.NewMemoryStore()
	scope := store.Scope{OrgUUID: "org-receipt-slow", WorkspaceUUID: "workspace-receipt-slow", ProjectName: "project", ProjectUID: "project-uid"}
	now := time.Now().UTC()
	threadID := "thread-receipt-slow"
	runID := "run-receipt-slow"
	requestID := "request-receipt-slow"
	thread := store.AssistantThread{ID: threadID, ActorID: "test-user", Status: store.AssistantThreadStatusIdle, CreatedAt: now, UpdatedAt: now}
	if _, err := base.CreateAssistantThread(ctx, scope, thread, nil); err != nil {
		t.Fatalf("CreateAssistantThread: %v", err)
	}
	run := store.AssistantRun{
		ID: runID, ThreadID: threadID, ProjectName: scope.ProjectName, ProjectUID: scope.ProjectUID,
		Mode: store.AssistantRunModeDefault, ApprovalMode: store.AssistantApprovalModeOnRequest,
		Status: store.AssistantRunStatusRunning, ClientRequestID: requestID,
		UserMessageID: "user-receipt-slow", ActiveMessageID: "assistant-receipt-slow",
		Revision: 1, CreatedAt: now, UpdatedAt: now,
	}
	user := store.Message{ID: run.UserMessageID, ActorID: thread.ActorID, Role: "user", Content: "Run a command.", CreatedAt: now, UpdatedAt: now}
	assistant := store.Message{ID: run.ActiveMessageID, ActorID: thread.ActorID, Role: "assistant", Content: "Working.", CreatedAt: now.Add(time.Microsecond), UpdatedAt: now.Add(time.Microsecond)}
	if _, err := base.CreateAssistantRun(ctx, scope, user, assistant, run); err != nil {
		t.Fatalf("CreateAssistantRun: %v", err)
	}
	if _, err := base.CreateAssistantTurn(ctx, scope, store.AssistantTurn{
		ID: run.ID, ThreadID: threadID, ActorID: thread.ActorID,
		ClientUserMessageID: run.ClientRequestID, Mode: run.Mode, ApprovalMode: run.ApprovalMode,
		Status: store.AssistantTurnStatusInProgress, CreatedAt: now, UpdatedAt: now,
	}, []store.AssistantThreadEvent{{Type: assistantThreadEventTurnStarted, CreatedAt: now}}); err != nil {
		t.Fatalf("CreateAssistantTurn: %v", err)
	}

	readEntered := make(chan struct{})
	readRelease := make(chan struct{})
	var releaseOnce sync.Once
	releaseReceipt := func() { releaseOnce.Do(func() { close(readRelease) }) }
	brokenStore := &orphanReceiptFailureStore{
		Store: base, failure: context.DeadlineExceeded,
		receiptReadEntered: readEntered, receiptReadRelease: readRelease,
		allReceiptReadsAfterClosed: true,
	}
	server := NewWithWorkspace(nil, brokenStore, nil, "", false)
	server.tenantWorkspaces = defaultTestWorkspaces.lookup
	server.tenantActors = defaultTestActors.lookup
	t.Cleanup(func() {
		releaseReceipt()
		server.projectAssistantSupervisor().Shutdown(context.Background())
	})

	recoveryDone := make(chan error, 1)
	go func() { recoveryDone <- server.reconcileOrphanedProjectAssistantRun(ctx, scope, run.ID) }()
	select {
	case <-readEntered:
	case <-time.After(time.Second):
		t.Fatal("orphan recovery did not reach optional receipt repair")
	}

	started, err := server.startProjectAssistantRunDurablyForThread(
		ctx, scope, threadID, thread.ActorID, "continue while receipt repair is pending", "request-next-turn",
		store.AssistantRunModeDefault, projectAssistantDurableSkillSelection{},
		func(store.AssistantRun, store.Message, bool) error { return nil },
	)
	if err != nil {
		t.Fatalf("start next turn during blocked receipt repair: %v", err)
	}
	if !started.Started || started.Run.ThreadID != threadID {
		t.Fatalf("next turn = %#v; want a started turn on %q", started, threadID)
	}
	releaseReceipt()
	select {
	case err := <-recoveryDone:
		if err != nil {
			t.Fatalf("orphan recovery after receipt release: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("orphan recovery did not finish after receipt release")
	}
}
