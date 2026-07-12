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
	"testing"
	"time"

	agentsv1alpha1 "github.com/faroshq/provider-agents/apis/v1alpha1"
	"github.com/faroshq/provider-agents/store"
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
