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

import "time"

// Options holds configuration for the hub server.
type Options struct {
	DataDir               string
	ListenAddr            string
	Kubeconfig            string
	ExternalKCPKubeconfig string
	// KCPShardKubeconfig is an optional kubeconfig used only for connections
	// to APIExport virtual-workspace URLs (which kcp publishes as shard-direct
	// addresses in APIExportEndpointSlice). When the primary kubeconfig
	// (ExternalKCPKubeconfig) authenticates via the kcp front-proxy, its
	// client cert is signed by the front-proxy CA and is not trusted by
	// shards directly — so cluster discovery via the multicluster provider
	// fails with "the server has asked for the client to provide credentials".
	// Set this to a kubeconfig whose cert is signed by the shared shard
	// ClientCA (e.g. one produced by a kcp-operator Kubeconfig CR targeting
	// rootShardRef). If empty, ExternalKCPKubeconfig is used for both.
	KCPShardKubeconfig string
	IDPIssuerURL       string
	IDPClientID        string
	// IDPCAFile is a path to a PEM-encoded CA bundle used to verify the IdP's
	// TLS certificate. Required when IDPIssuerURL is https and uses a cert
	// not signed by a system trust anchor (e.g. the dev Dex deployment).
	IDPCAFile        string
	ServingCertFile  string
	ServingKeyFile   string
	HubExternalURL   string
	HubInternalURL   string // Internal URL for kcp mount resolution (avoids CDN/proxy loops)
	DevMode          bool
	StaticAuthTokens []string

	// Providers is the list of first-party builtin providers to materialize
	// into root:kedge:providers at bootstrap. The flag accepts a comma-
	// separated list or repeats; see cmd/kedge-hub/main.go for the default.
	// Empty/nil enables every known builtin (kcp.BuiltinProviderNames()).
	// Dependencies between builtins are validated at hub startup — see
	// pkg/hub/kcp.builtinEntries[].Requires.
	Providers []string

	// GraphQLAddr is the address of an external GraphQL gateway to proxy /graphql/ requests to.
	// If empty and EmbeddedGraphQL is false, the graphql proxy is disabled.
	GraphQLAddr string

	// EmbeddedGraphQL runs the GraphQL listener+gateway in-process alongside the hub.
	// When true, GraphQLAddr is ignored.
	EmbeddedGraphQL bool

	// GraphQL listener options (used when EmbeddedGraphQL is true).
	GraphQLAPIExportSliceName      string // APIExportEndpointSlice name (default: "core.faros.sh")
	GraphQLAPIExportLogicalCluster string // logical cluster of that endpointslice (default: "root:kedge:providers")
	GraphQLGRPCAddr                string // in-process gRPC address (default: "localhost:50051")
	GraphQLPlayground              bool   // enable playground UI
	GraphQLPort                    int    // port for the embedded GraphQL HTTP server; 0 = serve via hub mux only

	// PortalDevURL, when set, reverse-proxies /ui/* to this URL (typically
	// a Vite dev server, e.g. http://localhost:3000). Takes precedence over the
	// embedded portal dist (if built with -tags portal_embed).
	PortalDevURL string

	// AppStudioDatabaseURL, when set, points App Studio message persistence at
	// a Postgres database owned by the App Studio provider deployment.
	AppStudioDatabaseURL string

	// AppStudioInMemoryMessageStore enables non-durable App Studio message
	// storage for local development and tests. Production deployments should
	// configure AppStudioDatabaseURL instead.
	AppStudioInMemoryMessageStore bool

	// AppStudioMessageEncryptionKeys is a comma-separated list of
	// key-id:base64-aes-key entries. The first key encrypts new messages and
	// all keys can decrypt existing messages.
	AppStudioMessageEncryptionKeys string

	// AppStudioMessageRetention controls the background cleanup horizon for
	// old chat messages. Zero disables periodic cleanup.
	AppStudioMessageRetention time.Duration

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
		DataDir:             "/tmp/kedge-data",
		ListenAddr:          ":9443",
		HubExternalURL:      "https://localhost:9443",
		GraphQLAddr:         "",
		EmbeddedKCP:         false,
		KCPSecurePort:       6443,
		KCPBindAddress:      "127.0.0.1",
		KCPBatteriesInclude: "admin,user",

		GraphQLAPIExportSliceName:      "core.faros.sh",
		GraphQLAPIExportLogicalCluster: "root:kedge:providers",
		GraphQLGRPCAddr:                "localhost:50051",
		GraphQLPlayground:              true,
	}
}
