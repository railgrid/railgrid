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

package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRunMainRoutesServeToProviderServer(t *testing.T) {
	var served bool
	code := runMainWith(
		[]string{"serve"},
		func(context.Context) error { return nil },
		func() { served = true },
		io.Discard,
	)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if !served {
		t.Fatal("serve handler was not called")
	}
}

func TestRunMainRejectsUnknownCommand(t *testing.T) {
	var stderr bytes.Buffer
	code := runMainWith(
		[]string{"bogus"},
		func(context.Context) error { return nil },
		func() { t.Fatal("serve handler should not be called") },
		&stderr,
	)
	if code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	if got := stderr.String(); !strings.Contains(got, "usage: app-studio [init|serve]") {
		t.Fatalf("stderr = %q, want usage", got)
	}
}

func TestHealthz(t *testing.T) {
	h, err := newHandler(nil)
	if err != nil {
		t.Fatalf("newHandler: %v", err)
	}

	res := httptest.NewRecorder()
	h.ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if got, want := res.Code, http.StatusOK; got != want {
		t.Fatalf("GET /healthz status = %d, want %d", got, want)
	}
	if !strings.Contains(res.Body.String(), `"status":"ok"`) {
		t.Fatalf("GET /healthz body = %q, want status ok", res.Body.String())
	}
}

