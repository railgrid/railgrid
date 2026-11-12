// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at http://www.apache.org/licenses/LICENSE-2.0

package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"
	"time"

	agentsv1alpha1 "github.com/railgrid/provider-agents/apis/v1alpha1"
	"github.com/railgrid/provider-agents/backend"
	"github.com/railgrid/provider-agents/store"
	"github.com/railgrid/provider-agents/tools"
)

func TestDelegationWaitDoesNotReturnPendingApprovalAsAResult(t *testing.T) {
	st := store.NewMemoryStore()
	s := &Server{store: st}
	scope := store.Scope{OrgUUID: "o", WorkspaceUUID: "w", AgentName: "worker"}
	now := time.Now()
	child := store.Run{ID: "child", AgentName: "worker", Phase: store.RunPhasePendingApproval, CreatedAt: now, UpdatedAt: now}
	if err := st.SaveRun(t.Context(), scope, child); err != nil {
		t.Fatal(err)
	}
	done := make(chan store.Run, 1)
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	go func() {
		r, err := s.waitDelegation(ctx, scope, "child")
		if err == nil {
			done <- r
		}
	}()
	select {
	case <-done:
		t.Fatal("parked child was returned as completed")
	case <-time.After(30 * time.Millisecond):
	}
	child.Phase = store.RunPhaseSucceeded
	child.Output = "verified snapshot"
	if err := st.SaveRun(t.Context(), scope, child); err != nil {
		t.Fatal(err)
	}
	select {
	case r := <-done:
		if r.Output != "verified snapshot" {
			t.Fatal("lost resumed result")
		}
	case <-ctx.Done():
		t.Fatal("did not join resumed child")
	}
}
func TestDelegationReplayReusesItsExistingChildBeforeBudgetGate(t *testing.T) {
	st := store.NewMemoryStore()
	s := &Server{store: st}
	parent := store.Scope{OrgUUID: "o", WorkspaceUUID: "w", AgentName: "coordinator"}
	scope := parent
	scope.AgentName = "reviewer"
	now := time.Now()
	if err := st.SaveRun(t.Context(), parent, store.Run{ID: "parent", AgentName: parent.AgentName, Phase: store.RunPhaseRunning, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	child := store.Run{ID: "child", AgentName: "reviewer", ParentRunID: "parent", Trigger: "delegation", IdempotencyKey: "delegate:parent:call", Phase: store.RunPhaseSucceeded, Output: "independent verdict", CreatedAt: now, UpdatedAt: now}
	if err := st.SaveRun(t.Context(), scope, child); err != nil {
		t.Fatal(err)
	}
	a := &agentsv1alpha1.Agent{}
	a.Name = "coordinator"
	got, err := s.delegateAndWait(backend.WithToolCallID(t.Context(), "call"), tools.Deps{Scope: parent, RunID: "parent", Agent: a}, "reviewer", "replayed tool input")
	if err != nil || got.ID != "child" || got.Output != "independent verdict" {
		t.Fatalf("replay: %+v %v", got, err)
	}
	rows, _ := st.ListRuns(t.Context(), scope, 10)
	if len(rows) != 1 {
		t.Fatal("replay created another worker")
	}
}

func TestNativeDelegationReturnsOnlyVerifiedStoredSnapshot(t *testing.T) {
	st := store.NewMemoryStore()
	s := &Server{store: st}
	scope := store.Scope{OrgUUID: "o", WorkspaceUUID: "w", AgentName: "worker"}
	data := []byte("verified runner bundle")
	sum := sha256.Sum256(data)
	digest := hex.EncodeToString(sum[:])
	artifact := store.RunArtifact{RunID: "child", Name: "git-result.bundle", Data: data, Digest: digest, Size: int64(len(data)), CreatedAt: time.Now()}
	if err := st.SaveRunArtifact(t.Context(), scope, artifact); err != nil {
		t.Fatal(err)
	}
	child := store.Run{ID: "child", Output: "tests passed", Repository: &store.RunRepository{BaseCommit: "base", CommitMessage: "Apply scoped PR fixes"}, Result: &store.RunResult{BaseCommit: "base", Commit: "commit", Tree: "tree", BundleDigest: digest}}
	raw, err := s.delegationOutput(t.Context(), scope, child)
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		RunID      string `json:"runId"`
		Report     string `json:"report"`
		BaseCommit string `json:"baseCommit"`
		NoChanges  bool   `json:"noChanges"`
		Snapshot   struct {
			Bundle     []byte `json:"bundle"`
			BaseCommit string `json:"baseCommit"`
		} `json:"snapshot"`
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil || string(out.Snapshot.Bundle) != string(data) || out.Report != "tests passed" || out.RunID != "child" || out.BaseCommit != "base" || out.NoChanges {
		t.Fatalf("snapshot output: %s %v", raw, err)
	}
	child.Result.BundleDigest = "changed"
	if _, err := s.delegationOutput(t.Context(), scope, child); err == nil {
		t.Fatal("changed snapshot accepted")
	}
}

func TestNativeDelegationNoChangesStillReturnsChildEvidence(t *testing.T) {
	s := &Server{store: store.NewMemoryStore()}
	child := store.Run{
		ID: "review-run", Output: "No actionable findings.",
		Repository: &store.RunRepository{BaseCommit: "base"},
		Result:     &store.RunResult{BaseCommit: "base", NoChanges: true},
	}
	raw, err := s.delegationOutput(t.Context(), store.Scope{OrgUUID: "o", WorkspaceUUID: "w", AgentName: "reviewer"}, child)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatal(err)
	}
	if got["runId"] != "review-run" || got["baseCommit"] != "base" || got["noChanges"] != true || got["report"] != "No actionable findings." {
		t.Fatalf("no-change delegated result = %s", raw)
	}
}
