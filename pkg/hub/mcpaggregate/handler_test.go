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

package mcpaggregate

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const testMCPPath = "/some-cluster/apis/faros.sh/v1alpha1/mcpservers/default/mcp"

// allowAll is the verifier the federation-focused tests use so they exercise
// the aggregate itself rather than bearer verification.
var allowAll = BearerVerifierFunc(func(*http.Request, string, string, string) error { return nil })

// countingVerifier records every verification attempt and answers with a
// fixed error (nil = allow).
type countingVerifier struct {
	calls atomic.Int32
	err   error
	last  struct{ token, cluster, name string }
}

func (c *countingVerifier) Verify(_ *http.Request, token, cluster, name string) error {
	c.calls.Add(1)
	c.last.token, c.last.cluster, c.last.name = token, cluster, name
	return c.err
}

// countingProvider is a fake upstream provider /mcp endpoint that counts how
// many federated calls reach it.
func countingProvider(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"tools":[]}}`))
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

// toolsList POSTs tools/list with the given bearer and returns the status.
func toolsList(h http.Handler, bearer string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, testMCPPath, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`))
	req.Header.Set("Authorization", "Bearer "+bearer)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.RemoteAddr = "203.0.113.7:4242"
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

// TestGarbageBearerNeverReachesProviders is the finding-4 regression: a
// bearer the verifier rejects gets 401 and no provider is contacted.
func TestGarbageBearerNeverReachesProviders(t *testing.T) {
	provider, upstream := countingProvider(t)
	v := &countingVerifier{err: ErrUnauthenticated}
	h := New(Options{
		Providers: func(context.Context) []ProviderTarget {
			return []ProviderTarget{{Name: "infra", MCPURL: provider.URL}}
		},
		Verifier: v,
	})

	rr := toolsList(h, "garbage")
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("garbage bearer: status = %d, want 401", rr.Code)
	}
	if got := v.calls.Load(); got != 1 {
		t.Fatalf("verifier calls = %d, want 1", got)
	}
	if v.last.token != "garbage" || v.last.cluster != "some-cluster" || v.last.name != "default" {
		t.Fatalf("verifier saw %+v, want (garbage, some-cluster, default)", v.last)
	}
	if got := upstream.Load(); got != 0 {
		t.Fatalf("upstream provider received %d calls for a rejected bearer, want 0", got)
	}
}

// TestValidTokenForOtherClusterForbidden: a real credential that does not
// belong to the addressed tenant is 403 and never federated.
func TestValidTokenForOtherClusterForbidden(t *testing.T) {
	provider, upstream := countingProvider(t)
	h := New(Options{
		Providers: func(context.Context) []ProviderTarget {
			return []ProviderTarget{{Name: "infra", MCPURL: provider.URL}}
		},
		Verifier: &countingVerifier{err: ErrForbidden},
	})

	if rr := toolsList(h, "other-tenant-token"); rr.Code != http.StatusForbidden {
		t.Fatalf("foreign bearer: status = %d, want 403", rr.Code)
	}
	if got := upstream.Load(); got != 0 {
		t.Fatalf("upstream provider received %d calls for a forbidden bearer, want 0", got)
	}
}

// TestVerifierOutageIsNotBypassed: an infrastructure error from the verifier
// fails closed with 503 rather than forwarding.
func TestVerifierOutageIsNotBypassed(t *testing.T) {
	provider, upstream := countingProvider(t)
	h := New(Options{
		Providers: func(context.Context) []ProviderTarget {
			return []ProviderTarget{{Name: "infra", MCPURL: provider.URL}}
		},
		Verifier: &countingVerifier{err: context.DeadlineExceeded},
	})
	if rr := toolsList(h, "t"); rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("verifier error: status = %d, want 503", rr.Code)
	}
	if got := upstream.Load(); got != 0 {
		t.Fatalf("upstream provider received %d calls during verifier outage, want 0", got)
	}

	// No verifier at all is the same: fail closed.
	h = New(Options{Providers: func(context.Context) []ProviderTarget { return nil }})
	if rr := toolsList(h, "t"); rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("nil verifier: status = %d, want 503", rr.Code)
	}
}

