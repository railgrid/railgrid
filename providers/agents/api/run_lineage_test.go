// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at http://www.apache.org/licenses/LICENSE-2.0

package api

import (
	"fmt"
	"strings"
	"testing"
	"time"

	agentsv1alpha1 "github.com/railgrid/provider-agents/apis/v1alpha1"
	"github.com/railgrid/provider-agents/backend"
	"github.com/railgrid/provider-agents/store"
	"github.com/railgrid/provider-agents/tools"
)

func TestDelegateDoesNotAdmitChildAfterParentTerminal(t *testing.T) {
	ctx := t.Context()
	st := store.NewMemoryStore()
	s := &Server{store: st}
	parent := store.Scope{OrgUUID: "org", WorkspaceUUID: "workspace", AgentName: "coordinator"}
	childScope := parent
	childScope.AgentName = "reviewer"
	now := time.Now().UTC()
	if err := st.SaveRun(ctx, parent, store.Run{ID: "parent", AgentName: parent.AgentName, Phase: store.RunPhaseSucceeded, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	a := &agentsv1alpha1.Agent{}
	a.Name = parent.AgentName
	_, err := s.delegateAndWait(backend.WithToolCallID(ctx, "call-1"), tools.Deps{
		Scope: parent, RunID: "parent", Agent: a,
	}, "reviewer", "task")
	if err == nil || !strings.Contains(err.Error(), "no longer active") {
		t.Fatalf("delegate after terminal parent error = %v", err)
	}
	children, err := st.QueryRuns(ctx, childScope, store.RunQuery{ParentRunID: "parent", Limit: 10})
	if err != nil || len(children.Items) != 0 {
		t.Fatalf("terminal parent admitted children: rows=%d err=%v", len(children.Items), err)
	}
}

func TestResumeCannotApproveChildAfterParentTerminal(t *testing.T) {
	ctx := t.Context()
	st := store.NewMemoryStore()
	s := &Server{store: st, liveRuns: newRunRegistry()}
	parent := store.Scope{OrgUUID: "org", WorkspaceUUID: "workspace", AgentName: "coordinator"}
	childScope := parent
	childScope.AgentName = "reviewer"
	now := time.Now().UTC()
	if err := st.SaveRun(ctx, parent, store.Run{ID: "parent", AgentName: parent.AgentName, Phase: store.RunPhaseAborted, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	child := store.Run{ID: "child", AgentName: childScope.AgentName, ParentRunID: "parent", Phase: store.RunPhasePendingApproval, Checkpoint: []byte(`{"backend":"model"}`), CreatedAt: now, UpdatedAt: now}
	if err := st.SaveRun(ctx, childScope, child); err != nil {
		t.Fatal(err)
	}
	if _, err := s.resumeRun(ctx, childScope, child.ID, runAccess{}, resumeIntent{FromPhase: store.RunPhasePendingApproval, Approve: true}); err == nil {
		t.Fatal("child approval resumed after its parent became terminal")
	}
	got, err := st.GetRun(ctx, childScope, child.ID)
	if err != nil || got.Phase != store.RunPhaseAborted || !got.CancelRequested {
		t.Fatalf("orphaned child = phase %s cancel=%t err=%v", got.Phase, got.CancelRequested, err)
	}
}

func TestSuccessfulParentPreservesSpawnApprovalButNotDelegatedChild(t *testing.T) {
	ctx := t.Context()
	st := store.NewMemoryStore()
	s := &Server{store: st, liveRuns: newRunRegistry()}
	parent := store.Scope{OrgUUID: "org", WorkspaceUUID: "workspace", AgentName: "coordinator"}
	now := time.Now().UTC()
	if err := st.SaveRun(ctx, parent, store.Run{ID: "parent", AgentName: parent.AgentName, Phase: store.RunPhaseSucceeded, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	spawned := store.Run{ID: "spawned", AgentName: parent.AgentName, ParentRunID: "parent", Trigger: agentsv1alpha1.RunTriggerSpawn, Phase: store.RunPhasePendingApproval, Checkpoint: []byte(`{"backend":"model","worker":{"depth":1}}`), CreatedAt: now, UpdatedAt: now}
	if err := st.SaveRun(ctx, parent, spawned); err != nil {
		t.Fatal(err)
	}
	runningSpawn := store.Run{ID: "running-spawn", AgentName: parent.AgentName, ParentRunID: "parent", Trigger: agentsv1alpha1.RunTriggerSpawn, Backend: "harness", Phase: store.RunPhaseRunning, Checkpoint: []byte(`{"backend":"harness","worker":{"depth":1}}`), CreatedAt: now, UpdatedAt: now}
	if err := st.SaveRun(ctx, parent, runningSpawn); err != nil {
		t.Fatal(err)
	}
	delegatedScope := parent
	delegatedScope.AgentName = "reviewer"
	activeDelegation := store.Run{ID: "active-delegation", AgentName: delegatedScope.AgentName, ParentRunID: runningSpawn.ID, Trigger: agentsv1alpha1.RunTriggerDelegation, Phase: store.RunPhaseRunning, CreatedAt: now, UpdatedAt: now}
	if err := st.SaveRun(ctx, delegatedScope, activeDelegation); err != nil {
		t.Fatal(err)
	}
	failedChild := store.Run{ID: "failed-child", AgentName: parent.AgentName, ParentRunID: "parent", Trigger: agentsv1alpha1.RunTriggerSpawn, Phase: store.RunPhaseFailed, CreatedAt: now, UpdatedAt: now}
	if err := st.SaveRun(ctx, parent, failedChild); err != nil {
		t.Fatal(err)
	}
	nestedSpawn := store.Run{ID: "nested-spawn", AgentName: parent.AgentName, ParentRunID: failedChild.ID, Trigger: agentsv1alpha1.RunTriggerSpawn, Phase: store.RunPhasePendingApproval, Checkpoint: []byte(`{"backend":"model","worker":{"depth":1}}`), CreatedAt: now, UpdatedAt: now}
	if err := st.SaveRun(ctx, parent, nestedSpawn); err != nil {
		t.Fatal(err)
	}
	successfulIntermediateScope := parent
	successfulIntermediateScope.AgentName = "planner"
	successfulIntermediate := store.Run{ID: "successful-intermediate", AgentName: successfulIntermediateScope.AgentName, ParentRunID: "parent", Trigger: agentsv1alpha1.RunTriggerSpawn, Phase: store.RunPhaseSucceeded, CreatedAt: now, UpdatedAt: now}
	if err := st.SaveRun(ctx, successfulIntermediateScope, successfulIntermediate); err != nil {
		t.Fatal(err)
	}
	nestedSuccessfulSpawnScope := parent
	nestedSuccessfulSpawnScope.AgentName = "writer"
	nestedSuccessfulSpawn := store.Run{ID: "nested-successful-spawn", AgentName: nestedSuccessfulSpawnScope.AgentName, ParentRunID: successfulIntermediate.ID, Trigger: agentsv1alpha1.RunTriggerSpawn, Phase: store.RunPhasePendingApproval, Checkpoint: []byte(`{"backend":"model","worker":{"depth":1}}`), CreatedAt: now, UpdatedAt: now}
	if err := st.SaveRun(ctx, nestedSuccessfulSpawnScope, nestedSuccessfulSpawn); err != nil {
		t.Fatal(err)
	}
	if err := s.reconcileRunDescendants(ctx, parent, "parent"); err != nil {
		t.Fatal(err)
	}
	allowed, err := s.parentRunAllowsResume(ctx, parent, spawned)
	current, getErr := st.GetRun(ctx, parent, spawned.ID)
	if err != nil || getErr != nil || !allowed || current.Phase != store.RunPhasePendingApproval || current.CancelRequested {
		t.Fatalf("successful parent spawn approval allowed=%t phase=%s cancelled=%t err=%v/%v", allowed, current.Phase, current.CancelRequested, err, getErr)
	}
	allowed, err = s.parentRunAllowsResume(ctx, parent, runningSpawn)
	current, getErr = st.GetRun(ctx, parent, runningSpawn.ID)
	if err != nil || getErr != nil || !allowed || current.Phase != store.RunPhaseRunning || current.CancelRequested {
		t.Fatalf("successful parent resumed spawn allowed=%t phase=%s cancelled=%t err=%v/%v", allowed, current.Phase, current.CancelRequested, err, getErr)
	}
	active, err := s.parentRunActive(ctx, delegatedScope, runningSpawn.ID)
	if err != nil || !active {
		t.Fatalf("authorized resumed Spawn cannot admit its active child: active=%t err=%v", active, err)
	}
	allowed, err = s.parentRunAllowsResume(ctx, delegatedScope, activeDelegation)
	current, getErr = st.GetRun(ctx, delegatedScope, activeDelegation.ID)
	if err != nil || getErr != nil || !allowed || current.Phase != store.RunPhaseRunning || current.CancelRequested {
		t.Fatalf("resumed spawn's active delegation allowed=%t phase=%s cancelled=%t err=%v/%v", allowed, current.Phase, current.CancelRequested, err, getErr)
	}
	allowed, err = s.parentRunAllowsResume(ctx, parent, nestedSpawn)
	current, getErr = st.GetRun(ctx, parent, nestedSpawn.ID)
	if err != nil || getErr != nil || allowed || current.Phase != store.RunPhaseAborted || !current.CancelRequested {
		t.Fatalf("failed immediate parent left nested spawn resumable=%t phase=%s cancelled=%t err=%v/%v", allowed, current.Phase, current.CancelRequested, err, getErr)
	}
	allowed, err = s.parentRunAllowsResume(ctx, nestedSuccessfulSpawnScope, nestedSuccessfulSpawn)
	current, getErr = st.GetRun(ctx, nestedSuccessfulSpawnScope, nestedSuccessfulSpawn.ID)
	if err != nil || getErr != nil || !allowed || current.Phase != store.RunPhasePendingApproval || current.CancelRequested {
		t.Fatalf("all-success lineage rejected nested spawn allowed=%t phase=%s cancelled=%t err=%v/%v", allowed, current.Phase, current.CancelRequested, err, getErr)
	}

	delegated := store.Run{ID: "delegated", AgentName: delegatedScope.AgentName, ParentRunID: "parent", Trigger: agentsv1alpha1.RunTriggerDelegation, Phase: store.RunPhasePendingApproval, Checkpoint: []byte(`{"backend":"model"}`), CreatedAt: now, UpdatedAt: now}
	if err := st.SaveRun(ctx, delegatedScope, delegated); err != nil {
		t.Fatal(err)
	}
	if err := s.reconcileRunParent(ctx, delegatedScope, delegated.ID); err != nil {
		t.Fatal(err)
	}
	current, err = st.GetRun(ctx, delegatedScope, delegated.ID)
	if err != nil || current.Phase != store.RunPhaseAborted || !current.CancelRequested {
		t.Fatalf("delegated child after parent success phase=%s cancelled=%t err=%v", current.Phase, current.CancelRequested, err)
	}
}

func TestMissingParentCancelsChildAndParkedHarnessFailsClosed(t *testing.T) {
	ctx := t.Context()
	st := store.NewMemoryStore()
	s := &Server{store: st, liveRuns: newRunRegistry()}
	scope := store.Scope{OrgUUID: "org", WorkspaceUUID: "workspace", AgentName: "reviewer", ClusterID: "cluster-id"}
	now := time.Now().UTC()
	modelChild := store.Run{ID: "orphan", AgentName: scope.AgentName, ParentRunID: "missing-parent", Trigger: agentsv1alpha1.RunTriggerDelegation, Phase: store.RunPhasePendingApproval, Checkpoint: []byte(`{"backend":"model"}`), CreatedAt: now, UpdatedAt: now}
	if err := st.SaveRun(ctx, scope, modelChild); err != nil {
		t.Fatal(err)
	}
	if err := s.reconcileRunParent(ctx, scope, modelChild.ID); err != nil {
		t.Fatal(err)
	}
	current, err := st.GetRun(ctx, scope, modelChild.ID)
	if err != nil || current.Phase != store.RunPhaseAborted || !current.CancelRequested {
		t.Fatalf("orphan model child phase=%s cancelled=%t err=%v", current.Phase, current.CancelRequested, err)
	}

	harnessChild := store.Run{ID: "parked", AgentName: scope.AgentName, ParentRunID: "missing-parent", Trigger: agentsv1alpha1.RunTriggerDelegation, Backend: "harness", Phase: store.RunPhasePendingApproval, Checkpoint: []byte(`{"backend":"harness"}`), CreatedAt: now, UpdatedAt: now}
	if err := st.SaveRun(ctx, scope, harnessChild); err != nil {
		t.Fatal(err)
	}
	if err := s.reconcileRunParent(ctx, scope, harnessChild.ID); err == nil {
		t.Fatal("missing workspace client must not count parked harness cancellation as complete")
	}
	current, err = st.GetRun(ctx, scope, harnessChild.ID)
	if err != nil || current.Phase != store.RunPhasePendingApproval || !current.CancelRequested {
		t.Fatalf("parked orphan after unavailable stop phase=%s cancelled=%t err=%v", current.Phase, current.CancelRequested, err)
	}
}

func TestFailedAncestorCancelsSpawnBelowSuccessfulIntermediate(t *testing.T) {
	for _, tc := range []struct {
		name            string
		phase           store.RunPhase
		missing, cancel bool
	}{
		{name: "failed root", phase: store.RunPhaseFailed},
		{name: "aborted root", phase: store.RunPhaseAborted},
		{name: "cancelled successful root", phase: store.RunPhaseSucceeded, cancel: true},
		{name: "missing root", missing: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			st := store.NewMemoryStore()
			s := &Server{store: st, liveRuns: newRunRegistry()}
			root := store.Scope{OrgUUID: "org", WorkspaceUUID: "workspace", AgentName: "coordinator"}
			intermediateScope := root
			intermediateScope.AgentName = "reviewer"
			spawnScope := root
			spawnScope.AgentName = "worker"
			now := time.Now().UTC()
			if !tc.missing {
				rootRun := store.Run{ID: "root", AgentName: root.AgentName, Phase: tc.phase, CancelRequested: tc.cancel, CreatedAt: now, UpdatedAt: now}
				if err := st.SaveRun(ctx, root, rootRun); err != nil {
					t.Fatal(err)
				}
			}
			intermediate := store.Run{ID: "intermediate", AgentName: intermediateScope.AgentName, ParentRunID: "root", Phase: store.RunPhaseSucceeded, CreatedAt: now, UpdatedAt: now}
			if err := st.SaveRun(ctx, intermediateScope, intermediate); err != nil {
				t.Fatal(err)
			}
			spawn := store.Run{ID: "nested-spawn", AgentName: spawnScope.AgentName, ParentRunID: "intermediate", Trigger: agentsv1alpha1.RunTriggerSpawn, Phase: store.RunPhasePendingApproval, Checkpoint: []byte(`{"backend":"model","worker":{"depth":1}}`), CreatedAt: now, UpdatedAt: now}
			if err := st.SaveRun(ctx, spawnScope, spawn); err != nil {
				t.Fatal(err)
			}
			runningIntermediate := store.Run{ID: "running-intermediate", AgentName: intermediateScope.AgentName, ParentRunID: "root", Trigger: agentsv1alpha1.RunTriggerSpawn, Phase: store.RunPhaseRunning, Checkpoint: []byte(`{"backend":"model","worker":{"depth":1}}`), CreatedAt: now, UpdatedAt: now}
			if err := st.SaveRun(ctx, intermediateScope, runningIntermediate); err != nil {
				t.Fatal(err)
			}
			active, err := s.parentRunActive(ctx, spawnScope, runningIntermediate.ID)
			if err != nil || active {
				t.Fatalf("running child below invalid ancestor active=%t err=%v", active, err)
			}
			if err := s.reconcileRunDescendants(ctx, root, "root"); err != nil {
				t.Fatal(err)
			}
			allowed, err := s.parentRunAllowsResume(ctx, spawnScope, spawn)
			current, getErr := st.GetRun(ctx, spawnScope, spawn.ID)
			if err != nil || getErr != nil || allowed || current.Phase != store.RunPhaseAborted || !current.CancelRequested {
				t.Fatalf("invalid ancestor left nested spawn allowed=%t phase=%s cancelled=%t err=%v/%v", allowed, current.Phase, current.CancelRequested, err, getErr)
			}
		})
	}
}

func TestTerminalRunCancelsPagedDescendantLineage(t *testing.T) {
	ctx := t.Context()
	st := store.NewMemoryStore()
	s := &Server{store: st, liveRuns: newRunRegistry()}
	scope := store.Scope{OrgUUID: "org", WorkspaceUUID: "workspace", AgentName: "coordinator"}
	childScope := scope
	childScope.AgentName = "reviewer"
	now := time.Now().UTC()
	if err := st.SaveRun(ctx, scope, store.Run{ID: "parent", AgentName: scope.AgentName, Phase: store.RunPhaseFailed, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	for i := range 205 {
		id := fmt.Sprintf("child-%03d", i)
		if err := st.SaveRun(ctx, childScope, store.Run{ID: id, AgentName: childScope.AgentName, ParentRunID: "parent", Phase: store.RunPhasePending, CreatedAt: now.Add(time.Duration(i) * time.Second), UpdatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	grandchildScope := childScope
	grandchildScope.AgentName = "worker"
	if err := st.SaveRun(ctx, grandchildScope, store.Run{ID: "grandchild", AgentName: grandchildScope.AgentName, ParentRunID: "child-000", Phase: store.RunPhasePendingApproval, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := s.reconcileRunDescendants(ctx, scope, "parent"); err != nil {
		t.Fatal(err)
	}
	for i := range 205 {
		id := fmt.Sprintf("child-%03d", i)
		got, err := st.GetRun(ctx, childScope, id)
		if err != nil || got.Phase != store.RunPhaseAborted || !got.CancelRequested {
			t.Fatalf("descendant %s phase=%s cancel=%t err=%v", id, got.Phase, got.CancelRequested, err)
		}
	}
	got, err := st.GetRun(ctx, grandchildScope, "grandchild")
	if err != nil || got.Phase != store.RunPhaseAborted || !got.CancelRequested {
		t.Fatalf("grandchild phase=%s cancel=%t err=%v", got.Phase, got.CancelRequested, err)
	}
}

func TestTerminalRunDescendantCancellationContinuesBeyondPerPassLimit(t *testing.T) {
	ctx := t.Context()
	st := store.NewMemoryStore()
	s := &Server{store: st, liveRuns: newRunRegistry()}
	scope := store.Scope{OrgUUID: "org", WorkspaceUUID: "workspace", AgentName: "coordinator"}
	childScope := scope
	childScope.AgentName = "reviewer"
	now := time.Now().UTC()
	if err := st.SaveRun(ctx, scope, store.Run{ID: "parent", AgentName: scope.AgentName, Phase: store.RunPhaseFailed, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	for i := range maxRunDescendants + 1 {
		id := fmt.Sprintf("child-%04d", i)
		if err := st.SaveRun(ctx, childScope, store.Run{ID: id, AgentName: childScope.AgentName, ParentRunID: "parent", Phase: store.RunPhasePending, CreatedAt: now.Add(time.Duration(i) * time.Second), UpdatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.reconcileRunDescendants(ctx, scope, "parent"); err == nil || !strings.Contains(err.Error(), "will continue on retry") {
		t.Fatalf("first cancellation pass error = %v, want bounded retry signal", err)
	}
	if err := s.reconcileRunDescendants(ctx, scope, "parent"); err != nil {
		t.Fatalf("second cancellation pass: %v", err)
	}
	for i := range maxRunDescendants + 1 {
		id := fmt.Sprintf("child-%04d", i)
		got, err := st.GetRun(ctx, childScope, id)
		if err != nil || got.Phase != store.RunPhaseAborted || !got.CancelRequested {
			t.Fatalf("descendant %s phase=%s cancel=%t err=%v", id, got.Phase, got.CancelRequested, err)
		}
	}
}

func TestTerminalRunKeepsHarnessChildLiveWhenRemoteStopUnavailable(t *testing.T) {
	ctx := t.Context()
	st := store.NewMemoryStore()
	s := &Server{store: st, liveRuns: newRunRegistry()}
	scope := store.Scope{OrgUUID: "org", WorkspaceUUID: "workspace", AgentName: "coordinator", ClusterID: "cluster-id"}
	childScope := scope
	childScope.AgentName = "reviewer"
	now := time.Now().UTC()
	if err := st.SaveRun(ctx, scope, store.Run{ID: "parent", AgentName: scope.AgentName, Phase: store.RunPhaseAborted, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveRun(ctx, childScope, store.Run{ID: "child", AgentName: childScope.AgentName, ParentRunID: "parent", Backend: "harness", Phase: store.RunPhasePendingApproval, Checkpoint: []byte(`{"backend":"harness"}`), CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	err := s.reconcileRunDescendants(ctx, scope, "parent")
	if err == nil {
		t.Fatal("unavailable remote stop counted as complete cleanup")
	}
	got, err := st.GetRun(ctx, childScope, "child")
	if err != nil || got.Phase != store.RunPhasePendingApproval || !got.CancelRequested {
		t.Fatalf("parked child after unavailable stop: phase=%s cancel=%t err=%v", got.Phase, got.CancelRequested, err)
	}
}
