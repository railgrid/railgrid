// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package queryapi

import (
	"encoding/json"
	"net/http"
)

// EdgeLister is the slice of the engagement controller the edges endpoint
// needs. Nil-able: with engagement disabled the endpoint serves an empty
// list rather than 404, so the portal renders consistently in dev.
type EdgeLister interface {
	TenantEdges(tenant string) []string
}

// EdgesHandler serves GET /api/edges: the caller's currently-engaged edge
// names (the portal's edge selector source).
type EdgesHandler struct {
	Lister EdgeLister
}

type edgesResponse struct {
	Edges []string `json:"edges"`
}

func (h *EdgesHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id := IdentityFromRequest(r)
	if id.Tenant == "" {
		http.Error(w, "missing tenant identity (X-Kedge-Tenant)", http.StatusUnauthorized)
		return
	}
	resp := edgesResponse{Edges: []string{}}
	if h.Lister != nil {
		if edges := h.Lister.TenantEdges(id.Tenant); edges != nil {
			resp.Edges = edges
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}
