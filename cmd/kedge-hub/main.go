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

// Package main is the entrypoint for the kedge-hub server.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"
	"k8s.io/klog/v2"

	"github.com/faroshq/faros-kedge/pkg/hub"
)

func main() {
	opts := hub.NewOptions()

	cmd := &cobra.Command{
		Use:   "kedge-hub",
		Short: "Kedge hub server - multi-tenant control plane",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer cancel()

			server, err := hub.NewServer(opts)
			if err != nil {
				return fmt.Errorf("failed to create hub server: %w", err)
			}

			return server.Run(ctx)
		},
	}

	cmd.Flags().StringVar(&opts.DataDir, "data-dir", opts.DataDir, "Data directory for state")
	cmd.Flags().StringVar(&opts.ListenAddr, "listen-addr", opts.ListenAddr, "Address to listen on")
	cmd.Flags().StringVar(&opts.Kubeconfig, "kubeconfig", "", "Kubeconfig for hub cluster")
	cmd.Flags().StringVar(&opts.ExternalKCPKubeconfig, "external-kcp-kubeconfig", "", "Kubeconfig for external kcp (empty for embedded)")
	cmd.Flags().StringVar(&opts.IDPIssuerURL, "idp-issuer-url", "", "OIDC identity provider issuer URL")
	cmd.Flags().StringVar(&opts.IDPClientID, "idp-client-id", "kedge", "OIDC identity provider client ID")
	cmd.Flags().StringVar(&opts.IDPCAFile, "idp-ca-file", "", "PEM-encoded CA bundle for verifying the IdP's TLS cert (required for self-signed/private CAs)")
	cmd.Flags().StringVar(&opts.ServingCertFile, "serving-cert-file", "", "TLS certificate file for HTTPS serving")
	cmd.Flags().StringVar(&opts.ServingKeyFile, "serving-key-file", "", "TLS key file for HTTPS serving")
	cmd.Flags().StringVar(&opts.HubExternalURL, "hub-external-url", opts.HubExternalURL, "External URL of this hub (for kubeconfig generation)")
	cmd.Flags().StringVar(&opts.HubInternalURL, "hub-internal-url", "", "Internal URL for kcp mount resolution (default: derived from listen-addr; avoids CDN loops)")
	cmd.Flags().BoolVar(&opts.DevMode, "dev-mode", false, "Enable dev mode (skip TLS verification for OIDC)")
	cmd.Flags().StringSliceVar(&opts.StaticAuthTokens, "static-auth-token", nil, "Static bearer tokens for access (can be specified multiple times)")

	cmd.Flags().StringVar(&opts.GraphQLAddr, "graphql-addr", opts.GraphQLAddr, "Address of an external GraphQL gateway to proxy /graphql/* requests to (empty to disable)")
	cmd.Flags().BoolVar(&opts.EmbeddedGraphQL, "embedded-graphql", opts.EmbeddedGraphQL, "Run GraphQL listener+gateway in-process (requires embedded or external kcp; overrides --graphql-addr)")
	cmd.Flags().StringVar(&opts.GraphQLAPIExportSliceName, "graphql-apiexport-slice-name", opts.GraphQLAPIExportSliceName, "APIExportEndpointSlice name to watch for GraphQL schema generation")
	cmd.Flags().StringVar(&opts.GraphQLAPIExportLogicalCluster, "graphql-apiexport-logical-cluster", opts.GraphQLAPIExportLogicalCluster, "Logical cluster path where the APIExportEndpointSlice lives")
	cmd.Flags().StringVar(&opts.GraphQLGRPCAddr, "graphql-grpc-addr", opts.GraphQLGRPCAddr, "In-process gRPC address for embedded GraphQL listener/gateway communication")
	cmd.Flags().BoolVar(&opts.GraphQLPlayground, "graphql-playground", opts.GraphQLPlayground, "Enable the GraphQL playground UI")

	cmd.Flags().StringVar(&opts.PortalDevURL, "portal-dev-url", "", "Reverse-proxy /console/* to this URL (e.g. http://localhost:3000 for Vite dev server); takes precedence over embedded portal dist")

	// Embedded kcp flags
	cmd.Flags().BoolVar(&opts.EmbeddedKCP, "embedded-kcp", opts.EmbeddedKCP, "Enable embedded kcp server (runs kcp in-process)")
	cmd.Flags().StringVar(&opts.KCPRootDir, "kcp-root-dir", "", "Root directory for embedded kcp data (default: <data-dir>/kcp)")
	cmd.Flags().IntVar(&opts.KCPSecurePort, "kcp-secure-port", opts.KCPSecurePort, "Secure port for embedded kcp API server")
	cmd.Flags().StringVar(&opts.KCPBindAddress, "kcp-bind-address", opts.KCPBindAddress, "Bind address for embedded kcp API server (default: 127.0.0.1, use 0.0.0.0 for all interfaces)")
	cmd.Flags().StringVar(&opts.KCPBatteriesInclude, "kcp-batteries-include", opts.KCPBatteriesInclude, "Comma-separated list of kcp batteries to include")
	cmd.Flags().StringVar(&opts.KCPTLSCertFile, "kcp-tls-cert-file", "", "TLS certificate file for embedded kcp API server")
	cmd.Flags().StringVar(&opts.KCPTLSKeyFile, "kcp-tls-key-file", "", "TLS key file for embedded kcp API server")

	// Add klog flags (provides -v for log verbosity, shared with embedded kcp)
	goFlags := flag.NewFlagSet("", flag.ContinueOnError)
	klog.InitFlags(goFlags)
	cmd.Flags().AddGoFlagSet(goFlags)

	if err := cmd.Execute(); err != nil {
		klog.Fatal(err)
		os.Exit(1)
	}
}
