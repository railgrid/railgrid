// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package api

// Capability discovery: what can an agent in THIS workspace actually reach?
//
// The answer comes from the hub's aggregate MCP endpoint rather than from
// configuration, because that endpoint federates exactly the providers a tenant
// has enabled — so listing its tools answers the question the UI actually has
// ("can an agent provision infrastructure for me?") instead of a proxy for it.
//
// Interactive runs always get this endpoint (it authenticates as the calling
// user, see buildToolset), so a capability reported here is one any agent can
// use in chat without any tool grant.

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/faroshq/provider-agents/engine"
	"github.com/faroshq/provider-agents/tools"
)

// capabilityTTL bounds how stale a cached probe may be. Enabling a provider is
// a rare, deliberate act, so a minute of staleness costs nothing and saves the
// portal an MCP round-trip on every tab switch.
const capabilityTTL = time.Minute

type capabilityResult struct {
	// Providers are the federated tool namespaces present on the endpoint
	// ("infrastructure", "code", "kuery", …), derived from the tool names.
	Providers []string `json:"providers"`
	// Unavailable is set when the endpoint could not be reached at all, so the
	// UI can distinguish "nothing enabled" from "we could not tell".
	Unavailable bool   `json:"unavailable,omitempty"`
	Message     string `json:"message,omitempty"`
}

type capabilityCache struct {
	mu      sync.Mutex
	entries map[string]capabilityEntry
}

type capabilityEntry struct {
	result capabilityResult
	at     time.Time
}

func newCapabilityCache() *capabilityCache {
	return &capabilityCache{entries: map[string]capabilityEntry{}}
}

func (c *capabilityCache) get(key string) (capabilityResult, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok || time.Since(e.at) > capabilityTTL {
		return capabilityResult{}, false
	}
	return e.result, true
}

func (c *capabilityCache) put(key string, r capabilityResult) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[key] = capabilityEntry{result: r, at: time.Now()}
}

// listCapabilities serves GET /api/capabilities.
func (s *Server) listCapabilities(w http.ResponseWriter, r *http.Request) {
	_, id, ok := s.requireClient(w, r)
	if !ok {
		return
	}
	endpoint := s.edgesEndpoint(id.clusterID)
	if endpoint == "" || id.token == "" {
		writeJSON(w, http.StatusOK, capabilityResult{
			Providers: []string{}, Unavailable: true,
			Message: "the hub's aggregate tool endpoint is not configured for this workspace",
		})
		return
	}
	if cached, hit := s.capabilities.get(id.clusterID); hit {
		writeJSON(w, http.StatusOK, cached)
		return
	}

	// Bound the probe: a slow or wedged provider must not hang a portal load.
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	sess, err := tools.ConnectMCPEndpoint(ctx, endpoint, id.token, "faros", s.cfg.HubInsecure)
	if err != nil {
		// Not cached: a transient failure should not suppress the capability
		// for a whole minute.
		writeJSON(w, http.StatusOK, capabilityResult{
			Providers: []string{}, Unavailable: true,
			Message: "could not reach the tool endpoint: " + err.Error(),
		})
		return
	}
	defer sess.Close()

	res := capabilityResult{Providers: providersFromTools(sess.Tools)}
	s.capabilities.put(id.clusterID, res)
	writeJSON(w, http.StatusOK, res)
}

// providersFromTools derives the federated provider set from tool names. The
// aggregate endpoint namespaces every tool as "<provider>__<tool>", and our MCP
// client prefixes what it dials, so a tool arrives as "faros__<provider>__<tool>".
func providersFromTools(ts []engine.Tool) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, t := range ts {
		parts := strings.Split(t.Name, "__")
		if len(parts) < 3 {
			continue
		}
		if p := parts[1]; p != "" && !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	return out
}
