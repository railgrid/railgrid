/*
Copyright 2026 The Faros Authors.

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

// Package vwhealth reports whether a provider can reach the APIExport virtual
// workspace it watches, and turns that into readiness.
//
// Every provider that serves an API to tenant workspaces watches them through
// the URL kcp publishes in APIExportEndpointSlice.status.endpoints[]. That URL
// is copied from Shard.spec.virtualWorkspaceURL — a property of the PLATFORM,
// not of the provider. A provider running somewhere the platform did not
// anticipate (a tenant's own cluster, a kind pod) can therefore find it
// unreachable through no fault of its own: a `127.0.0.1` address means the
// provider's own pod, and an in-cluster service DNS name does not resolve at
// all from outside that cluster.
//
// The failure is silent, which is why this package exists rather than a log
// line. A provider's own resources live in its own workspace and are reached
// over its provider kubeconfig — a different URL that keeps working — so the
// process starts, serves its API, wins leader election, reconciles what is
// local to it, and simply never acts on anything in a tenant workspace. Health
// checks pass throughout. On a real deployment that went unnoticed for days,
// and the eventual symptom pointed nowhere near the cause.
//
// Probing the address, rather than inspecting the multicluster manager, keeps
// the check honest about the thing that actually breaks — whether THIS process
// can reach THAT URL — and does not depend on library internals, which log
// watch failures without surfacing them.
package vwhealth

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
)

var endpointSliceGVR = schema.GroupVersionResource{
	Group: "apis.kcp.io", Version: "v1alpha1", Resource: "apiexportendpointslices",
}

// DefaultInterval is how often Watch re-probes. Slow on purpose: this detects a
// misconfiguration that persists until someone changes the platform, not a
// flapping dependency.
const DefaultInterval = 60 * time.Second

// Readiness holds the last probe result. The zero value is usable and reports
// ready. Safe for concurrent use: the prober writes, HTTP handlers read.
type Readiness struct {
	mu      sync.RWMutex
	checked bool
	url     string
	err     error
}

func (r *Readiness) set(url string, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.checked, r.url, r.err = true, url, err
}

// Check reports why the provider is not ready, or nil.
//
// Before the first probe completes it reports ready. Readiness gates traffic,
// and a provider that has not finished starting must not be marked broken — the
// interesting state is a probe that ran and failed. A later success clears it,
// so a transient blip does not pin a provider unready until someone notices.
func (r *Readiness) Check() error {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if !r.checked || r.err == nil {
		return nil
	}
	return fmt.Errorf("cannot reach the APIExport virtual workspace at %s: %w — "+
		"resources in tenant workspaces will not reconcile. That address comes from the "+
		"platform's Shard.spec.virtualWorkspaceURL and must be reachable from where this "+
		"provider runs", r.url, r.err)
}

// Handler serves a readiness endpoint for r: 200 when ready, 503 with the
// reason otherwise. Mount it at /readyz and point the provider's CatalogEntry
// backend.healthPath there, so the hub's BackendHealthy reflects it.
//
// Do NOT mount this as liveness. A provider in this state is still serving its
// API and still reconciling its own workspace, so restarting it would take away
// the work it is still doing.
func Handler(r *Readiness) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if err := r.Check(); err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			_ = json.NewEncoder(w).Encode(map[string]string{"status": "unready", "reason": err.Error()})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})
}

// Watch probes every interval until ctx ends, recording the result in r.
//
// Never fatal. An unreachable virtual workspace is a condition to report, not a
// reason to stop serving — see the package comment. interval <= 0 uses
// DefaultInterval. A nil cfg or r makes this a no-op, so a provider running
// without kcp (dev, REST-only) can call it unconditionally.
func Watch(ctx context.Context, cfg *rest.Config, exportName string, r *Readiness, interval time.Duration) {
	if cfg == nil || r == nil || exportName == "" {
		return
	}
	if interval <= 0 {
		interval = DefaultInterval
	}
	for {
		url, err := Probe(ctx, cfg, exportName)
		r.set(url, err)
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
	}
}

// Probe resolves the provider's advertised virtual-workspace URL and checks that
// this process can reach it. Returns the URL it tried, so a caller can report
// the address even when the attempt failed.
func Probe(ctx context.Context, cfg *rest.Config, exportName string) (string, error) {
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return "", fmt.Errorf("building client: %w", err)
	}
	slice, err := dyn.Resource(endpointSliceGVR).Get(ctx, exportName, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("reading APIExportEndpointSlice %s: %w", exportName, err)
	}
	url, err := FirstEndpointURL(slice.Object, exportName)
	if err != nil {
		return "", err
	}

	// rest.TransportFor carries both the TLS config and the credentials, so the
	// probe fails for the same reasons the manager would and no others.
	rt, err := rest.TransportFor(cfg)
	if err != nil {
		return url, fmt.Errorf("building probe transport: %w", err)
	}
	// Discovery on the wildcard path — the same shape the multicluster manager
	// opens, so reachability here means reachability for it.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(url, "/")+"/clusters/*/api", nil)
	if err != nil {
		return url, fmt.Errorf("building probe request: %w", err)
	}
	resp, err := (&http.Client{Transport: rt, Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		// DNS failure, connection refused, TLS mismatch — the whole class this
		// exists to catch.
		return url, err
	}
	defer func() { _ = resp.Body.Close() }()
	// Any answer means the address resolves and something is serving it. A 401
	// or 403 is a credential problem, which is real but different, and not this
	// probe's business to judge.
	if resp.StatusCode >= 500 {
		return url, fmt.Errorf("endpoint returned %s", resp.Status)
	}
	return url, nil
}

// FirstEndpointURL extracts the first published endpoint URL from an
// APIExportEndpointSlice. Exported for testing and for callers that already
// hold the object.
func FirstEndpointURL(obj map[string]any, exportName string) (string, error) {
	endpoints, _, _ := unstructured.NestedSlice(obj, "status", "endpoints")
	if len(endpoints) == 0 {
		// Normal right after install: kcp has not reconciled the slice yet.
		return "", fmt.Errorf("APIExportEndpointSlice %s publishes no endpoints yet", exportName)
	}
	first, _ := endpoints[0].(map[string]any)
	url, _ := first["url"].(string)
	if url == "" {
		return "", fmt.Errorf("APIExportEndpointSlice %s publishes an empty endpoint URL", exportName)
	}
	return url, nil
}
