package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

func newLoginCommand() *cobra.Command {
	var hubURL string

	cmd := &cobra.Command{
		Use:   "login",
		Short: "Authenticate with the kedge hub via OIDC",
		RunE: func(cmd *cobra.Command, args []string) error {
			if hubURL == "" {
				return fmt.Errorf("--hub-url is required")
			}

			// TODO: Open browser for OIDC flow
			// TODO: Start local callback server
			// TODO: Exchange code for tokens
			// TODO: Save kubeconfig

			fmt.Printf("Login to %s (OIDC flow not yet implemented)\n", hubURL)
			return nil
		},
	}

	cmd.Flags().StringVar(&hubURL, "hub-url", "", "Hub server URL")

	return cmd
}
