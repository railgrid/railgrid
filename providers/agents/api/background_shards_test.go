// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package api

// Multi-shard virtual-workspace coverage. An APIExportEndpointSlice carries one
// endpoint per kcp shard, and each endpoint serves only the tenant workspaces
// bound on that shard. Binding to a single endpoint made every tenant on the
// other shards invisible with no error anywhere — a wildcard list against the
// wrong shard returns an empty list, not a failure.

import (
	"errors"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"

	agentsclient "github.com/faroshq/provider-agents/client"
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
	root := "https://root-kcp:6443/services/apiexport/2tr07/agents.kedge.faros.sh"
	alpha := "https://alpha-shard-kcp:6443/services/apiexport/2tr07/agents.kedge.faros.sh"

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

// connectionsScheme registers the list kind the dynamic fake needs to serve
// wildcard List calls for connections.
func connectionsScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	s.AddKnownTypeWithName(
		schema.GroupVersionKind{Group: agentsclient.ConnectionGVR.Group, Version: agentsclient.ConnectionGVR.Version, Kind: "ConnectionList"},
		&unstructured.UnstructuredList{},
	)
	return s
}

func connectionObj(cluster, name string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": agentsclient.ConnectionGVR.GroupVersion().String(),
		"kind":       "Connection",
		"metadata": map[string]any{
			"name":        name,
			"annotations": map[string]any{"kcp.io/cluster": cluster},
		},
		"spec": map[string]any{"type": "discord"},
	}}
}

func shardWith(t *testing.T, url string, objs ...runtime.Object) *vwShard {
	t.Helper()
	return &vwShard{
		url: url,
		wildcard: dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
			connectionsScheme(),
			map[schema.GroupVersionResource]string{agentsclient.ConnectionGVR: "ConnectionList"},
			objs...,
		),
	}
}

// TestListAllMergesAcrossShards covers the real production shape: the tenant
// with the Discord connection lives on the SECOND endpoint. Listing only the
// first returns nothing and the bot never connects.
func TestListAllMergesAcrossShards(t *testing.T) {
	b := &background{
		shards: []*vwShard{
			shardWith(t, "https://root/vw", connectionObj("rootcluster", "other-conn")),
			shardWith(t, "https://alpha/vw", connectionObj("tenantcluster", "discordia-kedge")),
		},
	}

	items, err := b.listAll(t.Context(), agentsclient.ConnectionGVR)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for i := range items {
		names[items[i].GetName()] = true
	}
	if !names["discordia-kedge"] || !names["other-conn"] {
		t.Fatalf("want connections from both shards, got %v", names)
	}

	// The merge must also record where each tenant lives, so the write path
	// (scoped) addresses the shard that actually serves it.
	if got := b.clusterShard["tenantcluster"]; got != "https://alpha/vw" {
		t.Errorf("tenantcluster should map to the alpha endpoint, got %q", got)
	}
	if got := b.clusterShard["rootcluster"]; got != "https://root/vw" {
		t.Errorf("rootcluster should map to the root endpoint, got %q", got)
	}
}

// brokenShard is a shard whose VW endpoint errors on every list (unreachable
// shard, expired credential).
func brokenShard(t *testing.T, url string) *vwShard {
	t.Helper()
	s := shardWith(t, url)
	s.wildcard.(*dynamicfake.FakeDynamicClient).PrependReactor("list", "connections",
		func(k8stesting.Action) (bool, runtime.Object, error) {
			return true, nil, errors.New("shard unreachable")
		})
	return s
}

// TestListAllToleratesOneBadShard: one unhealthy shard must not stall the
// tenants on the healthy ones.
func TestListAllToleratesOneBadShard(t *testing.T) {
	b := &background{
		shards: []*vwShard{
			brokenShard(t, "https://broken/vw"),
			shardWith(t, "https://alpha/vw", connectionObj("tenantcluster", "discordia-kedge")),
		},
	}

	items, err := b.listAll(t.Context(), agentsclient.ConnectionGVR)
	if err != nil {
		t.Fatalf("a single failing shard must not fail the whole list: %v", err)
	}
	if len(items) != 1 || items[0].GetName() != "discordia-kedge" {
		t.Fatalf("want the healthy shard's connection, got %v", items)
	}
}

// TestListAllFailsWhenEveryShardFails distinguishes a total outage from an
// empty-but-healthy result, which would otherwise look the same.
func TestListAllFailsWhenEveryShardFails(t *testing.T) {
	b := &background{shards: []*vwShard{brokenShard(t, "https://a/vw"), brokenShard(t, "https://b/vw")}}
	if _, err := b.listAll(t.Context(), agentsclient.ConnectionGVR); err == nil {
		t.Fatal("want an error when every shard fails")
	}
}

func TestListAllWithoutShards(t *testing.T) {
	b := &background{}
	if _, err := b.listAll(t.Context(), agentsclient.ConnectionGVR); err == nil {
		t.Fatal("want an error before any endpoint is discovered")
	}
	if b.ready() {
		t.Error("ready() must be false before any endpoint is discovered")
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

// TestRememberClusterIsConcurrencySafe: the poll loop rewrites the map while
// webhook and gateway callbacks read it.
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
