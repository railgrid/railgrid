package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

func newApplyCommand() *cobra.Command {
	var filename string

	cmd := &cobra.Command{
		Use:   "apply",
		Short: "Apply a VirtualWorkload from a file",
		RunE: func(cmd *cobra.Command, args []string) error {
			if filename == "" {
				return fmt.Errorf("-f flag is required")
			}

			// TODO: Read file, parse VirtualWorkload, apply to hub
			fmt.Printf("Applying from %s (not connected to hub)\n", filename)
			return nil
		},
	}

	cmd.Flags().StringVarP(&filename, "filename", "f", "", "Path to VirtualWorkload YAML")

	return cmd
}
