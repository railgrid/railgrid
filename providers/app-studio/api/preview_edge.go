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
// Successes are cached per URL for the process lifetime: certificates and
// tunnel routes persist once provisioned, so after the first success the
// probe adds no latency to preview calls. Failures are re-probed on every
// call (that's the polling the portal already does).

const (
	// previewEdgeProbeTimeout bounds one probe attempt. The portal polls, so
	// a slow edge costs one bounded round-trip per poll, never a hang.
	previewEdgeProbeTimeout = 4 * time.Second

	// previewReasonEdgeProvisioning is the machine-readable reason the
	// portal and the assistant's preview tool receive while the edge is
	// still being provisioned.
	previewReasonEdgeProvisioning = "edge_provisioning"

	// previewEdgeProvisioningMessage is the human message for that state.
	previewEdgeProvisioningMessage = "Preview is getting ready. The public URL is being provisioned at the edge (DNS + TLS certificate) — this usually takes a minute or two."
)

// previewEdgeReady reports whether url is actually served at the edge. The
// probe function is injectable for tests (s.previewEdgeProbe); the default
// performs a real HTTPS request with certificate verification ON — an
// unprovisioned certificate MUST fail this check, that is the signal.
func (s *Server) previewEdgeReady(ctx context.Context, url string) bool {
	if _, ok := s.edgeReadyURLs.Load(url); ok {
		return true
	}
	probe := s.previewEdgeProbeHook()
	if err := probe(ctx, url); err != nil {
		return false
	}
	s.edgeReadyURLs.Store(url, struct{}{})
	return true
}

func (s *Server) previewEdgeProbeHook() func(context.Context, string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.previewEdgeProbe != nil {
		return s.previewEdgeProbe
	}
	return defaultPreviewEdgeProbe
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
// 530). Application-level statuses (200/302/404/503 from the app or the dev
// server) all count as served: the dev-runtime data plane owns app
// readiness, this probe owns the edge.
func defaultPreviewEdgeProbe(ctx context.Context, url string) error {
	ctx, cancel := context.WithTimeout(ctx, previewEdgeProbeTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	client := &http.Client{
		Timeout: previewEdgeProbeTimeout,
		// Don't chase the app's redirects — the first edge answer decides.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	if resp.StatusCode >= 520 && resp.StatusCode <= 530 {
		return fmt.Errorf("edge answered %d (origin/tunnel not provisioned)", resp.StatusCode)
	}
	return nil
}

// edgeReadyURLsCache is embedded in Server; declared here so the whole edge
// concern lives in one file.
type edgeReadyURLsCache = sync.Map
