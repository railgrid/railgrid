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
	"context"
	"fmt"
	"strings"

	mcpapi "github.com/containers/kubernetes-mcp-server/pkg/api"
	mcpkubernetes "github.com/containers/kubernetes-mcp-server/pkg/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

// kedgeEdgeProvider implements the kubernetes-mcp-server Provider interface so
// the MCP server can route Kubernetes API calls to a specific edge's tunnel.
//
// It is single-edge: cluster, resource and edgeName are fixed at construction.
// GetTargets returns edgeName only while the edge's reverse tunnel is registered
// in the provider's ConnManager. All kube API calls are routed through the
// provider's own consumer edgeproxy `k8s` subresource (reached back through the
// hub via hubBase + edgeProxyPublicPath), which streams down the reverse tunnel.
type kedgeEdgeProvider struct {
	cluster             string       // kcp logical-cluster ID, e.g. "11tcw27t4rdtnacy"
	resource            string       // GVR resource, e.g. "kubernetesclusters"
	group               string       // API group, e.g. "edges.kedge.faros.sh"
	version             string       // API version, e.g. "v1alpha1"
	edgeName            string       // fixed edge name, e.g. "my-cluster"
	edgeConnManager     *ConnManager // shared dialer registry (tunnel liveness)
	hubBase             string       // e.g. "https://kedge.example.com" (no trailing slash)
	edgeProxyPublicPath string       // e.g. "/services/providers/edges/edgeproxy"
	bearerToken         string       // caller's bearer token, forwarded to the edgeproxy
}

// Ensure kedgeEdgeProvider implements mcpkubernetes.Provider.
var _ mcpkubernetes.Provider = (*kedgeEdgeProvider)(nil)

// IsOpenShift always returns false — kedge edges are vanilla Kubernetes clusters.
func (p *kedgeEdgeProvider) IsOpenShift(_ context.Context) bool { return false }

// GetTargets returns the single fixed edgeName if its reverse tunnel is
// registered in ConnManager, or an empty slice if the edge is not connected.
func (p *kedgeEdgeProvider) GetTargets(_ context.Context) ([]string, error) {
	key := edgeConnKey(p.resource, p.cluster, p.edgeName)
	if _, ok := p.edgeConnManager.Load(key); ok {
		return []string{p.edgeName}, nil
	}
	return []string{}, nil
}

// k8sSubresourceURL builds the full URL of this edge's k8s subresource, served
// by the provider's own consumer edgeproxy and reached back through the hub:
//
//	{hubBase}{edgeProxyPublicPath}/clusters/{cluster}/apis/{group}/{version}/{resource}/{name}/k8s
func (p *kedgeEdgeProvider) k8sSubresourceURL(edgeName string) string {
	return fmt.Sprintf("%s%s/clusters/%s/apis/%s/%s/%s/%s/k8s",
		strings.TrimRight(p.hubBase, "/"),
		strings.TrimRight(p.edgeProxyPublicPath, "/"),
		p.cluster, p.group, p.version, p.resource, edgeName)
}

// GetDerivedKubernetes returns a *mcpkubernetes.Kubernetes pointing at the edge
// agent's Kubernetes API, reached via the provider's edgeproxy k8s subresource.
func (p *kedgeEdgeProvider) GetDerivedKubernetes(_ context.Context, edgeName string) (*mcpkubernetes.Kubernetes, error) {
	// Guard: if the caller passes an empty edge name (e.g. MCP client sends
	// cluster=""), fall back to the provider's fixed edge name.
	if edgeName == "" {
		edgeName = p.edgeName
	}
	if edgeName == "" {
		return nil, fmt.Errorf("no edge name specified and no default available")
	}
	serverURL := p.k8sSubresourceURL(edgeName)

	restCfg := &rest.Config{
		Host:        serverURL,
		BearerToken: p.bearerToken,
		TLSClientConfig: rest.TLSClientConfig{
			Insecure: true,
		},
	}

	// Build a minimal in-memory kubeconfig so NewKubernetes can construct its
	// clientcmd.ClientConfig.
	rawCfg := clientcmdapi.NewConfig()
	rawCfg.Clusters[edgeName] = &clientcmdapi.Cluster{
		Server:                serverURL,
		InsecureSkipTLSVerify: true,
	}
	rawCfg.AuthInfos[edgeName] = &clientcmdapi.AuthInfo{
		Token: p.bearerToken,
	}
	ctxName := edgeName + "-ctx"
	rawCfg.Contexts[ctxName] = &clientcmdapi.Context{
		Cluster:  edgeName,
		AuthInfo: edgeName,
	}
	rawCfg.CurrentContext = ctxName

	clientCmdConfig := clientcmd.NewDefaultClientConfig(*rawCfg, nil)

	k8s, err := mcpkubernetes.NewKubernetes(minimalBaseConfig{}, clientCmdConfig, restCfg)
	if err != nil {
		return nil, fmt.Errorf("creating kubernetes client for edge %s: %w", edgeName, err)
	}
	return k8s, nil
}

