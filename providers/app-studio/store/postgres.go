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
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/lib/pq"
)

const messageSchemaVersion = "work-item-v2"
const approvalModeSchemaVersion = "approval-mode-v1"
const approvalModeDefaultSchemaVersion = "approval-mode-default-auto-approve-v1"
const clientRequestUniqueSchemaVersion = "client-request-unique-v1"
const executionPlanSchemaVersion = "work-item-execution-plan-v1"
const cancellationReceiptSchemaVersion = "work-item-cancellation-receipt-v1"
const bootstrapPermitSchemaVersion = "project-bootstrap-permit-v1"

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
	defer func() { _ = tx.Rollback() }()

	if _, err = tx.ExecContext(ctx, createMessageSchemaMigrationsTable); err != nil {
		return fmt.Errorf("create schema migrations table: %w", err)
	}

	stmts := workItemSchemaStatements()
	missing, err := schemaVersionMissing(ctx, tx, messageSchemaVersion)
	if err != nil {
		return err
	}
	if missing {
		if err := rejectPopulatedLegacyProjectSchema(ctx, tx); err != nil {
			return err
		}
		// Empty legacy tables can be upgraded in place. Populated tables without
		// project_uid are rejected above: assigning their rows to a current
		// Project UID based only on a reused name would leak a prior project.
		for _, stmt := range workItemSchemaUpgradeStatements() {
			if _, err := tx.ExecContext(ctx, stmt); err != nil {
				return fmt.Errorf("upgrade legacy App Studio schema for %s: %w", messageSchemaVersion, err)
			}
		}
	}
	if err := ensureSchemaVersion(ctx, tx, messageSchemaVersion, stmts...); err != nil {
		return err
	}
	if err := ensureSchemaVersion(ctx, tx, clientRequestUniqueSchemaVersion, clientRequestUniqueSchemaStatements()...); err != nil {
		return err
	}
	if err := ensureSchemaVersion(ctx, tx, approvalModeSchemaVersion, approvalModeSchemaStatements()...); err != nil {
		return err
	}
	if err := ensureSchemaVersion(ctx, tx, approvalModeDefaultSchemaVersion, approvalModeDefaultSchemaStatements()...); err != nil {
		return err
	}
	if err := ensureSchemaVersion(ctx, tx, executionPlanSchemaVersion, executionPlanSchemaStatements()...); err != nil {
		return err
	}
	if err := ensureSchemaVersion(ctx, tx, cancellationReceiptSchemaVersion, cancellationReceiptSchemaStatements()...); err != nil {
		return err
	}
	if err := ensureSchemaVersion(ctx, tx, bootstrapPermitSchemaVersion, bootstrapPermitSchemaStatements()...); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("commit schema migration: %w", err)
	}
	return nil
}

func clientRequestUniqueSchemaStatements() []string {
	return []string{`CREATE UNIQUE INDEX IF NOT EXISTS app_studio_assistant_runs_scope_client_request_idx
		ON app_studio_assistant_runs (org_uuid, workspace_uuid, project_name, project_uid, client_request_id)
		WHERE client_request_id <> ''`}
}

