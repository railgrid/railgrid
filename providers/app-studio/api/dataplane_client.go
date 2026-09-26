/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package api

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/railgrid/provider-sdk/dataplane"

	"github.com/railgrid/provider-app-studio/internal/crossprovider"
)

// App Studio no longer holds a kubeconfig to the runtime cluster. The live
// development data plane (logs, file sync, restart, preview readiness) is
// served by the infrastructure provider as kcp custom subresources on the
// workload Instance — instances/{verb} — and App Studio calls them the one way
// a provider calls another provider's verb: through its OWN APIExport virtual
// workspace, as itself, at
//
//	<export VW>/clusters/{cluster}/apis/infrastructure.railgrid.ai/v1alpha1/instances/{name}/{verb}[/{tail}][?component={component}]
//
// Each verb is a claim on this provider's export (manifest.yaml
// spec.requires[].resources[], the "instances/<verb>" coordinate, whose
// generated claim spells every verb) that the tenant accepted at Enable. kcp authorizes the call against that claim,
// resolves it to whichever infrastructure copy the workspace bound, and
// forwards it there impersonating App Studio; the infrastructure gate sees a
// foreign provider whose claim is the authorization. The caller's identity
// does not travel — cross-provider work is done as the provider — and the
// infrastructure provider owns the runtime-cluster credential. See
// docs/app-studio-runtime-decoupling.md and
// docs/app-studio-template-sandboxes.md §3.
const (
	dataPlaneVerbLog       = "log"
	dataPlaneVerbSync      = "sync"
	dataPlaneVerbRestart   = "restart"
	dataPlaneVerbProxy     = "proxy"
	dataPlaneVerbEnv       = "env"
	dataPlaneVerbProcess   = "process"
	dataPlaneVerbExec      = "exec"
	dataPlaneVerbWorkspace = "workspace"

	dataPlaneCallTimeout = 30 * time.Second
	// dataPlaneMaxResponseBytes bounds control-verb responses. It is sized
	// like the development agent's /sync body cap (base64 bundles); a larger
	// response is an error rather than a silently truncated body.
	dataPlaneMaxResponseBytes = 96 << 20
)

// infrastructureGroupVersion is the API the infrastructure provider serves its
// Instance kind (and its verbs) under.
var infrastructureGroupVersion = schema.GroupVersion{Group: crossprovider.InfrastructureAPIGroup, Version: "v1alpha1"}

// dataPlaneRef addresses one data-plane target: an instance of a resource,
// optionally scoped to one of its components. An empty Component addresses
// instance-level verbs.
type dataPlaneRef struct {
	Resource  string
	Name      string
	Component string
}

// dataPlaneURL composes the URL of a data-plane verb on the infrastructure
// object ref, through this provider's export virtual workspace.
//
// tail (with a leading slash, and optionally a "?query") is what the open
// "proxy" verb addresses beyond the verb; the control verbs leave it empty.
// The path is rendered by dataplane.SubresourceURL, which refuses any segment
// that would not parse back to the same request on the serving side — so a
// component name with a slash in it fails here rather than rerouting the
// call. The component travels as the ?component= query parameter, never in
// the path (kcp would read it as a subresource named "components").
func (s *Server) dataPlaneURL(ctx context.Context, id identity, ref dataPlaneRef, verb, tail string) (string, error) {
	if s == nil || s.callers == nil {
		return "", fmt.Errorf("no provider credential configured; cannot reach the infrastructure data plane")
	}
	path, query, _ := strings.Cut(strings.TrimPrefix(tail, "/"), "?")
	gvr := infrastructureGroupVersion.WithResource(ref.Resource)
	u, err := s.callers.ExportVerbURL(ctx, gvr, dataplane.Request{
		ClusterID: id.clusterID,
		Resource:  ref.Resource,
		Name:      ref.Name,
		Component: ref.Component,
		Verb:      verb,
		Tail:      path,
	})
	if err != nil {
		return "", fmt.Errorf("addressing %s/%s %s: %w", ref.Resource, ref.Name, verb, err)
	}
	if query != "" {
		if strings.Contains(u, "?") {
			u += "&" + query
		} else {
			u += "?" + query
		}
	}
	return u, nil
}

// newDataPlaneRequest builds a data-plane request authenticated as THIS
// provider, addressed through its export virtual workspace.
func (s *Server) newDataPlaneRequest(ctx context.Context, method string, id identity, ref dataPlaneRef, verb, tail string, body io.Reader) (*http.Request, error) {
	if strings.TrimSpace(id.clusterID) == "" {
		return nil, fmt.Errorf("no workspace cluster on request; cannot address the development runtime")
	}
	// An empty resource or name would produce a URL with a hollow path segment
	// and surface as a confusing 404 from kcp — fail fast with the real cause
	// instead.
	if strings.TrimSpace(ref.Resource) == "" || strings.TrimSpace(ref.Name) == "" {
		return nil, fmt.Errorf("development target is incomplete (resource %q, name %q); the project's template binding did not resolve", ref.Resource, ref.Name)
	}
	endpoint, err := s.dataPlaneURL(ctx, id, ref, verb, tail)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return nil, err
	}
	// Who asked, for the far end's logs. It authorizes nothing: kcp
	// authorizes this call against App Studio's claim, as App Studio.
	if user := strings.TrimSpace(id.user); user != "" {
		req.Header.Set(dataplane.HeaderUser, user)
	}
	return req, nil
}

