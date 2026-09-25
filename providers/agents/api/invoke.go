// Copyright 2026 The Railgrid Authors.
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
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/dynamic"

	agentsv1alpha1 "github.com/railgrid/provider-agents/apis/v1alpha1"
	"github.com/railgrid/provider-agents/store"
)

// Programmatic invocation. Until now an agent could only be run by a human
// holding a stream open (chat) or by re-firing a pre-created Schedule/Trigger,
// so anything wanting to hand an agent an ad-hoc task had nowhere to go —
// RunTriggerAPI existed in the constants and was produced by nothing.
//
//	POST …/agents/{name}/run             → 202 {runId, phase} (200 when settled)
//	GET  …/agents/{name}/runs/{id}/wait  → 200 the run, once it settles or time is up
//
// The run is detached: it outlives the request that started it, so a caller may
// fire and forget, long-poll, or come back later — the answer is on the run
// record either way (…/agents/{name}/runs/{id}).
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

// invokeAgentRun serves the `run` verb: POST …/agents/{name}/run.
//
// This is the ONE way to start an unattended run, for every kind of caller.
// A signed-in user, an MCP client, another provider's ServiceAccount and a cron
// job all reach it the same way and are authorized the same way — by the two
// gates in dataplane.go, i.e. `get` on the agent plus `create` on `agents/run`
// in the tenant's own RBAC. That is what retired the bespoke /s2s/ route, its
// `agents/delegate` SubjectAccessReview, and the provider's own TokenReview
// client.
//
// WHOSE IDENTITY THE RUN THEN USES is a separate question from who was allowed
// to start it. There is no caller credential on a verb at all: the run reads
// its objects as THIS PROVIDER through the APIExport virtual workspace and
// reaches instance-backed tools the same way, exactly as a scheduled run does
// — so a caller who may start a run does not thereby lend the agent their own
// reach. Without the background plumbing (no provider kubeconfig; local dev)
// the run uses the gate's provider client, which is the only client there is.
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

	tr := taskRun{
		SessionID: strings.TrimSpace(req.SessionID), Task: req.Task,
		Trigger:        agentsv1alpha1.RunTriggerAPI,
		IdempotencyKey: req.IdempotencyKey,
		Callback:       req.Callback,
		// SourceName attributes the run to its caller in the activity view. The
		// caller's name comes from the hub's verified header when there is one
		// and is omitted otherwise — it is a label, never a trust root; the
		// gates already decided this caller may run this agent.
		SourceName: apiRunSource(id),
	}

	// Unattended: the agent acts as ITSELF, through the APIExport virtual
	// workspace, so starting a run never lends the agent the caller's reach.
	// Without that plumbing (local dev, no provider kubeconfig) the caller's
	// own credentials are the only identity available.
	var runID string
	if dyn, derr := s.backgroundScoped(r.Context(), id.clusterID); derr == nil {
		runID = s.startDetachedVWRun(r, dyn, id.clusterID, scope, agent, tr)
		log.Printf("agents: %s started run %s on agent %s in %s", tr.SourceName, runID, name, id.clusterID)
	} else {
		runID = s.startDetachedRun(r, c, id, agent, tr)
	}

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

// waitRunHandler serves the `wait` verb on a Run: GET …/runs/{id}/wait. It
// blocks until the run settles, or returns its current state when the timeout
// expires. Safe to re-issue.
//
// A caller with a kube client could watch the object instead, and should; this
// exists for the caller that has neither a watch nor a reason to hold one — a
// script, another provider, a job — and for whom one long-poll is simpler than
// a watch it has to reconnect.
func (s *Server) waitRunHandler(w http.ResponseWriter, r *http.Request) {
	_, id, ok := s.requireClient(w, r)
	if !ok {
		return
	}
	agent, ok := gatedRunAgent(r)
	if !ok {
		writeStatus(w, http.StatusConflict, "Conflict", "this run names no agent, so it cannot be located in the store")
		return
	}
	runID := r.PathValue("name")
	scope := id.scope(agent)
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

// backgroundScoped returns a dynamic client on clusterID through the APIExport
// virtual workspace, or an error when the background plumbing is not running.
func (s *Server) backgroundScoped(ctx context.Context, clusterID string) (dynamic.Interface, error) {
	if s.bg == nil {
		return nil, fmt.Errorf("no provider kubeconfig: the virtual workspace is unavailable")
	}
	return s.bg.scoped(ctx, clusterID)
}

// startDetachedVWRun starts a run through the APIExport virtual workspace, as
// the agent's own identity. The sibling of startDetachedRun for work that has
// no user behind it: same detachment and same pre-written record, different
// credentials.
func (s *Server) startDetachedVWRun(r *http.Request, dyn dynamic.Interface, clusterID string, scope store.Scope, agent *agentsv1alpha1.Agent, tr taskRun) string {
	runID := uuid.NewString()
	now := time.Now().UTC()
	tr.RunID = runID
	tr.Creds = vwSecrets{dyn}
	tr.CR = vwCR{dyn}
	tr.Scope = scope
	tr.Agent = agent
	tr.ClusterID = clusterID
	// The agent's own ServiceAccount, as for any unattended run — the caller's
	// token authorized the request, it does not become the identity the agent
	// acts with. Edges stays absent: nobody is watching (see buildToolset).
	tr.HubToken = s.bg.agentToken(r.Context(), dyn, clusterID, agent.Name)

	ctx := context.WithoutCancel(r.Context())
	_ = s.saveRun(ctx, scope, store.Run{
		ID: runID, AgentName: agent.Name, SessionID: tr.SessionID, Trigger: tr.Trigger,
		IdempotencyKey: tr.IdempotencyKey,
		Phase:          store.RunPhasePending, Input: tr.Task, CreatedAt: now, UpdatedAt: now,
	})
	go func() {
		if _, err := s.executeTask(ctx, tr); err != nil {
			log.Printf("agents: run %s on agent %s failed: %v", runID, agent.Name, err)
		}
		s.deliverRunCallback(ctx, scope, runID, tr.Callback)
	}()
	return runID
}
