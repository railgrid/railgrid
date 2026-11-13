// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/railgrid/provider-agents/store"
)

const sessionNotificationRequestMaxBytes = 20 << 10

type sessionAccessRequest struct {
	Subject string `json:"subject"`
}

type deliverSessionNotificationRequest struct {
	Key           string  `json:"key"`
	Payload       string  `json:"payload"`
	ExpectedOwner *string `json:"expectedOwner,omitempty"`
}

func (s *Server) sessionNotificationContext(w http.ResponseWriter, r *http.Request) (identity, string, store.SessionNotificationStore, bool) {
	_, id, ok := s.requireClient(w, r)
	if !ok {
		return identity{}, "", nil, false
	}
	if strings.TrimSpace(id.user) == "" {
		writeStatus(w, http.StatusUnauthorized, "Unauthorized", "an authenticated caller subject is required")
		return identity{}, "", nil, false
	}
	sessionID := strings.TrimSpace(r.PathValue("tail"))
	gate, gated := gateFrom(r.Context())
	if sessionID == "" || !gated || gate.object == nil || gate.object.GetUID() == "" {
		writeStatus(w, http.StatusBadRequest, "BadRequest", "a session ID and current Agent identity are required")
		return identity{}, "", nil, false
	}
	storeAPI, ok := s.store.(store.SessionNotificationStore)
	if !ok {
		writeStatus(w, http.StatusServiceUnavailable, "Unavailable", "session notification storage is unavailable")
		return identity{}, "", nil, false
	}
	return id, string(gate.object.GetUID()), storeAPI, true
}

func decodeBoundedJSON(w http.ResponseWriter, r *http.Request, target any) error {
	r.Body = http.MaxBytesReader(w, r.Body, sessionNotificationRequestMaxBytes)
	decoder := json.NewDecoder(r.Body)
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("request must contain one JSON value")
		}
		return err
	}
	return nil
}

func (s *Server) sessionAccess(w http.ResponseWriter, r *http.Request) {
	id, agentUID, access, ok := s.sessionNotificationContext(w, r)
	if !ok {
		return
	}
	var request sessionAccessRequest
	if err := decodeBoundedJSON(w, r, &request); err != nil {
		writeStatus(w, http.StatusBadRequest, "BadRequest", "invalid session access request: "+err.Error())
		return
	}
	request.Subject = strings.TrimSpace(request.Subject)
	if len(request.Subject) == 0 || len(request.Subject) > 512 || strings.ContainsAny(request.Subject, "\r\n") {
		writeStatus(w, http.StatusBadRequest, "BadRequest", "subject is required and must be at most 512 bytes")
		return
	}
	var err error
	if r.Method == http.MethodDelete {
		err = access.RevokeSessionAccess(r.Context(), id.scope(r.PathValue("name")), agentUID, r.PathValue("tail"), id.user, request.Subject)
	} else {
		err = access.GrantSessionAccess(r.Context(), id.scope(r.PathValue("name")), agentUID, r.PathValue("tail"), id.user, request.Subject, time.Now().UTC())
	}
	if errors.Is(err, store.ErrSessionAccessDenied) {
		writeStatus(w, http.StatusForbidden, "Forbidden", "only the session owner can change exact-session access")
		return
	}
	if err != nil {
		writeStatus(w, http.StatusServiceUnavailable, "Unavailable", "session access storage failed; retry the request")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) deliverSessionNotification(w http.ResponseWriter, r *http.Request) {
	id, agentUID, notifications, ok := s.sessionNotificationContext(w, r)
	if !ok {
		return
	}
	var request deliverSessionNotificationRequest
	if err := decodeBoundedJSON(w, r, &request); err != nil {
		writeStatus(w, http.StatusBadRequest, "BadRequest", "invalid notification request: "+err.Error())
		return
	}
	request.Key, request.Payload = strings.TrimSpace(request.Key), strings.TrimSpace(request.Payload)
	if request.ExpectedOwner != nil {
		*request.ExpectedOwner = strings.TrimSpace(*request.ExpectedOwner)
	}
	if request.ExpectedOwner != nil && (*request.ExpectedOwner == "" || len(*request.ExpectedOwner) > 512 || strings.ContainsAny(*request.ExpectedOwner, "\r\n")) {
		writeStatus(w, http.StatusBadRequest, "BadRequest", "expectedOwner must be at most 512 bytes and contain no line breaks")
		return
	}
	var ownerBinding []string
	if request.ExpectedOwner != nil {
		ownerBinding = []string{*request.ExpectedOwner}
	}
	item, reused, err := notifications.CreateSessionNotification(r.Context(), id.scope(r.PathValue("name")), agentUID, r.PathValue("tail"), id.user, request.Key, request.Payload, time.Now().UTC(), ownerBinding...)
	switch {
	case errors.Is(err, store.ErrSessionAccessDenied):
		writeStatus(w, http.StatusForbidden, "Forbidden", "caller has no explicit access to this session")
	case errors.Is(err, store.ErrNotificationConflict):
		writeStatus(w, http.StatusConflict, "Conflict", "notification key was already used with different content")
	case errors.Is(err, store.ErrNotificationInput):
		writeStatus(w, http.StatusBadRequest, "BadRequest", err.Error())
	case err != nil:
		writeStatus(w, http.StatusServiceUnavailable, "Unavailable", "notification storage failed; retry with the same key")
	default:
		writeJSON(w, http.StatusOK, map[string]any{"notification": item, "reused": reused})
	}
}

func (s *Server) listSessionNotifications(w http.ResponseWriter, r *http.Request) {
	id, agentUID, notifications, ok := s.sessionNotificationContext(w, r)
	if !ok {
		return
	}
	items, err := notifications.ListSessionNotifications(r.Context(), id.scope(r.PathValue("name")), agentUID, r.PathValue("tail"), id.user, 100)
	if errors.Is(err, store.ErrSessionAccessDenied) {
		writeStatus(w, http.StatusForbidden, "Forbidden", "caller has no explicit access to this session")
		return
	}
	if err != nil {
		writeStatus(w, http.StatusServiceUnavailable, "Unavailable", "notification storage failed; retry the request")
		return
	}
	writeList(w, items)
}
