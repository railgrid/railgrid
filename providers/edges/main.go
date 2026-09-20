// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// edges is the single, privileged provider that owns the whole edge
// connectivity plane for KubernetesCluster, LinuxServer, and MacOSServer under
// one group edges.railgrid.ai. It terminates agent reverse
// tunnels (revdial) with one in-process ConnManager, runs the token/RBAC/
// lifecycle controllers per kind, and serves the k8s/ssh/mcp data-plane
// subresources. The tunnel Server dispatches by the resource segment in the URL
// path, so both kinds share one pod, one APIExport, one CatalogEntry.
//
// Routes (all behind the hub backend proxy at /services/providers/edges/*).
// The whole surface is built by provider-sdk/serve, which takes one handler
// per Pillar 2 route class and refuses anything that is not one:
//
//   - /healthz, /readyz                                 (c) liveness, readiness
//   - /mcp, /mcp/sse                                    (b) provider MCP projection
//   - /dataplane/clusters/{cluster}/{resource}/{name}/{verb}[/{tail}]
//     (a) consumer egress: k8s | ssh | mcp | proxy | ticket
//   - /agent/clusters/{cluster}/{resource}/{name}/proxy (f) agent control tunnel
//   - /agent/proxy?revdial.dialer=<id>                  (f) revdial pickup (single-replica)
//   - /agent/proxy/{replica}?revdial.dialer=<id>        (f) replica-addressed pickup
//   - everything else                                   the portal bundle
//
// Multi-replica: revdial dialers stay process-local (the dialer closes over
// the accepted socket), but each agent dials only ONE replica. The replica
// terminating a tunnel claims it in a Lease registry in the provider
// workspace, and the other replicas relay pickups and data-plane requests to
// the owner over the internal listener (EDGES_INTERNAL_PORT, pod-to-pod
// only). See internal/tunnel/{registry,remote}.go.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/go-logr/logr"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog/v2"

	edgesv1alpha1 "github.com/railgrid/provider-edges/apis/v1alpha1"
	sdktunnel "github.com/railgrid/provider-edges/internal/tunnel"
	"github.com/railgrid/provider-sdk/hubclient"
	"github.com/railgrid/provider-sdk/identityclient"
	"github.com/railgrid/provider-sdk/serve"
	"github.com/railgrid/provider-sdk/vwhealth"
)

// heartbeatVersion is reported to the hub; align with manifest.yaml spec.version.
const heartbeatVersion = "0.1.0"

// providerPublicBase is the path prefix (behind the hub backend proxy) this
// provider is reachable at. Both the agent-ingress and consumer-egress mounts
// hang off it, and it is the prefix embedded into each edge's status.URL so CLI
// clients can reach the edgeproxy through the hub.
const providerPublicBase = "/services/providers/edges"

// agentPickupPath is the public revdial pickup path (behind the hub backend
// proxy) the agent re-enters through for this provider.
const agentPickupPath = providerPublicBase + "/" + sdktunnel.AgentRoot + "/proxy"

// edgeProxyPublicPath is the public consumer-egress base, stamped into edge
// and Service status.URL. It is the shared class (a) root: the provider-private
// "/edgeproxy" mount is gone, because provider-sdk/serve mounts the data plane
// at /dataplane/ and refuses to register anything else.
const edgeProxyPublicPath = providerPublicBase + "/" + sdktunnel.DataPlaneRoot

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "init":
			ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
			defer stop()
			if err := runInitCmd(ctx); err != nil {
				fmt.Fprintln(os.Stderr, "init:", err)
				os.Exit(1)
			}
			return
		case "serve":
			// fall through
		default:
			fmt.Fprintf(os.Stderr, "unknown subcommand: %s\nusage: edges-provider [init|serve [--allow-unverified-ssh-host-key]]\n", os.Args[1])
			os.Exit(2)
		}
	}
	var serveArgs []string
	if len(os.Args) > 2 {
		serveArgs = os.Args[2:]
	}
	opts, err := parseServeOptions(serveArgs)
	if err != nil {
		fmt.Fprintln(os.Stderr, "serve:", err)
		os.Exit(2)
	}
	if err := runServe(opts); err != nil {
		fmt.Fprintln(os.Stderr, "serve:", err)
		os.Exit(1)
	}
}

