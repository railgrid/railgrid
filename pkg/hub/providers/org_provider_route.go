/*
Copyright 2026 The Faros Authors.

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
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// Hub-originated requests to org-owned providers.
//
// The backend proxy is not the only thing in the hub that talks to a provider's
// backend on a caller's behalf: the MCP aggregate federates each provider's
// /mcp endpoint itself. For a platform provider that is a direct dial. For an
// org-owned one it must be exactly what serveOverEdge does — the edge hop, and
// a delegated token in place of the caller's bearer — or the aggregate becomes
// the side door around everything proxy_edge.go enforces. OrgProviderRoute
// hands out that same hop and token as an http.RoundTripper, so there is one
// implementation of the boundary, not two.

// OrgProviderRoute is how one hub-originated request reaches one org-owned
// provider's backend on behalf of one caller.
type OrgProviderRoute struct {
	// BaseURL addresses the provider's backend root through the platform
	// edges provider. Append the provider-relative path (e.g. "/mcp").
	BaseURL string
	// Transport sends requests addressed under BaseURL with the caller's
	// delegated token as Authorization, whatever Authorization the request
	// carried. It refuses any request not addressed under BaseURL, so the
	// delegated token cannot be pointed anywhere else.
	Transport http.RoundTripper
}

// errNotOrgOwned: OrgProviderRoute was asked for a platform provider, which the
// hub dials directly and which this path must not silently start delegating.
var errNotOrgOwned = errors.New("not an org-owned provider")

// OrgProviderRoute resolves the route for a hub-originated request to the
// org-owned provider prov on behalf of caller: the same edge hop serveOverEdge
// uses and the same delegated token delegatedAuthorization mints, with the
// same refusals (no user, no org, another org's provider, no team workspace,
// no issuer, mint failure). Every failure is an error. There is no fallback to
// BackendURL and none to a caller-supplied credential — the caller's bearer is
// not an input here at all.
//
// The caller must already be authenticated and its membership in
// (OrgUUID, WorkspaceUUID) verified; see DelegatedCaller.
func (p *ProviderProxy) OrgProviderRoute(ctx context.Context, prov Provider, caller DelegatedCaller) (OrgProviderRoute, error) {
	if prov.OrgUUID == "" {
		return OrgProviderRoute{}, fmt.Errorf("provider %q: %w", prov.Name, errNotOrgOwned)
	}
	hop, err := resolveEdgeHop(p.reg, prov)
	if err != nil {
		return OrgProviderRoute{}, fmt.Errorf("provider %q: %w", prov.Name, err)
	}
	token, refusal := issueDelegatedToken(ctx, p.delegatedIssuer, prov, caller)
	if refusal != nil {
		return OrgProviderRoute{}, fmt.Errorf("provider %q: %w", prov.Name, refusal)
	}
	base := hop.url("/")
	return OrgProviderRoute{
		BaseURL: base.String(),
		Transport: &delegatedEdgeTransport{
			base:     http.DefaultTransport,
			scheme:   base.Scheme,
			host:     base.Host,
			basePath: base.Path,
			token:    token,
			user:     caller.User,
		},
	}, nil
}

// delegatedEdgeTransport is the RoundTripper behind OrgProviderRoute. It is
// bound to one edge hop and one delegated token.
type delegatedEdgeTransport struct {
	base     http.RoundTripper
	scheme   string
	host     string
	basePath string
	token    string
	user     string
}

// RoundTrip implements http.RoundTripper. It does not modify req.
func (t *delegatedEdgeTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if !t.addressed(req.URL) {
		if req.Body != nil {
			_ = req.Body.Close()
		}
		return nil, fmt.Errorf("delegated edge transport refuses a request not addressed to its provider route")
	}
	out := req.Clone(req.Context())
	// Same Host the backend proxy sends on this hop (serveOverEdge).
	out.Host = t.host
	// The provider at the far end attributes the call to the person; the
	// credential that proves it is the delegated token, never the bearer the
	// hub received.
	out.Header.Del("X-Faros-User")
	if t.user != "" {
		out.Header.Set("X-Faros-User", t.user)
	}
	setDelegatedAuthorization(out.Header, t.token)
	return t.base.RoundTrip(out)
}

// addressed reports whether u targets this transport's edge hop: the edges
// provider's backend, under the edge-proxy path of this provider's Service,
// with no dot-segments that could climb out of it.
func (t *delegatedEdgeTransport) addressed(u *url.URL) bool {
	if u == nil || u.Scheme != t.scheme || u.Host != t.host || u.User != nil {
		return false
	}
	if u.Path != t.basePath && !strings.HasPrefix(u.Path, strings.TrimSuffix(t.basePath, "/")+"/") {
		return false
	}
	for _, seg := range strings.Split(u.Path, "/") {
		if seg == ".." || seg == "." {
			return false
		}
	}
	return true
}
