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

package identity

import (
	"fmt"
	"sort"
	"strings"

	rbacv1 "k8s.io/api/rbac/v1"

	"github.com/railgrid/railgrid/pkg/hub/providers"
)

// The policy is the whole point of this service. A provider asking the hub to
// mint an identity may ask for exactly three shapes of rule, and nothing else:
//
//	A. OWN GROUP     — any verb on resources of a group the requesting provider
//	                   exports. This is the provider's own API surface; it
//	                   already reconciles those objects with its own SA, so a
//	                   scoped identity over them adds no new reach. Name
//	                   scoping is optional here (and is what edges wants: the
//	                   agent gets `proxy` on its OWN edge, by name).
//	B. FOREIGN READ  — get on NAMED resources of another provider's group,
//	                   where that provider is bound in the tenant workspace.
//	C. FOREIGN VERB  — create on {resource}/{verb} of another provider's group,
//	                   where {verb} is DECLARED by that provider for that
//	                   resource — either as a catalog action (spec.actions) or
//	                   as a data-plane verb (spec.dataPlane.verbs) —
//	                   name-scoped.
//	D. PLATFORM      — a fixed, closed allowlist of platform groups every
//	                   workspace-scoped identity needs to function at all
//	                   (self-reviews, its own LogicalCluster, its Leases),
//	                   name-scoped wherever the API allows it.
//	E. COMPOSITION   — CRUD on a kind of another provider that the requester
//	                   DECLARES it composes (spec.dependencies[].composes) and
//	                   that a workspace or org ADMIN ACCEPTED in this
//	                   workspace. This is the one shape that writes another
//	                   provider's objects, and it exists because building a
//	                   product out of two providers' kinds — App Studio
//	                   creating the infrastructure Instance and the code
//	                   Repository a project is made of — is a tenant's
//	                   decision to delegate, not a provider's to take.
//
// Everything else is refused with a reason the caller sees. In particular
// there is no shape that grants anything on the core group (Secrets above all
// — review X-4), and outside clause E no unnamed rule and no write on another
// provider's objects.
//
// Clause E is deliberately NOT an APIExport permission claim, which is the
// other way a provider could reach a first-party group. A claim on a
// `*.railgrid.ai` group pins to one export's identityHash (AGENTS.md §5.7), so
// the moment an Org self-hosts the dependency, every claim-holder is pinned to
// the platform copy and silently serves nothing. A composition names the
// dependency by NAME and is resolved per workspace through whatever export is
// bound there, which is what makes it BYO-safe. It is also revocable: a claim
// lives in the consumer's APIExport, a composition in the tenant's own Grant.
//
// A note on list and watch, which the §10 design sketch included in shape B:
// Kubernetes RBAC does not apply resourceNames to collection requests, so a
// rule with resourceNames and verb `list` authorizes nothing at all, and one
// without resourceNames authorizes reading EVERY object of that kind in the
// workspace. "get/list/watch on named resources" is therefore not expressible;
// this policy refuses list and watch on a foreign group rather than silently
// emitting one of those two rules. A consumer that needs a collection reads it
// by name, or the owning provider publishes a list API of its own.

// RuleClass names which policy clause admitted a rule. It is recorded on the
// refusal reason and in tests.
type RuleClass string

const (
	// ClassOwnGroup is clause A.
	ClassOwnGroup RuleClass = "own-group"
	// ClassForeignRead is clause B.
	ClassForeignRead RuleClass = "foreign-read"
	// ClassForeignVerb is clause C.
	ClassForeignVerb RuleClass = "foreign-verb"
	// ClassPlatform is clause D.
	ClassPlatform RuleClass = "platform"
	// ClassComposition is clause E.
	ClassComposition RuleClass = "composition"
)

// Refusal explains why one requested rule was not allowed. It is returned to
// the requesting provider: a provider debugging its own identity request needs
// to know which rule failed and why, and nothing here reveals tenant state.
type Refusal struct {
	Rule   rbacv1.PolicyRule `json:"rule"`
	Reason string            `json:"reason"`
	Code   string            `json:"code"`
}

// Error implements error so a single refusal can be returned as one.
func (r Refusal) Error() string { return r.Code + ": " + r.Reason }

