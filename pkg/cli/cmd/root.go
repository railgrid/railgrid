package cmd

import (
	"github.com/spf13/cobra"
)

// NewRootCommand creates the root cobra command for the kedge CLI.
func NewRootCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "kedge",
		Short: "Kedge - workload management across edge sites",
		Long: `Kedge is an OSS control plane that combines multi-tenant API serving
with reverse-dialer connectivity mesh and OIDC identity.

Remote agents "kedge" (pull) toward the hub via reverse tunnels,
enabling secure workload deployment across distributed sites.`,
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	cmd.PersistentFlags().StringVar(&kubeconfig, "kubeconfig", "", "Path to kubeconfig file")

	cmd.AddCommand(
		newInitCommand(),
		newLoginCommand(),
		newGetTokenCommand(),
		newAgentCommand(),
		newSiteCommand(),
		newApplyCommand(),
		newGetCommand(),
		newWorkspaceCommand(),
		newVersionCommand(),
	)

	return cmd
}
