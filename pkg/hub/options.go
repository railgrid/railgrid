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

// Options holds configuration for the hub server.
type Options struct {
	DataDir               string
	ListenAddr            string
	Kubeconfig            string
	ExternalKCPKubeconfig string
	IDPIssuerURL          string
	IDPClientID           string
	ServingCertFile       string
	ServingKeyFile        string
	HubExternalURL        string
	HubInternalURL        string // Internal URL for kcp mount resolution (avoids CDN/proxy loops)
	DevMode               bool
	StaticAuthTokens      []string

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

	// Embedded kcp options
	EmbeddedKCP         bool   // Enable embedded kcp server
	KCPRootDir          string // Root directory for kcp data (default: <DataDir>/kcp)
	KCPSecurePort       int    // Secure port for kcp API server (default: 6443)
	KCPBindAddress      string // Bind address for kcp API server (default: "127.0.0.1")
	KCPBatteriesInclude string // Comma-separated list of batteries to include (default: "admin,user")
	KCPTLSCertFile      string // TLS certificate file for kcp API server
	KCPTLSKeyFile       string // TLS key file for kcp API server
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
