// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");

package store

import (
	"errors"
	"testing"
	"time"
)

func TestMemoryRunLeaseFencesReacquiredOwner(t *testing.T) {
	testRunLeaseFencesReacquiredOwner(t, NewMemoryStore(), Scope{OrgUUID: "lease-org", WorkspaceUUID: "lease-ws", AgentName: "agent"})
}

func TestPostgresRunLeaseFencesReacquiredOwner(t *testing.T) {
	st := openTestPostgres(t)
	testRunLeaseFencesReacquiredOwner(t, st, pgScope(t, st))
}

func testRunLeaseFencesReacquiredOwner(t *testing.T, st Store, scope Scope) {
	t.Helper()
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Microsecond)
	seed := Run{
		ID: "lease-run", AgentName: scope.AgentName, Phase: RunPhasePending, CreatedAt: now, UpdatedAt: now,
		Backend: "harness", AttemptID: "runner-attempt-1", Repository: &RunRepository{RepositoryID: "railgrid", BaseCommit: "base"},
	}
	if err := st.SaveRun(ctx, scope, seed); err != nil {
		t.Fatal(err)
	}

	fresh, err := st.ClaimFreshRun(ctx, scope, seed.ID, seed.UpdatedAt, "owner-1", "agent-uid", RecoveryIdentityAgent, now)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.Attempt != 0 {
		t.Fatalf("fresh claim changed recovery attempt: %d", fresh.Attempt)
	}

	agePostgresLease(t, st, scope, seed.ID)
	recovered, err := st.ClaimRecovery(ctx, scope, seed.ID, fresh.UpdatedAt, "owner-2", now.Add(RunLeaseDuration+time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Attempt != 1 || recovered.ExecutionEpoch <= fresh.ExecutionEpoch || recovered.AttemptID != seed.AttemptID || recovered.Repository == nil || recovered.Repository.BaseCommit != "base" {
		t.Fatalf("recovery did not advance recovery/lease identity: attempt=%d epoch=%d, prior epoch=%d", recovered.Attempt, recovered.ExecutionEpoch, fresh.ExecutionEpoch)
	}

	stale := recovered
	stale.Phase = RunPhaseSucceeded
	stale.Output = "stale executor"
	if ok, err := st.SaveRunOwned(ctx, scope, stale, fresh.ExecutionOwner, fresh.ExecutionEpoch, now.Add(RunLeaseDuration+2*time.Second)); ok || !errors.Is(err, ErrRunLeaseLost) {
		t.Fatalf("stale owner write = (%t, %v), want lease lost", ok, err)
	}

	current := recovered
	current.Phase = RunPhaseSucceeded
	current.Output = "current executor"
	if ok, err := st.SaveRunOwned(ctx, scope, current, recovered.ExecutionOwner, recovered.ExecutionEpoch, now.Add(RunLeaseDuration+2*time.Second)); err != nil || !ok {
		t.Fatalf("current owner write = (%t, %v), want success", ok, err)
	}
	got, err := st.GetRun(ctx, scope, seed.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Output != "current executor" || got.Attempt != 1 {
		t.Fatalf("terminal run = %+v", got)
	}
}

func TestMemoryParkReleasesRunLease(t *testing.T) {
	testParkReleasesRunLease(t, NewMemoryStore(), Scope{OrgUUID: "park-org", WorkspaceUUID: "park-ws", AgentName: "agent"})
}

func TestPostgresParkReleasesRunLease(t *testing.T) {
	st := openTestPostgres(t)
	testParkReleasesRunLease(t, st, pgScope(t, st))
}

func testParkReleasesRunLease(t *testing.T, st Store, scope Scope) {
	t.Helper()
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Microsecond)
	run := Run{ID: "park-run", AgentName: scope.AgentName, Phase: RunPhasePending, CreatedAt: now, UpdatedAt: now}
	if err := st.SaveRun(ctx, scope, run); err != nil {
		t.Fatal(err)
	}
	run, err := st.ClaimFreshRun(ctx, scope, run.ID, now, "before", "agent-uid", RecoveryIdentityCaller, now)
	if err != nil {
		t.Fatal(err)
	}
	run.Phase = RunPhasePendingApproval
	run.Checkpoint = []byte(`{"inboxID":"question"}`)
	if ok, err := st.SaveRunOwned(ctx, scope, run, "before", run.ExecutionEpoch, now.Add(time.Second)); !ok || err != nil {
		t.Fatalf("park = %t, %v", ok, err)
	}
	parked, err := st.GetRun(ctx, scope, run.ID)
	if err != nil || parked.ExecutionOwner != "" || !parked.ExecutionLeaseAt.IsZero() {
		t.Fatalf("parked owner = %q at %v, err=%v", parked.ExecutionOwner, parked.ExecutionLeaseAt, err)
	}
	resumed, err := st.ClaimRunWithLease(ctx, scope, run.ID, "after", "agent-uid", RecoveryIdentityCaller, now.Add(2*time.Second), "question")
	if err != nil || resumed.ExecutionEpoch <= run.ExecutionEpoch {
		t.Fatalf("approval resume = %+v, %v", resumed, err)
	}
	if _, ok, err := st.RenewRunLease(ctx, scope, run.ID, "before", run.ExecutionEpoch, now.Add(3*time.Second)); ok || err != nil {
		t.Fatalf("stale renew = %t, %v", ok, err)
	}
}

func agePostgresLease(t *testing.T, st Store, scope Scope, runID string) {
	t.Helper()
	if pg, ok := st.(*PostgresStore); ok {
		_, err := pg.db.ExecContext(t.Context(), `UPDATE agents_runs SET execution_lease_at=statement_timestamp() - make_interval(secs => $4) WHERE org_uuid=$1 AND workspace_uuid=$2 AND id=$3`, scope.OrgUUID, scope.WorkspaceUUID, runID, RunLeaseDuration.Seconds()+1)
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestPostgresFastReplicaCannotExpireLiveLease(t *testing.T) {
	st := openTestPostgres(t)
	scope := pgScope(t, st)
	now := time.Now().UTC().Truncate(time.Microsecond)
	run := Run{ID: "skewed-lease", AgentName: scope.AgentName, Phase: RunPhasePending, CreatedAt: now, UpdatedAt: now}
	if err := st.SaveRun(t.Context(), scope, run); err != nil {
		t.Fatal(err)
	}
	live, err := st.ClaimFreshRun(t.Context(), scope, run.ID, now, "owner", "agent-uid", RecoveryIdentityAgent, now)
	if err != nil {
		t.Fatal(err)
	}
	future := now.Add(time.Hour)
	if _, err := st.ClaimRecovery(t.Context(), scope, run.ID, live.UpdatedAt, "fast-replica", future); !errors.Is(err, ErrRecoveryClaimStale) {
		t.Fatalf("fast replica took over a live lease: %v", err)
	}
	refreshed, ok, err := st.RenewRunLease(t.Context(), scope, run.ID, live.ExecutionOwner, live.ExecutionEpoch, future)
	if err != nil || !ok || !refreshed.ExecutionLeaseAt.Before(future.Add(-time.Minute)) {
		t.Fatalf("renew with clock skew = %+v, %t, %v", refreshed, ok, err)
	}
}
