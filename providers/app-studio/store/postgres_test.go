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
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/lib/pq"
)

func TestAssistantSchemaIsV2Only(t *testing.T) {
	statements := strings.Join(assistantSchemaStatements(), "\n")
	for _, retired := range []string{
		"app_studio_assistant_work_items",
		"work_item_id",
		"expected_grant_revision",
		"engine_version",
		"discussion",
		"adaptive",
	} {
		if strings.Contains(statements, retired) {
			t.Fatalf("assistant schema still contains retired contract %q", retired)
		}
	}
	if !strings.Contains(statements, "CHECK (mode IN ('default', 'plan', 'review'))") {
		t.Fatal("assistant schema does not constrain runs to supported collaboration modes")
	}
}

func TestAssistantV2CutoverDropsRetiredStorageBeforeRecreate(t *testing.T) {
	statements := assistantV2CutoverSchemaStatements()
	joined := strings.Join(statements, "\n")
	for _, table := range []string{
		"app_studio_assistant_run_events",
		"app_studio_assistant_work_items",
		"app_studio_assistant_runs",
		"app_studio_messages",
	} {
		if !strings.Contains(joined, "DROP TABLE IF EXISTS "+table) {
			t.Fatalf("cutover does not drop %s", table)
		}
	}
	if !strings.Contains(joined, "CHECK (mode IN ('default', 'plan', 'review'))") ||
		!strings.Contains(joined, "CREATE TABLE IF NOT EXISTS app_studio_assistant_run_events") {
		t.Fatal("cutover does not recreate the complete V2 assistant schema")
	}
	if assistantV2CutoverSchemaVersion != "assistant-v2-destructive-cutover-v1" {
		t.Fatalf("cutover schema version = %q", assistantV2CutoverSchemaVersion)
	}
}

func TestAssistantV2CutoverSerializesMigrationChecks(t *testing.T) {
	if lockMessageSchemaMigrations != `SELECT pg_advisory_xact_lock(870408091945886937)` {
		t.Fatalf("migration lock = %q", lockMessageSchemaMigrations)
	}
}

func TestAssistantConversationIdentityIsRunScoped(t *testing.T) {
	initial := strings.Join(assistantConversationSchemaStatements(), "\n")
	if !strings.Contains(initial, "PRIMARY KEY (org_uuid, workspace_uuid, project_name, project_uid, run_id, item_id)") {
		t.Fatal("conversation schema does not make item identity run-scoped")
	}
}

func TestAssistantConversationSequenceSchemaPreservesProjectHighWaterMark(t *testing.T) {
	statements := strings.Join(assistantConversationSequenceSchemaStatements(), "\n")
	for _, want := range []string{
		"app_studio_assistant_conversation_sequences",
		"last_sequence bigint NOT NULL DEFAULT 0",
		"PRIMARY KEY (org_uuid, workspace_uuid, project_name, project_uid)",
	} {
		if !strings.Contains(statements, want) {
			t.Fatalf("conversation sequence schema missing %q", want)
		}
	}
	if assistantConversationSequenceSchemaVersion != "assistant-conversation-sequence-v1" {
		t.Fatalf("conversation sequence schema version = %q", assistantConversationSequenceSchemaVersion)
	}
}

func TestNormalizePostgresJSONBSanitizesNullCodePoint(t *testing.T) {
	raw := json.RawMessage(`{
		"message": "before\u0000after",
		"nested": {"bad\u0000key": "value\u0000"},
		"items": ["ok\u0000", {"inner": "still\u0000bad"}]
	}`)

	normalized, err := normalizePostgresJSONB(raw)
	if err != nil {
		t.Fatalf("normalizePostgresJSONB returned error: %v", err)
	}
	if strings.Contains(string(normalized), `\u0000`) || strings.Contains(string(normalized), "\x00") {
		t.Fatalf("normalized JSON still contains a PostgreSQL-rejected null: %q", normalized)
	}

	var got map[string]any
	if err := json.Unmarshal(normalized, &got); err != nil {
		t.Fatalf("normalized JSON did not unmarshal: %v", err)
	}
	if got["message"] != "before\ufffdafter" {
		t.Fatalf("message = %#v, want null code point replaced", got["message"])
	}
	nested, ok := got["nested"].(map[string]any)
	if !ok || nested["bad\ufffdkey"] != "value\ufffd" {
		t.Fatalf("nested sanitized value = %#v", got["nested"])
	}
}

