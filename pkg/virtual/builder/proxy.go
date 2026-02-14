package builder

import (
	"net/http"

	"github.com/faroshq/faros-kedge/pkg/util/connman"
	"k8s.io/klog/v2"
)

// virtualWorkspaces holds state and dependencies for all virtual workspaces.
type virtualWorkspaces struct {
	rootPathPrefix string
	connManager    *connman.ConnectionManager
	logger         klog.Logger
}

// VirtualWorkspaceHandlers provides access to the HTTP handlers for tunneling.
type VirtualWorkspaceHandlers struct {
	vws *virtualWorkspaces
}

// NewVirtualWorkspaces creates a new VirtualWorkspaceHandlers.
func NewVirtualWorkspaces(cm *connman.ConnectionManager, logger klog.Logger) *VirtualWorkspaceHandlers {
	return &VirtualWorkspaceHandlers{
		vws: &virtualWorkspaces{
			connManager: cm,
			logger:      logger.WithName("virtual-workspaces"),
		},
	}
}

// EdgeProxyHandler returns the HTTP handler for agent tunnel registration.
func (h *VirtualWorkspaceHandlers) EdgeProxyHandler() http.Handler {
	return h.vws.buildEdgeProxyHandler()
}

// AgentProxyHandler returns the HTTP handler for accessing agent resources.
func (h *VirtualWorkspaceHandlers) AgentProxyHandler() http.Handler {
	return h.vws.buildAgentProxyHandler()
}
