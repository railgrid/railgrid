// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package main

// One-shot bootstrap. Runs every step that needs admin credentials
// in the provider workspace, then writes a kubeconfig the serve
// subcommand reads to run with a lower-privilege minted SA token.
//
// Step list (each is idempotent):
//
//   1. Install platform CRDs into the provider workspace.
//   2. Register the platform CRDs as APIExport.spec.resources entries.
//   3. Apply the CachedResource that projects Templates to tenants.
//   4. Create the ServiceAccount + Role + RoleBinding the runtime uses.
//   5. Mint a TokenRequest bearer.
//   6. Build a kubeconfig (in-cluster server URL + minted token) and
//      write it to the path the serve subcommand reads.
//   7. Apply the kro-cluster Secret in the kro Helm release's
//      namespace so kro starts watching this APIExport's virtual
//      workspace.
//
// Exits on any step's error so a partial bootstrap is obvious.

import (
	"context"
	"fmt"
	"log"
	"net/url"
	"os"
	"strings"

	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	sdkinstall "github.com/faroshq/provider-sdk/install"

	"github.com/faroshq/provider-infrastructure/install"
)

// apiExportName is the infrastructure provider's APIExport (manifest.yaml
// spec.apiExport.name).
const apiExportName = "infrastructure.providers.kedge.faros.sh"

