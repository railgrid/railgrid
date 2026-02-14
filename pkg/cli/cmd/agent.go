package cmd

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/faroshq/faros-kedge/pkg/agent"
	"github.com/spf13/cobra"
)

func newAgentCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "agent",
		Short: "Agent management commands",
	}

	cmd.AddCommand(
		newAgentJoinCommand(),
		newAgentTokenCommand(),
	)

	return cmd
}

func newAgentJoinCommand() *cobra.Command {
	opts := agent.NewOptions()

	cmd := &cobra.Command{
		Use:   "join",
		Short: "Join a site to the hub",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer cancel()

			a, err := agent.New(opts)
			if err != nil {
				return fmt.Errorf("failed to create agent: %w", err)
			}

			return a.Run(ctx)
		},
	}

	cmd.Flags().StringVar(&opts.HubURL, "hub-url", "", "Hub server URL")
	cmd.Flags().StringVar(&opts.HubKubeconfig, "hub-kubeconfig", "", "Kubeconfig for hub cluster")
	cmd.Flags().StringVar(&opts.Token, "token", "", "Bootstrap token")
	cmd.Flags().StringVar(&opts.SiteName, "site-name", "", "Name of this site")
	cmd.Flags().StringVar(&opts.Kubeconfig, "kubeconfig", "", "Path to target cluster kubeconfig")
	cmd.Flags().StringVar(&opts.Context, "context", "", "Kubeconfig context to use")
	cmd.Flags().StringToStringVar(&opts.Labels, "labels", nil, "Labels for this site")

	return cmd
}

func newAgentTokenCommand() *cobra.Command {
	var siteName string

	cmd := &cobra.Command{
		Use:   "token",
		Short: "Manage agent tokens",
	}

	createCmd := &cobra.Command{
		Use:   "create",
		Short: "Create a bootstrap token for a site",
		RunE: func(cmd *cobra.Command, args []string) error {
			if siteName == "" {
				return fmt.Errorf("--site-name is required")
			}

			// TODO: Generate bootstrap token
			fmt.Printf("Bootstrap token for site %s (not yet implemented)\n", siteName)
			return nil
		},
	}
	createCmd.Flags().StringVar(&siteName, "site-name", "", "Site name")

	cmd.AddCommand(createCmd)
	return cmd
}
