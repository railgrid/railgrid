/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type previewEdgeObservedContext struct {
	context.Context
	observed chan struct{}
	once     sync.Once
}

func (c *previewEdgeObservedContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.observed) })
	return c.Context.Done()
}

// TestPreviewEdgeReadyGatesAndCaches pins the edge-readiness contract: a
// failing probe keeps the preview not-ready, a succeeding probe flips it, and
// the success is cached briefly. The bounded cache avoids probe latency on
// tight polls without hiding a later runtime restart or regression.
func TestPreviewEdgeReadyGatesAndCaches(t *testing.T) {
	s := &Server{}
	probeErr := errors.New("tls handshake failure")
	calls := 0
	s.SetPreviewEdgeProbe(func(_ context.Context, _ string) error {
		calls++
		return probeErr
	})

	const url = "https://demo-abc.apps.example.com"
	if s.previewEdgeReady(context.Background(), url) {
		t.Fatal("edge reported ready while the probe fails")
	}
	if s.previewEdgeReady(context.Background(), url) {
		t.Fatal("edge reported ready while the probe still fails")
	}
	if calls != 2 {
		t.Fatalf("failing probe must be retried every call; got %d calls", calls)
	}

	// Edge provisioned: probe succeeds once, then the cache answers.
	s.SetPreviewEdgeProbe(func(_ context.Context, _ string) error { return nil })
	if !s.previewEdgeReady(context.Background(), url) {
		t.Fatal("edge not ready after successful probe")
	}
	s.SetPreviewEdgeProbe(func(_ context.Context, _ string) error {
		t.Fatal("probe called for a URL already known ready")
		return nil
	})
	if !s.previewEdgeReady(context.Background(), url) {
		t.Fatal("cached readiness lost")
	}
	s.edgeReadyURLs.Store(url, time.Now().Add(-previewEdgeReadyTTL-time.Second))
	s.SetPreviewEdgeProbe(func(_ context.Context, _ string) error { return probeErr })
	if s.previewEdgeReady(context.Background(), url) {
		t.Fatal("expired readiness cache hid a later probe failure")
	}

	// A different URL starts cold.
	s.SetPreviewEdgeProbe(func(_ context.Context, _ string) error { return probeErr })
	if s.previewEdgeReady(context.Background(), "https://other.apps.example.com") {
		t.Fatal("readiness cache leaked across URLs")
	}
}

func TestPreviewEdgeReadyCoalescesConcurrentProbes(t *testing.T) {
	s := &Server{}
	const url = "https://demo-abc.apps.example.com"
	var calls atomic.Int32
	probeStarted := make(chan struct{})
	release := make(chan struct{})
	s.SetPreviewEdgeProbe(func(ctx context.Context, _ string) error {
		if calls.Add(1) == 1 {
			close(probeStarted)
		}
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})

	leaderResult := make(chan bool, 1)
	go func() { leaderResult <- s.previewEdgeReady(context.Background(), url) }()
	waitForPreviewEdgeSignal(t, probeStarted, "probe start")

	const waiterCount = 8
	waiterResults := make(chan bool, waiterCount)
	observed := make([]chan struct{}, waiterCount)
	for i := range observed {
		observed[i] = make(chan struct{})
		waiterContext := &previewEdgeObservedContext{
			Context:  context.Background(),
			observed: observed[i],
		}
		go func(ctx context.Context) { waiterResults <- s.previewEdgeReady(ctx, url) }(waiterContext)
	}
	for i := range observed {
		waitForPreviewEdgeSignal(t, observed[i], "waiter registration")
	}

	close(release)
	if !<-leaderResult {
		t.Fatal("leader did not observe the successful shared probe")
	}
	for range waiterCount {
		if !<-waiterResults {
			t.Fatal("waiter did not observe the successful shared probe")
		}
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("same-URL concurrent probes = %d, want exactly one", got)
	}
}

