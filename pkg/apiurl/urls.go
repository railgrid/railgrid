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

// Package apiurl is the single source of truth for all kedge service path
// construction and URL parsing. All packages that build or decompose kedge
// hub URLs should use the helpers here instead of hand-crafting strings.
package apiurl

import (
	"fmt"
	"net/url"
	"strings"
)

// Path prefix constants for kedge virtual-workspace services and auth endpoints.
// Hub-specific endpoints live under /services, /auth, /graphql — distinct from
// kcp's native /clusters, /apis/<group>, /api/v1 paths, which are forwarded
// straight to kcp.
const (
	PathPrefixAgentProxy = "/services/agent-proxy"
	PathPrefixEdgesProxy = "/services/edges-proxy"
	// PathPrefixMCP + PathPrefixLinuxMCP were removed in the MCP
	// collapse refactor — both surfaces live behind PathPrefixMCPServer
	// (the aggregate endpoint) now.
	PathPrefixMCPServer      = "/services/mcpserver"
	PathPrefixProvidersUI    = "/ui/providers"
	PathPrefixProvidersProxy = "/services/providers"
	PathAuthAuthorize        = "/auth/authorize"
	PathAuthCallback         = "/auth/callback"
	PathAuthRefresh          = "/auth/refresh"
	PathAuthTokenLogin       = "/auth/token-login"
	PathHealthz              = "/healthz"
	PathVersion              = "/version"
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

// EdgeAgentProxyPath returns the URL path (relative to the hub base) for the
// agent-proxy virtual workspace endpoint.
//
// Pattern: /services/agent-proxy/{cluster}/apis/kedge.faros.sh/v1alpha1/edges/{name}/{subresource}
func EdgeAgentProxyPath(cluster, edgeName, subresource string) string {
	return fmt.Sprintf("%s/%s/apis/kedge.faros.sh/v1alpha1/edges/%s/%s",
		PathPrefixAgentProxy, cluster, edgeName, subresource)
}

// EdgeAgentProxyURL returns the full agent-proxy URL for use when dialling the
// hub tunnel endpoint.
func EdgeAgentProxyURL(hubBase, cluster, edgeName, subresource string) string {
	return strings.TrimRight(hubBase, "/") + EdgeAgentProxyPath(cluster, edgeName, subresource)
}

// EdgeProviderCoordinates resolves an edge type ("kubernetes" | "server") to the
// owning provider's backend-proxy name, API group and resource. The edge plane
// is one provider `edges` holding both kinds under group edges.kedge.faros.sh;
// only the resource differs by type. Any value other than "server" defaults to
// kubernetes.
func EdgeProviderCoordinates(edgeType string) (provider, group, resource string) {
	if edgeType == "server" {
		return "edges", "edges.kedge.faros.sh", "linuxservers"
	}
	return "edges", "edges.kedge.faros.sh", "kubernetesclusters"
}

// ProviderAgentProxyPath returns the agent-ingress path for an edge provider's
// reverse-tunnel control connection, routed through the hub backend proxy to the
// provider Service. The provider StripPrefixes /services/providers/{provider}/agent
// so its tunnel handler sees /{cluster}/apis/{group}/v1alpha1/{resource}/{name}/{subresource}.
//
// Pattern: /services/providers/{provider}/agent/{cluster}/apis/{group}/v1alpha1/{resource}/{name}/{subresource}
func ProviderAgentProxyPath(provider, group, resource, cluster, edgeName, subresource string) string {
	return fmt.Sprintf("%s/%s/agent/%s/apis/%s/v1alpha1/%s/%s/%s",
		PathPrefixProvidersProxy, provider, cluster, group, resource, edgeName, subresource)
}

// ProviderAgentProxyURL returns the full agent-ingress URL for use when dialling
// the hub from the agent, resolving the provider coordinates from the edge type.
func ProviderAgentProxyURL(hubBase, edgeType, cluster, edgeName, subresource string) string {
	provider, group, resource := EdgeProviderCoordinates(edgeType)
	return strings.TrimRight(hubBase, "/") +
		ProviderAgentProxyPath(provider, group, resource, cluster, edgeName, subresource)
}

// EdgeProxyPath returns the URL path (relative to the hub base) for the
// edges-proxy virtual workspace endpoint.
//
// Pattern: /services/edges-proxy/clusters/{cluster}/apis/kedge.faros.sh/v1alpha1/edges/{name}/{subresource}
func EdgeProxyPath(cluster, edgeName, subresource string) string {
	return fmt.Sprintf("%s/clusters/%s/apis/kedge.faros.sh/v1alpha1/edges/%s/%s",
		PathPrefixEdgesProxy, cluster, edgeName, subresource)
}

// EdgeProxyURL returns the full edges-proxy URL, combining the hub base URL
// with the EdgeProxyPath.
func EdgeProxyURL(hubBase, cluster, edgeName, subresource string) string {
	return strings.TrimRight(hubBase, "/") + EdgeProxyPath(cluster, edgeName, subresource)
}

// KubernetesMCPPath / KubernetesMCPURL / LinuxMCPPath / LinuxMCPURL
// were removed when the dedicated per-kind MCP endpoints collapsed
// into the MCPServer aggregate. Use MCPServerURL below for the single
// unified endpoint.

// MCPServerPath returns the URL path for the unified MCPServer virtual
// workspace endpoint (aggregates kube + linux edges).
//
// Pattern: /services/mcpserver/{cluster}/apis/kedge.faros.sh/v1alpha1/mcpservers/{name}/mcp
func MCPServerPath(cluster, mcpServerName string) string {
	return fmt.Sprintf("%s/%s/apis/kedge.faros.sh/v1alpha1/mcpservers/%s/mcp",
		PathPrefixMCPServer, cluster, mcpServerName)
}

// MCPServerURL returns the full MCPServer endpoint URL.
func MCPServerURL(hubBase, cluster, mcpServerName string) string {
	return strings.TrimRight(hubBase, "/") + MCPServerPath(cluster, mcpServerName)
}

// EdgeAPIPath returns the kcp API path for an Edge resource, suitable for use
// as a client Host suffix or in kubeconfig server URLs.
//
// Pattern: /clusters/{cluster}/apis/kedge.faros.sh/v1alpha1/edges/{name}
func EdgeAPIPath(cluster, edgeName string) string {
	return fmt.Sprintf("/clusters/%s/apis/kedge.faros.sh/v1alpha1/edges/%s", cluster, edgeName)
}

// ExternalizeURL replaces the scheme and host in edgeURL with those from
// hubBase, making an internal edge-proxy URL routable through the public hub.
//
// If edgeURL does not start with /services/ it is returned unchanged.
func ExternalizeURL(edgeURL, hubBase string) (string, error) {
	if !strings.HasPrefix(edgeURL, "/services/") {
		return edgeURL, nil
	}
	hub, err := url.Parse(strings.TrimRight(hubBase, "/"))
	if err != nil {
		return "", fmt.Errorf("parsing hub base URL %q: %w", hubBase, err)
	}
	result := hub.Scheme + "://" + hub.Host + edgeURL
	return result, nil
}
