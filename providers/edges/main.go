// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// edges is the single, privileged, single-replica provider that owns the whole
// edge connectivity plane for BOTH connectable kinds — KubernetesCluster and
// LinuxServer, under one group edges.kedge.faros.sh. It terminates agent reverse
// tunnels (revdial) with one in-process ConnManager, runs the token/RBAC/
// lifecycle controllers per kind, and serves the k8s/ssh/mcp data-plane
// subresources. The tunnel Server dispatches by the resource segment in the URL
// path, so both kinds share one pod, one APIExport, one CatalogEntry.
//
// Routes (all behind the hub backend proxy at /services/providers/edges/*):
//
//   - /healthz                                          liveness/readiness gate
//   - /agent/{cluster}/apis/edges.kedge.faros.sh/v1alpha1/{kubernetesclusters|linuxservers}/{name}/proxy  agent control-tunnel ingress
//   - /agent/proxy?revdial.dialer=<id>                  agent revdial pickup ingress
//   - /edgeproxy/clusters/{cluster}/.../{name}/{k8s|ssh|mcp}  consumer egress
//
// IMPORTANT: this provider MUST run as a single replica — revdial registers
// dialers in a process-global map, so an agent's control connection and every
// later pickup connection must reach the same process.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/go-logr/logr"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog/v2"

	edgesv1alpha1 "github.com/faroshq/provider-edges/apis/v1alpha1"
	sdktunnel "github.com/faroshq/provider-edges/internal/tunnel"
	"github.com/faroshq/provider-edges/internal/svccatalog"
)

// providerPublicBase is the path prefix (behind the hub backend proxy) this
// provider is reachable at. Both the agent-ingress and consumer-egress mounts
// hang off it, and it is the prefix embedded into each edge's status.URL so CLI
// clients can reach the edgeproxy through the hub.
const providerPublicBase = "/services/providers/edges"

// agentPickupPath is the public revdial pickup path (behind the hub backend
// proxy) the agent re-enters through for this provider.
const agentPickupPath = providerPublicBase + "/agent/proxy"

// edgeProxyPublicPath is the public consumer-egress base for the k8s/ssh
// subresources, stamped into edge status.URL.
const edgeProxyPublicPath = providerPublicBase + "/edgeproxy"

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
			fmt.Fprintf(os.Stderr, "unknown subcommand: %s\nusage: edges-provider [init|serve]\n", os.Args[1])
			os.Exit(2)
		}
	}
	if err := runServe(); err != nil {
		fmt.Fprintln(os.Stderr, "serve:", err)
		os.Exit(1)
	}
}

