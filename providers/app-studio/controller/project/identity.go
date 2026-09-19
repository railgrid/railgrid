/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package project

// The per-project identity: what a PROJECT acts as, not what this provider
// reconciles as.
//
// The distinction is the whole design, and getting it wrong is what the first
// cut of this file did. Reconciliation — creating the bound Instances,
// converging the Repository, watching both — is the provider's own background
// work, so it runs as the provider's ServiceAccount through its APIExport
// virtual workspace with the claims the tenant accepted at Enable
// (AGENTS.md §5.4, manifest.yaml spec.apiExport.permissionClaims). None of it
// is here.
//
// What IS here is the credential a project needs when something acts AS THE
// PROJECT with no human behind it:
//
//   - asking the Code provider to commit the workspace, through the MCP
//     aggregate (commit.go) — the aggregate admits a caller on `use` of the
//     workspace's MCPServer and then forwards this bearer, so the commit is
//     authorized as the project rather than as this provider;
//   - the project's workload calling a data-plane verb on its own instance,
//     where the serving provider re-reads the addressed object AS THE CALLER
//     (gate 1) before it will run anything (gate 2).
//
// It ASKS THE HUB for that credential instead of minting it:
//
//	Before                                  Now
//	------                                  ---
//	The provider wrote a ServiceAccount,    The hub mints it, against a policy
//	a ClusterRole, a binding and a legacy   that checks every rule, records
//	token Secret into the tenant's          what it issued, and collects it
//	workspace with its own claimed          when the Project is deleted.
//	credentials.
//
//	The ClusterRole granted every verb      The rules name the exact Instance,
//	on EVERY resource in                    Repository and Connection this
//	infrastructure.railgrid.ai and          project is bound to, by name.
//	code.railgrid.ai.
//
//	The token never expired.                TTL'd, re-minted at 80% of its
//	                                        life, revoked on delete.
//
//	Rules were create-if-absent, so a       Every refresh re-states the rules,
//	grant never shrank when a project's     so rebinding drops the access the
//	bindings changed.                       old binding carried.
//
// The provider needs NO serviceaccounts, clusterroles or clusterrolebindings
// permission claims for any of this — see provider-sdk/identityclient and
// docs/provider-connectivity-contract.md §"Scoped identities".

import (
	"context"
	"sort"
	"strings"

	rbacv1 "k8s.io/api/rbac/v1"

	"github.com/railgrid/provider-sdk/identityclient"

	aiv1alpha1 "github.com/railgrid/provider-app-studio/apis/ai/v1alpha1"
	"github.com/railgrid/provider-app-studio/bindings"
	"github.com/railgrid/provider-app-studio/internal/crossprovider"
)

const (
	// infraAPIGroup and codeAPIGroup are the dependency groups a project's
	// bound objects live in. They are FOREIGN groups: everything this identity
	// holds on them is name-scoped.
	infraAPIGroup = "infrastructure.railgrid.ai"
	codeAPIGroup  = "code.railgrid.ai"

	// codeRepositoriesResource / codeConnectionsResource mirror the code
	// provider's own plural names (api/code_repository.go).
	codeRepositoriesResource = "repositories"
	codeConnectionsResource  = "connections"

	// defaultMCPServer is the workspace's MCP aggregate. Admission to it
	// confers nothing downstream — federation keeps forwarding this identity's
	// own bearer to each provider — so what the grant buys is the door, not
	// what is behind it.
	defaultMCPServer = "default"

	// mcpServersGroup is the hub's own group, which carries the `use` verb the
	// aggregate reviews before admitting a caller.
	mcpServersGroup    = "railgrid.ai"
	mcpServersResource = "mcpservers"
)

