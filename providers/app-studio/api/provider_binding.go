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
	"net/http"
	"strings"
	"sync"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/railgrid/provider-sdk/dataplane"

	"github.com/railgrid/provider-app-studio/internal/crossprovider"
)

// A cross-provider call no longer needs to know which PROVIDER serves a
// dependency in a workspace. App Studio calls another provider's verb through
// its OWN APIExport virtual workspace, as itself, at the kube coordinate of
// the claimed custom subresource
// (…/clusters/{tenant}/apis/{group}/{version}/{resource}/{name}/{verb}); kcp
// resolves the claim per consumer workspace against whichever copy of the
// dependency that workspace bound, and forwards the call there. The
// /services/providers/{name}/ segment that once had to be read off the
// tenant's APIBinding (cross-provider-simplification X-8) is gone with the
// grammar that carried it.
//
// What remains useful is the question the binding also answered: is the
// dependency enabled in this workspace at all? A claimed kind is served to
// this provider's virtual workspace exactly when the consumer's binding has
// accepted the claim, so a LIST of that kind through the export answers it —
// crossprovider.ClaimUnaccepted is the "no" (a RESTMapper miss or a 403, not
// an object 404).

// infraAPIExportName / codeAPIExportName are the APIExports the dependencies
// serve. App Studio's dependencies on them are declared in manifest.yaml
// (the spec.requires entries that name a provider); these name what a workspace
// binds when it enables each one, whichever copy of the provider it enabled.
const (
	infraAPIExportName = crossprovider.InfrastructureAPIExport
	codeAPIExportName  = crossprovider.CodeAPIExport
)

// infraDependencyName is the dependency App Studio declares on the
// infrastructure provider in manifest.yaml (spec.requires). It is a
// LABEL — it names the dependency in the project view — and is never a URL
// segment.
const infraDependencyName = "infrastructure"

// dependencyProbe is the kind a LIST through the export proves each
// dependency is served with. Both are claimed kinds (manifest.yaml
// spec.requires), so the workspace serving them is the workspace that accepted
// the dependency's claims.
var dependencyProbe = map[string]schema.GroupVersionResource{
	infraAPIExportName: {Group: crossprovider.InfrastructureAPIGroup, Version: "v1alpha1", Resource: crossprovider.InstancesResource},
	codeAPIExportName:  {Group: crossprovider.CodeAPIGroup, Version: "v1alpha1", Resource: crossprovider.RepositoriesResource},
}

// providerLookup reports whether exportName is enabled in a cluster, returning
// the dependency's LABEL (the provider name as this manifest knows it) when
// it is. Production wires DependencyResolver; tests substitute a table.
type providerLookup func(ctx context.Context, clusterID, exportName string) (string, error)

// DefaultProviderBindingTTL is how long a resolved answer is reused. Which
// providers a workspace binds changes only when someone enables or disables
// one, so a few minutes of staleness costs a failed call at worst.
const DefaultProviderBindingTTL = 5 * time.Minute

// DependencyResolver answers "is dependency X enabled in this workspace" by
// listing one of the kinds X serves through this provider's own export.
type DependencyResolver struct {
	callers dataplane.ProviderCallerFactory
	ttl     time.Duration

	mu  sync.Mutex
	hot map[string]providerEntry
}

type providerEntry struct {
	provider  string
	expiresAt time.Time
}

// NewDependencyResolver builds a resolver over the provider's caller factory.
// ttl <= 0 takes DefaultProviderBindingTTL.
func NewDependencyResolver(callers dataplane.ProviderCallerFactory, ttl time.Duration) *DependencyResolver {
	if ttl <= 0 {
		ttl = DefaultProviderBindingTTL
	}
	return &DependencyResolver{callers: callers, ttl: ttl, hot: map[string]providerEntry{}}
}

