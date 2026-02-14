package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/faroshq/faros-kedge/pkg/hub"
	"github.com/spf13/cobra"
	"k8s.io/klog/v2"
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
	cmd.Flags().StringVar(&opts.ExternalKCPKubeconfig, "external-kcp-kubeconfig", "", "Kubeconfig for external KCP (empty for embedded)")
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
