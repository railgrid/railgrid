// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package api

import (
	"context"
	"slices"
	"strings"
	"testing"

	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	agentsv1alpha1 "github.com/railgrid/provider-agents/apis/v1alpha1"
	agentsclient "github.com/railgrid/provider-agents/client"
	"github.com/railgrid/provider-agents/tools"
)

// identityWorkspace is a tenant workspace with a connection wired to a browser
// instance, a toolset that carries a second one, and a connection that reaches
// a public endpoint with its own credential and needs no platform identity.
func identityWorkspace() *dynamicfake.FakeDynamicClient {
	object := func(gvr schema.GroupVersionResource, kind, name string, spec map[string]any) *unstructured.Unstructured {
		return &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": gvr.GroupVersion().String(),
			"kind":       kind,
			"metadata":   map[string]any{"name": name},
			"spec":       spec,
		}}
	}
	return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		agentsclient.ConnectionGVR: "ConnectionList",
		agentsclient.ToolsetGVR:    "ToolsetList",
	},
		object(agentsclient.ConnectionGVR, "Connection", "browser", map[string]any{
			"type":   "mcp",
			"config": map[string]any{"instance": "chrome-1", "instanceResource": "browsers"},
		}),
		object(agentsclient.ConnectionGVR, "Connection", "search", map[string]any{
			"type":   "websearch",
			"config": map[string]any{"instance": "searx-1"},
		}),
		object(agentsclient.ConnectionGVR, "Connection", "github", map[string]any{
			"type":   "github",
			"config": map[string]any{},
		}),
		object(agentsclient.ToolsetGVR, "Toolset", "research", map[string]any{
			"connections": []any{"search"},
		}),
	)
}

func agentWiredTo(interactive, toolsets []string) *agentsv1alpha1.Agent {
	return &agentsv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: "scout", UID: "agent-uid-1"},
		Spec: agentsv1alpha1.AgentSpec{
			Tools: agentsv1alpha1.AgentToolPolicy{
				Interactive: agentsv1alpha1.ToolGrant{Connections: interactive, Toolsets: toolsets},
			},
		},
	}
}

func ruleFor(rules []rbacv1.PolicyRule, resource string) *rbacv1.PolicyRule {
	for i := range rules {
		if slices.Contains(rules[i].Resources, resource) {
			return &rules[i]
		}
	}
	return nil
}

// The grant this replaced was get+list on EVERY resource in the instance API
// group: a standing key to every instance in the workspace, including browser
// instances holding live logins the agent was never wired to. These rules name
// the instances the agent actually references, and nothing else.
func TestIdentityRulesNameOnlyTheReferencedInstances(t *testing.T) {
	identities := newAgentIdentities(nil)
	rules, err := identities.rulesFor(context.Background(), identityWorkspace(),
		agentWiredTo([]string{"browser", "github"}, []string{"research"}))
	if err != nil {
		t.Fatal(err)
	}

	// Clause B: see the instance, by name.
	browsers := ruleFor(rules, "browsers")
	if browsers == nil || !slices.Equal(browsers.ResourceNames, []string{"chrome-1"}) {
		t.Fatalf("browsers rule = %+v, want a get on chrome-1 alone", browsers)
	}
	if !slices.Equal(browsers.Verbs, []string{"get"}) {
		t.Errorf("browsers verbs = %v, want get only", browsers.Verbs)
	}
	// A toolset's connections count: the agent reaches them through it.
	instances := ruleFor(rules, "instances")
	if instances == nil || !slices.Equal(instances.ResourceNames, []string{"searx-1"}) {
		t.Fatalf("instances rule = %+v, want the toolset's instance", instances)
	}

	// Clause C: act on those same instances, one rule per declared verb.
	for _, verb := range instanceVerbs {
		rule := ruleFor(rules, "browsers/"+verb)
		if rule == nil || !slices.Equal(rule.ResourceNames, []string{"chrome-1"}) {
			t.Fatalf("browsers/%s rule = %+v, want it scoped to chrome-1", verb, rule)
		}
		if !slices.Equal(rule.Verbs, []string{"create"}) {
			t.Errorf("browsers/%s verbs = %v; the data plane expresses a grant as create", verb, rule.Verbs)
		}
	}

	// Nothing is granted on a wildcard, ever. This is the assertion that would
	// have caught the old grant.
	for _, rule := range rules {
		if slices.Contains(rule.Resources, "*") || slices.Contains(rule.ResourceNames, "*") {
			t.Fatalf("a wildcard rule reached the identity request: %+v", rule)
		}
		if len(rule.ResourceNames) == 0 {
			t.Fatalf("an unnamed rule reached the identity request: %+v", rule)
		}
	}
}

