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

// Package cmd provides the kedge dev command and its subcommands.
package cmd

import (
	"github.com/spf13/cobra"
	"k8s.io/cli-runtime/pkg/genericclioptions"

	"github.com/faroshq/faros-kedge/pkg/cli/cmd/dev/plugin"
)

var (
	devCreateExampleUses = `  # Create a development environment with kind cluster and default OCI chart
  kedge dev create

  # Create a development environment with custom hub cluster name
  kedge dev create --hub-cluster-name my-hub

  # Create with custom chart path (local development)
  kedge dev create --chart-path ../deploy/charts/kedge

  # Create with specific chart version
  kedge dev create --chart-version 0.1.0

  # Create with custom OCI chart
  kedge dev create --chart-path oci://registry.example.com/charts/kedge --chart-version 1.0.0`
)

// New creates the dev command and all its subcommands.
func New(streams genericclioptions.IOStreams) (*cobra.Command, error) {
	cmd := &cobra.Command{
		Use:   "dev",
		Short: "Manage development environment for kedge",
		Long: `Manage a development environment for kedge using kind clusters.

This command provides subcommands to initialize and manage kind clusters
configured for kedge development.`,
		SilenceUsage: true,
	}

	// Add create subcommand
	createCmd, err := newCreateCommand(streams)
	if err != nil {
		return nil, err
	}
	cmd.AddCommand(createCmd)

	// Add delete subcommand
	deleteCmd, err := newDeleteCommand(streams)
	if err != nil {
		return nil, err
	}
	cmd.AddCommand(deleteCmd)

	return cmd, nil
}

func newCreateCommand(streams genericclioptions.IOStreams) (*cobra.Command, error) {
	opts := plugin.NewDevOptions(streams)
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create development environment with kind cluster and kedge hub",
		Long: `Create a complete development environment for kedge using kind clusters.

This command will:

- Create a kind cluster configured for kedge development
- Add kedge.localhost to /etc/hosts (with sudo prompts if needed)
- Install kedge hub helm chart (default: OCI chart from ghcr.io)
- Configure necessary port mappings (8443, 8080)

The hub chart can be sourced from:

- OCI registry (default): oci://ghcr.io/faroshq/charts/kedge
- Local filesystem: --chart-path ./deploy/charts/kedge
- Custom OCI registry: --chart-path oci://custom.registry/charts/kedge`,
		Example:      devCreateExampleUses,
		SilenceUsage: true,
		Args:         cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := opts.Complete(args); err != nil {
				return err
			}

			if err := opts.Validate(); err != nil {
				return err
			}

			return opts.Run(cmd.Context())
		},
	}
	opts.AddCmdFlags(cmd)

	return cmd, nil
}

func newDeleteCommand(streams genericclioptions.IOStreams) (*cobra.Command, error) {
	opts := plugin.NewDevOptions(streams)
	cmd := &cobra.Command{
		Use:   "delete",
		Short: "Delete development environment",
		Long: `Delete the development environment for kedge.

This command will delete the kind cluster created for kedge development.`,
		SilenceUsage: true,
		Args:         cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := opts.Complete(args); err != nil {
				return err
			}

			if err := opts.Validate(); err != nil {
				return err
			}

			return opts.RunDelete()
		},
	}
	opts.AddCmdFlags(cmd)

	return cmd, nil
}