// TestValidTokenProceedsAndIsCached: a verified bearer federates, and the
// second request within the TTL reuses the verification instead of asking
// the verifier (and thus kcp) again. A different bearer is verified afresh.
func TestValidTokenProceedsAndIsCached(t *testing.T) {
	provider, upstream := countingProvider(t)
	v := &countingVerifier{}
	h := New(Options{
		Providers: func(context.Context) []ProviderTarget {
			return []ProviderTarget{{Name: "infra", MCPURL: provider.URL}}
		},
		Verifier: v,
	})

	for i := 0; i < 2; i++ {
		if rr := toolsList(h, "sa-token"); rr.Code != http.StatusOK {
			t.Fatalf("request %d: status = %d, want 200 (body %s)", i, rr.Code, rr.Body.String())
		}
	}
	if got := v.calls.Load(); got != 1 {
		t.Fatalf("verifier calls after two requests = %d, want 1 (cache hit)", got)
	}
	if got := upstream.Load(); got == 0 {
		t.Fatal("verified bearer was never federated to the provider")
	}
	if rr := toolsList(h, "another-sa-token"); rr.Code != http.StatusOK {
		t.Fatalf("second bearer: status = %d, want 200", rr.Code)
	}
	if got := v.calls.Load(); got != 2 {
		t.Fatalf("verifier calls after a new bearer = %d, want 2", got)
	}
}

// TestVerificationCacheExpires: once the TTL lapses the bearer is re-verified.
func TestVerificationCacheExpires(t *testing.T) {
	v := &countingVerifier{}
	h := New(Options{
		Providers:      func(context.Context) []ProviderTarget { return nil },
		Verifier:       v,
		VerifyCacheTTL: time.Nanosecond,
	})
	for i := 0; i < 2; i++ {
		if rr := toolsList(h, "sa-token"); rr.Code != http.StatusOK {
			t.Fatalf("request %d: status = %d, want 200", i, rr.Code)
		}
	}
	if got := v.calls.Load(); got != 2 {
		t.Fatalf("verifier calls with expired cache = %d, want 2", got)
	}
}

// fixedLimiter admits the first n attempts from any address.
type fixedLimiter struct {
	n    int
	seen []string
}

func (l *fixedLimiter) Allow(ip string) bool {
	l.seen = append(l.seen, ip)
	l.n--
	return l.n >= 0
}

// TestRateLimitCoversOnlyUncachedVerifications: unverified attempts consume
// the per-address budget and get 429 when it is exhausted; a cached bearer
// is never counted.
func TestRateLimitCoversOnlyUncachedVerifications(t *testing.T) {
	provider, upstream := countingProvider(t)
	lim := &fixedLimiter{n: 2}
	h := New(Options{
		Providers: func(context.Context) []ProviderTarget {
			return []ProviderTarget{{Name: "infra", MCPURL: provider.URL}}
		},
		Verifier: BearerVerifierFunc(func(_ *http.Request, token, _, _ string) error {
			if token == "good" {
				return nil
			}
			return ErrUnauthenticated
		}),
		RateLimiter: lim,
	})

	if rr := toolsList(h, "good"); rr.Code != http.StatusOK {
		t.Fatalf("good bearer: status = %d, want 200", rr.Code)
	}
	if rr := toolsList(h, "bad-1"); rr.Code != http.StatusUnauthorized {
		t.Fatalf("bad-1: status = %d, want 401", rr.Code)
	}
	if rr := toolsList(h, "bad-2"); rr.Code != http.StatusTooManyRequests {
		t.Fatalf("bad-2 over budget: status = %d, want 429", rr.Code)
	}
	if rr := toolsList(h, "good"); rr.Code != http.StatusOK {
		t.Fatalf("cached good bearer while throttled: status = %d, want 200", rr.Code)
	}
	if len(lim.seen) != 3 {
		t.Fatalf("limiter consulted %d times, want 3 (cache hit not counted)", len(lim.seen))
	}
	for _, ip := range lim.seen {
		if ip != "203.0.113.7" {
			t.Fatalf("limiter keyed on %q, want RemoteAddr host 203.0.113.7", ip)
		}
	}
	if got := upstream.Load(); got == 0 {
		t.Fatal("good bearer was never federated")
	}
}

func TestParseMCPServerPath(t *testing.T) {
	cluster, name, ok := parseMCPServerPath(testMCPPath)
	if !ok || cluster != "some-cluster" || name != "default" {
		t.Fatalf("parse = (%q,%q,%v), want (some-cluster, default, true)", cluster, name, ok)
	}
	for _, bad := range []string{
		"/",
		"/cluster/apis/faros.sh/v1alpha1/mcpservers",                // too short
		"/cluster/apis/wrong.group/v1alpha1/mcpservers/default/mcp", // wrong group
		"/cluster/apis/faros.sh/v1alpha1/mcpservers/default/x",      // not /mcp
	} {
		if _, _, ok := parseMCPServerPath(bad); ok {
			t.Errorf("parseMCPServerPath(%q) = ok, want !ok", bad)
		}
	}
}

