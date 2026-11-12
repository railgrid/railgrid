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
	"testing"
	"time"

	"github.com/railgrid/provider-agents/store"
)

func TestApprovalResumeUsesFreshExecutionNonce(t *testing.T) {
	ctx := context.Background()
	scope := store.Scope{OrgUUID: "org", WorkspaceUUID: "workspace", AgentName: "researcher"}
	st := store.NewMemoryStore()
	s := &Server{cfg: Config{ProcessID: "agents-process-incarnation"}, store: st}

	now := time.Now().UTC().Truncate(time.Microsecond)
	pending := store.Run{
		ID: "approval-run", AgentName: scope.AgentName, Phase: store.RunPhasePending,
		Checkpoint: []byte(`{"inboxID":"approval-request"}`), CreatedAt: now, UpdatedAt: now,
	}
	if err := st.SaveRun(ctx, scope, pending); err != nil {
		t.Fatal(err)
	}

	oldOwner := newExecutionOwner(s.cfg.ProcessID)
	firstClaim, err := st.ClaimFreshRun(ctx, scope, pending.ID, pending.UpdatedAt, oldOwner, pending.AgentUID, now.Add(time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	parked, _, stopOld, err := s.startRunLease(ctx, scope, firstClaim)
	if err != nil {
		t.Fatal(err)
	}
	oldStopped := false
	t.Cleanup(func() {
		if !oldStopped {
			stopOld()
		}
	})
	parked.Phase = store.RunPhasePendingApproval
	parked.UpdatedAt = now.Add(2 * time.Millisecond)
	parked.Checkpoint = []byte(`{"inboxID":"approval-request"}`)
	if updated, err := st.SaveRunOwned(ctx, scope, parked, oldOwner, parked.Attempt, parked.UpdatedAt); err != nil || !updated {
		t.Fatalf("parking with first owner: updated=%t err=%v", updated, err)
	}

	approvalClaim, err := st.ClaimRun(ctx, scope, pending.ID, "", now.Add(3*time.Millisecond), "approval-request")
	if err != nil {
		t.Fatal(err)
	}
	resumed, resumedCtx, stopResumed, err := s.startRunLease(ctx, scope, approvalClaim)
	if err != nil {
		t.Fatal(err)
	}
	defer stopResumed()
	if resumed.ExecutionOwner == oldOwner || !executionOwnerBelongsToProcess(resumed.ExecutionOwner, s.cfg.ProcessID) {
		t.Fatalf("approval resume reused or lost the process-scoped execution nonce: old=%q resumed=%q", oldOwner, resumed.ExecutionOwner)
	}
	if resumed.Attempt != firstClaim.Attempt {
		t.Fatalf("approval resume spent recovery attempt budget: first=%d resumed=%d", firstClaim.Attempt, resumed.Attempt)
	}

	// The parked executor can wake after approval on the same process. Its old
	// deferred release and lifecycle writes must not match the resumed nonce.
	stopOld()
	oldStopped = true
	staleTerminal := parked
	staleTerminal.Phase = store.RunPhaseSucceeded
	if updated, err := st.SaveRunOwned(ctx, scope, staleTerminal, oldOwner, parked.Attempt, now.Add(4*time.Millisecond)); err != nil || updated {
		t.Fatalf("old terminal write after same-process resume: updated=%t err=%v", updated, err)
	}
	if _, ok, err := st.RenewRunLease(ctx, scope, pending.ID, oldOwner, parked.Attempt, now.Add(4*time.Millisecond)); err != nil || ok {
		t.Fatalf("old heartbeat after same-process resume: renewed=%t err=%v", ok, err)
	}
	if released, err := st.ReleaseRunLease(ctx, scope, pending.ID, oldOwner, parked.Attempt); err != nil || released {
		t.Fatalf("old release after same-process resume: released=%t err=%v", released, err)
	}
	checkpointUpdated, err := st.UpdateRunCheckpoint(ctx, scope, pending.ID, oldOwner, parked.Attempt, resumed.UpdatedAt,
		[]byte(`{"stale":true}`), nil, now.Add(4*time.Millisecond))
	if err != nil || checkpointUpdated {
		t.Fatalf("old checkpoint after same-process resume: updated=%t err=%v", checkpointUpdated, err)
	}
	if cause := context.Cause(resumedCtx); cause != nil {
		t.Fatalf("new approval-resume lease was canceled by old-owner cleanup: %v", cause)
	}

	latest, err := st.GetRun(ctx, scope, pending.ID)
	if err != nil {
		t.Fatal(err)
	}
	if latest.Phase != store.RunPhaseRunning || latest.ExecutionOwner != resumed.ExecutionOwner || latest.ExecutionLeaseAt.IsZero() || latest.Attempt != resumed.Attempt || string(latest.Checkpoint) != string(parked.Checkpoint) {
		t.Fatalf("old executor changed the resumed run: phase=%q owner=%q lease=%s attempt=%d checkpoint=%s", latest.Phase, latest.ExecutionOwner, latest.ExecutionLeaseAt, latest.Attempt, latest.Checkpoint)
	}
}
