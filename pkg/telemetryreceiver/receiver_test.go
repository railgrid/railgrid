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
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	cloudevents "github.com/cloudevents/sdk-go/v2"
	"github.com/faroshq/faros/telemetry/generated"
)

func testEvent(id, tenant string) cloudevents.Event {
	definition, ok := generated.LookupEvent(generated.ActionOrganizationCreated)
	if !ok {
		panic("generated organization event missing")
	}
	identifiers := make(map[string]string, len(definition.Identifiers))
	for _, name := range definition.Identifiers {
		identifiers[name] = strings.Repeat("a", 64)
	}
	properties := make(map[string]interface{}, len(definition.AdditionalProperties))
	for name, property := range definition.AdditionalProperties {
		properties[name] = property.Enum[0]
	}
	occurredAt := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	event := cloudevents.NewEvent()
	event.SetID(id)
	event.SetSource("faros://installation/" + strings.TrimSpace(tenant) + "/hub")
	event.SetType(generated.ActionOrganizationCreated)
	event.SetSubject(definition.Owner)
	event.SetTime(occurredAt)
	event.SetExtension("tenant", tenant)
	if err := event.SetData(cloudevents.ApplicationJSON, Record{InstallationID: strings.TrimSpace(tenant), Provider: definition.Owner, Action: definition.Action, OccurredAt: occurredAt, Identifiers: identifiers, Properties: properties}); err != nil {
		panic(err)
	}
	return event
}

func batchRequest(t *testing.T, path, token string, events ...cloudevents.Event) *http.Request {
	t.Helper()
	payload, err := json.Marshal(events)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(payload))
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", CloudEventsBatchContentType)
	return request
}

func testServer(t *testing.T) (*Server, *MemoryStore) {
	t.Helper()
	store := NewMemoryStore()
	server, err := NewServer(store, Config{IngestToken: "ingest-secret-000", AdminToken: "admin-secret-0000"})
	if err != nil {
		t.Fatal(err)
	}
	return server, store
}

func TestParseBatchUsesCloudEventsValidationAndTenantRule(t *testing.T) {
	event := testEvent("event-1", "tenant-a")
	payload, err := json.Marshal([]cloudevents.Event{event})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/events", bytes.NewReader(payload))
	request.Header.Set("Content-Type", CloudEventsBatchContentType)
	events, err := ParseBatch(request, payload, 10, 10*1024)
	if err != nil {
		t.Fatalf("ParseBatch() error = %v", err)
	}
	if got := events[0].Tenant; got != "tenant-a" {
		t.Fatalf("tenant = %q, want tenant-a", got)
	}

	withWhitespace := testEvent("event-whitespace", "  tenant-a  ")
	payload, err = json.Marshal([]cloudevents.Event{withWhitespace})
	if err != nil {
		t.Fatal(err)
	}
	request = httptest.NewRequest(http.MethodPost, "/v1/events", bytes.NewReader(payload))
	request.Header.Set("Content-Type", CloudEventsBatchContentType)
	events, err = ParseBatch(request, payload, 10, 10*1024)
	if err != nil {
		t.Fatalf("ParseBatch() whitespace tenant error = %v", err)
	}
	if got := events[0].Tenant; got != "tenant-a" {
		t.Fatalf("trimmed tenant = %q, want tenant-a", got)
	}

	for _, tenant := range []string{" ", "tenant a"} {
		invalid := testEvent("event-invalid-tenant-"+tenant, tenant)
		payload, err = json.Marshal([]cloudevents.Event{invalid})
		if err != nil {
			t.Fatal(err)
		}
		request = httptest.NewRequest(http.MethodPost, "/v1/events", bytes.NewReader(payload))
		request.Header.Set("Content-Type", CloudEventsBatchContentType)
		if _, err := ParseBatch(request, payload, 10, 10*1024); !errors.Is(err, ErrInvalidEvent) {
			t.Fatalf("ParseBatch() tenant %q error = %v, want ErrInvalidEvent", tenant, err)
		}
	}

	withoutTenant := testEvent("event-2", "")
	withoutTenant.Context.GetExtensions()["tenant"] = nil
	payload, err = json.Marshal([]cloudevents.Event{withoutTenant})
	if err != nil {
		t.Fatal(err)
	}
	request = httptest.NewRequest(http.MethodPost, "/v1/events", bytes.NewReader(payload))
	request.Header.Set("Content-Type", CloudEventsBatchContentType)
	if _, err := ParseBatch(request, payload, 10, 10*1024); !errors.Is(err, ErrInvalidEvent) {
		t.Fatalf("ParseBatch() error = %v, want ErrInvalidEvent", err)
	}
}

