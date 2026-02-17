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

	"github.com/faroshq/faros-kedge/pkg/hub"
	"github.com/spf13/cobra"
)

func newInitCommand() *cobra.Command {
	opts := hub.NewOptions()

	cmd := &cobra.Command{
		Use:   "init",
		Short: "Initialize and start the kedge hub",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer cancel()

			server, err := hub.NewServer(opts)
			if err != nil {
				return fmt.Errorf("failed to create hub server: %w", err)
			}

			return server.Run(ctx)
		},
	}

	cmd.Flags().StringVar(&opts.DataDir, "data-dir", opts.DataDir, "Data directory for state")
	cmd.Flags().StringVar(&opts.Kubeconfig, "hub-kubeconfig", "", "Kubeconfig for hub cluster")
	cmd.Flags().StringVar(&opts.ListenAddr, "listen-addr", opts.ListenAddr, "Address to listen on")
	cmd.Flags().StringVar(&opts.ExternalKCPKubeconfig, "external-kcp", "", "Kubeconfig for external kcp")

	return cmd
}
