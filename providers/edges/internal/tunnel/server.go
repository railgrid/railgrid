/*
Copyright 2026 The Railgrid Authors.

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
	"net/url"

	"github.com/gorilla/websocket"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"

	"github.com/railgrid/provider-edges/internal/kcpurl"
	utilhttp "github.com/railgrid/provider-edges/internal/wsutil"
	"github.com/railgrid/provider-sdk/identityclient"
	"github.com/railgrid/provider-sdk/revdial"
)

// KindConfig declares one connectable kind the tunnel serves. All kinds a
// Server serves MUST share a group + version (they live in one APIExport); they
// differ only by resource/kind (e.g. kubernetesclusters/KubernetesCluster,
// linuxservers/LinuxServer, and macosservers/MacOSServer under edges.railgrid.ai).
type KindConfig struct {
	// GVR is the connectable kind's GroupVersionResource.
	GVR schema.GroupVersionResource
	// Kind is the Go/CRD Kind, for logging/owner context.
	Kind string
}

// authorizeFnType is the signature for the delegated authorization function.
// Factored out as a type to allow injection in tests. The default is the
// package-level authorize (auth.go).
type authorizeFnType func(ctx context.Context, tenantCfg, kcpConfig *rest.Config, token, clusterName, verb, group, resource, subresource, name string) error

// TenantConfigGetter returns a *rest.Config scoped to the given kcp tenant
// logical cluster, able to read/write the Edge resources (and their
// railgrid-system Secrets) the provider owns in that workspace.
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
// constructs one serving all connectable kinds (KubernetesCluster, LinuxServer,
// and MacOSServer under edges.railgrid.ai): it terminates their agent reverse tunnels
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
	// reads. Cluster-aware when replica routing is enabled (see connman.go).
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

	// staticTokens is a TEST-ONLY set of bearer tokens the agent-ingress handler
	// accepts in place of an agent credential when there is no kcp config to
	// validate one against. It is only populated through
	// Config.AllowStaticTokenBypass, which main.go never sets; consumer-egress
	// authorization (edgeproxy / services) never consults it. Every caller of the
	// data plane goes through authorizeFn (TokenReview + SubjectAccessReview),
	// including hub static-token users, whose identity kcp resolves natively.
	staticTokens map[string]struct{}

	// allowStaticTokenBypass mirrors Config.AllowStaticTokenBypass. It is the
	// ONLY thing that lets the consumer-egress data plane serve without a kcp
	// credential: with it unset and kcpConfig nil the edgeproxy / service
	// handlers refuse every request (503) rather than serving unauthorized
	// bearers. main.go never sets it, so production always fails closed.
	allowStaticTokenBypass bool

	// allowUnverifiedSSHHostKey is the provider-wide legacy escape hatch that
	// lets an SSH session to an edge with no known host key proceed
	// unverified. Logged at V(0) on startup and on every use.
	allowUnverifiedSSHHostKey bool

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

	// authorizeFn performs delegated authn/authz against kcp for the AGENT
	// ingress class (f), where the credential is an edge ServiceAccount the
	// provider must TokenReview itself because the request never passes
	// through kcp. Injectable for tests.
	authorizeFn authorizeFnType

	// identities is the hub scoped-identity client this provider mints edge
	// agent credentials through. Nil means the join path hands out no
	// credential and the agent-token verb refuses: a provider that cannot ask
	// the hub does NOT fall back to minting one itself, which is the whole
	// point of review finding M7.
	identities *identityclient.Client

	// hubCAData is the hub's serving CA (PEM), handed to an agent in its
	// enrolment bundle so it can verify the hub it was told to call.
	hubCAData []byte

	// tickets holds the short-lived, single-object WebSocket tickets the
	// browser terminal presents as a Sec-WebSocket-Protocol subprotocol,
	// because a browser cannot set Authorization on an upgrade. See ticket.go.
	tickets *ticketStore

	// gateFn runs the consumer data plane's two gates (class (a)), as the
	// caller and with the caller's credential only. Nil means gateAsCaller,
	// which is the only implementation outside tests.
	gateFn gateFnType

	// registry/replicaID/relayToken enable multi-replica tunnel routing (see
	// EnableReplicaRouting). All nil/empty in single-replica mode.
	registry   *Registry
	replicaID  string
	relayToken string

	logger klog.Logger
}

// EnableRegistry wires the tunnel Lease registry without peer relay: tunnels
// terminated here are claimed and renewed as Leases in the provider workspace
// (the edge lifecycle reconciler's only liveness input), pickup paths stay
// un-addressed and a tunnel held by another replica reports as absent. This
// is the single-replica / no-POD_IP mode. Call once before serving.
func (p *Server) EnableRegistry(reg *Registry) {
	p.registry = reg
	p.edgeConnManager.SetRegistry(reg, "")
}

// EnableReplicaRouting turns on multi-replica tunnel routing on top of the
// registry: new dialers advertise a replica-addressed pickup path, the
// ConnManager resolves peer-held tunnels through the registry, and the
// internal listener (see InternalHandler) serves the relay + forwarded
// pickups. relayToken is the shared provider bearer peers authenticate relays
// with. Call once before serving.
func (p *Server) EnableReplicaRouting(reg *Registry, relayToken string) {
	p.registry = reg
	p.replicaID = reg.ReplicaID()
	p.relayToken = relayToken
	p.edgeConnManager.SetRegistry(reg, relayToken)
}

// InternalHandler serves the pod-to-pod surface on the internal listener
// (never mounted on the public Service): the tunnel relay and forwarded
// revdial pickups.
func (p *Server) InternalHandler() http.Handler {
	upgrader := websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool {
			return utilhttp.CheckSameOrAllowedOrigin(r, []url.URL{})
		},
	}
	mux := http.NewServeMux()
	mux.HandleFunc(relayPath, p.relayHandler())
	// Forwarded pickups land here; the revdial dialer id in the query is the
	// capability (same model as the public pickup path).
	mux.Handle("/agent-pickup", revdial.ConnHandler(upgrader))
	return mux
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
	// StaticTokens are TEST-ONLY bearer tokens accepted as an agent credential
	// on the agent-ingress path when KCPConfig is nil. They are rejected unless
	// AllowStaticTokenBypass is set, and never combine with a KCPConfig: with a
	// kcp credential present every token is validated by kcp. main.go never sets
	// either field.
	StaticTokens           []string
	AllowStaticTokenBypass bool
	HubExternalURL         string
	HubInternalURL         string
	// HubCAData is the hub's serving CA bundle (PEM), travelling to agents in
	// the enrolment bundle. Empty means "trust the system pool".
	HubCAData []byte
	// AllowUnverifiedSSHHostKey restores the legacy behaviour of opening SSH
	// sessions to edges with no known host key without verifying the server
	// (--allow-unverified-ssh-host-key). Never affects an edge whose key is
	// known or pinned; those are always enforced.
	AllowUnverifiedSSHHostKey bool
	Logger                    klog.Logger
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
	if len(cfg.StaticTokens) > 0 && !cfg.AllowStaticTokenBypass {
		return nil, fmt.Errorf("tunnel: the StaticTokens field is test-only and requires AllowStaticTokenBypass")
	}
	if cfg.AllowStaticTokenBypass && cfg.KCPConfig != nil {
		return nil, fmt.Errorf("tunnel: the AllowStaticTokenBypass field is only valid without a KCPConfig (kcp validates every token)")
	}
	tokenSet := make(map[string]struct{}, len(cfg.StaticTokens))
	if cfg.AllowStaticTokenBypass {
		for _, t := range cfg.StaticTokens {
			tokenSet[t] = struct{}{}
		}
	}
	return &Server{
		kinds:                     kinds,
		group:                     group,
		version:                   version,
		edgeConnManager:           NewConnManager(),
		tickets:                   newTicketStore(),
		kcpConfig:                 cfg.KCPConfig,
		staticTokens:              tokenSet,
		allowStaticTokenBypass:    cfg.AllowStaticTokenBypass,
		hubExternalURL:            cfg.HubExternalURL,
		hubInternalURL:            cfg.HubInternalURL,
		hubCAData:                 cfg.HubCAData,
		agentPickupPath:           cfg.AgentPickupPath,
		edgeProxyPublicPath:       cfg.EdgeProxyPublicPath,
		allowUnverifiedSSHHostKey: cfg.AllowUnverifiedSSHHostKey,
		authorizeFn:               authorize,
		logger:                    cfg.Logger.WithName("edge-tunnel"),
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

// denyIfAuthorizationUnavailable fails the consumer-egress data plane CLOSED
// when there is no kcp credential to run the delegated TokenReview +
// SubjectAccessReview against.
//
// Without it the edgeproxy / service handlers wrapped their authorization in
// `if p.kcpConfig != nil`, so a provider that started without a usable kcp
// kubeconfig — including one whose RAILGRID_PROVIDER_KUBECONFIG is set but
// unreadable, which loadKCPConfig silently degrades to nil — served the data
// plane to any non-empty bearer. The handlers are mounted unconditionally, so
// "no kcp config" must mean "refuse traffic", not "skip the check".
//
// The single exception is the test-only AllowStaticTokenBypass, which main.go
// never sets: unit tests exercise the tunnel plane with no kcp at all.
//
// Returns true when the request was answered and the caller must stop.
func (p *Server) denyIfAuthorizationUnavailable(w http.ResponseWriter, r *http.Request) bool {
	if p.kcpConfig != nil || p.allowStaticTokenBypass {
		return false
	}
	// V(0): an operator needs to see this without raising verbosity — the data
	// plane is up but rejecting everything.
	p.logger.Info("refusing data-plane request: kcp delegated authorization is unavailable (no kcp credential)",
		"method", r.Method, "path", r.URL.Path)
	http.Error(w, "authorization unavailable", http.StatusServiceUnavailable)
	return true
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
func (p *Server) Start(stop <-chan struct{}) {
	p.edgeConnManager.StartSweeper(stop)
}

// ConnManager exposes the shared tunnel registry so the provider's edge
// controllers can check whether a given edge tunnel is live.
func (p *Server) ConnManager() *ConnManager { return p.edgeConnManager }

// AgentIngressHandler terminates agent reverse tunnels: Pillar 2 class (f).
// Mounted (behind the hub backend proxy) at /services/providers/edges/agent/,
// with the path UNMODIFIED — the grammar, not an http.ServeMux, decides what
// ".." and "//" mean. It serves:
//
//	/agent/clusters/{cluster}/{resource}/{name}/proxy   control tunnel
//	/agent/proxy                                        revdial pickup
//	/agent/proxy/{replica}                              replica-addressed pickup
func (p *Server) AgentIngressHandler() http.Handler {
	return p.buildEdgeAgentProxyHandler()
}

// EdgeProxyHandler serves the consumer data plane: Pillar 2 class (a).
// Mounted (behind the hub backend proxy) at
// /services/providers/edges/dataplane/, with the path UNMODIFIED:
//
//	/dataplane/clusters/{cluster}/{resource}/{name}/{verb}[/{tail}]
func (p *Server) EdgeProxyHandler() http.Handler {
	return p.buildEdgesProxyHandler()
}

// ProviderMCPHandler serves the provider's AGGREGATE MCP endpoint. Mounted
// (behind the hub backend proxy) at /services/providers/edges/mcp — the URL the
// hub's MCP aggregate federates. Exposes kube tools across every connected
// KubernetesCluster edge in the caller's tenant.
func (p *Server) ProviderMCPHandler() http.Handler {
	return p.buildProviderMCPHandler()
}
