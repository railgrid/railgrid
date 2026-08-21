/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package api

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// Streamed project creation. The plain POST /api/projects returns only the
// final Project, so the creation steps the backend emits via onStatus
// ("Planning project", "Configuring repository", "Attaching scaffold to
// <template>", …) are invisible. This SSE variant surfaces them so the
// wizard-first flow shows a real "scaffolding attached to the template" step
// — the moment the project opens on its starter code. It creates only the
// Project (+ repo + scaffold); the portal starts
// the first assistant turn afterward, exactly as the non-streamed path does.
//
// Event shapes (text/event-stream):
//
//	event: status   data: {"message":"Attaching scaffold to application"}
//	event: created  data: <Project JSON>
//	event: error    data: {"message":"..."}
func (s *Server) createProjectStream(w http.ResponseWriter, r *http.Request) {
	c, id, ok := s.requireProjectClient(w, r)
	if !ok {
		return
	}
	var req CreateProjectRequest
	if !decodeStrictJSON(w, r, &req) {
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeStatus(w, http.StatusInternalServerError, "InternalError", "streaming is not supported")
		return
	}

	sendEvent := func(event string, payload any) {
		data, err := json.Marshal(payload)
		if err != nil {
			return
		}
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data)
		flusher.Flush()
	}

	onStatus := func(message string) error {
		sendEvent("status", map[string]string{"message": message})
		return r.Context().Err() // stop the create if the client hung up
	}

	created, err := s.createProjectFromRequest(r.Context(), c, id, req, onStatus, r)
	if err != nil {
		sendEvent("error", map[string]string{"message": err.Error()})
		return
	}
	sendEvent("created", projectView(r.Context(), c, created, id))
}
