// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package store

import (
	"context"
	"testing"
	"time"
)

func testScope() Scope {
	return Scope{OrgUUID: "org1", WorkspaceUUID: "ws1", AgentName: "helper"}
}

func legacyTaskIdentity(taskID string) HarnessIdentity {
	return HarnessIdentity{TaskID: taskID, LegacyTaskID: taskID}
}

func TestMemoryStore_HarnessSessionGatesReceiptsByObservedEpoch(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryStore()
	sc := testScope()
	now := time.Now().UTC()

	identity := legacyTaskIdentity("agent-helper-chat")
	first, err := s.NextHarnessTurn(ctx, sc, "chat", now, identity)
	if err != nil {
		t.Fatalf("claim first turn: %v", err)
	}
	second, err := s.NextHarnessTurn(ctx, sc, "chat", now.Add(time.Second), identity)
	if err != nil {
		t.Fatalf("claim second turn: %v", err)
	}
	if err := s.PutHarnessSession(ctx, sc, HarnessSession{
		SessionID: "chat", TaskID: first.TaskID, Turns: first.Turns, ObservedEpoch: first.Turns, BackendKey: "backend-a",
		HarnessSessionID: "first-thread", UpdatedAt: now.Add(2 * time.Second),
	}); err != nil {
		t.Fatalf("persist first completed turn after second allocation: %v", err)
	}
	got, ok, err := s.GetHarnessSession(ctx, sc, "chat")
	if err != nil || !ok || got.Turns != second.Turns || got.ObservedEpoch != first.Turns ||
		got.HarnessSessionID != "first-thread" || got.BackendKey != "backend-a" {
		t.Fatalf("first receipt after later allocation = %+v, ok=%v, err=%v; want allocated epoch %d and observed first-thread/backend-a at epoch %d",
			got, ok, err, second.Turns, first.Turns)
	}
	if err := s.PutHarnessSession(ctx, sc, HarnessSession{
		SessionID: "chat", TaskID: second.TaskID, Turns: second.Turns, ObservedEpoch: second.Turns, BackendKey: "backend-b",
		HarnessSessionID: "newer-thread", UpdatedAt: now.Add(3 * time.Second),
	}); err != nil {
		t.Fatalf("persist second turn: %v", err)
	}
	if err := s.PutHarnessSession(ctx, sc, HarnessSession{
		SessionID: "chat", TaskID: first.TaskID, Turns: first.Turns, ObservedEpoch: first.Turns, BackendKey: "backend-a",
		HarnessSessionID: "older-thread", UpdatedAt: now.Add(4 * time.Second),
	}); err != nil {
		t.Fatalf("persist late first receipt: %v", err)
	}
	// An empty receipt is not allowed to clear the session, even when it comes
	// from the latest turn.
	if err := s.PutHarnessSession(ctx, sc, HarnessSession{
		SessionID: "chat", TaskID: second.TaskID, Turns: second.Turns, ObservedEpoch: second.Turns, UpdatedAt: now.Add(5 * time.Second),
	}); err != nil {
		t.Fatalf("persist empty receipt: %v", err)
	}

	got, ok, err = s.GetHarnessSession(ctx, sc, "chat")
	if err != nil || !ok || got.Turns != second.Turns || got.ObservedEpoch != second.Turns ||
		got.HarnessSessionID != "newer-thread" || got.BackendKey != "backend-b" {
		t.Fatalf("session after late writes = %+v, ok=%v, err=%v; want allocated epoch %d, observed epoch %d and newer-thread/backend-b",
			got, ok, err, second.Turns, second.Turns)
	}
	third, err := s.NextHarnessTurn(ctx, sc, "chat", now.Add(5*time.Second), identity)
	if err != nil || third.Turns != 3 || third.HarnessSessionID != "newer-thread" {
		t.Fatalf("next turn = %+v, err=%v; want epoch 3 resuming newer-thread", third, err)
	}
}

