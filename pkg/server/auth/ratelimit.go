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
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"
	"k8s.io/klog/v2"
)

// rateLimiter implements a per-IP rate limiter for authentication endpoints.
// It uses a sliding window rate limiter to prevent brute force attacks.
type rateLimiter struct {
	limiters map[string]*rate.Limiter
	mu       sync.RWMutex
	// bursts is the maximum burst size
	bursts int
	// burstDuration is the time window for the burst rate
	burstDuration time.Duration
	// logger for debugging
	logger klog.Logger
}

// newRateLimiter creates a new rate limiter with the given configuration.
// rateLimit: number of requests per burstDuration (e.g., 10 requests per minute)
// burstDuration: the time window for rate limiting
func newRateLimiter(limit int, burstDuration time.Duration, logger klog.Logger) *rateLimiter {
	return &rateLimiter{
		limiters:      make(map[string]*rate.Limiter),
		bursts:        limit,
		burstDuration: burstDuration,
		logger:        logger,
	}
}

// getLimiter returns a rate limiter for the given client IP.
// If a limiter doesn't exist for the IP, it creates a new one.
func (rl *rateLimiter) getLimiter(clientIP string) *rate.Limiter {
	rl.mu.RLock()
	limiter, exists := rl.limiters[clientIP]
	rl.mu.RUnlock()

	if exists {
		return limiter
	}

	rl.mu.Lock()
	defer rl.mu.Unlock()

	// Double-check after acquiring write lock
	if limiter, exists := rl.limiters[clientIP]; exists {
		return limiter
	}

	// Create a new limiter: limit requests per burstDuration
	// rate.Every(burstDuration/limit) gives us the correct interval between requests
	interval := rl.burstDuration / time.Duration(rl.bursts)
	limiter = rate.NewLimiter(rate.Every(interval), rl.bursts)
	rl.limiters[clientIP] = limiter

	return limiter
}

// isAllowed checks if a request from the given client IP is allowed.
// Returns true if the request can proceed, false if it should be rate limited.
func (rl *rateLimiter) isAllowed(clientIP string) bool {
	limiter := rl.getLimiter(clientIP)
	return limiter.Allow()
}

// middleware returns an HTTP middleware that applies rate limiting.
// Requests that exceed the rate limit receive a 429 Too Many Requests response.
func (rl *rateLimiter) middleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		clientIP := getClientIP(r)

		if !rl.isAllowed(clientIP) {
			rl.logger.V(2).Info("rate limit exceeded", "clientIP", clientIP, "path", r.URL.Path)
			w.Header().Set("Retry-After", "60")
			http.Error(w, "rate limit exceeded - too many requests", http.StatusTooManyRequests)
			return
		}

		next(w, r)
	}
}

// getClientIP extracts the client IP from the request.
// It first checks the X-Forwarded-For header (for proxies), then X-Real-IP,
// then falls back to the remote address.
func getClientIP(r *http.Request) string {
	// Check X-Forwarded-For header (common for proxied requests)
	xff := r.Header.Get("X-Forwarded-For")
	if xff != "" {
		// X-Forwarded-For can contain multiple IPs, take the first one (client)
		ips := splitAndTrim(xff, ",")
		if len(ips) > 0 {
			return ips[0]
		}
	}

	// Check X-Real-IP header (alternative proxy header)
	xri := r.Header.Get("X-Real-IP")
	if xri != "" {
		return xri
	}

	// Fall back to RemoteAddr
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr // Return as-is if parsing fails
	}
	return host
}

// splitAndTrim splits a string by comma and trims whitespace from each part.
func splitAndTrim(s, delimiter string) []string {
	parts := strings.Split(s, delimiter)
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}
