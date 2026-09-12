/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package apiexportprovider

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-logr/logr/testr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/multicluster-runtime/pkg/clusters"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"

	mcpcache "github.com/kcp-dev/multicluster-provider/pkg/cache"
	apisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"
)

type mockAware struct{}

func (m *mockAware) Engage(context.Context, multicluster.ClusterName, cluster.Cluster) error {
	return nil
}

// fakeWatcher stands in for the per-endpoint watcher. Each URL fails as many
// times as failures[url] says and then succeeds; every attempt is counted.
type fakeWatcher struct {
	mu       sync.Mutex
	failures map[string]int
	attempts map[string]int
	// onStopped keeps the callback of the last successful watcher per URL so a
	// test can simulate the endpoint's cache dying underneath it.
	onStopped map[string]func(error)
}

func newFakeWatcher() *fakeWatcher {
	return &fakeWatcher{failures: map[string]int{}, attempts: map[string]int{}, onStopped: map[string]func(error){}}
}

func (f *fakeWatcher) watch(ctx context.Context, cfg *rest.Config, _ multicluster.Aware, onStopped func(error)) (*watchedEndpoint, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.attempts[cfg.Host]++
	if f.failures[cfg.Host] > 0 {
		f.failures[cfg.Host]--
		return nil, errors.New("dial tcp 10.0.0.1:6443: connect: connection refused")
	}
	f.onStopped[cfg.Host] = onStopped
	_, cancel := context.WithCancel(ctx)
	return &watchedEndpoint{cancel: cancel}, nil
}

func (f *fakeWatcher) attemptsFor(url string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.attempts[url]
}

func (f *fakeWatcher) stoppedFor(url string) func(error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.onStopped[url]
}

// slice builds an APIExportEndpointSlice-shaped object publishing urls.
func slice(urls ...string) client.Object {
	endpoints := make([]any, 0, len(urls))
	for _, u := range urls {
		endpoints = append(endpoints, map[string]any{"url": u})
	}
	obj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apis.kcp.io/v1alpha1",
		"kind":       "APIExportEndpointSlice",
		"metadata":   map[string]any{"name": "code.faros.sh"},
		"status":     map[string]any{"endpoints": endpoints},
	}}
	return obj
}

func testProvider(t *testing.T, fw *fakeWatcher) *Provider {
	t.Helper()
	logger := testr.New(t)
	opts := Options{
		Scheme:          scheme.Scheme,
		Log:             &logger,
		RetryBackoff:    time.Millisecond,
		RetryBackoffCap: 4 * time.Millisecond,
	}
	opts.defaults()
	p := &Provider{
		opts:              opts,
		config:            &rest.Config{Host: "https://kcp.example.com"},
		sliceName:         "code.faros.sh",
		Clusters:          clusters.New[cluster.Cluster](),
		aggregateCache:    mcpcache.NewAggregateCache(),
		watched:           map[string]*watchedEndpoint{},
		pending:           map[string]*retryState{},
		watchEndpointFunc: fw.watch,
	}
	// The tests drive endpointSliceUpdate directly; mark the provider as
	// started so Check reflects the endpoint state rather than "not started".
	p.started = true
	return p
}

