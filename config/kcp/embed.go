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

// Package kcp embeds kcp configuration files.
package kcp

import "embed"

// RootWorkspaceFS contains the kedge workspace definition applied to the root workspace.
//
//go:embed workspace-kedge.yaml
var RootWorkspaceFS embed.FS

// KedgeWorkspaceFS contains workspace definitions for children of root:kedge:
// the provider sub-workspace parent, the tenant-fleet parent, and the `system`
// container. The User/Organization CR-object storage moved from
// root:kedge:users into root:kedge:system:tenants, so workspace-users.yaml is
// gone. The `organization` + `workspace` + `edge` + `provider` WorkspaceTypes
// ship in PostProvidersFS (they carry defaultAPIBindings to exports that must
// exist first).
//
//go:embed workspace-providers.yaml workspace-tenants.yaml workspace-system.yaml
var KedgeWorkspaceFS embed.FS

// SystemWorkspaceFS contains the children of root:kedge:system — controllers
// (all platform APIExports), providers (Provider/CatalogEntry objects), and
// tenants (User/Organization/Membership objects). Applied INTO root:kedge:system
// after it is Ready.
//
//go:embed workspace-system-controllers.yaml workspace-system-providers.yaml workspace-system-tenants.yaml
var SystemWorkspaceFS embed.FS

// ProvidersFS contains the platform APIResourceSchemas + APIExports applied to
// root:kedge:system:controllers (the single home for all platform exports).
//
//go:embed apiresourceschema-*.yaml apiexport-*.yaml
var ProvidersFS embed.FS

// PostProvidersFS contains workspace-scoped objects that must be applied in
// root:kedge AFTER ProvidersFS has populated root:kedge:system:controllers with
// the APIExports they reference. Ships the `organization` + `workspace` +
// `edge` + `provider` WorkspaceTypes. They carry defaultAPIBindings to exports
// under root:kedge:system:controllers (e.g. tenants.kedge.faros.sh,
// providers.kedge.faros.sh); kcp's WorkspaceType admission
// validates bind permission on every APIExport in defaultAPIBindings, so the
// referenced export has to exist by the time the WT is applied, otherwise
// the LogicalCluster lookup fails and admission returns 403 forbidden (see
// PR #205 / commit 3d2d277 for the failure shape). The `edge` WorkspaceType
// has no defaultAPIBindings (it is a pure mount point) but ships here too so
// the `workspace` type's limitAllowedChildren reference to it resolves.
//
//go:embed workspacetype-organization.yaml workspacetype-workspace.yaml workspacetype-edge.yaml workspacetype-provider.yaml
var PostProvidersFS embed.FS
