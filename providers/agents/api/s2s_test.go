// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/faroshq/provider-agents/store"
	"github.com/faroshq/provider-agents/tools"
)

// saToken builds a JWT-shaped kcp ServiceAccount token. Only the payload matters:
// nothing here verifies signatures, and neither does parseServiceAccountToken —
// it reads the claims solely to learn WHERE to send the token for verification.
func saToken(t *testing.T, cluster string) string {
	t.Helper()
	payload, err := json.Marshal(saTokenClaims{Issuer: "kubernetes/serviceaccount", ClusterName: cluster})
	if err != nil {
		t.Fatal(err)
	}
	enc := base64.RawURLEncoding.EncodeToString
	return enc([]byte(`{"alg":"RS256"}`)) + "." + enc(payload) + ".sig"
}

func TestParseServiceAccountToken(t *testing.T) {
	t.Run("a kcp SA token yields its home cluster", func(t *testing.T) {
		claims, ok := parseServiceAccountToken(saToken(t, "cluster-abc"))
		if !ok || claims.ClusterName != "cluster-abc" {
			t.Fatalf("ok=%v cluster=%q", ok, claims.ClusterName)
		}
	})

	for _, tc := range []struct {
		name, token string
	}{
		{"not a JWT", "opaque-token"},
		{"wrong segment count", "a.b"},
		{"undecodable payload", "aaa.!!!!.ccc"},
		{"payload is not JSON", base64.RawURLEncoding.EncodeToString([]byte("x")) + "." +
			base64.RawURLEncoding.EncodeToString([]byte("not json")) + ".c"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, ok := parseServiceAccountToken(tc.token); ok {
				t.Fatal("must not be taken for a ServiceAccount token")
			}
		})
	}

	t.Run("a user OIDC token is not an SA token", func(t *testing.T) {
		enc := base64.RawURLEncoding.EncodeToString
		payload, _ := json.Marshal(map[string]any{"iss": "https://dex.example", "sub": "alice"})
		tok := enc([]byte(`{"alg":"RS256"}`)) + "." + enc(payload) + ".sig"
		if _, ok := parseServiceAccountToken(tok); ok {
			t.Fatal("an OIDC token has no home logical cluster and must take the tenant path")
		}
	})

	t.Run("an SA token with no clusterName claim is rejected", func(t *testing.T) {
		enc := base64.RawURLEncoding.EncodeToString
		payload, _ := json.Marshal(map[string]any{"iss": "kubernetes/serviceaccount"})
		tok := enc([]byte(`{"alg":"RS256"}`)) + "." + enc(payload) + ".sig"
		if _, ok := parseServiceAccountToken(tok); ok {
			t.Fatal("without a home cluster there is nowhere to authenticate it")
		}
	})
}

func TestQualifyServiceAccount(t *testing.T) {
	// Two workspaces can each have a "default/runner" SA; without qualification a
	// binding for one would authorize the other.
	got, ok := qualifyServiceAccount("cluster-a", "system:serviceaccount:default:runner")
	if !ok || got != "system:serviceaccount:cluster-a:default:runner" {
		t.Fatalf("qualified = %q ok=%v", got, ok)
	}
	if _, ok := qualifyServiceAccount("cluster-a", "alice@example.com"); ok {
		t.Fatal("a non-ServiceAccount identity must not be qualified as one")
	}
}

