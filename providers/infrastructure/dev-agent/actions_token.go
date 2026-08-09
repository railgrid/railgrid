/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	actionsTokenRefreshSafetyWindow = time.Minute
	actionsTokenRetryDelay          = 5 * time.Second
	actionsExchangeTimeout          = 30 * time.Second
	actionsResponseLimit            = 1 << 20
	actionsExchangePath             = "/api/provider-actions/workload/exchange"
	actionsBasePath                 = "/services/providers/app-studio"
)

type actionsExchangeRequest struct {
	TenantPath  string `json:"tenantPath"`
	Project     string `json:"project"`
	ProjectUID  string `json:"projectUID"`
	Environment string `json:"environment"`
	Instance    string `json:"instance"`
}

type actionsExchangeResponse struct {
	Token     string `json:"token"`
	TokenType string `json:"tokenType"`
	ExpiresAt string `json:"expiresAt"`
}

// actionsTokenState is the coordinator's in-memory readiness authority. The
// token itself remains file-backed and is published with rename semantics;
// this state records only the expiry of the last successfully published
// exchange result. A mutex keeps readiness snapshots consistent while the
// refresh goroutine publishes a new expiry.
type actionsTokenState struct {
	mu        sync.RWMutex
	enabled   bool
	expiresAt time.Time
}

type actionsTokenStatus struct {
	Enabled   bool
	Ready     bool
	ExpiresAt time.Time
}

func newActionsTokenState(enabled bool) *actionsTokenState {
	return &actionsTokenState{enabled: enabled}
}

func ensureActionsTokenState(cfg *agentConfig) *actionsTokenState {
	if cfg == nil {
		return newActionsTokenState(false)
	}
	if cfg.actionsTokenState == nil {
		cfg.actionsTokenState = newActionsTokenState(strings.TrimSpace(cfg.ActionsExchangeURL) != "")
	}
	return cfg.actionsTokenState
}

func (s *actionsTokenState) publish(expiresAt time.Time) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.expiresAt = expiresAt
	s.mu.Unlock()
}

func (s *actionsTokenState) snapshot(now time.Time) actionsTokenStatus {
	if s == nil {
		return actionsTokenStatus{Ready: true}
	}
	s.mu.RLock()
	status := actionsTokenStatus{Enabled: s.enabled, ExpiresAt: s.expiresAt}
	s.mu.RUnlock()
	status.Ready = !status.Enabled || status.ExpiresAt.After(now)
	return status
}

