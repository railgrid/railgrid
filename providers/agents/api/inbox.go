// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package api

import (
	"encoding/json"
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
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

type resolveInboxRequest struct {
	// Decision: approve | deny | answer.
	Decision string `json:"decision"`
	Response string `json:"response,omitempty"`
}

// resolveInboxItem records the user's decision on an approval or question. When
// the tool loop lands, resolving an item resumes the checkpointed run; today it
// records the decision so the queue reflects reality.
func (s *Server) resolveInboxItem(w http.ResponseWriter, r *http.Request) {
	_, id, ok := s.requireClient(w, r)
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
	item, err := s.store.ResolveInboxItem(r.Context(), store.Scope{OrgUUID: id.orgUUID, WorkspaceUUID: id.workspaceUUID},
		r.PathValue("id"), state, req.Response, time.Now().UTC())
	if err != nil {
		writeStatus(w, http.StatusNotFound, "NotFound", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, item)
}
