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
	"net/http"
	"strings"
	"time"

	"github.com/railgrid/provider-agents/store"
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
	Decision string `json:"decision"`
	Response string `json:"response,omitempty"`
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
func (s *Server) resolveInboxDecision(ctx context.Context, scope store.Scope, id string, state store.InboxItemState, response string, now time.Time) (store.InboxItem, error) {
	if state != store.InboxStateApproved {
		return s.store.ResolveInboxItem(ctx, scope, id, state, response, now)
	}
	item, err := s.store.GetInboxItem(ctx, scope, id)
	if err != nil {
		return store.InboxItem{}, err
	}
	if item.Kind == store.InboxKindApproval && !approvalDisclosureAvailable(item) {
		return store.InboxItem{}, &requestError{
			code:   http.StatusConflict,
			reason: "ApprovalDisclosureUnavailable",
			msg:    approvalDisclosureUnavailableMessage,
		}
	}
	return s.store.ResolveInboxItem(ctx, scope, id, state, response, now)
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
	if existing, gerr := s.store.GetInboxItem(r.Context(), wsScope, itemID); gerr != nil || existing.AgentName != r.PathValue("name") {
		writeStatus(w, http.StatusNotFound, "NotFound", "no such inbox item on this agent")
		return
	}
	item, err := s.resolveInboxDecision(r.Context(), wsScope, itemID, state, req.Response, time.Now().UTC())
	if err != nil {
		if _, ok := errors.AsType[*requestError](err); ok {
			writeUpdateError(w, err)
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
	// an answer. The second is what a harness's own request-user-input parks on
	// (see postHarnessQuestion): the run is waiting in place, and the answer is
	// the only thing that can continue it. A free-standing question — the `ask`
	// tool, which deliberately does not block a run — carries no run id and
	// resumes nothing.
	if item.RunID != "" && resolutionResumesRun(item.Kind, state) {
		access := runAccess{Creds: c, CR: clientCR{c}, ClusterID: id.clusterID}
		go s.resumeApprovedRun(wsScope, item, access, state != store.InboxStateDenied, req.Response)
	}
	writeJSON(w, http.StatusOK, item)
}
