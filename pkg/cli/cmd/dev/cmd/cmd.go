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
	devInitExampleUses = `  # One kind cluster running the hub, the edges, infrastructure, code, agents
  # and App Studio providers, and an agent that joins that same cluster as
  # the edge "local" (default)
  faros dev init

  # Same, with GitHub sign-in for the code provider (register the callback
  # https://console.127.0.0.1.sslip.io:9443/services/providers/code/oauth/github/callback
  # on the GitHub OAuth App)
  GITHUB_OAUTH_CLIENT_ID=... GITHUB_OAUTH_CLIENT_SECRET=... faros dev init

  # Only edges, plus the quickstart provider
  faros dev init --providers edges,quickstart

  # App Studio (pulls in infrastructure, which it requires)
  faros dev init --providers app-studio

  # Hub only: no providers, no edge
  faros dev init --providers "" --with-edge=false

  # Extra plain worker kind clusters to connect by hand
  faros dev init --worker-count 1

  # Use local charts from a faros checkout for the hub and the providers
  faros dev init --chart-path deploy/charts/faros-hub --provider-chart-repo .

  # Pin chart versions
  faros dev init --chart-version 0.1.31 --provider-chart-version 0.1.19`

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
		Short:   "Initialize a local faros environment (one kind cluster: hub, providers and an edge)",
		Long: `Initialize a local faros environment in a single kind cluster.

This command will:

- Create a hub kind cluster and install the faros-hub Helm chart (default:
  OCI chart from ghcr.io) with the static token dev-token, served at
  https://console.127.0.0.1.sslip.io:9443 (public DNS answers every
  *.127.0.0.1.sslip.io name with 127.0.0.1, so no /etc/hosts entry is needed)
- Install the providers named by --providers into that same cluster from
  their published charts (default: edges, infrastructure, code, agents,
  app-studio; quickstart is also supported), onboarding each one on the hub.
  A provider's requirements are added (app-studio needs infrastructure);
  agents and app-studio get their own Postgres; infrastructure runs in
  operator mode and installs kro into the cluster, with an Envoy Gateway
  serving published apps at https://<app>.apps.127.0.0.1.sslip.io:10443;
  code gets GitHub sign-in when GITHUB_OAUTH_CLIENT_ID and
  GITHUB_OAUTH_CLIENT_SECRET are set
- Enable every installed provider in the dev user's default workspace
  (--enable-providers, default on)
- Join the hub kind cluster itself as a KubernetesCluster edge (--with-edge,
  default on): the faros-agent runs in the cluster next to the hub, so
  "faros edge list" shows a Ready edge right after login
- Create N extra plain worker kind clusters when --worker-count > 0, for
  connecting more edges by hand
- Configure necessary port mappings (9443, 8080, 10443)

The provider and edge automation signs in with the static dev token, so
--with-dex (which disables token login) skips it.

The hub chart can be sourced from:

- OCI registry (default): oci://ghcr.io/faroshq/charts/faros-hub
- Local filesystem: --chart-path ./deploy/charts/faros-hub
- Custom OCI registry: --chart-path oci://custom.registry/charts/faros-hub

Provider charts come from --provider-chart-repo: an OCI base (default
oci://ghcr.io/faroshq/charts, latest published version of each chart) or a
faros checkout, which uses providers/<name>/deploy/chart.`,
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
