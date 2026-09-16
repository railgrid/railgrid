// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package kro

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
)

// tenantNamespaceName derives a deterministic namespace name from a
// kcp workspace path. SHA256-truncated to 12 hex chars so it's
// idempotent across restarts but short enough to leave room for the
// prefix under Kubernetes' 63-char limit on label values that may
// reference the namespace name.
func tenantNamespaceName(tenantPath string) string {
	prefix := os.Getenv("KRO_NAMESPACE_PREFIX")
	if prefix == "" {
		prefix = "railgrid-tenants-"
	}
	return prefix + tenantHash(tenantPath)
}

// tenantHash returns a 12-char SHA prefix of a workspace path. Used
// both for the namespace name AND for label values (railgrid.ai/tenant)
// — the raw `root:railgrid:orgs:<uuid>` form contains `:` which Kubernetes
// rejects in label values (allowed: alphanumeric + `-_.` only). The full
// path is preserved as an annotation
// (railgrid.ai/tenant-workspace-path) for human debugging; programmatic
// label-selector lookups go through this hash so writer + reader agree
// without having to parse colons.
func tenantHash(tenantPath string) string {
	sum := sha256.Sum256([]byte(tenantPath))
	return hex.EncodeToString(sum[:6])
}

// LabelTenantValue is the public form of tenantHash. The server
// handlers + MCP tools build label selectors with this so they match the
// railgrid.ai/tenant value stamped onto tenant-owned objects.
func LabelTenantValue(tenantPath string) string { return tenantHash(tenantPath) }

// TenantNamespace is the public form of tenantNamespaceName: the
// per-tenant namespace in the central kro cluster under the legacy
// central-kro model. It is NOT where the kro fork materializes an
// instance's workloads under the control-plane/data-plane split; for
// that, use RuntimeNamespace.
func TenantNamespace(tenantPath string) string { return tenantNamespaceName(tenantPath) }

// RuntimeNamespace returns the namespace on the runtime cluster where the
// kro fork materializes an instance's namespaced children. Under the
// control-plane/data-plane split (DeployToLocalRuntime) the fork isolates
// each tenant's workloads per source (kcp logical) cluster as
// "<sourceCluster>-<sourceNamespace>" — mirroring kro-run/kro's instance
// controller tenantNamespace helper. Cross-cluster resources the infra
// provider writes to sit BESIDE those workloads — bridged OIDC/registry
// Secrets and the default-SA imagePullSecrets — must target this namespace,
// not the railgrid-tenants-<hash> control-plane namespace (TenantNamespace),
// or they land in an empty namespace no pod can see.
//
// An empty sourceNamespace defaults to "default": instances created without an
// explicit namespace (e.g. App Studio's promoted Application/SimpleWebApp) carry
// namespace "" on the instance object, yet their children materialize into
// "<sourceCluster>-default" (children default to the "default" namespace). Bare
// concatenation would produce "<sourceCluster>-", which is both an invalid
// RFC 1123 label (trailing "-") and the wrong target — the bridged Secret would
// miss the namespace the pods actually run in. Defaulting here keeps the bridge
// aligned with kro's child placement.
func RuntimeNamespace(sourceCluster, sourceNamespace string) string {
	if sourceNamespace == "" {
		sourceNamespace = "default"
	}
	return sourceCluster + "-" + sourceNamespace
}
