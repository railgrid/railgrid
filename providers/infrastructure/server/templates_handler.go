// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package server

import (
	"errors"
	"net/http"
	"strings"

	"github.com/faroshq/faros-kedge/providers/infrastructure/kro"
)

// templateListResponse wraps the array for forward-compatibility (we
// may want to add pagination or facet info later without re-versioning
// the endpoint).
type templateListResponse struct {
	Items []kro.Template `json:"items"`
}

type templateDetailResponse struct {
	Template kro.Template `json:"template"`
}

func (s *Server) handleListTemplates(w http.ResponseWriter, r *http.Request) {
	filter := kro.TemplateFilter{
		Category: r.URL.Query().Get("category"),
		Cloud:    r.URL.Query().Get("cloud"),
	}
	templates, err := s.kro.ListTemplates(r.Context(), filter)
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, ReasonUpstream, "kro list: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, templateListResponse{Items: templates})
}

func (s *Server) handleGetTemplate(w http.ResponseWriter, r *http.Request) {
	// Path is /api/templates/{name} after the mux strip. We do the
	// parse manually rather than gorilla/mux to keep deps tight — the
	// provider's mux is the stdlib ServeMux.
	name := strings.TrimPrefix(r.URL.Path, "/api/templates/")
	if name == "" || strings.Contains(name, "/") {
		writeJSONError(w, http.StatusBadRequest, ReasonBadRequest, "template name required and must be single-segment")
		return
	}
	version := r.URL.Query().Get("version")
	t, err := s.kro.GetTemplate(r.Context(), name, version)
	if err != nil {
		if errors.Is(err, kro.ErrTemplateNotFound) {
			writeJSONError(w, http.StatusNotFound, ReasonTemplateNotFound, err.Error())
			return
		}
		writeJSONError(w, http.StatusBadGateway, ReasonUpstream, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, templateDetailResponse{Template: *t})
}
