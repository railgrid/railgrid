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

// KedgeWorkspaceFS contains workspace definitions for children of root:kedge,
// plus the kedge-owned WorkspaceTypes whose defaultAPIBindings do NOT depend
// on an APIExport in root:kedge:providers (so they can be applied before
// providers is fully populated). The `tenant` WorkspaceType only references
// root.tenancy.kcp.io which is always available; the `organization`
// WorkspaceType references the kedge-owned tenancy.kedge.faros.sh APIExport
// and therefore ships in PostProvidersFS instead.
//
//go:embed workspace-providers.yaml workspace-tenants.yaml workspace-users.yaml workspace-orgs.yaml workspacetype-tenant.yaml
var KedgeWorkspaceFS embed.FS

// ProvidersFS contains APIResourceSchemas and APIExport applied to root:kedge:providers.
//
//go:embed apiresourceschema-*.yaml apiexport-*.yaml
var ProvidersFS embed.FS

// PostProvidersFS contains workspace-scoped objects that must be applied in
// root:kedge AFTER ProvidersFS has populated root:kedge:providers with the
// APIExports they reference. Ships the `organization` + `workspace`
// WorkspaceTypes, both of which carry a defaultAPIBinding to
// root:kedge:providers.tenancy.kedge.faros.sh. kcp's WorkspaceType admission
// validates bind permission on every APIExport in defaultAPIBindings; the
// referenced export has to exist by the time the WT is applied, otherwise
// the LogicalCluster lookup fails and admission returns 403 forbidden (see
// PR #205 / commit 3d2d277 for the failure shape).
//
//go:embed workspacetype-organization.yaml workspacetype-workspace.yaml
var PostProvidersFS embed.FS
