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
	"fmt"
	"net/http"
	"sync"
	"time"
)

// Edge readiness for preview URLs.
//
// A development instance's status.url exists as soon as its HTTPRoute does —
// but in production the Gateway edge (cfgate / Cloudflare Tunnel) still has
// to program DNS and provision the hostname's TLS certificate, which takes a
// minute or more. Declaring the preview Ready on status.url alone hands the
// portal a URL whose TLS handshake fails, so the user stares at a broken
// iframe with no explanation. The preview path therefore probes the URL and
// keeps reporting "provisioning" — with a reason the portal can render —
// until the edge actually serves it.
//
// Successes are cached per URL for a short TTL. This avoids repeated probe
// latency during tight portal polling while still observing runtime restarts
// and later gateway regressions. Failures are re-probed on every call.

const (
	// previewEdgeProbeTimeout bounds one probe attempt. The portal polls, so
	// a slow edge costs one bounded round-trip per poll, never a hang.
	previewEdgeProbeTimeout = 4 * time.Second
	previewEdgeReadyTTL     = 15 * time.Second

	// previewReasonEdgeProvisioning is the machine-readable reason the
	// portal and the assistant's preview tool receive while the edge is
	// still being provisioned.
	previewReasonEdgeProvisioning = "edge_provisioning"

	// previewEdgeProvisioningMessage is the human message for that state.
	previewEdgeProvisioningMessage = "Preview is getting ready. The public URL is being provisioned at the edge (DNS + TLS certificate) — this usually takes a minute or two."
)

// previewEdgeReady reports whether url is actually served at the edge. The
// probe function is injectable for tests (s.previewEdgeProbe); the default
// performs a real HTTPS request. Production keeps certificate verification
// on. Local development can reuse the server's explicit insecure-TLS setting
// because the local Gateway terminates with a self-signed certificate.
func (s *Server) previewEdgeReady(ctx context.Context, url string) bool {
	if observedAt, ok := s.edgeReadyURLs.Load(url); ok {
		if timestamp, valid := observedAt.(time.Time); valid && time.Since(timestamp) < previewEdgeReadyTTL {
			return true
		}
		s.edgeReadyURLs.Delete(url)
	}

	s.mu.Lock()
	if pending := s.previewEdgeProbeInflight[url]; pending != nil {
		done := pending.done
		s.mu.Unlock()
		select {
		case <-done:
			return pending.ready
		case <-ctx.Done():
			return false
		}
	}
	if s.previewEdgeProbeInflight == nil {
		s.previewEdgeProbeInflight = make(map[string]*previewEdgeProbeInflight)
	}
	pending := &previewEdgeProbeInflight{done: make(chan struct{})}
	s.previewEdgeProbeInflight[url] = pending
	s.mu.Unlock()

	probe := s.previewEdgeProbeHook()
	ready := probe(ctx, url) == nil
	if ready {
		s.edgeReadyURLs.Store(url, time.Now())
	}

	s.mu.Lock()
	pending.ready = ready
	delete(s.previewEdgeProbeInflight, url)
	close(pending.done)
	s.mu.Unlock()
	return ready
}

func (s *Server) previewEdgeProbeHook() func(context.Context, string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.previewEdgeProbe != nil {
		return s.previewEdgeProbe
	}
	if s.previewInsecureSkipTLSVerify {
		return insecurePreviewEdgeProbe
	}
	return defaultPreviewEdgeProbe
}

// SetPreviewInsecureSkipTLSVerify allows an explicitly configured local
// development environment to probe its self-signed preview Gateway. It is
// intentionally separate from the internal hub/MCP TLS setting so production
// preview hostnames retain certificate verification by default.
func (s *Server) SetPreviewInsecureSkipTLSVerify(enabled bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.previewInsecureSkipTLSVerify = enabled
}

// SetPreviewEdgeProbe overrides the edge probe (tests).
func (s *Server) SetPreviewEdgeProbe(probe func(context.Context, string) error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.previewEdgeProbe = probe
}

// defaultPreviewEdgeProbe GETs the URL and decides whether the edge serves
// it. Any transport error (DNS, connection, TLS handshake — the certificate
// case) means not-yet-provisioned. An HTTP response means the edge
// terminated TLS and routed somewhere — except Cloudflare's 52x range, which
// is the edge itself reporting the origin/tunnel isn't wired yet (520-527,
// 530). Only 2xx/3xx proves that the preview document is currently being
// served. A 4xx/5xx proves the edge answered, but must remain retryable rather
// than handing the portal a gateway or application error page as Ready.
func defaultPreviewEdgeProbe(ctx context.Context, url string) error {
	return probePreviewEdge(ctx, url, false)
}

func insecurePreviewEdgeProbe(ctx context.Context, url string) error {
	return probePreviewEdge(ctx, url, true)
}

func probePreviewEdge(ctx context.Context, url string, insecureSkipTLSVerify bool) error {
	ctx, cancel := context.WithTimeout(ctx, previewEdgeProbeTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	client := &http.Client{
		Timeout:   previewEdgeProbeTimeout,
		Transport: projectMCPTransport(insecureSkipTLSVerify),
		// Don't chase the app's redirects — the first edge answer decides.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusBadRequest {
		return fmt.Errorf("preview document answered %d", resp.StatusCode)
	}
	return nil
}

// edgeReadyURLsCache is embedded in Server; declared here so the whole edge
// concern lives in one file.
type edgeReadyURLsCache = sync.Map

type previewEdgeProbeInflight struct {
	done  chan struct{}
	ready bool
}
