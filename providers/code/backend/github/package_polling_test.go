/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	codev1alpha1 "github.com/faroshq/provider-code/apis/v1alpha1"
	"github.com/faroshq/provider-code/backend"
)

type pollingClock struct{ seconds atomic.Int64 }

func (c *pollingClock) now() time.Time          { return time.Unix(c.seconds.Load(), 0) }
func (c *pollingClock) advance(d time.Duration) { c.seconds.Add(int64(d / time.Second)) }
func pollingBackend() (*Backend, *pollingClock) {
	c := &pollingClock{}
	c.seconds.Store(time.Now().Unix())
	b := New()
	b.requestCache().now = c.now
	return b, c
}
func pollingConnection(server string) *codev1alpha1.Connection {
	return &codev1alpha1.Connection{ObjectMeta: metav1.ObjectMeta{Name: "github", UID: "tenant-connection"}, Spec: codev1alpha1.ConnectionSpec{BaseURL: server, Owner: "alice"}}
}
func pollingRepo(name string) *codev1alpha1.Repository {
	return &codev1alpha1.Repository{Spec: codev1alpha1.RepositorySpec{Name: name}}
}

// This measures the actual backend HTTP traffic for the five-repository case,
// without contacting GitHub. Old code makes 60 listing calls per crawl here.
func TestPackagePollingRequestVolumeAndIsolation(t *testing.T) {
	var requests, owners, orgProbes atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if strings.Contains(r.URL.Path, "/orgs/") {
			orgProbes.Add(1)
			http.NotFound(w, r)
			return
		}
		if r.URL.Path == "/api/v3/users/alice" {
			owners.Add(1)
			_, _ = fmt.Fprint(w, `{"type":"User"}`)
			return
		}
		if r.URL.Path != "/api/v3/users/alice/packages" {
			t.Errorf("unexpected path %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		if r.URL.Query().Get("package_type") != "npm" {
			_, _ = fmt.Fprint(w, `[]`)
			return
		}
		var out []map[string]any
		for i := 0; i < 5; i++ {
			out = append(out, map[string]any{"name": r.Header.Get("Authorization"), "package_type": "npm", "repository": map[string]string{"name": fmt.Sprintf("repo%d", i)}})
		}
		_ = json.NewEncoder(w).Encode(out)
	}))
	defer srv.Close()
	b, clock := pollingBackend()
	conn := pollingConnection(srv.URL)
	cred := backend.Credential{Token: "first-token"}
	run := func() {
		var wg sync.WaitGroup
		for i := 0; i < 5; i++ {
			wg.Go(func() {
				got, err := b.ListPackages(context.Background(), conn, cred, pollingRepo(fmt.Sprintf("repo%d", i)))
				if err != nil || len(got) != 1 {
					t.Errorf("packages=%v err=%v", got, err)
					return
				}
				if !strings.Contains(got[0].Name, cred.Token) {
					t.Errorf("cross-credential data: %v", got)
				}
			})
		}
		wg.Wait()
	}
	run()
	run()
	if requests.Load() != 7 || owners.Load() != 1 || orgProbes.Load() != 0 {
		t.Fatalf("cold+warm requests=%d owners=%d org probes=%d", requests.Load(), owners.Load(), orgProbes.Load())
	}
	clock.advance(packageCacheTTL)
	run()
	if requests.Load() != 13 || owners.Load() != 1 {
		t.Fatalf("refresh requests=%d owners=%d", requests.Load(), owners.Load())
	}
	cred.Token = "rotated-token"
	run()
	if requests.Load() != 20 {
		t.Fatalf("rotation requests=%d", requests.Load())
	}
	conn.UID = types.UID("other-tenant")
	run()
	if requests.Load() != 27 {
		t.Fatalf("tenant separation requests=%d", requests.Load())
	}
	clock.advance(time.Hour)
	run()
	if requests.Load() != 34 || owners.Load() != 4 {
		t.Fatalf("expiry requests=%d owners=%d", requests.Load(), owners.Load())
	}
	t.Log("five repos: old 60 listing requests/crawl; new cold 7 incl owner lookup, warm 0, refresh 6; no org probes")
}

