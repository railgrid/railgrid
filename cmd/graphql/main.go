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

package main

import (
	"context"
	"fmt"
	"os"

	"github.com/platform-mesh/kubernetes-graphql-gateway/gateway"
	gatewayoptions "github.com/platform-mesh/kubernetes-graphql-gateway/gateway/options"
	"github.com/platform-mesh/kubernetes-graphql-gateway/listener"
	listeneroptions "github.com/platform-mesh/kubernetes-graphql-gateway/listener/options"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"golang.org/x/sync/errgroup"

	genericapiserver "k8s.io/apiserver/pkg/server"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
)

func main() {
	cmd := &cobra.Command{
		Use:   "kedge-graphql",
		Short: "Kubernetes GraphQL gateway",
	}

	cmd.AddCommand(newRunCmd())
	cmd.AddCommand(newListenerCmd())
	cmd.AddCommand(newGatewayCmd())

	if err := cmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	klog.Flush()
}

// newRunCmd runs listener and gateway together in a single process.
// Transport is always gRPC; provider is always kcp (kedge is kcp-based).
func newRunCmd() *cobra.Command {
	listenerOpts := listeneroptions.NewOptions()
	gatewayOpts := gatewayoptions.NewOptions()

	// Fixed: gRPC transport, kcp provider.
	listenerOpts.SchemaHandler = "grpc"
	listenerOpts.Provider = "kcp"
	gatewayOpts.SchemaHandler = "grpc"

	grpcAddr := listenerOpts.GRPCListenAddr // default ":50051"

	cmd := &cobra.Command{
		Use:   "run",
		Short: "Run listener and gateway together (gRPC transport, kcp provider)",
		RunE: func(cmd *cobra.Command, args []string) error {
			listenerOpts.GRPCListenAddr = grpcAddr
			gatewayOpts.GRPCListenerAddress = grpcAddr

			ctx := genericapiserver.SetupSignalContext()
			ctrl.SetLogger(zap.New(zap.UseDevMode(true)))
			log.SetLogger(klog.NewKlogr())

			g, ctx := errgroup.WithContext(ctx)
			g.Go(func() error { return runListener(ctx, listenerOpts) })
			g.Go(func() error { return runGateway(ctx, gatewayOpts) })
			return g.Wait()
		},
	}

	fs := pflag.NewFlagSet("run", pflag.ContinueOnError)

	fs.StringVar(&listenerOpts.KubeConfig, "kubeconfig", listenerOpts.KubeConfig,
		"path to kubeconfig (only required if out-of-cluster)")
	fs.StringVar(&grpcAddr, "grpc-addr", grpcAddr,
		"gRPC address the listener binds to and the gateway connects to")
	fs.IntVar(&gatewayOpts.ServerBindPort, "gateway-port", gatewayOpts.ServerBindPort,
		"port for the GraphQL gateway server")
	fs.StringVar(&gatewayOpts.ServerBindAddress, "gateway-address", gatewayOpts.ServerBindAddress,
		"address for the GraphQL gateway server")
	fs.BoolVar(&gatewayOpts.PlaygroundEnabled, "enable-playground", gatewayOpts.PlaygroundEnabled,
		"enable the GraphQL playground")
	fs.StringSliceVar(&gatewayOpts.CORSAllowedOrigins, "cors-allowed-origins", gatewayOpts.CORSAllowedOrigins,
		"list of allowed origins for CORS")
	fs.StringVar(&gatewayOpts.URLSuffix, "url-suffix", gatewayOpts.URLSuffix,
		"URL suffix for the GraphQL endpoint")
	fs.StringVar(&listenerOpts.ProviderKcp.APIExportEndpointSliceName, "apiexport-endpoint-slice-name",
		listenerOpts.ProviderKcp.APIExportEndpointSliceName,
		"name of the APIExport EndpointSlice to watch")
	fs.StringVar(&listenerOpts.ProviderKcp.APIExportEndpointSliceLogicalCluster, "apiexport-endpoint-slice-logicalcluster",
		listenerOpts.ProviderKcp.APIExportEndpointSliceLogicalCluster,
		"logical cluster path where the APIExportEndpointSlice lives, e.g. root:kedge:providers")
	fs.StringVar(&listenerOpts.ProviderKcp.WorkspaceSchemaKubeconfigOverride, "workspace-schema-kubeconfig-override",
		listenerOpts.ProviderKcp.WorkspaceSchemaKubeconfigOverride,
		"kubeconfig used by the gateway to proxy API calls (extracts CA + auth); use instead of --workspace-schema-host-override")
	fs.StringVar(&listenerOpts.ProviderKcp.WorkspaceSchemaHostOverride, "workspace-schema-host-override",
		listenerOpts.ProviderKcp.WorkspaceSchemaHostOverride,
		"host override for workspace schema generation (overrides kubeconfig host, but loses CA/auth)")

	cmd.Flags().AddFlagSet(fs)
	return cmd
}

func newListenerCmd() *cobra.Command {
	opts := listeneroptions.NewOptions()
	fs := pflag.NewFlagSet("listener", pflag.ContinueOnError)
	opts.AddFlags(fs)

	cmd := &cobra.Command{
		Use:   "listener",
		Short: "Watch a Kubernetes cluster and write OpenAPI schemas",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := genericapiserver.SetupSignalContext()
			ctrl.SetLogger(zap.New(zap.UseDevMode(true)))
			log.SetLogger(klog.NewKlogr())
			return runListener(ctx, opts)
		},
	}
	cmd.Flags().AddFlagSet(fs)
	return cmd
}

func newGatewayCmd() *cobra.Command {
	opts := gatewayoptions.NewOptions()
	fs := pflag.NewFlagSet("gateway", pflag.ContinueOnError)
	opts.AddFlags(fs)

	cmd := &cobra.Command{
		Use:   "gateway",
		Short: "Serve Kubernetes OpenAPI schemas as GraphQL endpoints",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := genericapiserver.SetupSignalContext()
			ctrl.SetLogger(zap.New(zap.UseDevMode(true)))
			log.SetLogger(klog.NewKlogr())
			return runGateway(ctx, opts)
		},
	}
	cmd.Flags().AddFlagSet(fs)
	return cmd
}

func runListener(ctx context.Context, opts *listeneroptions.Options) error {
	completed, err := opts.Complete()
	if err != nil {
		return err
	}
	if err := completed.Validate(); err != nil {
		return err
	}
	config, err := listener.NewConfig(completed)
	if err != nil {
		return err
	}
	server, err := listener.NewServer(ctx, config)
	if err != nil {
		return err
	}
	if err := server.Run(ctx); err != nil {
		return err
	}
	<-ctx.Done()
	return nil
}

func runGateway(ctx context.Context, opts *gatewayoptions.Options) error {
	completed, err := opts.Complete()
	if err != nil {
		return err
	}
	if err := completed.Validate(); err != nil {
		return err
	}
	config, err := gateway.NewConfig(completed)
	if err != nil {
		return err
	}
	server, err := gateway.NewServer(config)
	if err != nil {
		return err
	}
	if err := server.Run(ctx); err != nil {
		return err
	}
	<-ctx.Done()
	return nil
}