func TestNormalizePostgresJSONBPreservesLargeIntegers(t *testing.T) {
	raw := json.RawMessage(`{"end":9223372036854775807}`)
	normalized, err := normalizePostgresJSONB(raw)
	if err != nil {
		t.Fatalf("normalizePostgresJSONB returned error: %v", err)
	}
	if string(normalized) != string(raw) {
		t.Fatalf("normalized JSON = %s, want %s", normalized, raw)
	}
}

func TestNormalizePostgresJSONBRejectsTrailingJSONValue(t *testing.T) {
	if _, err := normalizePostgresJSONB(json.RawMessage(`{} {}`)); err == nil {
		t.Fatal("normalizePostgresJSONB accepted multiple JSON values")
	}
}

func TestPostgresStoreV2ContractExternalDSN(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("APP_STUDIO_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("APP_STUDIO_TEST_POSTGRES_DSN is not set")
	}
	ctx := context.Background()
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("open postgres: %v", err)
	}
	defer db.Close()

	schemaName := "app_studio_v2_" + time.Now().UTC().Format("20060102150405")
	if _, err := db.ExecContext(ctx, "CREATE SCHEMA "+pq.QuoteIdentifier(schemaName)); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() {
		cleanupDB, openErr := sql.Open("postgres", dsn)
		if openErr != nil {
			return
		}
		defer cleanupDB.Close()
		_, _ = cleanupDB.ExecContext(context.Background(), "DROP SCHEMA "+pq.QuoteIdentifier(schemaName)+" CASCADE")
	})

	schemaDSN := postgresDSNWithSearchPath(t, dsn, schemaName)
	legacyDB, err := sql.Open("postgres", schemaDSN)
	if err != nil {
		t.Fatalf("open predecessor schema: %v", err)
	}
	for _, statement := range []string{
		createMessageSchemaMigrationsTable,
		`CREATE TABLE app_studio_messages (org_uuid text, workspace_uuid text, project_name text, project_uid text, message_id text, work_item_id text)`,
		`CREATE TABLE app_studio_assistant_runs (org_uuid text, workspace_uuid text, project_name text, project_uid text, run_id text, work_item_id text, engine_version text, mode text, status text)`,
		`CREATE TABLE app_studio_assistant_work_items (org_uuid text, workspace_uuid text, project_name text, project_uid text, work_item_id text)`,
		`CREATE TABLE app_studio_assistant_run_events (org_uuid text, workspace_uuid text, project_name text, project_uid text, run_id text, sequence bigint)`,
		`INSERT INTO app_studio_assistant_runs VALUES ('org-a','ws-1','customer-portal','project-1','legacy-run','legacy-item','engine-v1','adaptive','running')`,
	} {
		if _, err := legacyDB.ExecContext(ctx, statement); err != nil {
			_ = legacyDB.Close()
			t.Fatalf("seed predecessor schema: %v", err)
		}
	}
	if err := legacyDB.Close(); err != nil {
		t.Fatalf("close predecessor schema: %v", err)
	}

	s, err := OpenPostgres(ctx, schemaDSN)
	if err != nil {
		t.Fatalf("OpenPostgres: %v", err)
	}
	defer s.Close()

	var retiredTable sql.NullString
	if err := s.db.QueryRowContext(ctx, `SELECT to_regclass('app_studio_assistant_work_items')`).Scan(&retiredTable); err != nil {
		t.Fatalf("inspect retired table: %v", err)
	}
	if retiredTable.Valid {
		t.Fatalf("retired WorkItem table still exists as %q", retiredTable.String)
	}
	var legacyColumns int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM information_schema.columns
		WHERE table_schema = current_schema() AND table_name = 'app_studio_assistant_runs'
		AND column_name IN ('work_item_id', 'engine_version', 'expected_grant_revision')`).Scan(&legacyColumns); err != nil {
		t.Fatalf("inspect retired columns: %v", err)
	}
	if legacyColumns != 0 {
		t.Fatalf("assistant runs retain %d retired columns", legacyColumns)
	}
	var legacyRuns int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM app_studio_assistant_runs WHERE run_id = 'legacy-run'`).Scan(&legacyRuns); err != nil {
		t.Fatalf("inspect retired runs: %v", err)
	}
	if legacyRuns != 0 {
		t.Fatalf("destructive cutover retained %d legacy runs", legacyRuns)
	}

	scope := Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: "customer-portal", ProjectUID: "project-1"}
	createdAt := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	user := Message{ID: "user-1", Role: "user", ActorID: "actor-1", Content: "build it", CreatedAt: createdAt, UpdatedAt: createdAt}
	assistant := Message{ID: "assistant-1", Role: "assistant", CreatedAt: createdAt, UpdatedAt: createdAt}
	run := AssistantRun{
		ID:              "run-1",
		Mode:            AssistantRunModeDefault,
		ApprovalMode:    AssistantApprovalModeAutoApprove,
		Status:          AssistantRunStatusRunning,
		ClientRequestID: "request-1",
		UserMessageID:   user.ID,
		ActiveMessageID: assistant.ID,
		Revision:        1,
		CreatedAt:       createdAt,
		UpdatedAt:       createdAt,
	}
	created, err := s.CreateAssistantRun(ctx, scope, user, assistant, run)
	if err != nil {
		t.Fatalf("CreateAssistantRun: %v", err)
	}
	if created.Mode != AssistantRunModeDefault || created.ApprovalMode != AssistantApprovalModeAutoApprove {
		t.Fatalf("created run = %#v", created)
	}

	created.Status = AssistantRunStatusCompleted
	created.Revision++
	created.UpdatedAt = createdAt.Add(time.Minute)
	if err := s.SaveAssistantRunSnapshot(ctx, scope, created, []Message{{
		ID: created.ActiveMessageID, Role: "assistant", Content: "done", CreatedAt: createdAt, UpdatedAt: created.UpdatedAt,
	}}, 1); err != nil {
		t.Fatalf("SaveAssistantRunSnapshot: %v", err)
	}
	persisted, err := s.GetAssistantRun(ctx, scope, run.ID)
	if err != nil {
		t.Fatalf("GetAssistantRun: %v", err)
	}
	if persisted.Status != AssistantRunStatusCompleted || persisted.Revision != 2 {
		t.Fatalf("persisted run = %#v", persisted)
	}

	thread, err := s.CreateAssistantThread(ctx, scope, AssistantThread{ID: "thread-1", ActorID: "actor-1", CreatedAt: createdAt, UpdatedAt: createdAt}, []AssistantThreadEvent{
		{Type: "thread.created", Payload: json.RawMessage(`{"thread":"thread-1"}`), CreatedAt: createdAt},
	})
	if err != nil {
		t.Fatalf("CreateAssistantThread: %v", err)
	}
	turn, err := s.CreateAssistantTurn(ctx, scope, AssistantTurn{
		ID: "turn-1", ThreadID: thread.ID, ActorID: "actor-1", ClientUserMessageID: "client-message-1",
		Mode: AssistantRunModeDefault, ApprovalMode: AssistantApprovalModeOnRequest,
		Status: AssistantTurnStatusInProgress, CreatedAt: createdAt, UpdatedAt: createdAt,
	}, []AssistantThreadEvent{{Type: "turn.started", Payload: json.RawMessage(`{"turn":"turn-1"}`), CreatedAt: createdAt}})
	if err != nil {
		t.Fatalf("CreateAssistantTurn: %v", err)
	}
	approval, err := s.AppendAssistantThreadEvent(ctx, scope, AssistantThreadEvent{
		ThreadID: thread.ID, TurnID: turn.ID, Type: "approval.requested", ItemID: "perm-1", RequestID: "perm-1",
		Payload: json.RawMessage(`{"requestID":"perm-1"}`), CreatedAt: createdAt.Add(time.Second),
	}, 2)
	if err != nil {
		t.Fatalf("AppendAssistantThreadEvent: %v", err)
	}
	if approval.Sequence != 3 {
		t.Fatalf("approval sequence = %d, want 3", approval.Sequence)
	}
	turn.Status = AssistantTurnStatusCompleted
	turn.UpdatedAt = createdAt.Add(2 * time.Second)
	if err := s.SaveAssistantTurnWithEvent(ctx, scope, turn, AssistantThreadEvent{
		Type: "turn.completed", Payload: json.RawMessage(`{"turn":"turn-1"}`), CreatedAt: turn.UpdatedAt,
	}, approval.Sequence); err != nil {
		t.Fatalf("SaveAssistantTurnWithEvent: %v", err)
	}
}