func TestPackagePollingPaginationVersionsAndFailure(t *testing.T) {
	for _, accountType := range []string{"User", "Organization"} {
		t.Run(accountType, func(t *testing.T) {
			var fail atomic.Bool
			var versionCalls atomic.Int64
			var revision atomic.Int64
			var srv *httptest.Server
			prefix := "/users/alice"
			if accountType == "Organization" {
				prefix = "/orgs/alice"
			}
			srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/v3/users/alice" {
					_, _ = fmt.Fprintf(w, `{"type":%q}`, accountType)
					return
				}
				if strings.Contains(r.URL.Path, "/versions") {
					versionCalls.Add(1)
					if !strings.Contains(r.URL.EscapedPath(), "image%2Fpart") {
						t.Errorf("package path not escaped: %s", r.URL.EscapedPath())
					}
					if fail.Load() {
						http.Error(w, "temporary failure", http.StatusServiceUnavailable)
						return
					}
					if r.URL.Query().Get("page") == "2" {
						_, _ = fmt.Fprint(w, `[{"name":"sha256:older","metadata":{"container":{"tags":["old"]}}}]`)
						return
					}
					w.Header().Set("Link", fmt.Sprintf(`<%s%s?page=2&per_page=100>; rel="next"`, srv.URL, r.URL.EscapedPath()))
					_, _ = fmt.Fprintf(w, `[{"name":"sha256:new%d","created_at":"2026-09-08T19:00:00Z","metadata":{"container":{"tags":["latest","build-123"]}}}]`, revision.Load())
					return
				}
				if r.URL.Path != "/api/v3"+prefix+"/packages" {
					t.Errorf("wrong owner route: %s", r.URL.Path)
					http.NotFound(w, r)
					return
				}
				pt := r.URL.Query().Get("package_type")
				if r.URL.Query().Get("page") != "2" {
					w.Header().Set("Link", fmt.Sprintf(`<%s%s?package_type=%s&page=2&per_page=100>; rel="next"`, srv.URL, r.URL.Path, pt))
					_, _ = fmt.Fprint(w, `[{"name":"unrelated","repository":{"name":"elsewhere"}}]`)
					return
				}
				_, _ = fmt.Fprintf(w, `[{"name":"image/part","package_type":%q,"version_count":0,"repository":{"name":"DEMO"}}]`, pt)
			}))
			defer srv.Close()
			b, clock := pollingBackend()
			conn := pollingConnection(srv.URL)
			cred := backend.Credential{Token: "token"}
			got, err := b.ListPackages(context.Background(), conn, cred, pollingRepo("demo"))
			if err != nil || len(got) != 6 {
				t.Fatalf("packages=%v err=%v", got, err)
			}
			for _, p := range got {
				if p.Type != "container" && p.Type != "docker" {
					continue
				}
				if p.ImageRepository != "ghcr.io/alice/image/part" || p.VersionCount != 2 || len(p.Versions) != 2 || p.Versions[0].Digest != "sha256:new0" || strings.Join(p.Versions[0].Tags, ",") != "latest,build-123" || p.Versions[0].CreatedAt != "2026-09-08T19:00:00Z" {
					t.Fatalf("incorrect version result: %+v", p)
				}
			}
			// Mutating a decoded result must not mutate shared cached data.
			got[0].Versions[0].Tags[0] = "mutated"
			again, err := b.ListPackages(context.Background(), conn, cred, pollingRepo("demo"))
			if err != nil || again[0].Versions[0].Tags[0] != "latest" || versionCalls.Load() != 4 {
				t.Fatalf("cached versions=%v err=%v requests=%d", again, err, versionCalls.Load())
			}
			clock.advance(packageCacheTTL)
			fail.Store(true)
			if partial, err := b.ListPackages(context.Background(), conn, cred, pollingRepo("demo")); err == nil || partial != nil {
				t.Fatalf("version failure must fail whole crawl: %v %v", partial, err)
			}
			fail.Store(false)
			revision.Store(1)
			refreshed, err := b.ListPackages(context.Background(), conn, cred, pollingRepo("demo"))
			if err != nil || refreshed[0].Versions[0].Digest != "sha256:new1" {
				t.Fatalf("new artifact not discovered after expiry/recovery: %v %v", refreshed, err)
			}
		})
	}
}

