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

// Package cmd implements the kedge CLI commands.
package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"k8s.io/cli-runtime/pkg/genericclioptions"

	devcmd "github.com/faroshq/faros-kedge/pkg/cli/cmd/dev/cmd"
)

// NewRootCommand creates the root cobra command for the kedge CLI.
func NewRootCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "kedge",
		Short: "Kedge - workload management across edges",
		Long: `Kedge is an OSS control plane that combines multi-tenant API serving
with reverse-dialer connectivity mesh and OIDC identity.

Remote agents "kedge" (pull) toward the hub via reverse tunnels,
enabling secure workload deployment across distributed edges.`,
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	cmd.PersistentFlags().StringVar(&kubeconfig, "kubeconfig", "", "Path to kubeconfig file")

	// Add dev command
	devCmd, err := devcmd.New(genericclioptions.IOStreams{In: os.Stdin, Out: os.Stdout, ErrOut: os.Stderr})
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v", err)
		os.Exit(1)
	}

	cmd.AddCommand(
		newInitCommand(),
		newLoginCommand(),
		newGetTokenCommand(),
		newAgentCommand(),
		newEdgeCommand(),
		newApplyCommand(),
		newGetCommand(),
		newWorkspaceCommand(),
		newVersionCommand(),
		newSSHCommand(),
		devCmd,
	)

	return cmd
}
