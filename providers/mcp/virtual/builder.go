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

// Package virtual builds the MCPServer aggregate virtual-workspace HTTP
// handler. The handler is mounted in the hub at /services/mcpserver/ via
// the BuiltinSpec.VirtualWorkspaceHandler registration in the sibling
// manifest.go.
//
// Per request the handler:
//  1. Parses /{cluster}/apis/kedge.faros.sh/v1alpha1/mcpservers/{name}/mcp
//  2. Fetches the MCPServer CR to read the edgeSelector + toolset config.
//  3. Resolves matching Edges (both kubernetes-type and server-type),
//     filtering out edges with no live tunnel.
//  4. Composes a kubernetes-mcp-server multi-edge provider, a linuxmcp SSH
//     provider, and the in-tree list_targets + kedge://about resources
//     into one aggregate MCP server (pkg providers/mcp/aggregate).
//
// The aggregate server is built fresh per request — stateless mode — so
// the AI client always sees the current edge inventory.
package virtual

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/klog/v2"

	"github.com/faroshq/faros-kedge/pkg/apiurl"
	"github.com/faroshq/faros-kedge/pkg/virtual/builder"
	aggregatemcp "github.com/faroshq/faros-kedge/providers/mcp/aggregate"
)

// mcpServerGVR is the GVR for the aggregate MCPServer CRD this builder
// resolves on every request.
var mcpServerGVR = schema.GroupVersionResource{
	Group:    "kedge.faros.sh",
	Version:  "v1alpha1",
	Resource: "mcpservers",
}

// Build returns the http.Handler the hub mounts at /services/mcpserver/.
// Captures deps in the closure so per-request handler invocations re-read
// the live edge inventory rather than a snapshot.
func Build(deps *builder.Deps) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		logger := klog.FromContext(r.Context()).WithName("mcpserver-handler")

		cluster, name, ok := parseMCPServerPath(r.URL.Path)
		if !ok {
			http.Error(w, "invalid path: expected /{cluster}/apis/kedge.faros.sh/v1alpha1/mcpservers/{name}/mcp", http.StatusBadRequest)
			return
		}

		token := builder.ExtractBearerToken(r)
		if token == "" {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		if deps.KCPConfig == nil {
			http.Error(w, "kcp not configured", http.StatusInternalServerError)
			return
		}
		dynClient, err := builder.ClusterScopedDynamicClient(deps.KCPConfig, cluster)
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

		specRaw, _, _ := builder.UnstructuredNestedMap(obj.Object, "spec")

		edgeSelector, err := mcpServerLabelSelector(specRaw)
		if err != nil {
			logger.Error(err, "invalid edgeSelector")
			http.Error(w, "invalid edgeSelector", http.StatusBadRequest)
			return
		}

		readOnly, _, _ := unstructured.NestedBool(obj.Object, "spec", "readOnly")
		kubeToolsets := unstructuredStringSlice(specRaw["kubernetesToolsets"])
		linuxToolsets := unstructuredStringSlice(specRaw["linuxToolsets"])

		// Edge resolution is still here (per-request, scoped to the CR's
		// selector and live tunnel state) but tool registration has
		// moved into each edge-kind provider's mcp/ subpackage. They
		// read FamilyContext.EdgeNames + Toolsets + Extras and install
		// their tools onto the shared mcp.Server.
		kubeEdges, linuxEdges, err := resolveEdges(ctx, deps, dynClient, cluster, edgeSelector)
		if err != nil {
			logger.Error(err, "failed to resolve edges", "cluster", cluster)
			http.Error(w, "failed to resolve edges", http.StatusInternalServerError)
			return
		}
		logger.Info("MCPServer resolved", "cluster", cluster, "kube", len(kubeEdges), "linux", len(linuxEdges))

		callerIdentity := builder.ResolveCallerIdentity(ctx, deps.KCPConfig, token, deps.Logger)

		// MCP metadata advertised on `initialize` — derived from the CR so
		// each tenant's endpoint is self-describing. `Title` and the per-
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

		about := aggregatemcp.AboutDoc{
			Role:         "aggregate",
			Capabilities: []string{"kubernetes", "linux", "ssh", "list_targets"},
			Tenant:       cluster,
			MCPServer:    name,
			EndpointURL:  deps.HubExternalURL + apiurl.MCPServerPath(cluster, name),
			ReadOnly:     readOnly,
			Toolsets: aggregatemcp.AboutToolsets{
				Kubernetes: kubeToolsets,
				Linux:      linuxToolsets,
			},
		}

		// Collect commandTimeoutSeconds / maxOutputBytes off the CR as
		// Extras for the linux family — typed slots on the aggregator
		// would couple it to every family's knob set, so we pass the
		// raw values through and let serveredges/mcp pull them out.
		linuxExtras := map[string]any{}
		if v, ok := specRaw["commandTimeoutSeconds"]; ok {
			linuxExtras["commandTimeoutSeconds"] = v
		}
		if v, ok := specRaw["maxOutputBytes"]; ok {
			linuxExtras["maxOutputBytes"] = v
		}

		handler, err := aggregatemcp.Handler(aggregatemcp.Config{
			Cluster:        cluster,
			BearerToken:    token,
			HubBaseURL:     deps.HubBaseURL(),
			ReadOnly:       readOnly,
			Deps:           deps,
			CallerIdentity: callerIdentity,
			// Edge subsets keyed by Edge.spec.type. Family registrations
			// declare their EdgeType ("kubernetes" / "server"); the
			// aggregator's familyContextFor picks the matching slice.
			EdgesByType: map[string][]string{
				"kubernetes": kubeEdges,
				"server":     linuxEdges,
			},
			ToolsetsByFamily: map[string][]string{
				"kubernetes": kubeToolsets,
				"linux":      linuxToolsets,
			},
			ExtrasByFamily: map[string]map[string]any{
				"linux": linuxExtras,
			},
			Enumerate: enumerator(deps, cluster, dynClient, edgeSelector),
			Metadata:  meta,
			About:     about,
		})
		if err != nil {
			logger.Error(err, "failed to build aggregate MCP handler")
			http.Error(w, fmt.Sprintf("failed to initialize MCPServer: %v", err), http.StatusInternalServerError)
			return
		}
		handler.ServeHTTP(w, r)
	})
}

