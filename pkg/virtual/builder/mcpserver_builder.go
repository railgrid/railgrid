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
	"fmt"
	"net/http"
	"strings"
	"time"

	mcpconfig "github.com/containers/kubernetes-mcp-server/pkg/config"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/klog/v2"

	"github.com/faroshq/faros-kedge/pkg/aggregatemcp"
	"github.com/faroshq/faros-kedge/pkg/apiurl"
	"github.com/faroshq/faros-kedge/pkg/linuxmcp"
)

// mcpServerGVR is the GVR for the aggregate MCPServer CRD.
var mcpServerGVR = schema.GroupVersionResource{
	Group:    "kedge.faros.sh",
	Version:  "v1alpha1",
	Resource: "mcpservers",
}

// buildMCPServerHandler serves the aggregate MCPServer endpoint.
//
// URL pattern (after the /services/mcpserver prefix is stripped):
//
//	/{cluster}/apis/kedge.faros.sh/v1alpha1/mcpservers/{name}/mcp
//
// Per request the handler resolves the CR's edgeSelector against the live
// edge inventory, partitions the matches into kube-type and server-type
// buckets (only counting those with an active tunnel), then constructs an
// aggregate MCP server that exposes:
//   - kubernetes-mcp-server toolsets bound to the kube edges,
//   - linuxmcp toolsets bound to the server edges,
//   - a `list_targets` tool over the union, so the AI can self-discover
//     what's reachable before it tries calling anything else.
func (p *virtualWorkspaces) buildMCPServerHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		logger := klog.FromContext(r.Context()).WithName("mcpserver-handler")

		cluster, name, ok := parseMCPServerPath(r.URL.Path)
		if !ok {
			http.Error(w, "invalid path: expected /{cluster}/apis/kedge.faros.sh/v1alpha1/mcpservers/{name}/mcp", http.StatusBadRequest)
			return
		}

		token := extractBearerToken(r)
		if token == "" {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

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

		ctx := r.Context()
		obj, err := dynClient.Resource(mcpServerGVR).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			logger.Error(err, "failed to get MCPServer", "name", name)
			http.Error(w, fmt.Sprintf("MCPServer %q not found: %v", name, err), http.StatusNotFound)
			return
		}

		specRaw, _, _ := unstructuredNestedMap(obj.Object, "spec")

		edgeSelector, err := mcpServerLabelSelector(specRaw)
		if err != nil {
			logger.Error(err, "invalid edgeSelector")
			http.Error(w, "invalid edgeSelector", http.StatusBadRequest)
			return
		}

		readOnly, _, _ := unstructured.NestedBool(obj.Object, "spec", "readOnly")
		kubeToolsets := unstructuredStringSlice(specRaw["kubernetesToolsets"])
		linuxToolsets := unstructuredStringSlice(specRaw["linuxToolsets"])
		var cmdTimeout time.Duration
		if v, ok := specRaw["commandTimeoutSeconds"].(int64); ok && v > 0 {
			cmdTimeout = time.Duration(v) * time.Second
		}
		var maxOut int
		if v, ok := specRaw["maxOutputBytes"].(int64); ok && v > 0 {
			maxOut = int(v)
		}

		kubeEdges, linuxEdges, err := p.resolveMCPServerEdges(ctx, dynClient, cluster, edgeSelector)
		if err != nil {
			logger.Error(err, "failed to resolve edges", "cluster", cluster)
			http.Error(w, "failed to resolve edges", http.StatusInternalServerError)
			return
		}
		logger.Info("MCPServer resolved", "cluster", cluster, "kube", len(kubeEdges), "linux", len(linuxEdges))

		hubBase := p.hubInternalURL
		if hubBase == "" {
			hubBase = p.hubExternalURL
		}
		hubBase = strings.TrimRight(hubBase, "/")

		kubeProvider := &MultiEdgeKedgeEdgeProvider{
			cluster:         cluster,
			edgeNames:       kubeEdges,
			edgeConnManager: p.edgeConnManager,
			hubBase:         hubBase,
			bearerToken:     token,
		}

		callerIdentity := resolveCallerIdentity(ctx, p.kcpConfig, token, p.logger)
		linuxProvider := linuxmcp.NewProvider(linuxmcp.Config{
			Cluster:        cluster,
			EdgeNames:      linuxEdges,
			OpenSession:    p.linuxMCPOpenSessionFn(cluster, callerIdentity),
			CommandTimeout: cmdTimeout,
			MaxOutputBytes: maxOut,
			ReadOnly:       readOnly,
		})

		kubeStaticCfg := mcpconfig.Default()
		kubeStaticCfg.Stateless = true
		kubeStaticCfg.ReadOnly = readOnly
		if len(kubeToolsets) > 0 {
			kubeStaticCfg.Toolsets = kubeToolsets
		}

		// MCP metadata advertised on `initialize` — derived from the CR so
		// each tenant's endpoint is self-describing.  `Title` and the per-
		// endpoint Instructions teach AI clients what's reachable through
		// THIS specific kedge endpoint before they even call list_targets.
		//
		// spec.displayName and spec.instructions override the auto-generated
		// values; users typically set the latter to add environment-specific
		// guardrails ("this is prod, ask before destructive ops", "this
		// tenant only hosts staging clusters", etc.).
		displayName, _, _ := unstructured.NestedString(obj.Object, "spec", "displayName")
		instructions, _, _ := unstructured.NestedString(obj.Object, "spec", "instructions")
		title := displayName
		if title == "" {
			title = fmt.Sprintf("Kedge — %s (tenant %s)", name, cluster)
		}
		if instructions == "" {
			instructions = fmt.Sprintf(
				"You are connected to the kedge aggregate MCP endpoint %q in tenant workspace %q.\n\n"+
					"This single endpoint exposes:\n"+
					"  - Every Kubernetes cluster registered as a kube-type edge in this tenant (driven via kubernetes-mcp-server tools). Pass the edge name as the \"cluster\" argument.\n"+
					"  - Every Linux server registered as a server-type edge in this tenant (driven via kedge linux toolsets over SSH). Pass the edge name as the \"target\" argument.\n\n"+
					"Always call \"list_targets\" first to enumerate every reachable edge, its kind, labels and live connection state. Never guess target names.\n\n"+
					"Tool families do not cross types: kube tools (e.g. namespaces_list, pods_list_in_namespace) only operate on kubernetes-type edges; linux tools (e.g. run_command, read_file, systemd_unit_status) only operate on server-type edges. The \"type\" field in list_targets output tells you which family applies.",
				name, cluster,
			)
		}
		meta := aggregatemcp.ServerMetadata{
			Name:         "kedge-mcpserver-" + name,
			Title:        title,
			Instructions: instructions,
		}

		// AboutDoc is the structured "what is this server" payload served at
		// kedge://about — clients fetch it via resources/read to obtain a
		// machine-readable counterpart to the free-form Instructions string.
		// Fields the aggregator can derive at read time (ConnectedEdges,
		// schema version, defaults) are filled by buildAboutSnapshot; we
		// only set the stable identity / capability bits here.
		about := aggregatemcp.AboutDoc{
			Role:         "aggregate",
			Capabilities: []string{"kubernetes", "linux", "ssh", "list_targets"},
			Tenant:       cluster,
			MCPServer:    name,
			EndpointURL:  p.hubExternalURL + apiurl.MCPServerPath(cluster, name),
			ReadOnly:     readOnly,
			Toolsets: aggregatemcp.AboutToolsets{
				Kubernetes: kubeToolsets,
				Linux:      linuxToolsets,
			},
		}

		handler, err := aggregatemcp.Handler(aggregatemcp.Config{
			KubeProvider:     kubeProvider,
			KubeStaticConfig: kubeStaticCfg,
			LinuxProvider:    linuxProvider,
			LinuxToolsets:    linuxToolsets,
			Enumerate:        p.mcpServerEnumerator(cluster, dynClient, edgeSelector),
			Metadata:         meta,
			About:            about,
		})
		if err != nil {
			logger.Error(err, "failed to build aggregate MCP handler")
			http.Error(w, fmt.Sprintf("failed to initialize MCPServer: %v", err), http.StatusInternalServerError)
			return
		}
		handler.ServeHTTP(w, r)
	})
}

