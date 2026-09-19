// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package api

// Per-agent identity for unattended runs.
//
// An interactive run acts as the human driving it: their bearer reaches the
// platform data plane, which authorizes by re-reading the target instance as
// them. A scheduled, heartbeat, wakeup or inbound-channel run has no human, so
// it needs an identity of its own.
//
// It ASKS THE HUB for one. That is the whole change from what was here before,
// and it is not cosmetic:
//
//	Before                                  Now
//	------                                  ---
//	The provider wrote a ServiceAccount,    The hub mints it, against a policy
//	a ClusterRole, a binding and a legacy   that checks every rule, records
//	token Secret into the tenant's          what it issued, and collects it
//	workspace with its own claimed          when the owning object goes away.
//	credentials.
//
//	The ClusterRole granted get+list on     The rules name the exact Instances
//	EVERY resource in                       this agent's Connections and
//	infrastructure.railgrid.ai — a          Toolsets reference, by name.
//	standing key to every instance in
//	the workspace, including browser
//	instances holding live logins the
//	agent was never wired to.
//
//	The token never expired. Revoking      The token is TTL'd and re-minted
//	meant deleting the ServiceAccount.      at 80% of its life. Releasing the
//	                                        identity revokes it now.
//
//	Rules were create-if-absent, so a       Every refresh re-states the rules,
//	grant never shrank when an agent's      so removing a Connection removes
//	Connections changed.                    the access it carried.
//
// The provider needs NO serviceaccounts, clusterroles or clusterrolebindings
// permission claims for any of this — see provider-sdk/identityclient.

import (
	"context"
	"fmt"
	"log"
	"slices"
	"strings"
	"sync"

	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/dynamic"

	"github.com/railgrid/provider-sdk/identityclient"

	agentsv1alpha1 "github.com/railgrid/provider-agents/apis/v1alpha1"
	agentsclient "github.com/railgrid/provider-agents/client"
	"github.com/railgrid/provider-agents/tools"
)

// instanceVerbs are the data-plane verbs an agent may be granted on an
// Instance it reaches. They are the coordinates the infrastructure provider
// declares in its own CatalogEntry; the hub's policy (clause C) will only mint
// a capability for a verb it can verify exists there.
var instanceVerbs = []string{"exec", "proxy"}

// agentIdentities keeps one refreshing token source per (cluster, agent).
//
// A source is not a cache of a token — it is the identity itself, and it
// refreshes lazily on use. That matters at both ends: an agent that stops
// running stops refreshing and its identity lapses on its own, and an agent
// whose references change has its source rebuilt so the next token carries the
// new rules rather than the old ones.
type agentIdentities struct {
	client *identityclient.Client

	mu      sync.Mutex
	sources map[string]*identitySource
}

// identitySource is one agent's source plus the fingerprint of the rules it
// was built from, so a change is detectable without re-minting to find out.
type identitySource struct {
	source      *identityclient.TokenSource
	fingerprint string
}

func newAgentIdentities(client *identityclient.Client) *agentIdentities {
	return &agentIdentities{client: client, sources: map[string]*identitySource{}}
}

func identityKey(cluster, agent string) string { return cluster + "/" + agent }

// token returns a valid token for the agent, minting or refreshing as needed.
//
// Empty on any failure, and deliberately: the caller degrades to a run without
// instance-backed tools, each of which reports precisely why, rather than the
// whole run failing because one tool family could not be authorized.
func (a *agentIdentities) token(ctx context.Context, dyn dynamic.Interface, cluster string, agent *agentsv1alpha1.Agent) string {
	if a == nil || a.client == nil || agent == nil {
		return ""
	}
	rules, err := a.rulesFor(ctx, dyn, agent)
	if err != nil {
		log.Printf("agents: agent %q identity unavailable, instance-backed tools disabled for this run: %v", agent.Name, err)
		return ""
	}
	source := a.sourceFor(cluster, agent, rules)

	token, err := source.Token(ctx)
	if err != nil {
		log.Printf("agents: agent %q identity unavailable, instance-backed tools disabled for this run: %v", agent.Name, err)
		return ""
	}
	return token.Token
}

