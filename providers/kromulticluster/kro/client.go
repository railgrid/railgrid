// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package kro

import (
	"context"
	"fmt"
	"os"
	"sync"

	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// Client is the surface the server handlers and MCP tools depend on.
// Implementations: realClient (talks to a central kro cluster) and
// stubClient (returns baked-in templates so the provider is
// demonstrable without infra in phase 2). The split is internal — the
// constructor below picks based on whether KRO_KUBECONFIG is set.
type Client interface {
	// ListTemplates returns the catalog visible to all tenants. Filter
	// supports {Category, Cloud}; empty fields are wildcards.
	ListTemplates(ctx context.Context, filter TemplateFilter) ([]Template, error)
	// GetTemplate returns one template by Name (the RGD's slug), with
	// the full InputsSchema attached. Returns ErrTemplateNotFound when
	// no RGD with that Name+Version exists and the caller asked for a
	// specific version.
	GetTemplate(ctx context.Context, name, version string) (*Template, error)

	// EnsureTenantNamespace materializes the per-tenant namespace in
	// the central kro cluster on first provision. Idempotent. Returns
	// the namespace name (kedge-tenants-<hash>).
	EnsureTenantNamespace(ctx context.Context, tenantPath string) (string, error)

	// CreateInstance writes the kro instance CR and bridges the
	// tenant's cloud-credentials into a per-instance Secret in the
	// same namespace. The Secret is named cloud-credentials-<instance>
	// and adopted as an OwnerReference once the CR's UID is known so
	// Kubernetes GC handles cleanup.
	CreateInstance(ctx context.Context, in CreateInstanceParams) (*Instance, error)

	GetInstance(ctx context.Context, tenantPath, name string) (*Instance, error)
	ListInstances(ctx context.Context, tenantPath string) ([]Instance, error)
	DeleteInstance(ctx context.Context, tenantPath, name string) error
}

// TemplateFilter is the GET /api/templates query, normalized.
type TemplateFilter struct {
	Category string
	Cloud    string
}

// CreateInstanceParams threads identity (TenantPath/User) alongside the
// user-supplied CreateInstanceRequest. The handler builds it from
// request headers + body, then hands it to the client opaquely.
type CreateInstanceParams struct {
	TenantPath   string
	User         string
	Template     Template
	InstanceName string
	Values       map[string]any
	Credentials  map[string][]byte
}

// NewClient inspects KRO_KUBECONFIG. When the path is empty or
// unreadable, returns a stubClient with hardcoded sample templates
// so phase 2 is demoable end-to-end without a real central kro. When
// the kubeconfig loads, returns a realClient backed by dynamic.Interface.
//
// Env:
//
//	KRO_KUBECONFIG          - path to the central kro cluster kubeconfig
//	KRO_NAMESPACE_PREFIX    - tenant-namespace prefix (default "kedge-tenants-")
func NewClient() (Client, error) {
	path := os.Getenv("KRO_KUBECONFIG")
	if path == "" {
		return NewStubClient(), nil
	}

	cfg, err := clientcmd.BuildConfigFromFlags("", path)
	if err != nil {
		return nil, fmt.Errorf("loading KRO_KUBECONFIG %q: %w", path, err)
	}
	return newRealClient(cfg)
}

// realClient wraps dynamic.Interface against the central kro cluster.
type realClient struct {
	dyn dynamic.Interface
	cfg *rest.Config

	// nsCache memoizes EnsureTenantNamespace so the warm path skips
	// the kcp roundtrip. Per-process; populated lazily; never evicted
	// (the set is bounded by # of tenants that ever provisioned).
	nsCache sync.Map // map[string]string  tenantPath → namespace
}

func newRealClient(cfg *rest.Config) (*realClient, error) {
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("kro dynamic client: %w", err)
	}
	return &realClient{dyn: dyn, cfg: cfg}, nil
}
