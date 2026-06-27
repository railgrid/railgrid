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
	"io"
	"net/http"
	"strings"
	"time"
)

// App Studio no longer holds a kubeconfig to the runtime cluster. The live
// development data plane (logs, file sync, restart, preview readiness) is now
// served by the infrastructure provider as subresources on the SandboxRunner
// instance, reached through the hub backend proxy:
//
//	{hub}/services/providers/infrastructure/dataplane/clusters/{cluster}/sandboxrunners/{name}/{verb}
//
// The caller's bearer token is forwarded as-is; the infra provider authorizes
// the request as that caller (a tenant-scoped GET on the instance) and owns the
// runtime-cluster credential. See docs/app-studio-runtime-decoupling.md.
const (
	infraDataPlaneProvider = "infrastructure"
	sandboxRunnersResource = "sandboxrunners"

	dataPlaneVerbLog     = "log"
	dataPlaneVerbSync    = "sync"
	dataPlaneVerbRestart = "restart"
	dataPlaneVerbProxy   = "proxy"

	dataPlaneCallTimeout = 30 * time.Second
)

// sandboxDataPlaneURL composes the hub URL for a SandboxRunner data-plane verb.
// tail (with leading slash) is appended after the verb — used only by the open
// "proxy" verb; the control verbs leave it empty.
func (s *Server) sandboxDataPlaneURL(clusterID, runnerName, verb, tail string) string {
	u := strings.TrimRight(s.hubBase, "/") +
		fmt.Sprintf("/services/providers/%s/dataplane/clusters/%s/%s/%s/%s",
			infraDataPlaneProvider, clusterID, sandboxRunnersResource, runnerName, verb)
	if tail != "" {
		u += tail
	}
	return u
}

// newSandboxDataPlaneRequest builds a data-plane request authenticated as the
// caller (the same bearer token the caller authenticated to App Studio with).
func (s *Server) newSandboxDataPlaneRequest(ctx context.Context, method string, id identity, runnerName, verb, tail string, body io.Reader) (*http.Request, error) {
	if strings.TrimSpace(s.hubBase) == "" {
		return nil, fmt.Errorf("hub URL is not configured; cannot reach the infrastructure data plane")
	}
	if strings.TrimSpace(id.clusterID) == "" {
		return nil, fmt.Errorf("no workspace cluster on request; cannot address the sandbox runner")
	}
	req, err := http.NewRequestWithContext(ctx, method, s.sandboxDataPlaneURL(id.clusterID, runnerName, verb, tail), body)
	if err != nil {
		return nil, err
	}
	if token := strings.TrimSpace(id.token); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return req, nil
}

// sandboxDataPlaneClient returns an HTTP client for data-plane calls, honoring
// the same TLS-skip knob the MCP client uses for hub-internal addressing.
func (s *Server) sandboxDataPlaneClient(timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout, Transport: projectMCPTransport(s.mcpInsecureSkipTLSVerify)}
}

// sandboxDataPlanePost sends a POST verb (sync, restart) and returns the body +
// status code. The caller maps non-2xx to an error so the runner's own response
// surfaces to the UI.
func (s *Server) sandboxDataPlanePost(ctx context.Context, id identity, runnerName, verb string, payload []byte) ([]byte, int, error) {
	callCtx, cancel := context.WithTimeout(ctx, dataPlaneCallTimeout)
	defer cancel()
	req, err := s.newSandboxDataPlaneRequest(callCtx, http.MethodPost, id, runnerName, verb, "", strings.NewReader(string(payload)))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.sandboxDataPlaneClient(dataPlaneCallTimeout).Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("sandbox data plane %s: %w", verb, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return body, resp.StatusCode, nil
}

// sandboxDataPlaneStream proxies a streaming GET verb (logs) straight to w,
// copying the upstream status and content type.
func (s *Server) sandboxDataPlaneStream(ctx context.Context, id identity, runnerName, verb string, w http.ResponseWriter) error {
	req, err := s.newSandboxDataPlaneRequest(ctx, http.MethodGet, id, runnerName, verb, "", nil)
	if err != nil {
		return err
	}
	// No client timeout: log streams are long-lived; ctx cancellation (request
	// close) ends them.
	resp, err := s.sandboxDataPlaneClient(0).Do(req)
	if err != nil {
		return fmt.Errorf("sandbox data plane %s: %w", verb, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if ct := resp.Header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	w.WriteHeader(resp.StatusCode)
	flusher, _ := w.(http.Flusher)
	buf := make([]byte, 32<<10)
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			if _, writeErr := w.Write(buf[:n]); writeErr != nil {
				return writeErr
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		if readErr == io.EOF {
			return nil
		}
		if readErr != nil {
			return readErr
		}
	}
}

// sandboxDataPlaneProbe performs a GET against the open proxy verb (path tail)
// and returns the upstream status + a bounded body, for preview readiness.
func (s *Server) sandboxDataPlaneProbe(ctx context.Context, id identity, runnerName, tail string) (int, []byte, error) {
	callCtx, cancel := context.WithTimeout(ctx, previewReadinessProbeTimeout)
	defer cancel()
	req, err := s.newSandboxDataPlaneRequest(callCtx, http.MethodGet, id, runnerName, dataPlaneVerbProxy, tail, nil)
	if err != nil {
		return 0, nil, err
	}
	resp, err := s.sandboxDataPlaneClient(previewReadinessProbeTimeout).Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return resp.StatusCode, nil, err
	}
	return resp.StatusCode, body, nil
}
