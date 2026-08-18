// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/faroshq/provider-app-studio/store"
	"github.com/faroshq/provider-app-studio/workspace"
)

func affinityTestServer(t *testing.T, replicaID, addr string) (*Server, store.Store) {
	t.Helper()
	msgStore := store.NewMemoryStore()
	s := &Server{store: msgStore}
	s.SetReplicaRouting(replicaID, addr, "internal-token")
	return s, msgStore
}

func projectRequest(path string, method string) *http.Request {
	r := httptest.NewRequest(method, path, nil)
	r.Header.Set("X-Faros-Tenant", "root:faros:tenants:org-1:ws-1")
	return r
}

func TestReplicaAffinityClaimsUnownedProjectsAndServesLocally(t *testing.T) {
	s, msgStore := affinityTestServer(t, "replica-a", "10.0.0.1:8091")
	served := 0
	h := s.ReplicaAffinity(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		served++
		w.WriteHeader(http.StatusNoContent)
	}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, projectRequest("/api/projects/shop/hydrate-workspace", http.MethodPost))
	if served != 1 || rec.Code != http.StatusNoContent {
		t.Fatalf("unowned project not served locally: served=%d code=%d", served, rec.Code)
	}
	claim, ok, err := msgStore.GetReplicaClaim(context.Background(), projectClaimKey("org-1", "ws-1", "shop"))
	if err != nil || !ok || claim.OwnerReplica != "replica-a" || claim.OwnerAddr != "10.0.0.1:8091" {
		t.Fatalf("claim after local serve = %+v/%v/%v, want owned by replica-a", claim, ok, err)
	}
}

func TestReplicaAffinityForwardsForeignProjects(t *testing.T) {
	// The "owner" replica: an internal listener requiring the shared token.
	ownerHits := 0
	owner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(replicaInternalTokenHeader) != "internal-token" {
			t.Errorf("forwarded request missing internal token")
		}
		if r.URL.Path != "/api/projects/shop/sync-development" {
			t.Errorf("forwarded path = %q", r.URL.Path)
		}
		ownerHits++
		w.WriteHeader(http.StatusAccepted)
	}))
	defer owner.Close()
	ownerAddr := strings.TrimPrefix(owner.URL, "http://")

	s, msgStore := affinityTestServer(t, "replica-b", "10.0.0.2:8091")
	// Seed a fresh claim held by the owner replica.
	if _, held, err := msgStore.TryClaimReplica(context.Background(), store.ReplicaClaim{
		Key:          projectClaimKey("org-1", "ws-1", "shop"),
		Kind:         store.ReplicaClaimKindProject,
		ScopeKey:     projectClaimKey("org-1", "ws-1", "shop"),
		OwnerReplica: "replica-a",
		OwnerAddr:    ownerAddr,
	}, projectClaimTTL); err != nil || !held {
		t.Fatalf("seeding owner claim: %v/%v", held, err)
	}

	local := 0
	h := s.ReplicaAffinity(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { local++ }))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, projectRequest("/api/projects/shop/sync-development", http.MethodPost))
	if local != 0 || ownerHits != 1 || rec.Code != http.StatusAccepted {
		t.Fatalf("foreign project not forwarded: local=%d owner=%d code=%d", local, ownerHits, rec.Code)
	}
}

func TestReplicaAffinityServesStoreBackedReadsAnywhere(t *testing.T) {
	s, msgStore := affinityTestServer(t, "replica-b", "10.0.0.2:8091")
	// Fresh foreign claim exists, but store-backed reads never consult it.
	if _, held, err := msgStore.TryClaimReplica(context.Background(), store.ReplicaClaim{
		Key:          projectClaimKey("org-1", "ws-1", "shop"),
		Kind:         store.ReplicaClaimKindProject,
		ScopeKey:     projectClaimKey("org-1", "ws-1", "shop"),
		OwnerReplica: "replica-a",
		OwnerAddr:    "10.255.255.1:1", // unreachable — a forward would fail loudly
	}, projectClaimTTL); err != nil || !held {
		t.Fatalf("seeding owner claim: %v/%v", held, err)
	}
	served := 0
	h := s.ReplicaAffinity(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { served++ }))

	for _, path := range []string{
		"/api/projects/shop/assistant/threads/th-1/events", // SSE stream
		"/api/projects/shop/assistant/threads",
		"/api/projects/shop",
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, projectRequest(path, http.MethodGet))
		if rec.Code == http.StatusBadGateway {
			t.Fatalf("store-backed read %s was forwarded", path)
		}
	}
	if served != 3 {
		t.Fatalf("store-backed reads served locally = %d, want 3", served)
	}

	// Workspace-reading GETs are NOT local-safe.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, projectRequest("/api/projects/shop/files", http.MethodGet))
	if served != 3 {
		t.Fatal("workspace-reading GET bypassed affinity")
	}
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("workspace GET for a foreign project = %d, want forwarded (and here, 502 to the unreachable owner)", rec.Code)
	}
}

