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

// A run handed back to the queue keeps its retry count and wake-up time across
// a save and a read, and a re-queued (Pending) run is claimable again.
func TestRunRetryFieldsRoundTripAndStayClaimable(t *testing.T) {
	ctx := context.Background()
	m := NewMemoryStore()
	scope := Scope{OrgUUID: "org", WorkspaceUUID: "ws", AgentName: "scout"}
	now := time.Date(2026, 10, 11, 9, 0, 0, 0, time.UTC)
	retryAt := now.Add(2 * time.Minute)
	if err := m.SaveRun(ctx, scope, Run{
		ID: "r1", AgentName: "scout", Trigger: "event", Phase: RunPhasePending,
		Retries: 3, RetryAt: &retryAt, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	got, err := m.GetRun(ctx, scope, "r1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Retries != 3 || got.RetryAt == nil || !got.RetryAt.Equal(retryAt) {
		t.Fatalf("run after save = retries %d retryAt %v; want 3 and %v", got.Retries, got.RetryAt, retryAt)
	}
	claimed, err := m.ClaimRun(ctx, scope, "r1", "req", now.Add(3*time.Minute))
	if err != nil {
		t.Fatalf("ClaimRun on a re-queued run: %v", err)
	}
	if claimed.Phase != RunPhaseRunning || claimed.Retries != 3 {
		t.Fatalf("claimed run = phase %q retries %d; want Running with the count kept", claimed.Phase, claimed.Retries)
	}
}

// The queue is the agent's handed-back runs, oldest first: that is the order
// they are woken in. A run that is due already, cancelled, or not handed back
// at all is not in it.
func TestListQueuedRunsIsOldestFirstAndOnlyWaitingRuns(t *testing.T) {
	ctx := context.Background()
	m := NewMemoryStore()
	scope := Scope{OrgUUID: "org", WorkspaceUUID: "ws", AgentName: "scout"}
	now := time.Date(2026, 10, 11, 9, 0, 0, 0, time.UTC)
	later := now.Add(5 * time.Minute)
	save := func(id string, created time.Time, mutate func(*Run)) {
		run := Run{ID: id, AgentName: "scout", Trigger: "event", Phase: RunPhasePending, RetryAt: &later, CreatedAt: created, UpdatedAt: created}
		if mutate != nil {
			mutate(&run)
		}
		if err := m.SaveRun(ctx, scope, run); err != nil {
			t.Fatal(err)
		}
	}
	save("newer", now.Add(2*time.Minute), nil)
	save("older", now.Add(time.Minute), nil)
	save("oldest", now, nil)
	save("cancelled", now.Add(-time.Minute), func(r *Run) { r.CancelRequested = true })
	save("never-refused", now.Add(-time.Minute), func(r *Run) { r.RetryAt = nil })
	save("running", now.Add(-time.Minute), func(r *Run) { r.Phase = RunPhaseRunning })
	save("other-agent", now.Add(-time.Minute), func(r *Run) { r.AgentName = "other" })

	got, err := m.ListQueuedRuns(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, run := range got {
		ids = append(ids, run.ID)
	}
	if want := []string{"oldest", "older", "newer"}; len(ids) != 3 || ids[0] != want[0] || ids[1] != want[1] || ids[2] != want[2] {
		t.Fatalf("queued = %v, want %v", ids, want)
	}
	if _, err := m.ListQueuedRuns(ctx, Scope{OrgUUID: "org", WorkspaceUUID: "ws"}); err == nil {
		t.Fatal("ListQueuedRuns without an agent must be refused: the queue is per agent")
	}
}
