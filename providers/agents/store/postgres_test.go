// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package store

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
)

// openTestPostgres skips unless AGENTS_TEST_POSTGRES_DSN points at a disposable
// database (e.g. the agents-db-up dev container).
func openTestPostgres(t *testing.T) *PostgresStore {
	t.Helper()
	dsn := os.Getenv("AGENTS_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("AGENTS_TEST_POSTGRES_DSN not set — skipping Postgres store tests")
	}
	ps, err := OpenPostgres(context.Background(), dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = ps.Close() })
	if err := ps.EnsureSchema(context.Background()); err != nil {
		t.Fatalf("schema: %v", err)
	}
	return ps
}

// pgScope returns a unique scope per test run so tests don't collide on a
// shared database.
func pgScope() Scope {
	return Scope{OrgUUID: "org-" + uuid.NewString()[:8], WorkspaceUUID: "ws-" + uuid.NewString()[:8], AgentName: "helper"}
}

func TestPostgres_MessagesRoundTripAndPagination(t *testing.T) {
	ps := openTestPostgres(t)
	ctx := context.Background()
	sc := pgScope()
	base := time.Now().UTC().Truncate(time.Millisecond)

	for i := range 5 {
		if err := ps.AppendMessage(ctx, sc, Message{
			ID: uuid.NewString(), AgentName: sc.AgentName, SessionID: "sess",
			Role: "user", Content: "hi", CreatedAt: base.Add(time.Duration(i) * time.Second),
		}); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	recent, err := ps.LoadRecentMessages(ctx, sc, "sess", 3)
	if err != nil || len(recent) != 3 {
		t.Fatalf("recent: %v n=%d", err, len(recent))
	}
	if !recent[0].CreatedAt.Before(recent[2].CreatedAt) {
		t.Fatal("recent not chronological")
	}
	seen, cursor := 0, ""
	for range 10 {
		page, err := ps.ListMessages(ctx, sc, "sess", 2, cursor)
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		seen += len(page.Items)
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}
	if seen != 5 {
		t.Fatalf("paginated %d, want 5", seen)
	}
}

func TestPostgres_RunSaveClaimAndUsage(t *testing.T) {
	ps := openTestPostgres(t)
	ctx := context.Background()
	sc := pgScope()
	now := time.Now().UTC().Truncate(time.Millisecond)

	runID := uuid.NewString()
	if err := ps.SaveRun(ctx, sc, Run{
		ID: runID, AgentName: sc.AgentName, Trigger: "schedule", Phase: RunPhasePending,
		Input: "task", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("save: %v", err)
	}
	if _, err := ps.ClaimRun(ctx, sc, runID, "r1", now); err != nil {
		t.Fatalf("first claim: %v", err)
	}
	if _, err := ps.ClaimRun(ctx, sc, runID, "r2", now); err == nil {
		t.Fatal("second claim should fail")
	}
	runs, err := ps.ListRuns(ctx, sc, 10)
	if err != nil || len(runs) != 1 || runs[0].Phase != RunPhaseRunning {
		t.Fatalf("list runs: %v %+v", err, runs)
	}

	win := 30 * 24 * time.Hour
	if _, err := ps.AddUsage(ctx, sc, sc.AgentName, 100, 50, 2000, now, win); err != nil {
		t.Fatalf("usage: %v", err)
	}
	u, err := ps.AddUsage(ctx, sc, sc.AgentName, 10, 5, 300, now, win)
	if err != nil || u.InputTokens != 110 || u.USDMicros != 2300 {
		t.Fatalf("usage rollup: %v %+v", err, u)
	}
}

func TestPostgres_InboxMemoryTenantRefTeardown(t *testing.T) {
	ps := openTestPostgres(t)
	ctx := context.Background()
	sc := pgScope()
	now := time.Now().UTC().Truncate(time.Millisecond)

	// Inbox add + resolve with payload round-trip.
	itemID := uuid.NewString()
	if err := ps.AddInboxItem(ctx, sc, InboxItem{
		ID: itemID, AgentName: sc.AgentName, Kind: InboxKindApproval, State: InboxStatePending,
		Prompt: "allow?", Payload: map[string]any{"tool": "github__merge"}, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("inbox add: %v", err)
	}
	pending, err := ps.ListInbox(ctx, Scope{OrgUUID: sc.OrgUUID, WorkspaceUUID: sc.WorkspaceUUID}, InboxStatePending)
	if err != nil || len(pending) != 1 || pending[0].Payload["tool"] != "github__merge" {
		t.Fatalf("inbox list: %v %+v", err, pending)
	}
	if _, err := ps.ResolveInboxItem(ctx, sc, itemID, InboxStateApproved, "ok", now); err != nil {
		t.Fatalf("resolve: %v", err)
	}

	// Memory upsert + list.
	memID := uuid.NewString()
	if err := ps.PutMemory(ctx, sc, Memory{ID: memID, AgentName: sc.AgentName, Title: "t", Body: "b", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatalf("memory: %v", err)
	}
	mems, err := ps.ListMemories(ctx, sc, 10)
	if err != nil || len(mems) != 1 {
		t.Fatalf("memories: %v %d", err, len(mems))
	}

	// Tool call + tenant ref.
	if err := ps.AppendToolCall(ctx, sc, ToolCall{ID: uuid.NewString(), AgentName: sc.AgentName, Tool: "web_fetch", Outcome: "ok", CreatedAt: now}); err != nil {
		t.Fatalf("tool call: %v", err)
	}
	cluster := "cl-" + uuid.NewString()[:8]
	if err := ps.SaveTenantRef(ctx, cluster, TenantRef{OrgUUID: sc.OrgUUID, WorkspaceUUID: sc.WorkspaceUUID, UpdatedAt: now}); err != nil {
		t.Fatalf("tenant ref: %v", err)
	}
	ref, ok, err := ps.GetTenantRef(ctx, cluster)
	if err != nil || !ok || ref.OrgUUID != sc.OrgUUID {
		t.Fatalf("tenant ref get: %v ok=%v %+v", err, ok, ref)
	}

	// Teardown wipes the agent's rows.
	if err := ps.DeleteAgentData(ctx, sc, sc.AgentName); err != nil {
		t.Fatalf("teardown: %v", err)
	}
	mems, _ = ps.ListMemories(ctx, sc, 10)
	if len(mems) != 0 {
		t.Fatalf("memories not deleted: %d", len(mems))
	}
}
