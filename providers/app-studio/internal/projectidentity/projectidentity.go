/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package projectidentity builds the hub-minted, per-Project workload identity
// shared by App Studio's API and Project reconciler. Keeping the owner and rule
// set here ensures both paths refresh the same identity with the same exact
// grants.
package projectidentity

import (
	"errors"
	"regexp"
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
	infraAPIGroup = crossprovider.InfrastructureAPIGroup
	codeAPIGroup  = crossprovider.CodeAPIGroup

	codeConnectionsResource = "connections"
	defaultMCPServer        = "default"
	mcpServersGroup         = "railgrid.ai"
	mcpServersResource      = "mcpservers"
	templatesResource       = "templates"
)

var (
	instanceDataPlaneVerbs = []string{"env", "exec", "log", "process", "proxy", "restart", "sync", "workspace"}
	codeConnectionActions  = []string{"mint-registry-token"}
	codeRepositoryActions  = []string{codecommit.Action, codecommit.StageBundleAction}
	projectActionDigestRE  = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)
)

// Owner returns the tuple the hub verifies before it mints an identity. The
// Project UID is part of the owner so a deleted and recreated Project never
// inherits its predecessor's credential.
func Owner(p *aiv1alpha1.Project, clusterID string) identityclient.Owner {
	if p == nil {
		return identityclient.Owner{ClusterID: clusterID}
	}
	return identityclient.Owner{
		Kind:      "Project",
		Group:     aiv1alpha1.SchemeGroupVersion.Group,
		Version:   aiv1alpha1.SchemeGroupVersion.Version,
		Resource:  "projects",
		Name:      p.Name,
		UID:       string(p.UID),
		ClusterID: clusterID,
	}
}

// OwnerRevision is the persisted observation used to derive Rules. The
// Project generation fences spec changes; resourceVersion also changes for
// status updates that can affect permissions, such as pending commit state.
// The hub treats resourceVersion as opaque and checks it for exact equality.
func OwnerRevision(p *aiv1alpha1.Project) identityclient.OwnerRevision {
	if p == nil {
		return identityclient.OwnerRevision{}
	}
	return identityclient.OwnerRevision{
		Generation:      p.Generation,
		ResourceVersion: p.ResourceVersion,
	}
}

// IsRevisionConflict reports the two hub refusals that can be resolved by
// re-reading the Project and re-deriving its rules. Policy refusals and an
// unsupported hub are not retried.
func IsRevisionConflict(err error) bool {
	var hubErr *identityclient.Error
	if !errors.As(err, &hubErr) {
		return false
	}
	return hubErr.Code == identityclient.ErrorCodeStaleOwner || hubErr.Code == identityclient.ErrorCodeVersionConflict
}

// IsStaleOwnerRevision reports a refusal caused by the Project observation
// changing before the hub accepted the request. Retrying the same revision
// cannot help; callers must first obtain a different fresh observation.
func IsStaleOwnerRevision(err error) bool {
	var hubErr *identityclient.Error
	return errors.As(err, &hubErr) && hubErr.Code == identityclient.ErrorCodeStaleOwner
}

// Rules returns the exact permissions a Project needs. Foreign object reads
// are name-scoped; foreign action capabilities are included only for active,
// audited providerReference grants persisted on this Project. The hub's
// identity policy independently checks that each target provider is enabled
// and declares each action.
func Rules(p *aiv1alpha1.Project) []rbacv1.PolicyRule {
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
			ResourceNames: sortedUnique([]string{
				crossprovider.ProviderNameForExport(crossprovider.InfrastructureAPIExport),
				crossprovider.ProviderNameForExport(crossprovider.CodeAPIExport),
			}),
			Verbs: []string{"get"},
		},
	}
	if p == nil {
		return rules
	}

	if template := projectTemplateRef(p); template != "" {
		rules = append(rules, rbacv1.PolicyRule{
			APIGroups: []string{infraAPIGroup}, Resources: []string{templatesResource},
			ResourceNames: []string{template}, Verbs: []string{"get"},
		})
	}

	instances := projectInstanceNames(p)
	for _, resource := range sortedKeys(instances) {
		names := instances[resource]
		if len(names) == 0 {
			continue
		}
		rules = append(rules, rbacv1.PolicyRule{
			APIGroups: []string{infraAPIGroup}, Resources: []string{resource},
			ResourceNames: names, Verbs: []string{"get"},
		})
		for _, verb := range instanceDataPlaneVerbs {
			rules = append(rules, rbacv1.PolicyRule{
				APIGroups: []string{infraAPIGroup}, Resources: []string{resource + "/" + verb},
				ResourceNames: names, Verbs: []string{"create"},
			})
		}
	}

	if repository := projectRepositoryRef(p); repository != "" {
		rules = append(rules, rbacv1.PolicyRule{
			APIGroups: []string{codeAPIGroup}, Resources: []string{crossprovider.RepositoriesResource},
			ResourceNames: []string{repository}, Verbs: []string{"get"},
		})
		for _, action := range codeRepositoryActions {
			rules = append(rules, rbacv1.PolicyRule{
				APIGroups:     []string{codeAPIGroup},
				Resources:     []string{crossprovider.RepositoriesResource + "/" + action},
				ResourceNames: []string{repository}, Verbs: []string{"create"},
			})
		}
	}
	if commit := projectPendingCommitRef(p); commit != "" {
		rules = append(rules, rbacv1.PolicyRule{
			APIGroups: []string{codeAPIGroup}, Resources: []string{crossprovider.RepositoryCommitsResource},
			ResourceNames: []string{commit}, Verbs: []string{"get"},
		})
	}
	if connection := projectConnectionRef(p); connection != "" {
		rules = append(rules, rbacv1.PolicyRule{
			APIGroups: []string{codeAPIGroup}, Resources: []string{codeConnectionsResource},
			ResourceNames: []string{connection}, Verbs: []string{"get"},
		})
		for _, action := range codeConnectionActions {
			rules = append(rules, rbacv1.PolicyRule{
				APIGroups:     []string{codeAPIGroup},
				Resources:     []string{codeConnectionsResource + "/" + action},
				ResourceNames: []string{connection}, Verbs: []string{"create"},
			})
		}
	}

	rules = append(rules, providerReferenceRules(p)...)
	return rules
}