// sandboxDataPlaneClient returns an HTTP client for data-plane calls: the
// provider's own credential and TLS settings (dataplane.Callers.
// ProviderHTTPClient), bounded by timeout (0 leaves the call to its context).
func (s *Server) sandboxDataPlaneClient(timeout time.Duration) *http.Client {
	if s != nil && s.sandboxDataPlaneClientFactory != nil {
		return s.sandboxDataPlaneClientFactory(timeout)
	}
	if s != nil && s.callers != nil {
		if client, err := s.callers.ProviderHTTPClient(); err == nil {
			bounded := *client
			bounded.Timeout = timeout
			return &bounded
		}
	}
	// No provider credential: the request itself already failed to build
	// (dataPlaneURL refuses), so this client is never asked to authenticate.
	return &http.Client{Timeout: timeout, Transport: projectMCPTransport(s.mcpInsecureSkipTLSVerify)}
}

// dataPlanePost sends a POST verb (sync, restart, env) and returns the body +
// status code. The caller maps non-2xx to an error so the runtime's own
// response surfaces to the UI.
func (s *Server) dataPlanePost(ctx context.Context, id identity, ref dataPlaneRef, verb string, payload []byte) ([]byte, int, error) {
	return s.dataPlanePostBounded(ctx, id, ref, verb, payload, dataPlaneMaxResponseBytes)
}

// dataPlanePostWithTimeout gives long-running, explicitly bounded operations
// such as dependency-installing workspace syncs their own deadline without
// weakening the ordinary data-plane timeout used by logs, restarts, and env.
func (s *Server) dataPlanePostWithTimeout(ctx context.Context, id identity, ref dataPlaneRef, verb string, payload []byte, timeout time.Duration) ([]byte, int, error) {
	return s.dataPlanePostBoundedWithHeadersAndTimeout(ctx, id, ref, verb, payload, dataPlaneMaxResponseBytes, nil, timeout)
}

// dataPlanePostBounded sends a POST verb while applying a caller-selected
// response bound. Exec output is intentionally much smaller than the generic
// control-plane bound so a noisy process cannot consume the assistant context.
func (s *Server) dataPlanePostBounded(ctx context.Context, id identity, ref dataPlaneRef, verb string, payload []byte, maxBytes int64) ([]byte, int, error) {
	return s.dataPlanePostBoundedWithHeaders(ctx, id, ref, verb, payload, maxBytes, nil)
}

func (s *Server) dataPlanePostBoundedWithHeaders(ctx context.Context, id identity, ref dataPlaneRef, verb string, payload []byte, maxBytes int64, headers http.Header) ([]byte, int, error) {
	return s.dataPlanePostBoundedWithHeadersAndTimeout(ctx, id, ref, verb, payload, maxBytes, headers, dataPlaneCallTimeout)
}

func (s *Server) dataPlanePostBoundedWithHeadersAndTimeout(ctx context.Context, id identity, ref dataPlaneRef, verb string, payload []byte, maxBytes int64, headers http.Header, timeout time.Duration) ([]byte, int, error) {
	if timeout <= 0 {
		timeout = dataPlaneCallTimeout
	}
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := s.newDataPlaneRequest(callCtx, http.MethodPost, id, ref, verb, "", bytes.NewReader(payload))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	for key, values := range headers {
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}
	resp, err := s.sandboxDataPlaneClient(timeout).Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("development data plane %s: %w", verb, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if maxBytes <= 0 {
		maxBytes = dataPlaneMaxResponseBytes
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	if int64(len(body)) > maxBytes {
		return nil, resp.StatusCode, fmt.Errorf("development data plane %s: response exceeds %d bytes", verb, maxBytes)
	}
	return body, resp.StatusCode, nil
}

// dataPlaneGet sends a GET verb (log) and returns a bounded body + status
// code. Unlike dataPlaneStream it collects the response into memory, for
// callers (assistant tools) that need the payload as a value rather than a
// stream. maxBytes bounds the body so a large log buffer cannot blow the
// assistant context.
func (s *Server) dataPlaneGet(ctx context.Context, id identity, ref dataPlaneRef, verb string, maxBytes int64) ([]byte, int, error) {
	callCtx, cancel := context.WithTimeout(ctx, dataPlaneCallTimeout)
	defer cancel()
	req, err := s.newDataPlaneRequest(callCtx, http.MethodGet, id, ref, verb, "", nil)
	if err != nil {
		return nil, 0, err
	}
	resp, err := s.sandboxDataPlaneClient(dataPlaneCallTimeout).Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("development data plane %s: %w", verb, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if maxBytes <= 0 {
		maxBytes = 16 << 20
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return body, resp.StatusCode, nil
}

// dataPlaneStream proxies a streaming GET verb (logs) straight to w, copying
// the upstream status and content type.
func (s *Server) dataPlaneStream(ctx context.Context, id identity, ref dataPlaneRef, verb string, w http.ResponseWriter) error {
	req, err := s.newDataPlaneRequest(ctx, http.MethodGet, id, ref, verb, "", nil)
	if err != nil {
		return err
	}
	// No client timeout: log streams are long-lived; ctx cancellation (request
	// close) ends them.
	resp, err := s.sandboxDataPlaneClient(0).Do(req)
	if err != nil {
		return fmt.Errorf("development data plane %s: %w", verb, err)
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
