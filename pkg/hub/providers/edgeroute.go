/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package providers

import (
	"context"
	"strings"

	"github.com/faroshq/faros/pkg/apiurl"
)

// EdgeRoute says an org-owned provider's backend is reached over its edge
// tunnel instead of by dialling a URL.
//
// Mirrors kcp.EdgeRoute rather than importing it, matching how ProviderClaim is
// mirrored in the other direction: pkg/hub/kcp deliberately does not depend on
// this package, and this package should not gain a dependency on the bootstrap
// surface just to name three strings.
type EdgeRoute struct {
	// WorkspaceUUID is the team workspace holding the edge and the Service.
	WorkspaceUUID string
	// Cluster is that workspace's kcp logical-cluster ID, which is what the
	// edges proxy path addresses. Resolved by the catalog reconciler; a route
	// with an empty Cluster is not usable and is treated as absent.
	Cluster string
	// EdgeName is the KubernetesCluster edge whose agent carries the tunnel.
	EdgeName string
	// ServiceName is the hub-owned edges.faros.sh/Service in front of the
	// provider inside the tenant's cluster.
	ServiceName string
}

// Usable reports whether the route can actually be addressed. A route recorded
// at registration but whose workspace cluster has not been resolved yet is not
// usable, and a provider with one must 503 rather than fall back to dialling
// BackendURL — that address is inside the tenant's cluster, so falling back
// would mean the hub dialling something it cannot reach and, worse, would make
// routing depend on a field the tenant controls.
func (e *EdgeRoute) Usable() bool {
	return e != nil && e.Cluster != "" && e.ServiceName != ""
}

// EdgeProxyPath returns the path on the edges provider that carries one request
// to this provider's backend, with rest appended.
//
// The hub does not make a second HTTP hop through its own front door for this:
// it rewrites the target to the edges provider's own backend and hands it the
// path that provider's handler expects, which is EdgeServiceProxyPath minus the
// /services/providers prefix the hub would have stripped anyway.
func (e *EdgeRoute) EdgeProxyPath(rest string) string {
	full := apiurl.EdgeServiceProxyPath(e.Cluster, e.ServiceName, "proxy")
	base := strings.TrimPrefix(full, apiurl.PathPrefixProvidersProxy+"/"+EdgesProviderName)
	if rest == "" || rest == "/" {
		return base
	}
	if !strings.HasPrefix(rest, "/") {
		rest = "/" + rest
	}
	return base + rest
}

// EdgesProviderName is the platform provider that owns the tunnel. An org-owned
// provider's data plane is carried by the platform's edges provider, never by
// an org-owned copy of it: the tunnel is platform infrastructure, and letting
// an org supply the transport for its own traffic would put the org on both
// ends of the trust boundary.
const EdgesProviderName = "edges"

// EdgeRouteResolver reads back the edge binding the hub recorded at
// registration. Implemented by *kcp.Bootstrapper; declared as an interface so
// the catalog reconciler depends on the capability rather than the bootstrapper.
type EdgeRouteResolver interface {
	// ResolveProviderEdgeRoute returns the route for an org-owned provider, or
	// nil when it has none. backendURL is the address the provider published
	// about itself, from which the in-cluster Service target is derived and
	// validated; implementations reconcile the hub-owned Service as a side
	// effect so the tunnel has something to land on.
	ResolveProviderEdgeRoute(ctx context.Context, orgUUID, providerName, backendURL string) (*EdgeRoute, error)
}
