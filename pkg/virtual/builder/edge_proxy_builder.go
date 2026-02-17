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
	"net/url"

	"github.com/function61/holepunch-server/pkg/wsconnadapter"
	"github.com/gorilla/websocket"

	utilhttp "github.com/faroshq/faros-kedge/pkg/util/http"
	"github.com/faroshq/faros-kedge/pkg/util/revdial"
)

// buildEdgeProxyHandler creates the HTTP handler for agent tunnel registration.
// Agents connect via WebSocket, the connection is hijacked, and a revdial.Dialer
// is stored in the connection manager.
//
// Authentication follows the faros-core delegated authorizer pattern:
// TokenReview (authn) + SubjectAccessReview (authz) against kcp using admin
// credentials. The agent's SA token must be authenticated and authorized to
// "get" sites in its workspace.
func (p *virtualWorkspaces) buildEdgeProxyHandler() http.Handler {
	upgrader := websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool {
			return utilhttp.CheckSameOrAllowedOrigin(r, []url.URL{})
		},
	}

	mux := http.NewServeMux()

	// Handle revdial pickup connections.
	// These are authenticated by the random 128-bit dialer unique ID.
	mux.Handle("/proxy", revdial.ConnHandler(upgrader))

	// Handle initial agent connection.
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// 1. Extract bearer token.
		token := extractBearerToken(r)
		claims, ok := parseServiceAccountToken(token)
		if !ok {
			p.logger.Info("Rejected tunnel connection: invalid or missing SA token")
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		// 2. Extract cluster and site name from query parameters.
		clusterName := r.URL.Query().Get("cluster")
		siteName := r.URL.Query().Get("site")

		if clusterName == "" || siteName == "" {
			http.Error(w, "cluster and site query parameters required", http.StatusBadRequest)
			return
		}

		// 3. Delegated authorization: TokenReview + SubjectAccessReview via kcp.
		// Checks that the SA token can "get" the site (same verb as faros-core edge proxy).
		if p.kcpConfig != nil {
			if err := authorize(r.Context(), p.kcpConfig, token, claims.ClusterName, "get", "sites", siteName); err != nil {
				p.logger.Error(err, "edge proxy authorization failed", "tokenCluster", claims.ClusterName, "site", siteName)
				http.Error(w, "Forbidden", http.StatusForbidden)
				return
			}
		}

		// 4. Upgrade to WebSocket and register the tunnel.
		key := p.getKey(clusterName, siteName)
		p.logger.Info("Agent connecting", "key", key, "tokenCluster", claims.ClusterName)

		wsConn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			p.logger.Error(err, "failed to upgrade WebSocket connection")
			return
		}
		conn := wsconnadapter.New(wsConn)
		p.connManager.Set(key, conn)
		p.logger.Info("Agent tunnel established", "key", key, "tokenCluster", claims.ClusterName)
	})

	return mux
}

// getKey creates a unique key for a site connection.
func (p *virtualWorkspaces) getKey(clusterName, siteName string) string {
	return clusterName + "/" + siteName
}
