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
	"fmt"
	"net/http"
	"strings"

	mcpconfig "github.com/containers/kubernetes-mcp-server/pkg/config"
	mcpserver "github.com/containers/kubernetes-mcp-server/pkg/mcp"

	// Register MCP toolsets via side-effect imports (init() functions populate the toolset registry).
	_ "github.com/containers/kubernetes-mcp-server/pkg/toolsets/config"
	_ "github.com/containers/kubernetes-mcp-server/pkg/toolsets/core"
	_ "github.com/containers/kubernetes-mcp-server/pkg/toolsets/helm"
	_ "github.com/containers/kubernetes-mcp-server/pkg/toolsets/kcp"
	_ "github.com/containers/kubernetes-mcp-server/pkg/toolsets/kiali"
	_ "github.com/containers/kubernetes-mcp-server/pkg/toolsets/kubevirt"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"

	"github.com/faroshq/faros-kedge/pkg/apiurl"
)

// buildMCPHandler creates the HTTP handler for the per-edge MCP endpoint.
//
// URL pattern (mounted via agent-proxy route):
//
//	/services/agent-proxy/{cluster}/apis/kedge.faros.sh/v1alpha1/edges/{edgeName}/mcp
//
// cluster and edgeName are extracted from the path by the agent-proxy mux before
// this handler is called; they are passed as explicit arguments rather than
// parsed from the request URL.
//
// On each request the handler:
//  1. Extracts the caller's bearer token from the Authorization header.
//  2. Builds a single-edge KedgeEdgeProvider for (cluster, edgeName).
//  3. Spins up a fresh stateless MCP server.
//  4. Serves via the streamable-HTTP transport (ServeHTTP).
func (p *virtualWorkspaces) buildMCPHandler(cluster, edgeName string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		logger := klog.FromContext(r.Context()).WithName("mcp-handler")

		// 1. Extract bearer token.
		token := extractBearerToken(r)
		if token == "" {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		// 2. Build per-request single-edge MCP provider.
		//    edgeProxyBase uses the internal URL to avoid CDN/proxy loops
		//    (e.g. Cloudflare loop detection when the MCP handler calls back
		//    to the edges-proxy on the same hub).
		baseURL := p.hubInternalURL
		if baseURL == "" {
			baseURL = p.hubExternalURL
		}
		provider := &KedgeEdgeProvider{
			cluster:         cluster,
			edgeName:        edgeName,
			edgeConnManager: p.edgeConnManager,
			hubBase:         strings.TrimRight(baseURL, "/"),
			bearerToken:     token,
		}

		// 3. Create a stateless MCP server for this request.
		// Use the default toolset configuration (core, config, helm) so that
		// tools/list returns the expected tools. Override Stateless=true for
		// load-balanced / serverless deployments.  ServerInstructions seeds
		// the LLM with kedge-specific context the moment it connects.
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
		srv.ServeHTTP().ServeHTTP(w, r)
	})
}

// unstructuredNestedMap extracts a nested map from an unstructured object.
// Kept here because api.go's UnstructuredNestedMap exports a public wrapper.
func unstructuredNestedMap(obj map[string]any, key string) (map[string]any, bool, error) {
	v, ok := obj[key]
	if !ok {
		return nil, false, nil
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil, false, fmt.Errorf("expected map for key %q, got %T", key, v)
	}
	return m, true, nil
}

// clusterScopedDynamicClient creates a dynamic client scoped to a kcp
// cluster. api.go's ClusterScopedDynamicClient exports a public wrapper.
func clusterScopedDynamicClient(kcpConfig *rest.Config, cluster string) (dynamic.Interface, error) {
	if kcpConfig == nil {
		return nil, fmt.Errorf("kcpConfig is nil")
	}
	clusterConfig := *kcpConfig
	clusterConfig.Host = apiurl.KCPClusterURL(kcpConfig.Host, cluster)
	return dynamic.NewForConfig(&clusterConfig)
}
