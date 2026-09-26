/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package identity

import (
	"errors"
	"slices"
	"sort"
	"testing"

	"github.com/railgrid/provider-sdk/dataplane"
	rbacv1 "k8s.io/api/rbac/v1"
)

// fakeCatalog is the policy's view of the provider registry, written out by
// hand so the table below reads as a policy statement rather than as registry
// plumbing.
type fakeCatalog struct {
	groups map[string]string   // apiGroup -> owning provider
	verbs  map[string][]string // provider/resource -> declared action names
	// dataPlane is the second half of the union DeclaredVerbs returns:
	// spec.dataPlane.verbs, which is where exec/proxy/ssh/delegate live.
	dataPlane map[string][]string
}

func (c fakeCatalog) GroupOwner(apiGroup string) (string, bool) {
	owner, ok := c.groups[apiGroup]
	return owner, ok
}

func (c fakeCatalog) ExportedGroups(provider string) []string {
	var out []string
	for group, owner := range c.groups {
		if owner == provider {
			out = append(out, group)
		}
	}
	return out
}

func (c fakeCatalog) DeclaredVerbs(provider, resource string) []string {
	key := provider + "/" + resource
	return append(append([]string(nil), c.verbs[key]...), c.dataPlane[key]...)
}

type fakeBindings struct {
	bound map[string]bool
	err   error
}

func (b fakeBindings) IsBound(clusterID, provider string) (bool, error) {
	if b.err != nil {
		return false, b.err
	}
	return b.bound[provider], nil
}

func testPolicy() *Policy {
	return NewPolicy(fakeCatalog{
		groups: map[string]string{
			"edges.railgrid.ai":          "edges",
			"agents.railgrid.ai":         "agents",
			"infrastructure.railgrid.ai": "infrastructure",
			"databricks.railgrid.ai":     "databricks",
			"factory.railgrid.ai":        "factory",
			"code.railgrid.ai":           "code",
			"unbound.railgrid.ai":        "unbound",
		},
		verbs: map[string][]string{
			"databricks/tables": {"query_table"},
		},
		dataPlane: map[string][]string{
			"infrastructure/instances": {"exec", "logs", "proxy", "restart"},
			"edges/kubernetesclusters": {"k8s", "ssh", "mcp", "proxy"},
			"edges/services":           {"proxy", "mcp"},
			"agents/agents":            {"chat", "run", "delegate"},
		},
	}, fakeBindings{bound: map[string]bool{
		"edges": true, "agents": true, "infrastructure": true, "databricks": true,
		"factory": true, "code": true,
	}})
}

func rule(group string, resources, verbs, names []string) rbacv1.PolicyRule {
	return rbacv1.PolicyRule{APIGroups: []string{group}, Resources: resources, Verbs: verbs, ResourceNames: names}
}

// RBAC ignores resourceNames on a create request, so a named create grants
// nothing: an identity holding one could never create the occupancy Lease the
// scheduler needs. create is therefore minted unnamed and alone, while every
// other verb on leases still has to name what it covers.
func TestLeasesMintCreateUnnamedAndRefuseOtherUnnamedVerbs(t *testing.T) {
	policy := testPolicy()

	minted, err := policy.Authorize("factory", "cluster-1", []rbacv1.PolicyRule{{
		APIGroups: []string{"coordination.k8s.io"},
		Resources: []string{"leases"},
		Verbs:     []string{"create"},
	}})
	if err != nil {
		t.Fatalf("unnamed create on leases was refused: %v", err)
	}
	if len(minted) != 1 || len(minted[0].ResourceNames) != 0 || len(minted[0].Verbs) != 1 || minted[0].Verbs[0] != "create" {
		t.Fatalf("minted = %+v, want one unnamed create rule", minted)
	}

	if _, err := policy.Authorize("factory", "cluster-1", []rbacv1.PolicyRule{{
		APIGroups: []string{"coordination.k8s.io"},
		Resources: []string{"leases"},
		Verbs:     []string{"create", "get"},
	}}); err == nil {
		t.Fatal("an unnamed rule carrying get was admitted; only create is minted unnamed")
	}

	named, err := policy.Authorize("factory", "cluster-1", []rbacv1.PolicyRule{{
		APIGroups:     []string{"coordination.k8s.io"},
		Resources:     []string{"leases"},
		ResourceNames: []string{"factory-worker-x"},
		Verbs:         []string{"get", "update", "patch", "delete"},
	}})
	if err != nil {
		t.Fatalf("named lease verbs were refused: %v", err)
	}
	if len(named) != 1 || len(named[0].ResourceNames) != 1 {
		t.Fatalf("minted = %+v, want the named rule kept", named)
	}
}

