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
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	_ "github.com/lib/pq"
)

// PostgresStore is the durable production Store. Schema is created/updated by
// EnsureSchema (idempotent DDL); every table is scoped by org/workspace so a
// single database serves all tenants.
type PostgresStore struct {
	db *sql.DB
}

// OpenPostgres opens the Postgres-backed store and verifies connectivity.
// Call EnsureSchema before first use.
func OpenPostgres(ctx context.Context, dsn string) (*PostgresStore, error) {
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, fmt.Errorf("open postgres: %w", err)
	}
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(30 * time.Minute)
	pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := db.PingContext(pingCtx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping postgres: %w", err)
	}
	return &PostgresStore{db: db}, nil
}

func (p *PostgresStore) Close() error { return p.db.Close() }

var agentsSchema = []string{
	`CREATE TABLE IF NOT EXISTS agents_messages (
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
	)`,
	`CREATE INDEX IF NOT EXISTS agents_messages_scope_idx
		ON agents_messages (org_uuid, workspace_uuid, agent_name, session_id, created_at DESC, id DESC)`,
	`CREATE TABLE IF NOT EXISTS agents_runs (
		id TEXT PRIMARY KEY,
		org_uuid TEXT NOT NULL,
		workspace_uuid TEXT NOT NULL,
		agent_name TEXT NOT NULL,
		session_id TEXT NOT NULL DEFAULT '',
		trigger_kind TEXT NOT NULL DEFAULT '',
		parent_run_id TEXT NOT NULL DEFAULT '',
		phase TEXT NOT NULL,
		attempt INT NOT NULL DEFAULT 0,
		input TEXT NOT NULL DEFAULT '',
		message TEXT NOT NULL DEFAULT '',
		checkpoint JSONB,
		input_tokens BIGINT NOT NULL DEFAULT 0,
		output_tokens BIGINT NOT NULL DEFAULT 0,
		usd_micros BIGINT NOT NULL DEFAULT 0,
		created_at TIMESTAMPTZ NOT NULL,
		updated_at TIMESTAMPTZ NOT NULL,
		started_at TIMESTAMPTZ,
		finished_at TIMESTAMPTZ
	)`,
	`CREATE INDEX IF NOT EXISTS agents_runs_scope_idx
		ON agents_runs (org_uuid, workspace_uuid, created_at DESC)`,
	`CREATE TABLE IF NOT EXISTS agents_memories (
		id TEXT PRIMARY KEY,
		org_uuid TEXT NOT NULL,
		workspace_uuid TEXT NOT NULL,
		agent_name TEXT NOT NULL,
		title TEXT NOT NULL,
		body TEXT NOT NULL,
		content_encrypted BOOLEAN NOT NULL DEFAULT FALSE,
		content_key_id TEXT NOT NULL DEFAULT '',
		created_at TIMESTAMPTZ NOT NULL,
		updated_at TIMESTAMPTZ NOT NULL
	)`,
	`CREATE INDEX IF NOT EXISTS agents_memories_scope_idx
		ON agents_memories (org_uuid, workspace_uuid, agent_name, updated_at DESC)`,
	`CREATE TABLE IF NOT EXISTS agents_inbox (
		id TEXT PRIMARY KEY,
		org_uuid TEXT NOT NULL,
		workspace_uuid TEXT NOT NULL,
		agent_name TEXT NOT NULL,
		run_id TEXT NOT NULL DEFAULT '',
		kind TEXT NOT NULL,
		state TEXT NOT NULL,
		prompt TEXT NOT NULL,
		payload JSONB,
		response TEXT NOT NULL DEFAULT '',
		created_at TIMESTAMPTZ NOT NULL,
		updated_at TIMESTAMPTZ NOT NULL
	)`,
	`CREATE INDEX IF NOT EXISTS agents_inbox_scope_idx
		ON agents_inbox (org_uuid, workspace_uuid, state, created_at DESC)`,
	`CREATE TABLE IF NOT EXISTS agents_tool_calls (
		id TEXT PRIMARY KEY,
		org_uuid TEXT NOT NULL,
		workspace_uuid TEXT NOT NULL,
		agent_name TEXT NOT NULL,
		run_id TEXT NOT NULL DEFAULT '',
		trigger_kind TEXT NOT NULL DEFAULT '',
		tool TEXT NOT NULL,
		args_digest TEXT NOT NULL DEFAULT '',
		outcome TEXT NOT NULL,
		error TEXT NOT NULL DEFAULT '',
		duration_ms BIGINT NOT NULL DEFAULT 0,
		created_at TIMESTAMPTZ NOT NULL
	)`,
	`CREATE INDEX IF NOT EXISTS agents_tool_calls_scope_idx
		ON agents_tool_calls (org_uuid, workspace_uuid, agent_name, created_at DESC)`,
	`CREATE TABLE IF NOT EXISTS agents_usage (
		org_uuid TEXT NOT NULL,
		workspace_uuid TEXT NOT NULL,
		agent_name TEXT NOT NULL,
		window_start TIMESTAMPTZ NOT NULL,
		input_tokens BIGINT NOT NULL DEFAULT 0,
		output_tokens BIGINT NOT NULL DEFAULT 0,
		usd_micros BIGINT NOT NULL DEFAULT 0,
		updated_at TIMESTAMPTZ NOT NULL,
		PRIMARY KEY (org_uuid, workspace_uuid, agent_name, window_start)
	)`,
	`CREATE TABLE IF NOT EXISTS agents_tenants (
		cluster_id TEXT PRIMARY KEY,
		org_uuid TEXT NOT NULL,
		workspace_uuid TEXT NOT NULL,
		updated_at TIMESTAMPTZ NOT NULL
	)`,
}

