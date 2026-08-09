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

package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"reflect"
	"regexp"
	"sort"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	aiv1alpha1 "github.com/faroshq/provider-app-studio/apis/ai/v1alpha1"
	asclient "github.com/faroshq/provider-app-studio/client"
	"github.com/faroshq/provider-app-studio/tenant"
)

const (
	// automaticProviderActionGrantedBy is deliberately a server-owned audit
	// identity. It distinguishes this temporary compatibility path from the
	// user-consent grant path, which records the authenticated caller.
	automaticProviderActionGrantedBy        = "server:automatic-provider-actions"
	automaticProviderIntegrationPrefix      = "auto-"
	automaticProviderIntegrationEnvironment = projectIntegrationDefaultEnvironment
)

var automaticIntegrationAliasPartRE = regexp.MustCompile(`[^a-z0-9_-]+`)

// automaticProviderCatalogResource groups all eligible actions that are bound
// to one provider GVR/kind. A single project binding is materialized per
// provider-owned object, with every eligible action for that object's exact
// resource reference in its allow-list.
type automaticProviderCatalogResource struct {
	provider   string
	apiVersion string
	kind       string
	resource   string
	gvr        schema.GroupVersionResource
	actions    []automaticProviderCatalogAction
}

type automaticProviderCatalogAction struct {
	name         string
	version      string
	schemaDigest string
}

type automaticIntegrationTarget struct {
	provider string
	ref      *aiv1alpha1.ProjectProviderResourceReference
	actions  []aiv1alpha1.ProjectProviderActionSpec
}

// materializeAutomaticProjectIntegrations temporarily removes the requirement
// for a user-created integration grant before an assistant turn. It is
// intentionally best-effort for catalog and resource-list failures: an
// actionless turn must remain usable when a provider is unavailable. Once a
// target has been discovered, project persistence and runtime reconciliation
// are strict so the assistant never receives a stale Project after a reported
// successful materialization.
//
// TODO(least-privilege): restore explicit user consent and least-privilege
// action grants after the temporary automatic-access rollout is retired.
func (s *Server) materializeAutomaticProjectIntegrations(ctx context.Context, c *asclient.Client, id identity, project *aiv1alpha1.Project) (*aiv1alpha1.Project, error) {
	if s == nil || c == nil || project == nil {
		return project, nil
	}
	catalog, err := s.providerActionCatalog(ctx, id)
	if err != nil {
		return project, nil
	}
	resources := automaticProviderCatalogResources(catalog)
	if len(resources) == 0 {
		return project, nil
	}
	targets := make([]automaticIntegrationTarget, 0)
	for _, resource := range resources {
		list, listErr := c.Resource(automaticProviderResource(resource.gvr, resource.kind, resource.resource), "").List(ctx, metav1.ListOptions{})
		if listErr != nil || list == nil {
			// A denied or temporarily unavailable provider resource must not
			// turn an otherwise actionless assistant turn into a provider error.
			continue
		}
		for _, object := range list.Items {
			name := strings.TrimSpace(object.GetName())
			if name == "" {
				continue
			}
			ref := &aiv1alpha1.ProjectProviderResourceReference{
				Name: name, APIVersion: resource.apiVersion, Kind: resource.kind, Resource: resource.resource,
			}
			actions := make([]aiv1alpha1.ProjectProviderActionSpec, 0, len(resource.actions))
			for _, action := range resource.actions {
				actions = append(actions, aiv1alpha1.ProjectProviderActionSpec{
					Name: action.name, Version: action.version, SchemaDigest: action.schemaDigest,
				})
			}
			targets = append(targets, automaticIntegrationTarget{provider: resource.provider, ref: ref, actions: actions})
		}
	}
	if len(targets) == 0 {
		return project, nil
	}
	sort.Slice(targets, func(i, j int) bool {
		return automaticProviderReferenceKey(targets[i].provider, targets[i].ref) < automaticProviderReferenceKey(targets[j].provider, targets[j].ref)
	})

	next := project.DeepCopy()
	changed := materializeAutomaticIntegrationTargets(next, targets)
	if !changed {
		// Automatic discovery only writes the Project's providerReference
		// bindings. Provider-owned resources are converged by the Project
		// controller; this turn-start path must never create or update them.
		return project, nil
	}
	updated, err := c.Projects().Update(ctx, next, metav1.UpdateOptions{})
	if err != nil {
		return nil, fmt.Errorf("persist automatic provider integrations: %w", err)
	}
	// Do not synchronously reconcile provider resources here. The controller is
	// the sole owner of provider-resource writes, while read-through status
	// remains available to subsequent project/integration reads.
	return updated, nil
}

