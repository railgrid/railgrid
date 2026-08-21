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
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type PostgresStore struct {
	db *pgxpool.Pool
}

func NewPostgresStore(db *pgxpool.Pool) *PostgresStore { return &PostgresStore{db: db} }

func (s *PostgresStore) Ping(ctx context.Context) error { return s.db.Ping(ctx) }

func (s *PostgresStore) Insert(ctx context.Context, events []Event) (IngestStats, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return IngestStats{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var stats IngestStats
	for _, event := range events {
		typeName, ok := normalizeEventType(event.Type)
		if !ok {
			return IngestStats{}, fmt.Errorf("%w: event type %q is not declared", ErrInvalidEvent, event.Type)
		}
		event.Type = typeName
		receivedAt := event.ReceivedAt
		if receivedAt.IsZero() {
			receivedAt = time.Now().UTC()
		}
		result, err := tx.Exec(ctx, `
			INSERT INTO faros_telemetry_events
				(tenant_id, source, event_id, event_type, subject, event_time, received_at, data_content_type, data)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9::jsonb)
			ON CONFLICT (tenant_id, source, event_id) DO NOTHING`,
			event.Tenant, event.Source, event.ID, event.Type, event.Subject, event.Time, receivedAt, event.DataContentType, event.Data)
		if err != nil {
			return IngestStats{}, fmt.Errorf("insert telemetry event: %w", err)
		}
		rows := result.RowsAffected()
		if rows == 0 {
			stats.Duplicates++
			continue
		}
		bucket := receivedAt.UTC().Truncate(defaultBucket)
		if _, err := tx.Exec(ctx, `
			INSERT INTO faros_telemetry_aggregates (bucket_start, source, event_type, event_count)
			VALUES ($1, $2, $3, 1)
			ON CONFLICT (bucket_start, source, event_type)
			DO UPDATE SET event_count = faros_telemetry_aggregates.event_count + 1`, bucket, aggregateComponent, event.Type); err != nil {
			return IngestStats{}, fmt.Errorf("update telemetry aggregate: %w", err)
		}
		stats.Accepted++
	}
	if err := tx.Commit(ctx); err != nil {
		return IngestStats{}, err
	}
	return stats, nil
}

func (s *PostgresStore) EraseTenant(ctx context.Context, request ErasureRequest) (ErasureResult, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return ErasureResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, request.RequestID); err != nil {
		return ErasureResult{}, fmt.Errorf("lock telemetry erasure request: %w", err)
	}
	var result ErasureResult
	err = tx.QueryRow(ctx, `SELECT request_id, tenant_id, deleted_raw FROM faros_telemetry_erasure_requests WHERE request_id = $1 FOR UPDATE`, request.RequestID).Scan(&result.RequestID, &result.TenantID, &result.DeletedRaw)
	if err == nil {
		if result.TenantID != request.TenantID {
			return ErasureResult{}, ErrErasureConflict
		}
		result.Existing = true
		if err := tx.Commit(ctx); err != nil {
			return ErasureResult{}, err
		}
		return result, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return ErasureResult{}, fmt.Errorf("check telemetry erasure request: %w", err)
	}
	if err := tx.QueryRow(ctx, `WITH deleted AS (DELETE FROM faros_telemetry_events WHERE tenant_id = $1 RETURNING 1) SELECT COUNT(*) FROM deleted`, request.TenantID).Scan(&result.DeletedRaw); err != nil {
		return ErasureResult{}, fmt.Errorf("delete telemetry events: %w", err)
	}
	result.RequestID = request.RequestID
	result.TenantID = request.TenantID
	if _, err := tx.Exec(ctx, `INSERT INTO faros_telemetry_erasure_requests (request_id, tenant_id, created_at, deleted_raw) VALUES ($1, $2, NOW(), $3)`, request.RequestID, request.TenantID, result.DeletedRaw); err != nil {
		return ErasureResult{}, fmt.Errorf("record telemetry erasure: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return ErasureResult{}, err
	}
	return result, nil
}

func (s *PostgresStore) PurgeExpired(ctx context.Context, now time.Time, rawRetention, aggregateRetention time.Duration) (PurgeResult, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return PurgeResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var result PurgeResult
	if err := tx.QueryRow(ctx, `WITH deleted AS (DELETE FROM faros_telemetry_events WHERE received_at < $1 RETURNING 1) SELECT COUNT(*) FROM deleted`, now.Add(-rawRetention)).Scan(&result.DeletedRaw); err != nil {
		return PurgeResult{}, fmt.Errorf("purge telemetry events: %w", err)
	}
	if err := tx.QueryRow(ctx, `WITH deleted AS (DELETE FROM faros_telemetry_aggregates WHERE bucket_start < $1 RETURNING 1) SELECT COUNT(*) FROM deleted`, now.Add(-aggregateRetention)).Scan(&result.DeletedAggregate); err != nil {
		return PurgeResult{}, fmt.Errorf("purge telemetry aggregates: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return PurgeResult{}, err
	}
	return result, nil
}
