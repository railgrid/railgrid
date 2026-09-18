// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package api

// Multi-shard virtual-workspace coverage. An APIExportEndpointSlice carries one
// endpoint per kcp shard, and each endpoint serves only the tenant workspaces
// bound on that shard. Addressing a single endpoint made every tenant on the
// other shards unreachable; the inbound HTTP paths must resolve the shard that
// serves the cluster they were given.

import (
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func sliceWithEndpoints(urls ...string) *unstructured.Unstructured {
	eps := make([]any, 0, len(urls))
	for _, u := range urls {
		eps = append(eps, map[string]any{"url": u})
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apis.kcp.io/v1alpha1",
		"kind":       "APIExportEndpointSlice",
		"metadata":   map[string]any{"name": apiExportNameForSlice},
		"status":     map[string]any{"endpoints": eps},
	}}
}

// TestSliceEndpointURLsKeepsEveryShard is the regression: a two-shard slice
// must yield BOTH URLs. Returning only the first is what stranded every tenant
// on the second shard.
func TestSliceEndpointURLsKeepsEveryShard(t *testing.T) {
	root := "https://root-kcp:6443/services/apiexport/2tr07/agents.railgrid.ai"
	alpha := "https://alpha-shard-kcp:6443/services/apiexport/2tr07/agents.railgrid.ai"

	got, err := sliceEndpointURLs(sliceWithEndpoints(root, alpha))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != root || got[1] != alpha {
		t.Fatalf("want both endpoints in order, got %v", got)
	}
}

func TestSliceEndpointURLsNormalizes(t *testing.T) {
	// Trailing slashes trimmed, blanks dropped, duplicates collapsed — so a
	// cosmetic difference cannot produce two clients for one shard.
	got, err := sliceEndpointURLs(sliceWithEndpoints("https://a/vw/", "", "https://a/vw", "  https://b/vw  "))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != "https://a/vw" || got[1] != "https://b/vw" {
		t.Fatalf("want [https://a/vw https://b/vw], got %v", got)
	}
}

func TestSliceEndpointURLsErrors(t *testing.T) {
	if _, err := sliceEndpointURLs(sliceWithEndpoints()); err == nil {
		t.Error("want an error for a slice with no endpoints")
	}
	if _, err := sliceEndpointURLs(sliceWithEndpoints("", "  ")); err == nil {
		t.Error("want an error when no endpoint carries a url")
	}
}

// TestReadyBeforeDiscovery: nothing is ready until the endpoint slice has been
// read at least once.
func TestReadyBeforeDiscovery(t *testing.T) {
	b := &background{}
	if b.ready() {
		t.Error("ready() must be false before any endpoint is discovered")
	}
	b.shards = []*vwShard{{url: "https://a/vw"}}
	if !b.ready() {
		t.Error("ready() must be true once an endpoint is known")
	}
}

// TestShardForUsesCache: once a list has placed a cluster, scoped() must
// address that shard rather than probing or defaulting to the first.
func TestShardForUsesCache(t *testing.T) {
	b := &background{
		shards:       []*vwShard{{url: "https://root/vw"}, {url: "https://alpha/vw"}},
		clusterShard: map[string]string{"tenantcluster": "https://alpha/vw"},
	}
	got, err := b.shardFor(t.Context(), "tenantcluster")
	if err != nil {
		t.Fatal(err)
	}
	if got != "https://alpha/vw" {
		t.Fatalf("want the cached alpha endpoint, got %q", got)
	}
}

// TestShardForSingleShard: with one endpoint there is nothing to resolve, so an
// unseen cluster must not pay for a probe.
func TestShardForSingleShard(t *testing.T) {
	b := &background{shards: []*vwShard{{url: "https://only/vw"}}}
	got, err := b.shardFor(t.Context(), "never-listed")
	if err != nil {
		t.Fatal(err)
	}
	if got != "https://only/vw" {
		t.Fatalf("want the only endpoint, got %q", got)
	}
}

func TestShardForNoShards(t *testing.T) {
	b := &background{}
	if _, err := b.shardFor(t.Context(), "c"); err == nil || !strings.Contains(err.Error(), "no APIExport") {
		t.Fatalf("want a clear not-discovered-yet error, got %v", err)
	}
}

// TestRememberClusterIsConcurrencySafe: the discovery tick rewrites the map
// while webhook and gateway callbacks read it.
func TestRememberClusterIsConcurrencySafe(t *testing.T) {
	b := &background{shards: []*vwShard{{url: "https://a/vw"}}}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := range 200 {
			b.rememberCluster("c"+string(rune('a'+i%26)), "https://a/vw")
		}
	}()
	for range 200 {
		_, _ = b.shardFor(t.Context(), "cq")
		b.ready()
	}
	<-done
}
