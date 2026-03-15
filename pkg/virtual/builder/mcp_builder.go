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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
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
		// load-balanced / serverless deployments.
		staticCfg := mcpconfig.Default()
		staticCfg.Stateless = true
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

// kubernetesGVR is the GVR for Kubernetes MCP objects in the mcp.kedge.faros.sh group.
var kubernetesGVR = schema.GroupVersionResource{
	Group:    "mcp.kedge.faros.sh",
	Version:  "v1alpha1",
	Resource: "kubernetes",
}

// edgeGVRForMCPSelector is the GVR used to list Edge objects when resolving a
// KubernetesMCP edge selector.
var edgeGVRForMCPSelector = schema.GroupVersionResource{
	Group:    "kedge.faros.sh",
	Version:  "v1alpha1",
	Resource: "edges",
}

// buildKubernetesMCPHandler creates an HTTP handler for the Kubernetes MCP endpoint.
//
// URL pattern (after stripping /services/mcp prefix):
//
//	/{cluster}/apis/mcp.kedge.faros.sh/v1alpha1/kubernetes/{name}/mcp
//
// On each request the handler:
//  1. Parses cluster and Kubernetes MCP name from the path.
//  2. Authenticates the caller via bearer token.
//  3. Fetches the Kubernetes MCP object to get the edge selector.
//  4. Lists all edges in the cluster and filters by selector + connected state.
//  5. Builds a MultiEdgeKedgeEdgeProvider and serves via MCP streamable HTTP.
func (p *virtualWorkspaces) buildKubernetesMCPHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		logger := klog.FromContext(r.Context()).WithName("kubernetes-mcp-handler")

		// 1. Parse path.
		cluster, kubernetesName, ok := parseKubernetesMCPPath(r.URL.Path)
		if !ok {
			http.Error(w, "invalid path: expected /{cluster}/apis/mcp.kedge.faros.sh/v1alpha1/kubernetes/{name}/mcp", http.StatusBadRequest)
			return
		}

		// 2. Authenticate: require bearer token.
		token := extractBearerToken(r)
		if token == "" {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		// 3. Build a cluster-scoped dynamic client for the kcp cluster.
		if p.kcpConfig == nil {
			http.Error(w, "kcp not configured", http.StatusInternalServerError)
			return
		}
		dynClient, err := clusterScopedDynamicClient(p.kcpConfig, cluster)
		if err != nil {
			logger.Error(err, "failed to build cluster-scoped dynamic client", "cluster", cluster)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

		// 4. Fetch the Kubernetes MCP object.
		ctx := r.Context()
		kmcpObj, err := dynClient.Resource(kubernetesGVR).Get(ctx, kubernetesName, metav1.GetOptions{})
		if err != nil {
			logger.Error(err, "failed to get Kubernetes MCP", "name", kubernetesName)
			http.Error(w, fmt.Sprintf("Kubernetes MCP %q not found: %v", kubernetesName, err), http.StatusNotFound)
			return
		}

		// 5. Parse edge selector from spec.
		var edgeSelector labels.Selector
		specRaw, _, _ := unstructuredNestedMap(kmcpObj.Object, "spec")
		edgeSelectorRaw, _, _ := unstructuredNestedMap(specRaw, "edgeSelector")
		if len(edgeSelectorRaw) > 0 {
			// Convert the unstructured selector to a metav1.LabelSelector.
			ls := &metav1.LabelSelector{}
			if matchLabels, ok := edgeSelectorRaw["matchLabels"].(map[string]interface{}); ok {
				ls.MatchLabels = make(map[string]string)
				for k, v := range matchLabels {
					if s, ok := v.(string); ok {
						ls.MatchLabels[k] = s
					}
				}
			}
			edgeSelector, err = metav1.LabelSelectorAsSelector(ls)
			if err != nil {
				logger.Error(err, "invalid edgeSelector")
				http.Error(w, "invalid edgeSelector", http.StatusBadRequest)
				return
			}
		} else {
			edgeSelector = labels.Everything()
		}

		// 6. List all edges in the cluster and filter by selector + tunnel state.
		edgeList, err := dynClient.Resource(edgeGVRForMCPSelector).List(ctx, metav1.ListOptions{})
		if err != nil {
			logger.Error(err, "failed to list edges", "cluster", cluster)
			http.Error(w, "failed to list edges", http.StatusInternalServerError)
			return
		}

		logger.Info("KubernetesMCP edge resolution",
			"cluster", cluster,
			"edgeCount", len(edgeList.Items),
			"connManagerKeys", p.edgeConnManager.Keys())

		var resolvedEdges []string
		for _, edgeObj := range edgeList.Items {
			edgeName := edgeObj.GetName()
			edgeLabels := edgeObj.GetLabels()

			// Only include kubernetes-type edges — server-type edges provide
			// SSH access only and have no Kubernetes API to proxy via MCP.
			edgeType, _, _ := unstructured.NestedString(edgeObj.Object, "spec", "type")
			if edgeType != "kubernetes" {
				logger.V(4).Info("Skipping non-kubernetes edge", "edge", edgeName, "type", edgeType)
				continue
			}

			if !edgeSelector.Matches(labels.Set(edgeLabels)) {
				logger.V(4).Info("Skipping edge not matching selector", "edge", edgeName)
				continue
			}
			key := edgeConnKey(cluster, edgeName)
			if _, ok := p.edgeConnManager.Load(key); ok {
				resolvedEdges = append(resolvedEdges, edgeName)
			} else {
				logger.Info("Edge not in connManager", "edge", edgeName, "key", key)
			}
		}
		logger.Info("KubernetesMCP resolved edges", "resolvedEdges", resolvedEdges)

		// 7. Build multi-edge provider.
		//    Use internal URL to avoid CDN/proxy loops (same as single-edge handler).
		baseURL := p.hubInternalURL
		if baseURL == "" {
			baseURL = p.hubExternalURL
		}
		provider := &MultiEdgeKedgeEdgeProvider{
			cluster:         cluster,
			edgeNames:       resolvedEdges,
			edgeConnManager: p.edgeConnManager,
			hubBase:         strings.TrimRight(baseURL, "/"),
			bearerToken:     token,
		}

		// 8. Create a stateless MCP server and serve.
		// Start from defaults (core, config, helm toolsets) and override with any
		// toolsets explicitly listed in the KubernetesMCP spec.
		staticCfg := mcpconfig.Default()
		staticCfg.Stateless = true
		if toolsetsRaw, ok := specRaw["toolsets"].([]interface{}); ok && len(toolsetsRaw) > 0 {
			names := make([]string, 0, len(toolsetsRaw))
			for _, t := range toolsetsRaw {
				if s, ok := t.(string); ok {
					names = append(names, s)
				}
			}
			if len(names) > 0 {
				staticCfg.Toolsets = names
			}
		}
		srv, err := mcpserver.NewServer(mcpserver.Configuration{StaticConfig: staticCfg}, provider)
		if err != nil {
			logger.Error(err, "failed to create MCP server", "cluster", cluster, "kubernetes", kubernetesName)
			http.Error(w, fmt.Sprintf("failed to initialize MCP server: %v", err), http.StatusInternalServerError)
			return
		}
		defer srv.Close()

		srv.ServeHTTP().ServeHTTP(w, r)
	})
}

