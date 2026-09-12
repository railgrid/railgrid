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
	"errors"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"github.com/faroshq/faros/pkg/hub/serviceaccounts"
	"github.com/faroshq/faros/pkg/kcppaths"
)

// DelegatedTokenIssuer mints the credential the hub sends to an org-owned
// provider in place of the caller's own hub bearer: a ServiceAccount token in
// the caller's tenant workspace, annotated with the human user it stands in
// for. Implemented by *serviceaccounts.Manager.
type DelegatedTokenIssuer interface {
	IssueDelegatedUserToken(ctx context.Context, orgUUID, wsUUID string, user serviceaccounts.Identity, provider serviceaccounts.DelegatedProvider) (string, time.Time, error)
}

// DelegatedTokenIssuerFunc adapts a plain function to DelegatedTokenIssuer.
type DelegatedTokenIssuerFunc func(ctx context.Context, orgUUID, wsUUID string, user serviceaccounts.Identity, provider serviceaccounts.DelegatedProvider) (string, time.Time, error)

// IssueDelegatedUserToken satisfies DelegatedTokenIssuer.
func (f DelegatedTokenIssuerFunc) IssueDelegatedUserToken(ctx context.Context, orgUUID, wsUUID string, user serviceaccounts.Identity, provider serviceaccounts.DelegatedProvider) (string, time.Time, error) {
	return f(ctx, orgUUID, wsUUID, user, provider)
}

// SetDelegatedTokenIssuer installs the issuer used on the org-owned provider
// path. Wire alongside SetTenantResolver. Without one, every authenticated
// request to an org-owned provider is refused rather than forwarded with the
// caller's hub token.
func (p *ProviderProxy) SetDelegatedTokenIssuer(i DelegatedTokenIssuer) {
	p.delegatedIssuer = i
}

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
	_, tenantPath, err := p.resolveCaller(r)
	if err != nil || tenantPath == "" {
		return ""
	}
	orgUUID, _ := splitTenantPath(tenantPath)
	return orgUUID
}

// splitTenantPath takes root:faros:tenants:{org}[:{ws}] apart. Both results
// are empty when the path is not under the tenants parent; ws is empty for an
// org-scope path.
func splitTenantPath(tenantPath string) (orgUUID, wsUUID string) {
	rest := strings.TrimPrefix(tenantPath, kcppaths.TenantsParent+":")
	if rest == tenantPath {
		return "", ""
	}
	orgUUID, wsUUID, _ = strings.Cut(rest, ":")
	return orgUUID, wsUUID
}

// delegatedAuthorization decides what Authorization a provider receives for r
// when the caller's bearer must not reach it, writing the response itself when
// the request must not go on. It returns the delegated bearer, or "" for an
// anonymous caller (whose request carried nothing to substitute), and whether
// to proceed.
//
// For an org-owned provider this is the boundary the whole file exists to
// hold: the far end of the tunnel is a workload in a tenant's cluster,
// installed by whichever member registered it, so the caller's own hub token —
// good for every workspace and every REST endpoint they can reach — must never
// cross it. A platform provider under a delegating policy (proxy_delegation.go)
// gets the same treatment for the same reason in weaker form: it is trusted
// code, but a bug in it should be able to act in one workspace, not as the
// user everywhere. What crosses instead is a ServiceAccount token scoped by
// kcp to the caller's current workspace, carrying the caller's name in
// annotations for the provider to attribute the call. Every failure here is
// closed: no identity, no workspace, no issuer, or a mint error all refuse
// rather than fall back to forwarding the bearer.
//
// The delegated account is minted in the workspace the caller selected
// (X-Faros-Workspace, verified against their membership by the resolver). An
// org-scope selection has nowhere to mint it — org workspaces are sealed
// (O-10) and the hub's SA proxy path refuses tokens bound there — so it is
// refused for platform providers exactly as for org-owned ones.
func (p *ProviderProxy) delegatedAuthorization(w http.ResponseWriter, r *http.Request, prov Provider) (string, bool) {
	if strings.TrimSpace(r.Header.Get("Authorization")) == "" {
		// Anonymous probe (health checks). There is no credential to
		// protect; the provider sees an unauthenticated request, as today.
		return "", true
	}
	user, tenantPath, err := p.resolveCaller(r)
	if err != nil || user == "" {
		p.log.Info("refusing provider request: caller identity unresolved",
			"provider", prov.Name, "org", prov.OrgUUID, "err", errString(err))
		http.Error(w, "caller identity could not be established for provider: "+prov.Name, http.StatusForbidden)
		return "", false
	}
	orgUUID, wsUUID := splitTenantPath(tenantPath)
	token, refusal := issueDelegatedToken(r.Context(), p.delegatedIssuer, prov,
		DelegatedCaller{User: user, OrgUUID: orgUUID, WorkspaceUUID: wsUUID})
	if refusal != nil {
		if refusal.err != nil {
			p.log.Error(refusal.err, "issuing delegated user token",
				"provider", prov.Name, "org", prov.OrgUUID, "workspace", wsUUID, "user", user)
		} else if refusal.status == http.StatusServiceUnavailable {
			p.log.Info("refusing provider request: "+refusal.reason,
				"provider", prov.Name, "org", prov.OrgUUID)
		}
		http.Error(w, refusal.message, refusal.status)
		return "", false
	}
	return token, true
}