// Refusal codes. They are part of the API: the SDK client surfaces them.
const (
	CodeEmptyRule        = "empty_rule"
	CodeMultiGroup       = "multi_group_rule"
	CodeCoreGroup        = "core_group_forbidden"
	CodeWildcard         = "wildcard_forbidden"
	CodeUnnamedForeign   = "unnamed_foreign_rule"
	CodeForeignWrite     = "foreign_write_forbidden"
	CodeForeignList      = "foreign_list_not_name_scoped"
	CodeUnknownProvider  = "unknown_group"
	CodeUnboundProvider  = "provider_not_bound"
	CodeUndeclaredVerb   = "undeclared_verb"
	CodeNonResourceRule  = "non_resource_rule"
	CodeRuleLimit        = "too_many_rules"
	CodeUnknownRequester = "unknown_requester"
	// CodeUnknownOwner means the owner object the caller named does not exist
	// in the tenant workspace (or exists with a different UID).
	CodeUnknownOwner = "unknown_owner"
	// CodeInvalidRequest means the request was malformed before any policy ran.
	CodeInvalidRequest = "invalid_request"
	// CodePlatformNotAllowed means the rule named a platform API group but
	// asked for something outside that group's closed allowlist.
	CodePlatformNotAllowed = "platform_rule_not_allowed"
	// CodeCompositionNotDeclared means the requester's CatalogEntry declares
	// no composition for this (group, resource) on the dependency that owns
	// the group.
	CodeCompositionNotDeclared = "composition_not_declared"
	// CodeCompositionNotGranted means the composition is declared but nobody
	// in this workspace accepted it.
	CodeCompositionNotGranted = "composition_not_granted"
	// CodeCompositionVerbNotDeclared means the rule asked for a verb the
	// composition does not declare.
	CodeCompositionVerbNotDeclared = "composition_verb_not_declared"
	// CodeCompositionShape means the rule is name-scoped where RBAC cannot
	// name-scope it, or unnamed where this policy insists on names.
	CodeCompositionShape = "composition_rule_shape"
)

// maxRules bounds one identity's ClusterRole. A policy-checked rule is cheap,
// but an unbounded list is a way to make the hub write an unboundedly large
// object into a tenant workspace on one request.
const maxRules = 64

// ProviderCatalog is the registry seam the policy reads: which groups a
// provider exports, and which verbs it declares on which resources.
// pkg/hub/providers.Registry satisfies it through RegistryCatalog.
type ProviderCatalog interface {
	// GroupOwner returns the provider that exports apiGroup.
	GroupOwner(apiGroup string) (string, bool)
	// ExportedGroups returns the API groups provider exports.
	ExportedGroups(provider string) []string
	// DeclaredVerbs returns every verb provider declares on resource — catalog
	// action names and data-plane verbs together. They are the only
	// {resource}/{verb} subresources anybody may be granted create on.
	DeclaredVerbs(provider, resource string) []string
	// Compositions returns every composition provider declares across its
	// dependencies (spec.dependencies[].composes). It is the declaration half
	// of clause E; the consent half is CompositionGrants.
	Compositions(provider string) []Composition
}

// BindingChecker answers whether a provider's API is bound in a tenant
// workspace. A foreign-group rule is refused when the owning provider is not
// enabled there: minting a capability for an API the tenant never accepted
// would let one provider pull another into a workspace by the back door.
type BindingChecker interface {
	IsBound(clusterID, provider string) (bool, error)
}

// Policy decides which requested rules a provider may have.
type Policy struct {
	catalog      ProviderCatalog
	bindings     BindingChecker
	compositions CompositionGrants
}

// NewPolicy builds a policy. bindings may be nil, in which case foreign-group
// rules are refused outright: a hub that cannot tell whether a provider is
// bound must not assume it is. compositions may be nil on the same terms — a
// hub that cannot read a workspace's consent refuses every composition rather
// than presuming consent it cannot see.
func NewPolicy(catalog ProviderCatalog, bindings BindingChecker, compositions CompositionGrants) *Policy {
	return &Policy{catalog: catalog, bindings: bindings, compositions: compositions}
}

// Authorize returns the normalized rules requester may be granted in
// clusterID, or the first refusal. Rules are returned sorted and deduplicated
// so an identical request always produces an identical ClusterRole, which is
// what makes the reconciler's rule comparison stable.
func (p *Policy) Authorize(requester, clusterID string, requested []rbacv1.PolicyRule) ([]rbacv1.PolicyRule, error) {
	if p == nil || p.catalog == nil {
		return nil, Refusal{Code: CodeUnknownRequester, Reason: "the hub has no provider catalog to check rules against"}
	}
	if strings.TrimSpace(requester) == "" {
		return nil, Refusal{Code: CodeUnknownRequester, Reason: "the requesting provider is unknown"}
	}
	if len(requested) > maxRules {
		return nil, Refusal{Code: CodeRuleLimit, Reason: fmt.Sprintf("at most %d rules may be requested, got %d", maxRules, len(requested))}
	}
	own := map[string]bool{}
	for _, group := range p.catalog.ExportedGroups(requester) {
		own[group] = true
	}

	out := make([]rbacv1.PolicyRule, 0, len(requested))
	for _, rule := range requested {
		normalized, err := p.authorizeRule(requester, clusterID, own, rule)
		if err != nil {
			return nil, err
		}
		out = append(out, normalized)
	}
	return normalizeRules(out), nil
}

