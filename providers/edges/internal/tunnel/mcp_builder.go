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

package tunnel

import (
	"fmt"
	"net/http"
	"strings"

	mcpconfig "github.com/containers/kubernetes-mcp-server/pkg/config"
	mcpserver "github.com/containers/kubernetes-mcp-server/pkg/mcp"

	// Register MCP toolsets via side-effect imports (init() functions populate
	// the toolset registry).
	_ "github.com/containers/kubernetes-mcp-server/pkg/toolsets/config"
	_ "github.com/containers/kubernetes-mcp-server/pkg/toolsets/core"
	_ "github.com/containers/kubernetes-mcp-server/pkg/toolsets/helm"
	_ "github.com/containers/kubernetes-mcp-server/pkg/toolsets/kcp"
	_ "github.com/containers/kubernetes-mcp-server/pkg/toolsets/kiali"
	_ "github.com/containers/kubernetes-mcp-server/pkg/toolsets/kubevirt"

	"k8s.io/klog/v2"
)

// buildProviderMCPHandler is the provider's AGGREGATE MCP endpoint, mounted at
// the provider root `/mcp`. The hub's MCP aggregate federates it by POSTing
// tools/list here with the caller's bearer token and X-Kedge-Cluster (the tenant
// logical-cluster ID). It exposes the kube toolset across every connected
// KubernetesCluster edge in that tenant; the MCP "cluster" tool parameter selects
// which edge a call targets.
//
// This is the counterpart to the per-edge handler: per-edge is a single fixed
// KubernetesCluster; this one is the whole fleet, and is what appears in the
// tenant's aggregate `kedge` MCP endpoint.
func (p *Server) buildProviderMCPHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		logger := klog.FromContext(r.Context()).WithName("provider-mcp-handler")

		token := extractBearerToken(r)
		if token == "" {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		// Tenant is carried on X-Kedge-Cluster (injected by the hub backend proxy,
		// forwarded by the aggregate's federation client). Without it there is no
		// tenant to scope edges to — serve an empty-but-valid MCP server.
		cluster := r.Header.Get("X-Kedge-Cluster")

		// Kube MCP applies to KubernetesCluster edges. Enumerate the connected ones
		// for this tenant from the tunnel registry (keys: "{resource}/{cluster}/{name}").
		const resource = "kubernetesclusters"
		var edgeNames []string
		if cluster != "" {
			prefix := resource + "/" + cluster + "/"
			for _, k := range p.edgeConnManager.Keys() {
				if strings.HasPrefix(k, prefix) {
					edgeNames = append(edgeNames, strings.TrimPrefix(k, prefix))
				}
			}
		}

		baseURL := p.hubInternalURL
		if baseURL == "" {
			baseURL = p.hubExternalURL
		}
		provider := &multiEdgeProvider{
			cluster:             cluster,
			resource:            resource,
			group:               p.group,
			version:             p.version,
			edgeNames:           edgeNames,
			edgeConnManager:     p.edgeConnManager,
			hubBase:             strings.TrimRight(baseURL, "/"),
			edgeProxyPublicPath: p.edgeProxyPublicPath,
			bearerToken:         token,
		}

		staticCfg := mcpconfig.Default()
		staticCfg.Stateless = true
		staticCfg.ServerInstructions = fmt.Sprintf(
			"You are connected to the kedge edges provider MCP endpoint for tenant workspace %q. "+
				"Kube tools route to a connected KubernetesCluster edge selected by the \"cluster\" parameter; "+
				"call the targets/list tool to see which edges are reachable. %d edge(s) connected.",
			cluster, len(edgeNames),
		)
		srv, err := mcpserver.NewServer(mcpserver.Configuration{StaticConfig: staticCfg}, provider)
		if err != nil {
			logger.Error(err, "failed to create provider MCP server", "cluster", cluster)
			http.Error(w, fmt.Sprintf("failed to initialize MCP server: %v", err), http.StatusInternalServerError)
			return
		}
		defer srv.Close()

		// Normalize Host to loopback to satisfy the MCP SDK's DNS-rebinding guard
		// (see buildMCPHandler for the rationale).
		r.Host = "localhost"
		ensureUserAgent(r)
		srv.ServeHTTP().ServeHTTP(w, r)
	})
}

