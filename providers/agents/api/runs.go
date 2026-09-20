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
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/railgrid/provider-agents/store"
)

// runSummary is the list-view shape of a run. Class derives from the trigger
// (interactive vs background) so the portal can filter without knowing the
// trigger taxonomy.
type runSummary struct {
	ID           string `json:"id"`
	Agent        string `json:"agent"`
	SessionID    string `json:"sessionID,omitempty"`
	Trigger      string `json:"trigger"`
	Class        string `json:"class"`
	ParentRunID  string `json:"parentRunID,omitempty"`
	Phase        string `json:"phase"`
	Attempt      int    `json:"attempt,omitempty"`
	InputPreview string `json:"inputPreview,omitempty"`
	Message      string `json:"message,omitempty"`
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
}

// runStep is one tool call in a run's trace.
type runStep struct {
	ID         string    `json:"id"`
	Tool       string    `json:"tool"`
	Args       string    `json:"args,omitempty"`
	Result     string    `json:"result,omitempty"`
	Outcome    string    `json:"outcome"`
	Error      string    `json:"error,omitempty"`
	DurationMS int64     `json:"durationMS,omitempty"`
	At         time.Time `json:"at"`
}

// runDetail is the full trace view of one run. Steps and Children are always
// present as arrays (never null/absent) so a client can map over them without
// guarding every empty case.
type runDetail struct {
	runSummary
	Input string `json:"input,omitempty"`
	// Output is the run's answer, so a caller that polled the phase reads the
	// result from the same object rather than the session transcript.
	Output   string       `json:"output,omitempty"`
	Sources  []string     `json:"sources,omitempty"`
	Pending  *pendingInfo `json:"pending,omitempty"`
	Steps    []runStep    `json:"steps"`
	Children []runSummary `json:"children"`
}

func summarize(run store.Run) runSummary {
	class := "background"
	if isInteractive(run.Trigger) {
		class = "interactive"
	}
	rs := runSummary{
		ID: run.ID, Agent: run.AgentName, SessionID: run.SessionID, Trigger: run.Trigger, Class: class,
		ParentRunID: run.ParentRunID, Phase: string(run.Phase), Attempt: run.Attempt,
		InputPreview: safeTruncate(strings.Join(strings.Fields(run.Input), " "), 160),
		Message:      safeTruncate(run.Message, 500),
		HasOutput:    run.Output != "",
		InputTokens:  run.InputTokens, OutputTokens: run.OutputTokens, USDMicros: run.USDMicros,
		CreatedAt: run.CreatedAt, StartedAt: run.StartedAt, FinishedAt: run.FinishedAt,
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
	if run.Phase == store.RunPhasePendingApproval && len(run.Checkpoint) > 0 {
		var ck runCheckpoint
		if json.Unmarshal(run.Checkpoint, &ck) == nil {
			detail.Pending = &pendingInfo{InboxID: ck.InboxID, Tool: ck.Tool, Args: redactArgs(ck.Args)}
		}
	}
	if calls, err := s.store.ListToolCalls(ctx, scope, runID); err == nil {
		for _, tc := range calls {
			detail.Steps = append(detail.Steps, runStep{
				ID: tc.ID, Tool: tc.Tool, Args: tc.Args, Result: tc.Result,
				Outcome: tc.Outcome, Error: tc.Error, DurationMS: tc.DurationMS, At: tc.CreatedAt,
			})
		}
	}
	// Enough to hold a full fan-out (spawn caps at 20 workers per run) plus
	// delegations, so the tree view is not silently truncated.
	if children, err := s.store.QueryRuns(ctx, scope, store.RunQuery{ParentRunID: runID, Limit: 50}); err == nil {
		for _, child := range children.Items {
			detail.Children = append(detail.Children, summarize(child))
		}
	}
	return detail, nil
}

// cancelRun serves the `cancel` verb on a Run: POST …/runs/{id}/cancel. The request is recorded on the
// run row first (store.RequestCancel) so it reaches the run wherever it is:
// executing on this replica (its context is cancelled right away), executing
// on another replica or still queued (the engine loop reads the flag between
// tool rounds; a queued job checks it before starting), or resumed later by
// the recovery sweep (which closes a flagged run instead of resuming it).
// A run not live here is also stamped Aborted immediately, as before, so the
// caller sees it end without waiting for the executor to notice.
func (s *Server) cancelRun(w http.ResponseWriter, r *http.Request) {
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
	run, err := s.store.GetRun(r.Context(), id.scope(agent), runID)
	if err != nil {
		writeStatus(w, http.StatusNotFound, "NotFound", err.Error())
		return
	}
	switch run.Phase {
	case store.RunPhaseRunning, store.RunPhasePending, store.RunPhasePendingApproval:
	default:
		writeStatus(w, http.StatusConflict, "Conflict", "run is already "+string(run.Phase))
		return
	}
	now := time.Now().UTC()
	scope := store.Scope{OrgUUID: id.orgUUID, WorkspaceUUID: id.workspaceUUID, AgentName: run.AgentName}
	if err := s.store.RequestCancel(r.Context(), scope, runID, now); err != nil {
		log.Printf("runs: recording cancel for run %s: %v", runID, err)
	}
	live := s.liveRuns.cancel(runID)
	if !live {
		s.closeRunNow(r.Context(), scope, run, store.RunPhaseAborted, "cancelled by user")
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"id": runID, "cancelling": live})
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
	s.appendTurnTerminal(persistCtx, scope, taskRunForStored(run), run.SessionID, startedAt, now, tracker, turnStatusForRunPhase(phase), "", message)
	s.finishRun(persistCtx, scope, run.ID, runOutcome{Phase: phase, Message: message, WorkedDurationMS: tracker.workedDurationMS()}, now)
	s.publishRunEvent(scope, runEvent{ID: run.ID, Agent: run.AgentName, Trigger: run.Trigger, ParentRunID: run.ParentRunID, Phase: phase})
}
