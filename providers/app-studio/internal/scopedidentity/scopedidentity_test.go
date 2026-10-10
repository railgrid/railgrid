/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package scopedidentity

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	rbacv1 "k8s.io/api/rbac/v1"

	"github.com/railgrid/provider-sdk/identityclient"
)

type revisionTestHub struct {
	mu      sync.Mutex
	current identityclient.OwnerRevision
	posts   []map[string]any
	mints   int
	ttl     time.Duration
}

func (h *revisionTestHub) handler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || r.URL.Path != identityclient.PathIdentities {
		http.NotFound(w, r)
		return
	}
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.posts = append(h.posts, body)
	requestedGeneration, _ := body["expectedOwnerGeneration"].(float64)
	requestedResourceVersion, _ := body["expectedOwnerResourceVersion"].(string)
	if int64(requestedGeneration) != h.current.Generation || requestedResourceVersion != h.current.ResourceVersion {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(identityclient.Error{Code: identityclient.ErrorCodeStaleOwner, Message: "owner revision changed"})
		return
	}
	h.mints++
	ttl := h.ttl
	if ttl <= 0 {
		ttl = time.Hour
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(identityclient.Token{
		Token: fmt.Sprintf("token-%d", h.mints), TokenType: "Bearer",
		ExpiresAt: time.Now().Add(ttl), ServiceAccount: "railgrid-si-test", Name: "si-test",
		OwnerRevisionVerified: true,
	})
}

func waitForTokenSourceRefresh(t *testing.T, started time.Time, ttl time.Duration) {
	t.Helper()
	refreshAt := started.Add(time.Duration(float64(ttl)*0.8) + 100*time.Millisecond)
	if delay := time.Until(refreshAt); delay > 0 {
		time.Sleep(delay)
	}
}

func newRevisionTestCache(t *testing.T, hub *revisionTestHub) *Cache {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(hub.handler))
	t.Cleanup(server.Close)
	client, err := identityclient.New(identityclient.Options{
		HubURL: server.URL, Provider: "app-studio", Token: "provider-token", HTTPClient: server.Client(),
	})
	if err != nil {
		t.Fatalf("identity client: %v", err)
	}
	return New(client)
}

func revisionTestOwner() identityclient.Owner {
	return identityclient.Owner{
		Provider: "app-studio", Kind: "Project", Group: "ai.railgrid.ai", Version: "v1alpha1",
		Resource: "projects", Name: "demo", UID: "project-uid", ClusterID: "cluster-a",
	}
}

func revisionTestRules(name string) []rbacv1.PolicyRule {
	return []rbacv1.PolicyRule{{
		APIGroups: []string{"code.railgrid.ai"}, Resources: []string{"repositories"},
		ResourceNames: []string{name}, Verbs: []string{"get"},
	}}
}

func TestOwnerResourceVersionChurnReusesTokenAndRefreshesWithLatestRevision(t *testing.T) {
	firstRevision := identityclient.OwnerRevision{Generation: 5, ResourceVersion: "opaque-rv-a"}
	latestRevision := identityclient.OwnerRevision{Generation: 5, ResourceVersion: "opaque-rv-b"}
	hub := &revisionTestHub{current: firstRevision, ttl: 2 * time.Second}
	cache := newRevisionTestCache(t, hub)
	owner := revisionTestOwner()
	rules := revisionTestRules("demo-repo")

	started := time.Now()
	first, err := cache.TokenVersionedObserved(context.Background(), owner, firstRevision, rules)
	if err != nil || first != "token-1" {
		t.Fatalf("initial token = %q, %v", first, err)
	}
	entry := cache.sources[Key(owner)]
	if entry == nil || entry.source == nil {
		t.Fatal("initial token source was not retained")
	}
	hub.mu.Lock()
	hub.current = latestRevision
	hub.mu.Unlock()
	second, err := cache.TokenVersionedObserved(context.Background(), owner, latestRevision, rules)
	if err != nil || second != first {
		t.Fatalf("same-rules token after RV churn = %q, %v; want cached %q", second, err, first)
	}
	if len(hub.posts) != 1 {
		t.Fatalf("resourceVersion-only change minted early: posts=%d", len(hub.posts))
	}

	waitForTokenSourceRefresh(t, started, 2*time.Second)
	third, err := cache.TokenVersionedObserved(context.Background(), owner, latestRevision, rules)
	if err != nil || third == first {
		t.Fatalf("refresh token = %q, %v; want a newly minted token", third, err)
	}
	if len(hub.posts) != 2 || hub.posts[1]["expectedOwnerResourceVersion"] != latestRevision.ResourceVersion {
		t.Fatalf("refresh request did not carry latest revision: posts=%#v", hub.posts)
	}
}

func TestIndependentCachesCannotReuseOldTokenAfterOwnerRevisionChanges(t *testing.T) {
	oldRevision := identityclient.OwnerRevision{Generation: 8, ResourceVersion: "opaque-old"}
	newRevision := identityclient.OwnerRevision{Generation: 8, ResourceVersion: "opaque-new"}
	hub := &revisionTestHub{current: oldRevision, ttl: 2 * time.Second}
	firstReplica := newRevisionTestCache(t, hub)
	secondReplica := newRevisionTestCache(t, hub)
	owner := revisionTestOwner()
	oldRules := revisionTestRules("old-repo")
	newRules := revisionTestRules("new-repo")

	firstStarted := time.Now()
	first, err := firstReplica.TokenVersionedObserved(context.Background(), owner, oldRevision, oldRules)
	if err != nil || first != "token-1" {
		t.Fatalf("old replica initial token = %q, %v", first, err)
	}
	entry := firstReplica.sources[Key(owner)]
	if entry == nil || entry.source == nil {
		t.Fatal("first replica source missing")
	}
	hub.mu.Lock()
	hub.current = newRevision
	hub.mu.Unlock()
	if token, err := secondReplica.TokenVersionedObserved(context.Background(), owner, newRevision, newRules); err != nil || token != "token-2" {
		t.Fatalf("new replica token = %q, %v", token, err)
	}

	// The first replica has not observed the update. The hub rejects its old
	// expected revision; its still-live token must not be used as a fallback.
	waitForTokenSourceRefresh(t, firstStarted, 2*time.Second)
	if token, err := firstReplica.TokenVersionedObserved(context.Background(), owner, oldRevision, oldRules); err == nil || token != "" {
		t.Fatalf("stale replica returned token=%q, err=%v", token, err)
	} else {
		var stale *identityclient.Error
		if !errors.As(err, &stale) || stale.Code != identityclient.ErrorCodeStaleOwner {
			t.Fatalf("stale replica error = %#v", err)
		}
	}
	// The stale error is returned from TokenSource through Cache; verify the
	// request path itself used the old revision after the newer replica's mint.
	if len(hub.posts) != 3 || hub.posts[2]["expectedOwnerResourceVersion"] != oldRevision.ResourceVersion {
		t.Fatalf("stale replica request history = %#v", hub.posts)
	}
}
