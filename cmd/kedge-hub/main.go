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

package main

import (
	"context"
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
	cmd.Flags().StringVar(&opts.DexIssuerURL, "dex-issuer-url", "", "Dex OIDC issuer URL")
	cmd.Flags().StringVar(&opts.DexClientID, "dex-client-id", "kedge", "Dex OAuth2 client ID")
	cmd.Flags().StringVar(&opts.DexClientSecret, "dex-client-secret", "", "Dex OAuth2 client secret")
	cmd.Flags().StringVar(&opts.ServingCertFile, "serving-cert-file", "", "TLS certificate file for HTTPS serving")
	cmd.Flags().StringVar(&opts.ServingKeyFile, "serving-key-file", "", "TLS key file for HTTPS serving")
	cmd.Flags().StringVar(&opts.HubExternalURL, "hub-external-url", opts.HubExternalURL, "External URL of this hub (for kubeconfig generation)")
	cmd.Flags().BoolVar(&opts.DevMode, "dev-mode", false, "Enable dev mode (skip TLS verification for OIDC)")

	if err := cmd.Execute(); err != nil {
		klog.Fatal(err)
		os.Exit(1)
	}
}
