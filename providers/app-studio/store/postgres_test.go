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
	"net/url"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/lib/pq"
)

func TestPostgresStoreExternalDSN(t *testing.T) {
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

	schemaName := "app_studio_test_" + strings.ToLower(strings.ReplaceAll(t.Name(), "/", "_")) + "_" + time.Now().UTC().Format("20060102150405")
	if _, err := db.ExecContext(ctx, "CREATE SCHEMA "+pq.QuoteIdentifier(schemaName)); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), "DROP SCHEMA "+pq.QuoteIdentifier(schemaName)+" CASCADE")
	})

	s, err := OpenPostgres(ctx, postgresDSNWithSearchPath(t, dsn, schemaName))
	if err != nil {
		t.Fatalf("OpenPostgres: %v", err)
	}
	defer s.Close()

	scope := Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: "customer-portal", ProjectUID: "project-1"}
	msg := Message{
		ID:        "msg-1",
		Role:      "user",
		Content:   "build a customer portal",
		CreatedAt: time.Date(2026, 6, 14, 12, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 6, 14, 12, 0, 0, 0, time.UTC),
	}
	if err := s.AppendMessage(ctx, scope, msg); err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}
	page, err := s.ListMessages(ctx, scope, 10, "")
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	if len(page.Items) != 1 || page.Items[0].Content != msg.Content {
		t.Fatalf("unexpected messages: %#v", page.Items)
	}

	run := AssistantRun{
		ID:           "run-1",
		ApprovalMode: AssistantApprovalModeAutoApprove,
		Status:       AssistantRunStatusPendingPermission,
		RequestID:    "perm-1",
		Checkpoint:   json.RawMessage(`{"tool":"write_file"}`),
		Audit:        json.RawMessage(`{"decisions":[{"decision":"allow"}]}`),
		CreatedAt:    time.Date(2026, 6, 14, 12, 1, 0, 0, time.UTC),
		UpdatedAt:    time.Date(2026, 6, 14, 12, 1, 0, 0, time.UTC),
	}
	if err := s.SaveAssistantRun(ctx, scope, run); err != nil {
		t.Fatalf("SaveAssistantRun: %v", err)
	}
	gotRun, err := s.GetAssistantRun(ctx, scope, run.ID)
	if err != nil {
		t.Fatalf("GetAssistantRun: %v", err)
	}
	if gotRun.ID != run.ID || gotRun.ApprovalMode != run.ApprovalMode || gotRun.Status != run.Status || gotRun.RequestID != run.RequestID || !jsonSemanticallyEqual(gotRun.Checkpoint, run.Checkpoint) || !jsonSemanticallyEqual(gotRun.Audit, run.Audit) {
		t.Fatalf("assistant run = %#v, want %#v", gotRun, run)
	}
	claimed, err := s.ClaimAssistantRun(ctx, scope, run.ID, run.RequestID, run.UpdatedAt.Add(time.Minute))
	if err != nil {
		t.Fatalf("ClaimAssistantRun: %v", err)
	}
	if claimed.Status != AssistantRunStatusRunning || claimed.RequestID != run.RequestID || claimed.ApprovalMode != AssistantApprovalModeAutoApprove {
		t.Fatalf("claimed assistant run = %#v, want running request", claimed)
	}
	if _, err := s.ClaimAssistantRun(ctx, scope, run.ID, run.RequestID, run.UpdatedAt.Add(2*time.Minute)); err == nil {
		t.Fatal("second ClaimAssistantRun returned nil error")
	}
	claimed.Status = AssistantRunStatusCompleted
	claimed.UpdatedAt = run.UpdatedAt.Add(3 * time.Minute)
	claimed.Audit = json.RawMessage(`{"decisions":[{"decision":"allow","result":"ok"}]}`)
	if err := s.SaveAssistantRun(ctx, scope, claimed); err != nil {
		t.Fatalf("SaveAssistantRun completed: %v", err)
	}
	completed, err := s.GetAssistantRun(ctx, scope, run.ID)
	if err != nil {
		t.Fatalf("GetAssistantRun completed: %v", err)
	}
	if completed.Status != AssistantRunStatusCompleted || !jsonSemanticallyEqual(completed.Audit, claimed.Audit) {
		t.Fatalf("completed assistant run = %#v, want completed audit", completed)
	}
	deleted, err := s.DeleteMessagesOlderThan(ctx, run.UpdatedAt.Add(4*time.Minute))
	if err != nil {
		t.Fatalf("DeleteMessagesOlderThan: %v", err)
	}
	if deleted != 2 {
		t.Fatalf("deleted count = %d, want message and assistant run", deleted)
	}
	if _, err := s.GetAssistantRun(ctx, scope, run.ID); err == nil {
		t.Fatal("GetAssistantRun after retention returned nil error")
	}
}

