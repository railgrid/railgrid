package builder

import (
	"net/http"

	"github.com/faroshq/faros-kedge/pkg/util/connman"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"
)

const (
	// EdgeProxyVirtualWorkspaceName is the name of the edge proxy virtual workspace
	// where agents register their tunnel connections.
	EdgeProxyVirtualWorkspaceName = "edge-proxy"

	// AgentProxyVirtualWorkspaceName is the name of the agent proxy virtual workspace
	// that provides access to agent resources (k8s, ssh, exec).
	AgentProxyVirtualWorkspaceName = "agent-proxy"

	// ClusterProxyVirtualWorkspaceName is the name of the cluster proxy virtual workspace
	// that routes requests to appropriate workspaces.
	ClusterProxyVirtualWorkspaceName = "cluster-proxy"
)

// NamedVirtualWorkspace represents a virtual workspace with a name and HTTP handler.
type NamedVirtualWorkspace struct {
	Name    string
	Handler http.Handler
}

// VirtualWorkspaceConfig holds configuration for building virtual workspaces.
type VirtualWorkspaceConfig struct {
	RootPathPrefix string
	ConnManager    *connman.ConnectionManager
	KCPConfig      *rest.Config // KCP rest config for token verification (nil if KCP not configured)
}

// BuildVirtualWorkspaces creates the virtual workspaces for the kedge hub.
func BuildVirtualWorkspaces(config VirtualWorkspaceConfig) []NamedVirtualWorkspace {
	logger := klog.Background().WithName("virtual-workspaces")

	vw := &virtualWorkspaces{
		rootPathPrefix: config.RootPathPrefix,
		connManager:    config.ConnManager,
		kcpConfig:      config.KCPConfig,
		logger:         logger,
	}

	return []NamedVirtualWorkspace{
		{
			Name:    EdgeProxyVirtualWorkspaceName,
			Handler: vw.buildEdgeProxyHandler(),
		},
		{
			Name:    AgentProxyVirtualWorkspaceName,
			Handler: vw.buildAgentProxyHandler(),
		},
		{
			Name:    ClusterProxyVirtualWorkspaceName,
			Handler: vw.buildClusterProxyHandler(),
		},
	}
}
