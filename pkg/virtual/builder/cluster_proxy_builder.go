package builder

import (
	"net/http"
)

// buildClusterProxyHandler creates the HTTP handler for workspace routing.
// It routes requests to appropriate workspaces based on the path.
func (p *virtualWorkspaces) buildClusterProxyHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// TODO: Route requests to appropriate workspace
		// Parse workspace from path, forward to KCP API server
		p.logger.Info("Cluster proxy request", "path", r.URL.Path)
		http.Error(w, "cluster proxy not yet implemented", http.StatusNotImplemented)
	})
}
