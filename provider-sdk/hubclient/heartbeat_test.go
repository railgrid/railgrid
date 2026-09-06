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

package hubclient

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/go-logr/logr/funcr"
)

// recordingLogger captures every log line so tests can assert on them.
type recordingLogger struct {
	mu    sync.Mutex
	lines []string
}

func (r *recordingLogger) logger() logr.Logger {
	return funcr.New(func(prefix, args string) {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.lines = append(r.lines, prefix+" "+args)
	}, funcr.Options{Verbosity: 0})
}

func (r *recordingLogger) joined() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return strings.Join(r.lines, "\n")
}

type beat struct {
	auth    string
	path    string
	version string
	status  string
}

// heartbeatSink is an httptest hub that records every beat and answers with
// respond's status.
func heartbeatSink(t *testing.T, respond int) (*httptest.Server, func() []beat) {
	t.Helper()
	var mu sync.Mutex
	var beats []beat
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		var body struct {
			Version string `json:"version"`
			Status  string `json:"status"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode body: %v", err)
		}
		mu.Lock()
		beats = append(beats, beat{auth: r.Header.Get("Authorization"), path: r.URL.Path, version: body.Version, status: body.Status})
		mu.Unlock()
		if respond >= 300 {
			http.Error(w, "heartbeat not authenticated: heartbeat bearer token is not the provider's service account", respond)
			return
		}
		w.WriteHeader(respond)
	}))
	t.Cleanup(srv.Close)
	return srv, func() []beat {
		mu.Lock()
		defer mu.Unlock()
		return append([]beat(nil), beats...)
	}
}

func waitForBeats(t *testing.T, got func() []beat, n int) []beat {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if b := got(); len(b) >= n {
			return b
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("saw %d beats, want at least %d", len(got()), n)
	return nil
}

func TestRunHeartbeatPostsVersionWithBearerAndStopsOnCancel(t *testing.T) {
	srv, got := heartbeatSink(t, http.StatusOK)
	rec := &recordingLogger{}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		RunHeartbeat(ctx, HeartbeatConfig{
			HubURL:       srv.URL + "/", // trailing slash must not double up
			ProviderName: "quickstart",
			Version:      "1.2.3",
			Token:        "sa-token",
			Interval:     10 * time.Millisecond,
			Logger:       rec.logger(),
		})
	}()

	// One immediate beat plus at least one from the ticker.
	beats := waitForBeats(t, got, 2)
	for i, b := range beats {
		if b.path != "/api/providers/quickstart/heartbeat" {
			t.Errorf("beat %d path = %q", i, b.path)
		}
		if b.auth != "Bearer sa-token" {
			t.Errorf("beat %d authorization = %q, want bearer", i, b.auth)
		}
		if b.version != "1.2.3" || b.status != "healthy" {
			t.Errorf("beat %d body = %+v, want version 1.2.3 / healthy", i, b)
		}
	}

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("RunHeartbeat did not return after ctx cancel")
	}
	// A beat already in flight at cancel time may still land on the server
	// after RunHeartbeat returns; let it settle, then require no new beats.
	time.Sleep(50 * time.Millisecond)
	after := len(got())
	time.Sleep(50 * time.Millisecond)
	if n := len(got()); n != after {
		t.Fatalf("beats kept arriving after cancel: %d -> %d", after, n)
	}
	if logs := rec.joined(); strings.Contains(logs, "rejected") || strings.Contains(logs, "non-2xx") {
		t.Fatalf("unexpected failure logs on a healthy hub:\n%s", logs)
	}
}

// TestRunHeartbeatReusesTheConnection: the hub answers 2xx with a small JSON
// body. Unless that body is drained before Close, the transport cannot reuse
// the connection and every beat opens a new one (TCP + TLS in production).
func TestRunHeartbeatReusesTheConnection(t *testing.T) {
	var conns atomic.Int32
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	srv.Config.ConnState = func(_ net.Conn, s http.ConnState) {
		if s == http.StateNew {
			conns.Add(1)
		}
	}
	srv.Start()
	t.Cleanup(srv.Close)

	var beats atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		RunHeartbeat(ctx, HeartbeatConfig{
			HubURL:       srv.URL,
			ProviderName: "quickstart",
			Version:      "1.2.3",
			Token:        "sa-token",
			Interval:     10 * time.Millisecond,
			Logger:       (&recordingLogger{}).logger(),
			CanSend:      func() bool { beats.Add(1); return true },
		})
	}()
	deadline := time.Now().Add(5 * time.Second)
	for beats.Load() < 5 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	<-done
	if n := beats.Load(); n < 5 {
		t.Fatalf("only %d beats were sent", n)
	}
	if n := conns.Load(); n != 1 {
		t.Fatalf("%d connections opened for %d beats, want 1 (2xx body not drained before Close)", n, beats.Load())
	}
}

func TestRunHeartbeatLogsAuthRejection(t *testing.T) {
	srv, got := heartbeatSink(t, http.StatusUnauthorized)
	rec := &recordingLogger{}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		RunHeartbeat(ctx, HeartbeatConfig{
			HubURL:       srv.URL,
			ProviderName: "code",
			Version:      "0.1.0",
			Interval:     time.Hour, // only the immediate beat
			Logger:       rec.logger(),
		})
	}()

	beats := waitForBeats(t, got, 1)
	if beats[0].auth != "" {
		t.Fatalf("authorization = %q, want none when Token is empty", beats[0].auth)
	}
	cancel()
	<-done

	logs := rec.joined()
	for _, want := range []string{"heartbeat rejected", `"status"=401`, `"provider"="code"`, `"tokenConfigured"=false`, EnvHubToken, EnvProviderKubeconfig, "not the provider's service account"} {
		if !strings.Contains(logs, want) {
			t.Errorf("rejection log missing %q:\n%s", want, logs)
		}
	}
}

func TestRunHeartbeatHonoursCanSend(t *testing.T) {
	srv, got := heartbeatSink(t, http.StatusOK)
	var mu sync.Mutex
	ready := false

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go RunHeartbeat(ctx, HeartbeatConfig{
		HubURL:       srv.URL,
		ProviderName: "app-studio",
		Interval:     5 * time.Millisecond,
		CanSend: func() bool {
			mu.Lock()
			defer mu.Unlock()
			return ready
		},
	})

	time.Sleep(50 * time.Millisecond)
	if n := len(got()); n != 0 {
		t.Fatalf("got %d beats while CanSend is false", n)
	}
	mu.Lock()
	ready = true
	mu.Unlock()
	waitForBeats(t, got, 1)
}

func TestRunHeartbeatDisabledWithoutHubURL(t *testing.T) {
	rec := &recordingLogger{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		RunHeartbeat(context.Background(), HeartbeatConfig{ProviderName: "edges", Logger: rec.logger()})
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("RunHeartbeat should return immediately without FAROS_HUB_URL")
	}
	if !strings.Contains(rec.joined(), "heartbeat disabled") {
		t.Fatalf("expected a disabled log line, got:\n%s", rec.joined())
	}
}

func TestConfigFromEnv(t *testing.T) {
	t.Setenv(EnvHubURL, " https://hub.example.test/ ")
	t.Setenv(EnvProviderName, "")
	t.Setenv(EnvProviderVersion, "")
	t.Setenv(EnvHubInsecure, "true")
	t.Setenv(EnvHubToken, "explicit")
	t.Setenv(EnvProviderKubeconfig, "")

	cfg, err := ConfigFromEnv("kuery", "0.1.0")
	if err != nil {
		t.Fatal(err)
	}
	want := HeartbeatConfig{HubURL: "https://hub.example.test", ProviderName: "kuery", Version: "0.1.0", Token: "explicit", Insecure: true}
	if cfg.HubURL != want.HubURL || cfg.ProviderName != want.ProviderName || cfg.Version != want.Version || cfg.Token != want.Token || cfg.Insecure != want.Insecure {
		t.Fatalf("ConfigFromEnv = %+v, want %+v", cfg, want)
	}

	t.Setenv(EnvProviderName, "kuery-eu")
	t.Setenv(EnvProviderVersion, "v9.9.9")
	t.Setenv(EnvHubInsecure, "TRUE") // only the exact string "true" opts in
	t.Setenv(EnvHubToken, "")
	t.Setenv(EnvProviderKubeconfig, writeKubeconfig(t, kubeconfigWithToken))
	cfg, err = ConfigFromEnv("kuery", "0.1.0")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ProviderName != "kuery-eu" || cfg.Version != "v9.9.9" || cfg.Insecure || cfg.Token != "sa-token-from-kubeconfig" {
		t.Fatalf("ConfigFromEnv overrides = %+v", cfg)
	}

	// A broken kubeconfig surfaces as an error but still yields a usable
	// config so the provider can beat unauthenticated instead of not at all.
	t.Setenv(EnvProviderKubeconfig, writeKubeconfig(t, "not: [valid"))
	cfg, err = ConfigFromEnv("kuery", "0.1.0")
	if err == nil {
		t.Fatal("expected an error for an unreadable kubeconfig")
	}
	if cfg.HubURL != want.HubURL || cfg.Token != "" {
		t.Fatalf("config on token error = %+v, want hub URL kept and empty token", cfg)
	}
}

// A provider with no FAROS_HUB_URL has its heartbeat disabled, so
// ConfigFromEnv must not touch the provider kubeconfig at all: every call site
// logs ConfigFromEnv's error before RunHeartbeat ever reaches its disabled
// path, so resolving a token here means a pointless read and a misleading
// token error in tests and local runs.
func TestConfigFromEnvSkipsTokenResolutionWhenHeartbeatDisabled(t *testing.T) {
	t.Setenv(EnvHubURL, "  ") // trims to empty: heartbeat disabled
	t.Setenv(EnvProviderName, "")
	t.Setenv(EnvProviderVersion, "")
	t.Setenv(EnvHubInsecure, "")
	t.Setenv(EnvHubToken, "")
	// A directory, so any attempt to read it as a kubeconfig fails loudly.
	t.Setenv(EnvProviderKubeconfig, t.TempDir())

	cfg, err := ConfigFromEnv("edges", "0.1.0")
	if err != nil {
		t.Fatalf("ConfigFromEnv read the kubeconfig for a disabled heartbeat: %v", err)
	}
	if cfg.HubURL != "" || cfg.Token != "" {
		t.Fatalf("ConfigFromEnv = %+v, want an empty hub URL and no token", cfg)
	}
	// The config stays usable: RunHeartbeat takes its disabled path on it.
	rec := &recordingLogger{}
	cfg.Logger = rec.logger()
	done := make(chan struct{})
	go func() {
		defer close(done)
		RunHeartbeat(context.Background(), cfg)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("RunHeartbeat should return immediately on the disabled config")
	}
	if !strings.Contains(rec.joined(), "heartbeat disabled") {
		t.Fatalf("expected a disabled log line, got:\n%s", rec.joined())
	}
}

// A FAROS_HUB_URL ending in "/" used to concatenate into a double slash in
// seven of the eight per-provider copies. Gorilla mux cleaned the path and
// 301'd, Go's client turned the redirected POST into a GET, and the hub
// answered 405 — a silent heartbeat failure that made the provider go stale.
// End-to-end from the env var to the request line so it cannot come back.
func TestConfigFromEnvTrailingSlashPostsCleanPath(t *testing.T) {
	srv, got := heartbeatSink(t, http.StatusOK)
	t.Setenv(EnvHubURL, srv.URL+"/")
	t.Setenv(EnvProviderName, "quickstart")
	t.Setenv(EnvProviderVersion, "")
	t.Setenv(EnvHubInsecure, "")
	t.Setenv(EnvHubToken, "sa-token")
	t.Setenv(EnvProviderKubeconfig, "")

	cfg, err := ConfigFromEnv("quickstart", "0.1.0")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HubURL != srv.URL {
		t.Fatalf("HubURL = %q, want the trailing slash trimmed to %q", cfg.HubURL, srv.URL)
	}
	cfg.Interval = time.Hour // only the immediate beat

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go RunHeartbeat(ctx, cfg)

	beats := waitForBeats(t, got, 1)
	if beats[0].path != "/api/providers/quickstart/heartbeat" {
		t.Fatalf("path = %q, want no double slash", beats[0].path)
	}
}