func (p *Provider) snapshot() (watched, pending []string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for u := range p.watched {
		watched = append(watched, u)
	}
	for u := range p.pending {
		pending = append(pending, u)
	}
	return watched, pending
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

const (
	url1 = "https://kcp.example.com/services/apiexport/root/export-1"
	url2 = "https://kcp.example.com/services/apiexport/root/export-2"
)

// The success path is the upstream provider's: URLs are watched once each,
// stopped when they disappear, and re-watched when they come back.
func TestEndpointSliceUpdateTracksPublishedURLs(t *testing.T) {
	fw := newFakeWatcher()
	p := testProvider(t, fw)
	aware := &mockAware{}

	p.endpointSliceUpdate(t.Context(), aware, slice())
	if w, _ := p.snapshot(); len(w) != 0 {
		t.Fatalf("no URLs published, yet watching %v", w)
	}

	p.endpointSliceUpdate(t.Context(), aware, slice(url1, url2))
	if w, _ := p.snapshot(); len(w) != 2 {
		t.Fatalf("watching %v, want both URLs", w)
	}
	p.endpointSliceUpdate(t.Context(), aware, slice(url1, url2))
	if fw.attemptsFor(url1) != 1 || fw.attemptsFor(url2) != 1 {
		t.Errorf("a repeated slice event re-watched URLs: %d/%d attempts", fw.attemptsFor(url1), fw.attemptsFor(url2))
	}

	p.endpointSliceUpdate(t.Context(), aware, slice(url1))
	if w, _ := p.snapshot(); len(w) != 1 || w[0] != url1 {
		t.Fatalf("after dropping url2 watching %v", w)
	}

	p.endpointSliceUpdate(t.Context(), aware, slice(url1, url2))
	if w, _ := p.snapshot(); len(w) != 2 {
		t.Fatalf("re-added URL not re-watched: %v", w)
	}
	if fw.attemptsFor(url2) != 2 {
		t.Errorf("url2 attempts = %d, want 2 (initial + after re-add)", fw.attemptsFor(url2))
	}
}

// The incident: the watcher fails on its first attempt (kcp still coming
// back, connection refused). Upstream logs once and never looks again; here
// the URL is retried until it succeeds and readiness says so meanwhile.
func TestFailedEndpointWatchIsRetriedUntilItSucceeds(t *testing.T) {
	fw := newFakeWatcher()
	fw.failures[url1] = 3
	p := testProvider(t, fw)
	aware := &mockAware{}

	p.endpointSliceUpdate(t.Context(), aware, slice(url1))

	w, pending := p.snapshot()
	if len(w) != 0 || len(pending) != 1 {
		t.Fatalf("after a failed first attempt: watched=%v pending=%v", w, pending)
	}
	err := p.Check()
	if err == nil {
		t.Fatal("a provider whose only endpoint watcher failed reported ready")
	}
	for _, want := range []string{url1, "connection refused", "will not reconcile"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("readiness reason omits %q: %v", want, err)
		}
	}

	eventually(t, "the endpoint to be watched after retries", func() bool {
		w, _ := p.snapshot()
		return len(w) == 1
	})
	if got := fw.attemptsFor(url1); got != 4 {
		t.Errorf("attempts = %d, want 4 (3 failures + 1 success)", got)
	}
	if _, pending := p.snapshot(); len(pending) != 0 {
		t.Errorf("succeeded URL still pending: %v", pending)
	}
	if err := p.Check(); err != nil {
		t.Errorf("ready after the watcher succeeded, got: %v", err)
	}
}

// A URL the slice stops publishing while it is failing must not keep being
// retried: the platform moved the shard, there is nothing to reach any more.
func TestRetryStopsWhenURLIsUnpublished(t *testing.T) {
	fw := newFakeWatcher()
	fw.failures[url1] = 1_000_000
	p := testProvider(t, fw)
	aware := &mockAware{}

	p.endpointSliceUpdate(t.Context(), aware, slice(url1))
	eventually(t, "at least two attempts", func() bool { return fw.attemptsFor(url1) >= 2 })

	p.endpointSliceUpdate(t.Context(), aware, slice())
	if _, pending := p.snapshot(); len(pending) != 0 {
		t.Fatalf("unpublished URL still pending: %v", pending)
	}
	settled := fw.attemptsFor(url1)
	time.Sleep(20 * time.Millisecond)
	if got := fw.attemptsFor(url1); got != settled {
		t.Errorf("retries continued after the URL was unpublished: %d -> %d", settled, got)
	}
	if err := p.Check(); err == nil || !strings.Contains(err.Error(), "no endpoints") {
		t.Errorf("slice with no endpoints should report unready, got: %v", err)
	}
}

// A watcher whose cache exits while the URL is still published is
// re-established rather than silently gone.
func TestLostEndpointIsReestablished(t *testing.T) {
	fw := newFakeWatcher()
	p := testProvider(t, fw)
	aware := &mockAware{}

	p.endpointSliceUpdate(t.Context(), aware, slice(url1))
	stopped := fw.stoppedFor(url1)
	if stopped == nil {
		t.Fatal("watcher did not receive an onStopped callback")
	}

	fw.mu.Lock()
	fw.failures[url1] = 1
	fw.mu.Unlock()
	stopped(errors.New("watch closed"))

	// Immediately after the loss nothing is watched and readiness says why.
	w, pending := p.snapshot()
	if len(w) != 0 || len(pending) != 1 {
		t.Fatalf("after the cache stopped: watched=%v pending=%v", w, pending)
	}
	if err := p.Check(); err == nil || !strings.Contains(err.Error(), "watch closed") {
		t.Errorf("lost endpoint not reported: %v", err)
	}

	eventually(t, "the endpoint to be re-established", func() bool {
		w, _ := p.snapshot()
		return len(w) == 1
	})
	if got := fw.attemptsFor(url1); got != 3 {
		t.Errorf("attempts = %d, want 3 (initial, failed re-establish, success)", got)
	}
	if err := p.Check(); err != nil {
		t.Errorf("ready after re-establishing, got: %v", err)
	}
}

