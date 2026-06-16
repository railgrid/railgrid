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
	"time"

	"github.com/gorilla/mux"
	"golang.org/x/sync/errgroup"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/dynamic"
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

	"github.com/faroshq/faros-kedge/pkg/apiurl"
)

// startEmbeddedGraphQL starts the GraphQL listener and gateway in-process,
// registers the GraphQL handler on the provided router under
// /graphql/clusters/{clusterName}, and launches both goroutines into g.
//
// kcpFrontProxyConfig is the front-proxy-fronted kcp config; gateway resolver
// clients use this host so per-request workspace traffic is routed to the
// correct shard. kcpShardConfig is the shard-direct config; the listener uses
// it because the APIExport endpoint slice advertises shard-direct VW URLs that
// only succeed when reached from a shard-direct kubeconfig.
//
// The listener watches the kcp APIExportEndpointSlice and pushes OpenAPI schemas
// over an in-process gRPC connection. The gateway subscribes to those schemas
// and serves GraphQL. No separate process or port is required; all requests
// arrive via the hub's own mux.
func startEmbeddedGraphQL(ctx context.Context, g *errgroup.Group, opts *Options, kcpFrontProxyConfig, kcpShardConfig *rest.Config, router *mux.Router) error {
	logger := klog.FromContext(ctx)

	grpcAddr := opts.GraphQLGRPCAddr
	if grpcAddr == "" {
		grpcAddr = "localhost:50051"
	}

	// Write a kubeconfig containing only the kcp server endpoint and CA (no
	// credentials) so the listener and kcp-provider can locate the server.
	// Per-request authentication uses the user's own bearer token injected via
	// utilscontext.SetToken; admin credentials are intentionally excluded.
	// The listener side targets shard-direct URLs so the APIExportEndpointSlice
	// endpoints (which advertise shard-direct VW hosts) are reachable.
	kubeconfigPath, cleanup, err := writeKubeconfigTemp(kcpShardConfig)
	if err != nil {
		return fmt.Errorf("writing temp kubeconfig for GraphQL listener: %w", err)
	}

	// Resolve the configured APIExportEndpointSlice workspace PATH
	// (e.g. root:kedge:system:controllers) to its logical-cluster ID via the
	// front-proxy. The listener's multicluster provider builds its slice cache
	// over kcpShardConfig (shard-direct, because the slice advertises
	// shard-direct VW URLs that reject the front-proxy client cert). But shards
	// only resolve /clusters/<id> — workspace PATHS are a front-proxy-only
	// concept, so a path here makes shard discovery 404 ("could not find the
	// requested resource"). IDs resolve on both the front-proxy and shards, so
	// we always hand the listener the ID.
	logicalCluster := opts.GraphQLAPIExportLogicalCluster
	if id, rerr := resolveLogicalClusterID(ctx, kcpFrontProxyConfig, logicalCluster); rerr != nil {
		cleanup()
		return fmt.Errorf("resolving logical-cluster ID for %q: %w", logicalCluster, rerr)
	} else {
		logger.Info("Resolved APIExportEndpointSlice workspace to logical-cluster ID", "path", logicalCluster, "id", id)
		logicalCluster = id
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
			APIExportEndpointSliceLogicalCluster: logicalCluster,
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
	frontProxyHost := kcpFrontProxyConfig.Host
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
		metadata, err := gatewayv1alpha1.BuildClusterMetadataFromConfig(kcpFrontProxyConfig)
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

// logicalClusterGVR identifies the kcp LogicalCluster singleton ("cluster")
// present in every workspace; its kcp.io/cluster annotation holds the
// workspace's logical-cluster ID.
var logicalClusterGVR = schema.GroupVersionResource{
	Group: "core.kcp.io", Version: "v1alpha1", Resource: "logicalclusters",
}

// clusterAnnotation is the kcp-managed annotation carrying an object's
// logical-cluster ID (the shard-resolvable cluster name).
const clusterAnnotation = "kcp.io/cluster"

// resolveLogicalClusterID translates a workspace path (e.g.
// root:kedge:system:controllers) to its logical-cluster ID by reading the
// LogicalCluster singleton through the front-proxy config (only the front-proxy
// resolves paths). If clusterPath is already an ID (no ":" separator) it is
// returned unchanged. The LogicalCluster's kcp.io/cluster annotation is set
// asynchronously after workspace creation, so we poll briefly.
func resolveLogicalClusterID(ctx context.Context, frontProxyConfig *rest.Config, clusterPath string) (string, error) {
	if !strings.Contains(clusterPath, ":") {
		// Not a path — assume the caller already supplied an ID.
		return clusterPath, nil
	}

	cfg := rest.CopyConfig(frontProxyConfig)
	cfg.Host = apiurl.KCPClusterURL(cfg.Host, clusterPath)
	client, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return "", fmt.Errorf("creating dynamic client: %w", err)
	}

	var id string
	if err := wait.PollUntilContextTimeout(ctx, 2*time.Second, time.Minute, true, func(ctx context.Context) (bool, error) {
		lc, getErr := client.Resource(logicalClusterGVR).Get(ctx, "cluster", metav1.GetOptions{})
		if getErr != nil {
			klog.FromContext(ctx).V(4).Info("LogicalCluster not yet readable, retrying", "path", clusterPath, "err", getErr)
			return false, nil
		}
		id = lc.GetAnnotations()[clusterAnnotation]
		return id != "", nil
	}); err != nil {
		return "", fmt.Errorf("waiting for %s annotation on LogicalCluster: %w", clusterAnnotation, err)
	}
	return id, nil
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
