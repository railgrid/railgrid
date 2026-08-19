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

const assistantV2CutoverSchemaVersion = "assistant-v2-destructive-cutover-v1"
const bootstrapPermitSchemaVersion = "project-bootstrap-permit-v1"
const assistantConversationSchemaVersion = "assistant-conversation-items-v1"
const assistantConversationSequenceSchemaVersion = "assistant-conversation-sequence-v1"
const assistantThreadSchemaVersion = "assistant-thread-turn-item-v1"
const assistantApprovalPolicySchemaVersion = "assistant-approval-policy-v2"
const projectThumbnailSchemaVersion = "project-thumbnail-v1"
const projectThumbnailEncryptionSchemaVersion = "project-thumbnail-encryption-v2"
const projectThumbnailOrderSchemaVersion = "project-thumbnail-order-v3"
const projectThumbnailVariantSchemaVersion = "project-thumbnail-variant-v4"

const lockMessageSchemaMigrations = `SELECT pg_advisory_xact_lock(870408091945886937)`

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
	// Serialize every migration transaction before even creating the marker
	// table. This also makes the destructive V2 cutover safe when multiple
	// provider replicas start against the same database at once.
	if _, err = tx.ExecContext(ctx, lockMessageSchemaMigrations); err != nil {
		return fmt.Errorf("lock schema migrations: %w", err)
	}
	if _, err = tx.ExecContext(ctx, createMessageSchemaMigrationsTable); err != nil {
		return fmt.Errorf("create schema migrations table: %w", err)
	}
	if err := ensureSchemaVersion(ctx, tx, assistantV2CutoverSchemaVersion, assistantV2CutoverSchemaStatements()...); err != nil {
		return err
	}
	if err := ensureSchemaVersion(ctx, tx, bootstrapPermitSchemaVersion, bootstrapPermitSchemaStatements()...); err != nil {
		return err
	}
	if err := ensureSchemaVersion(ctx, tx, assistantConversationSchemaVersion, assistantConversationSchemaStatements()...); err != nil {
		return err
	}
	if err := ensureSchemaVersion(ctx, tx, assistantConversationSequenceSchemaVersion, assistantConversationSequenceSchemaStatements()...); err != nil {
		return err
	}
	if err := ensureSchemaVersion(ctx, tx, assistantThreadSchemaVersion, assistantThreadSchemaStatements()...); err != nil {
		return err
	}
	if err := ensureSchemaVersion(ctx, tx, assistantApprovalPolicySchemaVersion, assistantApprovalPolicySchemaStatements()...); err != nil {
		return err
	}
	if err := ensureSchemaVersion(ctx, tx, projectThumbnailSchemaVersion, projectThumbnailSchemaStatements()...); err != nil {
		return err
	}
	if err := ensureSchemaVersion(ctx, tx, projectThumbnailEncryptionSchemaVersion, projectThumbnailEncryptionSchemaStatements()...); err != nil {
		return err
	}
	if err := ensureSchemaVersion(ctx, tx, projectThumbnailOrderSchemaVersion, projectThumbnailOrderSchemaStatements()...); err != nil {
		return err
	}
	if err := ensureSchemaVersion(ctx, tx, projectThumbnailVariantSchemaVersion, projectThumbnailVariantSchemaStatements()...); err != nil {
		return err
	}
	if err := ensureSchemaVersion(ctx, tx, replicaClaimSchemaVersion, replicaClaimSchemaStatements()...); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("commit schema migration: %w", err)
	}
	return nil
}

func projectThumbnailSchemaStatements() []string {
	return []string{`CREATE TABLE IF NOT EXISTS app_studio_project_thumbnails (
		org_uuid text NOT NULL, workspace_uuid text NOT NULL, project_name text NOT NULL, project_uid text NOT NULL,
		commit_sha text NOT NULL, commit_created_at timestamptz NOT NULL, commit_order text NOT NULL DEFAULT '',
		variant text NOT NULL DEFAULT '',
		content_type text NOT NULL CHECK (content_type IN ('image/png','image/jpeg')),
		sha256 text NOT NULL, image_bytes bytea NOT NULL, image_encrypted boolean NOT NULL DEFAULT false,
		image_key_id text NOT NULL DEFAULT '', captured_at timestamptz NOT NULL,
		PRIMARY KEY (org_uuid, workspace_uuid, project_name, project_uid)
	)`}
}

func projectThumbnailOrderSchemaStatements() []string {
	return []string{
		`ALTER TABLE app_studio_project_thumbnails ADD COLUMN IF NOT EXISTS commit_order text NOT NULL DEFAULT ''`,
	}
}

func projectThumbnailVariantSchemaStatements() []string {
	return []string{
		`ALTER TABLE app_studio_project_thumbnails ADD COLUMN IF NOT EXISTS variant text NOT NULL DEFAULT ''`,
	}
}

