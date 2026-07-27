// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

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

const messageSchemaVersion = "v3"
const durableAssistantRunSchemaVersion = "v4"
const assistantRunConversationIndexSchemaVersion = "v5"

const createMessageSchemaMigrationsTable = `CREATE TABLE IF NOT EXISTS app_studio_message_schema_migrations (
	version text PRIMARY KEY,
	applied_at timestamptz NOT NULL DEFAULT now()
)`

// PostgresStore stores App Studio messages in Postgres with tenant-scoped
// primary keys and cursor pagination.
type PostgresStore struct {
	db *sql.DB
}

// OpenPostgres opens a Postgres-backed store, initializes the schema, and
// verifies the connection before returning it.
func OpenPostgres(ctx context.Context, dsn string) (*PostgresStore, error) {
	dsn = strings.TrimSpace(dsn)
	if dsn == "" {
		return nil, fmt.Errorf("dsn is required")
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, fmt.Errorf("open postgres: %w", err)
	}
	db.SetConnMaxLifetime(30 * time.Minute)
	db.SetMaxIdleConns(4)
	db.SetMaxOpenConns(8)

	store := &PostgresStore{db: db}
	if err := store.EnsureSchema(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping postgres: %w", err)
	}
	return store, nil
}

func (s *PostgresStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *PostgresStore) EnsureSchema(ctx context.Context) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("postgres store is nil")
	}

	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin schema migration tx: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	if _, err = tx.ExecContext(ctx, createMessageSchemaMigrationsTable); err != nil {
		return fmt.Errorf("create schema migrations table: %w", err)
	}

	stmts := []string{
		`CREATE TABLE IF NOT EXISTS app_studio_messages (
			org_uuid text NOT NULL,
			workspace_uuid text NOT NULL,
			project_name text NOT NULL,
			message_id text NOT NULL,
			role text NOT NULL,
			content text NOT NULL,
			content_encrypted boolean NOT NULL DEFAULT false,
			content_key_id text NOT NULL DEFAULT '',
			metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
			created_at timestamptz NOT NULL,
			updated_at timestamptz NOT NULL,
			PRIMARY KEY (org_uuid, workspace_uuid, project_name, message_id)
		)`,
		`CREATE INDEX IF NOT EXISTS app_studio_messages_scope_created_idx
			ON app_studio_messages (org_uuid, workspace_uuid, project_name, created_at, message_id)`,
		`CREATE INDEX IF NOT EXISTS app_studio_messages_created_idx
			ON app_studio_messages (created_at)`,
		`CREATE TABLE IF NOT EXISTS app_studio_assistant_runs (
			org_uuid text NOT NULL,
			workspace_uuid text NOT NULL,
			project_name text NOT NULL,
			run_id text NOT NULL,
			status text NOT NULL,
			request_id text NOT NULL DEFAULT '',
			checkpoint jsonb NOT NULL DEFAULT '{}'::jsonb,
			audit jsonb NOT NULL DEFAULT '{}'::jsonb,
			created_at timestamptz NOT NULL,
			updated_at timestamptz NOT NULL,
			PRIMARY KEY (org_uuid, workspace_uuid, project_name, run_id)
		)`,
		`CREATE INDEX IF NOT EXISTS app_studio_assistant_runs_scope_updated_idx
			ON app_studio_assistant_runs (org_uuid, workspace_uuid, project_name, updated_at, run_id)`,
	}
	if err := ensureSchemaVersion(ctx, tx, messageSchemaVersion, stmts...); err != nil {
		return err
	}
	durableRunStmts := []string{
		`ALTER TABLE app_studio_assistant_runs ADD COLUMN IF NOT EXISTS client_request_id text NOT NULL DEFAULT ''`,
		`ALTER TABLE app_studio_assistant_runs ADD COLUMN IF NOT EXISTS user_message_id text NOT NULL DEFAULT ''`,
		`ALTER TABLE app_studio_assistant_runs ADD COLUMN IF NOT EXISTS active_message_id text NOT NULL DEFAULT ''`,
		`ALTER TABLE app_studio_assistant_runs ADD COLUMN IF NOT EXISTS revision bigint NOT NULL DEFAULT 0`,
		`UPDATE app_studio_assistant_runs
		SET status = 'interrupted'
		WHERE status = 'running'`,
		`CREATE UNIQUE INDEX IF NOT EXISTS app_studio_assistant_runs_scope_client_request_idx
			ON app_studio_assistant_runs (org_uuid, workspace_uuid, project_name, client_request_id)
			WHERE client_request_id <> '' AND run_id <> 'approved-plan-grant'`,
		`CREATE UNIQUE INDEX IF NOT EXISTS app_studio_assistant_runs_scope_active_idx
			ON app_studio_assistant_runs (org_uuid, workspace_uuid, project_name)
			WHERE run_id <> 'approved-plan-grant' AND status NOT IN ('completed', 'aborted', 'failed', 'interrupted')`,
	}
	if err := ensureSchemaVersion(ctx, tx, durableAssistantRunSchemaVersion, durableRunStmts...); err != nil {
		return err
	}
	conversationIndexStmts := []string{
		`DROP INDEX IF EXISTS app_studio_assistant_runs_scope_client_request_idx`,
		`DROP INDEX IF EXISTS app_studio_assistant_runs_scope_active_idx`,
		`CREATE UNIQUE INDEX app_studio_assistant_runs_scope_client_request_idx
			ON app_studio_assistant_runs (org_uuid, workspace_uuid, project_name, client_request_id)
			WHERE client_request_id <> '' AND run_id <> 'approved-plan-grant'`,
		`CREATE UNIQUE INDEX app_studio_assistant_runs_scope_active_idx
			ON app_studio_assistant_runs (org_uuid, workspace_uuid, project_name)
			WHERE run_id <> 'approved-plan-grant' AND status NOT IN ('completed', 'aborted', 'failed', 'interrupted')`,
	}
	if err := ensureSchemaVersion(ctx, tx, assistantRunConversationIndexSchemaVersion, conversationIndexStmts...); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("commit schema migration: %w", err)
	}
	return nil
}

