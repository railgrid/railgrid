// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package api

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/railgrid/railgrid/pkg/runner"

	agentsv1alpha1 "github.com/railgrid/provider-agents/apis/v1alpha1"
	"github.com/railgrid/provider-agents/backend"
	backendharness "github.com/railgrid/provider-agents/backend/harness"
	"github.com/railgrid/provider-agents/llm"
	"github.com/railgrid/provider-agents/store"
)

type parkFailureStore struct {
	store.Store
	addInboxErr   error
	pendingRunErr error
	cancelOnInbox bool
}

func (s parkFailureStore) AddInboxItem(ctx context.Context, scope store.Scope, item store.InboxItem) error {
	if s.addInboxErr != nil {
		return s.addInboxErr
	}
	if err := s.Store.AddInboxItem(ctx, scope, item); err != nil {
		return err
	}
	if s.cancelOnInbox {
		scope.AgentName = item.AgentName
		return s.RequestCancel(ctx, scope, item.RunID, time.Now(), "reviewer")
	}
	return nil
}

func (s parkFailureStore) SaveRun(ctx context.Context, scope store.Scope, run store.Run) error {
	if run.Phase == store.RunPhasePendingApproval && s.pendingRunErr != nil {
		return s.pendingRunErr
	}
	return s.Store.SaveRun(ctx, scope, run)
}

func TestParkRunPersistenceFailuresStopHarnessAndCloseInbox(t *testing.T) {
	addErr := errors.New("inbox unavailable")
	checkpointErr := errors.New("run store unavailable")
	for _, test := range []struct {
		name          string
		question      bool
		addInboxErr   error
		pendingRunErr error
		wantInbox     store.InboxItemState
		cancelOnInbox bool
	}{
		{name: "cancellation races the owned checkpoint", cancelOnInbox: true, wantInbox: store.InboxStateDenied},
		{name: "inbox write fails", addInboxErr: addErr},
		{name: "approval checkpoint write fails", pendingRunErr: checkpointErr, wantInbox: store.InboxStateDenied},
		{name: "question checkpoint write fails", question: true, pendingRunErr: checkpointErr, wantInbox: store.InboxStateAnswered},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			dispatcher := &parkedHarnessDispatcher{receipt: runner.Receipt{
				AttemptID: "run-1", AttemptEpoch: 1, SessionID: "thread", Phase: runner.PhaseNeedsInput,
				Permission: &runner.PermissionRequest{ID: "permission", Tool: "Bash", Input: `{"command":"true"}`},
			}}
			b := backendharness.New(backendharness.Config{
				Runner: dispatcher, TaskID: "task", AttemptID: "run-1", Epoch: 1,
				WorkspaceID: "workspace", Credential: llm.HarnessIdentity{Kind: "codex-auth-json", Value: `{}`},
			})
			brun := &backend.Run{ID: "run-1", SessionID: "chat", Agent: "coder", Trigger: "chat"}
			out, err := b.Turn(ctx, brun, backend.Input{Messages: []backend.Message{{Role: backend.RoleUser, Content: "start"}}}, parkedHarnessSink{})
			if err != nil || out.Parked == nil {
				t.Fatalf("park harness: outcome=%+v err=%v", out, err)
			}
			if test.question {
				out.Parked.Tool, out.Parked.Args, out.Parked.Question = "", "", "What should I do?"
			}

			base := store.NewMemoryStore()
			scope := store.Scope{OrgUUID: "org", WorkspaceUUID: "workspace", AgentName: "coder"}
			now := time.Now().UTC()
			if err := base.SaveRun(ctx, scope, store.Run{
				ID: "run-1", AgentName: "coder", SessionID: "chat", Trigger: "chat",
				Phase: store.RunPhaseRunning, CreatedAt: now, UpdatedAt: now,
			}); err != nil {
				t.Fatal(err)
			}
			s := &Server{
				store:  parkFailureStore{Store: base, addInboxErr: test.addInboxErr, pendingRunErr: test.pendingRunErr, cancelOnInbox: test.cancelOnInbox},
				events: newEventBus(),
			}
			inboxEvents, unsubscribe := s.events.subscribe(scope)
			defer unsubscribe()
			agent := harnessAgent("edge")
			agent.Name = "coder"
			run := taskRun{Scope: scope, Agent: agent, RunID: "run-1", SessionID: "chat", Trigger: "chat"}
			if test.cancelOnInbox {
				run.ExecutionOwner = "pod/execution"
				if _, err := base.AcquireRunLease(ctx, scope, run.RunID, run.ExecutionOwner, run.Attempt, now); err != nil {
					t.Fatal(err)
				}
			}
			h := &harnessTurn{backend: b, Session: store.HarnessSession{SessionID: "chat", Turns: 1}}

			result, err := s.parkRun(ctx, run, "chat", now, now, newTurnProgressTracker(0), h, out)
			if err == nil || result.Phase == store.RunPhasePendingApproval {
				t.Fatalf("failed park reported success: result=%+v err=%v", result, err)
			}
			if dispatcher.cancels != 1 || dispatcher.receipt.Phase != runner.PhaseCancelled {
				t.Fatalf("failed park left remote permission active: cancels=%d receipt=%+v", dispatcher.cancels, dispatcher.receipt)
			}
			if test.cancelOnInbox && !errors.Is(err, store.ErrRunCancellationRequested) {
				t.Fatalf("park error = %v, want a recorded cancellation", err)
			}
			if test.pendingRunErr != nil && !errors.Is(err, checkpointErr) {
				t.Fatalf("park error = %v, want checkpoint write error", err)
			}
			if test.addInboxErr != nil && !errors.Is(err, addErr) {
				t.Fatalf("park error = %v, want inbox write error", err)
			}

			items, err := base.ListInbox(ctx, store.Scope{OrgUUID: scope.OrgUUID, WorkspaceUUID: scope.WorkspaceUUID}, "")
			if err != nil {
				t.Fatal(err)
			}
			if test.wantInbox == "" {
				if len(items) != 0 {
					t.Fatalf("failed inbox write left items behind: %+v", items)
				}
				return
			}
			if len(items) != 1 || items[0].State != test.wantInbox {
				t.Fatalf("orphan inbox item = %+v, want one item closed as %s", items, test.wantInbox)
			}
			if len(inboxEvents) != 2 {
				t.Fatalf("inbox events = %d, want pending plus cleanup update", len(inboxEvents))
			}
			<-inboxEvents // pending card was published before the checkpoint write.
			closedEvent := <-inboxEvents
			closed, ok := closedEvent.Data.(map[string]any)
			if !ok || closed["state"] != string(test.wantInbox) {
				t.Fatalf("cleanup event = %#v, want state %s", closedEvent.Data, test.wantInbox)
			}
		})
	}
}