// projectThumbnailEncryptionSchemaStatements repairs databases that applied
// project-thumbnail-v1 while that migration still had the original, smaller
// table shape. Migrations are immutable once released: keep v1 suitable for
// fresh installs, but always advance existing databases through this additive
// and idempotent forward migration.
func projectThumbnailEncryptionSchemaStatements() []string {
	return []string{
		`ALTER TABLE app_studio_project_thumbnails ADD COLUMN IF NOT EXISTS commit_created_at timestamptz`,
		`UPDATE app_studio_project_thumbnails SET commit_created_at = captured_at WHERE commit_created_at IS NULL`,
		`ALTER TABLE app_studio_project_thumbnails ALTER COLUMN commit_created_at SET NOT NULL`,
		`ALTER TABLE app_studio_project_thumbnails ADD COLUMN IF NOT EXISTS image_encrypted boolean NOT NULL DEFAULT false`,
		`ALTER TABLE app_studio_project_thumbnails ADD COLUMN IF NOT EXISTS image_key_id text NOT NULL DEFAULT ''`,
	}
}

func assistantApprovalPolicySchemaStatements() []string {
	return []string{
		`CREATE TABLE IF NOT EXISTS app_studio_assistant_approval_preferences (
			org_uuid text NOT NULL, workspace_uuid text NOT NULL, project_name text NOT NULL, project_uid text NOT NULL,
			actor_id text NOT NULL, approval_mode text NOT NULL CHECK (approval_mode IN ('on_request','always_ask','never')),
			updated_at timestamptz NOT NULL,
			PRIMARY KEY (org_uuid, workspace_uuid, project_name, project_uid, actor_id)
		)`,
		`DO $$
		DECLARE constraint_name text;
		BEGIN
			FOR constraint_name IN
				SELECT conname FROM pg_constraint
				WHERE conrelid = 'app_studio_assistant_approval_preferences'::regclass
					AND contype = 'c' AND pg_get_constraintdef(oid) LIKE '%approval_mode%'
			LOOP
				EXECUTE format('ALTER TABLE app_studio_assistant_approval_preferences DROP CONSTRAINT %I', constraint_name);
			END LOOP;
			-- The preference table previously stored auto_approve. Preserve every
			-- row, but make the retired value explicit in the new policy model.
			UPDATE app_studio_assistant_approval_preferences
			SET approval_mode = 'never'
			WHERE approval_mode = 'auto_approve';
			ALTER TABLE app_studio_assistant_approval_preferences
				ADD CONSTRAINT app_studio_assistant_approval_preferences_approval_mode_check
				CHECK (approval_mode IN ('on_request','always_ask','never'));
		END $$`,
		`DO $$
		DECLARE constraint_name text;
		BEGIN
			FOR constraint_name IN
				SELECT conname FROM pg_constraint
				WHERE conrelid = 'app_studio_assistant_runs'::regclass AND contype = 'c' AND pg_get_constraintdef(oid) LIKE '%approval_mode%'
			LOOP
				EXECUTE format('ALTER TABLE app_studio_assistant_runs DROP CONSTRAINT %I', constraint_name);
			END LOOP;
			ALTER TABLE app_studio_assistant_runs ADD CONSTRAINT app_studio_assistant_runs_approval_mode_check
				CHECK (approval_mode IN ('on_request','always_ask','never','auto_approve'));
		END $$`,
	}
}

func assistantThreadSchemaStatements() []string {
	return []string{
		`CREATE TABLE IF NOT EXISTS app_studio_assistant_threads (
			org_uuid text NOT NULL, workspace_uuid text NOT NULL, project_name text NOT NULL, project_uid text NOT NULL,
			thread_id text NOT NULL, title text NOT NULL DEFAULT '', status text NOT NULL CHECK (status IN ('idle','active','archived')),
			actor_id text NOT NULL, created_at timestamptz NOT NULL, updated_at timestamptz NOT NULL,
			PRIMARY KEY (org_uuid, workspace_uuid, project_name, project_uid, thread_id)
		)`,
		`CREATE INDEX IF NOT EXISTS app_studio_assistant_threads_scope_updated_idx
			ON app_studio_assistant_threads (org_uuid, workspace_uuid, project_name, project_uid, updated_at DESC, thread_id DESC)`,
		`CREATE TABLE IF NOT EXISTS app_studio_assistant_turns (
			org_uuid text NOT NULL, workspace_uuid text NOT NULL, project_name text NOT NULL, project_uid text NOT NULL,
			thread_id text NOT NULL, turn_id text NOT NULL, actor_id text NOT NULL, client_user_message_id text NOT NULL,
			mode text NOT NULL CHECK (mode IN ('default','plan','review')),
			approval_mode text NOT NULL CHECK (approval_mode IN ('on_request','always_ask','never')),
			status text NOT NULL CHECK (status IN ('in_progress','completed','interrupted','failed')),
			checkpoint jsonb NOT NULL DEFAULT '{}'::jsonb, terminal_error jsonb NOT NULL DEFAULT '{}'::jsonb,
			created_at timestamptz NOT NULL, updated_at timestamptz NOT NULL,
			PRIMARY KEY (org_uuid, workspace_uuid, project_name, project_uid, thread_id, turn_id),
			UNIQUE (org_uuid, workspace_uuid, project_name, project_uid, thread_id, client_user_message_id),
			FOREIGN KEY (org_uuid, workspace_uuid, project_name, project_uid, thread_id)
				REFERENCES app_studio_assistant_threads (org_uuid, workspace_uuid, project_name, project_uid, thread_id) ON DELETE CASCADE
		)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS app_studio_assistant_turns_active_idx
			ON app_studio_assistant_turns (org_uuid, workspace_uuid, project_name, project_uid, thread_id)
			WHERE status = 'in_progress'`,
		`CREATE TABLE IF NOT EXISTS app_studio_assistant_thread_events (
			org_uuid text NOT NULL, workspace_uuid text NOT NULL, project_name text NOT NULL, project_uid text NOT NULL,
			thread_id text NOT NULL, turn_id text NOT NULL DEFAULT '', sequence bigint NOT NULL CHECK (sequence > 0),
			event_type text NOT NULL, item_id text NOT NULL DEFAULT '', request_id text NOT NULL DEFAULT '',
			payload jsonb NOT NULL DEFAULT '{}'::jsonb, created_at timestamptz NOT NULL,
			PRIMARY KEY (org_uuid, workspace_uuid, project_name, project_uid, thread_id, sequence),
			FOREIGN KEY (org_uuid, workspace_uuid, project_name, project_uid, thread_id)
				REFERENCES app_studio_assistant_threads (org_uuid, workspace_uuid, project_name, project_uid, thread_id) ON DELETE CASCADE
		)`,
	}
}

