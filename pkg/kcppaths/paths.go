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

// Package kcppaths is the single source of truth for the kedge kcp workspace
// topology. It has no internal dependencies so every layer (bootstrap,
// provisioner, admin, controllers, proxy) can import it without cycles.
//
// Topology:
//
//	root:kedge
//	  providers:<provider>        provider sub-workspaces (parent stays universal)
//	  tenants:<uuid>:<ws>:<edge>  tenant fleet (org/team/edge workspaces)
//	  system
//	    controllers               ALL platform APIExports + APIResourceSchemas
//	    providers                 Provider + CatalogEntry OBJECTS
//	    tenants                   User/Organization/Membership CR OBJECTS
//
// The naming is symmetric: `providers` + `system:providers` are the provider
// workspaces vs. their objects; `tenants` + `system:tenants` are the tenant
// workspaces vs. their objects.
package kcppaths

const (
	// Root is the kedge root workspace.
	Root = "root:kedge"

	// ProvidersParent is the parent of per-provider sub-workspaces. It is NOT
	// where APIExports or Provider/CatalogEntry objects live anymore — only the
	// sub-workspaces root:kedge:providers:<name> hang off it.
	ProvidersParent = Root + ":providers"

	// TenantsParent is the parent of per-tenant (organization) workspaces. The
	// tenant *fleet* (org/team/edge workspaces) lives here.
	TenantsParent = Root + ":tenants"

	// System groups the platform-internal workspaces.
	System = Root + ":system"

	// SystemControllers holds ALL platform APIExports + APIResourceSchemas
	// (core / kedge / tenancy / providers / admin .kedge.faros.sh). Every
	// consumer binds the exports from here.
	SystemControllers = System + ":controllers"

	// SystemProviders holds the Provider + CatalogEntry OBJECTS (and the
	// builtin CatalogEntries). The catalog + provisioning controllers target it.
	SystemProviders = System + ":providers"

	// SystemTenants holds the User / Organization / Membership CR OBJECTS
	// (replaces the former root:kedge:users). NOT the tenant workspaces.
	SystemTenants = System + ":tenants"
)

// ProviderPath returns the sub-workspace path for a provider by name:
// root:kedge:providers:<name>.
func ProviderPath(name string) string { return ProvidersParent + ":" + name }

// OrgPath returns the tenant workspace path for an org by UUID:
// root:kedge:tenants:<uuid>.
func OrgPath(orgUUID string) string { return TenantsParent + ":" + orgUUID }

// WorkspacePath returns the team workspace path within a tenant org:
// root:kedge:tenants:<uuid>:<ws>.
func WorkspacePath(orgUUID, wsUUID string) string { return OrgPath(orgUUID) + ":" + wsUUID }
