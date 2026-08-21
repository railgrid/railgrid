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
	db   *pgxpool.Pool
	plan ProjectionPlan
}

func NewPostgresStore(db *pgxpool.Pool) *PostgresStore {
	plan, err := GeneratedProjectionPlan()
	if err != nil {
		panic(err)
	}
	return &PostgresStore{db: db, plan: plan}
}

func (s *PostgresStore) Ping(ctx context.Context) error { return s.db.Ping(ctx) }

// SyncCatalog exposes catalog metadata to stable SQL views without embedding
// hand-authored per-event logic in the receiver or migrations.
func (s *PostgresStore) SyncCatalog(ctx context.Context) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	for _, rule := range s.plan.CatalogRows() {
		if _, err := tx.Exec(ctx, `
			INSERT INTO faros_telemetry_metric_catalog
				(metric_key, metric_kind, event_type, funnel_step, step_order, window_days)
			VALUES ($1, $2, $3, $4, $5, $6)
			ON CONFLICT (metric_key, funnel_step, event_type) DO UPDATE SET
				metric_kind = EXCLUDED.metric_kind,
				step_order = EXCLUDED.step_order,
				window_days = EXCLUDED.window_days`,
			rule.MetricKey, rule.MetricKind, rule.EventType, rule.FunnelStep, rule.StepOrder, rule.WindowDays); err != nil {
			return fmt.Errorf("sync telemetry metric catalog: %w", err)
		}
	}
	return tx.Commit(ctx)
}

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
		event.ReceivedAt = receivedAt
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
		projections, err := s.plan.Project(event)
		if err != nil {
			return IngestStats{}, fmt.Errorf("project telemetry event: %w", err)
		}
		for _, projection := range projections {
			increment := true
			if projection.UniqueHash != "" {
				uniqueResult, err := tx.Exec(ctx, `
					INSERT INTO faros_telemetry_metric_uniques
						(bucket_start, metric_key, event_type, funnel_step, labels_key, labels, tenant_id, unique_kind, unique_hash, created_at)
					VALUES ($1::date, $2, $3, $4, $5, $6::jsonb, $7, $8, $9, $10)
					ON CONFLICT DO NOTHING`, projection.BucketStart, projection.MetricKey, projection.EventType, projection.FunnelStep, projection.LabelsKey, projection.Labels, event.Tenant, projection.UniqueKind, projection.UniqueHash, receivedAt)
				if err != nil {
					return IngestStats{}, fmt.Errorf("insert telemetry metric unique: %w", err)
				}
				increment = uniqueResult.RowsAffected() == 1
			}
			if increment {
				if _, err := tx.Exec(ctx, `
					INSERT INTO faros_telemetry_metric_aggregates
						(bucket_start, metric_key, funnel_step, labels_key, labels, value)
					VALUES ($1::date, $2, $3, $4, $5::jsonb, 1)
					ON CONFLICT (bucket_start, metric_key, funnel_step, labels_key)
					DO UPDATE SET value = faros_telemetry_metric_aggregates.value + 1`, projection.BucketStart, projection.MetricKey, projection.FunnelStep, projection.LabelsKey, projection.Labels); err != nil {
					return IngestStats{}, fmt.Errorf("update telemetry metric aggregate: %w", err)
				}
			}
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
	if err := tx.QueryRow(ctx, `
		WITH deleted_events AS (DELETE FROM faros_telemetry_events WHERE tenant_id = $1 RETURNING 1),
		deleted_uniques AS (DELETE FROM faros_telemetry_metric_uniques WHERE tenant_id = $1 RETURNING 1)
		SELECT (SELECT COUNT(*) FROM deleted_events) + (SELECT COUNT(*) FROM deleted_uniques)`, request.TenantID).Scan(&result.DeletedRaw); err != nil {
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
	for _, retention := range catalogRetentions(rawRetention) {
		var deleted int64
		if err := tx.QueryRow(ctx, `
			WITH deleted_events AS (DELETE FROM faros_telemetry_events WHERE event_type = $1 AND received_at < $2 RETURNING 1),
			deleted_uniques AS (DELETE FROM faros_telemetry_metric_uniques WHERE event_type = $1 AND created_at < $2 RETURNING 1)
			SELECT (SELECT COUNT(*) FROM deleted_events) + (SELECT COUNT(*) FROM deleted_uniques)`, retention.Action, now.Add(-retention.Retention)).Scan(&deleted); err != nil {
			return PurgeResult{}, fmt.Errorf("purge telemetry event %s: %w", retention.Action, err)
		}
		result.DeletedRaw += deleted
	}
	var deletedReceipts int64
	if err := tx.QueryRow(ctx, `WITH deleted AS (DELETE FROM faros_telemetry_erasure_requests WHERE created_at < $1 RETURNING 1) SELECT COUNT(*) FROM deleted`, now.Add(-boundedRawRetention(rawRetention))).Scan(&deletedReceipts); err != nil {
		return PurgeResult{}, fmt.Errorf("purge telemetry erasure receipts: %w", err)
	}
	result.DeletedRaw += deletedReceipts
	if err := tx.QueryRow(ctx, `WITH deleted AS (DELETE FROM faros_telemetry_aggregates WHERE bucket_start < $1 RETURNING 1) SELECT COUNT(*) FROM deleted`, now.Add(-aggregateRetention)).Scan(&result.DeletedAggregate); err != nil {
		return PurgeResult{}, fmt.Errorf("purge telemetry aggregates: %w", err)
	}
	var deletedMetrics int64
	if err := tx.QueryRow(ctx, `WITH deleted AS (DELETE FROM faros_telemetry_metric_aggregates WHERE bucket_start < $1::date RETURNING 1) SELECT COUNT(*) FROM deleted`, now.Add(-aggregateRetention)).Scan(&deletedMetrics); err != nil {
		return PurgeResult{}, fmt.Errorf("purge telemetry metric aggregates: %w", err)
	}
	result.DeletedAggregate += deletedMetrics
	if err := tx.Commit(ctx); err != nil {
		return PurgeResult{}, err
	}
	return result, nil
}
