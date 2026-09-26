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
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// openLegacyHistorySchema creates a private PostgreSQL schema, then points the
// store connection's search_path at it. The test can build the pre-migration
// tables and run EnsureSchema without dropping columns or touching other test
// data in the shared developer database.
func openLegacyHistorySchema(t *testing.T) (*PostgresStore, string) {
	t.Helper()
	baseDSN := os.Getenv("AGENTS_TEST_POSTGRES_DSN")
	if baseDSN == "" {
		t.Skip("AGENTS_TEST_POSTGRES_DSN not set — skipping isolated Postgres history migration test")
	}
	ctx := context.Background()
	admin, err := sql.Open("postgres", baseDSN)
	if err != nil {
		t.Fatalf("open Postgres admin connection: %v", err)
	}
	if err := admin.PingContext(ctx); err != nil {
		_ = admin.Close()
		t.Fatalf("ping Postgres admin connection: %v", err)
	}
	t.Cleanup(func() { _ = admin.Close() })

	schemaName := "agents_history_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := admin.ExecContext(ctx, `CREATE SCHEMA "`+schemaName+`"`); err != nil {
		t.Fatalf("create isolated history schema: %v", err)
	}
	t.Cleanup(func() {
		if _, err := admin.ExecContext(ctx, `DROP SCHEMA "`+schemaName+`" CASCADE`); err != nil {
			t.Logf("drop isolated history schema: %v", err)
		}
	})

	isolatedDSN, err := dsnWithSearchPath(baseDSN, schemaName)
	if err != nil {
		t.Fatalf("scope Postgres DSN to isolated schema: %v", err)
	}
	ps, err := OpenPostgres(ctx, isolatedDSN)
	if err != nil {
		t.Fatalf("open isolated Postgres store: %v", err)
	}
	t.Cleanup(func() { _ = ps.Close() })
	return ps, isolatedDSN
}

func dsnWithSearchPath(dsn, schema string) (string, error) {
	parsed, err := url.Parse(dsn)
	if err == nil && (parsed.Scheme == "postgres" || parsed.Scheme == "postgresql") {
		query := parsed.Query()
		query.Set("search_path", schema)
		parsed.RawQuery = query.Encode()
		return parsed.String(), nil
	}
	if err != nil && (strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://")) {
		return "", err
	}
	// lib/pq accepts runtime parameters such as search_path in keyword DSNs.
	return fmt.Sprintf("%s search_path='%s'", dsn, strings.ReplaceAll(schema, "'", "\\'")), nil
}

func createLegacyHistoryTables(t *testing.T, ps *PostgresStore) {
	t.Helper()
	ctx := context.Background()
	// These are the populated legacy shapes before append_sequence and the
	// nullable session checkpoint JSONB column were added.
	if _, err := ps.db.ExecContext(ctx, `
		CREATE TABLE agents_messages (
			id TEXT PRIMARY KEY,
			org_uuid TEXT NOT NULL,
			workspace_uuid TEXT NOT NULL,
			agent_name TEXT NOT NULL,
			session_id TEXT NOT NULL DEFAULT '',
			run_id TEXT NOT NULL DEFAULT '',
			role TEXT NOT NULL,
			content TEXT NOT NULL,
			content_encrypted BOOLEAN NOT NULL DEFAULT FALSE,
			content_key_id TEXT NOT NULL DEFAULT '',
			metadata JSONB,
			created_at TIMESTAMPTZ NOT NULL
		)`); err != nil {
		t.Fatalf("create legacy messages table: %v", err)
	}
	if _, err := ps.db.ExecContext(ctx, `
		CREATE TABLE agents_session_summaries (
			org_uuid TEXT NOT NULL,
			workspace_uuid TEXT NOT NULL,
			agent_name TEXT NOT NULL,
			session_id TEXT NOT NULL,
			summary TEXT NOT NULL,
			through_at TIMESTAMPTZ NOT NULL,
			message_count INT NOT NULL DEFAULT 0,
			created_at TIMESTAMPTZ NOT NULL,
			updated_at TIMESTAMPTZ NOT NULL,
			PRIMARY KEY (org_uuid, workspace_uuid, agent_name, session_id)
		)`); err != nil {
		t.Fatalf("create legacy session summaries table: %v", err)
	}
}