// sourceFor returns the agent's token source, rebuilding it when the rules it
// was created with no longer match.
//
// Rebuilding on a change is what makes a removed Connection actually remove
// access: the old source would go on refreshing the old rules, and the hub
// would go on honouring them, because a refresh is idempotent on the owner
// tuple and says nothing about what the agent references today.
//
// The check is on every use rather than driven off the Agent reconciler's
// watches, and that is the stronger place for it: a watch tells you an object
// changed, this tells you whether what the agent may REACH changed, which is
// the question that matters and is true of toolset and connection edits the
// reconciler would have to fan out to find.
func (a *agentIdentities) sourceFor(cluster string, agent *agentsv1alpha1.Agent, rules []rbacv1.PolicyRule) *identityclient.TokenSource {
	key := identityKey(cluster, agent.Name)
	print := fingerprint(rules)

	a.mu.Lock()
	defer a.mu.Unlock()
	if existing, ok := a.sources[key]; ok && existing.fingerprint == print {
		return existing.source
	}
	source := identityclient.NewTokenSource(a.client, identityclient.Request{
		Owner: identityclient.Owner{
			Kind:      "Agent",
			Group:     agentsv1alpha1.SchemeGroupVersion.Group,
			Version:   agentsv1alpha1.SchemeGroupVersion.Version,
			Resource:  "agents",
			Name:      agent.Name,
			UID:       string(agent.UID),
			ClusterID: cluster,
		},
		ClusterID: cluster,
		Rules:     rules,
	})
	a.sources[key] = &identitySource{source: source, fingerprint: print}
	return source
}

// release drops the agent's identity, at the hub and here. Called from the
// Agent reconciler's delete path: the hub's own sweep would collect it anyway,
// but only after up to one token TTL, and revocation should not wait.
func (a *agentIdentities) release(ctx context.Context, cluster, agent string) error {
	if a == nil || a.client == nil {
		return nil
	}
	a.mu.Lock()
	delete(a.sources, identityKey(cluster, agent))
	a.mu.Unlock()

	return a.client.Release(ctx, cluster, identityclient.Owner{
		Kind:      "Agent",
		Group:     agentsv1alpha1.SchemeGroupVersion.Group,
		Version:   agentsv1alpha1.SchemeGroupVersion.Version,
		Resource:  "agents",
		Name:      agent,
		ClusterID: cluster,
	})
}

// rulesFor builds the exact rules this agent needs, and nothing else.
//
// Three clauses, each the narrowest the hub's policy allows:
//
//   - B: `get` on the named Instances the agent's Connections and Toolsets
//     reference. Named, because "every instance in the workspace" was the old
//     grant and it was the thing worth fixing.
//   - C: `create` on the declared {resource}/{verb} subresources of those same
//     Instances, which is how the data plane expresses "may run exec/proxy on
//     this one".
//   - D: `get` on this workspace's APIBinding for the instance API group, and
//     `use` on its default MCPServer — the two objects the provider reads on
//     the agent's behalf to find out WHERE to send a data-plane call and which
//     aggregate endpoint to dial. Both by name.
//
// An agent that references no instances still gets clause D, because resolving
// the endpoint is not itself access to anything.
func (a *agentIdentities) rulesFor(ctx context.Context, dyn dynamic.Interface, agent *agentsv1alpha1.Agent) ([]rbacv1.PolicyRule, error) {
	instances, err := agentInstances(ctx, dyn, agent)
	if err != nil {
		return nil, err
	}
	rules := []rbacv1.PolicyRule{{
		// Clause D. The binding is named after the provider that serves the
		// group (see crossprovider.go), and the MCPServer is the conventional
		// default one.
		APIGroups:     []string{APIBindingGVR.Group},
		Resources:     []string{APIBindingGVR.Resource},
		ResourceNames: []string{ProviderNameForAPIGroup(tools.InstanceAPIGroup)},
		Verbs:         []string{"get"},
	}, {
		APIGroups:     []string{mcpServerGVR.Group},
		Resources:     []string{mcpServerGVR.Resource},
		ResourceNames: []string{defaultMCPServer},
		Verbs:         []string{"use"},
	}}

	for _, resource := range sortedNames(instances) {
		names := instances[resource]
		if len(names) == 0 {
			continue
		}
		// Clause B: see the instance.
		rules = append(rules, rbacv1.PolicyRule{
			APIGroups:     []string{tools.InstanceAPIGroup},
			Resources:     []string{resource},
			ResourceNames: names,
			Verbs:         []string{"get"},
		})
		// Clause C: act on it, one rule per verb, because the subresource is
		// the coordinate the grant is expressed on.
		for _, verb := range instanceVerbs {
			rules = append(rules, rbacv1.PolicyRule{
				APIGroups:     []string{tools.InstanceAPIGroup},
				Resources:     []string{resource + "/" + verb},
				ResourceNames: names,
				Verbs:         []string{"create"},
			})
		}
	}
	return rules, nil
}

