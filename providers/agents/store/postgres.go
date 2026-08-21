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
	"strings"
	"time"

	"github.com/lib/pq"
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
		output TEXT NOT NULL DEFAULT '',
		sources JSONB,
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
	// Runs predating the result-on-the-run-record change carry neither column;
	// migrate in place (idempotent).
	`ALTER TABLE agents_runs ADD COLUMN IF NOT EXISTS output TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE agents_runs ADD COLUMN IF NOT EXISTS sources JSONB`,
	`ALTER TABLE agents_runs ADD COLUMN IF NOT EXISTS idempotency_key TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE agents_runs ADD COLUMN IF NOT EXISTS delivery JSONB`,
	// Partial unique index: at most one run per (tenant, agent, key), while the
	// overwhelming majority of runs carry no key at all and are unconstrained.
	`CREATE UNIQUE INDEX IF NOT EXISTS agents_runs_idempotency_idx
		ON agents_runs (org_uuid, workspace_uuid, agent_name, idempotency_key)
		WHERE idempotency_key <> ''`,
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
		args TEXT NOT NULL DEFAULT '',
		result TEXT NOT NULL DEFAULT '',
		outcome TEXT NOT NULL,
		error TEXT NOT NULL DEFAULT '',
		duration_ms BIGINT NOT NULL DEFAULT 0,
		created_at TIMESTAMPTZ NOT NULL
	)`,
	// Pre-runs-API deployments stored a clipped args_digest and no result;
	// migrate in place (idempotent).
	`ALTER TABLE agents_tool_calls ADD COLUMN IF NOT EXISTS args TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE agents_tool_calls ADD COLUMN IF NOT EXISTS result TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE agents_tool_calls DROP COLUMN IF EXISTS args_digest`,
	`CREATE INDEX IF NOT EXISTS agents_tool_calls_scope_idx
		ON agents_tool_calls (org_uuid, workspace_uuid, agent_name, created_at DESC)`,
	`CREATE INDEX IF NOT EXISTS agents_tool_calls_run_idx
		ON agents_tool_calls (org_uuid, workspace_uuid, run_id, created_at ASC)`,
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
	// Reverse lookup (org, workspace) → cluster, for the recovery sweep.
	`CREATE INDEX IF NOT EXISTS agents_tenants_scope_idx
		ON agents_tenants (org_uuid, workspace_uuid)`,
	`CREATE TABLE IF NOT EXISTS agents_session_summaries (
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
	)`,
	// The sweep scans by phase + staleness across all tenants, so this index is
	// the one that keeps it from being a full table scan as run history grows.
	`CREATE INDEX IF NOT EXISTS agents_runs_phase_updated_idx
		ON agents_runs (phase, updated_at)`,
	`CREATE TABLE IF NOT EXISTS agents_agent_creation_claims (
		org_uuid TEXT NOT NULL,
		workspace_uuid TEXT NOT NULL,
		agent_name TEXT NOT NULL,
		claimed_at TIMESTAMPTZ NOT NULL,
		PRIMARY KEY (org_uuid, workspace_uuid, agent_name)
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
	if _, err := p.db.ExecContext(ctx, `
		DELETE FROM agents_messages
		WHERE org_uuid=$1 AND workspace_uuid=$2 AND agent_name=$3 AND session_id=$4`,
		scope.OrgUUID, scope.WorkspaceUUID, scope.AgentName, sessionID); err != nil {
		return err
	}
	// The summary stands for messages that no longer exist; keeping it would
	// replay a wiped conversation back into the model after "/new".
	_, err := p.db.ExecContext(ctx, `
		DELETE FROM agents_session_summaries
		WHERE org_uuid=$1 AND workspace_uuid=$2 AND agent_name=$3 AND session_id=$4`,
		scope.OrgUUID, scope.WorkspaceUUID, scope.AgentName, sessionID)
	return err
}

