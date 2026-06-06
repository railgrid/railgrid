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

// KedgeWorkspaceFS contains workspace definitions for children of root:kedge.
// The legacy `tenant` WorkspaceType and its `root:kedge:tenants` parent
// workspace were retired when the new multi-org model took over (the
// personal Org's default child Workspace replaces the per-user tenant
// workspace); their YAMLs were deleted and dropped from the embed.
// The `organization` + `workspace` WorkspaceTypes carry defaultAPIBindings
// referencing the kedge-owned tenancy.kedge.faros.sh APIExport and ship in
// PostProvidersFS instead, so KedgeWorkspaceFS now contains only
// workspace bootstrap definitions.
//
//go:embed workspace-providers.yaml workspace-users.yaml workspace-orgs.yaml
var KedgeWorkspaceFS embed.FS

// ProvidersFS contains APIResourceSchemas and APIExport applied to root:kedge:providers.
//
//go:embed apiresourceschema-*.yaml apiexport-*.yaml
var ProvidersFS embed.FS

// PostProvidersFS contains workspace-scoped objects that must be applied in
// root:kedge AFTER ProvidersFS has populated root:kedge:providers with the
// APIExports they reference. Ships the `organization` + `workspace` + `edge`
// WorkspaceTypes. The first two carry a defaultAPIBinding to
// root:kedge:providers.tenancy.kedge.faros.sh; kcp's WorkspaceType admission
// validates bind permission on every APIExport in defaultAPIBindings, so the
// referenced export has to exist by the time the WT is applied, otherwise
// the LogicalCluster lookup fails and admission returns 403 forbidden (see
// PR #205 / commit 3d2d277 for the failure shape). The `edge` WorkspaceType
// has no defaultAPIBindings (it is a pure mount point) but ships here too so
// the `workspace` type's limitAllowedChildren reference to it resolves.
//
//go:embed workspacetype-organization.yaml workspacetype-workspace.yaml workspacetype-edge.yaml
var PostProvidersFS embed.FS
