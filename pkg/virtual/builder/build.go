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
	KCPConfig      *rest.Config // kcp rest config for token verification (nil if kcp not configured)
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