func (p *PostgresStore) PutSessionSummary(ctx context.Context, scope Scope, s SessionSummary) error {
	if err := scope.withAgent(); err != nil {
		return err
	}
	if strings.TrimSpace(s.SessionID) == "" {
		return fmt.Errorf("session ID is required")
	}
	_, err := p.db.ExecContext(ctx, `
		INSERT INTO agents_session_summaries
			(org_uuid, workspace_uuid, agent_name, session_id, summary, through_at, message_count, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
		ON CONFLICT (org_uuid, workspace_uuid, agent_name, session_id) DO UPDATE SET
			summary=EXCLUDED.summary, through_at=EXCLUDED.through_at,
			message_count=EXCLUDED.message_count, updated_at=EXCLUDED.updated_at`,
		scope.OrgUUID, scope.WorkspaceUUID, scope.AgentName, s.SessionID,
		s.Summary, s.ThroughAt.UTC(), s.MessageCount, s.CreatedAt.UTC(), s.UpdatedAt.UTC())
	return err
}

func (p *PostgresStore) GetSessionSummary(ctx context.Context, scope Scope, sessionID string) (SessionSummary, bool, error) {
	if err := scope.withAgent(); err != nil {
		return SessionSummary{}, false, err
	}
	out := SessionSummary{SessionID: sessionID}
	row := p.db.QueryRowContext(ctx, `
		SELECT summary, through_at, message_count, created_at, updated_at
		FROM agents_session_summaries
		WHERE org_uuid=$1 AND workspace_uuid=$2 AND agent_name=$3 AND session_id=$4`,
		scope.OrgUUID, scope.WorkspaceUUID, scope.AgentName, sessionID)
	err := row.Scan(&out.Summary, &out.ThroughAt, &out.MessageCount, &out.CreatedAt, &out.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return SessionSummary{}, false, nil
	}
	if err != nil {
		return SessionSummary{}, false, err
	}
	out.ThroughAt, out.CreatedAt, out.UpdatedAt = out.ThroughAt.UTC(), out.CreatedAt.UTC(), out.UpdatedAt.UTC()
	return out, true, nil
}

// ---- runs ---------------------------------------------------------------------

func (p *PostgresStore) SaveRun(ctx context.Context, scope Scope, run Run) error {
	if err := scope.withAgent(); err != nil {
		return err
	}
	if run.ID == "" {
		return fmt.Errorf("run ID is required")
	}
	sources, err := marshalJSONB(run.Sources)
	if err != nil {
		return err
	}
	var delivery any
	if run.Delivery != nil {
		if delivery, err = marshalJSONB(run.Delivery); err != nil {
			return err
		}
	}
	_, err = p.db.ExecContext(ctx, `
		INSERT INTO agents_runs
			(id, org_uuid, workspace_uuid, agent_name, session_id, trigger_kind, parent_run_id, phase, attempt,
			 input, output, sources, idempotency_key, delivery, message, checkpoint, input_tokens, output_tokens, usd_micros, created_at, updated_at, started_at, finished_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23)
		ON CONFLICT (id) DO UPDATE SET
			phase=EXCLUDED.phase, attempt=EXCLUDED.attempt, message=EXCLUDED.message,
			output=EXCLUDED.output, sources=EXCLUDED.sources, delivery=EXCLUDED.delivery,
			checkpoint=EXCLUDED.checkpoint, input_tokens=EXCLUDED.input_tokens,
			output_tokens=EXCLUDED.output_tokens, usd_micros=EXCLUDED.usd_micros,
			updated_at=EXCLUDED.updated_at, started_at=EXCLUDED.started_at, finished_at=EXCLUDED.finished_at`,
		run.ID, scope.OrgUUID, scope.WorkspaceUUID, run.AgentName, run.SessionID, run.Trigger, run.ParentRunID,
		string(run.Phase), run.Attempt, run.Input, run.Output, sources, run.IdempotencyKey, delivery, run.Message, nullBytes(run.Checkpoint),
		run.InputTokens, run.OutputTokens, run.USDMicros,
		run.CreatedAt.UTC(), run.UpdatedAt.UTC(), nullTime(run.StartedAt), nullTime(run.FinishedAt))
	return err
}