// serveOptions are the serve-time switches. Each has a flag and an env form
// (the chart sets the env); either enables it.
type serveOptions struct {
	// allowUnverifiedSSHHostKey is the legacy escape hatch for LinuxServers
	// whose agents never reported an sshd host key: SSH sessions to them are
	// opened without verifying the server. Edges with a known or pinned key
	// are always verified regardless.
	allowUnverifiedSSHHostKey bool
}

// allowUnverifiedEnvVar is the env form of --allow-unverified-ssh-host-key
// (the chart sets it).
const allowUnverifiedEnvVar = "RAILGRID_EDGES_ALLOW_UNVERIFIED_SSH_HOST_KEY"

func parseServeOptions(args []string) (serveOptions, error) {
	var opts serveOptions
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.BoolVar(&opts.allowUnverifiedSSHHostKey, "allow-unverified-ssh-host-key", false,
		"open SSH sessions to LinuxServers with no known host key without verifying the server (legacy escape hatch; env "+allowUnverifiedEnvVar+"=true)")
	if err := fs.Parse(args); err != nil {
		return opts, err
	}
	// A malformed value is refused rather than silently read as false: this
	// switch decides whether SSH sessions verify the server at all, and a typo
	// ("treu") must not quietly land on a different security posture than the
	// operator asked for — in either direction.
	if raw, set := os.LookupEnv(allowUnverifiedEnvVar); set && raw != "" {
		v, err := strconv.ParseBool(raw)
		if err != nil {
			return opts, fmt.Errorf("%s=%q is not a boolean: %w (use true or false)", allowUnverifiedEnvVar, raw, err)
		}
		if v {
			opts.allowUnverifiedSSHHostKey = true
		}
	}
	return opts, nil
}

