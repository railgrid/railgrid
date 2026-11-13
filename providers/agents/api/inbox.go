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
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	agentsv1alpha1 "github.com/railgrid/provider-agents/apis/v1alpha1"
	agentsclient "github.com/railgrid/provider-agents/client"
	"github.com/railgrid/provider-agents/store"

	runnerharness "github.com/railgrid/railgrid/pkg/runner/harness"
)

// listInboxItems returns one agent's approvals + questions queue — the `inbox`
// verb on that agent. Filter with ?state=pending (default: all).
//
// It is per-agent because the grammar addresses an object, and an inbox item's
// object is the agent that raised it: a caller who may approve one agent's tool
// call has not thereby been granted the others'. A workspace-wide view is the
// portal's job, over the agents it can see.
func (s *Server) listInboxItems(w http.ResponseWriter, r *http.Request) {
	c, id, ok := s.requireClient(w, r)
	if !ok {
		return
	}
	agent := r.PathValue("name")
	state := store.InboxItemState(strings.TrimSpace(r.URL.Query().Get("state")))
	items, err := s.store.ListInbox(r.Context(), store.Scope{OrgUUID: id.orgUUID, WorkspaceUUID: id.workspaceUUID}, state)
	if err != nil {
		writeStatus(w, http.StatusInternalServerError, "InternalError", err.Error())
		return
	}
	if state == "" || state == store.InboxStatePending {
		items, err = s.repairMissingParkedInboxItems(r.Context(), store.Scope{OrgUUID: id.orgUUID, WorkspaceUUID: id.workspaceUUID, AgentName: r.PathValue("name")}, state, items, c)
		if err != nil {
			writeStatus(w, http.StatusServiceUnavailable, "InboxRepairUnavailable", "a parked run's inbox item could not be restored; retry the inbox request")
			return
		}
	}
	mine := make([]store.InboxItem, 0, len(items))
	for _, item := range items {
		if item.AgentName == agent {
			mine = append(mine, item)
		}
	}
	writeList(w, mine)
}

