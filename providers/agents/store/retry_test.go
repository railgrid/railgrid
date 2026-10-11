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
