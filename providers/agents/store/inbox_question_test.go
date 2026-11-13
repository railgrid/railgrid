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
	"errors"
	"testing"
	"time"
)

func TestMemoryQuestionResolution(t *testing.T) {
	testQuestionResolution(t, NewMemoryStore(), Scope{OrgUUID: "org", WorkspaceUUID: "workspace", AgentName: "agent"})
}

func TestPostgresQuestionResolution(t *testing.T) {
	st := openTestPostgres(t)
	testQuestionResolution(t, st, pgScope(t, st))
}

func testQuestionResolution(t *testing.T, st Store, scope Scope) {
	t.Helper()
	ctx, now := t.Context(), time.Now().UTC()
	run := Run{
		ID: "run", AgentName: scope.AgentName, Phase: RunPhasePendingApproval,
		CreatedAt: now, UpdatedAt: now, Checkpoint: []byte(`{"inboxID":"question"}`),
	}
	if err := st.SaveRun(ctx, scope, run); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ClaimRun(ctx, scope, run.ID, "stale", now, "old-question"); !errors.Is(err, ErrRunClaimConflict) {
		t.Fatalf("claim on a different question: %v", err)
	}
	if claimed, err := st.ClaimRun(ctx, scope, run.ID, "winner", now, "question"); err != nil || claimed.Phase != RunPhaseRunning {
		t.Fatalf("claim on the parked question: phase=%s err=%v", claimed.Phase, err)
	}
	if _, err := st.ClaimRun(ctx, scope, run.ID, "loser", now, "question"); !errors.Is(err, ErrRunClaimConflict) {
		t.Fatalf("duplicate claim: %v", err)
	}

	for _, item := range []InboxItem{
		{ID: "pending", State: InboxStatePending},
		{ID: "dismissed", State: InboxStateDenied, RunID: run.ID},
		{ID: "standalone", State: InboxStateDenied},
	} {
		t.Run(item.ID, func(t *testing.T) {
			item.AgentName, item.Kind = scope.AgentName, InboxKindQuestion
			item.CreatedAt, item.UpdatedAt = now, now
			item.Payload = map[string]any{"original": "preserved"}
			if err := st.AddInboxItem(ctx, scope, item); err != nil {
				t.Fatal(err)
			}
			answer := InboxResolution{QuestionPayload: map[string]any{"answers": map[string]any{"q1": "yes"}}}
			if item.State == InboxStateDenied {
				cleanup := answer
				cleanup.PendingOnly = true
				if _, err := st.ResolveInboxItem(ctx, scope, item.ID, InboxStateAnswered, "cleanup", now, cleanup); !errors.Is(err, ErrInboxAlreadyResolved) {
					t.Fatalf("pending-only update to dismissed question: %v", err)
				}
			}
			got, err := st.ResolveInboxItem(ctx, scope, item.ID, InboxStateAnswered, "first answer", now, answer)
			if item.ID == "standalone" {
				if !errors.Is(err, ErrInboxAlreadyResolved) {
					t.Fatalf("revival of dismissed standalone question: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			answers, _ := got.Payload["answers"].(map[string]any)
			if got.State != InboxStateAnswered || got.Response != "first answer" || got.Payload["original"] != "preserved" || answers["q1"] != "yes" {
				t.Fatalf("unexpected resolved question: %+v", got)
			}
			if _, err := st.ResolveInboxItem(ctx, scope, item.ID, InboxStateAnswered, "second answer", now, answer); !errors.Is(err, ErrInboxAlreadyResolved) {
				t.Fatalf("duplicate answer: %v", err)
			}
		})
	}
}

func TestAddInboxItemForParkedRunFencesTerminalRun(t *testing.T) {
	ctx := t.Context()
	scope := Scope{OrgUUID: "org", WorkspaceUUID: "ws", AgentName: "coder"}
	st := NewMemoryStore()
	now := time.Now().UTC()
	item := InboxItem{ID: "inbox", AgentName: "coder", RunID: "run", Kind: InboxKindQuestion, State: InboxStatePending}
	if err := st.SaveRun(ctx, scope, Run{ID: "run", AgentName: "coder", Phase: RunPhasePendingApproval, Checkpoint: []byte(`{"inboxID":"inbox"}`), CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := st.AddInboxItemForParkedRun(ctx, scope, "run", item); err != nil {
		t.Fatalf("insert while parked: %v", err)
	}
	if err := st.SaveRun(ctx, scope, Run{ID: "run", AgentName: "coder", Phase: RunPhaseFailed, Checkpoint: []byte(`{"inboxID":"inbox"}`), CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := st.AddInboxItemForParkedRun(ctx, scope, "run", item); !errors.Is(err, ErrRunNotParked) {
		t.Fatalf("terminal/missing run insert error=%v, want ErrRunNotParked", err)
	}
}

func TestAddInboxItemForParkedRunPreservesExistingResolutionAndScope(t *testing.T) {
	ctx := t.Context()
	scope := Scope{OrgUUID: "org", WorkspaceUUID: "ws", AgentName: "coder"}
	st := NewMemoryStore()
	now := time.Now().UTC()
	item := InboxItem{ID: "inbox", AgentName: "coder", RunID: "run", Kind: InboxKindQuestion, State: InboxStatePending}
	if err := st.SaveRun(ctx, scope, Run{ID: "run", AgentName: "coder", Phase: RunPhasePendingApproval, Checkpoint: []byte(`{"inboxID":"inbox"}`), CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	resolved := item
	resolved.State, resolved.Response = InboxStateAnswered, "continue"
	if err := st.AddInboxItem(ctx, Scope{OrgUUID: scope.OrgUUID, WorkspaceUUID: scope.WorkspaceUUID}, resolved); err != nil {
		t.Fatal(err)
	}
	if err := st.AddInboxItemForParkedRun(ctx, scope, "run", item); !errors.Is(err, ErrInboxItemExists) {
		t.Fatalf("duplicate insert error=%v, want ErrInboxItemExists", err)
	}
	got, err := st.GetInboxItem(ctx, Scope{OrgUUID: scope.OrgUUID, WorkspaceUUID: scope.WorkspaceUUID}, item.ID)
	if err != nil || got.State != InboxStateAnswered || got.Response != "continue" {
		t.Fatalf("existing resolution overwritten: %+v err=%v", got, err)
	}
	wrongScope := item
	wrongScope.AgentName = "other"
	if err := st.AddInboxItemForParkedRun(ctx, scope, "run", wrongScope); err == nil {
		t.Fatal("accepted inbox item with a mismatched agent scope")
	}
}

func TestPostgresAddInboxItemForParkedRunFencesTerminalRun(t *testing.T) {
	st := openTestPostgres(t)
	scope := pgScope(t, st)
	ctx, now := t.Context(), time.Now().UTC()
	item := InboxItem{ID: "inbox-repair", AgentName: scope.AgentName, RunID: "run-repair", Kind: InboxKindQuestion, State: InboxStatePending}
	run := Run{ID: item.RunID, AgentName: scope.AgentName, Phase: RunPhasePendingApproval, Checkpoint: []byte(`{"inboxID":"inbox-repair"}`), CreatedAt: now, UpdatedAt: now}
	if err := st.SaveRun(ctx, scope, run); err != nil {
		t.Fatal(err)
	}
	if err := st.AddInboxItemForParkedRun(ctx, scope, run.ID, item); err != nil {
		t.Fatalf("insert while parked: %v", err)
	}
	run.Phase = RunPhaseFailed
	if err := st.SaveRun(ctx, scope, run); err != nil {
		t.Fatal(err)
	}
	if err := st.AddInboxItemForParkedRun(ctx, scope, run.ID, item); !errors.Is(err, ErrRunNotParked) {
		t.Fatalf("terminal insert error=%v, want ErrRunNotParked", err)
	}
}

func TestMemoryAddInboxItemForParkedRunFencesCancellation(t *testing.T) {
	scope := Scope{OrgUUID: "org", WorkspaceUUID: "ws", AgentName: "coder"}
	st := NewMemoryStore()
	now := time.Now().UTC()
	item := InboxItem{ID: "inbox", AgentName: scope.AgentName, RunID: "run", Kind: InboxKindQuestion, State: InboxStatePending}
	run := Run{ID: item.RunID, AgentName: scope.AgentName, Phase: RunPhasePendingApproval,
		Checkpoint: []byte(`{"inboxID":"inbox"}`), CreatedAt: now, UpdatedAt: now, CancelRequested: true}
	if err := st.SaveRun(t.Context(), scope, run); err != nil {
		t.Fatal(err)
	}
	if err := st.AddInboxItemForParkedRun(t.Context(), scope, run.ID, item); !errors.Is(err, ErrRunNotParked) {
		t.Fatalf("insert after cancellation request: %v, want ErrRunNotParked", err)
	}
}

func TestPostgresAddInboxItemForParkedRunSerializesWithFinish(t *testing.T) {
	st := openTestPostgres(t)
	scope := pgScope(t, st)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	now := time.Now().UTC()
	item := InboxItem{ID: "inbox-repair", AgentName: scope.AgentName, RunID: "run-repair", Kind: InboxKindQuestion, State: InboxStatePending}
	run := Run{ID: item.RunID, AgentName: scope.AgentName, Phase: RunPhasePendingApproval,
		Checkpoint: []byte(`{"inboxID":"inbox-repair"}`), CreatedAt: now, UpdatedAt: now}
	if err := st.SaveRun(ctx, scope, run); err != nil {
		t.Fatal(err)
	}

	// Keep the terminal update uncommitted until repair has attempted to lock
	// the run row. A plain SELECT would see the old parked version and insert.
	finish, err := st.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = finish.Rollback() }()
	var writerPID int
	if err := finish.QueryRowContext(ctx, "SELECT pg_backend_pid()").Scan(&writerPID); err != nil {
		t.Fatal(err)
	}
	if _, err := finish.ExecContext(ctx, `UPDATE agents_runs SET phase=$1
		WHERE org_uuid=$2 AND workspace_uuid=$3 AND agent_name=$4 AND id=$5`,
		string(RunPhaseFailed), scope.OrgUUID, scope.WorkspaceUUID, scope.AgentName, run.ID); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() { result <- st.AddInboxItemForParkedRun(ctx, scope, run.ID, item) }()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case err := <-result:
			t.Fatalf("repair returned before the finish transaction committed: %v", err)
		case <-ctx.Done():
			t.Fatal("repair did not wait on the run row lock")
		case <-tick.C:
			var waiting bool
			if err := st.db.QueryRowContext(ctx, `SELECT EXISTS (
				SELECT 1 FROM pg_stat_activity WHERE $1 = ANY(pg_blocking_pids(pid))
			)`, writerPID).Scan(&waiting); err != nil {
				t.Fatal(err)
			}
			if waiting {
				if err := finish.Commit(); err != nil {
					t.Fatal(err)
				}
				if err := <-result; !errors.Is(err, ErrRunNotParked) {
					t.Fatalf("repair after terminal update: %v, want ErrRunNotParked", err)
				}
				return
			}
		}
	}
}

func TestAddInboxItemForParkedRunValidatesBeforeRunLookup(t *testing.T) {
	t.Run("memory", func(t *testing.T) {
		assertParkedInboxInputValidation(t, NewMemoryStore(), Scope{OrgUUID: "org", WorkspaceUUID: "ws", AgentName: "coder"})
	})
	t.Run("postgres", func(t *testing.T) {
		st := openTestPostgres(t)
		assertParkedInboxInputValidation(t, st, pgScope(t, st))
	})
}

func assertParkedInboxInputValidation(t *testing.T, st Store, scope Scope) {
	t.Helper()
	for _, tc := range []struct {
		item InboxItem
		want string
	}{
		{InboxItem{AgentName: scope.AgentName, RunID: "missing"}, "inbox item ID is required"},
		{InboxItem{ID: "inbox", AgentName: "other", RunID: "missing"}, "inbox item does not match parked run scope"},
	} {
		err := st.AddInboxItemForParkedRun(t.Context(), scope, "missing", tc.item)
		if err == nil || err.Error() != tc.want {
			t.Errorf("invalid inbox item: got %v, want %q", err, tc.want)
		}
	}
}
