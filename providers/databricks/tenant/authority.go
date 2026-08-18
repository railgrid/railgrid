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
	"fmt"
	"strings"
	"sync"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	databricksv1alpha1 "github.com/faroshq/provider-databricks/apis/databricks/v1alpha1"
)

// SliceAuthority resolves tenant logical clusters through the APIExport
// virtual-workspace URLs published on the provider's APIExportEndpointSlice,
// authenticated with the provider's own credential. It replaces the previous
// design where the running multicluster manager doubled as the serving path's
// cluster resolver — serving no longer depends on the controller lifecycle,
// which is what lets the controller manager be leader-elected and the
// provider scale horizontally.
//
// Reads through the returned clients are live GETs against the VW (no
// informer cache): slightly slower per action than the old cached reads, but
// with no warm-up, no engagement precondition, and no per-replica informer
// memory. A tenant whose APIBinding is not yet Ready gets kcp's 403/404
// instead of the old "cluster unavailable".
type SliceAuthority struct {
	base      *rest.Config
	sliceName string
	scheme    *runtime.Scheme

	// getSlice, probe, and newClient are injectable for tests; production
	// wiring is installed by NewSliceAuthority.
	getSlice  func(ctx context.Context) (*unstructured.Unstructured, error)
	probe     func(ctx context.Context, shardURL, clusterID string) bool
	newClient func(cfg *rest.Config) (client.Client, error)
	now       func() time.Time

	mu           sync.RWMutex
	shardURLs    []string
	clusterShard map[string]string
	clients      map[string]client.Client
	lastRefresh  time.Time
}

// sliceRefreshInterval bounds how often a Ready()/miss re-reads the endpoint
// slice. kcp publishes one endpoint per shard and the set changes only on
// shard topology changes, so a short cache is plenty.
const sliceRefreshInterval = 10 * time.Second

// authorityClientCacheCap bounds the per-cluster client cache. Clients hold a
// lazy RESTMapper (one discovery round-trip each), so they are worth keeping,
// but the set must not grow unbounded with tenant churn. On overflow the whole
// map is dropped — rebuilding is cheap and keeps the bookkeeping trivial.
const authorityClientCacheCap = 256

var errNoEndpoints = errors.New("APIExportEndpointSlice has no endpoint with a url yet")

var apiExportEndpointSliceGVR = schema.GroupVersionResource{
	Group: "apis.kcp.io", Version: "v1alpha1", Resource: "apiexportendpointslices",
}

// NewSliceAuthority builds the production authority: the slice is read from
// the provider workspace base config, shards are probed with a bounded Table
// list, and clients are controller-runtime clients over the given scheme.
func NewSliceAuthority(base *rest.Config, sliceName string, scheme *runtime.Scheme) (*SliceAuthority, error) {
	if base == nil {
		return nil, errors.New("provider kubeconfig is required")
	}
	dyn, err := dynamic.NewForConfig(base)
	if err != nil {
		return nil, fmt.Errorf("slice client: %w", err)
	}
	a := &SliceAuthority{
		base:      rest.CopyConfig(base),
		sliceName: sliceName,
		scheme:    scheme,
		getSlice: func(ctx context.Context) (*unstructured.Unstructured, error) {
			return dyn.Resource(apiExportEndpointSliceGVR).Get(ctx, sliceName, metav1.GetOptions{})
		},
		now: time.Now,
	}
	a.probe = a.probeShard
	a.newClient = func(cfg *rest.Config) (client.Client, error) {
		return client.New(cfg, client.Options{Scheme: scheme})
	}
	a.clusterShard = map[string]string{}
	a.clients = map[string]client.Client{}
	return a, nil
}

