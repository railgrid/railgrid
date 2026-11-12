// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at http://www.apache.org/licenses/LICENSE-2.0

package api

import (
	"testing"
	"time"

	agentsv1alpha1 "github.com/railgrid/provider-agents/apis/v1alpha1"
	"github.com/railgrid/provider-agents/store"
)

func TestPendingApprovalParentPreservesExistingChildren(t *testing.T) {
	ctx := t.Context()
	st := store.NewMemoryStore()
	s := &Server{store: st, events: newEventBus(), liveRuns: newRunRegistry()}
	parentScope := store.Scope{OrgUUID: "org", WorkspaceUUID: "workspace", AgentName: "coordinator"}
	now := time.Now().UTC()
	parent := store.Run{ID: "parent", AgentName: parentScope.AgentName, Phase: store.RunPhasePendingApproval, CreatedAt: now, UpdatedAt: now}
	if err := st.SaveRun(ctx, parentScope, parent); err != nil {
		t.Fatal(err)
	}
	children := []store.Run{
		{
			ID: "spawned", AgentName: parentScope.AgentName, ParentRunID: parent.ID,
			Trigger: agentsv1alpha1.RunTriggerSpawn, Phase: store.RunPhaseRunning,
			Checkpoint: []byte(`{"backend":"model","worker":{"depth":1}}`), CreatedAt: now, UpdatedAt: now,
		},
		{
			ID: "delegated", AgentName: "reviewer", ParentRunID: parent.ID,
			Trigger: agentsv1alpha1.RunTriggerDelegation, Phase: store.RunPhaseRunning,
			CreatedAt: now, UpdatedAt: now,
		},
	}
	for _, child := range children {
		childScope := parentScope
		childScope.AgentName = child.AgentName
		if err := st.SaveRun(ctx, childScope, child); err != nil {
			t.Fatal(err)
		}
		if err := s.reconcileRunParent(ctx, childScope, child.ID); err != nil {
			t.Fatalf("existing %s child reconciliation failed: %v", child.Trigger, err)
		}
		got, err := st.GetRun(ctx, childScope, child.ID)
		if err != nil || got.Phase != store.RunPhaseRunning || got.CancelRequested {
			t.Fatalf("existing %s child = phase %s cancelled=%t err=%v; want active and uncancelled", child.Trigger, got.Phase, got.CancelRequested, err)
		}
	}
	if active, err := s.parentRunActive(ctx, parentScope, parent.ID); err != nil || active {
		t.Fatalf("new child admission from paused parent = active %t, err %v; want rejected", active, err)
	}
}

func TestCancelledPendingApprovalParentDrainsExistingChildren(t *testing.T) {
	ctx := t.Context()
	st := store.NewMemoryStore()
	s := &Server{store: st, events: newEventBus(), liveRuns: newRunRegistry()}
	parentScope := store.Scope{OrgUUID: "org", WorkspaceUUID: "workspace", AgentName: "coordinator"}
	now := time.Now().UTC()
	parent := store.Run{
		ID: "parent", AgentName: parentScope.AgentName, Phase: store.RunPhasePendingApproval,
		CancelRequested: true, CreatedAt: now, UpdatedAt: now,
	}
	if err := st.SaveRun(ctx, parentScope, parent); err != nil {
		t.Fatal(err)
	}
	children := []store.Run{
		{
			ID: "spawned", AgentName: parentScope.AgentName, ParentRunID: parent.ID,
			Trigger: agentsv1alpha1.RunTriggerSpawn, Phase: store.RunPhaseRunning,
			Checkpoint: []byte(`{"backend":"model","worker":{"depth":1}}`), CreatedAt: now, UpdatedAt: now,
		},
		{
			ID: "delegated", AgentName: "reviewer", ParentRunID: parent.ID,
			Trigger: agentsv1alpha1.RunTriggerDelegation, Phase: store.RunPhaseRunning,
			CreatedAt: now, UpdatedAt: now,
		},
	}
	for _, child := range children {
		childScope := parentScope
		childScope.AgentName = child.AgentName
		if err := st.SaveRun(ctx, childScope, child); err != nil {
			t.Fatal(err)
		}
		if err := s.reconcileRunParent(ctx, childScope, child.ID); err != nil {
			t.Fatalf("cancelled parent failed to drain %s child: %v", child.Trigger, err)
		}
		got, err := st.GetRun(ctx, childScope, child.ID)
		if err != nil || got.Phase != store.RunPhaseAborted || !got.CancelRequested {
			t.Fatalf("cancelled-parent %s child = phase %s cancelled=%t err=%v; want aborted and cancelled", child.Trigger, got.Phase, got.CancelRequested, err)
		}
	}
}
