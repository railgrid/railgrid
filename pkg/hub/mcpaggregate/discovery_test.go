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
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-logr/logr"
)

// fakeProvider is a provider /mcp endpoint whose tools, instructions and
// health can change between requests, and which counts requests per method.
type fakeProvider struct {
	*httptest.Server

	mu           sync.Mutex
	tools        []string
	instructions string
	fail         bool
	delay        time.Duration
	counts       map[string]int
}

func newFakeProvider(t *testing.T, tools ...string) *fakeProvider {
	t.Helper()
	f := &fakeProvider{tools: tools, instructions: "guide-v1", counts: map[string]int{}}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		f.mu.Lock()
		f.counts[req.Method]++
		tools, instructions, fail, delay := f.tools, f.instructions, f.fail, f.delay
		f.mu.Unlock()
		time.Sleep(delay)
		if fail {
			http.Error(w, "provider down", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch req.Method {
		case "initialize":
			_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":1,"result":{"instructions":%q}}`, instructions)
		case "tools/list":
			list := make([]string, 0, len(tools))
			for _, name := range tools {
				list = append(list, fmt.Sprintf(`{"name":%q,"inputSchema":{"type":"object"}}`, name))
			}
			_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":1,"result":{"tools":[%s]}}`, strings.Join(list, ","))
		case "tools/call":
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"done"}]}}`))
		default:
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}`))
		}
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeProvider) count(method string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.counts[method]
}

