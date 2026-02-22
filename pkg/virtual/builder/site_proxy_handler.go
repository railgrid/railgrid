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

package builder

import (
	"net/http"
	"net/http/httputil"
	"strings"

	"k8s.io/klog/v2"
)

// buildSiteProxyHandler creates the HTTP handler for proxying kube API requests
// to downstream site clusters through reverse-dial tunnels.
//
// Expected path format (after StripPrefix of /services/site-proxy):
//
//	/{clusterName}/{siteName}/api/v1/pods
//	/{clusterName}/{siteName}/apis/apps/v1/deployments
//
// The handler extracts clusterName and siteName, looks up the tunnel key,
// dials the agent, and proxies the remaining kube API path as /k8s/...
func (p *virtualWorkspaces) buildSiteProxyHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		logger := p.logger.WithName("site-proxy")

		// Parse path: /{clusterName}/{siteName}/...
		path := strings.TrimPrefix(r.URL.Path, "/")
		parts := strings.SplitN(path, "/", 3)
		if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
			http.Error(w, "invalid path: expected /{clusterName}/{siteName}/...", http.StatusBadRequest)
			return
		}

		clusterName := parts[0]
		siteName := parts[1]
		apiPath := "/"
		if len(parts) == 3 {
			apiPath = "/" + parts[2]
		}

		// Look up the tunnel key from the site route map.
		routeKey := clusterName + ":" + siteName
		tunnelKey, ok := p.siteRoutes.Get(routeKey)
		if !ok {
			http.Error(w, "site not found or not connected", http.StatusNotFound)
			return
		}

		logger.V(4).Info("Site proxy request", "cluster", clusterName, "site", siteName, "apiPath", apiPath)

		// Auth: kcp backend config is required for authorization.
		// Refuse to serve any request when kcpConfig is nil — there is no way
		// to validate tokens without a kcp backend (fix for #27).
		if p.kcpConfig == nil {
			logger.Error(nil, "site proxy has no kcp backend config; rejecting request")
			http.Error(w, "Service Unavailable: site proxy not configured", http.StatusServiceUnavailable)
			return
		}

		// Extract and validate the bearer token.
		token := extractBearerToken(r)
		if token == "" {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		// Trust the kcp admin bearer token (used by the mount proxy internally).
		// For all other tokens, require a valid ServiceAccount token that passes
		// kcp RBAC authorization. Unknown/opaque tokens are explicitly rejected
		// rather than silently passed through (fix for #20).
		if token != p.kcpConfig.BearerToken {
			claims, ok := parseServiceAccountToken(token)
			if !ok {
				// Token is not a recognized ServiceAccount JWT — reject.
				logger.Info("site proxy rejected unrecognized token type", "site", siteName)
				http.Error(w, "Forbidden", http.StatusForbidden)
				return
			}
			if err := authorize(r.Context(), p.kcpConfig, token, claims.ClusterName, "proxy", "sites", siteName); err != nil {
				logger.Error(err, "site proxy authorization failed", "site", siteName)
				http.Error(w, "Forbidden", http.StatusForbidden)
				return
			}
		}

		// Dial the agent through the reverse tunnel.
		deviceConn, err := p.connManager.Dial(r.Context(), tunnelKey)
		if err != nil {
			logger.Error(err, "failed to dial agent", "tunnelKey", tunnelKey)
			http.Error(w, "failed to connect to site", http.StatusBadGateway)
			return
		}

		// Rewrite path: prepend /k8s/ for the agent's local HTTP server.
		k8sPath := "/k8s" + apiPath

		// Handle upgrade requests (exec, port-forward).
		if isUpgradeRequest(r) {
			r.URL.Path = k8sPath
			p.handleK8sUpgrade(r.Context(), w, r, deviceConn)
			return
		}

		// Normal request — reverse proxy via device connection.
		transport := &deviceConnTransport{conn: deviceConn}
		proxy := &httputil.ReverseProxy{
			Director: func(req *http.Request) {
				req.URL.Scheme = "http"
				req.URL.Host = "agent"
				req.URL.Path = k8sPath
			},
			Transport: transport,
			ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
				klog.Background().Error(err, "site proxy upstream error", "tunnelKey", tunnelKey)
				http.Error(w, "proxy error", http.StatusBadGateway)
			},
		}
		proxy.ServeHTTP(w, r)
	})
}
