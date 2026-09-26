/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package providers

import (
	"context"
	"strings"

	"github.com/railgrid/railgrid/pkg/apiurl"
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
	// edges verb path addresses. Resolved by the catalog reconciler; a route
	// with an empty Cluster is not usable and is treated as absent.
	Cluster string
	// EdgeName is the KubernetesCluster edge whose agent carries the tunnel.
	EdgeName string
	// ServiceName is the hub-owned edges.railgrid.ai/Service in front of the
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

// EdgeProxyPath returns the kcp path that carries one request to this
// provider's backend, with rest appended: the edges provider's
// services/{name}/proxy custom subresource in the route's workspace,
//
//	/clusters/{cluster}/apis/edges.railgrid.ai/v1alpha1/services/{service}/proxy{rest}
//
// The hub dials kcp for this hop exactly as any other caller of a verb would:
// kcp authorizes it and reverse-proxies it to the edges provider, which
// carries it down the tunnel. There is no hub→provider grammar left to take
// instead.
func (e *EdgeRoute) EdgeProxyPath(rest string) string {
	base := apiurl.EdgeServiceProxyPath(e.Cluster, e.ServiceName, "proxy")
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
