/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package providers

import (
	"net/http"
	"net/http/httputil"
	"strings"

	"github.com/faroshq/faros/pkg/kcppaths"
)

// Org-scoped resolution and edge-fronted transport for the backend proxy.
//
// A platform provider is a URL the hub dials. An org-owned one is a workload in
// the tenant's own cluster, behind NAT, reachable only through the edge agent's
// reverse tunnel. Both are addressed as /services/providers/{name}/**, so the
// difference has to be resolved here.

// resolveProvider picks the copy of name that applies to this caller.
//
// The backend proxy resolves in the caller's Org first, so an Org that
// self-hosts a provider reaches its own. The UI proxy does not: it serves
// assets embedded in the hub or fetched from a platform URL, and an org's
// bundle is not something the hub hosts, so org-scoping there would only
// produce 404s where the platform asset used to work.
func (p *ProviderProxy) resolveProvider(r *http.Request, name string) (Provider, bool) {
	if p.fallbackForSPA || p.tenantResolver == nil {
		return p.reg.Get(name)
	}
	orgUUID := p.callerOrgUUID(r)
	if orgUUID == "" {
		return p.reg.Get(name)
	}
	return p.reg.GetForOrg(orgUUID, name)
}

// callerOrgUUID derives the caller's Org from the tenant workspace path the
// resolver returns, which is Organization.Status.WorkspacePath —
// root:faros:tenants:{orgUUID}[:{wsUUID}].
//
// Returns "" on any doubt. Every caller falls back to platform-scoped
// resolution in that case, which is the pre-existing behaviour: unresolvable
// identity must not silently widen what a request can reach.
func (p *ProviderProxy) callerOrgUUID(r *http.Request) string {
	_, tenantPath, err := p.tenantResolver.Resolve(r)
	if err != nil || tenantPath == "" {
		return ""
	}
	rest := strings.TrimPrefix(tenantPath, kcppaths.TenantsParent+":")
	if rest == tenantPath {
		return ""
	}
	orgUUID, _, _ := strings.Cut(rest, ":")
	return orgUUID
}

// serveOverEdge forwards one request to an org-owned provider through the
// platform edges provider's tunnel.
//
// Rather than making a second HTTP hop through the hub's own front door, this
// rewrites the target to the edges provider's backend and hands it the path its
// handler expects. One less hop, and the caller's identity headers are injected
// exactly once, by the same code that injects them for a platform provider.
func (p *ProviderProxy) serveOverEdge(w http.ResponseWriter, r *http.Request, prov Provider, rest string) {
	route := prov.EdgeRoute
	if !route.Usable() {
		// Recorded but not yet resolvable — the workspace's cluster ID is
		// missing, so there is no address to build. 503 rather than falling
		// back to BackendURL: that address lives in the tenant's cluster, and
		// dialling it from here is exactly the confusion this path exists to
		// remove.
		p.log.Info("org-owned provider has no usable edge route yet",
			"provider", prov.Name, "org", prov.OrgUUID)
		http.Error(w, "provider backend is not routable yet: "+prov.Name, http.StatusServiceUnavailable)
		return
	}

	// The tunnel is platform infrastructure. Resolve the edges provider from
	// the PLATFORM registry, never org-scoped: an org supplying the transport
	// for its own traffic would sit on both ends of the trust boundary.
	edges, ok := p.reg.Get(EdgesProviderName)
	if !ok || edges.BackendURL == nil {
		p.log.Info("edge transport unavailable: the platform edges provider has no backend",
			"provider", prov.Name, "org", prov.OrgUUID)
		http.Error(w, "edge transport unavailable for provider: "+prov.Name, http.StatusServiceUnavailable)
		return
	}

	target := *edges.BackendURL
	edgePath := route.EdgeProxyPath(rest)
	basePath := p.pathPrefix + "/" + prov.Name

	rp := &httputil.ReverseProxy{
		// Same reason as the direct path, and more acute here: the dataplane
		// log verb streams, and an unflushed reverse proxy in front of a
		// tunnel turns "tail my logs" into "hang". See E-7.
		FlushInterval: -1,
		Director: func(req *http.Request) {
			req.URL.Scheme = target.Scheme
			req.URL.Host = target.Host
			req.URL.Path = singleJoiningSlash(target.Path, edgePath)
			req.URL.RawPath = ""
			req.Host = target.Host
			// Identity is injected under the ORG provider's name, not the
			// edges provider's: the headers describe who is calling the
			// provider at the far end of the tunnel, and the agent forwards
			// them untouched (E-6).
			p.setHeaders(req, prov.Name, basePath)
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			p.log.Error(err, "edge upstream error",
				"provider", prov.Name, "org", prov.OrgUUID, "edge", route.EdgeName, "service", route.ServiceName)
			http.Error(w, "provider upstream error", http.StatusBadGateway)
		},
	}
	p.log.V(4).Info("forwarding provider request over edge",
		"provider", prov.Name, "org", prov.OrgUUID, "edge", route.EdgeName, "path", edgePath)
	rp.ServeHTTP(w, r)
}