func TestParseBatchRejectsUnsupportedSpecVersionAndUndeclaredType(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*cloudevents.Event)
	}{
		{name: "unsupported specversion", mutate: func(event *cloudevents.Event) { event.SetSpecVersion("0.3") }},
		{name: "undeclared type", mutate: func(event *cloudevents.Event) { event.SetType("dev.faros.telemetry.not_declared") }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			event := testEvent("event-"+strings.ReplaceAll(tt.name, " ", "-"), "tenant-a")
			tt.mutate(&event)
			payload, err := json.Marshal([]cloudevents.Event{event})
			if err != nil {
				t.Fatal(err)
			}
			request := httptest.NewRequest(http.MethodPost, "/v1/events", bytes.NewReader(payload))
			request.Header.Set("Content-Type", CloudEventsBatchContentType)
			if _, err := ParseBatch(request, payload, 10, 10*1024); !errors.Is(err, ErrInvalidEvent) {
				t.Fatalf("ParseBatch() error = %v, want ErrInvalidEvent", err)
			}
		})
	}

	event := testEvent("event-prefixed", "tenant-a")
	event.SetType("dev.faros.telemetry." + generated.ActionOrganizationCreated)
	payload, err := json.Marshal([]cloudevents.Event{event})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/events", bytes.NewReader(payload))
	request.Header.Set("Content-Type", CloudEventsBatchContentType)
	events, err := ParseBatch(request, payload, 10, 10*1024)
	if err != nil {
		t.Fatalf("ParseBatch() prefixed type error = %v", err)
	}
	if events[0].Type != generated.ActionOrganizationCreated {
		t.Fatalf("normalized type = %q, want %q", events[0].Type, generated.ActionOrganizationCreated)
	}
}

func TestParseBatchRejectsSpoofedOrNonCatalogRecord(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*cloudevents.Event, *Record)
	}{
		{name: "action mismatch", mutate: func(_ *cloudevents.Event, record *Record) { record.Action = "workspace_created" }},
		{name: "provider mismatch", mutate: func(_ *cloudevents.Event, record *Record) { record.Provider = "edges" }},
		{name: "subject mismatch", mutate: func(event *cloudevents.Event, _ *Record) { event.SetSubject("edges") }},
		{name: "installation mismatch", mutate: func(_ *cloudevents.Event, record *Record) { record.InstallationID = "tenant-b" }},
		{name: "raw identifier", mutate: func(_ *cloudevents.Event, record *Record) { record.Identifiers["org"] = "org-raw" }},
		{name: "undeclared identifier", mutate: func(_ *cloudevents.Event, record *Record) { record.Identifiers["resource"] = strings.Repeat("b", 64) }},
		{name: "missing property", mutate: func(_ *cloudevents.Event, record *Record) { delete(record.Properties, "outcome") }},
		{name: "unbounded property", mutate: func(_ *cloudevents.Event, record *Record) { record.Properties["outcome"] = "anything" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			event := testEvent("invalid-record", "tenant-a")
			var record Record
			if err := json.Unmarshal(event.Data(), &record); err != nil {
				t.Fatal(err)
			}
			tt.mutate(&event, &record)
			if err := event.SetData(cloudevents.ApplicationJSON, record); err != nil {
				t.Fatal(err)
			}
			payload, err := json.Marshal([]cloudevents.Event{event})
			if err != nil {
				t.Fatal(err)
			}
			request := httptest.NewRequest(http.MethodPost, "/v1/events", bytes.NewReader(payload))
			request.Header.Set("Content-Type", CloudEventsBatchContentType)
			if _, err := ParseBatch(request, payload, 10, 10*1024); !errors.Is(err, ErrInvalidEvent) {
				t.Fatalf("ParseBatch() error = %v, want ErrInvalidEvent", err)
			}
		})
	}

	event := testEvent("unknown-field", "tenant-a")
	var raw map[string]interface{}
	if err := json.Unmarshal(event.Data(), &raw); err != nil {
		t.Fatal(err)
	}
	raw["content"] = "must not pass"
	if err := event.SetData(cloudevents.ApplicationJSON, raw); err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal([]cloudevents.Event{event})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/events", bytes.NewReader(payload))
	request.Header.Set("Content-Type", CloudEventsBatchContentType)
	if _, err := ParseBatch(request, payload, 10, 10*1024); !errors.Is(err, ErrInvalidEvent) {
		t.Fatalf("unknown field error = %v, want ErrInvalidEvent", err)
	}
}

