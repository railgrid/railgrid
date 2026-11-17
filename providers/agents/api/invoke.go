// Copyright 2026 The Railgrid Authors.
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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/dynamic"

	agentsv1alpha1 "github.com/railgrid/provider-agents/apis/v1alpha1"
	agentsclient "github.com/railgrid/provider-agents/client"
	"github.com/railgrid/provider-agents/llm"
	"github.com/railgrid/provider-agents/store"
	"github.com/railgrid/provider-sdk/dataplane"

	runnerharness "github.com/railgrid/railgrid/pkg/runner/harness"
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
	// maxIdempotencyKeyBytes is safely below PostgreSQL's B-tree index key
	// limit, avoiding an opaque retryable storage error for oversized input.
	maxIdempotencyKeyBytes = 1024
)

type invokeRunRequest struct {
	// PreventStart atomically reserves an absent idempotency key as an Aborted
	// tombstone. If a Run already exists, it is returned unchanged for the caller
	// to cancel through its ordinary Run verb.
	PreventStart bool `json:"preventStart,omitempty"`
	// ExpectedAgentUID lets a caller fence a name recreation between reading an
	// Agent and submitting this invocation.
	ExpectedAgentUID string `json:"expectedAgentUID,omitempty"`
	// Model overrides the Agent model for this run; omitted inherits, empty selects the harness default.
	Model *string `json:"model,omitempty"`
	// ReasoningEffort follows the same inheritance rules and requires the existing run permission.
	ReasoningEffort *string `json:"reasoningEffort,omitempty"`
	// Task is the prompt the agent runs. Required.
	Task string `json:"task"`
	// SessionID selects a transcript for a general API run. An empty value
	// uses the default API transcript. Repository runs get their own transcript
	// and require this field to be empty.
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
	// Repository makes the run a REPOSITORY attempt on a harness-backed agent:
	// a fresh clone of the named commit, never the agent's workspace or a chat
	// session, exporting a Git result the `artifact` verb then serves. See
	// api/repository.go. Refused on a model-backed agent.
	Repository *invokeRepository `json:"repository,omitempty"`
	// ResultSchema asks the run's final answer to be one JSON document
	// matching this JSON Schema. The agent is told so, the answer is checked
	// when the turn completes, and a run that does not comply fails with
	// status.structuredResult saying why ("missing" or "invalid"). A valid
	// answer is kept as the structured-result.json artifact. Refused on a
	// repository run, which declares it as repository.resultSchema.
	ResultSchema json.RawMessage `json:"resultSchema,omitempty"`
	// Limits requests hard per-run allowances. Only maxOutputTokens is
	// enforceable by the in-process model backend; maxUSD is rejected explicitly.
	Limits *invokeRunLimits `json:"runLimits,omitempty"`
	// modelUseDelegated is set only by the start-run/v1 action, never by a
	// request body: the run spends the agent's configured models without
	// asking the caller for `use` on their provider connection. The `run`
	// verb leaves it false, so its caller is still asked.
	modelUseDelegated bool
}

type invokeRunLimits struct {
	MaxOutputTokens *int64 `json:"maxOutputTokens,omitempty"`
	MaxUSD          *int64 `json:"maxUSD,omitempty"`
}

func invocationFingerprint(agentUID string, req *invokeRunRequest) string {
	copy := *req
	copy.PreventStart = false
	copy.Wait = 0
	copy.IdempotencyKey = ""
	copy.ExpectedAgentUID = ""
	if copy.Repository != nil {
		repository := *copy.Repository
		if repository.CloneSource != nil {
			clone := *repository.CloneSource
			clone.Token = ""
			repository.CloneSource = &clone
		}
		if len(repository.ResultSchema) > 0 {
			repository.ResultSchema = canonicalJSON(repository.ResultSchema)
		}
		if len(repository.ApprovedInput) > 0 {
			repository.ApprovedInput = canonicalJSON(repository.ApprovedInput)
		}
		copy.Repository = &repository
	}
	if len(copy.ResultSchema) > 0 {
		copy.ResultSchema = canonicalJSON(copy.ResultSchema)
	}
	data, _ := json.Marshal(struct {
		AgentUID string           `json:"agentUID"`
		Request  invokeRunRequest `json:"request"`
	}{agentUID, copy})
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func canonicalJSON(raw json.RawMessage) json.RawMessage {
	var value any
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if d.Decode(&value) != nil {
		return raw
	}
	data, err := json.Marshal(value)
	if err != nil {
		return raw
	}
	return data
}

// agentMissingError marks the one NotFound from invokeRun that means the
// addressed agent does not exist. It reads and classifies exactly like the
// error it wraps, so the run verb's response is unchanged; an action uses it
// to tell a missing agent from a missing dependency of one.
type agentMissingError struct{ error }

func (e agentMissingError) Unwrap() error { return e.error }

type invokeRunResponse struct {
	RunID string `json:"runId"`
	Phase string `json:"phase"`
	// Reused reports that an idempotency key matched an existing run, so the
	// caller knows this response is not a new unit of work.
	Reused bool `json:"reused,omitempty"`
	// Prevented confirms that an absent-key start fence was durably recorded.
	// It is distinct from Reused: an existing admitted run may also be returned.
	Prevented bool `json:"prevented,omitempty"`
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
	var req invokeRunRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeStatus(w, http.StatusBadRequest, "BadRequest", "invalid JSON body: "+err.Error())
		return
	}
	status, resp, err := s.invokeRun(r, c, id, r.PathValue("name"), req)
	if err != nil {
		writeResourceError(w, err)
		return
	}
	writeJSON(w, status, resp)
}

