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
	"slices"
	"sync"
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

// pgScope returns a unique scope per test and deletes everything it wrote when
// the test ends.
//
// The cleanup is not hygiene, it is correctness of the environment: the DSN
// commonly points at the developer's live dev database (that is the whole point
// of AGENTS_TEST_POSTGRES_DSN), so rows left behind show up as phantom agents and
// stranded runs in the portal — and the recovery sweep then dutifully marks them
// Failed, which looks exactly like a real outage.
func pgScope(t *testing.T, ps *PostgresStore) Scope {
	t.Helper()
	sc := Scope{OrgUUID: "org-" + uuid.NewString()[:8], WorkspaceUUID: "ws-" + uuid.NewString()[:8], AgentName: "helper"}
	t.Cleanup(func() { purgeTestScope(t, ps, sc.OrgUUID) })
	return sc
}

// purgeTestScope removes every row an org wrote. Keyed on org_uuid alone so it
// also catches the extra agents and workspaces a test invents under its own org.
func purgeTestScope(t *testing.T, ps *PostgresStore, orgUUID string) {
	t.Helper()
	ctx := context.Background()
	for _, table := range []string{
		"agents_messages", "agents_runs", "agents_memories", "agents_inbox",
		"agents_tool_calls", "agents_usage", "agents_session_summaries", "agents_tenants",
	} {
		if _, err := ps.db.ExecContext(ctx, "DELETE FROM "+table+" WHERE org_uuid=$1", orgUUID); err != nil {
			t.Logf("cleanup: %s for %s: %v", table, orgUUID, err)
		}
	}
}

