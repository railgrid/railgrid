/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package api

import (
	"context"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// testProviders answers the binding lookup with a fixed provider name, the way
// a workspace that enabled exactly one copy of the dependency would.
func testProviders(provider string) providerLookup {
	return func(_ context.Context, _, _, _ string) (string, error) { return provider, nil }
}

// defaultTestProviders is the lookup every test server gets unless it cares
// which provider it resolved: the platform copy, under its usual name.
var defaultTestProviders = testProviders(infraDependencyName)

func binding(name, export string) *unstructured.Unstructured {
	object := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apis.kcp.io/v1alpha2",
		"kind":       "APIBinding",
		"spec":       map[string]any{"reference": map[string]any{"export": map[string]any{"name": export}}},
	}}
	object.SetName(name)
	return object
}

func TestProviderResolverTakesTheNameFromTheBinding(t *testing.T) {
	got, err := providerForBinding(binding("infrastructure", infraAPIExportName), infraAPIExportName, "cluster-a")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	// The name is the BINDING's, because that is the segment the hub mounts
	// the provider under — not the export's, and not a constant.
	if got != "infrastructure" {
		t.Fatalf("provider = %q, want infrastructure", got)
	}
}

// The GET is by the name the hub enables a provider under, so reading the
// object earns its keep only through this assertion: a binding that does not
// serve the export asked for answers a different question, and guessing from
// it would be the hardcoded name with extra steps.
func TestProviderResolverRefusesABindingThatServesAnotherExport(t *testing.T) {
	if _, err := providerForBinding(binding("infrastructure", "edges.providers.railgrid.ai"), infraAPIExportName, "cluster-a"); err == nil {
		t.Fatal("expected a binding serving another export to be refused")
	}
	// No binding at all is the ordinary "the dependency is not enabled here"
	// answer, and says so.
	_, err := providerForBinding(nil, infraAPIExportName, "cluster-a")
	if err == nil || !strings.Contains(err.Error(), "binds no provider") {
		t.Fatalf("unbound dependency error = %v", err)
	}
}

func TestProviderResolverCachesPerClusterAndExport(t *testing.T) {
	resolver := NewProviderResolver("https://hub.example", false, 0)
	resolver.hot["cluster-a\x00"+infraAPIExportName] = providerEntry{provider: "cached", expiresAt: time.Now().Add(time.Minute)}
	got, err := resolver.Resolve(context.Background(), "cluster-a", "tok", infraAPIExportName)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got != "cached" {
		t.Fatalf("provider = %q, want the cached answer", got)
	}
	// A different export is a different question and is not served from that
	// entry — it would need its own lookup, which has no hub to reach here.
	if _, err := resolver.Resolve(context.Background(), "cluster-a", "tok", "code.providers.railgrid.ai"); err == nil {
		t.Fatal("expected a cache miss to attempt a lookup")
	}
}