func TestMemoryStore_DeleteSessionRetainsHarnessEpochFence(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryStore()
	sc := testScope()
	now := time.Now().UTC()
	identity := HarnessIdentity{TaskID: "agent-helper-uid-reused", LegacyTaskID: "agent-helper-reused", AgentUID: "uid-reused"}
	first, err := s.NextHarnessTurn(ctx, sc, "reused", now, identity)
	if err != nil {
		t.Fatalf("allocate first turn: %v", err)
	}
	if err := s.PutHarnessSession(ctx, sc, HarnessSession{
		SessionID: "reused", TaskID: first.TaskID, AgentUID: identity.AgentUID, HarnessSessionID: "runner-thread-old", BackendKey: "edge-a",
		Turns: first.Turns, ObservedEpoch: first.Turns, UpdatedAt: now.Add(time.Second),
	}); err != nil {
		t.Fatalf("save first receipt: %v", err)
	}
	if err := s.DeleteSession(ctx, sc, "reused"); err != nil {
		t.Fatalf("delete session: %v", err)
	}
	tombstone, ok, err := s.GetHarnessSession(ctx, sc, "reused")
	if err != nil || !ok || tombstone.Turns != 1 || tombstone.ObservedEpoch != 2 || tombstone.TaskID != first.TaskID || tombstone.HarnessSessionID != "" || tombstone.BackendKey != "" {
		t.Fatalf("post-delete tombstone = %+v, ok=%v, err=%v", tombstone, ok, err)
	}
	next, err := s.NextHarnessTurn(ctx, sc, "reused", now.Add(2*time.Second), identity)
	if err != nil || next.Turns != 2 || next.TaskID != first.TaskID || next.HarnessSessionID != "" {
		t.Fatalf("reused session turn = %+v, err=%v; want fresh native session at epoch 2", next, err)
	}
	if err := s.PutHarnessSession(ctx, sc, HarnessSession{
		SessionID: "reused", TaskID: first.TaskID, AgentUID: identity.AgentUID, HarnessSessionID: "runner-thread-old", BackendKey: "edge-a",
		Turns: 1, ObservedEpoch: 1, UpdatedAt: now.Add(3 * time.Second),
	}); err != nil {
		t.Fatalf("save late old receipt: %v", err)
	}
	late, _, err := s.GetHarnessSession(ctx, sc, "reused")
	if err != nil || late.HarnessSessionID != "" || late.ObservedEpoch != 2 {
		t.Fatalf("late receipt crossed delete fence: %+v, err=%v", late, err)
	}
	if err := s.PutHarnessSession(ctx, sc, HarnessSession{
		SessionID: "reused", TaskID: next.TaskID, AgentUID: identity.AgentUID, HarnessSessionID: "runner-thread-new", BackendKey: "edge-a",
		Turns: 2, ObservedEpoch: 2, UpdatedAt: now.Add(4 * time.Second),
	}); err != nil {
		t.Fatalf("save new receipt: %v", err)
	}
	current, _, err := s.GetHarnessSession(ctx, sc, "reused")
	if err != nil || current.HarnessSessionID != "runner-thread-new" || current.ObservedEpoch != 2 {
		t.Fatalf("new receipt did not replace tombstone: %+v, err=%v", current, err)
	}
}