func TestPreviewEdgeReadyCoalescesFailureWithoutCachingIt(t *testing.T) {
	s := &Server{}
	const url = "https://demo-abc.apps.example.com"
	probeErr := errors.New("edge is still provisioning")
	var calls atomic.Int32
	probeStarted := make(chan struct{})
	release := make(chan struct{})
	s.SetPreviewEdgeProbe(func(ctx context.Context, _ string) error {
		if calls.Add(1) == 1 {
			close(probeStarted)
			select {
			case <-release:
				return probeErr
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return nil
	})

	leaderResult := make(chan bool, 1)
	go func() { leaderResult <- s.previewEdgeReady(context.Background(), url) }()
	waitForPreviewEdgeSignal(t, probeStarted, "probe start")

	const waiterCount = 4
	waiterResults := make(chan bool, waiterCount)
	observed := make([]chan struct{}, waiterCount)
	for i := range observed {
		observed[i] = make(chan struct{})
		waiterContext := &previewEdgeObservedContext{
			Context:  context.Background(),
			observed: observed[i],
		}
		go func(ctx context.Context) { waiterResults <- s.previewEdgeReady(ctx, url) }(waiterContext)
	}
	for i := range observed {
		waitForPreviewEdgeSignal(t, observed[i], "waiter registration")
	}

	close(release)
	if <-leaderResult {
		t.Fatal("leader reported ready after a failed shared probe")
	}
	for range waiterCount {
		if <-waiterResults {
			t.Fatal("waiter reported ready after a failed shared probe")
		}
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("same-URL failed concurrent probes = %d, want exactly one", got)
	}
	if !s.previewEdgeReady(context.Background(), url) {
		t.Fatal("failed probe was cached or the retry did not run")
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("retry after failed flight probes = %d, want two total probes", got)
	}
}

func TestPreviewEdgeReadyCanceledWaiterDoesNotCancelSharedProbe(t *testing.T) {
	s := &Server{}
	const url = "https://demo-abc.apps.example.com"
	var calls atomic.Int32
	probeStarted := make(chan struct{})
	release := make(chan struct{})
	probeCanceled := make(chan struct{})
	s.SetPreviewEdgeProbe(func(ctx context.Context, _ string) error {
		calls.Add(1)
		close(probeStarted)
		select {
		case <-release:
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return nil
		case <-ctx.Done():
			close(probeCanceled)
			return ctx.Err()
		}
	})

	leaderResult := make(chan bool, 1)
	go func() { leaderResult <- s.previewEdgeReady(context.Background(), url) }()
	waitForPreviewEdgeSignal(t, probeStarted, "probe start")

	waiterObserved := make(chan struct{})
	waiterContext, cancelWaiter := context.WithCancel(context.Background())
	waiter := &previewEdgeObservedContext{Context: waiterContext, observed: waiterObserved}
	waiterResult := make(chan bool, 1)
	go func() { waiterResult <- s.previewEdgeReady(waiter, url) }()
	waitForPreviewEdgeSignal(t, waiterObserved, "waiter registration")
	cancelWaiter()
	if <-waiterResult {
		t.Fatal("canceled waiter reported the shared probe as ready")
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("canceled waiter changed probe count to %d, want one", got)
	}
	select {
	case <-probeCanceled:
		t.Fatal("canceled waiter canceled the shared probe")
	default:
	}

	close(release)
	if !<-leaderResult {
		t.Fatal("shared probe did not complete successfully after waiter cancellation")
	}
}

func TestPreviewEdgeReadyDifferentURLsProbeIndependently(t *testing.T) {
	s := &Server{}
	const firstURL = "https://first.apps.example.com"
	const secondURL = "https://second.apps.example.com"
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	secondProbed := make(chan struct{})
	s.SetPreviewEdgeProbe(func(ctx context.Context, url string) error {
		switch url {
		case firstURL:
			close(firstStarted)
			select {
			case <-releaseFirst:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		case secondURL:
			close(secondProbed)
			return nil
		default:
			return errors.New("unexpected preview URL")
		}
	})

	firstResult := make(chan bool, 1)
	go func() { firstResult <- s.previewEdgeReady(context.Background(), firstURL) }()
	waitForPreviewEdgeSignal(t, firstStarted, "first probe start")

	secondResult := make(chan bool, 1)
	go func() { secondResult <- s.previewEdgeReady(context.Background(), secondURL) }()
	waitForPreviewEdgeSignal(t, secondProbed, "second probe start")
	if !<-secondResult {
		t.Fatal("second URL did not become ready while first URL was probing")
	}

	close(releaseFirst)
	if !<-firstResult {
		t.Fatal("first URL did not become ready after its probe completed")
	}
}

func waitForPreviewEdgeSignal(t *testing.T, signal <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
}

// TestDefaultPreviewEdgeProbe pins the classification: only a successful or
// redirect response serves the preview document. Error documents and
// transport failures remain not-ready.
func TestDefaultPreviewEdgeProbe(t *testing.T) {
	served := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer served.Close()
	if err := defaultPreviewEdgeProbe(context.Background(), served.URL); err != nil {
		t.Fatalf("2xx must count as served: %v", err)
	}

	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", "/app")
		w.WriteHeader(http.StatusFound)
	}))
	defer redirect.Close()
	if err := defaultPreviewEdgeProbe(context.Background(), redirect.URL); err != nil {
		t.Fatalf("3xx must count as served: %v", err)
	}

	for _, status := range []int{http.StatusNotFound, http.StatusServiceUnavailable} {
		errorDocument := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(status) }))
		if err := defaultPreviewEdgeProbe(context.Background(), errorDocument.URL); err == nil {
			errorDocument.Close()
			t.Fatalf("HTTP %d must remain not-ready", status)
		}
		errorDocument.Close()
	}

	edge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(522) // Cloudflare: connection timed out to origin
	}))
	defer edge.Close()
	if err := defaultPreviewEdgeProbe(context.Background(), edge.URL); err == nil {
		t.Fatal("Cloudflare 522 must count as not provisioned")
	}

	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	deadURL := dead.URL
	dead.Close() // connection refused from here on
	if err := defaultPreviewEdgeProbe(context.Background(), deadURL); err == nil {
		t.Fatal("transport error must count as not provisioned")
	}
}

func TestPreviewEdgeProbeUsesConfiguredLocalInsecureTLS(t *testing.T) {
	preview := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer preview.Close()

	secureServer := &Server{}
	if secureServer.previewEdgeReady(context.Background(), preview.URL) {
		t.Fatal("untrusted preview certificate must fail when insecure TLS is disabled")
	}

	hubInsecureServer := &Server{mcpInsecureSkipTLSVerify: true}
	if hubInsecureServer.previewEdgeReady(context.Background(), preview.URL) {
		t.Fatal("internal hub TLS setting must not weaken external preview verification")
	}

	localDevServer := &Server{previewInsecureSkipTLSVerify: true}
	if !localDevServer.previewEdgeReady(context.Background(), preview.URL) {
		t.Fatal("local insecure TLS setting should allow the self-signed preview edge")
	}
}
