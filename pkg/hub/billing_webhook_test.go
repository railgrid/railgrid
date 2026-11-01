/*
Copyright 2026 The Railgrid Authors.

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

package hub

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/mux"
)

// A hub without kcp or metering still serves the webhook route: a delivery is
// answered 503 (the provider retries later), never a 404 from the generic
// handling, and only POST is a delivery.
func TestBillingWebhookRouteWithoutMetering(t *testing.T) {
	r := mux.NewRouter()
	registerBillingWebhook(r, nil)

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/billing/webhook", strings.NewReader(`{"id":"evt_1"}`)))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("POST without metering: status %d, want 503", rec.Code)
	}

	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/billing/webhook", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET: status %d, want 405", rec.Code)
	}
}
