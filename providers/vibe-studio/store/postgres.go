// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0

package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	_ "github.com/lib/pq"

	"github.com/faroshq/provider-vibe-studio/session"
)

// PostgresStore is the durable production Store. Schema is created/updated by
// EnsureSchema (idempotent DDL); every table is scoped by tenant so a single
// database serves all tenants.
type PostgresStore struct {
	db *sql.DB
}

// OpenPostgres opens the Postgres-backed store and verifies connectivity.
// Call EnsureSchema before first use.
func OpenPostgres(ctx context.Context, dsn string) (*PostgresStore, error) {
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, fmt.Errorf("open postgres: %w", err)
	}
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(30 * time.Minute)
	pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := db.PingContext(pingCtx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping postgres: %w", err)
	}
	return &PostgresStore{db: db}, nil
}

func (p *PostgresStore) Close() error { return p.db.Close() }

var vibeSchema = []string{
	`CREATE TABLE IF NOT EXISTS vibe_sessions (
		tenant TEXT NOT NULL,
		id TEXT NOT NULL,
		preview TEXT NOT NULL DEFAULT '',
		phase TEXT NOT NULL DEFAULT 'intake',
		workspace_revision BIGINT NOT NULL DEFAULT 0,
		created_at TIMESTAMPTZ NOT NULL,
		updated_at TIMESTAMPTZ NOT NULL,
		PRIMARY KEY (tenant, id)
	)`,
	`ALTER TABLE vibe_sessions ADD COLUMN IF NOT EXISTS workspace_revision BIGINT NOT NULL DEFAULT 0`,
	`CREATE INDEX IF NOT EXISTS vibe_sessions_activity_idx
		ON vibe_sessions (tenant, updated_at DESC)`,
	// The append-only event log. Ordinals are per-session, dense from 1; the
	// (tenant, session_id, ordinal) PK makes concurrent appends of the same
	// ordinal a unique violation, which surfaces as ErrOrdinalConflict.
	// The session's editable source tree; upserted whole-file.
	`CREATE TABLE IF NOT EXISTS vibe_workspace_files (
		tenant TEXT NOT NULL,
		session_id TEXT NOT NULL,
		path TEXT NOT NULL,
		content TEXT NOT NULL,
		updated_at TIMESTAMPTZ NOT NULL,
		PRIMARY KEY (tenant, session_id, path)
	)`,
	`CREATE TABLE IF NOT EXISTS vibe_session_events (
		tenant TEXT NOT NULL,
		session_id TEXT NOT NULL,
		ordinal BIGINT NOT NULL,
		submission_id TEXT NOT NULL DEFAULT '',
		event_type TEXT NOT NULL,
		at TIMESTAMPTZ NOT NULL,
		data JSONB,
		PRIMARY KEY (tenant, session_id, ordinal)
	)`,
}

// EnsureSchema applies the idempotent DDL.
func (p *PostgresStore) EnsureSchema(ctx context.Context) error {
	for _, stmt := range vibeSchema {
		if _, err := p.db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("ensure schema: %w", err)
		}
	}
	return nil
}