func TestPolicyTable(t *testing.T) {
	for _, tc := range []struct {
		name      string
		requester string
		rule      rbacv1.PolicyRule
		wantCode  string // empty = allowed
	}{
		// Clause A — the requester's own exported group.
		{
			name:      "edges may take proxy on its own edge, name scoped",
			requester: "edges",
			rule:      rule("edges.railgrid.ai", []string{"kubernetesclusters"}, []string{"proxy"}, []string{"edge-1"}),
		},
		{
			name:      "edges may take write verbs on its own group",
			requester: "edges",
			rule:      rule("edges.railgrid.ai", []string{"placements/status"}, []string{"update", "patch"}, nil),
		},
		{
			name:      "own group does not require resource names",
			requester: "agents",
			rule:      rule("agents.railgrid.ai", []string{"runs"}, []string{"create"}, nil),
		},

		// Clause B — named reads on a foreign group.
		{
			name:      "agents may read one named instance",
			requester: "agents",
			rule:      rule("infrastructure.railgrid.ai", []string{"instances"}, []string{"get"}, []string{"search-1"}),
		},
		{
			name:      "an unnamed read of a foreign group is refused",
			requester: "agents",
			rule:      rule("infrastructure.railgrid.ai", []string{"instances"}, []string{"get"}, nil),
			wantCode:  CodeUnnamedForeign,
		},
		{
			name:      "list on a foreign group is refused: RBAC cannot name-scope it",
			requester: "factory",
			rule:      rule("edges.railgrid.ai", []string{"services"}, []string{"get", "list"}, []string{"runner"}),
			wantCode:  CodeForeignList,
		},
		{
			name:      "watch on a foreign group is refused for the same reason",
			requester: "factory",
			rule:      rule("edges.railgrid.ai", []string{"services"}, []string{"watch"}, []string{"runner"}),
			wantCode:  CodeForeignList,
		},
		{
			name:      "a write on a foreign group is refused",
			requester: "agents",
			rule:      rule("infrastructure.railgrid.ai", []string{"instances"}, []string{"update"}, []string{"search-1"}),
			wantCode:  CodeForeignWrite,
		},
		{
			name:      "the agents provider may not read a group nobody exports",
			requester: "agents",
			rule:      rule("made-up.railgrid.ai", []string{"things"}, []string{"get"}, []string{"thing"}),
			wantCode:  CodeUnknownProvider,
		},

		// Clause C — declared verb subresources on a foreign group.
		{
			name:      "app-studio style create on a declared action subresource",
			requester: "agents",
			rule:      rule("databricks.railgrid.ai", []string{"tables/query_table"}, []string{"create"}, []string{"sales"}),
		},
		{
			name:      "an undeclared verb subresource is refused",
			requester: "agents",
			rule:      rule("databricks.railgrid.ai", []string{"tables/drop_table"}, []string{"create"}, []string{"sales"}),
			wantCode:  CodeUndeclaredVerb,
		},
		{
			name:      "a verb subresource on a foreign group must still be named",
			requester: "agents",
			rule:      rule("databricks.railgrid.ai", []string{"tables/query_table"}, []string{"create"}, nil),
			wantCode:  CodeUnnamedForeign,
		},
		{
			name:      "a foreign rule may not mix resources and verb subresources",
			requester: "agents",
			rule:      rule("databricks.railgrid.ai", []string{"tables", "tables/query_table"}, []string{"create"}, []string{"sales"}),
			wantCode:  CodeMultiGroup,
		},

		// Shapes nobody gets, whatever they own.
		{
			name:      "the core group is never minted",
			requester: "edges",
			rule:      rule("", []string{"secrets"}, []string{"get"}, []string{"edge-1-creds"}),
			wantCode:  CodeCoreGroup,
		},
		{
			name:      "a wildcard verb is never minted, even on the requester's own group",
			requester: "edges",
			rule:      rule("edges.railgrid.ai", []string{"kubernetesclusters"}, []string{"*"}, []string{"edge-1"}),
			wantCode:  CodeWildcard,
		},
		{
			name:      "a wildcard resource is never minted",
			requester: "agents",
			rule:      rule("agents.railgrid.ai", []string{"*"}, []string{"get"}, nil),
			wantCode:  CodeWildcard,
		},
		{
			name:      "a wildcard group is never minted",
			requester: "agents",
			rule:      rule("*", []string{"things"}, []string{"get"}, []string{"t"}),
			wantCode:  CodeWildcard,
		},
		{
			name:      "a rule must name exactly one group",
			requester: "agents",
			rule:      rbacv1.PolicyRule{APIGroups: []string{"agents.railgrid.ai", "edges.railgrid.ai"}, Resources: []string{"runs"}, Verbs: []string{"get"}},
			wantCode:  CodeMultiGroup,
		},
		{
			name:      "a rule with no verbs is refused",
			requester: "agents",
			rule:      rule("agents.railgrid.ai", []string{"runs"}, nil, nil),
			wantCode:  CodeEmptyRule,
		},
		{
			name:      "non-resource URL rules are never minted",
			requester: "agents",
			rule:      rbacv1.PolicyRule{NonResourceURLs: []string{"/healthz"}, Verbs: []string{"get"}},
			wantCode:  CodeNonResourceRule,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := testPolicy().Authorize(tc.requester, "cluster-1", []rbacv1.PolicyRule{tc.rule})
			if tc.wantCode == "" {
				if err != nil {
					t.Fatalf("rule was refused: %v", err)
				}
				if len(got) != 1 {
					t.Fatalf("got %d rules, want 1", len(got))
				}
				return
			}
			var refusal Refusal
			if !errors.As(err, &refusal) {
				t.Fatalf("want refusal %q, got err=%v rules=%v", tc.wantCode, err, got)
			}
			if refusal.Code != tc.wantCode {
				t.Fatalf("refusal code = %q, want %q (reason: %s)", refusal.Code, tc.wantCode, refusal.Reason)
			}
		})
	}
}

