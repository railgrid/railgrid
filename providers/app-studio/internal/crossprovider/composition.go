/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package crossprovider

// The composition: what App Studio's own reconcilers do to a dependency's
// objects, inside the tenant's workspace, as a hub-minted scoped identity.
//
// This is the second half of a declaration, not a new power. The CatalogEntry
// declares it as `spec.dependencies[].composes` (manifest.yaml and the chart's
// copy, identically), the tenant consents to it at Enable, and the hub's
// identity policy (clause E, composition) will only admit a rule that some
// declared composition covers. The rules built here are therefore a mirror of
// that declaration, and the two are kept in step by
// TestCompositionRulesMatchTheManifest.
//
// Why this rather than an APIExport permission claim, which is what the shape
// of the work would first suggest: a claim on a FIRST-PARTY group has to pin
// the serving export's identityHash, and an export pins exactly one identity
// per claimed resource for every consuming workspace at once. A workspace that
// binds an org-owned infrastructure or code provider then gets nothing served —
// silently. Acting inside the workspace, through the workspace's own bindings,
// is authorized by that workspace's RBAC instead, which works for whichever
// copy it bound. See docs/app-studio-runtime-decoupling.md.
//
// Two rule shapes exist because Kubernetes RBAC has two:
//
//   - COLLECTION verbs (create, list, watch) do not take resourceNames at all —
//     a rule with names and verb `list` authorizes nothing — so they are
//     unnamed, bounded instead by the declared (group, resource) pair.
//   - OBJECT verbs (get, update, patch, delete) are always name-scoped, to the
//     exact objects the owner is bound to.

import (
	rbacv1 "k8s.io/api/rbac/v1"
)

const (
	// InfrastructureAPIGroup / CodeAPIGroup are the dependency groups. They are
	// FOREIGN groups: nothing here is a claim on them, and every object verb is
	// name-scoped.
	InfrastructureAPIGroup = "infrastructure.railgrid.ai"
	CodeAPIGroup           = "code.railgrid.ai"

	// InstancesResource is the infrastructure provider's one instance kind.
	// A Project binding records a per-template resource name that resolves to
	// it; a binding naming anything else is not part of the declared
	// composition and gets a plain foreign read instead.
	InstancesResource = "instances"
	// RepositoriesResource / RepositoryCommitsResource are the code provider's
	// plural names (providers/code api/code_repository.go).
	RepositoriesResource      = "repositories"
	RepositoryCommitsResource = "repositorycommits"
)

// Collection verbs, per composed resource. They are what the reconcilers and
// the per-workspace dependency watch (controller/tenantwatch) actually issue:
//
//	instances          created by the Project and Studio reconcilers; listed
//	                   and watched so readiness arrives as an event.
//	repositories       created with autoInit; listed and watched.
//	repositorycommits  listed and watched only — the CR is created by the Code
//	                   provider itself, behind its repositories/commit action,
//	                   because it is a POINTER at a source bundle only that
//	                   provider can store. App Studio holds the VERB (a
//	                   clause-C capability on the project's Repository), not
//	                   create on the kind.
var (
	instanceCollectionVerbs         = []string{"create", "list", "watch"}
	repositoryCollectionVerbs       = []string{"create", "list", "watch"}
	repositoryCommitCollectionVerbs = []string{"list", "watch"}
)

// Object verbs, per composed resource, always against named objects.
//
//	instances          converged on drift and deleted on the owner's finalizer.
//	repositories       converged, and released on the owner's finalizer — the
//	                   claim label is cleared so the repository can be imported
//	                   again. Deletion is also here, and it is the exception
//	                   rather than the rule: a project's teardown deletes the
//	                   repository ONLY when that deletion explicitly asked for
//	                   it (Project annotation ai.railgrid.ai/delete-repository)
//	                   and only one App Studio created for that exact project
//	                   incarnation — never an adopted one. It used to be the
//	                   human's own `delete` through the data-plane verb; the
//	                   verb is gone (Cut D.4) and the teardown that replaced it
//	                   runs as the project identity, so the capability moved
//	                   with it. It stays NAME-SCOPED to the project's own
//	                   repository, which is what keeps "delete a project" from
//	                   becoming "delete this workspace's code".
//	repositorycommits  read while a commit is in flight; nothing writes one.
var (
	instanceObjectVerbs         = []string{"get", "update", "delete"}
	repositoryObjectVerbs       = []string{"get", "update", "delete"}
	repositoryCommitObjectVerbs = []string{"get"}
)

// InstanceCollectionRule is the unnamed half of the instance composition.
func InstanceCollectionRule() rbacv1.PolicyRule {
	return rule(InfrastructureAPIGroup, InstancesResource, nil, instanceCollectionVerbs)
}

// InstanceObjectRule is the named half, for the instances one owner is bound
// to. No names yields no rule: an owner that is bound to nothing is granted
// nothing, rather than a rule that would read as "all of them".
func InstanceObjectRule(names []string) (rbacv1.PolicyRule, bool) {
	return namedRule(InfrastructureAPIGroup, InstancesResource, names, instanceObjectVerbs)
}

// RepositoryCollectionRule is the unnamed half of the repository composition.
func RepositoryCollectionRule() rbacv1.PolicyRule {
	return rule(CodeAPIGroup, RepositoriesResource, nil, repositoryCollectionVerbs)
}

// RepositoryObjectRule is the named half, for a project's backing repository.
func RepositoryObjectRule(names []string) (rbacv1.PolicyRule, bool) {
	return namedRule(CodeAPIGroup, RepositoriesResource, names, repositoryObjectVerbs)
}

// RepositoryCommitCollectionRule is the unnamed half of the commit
// composition. There is no create: see repositoryCommitCollectionVerbs.
func RepositoryCommitCollectionRule() rbacv1.PolicyRule {
	return rule(CodeAPIGroup, RepositoryCommitsResource, nil, repositoryCommitCollectionVerbs)
}

// RepositoryCommitObjectRule is the named half, for the commit a project is
// currently following up.
func RepositoryCommitObjectRule(names []string) (rbacv1.PolicyRule, bool) {
	return namedRule(CodeAPIGroup, RepositoryCommitsResource, names, repositoryCommitObjectVerbs)
}

func rule(group, resource string, names, verbs []string) rbacv1.PolicyRule {
	return rbacv1.PolicyRule{
		APIGroups:     []string{group},
		Resources:     []string{resource},
		ResourceNames: names,
		Verbs:         verbs,
	}
}

func namedRule(group, resource string, names, verbs []string) (rbacv1.PolicyRule, bool) {
	if len(names) == 0 {
		return rbacv1.PolicyRule{}, false
	}
	return rule(group, resource, names, verbs), true
}
