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
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
	"time"

	"k8s.io/klog/v2"

	"github.com/faroshq/faros/pkg/server/proxy"
)

func hit(mw func(http.HandlerFunc) http.HandlerFunc, remoteAddr, xff, xri string) int {
	req := httptest.NewRequest(http.MethodPost, "/auth/refresh", nil)
	req.RemoteAddr = remoteAddr
	if xff != "" {
		req.Header.Set("X-Forwarded-For", xff)
	}
	if xri != "" {
		req.Header.Set("X-Real-IP", xri)
	}
	rr := httptest.NewRecorder()
	mw(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })(rr, req)
	return rr.Code
}

// TestMiddlewareIgnoresSpoofedHeadersFromUntrustedPeer: with no trusted
// proxies configured, rotating X-Forwarded-For / X-Real-IP from one peer
// still lands in that peer's single bucket.
func TestMiddlewareIgnoresSpoofedHeadersFromUntrustedPeer(t *testing.T) {
	rl := newRateLimiter(5, time.Minute, klog.Background())
	mw := rl.middleware

	for i := 1; i <= 5; i++ {
		if code := hit(mw, "203.0.113.7:1", fmt.Sprintf("10.0.0.%d", i), fmt.Sprintf("10.1.0.%d", i)); code != http.StatusNoContent {
			t.Fatalf("request %d: status = %d, want 204", i, code)
		}
	}
	if code := hit(mw, "203.0.113.7:1", "10.0.0.99", "10.1.0.99"); code != http.StatusTooManyRequests {
		t.Fatalf("over budget with spoofed headers: status = %d, want 429", code)
	}
	if n := rl.tracked(); n != 1 {
		t.Fatalf("tracked %d buckets, want 1 (the peer)", n)
	}
	// Trusting the proxy range changes the answer only when the peer is in it.
	rl.trustedProxies = mustPrefixes(t, "10.0.0.0/8")
	if code := hit(mw, "203.0.113.7:1", "10.0.0.99", ""); code != http.StatusTooManyRequests {
		t.Fatalf("untrusted peer after trust configured: status = %d, want 429", code)
	}
	if code := hit(mw, "10.0.0.5:1", "198.51.100.9", ""); code != http.StatusNoContent {
		t.Fatalf("trusted peer forwarding a new client: status = %d, want 204", code)
	}
}

func TestAuthRateLimiterStaysWithinCap(t *testing.T) {
	rl := newRateLimiter(10, time.Minute, klog.Background())
	for i := 0; i < 100_000; i++ {
		rl.isAllowed(fmt.Sprintf("key-%d", i))
	}
	if n := rl.tracked(); n > proxy.DefaultMaxLimiterEntries {
		t.Fatalf("tracked %d entries, cap is %d", n, proxy.DefaultMaxLimiterEntries)
	}
}

func TestAuthRateLimiterForgetsIdleEntries(t *testing.T) {
	rl := newRateLimiter(10, time.Minute, klog.Background())
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	rl.now = func() time.Time { return now }
	for i := 0; i < 50; i++ {
		rl.isAllowed(fmt.Sprintf("idle-%d", i))
	}
	now = now.Add(2 * time.Minute)
	rl.isAllowed("fresh")
	if n := rl.tracked(); n != 1 {
		t.Fatalf("after idle sweep tracked %d, want 1", n)
	}
}

func mustPrefixes(t *testing.T, cidrs ...string) []netip.Prefix {
	t.Helper()
	ps, err := proxy.ParseTrustedProxyCIDRs(cidrs)
	if err != nil {
		t.Fatal(err)
	}
	return ps
}
