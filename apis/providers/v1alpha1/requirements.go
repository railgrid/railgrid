/*
Copyright 2026 The Railgrid Authors.

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

package v1alpha1

import (
	"fmt"
	"regexp"
	"strings"
)

// apiGroupPattern is a DNS-subdomain API group.
var apiGroupPattern = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`)

// providerNamePattern is a CatalogEntry metadata.name.
var providerNamePattern = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

// requiredVerbs is the closed verb vocabulary. It matches the kubebuilder
// enum; the Go check exists because the hub also reads CatalogEntries written
// before a schema update reached the server.
var requiredVerbs = map[ProviderRequiredVerb]struct{}{
	RequiredVerbGet:    {},
	RequiredVerbList:   {},
	RequiredVerbWatch:  {},
	RequiredVerbCreate: {},
	RequiredVerbUpdate: {},
	RequiredVerbPatch:  {},
	RequiredVerbDelete: {},
}

// ValidateProviderRequirements validates everything a provider declares it
// needs from groups it does not own.
//
// It fails closed, for the same reason the export does: a half-admitted
// requirement list would have the hub generate a claim nobody declared, or mint
// a scoped-identity rule for a coordinate the owning provider never published.
func ValidateProviderRequirements(requirements []ProviderRequirement) error {
	seenGroup := make(map[string]struct{}, len(requirements))
	for i, requirement := range requirements {
		if err := ValidateProviderRequirement(requirement); err != nil {
			return fmt.Errorf("requires[%d] (%s): %w", i, requirementLabel(requirement), err)
		}
		if _, dup := seenGroup[requirement.Group]; dup {
			return fmt.Errorf("requires[%d] (%s): group is declared twice; one group belongs to one provider, so state everything it needs in one entry",
				i, requirementLabel(requirement))
		}
		seenGroup[requirement.Group] = struct{}{}
	}
	return nil
}

// ValidateProviderRequirement validates one requirement independently of its
// siblings.
func ValidateProviderRequirement(requirement ProviderRequirement) error {
	if requirement.Provider != "" {
		if !providerNamePattern.MatchString(requirement.Provider) || len(requirement.Provider) > 63 {
			return fmt.Errorf("provider must be a CatalogEntry name")
		}
		if requirement.Group == "" {
			return fmt.Errorf("a requirement on a provider must name the API group that provider serves, not the core group")
		}
	}
	if requirement.Group != "" {
		if !apiGroupPattern.MatchString(requirement.Group) || len(requirement.Group) > 253 {
			return fmt.Errorf("group must be a DNS-subdomain API group")
		}
	}
	if len(requirement.Resources) == 0 {
		return fmt.Errorf("at least one resource is required")
	}
	seen := make(map[string]struct{}, len(requirement.Resources))
	for i, resource := range requirement.Resources {
		if err := ValidateProviderRequiredResource(requirement.Group, resource); err != nil {
			return fmt.Errorf("resources[%d] (%s): %w", i, resource.Name, err)
		}
		if _, dup := seen[resource.Name]; dup {
			return fmt.Errorf("resources[%d] (%s): duplicate coordinate", i, resource.Name)
		}
		seen[resource.Name] = struct{}{}
	}
	return nil
}

// ValidateProviderRequiredResource validates one claimed coordinate. group is
// the requirement's group, needed because the core group's secrets carry an
// extra rule.
func ValidateProviderRequiredResource(group string, resource ProviderRequiredResource) error {
	name, verb, isCoordinate := strings.Cut(resource.Name, "/")
	if !resourceNamePattern.MatchString(name) || len(name) > 63 {
		return fmt.Errorf("name must be a lowercase DNS-like plural name, optionally followed by /<verb>")
	}
	if isCoordinate {
		if err := validateCoordinateName(verb); err != nil {
			return fmt.Errorf("verb %q: %w", verb, err)
		}
		if len(resource.Verbs) != 0 {
			return fmt.Errorf("verbs must be empty on a %s coordinate: the verb IS the capability, and which HTTP method it uses is the serving provider's business", resource.Name)
		}
		if resource.Selector != nil {
			return fmt.Errorf("selector does not apply to a verb coordinate; it narrows which objects a claim covers, and a verb is invoked on an object the parent claim already covers")
		}
		return nil
	}
	if len(resource.Verbs) == 0 {
		return fmt.Errorf("at least one verb is required on a plain resource")
	}
	seen := make(map[ProviderRequiredVerb]struct{}, len(resource.Verbs))
	for _, verb := range resource.Verbs {
		if _, ok := requiredVerbs[verb]; !ok {
			return fmt.Errorf("verb %q is not an ordinary Kubernetes verb", verb)
		}
		if _, dup := seen[verb]; dup {
			return fmt.Errorf("verb %q is listed twice", verb)
		}
		seen[verb] = struct{}{}
	}
	if resource.Selector != nil && len(resource.Selector.MatchLabels) == 0 {
		return fmt.Errorf("selector.matchLabels must not be empty")
	}
	if group == "" && name == "secrets" && resource.Selector == nil {
		return fmt.Errorf("a claim on core secrets must carry a selector: without one the provider reaches every Secret the tenant holds")
	}
	return nil
}

// requirementLabel names a requirement in an error the way its author wrote it.
func requirementLabel(requirement ProviderRequirement) string {
	group := requirement.Group
	if group == "" {
		group = "core"
	}
	if requirement.Provider == "" {
		return group
	}
	return requirement.Provider + " " + group
}

// RequiredVerbStrings renders verbs as the plain strings an RBAC rule and a kcp
// permission claim carry.
func RequiredVerbStrings(verbs []ProviderRequiredVerb) []string {
	if len(verbs) == 0 {
		return nil
	}
	out := make([]string, 0, len(verbs))
	for _, verb := range verbs {
		out = append(out, string(verb))
	}
	return out
}

// Dependencies returns the provider names these requirements depend on, in
// declaration order and deduplicated: the providers that must already be
// enabled in a workspace before this one can be.
func Dependencies(requirements []ProviderRequirement) []string {
	seen := make(map[string]struct{}, len(requirements))
	out := make([]string, 0, len(requirements))
	for _, requirement := range requirements {
		if requirement.Provider == "" {
			continue
		}
		if _, dup := seen[requirement.Provider]; dup {
			continue
		}
		seen[requirement.Provider] = struct{}{}
		out = append(out, requirement.Provider)
	}
	return out
}

// RequiredCoordinate is one flattened claim: a (group, resource) with the verbs
// it carries. A verb coordinate ("instances/exec") reports Coordinate=true and
// carries no verbs, because the generated claim spells every verb.
type RequiredCoordinate struct {
	Provider   string
	Group      string
	Resource   string
	Verbs      []ProviderRequiredVerb
	Selector   *ProviderLabelSelector
	Coordinate bool
}

// RequiredCoordinates flattens the requirements into one claim per resource, in
// declaration order. Everything that materialises claims — the APIExport
// generator, the hub's Enable flow, the permission-claim policy generator —
// walks these.
func RequiredCoordinates(requirements []ProviderRequirement) []RequiredCoordinate {
	out := make([]RequiredCoordinate, 0, len(requirements)*2)
	for _, requirement := range requirements {
		for _, resource := range requirement.Resources {
			out = append(out, RequiredCoordinate{
				Provider:   requirement.Provider,
				Group:      requirement.Group,
				Resource:   resource.Name,
				Verbs:      resource.Verbs,
				Selector:   resource.Selector,
				Coordinate: strings.Contains(resource.Name, "/"),
			})
		}
	}
	return out
}
