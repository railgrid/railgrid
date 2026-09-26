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

package dataplane

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"path"
	"strings"
	"time"

	sdk "github.com/railgrid/provider-sdk/dataplane"
)

// Runtime is the provider's handle to the cluster where workloads run. It is
// the only component that holds a runtime-cluster credential — the whole point
// of this work is that consumers (App Studio) no longer do. Implementations
// wrap the runtime rest.Config the serve process already resolves from
// KRO_KUBECONFIG / in-cluster.
type Runtime interface {
	// Host is the runtime API server base URL (scheme://host[:port]).
	Host() string
	// Transport authenticates to the runtime API server (client cert / token +
	// CA from the runtime kubeconfig).
	Transport() (http.RoundTripper, error)
	// ControlToken reads the "token" key of the named Secret in the runtime
	// cluster. Used to mint the X-Sandbox-Control-Token header the runner's
	// control sidecar expects.
	ControlToken(ctx context.Context, namespace, name string) (string, error)
}

// controlTokenHeader is the header the sandbox runner's control sidecar checks.
// Kept identical to App Studio's today so the runner image is unchanged.
const controlTokenHeader = "X-Sandbox-Control-Token"

// serveProxy reverse-proxies the request to the resolved runtime Service via
// the runtime API server's services/proxy subresource. callerPath is the
// remaining path the caller addressed beyond the verb (only meaningful for an
// open proxy verb like "proxy"; the control verbs carry a fixed UpstreamPath
// and an empty callerPath).
//
// httputil.ReverseProxy handles both plain requests and connection upgrades
// (WebSocket / SPDY) when the upstream answers 101, so a single path covers
// preview WebSockets and log streaming alike; FlushInterval=-1 disables
// response buffering for streamed/upgraded responses.
func serveProxy(w http.ResponseWriter, r *http.Request, rt Runtime, target ResolvedTarget, callerPath string) {
	transport, err := rt.Transport()
	if err != nil {
		http.Error(w, "runtime transport unavailable: "+err.Error(), http.StatusBadGateway)
		return
	}

	base, err := url.Parse(rt.Host())
	if err != nil {
		http.Error(w, "invalid runtime host: "+err.Error(), http.StatusBadGateway)
		return
	}

	var token string
	if target.TokenSecretName != "" {
		token, err = rt.ControlToken(r.Context(), target.TokenSecretNamespace, target.TokenSecretName)
		if err != nil {
			http.Error(w, "control token unavailable: "+err.Error(), http.StatusBadGateway)
			return
		}
	}

	upstreamPath := serviceProxyPath(target, callerPath)

	// Buffer plain responses; disable buffering for streamed or upgraded ones so
	// log follow and preview WebSockets flush immediately.
	flush := time.Duration(0)
	if target.Stream || target.Upgrade {
		flush = -1
	}

	proxy := &httputil.ReverseProxy{
		Transport:     transport,
		FlushInterval: flush,
		Director: func(req *http.Request) {
			req.URL.Scheme = base.Scheme
			req.URL.Host = base.Host
			req.URL.Path = upstreamPath
			// The component parameter addressed the object, not the verb; the
			// rest of the query string is the verb's own and rides along
			// verbatim.
			req.URL.RawQuery = stripComponentQuery(req.URL.RawQuery)
			req.Host = base.Host
			// The runtime credential is supplied by Transport, and no caller
			// credential exists on a verb; nothing identity-shaped is
			// forwarded to the runtime cluster. The identity kcp stamped was
			// for this provider to read, and the hop counter is kcp's own.
			req.Header.Del("Authorization")
			stripCallerIdentity(req.Header)
			if token != "" {
				req.Header.Set(controlTokenHeader, token)
			} else {
				// Defense in depth: a caller cannot smuggle its own control token.
				req.Header.Del(controlTokenHeader)
			}
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			http.Error(w, "runtime proxy error: "+err.Error(), http.StatusBadGateway)
		},
	}
	proxy.ServeHTTP(w, r)
}

// stripCallerIdentity removes the caller-identity headers a kcp shard (and,
// for MCP, the hub proxy) stamps, so the workload behind the runtime Service
// learns nothing about who called — the provider authorized them already.
func stripCallerIdentity(h http.Header) {
	h.Del(sdk.HeaderRemoteUser)
	h.Del(sdk.HeaderRemoteGroup)
	h.Del(sdk.HeaderHops)
	h.Del(sdk.HeaderCluster)
	h.Del(sdk.HeaderTenant)
	h.Del(sdk.HeaderUser)
	for name := range h {
		if strings.HasPrefix(http.CanonicalHeaderKey(name), sdk.HeaderRemoteExtraPrefix) {
			h.Del(name)
		}
	}
}

// stripComponentQuery removes every "component" pair from a raw query string
// without re-encoding or reordering what remains: the upstream must see the
// caller's own query exactly as it was sent. The key is compared decoded, as
// the parser read it, so an encoded spelling cannot slip through.
func stripComponentQuery(raw string) string {
	if raw == "" {
		return raw
	}
	kept := make([]string, 0, 4)
	for _, pair := range strings.Split(raw, "&") {
		key := pair
		if i := strings.IndexByte(pair, '='); i >= 0 {
			key = pair[:i]
		}
		if decoded, err := url.QueryUnescape(key); err == nil && decoded == sdk.ComponentQuery {
			continue
		}
		kept = append(kept, pair)
	}
	return strings.Join(kept, "&")
}

// serviceProxyPath composes the runtime API path that reverse-proxies to the
// target Service's named port:
//
//	/api/v1/namespaces/<ns>/services/<name>:<port>/proxy<upstream><caller>
func serviceProxyPath(target ResolvedTarget, callerPath string) string {
	svc := fmt.Sprintf("%s:%s", target.ServiceName, target.ServicePort)
	base := fmt.Sprintf("/api/v1/namespaces/%s/services/%s/proxy", target.ServiceNamespace, svc)
	tail := joinPaths(target.UpstreamPath, callerPath)
	if tail == "" || tail == "/" {
		// services/proxy requires a trailing slash to hit the service root.
		return base + "/"
	}
	return base + tail
}

// joinPaths joins the configured upstream path with the caller-supplied tail,
// preserving a single leading slash and avoiding duplicate separators. Returns
// "/" when both are effectively empty.
func joinPaths(upstream, caller string) string {
	upstream = strings.TrimSpace(upstream)
	caller = strings.TrimSpace(strings.TrimPrefix(caller, "/"))
	switch {
	case upstream == "" && caller == "":
		return "/"
	case caller == "":
		return ensureLeadingSlash(upstream)
	case upstream == "" || upstream == "/":
		return "/" + caller
	default:
		joined := path.Join(upstream, caller)
		return ensureLeadingSlash(joined)
	}
}

func ensureLeadingSlash(p string) string {
	if !strings.HasPrefix(p, "/") {
		return "/" + p
	}
	return p
}
