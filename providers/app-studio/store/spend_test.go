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
	"os"
	"strings"
	"testing"
	"time"

	"github.com/lib/pq"
)

func TestOrganizationSpendPeriodStartIsUTCMonth(t *testing.T) {
	loc := time.FixedZone("plus-ten", 10*60*60)
	at := time.Date(2026, 9, 1, 3, 30, 0, 0, loc) // 2026-08-31T17:30Z
	got := OrganizationSpendPeriodStart(at)
	want := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	if !got.Equal(want) || got.Location() != time.UTC {
		t.Fatalf("period start = %s, want %s", got, want)
	}
}

func testOrganizationSpendAccounting(t *testing.T, s OrganizationSpendStore) {
	t.Helper()
	ctx := context.Background()
	september := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	october := time.Date(2026, 10, 2, 8, 0, 0, 0, time.UTC)

	empty, err := s.GetOrganizationSpend(ctx, "org-a", september)
	if err != nil {
		t.Fatalf("GetOrganizationSpend empty: %v", err)
	}
	if empty.USDMicros != 0 || empty.InputTokens != 0 || empty.OutputTokens != 0 || empty.OrgUUID != "org-a" {
		t.Fatalf("empty period = %#v, want zero totals", empty)
	}

	first, err := s.AddOrganizationSpend(ctx, "org-a", september, OrganizationSpendDelta{InputTokens: 1000, OutputTokens: 200, USDMicros: 5_500}, september)
	if err != nil {
		t.Fatalf("AddOrganizationSpend first: %v", err)
	}
	if first.USDMicros != 5_500 || first.InputTokens != 1000 || first.OutputTokens != 200 {
		t.Fatalf("first add = %#v", first)
	}
	second, err := s.AddOrganizationSpend(ctx, "org-a", september.Add(48*time.Hour), OrganizationSpendDelta{InputTokens: 10, OutputTokens: 20, USDMicros: 4_500}, september.Add(48*time.Hour))
	if err != nil {
		t.Fatalf("AddOrganizationSpend second: %v", err)
	}
	if second.USDMicros != 10_000 || second.InputTokens != 1010 || second.OutputTokens != 220 {
		t.Fatalf("second add did not accumulate: %#v", second)
	}
	if !second.PeriodStart.Equal(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("period start = %s, want 2026-09-01", second.PeriodStart)
	}

	got, err := s.GetOrganizationSpend(ctx, "org-a", september.Add(72*time.Hour))
	if err != nil {
		t.Fatalf("GetOrganizationSpend: %v", err)
	}
	if got.USDMicros != 10_000 || got.InputTokens != 1010 || got.OutputTokens != 220 {
		t.Fatalf("read back = %#v, want accumulated totals", got)
	}

	// Another month and another organization are separate buckets.
	nextMonth, err := s.GetOrganizationSpend(ctx, "org-a", october)
	if err != nil || nextMonth.USDMicros != 0 {
		t.Fatalf("next month = %#v, %v; want zero", nextMonth, err)
	}
	otherOrg, err := s.GetOrganizationSpend(ctx, "org-b", september)
	if err != nil || otherOrg.USDMicros != 0 {
		t.Fatalf("other org = %#v, %v; want zero", otherOrg, err)
	}

	if _, err := s.AddOrganizationSpend(ctx, "", september, OrganizationSpendDelta{USDMicros: 1}, september); err == nil {
		t.Fatal("AddOrganizationSpend accepted an empty org")
	}
	if _, err := s.AddOrganizationSpend(ctx, "org-a", september, OrganizationSpendDelta{USDMicros: -1}, september); err == nil {
		t.Fatal("AddOrganizationSpend accepted a negative delta")
	}
}

func TestMemoryStoreOrganizationSpendAccounting(t *testing.T) {
	testOrganizationSpendAccounting(t, NewMemoryStore())
}

// The spend API takes any instant inside the accounting period, not a
// pre-normalized month boundary: every instant in the same UTC calendar month
// addresses one bucket, and the boundary is UTC regardless of the caller's
// zone.
func TestOrganizationSpendAcceptsAnyInstantInThePeriod(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryStore()
	monthStart := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	midMonth := time.Date(2026, 9, 17, 13, 45, 12, 0, time.UTC)
	// 2026-09-01T01:00+03:00 is 2026-08-31T22:00Z, so it belongs to August.
	previousMonthInLocalZone := time.Date(2026, 9, 1, 1, 0, 0, 0, time.FixedZone("plus-three", 3*60*60))

	if _, err := s.AddOrganizationSpend(ctx, "org-a", monthStart, OrganizationSpendDelta{USDMicros: 100}, monthStart); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddOrganizationSpend(ctx, "org-a", midMonth, OrganizationSpendDelta{USDMicros: 25}, midMonth); err != nil {
		t.Fatal(err)
	}
	for _, at := range []time.Time{monthStart, midMonth, time.Date(2026, 9, 30, 23, 59, 59, 0, time.UTC)} {
		got, err := s.GetOrganizationSpend(ctx, "org-a", at)
		if err != nil {
			t.Fatal(err)
		}
		if got.USDMicros != 125 {
			t.Fatalf("spend read at %s = %d micros, want 125 from the one September bucket", at, got.USDMicros)
		}
		if !got.PeriodStart.Equal(monthStart) {
			t.Fatalf("period start for %s = %s, want %s", at, got.PeriodStart, monthStart)
		}
	}

	august, err := s.GetOrganizationSpend(ctx, "org-a", previousMonthInLocalZone)
	if err != nil {
		t.Fatal(err)
	}
	if august.USDMicros != 0 {
		t.Fatalf("august spend = %d micros, want 0; the month boundary must be UTC", august.USDMicros)
	}
}

func TestEncryptedStoreOrganizationSpendPassesThrough(t *testing.T) {
	inner := NewMemoryStore()
	wrapped, err := NewEncryptedStore(inner, []EncryptionKey{{ID: "k1", Value: make([]byte, 32)}})
	if err != nil {
		t.Fatal(err)
	}
	testOrganizationSpendAccounting(t, wrapped)
	got, err := inner.GetOrganizationSpend(context.Background(), "org-a", time.Date(2026, 9, 4, 0, 0, 0, 0, time.UTC))
	if err != nil || got.USDMicros != 10_000 {
		t.Fatalf("inner spend = %#v, %v; want the wrapped writes", got, err)
	}
}

func TestPostgresStoreOrganizationSpendAccountingExternalDSN(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("APP_STUDIO_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("APP_STUDIO_TEST_POSTGRES_DSN is not set")
	}
	ctx := context.Background()
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("open postgres: %v", err)
	}
	defer db.Close()

	schemaName := "app_studio_spend_" + time.Now().UTC().Format("20060102150405")
	if _, err := db.ExecContext(ctx, "CREATE SCHEMA "+pq.QuoteIdentifier(schemaName)); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() {
		cleanupDB, openErr := sql.Open("postgres", dsn)
		if openErr != nil {
			return
		}
		defer cleanupDB.Close()
		_, _ = cleanupDB.ExecContext(context.Background(), "DROP SCHEMA "+pq.QuoteIdentifier(schemaName)+" CASCADE")
	})

	s, err := OpenPostgres(ctx, postgresDSNWithSearchPath(t, dsn, schemaName))
	if err != nil {
		t.Fatalf("OpenPostgres: %v", err)
	}
	defer s.Close()
	// EnsureSchema must be idempotent for the new migration.
	if err := s.EnsureSchema(ctx); err != nil {
		t.Fatalf("EnsureSchema second run: %v", err)
	}
	testOrganizationSpendAccounting(t, s)
}
