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
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/go-logr/logr"
	"k8s.io/klog/v2"
)

const (
	// EnvHubURL is the hub base URL (https://localhost:9443 in dev). Empty
	// disables the heartbeat, which is what tests and dry runs want.
	EnvHubURL = "FAROS_HUB_URL"
	// EnvProviderName is this provider's CatalogEntry name.
	EnvProviderName = "FAROS_PROVIDER_NAME"
	// EnvProviderVersion lets a chart override the version the heartbeat
	// reports so a separately packaged binary matches its CatalogEntry/image.
	EnvProviderVersion = "FAROS_PROVIDER_VERSION"
	// EnvHubInsecure set to exactly "true" skips TLS verification of the hub.
	// Dev-only, for self-signed certificates.
	EnvHubInsecure = "FAROS_HUB_INSECURE"

	// DefaultHeartbeatInterval is how often a provider beats. The hub's TTL
	// is ~90s, so three missed beats flip the provider to NotReady.
	DefaultHeartbeatInterval = 30 * time.Second
	// DefaultHeartbeatTimeout bounds a single POST.
	DefaultHeartbeatTimeout = 5 * time.Second

	// maxHeartbeatErrorBody bounds how much of a rejection body is logged.
	maxHeartbeatErrorBody = 512
)

// HeartbeatConfig describes one provider's heartbeat to the hub.
type HeartbeatConfig struct {
	// HubURL is the hub base URL. Empty disables the heartbeat.
	HubURL string
	// ProviderName is the CatalogEntry name the beat is posted for.
	ProviderName string
	// Version is reported in the beat body and shows up as
	// CatalogEntry.status.reportedVersion.
	Version string
	// Token is the bearer presented to the hub (see ResolveHubToken). Empty
	// sends unauthenticated beats, which the hub logs (warn) or rejects
	// (enforce).
	Token string
	// Interval between beats; DefaultHeartbeatInterval when zero.
	Interval time.Duration
	// Timeout for a single POST; DefaultHeartbeatTimeout when zero.
	Timeout time.Duration
	// Insecure skips TLS verification of the hub. Dev-only.
	Insecure bool
	// Logger receives send failures and rejections. A zero Logger falls
	// back to klog.
	Logger logr.Logger
	// CanSend, when set, is consulted before every beat and a false result
	// skips that beat. Providers with a required controller use it so the
	// hub's TTL marks them stale while the controller is not ready: the hub
	// records any received beat as liveness and ignores the body's status.
	CanSend func() bool
}

// ConfigFromEnv builds a HeartbeatConfig from the environment every provider
// chart already sets: FAROS_HUB_URL, FAROS_PROVIDER_NAME (defaultName when
// unset), FAROS_HUB_INSECURE, FAROS_PROVIDER_VERSION (version when unset),
// and the bearer from ResolveHubToken (FAROS_HUB_TOKEN, else the token in
// FAROS_PROVIDER_KUBECONFIG).
//
// A token-resolution failure is returned together with a usable config whose
// Token is empty, so a caller can log the error and still run the heartbeat
// unauthenticated rather than not at all.
//
// An empty FAROS_HUB_URL disables the heartbeat, so the token is not resolved
// at all in that case: reading the provider kubeconfig for a beat that will
// never be sent is wasted work, and its failure logs a misleading token error
// in tests and local runs. The returned config is still usable — RunHeartbeat
// takes its disabled path on the empty HubURL.
func ConfigFromEnv(defaultName, version string) (HeartbeatConfig, error) {
	cfg := HeartbeatConfig{
		HubURL:       strings.TrimRight(strings.TrimSpace(os.Getenv(EnvHubURL)), "/"),
		ProviderName: strings.TrimSpace(os.Getenv(EnvProviderName)),
		Version:      strings.TrimSpace(os.Getenv(EnvProviderVersion)),
		Insecure:     os.Getenv(EnvHubInsecure) == "true",
	}
	if cfg.ProviderName == "" {
		cfg.ProviderName = defaultName
	}
	if cfg.Version == "" {
		cfg.Version = version
	}
	if cfg.HubURL == "" {
		return cfg, nil
	}
	token, err := ResolveHubToken()
	cfg.Token = token
	return cfg, err
}

// RunHeartbeat POSTs to {HubURL}/api/providers/{ProviderName}/heartbeat once
// immediately and then every Interval until ctx is done. It returns at once
// when HubURL is empty. Failures are logged and the loop keeps going: losing
// a beat only means the hub flips the provider to NotReady until the next
// successful one. A 401 or 403 from the hub means the beat was not accepted
// as the provider's own service account and is logged with what to fix.
func RunHeartbeat(ctx context.Context, cfg HeartbeatConfig) {
	log := cfg.Logger
	if log.GetSink() == nil {
		log = klog.Background()
	}
	log = log.WithName("heartbeat")
	if cfg.HubURL == "" {
		log.Info("heartbeat disabled (set " + EnvHubURL + " to enable)")
		return
	}
	if cfg.Interval <= 0 {
		cfg.Interval = DefaultHeartbeatInterval
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = DefaultHeartbeatTimeout
	}

	url := strings.TrimRight(cfg.HubURL, "/") + "/api/providers/" + cfg.ProviderName + "/heartbeat"
	body, err := json.Marshal(map[string]string{"version": cfg.Version, "status": "healthy"})
	if err != nil {
		log.Error(err, "heartbeat encode")
		return
	}

	client := &http.Client{Timeout: cfg.Timeout}
	if cfg.Insecure {
		client.Transport = &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // dev-only; opt-in via FAROS_HUB_INSECURE
		}
	}

	send := func() {
		if cfg.CanSend != nil && !cfg.CanSend() {
			return
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
		if err != nil {
			log.Error(err, "heartbeat build req")
			return
		}
		req.Header.Set("Content-Type", "application/json")
		if cfg.Token != "" {
			req.Header.Set("Authorization", "Bearer "+cfg.Token)
		}
		resp, err := client.Do(req)
		if err != nil {
			if ctx.Err() == nil {
				log.Error(err, "heartbeat send", "url", url)
			}
			return
		}
		defer resp.Body.Close()
		switch {
		case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
			reason, _ := io.ReadAll(io.LimitReader(resp.Body, maxHeartbeatErrorBody))
			log.Info("heartbeat rejected: the hub did not accept the bearer as this provider's own service account, so the provider will go stale; set "+EnvHubToken+" or mount "+EnvProviderKubeconfig+" with the kubeconfig the hub minted for this provider",
				"url", url, "status", resp.StatusCode, "provider", cfg.ProviderName, "tokenConfigured", cfg.Token != "", "reason", strings.TrimSpace(string(reason)))
		case resp.StatusCode >= 300:
			log.Info("heartbeat non-2xx", "url", url, "status", resp.StatusCode)
		}
	}

	// First beat immediately so the hub sees the provider as soon as its
	// CatalogEntry exists; the rest on the ticker.
	send()
	t := time.NewTicker(cfg.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			send()
		}
	}
}
