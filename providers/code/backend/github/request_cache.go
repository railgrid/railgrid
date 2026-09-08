/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package github

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	codev1alpha1 "github.com/faroshq/provider-code/apis/v1alpha1"
	"github.com/faroshq/provider-code/backend"
)

const (
	packageCacheTTL      = 2 * time.Minute
	githubRequestTimeout = 30 * time.Second
	maxRequestStates     = 64
	maxCachedResponses   = 256
	maxCachedBytes       = 1 << 20 // per credential/host; at most 64 MiB per backend
)

type cacheTTLKey struct{}
type cacheScopeKey struct{}

func packageCacheContext(ctx context.Context, conn *codev1alpha1.Connection) context.Context {
	// UID is globally unique in kcp. Include cluster/name for incomplete objects,
	// and keep all tenant identity out of printable cache keys.
	identity, _ := json.Marshal([]string{string(conn.UID), conn.Annotations["kcp.io/cluster"], conn.Namespace, conn.Name})
	ctx = context.WithValue(ctx, cacheScopeKey{}, sha256.Sum256(identity))
	return context.WithValue(ctx, cacheTTLKey{}, packageCacheTTL)
}

func credentialHash(token string) [32]byte { return sha256.Sum256([]byte(token)) }

type requestCache struct {
	mu     sync.Mutex
	now    func() time.Time
	states map[requestIdentity]*requestState
}
type requestIdentity struct {
	credential [32]byte
	host       string
}
type requestState struct {
	gate      chan struct{}
	lastUsed  time.Time // protected by requestCache.mu
	users     int       // holders and waiters; protected by requestCache.mu
	until     time.Time // protected by gate
	backoff   time.Duration
	listings  map[[32]byte]*listingFlight // protected by gate; pins state during refresh
	responses map[[32]byte]cachedResponse
	bytes     int
}
type cachedResponse struct {
	status  int
	header  http.Header
	body    []byte
	expires time.Time
}

func (b *Backend) requestCache() *requestCache {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.requests == nil {
		b.requests = &requestCache{now: time.Now, states: make(map[requestIdentity]*requestState)}
	}
	return b.requests
}

// acquire serializes requests for a credential/host, coalescing simultaneous
// cache misses and preventing a burst when a throttle expires. Waiting callers
// can cancel. At capacity only idle, unthrottled states may be evicted.
func (c *requestCache) acquire(ctx context.Context, key requestIdentity) (*requestState, error) {
	c.mu.Lock()
	now := c.now()
	var oldest *requestState
	var evict requestIdentity
	for k, s := range c.states {
		select {
		case <-s.gate:
			if s.users == 0 && len(s.listings) == 0 && now.Sub(s.lastUsed) >= time.Hour && !now.Before(s.until) {
				delete(c.states, k)
			} else if s.users == 0 && len(s.listings) == 0 && !now.Before(s.until) && (oldest == nil || s.lastUsed.Before(oldest.lastUsed)) {
				oldest = s
				evict = k
			}
			s.gate <- struct{}{}
		default:
		}
	}
	s := c.states[key]
	if s == nil {
		if len(c.states) >= maxRequestStates && oldest != nil {
			delete(c.states, evict)
		}
		if len(c.states) >= maxRequestStates {
			c.mu.Unlock()
			return nil, fmt.Errorf("github: request budget capacity reached; retry later")
		}
		s = &requestState{gate: make(chan struct{}, 1), responses: make(map[[32]byte]cachedResponse)}
		s.gate <- struct{}{}
		c.states[key] = s
	}
	// Reserve before releasing mu: a waiting caller must not lose its state to
	// idle eviction while another caller owns the gate.
	s.lastUsed = now
	s.users++
	c.mu.Unlock()
	select {
	case <-ctx.Done():
		c.mu.Lock()
		s.users--
		c.mu.Unlock()
		return nil, ctx.Err()
	case <-s.gate:
		return s, nil
	}
}

type sharedTransport struct {
	base       http.RoundTripper
	cache      *requestCache
	credential [32]byte
}

// release drops a reservation and releases its request gate.
func (c *requestCache) release(s *requestState) {
	c.mu.Lock()
	defer c.mu.Unlock()
	s.users--
	s.lastUsed = c.now()
	s.gate <- struct{}{}
}