func (p *Policy) authorizeRule(requester, clusterID string, own map[string]bool, rule rbacv1.PolicyRule) (rbacv1.PolicyRule, error) {
	refuse := func(code, reason string) (rbacv1.PolicyRule, error) {
		return rbacv1.PolicyRule{}, Refusal{Rule: rule, Code: code, Reason: reason}
	}
	if len(rule.NonResourceURLs) != 0 {
		return refuse(CodeNonResourceRule, "non-resource URL rules are never minted")
	}
	if len(rule.Resources) == 0 || len(rule.Verbs) == 0 {
		return refuse(CodeEmptyRule, "a rule must name at least one resource and one verb")
	}
	// One group per rule. A multi-group rule cannot be classified — half of it
	// could be the requester's own and half somebody else's — so the caller
	// splits it instead of the hub guessing.
	if len(rule.APIGroups) != 1 {
		return refuse(CodeMultiGroup, "each rule must name exactly one API group")
	}
	group := strings.TrimSpace(rule.APIGroups[0])
	if group == "" {
		return refuse(CodeCoreGroup, "the core API group is never minted: Secrets, ConfigMaps and Namespaces are not cross-provider currency (review X-4)")
	}
	if group == "*" || containsAny(rule.Resources, "*") || containsAny(rule.Verbs, "*") || containsAny(rule.ResourceNames, "*") {
		return refuse(CodeWildcard, "wildcards are never minted")
	}
	for _, value := range append(append(append([]string{group}, rule.Resources...), rule.Verbs...), rule.ResourceNames...) {
		if strings.ContainsAny(value, "\r\n\x00 ") {
			return refuse(CodeWildcard, "a rule field contains a prohibited character")
		}
	}

	// Clause D: the closed platform allowlist. It is checked BEFORE the own-
	// group clause so a provider whose exported group somehow collided with a
	// platform group could not widen these rules through clause A.
	if allowed, ok := platformGroups[group]; ok {
		return allowed(rule, refuse)
	}

	// Clause A: the requester's own exported group.
	if own[group] {
		return rbacv1.PolicyRule{
			APIGroups:     []string{group},
			Resources:     dedupeSorted(rule.Resources),
			Verbs:         dedupeSorted(rule.Verbs),
			ResourceNames: dedupeSorted(rule.ResourceNames),
		}, nil
	}

	// From here on the group belongs to somebody else.
	owner, ok := p.catalog.GroupOwner(group)
	if !ok {
		return refuse(CodeUnknownProvider, fmt.Sprintf("no provider in the catalog exports API group %q", group))
	}

	// Clause E: tenant-consented composition. It is evaluated BEFORE B and C
	// so a rule the requester declared as a composition is always measured
	// against the composition's verbs and the tenant's acceptance — never
	// admitted by the weaker foreign-read clause because it happened to ask
	// for `get`.
	//
	// The clause claims a rule only when EVERY resource in it is composed.
	// A rule mixing a composed kind with an ordinary foreign read falls
	// through to B, which is the right answer: declaring a composition must
	// never cost a provider access it already had.
	if p.composes(requester, group, rule.Resources) {
		return p.authorizeComposition(requester, clusterID, owner, rule, refuse)
	}

	// Outside clause E every foreign rule is name-scoped and
	// read-only-or-declared.
	if len(rule.ResourceNames) == 0 {
		return refuse(CodeUnnamedForeign, fmt.Sprintf("a rule on %s's group must name the exact resources it covers", owner))
	}
	if p.bindings == nil {
		return refuse(CodeUnboundProvider, "the hub cannot verify that "+owner+" is enabled in this workspace")
	}
	bound, err := p.bindings.IsBound(clusterID, owner)
	if err != nil {
		return rbacv1.PolicyRule{}, fmt.Errorf("checking whether %s is bound: %w", owner, err)
	}
	if !bound {
		return refuse(CodeUnboundProvider, fmt.Sprintf("%s is not enabled in this workspace", owner))
	}

	// Split the rule's resources into plain resources (clause B) and
	// {resource}/{verb} subresources (clause C); a rule may not mix them,
	// because their verb vocabularies are disjoint.
	var plain, sub []string
	for _, resource := range rule.Resources {
		if strings.Contains(resource, "/") {
			sub = append(sub, resource)
		} else {
			plain = append(plain, resource)
		}
	}
	if len(plain) != 0 && len(sub) != 0 {
		return refuse(CodeMultiGroup, "split a foreign rule so resources and {resource}/{verb} subresources are in separate rules")
	}

	if len(sub) != 0 {
		// Clause C: create on a declared verb subresource.
		for _, verb := range rule.Verbs {
			if verb != "create" {
				return refuse(CodeForeignWrite, fmt.Sprintf("only create is minted on a %s subresource, not %q", owner, verb))
			}
		}
		for _, resource := range sub {
			parts := strings.Split(resource, "/")
			if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
				return refuse(CodeUndeclaredVerb, fmt.Sprintf("%q is not a {resource}/{verb} subresource", resource))
			}
			// The owning provider must DECLARE the coordinate, as an action
			// or as a data-plane verb. Before spec.dataPlane.verbs existed,
			// exec/proxy/delegate lived only in provider code and no
			// cross-provider capability for them could be minted at all.
			declared := p.catalog.DeclaredVerbs(owner, parts[0])
			if !containsAny(declared, parts[1]) {
				return refuse(CodeUndeclaredVerb, fmt.Sprintf("%s declares no action or data-plane verb %q on %s", owner, parts[1], parts[0]))
			}
		}
		return rbacv1.PolicyRule{
			APIGroups:     []string{group},
			Resources:     dedupeSorted(sub),
			Verbs:         []string{"create"},
			ResourceNames: dedupeSorted(rule.ResourceNames),
		}, nil
	}

	// Clause B: get on named resources.
	for _, verb := range rule.Verbs {
		switch verb {
		case "get":
		case "list", "watch":
			return refuse(CodeForeignList, "list and watch cannot be scoped to resource names by RBAC, so they are not minted on another provider's group; read the objects by name")
		default:
			return refuse(CodeForeignWrite, fmt.Sprintf("only get is minted on %s's resources, not %q", owner, verb))
		}
	}
	return rbacv1.PolicyRule{
		APIGroups:     []string{group},
		Resources:     dedupeSorted(plain),
		Verbs:         []string{"get"},
		ResourceNames: dedupeSorted(rule.ResourceNames),
	}, nil
}