// DelegatedCaller is the verified human a delegated token is minted for: the
// hub User name plus the tenant workspace the token will be scoped to. Callers
// must have authenticated the user and verified their membership in
// (OrgUUID, WorkspaceUUID) before building one — issuance mints on the hub's
// own authority and re-checks neither.
type DelegatedCaller struct {
	User          string
	OrgUUID       string
	WorkspaceUUID string
}

// delegationRefusal says why no delegated token was issued. status and message
// are what the backend proxy answers with; reason and err are for the log.
type delegationRefusal struct {
	status  int
	message string
	reason  string
	err     error
}

func (d *delegationRefusal) Error() string {
	if d.err != nil {
		return d.reason + ": " + d.err.Error()
	}
	return d.reason
}

// issueDelegatedToken is the single decision point for replacing a caller's
// bearer with a delegated token. The backend proxy (delegatedAuthorization)
// and hub-originated requests to org-owned providers (OrgProviderRoute, used
// by the MCP aggregate) both go through it, so the rules cannot drift between
// the two paths. It never returns a token for a tuple it has not checked, and
// there is no fallback: a refusal means the request does not go out.
func issueDelegatedToken(ctx context.Context, issuer DelegatedTokenIssuer, prov Provider, caller DelegatedCaller) (string, *delegationRefusal) {
	if caller.User == "" {
		return "", &delegationRefusal{status: http.StatusForbidden, reason: "caller identity unresolved",
			message: "caller identity could not be established for provider: " + prov.Name}
	}
	if caller.OrgUUID == "" {
		return "", &delegationRefusal{status: http.StatusForbidden, reason: "caller has no tenant workspace",
			message: "caller has no tenant workspace to act from for provider: " + prov.Name}
	}
	if prov.OrgUUID != "" && caller.OrgUUID != prov.OrgUUID {
		// Resolution picked this provider from the caller's own org, so a
		// mismatch means something other than that resolution routed us.
		// Refuse. A platform provider has no owning org; the caller's own is
		// where the token is minted.
		return "", &delegationRefusal{status: http.StatusForbidden, reason: "caller is outside the owning organization",
			message: "caller is not in the organization that owns provider: " + prov.Name}
	}
	if caller.WorkspaceUUID == "" {
		// The delegated account lives in a team workspace; an org-scope
		// resolution (no X-Faros-Workspace) has nowhere to mint it. The portal
		// sends the workspace header on provider calls whenever a workspace
		// is selected.
		return "", &delegationRefusal{status: http.StatusForbidden, reason: "no team workspace to mint in",
			message: "a workspace selection (X-Faros-Workspace) is required to reach provider: " + prov.Name}
	}
	if issuer == nil {
		return "", &delegationRefusal{status: http.StatusServiceUnavailable, reason: "no delegated token issuer wired",
			message: "delegated identity unavailable for provider: " + prov.Name}
	}
	token, _, err := issuer.IssueDelegatedUserToken(ctx, caller.OrgUUID, caller.WorkspaceUUID, serviceaccounts.Identity{User: caller.User}, serviceaccounts.DelegatedProvider{Name: prov.Name, OrgUUID: prov.OrgUUID})
	if err != nil {
		return "", &delegationRefusal{status: http.StatusServiceUnavailable, reason: "issuing delegated user token", err: err,
			message: "delegated identity unavailable for provider: " + prov.Name}
	}
	if strings.TrimSpace(token) == "" {
		return "", &delegationRefusal{status: http.StatusServiceUnavailable, reason: "issuer returned an empty token",
			message: "delegated identity unavailable for provider: " + prov.Name}
	}
	return token, nil
}

