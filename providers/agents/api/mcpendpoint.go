// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package api

// The workspace's aggregate MCP endpoint — the URL the edges tool family and
// every interactive run dial to reach whatever the tenant's enabled providers
// federate.
//
// It used to be composed here from a hardcoded path
// ("<hub>/services/mcpserver/<cluster>/apis/railgrid.ai/v1alpha1/mcpservers/default/mcp"),
// which is the hub's routing shape restated in a provider that has no business
// knowing it: change the hub's mount and every agent's tools break with a 404
// two hops away. The MCPServer object already publishes the answer on
// status.URL (apis/railgrid/v1alpha1/types_mcpserver.go), so this reads it,
// as the caller, off the conventional "default" server in the tenant's own
// workspace.
//
// Reading it as the CALLER matters: the endpoint the agent will dial with the
// caller's token is the one that caller can see, and a tenant that cannot read
// their own MCPServer has no business getting its URL from us.

import (
	"context"
	"fmt"
	"sync"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// mcpServerGVR is the hub's aggregate MCP endpoint object, bound in every
// tenant workspace.
var mcpServerGVR = schema.GroupVersionResource{Group: "railgrid.ai", Version: "v1alpha1", Resource: "mcpservers"}

// defaultMCPServer is the conventional endpoint every workspace gets. A tenant
// may have more (a read-only "audit" one, a full-access "ops" one); agents use
// the default unless and until an Agent names one.
const defaultMCPServer = "default"

// mcpEndpointTTL bounds how stale a cached URL may be. The URL changes when the
// hub re-provisions the endpoint, which is rare, and a run that dials a stale
// one fails loudly rather than silently doing the wrong thing.
const mcpEndpointTTL = 5 * time.Minute

type mcpEndpointCache struct {
	mu      sync.Mutex
	entries map[string]mcpEndpointEntry
}

type mcpEndpointEntry struct {
	url string
	// providers are the reachable federated providers the endpoint publishes,
	// which is the same answer the old GET /api/capabilities probe computed by
	// dialling MCP and splitting tool names — except the hub's own reconciler
	// already computed it and put it on the object.
	providers []string
	at        time.Time
}

func newMCPEndpointCache() *mcpEndpointCache {
	return &mcpEndpointCache{entries: map[string]mcpEndpointEntry{}}
}

func (c *mcpEndpointCache) get(key string) (mcpEndpointEntry, bool) {
	if c == nil {
		return mcpEndpointEntry{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok || time.Since(e.at) > mcpEndpointTTL {
		return mcpEndpointEntry{}, false
	}
	return e, true
}

func (c *mcpEndpointCache) put(key string, entry mcpEndpointEntry) {
	if c == nil {
		return
	}
	entry.at = time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[key] = entry
	// Bounded: one entry per workspace this replica has served, and a workspace
	// that stops asking ages out.
	if len(c.entries) > 512 {
		for k, v := range c.entries {
			if time.Since(v.at) > mcpEndpointTTL {
				delete(c.entries, k)
			}
		}
	}
}

// aggregateMCPEndpoint returns the workspace's aggregate MCP URL, or "" when
// there is none to give. Empty is a usable answer: the tool families that dial
// it report "no endpoint" precisely, which is better than composing a URL the
// hub may not answer.
func (s *Server) aggregateMCPEndpoint(ctx context.Context, id identity) string {
	return s.mcpServerStatus(ctx, id).url
}

// federatedProviders reports which providers the workspace's aggregate MCP
// endpoint currently reaches ("infrastructure", "code", "edges", …). It answers
// the question the portal and the discovery tools actually have — "can an agent
// here provision infrastructure for me?" — from the hub's own reconciled view
// of the endpoint, instead of this provider dialling MCP and splitting tool
// names to guess at it.
func (s *Server) federatedProviders(ctx context.Context, id identity) []string {
	return s.mcpServerStatus(ctx, id).providers
}

func (s *Server) mcpServerStatus(ctx context.Context, id identity) mcpEndpointEntry {
	if s == nil || id.clusterID == "" || id.token == "" || s.callers == nil {
		return mcpEndpointEntry{}
	}
	if cached, ok := s.mcpEndpoints.get(id.clusterID); ok {
		return cached
	}
	entry, err := s.readMCPServer(ctx, id)
	if err != nil {
		// Not cached: a transient failure must not suppress the endpoint for
		// the whole TTL.
		return mcpEndpointEntry{}
	}
	s.mcpEndpoints.put(id.clusterID, entry)
	return entry
}

func (s *Server) readMCPServer(ctx context.Context, id identity) (mcpEndpointEntry, error) {
	caller, err := s.callers.For(id.clusterID, id.token)
	if err != nil {
		return mcpEndpointEntry{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	object, err := caller.Resource(mcpServerGVR).Get(ctx, defaultMCPServer, metav1.GetOptions{})
	if err != nil {
		return mcpEndpointEntry{}, fmt.Errorf("reading MCPServer %q in %s: %w", defaultMCPServer, id.clusterID, err)
	}
	// The field is spelled "URL" on the CR, not "url" — see MCPServerStatus.
	url, _, err := unstructured.NestedString(object.Object, "status", "URL")
	if err != nil || url == "" {
		return mcpEndpointEntry{}, fmt.Errorf("MCPServer %q in %s has no status.URL yet", defaultMCPServer, id.clusterID)
	}
	entry := mcpEndpointEntry{url: url, providers: []string{}}
	federated, _, _ := unstructured.NestedSlice(object.Object, "status", "federatedProviders")
	for _, raw := range federated {
		p, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		// Unreachable providers are left out: a capability the endpoint cannot
		// currently deliver must not read as available.
		if reachable, _ := p["reachable"].(bool); !reachable {
			continue
		}
		if name, _ := p["name"].(string); name != "" {
			entry.providers = append(entry.providers, name)
		}
	}
	return entry, nil
}
