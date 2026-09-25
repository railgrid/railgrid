/*
Copyright 2026 The Railgrid Authors.

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

// Package kcp embeds kcp configuration files.
package kcp

import "embed"

// RootWorkspaceFS contains the railgrid workspace definition applied to the root workspace.
//
//go:embed workspace-railgrid.yaml
var RootWorkspaceFS embed.FS

// RailgridWorkspaceFS contains workspace definitions for children of root:railgrid:
// the provider sub-workspace parent, the tenant-fleet parent, and the `system`
// container. The User/Organization CR-object storage moved from
// root:railgrid:users into root:railgrid:system:tenants, so workspace-users.yaml is
// gone. The `organization` + `workspace` + `edge` + `provider` WorkspaceTypes
// ship in PostProvidersFS (they carry defaultAPIBindings to exports that must
// exist first).
//
//go:embed workspace-providers.yaml workspace-tenants.yaml workspace-system.yaml
var RailgridWorkspaceFS embed.FS

// SystemWorkspaceFS contains the children of root:railgrid:system — controllers
// (all platform APIExports), providers (Provider/CatalogEntry objects), and
// tenants (User/Organization/Membership objects). Applied INTO root:railgrid:system
// after it is Ready.
//
//go:embed workspace-system-controllers.yaml workspace-system-providers.yaml workspace-system-tenants.yaml
var SystemWorkspaceFS embed.FS

// PermissionClaimPolicyFS contains the generated kcp PermissionClaimPolicy
// (group admin.kcp.io), written by hack/generate-permission-claim-policy.mjs
// from every provider manifest's spec.requires[] entries that name a provider.
//
// It is applied only when the cluster actually serves the API — see
// pkg/hub/bootstrap.InstallPermissionClaimPolicy. The type is unmerged in kcp
// (kcp-dev/kcp#4385), so this stays a YAML blob the hub applies through the
// dynamic client: nothing here compiles against it.
//
//go:embed permissionclaimpolicy.yaml
var PermissionClaimPolicyFS embed.FS

// PermissionClaimPolicyFile is the name of the single file in
// PermissionClaimPolicyFS.
const PermissionClaimPolicyFile = "permissionclaimpolicy.yaml"

// ProvidersFS contains the platform APIResourceSchemas + APIExports applied to
// root:railgrid:system:controllers (the single home for all platform exports).
//
//go:embed apiresourceschema-*.yaml apiexport-*.yaml
var ProvidersFS embed.FS

// PostProvidersFS contains workspace-scoped objects that must be applied in
// root:railgrid AFTER ProvidersFS has populated root:railgrid:system:controllers with
// the APIExports they reference. Ships the `organization` + `workspace` +
// `edge` + `provider` WorkspaceTypes. They carry defaultAPIBindings to exports
// under root:railgrid:system:controllers (e.g. tenants.railgrid.ai,
// providers.railgrid.ai); kcp's WorkspaceType admission
// validates bind permission on every APIExport in defaultAPIBindings, so the
// referenced export has to exist by the time the WT is applied, otherwise
// the LogicalCluster lookup fails and admission returns 403 forbidden (see
// PR #205 / commit 3d2d277 for the failure shape). The `edge` WorkspaceType
// has no defaultAPIBindings (it is a pure mount point) but ships here too so
// the `workspace` type's limitAllowedChildren reference to it resolves.
//
//go:embed workspacetype-organization.yaml workspacetype-workspace.yaml workspacetype-edge.yaml workspacetype-provider.yaml
var PostProvidersFS embed.FS
