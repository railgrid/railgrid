/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package api

import (
	"net/http"
	"strings"

	"github.com/faroshq/provider-app-studio/workspace"
)

// Read-only workspace file access for the portal's code explorer. The
// workspace FileStore is the live development source — what the assistant
// edits and what development sync pushes into the sandbox — so browsing it
// shows exactly what the dev environment runs. Both handlers are token-gated
// through requireProjectWithClient (the caller must be able to GET the
// Project), and they never mutate anything.

// listProjectFiles is GET /api/projects/{project}/files — the whole workspace
// tree as a flat, sorted path list with sizes. The client builds the tree.
func (s *Server) listProjectFiles(w http.ResponseWriter, r *http.Request) {
	_, id, project, ok := s.requireProjectWithClient(w, r)
	if !ok {
		return
	}
	if s.workspaces == nil {
		writeStatus(w, http.StatusNotImplemented, "NotImplemented", "project workspace store is not configured")
		return
	}
	list, err := s.workspaces.ListFiles(r.Context(), projectWorkspaceScope(id, project), workspace.ListOptions{})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

// readProjectFile is GET /api/projects/{project}/files/content?path=... — one
// file's bounded content (with its opaque version, binary/truncated flags).
func (s *Server) readProjectFile(w http.ResponseWriter, r *http.Request) {
	_, id, project, ok := s.requireProjectWithClient(w, r)
	if !ok {
		return
	}
	if s.workspaces == nil {
		writeStatus(w, http.StatusNotImplemented, "NotImplemented", "project workspace store is not configured")
		return
	}
	path := strings.TrimSpace(r.URL.Query().Get("path"))
	if path == "" {
		writeStatus(w, http.StatusBadRequest, "BadRequest", "path query parameter is required")
		return
	}
	content, err := s.workspaces.ReadFile(r.Context(), projectWorkspaceScope(id, project), workspace.ReadOptions{
		Path:     path,
		MaxBytes: workspace.MaxReadMaxBytes,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, content)
}
