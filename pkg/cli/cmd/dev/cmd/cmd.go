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

// Package cmd provides the faros dev command and its subcommands.
package cmd

import (
	"github.com/spf13/cobra"
	"k8s.io/cli-runtime/pkg/genericclioptions"

	"github.com/faroshq/faros/pkg/cli/cmd/dev/plugin"
)

var (
	devInitExampleUses = `  # Initialize a hub-only local faros environment (default, for end users)
  faros dev init

  # Hub + 1 worker kind cluster (typical developer setup)
  faros dev init --worker-count 1

  # Hub + 3 worker kind clusters
  faros dev init --worker-count 3

  # Use a local chart for development
  faros dev init --chart-path ../deploy/charts/faros-hub

  # Pin chart version
  faros dev init --chart-version 0.1.0`

	devUpdateExampleUses = `  # Upgrade the faros-hub release on the existing hub cluster
  faros dev update

  # Upgrade to a specific image tag
  faros dev update --tag v0.0.52

  # Upgrade to a specific chart version
  faros dev update --chart-version 0.1.0`
)

// New creates the dev command and all its subcommands.
func New(streams genericclioptions.IOStreams) (*cobra.Command, error) {
	cmd := &cobra.Command{
		Use:   "dev",
		Short: "Manage development environment for faros",
		Long: `Manage a development environment for faros using kind clusters.

This command provides subcommands to initialize, update and delete kind
clusters configured for faros.`,
		SilenceUsage: true,
	}

	initCmd, err := newInitCommand(streams)
	if err != nil {
		return nil, err
	}
	cmd.AddCommand(initCmd)

	updateCmd, err := newUpdateCommand(streams)
	if err != nil {
		return nil, err
	}
	cmd.AddCommand(updateCmd)

	deleteCmd, err := newDeleteCommand(streams)
	if err != nil {
		return nil, err
	}
	cmd.AddCommand(deleteCmd)

	return cmd, nil
}

func newInitCommand(streams genericclioptions.IOStreams) (*cobra.Command, error) {
	opts := plugin.NewDevOptions(streams)
	cmd := &cobra.Command{
		Use:     "init",
		Aliases: []string{"create"},
		Short:   "Initialize a local faros environment (hub kind cluster + optional workers)",
		Long: `Initialize a local faros environment using kind clusters.

This command will:

- Create a hub kind cluster running the faros hub
- Create N worker (agent) kind clusters when --worker-count > 0
- Add faros.localhost to /etc/hosts (with sudo prompts if needed)
- Install the faros-hub Helm chart (default: OCI chart from ghcr.io)
- Configure necessary port mappings (9443, 8080)

The default is hub-only (--worker-count 0), suitable for end users who just
want to run a local faros instance. Developers working on agents should set
--worker-count to the number of edges they want to simulate.

The hub chart can be sourced from:

- OCI registry (default): oci://ghcr.io/faroshq/charts/faros-hub
- Local filesystem: --chart-path ./deploy/charts/faros-hub
- Custom OCI registry: --chart-path oci://custom.registry/charts/faros-hub`,
		Example:      devInitExampleUses,
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

func newUpdateCommand(streams genericclioptions.IOStreams) (*cobra.Command, error) {
	opts := plugin.NewDevOptions(streams)
	cmd := &cobra.Command{
		Use:   "update",
		Short: "Upgrade the faros-hub release on an existing local environment",
		Long: `Upgrade the faros-hub Helm release on the hub kind cluster
created by ` + "`faros dev init`" + `. The kind clusters themselves are not modified;
only the faros-hub release is upgraded (image, tag, chart version, …).`,
		Example:      devUpdateExampleUses,
		SilenceUsage: true,
		Args:         cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := opts.Complete(args); err != nil {
				return err
			}
			if err := opts.Validate(); err != nil {
				return err
			}
			return opts.RunUpdate(cmd.Context())
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
		Long: `Delete the development environment for faros.

This command will delete the kind cluster created for faros development.`,
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