// repairMissingParkedInboxItems closes the tiny crash window between saving a
// PendingApproval checkpoint and writing its inbox row. The checkpoint is the
// durable source for reconstructing the item, so a subsequent inbox read can
// restore it without resuming or otherwise changing the parked run.
func (s *Server) repairMissingParkedInboxItems(ctx context.Context, scope store.Scope, state store.InboxItemState, items []store.InboxItem, callers ...*agentsclient.Client) ([]store.InboxItem, error) {
	var caller *agentsclient.Client
	if len(callers) > 0 {
		caller = callers[0]
	}
	var agent *agentsv1alpha1.Agent
	var agentErr error
	if caller != nil {
		agent, agentErr = (clientCR{caller}).GetAgent(ctx, scope.AgentName)
	}
	workspaceScope := store.Scope{OrgUUID: scope.OrgUUID, WorkspaceUUID: scope.WorkspaceUUID}
	known := make(map[string]struct{}, len(items))
	for _, item := range items {
		known[item.ID] = struct{}{}
	}
	var cursor string
	for {
		page, err := s.store.QueryRuns(ctx, scope, store.RunQuery{Phase: store.RunPhasePendingApproval, Limit: 200, Cursor: cursor})
		if err != nil {
			return nil, err
		}
		for _, run := range page.Items {
			var checkpoint runCheckpoint
			if json.Unmarshal(run.Checkpoint, &checkpoint) != nil || checkpoint.InboxID == "" {
				continue
			}
			if _, ok := known[checkpoint.InboxID]; ok {
				continue
			}
			// Re-read before repairing: a concurrent answer, cancel, or resume
			// may have moved the run since QueryRuns returned this snapshot.
			live, err := s.store.GetRun(ctx, scope, run.ID)
			if err != nil {
				return nil, err
			}
			var liveCheckpoint runCheckpoint
			if live.Phase != store.RunPhasePendingApproval || json.Unmarshal(live.Checkpoint, &liveCheckpoint) != nil || liveCheckpoint.InboxID != checkpoint.InboxID {
				continue
			}
			item, ok := parkedInboxItem(live, liveCheckpoint)
			if !ok {
				continue
			}
			created := true
			if err := s.store.AddInboxItemForParkedRun(ctx, scope, live.ID, item); err != nil {
				if errors.Is(err, store.ErrRunNotParked) {
					continue
				}
				created = false
				// Another list request may have repaired the same checkpoint.
				saved, readErr := s.store.GetInboxItem(ctx, workspaceScope, item.ID)
				if readErr != nil || saved.AgentName != item.AgentName || saved.RunID != item.RunID || saved.Kind != item.Kind {
					return nil, err
				}
				item = saved
			} else {
				saved, readErr := s.store.GetInboxItem(ctx, workspaceScope, item.ID)
				if readErr != nil {
					return nil, readErr
				}
				item = saved
			}
			known[item.ID] = struct{}{}
			if state == "" || item.State == state {
				items = append(items, item)
			}
			if created {
				s.events.publish(workspaceScope, "inbox", map[string]any{
					"id": item.ID, "state": string(item.State), "agent": item.AgentName, "runID": item.RunID, "kind": string(item.Kind),
				})
				if agentErr == nil && agent != nil && caller != nil {
					notifyRun := taskRun{Scope: scope, RunID: live.ID, Trigger: live.Trigger, NotifyChannel: liveCheckpoint.NotifyChannel, Agent: agent, CR: clientCR{caller}, Creds: caller}
					if item.Kind == store.InboxKindApproval {
						s.notifyInboxApproval(ctx, notifyRun, checkpoint.Tool)
					} else {
						s.notifyInboxQuestion(ctx, notifyRun, item.Prompt)
					}
				}
			}
		}
		if page.NextCursor == "" {
			return items, nil
		}
		cursor = page.NextCursor
	}
}

func (s *Server) addInboxItemOnce(ctx context.Context, scope store.Scope, item store.InboxItem) (created bool, err error) {
	if err := s.store.AddInboxItem(ctx, scope, item); err == nil {
		return true, nil
	} else {
		saved, readErr := s.store.GetInboxItem(ctx, scope, item.ID)
		if readErr == nil && saved.AgentName == item.AgentName && saved.RunID == item.RunID && saved.Kind == item.Kind {
			return false, nil
		}
		return false, err
	}
}

func parkedInboxItem(run store.Run, checkpoint runCheckpoint) (store.InboxItem, bool) {
	item := store.InboxItem{ID: checkpoint.InboxID, AgentName: run.AgentName, RunID: run.ID, State: store.InboxStatePending,
		CreatedAt: run.UpdatedAt, UpdatedAt: run.UpdatedAt}
	if strings.TrimSpace(checkpoint.Tool) != "" {
		item.Kind = store.InboxKindApproval
		item.Prompt = "Allow " + run.AgentName + " to run " + checkpoint.Tool + "?"
		item.Payload = map[string]any{"tool": checkpoint.Tool, "args": redactArgs(approvableArgs(checkpoint.Args)), "trigger": run.Trigger, "harness": checkpoint.Backend == agentsv1alpha1.AgentBackendHarness}
		return item, true
	}
	question := strings.TrimSpace(checkpoint.Question)
	if question == "" {
		question = "the agent is waiting for input"
	}
	item.Kind = store.InboxKindQuestion
	item.Prompt = safeTruncate(question, maxHarnessQuestion)
	item.Payload = map[string]any{"harness": checkpoint.Backend == agentsv1alpha1.AgentBackendHarness, "trigger": run.Trigger, "questions": checkpoint.Questions}
	return item, true
}

type resolveInboxRequest struct {
	// Decision: approve | deny | answer.
	Decision string             `json:"decision"`
	Response string             `json:"response,omitempty"`
	Answers  map[string]*string `json:"answers,omitempty"`
}

