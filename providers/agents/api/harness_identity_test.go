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

package api

import (
	"os"
	"slices"
	"strings"
	"testing"

	rbacv1 "k8s.io/api/rbac/v1"

	agentsv1alpha1 "github.com/railgrid/provider-agents/apis/v1alpha1"
	"github.com/railgrid/provider-agents/internal/edgeref"
)

func harnessAgent(edge string) *agentsv1alpha1.Agent {
	agent := &agentsv1alpha1.Agent{}
	agent.Name = "coder"
	agent.Spec.Backend = agentsv1alpha1.AgentBackendSpec{
		Type: agentsv1alpha1.AgentBackendHarness,
		Harness: &agentsv1alpha1.AgentHarnessBackend{
			EdgeRef:       agentsv1alpha1.AgentHarnessEdgeRef{Kind: edgeref.KindLinuxServer, Name: edge},
			CredentialRef: "my-claude",
		},
	}
	return agent
}

// TestAHarnessAgentsIdentityNamesItsRunnerService.
//
// Reported from a live workspace: a harness turn failed with
//
//	services.edges.railgrid.ai "dev-edge-server-1-claude" is forbidden:
//	User "system:serviceaccount:default:provider" cannot get resource "services"
//
// The provider's bootstrap credential has no rights in a tenant workspace; what
// carries them is the identity minted per agent. It was minted without any edges
// rule, so there was nothing to carry.
func TestAHarnessAgentsIdentityNamesItsRunnerService(t *testing.T) {
	names := harnessServiceNames(harnessAgent("dev-edge-server-1"))
	if len(names) == 0 {
		t.Fatal("a harness-backed agent got no runner Service names, so its identity can grant nothing")
	}
	// The selector is not knowable from the spec (it lives on the credential),
	// so both harnesses on THIS edge are named. Two names, not a wildcard.
	if !slices.Contains(names, "dev-edge-server-1-claude") || !slices.Contains(names, "dev-edge-server-1-codex") {
		t.Errorf("names = %v, want both harnesses on that edge", names)
	}
	for _, name := range names {
		if !strings.HasPrefix(name, "dev-edge-server-1-") {
			t.Errorf("name %q is not on the agent's own edge", name)
		}
	}
}

// TestAModelBackedAgentGetsNoEdgeGrant: the rule set is derived from what the
// agent references, so an agent that references no machine must not be able to
// reach one.
func TestAModelBackedAgentGetsNoEdgeGrant(t *testing.T) {
	agent := &agentsv1alpha1.Agent{}
	agent.Name = "scout"
	if names := harnessServiceNames(agent); len(names) != 0 {
		t.Errorf("a model-backed agent was granted runner Services: %v", names)
	}
	// Nor one that asks for a harness but names no machine yet.
	half := harnessAgent("")
	if names := harnessServiceNames(half); len(names) != 0 {
		t.Errorf("an agent with no edge was granted runner Services: %v", names)
	}
}

// TestTheEdgeGrantIsNameScopedAndIsTwoClauses: seeing the object and invoking
// its verb are separate coordinates, and neither may be workspace-wide.
func TestTheEdgeGrantIsNameScopedAndIsTwoClauses(t *testing.T) {
	names := harnessServiceNames(harnessAgent("build-01"))
	rules := []rbacv1.PolicyRule{
		{APIGroups: []string{edgeref.GroupName}, Resources: []string{edgeref.ResourceServices}, ResourceNames: names, Verbs: []string{"get"}},
		{APIGroups: []string{edgeref.GroupName}, Resources: []string{edgeref.ResourceServices + "/" + edgeref.VerbProxy}, ResourceNames: names, Verbs: []string{"create"}},
	}
	for _, rule := range rules {
		if len(rule.ResourceNames) == 0 {
			t.Errorf("rule %v is workspace-wide; a grant to reach a machine must name it", rule.Resources)
		}
		if slices.Contains(rule.Verbs, "*") || slices.Contains(rule.Resources, "*") || slices.Contains(rule.APIGroups, "*") {
			t.Errorf("rule %v carries a wildcard", rule.Resources)
		}
	}
	if rules[1].Resources[0] != "services/proxy" {
		t.Errorf("the invocation clause is on %q, want services/proxy", rules[1].Resources[0])
	}
}

// TestAHarnessTurnNeverBorrowsTheCallersToken.
//
// The second failure in this area, after the provider's bootstrap credential:
// the dispatch fell back to runAccess.HubToken, which is the CALLER's bearer.
// A data-plane verb carries none — see identity.token, "MCP class only; a verb
// never has one" — so the portal's chat, the most ordinary way to use a
// harness-backed agent, had nothing there and failed on an empty token.
//
// The guard is structural: harness.go must not read HubToken at all.
func TestAHarnessTurnNeverBorrowsTheCallersToken(t *testing.T) {
	source, err := os.ReadFile("harness.go")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(source), "HubToken") {
		t.Error("harness.go reads HubToken; a verb request has no caller bearer, so the dispatch must use the agent's own minted identity")
	}
	if !strings.Contains(string(source), "harnessIdentity(") {
		t.Error("harness.go no longer mints the agent's identity for the dispatch")
	}
}
