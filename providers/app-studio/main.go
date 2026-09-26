// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

// app-studio is the runtime for the App Studio provider. It serves the project
// REST + LLM API, the embedded App Studio portal, and the provider
// liveness/readiness endpoints from a single port, and keeps the hub heartbeat
// alive.
//
// Two surfaces share the port, split only by URL:
//
//   - /, /main.js, /icon.svg, /assets/* — the portal micro-frontend (Vite
//     build embedded via portal/dist, see assets.go). Mounted by the hub under
//     /ui/providers/app-studio/.
//   - /clusters/{id}/apis/ai.railgrid.ai/v1alpha1/{resource}/{name}/{verb} —
//     the data-plane verbs, each a kcp custom subresource on this provider's
//     APIExport. A kcp shard authorizes the caller with RBAC and reverse-proxies
//     the request here (through the DataPlaneEndpointSlice) with the caller's
//     identity stamped in X-Remote-* headers; there is no hub-proxied grammar
//     and no caller bearer. Plus /healthz (liveness) and /readyz
//     (controller-aware readiness).
package main

import (
	"context"
	"errors"
	"fmt"
	"github.com/railgrid/provider-sdk/dataplane"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/railgrid/provider-app-studio/api"
	"github.com/railgrid/provider-app-studio/store"
	"github.com/railgrid/provider-app-studio/tenant"
	"github.com/railgrid/provider-app-studio/workspace"
	"github.com/railgrid/provider-sdk/hubclient"
	"github.com/railgrid/provider-sdk/serve"
	"github.com/railgrid/provider-sdk/vwhealth"
)

// heartbeatVersion is reported to the hub; align with manifest.yaml spec.version.
const heartbeatVersion = "0.1.0"

