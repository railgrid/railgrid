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
	_, id, ok := s.requireClient(w, r)
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
	mine := make([]store.InboxItem, 0, len(items))
	for _, item := range items {
		if item.AgentName == agent {
			mine = append(mine, item)
		}
	}
	writeList(w, mine)
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
	if item.Kind == store.InboxKindQuestion {
		if state != store.InboxStateAnswered && state != store.InboxStateDenied {
			return store.InboxItem{}, &requestError{code: http.StatusBadRequest, reason: "InvalidQuestionDecision", msg: "questions can only be answered or dismissed"}
		}
		payload := map[string]any{}
		if len(answerPayload) > 0 {
			payload = answerPayload[0]
		}
		resolved, err := s.store.ResolveInboxItem(ctx, scope, id, state, response, now, store.InboxResolution{QuestionPayload: payload})
		if state == store.InboxStateAnswered && errors.Is(err, store.ErrInboxAlreadyResolved) {
			// The first request may have saved its reply without dispatching a
			// resume. Use the saved reply if this exact question still parks
			// the run. ClaimRun fences concurrent and delayed dispatches.
			if saved, readErr := s.store.GetInboxItem(ctx, scope, id); readErr == nil && saved.State == store.InboxStateAnswered && saved.RunID != "" {
				agentScope := store.Scope{OrgUUID: scope.OrgUUID, WorkspaceUUID: scope.WorkspaceUUID, AgentName: saved.AgentName}
				run, runErr := s.store.GetRun(ctx, agentScope, saved.RunID)
				if runErr == nil && run.Phase == store.RunPhasePendingApproval && !run.CancelRequested {
					var checkpoint runCheckpoint
					if json.Unmarshal(run.Checkpoint, &checkpoint) == nil && checkpoint.InboxID == saved.ID {
						return saved, nil
					}
				}
			}
		}
		return resolved, err
	}
	if state == store.InboxStateAnswered {
		return store.InboxItem{}, &requestError{code: http.StatusBadRequest, reason: "InvalidInboxDecision", msg: "only questions accept answers"}
	}
	if state != store.InboxStateApproved {
		return s.store.ResolveInboxItem(ctx, scope, id, state, response, now, store.InboxResolution{})
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
	return s.store.ResolveInboxItem(ctx, scope, id, state, response, now, store.InboxResolution{})
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
	var payload []map[string]any
	if state == store.InboxStateAnswered && req.Answers == nil && strings.TrimSpace(req.Response) == "" {
		writeStatus(w, http.StatusBadRequest, "BadRequest", "answer text is required")
		return
	}
	if req.Answers != nil {
		response, err := formatQuestionAnswers(existing, req.Answers)
		if err != nil || state != store.InboxStateAnswered {
			writeStatus(w, http.StatusBadRequest, "BadRequest", "provide one answer or skip for each question")
			return
		}
		req.Response = response
		payload = append(payload, map[string]any{"answers": req.Answers})
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
		access := runAccess{Creds: c, CR: clientCR{c}, ClusterID: id.clusterID, CallerUser: id.user}
		go s.resumeApprovedRun(r.Context(), wsScope, item, access, state != store.InboxStateDenied, item.Response)
	}
	writeJSON(w, http.StatusOK, item)
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