func ensureSchemaVersion(ctx context.Context, tx *sql.Tx, version string, stmts ...string) error {
	var exists bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS (
		SELECT 1 FROM app_studio_message_schema_migrations WHERE version = $1
	)`, version).Scan(&exists); err != nil {
		return fmt.Errorf("check schema migration %s: %w", version, err)
	}
	if exists {
		return nil
	}
	for _, stmt := range stmts {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("apply schema statement for %s: %w", version, err)
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO app_studio_message_schema_migrations(version) VALUES ($1) ON CONFLICT DO NOTHING`, version); err != nil {
		return fmt.Errorf("record schema migration %s: %w", version, err)
	}
	return nil
}

func (s *PostgresStore) AppendMessage(ctx context.Context, scope Scope, msg Message) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("postgres store is nil")
	}
	if err := scope.validate(); err != nil {
		return err
	}
	if msg.ID == "" {
		return fmt.Errorf("message id is required")
	}
	if msg.CreatedAt.IsZero() {
		msg.CreatedAt = time.Now().UTC()
	}
	if msg.UpdatedAt.IsZero() {
		msg.UpdatedAt = msg.CreatedAt
	}
	msg.ProjectName = scope.ProjectName
	if msg.Metadata == nil {
		msg.Metadata = map[string]any{}
	}
	metadata, err := json.Marshal(msg.Metadata)
	if err != nil {
		return fmt.Errorf("marshal message metadata: %w", err)
	}

	_, err = s.db.ExecContext(ctx, `
		INSERT INTO app_studio_messages (
			org_uuid, workspace_uuid, project_name, message_id,
			role, content, content_encrypted, content_key_id,
			metadata, created_at, updated_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
		ON CONFLICT (org_uuid, workspace_uuid, project_name, message_id)
		DO UPDATE SET
			role = EXCLUDED.role,
			content = EXCLUDED.content,
			content_encrypted = EXCLUDED.content_encrypted,
			content_key_id = EXCLUDED.content_key_id,
			metadata = EXCLUDED.metadata,
			created_at = EXCLUDED.created_at,
			updated_at = EXCLUDED.updated_at
	`,
		scope.OrgUUID, scope.WorkspaceUUID, scope.ProjectName, msg.ID,
		msg.Role, msg.Content, msg.ContentEncrypted, msg.ContentKeyID,
		metadata, msg.CreatedAt.UTC(), msg.UpdatedAt.UTC(),
	)
	if err != nil {
		return fmt.Errorf("upsert message: %w", err)
	}
	return nil
}