// invokeRun is the whole of starting an unattended run, for the `run` verb
// and the catalogued `start-run` action alike: validation, the idempotency
// fence, admission and the optional inline wait. It writes no response, so
// the two transports cannot drift in what they admit. A refusal is a
// *requestError (or a tenant API error) carrying its own HTTP class; success
// is the status the verb answers with — 202 while the run is going, 200 once
// it has settled or a key matched a run already started.
func (s *Server) invokeRun(r *http.Request, c *agentsclient.Client, id identity, name string, req invokeRunRequest) (int, invokeRunResponse, error) {
	unavailable := func(msg string) error {
		return &requestError{http.StatusServiceUnavailable, "ServiceUnavailable", msg}
	}
	req.Task = strings.TrimSpace(req.Task)
	if req.Task == "" && !req.PreventStart {
		return 0, invokeRunResponse{}, errBadRequest("task is required")
	}
	req.IdempotencyKey = strings.TrimSpace(req.IdempotencyKey)
	if err := validateIdempotencyKey(req.IdempotencyKey); err != nil {
		return 0, invokeRunResponse{}, errBadRequest(err.Error())
	}
	if len(req.ExpectedAgentUID) > 128 {
		return 0, invokeRunResponse{}, errBadRequest("expectedAgentUID is too long")
	}
	if err := req.Callback.validate(); err != nil {
		return 0, invokeRunResponse{}, errBadRequest(err.Error())
	}
	repository, err := req.Repository.validate()
	if err != nil {
		return 0, invokeRunResponse{}, errBadRequest(err.Error())
	}
	limits, err := req.Limits.validate()
	if err != nil {
		return 0, invokeRunResponse{}, errBadRequest(err.Error())
	}
	if len(req.ResultSchema) > 0 {
		if repository != nil {
			return 0, invokeRunResponse{}, errBadRequest("a repository run declares its schema as repository.resultSchema")
		}
		if err := validateResultSchema(req.ResultSchema); err != nil {
			return 0, invokeRunResponse{}, errBadRequest("resultSchema " + err.Error())
		}
	}
	if repository != nil && strings.TrimSpace(req.SessionID) != "" {
		return 0, invokeRunResponse{}, errBadRequest("a repository run is a fresh checkout and cannot continue a session")
	}
	if req.PreventStart && req.IdempotencyKey == "" {
		return 0, invokeRunResponse{}, errBadRequest("preventStart requires idempotencyKey")
	}

	agent, err := c.Agents().Get(r.Context(), name, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			// Marked, because other reads below 404 too (the agent's model
			// credential), and only this one means the agent itself is gone.
			err = agentMissingError{err}
		}
		return 0, invokeRunResponse{}, err
	}
	if limits != nil && (repository != nil || agent.Spec.Harness() != nil) {
		return 0, invokeRunResponse{}, errBadRequest("maxOutputTokens is not supported by harness-backed runs")
	}
	if req.ExpectedAgentUID != "" && req.ExpectedAgentUID != string(agent.UID) {
		return 0, invokeRunResponse{}, errConflict("Agent identity changed since the invocation was prepared")
	}
	if repository != nil && !agent.Spec.HarnessBacked() {
		// Only a coding harness on an edge can clone a repository and export a
		// result; an in-process model turn has no checkout to run in.
		return 0, invokeRunResponse{}, errBadRequest("this agent cannot run against a repository")
	}
	scope := id.scope(name)
	fingerprint := invocationFingerprint(string(agent.UID), &req)
	if req.PreventStart {
		existing, admitted, err := s.store.PreventStartRun(r.Context(), scope, req.IdempotencyKey, string(agent.UID), fingerprint, time.Now().UTC())
		if errors.Is(err, store.ErrInvocationAgentChanged) {
			return 0, invokeRunResponse{}, errConflict("idempotencyKey belongs to a different Agent identity")
		}
		if err != nil {
			return 0, invokeRunResponse{}, unavailable("the invocation could not be fenced; retry or check the run")
		}
		if !admitted {
			s.projectRun(r.Context(), scope, existing)
		}
		resp, ownerErr := reusedInvocationResponse(existing, string(agent.UID))
		if ownerErr != nil {
			return 0, invokeRunResponse{}, errConflict(ownerErr.Error())
		}
		resp.Reused = admitted
		if detail, derr := s.runDetailFor(r.Context(), scope, existing.ID); derr == nil {
			resp.Run = &detail
		}
		return http.StatusOK, resp, nil
	}

	// A retried delivery must not start the work again.
	if req.IdempotencyKey != "" {
		existing, found, lookupErr := s.store.FindRunByIdempotencyKey(r.Context(), scope, req.IdempotencyKey)
		if lookupErr != nil {
			return 0, invokeRunResponse{}, unavailable("the previous run could not be checked; retry later")
		}
		if found {
			return s.reusedInvocation(r.Context(), scope, existing, string(agent.UID), fingerprint)
		}
	}

	settings, err := invokeHarnessSettings(agent, req.Model, req.ReasoningEffort)
	if err != nil {
		return 0, invokeRunResponse{}, errBadRequest(err.Error())
	}
	if settings != nil && settings.ReasoningEffort != "" {
		// Read only credential metadata as the provider, just as execution
		// does. Reject unsupported harnesses before creating a durable Run;
		// the runner still checks model-specific support at dispatch time.
		cred, err := c.GetModelCredential(r.Context(), strings.TrimSpace(agent.Spec.Harness().CredentialRef))
		if err != nil {
			return 0, invokeRunResponse{}, err
		}
		// A delegated run is not the caller's use of the connection, at
		// admission any more than in its turns: resolve it as an unattended
		// run does, through the provider's view with no caller attached.
		resolveCtx, resolver := r.Context(), llm.SecretGetter(c)
		if gate, ok := gateFrom(r.Context()); ok && req.modelUseDelegated {
			resolveCtx, resolver = dataplane.WithoutProxiedIdentity(r.Context()), vwSecrets{dyn: gate.provider}
		}
		cred, err = llm.ResolveModelCredentialForRuntime(resolveCtx, resolver, cred)
		if err != nil {
			return 0, invokeRunResponse{}, err
		}
		if err := runnerharness.ValidateReasoningEffort(llm.HarnessAdvertisedName(cred.Spec.Provider), settings.ReasoningEffort); err != nil {
			return 0, invokeRunResponse{}, errBadRequest(err.Error())
		}
	}
	tr := taskRun{
		HarnessSettings: settings,
		Limits:          limits,
		SessionID:       strings.TrimSpace(req.SessionID), Task: taskWithResultSchema(req.Task, req.ResultSchema),
		Trigger:               agentsv1alpha1.RunTriggerAPI,
		IdempotencyKey:        req.IdempotencyKey,
		InvocationFingerprint: fingerprint,
		InvocationAgentUID:    string(agent.UID),
		Callback:              req.Callback,
		Repository:            repository,
		ResultSchema:          canonicalResultSchema(req.ResultSchema),
		ModelUseDelegated:     req.modelUseDelegated,
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
	var admission runAdmission
	if dyn, derr := s.backgroundScoped(r.Context(), id.clusterID); derr == nil {
		// The agent's own ServiceAccount, as for any unattended run — the
		// caller's token authorized the request, it does not become the identity
		// the agent acts with. Edges stays absent: nobody is watching (see
		// buildToolset).
		admission, err = s.startRun(context.WithoutCancel(r.Context()), scope, agent, tr,
			s.bg.agentAccess(r.Context(), dyn, id.clusterID, agent.Name))
	} else {
		admission, err = s.startDetachedRun(r, c, id, agent, tr)
	}

	if err != nil {
		return 0, invokeRunResponse{}, unavailable("the run could not be recorded; retry later")
	}
	if admission.Reused && req.IdempotencyKey != "" {
		existing, found, lookupErr := s.store.FindRunByIdempotencyKey(r.Context(), scope, req.IdempotencyKey)
		if lookupErr != nil {
			return 0, invokeRunResponse{}, unavailable("the previous run could not be checked; retry later")
		}
		if found {
			// Another request admitted this key first: answer exactly as a
			// retry that found it up front would.
			return s.reusedInvocation(r.Context(), scope, existing, string(agent.UID), fingerprint)
		}
	}
	runID := admission.ID
	if !admission.Reused {
		log.Printf("agents: %s started run %s on agent %s in %s", tr.SourceName, runID, name, id.clusterID)
	}
	// A caller that asked to wait gets the settled run inline; one that did not
	// gets the id to poll. Either way the run is already going.
	if req.Wait > 0 {
		wait := min(time.Duration(req.Wait)*time.Second, invokeMaxWait)
		if run, settled := s.waitForRun(r.Context(), scope, runID, wait); settled {
			resp := invokeRunResponse{RunID: runID, Phase: string(run.Phase), Reused: admission.Reused}
			if detail, derr := s.runDetailFor(r.Context(), scope, runID); derr == nil {
				resp.Run = &detail
			}
			return http.StatusOK, resp, nil
		}
		// Out of time, not out of luck: the run continues and the caller polls.
		return http.StatusAccepted, invokeRunResponse{RunID: runID, Phase: string(store.RunPhaseRunning), Reused: admission.Reused}, nil
	}
	return http.StatusAccepted, invokeRunResponse{RunID: runID, Phase: string(admission.Phase), Reused: admission.Reused}, nil
}

