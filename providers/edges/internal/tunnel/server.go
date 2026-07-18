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
	"net/http"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"

	"github.com/faroshq/provider-edges/internal/kcpurl"
)

// KindConfig declares one connectable kind the tunnel serves. All kinds a
// Server serves MUST share a group + version (they live in one APIExport); they
// differ only by resource/kind (e.g. kubernetesclusters/KubernetesCluster and
// linuxservers/LinuxServer under edges.kedge.faros.sh).
type KindConfig struct {
	// GVR is the connectable kind's GroupVersionResource.
	GVR schema.GroupVersionResource
	// Kind is the Go/CRD Kind, for logging/owner context.
	Kind string
}

// authorizeFnType is the signature for the delegated authorization function.
// Factored out as a type to allow injection in tests. The default is the
// package-level authorize (auth.go).
type authorizeFnType func(ctx context.Context, kcpConfig *rest.Config, token, clusterName, verb, group, resource, name string) error

// TenantConfigGetter returns a *rest.Config scoped to the given kcp tenant
// logical cluster, able to read/write the Edge resources (and their
// kedge-system Secrets) the provider owns in that workspace.
//
// It exists because the provider's own SA credential (p.kcpConfig) is
// workspace-scoped: re-rooting it to /clusters/<tenant> is rejected by kcp
// ("the server could not find the requested resource"), which broke agent
// join-token registration in production. The provider's ONLY cross-workspace
// credential is its APIExport virtual workspace — the same one the edge
// controller manager engages each tenant cluster through. This getter is wired
// from that manager (mcmanager.GetCluster(...).GetConfig()); when unset
// (dev/tests with an admin/front-proxy kcpConfig) callers fall back to
// re-rooting kcpConfig directly, which works only because that credential is
// not workspace-scoped.
type TenantConfigGetter func(ctx context.Context, cluster string) (*rest.Config, error)

// Server is the SDK's generic tunnel plane. The single `edges` provider
// constructs one serving BOTH connectable kinds (KubernetesCluster + LinuxServer
// under edges.kedge.faros.sh): it terminates their agent reverse tunnels
// (revdial + one in-process ConnManager, keyed by resource/cluster/name) and
// serves the k8s / ssh data-plane subresources. Requests are dispatched to the
// right kind by the resource segment in the URL path.
//
// The handler methods (buildEdgeAgentProxyHandler, buildEdgesProxyHandler, and
// their helpers) hang off this struct with receiver name p.
type Server struct {
	// kinds maps the URL resource segment (e.g. "kubernetesclusters") to its
	// GVR + Kind. group/version are shared across all kinds (validated in New).
	kinds   map[string]KindConfig
	group   string
	version string

	// edgeConnManager is the tunnel registry: agent-ingress writes, edgeproxy
	// reads. Single-replica invariant applies (see connman.go).
	edgeConnManager *ConnManager

	// kcpConfig is the provider's kcp credential. Used for delegated agent-token
	// authorization (TokenReview/SAR via a tenant-workspace RBAC grant) and, as a
	// fallback when tenantConfig is unset, for direct tenant reads/writes.
	kcpConfig *rest.Config

	// tenantConfig, when set, yields a cross-workspace-capable *rest.Config for a
	// tenant logical cluster (the provider's APIExport virtual workspace). Wired
	// from the edge controller manager. When nil, tenantConfigFor falls back to
	// re-rooting kcpConfig (dev/tests with an admin credential). See
	// TenantConfigGetter.
	tenantConfig TenantConfigGetter

	// staticTokens bypass the SA/join-token requirement (dev / static-auth hubs).
	staticTokens map[string]struct{}

	// hubExternalURL is embedded into agent kubeconfigs. hubInternalURL is used
	// for internal MCP→edgeproxy calls to avoid CDN loops; falls back to
	// hubExternalURL when empty.
	hubExternalURL string
	hubInternalURL string

	// agentPickupPath is the PUBLIC path (behind the hub backend proxy) the
	// agent re-enters through for revdial pickup connections, e.g.
	// /services/providers/edges/agent/proxy.
	agentPickupPath string

	// edgeProxyPublicPath is the PUBLIC consumer-egress base (behind the hub
	// backend proxy) for the k8s/ssh subresources, e.g.
	// /services/providers/edges/edgeproxy. It is stamped into an edge's
	// status.URL (see edgeProxyStatusURL) so CLI clients can reach the edge
	// through the hub. Empty disables URL stamping.
	edgeProxyPublicPath string

	// authorizeFn performs delegated authn/authz against kcp; injectable for tests.
	authorizeFn authorizeFnType

	logger klog.Logger
}

// Config carries the inputs for New. Kinds is required (>=1, all sharing a
// group+version); everything else is optional (nil KCPConfig is allowed for
// tests that only exercise the ConnManager).
type Config struct {
	// Kinds are the connectable kinds this tunnel serves. At least one; all must
	// share the same group + version (one APIExport).
	Kinds []KindConfig
	// AgentPickupPath is the public revdial pickup path the agent re-enters
	// through, e.g. /services/providers/edges/agent/proxy (required).
	AgentPickupPath string
	// EdgeProxyPublicPath is the public consumer-egress base stamped into an
	// edge's status.URL, e.g. /services/providers/edges/edgeproxy. Empty
	// disables status.URL stamping (the CLI kubeconfig/ssh commands then have
	// no URL to externalize).
	EdgeProxyPublicPath string
	KCPConfig           *rest.Config
	StaticTokens        []string
	HubExternalURL      string
	HubInternalURL      string
	Logger              klog.Logger
}

