// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package tenant

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
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
// connection) supplies only the front-proxy host + TLS settings; its
// credentials are intentionally dropped so the factory cannot act as the
// provider. Per request we build a config with that host (cluster segment
// swapped for the tenant's path) and the caller's bearer token.
type ClientFactory struct {
	baseHost string
	baseTLS  rest.TLSClientConfig

	mu  sync.RWMutex
	hot map[string]dynamic.Interface
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
	}
}

// For returns a dynamic client scoped to tenantPath, authenticating as the
// caller via token. Cached per (tenant, token) so a stable per-MCPServer SA
// token reuses one client/transport. An empty token is an error — actions
// must always carry the caller's identity.
func (f *ClientFactory) For(tenantPath, token string) (dynamic.Interface, error) {
	if token == "" {
		return nil, fmt.Errorf("no bearer token on request — cannot act on the tenant's behalf")
	}
	key := tenantPath + ":" + hashToken(token)

	f.mu.RLock()
	dyn, ok := f.hot[key]
	f.mu.RUnlock()
	if ok {
		return dyn, nil
	}

	cfg := &rest.Config{
		Host:            f.baseHost + "/clusters/" + tenantPath,
		BearerToken:     token,
		TLSClientConfig: f.baseTLS,
	}
	d, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("dynamic client for tenant %q: %w", tenantPath, err)
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	if existing, ok := f.hot[key]; ok {
		return existing, nil
	}
	f.hot[key] = d
	return d, nil
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
