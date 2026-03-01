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

package cmd

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/faroshq/faros-kedge/pkg/agent"
)

func newAgentCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "agent",
		Short: "Agent management commands",
	}

	cmd.AddCommand(
		newAgentJoinCommand(),
		newAgentTokenCommand(),
	)

	return cmd
}

func newAgentJoinCommand() *cobra.Command {
	opts := agent.NewOptions()

	cmd := &cobra.Command{
		Use:   "join",
		Short: "Join an edge to the hub",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer cancel()

			a, err := agent.New(opts)
			if err != nil {
				return fmt.Errorf("failed to create agent: %w", err)
			}

			return a.Run(ctx)
		},
	}

	cmd.Flags().StringVar(&opts.HubURL, "hub-url", "", "Hub server URL")
	cmd.Flags().StringVar(&opts.HubKubeconfig, "hub-kubeconfig", "", "Kubeconfig for hub cluster")
	cmd.Flags().StringVar(&opts.HubContext, "hub-context", "", "Kubeconfig context for hub cluster")
	cmd.Flags().StringVar(&opts.TunnelURL, "tunnel-url", "", "Hub tunnel URL (defaults to hub URL)")
	cmd.Flags().StringVar(&opts.Token, "token", "", "Bootstrap token")
	cmd.Flags().StringVar(&opts.EdgeName, "edge-name", "", "Name of this edge")
	cmd.Flags().StringVar(&opts.Kubeconfig, "kubeconfig", "", "Path to target cluster kubeconfig")
	cmd.Flags().StringVar(&opts.Context, "context", "", "Kubeconfig context to use")
	cmd.Flags().StringToStringVar(&opts.Labels, "labels", nil, "Labels for this edge")
	cmd.Flags().BoolVar(&opts.InsecureSkipTLSVerify, "hub-insecure-skip-tls-verify", false, "Skip TLS certificate verification for the hub connection (insecure, for development only)")
	cmd.Flags().IntVar(&opts.SSHProxyPort, "ssh-proxy-port", 22, "Local port of the SSH daemon to proxy connections to (default 22; set to a different port in test environments)")
	cmd.Flags().StringVar((*string)(&opts.Type), "type", string(agent.AgentTypeKubernetes),
		`Edge type: "kubernetes" (Kubernetes cluster) or "server" (bare-metal/systemd host with SSH access)`)
	cmd.Flags().StringVar(&opts.Cluster, "cluster", "",
		"kcp logical cluster name (e.g. '1tww43gelbj45g0k'); required when using static token auth without a cluster-scoped hub kubeconfig")

	return cmd
}

func newAgentTokenCommand() *cobra.Command {
	var edgeName string

	cmd := &cobra.Command{
		Use:   "token",
		Short: "Manage agent tokens",
	}

	createCmd := &cobra.Command{
		Use:   "create",
		Short: "Create a bootstrap token for an edge",
		RunE: func(cmd *cobra.Command, args []string) error {
			if edgeName == "" {
				return fmt.Errorf("--edge-name is required")
			}

			// TODO: Generate bootstrap token
			fmt.Printf("Bootstrap token for edge %s (not yet implemented)\n", edgeName)
			return nil
		},
	}
	createCmd.Flags().StringVar(&edgeName, "edge-name", "", "Edge name")

	cmd.AddCommand(createCmd)
	return cmd
}