func (p *PostgresStore) CreateSession(ctx context.Context, scope Scope, id, preview string, now time.Time) error {
	if err := scope.validate(); err != nil {
		return err
	}
	res, err := p.db.ExecContext(ctx,
		`INSERT INTO vibe_sessions (tenant, id, preview, phase, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $5)
		 ON CONFLICT (tenant, id) DO NOTHING`,
		scope.Tenant, id, preview, string(session.PhaseIntake), now.UTC())
	if err != nil {
		return fmt.Errorf("create session: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return session.ErrConflict
	}
	return nil
}

func (p *PostgresStore) AppendEvents(ctx context.Context, scope Scope, sessionID string, expectedLast int64, events []session.Event) (int64, error) {
	if err := scope.validate(); err != nil {
		return 0, err
	}
	if len(events) == 0 {
		return expectedLast, nil
	}
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin append: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var exists bool
	if err := tx.QueryRowContext(ctx,
		`SELECT TRUE FROM vibe_sessions WHERE tenant = $1 AND id = $2`,
		scope.Tenant, sessionID).Scan(&exists); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, ErrNotFound
		}
		return 0, fmt.Errorf("append lookup: %w", err)
	}

	var last sql.NullInt64
	if err := tx.QueryRowContext(ctx,
		`SELECT MAX(ordinal) FROM vibe_session_events WHERE tenant = $1 AND session_id = $2`,
		scope.Tenant, sessionID).Scan(&last); err != nil {
		return 0, fmt.Errorf("append max ordinal: %w", err)
	}
	if last.Int64 != expectedLast {
		return 0, ErrOrdinalConflict
	}

	newLast := expectedLast
	for i := range events {
		e := events[i]
		ordinal := expectedLast + int64(i) + 1
		var data any
		if len(e.Data) > 0 {
			data = string(e.Data)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO vibe_session_events (tenant, session_id, ordinal, submission_id, event_type, at, data)
			 VALUES ($1, $2, $3, $4, $5, $6, $7)`,
			scope.Tenant, sessionID, ordinal, e.SubmissionID, string(e.Type), e.At.UTC(), data); err != nil {
			// A concurrent appender that won the race trips the PK.
			return 0, ErrOrdinalConflict
		}
		newLast = ordinal
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit append: %w", err)
	}
	return newLast, nil
}

func (p *PostgresStore) ListEvents(ctx context.Context, scope Scope, sessionID string, after int64, limit int) ([]session.Event, error) {
	if err := scope.validate(); err != nil {
		return nil, err
	}
	var exists bool
	if err := p.db.QueryRowContext(ctx,
		`SELECT TRUE FROM vibe_sessions WHERE tenant = $1 AND id = $2`,
		scope.Tenant, sessionID).Scan(&exists); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("list events lookup: %w", err)
	}
	q := `SELECT ordinal, submission_id, event_type, at, data
	      FROM vibe_session_events
	      WHERE tenant = $1 AND session_id = $2 AND ordinal > $3
	      ORDER BY ordinal ASC`
	args := []any{scope.Tenant, sessionID, after}
	if limit > 0 {
		q += ` LIMIT $4`
		args = append(args, limit)
	}
	rows, err := p.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list events: %w", err)
	}
	defer rows.Close()
	var out []session.Event
	for rows.Next() {
		var (
			e    session.Event
			data sql.NullString
		)
		e.SessionID = sessionID
		if err := rows.Scan(&e.Ordinal, &e.SubmissionID, &e.Type, &e.At, &data); err != nil {
			return nil, fmt.Errorf("scan event: %w", err)
		}
		if data.Valid {
			e.Data = json.RawMessage(data.String)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (p *PostgresStore) ListSessions(ctx context.Context, scope Scope, limit int) ([]SessionRecord, error) {
	if err := scope.validate(); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 100
	}
	rows, err := p.db.QueryContext(ctx,
		`SELECT id, preview, phase, created_at, updated_at
		 FROM vibe_sessions WHERE tenant = $1
		 ORDER BY updated_at DESC LIMIT $2`,
		scope.Tenant, limit)
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	defer rows.Close()
	var out []SessionRecord
	for rows.Next() {
		var r SessionRecord
		if err := rows.Scan(&r.ID, &r.Preview, &r.Phase, &r.CreatedAt, &r.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan session: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (p *PostgresStore) PutWorkspaceFiles(ctx context.Context, scope Scope, sessionID string, files []WorkspaceFile, now time.Time) error {
	if err := scope.validate(); err != nil {
		return err
	}
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin put files: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	for _, f := range files {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO vibe_workspace_files (tenant, session_id, path, content, updated_at)
			 VALUES ($1, $2, $3, $4, $5)
			 ON CONFLICT (tenant, session_id, path) DO UPDATE SET content = $4, updated_at = $5`,
			scope.Tenant, sessionID, f.Path, f.Content, now.UTC()); err != nil {
			return fmt.Errorf("put file %s: %w", f.Path, err)
		}
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE vibe_sessions SET workspace_revision = workspace_revision + 1 WHERE tenant = $1 AND id = $2`,
		scope.Tenant, sessionID); err != nil {
		return fmt.Errorf("bump workspace revision: %w", err)
	}
	return tx.Commit()
}

func (p *PostgresStore) GetWorkspaceFile(ctx context.Context, scope Scope, sessionID, path string) (WorkspaceFile, error) {
	if err := scope.validate(); err != nil {
		return WorkspaceFile{}, err
	}
	var f WorkspaceFile
	f.Path = path
	err := p.db.QueryRowContext(ctx,
		`SELECT content FROM vibe_workspace_files WHERE tenant = $1 AND session_id = $2 AND path = $3`,
		scope.Tenant, sessionID, path).Scan(&f.Content)
	if errors.Is(err, sql.ErrNoRows) {
		return WorkspaceFile{}, ErrNotFound
	}
	if err != nil {
		return WorkspaceFile{}, fmt.Errorf("get file: %w", err)
	}
	return f, nil
}

func (p *PostgresStore) ListWorkspaceFiles(ctx context.Context, scope Scope, sessionID string) ([]string, error) {
	if err := scope.validate(); err != nil {
		return nil, err
	}
	rows, err := p.db.QueryContext(ctx,
		`SELECT path FROM vibe_workspace_files WHERE tenant = $1 AND session_id = $2 ORDER BY path`,
		scope.Tenant, sessionID)
	if err != nil {
		return nil, fmt.Errorf("list files: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (p *PostgresStore) ListWorkspaceContents(ctx context.Context, scope Scope, sessionID string) ([]WorkspaceFile, error) {
	if err := scope.validate(); err != nil {
		return nil, err
	}
	rows, err := p.db.QueryContext(ctx,
		`SELECT path, content FROM vibe_workspace_files WHERE tenant = $1 AND session_id = $2 ORDER BY path`,
		scope.Tenant, sessionID)
	if err != nil {
		return nil, fmt.Errorf("list contents: %w", err)
	}
	defer rows.Close()
	var out []WorkspaceFile
	for rows.Next() {
		var f WorkspaceFile
		if err := rows.Scan(&f.Path, &f.Content); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

func (p *PostgresStore) DeleteWorkspaceFile(ctx context.Context, scope Scope, sessionID, path string) error {
	if err := scope.validate(); err != nil {
		return err
	}
	if _, err := p.db.ExecContext(ctx,
		`DELETE FROM vibe_workspace_files WHERE tenant = $1 AND session_id = $2 AND path = $3`,
		scope.Tenant, sessionID, path); err != nil {
		return err
	}
	_, err := p.db.ExecContext(ctx,
		`UPDATE vibe_sessions SET workspace_revision = workspace_revision + 1 WHERE tenant = $1 AND id = $2`,
		scope.Tenant, sessionID)
	return err
}

func (p *PostgresStore) WorkspaceRevision(ctx context.Context, scope Scope, sessionID string) (int64, error) {
	if err := scope.validate(); err != nil {
		return 0, err
	}
	var rev int64
	err := p.db.QueryRowContext(ctx,
		`SELECT workspace_revision FROM vibe_sessions WHERE tenant = $1 AND id = $2`,
		scope.Tenant, sessionID).Scan(&rev)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	return rev, err
}

func (p *PostgresStore) PurgeSession(ctx context.Context, scope Scope, sessionID string) error {
	if err := scope.validate(); err != nil {
		return err
	}
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin purge: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	for _, stmt := range []string{
		`DELETE FROM vibe_session_events WHERE tenant = $1 AND session_id = $2`,
		`DELETE FROM vibe_workspace_files WHERE tenant = $1 AND session_id = $2`,
		`DELETE FROM vibe_sessions WHERE tenant = $1 AND id = $2`,
	} {
		if _, err := tx.ExecContext(ctx, stmt, scope.Tenant, sessionID); err != nil {
			return fmt.Errorf("purge session: %w", err)
		}
	}
	return tx.Commit()
}

func (p *PostgresStore) TouchSession(ctx context.Context, scope Scope, sessionID string, phase session.Phase, now time.Time) error {
	if err := scope.validate(); err != nil {
		return err
	}
	res, err := p.db.ExecContext(ctx,
		`UPDATE vibe_sessions SET phase = $3, updated_at = $4 WHERE tenant = $1 AND id = $2`,
		scope.Tenant, sessionID, string(phase), now.UTC())
	if err != nil {
		return fmt.Errorf("touch session: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