// assistantV2CutoverSchemaStatements intentionally discards pre-V2 assistant
// conversations and runs. App Studio is not preserving the retired WorkItem,
// engine-version, or semantic-mode storage contract.
func assistantV2CutoverSchemaStatements() []string {
	statements := []string{
		`DROP TABLE IF EXISTS app_studio_assistant_run_events`,
		`DROP TABLE IF EXISTS app_studio_assistant_work_items`,
		`DROP TABLE IF EXISTS app_studio_assistant_runs`,
		`DROP TABLE IF EXISTS app_studio_messages`,
	}
	statements = append(statements, assistantSchemaStatements()...)
	statements = append(statements, assistantRunEventSchemaStatements()...)
	return statements
}

func assistantSchemaStatements() []string {
	return []string{
		`CREATE TABLE IF NOT EXISTS app_studio_messages (
			org_uuid text NOT NULL, workspace_uuid text NOT NULL, project_name text NOT NULL, project_uid text NOT NULL,
			message_id text NOT NULL, role text NOT NULL, actor_id text NOT NULL DEFAULT '', content text NOT NULL,
			content_encrypted boolean NOT NULL DEFAULT false, content_key_id text NOT NULL DEFAULT '',
			metadata jsonb NOT NULL DEFAULT '{}'::jsonb, created_at timestamptz NOT NULL, updated_at timestamptz NOT NULL,
			PRIMARY KEY (org_uuid, workspace_uuid, project_name, project_uid, message_id)
		)`,
		`CREATE INDEX IF NOT EXISTS app_studio_messages_scope_created_idx
			ON app_studio_messages (org_uuid, workspace_uuid, project_name, project_uid, created_at, message_id)`,
		`CREATE INDEX IF NOT EXISTS app_studio_messages_created_idx ON app_studio_messages (created_at)`,
		`CREATE TABLE IF NOT EXISTS app_studio_assistant_runs (
			org_uuid text NOT NULL, workspace_uuid text NOT NULL, project_name text NOT NULL, project_uid text NOT NULL,
			run_id text NOT NULL, mode text NOT NULL CHECK (mode IN ('default', 'plan', 'review')),
			approval_mode text NOT NULL DEFAULT 'auto_approve' CHECK (approval_mode IN ('always_ask', 'auto_approve')),
			status text NOT NULL, client_request_id text NOT NULL DEFAULT '', user_message_id text NOT NULL DEFAULT '',
			active_message_id text NOT NULL DEFAULT '', revision bigint NOT NULL DEFAULT 0, request_id text NOT NULL DEFAULT '',
			checkpoint jsonb NOT NULL DEFAULT '{}'::jsonb, audit jsonb NOT NULL DEFAULT '{}'::jsonb,
			terminal_error jsonb NOT NULL DEFAULT '{}'::jsonb, abort_reason text NOT NULL DEFAULT '',
			created_at timestamptz NOT NULL, updated_at timestamptz NOT NULL,
			PRIMARY KEY (org_uuid, workspace_uuid, project_name, project_uid, run_id)
		)`,
		`CREATE INDEX IF NOT EXISTS app_studio_assistant_runs_scope_updated_idx
			ON app_studio_assistant_runs (org_uuid, workspace_uuid, project_name, project_uid, updated_at, run_id)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS app_studio_assistant_runs_scope_client_request_idx
			ON app_studio_assistant_runs (org_uuid, workspace_uuid, project_name, project_uid, client_request_id)
			WHERE client_request_id <> ''`,
		`CREATE UNIQUE INDEX IF NOT EXISTS app_studio_runs_active_idx
			ON app_studio_assistant_runs (org_uuid, workspace_uuid, project_name, project_uid)
			WHERE status NOT IN ('completed','failed','interrupted','aborted')`,
	}
}

func bootstrapPermitSchemaStatements() []string {
	return []string{`CREATE TABLE IF NOT EXISTS app_studio_project_bootstrap_permits (
		org_uuid text NOT NULL, workspace_uuid text NOT NULL, project_name text NOT NULL, project_uid text NOT NULL,
		actor_id text NOT NULL, prompt_digest text NOT NULL, consumed_client_request_id text NOT NULL DEFAULT '',
		created_at timestamptz NOT NULL, consumed_at timestamptz,
		PRIMARY KEY (org_uuid, workspace_uuid, project_name, project_uid)
	)`}
}