// replicaIdentityFromEnv is the durable-claim identity: the pod name (chart
// downward API) plus the pid, so a container restart inside the same pod gets
// a fresh identity and its predecessor's claims age out.
func replicaIdentityFromEnv() string {
	host := os.Getenv("POD_NAME")
	if host == "" {
		if h, err := os.Hostname(); err == nil {
			host = h
		} else {
			host = "replica"
		}
	}
	return fmt.Sprintf("%s_%d", host, os.Getpid())
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func previewBridgeEnvironmentConfig() (bool, string, string) {
	enabled := !strings.EqualFold(strings.TrimSpace(os.Getenv("APP_STUDIO_PREVIEW_BRIDGE_ENABLED")), "false")
	privateKey := strings.TrimSpace(os.Getenv("APP_STUDIO_PREVIEW_BRIDGE_SIGNING_KEY"))
	keyID := strings.TrimSpace(os.Getenv("APP_STUDIO_PREVIEW_BRIDGE_SIGNING_KEY_ID"))
	if enabled && privateKey == "" && keyID == "" {
		log.Print("preview bridge requested but unavailable: signing key and key ID are not configured")
		enabled = false
	}
	return enabled, privateKey, keyID
}

// loadProviderConfig loads the provider kubeconfig used for the kcp front-proxy
// host + TLS only (the tenant client drops its credential and authenticates as
// the caller). Resolution order matches the other providers.
func loadProviderConfig() (*rest.Config, error) {
	candidates := []string{
		os.Getenv("RAILGRID_PROVIDER_KUBECONFIG"),
		"/var/run/secrets/railgrid/railgrid-provider-kubeconfig",
		os.Getenv("KUBECONFIG"),
	}
	for _, path := range candidates {
		if path == "" {
			continue
		}
		if _, err := os.Stat(path); err != nil {
			continue
		}
		cfg, err := clientcmd.BuildConfigFromFlags("", path)
		if err != nil {
			return nil, fmt.Errorf("loading kubeconfig %s: %w", path, err)
		}
		return cfg, nil
	}
	return nil, fmt.Errorf("no kubeconfig found (set RAILGRID_PROVIDER_KUBECONFIG)")
}

// Subcommands:
//
//	app-studio init   — one-shot: apply APIResourceSchemas, APIExport,
//	    APIExportEndpointSlice, and bind grant into the provider workspace using
//	    RAILGRID_PROVIDER_KUBECONFIG. See init_cmd.go.
//	app-studio serve  — runtime (default).
func main() {
	os.Exit(runMain(os.Args[1:]))
}

func runMain(args []string) int {
	return runMainWith(args, runInitCmd, runServe, os.Stderr)
}

func runMainWith(args []string, initCmd func(context.Context) error, serve func(), stderr io.Writer) int {
	if len(args) > 0 {
		switch args[0] {
		case "init":
			ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
			defer stop()
			if err := initCmd(ctx); err != nil {
				// The exit code, not the message, is what the caller acts on.
				_, _ = fmt.Fprintln(stderr, "init:", err)
				return 1
			}
			return 0
		case "serve":
			serve()
			return 0
		default:
			// The exit code, not the message, is what the caller acts on.
			_, _ = fmt.Fprintf(stderr, "unknown subcommand: %s\nusage: app-studio [init|serve]\n", args[0])
			return 2
		}
	}
	serve()
	return 0
}

func runServe() {
	port := envOr("PORT", "8081")
	sandboxConfig, sandboxWarnings, err := api.ParseCodingSandboxConfig(os.Getenv)
	if err != nil {
		log.Fatalf("coding sandbox configuration: %v", err)
	}
	for _, warning := range sandboxWarnings {
		log.Printf("WARNING coding sandbox configuration: %s", warning)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	hubInsecure := os.Getenv("RAILGRID_HUB_INSECURE") == "true"
	if os.Getenv("RAILGRID_HUB_URL") == "" {
		log.Printf("WARNING hub REST and MCP calls disabled (no RAILGRID_HUB_URL)")
	}

	// The provider's own kcp credential, mounted by the chart from the Secret
	// the hub minted. It is the ONE credential on the data plane: a verb
	// arrives with the caller's identity stamped by a kcp shard and no bearer,
	// so the gate decides visibility with a SubjectAccessReview on the
	// caller's behalf and every handler then acts as the provider through its
	// APIExport virtual workspace — the same door the controllers watch tenant
	// workspaces through. Without it every verb fails closed and the project
	// API is effectively disabled (useful for UI-only dev), with a loud
	// warning.
	kcpConfig, kcpErr := loadProviderConfig()
	if kcpErr != nil {
		kcpConfig = nil
	}
	var providerCallers *dataplane.Callers
	var tenantClient *tenant.Client
	if kcpConfig == nil {
		log.Printf("WARNING project API disabled (no provider kubeconfig: %v)", kcpErr)
	} else {
		pc, err := dataplane.NewCallerFactory(kcpConfig, dataplane.WithProviderConfig(kcpConfig, apiExportName))
		if err != nil {
			log.Fatalf("provider caller factory: %v", err)
		}
		providerCallers = pc
		tenantClient = tenant.NewClient(pc)
	}

	msgStore, closeStore, err := openMessageStore(ctx)
	if err != nil {
		log.Fatalf("message store: %v", err)
	}
	defer closeStore()

	// One workspace store instance, shared by the HTTP layer and the Project
	// reconciler's commit convergence (same PVC, same settlement ledger).
	workspaces := openWorkspaceStore()

	apiServer := api.NewWithWorkspaceContext(ctx,
		tenantClient,
		msgStore,
		workspaces,
		os.Getenv("RAILGRID_HUB_URL"),
		// The MCP virtual-workspace and authenticated catalog endpoints live on the
		// same hub host as the tenant client above, so they must honor the standard
		// RAILGRID_HUB_INSECURE knob every provider uses for in-cluster hub TLS (the
		// hub serves its external cert, not one valid for the .svc.cluster.local
		// name). Keep the MCP-specific override too, for callers that want to
		// scope it narrowly. Provider Action invocation has its own verified
		// transport and is not relaxed by this setting.
		os.Getenv("APP_STUDIO_MCP_INSECURE_SKIP_TLS_VERIFY") == "true" ||
			hubInsecure,
	)
	attachmentRetention := parseRetention(os.Getenv("APP_STUDIO_ATTACHMENT_DRAFT_RETENTION"))
	if attachmentRetention <= 0 {
		attachmentRetention = store.DefaultAttachmentDraftRetention
	}
	apiServer.ConfigureAttachmentDraftRetention(attachmentRetention)
	if quotaConfigurer, ok := msgStore.(store.AttachmentQuotaConfigurer); ok {
		quota := store.DefaultAttachmentQuota()
		quotaRaw := strings.TrimSpace(os.Getenv("APP_STUDIO_ATTACHMENT_WORKSPACE_QUOTA_BYTES"))
		if quotaRaw == "" {
			// Keep the shorter name as a compatibility alias for early chart
			// values and local deployments.
			quotaRaw = strings.TrimSpace(os.Getenv("APP_STUDIO_ATTACHMENT_WORKSPACE_QUOTA"))
		}
		if quotaRaw != "" {
			quota.WorkspaceMaxBytes, err = store.ParseAttachmentQuotaBytes(quotaRaw)
			if err != nil {
				log.Fatalf("attachment workspace quota: %v", err)
			}
		}
		if err := quotaConfigurer.ConfigureAttachmentQuota(quota); err != nil {
			log.Fatalf("configure attachment quota: %v", err)
		}
	}
	if bindingReconciler, ok := msgStore.(store.AttachmentBindingReconciler); ok {
		if err := bindingReconciler.ReconcileAttachmentBindings(ctx); err != nil {
			log.Fatalf("reconcile attachment bindings: %v", err)
		}
	}
	if attachmentStore, ok := msgStore.(store.AttachmentStore); ok {
		go runAttachmentRetention(ctx, attachmentStore, attachmentRetention)
	}
	apiServer.ConfigureCodingSandbox(sandboxConfig)
	apiServer.SetPreviewInsecureSkipTLSVerify(os.Getenv("APP_STUDIO_PREVIEW_INSECURE_SKIP_TLS_VERIFY") == "true")
	// Preview inspection drives the workspace's shared browser (the Studio's
	// Playwright MCP instance) over the infrastructure data plane — no
	// app-studio-owned browser worker to configure.
	previewBridgeEnabled, previewBridgeSigningKey, previewBridgeSigningKeyID := previewBridgeEnvironmentConfig()
	if err := apiServer.ConfigurePreviewBridge(
		previewBridgeEnabled,
		previewBridgeSigningKey,
		previewBridgeSigningKeyID,
	); err != nil {
		log.Fatalf("preview bridge: %v", err)
	}
	// App Studio holds no runtime-cluster kubeconfig: the development data
	// plane (logs/sync/restart) is served by the infrastructure provider as
	// custom subresources on the template instance, which App Studio calls
	// through its own export virtual workspace as itself under the claims in
	// manifest.yaml. See docs/app-studio-template-sandboxes.md.

	// Readiness is reachability of the APIExport virtual workspace, plus —
	// while this replica holds the controller lease — whether the multicluster
	// provider is actually watching tenant workspaces (see
	// provider-sdk/vwhealth). A replica that is not leading has nothing
	// attached and stays ready on the probe alone: its REST API, assistant
	// supervisor and replica-affinity forwarder are serving regardless.
	mode := controllerModeFromEnv()
	vwState := &vwhealth.Readiness{}
	if mode == controllerModeRequired && kcpConfig == nil {
		// Fail closed: a pod told to run controllers that has no credential to
		// run them with must not advertise readiness. Nothing else can notice
		// this — the probe needs a config to probe with.
		err := fmt.Errorf("controller mode is %q but no provider kubeconfig resolved: %w", mode, kcpErr)
		log.Printf("%v", err)
		vwState.Attach("controllers", checkerFunc(func() error { return err }))
	}

	// The data plane acts as the provider (see providerCallers above); the
	// hub's own REST API and MCP aggregate — not data-plane verbs — are
	// reached with the provider's hub token, since a verb carries no caller
	// bearer to forward there.
	if providerCallers != nil {
		apiServer.UseProviderCallers(providerCallers)
	}
	if hubToken, err := hubclient.ResolveHubToken(); err != nil {
		log.Printf("hub token: %v (hub REST and MCP calls will be unauthenticated)", err)
	} else {
		apiServer.SetHubToken(hubToken)
	}

	handler, err := newHandler(apiServer, vwhealth.Handler(vwState))
	if err != nil {
		log.Fatalf("server: %v", err)
	}

	// Replica awareness (docs/app-studio-replica-awareness.md): durable run
	// claims + project affinity. The routing identity is always set (claims
	// work single-replica and fix restart semantics); the forwarding address
	// and the internal listener need POD_IP + a provider bearer and degrade
	// to local-only serving without them.
	replicaID := replicaIdentityFromEnv()
	internalPort := envOr("APP_STUDIO_INTERNAL_PORT", "8091")
	replicaAddr := ""
	if podIP := os.Getenv("POD_IP"); podIP != "" {
		replicaAddr = podIP + ":" + internalPort
	}
	internalToken := ""
	if kcpConfig != nil {
		internalToken = kcpConfig.BearerToken
	}
	if replicaAddr != "" && internalToken == "" {
		log.Printf("replica forwarding disabled (provider credential has no bearer token); serving project requests locally")
		replicaAddr = ""
	}
	apiServer.SetReplicaRouting(replicaID, replicaAddr, internalToken)

	// Project affinity wraps the full handler; the public listener strips the
	// spoofable routing headers, the internal listener authenticates peers
	// and marks their requests forwarded (the loop guard).
	core := apiServer.ReplicaAffinity(handler)
	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           apiServer.StripReplicaHeaders(core),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		log.Printf("app-studio provider listening on :%s", port)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("server: %v", err)
		}
	}()

	// The internal listener carries peer-forwarded project requests AND
	// /metrics. Metrics used to sit beside /api/* on the public port, where
	// any caller the hub proxied could scrape the assistant's operational
	// counters; it is not a Pillar 2 route class and serve.New has no field
	// for it, which is the contract saying the same thing.
	internalMux := http.NewServeMux()
	internalMux.Handle("/metrics", apiServer.MetricsHandler())
	internalMux.Handle("/", apiServer.InternalReplicaHandler(core))
	if replicaAddr != "" {
		internalSrv := &http.Server{
			Addr:              ":" + internalPort,
			Handler:           internalMux,
			ReadHeaderTimeout: 10 * time.Second,
		}
		go func() {
			log.Printf("app-studio internal listener (peer-forwarded project requests) on :%s replica=%s", internalPort, replicaID)
			if err := internalSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Printf("internal listener failed; peers cannot forward to this replica: %v", err)
			}
		}()
		go func() {
			<-ctx.Done()
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = internalSrv.Shutdown(shutdownCtx)
		}()
	}

	// The hub records any received beat as liveness and never inspects its
	// status, so hold beats while readiness says otherwise: the TTL then flips
	// the catalog entry to NotReady instead of it staying green over a
	// provider that cannot reach the tenant workspaces it serves.
	go vwhealth.Watch(ctx, kcpConfig, endpointSliceName, vwState, vwhealth.DefaultInterval)

	hb, err := hubclient.ConfigFromEnv(providerName, heartbeatVersion)
	if err != nil {
		log.Printf("heartbeat token: %v (beats will be unauthenticated)", err)
	}
	hb.CanSend = func() bool { return vwState.Check() == nil }
	go hubclient.RunHeartbeat(ctx, hb)

	// Deterministic lifecycle: the Project, Session and Studio reconcilers
	// converge state across every tenant workspace, behind a Lease in the
	// provider workspace so only one replica writes. Opt-in via
	// RAILGRID_PROVIDER_KUBECONFIG; the campaign itself retries forever, which
	// is what covers a provider that comes up before `init` has created its
	// workspace, APIExport and endpoint slice.
	if mode == controllerModeRESTOnly {
		log.Printf("controller manager disabled: explicit REST-only mode")
	} else {
		// The provider's own connection for what the controllers read and
		// watch of OTHER providers' kinds — and for the verbs they call on
		// them — through this export's virtual workspace, under the
		// composition claims the tenant accepted.
		deps := controllerDeps{
			Actions:     apiServer.ActionsRuntimeConfig(),
			Workspace:   workspaces,
			Busy:        apiServer.AssistantBusy,
			Owns:        apiServer.OwnsProject,
			OnCommitted: projectCommitNotifier(apiServer.ProjectCommitted),
			Store:       msgStore,
			// Deleting a Project stops its assistant run rather than being
			// refused by it: the CR is already going (Cut D.4).
			StopAssistant: apiServer.StopAssistantForDeletedProject,
			HubBase:       strings.TrimRight(os.Getenv("RAILGRID_HUB_URL"), "/"),
			HubInsecure:   os.Getenv("RAILGRID_HUB_INSECURE") == "true",
			Callers:       providerCallers,
			// Event-driven reconciles: the API publishes thread/turn and
			// workspace transitions, the controllers subscribe.
			SessionSignals: apiServer.SessionSignals(),
			ProjectSignals: apiServer.ProjectSignals(),
			// Conversation retention as a per-Session deadline (zero: keep
			// conversations forever).
			SessionRetention: parseRetention(os.Getenv("APP_STUDIO_MESSAGE_RETENTION")),
		}
		if err := startControllerManager(ctx, kcpConfig, deps, vwState); err != nil {
			log.Printf("controller manager: NOT started: %v", err)
		}
	}

	<-ctx.Done()
	log.Printf("shutting down")
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	apiServer.Shutdown(shutdown)
	if err := srv.Shutdown(shutdown); err != nil {
		log.Printf("shutdown error: %v", err)
	}
	// Only once no request can renew them: hand this replica's projects to
	// whichever replica serves them next (including its own replacement).
	release, cancelRelease := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancelRelease()
	apiServer.RelinquishProjectClaims(release)
}

