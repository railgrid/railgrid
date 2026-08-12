// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package api

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/faroshq/provider-agents/store"
	"github.com/faroshq/provider-agents/tools"
)

// Completion callbacks. A caller that neither waits nor polls can name a URL to
// be told when the run finishes.
//
// The URL is treated as caller configuration, not as a model-chosen destination:
// private addresses are allowed (an in-cluster Service is the normal target for a
// service-to-service caller) while link-local stays blocked. The caller is already
// authenticated and authorized to run the agent, so this is the same trust level
// as a Connection's baseURL — see tools.ConfiguredEndpointHTTPClient.
//
// Explicitly best-effort, and documented as such at the API: the callback fires
// from the goroutine that ran the task, so a provider restart between the run
// finishing and the POST landing drops it. Polling GET /api/runs/{id} remains the
// reliable path, and an idempotency key makes a re-invoke safe if a caller would
// rather retry than poll. Building an at-least-once outbox would mean a delivery
// queue, retries across restarts, and a dead-letter story — worth doing when a
// consumer actually needs it, not on spec.
const (
	// callbackAttempts is how many times one callback is tried before giving up.
	callbackAttempts = 3
	// callbackBackoff is the pause after the first failure; it doubles.
	callbackBackoff = 2 * time.Second
	// callbackTimeout bounds one POST.
	callbackTimeout = 10 * time.Second
	// callbackSignatureHeader carries the HMAC over the body, so a receiver can
	// tell a real callback from anything else that finds the URL.
	callbackSignatureHeader = "X-Faros-Signature"
)

// runCallback is where to report a finished run.
type runCallback struct {
	// URL receives a POST with the run's outcome. Must be absolute http(s). An
	// in-cluster address is fine (see the package comment on trust level);
	// link-local is refused.
	URL string `json:"url"`
	// Secret keys the HMAC signature over the request body. Optional but strongly
	// advised: without it a receiver cannot authenticate the callback.
	Secret string `json:"secret,omitempty"`
}

// validate reports why a callback is unusable, or nil.
func (c *runCallback) validate() error {
	if c == nil {
		return nil
	}
	raw := strings.TrimSpace(c.URL)
	if raw == "" {
		return fmt.Errorf("callback.url is required when callback is set")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return fmt.Errorf("callback.url must be an absolute http(s) URL")
	}
	c.URL = raw
	return nil
}

// runCallbackPayload is the body POSTed to the callback URL.
type runCallbackPayload struct {
	RunID   string   `json:"runId"`
	Agent   string   `json:"agent"`
	Phase   string   `json:"phase"`
	Output  string   `json:"output,omitempty"`
	Sources []string `json:"sources,omitempty"`
	Message string   `json:"message,omitempty"`
	Usage   struct {
		InputTokens  int64 `json:"inputTokens"`
		OutputTokens int64 `json:"outputTokens"`
		USDMicros    int64 `json:"usdMicros"`
	} `json:"usage"`
	FinishedAt string `json:"finishedAt,omitempty"`
}

// deliverRunCallback re-reads the finished run and POSTs its outcome. Re-reading
// rather than trusting the in-memory result keeps the callback body identical to
// what GET /api/runs/{id} would return, so a receiver that does both sees one
// story.
func (s *Server) deliverRunCallback(ctx context.Context, scope store.Scope, runID string, cb *runCallback) {
	if cb == nil {
		return
	}
	run, err := s.store.GetRun(ctx, scope, runID)
	if err != nil {
		log.Printf("callback: run %s: %v", runID, err)
		return
	}
	payload := runCallbackPayload{
		RunID: run.ID, Agent: run.AgentName, Phase: string(run.Phase),
		Output: run.Output, Sources: run.Sources, Message: run.Message,
	}
	payload.Usage.InputTokens = run.InputTokens
	payload.Usage.OutputTokens = run.OutputTokens
	payload.Usage.USDMicros = run.USDMicros
	if run.FinishedAt != nil {
		payload.FinishedAt = run.FinishedAt.UTC().Format(time.RFC3339)
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return
	}

	client := tools.ConfiguredEndpointHTTPClient()
	backoff := callbackBackoff
	for attempt := 1; attempt <= callbackAttempts; attempt++ {
		err = postCallback(ctx, client, cb, body)
		if err == nil {
			return
		}
		if attempt < callbackAttempts {
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			backoff *= 2
		}
	}
	// Say so once, with the run id: the caller's own poll is the recovery path and
	// an operator needs to be able to tell that delivery, not the run, failed.
	log.Printf("callback: giving up on %s for run %s after %d attempts: %v", cb.URL, runID, callbackAttempts, err)
}

func postCallback(ctx context.Context, client *http.Client, cb *runCallback, body []byte) error {
	reqCtx, cancel := context.WithTimeout(ctx, callbackTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, cb.URL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "faros-agents/0.1")
	if secret := strings.TrimSpace(cb.Secret); secret != "" {
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write(body)
		req.Header.Set(callbackSignatureHeader, "sha256="+hex.EncodeToString(mac.Sum(nil)))
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("callback returned HTTP %d", resp.StatusCode)
	}
	return nil
}
