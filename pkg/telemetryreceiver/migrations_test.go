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

package telemetryreceiver

import (
	"database/sql"
	"io/fs"
	"strings"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/database"
)

func TestEmbeddedMigrationParsesWithGoose(t *testing.T) {
	migrationFS, err := fs.Sub(migrations, "migrations")
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("pgx", "postgres://user:password@localhost/faros")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("database Close() error = %v", err)
		}
	})
	store, err := database.NewStore(database.DialectPostgres, "faros_telemetry_schema_migrations")
	if err != nil {
		t.Fatal(err)
	}
	provider, err := goose.NewProvider("", db, migrationFS, goose.WithStore(store))
	if err != nil {
		t.Fatalf("goose migration parse = %v", err)
	}
	t.Cleanup(func() {
		if err := provider.Close(); err != nil {
			t.Errorf("provider Close() error = %v", err)
		}
	})
	sources := provider.ListSources()
	wantSources := []string{"001_initial.sql", "002_metric_projections.sql", "003_retention_and_funnel_labels.sql", "004_erasure_installation_scope.sql", "005_durable_installation_erasure.sql", "006_utc_metric_windows.sql"}
	if len(sources) != len(wantSources) {
		t.Fatalf("goose sources = %+v", sources)
	}
	for index, want := range wantSources {
		if sources[index].Version != int64(index+1) || sources[index].Path != want {
			t.Fatalf("goose source %d = %+v, want %s", index, sources[index], want)
		}
	}
	raw, err := fs.ReadFile(migrationFS, "002_metric_projections.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(raw)
	if !strings.Contains(sql, "PRIMARY KEY (metric_key, funnel_step, event_type)") || !strings.Contains(sql, "SELECT DISTINCT metric_key, window_days") {
		t.Fatal("metric catalog must preserve event selection identity and de-duplicate counter metadata joins")
	}
	raw, err = fs.ReadFile(migrationFS, "003_retention_and_funnel_labels.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql = string(raw)
	if !strings.Contains(sql, "PRIMARY KEY (bucket_start, metric_key, event_type, funnel_step") ||
		!strings.Contains(sql, "COALESCE(u.labels, '{}'::jsonb) AS labels") ||
		!strings.Contains(sql, "u.event_type = c.event_type") ||
		!strings.Contains(sql, "ADD COLUMN IF NOT EXISTS event_type") {
		t.Fatal("migration 003 must bind uniqueness retention to event type and preserve funnel labels")
	}
	raw, err = fs.ReadFile(migrationFS, "004_erasure_installation_scope.sql")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "RENAME COLUMN tenant_id TO installation_id") {
		t.Fatal("migration 004 must make the installation-wide erasure scope explicit")
	}
	raw, err = fs.ReadFile(migrationFS, "005_durable_installation_erasure.sql")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "faros_telemetry_erased_installations") {
		t.Fatal("migration 005 must persist installation erasure tombstones")
	}
	raw, err = fs.ReadFile(migrationFS, "006_utc_metric_windows.sql")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "CURRENT_TIMESTAMP AT TIME ZONE 'UTC'") {
		t.Fatal("migration 006 must make metric windows independent of the database session timezone")
	}
}
