/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package tenant talks to a tenant's kcp workspace as the CALLER. It is used
// only by the MCP/portal surface, where every request carries the caller's own
// bearer token; the controllers, by contrast, act as the provider SA via the
// APIExport virtual workspace and never use this factory.
//
// The base kubeconfig (the provider's own kcp connection) supplies only the
// host + TLS; its credential is dropped so the factory can never authenticate as
// the provider. Per request we build a config with that host (cluster segment
// set to the tenant's logical-cluster ID) and the caller's bearer token. The
// workspace MUST be addressed by ID, never by path: the hub proxy's membership
// gate rejects path-form /clusters/<root:...> with a 403.
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

// ClientFactory builds per-(tenant, caller) dynamic clients.
type ClientFactory struct {
	baseHost string
	baseTLS  rest.TLSClientConfig

	mu  sync.RWMutex
	hot map[string]dynamic.Interface
}

// NewClientFactory reuses base for host + TLS only; the bearer token (and any
// client cert) is dropped. Returns nil when base is nil (serve mode without a
// kcp config), which the MCP tools surface as a clear error.
func NewClientFactory(base *rest.Config) *ClientFactory {
	if base == nil {
		return nil
	}
	baseHost, err := stripClusterSuffix(base.Host)
	if err != nil {
		baseHost = strings.TrimRight(base.Host, "/")
	}
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

// For returns a dynamic client scoped to the workspace's logical-cluster ID,
// authenticating as the caller via token. Cached per (cluster, token). The
// cluster MUST be the kcp logical-cluster ID (X-Kedge-Cluster), never a
// workspace path — the hub proxy rejects path-form addressing.
func (f *ClientFactory) For(clusterID, token string) (dynamic.Interface, error) {
	if token == "" {
		return nil, fmt.Errorf("no bearer token on request — cannot act on the tenant's behalf")
	}
	key := clusterID + ":" + hashToken(token)

	f.mu.RLock()
	dyn, ok := f.hot[key]
	f.mu.RUnlock()
	if ok {
		return dyn, nil
	}

	cfg := &rest.Config{
		Host:            f.baseHost + "/clusters/" + clusterID,
		BearerToken:     token,
		TLSClientConfig: f.baseTLS,
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

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:8])
}

func stripClusterSuffix(host string) (string, error) {
	u, err := url.Parse(host)
	if err != nil {
		return "", fmt.Errorf("parse base kubeconfig host %q: %w", host, err)
	}
	idx := strings.Index(u.Path, "/clusters/")
	if idx < 0 {
		return strings.TrimRight(host, "/"), nil
	}
	u.Path = u.Path[:idx]
	return strings.TrimRight(u.String(), "/"), nil
}
