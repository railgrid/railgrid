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

	"github.com/faroshq/provider-agents/store"
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
	if run.StartedAt != nil && run.FinishedAt != nil {
		rs.DurationMS = run.FinishedAt.Sub(*run.StartedAt).Milliseconds()
	}
	return rs
}

// listRuns serves GET /api/runs — the Activity feed. Filters: agent, phase,
// trigger, class (interactive|background), session, parent; cursor+limit page
// newest-first.
func (s *Server) listRuns(w http.ResponseWriter, r *http.Request) {
	_, id, ok := s.requireClient(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	scope := id.scope(strings.TrimSpace(q.Get("agent")))
	limit := 50
	if v, err := strconv.Atoi(q.Get("limit")); err == nil && v > 0 {
		limit = min(v, 200)
	}
	// Optional date range (RFC3339). Applied after the store query so the
	// cursor stays a pure (createdAt, id) pair.
	var since, until time.Time
	if v := strings.TrimSpace(q.Get("since")); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			writeStatus(w, http.StatusBadRequest, "BadRequest", "since must be RFC3339: "+err.Error())
			return
		}
		since = t
	}
	if v := strings.TrimSpace(q.Get("until")); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			writeStatus(w, http.StatusBadRequest, "BadRequest", "until must be RFC3339: "+err.Error())
			return
		}
		until = t
	}
	page, err := s.store.QueryRuns(r.Context(), scope, store.RunQuery{
		Phase:       store.RunPhase(strings.TrimSpace(q.Get("phase"))),
		Trigger:     strings.TrimSpace(q.Get("trigger")),
		SessionID:   strings.TrimSpace(q.Get("session")),
		ParentRunID: strings.TrimSpace(q.Get("parent")),
		Limit:       limit,
		Cursor:      strings.TrimSpace(q.Get("cursor")),
	})
	if err != nil {
		writeStatus(w, http.StatusInternalServerError, "InternalError", err.Error())
		return
	}
	class := strings.TrimSpace(q.Get("class"))
	items := make([]runSummary, 0, len(page.Items))
	for _, run := range page.Items {
		if !since.IsZero() && run.CreatedAt.Before(since) {
			continue
		}
		if !until.IsZero() && run.CreatedAt.After(until) {
			continue
		}
		rs := summarize(run)
		if class != "" && rs.Class != class {
			continue
		}
		items = append(items, rs)
	}
	writeList(w, items, map[string]any{"nextCursor": page.NextCursor})
}

// getRun serves GET /api/runs/{id}: the step-level trace (tool calls in
// execution order), pending-approval state, and delegated child runs.
func (s *Server) getRun(w http.ResponseWriter, r *http.Request) {
	_, id, ok := s.requireClient(w, r)
	if !ok {
		return
	}
	detail, err := s.runDetailFor(r.Context(), id.scope(""), r.PathValue("id"))
	if err != nil {
		writeStatus(w, http.StatusNotFound, "NotFound", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

// runDetailFor assembles one run's full record: summary, answer, pending-approval
// state, step trace, and child runs. Shared by GET /api/runs/{id}, the long-poll
// wait, and an invoke that waited — so every way of asking about a run returns
// the same shape.
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

// cancelRun serves POST /api/runs/{id}/cancel: aborts a live run's context, or
// force-stamps Aborted on a stale Running/PendingApproval record (e.g. the
// provider restarted mid-run).
func (s *Server) cancelRun(w http.ResponseWriter, r *http.Request) {
	_, id, ok := s.requireClient(w, r)
	if !ok {
		return
	}
	runID := r.PathValue("id")
	run, err := s.store.GetRun(r.Context(), id.scope(""), runID)
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
	live := s.liveRuns.cancel(runID)
	if !live {
		// Not executing on this replica: stamp the terminal phase directly.
		now := time.Now().UTC()
		scope := store.Scope{OrgUUID: id.orgUUID, WorkspaceUUID: id.workspaceUUID, AgentName: run.AgentName}
		s.finishRun(r.Context(), scope, runID, runOutcome{Phase: store.RunPhaseAborted, Message: "cancelled by user"}, now)
		s.publishRunEvent(scope, runEvent{ID: runID, Agent: run.AgentName, Trigger: run.Trigger, ParentRunID: run.ParentRunID, Phase: store.RunPhaseAborted})
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"id": runID, "cancelling": live})
}
