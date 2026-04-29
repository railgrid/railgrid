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

package hub

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/gorilla/mux"
	"golang.org/x/sync/errgroup"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"k8s.io/klog/v2"

	gatewaygw "github.com/platform-mesh/kubernetes-graphql-gateway/gateway/gateway"
	gatewayconfig "github.com/platform-mesh/kubernetes-graphql-gateway/gateway/gateway/config"
	utilscontext "github.com/platform-mesh/kubernetes-graphql-gateway/gateway/utils/context"
	"github.com/platform-mesh/kubernetes-graphql-gateway/listener"
	listeneroptions "github.com/platform-mesh/kubernetes-graphql-gateway/listener/options"
	kcplisteneroptions "github.com/platform-mesh/kubernetes-graphql-gateway/providers/kcp/options"
)

// startEmbeddedGraphQL starts the GraphQL listener and gateway in-process,
// registers the GraphQL handler on the provided router under
// /graphql/clusters/{clusterName}, and launches both goroutines into g.
//
// The listener watches the kcp APIExportEndpointSlice and pushes OpenAPI schemas
// over an in-process gRPC connection. The gateway subscribes to those schemas
// and serves GraphQL. No separate process or port is required; all requests
// arrive via the hub's own mux.
func startEmbeddedGraphQL(ctx context.Context, g *errgroup.Group, opts *Options, kcpConfig *rest.Config, router *mux.Router) error {
	logger := klog.FromContext(ctx)

	grpcAddr := opts.GraphQLGRPCAddr
	if grpcAddr == "" {
		grpcAddr = "localhost:50051"
	}

	// Write a kubeconfig containing only the kcp server endpoint and CA (no
	// credentials) so the listener and kcp-provider can locate the server.
	// Per-request authentication uses the user's own bearer token injected via
	// utilscontext.SetToken; admin credentials are intentionally excluded.
	kubeconfigPath, cleanup, err := writeKubeconfigTemp(kcpConfig)
	if err != nil {
		return fmt.Errorf("writing temp kubeconfig for GraphQL listener: %w", err)
	}

	// ── Listener ─────────────────────────────────────────────────────────────
	listenerOpts := listeneroptions.NewOptions()
	listenerOpts.Provider = "kcp"
	listenerOpts.SchemaHandler = "grpc"
	listenerOpts.KubeConfig = kubeconfigPath
	listenerOpts.GRPCListenAddr = grpcAddr
	listenerOpts.ProviderKcp = &kcplisteneroptions.Options{
		ExtraOptions: kcplisteneroptions.ExtraOptions{
			APIExportEndpointSliceName:           opts.GraphQLAPIExportSliceName,
			APIExportEndpointSliceLogicalCluster: opts.GraphQLAPIExportLogicalCluster,
			WorkspaceSchemaKubeconfigOverride:    kubeconfigPath,
		},
	}

	listenerCompleted, err := listenerOpts.Complete()
	if err != nil {
		cleanup()
		return fmt.Errorf("completing listener options: %w", err)
	}
	if err := listenerCompleted.Validate(); err != nil {
		cleanup()
		return fmt.Errorf("validating listener options: %w", err)
	}

	listenerCfg, err := listener.NewConfig(listenerCompleted)
	if err != nil {
		cleanup()
		return fmt.Errorf("creating listener config: %w", err)
	}

	listenerServer, err := listener.NewServer(ctx, listenerCfg)
	if err != nil {
		cleanup()
		return fmt.Errorf("creating listener server: %w", err)
	}

	// ── Gateway service ───────────────────────────────────────────────────────
	gatewayService, err := gatewaygw.New(gatewayconfig.Gateway{
		SchemaHandler: "grpc",
		GRPCAddress:   grpcAddr,
		GraphQL: gatewayconfig.GraphQL{
			Pretty:     true,
			Playground: opts.GraphQLPlayground,
			GraphiQL:   opts.GraphQLPlayground,
		},
	})
	if err != nil {
		cleanup()
		return fmt.Errorf("creating gateway service: %w", err)
	}

	// ── Mount on hub router ───────────────────────────────────────────────────
	// Gorilla mux prefix match; we extract clusterName from the URL ourselves
	// and inject it into context for gateway.Service.ServeHTTP.
	// External URL: /graphql/{clusterName}
	router.PathPrefix("/graphql/").Handler(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Extract cluster name from /graphql/{clusterName}[/...]
			rest := strings.TrimPrefix(r.URL.Path, "/graphql/")
			clusterName, _, _ := strings.Cut(rest, "/")

			token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")

			rctx := utilscontext.SetToken(r.Context(), token)
			rctx = utilscontext.SetCluster(rctx, clusterName)

			r = r.WithContext(rctx)
			// Gateway receives the path it needs internally.
			r.URL.Path = "/clusters/" + rest

			gatewayService.ServeHTTP(w, r)
		}),
	)

	logger.Info("Embedded GraphQL gateway mounted",
		"path", "/graphql/{clusterName}",
		"grpcAddr", grpcAddr,
		"playground", opts.GraphQLPlayground,
	)

	// Start listener and gateway service goroutines.
	g.Go(func() error {
		defer cleanup()
		return listenerServer.Run(ctx)
	})
	g.Go(func() error {
		return gatewayService.Run(ctx)
	})

	return nil
}

// writeKubeconfigTemp serialises kcpConfig as a kubeconfig to a temporary file
// and returns the path plus a cleanup function that removes it.
//
// Admin credentials from the rest.Config are preserved so the listener can
// perform startup discovery (APIExportEndpointSlice restmapping, server groups)
// against kcp. Per-request auth for GraphQL queries is handled separately via
// utilscontext.SetToken — the gateway uses the user's bearer token, not these
// credentials.
func writeKubeconfigTemp(cfg *rest.Config) (path string, cleanup func(), err error) {
	kubeConfig := clientcmdapi.NewConfig()
	kubeConfig.Clusters["default"] = &clientcmdapi.Cluster{
		Server:                   cfg.Host,
		CertificateAuthorityData: cfg.CAData,
		CertificateAuthority:     cfg.CAFile,
		InsecureSkipTLSVerify:    cfg.Insecure,
	}
	kubeConfig.AuthInfos["default"] = &clientcmdapi.AuthInfo{
		Token:                 cfg.BearerToken,
		TokenFile:             cfg.BearerTokenFile,
		ClientCertificate:     cfg.CertFile,
		ClientCertificateData: cfg.CertData,
		ClientKey:             cfg.KeyFile,
		ClientKeyData:         cfg.KeyData,
		Username:              cfg.Username,
		Password:              cfg.Password,
	}
	kubeConfig.Contexts["default"] = &clientcmdapi.Context{
		Cluster:  "default",
		AuthInfo: "default",
	}
	kubeConfig.CurrentContext = "default"

	data, err := clientcmd.Write(*kubeConfig)
	if err != nil {
		return "", func() {}, fmt.Errorf("serialising kubeconfig: %w", err)
	}

	f, err := os.CreateTemp("", "kedge-graphql-kubeconfig-*.yaml")
	if err != nil {
		return "", func() {}, fmt.Errorf("creating temp kubeconfig: %w", err)
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(f.Name())
		return "", func() {}, fmt.Errorf("writing temp kubeconfig: %w", err)
	}
	_ = f.Close()

	return f.Name(), func() { _ = os.Remove(f.Name()) }, nil
}