func (p *PostgresStore) FinalizeRun(ctx context.Context, scope Scope, run Run) (bool, error) {
	if err := scope.withAgent(); err != nil {
		return false, err
	}
	if run.ID == "" {
		return false, fmt.Errorf("run ID is required")
	}
	if !terminalRunPhase(run.Phase) {
		return false, fmt.Errorf("run phase %q is not terminal", run.Phase)
	}
	sources, err := marshalJSONB(run.Sources)
	if err != nil {
		return false, err
	}
	var delivery any
	if run.Delivery != nil {
		if delivery, err = marshalJSONB(run.Delivery); err != nil {
			return false, err
		}
	}
	result, err := p.db.ExecContext(ctx, `
		UPDATE agents_runs SET
			phase=$4, attempt=$5, message=$6, output=$7, sources=$8, delivery=$9,
			checkpoint=$10, input_tokens=$11, output_tokens=$12, usd_micros=$13,
			updated_at=$14, started_at=$15, finished_at=$16
		WHERE org_uuid=$1 AND workspace_uuid=$2 AND id=$3
			AND phase NOT IN ('Succeeded', 'Failed', 'Aborted')`,
		scope.OrgUUID, scope.WorkspaceUUID, run.ID, string(run.Phase), run.Attempt, run.Message,
		run.Output, sources, delivery, nullBytes(run.Checkpoint), run.InputTokens, run.OutputTokens,
		run.USDMicros, run.UpdatedAt.UTC(), nullTime(run.StartedAt), nullTime(run.FinishedAt))
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	return n == 1, nil
}

// runColumns is the run SELECT list, shared by every read path so a schema
// change cannot drift one query out of step with scanRun.
const runColumns = `id, agent_name, session_id, trigger_kind, parent_run_id, phase, attempt, input, output, sources, idempotency_key, delivery, message,
		       checkpoint, input_tokens, output_tokens, usd_micros, created_at, updated_at, started_at, finished_at`