// exchangeActionsToken performs one bootstrap-token exchange and atomically
// publishes the returned bearer token. It intentionally never logs either
// token or the response body.
func exchangeActionsToken(ctx context.Context, cfg *agentConfig) (time.Time, error) {
	if cfg == nil {
		return time.Time{}, errors.New("actions configuration is nil")
	}
	exchangeURL, err := validateActionsExchangeEndpoint(cfg.ActionsExchangeURL, cfg.ActionsBaseURL)
	if err != nil {
		return time.Time{}, err
	}
	reqBody := actionsExchangeRequest{
		TenantPath:  strings.TrimSpace(cfg.ActionsTenantPath),
		Project:     strings.TrimSpace(cfg.ActionsProject),
		ProjectUID:  strings.TrimSpace(cfg.ActionsProjectUID),
		Environment: strings.TrimSpace(cfg.ActionsEnvironment),
		Instance:    strings.TrimSpace(cfg.ActionsInstance),
	}
	if reqBody.TenantPath == "" || reqBody.Project == "" || reqBody.ProjectUID == "" || reqBody.Environment == "" || reqBody.Instance == "" {
		return time.Time{}, errors.New("actions identity fields are required")
	}
	bootstrapPath := strings.TrimSpace(cfg.ActionsBootstrapTokenFile)
	if bootstrapPath == "" {
		return time.Time{}, errors.New("KEDGE_ACTIONS_BOOTSTRAP_TOKEN_FILE is required")
	}
	bootstrap, err := os.ReadFile(bootstrapPath)
	if err != nil {
		return time.Time{}, fmt.Errorf("read actions bootstrap token: %w", err)
	}
	bootstrapToken := strings.TrimSpace(string(bootstrap))
	if strings.HasPrefix(strings.ToLower(bootstrapToken), "bearer ") {
		bootstrapToken = strings.TrimSpace(bootstrapToken[len("Bearer "):])
	}
	if bootstrapToken == "" {
		return time.Time{}, errors.New("actions bootstrap token file is empty")
	}

	client := cfg.actionsHTTPClient
	if client == nil {
		client, err = actionsExchangeClient(cfg.ActionsCAFile)
		if err != nil {
			return time.Time{}, err
		}
	}
	// Preserve an injected transport/client for deterministic tests, but never
	// inherit its redirect policy: a redirect must not receive bootstrap
	// credentials at a second location. Clone so the caller's test client is not
	// mutated.
	clientCopy := *client
	clientCopy.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	client = &clientCopy
	body, err := json.Marshal(reqBody)
	if err != nil {
		return time.Time{}, fmt.Errorf("marshal actions exchange request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, exchangeURL, bytes.NewReader(body))
	if err != nil {
		return time.Time{}, fmt.Errorf("create actions exchange request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+bootstrapToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return time.Time{}, fmt.Errorf("actions token exchange: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	responseBody, err := io.ReadAll(io.LimitReader(resp.Body, actionsResponseLimit))
	if err != nil {
		return time.Time{}, fmt.Errorf("read actions exchange response: %w", err)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return time.Time{}, fmt.Errorf("actions token exchange returned HTTP %d", resp.StatusCode)
	}
	var exchanged actionsExchangeResponse
	if err := json.Unmarshal(responseBody, &exchanged); err != nil {
		return time.Time{}, fmt.Errorf("decode actions exchange response: %w", err)
	}
	if !strings.EqualFold(strings.TrimSpace(exchanged.TokenType), "Bearer") {
		return time.Time{}, fmt.Errorf("actions exchange returned unsupported token type %q", exchanged.TokenType)
	}
	token := strings.TrimSpace(exchanged.Token)
	if strings.HasPrefix(strings.ToLower(token), "bearer ") {
		token = strings.TrimSpace(token[len("Bearer "):])
	}
	if token == "" {
		return time.Time{}, errors.New("actions exchange returned an empty token")
	}
	expiresAt, err := parseActionsExpiry(exchanged.ExpiresAt)
	if err != nil {
		return time.Time{}, err
	}
	if !expiresAt.After(time.Now()) {
		return time.Time{}, errors.New("actions exchange returned an expired token")
	}
	if err := writeActionsTokenAtomic(cfg.ActionsTokenFile, []byte(token+"\n"), 0o600); err != nil {
		return time.Time{}, fmt.Errorf("publish actions token: %w", err)
	}
	if cfg.actionsTokenState != nil {
		cfg.actionsTokenState.publish(expiresAt)
	}
	return expiresAt, nil
}

// validateActionsExchangeEndpoint accepts only the fixed HTTPS exchange path
// on the same host as the fixed SDK base URL. Both values are operator/platform
// inputs; binding values never participate in this validation.
func validateActionsExchangeEndpoint(exchangeRaw, baseRaw string) (string, error) {
	exchangeRaw = strings.TrimSpace(exchangeRaw)
	if exchangeRaw == "" {
		return "", errors.New("KEDGE_ACTIONS_EXCHANGE_URL is required")
	}
	exchange, err := url.Parse(exchangeRaw)
	if err != nil || !exchange.IsAbs() || exchange.Host == "" || exchange.User != nil || exchange.RawQuery != "" || exchange.Fragment != "" || exchange.RawPath != "" {
		return "", errors.New("KEDGE_ACTIONS_EXCHANGE_URL must be an absolute HTTPS exchange endpoint")
	}
	if !strings.EqualFold(exchange.Scheme, "https") {
		return "", errors.New("KEDGE_ACTIONS_EXCHANGE_URL must use HTTPS")
	}
	if exchange.Path != actionsExchangePath {
		return "", fmt.Errorf("KEDGE_ACTIONS_EXCHANGE_URL must use path %q", actionsExchangePath)
	}

	baseRaw = strings.TrimRight(strings.TrimSpace(baseRaw), "/")
	if baseRaw == "" {
		return "", errors.New("KEDGE_ACTIONS_BASE_URL is required when Provider Actions are enabled")
	}
	base, err := url.Parse(baseRaw)
	if err != nil || !base.IsAbs() || base.Host == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" || base.RawPath != "" {
		return "", errors.New("KEDGE_ACTIONS_BASE_URL must be an absolute HTTPS provider base URL")
	}
	if !strings.EqualFold(base.Scheme, "https") {
		return "", errors.New("KEDGE_ACTIONS_BASE_URL must use HTTPS")
	}
	if base.Path != actionsBasePath {
		return "", fmt.Errorf("KEDGE_ACTIONS_BASE_URL must use path %q", actionsBasePath)
	}
	if !strings.EqualFold(exchange.Host, base.Host) {
		return "", errors.New("KEDGE_ACTIONS_EXCHANGE_URL host must match KEDGE_ACTIONS_BASE_URL")
	}
	return exchange.String(), nil
}

func parseActionsExpiry(raw string) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, errors.New("actions exchange returned no expiresAt")
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if value, err := time.Parse(layout, raw); err == nil {
			return value, nil
		}
	}
	return time.Time{}, fmt.Errorf("actions exchange returned invalid expiresAt %q", raw)
}

func runActionsTokenRefreshLoop(ctx context.Context, cfg *agentConfig) {
	_ = ensureActionsTokenState(cfg)
	for {
		expiresAt, err := exchangeActionsToken(ctx, cfg)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Printf("actions token exchange failed: %v", err)
			if !waitActionsTokenRefresh(ctx, actionsTokenRetryDelay) {
				return
			}
			continue
		}
		if !waitActionsTokenRefresh(ctx, actionsTokenRefreshDelay(expiresAt, time.Now())) {
			return
		}
	}
}

func actionsTokenRefreshDelay(expiresAt, now time.Time) time.Duration {
	delay := expiresAt.Sub(now) - actionsTokenRefreshSafetyWindow
	if delay < 5*time.Second {
		return 5 * time.Second
	}
	return delay
}

func waitActionsTokenRefresh(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func actionsExchangeClient(caFile string) (*http.Client, error) {
	transport, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return &http.Client{
			Timeout:       actionsExchangeTimeout,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		}, nil
	}
	transport = transport.Clone()
	caFile = strings.TrimSpace(caFile)
	if caFile != "" {
		if data, err := os.ReadFile(caFile); err == nil {
			roots, poolErr := x509.SystemCertPool()
			if poolErr != nil || roots == nil {
				roots = x509.NewCertPool()
			}
			if !roots.AppendCertsFromPEM(data) {
				return nil, fmt.Errorf("actions CA file %q contains no certificates", caFile)
			}
			transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots}
		} else if !os.IsNotExist(err) {
			return nil, fmt.Errorf("read actions CA file: %w", err)
		}
	}
	return &http.Client{
		Timeout:       actionsExchangeTimeout,
		Transport:     transport,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}, nil
}

func writeActionsTokenAtomic(path string, data []byte, mode os.FileMode) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return errors.New("actions token file path is required")
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".kedge-actions-token-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
	}()
	if err := tmp.Chmod(mode); err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	return nil
}