func runServe() error {
	log := klog.Background().WithName("edges")

	port := os.Getenv("PORT")
	if port == "" {
		port = "8084"
	}

	mux := http.NewServeMux()

	// Health gates Ready=true in the hub via spec.backend.healthPath.
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// The provider's kcp credential (its provisioned SA kubeconfig), shared by
	// the tunnel server (token validation + Edge reads) and the edge controller
	// manager (Edge reconcilers across tenant workspaces).
	kcpConfig := loadKCPConfig(log)
	hubExternalURL := os.Getenv("KEDGE_HUB_EXTERNAL_URL")

	// Tunnel plane. The provider owns the ConnManager and terminates agent
	// reverse tunnels in-process (single-replica). Both prefixes sit behind the
	// hub backend proxy at /services/providers/edges/*.
	tsrv, err := sdktunnel.New(sdktunnel.Config{
		Kinds: []sdktunnel.KindConfig{
			{GVR: edgesv1alpha1.KubernetesClusterGVR, Kind: "KubernetesCluster"},
			{GVR: edgesv1alpha1.LinuxServerGVR, Kind: "LinuxServer"},
		},
		AgentPickupPath:     agentPickupPath,
		EdgeProxyPublicPath: edgeProxyPublicPath,
		KCPConfig:           kcpConfig,
		StaticTokens:        splitEnv(os.Getenv("KEDGE_STATIC_TOKENS")),
		HubExternalURL:      hubExternalURL,
		HubInternalURL:      os.Getenv("KEDGE_HUB_INTERNAL_URL"),
		Logger:              log,
	})
	if err != nil {
		return fmt.Errorf("build tunnel server: %w", err)
	}
	tsrv.Start(ctx.Done())

	// Edge controllers (token / RBAC / lifecycle) on the provider's own
	// APIExportEndpointSlice multicluster manager. Best-effort: a missing
	// kubeconfig just disables the manager (healthz + tunnel still serve).
	if cerr := startEdgeControllerManager(ctx, kcpConfig, tsrv,
		hubExternalURL, hubCAData(log), os.Getenv("KEDGE_DEV_MODE") == "true"); cerr != nil {
		if errors.Is(cerr, errControllerDisabled) {
			log.Info("edge controller manager disabled (no kcp kubeconfig)")
		} else {
			log.Error(cerr, "edge controller manager failed to start")
		}
	}

	// Agent ingress: control tunnel + revdial pickup. StripPrefix so the
	// handler sees /{cluster}/.../edges/{name}/proxy and /proxy.
	mux.Handle("/agent/", http.StripPrefix("/agent", tsrv.AgentIngressHandler()))
	// Consumer egress: k8s/ssh/mcp subresources on the Edge CR.
	mux.Handle("/edgeproxy/", http.StripPrefix("/edgeproxy", tsrv.EdgeProxyHandler()))
	// Provider aggregate MCP: the hub's MCP aggregate federates this endpoint
	// (POST tools/list with the caller's token + X-Kedge-Cluster). Exposes kube
	// tools across the tenant's connected KubernetesCluster edges AND the Home
	// Assistant tools of every Ready home-assistant EdgeService.
	mux.Handle("/mcp", tsrv.RootMCPHandler())

	// Service catalog: the UI-facing form schema for every service type
	// (svccatalog.All() — connection defaults, auth model + credential fields,
	// scheme-lock/host-required hints). The portal fetches this at
	// /services/providers/edges/catalog to render the add/configure-service form
	// from data, so it never drifts from the backend's auth/probe knowledge.
	mux.HandleFunc("/catalog", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-cache")
		if err := json.NewEncoder(w).Encode(svccatalog.All()); err != nil {
			log.Error(err, "encoding service catalog")
		}
	})

	// Provider portal micro-frontend (embedded Vite bundle). The hub proxies
	// /ui/providers/edges/* here; ProviderFrame injects <script src=".../main.js">
	// and mounts <kedge-provider-edges>. Serve /main.js, /assets/*, /icon.svg from
	// portal/dist with an index.html fallback. Best-effort: a missing/empty bundle
	// just disables the UI (healthz + tunnel still serve).
	if fileServer, distFS, perr := portalHandler(); perr != nil {
		log.Error(perr, "portal embed unavailable; provider UI disabled")
	} else {
		mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet && r.Method != http.MethodHead {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			if clean := strings.TrimPrefix(r.URL.Path, "/"); clean != "" {
				if servePortalAsset(w, r, distFS, clean) {
					return
				}
			}
			r2 := r.Clone(r.Context())
			r2.URL.Path = "/"
			fileServer.ServeHTTP(w, r2)
		})
	}

	// NOTE: no WriteTimeout / IdleTimeout — the agent control tunnel and
	// consumer streams are long-lived (revdial pings every 18s, 60s read
	// deadline). ReadHeaderTimeout only bounds the header phase.
	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		log.Info("edges provider listening", "port", port)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	go runHeartbeat(ctx, log)

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
// KEDGE_PROVIDER_KUBECONFIG, KUBECONFIG, in-cluster.
func loadKCPConfig(log logr.Logger) *rest.Config {
	if p := os.Getenv("KEDGE_PROVIDER_KUBECONFIG"); p != "" {
		if c, err := clientcmd.BuildConfigFromFlags("", p); err == nil {
			return c
		} else {
			log.Error(err, "KEDGE_PROVIDER_KUBECONFIG set but unusable")
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
	log.Info("no kcp kubeconfig available; tunnel token validation + Edge reads disabled (healthz only)")
	return nil
}

// hubCAData resolves the hub's CA bundle (PEM), embedded by the RBAC reconciler
// into the per-edge agent kubeconfig so agents trust the hub's serving cert.
// Source: KEDGE_HUB_CA_FILE (path) or KEDGE_HUB_CA_DATA (raw PEM). Best-effort:
// returns nil when neither is set (dev with insecure/skip-verify agents).
func hubCAData(log logr.Logger) []byte {
	if p := os.Getenv("KEDGE_HUB_CA_FILE"); p != "" {
		if b, err := os.ReadFile(p); err == nil {
			return b
		} else {
			log.Error(err, "KEDGE_HUB_CA_FILE set but unreadable")
		}
	}
	if d := os.Getenv("KEDGE_HUB_CA_DATA"); d != "" {
		return []byte(d)
	}
	return nil
}

// splitEnv splits a comma-separated env value into a trimmed, non-empty slice.
func splitEnv(v string) []string {
	if v == "" {
		return nil
	}
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