// parseKubernetesMCPPath extracts cluster and Kubernetes MCP name from the path
// seen by the /services/mcp handler.
//
// Expected format after prefix strip:
//
//	/{cluster}/apis/mcp.kedge.faros.sh/v1alpha1/kubernetes/{name}/mcp
func parseKubernetesMCPPath(path string) (cluster, name string, ok bool) {
	path = strings.TrimPrefix(path, "/")
	// Expected segments: [cluster, "apis", "mcp.kedge.faros.sh", "v1alpha1", "kubernetes", name, "mcp"]
	parts := strings.SplitN(path, "/", 8)
	if len(parts) < 7 {
		return "", "", false
	}
	if parts[1] != "apis" || parts[2] != "mcp.kedge.faros.sh" || parts[3] != "v1alpha1" || parts[4] != "kubernetes" || parts[6] != "mcp" {
		return "", "", false
	}
	return parts[0], parts[5], true
}

// unstructuredNestedMap is a helper to extract a nested map from an unstructured object.
func unstructuredNestedMap(obj map[string]interface{}, key string) (map[string]interface{}, bool, error) {
	v, ok := obj[key]
	if !ok {
		return nil, false, nil
	}
	m, ok := v.(map[string]interface{})
	if !ok {
		return nil, false, fmt.Errorf("expected map for key %q, got %T", key, v)
	}
	return m, true, nil
}

// clusterScopedDynamicClient creates a dynamic client scoped to a kcp cluster.
func clusterScopedDynamicClient(kcpConfig *rest.Config, cluster string) (dynamic.Interface, error) {
	if kcpConfig == nil {
		return nil, fmt.Errorf("kcpConfig is nil")
	}
	clusterConfig := *kcpConfig
	clusterConfig.Host = apiurl.HubServerURL(kcpConfig.Host, cluster)
	return dynamic.NewForConfig(&clusterConfig)
}

// KubernetesMCPHandler returns an HTTP handler for the KubernetesMCP endpoint.
// Mount at /services/mcp/.
func (h *VirtualWorkspaceHandlers) KubernetesMCPHandler() http.Handler {
	return h.vws.buildKubernetesMCPHandler()
}
