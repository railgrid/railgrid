// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package api

// Reaching another provider's data plane.
//
// An agent's Connections can point at a tenant workload the infrastructure
// provider runs — a self-hosted search engine, a browser instance — and the
// tools reach it over the platform data plane rather than a public hostname.
//
// Composing that URL used to mean writing
// "/services/providers/infrastructure/dataplane/clusters/%s/%s/%s/proxy" by
// hand, which hardcodes two things this provider has no business knowing: the
// other provider's catalog NAME, and the grammar's spelling. The name is a
// deployment fact (a tenant could have the instance kinds served by something
// else), and the grammar belongs to provider-sdk/dataplane, which builds the
// path from a Request so there is one implementation of it in the tree.
//
// What stays legitimate to know is the API GROUP: an agent's Connection names
// an instance of infrastructure.railgrid.ai, and that is a contract between
// the two providers' APIs, not a routing detail. So the provider name is
// resolved from the TENANT'S OWN APIBinding for that group — the hub names a
// provider's binding after the provider (pkg/hub/restapi/providers_enable.go)
// — read as the caller.
//
// Read as the caller, and cached per workspace. A signed-in user can list
// their workspace's APIBindings; an agent's own scoped identity holds nothing
// of the sort, and should not. So the first interactive run in a workspace
// resolves it and every later run — including unattended ones — uses the
// cached answer. A cold cache means instance-backed tools report that they
// could not resolve the provider, which is a precise failure rather than a 404
// from two hops away.

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// APIBindingGVR is kcp's APIBinding, which every enabled provider has one of
// in the tenant's workspace. Exported because the identity request has to name
// the same coordinate it is granted on.
var APIBindingGVR = schema.GroupVersionResource{Group: "apis.kcp.io", Version: "v1alpha2", Resource: "apibindings"}

// ProviderNameForAPIGroup is the candidate binding name for an API group: the
// group's first label. It is the convention the hub follows when it enables a
// provider, and it is only ever a candidate — readProviderForAPIGroup confirms
// it against the binding before anything is built from it.
func ProviderNameForAPIGroup(group string) string {
	name, _, _ := strings.Cut(group, ".")
	return name
}

// providerLookupTTL bounds how stale a resolved provider name may be. Enabling
// or replacing a provider is a rare, deliberate act.
const providerLookupTTL = 15 * time.Minute

type providerLookupCache struct {
	mu      sync.Mutex
	entries map[string]providerLookupEntry
}

type providerLookupEntry struct {
	provider string
	at       time.Time
}

func newProviderLookupCache() *providerLookupCache {
	return &providerLookupCache{entries: map[string]providerLookupEntry{}}
}

func (c *providerLookupCache) get(key string) (string, bool) {
	if c == nil {
		return "", false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok || time.Since(e.at) > providerLookupTTL {
		return "", false
	}
	return e.provider, true
}

func (c *providerLookupCache) put(key, provider string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[key] = providerLookupEntry{provider: provider, at: time.Now()}
	if len(c.entries) > 512 {
		for k, v := range c.entries {
			if time.Since(v.at) > providerLookupTTL {
				delete(c.entries, k)
			}
		}
	}
}

// providerForAPIGroup returns the catalog name of the provider serving group in
// clusterID, or "" when it cannot be determined with the credentials in hand.
func (s *Server) providerForAPIGroup(ctx context.Context, clusterID, token, group string) string {
	if s == nil || clusterID == "" || group == "" {
		return ""
	}
	key := clusterID + "|" + group
	if cached, ok := s.providerLookups.get(key); ok {
		return cached
	}
	if token == "" || s.callers == nil {
		return ""
	}
	provider, err := s.readProviderForAPIGroup(ctx, clusterID, token, group)
	if err != nil {
		// Not cached: the next caller may hold credentials this one did not.
		log.Printf("agents: resolving the provider serving %s in %s: %v", group, clusterID, err)
		return ""
	}
	s.providerLookups.put(key, provider)
	return provider
}

func (s *Server) readProviderForAPIGroup(ctx context.Context, clusterID, token, group string) (string, error) {
	candidate := ProviderNameForAPIGroup(group)
	if candidate == "" {
		return "", fmt.Errorf("%q is not an API group a provider name can be derived from", group)
	}
	caller, err := s.callers.For(clusterID, token)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	binding, err := caller.Resource(APIBindingGVR).Get(ctx, candidate, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("reading APIBinding %q in %s: %w", candidate, clusterID, err)
	}
	// The consistency check is the point of reading the object at all: without
	// it this would be the hardcoded name with extra steps.
	if !bindingServesGroup(binding, group) {
		return "", fmt.Errorf("APIBinding %q in %s does not serve %s, so which provider does is unknown", candidate, clusterID, group)
	}
	return candidate, nil
}

// bindingServesGroup reports whether an APIBinding serves group. status is
// authoritative (it is what kcp actually bound); the export name is the
// fallback for a binding that has not reported yet, and by convention an
// APIExport is named after the group it exports.
func bindingServesGroup(binding *unstructured.Unstructured, group string) bool {
	bound, _, _ := unstructured.NestedSlice(binding.Object, "status", "boundResources")
	for _, raw := range bound {
		resource, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if g, _ := resource["group"].(string); g == group {
			return true
		}
	}
	exportName, _, _ := unstructured.NestedString(binding.Object, "spec", "reference", "export", "name")
	return exportName == group
}
