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
	"encoding/base64"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"
	"time"

	agentsv1alpha1 "github.com/railgrid/provider-agents/apis/v1alpha1"
	agentsclient "github.com/railgrid/provider-agents/client"
	"github.com/railgrid/provider-agents/store"
)

// runSummary is the list-view shape of a run. Class derives from the trigger
// (interactive vs background) so the portal can filter without knowing the
// trigger taxonomy.
type runSummary struct {
	ID          string `json:"id"`
	Agent       string `json:"agent"`
	SessionID   string `json:"sessionID,omitempty"`
	Trigger     string `json:"trigger"`
	Class       string `json:"class"`
	ParentRunID string `json:"parentRunID,omitempty"`
	Phase       string `json:"phase"`
	Attempt     int    `json:"attempt,omitempty"`
	// Retries is how many times the run was handed back to the queue after a
	// transient refusal, and RetryAt when a queued one is picked up again.
	Retries      int        `json:"retries,omitempty"`
	RetryAt      *time.Time `json:"retryAt,omitempty"`
	InputPreview string     `json:"inputPreview,omitempty"`
	Message      string     `json:"message,omitempty"`
	// HasOutput reports that this run produced an answer, fetchable from
	// GET /api/runs/{id}. Lists carry the flag rather than the text.
	HasOutput    bool       `json:"hasOutput,omitempty"`
	InputTokens  int64      `json:"inputTokens"`
	OutputTokens int64      `json:"outputTokens"`
	USDMicros    int64      `json:"usdMicros"`
	CreatedAt    time.Time  `json:"createdAt"`
	StartedAt    *time.Time `json:"startedAt,omitempty"`
	FinishedAt   *time.Time `json:"finishedAt,omitempty"`
	DurationMS   int64      `json:"durationMS,omitempty"`
	// WorkedDurationMS is measured model/tool time. It is kept separate from
	// DurationMS, which is the wall-clock elapsed run duration.
	WorkedDurationMS *int64 `json:"workedDurationMS,omitempty"`
	// Backend is WHERE this run's turn executed: "model" for an in-process
	// turn, "harness" for one on a coding harness on an edge. Absent on rows
	// written before an agent could have a backend, which read as "model".
	//
	// A list carries it because the two fail in different places and are
	// otherwise indistinguishable: a harness-backed run that never produced
	// output is a question about a machine, not about a model endpoint.
	Backend string `json:"backend,omitempty"`
}

// runStep is one tool call in a run's trace.
type runStep struct {
	ID             string    `json:"id"`
	Tool           string    `json:"tool"`
	Args           string    `json:"args,omitempty"`
	Result         string    `json:"result,omitempty"`
	Outcome        string    `json:"outcome"`
	Error          string    `json:"error,omitempty"`
	DurationMS     int64     `json:"durationMS,omitempty"`
	ExitCode       *int64    `json:"exitCode,omitempty"`
	OutcomeUnknown bool      `json:"outcomeUnknown,omitempty"`
	At             time.Time `json:"at"`
}

// runDetail is the full trace view of one run. Steps and Children are always
// present as arrays (never null/absent) so a client can map over them without
// guarding every empty case.
type runDetail struct {
	runSummary
	Input string `json:"input,omitempty"`
	// Progress overlays incomplete presentation rows on the saved transcript.
	Progress []store.Message `json:"progress,omitempty"`
	// Output is the run's answer, so a caller that polled the phase reads the
	// result from the same object rather than the session transcript.
	Output   string       `json:"output,omitempty"`
	Sources  []string     `json:"sources,omitempty"`
	Pending  *pendingInfo `json:"pending,omitempty"`
	Steps    []runStep    `json:"steps"`
	Children []runSummary `json:"children"`
	// Harness is present only for a harness-backed run, and carries the
	// coordinates somebody debugging it needs: the runner attempt this run IS,
	// and the harness session its turn ran in. Without them a failed run can be
	// seen but not correlated with anything on the machine that ran it.
	Harness         *runHarnessInfo        `json:"harness,omitempty"`
	HarnessSettings *store.HarnessSettings `json:"harnessSettings,omitempty"`
	// Repository and Result are present only for a repository run: what it ran
	// against, and — once it succeeded — the verified Git result whose
	// artifacts the `artifact` verb serves. Same shapes as the Run object's
	// spec.repository and status.result.
	Repository *store.RunRepository `json:"repository,omitempty"`
	Result     *store.RunResult     `json:"result,omitempty"`
}