// jsonrpc POSTs a single JSON-RPC method to the handler (prefix already
// stripped) and returns the decoded envelope result + HTTP status.
func jsonrpc(t *testing.T, h http.Handler, method string, params string) (json.RawMessage, int) {
	t.Helper()
	body := `{"jsonrpc":"2.0","id":1,"method":"` + method + `","params":` + params + `}`
	req := httptest.NewRequest(http.MethodPost, testMCPPath, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer test-token")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	raw, _ := io.ReadAll(rr.Body)
	if rr.Code != http.StatusOK {
		return nil, rr.Code
	}
	payload := raw
	if strings.HasPrefix(rr.Header().Get("Content-Type"), "text/event-stream") {
		d, ok := firstSSEData(raw)
		if !ok {
			t.Fatalf("no SSE data line in response: %s", raw)
		}
		payload = d
	}
	var env struct {
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(payload, &env); err != nil {
		t.Fatalf("decode envelope: %v (body=%s)", err, payload)
	}
	return env.Result, http.StatusOK
}

// TestAlwaysOnEmptyAggregate is the core guarantee: with zero providers the
// endpoint still initializes and serves an (empty) tools/list — never 501.
func TestAlwaysOnEmptyAggregate(t *testing.T) {
	h := New(Options{Providers: func(context.Context) []ProviderTarget { return nil }, Verifier: allowAll})

	if _, code := jsonrpc(t, h, "initialize", `{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"1"}}`); code != http.StatusOK {
		t.Fatalf("initialize status = %d, want 200 (endpoint must be always-on)", code)
	}

	result, code := jsonrpc(t, h, "tools/list", `{}`)
	if code != http.StatusOK {
		t.Fatalf("tools/list status = %d, want 200", code)
	}
	var out struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(result, &out); err != nil {
		t.Fatalf("decode tools/list: %v", err)
	}
	if len(out.Tools) != 0 {
		t.Fatalf("empty aggregate returned %d tools, want 0", len(out.Tools))
	}
}

// TestUnauthorizedAndBadPath covers the two request-level guards.
func TestUnauthorizedAndBadPath(t *testing.T) {
	h := New(Options{Providers: func(context.Context) []ProviderTarget { return nil }, Verifier: allowAll})

	noAuth := httptest.NewRequest(http.MethodPost, testMCPPath, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, noAuth)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("missing bearer: status = %d, want 401", rr.Code)
	}

	badPath := httptest.NewRequest(http.MethodPost, "/nope", strings.NewReader(`{}`))
	badPath.Header.Set("Authorization", "Bearer t")
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, badPath)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("bad path: status = %d, want 400", rr.Code)
	}
}

// TestFederatesReadyProvider stands up a fake provider MCP server and checks
// its tool shows up namespaced as "<provider>__<tool>" in the aggregate, and
// that calling it proxies through.
func TestFederatesReadyProvider(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
		switch req.Method {
		case "tools/list":
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"tools":[{"name":"provision","description":"make a thing","inputSchema":{"type":"object"}}]}}`))
		case "tools/call":
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"provisioned"}]}}`))
		default:
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}`))
		}
	}))
	defer provider.Close()

	h := New(Options{Providers: func(context.Context) []ProviderTarget {
		return []ProviderTarget{{Name: "infra", DisplayName: "Infrastructure", MCPURL: provider.URL}}
	}, Verifier: allowAll})

	result, code := jsonrpc(t, h, "tools/list", `{}`)
	if code != http.StatusOK {
		t.Fatalf("tools/list status = %d", code)
	}
	var out struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(result, &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	found := false
	for _, tl := range out.Tools {
		if tl.Name == "infra__provision" {
			found = true
		}
	}
	if !found {
		t.Fatalf("federated tool infra__provision not in aggregate; got %+v", out.Tools)
	}

	callResult, code := jsonrpc(t, h, "tools/call", `{"name":"infra__provision","arguments":{}}`)
	if code != http.StatusOK {
		t.Fatalf("tools/call status = %d", code)
	}
	if !strings.Contains(string(callResult), "provisioned") {
		t.Fatalf("tools/call did not proxy through; got %s", callResult)
	}
}