// Ready reports whether at least one virtual-workspace endpoint is known —
// the ClientFactory's readiness probe. It refreshes opportunistically so
// readiness converges without any background goroutine.
func (a *SliceAuthority) Ready() bool {
	if a == nil {
		return false
	}
	a.mu.RLock()
	known := len(a.shardURLs) > 0
	a.mu.RUnlock()
	if known {
		return true
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return a.refreshShards(ctx, true) == nil
}

// ClusterClient returns a provider-credential client scoped to one tenant
// logical cluster, resolving which shard's VW serves it.
func (a *SliceAuthority) ClusterClient(ctx context.Context, clusterID string) (client.Client, error) {
	if a == nil {
		return nil, errors.New("provider authority client unavailable")
	}
	shardURL, err := a.shardFor(ctx, clusterID)
	if err != nil {
		return nil, err
	}
	key := shardURL + "|" + clusterID
	a.mu.RLock()
	cl, ok := a.clients[key]
	a.mu.RUnlock()
	if ok {
		return cl, nil
	}
	cfg := rest.CopyConfig(a.base)
	cfg.Host = shardURL + "/clusters/" + clusterID
	cl, err = a.newClient(cfg)
	if err != nil {
		return nil, fmt.Errorf("provider authority client for cluster %q: %w", clusterID, err)
	}
	a.mu.Lock()
	if len(a.clients) >= authorityClientCacheCap {
		a.clients = map[string]client.Client{}
	}
	a.clients[key] = cl
	a.mu.Unlock()
	return cl, nil
}

// shardFor resolves which shard's VW serves a tenant cluster: answered from
// cache once known, shortcut when there is a single shard, otherwise probed —
// a shard that does not host the logical cluster rejects the read, the one
// that does answers (an empty list is still an answer).
func (a *SliceAuthority) shardFor(ctx context.Context, clusterID string) (string, error) {
	a.mu.RLock()
	url, cached := a.clusterShard[clusterID]
	shards := append([]string(nil), a.shardURLs...)
	a.mu.RUnlock()
	if cached {
		return url, nil
	}
	if len(shards) == 0 {
		if err := a.refreshShards(ctx, true); err != nil {
			return "", err
		}
		a.mu.RLock()
		shards = append([]string(nil), a.shardURLs...)
		a.mu.RUnlock()
	}
	switch len(shards) {
	case 0:
		return "", errNoEndpoints
	case 1:
		return shards[0], nil
	}
	for _, shardURL := range shards {
		if a.probe(ctx, shardURL, clusterID) {
			a.mu.Lock()
			a.clusterShard[clusterID] = shardURL
			a.mu.Unlock()
			return shardURL, nil
		}
	}
	return "", fmt.Errorf("tenant workspace %q is not served by any of the %d APIExport virtual workspace endpoints", clusterID, len(shards))
}

func (a *SliceAuthority) probeShard(ctx context.Context, shardURL, clusterID string) bool {
	cfg := rest.CopyConfig(a.base)
	cfg.Host = shardURL + "/clusters/" + clusterID
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return false
	}
	gvr := databricksv1alpha1.SchemeGroupVersion.WithResource("tables")
	_, err = dyn.Resource(gvr).List(ctx, metav1.ListOptions{Limit: 1})
	return err == nil
}

// refreshShards re-reads the endpoint slice. It is rate-limited unless
// force is set (a miss forces it so a fresh shard is picked up immediately);
// bindings whose shard disappeared are forgotten so they re-resolve instead
// of pinning a dead URL — same shape as the agents provider's ensureVW.
func (a *SliceAuthority) refreshShards(ctx context.Context, force bool) error {
	a.mu.RLock()
	last := a.lastRefresh
	a.mu.RUnlock()
	if !force && a.now().Sub(last) < sliceRefreshInterval {
		return nil
	}
	obj, err := a.getSlice(ctx)
	if err != nil {
		return fmt.Errorf("reading APIExportEndpointSlice %q: %w", a.sliceName, err)
	}
	urls := SliceEndpointURLs(obj)
	if len(urls) == 0 {
		return errNoEndpoints
	}
	seen := make(map[string]bool, len(urls))
	for _, u := range urls {
		seen[u] = true
	}
	a.mu.Lock()
	a.shardURLs = urls
	a.lastRefresh = a.now()
	for cluster, shardURL := range a.clusterShard {
		if !seen[shardURL] {
			delete(a.clusterShard, cluster)
		}
	}
	for key := range a.clients {
		if shardURL, _, ok := strings.Cut(key, "|"); ok && !seen[shardURL] {
			delete(a.clients, key)
		}
	}
	a.mu.Unlock()
	return nil
}

// SliceEndpointURLs reads EVERY endpoint URL off an APIExportEndpointSlice,
// trimmed and de-duplicated in slice order. kcp publishes one endpoint per
// shard, so taking only the first would silently drop every tenant workspace
// bound on the other shards.
func SliceEndpointURLs(u *unstructured.Unstructured) []string {
	endpoints, _, _ := unstructured.NestedSlice(u.Object, "status", "endpoints")
	urls := make([]string, 0, len(endpoints))
	seen := map[string]bool{}
	for _, e := range endpoints {
		m, _ := e.(map[string]any)
		raw, _ := m["url"].(string)
		trimmed := strings.TrimRight(strings.TrimSpace(raw), "/")
		if trimmed == "" || seen[trimmed] {
			continue
		}
		seen[trimmed] = true
		urls = append(urls, trimmed)
	}
	return urls
}
