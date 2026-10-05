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
	"database/sql"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestMemoryStoreTenantMappingMigratesSplitHistory(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryStore()
	clusterID := "cluster-scope-migration"
	fallback := Scope{OrgUUID: UnmappedOrg, WorkspaceUUID: clusterID, AgentName: "coder", ClusterID: clusterID}
	mapped := Scope{OrgUUID: "org-scope-migration", WorkspaceUUID: "workspace-scope-migration", AgentName: "coder", ClusterID: clusterID}
	now := time.Date(2026, 10, 5, 10, 0, 0, 0, time.UTC)

	for _, entry := range []struct {
		scope Scope
		id    string
		text  string
		at    time.Time
	}{
		{fallback, "legacy-user", "continue the incident review", now},
		{mapped, "mapped-user", "include the deployment diff", now.Add(time.Second)},
	} {
		if err := s.AppendMessage(ctx, entry.scope, Message{
			ID: entry.id, AgentName: "coder", SessionID: "continuity", Role: "user", Content: entry.text, CreatedAt: entry.at,
		}); err != nil {
			t.Fatalf("append %s transcript: %v", entry.id, err)
		}
	}
	legacyCheckpoint := &SessionCheckpoint{Version: 1, ThroughSequence: 1, ReplacementHistory: []SessionCheckpointMessage{{Role: "user", Content: "legacy prefix"}}}
	mappedCheckpoint := &SessionCheckpoint{Version: 1, ThroughSequence: 2, ReplacementHistory: []SessionCheckpointMessage{{Role: "user", Content: "merged prefix"}}}
	if err := s.PutSessionSummary(ctx, fallback, SessionSummary{
		SessionID: "continuity", Summary: "legacy summary", ThroughAt: now, MessageCount: 5,
		CreatedAt: now, UpdatedAt: now.Add(2 * time.Second), Checkpoint: legacyCheckpoint,
	}); err != nil {
		t.Fatalf("put legacy summary: %v", err)
	}
	if err := s.PutSessionSummary(ctx, mapped, SessionSummary{
		SessionID: "continuity", Summary: "mapped summary", ThroughAt: now.Add(time.Second), MessageCount: 3,
		CreatedAt: now.Add(time.Second), UpdatedAt: now.Add(3 * time.Second), Checkpoint: mappedCheckpoint,
	}); err != nil {
		t.Fatalf("put mapped summary: %v", err)
	}

	for i := 0; i < 4; i++ {
		if _, err := s.NextHarnessTurn(ctx, fallback, "continuity", now.Add(time.Duration(i)*time.Second)); err != nil {
			t.Fatalf("allocate legacy harness turn %d: %v", i, err)
		}
	}
	if err := s.PutHarnessSession(ctx, fallback, HarnessSession{
		SessionID: "continuity", HarnessSessionID: "runner-thread-legacy", BackendKey: "edge-a", Turns: 4,
		ObservedEpoch: 3, UpdatedAt: now.Add(4 * time.Second),
	}); err != nil {
		t.Fatalf("save legacy harness receipt: %v", err)
	}
	for i := 0; i < 2; i++ {
		if _, err := s.NextHarnessTurn(ctx, mapped, "continuity", now.Add(time.Duration(i+5)*time.Second)); err != nil {
			t.Fatalf("allocate mapped harness turn %d: %v", i, err)
		}
	}
	if err := s.PutHarnessSession(ctx, mapped, HarnessSession{
		SessionID: "continuity", HarnessSessionID: "runner-thread-mapped", BackendKey: "edge-b", Turns: 2,
		ObservedEpoch: 2, UpdatedAt: now.Add(8 * time.Second),
	}); err != nil {
		t.Fatalf("save mapped harness receipt: %v", err)
	}

	legacyRun := Run{ID: "legacy-run", AgentName: "coder", SessionID: "continuity", Trigger: "chat", Phase: RunPhaseRunning,
		Input: "legacy request", IdempotencyKey: "same-request", Checkpoint: json.RawMessage(`{"at":"legacy"}`),
		CreatedAt: now, UpdatedAt: now.Add(4 * time.Second)}
	mappedRun := Run{ID: "mapped-run", AgentName: "coder", SessionID: "continuity", Trigger: "chat", Phase: RunPhaseSucceeded,
		Input: "mapped retry", IdempotencyKey: "same-request", CreatedAt: now.Add(time.Second), UpdatedAt: now.Add(9 * time.Second)}
	if err := s.SaveRun(ctx, fallback, legacyRun); err != nil {
		t.Fatalf("save legacy run: %v", err)
	}
	if err := s.SaveRun(ctx, mapped, mappedRun); err != nil {
		t.Fatalf("save mapped duplicate-idempotency run: %v", err)
	}
	for _, entry := range []struct {
		scope Scope
		item  InboxItem
	}{
		{fallback, InboxItem{ID: "legacy-approval", AgentName: "coder", RunID: "legacy-run", Kind: InboxKindApproval, State: InboxStatePending, Prompt: "approve deploy", CreatedAt: now, UpdatedAt: now}},
		{mapped, InboxItem{ID: "mapped-question", AgentName: "coder", RunID: "mapped-run", Kind: InboxKindQuestion, State: InboxStatePending, Prompt: "which region?", CreatedAt: now.Add(time.Second), UpdatedAt: now.Add(time.Second)}},
	} {
		if err := s.AddInboxItem(ctx, entry.scope, entry.item); err != nil {
			t.Fatalf("save inbox item %s: %v", entry.item.ID, err)
		}
	}
	for _, entry := range []struct {
		scope Scope
		id    string
		body  string
	}{
		{fallback, "legacy-memory", "remember the rollback plan"},
		{mapped, "mapped-memory", "remember the deployed region"},
	} {
		if err := s.PutMemory(ctx, entry.scope, Memory{ID: entry.id, AgentName: "coder", Title: entry.id, Body: entry.body, CreatedAt: now, UpdatedAt: now}); err != nil {
			t.Fatalf("save memory %s: %v", entry.id, err)
		}
	}
	for _, entry := range []struct {
		scope Scope
		tc    ToolCall
	}{
		{fallback, ToolCall{ID: "legacy-step", AgentName: "coder", RunID: "legacy-run", Tool: "shell", Outcome: "ok", CreatedAt: now}},
		{mapped, ToolCall{ID: "mapped-step", AgentName: "coder", RunID: "mapped-run", Tool: "read_file", Outcome: "ok", CreatedAt: now.Add(time.Second)}},
	} {
		if err := s.AppendToolCall(ctx, entry.scope, entry.tc); err != nil {
			t.Fatalf("save tool trace %s: %v", entry.tc.ID, err)
		}
	}
	for _, entry := range []struct {
		scope        Scope
		in, out, usd int64
	}{
		{fallback, 10, 20, 5},
		{mapped, 1, 2, 3},
	} {
		if _, err := s.AddUsage(ctx, entry.scope, "coder", entry.in, entry.out, entry.usd, now, 30*24*time.Hour); err != nil {
			t.Fatalf("add usage: %v", err)
		}
	}

	ref := TenantRef{OrgUUID: mapped.OrgUUID, WorkspaceUUID: mapped.WorkspaceUUID, UpdatedAt: now.Add(10 * time.Second)}
	if err := s.SaveTenantRef(ctx, clusterID, ref); err != nil {
		t.Fatalf("save mapping and migrate history: %v", err)
	}
	if err := s.SaveTenantRef(ctx, clusterID, TenantRef{OrgUUID: "other-org", WorkspaceUUID: "other-workspace", UpdatedAt: now.Add(11 * time.Second)}); err == nil {
		t.Fatal("conflicting tenant mapping replaced an established mapping")
	}
	if got, ok, err := s.GetTenantRef(ctx, clusterID); err != nil || !ok || got.OrgUUID != ref.OrgUUID || got.WorkspaceUUID != ref.WorkspaceUUID {
		t.Fatalf("mapping after conflicting update = %+v, ok=%v, err=%v", got, ok, err)
	}

	page, err := s.ListMessages(ctx, mapped, "continuity", 20, "")
	if err != nil || len(page.Items) != 2 {
		t.Fatalf("merged transcript = %d messages, err=%v; want both scopes' messages", len(page.Items), err)
	}
	summary, ok, err := s.GetSessionSummary(ctx, mapped, "continuity")
	if err != nil || !ok || summary.Summary != "mapped summary" || summary.MessageCount != 5 || summary.Checkpoint == nil || summary.Checkpoint.ThroughSequence != 2 {
		t.Fatalf("merged summary = %+v, ok=%v, err=%v; want newest checkpoint plus max count", summary, ok, err)
	}
	next, err := s.NextHarnessTurn(ctx, mapped, "continuity", now.Add(11*time.Second))
	if err != nil || next.Turns != 5 || next.ObservedEpoch != 3 || next.HarnessSessionID != "runner-thread-legacy" || next.BackendKey != "edge-a" {
		t.Fatalf("merged harness state = %+v, err=%v; want epoch 5 continuing the highest observed receipt", next, err)
	}
	if runs, err := s.ListRuns(ctx, mapped, 20); err != nil || len(runs) != 2 {
		t.Fatalf("migrated runs = %d, err=%v; want both duplicate-key runs preserved", len(runs), err)
	}
	if winner, found, err := s.FindRunByIdempotencyKey(ctx, mapped, "same-request"); err != nil || !found || winner.ID != "legacy-run" {
		t.Fatalf("idempotency winner = %+v, found=%v, err=%v; want earliest legacy run", winner, found, err)
	}
	if _, err := s.GetRun(ctx, fallback, "legacy-run"); err != nil {
		t.Fatalf("old in-flight scope could not follow moved run: %v", err)
	}
	if err := s.RequestCancel(ctx, fallback, "legacy-run", now.Add(12*time.Second)); err != nil {
		t.Fatalf("cancel through old in-flight scope: %v", err)
	}
	cancelled, err := s.GetRun(ctx, mapped, "legacy-run")
	if err != nil || !cancelled.CancelRequested {
		t.Fatalf("canonical run after old-scope cancellation = %+v, err=%v", cancelled, err)
	}
	if inbox, err := s.ListInbox(ctx, mapped, InboxStatePending); err != nil || len(inbox) != 2 {
		t.Fatalf("migrated approvals/questions = %d, err=%v; want both", len(inbox), err)
	}
	if memories, err := s.ListMemories(ctx, mapped, 20); err != nil || len(memories) != 2 {
		t.Fatalf("migrated memories = %d, err=%v; want both", len(memories), err)
	}
	if trace, err := s.ListToolCalls(ctx, mapped, "legacy-run"); err != nil || len(trace) != 1 || trace[0].ID != "legacy-step" {
		t.Fatalf("migrated trace = %+v, err=%v", trace, err)
	}
	usage, err := s.GetUsage(ctx, mapped, "coder", now, 30*24*time.Hour)
	if err != nil || usage.InputTokens != 11 || usage.OutputTokens != 22 || usage.USDMicros != 8 {
		t.Fatalf("merged usage = %+v, err=%v; want 11/22/8", usage, err)
	}

	// A late write from a request that captured the fallback before mapping is
	// normalized by the store; another migration pass must not double-count it.
	if err := s.AppendMessage(ctx, fallback, Message{ID: "late-message", AgentName: "coder", SessionID: "continuity", Role: "assistant", Content: "finished", CreatedAt: now.Add(13 * time.Second)}); err != nil {
		t.Fatalf("append late fallback message: %v", err)
	}
	if err := s.SaveTenantRef(ctx, clusterID, ref); err != nil {
		t.Fatalf("repeat mapping reconciliation: %v", err)
	}
	usage, err = s.GetUsage(ctx, mapped, "coder", now, 30*24*time.Hour)
	if err != nil || usage.InputTokens != 11 || usage.OutputTokens != 22 || usage.USDMicros != 8 {
		t.Fatalf("usage after idempotent reconciliation = %+v, err=%v", usage, err)
	}
}