// reusedInvocation answers a request whose idempotency key already names a
// run: 200 with the run's detail, or a 409 when the key belongs to another
// Agent or was used with different input. A prevented key matches any input.
func (s *Server) reusedInvocation(ctx context.Context, scope store.Scope, existing store.Run, agentUID, fingerprint string) (int, invokeRunResponse, error) {
	resp, ownerErr := reusedInvocationResponse(existing, agentUID)
	if ownerErr != nil {
		return 0, invokeRunResponse{}, errConflict(ownerErr.Error())
	}
	if !existing.InvocationPrevented && existing.InvocationFingerprint != "" && existing.InvocationFingerprint != fingerprint {
		return 0, invokeRunResponse{}, errConflict("idempotencyKey was already used with different invocation input")
	}
	resp.Reused = true
	if detail, derr := s.runDetailFor(ctx, scope, existing.ID); derr == nil {
		resp.Run = &detail
	}
	return http.StatusOK, resp, nil
}

func reusedInvocationResponse(run store.Run, agentUID string) (invokeRunResponse, error) {
	if run.InvocationAgentUID != "" && run.InvocationAgentUID != agentUID {
		return invokeRunResponse{}, store.ErrInvocationAgentChanged
	}
	return invokeRunResponse{
		RunID: run.ID, Phase: string(run.Phase), Reused: true, Prevented: run.InvocationPrevented,
	}, nil
}

