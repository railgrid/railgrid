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
	"encoding/pem"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	producttelemetry "github.com/faroshq/provider-sdk/telemetry"
	"k8s.io/client-go/rest"
)

func TestProductTelemetryDisabledByDefault(t *testing.T) {
	t.Setenv("FAROS_PRODUCT_TELEMETRY_ENABLED", "")
	t.Setenv("FAROS_HUB_URL", "https://hub.example.invalid")
	t.Setenv("FAROS_HUB_TOKEN", "provider-token")
	tracker := newProductTelemetryTracker("provider-token")
	if _, ok := tracker.(producttelemetry.NoopTracker); !ok {
		t.Fatalf("default product telemetry tracker = %T, want telemetry.NoopTracker", tracker)
	}
	if err := tracker.Close(); err != nil {
		t.Fatalf("disabled tracker close: %v", err)
	}
}

func TestProductTelemetryRequiresProviderServiceAccountToken(t *testing.T) {
	t.Setenv("FAROS_PRODUCT_TELEMETRY_ENABLED", "true")
	t.Setenv("FAROS_HUB_URL", "https://hub.example.invalid")
	t.Setenv("FAROS_HUB_TOKEN", "legacy-heartbeat-token")
	tracker := newProductTelemetryTracker("")
	if _, ok := tracker.(producttelemetry.NoopTracker); !ok {
		t.Fatalf("tracker without provider ServiceAccount token = %T, want telemetry.NoopTracker", tracker)
	}
}

func TestProductTelemetryUsesProviderKubeconfigCA(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})

	for _, tc := range []struct {
		name   string
		caFile bool
	}{
		{name: "data"},
		{name: "file", caFile: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &rest.Config{BearerToken: "provider-token"}
			if tc.caFile {
				path := filepath.Join(t.TempDir(), "hub-ca.pem")
				if err := os.WriteFile(path, caPEM, 0o600); err != nil {
					t.Fatal(err)
				}
				cfg.CAFile = path
			} else {
				cfg.CAData = caPEM
			}
			t.Setenv("FAROS_PRODUCT_TELEMETRY_ENABLED", "true")
			t.Setenv("FAROS_HUB_URL", server.URL)

			tracker := newProductTelemetryTrackerWithConfig(cfg.BearerToken, cfg)
			if _, ok := tracker.(producttelemetry.NoopTracker); ok {
				t.Fatal("private-CA telemetry unexpectedly disabled")
			}
			if err := tracker.Track(context.Background(), producttelemetry.Event{Action: "project_created"}); err != nil {
				t.Fatalf("Track() error = %v", err)
			}
			if err := tracker.Close(); err != nil {
				t.Fatalf("Close() error = %v", err)
			}
		})
	}
}

func TestProductTelemetryInvalidProviderKubeconfigCAFallsBackToNoop(t *testing.T) {
	serverCalls := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		serverCalls++
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()
	t.Setenv("FAROS_PRODUCT_TELEMETRY_ENABLED", "true")
	t.Setenv("FAROS_HUB_URL", server.URL)

	tracker := newProductTelemetryTrackerWithConfig("provider-token", &rest.Config{TLSClientConfig: rest.TLSClientConfig{CAData: []byte("not a certificate")}})
	if _, ok := tracker.(producttelemetry.NoopTracker); !ok {
		t.Fatalf("invalid configured CA tracker = %T, want NoopTracker", tracker)
	}
	if err := tracker.Track(context.Background(), producttelemetry.Event{Action: "project_created"}); err != nil {
		t.Fatalf("noop Track() error = %v", err)
	}
	if serverCalls != 0 {
		t.Fatalf("invalid configured CA made %d telemetry calls", serverCalls)
	}
}

func TestProductTelemetryOnlyEnablesForExplicitTrue(t *testing.T) {
	for _, value := range []string{"", "false", "1", "yes"} {
		t.Run("disabled_"+value, func(t *testing.T) {
			t.Setenv("FAROS_PRODUCT_TELEMETRY_ENABLED", value)
			if productTelemetryEnabled() {
				t.Fatalf("productTelemetryEnabled(%q) = true, want false", value)
			}
		})
	}
	t.Setenv("FAROS_PRODUCT_TELEMETRY_ENABLED", "true")
	if !productTelemetryEnabled() {
		t.Fatal("productTelemetryEnabled(true) = false")
	}
}

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

	h, err := newHandler(nil)
	if err != nil {
		t.Fatalf("newHandler: %v", err)
	}

	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	for _, tc := range []struct {
		path         string
		contentType  string
		bodyContains string
	}{
		{path: "/main.js", contentType: "javascript", bodyContains: "faros-provider-app-studio"},
		{path: "/icon.svg", contentType: "image/svg+xml", bodyContains: "<svg"},
		{path: "/does-not-exist", contentType: "text/html", bodyContains: "App Studio provider"},
	} {
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
			if got, want := res.StatusCode, http.StatusOK; got != want {
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

func TestPreviewConsoleEnvironmentConfigDefaultsOnWhenConfigured(t *testing.T) {
	t.Setenv("APP_STUDIO_PREVIEW_CONSOLE_ENABLED", "")
	t.Setenv("APP_STUDIO_PREVIEW_CONSOLE_SIGNING_KEY", " private-key ")
	t.Setenv("APP_STUDIO_PREVIEW_CONSOLE_SIGNING_KEY_ID", " current ")

	enabled, privateKey, keyID := previewConsoleEnvironmentConfig()
	if !enabled || privateKey != "private-key" || keyID != "current" {
		t.Fatalf("config = (%v, %q, %q), want enabled trimmed configuration", enabled, privateKey, keyID)
	}
}

func TestPreviewConsoleEnvironmentConfigCanBeExplicitlyDisabled(t *testing.T) {
	t.Setenv("APP_STUDIO_PREVIEW_CONSOLE_ENABLED", "false")
	t.Setenv("APP_STUDIO_PREVIEW_CONSOLE_SIGNING_KEY", "private-key")
	t.Setenv("APP_STUDIO_PREVIEW_CONSOLE_SIGNING_KEY_ID", "current")

	enabled, _, _ := previewConsoleEnvironmentConfig()
	if enabled {
		t.Fatal("config enabled with explicit false")
	}
}

func TestPreviewConsoleEnvironmentConfigSoftDisablesWithoutSigningMaterial(t *testing.T) {
	t.Setenv("APP_STUDIO_PREVIEW_CONSOLE_ENABLED", "")
	t.Setenv("APP_STUDIO_PREVIEW_CONSOLE_SIGNING_KEY", "")
	t.Setenv("APP_STUDIO_PREVIEW_CONSOLE_SIGNING_KEY_ID", "")

	enabled, _, _ := previewConsoleEnvironmentConfig()
	if enabled {
		t.Fatal("config enabled without signing material")
	}
}