func (s *PostgresStore) ListMessages(ctx context.Context, scope Scope, limit int, cursor string) (page Page, err error) {
	if s == nil || s.db == nil {
		return Page{}, fmt.Errorf("postgres store is nil")
	}
	if err := scope.validate(); err != nil {
		return Page{}, err
	}
	limit = normalizeLimit(limit)

	cutoffAt, cutoffID, err := decodeCursor(cursor)
	if err != nil {
		return Page{}, err
	}

	query := `
		SELECT message_id, role, content, content_encrypted, content_key_id,
		       metadata, created_at, updated_at
		FROM app_studio_messages
		WHERE org_uuid = $1 AND workspace_uuid = $2 AND project_name = $3`
	args := []any{scope.OrgUUID, scope.WorkspaceUUID, scope.ProjectName}
	if !cutoffAt.IsZero() {
		query += ` AND (created_at, message_id) > ($4, $5)`
		args = append(args, cutoffAt.UTC(), cutoffID)
	}
	query += ` ORDER BY created_at ASC, message_id ASC LIMIT $` + fmt.Sprint(len(args)+1)
	args = append(args, limit+1)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return Page{}, fmt.Errorf("list messages: %w", err)
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil && err == nil {
			err = fmt.Errorf("close list messages rows: %w", closeErr)
		}
	}()

	items := make([]Message, 0, limit)
	for rows.Next() {
		msg, err := scanMessage(rows, scope.ProjectName)
		if err != nil {
			return Page{}, err
		}
		items = append(items, msg)
	}
	if err := rows.Err(); err != nil {
		return Page{}, fmt.Errorf("list messages rows: %w", err)
	}

	page = Page{Items: items}
	if len(page.Items) > limit {
		last := page.Items[limit-1]
		page.Items = page.Items[:limit]
		page.NextCursor = encodeCursor(last.CreatedAt, last.ID)
	}
	return page, nil
}

func (s *PostgresStore) LoadRecentMessages(ctx context.Context, scope Scope, limit int) (items []Message, err error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("postgres store is nil")
	}
	if err := scope.validate(); err != nil {
		return nil, err
	}
	limit = normalizeLimit(limit)
	rows, err := s.db.QueryContext(ctx, `
		SELECT message_id, role, content, content_encrypted, content_key_id,
		       metadata, created_at, updated_at
		FROM app_studio_messages
		WHERE org_uuid = $1 AND workspace_uuid = $2 AND project_name = $3
		ORDER BY created_at DESC, message_id DESC
		LIMIT $4
	`, scope.OrgUUID, scope.WorkspaceUUID, scope.ProjectName, limit)
	if err != nil {
		return nil, fmt.Errorf("load recent messages: %w", err)
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil && err == nil {
			err = fmt.Errorf("close recent messages rows: %w", closeErr)
		}
	}()

	for rows.Next() {
		msg, err := scanMessage(rows, scope.ProjectName)
		if err != nil {
			return nil, err
		}
		items = append(items, msg)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("load recent messages rows: %w", err)
	}
	for i, j := 0, len(items)-1; i < j; i, j = i+1, j-1 {
		items[i], items[j] = items[j], items[i]
	}
	return items, nil
}

func (s *PostgresStore) SaveAssistantRun(ctx context.Context, scope Scope, run AssistantRun) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("postgres store is nil")
	}
	if err := scope.validate(); err != nil {
		return err
	}
	if run.ID == "" {
		return fmt.Errorf("assistant run id is required")
	}
	if run.Status == "" {
		return fmt.Errorf("assistant run status is required")
	}
	if run.CreatedAt.IsZero() {
		run.CreatedAt = time.Now().UTC()
	}
	if run.UpdatedAt.IsZero() {
		run.UpdatedAt = run.CreatedAt
	}
	run.ProjectName = scope.ProjectName
	checkpoint := run.Checkpoint
	if len(checkpoint) == 0 {
		checkpoint = json.RawMessage(`{}`)
	}
	normalizedCheckpoint, err := normalizePostgresJSONB(checkpoint)
	if err != nil {
		return fmt.Errorf("assistant run checkpoint is not valid json: %w", err)
	}
	audit := run.Audit
	if len(audit) == 0 {
		audit = json.RawMessage(`{}`)
	}
	normalizedAudit, err := normalizePostgresJSONB(audit)
	if err != nil {
		return fmt.Errorf("assistant run audit is not valid json: %w", err)
	}

	_, err = s.db.ExecContext(ctx, `
		INSERT INTO app_studio_assistant_runs (
			org_uuid, workspace_uuid, project_name, run_id,
			status, client_request_id, user_message_id, active_message_id, revision, request_id,
			checkpoint, audit, created_at, updated_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)
		ON CONFLICT (org_uuid, workspace_uuid, project_name, run_id)
		DO UPDATE SET
			status = EXCLUDED.status,
			user_message_id = EXCLUDED.user_message_id,
			active_message_id = EXCLUDED.active_message_id,
			request_id = EXCLUDED.request_id,
			checkpoint = EXCLUDED.checkpoint,
			audit = EXCLUDED.audit,
			updated_at = EXCLUDED.updated_at
	`,
		scope.OrgUUID, scope.WorkspaceUUID, scope.ProjectName, run.ID,
		run.Status, run.ClientRequestID, run.UserMessageID, run.ActiveMessageID, run.Revision, run.RequestID,
		string(normalizedCheckpoint), string(normalizedAudit), run.CreatedAt.UTC(), run.UpdatedAt.UTC(),
	)
	if err != nil {
		if isAssistantRunUniqueViolation(err) {
			return fmt.Errorf("%w: project already has active assistant run", ErrAssistantRunConflict)
		}
		return fmt.Errorf("upsert assistant run: %w", err)
	}
	return nil
}

