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

package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// staticActors is an actorLookup over a fixed token→user table, standing in
// for the SelfSubjectReview production posts through the hub as the caller.
type staticActors map[string]string

func (t staticActors) lookup(_ context.Context, _ string, token string) (string, error) {
	if token == "" {
		return "", errors.New("no caller token")
	}
	if user, ok := t[token]; ok {
		return user, nil
	}
	// Otherwise the token IS the identity, spelled "<user>-token", which is
	// the property the production change establishes: one bearer reviews as
	// exactly one subject. A test that wants two actors uses two tokens.
	return strings.TrimSuffix(token, "-token"), nil
}

// defaultTestActors gives the HTTP-level tests the actor the old
// X-Railgrid-User header used to supply.
var defaultTestActors = staticActors{"test-token": "test-user", "token": "test-user"}

// The actor is the answer to "who am I, with this token?", not a header. A
// forged X-Railgrid-User must not become anyone.
func TestActorComesFromTheTokenNotTheHeader(t *testing.T) {
	s := &Server{
		tenantWorkspaces: testWorkspaceLookup("cluster-a", "org-a", "workspace-a"),
		tenantActors:     staticActors{"alice-token": "alice"}.lookup,
	}

	r := httptest.NewRequest(http.MethodGet, "/api/projects", nil)
	r.Header.Set("X-Railgrid-Tenant", "cluster-a")
	r.Header.Set("X-Railgrid-Cluster", "cluster-a")
	r.Header.Set("X-Railgrid-User", "mallory")
	r.Header.Set("Authorization", "Bearer alice-token")
	id, ok := s.identityFromRequest(httptest.NewRecorder(), r)
	if !ok {
		t.Fatal("identity should authenticate")
	}
	if id.user != "alice" {
		t.Fatalf("actor = %q, want the reviewed subject %q", id.user, "alice")
	}
	if id.userLabel != "mallory" {
		t.Fatalf("userLabel = %q, want the header kept as a label", id.userLabel)
	}
}

// With no resolver the actor is empty and the reason is recorded — never a
// silent fallback to the header.
func TestActorIsUnresolvedWithoutALookup(t *testing.T) {
	s := &Server{tenantWorkspaces: testWorkspaceLookup("cluster-a", "org-a", "workspace-a")}

	r := httptest.NewRequest(http.MethodGet, "/api/projects", nil)
	r.Header.Set("X-Railgrid-Tenant", "cluster-a")
	r.Header.Set("X-Railgrid-Cluster", "cluster-a")
	r.Header.Set("X-Railgrid-User", "mallory")
	r.Header.Set("Authorization", "Bearer alice-token")
	id, _ := s.identityFromRequest(httptest.NewRecorder(), r)
	if id.user != "" {
		t.Fatalf("actor = %q, want empty without a lookup", id.user)
	}
	if !errors.Is(id.userErr, errNoActorLookup) {
		t.Fatalf("userErr = %v, want errNoActorLookup", id.userErr)
	}
}

// One page load makes many requests on one token; each must not cost a review.
func TestActorResolverCachesPerTokenAndCluster(t *testing.T) {
	var reviews atomic.Int32
	resolver := NewActorResolver("https://hub.example", false, time.Minute)
	resolver.review = func(_ context.Context, cluster, token string) (string, error) {
		reviews.Add(1)
		return "user-for-" + cluster + "-" + token, nil
	}

	for range 3 {
		user, err := resolver.Resolve(context.Background(), "cluster-a", "tok")
		if err != nil || user != "user-for-cluster-a-tok" {
			t.Fatalf("Resolve = %q, %v", user, err)
		}
	}
	if got := reviews.Load(); got != 1 {
		t.Fatalf("reviews = %d, want 1 for a repeated (cluster, token)", got)
	}

	// A different cluster is a different answer: the review is served by
	// whichever shard hosts that logical cluster.
	if _, err := resolver.Resolve(context.Background(), "cluster-b", "tok"); err != nil {
		t.Fatalf("Resolve on a second cluster: %v", err)
	}
	if got := reviews.Load(); got != 2 {
		t.Fatalf("reviews = %d, want a second cluster to review again", got)
	}

	// A different token is a different caller.
	if _, err := resolver.Resolve(context.Background(), "cluster-a", "other"); err != nil {
		t.Fatalf("Resolve with a second token: %v", err)
	}
	if got := reviews.Load(); got != 3 {
		t.Fatalf("reviews = %d, want a second token to review again", got)
	}
}

// The cache must not hold bearer tokens: it lives for the process lifetime and
// is read on every request.
func TestActorCacheKeyDoesNotContainTheToken(t *testing.T) {
	key := actorCacheKey("cluster-a", "super-secret-token")
	if strings.Contains(key, "super-secret-token") || strings.Contains(key, "cluster-a") {
		t.Fatalf("cache key %q leaks its inputs", key)
	}
	if key == actorCacheKey("cluster-a", "other-token") || key == actorCacheKey("cluster-b", "super-secret-token") {
		t.Fatal("cache key must separate tokens and clusters")
	}
}

// A hub blip must not pin the caller as anonymous for a whole TTL.
func TestActorResolverDoesNotCacheFailures(t *testing.T) {
	var reviews atomic.Int32
	resolver := NewActorResolver("https://hub.example", false, time.Minute)
	resolver.review = func(_ context.Context, _, _ string) (string, error) {
		if reviews.Add(1) == 1 {
			return "", errors.New("hub unavailable")
		}
		return "alice", nil
	}
	if _, err := resolver.Resolve(context.Background(), "cluster-a", "tok"); err == nil {
		t.Fatal("first Resolve should fail")
	}
	user, err := resolver.Resolve(context.Background(), "cluster-a", "tok")
	if err != nil || user != "alice" {
		t.Fatalf("second Resolve = %q, %v; want the retry to succeed", user, err)
	}
}

// An empty username is not an identity.
func TestActorResolverRejectsAnEmptyUsername(t *testing.T) {
	resolver := NewActorResolver("https://hub.example", false, time.Minute)
	resolver.review = func(_ context.Context, _, _ string) (string, error) { return "  ", nil }
	if _, err := resolver.Resolve(context.Background(), "cluster-a", "tok"); err == nil {
		t.Fatal("a blank SelfSubjectReview username must be an error")
	}
}