func (p *PostgresStore) GetRun(ctx context.Context, scope Scope, id string) (Run, error) {
	if err := scope.validate(); err != nil {
		return Run{}, err
	}
	row := p.db.QueryRowContext(ctx, `
		SELECT `+runColumns+`
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
		SELECT ` + runColumns + `
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

func (p *PostgresStore) QueryRuns(ctx context.Context, scope Scope, q RunQuery) (RunPage, error) {
	if err := scope.validate(); err != nil {
		return RunPage{}, err
	}
	limit := q.Limit
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	before, beforeID, err := decodeCursor(q.Cursor)
	if err != nil {
		return RunPage{}, err
	}
	qs := `
		SELECT ` + runColumns + `
		FROM agents_runs WHERE org_uuid=$1 AND workspace_uuid=$2`
	args := []any{scope.OrgUUID, scope.WorkspaceUUID}
	add := func(clause string, v any) {
		args = append(args, v)
		qs += fmt.Sprintf(clause, len(args))
	}
	if scope.AgentName != "" {
		add(` AND agent_name=$%d`, scope.AgentName)
	}
	if q.Phase != "" {
		add(` AND phase=$%d`, string(q.Phase))
	}
	if q.Trigger != "" {
		add(` AND trigger_kind=$%d`, q.Trigger)
	}
	if q.SessionID != "" {
		add(` AND session_id=$%d`, q.SessionID)
	}
	if q.ParentRunID != "" {
		add(` AND parent_run_id=$%d`, q.ParentRunID)
	}
	if !before.IsZero() {
		args = append(args, before, beforeID)
		qs += fmt.Sprintf(` AND (created_at < $%d OR (created_at = $%d AND id < $%d))`, len(args)-1, len(args)-1, len(args))
	}
	qs += fmt.Sprintf(` ORDER BY created_at DESC, id DESC LIMIT %d`, limit)
	rows, err := p.db.QueryContext(ctx, qs, args...)
	if err != nil {
		return RunPage{}, err
	}
	defer rows.Close()
	var out []Run
	for rows.Next() {
		run, err := scanRun(rows)
		if err != nil {
			return RunPage{}, err
		}
		out = append(out, run)
	}
	page := RunPage{Items: out}
	if len(out) == limit {
		last := out[len(out)-1]
		page.NextCursor = encodeCursor(last.CreatedAt, last.ID)
	}
	return page, rows.Err()
}

type rowScanner interface{ Scan(dest ...any) error }

func scanRun(r rowScanner) (Run, error) { return scanScopedRun(r, nil) }

// scanScopedRun scans the runColumns list, optionally preceded by org_uuid and
// workspace_uuid into sc — the shape ListUnfinishedRuns selects, since it reads
// across tenants and needs each row's scope. A nil sc means the plain column
// list, keeping one scanner for both.
func scanScopedRun(r rowScanner, sc *Scope) (Run, error) {
	var run Run
	var phase string
	var checkpoint, sources, delivery []byte
	var started, finished sql.NullTime
	dest := []any{}
	if sc != nil {
		dest = append(dest, &sc.OrgUUID, &sc.WorkspaceUUID)
	}
	dest = append(dest, &run.ID, &run.AgentName, &run.SessionID, &run.Trigger, &run.ParentRunID, &phase, &run.Attempt,
		&run.Input, &run.Output, &sources, &run.IdempotencyKey, &delivery, &run.Message, &checkpoint, &run.InputTokens, &run.OutputTokens, &run.USDMicros,
		&run.CreatedAt, &run.UpdatedAt, &started, &finished)
	if err := r.Scan(dest...); err != nil {
		return Run{}, err
	}
	run.Phase = RunPhase(phase)
	run.Checkpoint = checkpoint
	if len(sources) > 0 {
		if err := json.Unmarshal(sources, &run.Sources); err != nil {
			return Run{}, fmt.Errorf("decode run sources: %w", err)
		}
	}
	if len(delivery) > 0 {
		if err := json.Unmarshal(delivery, &run.Delivery); err != nil {
			return Run{}, fmt.Errorf("decode run delivery: %w", err)
		}
	}
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
		INSERT INTO agents_tool_calls (id, org_uuid, workspace_uuid, agent_name, run_id, trigger_kind, tool, args, result, outcome, error, duration_ms, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`,
		tc.ID, scope.OrgUUID, scope.WorkspaceUUID, tc.AgentName, tc.RunID, tc.Trigger, tc.Tool, tc.Args, tc.Result,
		tc.Outcome, tc.Error, tc.DurationMS, tc.CreatedAt.UTC())
	return err
}

