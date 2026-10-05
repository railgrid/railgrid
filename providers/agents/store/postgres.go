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
	"errors"
	"fmt"
	"strings"
	"time"

	// Registers the "postgres" database/sql driver that OpenPostgres names. The
	// import used to ride along with a pq.Array call; when that call went, so
	// did the driver, and the provider failed at startup with "unknown driver".
	_ "github.com/lib/pq"
)

// PostgresStore is the durable production Store. Schema is created/updated by
// EnsureSchema (idempotent DDL); every table is scoped by org/workspace so a
// single database serves all tenants.
type PostgresStore struct {
	db *sql.DB
}

// scopedExecutor is the part of database/sql used by scoped store operations.
// Mutations use either the pool (canonical scopes) or a transaction (legacy
// fallback scopes while they serialize with tenant mapping).
type scopedExecutor interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

type scopedMutation struct {
	scope    Scope
	executor scopedExecutor
	tx       *sql.Tx
}

func (m *scopedMutation) commit() error {
	if m.tx == nil {
		return nil
	}
	if err := m.tx.Commit(); err != nil {
		return fmt.Errorf("commit scoped mutation: %w", err)
	}
	m.tx = nil
	return nil
}

func (m *scopedMutation) rollback() {
	if m.tx != nil {
		_ = m.tx.Rollback()
	}
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
		append_sequence BIGSERIAL NOT NULL,
		org_uuid TEXT NOT NULL,
		workspace_uuid TEXT NOT NULL,
		agent_name TEXT NOT NULL,
		session_id TEXT NOT NULL DEFAULT '',
		run_id TEXT NOT NULL DEFAULT '',
		role TEXT NOT NULL,
		content TEXT NOT NULL,
		-- content_encrypted / content_key_id are reserved for a future
		-- application-level encryption layer. Nothing sets them today; content
		-- is plaintext and at-rest encryption is the database's responsibility.
		content_encrypted BOOLEAN NOT NULL DEFAULT FALSE,
		content_key_id TEXT NOT NULL DEFAULT '',
		metadata JSONB,
		created_at TIMESTAMPTZ NOT NULL
	)`,
	// Existing transcripts predate append_sequence. BIGSERIAL backfills those
	// rows and assigns a durable exact append boundary to all future messages.
	`ALTER TABLE agents_messages ADD COLUMN IF NOT EXISTS append_sequence BIGSERIAL NOT NULL`,
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
		finished_at TIMESTAMPTZ,
		worked_duration_ms BIGINT
	)`,
	// Runs predating the result-on-the-run-record change carry neither column;
	// migrate in place (idempotent).
	`ALTER TABLE agents_runs ADD COLUMN IF NOT EXISTS output TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE agents_runs ADD COLUMN IF NOT EXISTS sources JSONB`,
	`ALTER TABLE agents_runs ADD COLUMN IF NOT EXISTS idempotency_key TEXT NOT NULL DEFAULT ''`,
	// Several old scopes can be re-keyed into one tenant scope during mapping
	// migration. Keep the original key on every run, with one stable primary row
	// selected for retries, so neither duplicate's transcript and audit trail is
	// discarded.
	`ALTER TABLE agents_runs ADD COLUMN IF NOT EXISTS idempotency_primary BOOLEAN NOT NULL DEFAULT TRUE`,
	`ALTER TABLE agents_runs ADD COLUMN IF NOT EXISTS delivery JSONB`,
	// Worked duration is nullable so historical runs without measured model/tool
	// timing remain distinguishable from a measured zero.
	`ALTER TABLE agents_runs ADD COLUMN IF NOT EXISTS worked_duration_ms BIGINT`,
	// Durable cancellation (see Run.CancelRequested): written only by
	// RequestCancel, never by SaveRun's upsert, so a checkpoint written from a
	// stale in-memory copy cannot un-cancel a run.
	`ALTER TABLE agents_runs ADD COLUMN IF NOT EXISTS cancel_requested BOOLEAN NOT NULL DEFAULT FALSE`,
	`ALTER TABLE agents_runs ADD COLUMN IF NOT EXISTS cancel_requested_at TIMESTAMPTZ`,
	// Where the run executed and, for a harness-backed one, the runner attempt
	// and harness session it IS. Migrated in place (idempotent): every existing
	// row is a model-backed run, which an empty backend column reads as.
	`ALTER TABLE agents_runs ADD COLUMN IF NOT EXISTS backend TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE agents_runs ADD COLUMN IF NOT EXISTS attempt_id TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE agents_runs ADD COLUMN IF NOT EXISTS harness_session_id TEXT NOT NULL DEFAULT ''`,
	// Partial unique index: at most one primary run per (tenant, agent, key),
	// while migrated duplicate run records remain intact for history.
	`CREATE UNIQUE INDEX IF NOT EXISTS agents_runs_idempotency_primary_idx
		ON agents_runs (org_uuid, workspace_uuid, agent_name, idempotency_key)
		WHERE idempotency_key <> '' AND idempotency_primary`,
	// Install the replacement first so existing retries remain protected while
	// the pre-migration index is retired. The old index also rejected historical
	// duplicate keys when two store scopes were merged.
	`DROP INDEX IF EXISTS agents_runs_idempotency_idx`,
	`CREATE INDEX IF NOT EXISTS agents_runs_scope_idx
		ON agents_runs (org_uuid, workspace_uuid, created_at DESC)`,
	`CREATE TABLE IF NOT EXISTS agents_memories (
		id TEXT PRIMARY KEY,
		org_uuid TEXT NOT NULL,
		workspace_uuid TEXT NOT NULL,
		agent_name TEXT NOT NULL,
		title TEXT NOT NULL,
		body TEXT NOT NULL,
		-- Reserved and unused; see agents_messages.
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
	`ALTER TABLE agents_session_summaries ADD COLUMN IF NOT EXISTS checkpoint JSONB`,
	// One row per conversational session that has ever run a harness turn: the
	// harness session its turns chain onto, and the turn count that IS the next
	// attempt epoch. Both are protocol requirements (see store.HarnessSession),
	// which is why they are a durable row rather than in-process state.
	`CREATE TABLE IF NOT EXISTS agents_harness_sessions (
		org_uuid TEXT NOT NULL,
		workspace_uuid TEXT NOT NULL,
		agent_name TEXT NOT NULL,
		session_id TEXT NOT NULL,
		task_id TEXT NOT NULL DEFAULT '',
		agent_uid TEXT NOT NULL DEFAULT '',
		harness_session_id TEXT NOT NULL DEFAULT '',
		backend_key TEXT NOT NULL DEFAULT '',
		turns BIGINT NOT NULL DEFAULT 0,
		observed_epoch BIGINT NOT NULL DEFAULT 0,
		updated_at TIMESTAMPTZ NOT NULL,
		PRIMARY KEY (org_uuid, workspace_uuid, agent_name, session_id)
	)`,
	`ALTER TABLE agents_harness_sessions ADD COLUMN IF NOT EXISTS task_id TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE agents_harness_sessions ADD COLUMN IF NOT EXISTS agent_uid TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE agents_harness_sessions ADD COLUMN IF NOT EXISTS backend_key TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE agents_harness_sessions ADD COLUMN IF NOT EXISTS observed_epoch BIGINT NOT NULL DEFAULT 0`,
	// Existing native sessions have no recorded receipt epoch. Backfill them
	// once from the old allocation counter; future observations always write a
	// positive epoch, so later allocations cannot advance this value on startup.
	`UPDATE agents_harness_sessions SET observed_epoch=turns
		WHERE observed_epoch=0 AND harness_session_id<>'' AND turns>0`,
	// The sweep scans by phase + staleness across all tenants, so this index is
	// the one that keeps it from being a full table scan as run history grows.
	`CREATE INDEX IF NOT EXISTS agents_runs_phase_updated_idx
		ON agents_runs (phase, updated_at)`,
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
	mutation, err := p.beginScopedMutation(ctx, scope)
	if err != nil {
		return err
	}
	defer mutation.rollback()
	scope = mutation.scope
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
	if mutation.tx == nil {
		tx, err := p.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		mutation.tx, mutation.executor = tx, tx
	}
	// Serialize appends within a session until commit. BIGSERIAL values are
	// allocated before commit; this lock prevents a late-committing older value
	// from appearing behind a checkpoint boundary captured by another turn.
	lockKey := scope.OrgUUID + "/" + scope.WorkspaceUUID + "/" + scope.AgentName + "/" + msg.SessionID
	if _, err := mutation.executor.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, lockKey); err != nil {
		return fmt.Errorf("lock transcript session: %w", err)
	}
	_, err = mutation.executor.ExecContext(ctx, `
		INSERT INTO agents_messages
			(id, org_uuid, workspace_uuid, agent_name, session_id, run_id, role, content, content_encrypted, content_key_id, metadata, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`,
		msg.ID, scope.OrgUUID, scope.WorkspaceUUID, scope.AgentName, msg.SessionID, msg.RunID,
		msg.Role, msg.Content, msg.ContentEncrypted, msg.ContentKeyID, meta, msg.CreatedAt.UTC())
	if err != nil {
		return err
	}
	return mutation.commit()
}

func (p *PostgresStore) ListMessages(ctx context.Context, scope Scope, sessionID string, limit int, cursor string) (Page, error) {
	scope = p.normalizeScope(ctx, scope)
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
		SELECT append_sequence, id, session_id, run_id, role, content, content_encrypted, content_key_id, metadata, created_at
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
	defer func() { _ = rows.Close() }()
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
	scope = p.normalizeScope(ctx, scope)
	if err := scope.withAgent(); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	rows, err := p.db.QueryContext(ctx, fmt.Sprintf(`
		SELECT append_sequence, id, session_id, run_id, role, content, content_encrypted, content_key_id, metadata, created_at
		FROM agents_messages
		WHERE org_uuid=$1 AND workspace_uuid=$2 AND agent_name=$3 AND session_id=$4
		ORDER BY created_at DESC, id DESC LIMIT %d`, limit),
		scope.OrgUUID, scope.WorkspaceUUID, scope.AgentName, sessionID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
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
	if err := rows.Scan(&m.Sequence, &m.ID, &m.SessionID, &m.RunID, &m.Role, &m.Content, &m.ContentEncrypted, &m.ContentKeyID, &meta, &m.CreatedAt); err != nil {
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
	scope = p.normalizeScope(ctx, scope)
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
	defer func() { _ = rows.Close() }()
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
	mutation, err := p.beginScopedMutation(ctx, scope)
	if err != nil {
		return err
	}
	defer mutation.rollback()
	scope = mutation.scope
	if err := scope.withAgent(); err != nil {
		return err
	}
	if mutation.tx == nil {
		tx, err := p.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		mutation.tx, mutation.executor = tx, tx
	}
	if _, err := mutation.executor.ExecContext(ctx, `
		DELETE FROM agents_messages
		WHERE org_uuid=$1 AND workspace_uuid=$2 AND agent_name=$3 AND session_id=$4`,
		scope.OrgUUID, scope.WorkspaceUUID, scope.AgentName, sessionID); err != nil {
		return err
	}
	// The summary stands for messages that no longer exist; keeping it would
	// replay a wiped conversation back into the model after "/new".
	if _, err := mutation.executor.ExecContext(ctx, `
		DELETE FROM agents_session_summaries
		WHERE org_uuid=$1 AND workspace_uuid=$2 AND agent_name=$3 AND session_id=$4`,
		scope.OrgUUID, scope.WorkspaceUUID, scope.AgentName, sessionID); err != nil {
		return err
	}
	// Clear the native session but retain an epoch fence. The runner remembers
	// its highest attempt for this session ID even after the provider forgets the
	// conversation; the next turn must be strictly newer, and an old receipt must
	// not restore the native session the user deleted.
	if _, err := mutation.executor.ExecContext(ctx, `
		UPDATE agents_harness_sessions SET
			harness_session_id='', backend_key='',
			turns=GREATEST(turns, observed_epoch-1),
			observed_epoch=GREATEST(observed_epoch, turns+1), updated_at=$5
		WHERE org_uuid=$1 AND workspace_uuid=$2 AND agent_name=$3 AND session_id=$4`,
		scope.OrgUUID, scope.WorkspaceUUID, scope.AgentName, sessionID, time.Now().UTC()); err != nil {
		return err
	}
	return mutation.commit()
}

func (p *PostgresStore) PutSessionSummary(ctx context.Context, scope Scope, s SessionSummary) error {
	mutation, err := p.beginScopedMutation(ctx, scope)
	if err != nil {
		return err
	}
	defer mutation.rollback()
	scope = mutation.scope
	if err := scope.withAgent(); err != nil {
		return err
	}
	if strings.TrimSpace(s.SessionID) == "" {
		return fmt.Errorf("session ID is required")
	}
	if err := validateSessionCheckpoint(s.Checkpoint); err != nil {
		return err
	}
	var checkpoint any
	if s.Checkpoint != nil {
		checkpoint, err = marshalJSONB(s.Checkpoint)
	}
	if err != nil {
		return fmt.Errorf("marshal session checkpoint: %w", err)
	}
	result, err := mutation.executor.ExecContext(ctx, `
		INSERT INTO agents_session_summaries
			(org_uuid, workspace_uuid, agent_name, session_id, summary, through_at, message_count, created_at, updated_at, checkpoint)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
		ON CONFLICT (org_uuid, workspace_uuid, agent_name, session_id) DO UPDATE SET
			summary=EXCLUDED.summary, through_at=EXCLUDED.through_at,
			message_count=EXCLUDED.message_count, updated_at=EXCLUDED.updated_at,
			checkpoint=EXCLUDED.checkpoint
		WHERE
			(agents_session_summaries.checkpoint IS NULL AND EXCLUDED.checkpoint IS NULL)
			OR (EXCLUDED.checkpoint IS NOT NULL AND (
				agents_session_summaries.checkpoint IS NULL OR
				COALESCE((agents_session_summaries.checkpoint->>'throughSequence')::BIGINT, 0) <=
				COALESCE((EXCLUDED.checkpoint->>'throughSequence')::BIGINT, 0)
			))`,
		scope.OrgUUID, scope.WorkspaceUUID, scope.AgentName, s.SessionID,
		s.Summary, s.ThroughAt.UTC(), s.MessageCount, s.CreatedAt.UTC(), s.UpdatedAt.UTC(), checkpoint)
	if err != nil {
		return err
	}
	if affected, err := result.RowsAffected(); err != nil {
		return err
	} else if affected == 0 {
		return ErrSessionCheckpointStale
	}
	return mutation.commit()
}

func (p *PostgresStore) GetSessionSummary(ctx context.Context, scope Scope, sessionID string) (SessionSummary, bool, error) {
	scope = p.normalizeScope(ctx, scope)
	if err := scope.withAgent(); err != nil {
		return SessionSummary{}, false, err
	}
	out := SessionSummary{SessionID: sessionID}
	var checkpoint []byte
	row := p.db.QueryRowContext(ctx, `
		SELECT summary, through_at, message_count, created_at, updated_at, checkpoint
		FROM agents_session_summaries
		WHERE org_uuid=$1 AND workspace_uuid=$2 AND agent_name=$3 AND session_id=$4`,
		scope.OrgUUID, scope.WorkspaceUUID, scope.AgentName, sessionID)
	err := row.Scan(&out.Summary, &out.ThroughAt, &out.MessageCount, &out.CreatedAt, &out.UpdatedAt, &checkpoint)
	if errors.Is(err, sql.ErrNoRows) {
		return SessionSummary{}, false, nil
	}
	if err != nil {
		return SessionSummary{}, false, err
	}
	if len(checkpoint) > 0 {
		var decoded SessionCheckpoint
		if err := json.Unmarshal(checkpoint, &decoded); err != nil {
			return SessionSummary{}, false, fmt.Errorf("decode session checkpoint: %w", err)
		}
		if err := validateSessionCheckpoint(&decoded); err != nil {
			return SessionSummary{}, false, fmt.Errorf("invalid session checkpoint: %w", err)
		}
		out.Checkpoint = &decoded
	}
	out.ThroughAt, out.CreatedAt, out.UpdatedAt = out.ThroughAt.UTC(), out.CreatedAt.UTC(), out.UpdatedAt.UTC()
	return out, true, nil
}

// ---- harness sessions ---------------------------------------------------------

// NextHarnessTurn claims the next turn number for a session.
//
// A row lock makes identity selection, stale-incarnation reset and epoch
// allocation one transaction: two replicas cannot both receive one attempt
// epoch, and the runner's stale_attempt refusal remains a backstop.
func (p *PostgresStore) NextHarnessTurn(ctx context.Context, scope Scope, sessionID string, now time.Time, identity HarnessIdentity) (HarnessSession, error) {
	mutation, err := p.beginScopedMutation(ctx, scope)
	if err != nil {
		return HarnessSession{}, err
	}
	defer mutation.rollback()
	scope = mutation.scope
	// Canonical scopes need a transaction too: row locking keeps identity
	// selection, stale-incarnation reset and epoch allocation atomic.
	if mutation.tx == nil {
		tx, err := p.db.BeginTx(ctx, nil)
		if err != nil {
			return HarnessSession{}, fmt.Errorf("begin harness turn mutation: %w", err)
		}
		mutation.tx, mutation.executor = tx, tx
	}
	if err := scope.withAgent(); err != nil {
		return HarnessSession{}, err
	}
	if strings.TrimSpace(sessionID) == "" {
		return HarnessSession{}, fmt.Errorf("session ID is required")
	}
	now = now.UTC()
	if _, err := mutation.executor.ExecContext(ctx, `
		INSERT INTO agents_harness_sessions
			(org_uuid, workspace_uuid, agent_name, session_id, task_id, agent_uid, harness_session_id, backend_key, turns, observed_epoch, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,'','',0,0,$7)
		ON CONFLICT (org_uuid, workspace_uuid, agent_name, session_id) DO NOTHING`,
		scope.OrgUUID, scope.WorkspaceUUID, scope.AgentName, sessionID, identity.TaskID, identity.AgentUID, now); err != nil {
		return HarnessSession{}, err
	}
	out := HarnessSession{SessionID: sessionID}
	err = mutation.executor.QueryRowContext(ctx, `
		SELECT task_id, agent_uid, harness_session_id, backend_key, turns, observed_epoch, updated_at
		FROM agents_harness_sessions
		WHERE org_uuid=$1 AND workspace_uuid=$2 AND agent_name=$3 AND session_id=$4
		FOR UPDATE`, scope.OrgUUID, scope.WorkspaceUUID, scope.AgentName, sessionID).
		Scan(&out.TaskID, &out.AgentUID, &out.HarnessSessionID, &out.BackendKey, &out.Turns, &out.ObservedEpoch, &out.UpdatedAt)
	if err != nil {
		return HarnessSession{}, err
	}
	if err := selectHarnessIdentity(&out, identity, now); err != nil {
		return HarnessSession{}, err
	}
	out.SessionID = sessionID
	out.Turns++
	out.UpdatedAt = now
	if _, err := mutation.executor.ExecContext(ctx, `
		UPDATE agents_harness_sessions SET task_id=$5, agent_uid=$6, harness_session_id=$7,
			backend_key=$8, turns=$9, observed_epoch=$10, updated_at=$11
		WHERE org_uuid=$1 AND workspace_uuid=$2 AND agent_name=$3 AND session_id=$4`,
		scope.OrgUUID, scope.WorkspaceUUID, scope.AgentName, sessionID, out.TaskID, out.AgentUID,
		out.HarnessSessionID, out.BackendKey, out.Turns, out.ObservedEpoch, out.UpdatedAt); err != nil {
		return HarnessSession{}, err
	}
	out.UpdatedAt = out.UpdatedAt.UTC()
	if err := mutation.commit(); err != nil {
		return HarnessSession{}, err
	}
	return out, nil
}

// PutHarnessSession records the native session and backend key a receipt
// reported unless a receipt from a newer observed epoch already won. The
// allocated turn count only moves forward, and empty session IDs never clear it.
func (p *PostgresStore) PutHarnessSession(ctx context.Context, scope Scope, s HarnessSession) error {
	mutation, err := p.beginScopedMutation(ctx, scope)
	if err != nil {
		return err
	}
	defer mutation.rollback()
	scope = mutation.scope
	if err := scope.withAgent(); err != nil {
		return err
	}
	if strings.TrimSpace(s.SessionID) == "" {
		return fmt.Errorf("session ID is required")
	}
	if strings.TrimSpace(s.HarnessSessionID) != "" {
		if strings.TrimSpace(s.BackendKey) == "" {
			return fmt.Errorf("backend key is required when a harness session ID is observed")
		}
		if s.ObservedEpoch <= 0 {
			return fmt.Errorf("observed harness session epoch must be positive")
		}
	} else {
		// A new row must never acquire a backend key or observation epoch without
		// the native session ID they qualify.
		s.HarnessSessionID = ""
		s.BackendKey = ""
		s.ObservedEpoch = 0
	}
	res, err := mutation.executor.ExecContext(ctx, `
		INSERT INTO agents_harness_sessions
			(org_uuid, workspace_uuid, agent_name, session_id, task_id, agent_uid, harness_session_id, backend_key, turns, observed_epoch, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
		ON CONFLICT (org_uuid, workspace_uuid, agent_name, session_id) DO UPDATE SET
			task_id=CASE WHEN agents_harness_sessions.task_id='' THEN EXCLUDED.task_id ELSE agents_harness_sessions.task_id END,
			agent_uid=CASE WHEN agents_harness_sessions.agent_uid='' THEN EXCLUDED.agent_uid ELSE agents_harness_sessions.agent_uid END,
			harness_session_id=CASE WHEN EXCLUDED.harness_session_id <> ''
					AND EXCLUDED.observed_epoch >= agents_harness_sessions.observed_epoch THEN EXCLUDED.harness_session_id
				ELSE agents_harness_sessions.harness_session_id END,
			backend_key=CASE WHEN EXCLUDED.harness_session_id <> ''
					AND EXCLUDED.observed_epoch >= agents_harness_sessions.observed_epoch THEN EXCLUDED.backend_key
				ELSE agents_harness_sessions.backend_key END,
			observed_epoch=CASE WHEN EXCLUDED.harness_session_id <> ''
					AND EXCLUDED.observed_epoch >= agents_harness_sessions.observed_epoch THEN EXCLUDED.observed_epoch
				ELSE agents_harness_sessions.observed_epoch END,
			turns=GREATEST(agents_harness_sessions.turns, EXCLUDED.turns),
			updated_at=EXCLUDED.updated_at
		WHERE (agents_harness_sessions.task_id='' OR agents_harness_sessions.task_id=EXCLUDED.task_id)
			AND (agents_harness_sessions.agent_uid='' OR agents_harness_sessions.agent_uid=EXCLUDED.agent_uid)`,
		scope.OrgUUID, scope.WorkspaceUUID, scope.AgentName, s.SessionID, s.TaskID, s.AgentUID,
		s.HarnessSessionID, s.BackendKey, s.Turns, s.ObservedEpoch, s.UpdatedAt.UTC())
	if err != nil {
		return err
	}
	if affected, err := res.RowsAffected(); err != nil {
		return err
	} else if affected == 0 {
		return fmt.Errorf("harness session receipt belongs to a different task identity")
	}
	return mutation.commit()
}

func (p *PostgresStore) GetHarnessSession(ctx context.Context, scope Scope, sessionID string) (HarnessSession, bool, error) {
	scope = p.normalizeScope(ctx, scope)
	if err := scope.withAgent(); err != nil {
		return HarnessSession{}, false, err
	}
	out := HarnessSession{SessionID: sessionID}
	row := p.db.QueryRowContext(ctx, `
		SELECT task_id, agent_uid, harness_session_id, backend_key, turns, observed_epoch, updated_at
		FROM agents_harness_sessions
		WHERE org_uuid=$1 AND workspace_uuid=$2 AND agent_name=$3 AND session_id=$4`,
		scope.OrgUUID, scope.WorkspaceUUID, scope.AgentName, sessionID)
	err := row.Scan(&out.TaskID, &out.AgentUID, &out.HarnessSessionID, &out.BackendKey, &out.Turns, &out.ObservedEpoch, &out.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return HarnessSession{}, false, nil
	}
	if err != nil {
		return HarnessSession{}, false, err
	}
	out.UpdatedAt = out.UpdatedAt.UTC()
	return out, true, nil
}

// ---- runs ---------------------------------------------------------------------

func (p *PostgresStore) SaveRun(ctx context.Context, scope Scope, run Run) error {
	mutation, err := p.beginScopedMutation(ctx, scope)
	if err != nil {
		return err
	}
	defer mutation.rollback()
	scope = mutation.scope
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
	_, err = mutation.executor.ExecContext(ctx, `
		INSERT INTO agents_runs
			(id, org_uuid, workspace_uuid, agent_name, session_id, trigger_kind, parent_run_id, phase, attempt,
			 input, output, sources, idempotency_key, delivery, message, checkpoint, input_tokens, output_tokens, usd_micros, created_at, updated_at, started_at, finished_at, worked_duration_ms,
			 backend, attempt_id, harness_session_id)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25,$26,$27)
		ON CONFLICT (id) DO UPDATE SET
			phase=EXCLUDED.phase, attempt=EXCLUDED.attempt, message=EXCLUDED.message,
			output=EXCLUDED.output, sources=EXCLUDED.sources, delivery=EXCLUDED.delivery,
			checkpoint=EXCLUDED.checkpoint, input_tokens=EXCLUDED.input_tokens,
			output_tokens=EXCLUDED.output_tokens, usd_micros=EXCLUDED.usd_micros,
			updated_at=EXCLUDED.updated_at, started_at=EXCLUDED.started_at, finished_at=EXCLUDED.finished_at,
			worked_duration_ms=EXCLUDED.worked_duration_ms,
			backend=EXCLUDED.backend, attempt_id=EXCLUDED.attempt_id,
			harness_session_id=EXCLUDED.harness_session_id`,
		run.ID, scope.OrgUUID, scope.WorkspaceUUID, run.AgentName, run.SessionID, run.Trigger, run.ParentRunID,
		string(run.Phase), run.Attempt, run.Input, run.Output, sources, run.IdempotencyKey, delivery, run.Message, nullBytes(run.Checkpoint),
		run.InputTokens, run.OutputTokens, run.USDMicros,
		run.CreatedAt.UTC(), run.UpdatedAt.UTC(), nullTime(run.StartedAt), nullTime(run.FinishedAt), nullInt64(run.WorkedDurationMS),
		run.Backend, run.AttemptID, run.HarnessSessionID)
	if err != nil {
		return err
	}
	return mutation.commit()
}

// runColumns is the run SELECT list, shared by every read path so a schema
// change cannot drift one query out of step with scanRun.
const runColumns = `id, agent_name, session_id, trigger_kind, parent_run_id, phase, attempt, input, output, sources, idempotency_key, delivery, message,
		       checkpoint, input_tokens, output_tokens, usd_micros, created_at, updated_at, started_at, finished_at, worked_duration_ms,
		       cancel_requested, cancel_requested_at, backend, attempt_id, harness_session_id`

func (p *PostgresStore) GetRun(ctx context.Context, scope Scope, id string) (Run, error) {
	scope = p.normalizeScope(ctx, scope)
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

func (p *PostgresStore) RequestCancel(ctx context.Context, scope Scope, id string, now time.Time) error {
	mutation, err := p.beginScopedMutation(ctx, scope)
	if err != nil {
		return err
	}
	defer mutation.rollback()
	scope = mutation.scope
	if err := scope.validate(); err != nil {
		return err
	}
	// Only the cancel columns move: phase, checkpoint and the rest belong to
	// whoever is executing the run, and this must not race their writes.
	res, err := mutation.executor.ExecContext(ctx, `
		UPDATE agents_runs SET cancel_requested=TRUE, cancel_requested_at=COALESCE(cancel_requested_at, $4)
		WHERE org_uuid=$1 AND workspace_uuid=$2 AND id=$3 AND phase IN ($5,$6,$7)`,
		scope.OrgUUID, scope.WorkspaceUUID, id, now.UTC(),
		string(RunPhasePending), string(RunPhaseRunning), string(RunPhasePendingApproval))
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		// Either unknown or already terminal; both mean there is nothing to stop.
		row := mutation.executor.QueryRowContext(ctx, `SELECT `+runColumns+`
			FROM agents_runs WHERE org_uuid=$1 AND workspace_uuid=$2 AND id=$3`, scope.OrgUUID, scope.WorkspaceUUID, id)
		if _, gerr := scanRun(row); errors.Is(gerr, sql.ErrNoRows) {
			return fmt.Errorf("run %q not found", id)
		} else if gerr != nil {
			return gerr
		}
	}
	return mutation.commit()
}

func (p *PostgresStore) ClaimRun(ctx context.Context, scope Scope, id, _ string, now time.Time) (Run, error) {
	mutation, err := p.beginScopedMutation(ctx, scope)
	if err != nil {
		return Run{}, err
	}
	defer mutation.rollback()
	scope = mutation.scope
	if err := scope.validate(); err != nil {
		return Run{}, err
	}
	res, err := mutation.executor.ExecContext(ctx, `
		UPDATE agents_runs SET phase=$4, updated_at=$5, started_at=COALESCE(started_at, $5)
		WHERE org_uuid=$1 AND workspace_uuid=$2 AND id=$3 AND phase IN ($6,$7) AND cancel_requested=FALSE`,
		scope.OrgUUID, scope.WorkspaceUUID, id, string(RunPhaseRunning), now.UTC(),
		string(RunPhasePending), string(RunPhasePendingApproval))
	if err != nil {
		return Run{}, err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return Run{}, fmt.Errorf("run %q not found or already claimed", id)
	}
	row := mutation.executor.QueryRowContext(ctx, `SELECT `+runColumns+`
		FROM agents_runs WHERE org_uuid=$1 AND workspace_uuid=$2 AND id=$3`, scope.OrgUUID, scope.WorkspaceUUID, id)
	run, err := scanRun(row)
	if err != nil {
		return Run{}, err
	}
	if err := mutation.commit(); err != nil {
		return Run{}, err
	}
	return run, nil
}

func (p *PostgresStore) ListRuns(ctx context.Context, scope Scope, limit int) ([]Run, error) {
	scope = p.normalizeScope(ctx, scope)
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
	defer func() { _ = rows.Close() }()
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
	scope = p.normalizeScope(ctx, scope)
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
	defer func() { _ = rows.Close() }()
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
	var started, finished, cancelAt sql.NullTime
	var worked sql.NullInt64
	dest := []any{}
	if sc != nil {
		dest = append(dest, &sc.OrgUUID, &sc.WorkspaceUUID)
	}
	dest = append(dest, &run.ID, &run.AgentName, &run.SessionID, &run.Trigger, &run.ParentRunID, &phase, &run.Attempt,
		&run.Input, &run.Output, &sources, &run.IdempotencyKey, &delivery, &run.Message, &checkpoint, &run.InputTokens, &run.OutputTokens, &run.USDMicros,
		&run.CreatedAt, &run.UpdatedAt, &started, &finished, &worked, &run.CancelRequested, &cancelAt,
		&run.Backend, &run.AttemptID, &run.HarnessSessionID)
	if err := r.Scan(dest...); err != nil {
		return Run{}, err
	}
	if cancelAt.Valid {
		t := cancelAt.Time.UTC()
		run.CancelRequestedAt = &t
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
	if worked.Valid {
		value := worked.Int64
		run.WorkedDurationMS = &value
	}
	run.CreatedAt, run.UpdatedAt = run.CreatedAt.UTC(), run.UpdatedAt.UTC()
	return run, nil
}

// ---- memories ------------------------------------------------------------------

func (p *PostgresStore) PutMemory(ctx context.Context, scope Scope, m Memory) error {
	mutation, err := p.beginScopedMutation(ctx, scope)
	if err != nil {
		return err
	}
	defer mutation.rollback()
	scope = mutation.scope
	if err := scope.withAgent(); err != nil {
		return err
	}
	if m.ID == "" {
		return fmt.Errorf("memory ID is required")
	}
	_, err = mutation.executor.ExecContext(ctx, `
		INSERT INTO agents_memories (id, org_uuid, workspace_uuid, agent_name, title, body, content_encrypted, content_key_id, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
		ON CONFLICT (id) DO UPDATE SET title=EXCLUDED.title, body=EXCLUDED.body, updated_at=EXCLUDED.updated_at`,
		m.ID, scope.OrgUUID, scope.WorkspaceUUID, m.AgentName, m.Title, m.Body, m.ContentEncrypted, m.ContentKeyID,
		m.CreatedAt.UTC(), m.UpdatedAt.UTC())
	if err != nil {
		return err
	}
	return mutation.commit()
}

func (p *PostgresStore) ListMemories(ctx context.Context, scope Scope, limit int) ([]Memory, error) {
	scope = p.normalizeScope(ctx, scope)
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
	defer func() { _ = rows.Close() }()
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
	mutation, err := p.beginScopedMutation(ctx, scope)
	if err != nil {
		return err
	}
	defer mutation.rollback()
	scope = mutation.scope
	if err := scope.withAgent(); err != nil {
		return err
	}
	_, err = mutation.executor.ExecContext(ctx, `DELETE FROM agents_memories WHERE org_uuid=$1 AND workspace_uuid=$2 AND id=$3`,
		scope.OrgUUID, scope.WorkspaceUUID, id)
	if err != nil {
		return err
	}
	return mutation.commit()
}

// ---- inbox ----------------------------------------------------------------------

func (p *PostgresStore) AddInboxItem(ctx context.Context, scope Scope, item InboxItem) error {
	mutation, err := p.beginScopedMutation(ctx, scope)
	if err != nil {
		return err
	}
	defer mutation.rollback()
	scope = mutation.scope
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
	_, err = mutation.executor.ExecContext(ctx, `
		INSERT INTO agents_inbox (id, org_uuid, workspace_uuid, agent_name, run_id, kind, state, prompt, payload, response, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`,
		item.ID, scope.OrgUUID, scope.WorkspaceUUID, item.AgentName, item.RunID, string(item.Kind), string(item.State),
		item.Prompt, payload, item.Response, item.CreatedAt.UTC(), item.UpdatedAt.UTC())
	if err != nil {
		return err
	}
	return mutation.commit()
}

func (p *PostgresStore) GetInboxItem(ctx context.Context, scope Scope, id string) (InboxItem, error) {
	scope = p.normalizeScope(ctx, scope)
	if err := scope.validate(); err != nil {
		return InboxItem{}, err
	}
	var it InboxItem
	var kind, state string
	var payload []byte
	err := p.db.QueryRowContext(ctx, `
		SELECT id, agent_name, run_id, kind, state, prompt, payload, response, created_at, updated_at
		FROM agents_inbox WHERE org_uuid=$1 AND workspace_uuid=$2 AND id=$3`,
		scope.OrgUUID, scope.WorkspaceUUID, id,
	).Scan(&it.ID, &it.AgentName, &it.RunID, &kind, &state, &it.Prompt, &payload, &it.Response, &it.CreatedAt, &it.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return InboxItem{}, fmt.Errorf("inbox item %q not found", id)
	}
	if err != nil {
		return InboxItem{}, err
	}
	it.Kind, it.State = InboxItemKind(kind), InboxItemState(state)
	if len(payload) > 0 {
		_ = json.Unmarshal(payload, &it.Payload)
	}
	it.CreatedAt, it.UpdatedAt = it.CreatedAt.UTC(), it.UpdatedAt.UTC()
	return it, nil
}

func (p *PostgresStore) ListInbox(ctx context.Context, scope Scope, state InboxItemState) ([]InboxItem, error) {
	scope = p.normalizeScope(ctx, scope)
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
	defer func() { _ = rows.Close() }()
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
	mutation, err := p.beginScopedMutation(ctx, scope)
	if err != nil {
		return InboxItem{}, err
	}
	defer mutation.rollback()
	scope = mutation.scope
	if err := scope.validate(); err != nil {
		return InboxItem{}, err
	}
	res, err := mutation.executor.ExecContext(ctx, `
		UPDATE agents_inbox SET state=$4, response=$5, updated_at=$6
		WHERE org_uuid=$1 AND workspace_uuid=$2 AND id=$3`,
		scope.OrgUUID, scope.WorkspaceUUID, id, string(state), response, now.UTC())
	if err != nil {
		return InboxItem{}, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return InboxItem{}, fmt.Errorf("inbox item %q not found", id)
	}
	var item InboxItem
	var kind, currentState string
	var payload []byte
	err = mutation.executor.QueryRowContext(ctx, `
		SELECT id, agent_name, run_id, kind, state, prompt, payload, response, created_at, updated_at
		FROM agents_inbox WHERE org_uuid=$1 AND workspace_uuid=$2 AND id=$3`,
		scope.OrgUUID, scope.WorkspaceUUID, id,
	).Scan(&item.ID, &item.AgentName, &item.RunID, &kind, &currentState, &item.Prompt, &payload, &item.Response, &item.CreatedAt, &item.UpdatedAt)
	if err != nil {
		return InboxItem{}, err
	}
	item.Kind, item.State = InboxItemKind(kind), InboxItemState(currentState)
	if len(payload) > 0 {
		_ = json.Unmarshal(payload, &item.Payload)
	}
	item.CreatedAt, item.UpdatedAt = item.CreatedAt.UTC(), item.UpdatedAt.UTC()
	if err := mutation.commit(); err != nil {
		return InboxItem{}, err
	}
	return item, nil
}

// ---- audit + usage -----------------------------------------------------------------

func (p *PostgresStore) AppendToolCall(ctx context.Context, scope Scope, tc ToolCall) error {
	mutation, err := p.beginScopedMutation(ctx, scope)
	if err != nil {
		return err
	}
	defer mutation.rollback()
	scope = mutation.scope
	if err := scope.validate(); err != nil {
		return err
	}
	_, err = mutation.executor.ExecContext(ctx, `
		INSERT INTO agents_tool_calls (id, org_uuid, workspace_uuid, agent_name, run_id, trigger_kind, tool, args, result, outcome, error, duration_ms, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`,
		tc.ID, scope.OrgUUID, scope.WorkspaceUUID, tc.AgentName, tc.RunID, tc.Trigger, tc.Tool, tc.Args, tc.Result,
		tc.Outcome, tc.Error, tc.DurationMS, tc.CreatedAt.UTC())
	if err != nil {
		return err
	}
	return mutation.commit()
}

func (p *PostgresStore) ListToolCalls(ctx context.Context, scope Scope, runID string) ([]ToolCall, error) {
	scope = p.normalizeScope(ctx, scope)
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
	defer func() { _ = rows.Close() }()
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
	mutation, err := p.beginScopedMutation(ctx, scope)
	if err != nil {
		return Usage{}, err
	}
	defer mutation.rollback()
	scope = mutation.scope
	if err := scope.validate(); err != nil {
		return Usage{}, err
	}
	ws := windowStart(now, window)
	row := mutation.executor.QueryRowContext(ctx, `
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
	if err := mutation.commit(); err != nil {
		return Usage{}, err
	}
	return u, nil
}

func (p *PostgresStore) GetUsage(ctx context.Context, scope Scope, agentName string, now time.Time, window time.Duration) (Usage, error) {
	mutation, err := p.beginScopedMutation(ctx, scope)
	if err != nil {
		return Usage{}, err
	}
	defer mutation.rollback()
	scope = mutation.scope
	if err := scope.validate(); err != nil {
		return Usage{}, err
	}
	ws := windowStart(now, window)
	row := mutation.executor.QueryRowContext(ctx, `
		SELECT input_tokens, output_tokens, usd_micros, updated_at FROM agents_usage
		WHERE org_uuid=$1 AND workspace_uuid=$2 AND agent_name=$3 AND window_start=$4`,
		scope.OrgUUID, scope.WorkspaceUUID, agentName, ws)
	u := Usage{AgentName: agentName, WindowStart: ws}
	err = row.Scan(&u.InputTokens, &u.OutputTokens, &u.USDMicros, &u.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		u = Usage{AgentName: agentName, WindowStart: ws}
	} else if err != nil {
		return Usage{}, err
	} else {
		u.UpdatedAt = u.UpdatedAt.UTC()
	}
	if err := mutation.commit(); err != nil {
		return Usage{}, err
	}
	return u, nil
}

// ---- tenant refs ---------------------------------------------------------------------

func (p *PostgresStore) SaveTenantRef(ctx context.Context, clusterID string, ref TenantRef) error {
	if err := validateTenantRef(clusterID, ref); err != nil {
		return err
	}
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tenant scope migration: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	// Serialize mapping publication with fallback mutations. A request holding a
	// stale fallback scope either commits before this transaction and is migrated
	// here, or waits for publication and re-resolves into the canonical scope.
	// Repeated calls also catch rows written directly by an older provider build.
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, "agents-tenant-map:"+clusterID); err != nil {
		return fmt.Errorf("lock tenant scope migration: %w", err)
	}
	var current TenantRef
	err = tx.QueryRowContext(ctx, `SELECT org_uuid, workspace_uuid, updated_at FROM agents_tenants WHERE cluster_id=$1`, clusterID).
		Scan(&current.OrgUUID, &current.WorkspaceUUID, &current.UpdatedAt)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("read existing tenant mapping: %w", err)
	}
	if err == nil && (current.OrgUUID != ref.OrgUUID || current.WorkspaceUUID != ref.WorkspaceUUID) {
		return fmt.Errorf("tenant mapping for cluster %s is immutable (%s/%s already mapped)", clusterID, current.OrgUUID, current.WorkspaceUUID)
	}
	if err := migrateUnmappedScopeTx(ctx, tx, clusterID, ref); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO agents_tenants (cluster_id, org_uuid, workspace_uuid, updated_at)
		VALUES ($1,$2,$3,$4)
		ON CONFLICT (cluster_id) DO NOTHING`,
		clusterID, ref.OrgUUID, ref.WorkspaceUUID, ref.UpdatedAt.UTC()); err != nil {
		return fmt.Errorf("save tenant mapping: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit tenant scope migration: %w", err)
	}
	return nil
}

// migrateUnmappedScopeTx moves fallback rows into the canonical workspace in
// one transaction. It is intentionally idempotent: once a row has moved, a
// retry sees no source row. The shared tenant-map lock fences store mutations,
// so a request that resolved fallback before mapping either commits before
// this migration or re-resolves to the canonical scope after it.
func migrateUnmappedScopeTx(ctx context.Context, tx *sql.Tx, clusterID string, ref TenantRef) error {
	if clusterID == "" || ref.OrgUUID == "" || ref.WorkspaceUUID == "" ||
		(ref.OrgUUID == UnmappedOrg && ref.WorkspaceUUID == clusterID) {
		return nil
	}
	const fromOrg = UnmappedOrg
	fromWorkspace, toOrg, toWorkspace := clusterID, ref.OrgUUID, ref.WorkspaceUUID
	var hasSource bool
	if err := tx.QueryRowContext(ctx, `
		SELECT
			EXISTS (SELECT 1 FROM agents_messages WHERE org_uuid=$1 AND workspace_uuid=$2) OR
			EXISTS (SELECT 1 FROM agents_runs WHERE org_uuid=$1 AND workspace_uuid=$2) OR
			EXISTS (SELECT 1 FROM agents_memories WHERE org_uuid=$1 AND workspace_uuid=$2) OR
			EXISTS (SELECT 1 FROM agents_inbox WHERE org_uuid=$1 AND workspace_uuid=$2) OR
			EXISTS (SELECT 1 FROM agents_tool_calls WHERE org_uuid=$1 AND workspace_uuid=$2) OR
			EXISTS (SELECT 1 FROM agents_usage WHERE org_uuid=$1 AND workspace_uuid=$2) OR
			EXISTS (SELECT 1 FROM agents_session_summaries WHERE org_uuid=$1 AND workspace_uuid=$2) OR
			EXISTS (SELECT 1 FROM agents_harness_sessions WHERE org_uuid=$1 AND workspace_uuid=$2)`,
		fromOrg, fromWorkspace).Scan(&hasSource); err != nil {
		return fmt.Errorf("check legacy scope for cluster %s: %w", clusterID, err)
	}
	if !hasSource {
		return nil
	}
	// Pick one stable idempotency owner across both scopes before the rows meet
	// under the destination key. Every Run row remains; retries resolve to the
	// earliest-created run, with ID as a deterministic tie-breaker.
	if _, err := tx.ExecContext(ctx, `
		WITH candidates AS (
			SELECT id, row_number() OVER (
				PARTITION BY agent_name, idempotency_key ORDER BY created_at, id
			) AS position
			FROM agents_runs
			WHERE idempotency_key <> '' AND (
				(org_uuid=$1 AND workspace_uuid=$2) OR
				(org_uuid=$3 AND workspace_uuid=$4)
			)
		)
		UPDATE agents_runs AS runs
		SET idempotency_primary=(candidates.position=1)
		FROM candidates
		WHERE runs.id=candidates.id`, fromOrg, fromWorkspace, toOrg, toWorkspace); err != nil {
		return fmt.Errorf("choose idempotency winners for cluster %s: %w", clusterID, err)
	}
	for _, table := range []string{"agents_messages", "agents_runs", "agents_memories", "agents_inbox", "agents_tool_calls"} {
		query := "UPDATE " + table + " SET org_uuid=$3, workspace_uuid=$4 WHERE org_uuid=$1 AND workspace_uuid=$2"
		if _, err := tx.ExecContext(ctx, query, fromOrg, fromWorkspace, toOrg, toWorkspace); err != nil {
			return fmt.Errorf("migrate %s for cluster %s: %w", table, clusterID, err)
		}
	}
	// Usage is a cumulative bucket: combine both partitions once, then remove
	// the source so a later catch-up cannot count it a second time.
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO agents_usage (org_uuid, workspace_uuid, agent_name, window_start,
			input_tokens, output_tokens, usd_micros, updated_at)
		SELECT $3, $4, agent_name, window_start, input_tokens, output_tokens, usd_micros, updated_at
		FROM agents_usage WHERE org_uuid=$1 AND workspace_uuid=$2
		ON CONFLICT (org_uuid, workspace_uuid, agent_name, window_start) DO UPDATE SET
			input_tokens=agents_usage.input_tokens+EXCLUDED.input_tokens,
			output_tokens=agents_usage.output_tokens+EXCLUDED.output_tokens,
			usd_micros=agents_usage.usd_micros+EXCLUDED.usd_micros,
			updated_at=GREATEST(agents_usage.updated_at, EXCLUDED.updated_at)`,
		fromOrg, fromWorkspace, toOrg, toWorkspace); err != nil {
		return fmt.Errorf("migrate usage for cluster %s: %w", clusterID, err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM agents_usage WHERE org_uuid=$1 AND workspace_uuid=$2`, fromOrg, fromWorkspace); err != nil {
		return fmt.Errorf("remove migrated usage for cluster %s: %w", clusterID, err)
	}
	// When the same session was used before and after tenant resolution, retain
	// the most advanced durable checkpoint. Append sequences are global to the
	// table, so ThroughSequence is a stable ordering across both scopes.
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO agents_session_summaries (org_uuid, workspace_uuid, agent_name, session_id,
			summary, through_at, message_count, created_at, updated_at, checkpoint)
		SELECT $3, $4, agent_name, session_id, summary, through_at, message_count, created_at, updated_at, checkpoint
		FROM agents_session_summaries WHERE org_uuid=$1 AND workspace_uuid=$2
		ON CONFLICT (org_uuid, workspace_uuid, agent_name, session_id) DO UPDATE SET
			summary=CASE WHEN (
				(EXCLUDED.checkpoint IS NOT NULL AND (agents_session_summaries.checkpoint IS NULL OR
					COALESCE((EXCLUDED.checkpoint->>'throughSequence')::BIGINT, 0) >
					COALESCE((agents_session_summaries.checkpoint->>'throughSequence')::BIGINT, 0) OR
					(COALESCE((EXCLUDED.checkpoint->>'throughSequence')::BIGINT, 0) =
					 COALESCE((agents_session_summaries.checkpoint->>'throughSequence')::BIGINT, 0) AND
					 EXCLUDED.updated_at >= agents_session_summaries.updated_at))) OR
				(EXCLUDED.checkpoint IS NULL AND agents_session_summaries.checkpoint IS NULL AND
				 EXCLUDED.updated_at >= agents_session_summaries.updated_at)
			) THEN EXCLUDED.summary ELSE agents_session_summaries.summary END,
			through_at=CASE WHEN EXCLUDED.updated_at >= agents_session_summaries.updated_at THEN EXCLUDED.through_at ELSE agents_session_summaries.through_at END,
			message_count=GREATEST(agents_session_summaries.message_count, EXCLUDED.message_count),
			created_at=LEAST(agents_session_summaries.created_at, EXCLUDED.created_at),
			updated_at=GREATEST(agents_session_summaries.updated_at, EXCLUDED.updated_at),
			checkpoint=CASE WHEN (
				(EXCLUDED.checkpoint IS NOT NULL AND (agents_session_summaries.checkpoint IS NULL OR
					COALESCE((EXCLUDED.checkpoint->>'throughSequence')::BIGINT, 0) >
					COALESCE((agents_session_summaries.checkpoint->>'throughSequence')::BIGINT, 0) OR
					(COALESCE((EXCLUDED.checkpoint->>'throughSequence')::BIGINT, 0) =
					 COALESCE((agents_session_summaries.checkpoint->>'throughSequence')::BIGINT, 0) AND
					 EXCLUDED.updated_at >= agents_session_summaries.updated_at))) OR
				(EXCLUDED.checkpoint IS NULL AND agents_session_summaries.checkpoint IS NULL AND
				 EXCLUDED.updated_at >= agents_session_summaries.updated_at)
			) THEN EXCLUDED.checkpoint ELSE agents_session_summaries.checkpoint END`,
		fromOrg, fromWorkspace, toOrg, toWorkspace); err != nil {
		return fmt.Errorf("migrate session summaries for cluster %s: %w", clusterID, err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM agents_session_summaries WHERE org_uuid=$1 AND workspace_uuid=$2`, fromOrg, fromWorkspace); err != nil {
		return fmt.Errorf("remove migrated session summaries for cluster %s: %w", clusterID, err)
	}
	// Epoch and native-session identity travel together. A later tombstone or
	// receipt wins by observed epoch, then UpdatedAt; allocation count never goes
	// backwards when two previously split rows are merged.
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO agents_harness_sessions (org_uuid, workspace_uuid, agent_name, session_id,
			task_id, agent_uid, harness_session_id, backend_key, turns, observed_epoch, updated_at)
		SELECT $3, $4, agent_name, session_id, task_id, agent_uid, harness_session_id, backend_key, turns, observed_epoch, updated_at
		FROM agents_harness_sessions WHERE org_uuid=$1 AND workspace_uuid=$2
		ON CONFLICT (org_uuid, workspace_uuid, agent_name, session_id) DO UPDATE SET
			task_id=CASE WHEN EXCLUDED.observed_epoch > agents_harness_sessions.observed_epoch OR
				(EXCLUDED.observed_epoch = agents_harness_sessions.observed_epoch AND
				 EXCLUDED.updated_at >= agents_harness_sessions.updated_at)
				THEN EXCLUDED.task_id ELSE agents_harness_sessions.task_id END,
			agent_uid=CASE WHEN EXCLUDED.observed_epoch > agents_harness_sessions.observed_epoch OR
				(EXCLUDED.observed_epoch = agents_harness_sessions.observed_epoch AND
				 EXCLUDED.updated_at >= agents_harness_sessions.updated_at)
				THEN EXCLUDED.agent_uid ELSE agents_harness_sessions.agent_uid END,
			harness_session_id=CASE WHEN EXCLUDED.observed_epoch > agents_harness_sessions.observed_epoch OR
				(EXCLUDED.observed_epoch = agents_harness_sessions.observed_epoch AND
				 EXCLUDED.updated_at >= agents_harness_sessions.updated_at)
				THEN EXCLUDED.harness_session_id ELSE agents_harness_sessions.harness_session_id END,
			backend_key=CASE WHEN EXCLUDED.observed_epoch > agents_harness_sessions.observed_epoch OR
				(EXCLUDED.observed_epoch = agents_harness_sessions.observed_epoch AND
				 EXCLUDED.updated_at >= agents_harness_sessions.updated_at)
				THEN EXCLUDED.backend_key ELSE agents_harness_sessions.backend_key END,
			turns=GREATEST(agents_harness_sessions.turns, EXCLUDED.turns),
			observed_epoch=GREATEST(agents_harness_sessions.observed_epoch, EXCLUDED.observed_epoch),
			updated_at=GREATEST(agents_harness_sessions.updated_at, EXCLUDED.updated_at)`,
		fromOrg, fromWorkspace, toOrg, toWorkspace); err != nil {
		return fmt.Errorf("migrate harness sessions for cluster %s: %w", clusterID, err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM agents_harness_sessions WHERE org_uuid=$1 AND workspace_uuid=$2`, fromOrg, fromWorkspace); err != nil {
		return fmt.Errorf("remove migrated harness sessions for cluster %s: %w", clusterID, err)
	}
	return nil
}

// beginScopedMutation holds the tenant-map advisory lock from the final map
// lookup through the stateful operation whenever the caller still has a
// cluster-keyed fallback scope. SaveTenantRef takes the same lock while it
// migrates and publishes a mapping, so either the fallback mutation commits
// first and is included in that migration, or it sees the committed mapping
// and performs the mutation in the canonical scope.
//
// Canonical scopes do not take the lock: cluster mappings are immutable once
// learned, and these rows no longer participate in fallback migration.
func (p *PostgresStore) beginScopedMutation(ctx context.Context, scope Scope) (*scopedMutation, error) {
	mutation := &scopedMutation{scope: scope, executor: p.db}
	if scope.OrgUUID != UnmappedOrg {
		return mutation, nil
	}
	clusterID := scope.ClusterID
	if clusterID == "" {
		clusterID = scope.WorkspaceUUID
	}
	if strings.TrimSpace(clusterID) == "" {
		return nil, fmt.Errorf("fallback scope is missing its cluster ID")
	}
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin scoped mutation: %w", err)
	}
	mutation.tx, mutation.executor = tx, tx
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, "agents-tenant-map:"+clusterID); err != nil {
		mutation.rollback()
		return nil, fmt.Errorf("lock scoped mutation for cluster %s: %w", clusterID, err)
	}
	var ref TenantRef
	err = tx.QueryRowContext(ctx, `
		SELECT org_uuid, workspace_uuid, updated_at FROM agents_tenants WHERE cluster_id=$1`, clusterID).
		Scan(&ref.OrgUUID, &ref.WorkspaceUUID, &ref.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		// No mapping is a valid fallback case. Keep the lock through the write so
		// a concurrent SaveTenantRef cannot migrate an empty snapshot and then
		// miss the row this operation is about to create.
		return mutation, nil
	}
	if err != nil {
		mutation.rollback()
		return nil, fmt.Errorf("resolve scoped mutation for cluster %s: %w", clusterID, err)
	}
	if ref.OrgUUID == "" || ref.WorkspaceUUID == "" {
		mutation.rollback()
		return nil, fmt.Errorf("tenant mapping for cluster %s is incomplete", clusterID)
	}
	if err := migrateUnmappedScopeTx(ctx, tx, clusterID, ref); err != nil {
		mutation.rollback()
		return nil, err
	}
	mutation.scope.OrgUUID, mutation.scope.WorkspaceUUID, mutation.scope.ClusterID = ref.OrgUUID, ref.WorkspaceUUID, clusterID
	return mutation, nil
}

// normalizeScope follows a legacy scope onto its mapped tenant. Missing mappings
// are a valid legacy scope; lookup or migration failures are carried through to
// validate so callers cannot accidentally read or allocate history in a scope
// that may no longer be authoritative.
func (p *PostgresStore) normalizeScope(ctx context.Context, scope Scope) Scope {
	if scope.OrgUUID != UnmappedOrg || scope.WorkspaceUUID == "" {
		return scope
	}
	clusterID := scope.ClusterID
	if clusterID == "" {
		clusterID = scope.WorkspaceUUID
	}
	ref, ok, err := p.GetTenantRef(ctx, clusterID)
	if err != nil {
		scope.resolutionErr = fmt.Sprintf("cluster %s mapping lookup: %v", clusterID, err)
		return scope
	}
	if !ok {
		return scope
	}
	if ref.OrgUUID == "" || ref.WorkspaceUUID == "" {
		scope.resolutionErr = fmt.Sprintf("cluster %s mapping is incomplete", clusterID)
		return scope
	}
	if err := p.SaveTenantRef(ctx, clusterID, ref); err != nil {
		scope.resolutionErr = fmt.Sprintf("cluster %s mapping migration: %v", clusterID, err)
		return scope
	}
	scope.OrgUUID, scope.WorkspaceUUID, scope.ClusterID = ref.OrgUUID, ref.WorkspaceUUID, clusterID
	return scope
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
	scope = p.normalizeScope(ctx, scope)
	if err := scope.withAgent(); err != nil {
		return Run{}, false, err
	}
	if strings.TrimSpace(key) == "" {
		return Run{}, false, nil
	}
	row := p.db.QueryRowContext(ctx, `
		SELECT `+runColumns+`
		FROM agents_runs
		WHERE org_uuid=$1 AND workspace_uuid=$2 AND agent_name=$3 AND idempotency_key=$4 AND idempotency_primary`,
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

// ---- recovery -------------------------------------------------------------------------

// ---- teardown -------------------------------------------------------------------------

func (p *PostgresStore) DeleteAgentData(ctx context.Context, scope Scope, agentName string) error {
	mutation, err := p.beginScopedMutation(ctx, scope)
	if err != nil {
		return err
	}
	defer mutation.rollback()
	scope = mutation.scope
	if err := scope.validate(); err != nil {
		return err
	}
	for _, table := range []string{"agents_messages", "agents_runs", "agents_memories", "agents_inbox", "agents_tool_calls", "agents_usage", "agents_session_summaries", "agents_harness_sessions"} {
		if _, err := mutation.executor.ExecContext(ctx,
			fmt.Sprintf(`DELETE FROM %s WHERE org_uuid=$1 AND workspace_uuid=$2 AND agent_name=$3`, table),
			scope.OrgUUID, scope.WorkspaceUUID, agentName); err != nil {
			return err
		}
	}
	return mutation.commit()
}

// DeleteRunData removes one run's rows. See Store.DeleteRunData for why usage
// is not among them.
func (p *PostgresStore) DeleteRunData(ctx context.Context, scope Scope, runID string) error {
	mutation, err := p.beginScopedMutation(ctx, scope)
	if err != nil {
		return err
	}
	defer mutation.rollback()
	scope = mutation.scope
	if err := scope.withAgent(); err != nil {
		return err
	}
	if strings.TrimSpace(runID) == "" {
		return fmt.Errorf("run ID is required")
	}
	// Messages and tool calls first: a crash between statements must leave the
	// run row behind as the thing that still points at them, never orphans
	// nothing points at.
	for _, table := range []string{"agents_messages", "agents_tool_calls"} {
		if _, err := mutation.executor.ExecContext(ctx,
			fmt.Sprintf(`DELETE FROM %s WHERE org_uuid=$1 AND workspace_uuid=$2 AND agent_name=$3 AND run_id=$4`, table),
			scope.OrgUUID, scope.WorkspaceUUID, scope.AgentName, runID); err != nil {
			return err
		}
	}
	if _, err := mutation.executor.ExecContext(ctx,
		`DELETE FROM agents_runs WHERE org_uuid=$1 AND workspace_uuid=$2 AND agent_name=$3 AND id=$4`,
		scope.OrgUUID, scope.WorkspaceUUID, scope.AgentName, runID); err != nil {
		return err
	}
	return mutation.commit()
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

func nullInt64(value *int64) any {
	if value == nil {
		return nil
	}
	return *value
}

// Compile-time interface check.
var _ Store = (*PostgresStore)(nil)
