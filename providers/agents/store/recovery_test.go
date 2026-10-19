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
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestMemoryClaimRecovery(t *testing.T) {
	testClaimRecovery(t, NewMemoryStore(), Scope{OrgUUID: "recovery-org", WorkspaceUUID: "recovery-ws", AgentName: "agent"})
}
func TestPostgresClaimRecovery(t *testing.T) {
	st := openTestPostgres(t)
	testClaimRecovery(t, st, pgScope(t, st))
}
func testClaimRecovery(t *testing.T, st Store, scope Scope) {
	t.Helper()
	ctx := context.Background()
	at := time.Now().UTC().Truncate(time.Microsecond).Add(-time.Minute)
	seed := Run{ID: "recover", AgentName: scope.AgentName, Phase: RunPhaseRunning, CreatedAt: at, UpdatedAt: at, Checkpoint: []byte(`{"cursor":42}`)}
	if err := st.SaveRun(ctx, scope, seed); err != nil {
		t.Fatal(err)
	}
	var winners atomic.Int32
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if got, err := st.ClaimRecovery(ctx, scope, seed.ID, at, at.Add(time.Second)); err == nil {
				winners.Add(1)
				var checkpoint struct{ Cursor int }
				_ = json.Unmarshal(got.Checkpoint, &checkpoint)
				if got.Attempt != 1 || checkpoint.Cursor != 42 {
					t.Errorf("claim lost recovery state: attempt=%d checkpoint=%s", got.Attempt, got.Checkpoint)
				}
			} else if !errors.Is(err, ErrRecoveryClaimStale) {
				t.Errorf("unexpected claim error: %v", err)
			}
		})
	}
	wg.Wait()
	if winners.Load() != 1 {
		t.Fatalf("recovery winners=%d, want one", winners.Load())
	}
	for _, phase := range []RunPhase{RunPhasePending, RunPhasePendingApproval, RunPhaseSucceeded, RunPhaseFailed, RunPhaseAborted} {
		r := seed
		r.ID = string(phase)
		r.Phase = phase
		if err := st.SaveRun(ctx, scope, r); err != nil {
			t.Fatal(err)
		}
		if _, err := st.ClaimRecovery(ctx, scope, r.ID, at, at.Add(time.Second)); err == nil {
			t.Errorf("recovered %s run", phase)
		}
	}
	seed.ID = "cancelled"
	if err := st.SaveRun(ctx, scope, seed); err != nil {
		t.Fatal(err)
	}
	if err := st.RequestCancel(ctx, scope, seed.ID, at.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	cancelled, err := st.GetRun(ctx, scope, seed.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.ClaimRecovery(ctx, scope, seed.ID, cancelled.UpdatedAt, at.Add(2*time.Second)); !errors.Is(err, ErrRecoveryClaimStale) {
		t.Fatal("recovered cancelled run")
	}
}

func TestMemoryClaimRecoveryClockSkew(t *testing.T) {
	testClaimRecoveryClockSkew(t, NewMemoryStore(), Scope{OrgUUID: "skew-org", WorkspaceUUID: "skew-ws", AgentName: "agent"})
}
func TestPostgresClaimRecoveryClockSkew(t *testing.T) {
	st := openTestPostgres(t)
	testClaimRecoveryClockSkew(t, st, pgScope(t, st))
}
func testClaimRecoveryClockSkew(t *testing.T, st Store, scope Scope) {
	t.Helper()
	ctx := context.Background()
	observed := time.Now().UTC().Truncate(time.Microsecond)
	for _, offset := range []time.Duration{-time.Hour, 0, time.Hour} {
		t.Run(offset.String(), func(t *testing.T) {
			seed := Run{ID: offset.String(), AgentName: scope.AgentName, Phase: RunPhaseRunning, CreatedAt: observed, UpdatedAt: observed}
			if err := st.SaveRun(ctx, scope, seed); err != nil {
				t.Fatal(err)
			}
			now := observed.Add(offset)
			got, err := st.ClaimRecovery(ctx, scope, seed.ID, observed, now)
			if err != nil {
				t.Fatal(err)
			}
			want := now
			if offset <= 0 {
				want = observed.Add(time.Microsecond)
			}
			if !got.UpdatedAt.Equal(want) || got.Attempt != 1 {
				t.Fatalf("claim revision=%v attempt=%d; want %v and 1", got.UpdatedAt, got.Attempt, want)
			}
			if _, err := st.ClaimRecovery(ctx, scope, seed.ID, observed, now); !errors.Is(err, ErrRecoveryClaimStale) {
				t.Fatalf("duplicate claim: %v", err)
			}
		})
	}
}