func workItemSchemaUpgradeStatements() []string {
	return []string{
		`CREATE TABLE IF NOT EXISTS app_studio_messages (org_uuid text NOT NULL, workspace_uuid text NOT NULL, project_name text NOT NULL, project_uid text NOT NULL DEFAULT '', message_id text NOT NULL, role text NOT NULL DEFAULT '', actor_id text NOT NULL DEFAULT '', work_item_id text NOT NULL DEFAULT '', content text NOT NULL DEFAULT '', content_encrypted boolean NOT NULL DEFAULT false, content_key_id text NOT NULL DEFAULT '', metadata jsonb NOT NULL DEFAULT '{}'::jsonb, created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now(), PRIMARY KEY (org_uuid, workspace_uuid, project_name, project_uid, message_id))`,
		`CREATE TABLE IF NOT EXISTS app_studio_assistant_runs (org_uuid text NOT NULL, workspace_uuid text NOT NULL, project_name text NOT NULL, project_uid text NOT NULL DEFAULT '', run_id text NOT NULL, work_item_id text NOT NULL DEFAULT '', mode text NOT NULL DEFAULT 'discussion', approval_mode text NOT NULL DEFAULT 'auto_approve', expected_grant_revision text NOT NULL DEFAULT '', status text NOT NULL DEFAULT '', client_request_id text NOT NULL DEFAULT '', user_message_id text NOT NULL DEFAULT '', active_message_id text NOT NULL DEFAULT '', revision bigint NOT NULL DEFAULT 0, request_id text NOT NULL DEFAULT '', checkpoint jsonb NOT NULL DEFAULT '{}'::jsonb, audit jsonb NOT NULL DEFAULT '{}'::jsonb, created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now(), PRIMARY KEY (org_uuid, workspace_uuid, project_name, project_uid, run_id))`,
		`CREATE TABLE IF NOT EXISTS app_studio_assistant_work_items (org_uuid text NOT NULL, workspace_uuid text NOT NULL, project_name text NOT NULL, project_uid text NOT NULL DEFAULT '', work_item_id text NOT NULL, root_message_id text NOT NULL, created_by text NOT NULL, status text NOT NULL, status_reason text NOT NULL DEFAULT '', revision bigint NOT NULL DEFAULT 0, active_run_id text NOT NULL DEFAULT '', plan_grant jsonb NOT NULL DEFAULT '{}'::jsonb, grant_revision text NOT NULL DEFAULT '', execution_plan jsonb NOT NULL DEFAULT '{}'::jsonb, execution_plan_revision text NOT NULL DEFAULT '', cancellation_receipt jsonb NOT NULL DEFAULT '{}'::jsonb, created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now(), PRIMARY KEY (org_uuid, workspace_uuid, project_name, project_uid, work_item_id))`,
		`ALTER TABLE app_studio_messages ADD COLUMN IF NOT EXISTS project_uid text NOT NULL DEFAULT ''`,
		`ALTER TABLE app_studio_messages ADD COLUMN IF NOT EXISTS role text NOT NULL DEFAULT ''`,
		`ALTER TABLE app_studio_messages ADD COLUMN IF NOT EXISTS actor_id text NOT NULL DEFAULT ''`,
		`ALTER TABLE app_studio_messages ADD COLUMN IF NOT EXISTS work_item_id text NOT NULL DEFAULT ''`,
		`ALTER TABLE app_studio_messages ADD COLUMN IF NOT EXISTS content text NOT NULL DEFAULT ''`,
		`ALTER TABLE app_studio_messages ADD COLUMN IF NOT EXISTS content_encrypted boolean NOT NULL DEFAULT false`,
		`ALTER TABLE app_studio_messages ADD COLUMN IF NOT EXISTS content_key_id text NOT NULL DEFAULT ''`,
		`ALTER TABLE app_studio_messages ADD COLUMN IF NOT EXISTS metadata jsonb NOT NULL DEFAULT '{}'::jsonb`,
		`ALTER TABLE app_studio_messages ADD COLUMN IF NOT EXISTS created_at timestamptz NOT NULL DEFAULT now()`,
		`ALTER TABLE app_studio_messages ADD COLUMN IF NOT EXISTS updated_at timestamptz NOT NULL DEFAULT now()`,
		`ALTER TABLE app_studio_assistant_runs ADD COLUMN IF NOT EXISTS project_uid text NOT NULL DEFAULT ''`,
		`ALTER TABLE app_studio_assistant_runs ADD COLUMN IF NOT EXISTS work_item_id text NOT NULL DEFAULT ''`,
		`ALTER TABLE app_studio_assistant_runs ADD COLUMN IF NOT EXISTS mode text NOT NULL DEFAULT 'discussion'`,
		`ALTER TABLE app_studio_assistant_runs ADD COLUMN IF NOT EXISTS approval_mode text NOT NULL DEFAULT 'auto_approve'`,
		`ALTER TABLE app_studio_assistant_runs ADD COLUMN IF NOT EXISTS expected_grant_revision text NOT NULL DEFAULT ''`,
		`ALTER TABLE app_studio_assistant_runs ADD COLUMN IF NOT EXISTS status text NOT NULL DEFAULT ''`,
		`ALTER TABLE app_studio_assistant_runs ADD COLUMN IF NOT EXISTS client_request_id text NOT NULL DEFAULT ''`,
		`ALTER TABLE app_studio_assistant_runs ADD COLUMN IF NOT EXISTS user_message_id text NOT NULL DEFAULT ''`,
		`ALTER TABLE app_studio_assistant_runs ADD COLUMN IF NOT EXISTS active_message_id text NOT NULL DEFAULT ''`,
		`ALTER TABLE app_studio_assistant_runs ADD COLUMN IF NOT EXISTS revision bigint NOT NULL DEFAULT 0`,
		`ALTER TABLE app_studio_assistant_runs ADD COLUMN IF NOT EXISTS request_id text NOT NULL DEFAULT ''`,
		`ALTER TABLE app_studio_assistant_runs ADD COLUMN IF NOT EXISTS checkpoint jsonb NOT NULL DEFAULT '{}'::jsonb`,
		`ALTER TABLE app_studio_assistant_runs ADD COLUMN IF NOT EXISTS audit jsonb NOT NULL DEFAULT '{}'::jsonb`,
		`ALTER TABLE app_studio_assistant_runs ADD COLUMN IF NOT EXISTS created_at timestamptz NOT NULL DEFAULT now()`,
		`ALTER TABLE app_studio_assistant_runs ADD COLUMN IF NOT EXISTS updated_at timestamptz NOT NULL DEFAULT now()`,
		`ALTER TABLE app_studio_assistant_work_items ADD COLUMN IF NOT EXISTS cancellation_receipt jsonb NOT NULL DEFAULT '{}'::jsonb`,
		`ALTER TABLE app_studio_messages DROP CONSTRAINT IF EXISTS app_studio_messages_pkey`,
		`ALTER TABLE app_studio_messages ADD PRIMARY KEY (org_uuid, workspace_uuid, project_name, project_uid, message_id)`,
		`ALTER TABLE app_studio_assistant_runs DROP CONSTRAINT IF EXISTS app_studio_assistant_runs_pkey`,
		`ALTER TABLE app_studio_assistant_runs ADD PRIMARY KEY (org_uuid, workspace_uuid, project_name, project_uid, run_id)`,
		`DROP INDEX IF EXISTS app_studio_assistant_runs_scope_client_request_idx`,
		`DROP INDEX IF EXISTS app_studio_assistant_runs_scope_active_idx`,
	}
}