type providerReferenceCoordinate struct {
	group    string
	resource string
	name     string
}

func providerReferenceRules(p *aiv1alpha1.Project) []rbacv1.PolicyRule {
	if p == nil {
		return nil
	}
	reads := map[providerReferenceCoordinate]struct{}{}
	actions := map[providerReferenceCoordinate]map[string]struct{}{}
	for _, env := range p.Spec.Environments {
		for _, binding := range env.Bindings {
			if binding.Kind != aiv1alpha1.ProjectBindingKindProviderReference || strings.TrimSpace(binding.Provider) == "" || binding.ResourceRef == nil {
				continue
			}
			ref := binding.ResourceRef
			gvr, err := bindings.GVR(ref)
			name := strings.TrimSpace(ref.Name)
			if err != nil || gvr.Group == "" || gvr.Version == "" || name == "" {
				continue
			}
			coordinate := providerReferenceCoordinate{group: gvr.Group, resource: gvr.Resource, name: name}
			for _, action := range binding.AllowedActions {
				if action.Revoked || !validPersistedGrant(action) || !validSubresourceName(action.Name) {
					continue
				}
				if actions[coordinate] == nil {
					actions[coordinate] = map[string]struct{}{}
				}
				actions[coordinate][strings.TrimSpace(action.Name)] = struct{}{}
				reads[coordinate] = struct{}{}
			}
		}
	}

	keys := make([]providerReferenceCoordinate, 0, len(reads))
	for coordinate := range reads {
		keys = append(keys, coordinate)
	}
	sort.Slice(keys, func(i, j int) bool {
		left, right := keys[i], keys[j]
		if left.group != right.group {
			return left.group < right.group
		}
		if left.resource != right.resource {
			return left.resource < right.resource
		}
		return left.name < right.name
	})

	rules := make([]rbacv1.PolicyRule, 0, len(keys)*2)
	for _, coordinate := range keys {
		rules = append(rules, rbacv1.PolicyRule{
			APIGroups: []string{coordinate.group}, Resources: []string{coordinate.resource},
			ResourceNames: []string{coordinate.name}, Verbs: []string{"get"},
		})
		verbs := sortedKeys(actions[coordinate])
		if len(verbs) == 0 {
			continue
		}
		subresources := make([]string, 0, len(verbs))
		for _, verb := range verbs {
			subresources = append(subresources, coordinate.resource+"/"+verb)
		}
		rules = append(rules, rbacv1.PolicyRule{
			APIGroups: []string{coordinate.group}, Resources: subresources,
			ResourceNames: []string{coordinate.name}, Verbs: []string{"create"},
		})
	}
	return rules
}

func validPersistedGrant(action aiv1alpha1.ProjectProviderActionSpec) bool {
	return strings.TrimSpace(action.GrantedBy) != "" && action.GrantedAt != nil && !action.GrantedAt.IsZero() &&
		projectActionDigestRE.MatchString(strings.TrimSpace(action.SchemaDigest))
}

func validSubresourceName(name string) bool {
	name = strings.TrimSpace(name)
	return name != "" && !strings.ContainsAny(name, "/\\\r\n\x00 ")
}

func projectInstanceNames(p *aiv1alpha1.Project) map[string][]string {
	found := map[string]map[string]bool{}
	for _, env := range p.Spec.Environments {
		for _, binding := range env.Bindings {
			if binding.Kind != aiv1alpha1.ProjectBindingKindProviderResource || binding.ResourceRef == nil {
				continue
			}
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

func projectTemplateRef(p *aiv1alpha1.Project) string {
	if p == nil || p.Spec.Template == nil {
		return ""
	}
	return strings.TrimSpace(p.Spec.Template.Name)
}

func projectConnectionRef(p *aiv1alpha1.Project) string {
	if p.Spec.Repository == nil {
		return ""
	}
	return strings.TrimSpace(p.Spec.Repository.ConnectionRef)
}

func projectPendingCommitRef(p *aiv1alpha1.Project) string {
	if p.Status.Workspace == nil || p.Status.Workspace.PendingCommit == nil {
		return ""
	}
	return strings.TrimSpace(p.Status.Workspace.PendingCommit.Name)
}

func sortedKeys[V any](values map[string]V) []string {
	out := make([]string, 0, len(values))
	for key := range values {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

func sortedUnique(values []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}