func (s *PostgresStore) CreateAssistantRun(ctx context.Context, scope Scope, user Message, assistant Message, run AssistantRun) (AssistantRun, error) {
	if s == nil || s.db == nil {
		return AssistantRun{}, fmt.Errorf("postgres store is nil")
	}
	if err := scope.validate(); err != nil {
		return AssistantRun{}, err
	}
	if err := validateNewAssistantRun(user, assistant, run); err != nil {
		return AssistantRun{}, err
	}
	user = prepareMessage(scope, user)
	assistant = prepareMessage(scope, assistant)
	run = prepareAssistantRun(scope, run)
	checkpoint, audit, err := normalizeAssistantRunJSON(run)
	if err != nil {
		return AssistantRun{}, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AssistantRun{}, fmt.Errorf("begin create assistant run: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	row := tx.QueryRowContext(ctx, `
		INSERT INTO app_studio_assistant_runs (
			org_uuid, workspace_uuid, project_name, run_id, status,
			client_request_id, user_message_id, active_message_id, revision, request_id,
			checkpoint, audit, created_at, updated_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)
		ON CONFLICT DO NOTHING
		RETURNING run_id, status, client_request_id, user_message_id, active_message_id, revision,
		          request_id, checkpoint, audit, created_at, updated_at
	`, scope.OrgUUID, scope.WorkspaceUUID, scope.ProjectName, run.ID, run.Status,
		run.ClientRequestID, run.UserMessageID, run.ActiveMessageID, run.Revision, run.RequestID,
		string(checkpoint), string(audit), run.CreatedAt.UTC(), run.UpdatedAt.UTC())
	inserted, err := scanAssistantRun(row, scope.ProjectName)
	if err == sql.ErrNoRows {
		existing, lookupErr := getAssistantRunByClientRequestID(ctx, tx, scope, run.ClientRequestID)
		if lookupErr == nil {
			if err := tx.Commit(); err != nil {
				return AssistantRun{}, fmt.Errorf("commit duplicate assistant run lookup: %w", err)
			}
			return existing, nil
		}
		if errors.Is(lookupErr, ErrAssistantRunNotFound) {
			return AssistantRun{}, fmt.Errorf("%w: project already has an active assistant run", ErrAssistantRunConflict)
		}
		return AssistantRun{}, lookupErr
	}
	if err != nil {
		return AssistantRun{}, fmt.Errorf("insert assistant run: %w", err)
	}
	if err := appendMessageTx(ctx, tx, scope, user); err != nil {
		return AssistantRun{}, err
	}
	if err := appendMessageTx(ctx, tx, scope, assistant); err != nil {
		return AssistantRun{}, err
	}
	if err := tx.Commit(); err != nil {
		return AssistantRun{}, fmt.Errorf("commit create assistant run: %w", err)
	}
	return inserted, nil
}

func (s *PostgresStore) SaveAssistantRunSnapshot(ctx context.Context, scope Scope, run AssistantRun, messages []Message, expectedRevision int64) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("postgres store is nil")
	}
	if err := scope.validate(); err != nil {
		return err
	}
	if err := validateAssistantRunSnapshot(run, messages, expectedRevision); err != nil {
		return err
	}
	run = prepareAssistantRun(scope, run)
	checkpoint, audit, err := normalizeAssistantRunJSON(run)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin save assistant run snapshot: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	result, err := tx.ExecContext(ctx, `
		UPDATE app_studio_assistant_runs
		SET status = $5,
			active_message_id = $6,
			revision = $7,
			request_id = $8,
			checkpoint = $9,
			audit = $10,
			updated_at = $11
		WHERE org_uuid = $1 AND workspace_uuid = $2 AND project_name = $3 AND run_id = $4
		  AND revision = $12
	`, scope.OrgUUID, scope.WorkspaceUUID, scope.ProjectName, run.ID,
		run.Status, run.ActiveMessageID, run.Revision, run.RequestID,
		string(checkpoint), string(audit), run.UpdatedAt.UTC(), expectedRevision)
	if err != nil {
		if isAssistantRunUniqueViolation(err) {
			return fmt.Errorf("%w: project already has active assistant run", ErrAssistantRunConflict)
		}
		return fmt.Errorf("update assistant run snapshot: %w", err)
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("count updated assistant run snapshot: %w", err)
	}
	if updated != 1 {
		return fmt.Errorf("%w: assistant run %q", ErrAssistantRunConflict, run.ID)
	}
	for _, message := range messages {
		if err := appendMessageTx(ctx, tx, scope, prepareMessage(scope, message)); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit assistant run snapshot: %w", err)
	}
	return nil
}

func (s *PostgresStore) CompareAndSwapAssistantRun(
	ctx context.Context,
	scope Scope,
	run AssistantRun,
	expectedRequestID string,
) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("postgres store is nil")
	}
	if err := scope.validate(); err != nil {
		return err
	}
	if run.ID == "" {
		return fmt.Errorf("assistant run id is required")
	}
	if run.Status == "" {
		return fmt.Errorf("assistant run status is required")
	}
	if run.CreatedAt.IsZero() {
		run.CreatedAt = time.Now().UTC()
	}
	if run.UpdatedAt.IsZero() {
		run.UpdatedAt = run.CreatedAt
	}
	run.ProjectName = scope.ProjectName
	checkpoint := run.Checkpoint
	if len(checkpoint) == 0 {
		checkpoint = json.RawMessage(`{}`)
	}
	normalizedCheckpoint, err := normalizePostgresJSONB(checkpoint)
	if err != nil {
		return fmt.Errorf("assistant run checkpoint is not valid json: %w", err)
	}
	audit := run.Audit
	if len(audit) == 0 {
		audit = json.RawMessage(`{}`)
	}
	normalizedAudit, err := normalizePostgresJSONB(audit)
	if err != nil {
		return fmt.Errorf("assistant run audit is not valid json: %w", err)
	}

	var result sql.Result
	if expectedRequestID == "" {
		result, err = s.db.ExecContext(ctx, `
			INSERT INTO app_studio_assistant_runs (
				org_uuid, workspace_uuid, project_name, run_id,
				status, client_request_id, user_message_id, active_message_id, revision, request_id,
				checkpoint, audit, created_at, updated_at
			) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)
			ON CONFLICT (org_uuid, workspace_uuid, project_name, run_id)
			DO UPDATE SET
				status = EXCLUDED.status,
				user_message_id = EXCLUDED.user_message_id,
				active_message_id = EXCLUDED.active_message_id,
				request_id = EXCLUDED.request_id,
				checkpoint = EXCLUDED.checkpoint,
				audit = EXCLUDED.audit,
				updated_at = EXCLUDED.updated_at
			WHERE app_studio_assistant_runs.request_id = ''
		`,
			scope.OrgUUID, scope.WorkspaceUUID, scope.ProjectName, run.ID,
			run.Status, run.ClientRequestID, run.UserMessageID, run.ActiveMessageID, run.Revision, run.RequestID,
			string(normalizedCheckpoint), string(normalizedAudit), run.CreatedAt.UTC(), run.UpdatedAt.UTC(),
		)
	} else {
		result, err = s.db.ExecContext(ctx, `
			UPDATE app_studio_assistant_runs
			SET status = $5,
				active_message_id = $6,
				request_id = $7,
				checkpoint = $8,
				audit = $9,
				updated_at = $10
			WHERE org_uuid = $1
				AND workspace_uuid = $2
				AND project_name = $3
				AND run_id = $4
				AND request_id = $11
		`,
			scope.OrgUUID, scope.WorkspaceUUID, scope.ProjectName, run.ID,
			run.Status, run.ActiveMessageID, run.RequestID, string(normalizedCheckpoint), string(normalizedAudit), run.UpdatedAt.UTC(), expectedRequestID,
		)
	}
	if err != nil {
		if isAssistantRunUniqueViolation(err) {
			return fmt.Errorf("%w: project already has active assistant run", ErrAssistantRunConflict)
		}
		return fmt.Errorf("compare and swap assistant run: %w", err)
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read compare and swap assistant run result: %w", err)
	}
	if updated != 1 {
		return fmt.Errorf("%w: assistant run %q", ErrAssistantRunConflict, run.ID)
	}
	return nil
}

func normalizePostgresJSONB(raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var parsed any
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, err
	}
	normalized, err := json.Marshal(sanitizePostgresJSONBValue(parsed))
	if err != nil {
		return nil, err
	}
	return normalized, nil
}

func sanitizePostgresJSONBValue(value any) any {
	switch typed := value.(type) {
	case string:
		return strings.ReplaceAll(typed, "\x00", "\ufffd")
	case []any:
		for i := range typed {
			typed[i] = sanitizePostgresJSONBValue(typed[i])
		}
		return typed
	case map[string]any:
		sanitized := make(map[string]any, len(typed))
		for key, item := range typed {
			sanitized[strings.ReplaceAll(key, "\x00", "\ufffd")] = sanitizePostgresJSONBValue(item)
		}
		return sanitized
	default:
		return typed
	}
}

func (s *PostgresStore) ClaimAssistantRun(ctx context.Context, scope Scope, id string, requestID string, now time.Time) (AssistantRun, error) {
	if s == nil || s.db == nil {
		return AssistantRun{}, fmt.Errorf("postgres store is nil")
	}
	if err := scope.validate(); err != nil {
		return AssistantRun{}, err
	}
	if id == "" {
		return AssistantRun{}, fmt.Errorf("assistant run id is required")
	}
	if requestID == "" {
		return AssistantRun{}, fmt.Errorf("assistant run request id is required")
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	row := s.db.QueryRowContext(ctx, `
		UPDATE app_studio_assistant_runs
		SET status = $1, updated_at = $2
		WHERE org_uuid = $3
		  AND workspace_uuid = $4
		  AND project_name = $5
		  AND run_id = $6
		  AND request_id = $7
		  AND status IN ($8, $9)
		RETURNING run_id, status, client_request_id, user_message_id, active_message_id, revision,
		          request_id, checkpoint, audit, created_at, updated_at
	`,
		AssistantRunStatusRunning, now.UTC(),
		scope.OrgUUID, scope.WorkspaceUUID, scope.ProjectName, id, requestID,
		AssistantRunStatusPendingPermission, AssistantRunStatusPendingInput,
	)

	run, err := scanAssistantRun(row, scope.ProjectName)
	if err != nil {
		if err == sql.ErrNoRows {
			return AssistantRun{}, fmt.Errorf("assistant run %q is not waiting for this request", id)
		}
		return AssistantRun{}, fmt.Errorf("claim assistant run: %w", err)
	}
	return run, nil
}

func (s *PostgresStore) GetAssistantRun(ctx context.Context, scope Scope, id string) (AssistantRun, error) {
	if s == nil || s.db == nil {
		return AssistantRun{}, fmt.Errorf("postgres store is nil")
	}
	if err := scope.validate(); err != nil {
		return AssistantRun{}, err
	}
	if id == "" {
		return AssistantRun{}, fmt.Errorf("assistant run id is required")
	}
	row := s.db.QueryRowContext(ctx, `
		SELECT run_id, status, client_request_id, user_message_id, active_message_id, revision,
		       request_id, checkpoint, audit, created_at, updated_at
		FROM app_studio_assistant_runs
		WHERE org_uuid = $1 AND workspace_uuid = $2 AND project_name = $3 AND run_id = $4
	`, scope.OrgUUID, scope.WorkspaceUUID, scope.ProjectName, id)

	run, err := scanAssistantRun(row, scope.ProjectName)
	if err != nil {
		if err == sql.ErrNoRows {
			return AssistantRun{}, fmt.Errorf("%w: %q", ErrAssistantRunNotFound, id)
		}
		return AssistantRun{}, fmt.Errorf("get assistant run: %w", err)
	}
	return run, nil
}

func (s *PostgresStore) FindAssistantRunByClientRequestID(ctx context.Context, scope Scope, clientRequestID string) (AssistantRun, error) {
	if s == nil || s.db == nil {
		return AssistantRun{}, fmt.Errorf("postgres store is nil")
	}
	if err := scope.validate(); err != nil {
		return AssistantRun{}, err
	}
	if clientRequestID == "" {
		return AssistantRun{}, fmt.Errorf("assistant run client request id is required")
	}
	return getAssistantRunByClientRequestID(ctx, s.db, scope, clientRequestID)
}

func (s *PostgresStore) LatestAssistantRun(ctx context.Context, scope Scope) (AssistantRun, error) {
	if s == nil || s.db == nil {
		return AssistantRun{}, fmt.Errorf("postgres store is nil")
	}
	if err := scope.validate(); err != nil {
		return AssistantRun{}, err
	}
	row := s.db.QueryRowContext(ctx, `
		SELECT run_id, status, client_request_id, user_message_id, active_message_id, revision,
		       request_id, checkpoint, audit, created_at, updated_at
		FROM app_studio_assistant_runs
		WHERE org_uuid = $1 AND workspace_uuid = $2 AND project_name = $3
		  AND run_id <> $4
		ORDER BY updated_at DESC, run_id DESC
		LIMIT 1
	`, scope.OrgUUID, scope.WorkspaceUUID, scope.ProjectName, AssistantRunIDApprovedPlanGrant)
	run, err := scanAssistantRun(row, scope.ProjectName)
	if err == sql.ErrNoRows {
		return AssistantRun{}, fmt.Errorf("%w: latest run", ErrAssistantRunNotFound)
	}
	if err != nil {
		return AssistantRun{}, fmt.Errorf("get latest assistant run: %w", err)
	}
	return run, nil
}

func (s *PostgresStore) DeleteProjectMessages(ctx context.Context, scope Scope) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("postgres store is nil")
	}
	if err := scope.validate(); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `
		DELETE FROM app_studio_messages
		WHERE org_uuid = $1 AND workspace_uuid = $2 AND project_name = $3
	`, scope.OrgUUID, scope.WorkspaceUUID, scope.ProjectName); err != nil {
		return fmt.Errorf("delete project messages: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `
		DELETE FROM app_studio_assistant_runs
		WHERE org_uuid = $1 AND workspace_uuid = $2 AND project_name = $3
	`, scope.OrgUUID, scope.WorkspaceUUID, scope.ProjectName); err != nil {
		return fmt.Errorf("delete project assistant runs: %w", err)
	}
	return nil
}

func (s *PostgresStore) DeleteMessagesOlderThan(ctx context.Context, before time.Time) (int64, error) {
	if s == nil || s.db == nil {
		return 0, fmt.Errorf("postgres store is nil")
	}
	res, err := s.db.ExecContext(ctx, `DELETE FROM app_studio_messages WHERE created_at < $1`, before.UTC())
	if err != nil {
		return 0, fmt.Errorf("delete stale messages: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("count deleted messages: %w", err)
	}
	runRes, err := s.db.ExecContext(ctx, `DELETE FROM app_studio_assistant_runs WHERE updated_at < $1 AND run_id <> $2`, before.UTC(), AssistantRunIDApprovedPlanGrant)
	if err != nil {
		return 0, fmt.Errorf("delete stale assistant runs: %w", err)
	}
	runN, err := runRes.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("count deleted assistant runs: %w", err)
	}
	return n + runN, nil
}

func scanMessage(row interface {
	Scan(dest ...any) error
}, projectName string) (Message, error) {
	var (
		msg       Message
		metadata  []byte
		createdAt time.Time
		updatedAt time.Time
	)
	if err := row.Scan(
		&msg.ID,
		&msg.Role,
		&msg.Content,
		&msg.ContentEncrypted,
		&msg.ContentKeyID,
		&metadata,
		&createdAt,
		&updatedAt,
	); err != nil {
		return Message{}, fmt.Errorf("scan message: %w", err)
	}
	msg.Metadata = map[string]any{}
	if len(metadata) > 0 {
		if err := json.Unmarshal(metadata, &msg.Metadata); err != nil {
			return Message{}, fmt.Errorf("decode message metadata: %w", err)
		}
	}
	msg.CreatedAt = createdAt.UTC()
	msg.UpdatedAt = updatedAt.UTC()
	msg.ProjectName = projectName
	return msg, nil
}

func appendMessageTx(ctx context.Context, tx *sql.Tx, scope Scope, msg Message) error {
	if msg.Metadata == nil {
		msg.Metadata = map[string]any{}
	}
	metadata, err := json.Marshal(msg.Metadata)
	if err != nil {
		return fmt.Errorf("marshal message metadata: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO app_studio_messages (
			org_uuid, workspace_uuid, project_name, message_id,
			role, content, content_encrypted, content_key_id,
			metadata, created_at, updated_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
		ON CONFLICT (org_uuid, workspace_uuid, project_name, message_id)
		DO UPDATE SET
			role = EXCLUDED.role,
			content = EXCLUDED.content,
			content_encrypted = EXCLUDED.content_encrypted,
			content_key_id = EXCLUDED.content_key_id,
			metadata = EXCLUDED.metadata,
			created_at = EXCLUDED.created_at,
			updated_at = EXCLUDED.updated_at
	`, scope.OrgUUID, scope.WorkspaceUUID, scope.ProjectName, msg.ID,
		msg.Role, msg.Content, msg.ContentEncrypted, msg.ContentKeyID,
		metadata, msg.CreatedAt.UTC(), msg.UpdatedAt.UTC()); err != nil {
		return fmt.Errorf("upsert snapshot message: %w", err)
	}
	return nil
}