// runHarnessInfo is the edge-side identity of one harness-backed run.
type runHarnessInfo struct {
	// AttemptID is the runner attempt, which is the run's own id by
	// construction. It is reported rather than implied because the runner is
	// the other system a reader has to look in.
	AttemptID string `json:"attemptID,omitempty"`
	// SessionID is the harness session the turn ran in. Consecutive turns of
	// one conversation share it, and the harness may fork it, so this is what
	// the harness actually reported rather than what was requested.
	SessionID string `json:"sessionID,omitempty"`
}

func summarize(run store.Run) runSummary {
	class := "background"
	if isInteractive(run.Trigger) {
		class = "interactive"
	}
	rs := runSummary{
		ID: run.ID, Agent: run.AgentName, SessionID: effectiveSessionID(run.SessionID, run.Trigger), Trigger: run.Trigger, Class: class,
		ParentRunID: run.ParentRunID, Phase: string(run.Phase), Attempt: run.Attempt,
		Retries: run.Retries, RetryAt: run.RetryAt,
		InputPreview: safeTruncate(strings.Join(strings.Fields(run.Input), " "), 160),
		Message:      safeTruncate(run.Message, 500),
		HasOutput:    run.Output != "",
		InputTokens:  run.InputTokens, OutputTokens: run.OutputTokens, USDMicros: run.USDMicros,
		CreatedAt: run.CreatedAt, StartedAt: run.StartedAt, FinishedAt: run.FinishedAt,
		Backend: run.Backend,
	}
	if run.WorkedDurationMS != nil && *run.WorkedDurationMS >= 0 {
		rs.WorkedDurationMS = run.WorkedDurationMS
	}
	if run.StartedAt != nil && run.FinishedAt != nil {
		rs.DurationMS = run.FinishedAt.Sub(*run.StartedAt).Milliseconds()
	}
	return rs
}