func rejectPopulatedLegacyProjectSchema(ctx context.Context, tx *sql.Tx) error {
	for _, table := range []string{"app_studio_messages", "app_studio_assistant_runs"} {
		var exists, hasProjectUID, hasRows bool
		if err := tx.QueryRowContext(ctx, `SELECT to_regclass($1) IS NOT NULL`, table).Scan(&exists); err != nil {
			return fmt.Errorf("check legacy App Studio table %s: %w", table, err)
		}
		if !exists {
			continue
		}
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema=current_schema() AND table_name=$1 AND column_name='project_uid')`, table).Scan(&hasProjectUID); err != nil {
			return fmt.Errorf("inspect legacy App Studio table %s: %w", table, err)
		}
		if hasProjectUID {
			continue
		}
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM `+table+`)`).Scan(&hasRows); err != nil {
			return fmt.Errorf("inspect legacy App Studio rows in %s: %w", table, err)
		}
		if hasRows {
			return fmt.Errorf("legacy App Studio %s rows have no project_uid; schema migration stopped without changing data. Map each legacy (org_uuid, workspace_uuid, project_name) to its immutable Project UID, backfill project_uid, and rerun", table)
		}
	}
	return nil
}

func approvalModeSchemaStatements() []string {
	return []string{
		`CREATE TABLE IF NOT EXISTS app_studio_assistant_approval_preferences (
			org_uuid text NOT NULL,
			workspace_uuid text NOT NULL,
			project_name text NOT NULL,
			project_uid text NOT NULL,
			actor_id text NOT NULL,
				approval_mode text NOT NULL CHECK (approval_mode IN ('always_ask', 'auto_approve')),
			updated_at timestamptz NOT NULL,
			PRIMARY KEY (org_uuid, workspace_uuid, project_name, project_uid, actor_id)
			)`,
		`ALTER TABLE app_studio_assistant_runs
				ADD COLUMN IF NOT EXISTS approval_mode text NOT NULL DEFAULT 'auto_approve'
				CHECK (approval_mode IN ('always_ask', 'auto_approve'))`,
	}
}

func approvalModeDefaultSchemaStatements() []string {
	return []string{`ALTER TABLE app_studio_assistant_runs
		ALTER COLUMN approval_mode SET DEFAULT 'auto_approve'`}
}

func executionPlanSchemaStatements() []string {
	return []string{
		`ALTER TABLE app_studio_assistant_work_items
			ADD COLUMN IF NOT EXISTS execution_plan jsonb NOT NULL DEFAULT '{}'::jsonb`,
		`ALTER TABLE app_studio_assistant_work_items
			ADD COLUMN IF NOT EXISTS execution_plan_revision text NOT NULL DEFAULT ''`,
	}
}

func cancellationReceiptSchemaStatements() []string {
	return []string{`ALTER TABLE app_studio_assistant_work_items
		ADD COLUMN IF NOT EXISTS cancellation_receipt jsonb NOT NULL DEFAULT '{}'::jsonb`}
}

func bootstrapPermitSchemaStatements() []string {
	return []string{`CREATE TABLE IF NOT EXISTS app_studio_project_bootstrap_permits (
		org_uuid text NOT NULL, workspace_uuid text NOT NULL, project_name text NOT NULL, project_uid text NOT NULL,
		actor_id text NOT NULL, prompt_digest text NOT NULL, consumed_client_request_id text NOT NULL DEFAULT '',
		created_at timestamptz NOT NULL, consumed_at timestamptz,
		PRIMARY KEY (org_uuid, workspace_uuid, project_name, project_uid)
	)`}
}