// GetDefaultTarget returns the fixed edge name so MCP tool calls that omit the
// "cluster" parameter automatically route to the only available edge.
func (p *kedgeEdgeProvider) GetDefaultTarget() string { return p.edgeName }

// GetTargetParameterName returns "cluster" as the MCP target query parameter.
// "cluster" is used rather than "edge" because users think in terms of clusters.
func (p *kedgeEdgeProvider) GetTargetParameterName() string { return "cluster" }

// WatchTargets is a no-op; dynamic target reloading is not implemented.
func (p *kedgeEdgeProvider) WatchTargets(_ mcpkubernetes.McpReload) {}

// Close is a no-op.
func (p *kedgeEdgeProvider) Close() {}

// multiEdgeProvider implements the kubernetes-mcp-server Provider interface for
// routing kube tool calls across a SET of edges in one tenant — the provider's
// aggregate `/mcp` endpoint. GetTargets returns the connected subset; the MCP
// "cluster" tool parameter selects which edge a call targets.
type multiEdgeProvider struct {
	cluster             string       // kcp logical-cluster ID
	resource            string       // e.g. "kubernetesclusters"
	group               string       // API group
	version             string       // API version
	edgeNames           []string     // candidate edge names in this tenant
	edgeConnManager     *ConnManager // shared dialer registry
	hubBase             string       // e.g. "https://kedge.example.com" (no trailing slash)
	edgeProxyPublicPath string       // e.g. "/services/providers/edges/edgeproxy"
	bearerToken         string       // caller's bearer token
}

var _ mcpkubernetes.Provider = (*multiEdgeProvider)(nil)

func (p *multiEdgeProvider) IsOpenShift(_ context.Context) bool { return false }

// GetTargets returns only the candidate edges whose reverse tunnel is live.
func (p *multiEdgeProvider) GetTargets(_ context.Context) ([]string, error) {
	var targets []string
	for _, name := range p.edgeNames {
		if _, ok := p.edgeConnManager.Load(edgeConnKey(p.resource, p.cluster, name)); ok {
			targets = append(targets, name)
		}
	}
	return targets, nil
}

// GetDerivedKubernetes delegates to a throwaway single-edge provider for the
// requested edge (falling back to the default target when unspecified).
func (p *multiEdgeProvider) GetDerivedKubernetes(ctx context.Context, edgeName string) (*mcpkubernetes.Kubernetes, error) {
	if edgeName == "" {
		edgeName = p.GetDefaultTarget()
	}
	if edgeName == "" {
		return nil, fmt.Errorf("no edge name specified and no connected edges available")
	}
	single := &kedgeEdgeProvider{
		cluster:             p.cluster,
		resource:            p.resource,
		group:               p.group,
		version:             p.version,
		edgeName:            edgeName,
		edgeConnManager:     p.edgeConnManager,
		hubBase:             p.hubBase,
		edgeProxyPublicPath: p.edgeProxyPublicPath,
		bearerToken:         p.bearerToken,
	}
	return single.GetDerivedKubernetes(ctx, edgeName)
}

// GetDefaultTarget returns the first candidate edge so tool calls that omit the
// "cluster" parameter still resolve to a valid edge.
func (p *multiEdgeProvider) GetDefaultTarget() string {
	if len(p.edgeNames) > 0 {
		return p.edgeNames[0]
	}
	return ""
}

func (p *multiEdgeProvider) GetTargetParameterName() string         { return "cluster" }
func (p *multiEdgeProvider) WatchTargets(_ mcpkubernetes.McpReload) {}
func (p *multiEdgeProvider) Close()                                 {}

// ─── minimalBaseConfig ───────────────────────────────────────────────────────
//
// minimalBaseConfig is the smallest possible implementation of mcpapi.BaseConfig
// needed to construct a mcpkubernetes.Kubernetes instance. All feature flags
// default to their safe zero-values.

type minimalBaseConfig struct{}

var _ mcpapi.BaseConfig = minimalBaseConfig{}

func (minimalBaseConfig) IsRequireOAuth() bool               { return false }
func (minimalBaseConfig) GetClusterProviderStrategy() string { return mcpapi.ClusterProviderDisabled }
func (minimalBaseConfig) GetKubeConfigPath() string          { return "" }
func (minimalBaseConfig) GetDeniedResources() []mcpapi.GroupVersionKind {
	return nil
}
func (minimalBaseConfig) GetProviderConfig(_ string) (mcpapi.ExtendedConfig, bool) {
	return nil, false
}
func (minimalBaseConfig) GetToolsetConfig(_ string) (mcpapi.ExtendedConfig, bool) {
	return nil, false
}
func (minimalBaseConfig) GetStsClientId() string     { return "" } //nolint:staticcheck // interface name from upstream library
func (minimalBaseConfig) GetStsClientSecret() string { return "" }
func (minimalBaseConfig) GetStsAudience() string     { return "" }
func (minimalBaseConfig) GetStsScopes() []string     { return nil }
func (minimalBaseConfig) IsValidationEnabled() bool  { return false }