func (f *fakeProvider) update(fn func(f *fakeProvider)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(f)
}

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// cachedHandler is an aggregate over the given targets whose discovery cache
// runs on a fake clock.
func cachedHandler(t *testing.T, targets func() []ProviderTarget) (*handler, *fakeClock) {
	t.Helper()
	h := New(Options{
		Providers: func(context.Context, Caller) []ProviderTarget { return targets() },
		Verifier:  allowAll,
	}).(*handler)
	clk := &fakeClock{t: time.Unix(1_800_000_000, 0)}
	h.discovery.now = clk.now
	return h, clk
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// waitRefreshed waits until every cached entry is fresh again — i.e. any
// background refresh has been stored.
func waitRefreshed(t *testing.T, h *handler, clk *fakeClock) {
	t.Helper()
	eventually(t, "the background refresh to be stored", func() bool {
		h.discovery.mu.Lock()
		defer h.discovery.mu.Unlock()
		for _, e := range h.discovery.entries {
			if !clk.now().Before(e.refreshAt) {
				return false
			}
		}
		return true
	})
}

func oneTarget(p *fakeProvider) func() []ProviderTarget {
	return func() []ProviderTarget { return []ProviderTarget{{Name: "infra", MCPURL: p.URL}} }
}

// Repeated requests of one caller — tools/call included — reuse one
// discovery instead of fanning out to every provider each time.
func TestDiscoveryCachedAcrossRequests(t *testing.T) {
	p := newFakeProvider(t, "provision")
	h, _ := cachedHandler(t, oneTarget(p))

	for range 3 {
		assertTools(t, toolNames(t, h, "alice"), "infra__provision")
	}
	if got := string(mcpCall(t, h, "alice", "initialize", `{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"1"}}`)); !strings.Contains(got, "guide-v1") {
		t.Fatalf("initialize lost the provider's instructions: %s", got)
	}
	if got := string(mcpCall(t, h, "alice", "tools/call", `{"name":"infra__provision","arguments":{}}`)); !strings.Contains(got, "done") {
		t.Fatalf("tools/call did not proxy through: %s", got)
	}
	if got := p.count("tools/list"); got != 1 {
		t.Fatalf("provider tools/list calls = %d, want 1", got)
	}
	if got := p.count("initialize"); got != 1 {
		t.Fatalf("provider initialize calls = %d, want 1", got)
	}
	if got := p.count("tools/call"); got != 1 {
		t.Fatalf("provider tools/call calls = %d, want 1", got)
	}
}

// Each bearer gets its own discovery: what a provider answered one caller is
// never served to another.
func TestDiscoveryCacheIsPerBearer(t *testing.T) {
	p := newFakeProvider(t, "provision")
	h, _ := cachedHandler(t, oneTarget(p))

	toolNames(t, h, "alice")
	toolNames(t, h, "bob")
	toolNames(t, h, "alice")
	if got := p.count("tools/list"); got != 2 {
		t.Fatalf("provider tools/list calls = %d, want 2 (one per bearer)", got)
	}
}

// Concurrent requests of one caller on a cold cache share one discovery.
func TestConcurrentColdRequestsShareOneDiscovery(t *testing.T) {
	p := newFakeProvider(t, "provision")
	p.delay = 100 * time.Millisecond
	h, _ := cachedHandler(t, oneTarget(p))

	var wg sync.WaitGroup
	codes := make([]int, 8)
	for i := range codes {
		wg.Go(func() { codes[i] = toolsList(h, "alice").Code })
	}
	wg.Wait()
	for i, code := range codes {
		if code != http.StatusOK {
			t.Fatalf("request %d: status %d", i, code)
		}
	}
	if got := p.count("tools/list"); got != 1 {
		t.Fatalf("provider tools/list calls = %d, want 1", got)
	}
}

// Past the fresh TTL the cached answer is served at once and refreshed in the
// background; the request after the refresh sees the provider's new tools.
func TestDiscoveryServesStaleWhileRefreshing(t *testing.T) {
	p := newFakeProvider(t, "provision")
	h, clk := cachedHandler(t, oneTarget(p))

	assertTools(t, toolNames(t, h, "alice"), "infra__provision")
	p.update(func(f *fakeProvider) { f.tools = []string{"provision", "destroy"}; f.delay = 50 * time.Millisecond })
	clk.advance(DefaultDiscoveryCacheTTL + time.Second)

	assertTools(t, toolNames(t, h, "alice"), "infra__provision")
	eventually(t, "the background refresh", func() bool { return p.count("tools/list") == 2 })
	eventually(t, "the refreshed tools", func() bool {
		return strings.Join(toolNames(t, h, "alice"), ",") == "infra__destroy,infra__provision"
	})
	if got := p.count("tools/list"); got != 2 {
		t.Fatalf("provider tools/list calls = %d, want 2", got)
	}
}

// An answer older than discoveryMaxStale is not served: the request waits for
// a fresh discovery.
func TestDiscoveryPastMaxStaleIsFetchedSynchronously(t *testing.T) {
	p := newFakeProvider(t, "provision")
	h, clk := cachedHandler(t, oneTarget(p))

	toolNames(t, h, "alice")
	p.update(func(f *fakeProvider) { f.tools = []string{"destroy"} })
	clk.advance(discoveryMaxStale + time.Second)
	assertTools(t, toolNames(t, h, "alice"), "infra__destroy")
}

// A failed refresh of a Ready provider keeps its last good tools, retries
// after discoveryErrorTTL, and gives up on them once they pass
// discoveryMaxStale.
func TestFailedRefreshKeepsLastGoodTools(t *testing.T) {
	p := newFakeProvider(t, "provision")
	h, clk := cachedHandler(t, oneTarget(p))

	toolNames(t, h, "alice")
	p.update(func(f *fakeProvider) { f.fail = true })
	clk.advance(DefaultDiscoveryCacheTTL + time.Second)

	assertTools(t, toolNames(t, h, "alice"), "infra__provision")
	waitRefreshed(t, h, clk)
	assertTools(t, toolNames(t, h, "alice"), "infra__provision")
	if got := p.count("tools/list"); got != 2 {
		t.Fatalf("provider re-dialled before the retry delay: tools/list calls = %d, want 2", got)
	}

	clk.advance(discoveryErrorTTL + time.Second)
	assertTools(t, toolNames(t, h, "alice"), "infra__provision")
	waitRefreshed(t, h, clk)
	if got := p.count("tools/list"); got != 3 {
		t.Fatalf("provider tools/list calls after the retry delay = %d, want 3", got)
	}

	clk.advance(discoveryMaxStale)
	assertTools(t, toolNames(t, h, "alice"))
}

// A provider whose first discovery fails contributes nothing, is not
// re-dialled by every request, and is picked up again once it recovers.
func TestFailedDiscoveryIsRetriedAfterErrorTTL(t *testing.T) {
	p := newFakeProvider(t, "provision")
	p.fail = true
	h, clk := cachedHandler(t, oneTarget(p))

	assertTools(t, toolNames(t, h, "alice"))
	assertTools(t, toolNames(t, h, "alice"))
	if got := p.count("tools/list"); got != 1 {
		t.Fatalf("failing provider tools/list calls = %d, want 1 within the error TTL", got)
	}

	p.update(func(f *fakeProvider) { f.fail = false })
	clk.advance(discoveryErrorTTL + time.Second)
	eventually(t, "the recovered provider's tools", func() bool {
		return strings.Join(toolNames(t, h, "alice"), ",") == "infra__provision"
	})
}

// Enumeration is not cached: a provider that becomes Ready is federated on
// the very next request, and one that stops being Ready drops out at once.
func TestReadySetChangesApplyImmediately(t *testing.T) {
	a := newFakeProvider(t, "provision")
	b := newFakeProvider(t, "commit_files")
	var mu sync.Mutex
	targets := []ProviderTarget{{Name: "infra", MCPURL: a.URL}}
	h, _ := cachedHandler(t, func() []ProviderTarget {
		mu.Lock()
		defer mu.Unlock()
		return append([]ProviderTarget(nil), targets...)
	})

	assertTools(t, toolNames(t, h, "alice"), "infra__provision")
	mu.Lock()
	targets = append(targets, ProviderTarget{Name: "code", MCPURL: b.URL})
	mu.Unlock()
	assertTools(t, toolNames(t, h, "alice"), "code__commit_files", "infra__provision")

	mu.Lock()
	targets = targets[1:]
	mu.Unlock()
	assertTools(t, toolNames(t, h, "alice"), "code__commit_files")
	if got := a.count("tools/list"); got != 1 {
		t.Fatalf("infra tools/list calls = %d, want 1", got)
	}
}

// A negative DiscoveryCacheTTL turns the cache off.
func TestDiscoveryCacheDisabled(t *testing.T) {
	p := newFakeProvider(t, "provision")
	h := New(Options{
		Providers:         func(context.Context, Caller) []ProviderTarget { return oneTarget(p)() },
		Verifier:          allowAll,
		DiscoveryCacheTTL: -1,
	})
	toolNames(t, h, "alice")
	toolNames(t, h, "alice")
	if got := p.count("tools/list"); got != 2 {
		t.Fatalf("provider tools/list calls = %d, want 2 with the cache disabled", got)
	}
}

// Discovery is detached from the request's cancellation, so a caller that
// goes away cannot leave a failure in the cache for everyone sharing it.
func TestDiscoveryIgnoresRequestCancellation(t *testing.T) {
	p := newFakeProvider(t, "provision")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	d := fetchDiscovery(ctx, logr.Discard(), newProviderMCPClient("alice", "c"), ProviderTarget{Name: "infra", MCPURL: p.URL})
	if d.err != nil || len(d.tools) != 1 || d.instructions != "guide-v1" {
		t.Fatalf("discovery under a cancelled request = %+v, want the provider's answer", d)
	}
}