// resolveMCPServerEdges lists every Edge in the tenant workspace, applies
// the label selector, and partitions the survivors into kube-type and
// server-type buckets, skipping any edge whose tunnel isn't currently
// registered with the ConnManager.
func (p *virtualWorkspaces) resolveMCPServerEdges(
	ctx context.Context, dynClient dynamic.Interface, cluster string, sel labels.Selector,
) (kube, linux []string, err error) {
	edgeList, err := dynClient.Resource(edgeGVRForMCPSelector).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, nil, err
	}
	for _, edgeObj := range edgeList.Items {
		name := edgeObj.GetName()
		if !sel.Matches(labels.Set(edgeObj.GetLabels())) {
			continue
		}
		if _, ok := p.edgeConnManager.Load(edgeConnKey(cluster, name)); !ok {
			continue
		}
		edgeType, _, _ := unstructured.NestedString(edgeObj.Object, "spec", "type")
		switch edgeType {
		case "kubernetes":
			kube = append(kube, name)
		case "server":
			linux = append(linux, name)
		}
	}
	return kube, linux, nil
}

// mcpServerEnumerator returns a TargetEnumerator that re-resolves the live
// edge inventory on every list_targets call.  Snapshotting at handler-build
// time would go stale for long-lived MCP sessions; the AI almost always
// wants the current state.
func (p *virtualWorkspaces) mcpServerEnumerator(
	cluster string, dynClient dynamic.Interface, sel labels.Selector,
) aggregatemcp.TargetEnumerator {
	return func(ctx context.Context) (kube, linux []aggregatemcp.TargetInfo, err error) {
		edgeList, err := dynClient.Resource(edgeGVRForMCPSelector).List(ctx, metav1.ListOptions{})
		if err != nil {
			return nil, nil, err
		}
		for _, edgeObj := range edgeList.Items {
			name := edgeObj.GetName()
			if !sel.Matches(labels.Set(edgeObj.GetLabels())) {
				continue
			}
			connected := false
			if _, ok := p.edgeConnManager.Load(edgeConnKey(cluster, name)); ok {
				connected = true
			}
			edgeType, _, _ := unstructured.NestedString(edgeObj.Object, "spec", "type")
			info := aggregatemcp.TargetInfo{
				Name:      name,
				Connected: connected,
				Labels:    edgeObj.GetLabels(),
			}
			switch edgeType {
			case "kubernetes":
				info.Type = "kubernetes"
				kube = append(kube, info)
			case "server":
				info.Type = "linux"
				linux = append(linux, info)
			}
		}
		return kube, linux, nil
	}
}