func TestMemoryStore_NextHarnessTurnSelectsAndKeepsTaskIdentity(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryStore()
	sc := testScope()
	now := time.Now().UTC()

	identity := HarnessIdentity{TaskID: "new-uid-task", LegacyTaskID: "new-legacy-task", AgentUID: "new-agent-uid", CreatedAt: now.Add(-3 * time.Second).Truncate(time.Second)}
	created, err := s.NextHarnessTurn(ctx, sc, "new", now, identity)
	if err != nil || created.TaskID != "new-uid-task" {
		t.Fatalf("new session identity = %+v, err=%v; want UID task", created, err)
	}
	next, err := s.NextHarnessTurn(ctx, sc, "new", now.Add(time.Second), identity)
	if err != nil || next.TaskID != "new-uid-task" {
		t.Fatalf("existing session identity changed = %+v, err=%v", next, err)
	}

	// A session row created by the old provider has no marker. Its first
	// post-upgrade claim selects and persists the legacy task identity.
	legacyID := legacyTaskIdentity("legacy-task")
	if _, err := s.NextHarnessTurn(ctx, sc, "legacy", now, legacyID); err != nil {
		t.Fatal(err)
	}
	newIdentity := HarnessIdentity{TaskID: "wrong-new-uid", LegacyTaskID: "legacy-task", AgentUID: "new-agent-uid", CreatedAt: now.Add(-3 * time.Second).Truncate(time.Second)}
	legacy, err := s.NextHarnessTurn(ctx, sc, "legacy", now.Add(time.Second), newIdentity)
	if err != nil || legacy.TaskID != "legacy-task" {
		t.Fatalf("unmarked existing session identity = %+v, err=%v; want legacy marker", legacy, err)
	}
	if err := s.PutHarnessSession(ctx, sc, HarnessSession{SessionID: "legacy", TaskID: "wrong-task", AgentUID: "new-agent-uid", Turns: legacy.Turns, UpdatedAt: now.Add(2 * time.Second)}); err == nil {
		t.Fatal("receipt with mismatched identity was accepted")
	}
	stored, ok, err := s.GetHarnessSession(ctx, sc, "legacy")
	if err != nil || !ok || stored.TaskID != "legacy-task" {
		t.Fatalf("receipt overwrote task identity = %+v, ok=%v, err=%v", stored, ok, err)
	}

	if err := s.DeleteAgentData(ctx, sc, sc.AgentName); err != nil {
		t.Fatal(err)
	}
	recreatedIdentity := HarnessIdentity{TaskID: "recreated-uid-task", LegacyTaskID: "new-legacy-task", AgentUID: "recreated-agent-uid", CreatedAt: now.Add(3 * time.Second).Truncate(time.Second)}
	recreated, err := s.NextHarnessTurn(ctx, sc, "new", now.Add(4*time.Second), recreatedIdentity)
	if err != nil || recreated.TaskID != "recreated-uid-task" {
		t.Fatalf("recreated Agent session identity = %+v, err=%v", recreated, err)
	}
}

func TestMemoryStore_RecreatedAgentResetsSessionAndFencesLateReceipt(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryStore()
	sc := testScope()
	base := time.Now().UTC().Truncate(time.Second)
	oldIdentity := HarnessIdentity{TaskID: "task-old", LegacyTaskID: "legacy-task", AgentUID: "uid-old", CreatedAt: base.Add(-10 * time.Second)}
	old, err := s.NextHarnessTurn(ctx, sc, "chat", base, oldIdentity)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.PutHarnessSession(ctx, sc, HarnessSession{
		SessionID: "chat", TaskID: oldIdentity.TaskID, AgentUID: oldIdentity.AgentUID,
		HarnessSessionID: "old-native", BackendKey: "edge", Turns: 8, ObservedEpoch: 8,
		UpdatedAt: base.Add(time.Second),
	}); err != nil {
		t.Fatal(err)
	}
	newIdentity := HarnessIdentity{TaskID: "task-new", LegacyTaskID: "legacy-task", AgentUID: "uid-new", CreatedAt: base.Add(2 * time.Second)}
	newTurn, err := s.NextHarnessTurn(ctx, sc, "chat", base.Add(3*time.Second), newIdentity)
	if err != nil || newTurn.TaskID != newIdentity.TaskID || newTurn.AgentUID != newIdentity.AgentUID || newTurn.Turns != 1 || newTurn.HarnessSessionID != "" {
		t.Fatalf("recreated Agent did not get a fresh task: %+v, err=%v", newTurn, err)
	}
	if err := s.PutHarnessSession(ctx, sc, HarnessSession{
		SessionID: "chat", TaskID: oldIdentity.TaskID, AgentUID: oldIdentity.AgentUID,
		HarnessSessionID: "late-old-native", BackendKey: "edge", Turns: old.Turns,
		ObservedEpoch: 20, UpdatedAt: base.Add(4 * time.Second),
	}); err == nil {
		t.Fatal("late receipt from the deleted Agent incarnation was accepted")
	}
	current, ok, err := s.GetHarnessSession(ctx, sc, "chat")
	if err != nil || !ok || current.TaskID != newIdentity.TaskID || current.AgentUID != newIdentity.AgentUID || current.HarnessSessionID != "" {
		t.Fatalf("late receipt changed recreated Agent state: %+v, ok=%v, err=%v", current, ok, err)
	}
}