func TestPostgresRetentionProtectsActiveMessagesAndConversationHighWaterMark(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("APP_STUDIO_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("APP_STUDIO_TEST_POSTGRES_DSN is not set")
	}
	ctx := context.Background()
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("open postgres: %v", err)
	}
	defer db.Close()

	schemaName := fmt.Sprintf("app_studio_retention_seq_%d", time.Now().UTC().UnixNano())
	if _, err := db.ExecContext(ctx, "CREATE SCHEMA "+pq.QuoteIdentifier(schemaName)); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() {
		cleanupDB, openErr := sql.Open("postgres", dsn)
		if openErr != nil {
			return
		}
		defer cleanupDB.Close()
		_, _ = cleanupDB.ExecContext(context.Background(), "DROP SCHEMA "+pq.QuoteIdentifier(schemaName)+" CASCADE")
	})

	store, err := OpenPostgres(ctx, postgresDSNWithSearchPath(t, dsn, schemaName))
	if err != nil {
		t.Fatalf("OpenPostgres: %v", err)
	}
	defer store.Close()

	scope := Scope{OrgUUID: "org-retention", WorkspaceUUID: "workspace-retention", ProjectName: "demo", ProjectUID: "project-retention"}
	old := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	activeRun, err := store.CreateAssistantRun(ctx, scope,
		Message{ID: "active-user", Role: "user", ActorID: "alice", Content: "active prompt", CreatedAt: old, UpdatedAt: old},
		Message{ID: "active-assistant", Role: "assistant", Content: "active response", CreatedAt: old, UpdatedAt: old},
		AssistantRun{ID: "run-active", Mode: AssistantRunModeDefault, Status: AssistantRunStatusRunning,
			ClientRequestID: "request-active", UserMessageID: "active-user", ActiveMessageID: "active-assistant",
			Revision: 1, CreatedAt: old, UpdatedAt: old},
	)
	if err != nil {
		t.Fatalf("CreateAssistantRun: %v", err)
	}
	if activeRun.ID != "run-active" {
		t.Fatalf("active run = %#v", activeRun)
	}
	if err := store.SaveAssistantRun(ctx, scope, AssistantRun{ID: "run-terminal", Mode: AssistantRunModeDefault,
		Status: AssistantRunStatusCompleted, CreatedAt: old, UpdatedAt: old}); err != nil {
		t.Fatalf("SaveAssistantRun terminal: %v", err)
	}
	first, err := store.AppendAssistantConversationItem(ctx, scope, AssistantConversationItem{
		ID: "terminal-item", RunID: "run-terminal", Type: "assistant_message", Payload: json.RawMessage(`{"content":"terminal"}`), CreatedAt: old,
	})
	if err != nil {
		t.Fatalf("AppendAssistantConversationItem terminal: %v", err)
	}
	if first.Sequence != 1 {
		t.Fatalf("terminal item sequence = %d, want 1", first.Sequence)
	}

	deleted, err := store.DeleteMessagesOlderThan(ctx, old.Add(time.Hour))
	if err != nil {
		t.Fatalf("DeleteMessagesOlderThan: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("deleted = %d, want terminal run only (active messages are protected)", deleted)
	}
	if _, err := store.GetAssistantRun(ctx, scope, "run-terminal"); !errors.Is(err, ErrAssistantRunNotFound) {
		t.Fatalf("terminal run after retention = %v, want not found", err)
	}
	if _, err := store.GetAssistantRun(ctx, scope, "run-active"); err != nil {
		t.Fatalf("active run after retention = %v, want present", err)
	}
	page, err := store.ListMessages(ctx, scope, 10, "")
	if err != nil {
		t.Fatalf("ListMessages after retention: %v", err)
	}
	if len(page.Items) != 2 {
		t.Fatalf("active messages after retention = %#v, want both messages", page.Items)
	}
	items, err := store.ListAssistantConversationItems(ctx, scope, 0, 10)
	if err != nil {
		t.Fatalf("ListAssistantConversationItems after retention: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("terminal conversation items after retention = %#v, want empty", items)
	}

	second, err := store.AppendAssistantConversationItem(ctx, scope, AssistantConversationItem{
		ID: "active-item", RunID: "run-active", Type: "assistant_message", Payload: json.RawMessage(`{"content":"active"}`), CreatedAt: old.Add(2 * time.Hour),
	})
	if err != nil {
		t.Fatalf("AppendAssistantConversationItem after retention: %v", err)
	}
	if second.Sequence != 2 {
		t.Fatalf("next sequence after all-items retention = %d, want 2", second.Sequence)
	}
	items, err = store.ListAssistantConversationItems(ctx, scope, first.Sequence, 10)
	if err != nil {
		t.Fatalf("ListAssistantConversationItems after old cursor: %v", err)
	}
	if len(items) != 1 || items[0].ID != second.ID || items[0].Sequence != second.Sequence {
		t.Fatalf("items after old cursor = %#v, want active item at sequence 2", items)
	}
}

func TestPostgresRetentionDoesNotDeleteAttachmentWhenThreadIsRevived(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("APP_STUDIO_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("APP_STUDIO_TEST_POSTGRES_DSN is not set")
	}
	ctx := context.Background()
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("open postgres: %v", err)
	}
	defer db.Close()
	schemaName := fmt.Sprintf("app_studio_retention_attachment_race_%d", time.Now().UTC().UnixNano())
	if _, err := db.ExecContext(ctx, "CREATE SCHEMA "+pq.QuoteIdentifier(schemaName)); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() {
		cleanupDB, openErr := sql.Open("postgres", dsn)
		if openErr != nil {
			return
		}
		defer cleanupDB.Close()
		_, _ = cleanupDB.ExecContext(context.Background(), "DROP SCHEMA "+pq.QuoteIdentifier(schemaName)+" CASCADE")
	})

	schemaDSN := postgresDSNWithSearchPath(t, dsn, schemaName)
	store, err := OpenPostgres(ctx, schemaDSN)
	if err != nil {
		t.Fatalf("OpenPostgres: %v", err)
	}
	defer store.Close()
	scope := Scope{OrgUUID: "org-retention-race", WorkspaceUUID: "workspace-retention-race", ProjectName: "demo", ProjectUID: "project-retention-race"}
	old := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Microsecond)
	thread, err := store.CreateAssistantThread(ctx, scope, AssistantThread{ID: "thread-retention-race", ActorID: "alice", CreatedAt: old, UpdatedAt: old}, nil)
	if err != nil {
		t.Fatalf("CreateAssistantThread: %v", err)
	}
	turn, err := store.CreateAssistantTurn(ctx, scope, AssistantTurn{ID: "turn-retention-race", ThreadID: thread.ID, ActorID: "alice", ClientUserMessageID: "client-retention-race", Status: AssistantTurnStatusCompleted, CreatedAt: old, UpdatedAt: old}, nil)
	if err != nil {
		t.Fatalf("CreateAssistantTurn: %v", err)
	}
	thread.Status, thread.UpdatedAt = AssistantThreadStatusIdle, old
	if _, err := store.UpdateAssistantThread(ctx, scope, thread); err != nil {
		t.Fatalf("make thread stale: %v", err)
	}
	expires := time.Now().UTC().Add(time.Hour)
	attachment := attachmentTestRecord(old, true, &expires)
	attachment.ID = "retention-race-attachment"
	if _, err := store.CreateAttachment(ctx, scope, attachment); err != nil {
		t.Fatalf("CreateAttachment: %v", err)
	}
	receipt := AttachmentReceipt{ID: attachment.ID, Filename: attachment.Filename, ContentType: attachment.ContentType, SizeBytes: attachment.SizeBytes, SHA256: attachment.SHA256, CreatedAt: attachment.CreatedAt}
	if _, err := store.BindAttachments(ctx, scope, []AttachmentReceipt{receipt}, "alice", turn.ID); err != nil {
		t.Fatalf("BindAttachments: %v", err)
	}

	holdDB, err := sql.Open("postgres", schemaDSN)
	if err != nil {
		t.Fatalf("open lock-holder postgres: %v", err)
	}
	defer holdDB.Close()
	holdTx, err := holdDB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin lock-holder transaction: %v", err)
	}
	defer func() { _ = holdTx.Rollback() }()
	if _, err := holdTx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, postgresAttachmentWorkspaceLockKey(scope)); err != nil {
		t.Fatalf("hold workspace attachment lock: %v", err)
	}
	if _, err := holdTx.ExecContext(ctx, `SELECT thread_id FROM app_studio_assistant_threads
		WHERE org_uuid=$1 AND workspace_uuid=$2 AND project_name=$3 AND project_uid=$4 AND thread_id=$5 FOR UPDATE`,
		scope.OrgUUID, scope.WorkspaceUUID, scope.ProjectName, scope.ProjectUID, thread.ID); err != nil {
		t.Fatalf("hold thread row lock: %v", err)
	}

	retentionCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	retentionDone := make(chan error, 1)
	go func() {
		_, retentionErr := store.DeleteMessagesOlderThan(retentionCtx, old.Add(time.Hour))
		retentionDone <- retentionErr
	}()
	// Let retention build its candidate set and wait on the held thread row.
	time.Sleep(100 * time.Millisecond)
	if _, err := holdTx.ExecContext(ctx, `UPDATE app_studio_assistant_threads SET status='active',updated_at=$6
		WHERE org_uuid=$1 AND workspace_uuid=$2 AND project_name=$3 AND project_uid=$4 AND thread_id=$5`,
		scope.OrgUUID, scope.WorkspaceUUID, scope.ProjectName, scope.ProjectUID, thread.ID, time.Now().UTC()); err != nil {
		t.Fatalf("revive held thread: %v", err)
	}
	if err := holdTx.Commit(); err != nil {
		t.Fatalf("commit revived thread: %v", err)
	}
	if err := <-retentionDone; err != nil {
		t.Fatalf("DeleteMessagesOlderThan: %v", err)
	}
	if _, err := store.GetAssistantThread(ctx, scope, thread.ID); err != nil {
		t.Fatalf("revived thread after retention = %v, want present", err)
	}
	if _, err := store.GetAttachment(ctx, scope, attachment.ID); err != nil {
		t.Fatalf("attachment after thread revival race = %v, want present", err)
	}
}

