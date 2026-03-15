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

	mcpapi "github.com/containers/kubernetes-mcp-server/pkg/api"
	mcpkubernetes "github.com/containers/kubernetes-mcp-server/pkg/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/faroshq/faros-kedge/pkg/apiurl"
)

// KedgeEdgeProvider implements the kubernetes-mcp-server Provider interface so
// that the MCP server can route Kubernetes API calls to a specific Edge tunnel.
//
// Unlike the previous multi-edge aggregator, this provider is single-edge:
// cluster and edgeName are fixed at construction time. GetTargets returns the
// single edgeName if its tunnel is active, or an empty slice if not connected.
type KedgeEdgeProvider struct {
	cluster         string       // kcp cluster path, e.g. "root:kedge:user-default"
	edgeName        string       // fixed edge name, e.g. "my-edge"
	edgeConnManager *ConnManager // shared dialer registry
	hubBase         string       // e.g. "https://kedge.example.com" (no trailing slash, no services path)
	bearerToken     string       // caller's bearer token to forward to edge proxies
}

// Ensure KedgeEdgeProvider implements mcpkubernetes.Provider.
var _ mcpkubernetes.Provider = (*KedgeEdgeProvider)(nil)

// IsOpenShift always returns false — kedge edges are vanilla Kubernetes clusters.
func (p *KedgeEdgeProvider) IsOpenShift(_ context.Context) bool {
	return false
}

// GetTargets returns the single fixed edgeName if its reverse tunnel is
// registered in ConnManager, or an empty slice if the edge is not connected.
func (p *KedgeEdgeProvider) GetTargets(_ context.Context) ([]string, error) {
	key := edgeConnKey(p.cluster, p.edgeName)
	if _, ok := p.edgeConnManager.Load(key); ok {
		return []string{p.edgeName}, nil
	}
	return []string{}, nil
}

// GetDerivedKubernetes returns a *mcpkubernetes.Kubernetes pointing at the edge
// agent's Kubernetes API, reachable via the hub's edges-proxy endpoint.
func (p *KedgeEdgeProvider) GetDerivedKubernetes(_ context.Context, edgeName string) (*mcpkubernetes.Kubernetes, error) {
	// Guard: if the caller passes an empty edge name (e.g. MCP client sends
	// cluster=""), fall back to the provider's fixed edge name.
	if edgeName == "" {
		edgeName = p.edgeName
	}
	if edgeName == "" {
		return nil, fmt.Errorf("no edge name specified and no default available")
	}
	serverURL := apiurl.EdgeProxyURL(p.hubBase, p.cluster, edgeName, "k8s")

	restCfg := &rest.Config{
		Host:        serverURL,
		BearerToken: p.bearerToken,
		TLSClientConfig: rest.TLSClientConfig{
			Insecure: true,
		},
	}

	// Build a minimal in-memory kubeconfig so that NewKubernetes can
	// construct its clientcmd.ClientConfig.
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

// GetDefaultTarget returns the fixed edge name so that MCP tool calls that
// omit the "cluster" parameter automatically route to the only available edge.
func (p *KedgeEdgeProvider) GetDefaultTarget() string { return p.edgeName }

// GetTargetParameterName returns "cluster" as the MCP target query parameter.
// "cluster" is used rather than "edge" because users think in terms of clusters,
// not kedge-internal "edge" terminology.
func (p *KedgeEdgeProvider) GetTargetParameterName() string { return "cluster" }

// WatchTargets is a no-op for v1; dynamic target reloading is not implemented.
func (p *KedgeEdgeProvider) WatchTargets(_ mcpkubernetes.McpReload) {}

// Close is a no-op.
func (p *KedgeEdgeProvider) Close() {}

// ─── MultiEdgeKedgeEdgeProvider ─────────────────────────────────────────────

// MultiEdgeKedgeEdgeProvider implements the kubernetes-mcp-server Provider
// interface for a set of explicitly listed edge names.  It is used by the
// KubernetesMCP handler which resolves edges via a label selector before
// constructing the provider.
// MultiEdgeKedgeEdgeProvider implements the kubernetes-mcp-server Provider
// interface for routing across multiple edges (used by KubernetesMCP resources).
type MultiEdgeKedgeEdgeProvider struct {
	cluster         string       // kcp cluster path, e.g. "root:kedge:user-default"
	edgeNames       []string     // explicit list of candidate edge names
	edgeConnManager *ConnManager // shared dialer registry
	hubBase         string       // e.g. "https://kedge.example.com" (no trailing slash, no services path)
	bearerToken     string       // caller's bearer token to forward to edge proxies
}

// Ensure MultiEdgeKedgeEdgeProvider implements mcpkubernetes.Provider.
var _ mcpkubernetes.Provider = (*MultiEdgeKedgeEdgeProvider)(nil)

// IsOpenShift always returns false.
func (p *MultiEdgeKedgeEdgeProvider) IsOpenShift(_ context.Context) bool { return false }

// GetTargets returns only those edges in edgeNames that have an active tunnel.
func (p *MultiEdgeKedgeEdgeProvider) GetTargets(_ context.Context) ([]string, error) {
	var targets []string
	for _, name := range p.edgeNames {
		key := edgeConnKey(p.cluster, name)
		if _, ok := p.edgeConnManager.Load(key); ok {
			targets = append(targets, name)
		}
	}
	return targets, nil
}

// GetDerivedKubernetes returns a Kubernetes client for the given edge via the edges-proxy.
func (p *MultiEdgeKedgeEdgeProvider) GetDerivedKubernetes(ctx context.Context, edgeName string) (*mcpkubernetes.Kubernetes, error) {
	// Guard: if the caller passes an empty edge name, fall back to the default.
	if edgeName == "" {
		edgeName = p.GetDefaultTarget()
	}
	if edgeName == "" {
		return nil, fmt.Errorf("no edge name specified and no connected edges available")
	}
	// Delegate to a throwaway single-edge provider.
	single := &KedgeEdgeProvider{
		cluster:         p.cluster,
		edgeName:        edgeName,
		edgeConnManager: p.edgeConnManager,
		hubBase:         p.hubBase,
		bearerToken:     p.bearerToken,
	}
	return single.GetDerivedKubernetes(ctx, edgeName)
}

// GetDefaultTarget returns the first edge name as the default target.
// This ensures tool calls without an explicit "cluster" parameter still
// route to a valid edge rather than producing an empty edge name in the URL.
func (p *MultiEdgeKedgeEdgeProvider) GetDefaultTarget() string {
	if len(p.edgeNames) > 0 {
		return p.edgeNames[0]
	}
	return ""
}

// GetTargetParameterName returns "cluster".
func (p *MultiEdgeKedgeEdgeProvider) GetTargetParameterName() string { return "cluster" }

// WatchTargets is a no-op.
func (p *MultiEdgeKedgeEdgeProvider) WatchTargets(_ mcpkubernetes.McpReload) {}

// Close is a no-op.
func (p *MultiEdgeKedgeEdgeProvider) Close() {}

// ─── minimalBaseConfig ───────────────────────────────────────────────────────
//
// minimalBaseConfig is the smallest possible implementation of mcpapi.BaseConfig
// needed to construct a mcpkubernetes.Kubernetes instance.  All feature flags
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