type transientParkInboxFailure struct{ parkFailureStore }

func (s transientParkInboxFailure) GetInboxItem(context.Context, store.Scope, string) (store.InboxItem, error) {
	return store.InboxItem{}, errors.New("transient inbox read failure")
}

func TestCancelledParkRetriesInboxCleanup(t *testing.T) {
	ctx := t.Context()
	base := store.NewMemoryStore()
	scope := store.Scope{OrgUUID: "org", WorkspaceUUID: "workspace", AgentName: "agent"}
	now := time.Now()
	seed := store.Run{ID: "run", AgentName: "agent", Phase: store.RunPhaseRunning, CreatedAt: now, UpdatedAt: now}
	if err := base.SaveRun(ctx, scope, seed); err != nil {
		t.Fatal(err)
	}
	if _, err := base.AcquireRunLease(ctx, scope, seed.ID, "pod/owner", 0, now); err != nil {
		t.Fatal(err)
	}
	s := &Server{store: transientParkInboxFailure{parkFailureStore{Store: base, cancelOnInbox: true}}, events: newEventBus()}
	agent := &agentsv1alpha1.Agent{}
	agent.Name = "agent"
	tr := taskRun{Scope: scope, RunID: seed.ID, Agent: agent, SessionID: "chat", ExecutionOwner: "pod/owner"}
	if _, err := s.parkRun(ctx, tr, "chat", now, now, newTurnProgressTracker(0), nil, backend.Outcome{Parked: &backend.Parked{Question: "Proceed?", State: []byte("{}")}}); !errors.Is(err, store.ErrRunLeaseLost) {
		t.Fatalf("park did not hit cancellation fence: %v", err)
	}
	s.store = base
	stored, err := base.GetRun(ctx, scope, seed.ID)
	if err != nil || !stored.CancelRequested {
		t.Fatalf("cancel was not recorded: %+v %v", stored, err)
	}
	bg := &background{server: s}
	if err := bg.stopCancelledRun(ctx, "", scope, stored, "cancelled by user"); err != nil {
		t.Fatal(err)
	}
	items, err := base.ListInbox(ctx, store.Scope{OrgUUID: "org", WorkspaceUUID: "workspace"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].State != store.InboxStateAnswered {
		t.Fatalf("after cancellation retry the inbox item remains open: %+v", items)
	}
}
