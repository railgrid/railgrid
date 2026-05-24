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
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"

	"github.com/go-logr/logr"

	"github.com/faroshq/faros-kedge/pkg/apiurl"
)

// NewUIProxy returns an http.Handler serving /ui/providers/{name}/* by reverse
// proxying to the provider's spec.ui.url. The handler is mounted in the hub
// router WITHOUT http.StripPrefix; this proxy strips the /ui/providers/{name}
// segment itself so it can inject X-Kedge-Base-Path before forwarding.
//
// Unknown name → 404. Provider not Ready → 503. Provider without a UI → 404.
func NewUIProxy(reg *Registry, log logr.Logger) http.Handler {
	return &providerProxy{
		reg:        reg,
		log:        log.WithName("ui-proxy"),
		pathPrefix: apiurl.PathPrefixProvidersUI,
		pick:       func(p Provider) *url.URL { return p.UIURL },
		setHeaders: func(req *http.Request, name, base string) {
			req.Header.Set("X-Kedge-Base-Path", base)
		},
	}
}

// NewBackendProxy returns an http.Handler serving /services/providers/{name}/*
// by reverse proxying to the provider's spec.backend.url. The user's
// Authorization header (already validated by upstream middleware on this
// route) is forwarded; we inject X-Kedge-User / X-Kedge-Tenant for the
// provider's convenience (best-effort, may be empty during Phase 1A).
func NewBackendProxy(reg *Registry, log logr.Logger) http.Handler {
	return &providerProxy{
		reg:        reg,
		log:        log.WithName("backend-proxy"),
		pathPrefix: apiurl.PathPrefixProvidersProxy,
		pick:       func(p Provider) *url.URL { return p.BackendURL },
		setHeaders: func(req *http.Request, _, _ string) {
			// Auth header is preserved by ReverseProxy's default Director
			// composition. Phase 1A: nothing else to inject here.
		},
	}
}

// providerProxy is the shared implementation backing both proxies.
type providerProxy struct {
	reg        *Registry
	log        logr.Logger
	pathPrefix string // "/ui/providers" or "/services/providers"
	pick       func(Provider) *url.URL
	setHeaders func(req *http.Request, name, base string)
}

func (p *providerProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	name, rest, ok := splitProviderPath(r.URL.Path, p.pathPrefix)
	if !ok {
		http.NotFound(w, r)
		return
	}

	prov, found := p.reg.Get(name)
	if !found {
		http.Error(w, "provider not found: "+name, http.StatusNotFound)
		return
	}
	target := p.pick(prov)
	if target == nil {
		http.Error(w, "provider has no endpoint for this route: "+name, http.StatusNotFound)
		return
	}
	if !prov.Ready() {
		http.Error(w, "provider not ready: "+name, http.StatusServiceUnavailable)
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