func TestIngestAuthDuplicateAndMetrics(t *testing.T) {
	server, store := testServer(t)
	handler := server.Handler()
	event := testEvent("event-1", "tenant-a")

	unauthorized := batchRequest(t, "/v1/events", "wrong", event)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, unauthorized)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d, want %d", response.Code, http.StatusUnauthorized)
	}

	for i := 0; i < 2; i++ {
		response = httptest.NewRecorder()
		handler.ServeHTTP(response, batchRequest(t, "/v1/events", "ingest-secret-000", event))
		if response.Code != http.StatusAccepted {
			t.Fatalf("ingest status = %d, want %d", response.Code, http.StatusAccepted)
		}
	}
	var stats IngestStats
	if err := json.NewDecoder(strings.NewReader(response.Body.String())).Decode(&stats); err != nil {
		t.Fatal(err)
	}
	if stats.Accepted != 0 || stats.Duplicates != 1 {
		t.Fatalf("duplicate response = %+v", stats)
	}
	raw, aggregate := store.Counts()
	if raw != 1 || aggregate != 1 {
		t.Fatalf("counts = raw %d aggregate %d, want 1 and 1", raw, aggregate)
	}
	metricAggregates, uniques := store.ProjectionCounts()
	if metricAggregates != 1 || uniques != 1 {
		t.Fatalf("metric duplicate counts = %d aggregates and %d uniques, want 1 and 1", metricAggregates, uniques)
	}

	metrics := httptest.NewRecorder()
	handler.ServeHTTP(metrics, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if !strings.Contains(metrics.Body.String(), `faros_telemetry_events_total{outcome="duplicate"} 1`) {
		t.Fatalf("metrics missing duplicate count: %s", metrics.Body.String())
	}
}

func TestNewServerRejectsIdenticalCredentials(t *testing.T) {
	_, err := NewServer(NewMemoryStore(), Config{IngestToken: "same-secret-0000", AdminToken: "same-secret-0000"})
	if !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("NewServer() error = %v, want ErrInvalidConfig", err)
	}
}

func TestNewServerRejectsWeakOrWhitespaceCredentials(t *testing.T) {
	for _, cfg := range []Config{
		{IngestToken: "short", AdminToken: "admin-secret-0000"},
		{IngestToken: "ingest-secret-000", AdminToken: "short"},
		{IngestToken: "ingest secret-000", AdminToken: "admin-secret-0000"},
		{IngestToken: "ingest-secret-000", AdminToken: "admin-secret-000\n"},
	} {
		if _, err := NewServer(NewMemoryStore(), cfg); !errors.Is(err, ErrInvalidConfig) {
			t.Fatalf("NewServer(%+v) error = %v, want ErrInvalidConfig", cfg, err)
		}
	}
}

