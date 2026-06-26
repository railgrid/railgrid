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
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"mime"
	"net/http"
	"net/http/httputil"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/go-logr/logr"

	"github.com/faroshq/faros-kedge/pkg/apiurl"
)

// NewUIProxy returns an http.Handler serving /ui/providers/{name}/* by reverse
// proxying to the provider's spec.ui.url. The handler is mounted in the hub
// router WITHOUT http.StripPrefix; this proxy strips the /ui/providers/{name}
// segment itself so it can inject X-Kedge-Base-Path before forwarding.
//
// Routing nuance: the portal SPA also lives at /ui/, with Vue Router serving
// /providers/{name} (and arbitrary sub-paths) as in-app routes that mount
// ProviderFrame. The proxy therefore only handles requests whose last path
// segment looks like a file (contains a "."): main.js, icon.svg, etc. Bare
// names like /ui/providers/quickstart or /ui/providers/quickstart/some-page
// are SPA routes — the proxy calls SetFallback() to dispatch them back to
// the portal SPA so a hard refresh preserves portal chrome.
//
// Unknown name → 404. Provider not Ready → 503. Provider without a UI → 404.
func NewUIProxy(reg *Registry, log logr.Logger) *ProviderProxy {
	return &ProviderProxy{
		reg:        reg,
		log:        log.WithName("ui-proxy"),
		pathPrefix: apiurl.PathPrefixProvidersUI,
		pick:       func(p Provider) *url.URL { return p.UIURL },
		setHeaders: func(req *http.Request, name, base string) {
			req.Header.Set("X-Kedge-Base-Path", base)
		},
		// UI proxy reserves only asset-shaped paths; portal SPA routes fall
		// through (see SetFallback). Backend proxy keeps the default "always
		// proxy" behaviour by leaving fallbackForSPA false.
		fallbackForSPA: true,
	}
}

// TenantResolver resolves the caller's identity (User CR name) and
// tenant workspace path (e.g. root:kedge:orgs:{orgUUID}) from an HTTP
// request's bearer token. Implementations typically wrap
// proxy.KCPProxy.IdentifyUser plus a User → Organization → WorkspacePath
// lookup; see pkg/hub/server.go for the canonical wiring. Returns an
// error when the caller can't be resolved — the proxy treats that as
// "no headers to inject" (best effort), it does NOT 401, so anonymous
// reads of provider /healthz keep working.
type TenantResolver interface {
	Resolve(r *http.Request) (user, tenantPath string, err error)
}

// TenantResolverFunc adapts a plain function to the TenantResolver
// interface. Lets callers compose the lookup inline without declaring a
// type.
type TenantResolverFunc func(r *http.Request) (string, string, error)

// Resolve satisfies TenantResolver.
func (f TenantResolverFunc) Resolve(r *http.Request) (string, string, error) {
	return f(r)
}