func TestPostgresStoreWorkItemContractExternalDSN(t *testing.T) {
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
	schemaName := "app_studio_work_items_" + time.Now().UTC().Format("20060102150405")
	if _, err := db.ExecContext(ctx, "CREATE SCHEMA "+pq.QuoteIdentifier(schemaName)); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), "DROP SCHEMA "+pq.QuoteIdentifier(schemaName)+" CASCADE")
	})
	s, err := OpenPostgres(ctx, postgresDSNWithSearchPath(t, dsn, schemaName))
	if err != nil {
		t.Fatalf("OpenPostgres: %v", err)
	}
	defer s.Close()

	scope := Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: "demo", ProjectUID: "project-1"}
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	item := testWorkItem("item-1", "user-1")
	run := testWorkItemRun("run-1", item.ID, "user-1", "assistant-1")
	run.CreatedAt, run.UpdatedAt = now, now
	created, err := s.CreateWorkItemAndAssistantRun(ctx, scope, item, testWorkItemUser("user-1"), testWorkItemAssistant("assistant-1"), run)
	if err != nil {
		t.Fatalf("CreateWorkItemAndAssistantRun: %v", err)
	}
	cleared := created
	cleared.PlanGrant = nil
	cleared.Revision++
	cleared.UpdatedAt = now.Add(30 * time.Second)
	if err := s.CompareAndSwapAssistantWorkItem(ctx, scope, cleared, created.Revision); err != nil {
		t.Fatalf("CompareAndSwapAssistantWorkItem with nil plan grant: %v", err)
	}
	persistedCleared, err := s.GetAssistantWorkItem(ctx, scope, item.ID)
	if err != nil {
		t.Fatalf("GetAssistantWorkItem after cleared plan grant: %v", err)
	}
	if !jsonSemanticallyEqual(persistedCleared.PlanGrant, json.RawMessage(`{}`)) {
		t.Fatalf("cleared plan grant = %s, want empty JSON object", persistedCleared.PlanGrant)
	}
	created = cleared
	executionPlan := json.RawMessage(`{"summary":"Build it","steps":[{"id":"one"}]}`)
	planned, err := s.SaveWorkItemExecutionPlan(ctx, scope, item.ID, run.ID, created.Revision, "execution-plan-1", executionPlan, now.Add(45*time.Second))
	if err != nil {
		t.Fatalf("SaveWorkItemExecutionPlan: %v", err)
	}
	var rawExecutionPlan []byte
	var rawExecutionPlanRevision string
	if err := s.db.QueryRowContext(ctx, `SELECT execution_plan, execution_plan_revision
		FROM app_studio_assistant_work_items
		WHERE org_uuid=$1 AND workspace_uuid=$2 AND project_name=$3 AND project_uid=$4 AND work_item_id=$5`,
		scope.OrgUUID, scope.WorkspaceUUID, scope.ProjectName, scope.ProjectUID, item.ID,
	).Scan(&rawExecutionPlan, &rawExecutionPlanRevision); err != nil {
		t.Fatalf("read raw execution plan columns: %v", err)
	}
	if rawExecutionPlanRevision != "execution-plan-1" || !jsonSemanticallyEqual(rawExecutionPlan, executionPlan) {
		t.Fatalf("raw execution plan = %s revision=%q", rawExecutionPlan, rawExecutionPlanRevision)
	}
	created = planned
	approved, err := s.ApproveWorkItemPlan(ctx, scope, item.ID, run.ID, created.Revision, "grant-1", json.RawMessage(`{"capabilities":["workspace_mutate"]}`), now.Add(time.Minute))
	if err != nil {
		t.Fatalf("ApproveWorkItemPlan: %v", err)
	}
	persistedRun, err := s.GetAssistantRun(ctx, scope, run.ID)
	if err != nil {
		t.Fatalf("GetAssistantRun: %v", err)
	}
	if persistedRun.ExpectedGrantRevision != "grant-1" {
		t.Fatalf("expected grant revision = %q, want grant-1", persistedRun.ExpectedGrantRevision)
	}
	persistedRun.Status = AssistantRunStatusCompleted
	persistedRun.Revision++
	persistedRun.Checkpoint = json.RawMessage(`{"must":"clear"}`)
	if err := s.TransitionWorkItemAndRun(ctx, scope, item.ID, approved.Revision, persistedRun, AssistantWorkItemStatusCompleted, "completed", now.Add(2*time.Minute)); err != nil {
		t.Fatalf("TransitionWorkItemAndRun: %v", err)
	}
	terminal, err := s.GetAssistantWorkItem(ctx, scope, item.ID)
	if err != nil {
		t.Fatalf("GetAssistantWorkItem: %v", err)
	}
	if terminal.Status != AssistantWorkItemStatusCompleted || terminal.ActiveRunID != "" || terminal.GrantRevision != "" || !jsonSemanticallyEqual(terminal.PlanGrant, json.RawMessage(`{}`)) {
		t.Fatalf("terminal work item = %#v", terminal)
	}
	persistedRun, err = s.GetAssistantRun(ctx, scope, run.ID)
	if err != nil {
		t.Fatalf("GetAssistantRun terminal: %v", err)
	}
	if !jsonSemanticallyEqual(persistedRun.Checkpoint, json.RawMessage(`{}`)) {
		t.Fatalf("terminal checkpoint = %s, want empty object", persistedRun.Checkpoint)
	}
	duplicate := persistedRun
	duplicate.ID = "run-duplicate-request"
	duplicate.Revision = 1
	duplicate.CreatedAt = now.Add(3 * time.Minute)
	duplicate.UpdatedAt = duplicate.CreatedAt
	if err := s.SaveAssistantRun(ctx, scope, duplicate); !errors.Is(err, ErrAssistantRunConflict) {
		t.Fatalf("duplicate scoped client request ID error = %v, want %v", err, ErrAssistantRunConflict)
	}
	for i := 0; i < 30; i++ {
		messageTime := now.Add(time.Duration(10+i) * time.Minute)
		if err := s.AppendMessage(ctx, scope, Message{
			ID:         "history-" + time.Unix(int64(i), 0).UTC().Format("150405"),
			WorkItemID: item.ID,
			Role:       "assistant",
			Content:    "history",
			CreatedAt:  messageTime,
			UpdatedAt:  messageTime,
		}); err != nil {
			t.Fatalf("AppendMessage history %d: %v", i, err)
		}
	}
	recent, err := s.LoadMessagesForWorkItem(ctx, scope, item.ID, 5)
	if err != nil {
		t.Fatalf("LoadMessagesForWorkItem newest messages: %v", err)
	}
	wantRecent := []string{"history-000025", "history-000026", "history-000027", "history-000028", "history-000029"}
	if len(recent) != len(wantRecent) {
		t.Fatalf("recent messages = %#v, want %d", recent, len(wantRecent))
	}
	for i := range wantRecent {
		if recent[i].ID != wantRecent[i] {
			t.Fatalf("recent message %d = %q, want %q", i, recent[i].ID, wantRecent[i])
		}
	}
	testRetireWorkItemPlanContract(t, s)
}

