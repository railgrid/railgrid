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