// runTrace serves the `trace` verb on a Run: GET …/runs/{id}/trace.
//
// It is the Postgres half of the projection — the step-level tool trace (each
// call's args, result, outcome and duration, secrets redacted), the answer,
// the pending-approval state and the child runs. Everything ON the object
// (phase, timings, usage, who started it) is read with a kube client and is
// deliberately not served here: a route that restated the object would be a
// second source of truth for the same facts.
func (s *Server) runTrace(w http.ResponseWriter, r *http.Request) {
	_, id, ok := s.requireClient(w, r)
	if !ok {
		return
	}
	agent, ok := gatedRunAgent(r)
	if !ok {
		writeStatus(w, http.StatusConflict, "Conflict", "this run names no agent, so its trace cannot be located")
		return
	}
	if view := r.URL.Query().Get("view"); view != "" {
		if view != "answer" {
			writeStatus(w, http.StatusBadRequest, "BadRequest", "view must be answer when specified")
			return
		}
		run, err := s.store.GetRun(r.Context(), id.scope(agent), r.PathValue("name"))
		if err != nil {
			writeStatus(w, http.StatusNotFound, "NotFound", err.Error())
			return
		}
		// This projection deliberately reads only the answer column. It avoids
		// loading a large trace or child list when an orchestrator needs one
		// bounded result from a delegated Run.
		writeJSON(w, http.StatusOK, struct {
			Output string `json:"output"`
		}{Output: safeTruncate(run.Output, 16<<10)})
		return
	}
	detail, err := s.runDetailFor(r.Context(), id.scope(agent), r.PathValue("name"))
	if err != nil {
		writeStatus(w, http.StatusNotFound, "NotFound", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

// runDetailFor assembles one run's full record: summary, answer,
// pending-approval state, step trace, and child runs. Shared by the `trace`
// verb, the long-poll `wait`, and a `run` that waited — so every way of asking
// about a run returns the same shape.
func (s *Server) runDetailFor(ctx context.Context, scope store.Scope, runID string) (runDetail, error) {
	run, err := s.store.GetRun(ctx, scope, runID)
	if err != nil {
		return runDetail{}, err
	}
	detail := runDetail{
		runSummary: summarize(run), Input: run.Input,
		Output: run.Output, Sources: run.Sources,
		Steps: []runStep{}, Children: []runSummary{},
	}
	// Only for a harness-backed run: on an in-process one these coordinates
	// name nothing, and an empty block would invite a reader to look for a
	// machine that was never involved.
	if run.Backend == agentsv1alpha1.AgentBackendHarness && (run.AttemptID != "" || run.HarnessSessionID != "") {
		detail.Harness = &runHarnessInfo{AttemptID: run.AttemptID, SessionID: run.HarnessSessionID}
	}
	if run.Phase == store.RunPhaseRunning {
		progress, err := s.store.GetRunProgress(ctx, scope, runID)
		if err != nil {
			log.Printf("agents: reading live progress for run %s: %v", runID, err)
		} else {
			detail.Progress = progress
		}
	}
	detail.Repository, detail.Result = run.Repository, run.Result
	detail.HarnessSettings = run.HarnessSettings
	if run.Phase == store.RunPhasePendingApproval && len(run.Checkpoint) > 0 {
		var ck runCheckpoint
		if json.Unmarshal(run.Checkpoint, &ck) == nil {
			// Same two reasons as the live event: a gated call, or a question
			// the turn asked. A checkpoint with no tool is the second.
			if strings.TrimSpace(ck.Tool) == "" {
				detail.Pending = &pendingInfo{InboxID: ck.InboxID, Kind: string(store.InboxKindQuestion), Question: ck.Question, Questions: ck.Questions}
			} else {
				detail.Pending = &pendingInfo{
					InboxID: ck.InboxID, Kind: string(store.InboxKindApproval),
					Tool: ck.Tool, Args: redactArgs(ck.Args),
				}
			}
		}
	}
	if calls, err := s.store.ListToolCalls(ctx, scope, runID); err == nil {
		for _, tc := range calls {
			detail.Steps = append(detail.Steps, runStep{
				ID: tc.ID, Tool: tc.Tool, Args: tc.Args, Result: tc.Result,
				Outcome: tc.Outcome, Error: tc.Error, DurationMS: tc.DurationMS, At: tc.CreatedAt,
				ExitCode: tc.ExitCode, OutcomeUnknown: tc.OutcomeUnknown,
			})
		}
	}
	// Enough to hold a full fan-out (spawn caps at 20 workers per run) plus
	// delegations, so the tree view is not silently truncated.
	childScope := scope
	childScope.AgentName = "" // delegated children may belong to another Agent in this workspace
	if children, err := s.store.QueryRuns(ctx, childScope, store.RunQuery{ParentRunID: runID, Limit: 50}); err == nil {
		for _, child := range children.Items {
			detail.Children = append(detail.Children, summarize(child))
		}
	}
	return detail, nil
}

// runArtifactRequest is the `artifact` verb's input: which of a repository
// run's stored artifacts to read.
type runArtifactRequest struct {
	Name string `json:"name"`
}

// runArtifactResponse is one stored artifact, bytes included. Base64 because
// the verb answers JSON like every other, and the bundle is binary; a reader
// re-checks Digest against what it decodes.
type runArtifactResponse struct {
	Name      string `json:"name"`
	Digest    string `json:"digest"`
	Size      int64  `json:"size"`
	MediaType string `json:"mediaType"`
	Data      string `json:"data"`
}

// runArtifact serves the `artifact` verb on a Run: POST …/runs/{id}/artifact
// with {"name": "git-result.json" | "git-result.bundle" | "final-response.md" | "screenshots.json"}.
// The public report name is backend.FinalResponseArtifactName.
//
// It serves only what this provider stored for THAT run, after it verified the
// runner's export (backend/harness/repository.go): a coordinator reads the
// result from the provider that vouched for it, never from the runner. A name
// the run did not store — a conversational run has none at all — is not
// found, and the refusal does not say which of the two it was.
func (s *Server) runArtifact(w http.ResponseWriter, r *http.Request) {
	_, id, ok := s.requireClient(w, r)
	if !ok {
		return
	}
	agent, ok := gatedRunAgent(r)
	if !ok {
		writeStatus(w, http.StatusConflict, "Conflict", "this run names no agent, so its artifacts cannot be located")
		return
	}
	var req runArtifactRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeStatus(w, http.StatusBadRequest, "BadRequest", "invalid JSON body: "+err.Error())
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		writeStatus(w, http.StatusBadRequest, "BadRequest", "name is required")
		return
	}
	runID := r.PathValue("name")
	scope := id.scope(agent)
	if _, err := s.store.GetRun(r.Context(), scope, runID); err != nil {
		writeStatus(w, http.StatusNotFound, "NotFound", err.Error())
		return
	}
	artifact, found, err := s.store.GetRunArtifact(r.Context(), scope, runID, name)
	if err != nil {
		log.Printf("runs: reading artifact %q of run %s failed: %v", name, runID, err)
		writeStatus(w, http.StatusServiceUnavailable, "ServiceUnavailable", "the artifact could not be read; retry later")
		return
	}
	if !found {
		writeStatus(w, http.StatusNotFound, "NotFound", "run "+runID+" has no stored artifact "+name)
		return
	}
	writeJSON(w, http.StatusOK, runArtifactResponse{
		Name: artifact.Name, Digest: artifact.Digest, Size: artifact.Size, MediaType: artifact.MediaType,
		Data: base64.StdEncoding.EncodeToString(artifact.Data),
	})
}

// cancelRun serves the `cancel` verb on a Run: POST …/runs/{id}/cancel. The request is recorded on the
// run row first (store.RequestCancel) so it reaches the run wherever it is:
// executing on this replica (its context is cancelled right away), executing
// on another replica or still queued (the engine loop reads the flag between
// tool rounds; a queued job checks it before starting), or resumed later by
// the recovery sweep (which closes a flagged run instead of resuming it).
// A run not live here is stamped Aborted only after any parked harness attempt
// has acknowledged cancellation. If that remote stop fails, the durable cancel
// request remains and the caller can retry without falsely terminalizing a
// runner that may still be working.
func (s *Server) cancelRun(w http.ResponseWriter, r *http.Request) {
	c, id, ok := s.requireClient(w, r)
	if !ok {
		return
	}
	agent, ok := gatedRunAgent(r)
	if !ok {
		writeStatus(w, http.StatusConflict, "Conflict", "this run names no agent, so it cannot be located in the store")
		return
	}
	runID := r.PathValue("name")
	run, err := s.store.GetRun(r.Context(), id.scope(agent), runID)
	if err != nil {
		writeStatus(w, http.StatusNotFound, "NotFound", err.Error())
		return
	}
	if !runCancellable(run.Phase) {
		writeStatus(w, http.StatusConflict, "Conflict", "run is already "+string(run.Phase))
		return
	}
	live, err := s.cancelStoredRun(r.Context(), c, id, run)
	if err != nil {
		writeStatus(w, http.StatusServiceUnavailable, "ServiceUnavailable", err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"id": runID, "cancelling": live})
}

// runCancellable reports whether a run in this phase can still be cancelled.
func runCancellable(phase store.RunPhase) bool {
	switch phase {
	case store.RunPhaseRunning, store.RunPhasePending, store.RunPhasePendingApproval:
		return true
	default:
		return false
	}
}

// The two ways a cancel can fail. Both are safe to retry, and both are fixed
// text: errors can originate in an authenticated request, so an upstream
// response must not reach the caller and expose a credential or
// secret-bearing body.
var (
	errCancelNotRecorded = errors.New("could not record the cancellation request; retry the cancel")
	errHarnessStopFailed = errors.New("remote harness cancellation failed; retry the cancel")
)

// cancelStoredRun cancels one run: the cancel verb and the channel /cancel
// command both end here. It records the durable cancel request, interrupts the
// run if it is executing on this replica, and otherwise stops a parked harness
// attempt and stamps the run Aborted. live reports whether the run was
// executing here (and so finishes on its own once it observes the cancel).
func (s *Server) cancelStoredRun(ctx context.Context, c *agentsclient.Client, id identity, run store.Run) (live bool, err error) {
	scope := store.Scope{OrgUUID: id.orgUUID, WorkspaceUUID: id.workspaceUUID, AgentName: run.AgentName, ClusterID: id.clusterID}
	if err := s.store.RequestCancel(ctx, scope, run.ID, time.Now().UTC(), id.user); err != nil {
		log.Printf("runs: recording cancel for run %s failed", run.ID)
		return false, errCancelNotRecorded
	}
	if s.liveRuns != nil && s.liveRuns.cancel(run.ID) {
		return true, nil
	}
	if run.Backend == agentsv1alpha1.AgentBackendHarness && len(run.Checkpoint) > 0 {
		if c == nil {
			log.Printf("runs: no workspace client available to stop parked harness attempt for run %s", run.ID)
			return false, errHarnessStopFailed
		}
		if err := s.stopParkedHarness(ctx, c, id, run); err != nil {
			log.Printf("runs: stopping parked harness attempt for run %s failed", run.ID)
			return false, errHarnessStopFailed
		}
	}
	message := "cancelled by user"
	if strings.HasPrefix(id.user, "parent:") {
		message = "parent run terminated"
	}
	s.closeRunNow(ctx, scope, run, store.RunPhaseAborted, message)
	return false, nil
}

// closeRunNow stamps a terminal phase on a run that is not executing on this
// replica: it writes the closing transcript row, finishes the run record and
// publishes the phase change.
//
// It exists because two things need it and must agree: a cancel the user asked
// for, and a deadline the Run reconciler observed. Both are "this run is over
// and nobody local is going to notice", and a second implementation of that
// would be a second set of timing rules for the same event.
func (s *Server) closeRunNow(ctx context.Context, scope store.Scope, run store.Run, phase store.RunPhase, message string) {
	now := time.Now().UTC()
	startedAt := run.CreatedAt
	if run.StartedAt != nil {
		startedAt = *run.StartedAt
	}
	if startedAt.IsZero() {
		startedAt = now
	}
	persistCtx, cancelPersist := boundedPersistContext(ctx)
	defer cancelPersist()
	tracker := trackerForStored(run)
	if run.CancelRequested && phase == store.RunPhaseAborted {
		// RequestCancel prevents new leases and fences every non-abort owned
		// save. Closing from the cancel record itself handles a crashed owner.
		if !s.finishRun(persistCtx, scope, run.ID, runOutcome{Phase: phase, Message: message, WorkedDurationMS: tracker.workedDurationMS()}, now) {
			return
		}
	} else if run.ExecutionOwner != "" {
		if !s.finishRunOwned(persistCtx, scope, run.ID, runOutcome{Phase: phase, Message: message, WorkedDurationMS: tracker.workedDurationMS()}, now, run.ExecutionOwner, run.Attempt) {
			return
		}
	} else if !s.finishRun(persistCtx, scope, run.ID, runOutcome{Phase: phase, Message: message, WorkedDurationMS: tracker.workedDurationMS()}, now) {
		return
	}
	s.appendTurnTerminal(persistCtx, scope, taskRunForStored(run), effectiveSessionID(run.SessionID, run.Trigger), startedAt, now, tracker, turnStatusForRunPhase(phase), "", message)
	s.publishRunEvent(scope, runEvent{ID: run.ID, Agent: run.AgentName, Trigger: run.Trigger, ParentRunID: run.ParentRunID, Phase: phase})
}