// Clause E — tenant-consented composition.
//
// compositionUnnamedVerbs and compositionNamedVerbs split the closed verb
// vocabulary by what RBAC can actually scope.
//
//   - `create` takes no resourceNames: a create request has no name to match
//     yet, so a name-scoped create rule authorizes NOTHING. The grant is
//     therefore "may create this kind in this workspace", and the workspace is
//     the bound scope.
//   - `list` and `watch` take no resourceNames either, for the reason clause B
//     refuses them outright: RBAC does not apply resourceNames to collection
//     requests. Here they are admitted anyway, and the difference is the whole
//     argument for this clause. The identity is a ServiceAccount inside ONE
//     tenant workspace, holding a ClusterRole the hub wrote and reconciles; a
//     list it authorizes returns the objects of that one workspace — the
//     workspace whose admin accepted the composition, of a kind they accepted.
//     Bounding a reconciler to a workspace is precisely what the scoped
//     identity is for, so "every Instance in this workspace" is the intended
//     scope and not an escape from one. A reconciler cannot work without a
//     watch; refusing it would push providers back to polling by name or to a
//     permission claim, which is worse on every axis.
//   - `get`, `update`, `patch` and `delete` act on an object that already
//     exists and has a name, so they must name it. A composing reconciler
//     knows the names — it created them.
var compositionUnnamedVerbs = map[string]bool{"create": true, "list": true, "watch": true}

var compositionNamedVerbs = map[string]bool{"get": true, "update": true, "patch": true, "delete": true}

