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
	"context"
	"net/http"
	"net/url"
	"strings"

	"github.com/function61/holepunch-server/pkg/wsconnadapter"
	"github.com/gorilla/websocket"

	utilhttp "github.com/faroshq/faros-kedge/pkg/util/http"
	"github.com/faroshq/faros-kedge/pkg/util/revdial"
)

// buildEdgeAgentProxyHandler creates the HTTP handler for Edge agent tunnel
// registration (the agent-facing side of the new Edge workflow).
//
// Agents connect via WebSocket to:
//
//	/services/agent-proxy/{cluster}/apis/kedge.faros.sh/v1alpha1/edges/{name}/proxy
//
// The hub upgrades the connection, wraps it in a revdial.Dialer, and stores
// it in p.edgeConnManager keyed by "edges/{cluster}/{name}". Subsequent
// user-facing requests (buildEdgesProxyHandler) look up that dialer to open
// back-connections to the agent.
//
// A separate /proxy endpoint (relative to the mount point) handles revdial
// pick-up connections initiated by the agent side.
func (p *virtualWorkspaces) buildEdgeAgentProxyHandler() http.Handler {
	upgrader := websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool {
			return utilhttp.CheckSameOrAllowedOrigin(r, []url.URL{})
		},
	}

	mux := http.NewServeMux()

	// /proxy — revdial pick-up endpoint.
	// When the hub dials the agent (Dialer.Dial), it sends a "conn-ready"
	// message to the agent telling it to open a new WebSocket to this path.
	// The path passed to revdial.NewDialer below must match the absolute URL
	// path where this handler is mounted.
	mux.Handle("/proxy", revdial.ConnHandler(upgrader))

	// / — initial agent connection handler.
	// Path (after mount-prefix stripping):
	//   /{cluster}/apis/kedge.faros.sh/v1alpha1/edges/{name}/proxy
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// 1. Authenticate: require a valid bearer token.
		token := extractBearerToken(r)
		if token == "" {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		// 2. Parse cluster and name from the URL path.
		cluster, name, ok := parseEdgeAgentPath(r.URL.Path)
		if !ok {
			http.Error(w, "invalid path: expected /{cluster}/apis/kedge.faros.sh/v1alpha1/edges/{name}/proxy", http.StatusBadRequest)
			return
		}

		// 3. Authentication: static tokens bypass JWT SA requirement.
		//    SA tokens go through kcp delegated authorization.
		_, isStaticToken := p.staticTokens[token]
		if !isStaticToken {
			claims, ok := parseServiceAccountToken(token)
			if !ok {
				p.logger.Info("Rejected edge agent tunnel: invalid or missing SA token",
					"cluster", cluster, "name", name)
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}
			if p.kcpConfig != nil {
				if err := authorize(r.Context(), p.kcpConfig, token, claims.ClusterName, "get", "edges", name); err != nil {
					p.logger.Error(err, "edge agent proxy authorization failed",
						"cluster", cluster, "name", name)
					http.Error(w, "Forbidden", http.StatusForbidden)
					return
				}
			}
		}

		// 4. Upgrade to WebSocket.
		wsConn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			p.logger.Error(err, "failed to upgrade WebSocket connection",
				"cluster", cluster, "name", name)
			return
		}

		// 5. Register the revdial tunnel.
		// The pick-up path must match the absolute path at which the /proxy
		// endpoint is reachable (i.e. the mount point + /proxy).
		key := edgeConnKey(cluster, name)
		p.logger.Info("Edge agent connecting", "key", key)

		conn := wsconnadapter.New(wsConn)
		dialer := revdial.NewDialer(conn, "/services/agent-proxy/proxy")
		p.edgeConnManager.Store(key, dialer)
		p.logger.Info("Edge agent tunnel established", "key", key)

		// Block until the tunnel closes, then clean up the entry so stale
		// look-ups don't succeed.
		<-dialer.Done()
		p.edgeConnManager.Delete(key)
		p.logger.Info("Edge agent tunnel closed", "key", key)

		// Proactively mark the Edge as Disconnected in the hub.  Agents may die
		// without sending a clean disconnect heartbeat (e.g. SIGKILL), so the
		// hub must be the authoritative source for connectivity state.
		go p.markEdgeDisconnected(context.Background(), cluster, name)
	})

	return mux
}

// parseEdgeAgentPath extracts {cluster} and {name} from the path that the
// handler sees after the "/services/agent-proxy" prefix has been stripped.
//
// Expected format:
//
//	/{cluster}/apis/kedge.faros.sh/v1alpha1/edges/{name}/proxy
func parseEdgeAgentPath(path string) (cluster, name string, ok bool) {
	// Strip any leading slash.
	path = strings.TrimPrefix(path, "/")

	// Expected segments:
	//   [0] cluster
	//   [1] "apis"
	//   [2] "kedge.faros.sh"
	//   [3] "v1alpha1"
	//   [4] "edges"
	//   [5] name
	//   [6] "proxy"
	parts := strings.SplitN(path, "/", 8) // cap at 8 to handle extra segments gracefully
	if len(parts) < 7 {
		return "", "", false
	}
	if parts[1] != "apis" || parts[2] != "kedge.faros.sh" ||
		parts[3] != "v1alpha1" || parts[4] != "edges" || parts[6] != "proxy" {
		return "", "", false
	}
	return parts[0], parts[5], true
}

// edgeConnKey returns the ConnManager key for an Edge tunnel.
// Format: "edges/{cluster}/{name}"
func edgeConnKey(cluster, name string) string {
	return "edges/" + cluster + "/" + name
}
