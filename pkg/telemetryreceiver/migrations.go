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
	"embed"
	"fmt"
	"io/fs"

	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/database"
)

//go:embed migrations/*.sql
var migrations embed.FS

// RunMigrations applies the embedded PostgreSQL migrations with goose. The
// receiver's request path uses pgxpool; goose is intentionally isolated here
// behind database/sql because its public API uses that standard interface.
func RunMigrations(ctx context.Context, db *sql.DB) error {
	migrationFS, err := fs.Sub(migrations, "migrations")
	if err != nil {
		return fmt.Errorf("open telemetry migrations: %w", err)
	}
	store, err := database.NewStore(database.DialectPostgres, "faros_telemetry_schema_migrations")
	if err != nil {
		return fmt.Errorf("create telemetry migration store: %w", err)
	}
	provider, err := goose.NewProvider("", db, migrationFS, goose.WithStore(store))
	if err != nil {
		return fmt.Errorf("create telemetry migration provider: %w", err)
	}
	defer func() { _ = provider.Close() }()
	if _, err := provider.Up(ctx); err != nil {
		return fmt.Errorf("run telemetry migrations: %w", err)
	}
	return nil
}