func TestMemoryStoreFallbackMutationRechecksMappingUnderWriteLock(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryStore()
	clusterID := "cluster-scope-race"
	fallback := Scope{OrgUUID: UnmappedOrg, WorkspaceUUID: clusterID, AgentName: "coder", ClusterID: clusterID}
	mapped := Scope{OrgUUID: "org-scope-race", WorkspaceUUID: "workspace-scope-race", AgentName: "coder", ClusterID: clusterID}
	now := time.Now().UTC()
	if err := s.AppendMessage(ctx, fallback, Message{ID: "before-map", AgentName: "coder", SessionID: "s", Role: "user", Content: "old", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	// Simulate a caller that resolved the fallback before SaveTenantRef, then
	// pauses until after the mapping is committed. MemoryStore's final scope
	// recheck happens under the same mutex as the write.
	if err := s.SaveTenantRef(ctx, clusterID, TenantRef{OrgUUID: mapped.OrgUUID, WorkspaceUUID: mapped.WorkspaceUUID, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendMessage(ctx, fallback, Message{ID: "after-map", AgentName: "coder", SessionID: "s", Role: "assistant", Content: "new", CreatedAt: now.Add(time.Second)}); err != nil {
		t.Fatal(err)
	}
	page, err := s.ListMessages(ctx, mapped, "s", 10, "")
	if err != nil || len(page.Items) != 2 {
		t.Fatalf("canonical transcript after delayed fallback write = %d messages, err=%v", len(page.Items), err)
	}
}

func TestPostgresTenantMappingMigratesSplitHistory(t *testing.T) {
	ps := openTestPostgres(t)
	ctx := context.Background()
	clusterID := "scope-migration-" + uuid.NewString()
	t.Cleanup(func() {
		for _, unmappedClusterID := range []string{clusterID, clusterID + "-destructive", clusterID + "-budget"} {
			for _, table := range []string{"agents_messages", "agents_runs", "agents_memories", "agents_inbox", "agents_tool_calls", "agents_usage", "agents_session_summaries", "agents_harness_sessions"} {
				_, _ = ps.db.ExecContext(context.Background(), "DELETE FROM "+table+" WHERE org_uuid=$1 AND workspace_uuid=$2", UnmappedOrg, unmappedClusterID)
			}
			_, _ = ps.db.ExecContext(context.Background(), `DELETE FROM agents_tenants WHERE cluster_id=$1`, unmappedClusterID)
		}
	})
	canonical := pgScope(t, ps)
	canonical.AgentName = "coder"
	fallback := Scope{OrgUUID: UnmappedOrg, WorkspaceUUID: clusterID, AgentName: "coder", ClusterID: clusterID}
	now := time.Now().UTC().Truncate(time.Microsecond)
	for _, entry := range []struct {
		scope Scope
		id    string
		text  string
		at    time.Time
	}{
		{fallback, "legacy-user-" + uuid.NewString(), "legacy transcript", now},
		{canonical, "mapped-user-" + uuid.NewString(), "mapped transcript", now.Add(time.Second)},
	} {
		if err := ps.AppendMessage(ctx, entry.scope, Message{
			ID: entry.id, AgentName: "coder", SessionID: "continuity", Role: "user", Content: entry.text, CreatedAt: entry.at,
		}); err != nil {
			t.Fatalf("append transcript: %v", err)
		}
	}
	legacyCheckpoint := &SessionCheckpoint{Version: 1, ThroughSequence: 1, ReplacementHistory: []SessionCheckpointMessage{{Role: "user", Content: "legacy prefix"}}}
	mappedCheckpoint := &SessionCheckpoint{Version: 1, ThroughSequence: 2, ReplacementHistory: []SessionCheckpointMessage{{Role: "user", Content: "mapped prefix"}}}
	for _, entry := range []struct {
		scope Scope
		count int
		seq   int64
		at    time.Time
	}{
		{fallback, 5, 1, now.Add(2 * time.Second)},
		{canonical, 3, 2, now.Add(3 * time.Second)},
	} {
		checkpoint := legacyCheckpoint
		if entry.seq == 2 {
			checkpoint = mappedCheckpoint
		}
		if err := ps.PutSessionSummary(ctx, entry.scope, SessionSummary{
			SessionID: "continuity", Summary: fmt.Sprintf("summary-%d", entry.seq), ThroughAt: entry.at,
			MessageCount: entry.count, CreatedAt: entry.at, UpdatedAt: entry.at, Checkpoint: checkpoint,
		}); err != nil {
			t.Fatalf("put summary: %v", err)
		}
	}
	for _, entry := range []struct {
		scope Scope
		turns int
		at    time.Time
	}{
		{fallback, 4, now.Add(4 * time.Second)},
		{canonical, 2, now.Add(6 * time.Second)},
	} {
		for i := 0; i < entry.turns; i++ {
			if _, err := ps.NextHarnessTurn(ctx, entry.scope, "continuity", entry.at.Add(time.Duration(i)*time.Second)); err != nil {
				t.Fatalf("allocate harness turn: %v", err)
			}
		}
		epoch := int64(entry.turns - 1)
		native, backend := "runner-thread-legacy", "edge-a"
		if entry.scope.OrgUUID != UnmappedOrg {
			epoch, native, backend = 2, "runner-thread-mapped", "edge-b"
		}
		if err := ps.PutHarnessSession(ctx, entry.scope, HarnessSession{
			SessionID: "continuity", HarnessSessionID: native, BackendKey: backend, Turns: int64(entry.turns),
			ObservedEpoch: epoch, UpdatedAt: entry.at.Add(time.Duration(entry.turns) * time.Second),
		}); err != nil {
			t.Fatalf("put harness session: %v", err)
		}
	}
	legacyRun := Run{ID: "legacy-run-" + uuid.NewString(), AgentName: "coder", SessionID: "continuity", Trigger: "chat", Phase: RunPhaseRunning,
		Input: "legacy request", IdempotencyKey: "same-request", Checkpoint: json.RawMessage(`{"at":"legacy"}`),
		CreatedAt: now, UpdatedAt: now.Add(4 * time.Second)}
	mappedRun := Run{ID: "mapped-run-" + uuid.NewString(), AgentName: "coder", SessionID: "continuity", Trigger: "chat", Phase: RunPhaseSucceeded,
		Input: "mapped retry", IdempotencyKey: "same-request", CreatedAt: now.Add(time.Second), UpdatedAt: now.Add(9 * time.Second)}
	if err := ps.SaveRun(ctx, fallback, legacyRun); err != nil {
		t.Fatalf("save legacy run: %v", err)
	}
	if err := ps.SaveRun(ctx, canonical, mappedRun); err != nil {
		t.Fatalf("save mapped duplicate-idempotency run: %v", err)
	}
	for _, entry := range []struct {
		scope Scope
		item  InboxItem
	}{
		{fallback, InboxItem{ID: "legacy-approval-" + uuid.NewString(), AgentName: "coder", RunID: legacyRun.ID, Kind: InboxKindApproval, State: InboxStatePending, Prompt: "approve deploy", CreatedAt: now, UpdatedAt: now}},
		{canonical, InboxItem{ID: "mapped-question-" + uuid.NewString(), AgentName: "coder", RunID: mappedRun.ID, Kind: InboxKindQuestion, State: InboxStatePending, Prompt: "which region?", CreatedAt: now.Add(time.Second), UpdatedAt: now.Add(time.Second)}},
	} {
		if err := ps.AddInboxItem(ctx, entry.scope, entry.item); err != nil {
			t.Fatalf("save inbox item: %v", err)
		}
	}
	for _, entry := range []struct {
		scope Scope
		id    string
	}{
		{fallback, "legacy-memory-" + uuid.NewString()},
		{canonical, "mapped-memory-" + uuid.NewString()},
	} {
		if err := ps.PutMemory(ctx, entry.scope, Memory{ID: entry.id, AgentName: "coder", Title: entry.id, Body: entry.id, CreatedAt: now, UpdatedAt: now}); err != nil {
			t.Fatalf("save memory: %v", err)
		}
	}
	for _, entry := range []struct {
		scope Scope
		tc    ToolCall
	}{
		{fallback, ToolCall{ID: "legacy-step-" + uuid.NewString(), AgentName: "coder", RunID: legacyRun.ID, Tool: "shell", Outcome: "ok", CreatedAt: now}},
		{canonical, ToolCall{ID: "mapped-step-" + uuid.NewString(), AgentName: "coder", RunID: mappedRun.ID, Tool: "read_file", Outcome: "ok", CreatedAt: now.Add(time.Second)}},
	} {
		if err := ps.AppendToolCall(ctx, entry.scope, entry.tc); err != nil {
			t.Fatalf("save tool trace: %v", err)
		}
	}
	for _, entry := range []struct {
		scope        Scope
		in, out, usd int64
	}{
		{fallback, 10, 20, 5},
		{canonical, 1, 2, 3},
	} {
		if _, err := ps.AddUsage(ctx, entry.scope, "coder", entry.in, entry.out, entry.usd, now, 30*24*time.Hour); err != nil {
			t.Fatalf("add usage: %v", err)
		}
	}

	ref := TenantRef{OrgUUID: canonical.OrgUUID, WorkspaceUUID: canonical.WorkspaceUUID, UpdatedAt: now.Add(10 * time.Second)}
	if err := ps.SaveTenantRef(ctx, clusterID, ref); err != nil {
		t.Fatalf("save mapping and migrate history: %v", err)
	}
	page, err := ps.ListMessages(ctx, canonical, "continuity", 20, "")
	if err != nil || len(page.Items) != 2 {
		t.Fatalf("merged transcript = %d messages, err=%v", len(page.Items), err)
	}
	summary, ok, err := ps.GetSessionSummary(ctx, canonical, "continuity")
	if err != nil || !ok || summary.MessageCount != 5 || summary.Checkpoint == nil || summary.Checkpoint.ThroughSequence != 2 {
		t.Fatalf("merged summary = %+v, ok=%v, err=%v", summary, ok, err)
	}
	next, err := ps.NextHarnessTurn(ctx, canonical, "continuity", now.Add(11*time.Second))
	if err != nil || next.Turns != 5 || next.ObservedEpoch != 3 || next.HarnessSessionID != "runner-thread-legacy" || next.BackendKey != "edge-a" {
		t.Fatalf("merged harness = %+v, err=%v", next, err)
	}
	if runs, err := ps.ListRuns(ctx, canonical, 20); err != nil || len(runs) != 2 {
		t.Fatalf("migrated runs = %d, err=%v", len(runs), err)
	}
	if winner, found, err := ps.FindRunByIdempotencyKey(ctx, canonical, "same-request"); err != nil || !found || winner.ID != legacyRun.ID {
		t.Fatalf("idempotency winner = %s, found=%v, err=%v; want %s", winner.ID, found, err, legacyRun.ID)
	}
	// A handler that retained the old Scope across migration follows the moved
	// rows, including cancellation of the run.
	if _, err := ps.GetRun(ctx, fallback, legacyRun.ID); err != nil {
		t.Fatalf("old-scope read after migration: %v", err)
	}
	if err := ps.RequestCancel(ctx, fallback, legacyRun.ID, now.Add(12*time.Second)); err != nil {
		t.Fatalf("old-scope cancel after migration: %v", err)
	}
	if run, err := ps.GetRun(ctx, canonical, legacyRun.ID); err != nil || !run.CancelRequested {
		t.Fatalf("canonical run after old-scope cancellation = %+v, err=%v", run, err)
	}
	if inbox, err := ps.ListInbox(ctx, canonical, InboxStatePending); err != nil || len(inbox) != 2 {
		t.Fatalf("migrated inbox = %d, err=%v", len(inbox), err)
	}
	if memories, err := ps.ListMemories(ctx, canonical, 20); err != nil || len(memories) != 2 {
		t.Fatalf("migrated memories = %d, err=%v", len(memories), err)
	}
	if trace, err := ps.ListToolCalls(ctx, canonical, legacyRun.ID); err != nil || len(trace) != 1 {
		t.Fatalf("migrated tool trace = %+v, err=%v", trace, err)
	}
	usage, err := ps.GetUsage(ctx, canonical, "coder", now, 30*24*time.Hour)
	if err != nil || usage.InputTokens != 11 || usage.OutputTokens != 22 || usage.USDMicros != 8 {
		t.Fatalf("merged usage = %+v, err=%v", usage, err)
	}

	// Simulate a still-running older replica writing after the first migration.
	// The next mapping reconciliation must move it once, without re-adding the
	// usage that was already merged above.
	lateID := "late-message-" + uuid.NewString()
	if _, err := ps.db.ExecContext(ctx, `INSERT INTO agents_messages
		(id, org_uuid, workspace_uuid, agent_name, session_id, role, content, created_at)
		VALUES ($1,$2,$3,'coder','continuity','assistant','late result',$4)`,
		lateID, UnmappedOrg, clusterID, now.Add(13*time.Second)); err != nil {
		t.Fatalf("insert late legacy message: %v", err)
	}
	windowStart := windowStart(now, 30*24*time.Hour)
	if _, err := ps.db.ExecContext(ctx, `INSERT INTO agents_usage
		(org_uuid,workspace_uuid,agent_name,window_start,input_tokens,output_tokens,usd_micros,updated_at)
		VALUES ($1,$2,'coder',$3,3,4,6,$4)`, UnmappedOrg, clusterID, windowStart, now.Add(13*time.Second)); err != nil {
		t.Fatalf("insert late legacy usage: %v", err)
	}
	if err := ps.SaveTenantRef(ctx, clusterID, ref); err != nil {
		t.Fatalf("reconcile late legacy writes: %v", err)
	}
	if err := ps.SaveTenantRef(ctx, clusterID, ref); err != nil {
		t.Fatalf("repeat reconciliation: %v", err)
	}
	usage, err = ps.GetUsage(ctx, canonical, "coder", now, 30*24*time.Hour)
	if err != nil || usage.InputTokens != 14 || usage.OutputTokens != 26 || usage.USDMicros != 14 {
		t.Fatalf("usage after catch-up = %+v, err=%v; want 14/26/14", usage, err)
	}
	page, err = ps.ListMessages(ctx, canonical, "continuity", 20, "")
	if err != nil || len(page.Items) != 3 {
		t.Fatalf("transcript after catch-up = %d messages, err=%v", len(page.Items), err)
	}
}

func TestPostgresMappedFallbackScopeFailsClosedWhenMappingLookupFails(t *testing.T) {
	ps := openTestPostgres(t)
	ctx := context.Background()
	clusterID := "scope-lookup-error-" + uuid.NewString()
	canonical := pgScope(t, ps)
	fallback := Scope{OrgUUID: UnmappedOrg, WorkspaceUUID: clusterID, AgentName: "coder", ClusterID: clusterID}
	if err := ps.SaveTenantRef(ctx, clusterID, TenantRef{
		OrgUUID: canonical.OrgUUID, WorkspaceUUID: canonical.WorkspaceUUID, UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("save mapping: %v", err)
	}

	// Hide only the mapping table to exercise a real lookup error while leaving
	// the store and the fallback data tables usable. A fresh fallback scope must
	// not write or allocate a new epoch when a mapping may already exist.
	if _, err := ps.db.ExecContext(ctx, `ALTER TABLE agents_tenants RENAME TO agents_tenants_scope_lookup_hidden`); err != nil {
		t.Fatalf("hide tenant mappings: %v", err)
	}
	t.Cleanup(func() {
		_, _ = ps.db.ExecContext(context.Background(), `ALTER TABLE agents_tenants_scope_lookup_hidden RENAME TO agents_tenants`)
		_, _ = ps.db.ExecContext(context.Background(), `DELETE FROM agents_messages WHERE org_uuid=$1 AND workspace_uuid=$2`, UnmappedOrg, clusterID)
		_, _ = ps.db.ExecContext(context.Background(), `DELETE FROM agents_harness_sessions WHERE org_uuid=$1 AND workspace_uuid=$2`, UnmappedOrg, clusterID)
		_, _ = ps.db.ExecContext(context.Background(), `DELETE FROM agents_tenants WHERE cluster_id=$1`, clusterID)
	})

	if err := ps.AppendMessage(ctx, fallback, Message{
		ID: "must-not-write", AgentName: "coder", SessionID: "continuity", Role: "user", Content: "no fallback write",
		CreatedAt: time.Now().UTC(),
	}); err == nil {
		t.Fatal("AppendMessage succeeded despite an unknown tenant mapping")
	}
	if _, err := ps.NextHarnessTurn(ctx, fallback, "continuity", time.Now().UTC()); err == nil {
		t.Fatal("NextHarnessTurn allocated an epoch despite an unknown tenant mapping")
	}
	var messages, sessions int
	if err := ps.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM agents_messages WHERE org_uuid=$1 AND workspace_uuid=$2`, UnmappedOrg, clusterID).Scan(&messages); err != nil {
		t.Fatalf("count fallback messages: %v", err)
	}
	if err := ps.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM agents_harness_sessions WHERE org_uuid=$1 AND workspace_uuid=$2`, UnmappedOrg, clusterID).Scan(&sessions); err != nil {
		t.Fatalf("count fallback harness sessions: %v", err)
	}
	if messages != 0 || sessions != 0 {
		t.Fatalf("lookup failure created fallback rows: messages=%d harness_sessions=%d", messages, sessions)
	}
}

func TestPostgresTenantMappingIsImmutable(t *testing.T) {
	ps := openTestPostgres(t)
	ctx := context.Background()
	clusterID := "scope-immutable-" + uuid.NewString()
	first := TenantRef{OrgUUID: "org-immutable-" + uuid.NewString(), WorkspaceUUID: "ws-immutable-" + uuid.NewString(), UpdatedAt: time.Now().UTC()}
	second := TenantRef{OrgUUID: "org-other-" + uuid.NewString(), WorkspaceUUID: "ws-other-" + uuid.NewString(), UpdatedAt: time.Now().UTC()}
	t.Cleanup(func() {
		for _, table := range []string{"agents_messages", "agents_runs", "agents_memories", "agents_inbox", "agents_tool_calls", "agents_usage", "agents_session_summaries", "agents_harness_sessions"} {
			_, _ = ps.db.ExecContext(context.Background(), "DELETE FROM "+table+" WHERE org_uuid=$1 OR (org_uuid=$2 AND workspace_uuid=$3)", first.OrgUUID, UnmappedOrg, clusterID)
		}
		_, _ = ps.db.ExecContext(context.Background(), `DELETE FROM agents_tenants WHERE cluster_id=$1`, clusterID)
	})
	if err := ps.SaveTenantRef(ctx, clusterID, first); err != nil {
		t.Fatalf("save initial mapping: %v", err)
	}
	// Model a late row from an older provider that does not participate in the
	// fence. A conflicting mapping attempt must fail before it moves that row.
	if _, err := ps.db.ExecContext(ctx, `INSERT INTO agents_messages
		(id, org_uuid, workspace_uuid, agent_name, session_id, role, content, created_at)
		VALUES ($1,$2,$3,'coder','s','user','legacy row',$4)`,
		"legacy-immutable-"+uuid.NewString(), UnmappedOrg, clusterID, time.Now().UTC()); err != nil {
		t.Fatalf("seed late fallback row: %v", err)
	}
	if err := ps.SaveTenantRef(ctx, clusterID, second); err == nil {
		t.Fatal("conflicting tenant mapping replaced an established mapping")
	}
	got, ok, err := ps.GetTenantRef(ctx, clusterID)
	if err != nil || !ok || got.OrgUUID != first.OrgUUID || got.WorkspaceUUID != first.WorkspaceUUID {
		t.Fatalf("mapping after conflicting update = %+v, ok=%v, err=%v", got, ok, err)
	}
	var fallbackRows, wrongTenantRows int
	if err := ps.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM agents_messages WHERE org_uuid=$1 AND workspace_uuid=$2`, UnmappedOrg, clusterID).Scan(&fallbackRows); err != nil {
		t.Fatalf("count unmoved fallback rows: %v", err)
	}
	if err := ps.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM agents_messages WHERE org_uuid=$1 AND workspace_uuid=$2`, second.OrgUUID, second.WorkspaceUUID).Scan(&wrongTenantRows); err != nil {
		t.Fatalf("count wrong-tenant rows: %v", err)
	}
	if fallbackRows != 1 || wrongTenantRows != 0 {
		t.Fatalf("conflicting mapping moved rows: fallback=%d wrong tenant=%d", fallbackRows, wrongTenantRows)
	}
}

func TestPostgresFallbackMutationsFenceTenantMapping(t *testing.T) {
	ps := openTestPostgres(t)
	ctx := context.Background()
	clusterID := "scope-fence-" + uuid.NewString()
	canonical := pgScope(t, ps)
	canonical.AgentName = "coder"
	fallback := Scope{OrgUUID: UnmappedOrg, WorkspaceUUID: clusterID, AgentName: "coder", ClusterID: clusterID}
	ref := TenantRef{OrgUUID: canonical.OrgUUID, WorkspaceUUID: canonical.WorkspaceUUID, UpdatedAt: time.Now().UTC()}
	t.Cleanup(func() {
		for _, unmappedClusterID := range []string{clusterID, clusterID + "-destructive"} {
			for _, table := range []string{"agents_messages", "agents_runs", "agents_memories", "agents_inbox", "agents_tool_calls", "agents_usage", "agents_session_summaries", "agents_harness_sessions"} {
				_, _ = ps.db.ExecContext(context.Background(), "DELETE FROM "+table+" WHERE org_uuid=$1 AND workspace_uuid=$2", UnmappedOrg, unmappedClusterID)
			}
			_, _ = ps.db.ExecContext(context.Background(), `DELETE FROM agents_tenants WHERE cluster_id=$1`, unmappedClusterID)
		}
	})

	// The same idempotency key already exists in the canonical scope. A fallback
	// SaveRun racing mapping publication must block on the shared tenant-map lock,
	// route to canonical after release, and hit the unique index before dispatch.
	existing := Run{ID: "existing-" + uuid.NewString(), AgentName: "coder", IdempotencyKey: "same-key", Phase: RunPhasePending,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	if err := ps.SaveRun(ctx, canonical, existing); err != nil {
		t.Fatalf("save canonical idempotency winner: %v", err)
	}
	mapTx, blockerPID := lockTenantMapping(t, ps, clusterID)
	writeDone := make(chan error, 1)
	go func() {
		writeDone <- ps.SaveRun(ctx, fallback, Run{ID: "duplicate-" + uuid.NewString(), AgentName: "coder", IdempotencyKey: "same-key", Phase: RunPhasePending,
			CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()})
	}()
	waitForTenantLockWaiters(t, ps, blockerPID, 1)
	publishTenantMappingInTx(t, mapTx, clusterID, ref)
	if err := <-writeDone; err == nil {
		t.Fatal("fallback duplicate SaveRun succeeded after mapping publication")
	}
	if runs, err := ps.ListRuns(ctx, canonical, 20); err != nil || len(runs) != 1 || runs[0].ID != existing.ID {
		t.Fatalf("canonical runs after duplicate race = %+v, err=%v", runs, err)
	}
	var fallbackRuns int
	if err := ps.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM agents_runs WHERE org_uuid=$1 AND workspace_uuid=$2`, UnmappedOrg, clusterID).Scan(&fallbackRuns); err != nil || fallbackRuns != 0 {
		t.Fatalf("fallback duplicate rows=%d, err=%v", fallbackRuns, err)
	}

	// A cancellation and session delete resolved against a separate fallback
	// scope before map publication. Both must wait, re-resolve after commit, and
	// mutate the rows that migration moved into the canonical scope.
	destructiveClusterID := clusterID + "-destructive"
	destructiveFallback := Scope{OrgUUID: UnmappedOrg, WorkspaceUUID: destructiveClusterID, AgentName: "coder", ClusterID: destructiveClusterID}
	destructiveRef := TenantRef{OrgUUID: canonical.OrgUUID, WorkspaceUUID: canonical.WorkspaceUUID, UpdatedAt: time.Now().UTC()}
	run := Run{ID: "cancel-" + uuid.NewString(), AgentName: "coder", SessionID: "cancel-session", Phase: RunPhasePending,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	if err := ps.SaveRun(ctx, destructiveFallback, run); err != nil {
		t.Fatalf("save fallback run: %v", err)
	}
	if err := ps.AppendMessage(ctx, destructiveFallback, Message{ID: "delete-me-" + uuid.NewString(), AgentName: "coder", SessionID: "delete-session", Role: "user", Content: "old", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatalf("append fallback session: %v", err)
	}
	if _, err := ps.NextHarnessTurn(ctx, destructiveFallback, "delete-session", time.Now().UTC()); err != nil {
		t.Fatalf("allocate fallback harness epoch: %v", err)
	}
	if err := ps.PutHarnessSession(ctx, destructiveFallback, HarnessSession{SessionID: "delete-session", HarnessSessionID: "native-session", BackendKey: "edge", Turns: 1, ObservedEpoch: 1, UpdatedAt: time.Now().UTC()}); err != nil {
		t.Fatalf("save fallback harness session: %v", err)
	}
	mapTx, blockerPID = lockTenantMapping(t, ps, destructiveClusterID)
	cancelDone := make(chan error, 1)
	deleteDone := make(chan error, 1)
	go func() { cancelDone <- ps.RequestCancel(ctx, destructiveFallback, run.ID, time.Now().UTC()) }()
	go func() { deleteDone <- ps.DeleteSession(ctx, destructiveFallback, "delete-session") }()
	waitForTenantLockWaiters(t, ps, blockerPID, 2)
	publishTenantMappingInTx(t, mapTx, destructiveClusterID, destructiveRef)
	if err := <-cancelDone; err != nil {
		t.Fatalf("fallback cancellation after mapping: %v", err)
	}
	if err := <-deleteDone; err != nil {
		t.Fatalf("fallback session delete after mapping: %v", err)
	}
	cancelled, err := ps.GetRun(ctx, canonical, run.ID)
	if err != nil || !cancelled.CancelRequested {
		t.Fatalf("canonical run after fenced cancellation = %+v, err=%v", cancelled, err)
	}
	if page, err := ps.ListMessages(ctx, canonical, "delete-session", 10, ""); err != nil || len(page.Items) != 0 {
		t.Fatalf("canonical session after fenced delete = %d messages, err=%v", len(page.Items), err)
	}
	harness, ok, err := ps.GetHarnessSession(ctx, canonical, "delete-session")
	if err != nil || !ok || harness.HarnessSessionID != "" || harness.ObservedEpoch <= 1 {
		t.Fatalf("canonical harness tombstone after fenced delete = %+v, ok=%v, err=%v", harness, ok, err)
	}

	// Budget reads must use the same fence: otherwise a lookup that races map
	// publication can see only the source usage, or report zero after migration.
	budgetClusterID := clusterID + "-budget"
	budgetFallback := Scope{OrgUUID: UnmappedOrg, WorkspaceUUID: budgetClusterID, AgentName: "coder", ClusterID: budgetClusterID}
	budgetRef := TenantRef{OrgUUID: canonical.OrgUUID, WorkspaceUUID: canonical.WorkspaceUUID, UpdatedAt: time.Now().UTC()}
	budgetNow := time.Now().UTC()
	if _, err := ps.AddUsage(ctx, budgetFallback, "coder", 7, 0, 0, budgetNow, 24*time.Hour); err != nil {
		t.Fatalf("add fallback usage: %v", err)
	}
	if _, err := ps.AddUsage(ctx, canonical, "coder", 11, 0, 0, budgetNow, 24*time.Hour); err != nil {
		t.Fatalf("add canonical usage: %v", err)
	}
	mapTx, blockerPID = lockTenantMapping(t, ps, budgetClusterID)
	type usageResult struct {
		usage Usage
		err   error
	}
	usageDone := make(chan usageResult, 1)
	go func() {
		usage, err := ps.GetUsage(ctx, budgetFallback, "coder", budgetNow, 24*time.Hour)
		usageDone <- usageResult{usage: usage, err: err}
	}()
	waitForTenantLockWaiters(t, ps, blockerPID, 1)
	publishTenantMappingInTx(t, mapTx, budgetClusterID, budgetRef)
	gotUsage := <-usageDone
	if gotUsage.err != nil || gotUsage.usage.InputTokens != 18 {
		t.Fatalf("budget usage after fenced migration = %+v, err=%v; want 18 input tokens", gotUsage.usage, gotUsage.err)
	}
}

func TestPostgresFallbackMutationRollsBackWithoutTenantMapping(t *testing.T) {
	ps := openTestPostgres(t)
	ctx := context.Background()
	clusterID := "scope-fence-rollback-" + uuid.NewString()
	fallback := Scope{OrgUUID: UnmappedOrg, WorkspaceUUID: clusterID, AgentName: "coder", ClusterID: clusterID}
	ref := TenantRef{OrgUUID: "org-rollback-" + uuid.NewString(), WorkspaceUUID: "ws-rollback-" + uuid.NewString(), UpdatedAt: time.Now().UTC()}
	t.Cleanup(func() {
		_, _ = ps.db.ExecContext(context.Background(), `DELETE FROM agents_runs WHERE org_uuid=$1 AND workspace_uuid=$2`, UnmappedOrg, clusterID)
		_, _ = ps.db.ExecContext(context.Background(), `DELETE FROM agents_tenants WHERE cluster_id=$1`, clusterID)
	})
	before := Run{ID: "before-rollback-" + uuid.NewString(), AgentName: "coder", Phase: RunPhasePending,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	if err := ps.SaveRun(ctx, fallback, before); err != nil {
		t.Fatalf("save pre-migration fallback run: %v", err)
	}
	mapTx, blockerPID := lockTenantMapping(t, ps, clusterID)
	writeDone := make(chan error, 1)
	after := Run{ID: "after-rollback-" + uuid.NewString(), AgentName: "coder", Phase: RunPhasePending,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	go func() {
		writeDone <- ps.SaveRun(ctx, fallback, after)
	}()
	waitForTenantLockWaiters(t, ps, blockerPID, 1)
	if err := migrateUnmappedScopeTx(ctx, mapTx, clusterID, ref); err != nil {
		t.Fatalf("stage scope migration before rollback: %v", err)
	}
	if _, err := mapTx.ExecContext(ctx, `
		INSERT INTO agents_tenants (cluster_id, org_uuid, workspace_uuid, updated_at)
		VALUES ($1,$2,$3,$4)`, clusterID, ref.OrgUUID, ref.WorkspaceUUID, ref.UpdatedAt.UTC()); err != nil {
		t.Fatalf("stage mapping before rollback: %v", err)
	}
	if err := mapTx.Rollback(); err != nil {
		t.Fatalf("rollback mapping transaction: %v", err)
	}
	if err := <-writeDone; err != nil {
		t.Fatalf("fallback write after mapping rollback: %v", err)
	}
	if _, ok, err := ps.GetTenantRef(ctx, clusterID); err != nil || ok {
		t.Fatalf("mapping after rollback exists=%v, err=%v", ok, err)
	}
	var fallbackRuns int
	if err := ps.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM agents_runs WHERE org_uuid=$1 AND workspace_uuid=$2`, UnmappedOrg, clusterID).Scan(&fallbackRuns); err != nil || fallbackRuns != 2 {
		t.Fatalf("fallback rows after mapping rollback=%d, err=%v; want both original and delayed writes", fallbackRuns, err)
	}
	var rolledBackCanonicalRuns int
	if err := ps.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM agents_runs WHERE org_uuid=$1 AND workspace_uuid=$2`, ref.OrgUUID, ref.WorkspaceUUID).Scan(&rolledBackCanonicalRuns); err != nil || rolledBackCanonicalRuns != 0 {
		t.Fatalf("canonical rows after mapping rollback=%d, err=%v; want zero", rolledBackCanonicalRuns, err)
	}
}

func lockTenantMapping(t *testing.T, ps *PostgresStore, clusterID string) (*sql.Tx, int) {
	t.Helper()
	tx, err := ps.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("begin tenant mapping blocker: %v", err)
	}
	var blockerPID int
	if err := tx.QueryRowContext(context.Background(), `SELECT pg_backend_pid()`).Scan(&blockerPID); err != nil {
		_ = tx.Rollback()
		t.Fatalf("read blocker PID: %v", err)
	}
	if _, err := tx.ExecContext(context.Background(), `SELECT pg_advisory_xact_lock(hashtext($1))`, "agents-tenant-map:"+clusterID); err != nil {
		_ = tx.Rollback()
		t.Fatalf("lock tenant mapping: %v", err)
	}
	t.Cleanup(func() { _ = tx.Rollback() })
	return tx, blockerPID
}

func waitForTenantLockWaiters(t *testing.T, ps *PostgresStore, blockerPID, minimum int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var count int
		err := ps.db.QueryRowContext(context.Background(), `
			SELECT COUNT(*) FROM pg_stat_activity
			WHERE $1 = ANY(pg_blocking_pids(pid)) AND wait_event_type='Lock'`, blockerPID).Scan(&count)
		if err == nil && count >= minimum {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("did not observe %d mutation(s) blocked by mapping PID %d", minimum, blockerPID)
}

func publishTenantMappingInTx(t *testing.T, tx *sql.Tx, clusterID string, ref TenantRef) {
	t.Helper()
	ctx := context.Background()
	if err := migrateUnmappedScopeTx(ctx, tx, clusterID, ref); err != nil {
		t.Fatalf("migrate fallback scope in mapping transaction: %v", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO agents_tenants (cluster_id, org_uuid, workspace_uuid, updated_at)
		VALUES ($1,$2,$3,$4) ON CONFLICT (cluster_id) DO NOTHING`,
		clusterID, ref.OrgUUID, ref.WorkspaceUUID, ref.UpdatedAt.UTC()); err != nil {
		t.Fatalf("publish tenant mapping in transaction: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit tenant mapping transaction: %v", err)
	}
}
