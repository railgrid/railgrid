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

// Package builder constructs virtual workspace HTTP handlers for the kedge hub.
package builder

import (
	"context"
	"net/http"

	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"

	kedgeclient "github.com/faroshq/faros-kedge/pkg/client"
	"github.com/faroshq/faros-kedge/pkg/util/connman"
)

// authorizeFnType is the signature for the delegated authorization function.
// Factored out as a type to allow injection in tests.
type authorizeFnType func(ctx context.Context, kcpConfig *rest.Config, token, clusterName, verb, resource, name string) error

// virtualWorkspaces holds state and dependencies for all virtual workspaces.
type virtualWorkspaces struct {
	connManager     *connman.ConnectionManager
	edgeConnManager *ConnManager         // shared between agent-proxy-v2 and edges-proxy builders
	kcpConfig       *rest.Config         // kcp rest config for token verification and edge status updates
	kcpK8sClient    kubernetes.Interface // kubernetes client for fetching secrets
	kedgeClient     *kedgeclient.Client  // kedge client for fetching Edge resources
	staticTokens    map[string]struct{}  // static tokens that bypass JWT SA requirement
	hubExternalURL  string               // external URL of the hub (used for kubeconfig generation)
	hubInternalURL  string               // internal URL of the hub (used for MCP→edges-proxy calls to avoid CDN loops)
	// authorizeFn performs delegated authentication and authorization against kcp.
	// Defaults to the package-level authorize function; injectable for testing.
	authorizeFn authorizeFnType
	logger      klog.Logger
}

// VirtualWorkspaceHandlers provides access to the HTTP handlers for tunneling.
type VirtualWorkspaceHandlers struct {
	vws *virtualWorkspaces
}

// NewVirtualWorkspaces creates a new VirtualWorkspaceHandlers.
// kcpConfig is required for SA token authorization against kcp and for fetching Edge resources/secrets.
// hubExternalURL is the externally reachable URL of the hub, used when building kubeconfigs for agents.
// hubInternalURL is the internal URL for MCP→edges-proxy calls to avoid CDN/proxy loops.
// If empty, hubExternalURL is used as fallback.
func NewVirtualWorkspaces(cm *connman.ConnectionManager, kcpConfig *rest.Config, staticTokens []string, hubExternalURL, hubInternalURL string, logger klog.Logger) (*VirtualWorkspaceHandlers, error) {
	staticTokenSet := make(map[string]struct{}, len(staticTokens))
	for _, t := range staticTokens {
		staticTokenSet[t] = struct{}{}
	}

	var kcpK8sClient kubernetes.Interface
	var kedgeClient *kedgeclient.Client

	if kcpConfig != nil {
		var err error
		kcpK8sClient, err = kubernetes.NewForConfig(kcpConfig)
		if err != nil {
			return nil, err
		}

		dynClient, err := dynamic.NewForConfig(kcpConfig)
		if err != nil {
			return nil, err
		}
		kedgeClient = kedgeclient.NewFromDynamic(dynClient)
	}

	return &VirtualWorkspaceHandlers{
		vws: &virtualWorkspaces{
			connManager:     cm,
			edgeConnManager: NewConnManager(),
			kcpConfig:       kcpConfig,
			kcpK8sClient:    kcpK8sClient,
			kedgeClient:     kedgeClient,
			staticTokens:    staticTokenSet,
			hubExternalURL:  hubExternalURL,
			hubInternalURL:  hubInternalURL,
			authorizeFn:     authorize,
			logger:          logger.WithName("virtual-workspaces"),
		},
	}, nil
}

// EdgeAgentProxyHandler returns the handler for Edge agent tunnel registration.
// Agents connect to register their revdial tunnel for an Edge resource.
// Mount at /services/agent-proxy/.
func (h *VirtualWorkspaceHandlers) EdgeAgentProxyHandler() http.Handler {
	return h.vws.buildEdgeAgentProxyHandler()
}

// EdgesProxyHandler returns the handler for user-facing access to Edge resources.
// Supports the k8s (kubernetes API proxy) and ssh (WebSocket terminal) subresources.
// Mount at /services/edges-proxy/.
func (h *VirtualWorkspaceHandlers) EdgesProxyHandler() http.Handler {
	return h.vws.buildEdgesProxyHandler()
}

// EdgeConnManager returns the shared edge connection manager.
// Exposed so that hub controllers can check whether a given edge tunnel is active.
func (h *VirtualWorkspaceHandlers) EdgeConnManager() *ConnManager {
	return h.vws.edgeConnManager
}
