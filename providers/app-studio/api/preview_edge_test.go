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
	"testing"
)

// TestPreviewEdgeReadyGatesAndCaches pins the edge-readiness contract: a
// failing probe keeps the preview not-ready, a succeeding probe flips it, and
// the success is cached — once provisioned, the edge is never probed again
// for that URL (certificates persist; polling must not pay probe latency
// forever).
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

	// A different URL starts cold.
	s.SetPreviewEdgeProbe(func(_ context.Context, _ string) error { return probeErr })
	if s.previewEdgeReady(context.Background(), "https://other.apps.example.com") {
		t.Fatal("readiness cache leaked across URLs")
	}
}

// TestDefaultPreviewEdgeProbe pins the classification: an HTTP answer is
// served (whatever the app-level status), Cloudflare's 52x edge range is
// not-provisioned, and transport errors (connection refused — the same class
// as a TLS handshake failure) are not-provisioned.
func TestDefaultPreviewEdgeProbe(t *testing.T) {
	served := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound) // app-level 404 still means the edge serves
	}))
	defer served.Close()
	if err := defaultPreviewEdgeProbe(context.Background(), served.URL); err != nil {
		t.Fatalf("app-level 404 must count as served: %v", err)
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
