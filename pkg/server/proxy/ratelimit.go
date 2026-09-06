/*
Copyright 2026 The Faros Authors.

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

package proxy

import (
	"net/http"
	"net/netip"
	"sync"
	"time"
)

// rateLimiter is the per-client-address token bucket behind the token-login
// endpoint and the aggregate MCP bearer check. Each address starts with
// burstSize tokens and earns one back per interval, so an address that keeps
// failing is held to one attempt per interval once its burst is spent.
//
// Buckets live in a BoundedKeys store: an address idle for long enough to
// have refilled completely (interval*burstSize) is forgotten, and past
// DefaultMaxLimiterEntries the least recently seen address is dropped, so a
// flood of distinct peers cannot grow memory without bound.
type rateLimiter struct {
	mu        sync.Mutex
	visitors  *BoundedKeys[*visitor]
	interval  time.Duration
	burstSize int
	// trustedProxies is the prefix list handed to ClientIP; empty means only
	// the connection peer is ever consulted.
	trustedProxies []netip.Prefix
	now            func() time.Time
}

// visitor tracks rate limiting state for a single address.
type visitor struct {
	tokens    int
	lastVisit time.Time
}

// newRateLimiter creates a new in-memory rate limiter.
func newRateLimiter(interval time.Duration, burstSize int) *rateLimiter {
	return &rateLimiter{
		visitors:  NewBoundedKeys[*visitor](DefaultMaxLimiterEntries, interval*time.Duration(burstSize)),
		interval:  interval,
		burstSize: burstSize,
		now:       time.Now,
	}
}

// isAllowed checks if a request from the given client address is allowed.
func (rl *rateLimiter) isAllowed(clientIP string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := rl.now()
	v, exists := rl.visitors.Get(clientIP, now)
	if !exists {
		// First request from this address.
		rl.visitors.Put(clientIP, &visitor{tokens: rl.burstSize - 1, lastVisit: now}, now)
		return true
	}

	// Refill tokens based on time elapsed.
	elapsed := now.Sub(v.lastVisit)
	refill := int(elapsed / rl.interval)
	if refill > 0 {
		v.tokens = min(v.tokens+refill, rl.burstSize)
		v.lastVisit = now
	}

	if v.tokens <= 0 {
		return false
	}

	v.tokens--
	return true
}

// tracked is the number of addresses currently holding a bucket.
func (rl *rateLimiter) tracked() int {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	return rl.visitors.Len()
}

// middleware wraps an http.HandlerFunc with rate limiting keyed on ClientIP.
func (rl *rateLimiter) middleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		clientIP := ClientIP(r, rl.trustedProxies)
		if !rl.isAllowed(clientIP) {
			w.Header().Set("Retry-After", "60")
			http.Error(w, "rate limit exceeded - too many requests", http.StatusTooManyRequests)
			return
		}
		next(w, r)
	}
}

// IPRateLimiter is the per-client-address token bucket the hub uses on its
// pre-authentication endpoints (token-login, the aggregate MCP bearer check).
// It is the same limiter HandleTokenLoginRateLimited applies, exposed so other
// packages can share one implementation without importing its internals.
type IPRateLimiter struct {
	rl *rateLimiter
}

// NewIPRateLimiter returns a limiter that admits burstSize requests per client
// address immediately and refills one token per interval up to burstSize.
func NewIPRateLimiter(interval time.Duration, burstSize int) *IPRateLimiter {
	return &IPRateLimiter{rl: newRateLimiter(interval, burstSize)}
}

// Allow reports whether a request from clientIP may proceed, consuming one
// token when it may.
func (l *IPRateLimiter) Allow(clientIP string) bool {
	return l.rl.isAllowed(clientIP)
}
