/*
Copyright 2026 The Railgrid Authors.

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

// Package apiurl is the single source of truth for all railgrid service path
// construction and URL parsing. All packages that build or decompose railgrid
// hub URLs should use the helpers here instead of hand-crafting strings.
package apiurl

import (
	"fmt"
	"net/url"
	"strings"
)

// Path prefix constants for railgrid virtual-workspace services and auth endpoints.
// Hub-specific endpoints live under /services and /auth — distinct from
// kcp's native /clusters, /apis/<group>, /api/v1 paths, which are forwarded
// straight to kcp.
const (
	// PathPrefixMCP + PathPrefixLinuxMCP were removed in the MCP
	// collapse refactor — both surfaces live behind PathPrefixMCPServer
	// (the aggregate endpoint) now.
	PathPrefixMCPServer      = "/services/mcpserver"
	PathPrefixProvidersUI    = "/ui/providers"
	PathPrefixProvidersProxy = "/services/providers"
	// PathPrefixAPIExportVW is kcp's APIExport virtual-workspace prefix, which
	// the hub forwards verbatim to kcp so a provider running OUTSIDE the
	// platform can watch its own APIExport. Shape:
	//
	//	/services/apiexport/{cluster}/{export}/clusters/{wildcard}/...
	//
	// Unlike the prefixes above, the hub does not own this path — it is kcp's,
	// and the segments are kcp's to interpret. The hub only authorizes and
	// relays. See docs/byo-providers.md.
	PathPrefixAPIExportVW = "/services/apiexport"
	PathAuthAuthorize     = "/auth/authorize"
	PathAuthCallback      = "/auth/callback"
	PathAuthRefresh       = "/auth/refresh"
	PathAuthTokenLogin    = "/auth/token-login"
	PathHealthz           = "/healthz"
	PathVersion           = "/version"
)

// SplitBaseAndCluster splits a URL that contains a /clusters/<name> path into
// a base URL (scheme+host only, no trailing slash) and the kcp cluster name.
//
// Examples:
//
//	"https://hub:9443/clusters/abc123"            → ("https://hub:9443", "abc123")
//	"https://hub:9443/clusters/abc123/extra/path" → ("https://hub:9443", "abc123")
//	"https://hub:9443"                            → ("https://hub:9443", "default")
//	"https://hub:9443/"                           → ("https://hub:9443", "default")
//
// Returns (trimmed url, "default") on parse error.
func SplitBaseAndCluster(rawURL string) (base, cluster string) {
	u, err := url.Parse(strings.TrimRight(rawURL, "/"))
	if err != nil {
		return strings.TrimRight(rawURL, "/"), "default"
	}
	parts := strings.SplitN(strings.TrimPrefix(u.Path, "/"), "/", 4)
	if len(parts) >= 2 && parts[0] == "clusters" && parts[1] != "" {
		u.Path = ""
		u.RawPath = ""
		return strings.TrimRight(u.String(), "/"), parts[1]
	}
	u.Path = ""
	u.RawPath = ""
	return strings.TrimRight(u.String(), "/"), "default"
}

// HubServerURL returns a URL with a /clusters/<cluster> suffix, suitable for
// both user-facing kubeconfigs (routed via the hub) and internal kcp client
// configurations. The hub forwards /clusters/* paths straight to kcp, so the
// same URL form works for both purposes.
//
// If hubBase already contains a /clusters/ path it is replaced.
//
// Example: HubServerURL("https://hub:9443", "abc123") → "https://hub:9443/clusters/abc123"
func HubServerURL(hubBase, cluster string) string {
	base := strings.TrimSuffix(hubBase, "/")
	if idx := strings.Index(base, "/clusters/"); idx != -1 {
		base = base[:idx]
	}
	return base + "/clusters/" + cluster
}

// KCPClusterURL is an alias for HubServerURL. In earlier iterations the hub
// had a prefix (/api or /apis) that the router stripped before forwarding to
// kcp, which required distinguishing "external hub" vs "internal kcp" URL
// forms. That distinction is gone; /clusters/ goes straight through.
func KCPClusterURL(kcpBase, cluster string) string {
	return HubServerURL(kcpBase, cluster)
}

// EdgeProviderCoordinates resolves an edge type ("kubernetes" | "server" | "macos") to the
// owning provider's name, API group and resource. The edge plane
// is one provider `edges` holding all kinds under group edges.railgrid.ai;
// only the resource differs by type. Unknown values default to kubernetes for
// backwards compatibility with callers that omitted the edge type.
func EdgeProviderCoordinates(edgeType string) (provider, group, resource string) {
	if edgeType == "server" {
		return "edges", "edges.railgrid.ai", "linuxservers"
	}
	if edgeType == "macos" {
		return "edges", "edges.railgrid.ai", "macosservers"
	}
	return "edges", "edges.railgrid.ai", "kubernetesclusters"
}

// ProviderAgentProxyPath returns the agent-ingress path for an edge provider's
// reverse-tunnel control connection: Pillar 2 route class (f), routed through
// the hub backend proxy to the provider Service. It is not a data-plane verb
// (the agent is not a kcp client; it authenticates with a join token or its
// edge ServiceAccount and holds a long-lived tunnel), so it is the one
// provider path that stays on the hub proxy. The provider sees the path
// unmodified.
//
// Pattern: /services/providers/{provider}/agent/clusters/{cluster}/{resource}/{name}/proxy
func ProviderAgentProxyPath(provider, resource, cluster, edgeName, verb string) string {
	return fmt.Sprintf("%s/%s/agent/clusters/%s/%s/%s/%s",
		PathPrefixProvidersProxy, provider, cluster, resource, edgeName, verb)
}

// ProviderAgentProxyURL returns the full agent-ingress URL for use when dialling
// the hub from the agent, resolving the provider coordinates from the edge type.
func ProviderAgentProxyURL(hubBase, edgeType, cluster, edgeName, verb string) string {
	provider, _, resource := EdgeProviderCoordinates(edgeType)
	return strings.TrimRight(hubBase, "/") +
		ProviderAgentProxyPath(provider, resource, cluster, edgeName, verb)
}

// ProviderVerbPath returns the path of a provider data-plane verb: the kcp
// custom subresource "{resource}/{verb}" the provider publishes on its
// APIExport, addressed like any other kube API path on the front door the
// caller holds a credential for (the hub's /clusters/{id}). kcp authorizes
// SSAR "create" on the subresource with ordinary RBAC and reverse-proxies the
// request to the provider. There is no hub-side grammar for a verb: this is
// the only spelling.
//
// Pattern: /clusters/{cluster}/apis/{group}/{version}/{resource}/{name}/{verb}
//
// A verb on one component of a multi-component object carries the component
// as the "component" query parameter (provider-sdk/dataplane.ComponentQuery),
// which this helper leaves to the caller.
func ProviderVerbPath(cluster, group, version, resource, name, verb string) string {
	return fmt.Sprintf("/clusters/%s/apis/%s/%s/%s/%s/%s", cluster, group, version, resource, name, verb)
}

// ProviderVerbURL is ProviderVerbPath against a hub (or kcp front door) base URL.
func ProviderVerbURL(base, cluster, group, version, resource, name, verb string) string {
	return strings.TrimRight(base, "/") + ProviderVerbPath(cluster, group, version, resource, name, verb)
}

// EdgesAPIGroup and EdgesAPIVersion are the edges provider's API coordinates,
// shared by every edge verb helper below.
const (
	EdgesAPIGroup   = "edges.railgrid.ai"
	EdgesAPIVersion = "v1alpha1"
)

// EdgeVerbPath returns the kube path of a verb on an edge (a
// kubernetescluster, linuxserver or macosserver): its Kubernetes API ("k8s"),
// its SSH session ("ssh"), its MCP server ("mcp"), or an agent's own
// credential verbs ("agent-token", "ssh-credentials", "addon-credentials").
func EdgeVerbPath(cluster, resource, name, verb string) string {
	return ProviderVerbPath(cluster, EdgesAPIGroup, EdgesAPIVersion, resource, name, verb)
}

// EdgeVerbURL is EdgeVerbPath against a hub base URL, resolving the resource
// from the edge type.
func EdgeVerbURL(hubBase, edgeType, cluster, name, verb string) string {
	_, _, resource := EdgeProviderCoordinates(edgeType)
	return strings.TrimRight(hubBase, "/") + EdgeVerbPath(cluster, resource, name, verb)
}

// EdgeServiceProxyPath returns the kube path of a verb on a Service published
// from an edge, served by the edges provider as a custom subresource.
//
// verb is "proxy" (HTTP data plane) or "mcp".
//
// Pattern: /clusters/{cluster}/apis/edges.railgrid.ai/v1alpha1/services/{name}/{verb}
func EdgeServiceProxyPath(cluster, name, verb string) string {
	return EdgeVerbPath(cluster, "services", name, verb)
}

// EdgeServiceProxyURL returns the full Service verb URL.
func EdgeServiceProxyURL(hubBase, cluster, name, verb string) string {
	return strings.TrimRight(hubBase, "/") + EdgeServiceProxyPath(cluster, name, verb)
}

// KubernetesMCPPath / KubernetesMCPURL / LinuxMCPPath / LinuxMCPURL
// were removed when the dedicated per-kind MCP endpoints collapsed
// into the MCPServer aggregate. Use MCPServerURL below for the single
// unified endpoint.

// MCPServerPath returns the URL path for the unified MCPServer virtual
// workspace endpoint (aggregates kube + linux edges).
//
// Pattern: /services/mcpserver/{cluster}/apis/railgrid.ai/v1alpha1/mcpservers/{name}/mcp
func MCPServerPath(cluster, mcpServerName string) string {
	return fmt.Sprintf("%s/%s/apis/railgrid.ai/v1alpha1/mcpservers/%s/mcp",
		PathPrefixMCPServer, cluster, mcpServerName)
}

// MCPServerURL returns the full MCPServer endpoint URL.
func MCPServerURL(hubBase, cluster, mcpServerName string) string {
	return strings.TrimRight(hubBase, "/") + MCPServerPath(cluster, mcpServerName)
}

// EdgeAPIPath returns the kcp API path for an Edge resource, suitable for use
// as a client Host suffix or in kubeconfig server URLs.
//
// Pattern: /clusters/{cluster}/apis/railgrid.ai/v1alpha1/edges/{name}
func EdgeAPIPath(cluster, edgeName string) string {
	return fmt.Sprintf("/clusters/%s/apis/railgrid.ai/v1alpha1/edges/%s", cluster, edgeName)
}

// ExternalizeURL replaces the scheme and host in edgeURL with those from
// hubBase, making an internal edge-proxy URL routable through the public hub.
//
// If edgeURL does not start with /services/ it is returned unchanged.
func ExternalizeURL(edgeURL, hubBase string) (string, error) {
	// A hub-relative path is either a kube path on the front door (an edge's
	// status.URL names its k8s or ssh verb under /clusters/{id}/apis/…) or a
	// backend-proxy route (/services/…). Anything else is already absolute.
	if !strings.HasPrefix(edgeURL, "/clusters/") && !strings.HasPrefix(edgeURL, "/services/") {
		return edgeURL, nil
	}
	hub, err := url.Parse(strings.TrimRight(hubBase, "/"))
	if err != nil {
		return "", fmt.Errorf("parsing hub base URL %q: %w", hubBase, err)
	}
	result := hub.Scheme + "://" + hub.Host + edgeURL
	return result, nil
}
