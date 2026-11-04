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

// railgrid-kcp serves external shards with the same organization boundary as
// embedded kcp, using the repository's existing dependency and exported hooks.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/kcp-dev/embeddedetcd"
	kcpfeatures "github.com/kcp-dev/kcp/pkg/features"
	"github.com/kcp-dev/kcp/pkg/server"
	serveroptions "github.com/kcp-dev/kcp/pkg/server/options"
	"github.com/spf13/cobra"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	utilfeature "k8s.io/apiserver/pkg/util/feature"
	cliflag "k8s.io/component-base/cli/flag"

	"github.com/railgrid/railgrid/pkg/hub/kcp/orgboundary"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	if err := newCommand(rootDirectory(os.Args[1:])).ExecuteContext(ctx); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// Initialize path-dependent upstream defaults from the same root the flag
// parser will select, as the upstream kcp command does.
func rootDirectory(args []string) string {
	root := ".kcp"
	for index, arg := range args {
		if value, ok := strings.CutPrefix(arg, "--root-directory="); ok {
			root = value
		} else if arg == "--root-directory" && index+1 < len(args) {
			root = args[index+1]
		}
	}
	return root
}

func newCommand(root string) *cobra.Command {
	opts := serveroptions.NewOptions(root)
	opts.Extra.BatteriesIncluded = []string{"admin", "user"}
	command := &cobra.Command{Use: "railgrid-kcp", Short: "kcp with Railgrid organization isolation", SilenceUsage: true, SilenceErrors: true}
	start := &cobra.Command{
		Use: "start", Short: "Start a shard with native organization enforcement", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := utilfeature.DefaultMutableFeatureGate.Set(fmt.Sprintf("%s=true,%s=true", kcpfeatures.CacheAPIs, kcpfeatures.WorkspaceMounts)); err != nil {
				return err
			}
			completed, err := opts.Complete(cmd.Context(), root)
			if err != nil {
				return err
			}
			if err := utilerrors.NewAggregate(completed.Validate()); err != nil {
				return err
			}
			config, err := server.NewConfig(cmd.Context(), *completed)
			if err != nil {
				return err
			}
			if err := orgboundary.Install(config); err != nil {
				return err
			}
			ready, err := config.Complete()
			if err != nil {
				return err
			}
			// Keep etcd alive through the API server's graceful shutdown.
			etcdContext, stopEtcd := context.WithCancel(context.Background())
			defer stopEtcd()
			if ready.EmbeddedEtcd.Config != nil {
				if err := embeddedetcd.NewServer(ready.EmbeddedEtcd).Run(etcdContext); err != nil {
					return err
				}
			}
			shard, err := server.NewServer(ready)
			if err != nil {
				return err
			}
			return shard.Run(cmd.Context())
		},
	}
	sets := &cliflag.NamedFlagSets{}
	opts.AddFlags(sets)
	for _, flags := range sets.FlagSets {
		start.Flags().AddFlagSet(flags)
	}
	start.Flags().StringVar(&root, "root-directory", root, "Directory holding this shard's configuration and state")
	command.AddCommand(start)
	return command
}