func TestErasureIsIdempotentAndRetainsAggregates(t *testing.T) {
	server, store := testServer(t)
	handler := server.Handler()
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, batchRequest(t, "/v1/events", "ingest-secret-000", testEvent("event-1", "tenant-a"), testEvent("event-2", "tenant-b")))
	if response.Code != http.StatusAccepted {
		t.Fatalf("ingest status = %d", response.Code)
	}

	body := `{"request_id":"erase-1","tenant_id":"tenant-a"}`
	request := httptest.NewRequest(http.MethodPost, "/v1/erasure", strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer admin-secret-0000")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted {
		t.Fatalf("erasure status = %d", response.Code)
	}
	raw, aggregate := store.Counts()
	if raw != 1 || aggregate != 1 {
		t.Fatalf("post-erasure counts = raw %d aggregate %d, want 1 and 1", raw, aggregate)
	}

	request = httptest.NewRequest(http.MethodPost, "/v1/erasure", strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer admin-secret-0000")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	var result ErasureResult
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if !result.Existing || result.DeletedRaw != 2 {
		t.Fatalf("repeat erasure result = %+v", result)
	}

	request = httptest.NewRequest(http.MethodPost, "/v1/erasure", strings.NewReader(`{"request_id":"erase-1","tenant_id":"tenant-b"}`))
	request.Header.Set("Authorization", "Bearer admin-secret-0000")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusConflict {
		t.Fatalf("conflicting erasure status = %d, want %d", response.Code, http.StatusConflict)
	}

	request = httptest.NewRequest(http.MethodPost, "/v1/erasure", strings.NewReader(body+`{"unexpected":true}`))
	request.Header.Set("Authorization", "Bearer admin-secret-0000")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("trailing JSON erasure status = %d, want %d", response.Code, http.StatusBadRequest)
	}

	request = httptest.NewRequest(http.MethodPost, "/v1/erasure", strings.NewReader(body+strings.Repeat(" ", 64*1024)))
	request.Header.Set("Authorization", "Bearer admin-secret-0000")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("oversize erasure status = %d, want %d", response.Code, http.StatusBadRequest)
	}

	request = httptest.NewRequest(http.MethodPost, "/v1/erasure", strings.NewReader(`{"request_id":"erase-space","tenant_id":"  tenant-b  "}`))
	request.Header.Set("Authorization", "Bearer admin-secret-0000")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted {
		t.Fatalf("trimmed tenant erasure status = %d, want %d", response.Code, http.StatusAccepted)
	}
}

func TestMemoryAggregatesUseFixedComponentAndDeclaredType(t *testing.T) {
	store := NewMemoryStore()
	event := receiverTestEvent(generated.ActionOrganizationCreated, "tenant-a", time.Unix(0, 0).UTC(), map[string]interface{}{"outcome": "success"})
	event.ID = "event-1"
	if _, err := store.Insert(nil, []Event{{
		Tenant: event.Tenant, ID: event.ID, Source: event.Source, Type: event.Type, Subject: event.Subject, Time: event.Time,
		DataContentType: "application/json", Data: event.Data, Record: event.Record, ReceivedAt: event.Time,
	}}); err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	for key := range store.aggregates {
		if key.source != aggregateComponent {
			t.Fatalf("aggregate source = %q, want fixed %q", key.source, aggregateComponent)
		}
		if key.type_ != generated.ActionOrganizationCreated {
			t.Fatalf("aggregate type = %q, want %q", key.type_, generated.ActionOrganizationCreated)
		}
	}
}

func TestRetentionAndReadiness(t *testing.T) {
	server, store := testServer(t)
	old := time.Now().UTC().Add(-48 * time.Hour)
	event := receiverTestEvent(generated.ActionOrganizationCreated, "tenant-a", old, map[string]interface{}{"outcome": "success"})
	event.ID = "old"
	event.ReceivedAt = old
	if _, err := store.Insert(nil, []Event{event}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PurgeExpired(nil, time.Now().UTC(), 24*time.Hour, 72*time.Hour); err != nil {
		t.Fatal(err)
	}
	raw, aggregate := store.Counts()
	if raw != 0 || aggregate != 1 {
		t.Fatalf("retention counts = raw %d aggregate %d, want 0 and 1", raw, aggregate)
	}

	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("readiness status = %d, want %d", response.Code, http.StatusOK)
	}
	if _, err := io.ReadAll(response.Body); err != nil {
		t.Fatal(err)
	}
}

func receiverTestEvent(action, tenant string, occurredAt time.Time, properties map[string]interface{}) Event {
	definition, ok := generated.LookupEvent(action)
	if !ok {
		panic("generated event missing: " + action)
	}
	identifiers := make(map[string]string, len(definition.Identifiers))
	for index, name := range definition.Identifiers {
		identifiers[name] = strings.Repeat(string(rune('a'+index)), 64)
	}
	record := Record{InstallationID: tenant, Provider: definition.Owner, Action: action, OccurredAt: occurredAt, Identifiers: identifiers, Properties: properties}
	data, err := json.Marshal(record)
	if err != nil {
		panic(err)
	}
	return Event{Tenant: tenant, ID: action + "-id", Source: "faros://installation/" + tenant + "/hub", Type: action, Subject: definition.Owner, Time: occurredAt, DataContentType: "application/json", Data: data, Record: record, ReceivedAt: occurredAt}
}