// setDelegatedAuthorization replaces whatever Authorization h carries with the
// delegated token, or removes it when token is empty (an anonymous probe).
// Every path that talks to a provider on a caller's behalf without forwarding
// their bearer ends here, so "the bearer that arrived never crosses" is one
// line rather than one line per path.
func setDelegatedAuthorization(h http.Header, token string) {
	h.Del("Authorization")
	if token != "" {
		h.Set("Authorization", "Bearer "+token)
	}
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

var (
	// errEdgeRouteUnusable: the provider's edge route is recorded but not yet
	// resolvable (no workspace cluster ID), or absent.
	errEdgeRouteUnusable = errors.New("provider backend is not routable yet")
	// errEdgeTransportUnavailable: the platform edges provider, which carries
	// the tunnel, has no backend.
	errEdgeTransportUnavailable = errors.New("edge transport unavailable")
)

// edgeHop is where an org-owned provider's backend is reached from the hub:
// the PLATFORM edges provider's backend, at the edge-proxy path of the
// hub-owned Service fronting the provider in the tenant's cluster.
type edgeHop struct {
	target url.URL
	route  EdgeRoute
}

// resolveEdgeHop finds the edge hop for prov. It never falls back to
// BackendURL: that address lives in the tenant's cluster, and dialling it from
// here is exactly the confusion the edge route exists to remove.
//
// The tunnel is platform infrastructure, so the edges provider is resolved from
// the PLATFORM registry, never org-scoped: an org supplying the transport for
// its own traffic would sit on both ends of the trust boundary.
func resolveEdgeHop(reg *Registry, prov Provider) (edgeHop, error) {
	if !prov.EdgeRoute.Usable() {
		return edgeHop{}, errEdgeRouteUnusable
	}
	edges, ok := reg.Get(EdgesProviderName)
	if !ok || edges.BackendURL == nil {
		return edgeHop{}, errEdgeTransportUnavailable
	}
	return edgeHop{target: *edges.BackendURL, route: *prov.EdgeRoute}, nil
}

// url returns the edges-provider URL that carries rest (a path relative to the
// provider's backend root) through the tunnel. The query is left to the caller.
func (h edgeHop) url(rest string) *url.URL {
	u := h.target
	u.Path = singleJoiningSlash(h.target.Path, h.route.EdgeProxyPath(rest))
	u.RawPath = ""
	u.RawQuery = ""
	u.Fragment = ""
	return &u
}

// serveOverEdge forwards one request to an org-owned provider through the
// platform edges provider's tunnel.
//
// Rather than making a second HTTP hop through the hub's own front door, this
// rewrites the target to the edges provider's backend and hands it the path its
// handler expects. One less hop, and the caller's identity headers are injected
// exactly once, by the same code that injects them for a platform provider.
//
// This is also the only route to an org-owned provider's backend — ServeHTTP
// sends every provider with an OrgUUID here, and a missing edge route is a 503,
// never a fallback to BackendURL — so the bearer substitution in
// delegatedAuthorization covers every request such a provider can receive.
func (p *ProviderProxy) serveOverEdge(w http.ResponseWriter, r *http.Request, prov Provider, rest string) {
	hop, err := resolveEdgeHop(p.reg, prov)
	switch {
	case errors.Is(err, errEdgeRouteUnusable):
		// Recorded but not yet resolvable — the workspace's cluster ID is
		// missing, so there is no address to build. 503 rather than falling
		// back to BackendURL (see resolveEdgeHop).
		p.log.Info("org-owned provider has no usable edge route yet",
			"provider", prov.Name, "org", prov.OrgUUID)
		http.Error(w, "provider backend is not routable yet: "+prov.Name, http.StatusServiceUnavailable)
		return
	case err != nil:
		p.log.Info("edge transport unavailable: the platform edges provider has no backend",
			"provider", prov.Name, "org", prov.OrgUUID)
		http.Error(w, "edge transport unavailable for provider: "+prov.Name, http.StatusServiceUnavailable)
		return
	}

	delegated, ok := p.delegatedAuthorization(w, r, prov)
	if !ok {
		return
	}

	dst := hop.url(rest)
	basePath := p.pathPrefix + "/" + prov.Name

	rp := &httputil.ReverseProxy{
		// Same reason as the direct path, and more acute here: the dataplane
		// log verb streams, and an unflushed reverse proxy in front of a
		// tunnel turns "tail my logs" into "hang". See E-7.
		FlushInterval: -1,
		Director: func(req *http.Request) {
			req.URL.Scheme = dst.Scheme
			req.URL.Host = dst.Host
			req.URL.Path = dst.Path
			req.URL.RawPath = ""
			req.Host = dst.Host
			// Identity is injected under the ORG provider's name, not the
			// edges provider's: the headers describe who is calling the
			// provider at the far end of the tunnel, and the agent forwards
			// them untouched (E-6).
			p.setHeaders(req, prov.Name, basePath)
			// The caller's hub token stops here. What the tenant's cluster
			// receives is the delegated ServiceAccount token, or nothing for
			// an anonymous probe — never the bearer that arrived.
			setDelegatedAuthorization(req.Header, delegated)
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			p.log.Error(err, "edge upstream error",
				"provider", prov.Name, "org", prov.OrgUUID, "edge", hop.route.EdgeName, "service", hop.route.ServiceName)
			http.Error(w, "provider upstream error", http.StatusBadGateway)
		},
	}
	p.log.V(4).Info("forwarding provider request over edge",
		"provider", prov.Name, "org", prov.OrgUUID, "edge", hop.route.EdgeName, "path", dst.Path)
	rp.ServeHTTP(w, r)
}