func (p *PostgresStore) ListToolCalls(ctx context.Context, scope Scope, runID string) ([]ToolCall, error) {
	if err := scope.validate(); err != nil {
		return nil, err
	}
	rows, err := p.db.QueryContext(ctx, `
		SELECT id, agent_name, run_id, trigger_kind, tool, args, result, outcome, error, duration_ms, created_at
		FROM agents_tool_calls
		WHERE org_uuid=$1 AND workspace_uuid=$2 AND run_id=$3
		ORDER BY created_at ASC, id ASC LIMIT 500`,
		scope.OrgUUID, scope.WorkspaceUUID, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ToolCall
	for rows.Next() {
		var tc ToolCall
		if err := rows.Scan(&tc.ID, &tc.AgentName, &tc.RunID, &tc.Trigger, &tc.Tool, &tc.Args, &tc.Result,
			&tc.Outcome, &tc.Error, &tc.DurationMS, &tc.CreatedAt); err != nil {
			return nil, err
		}
		tc.CreatedAt = tc.CreatedAt.UTC()
		out = append(out, tc)
	}
	return out, rows.Err()
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

func (p *PostgresStore) FindClusterForScope(ctx context.Context, orgUUID, workspaceUUID string) (string, bool, error) {
	if strings.TrimSpace(orgUUID) == "" || strings.TrimSpace(workspaceUUID) == "" {
		return "", false, fmt.Errorf("org and workspace are required")
	}
	var clusterID string
	// Newest mapping wins: a workspace is served by one cluster, but a stale row
	// can survive a re-provision, and the recent one is the live one.
	row := p.db.QueryRowContext(ctx, `
		SELECT cluster_id FROM agents_tenants
		WHERE org_uuid=$1 AND workspace_uuid=$2
		ORDER BY updated_at DESC LIMIT 1`, orgUUID, workspaceUUID)
	err := row.Scan(&clusterID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return clusterID, true, nil
}

func (p *PostgresStore) FindRunByIdempotencyKey(ctx context.Context, scope Scope, key string) (Run, bool, error) {
	if err := scope.withAgent(); err != nil {
		return Run{}, false, err
	}
	if strings.TrimSpace(key) == "" {
		return Run{}, false, nil
	}
	row := p.db.QueryRowContext(ctx, `
		SELECT `+runColumns+`
		FROM agents_runs
		WHERE org_uuid=$1 AND workspace_uuid=$2 AND agent_name=$3 AND idempotency_key=$4`,
		scope.OrgUUID, scope.WorkspaceUUID, scope.AgentName, key)
	run, err := scanRun(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Run{}, false, nil
	}
	if err != nil {
		return Run{}, false, err
	}
	return run, true, nil
}

func (p *PostgresStore) ClaimAgentCreation(ctx context.Context, scope Scope) (bool, error) {
	if err := scope.withAgent(); err != nil {
		return false, err
	}
	result, err := p.db.ExecContext(ctx, `
		INSERT INTO agents_agent_creation_claims (org_uuid, workspace_uuid, agent_name, claimed_at)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (org_uuid, workspace_uuid, agent_name) DO NOTHING`,
		scope.OrgUUID, scope.WorkspaceUUID, scope.AgentName, time.Now().UTC())
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	return n == 1, nil
}

// ---- recovery -------------------------------------------------------------------------

func (p *PostgresStore) ListUnfinishedRuns(ctx context.Context, phases []RunPhase, updatedBefore time.Time, limit int) ([]ScopedRun, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	names := make([]string, 0, len(phases))
	for _, ph := range phases {
		names = append(names, string(ph))
	}
	// Cross-tenant by design (see the Store interface): a restart has to find
	// stranded work it has no request scope for.
	qs := `
		SELECT org_uuid, workspace_uuid, ` + runColumns + `
		FROM agents_runs WHERE updated_at < $1`
	args := []any{updatedBefore.UTC()}
	if len(names) > 0 {
		qs += ` AND phase = ANY($2)`
		args = append(args, pq.Array(names))
	}
	qs += fmt.Sprintf(` ORDER BY updated_at ASC LIMIT %d`, limit)
	rows, err := p.db.QueryContext(ctx, qs, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ScopedRun
	for rows.Next() {
		var sc Scope
		run, err := scanScopedRun(rows, &sc)
		if err != nil {
			return nil, err
		}
		sc.AgentName = run.AgentName
		out = append(out, ScopedRun{Scope: sc, Run: run})
	}
	return out, rows.Err()
}

// ---- teardown -------------------------------------------------------------------------

func (p *PostgresStore) DeleteAgentData(ctx context.Context, scope Scope, agentName string) error {
	if err := scope.validate(); err != nil {
		return err
	}
	for _, table := range []string{"agents_messages", "agents_runs", "agents_memories", "agents_inbox", "agents_tool_calls", "agents_usage", "agents_session_summaries", "agents_agent_creation_claims"} {
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
	// A nil slice inside a non-nil interface is not caught above; it would be
	// stored as a literal JSON null rather than SQL NULL.
	if l, ok := v.([]string); ok && len(l) == 0 {
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
