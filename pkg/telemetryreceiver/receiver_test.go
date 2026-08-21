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
	event := cloudevents.NewEvent()
	event.SetID(id)
	event.SetSource("test")
	event.SetType(generated.ActionOrganizationCreated)
	event.SetExtension("tenant", tenant)
	if err := event.SetData(cloudevents.ApplicationJSON, map[string]any{"id": id}); err != nil {
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
	server, err := NewServer(store, Config{IngestToken: "ingest-secret", AdminToken: "admin-secret"})
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
		handler.ServeHTTP(response, batchRequest(t, "/v1/events", "ingest-secret", event))
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

	metrics := httptest.NewRecorder()
	handler.ServeHTTP(metrics, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if !strings.Contains(metrics.Body.String(), `faros_telemetry_events_total{outcome="duplicate"} 1`) {
		t.Fatalf("metrics missing duplicate count: %s", metrics.Body.String())
	}
}

func TestNewServerRejectsIdenticalCredentials(t *testing.T) {
	_, err := NewServer(NewMemoryStore(), Config{IngestToken: "same-secret", AdminToken: "same-secret"})
	if !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("NewServer() error = %v, want ErrInvalidConfig", err)
	}
}

func TestErasureIsIdempotentAndRetainsAggregates(t *testing.T) {
	server, store := testServer(t)
	handler := server.Handler()
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, batchRequest(t, "/v1/events", "ingest-secret", testEvent("event-1", "tenant-a"), testEvent("event-2", "tenant-b")))
	if response.Code != http.StatusAccepted {
		t.Fatalf("ingest status = %d", response.Code)
	}

	body := `{"request_id":"erase-1","tenant_id":"tenant-a"}`
	request := httptest.NewRequest(http.MethodPost, "/v1/erasure", strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer admin-secret")
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
	request.Header.Set("Authorization", "Bearer admin-secret")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	var result ErasureResult
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if !result.Existing || result.DeletedRaw != 1 {
		t.Fatalf("repeat erasure result = %+v", result)
	}

	request = httptest.NewRequest(http.MethodPost, "/v1/erasure", strings.NewReader(`{"request_id":"erase-1","tenant_id":"tenant-b"}`))
	request.Header.Set("Authorization", "Bearer admin-secret")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusConflict {
		t.Fatalf("conflicting erasure status = %d, want %d", response.Code, http.StatusConflict)
	}

	request = httptest.NewRequest(http.MethodPost, "/v1/erasure", strings.NewReader(body+`{"unexpected":true}`))
	request.Header.Set("Authorization", "Bearer admin-secret")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("trailing JSON erasure status = %d, want %d", response.Code, http.StatusBadRequest)
	}

	request = httptest.NewRequest(http.MethodPost, "/v1/erasure", strings.NewReader(body+strings.Repeat(" ", 64*1024)))
	request.Header.Set("Authorization", "Bearer admin-secret")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("oversize erasure status = %d, want %d", response.Code, http.StatusBadRequest)
	}

	request = httptest.NewRequest(http.MethodPost, "/v1/erasure", strings.NewReader(`{"request_id":"erase-space","tenant_id":"  tenant-b  "}`))
	request.Header.Set("Authorization", "Bearer admin-secret")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted {
		t.Fatalf("trimmed tenant erasure status = %d, want %d", response.Code, http.StatusAccepted)
	}
}

func TestMemoryAggregatesUseFixedComponentAndDeclaredType(t *testing.T) {
	store := NewMemoryStore()
	if _, err := store.Insert(nil, []Event{{
		Tenant: "tenant-a", ID: "event-1", Source: "faros://installation/tenant-a/hub", Type: generated.ActionOrganizationCreated,
		DataContentType: "application/json", Data: []byte(`{"ok":true}`), ReceivedAt: time.Unix(0, 0).UTC(),
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
	if _, err := store.Insert(nil, []Event{{Tenant: "tenant-a", ID: "old", Source: "test", Type: generated.ActionOrganizationCreated, Time: old, DataContentType: "application/json", Data: []byte(`{"old":true}`), ReceivedAt: old}}); err != nil {
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