const approvalDisclosureUnavailableMessage = "Approval details are unavailable or malformed. Deny this request or inspect the run."

func approvalDisclosureAvailable(item store.InboxItem) bool {
	tool, ok := item.Payload["tool"].(string)
	if !ok || strings.TrimSpace(tool) == "" {
		return false
	}
	args, ok := item.Payload["args"].(string)
	if !ok || strings.TrimSpace(args) == "" {
		return false
	}
	var disclosed map[string]json.RawMessage
	return json.Unmarshal([]byte(args), &disclosed) == nil && disclosed != nil
}

// resolveInboxDecision keeps approval validation next to the persisted inbox
// disclosure and, critically, completes it before any inbox mutation. Denials
// remain available when the disclosure is unavailable so the user can stop the
// requested action safely.
func (s *Server) resolveInboxDecision(ctx context.Context, scope store.Scope, id string, state store.InboxItemState, response string, now time.Time, answerPayload ...map[string]any) (store.InboxItem, error) {
	item, err := s.store.GetInboxItem(ctx, scope, id)
	if err != nil {
		return store.InboxItem{}, err
	}
	if item.State != store.InboxStatePending && item.State == state && item.Response == response {
		// A client may retry after losing the response to the first decision.
		// Return the stored verdict without asking the already-resumed run to park again.
		return item, nil
	}
	if item.State == store.InboxStatePending && item.RunID != "" && resolutionResumesRun(item.Kind, state) {
		runScope := store.Scope{OrgUUID: scope.OrgUUID, WorkspaceUUID: scope.WorkspaceUUID, AgentName: item.AgentName}
		run, runErr := s.store.GetRun(ctx, runScope, item.RunID)
		if runErr != nil && !errors.Is(runErr, store.ErrRunNotFound) {
			return store.InboxItem{}, runErr
		}
		if state == store.InboxStateApproved && (errors.Is(runErr, store.ErrRunNotFound) || (runErr == nil && (run.Phase == store.RunPhaseSucceeded || run.Phase == store.RunPhaseFailed || run.Phase == store.RunPhaseAborted))) {
			return store.InboxItem{}, &requestError{code: http.StatusConflict, reason: "RunEnded", msg: "the run has ended; this approval can no longer be applied"}
		}
		if runErr == nil && run.Phase != store.RunPhaseSucceeded && run.Phase != store.RunPhaseFailed && run.Phase != store.RunPhaseAborted {
			var checkpoint runCheckpoint
			if run.Phase != store.RunPhasePendingApproval || run.CancelRequested || json.Unmarshal(run.Checkpoint, &checkpoint) != nil || checkpoint.InboxID != item.ID {
				// A live run may still be writing the checkpoint that makes this item resumable.
				if latest, err := s.store.GetInboxItem(ctx, scope, id); err == nil && latest.State != store.InboxStatePending {
					return store.InboxItem{}, store.ErrInboxAlreadyResolved
				}
				return store.InboxItem{}, &requestError{code: http.StatusConflict, reason: "RunNotParked", msg: "the run is not waiting on this inbox item yet; retry after the run parks"}
			}
		}
	}
	if item.Kind == store.InboxKindQuestion {
		if state != store.InboxStateAnswered && state != store.InboxStateDenied {
			return store.InboxItem{}, &requestError{code: http.StatusBadRequest, reason: "InvalidQuestionDecision", msg: "questions can only be answered or dismissed"}
		}
		payload := map[string]any{}
		if len(answerPayload) > 0 {
			payload = answerPayload[0]
		}
		return s.resolveInboxDecisionOnce(ctx, scope, id, state, response, now, store.InboxResolution{QuestionPayload: payload})
	}
	if state == store.InboxStateAnswered {
		return store.InboxItem{}, &requestError{code: http.StatusBadRequest, reason: "InvalidInboxDecision", msg: "only questions accept answers"}
	}
	if state != store.InboxStateApproved {
		return s.resolveInboxDecisionOnce(ctx, scope, id, state, response, now, store.InboxResolution{PendingOnly: item.Kind == store.InboxKindApproval})
	}
	if item.Kind == store.InboxKindApproval && !approvalDisclosureAvailable(item) {
		return store.InboxItem{}, &requestError{
			code:   http.StatusConflict,
			reason: "ApprovalDisclosureUnavailable",
			msg:    approvalDisclosureUnavailableMessage,
		}
	}
	// A change request is applied by approving it, before the item is marked
	// approved: a change that can no longer be made stays pending with the
	// reason, rather than reading as done.
	if item.Kind == store.InboxKindChange {
		if item.State != store.InboxStatePending {
			return store.InboxItem{}, &requestError{code: http.StatusConflict, reason: "AlreadyResolved", msg: "this change request was already resolved"}
		}
		result, err := s.applyChangeRequest(ctx, scope, item)
		if err != nil {
			return store.InboxItem{}, &requestError{code: http.StatusConflict, reason: "ChangeNotApplied", msg: "the change was not applied: " + err.Error()}
		}
		response = strings.TrimSpace(strings.TrimSpace(response) + "\n" + result)
	}
	return s.resolveInboxDecisionOnce(ctx, scope, id, state, response, now, store.InboxResolution{PendingOnly: item.Kind == store.InboxKindApproval})
}