func TestMemoryStore_MessagesRoundTrip(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryStore()
	sc := testScope()
	base := time.Now().UTC()

	for i := range 5 {
		if err := s.AppendMessage(ctx, sc, Message{
			ID:        string(rune('a' + i)),
			AgentName: sc.AgentName,
			SessionID: "sess",
			Role:      "user",
			Content:   "hi",
			CreatedAt: base.Add(time.Duration(i) * time.Second),
		}); err != nil {
			t.Fatalf("append: %v", err)
		}
	}

	recent, err := s.LoadRecentMessages(ctx, sc, "sess", 3)
	if err != nil {
		t.Fatalf("recent: %v", err)
	}
	if len(recent) != 3 || recent[len(recent)-1].ID != "e" {
		t.Fatalf("recent got %d msgs, last=%q", len(recent), recent[len(recent)-1].ID)
	}

	// Cursor pagination: 2 + 2 + 1.
	seen := 0
	cursor := ""
	for range 10 {
		page, err := s.ListMessages(ctx, sc, "sess", 2, cursor)
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		seen += len(page.Items)
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}
	if seen != 5 {
		t.Fatalf("paginated %d messages, want 5", seen)
	}
}

func TestMemoryStore_RunClaimIsExclusive(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryStore()
	sc := testScope()
	now := time.Now().UTC()

	if err := s.SaveRun(ctx, sc, Run{ID: "r1", AgentName: sc.AgentName, Trigger: "schedule", Phase: RunPhasePending, CreatedAt: now}); err != nil {
		t.Fatalf("save: %v", err)
	}
	if _, err := s.ClaimRun(ctx, sc, "r1", "req-1", now); err != nil {
		t.Fatalf("first claim should win: %v", err)
	}
	if _, err := s.ClaimRun(ctx, sc, "r1", "req-2", now); err == nil {
		t.Fatalf("second claim should fail while running")
	}
}

