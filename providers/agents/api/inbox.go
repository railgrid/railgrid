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
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/faroshq/provider-agents/store"
)

// listInbox returns the cross-agent approvals + questions queue for the
// workspace. Filter with ?state=pending (default: all).
func (s *Server) listInboxItems(w http.ResponseWriter, r *http.Request) {
	_, id, ok := s.requireClient(w, r)
	if !ok {
		return
	}
	state := store.InboxItemState(strings.TrimSpace(r.URL.Query().Get("state")))
	items, err := s.store.ListInbox(r.Context(), store.Scope{OrgUUID: id.orgUUID, WorkspaceUUID: id.workspaceUUID}, state)
	if err != nil {
		writeStatus(w, http.StatusInternalServerError, "InternalError", err.Error())
		return
	}
	writeList(w, items)
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

// resolveInboxItem records the user's decision on an approval or question.
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
	item, err := s.resolveInboxDecision(r.Context(), wsScope, r.PathValue("id"), state, req.Response, time.Now().UTC())
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
	// Resume the paused run as the resolving user (tenant client + edges).
	if item.Kind == store.InboxKindApproval && item.RunID != "" && state != store.InboxStateAnswered {
		rd := resumeDeps{
			Creds: c, CR: clientCR{c},
			EdgesEndpoint: s.edgesEndpoint(id.clusterID), HubToken: id.token, EdgesInsecure: s.cfg.HubInsecure,
			ClusterID: id.clusterID,
		}
		go s.resumeApprovedRun(wsScope, item, rd, state == store.InboxStateApproved, req.Response)
	}
	writeJSON(w, http.StatusOK, item)
}
