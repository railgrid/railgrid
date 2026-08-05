// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	agentsv1alpha1 "github.com/faroshq/provider-agents/apis/v1alpha1"
	"github.com/faroshq/provider-agents/store"
)

// Programmatic invocation. Until now an agent could only be run by a human
// holding a stream open (chat) or by re-firing a pre-created Schedule/Trigger,
// so anything wanting to hand an agent an ad-hoc task had nowhere to go —
// RunTriggerAPI existed in the constants and was produced by nothing.
//
//	POST /api/agents/{name}/runs   → 202 {runId, phase}   (or 200 when settled)
//	GET  /api/runs/{id}/wait       → 200 the run, once it settles or time is up
//
// The run is detached: it outlives the request that started it, so a caller may
// fire and forget, long-poll, or come back later — the answer is on the run
// record either way (GET /api/runs/{id}).
const (
	// invokeMaxWait bounds an inline wait. Past this a caller should poll: holding
	// a request open for a long research run wastes a connection on both ends and
	// dies to any proxy in between.
	invokeMaxWait = 120 * time.Second
	// waitMaxTimeout bounds the dedicated long-poll, which exists for exactly this
	// and is cheap to re-issue.
	waitMaxTimeout = 300 * time.Second
	// waitPollInterval is how often a wait re-reads the run. Deliberately the
	// STORE and not the event bus: the run may be executing on another replica,
	// and Postgres is the only thing both replicas agree on.
	waitPollInterval = 500 * time.Millisecond
)

type invokeRunRequest struct {
	// Task is the prompt the agent runs. Required.
	Task string `json:"task"`
	// SessionID continues an existing conversation; empty starts a fresh one, so
	// unrelated API calls do not accumulate into one context.
	SessionID string `json:"sessionId,omitempty"`
	// IdempotencyKey de-duplicates retries: the same key returns the run it
	// already started instead of doing the work twice.
	IdempotencyKey string `json:"idempotencyKey,omitempty"`
	// Wait, in seconds, holds the response until the run settles (capped at
	// invokeMaxWait). 0 returns as soon as the run is accepted.
	Wait int `json:"wait,omitempty"`
	// Callback names a URL to POST the outcome to when the run finishes.
	// Best-effort — see api/callback.go; polling stays the reliable path.
	Callback *runCallback `json:"callback,omitempty"`
}

type invokeRunResponse struct {
	RunID string `json:"runId"`
	Phase string `json:"phase"`
	// Reused reports that an idempotency key matched an existing run, so the
	// caller knows this response is not a new unit of work.
	Reused bool `json:"reused,omitempty"`
	// Run carries the full record once the run has settled (a wait that paid off).
	Run *runDetail `json:"run,omitempty"`
}