// NewBackendProxy returns an http.Handler serving /services/providers/{name}/*
// by reverse proxying to the provider's spec.backend.url. The user's
// Authorization header is forwarded as-is. If a TenantResolver is
// installed via SetTenantResolver, the proxy resolves the caller's
// identity and injects X-Kedge-User + X-Kedge-Tenant so the provider can
// scope work without re-parsing the bearer token. Incoming
// X-Kedge-User / X-Kedge-Tenant headers are ALWAYS stripped before the
// request is forwarded — a third-party caller can't forge identity by
// setting those headers directly.
func NewBackendProxy(reg *Registry, log logr.Logger) *ProviderProxy {
	p := &ProviderProxy{
		reg:        reg,
		log:        log.WithName("backend-proxy"),
		pathPrefix: apiurl.PathPrefixProvidersProxy,
		pick:       func(p Provider) *url.URL { return p.BackendURL },
	}
	// setHeaders runs after the Director's URL rewrite. Always strip
	// inbound X-Kedge-* identity headers (defense in depth — a client
	// must not be able to spoof identity by setting them at the front
	// door); if a TenantResolver is installed, populate them from the
	// resolver. Reading p.tenantResolver via the closure is safe
	// because SetTenantResolver writes it before the first proxied
	// request lands in practice; if the wiring ever needs hot-swap,
	// switch the field to atomic.Pointer[TenantResolver].
	p.setHeaders = func(req *http.Request, name, _ string) {
		req.Header.Del("X-Kedge-User")
		req.Header.Del("X-Kedge-Tenant")
		req.Header.Del("X-Kedge-Cluster")
		if p.tenantResolver == nil {
			// V(2) so tests / non-bootstrapper hubs don't spam, but
			// devs can flip on verbosity to see the dropped path.
			p.log.V(2).Info("no tenant resolver wired; forwarding without X-Kedge-* headers", "provider", name)
			return
		}
		user, tenantPath, err := p.tenantResolver.Resolve(req)
		if err != nil {
			// Anonymous (no bearer) is common on /healthz probes
			// and isn't worth screaming about — keep at V(2). Real
			// resolve failures (auth verify error, kcp lookup
			// failure) come through at default verbosity so a
			// TenantMissing error in a provider has a corresponding
			// hub log line a dev can grep for.
			if err.Error() == "anonymous caller" {
				p.log.V(2).Info("anonymous caller — forwarding without X-Kedge-* headers", "provider", name, "path", req.URL.Path)
			} else {
				p.log.Info("tenant resolve failed — forwarding without X-Kedge-* headers", "provider", name, "path", req.URL.Path, "err", err.Error())
			}
			// Still inject user when the resolver returned a name
			// but errored later in the chain. Lets the provider at
			// least attribute the call even when tenant scoping
			// isn't available.
			if user != "" {
				req.Header.Set("X-Kedge-User", user)
			}
			return
		}
		if user != "" {
			req.Header.Set("X-Kedge-User", user)
		}
		if tenantPath != "" {
			req.Header.Set("X-Kedge-Tenant", tenantPath)
		} else {
			p.log.Info("tenant resolved but tenantPath empty — forwarding without X-Kedge-Tenant", "provider", name, "user", user, "hint", "user may not have a personal Organization workspace bootstrapped yet")
		}
		// Resolve the workspace path to its kcp logical-cluster ID and inject
		// X-Kedge-Cluster. Best-effort: a resolve failure drops only this
		// header (the provider still has X-Kedge-Tenant), so a provider that
		// doesn't need the ID is unaffected.
		if tenantPath != "" && p.clusterResolver != nil {
			if clusterID, err := p.clusterResolver(req.Context(), tenantPath); err != nil {
				p.log.Info("cluster-id resolve failed — forwarding without X-Kedge-Cluster", "provider", name, "tenant", tenantPath, "err", err.Error())
			} else if clusterID != "" {
				req.Header.Set("X-Kedge-Cluster", clusterID)
			}
		}
	}
	return p
}

// SetTenantResolver installs the resolver used to populate
// X-Kedge-User and X-Kedge-Tenant on proxied requests. Wire after the
// kcpProxy and kedgeClient are built (see pkg/hub/server.go around the
// providerRegistry setup). Calling with nil disables injection but the
// inbound-header stripping below still runs.
func (p *ProviderProxy) SetTenantResolver(r TenantResolver) {
	p.tenantResolver = r
}

// SetClusterResolver installs an optional resolver mapping a tenant workspace
// path to its kcp logical-cluster ID, injected as X-Kedge-Cluster on
// backend-proxied requests. Wire alongside SetTenantResolver; without it the
// header is simply omitted (and any inbound value is still stripped).
func (p *ProviderProxy) SetClusterResolver(f func(ctx context.Context, tenantPath string) (string, error)) {
	p.clusterResolver = f
}

