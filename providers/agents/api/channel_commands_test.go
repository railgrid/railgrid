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
	"strings"
	"testing"
	"time"

	"github.com/railgrid/provider-agents/store"
)

func TestChannelHelpListsCommands(t *testing.T) {
	s := &Server{store: store.NewMemoryStore()}
	scope := store.Scope{OrgUUID: "org", WorkspaceUUID: "ws", AgentName: "ada"}
	reply, handled := s.channelCommand(context.Background(), scope, nil, mkAgent(""), "", "/help")
	if !handled {
		t.Fatal("/help was handed to the agent as a chat message")
	}
	for _, want := range []string{"/inbox", "/answer", "/approve", "/cancel"} {
		if !strings.Contains(reply, want) {
			t.Fatalf("/help reply %q does not mention %s", reply, want)
		}
	}
}

func TestChannelAnswerResolvesTheOnlyPendingQuestion(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemoryStore()
	s := &Server{store: st, events: newEventBus()}
	scope := store.Scope{OrgUUID: "org", WorkspaceUUID: "ws", AgentName: "ada"}
	wsScope := store.Scope{OrgUUID: "org", WorkspaceUUID: "ws"}
	now := time.Now().UTC()
	if err := st.AddInboxItem(ctx, wsScope, store.InboxItem{
		ID: "q1", AgentName: "ada", Kind: store.InboxKindQuestion, State: store.InboxStatePending,
		Prompt: "How many apples?", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}

	// No index: with one item pending, the whole tail is the answer — even
	// when it starts with a number that names no pending item.
	reply, handled := s.channelCommand(ctx, scope, nil, mkAgent(""), "", "/answer 42 apples")
	if !handled || !strings.Contains(reply, "Answered") {
		t.Fatalf("/answer reply = %q, handled %t", reply, handled)
	}
	if strings.Contains(reply, "Resuming") {
		t.Fatalf("a question bound to no run claimed to resume one: %q", reply)
	}
	item, err := st.GetInboxItem(ctx, wsScope, "q1")
	if err != nil || item.State != store.InboxStateAnswered || item.Response != "42 apples" {
		t.Fatalf("question after /answer = state %q response %q (err %v)", item.State, item.Response, err)
	}
}

func TestChannelAnswerRefusesAnApproval(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemoryStore()
	s := &Server{store: st, events: newEventBus()}
	scope := store.Scope{OrgUUID: "org", WorkspaceUUID: "ws", AgentName: "ada"}
	wsScope := store.Scope{OrgUUID: "org", WorkspaceUUID: "ws"}
	now := time.Now().UTC()
	if err := st.AddInboxItem(ctx, wsScope, store.InboxItem{
		ID: "a1", AgentName: "ada", Kind: store.InboxKindApproval, State: store.InboxStatePending,
		Prompt: "Allow the tool?", Payload: map[string]any{"tool": "notify", "args": `{"x":1}`},
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	reply, handled := s.channelCommand(ctx, scope, nil, mkAgent(""), "", "/answer yes please")
	if !handled || !strings.Contains(reply, "/approve") {
		t.Fatalf("/answer on an approval = %q, handled %t", reply, handled)
	}
	item, err := st.GetInboxItem(ctx, wsScope, "a1")
	if err != nil || item.State != store.InboxStatePending {
		t.Fatalf("approval after /answer = %q (err %v); want it left pending", item.State, err)
	}
}

func TestChannelCancelPicksAmongActiveRuns(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemoryStore()
	s := &Server{store: st, events: newEventBus(), liveRuns: newRunRegistry()}
	scope := store.Scope{OrgUUID: "org", WorkspaceUUID: "ws", AgentName: "ada"}

	reply, handled := s.channelCommand(ctx, scope, nil, mkAgent(""), "", "/cancel")
	if !handled || !strings.Contains(reply, "Nothing") {
		t.Fatalf("/cancel with nothing running = %q, handled %t", reply, handled)
	}

	now := time.Now().UTC()
	for i, run := range []store.Run{
		{ID: "run-old", AgentName: "ada", Trigger: "chat", Phase: store.RunPhasePendingApproval, Input: "older task", CreatedAt: now.Add(-time.Minute)},
		{ID: "run-new", AgentName: "ada", Trigger: "channel", Phase: store.RunPhaseRunning, Input: "newer task", CreatedAt: now},
		{ID: "run-done", AgentName: "ada", Trigger: "chat", Phase: store.RunPhaseSucceeded, Input: "finished", CreatedAt: now},
		{ID: "run-other", AgentName: "bob", Trigger: "chat", Phase: store.RunPhaseRunning, Input: "not ada's", CreatedAt: now},
	} {
		runScope := store.Scope{OrgUUID: "org", WorkspaceUUID: "ws", AgentName: run.AgentName}
		run.UpdatedAt = now
		if err := st.SaveRun(ctx, runScope, run); err != nil {
			t.Fatalf("save run %d: %v", i, err)
		}
	}

	listed, handled := s.channelCommand(ctx, scope, nil, mkAgent(""), "", "/cancel")
	if !handled || !strings.Contains(listed, "2 active runs") || !strings.Contains(listed, "/cancel N") {
		t.Fatalf("/cancel with two active runs = %q, handled %t", listed, handled)
	}
	if strings.Index(listed, "newer task") > strings.Index(listed, "older task") {
		t.Fatalf("active runs are not newest-first: %q", listed)
	}
	if strings.Contains(listed, "not ada's") || strings.Contains(listed, "finished") {
		t.Fatalf("/cancel listed a run it cannot cancel: %q", listed)
	}
	if reply, _ := s.channelCommand(ctx, scope, nil, mkAgent(""), "", "/cancel 3"); !strings.Contains(reply, "Pick a run number") {
		t.Fatalf("/cancel 3 = %q", reply)
	}

	reply, _ = s.channelCommand(ctx, scope, nil, mkAgent(""), "", "/cancel 2")
	if !strings.Contains(reply, "Cancelled") {
		t.Fatalf("/cancel 2 = %q", reply)
	}
	cancelled, err := st.GetRun(ctx, scope, "run-old")
	if err != nil || cancelled.Phase != store.RunPhaseAborted {
		t.Fatalf("run-old after /cancel 2 = %q (err %v); want Aborted", cancelled.Phase, err)
	}
	untouched, err := st.GetRun(ctx, scope, "run-new")
	if err != nil || untouched.Phase != store.RunPhaseRunning {
		t.Fatalf("run-new after /cancel 2 = %q (err %v); want it still Running", untouched.Phase, err)
	}
}

// TestChannelCancelStopsParkedHarnessRun is the Discord "abort this run" case:
// a harness run parked on a question is stopped on its runner and closed, with
// the channel's cluster ID addressing the runner exactly as the cancel verb's
// does.
func TestChannelCancelStopsParkedHarnessRun(t *testing.T) {
	agent := harnessAgent("edge-test")
	agent.UID = "agent-uid"
	f := newParkedCancelFixture(t, agent, nil)
	f.inboxID = "inbox-parked-question"
	run := f.park(t, true)
	workspaceScope := store.Scope{OrgUUID: f.scope.OrgUUID, WorkspaceUUID: f.scope.WorkspaceUUID}
	now := time.Now().UTC()
	if err := f.server.store.AddInboxItem(context.Background(), workspaceScope, store.InboxItem{
		ID: f.inboxID, AgentName: run.AgentName, RunID: run.ID, Kind: store.InboxKindQuestion,
		State: store.InboxStatePending, Prompt: "Can you add me to the adm group?",
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	f.dispatcher.once.Do(func() { close(f.dispatcher.release) })

	channelScope := store.Scope{OrgUUID: f.scope.OrgUUID, WorkspaceUUID: f.scope.WorkspaceUUID, AgentName: agent.Name, ClusterID: "cluster-test"}
	reply, handled := f.server.channelCommand(context.Background(), channelScope, f.dynamic, agent, "", "/cancel")
	if !handled || !strings.Contains(reply, "Cancelled") {
		t.Fatalf("/cancel reply = %q, handled %t", reply, handled)
	}
	stored, err := f.server.store.GetRun(context.Background(), f.scope, run.ID)
	if err != nil || stored.Phase != store.RunPhaseAborted {
		t.Fatalf("run after /cancel = %+v, %v; want Aborted", stored, err)
	}
	select {
	case dial := <-f.dialed:
		if dial.ref.Cluster != "cluster-test" || dial.ref.Service != "edge-test-codex" {
			t.Fatalf("runner dial ref = %+v, want the checkpoint's saved edge service", dial.ref)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("/cancel did not reach the parked run's runner")
	}
	item, err := f.server.store.GetInboxItem(context.Background(), workspaceScope, f.inboxID)
	if err != nil || item.State == store.InboxStatePending {
		t.Fatalf("question after /cancel = %q (err %v); want it closed", item.State, err)
	}
}
