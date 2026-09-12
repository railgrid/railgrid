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

// Package hubaccess implements the hub-access half of the provider contract:
// the hub REST capabilities a provider may exercise with the delegated user
// token the hub hands it in place of the caller's bearer.
//
// A provider requests capabilities in its CatalogEntry (spec.hubAccess) from
// a closed set this package owns; it never names routes. A tenant accepts
// them when enabling the provider, which records a Grant for the provider. The
// Gate admits a delegated call only for a route that maps to a capability the
// provider both declares and was granted in that workspace, and marks the
// request with the accepted limits (tenant.DelegatedCall). The person the
// token stands for is still authorized by the tenant middleware and handlers
// exactly as if they had called: a provider never exceeds that person.
package hubaccess

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"path"
	"strings"

	providersv1alpha1 "github.com/faroshq/faros/apis/providers/v1alpha1"
	tenancyv1alpha1 "github.com/faroshq/faros/apis/tenancy/v1alpha1"
)

// Requirement is the capability a route needs, at a scope.
type Requirement struct {
	Capability providersv1alpha1.ProviderHubCapability
	Scope      providersv1alpha1.ProviderHubAccessScope
}

// Match maps a hub REST request to the capability it requires. Only the
// routes listed here are reachable with a delegated token; everything else
// (role changes, removals, workspace membership writes, org and workspace
// management) is not part of the closed set and is refused.
//
//	GET  /api/orgs/{org}/memberships                   memberships.read   org
//	GET  /api/orgs/{org}/workspaces/{ws}/memberships   memberships.read   workspace
//	POST /api/orgs/{org}/memberships                   memberships.invite org
func Match(method, urlPath string) (Requirement, bool) {
	parts := strings.Split(strings.Trim(path.Clean("/"+urlPath), "/"), "/")
	switch {
	case len(parts) == 4 && parts[0] == "api" && parts[1] == "orgs" && parts[2] != "" && parts[3] == "memberships":
		switch method {
		case http.MethodGet:
			return Requirement{providersv1alpha1.HubCapabilityMembershipsRead, providersv1alpha1.HubAccessScopeOrg}, true
		case http.MethodPost:
			return Requirement{providersv1alpha1.HubCapabilityMembershipsInvite, providersv1alpha1.HubAccessScopeOrg}, true
		}
	case len(parts) == 6 && parts[0] == "api" && parts[1] == "orgs" && parts[2] != "" &&
		parts[3] == "workspaces" && parts[4] != "" && parts[5] == "memberships" && method == http.MethodGet:
		return Requirement{providersv1alpha1.HubCapabilityMembershipsRead, providersv1alpha1.HubAccessScopeWorkspace}, true
	}
	return Requirement{}, false
}

// Declared returns the provider's declaration for req, if any.
func Declared(declared []providersv1alpha1.ProviderHubAccess, req Requirement) (providersv1alpha1.ProviderHubAccess, bool) {
	for _, d := range declared {
		if d.Capability == req.Capability && d.Scope == req.Scope {
			return d, true
		}
	}
	return providersv1alpha1.ProviderHubAccess{}, false
}

// Decision is what a grant says about one capability.
type Decision int

const (
	// Undecided: nobody entitled to decide it has.
	Undecided Decision = iota
	Accepted
	Declined
)

// Decide returns the grant's decision for req, and the accepted entry when
// the decision is Accepted.
func Decide(grant *tenancyv1alpha1.Grant, req Requirement) (Decision, tenancyv1alpha1.GrantedCapability) {
	if g, ok := Granted(grant, req); ok {
		return Accepted, g
	}
	if grant != nil {
		for _, d := range grant.Spec.Declined {
			if d.Capability == string(req.Capability) && d.Scope == string(req.Scope) {
				return Declined, tenancyv1alpha1.GrantedCapability{}
			}
		}
	}
	return Undecided, tenancyv1alpha1.GrantedCapability{}
}

// Allowed is the gate's answer for one declared capability: accepted
// capabilities are allowed, declined ones are not, and an undecided one is
// allowed only for a platform provider under the platform default. The
// returned entry carries the limits to combine with the declaration.
func Allowed(grant *tenancyv1alpha1.Grant, declared providersv1alpha1.ProviderHubAccess, platformProvider, platformDefault bool) (tenancyv1alpha1.GrantedCapability, bool, bool) {
	req := Requirement{Capability: declared.Capability, Scope: declared.Scope}
	switch decision, entry := Decide(grant, req); decision {
	case Accepted:
		return entry, true, false
	case Declined:
		return tenancyv1alpha1.GrantedCapability{}, false, false
	default:
		if platformProvider && platformDefault {
			return FromDeclaration(declared), true, true
		}
		return tenancyv1alpha1.GrantedCapability{}, false, false
	}
}

// MayDecide reports whether a person with these roles may accept or decline
// a capability at scope: org scope needs an org admin, workspace scope a
// workspace or org admin.
func MayDecide(scope providersv1alpha1.ProviderHubAccessScope, workspaceRole, orgRole string) bool {
	if scope == providersv1alpha1.HubAccessScopeOrg {
		return orgRole == tenancyv1alpha1.MembershipRoleAdmin
	}
	return workspaceRole == tenancyv1alpha1.MembershipRoleAdmin || orgRole == tenancyv1alpha1.MembershipRoleAdmin
}

// Granted returns the grant entry for req, if any.
func Granted(grant *tenancyv1alpha1.Grant, req Requirement) (tenancyv1alpha1.GrantedCapability, bool) {
	if grant == nil {
		return tenancyv1alpha1.GrantedCapability{}, false
	}
	for _, g := range grant.Spec.Capabilities {
		if g.Capability == string(req.Capability) && g.Scope == string(req.Scope) {
			return g, true
		}
	}
	return tenancyv1alpha1.GrantedCapability{}, false
}

// Limits are the constraints in force for one admitted capability.
type Limits struct {
	MaxRole     string
	AllowInvite bool
}

// Effective combines a declaration with its grant: the narrower of the two
// wins, so neither a catalog update nor an old grant can widen access. A role
// cap is always "member" — admin is never grantable through a provider.
func Effective(declared providersv1alpha1.ProviderHubAccess, granted tenancyv1alpha1.GrantedCapability) Limits {
	return Limits{
		MaxRole:     tenancyv1alpha1.MembershipRoleMember,
		AllowInvite: declared.AllowInvite && granted.AllowInvite,
	}
}

// FromDeclaration is the grant entry recorded when a tenant accepts a
// declared capability.
func FromDeclaration(d providersv1alpha1.ProviderHubAccess) tenancyv1alpha1.GrantedCapability {
	g := tenancyv1alpha1.GrantedCapability{
		Capability: string(d.Capability),
		Scope:      string(d.Scope),
	}
	if d.Capability == providersv1alpha1.HubCapabilityMembershipsInvite {
		g.MaxRole = tenancyv1alpha1.MembershipRoleMember
		g.AllowInvite = d.AllowInvite
	}
	return g
}

// GrantName is the deterministic Grant name for one provider in one
// workspace. The subject kind and the provider owner are part of it, so an
// org-owned provider and the platform provider it shadows never share a
// grant, and neither collides with a grant for another kind of subject.
func GrantName(orgUUID, wsUUID, provider, providerOrgUUID string) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{string(tenancyv1alpha1.GrantSubjectProvider), orgUUID, wsUUID, provider, providerOrgUUID}, "\x00")))
	return "grant-" + hex.EncodeToString(sum[:20])
}