// newHandler builds the provider's whole HTTP surface from provider-sdk/serve.
//
// It used to be a hand-rolled gorilla router: /healthz, /readyz, ~95 /api/*
// routes, a /metrics endpoint beside them, and a portal catch-all with its own
// index fallback. serve.New takes one handler per Pillar 2 route class and
// refuses anything else — there is no /api/* field, so the deviation this
// provider had the most of is now one it cannot express
// (docs/provider-connectivity-contract.md §"Pillar 2 route classes").
//
// apiServer may be nil (the portal still serves), which keeps the asset tests
// independent of the kube/store wiring. readiness may be nil, which serves
// /readyz as always ready.
func newHandler(apiServer *api.Server, readiness http.Handler) (http.Handler, error) {
	_, distFS, err := portalHandler()
	if err != nil {
		return nil, err
	}
	if readiness == nil {
		readiness = vwhealth.Handler(&vwhealth.Readiness{})
	}
	options := serve.Options{
		Name:      providerName,
		Readiness: readiness,
		Portal:    distFS,
	}
	if apiServer != nil {
		// Class (a): every tenant-facing verb, dispatched by serve's
		// subresource adapter off the path a kcp shard forwards for a custom
		// subresource, each gated for visibility as the caller and then served
		// as the provider. The table of coordinates is derived from this
		// provider's own CatalogEntry manifest (catalogentry.go), so what kcp
		// routes and what serve answers are one declaration; without the
		// manifest there is no data plane, and that is a startup error.
		// Without an apiServer there is nothing to dispatch to, so neither is
		// set rather than mounting a dead prefix.
		options.DataPlane = apiServer.DataPlane()
		subresources, err := subresourceRoutes()
		if err != nil {
			return nil, err
		}
		options.Subresources = subresources
	}
	handler, err := serve.New(options)
	if err != nil {
		return nil, err
	}
	return handler, nil
}