func workItemSchemaStatements() []string {
	return []string{
		`CREATE TABLE IF NOT EXISTS app_studio_messages (
			org_uuid text NOT NULL,
			workspace_uuid text NOT NULL,
			project_name text NOT NULL,
			project_uid text NOT NULL,
			message_id text NOT NULL,
			role text NOT NULL,
			actor_id text NOT NULL DEFAULT '',
			work_item_id text NOT NULL DEFAULT '',
			content text NOT NULL,
			content_encrypted boolean NOT NULL DEFAULT false,
			content_key_id text NOT NULL DEFAULT '',
			metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
			created_at timestamptz NOT NULL,
			updated_at timestamptz NOT NULL,
			PRIMARY KEY (org_uuid, workspace_uuid, project_name, project_uid, message_id)
		)`,
		`CREATE INDEX IF NOT EXISTS app_studio_messages_scope_created_idx
			ON app_studio_messages (org_uuid, workspace_uuid, project_name, project_uid, created_at, message_id)`,
		`CREATE INDEX IF NOT EXISTS app_studio_messages_created_idx
			ON app_studio_messages (created_at)`,
		`CREATE TABLE IF NOT EXISTS app_studio_assistant_runs (
			org_uuid text NOT NULL,
			workspace_uuid text NOT NULL,
			project_name text NOT NULL,
			project_uid text NOT NULL,
			run_id text NOT NULL,
				work_item_id text NOT NULL DEFAULT '',
				mode text NOT NULL DEFAULT 'discussion',
				approval_mode text NOT NULL DEFAULT 'auto_approve' CHECK (approval_mode IN ('always_ask', 'auto_approve')),
				expected_grant_revision text NOT NULL DEFAULT '',
			status text NOT NULL,
			client_request_id text NOT NULL DEFAULT '',
			user_message_id text NOT NULL DEFAULT '',
			active_message_id text NOT NULL DEFAULT '',
			revision bigint NOT NULL DEFAULT 0,
			request_id text NOT NULL DEFAULT '',
			checkpoint jsonb NOT NULL DEFAULT '{}'::jsonb,
			audit jsonb NOT NULL DEFAULT '{}'::jsonb,
			created_at timestamptz NOT NULL,
			updated_at timestamptz NOT NULL,
			PRIMARY KEY (org_uuid, workspace_uuid, project_name, project_uid, run_id)
		)`,
		`CREATE INDEX IF NOT EXISTS app_studio_assistant_runs_scope_updated_idx
			ON app_studio_assistant_runs (org_uuid, workspace_uuid, project_name, project_uid, updated_at, run_id)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS app_studio_assistant_runs_scope_client_request_idx
			ON app_studio_assistant_runs (org_uuid, workspace_uuid, project_name, project_uid, client_request_id)
			WHERE client_request_id <> ''`,
		`CREATE TABLE IF NOT EXISTS app_studio_assistant_work_items (
			org_uuid text NOT NULL, workspace_uuid text NOT NULL, project_name text NOT NULL, project_uid text NOT NULL,
			work_item_id text NOT NULL, root_message_id text NOT NULL, created_by text NOT NULL,
			status text NOT NULL, status_reason text NOT NULL DEFAULT '', revision bigint NOT NULL,
			active_run_id text NOT NULL DEFAULT '', plan_grant jsonb NOT NULL DEFAULT '{}'::jsonb,
			grant_revision text NOT NULL DEFAULT '', execution_plan jsonb NOT NULL DEFAULT '{}'::jsonb,
			execution_plan_revision text NOT NULL DEFAULT '', cancellation_receipt jsonb NOT NULL DEFAULT '{}'::jsonb,
			created_at timestamptz NOT NULL, updated_at timestamptz NOT NULL,
			PRIMARY KEY (org_uuid, workspace_uuid, project_name, project_uid, work_item_id),
			UNIQUE (org_uuid, workspace_uuid, project_name, project_uid, root_message_id)
		)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS app_studio_work_items_active_idx ON app_studio_assistant_work_items (org_uuid, workspace_uuid, project_name, project_uid) WHERE status = 'active'`,
		`CREATE UNIQUE INDEX IF NOT EXISTS app_studio_runs_active_idx ON app_studio_assistant_runs (org_uuid, workspace_uuid, project_name, project_uid) WHERE status NOT IN ('completed','aborted','failed','interrupted')`,
	}
}

