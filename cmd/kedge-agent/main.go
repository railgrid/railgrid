package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/faroshq/faros-kedge/pkg/agent"
	"github.com/spf13/cobra"
	"k8s.io/klog/v2"
)

func main() {
	cmd := &cobra.Command{
		Use:   "kedge-agent",
		Short: "Kedge agent - connects a site to the hub via reverse tunnel",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer cancel()

			opts := agent.NewOptions()
			cmd.Flags().StringVar(&opts.HubURL, "hub-url", opts.HubURL, "Hub server URL")
			cmd.Flags().StringVar(&opts.Token, "token", opts.Token, "Bootstrap token")
			cmd.Flags().StringVar(&opts.SiteName, "site-name", opts.SiteName, "Name of this site")
			cmd.Flags().StringVar(&opts.Kubeconfig, "kubeconfig", opts.Kubeconfig, "Path to target cluster kubeconfig")
			cmd.Flags().StringVar(&opts.Context, "context", opts.Context, "Kubeconfig context to use")
			cmd.Flags().StringToStringVar(&opts.Labels, "labels", opts.Labels, "Labels for this site (key=value pairs)")

			a, err := agent.New(opts)
			if err != nil {
				return fmt.Errorf("failed to create agent: %w", err)
			}

			return a.Run(ctx)
		},
	}

	if err := cmd.Execute(); err != nil {
		klog.Fatal(err)
		os.Exit(1)
	}
}