func TestPostgresAttachmentLifecycleExternalDSN(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("APP_STUDIO_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("APP_STUDIO_TEST_POSTGRES_DSN is not set")
	}
	ctx := context.Background()
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	schemaName := fmt.Sprintf("app_studio_attachment_lifecycle_%d", time.Now().UTC().UnixNano())
	if _, err := db.ExecContext(ctx, "CREATE SCHEMA "+pq.QuoteIdentifier(schemaName)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupDB, openErr := sql.Open("postgres", dsn)
		if openErr != nil {
			return
		}
		defer cleanupDB.Close()
		_, _ = cleanupDB.ExecContext(context.Background(), "DROP SCHEMA "+pq.QuoteIdentifier(schemaName)+" CASCADE")
	})
	s, err := OpenPostgres(ctx, postgresDSNWithSearchPath(t, dsn, schemaName))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	scope := attachmentTestScope()
	now := time.Now().UTC().Truncate(time.Microsecond)
	expires := now.Add(time.Hour)
	first := attachmentTestRecord(now, true, &expires)
	second := attachmentTestRecord(now.Add(time.Microsecond), true, &expires)
	second.ID, second.Filename = "att-postgres-second", "second.png"
	for _, attachment := range []Attachment{first, second} {
		if _, err := s.CreateAttachment(ctx, scope, attachment); err != nil {
			t.Fatal(err)
		}
	}
	receipt := func(attachment Attachment) AttachmentReceipt {
		return AttachmentReceipt{ID: attachment.ID, Filename: attachment.Filename, ContentType: attachment.ContentType, SizeBytes: attachment.SizeBytes, SHA256: attachment.SHA256, CreatedAt: attachment.CreatedAt}
	}
	bad := receipt(second)
	bad.SHA256 = strings.Repeat("0", 64)
	if _, err := s.BindAttachments(ctx, scope, []AttachmentReceipt{receipt(first), bad}, "alice", "run-bad"); !errors.Is(err, ErrAttachmentReceiptMismatch) {
		t.Fatalf("invalid batch error = %v", err)
	}
	if got, _ := s.GetAttachment(ctx, scope, first.ID); !got.Draft {
		t.Fatal("rejected batch partially promoted an attachment")
	}
	if _, err := s.BindAttachments(ctx, scope, []AttachmentReceipt{receipt(first), receipt(second)}, "alice", "run-1"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteAttachment(ctx, scope, first.ID, "alice"); !errors.Is(err, ErrAttachmentImmutable) {
		t.Fatalf("delete bound attachment error = %v", err)
	}
	if err := s.RollbackAttachmentBinding(ctx, scope, "run-1"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetAttachment(ctx, scope, first.ID); !got.Draft {
		t.Fatal("rollback did not restore draft")
	}
	if err := s.DeleteProjectAttachments(ctx, scope); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateAttachment(ctx, scope, first); !errors.Is(err, ErrAttachmentProjectDeleted) {
		t.Fatalf("create after deletion error = %v", err)
	}
}

func postgresDSNWithSearchPath(t *testing.T, dsn, schemaName string) string {
	t.Helper()
	u, err := url.Parse(dsn)
	if err != nil || u.Scheme == "" {
		t.Fatalf("APP_STUDIO_TEST_POSTGRES_DSN must be a URL-style DSN for this test")
	}
	q := u.Query()
	q.Set("search_path", schemaName)
	u.RawQuery = q.Encode()
	return u.String()
}
