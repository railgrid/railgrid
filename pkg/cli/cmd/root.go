/*
Copyright 2026 The Railgrid Authors.

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

// Package cmd implements the railgrid CLI commands.
package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"k8s.io/cli-runtime/pkg/genericclioptions"

	devcmd "github.com/railgrid/railgrid/pkg/cli/cmd/dev/cmd"
)

// Command groups shown in `railgrid --help`. Order here is display order.
const (
	groupStart  = "start"
	groupEdges  = "edges"
	groupAccess = "access"
	groupDev    = "dev"
	groupOps    = "ops"
)

// NewRootCommand creates the root cobra command for the railgrid CLI.
func NewRootCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "railgrid",
		Short: "railgrid: an open-source control plane for platform teams",
		Long: `railgrid connects Kubernetes clusters and Linux servers behind NAT to one
hub, and gives every team an isolated workspace with its own APIs, RBAC and
providers on top.

Typical session:

  railgrid login --hub-url https://hub.example.com   # OIDC in the browser
  railgrid use                                        # pick an org and workspace
  railgrid edge list                                  # what is connected
  railgrid connect my-cluster                         # point kubectl at an edge
  railgrid ssh my-server                              # shell on a Linux edge
  railgrid whoami                                     # where am I, what can I do

Every command talks to the hub as you, with your workspace RBAC. Run
'railgrid <command> --help' for details and 'railgrid completion --help' for shell
completion.`,
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	cmd.PersistentFlags().StringVar(&kubeconfig, "kubeconfig", "", "Path to the kubeconfig file (default: $KUBECONFIG, then ~/.kube/config)")
	cmd.PersistentFlags().BoolVar(&globalInsecureTLS, "insecure-skip-tls-verify", false, "Skip TLS certificate verification when talking to the hub")

	cmd.AddGroup(
		&cobra.Group{ID: groupStart, Title: "Getting started:"},
		&cobra.Group{ID: groupEdges, Title: "Edges (clusters and servers):"},
		&cobra.Group{ID: groupAccess, Title: "Organizations and access:"},
		&cobra.Group{ID: groupDev, Title: "Developer workflow:"},
		&cobra.Group{ID: groupOps, Title: "Agents, hub and local development:"},
	)

	// Add dev command
	devCmd, err := devcmd.New(genericclioptions.IOStreams{In: os.Stdin, Out: os.Stdout, ErrOut: os.Stderr})
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v", err)
		os.Exit(1)
	}
	devCmd.GroupID = groupOps

	grouped := func(id string, cmds ...*cobra.Command) []*cobra.Command {
		for _, c := range cmds {
			c.GroupID = id
		}
		return cmds
	}
	cmd.AddCommand(grouped(groupStart,
		newLoginCommand(),
		newLogoutCommand(),
		newUseCommand(),
		newWhoamiCommand(),
		newTokenCommand(),
	)...)
	cmd.AddCommand(grouped(groupEdges,
		newEdgeCommand(),
		newConnectCommand(),
		newDisconnectCommand(),
		newSSHCommand(),
	)...)
	cmd.AddCommand(grouped(groupAccess,
		newOrgCommand(),
		newWorkspaceCommand(),
	)...)
	cmd.AddCommand(grouped(groupDev,
		newAppCommand(),
		newCommitCommand(),
		newSandboxCommand(),
		newEnvCommand(),
		newMCPCommand(),
		newSkillsCommand(),
	)...)
	cmd.AddCommand(grouped(groupOps,
		newAgentCommand(),
		newInstallCommand(),
		newInitCommand(),
		devCmd,
	)...)

	// Hidden: kubectl's exec credential plugin, doc generation, and raw kcp
	// workspace navigation.
	cmd.AddCommand(
		newVersionCommand(),
		newGetTokenCommand(),
		newDocsCommand(),
		newKCPWorkspaceCommand(),
	)

	rejectUnknownSubcommands(cmd)
	return cmd
}

// rejectUnknownSubcommands makes every command group below the root (a
// command with subcommands and no action of its own) fail on an argument that
// names none of its subcommands. Cobra checks that only on the root: below
// it, a group printed its help and exited 0, so 'railgrid mcp proxy' on a CLI
// without the proxy looked like success to scripts and MCP clients.
func rejectUnknownSubcommands(parent *cobra.Command) {
	for _, c := range parent.Commands() {
		rejectUnknownSubcommands(c)
		if !c.HasSubCommands() || c.Runnable() {
			continue
		}
		if c.Args == nil {
			c.Args = cobra.NoArgs // unknown command "<arg>" for "<group>"
		}
		c.RunE = func(c *cobra.Command, _ []string) error { return c.Help() }
	}
}