// composes reports whether requester declares a composition for EVERY
// resource in resources on group. All-or-nothing is what keeps clause E from
// taking a rule it would then refuse: a mixed rule belongs to B.
func (p *Policy) composes(requester, group string, resources []string) bool {
	declared := p.catalog.Compositions(requester)
	if len(declared) == 0 {
		return false
	}
	for _, resource := range resources {
		found := false
		for _, composition := range declared {
			if composition.Group == group && composition.Resource == resource {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return len(resources) > 0
}

// authorizeComposition decides one rule under clause E. Every condition is
// re-checked here, on this request, against the CURRENT catalog and the
// CURRENT grant: the declaration bounds the verbs, the dependency must own
// the group, its export must be bound in this workspace, and the tenant must
// have accepted the composition. A narrowed chart narrows the identity on its
// next refresh; a widened one grants nothing until somebody accepts it again.
func (p *Policy) authorizeComposition(requester, clusterID, owner string, rule rbacv1.PolicyRule, refuse refuseFunc) (rbacv1.PolicyRule, error) {
	group := rule.APIGroups[0]

	// One verb class per rule: the two classes disagree about resourceNames,
	// so a rule mixing them could not be expressed in RBAC without either
	// widening the named verbs or neutering the unnamed ones.
	named, unnamed := 0, 0
	for _, verb := range rule.Verbs {
		switch {
		case compositionNamedVerbs[verb]:
			named++
		case compositionUnnamedVerbs[verb]:
			unnamed++
		default:
			return refuse(CodeCompositionVerbNotDeclared, fmt.Sprintf("verb %q is not a composition verb (get, list, watch, create, update, patch, delete are)", verb))
		}
	}
	if named != 0 && unnamed != 0 {
		return refuse(CodeCompositionShape, "split the rule: create, list and watch are not name-scoped, and get, update, patch and delete must be")
	}
	if named != 0 && len(rule.ResourceNames) == 0 {
		return refuse(CodeCompositionShape, fmt.Sprintf("a composition rule on %s's objects with get, update, patch or delete must name the exact objects it covers", owner))
	}
	if unnamed != 0 && len(rule.ResourceNames) != 0 {
		return refuse(CodeCompositionShape, "create, list and watch cannot be scoped to resource names by RBAC; request them unnamed, bounded to this workspace")
	}

	declared := p.catalog.Compositions(requester)
	for _, resource := range rule.Resources {
		composition, ok := findComposition(declared, group, resource)
		if !ok {
			return refuse(CodeCompositionNotDeclared, fmt.Sprintf("%s declares no composition of %s/%s", requester, group, resource))
		}
		// The dependency the composition hangs off must be the provider that
		// actually exports the group. Otherwise the consent prompt the admin
		// answered ("App Studio manages infrastructure Instances") described a
		// different provider's API than the rule reaches.
		if composition.Dependency != owner {
			return refuse(CodeCompositionNotDeclared, fmt.Sprintf("%s composes %s/%s on dependency %s, but %s exports that group", requester, group, resource, composition.Dependency, owner))
		}
		for _, verb := range rule.Verbs {
			if !containsAny(composition.Verbs, verb) {
				return refuse(CodeCompositionVerbNotDeclared, fmt.Sprintf("%s declares no verb %q on composed %s/%s", requester, verb, group, resource))
			}
		}
	}

	// The dependency has to be enabled here, exactly as for clauses B and C:
	// composing a provider's kinds into a workspace that never accepted that
	// provider would pull it in through the back door.
	if p.bindings == nil {
		return refuse(CodeUnboundProvider, "the hub cannot verify that "+owner+" is enabled in this workspace")
	}
	bound, err := p.bindings.IsBound(clusterID, owner)
	if err != nil {
		return rbacv1.PolicyRule{}, fmt.Errorf("checking whether %s is bound: %w", owner, err)
	}
	if !bound {
		return refuse(CodeUnboundProvider, fmt.Sprintf("%s is not enabled in this workspace", owner))
	}

	// Consent last, so a misdeclared rule is reported as the declaration bug
	// it is rather than as a missing acceptance nobody can grant.
	if p.compositions == nil {
		return refuse(CodeCompositionNotGranted, "the hub cannot read this workspace's composition grants")
	}
	for _, resource := range rule.Resources {
		granted, err := p.compositions.IsComposed(clusterID, requester, group, resource)
		if err != nil {
			return rbacv1.PolicyRule{}, fmt.Errorf("checking whether %s may compose %s/%s: %w", requester, group, resource, err)
		}
		if !granted {
			return refuse(CodeCompositionNotGranted, fmt.Sprintf("nobody in this workspace has accepted that %s manages %s/%s here; a workspace or organization admin accepts it by enabling %s again", requester, group, resource, requester))
		}
	}

	return rbacv1.PolicyRule{
		APIGroups:     []string{group},
		Resources:     dedupeSorted(rule.Resources),
		Verbs:         dedupeSorted(rule.Verbs),
		ResourceNames: dedupeSorted(rule.ResourceNames),
	}, nil
}

func findComposition(declared []Composition, group, resource string) (Composition, bool) {
	for _, composition := range declared {
		if composition.Group == group && composition.Resource == resource {
			return composition, true
		}
	}
	return Composition{}, false
}

// normalizeRules sorts and deduplicates so the same request always yields the
// same ClusterRole bytes.
func normalizeRules(rules []rbacv1.PolicyRule) []rbacv1.PolicyRule {
	seen := map[string]bool{}
	out := make([]rbacv1.PolicyRule, 0, len(rules))
	for _, rule := range rules {
		key := ruleKey(rule)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, rule)
	}
	sort.Slice(out, func(i, j int) bool { return ruleKey(out[i]) < ruleKey(out[j]) })
	return out
}

func ruleKey(rule rbacv1.PolicyRule) string {
	return strings.Join(rule.APIGroups, ",") + "|" + strings.Join(rule.Resources, ",") +
		"|" + strings.Join(rule.Verbs, ",") + "|" + strings.Join(rule.ResourceNames, ",")
}

func dedupeSorted(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	copied := append([]string(nil), values...)
	sort.Strings(copied)
	out := copied[:0]
	for i, value := range copied {
		if i == 0 || value != copied[i-1] {
			out = append(out, value)
		}
	}
	return out
}

func containsAny(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// RegistryCatalog adapts the hub provider registry to ProviderCatalog.
//
// A provider's exported groups are Provider.APIGroups: the groups the catalog
// controller READ from spec.resources[].group on the provider's own APIExport.
// They are emphatically NOT the APIExport's name. An export is named after the
// provider that serves it — `edges.providers.railgrid.ai` — while the kinds it
// serves live in `edges.railgrid.ai`, and a single export may serve several
// groups. Answering "who owns this group" from the export name therefore
// misses every provider whose two names differ (edges, infrastructure, code),
// which is most of them: the edges agent asking for its own
// `edges.railgrid.ai` was told unknown_group.
//
// A provider whose export the hub has not read yet has no groups, and the
// answer here is "nobody owns it" — the policy then refuses the rule with
// unknown_group. That is the fail-closed direction: a guess would hand one
// provider's group to another provider's identity.
//
// Declared verbs come from the entry's actions: an action's ID is {name}/v{n}
// and its boundResource names the resource it applies to, which is exactly the
// {resource}/{verb} coordinate clause C mints.
//
// Declared verbs are the union of two declarations that land on the same RBAC
// coordinate: catalog ACTIONS (spec.actions — versioned, schema'd,
// request/response) and DATA-PLANE VERBS (spec.dataPlane.verbs — unversioned,
// streaming or proxying: exec, ssh, proxy, delegate). Both are enforced
// provider-side by a caller-scoped SSAR on {resource}/{verb}; the declaration
// is what makes the coordinate machine-readable so the hub can tell a real
// verb from an invented one.
type RegistryCatalog struct {
	registry *providers.Registry
}

// NewRegistryCatalog adapts a provider registry.
func NewRegistryCatalog(registry *providers.Registry) *RegistryCatalog {
	return &RegistryCatalog{registry: registry}
}

// GroupOwner implements ProviderCatalog.
//
// The registry spans scopes, so an org-owned provider and a platform one could
// both claim a group. The platform copy wins and ties below it break on name,
// so the answer is the same on every replica and on every call — a map-order
// answer would make one identity request succeed and the next one refuse.
func (c *RegistryCatalog) GroupOwner(apiGroup string) (string, bool) {
	if c == nil || c.registry == nil || apiGroup == "" {
		return "", false
	}
	owner, platform := "", false
	for _, provider := range c.registry.List() {
		if !containsAny(provider.APIGroups, apiGroup) {
			continue
		}
		isPlatform := provider.OrgUUID == ""
		switch {
		case owner == "":
		case platform && !isPlatform:
			continue
		case platform == isPlatform && provider.Name >= owner:
			continue
		}
		owner, platform = provider.Name, isPlatform
	}
	return owner, owner != ""
}

// ExportedGroups implements ProviderCatalog.
func (c *RegistryCatalog) ExportedGroups(provider string) []string {
	if c == nil || c.registry == nil {
		return nil
	}
	entry, ok := c.registry.Get(provider)
	if !ok {
		return nil
	}
	return append([]string(nil), entry.APIGroups...)
}

// DeclaredVerbs implements ProviderCatalog.
func (c *RegistryCatalog) DeclaredVerbs(provider, resource string) []string {
	if c == nil || c.registry == nil {
		return nil
	}
	entry, ok := c.registry.Get(provider)
	if !ok {
		return nil
	}
	verbs := make([]string, 0, len(entry.Actions)+len(entry.DataPlaneVerbs))
	for _, action := range entry.Actions {
		if action.Resource.Resource == resource && action.Name != "" {
			verbs = append(verbs, action.Name)
		}
	}
	for _, verb := range entry.DataPlaneVerbs {
		if verb.Resource == resource && verb.Verb != "" {
			verbs = append(verbs, verb.Verb)
		}
	}
	return dedupeSorted(verbs)
}

// Clause D — the platform allowlist.
//
// A workspace-scoped identity cannot function at all without a few rules that
// belong to no provider: it must be able to ask "may I?" about itself, read
// the LogicalCluster that tells it which workspace it is in, and hold the
// Leases its own coordination needs. None of these is cross-provider access
// and none is negotiable, so rather than leaving every consumer to smuggle
// them in under some other clause, the policy names them explicitly.
//
// The list is CLOSED. Anything else in these groups is refused, including a
// wider verb on an allowed resource and any other resource in the same group —
// `core.kcp.io` also carries Shards, and `coordination.k8s.io` is one group
// away from nothing else at all, but neither is opened by being adjacent.
var platformGroups = map[string]func(rbacv1.PolicyRule, refuseFunc) (rbacv1.PolicyRule, error){
	"authorization.k8s.io":  platformSelfReview("selfsubjectaccessreviews"),
	"authentication.k8s.io": platformSelfReview("selfsubjectreviews"),
	"core.kcp.io":           platformOwnLogicalCluster,
	"coordination.k8s.io":   platformLeases,
	"railgrid.ai":           platformMCPServers,
	"apis.kcp.io":           platformAPIBindings,
}

// refuseFunc is authorizeRule's local refusal closure, threaded into the
// clause D handlers so their refusals carry the offending rule like any other.
type refuseFunc func(code, reason string) (rbacv1.PolicyRule, error)

// platformSelfReview allows `create` on exactly one self-review resource.
// These are the only way an identity can ask the API server what it may do,
// and they are safe by construction: a self-review answers about the CALLER,
// so it can never reveal or confer anything the caller does not already have.
// They take no resourceNames because the API has no names to scope to.
func platformSelfReview(resource string) func(rbacv1.PolicyRule, refuseFunc) (rbacv1.PolicyRule, error) {
	return func(rule rbacv1.PolicyRule, refuse refuseFunc) (rbacv1.PolicyRule, error) {
		if len(rule.Resources) != 1 || rule.Resources[0] != resource {
			return refuse(CodePlatformNotAllowed, fmt.Sprintf("only %q is minted in API group %q", resource, rule.APIGroups[0]))
		}
		if len(rule.Verbs) != 1 || rule.Verbs[0] != "create" {
			return refuse(CodePlatformNotAllowed, fmt.Sprintf("only create is minted on %s", resource))
		}
		if len(rule.ResourceNames) != 0 {
			return refuse(CodePlatformNotAllowed, fmt.Sprintf("%s has no resource names to scope to", resource))
		}
		return rbacv1.PolicyRule{
			APIGroups: []string{rule.APIGroups[0]},
			Resources: []string{resource}, Verbs: []string{"create"},
		}, nil
	}
}

// platformOwnLogicalCluster allows `get` on the workspace's own
// LogicalCluster, and only on it. kcp names that object exactly "cluster", so
// the rule is name-scoped to it: an identity may learn which workspace it is
// in — which several data planes check before they will send anything — and
// nothing else in core.kcp.io.
func platformOwnLogicalCluster(rule rbacv1.PolicyRule, refuse refuseFunc) (rbacv1.PolicyRule, error) {
	if len(rule.Resources) != 1 || rule.Resources[0] != "logicalclusters" {
		return refuse(CodePlatformNotAllowed, "only logicalclusters is minted in API group \"core.kcp.io\"")
	}
	if len(rule.Verbs) != 1 || rule.Verbs[0] != "get" {
		return refuse(CodePlatformNotAllowed, "only get is minted on logicalclusters")
	}
	if len(rule.ResourceNames) != 1 || rule.ResourceNames[0] != "cluster" {
		return refuse(CodePlatformNotAllowed, "logicalclusters is minted only for the workspace's own object, named \"cluster\"")
	}
	return rbacv1.PolicyRule{
		APIGroups: []string{"core.kcp.io"}, Resources: []string{"logicalclusters"},
		Verbs: []string{"get"}, ResourceNames: []string{"cluster"},
	}, nil
}

// platformLeaseVerbs is the closed verb set for Leases. `list` and `watch` are
// absent deliberately: like every other name-scoped rule in this policy they
// cannot be scoped to names by RBAC, and an identity holding its own Leases
// has no business enumerating everybody else's.
var platformLeaseVerbs = map[string]bool{
	"get": true, "create": true, "update": true, "patch": true, "delete": true,
}

// platformLeases allows an identity to hold its OWN coordination Leases,
// name-scoped. Leader election and occupancy accounting both need this, and
// neither needs a Lease it did not name.
func platformLeases(rule rbacv1.PolicyRule, refuse refuseFunc) (rbacv1.PolicyRule, error) {
	if len(rule.Resources) != 1 || rule.Resources[0] != "leases" {
		return refuse(CodePlatformNotAllowed, "only leases is minted in API group \"coordination.k8s.io\"")
	}
	for _, verb := range rule.Verbs {
		if !platformLeaseVerbs[verb] {
			return refuse(CodePlatformNotAllowed, fmt.Sprintf("verb %q is not minted on leases (get, create, update, patch and delete are)", verb))
		}
	}
	if len(rule.ResourceNames) == 0 {
		// `create` is the one verb a name cannot bound: Kubernetes RBAC does
		// not apply resourceNames to a create request, so a named create
		// authorizes nothing at all — an identity holding one could never
		// create the occupancy Lease it was granted. It is therefore minted
		// unnamed, and alone: what it buys is a Lease in this workspace that
		// the identity then owns, while reading or changing anyone else's
		// still requires naming it.
		for _, verb := range rule.Verbs {
			if verb != "create" {
				return refuse(CodeUnnamedForeign, "a leases rule must name the exact Leases it covers; only create is minted unnamed, because RBAC ignores resourceNames on a create request")
			}
		}
		if len(rule.Verbs) == 0 {
			return refuse(CodeUnnamedForeign, "a leases rule must name the exact Leases it covers")
		}
		return rbacv1.PolicyRule{
			APIGroups: []string{"coordination.k8s.io"}, Resources: []string{"leases"},
			Verbs: []string{"create"},
		}, nil
	}
	return rbacv1.PolicyRule{
		APIGroups: []string{"coordination.k8s.io"}, Resources: []string{"leases"},
		Verbs: dedupeSorted(rule.Verbs), ResourceNames: dedupeSorted(rule.ResourceNames),
	}, nil
}

// platformMCPServers allows `use` on a NAMED MCPServer: the verb the hub MCP
// aggregate itself SARs before it will admit a caller
// (pkg/hub/mcpaggregate/verifier.go, which reviews
// railgrid.ai/mcpservers/{name} for verb "use"). Admission to the aggregate
// confers nothing downstream — federation keeps forwarding the caller's own
// bearer to each provider, so a caller still reaches exactly the providers it
// could reach anyway. What this rule buys a scoped identity is the door, not
// what is behind it.
//
// `use` is the only verb minted here, and only on named servers: an unnamed
// rule would admit an identity to every aggregate in the workspace, and any
// other verb would be a lifecycle grant on the MCPServer object itself, which
// is a tenant's to make and not an identity's to hold.
func platformMCPServers(rule rbacv1.PolicyRule, refuse refuseFunc) (rbacv1.PolicyRule, error) {
	if len(rule.Resources) != 1 || rule.Resources[0] != "mcpservers" {
		return refuse(CodePlatformNotAllowed, "only mcpservers is minted in API group \"railgrid.ai\"")
	}
	if len(rule.Verbs) != 1 || rule.Verbs[0] != "use" {
		return refuse(CodePlatformNotAllowed, "only use is minted on mcpservers (it is the verb the MCP aggregate reviews)")
	}
	if len(rule.ResourceNames) == 0 {
		return refuse(CodeUnnamedForeign, "an mcpservers rule must name the exact servers it admits the identity to")
	}
	return rbacv1.PolicyRule{
		APIGroups: []string{"railgrid.ai"}, Resources: []string{"mcpservers"},
		Verbs: []string{"use"}, ResourceNames: dedupeSorted(rule.ResourceNames),
	}, nil
}

// platformAPIBindings allows `get` on a NAMED APIBinding. An identity that may
// reach another provider's group has to learn which provider serves it in this
// workspace — the answer is the workspace's own binding, not a compiled-in
// string (review X-8). Today the two consumers that need this get away with an
// interactive caller having warmed a cache; a background identity has no such
// caller.
//
// `get` on a named binding, rather than the `list` those consumers do today:
// the hub names each binding after the provider it enables
// (pkg/hub/restapi/providers_enable.go, and the comment at
// providers/app-studio/api/provider_binding.go:132 depends on it), so the
// name is already known to anyone who knows which provider they want. Listing
// would hand an identity the full inventory of what a tenant has enabled,
// which is a good deal more than "does the provider I was granted access to
// answer here".
func platformAPIBindings(rule rbacv1.PolicyRule, refuse refuseFunc) (rbacv1.PolicyRule, error) {
	if len(rule.Resources) != 1 || rule.Resources[0] != "apibindings" {
		return refuse(CodePlatformNotAllowed, "only apibindings is minted in API group \"apis.kcp.io\"")
	}
	if len(rule.Verbs) != 1 || rule.Verbs[0] != "get" {
		return refuse(CodePlatformNotAllowed, "only get is minted on apibindings")
	}
	if len(rule.ResourceNames) == 0 {
		return refuse(CodeUnnamedForeign, "an apibindings rule must name the exact bindings it covers; the hub names each binding after the provider it enables")
	}
	return rbacv1.PolicyRule{
		APIGroups: []string{"apis.kcp.io"}, Resources: []string{"apibindings"},
		Verbs: []string{"get"}, ResourceNames: dedupeSorted(rule.ResourceNames),
	}, nil
}