// resolveInboxDecisionOnce atomically preserves the first approval verdict.
// If a request saved a decision but failed before dispatch, its exact retry can
// resume the same parked checkpoint. The saved note is authoritative, and
// ClaimRun still fences concurrent dispatches. Opposite or stale replies fail.
func (s *Server) resolveInboxDecisionOnce(ctx context.Context, scope store.Scope, id string, state store.InboxItemState, response string, now time.Time, resolution store.InboxResolution) (store.InboxItem, error) {
	resolved, err := s.store.ResolveInboxItem(ctx, scope, id, state, response, now, resolution)
	if !errors.Is(err, store.ErrInboxAlreadyResolved) {
		return resolved, err
	}
	saved, readErr := s.store.GetInboxItem(ctx, scope, id)
	if readErr != nil || saved.State != state || saved.RunID == "" || !resolutionResumesRun(saved.Kind, state) {
		return resolved, err
	}
	agentScope := store.Scope{OrgUUID: scope.OrgUUID, WorkspaceUUID: scope.WorkspaceUUID, AgentName: saved.AgentName}
	run, runErr := s.store.GetRun(ctx, agentScope, saved.RunID)
	if runErr != nil || run.Phase != store.RunPhasePendingApproval || run.CancelRequested {
		return resolved, err
	}
	var checkpoint runCheckpoint
	if json.Unmarshal(run.Checkpoint, &checkpoint) == nil && checkpoint.InboxID == saved.ID {
		return saved, nil
	}
	return resolved, err
}

// inboxAnswerAllowed reports whether the answer-only endpoint may resolve an
// item with this kind and state.
func inboxAnswerAllowed(kind store.InboxItemKind, state store.InboxItemState) bool {
	return kind == store.InboxKindQuestion && state == store.InboxStateAnswered
}

// resolutionResumesRun reports whether resolving an item this way continues the
// run it is bound to: a verdict on an approval, or an answer to a question.
func resolutionResumesRun(kind store.InboxItemKind, state store.InboxItemState) bool {
	switch kind {
	case store.InboxKindApproval:
		return state == store.InboxStateApproved || state == store.InboxStateDenied
	case store.InboxKindQuestion:
		return state == store.InboxStateAnswered
	default:
		return false
	}
}

// resolveInboxItem records the user's decision on an approval or question. It
// is the `inbox-resolve` verb on the agent, with the item id in the tail:
// POST …/agents/{name}/inbox-resolve/{itemID}.
//
// Resolving an approval bound to a paused run resumes it in place: approve
// executes the gated call with the exact requested arguments, deny feeds the
// refusal back to the model.
func (s *Server) resolveInboxItem(w http.ResponseWriter, r *http.Request) {
	s.resolveInboxItemWithPolicy(w, r, false)
}