// Clause D: the two objects the provider reads on the agent's behalf to find
// out WHERE to send a data-plane call. An agent that references no instance
// still gets them — resolving an endpoint is not access to anything.
func TestIdentityRulesAlwaysCarryTheLookupGrants(t *testing.T) {
	identities := newAgentIdentities(nil)
	rules, err := identities.rulesFor(context.Background(), identityWorkspace(), agentWiredTo(nil, nil))
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 2 {
		t.Fatalf("an agent wired to nothing got %d rules, want only the two lookups: %+v", len(rules), rules)
	}
	binding := ruleFor(rules, APIBindingGVR.Resource)
	if binding == nil || !slices.Equal(binding.ResourceNames, []string{"infrastructure"}) {
		t.Fatalf("apibindings rule = %+v, want a get on the binding named after the provider", binding)
	}
	if !slices.Equal(binding.Verbs, []string{"get"}) {
		t.Errorf("apibindings verbs = %v, want get", binding.Verbs)
	}
	mcp := ruleFor(rules, mcpServerGVR.Resource)
	if mcp == nil || !slices.Equal(mcp.ResourceNames, []string{defaultMCPServer}) {
		t.Fatalf("mcpservers rule = %+v, want a use on the default server", mcp)
	}
	if !slices.Equal(mcp.Verbs, []string{"use"}) {
		t.Errorf("mcpservers verbs = %v, want use", mcp.Verbs)
	}
}

// A connection with no instance contributes nothing: it reaches a public
// endpoint with its own credential, and an identity rule for it would be a
// grant with no subject.
func TestConnectionsWithoutAnInstanceGrantNothing(t *testing.T) {
	identities := newAgentIdentities(nil)
	rules, err := identities.rulesFor(context.Background(), identityWorkspace(), agentWiredTo([]string{"github"}, nil))
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 2 {
		t.Fatalf("a github connection produced instance rules: %+v", rules)
	}
}

// A reference that cannot be read is not evidence of access. It contributes
// nothing and does NOT fail the request: taking away an agent's whole identity
// — and with it every instance-backed tool — because one toolset was briefly
// unreadable would turn a blip into an outage.
func TestAnUnreadableReferenceIsSkippedNotFatal(t *testing.T) {
	identities := newAgentIdentities(nil)
	rules, err := identities.rulesFor(context.Background(), identityWorkspace(),
		agentWiredTo([]string{"browser", "does-not-exist"}, []string{"missing-toolset"}))
	if err != nil {
		t.Fatalf("an unreadable reference must not fail the rules: %v", err)
	}
	if browsers := ruleFor(rules, "browsers"); browsers == nil {
		t.Fatal("the readable connection's grant was lost with the unreadable one")
	}
}

// The fingerprint is what makes a changed grant actually change: without it the
// old token source goes on refreshing the old rules, and the hub goes on
// honouring them, because a refresh is idempotent on the owner tuple and says
// nothing about what the agent references today.
func TestRemovingAConnectionChangesTheFingerprint(t *testing.T) {
	identities := newAgentIdentities(nil)
	ctx := context.Background()
	dyn := identityWorkspace()

	with, err := identities.rulesFor(ctx, dyn, agentWiredTo([]string{"browser"}, nil))
	if err != nil {
		t.Fatal(err)
	}
	without, err := identities.rulesFor(ctx, dyn, agentWiredTo(nil, nil))
	if err != nil {
		t.Fatal(err)
	}
	if fingerprint(with) == fingerprint(without) {
		t.Fatal("dropping a connection left the rules looking unchanged")
	}
	// And the same wiring fingerprints the same, or every call would rebuild
	// the source and re-mint a token it already had.
	again, err := identities.rulesFor(ctx, dyn, agentWiredTo([]string{"browser"}, nil))
	if err != nil {
		t.Fatal(err)
	}
	if fingerprint(with) != fingerprint(again) {
		t.Fatal("identical wiring produced a different fingerprint")
	}
}

// The instance API group is the one thing still hardcoded, and it is a contract
// between the two providers' APIs rather than a routing detail. The provider
// NAME is derived from it and confirmed against the tenant's own binding.
func TestIdentityRulesUseTheDerivedProviderName(t *testing.T) {
	if got := ProviderNameForAPIGroup(tools.InstanceAPIGroup); got != "infrastructure" {
		t.Fatalf("provider name = %q", got)
	}
	if !strings.HasSuffix(tools.InstanceAPIGroup, ".railgrid.ai") {
		t.Fatalf("instance API group %q is not a railgrid group", tools.InstanceAPIGroup)
	}
}
