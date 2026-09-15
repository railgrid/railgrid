/*
Copyright 2026 The Railgrid Authors.

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

// Discovery cache.
//
// Building the aggregate for a request means asking every Ready provider for
// its instructions (initialize) and its tools (tools/list). Done per request,
// that fan-out runs for every MCP message — each tools/call included — so the
// slowest provider sets the floor for every call. The cache keeps each
// provider's answer per (bearer, cluster, target) and serves it
// stale-while-revalidate:
//
//   - younger than the fresh TTL: served as is;
//   - older, but younger than discoveryMaxStale: served as is, and one
//     background refresh is started, so the next request sees the new answer;
//   - older than discoveryMaxStale, or missing: fetched before the request
//     continues.
//
// A failed refresh does not throw away a good answer: the provider is still
// Ready (enumeration says so), so its last known tools keep being served —
// and a call to one reports the provider's real error — while the refresh is
// retried after discoveryErrorTTL, until the answer passes discoveryMaxStale.
//
// Agents think between tool calls, often for longer than any sensible TTL, so
// a plain TTL would miss on most calls; stale-while-revalidate keeps every
// call after the first off the discovery path.
//
// Enumeration is not cached. Which providers are Ready — and so which targets
// exist — is still decided per request with the verified Caller: a provider
// that becomes Ready is discovered on the very next request, one that stops
// being Ready drops out immediately, and an org-owned target still gets a
// delegated transport minted for this request. Only a Ready provider's own
// tool set and instructions can lag, by one request after the fresh TTL.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/go-logr/logr"
	"golang.org/x/sync/singleflight"
)

const (
	// DefaultDiscoveryCacheTTL is how long a provider's discovered tools and
	// instructions are served without a background refresh.
	DefaultDiscoveryCacheTTL = 30 * time.Second
	// discoveryErrorTTL is the fresh window of a failed discovery: short, so a
	// provider that recovers is retried soon, but long enough that a hung
	// provider is not re-dialled by every request in a burst.
	discoveryErrorTTL = 5 * time.Second
	// discoveryMaxStale bounds how old an answer may be and still be served
	// while it refreshes. Past it the request waits for a fresh discovery.
	discoveryMaxStale = 10 * time.Minute
	// maxDiscoveryEntries bounds the cache (one entry per bearer, cluster and
	// provider). Entries past discoveryMaxStale are swept once it is reached,
	// then the oldest entry is evicted.
	maxDiscoveryEntries = 2048
)

// discovery is one provider's answer to initialize + tools/list. A stored
// discovery is never modified (readers share it without the lock); a change
// stores a copy.
type discovery struct {
	tools        []discoveredTool
	instructions string
	// err is errNoMCPEndpoint for a provider without an MCP endpoint, any
	// other error for a failed tools/list, nil on success.
	err error
	// fetchedAt is when this answer was obtained; refreshAt is when serving
	// it starts a background refresh.
	fetchedAt time.Time
	refreshAt time.Time
}

type discoveryCache struct {
	ttl time.Duration
	now func() time.Time

	mu      sync.Mutex
	entries map[string]*discovery
	group   singleflight.Group
}

// newDiscoveryCache returns a cache with the given fresh TTL, or nil (no
// caching: every request discovers afresh) for a negative ttl.
func newDiscoveryCache(ttl time.Duration) *discoveryCache {
	if ttl < 0 {
		return nil
	}
	if ttl == 0 {
		ttl = DefaultDiscoveryCacheTTL
	}
	return &discoveryCache{ttl: ttl, now: time.Now, entries: make(map[string]*discovery)}
}

// discover returns, parallel to targets, what each provider contributes to
// this request: its tools and instructions plus the per-target client that
// tools/call must use. An entry is nil when the target is skipped — no MCP
// URL, an org-owned target without a transport, no MCP endpoint — or when the
// request was cancelled while waiting for discovery. A failed tools/list keeps
// the provider's instructions but contributes no tools.
func (c *discoveryCache) discover(ctx context.Context, log logr.Logger, cli *providerMCPClient, targets []ProviderTarget) []*providerTools {
	out := make([]*providerTools, len(targets))
	var wg sync.WaitGroup
	for i, p := range targets {
		if p.MCPURL == "" {
			continue
		}
		tc, ok := cli.forTarget(p)
		if !ok {
			log.V(1).Info("provider federation: org-owned provider has no delegated transport (skipping)", "provider", p.Name, "org", p.OrgUUID)
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			d := c.get(ctx, discoveryKey(cli.bearerToken, cli.clusterID, p), func() *discovery {
				return fetchDiscovery(ctx, log, tc, p)
			})
			if d == nil || errors.Is(d.err, errNoMCPEndpoint) {
				return
			}
			pt := &providerTools{provider: p, instructions: d.instructions, client: tc}
			if d.err == nil {
				pt.tools = d.tools
			}
			out[i] = pt
		}()
	}
	wg.Wait()
	return out
}

// get returns the cached discovery for key, refreshing or fetching it as the
// package comment describes. A nil cache always fetches. It returns nil only
// when ctx ends while the request waits on a fetch.
func (c *discoveryCache) get(ctx context.Context, key string, fetch func() *discovery) *discovery {
	if c == nil {
		return fetch()
	}
	refresh := func() (any, error) { return c.store(key, fetch()), nil }

	c.mu.Lock()
	d := c.entries[key]
	c.mu.Unlock()
	if d != nil {
		now := c.now()
		if now.Before(d.refreshAt) {
			return d
		}
		if now.Sub(d.fetchedAt) < discoveryMaxStale {
			// The result channel is buffered, so nobody has to read it.
			c.group.DoChan(key, refresh)
			return d
		}
	}
	select {
	case r := <-c.group.DoChan(key, refresh):
		return r.Val.(*discovery)
	case <-ctx.Done():
		return nil
	}
}

// store records a fetched discovery and returns what is now served for key.
func (c *discoveryCache) store(key string, d *discovery) *discovery {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	if d.err != nil && !errors.Is(d.err, errNoMCPEndpoint) {
		retryAt := now.Add(min(discoveryErrorTTL, c.ttl))
		if prev := c.entries[key]; prev != nil && prev.err == nil && now.Sub(prev.fetchedAt) < discoveryMaxStale {
			kept := *prev
			kept.refreshAt = retryAt
			c.entries[key] = &kept
			return &kept
		}
		d.refreshAt = retryAt
	} else {
		d.refreshAt = now.Add(c.ttl)
	}
	d.fetchedAt = now
	if _, ok := c.entries[key]; !ok && len(c.entries) >= maxDiscoveryEntries {
		for k, e := range c.entries {
			if now.Sub(e.fetchedAt) >= discoveryMaxStale {
				delete(c.entries, k)
			}
		}
	}
	if _, ok := c.entries[key]; !ok && len(c.entries) >= maxDiscoveryEntries {
		var victim string
		var oldest time.Time
		for k, e := range c.entries {
			if victim == "" || e.fetchedAt.Before(oldest) {
				victim, oldest = k, e.fetchedAt
			}
		}
		delete(c.entries, victim)
	}
	c.entries[key] = d
	return d
}

// discoveryKey binds an entry to the exact bearer (by digest; the bearer
// itself is never stored), tenant cluster and federation target it was
// discovered for, so one caller's answer is never served to another and a
// platform provider's answer never stands in for an org-owned one.
func discoveryKey(bearerToken, cluster string, p ProviderTarget) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{bearerToken, cluster, p.Name, p.OrgUUID, p.MCPURL}, "\x00")))
	return hex.EncodeToString(sum[:])
}

// fetchDiscovery asks one provider for its instructions and tools, both
// under a single providerDiscoveryTimeout. It runs detached from the
// request's cancellation: the result may be shared with other requests and
// cached, so a caller that disconnects mid-discovery must not turn into a
// cached failure for everyone else.
func fetchDiscovery(ctx context.Context, log logr.Logger, cli *providerMCPClient, p ProviderTarget) *discovery {
	dctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), providerDiscoveryTimeout)
	defer cancel()

	d := &discovery{}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer func() { _ = recover() }()
		d.instructions = strings.TrimSpace(cli.fetchInstructions(dctx, p.MCPURL))
	}()
	func() {
		defer func() {
			if r := recover(); r != nil {
				d.err = fmt.Errorf("discovery panic: %v", r)
			}
		}()
		d.tools, d.err = cli.listTools(dctx, p.MCPURL)
	}()
	wg.Wait()

	switch {
	case d.err == nil:
	case errors.Is(d.err, errNoMCPEndpoint):
		log.V(2).Info("provider federation: no MCP endpoint (skipping)", "provider", p.Name)
	default:
		log.Info("provider federation: tools/list failed (skipping)", "provider", p.Name, "org", p.OrgUUID, "mcpURL", p.MCPURL, "err", d.err.Error())
	}
	return d
}
