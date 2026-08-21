// Copyright 2026 The Faros Authors.
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
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	agentsv1alpha1 "github.com/faroshq/provider-agents/apis/v1alpha1"
	"github.com/faroshq/provider-agents/store"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestCheckBudget(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	scope := store.Scope{OrgUUID: "o", WorkspaceUUID: "w", AgentName: "a"}

	newServer := func() *Server {
		st := store.NewMemoryStore()
		return &Server{store: st}
	}
	agentWith := func(b *agentsv1alpha1.AgentBudget) *agentsv1alpha1.Agent {
		a := &agentsv1alpha1.Agent{}
		a.Name = "a"
		a.Spec.Budget = b
		return a
	}

	t.Run("nil budget never blocks", func(t *testing.T) {
		s := newServer()
		if err := s.checkBudget(ctx, scope, agentWith(nil), now); err != nil {
			t.Fatalf("nil budget should not block: %v", err)
		}
	})

	t.Run("token limit blocks once reached", func(t *testing.T) {
		s := newServer()
		a := agentWith(&agentsv1alpha1.AgentBudget{Window: "month", TokenLimit: 100})
		// Under budget: allowed.
		if _, err := s.store.AddUsage(ctx, scope, "a", 40, 20, 0, now, budgetWindow(a.Spec.Budget)); err != nil {
			t.Fatal(err)
		}
		if err := s.checkBudget(ctx, scope, a, now); err != nil {
			t.Fatalf("60/100 should be allowed: %v", err)
		}
		// Push over the limit.
		if _, err := s.store.AddUsage(ctx, scope, "a", 40, 20, 0, now, budgetWindow(a.Spec.Budget)); err != nil {
			t.Fatal(err)
		}
		err := s.checkBudget(ctx, scope, a, now)
		if !errors.Is(err, ErrBudgetExceeded) {
			t.Fatalf("120/100 should exceed budget, got: %v", err)
		}
	})

	t.Run("usd limit blocks once reached", func(t *testing.T) {
		s := newServer()
		a := agentWith(&agentsv1alpha1.AgentBudget{Window: "month", USDLimit: "1.00"})
		// $0.50 spent — allowed.
		if _, err := s.store.AddUsage(ctx, scope, "a", 0, 0, 500_000, now, budgetWindow(a.Spec.Budget)); err != nil {
			t.Fatal(err)
		}
		if err := s.checkBudget(ctx, scope, a, now); err != nil {
			t.Fatalf("$0.50/$1.00 should be allowed: %v", err)
		}
		// $1.00 spent — blocked.
		if _, err := s.store.AddUsage(ctx, scope, "a", 0, 0, 500_000, now, budgetWindow(a.Spec.Budget)); err != nil {
			t.Fatal(err)
		}
		if err := s.checkBudget(ctx, scope, a, now); !errors.Is(err, ErrBudgetExceeded) {
			t.Fatalf("$1.00/$1.00 should exceed, got: %v", err)
		}
	})
}

func TestDetachedRunFinalizesPreflightError(t *testing.T) {
	ctx := context.Background()
	scope := store.Scope{OrgUUID: "org", WorkspaceUUID: "workspace", AgentName: "scout"}
	tracker := &recordingTelemetryTracker{}
	s := &Server{
		store:     store.NewMemoryStore(),
		telemetry: tracker,
		events:    newEventBus(),
	}
	watch, unsubscribe := s.events.subscribe(scope)
	defer unsubscribe()

	agent := &agentsv1alpha1.Agent{ObjectMeta: metav1.ObjectMeta{Name: scope.AgentName}}
	r := httptest.NewRequestWithContext(ctx, http.MethodPost, "/api/agents/scout/runs", nil)
	runID := s.startDetachedRun(r, nil, identity{
		orgUUID:       scope.OrgUUID,
		workspaceUUID: scope.WorkspaceUUID,
	}, agent, taskRun{Task: "preflight", Trigger: agentsv1alpha1.RunTriggerAPI})

	select {
	case ev := <-watch:
		data, ok := ev.Data.(map[string]any)
		if !ok {
			t.Fatalf("event payload = %T, want map", ev.Data)
		}
		if data["id"] != runID || data["phase"] != string(store.RunPhaseFailed) {
			t.Fatalf("run event = %#v, want failed run %s", data, runID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("preflight error did not publish a terminal run event")
	}

	got, err := s.store.GetRun(ctx, scope, runID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Phase != store.RunPhaseFailed {
		t.Fatalf("phase = %q, want Failed", got.Phase)
	}
	if got.Message == "" || !strings.Contains(got.Message, "model credential") {
		t.Fatalf("message = %q, want the preflight credential error", got.Message)
	}
	if got.FinishedAt == nil {
		t.Fatal("preflight failure must stamp FinishedAt")
	}
	events := tracker.snapshot()
	if len(events) != 1 || events[0].Action != agentsRunTerminalAction || events[0].Properties["outcome"] != "failed" {
		t.Fatalf("terminal telemetry = %#v, want one failed event", events)
	}
}

func TestDetachedRunFinalizationPreservesTerminalAndDeduplicates(t *testing.T) {
	ctx := context.Background()
	scope := store.Scope{OrgUUID: "org", WorkspaceUUID: "workspace", AgentName: "scout"}
	tracker := &recordingTelemetryTracker{}
	s := &Server{store: store.NewMemoryStore(), telemetry: tracker, events: newEventBus()}
	now := time.Now().UTC()
	finished := now.Add(-time.Minute)
	if err := s.store.SaveRun(ctx, scope, store.Run{
		ID: "already-done", AgentName: scope.AgentName, Phase: store.RunPhaseAborted,
		Message: "cancelled by user", Output: "preserve me", CreatedAt: now, UpdatedAt: finished,
		FinishedAt: &finished,
	}); err != nil {
		t.Fatal(err)
	}

	s.finalizeDetachedRun(ctx, scope, "already-done", scope.AgentName, "api", errors.New("late preflight error"))
	got, err := s.store.GetRun(ctx, scope, "already-done")
	if err != nil {
		t.Fatal(err)
	}
	if got.Phase != store.RunPhaseAborted || got.Message != "cancelled by user" || got.Output != "preserve me" || !got.FinishedAt.Equal(finished) {
		t.Fatalf("terminal run was overwritten: %#v", got)
	}
	if got := len(tracker.snapshot()); got != 0 {
		t.Fatalf("late finalization emitted %d telemetry events, want none", got)
	}

	if err := s.store.SaveRun(ctx, scope, store.Run{
		ID: "pending", AgentName: scope.AgentName, Phase: store.RunPhasePending,
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	s.finalizeDetachedRun(ctx, scope, "pending", scope.AgentName, "api", errors.New("first preflight error"))
	s.finalizeDetachedRun(ctx, scope, "pending", scope.AgentName, "api", errors.New("duplicate preflight error"))
	got, err = s.store.GetRun(ctx, scope, "pending")
	if err != nil {
		t.Fatal(err)
	}
	if got.Phase != store.RunPhaseFailed || got.Message != "first preflight error" || got.FinishedAt == nil {
		t.Fatalf("pending run finalization = %#v, want first failed result", got)
	}
	if got := len(tracker.snapshot()); got != 1 {
		t.Fatalf("duplicate finalization emitted %d telemetry events, want one", got)
	}
}