func TestPostgres_MessagesRoundTripAndPagination(t *testing.T) {
	ps := openTestPostgres(t)
	ctx := context.Background()
	sc := pgScope(t, ps)
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
	sc := pgScope(t, ps)
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

	// The run's answer and its sources are read back from the run record — the
	// contract a programmatic caller (or a worker's parent) depends on.
	stored := runs[0]
	stored.Phase = RunPhaseSucceeded
	stored.Output = "the answer"
	stored.Sources = []string{"https://a.example/x", "https://b.example/y"}
	stored.UpdatedAt = now
	if err := ps.SaveRun(ctx, sc, stored); err != nil {
		t.Fatalf("save with output: %v", err)
	}
	got, err := ps.GetRun(ctx, sc, runID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Output != "the answer" {
		t.Fatalf("output = %q, want it persisted on the run", got.Output)
	}
	if !slices.Equal(got.Sources, []string{"https://a.example/x", "https://b.example/y"}) {
		t.Fatalf("sources = %v", got.Sources)
	}
	// A run with no sources must round-trip as nil, not as a JSON null that
	// later fails to decode.
	stored.Sources = nil
	if err := ps.SaveRun(ctx, sc, stored); err != nil {
		t.Fatalf("save without sources: %v", err)
	}
	if got, err = ps.GetRun(ctx, sc, runID); err != nil || got.Sources != nil {
		t.Fatalf("sources should clear to nil: %v %v", err, got.Sources)
	}
}

func TestPostgres_RunFinalizeIsExclusive(t *testing.T) {
	ps := openTestPostgres(t)
	ctx := context.Background()
	sc := pgScope(t, ps)
	now := time.Now().UTC().Truncate(time.Millisecond)
	runID := uuid.NewString()
	if err := ps.SaveRun(ctx, sc, Run{
		ID: runID, AgentName: sc.AgentName, Trigger: "api", Phase: RunPhasePending,
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("save: %v", err)
	}

	start := make(chan struct{})
	wins := make(chan bool, 2)
	var wg sync.WaitGroup
	for _, candidate := range []Run{
		{ID: runID, AgentName: sc.AgentName, Trigger: "api", Phase: RunPhaseFailed, Message: "first"},
		{ID: runID, AgentName: sc.AgentName, Trigger: "api", Phase: RunPhaseAborted, Message: "second"},
	} {
		candidate.CreatedAt, candidate.UpdatedAt = now, now
		candidate.FinishedAt = &now
		wg.Add(1)
		go func(run Run) {
			defer wg.Done()
			<-start
			won, err := ps.FinalizeRun(ctx, sc, run)
			if err != nil {
				t.Errorf("finalize: %v", err)
			}
			wins <- won
		}(candidate)
	}
	close(start)
	wg.Wait()
	close(wins)
	var count int
	for won := range wins {
		if won {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("finalize winners = %d, want one", count)
	}
	got, err := ps.GetRun(ctx, sc, runID)
	if err != nil {
		t.Fatal(err)
	}
	if !terminalRunPhase(got.Phase) || got.Message == "" {
		t.Fatalf("stored run = %#v, want one terminal winner", got)
	}
	winnerMessage := got.Message
	if won, err := ps.FinalizeRun(ctx, sc, Run{
		ID: runID, AgentName: sc.AgentName, Trigger: "api", Phase: RunPhaseFailed,
		Message: "late", CreatedAt: now, UpdatedAt: now, FinishedAt: &now,
	}); err != nil || won {
		t.Fatalf("late finalize = (%v, %v), want (false, nil)", won, err)
	}
	got, err = ps.GetRun(ctx, sc, runID)
	if err != nil || got.Message != winnerMessage {
		t.Fatalf("late finalize changed winner: err=%v run=%#v", err, got)
	}
}

func TestPostgres_InboxMemoryTenantRefTeardown(t *testing.T) {
	ps := openTestPostgres(t)
	ctx := context.Background()
	sc := pgScope(t, ps)
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

// Compaction and recovery both add cross-cutting store surface: a summary keyed
// per session, a reverse tenant lookup, and the one query that ignores Scope.
func TestPostgres_CompactionAndRecovery(t *testing.T) {
	ps := openTestPostgres(t)
	ctx := context.Background()
	sc := pgScope(t, ps)
	now := time.Now().UTC().Truncate(time.Millisecond)

	t.Run("session summary upserts and clears with the session", func(t *testing.T) {
		sess := "chat-" + uuid.NewString()
		if _, ok, err := ps.GetSessionSummary(ctx, sc, sess); err != nil || ok {
			t.Fatalf("expected no summary yet: ok=%v err=%v", ok, err)
		}
		first := SessionSummary{
			SessionID: sess, Summary: "earlier talk", ThroughAt: now.Add(-time.Hour),
			MessageCount: 6, CreatedAt: now, UpdatedAt: now,
		}
		if err := ps.PutSessionSummary(ctx, sc, first); err != nil {
			t.Fatal(err)
		}
		got, ok, err := ps.GetSessionSummary(ctx, sc, sess)
		if err != nil || !ok {
			t.Fatalf("get: ok=%v err=%v", ok, err)
		}
		if got.Summary != "earlier talk" || got.MessageCount != 6 || !got.ThroughAt.Equal(first.ThroughAt) {
			t.Fatalf("round-trip mismatch: %+v", got)
		}

		// Compacting again replaces the row rather than adding a second one.
		second := first
		second.Summary, second.MessageCount, second.ThroughAt = "merged record", 14, now
		if err := ps.PutSessionSummary(ctx, sc, second); err != nil {
			t.Fatal(err)
		}
		got, _, err = ps.GetSessionSummary(ctx, sc, sess)
		if err != nil {
			t.Fatal(err)
		}
		if got.Summary != "merged record" || got.MessageCount != 14 {
			t.Fatalf("upsert did not replace the row: %+v", got)
		}

		// "/new" wipes the transcript, so the summary standing in for it must go.
		if err := ps.DeleteSession(ctx, sc, sess); err != nil {
			t.Fatal(err)
		}
		if _, ok, _ := ps.GetSessionSummary(ctx, sc, sess); ok {
			t.Fatal("DeleteSession must clear the summary too")
		}
	})

	t.Run("reverse tenant lookup finds the cluster for a workspace", func(t *testing.T) {
		cluster := "cluster-" + uuid.NewString()
		if err := ps.SaveTenantRef(ctx, cluster, TenantRef{
			OrgUUID: sc.OrgUUID, WorkspaceUUID: sc.WorkspaceUUID, UpdatedAt: now,
		}); err != nil {
			t.Fatal(err)
		}
		got, ok, err := ps.FindClusterForScope(ctx, sc.OrgUUID, sc.WorkspaceUUID)
		if err != nil || !ok {
			t.Fatalf("find: ok=%v err=%v", ok, err)
		}
		if got == "" {
			t.Fatal("expected a cluster id")
		}
		if _, ok, _ := ps.FindClusterForScope(ctx, "nope", "nope"); ok {
			t.Fatal("an unmapped workspace must report not-found, not a stale cluster")
		}
	})

	t.Run("idempotency keys are unique per agent and ignore keyless runs", func(t *testing.T) {
		key := "k-" + uuid.NewString()
		first := uuid.NewString()
		if err := ps.SaveRun(ctx, sc, Run{
			ID: first, AgentName: sc.AgentName, Phase: RunPhaseRunning, IdempotencyKey: key,
			CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			t.Fatal(err)
		}
		got, found, err := ps.FindRunByIdempotencyKey(ctx, sc, key)
		if err != nil || !found || got.ID != first {
			t.Fatalf("lookup: found=%v id=%q err=%v", found, got.ID, err)
		}

		// The partial unique index refuses a second run under the same key, so a
		// racing retry cannot create duplicate work even if it passes the read check.
		if err := ps.SaveRun(ctx, sc, Run{
			ID: uuid.NewString(), AgentName: sc.AgentName, Phase: RunPhaseRunning, IdempotencyKey: key,
			CreatedAt: now, UpdatedAt: now,
		}); err == nil {
			t.Fatal("a second run with the same key must be rejected by the unique index")
		}

		// Keyless runs are unconstrained — the index is partial, or every run
		// without a key would collide with every other.
		for range 3 {
			if err := ps.SaveRun(ctx, sc, Run{
				ID: uuid.NewString(), AgentName: sc.AgentName, Phase: RunPhaseRunning,
				CreatedAt: now, UpdatedAt: now,
			}); err != nil {
				t.Fatalf("keyless run rejected: %v", err)
			}
		}

		// Another agent may reuse the key for its own work.
		other := Scope{OrgUUID: sc.OrgUUID, WorkspaceUUID: sc.WorkspaceUUID, AgentName: "other-" + uuid.NewString()[:6]}
		if err := ps.SaveRun(ctx, other, Run{
			ID: uuid.NewString(), AgentName: other.AgentName, Phase: RunPhaseRunning, IdempotencyKey: key,
			CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			t.Fatalf("the same key under a different agent must be allowed: %v", err)
		}
		if _, found, _ := ps.FindRunByIdempotencyKey(ctx, sc, key); !found {
			t.Fatal("the original run should still be the match for its own agent")
		}
	})

	t.Run("unfinished runs come back with their scope, oldest first", func(t *testing.T) {
		old, newer := uuid.NewString(), uuid.NewString()
		terminal := uuid.NewString()
		mk := func(id string, phase RunPhase, age time.Duration) {
			at := now.Add(-age)
			if err := ps.SaveRun(ctx, sc, Run{
				ID: id, AgentName: sc.AgentName, Trigger: "chat", Phase: phase,
				CreatedAt: at, UpdatedAt: at,
			}); err != nil {
				t.Fatal(err)
			}
		}
		mk(old, RunPhaseRunning, 5*time.Hour)
		mk(newer, RunPhaseRunning, 4*time.Hour)
		mk(terminal, RunPhaseSucceeded, 5*time.Hour)

		got, err := ps.ListUnfinishedRuns(ctx,
			[]RunPhase{RunPhaseRunning, RunPhasePending}, now.Add(-3*time.Hour), 100)
		if err != nil {
			t.Fatal(err)
		}
		var seen []string
		for _, sr := range got {
			if sr.Run.ID == old || sr.Run.ID == newer {
				seen = append(seen, sr.Run.ID)
				if sr.Scope.OrgUUID != sc.OrgUUID || sr.Scope.WorkspaceUUID != sc.WorkspaceUUID {
					t.Fatalf("run %s lost its scope: %+v", sr.Run.ID, sr.Scope)
				}
				if sr.Scope.AgentName != sc.AgentName {
					t.Fatalf("run %s scope should name its agent, got %q", sr.Run.ID, sr.Scope.AgentName)
				}
			}
			if sr.Run.ID == terminal {
				t.Fatal("a Succeeded run is not unfinished")
			}
			if sr.Run.Phase != RunPhaseRunning && sr.Run.Phase != RunPhasePending {
				t.Fatalf("phase filter leaked %s", sr.Run.Phase)
			}
		}
		if len(seen) != 2 || seen[0] != old {
			t.Fatalf("got %v, want the older run first", seen)
		}
	})
}