func (t *sharedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	c := t.cache
	identity := requestIdentity{t.credential, req.URL.Scheme + "://" + req.URL.Host}
	s, err := c.acquire(req.Context(), identity)
	if err != nil {
		return nil, err
	}
	defer c.release(s)
	now := c.now()
	ttl, _ := req.Context().Value(cacheTTLKey{}).(time.Duration)
	scope, _ := req.Context().Value(cacheScopeKey{}).([32]byte)
	key := sha256.Sum256(append(scope[:], []byte(req.URL.String())...))
	s.expire(now)
	if req.Method == http.MethodGet && ttl > 0 {
		if v, ok := s.responses[key]; ok {
			return v.response(req), nil
		}
	}
	if now.Before(s.until) {
		return nil, &backend.RateLimitError{RetryAt: s.until}
	}
	resp, err := t.base.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	// Record header-based throttling immediately. Body failure or cancellation
	// must not discard a reset or Retry-After already received.
	observedAt := c.now()
	headerThrottle := s.observe(resp, nil, observedAt)
	// Inspect errors without unbounded buffering. Restore the stream for go-github.
	var body []byte
	if resp.StatusCode == 403 || resp.StatusCode == 429 || (ttl > 0 && req.Method == http.MethodGet && resp.StatusCode == 200) {
		body, err = io.ReadAll(io.LimitReader(resp.Body, maxCachedBytes+1))
		if err != nil {
			_ = resp.Body.Close()
			return nil, s.retryError(err, c.now())
		}
		resp.Body = &replayBody{Reader: io.MultiReader(bytes.NewReader(body), resp.Body), Closer: resp.Body}
	}
	throttled := headerThrottle
	if !headerThrottle {
		throttled = s.observe(resp, body, observedAt)
	}
	if throttled {
		_ = resp.Body.Close()
		return nil, &backend.RateLimitError{RetryAt: s.until, Err: fmt.Errorf("github: rate limited (HTTP %d)", resp.StatusCode)}
	}
	if resp.StatusCode == 200 && req.Method == http.MethodGet && ttl > 0 && len(body) <= maxCachedBytes {
		s.store(key, cachedResponse{resp.StatusCode, resp.Header.Clone(), body, c.now().Add(ttl)})
	}
	return resp, nil
}

type replayBody struct {
	io.Reader
	io.Closer
}

func (v cachedResponse) response(req *http.Request) *http.Response {
	h := v.header.Clone()
	// A cached response must not restore an obsolete rate budget in go-github.
	h.Del("X-RateLimit-Remaining")
	h.Del("X-RateLimit-Reset")
	return &http.Response{StatusCode: v.status, Header: h, Body: io.NopCloser(bytes.NewReader(v.body)), Request: req}
}

func (s *requestState) observe(resp *http.Response, body []byte, now time.Time) bool {
	until := time.Time{}
	if resp.Header.Get("X-RateLimit-Remaining") == "0" {
		if reset, err := strconv.ParseInt(resp.Header.Get("X-RateLimit-Reset"), 10, 64); err == nil {
			until = time.Unix(reset, 0).Add(time.Second)
		}
	}
	throttled := resp.StatusCode == 429 || (resp.StatusCode == 403 && (resp.Header.Get("Retry-After") != "" || resp.Header.Get("X-RateLimit-Remaining") == "0" || strings.Contains(strings.ToLower(string(body)), "rate limit") || strings.Contains(string(body), "secondary-rate-limits") || strings.Contains(string(body), "#abuse-rate-limits")))
	if throttled {
		if s.backoff == 0 {
			s.backoff = time.Minute
		} else {
			s.backoff = min(s.backoff*2, 15*time.Minute)
		}
		retry := now.Add(s.backoff)
		if raw := resp.Header.Get("Retry-After"); raw != "" {
			if seconds, err := strconv.ParseInt(raw, 10, 32); err == nil && seconds >= 0 {
				retry = now.Add(time.Duration(seconds) * time.Second)
			} else if date, err := http.ParseTime(raw); err == nil {
				retry = date
			}
		}
		if !retry.After(now) {
			retry = now.Add(time.Second)
		}
		if retry.After(until) {
			until = retry
		}
	} else if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		s.backoff = 0
	}
	if until.After(s.until) {
		s.until = until
	}
	return throttled
}

func (s *requestState) expire(now time.Time) {
	for k, v := range s.responses {
		if !now.Before(v.expires) {
			s.bytes -= len(v.body)
			delete(s.responses, k)
		}
	}
}

func (s *requestState) store(key [32]byte, value cachedResponse) {
	if len(value.body) > maxCachedBytes {
		return
	}
	if previous, ok := s.responses[key]; ok {
		s.bytes -= len(previous.body)
		delete(s.responses, key)
	}
	if len(s.responses) >= maxCachedResponses || s.bytes+len(value.body) > maxCachedBytes {
		s.responses = make(map[[32]byte]cachedResponse)
		s.bytes = 0
	}
	s.responses[key] = value
	s.bytes += len(value.body)
}

// Decode failures and go-github's own preflight rate checks can occur after
// RoundTrip. Preserve the shared deadline when those fail a paginated refresh.
func (s *requestState) retryError(err error, now time.Time) error {
	var limited *backend.RateLimitError
	if err != nil && now.Before(s.until) && !errors.As(err, &limited) {
		return &backend.RateLimitError{RetryAt: s.until, Err: err}
	}
	return err
}