func assistantRunEventSchemaStatements() []string {
	return []string{`CREATE TABLE IF NOT EXISTS app_studio_assistant_run_events (
		org_uuid text NOT NULL, workspace_uuid text NOT NULL, project_name text NOT NULL, project_uid text NOT NULL,
		run_id text NOT NULL, sequence bigint NOT NULL CHECK (sequence > 0), event_type text NOT NULL,
		call_id text NOT NULL DEFAULT '', tool_name text NOT NULL DEFAULT '', args_digest text NOT NULL DEFAULT '',
		payload jsonb NOT NULL DEFAULT '{}'::jsonb, created_at timestamptz NOT NULL,
		PRIMARY KEY (org_uuid, workspace_uuid, project_name, project_uid, run_id, sequence),
		FOREIGN KEY (org_uuid, workspace_uuid, project_name, project_uid, run_id)
			REFERENCES app_studio_assistant_runs (org_uuid, workspace_uuid, project_name, project_uid, run_id)
			ON DELETE CASCADE
	)`}
}

func assistantConversationSchemaStatements() []string {
	return []string{`CREATE TABLE IF NOT EXISTS app_studio_assistant_conversation_items (
		org_uuid text NOT NULL, workspace_uuid text NOT NULL, project_name text NOT NULL, project_uid text NOT NULL,
		item_id text NOT NULL, run_id text NOT NULL, sequence bigint NOT NULL CHECK (sequence > 0), item_type text NOT NULL,
		payload jsonb NOT NULL DEFAULT '{}'::jsonb, created_at timestamptz NOT NULL,
		PRIMARY KEY (org_uuid, workspace_uuid, project_name, project_uid, run_id, item_id),
		UNIQUE (org_uuid, workspace_uuid, project_name, project_uid, sequence)
	)`, `CREATE INDEX IF NOT EXISTS app_studio_assistant_conversation_items_scope_idx
		ON app_studio_assistant_conversation_items (org_uuid, workspace_uuid, project_name, project_uid, sequence)`}
}

