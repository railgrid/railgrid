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
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/railgrid/provider-agents/backend"
)

func TestMemoryRunExecutionLeaseContract(t *testing.T) {
	testRunExecutionLeaseContract(t, NewMemoryStore(), Scope{OrgUUID: "lease-org", WorkspaceUUID: "lease-ws", AgentName: "agent"})
}

func TestPostgresRunExecutionLeaseContract(t *testing.T) {
	st := openTestPostgres(t)
	testRunExecutionLeaseContract(t, st, pgScope(t, st))
}

func testRunExecutionLeaseContract(t *testing.T, st Store, scope Scope) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	const oldOwner = "provider-a-incarnation-1"
	const newOwner = "provider-b-incarnation-1"

	t.Run("successful completion commits the final message with the run", func(t *testing.T) {
		seed := leaseTestRun(scope, "owned-final", now)
		if err := st.SaveRun(ctx, scope, seed); err != nil {
			t.Fatal(err)
		}
		leased, err := st.AcquireRunLease(ctx, scope, seed.ID, oldOwner, seed.Attempt, now)
		if err != nil {
			t.Fatal(err)
		}
		stale := seed
		stale.Output = "unowned update"
		if err := st.SaveRun(ctx, scope, stale); !errors.Is(err, ErrRunLeaseLost) {
			t.Fatalf("unowned save modified a leased run: %v", err)
		}
		leased.Phase = RunPhaseSucceeded
		final := Message{ID: scope.OrgUUID + "-final", RunID: seed.ID, AgentName: scope.AgentName, SessionID: "owned-final", Role: "assistant", Content: "answer", CreatedAt: now}
		if ok, err := st.CompleteRun(ctx, scope, leased, newOwner, seed.Attempt, now, final); err != nil || ok {
			t.Fatalf("foreign owner completed the run: ok=%t err=%v", ok, err)
		}
		if _, postgres := st.(*PostgresStore); postgres {
			// Force a transcript write error after the run update, then verify
			// the transaction rolled back both sides.
			invalid := final
			invalid.Metadata = map[string]any{"unserializable": make(chan int)}
			if ok, err := st.CompleteRun(ctx, scope, leased, oldOwner, seed.Attempt, now, invalid); err == nil || ok {
				t.Fatalf("completed with invalid final message: ok=%t err=%v", ok, err)
			}
		}
		current, err := st.GetRun(ctx, scope, seed.ID)
		if err != nil || current.Phase != RunPhaseRunning {
			t.Fatalf("failed completion changed phase: %s, %v", current.Phase, err)
		}
		if rows, err := st.LoadRecentMessages(ctx, scope, final.SessionID, 10); err != nil || len(rows) != 0 {
			t.Fatalf("failed completion wrote a transcript row: %+v, %v", rows, err)
		}
		if ok, err := st.CompleteRun(ctx, scope, leased, oldOwner, seed.Attempt, now, final); err != nil || !ok {
			t.Fatalf("could not complete with final message: ok=%t err=%v", ok, err)
		}
		current, err = st.GetRun(ctx, scope, seed.ID)
		if err != nil || current.Phase != RunPhaseSucceeded {
			t.Fatalf("run did not succeed: %s, %v", current.Phase, err)
		}
		if rows, err := st.LoadRecentMessages(ctx, scope, final.SessionID, 10); err != nil || len(rows) != 1 || rows[0].Content != final.Content {
			t.Fatalf("missing final answer: %+v, %v", rows, err)
		}
	})

	t.Run("concurrent child admission respects the delegation cap across agents", func(t *testing.T) {
		const callers = 8
		start := make(chan struct{})
		results := make(chan error, callers)
		for i := range callers {
			go func() {
				<-start
				childScope := scope
				childScope.AgentName = fmt.Sprintf("worker-%d", i)
				results <- st.SaveRun(ctx, childScope, Run{
					ID: scope.OrgUUID + "-" + childScope.AgentName, AgentName: childScope.AgentName,
					ParentRunID: "fan-out-parent", Phase: RunPhasePending, Trigger: "delegation", CreatedAt: now, UpdatedAt: now,
				})
			}()
		}
		close(start)
		admitted := 0
		for range callers {
			switch err := <-results; {
			case err == nil:
				admitted++
			case !errors.Is(err, ErrDelegationLimit):
				t.Errorf("unexpected child admission error: %v", err)
			}
		}
		if admitted != MaxDelegatedRuns {
			t.Fatalf("admitted %d children, want %d", admitted, MaxDelegatedRuns)
		}
	})

	t.Run("fresh claim and stale admission settlement race atomically", func(t *testing.T) {
		seed := leaseTestRun(scope, "fresh-claim-versus-expiry", now)
		seed.Phase = RunPhasePending
		seed.CreatedAt = now.Add(-5 * time.Minute)
		seed.UpdatedAt = seed.CreatedAt
		if err := st.SaveRun(ctx, scope, seed); err != nil {
			t.Fatal(err)
		}

		start := make(chan struct{})
		var wg sync.WaitGroup
		var claimErr error
		var settled Run
		var settledOK bool
		var settleErr error
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			_, claimErr = st.ClaimFreshRun(ctx, scope, seed.ID, seed.UpdatedAt, newOwner, now)
		}()
		go func() {
			defer wg.Done()
			<-start
			settled, settledOK, settleErr = st.FailPendingRun(ctx, scope, seed.ID, seed.UpdatedAt, now, now, "admission owner disappeared")
		}()
		close(start)
		wg.Wait()
		if claimErr != nil && !errors.Is(claimErr, ErrRunClaimConflict) {
			t.Fatalf("claiming fresh run: %v", claimErr)
		}
		if settleErr != nil {
			t.Fatalf("settling stale admission: %v", settleErr)
		}
		if (claimErr == nil) == settledOK {
			t.Fatalf("claim and settlement winners are not exclusive: claimErr=%v settled=%t", claimErr, settledOK)
		}

		got, err := st.GetRun(ctx, scope, seed.ID)
		if err != nil {
			t.Fatal(err)
		}
		if claimErr == nil {
			if settledOK || got.Phase != RunPhaseRunning || got.ExecutionOwner != newOwner || got.ExecutionLeaseAt.IsZero() {
				t.Fatalf("fresh claim winner left phase=%s owner=%q lease=%v settled=%t", got.Phase, got.ExecutionOwner, got.ExecutionLeaseAt, settledOK)
			}
			if _, ok, err := st.FailPendingRun(ctx, scope, seed.ID, seed.UpdatedAt, now.Add(time.Hour), now.Add(time.Hour), "late failure"); err != nil || ok {
				t.Fatalf("late settlement changed claimed run: settled=%t err=%v", ok, err)
			}
		} else {
			if !settledOK || settled.Phase != RunPhaseFailed || got.Phase != RunPhaseFailed || got.FinishedAt == nil || got.ExecutionOwner != "" || !got.ExecutionLeaseAt.IsZero() {
				t.Fatalf("stale admission winner left phase=%s owner=%q lease=%v finished=%v settled=%t", got.Phase, got.ExecutionOwner, got.ExecutionLeaseAt, got.FinishedAt, settledOK)
			}
			if _, err := st.ClaimFreshRun(ctx, scope, seed.ID, seed.UpdatedAt, newOwner, now.Add(time.Hour)); !errors.Is(err, ErrRunClaimConflict) {
				t.Fatalf("settled admission was revived: %v", err)
			}
		}
	})

	t.Run("old pending admission settles once and cannot be revived", func(t *testing.T) {
		seed := leaseTestRun(scope, "stale-pending", now)
		seed.Phase = RunPhasePending
		seed.CreatedAt = now.Add(-5 * time.Minute)
		seed.UpdatedAt = seed.CreatedAt
		if err := st.SaveRun(ctx, scope, seed); err != nil {
			t.Fatal(err)
		}
		settled, ok, err := st.FailPendingRun(ctx, scope, seed.ID, seed.UpdatedAt, now, now.Add(time.Second), "admission owner disappeared")
		if err != nil || !ok {
			t.Fatalf("settling old admission: settled=%t err=%v", ok, err)
		}
		if settled.Phase != RunPhaseFailed || settled.FinishedAt == nil || settled.Message != "admission owner disappeared" {
			t.Fatalf("settled admission = phase %s finished=%v message=%q", settled.Phase, settled.FinishedAt, settled.Message)
		}
		if _, ok, err := st.FailPendingRun(ctx, scope, seed.ID, seed.UpdatedAt, now, now.Add(2*time.Second), "duplicate failure"); err != nil || ok {
			t.Fatalf("stale settlement repeated: settled=%t err=%v", ok, err)
		}
		if _, err := st.ClaimFreshRun(ctx, scope, seed.ID, seed.UpdatedAt, newOwner, now.Add(time.Hour)); !errors.Is(err, ErrRunClaimConflict) {
			t.Fatalf("failed admission was revived: %v", err)
		}
	})

	t.Run("backward-clock approval claim advances its revision", func(t *testing.T) {
		storedAt := now.Add(time.Hour)
		seed := leaseTestRun(scope, "approval-clock-skew", now)
		seed.Phase = RunPhasePendingApproval
		seed.UpdatedAt = storedAt
		seed.Checkpoint = []byte(`{"inboxID":"approval-item"}`)
		if err := st.SaveRun(ctx, scope, seed); err != nil {
			t.Fatal(err)
		}

		claimed, err := st.ClaimRun(ctx, scope, seed.ID, oldOwner, now, "approval-item")
		if err != nil {
			t.Fatal(err)
		}
		if claimed.Phase != RunPhaseRunning || !claimed.UpdatedAt.After(storedAt) {
			t.Fatalf("backward-clock approval claim = phase %s revision %v; want Running after %v", claimed.Phase, claimed.UpdatedAt, storedAt)
		}
		if claimed.StartedAt == nil || !claimed.StartedAt.Equal(claimed.UpdatedAt) {
			t.Fatalf("approval start time = %v; want monotonic claim revision %v", claimed.StartedAt, claimed.UpdatedAt)
		}
		if claimed.ExecutionOwner != oldOwner || !claimed.ExecutionLeaseAt.Equal(claimed.UpdatedAt) {
			t.Fatalf("approval claim did not take a lease: owner=%q lease=%v", claimed.ExecutionOwner, claimed.ExecutionLeaseAt)
		}
		if _, err := st.ClaimRecovery(ctx, scope, seed.ID, claimed.UpdatedAt, newOwner, claimed.UpdatedAt.Add(time.Second)); !errors.Is(err, ErrRecoveryClaimStale) {
			t.Fatalf("recovery stole the approval claim before dispatch: %v", err)
		}
		if _, err := st.AcquireRunLease(ctx, scope, seed.ID, oldOwner, claimed.Attempt, claimed.UpdatedAt.Add(time.Second)); err != nil {
			t.Fatalf("approval owner could not start dispatch: %v", err)
		}
		if _, err := st.ClaimRun(ctx, scope, seed.ID, "", now.Add(time.Second), "approval-item"); !errors.Is(err, ErrRunClaimConflict) {
			t.Fatalf("approval was claimed twice: %v", err)
		}
	})

	t.Run("fresh lease cannot be stolen and renewal does not change checkpoint revision", func(t *testing.T) {
		seed := leaseTestRun(scope, "fresh-lease", now)
		if err := st.SaveRun(ctx, scope, seed); err != nil {
			t.Fatal(err)
		}
		leased, err := st.AcquireRunLease(ctx, scope, seed.ID, oldOwner, seed.Attempt, now)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := st.ClaimRecovery(ctx, scope, seed.ID, leased.UpdatedAt, newOwner, now.Add(time.Minute)); !errors.Is(err, ErrRecoveryClaimStale) {
			t.Fatalf("fresh lease was stolen: %v", err)
		}

		renewed, ok, err := st.RenewRunLease(ctx, scope, seed.ID, oldOwner, seed.Attempt, now.Add(time.Minute))
		if err != nil || !ok {
			t.Fatalf("renewing fresh lease: ok=%t err=%v", ok, err)
		}
		if !renewed.UpdatedAt.Equal(leased.UpdatedAt) {
			t.Fatalf("lease heartbeat changed checkpoint revision: got %v, want %v", renewed.UpdatedAt, leased.UpdatedAt)
		}
		if renewed.ExecutionOwner != oldOwner || !renewed.ExecutionLeaseAt.Equal(now.Add(time.Minute)) {
			t.Fatalf("renewed lease = owner %q at %v", renewed.ExecutionOwner, renewed.ExecutionLeaseAt)
		}
	})

	t.Run("expired lease transfers once and fences the old executor", func(t *testing.T) {
		seed := leaseTestRun(scope, "expired-lease", now)
		if err := st.SaveRun(ctx, scope, seed); err != nil {
			t.Fatal(err)
		}
		leased, err := st.AcquireRunLease(ctx, scope, seed.ID, oldOwner, seed.Attempt, now)
		if err != nil {
			t.Fatal(err)
		}
		recoveryAt := now.Add(RunLeaseDuration + time.Second)
		claimed, err := st.ClaimRecovery(ctx, scope, seed.ID, leased.UpdatedAt, newOwner, recoveryAt)
		if err != nil {
			t.Fatalf("claim expired lease: %v", err)
		}
		if claimed.Attempt != seed.Attempt+1 || claimed.ExecutionOwner != newOwner {
			t.Fatalf("recovery claim = attempt %d owner %q; want attempt %d owner %q", claimed.Attempt, claimed.ExecutionOwner, seed.Attempt+1, newOwner)
		}

		if _, ok, err := st.RenewRunLease(ctx, scope, seed.ID, oldOwner, seed.Attempt, recoveryAt.Add(time.Second)); err != nil || ok {
			t.Fatalf("old owner renewed after transfer: ok=%t err=%v", ok, err)
		}
		staleTerminal := leased
		staleTerminal.Phase = RunPhaseFailed
		staleTerminal.Message = "stale executor"
		staleTerminal.UpdatedAt = recoveryAt.Add(time.Second)
		if ok, err := st.SaveRunOwned(ctx, scope, staleTerminal, oldOwner, seed.Attempt, recoveryAt.Add(time.Second)); err != nil || ok {
			t.Fatalf("old owner persisted a terminal result after transfer: ok=%t err=%v", ok, err)
		}

		current := claimed
		current.Phase = RunPhaseSucceeded
		current.UpdatedAt = recoveryAt.Add(2 * time.Second)
		if ok, err := st.SaveRunOwned(ctx, scope, current, newOwner, claimed.Attempt, recoveryAt.Add(2*time.Second)); err != nil || !ok {
			t.Fatalf("new owner could not persist terminal result: ok=%t err=%v", ok, err)
		}
		got, err := st.GetRun(ctx, scope, seed.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Phase != RunPhaseSucceeded || got.Attempt != claimed.Attempt || got.ExecutionOwner != newOwner {
			t.Fatalf("final run = phase %q attempt %d owner %q", got.Phase, got.Attempt, got.ExecutionOwner)
		}
	})

	t.Run("stale SaveRun preserves recovery authority", func(t *testing.T) {
		seed := leaseTestRun(scope, "stale-save", now)
		seed.Checkpoint = []byte(`{"iteration":0}`)
		if err := st.SaveRun(ctx, scope, seed); err != nil {
			t.Fatal(err)
		}
		leased, err := st.AcquireRunLease(ctx, scope, seed.ID, oldOwner, seed.Attempt, now)
		if err != nil {
			t.Fatal(err)
		}
		recoveryAt := now.Add(RunLeaseDuration + time.Second)
		claimed, err := st.ClaimRecovery(ctx, scope, seed.ID, leased.UpdatedAt, newOwner, recoveryAt)
		if err != nil {
			t.Fatal(err)
		}
		staleCheckpoint := []byte(`{"iteration":999}`)
		if updated, err := st.UpdateRunCheckpoint(ctx, scope, seed.ID, oldOwner, claimed.Attempt, claimed.UpdatedAt,
			staleCheckpoint, nil, recoveryAt.Add(time.Second)); err != nil || updated {
			t.Fatalf("old owner updated a checkpoint after recovery: updated=%t err=%v", updated, err)
		}
		if updated, err := st.UpdateRunCheckpoint(ctx, scope, seed.ID, newOwner, seed.Attempt, claimed.UpdatedAt,
			staleCheckpoint, nil, recoveryAt.Add(time.Second)); err != nil || updated {
			t.Fatalf("stale attempt updated a checkpoint after recovery: updated=%t err=%v", updated, err)
		}

		stale := seed
		stale.RecoveryIdentity = RecoveryIdentityCaller
		stale.AgentUID = "replacement-agent-uid"
		stale.Message = "stale generic write"
		stale.UpdatedAt = seed.UpdatedAt
		if err := st.SaveRun(ctx, scope, stale); !errors.Is(err, ErrRunLeaseLost) {
			t.Fatalf("generic SaveRun under a live lease error = %v, want ErrRunLeaseLost", err)
		}
		got, err := st.GetRun(ctx, scope, seed.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Phase != claimed.Phase || got.Message != claimed.Message {
			t.Errorf("stale SaveRun changed run state to phase %q message %q", got.Phase, got.Message)
		}
		var checkpoint struct {
			Iteration int `json:"iteration"`
		}
		if err := json.Unmarshal(got.Checkpoint, &checkpoint); err != nil || checkpoint.Iteration != 0 {
			t.Errorf("stale owner or attempt changed checkpoint to %s (decode err=%v)", got.Checkpoint, err)
		}
		if got.Attempt != claimed.Attempt {
			t.Errorf("stale SaveRun rolled recovery attempt back: got %d, want %d", got.Attempt, claimed.Attempt)
		}
		if got.RecoveryIdentity != RecoveryIdentityAgent {
			t.Errorf("stale SaveRun changed recovery identity to %q", got.RecoveryIdentity)
		}
		if got.AgentUID != seed.AgentUID {
			t.Errorf("stale SaveRun changed Agent UID to %q, want %q", got.AgentUID, seed.AgentUID)
		}
		if got.ExecutionOwner != newOwner || !got.ExecutionLeaseAt.Equal(claimed.ExecutionLeaseAt) {
			t.Errorf("stale SaveRun changed lease to owner %q at %v", got.ExecutionOwner, got.ExecutionLeaseAt)
		}
		if !got.UpdatedAt.Equal(claimed.UpdatedAt) {
			t.Errorf("stale SaveRun changed checkpoint revision: got %v, want %v", got.UpdatedAt, claimed.UpdatedAt)
		}
	})

	t.Run("approval claim releases parked lease for a new owner", func(t *testing.T) {
		seed := leaseTestRun(scope, "approval-resume", now)
		if err := st.SaveRun(ctx, scope, seed); err != nil {
			t.Fatal(err)
		}
		leased, err := st.AcquireRunLease(ctx, scope, seed.ID, oldOwner, seed.Attempt, now)
		if err != nil {
			t.Fatal(err)
		}
		parked := leased
		parked.Phase = RunPhasePendingApproval
		parked.Checkpoint = []byte(`{"inboxID":"approval-item"}`)
		parked.UpdatedAt = now.Add(time.Second)
		if ok, err := st.SaveRunOwned(ctx, scope, parked, oldOwner, seed.Attempt, parked.UpdatedAt); err != nil || !ok {
			t.Fatalf("park run under current lease: ok=%t err=%v", ok, err)
		}

		claimed, err := st.ClaimRun(ctx, scope, seed.ID, "", now.Add(2*time.Second), "approval-item")
		if err != nil {
			t.Fatal(err)
		}
		resumed, err := st.AcquireRunLease(ctx, scope, seed.ID, newOwner, claimed.Attempt, now.Add(2*time.Second))
		if err != nil {
			t.Fatalf("new owner could not acquire resumed approval: %v", err)
		}
		if resumed.ExecutionOwner != newOwner || resumed.ExecutionLeaseAt.Before(now.Add(2*time.Second)) {
			t.Fatalf("resumed lease = owner %q at %v", resumed.ExecutionOwner, resumed.ExecutionLeaseAt)
		}
		if _, ok, err := st.RenewRunLease(ctx, scope, seed.ID, oldOwner, seed.Attempt, now.Add(3*time.Second)); err != nil || ok {
			t.Fatalf("parked owner renewed after approval resumed: ok=%t err=%v", ok, err)
		}
	})

	t.Run("owned usage bills monotonic cumulative deltas once", func(t *testing.T) {
		seed := leaseTestRun(scope, "owned-usage", now)
		seed.InputTokens, seed.OutputTokens, seed.USDMicros = 10, 20, 30
		seed.Checkpoint = []byte(`{"position":1}`)
		if err := st.SaveRun(ctx, scope, seed); err != nil {
			t.Fatal(err)
		}
		leased, err := st.AcquireRunLease(ctx, scope, seed.ID, oldOwner, seed.Attempt, now)
		if err != nil {
			t.Fatal(err)
		}
		firstTotal := backend.Cost{Tokens: backend.Tokens{InputTokens: 100, OutputTokens: 80}, CostMicros: 150}
		writeAt := now.Add(time.Second)
		start := make(chan struct{})
		type writeResult struct {
			usage Usage
			ok    bool
			err   error
		}
		results := make(chan writeResult, 2)
		for range 2 {
			go func() {
				<-start
				u, ok, err := st.RecordRunUsageOwned(ctx, scope, seed.ID, oldOwner, seed.Attempt, firstTotal, writeAt, 24*time.Hour)
				results <- writeResult{usage: u, ok: ok, err: err}
			}()
		}
		close(start)
		for range 2 {
			result := <-results
			if result.err != nil || !result.ok {
				t.Fatalf("record concurrent owned usage: ok=%t err=%v", result.ok, result.err)
			}
		}
		usage, err := st.GetUsage(ctx, scope, scope.AgentName, writeAt, 24*time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		if usage.InputTokens != 90 || usage.OutputTokens != 60 || usage.USDMicros != 120 {
			t.Fatalf("concurrent cumulative usage billed %+v, want 90/60/120", usage)
		}
		stored, err := st.GetRun(ctx, scope, seed.ID)
		if err != nil {
			t.Fatal(err)
		}
		if stored.InputTokens != 100 || stored.OutputTokens != 80 || stored.USDMicros != 150 {
			t.Fatalf("stored run usage = %d/%d/%d, want 100/80/150", stored.InputTokens, stored.OutputTokens, stored.USDMicros)
		}
		if !stored.UpdatedAt.Equal(leased.UpdatedAt) {
			t.Fatalf("usage write changed checkpoint revision: updatedAt=%v want=%v", stored.UpdatedAt, leased.UpdatedAt)
		}
		var checkpoint struct {
			Position int `json:"position"`
		}
		if err := json.Unmarshal(stored.Checkpoint, &checkpoint); err != nil || checkpoint.Position != 1 {
			t.Fatalf("usage write changed checkpoint: value=%s err=%v", stored.Checkpoint, err)
		}

		// Incoming totals can be inconsistent across a resumed backend. Never
		// reduce the run's recorded totals or credit the rolling usage window.
		lowerTotal := backend.Cost{Tokens: backend.Tokens{InputTokens: 90, OutputTokens: 120}, CostMicros: 130}
		usage, ok, err := st.RecordRunUsageOwned(ctx, scope, seed.ID, oldOwner, seed.Attempt, lowerTotal, writeAt.Add(time.Second), 24*time.Hour)
		if err != nil || !ok {
			t.Fatalf("record partially increasing usage: ok=%t err=%v", ok, err)
		}
		if usage.InputTokens != 90 || usage.OutputTokens != 100 || usage.USDMicros != 120 {
			t.Fatalf("partially increasing totals billed %+v, want 90/100/120", usage)
		}
		stored, err = st.GetRun(ctx, scope, seed.ID)
		if err != nil {
			t.Fatal(err)
		}
		if stored.InputTokens != 100 || stored.OutputTokens != 120 || stored.USDMicros != 150 || !stored.UpdatedAt.Equal(leased.UpdatedAt) {
			t.Fatalf("monotonic run totals = %d/%d/%d at %v", stored.InputTokens, stored.OutputTokens, stored.USDMicros, stored.UpdatedAt)
		}
		staleOwned := stored
		staleOwned.InputTokens, staleOwned.OutputTokens, staleOwned.USDMicros = 1, 2, 3
		if ok, err := st.SaveRunOwned(ctx, scope, staleOwned, oldOwner, seed.Attempt, stored.UpdatedAt); err != nil || !ok {
			t.Fatalf("save stale lower run totals under current owner: ok=%t err=%v", ok, err)
		}
		stored, err = st.GetRun(ctx, scope, seed.ID)
		if err != nil {
			t.Fatal(err)
		}
		if stored.InputTokens != 100 || stored.OutputTokens != 120 || stored.USDMicros != 150 {
			t.Fatalf("owned SaveRun lowered usage totals to %d/%d/%d", stored.InputTokens, stored.OutputTokens, stored.USDMicros)
		}

		recoveryAt := leased.ExecutionLeaseAt.Add(RunLeaseDuration + time.Second)
		claimed, err := st.ClaimRecovery(ctx, scope, seed.ID, stored.UpdatedAt, newOwner, recoveryAt)
		if err != nil {
			t.Fatal(err)
		}
		staleTotal := backend.Cost{Tokens: backend.Tokens{InputTokens: 500, OutputTokens: 500}, CostMicros: 500}
		if _, ok, err := st.RecordRunUsageOwned(ctx, scope, seed.ID, oldOwner, claimed.Attempt, staleTotal, recoveryAt.Add(time.Second), 24*time.Hour); err != nil || ok {
			t.Fatalf("stale owner recorded usage after transfer: ok=%t err=%v", ok, err)
		}
		if _, ok, err := st.RecordRunUsageOwned(ctx, scope, seed.ID, newOwner, seed.Attempt, staleTotal, recoveryAt.Add(time.Second), 24*time.Hour); err != nil || ok {
			t.Fatalf("stale attempt recorded usage after transfer: ok=%t err=%v", ok, err)
		}
		usage, ok, err = st.RecordRunUsageOwned(ctx, scope, seed.ID, newOwner, claimed.Attempt,
			backend.Cost{Tokens: backend.Tokens{InputTokens: 125, OutputTokens: 130}, CostMicros: 180}, recoveryAt.Add(time.Second), 24*time.Hour)
		if err != nil || !ok {
			t.Fatalf("recovery owner could not record usage: ok=%t err=%v", ok, err)
		}
		if usage.InputTokens != 115 || usage.OutputTokens != 110 || usage.USDMicros != 150 {
			t.Fatalf("post-recovery usage = %+v, want 115/110/150", usage)
		}
	})

	t.Run("owned usage accepts cancellation but rejects expired leases", func(t *testing.T) {
		guardScope := scope
		guardScope.AgentName += "-usage-guard"
		seed := leaseTestRun(guardScope, "owned-usage-cancel", now)
		if err := st.SaveRun(ctx, guardScope, seed); err != nil {
			t.Fatal(err)
		}
		_, err := st.AcquireRunLease(ctx, guardScope, seed.ID, oldOwner, seed.Attempt, now)
		if err != nil {
			t.Fatal(err)
		}
		if err := st.RequestCancel(ctx, guardScope, seed.ID, now.Add(time.Second), "operator"); err != nil {
			t.Fatal(err)
		}
		usage, ok, err := st.RecordRunUsageOwned(ctx, guardScope, seed.ID, oldOwner, seed.Attempt,
			backend.Cost{Tokens: backend.Tokens{InputTokens: 10}}, now.Add(2*time.Second), 24*time.Hour)
		if err != nil || !ok {
			t.Fatalf("current owner could not record completed usage after cancellation: ok=%t err=%v", ok, err)
		}
		if usage.InputTokens != 10 || usage.OutputTokens != 0 || usage.USDMicros != 0 {
			t.Fatalf("canceled run usage = %+v, want recorded input tokens", usage)
		}
		canceled, err := st.GetRun(ctx, guardScope, seed.ID)
		if err != nil {
			t.Fatal(err)
		}
		canceled.Phase = RunPhaseAborted
		finished := now.Add(3 * time.Second)
		canceled.FinishedAt = &finished
		canceled.UpdatedAt = finished
		// Another replica settles the cancellation before the executor receives
		// its final token count. The executor can report that count repeatedly.
		if err := st.SaveRun(ctx, guardScope, canceled); err != nil {
			t.Fatalf("remote replica could not settle canceled run: %v", err)
		}
		finalUsage := backend.Cost{Tokens: backend.Tokens{InputTokens: 15, OutputTokens: 2}, CostMicros: 3}
		if _, ok, err := st.RecordRunUsageOwned(ctx, guardScope, seed.ID, newOwner, seed.Attempt, finalUsage, finished, 24*time.Hour); err != nil || ok {
			t.Fatalf("foreign owner could bill a canceled run: ok=%t err=%v", ok, err)
		}
		for range 2 {
			usage, ok, err := st.RecordRunUsageOwned(ctx, guardScope, seed.ID, oldOwner, seed.Attempt, finalUsage, finished, 24*time.Hour)
			if err != nil || !ok || usage.InputTokens != 15 || usage.OutputTokens != 2 || usage.USDMicros != 3 {
				t.Fatalf("final canceled usage = %+v, ok=%t err=%v", usage, ok, err)
			}
		}
		settled, err := st.GetRun(ctx, guardScope, seed.ID)
		if err != nil {
			t.Fatal(err)
		}
		if settled.Phase != RunPhaseAborted || settled.InputTokens != 15 {
			t.Fatalf("canceled run settled as phase %q with %d input tokens", settled.Phase, settled.InputTokens)
		}

		expired := leaseTestRun(guardScope, "owned-usage-expired", now)
		if err := st.SaveRun(ctx, guardScope, expired); err != nil {
			t.Fatal(err)
		}
		expiredLease, err := st.AcquireRunLease(ctx, guardScope, expired.ID, oldOwner, expired.Attempt, now)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok, err := st.RecordRunUsageOwned(ctx, guardScope, expired.ID, oldOwner, expired.Attempt,
			backend.Cost{Tokens: backend.Tokens{InputTokens: 10}}, expiredLease.ExecutionLeaseAt.Add(RunLeaseDuration), 24*time.Hour); err != nil || ok {
			t.Fatalf("expired lease recorded usage: ok=%t err=%v", ok, err)
		}
		usage, err = st.GetUsage(ctx, guardScope, guardScope.AgentName, now.Add(2*time.Second), 24*time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		if usage.InputTokens != 15 || usage.OutputTokens != 2 || usage.USDMicros != 3 {
			t.Fatalf("expired owner changed usage: %+v", usage)
		}
	})
}

func leaseTestRun(scope Scope, id string, now time.Time) Run {
	return Run{
		ID: id, AgentName: scope.AgentName, Phase: RunPhaseRunning, Attempt: 0,
		RecoveryIdentity: RecoveryIdentityAgent, AgentUID: "agent-uid-original",
		CreatedAt: now, UpdatedAt: now,
	}
}
