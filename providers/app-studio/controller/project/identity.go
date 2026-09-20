/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package project

// The per-project identity: the ONE credential everything a project needs
// presents, whether the actor is the project's own workload or this
// provider's reconciler converging that project's dependencies.
//
// There is deliberately no second credential. The reconciler could have used
// the provider's own ServiceAccount over its APIExport virtual workspace, and
// for one commit it did — but serving the dependency kinds there means
// CLAIMING them, and a claim on a first-party (*.railgrid.ai) group pins the
// exact APIExport that serves it by identityHash, for every consuming
// workspace at once. A workspace that binds an org-owned infrastructure or
// code provider is then served nothing, silently. Acting INSIDE the tenant
// workspace instead — at {hub}/clusters/{cluster}, through the workspace's own
// APIBindings — is authorized by that workspace's RBAC, which is true of
// whichever copy it bound. See docs/app-studio-runtime-decoupling.md and
// internal/crossprovider/composition.go.
//
// So the token this file mints carries three things at once:
//
//   - the COMPOSITION the CatalogEntry declares on each dependency
//     (spec.dependencies[].composes, accepted by the tenant at Enable): what
//     the reconcilers do to Instances, Repositories and RepositoryCommits in
//     this workspace, and what the per-workspace dependency watch lists and
//     watches (controller/tenantwatch);
//   - asking the Code provider to commit the workspace, on the action grammar
//     (commitaction.go): `create` on repositories/commit — and on
//     repositories/stage_commit_bundle, for a payload past the catalogue's
//     1 MiB input ceiling — so the commit is authorized as the project rather
//     than as this provider, and the provider writes the RepositoryCommit
//     itself;
//   - the remaining Code MCP tools (checkout, build status, rebuild) through
//     the workspace's MCP aggregate, which admits a caller on `use` of the
//     MCPServer and then forwards this bearer;
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
	"github.com/railgrid/provider-app-studio/internal/codecommit"
	"github.com/railgrid/provider-app-studio/internal/crossprovider"
)

