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
	// TenantEdges lists the bare edge names engaged for the tenant
	// identified by its kcp logical-cluster ID.
	TenantEdges(ctx context.Context, cluster string) ([]string, error)
}

// EdgesHandler serves GET /api/edges: the caller's currently-engaged edges
// (the portal's edge selector source).
type EdgesHandler struct {
	Lister EdgeLister
}

// edgesResponse lists the caller's engaged edges. Edges are the bare edge
// names the portal shows and accepts as cluster.name; Clusters are the same
// edges as the "{clusterID}/{edge}" keys kuery records them under (what
// query results report in objects[].cluster); Tenant is the caller's kcp
// logical-cluster ID.
type edgesResponse struct {
	Tenant   string   `json:"tenant"`
	Edges    []string `json:"edges"`
	Clusters []string `json:"clusters"`
}

func (h *EdgesHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id, err := IdentityFromRequest(r)
	if err != nil {
		writeIdentityError(w, err)
		return
	}
	resp := edgesResponse{Tenant: id.Cluster, Edges: []string{}, Clusters: []string{}}
	if h.Lister != nil {
		edges, err := h.Lister.TenantEdges(r.Context(), id.Cluster)
		if err != nil {
			log.Printf("listing tenant edges: %v", err)
			http.Error(w, "listing edges failed", http.StatusInternalServerError)
			return
		}
		for _, edge := range edges {
			resp.Edges = append(resp.Edges, edge)
			resp.Clusters = append(resp.Clusters, id.Cluster+"/"+edge)
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}