// runInitCmd drives the bootstrap chain. Reads admin credentials from
// INFRASTRUCTURE_ADMIN_KUBECONFIG (preferred) or the standard
// KUBECONFIG env var. Writes the minted kubeconfig to the path in
// INFRASTRUCTURE_KUBECONFIG (defaulting to ./infrastructure.kubeconfig
// when unset).
func runInitCmd(ctx context.Context) error {
	adminConfig, err := loadAdminConfig()
	if err != nil {
		return fmt.Errorf("load admin kubeconfig: %w", err)
	}

	log.Printf("init: installing CRDs into provider workspace")
	if err := install.CRDs(ctx, adminConfig); err != nil {
		return fmt.Errorf("install CRDs: %w", err)
	}

	// CachedResource MUST precede APIExport wiring: the APIExport's
	// templates entry uses storage.virtual backed by an EndpointSlice
	// over this CachedResource. Order = CachedResource → EndpointSlice
	// → wait for IdentityHash → APIExport with the resolved hash.
	log.Printf("init: applying CachedResource for Templates")
	if err := install.PlatformCachedResources(ctx, adminConfig); err != nil {
		return fmt.Errorf("install CachedResource: %w", err)
	}

	log.Printf("init: applying CachedResourceEndpointSlice for Templates")
	if err := install.PlatformCachedResourceEndpointSlices(ctx, adminConfig); err != nil {
		return fmt.Errorf("install EndpointSlice: %w", err)
	}

	// The slice MUST carry the provider workspace path so kcp can resolve the
	// export's logical cluster and publish endpoint URLs — otherwise kro never
	// discovers the VW and tenant instances go unreconciled.
	workspacePath := os.Getenv("INFRASTRUCTURE_WORKSPACE_PATH")
	log.Printf("init: applying APIExportEndpointSlice (path=%q) for kro kcp-apiexport provider", workspacePath)
	if err := install.PlatformAPIExportEndpointSlice(ctx, adminConfig, workspacePath); err != nil {
		return fmt.Errorf("install APIExportEndpointSlice: %w", err)
	}

	log.Printf("init: waiting for CachedResource identityHash")
	templatesIdentityHash, err := install.WaitForCachedResourceIdentity(ctx, adminConfig)
	if err != nil {
		// Non-fatal: fall back to CRD storage so a single missing hash
		// doesn't block boot. Operators can re-run init once the
		// CachedResource catches up.
		log.Printf("init: WARNING %v — falling back to CRD storage for templates", err)
		templatesIdentityHash = ""
	}

	log.Printf("init: registering platform schemas on APIExport (templates storage=%s)", storageLabel(templatesIdentityHash))
	if err := install.PlatformSchemaInAPIExport(ctx, adminConfig, templatesIdentityHash); err != nil {
		return fmt.Errorf("register APIExport schemas: %w", err)
	}

	// Bind grant: let any authenticated tenant bind this APIExport from their
	// own workspace. The hub catalog controller used to create this; in the new
	// bootstrap split it is the provider init's responsibility. adminConfig.Host
	// already targets the provider workspace (retargeted from
	// INFRASTRUCTURE_WORKSPACE_PATH), so the SDK call lands there.
	log.Printf("init: applying APIExport bind grant")
	dynCl, err := dynamic.NewForConfig(adminConfig)
	if err != nil {
		return fmt.Errorf("dynamic client for bind grant: %w", err)
	}
	if err := sdkinstall.ApplyBindGrant(ctx, dynCl, apiExportName); err != nil {
		return fmt.Errorf("apply bind grant: %w", err)
	}

	// CatalogEntry self-registration: apply the provider's CatalogEntry into its
	// own workspace (the Provider controller bound providers.kedge.faros.sh
	// here). adminConfig.Host already targets the provider workspace. Empty
	// KEDGE_CATALOGENTRY_FILE → skip.
	if f := os.Getenv("KEDGE_CATALOGENTRY_FILE"); f != "" {
		log.Printf("init: self-registering CatalogEntry from %s", f)
		if err := sdkinstall.ApplyCatalogEntry(ctx, dynCl, f); err != nil {
			return fmt.Errorf("apply CatalogEntry: %w", err)
		}
	}

	// Seed catalog Templates so a fresh workspace renders a non-empty
	// catalog. Off-switch (INFRASTRUCTURE_SKIP_SEED_TEMPLATES) is for
	// production clusters whose catalog is managed by GitOps.
	if os.Getenv("INFRASTRUCTURE_SKIP_SEED_TEMPLATES") == "" {
		log.Printf("init: seeding catalog Templates")
		if err := install.SeedTemplates(ctx, adminConfig); err != nil {
			// Non-fatal — operators can hand-apply, and the rest of
			// the chain (SA mint, kro seed) is independent of seed
			// content. Log loudly so the failure is visible.
			log.Printf("init: WARNING failed to seed Templates: %v", err)
		}
	} else {
		log.Printf("init: INFRASTRUCTURE_SKIP_SEED_TEMPLATES set — leaving catalog empty")
	}

	log.Printf("init: minting ServiceAccount + token for runtime")
	mint, err := install.MintRuntimeIdentity(ctx, adminConfig)
	if err != nil {
		return fmt.Errorf("mint runtime identity: %w", err)
	}

	kubeconfigPath := os.Getenv("INFRASTRUCTURE_KUBECONFIG")
	if kubeconfigPath == "" {
		kubeconfigPath = "./infrastructure.kubeconfig"
	}
	log.Printf("init: writing minted kubeconfig to %s", kubeconfigPath)
	if err := install.WriteKubeconfig(kubeconfigPath, mint); err != nil {
		return fmt.Errorf("write kubeconfig: %w", err)
	}

	// When INFRASTRUCTURE_RUNTIME_KUBECONFIG_SECRET is set (the Helm init
	// container path), also write the minted runtime kubeconfig into a Secret
	// in the host cluster so the long-lived serve container can mount it. The
	// host cluster is the pod's own cluster (in-cluster config), which is
	// distinct from the admin kcp config used for the bootstrap above.
	if secretName := os.Getenv("INFRASTRUCTURE_RUNTIME_KUBECONFIG_SECRET"); secretName != "" {
		ns := os.Getenv("INFRASTRUCTURE_RUNTIME_KUBECONFIG_NAMESPACE")
		if ns == "" {
			ns = os.Getenv("POD_NAMESPACE")
		}
		if ns == "" {
			ns = "default"
		}
		hostConfig, herr := loadHostConfig()
		if herr != nil {
			return fmt.Errorf("load host kubeconfig for runtime Secret: %w", herr)
		}
		log.Printf("init: writing runtime kubeconfig to Secret %s/%s", ns, secretName)
		if err := install.WriteKubeconfigToSecret(ctx, hostConfig, ns, secretName, mint); err != nil {
			return fmt.Errorf("write runtime kubeconfig Secret: %w", err)
		}
	}

	// kro seeding is best-effort during PR C bring-up: if no
	// KRO_KUBECONFIG is set, we skip and log loudly. The serve
	// subcommand still runs; tenants who apply Instance CRs see them
	// as Pending until kro is wired.
	if os.Getenv("KRO_KUBECONFIG") != "" {
		log.Printf("init: seeding kro with VW kubeconfig Secret")
		if err := install.SeedKroCluster(ctx, mint); err != nil {
			return fmt.Errorf("seed kro: %w", err)
		}
	} else {
		log.Printf("init: KRO_KUBECONFIG unset — skipping kro Secret seed; tenant Instance CRs will stay Pending until kro is configured")
	}

	log.Printf("init: complete. serve with INFRASTRUCTURE_KUBECONFIG=%s", kubeconfigPath)
	return nil
}