func TestMemoryStore_RunClaimRejectsCancelledAndTerminalRuns(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryStore()
	sc := testScope()
	now := time.Now().UTC()

	if err := s.SaveRun(ctx, sc, Run{
		ID: "cancelled-approval", AgentName: sc.AgentName, Trigger: "chat",
		Phase: RunPhasePendingApproval, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("save pending approval: %v", err)
	}
	if err := s.RequestCancel(ctx, sc, "cancelled-approval", now); err != nil {
		t.Fatalf("request cancel: %v", err)
	}
	if _, err := s.ClaimRun(ctx, sc, "cancelled-approval", "late-approval", now); err == nil {
		t.Fatal("ClaimRun succeeded after cancellation was recorded")
	}
	stored, err := s.GetRun(ctx, sc, "cancelled-approval")
	if err != nil || stored.Phase != RunPhasePendingApproval || !stored.CancelRequested {
		t.Fatalf("cancelled run after failed claim = %+v, %v", stored, err)
	}

	if err := s.SaveRun(ctx, sc, Run{
		ID: "already-aborted", AgentName: sc.AgentName, Trigger: "chat",
		Phase: RunPhaseAborted, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("save aborted run: %v", err)
	}
	if _, err := s.ClaimRun(ctx, sc, "already-aborted", "late-approval", now); err == nil {
		t.Fatal("ClaimRun succeeded for an aborted run")
	}
	stored, err = s.GetRun(ctx, sc, "already-aborted")
	if err != nil || stored.Phase != RunPhaseAborted {
		t.Fatalf("aborted run after failed claim = %+v, %v", stored, err)
	}
}

func TestMemoryStore_RunWorkedDurationKeepsUnknownAndMeasuredZeroDistinct(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryStore()
	sc := testScope()
	now := time.Now().UTC()

	if err := s.SaveRun(ctx, sc, Run{
		ID: "unknown", AgentName: sc.AgentName, Trigger: "chat", Phase: RunPhaseSucceeded,
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("save unknown: %v", err)
	}
	unknown, err := s.GetRun(ctx, sc, "unknown")
	if err != nil {
		t.Fatalf("get unknown: %v", err)
	}
	if unknown.WorkedDurationMS != nil {
		t.Fatalf("unknown worked duration = %v, want nil", unknown.WorkedDurationMS)
	}

	zero := int64(0)
	if err := s.SaveRun(ctx, sc, Run{
		ID: "zero", AgentName: sc.AgentName, Trigger: "chat", Phase: RunPhaseSucceeded,
		CreatedAt: now.Add(time.Second), UpdatedAt: now.Add(time.Second), WorkedDurationMS: &zero,
	}); err != nil {
		t.Fatalf("save measured zero: %v", err)
	}
	measuredZero, err := s.GetRun(ctx, sc, "zero")
	if err != nil {
		t.Fatalf("get measured zero: %v", err)
	}
	if measuredZero.WorkedDurationMS == nil || *measuredZero.WorkedDurationMS != 0 {
		t.Fatalf("measured zero worked duration = %v, want pointer to zero", measuredZero.WorkedDurationMS)
	}

	positive := int64(4200)
	if err := s.SaveRun(ctx, sc, Run{
		ID: "positive", AgentName: sc.AgentName, Trigger: "chat", Phase: RunPhaseSucceeded,
		CreatedAt: now.Add(2 * time.Second), UpdatedAt: now.Add(2 * time.Second), WorkedDurationMS: &positive,
	}); err != nil {
		t.Fatalf("save positive: %v", err)
	}
	page, err := s.QueryRuns(ctx, sc, RunQuery{Limit: 10})
	if err != nil {
		t.Fatalf("query runs: %v", err)
	}
	seen := map[string]*int64{}
	for _, run := range page.Items {
		seen[run.ID] = run.WorkedDurationMS
	}
	if seen["unknown"] != nil {
		t.Fatalf("queried unknown worked duration = %v, want nil", seen["unknown"])
	}
	if seen["zero"] == nil || *seen["zero"] != 0 {
		t.Fatalf("queried measured zero worked duration = %v, want pointer to zero", seen["zero"])
	}
	if seen["positive"] == nil || *seen["positive"] != positive {
		t.Fatalf("queried positive worked duration = %v, want %d", seen["positive"], positive)
	}
}

func TestMemoryStore_UsageAccumulates(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryStore()
	sc := testScope()
	now := time.Now().UTC()
	win := 30 * 24 * time.Hour

	if _, err := s.AddUsage(ctx, sc, "helper", 100, 50, 2000, now, win); err != nil {
		t.Fatalf("add usage: %v", err)
	}
	u, err := s.AddUsage(ctx, sc, "helper", 10, 5, 300, now, win)
	if err != nil {
		t.Fatalf("add usage 2: %v", err)
	}
	if u.InputTokens != 110 || u.OutputTokens != 55 || u.USDMicros != 2300 {
		t.Fatalf("usage rollup wrong: %+v", u)
	}
	got, err := s.GetUsage(ctx, sc, "helper", now, win)
	if err != nil {
		t.Fatalf("get usage: %v", err)
	}
	if got.USDMicros != 2300 {
		t.Fatalf("get usage got %d micros, want 2300", got.USDMicros)
	}
}

func TestMemoryStore_InboxResolve(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryStore()
	sc := testScope()
	now := time.Now().UTC()

	if err := s.AddInboxItem(ctx, sc, InboxItem{
		ID: "i1", AgentName: "helper", RunID: "r1",
		Kind: InboxKindApproval, State: InboxStatePending,
		Prompt: "merge PR #42?", CreatedAt: now,
	}); err != nil {
		t.Fatalf("add inbox: %v", err)
	}
	got, err := s.GetInboxItem(ctx, sc, "i1")
	if err != nil || got.RunID != "r1" {
		t.Fatalf("get inbox: %v %+v", err, got)
	}
	pending, err := s.ListInbox(ctx, sc, InboxStatePending)
	if err != nil || len(pending) != 1 {
		t.Fatalf("list pending: %v n=%d", err, len(pending))
	}
	if _, err := s.ResolveInboxItem(ctx, sc, "i1", InboxStateApproved, "ok", now); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	pending, _ = s.ListInbox(ctx, sc, InboxStatePending)
	if len(pending) != 0 {
		t.Fatalf("still %d pending after resolve", len(pending))
	}
}
