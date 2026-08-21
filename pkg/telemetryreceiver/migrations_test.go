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
	defer db.Close()
	store, err := database.NewStore(database.DialectPostgres, "faros_telemetry_schema_migrations")
	if err != nil {
		t.Fatal(err)
	}
	provider, err := goose.NewProvider("", db, migrationFS, goose.WithStore(store))
	if err != nil {
		t.Fatalf("goose migration parse = %v", err)
	}
	defer provider.Close()
	sources := provider.ListSources()
	if len(sources) != 1 || sources[0].Version != 1 || sources[0].Path != "001_initial.sql" {
		t.Fatalf("goose sources = %+v", sources)
	}
}