// assistantConversationSequenceSchemaStatements stores a project-scoped
// high-water mark separately from conversation rows.  Conversation retention
// may remove every row in a project, but sequence values remain monotonic
// until the project itself is deleted.
func assistantConversationSequenceSchemaStatements() []string {
	return []string{`CREATE TABLE IF NOT EXISTS app_studio_assistant_conversation_sequences (
		org_uuid text NOT NULL, workspace_uuid text NOT NULL, project_name text NOT NULL, project_uid text NOT NULL,
		last_sequence bigint NOT NULL DEFAULT 0 CHECK (last_sequence >= 0),
		PRIMARY KEY (org_uuid, workspace_uuid, project_name, project_uid)
	)`}
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
			actor_id, role, content, content_encrypted, content_key_id,
			metadata, created_at, updated_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)
		ON CONFLICT (org_uuid, workspace_uuid, project_name, project_uid, message_id)
		DO UPDATE SET
			actor_id = EXCLUDED.actor_id,
			role = EXCLUDED.role,
			content = EXCLUDED.content,
			content_encrypted = EXCLUDED.content_encrypted,
			content_key_id = EXCLUDED.content_key_id,
			metadata = EXCLUDED.metadata,
			created_at = EXCLUDED.created_at,
			updated_at = EXCLUDED.updated_at
		WHERE app_studio_messages.actor_id = EXCLUDED.actor_id
	`,
		scope.OrgUUID, scope.WorkspaceUUID, scope.ProjectName, scope.ProjectUID, msg.ID,
		msg.ActorID, msg.Role, msg.Content, msg.ContentEncrypted, msg.ContentKeyID,
		metadata, msg.CreatedAt.UTC(), msg.UpdatedAt.UTC(),
	)
	if err != nil {
		return fmt.Errorf("upsert message: %w", err)
	}
	if n, err := result.RowsAffected(); err != nil {
		return fmt.Errorf("count upsert message: %w", err)
	} else if n != 1 {
		return fmt.Errorf("message %q actor is immutable", msg.ID)
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
		SELECT message_id, actor_id, role, content, content_encrypted, content_key_id,
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
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("postgres store is nil")
	}
	if err := scope.validate(); err != nil {
		return nil, err
	}
	limit = normalizeLimit(limit)
	query := `
		SELECT message_id, actor_id, role, content, content_encrypted, content_key_id,
		       metadata, created_at, updated_at
		FROM app_studio_messages
		WHERE org_uuid = $1 AND workspace_uuid = $2 AND project_name = $3 AND project_uid = $4
	`
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
		return AssistantApprovalPreference{ActorID: actor, Mode: AssistantApprovalModeOnRequest}, nil
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
	terminalError, err := normalizeAssistantRunError(run.Error)
	if err != nil {
		return err
	}

	result, err := s.db.ExecContext(ctx, `
		INSERT INTO app_studio_assistant_runs (
			org_uuid, workspace_uuid, project_name, project_uid, run_id, mode, approval_mode,
			status, client_request_id, user_message_id, active_message_id, revision, request_id,
			checkpoint, audit, terminal_error, abort_reason, created_at, updated_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19)
		ON CONFLICT (org_uuid, workspace_uuid, project_name, project_uid, run_id)
		DO UPDATE SET
			status = EXCLUDED.status,
			active_message_id = EXCLUDED.active_message_id,
			request_id = EXCLUDED.request_id,
			checkpoint = EXCLUDED.checkpoint,
			audit = EXCLUDED.audit,
			terminal_error = EXCLUDED.terminal_error,
			abort_reason = EXCLUDED.abort_reason,
			updated_at = EXCLUDED.updated_at
			WHERE app_studio_assistant_runs.mode = EXCLUDED.mode
				AND app_studio_assistant_runs.approval_mode = EXCLUDED.approval_mode
		`,
		scope.OrgUUID, scope.WorkspaceUUID, scope.ProjectName, scope.ProjectUID, run.ID, run.Mode, run.ApprovalMode,
		run.Status, run.ClientRequestID, run.UserMessageID, run.ActiveMessageID, run.Revision, run.RequestID,
		string(normalizedCheckpoint), string(normalizedAudit), string(terminalError), run.AbortReason, run.CreatedAt.UTC(), run.UpdatedAt.UTC(),
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
		return fmt.Errorf("%w: assistant run %q contract is immutable", ErrAssistantRunConflict, run.ID)
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
	terminalError, err := normalizeAssistantRunError(run.Error)
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
			org_uuid, workspace_uuid, project_name, project_uid, run_id, mode, approval_mode, status,
			client_request_id, user_message_id, active_message_id, revision, request_id,
			checkpoint, audit, terminal_error, abort_reason, created_at, updated_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19)
		ON CONFLICT DO NOTHING
		RETURNING run_id, mode, approval_mode, status, client_request_id, user_message_id, active_message_id, revision,
		          request_id, checkpoint, audit, terminal_error, abort_reason, created_at, updated_at
	`, scope.OrgUUID, scope.WorkspaceUUID, scope.ProjectName, scope.ProjectUID, run.ID, run.Mode, run.ApprovalMode, run.Status,
		run.ClientRequestID, run.UserMessageID, run.ActiveMessageID, run.Revision, run.RequestID,
		string(checkpoint), string(audit), string(terminalError), run.AbortReason, run.CreatedAt.UTC(), run.UpdatedAt.UTC())
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
	terminalError, err := normalizeAssistantRunError(run.Error)
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
			terminal_error = $12,
			abort_reason = $13,
			updated_at = $14
		WHERE org_uuid = $1 AND workspace_uuid = $2 AND project_name = $3 AND project_uid = $4 AND run_id = $5
			  AND revision = $15
			  AND mode = $16
			  AND approval_mode = $17
		`, scope.OrgUUID, scope.WorkspaceUUID, scope.ProjectName, scope.ProjectUID, run.ID,
		run.Status, run.ActiveMessageID, run.Revision, run.RequestID,
		string(checkpoint), string(audit), string(terminalError), run.AbortReason, run.UpdatedAt.UTC(), expectedRevision, run.Mode, run.ApprovalMode)
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

func (s *PostgresStore) RequestAssistantRunStopWithAssistantMessage(ctx context.Context, scope Scope, runID string, expectedRunRevision int64, assistant Message, now time.Time) (AssistantRun, error) {
	return s.requestAssistantRunStop(ctx, scope, runID, expectedRunRevision, assistant, now)
}

func (s *PostgresStore) requestAssistantRunStop(ctx context.Context, scope Scope, runID string, expectedRunRevision int64, assistant Message, now time.Time) (AssistantRun, error) {
	if s == nil || s.db == nil {
		return AssistantRun{}, fmt.Errorf("postgres store is nil")
	}
	if err := scope.validate(); err != nil {
		return AssistantRun{}, err
	}
	if runID == "" || expectedRunRevision < 1 {
		return AssistantRun{}, fmt.Errorf("assistant run and revision are required")
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AssistantRun{}, fmt.Errorf("begin stop assistant run: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	row := tx.QueryRowContext(ctx, `UPDATE app_studio_assistant_runs
		SET status=$1, revision=revision+1, updated_at=$2
		WHERE org_uuid=$3 AND workspace_uuid=$4 AND project_name=$5 AND project_uid=$6
			AND run_id=$7 AND revision=$8 AND status=$9
		RETURNING run_id, mode, approval_mode, status, client_request_id, user_message_id, active_message_id, revision,
			request_id, checkpoint, audit, terminal_error, abort_reason, created_at, updated_at`,
		AssistantRunStatusStopping, now.UTC(), scope.OrgUUID, scope.WorkspaceUUID, scope.ProjectName, scope.ProjectUID,
		runID, expectedRunRevision, AssistantRunStatusRunning)
	run, err := scanAssistantRun(row, scope)
	if err == sql.ErrNoRows {
		return AssistantRun{}, fmt.Errorf("%w: assistant run %q", ErrAssistantRunConflict, runID)
	}
	if err != nil {
		return AssistantRun{}, fmt.Errorf("stop assistant run: %w", err)
	}
	if assistant.ID != "" {
		if assistant.Role != "assistant" || assistant.ID != run.ActiveMessageID {
			return AssistantRun{}, fmt.Errorf("assistant lifecycle message must be the active run message")
		}
		if err := appendMessageTx(ctx, tx, scope, prepareMessage(scope, assistant)); err != nil {
			return AssistantRun{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return AssistantRun{}, fmt.Errorf("commit stop assistant run: %w", err)
	}
	return run, nil
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
			RETURNING run_id, mode, approval_mode, status, client_request_id, user_message_id, active_message_id, revision,
		          request_id, checkpoint, audit, terminal_error, abort_reason, created_at, updated_at
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
			SELECT run_id, mode, approval_mode, status, client_request_id, user_message_id, active_message_id, revision,
		       request_id, checkpoint, audit, terminal_error, abort_reason, created_at, updated_at
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
			SELECT run_id, mode, approval_mode, status, client_request_id, user_message_id, active_message_id, revision,
		       request_id, checkpoint, audit, terminal_error, abort_reason, created_at, updated_at
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

func (s *PostgresStore) AppendAssistantRunEvent(ctx context.Context, scope Scope, event AssistantRunEvent, expectedSequence int64) (AssistantRunEvent, error) {
	if s == nil || s.db == nil {
		return AssistantRunEvent{}, fmt.Errorf("postgres store is nil")
	}
	event, err := prepareAssistantRunEvent(scope, event, expectedSequence)
	if err != nil {
		return AssistantRunEvent{}, err
	}
	payload, err := normalizePostgresJSONB(event.Payload)
	if err != nil {
		return AssistantRunEvent{}, fmt.Errorf("assistant run event payload is not valid json: %w", err)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AssistantRunEvent{}, fmt.Errorf("begin append assistant run event: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var persistedRunID string
	err = tx.QueryRowContext(ctx, `SELECT run_id FROM app_studio_assistant_runs
		WHERE org_uuid=$1 AND workspace_uuid=$2 AND project_name=$3 AND project_uid=$4 AND run_id=$5
		FOR UPDATE`, scope.OrgUUID, scope.WorkspaceUUID, scope.ProjectName, scope.ProjectUID, event.RunID).Scan(&persistedRunID)
	if errors.Is(err, sql.ErrNoRows) {
		return AssistantRunEvent{}, fmt.Errorf("%w: %q", ErrAssistantRunNotFound, event.RunID)
	}
	if err != nil {
		return AssistantRunEvent{}, fmt.Errorf("lock assistant run for event append: %w", err)
	}

	var currentSequence int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(sequence), 0) FROM app_studio_assistant_run_events
		WHERE org_uuid=$1 AND workspace_uuid=$2 AND project_name=$3 AND project_uid=$4 AND run_id=$5`,
		scope.OrgUUID, scope.WorkspaceUUID, scope.ProjectName, scope.ProjectUID, event.RunID).Scan(&currentSequence); err != nil {
		return AssistantRunEvent{}, fmt.Errorf("read assistant run event sequence: %w", err)
	}
	if currentSequence != expectedSequence {
		return AssistantRunEvent{}, fmt.Errorf("%w: assistant run %q is at sequence %d, expected %d", ErrAssistantRunEventConflict, event.RunID, currentSequence, expectedSequence)
	}

	_, err = tx.ExecContext(ctx, `INSERT INTO app_studio_assistant_run_events (
		org_uuid, workspace_uuid, project_name, project_uid, run_id, sequence, event_type,
		call_id, tool_name, args_digest, payload, created_at
	) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`,
		scope.OrgUUID, scope.WorkspaceUUID, scope.ProjectName, scope.ProjectUID, event.RunID, event.Sequence, event.Type,
		event.CallID, event.ToolName, event.ArgsDigest, string(payload), event.CreatedAt.UTC())
	if err != nil {
		if isAssistantRunUniqueViolation(err) {
			return AssistantRunEvent{}, fmt.Errorf("%w: assistant run %q sequence %d", ErrAssistantRunEventConflict, event.RunID, event.Sequence)
		}
		return AssistantRunEvent{}, fmt.Errorf("insert assistant run event: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return AssistantRunEvent{}, fmt.Errorf("commit assistant run event: %w", err)
	}
	event.Payload = cloneRawMessage(payload)
	return event, nil
}

