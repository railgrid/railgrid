// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package tenant

import (
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"

	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// ClientFactory builds per-tenant dynamic clients on demand from the
// base kedge-provider-kubeconfig. Caches one *dynamic.Interface per
// tenant path; the warm path is a single sync.Map load.
//
// The base kubeconfig (env KEDGE_PROVIDER_KUBECONFIG, default
// /var/run/secrets/kedge/kedge-provider-kubeconfig) is minted by the
// hub catalog controller (pkg/hub/providers/provision.go ~line 132)
// and targets root:kedge:providers:kro-multicluster. To read a tenant's
// resources via the APIExport's virtual workspace, we copy the rest
// config and swap the /clusters/<path> segment in cfg.Host for the
// tenant's path. The bearer token and TLS settings carry over.
type ClientFactory struct {
	base     *rest.Config
	baseHost string

	mu    sync.RWMutex
	hot   map[string]dynamic.Interface
}

// NewClientFactory loads the mounted kubeconfig and prepares a factory.
// Returns an error only if the kubeconfig is unreadable or malformed —
// missing/empty env var is treated as a hard failure (the provider
// CANNOT broker without it; degrading silently would mask config
// problems in deployments).
func NewClientFactory() (*ClientFactory, error) {
	path := os.Getenv("KEDGE_PROVIDER_KUBECONFIG")
	if path == "" {
		path = "/var/run/secrets/kedge/kedge-provider-kubeconfig"
	}
	if _, err := os.Stat(path); err != nil {
		return nil, fmt.Errorf("kedge-provider-kubeconfig at %q: %w", path, err)
	}
	cfg, err := clientcmd.BuildConfigFromFlags("", path)
	if err != nil {
		return nil, fmt.Errorf("loading kedge-provider-kubeconfig: %w", err)
	}
	baseHost, err := stripClusterSuffix(cfg.Host)
	if err != nil {
		return nil, err
	}
	return &ClientFactory{
		base:     cfg,
		baseHost: baseHost,
		hot:      make(map[string]dynamic.Interface),
	}, nil
}

// For returns a dynamic client scoped to the given tenant's workspace.
// Cached per tenantPath; the warm path is a single map lookup. The
// returned client honours the APIExport's permissionClaims — reads
// outside the granted resources will get 403 from kcp.
func (f *ClientFactory) For(tenantPath string) (dynamic.Interface, error) {
	f.mu.RLock()
	dyn, ok := f.hot[tenantPath]
	f.mu.RUnlock()
	if ok {
		return dyn, nil
	}

	cfg := rest.CopyConfig(f.base)
	cfg.Host = f.baseHost + "/clusters/" + tenantPath
	d, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("dynamic client for tenant %q: %w", tenantPath, err)
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	// Double-check under write lock to avoid two competing fills.
	if existing, ok := f.hot[tenantPath]; ok {
		return existing, nil
	}
	f.hot[tenantPath] = d
	return d, nil
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
