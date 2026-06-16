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
	"net/url"
	"os"
	"path"
	"strings"

	"github.com/gorilla/mux"
	"golang.org/x/sync/errgroup"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"k8s.io/klog/v2"

	gatewayv1alpha1 "github.com/platform-mesh/kubernetes-graphql-gateway/apis/v1alpha1"
	gatewaygw "github.com/platform-mesh/kubernetes-graphql-gateway/gateway/gateway"
	"github.com/platform-mesh/kubernetes-graphql-gateway/gateway/gateway/authn"
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
// kcpConfig is the front-proxy-fronted kcp config used for everything: the
// listener's APIExportEndpointSlice watch and the gateway resolver clients. The
// front-proxy resolves the workspace path and routes to whichever shard hosts
// it (so it is multi-shard safe), and the shards accept the front-proxy client
// cert for the shard-direct VW endpoints advertised in the slice status.
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

	// Write a kubeconfig containing the front-proxy server endpoint, CA and
	// admin credentials so the listener's kcp-provider can locate the server and
	// perform startup discovery. Per-request authentication for GraphQL queries
	// is handled separately via utilscontext.SetToken (the user's bearer token).
	// The front-proxy resolves the APIExportEndpointSlice workspace path and
	// routes to the hosting shard, and reaches the shard-direct VW endpoints the
	// slice advertises (the shards trust the front-proxy client cert).
	kubeconfigPath, cleanup, err := writeKubeconfigTemp(kcpConfig)
	if err != nil {
		return fmt.Errorf("writing temp kubeconfig for GraphQL listener: %w", err)
	}

	// ── Listener ─────────────────────────────────────────────────────────────
	listenerOpts := listeneroptions.NewOptions()
	listenerOpts.Provider = "kcp"
	listenerOpts.SchemaHandler = "grpc"
	listenerOpts.Common.Kubeconfig = kubeconfigPath
	listenerOpts.GRPCListenAddr = grpcAddr
	// The default anchor (`namespaces.v1` / name=='default') doesn't work
	// against a kcp APIExport virtual workspace — the VW doesn't surface the
	// core/v1 group, so the resource controller's watch never starts. Use the
	// APIBinding for this APIExport as the anchor instead: every consumer has
	// exactly one such binding, so we reconcile once per consumer cluster.
	listenerOpts.ResourceGVR = "apibindings.v1alpha2.apis.kcp.io"
	listenerOpts.AnchorResource = fmt.Sprintf("object.spec.reference.export.name == %q", opts.GraphQLAPIExportSliceName)
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
	// Both schema discovery (ClusterURLResolverFunc) and runtime data resolution
	// (ClusterMetadataFunc) target the full workspace via the front-proxy
	// (<frontproxy>/clusters/<id>) — NOT the per-APIExport virtual workspace the
	// multicluster provider emits.
	//
	// Pointing schema discovery at the workspace endpoint means its aggregated
	// /openapi/v3 surfaces every bound API group (all APIBindings in the
	// workspace), so the generated GraphQL schema covers the whole workspace
	// rather than just the single APIExport behind GraphQLAPIExportSliceName.
	// (This relies on kcp surfacing bound CRD schemas in the workspace's
	// aggregated /openapi/v3.)
	//
	// The front-proxy (rather than a shard-direct host) lets kcp route to the
	// shard actually hosting each workspace: in a multi-shard topology a
	// shard-direct host yields empty discovery for cross-shard workspaces and
	// the gateway resolver then trips `no matches for kind`.
	frontProxyHost := kcpConfig.Host
	workspaceURL := func(clusterName string) (string, error) {
		parsed, err := url.Parse(frontProxyHost)
		if err != nil {
			return "", fmt.Errorf("parsing front-proxy host %q: %w", frontProxyHost, err)
		}
		parsed.Path = path.Join("/clusters", clusterName)
		return parsed.String(), nil
	}
	listenerCompleted.ClusterURLResolverFunc = func(_ string, clusterName string) (string, error) {
		return workspaceURL(clusterName)
	}
	listenerCompleted.ClusterMetadataFunc = func(clusterName string) (*gatewayv1alpha1.ClusterMetadata, error) {
		metadata, err := gatewayv1alpha1.BuildClusterMetadataFromConfig(kcpConfig)
		if err != nil {
			return nil, fmt.Errorf("building front-proxy cluster metadata: %w", err)
		}
		host, err := workspaceURL(clusterName)
		if err != nil {
			return nil, err
		}
		metadata.Host = host
		return metadata, nil
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
	// Upstream wires GRPCMaxRecvMsgSize directly into grpc.MaxCallRecvMsgSize,
	// so leaving it at zero rejects every message (incl. our ~750 KB kcp schemas).
	// 32 MB is comfortably above the largest schema we generate today.
	gatewayService, err := gatewaygw.New(gatewayconfig.Gateway{
		SchemaHandler:      "grpc",
		GRPCAddress:        grpcAddr,
		GRPCMaxRecvMsgSize: 32 * 1024 * 1024,
		GraphQL: gatewayconfig.GraphQL{
			Pretty:            true,
			PlaygroundEnabled: opts.GraphQLPlayground,
			GraphiQL:          opts.GraphQLPlayground,
		},
		Validator: authn.NoopValidator{},
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
			if token == "" {
				token = r.URL.Query().Get("token")
			}

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
