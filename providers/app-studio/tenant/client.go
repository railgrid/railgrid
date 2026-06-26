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
// front-proxy host + TLS; its credential is dropped so the factory can never
// authenticate as the provider. Per request we build a config with that host
// (cluster segment swapped for the tenant's path) and the caller's bearer token.
package tenant

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"strings"

	lru "github.com/hashicorp/golang-lru/v2"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
)

// maxCachedClients bounds the per-(tenant, token) client cache so a
// long-running provider can't grow without bound as tokens rotate.
const maxCachedClients = 1024

// ClientFactory builds per-(tenant, caller) dynamic clients.
type ClientFactory struct {
	baseHost string
	baseTLS  rest.TLSClientConfig

	// hot is an LRU keyed by (tenantPath, full token hash); it is internally
	// synchronized, so no extra mutex is needed.
	hot *lru.Cache[string, dynamic.Interface]
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
	// lru.New only errors on a non-positive size, which maxCachedClients never is.
	hot, _ := lru.New[string, dynamic.Interface](maxCachedClients)
	return &ClientFactory{
		baseHost: baseHost,
		baseTLS:  tls,
		hot:      hot,
	}
}

// For returns a dynamic client scoped to tenantPath, authenticating as the
// caller via token. Cached per (tenant, token).
func (f *ClientFactory) For(tenantPath, token string) (dynamic.Interface, error) {
	if token == "" {
		return nil, fmt.Errorf("no bearer token on request — cannot act on the tenant's behalf")
	}
	key := tenantPath + ":" + hashToken(token)

	if dyn, ok := f.hot.Get(key); ok {
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

	// A concurrent caller may have built an equivalent client for the same key;
	// LRU.Add is safe either way and the loser is simply discarded.
	f.hot.Add(key, d)
	return d, nil
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
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
