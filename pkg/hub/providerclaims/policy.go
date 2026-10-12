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

// Package providerclaims is the one answer to "which permission claims does a
// tenant's APIBinding to a provider's export carry".
//
// A provider declares what it needs once, in CatalogEntry.spec.requires;
// codegen stamps the same list onto its APIExport. kcp, however, acts only on
// the claims the tenant's own binding lists, and the binding is written by the
// hub's Enable flow — once. This package holds the policy that keeps the two in
// step, and the reconciler that applies it whenever either side changes, so a
// provider that grows a requirement does not need every tenant to re-enable it.
//
// The policy, in one place:
//
//   - A requirement that names no provider (a platform builtin, a core kind) is
//     the provider's own machinery; the tenant consented to the provider, so
//     the hub accepts it on their behalf.
//   - A requirement that names another provider is a composition: a claim on
//     somebody else's kinds. It is Accepted only where the workspace's Grant
//     records that consent, and Rejected otherwise — never silently granted.
//   - A claim the binding already holds Rejected stays Rejected (the writer in
//     pkg/hub/kcp enforces this), and an existing claim keeps its selector.
package providerclaims

import (
	providersv1alpha1 "github.com/railgrid/railgrid/apis/providers/v1alpha1"
	tenancyv1alpha1 "github.com/railgrid/railgrid/apis/tenancy/v1alpha1"
	"github.com/railgrid/railgrid/pkg/hub/hubaccess"
	"github.com/railgrid/railgrid/pkg/hub/kcp"
	"github.com/railgrid/railgrid/pkg/hub/providers"
)

// RequiredClaims renders spec.requires as the binding's permission claims, one
// per declared coordinate, Accepted as isAccepted says. These are the same
// claims provider-sdk/cmd/apiexportgen puts on the generated APIExport, so the
// binding lists exactly what the export declares.
//
// A verb coordinate ("instances/exec") is claimed whole: the verb IS the
// capability, and which HTTP method it uses — which is what kcp maps onto an
// RBAC verb — is the serving provider's transport detail, so the claim spells
// every verb.
func RequiredClaims(requires []providersv1alpha1.ProviderRequirement, isAccepted func(providersv1alpha1.RequiredCoordinate) bool) []kcp.ProviderClaim {
	coordinates := providersv1alpha1.RequiredCoordinates(requires)
	claims := make([]kcp.ProviderClaim, 0, len(coordinates))
	for _, coordinate := range coordinates {
		claim := kcp.ProviderClaim{
			Group:    coordinate.Group,
			Resource: coordinate.Resource,
			Verbs:    providersv1alpha1.RequiredVerbStrings(coordinate.Verbs),
			Accepted: isAccepted(coordinate),
		}
		if coordinate.Coordinate {
			claim.Verbs = []string{"*"}
		}
		if coordinate.Selector != nil {
			// The scope travels with the claim: what the tenant accepts is the
			// narrowed claim the provider declared, not a blanket one the hub
			// would then have to walk back.
			claim.MatchLabels = coordinate.Selector.MatchLabels
		}
		claims = append(claims, claim)
	}
	return claims
}

// Desired is the claim set the hub keeps on a tenant's binding to prov's
// export: every requirement the provider declares, with the provider's own
// requirements Accepted and compositions Accepted only where grant records the
// workspace's consent. grant may be nil (no consent recorded).
//
// Own requirements are accepted for platform and org-owned providers alike: an
// org-owned provider is registered by the Org's own admins, who already hold
// admin in every workspace of that Org, so a claim they declare widens nothing
// they could not grant directly. AGENTS.md §5.1 records that decision.
//
// A composition the grant consents to is marked Consented, so the merge in
// pkg/hub/kcp knows the Rejected it may be replacing was the hub's own
// placeholder, not a tenant's decision.
func Desired(prov providers.Provider, grant *tenancyv1alpha1.Grant) []kcp.ProviderClaim {
	consented := ConsentedCompositions(grant)
	claims := RequiredClaims(prov.Requires, func(coordinate providersv1alpha1.RequiredCoordinate) bool {
		if coordinate.Provider == "" {
			return true
		}
		return consented[Key(coordinate.Group, coordinate.Resource)]
	})
	coordinates := providersv1alpha1.RequiredCoordinates(prov.Requires)
	for i := range claims {
		if coordinates[i].Provider != "" && claims[i].Accepted {
			claims[i].Consented = true
		}
	}
	return claims
}

// ConsentedCompositions reads the compositions a Grant records as accepted,
// keyed by Key(group, resource).
func ConsentedCompositions(grant *tenancyv1alpha1.Grant) map[string]bool {
	out := map[string]bool{}
	if grant == nil {
		return out
	}
	for _, g := range grant.Spec.Capabilities {
		if group, resource, ok := hubaccess.ParseComposeCapability(g.Capability); ok {
			out[Key(group, resource)] = true
		}
	}
	return out
}

// Key identifies a claim by what identifies it on a binding: group and
// resource. Verbs and selectors ride along with the declaration.
func Key(group, resource string) string { return group + "/" + resource }
