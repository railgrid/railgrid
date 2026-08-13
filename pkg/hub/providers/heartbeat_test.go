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

package providers

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"
)

func heartbeatRequestFor(name string) *http.Request {
	return httptest.NewRequest(http.MethodPost, PathProviderHeartbeat+"/"+name+"/heartbeat", nil)
}

// A heartbeat reaches one replica but must become visible to all of them, which
// is what persisting it to CatalogEntry status buys.
func TestHeartbeatIsPersistedForOtherReplicas(t *testing.T) {
	reg := NewRegistry()
	reg.Upsert(Provider{Name: "cost", EndpointsValid: true})

	var recorded []string
	handler := NewHeartbeatHandler(reg, func(_ context.Context, name, version string, _ time.Time) error {
		recorded = append(recorded, name+"@"+version)
		return nil
	}, logr.Discard())

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost,
		PathProviderHeartbeat+"/cost/heartbeat", strings.NewReader(`{"version":"v1.2.3"}`))
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if len(recorded) != 1 || recorded[0] != "cost@v1.2.3" {
		t.Fatalf("recorded = %v, want [cost@v1.2.3]", recorded)
	}
	if got, _ := reg.Get("cost"); !got.HeartbeatRequired || got.LastHeartbeat.IsZero() {
		t.Fatalf("local registry not updated: %+v", got)
	}
}

// A provider that beats far faster than intended must not turn into a stream of
// API writes; the local registry still records every beat.
func TestHeartbeatPersistIsThrottled(t *testing.T) {
	reg := NewRegistry()
	reg.Upsert(Provider{Name: "cost", EndpointsValid: true})

	writes := 0
	handler := NewHeartbeatHandler(reg, func(context.Context, string, string, time.Time) error {
		writes++
		return nil
	}, logr.Discard())

	for range 5 {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, heartbeatRequestFor("cost"))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
	}
	if writes != 1 {
		t.Fatalf("status writes = %d, want 1 (first beat only)", writes)
	}
}

// If the beat cannot be shared, reporting success would leave every other
// replica timing out a healthy provider with nothing in the logs.
func TestHeartbeatFailsWhenPersistFails(t *testing.T) {
	reg := NewRegistry()
	reg.Upsert(Provider{Name: "cost", EndpointsValid: true})

	handler := NewHeartbeatHandler(reg, func(context.Context, string, string, time.Time) error {
		return fmt.Errorf("kcp unavailable")
	}, logr.Discard())

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, heartbeatRequestFor("cost"))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
}

func TestHeartbeatUnknownProviderIsNotPersisted(t *testing.T) {
	reg := NewRegistry()
	persisted := false
	handler := NewHeartbeatHandler(reg, func(context.Context, string, string, time.Time) error {
		persisted = true
		return nil
	}, logr.Discard())

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, heartbeatRequestFor("nope"))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if persisted {
		t.Fatal("persisted a heartbeat for a provider that is not registered")
	}
}

// Without a recorder (no kcp) the handler still works process-locally.
func TestHeartbeatWithoutRecorder(t *testing.T) {
	reg := NewRegistry()
	reg.Upsert(Provider{Name: "cost", EndpointsValid: true})
	handler := NewHeartbeatHandler(reg, nil, logr.Discard())

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, heartbeatRequestFor("cost"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got, _ := reg.Get("cost"); !got.HeartbeatRequired {
		t.Fatal("local registry not updated")
	}
}
