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

package hub

import (
	"time"

	"github.com/railgrid/railgrid/pkg/hub/controllers/softdelete"
	"github.com/railgrid/railgrid/pkg/hub/kcp/byolimits"
	"github.com/railgrid/railgrid/pkg/hub/providers"
	"github.com/railgrid/railgrid/pkg/hub/quota"
)

// Options holds configuration for the hub server.
type Options struct {
	DataDir               string
	ListenAddr            string
	Kubeconfig            string
	ExternalKCPKubeconfig string
	IDPIssuerURL          string
	// IDPBrowserAuthURL optionally overrides only the OIDC authorization
	// endpoint placed in browser redirects. Discovery, token exchange, JWKS,
	// and issuer validation continue to use IDPIssuerURL.
	IDPBrowserAuthURL string
	IDPClientID       string
	// DisableTokenLogin removes the interactive static-token login: the
	// /auth/token-login endpoint is not registered and /healthz advertises
	// tokenLogin=false so the portal hides its token form. Static bearer
	// tokens keep authenticating API/MCP/CLI calls — only the browser login
	// path is disabled. Use with OIDC so humans sign in with a username and
	// password at the IdP.
	DisableTokenLogin bool
	// PublishedAppsDomain is deprecated and ignored. The published-app
	// authorize/exchange endpoints are always mounted, and sign-in redirects
	// are pinned per request to the host stamped on the instance being
	// authorized — so apps under BYO provider zones and customer-owned
	// domains need no hub-side domain configuration. The flag is kept only
	// so existing deployments that pass it keep starting.
	PublishedAppsDomain string
	// IDPCAFile is a path to a PEM-encoded CA bundle used to verify the IdP's
	// TLS certificate. Required when IDPIssuerURL is https and uses a cert
	// not signed by a system trust anchor (e.g. the dev Dex deployment).
	IDPCAFile       string
	ServingCertFile string
	ServingKeyFile  string
	// ProviderRelay is a separate mTLS listener for external kcp shards. The
	// browser-facing server never trusts shard identity headers.
	ProviderRelayURL          string
	ProviderRelayListenAddr   string
	ProviderRelayCertFile     string
	ProviderRelayKeyFile      string
	ProviderRelayClientCAFile string
	ProviderRelayAllowedNames []string
	HubExternalURL            string
	// HubInternalURL, when set, is the address in-cluster components use to
	// reach this hub instead of HubExternalURL. It is baked into minted provider
	// kubeconfigs and offered as the "internal" option by the admin kubeconfig
	// download.
	//
	// Without it, a provider pod's calls to the hub resolve the public hostname,
	// leave the cluster, and re-enter through whatever fronts it (CDN, tunnel,
	// load balancer) — for a provider running beside the hub that is a pointless
	// round trip that also makes the whole platform look like one noisy client
	// IP to the edge.
	//
	// Typically the hub's in-cluster Service
	// (https://<release>-railgrid-hub.<namespace>.svc.cluster.local:9443). Also
	// useful when provider pods reach the hub at a different address than
	// browsers do — e.g. a kind pod dialing https://host.docker.internal:9443
	// while browsers use https://localhost:9443. Leave empty when providers run
	// outside the cluster.
	HubInternalURL   string
	DevMode          bool
	StaticAuthTokens []string

	// TrustedProxyCIDRs lists the address ranges of the reverse proxies that
	// front this hub (ingress controller, load balancer, CDN egress). The
	// pre-authentication rate limiters (token-login, /auth/refresh, the
	// published-app code exchange, the aggregate MCP bearer check) key their
	// buckets on the client address, and a proxy's X-Forwarded-For is only
	// believed when the connection peer is inside one of these ranges — see
	// proxy.ClientIP for the exact rule.
	//
	// Empty (the default) means proxy headers are ignored and every request is
	// keyed on its connection peer. A hub deployed behind a proxy without this
	// set therefore sees all of its clients as the proxy's own address and
	// throttles them as one; that is the safe failure, so set it. Never widen
	// it to ranges clients can connect from: any peer inside it can pick its
	// own bucket by sending X-Forwarded-For.
	TrustedProxyCIDRs []string

	// ProviderDelegatedTokens selects which providers receive a short-lived,
	// workspace-scoped ServiceAccount token in place of the caller's own hub
	// bearer on the backend proxy (/services/providers/{name}/*). Org-owned
	// providers always receive the delegated token regardless of this
	// setting; it governs platform providers only:
	//
	//   off      — platform providers receive the caller's bearer as-is
	//              (the historical behaviour).
	//   platform — platform providers receive the delegated token, except
	//              the names in ProviderDelegatedTokensExclude.
	//   all      — every platform provider receives the delegated token;
	//              the exclusion list is ignored.
	//
	// Default "off" for this release. The next release defaults to
	// "platform"; deployments should run "platform" ahead of that to
	// confirm their providers need nothing beyond /clusters/{id} access.
	ProviderDelegatedTokens string
	// ProviderDelegatedTokensExclude names platform providers that keep
	// receiving the caller's bearer under ProviderDelegatedTokens=platform.
	// Defaults to providers.DefaultDelegationExclude.
	ProviderDelegatedTokensExclude []string

	// ProviderHubAccessPlatformDefault lets a platform provider use the hub
	// capabilities it declares (CatalogEntry.spec.hub.access) in a workspace
	// where no tenant decision was recorded yet. Platform providers are
	// operator-installed and, under ProviderDelegatedTokens=off, hold the
	// caller's own bearer anyway; this keeps them working across the upgrade
	// to the hub-access contract. An explicit decision (Enable with or
	// without accepting) always wins, and org-owned providers always need
	// one. Set false to require acceptance for every provider.
	ProviderHubAccessPlatformDefault bool

	// AdminUsers is the allowlist of platform-admin identities permitted to
	// reach the /api/admin/* surface and the portal's /bonkers area. Each entry
	// matches a User CR by name, email, or rbacIdentity (case-insensitive).
	// Empty disables the admin surface entirely.
	AdminUsers []string

	// Providers is the list of first-party builtin providers to materialize
	// into root:railgrid:providers at bootstrap. The flag accepts a comma-
	// separated list or repeats; see cmd/railgrid-hub/main.go for the default.
	// Empty/nil enables every known builtin (kcp.BuiltinProviderNames()).
	// Dependencies between builtins are validated at hub startup — see
	// pkg/hub/kcp.builtinEntries[].Requires.
	Providers []string

	// DefaultOrgsPerUser is how many Organizations a User may create
	// (docs/organizations.md O-5); User.spec.quota.organizations overrides
	// it per user. Personal organizations do not count. Must be positive.
	DefaultOrgsPerUser int32
	// FreeOrgsPerUser is how many non-personal Organizations on the default
	// (free) metering plan a User may administer at once (O-5); creating
	// another needs a plan on one of them. User.spec.quota.freeOrganizations
	// overrides it per user. Applies only while a metering deployment is
	// discovered in MeteringWorkspace: without metering there are no
	// allowances to multiply. Zero forbids free organizations beyond the
	// personal one; must not be negative.
	FreeOrgsPerUser int32

	// SoftDeleteGracePeriod is how long a soft-deleted User, Organization
	// or Workspace stays recoverable before the softdelete reconciler
	// cascades it away. One value for every kind (docs/organizations.md
	// O-8 / O-13); the REST undelete handlers refuse a restore once it
	// has elapsed. Zero or negative is rejected at startup. Defaults to
	// softdelete.DefaultGracePeriod (30 days).
	SoftDeleteGracePeriod time.Duration

	// ProviderHeartbeatAuth is what the hub does with a provider heartbeat
	// whose bearer token does not verify as that provider's own service
	// account: "warn" logs and records the beat, "enforce" rejects it. The
	// default is "warn" for this release so providers installed from charts
	// that predate the authenticated heartbeat keep reporting alive while
	// they are rolled forward; the next release flips the default to
	// "enforce". See providers.HeartbeatAuthMode.
	ProviderHeartbeatAuth string
	// ProviderWorkspaceClusterAdmin selects the role the provider ServiceAccount
	// is bound to inside its own provider workspace: true (the default this
	// release) keeps cluster-admin, false binds the generated, narrower
	// railgrid:provider ClusterRole.
	//
	// The default is the wide one for one release so an operator can stage the
	// change — flip it, watch their providers, flip back if one of them needed
	// a right the narrow role does not grant (the infrastructure provider, which
	// serves its own CRDs from this workspace, is the known case). The NEXT
	// release defaults this to false. Flipping either way replaces the existing
	// binding: RoleRef is immutable, so the hub deletes and recreates it.
	ProviderWorkspaceClusterAdmin bool

	// PortalDevURL, when set, reverse-proxies /ui/* to this URL (typically
	// a Vite dev server, e.g. http://localhost:3000). Takes precedence over the
	// embedded portal dist (if built with -tags portal_embed).
	PortalDevURL string
	// PortalFrameSources are additional CSP frame-src source expressions allowed
	// by the portal. Keep this narrow; provider UIs are still same-origin through
	// the hub, while platform-owned preview hosts can be added explicitly.
	PortalFrameSources []string

	// Embedded kcp options
	EmbeddedKCP bool // Enable embedded kcp server
	// OrganizationLimits is enforced inside the embedded shard, including its
	// virtual-workspace listener. Zero leaves hosted BYO unavailable; operators
	// must supply a complete policy appropriate to their shared capacity.
	OrganizationLimits byolimits.Config
	// OrganizationConsumerPolicy bounds quota-governed live allocations per org.
	// Its lifecycle-lock capacity always follows OrganizationLimits.MaxOrganizations.
	OrganizationConsumerPolicy providers.OrgConsumerPolicyConfig
	KCPRootDir                 string // Root directory for kcp data (default: <DataDir>/kcp)
	KCPEtcdClientPort          int    // Embedded etcd client port (0 keeps kcp default: 2379)
	KCPEtcdPeerPort            int    // Embedded etcd peer port (0 keeps kcp default: 2380)
	KCPSecurePort              int    // Secure port for kcp API server (default: 6443)
	KCPBindAddress             string // Bind address for kcp API server (default: "127.0.0.1")
	KCPBatteriesInclude        string // Comma-separated list of batteries to include (default: "admin,user")
	KCPTLSCertFile             string // TLS certificate file for kcp API server
	KCPTLSKeyFile              string // TLS key file for kcp API server
	// KCPShardExternalURL is the URL kcp publishes into APIExportEndpointSlice
	// and CachedResourceEndpointSlice statuses for outside consumers to dial.
	// Empty defaults to kcp's auto-detected external address, which for an
	// embedded kcp bound to 127.0.0.1 is "https://127.0.0.1:6443" — fine for
	// host-side clients, broken for clients running in a kind pod (they
	// resolve 127.0.0.1 to the pod itself). Override with e.g.
	// "https://host.docker.internal:6443" for kind-based dev setups.
	KCPShardExternalURL string
	// KCPShardVirtualWorkspaceURL must be set alongside KCPShardExternalURL.
	// The two URLs cover different slots in Shard.spec — externalURL feeds
	// generic outside-clients discovery, virtualWorkspaceURL is what
	// APIExportEndpointSlice / CachedResourceEndpointSlice publish in their
	// status.endpoints[]. For a single-shard embedded dev setup both want
	// the same value.
	KCPShardVirtualWorkspaceURL string
	// KCPAdmissionConfigFile is an apiserver AdmissionConfiguration handed to
	// the embedded kcp shard (kcp's --admission-control-config-file). Its
	// use today is the apis.kcp.io/GlobalValidatingWebhook plugin section: a
	// ValidatingWebhookConfiguration the shard fires in EVERY logical
	// cluster, which is how the metering provider's enforcement webhook is
	// wired without a per-workspace object. Empty leaves admission as kcp
	// ships it. An external kcp takes the same flag on its shards.
	KCPAdmissionConfigFile string
	// KCPAuditPolicyFile and KCPAuditWebhookConfigFile are kcp's
	// --audit-policy-file and --audit-webhook-config-file for the embedded
	// shard: the audit backend (batch mode) that feeds the metering
	// provider's request-cost meter. Both or neither.
	KCPAuditPolicyFile        string
	KCPAuditWebhookConfigFile string

	// MeteringWorkspace is the kcp workspace the contrib-metering deployment
	// owns. The hub discovers metering by reading two objects it publishes
	// there (ConfigMap default/metering-endpoints, Secret
	// default/metering-admin): present means installed, and the admin area
	// then surfaces its data through /api/admin/metering. Empty disables
	// discovery.
	MeteringWorkspace string
	// MeteringDefaultPlan is the metering Plan every Organization's
	// Entitlement is created on. The hub only creates that Entitlement;
	// changing an organization's plan afterwards is an admin (later a
	// billing) action the hub never undoes.
	MeteringDefaultPlan string
}

// NewOptions returns default Options.
func NewOptions() *Options {
	return &Options{
		DataDir:             "/tmp/railgrid-data",
		ListenAddr:          ":9443",
		HubExternalURL:      "https://localhost:9443",
		EmbeddedKCP:         false,
		KCPSecurePort:       6443,
		KCPBindAddress:      "127.0.0.1",
		KCPBatteriesInclude: "admin,user",

		SoftDeleteGracePeriod: softdelete.DefaultGracePeriod,
		DefaultOrgsPerUser:    quota.DefaultOrgsPerUser,
		FreeOrgsPerUser:       quota.DefaultFreeOrgsPerUser,

		ProviderHeartbeatAuth: string(providers.HeartbeatAuthWarn),
		// Wide for this release; the next one defaults to false. See the field.
		ProviderWorkspaceClusterAdmin:  true,
		ProviderDelegatedTokens:        string(providers.DelegationOff),
		ProviderDelegatedTokensExclude: append([]string(nil), providers.DefaultDelegationExclude...),

		ProviderHubAccessPlatformDefault: true,
	}
}
