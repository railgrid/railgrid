// Copyright 2026 The Railgrid Authors.
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
	"os"
	"strings"

	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	sdkinstall "github.com/railgrid/provider-sdk/install"

	"github.com/railgrid/provider-infrastructure/install"
)

// apiExportName is the infrastructure provider's APIExport (manifest.yaml
// spec.export.name).
const apiExportName = "infrastructure.providers.railgrid.ai"

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

	// The shipped APIResourceSchemas (instances, templates and one per
	// instances/<verb> subresource, all from apigen), the DataPlaneEndpointSlice
	// the subresource entries route through, the generated APIExport, its
	// APIExportEndpointSlice and the bind grant — the same bootstrap every
	// provider runs. The export is applied with templates on CRD storage; once
	// the CachedResource identityHash is known, PlatformSchemaInAPIExport below
	// re-points that one entry at virtual storage. The CatalogEntry is
	// self-registered here too when RAILGRID_CATALOGENTRY_FILE names one (the
	// chart's init container); the dev Makefile applies it through the admin
	// path and leaves the variable unset.
	workspacePath := os.Getenv("INFRASTRUCTURE_WORKSPACE_PATH")
	log.Printf("init: bootstrapping schemas, APIExport %q, endpoint slices and bind grant", apiExportName)
	if err := sdkinstall.Bootstrap(ctx, sdkinstall.Options{
		Config:           adminConfig,
		ExportName:       apiExportName,
		WorkspacePath:    workspacePath,
		KCPDir:           install.KCPDir(),
		CatalogEntryFile: catalogEntryPath(),
		DataPlaneURL:     strings.TrimSpace(os.Getenv("RAILGRID_DATAPLANE_URL")),
	}); err != nil {
		return fmt.Errorf("provider workspace bootstrap: %w", err)
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
	// discovers the VW and tenant instances go unreconciled. It is the
	// kro-facing slice (named "infrastructure", what the kro chart is pointed
	// at); the SDK bootstrap above wrote the export-named one beside it.
	log.Printf("init: applying APIExportEndpointSlice (path=%q) for the provider's virtual-workspace controllers", workspacePath)
	if err := install.PlatformAPIExportEndpointSlice(ctx, adminConfig, workspacePath); err != nil {
		return fmt.Errorf("install APIExportEndpointSlice: %w", err)
	}

	log.Printf("init: waiting for CachedResource identityHash")
	// Templates MUST use the CachedResource virtual storage (so they project into
	// tenant workspaces). Never fall back to CRD storage — fail so the init
	// container retries until the identityHash is ready.
	templatesIdentityHash, err := install.WaitForCachedResourceIdentity(ctx, adminConfig)
	if err != nil {
		return fmt.Errorf("CachedResource identityHash not ready (templates require virtual storage): %w", err)
	}
	if templatesIdentityHash == "" {
		return fmt.Errorf("CachedResource identityHash empty (templates require virtual storage)")
	}

	log.Printf("init: re-pointing the templates entry at CachedResource virtual storage (storage=%s)", storageLabel(templatesIdentityHash))
	if err := install.PlatformSchemaInAPIExport(ctx, adminConfig, templatesIdentityHash); err != nil {
		return fmt.Errorf("register APIExport schemas: %w", err)
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

	// kro runs single-cluster against the runtime cluster (the instance
	// controller bridges kcp → runtime), so no kcp kubeconfig is seeded onto
	// the runtime cluster anymore.

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
// /clusters/<workspacePath>. Thin wrapper over install.RetargetHostToWorkspace
// so the init and operator paths share one implementation.
func retargetHostToWorkspace(host, workspacePath string) (string, error) {
	return install.RetargetHostToWorkspace(host, workspacePath)
}
