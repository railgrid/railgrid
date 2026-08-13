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
	"time"
)

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
