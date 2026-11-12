// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package api

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/railgrid/provider-agents/store"
)

func TestDelegationBudgetSurvivesRebuildAndCountsOtherAgents(t *testing.T) {
	ctx := context.Background()
	storage := store.NewMemoryStore()
	scope := store.Scope{OrgUUID: "org", WorkspaceUUID: "ws", AgentName: "coordinator"}
	for i := 0; i < 3; i++ {
		server := &Server{store: storage} // each check can come from a rebuilt executor
		if err := server.checkDelegationBudget(ctx, scope, "parent"); err != nil {
			t.Fatal(err)
		}
		childScope := scope
		childScope.AgentName = fmt.Sprintf("worker-%d", i)
		if err := storage.SaveRun(ctx, childScope, store.Run{ID: fmt.Sprintf("child-%d", i), AgentName: childScope.AgentName, ParentRunID: "parent", Trigger: "delegation", Phase: store.RunPhaseSucceeded, CreatedAt: time.Now(), UpdatedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	recovered := &Server{store: storage}
	if err := recovered.checkDelegationBudget(ctx, scope, "parent"); err == nil {
		t.Fatal("recovery reset the delegation limit")
	}
	if err := recovered.checkDelegationBudget(ctx, scope, "different-parent"); err != nil {
		t.Fatal("another invocation inherited the budget", err)
	}
	scope.WorkspaceUUID = "another-ws"
	if err := recovered.checkDelegationBudget(ctx, scope, "parent"); err != nil {
		t.Fatal("another workspace inherited the budget", err)
	}
}
