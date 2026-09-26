// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package tenant

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
)

// ClientFactory builds per-(tenant, caller) dynamic clients. There is NO
// provider-wide identity: every MCP request carries its own bearer token
// (the per-MCPServer ServiceAccount token, or the end user's token), and
// the factory uses THAT token as the credential, scoped to the tenant's
// workspace. Every action is therefore performed as the caller and
// authorized by the caller's RBAC in the tenant workspace.
//
// The base kubeconfig (INFRASTRUCTURE_KUBECONFIG — the provider's own kcp
// connection) supplies only the host + TLS settings; its credentials are
// intentionally dropped so the factory cannot act as the provider. Per request
// we build a config with that host (cluster segment set to the tenant's
// logical-cluster ID) and the caller's bearer token. The workspace MUST be
// addressed by ID, never by path: the hub proxy's membership gate rejects
// path-form /clusters/<root:...> with a 403.
type ClientFactory struct {
	baseHost string
	baseTLS  rest.TLSClientConfig

	mu   sync.RWMutex
	hot  map[string]dynamic.Interface
	http map[string]*http.Client
}

// NewClientFactory reuses the provider's existing kcp connection (base) for
// the front-proxy host + TLS only — the bearer token (and any client-cert
// credential) is dropped, so the factory can never authenticate as the
// provider. Returns nil when base is nil (serve mode without a kcp config),
// which the MCP tools surface as a clear "tenant client unavailable" error.
func NewClientFactory(base *rest.Config) *ClientFactory {
	if base == nil {
		return nil
	}
	// stripClusterSuffix only fails on an unparseable URL, which a loaded
	// rest.Config won't have; fall back to the raw host if it ever does.
	baseHost, err := stripClusterSuffix(base.Host)
	if err != nil {
		baseHost = strings.TrimRight(base.Host, "/")
	}
	// Keep only the server-verification side of TLS (CA / insecure). Drop
	// any client certificate so the only credential is the per-request
	// bearer token set in For().
	tls := base.TLSClientConfig
	tls.CertData = nil
	tls.CertFile = ""
	tls.KeyData = nil
	tls.KeyFile = ""
	return &ClientFactory{
		baseHost: baseHost,
		baseTLS:  tls,
		hot:      make(map[string]dynamic.Interface),
		http:     make(map[string]*http.Client),
	}
}

// verbPathPrefix is where every provider data-plane verb lives on the front
// door: a kcp custom subresource under /clusters/{id}/apis/….
const verbPathPrefix = "/clusters/"

// DoVerb sends one request for a provider data-plane verb — a kcp custom
// subresource, provider-sdk/dataplane.SubresourcePath — to the same front
// door the tenant clients use, authenticating as the caller with token. kcp
// authorizes the verb with the caller's RBAC and forwards it to the owning
// provider (which may be this very process) with the caller's identity
// stamped; nothing here acts as the provider.
//
// path must be the kube path, leading /clusters/{id} included; headers may
// carry verb-specific extras but never the credential, which is set from
// token alone. The caller owns the response body.
func (f *ClientFactory) DoVerb(ctx context.Context, token, method, path string, body io.Reader, headers http.Header) (*http.Response, error) {
	if f == nil {
		return nil, fmt.Errorf("tenant client factory is unavailable")
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return nil, fmt.Errorf("no bearer token on request — cannot act on the tenant's behalf")
	}
	if !strings.HasPrefix(path, verbPathPrefix) {
		return nil, fmt.Errorf("verb path %q is not a kcp custom-subresource path under %s", path, verbPathPrefix)
	}
	client, err := f.httpClientFor(token)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, method, f.baseHost+path, body)
	if err != nil {
		return nil, fmt.Errorf("verb request: %w", err)
	}
	for name, values := range headers {
		if strings.EqualFold(name, "Authorization") {
			continue
		}
		for _, value := range values {
			req.Header.Add(name, value)
		}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("verb %s %s: %w", method, path, err)
	}
	return resp, nil
}

// httpClientFor is the bearer-authenticated HTTP client for the front door,
// cached per token like the dynamic clients so a stable token reuses one
// transport.
func (f *ClientFactory) httpClientFor(token string) (*http.Client, error) {
	key := hashToken(token)

	f.mu.RLock()
	client, ok := f.http[key]
	f.mu.RUnlock()
	if ok {
		return client, nil
	}

	client, err := rest.HTTPClientFor(&rest.Config{
		Host:            f.baseHost,
		BearerToken:     token,
		TLSClientConfig: f.baseTLS,
	})
	if err != nil {
		return nil, fmt.Errorf("http client for the front door: %w", err)
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	if existing, ok := f.http[key]; ok {
		return existing, nil
	}
	f.http[key] = client
	return client, nil
}

// For returns a dynamic client scoped to the workspace's logical-cluster ID,
// authenticating as the caller via token. Cached per (cluster, token) so a
// stable per-MCPServer SA token reuses one client/transport. An empty token is
// an error — actions must always carry the caller's identity. The cluster MUST
// be the kcp logical-cluster ID (X-Railgrid-Cluster), never a workspace path — the
// hub proxy rejects path-form addressing.
func (f *ClientFactory) For(clusterID, token string) (dynamic.Interface, error) {
	cfg, err := f.configFor(clusterID, token)
	if err != nil {
		return nil, err
	}
	key := clusterID + ":" + hashToken(token)

	f.mu.RLock()
	dyn, ok := f.hot[key]
	f.mu.RUnlock()
	if ok {
		return dyn, nil
	}

	d, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("dynamic client for cluster %q: %w", clusterID, err)
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	if existing, ok := f.hot[key]; ok {
		return existing, nil
	}
	f.hot[key] = d
	return d, nil
}

func (f *ClientFactory) configFor(clusterID, token string) (*rest.Config, error) {
	if f == nil {
		return nil, fmt.Errorf("tenant client factory is unavailable")
	}
	clusterID = strings.TrimSpace(clusterID)
	if clusterID == "" || clusterID == "." || clusterID == ".." || url.PathEscape(clusterID) != clusterID {
		return nil, fmt.Errorf("invalid tenant logical-cluster ID")
	}
	if strings.TrimSpace(token) == "" {
		return nil, fmt.Errorf("no bearer token on request — cannot act on the tenant's behalf")
	}
	return &rest.Config{
		Host:            f.baseHost + "/clusters/" + clusterID,
		BearerToken:     token,
		TLSClientConfig: f.baseTLS,
	}, nil
}

// hashToken returns a short, non-reversible cache key for a bearer token so
// raw credentials never sit in map keys.
func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:8])
}

// stripClusterSuffix turns "https://hub:9443/clusters/root:foo:bar"
// into "https://hub:9443" so we can re-attach a different /clusters/<path>
// per tenant. Returns an error when the URL doesn't have the expected
// shape — better to fail loud at startup than mint garbage URLs per
// request.
func stripClusterSuffix(host string) (string, error) {
	u, err := url.Parse(host)
	if err != nil {
		return "", fmt.Errorf("parse base kubeconfig host %q: %w", host, err)
	}
	idx := strings.Index(u.Path, "/clusters/")
	if idx < 0 {
		// Some local-dev configs don't include the suffix because
		// the hub is being addressed directly. Accept and return as-is.
		return strings.TrimRight(host, "/"), nil
	}
	u.Path = u.Path[:idx]
	return strings.TrimRight(u.String(), "/"), nil
}
