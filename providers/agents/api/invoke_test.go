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
	"slices"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	agentsv1alpha1 "github.com/faroshq/provider-agents/apis/v1alpha1"
	"github.com/faroshq/provider-agents/store"
	"github.com/faroshq/provider-agents/tools"
)

func TestRunSettled(t *testing.T) {
	settled := []store.RunPhase{store.RunPhaseSucceeded, store.RunPhaseFailed, store.RunPhaseAborted,
		// Settled from the caller's point of view: nothing more happens until a
		// human acts, so waiting longer is pointless.
		store.RunPhasePendingApproval}
	for _, p := range settled {
		if !runSettled(p) {
			t.Fatalf("%s should be settled", p)
		}
	}
	for _, p := range []store.RunPhase{store.RunPhasePending, store.RunPhaseRunning} {
		if runSettled(p) {
			t.Fatalf("%s is still moving", p)
		}
	}
}

func TestWaitForRun(t *testing.T) {
	ctx := context.Background()
	newServer := func() (*Server, store.Scope) {
		return &Server{store: store.NewMemoryStore(), events: newEventBus(), liveRuns: newRunRegistry()},
			store.Scope{OrgUUID: "o", WorkspaceUUID: "w", AgentName: "a"}
	}
	seed := func(s *Server, scope store.Scope, id string, phase store.RunPhase) {
		now := time.Now().UTC()
		if err := s.store.SaveRun(ctx, scope, store.Run{
			ID: id, AgentName: "a", Phase: phase, CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("returns immediately for an already-settled run", func(t *testing.T) {
		s, scope := newServer()
		seed(s, scope, "r1", store.RunPhaseSucceeded)
		start := time.Now()
		run, settled := s.waitForRun(ctx, scope, "r1", 5*time.Second)
		if !settled || run.Phase != store.RunPhaseSucceeded {
			t.Fatalf("settled=%v phase=%s", settled, run.Phase)
		}
		if time.Since(start) > time.Second {
			t.Fatal("a settled run should not be waited on")
		}
	})

	t.Run("observes a transition made by someone else", func(t *testing.T) {
		s, scope := newServer()
		seed(s, scope, "r1", store.RunPhaseRunning)
		// Another replica finishes it. The wait polls the STORE, which is the only
		// thing both replicas see — an in-process event bus would miss this.
		go func() {
			time.Sleep(150 * time.Millisecond)
			seed(s, scope, "r1", store.RunPhaseSucceeded)
		}()
		run, settled := s.waitForRun(ctx, scope, "r1", 5*time.Second)
		if !settled || run.Phase != store.RunPhaseSucceeded {
			t.Fatalf("settled=%v phase=%s", settled, run.Phase)
		}
	})

	t.Run("gives up on timeout without failing the run", func(t *testing.T) {
		s, scope := newServer()
		seed(s, scope, "r1", store.RunPhaseRunning)
		run, settled := s.waitForRun(ctx, scope, "r1", 300*time.Millisecond)
		if settled {
			t.Fatal("a Running run must not report settled")
		}
		// The caller learns where it got to, and the run is untouched.
		if run.Phase != store.RunPhaseRunning {
			t.Fatalf("phase = %s; waiting must not change the run", run.Phase)
		}
		stored, err := s.store.GetRun(ctx, scope, "r1")
		if err != nil || stored.Phase != store.RunPhaseRunning {
			t.Fatalf("run was mutated by a wait: %v %s", err, stored.Phase)
		}
	})

	t.Run("a cancelled caller stops waiting", func(t *testing.T) {
		s, scope := newServer()
		seed(s, scope, "r1", store.RunPhaseRunning)
		cctx, cancel := context.WithCancel(ctx)
		go func() {
			time.Sleep(100 * time.Millisecond)
			cancel()
		}()
		start := time.Now()
		if _, settled := s.waitForRun(cctx, scope, "r1", 10*time.Second); settled {
			t.Fatal("should not report settled")
		}
		if time.Since(start) > 3*time.Second {
			t.Fatal("the wait should end when its caller goes away")
		}
	})
}

func TestFindRunByIdempotencyKey(t *testing.T) {
	ctx := context.Background()
	s := &Server{store: store.NewMemoryStore()}
	scope := store.Scope{OrgUUID: "o", WorkspaceUUID: "w", AgentName: "a"}
	other := store.Scope{OrgUUID: "o", WorkspaceUUID: "w", AgentName: "b"}
	now := time.Now().UTC()

	if err := s.store.SaveRun(ctx, scope, store.Run{
		ID: "r1", AgentName: "a", Phase: store.RunPhaseRunning, IdempotencyKey: "k1",
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}

	got, found, err := s.store.FindRunByIdempotencyKey(ctx, scope, "k1")
	if err != nil || !found || got.ID != "r1" {
		t.Fatalf("lookup: found=%v id=%q err=%v", found, got.ID, err)
	}
	if _, found, _ := s.store.FindRunByIdempotencyKey(ctx, scope, "nope"); found {
		t.Fatal("an unknown key must not match")
	}
	// Keys are per-agent: two agents may use the same key for different work.
	if _, found, _ := s.store.FindRunByIdempotencyKey(ctx, other, "k1"); found {
		t.Fatal("a key must not leak across agents")
	}
	// An empty key never matches, or every keyless run would collide.
	if err := s.store.SaveRun(ctx, scope, store.Run{
		ID: "r2", AgentName: "a", Phase: store.RunPhaseRunning, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := s.store.FindRunByIdempotencyKey(ctx, scope, ""); found {
		t.Fatal("an empty key must never match")
	}
}

// An API-invoked run is unattended even though its caller holds a user token, so
// it is held to the background grant and does not get the edges family. Getting
// this backwards would let a programmatic caller act as the human with the full
// interactive surface and nobody watching.
func TestAPIRunToolPosture(t *testing.T) {
	ctx := context.Background()
	s := &Server{store: store.NewMemoryStore()}
	agent := &agentsv1alpha1.Agent{ObjectMeta: metav1.ObjectMeta{Name: "scout"}}
	agent.Spec.Tools.Interactive = agentsv1alpha1.ToolGrant{Families: []string{"core", "web", "spawn"}}
	agent.Spec.Tools.Background = agentsv1alpha1.ToolGrant{Families: []string{"core"}}
	deps := tools.Deps{Store: s.store, Agent: agent, CR: fakeCR{}, RunID: "r1",
		Scope: store.Scope{OrgUUID: "o", WorkspaceUUID: "w", AgentName: "scout"}}

	t.Run("api is not an interactive class", func(t *testing.T) {
		if isInteractive(agentsv1alpha1.RunTriggerAPI) {
			t.Fatal("an API run has no human present to answer an approval gate")
		}
		// Chat and channel still are.
		if !isInteractive(agentsv1alpha1.RunTriggerChat) || !isInteractive(agentsv1alpha1.RunTriggerChannel) {
			t.Fatal("chat and channel runs must stay interactive")
		}
	})

	t.Run("it takes the background grant", func(t *testing.T) {
		got, _, closer := s.buildToolset(ctx, deps, taskRun{Agent: agent, Trigger: agentsv1alpha1.RunTriggerAPI})
		defer closer()
		names := toolNames(got)
		// Background grants core only, so the interactive-only families are absent.
		if slices.Contains(names, "spawn") || slices.Contains(names, "web_search") {
			t.Fatalf("an API run must not get the interactive grant; got %v", names)
		}
		if !slices.Contains(names, "memory_list") {
			t.Fatalf("core should still be granted; got %v", names)
		}
	})

	t.Run("edges is withheld even though the caller's token is present", func(t *testing.T) {
		// The token is needed for the infrastructure data plane, so it IS set on an
		// API run — which is exactly why the edges gate cannot rely on its absence.
		run := taskRun{
			Agent: agent, Trigger: agentsv1alpha1.RunTriggerAPI,
			EdgesEndpoint: "https://hub.example/mcp", HubToken: "caller-token", ClusterID: "c1",
		}
		got, _, closer := s.buildToolset(ctx, deps, run)
		defer closer()
		for _, n := range toolNames(got) {
			if len(n) > 6 && n[:6] == "edges_" {
				t.Fatalf("edges acts as the calling human and must not appear on an unattended run; got %v", toolNames(got))
			}
		}
	})
}

func TestAPIRunSourceLabel(t *testing.T) {
	if got := apiRunSource(identity{user: "alice"}); got != "api:alice" {
		t.Fatalf("source = %q, want api:alice", got)
	}
	// A ServiceAccount caller has no user name; the label still says how it came in.
	if got := apiRunSource(identity{}); got != "api" {
		t.Fatalf("source = %q, want api", got)
	}
}

// A client watching a run has to be able to recognize a child it has never
// loaded — otherwise a worker spawned after the page opened stays invisible
// until a manual refresh, which is exactly how a fan-out looked like nothing
// was happening.
func TestPublishRunEventCarriesTheParent(t *testing.T) {
	s := &Server{events: newEventBus()}
	scope := store.Scope{OrgUUID: "o", WorkspaceUUID: "w", AgentName: "scout"}
	ch, unsubscribe := s.events.subscribe(store.Scope{OrgUUID: "o", WorkspaceUUID: "w"})
	defer unsubscribe()

	s.publishRunEvent(scope, runEvent{
		ID: "child-1", Agent: "scout", Trigger: agentsv1alpha1.RunTriggerSpawn,
		ParentRunID: "parent-1", Phase: store.RunPhaseRunning,
	})

	select {
	case ev := <-ch:
		data, ok := ev.Data.(map[string]any)
		if !ok {
			t.Fatalf("payload = %T", ev.Data)
		}
		if data["parentRunID"] != "parent-1" {
			t.Fatalf("parentRunID = %v, want parent-1", data["parentRunID"])
		}
		if data["id"] != "child-1" || data["phase"] != "Running" {
			t.Fatalf("payload = %+v", data)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no event published")
	}

	t.Run("a top-level run omits the field rather than sending an empty one", func(t *testing.T) {
		s.publishRunEvent(scope, runEvent{ID: "top", Agent: "scout", Trigger: agentsv1alpha1.RunTriggerChat, Phase: store.RunPhaseSucceeded})
		select {
		case ev := <-ch:
			data := ev.Data.(map[string]any)
			if _, present := data["parentRunID"]; present {
				t.Fatalf("parentRunID should be absent for a top-level run; got %+v", data)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("no event published")
		}
	})
}
