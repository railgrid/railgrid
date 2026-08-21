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
	"reflect"
	"testing"
	"time"

	"github.com/faroshq/faros/telemetry/catalog"
	"github.com/faroshq/faros/telemetry/generated"
)

func TestGeneratedProjectionPlanIsDeterministicAndAppliesFiltersLabelsUnique(t *testing.T) {
	first, err := GeneratedProjectionPlan()
	if err != nil {
		t.Fatal(err)
	}
	second, err := GeneratedProjectionPlan()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first.CatalogRows(), second.CatalogRows()) {
		t.Fatal("generated projection plan is not deterministic")
	}
	event := receiverTestEvent(generated.ActionEdgeFirstReady, "tenant-a", time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC), map[string]interface{}{"edge_type": "linux_server", "outcome": "ready"})
	event.ReceivedAt = time.Date(2026, 8, 22, 1, 0, 0, 0, time.UTC)
	projections, err := first.Project(event)
	if err != nil {
		t.Fatal(err)
	}
	if len(projections) != 2 {
		t.Fatalf("ready projections = %d, want counter and funnel", len(projections))
	}
	if projections[0].BucketStart != "2026-08-22" {
		t.Fatalf("bucket = %q, want trusted receiver date", projections[0].BucketStart)
	}
	if projections[0].MetricKey != "activation_funnel" || projections[0].FunnelStep != generated.ActionEdgeFirstReady || projections[0].UniqueKind != "workspace" {
		t.Fatalf("funnel projection = %+v", projections[0])
	}
	if projections[1].MetricKey != "edge_first_ready_total" || string(projections[1].Labels) != `{"edge_type":"linux_server","outcome":"ready"}` || projections[1].UniqueKind != "workspace" {
		t.Fatalf("counter projection = %+v", projections[1])
	}
	promoted := receiverTestEvent(generated.ActionAppStudioProjectPublished, "tenant-a", event.Time, map[string]interface{}{"outcome": "promoted"})
	promoted.ReceivedAt = event.ReceivedAt
	projections, err = first.Project(promoted)
	if err != nil {
		t.Fatal(err)
	}
	if len(projections) != 1 || projections[0].MetricKey != "app_studio_project_published_total" {
		t.Fatalf("filter-mismatch projections = %+v, want counter only", projections)
	}
}

func TestProjectionPlanPreservesEveryCounterEventSelection(t *testing.T) {
	metric := catalog.MetricDefinition{
		KeyPath: "multi_event_total", MetricKind: "counter", Status: "active", TimeFrame: "all",
		Events: []catalog.EventSelection{{Name: "event_one", Unique: "resource"}, {Name: "event_two", Unique: "resource"}},
	}
	plan, err := BuildProjectionPlan([]catalog.MetricDefinition{metric})
	if err != nil {
		t.Fatal(err)
	}
	rows := plan.CatalogRows()
	if len(rows) != 2 || rows[0].EventType != "event_one" || rows[1].EventType != "event_two" {
		t.Fatalf("catalog rows = %+v, want both counter event selections", rows)
	}
	if rows[0].MetricKey != rows[1].MetricKey || rows[0].FunnelStep != "" || rows[1].FunnelStep != "" {
		t.Fatalf("counter selection identities = %+v", rows)
	}
}

func TestMemoryProjectionDedupErasureAndRetention(t *testing.T) {
	store := NewMemoryStore()
	now := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	event := receiverTestEvent(generated.ActionOrganizationCreated, "tenant-a", now, map[string]interface{}{"outcome": "success"})
	duplicateSubject := event
	duplicateSubject.ID = "different-cloud-event-id"
	if stats, err := store.Insert(nil, []Event{event, duplicateSubject}); err != nil || stats.Accepted != 2 {
		t.Fatalf("insert = %+v, %v", stats, err)
	}
	aggregates, uniques := store.ProjectionCounts()
	if aggregates != 1 || uniques != 1 {
		t.Fatalf("projection counts = %d aggregates, %d uniques, want 1 and 1", aggregates, uniques)
	}
	if _, err := store.EraseTenant(nil, ErasureRequest{RequestID: "erase", TenantID: "tenant-a"}); err != nil {
		t.Fatal(err)
	}
	aggregates, uniques = store.ProjectionCounts()
	if aggregates != 1 || uniques != 0 {
		t.Fatalf("after erasure = %d aggregates, %d uniques", aggregates, uniques)
	}

	old := receiverTestEvent(generated.ActionOrganizationCreated, "tenant-b", now.Add(-91*24*time.Hour), map[string]interface{}{"outcome": "success"})
	if _, err := store.Insert(nil, []Event{old}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PurgeExpired(nil, now, 90*24*time.Hour, 13*30*24*time.Hour); err != nil {
		t.Fatal(err)
	}
	aggregates, uniques = store.ProjectionCounts()
	if aggregates != 2 || uniques != 0 {
		t.Fatalf("after raw purge = %d aggregates, %d uniques", aggregates, uniques)
	}
}