func schemaVersionMissing(ctx context.Context, tx *sql.Tx, version string) (bool, error) {
	var exists bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS (
		SELECT 1 FROM app_studio_message_schema_migrations WHERE version = $1
	)`, version).Scan(&exists); err != nil {
		return false, fmt.Errorf("check schema migration %s: %w", version, err)
	}
	return !exists, nil
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
	msg = prepareMessage(scope, msg)
	if msg.Metadata == nil {
		msg.Metadata = map[string]any{}
	}
	metadata, err := json.Marshal(msg.Metadata)
	if err != nil {
		return fmt.Errorf("marshal message metadata: %w", err)
	}

	result, err := s.db.ExecContext(ctx, `
		INSERT INTO app_studio_messages (
			org_uuid, workspace_uuid, project_name, project_uid, message_id,
			actor_id, work_item_id, role, content, content_encrypted, content_key_id,
			metadata, created_at, updated_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)
		ON CONFLICT (org_uuid, workspace_uuid, project_name, project_uid, message_id)
		DO UPDATE SET
			actor_id = EXCLUDED.actor_id,
			work_item_id = EXCLUDED.work_item_id,
			role = EXCLUDED.role,
			content = EXCLUDED.content,
			content_encrypted = EXCLUDED.content_encrypted,
			content_key_id = EXCLUDED.content_key_id,
			metadata = EXCLUDED.metadata,
			created_at = EXCLUDED.created_at,
			updated_at = EXCLUDED.updated_at
		WHERE app_studio_messages.actor_id = EXCLUDED.actor_id
			AND app_studio_messages.work_item_id = EXCLUDED.work_item_id
	`,
		scope.OrgUUID, scope.WorkspaceUUID, scope.ProjectName, scope.ProjectUID, msg.ID,
		msg.ActorID, msg.WorkItemID, msg.Role, msg.Content, msg.ContentEncrypted, msg.ContentKeyID,
		metadata, msg.CreatedAt.UTC(), msg.UpdatedAt.UTC(),
	)
	if err != nil {
		return fmt.Errorf("upsert message: %w", err)
	}
	if n, err := result.RowsAffected(); err != nil {
		return fmt.Errorf("count upsert message: %w", err)
	} else if n != 1 {
		return fmt.Errorf("%w: message %q actor and work item are immutable", ErrAssistantWorkItemConflict, msg.ID)
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
		SELECT message_id, actor_id, work_item_id, role, content, content_encrypted, content_key_id,
		       metadata, created_at, updated_at
		FROM app_studio_messages
		WHERE org_uuid = $1 AND workspace_uuid = $2 AND project_name = $3 AND project_uid = $4`
	args := []any{scope.OrgUUID, scope.WorkspaceUUID, scope.ProjectName, scope.ProjectUID}
	if !cutoffAt.IsZero() {
		query += ` AND (created_at, message_id) > ($5, $6)`
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
		msg, err := scanMessage(rows, scope)
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
	return s.loadRecentMessages(ctx, scope, limit, false)
}

func (s *PostgresStore) LoadRecentDiscussionMessages(ctx context.Context, scope Scope, limit int) (items []Message, err error) {
	return s.loadRecentMessages(ctx, scope, limit, true)
}

func (s *PostgresStore) loadRecentMessages(ctx context.Context, scope Scope, limit int, discussionOnly bool) (items []Message, err error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("postgres store is nil")
	}
	if err := scope.validate(); err != nil {
		return nil, err
	}
	limit = normalizeLimit(limit)
	query := `
		SELECT message_id, actor_id, work_item_id, role, content, content_encrypted, content_key_id,
		       metadata, created_at, updated_at
		FROM app_studio_messages
		WHERE org_uuid = $1 AND workspace_uuid = $2 AND project_name = $3 AND project_uid = $4
	`
	if discussionOnly {
		query += ` AND work_item_id = ''`
	}
	query += `
		ORDER BY created_at DESC, message_id DESC
		LIMIT $5
	`
	rows, err := s.db.QueryContext(ctx, query, scope.OrgUUID, scope.WorkspaceUUID, scope.ProjectName, scope.ProjectUID, limit)
	if err != nil {
		return nil, fmt.Errorf("load recent messages: %w", err)
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil && err == nil {
			err = fmt.Errorf("close recent messages rows: %w", closeErr)
		}
	}()

	for rows.Next() {
		msg, err := scanMessage(rows, scope)
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

func (s *PostgresStore) GetAssistantApprovalPreference(ctx context.Context, scope Scope, actor string) (AssistantApprovalPreference, error) {
	if s == nil || s.db == nil {
		return AssistantApprovalPreference{}, fmt.Errorf("postgres store is nil")
	}
	if err := scope.validate(); err != nil {
		return AssistantApprovalPreference{}, err
	}
	actor = strings.TrimSpace(actor)
	if actor == "" {
		return AssistantApprovalPreference{}, fmt.Errorf("assistant approval preference actor is required")
	}
	var preference AssistantApprovalPreference
	var mode string
	err := s.db.QueryRowContext(ctx, `SELECT actor_id, approval_mode, updated_at
		FROM app_studio_assistant_approval_preferences
		WHERE org_uuid=$1 AND workspace_uuid=$2 AND project_name=$3 AND project_uid=$4 AND actor_id=$5`,
		scope.OrgUUID, scope.WorkspaceUUID, scope.ProjectName, scope.ProjectUID, actor,
	).Scan(&preference.ActorID, &mode, &preference.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return AssistantApprovalPreference{ActorID: actor, Mode: AssistantApprovalModeAutoApprove}, nil
	}
	if err != nil {
		return AssistantApprovalPreference{}, fmt.Errorf("get assistant approval preference: %w", err)
	}
	preference.Mode, err = NormalizeAssistantApprovalMode(AssistantApprovalMode(mode))
	if err != nil {
		return AssistantApprovalPreference{}, err
	}
	preference.UpdatedAt = preference.UpdatedAt.UTC()
	return preference, nil
}

func (s *PostgresStore) SetAssistantApprovalPreference(ctx context.Context, scope Scope, preference AssistantApprovalPreference) (AssistantApprovalPreference, error) {
	if s == nil || s.db == nil {
		return AssistantApprovalPreference{}, fmt.Errorf("postgres store is nil")
	}
	if err := scope.validate(); err != nil {
		return AssistantApprovalPreference{}, err
	}
	preference.ActorID = strings.TrimSpace(preference.ActorID)
	if preference.ActorID == "" {
		return AssistantApprovalPreference{}, fmt.Errorf("assistant approval preference actor is required")
	}
	mode, err := NormalizeAssistantApprovalMode(preference.Mode)
	if err != nil {
		return AssistantApprovalPreference{}, err
	}
	preference.Mode = mode
	if preference.UpdatedAt.IsZero() {
		preference.UpdatedAt = time.Now().UTC()
	} else {
		preference.UpdatedAt = preference.UpdatedAt.UTC()
	}
	err = s.db.QueryRowContext(ctx, `INSERT INTO app_studio_assistant_approval_preferences (
			org_uuid, workspace_uuid, project_name, project_uid, actor_id, approval_mode, updated_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7)
		ON CONFLICT (org_uuid, workspace_uuid, project_name, project_uid, actor_id)
		DO UPDATE SET approval_mode=EXCLUDED.approval_mode, updated_at=EXCLUDED.updated_at
		RETURNING actor_id, approval_mode, updated_at`,
		scope.OrgUUID, scope.WorkspaceUUID, scope.ProjectName, scope.ProjectUID,
		preference.ActorID, preference.Mode, preference.UpdatedAt,
	).Scan(&preference.ActorID, &preference.Mode, &preference.UpdatedAt)
	if err != nil {
		return AssistantApprovalPreference{}, fmt.Errorf("set assistant approval preference: %w", err)
	}
	preference.UpdatedAt = preference.UpdatedAt.UTC()
	return preference, nil
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
	approvalMode, err := NormalizeAssistantApprovalMode(run.ApprovalMode)
	if err != nil {
		return err
	}
	run.ApprovalMode = approvalMode
	if run.CreatedAt.IsZero() {
		run.CreatedAt = time.Now().UTC()
	}
	if run.UpdatedAt.IsZero() {
		run.UpdatedAt = run.CreatedAt
	}
	run = prepareAssistantRun(scope, run)
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

	result, err := s.db.ExecContext(ctx, `
		INSERT INTO app_studio_assistant_runs (
			org_uuid, workspace_uuid, project_name, project_uid, run_id, work_item_id, mode, approval_mode, expected_grant_revision,
			status, client_request_id, user_message_id, active_message_id, revision, request_id,
			checkpoint, audit, created_at, updated_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19)
		ON CONFLICT (org_uuid, workspace_uuid, project_name, project_uid, run_id)
		DO UPDATE SET
			status = EXCLUDED.status,
			active_message_id = EXCLUDED.active_message_id,
			request_id = EXCLUDED.request_id,
			checkpoint = EXCLUDED.checkpoint,
			audit = EXCLUDED.audit,
			updated_at = EXCLUDED.updated_at
			WHERE app_studio_assistant_runs.work_item_id = EXCLUDED.work_item_id
				AND app_studio_assistant_runs.mode = EXCLUDED.mode
				AND app_studio_assistant_runs.approval_mode = EXCLUDED.approval_mode
		`,
		scope.OrgUUID, scope.WorkspaceUUID, scope.ProjectName, scope.ProjectUID, run.ID, run.WorkItemID, run.Mode, run.ApprovalMode, run.ExpectedGrantRevision,
		run.Status, run.ClientRequestID, run.UserMessageID, run.ActiveMessageID, run.Revision, run.RequestID,
		string(normalizedCheckpoint), string(normalizedAudit), run.CreatedAt.UTC(), run.UpdatedAt.UTC(),
	)
	if err != nil {
		if isAssistantRunUniqueViolation(err) {
			return fmt.Errorf("%w: project already has active assistant run", ErrAssistantRunConflict)
		}
		return fmt.Errorf("upsert assistant run: %w", err)
	}
	if n, err := result.RowsAffected(); err != nil {
		return fmt.Errorf("count upsert assistant run: %w", err)
	} else if n != 1 {
		return fmt.Errorf("%w: assistant run %q work item and mode are immutable", ErrAssistantRunConflict, run.ID)
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
			org_uuid, workspace_uuid, project_name, project_uid, run_id, work_item_id, mode, approval_mode, expected_grant_revision, status,
			client_request_id, user_message_id, active_message_id, revision, request_id,
			checkpoint, audit, created_at, updated_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19)
		ON CONFLICT DO NOTHING
		RETURNING run_id, work_item_id, mode, approval_mode, expected_grant_revision, status, client_request_id, user_message_id, active_message_id, revision,
		          request_id, checkpoint, audit, created_at, updated_at
	`, scope.OrgUUID, scope.WorkspaceUUID, scope.ProjectName, scope.ProjectUID, run.ID, run.WorkItemID, run.Mode, run.ApprovalMode, run.ExpectedGrantRevision, run.Status,
		run.ClientRequestID, run.UserMessageID, run.ActiveMessageID, run.Revision, run.RequestID,
		string(checkpoint), string(audit), run.CreatedAt.UTC(), run.UpdatedAt.UTC())
	inserted, err := scanAssistantRun(row, scope)
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
		SET status = $6,
			active_message_id = $7,
			revision = $8,
			request_id = $9,
			checkpoint = $10,
			audit = $11,
			updated_at = $12
		WHERE org_uuid = $1 AND workspace_uuid = $2 AND project_name = $3 AND project_uid = $4 AND run_id = $5
			  AND revision = $13
			  AND work_item_id = $14
			  AND mode = $15
			  AND approval_mode = $16
		`, scope.OrgUUID, scope.WorkspaceUUID, scope.ProjectName, scope.ProjectUID, run.ID,
		run.Status, run.ActiveMessageID, run.Revision, run.RequestID,
		string(checkpoint), string(audit), run.UpdatedAt.UTC(), expectedRevision, run.WorkItemID, run.Mode, run.ApprovalMode)
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

func normalizePostgresJSONB(raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var parsed any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&parsed); err != nil {
		return nil, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("JSON contains multiple values")
		}
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
		  AND project_uid = $6
		  AND run_id = $7
		  AND request_id = $8
		  AND status IN ($9, $10)
			RETURNING run_id, work_item_id, mode, approval_mode, expected_grant_revision, status, client_request_id, user_message_id, active_message_id, revision,
		          request_id, checkpoint, audit, created_at, updated_at
	`,
		AssistantRunStatusRunning, now.UTC(),
		scope.OrgUUID, scope.WorkspaceUUID, scope.ProjectName, scope.ProjectUID, id, requestID,
		AssistantRunStatusPendingPermission, AssistantRunStatusPendingInput,
	)

	run, err := scanAssistantRun(row, scope)
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
			SELECT run_id, work_item_id, mode, approval_mode, expected_grant_revision, status, client_request_id, user_message_id, active_message_id, revision,
		       request_id, checkpoint, audit, created_at, updated_at
		FROM app_studio_assistant_runs
		WHERE org_uuid = $1 AND workspace_uuid = $2 AND project_name = $3 AND project_uid = $4 AND run_id = $5
	`, scope.OrgUUID, scope.WorkspaceUUID, scope.ProjectName, scope.ProjectUID, id)

	run, err := scanAssistantRun(row, scope)
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
			SELECT run_id, work_item_id, mode, approval_mode, expected_grant_revision, status, client_request_id, user_message_id, active_message_id, revision,
		       request_id, checkpoint, audit, created_at, updated_at
		FROM app_studio_assistant_runs
		WHERE org_uuid = $1 AND workspace_uuid = $2 AND project_name = $3 AND project_uid = $4
		ORDER BY updated_at DESC, run_id DESC
		LIMIT 1
	`, scope.OrgUUID, scope.WorkspaceUUID, scope.ProjectName, scope.ProjectUID)
	run, err := scanAssistantRun(row, scope)
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
		DELETE FROM app_studio_assistant_approval_preferences
		WHERE org_uuid = $1 AND workspace_uuid = $2 AND project_name = $3 AND project_uid = $4
	`, scope.OrgUUID, scope.WorkspaceUUID, scope.ProjectName, scope.ProjectUID); err != nil {
		return fmt.Errorf("delete project assistant approval preferences: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `
		DELETE FROM app_studio_project_bootstrap_permits
		WHERE org_uuid = $1 AND workspace_uuid = $2 AND project_name = $3 AND project_uid = $4
	`, scope.OrgUUID, scope.WorkspaceUUID, scope.ProjectName, scope.ProjectUID); err != nil {
		return fmt.Errorf("delete project bootstrap permit: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `
		DELETE FROM app_studio_messages
		WHERE org_uuid = $1 AND workspace_uuid = $2 AND project_name = $3 AND project_uid = $4
	`, scope.OrgUUID, scope.WorkspaceUUID, scope.ProjectName, scope.ProjectUID); err != nil {
		return fmt.Errorf("delete project messages: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `
		DELETE FROM app_studio_assistant_runs
		WHERE org_uuid = $1 AND workspace_uuid = $2 AND project_name = $3 AND project_uid = $4
	`, scope.OrgUUID, scope.WorkspaceUUID, scope.ProjectName, scope.ProjectUID); err != nil {
		return fmt.Errorf("delete project assistant runs: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `
		DELETE FROM app_studio_assistant_work_items
		WHERE org_uuid = $1 AND workspace_uuid = $2 AND project_name = $3 AND project_uid = $4
	`, scope.OrgUUID, scope.WorkspaceUUID, scope.ProjectName, scope.ProjectUID); err != nil {
		return fmt.Errorf("delete project assistant work items: %w", err)
	}
	return nil
}

func (s *PostgresStore) DeleteMessagesOlderThan(ctx context.Context, before time.Time) (int64, error) {
	if s == nil || s.db == nil {
		return 0, fmt.Errorf("postgres store is nil")
	}
	res, err := s.db.ExecContext(ctx, `DELETE FROM app_studio_messages WHERE work_item_id = '' AND created_at < $1`, before.UTC())
	if err != nil {
		return 0, fmt.Errorf("delete stale messages: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("count deleted messages: %w", err)
	}
	runRes, err := s.db.ExecContext(ctx, `DELETE FROM app_studio_assistant_runs
		WHERE work_item_id = '' AND status IN ('completed', 'aborted', 'failed', 'interrupted') AND updated_at < $1`, before.UTC())
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
}, scope Scope) (Message, error) {
	var (
		msg       Message
		metadata  []byte
		createdAt time.Time
		updatedAt time.Time
	)
	if err := row.Scan(
		&msg.ID,
		&msg.ActorID,
		&msg.WorkItemID,
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
	msg.ProjectName = scope.ProjectName
	msg.ProjectUID = scope.ProjectUID
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
	result, err := tx.ExecContext(ctx, `
		INSERT INTO app_studio_messages (
			org_uuid, workspace_uuid, project_name, project_uid, message_id,
			actor_id, work_item_id, role, content, content_encrypted, content_key_id,
			metadata, created_at, updated_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)
		ON CONFLICT (org_uuid, workspace_uuid, project_name, project_uid, message_id)
		DO UPDATE SET
			actor_id = EXCLUDED.actor_id,
			work_item_id = EXCLUDED.work_item_id,
			role = EXCLUDED.role,
			content = EXCLUDED.content,
			content_encrypted = EXCLUDED.content_encrypted,
			content_key_id = EXCLUDED.content_key_id,
			metadata = EXCLUDED.metadata,
			created_at = EXCLUDED.created_at,
			updated_at = EXCLUDED.updated_at
		WHERE app_studio_messages.actor_id = EXCLUDED.actor_id
			AND app_studio_messages.work_item_id = EXCLUDED.work_item_id
	`, scope.OrgUUID, scope.WorkspaceUUID, scope.ProjectName, scope.ProjectUID, msg.ID,
		msg.ActorID, msg.WorkItemID, msg.Role, msg.Content, msg.ContentEncrypted, msg.ContentKeyID,
		metadata, msg.CreatedAt.UTC(), msg.UpdatedAt.UTC())
	if err != nil {
		return fmt.Errorf("upsert snapshot message: %w", err)
	}
	if n, err := result.RowsAffected(); err != nil {
		return fmt.Errorf("count upsert snapshot message: %w", err)
	} else if n != 1 {
		return fmt.Errorf("%w: message %q actor and work item are immutable", ErrAssistantWorkItemConflict, msg.ID)
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
			SELECT run_id, work_item_id, mode, approval_mode, expected_grant_revision, status, client_request_id, user_message_id, active_message_id, revision,
		       request_id, checkpoint, audit, created_at, updated_at
		FROM app_studio_assistant_runs
		WHERE org_uuid = $1 AND workspace_uuid = $2 AND project_name = $3 AND project_uid = $4 AND client_request_id = $5
	`, scope.OrgUUID, scope.WorkspaceUUID, scope.ProjectName, scope.ProjectUID, clientRequestID)
	run, err := scanAssistantRun(row, scope)
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
}, scope Scope) (AssistantRun, error) {
	var run AssistantRun
	var status string
	if err := row.Scan(
		&run.ID,
		&run.WorkItemID,
		&run.Mode,
		&run.ApprovalMode,
		&run.ExpectedGrantRevision,
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
	run.ProjectName = scope.ProjectName
	run.ProjectUID = scope.ProjectUID
	run.Status = AssistantRunStatus(status)
	run.ApprovalMode, _ = NormalizeAssistantApprovalMode(run.ApprovalMode)
	run.Checkpoint = cloneRawMessage(run.Checkpoint)
	run.Audit = cloneRawMessage(run.Audit)
	run.CreatedAt = run.CreatedAt.UTC()
	run.UpdatedAt = run.UpdatedAt.UTC()
	return run, nil
}

var _ Store = (*PostgresStore)(nil)