func (s *PostgresStore) ListAssistantRunEvents(ctx context.Context, scope Scope, runID string, afterSequence int64, limit int) ([]AssistantRunEvent, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("postgres store is nil")
	}
	runID, err := validateAssistantRunEventList(scope, runID, afterSequence)
	if err != nil {
		return nil, err
	}
	var exists bool
	if err := s.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM app_studio_assistant_runs
		WHERE org_uuid=$1 AND workspace_uuid=$2 AND project_name=$3 AND project_uid=$4 AND run_id=$5)`,
		scope.OrgUUID, scope.WorkspaceUUID, scope.ProjectName, scope.ProjectUID, runID).Scan(&exists); err != nil {
		return nil, fmt.Errorf("check assistant run for event listing: %w", err)
	}
	if !exists {
		return nil, fmt.Errorf("%w: %q", ErrAssistantRunNotFound, runID)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT run_id, sequence, event_type, call_id, tool_name, args_digest, payload, created_at
		FROM app_studio_assistant_run_events
		WHERE org_uuid=$1 AND workspace_uuid=$2 AND project_name=$3 AND project_uid=$4 AND run_id=$5 AND sequence>$6
		ORDER BY sequence
		LIMIT $7`, scope.OrgUUID, scope.WorkspaceUUID, scope.ProjectName, scope.ProjectUID, runID, afterSequence, normalizeLimit(limit))
	if err != nil {
		return nil, fmt.Errorf("list assistant run events: %w", err)
	}
	defer rows.Close()
	events := make([]AssistantRunEvent, 0)
	for rows.Next() {
		event, err := scanAssistantRunEvent(rows, scope)
		if err != nil {
			return nil, fmt.Errorf("scan assistant run event: %w", err)
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list assistant run events: %w", err)
	}
	return events, nil
}