func automaticProviderCatalogResources(catalog []providerCatalogEntry) []automaticProviderCatalogResource {
	byKey := map[string]automaticProviderCatalogResource{}
	for _, provider := range catalog {
		providerName := strings.TrimSpace(provider.Name)
		if providerName == "" || !provider.Ready {
			continue
		}
		for _, action := range provider.Actions {
			name, version, ok := automaticCatalogActionIdentity(action)
			if !ok {
				continue
			}
			bound := action.BoundResource
			apiVersion := strings.TrimSpace(bound.APIVersion)
			kind := strings.TrimSpace(bound.Kind)
			resource := strings.TrimSpace(bound.Resource)
			gv, err := schema.ParseGroupVersion(apiVersion)
			if err != nil || apiVersion == "" || kind == "" || resource == "" {
				continue
			}
			key := automaticProviderCatalogResourceKey(providerName, gv.WithResource(resource), kind, resource)
			group := byKey[key]
			if group.provider == "" {
				group = automaticProviderCatalogResource{
					provider: providerName, apiVersion: apiVersion, kind: kind, resource: resource,
					gvr: gv.WithResource(resource),
				}
			}
			group.actions = append(group.actions, automaticProviderCatalogAction{name: name, version: version, schemaDigest: strings.TrimSpace(action.SchemaDigest)})
			byKey[key] = group
		}
	}
	resources := make([]automaticProviderCatalogResource, 0, len(byKey))
	for _, resource := range byKey {
		// Catalog entries are external data. Keep one deterministic action
		// contract when a provider accidentally publishes the same ID twice.
		sort.Slice(resource.actions, func(i, j int) bool {
			left, right := resource.actions[i], resource.actions[j]
			if left.name != right.name {
				return left.name < right.name
			}
			if left.version != right.version {
				return left.version < right.version
			}
			return left.schemaDigest < right.schemaDigest
		})
		deduped := resource.actions[:0]
		seen := map[string]struct{}{}
		for _, action := range resource.actions {
			key := action.name + "\x00" + action.version
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			deduped = append(deduped, action)
		}
		resource.actions = deduped
		resources = append(resources, resource)
	}
	sort.Slice(resources, func(i, j int) bool {
		return automaticProviderCatalogResourceKey(resources[i].provider, resources[i].gvr, resources[i].kind, resources[i].resource) < automaticProviderCatalogResourceKey(resources[j].provider, resources[j].gvr, resources[j].kind, resources[j].resource)
	})
	return resources
}

func automaticCatalogActionIdentity(action providerCatalogAction) (string, string, bool) {
	name, version, ok := splitProviderCatalogActionID(action.ID)
	if !ok {
		return "", "", false
	}
	name, version, err := normalizeIntegrationAction(name, version)
	if err != nil || !projectActionSchemaDigestRE.MatchString(strings.TrimSpace(action.SchemaDigest)) {
		return "", "", false
	}
	if action.Deprecation != nil && action.Deprecation.Deprecated {
		return "", "", false
	}
	return name, version, true
}

func automaticProviderCatalogResourceKey(provider string, gvr schema.GroupVersionResource, kind, resource string) string {
	return strings.TrimSpace(provider) + "\x00" + gvr.Group + "\x00" + gvr.Version + "\x00" + strings.TrimSpace(kind) + "\x00" + strings.TrimSpace(resource)
}

