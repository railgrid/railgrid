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
	"errors"
	"strings"
	"time"
)

// OrganizationSpend is the accumulated App Studio model usage for one
// organization inside one accounting period. USDMicros is the estimated cost
// in millionths of a USD, mirroring the Agents provider's usd_micros column.
type OrganizationSpend struct {
	OrgUUID      string    `json:"orgUUID"`
	PeriodStart  time.Time `json:"periodStart"`
	InputTokens  int64     `json:"inputTokens"`
	OutputTokens int64     `json:"outputTokens"`
	USDMicros    int64     `json:"usdMicros"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

// OrganizationSpendDelta is one model call's contribution to the period.
type OrganizationSpendDelta struct {
	InputTokens  int64
	OutputTokens int64
	USDMicros    int64
}

// OrganizationSpendStore accumulates per-organization model spend so the
// assistant can enforce a monthly USD cap before each model call.
//
// Both methods accept the at timestamp, which may be any instant within the
// accounting period — typically just time.Now() — rather than a pre-normalized
// boundary. The store normalizes it to the period start, the containing UTC
// calendar month per OrganizationSpendPeriodStart, so every instant in one
// month addresses one bucket and callers never have to agree on how the
// boundary is computed. The returned OrganizationSpend carries the resolved
// PeriodStart.
type OrganizationSpendStore interface {
	// AddOrganizationSpend atomically adds delta to the bucket containing at
	// and returns the updated totals.
	AddOrganizationSpend(ctx context.Context, orgUUID string, at time.Time, delta OrganizationSpendDelta, now time.Time) (OrganizationSpend, error)
	// GetOrganizationSpend returns the totals for the bucket containing at. A
	// period with no recorded usage returns zero totals, not an error.
	GetOrganizationSpend(ctx context.Context, orgUUID string, at time.Time) (OrganizationSpend, error)
}

// Spend totals are numeric counters, never message content, so the encrypting
// wrapper passes them through unchanged.
func (e *encryptedStore) AddOrganizationSpend(ctx context.Context, orgUUID string, at time.Time, delta OrganizationSpendDelta, now time.Time) (OrganizationSpend, error) {
	return e.inner.AddOrganizationSpend(ctx, orgUUID, at, delta, now)
}

func (e *encryptedStore) GetOrganizationSpend(ctx context.Context, orgUUID string, at time.Time) (OrganizationSpend, error) {
	return e.inner.GetOrganizationSpend(ctx, orgUUID, at)
}

// OrganizationSpendPeriodStart returns the UTC calendar-month bucket that
// contains at.
func OrganizationSpendPeriodStart(at time.Time) time.Time {
	at = at.UTC()
	return time.Date(at.Year(), at.Month(), 1, 0, 0, 0, 0, time.UTC)
}

// normalizeOrganizationSpendRequest validates one spend call and resolves at
// to its UTC calendar-month bucket.
func normalizeOrganizationSpendRequest(orgUUID string, at time.Time, delta OrganizationSpendDelta) (string, time.Time, error) {
	orgUUID = strings.TrimSpace(orgUUID)
	if orgUUID == "" {
		return "", time.Time{}, errors.New("organization spend org uuid is required")
	}
	if at.IsZero() {
		return "", time.Time{}, errors.New("organization spend period is required")
	}
	if delta.InputTokens < 0 || delta.OutputTokens < 0 || delta.USDMicros < 0 {
		return "", time.Time{}, errors.New("organization spend delta must not be negative")
	}
	return orgUUID, OrganizationSpendPeriodStart(at), nil
}
