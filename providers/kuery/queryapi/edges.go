// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package queryapi

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
)

// EdgeLister is the slice of the engagement controller the edges endpoint
// needs. Answered from the shared store, so any replica lists the full
// fleet regardless of which replica syncs each edge. Nil-able: with
// engagement disabled the endpoint serves an empty list rather than 404, so
// the portal renders consistently in dev.
type EdgeLister interface {
	TenantEdges(ctx context.Context, tenant string) ([]string, error)
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
		http.Error(w, "missing tenant identity (X-Faros-Tenant)", http.StatusUnauthorized)
		return
	}
	resp := edgesResponse{Edges: []string{}}
	if h.Lister != nil {
		edges, err := h.Lister.TenantEdges(r.Context(), id.Tenant)
		if err != nil {
			log.Printf("listing tenant edges: %v", err)
			http.Error(w, "listing edges failed", http.StatusInternalServerError)
			return
		}
		if edges != nil {
			resp.Edges = edges
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}
