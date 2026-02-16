package builder

import (
	"net/http"

	"github.com/faroshq/faros-kedge/pkg/util/connman"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"
)

// virtualWorkspaces holds state and dependencies for all virtual workspaces.
type virtualWorkspaces struct {
	rootPathPrefix string
	connManager    *connman.ConnectionManager
	kcpConfig      *rest.Config // KCP rest config for token verification (nil if KCP not configured)
	logger         klog.Logger
}

// VirtualWorkspaceHandlers provides access to the HTTP handlers for tunneling.
type VirtualWorkspaceHandlers struct {
	vws *virtualWorkspaces
}

// NewVirtualWorkspaces creates a new VirtualWorkspaceHandlers.
// kcpConfig is used for SA token verification against KCP. If nil, token
// verification is skipped (dev mode only).
func NewVirtualWorkspaces(cm *connman.ConnectionManager, kcpConfig *rest.Config, logger klog.Logger) *VirtualWorkspaceHandlers {
	return &VirtualWorkspaceHandlers{
		vws: &virtualWorkspaces{
			connManager: cm,
			kcpConfig:   kcpConfig,
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