func TestPostgresStoreUpgradesEmptyLegacySchemaAtWorkItemBoundaryExternalDSN(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("APP_STUDIO_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("APP_STUDIO_TEST_POSTGRES_DSN is not set")
	}
	ctx := context.Background()
	admin, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("open postgres: %v", err)
	}
	defer admin.Close()
	schemaName := "app_studio_legacy_reset_" + time.Now().UTC().Format("20060102150405")
	if _, err := admin.ExecContext(ctx, "CREATE SCHEMA "+pq.QuoteIdentifier(schemaName)); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() {
		_, _ = admin.ExecContext(context.Background(), "DROP SCHEMA "+pq.QuoteIdentifier(schemaName)+" CASCADE")
	})
	scopedDSN := postgresDSNWithSearchPath(t, dsn, schemaName)
	legacy, err := sql.Open("postgres", scopedDSN)
	if err != nil {
		t.Fatalf("open legacy schema: %v", err)
	}
	if _, err := legacy.ExecContext(ctx, createMessageSchemaMigrationsTable); err != nil {
		t.Fatalf("create legacy migration table: %v", err)
	}
	if _, err := legacy.ExecContext(ctx, `INSERT INTO app_studio_message_schema_migrations(version) VALUES ('v3')`); err != nil {
		t.Fatalf("insert legacy version: %v", err)
	}
	if _, err := legacy.ExecContext(ctx, `CREATE TABLE app_studio_messages (
		org_uuid text NOT NULL, workspace_uuid text NOT NULL, project_name text NOT NULL,
		message_id text NOT NULL, role text NOT NULL, content text NOT NULL,
		created_at timestamptz NOT NULL, updated_at timestamptz NOT NULL
	)`); err != nil {
		t.Fatalf("create legacy messages: %v", err)
	}
	if _, err := legacy.ExecContext(ctx, `CREATE TABLE app_studio_assistant_runs (
		org_uuid text NOT NULL, workspace_uuid text NOT NULL, project_name text NOT NULL,
		run_id text NOT NULL, status text NOT NULL, created_at timestamptz NOT NULL, updated_at timestamptz NOT NULL
	)`); err != nil {
		t.Fatalf("create legacy runs: %v", err)
	}
	_ = legacy.Close()

	s, err := OpenPostgres(ctx, scopedDSN)
	if err != nil {
		t.Fatalf("OpenPostgres upgrade empty legacy schema: %v", err)
	}
	defer s.Close()
	var projectUIDColumns int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM information_schema.columns
		WHERE table_schema = $1 AND table_name IN ('app_studio_messages', 'app_studio_assistant_runs')
			AND column_name = 'project_uid'`, schemaName).Scan(&projectUIDColumns); err != nil {
		t.Fatalf("query reset schema: %v", err)
	}
	if projectUIDColumns != 2 {
		t.Fatalf("project_uid columns = %d, want 2 after empty legacy upgrade", projectUIDColumns)
	}
	scope := Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo", ProjectUID: "project-uid-a"}
	now := time.Now().UTC()
	user := Message{ID: "user-1", Role: "user", ActorID: "alice", Content: "hello", CreatedAt: now, UpdatedAt: now}
	assistant := Message{ID: "assistant-1", Role: "assistant", CreatedAt: now.Add(time.Microsecond), UpdatedAt: now.Add(time.Microsecond)}
	run := AssistantRun{
		ID: "run-1", Mode: AssistantRunModeDiscussion, Status: AssistantRunStatusRunning,
		ClientRequestID: "request-1", UserMessageID: user.ID, ActiveMessageID: assistant.ID,
		Revision: 1, CreatedAt: now, UpdatedAt: now,
	}
	if _, err := s.CreateAssistantRun(ctx, scope, user, assistant, run); err != nil {
		t.Fatalf("CreateAssistantRun after empty legacy upgrade: %v", err)
	}
	if messages, err := s.LoadRecentMessages(ctx, scope, 10); err != nil || len(messages) != 2 {
		t.Fatalf("LoadRecentMessages after empty legacy upgrade = %#v, %v; want two messages", messages, err)
	}
	if persisted, err := s.GetAssistantRun(ctx, scope, run.ID); err != nil || persisted.ID != run.ID {
		t.Fatalf("GetAssistantRun after empty legacy upgrade = %#v, %v", persisted, err)
	}
}

func TestPostgresStoreRejectsPopulatedLegacySchemaWithoutDataLossExternalDSN(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("APP_STUDIO_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("APP_STUDIO_TEST_POSTGRES_DSN is not set")
	}
	ctx := context.Background()
	admin, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("open postgres: %v", err)
	}
	defer admin.Close()
	schemaName := "app_studio_legacy_preserve_" + time.Now().UTC().Format("20060102150405")
	if _, err := admin.ExecContext(ctx, "CREATE SCHEMA "+pq.QuoteIdentifier(schemaName)); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() {
		_, _ = admin.ExecContext(context.Background(), "DROP SCHEMA "+pq.QuoteIdentifier(schemaName)+" CASCADE")
	})
	scopedDSN := postgresDSNWithSearchPath(t, dsn, schemaName)
	legacy, err := sql.Open("postgres", scopedDSN)
	if err != nil {
		t.Fatalf("open legacy schema: %v", err)
	}
	defer legacy.Close()
	if _, err := legacy.ExecContext(ctx, createMessageSchemaMigrationsTable); err != nil {
		t.Fatalf("create legacy migration table: %v", err)
	}
	if _, err := legacy.ExecContext(ctx, `CREATE TABLE app_studio_messages (
		org_uuid text NOT NULL, workspace_uuid text NOT NULL, project_name text NOT NULL,
		message_id text NOT NULL, role text NOT NULL, content text NOT NULL,
		created_at timestamptz NOT NULL, updated_at timestamptz NOT NULL
	)`); err != nil {
		t.Fatalf("create legacy messages: %v", err)
	}
	if _, err := legacy.ExecContext(ctx, `INSERT INTO app_studio_messages (
		org_uuid, workspace_uuid, project_name, message_id, role, content, created_at, updated_at
	) VALUES ('org-a', 'workspace-a', 'demo', 'message-1', 'user', 'preserve me', now(), now())`); err != nil {
		t.Fatalf("insert legacy message: %v", err)
	}

	if _, err := OpenPostgres(ctx, scopedDSN); err == nil || !strings.Contains(err.Error(), "stopped without changing data") {
		t.Fatalf("OpenPostgres populated legacy error = %v, want actionable safe-stop error", err)
	}
	var rows int
	if err := legacy.QueryRowContext(ctx, `SELECT count(*) FROM app_studio_messages WHERE content='preserve me'`).Scan(&rows); err != nil {
		t.Fatalf("count preserved legacy messages: %v", err)
	}
	if rows != 1 {
		t.Fatalf("preserved legacy rows = %d, want 1", rows)
	}
	var projectUIDColumn bool
	if err := legacy.QueryRowContext(ctx, `SELECT EXISTS (
		SELECT 1 FROM information_schema.columns
		WHERE table_schema=$1 AND table_name='app_studio_messages' AND column_name='project_uid'
	)`, schemaName).Scan(&projectUIDColumn); err != nil {
		t.Fatalf("inspect legacy schema after rejection: %v", err)
	}
	if projectUIDColumn {
		t.Fatal("populated legacy schema was mutated before safe rejection")
	}
}

func jsonSemanticallyEqual(left, right json.RawMessage) bool {
	var leftValue, rightValue any
	if json.Unmarshal(left, &leftValue) != nil || json.Unmarshal(right, &rightValue) != nil {
		return false
	}
	return reflect.DeepEqual(leftValue, rightValue)
}

func TestNormalizePostgresJSONBSanitizesNullCodePoint(t *testing.T) {
	raw := json.RawMessage(`{
		"message": "before\u0000after",
		"nested": {
			"bad\u0000key": "value\u0000"
		},
		"items": ["ok\u0000", {"inner": "still\u0000bad"}]
	}`)

	normalized, err := normalizePostgresJSONB(raw)
	if err != nil {
		t.Fatalf("normalizePostgresJSONB returned error: %v", err)
	}
	if strings.Contains(string(normalized), `\u0000`) {
		t.Fatalf("normalized JSON still contains PostgreSQL-rejected null escape: %s", normalized)
	}
	if strings.Contains(string(normalized), "\x00") {
		t.Fatalf("normalized JSON still contains raw null byte: %q", normalized)
	}

	var got map[string]any
	if err := json.Unmarshal(normalized, &got); err != nil {
		t.Fatalf("normalized JSON did not unmarshal: %v", err)
	}
	if got["message"] != "before\ufffdafter" {
		t.Fatalf("message = %#v, want null code point replaced", got["message"])
	}
	nested, ok := got["nested"].(map[string]any)
	if !ok {
		t.Fatalf("nested = %#v, want object", got["nested"])
	}
	if nested["bad\ufffdkey"] != "value\ufffd" {
		t.Fatalf("nested sanitized value = %#v, want replacement in key and value", nested)
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

func TestWorkItemSchemaUpgradePreservesTables(t *testing.T) {
	upgrade := workItemSchemaUpgradeStatements()
	final := workItemSchemaStatements()
	if len(upgrade) == 0 || len(final) == 0 {
		t.Fatalf("schema statements upgrade=%#v final=%#v", upgrade, final)
	}
	for _, stmt := range append(upgrade, final...) {
		if strings.HasPrefix(strings.ToUpper(strings.TrimSpace(stmt)), "DROP TABLE") {
			t.Fatalf("schema statement %q must not drop tables", stmt)
		}
	}
	if messageSchemaVersion != "work-item-v2" {
		t.Fatalf("message schema version = %q, want rebuilt work-item-v2 boundary", messageSchemaVersion)
	}
	if !schemaStatementsContain(final, "CREATE UNIQUE INDEX IF NOT EXISTS app_studio_assistant_runs_scope_client_request_idx") {
		t.Fatal("work item schema does not enforce scoped client request ID uniqueness")
	}
	if clientRequestUniqueSchemaVersion == messageSchemaVersion ||
		!schemaStatementsContain(clientRequestUniqueSchemaStatements(), "CREATE UNIQUE INDEX IF NOT EXISTS app_studio_assistant_runs_scope_client_request_idx") {
		t.Fatal("client request uniqueness must also have an independently applied migration for existing schemas")
	}
	if executionPlanSchemaVersion == messageSchemaVersion ||
		!schemaStatementsContain(executionPlanSchemaStatements(), "ADD COLUMN IF NOT EXISTS execution_plan jsonb") ||
		!schemaStatementsContain(executionPlanSchemaStatements(), "ADD COLUMN IF NOT EXISTS execution_plan_revision text") {
		t.Fatal("execution-plan columns must have an independently applied additive migration")
	}
}

func TestNormalizeAssistantWorkItemPlanGrant(t *testing.T) {
	normalized, err := normalizeAssistantWorkItemPlanGrant(nil)
	if err != nil {
		t.Fatalf("normalize nil plan grant: %v", err)
	}
	if string(normalized) != `{}` {
		t.Fatalf("normalized nil plan grant = %q, want valid empty JSON object", normalized)
	}
	if _, err := normalizeAssistantWorkItemPlanGrant(json.RawMessage(`not-json`)); err == nil {
		t.Fatal("invalid plan grant returned nil error")
	}
}

func schemaStatementsContain(statements []string, fragment string) bool {
	for _, statement := range statements {
		if strings.Contains(statement, fragment) {
			return true
		}
	}
	return false
}

func TestPostgresStoreDurableAssistantRunContractExternalDSN(t *testing.T) {
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
	schemaName := "app_studio_durable_runs_" + time.Now().UTC().Format("20060102150405")
	if _, err := db.ExecContext(ctx, "CREATE SCHEMA "+pq.QuoteIdentifier(schemaName)); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), "DROP SCHEMA "+pq.QuoteIdentifier(schemaName)+" CASCADE")
	})
	store, err := OpenPostgres(ctx, postgresDSNWithSearchPath(t, dsn, schemaName))
	if err != nil {
		t.Fatalf("OpenPostgres: %v", err)
	}
	defer store.Close()
	durable := mustDurableAssistantRunStore(t, store)
	scope := testAssistantRunScope()
	createdAt := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	run := testAssistantRun(t, "run-1", "request-1", "assistant-1", createdAt)
	created, err := durable.CreateAssistantRun(ctx, scope,
		Message{ID: "user-1", Role: "user", ActorID: "actor-1", Content: "first", CreatedAt: createdAt, UpdatedAt: createdAt},
		Message{ID: "assistant-1", Role: "assistant", CreatedAt: createdAt, UpdatedAt: createdAt},
		run,
	)
	if err != nil {
		t.Fatalf("CreateAssistantRun: %v", err)
	}
	if created.UserMessageID != "user-1" {
		t.Fatalf("created user message id = %q, want user-1", created.UserMessageID)
	}
	recovered, err := durable.CreateAssistantRun(ctx, scope,
		Message{ID: "user-retry", Role: "user", ActorID: "actor-1", Content: "retry", CreatedAt: createdAt.Add(time.Minute), UpdatedAt: createdAt.Add(time.Minute)},
		Message{ID: "assistant-retry", Role: "assistant", CreatedAt: createdAt.Add(time.Minute), UpdatedAt: createdAt.Add(time.Minute)},
		testAssistantRun(t, "run-retry", "request-1", "assistant-retry", createdAt.Add(time.Minute)),
	)
	if err != nil {
		t.Fatalf("duplicate CreateAssistantRun: %v", err)
	}
	if recovered.ID != created.ID {
		t.Fatalf("duplicate create recovered %q, want %q", recovered.ID, created.ID)
	}
	if recovered.UserMessageID != created.UserMessageID {
		t.Fatalf("duplicate create user message id = %q, want %q", recovered.UserMessageID, created.UserMessageID)
	}
	if err := store.SaveAssistantRun(ctx, scope, AssistantRun{
		ID:        "legacy-2",
		Status:    AssistantRunStatusPendingInput,
		CreatedAt: createdAt.Add(time.Minute),
		UpdatedAt: createdAt.Add(time.Minute),
	}); !errors.Is(err, ErrAssistantRunConflict) {
		t.Fatalf("second active SaveAssistantRun error = %v, want conflict", err)
	}
	_, err = durable.CreateAssistantRun(ctx, scope,
		Message{ID: "user-2", Role: "user", ActorID: "actor-1", Content: "second", CreatedAt: createdAt.Add(2 * time.Minute), UpdatedAt: createdAt.Add(2 * time.Minute)},
		Message{ID: "assistant-2", Role: "assistant", CreatedAt: createdAt.Add(2 * time.Minute), UpdatedAt: createdAt.Add(2 * time.Minute)},
		testAssistantRun(t, "run-2", "request-2", "assistant-2", createdAt.Add(2*time.Minute)),
	)
	if !errors.Is(err, ErrAssistantRunConflict) {
		t.Fatalf("second active CreateAssistantRun error = %v, want conflict", err)
	}

	run = created
	run.Revision = 2
	run.ClientRequestID = "replacement-request-id"
	run.CreatedAt = createdAt.Add(24 * time.Hour)
	run.UpdatedAt = createdAt.Add(3 * time.Minute)
	if err := durable.SaveAssistantRunSnapshot(ctx, scope, run, []Message{{
		ID:        "assistant-1",
		Role:      "assistant",
		Content:   "working",
		CreatedAt: createdAt,
		UpdatedAt: run.UpdatedAt,
	}}, 1); err != nil {
		t.Fatalf("SaveAssistantRunSnapshot: %v", err)
	}
	if err := durable.SaveAssistantRunSnapshot(ctx, scope, run, nil, 1); !errors.Is(err, ErrAssistantRunConflict) {
		t.Fatalf("stale SaveAssistantRunSnapshot error = %v, want conflict", err)
	}
	found, err := durable.FindAssistantRunByClientRequestID(ctx, scope, "request-1")
	if err != nil {
		t.Fatalf("FindAssistantRunByClientRequestID: %v", err)
	}
	if found.ID != run.ID || found.Revision != 2 || !found.CreatedAt.Equal(createdAt) {
		t.Fatalf("found run = %#v, want revisioned run", found)
	}
	if _, err := durable.FindAssistantRunByClientRequestID(ctx, scope, "replacement-request-id"); !errors.Is(err, ErrAssistantRunNotFound) {
		t.Fatalf("replacement client request lookup error = %v, want not found", err)
	}
	latest, err := durable.LatestAssistantRun(ctx, scope)
	if err != nil {
		t.Fatalf("LatestAssistantRun: %v", err)
	}
	if latest.ID != run.ID {
		t.Fatalf("latest run = %q, want %q", latest.ID, run.ID)
	}
	if err := store.DeleteProjectMessages(ctx, scope); err != nil {
		t.Fatalf("DeleteProjectMessages: %v", err)
	}
	if _, err := store.GetAssistantRun(ctx, scope, run.ID); !errors.Is(err, ErrAssistantRunNotFound) {
		t.Fatalf("GetAssistantRun after deletion error = %v, want not found", err)
	}
	page, err := store.ListMessages(ctx, scope, 10, "")
	if err != nil {
		t.Fatalf("ListMessages after deletion: %v", err)
	}
	if len(page.Items) != 0 {
		t.Fatalf("messages after deletion = %#v, want empty", page.Items)
	}
}

func TestPostgresStoreStopAndGrantRevocationAreAtomicExternalDSN(t *testing.T) {
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
	schemaName := "app_studio_stop_" + time.Now().UTC().Format("20060102150405")
	if _, err := db.ExecContext(ctx, "CREATE SCHEMA "+pq.QuoteIdentifier(schemaName)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), "DROP SCHEMA "+pq.QuoteIdentifier(schemaName)+" CASCADE")
	})
	s, err := OpenPostgres(ctx, postgresDSNWithSearchPath(t, dsn, schemaName))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	scope := Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-a", ProjectName: "demo", ProjectUID: "project-1"}
	item := testWorkItem("item-1", "user-1")
	run := testWorkItemRun("run-1", item.ID, "user-1", "assistant-1")
	created, err := s.CreateWorkItemAndAssistantRun(ctx, scope, item, testWorkItemUser("user-1"), testWorkItemAssistant("assistant-1"), run)
	if err != nil {
		t.Fatal(err)
	}
	approved, err := s.ApproveWorkItemPlan(ctx, scope, item.ID, run.ID, created.Revision, "grant-1", json.RawMessage(`{"capabilities":["workspace_mutate"]}`), time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	run, err = s.GetAssistantRun(ctx, scope, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	stopping, err := s.RequestAssistantRunStop(ctx, scope, item.ID, run.ID, approved.Revision, run.Revision, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	revoked, err := s.GetAssistantWorkItem(ctx, scope, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stopping.Status != AssistantRunStatusStopping || revoked.GrantRevision != "" || !jsonSemanticallyEqual(revoked.PlanGrant, json.RawMessage(`{}`)) {
		t.Fatalf("stop = %#v, WorkItem = %#v", stopping, revoked)
	}
}

func TestPostgresStoreMigratesOnlyLegacyRunningRunsExternalDSN(t *testing.T) {
	t.Skip("work-item persistence is a net-new deployment and does not migrate legacy run schemas")
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
	schemaName := "app_studio_legacy_runs_" + time.Now().UTC().Format("20060102150405")
	if _, err := db.ExecContext(ctx, "CREATE SCHEMA "+pq.QuoteIdentifier(schemaName)); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), "DROP SCHEMA "+pq.QuoteIdentifier(schemaName)+" CASCADE")
	})
	if _, err := db.ExecContext(ctx, `
		CREATE TABLE `+pq.QuoteIdentifier(schemaName)+`.app_studio_message_schema_migrations (
			version text PRIMARY KEY,
			applied_at timestamptz NOT NULL DEFAULT now()
		);
		INSERT INTO `+pq.QuoteIdentifier(schemaName)+`.app_studio_message_schema_migrations(version) VALUES ('v3');
		CREATE TABLE `+pq.QuoteIdentifier(schemaName)+`.app_studio_assistant_runs (
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
		);
	`); err != nil {
		t.Fatalf("create legacy schema: %v", err)
	}
	scope := testAssistantRunScope()
	createdAt := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	if _, err := db.ExecContext(ctx, `INSERT INTO `+pq.QuoteIdentifier(schemaName)+`.app_studio_assistant_runs (
		org_uuid, workspace_uuid, project_name, run_id, status, created_at, updated_at
	) VALUES
		($1,$2,'running-project','legacy-running','running',$3,$3),
		($1,$2,'permission-project','legacy-permission','pending_permission',$3,$3),
		($1,$2,'input-project','legacy-input','pending_input',$3,$3)`, scope.OrgUUID, scope.WorkspaceUUID, createdAt); err != nil {
		t.Fatalf("insert legacy runs: %v", err)
	}
	store, err := OpenPostgres(ctx, postgresDSNWithSearchPath(t, dsn, schemaName))
	if err != nil {
		t.Fatalf("OpenPostgres: %v", err)
	}
	defer store.Close()
	for _, tt := range []struct {
		id      string
		project string
		status  AssistantRunStatus
	}{
		{id: "legacy-running", project: "running-project", status: AssistantRunStatusInterrupted},
		{id: "legacy-permission", project: "permission-project", status: AssistantRunStatusPendingPermission},
		{id: "legacy-input", project: "input-project", status: AssistantRunStatusPendingInput},
	} {
		legacyScope := scope
		legacyScope.ProjectName = tt.project
		legacy, err := store.GetAssistantRun(ctx, legacyScope, tt.id)
		if err != nil {
			t.Fatalf("GetAssistantRun %s: %v", tt.id, err)
		}
		if legacy.Status != tt.status {
			t.Fatalf("migrated %s status = %q, want %q", tt.id, legacy.Status, tt.status)
		}
	}
	for _, tt := range []struct {
		id        string
		project   string
		requestID string
	}{
		{id: "legacy-permission", project: "permission-project", requestID: "permission-1"},
		{id: "legacy-input", project: "input-project", requestID: "input-1"},
	} {
		if _, err := db.ExecContext(ctx, `UPDATE `+pq.QuoteIdentifier(schemaName)+`.app_studio_assistant_runs SET request_id = $1 WHERE run_id = $2`, tt.requestID, tt.id); err != nil {
			t.Fatalf("set %s request id: %v", tt.id, err)
		}
		legacyScope := scope
		legacyScope.ProjectName = tt.project
		claimed, err := store.ClaimAssistantRun(ctx, legacyScope, tt.id, tt.requestID, createdAt.Add(time.Minute))
		if err != nil {
			t.Fatalf("ClaimAssistantRun %s: %v", tt.id, err)
		}
		if claimed.Status != AssistantRunStatusRunning {
			t.Fatalf("claimed %s status = %q, want running", tt.id, claimed.Status)
		}
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
