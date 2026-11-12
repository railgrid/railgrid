// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// The repository half of a run record and the artifacts filed under it, on
// both stores: the request and result round-trip, artifacts are keyed by run
// and name, and deleting the run deletes them.
func testRunArtifacts(t *testing.T, s Store, sc Scope) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	const base = "0123456789abcdef0123456789abcdef01234567"
	runID := uuid.NewString()

	// A repository run records what it ran against, and nothing it could
	// clone with.
	if err := s.SaveRun(ctx, sc, Run{
		ID: runID, AgentName: sc.AgentName, Trigger: "api", Phase: RunPhaseRunning, Backend: "harness",
		Repository: &RunRepository{RepositoryID: "railgrid", BaseCommit: base, CommitMessage: "Implement T-1"},
		CreatedAt:  now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := s.GetRun(ctx, sc, runID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Repository == nil || got.Repository.RepositoryID != "railgrid" || got.Repository.BaseCommit != base || got.Repository.CommitMessage != "Implement T-1" {
		t.Fatalf("repository = %+v", got.Repository)
	}
	if got.Result != nil {
		t.Fatalf("a running run has no result: %+v", got.Result)
	}

	// Nothing stored yet: not found, not an error.
	if _, found, err := s.GetRunArtifact(ctx, sc, runID, "git-result.json"); err != nil || found {
		t.Fatalf("before save: found=%v err=%v", found, err)
	}

	doc := []byte(`{"version":"git-result/v1"}` + "\n")
	bundle := []byte{0x00, 0xff, 'b', 'u', 'n', 'd', 'l', 'e'}
	for _, a := range []RunArtifact{
		{RunID: runID, Name: "git-result.json", Digest: "d1", MediaType: "application/json", Size: int64(len(doc)), Data: doc, CreatedAt: now},
		{RunID: runID, Name: "git-result.bundle", Digest: "d2", MediaType: "application/x-git-bundle", Size: int64(len(bundle)), Data: bundle, CreatedAt: now},
	} {
		if err := s.SaveRunArtifact(ctx, sc, a); err != nil {
			t.Fatalf("save artifact %s: %v", a.Name, err)
		}
	}
	// A size that does not match the bytes is refused: the digest a reader
	// re-checks is over exactly these bytes.
	if err := s.SaveRunArtifact(ctx, sc, RunArtifact{RunID: runID, Name: "x", Digest: "d", Size: 99, Data: []byte("abc"), CreatedAt: now}); err == nil {
		t.Fatal("a mismatched size must be refused")
	}
	// A scope with no agent cannot file one.
	if err := s.SaveRunArtifact(ctx, Scope{OrgUUID: sc.OrgUUID, WorkspaceUUID: sc.WorkspaceUUID}, RunArtifact{RunID: runID, Name: "y", Digest: "d", Size: 1, Data: []byte("a"), CreatedAt: now}); err == nil {
		t.Fatal("an agent-less scope must be refused")
	}

	a, found, err := s.GetRunArtifact(ctx, sc, runID, "git-result.bundle")
	if err != nil || !found {
		t.Fatalf("get bundle: found=%v err=%v", found, err)
	}
	if a.RunID != runID || a.Name != "git-result.bundle" || a.Digest != "d2" || a.MediaType != "application/x-git-bundle" || a.Size != int64(len(bundle)) || string(a.Data) != string(bundle) {
		t.Fatalf("bundle = %+v", a)
	}
	if !a.CreatedAt.Equal(now) {
		t.Fatalf("createdAt = %v, want %v", a.CreatedAt, now)
	}
	// Mutating what came back does not reach the store.
	a.Data[0] = 'Z'
	if again, _, _ := s.GetRunArtifact(ctx, sc, runID, "git-result.bundle"); string(again.Data) != string(bundle) {
		t.Fatal("the store handed out its own buffer")
	}
	// Upsert: saving the same name again replaces it.
	if err := s.SaveRunArtifact(ctx, sc, RunArtifact{RunID: runID, Name: "git-result.json", Digest: "d3", MediaType: "application/json", Size: 2, Data: []byte("{}"), CreatedAt: now}); err != nil {
		t.Fatalf("re-save: %v", err)
	}
	if j, _, _ := s.GetRunArtifact(ctx, sc, runID, "git-result.json"); j.Digest != "d3" || string(j.Data) != "{}" {
		t.Fatalf("re-saved document = %+v", j)
	}
	// Another run in the same scope sees nothing of this one's.
	if _, found, _ := s.GetRunArtifact(ctx, sc, uuid.NewString(), "git-result.json"); found {
		t.Fatal("artifacts are keyed by run")
	}
	// Another tenant sees nothing at all.
	other := Scope{OrgUUID: sc.OrgUUID + "-other", WorkspaceUUID: sc.WorkspaceUUID, AgentName: sc.AgentName}
	if _, found, _ := s.GetRunArtifact(ctx, other, runID, "git-result.json"); found {
		t.Fatal("artifacts are tenant-scoped")
	}

	// The verified result lands on the run and reads back whole.
	got.Phase = RunPhaseSucceeded
	got.Result = &RunResult{
		BaseCommit: base, Commit: strings.Repeat("b", 40), Tree: strings.Repeat("c", 40),
		ResultDigest: "d3", BundleDigest: "d2", BundleSize: int64(len(bundle)),
	}
	got.UpdatedAt = now
	if err := s.SaveRun(ctx, sc, got); err != nil {
		t.Fatalf("save with result: %v", err)
	}
	done, err := s.GetRun(ctx, sc, runID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if done.Result == nil || done.Result.Commit != strings.Repeat("b", 40) || done.Result.BundleDigest != "d2" || done.Result.BundleSize != int64(len(bundle)) || done.Result.NoChanges {
		t.Fatalf("result = %+v", done.Result)
	}
	if done.Repository == nil || done.Repository.RepositoryID != "railgrid" {
		t.Fatalf("the request survives the result: %+v", done.Repository)
	}
	// A no-changes result keeps its flag and nothing else.
	done.Result = &RunResult{BaseCommit: base, NoChanges: true, ResultDigest: "d3"}
	if err := s.SaveRun(ctx, sc, done); err != nil {
		t.Fatal(err)
	}
	if again, _ := s.GetRun(ctx, sc, runID); again.Result == nil || !again.Result.NoChanges || again.Result.Commit != "" {
		t.Fatalf("no-changes result = %+v", again.Result)
	}
	if raw, _ := json.Marshal(done.Result); strings.Contains(string(raw), `"commit"`) {
		t.Fatalf("an absent commit must be absent, not empty: %s", raw)
	}

	// Deleting the run deletes what it exported.
	if err := s.DeleteRunData(ctx, sc, runID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	for _, name := range []string{"git-result.json", "git-result.bundle"} {
		if _, found, err := s.GetRunArtifact(ctx, sc, runID, name); err != nil || found {
			t.Fatalf("%s after delete: found=%v err=%v", name, found, err)
		}
	}
	if _, err := s.GetRun(ctx, sc, runID); err == nil {
		t.Fatal("the run row should be gone")
	}

	// And so does deleting the agent.
	keep := uuid.NewString()
	if err := s.SaveRun(ctx, sc, Run{ID: keep, AgentName: sc.AgentName, Trigger: "api", Phase: RunPhaseSucceeded, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveRunArtifact(ctx, sc, RunArtifact{RunID: keep, Name: "git-result.json", Digest: "d", MediaType: "application/json", Size: 2, Data: []byte("{}"), CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteAgentData(ctx, sc, sc.AgentName); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := s.GetRunArtifact(ctx, sc, keep, "git-result.json"); found {
		t.Fatal("deleting the agent must delete its runs' artifacts")
	}
}

func TestMemoryStore_RunArtifacts(t *testing.T) {
	testRunArtifacts(t, NewMemoryStore(), testScope())
}

func TestPostgres_RunArtifacts(t *testing.T) {
	ps := openTestPostgres(t)
	testRunArtifacts(t, ps, pgScope(t, ps))
}

// A tenant mapping learned after a run was filed under the cluster-keyed
// fallback scope carries its artifacts along with it.
func TestMemoryStore_RunArtifactsFollowScopeMigration(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryStore()
	now := time.Now().UTC()
	fallback := Scope{OrgUUID: UnmappedOrg, WorkspaceUUID: "cluster-1", AgentName: "helper", ClusterID: "cluster-1"}
	if err := s.SaveRun(ctx, fallback, Run{ID: "r1", AgentName: "helper", Trigger: "api", Phase: RunPhaseSucceeded, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveRunArtifact(ctx, fallback, RunArtifact{RunID: "r1", Name: "git-result.json", Digest: "d", MediaType: "application/json", Size: 2, Data: []byte("{}"), CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveTenantRef(ctx, "cluster-1", TenantRef{OrgUUID: "org", WorkspaceUUID: "ws", UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	mapped := Scope{OrgUUID: "org", WorkspaceUUID: "ws", AgentName: "helper"}
	if _, found, err := s.GetRunArtifact(ctx, mapped, "r1", "git-result.json"); err != nil || !found {
		t.Fatalf("after migration: found=%v err=%v", found, err)
	}
}