// invokeAgentRun serves POST /api/agents/{name}/runs.
func (s *Server) invokeAgentRun(w http.ResponseWriter, r *http.Request) {
	c, id, ok := s.requireClient(w, r)
	if !ok {
		return
	}
	name := r.PathValue("name")

	var req invokeRunRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeStatus(w, http.StatusBadRequest, "BadRequest", "invalid JSON body: "+err.Error())
		return
	}
	req.Task = strings.TrimSpace(req.Task)
	if req.Task == "" {
		writeStatus(w, http.StatusBadRequest, "BadRequest", "task is required")
		return
	}
	req.IdempotencyKey = strings.TrimSpace(req.IdempotencyKey)
	if err := req.Callback.validate(); err != nil {
		writeStatus(w, http.StatusBadRequest, "BadRequest", err.Error())
		return
	}

	agent, err := c.Agents().Get(r.Context(), name, metav1.GetOptions{})
	if err != nil {
		writeResourceError(w, err)
		return
	}
	scope := id.scope(name)

	// A retried delivery must not start the work again.
	if req.IdempotencyKey != "" {
		if existing, found, ferr := s.store.FindRunByIdempotencyKey(r.Context(), scope, req.IdempotencyKey); ferr == nil && found {
			resp := invokeRunResponse{RunID: existing.ID, Phase: string(existing.Phase), Reused: true}
			if detail, derr := s.runDetailFor(r.Context(), scope, existing.ID); derr == nil {
				resp.Run = &detail
			}
			writeJSON(w, http.StatusOK, resp)
			return
		}
	}

	sessionID := strings.TrimSpace(req.SessionID)
	runID := s.startDetachedRun(r, c, id, agent, taskRun{
		SessionID: sessionID, Task: req.Task,
		Trigger:        agentsv1alpha1.RunTriggerAPI,
		IdempotencyKey: req.IdempotencyKey,
		Callback:       req.Callback,
		// SourceName attributes the run to its caller in the activity view; the
		// hub resolves the identity, so this is not self-asserted.
		SourceName: apiRunSource(id),
	})

	// A caller that asked to wait gets the settled run inline; one that did not
	// gets the id to poll. Either way the run is already going.
	if req.Wait > 0 {
		wait := min(time.Duration(req.Wait)*time.Second, invokeMaxWait)
		if run, settled := s.waitForRun(r.Context(), scope, runID, wait); settled {
			resp := invokeRunResponse{RunID: runID, Phase: string(run.Phase)}
			if detail, derr := s.runDetailFor(r.Context(), scope, runID); derr == nil {
				resp.Run = &detail
			}
			writeJSON(w, http.StatusOK, resp)
			return
		}
		// Out of time, not out of luck: the run continues and the caller polls.
		writeJSON(w, http.StatusAccepted, invokeRunResponse{RunID: runID, Phase: string(store.RunPhaseRunning)})
		return
	}
	writeJSON(w, http.StatusAccepted, invokeRunResponse{RunID: runID, Phase: string(store.RunPhasePending)})
}

// apiRunSource labels an API-invoked run with who asked for it, falling back to
// a generic label when the hub did not resolve a user (a ServiceAccount caller).
func apiRunSource(id identity) string {
	if u := strings.TrimSpace(id.user); u != "" {
		return "api:" + u
	}
	return "api"
}

// waitRunHandler serves GET /api/runs/{id}/wait: block until the run settles, or
// return its current state when the timeout expires. Safe to re-issue.
func (s *Server) waitRunHandler(w http.ResponseWriter, r *http.Request) {
	_, id, ok := s.requireClient(w, r)
	if !ok {
		return
	}
	runID := r.PathValue("id")
	scope := id.scope("")
	// Confirm the run exists (and is this tenant's) before holding the request.
	if _, err := s.store.GetRun(r.Context(), scope, runID); err != nil {
		writeStatus(w, http.StatusNotFound, "NotFound", err.Error())
		return
	}

	timeout := 60 * time.Second
	if v, err := strconv.Atoi(strings.TrimSpace(r.URL.Query().Get("timeoutSeconds"))); err == nil && v > 0 {
		timeout = min(time.Duration(v)*time.Second, waitMaxTimeout)
	}
	s.waitForRun(r.Context(), scope, runID, timeout)

	detail, err := s.runDetailFor(r.Context(), scope, runID)
	if err != nil {
		writeStatus(w, http.StatusNotFound, "NotFound", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

// waitForRun polls the run until it settles — a terminal phase, or
// PendingApproval, which is settled from the caller's point of view because
// nothing more happens until a human acts. Reports whether it settled in time.
func (s *Server) waitForRun(ctx context.Context, scope store.Scope, runID string, timeout time.Duration) (store.Run, bool) {
	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(waitPollInterval)
	defer ticker.Stop()
	for {
		run, err := s.store.GetRun(ctx, scope, runID)
		if err == nil && runSettled(run.Phase) {
			return run, true
		}
		if time.Now().After(deadline) {
			return run, false
		}
		select {
		case <-ctx.Done():
			return run, false
		case <-ticker.C:
		}
	}
}

// runSettled reports whether a run has stopped moving on its own.
func runSettled(phase store.RunPhase) bool {
	switch phase {
	case store.RunPhaseSucceeded, store.RunPhaseFailed, store.RunPhaseAborted, store.RunPhasePendingApproval:
		return true
	}
	return false
}
