// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/faroshq/provider-sdk/hubclient"
)

const (
	heartbeatVersion  = "0.1.0" // align with manifest.yaml spec.version
	heartbeatInterval = 30 * time.Second
)

// runHeartbeat POSTs to /api/providers/{name}/heartbeat every 30s. Skips
// silently when FAROS_HUB_URL is empty so local invocations don't need a
// hub. Mirrors providers/quickstart/main.go runHeartbeat — keep the two
// implementations aligned until the heartbeat loop moves into a shared
// provider SDK.
//
// Env:
//
//	FAROS_HUB_URL        - hub base URL (https://localhost:9443 in dev)
//	FAROS_HUB_TOKEN      - bearer token for the heartbeat request (default: the
//	                       provider SA token in FAROS_PROVIDER_KUBECONFIG)
//	FAROS_PROVIDER_NAME  - this provider's CatalogEntry name (default: infrastructure)
//	FAROS_HUB_INSECURE   - "true" → skip TLS verification (dev with self-signed certs)
func runHeartbeat(ctx context.Context) {
	hub := os.Getenv("FAROS_HUB_URL")
	name := os.Getenv("FAROS_PROVIDER_NAME")
	if name == "" {
		name = "infrastructure"
	}
	if hub == "" {
		log.Printf("heartbeat disabled (set FAROS_HUB_URL to enable)")
		return
	}

	// Resolved only once the beat is actually going to be sent: reading the
	// provider kubeconfig for a heartbeat that is disabled is wasted work, and
	// its failure logs a misleading token error in tests and local runs.
	token, err := hubclient.ResolveHubToken()
	if err != nil {
		log.Printf("heartbeat token: %v (beats will be unauthenticated)", err)
	}

	url := hub + "/api/providers/" + name + "/heartbeat"
	body, _ := json.Marshal(map[string]string{"version": heartbeatVersion, "status": "healthy"})

	client := &http.Client{Timeout: 5 * time.Second}
	if os.Getenv("FAROS_HUB_INSECURE") == "true" {
		client.Transport = &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // dev-only; opt-in via FAROS_HUB_INSECURE
		}
	}

	send := func() {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
		if err != nil {
			log.Printf("heartbeat build req: %v", err)
			return
		}
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := client.Do(req)
		if err != nil {
			log.Printf("heartbeat send: %v", err)
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 300 {
			log.Printf("heartbeat %s: %d %s", url, resp.StatusCode, resp.Status)
		}
	}
	send()

	t := time.NewTicker(heartbeatInterval)
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
