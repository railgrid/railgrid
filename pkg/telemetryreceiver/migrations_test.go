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
	if len(sources) != 3 || sources[0].Version != 1 || sources[0].Path != "001_initial.sql" || sources[1].Version != 2 || sources[1].Path != "002_metric_projections.sql" || sources[2].Version != 3 || sources[2].Path != "003_retention_and_funnel_labels.sql" {
		t.Fatalf("goose sources = %+v", sources)
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
		!strings.Contains(sql, "u.event_type = c.event_type") {
		t.Fatal("migration 003 must bind uniqueness retention to event type and preserve funnel labels")
	}
}