func (s *PostgresStore) DeleteProjectMessages(ctx context.Context, scope Scope) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("postgres store is nil")
	}
	if err := scope.validate(); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin delete project assistant data: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM app_studio_assistant_approval_preferences
		WHERE org_uuid = $1 AND workspace_uuid = $2 AND project_name = $3 AND project_uid = $4
	`, scope.OrgUUID, scope.WorkspaceUUID, scope.ProjectName, scope.ProjectUID); err != nil {
		return fmt.Errorf("delete project assistant approval preferences: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM app_studio_project_bootstrap_permits
		WHERE org_uuid = $1 AND workspace_uuid = $2 AND project_name = $3 AND project_uid = $4
	`, scope.OrgUUID, scope.WorkspaceUUID, scope.ProjectName, scope.ProjectUID); err != nil {
		return fmt.Errorf("delete project bootstrap permit: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM app_studio_assistant_run_events
		WHERE org_uuid = $1 AND workspace_uuid = $2 AND project_name = $3 AND project_uid = $4
	`, scope.OrgUUID, scope.WorkspaceUUID, scope.ProjectName, scope.ProjectUID); err != nil {
		return fmt.Errorf("delete project assistant run events: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM app_studio_assistant_conversation_items
		WHERE org_uuid = $1 AND workspace_uuid = $2 AND project_name = $3 AND project_uid = $4
	`, scope.OrgUUID, scope.WorkspaceUUID, scope.ProjectName, scope.ProjectUID); err != nil {
		return fmt.Errorf("delete project assistant conversation items: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM app_studio_assistant_conversation_sequences
		WHERE org_uuid = $1 AND workspace_uuid = $2 AND project_name = $3 AND project_uid = $4
	`, scope.OrgUUID, scope.WorkspaceUUID, scope.ProjectName, scope.ProjectUID); err != nil {
		return fmt.Errorf("delete project assistant conversation sequence: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM app_studio_assistant_thread_events
		WHERE org_uuid = $1 AND workspace_uuid = $2 AND project_name = $3 AND project_uid = $4
	`, scope.OrgUUID, scope.WorkspaceUUID, scope.ProjectName, scope.ProjectUID); err != nil {
		return fmt.Errorf("delete project assistant thread events: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM app_studio_assistant_turns
		WHERE org_uuid = $1 AND workspace_uuid = $2 AND project_name = $3 AND project_uid = $4
	`, scope.OrgUUID, scope.WorkspaceUUID, scope.ProjectName, scope.ProjectUID); err != nil {
		return fmt.Errorf("delete project assistant turns: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM app_studio_assistant_threads
		WHERE org_uuid = $1 AND workspace_uuid = $2 AND project_name = $3 AND project_uid = $4
	`, scope.OrgUUID, scope.WorkspaceUUID, scope.ProjectName, scope.ProjectUID); err != nil {
		return fmt.Errorf("delete project assistant threads: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM app_studio_messages
		WHERE org_uuid = $1 AND workspace_uuid = $2 AND project_name = $3 AND project_uid = $4
	`, scope.OrgUUID, scope.WorkspaceUUID, scope.ProjectName, scope.ProjectUID); err != nil {
		return fmt.Errorf("delete project messages: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM app_studio_assistant_runs
		WHERE org_uuid = $1 AND workspace_uuid = $2 AND project_name = $3 AND project_uid = $4
	`, scope.OrgUUID, scope.WorkspaceUUID, scope.ProjectName, scope.ProjectUID); err != nil {
		return fmt.Errorf("delete project assistant runs: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit delete project assistant data: %w", err)
	}
	return nil
}

func (s *PostgresStore) DeleteMessagesOlderThan(ctx context.Context, before time.Time) (int64, error) {
	if s == nil || s.db == nil {
		return 0, fmt.Errorf("postgres store is nil")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin delete stale assistant data: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.ExecContext(ctx, `DELETE FROM app_studio_messages AS message
		WHERE message.created_at < $1
		  AND NOT EXISTS (
			SELECT 1 FROM app_studio_assistant_runs AS active_run
			WHERE active_run.org_uuid = message.org_uuid
			  AND active_run.workspace_uuid = message.workspace_uuid
			  AND active_run.project_name = message.project_name
			  AND active_run.project_uid = message.project_uid
			  AND active_run.status NOT IN ('completed','failed','interrupted','aborted')
			  AND (active_run.user_message_id = message.message_id OR active_run.active_message_id = message.message_id)
		  )`, before.UTC())
	if err != nil {
		return 0, fmt.Errorf("delete stale messages: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("count deleted messages: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM app_studio_assistant_run_events AS event
		USING app_studio_assistant_runs AS run
		WHERE event.org_uuid=run.org_uuid AND event.workspace_uuid=run.workspace_uuid
			AND event.project_name=run.project_name AND event.project_uid=run.project_uid AND event.run_id=run.run_id
			AND run.status IN ('completed','failed','interrupted','aborted') AND run.updated_at < $1`, before.UTC()); err != nil {
		return 0, fmt.Errorf("delete stale assistant run events: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM app_studio_assistant_conversation_items AS item
		USING app_studio_assistant_runs AS run
		WHERE item.org_uuid=run.org_uuid AND item.workspace_uuid=run.workspace_uuid
			AND item.project_name=run.project_name AND item.project_uid=run.project_uid AND item.run_id=run.run_id
			AND run.status IN ('completed','failed','interrupted','aborted') AND run.updated_at < $1`, before.UTC()); err != nil {
		return 0, fmt.Errorf("delete stale assistant conversation items: %w", err)
	}
	runRes, err := tx.ExecContext(ctx, `DELETE FROM app_studio_assistant_runs
		WHERE status IN ('completed','failed','interrupted','aborted') AND updated_at < $1`, before.UTC())
	if err != nil {
		return 0, fmt.Errorf("delete stale assistant runs: %w", err)
	}
	runN, err := runRes.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("count deleted assistant runs: %w", err)
	}
	// A thread projection is one canonical transcript.  Remove old idle or
	// archived projections only when no in-progress turn remains; active turns
	// must survive retention so a refresh can still resume them.  The FK
	// cascade removes the associated turns and thread events atomically.
	if _, err := tx.ExecContext(ctx, `DELETE FROM app_studio_assistant_threads AS thread
		WHERE thread.updated_at < $1
		  AND thread.status <> 'active'
		  AND NOT EXISTS (
			SELECT 1 FROM app_studio_assistant_turns AS active_turn
			WHERE active_turn.org_uuid = thread.org_uuid
			  AND active_turn.workspace_uuid = thread.workspace_uuid
			  AND active_turn.project_name = thread.project_name
			  AND active_turn.project_uid = thread.project_uid
			  AND active_turn.thread_id = thread.thread_id
			  AND active_turn.status = 'in_progress'
		  )`, before.UTC()); err != nil {
		return 0, fmt.Errorf("delete stale assistant threads: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit delete stale assistant data: %w", err)
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
			actor_id, role, content, content_encrypted, content_key_id,
			metadata, created_at, updated_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)
		ON CONFLICT (org_uuid, workspace_uuid, project_name, project_uid, message_id)
		DO UPDATE SET
			actor_id = EXCLUDED.actor_id,
			role = EXCLUDED.role,
			content = EXCLUDED.content,
			content_encrypted = EXCLUDED.content_encrypted,
			content_key_id = EXCLUDED.content_key_id,
			metadata = EXCLUDED.metadata,
			created_at = EXCLUDED.created_at,
			updated_at = EXCLUDED.updated_at
		WHERE app_studio_messages.actor_id = EXCLUDED.actor_id
	`, scope.OrgUUID, scope.WorkspaceUUID, scope.ProjectName, scope.ProjectUID, msg.ID,
		msg.ActorID, msg.Role, msg.Content, msg.ContentEncrypted, msg.ContentKeyID,
		metadata, msg.CreatedAt.UTC(), msg.UpdatedAt.UTC())
	if err != nil {
		return fmt.Errorf("upsert snapshot message: %w", err)
	}
	if n, err := result.RowsAffected(); err != nil {
		return fmt.Errorf("count upsert snapshot message: %w", err)
	} else if n != 1 {
		return fmt.Errorf("message %q actor is immutable", msg.ID)
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

func normalizeAssistantRunError(raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 {
		raw = json.RawMessage(`{}`)
	}
	normalized, err := normalizePostgresJSONB(raw)
	if err != nil {
		return nil, fmt.Errorf("assistant run terminal error is not valid json: %w", err)
	}
	return normalized, nil
}

func getAssistantRunByClientRequestID(ctx context.Context, queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, scope Scope, clientRequestID string) (AssistantRun, error) {
	row := queryer.QueryRowContext(ctx, `
			SELECT run_id, mode, approval_mode, status, client_request_id, user_message_id, active_message_id, revision,
		       request_id, checkpoint, audit, terminal_error, abort_reason, created_at, updated_at
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
		&run.Mode,
		&run.ApprovalMode,
		&status,
		&run.ClientRequestID,
		&run.UserMessageID,
		&run.ActiveMessageID,
		&run.Revision,
		&run.RequestID,
		&run.Checkpoint,
		&run.Audit,
		&run.Error,
		&run.AbortReason,
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
	run.Error = cloneRawMessage(run.Error)
	run.CreatedAt = run.CreatedAt.UTC()
	run.UpdatedAt = run.UpdatedAt.UTC()
	return run, nil
}

func scanAssistantRunEvent(row interface {
	Scan(dest ...any) error
}, scope Scope) (AssistantRunEvent, error) {
	var event AssistantRunEvent
	if err := row.Scan(
		&event.RunID,
		&event.Sequence,
		&event.Type,
		&event.CallID,
		&event.ToolName,
		&event.ArgsDigest,
		&event.Payload,
		&event.CreatedAt,
	); err != nil {
		return AssistantRunEvent{}, err
	}
	event.ProjectName = scope.ProjectName
	event.ProjectUID = scope.ProjectUID
	event.Payload = cloneRawMessage(event.Payload)
	event.CreatedAt = event.CreatedAt.UTC()
	return event, nil
}

var _ Store = (*PostgresStore)(nil)
