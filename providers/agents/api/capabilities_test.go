// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package api

import (
	"slices"
	"testing"
	"time"

	"github.com/faroshq/provider-agents/engine"
)

func TestProvidersFromTools(t *testing.T) {
	// Shape as it actually arrives: our MCP client prefixes what it dialed
	// ("faros"), and the aggregate endpoint namespaces each federated tool.
	tools := []engine.Tool{
		{Name: "faros__infrastructure__provision"},
		{Name: "faros__infrastructure__list_templates"},
		{Name: "faros__code__create_repository"},
		{Name: "faros__kuery__kuery_query"},
		// A tool the endpoint serves directly, with no provider segment.
		{Name: "faros__ping"},
		{Name: "malformed"},
	}
	got := providersFromTools(tools)
	for _, want := range []string{"infrastructure", "code", "kuery"} {
		if !slices.Contains(got, want) {
			t.Errorf("provider %q not detected in %v", want, got)
		}
	}
	if len(got) != 3 {
		t.Fatalf("got %v, want exactly the three federated providers (no dupes, nothing malformed)", got)
	}
}

func TestProvidersFromToolsEmpty(t *testing.T) {
	// A tenant with no providers enabled must serialize as an empty array, not
	// null — the portal maps over it.
	got := providersFromTools(nil)
	if got == nil {
		t.Fatal("want an empty slice, not nil")
	}
	if len(got) != 0 {
		t.Fatalf("got %v", got)
	}
}

func TestCapabilityCache(t *testing.T) {
	c := newCapabilityCache()
	if _, hit := c.get("cluster1"); hit {
		t.Fatal("empty cache must miss")
	}
	c.put("cluster1", capabilityResult{Providers: []string{"infrastructure"}})
	got, hit := c.get("cluster1")
	if !hit || !slices.Contains(got.Providers, "infrastructure") {
		t.Fatalf("get = %+v, hit=%v", got, hit)
	}
	// Workspaces must not read each other's capabilities.
	if _, hit := c.get("cluster2"); hit {
		t.Fatal("cache is keyed per workspace")
	}
	// An entry older than the TTL is a miss, so enabling a provider shows up.
	c.entries["cluster1"] = capabilityEntry{
		result: capabilityResult{Providers: []string{"infrastructure"}},
		at:     time.Now().Add(-2 * capabilityTTL),
	}
	if _, hit := c.get("cluster1"); hit {
		t.Fatal("stale entry must expire")
	}
}
