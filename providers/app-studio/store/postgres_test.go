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
