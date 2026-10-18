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
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/railgrid/provider-sdk/identityclient"
	"k8s.io/client-go/rest"

	rbacv1 "k8s.io/api/rbac/v1"

	agentsv1alpha1 "github.com/railgrid/provider-agents/apis/v1alpha1"
	backendharness "github.com/railgrid/provider-agents/backend/harness"
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

// One client must keep working after its initially issued credential rotates.
func TestRunnerClientRefreshesCredentialPerRequest(t *testing.T) {
	current := "first"
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if got := r.Header.Get("Authorization"); got != "Bearer "+current {
			t.Errorf("authorization = %q", got)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	var refreshErr error
	source := func(ctx context.Context) (string, error) {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		return current, refreshErr
	}
	s := &Server{providerBase: &rest.Config{Host: server.URL, BearerToken: "bootstrap-must-not-leak"}}
	cfg, err := s.runnerRESTConfig("tenant", source)
	if err != nil {
		t.Fatal(err)
	}
	client, err := rest.HTTPClientFor(cfg)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer caller-must-not-leak")
	for _, token := range []string{"first", "renewed"} {
		current = token
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		if err := resp.Body.Close(); err != nil {
			t.Fatal(err)
		}
		if req.Header.Get("Authorization") != "Bearer caller-must-not-leak" {
			t.Fatal("mutated caller request")
		}
	}
	refreshErr = errors.New("identity renewal refused")
	if _, err := client.Do(req); !errors.Is(err, refreshErr) {
		t.Fatalf("renewal error = %v", err)
	}
	refreshErr = nil
	current = ""
	if _, err := client.Do(req); err == nil {
		t.Fatal("empty identity sent")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := client.Do(req.WithContext(ctx)); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation = %v", err)
	}
	if calls.Load() != 2 {
		t.Fatalf("sent %d requests; failed renewals must not reach server", calls.Load())
	}
}

func TestHarnessIdentityRenewsThroughExistingRunnerClient(t *testing.T) {
	var mints atomic.Int32
	var refuse atomic.Bool
	issuer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if refuse.Load() {
			http.Error(w, "issuer unavailable", http.StatusServiceUnavailable)
			return
		}
		mint := mints.Add(1)
		token := "initial"
		if mint > 1 {
			token = "renewed"
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(identityclient.Token{Token: token, ExpiresAt: time.Now().Add(time.Hour)})
	}))
	defer issuer.Close()
	issuerClient, err := identityclient.New(identityclient.Options{HubURL: issuer.URL, Provider: providerName, Token: "bootstrap", HTTPClient: issuer.Client()})
	if err != nil {
		t.Fatal(err)
	}
	agent := harnessIdentityAgent("renewal-test")
	agent.Spec.Tools.Interactive.Toolsets = []string{"test-toolset"}
	agent.Spec.Tools.Interactive.Connections = []string{"test-connection"}
	f := newParkedCancelFixture(t, agent, nil)
	f.server.bg.identities = newAgentIdentities(issuerClient)
	source, err := f.server.harnessIdentity(context.Background(), f.identity.clusterID, agent)
	if err != nil {
		t.Fatal(err)
	}
	reads := len(f.dynamic.Actions())
	if reads == 0 {
		t.Fatal("expected permission discovery reads")
	}
	expected := "initial"
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+expected {
			t.Errorf("received stale credential")
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer endpoint.Close()
	f.server.providerBase = &rest.Config{Host: endpoint.URL, BearerToken: "bootstrap"}
	cfg, err := f.server.runnerRESTConfig(f.identity.clusterID, source)
	if err != nil {
		t.Fatal(err)
	}
	client, err := rest.HTTPClientFor(cfg)
	if err != nil {
		t.Fatal(err)
	}
	check := func() {
		t.Helper()
		resp, err := client.Get(endpoint.URL)
		if err != nil {
			t.Fatal(err)
		}
		if err := resp.Body.Close(); err != nil {
			t.Fatal(err)
		}
	}
	check()
	// Force the same cache transition as expiry without a wall-clock sleep.
	f.server.bg.identities.sources[identityKey(f.identity.clusterID, agent.Name)].source.Invalidate()
	expected = "renewed"
	check()
	if mints.Load() != 2 {
		t.Fatalf("mints = %d, want 2", mints.Load())
	}
	if got := len(f.dynamic.Actions()); got != reads {
		t.Fatalf("workspace reads grew from %d to %d during runner requests", reads, got)
	}
	refuse.Store(true)
	f.server.bg.identities.sources[identityKey(f.identity.clusterID, agent.Name)].source.Invalidate()
	if _, err := source(context.Background()); err == nil || !strings.Contains(err.Error(), "503") || !strings.Contains(err.Error(), "renewing the agent's runner identity") {
		t.Fatalf("lost issuer failure: %v", err)
	}

}

type runnerCloseBody struct {
	io.Reader
	closed bool
}

func (b *runnerCloseBody) Close() error { b.closed = true; return nil }

func TestRunnerIdentityFailureClosesRequestBody(t *testing.T) {
	for _, failure := range []bool{true, false} {
		body := &runnerCloseBody{Reader: strings.NewReader("request")}
		req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, "http://runner", body)
		if err != nil {
			t.Fatal(err)
		}
		transport := runnerIdentityTransport{token: func(context.Context) (string, error) {
			if failure {
				return "", errors.New("renewal refused")
			}
			return "", nil
		}}
		// A nil base also proves that authentication failure never dispatches.
		if _, err := transport.RoundTrip(req); err == nil {
			t.Fatal("expected authentication failure")
		}
		if !body.closed {
			t.Fatal("request body was not closed")
		}
	}
}

func TestRepositoryEnvironmentIdentityIsTenantAndAgentScoped(t *testing.T) {
	repo := backendharness.Repository{RepositoryID: "repo", EnvironmentID: "ticket"}
	first := scopedRepositoryEnvironment(repo, "cluster-a", "agent-a").EnvironmentID
	if first == "ticket" || first == "" {
		t.Fatal("environment identity not scoped")
	}
	if scopedRepositoryEnvironment(repo, "cluster-b", "agent-a").EnvironmentID == first {
		t.Fatal("cross-tenant reuse")
	}
	if scopedRepositoryEnvironment(repo, "cluster-a", "agent-b").EnvironmentID == first {
		t.Fatal("cross-agent reuse")
	}
	if repo.EnvironmentID != "ticket" {
		t.Fatal("mutated persisted coordinator identity")
	}
}
