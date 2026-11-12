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
	"strings"
	"testing"
	"time"

	agentsv1alpha1 "github.com/railgrid/provider-agents/apis/v1alpha1"
	"github.com/railgrid/provider-agents/store"
)

// flakySaveStore fails the first SaveRunOwned calls. commitFirst makes the
// first failure land anyway, as a commit whose acknowledgement was lost.
type flakySaveStore struct {
	store.Store
	failures    int
	commitFirst bool
	calls       int
}

func (s *flakySaveStore) SaveRunOwned(ctx context.Context, scope store.Scope, run store.Run, owner string, attempt int, now time.Time) (bool, error) {
	s.calls++
	if s.calls > s.failures {
		return s.Store.SaveRunOwned(ctx, scope, run, owner, attempt, now)
	}
	if s.calls == 1 && s.commitFirst {
		_, _ = s.Store.SaveRunOwned(ctx, scope, run, owner, attempt, now)
	}
	return false, errors.New("connection reset by peer")
}

func leasedTestRun(t *testing.T, f *recoverFixture, id string) store.Run {
	t.Helper()
	ctx := context.Background()
	at := time.Now().UTC()
	run := store.Run{
		ID: id, AgentName: f.scope.AgentName, SessionID: "chat", Trigger: agentsv1alpha1.RunTriggerChat,
		Phase: store.RunPhaseRunning, Input: "do the thing", CreatedAt: at, UpdatedAt: at, StartedAt: &at,
		RecoveryIdentity: store.RecoveryIdentityAgent, AgentUID: "agent-uid",
	}
	if err := f.s.store.SaveRun(ctx, f.scope, run); err != nil {
		t.Fatal(err)
	}
	leased, err := f.s.store.AcquireRunLease(ctx, f.scope, id, "process/one", run.Attempt, at)
	if err != nil {
		t.Fatal(err)
	}
	return leased
}

func TestTerminalWriteRetriesTransientStoreFailures(t *testing.T) {
	retries := finishRunOwnedRetries
	finishRunOwnedRetries = []time.Duration{time.Millisecond, time.Millisecond}
	t.Cleanup(func() { finishRunOwnedRetries = retries })
	for name, tc := range map[string]struct {
		failures    int
		commitFirst bool
		wantErr     bool
	}{
		"one transient failure":        {failures: 1},
		"failure whose commit landed":  {failures: 1, commitFirst: true},
		"store down for every attempt": {failures: 10, wantErr: true},
	} {
		t.Run(name, func(t *testing.T) {
			f := newRecoverFixture(t)
			run := leasedTestRun(t, f, "terminal-"+strings.ReplaceAll(name, " ", "-"))
			flaky := &flakySaveStore{Store: f.s.store, failures: tc.failures, commitFirst: tc.commitFirst}
			f.s.store = flaky
			err := f.s.finishRunOwnedRetrying(context.Background(), f.scope, run.ID,
				runOutcome{Phase: store.RunPhaseFailed, Message: "model refused"}, time.Now().UTC(), run.ExecutionOwner, run.Attempt)
			got := f.phaseOf(t, run.ID)
			if tc.wantErr {
				if err == nil || errors.Is(err, store.ErrRunLeaseLost) || got.Phase != store.RunPhaseRunning {
					t.Fatalf("persistent failure: err=%v phase=%s; want the store error and Running", err, got.Phase)
				}
				return
			}
			if err != nil || got.Phase != store.RunPhaseFailed || got.Message != "model refused" {
				t.Fatalf("err=%v phase=%s message=%q", err, got.Phase, got.Message)
			}
		})
	}
}

func TestTerminalWriteDoesNotRetryALostLease(t *testing.T) {
	f := newRecoverFixture(t)
	run := leasedTestRun(t, f, "lost-lease")
	err := f.s.finishRunOwnedRetrying(context.Background(), f.scope, run.ID,
		runOutcome{Phase: store.RunPhaseFailed, Message: "x"}, time.Now().UTC(), "process/other", run.Attempt)
	if !errors.Is(err, store.ErrRunLeaseLost) {
		t.Fatalf("got %v, want ErrRunLeaseLost", err)
	}
}

func TestUnclaimedAdmissionIsSettledNotOrphaned(t *testing.T) {
	ctx := context.Background()
	f := newRecoverFixture(t)
	at := time.Now().UTC()
	pending := store.Run{
		ID: "unclaimed", AgentName: f.scope.AgentName, SessionID: "api", Trigger: agentsv1alpha1.RunTriggerAPI,
		Phase: store.RunPhasePending, Input: "x", IdempotencyKey: "key-1", AgentUID: "agent-uid",
		RecoveryIdentity: store.RecoveryIdentityCaller, CreatedAt: at, UpdatedAt: at,
	}
	if err := f.s.store.SaveRun(ctx, f.scope, pending); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.settleUnclaimedAdmission(ctx, f.scope, pending.ID, errors.New("claim transaction timed out")); err == nil {
		t.Fatal("an unclaimed admission reported success")
	}
	got := f.phaseOf(t, pending.ID)
	if got.Phase != store.RunPhaseFailed || !strings.Contains(got.Message, "claim transaction timed out") {
		t.Fatalf("phase=%s message=%q; want Failed with the real cause", got.Phase, got.Message)
	}

	// Claimed by another executor in the meantime: genuinely admitted.
	claimed := pending
	claimed.ID, claimed.IdempotencyKey = "claimed-elsewhere", "key-2"
	if err := f.s.store.SaveRun(ctx, f.scope, claimed); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.store.ClaimFreshRun(ctx, f.scope, claimed.ID, claimed.UpdatedAt, "process/other", claimed.AgentUID, at.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	admission, err := f.s.settleUnclaimedAdmission(ctx, f.scope, claimed.ID, errors.New("lost the claim race"))
	if err != nil || admission.ID != claimed.ID || admission.Phase != store.RunPhaseRunning {
		t.Fatalf("claimed elsewhere: admission=%+v err=%v", admission, err)
	}
}
