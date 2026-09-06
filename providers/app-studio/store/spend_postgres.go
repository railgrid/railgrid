/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

const organizationSpendSchemaVersion = "organization-spend-v1"

// organizationSpendSchemaStatements creates the per-organization monthly
// spend ledger. One row per (org, calendar month); model calls add to it
// atomically so every replica and every run in the org sees one total.
func organizationSpendSchemaStatements() []string {
	return []string{`CREATE TABLE IF NOT EXISTS app_studio_organization_spend (
		org_uuid text NOT NULL,
		period_start timestamptz NOT NULL,
		input_tokens bigint NOT NULL DEFAULT 0,
		output_tokens bigint NOT NULL DEFAULT 0,
		usd_micros bigint NOT NULL DEFAULT 0,
		updated_at timestamptz NOT NULL DEFAULT now(),
		PRIMARY KEY (org_uuid, period_start)
	)`}
}

func (s *PostgresStore) AddOrganizationSpend(ctx context.Context, orgUUID string, at time.Time, delta OrganizationSpendDelta, now time.Time) (OrganizationSpend, error) {
	if s == nil || s.db == nil {
		return OrganizationSpend{}, fmt.Errorf("postgres store is nil")
	}
	orgUUID, periodStart, err := normalizeOrganizationSpendRequest(orgUUID, at, delta)
	if err != nil {
		return OrganizationSpend{}, err
	}
	if now.IsZero() {
		now = time.Now()
	}
	spend := OrganizationSpend{OrgUUID: orgUUID, PeriodStart: periodStart}
	err = s.db.QueryRowContext(ctx, `
		INSERT INTO app_studio_organization_spend (org_uuid, period_start, input_tokens, output_tokens, usd_micros, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (org_uuid, period_start) DO UPDATE SET
			input_tokens = app_studio_organization_spend.input_tokens + EXCLUDED.input_tokens,
			output_tokens = app_studio_organization_spend.output_tokens + EXCLUDED.output_tokens,
			usd_micros = app_studio_organization_spend.usd_micros + EXCLUDED.usd_micros,
			updated_at = EXCLUDED.updated_at
		RETURNING input_tokens, output_tokens, usd_micros, updated_at`,
		orgUUID, periodStart, delta.InputTokens, delta.OutputTokens, delta.USDMicros, now.UTC(),
	).Scan(&spend.InputTokens, &spend.OutputTokens, &spend.USDMicros, &spend.UpdatedAt)
	if err != nil {
		return OrganizationSpend{}, fmt.Errorf("add organization spend: %w", err)
	}
	spend.UpdatedAt = spend.UpdatedAt.UTC()
	return spend, nil
}

func (s *PostgresStore) GetOrganizationSpend(ctx context.Context, orgUUID string, at time.Time) (OrganizationSpend, error) {
	if s == nil || s.db == nil {
		return OrganizationSpend{}, fmt.Errorf("postgres store is nil")
	}
	orgUUID, periodStart, err := normalizeOrganizationSpendRequest(orgUUID, at, OrganizationSpendDelta{})
	if err != nil {
		return OrganizationSpend{}, err
	}
	spend := OrganizationSpend{OrgUUID: orgUUID, PeriodStart: periodStart}
	err = s.db.QueryRowContext(ctx, `SELECT input_tokens, output_tokens, usd_micros, updated_at
		FROM app_studio_organization_spend
		WHERE org_uuid=$1 AND period_start=$2`,
		orgUUID, periodStart,
	).Scan(&spend.InputTokens, &spend.OutputTokens, &spend.USDMicros, &spend.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return spend, nil
	}
	if err != nil {
		return OrganizationSpend{}, fmt.Errorf("get organization spend: %w", err)
	}
	spend.UpdatedAt = spend.UpdatedAt.UTC()
	return spend, nil
}