func TestReplicaAffinityLoopGuardAndCollectionRoutes(t *testing.T) {
	s, _ := affinityTestServer(t, "replica-b", "10.0.0.2:8091")
	served := 0
	h := s.ReplicaAffinity(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { served++ }))

	// A forwarded request must serve locally regardless of claims.
	r := projectRequest("/api/projects/shop/sync-development", http.MethodPost)
	r.Header.Set(replicaForwardedHeader, "1")
	h.ServeHTTP(httptest.NewRecorder(), r)

	// Collection endpoints under /api/projects/ are not project-scoped.
	for _, path := range []string{"/api/projects", "/api/projects/plan", "/api/projects/llm-settings"} {
		h.ServeHTTP(httptest.NewRecorder(), projectRequest(path, http.MethodPost))
	}
	if served != 4 {
		t.Fatalf("loop-guard/collection dispatch served = %d, want 4 local", served)
	}
}

func TestInternalReplicaHandlerRequiresToken(t *testing.T) {
	s, _ := affinityTestServer(t, "replica-a", "10.0.0.1:8091")
	inner := 0
	h := s.InternalReplicaHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(replicaForwardedHeader) == "" {
			t.Error("internal handler did not mark the request forwarded")
		}
		inner++
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/projects/shop/sync-development", nil))
	if rec.Code != http.StatusUnauthorized || inner != 0 {
		t.Fatalf("tokenless internal request = %d/%d, want 401", rec.Code, inner)
	}
	r := httptest.NewRequest(http.MethodPost, "/api/projects/shop/sync-development", nil)
	r.Header.Set(replicaInternalTokenHeader, "internal-token")
	h.ServeHTTP(httptest.NewRecorder(), r)
	if inner != 1 {
		t.Fatal("authenticated internal request rejected")
	}
}

func TestAssistantBusySeesForeignActivityClaims(t *testing.T) {
	s, msgStore := affinityTestServer(t, "replica-b", "")
	scope := store.Scope{OrgUUID: "org-1", WorkspaceUUID: "ws-1", ProjectName: "shop", ProjectUID: "uid-1"}
	if _, held, err := msgStore.TryClaimReplica(context.Background(), store.ReplicaClaim{
		Key:          store.ActivityClaimKey(scope),
		Kind:         store.ReplicaClaimKindActivity,
		ScopeKey:     store.ReplicaClaimScopeKey(scope),
		OwnerReplica: "replica-a",
		Detail:       "run-1",
	}, assistantActivityClaimTTL); err != nil || !held {
		t.Fatalf("seeding activity claim: %v/%v", held, err)
	}
	wsScope := workspace.Scope{OrgUUID: scope.OrgUUID, WorkspaceUUID: scope.WorkspaceUUID, ProjectName: scope.ProjectName, ProjectUID: scope.ProjectUID}
	if !s.AssistantBusy(wsScope) {
		t.Fatal("AssistantBusy ignored a fresh foreign activity claim")
	}
	// Released claim → not busy.
	if err := msgStore.ReleaseReplicaClaim(context.Background(), store.ActivityClaimKey(scope), "replica-a"); err != nil {
		t.Fatalf("release: %v", err)
	}
	if s.AssistantBusy(wsScope) {
		t.Fatal("AssistantBusy stuck after the claim was released")
	}
}
