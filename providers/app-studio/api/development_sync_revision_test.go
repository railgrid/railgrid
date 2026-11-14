/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fakeDevelopmentAgent models the dev-agent's authoritative revision fence
// and its status endpoint behind the data-plane route.
type fakeDevelopmentAgent struct {
	mu            sync.Mutex
	applied       uint64
	respondWith   uint64 // when set, the sync response reports this revision
	hideStatus    bool
	legacy        bool
	syncRevisions []uint64
}

func (a *fakeDevelopmentAgent) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	switch {
	case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/process"):
		revision := a.applied
		if a.hideStatus {
			revision = 0
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"running": true, "sourceRevision": revision})
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/sync"):
		var req projectSandboxSyncRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if a.legacy {
			var legacy struct {
				SourceRevision uint64 `json:"sourceRevision"`
				SourceDigest   string `json:"sourceDigest"`
			}
			// The old wire decoder ignores explicitSourceDigest.
			wire, err := json.Marshal(req)
			if err != nil || json.Unmarshal(wire, &legacy) != nil {
				http.Error(w, "invalid legacy request", http.StatusBadRequest)
				return
			}
			if legacy.SourceRevision != 0 && legacy.SourceDigest == "" {
				http.Error(w, "sourceRevision and sourceDigest must be supplied together", http.StatusBadRequest)
				return
			}
		}
		a.syncRevisions = append(a.syncRevisions, req.SourceRevision)
		if req.SourceRevision < a.applied {
			http.Error(w, "workspace sync revision is older than the applied revision", http.StatusConflict)
			return
		}
		if req.SourceRevision == a.applied {
			http.Error(w, "workspace sync revision was already applied with a different digest", http.StatusConflict)
			return
		}
		a.applied = req.SourceRevision
		if a.respondWith > a.applied {
			a.applied = a.respondWith
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"phase": "Synced", "sourceRevision": a.applied})
	default:
		http.NotFound(w, r)
	}
}

func TestPostProjectComponentSyncRecoversFromPlainSyncRevisions(t *testing.T) {
	ctx := context.Background()
	id := identity{clusterID: "cluster-a", orgUUID: "org-a", workspaceUUID: "ws-a"}
	ref := dataPlaneRef{Resource: "instances", Name: "demo-dev", Component: "web"}
	request := projectSandboxSyncRequest{Files: []projectSandboxSyncFile{{Path: "index.js", Content: "ok"}}, SourceDigest: "sha256:abc"}

	t.Run("older agent rejects sync before applying it", func(t *testing.T) {
		agent := &fakeDevelopmentAgent{legacy: true}
		hub := httptest.NewServer(agent)
		defer hub.Close()
		server := &Server{hubBase: hub.URL, callers: newTestCallers(nil, hub.URL)}
		_, err := server.postProjectComponentSync(ctx, id, ref, "web", 6, request)
		if err == nil || !strings.Contains(err.Error(), "explicit sync deletions") || len(agent.syncRevisions) != 0 {
			t.Fatalf("older agent: err = %v, sync revisions = %v", err, agent.syncRevisions)
		}
	})

	t.Run("plain sync moved the applied revision ahead", func(t *testing.T) {
		// App Studio last synced revision 5; a plain sync stamped 6 and 7.
		agent := &fakeDevelopmentAgent{applied: 7}
		hub := httptest.NewServer(agent)
		defer hub.Close()
		server := &Server{tenantWorkspaces: defaultTestWorkspaces.lookup, tenantActors: defaultTestActors.lookup, tenantProviders: defaultTestProviders, hubBase: hub.URL, callers: newTestCallers(nil, hub.URL)}

		if _, err := server.postProjectComponentSync(ctx, id, ref, "web", 6, request); err != nil {
			t.Fatalf("postProjectComponentSync: %v", err)
		}
		if got := agent.syncRevisions; len(got) != 2 || got[0] != 6 || got[1] != 8 {
			t.Fatalf("sync revisions = %v, want [6 8] (one renumbered retry)", got)
		}
		// Later FileStore revisions continue from the adopted numbering, as
		// do exec and status comparisons.
		if got := server.developmentSyncRevision(id, ref, 7); got != 9 {
			t.Fatalf("next mapped revision = %d, want 9", got)
		}
		if _, err := server.postProjectComponentSync(ctx, id, ref, "web", 7, request); err != nil {
			t.Fatalf("follow-up sync: %v", err)
		}
		if got := agent.syncRevisions[len(agent.syncRevisions)-1]; got != 9 || len(agent.syncRevisions) != 3 {
			t.Fatalf("follow-up sync revisions = %v, want a single sync at 9", agent.syncRevisions)
		}
		// Other components keep their own numbering.
		if got := server.developmentSyncRevision(id, dataPlaneRef{Resource: "instances", Name: "demo-dev", Component: "api"}, 7); got != 7 {
			t.Fatalf("other component mapped revision = %d, want 7", got)
		}
	})

	t.Run("same revision with a different digest", func(t *testing.T) {
		agent := &fakeDevelopmentAgent{applied: 6}
		hub := httptest.NewServer(agent)
		defer hub.Close()
		server := &Server{tenantWorkspaces: defaultTestWorkspaces.lookup, tenantActors: defaultTestActors.lookup, tenantProviders: defaultTestProviders, hubBase: hub.URL, callers: newTestCallers(nil, hub.URL)}
		if _, err := server.postProjectComponentSync(ctx, id, ref, "web", 6, request); err != nil {
			t.Fatalf("postProjectComponentSync: %v", err)
		}
		if got := agent.syncRevisions; len(got) != 2 || got[1] != 7 {
			t.Fatalf("sync revisions = %v, want retry at 7", got)
		}
	})

	t.Run("unknown applied revision surfaces the conflict", func(t *testing.T) {
		agent := &fakeDevelopmentAgent{applied: 7, hideStatus: true}
		hub := httptest.NewServer(agent)
		defer hub.Close()
		server := &Server{tenantWorkspaces: defaultTestWorkspaces.lookup, tenantActors: defaultTestActors.lookup, tenantProviders: defaultTestProviders, hubBase: hub.URL, callers: newTestCallers(nil, hub.URL)}
		_, err := server.postProjectComponentSync(ctx, id, ref, "web", 6, request)
		if err == nil || !strings.Contains(err.Error(), "409") {
			t.Fatalf("error = %v, want the 409 conflict", err)
		}
		if len(agent.syncRevisions) != 1 {
			t.Fatalf("sync revisions = %v, want no retry without an applied revision", agent.syncRevisions)
		}
	})

	t.Run("adopts a higher revision from the sync response", func(t *testing.T) {
		agent := &fakeDevelopmentAgent{applied: 1, respondWith: 12}
		hub := httptest.NewServer(agent)
		defer hub.Close()
		server := &Server{tenantWorkspaces: defaultTestWorkspaces.lookup, tenantActors: defaultTestActors.lookup, tenantProviders: defaultTestProviders, hubBase: hub.URL, callers: newTestCallers(nil, hub.URL)}
		if _, err := server.postProjectComponentSync(ctx, id, ref, "web", 3, request); err != nil {
			t.Fatalf("postProjectComponentSync: %v", err)
		}
		if got := server.developmentSyncRevision(id, ref, 4); got != 13 {
			t.Fatalf("mapped revision after a higher response = %d, want 13", got)
		}
	})
}
