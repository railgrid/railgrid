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

package agent

import (
	"os"
	"strings"
	"testing"

	"github.com/railgrid/railgrid/pkg/agent/harnessplane"
)

// TestNewAcceptsTheDefaultHarnessOnEveryEdgeType: mode auto is the DEFAULT, so a
// plain `agent run` must never be refused because of it — including on a
// Kubernetes edge, which simply supervises nothing.
func TestNewAcceptsTheDefaultHarnessOnEveryEdgeType(t *testing.T) {
	for _, edgeType := range []AgentType{AgentTypeKubernetes, AgentTypeServer, AgentTypeMacOS} {
		opts := NewOptions()
		opts.EdgeName = "build-01"
		opts.HubURL = "https://hub.example"
		opts.Type = edgeType
		opts.Harness = string(harnessplane.ModeAuto)
		// The harness flag must never be the reason. Anything else a bare New
		// needs on this host — a Kubernetes edge builds a downstream client, so
		// it wants a kubeconfig — is not what this test is about, and requiring
		// it would make the assertion depend on where the test runs.
		if _, err := New(opts); err != nil && strings.Contains(err.Error(), "harness") {
			t.Errorf("New(--type %s, --harness auto) = %v, want the harness accepted", edgeType, err)
		}
	}
}

// TestNewRejectsAnUnknownHarness keeps the flag's validation attached to startup
// rather than to the first reconcile: a typo that normalized to "nothing" would
// leave an operator waiting for a runner that is never coming.
func TestNewRejectsAnUnknownHarness(t *testing.T) {
	opts := NewOptions()
	opts.EdgeName = "build-01"
	opts.HubURL = "https://hub.example"
	opts.Type = AgentTypeServer
	opts.Harness = "definitely-not-a-harness"

	if _, err := New(opts); err == nil || !strings.Contains(err.Error(), "--harness") {
		t.Fatalf("New err = %v, want a refusal naming --harness", err)
	}
}

// TestNewRejectsNamedHarnessesOnAKubernetesEdge: a Kubernetes edge has no host to
// supervise a process on, so naming harnesses for one can only produce something
// that never runs.
func TestNewRejectsNamedHarnessesOnAKubernetesEdge(t *testing.T) {
	opts := NewOptions()
	opts.EdgeName = "prod"
	opts.HubURL = "https://hub.example"
	opts.Type = AgentTypeKubernetes
	opts.Harness = "claude"

	if _, err := New(opts); err == nil || !strings.Contains(err.Error(), "host edges") {
		t.Fatalf("New err = %v, want a refusal naming host edges", err)
	}
}

// TestResolveRunnerAccountRefusesAForeignAccountForANonRootAgent: a non-root
// agent cannot become another user, so claiming one would produce a child that
// fails to start with a confusing EPERM instead of a clear message.
func TestResolveRunnerAccountRefusesAForeignAccountForANonRootAgent(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("this case only applies to a non-root agent")
	}
	a := &Agent{opts: &Options{EdgeName: "build-01", RunnerUser: "definitely-not-this-user"}}
	if _, err := a.resolveRunnerAccount(); err == nil {
		t.Fatal("a non-root agent accepted a foreign --runner-user")
	}

	// Its own account is fine, and yields an "inherit" credential — which is how
	// the macOS LaunchDaemon worker runs its harnesses.
	self := &Agent{opts: &Options{EdgeName: "build-01"}}
	account, err := self.resolveRunnerAccount()
	if err != nil {
		t.Fatalf("resolving the agent's own account: %v", err)
	}
	if !account.Inherited() {
		t.Errorf("a non-root agent should inherit its own credential, got uid %d", account.UID)
	}
	if account.Home == "" {
		t.Error("no home resolved for the runner account")
	}
}

// TestEnsureRunnerAccountDoesNotInventANamedAccount: creating an account the
// operator did not ask for would hide a --runner-user typo behind a new system
// user. Only the default name is ever created.
func TestEnsureRunnerAccountDoesNotInventANamedAccount(t *testing.T) {
	if _, err := EnsureRunnerAccount("definitely-not-a-local-account"); err == nil {
		t.Fatal("a nonexistent named account was accepted (or created)")
	}
}