func openWorkspaceStore() *workspace.FileStore {
	root := strings.TrimSpace(os.Getenv("APP_STUDIO_WORKSPACE_ROOT"))
	if root == "" {
		root = filepath.Join(os.TempDir(), "railgrid-app-studio-workspaces")
	}
	log.Printf("app studio workspace root: %s", root)
	store := workspace.NewFileStore(root)
	// The volume holds the working TREE and nothing else. The working-copy
	// ledger — dirty paths, source revision, the commit in flight, the
	// settlement receipt — is `Project.status.workspace`, reached through a
	// client attached to each call's context: the caller's on the request path
	// (api/project_ledger.go), the manager's in the Project reconciler. This
	// makes a call path that forgot to attach one an error rather than a
	// silent return to pod-local authority.
	store.RequireContextLedger()
	return store
}

// openMessageStore builds the App Studio message store from env, wraps it with
// envelope encryption when configured, and starts the retention sweeper. The
// returned closer is always safe to call.
func openMessageStore(ctx context.Context) (store.Store, func(), error) {
	noop := func() {}

	var msgStore store.Store
	closeFn := noop
	if dsn := strings.TrimSpace(os.Getenv("APP_STUDIO_DATABASE_URL")); dsn != "" {
		ps, err := store.OpenPostgres(ctx, dsn)
		if err != nil {
			return nil, noop, fmt.Errorf("opening app studio message store: %w", err)
		}
		msgStore = ps
		closeFn = func() {
			if err := ps.Close(); err != nil {
				log.Printf("closing App Studio message store: %v", err)
			}
		}
	} else if os.Getenv("APP_STUDIO_IN_MEMORY_MESSAGE_STORE") == "true" {
		msgStore = store.NewMemoryStore()
	}
	if msgStore == nil {
		return nil, noop, fmt.Errorf("app studio message store requires APP_STUDIO_DATABASE_URL or APP_STUDIO_IN_MEMORY_MESSAGE_STORE=true")
	}

	encKeys := strings.TrimSpace(os.Getenv("APP_STUDIO_MESSAGE_ENCRYPTION_KEYS"))
	if encKeys != "" {
		keys, err := store.ParseEncryptionKeys(encKeys)
		if err != nil {
			return nil, noop, fmt.Errorf("parsing app studio message encryption keys: %w", err)
		}
		msgStore, err = store.NewEncryptedStore(msgStore, keys)
		if err != nil {
			return nil, noop, fmt.Errorf("configuring app studio message encryption: %w", err)
		}
	}

	// Conversation retention is NOT swept here any more. It is a per-Session
	// deadline owned by the Session reconciler (controller/session): it
	// requeues at status.lastActivityAt + APP_STUDIO_MESSAGE_RETENTION and
	// deletes that Session, whose finalizer purges the thread. One owner (the
	// controller leader), one conversation at a time, and an in-flight turn
	// defers its own expiry — none of which a fleet-wide cutoff could do.
	return msgStore, closeFn, nil
}

func parseRetention(raw string) time.Duration {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		log.Printf("WARNING ignoring invalid APP_STUDIO_MESSAGE_RETENTION %q: %v", raw, err)
		return 0
	}
	return d
}

// runAttachmentRetention sweeps expired DRAFT attachments on a cutoff, and
// deliberately stays a sweep.
//
// A draft is an upload that no turn has claimed yet: it is scoped to a project
// and an actor, carries its own expires_at, and can outlive — or entirely
// predate — any thread. There is therefore no Session to hang its deadline on,
// which is why conversation retention moved to the Session reconciler and this
// one did not. Attachments that a turn DID bind are not swept here at all:
// they belong to the conversation and go with it when the Session's finalizer
// purges the thread.
func runAttachmentRetention(ctx context.Context, attachmentStore store.AttachmentStore, retention time.Duration) {
	interval := retention / 4
	if interval < time.Minute {
		interval = time.Minute
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, err := attachmentStore.DeleteExpiredAttachments(ctx, time.Now().UTC()); err != nil {
				log.Printf("App Studio attachment retention cleanup failed: %v", err)
			}
		}
	}
}
