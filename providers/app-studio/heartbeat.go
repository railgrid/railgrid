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
)

const (
	heartbeatVersion  = "0.1.0"
	heartbeatInterval = 30 * time.Second
)

// runHeartbeat emits heartbeats only while the provider is eligible to be
// considered healthy by the hub. The hub currently ignores the heartbeat body
// status, so a required controller that is starting, failed, or stopped must
// stop sending beats and let the hub's TTL mark the provider stale. A
// nil/omitted health dependency remains compatible with REST-only callers.
func runHeartbeat(ctx context.Context, healthStates ...*controllerHealth) {
	hub := os.Getenv("FAROS_HUB_URL")
	token := os.Getenv("FAROS_HUB_TOKEN")
	name := os.Getenv("FAROS_PROVIDER_NAME")
	if name == "" {
		name = "app-studio"
	}
	if hub == "" {
		log.Printf("heartbeat disabled (set FAROS_HUB_URL to enable)")
		return
	}

	url := hub + "/api/providers/" + name + "/heartbeat"
	var health *controllerHealth
	if len(healthStates) > 0 {
		health = healthStates[0]
	}

	client := &http.Client{Timeout: 5 * time.Second}
	if os.Getenv("FAROS_HUB_INSECURE") == "true" {
		client.Transport = &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // dev-only; opt-in via FAROS_HUB_INSECURE
		}
	}

	send := func() {
		if !heartbeatCanSend(health) {
			return
		}
		body, err := json.Marshal(map[string]string{"version": heartbeatVersion, "status": "healthy"})
		if err != nil {
			log.Printf("heartbeat encode: %v", err)
			return
		}
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
		defer func() {
			if err := resp.Body.Close(); err != nil {
				log.Printf("heartbeat response close: %v", err)
			}
		}()
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

// heartbeatCanSend is intentionally stricter than the payload contract: the
// hub records any received heartbeat as liveness and does not inspect its
// status field. REST-only mode (or a legacy caller without a health dependency)
// is always eligible; a required controller must already be running.
func heartbeatCanSend(health *controllerHealth) bool {
	return health == nil || health.ready()
}