// answerInboxItem records a response to a question, without exposing the
// approval and denial powers of inbox-resolve to provider callers.
func (s *Server) answerInboxItem(w http.ResponseWriter, r *http.Request) {
	s.resolveInboxItemWithPolicy(w, r, true)
}

func (s *Server) resolveInboxItemWithPolicy(w http.ResponseWriter, r *http.Request, answerOnly bool) {
	c, id, ok := s.requireClient(w, r)
	if !ok {
		return
	}
	var req resolveInboxRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeStatus(w, http.StatusBadRequest, "BadRequest", "invalid JSON body: "+err.Error())
		return
	}
	var state store.InboxItemState
	if answerOnly && strings.TrimSpace(req.Decision) != "answer" {
		writeStatus(w, http.StatusBadRequest, "BadRequest", "decision must be answer")
		return
	}
	switch strings.TrimSpace(req.Decision) {
	case "approve":
		state = store.InboxStateApproved
	case "deny":
		state = store.InboxStateDenied
	case "answer":
		state = store.InboxStateAnswered
	default:
		writeStatus(w, http.StatusBadRequest, "BadRequest", "decision must be approve, deny, or answer")
		return
	}
	wsScope := store.Scope{OrgUUID: id.orgUUID, WorkspaceUUID: id.workspaceUUID}
	itemID := r.PathValue("tail")
	// The item id is a tail segment, not an object the gates saw, so the agent
	// it belongs to is checked against the one that WAS gated — before anything
	// is mutated.
	existing, gerr := s.store.GetInboxItem(r.Context(), wsScope, itemID)
	if gerr != nil || existing.AgentName != r.PathValue("name") {
		writeStatus(w, http.StatusNotFound, "NotFound", "no such inbox item on this agent")
		return
	}
	if answerOnly && !inboxAnswerAllowed(existing.Kind, state) {
		writeStatus(w, http.StatusBadRequest, "InvalidInboxItem", "inbox-answer only accepts question items")
		return
	}
	payload, err := inboxAnswerPayload(existing, state, &req)
	if err != nil {
		writeUpdateError(w, err)
		return
	}
	access := runAccess{Creds: c, CR: clientCR{c}, ClusterID: id.clusterID, HubToken: id.token, CallerUser: id.user}
	access, err = s.prepareInboxAttachmentAccess(r.Context(), wsScope, existing, state, access)
	if err != nil {
		writeStatus(w, http.StatusServiceUnavailable, "CallerIdentityUnavailable", "the current caller identity could not be verified for conversation attachments; retry this decision")
		return
	}
	var emit func(string, map[string]any)
	ctx := r.Context()
	if existing.RunID != "" && existing.Kind == store.InboxKindQuestion && state == store.InboxStateAnswered &&
		r.Header.Get("Accept") == "text/event-stream" {
		ctx, emit = chatSSE(w, r)
		if emit == nil {
			return
		}
	}
	item, err := s.resolveInboxDecision(r.Context(), wsScope, itemID, state, req.Response, time.Now().UTC(), payload...)
	if err != nil {
		if _, ok := errors.AsType[*requestError](err); ok {
			writeUpdateError(w, err)
		} else if errors.Is(err, store.ErrInboxAlreadyResolved) {
			writeStatus(w, http.StatusConflict, "Conflict", err.Error())
		} else if strings.Contains(err.Error(), "not found") {
			writeStatus(w, http.StatusNotFound, "NotFound", err.Error())
		} else {
			writeStatus(w, http.StatusInternalServerError, "InternalError", err.Error())
		}
		return
	}
	s.events.publish(wsScope, "inbox", map[string]any{
		"id": item.ID, "state": string(item.State), "agent": item.AgentName, "runID": item.RunID,
	})
	// Resume the paused run as this provider (the gate's client). No edges:
	// a verb carries no caller credential, and the edges family dials the
	// hub's aggregate MCP endpoint as the calling user.
	// An APPROVAL resumes on a verdict, and a QUESTION bound to a run resumes on
	// an answer. A free-standing question carries no run ID and resumes nothing.
	if item.RunID != "" && resolutionResumesRun(item.Kind, state) {
		if emit != nil {
			started := false
			res, err := s.continueInboxRun(ctx, wsScope, item, access, true, item.Response, func(event string, data map[string]any) {
				if event == "start" {
					started = true
				}
				emit(event, data)
			})
			if !started {
				if errors.Is(err, errResumeConflict) {
					writeStatus(w, http.StatusConflict, "Conflict", err.Error())
				} else {
					writeStatus(w, http.StatusInternalServerError, "InternalError", "could not resume this run")
				}
				return
			}
			frame := s.chatTerminalFrame(item.RunID, res, err)
			emit(frame.Event, frame.Data)
			return
		}
		go s.resumeApprovedRun(r.Context(), wsScope, item, access, state != store.InboxStateDenied, item.Response)
	}
	writeJSON(w, http.StatusOK, item)
}

