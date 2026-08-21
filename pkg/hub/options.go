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

package hub

import (
	"time"

	"github.com/faroshq/faros/pkg/kcppaths"
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
	// PublishedAppsDomain is the DNS zone published template instances are
	// served under (e.g. "apps.faros.example"). When set, the hub mounts the
	// login-time published-app authorize/exchange endpoints and only ever
	// redirects sign-ins back to hosts directly under this zone. Empty
	// disables published-app auth entirely.
	PublishedAppsDomain string
	// IDPCAFile is a path to a PEM-encoded CA bundle used to verify the IdP's
	// TLS certificate. Required when IDPIssuerURL is https and uses a cert
	// not signed by a system trust anchor (e.g. the dev Dex deployment).
	IDPCAFile       string
	ServingCertFile string
	ServingKeyFile  string
	HubExternalURL  string
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
	// (https://<release>-faros-hub.<namespace>.svc.cluster.local:9443). Also
	// useful when provider pods reach the hub at a different address than
	// browsers do — e.g. a kind pod dialing https://host.docker.internal:9443
	// while browsers use https://localhost:9443. Leave empty when providers run
	// outside the cluster.
	HubInternalURL   string
	DevMode          bool
	StaticAuthTokens []string

	// AdminUsers is the allowlist of platform-admin identities permitted to
	// reach the /api/admin/* surface and the portal's /bonkers area. Each entry
	// matches a User CR by name, email, or rbacIdentity (case-insensitive).
	// Empty disables the admin surface entirely.
	AdminUsers []string

	// Providers is the list of first-party builtin providers to materialize
	// into root:faros:providers at bootstrap. The flag accepts a comma-
	// separated list or repeats; see cmd/faros-hub/main.go for the default.
	// Empty/nil enables every known builtin (kcp.BuiltinProviderNames()).
	// Dependencies between builtins are validated at hub startup — see
	// pkg/hub/kcp.builtinEntries[].Requires.
	Providers []string

	// Product telemetry is explicitly opt-in. Off is the self-hosted default
	// and creates neither a worker nor an outbound HTTP client.
	TelemetryMode     string
	TelemetryEndpoint string
	// TelemetryCAFile is an optional PEM-encoded CA bundle appended to the
	// system trust roots for the SaaS telemetry receiver. It is used only for
	// the dedicated telemetry HTTP client and never changes trust for other hub
	// traffic.
	TelemetryCAFile          string
	TelemetrySinkToken       string
	TelemetryHMACSecret      string
	TelemetryInstallationID  string
	TelemetryQueueSize       int
	TelemetryBatchSize       int
	TelemetryFlushInterval   time.Duration
	TelemetryEnqueueTimeout  time.Duration
	TelemetrySendTimeout     time.Duration
	TelemetryShutdownTimeout time.Duration
	TelemetryMaxRequestBytes int64
	TelemetryMaxRetries      int

	// GraphQLAddr is the address of an external GraphQL gateway to proxy /graphql/ requests to.
	// If empty and EmbeddedGraphQL is false, the graphql proxy is disabled.
	GraphQLAddr string

	// EmbeddedGraphQL runs the GraphQL listener+gateway in-process alongside the hub.
	// When true, GraphQLAddr is ignored.
	EmbeddedGraphQL bool

	// GraphQL listener options (used when EmbeddedGraphQL is true).
	GraphQLAPIExportSliceName      string // APIExportEndpointSlice name (default: "core.faros.sh")
	GraphQLAPIExportLogicalCluster string // logical cluster of that endpointslice (default: "root:faros:providers")
	GraphQLGRPCAddr                string // in-process gRPC address (default: "localhost:50051")
	GraphQLPlayground              bool   // enable playground UI
	GraphQLPort                    int    // port for the embedded GraphQL HTTP server; 0 = serve via hub mux only

	// PortalDevURL, when set, reverse-proxies /ui/* to this URL (typically
	// a Vite dev server, e.g. http://localhost:3000). Takes precedence over the
	// embedded portal dist (if built with -tags portal_embed).
	PortalDevURL string
	// PortalFrameSources are additional CSP frame-src source expressions allowed
	// by the portal. Keep this narrow; provider UIs are still same-origin through
	// the hub, while platform-owned preview hosts can be added explicitly.
	PortalFrameSources []string

	// Embedded kcp options
	EmbeddedKCP         bool   // Enable embedded kcp server
	KCPRootDir          string // Root directory for kcp data (default: <DataDir>/kcp)
	KCPSecurePort       int    // Secure port for kcp API server (default: 6443)
	KCPBindAddress      string // Bind address for kcp API server (default: "127.0.0.1")
	KCPBatteriesInclude string // Comma-separated list of batteries to include (default: "admin,user")
	KCPTLSCertFile      string // TLS certificate file for kcp API server
	KCPTLSKeyFile       string // TLS key file for kcp API server
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
}

// NewOptions returns default Options.
func NewOptions() *Options {
	return &Options{
		DataDir:             "/tmp/faros-data",
		ListenAddr:          ":9443",
		HubExternalURL:      "https://localhost:9443",
		GraphQLAddr:         "",
		EmbeddedKCP:         false,
		KCPSecurePort:       6443,
		KCPBindAddress:      "127.0.0.1",
		KCPBatteriesInclude: "admin,user",

		GraphQLAPIExportSliceName:      "core.faros.sh",
		GraphQLAPIExportLogicalCluster: kcppaths.SystemControllers,
		GraphQLGRPCAddr:                "localhost:50051",
		GraphQLPlayground:              true,
		TelemetryMode:                  "off",
	}
}