func TestPolicyRefusesForeignGroupOfAnUnboundProvider(t *testing.T) {
	policy := NewPolicy(fakeCatalog{groups: map[string]string{
		"agents.railgrid.ai":         "agents",
		"infrastructure.railgrid.ai": "infrastructure",
	}}, fakeBindings{bound: map[string]bool{}})
	_, err := policy.Authorize("agents", "cluster-1", []rbacv1.PolicyRule{
		rule("infrastructure.railgrid.ai", []string{"instances"}, []string{"get"}, []string{"search-1"}),
	})
	var refusal Refusal
	if !errors.As(err, &refusal) || refusal.Code != CodeUnboundProvider {
		t.Fatalf("want %q for an unenabled provider, got %v", CodeUnboundProvider, err)
	}
}

func TestPolicyRefusesForeignGroupWhenBindingsCannotBeChecked(t *testing.T) {
	policy := NewPolicy(fakeCatalog{groups: map[string]string{
		"agents.railgrid.ai":         "agents",
		"infrastructure.railgrid.ai": "infrastructure",
	}}, nil)
	_, err := policy.Authorize("agents", "cluster-1", []rbacv1.PolicyRule{
		rule("infrastructure.railgrid.ai", []string{"instances"}, []string{"get"}, []string{"search-1"}),
	})
	var refusal Refusal
	if !errors.As(err, &refusal) || refusal.Code != CodeUnboundProvider {
		t.Fatalf("a hub that cannot check bindings must fail closed, got %v", err)
	}
}

func TestPolicyNormalizesSoTheSameRequestIsTheSameRole(t *testing.T) {
	requested := []rbacv1.PolicyRule{
		rule("edges.railgrid.ai", []string{"linuxservers", "kubernetesclusters"}, []string{"watch", "get"}, []string{"b", "a"}),
		rule("edges.railgrid.ai", []string{"kubernetesclusters", "linuxservers"}, []string{"get", "watch"}, []string{"a", "b"}),
	}
	got, err := testPolicy().Authorize("edges", "cluster-1", requested)
	if err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("identical rules were not deduplicated: %#v", got)
	}
	want := rbacv1.PolicyRule{
		APIGroups: []string{"edges.railgrid.ai"},
		Resources: []string{"kubernetesclusters", "linuxservers"},
		Verbs:     []string{"get", "watch"}, ResourceNames: []string{"a", "b"},
	}
	if ruleKey(got[0]) != ruleKey(want) {
		t.Fatalf("normalized rule = %#v, want %#v", got[0], want)
	}
}