// resolveEdges lists every Edge in the tenant workspace, applies the
// label selector, and partitions the survivors into kube-type and
// server-type buckets, skipping any edge whose tunnel isn't currently
// registered with the ConnManager.
func resolveEdges(
	ctx context.Context, deps *builder.Deps, dynClient dynamic.Interface, cluster string, sel labels.Selector,
) (kube, linux []string, err error) {
	edgeList, err := dynClient.Resource(builder.EdgeGVRForMCPSelector).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, nil, err
	}
	for _, edgeObj := range edgeList.Items {
		name := edgeObj.GetName()
		if !sel.Matches(labels.Set(edgeObj.GetLabels())) {
			continue
		}
		if _, ok := deps.EdgeConnManager.Load(builder.EdgeConnKey(cluster, name)); !ok {
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

// enumerator returns a TargetEnumerator that re-resolves the live edge
// inventory on every list_targets call. Snapshotting at handler-build
// time would go stale for long-lived MCP sessions; the AI almost always
// wants the current state.
func enumerator(
	deps *builder.Deps, cluster string, dynClient dynamic.Interface, sel labels.Selector,
) aggregatemcp.TargetEnumerator {
	return func(ctx context.Context) (kube, linux []aggregatemcp.TargetInfo, err error) {
		edgeList, err := dynClient.Resource(builder.EdgeGVRForMCPSelector).List(ctx, metav1.ListOptions{})
		if err != nil {
			return nil, nil, err
		}
		for _, edgeObj := range edgeList.Items {
			name := edgeObj.GetName()
			if !sel.Matches(labels.Set(edgeObj.GetLabels())) {
				continue
			}
			connected := false
			if _, ok := deps.EdgeConnManager.Load(builder.EdgeConnKey(cluster, name)); ok {
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

// mcpServerLabelSelector extracts a label selector from the MCPServer
// spec map. Absent / empty selector means labels.Everything().
func mcpServerLabelSelector(spec map[string]any) (labels.Selector, error) {
	raw, _, _ := builder.UnstructuredNestedMap(spec, "edgeSelector")
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
	// LinuxMCP also skip them today. Easy to add when we need them.
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

// parseMCPServerPath extracts cluster + MCPServer name from the path
// seen by the /services/mcpserver handler after StripPrefix.
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