func TestPackageThrottleSharedWithWorkflowAndRecovery(t *testing.T) {
	for _, kind := range []string{"primary", "secondary-header", "secondary-date", "secondary-fallback", "success-exhausted"} {
		t.Run(kind, func(t *testing.T) {
			b, clock := pollingBackend()
			var calls atomic.Int64
			var throttle atomic.Bool
			throttle.Store(true)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if throttle.Load() {
					switch kind {
					case "primary", "success-exhausted":
						w.Header().Set("X-RateLimit-Remaining", "0")
						w.Header().Set("X-RateLimit-Reset", fmt.Sprint(clock.now().Add(5*time.Minute).Unix()))
					case "secondary-header":
						w.Header().Set("Retry-After", "300")
					case "secondary-date":
						w.Header().Set("Retry-After", clock.now().Add(5*time.Minute).UTC().Format(http.TimeFormat))
					}
					if kind == "success-exhausted" {
						_, _ = fmt.Fprint(w, `{"type":"User"}`)
						return
					}
					status := 403
					if kind == "secondary-header" {
						status = 429
					}
					w.WriteHeader(status)
					_, _ = fmt.Fprint(w, `{"message":"secondary rate limit","documentation_url":"https://docs.github.com/rest/using-the-rest-api/rate-limits-for-the-rest-api#secondary-rate-limits"}`)
					return
				}
				_, _ = fmt.Fprint(w, `{"total_count":0,"workflow_runs":[]}`)
			}))
			defer srv.Close()
			conn := pollingConnection(srv.URL)
			cred := backend.Credential{Token: "token"}
			wait := 5 * time.Minute
			if kind == "primary" || kind == "success-exhausted" {
				wait += time.Second
			}
			if kind == "secondary-fallback" {
				wait = time.Minute
			}
			retryAt := clock.now().Add(wait)
			_, err := b.ListPackages(context.Background(), conn, cred, pollingRepo("demo"))
			assertRetryAt(t, err, retryAt)
			query := backend.WorkflowRunQuery{WorkflowFileName: "build.yml"}
			for i := 0; i < 5; i++ {
				_, err := b.LatestWorkflowRun(context.Background(), conn, cred, pollingRepo("demo"), query)
				assertRetryAt(t, err, retryAt)
			}
			if calls.Load() != 1 {
				t.Fatalf("requests while throttled=%d", calls.Load())
			}
			clock.advance(30 * time.Second)
			if _, err := b.LatestWorkflowRun(context.Background(), conn, cred, pollingRepo("demo"), query); err == nil {
				t.Fatal("early retry")
			}
			if calls.Load() != 1 {
				t.Fatal("early network request")
			}
			clock.advance(wait - 31*time.Second)
			if _, err := b.LatestWorkflowRun(context.Background(), conn, cred, pollingRepo("demo"), query); err == nil || calls.Load() != 1 {
				t.Fatal("request before exact throttle deadline")
			}
			clock.advance(time.Second)
			throttle.Store(false)
			if _, err := b.LatestWorkflowRun(context.Background(), conn, cred, pollingRepo("demo"), query); err != nil {
				t.Fatal(err)
			}
			if calls.Load() != 2 {
				t.Fatalf("recovery requests=%d", calls.Load())
			}
		})
	}
}

func TestPollingCacheHostOwnerAndBounds(t *testing.T) {
	b, clock := pollingBackend()
	var calls atomic.Int64
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if !strings.Contains(r.URL.Path, "/packages") {
			_, _ = fmt.Fprint(w, `{"type":"User"}`)
		} else {
			_, _ = fmt.Fprint(w, `[]`)
		}
	})
	a := httptest.NewServer(handler)
	defer a.Close()
	other := httptest.NewServer(handler)
	defer other.Close()
	conn := pollingConnection(a.URL)
	cred := backend.Credential{Token: "token"}
	for _, host := range []string{a.URL, other.URL} {
		conn.Spec.BaseURL = host
		for _, owner := range []string{"alice", "bob"} {
			conn.Spec.Owner = owner
			if _, err := b.ListPackages(context.Background(), conn, cred, pollingRepo("demo")); err != nil {
				t.Fatal(err)
			}
		}
	}
	if calls.Load() != 28 {
		t.Fatalf("host/owner isolation calls=%d", calls.Load())
	}
	// Expired credentials can be reclaimed, but active throttle state must never
	// be evicted to make room for a different token.
	cache := b.requestCache()
	cache.mu.Lock()
	for _, s := range cache.states {
		s.until = clock.now().Add(3 * time.Hour)
	}
	cache.mu.Unlock()
	for i := len(cache.states); i < maxRequestStates; i++ {
		s, err := cache.acquire(context.Background(), requestIdentity{credential: credentialHash(fmt.Sprint(i)), host: "test"})
		if err != nil {
			t.Fatal(err)
		}
		cache.mu.Lock()
		s.users--
		s.gate <- struct{}{}
		cache.mu.Unlock()
	}
	replacement, err := cache.acquire(context.Background(), requestIdentity{host: "replacement"})
	if err != nil {
		t.Fatal(err)
	}
	cache.release(replacement)
	if len(cache.states) != maxRequestStates {
		t.Fatal("unbounded credential states")
	}
	// Protect all states temporarily to exercise fail-closed admission.
	for _, state := range cache.states {
		if state.until.IsZero() {
			state.until = clock.now().Add(time.Hour)
		}
	}
	if _, err := cache.acquire(context.Background(), requestIdentity{host: "overflow"}); err == nil {
		t.Fatal("evicted active throttle")
	}

	clock.advance(2 * time.Hour)
	s, err := cache.acquire(context.Background(), requestIdentity{host: "after-expiry"})
	if err != nil {
		t.Fatal(err)
	}
	cache.mu.Lock()
	s.users--
	s.gate <- struct{}{}
	cache.mu.Unlock()
	if len(cache.states) != 3 {
		t.Fatalf("idle cleanup lost throttle state or kept expired state: %d", len(cache.states))
	}
}

