package builder

import (
	"net/http"
	"sync"

	"github.com/faroshq/faros-kedge/pkg/util/connman"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"
)

// SiteRouteMap maps URL route keys to connManager tunnel keys.
// Route key format: "{clusterName}:{siteName}" (used in URL path).
// Tunnel key format: "{clusterName}/{siteName}" (used by connManager).
type SiteRouteMap struct {
	routes sync.Map
}

// NewSiteRouteMap creates a new SiteRouteMap.
func NewSiteRouteMap() *SiteRouteMap { return &SiteRouteMap{} }

// Set registers a mapping from route key to tunnel key.
func (m *SiteRouteMap) Set(routeKey, tunnelKey string) {
	m.routes.Store(routeKey, tunnelKey)
}

// Get looks up the tunnel key for a route key.
func (m *SiteRouteMap) Get(routeKey string) (string, bool) {
	v, ok := m.routes.Load(routeKey)
	if !ok {
		return "", false
	}
	return v.(string), true
}

// virtualWorkspaces holds state and dependencies for all virtual workspaces.
type virtualWorkspaces struct {
	rootPathPrefix string
	connManager    *connman.ConnectionManager
	kcpConfig      *rest.Config // KCP rest config for token verification (nil if KCP not configured)
	siteRoutes     *SiteRouteMap
	logger         klog.Logger
}

// VirtualWorkspaceHandlers provides access to the HTTP handlers for tunneling.
type VirtualWorkspaceHandlers struct {
	vws *virtualWorkspaces
}

// NewVirtualWorkspaces creates a new VirtualWorkspaceHandlers.
// kcpConfig is used for SA token verification against KCP. If nil, token
// verification is skipped (dev mode only).
func NewVirtualWorkspaces(cm *connman.ConnectionManager, kcpConfig *rest.Config, siteRoutes *SiteRouteMap, logger klog.Logger) *VirtualWorkspaceHandlers {
	return &VirtualWorkspaceHandlers{
		vws: &virtualWorkspaces{
			connManager: cm,
			kcpConfig:   kcpConfig,
			siteRoutes:  siteRoutes,
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

// SiteProxyHandler returns the handler for proxying kube API to sites via reverse tunnels.
func (h *VirtualWorkspaceHandlers) SiteProxyHandler() http.Handler {
	return h.vws.buildSiteProxyHandler()
}
