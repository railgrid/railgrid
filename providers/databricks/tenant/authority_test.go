// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package tenant

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	fakeclient "sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func sliceWithEndpoints(urls ...string) *unstructured.Unstructured {
	endpoints := make([]any, 0, len(urls))
	for _, u := range urls {
		endpoints = append(endpoints, map[string]any{"url": u})
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"status": map[string]any{"endpoints": endpoints},
	}}
}

// testAuthority builds a SliceAuthority with every external dependency faked.
func testAuthority(getSlice func(context.Context) (*unstructured.Unstructured, error)) *SliceAuthority {
	a := &SliceAuthority{
		base:         &rest.Config{Host: "https://kcp.example/clusters/root:faros:providers:databricks"},
		sliceName:    "databricks.providers.faros.sh",
		getSlice:     getSlice,
		now:          time.Now,
		clusterShard: map[string]string{},
		clients:      map[string]client.Client{},
	}
	a.newClient = func(cfg *rest.Config) (client.Client, error) {
		_ = cfg
		return fakeclient.NewClientBuilder().Build(), nil
	}
	a.probe = func(context.Context, string, string) bool { return false }
	return a
}

func TestSliceEndpointURLsDeduplicatesAndTrims(t *testing.T) {
	urls := SliceEndpointURLs(sliceWithEndpoints(
		" https://shard-a.example/services/apiexport/a/x/ ",
		"https://shard-a.example/services/apiexport/a/x",
		"",
		"https://shard-b.example/services/apiexport/b/x",
	))
	if len(urls) != 2 || urls[0] != "https://shard-a.example/services/apiexport/a/x" || urls[1] != "https://shard-b.example/services/apiexport/b/x" {
		t.Fatalf("SliceEndpointURLs = %v, want the two deduplicated shard URLs in order", urls)
	}
}

func TestSliceAuthorityReadyRequiresAnEndpoint(t *testing.T) {
	a := testAuthority(func(context.Context) (*unstructured.Unstructured, error) {
		return sliceWithEndpoints(), nil
	})
	if a.Ready() {
		t.Fatal("authority with no endpoint URLs reported ready")
	}
	a.getSlice = func(context.Context) (*unstructured.Unstructured, error) {
		return sliceWithEndpoints("https://shard-a.example/services/apiexport/a/x"), nil
	}
	if !a.Ready() {
		t.Fatal("authority with a published endpoint URL reported not ready")
	}
	// Once discovered, readiness answers from cache without re-reading.
	a.getSlice = func(context.Context) (*unstructured.Unstructured, error) {
		return nil, errors.New("slice read must not happen")
	}
	if !a.Ready() {
		t.Fatal("authority forgot its discovered endpoints")
	}
}

func TestSliceAuthoritySingleShardSkipsProbe(t *testing.T) {
	a := testAuthority(func(context.Context) (*unstructured.Unstructured, error) {
		return sliceWithEndpoints("https://shard-a.example/services/apiexport/a/x"), nil
	})
	var hosts []string
	a.newClient = func(cfg *rest.Config) (client.Client, error) {
		hosts = append(hosts, cfg.Host)
		return fakeclient.NewClientBuilder().Build(), nil
	}
	a.probe = func(context.Context, string, string) bool {
		t.Fatal("single-shard resolution must not probe")
		return false
	}
	cl, err := a.ClusterClient(context.Background(), "tenant-cluster-1")
	if err != nil || cl == nil {
		t.Fatalf("ClusterClient: %v", err)
	}
	if len(hosts) != 1 || hosts[0] != "https://shard-a.example/services/apiexport/a/x/clusters/tenant-cluster-1" {
		t.Fatalf("client host = %v, want the shard VW URL scoped to the tenant cluster", hosts)
	}
	// Second call is served from the client cache.
	if _, err := a.ClusterClient(context.Background(), "tenant-cluster-1"); err != nil {
		t.Fatalf("cached ClusterClient: %v", err)
	}
	if len(hosts) != 1 {
		t.Fatalf("client rebuilt despite cache: %d constructions", len(hosts))
	}
}

func TestSliceAuthorityProbesShardsAndCachesTheAnswer(t *testing.T) {
	a := testAuthority(func(context.Context) (*unstructured.Unstructured, error) {
		return sliceWithEndpoints(
			"https://shard-a.example/services/apiexport/a/x",
			"https://shard-b.example/services/apiexport/b/x",
		), nil
	})
	probes := 0
	a.probe = func(_ context.Context, shardURL, clusterID string) bool {
		probes++
		return strings.HasPrefix(shardURL, "https://shard-b") && clusterID == "tenant-2"
	}
	cl, err := a.ClusterClient(context.Background(), "tenant-2")
	if err != nil || cl == nil {
		t.Fatalf("ClusterClient across shards: %v", err)
	}
	if probes == 0 {
		t.Fatal("multi-shard resolution did not probe")
	}
	probes = 0
	if _, err := a.ClusterClient(context.Background(), "tenant-2"); err != nil {
		t.Fatalf("cached multi-shard ClusterClient: %v", err)
	}
	if probes != 0 {
		t.Fatal("shard binding was not cached")
	}
}

func TestSliceAuthorityUnservedClusterFailsLoudly(t *testing.T) {
	a := testAuthority(func(context.Context) (*unstructured.Unstructured, error) {
		return sliceWithEndpoints(
			"https://shard-a.example/services/apiexport/a/x",
			"https://shard-b.example/services/apiexport/b/x",
		), nil
	})
	if _, err := a.ClusterClient(context.Background(), "nowhere"); err == nil {
		t.Fatal("cluster served by no shard must fail loudly, not guess a shard")
	}
}

func TestSliceAuthorityForgetsBindingsForRemovedShards(t *testing.T) {
	current := sliceWithEndpoints(
		"https://shard-a.example/services/apiexport/a/x",
		"https://shard-b.example/services/apiexport/b/x",
	)
	a := testAuthority(func(context.Context) (*unstructured.Unstructured, error) { return current, nil })
	a.probe = func(_ context.Context, shardURL, _ string) bool {
		return strings.HasPrefix(shardURL, "https://shard-b")
	}
	if _, err := a.ClusterClient(context.Background(), "tenant-2"); err != nil {
		t.Fatalf("initial resolution: %v", err)
	}
	// Shard B disappears; a forced refresh must drop its binding and client.
	current = sliceWithEndpoints("https://shard-a.example/services/apiexport/a/x")
	if err := a.refreshShards(context.Background(), true); err != nil {
		t.Fatalf("refreshShards: %v", err)
	}
	a.mu.RLock()
	_, bound := a.clusterShard["tenant-2"]
	clients := len(a.clients)
	a.mu.RUnlock()
	if bound || clients != 0 {
		t.Fatalf("dead-shard state retained: bound=%v clients=%d", bound, clients)
	}
}
