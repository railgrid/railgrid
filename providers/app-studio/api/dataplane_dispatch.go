/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package api

// Three of the old routes were a collection and a member on the same path
// shape, distinguished only by whether a segment was present. On the grammar
// that is one verb — one grant — with the member in the tail, so the split
// happens here instead of in the router.

import (
	"net/http"

	"github.com/gorilla/mux"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// projectAssistantAttachmentRead lists the project's attachments, or returns
// one when the tail names it.
func (s *Server) projectAssistantAttachmentRead(w http.ResponseWriter, r *http.Request) {
	if mux.Vars(r)["attachment"] == "" {
		s.listProjectAssistantAttachments(w, r)
		return
	}
	s.getProjectAssistantAttachment(w, r)
}

// projectPreviewGrantWrite creates a preview grant, or revokes the one the
// tail names. Revocation is a POST (not a DELETE) because it records who
// revoked what rather than removing a row.
func (s *Server) projectPreviewGrantWrite(w http.ResponseWriter, r *http.Request) {
	if mux.Vars(r)["grant"] == "" {
		s.createProjectPreviewGrant(w, r)
		return
	}
	s.revokeProjectPreviewGrant(w, r)
}

// projectPublishingGrantWrite is projectPreviewGrantWrite for publishing.
func (s *Server) projectPublishingGrantWrite(w http.ResponseWriter, r *http.Request) {
	if mux.Vars(r)["grant"] == "" {
		s.createProjectPublishingGrant(w, r)
		return
	}
	s.revokeProjectPublishingGrant(w, r)
}

// adoptProjectAssistantSession creates the Session projection for a thread
// that has none, as the caller, so the thread becomes addressable as
// sessions/{thread}.
//
// It is deliberately narrow: the thread must already exist in the store for
// THIS project, so adopting cannot mint a projection for a conversation the
// caller has no claim to, and an existing Session is left exactly as it is.
func (s *Server) adoptProjectAssistantSession(w http.ResponseWriter, r *http.Request) {
	c, id, project, ok := s.requireProjectWithClient(w, r)
	if !ok {
		return
	}
	threadID := mux.Vars(r)["thread"]
	if threadID == "" {
		writeStatus(w, http.StatusBadRequest, "BadRequest", "adopt-session needs the thread id in the path")
		return
	}
	messageStore, ok := s.requireStore(w)
	if !ok {
		return
	}
	scope := projectMessageScope(id.orgUUID, id.workspaceUUID, project)
	thread, err := messageStore.GetAssistantThread(r.Context(), scope, threadID)
	if err != nil {
		writeProjectError(w, err)
		return
	}
	s.ensureSessionCR(r.Context(), c, id, project, thread.ID, thread.ActorID)
	sess, err := c.Resource(sessionResource, "").Get(r.Context(), thread.ID, metav1.GetOptions{})
	if err != nil {
		writeProjectError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"thread": thread.ID, "session": sess.GetName()})
}