func TestShardBase(t *testing.T) {
	cases := map[string]string{
		"https://shard.example/services/apiexport/abc/agents.kedge.faros.sh/clusters/xyz": "https://shard.example/services/apiexport/abc/agents.kedge.faros.sh",
		"https://shard.example/base":  "https://shard.example/base",
		"https://shard.example/base/": "https://shard.example/base",
	}
	for in, want := range cases {
		if got := shardBase(in); got != want {
			t.Fatalf("shardBase(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestS2SAuthCache(t *testing.T) {
	c := newS2SAuthCache()
	if _, ok := c.get("k"); ok {
		t.Fatal("empty cache must miss")
	}
	c.put("k", s2sAuthEntry{user: "system:serviceaccount:c:ns:sa"})
	got, ok := c.get("k")
	if !ok || got.user != "system:serviceaccount:c:ns:sa" {
		t.Fatalf("hit=%v user=%q", ok, got.user)
	}

	t.Run("denials are cached too", func(t *testing.T) {
		// A misconfigured caller retrying in a loop must not become load on kcp.
		c.put("denied", s2sAuthEntry{err: errors.New("access denied")})
		got, ok := c.get("denied")
		if !ok || got.err == nil {
			t.Fatalf("hit=%v err=%v", ok, got.err)
		}
	})

	t.Run("entries expire", func(t *testing.T) {
		c.mu.Lock()
		c.entries["stale"] = s2sAuthEntry{user: "u", at: time.Now().Add(-2 * s2sAuthTTL)}
		c.mu.Unlock()
		if _, ok := c.get("stale"); ok {
			t.Fatal("a revoked binding must stop working when the entry ages out")
		}
	})
}

// The cache key must separate every dimension of a decision, or an allow for one
// agent/verb/workspace would authorize another.
func TestS2SAuthCacheKeyIsSpecific(t *testing.T) {
	ctx := context.Background()
	s := &Server{store: store.NewMemoryStore(), s2sAuth: newS2SAuthCache()}

	// No background executor, so every review fails the same way — which is what
	// makes this a pure test of key separation: each distinct key must produce its
	// own (failed) lookup rather than reusing a neighbour's.
	calls := map[string]bool{}
	for _, tc := range []struct{ cluster, verb, agent string }{
		{"c1", "create", "a1"},
		{"c2", "create", "a1"},
		{"c1", "get", "a1"},
		{"c1", "create", "a2"},
	} {
		if _, err := s.authorizeS2S(ctx, tc.cluster, "tok", tc.verb, tc.agent); err == nil {
			t.Fatal("expected failure without a virtual-workspace connection")
		}
		calls[tc.cluster+"|"+tc.verb+"|"+tc.agent] = true
	}
	if len(calls) != 4 {
		t.Fatalf("expected 4 distinct decisions, got %d", len(calls))
	}
	s.s2sAuth.mu.Lock()
	n := len(s.s2sAuth.entries)
	s.s2sAuth.mu.Unlock()
	if n != 4 {
		t.Fatalf("cache holds %d entries for 4 distinct (cluster, verb, agent) tuples", n)
	}
}

func TestS2SRequiresABearerToken(t *testing.T) {
	s := &Server{store: store.NewMemoryStore(), s2sAuth: newS2SAuthCache()}
	if _, err := s.authorizeS2S(context.Background(), "c1", "", "create", "a1"); err == nil {
		t.Fatal("a missing token must be refused before any review")
	} else if !strings.Contains(err.Error(), "bearer token is required") {
		t.Fatalf("err = %v", err)
	}
}

// Availability failures must not read as authorization failures: a caller seeing
// 403 will go rewrite RBAC, while 503 tells it to retry.
func TestWriteS2SAuthErrorStatuses(t *testing.T) {
	s := &Server{}
	cases := []struct {
		err  string
		want int
	}{
		{"service-to-service access is unavailable: no virtual-workspace connection yet", http.StatusServiceUnavailable},
		{"locating workspace c1: not found on any shard", http.StatusServiceUnavailable},
		{"a bearer token is required", http.StatusUnauthorized},
		{`"system:serviceaccount:c:ns:sa" may not create agent "x"`, http.StatusForbidden},
		{"the presented token is not valid in this platform", http.StatusForbidden},
	}
	for _, tc := range cases {
		w := httptest.NewRecorder()
		s.writeS2SAuthError(w, errors.New(tc.err))
		if w.Code != tc.want {
			t.Fatalf("%q → %d, want %d", tc.err, w.Code, tc.want)
		}
	}
}

func TestS2SScopeRequiresAMappedWorkspace(t *testing.T) {
	ctx := context.Background()
	s := &Server{store: store.NewMemoryStore()}

	// Unmapped: refuse rather than record the run under a fallback scope the
	// portal never reads.
	if _, err := s.s2sScope(ctx, "c-unknown", "a1"); err == nil {
		t.Fatal("an unmapped workspace must be refused")
	} else if !strings.Contains(err.Error(), "not mapped yet") {
		t.Fatalf("err = %v", err)
	}

	if err := s.store.SaveTenantRef(ctx, "c1", store.TenantRef{
		OrgUUID: "o", WorkspaceUUID: "w", UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	scope, err := s.s2sScope(ctx, "c1", "a1")
	if err != nil {
		t.Fatal(err)
	}
	if scope.OrgUUID != "o" || scope.WorkspaceUUID != "w" || scope.AgentName != "a1" {
		t.Fatalf("scope = %+v", scope)
	}
}

// The S2S routes must never be reachable without the provider doing its own
// authorization — a missing background executor has to fail closed.
func TestS2SFailsClosedWithoutVirtualWorkspace(t *testing.T) {
	s := &Server{store: store.NewMemoryStore(), s2sAuth: newS2SAuthCache()}
	if _, err := s.s2sVWConfig(context.Background(), "c1"); err == nil {
		t.Fatal("without a VW connection there is no way to authenticate anyone; must fail")
	}

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/s2s/clusters/c1/agents/a1/runs",
		strings.NewReader(`{"task":"do it"}`))
	r.SetPathValue("cluster", "c1")
	r.SetPathValue("name", "a1")
	r.Header.Set("Authorization", "Bearer "+saToken(t, "c1"))
	s.s2sInvoke(w, r)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", w.Code)
	}
}

func TestS2SInvokeRejectsAnEmptyTask(t *testing.T) {
	s := &Server{store: store.NewMemoryStore(), s2sAuth: newS2SAuthCache()}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/s2s/clusters/c1/agents/a1/runs", strings.NewReader(`{"task":"  "}`))
	r.SetPathValue("cluster", "c1")
	r.SetPathValue("name", "a1")
	s.s2sInvoke(w, r)
	// Refused on shape before any authorization work is done.
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

func TestRunCallbackValidate(t *testing.T) {
	// Nil is the norm — most callers poll.
	var nilCB *runCallback
	if err := nilCB.validate(); err != nil {
		t.Fatalf("nil callback should be fine: %v", err)
	}
	for _, tc := range []struct{ name, url string }{
		{"empty", ""},
		{"relative", "/hook"},
		{"no host", "https://"},
		{"wrong scheme", "ftp://example.com/hook"},
	} {
		t.Run("rejects "+tc.name, func(t *testing.T) {
			cb := &runCallback{URL: tc.url}
			if err := cb.validate(); err == nil {
				t.Fatalf("url %q must be refused", tc.url)
			}
		})
	}
	cb := &runCallback{URL: "  https://example.com/hook  "}
	if err := cb.validate(); err != nil {
		t.Fatal(err)
	}
	if cb.URL != "https://example.com/hook" {
		t.Fatalf("url = %q, want it trimmed", cb.URL)
	}
}

func TestDeliverRunCallback(t *testing.T) {
	ctx := context.Background()
	scope := store.Scope{OrgUUID: "o", WorkspaceUUID: "w", AgentName: "scout"}
	newServerWithRun := func(t *testing.T, phase store.RunPhase) *Server {
		t.Helper()
		s := &Server{store: store.NewMemoryStore()}
		now := time.Now().UTC()
		fin := now
		if err := s.store.SaveRun(ctx, scope, store.Run{
			ID: "r1", AgentName: "scout", Phase: phase, Output: "the answer",
			Sources: []string{"https://a.example/x"}, InputTokens: 10, OutputTokens: 5,
			CreatedAt: now, UpdatedAt: now, FinishedAt: &fin,
		}); err != nil {
			t.Fatal(err)
		}
		return s
	}

	t.Run("posts the run's outcome, signed", func(t *testing.T) {
		type received struct {
			body, sig string
		}
		got := make(chan received, 1)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			b := make([]byte, r.ContentLength)
			_, _ = r.Body.Read(b)
			got <- received{string(b), r.Header.Get(callbackSignatureHeader)}
		}))
		defer srv.Close()

		s := newServerWithRun(t, store.RunPhaseSucceeded)
		s.deliverRunCallback(ctx, scope, "r1", &runCallback{URL: srv.URL, Secret: "shh"})

		select {
		case r := <-got:
			var payload runCallbackPayload
			if err := json.Unmarshal([]byte(r.body), &payload); err != nil {
				t.Fatalf("body is not the documented payload: %v (%q)", err, r.body)
			}
			if payload.RunID != "r1" || payload.Phase != "Succeeded" || payload.Output != "the answer" {
				t.Fatalf("payload = %+v", payload)
			}
			if payload.Usage.InputTokens != 10 {
				t.Fatalf("usage not reported: %+v", payload.Usage)
			}
			// A receiver must be able to authenticate the callback.
			if !strings.HasPrefix(r.sig, "sha256=") {
				t.Fatalf("signature = %q, want an HMAC", r.sig)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("no callback received")
		}
	})

	t.Run("no signature header without a secret", func(t *testing.T) {
		got := make(chan string, 1)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			got <- r.Header.Get(callbackSignatureHeader)
		}))
		defer srv.Close()
		s := newServerWithRun(t, store.RunPhaseSucceeded)
		s.deliverRunCallback(ctx, scope, "r1", &runCallback{URL: srv.URL})
		if sig := <-got; sig != "" {
			t.Fatalf("signature = %q, want none", sig)
		}
	})

	t.Run("a failure is reported too", func(t *testing.T) {
		got := make(chan string, 1)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var p runCallbackPayload
			_ = json.NewDecoder(r.Body).Decode(&p)
			got <- p.Phase
		}))
		defer srv.Close()
		s := newServerWithRun(t, store.RunPhaseFailed)
		s.deliverRunCallback(ctx, scope, "r1", &runCallback{URL: srv.URL})
		// A caller waiting on a callback must hear about a failure, not time out.
		if phase := <-got; phase != "Failed" {
			t.Fatalf("phase = %q, want Failed", phase)
		}
	})

	t.Run("retries then gives up without blocking forever", func(t *testing.T) {
		var attempts int
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			attempts++
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer srv.Close()
		s := newServerWithRun(t, store.RunPhaseSucceeded)
		done := make(chan struct{})
		go func() {
			s.deliverRunCallback(ctx, scope, "r1", &runCallback{URL: srv.URL})
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(30 * time.Second):
			t.Fatal("delivery must give up rather than retry forever")
		}
		if attempts != callbackAttempts {
			t.Fatalf("%d attempts, want %d", attempts, callbackAttempts)
		}
	})

	t.Run("nil callback is a no-op", func(t *testing.T) {
		s := newServerWithRun(t, store.RunPhaseSucceeded)
		s.deliverRunCallback(ctx, scope, "r1", nil)
	})

	// An in-cluster target is the normal case for a service-to-service caller, so
	// private addresses are reachable — the successful subtests above prove it,
	// since httptest serves on loopback. Link-local is still refused: that range
	// carries cloud instance metadata, which no caller-supplied URL may reach.
	t.Run("link-local is refused even though private is allowed", func(t *testing.T) {
		err := postCallback(ctx, tools.ConfiguredEndpointHTTPClient(),
			&runCallback{URL: "http://169.254.169.254/latest/meta-data/"}, []byte(`{}`))
		if err == nil {
			t.Fatal("the instance-metadata address must not be reachable")
		}
		if !strings.Contains(err.Error(), "link-local") {
			t.Fatalf("err = %v, want the dial guard's link-local refusal", err)
		}
	})
}