// automaticProviderResource carries the published plural resource name into
// the GraphQL-backed tenant client. Kind+s is not correct for irregular
// plurals (and is unnecessary because catalog actions already publish the
// exact resource segment).
func automaticProviderResource(gvr schema.GroupVersionResource, kind, resource string) tenant.Resource {
	plural := strings.TrimSpace(resource)
	if plural != "" {
		plural = strings.ToUpper(plural[:1]) + plural[1:]
	}
	return tenant.Resource{GVR: gvr, Kind: strings.TrimSpace(kind), Plural: plural}
}

func automaticProviderReferenceKey(provider string, ref *aiv1alpha1.ProjectProviderResourceReference) string {
	if ref == nil {
		return ""
	}
	gvr, err := projectProviderResourceGVR(ref)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(provider) + "\x00" + gvr.Group + "\x00" + gvr.Version + "\x00" + strings.TrimSpace(ref.Kind) + "\x00" + gvr.Resource + "\x00" + strings.TrimSpace(ref.Name)
}

func materializeAutomaticIntegrationTargets(project *aiv1alpha1.Project, targets []automaticIntegrationTarget) bool {
	if project == nil || len(targets) == 0 {
		return false
	}
	usedAliases := map[string]struct{}{}
	for envIndex := range project.Spec.Environments {
		for bindingIndex := range project.Spec.Environments[envIndex].Bindings {
			binding := &project.Spec.Environments[envIndex].Bindings[bindingIndex]
			if alias := strings.ToLower(strings.TrimSpace(binding.Name)); alias != "" {
				usedAliases[alias] = struct{}{}
			}
		}
	}
	changed := false
	for _, target := range targets {
		key := automaticProviderReferenceKey(target.provider, target.ref)
		if key == "" {
			continue
		}
		if existing := findAutomaticIntegrationBinding(project, key); existing != nil {
			// Reuse the first binding for this exact target. Existing bindings
			// (including user-created aliases) retain their identity and audit.
			if mergeAutomaticIntegrationActions(existing, target.actions) {
				changed = true
			}
			continue
		}
		alias := automaticProviderIntegrationAlias(target.provider, target.ref, usedAliases)
		usedAliases[strings.ToLower(alias)] = struct{}{}
		binding := aiv1alpha1.ProjectProviderBindingSpec{
			Name: alias, Provider: target.provider, Kind: aiv1alpha1.ProjectBindingKindProviderReference,
			ResourceRef: target.ref.DeepCopy(), AllowedActions: cloneAutomaticIntegrationActions(target.actions),
		}
		env := ensureProjectIntegrationEnvironment(project, automaticProviderIntegrationEnvironment)
		env.Bindings = append(env.Bindings, binding)
		changed = true
	}
	return changed
}

func findAutomaticIntegrationBinding(project *aiv1alpha1.Project, targetKey string) *aiv1alpha1.ProjectProviderBindingSpec {
	if project == nil || targetKey == "" {
		return nil
	}
	for envIndex := range project.Spec.Environments {
		for bindingIndex := range project.Spec.Environments[envIndex].Bindings {
			binding := &project.Spec.Environments[envIndex].Bindings[bindingIndex]
			if binding.Kind == aiv1alpha1.ProjectBindingKindProviderReference && automaticProviderReferenceKey(binding.Provider, binding.ResourceRef) == targetKey {
				return binding
			}
		}
	}
	return nil
}