func runServe(opts serveOptions) error {
	log := klog.Background().WithName("edges")

	if opts.allowUnverifiedSSHHostKey {
		log.Info("WARNING: --allow-unverified-ssh-host-key is set: SSH sessions to LinuxServers with no known host key will NOT verify the server (MITM risk); pin spec.sshHostKey or let agents report keys, then remove the flag")
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "8088"
	}

	// vwState is this provider's readiness: can THIS process reach the
	// APIExport virtual workspace it watches, and — while this replica holds
	// the controller lease — is the manager actually watching tenant
	// workspaces. It backs /readyz, which the CatalogEntry points
	// spec.backend.healthPath at, and it gates the heartbeat.
	//
	// /healthz stays unconditional and is NOT this: a provider whose watches
	// are dead is alive and must not be restarted, it must stop claiming to
	// be ready.
	vwState := &vwhealth.Readiness{}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// The provider's kcp credential (its provisioned SA kubeconfig), shared by
	// the tunnel server (token validation + Edge reads) and the edge controller
	// manager (Edge reconcilers across tenant workspaces).
	kcpConfig := loadKCPConfig(log)
	hubExternalURL := os.Getenv("RAILGRID_HUB_EXTERNAL_URL")
	hubCA := hubCAData(log)

	// RAILGRID_STATIC_TOKENS used to let listed bearers skip TokenReview/SAR on
	// every data-plane path. The bypass is gone: kcp validates every token, and
	// hub static-token users are ordinary kcp identities that pass that check.
	// A set value with a kcp credential present is a misconfiguration that
	// would silently expect the old behaviour, so refuse to start rather than
	// run with a different security posture than the operator assumed.
	if v := os.Getenv("RAILGRID_STATIC_TOKENS"); v != "" {
		if kcpConfig != nil {
			err := errors.New("RAILGRID_STATIC_TOKENS is set but the static-token authorization bypass has been removed; every token is validated by kcp, so unset the variable")
			log.Error(err, "refusing to start")
			return err
		}
		log.Info("static-token authorization bypass has been removed; the environment variable is ignored",
			"envVar", "RAILGRID_STATIC_TOKENS", "ignored", true, "severity", "warning")
	}

	// The hub scoped-identity client: how this provider obtains an edge
	// agent's credential instead of minting one. Best-effort at startup — a
	// dev run with no hub URL simply has no identity client, and the join path
	// then hands out no credential rather than falling back to minting a
	// permanent one itself.
	identities, ierr := identityclient.New(identityclient.Options{Provider: "edges"})
	if ierr != nil {
		log.Error(ierr, "hub identity service unavailable; edge agents will not be issued credentials",
			"consequence", "an enrolling agent keeps its join token and retries")
		identities = nil
	}

	// Tunnel plane. The provider owns the ConnManager and terminates agent
	// reverse tunnels in-process; with replica routing enabled below, peer
	// replicas relay to whichever replica holds a tunnel. Both prefixes sit
	// behind the hub backend proxy at /services/providers/edges/*.
	tsrv, err := sdktunnel.New(sdktunnel.Config{
		Kinds: []sdktunnel.KindConfig{
			{GVR: edgesv1alpha1.KubernetesClusterGVR, Kind: "KubernetesCluster"},
			{GVR: edgesv1alpha1.LinuxServerGVR, Kind: "LinuxServer"},
			{GVR: edgesv1alpha1.MacOSServerGVR, Kind: "MacOSServer"},
		},
		AgentPickupPath:           agentPickupPath,
		EdgeProxyPublicPath:       edgeProxyPublicPath,
		KCPConfig:                 kcpConfig,
		HubExternalURL:            hubExternalURL,
		HubInternalURL:            os.Getenv("RAILGRID_HUB_INTERNAL_URL"),
		HubCAData:                 hubCA,
		AllowUnverifiedSSHHostKey: opts.allowUnverifiedSSHHostKey,
		Logger:                    log,
	})
	if err != nil {
		return fmt.Errorf("build tunnel server: %w", err)
	}
	tsrv.SetIdentityClient(identities)

	// Tunnel Lease registry + multi-replica routing. The registry is always on
	// when a kcp credential exists: the replica terminating a tunnel claims it
	// as a Lease in the provider workspace and renews it while the socket
	// lives, and the edge lifecycle reconciler derives status.connected /
	// phase / lastHeartbeatTime from those Leases — the tunnel handler never
	// writes connectivity to Edge status itself. Routing on top of it (each
	// agent dials ONE replica; every other replica relays pickups and
	// data-plane requests to the owner over the internal listener — pod-to-pod
	// only, its port is not on the Service) additionally requires POD_IP
	// (downward API) and a bearer-token provider credential; without them the
	// provider runs single-replica with the registry as bookkeeping only.
	if kcpConfig != nil {
		replicaID := sdktunnel.SanitizeReplicaID(envOrHostname("POD_NAME"))
		podIP := os.Getenv("POD_IP")
		internalPort := os.Getenv("EDGES_INTERNAL_PORT")
		if internalPort == "" {
			internalPort = "8090"
		}
		routing := true
		switch {
		case podIP == "":
			log.Info("replica tunnel routing disabled (POD_IP unset); single-replica mode")
			routing = false
		case kcpConfig.BearerToken == "":
			log.Info("replica tunnel routing disabled (provider credential has no bearer token to authenticate the relay); single-replica mode")
			routing = false
		}
		// The lease holder identity is the relay address when routing, else a
		// non-routable placeholder that still identifies the replica.
		selfAddr := "local/" + replicaID
		if routing {
			selfAddr = podIP + ":" + internalPort
		}
		reg, rerr := sdktunnel.NewRegistry(kcpConfig, replicaID, selfAddr)
		switch {
		case rerr != nil:
			log.Error(rerr, "tunnel lease registry unavailable; edges will not report Connected", "replica", replicaID)
		case !routing:
			tsrv.EnableRegistry(reg)
		default:
			tsrv.EnableReplicaRouting(reg, kcpConfig.BearerToken)
			internalSrv := &http.Server{
				Addr:              ":" + internalPort,
				Handler:           tsrv.InternalHandler(),
				ReadHeaderTimeout: 10 * time.Second,
			}
			go func() {
				log.Info("edges internal listener (relay + forwarded pickups)", "port", internalPort, "replica", replicaID)
				if lerr := internalSrv.ListenAndServe(); lerr != nil && !errors.Is(lerr, http.ErrServerClosed) {
					log.Error(lerr, "internal listener failed; peer replicas cannot relay to this one")
				}
			}()
			go func() {
				<-ctx.Done()
				shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				_ = internalSrv.Shutdown(shutdownCtx)
			}()
		}
	}
	tsrv.Start(ctx.Done())

	// Edge controllers (token / RBAC / lifecycle) on the provider's own
	// APIExportEndpointSlice multicluster manager. Best-effort: a missing
	// kubeconfig just disables the manager (healthz + tunnel still serve).
	if cerr := startEdgeControllerManager(ctx, kcpConfig, tsrv,
		hubExternalURL, hubCA, os.Getenv("RAILGRID_DEV_MODE") == "true", identities, vwState); cerr != nil {
		if errors.Is(cerr, errControllerDisabled) {
			log.Info("edge controller manager disabled (no kcp kubeconfig)")
		} else {
			log.Error(cerr, "edge controller manager failed to start")
		}
	}

	// Probe the advertised virtual-workspace URL periodically, so an
	// unreachable VW (the silent failure vwhealth exists for) shows up on
	// /readyz instead of only in a watch log nobody reads. A nil kcpConfig
	// makes this a no-op.
	go vwhealth.Watch(ctx, kcpConfig, apiExportName, vwState, vwhealth.DefaultInterval)

	// The whole HTTP surface, one handler per Pillar 2 route class.
	// provider-sdk/serve refuses anything that is not a class — there is no
	// /api/*, no unauthenticated /catalog, and no provider-private root: the
	// consumer data plane is class (a) at /dataplane/ and the agent tunnel is
	// class (f) at /agent/. Both receive the path EXACTLY as the caller sent
	// it, so dataplane.ParseRequest — not an http.ServeMux — decides what
	// ".." and "//" mean.
	serveOpts := serve.Options{
		Name:      "edges",
		Readiness: vwhealth.Handler(vwState),
		// (b) Provider aggregate MCP: the hub's MCP aggregate federates this
		// endpoint (POST tools/list with the caller's token +
		// X-Railgrid-Cluster). Exposes kube tools across the tenant's connected
		// KubernetesCluster edges AND the tools of every Ready Service.
		MCP: tsrv.RootMCPHandler(),
		// (a) Consumer egress: the declared verbs on the edge kinds and on
		// published Services.
		DataPlane: tsrv.EdgeProxyHandler(),
		// (f) Agent ingress: control tunnel + revdial pickup.
		Extra: []serve.Route{{
			Prefix:  serve.AgentPrefix,
			Class:   serve.ClassAgentTunnel,
			Handler: tsrv.AgentIngressHandler(),
		}},
		Logger: log,
	}
	// Provider portal micro-frontend (embedded Vite bundle). The hub proxies
	// /ui/providers/edges/* here; ProviderFrame injects
	// <script src=".../main.js"> and mounts <railgrid-provider-edges>.
	// Best-effort: a missing or empty bundle just disables the UI.
	if distFS, perr := portalDist(); perr != nil {
		log.Error(perr, "portal embed unavailable; provider UI disabled")
	} else {
		serveOpts.Portal = distFS
	}

	handler, err := serve.New(serveOpts)
	if err != nil {
		return fmt.Errorf("build server: %w", err)
	}

	// NOTE: no WriteTimeout / IdleTimeout — the agent control tunnel and
	// consumer streams are long-lived (revdial pings every 18s, 60s read
	// deadline). ReadHeaderTimeout only bounds the header phase.
	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
	}

	listener, err := net.Listen("tcp", srv.Addr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", srv.Addr, err)
	}

	errCh := make(chan error, 1)
	go func() {
		log.Info("edges provider listening", "port", port)
		if err := srv.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	hb, err := hubclient.ConfigFromEnv("edges", heartbeatVersion)
	if err != nil {
		log.Error(err, "resolving heartbeat token; beats will be unauthenticated")
	}
	hb.Logger = log
	// The hub records any beat it receives as liveness and ignores the body's
	// status, so the beat must stop the moment the provider stops being able
	// to do its job — the same answer /readyz gives. A flag set once at
	// startup, which this used to be, cannot notice the controllers dying.
	hb.CanSend = func() bool { return vwState.Check() == nil }
	go hubclient.RunHeartbeat(ctx, hb)

	select {
	case <-ctx.Done():
	case err := <-errCh:
		return err
	}
	log.Info("shutting down")
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return srv.Shutdown(shutdown)
}