func TestPolicyBoundsRuleCount(t *testing.T) {
	requested := make([]rbacv1.PolicyRule, maxRules+1)
	for i := range requested {
		requested[i] = rule("agents.railgrid.ai", []string{"runs"}, []string{"get"}, nil)
	}
	_, err := testPolicy().Authorize("agents", "cluster-1", requested)
	var refusal Refusal
	if !errors.As(err, &refusal) || refusal.Code != CodeRuleLimit {
		t.Fatalf("want %q, got %v", CodeRuleLimit, err)
	}
}

// Clause C admits a declared DATA-PLANE verb as readily as a declared action.
// Before spec.dataPlane.verbs existed, exec/proxy/ssh/delegate were in
// provider code only, so none of these could be minted at all.
func TestPolicyAdmitsDeclaredDataPlaneVerbs(t *testing.T) {
	for _, tc := range []struct {
		name      string
		requester string
		rule      rbacv1.PolicyRule
		wantCode  string
	}{
		{
			name:      "app-studio style exec on one named instance",
			requester: "agents",
			rule:      rule("infrastructure.railgrid.ai", []string{"instances/exec"}, []string{"create"}, []string{"search-1"}),
		},
		{
			name:      "factory style proxy on one named edges Service",
			requester: "factory",
			rule:      rule("edges.railgrid.ai", []string{"services/proxy"}, []string{"create"}, []string{"runner-7"}),
		},
		{
			name:      "an agents delegate coordinate",
			requester: "edges",
			rule:      rule("agents.railgrid.ai", []string{"agents/delegate"}, []string{"create"}, []string{"scheduler"}),
		},
		{
			name:      "actions and data-plane verbs are one vocabulary",
			requester: "agents",
			rule:      rule("databricks.railgrid.ai", []string{"tables/query_table"}, []string{"create"}, []string{"sales"}),
		},
		{
			name:      "a verb the owner does not declare is still refused",
			requester: "agents",
			rule:      rule("infrastructure.railgrid.ai", []string{"instances/rm"}, []string{"create"}, []string{"search-1"}),
			wantCode:  CodeUndeclaredVerb,
		},
		{
			name:      "a per-edge mcp coordinate",
			requester: "factory",
			rule:      rule("edges.railgrid.ai", []string{"kubernetesclusters/mcp"}, []string{"create"}, []string{"edge-1"}),
		},
		{
			name:      "a verb declared on another resource does not carry over",
			requester: "agents",
			rule:      rule("edges.railgrid.ai", []string{"services/ssh"}, []string{"create"}, []string{"runner-7"}),
			wantCode:  CodeUndeclaredVerb,
		},
		{
			name:      "a data-plane verb still has to be name-scoped",
			requester: "agents",
			rule:      rule("infrastructure.railgrid.ai", []string{"instances/exec"}, []string{"create"}, nil),
			wantCode:  CodeUnnamedForeign,
		},
		{
			// The coordinate is the capability: whatever verb the requester
			// spells, the full verb set is minted, because kcp maps the HTTP
			// method a verb uses onto the RBAC verb.
			name:      "any verb on a data-plane coordinate mints every kcp verb",
			requester: "agents",
			rule:      rule("infrastructure.railgrid.ai", []string{"instances/exec"}, []string{"get"}, []string{"search-1"}),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := testPolicy().Authorize(tc.requester, "cluster-1", []rbacv1.PolicyRule{tc.rule})
			if tc.wantCode == "" {
				if err != nil {
					t.Fatalf("rule was refused: %v", err)
				}
				want := append([]string(nil), dataplane.SubresourceVerbs...)
				sort.Strings(want)
				if len(got) != 1 || !slices.Equal(got[0].Verbs, want) {
					t.Fatalf("minted rule = %#v, want every kcp verb on the coordinate", got)
				}
				return
			}
			var refusal Refusal
			if !errors.As(err, &refusal) || refusal.Code != tc.wantCode {
				t.Fatalf("want refusal %q, got %v", tc.wantCode, err)
			}
		})
	}
}