func normalizeAssistantRunJSON(run AssistantRun) (json.RawMessage, json.RawMessage, error) {
	checkpoint := run.Checkpoint
	if len(checkpoint) == 0 {
		checkpoint = json.RawMessage(`{}`)
	}
	normalizedCheckpoint, err := normalizePostgresJSONB(checkpoint)
	if err != nil {
		return nil, nil, fmt.Errorf("assistant run checkpoint is not valid json: %w", err)
	}
	audit := run.Audit
	if len(audit) == 0 {
		audit = json.RawMessage(`{}`)
	}
	normalizedAudit, err := normalizePostgresJSONB(audit)
	if err != nil {
		return nil, nil, fmt.Errorf("assistant run audit is not valid json: %w", err)
	}
	return normalizedCheckpoint, normalizedAudit, nil
}

func getAssistantRunByClientRequestID(ctx context.Context, queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, scope Scope, clientRequestID string) (AssistantRun, error) {
	row := queryer.QueryRowContext(ctx, `
		SELECT run_id, status, client_request_id, user_message_id, active_message_id, revision,
		       request_id, checkpoint, audit, created_at, updated_at
		FROM app_studio_assistant_runs
		WHERE org_uuid = $1 AND workspace_uuid = $2 AND project_name = $3 AND client_request_id = $4
		  AND run_id <> $5
	`, scope.OrgUUID, scope.WorkspaceUUID, scope.ProjectName, clientRequestID, AssistantRunIDApprovedPlanGrant)
	run, err := scanAssistantRun(row, scope.ProjectName)
	if err == sql.ErrNoRows {
		return AssistantRun{}, fmt.Errorf("%w: client request %q", ErrAssistantRunNotFound, clientRequestID)
	}
	if err != nil {
		return AssistantRun{}, fmt.Errorf("get assistant run by client request id: %w", err)
	}
	return run, nil
}

func isAssistantRunUniqueViolation(err error) bool {
	var postgresErr *pq.Error
	return errors.As(err, &postgresErr) && postgresErr.Code == "23505"
}

func scanAssistantRun(row interface {
	Scan(dest ...any) error
}, projectName string) (AssistantRun, error) {
	var run AssistantRun
	var status string
	if err := row.Scan(
		&run.ID,
		&status,
		&run.ClientRequestID,
		&run.UserMessageID,
		&run.ActiveMessageID,
		&run.Revision,
		&run.RequestID,
		&run.Checkpoint,
		&run.Audit,
		&run.CreatedAt,
		&run.UpdatedAt,
	); err != nil {
		return AssistantRun{}, err
	}
	run.ProjectName = projectName
	run.Status = AssistantRunStatus(status)
	run.Checkpoint = cloneRawMessage(run.Checkpoint)
	run.Audit = cloneRawMessage(run.Audit)
	run.CreatedAt = run.CreatedAt.UTC()
	run.UpdatedAt = run.UpdatedAt.UTC()
	return run, nil
}

var _ Store = (*PostgresStore)(nil)
