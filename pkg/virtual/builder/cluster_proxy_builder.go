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
)

// buildClusterProxyHandler creates the HTTP handler for workspace routing.
// It routes requests to appropriate workspaces based on the path.
func (p *virtualWorkspaces) buildClusterProxyHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// TODO: Route requests to appropriate workspace
		// Parse workspace from path, forward to kcp API server
		p.logger.Info("Cluster proxy request", "path", r.URL.Path)
		http.Error(w, "cluster proxy not yet implemented", http.StatusNotImplemented)
	})
}