// instanceDataPlaneVerbs are the infrastructure data-plane verbs App Studio
// invokes on a project's instance. Derived from the verb constants in
// api/dataplane_client.go, which are the only ones this provider ever
// addresses:
//
//	env       development_runtime.go  (read/write the sandbox environment)
//	exec      assistant_exec_command.go
//	log       development logs, assistant log tool
//	process   assistant process listing
//	proxy     preview bridge, browser MCP, published-app access
//	restart   restart-development
//	sync      sync-development, workspace hydration
//	workspace assistant sandbox file read/write
//
// The infrastructure provider declares all eight in its CatalogEntry
// (providers/infrastructure/manifest.yaml spec.dataPlane.verbs); the hub's
// policy (clause C) will only mint a capability for a verb it can verify
// exists there. `status` is declared there too and is deliberately absent
// here: nothing in this provider calls it.
var instanceDataPlaneVerbs = []string{"env", "exec", "log", "process", "proxy", "restart", "sync", "workspace"}

// codeConnectionActions are the code-provider ACTIONS App Studio invokes, as
// {resource}/{action} coordinates.
//
// There is exactly one, and it is on connections rather than repositories:
// `mint_registry_token` (api/project_promote.go), which issues the image-pull
// credential a promotion needs. Everything else App Studio asks the code
// provider for — commit_files, checkout_repository, build_status, rebuild — is
// an MCP TOOL, not a declared action, so it is reached through the aggregate
// (clause D) and authorized by the code provider against this identity's own
// RBAC rather than by a {resource}/{verb} capability.
var codeConnectionActions = []string{"mint_registry_token"}

// projectOwner is the tuple the hub verifies before it mints anything: the
// Project must exist in this workspace with this UID. The ServiceAccount name
// is a hash of it, so a deleted and recreated project never inherits its
// predecessor's credential.
func projectOwner(p *aiv1alpha1.Project, clusterName string) identityclient.Owner {
	return identityclient.Owner{
		Kind:      "Project",
		Group:     aiv1alpha1.SchemeGroupVersion.Group,
		Version:   aiv1alpha1.SchemeGroupVersion.Version,
		Resource:  "projects",
		Name:      p.Name,
		UID:       string(p.UID),
		ClusterID: clusterName,
	}
}

// projectIdentityRules builds the exact rules a project needs to act as
// itself, and nothing else. Three clauses, each the narrowest the hub's policy
// admits (pkg/hub/identity/policy.go):
//
//   - D (platform): `use` on the workspace's default MCPServer — the verb the
//     aggregate reviews before admitting a caller — and `get` on the
//     APIBindings that say WHICH provider serves infrastructure and code here.
//     Both by name; a background identity has no interactive caller to have
//     warmed a cache for it, and listing would hand it the full inventory of
//     what the tenant has enabled.
//   - B (foreign read): `get` on the named Instance, Repository and Connection
//     this project is bound to. Not so this provider can read them — it reads
//     them over its own virtual workspace — but because gate 1 on the serving
//     side is a real GET as the caller: an identity that cannot see the object
//     cannot invoke a verb on it.
//   - C (foreign verb): `create` on the declared {resource}/{verb}
//     subresources of those same objects, which is how the data plane
//     expresses "may run this verb on this one".
//
// Nothing on this provider's own group: a project acting as itself has no
// business writing Projects, and the reconciler that does holds a different
// credential entirely.
//
// A project with no bindings still gets clause D: resolving where a dependency
// answers, and reaching the aggregate, is not access to anything.
func projectIdentityRules(p *aiv1alpha1.Project) []rbacv1.PolicyRule {
	rules := []rbacv1.PolicyRule{
		{
			APIGroups:     []string{mcpServersGroup},
			Resources:     []string{mcpServersResource},
			ResourceNames: []string{defaultMCPServer},
			Verbs:         []string{"use"},
		},
		{
			APIGroups: []string{crossprovider.APIBindingsGVR.Group},
			Resources: []string{crossprovider.APIBindingsGVR.Resource},
			ResourceNames: dedupeSorted([]string{
				crossprovider.ProviderNameForExport(crossprovider.InfrastructureAPIExport),
				crossprovider.ProviderNameForExport(crossprovider.CodeAPIExport),
			}),
			Verbs: []string{"get"},
		},
	}

	// Clause B and C on the bound instances, one rule per (resource, verb)
	// because the subresource is the coordinate the grant is expressed on.
	instances := projectInstanceNames(p)
	for _, resource := range sortedKeys(instances) {
		names := instances[resource]
		if len(names) == 0 {
			continue
		}
		rules = append(rules, rbacv1.PolicyRule{
			APIGroups:     []string{infraAPIGroup},
			Resources:     []string{resource},
			ResourceNames: names,
			Verbs:         []string{"get"},
		})
		for _, verb := range instanceDataPlaneVerbs {
			rules = append(rules, rbacv1.PolicyRule{
				APIGroups:     []string{infraAPIGroup},
				Resources:     []string{resource + "/" + verb},
				ResourceNames: names,
				Verbs:         []string{"create"},
			})
		}
	}

	if repository := projectRepositoryRef(p); repository != "" {
		rules = append(rules, rbacv1.PolicyRule{
			APIGroups:     []string{codeAPIGroup},
			Resources:     []string{codeRepositoriesResource},
			ResourceNames: []string{repository},
			Verbs:         []string{"get"},
		})
	}
	if connection := projectConnectionRef(p); connection != "" {
		rules = append(rules, rbacv1.PolicyRule{
			APIGroups:     []string{codeAPIGroup},
			Resources:     []string{codeConnectionsResource},
			ResourceNames: []string{connection},
			Verbs:         []string{"get"},
		})
		for _, action := range codeConnectionActions {
			rules = append(rules, rbacv1.PolicyRule{
				APIGroups:     []string{codeAPIGroup},
				Resources:     []string{codeConnectionsResource + "/" + action},
				ResourceNames: []string{connection},
				Verbs:         []string{"create"},
			})
		}
	}
	return rules
}

