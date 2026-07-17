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

package restapi

import (
	"net/http"
	"strings"

	"github.com/gorilla/mux"

	"github.com/faroshq/faros-kedge/pkg/apiurl"
	"github.com/faroshq/faros-kedge/pkg/hub/kcp"
)

// mcpClientServerName is the friendly name connect snippets give the server
// entry in the user's MCP client config.
const mcpClientServerName = "kedge"

// mcpServerBody is the create/update payload for an MCPServer.
type mcpServerBody struct {
	Name         string `json:"name"`
	DisplayName  string `json:"displayName"`
	Instructions string `json:"instructions"`
	ReadOnly     bool   `json:"readOnly"`
}

// mcpConnectResponse carries the endpoint + long-lived token for a named server.
type mcpConnectResponse struct {
	EndpointURL string `json:"endpointURL"`
	ServerName  string `json:"serverName"`
	Token       string `json:"token"`
	TokenReady  bool   `json:"tokenReady"`
}

// clusterForWorkspace resolves the tenant workspace's kcp cluster name and
// writes an error response on failure.
func (h *Handler) clusterForWorkspace(w http.ResponseWriter, r *http.Request) (string, bool) {
	if _, ok := h.requireTenantContext(w, r, true, false); !ok {
		return "", false
	}
	clusterName, err := h.mgr.bootstrapper.GetChildWorkspaceClusterName(
		r.Context(), mux.Vars(r)["org"], mux.Vars(r)["ws"])
	if err != nil {
		writeError(w, err)
		return "", false
	}
	return clusterName, true
}

// listMCPServers: GET /{org}/workspaces/{ws}/mcpservers
func (h *Handler) listMCPServers(w http.ResponseWriter, r *http.Request) {
	cluster, ok := h.clusterForWorkspace(w, r)
	if !ok {
		return
	}
	servers, err := h.mgr.bootstrapper.ListMCPServers(r.Context(), cluster)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": servers})
}

// createMCPServer: POST /{org}/workspaces/{ws}/mcpservers
func (h *Handler) createMCPServer(w http.ResponseWriter, r *http.Request) {
	cluster, ok := h.clusterForWorkspace(w, r)
	if !ok {
		return
	}
	var body mcpServerBody
	if !decodeJSON(w, r, &body) {
		return
	}
	body.Name = strings.TrimSpace(body.Name)
	if body.Name == "" {
		writeStatus(w, http.StatusBadRequest, "BadRequest", "name is required")
		return
	}
	if err := h.mgr.bootstrapper.CreateMCPServer(r.Context(), cluster, body.Name, body.DisplayName, body.Instructions, body.ReadOnly); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, kcp.MCPServerInfo{
		Name: body.Name, DisplayName: body.DisplayName, Instructions: body.Instructions,
		ReadOnly: body.ReadOnly, Phase: "Provisioning",
	})
}

// updateMCPServer: PATCH /{org}/workspaces/{ws}/mcpservers/{name}
func (h *Handler) updateMCPServer(w http.ResponseWriter, r *http.Request) {
	cluster, ok := h.clusterForWorkspace(w, r)
	if !ok {
		return
	}
	var body mcpServerBody
	if !decodeJSON(w, r, &body) {
		return
	}
	name := mux.Vars(r)["name"]
	if err := h.mgr.bootstrapper.UpdateMCPServer(r.Context(), cluster, name, body.DisplayName, body.Instructions, body.ReadOnly); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// deleteMCPServer: DELETE /{org}/workspaces/{ws}/mcpservers/{name}
func (h *Handler) deleteMCPServer(w http.ResponseWriter, r *http.Request) {
	cluster, ok := h.clusterForWorkspace(w, r)
	if !ok {
		return
	}
	if err := h.mgr.bootstrapper.DeleteMCPServer(r.Context(), cluster, mux.Vars(r)["name"]); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// connectMCPServer: GET /{org}/workspaces/{ws}/mcpservers/{name}/connect
func (h *Handler) connectMCPServer(w http.ResponseWriter, r *http.Request) {
	if h.mgr.kubeconfig.HubExternalURL == "" {
		writeStatus(w, http.StatusServiceUnavailable, "ServiceUnavailable", "MCP access is not configured on this hub")
		return
	}
	cluster, ok := h.clusterForWorkspace(w, r)
	if !ok {
		return
	}
	name := mux.Vars(r)["name"]
	token, err := h.mgr.bootstrapper.GetMCPServerToken(r.Context(), cluster, name)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, mcpConnectResponse{
		EndpointURL: apiurl.MCPServerURL(h.mgr.kubeconfig.HubExternalURL, cluster, name),
		ServerName:  mcpClientServerName,
		Token:       token,
		TokenReady:  token != "",
	})
}
