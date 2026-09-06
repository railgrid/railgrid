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

package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gorilla/mux"
)

// adminGatedRouter mounts the admin routes behind the real gate, with the
// caller's identity and admin verdict supplied by the test.
func adminGatedRouter(user string, isAdmin bool) http.Handler {
	r := mux.NewRouter()
	sub := r.PathPrefix("/api/admin").Subrouter()
	sub.Use(Middleware(
		UserResolverFunc(func(*http.Request) (string, error) { return user, nil }),
		AdminCheckerFunc(func(context.Context, string) bool { return isAdmin }),
	))
	// A nil Service is enough: every case here is refused by the gate or the
	// handler's own validation before any kcp call, and a case that got past
	// both would panic rather than silently pass.
	NewHandler(nil, nil, nil).Register(sub)
	return r
}

// Rotation mints a live cluster credential for a platform provider, so it sits
// behind the same platform-admin gate as the rest of /api/admin — an
// authenticated non-admin gets 403 and an unidentified caller 401, and neither
// reaches the handler.
func TestRotateProviderCredentialRequiresPlatformAdmin(t *testing.T) {
	for _, tc := range []struct {
		name       string
		user       string
		isAdmin    bool
		wantStatus int
	}{
		{"non-admin", "bob", false, http.StatusForbidden},
		{"unidentified", "", false, http.StatusUnauthorized},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/api/admin/providers/code/credentials/rotate", nil)
			adminGatedRouter(tc.user, tc.isAdmin).ServeHTTP(rec, req)
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tc.wantStatus)
			}
		})
	}
}

// Rotation must be a POST to its own path. A GET on the kubeconfig route
// deliberately returns the SAME credential every time, so if rotation were
// reachable by any other method or path the "re-fetching does not multiply live
// credentials" property would be quietly gone.
func TestRotateProviderCredentialIsPOSTOnly(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(method, "/api/admin/providers/code/credentials/rotate", nil)
		adminGatedRouter("alice", true).ServeHTTP(rec, req)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s: status = %d, want 405", method, rec.Code)
		}
	}
}

// The server mode is parsed before anything is minted: an unknown value is the
// caller's mistake, and rotating first and then failing to render would burn a
// credential and start the previous one's deletion clock for nothing.
func TestRotateProviderCredentialRejectsAnUnknownServerModeBeforeMinting(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/admin/providers/code/credentials/rotate?server=sideways", nil)
	adminGatedRouter("alice", true).ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (a nil Service would have panicked had it minted)", rec.Code)
	}
}
