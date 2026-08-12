/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type actionsTestRoundTripper func(*http.Request) (*http.Response, error)

func (f actionsTestRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

type actionsStatusRuntime struct{}

func (actionsStatusRuntime) Restart(context.Context) error          { return nil }
func (actionsStatusRuntime) ExitContainer(context.Context) error    { return nil }
func (actionsStatusRuntime) Reload(context.Context, []string) error { return nil }
func (actionsStatusRuntime) SetEnv(context.Context, map[string]string) ([]string, error) {
	return nil, nil
}
func (actionsStatusRuntime) Logs(context.Context) (string, error) { return "", nil }
func (actionsStatusRuntime) Status(context.Context) (processStatusResponse, error) {
	return processStatusResponse{}, nil
}

func actionsTestURLs(origin string) (exchangeURL, baseURL string) {
	origin = strings.TrimRight(origin, "/")
	return origin + actionsExchangePath, origin + actionsBasePath
}

func actionsExchangeClientForResponse(status int, body string) *http.Client {
	return &http.Client{Transport: actionsTestRoundTripper(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: status,
			Body:       io.NopCloser(strings.NewReader(body)),
			Header:     make(http.Header),
			Request:    r,
		}, nil
	})}
}

func actionsReadinessConfig(t *testing.T, enabled bool, client *http.Client) (*agentConfig, *agentServer) {
	t.Helper()
	dir := t.TempDir()
	bootstrapPath := filepath.Join(dir, "bootstrap", "token")
	if err := os.MkdirAll(filepath.Dir(bootstrapPath), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bootstrapPath, []byte("bootstrap-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	exchangeURL := ""
	baseURL := ""
	if enabled {
		exchangeURL, baseURL = actionsTestURLs("https://actions.test")
	}
	cfg := &agentConfig{
		ControlToken:              "control-token",
		ActionsBootstrapTokenFile: bootstrapPath,
		ActionsTokenFile:          filepath.Join(dir, "actions", "token"),
		ActionsExchangeURL:        exchangeURL,
		ActionsBaseURL:            baseURL,
		ActionsTenantPath:         "root:faros:tenants:org:workspace",
		ActionsProject:            "demo",
		ActionsProjectUID:         "project-uid",
		ActionsEnvironment:        "development",
		ActionsInstance:           "demo-dev",
		actionsHTTPClient:         client,
	}
	return cfg, newCoordinatorServer(cfg, actionsStatusRuntime{}, &sync.Mutex{})
}

func assertActionsReadiness(t *testing.T, server http.Handler, wantCode int, wantEnabled, wantReady bool) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	response := httptest.NewRecorder()
	server.ServeHTTP(response, req)
	if response.Code != wantCode {
		t.Fatalf("readyz status = %d body=%s, want %d", response.Code, response.Body.String(), wantCode)
	}
	var got struct {
		ActionsEnabled bool `json:"actionsEnabled"`
		ActionsReady   bool `json:"actionsReady"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode readyz: %v", err)
	}
	if got.ActionsEnabled != wantEnabled || got.ActionsReady != wantReady {
		t.Fatalf("readyz actions = enabled:%v ready:%v, want enabled:%v ready:%v", got.ActionsEnabled, got.ActionsReady, wantEnabled, wantReady)
	}
}

func TestExchangeActionsTokenPublishesTokenAndExactIdentity(t *testing.T) {
	dir := t.TempDir()
	bootstrapPath := filepath.Join(dir, "bootstrap", "token")
	if err := os.MkdirAll(filepath.Dir(bootstrapPath), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bootstrapPath, []byte("bootstrap-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var gotRequest actionsExchangeRequest
	var gotAuth string
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		if err := json.NewDecoder(r.Body).Decode(&gotRequest); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"token":"refreshed-token","tokenType":"Bearer","expiresAt":"2099-01-01T00:00:00Z"}`))
	}))
	defer server.Close()
	exchangeURL, baseURL := actionsTestURLs(server.URL)

	cfg := &agentConfig{
		ActionsBootstrapTokenFile: bootstrapPath,
		ActionsTokenFile:          filepath.Join(dir, "actions", "token"),
		ActionsExchangeURL:        exchangeURL,
		ActionsBaseURL:            baseURL,
		ActionsTenantPath:         "root:faros:tenants:org:ws",
		ActionsProject:            "demo",
		ActionsProjectUID:         "project-uid",
		ActionsEnvironment:        "development",
		ActionsInstance:           "demo-dev",
		actionsHTTPClient:         server.Client(),
	}
	expiresAt, err := exchangeActionsToken(t.Context(), cfg)
	if err != nil {
		t.Fatalf("exchangeActionsToken: %v", err)
	}
	if !expiresAt.After(time.Now()) {
		t.Fatalf("expiresAt = %s", expiresAt)
	}
	if gotAuth != "Bearer bootstrap-token" {
		t.Fatalf("authorization = %q", gotAuth)
	}
	want := actionsExchangeRequest{
		TenantPath: "root:faros:tenants:org:ws", Project: "demo", ProjectUID: "project-uid", Environment: "development", Instance: "demo-dev",
	}
	if gotRequest != want {
		t.Fatalf("request = %+v, want %+v", gotRequest, want)
	}
	gotToken, err := os.ReadFile(cfg.ActionsTokenFile)
	if err != nil {
		t.Fatalf("read token: %v", err)
	}
	if string(gotToken) != "refreshed-token\n" {
		t.Fatalf("token file = %q", gotToken)
	}
	mode, err := os.Stat(cfg.ActionsTokenFile)
	if err != nil {
		t.Fatal(err)
	}
	if mode.Mode().Perm() != 0o600 {
		t.Fatalf("token mode = %o, want 600", mode.Mode().Perm())
	}
}