// Clause D: the closed platform allowlist. Every entry is checked for the
// exact shape admitted AND for the adjacent shapes that are not.
func TestPolicyPlatformAllowlist(t *testing.T) {
	for _, tc := range []struct {
		name     string
		rule     rbacv1.PolicyRule
		wantCode string
		wantRule rbacv1.PolicyRule
	}{
		{
			name:     "self subject access review",
			rule:     rbacv1.PolicyRule{APIGroups: []string{"authorization.k8s.io"}, Resources: []string{"selfsubjectaccessreviews"}, Verbs: []string{"create"}},
			wantRule: rbacv1.PolicyRule{APIGroups: []string{"authorization.k8s.io"}, Resources: []string{"selfsubjectaccessreviews"}, Verbs: []string{"create"}},
		},
		{
			name:     "self subject review",
			rule:     rbacv1.PolicyRule{APIGroups: []string{"authentication.k8s.io"}, Resources: []string{"selfsubjectreviews"}, Verbs: []string{"create"}},
			wantRule: rbacv1.PolicyRule{APIGroups: []string{"authentication.k8s.io"}, Resources: []string{"selfsubjectreviews"}, Verbs: []string{"create"}},
		},
		{
			name:     "the workspace's own LogicalCluster",
			rule:     rule("core.kcp.io", []string{"logicalclusters"}, []string{"get"}, []string{"cluster"}),
			wantRule: rule("core.kcp.io", []string{"logicalclusters"}, []string{"get"}, []string{"cluster"}),
		},
		{
			name:     "named leases",
			rule:     rule("coordination.k8s.io", []string{"leases"}, []string{"get", "create", "update", "patch", "delete"}, []string{"factory-worker-3"}),
			wantRule: rule("coordination.k8s.io", []string{"leases"}, []string{"create", "delete", "get", "patch", "update"}, []string{"factory-worker-3"}),
		},

		{
			// The verb pkg/hub/mcpaggregate/verifier.go actually reviews.
			name:     "use on a named MCPServer admits the identity to the aggregate",
			rule:     rule("railgrid.ai", []string{"mcpservers"}, []string{"use"}, []string{"default"}),
			wantRule: rule("railgrid.ai", []string{"mcpservers"}, []string{"use"}, []string{"default"}),
		},
		{
			name:     "get on a named APIBinding",
			rule:     rule("apis.kcp.io", []string{"apibindings"}, []string{"get"}, []string{"infrastructure"}),
			wantRule: rule("apis.kcp.io", []string{"apibindings"}, []string{"get"}, []string{"infrastructure"}),
		},

		// The adjacent shapes that are NOT allowed.
		{
			name:     "a non-self access review is not a self review",
			rule:     rbacv1.PolicyRule{APIGroups: []string{"authorization.k8s.io"}, Resources: []string{"subjectaccessreviews"}, Verbs: []string{"create"}},
			wantCode: CodePlatformNotAllowed,
		},
		{
			name:     "a token review is not a self review",
			rule:     rbacv1.PolicyRule{APIGroups: []string{"authentication.k8s.io"}, Resources: []string{"tokenreviews"}, Verbs: []string{"create"}},
			wantCode: CodePlatformNotAllowed,
		},
		{
			name:     "a self review may only be created",
			rule:     rbacv1.PolicyRule{APIGroups: []string{"authorization.k8s.io"}, Resources: []string{"selfsubjectaccessreviews"}, Verbs: []string{"create", "list"}},
			wantCode: CodePlatformNotAllowed,
		},
		{
			name:     "another workspace's LogicalCluster is not minted",
			rule:     rule("core.kcp.io", []string{"logicalclusters"}, []string{"get"}, []string{"other"}),
			wantCode: CodePlatformNotAllowed,
		},
		{
			name:     "an unnamed LogicalCluster read is not minted",
			rule:     rule("core.kcp.io", []string{"logicalclusters"}, []string{"get"}, nil),
			wantCode: CodePlatformNotAllowed,
		},
		{
			name:     "LogicalCluster is read-only",
			rule:     rule("core.kcp.io", []string{"logicalclusters"}, []string{"get", "patch"}, []string{"cluster"}),
			wantCode: CodePlatformNotAllowed,
		},
		{
			name:     "core.kcp.io opens nothing beyond logicalclusters",
			rule:     rule("core.kcp.io", []string{"shards"}, []string{"get"}, []string{"root"}),
			wantCode: CodePlatformNotAllowed,
		},
		{
			name:     "leases must be named",
			rule:     rule("coordination.k8s.io", []string{"leases"}, []string{"get"}, nil),
			wantCode: CodeUnnamedForeign,
		},
		{
			name:     "leases cannot be listed",
			rule:     rule("coordination.k8s.io", []string{"leases"}, []string{"get", "list"}, []string{"factory-worker-3"}),
			wantCode: CodePlatformNotAllowed,
		},
		{
			name:     "coordination.k8s.io opens nothing else",
			rule:     rule("coordination.k8s.io", []string{"leasecandidates"}, []string{"get"}, []string{"x"}),
			wantCode: CodePlatformNotAllowed,
		},
		{
			name:     "an unnamed MCPServer rule would admit the identity to every aggregate",
			rule:     rule("railgrid.ai", []string{"mcpservers"}, []string{"use"}, nil),
			wantCode: CodeUnnamedForeign,
		},
		{
			name:     "MCPServers cannot be listed",
			rule:     rule("railgrid.ai", []string{"mcpservers"}, []string{"use", "list"}, []string{"default"}),
			wantCode: CodePlatformNotAllowed,
		},
		{
			name:     "MCPServers cannot be watched",
			rule:     rule("railgrid.ai", []string{"mcpservers"}, []string{"watch"}, []string{"default"}),
			wantCode: CodePlatformNotAllowed,
		},
		{
			// use is admission, not lifecycle: owning the MCPServer object is
			// the tenant's to decide, not an identity's to hold.
			name:     "an MCPServer lifecycle verb is not minted",
			rule:     rule("railgrid.ai", []string{"mcpservers"}, []string{"update"}, []string{"default"}),
			wantCode: CodePlatformNotAllowed,
		},
		{
			name:     "reading the MCPServer object is not the same as using it",
			rule:     rule("railgrid.ai", []string{"mcpservers"}, []string{"get"}, []string{"default"}),
			wantCode: CodePlatformNotAllowed,
		},
		{
			name:     "railgrid.ai opens nothing beyond mcpservers",
			rule:     rule("railgrid.ai", []string{"grants"}, []string{"get"}, []string{"g"}),
			wantCode: CodePlatformNotAllowed,
		},
		{
			// Listing would hand over the full inventory of what the tenant
			// has enabled, not "does the provider I was granted answer here".
			name:     "APIBindings cannot be listed",
			rule:     rule("apis.kcp.io", []string{"apibindings"}, []string{"get", "list"}, []string{"infrastructure"}),
			wantCode: CodePlatformNotAllowed,
		},
		{
			name:     "APIBindings cannot be watched",
			rule:     rule("apis.kcp.io", []string{"apibindings"}, []string{"watch"}, []string{"infrastructure"}),
			wantCode: CodePlatformNotAllowed,
		},
		{
			name:     "an unnamed APIBinding read is not minted",
			rule:     rule("apis.kcp.io", []string{"apibindings"}, []string{"get"}, nil),
			wantCode: CodeUnnamedForeign,
		},
		{
			name:     "APIBindings are read-only",
			rule:     rule("apis.kcp.io", []string{"apibindings"}, []string{"get", "patch"}, []string{"infrastructure"}),
			wantCode: CodePlatformNotAllowed,
		},
		{
			name:     "apis.kcp.io opens nothing beyond apibindings",
			rule:     rule("apis.kcp.io", []string{"apiexports"}, []string{"get"}, []string{"infrastructure.railgrid.ai"}),
			wantCode: CodePlatformNotAllowed,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// The requester is irrelevant to clause D: these rules are the
			// same for every provider, which is why they are a fixed list and
			// not a per-provider negotiation.
			got, err := testPolicy().Authorize("factory", "cluster-1", []rbacv1.PolicyRule{tc.rule})
			if tc.wantCode != "" {
				var refusal Refusal
				if !errors.As(err, &refusal) || refusal.Code != tc.wantCode {
					t.Fatalf("want refusal %q, got err=%v rules=%#v", tc.wantCode, err, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("rule was refused: %v", err)
			}
			if len(got) != 1 || ruleKey(got[0]) != ruleKey(tc.wantRule) {
				t.Fatalf("minted rule = %#v, want %#v", got, tc.wantRule)
			}
		})
	}
}

// Clause D is checked before clause A, so a provider cannot reach a platform
// group through its own-group branch even if it somehow exported one.
func TestPlatformAllowlistIsNotWidenedByOwningTheGroup(t *testing.T) {
	policy := NewPolicy(fakeCatalog{groups: map[string]string{
		"coordination.k8s.io": "rogue",
	}}, fakeBindings{bound: map[string]bool{"rogue": true}})
	_, err := policy.Authorize("rogue", "cluster-1", []rbacv1.PolicyRule{
		rule("coordination.k8s.io", []string{"leases"}, []string{"get", "list", "watch"}, nil),
	})
	var refusal Refusal
	if !errors.As(err, &refusal) {
		t.Fatalf("a platform group was widened through clause A: %v", err)
	}
}