func TestReadinessRequiresControllerButLivenessStaysProcessLevel(t *testing.T) {
	health := newControllerHealth(true)
	h, err := newHandler(nil, health)
	if err != nil {
		t.Fatalf("newHandler: %v", err)
	}

	liveness := httptest.NewRecorder()
	h.ServeHTTP(liveness, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if got, want := liveness.Code, http.StatusOK; got != want {
		t.Fatalf("GET /healthz status = %d, want %d", got, want)
	}

	readiness := httptest.NewRecorder()
	h.ServeHTTP(readiness, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if got, want := readiness.Code, http.StatusServiceUnavailable; got != want {
		t.Fatalf("GET /readyz while starting status = %d, want %d", got, want)
	}
	if body := readiness.Body.String(); !strings.Contains(body, `"controller":"starting"`) || !strings.Contains(body, `"status":"not_ready"`) {
		t.Fatalf("GET /readyz while starting body = %q, want starting/not_ready", body)
	}

	health.markReady()
	readiness = httptest.NewRecorder()
	h.ServeHTTP(readiness, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if got, want := readiness.Code, http.StatusOK; got != want {
		t.Fatalf("GET /readyz while running status = %d, want %d", got, want)
	}

	health.markFailed(errors.New("manager exited"))
	readiness = httptest.NewRecorder()
	h.ServeHTTP(readiness, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if got, want := readiness.Code, http.StatusServiceUnavailable; got != want {
		t.Fatalf("GET /readyz after manager exit status = %d, want %d", got, want)
	}
	if body := readiness.Body.String(); !strings.Contains(body, `"controller":"failed"`) || !strings.Contains(body, "manager exited") {
		t.Fatalf("GET /readyz after manager exit body = %q, want failure detail", body)
	}
}

func TestRESTOnlyReadinessIsIntentional(t *testing.T) {
	health := newControllerHealth(false)
	h, err := newHandler(nil, health)
	if err != nil {
		t.Fatalf("newHandler: %v", err)
	}
	readiness := httptest.NewRecorder()
	h.ServeHTTP(readiness, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if got, want := readiness.Code, http.StatusOK; got != want {
		t.Fatalf("GET /readyz in REST-only mode status = %d, want %d", got, want)
	}
	if body := readiness.Body.String(); !strings.Contains(body, `"controller":"rest-only"`) {
		t.Fatalf("GET /readyz in REST-only mode body = %q, want rest-only controller", body)
	}
}

func TestPortalAssets(t *testing.T) {
	_, distFS, err := portalHandler()
	if err != nil {
		t.Fatalf("portalHandler: %v", err)
	}
	if _, err := fs.Stat(distFS, "main.js"); errors.Is(err, fs.ErrNotExist) {
		t.Skip("portal bundle not built; run make build-app-studio-provider")
	} else if err != nil {
		t.Fatalf("stat main.js: %v", err)
	}
	var chunks []string
	for _, name := range []string{"page-element", "tile-element", "styles"} {
		matches, err := fs.Glob(distFS, "assets/"+name+"-*.js")
		if err != nil {
			t.Fatalf("glob %s portal chunk: %v", name, err)
		}
		if len(matches) != 1 {
			t.Fatalf("%s portal chunks = %v, want one content-hashed chunk", name, matches)
		}
		chunks = append(chunks, matches[0])
	}
	componentCSS, err := fs.Glob(distFS, "assets/page-element-*.css")
	if err != nil {
		t.Fatalf("glob component CSS: %v", err)
	}
	if len(componentCSS) != 1 {
		t.Fatalf("component CSS = %v, want one content-hashed stylesheet", componentCSS)
	}

	h, err := newHandler(nil)
	if err != nil {
		t.Fatalf("newHandler: %v", err)
	}

	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	tests := []struct {
		path         string
		status       int
		contentType  string
		bodyContains string
	}{
		{path: "/main.js", status: http.StatusOK, contentType: "javascript", bodyContains: "faros-provider-app-studio"},
		{path: "/" + componentCSS[0], status: http.StatusOK, contentType: "text/css"},
		{path: "/icon.svg", status: http.StatusOK, contentType: "image/svg+xml", bodyContains: "<svg"},
		{path: "/does-not-exist", status: http.StatusOK, contentType: "text/html", bodyContains: "App Studio provider"},
		{path: "/assets/retired-chunk.js", status: http.StatusNotFound, contentType: "text/plain", bodyContains: "404 page not found"},
		{path: "/missing.css", status: http.StatusNotFound, contentType: "text/plain", bodyContains: "404 page not found"},
	}
	for _, chunk := range chunks {
		tests = append(tests, struct {
			path         string
			status       int
			contentType  string
			bodyContains string
		}{path: "/" + chunk, status: http.StatusOK, contentType: "javascript"})
	}

	for _, tc := range tests {
		res, err := srv.Client().Get(srv.URL + tc.path)
		if err != nil {
			t.Fatalf("GET %s: %v", tc.path, err)
		}
		func() {
			defer func() {
				if err := res.Body.Close(); err != nil {
					t.Errorf("close %s response body: %v", tc.path, err)
				}
			}()
			if got, want := res.StatusCode, tc.status; got != want {
				t.Fatalf("GET %s status = %d, want %d", tc.path, got, want)
			}
			if got := res.Header.Get("Content-Type"); !strings.Contains(got, tc.contentType) {
				t.Fatalf("GET %s content-type = %q, want %q", tc.path, got, tc.contentType)
			}
			body, _ := io.ReadAll(res.Body)
			if !strings.Contains(string(body), tc.bodyContains) {
				t.Fatalf("GET %s body missing %q", tc.path, tc.bodyContains)
			}
		}()
	}
}

func TestOpenMessageStoreRequiresConfiguredStore(t *testing.T) {
	t.Setenv("APP_STUDIO_DATABASE_URL", "")
	t.Setenv("APP_STUDIO_IN_MEMORY_MESSAGE_STORE", "")
	t.Setenv("APP_STUDIO_MESSAGE_ENCRYPTION_KEYS", "")
	t.Setenv("APP_STUDIO_MESSAGE_RETENTION", "")

	_, closeFn, err := openMessageStore(context.Background())
	if err == nil {
		t.Fatal("openMessageStore returned nil error without a configured store")
	}
	closeFn()
}

func TestOpenMessageStoreAllowsInMemoryStore(t *testing.T) {
	t.Setenv("APP_STUDIO_DATABASE_URL", "")
	t.Setenv("APP_STUDIO_IN_MEMORY_MESSAGE_STORE", "true")
	t.Setenv("APP_STUDIO_MESSAGE_ENCRYPTION_KEYS", "")
	t.Setenv("APP_STUDIO_MESSAGE_RETENTION", "")

	msgStore, closeFn, err := openMessageStore(context.Background())
	if err != nil {
		t.Fatalf("openMessageStore returned error: %v", err)
	}
	defer closeFn()
	if msgStore == nil {
		t.Fatal("openMessageStore returned nil store")
	}
}

func TestPreviewBridgeEnvironmentConfigDefaultsOnWhenConfigured(t *testing.T) {
	t.Setenv("APP_STUDIO_PREVIEW_BRIDGE_ENABLED", "")
	t.Setenv("APP_STUDIO_PREVIEW_BRIDGE_SIGNING_KEY", " private-key ")
	t.Setenv("APP_STUDIO_PREVIEW_BRIDGE_SIGNING_KEY_ID", " current ")

	enabled, privateKey, keyID := previewBridgeEnvironmentConfig()
	if !enabled || privateKey != "private-key" || keyID != "current" {
		t.Fatalf("config = (%v, %q, %q), want enabled trimmed configuration", enabled, privateKey, keyID)
	}
}

func TestPreviewBridgeEnvironmentConfigCanBeExplicitlyDisabled(t *testing.T) {
	t.Setenv("APP_STUDIO_PREVIEW_BRIDGE_ENABLED", "false")
	t.Setenv("APP_STUDIO_PREVIEW_BRIDGE_SIGNING_KEY", "private-key")
	t.Setenv("APP_STUDIO_PREVIEW_BRIDGE_SIGNING_KEY_ID", "current")

	enabled, _, _ := previewBridgeEnvironmentConfig()
	if enabled {
		t.Fatal("config enabled with explicit false")
	}
}

func TestPreviewBridgeEnvironmentConfigSoftDisablesWithoutSigningMaterial(t *testing.T) {
	t.Setenv("APP_STUDIO_PREVIEW_BRIDGE_ENABLED", "")
	t.Setenv("APP_STUDIO_PREVIEW_BRIDGE_SIGNING_KEY", "")
	t.Setenv("APP_STUDIO_PREVIEW_BRIDGE_SIGNING_KEY_ID", "")

	enabled, _, _ := previewBridgeEnvironmentConfig()
	if enabled {
		t.Fatal("config enabled without signing material")
	}
}
