package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

func newSiteCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "site",
		Short: "Manage sites",
	}

	cmd.AddCommand(
		newSiteListCommand(),
		newSiteGetCommand(),
	)

	return cmd
}

func newSiteListCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List all connected sites",
		RunE: func(cmd *cobra.Command, args []string) error {
			// TODO: Fetch sites from hub
			fmt.Println("NAME\tSTATUS\tK8S VERSION\tREGION\tAGE")
			fmt.Println("(no sites found - connect to hub first)")
			return nil
		},
	}
}

func newSiteGetCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "get [name]",
		Short: "Get site details",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			// TODO: Fetch site from hub
			fmt.Printf("Site: %s (not connected to hub)\n", name)
			return nil
		},
	}
}