// projectInstanceNames collects the instances a project is bound to, as
// resource → sorted names. A binding whose desired name cannot be resolved
// contributes nothing: it names no object, so there is nothing to be granted
// on.
func projectInstanceNames(p *aiv1alpha1.Project) map[string][]string {
	found := map[string]map[string]bool{}
	for _, env := range providerBindings(p) {
		for _, binding := range env.bindings {
			gvr, err := bindings.GVR(binding.ResourceRef)
			if err != nil || gvr.Group != infraAPIGroup || gvr.Resource == "" {
				continue
			}
			values, err := bindings.Values(binding)
			if err != nil {
				continue
			}
			name := bindings.ResourceName(p, binding, values)
			if name == "" {
				continue
			}
			if found[gvr.Resource] == nil {
				found[gvr.Resource] = map[string]bool{}
			}
			found[gvr.Resource][name] = true
		}
	}
	out := make(map[string][]string, len(found))
	for resource, names := range found {
		out[resource] = sortedKeys(names)
	}
	return out
}

func projectRepositoryRef(p *aiv1alpha1.Project) string {
	if p.Spec.Repository == nil {
		return ""
	}
	return strings.TrimSpace(p.Spec.Repository.RepositoryRef)
}

func projectConnectionRef(p *aiv1alpha1.Project) string {
	if p.Spec.Repository == nil {
		return ""
	}
	return strings.TrimSpace(p.Spec.Repository.ConnectionRef)
}

// identityToken returns the project's current token, minting or refreshing it
// through the hub. The rules are recomputed from the Project on every call, so
// a rebinding rebuilds the source and the next token carries the new grant.
//
// An empty token with no error means there is no hub to ask (REST-only dev).
// The caller degrades — the commit path waits for a hub rather than failing
// the reconcile, and everything the provider does as itself is unaffected.
func (r *Reconciler) identityToken(ctx context.Context, clusterName string, p *aiv1alpha1.Project) (string, error) {
	if !r.Identities.Enabled() {
		// No hub to ask (REST-only dev): the claimed-VW fallback in
		// tenantClient carries the reconcile, with its own warning.
		return "", nil
	}
	return r.Identities.Token(ctx, projectOwner(p, clusterName), projectIdentityRules(p))
}

// releaseIdentity revokes the project's identity now rather than waiting for
// the hub's sweep to notice the Project is gone (up to one token TTL later).
func (r *Reconciler) releaseIdentity(ctx context.Context, clusterName string, p *aiv1alpha1.Project) error {
	if !r.Identities.Enabled() {
		return nil
	}
	return r.Identities.Release(ctx, projectOwner(p, clusterName))
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for key := range m {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

func dedupeSorted(values []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}