// loadKCPConfig resolves the provider's kcp credential (its provisioned SA
// kubeconfig) for token validation and Edge reads/writes. Best-effort: returns
// nil (with a warning) when no kubeconfig is available, so the binary still
// serves /healthz in environments where kcp isn't wired yet. Resolution order:
// RAILGRID_PROVIDER_KUBECONFIG, KUBECONFIG, in-cluster; note that a
// RAILGRID_PROVIDER_KUBECONFIG that is set but unusable falls through the same
// chain and can end in nil.
//
// A nil result does NOT unmount the data plane: the tunnel handlers are always
// mounted and instead refuse every consumer-egress request with 503, because
// there is no kcp credential to authorize bearers against (see
// tunnel.Server.denyIfAuthorizationUnavailable).
func loadKCPConfig(log logr.Logger) *rest.Config {
	if p := os.Getenv("RAILGRID_PROVIDER_KUBECONFIG"); p != "" {
		if c, err := clientcmd.BuildConfigFromFlags("", p); err == nil {
			return c
		} else {
			log.Error(err, "RAILGRID_PROVIDER_KUBECONFIG set but unusable")
		}
	}
	if p := os.Getenv("KUBECONFIG"); p != "" {
		if c, err := clientcmd.BuildConfigFromFlags("", p); err == nil {
			return c
		}
	}
	if c, err := rest.InClusterConfig(); err == nil {
		return c
	}
	log.Info("no kcp kubeconfig available; edge controllers are disabled and the tunnel data plane refuses every request with 503 (delegated authorization unavailable) - only /healthz and the static portal serve")
	return nil
}

// hubCAData resolves the hub's CA bundle (PEM), embedded by the RBAC reconciler
// into the per-edge agent kubeconfig so agents trust the hub's serving cert.
// Source: RAILGRID_HUB_CA_FILE (path) or RAILGRID_HUB_CA_DATA (raw PEM). Best-effort:
// returns nil when neither is set (dev with insecure/skip-verify agents).
func hubCAData(log logr.Logger) []byte {
	if p := os.Getenv("RAILGRID_HUB_CA_FILE"); p != "" {
		if b, err := os.ReadFile(p); err == nil {
			return b
		} else {
			log.Error(err, "RAILGRID_HUB_CA_FILE set but unreadable")
		}
	}
	if d := os.Getenv("RAILGRID_HUB_CA_DATA"); d != "" {
		return []byte(d)
	}
	return nil
}

// envOrHostname returns the named env var (the chart sets POD_NAME via the
// downward API) falling back to the hostname — which in a pod is the pod
// name anyway.
func envOrHostname(key string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	host, err := os.Hostname()
	if err != nil {
		return "replica"
	}
	return host
}