// mcpServerLabelSelector extracts a label selector from the spec map.  An
// absent / empty selector means labels.Everything() — i.e. match all edges.
func mcpServerLabelSelector(spec map[string]any) (labels.Selector, error) {
	raw, _, _ := unstructuredNestedMap(spec, "edgeSelector")
	if len(raw) == 0 {
		return labels.Everything(), nil
	}
	ls := &metav1.LabelSelector{}
	if ml, ok := raw["matchLabels"].(map[string]any); ok {
		ls.MatchLabels = make(map[string]string, len(ml))
		for k, v := range ml {
			if s, ok := v.(string); ok {
				ls.MatchLabels[k] = s
			}
		}
	}
	// matchExpressions intentionally omitted for v1; KubernetesMCP /
	// LinuxMCP also skip them today.  Easy to add when we need them.
	return metav1.LabelSelectorAsSelector(ls)
}

// unstructuredStringSlice coerces a []any of strings (as decoded from
// JSON/YAML) into []string, silently dropping non-string entries.
func unstructuredStringSlice(v any) []string {
	raw, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, x := range raw {
		if s, ok := x.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// parseMCPServerPath extracts cluster + MCPServer name from the path seen
// by the /services/mcpserver handler after StripPrefix.
//
// Expected format:
//
//	/{cluster}/apis/kedge.faros.sh/v1alpha1/mcpservers/{name}/mcp
func parseMCPServerPath(path string) (cluster, name string, ok bool) {
	path = strings.TrimPrefix(path, "/")
	parts := strings.SplitN(path, "/", 8)
	if len(parts) < 7 {
		return "", "", false
	}
	if parts[1] != "apis" || parts[2] != "kedge.faros.sh" || parts[3] != "v1alpha1" ||
		parts[4] != "mcpservers" || parts[6] != "mcp" {
		return "", "", false
	}
	return parts[0], parts[5], true
}

// MCPServerHandler returns an HTTP handler for the aggregate MCPServer
// endpoint.  Mount at /services/mcpserver/.
func (h *VirtualWorkspaceHandlers) MCPServerHandler() http.Handler {
	return h.vws.buildMCPServerHandler()
}