// ProviderProxy is the shared implementation backing both proxies. Exported
// so the server can call SetFallback on the UI proxy after the portal SPA
// handler is built (the two are constructed at different points in Server.Run).
type ProviderProxy struct {
	reg        *Registry
	log        logr.Logger
	pathPrefix string // "/ui/providers" or "/services/providers"
	pick       func(Provider) *url.URL
	setHeaders func(req *http.Request, name, base string)

	// fallbackForSPA, when true, makes ServeHTTP route requests whose path
	// doesn't look like a static asset (no "." in the last segment) to the
	// fallback handler instead of proxying them. Set by NewUIProxy so that
	// /ui/providers/{name} and /ui/providers/{name}/some-route reach the
	// Vue SPA on hard refresh.
	fallbackForSPA bool
	// fallback is invoked for portal-SPA-shaped paths when fallbackForSPA
	// is true. Nil until SetFallback is called; while nil, those paths 404.
	fallback http.Handler

	// tenantResolver, when set, populates X-Kedge-User / X-Kedge-Tenant
	// on backend-proxied requests. Used only by the backend proxy; the
	// UI proxy serves static assets and has no use for caller identity.
	// See SetTenantResolver.
	tenantResolver TenantResolver

	// clusterResolver, when set, maps the resolved tenant workspace path to
	// its kcp logical-cluster ID, injected as X-Kedge-Cluster. Providers need
	// the ID (not the path) to address per-workspace surfaces that key on it —
	// notably the hub's GraphQL gateway at /graphql/clusters/{id}, whose
	// per-cluster schema lookup only matches a cluster ID. See
	// SetClusterResolver.
	clusterResolver func(ctx context.Context, tenantPath string) (string, error)
}

// SetFallback installs the portal SPA handler invoked for non-asset paths
// under /ui/providers/{name}. See NewUIProxy for the rationale.
func (p *ProviderProxy) SetFallback(h http.Handler) {
	p.fallback = h
}

func (p *ProviderProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	name, rest, ok := splitProviderPath(r.URL.Path, p.pathPrefix)
	if !ok {
		// In UI-proxy mode, /ui/providers/ (trailing slash, no provider
		// name) is a portal SPA route — the catalog page — not an error.
		// Fall through to the SPA fallback when one is wired up. Backend
		// proxy mode keeps the strict 404 since /services/providers/ has
		// no SPA meaning.
		if p.fallbackForSPA && p.fallback != nil {
			p.fallback.ServeHTTP(w, r)
			return
		}
		http.NotFound(w, r)
		return
	}

	// UI-proxy mode: portal SPA owns any path that doesn't look like a
	// static asset (no "." in the last segment). Without this fallback, a
	// browser refresh of /ui/providers/quickstart/anything would serve the
	// provider's raw HTML and the portal chrome would be lost.
	if p.fallbackForSPA && !isAssetPath(rest) {
		if p.fallback != nil {
			p.fallback.ServeHTTP(w, r)
			return
		}
		http.NotFound(w, r)
		return
	}

	prov, found := p.reg.Get(name)
	if !found {
		http.Error(w, "provider not found: "+name, http.StatusNotFound)
		return
	}
	if !prov.Ready() {
		http.Error(w, "provider not ready: "+name, http.StatusServiceUnavailable)
		return
	}

	// First-party providers ship their pre-built micro-frontend embedded
	// into the hub binary; serve those assets directly from the in-memory
	// FS rather than reverse-proxying anywhere. Only applies to the UI
	// proxy (fallbackForSPA implies p is the UI proxy); the backend proxy
	// keeps its existing UIURL/BackendURL semantics.
	if p.fallbackForSPA && prov.LocalUIAssets != nil {
		serveLocalAsset(w, r, prov.LocalUIAssets, rest, p.log)
		return
	}

	target := p.pick(prov)
	if target == nil {
		http.Error(w, "provider has no endpoint for this route: "+name, http.StatusNotFound)
		return
	}

	basePath := p.pathPrefix + "/" + name

	rp := &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			req.URL.Scheme = target.Scheme
			req.URL.Host = target.Host
			// Forward only the path AFTER /{prefix}/{name}. The target URL's
			// own path (typically empty for a Service URL) is preserved as a
			// base; we append the remaining request path.
			req.URL.Path = singleJoiningSlash(target.Path, rest)
			req.URL.RawPath = "" // let net/url re-encode from Path
			req.Host = target.Host
			p.setHeaders(req, name, basePath)
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			p.log.Error(err, "upstream error", "provider", name, "target", target.String())
			http.Error(w, "provider upstream error", http.StatusBadGateway)
		},
	}
	rp.ServeHTTP(w, r)
}

