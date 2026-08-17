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

package hub

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNormalizePortalRootServesSlashlessRootWithoutRedirect(t *testing.T) {
	t.Parallel()

	var receivedPath string
	handler := normalizePortalRoot(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedPath = r.URL.Path
		w.WriteHeader(http.StatusNoContent)
	}))

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/ui?from=refresh", nil))

	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusNoContent)
	}
	if location := recorder.Header().Get("Location"); location != "" {
		t.Fatalf("Location = %q, want no redirect", location)
	}
	if receivedPath != "/ui/" {
		t.Fatalf("downstream path = %q, want /ui/", receivedPath)
	}
}

func TestNormalizePortalRootLeavesOtherPathsUnchanged(t *testing.T) {
	t.Parallel()

	var receivedPath string
	handler := normalizePortalRoot(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedPath = r.URL.Path
		w.WriteHeader(http.StatusNoContent)
	}))

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/ui/providers/app-studio", nil))

	if receivedPath != "/ui/providers/app-studio" {
		t.Fatalf("downstream path = %q, want original path", receivedPath)
	}
}
