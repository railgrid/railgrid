/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package api

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"

	"github.com/railgrid/provider-sdk/dataplane"
	"github.com/railgrid/provider-sdk/tenantaccess"

	"github.com/railgrid/provider-app-studio/internal/crossprovider"
)

// Which provider serves a dependency is a fact about the TENANT's workspace,
// not about this provider's configuration. A workspace may have enabled the
// platform copy of infrastructure or its own self-hosted one, and both answer
// to a different name under /services/providers/{name}/. App Studio used to
// hardcode "infrastructure" and build the rest of the URL by hand, which is
// cross-provider-simplification finding X-8: the coordinate was a constant in
// the consumer rather than something read from the binding that makes the
// dependency reachable at all.
//
// The provider name is therefore read from the tenant's APIBinding for the
// dependency's APIExport, as the caller, and the path is rendered by
// dataplane.ProviderPath so the grammar lives in one place.
//
// The read is a GET of the binding named after the provider, not a LIST of
// every binding in the workspace. The hub names each binding after the
// provider it enables (pkg/hub/restapi/providers_enable.go), so anyone who
// knows which dependency they want already knows the name; a list would hand
// the caller the full inventory of what the tenant has enabled, and it is not
// a shape the hub's scoped-identity policy will mint for a background identity
// at all (docs/provider-connectivity-contract.md §"Scoped identities", clause
// D). Reading the object is still the point: the export reference on it is
// asserted against the export asked for, without which this would be the
// hardcoded name with extra steps.

// infraAPIExportName is the APIExport the infrastructure provider serves.
// App Studio's dependency on it is declared in manifest.yaml
// (spec.dependencies); this is the name of the export a workspace binds when
// it enables that dependency, whichever copy of the provider it enabled.
const infraAPIExportName = crossprovider.InfrastructureAPIExport

// infraDependencyName is the dependency App Studio declares on the
// infrastructure provider in manifest.yaml (spec.dependencies). It is a
// LABEL — it names the dependency in the project view — and is never a URL
// segment: which provider actually serves it in a given workspace comes from
// that workspace's APIBinding, because a self-hosted copy answers to whatever
// name the tenant enabled it under.
const infraDependencyName = "infrastructure"

// providerLookup resolves the provider name serving exportName in a cluster,
// as the caller holding token. Production wires ProviderResolver; tests
// substitute a table.
type providerLookup func(ctx context.Context, clusterID, token, exportName string) (string, error)

// DefaultProviderBindingTTL is how long a resolved provider name is reused.
// Which provider a workspace binds changes only when someone enables or
// disables one, so a few minutes of staleness costs a failed call at worst
// and saves a List on every data-plane hop.
const DefaultProviderBindingTTL = 5 * time.Minute

// ProviderResolver answers "which provider serves APIExport X in this
// workspace" from the workspace's own APIBindings.
//
// The read is done as the caller — a caller who cannot see the binding cannot
// use the provider either, so there is no reason to hold a provider identity
// for it — and the answer is cached per (cluster, export) rather than per
// token, because it is a property of the workspace and not of who asked.
type ProviderResolver struct {
	hubBase  string
	insecure bool
	ttl      time.Duration

	mu  sync.Mutex
	hot map[string]providerEntry
}

type providerEntry struct {
	provider  string
	expiresAt time.Time
}

// NewProviderResolver builds a resolver against a hub. ttl <= 0 takes
// DefaultProviderBindingTTL.
func NewProviderResolver(hubBase string, insecure bool, ttl time.Duration) *ProviderResolver {
	if ttl <= 0 {
		ttl = DefaultProviderBindingTTL
	}
	return &ProviderResolver{hubBase: strings.TrimRight(strings.TrimSpace(hubBase), "/"), insecure: insecure, ttl: ttl, hot: map[string]providerEntry{}}
}