func inboxAnswerPayload(item store.InboxItem, state store.InboxItemState, req *resolveInboxRequest) ([]map[string]any, error) {
	if req.Answers == nil {
		if state == store.InboxStateAnswered && strings.TrimSpace(req.Response) == "" {
			return nil, errBadRequest("answer text is required")
		}
		return nil, nil
	}
	response, err := formatQuestionAnswers(item, req.Answers)
	if err != nil || state != store.InboxStateAnswered {
		return nil, errBadRequest("provide one answer or skip for each question")
	}
	req.Response = response
	return []map[string]any{{"answers": req.Answers}}, nil
}

func formatQuestionAnswers(item store.InboxItem, answers map[string]*string) (string, error) {
	raw, err := json.Marshal(item.Payload["questions"])
	if err != nil {
		return "", err
	}
	var questions []runnerharness.Question
	if err := json.Unmarshal(raw, &questions); err != nil {
		return "", err
	}
	if len(questions) == 0 || len(questions) != len(answers) {
		return "", fmt.Errorf("answer count does not match question count")
	}
	var reply strings.Builder
	for _, question := range questions {
		answer, ok := answers[question.ID]
		if !ok {
			return "", fmt.Errorf("missing answer for question %q", question.ID)
		}
		value := "(skipped by the user)"
		if answer != nil {
			*answer = strings.TrimSpace(*answer)
			if *answer == "" || len(*answer) > 8192 {
				return "", fmt.Errorf("invalid answer for question %q", question.ID)
			}
			value = *answer
		}
		fmt.Fprintf(&reply, "%s\n%s\n\n", question.Prompt, value)
	}
	return strings.TrimSpace(reply.String()), nil
}

// prepareInboxAttachmentAccess performs fallible caller authentication before
// resolving a decision. Its canonical result is carried into the resume so a
// hub outage cannot consume the inbox item or claim the paused run.
func (s *Server) prepareInboxAttachmentAccess(ctx context.Context, scope store.Scope, item store.InboxItem, state store.InboxItemState, access runAccess) (runAccess, error) {
	if item.State != store.InboxStatePending || state == store.InboxStateDenied || !resolutionResumesRun(item.Kind, state) || item.RunID == "" || access.HubToken == "" || access.CallerUser == "" {
		return access, nil
	}
	scope.AgentName = item.AgentName
	run, err := s.store.GetRun(ctx, scope, item.RunID)
	if err != nil {
		return access, err
	}
	var checkpoint runCheckpoint
	if err := json.Unmarshal(run.Checkpoint, &checkpoint); err != nil {
		return access, err
	}
	access.AttachmentCaller, err = s.resumeAttachmentCaller(ctx, checkpoint, access)
	return access, err
}