const (
	// infraAPIGroup and codeAPIGroup are the dependency groups a project's
	// bound objects live in. They are FOREIGN groups: nothing here is a claim
	// on them, and every OBJECT verb is name-scoped.
	infraAPIGroup = crossprovider.InfrastructureAPIGroup
	codeAPIGroup  = crossprovider.CodeAPIGroup

	// codeConnectionsResource mirrors the code provider's own plural name
	// (api/code_repository.go). Connections are read, never composed: App
	// Studio asks one for a registry token and otherwise leaves it alone.
	codeConnectionsResource = "connections"

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

// codeConnectionActions are the code-provider actions App Studio invokes on a
// Connection. There is exactly one: `mint_registry_token`
// (api/project_promote.go), which issues the image-pull credential a
// promotion needs.
var codeConnectionActions = []string{"mint_registry_token"}

// codeRepositoryActions are the code-provider actions App Studio invokes on
// the project's Repository, as clause-C {resource}/{verb} capabilities.
//
//	commit               creates the RepositoryCommit for a convergence pass
//	                     (commit.go). The provider writes the CR itself once
//	                     this grant is proven, which is why the composition on
//	                     repositorycommits still carries no create.
//	stage_commit_bundle  uploads a payload past the catalogue's 1 MiB input
//	                     ceiling and hands back the handle `commit` names. It
//	                     is UNCATALOGUED (docs/provider-actions.md
//	                     §"Uncatalogued large-upload verbs"): App Studio cannot
//	                     grant it through a project binding, so the project's
//	                     own identity is the only thing that carries it, and a
//	                     generated application is exactly the payload that
//	                     needs it.
//
// The rest of what App Studio asks the code provider for — checkout_repository,
// build_status, rebuild — is an MCP TOOL, not a declared action, so it is
// reached through the aggregate (clause D) and authorized by the code provider
// against this identity's own RBAC rather than by a {resource}/{verb}
// capability.
var codeRepositoryActions = []string{codecommit.Action, codecommit.StageBundleAction}

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

// projectIdentityRules builds the exact rules a project needs, and nothing
// else. Four clauses, each the narrowest the hub's policy admits
// (pkg/hub/identity/policy.go):
//
//   - D (platform): `use` on the workspace's default MCPServer — the verb the
//     aggregate reviews before admitting a caller — and `get` on the
//     APIBindings that say WHICH provider serves infrastructure and code here.
//     Both by name; a background identity has no interactive caller to have
//     warmed a cache for it, and listing would hand it the full inventory of
//     what the tenant has enabled.
//   - E (composition): what THIS PROVIDER'S RECONCILERS do to the dependency
//     objects inside the workspace, bounded by the composition the CatalogEntry
//     declares and the tenant accepted at Enable
//     (internal/crossprovider/composition.go). Unnamed create/list/watch,
//     because RBAC ignores resourceNames on a collection request; name-scoped
//     get/update/delete on the objects this project is actually bound to.
//   - B (foreign read): `get` on the named Connection this project was created
//     from. Not composed — nothing writes a Connection — but gate 1 on the
//     serving side is a real GET as the caller, so an identity that cannot see
//     the object cannot invoke a verb on it.
//   - C (foreign verb): `create` on the declared {resource}/{verb}
//     subresources of the bound instances, the project's Repository and that
//     Connection, which is how the data plane expresses "may run this verb on
//     this one".
//
// Nothing on this provider's own group: the Project itself is read and written
// over the APIExport virtual workspace, where this provider is already the
// owner of the kind.
//
// A project with no bindings still gets clause D and the unnamed half of the
// composition: reaching the aggregate, resolving where a dependency answers,
// and watching a workspace for the objects that are about to exist are not
// access to any particular object.
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
		crossprovider.InstanceCollectionRule(),
		crossprovider.RepositoryCollectionRule(),
		crossprovider.RepositoryCommitCollectionRule(),
	}

	// The named half of the instance composition, plus clause C on the same
	// objects: one rule per (resource, verb), because the subresource is the
	// coordinate a data-plane grant is expressed on.
	//
	// A binding is normally on the infrastructure provider's one instance kind,
	// which is what the composition declares. A binding that records some other
	// resource is outside it and gets a plain clause-B read instead of the
	// composed write verbs — a template this provider was never declared to
	// compose is not one it may converge.
	instances := projectInstanceNames(p)
	for _, resource := range sortedKeys(instances) {
		names := instances[resource]
		if len(names) == 0 {
			continue
		}
		if resource == crossprovider.InstancesResource {
			if rule, ok := crossprovider.InstanceObjectRule(names); ok {
				rules = append(rules, rule)
			}
		} else {
			rules = append(rules, rbacv1.PolicyRule{
				APIGroups:     []string{infraAPIGroup},
				Resources:     []string{resource},
				ResourceNames: names,
				Verbs:         []string{"get"},
			})
		}
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
		if rule, ok := crossprovider.RepositoryObjectRule([]string{repository}); ok {
			rules = append(rules, rule)
		}
		// Clause C on the same object: the verbs the commit pass invokes.
		for _, action := range codeRepositoryActions {
			rules = append(rules, rbacv1.PolicyRule{
				APIGroups:     []string{codeAPIGroup},
				Resources:     []string{crossprovider.RepositoriesResource + "/" + action},
				ResourceNames: []string{repository},
				Verbs:         []string{"create"},
			})
		}
	}
	// The RepositoryCommit a commit pass is following up, by name. It appears
	// only once there is one (commit.go records the pointer on the Project), so
	// the next mint carries the read and the one after it drops it again —
	// which is the whole point of restating the rules on every refresh.
	if commit := projectPendingCommitRef(p); commit != "" {
		if rule, ok := crossprovider.RepositoryCommitObjectRule([]string{commit}); ok {
			rules = append(rules, rule)
		}
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

// projectPendingCommitRef names the RepositoryCommit this project is waiting
// on. It reads the working-copy ledger on the project's own status, which
// since §9 Cut D.3 is where the pending commit is recorded — the same record
// the convergence loop follows, so the grant and the follow-up can no longer
// name different commits.
func projectPendingCommitRef(p *aiv1alpha1.Project) string {
	if p.Status.Workspace == nil || p.Status.Workspace.PendingCommit == nil {
		return ""
	}
	return strings.TrimSpace(p.Status.Workspace.PendingCommit.Name)
}

// identityToken returns the project's current token, minting or refreshing it
// through the hub. The rules are recomputed from the Project on every call, so
// a rebinding rebuilds the source and the next token carries the new grant.
//
// An empty token with no error means there is no hub to ask (REST-only dev).
// The caller degrades rather than failing: everything the provider does inside
// its OWN virtual workspace — the Project, its status, its finalizers — keeps
// converging, and only the cross-provider half is skipped, because without an
// identity there is no workspace client to do it with.
func (r *Reconciler) identityToken(ctx context.Context, clusterName string, p *aiv1alpha1.Project) (string, error) {
	if !r.Identities.Enabled() {
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