// localAssetCacheControl is what we serve on embedded provider assets.
// `Cache-Control: no-cache` (the old value) is silently ignored by
// Cloudflare for .js/.css under "Standard" caching — it falls back to
// its 4h default, which once cached a 404 across every browser session
// for hours after a fix shipped. An explicit short max-age is honored,
// and the ETag below makes the post-expiry revalidation a cheap 304.
const localAssetCacheControl = "public, max-age=60, must-revalidate"

// serveLocalAsset writes the file at rest from the provider's embedded
// LocalUIAssets FS to w. rest is the path after /ui/providers/{name} (so
// "/main.js", "/icon.svg", "/assets/foo-abc.js"). 404s when the file
// isn't present — the SPA-fallback branch upstream of this function
// already handled non-asset paths, so 404 here is the right behavior:
// the provider's bundle is missing an asset the portal asked for.
func serveLocalAsset(w http.ResponseWriter, r *http.Request, assets fs.FS, rest string, log logr.Logger) {
	name := strings.TrimPrefix(rest, "/")
	if name == "" {
		name = "index.html"
	}
	data, err := fs.ReadFile(assets, name)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			log.Error(err, "open local asset", "name", name)
		}
		http.NotFound(w, nil)
		return
	}

	ct := mime.TypeByExtension(path.Ext(name))
	if ct == "" {
		ct = "application/octet-stream"
	}
	sum := sha256.Sum256(data)
	etag := `"` + hex.EncodeToString(sum[:16]) + `"`

	w.Header().Set("Content-Type", ct)
	w.Header().Set("Cache-Control", localAssetCacheControl)
	w.Header().Set("ETag", etag)
	// ServeContent handles If-None-Match -> 304 and Content-Length for us.
	// Zero ModTime skips Last-Modified so ETag alone drives revalidation.
	http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(data))
}

// splitProviderPath parses "/ui/providers/foo/bar" given prefix
// "/ui/providers" into name="foo", rest="/bar". A bare "/ui/providers/foo"
// (no trailing slash) returns name="foo", rest="/".
func splitProviderPath(reqPath, prefix string) (name, rest string, ok bool) {
	if !strings.HasPrefix(reqPath, prefix+"/") {
		return "", "", false
	}
	tail := strings.TrimPrefix(reqPath, prefix+"/")
	slash := strings.IndexByte(tail, '/')
	if slash < 0 {
		if tail == "" {
			return "", "", false
		}
		return tail, "/", true
	}
	name = tail[:slash]
	if name == "" {
		return "", "", false
	}
	return name, tail[slash:], true
}

// isAssetPath reports whether the request looks like a static asset the
// provider's UI server should serve (main.js, icon.svg, foo/bar.css) as
// opposed to a portal SPA route (/, /workloads, /foo/bar). The heuristic is
// "does the last path segment contain a dot?" — file extensions are the
// only durable signal we have without coupling to a per-provider asset
// manifest. Edge case: a SPA route ending in a dotted segment (e.g.
// /providers/foo/v1.2) would be misclassified, but that is unusual and
// providers can avoid it.
func isAssetPath(rest string) bool {
	last := strings.TrimPrefix(rest, "/")
	if i := strings.LastIndexByte(last, '/'); i >= 0 {
		last = last[i+1:]
	}
	return strings.Contains(last, ".")
}

// singleJoiningSlash mirrors net/http/httputil's unexported helper.
func singleJoiningSlash(a, b string) string {
	aslash := strings.HasSuffix(a, "/")
	bslash := strings.HasPrefix(b, "/")
	switch {
	case aslash && bslash:
		return a + b[1:]
	case !aslash && !bslash:
		return a + "/" + b
	}
	return a + b
}
