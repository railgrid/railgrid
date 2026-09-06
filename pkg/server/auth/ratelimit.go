// Copyright 2026 The Faros Authors
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

package auth

import (
	"net/http"
	"net/netip"
	"sync"
	"time"

	"golang.org/x/time/rate"
	"k8s.io/klog/v2"

	"github.com/faroshq/faros/pkg/server/proxy"
)

// rateLimiter implements a per-IP rate limiter for authentication endpoints.
// It uses a sliding window rate limiter to prevent brute force attacks.
//
// The budget is per hub replica, so a hub scaled to N replicas admits up to N
// times the configured burst for a client the load balancer spreads across
// pods. That is a deliberate trade: a shared counter would put a control-plane
// round trip on every unauthenticated request, and this limiter is defence in
// depth behind bearer-token and OIDC verification, not the primary control.
// Deployments that need a hard global bound should enforce it at the ingress.
//
// Requests are keyed on proxy.ClientIP: the connection peer, or — only when
// that peer is one of trustedProxies — the client the proxy vouches for in
// X-Forwarded-For. Buckets idle for a full window are forgotten and the
// store is capped at proxy.DefaultMaxLimiterEntries, so distinct peers
// cannot grow memory without bound.
type rateLimiter struct {
	limiters *proxy.BoundedKeys[*rate.Limiter]
	mu       sync.Mutex
	// bursts is the maximum burst size
	bursts int
	// burstDuration is the time window for the burst rate
	burstDuration time.Duration
	// trustedProxies is the prefix list handed to proxy.ClientIP.
	trustedProxies []netip.Prefix
	// logger for debugging
	logger klog.Logger
	now    func() time.Time
}

// newRateLimiter creates a new rate limiter with the given configuration.
// rateLimit: number of requests per burstDuration (e.g., 10 requests per minute)
// burstDuration: the time window for rate limiting
func newRateLimiter(limit int, burstDuration time.Duration, logger klog.Logger) *rateLimiter {
	return &rateLimiter{
		limiters:      proxy.NewBoundedKeys[*rate.Limiter](proxy.DefaultMaxLimiterEntries, burstDuration),
		bursts:        limit,
		burstDuration: burstDuration,
		logger:        logger,
		now:           time.Now,
	}
}

// getLimiter returns a rate limiter for the given client IP.
// If a limiter doesn't exist for the IP, it creates a new one.
func (rl *rateLimiter) getLimiter(clientIP string) *rate.Limiter {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := rl.now()
	if limiter, exists := rl.limiters.Get(clientIP, now); exists {
		return limiter
	}

	// Create a new limiter: limit requests per burstDuration
	// rate.Every(burstDuration/limit) gives us the correct interval between requests
	interval := rl.burstDuration / time.Duration(rl.bursts)
	limiter := rate.NewLimiter(rate.Every(interval), rl.bursts)
	rl.limiters.Put(clientIP, limiter, now)

	return limiter
}

// isAllowed checks if a request from the given client IP is allowed.
// Returns true if the request can proceed, false if it should be rate limited.
func (rl *rateLimiter) isAllowed(clientIP string) bool {
	limiter := rl.getLimiter(clientIP)
	return limiter.Allow()
}

// tracked is the number of addresses currently holding a bucket.
func (rl *rateLimiter) tracked() int {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	return rl.limiters.Len()
}

// middleware returns an HTTP middleware that applies rate limiting.
// Requests that exceed the rate limit receive a 429 Too Many Requests response.
func (rl *rateLimiter) middleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		clientIP := proxy.ClientIP(r, rl.trustedProxies)

		if !rl.isAllowed(clientIP) {
			rl.logger.V(2).Info("rate limit exceeded", "clientIP", clientIP, "path", r.URL.Path)
			w.Header().Set("Retry-After", "60")
			http.Error(w, "rate limit exceeded - too many requests", http.StatusTooManyRequests)
			return
		}

		next(w, r)
	}
}
