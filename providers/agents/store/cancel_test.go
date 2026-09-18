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
	"testing"
	"time"
)

// A cancel request must survive a later SaveRun from a copy that predates it:
// the checkpoint recorder reads the row, does work, and writes it back, and
// that write must not un-cancel the run.
func TestMemoryRequestCancelSurvivesStaleSave(t *testing.T) {
	ctx := context.Background()
	m := NewMemoryStore()
	scope := Scope{OrgUUID: "o", WorkspaceUUID: "w", AgentName: "a"}
	now := time.Now().UTC()
	run := Run{ID: "r1", AgentName: "a", Phase: RunPhaseRunning, CreatedAt: now, UpdatedAt: now}
	if err := m.SaveRun(ctx, scope, run); err != nil {
		t.Fatal(err)
	}
	stale, _ := m.GetRun(ctx, scope, "r1")

	if err := m.RequestCancel(ctx, scope, "r1", now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	got, _ := m.GetRun(ctx, scope, "r1")
	if !got.CancelRequested || got.CancelRequestedAt == nil {
		t.Fatalf("cancel should be recorded, got %+v", got)
	}
	first := *got.CancelRequestedAt

	// Repeat keeps the first timestamp.
	if err := m.RequestCancel(ctx, scope, "r1", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	got, _ = m.GetRun(ctx, scope, "r1")
	if !got.CancelRequestedAt.Equal(first) {
		t.Fatalf("a repeated cancel must keep the first request time, got %v want %v", got.CancelRequestedAt, first)
	}

	// A save from the pre-cancel copy must not clear it.
	stale.Checkpoint = []byte(`{"iter":2}`)
	if err := m.SaveRun(ctx, scope, stale); err != nil {
		t.Fatal(err)
	}
	got, _ = m.GetRun(ctx, scope, "r1")
	if !got.CancelRequested || len(got.Checkpoint) == 0 {
		t.Fatalf("stale save must keep the cancel flag and land its checkpoint, got %+v", got)
	}
}

func TestMemoryRequestCancelLeavesTerminalRunsAlone(t *testing.T) {
	ctx := context.Background()
	m := NewMemoryStore()
	scope := Scope{OrgUUID: "o", WorkspaceUUID: "w", AgentName: "a"}
	now := time.Now().UTC()
	if err := m.SaveRun(ctx, scope, Run{ID: "done", AgentName: "a", Phase: RunPhaseSucceeded, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := m.RequestCancel(ctx, scope, "done", now); err != nil {
		t.Fatalf("cancelling a finished run is a no-op, not an error: %v", err)
	}
	if got, _ := m.GetRun(ctx, scope, "done"); got.CancelRequested {
		t.Fatal("a finished run must not be flagged")
	}
	if err := m.RequestCancel(ctx, scope, "missing", now); err == nil {
		t.Fatal("an unknown run must be reported")
	}
}
