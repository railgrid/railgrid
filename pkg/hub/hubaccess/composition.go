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

package hubaccess

import (
	"strings"

	providersv1alpha1 "github.com/railgrid/railgrid/apis/providers/v1alpha1"
	tenancyv1alpha1 "github.com/railgrid/railgrid/apis/tenancy/v1alpha1"
)

// Compositions are the second thing a tenant consents to when enabling a
// provider, and they ride in the SAME Grant as hub access: one object per
// (provider, workspace), one place to look for "what did this workspace agree
// this provider may do".
//
// A composition capability is spelled
//
//	compose:<group>/<resource>      scope: workspace
//
// so it reads in a grant listing as what it is — "App Studio may compose
// infrastructure.railgrid.ai/instances here" — and so the capability
// vocabulary stays one flat namespace that `kubectl get grant -o yaml` can be
// read against. The scope is always workspace: a composition is a reconciler
// writing objects into ONE tenant workspace, which is the whole point of the
// scoped identity that carries it. There is no org-wide composition.
//
// Unlike a hub-access capability, a composition carries no limits in the grant
// entry. What bounds it is the declaration (the verbs in
// spec.requires[].resources[].verbs), which the identity policy reads fresh on
// every mint — so narrowing a chart narrows live identities on the next
// refresh, and widening one is pending consent until an admin accepts it
// again.

// ComposeCapabilityPrefix marks a composition capability inside a Grant.
const ComposeCapabilityPrefix = "compose:"

// ComposeCapability is the capability name recorded for one composed kind.
func ComposeCapability(group, resource string) string {
	return ComposeCapabilityPrefix + group + "/" + resource
}

// ParseComposeCapability splits a composition capability back into its
// (group, resource). It reports false for any other capability, so a caller
// iterating a grant can tell compositions from hub access without a second
// list.
//
// The resource half may itself be a "<resource>/<verb>" coordinate: a
// requirement on another provider names either a kind or one verb on a kind
// (ProviderRequiredResource), and both are recorded as compositions. The group
// is therefore the first segment and everything after it is the resource, so a
// coordinate round-trips instead of failing to parse and disappearing from the
// Enable dialog's composition list.
func ParseComposeCapability(capability string) (group, resource string, ok bool) {
	rest, found := strings.CutPrefix(capability, ComposeCapabilityPrefix)
	if !found {
		return "", "", false
	}
	group, resource, found = strings.Cut(rest, "/")
	if !found || group == "" || resource == "" {
		return "", "", false
	}
	// A resource is one segment, or a "<resource>/<verb>" coordinate. Anything
	// deeper is not a coordinate this contract can name.
	if strings.Count(resource, "/") > 1 {
		return "", "", false
	}
	return group, resource, true
}

// CompositionRequirement is one composed kind at the point of decision: which
// dependency declared it, and which kind it is.
type CompositionRequirement struct {
	// Dependency is the provider whose kind is composed (the OWNER of the
	// group), not the provider the grant is for.
	Dependency string
	Group      string
	Resource   string
}

// Ref is the capability reference this composition is recorded under.
func (c CompositionRequirement) Ref() tenancyv1alpha1.CapabilityRef {
	return tenancyv1alpha1.CapabilityRef{
		Capability: ComposeCapability(c.Group, c.Resource),
		Scope:      string(providersv1alpha1.HubAccessScopeWorkspace),
	}
}

// DeclaredCompositions flattens a provider's requirements into the list an
// Enable dialog offers and an Enable request is checked against.
//
// Only a requirement that NAMES a provider produces one. A requirement on a
// platform builtin — authorization.k8s.io, the core group — belongs to no
// provider, so there is no "this provider manages that provider's kinds here"
// consent to ask for; it is an ordinary permission claim the tenant ticks in
// the same dialog.
func DeclaredCompositions(requires []providersv1alpha1.ProviderRequirement) []CompositionRequirement {
	out := make([]CompositionRequirement, 0, len(requires))
	for _, requirement := range providersv1alpha1.RequiredCoordinates(requires) {
		if requirement.Provider == "" {
			continue
		}
		out = append(out, CompositionRequirement{
			Dependency: requirement.Provider,
			Group:      requirement.Group,
			Resource:   requirement.Resource,
		})
	}
	return out
}

// DecideComposition returns the grant's decision for one composition.
func DecideComposition(grant *tenancyv1alpha1.Grant, composition CompositionRequirement) Decision {
	ref := composition.Ref()
	if grant == nil {
		return Undecided
	}
	for _, granted := range grant.Spec.Capabilities {
		if granted.Capability == ref.Capability && granted.Scope == ref.Scope {
			return Accepted
		}
	}
	for _, declined := range grant.Spec.Declined {
		if declined.Capability == ref.Capability && declined.Scope == ref.Scope {
			return Declined
		}
	}
	return Undecided
}

// ComposeAllowed is the policy's answer for one declared composition:
// accepted is allowed, declined is not, and undecided is allowed only for a
// platform provider under the platform default — exactly the rule hub access
// uses, for the same reason. Platform providers are operator-installed and
// were composing these kinds before consent existed; an org-owned provider
// always needs an explicit acceptance. The second return says the decision
// came from the default rather than from a person, which the enabled listing
// surfaces so an admin can see what nobody has actually agreed to.
func ComposeAllowed(grant *tenancyv1alpha1.Grant, composition CompositionRequirement, platformProvider, platformDefault bool) (allowed, byDefault bool) {
	switch DecideComposition(grant, composition) {
	case Accepted:
		return true, false
	case Declined:
		return false, false
	default:
		if platformProvider && platformDefault {
			return true, true
		}
		return false, false
	}
}

// MayDecideComposition reports whether a person with these roles may accept a
// composition. A composition applies to exactly one workspace, so it is a
// workspace decision: a workspace admin or an org admin.
func MayDecideComposition(workspaceRole, orgRole string) bool {
	return MayDecide(providersv1alpha1.HubAccessScopeWorkspace, workspaceRole, orgRole)
}

// GrantedComposition is the grant entry recorded when a tenant accepts a
// composition. It carries no limits: the verbs come from the declaration,
// read fresh on every mint.
func GrantedComposition(composition CompositionRequirement) tenancyv1alpha1.GrantedCapability {
	ref := composition.Ref()
	return tenancyv1alpha1.GrantedCapability{Capability: ref.Capability, Scope: ref.Scope}
}
