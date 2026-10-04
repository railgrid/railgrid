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
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	agentsv1alpha1 "github.com/railgrid/provider-agents/apis/v1alpha1"
	"github.com/railgrid/provider-agents/backend"
	backendharness "github.com/railgrid/provider-agents/backend/harness"
	"github.com/railgrid/provider-agents/store"
)

func TestHarnessApprovalsKeepTheAttemptEpoch(t *testing.T) {
	ctx := context.Background()
	s := &Server{store: store.NewMemoryStore()}
	scope := store.Scope{OrgUUID: "org", WorkspaceUUID: "workspace", AgentName: "coder"}
	first, err := s.harnessSessionFor(ctx, scope, "chat", "backend-a", nil)
	if err != nil || first.Turns != 1 {
		t.Fatalf("first turn = %+v, err = %v", first, err)
	}
	raw, err := json.Marshal(backendharness.State{AttemptID: "attempt", Epoch: 1, SessionID: "thread"})
	if err != nil {
		t.Fatal(err)
	}
	cont := &continuation{Checkpoint: runCheckpoint{Backend: agentsv1alpha1.AgentBackendHarness, Harness: raw}}
	for range 2 {
		resumed, err := s.harnessSessionFor(ctx, scope, "chat", "backend-a", cont)
		if err != nil || resumed.Turns != 1 || resumed.HarnessSessionID != "thread" {
			t.Fatalf("approval resume = %+v, err = %v; want the original attempt", resumed, err)
		}
		if err := s.persistHarnessSession(ctx, scope, resumed, backendharness.Observed{SessionID: "thread"}, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	next, err := s.harnessSessionFor(ctx, scope, "chat", "backend-a", nil)
	if err != nil || next.Turns != 2 || next.HarnessSessionID != "thread" {
		t.Fatalf("next fresh turn = %+v, err = %v; approvals must not allocate epochs", next, err)
	}
}

func TestHarnessSessionChainsAcrossTurnsAndCancellation(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemoryStore()
	s := &Server{store: st}
	scope := store.Scope{OrgUUID: "org", WorkspaceUUID: "workspace", AgentName: "coder"}
	first, err := st.NextHarnessTurn(ctx, scope, "chat", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	first.BackendKey = "backend-a"
	if err := s.persistHarnessSession(ctx, scope, first, backendharness.Observed{SessionID: "codex-thread"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	second, err := st.NextHarnessTurn(ctx, scope, "chat", time.Now())
	if err != nil || second.Turns != 2 || second.HarnessSessionID != "codex-thread" {
		t.Fatalf("next turn = %+v, err = %v; want epoch 2 resuming codex-thread", second, err)
	}

	// A cancelled turn may still have forked or recovered a different thread.
	// Persist the receipt's identity even after its request context is done.
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	second.BackendKey = "backend-a"
	if err := s.persistHarnessSession(cancelled, scope, second, backendharness.Observed{SessionID: "recovered-thread"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	// A failed dispatch with no new session must not erase the saved identity.
	if err := s.persistHarnessSession(ctx, scope, second, backendharness.Observed{}, time.Now()); err != nil {
		t.Fatal(err)
	}
	third, err := st.NextHarnessTurn(ctx, scope, "chat", time.Now())
	if err != nil || third.Turns != 3 || third.HarnessSessionID != "recovered-thread" {
		t.Fatalf("next turn = %+v, err = %v; want epoch 3 resuming recovered-thread", third, err)
	}
	other := scope
	other.WorkspaceUUID = "other-workspace"
	isolated, err := st.NextHarnessTurn(ctx, other, "chat", time.Now())
	if err != nil || isolated.HarnessSessionID != "" || isolated.Turns != 1 {
		t.Fatalf("other workspace inherited a session: %+v, %v", isolated, err)
	}
}

func TestHarnessSessionDoesNotReuseNativeSessionAcrossBackends(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemoryStore()
	s := &Server{store: st}
	scope := store.Scope{OrgUUID: "org", WorkspaceUUID: "workspace", AgentName: "coder"}
	first, err := s.harnessSessionFor(ctx, scope, "chat", "backend-a", nil)
	if err != nil || first.Turns != 1 || first.HarnessSessionID != "" {
		t.Fatalf("first backend turn = %+v, err = %v", first, err)
	}
	if err := s.persistHarnessSession(ctx, scope, first, backendharness.Observed{SessionID: "edge-a-thread"}, time.Now()); err != nil {
		t.Fatalf("persist first backend receipt: %v", err)
	}

	changed, err := s.harnessSessionFor(ctx, scope, "chat", "backend-b", nil)
	if err != nil || changed.Turns != 2 || changed.HarnessSessionID != "" || changed.BackendKey != "backend-b" {
		t.Fatalf("changed backend turn = %+v, err = %v; must start without edge-a-thread", changed, err)
	}
	stored, ok, err := st.GetHarnessSession(ctx, scope, "chat")
	if err != nil || !ok || stored.HarnessSessionID != "edge-a-thread" || stored.BackendKey != "backend-a" {
		t.Fatalf("failed dispatch must leave last observed backend pair intact: %+v, ok=%v, err=%v", stored, ok, err)
	}

	// Returning to the original backend can still use its last observed native
	// session if the changed-backend dispatch never produced a receipt.
	returned, err := s.harnessSessionFor(ctx, scope, "chat", "backend-a", nil)
	if err != nil || returned.Turns != 3 || returned.HarnessSessionID != "edge-a-thread" {
		t.Fatalf("return to prior backend = %+v, err = %v", returned, err)
	}
}

func TestHarnessSessionTreatsLegacyBackendKeyAsUnknown(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemoryStore()
	s := &Server{store: legacyHarnessSessionStore{Store: st}}
	scope := store.Scope{OrgUUID: "org", WorkspaceUUID: "workspace", AgentName: "coder"}
	// Simulate a pre-BackendKey row. Its native ID cannot safely be attributed to
	// the current edge and harness.
	got, err := s.harnessSessionFor(ctx, scope, "chat", "backend-a", nil)
	if err != nil || got.Turns != 5 || got.HarnessSessionID != "" {
		t.Fatalf("legacy session dispatch = %+v, err = %v; must allocate the next turn but clear the unknown native ID", got, err)
	}
}

func TestHarnessBackendKeyBindsAllRunnerCoordinates(t *testing.T) {
	base := harnessBackendKey("cluster-a", "LinuxServer", "edge-a", "codex")
	if base != harnessBackendKey("cluster-a", "LinuxServer", "edge-a", "codex") {
		t.Fatal("backend key is not stable")
	}
	for _, changed := range [][4]string{
		{"cluster-b", "LinuxServer", "edge-a", "codex"},
		{"cluster-a", "KubernetesCluster", "edge-a", "codex"},
		{"cluster-a", "LinuxServer", "edge-b", "codex"},
		{"cluster-a", "LinuxServer", "edge-a", "claude-code"},
	} {
		if got := harnessBackendKey(changed[0], changed[1], changed[2], changed[3]); got == base {
			t.Fatalf("backend key did not change for coordinates %q", changed)
		}
	}
}

type failingHarnessSessionStore struct {
	store.Store
	err error
}

type legacyHarnessSessionStore struct {
	store.Store
}

func (s legacyHarnessSessionStore) NextHarnessTurn(ctx context.Context, scope store.Scope, sessionID string, now time.Time) (store.HarnessSession, error) {
	row, err := s.Store.NextHarnessTurn(ctx, scope, sessionID, now)
	if err != nil {
		return store.HarnessSession{}, err
	}
	// Model a pre-BackendKey row whose saved native session has unknown origin.
	row.Turns = 5
	row.HarnessSessionID = "legacy-thread"
	row.BackendKey = ""
	return row, nil
}

func (s failingHarnessSessionStore) PutHarnessSession(ctx context.Context, _ store.Scope, _ store.HarnessSession) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return s.err
}

func TestHarnessSessionWriteFailureIsReturned(t *testing.T) {
	want := errors.New("store unavailable")
	s := &Server{store: failingHarnessSessionStore{Store: store.NewMemoryStore(), err: want}}
	err := s.persistHarnessSession(context.Background(), store.Scope{OrgUUID: "org", WorkspaceUUID: "workspace", AgentName: "coder"},
		store.HarnessSession{SessionID: "chat", Turns: 1}, backendharness.Observed{SessionID: "thread"}, time.Now())
	if !errors.Is(err, want) {
		t.Fatalf("session write failure = %v, want %v", err, want)
	}
}

func TestHarnessRecoveryBillsUsageSinceLastParkNotSinceCheckpoint(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemoryStore()
	s := &Server{store: st}
	scope := store.Scope{OrgUUID: "org", WorkspaceUUID: "workspace", AgentName: "coder"}
	now := time.Now().UTC()
	// The first park billed 100 input tokens. A later running checkpoint has
	// observed 250; that additional 150 was never charged before the crash.
	if _, err := st.AddUsage(ctx, scope, "coder", 100, 10, 20, now, 30*24*time.Hour); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveRun(ctx, scope, store.Run{ID: "run", AgentName: "coder", Phase: store.RunPhaseRunning, InputTokens: 100, OutputTokens: 10, USDMicros: 20, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(backendharness.State{AttemptID: "attempt", Epoch: 1, Cursor: 5, Spent: backend.Cost{Tokens: backend.Tokens{InputTokens: 250, OutputTokens: 25}, CostMicros: 50}})
	if err != nil {
		t.Fatal(err)
	}
	s.checkpointRecorder(ctx, taskRun{Scope: scope, Agent: &agentsv1alpha1.Agent{ObjectMeta: metav1.ObjectMeta{Name: "coder"}}, RunID: "run"}, "chat", agentsv1alpha1.AgentBackendHarness)(raw)
	saved, err := st.GetRun(ctx, scope, "run")
	if err != nil {
		t.Fatal(err)
	}
	total := backend.Cost{Tokens: backend.Tokens{InputTokens: 300, OutputTokens: 30}, CostMicros: 60}
	delta := unbilledHarnessUsage(total, backend.Cost{Tokens: backend.Tokens{InputTokens: saved.InputTokens, OutputTokens: saved.OutputTokens}, CostMicros: saved.USDMicros})
	if delta.InputTokens != 200 || delta.OutputTokens != 20 || delta.CostMicros != 40 {
		t.Fatalf("recovered charge = %+v, want 200/20 tokens and 40 micros", delta)
	}
	usage, err := st.AddUsage(ctx, scope, "coder", delta.InputTokens, delta.OutputTokens, delta.CostMicros, now, 30*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if usage.InputTokens != 300 || usage.OutputTokens != 30 || usage.USDMicros != 60 {
		t.Fatalf("rolling usage = %+v", usage)
	}
	if delta := unbilledHarnessUsage(total, total); delta != (backend.Cost{}) {
		t.Fatalf("already billed park charged again: %+v", delta)
	}
	if delta := unbilledHarnessUsage(total, backend.Cost{}); delta != total {
		t.Fatalf("recovery before first park omitted usage: %+v", delta)
	}
}
