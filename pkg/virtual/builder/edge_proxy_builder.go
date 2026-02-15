package builder

import (
	"net/http"
	"net/url"

	utilhttp "github.com/faroshq/faros-kedge/pkg/util/http"
	"github.com/faroshq/faros-kedge/pkg/util/revdial"
	"github.com/function61/holepunch-server/pkg/wsconnadapter"
	"github.com/gorilla/websocket"
)

// buildEdgeProxyHandler creates the HTTP handler for agent tunnel registration.
// Agents connect via WebSocket, the connection is hijacked, and a revdial.Dialer
// is stored in the connection manager.
func (p *virtualWorkspaces) buildEdgeProxyHandler() http.Handler {
	upgrader := websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool {
			return utilhttp.CheckSameOrAllowedOrigin(r, []url.URL{})
		},
	}

	mux := http.NewServeMux()

	// Handle revdial pickup connections
	mux.Handle("/proxy", revdial.ConnHandler(upgrader))

	// Handle initial agent connection
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// Extract cluster and site name from path
		clusterName := r.URL.Query().Get("cluster")
		siteName := r.URL.Query().Get("site")

		if clusterName == "" || siteName == "" {
			http.Error(w, "cluster and site query parameters required", http.StatusBadRequest)
			return
		}

		key := p.getKey(clusterName, siteName)
		p.logger.Info("Agent connecting", "key", key)

		wsConn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			p.logger.Error(err, "failed to upgrade WebSocket connection")
			return
		}
		conn := wsconnadapter.New(wsConn)
		p.connManager.Set(key, conn)
		p.logger.Info("Agent tunnel established", "key", key)
	})

	return mux
}

// getKey creates a unique key for a site connection.
func (p *virtualWorkspaces) getKey(clusterName, siteName string) string {
	return clusterName + "/" + siteName
}
