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
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/database"
)

func TestPostgresMigrationUpgradeShapes(t *testing.T) {
	dsn := os.Getenv("TELEMETRY_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TELEMETRY_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = admin.Close() }()

	tests := []struct {
		name   string
		mutate func(*testing.T, context.Context, *sql.DB)
	}{
		{name: "clean_v2"},
		{name: "v2_with_uniques_event_type", mutate: addHistoricalUniqueEventType},
		{name: "v2_without_catalog_event_type", mutate: removeHistoricalCatalogEventType},
	}
	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			schema := fmt.Sprintf("telemetry_migration_%d_%d", time.Now().UnixNano(), i)
			if _, err := admin.ExecContext(ctx, `CREATE SCHEMA "`+schema+`"`); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				_, _ = admin.ExecContext(context.Background(), `DROP SCHEMA "`+schema+`" CASCADE`)
			})

			scopedDSN := migrationTestDSN(t, dsn, schema)
			v2DB, err := sql.Open("pgx", scopedDSN)
			if err != nil {
				t.Fatal(err)
			}
			provider := migrationTestProvider(t, v2DB)
			if _, err := provider.UpTo(ctx, 2); err != nil {
				t.Fatalf("migrate to v2: %v", err)
			}
			if err := provider.Close(); err != nil {
				t.Fatal(err)
			}

			db, err := sql.Open("pgx", scopedDSN)
			if err != nil {
				t.Fatal(err)
			}
			if tt.mutate != nil {
				tt.mutate(t, ctx, db)
			}
			if err := RunMigrations(ctx, db); err != nil {
				t.Fatalf("upgrade historical v2 shape: %v", err)
			}
			if err := db.PingContext(ctx); err != nil {
				t.Fatalf("RunMigrations closed the caller-owned database: %v", err)
			}
			defer func() { _ = db.Close() }()

			verifyDB, err := sql.Open("pgx", scopedDSN)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = verifyDB.Close() }()
			var version int
			if err := verifyDB.QueryRowContext(ctx, `SELECT MAX(version_id) FROM faros_telemetry_schema_migrations WHERE is_applied`).Scan(&version); err != nil {
				t.Fatal(err)
			}
			if version != 6 {
				t.Fatalf("migration version = %d, want 6", version)
			}
			for _, table := range []string{"faros_telemetry_erased_installations", "faros_telemetry_metric_uniques"} {
				var exists bool
				if err := verifyDB.QueryRowContext(ctx, `SELECT to_regclass($1) IS NOT NULL`, table).Scan(&exists); err != nil {
					t.Fatal(err)
				}
				if !exists {
					t.Fatalf("table %s was not created", table)
				}
			}
		})
	}
}

func migrationTestDSN(t *testing.T, dsn, schema string) string {
	t.Helper()
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	query := u.Query()
	query.Set("search_path", schema)
	u.RawQuery = query.Encode()
	return u.String()
}

func migrationTestProvider(t *testing.T, db *sql.DB) *goose.Provider {
	t.Helper()
	migrationFS, err := fs.Sub(migrations, "migrations")
	if err != nil {
		t.Fatal(err)
	}
	store, err := database.NewStore(database.DialectPostgres, "faros_telemetry_schema_migrations")
	if err != nil {
		t.Fatal(err)
	}
	provider, err := goose.NewProvider("", db, migrationFS, goose.WithStore(store))
	if err != nil {
		t.Fatal(err)
	}
	return provider
}

func addHistoricalUniqueEventType(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	statements := []string{
		`ALTER TABLE faros_telemetry_metric_uniques ADD COLUMN event_type TEXT NOT NULL DEFAULT 'edge_first_ready'`,
		`ALTER TABLE faros_telemetry_metric_uniques DROP CONSTRAINT faros_telemetry_metric_uniques_pkey`,
		`ALTER TABLE faros_telemetry_metric_uniques ADD PRIMARY KEY (bucket_start, metric_key, event_type, funnel_step, labels_key, tenant_id, unique_kind, unique_hash)`,
	}
	execMigrationTestStatements(t, ctx, db, statements)
}

func removeHistoricalCatalogEventType(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	statements := []string{
		`DROP VIEW faros_telemetry_activation_current`,
		`DROP VIEW faros_telemetry_funnel_current`,
		`DROP VIEW faros_telemetry_counter_current`,
		`ALTER TABLE faros_telemetry_metric_catalog DROP CONSTRAINT faros_telemetry_metric_catalog_pkey`,
		`ALTER TABLE faros_telemetry_metric_catalog DROP COLUMN event_type`,
		`ALTER TABLE faros_telemetry_metric_catalog ADD PRIMARY KEY (metric_key, funnel_step)`,
		`CREATE VIEW faros_telemetry_counter_current AS SELECT 1 AS placeholder`,
		`CREATE VIEW faros_telemetry_funnel_current AS SELECT 1 AS placeholder`,
		`CREATE VIEW faros_telemetry_activation_current AS SELECT 1 AS placeholder`,
	}
	execMigrationTestStatements(t, ctx, db, statements)
}

func execMigrationTestStatements(t *testing.T, ctx context.Context, db *sql.DB, statements []string) {
	t.Helper()
	for _, statement := range statements {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			t.Fatalf("execute %q: %v", statement, err)
		}
	}
}
