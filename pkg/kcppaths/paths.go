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

// Package kcppaths is the single source of truth for the faros kcp workspace
// topology. It has no internal dependencies so every layer (bootstrap,
// provisioner, admin, controllers, proxy) can import it without cycles.
//
// Topology:
//
//	root:faros
//	  providers:<provider>        platform provider sub-workspaces (parent stays universal)
//	  tenants:<uuid>              tenant fleet (org workspaces)
//	    <ws>:<edge>               team + edge workspaces
//	    providers:<provider>      ORG-OWNED (BYO) provider sub-workspaces
//	  system
//	    controllers               ALL platform APIExports + APIResourceSchemas
//	    providers                 Provider + CatalogEntry OBJECTS
//	    tenants                   User/Organization/Membership CR OBJECTS
//
// The naming is symmetric: `providers` + `system:providers` are the provider
// workspaces vs. their objects; `tenants` + `system:tenants` are the tenant
// workspaces vs. their objects.
//
// Org-owned providers mirror the platform layout one level down: the
// well-known `providers` child of an Org workspace is a plain `universal`
// workspace, exactly like root:faros:providers, so each provider under it can
// use the SAME restricted `provider` WorkspaceType (which requires a universal
// parent). That symmetry is deliberate — it lets the provider install path
// (provider-sdk/install, the SA mint, CatalogEntry self-registration) run
// unchanged against an org-owned provider.
package kcppaths

import "strings"

const (
	// Root is the faros root workspace.
	Root = "root:faros"

	// ProvidersParent is the parent of per-provider sub-workspaces. It is NOT
	// where APIExports or Provider/CatalogEntry objects live anymore — only the
	// sub-workspaces root:faros:providers:<name> hang off it.
	ProvidersParent = Root + ":providers"

	// TenantsParent is the parent of per-tenant (organization) workspaces. The
	// tenant *fleet* (org/team/edge workspaces) lives here.
	TenantsParent = Root + ":tenants"

	// System groups the platform-internal workspaces.
	System = Root + ":system"

	// SystemControllers holds ALL platform APIExports + APIResourceSchemas
	// (core / faros / tenancy / providers / admin .faros.sh). Every
	// consumer binds the exports from here.
	SystemControllers = System + ":controllers"

	// SystemProviders holds the Provider + CatalogEntry OBJECTS (and the
	// builtin CatalogEntries). The catalog + provisioning controllers target it.
	SystemProviders = System + ":providers"

	// SystemTenants holds the User / Organization / Membership CR OBJECTS
	// (replaces the former root:faros:users). NOT the tenant workspaces.
	SystemTenants = System + ":tenants"

	// OrgProvidersWorkspaceName is the well-known child of an Org workspace
	// that parents that Org's own (BYO) provider sub-workspaces. It is a
	// reserved name: team workspaces are UUID-named, so it cannot collide.
	OrgProvidersWorkspaceName = "providers"
)

// ProviderPath returns the sub-workspace path for a provider by name:
// root:faros:providers:<name>.
func ProviderPath(name string) string { return ProvidersParent + ":" + name }

// OrgPath returns the tenant workspace path for an org by UUID:
// root:faros:tenants:<uuid>.
func OrgPath(orgUUID string) string { return TenantsParent + ":" + orgUUID }

// WorkspacePath returns the team workspace path within a tenant org:
// root:faros:tenants:<uuid>:<ws>.
func WorkspacePath(orgUUID, wsUUID string) string { return OrgPath(orgUUID) + ":" + wsUUID }

// OrgProvidersParent returns the well-known parent of an Org's own provider
// sub-workspaces: root:faros:tenants:<uuid>:providers.
func OrgProvidersParent(orgUUID string) string {
	return OrgPath(orgUUID) + ":" + OrgProvidersWorkspaceName
}

// OrgProviderPath returns the sub-workspace path for one org-owned provider:
// root:faros:tenants:<uuid>:providers:<name>.
func OrgProviderPath(orgUUID, name string) string {
	return OrgProvidersParent(orgUUID) + ":" + name
}

// SplitOrgProviderPath reports whether path names an org-owned provider
// workspace (root:faros:tenants:<org>:providers:<name>) and, if so, returns the
// owning org UUID and the provider name.
//
// This is the inverse the catalog layer needs: a CatalogEntry is observed by
// logical cluster, and the only way to tell an org-owned provider from a
// platform one — and to attribute it to an org — is the workspace path it
// lives in. Nothing on the entry itself is trustworthy for this, because the
// provider's own `init` writes it.
func SplitOrgProviderPath(path string) (orgUUID, name string, ok bool) {
	rest, found := strings.CutPrefix(path, TenantsParent+":")
	if !found {
		return "", "", false
	}
	orgUUID, rest, found = strings.Cut(rest, ":")
	if !found || orgUUID == "" {
		return "", "", false
	}
	rest, found = strings.CutPrefix(rest, OrgProvidersWorkspaceName+":")
	if !found || rest == "" || strings.Contains(rest, ":") {
		return "", "", false
	}
	return orgUUID, rest, true
}
