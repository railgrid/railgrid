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
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"k8s.io/client-go/rest"
)

// newTokenLoginProxy builds a KCPProxy with one static token and nothing
// else: a wrong bearer is rejected by HandleTokenLogin before any kcp or
// User lookup, which is exactly the brute-force path under test.
func newTokenLoginProxy(t *testing.T) *KCPProxy {
	t.Helper()
	p, err := NewKCPProxy(&rest.Config{Host: "https://127.0.0.1:1"}, nil, nil, nil, []string{"the-real-token"}, "https://hub.example", false)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func tokenLogin(p *KCPProxy, remoteAddr, xff, bearer string) int {
	req := httptest.NewRequest(http.MethodPost, "/auth/token-login", nil)
	req.RemoteAddr = remoteAddr
	req.Header.Set("Authorization", "Bearer "+bearer)
	if xff != "" {
		req.Header.Set("X-Forwarded-For", xff)
	}
	rr := httptest.NewRecorder()
	p.HandleTokenLoginRateLimited(rr, req)
	return rr.Code
}

// TestTokenLoginBruteForceCannotRotateXFF is the regression for the
// client-supplied X-Forwarded-For hole: twenty guesses from one connection
// peer, each carrying a fresh X-Forwarded-For, must share a single bucket,
// so the eleventh is throttled. Before the fix every request minted its own
// bucket from the header's first hop and none were ever throttled.
func TestTokenLoginBruteForceCannotRotateXFF(t *testing.T) {
	p := newTokenLoginProxy(t)

	for i := 1; i <= 20; i++ {
		xff := fmt.Sprintf("10.%d.%d.%d", i, i, i)
		code := tokenLogin(p, "203.0.113.7:4242", xff, fmt.Sprintf("guess-%d", i))
		switch {
		case i <= defaultStaticTokenRateLimit && code != http.StatusUnauthorized:
			t.Fatalf("guess %d: status = %d, want 401 (wrong token, within budget)", i, code)
		case i > defaultStaticTokenRateLimit && code != http.StatusTooManyRequests:
			t.Fatalf("guess %d with X-Forwarded-For %s: status = %d, want 429 — spoofed header minted a fresh bucket", i, xff, code)
		}
	}

	// Another real peer is unaffected by the first one's exhausted bucket.
	if code := tokenLogin(p, "198.51.100.9:4242", "", "guess-x"); code != http.StatusUnauthorized {
		t.Fatalf("second peer: status = %d, want 401", code)
	}
}

// TestTokenLoginTrustedProxySeesClient is the counterpart: from a trusted
// proxy the LAST X-Forwarded-For hop is the client, so two clients behind
// the same proxy get separate buckets while a client-prepended hop is
// ignored.
func TestTokenLoginTrustedProxySeesClient(t *testing.T) {
	p := newTokenLoginProxy(t)
	p.SetTrustedProxies(mustPrefixes(t, "10.0.0.0/8"))

	for i := 1; i <= defaultStaticTokenRateLimit; i++ {
		// The client prepends a different fake hop every time; the proxy
		// appends the client's real address last.
		xff := fmt.Sprintf("1.1.1.%d, 198.51.100.9", i)
		if code := tokenLogin(p, "10.0.0.5:4242", xff, "guess"); code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: status = %d, want 401", i, code)
		}
	}
	if code := tokenLogin(p, "10.0.0.5:4242", "1.1.1.99, 198.51.100.9", "guess"); code != http.StatusTooManyRequests {
		t.Fatalf("over budget: status = %d, want 429", code)
	}
	if code := tokenLogin(p, "10.0.0.5:4242", "198.51.100.10", "guess"); code != http.StatusUnauthorized {
		t.Fatalf("different client behind the same proxy: status = %d, want 401", code)
	}
}

func TestRateLimiterStaysWithinCap(t *testing.T) {
	rl := newRateLimiter(time.Minute, 10)
	for i := 0; i < 100_000; i++ {
		rl.isAllowed(fmt.Sprintf("key-%d", i))
	}
	if n := rl.tracked(); n > DefaultMaxLimiterEntries {
		t.Fatalf("tracked %d entries, cap is %d", n, DefaultMaxLimiterEntries)
	}
}

func TestRateLimiterForgetsIdleEntries(t *testing.T) {
	rl := newRateLimiter(time.Second, 5)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	rl.now = func() time.Time { return now }

	for i := 0; i < 100; i++ {
		rl.isAllowed(fmt.Sprintf("idle-%d", i))
	}
	if n := rl.tracked(); n != 100 {
		t.Fatalf("tracked %d, want 100", n)
	}
	// Past a full refill (interval*burst) every bucket is forgotten on the
	// next insert.
	now = now.Add(6 * time.Second)
	rl.isAllowed("fresh")
	if n := rl.tracked(); n != 1 {
		t.Fatalf("after idle sweep tracked %d, want 1", n)
	}
}