// ensureUserAgent guarantees a non-empty User-Agent header on an MCP request.
//
// kubernetes-mcp-server's userAgentPropagationMiddleware falls back to the MCP
// session's clientInfo when the header is absent, and dereferences it without a
// nil check. In stateless mode the go-sdk synthesizes InitializeParams with a
// nil ClientInfo (there is no initialize handshake to fill it), so that fallback
// panics on every request. Our handlers are always stateless, and the hub's
// reverse proxy blanks User-Agent when the original request carries none, so the
// panic path is reachable in normal traffic. Keeping the header populated means
// the middleware returns before it can dereference clientInfo.
func ensureUserAgent(r *http.Request) {
	if r.Header.Get("User-Agent") == "" {
		r.Header.Set("User-Agent", "kedge-edges-provider")
	}
}

// buildMCPHandler creates the HTTP handler for the per-edge MCP endpoint.
//
// URL pattern (behind the hub backend proxy, on the provider's agent-ingress
// mount, which dispatches /mcp before agent auth):
//
//	/services/providers/edges/agent/{cluster}/apis/edges.kedge.faros.sh/v1alpha1/{resource}/{name}/mcp
//
// cluster, resource and edgeName are parsed from the path by the caller and
// passed as explicit arguments. Kube MCP tools only apply to KubernetesCluster
// edges (they route through the edge's k8s subresource); server edges have no
// Kubernetes API, so they are rejected.
//
// On each request the handler:
//  1. Extracts the caller's bearer token from the Authorization header.
//  2. Builds a single-edge kedgeEdgeProvider for (cluster, resource, edgeName).
//  3. Spins up a fresh stateless MCP server.
//  4. Serves via the streamable-HTTP transport (ServeHTTP).
func (p *Server) buildMCPHandler(cluster, resource, edgeName string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		logger := klog.FromContext(r.Context()).WithName("mcp-handler")

		// Kube MCP tools require a Kubernetes API. Server edges (linuxservers)
		// don't have one — reject rather than build a k8s client that 404s on
		// every tool call.
		if resource != "kubernetesclusters" {
			http.Error(w, "per-edge MCP is only available for KubernetesCluster edges", http.StatusBadRequest)
			return
		}

		// 1. Extract bearer token.
		token := extractBearerToken(r)
		if token == "" {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		// 2. Build per-request single-edge MCP provider. hubInternalURL is
		//    preferred over the external URL to avoid CDN/proxy loops when the
		//    MCP kube client calls back through the hub to this provider's
		//    edgeproxy k8s subresource.
		baseURL := p.hubInternalURL
		if baseURL == "" {
			baseURL = p.hubExternalURL
		}
		provider := &kedgeEdgeProvider{
			cluster:             cluster,
			resource:            resource,
			group:               p.group,
			version:             p.version,
			edgeName:            edgeName,
			edgeConnManager:     p.edgeConnManager,
			hubBase:             strings.TrimRight(baseURL, "/"),
			edgeProxyPublicPath: p.edgeProxyPublicPath,
			bearerToken:         token,
		}

		// 3. Create a stateless MCP server for this request. Use the default
		//    toolset configuration (core, config, helm) so tools/list returns the
		//    expected tools. ServerInstructions seeds the LLM with kedge-specific
		//    context the moment it connects.
		staticCfg := mcpconfig.Default()
		staticCfg.Stateless = true
		staticCfg.ServerInstructions = fmt.Sprintf(
			"You are connected to a kedge per-edge Kubernetes MCP endpoint for edge %q in tenant workspace %q. "+
				"All kube tools route to this single edge — there is no \"cluster\" selection to make here. "+
				"For multi-cluster operation, point your MCP client at the kedge aggregate endpoint instead.",
			edgeName, cluster,
		)
		srv, err := mcpserver.NewServer(mcpserver.Configuration{StaticConfig: staticCfg}, provider)
		if err != nil {
			logger.Error(err, "failed to create MCP server", "cluster", cluster, "edge", edgeName)
			http.Error(w, fmt.Sprintf("failed to initialize MCP server: %v", err), http.StatusInternalServerError)
			return
		}
		defer srv.Close()

		// 4. Serve via streamable HTTP transport.
		//
		// The MCP SDK's streamable handler has DNS-rebinding protection that 403s
		// ("invalid Host header") when the server's local address is loopback but
		// the request Host isn't. Behind the hub backend proxy the provider sees
		// Host "host.docker.internal:8084" (or the pod address) on a loopback
		// connection, which trips it. That guard is meant for browser-facing
		// localhost servers; this endpoint is reached only through the hub's
		// authenticated proxy, so normalize Host to loopback to satisfy the check.
		r.Host = "localhost"
		ensureUserAgent(r)
		srv.ServeHTTP().ServeHTTP(w, r)
	})
}
