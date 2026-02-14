package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

func newGetCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "get [resource]",
		Short: "Get resources",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			resource := args[0]

			switch resource {
			case "virtualworkloads", "vw":
				// TODO: Fetch VirtualWorkloads from hub
				fmt.Println("NAME\tPHASE\tREADY\tAVAILABLE\tAGE")
				fmt.Println("(no virtualworkloads found)")
			case "placements":
				// TODO: Fetch Placements from hub
				fmt.Println("NAME\tSITE\tPHASE\tREADY\tAGE")
				fmt.Println("(no placements found)")
			case "sites":
				// TODO: Fetch Sites from hub
				fmt.Println("NAME\tSTATUS\tK8S VERSION\tREGION\tAGE")
				fmt.Println("(no sites found)")
			default:
				return fmt.Errorf("unknown resource type: %s", resource)
			}

			return nil
		},
	}

	return cmd
}
