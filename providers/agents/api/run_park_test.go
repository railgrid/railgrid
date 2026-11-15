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
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/railgrid/railgrid/pkg/runner"

	"github.com/railgrid/provider-agents/backend"
	backendharness "github.com/railgrid/provider-agents/backend/harness"
	"github.com/railgrid/provider-agents/llm"
	"github.com/railgrid/provider-agents/store"
)

type parkFailureStore struct {
	store.Store
	addInboxErr   error
	pendingRunErr error
	getRunErr     error
	beforeAdd     func(context.Context, store.Scope, store.InboxItem) error
}

func (s parkFailureStore) AddInboxItem(ctx context.Context, scope store.Scope, item store.InboxItem) error {
	if s.beforeAdd != nil {
		if err := s.beforeAdd(ctx, scope, item); err != nil {
			return err
		}
	}
	if s.addInboxErr != nil {
		return s.addInboxErr
	}
	return s.Store.AddInboxItem(ctx, scope, item)
}

func (s parkFailureStore) SaveRun(ctx context.Context, scope store.Scope, run store.Run) error {
	if run.Phase == store.RunPhasePendingApproval && s.pendingRunErr != nil {
		return s.pendingRunErr
	}
	return s.Store.SaveRun(ctx, scope, run)
}

func (s parkFailureStore) GetRun(ctx context.Context, scope store.Scope, id string) (store.Run, error) {
	if s.getRunErr != nil {
		return store.Run{}, s.getRunErr
	}
	return s.Store.GetRun(ctx, scope, id)
}

func TestParkRunPersistenceFailuresStopHarnessAndCloseInbox(t *testing.T) {
	addErr := errors.New("inbox unavailable")
	checkpointErr := errors.New("run store unavailable")
	getRunErr := errors.New("run read unavailable")
	for _, test := range []struct {
		name            string
		question        bool
		addInboxErr     error
		pendingRunErr   error
		getRunErr       error
		backendStateErr bool
		marshalErr      bool
		preposted       bool
	}{
		{name: "inbox write fails", addInboxErr: addErr},
		{name: "approval checkpoint write fails", pendingRunErr: checkpointErr},
		{name: "preposted approval checkpoint write fails", preposted: true, pendingRunErr: checkpointErr},
		{name: "question checkpoint write fails", question: true, pendingRunErr: checkpointErr},
		{name: "run read fails and closes preposted approval", preposted: true, getRunErr: getRunErr},
		{name: "backend state fails and closes preposted approval", preposted: true, backendStateErr: true},
		{name: "checkpoint marshal fails and closes preposted approval", preposted: true, marshalErr: true},
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
			if test.backendStateErr || test.marshalErr {
				out.Parked.RequestID = "gate-item"
				out.Parked.State = json.RawMessage("not-json")
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
			if test.preposted {
				out.Parked.RequestID = "gate-item"
				if err := base.AddInboxItem(ctx, scope, store.InboxItem{
					ID: out.Parked.RequestID, AgentName: scope.AgentName, RunID: "run-1",
					Kind: store.InboxKindApproval, State: store.InboxStatePending, CreatedAt: now, UpdatedAt: now,
				}); err != nil {
					t.Fatal(err)
				}
			}
			beforeAdd := func(ctx context.Context, scope store.Scope, item store.InboxItem) error {
				parked, err := base.GetRun(ctx, store.Scope{OrgUUID: scope.OrgUUID, WorkspaceUUID: scope.WorkspaceUUID, AgentName: item.AgentName}, item.RunID)
				if err != nil {
					return err
				}
				var checkpoint runCheckpoint
				if parked.Phase != store.RunPhasePendingApproval || json.Unmarshal(parked.Checkpoint, &checkpoint) != nil || checkpoint.InboxID != item.ID {
					return errors.New("inbox item published before its matching park checkpoint")
				}
				return nil
			}
			var verifyOrder func(context.Context, store.Scope, store.InboxItem) error
			if test.addInboxErr != nil {
				verifyOrder = beforeAdd
			}
			if test.marshalErr {
				out.Parked.State = json.RawMessage("not-json")
			}
			s := &Server{
				store:  parkFailureStore{Store: base, addInboxErr: test.addInboxErr, pendingRunErr: test.pendingRunErr, getRunErr: test.getRunErr, beforeAdd: verifyOrder},
				events: newEventBus(),
			}
			inboxEvents, unsubscribe := s.events.subscribe(scope)
			defer unsubscribe()
			agent := harnessAgent("edge")
			agent.Name = "coder"
			if test.backendStateErr {
				agent.Spec.Backend.Type = "model"
			}
			run := taskRun{Scope: scope, Agent: agent, RunID: "run-1", SessionID: "chat", Trigger: "chat"}
			h := &harnessTurn{backend: b, Session: store.HarnessSession{SessionID: "chat", Turns: 1}}

			result, err := s.parkRun(ctx, run, "chat", now, now, newTurnProgressTracker(0), h, out)
			if err == nil || result.Phase == store.RunPhasePendingApproval {
				t.Fatalf("failed park reported success: result=%+v err=%v", result, err)
			}
			if dispatcher.cancels != 1 || dispatcher.receipt.Phase != runner.PhaseCancelled {
				t.Fatalf("failed park left remote permission active: cancels=%d receipt=%+v", dispatcher.cancels, dispatcher.receipt)
			}
			if test.pendingRunErr != nil && !errors.Is(err, checkpointErr) {
				t.Fatalf("park error = %v, want checkpoint write error", err)
			}
			if test.getRunErr != nil && !errors.Is(err, getRunErr) {
				t.Fatalf("park error = %v, want run read error", err)
			}
			if test.backendStateErr && !strings.Contains(err.Error(), "invalid character") {
				t.Fatalf("park error = %v, want backend state decode error", err)
			}
			if test.marshalErr && !strings.Contains(err.Error(), "encoding the run's resume checkpoint") {
				t.Fatalf("park error = %v, want checkpoint marshal error", err)
			}
			if test.addInboxErr != nil && !errors.Is(err, addErr) {
				t.Fatalf("park error = %v, want inbox write error", err)
			}

			items, err := base.ListInbox(ctx, store.Scope{OrgUUID: scope.OrgUUID, WorkspaceUUID: scope.WorkspaceUUID}, "")
			if err != nil {
				t.Fatal(err)
			}
			if test.preposted {
				if len(items) != 1 || items[0].State != store.InboxStateDenied {
					t.Fatalf("failed park did not close the preposted approval: %+v", items)
				}
				return
			}
			if len(items) != 0 {
				t.Fatalf("failed park left inbox items behind: %+v", items)
			}
			for range len(inboxEvents) {
				event := <-inboxEvents
				if event.Type == "inbox" {
					t.Fatalf("failed park published inbox event: %+v", event)
				}
			}
		})
	}
}