func TestPostgres_LegacyHistoryUpgradeAndCheckpointSequencePersistence(t *testing.T) {
	ctx := context.Background()
	ps, isolatedDSN := openLegacyHistorySchema(t)
	createLegacyHistoryTables(t, ps)

	scope := Scope{
		OrgUUID: "org-" + uuid.NewString(), WorkspaceUUID: "ws-" + uuid.NewString(), AgentName: "history-test",
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	legacyIDs := []string{"legacy-a-" + uuid.NewString(), "legacy-b-" + uuid.NewString()}
	for i, id := range legacyIDs {
		if _, err := ps.db.ExecContext(ctx, `
			INSERT INTO agents_messages
				(id, org_uuid, workspace_uuid, agent_name, session_id, run_id, role, content, created_at)
			VALUES ($1,$2,$3,$4,'checkpoint-session','','user',$5,$6)`,
			id, scope.OrgUUID, scope.WorkspaceUUID, scope.AgentName, fmt.Sprintf("legacy message %d", i), now); err != nil {
			t.Fatalf("seed legacy message: %v", err)
		}
	}
	if _, err := ps.db.ExecContext(ctx, `
		INSERT INTO agents_session_summaries
			(org_uuid, workspace_uuid, agent_name, session_id, summary, through_at, message_count, created_at, updated_at)
		VALUES ($1,$2,$3,'legacy-session','existing summary',$4,3,$4,$4)`,
		scope.OrgUUID, scope.WorkspaceUUID, scope.AgentName, now); err != nil {
		t.Fatalf("seed legacy summary: %v", err)
	}

	if err := ps.EnsureSchema(ctx); err != nil {
		t.Fatalf("upgrade legacy schema: %v", err)
	}
	if err := ps.EnsureSchema(ctx); err != nil {
		t.Fatalf("repeat legacy schema upgrade: %v", err)
	}

	legacy, ok, err := ps.GetSessionSummary(ctx, scope, "legacy-session")
	if err != nil || !ok || legacy.Summary != "existing summary" || legacy.Checkpoint != nil {
		t.Fatalf("legacy summary did not survive upgrade: ok=%v err=%v summary=%+v", ok, err, legacy)
	}

	messages, err := ps.LoadRecentMessages(ctx, scope, "checkpoint-session", 10)
	if err != nil || len(messages) != len(legacyIDs) {
		t.Fatalf("load migrated messages: err=%v messages=%+v", err, messages)
	}
	seenSequences := make(map[int64]bool, len(messages))
	var through Message
	for _, message := range messages {
		if message.Sequence <= 0 || seenSequences[message.Sequence] {
			t.Fatalf("legacy sequence missing or duplicated: %+v", messages)
		}
		seenSequences[message.Sequence] = true
		if message.ID == legacyIDs[1] {
			through = message
		}
	}
	if through.ID == "" {
		t.Fatalf("could not find checkpoint boundary row %q in %+v", legacyIDs[1], messages)
	}

	if err := ps.AppendMessage(ctx, scope, Message{
		ID: "appended-" + uuid.NewString(), AgentName: scope.AgentName, SessionID: "checkpoint-session",
		Role: "assistant", Content: "new after migration", CreatedAt: now,
	}); err != nil {
		t.Fatalf("append after migration: %v", err)
	}
	messages, err = ps.LoadRecentMessages(ctx, scope, "checkpoint-session", 10)
	if err != nil || len(messages) != 3 {
		t.Fatalf("load appended messages: err=%v messages=%+v", err, messages)
	}
	maxOldSequence := int64(0)
	var appended Message
	for _, message := range messages {
		if message.ID == legacyIDs[0] || message.ID == legacyIDs[1] {
			maxOldSequence = max(maxOldSequence, message.Sequence)
		} else {
			appended = message
		}
	}
	if appended.ID == "" || appended.Sequence <= maxOldSequence {
		t.Fatalf("append_sequence did not advance beyond migrated rows: old max=%d appended=%+v", maxOldSequence, appended)
	}

	checkpoint := &SessionCheckpoint{
		Version: 1, ThroughSequence: through.Sequence, ThroughMessageID: through.ID, ThroughAt: through.CreatedAt,
		ReplacementHistory: []SessionCheckpointMessage{
			{Role: "assistant", ToolCalls: []SessionCheckpointToolCall{{ID: "call-1", Name: "lookup", Args: `{"query":"railgrid"}`}}},
			{Role: "tool", Name: "lookup", ToolCallID: "call-1", Content: "structured result"},
		},
	}
	if err := ps.PutSessionSummary(ctx, scope, SessionSummary{
		SessionID: "checkpoint-session", Summary: "structured replacement", ThroughAt: through.CreatedAt,
		MessageCount: 1, CreatedAt: now, UpdatedAt: now, Checkpoint: checkpoint,
	}); err != nil {
		t.Fatalf("persist session checkpoint: %v", err)
	}
	if err := ps.Close(); err != nil {
		t.Fatalf("close store before persistence check: %v", err)
	}

	reopened, err := OpenPostgres(ctx, isolatedDSN)
	if err != nil {
		t.Fatalf("reopen isolated store: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	if err := reopened.EnsureSchema(ctx); err != nil {
		t.Fatalf("ensure reopened schema: %v", err)
	}
	persisted, ok, err := reopened.GetSessionSummary(ctx, scope, "checkpoint-session")
	if err != nil || !ok || persisted.Checkpoint == nil {
		t.Fatalf("checkpoint did not persist after reopen: ok=%v err=%v summary=%+v", ok, err, persisted)
	}
	if persisted.Checkpoint.ThroughSequence != through.Sequence || persisted.Checkpoint.ThroughMessageID != through.ID {
		t.Fatalf("checkpoint boundary changed: got sequence=%d ID=%q; want sequence=%d ID=%q",
			persisted.Checkpoint.ThroughSequence, persisted.Checkpoint.ThroughMessageID, through.Sequence, through.ID)
	}
	gotMessages, err := reopened.LoadRecentMessages(ctx, scope, "checkpoint-session", 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range gotMessages {
		if message.ID == appended.ID && message.Sequence != appended.Sequence {
			t.Fatalf("appended sequence changed after reopen: before=%d after=%d", appended.Sequence, message.Sequence)
		}
	}
	if len(persisted.Checkpoint.ReplacementHistory) != 2 || len(persisted.Checkpoint.ReplacementHistory[0].ToolCalls) != 1 {
		t.Fatalf("structured replacement history did not round-trip: %+v", persisted.Checkpoint.ReplacementHistory)
	}
	if persisted.Checkpoint.ReplacementHistory[0].ToolCalls[0].ID != "call-1" || persisted.Checkpoint.ReplacementHistory[1].ToolCallID != "call-1" {
		t.Fatalf("tool-call pairing changed in persisted checkpoint: %+v", persisted.Checkpoint.ReplacementHistory)
	}
}