func TestExchangeActionsTokenRejectsInvalidResponse(t *testing.T) {
	dir := t.TempDir()
	bootstrap := filepath.Join(dir, "bootstrap")
	if err := os.MkdirAll(bootstrap, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bootstrap, "token"), []byte("bootstrap"), 0o600); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"token":"token","tokenType":"Basic","expiresAt":"2099-01-01T00:00:00Z"}`))
	}))
	defer server.Close()
	exchangeURL, baseURL := actionsTestURLs(server.URL)
	cfg := &agentConfig{
		ActionsBootstrapTokenFile: filepath.Join(bootstrap, "token"),
		ActionsTokenFile:          filepath.Join(dir, "token"),
		ActionsExchangeURL:        exchangeURL,
		ActionsBaseURL:            baseURL,
		ActionsTenantPath:         "tenant", ActionsProject: "project", ActionsProjectUID: "uid", ActionsEnvironment: "development", ActionsInstance: "instance",
		actionsHTTPClient: server.Client(),
	}
	if _, err := exchangeActionsToken(t.Context(), cfg); err == nil {
		t.Fatal("invalid token type unexpectedly accepted")
	}
}

func TestExchangeActionsTokenDoesNotFollowRedirectOrForwardBootstrap(t *testing.T) {
	dir := t.TempDir()
	bootstrapPath := filepath.Join(dir, "bootstrap", "token")
	if err := os.MkdirAll(filepath.Dir(bootstrapPath), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bootstrapPath, []byte("bootstrap-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	exchangeURL, baseURL := actionsTestURLs("https://actions.test")
	var requests []*http.Request
	client := &http.Client{Transport: actionsTestRoundTripper(func(r *http.Request) (*http.Response, error) {
		requests = append(requests, r.Clone(r.Context()))
		return &http.Response{
			StatusCode: http.StatusFound,
			Header:     http.Header{"Location": []string{"https://evil.example/capture"}},
			Body:       io.NopCloser(strings.NewReader("redirect")),
			Request:    r,
		}, nil
	})}
	cfg := &agentConfig{
		ActionsBootstrapTokenFile: bootstrapPath,
		ActionsTokenFile:          filepath.Join(dir, "actions", "token"),
		ActionsExchangeURL:        exchangeURL,
		ActionsBaseURL:            baseURL,
		ActionsTenantPath:         "root:faros:tenants:org:ws",
		ActionsProject:            "demo",
		ActionsProjectUID:         "project-uid",
		ActionsEnvironment:        "development",
		ActionsInstance:           "demo-dev",
		actionsHTTPClient:         client,
	}
	if _, err := exchangeActionsToken(t.Context(), cfg); err == nil {
		t.Fatal("redirect response unexpectedly accepted")
	}
	if len(requests) != 1 {
		t.Fatalf("transport requests = %d, want one request with no redirect follow", len(requests))
	}
	if got := requests[0].Header.Get("Authorization"); got != "Bearer bootstrap-token" {
		t.Fatalf("initial authorization = %q, want bootstrap credential", got)
	}
	if _, err := os.Stat(cfg.ActionsTokenFile); !os.IsNotExist(err) {
		t.Fatalf("token file stat error = %v, want no token published", err)
	}
}

func TestActionsTokenRefreshDelayLeavesSafetyWindow(t *testing.T) {
	now := time.Now()
	if got, want := actionsTokenRefreshDelay(now.Add(10*time.Minute), now), 9*time.Minute; got != want {
		t.Fatalf("delay = %s, want %s", got, want)
	}
	if got := actionsTokenRefreshDelay(now.Add(2*time.Second), now); got < 5*time.Second {
		t.Fatalf("short-lived token delay = %s", got)
	}
}

func TestActionsReadinessInitialExchangeFailureDoesNotAffectLiveness(t *testing.T) {
	cfg, server := actionsReadinessConfig(t, true, actionsExchangeClientForResponse(http.StatusBadGateway, `{"error":"unavailable"}`))
	if _, err := exchangeActionsToken(t.Context(), cfg); err == nil {
		t.Fatal("exchange unexpectedly succeeded")
	}
	assertActionsReadiness(t, server, http.StatusServiceUnavailable, true, false)
	healthRequest := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	healthResponse := httptest.NewRecorder()
	server.ServeHTTP(healthResponse, healthRequest)
	if healthResponse.Code != http.StatusOK {
		t.Fatalf("healthz status = %d body=%s, want liveness 200", healthResponse.Code, healthResponse.Body.String())
	}
}

func TestActionsReadinessSuccessfulExchangePublishesStatus(t *testing.T) {
	expiresAt := time.Now().Add(5 * time.Minute).UTC()
	cfg, server := actionsReadinessConfig(t, true, actionsExchangeClientForResponse(http.StatusOK, `{"token":"runtime-token","tokenType":"Bearer","expiresAt":"`+expiresAt.Format(time.RFC3339Nano)+`"}`))
	if _, err := exchangeActionsToken(t.Context(), cfg); err != nil {
		t.Fatalf("exchangeActionsToken: %v", err)
	}
	assertActionsReadiness(t, server, http.StatusOK, true, true)

	statusRequest := httptest.NewRequest(http.MethodGet, "/status", nil)
	statusRequest.Header.Set(controlTokenHeader, "control-token")
	statusResponse := httptest.NewRecorder()
	server.ServeHTTP(statusResponse, statusRequest)
	if statusResponse.Code != http.StatusOK {
		t.Fatalf("status endpoint = %d body=%s", statusResponse.Code, statusResponse.Body.String())
	}
	var status processStatusResponse
	if err := json.Unmarshal(statusResponse.Body.Bytes(), &status); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if !status.ActionsEnabled || !status.ActionsReady || status.ActionsTokenExpiresAt == 0 {
		t.Fatalf("status action readiness = %+v", status)
	}
}

func TestActionsReadinessExpiresAndRecoversOnRefresh(t *testing.T) {
	cfg, server := actionsReadinessConfig(t, true, actionsExchangeClientForResponse(http.StatusOK, `{"token":"runtime-token","tokenType":"Bearer","expiresAt":"2099-01-01T00:00:00Z"}`))
	cfg.actionsTokenState.publish(time.Now().Add(-time.Second))
	assertActionsReadiness(t, server, http.StatusServiceUnavailable, true, false)
	if _, err := exchangeActionsToken(t.Context(), cfg); err != nil {
		t.Fatalf("refresh exchange: %v", err)
	}
	assertActionsReadiness(t, server, http.StatusOK, true, true)

	cfg.actionsHTTPClient = actionsExchangeClientForResponse(http.StatusBadGateway, `{"error":"refresh failed"}`)
	cfg.actionsTokenState.publish(time.Now().Add(-time.Second))
	if _, err := exchangeActionsToken(t.Context(), cfg); err == nil {
		t.Fatal("failed refresh unexpectedly succeeded")
	}
	assertActionsReadiness(t, server, http.StatusServiceUnavailable, true, false)
}

func TestActionsReadinessDisabledDoesNotGateCoordinator(t *testing.T) {
	cfg, server := actionsReadinessConfig(t, false, nil)
	assertActionsReadiness(t, server, http.StatusOK, false, true)
	if _, err := exchangeActionsToken(t.Context(), cfg); err == nil {
		t.Fatal("disabled actions exchange unexpectedly succeeded")
	}
}

func TestActionsTokenStateSnapshotsAreConcurrencySafe(t *testing.T) {
	state := newActionsTokenState(true)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 1000; j++ {
				_ = state.snapshot(time.Now())
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for j := 0; j < 1000; j++ {
			state.publish(time.Now().Add(time.Minute))
		}
	}()
	wg.Wait()
}