// Resolve reports the dependency label for exportName in clusterID, or an
// error saying the workspace does not serve it.
func (r *DependencyResolver) Resolve(ctx context.Context, clusterID, exportName string) (string, error) {
	if r == nil || r.callers == nil {
		return "", fmt.Errorf("dependency resolver is unavailable")
	}
	if !dataplane.IsClusterID(clusterID) {
		return "", fmt.Errorf("%q is not a kcp logical-cluster ID", clusterID)
	}
	probe, ok := dependencyProbe[exportName]
	if !ok {
		return "", fmt.Errorf("%q is not a dependency this provider declares", exportName)
	}
	key := clusterID + "\x00" + exportName
	now := time.Now()

	r.mu.Lock()
	entry, ok := r.hot[key]
	r.mu.Unlock()
	if ok && now.Before(entry.expiresAt) {
		return entry.provider, nil
	}

	provider, err := r.callers.AsProvider(clusterID)
	if err != nil {
		return "", err
	}
	if _, err := provider.Resource(probe).List(ctx, metav1.ListOptions{Limit: 1}); err != nil {
		if crossprovider.ClaimUnaccepted(err) {
			return "", fmt.Errorf("workspace %s binds no provider serving %s — enable the dependency in this workspace first", clusterID, exportName)
		}
		return "", fmt.Errorf("probing %s in cluster %q: %w", exportName, clusterID, err)
	}
	name := crossprovider.ProviderNameForExport(exportName)

	r.mu.Lock()
	r.hot[key] = providerEntry{provider: name, expiresAt: now.Add(r.ttl)}
	r.mu.Unlock()
	return name, nil
}

// providerLookupFor wires the production resolver, or nil when there is no
// provider credential (bare dev / tests). A nil lookup makes every dependency
// check report that it cannot tell, which is the honest answer.
func providerLookupFor(callers dataplane.ProviderCallerFactory) providerLookup {
	if callers == nil {
		return nil
	}
	return NewDependencyResolver(callers, 0).Resolve
}

// providerFor reports whether the dependency serving exportName is enabled in
// this request's workspace, returning its label.
func (s *Server) providerFor(ctx context.Context, id identity, exportName string) (string, error) {
	if s == nil || s.tenantProviders == nil {
		return "", fmt.Errorf("cannot tell whether %s is enabled: no provider credential to read this workspace with", exportName)
	}
	return s.tenantProviders(ctx, id.clusterID, exportName)
}

// providerCallers is what the data plane needs of the provider's caller
// factory: to act as the provider in a tenant workspace (the gate, every
// handler's client), and to call a verb ANOTHER provider serves — a custom
// subresource this provider has claimed — through its own export virtual
// workspace with its own credential. *dataplane.Callers is the production
// implementation.
type providerCallers interface {
	dataplane.ProviderCallerFactory
	ExportVerbURL(ctx context.Context, gvr schema.GroupVersionResource, r dataplane.Request) (string, error)
	ProviderHTTPClient() (*http.Client, error)
}

// UseProviderCallers hands the server this provider's OWN authenticated kcp
// connection, wrapped as the caller factory every verb acts through.
//
// There is no other credential on the data plane: the shard authenticates the
// caller and stamps their identity, handing over no bearer, so the gate
// decides visibility with a SubjectAccessReview the provider runs on the
// caller's behalf and every handler then acts as the provider through its
// export virtual workspace. The tenant client, the workspace lookup and the
// dependency check all ride the same factory.
//
// A nil factory leaves the server as it was, which keeps every verb failing
// closed (dataplane.Gate refuses without a factory).
func (s *Server) UseProviderCallers(callers providerCallers) {
	if s == nil || callers == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.callers = callers
	s.tenant = tenantClientFor(callers)
	if s.tenantWorkspaces == nil {
		s.tenantWorkspaces = workspaceLookupFor(callers)
	}
	if s.tenantProviders == nil {
		s.tenantProviders = providerLookupFor(callers)
	}
}

// hubTokenFrom trims a bearer for the hub REST calls this provider makes as
// itself.
func hubTokenFrom(token string) string { return strings.TrimSpace(token) }