func validateIdempotencyKey(key string) error {
	if len(key) > maxIdempotencyKeyBytes {
		return fmt.Errorf("idempotencyKey must be at most 1024 bytes")
	}
	return nil
}

func (l *invokeRunLimits) validate() (*agentsv1alpha1.RunLimits, error) {
	if l == nil {
		return nil, nil
	}
	if l.MaxUSD != nil {
		return nil, fmt.Errorf("hard maxUSD limits are not currently enforceable")
	}
	if l.MaxOutputTokens == nil {
		return nil, fmt.Errorf("runLimits must include maxOutputTokens")
	}
	if *l.MaxOutputTokens < 1 || *l.MaxOutputTokens > 10_000_000 {
		return nil, fmt.Errorf("runLimits.maxOutputTokens must be between 1 and 10000000")
	}
	return &agentsv1alpha1.RunLimits{MaxOutputTokens: *l.MaxOutputTokens}, nil
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

// canonicalResultSchema is the schema as the run stores it: re-encoded once,
// so the stored bytes do not carry the caller's whitespace. Nil stays nil.
func canonicalResultSchema(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	return canonicalJSON(raw)
}

// taskWithResultSchema appends the answer contract to the task the agent is
// given. It is part of the visible task on purpose: whoever reads the
// transcript sees exactly what the agent was asked to return.
func taskWithResultSchema(task string, schema json.RawMessage) string {
	if len(schema) == 0 {
		return task
	}
	return task + "\n\n---\nYour final answer must be exactly one JSON document, with no other text, that validates against this JSON Schema:\n\n" + string(canonicalJSON(schema))
}