func mergeAutomaticIntegrationActions(binding *aiv1alpha1.ProjectProviderBindingSpec, desired []aiv1alpha1.ProjectProviderActionSpec) bool {
	if binding == nil {
		return false
	}
	byID := map[string]aiv1alpha1.ProjectProviderActionSpec{}
	for _, action := range binding.AllowedActions {
		byID[strings.ToLower(strings.TrimSpace(action.Name)+"\x00"+strings.TrimSpace(action.Version))] = action
	}
	changed := false
	for _, action := range desired {
		key := strings.ToLower(strings.TrimSpace(action.Name) + "\x00" + strings.TrimSpace(action.Version))
		prior, exists := byID[key]
		if !exists {
			byID[key] = automaticIntegrationActionWithAudit(action, automaticProviderActionTimestamp())
			changed = true
			continue
		}
		if prior.Revoked {
			// Revocations are durable operator intent. Automatic discovery must
			// never silently reactivate one.
			continue
		}
		if prior.GrantedBy == automaticProviderActionGrantedBy && prior.SchemaDigest != action.SchemaDigest {
			prior.SchemaDigest = action.SchemaDigest
			prior.GrantedAt = automaticProviderActionTimestamp()
			prior.RevokedBy = ""
			prior.RevokedAt = nil
			byID[key] = prior
			changed = true
			continue
		}
		// Keep an existing active grant's audit fields and digest. This is
		// what makes repeated turns idempotent and preserves user-created
		// grants that predate automatic materialization.
	}
	merged := make([]aiv1alpha1.ProjectProviderActionSpec, 0, len(byID))
	for _, action := range byID {
		merged = append(merged, action)
	}
	sort.Slice(merged, func(i, j int) bool {
		left, right := strings.ToLower(merged[i].Name), strings.ToLower(merged[j].Name)
		if left != right {
			return left < right
		}
		return strings.ToLower(merged[i].Version) < strings.ToLower(merged[j].Version)
	})
	if !reflect.DeepEqual(binding.AllowedActions, merged) {
		binding.AllowedActions = merged
		changed = true
	}
	return changed
}

func cloneAutomaticIntegrationActions(actions []aiv1alpha1.ProjectProviderActionSpec) []aiv1alpha1.ProjectProviderActionSpec {
	cloned := make([]aiv1alpha1.ProjectProviderActionSpec, 0, len(actions))
	now := automaticProviderActionTimestamp()
	for _, action := range actions {
		cloned = append(cloned, automaticIntegrationActionWithAudit(action, now))
	}
	return cloned
}

func automaticIntegrationActionWithAudit(action aiv1alpha1.ProjectProviderActionSpec, grantedAt *metav1.Time) aiv1alpha1.ProjectProviderActionSpec {
	action.GrantedBy = automaticProviderActionGrantedBy
	action.GrantedAt = grantedAt.DeepCopy()
	action.Revoked = false
	action.RevokedBy = ""
	action.RevokedAt = nil
	return action
}

func automaticProviderActionTimestamp() *metav1.Time {
	now := metav1.Now()
	return &now
}

func automaticProviderIntegrationAlias(provider string, ref *aiv1alpha1.ProjectProviderResourceReference, used map[string]struct{}) string {
	key := automaticProviderReferenceKey(provider, ref)
	hash := sha256.Sum256([]byte(key))
	suffix := hex.EncodeToString(hash[:])[:10]
	parts := []string{provider, ref.Resource, ref.Name}
	slug := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.ToLower(strings.TrimSpace(part))
		part = automaticIntegrationAliasPartRE.ReplaceAllString(part, "-")
		part = strings.Trim(part, "-_")
		if part != "" {
			slug = append(slug, part)
		}
	}
	base := strings.Join(slug, "-")
	if base == "" {
		base = "provider-resource"
	}
	maxBase := 63 - len(automaticProviderIntegrationPrefix) - 1 - len(suffix)
	if len(base) > maxBase {
		base = strings.Trim(base[:maxBase], "-_")
	}
	candidate := automaticProviderIntegrationPrefix + base + "-" + suffix
	for attempt := 1; ; attempt++ {
		if _, exists := used[strings.ToLower(candidate)]; !exists && projectIntegrationIdentifierRE.MatchString(candidate) {
			return candidate
		}
		attemptHash := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%d", key, attempt)))
		attemptSuffix := hex.EncodeToString(attemptHash[:])[:10]
		candidate = automaticProviderIntegrationPrefix + base + "-" + attemptSuffix
	}
}