// agentInstances collects the Instances an agent can reach, as
// resource → sorted names, from every Connection it is wired to directly and
// through its Toolsets.
//
// A Connection that names no instance contributes nothing: it reaches a public
// endpoint with its own credential and needs no platform identity at all.
func agentInstances(ctx context.Context, dyn dynamic.Interface, agent *agentsv1alpha1.Agent) (map[string][]string, error) {
	names := map[string]map[string]bool{}
	add := func(resource, instance string) {
		resource, instance = strings.TrimSpace(resource), strings.TrimSpace(instance)
		if resource == "" || instance == "" {
			return
		}
		if names[resource] == nil {
			names[resource] = map[string]bool{}
		}
		names[resource][instance] = true
	}

	connections := map[string]bool{}
	for _, grant := range []agentsv1alpha1.ToolGrant{agent.Spec.Tools.Interactive, agent.Spec.Tools.Background} {
		for _, name := range grant.Connections {
			connections[strings.TrimSpace(name)] = true
		}
		for _, toolsetName := range grant.Toolsets {
			toolset, err := readToolset(ctx, dyn, strings.TrimSpace(toolsetName))
			if err != nil {
				// A toolset that cannot be read is not evidence of access, so
				// it contributes nothing. Failing here would take away an
				// agent's whole identity over one unreadable reference.
				log.Printf("agents: agent %q references toolset %q, which could not be read: %v", agent.Name, toolsetName, err)
				continue
			}
			for _, name := range toolset.Spec.Connections {
				connections[strings.TrimSpace(name)] = true
			}
		}
	}

	for name := range connections {
		if name == "" {
			continue
		}
		connection, err := readConnection(ctx, dyn, name)
		if err != nil {
			log.Printf("agents: agent %q references connection %q, which could not be read: %v", agent.Name, name, err)
			continue
		}
		instance := strings.TrimSpace(connection.Spec.Config["instance"])
		resource := strings.TrimSpace(connection.Spec.Config["instanceResource"])
		if resource == "" {
			resource = defaultInstanceResource
		}
		add(resource, instance)
	}

	out := make(map[string][]string, len(names))
	for resource, set := range names {
		out[resource] = sortedNames(set)
	}
	return out, nil
}

// defaultInstanceResource is the resource a Connection's instance lives under
// when it names none. Mirrors tools/mcp.go and tools/web.go.
const defaultInstanceResource = "instances"

func readToolset(ctx context.Context, dyn dynamic.Interface, name string) (*agentsv1alpha1.Toolset, error) {
	if name == "" {
		return nil, fmt.Errorf("empty toolset name")
	}
	u, err := dyn.Resource(agentsclient.ToolsetGVR).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	return fromU[agentsv1alpha1.Toolset](u)
}

func readConnection(ctx context.Context, dyn dynamic.Interface, name string) (*agentsv1alpha1.Connection, error) {
	u, err := dyn.Resource(agentsclient.ConnectionGVR).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	return fromU[agentsv1alpha1.Connection](u)
}

// fingerprint renders a rule set so two of them can be compared cheaply. It is
// not a hash: the rules are few and short, and a readable fingerprint makes a
// rebuild explicable in a log line.
func fingerprint(rules []rbacv1.PolicyRule) string {
	parts := make([]string, 0, len(rules))
	for _, rule := range rules {
		parts = append(parts, strings.Join(rule.APIGroups, ",")+"|"+
			strings.Join(rule.Resources, ",")+"|"+
			strings.Join(rule.ResourceNames, ",")+"|"+
			strings.Join(rule.Verbs, ","))
	}
	return strings.Join(parts, ";")
}

// sortedNames returns a map's keys in a stable order, so a fingerprint does
// not change with Go's map iteration.
func sortedNames[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}