// New constructs the tunnel Server for one or more connectable kinds.
func New(cfg Config) (*Server, error) {
	if len(cfg.Kinds) == 0 {
		return nil, fmt.Errorf("tunnel: at least one Kind is required")
	}
	kinds := make(map[string]KindConfig, len(cfg.Kinds))
	var group, version string
	for _, k := range cfg.Kinds {
		if group == "" {
			group, version = k.GVR.Group, k.GVR.Version
		} else if k.GVR.Group != group || k.GVR.Version != version {
			return nil, fmt.Errorf("tunnel: all kinds must share group/version; got %s and %s/%s",
				k.GVR.GroupVersion().String(), group, version)
		}
		kinds[k.GVR.Resource] = k
	}
	tokenSet := make(map[string]struct{}, len(cfg.StaticTokens))
	for _, t := range cfg.StaticTokens {
		tokenSet[t] = struct{}{}
	}
	return &Server{
		kinds:               kinds,
		group:               group,
		version:             version,
		edgeConnManager:     NewConnManager(),
		kcpConfig:           cfg.KCPConfig,
		staticTokens:        tokenSet,
		hubExternalURL:      cfg.HubExternalURL,
		hubInternalURL:      cfg.HubInternalURL,
		agentPickupPath:     cfg.AgentPickupPath,
		edgeProxyPublicPath: cfg.EdgeProxyPublicPath,
		authorizeFn:         authorize,
		logger:              cfg.Logger.WithName("edge-tunnel"),
	}, nil
}

// SetTenantConfigGetter wires the cross-workspace tenant config source (the
// provider's APIExport virtual workspace, owned by the edge controller
// manager). Call once during startup, before the tunnel handlers begin serving
// agent requests. When never set, tenant reads/writes fall back to re-rooting
// kcpConfig (see TenantConfigGetter).
func (p *Server) SetTenantConfigGetter(fn TenantConfigGetter) { p.tenantConfig = fn }

// tenantConfigFor returns a *rest.Config able to read/write the given tenant
// logical cluster. It prefers the APIExport virtual-workspace getter and falls
// back to re-rooting the provider's kcpConfig at /clusters/<cluster> (which
// only works when kcpConfig is a non-workspace-scoped admin credential).
func (p *Server) tenantConfigFor(ctx context.Context, cluster string) (*rest.Config, error) {
	if p.tenantConfig != nil {
		return p.tenantConfig(ctx, cluster)
	}
	if p.kcpConfig == nil {
		return nil, fmt.Errorf("no kcp config available")
	}
	cfg := rest.CopyConfig(p.kcpConfig)
	cfg.Host = kcpurl.ClusterURL(cfg.Host, cluster)
	return cfg, nil
}

// gvrForResource resolves a URL resource segment to its GVR + Kind. ok is false
// when the resource is not one of the kinds this Server serves.
func (p *Server) gvrForResource(resource string) (gvr schema.GroupVersionResource, kind string, ok bool) {
	k, exists := p.kinds[resource]
	if !exists {
		return schema.GroupVersionResource{}, "", false
	}
	return k.GVR, k.Kind, true
}

// Start launches background maintenance (the stale-tunnel sweeper). Call once;
// the goroutine exits when stop is closed.
func (s *Server) Start(stop <-chan struct{}) {
	s.edgeConnManager.StartSweeper(stop)
}

// ConnManager exposes the shared tunnel registry so the provider's edge
// controllers can check whether a given edge tunnel is live.
func (s *Server) ConnManager() *ConnManager { return s.edgeConnManager }

// AgentIngressHandler terminates agent reverse tunnels. Mounted (behind the hub
// backend proxy) at /services/providers/edges/agent/. Path after
// StripPrefix: /{cluster}/apis/edges.kedge.faros.sh/v1alpha1/{kubernetesclusters|linuxservers}/{name}/proxy
// and /proxy (revdial pickup).
func (s *Server) AgentIngressHandler() http.Handler {
	return s.buildEdgeAgentProxyHandler()
}

// EdgeProxyHandler serves the consumer data-plane subresources. Mounted (behind
// the hub backend proxy) at /services/providers/edges/edgeproxy/.
// Path after StripPrefix: /clusters/{cluster}/apis/edges.kedge.faros.sh/v1alpha1/{kubernetesclusters|linuxservers}/{name}/{k8s|ssh}.
func (s *Server) EdgeProxyHandler() http.Handler {
	return s.buildEdgesProxyHandler()
}

// ProviderMCPHandler serves the provider's AGGREGATE MCP endpoint. Mounted
// (behind the hub backend proxy) at /services/providers/edges/mcp — the URL the
// hub's MCP aggregate federates. Exposes kube tools across every connected
// KubernetesCluster edge in the caller's tenant.
func (s *Server) ProviderMCPHandler() http.Handler {
	return s.buildProviderMCPHandler()
}