// Resolve returns the provider name serving exportName in clusterID.
func (r *ProviderResolver) Resolve(ctx context.Context, clusterID, token, exportName string) (string, error) {
	if r == nil {
		return "", fmt.Errorf("provider binding resolver is unavailable")
	}
	if !dataplane.IsClusterID(clusterID) {
		return "", fmt.Errorf("%q is not a kcp logical-cluster ID", clusterID)
	}
	key := clusterID + "\x00" + exportName
	now := time.Now()

	r.mu.Lock()
	entry, ok := r.hot[key]
	r.mu.Unlock()
	if ok && now.Before(entry.expiresAt) {
		return entry.provider, nil
	}

	provider, err := r.lookup(ctx, clusterID, token, exportName)
	if err != nil {
		return "", err
	}

	r.mu.Lock()
	r.hot[key] = providerEntry{provider: provider, expiresAt: now.Add(r.ttl)}
	r.mu.Unlock()
	return provider, nil
}

// lookup reads the workspace's APIBinding for exportName as the caller and
// returns the provider name it addresses.
func (r *ProviderResolver) lookup(ctx context.Context, clusterID, token, exportName string) (string, error) {
	candidate := crossprovider.ProviderNameForExport(exportName)
	if candidate == "" {
		return "", fmt.Errorf("%q is not an APIExport a provider name can be derived from", exportName)
	}
	cfg, err := tenantaccess.RESTConfig(r.hubBase, clusterID, token, r.insecure)
	if err != nil {
		return "", err
	}
	client, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return "", fmt.Errorf("building APIBinding client for cluster %q: %w", clusterID, err)
	}
	binding, err := client.Resource(crossprovider.APIBindingsGVR).Get(ctx, candidate, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return "", fmt.Errorf("workspace %s binds no provider serving %s — enable the dependency in this workspace first", clusterID, exportName)
	}
	if err != nil {
		return "", fmt.Errorf("reading APIBinding %q in cluster %q: %w", candidate, clusterID, err)
	}
	return providerForBinding(binding, exportName, clusterID)
}

// providerForBinding is the pure half of lookup: the consistency assertion
// that this binding really is the one that makes exportName reachable, and
// therefore that its own name is the /services/providers/{name}/ segment that
// addresses it.
func providerForBinding(binding *unstructured.Unstructured, exportName, clusterID string) (string, error) {
	if binding == nil {
		return "", fmt.Errorf("workspace %s binds no provider serving %s — enable the dependency in this workspace first", clusterID, exportName)
	}
	bound, _, _ := unstructured.NestedString(binding.Object, "spec", "reference", "export", "name")
	if bound != exportName {
		return "", fmt.Errorf("APIBinding %q in workspace %s serves %q, not %s; which provider serves the dependency here is unknown", binding.GetName(), clusterID, bound, exportName)
	}
	name := strings.TrimSpace(binding.GetName())
	if name == "" {
		return "", fmt.Errorf("the APIBinding serving %s in workspace %s has no name", exportName, clusterID)
	}
	return name, nil
}

// providerLookupFor wires the production resolver, or nil when there is no hub
// to ask (bare dev / tests). A nil lookup makes every cross-provider call
// report that it cannot address the dependency, which is the honest answer:
// without the binding there is no coordinate to call.
func providerLookupFor(hubBase string, insecure bool) providerLookup {
	if strings.TrimSpace(hubBase) == "" {
		return nil
	}
	return NewProviderResolver(hubBase, insecure, 0).Resolve
}

// providerFor resolves the provider serving exportName for this request's
// workspace.
func (s *Server) providerFor(ctx context.Context, id identity, exportName string) (string, error) {
	if s == nil || s.tenantProviders == nil {
		return "", fmt.Errorf("cannot resolve which provider serves %s: no hub configured to read this workspace's APIBindings", exportName)
	}
	return s.tenantProviders(ctx, id.clusterID, id.token, exportName)
}

// callerFactoryFor builds the data plane's caller factory against the hub, or
// nil when there is no hub to reach (bare dev / tests). A nil factory makes
// dataplane.Gate refuse every request, which is the honest failure: without a
// way to check the caller's own RBAC there is nothing to authorize against.
func callerFactoryFor(hubBase string, insecure bool) dataplane.CallerFactory {
	if strings.TrimSpace(hubBase) == "" {
		return nil
	}
	// The hub's CA travels in the pod's trust store; insecure mirrors the
	// RAILGRID_HUB_INSECURE knob every other hub client here honours.
	callers, err := dataplane.NewHubCallerFactory(hubBase, nil, insecure)
	if err != nil {
		return nil
	}
	return callers
}