func TestBackoffDoublesAndCaps(t *testing.T) {
	p := &Provider{opts: Options{RetryBackoff: time.Second, RetryBackoffCap: 30 * time.Second}}
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, 30 * time.Second, 30 * time.Second}
	for i, d := range want {
		if got := p.backoff(i + 1); got != d {
			t.Errorf("backoff(%d) = %s, want %s", i+1, got, d)
		}
	}
	// Overflow guard: a huge attempt count must still return the cap.
	if got := p.backoff(200); got != 30*time.Second {
		t.Errorf("backoff(200) = %s, want the cap", got)
	}
}

func TestCheckReportsStartupStates(t *testing.T) {
	fw := newFakeWatcher()
	p := testProvider(t, fw)
	p.started = false
	if err := p.Check(); err == nil || !strings.Contains(err.Error(), "not started") {
		t.Errorf("before Start: %v", err)
	}
	p.started = true
	if err := p.Check(); err == nil || !strings.Contains(err.Error(), "code.faros.sh") {
		t.Errorf("before the slice is seen: %v", err)
	}
	p.endpointSliceUpdate(t.Context(), &mockAware{}, slice())
	if err := p.Check(); err == nil || !strings.Contains(err.Error(), "no endpoints") {
		t.Errorf("slice without endpoints: %v", err)
	}
	p.endpointSliceUpdate(t.Context(), &mockAware{}, slice(url1))
	if err := p.Check(); err != nil {
		t.Errorf("one watched endpoint: %v", err)
	}
	p.stopped = true
	if err := p.Check(); err == nil || !strings.Contains(err.Error(), "stopped") {
		t.Errorf("after Start returned: %v", err)
	}
}

// Start's context ending must stop pending retries; a timer firing into a
// torn-down provider would otherwise start a watcher nobody stops.
func TestPendingRetriesStopWhenContextEnds(t *testing.T) {
	fw := newFakeWatcher()
	fw.failures[url1] = 1_000_000
	p := testProvider(t, fw)
	ctx, cancel := context.WithCancel(t.Context())

	p.endpointSliceUpdate(ctx, &mockAware{}, slice(url1))
	eventually(t, "a retry", func() bool { return fw.attemptsFor(url1) >= 2 })
	cancel()
	time.Sleep(10 * time.Millisecond)
	settled := fw.attemptsFor(url1)
	time.Sleep(20 * time.Millisecond)
	if got := fw.attemptsFor(url1); got != settled {
		t.Errorf("retries continued after the context ended: %d -> %d", settled, got)
	}
}

// Options.defaults must fill in what the providers relied on from upstream
// apiexport.New — the APIBinding readiness filters and a logger — plus the
// retry knobs. New itself needs a reachable control plane (the cache's REST
// mapper discovers eagerly), so it is exercised by the e2e suites.
func TestOptionsDefaults(t *testing.T) {
	s := runtime.NewScheme()
	if err := apisv1alpha1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	opts := Options{Scheme: s}
	opts.defaults()
	if opts.RetryBackoff != DefaultRetryBackoff || opts.RetryBackoffCap != DefaultRetryBackoffCap {
		t.Errorf("retry defaults = %s/%s", opts.RetryBackoff, opts.RetryBackoffCap)
	}
	if opts.AddFilter == nil || opts.UpdateFilter == nil {
		t.Error("APIBinding readiness filters not defaulted")
	}
	if opts.Log == nil || opts.Scheme != s {
		t.Error("logger or scheme not defaulted as expected")
	}
	if _, ok := opts.ObjectToWatch.(*apisv1alpha1.APIBinding); !ok {
		t.Errorf("ObjectToWatch = %T, want *APIBinding", opts.ObjectToWatch)
	}

	// A cap below the base is raised to it so the sequence never shrinks.
	inverted := Options{RetryBackoff: 10 * time.Second, RetryBackoffCap: time.Second}
	inverted.defaults()
	if inverted.RetryBackoffCap != 10*time.Second {
		t.Errorf("cap below base kept: %s", inverted.RetryBackoffCap)
	}
	var _ metav1.Object = slice() // keep the fixture honest about its shape
}
