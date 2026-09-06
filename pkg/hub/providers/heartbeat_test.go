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
	"k8s.io/client-go/rest"
)

func heartbeatRequestFor(name string) *http.Request {
	return httptest.NewRequest(http.MethodPost, PathProviderHeartbeat+"/"+name+"/heartbeat", nil)
}

// allowAllHeartbeats stands in for the TokenReview authenticator in tests that
// exercise recording rather than authentication.
func allowAllHeartbeats(context.Context, *http.Request, string) error { return nil }

// A heartbeat reaches one replica but must become visible to all of them, which
// is what persisting it to CatalogEntry status buys.
func TestHeartbeatIsPersistedForOtherReplicas(t *testing.T) {
	reg := NewRegistry()
	reg.Upsert(Provider{Name: "cost", EndpointsValid: true})

	var recorded []string
	handler := NewHeartbeatHandler(reg, func(_ context.Context, name, version string, _ time.Time) error {
		recorded = append(recorded, name+"@"+version)
		return nil
	}, allowAllHeartbeats, HeartbeatAuthEnforce, logr.Discard())

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
	}, allowAllHeartbeats, HeartbeatAuthEnforce, logr.Discard())

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
	}, allowAllHeartbeats, HeartbeatAuthEnforce, logr.Discard())

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, heartbeatRequestFor("cost"))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
}

func TestHeartbeatUnknownProviderIsNotPersisted(t *testing.T) {
	// Enforce mode hides the fact that the name is unknown behind the same 401
	// a bad credential gets (see TestHeartbeatEnforceHidesUnknownProviders);
	// warn mode, which accepts unauthenticated beats anyway, says 404.
	for mode, want := range map[HeartbeatAuthMode]int{
		HeartbeatAuthEnforce: http.StatusUnauthorized,
		HeartbeatAuthWarn:    http.StatusNotFound,
	} {
		reg := NewRegistry()
		persisted := false
		handler := NewHeartbeatHandler(reg, func(context.Context, string, string, time.Time) error {
			persisted = true
			return nil
		}, allowAllHeartbeats, mode, logr.Discard())

		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, heartbeatRequestFor("nope"))
		if rec.Code != want {
			t.Fatalf("%s: status = %d, want %d", mode, rec.Code, want)
		}
		if persisted {
			t.Fatalf("%s: persisted a heartbeat for a provider that is not registered", mode)
		}
	}
}

// An unknown name must be turned away before the authenticator runs: the
// TokenReview round trip and, in warn mode, the log line per beat are work an
// anonymous caller would otherwise drive at will.
func TestHeartbeatUnknownProviderIsNotAuthenticated(t *testing.T) {
	for _, mode := range []HeartbeatAuthMode{HeartbeatAuthEnforce, HeartbeatAuthWarn} {
		calls := 0
		handler := NewHeartbeatHandler(NewRegistry(), nil, func(context.Context, *http.Request, string) error {
			calls++
			return nil
		}, mode, logr.Discard())

		handler.ServeHTTP(httptest.NewRecorder(), heartbeatRequestWithBearer("nope", "tok"))
		if calls != 0 {
			t.Fatalf("%s: authenticator ran %d times for an unregistered provider, want 0", mode, calls)
		}
	}
}

// Without a recorder (no kcp) the handler still works process-locally.
func TestHeartbeatWithoutRecorder(t *testing.T) {
	reg := NewRegistry()
	reg.Upsert(Provider{Name: "cost", EndpointsValid: true})
	handler := NewHeartbeatHandler(reg, nil, allowAllHeartbeats, HeartbeatAuthEnforce, logr.Discard())

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, heartbeatRequestFor("cost"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got, _ := reg.Get("cost"); !got.HeartbeatRequired {
		t.Fatal("local registry not updated")
	}
}

// The recorder must address a provider's CatalogEntry by the cluster the
// catalog watch observed it in. An earlier version wrote to a fixed workspace
// path (root:faros:system:providers) that serves the API but holds no entries,
// so every beat 404'd and liveness never reached the other replicas.
func TestCatalogEntryClusterComesFromTheObservedEntry(t *testing.T) {
	reg := NewRegistry()
	reg.Upsert(Provider{Name: "code", EndpointsValid: true, CatalogEntryCluster: "1axwjxprfb96jgta"})

	cluster, ok := reg.CatalogEntryCluster("code")
	if !ok || cluster != "1axwjxprfb96jgta" {
		t.Fatalf("CatalogEntryCluster = %q, %t; want the observed cluster", cluster, ok)
	}

	// A later reconcile that rebuilds the record without the field must not
	// erase it, exactly as WorkspaceCluster is preserved.
	reg.Upsert(Provider{Name: "code", EndpointsValid: true})
	if cluster, ok := reg.CatalogEntryCluster("code"); !ok || cluster != "1axwjxprfb96jgta" {
		t.Fatalf("after re-upsert: %q, %t; want the cluster preserved", cluster, ok)
	}

	// Unknown provider, and known provider whose entry has not been seen yet,
	// both report "not known" so the recorder can say so instead of guessing.
	if _, ok := reg.CatalogEntryCluster("nope"); ok {
		t.Fatal("unknown provider reported a cluster")
	}
	reg.Upsert(Provider{Name: "fresh", EndpointsValid: true})
	if _, ok := reg.CatalogEntryCluster("fresh"); ok {
		t.Fatal("provider with no observed entry reported a cluster")
	}
}

func TestCatalogHeartbeatRecorderRequiresAResolver(t *testing.T) {
	if _, err := NewCatalogHeartbeatRecorder(nil, NewRegistry()); err == nil {
		t.Fatal("nil kcp config accepted")
	}
	if _, err := NewCatalogHeartbeatRecorder(&rest.Config{Host: "https://example.test"}, nil); err == nil {
		t.Fatal("nil cluster resolver accepted")
	}
}

// A beat for a provider whose entry has not been observed yet must fail with a
// message that names the cause, not a 404 from a workspace that never had it.
func TestRecorderReportsUnknownCluster(t *testing.T) {
	reg := NewRegistry()
	reg.Upsert(Provider{Name: "code", EndpointsValid: true})
	recorder, err := NewCatalogHeartbeatRecorder(&rest.Config{Host: "https://example.test"}, reg)
	if err != nil {
		t.Fatalf("NewCatalogHeartbeatRecorder: %v", err)
	}
	err = recorder(context.Background(), "code", "v1", time.Now())
	if err == nil || !strings.Contains(err.Error(), "no CatalogEntry cluster known") {
		t.Fatalf("err = %v, want an unknown-cluster error", err)
	}
}
