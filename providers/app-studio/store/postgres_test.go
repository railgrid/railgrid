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

	scope := Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: "customer-portal"}
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
		ID:         "run-1",
		Status:     AssistantRunStatusPendingPermission,
		RequestID:  "perm-1",
		Checkpoint: json.RawMessage(`{"tool":"write_file"}`),
		Audit:      json.RawMessage(`{"decisions":[{"decision":"allow"}]}`),
		CreatedAt:  time.Date(2026, 6, 14, 12, 1, 0, 0, time.UTC),
		UpdatedAt:  time.Date(2026, 6, 14, 12, 1, 0, 0, time.UTC),
	}
	if err := s.SaveAssistantRun(ctx, scope, run); err != nil {
		t.Fatalf("SaveAssistantRun: %v", err)
	}
	gotRun, err := s.GetAssistantRun(ctx, scope, run.ID)
	if err != nil {
		t.Fatalf("GetAssistantRun: %v", err)
	}
	if gotRun.ID != run.ID || gotRun.Status != run.Status || gotRun.RequestID != run.RequestID || string(gotRun.Checkpoint) != string(run.Checkpoint) || string(gotRun.Audit) != string(run.Audit) {
		t.Fatalf("assistant run = %#v, want %#v", gotRun, run)
	}
	claimed, err := s.ClaimAssistantRun(ctx, scope, run.ID, run.RequestID, run.UpdatedAt.Add(time.Minute))
	if err != nil {
		t.Fatalf("ClaimAssistantRun: %v", err)
	}
	if claimed.Status != AssistantRunStatusRunning || claimed.RequestID != run.RequestID {
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
	if completed.Status != AssistantRunStatusCompleted || string(completed.Audit) != string(claimed.Audit) {
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
		Message{ID: "user-1", Role: "user", Content: "first", CreatedAt: createdAt, UpdatedAt: createdAt},
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
		Message{ID: "user-retry", Role: "user", Content: "retry", CreatedAt: createdAt.Add(time.Minute), UpdatedAt: createdAt.Add(time.Minute)},
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
		Message{ID: "user-2", Role: "user", Content: "second", CreatedAt: createdAt.Add(2 * time.Minute), UpdatedAt: createdAt.Add(2 * time.Minute)},
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
	grant := AssistantRun{
		ID:              AssistantRunIDApprovedPlanGrant,
		Status:          AssistantRunStatusCompleted,
		ClientRequestID: "internal-grant-request",
		RequestID:       "grant-revision",
		Checkpoint:      json.RawMessage(`{"revision":"grant-revision"}`),
		CreatedAt:       createdAt.Add(4 * time.Minute),
		UpdatedAt:       createdAt.Add(4 * time.Minute),
	}
	if err := store.CompareAndSwapAssistantRun(ctx, scope, grant, ""); err != nil {
		t.Fatalf("persist grant: %v", err)
	}
	latest, err = durable.LatestAssistantRun(ctx, scope)
	if err != nil {
		t.Fatalf("LatestAssistantRun after grant: %v", err)
	}
	if latest.ID != run.ID {
		t.Fatalf("latest run after grant = %q, want conversation run %q", latest.ID, run.ID)
	}
	if _, err := durable.FindAssistantRunByClientRequestID(ctx, scope, grant.ClientRequestID); !errors.Is(err, ErrAssistantRunNotFound) {
		t.Fatalf("FindAssistantRunByClientRequestID grant error = %v, want not found", err)
	}
	if _, err := durable.GetAssistantRun(ctx, scope, grant.ID); err != nil {
		t.Fatalf("GetAssistantRun grant: %v", err)
	}

	for _, indexName := range []string{
		"app_studio_assistant_runs_scope_client_request_idx",
		"app_studio_assistant_runs_scope_active_idx",
	} {
		var indexDef string
		if err := store.db.QueryRowContext(ctx, `SELECT indexdef FROM pg_indexes WHERE schemaname = $1 AND indexname = $2`, schemaName, indexName).Scan(&indexDef); err != nil {
			t.Fatalf("check %s: %v", indexName, err)
		}
		if !strings.Contains(indexDef, AssistantRunIDApprovedPlanGrant) {
			t.Fatalf("durable assistant-run index %q = %q, want reserved grant predicate", indexName, indexDef)
		}
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

func TestPostgresStoreMigratesOnlyLegacyRunningRunsExternalDSN(t *testing.T) {
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