func TestPollingBackoffAndCancellation(t *testing.T) {
	b, clock := pollingBackend()
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(403)
		_, _ = fmt.Fprint(w, `{"message":"secondary rate limit"}`)
	}))
	defer srv.Close()
	conn := pollingConnection(srv.URL)
	cred := backend.Credential{Token: "token"}
	query := backend.WorkflowRunQuery{WorkflowFileName: "build.yml"}
	run := func() {
		if _, err := b.LatestWorkflowRun(context.Background(), conn, cred, pollingRepo("demo"), query); err == nil {
			t.Fatal("expected throttle")
		}
	}
	run()
	clock.advance(time.Minute)
	run() // second failure doubles the fallback delay
	clock.advance(time.Minute)
	run()
	if calls.Load() != 2 {
		t.Fatalf("backoff failed: %d", calls.Load())
	}
	clock.advance(time.Minute)
	run()
	if calls.Load() != 3 {
		t.Fatalf("backoff recovery probe missing: %d", calls.Load())
	}
	cache := b.requestCache()
	key := requestIdentity{host: "cancel"}
	s, err := cache.acquire(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := cache.acquire(ctx, key); err != context.Canceled {
		t.Fatalf("cancellation: %v", err)
	}
	cache.mu.Lock()
	s.users--
	s.gate <- struct{}{}
	cache.mu.Unlock()
	if s.users != 0 {
		t.Fatal("cancelled waiter leaked state reservation")
	}
}

func TestPollingResponseCacheBounds(t *testing.T) {
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		size := 1
		if r.URL.Path == "/large" {
			size = maxCachedBytes + 10
		}
		_, _ = io.WriteString(w, strings.Repeat("x", size))
	}))
	defer srv.Close()
	b, _ := pollingBackend()
	cache := b.requestCache()
	client := &http.Client{Transport: &sharedTransport{base: http.DefaultTransport, cache: cache, credential: credentialHash("token")}}
	ctx := packageCacheContext(context.Background(), pollingConnection(srv.URL))
	read := func(path string) int64 {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		n, err := io.Copy(io.Discard, resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		if err := resp.Body.Close(); err != nil {
			t.Fatal(err)
		}
		return n
	}
	for i := 0; i < 2; i++ {
		if n := read("/large"); n != maxCachedBytes+10 {
			t.Fatalf("oversize stream truncated: %d", n)
		}
	}
	if calls.Load() != 2 {
		t.Fatal("oversized response cached")
	}
	for i := 0; i < maxCachedResponses+1; i++ {
		read(fmt.Sprintf("/page%d", i))
	}
	if calls.Load() != maxCachedResponses+3 {
		t.Fatal("unexpected response count")
	}
	for _, s := range cache.states {
		if len(s.responses) > maxCachedResponses || s.bytes > maxCachedBytes {
			t.Fatal("cache bounds exceeded")
		}
	}
	before := calls.Load()
	read(fmt.Sprintf("/page%d", maxCachedResponses))
	if calls.Load() != before {
		t.Fatal("latest bounded response not cached")
	}
	read("/page0")
	if calls.Load() != before+1 {
		t.Fatal("old response not evicted")
	}
}

func TestPollingPermissionErrorDoesNotThrottle(t *testing.T) {
	s := &requestState{}
	now := time.Now()
	s.observe(&http.Response{StatusCode: 403, Header: http.Header{}}, []byte(`{"message":"Resource not accessible by integration"}`), now)
	if !s.until.IsZero() {
		t.Fatal("permission error treated as throttling")
	}
}

func assertRetryAt(t *testing.T, err error, want time.Time) {
	t.Helper()
	var limited *backend.RateLimitError
	if !errors.As(err, &limited) || !limited.RetryAt.Equal(want) {
		t.Fatalf("error=%v, want typed retry at %s", err, want)
	}
}