func (p *PostgresStore) EnsureSchema(ctx context.Context) error {
	for _, ddl := range agentsSchema {
		if _, err := p.db.ExecContext(ctx, ddl); err != nil {
			return fmt.Errorf("ensure schema: %w", err)
		}
	}
	return nil
}

// ---- transcript --------------------------------------------------------------

func (p *PostgresStore) AppendMessage(ctx context.Context, scope Scope, msg Message) error {
	if err := scope.withAgent(); err != nil {
		return err
	}
	if msg.CreatedAt.IsZero() {
		return fmt.Errorf("message CreatedAt is required")
	}
	meta, err := marshalJSONB(msg.Metadata)
	if err != nil {
		return err
	}
	_, err = p.db.ExecContext(ctx, `
		INSERT INTO agents_messages
			(id, org_uuid, workspace_uuid, agent_name, session_id, run_id, role, content, content_encrypted, content_key_id, metadata, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`,
		msg.ID, scope.OrgUUID, scope.WorkspaceUUID, scope.AgentName, msg.SessionID, msg.RunID,
		msg.Role, msg.Content, msg.ContentEncrypted, msg.ContentKeyID, meta, msg.CreatedAt.UTC())
	return err
}

func (p *PostgresStore) ListMessages(ctx context.Context, scope Scope, sessionID string, limit int, cursor string) (Page, error) {
	if err := scope.withAgent(); err != nil {
		return Page{}, err
	}
	before, beforeID, err := decodeCursor(cursor)
	if err != nil {
		return Page{}, err
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	q := `
		SELECT id, session_id, run_id, role, content, content_encrypted, content_key_id, metadata, created_at
		FROM agents_messages
		WHERE org_uuid=$1 AND workspace_uuid=$2 AND agent_name=$3 AND session_id=$4`
	args := []any{scope.OrgUUID, scope.WorkspaceUUID, scope.AgentName, sessionID}
	if !before.IsZero() {
		q += ` AND (created_at < $5 OR (created_at = $5 AND id < $6))`
		args = append(args, before, beforeID)
	}
	q += fmt.Sprintf(` ORDER BY created_at DESC, id DESC LIMIT %d`, limit)

	rows, err := p.db.QueryContext(ctx, q, args...)
	if err != nil {
		return Page{}, err
	}
	defer rows.Close()
	var items []Message
	for rows.Next() {
		m, err := scanMessage(rows, scope.AgentName)
		if err != nil {
			return Page{}, err
		}
		items = append(items, m)
	}
	page := Page{Items: items}
	if len(items) == limit {
		last := items[len(items)-1]
		page.NextCursor = encodeCursor(last.CreatedAt, last.ID)
	}
	return page, rows.Err()
}

func (p *PostgresStore) LoadRecentMessages(ctx context.Context, scope Scope, sessionID string, limit int) ([]Message, error) {
	if err := scope.withAgent(); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	rows, err := p.db.QueryContext(ctx, fmt.Sprintf(`
		SELECT id, session_id, run_id, role, content, content_encrypted, content_key_id, metadata, created_at
		FROM agents_messages
		WHERE org_uuid=$1 AND workspace_uuid=$2 AND agent_name=$3 AND session_id=$4
		ORDER BY created_at DESC, id DESC LIMIT %d`, limit),
		scope.OrgUUID, scope.WorkspaceUUID, scope.AgentName, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []Message
	for rows.Next() {
		m, err := scanMessage(rows, scope.AgentName)
		if err != nil {
			return nil, err
		}
		items = append(items, m)
	}
	// Reverse to chronological order.
	for i, j := 0, len(items)-1; i < j; i, j = i+1, j-1 {
		items[i], items[j] = items[j], items[i]
	}
	return items, rows.Err()
}

func scanMessage(rows *sql.Rows, agentName string) (Message, error) {
	var m Message
	var meta []byte
	if err := rows.Scan(&m.ID, &m.SessionID, &m.RunID, &m.Role, &m.Content, &m.ContentEncrypted, &m.ContentKeyID, &meta, &m.CreatedAt); err != nil {
		return Message{}, err
	}
	m.AgentName = agentName
	if len(meta) > 0 {
		_ = json.Unmarshal(meta, &m.Metadata)
	}
	m.CreatedAt = m.CreatedAt.UTC()
	return m, nil
}

func (p *PostgresStore) ListSessions(ctx context.Context, scope Scope, limit int) ([]Session, error) {
	if err := scope.withAgent(); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	// One row per session: counts, activity bounds, and the first user message
	// (via a correlated subquery) as a preview label.
	rows, err := p.db.QueryContext(ctx, fmt.Sprintf(`
		SELECT m.session_id, COUNT(*), MIN(m.created_at), MAX(m.created_at),
			(SELECT f.content FROM agents_messages f
			 WHERE f.org_uuid=m.org_uuid AND f.workspace_uuid=m.workspace_uuid
				AND f.agent_name=m.agent_name AND f.session_id=m.session_id
				AND f.role='user' AND f.content_encrypted=FALSE
			 ORDER BY f.created_at ASC, f.id ASC LIMIT 1)
		FROM agents_messages m
		WHERE m.org_uuid=$1 AND m.workspace_uuid=$2 AND m.agent_name=$3
		GROUP BY m.session_id, m.org_uuid, m.workspace_uuid, m.agent_name
		ORDER BY MAX(m.created_at) DESC LIMIT %d`, limit),
		scope.OrgUUID, scope.WorkspaceUUID, scope.AgentName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Session
	for rows.Next() {
		var s Session
		var preview sql.NullString
		if err := rows.Scan(&s.ID, &s.MessageCount, &s.CreatedAt, &s.LastActivity, &preview); err != nil {
			return nil, err
		}
		s.CreatedAt = s.CreatedAt.UTC()
		s.LastActivity = s.LastActivity.UTC()
		s.Preview = previewText(preview.String)
		out = append(out, s)
	}
	return out, rows.Err()
}

func (p *PostgresStore) DeleteSession(ctx context.Context, scope Scope, sessionID string) error {
	if err := scope.withAgent(); err != nil {
		return err
	}
	_, err := p.db.ExecContext(ctx, `
		DELETE FROM agents_messages
		WHERE org_uuid=$1 AND workspace_uuid=$2 AND agent_name=$3 AND session_id=$4`,
		scope.OrgUUID, scope.WorkspaceUUID, scope.AgentName, sessionID)
	return err
}

// ---- runs ---------------------------------------------------------------------

func (p *PostgresStore) SaveRun(ctx context.Context, scope Scope, run Run) error {
	if err := scope.withAgent(); err != nil {
		return err
	}
	if run.ID == "" {
		return fmt.Errorf("run ID is required")
	}
	_, err := p.db.ExecContext(ctx, `
		INSERT INTO agents_runs
			(id, org_uuid, workspace_uuid, agent_name, session_id, trigger_kind, parent_run_id, phase, attempt,
			 input, message, checkpoint, input_tokens, output_tokens, usd_micros, created_at, updated_at, started_at, finished_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19)
		ON CONFLICT (id) DO UPDATE SET
			phase=EXCLUDED.phase, attempt=EXCLUDED.attempt, message=EXCLUDED.message,
			checkpoint=EXCLUDED.checkpoint, input_tokens=EXCLUDED.input_tokens,
			output_tokens=EXCLUDED.output_tokens, usd_micros=EXCLUDED.usd_micros,
			updated_at=EXCLUDED.updated_at, started_at=EXCLUDED.started_at, finished_at=EXCLUDED.finished_at`,
		run.ID, scope.OrgUUID, scope.WorkspaceUUID, run.AgentName, run.SessionID, run.Trigger, run.ParentRunID,
		string(run.Phase), run.Attempt, run.Input, run.Message, nullBytes(run.Checkpoint),
		run.InputTokens, run.OutputTokens, run.USDMicros,
		run.CreatedAt.UTC(), run.UpdatedAt.UTC(), nullTime(run.StartedAt), nullTime(run.FinishedAt))
	return err
}

func (p *PostgresStore) GetRun(ctx context.Context, scope Scope, id string) (Run, error) {
	if err := scope.validate(); err != nil {
		return Run{}, err
	}
	row := p.db.QueryRowContext(ctx, `
		SELECT id, agent_name, session_id, trigger_kind, parent_run_id, phase, attempt, input, message,
		       checkpoint, input_tokens, output_tokens, usd_micros, created_at, updated_at, started_at, finished_at
		FROM agents_runs WHERE org_uuid=$1 AND workspace_uuid=$2 AND id=$3`,
		scope.OrgUUID, scope.WorkspaceUUID, id)
	run, err := scanRun(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Run{}, fmt.Errorf("run %q not found", id)
	}
	return run, err
}

func (p *PostgresStore) ClaimRun(ctx context.Context, scope Scope, id, _ string, now time.Time) (Run, error) {
	if err := scope.validate(); err != nil {
		return Run{}, err
	}
	res, err := p.db.ExecContext(ctx, `
		UPDATE agents_runs SET phase=$4, updated_at=$5, started_at=COALESCE(started_at, $5)
		WHERE org_uuid=$1 AND workspace_uuid=$2 AND id=$3 AND phase <> $4`,
		scope.OrgUUID, scope.WorkspaceUUID, id, string(RunPhaseRunning), now.UTC())
	if err != nil {
		return Run{}, err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return Run{}, fmt.Errorf("run %q not found or already claimed", id)
	}
	return p.GetRun(ctx, scope, id)
}

func (p *PostgresStore) ListRuns(ctx context.Context, scope Scope, limit int) ([]Run, error) {
	if err := scope.validate(); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	q := `
		SELECT id, agent_name, session_id, trigger_kind, parent_run_id, phase, attempt, input, message,
		       checkpoint, input_tokens, output_tokens, usd_micros, created_at, updated_at, started_at, finished_at
		FROM agents_runs WHERE org_uuid=$1 AND workspace_uuid=$2`
	args := []any{scope.OrgUUID, scope.WorkspaceUUID}
	if scope.AgentName != "" {
		q += ` AND agent_name=$3`
		args = append(args, scope.AgentName)
	}
	q += fmt.Sprintf(` ORDER BY created_at DESC LIMIT %d`, limit)
	rows, err := p.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Run
	for rows.Next() {
		run, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, run)
	}
	return out, rows.Err()
}

type rowScanner interface{ Scan(dest ...any) error }

func scanRun(r rowScanner) (Run, error) {
	var run Run
	var phase string
	var checkpoint []byte
	var started, finished sql.NullTime
	if err := r.Scan(&run.ID, &run.AgentName, &run.SessionID, &run.Trigger, &run.ParentRunID, &phase, &run.Attempt,
		&run.Input, &run.Message, &checkpoint, &run.InputTokens, &run.OutputTokens, &run.USDMicros,
		&run.CreatedAt, &run.UpdatedAt, &started, &finished); err != nil {
		return Run{}, err
	}
	run.Phase = RunPhase(phase)
	run.Checkpoint = checkpoint
	if started.Valid {
		t := started.Time.UTC()
		run.StartedAt = &t
	}
	if finished.Valid {
		t := finished.Time.UTC()
		run.FinishedAt = &t
	}
	run.CreatedAt, run.UpdatedAt = run.CreatedAt.UTC(), run.UpdatedAt.UTC()
	return run, nil
}

// ---- memories ------------------------------------------------------------------

func (p *PostgresStore) PutMemory(ctx context.Context, scope Scope, m Memory) error {
	if err := scope.withAgent(); err != nil {
		return err
	}
	if m.ID == "" {
		return fmt.Errorf("memory ID is required")
	}
	_, err := p.db.ExecContext(ctx, `
		INSERT INTO agents_memories (id, org_uuid, workspace_uuid, agent_name, title, body, content_encrypted, content_key_id, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
		ON CONFLICT (id) DO UPDATE SET title=EXCLUDED.title, body=EXCLUDED.body, updated_at=EXCLUDED.updated_at`,
		m.ID, scope.OrgUUID, scope.WorkspaceUUID, m.AgentName, m.Title, m.Body, m.ContentEncrypted, m.ContentKeyID,
		m.CreatedAt.UTC(), m.UpdatedAt.UTC())
	return err
}

func (p *PostgresStore) ListMemories(ctx context.Context, scope Scope, limit int) ([]Memory, error) {
	if err := scope.withAgent(); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := p.db.QueryContext(ctx, fmt.Sprintf(`
		SELECT id, agent_name, title, body, content_encrypted, content_key_id, created_at, updated_at
		FROM agents_memories WHERE org_uuid=$1 AND workspace_uuid=$2 AND agent_name=$3
		ORDER BY updated_at DESC LIMIT %d`, limit),
		scope.OrgUUID, scope.WorkspaceUUID, scope.AgentName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Memory
	for rows.Next() {
		var m Memory
		if err := rows.Scan(&m.ID, &m.AgentName, &m.Title, &m.Body, &m.ContentEncrypted, &m.ContentKeyID, &m.CreatedAt, &m.UpdatedAt); err != nil {
			return nil, err
		}
		m.CreatedAt, m.UpdatedAt = m.CreatedAt.UTC(), m.UpdatedAt.UTC()
		out = append(out, m)
	}
	return out, rows.Err()
}

func (p *PostgresStore) DeleteMemory(ctx context.Context, scope Scope, id string) error {
	if err := scope.withAgent(); err != nil {
		return err
	}
	_, err := p.db.ExecContext(ctx, `DELETE FROM agents_memories WHERE org_uuid=$1 AND workspace_uuid=$2 AND id=$3`,
		scope.OrgUUID, scope.WorkspaceUUID, id)
	return err
}

// ---- inbox ----------------------------------------------------------------------

func (p *PostgresStore) AddInboxItem(ctx context.Context, scope Scope, item InboxItem) error {
	if err := scope.validate(); err != nil {
		return err
	}
	if item.ID == "" {
		return fmt.Errorf("inbox item ID is required")
	}
	payload, err := marshalJSONB(item.Payload)
	if err != nil {
		return err
	}
	_, err = p.db.ExecContext(ctx, `
		INSERT INTO agents_inbox (id, org_uuid, workspace_uuid, agent_name, run_id, kind, state, prompt, payload, response, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`,
		item.ID, scope.OrgUUID, scope.WorkspaceUUID, item.AgentName, item.RunID, string(item.Kind), string(item.State),
		item.Prompt, payload, item.Response, item.CreatedAt.UTC(), item.UpdatedAt.UTC())
	return err
}

func (p *PostgresStore) ListInbox(ctx context.Context, scope Scope, state InboxItemState) ([]InboxItem, error) {
	if err := scope.validate(); err != nil {
		return nil, err
	}
	q := `
		SELECT id, agent_name, run_id, kind, state, prompt, payload, response, created_at, updated_at
		FROM agents_inbox WHERE org_uuid=$1 AND workspace_uuid=$2`
	args := []any{scope.OrgUUID, scope.WorkspaceUUID}
	if state != "" {
		args = append(args, string(state))
		q += fmt.Sprintf(` AND state=$%d`, len(args))
	}
	if scope.AgentName != "" {
		args = append(args, scope.AgentName)
		q += fmt.Sprintf(` AND agent_name=$%d`, len(args))
	}
	q += ` ORDER BY created_at DESC LIMIT 200`
	rows, err := p.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []InboxItem
	for rows.Next() {
		var it InboxItem
		var kind, st string
		var payload []byte
		if err := rows.Scan(&it.ID, &it.AgentName, &it.RunID, &kind, &st, &it.Prompt, &payload, &it.Response, &it.CreatedAt, &it.UpdatedAt); err != nil {
			return nil, err
		}
		it.Kind, it.State = InboxItemKind(kind), InboxItemState(st)
		if len(payload) > 0 {
			_ = json.Unmarshal(payload, &it.Payload)
		}
		it.CreatedAt, it.UpdatedAt = it.CreatedAt.UTC(), it.UpdatedAt.UTC()
		out = append(out, it)
	}
	return out, rows.Err()
}

func (p *PostgresStore) ResolveInboxItem(ctx context.Context, scope Scope, id string, state InboxItemState, response string, now time.Time) (InboxItem, error) {
	if err := scope.validate(); err != nil {
		return InboxItem{}, err
	}
	res, err := p.db.ExecContext(ctx, `
		UPDATE agents_inbox SET state=$4, response=$5, updated_at=$6
		WHERE org_uuid=$1 AND workspace_uuid=$2 AND id=$3`,
		scope.OrgUUID, scope.WorkspaceUUID, id, string(state), response, now.UTC())
	if err != nil {
		return InboxItem{}, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return InboxItem{}, fmt.Errorf("inbox item %q not found", id)
	}
	items, err := p.ListInbox(ctx, scope, "")
	if err != nil {
		return InboxItem{}, err
	}
	for _, it := range items {
		if it.ID == id {
			return it, nil
		}
	}
	return InboxItem{}, fmt.Errorf("inbox item %q not found after update", id)
}

// ---- audit + usage -----------------------------------------------------------------

func (p *PostgresStore) AppendToolCall(ctx context.Context, scope Scope, tc ToolCall) error {
	if err := scope.validate(); err != nil {
		return err
	}
	_, err := p.db.ExecContext(ctx, `
		INSERT INTO agents_tool_calls (id, org_uuid, workspace_uuid, agent_name, run_id, trigger_kind, tool, args_digest, outcome, error, duration_ms, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`,
		tc.ID, scope.OrgUUID, scope.WorkspaceUUID, tc.AgentName, tc.RunID, tc.Trigger, tc.Tool, tc.ArgsDigest,
		tc.Outcome, tc.Error, tc.DurationMS, tc.CreatedAt.UTC())
	return err
}

func (p *PostgresStore) AddUsage(ctx context.Context, scope Scope, agentName string, in, out, usdMicros int64, now time.Time, window time.Duration) (Usage, error) {
	if err := scope.validate(); err != nil {
		return Usage{}, err
	}
	ws := windowStart(now, window)
	row := p.db.QueryRowContext(ctx, `
		INSERT INTO agents_usage (org_uuid, workspace_uuid, agent_name, window_start, input_tokens, output_tokens, usd_micros, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
		ON CONFLICT (org_uuid, workspace_uuid, agent_name, window_start) DO UPDATE SET
			input_tokens = agents_usage.input_tokens + EXCLUDED.input_tokens,
			output_tokens = agents_usage.output_tokens + EXCLUDED.output_tokens,
			usd_micros = agents_usage.usd_micros + EXCLUDED.usd_micros,
			updated_at = EXCLUDED.updated_at
		RETURNING input_tokens, output_tokens, usd_micros, updated_at`,
		scope.OrgUUID, scope.WorkspaceUUID, agentName, ws, in, out, usdMicros, now.UTC())
	u := Usage{AgentName: agentName, WindowStart: ws}
	if err := row.Scan(&u.InputTokens, &u.OutputTokens, &u.USDMicros, &u.UpdatedAt); err != nil {
		return Usage{}, err
	}
	u.UpdatedAt = u.UpdatedAt.UTC()
	return u, nil
}

func (p *PostgresStore) GetUsage(ctx context.Context, scope Scope, agentName string, now time.Time, window time.Duration) (Usage, error) {
	if err := scope.validate(); err != nil {
		return Usage{}, err
	}
	ws := windowStart(now, window)
	row := p.db.QueryRowContext(ctx, `
		SELECT input_tokens, output_tokens, usd_micros, updated_at FROM agents_usage
		WHERE org_uuid=$1 AND workspace_uuid=$2 AND agent_name=$3 AND window_start=$4`,
		scope.OrgUUID, scope.WorkspaceUUID, agentName, ws)
	u := Usage{AgentName: agentName, WindowStart: ws}
	err := row.Scan(&u.InputTokens, &u.OutputTokens, &u.USDMicros, &u.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Usage{AgentName: agentName, WindowStart: ws}, nil
	}
	if err != nil {
		return Usage{}, err
	}
	u.UpdatedAt = u.UpdatedAt.UTC()
	return u, nil
}

// ---- tenant refs ---------------------------------------------------------------------

func (p *PostgresStore) SaveTenantRef(ctx context.Context, clusterID string, ref TenantRef) error {
	if clusterID == "" {
		return fmt.Errorf("cluster ID is required")
	}
	_, err := p.db.ExecContext(ctx, `
		INSERT INTO agents_tenants (cluster_id, org_uuid, workspace_uuid, updated_at)
		VALUES ($1,$2,$3,$4)
		ON CONFLICT (cluster_id) DO UPDATE SET org_uuid=EXCLUDED.org_uuid, workspace_uuid=EXCLUDED.workspace_uuid, updated_at=EXCLUDED.updated_at`,
		clusterID, ref.OrgUUID, ref.WorkspaceUUID, ref.UpdatedAt.UTC())
	return err
}

func (p *PostgresStore) GetTenantRef(ctx context.Context, clusterID string) (TenantRef, bool, error) {
	var ref TenantRef
	row := p.db.QueryRowContext(ctx, `SELECT org_uuid, workspace_uuid, updated_at FROM agents_tenants WHERE cluster_id=$1`, clusterID)
	err := row.Scan(&ref.OrgUUID, &ref.WorkspaceUUID, &ref.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return TenantRef{}, false, nil
	}
	if err != nil {
		return TenantRef{}, false, err
	}
	ref.UpdatedAt = ref.UpdatedAt.UTC()
	return ref, true, nil
}

// ---- teardown -------------------------------------------------------------------------

func (p *PostgresStore) DeleteAgentData(ctx context.Context, scope Scope, agentName string) error {
	if err := scope.validate(); err != nil {
		return err
	}
	for _, table := range []string{"agents_messages", "agents_runs", "agents_memories", "agents_inbox", "agents_tool_calls", "agents_usage"} {
		if _, err := p.db.ExecContext(ctx,
			fmt.Sprintf(`DELETE FROM %s WHERE org_uuid=$1 AND workspace_uuid=$2 AND agent_name=$3`, table),
			scope.OrgUUID, scope.WorkspaceUUID, agentName); err != nil {
			return err
		}
	}
	return nil
}

// ---- helpers ----------------------------------------------------------------------------

// marshalJSONB returns a driver-level NULL for empty values (a nil []byte is
// sent as an empty string, which JSONB rejects) and marshaled JSON otherwise.
func marshalJSONB(v any) (any, error) {
	if v == nil {
		return nil, nil
	}
	if m, ok := v.(map[string]any); ok && len(m) == 0 {
		return nil, nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return b, nil
}

func nullBytes(b []byte) any {
	if len(b) == 0 {
		return nil
	}
	return []byte(b)
}

func nullTime(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.UTC()
}

// Compile-time interface check.
var _ Store = (*PostgresStore)(nil)