// loadAdminConfig resolves the admin kubeconfig used for the init
// chain. Search order matches the existing controller-manager loader:
// INFRASTRUCTURE_ADMIN_KUBECONFIG → KUBECONFIG → in-cluster (rare for
// init; mostly for completeness).
//
// When INFRASTRUCTURE_WORKSPACE_PATH is set, the resolved config's
// Host is rewritten to point at that workspace (the install/* code
// uses Host as the cluster URL). This is the equivalent of the
// `--server=…/clusters/<path>` flag the install-provider-infrastructure
// target uses with kubectl: it lets a generic admin kubeconfig install
// CRDs / APIExport entries into a specific workspace without the
// operator having to maintain a workspace-scoped kubeconfig on disk.
func loadAdminConfig() (*rest.Config, error) {
	var (
		cfg *rest.Config
		err error
	)
	switch {
	case os.Getenv("INFRASTRUCTURE_ADMIN_KUBECONFIG") != "":
		cfg, err = clientcmd.BuildConfigFromFlags("", os.Getenv("INFRASTRUCTURE_ADMIN_KUBECONFIG"))
	case os.Getenv("KUBECONFIG") != "":
		cfg, err = clientcmd.BuildConfigFromFlags("", os.Getenv("KUBECONFIG"))
	default:
		cfg, err = rest.InClusterConfig()
	}
	if err != nil {
		return nil, err
	}
	if ws := os.Getenv("INFRASTRUCTURE_WORKSPACE_PATH"); ws != "" {
		host, err := retargetHostToWorkspace(cfg.Host, ws)
		if err != nil {
			return nil, fmt.Errorf("retarget admin kubeconfig to workspace %q: %w", ws, err)
		}
		cfg.Host = host
	}
	return cfg, nil
}

// loadHostConfig resolves the client for the host cluster — the cluster the
// provider Deployment runs in, where the runtime kubeconfig Secret is written.
// This is deliberately separate from loadAdminConfig (which targets kcp): the
// init container writes the Secret into its own pod's cluster via the pod
// ServiceAccount (in-cluster), with HOST_KUBECONFIG as an out-of-cluster dev
// override.
func loadHostConfig() (*rest.Config, error) {
	if p := os.Getenv("HOST_KUBECONFIG"); p != "" {
		return clientcmd.BuildConfigFromFlags("", p)
	}
	return rest.InClusterConfig()
}

// storageLabel renders the templates storage kind for a startup log
// line — "virtual" when the CachedResource produced an identityHash,
// "crd" when init fell back. Kept inline to avoid leaking the helper
// from install/.
func storageLabel(hash string) string {
	if hash == "" {
		return "crd"
	}
	return "virtual"
}

// retargetHostToWorkspace rewrites a kcp host URL so it terminates at
// /clusters/<workspacePath>. Idempotent — if host already has a
// /clusters/… segment, it's replaced rather than appended.
func retargetHostToWorkspace(host, workspacePath string) (string, error) {
	u, err := url.Parse(host)
	if err != nil {
		return "", fmt.Errorf("parse host %q: %w", host, err)
	}
	if idx := strings.Index(u.Path, "/clusters/"); idx >= 0 {
		u.Path = u.Path[:idx]
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/clusters/" + workspacePath
	return u.String(), nil
}
