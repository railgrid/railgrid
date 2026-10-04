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

	agentsv1alpha1 "github.com/railgrid/provider-agents/apis/v1alpha1"
	backendharness "github.com/railgrid/provider-agents/backend/harness"
	"github.com/railgrid/provider-agents/store"
)

func TestHarnessApprovalsKeepTheAttemptEpoch(t *testing.T) {
	ctx := context.Background()
	s := &Server{store: store.NewMemoryStore()}
	scope := store.Scope{OrgUUID: "org", WorkspaceUUID: "workspace", AgentName: "coder"}
	first, err := s.harnessSessionFor(ctx, scope, "chat", nil)
	if err != nil || first.Turns != 1 {
		t.Fatalf("first turn = %+v, err = %v", first, err)
	}
	raw, err := json.Marshal(backendharness.State{AttemptID: "attempt", Epoch: 1, SessionID: "thread"})
	if err != nil {
		t.Fatal(err)
	}
	cont := &continuation{Checkpoint: runCheckpoint{Backend: agentsv1alpha1.AgentBackendHarness, Harness: raw}}
	for range 2 {
		resumed, err := s.harnessSessionFor(ctx, scope, "chat", cont)
		if err != nil || resumed.Turns != 1 || resumed.HarnessSessionID != "thread" {
			t.Fatalf("approval resume = %+v, err = %v; want the original attempt", resumed, err)
		}
		if err := s.persistHarnessSession(ctx, scope, resumed, backendharness.Observed{SessionID: "thread"}, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	next, err := s.harnessSessionFor(ctx, scope, "chat", nil)
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

type failingHarnessSessionStore struct {
	store.Store
	err error
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
