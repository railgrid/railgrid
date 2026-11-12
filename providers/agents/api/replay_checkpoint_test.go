// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at http://www.apache.org/licenses/LICENSE-2.0

package api

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	agentsv1alpha1 "github.com/railgrid/provider-agents/apis/v1alpha1"
	"github.com/railgrid/provider-agents/engine"
	"github.com/railgrid/provider-agents/store"
)

func TestReplaySafeCheckpointRejectsOldRecoveryEpoch(t *testing.T) {
	ctx := t.Context()
	st := store.NewMemoryStore()
	scope := store.Scope{OrgUUID: "org", WorkspaceUUID: "workspace", AgentName: "coordinator"}
	s := &Server{store: st}
	now := time.Now().UTC()
	run := store.Run{ID: "parent", AgentName: scope.AgentName, Phase: store.RunPhaseRunning, Attempt: 0, CreatedAt: now, UpdatedAt: now}
	if err := st.SaveRun(ctx, scope, run); err != nil {
		t.Fatal(err)
	}
	claimed, err := st.ClaimRecovery(ctx, scope, run.ID, run.UpdatedAt, "recovery-process", now.Add(time.Second))
	if err != nil || claimed.Attempt != 1 {
		t.Fatalf("recovery claim = %+v err=%v", claimed, err)
	}
	state, err := json.Marshal(engine.Checkpoint{
		Messages: []engine.CheckpointMessage{{Role: engine.RoleUser, Content: "task"}},
		Pending:  []engine.PendingCall{{ID: "call", Name: "delegate", Args: `{}`}}, ReplaySafePending: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	stale := taskRun{Scope: scope, RunID: run.ID, Attempt: 0, Task: "task"}
	err = s.persistReplaySafeCheckpoint(ctx, stale, agentsv1alpha1.AgentBackendModel, 1, state, false)
	if err == nil || !strings.Contains(err.Error(), "recovery epoch changed") {
		t.Fatalf("stale replay checkpoint error = %v", err)
	}
	got, err := st.GetRun(ctx, scope, run.ID)
	if err != nil || len(got.Checkpoint) != 0 || got.Attempt != 1 {
		t.Fatalf("stale checkpoint modified claimed run: attempt=%d checkpoint=%s err=%v", got.Attempt, got.Checkpoint, err)
	}
}

func TestReplaySafeCheckpointPersistsExactApprovedCall(t *testing.T) {
	ctx := t.Context()
	st := store.NewMemoryStore()
	scope := store.Scope{OrgUUID: "org", WorkspaceUUID: "workspace", AgentName: "coordinator"}
	s := &Server{store: st}
	now := time.Now().UTC()
	run := store.Run{ID: "parent", AgentName: scope.AgentName, Phase: store.RunPhaseRunning, Attempt: 2, CreatedAt: now, UpdatedAt: now}
	if err := st.SaveRun(ctx, scope, run); err != nil {
		t.Fatal(err)
	}
	state, err := json.Marshal(engine.Checkpoint{
		Messages:          []engine.CheckpointMessage{{Role: engine.RoleUser, Content: "task"}},
		Pending:           []engine.PendingCall{{ID: "delegate-call", Name: "delegate", Args: `{"agent":"reviewer"}`}},
		ReplaySafePending: true, ApprovedPending: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.persistReplaySafeCheckpoint(ctx, taskRun{Scope: scope, RunID: run.ID, Attempt: 2, Task: "task"}, agentsv1alpha1.AgentBackendModel, 4, state, true); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetRun(ctx, scope, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	var checkpoint runCheckpoint
	if err := json.Unmarshal(got.Checkpoint, &checkpoint); err != nil {
		t.Fatal(err)
	}
	if !checkpoint.Engine.ReplaySafePending || !checkpoint.Engine.ApprovedPending || checkpoint.Tool != "delegate" || checkpoint.Args != `{"agent":"reviewer"}` {
		t.Fatalf("persisted replay checkpoint = %+v", checkpoint)
	}
}
