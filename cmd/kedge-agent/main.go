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

// Package main is the entrypoint for the kedge-agent.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"
	"k8s.io/klog/v2"

	"github.com/faroshq/faros-kedge/pkg/agent"
)

func main() {
	opts := agent.NewOptions()

	cmd := &cobra.Command{
		Use:   "kedge-agent",
		Short: "Kedge agent - connects an edge to the hub via reverse tunnel",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer cancel()

			logger := klog.FromContext(ctx)

			// In-cluster kubeconfig recovery: when running inside a Kubernetes Pod
			// with a bootstrap token, check whether a prior run already saved the
			// hub kubeconfig to the well-known Secret. If so, use it directly
			// instead of performing a new token exchange.
			if opts.Token != "" && opts.EdgeName != "" && isInCluster() {
				kc, err := loadKubeconfigFromSecret(opts.EdgeName)
				if err != nil {
					logger.Info("Could not check in-cluster kubeconfig Secret", "err", err)
				} else if kc != "" {
					tmpPath, err := writeKubeconfigToTempFile(kc)
					if err != nil {
						logger.Error(err, "failed to write in-cluster kubeconfig to temp file")
					} else if err := agent.ValidateAgentKubeconfig(tmpPath, opts.InsecureSkipTLSVerify); err != nil {
						logger.Info("In-cluster kubeconfig is invalid, falling back to join token",
							"edgeName", opts.EdgeName, "err", err)
						_ = os.Remove(tmpPath)
					} else {
						logger.Info("Using kubeconfig from in-cluster Secret (previous registration)", "edgeName", opts.EdgeName, "path", tmpPath)
						opts.HubKubeconfig = tmpPath
						opts.Token = ""
						opts.UsingSavedKubeconfig = true
					}
				}
			}

			// Token-exchange: if a saved kubeconfig exists from a previous
			// join-token registration, use it instead of the bootstrap token.
			if opts.HubKubeconfig == "" && opts.EdgeName != "" {
				kubeconfigPath, err := agent.LoadAgentKubeconfig(opts.EdgeName)
				if err != nil {
					logger.Info("Could not check for saved agent kubeconfig", "err", err)
				} else if kubeconfigPath != "" {
					// Validate the saved kubeconfig before using it. If the Edge was
					// recreated, the SA token will have been revoked and we must fall
					// back to the join token for a fresh token exchange.
					if err := agent.ValidateAgentKubeconfig(kubeconfigPath, opts.InsecureSkipTLSVerify); err != nil {
						logger.Info("Saved agent kubeconfig is invalid, deleting and falling back to join token",
							"edgeName", opts.EdgeName, "path", kubeconfigPath, "err", err)
						if delErr := agent.DeleteAgentKubeconfig(opts.EdgeName); delErr != nil {
							logger.Error(delErr, "Failed to delete stale agent kubeconfig")
						}
					} else {
						logger.Info("Using saved agent kubeconfig from previous registration", "edgeName", opts.EdgeName, "path", kubeconfigPath)
						opts.HubKubeconfig = kubeconfigPath
						opts.Token = "" // Clear join token; SA kubeconfig takes precedence.
						opts.UsingSavedKubeconfig = true
					}
				}
			}

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
	cmd.Flags().StringToStringVar(&opts.Labels, "labels", nil, "Labels for this edge (key=value pairs)")
	cmd.Flags().StringVar((*string)(&opts.Type), "type", string(agent.AgentTypeKubernetes),
		"Edge type: 'kubernetes' (k8s cluster) or 'server' (bare-metal/systemd host)")
	cmd.Flags().BoolVar(&opts.InsecureSkipTLSVerify, "insecure-skip-tls-verify", false, "Skip TLS certificate verification for hub connection (dev/test only)")
	cmd.Flags().IntVar(&opts.SSHProxyPort, "ssh-proxy-port", 22, "Local SSH daemon port to proxy connections to")
	cmd.Flags().StringVar(&opts.SSHUser, "ssh-user", "", "SSH username for server-type edges (default: current user)")
	cmd.Flags().StringVar(&opts.SSHPassword, "ssh-password", "", "SSH password for password-based authentication (prefer --ssh-private-key for security)")
	cmd.Flags().StringVar(&opts.SSHPrivateKeyPath, "ssh-private-key", "", "Path to SSH private key file for key-based authentication")
	cmd.Flags().StringVar(&opts.Cluster, "cluster", "", "kcp logical cluster path (e.g., 'root:kedge:user-default'); required when using static token auth")

	if err := cmd.Execute(); err != nil {
		klog.Fatal(err)
		os.Exit(1)
	}
}
