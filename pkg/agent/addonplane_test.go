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
	"slices"
	"strings"
	"testing"
)

// TestNormalizeAllowedAddons: an opt-in that silently normalizes to nothing is
// the worst failure mode there is — the operator believes the machine accepts
// an add-on and every Addon sits Blocked. A typo is therefore an error, not an
// empty list.
func TestNormalizeAllowedAddons(t *testing.T) {
	got, err := NormalizeAllowedAddons([]string{" runner ", "runner,runner", ""})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, []string{"runner"}) {
		t.Errorf("normalized = %v, want [runner]", got)
	}

	if got, err := NormalizeAllowedAddons(nil); err != nil || len(got) != 0 {
		t.Errorf("empty input = %v, %v; want an empty list and no error", got, err)
	}

	_, err = NormalizeAllowedAddons([]string{"runner", "shell"})
	if err == nil {
		t.Fatal("an unknown add-on type was accepted")
	}
	if !strings.Contains(err.Error(), "shell") || !strings.Contains(err.Error(), "runner") {
		t.Errorf("error should name the bad value and the known set: %v", err)
	}
}

// TestNewRejectsAddonsOnKubernetesEdges: the add-on plane materializes host
// processes. A KubernetesCluster agent has no host to run them on, so allowing
// one there can only produce an Addon that never becomes Running.
func TestNewRejectsAddonsOnKubernetesEdges(t *testing.T) {
	opts := NewOptions()
	opts.EdgeName = "prod"
	opts.HubURL = "https://hub.example"
	opts.Type = AgentTypeKubernetes
	opts.AllowedAddons = []string{"runner"}

	if _, err := New(opts); err == nil || !strings.Contains(err.Error(), "host edges") {
		t.Fatalf("New err = %v, want a refusal naming host edges", err)
	}
}

// TestNewRejectsAnUnknownAddonType keeps the flag's validation attached to
// startup rather than to the first Addon.
func TestNewRejectsAnUnknownAddonType(t *testing.T) {
	opts := NewOptions()
	opts.EdgeName = "build-01"
	opts.HubURL = "https://hub.example"
	opts.Type = AgentTypeServer
	opts.AllowedAddons = []string{"definitely-not-an-addon"}

	if _, err := New(opts); err == nil || !strings.Contains(err.Error(), "--allow-addon") {
		t.Fatalf("New err = %v, want a refusal naming --allow-addon", err)
	}
}

// TestResolveAddonAccountRefusesAForeignAccountForANonRootAgent: a non-root
// agent cannot become another user, so claiming one would produce a child that
// fails to start with a confusing EPERM instead of a clear message.
func TestResolveAddonAccountRefusesAForeignAccountForANonRootAgent(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("this case only applies to a non-root agent")
	}
	a := &Agent{opts: &Options{EdgeName: "build-01", AddonUser: "definitely-not-this-user"}}
	if _, err := a.resolveAddonAccount(); err == nil {
		t.Fatal("a non-root agent accepted a foreign --addon-user")
	}

	// Its own account is fine, and yields an "inherit" credential.
	self := &Agent{opts: &Options{EdgeName: "build-01"}}
	account, err := self.resolveAddonAccount()
	if err != nil {
		t.Fatalf("resolving the agent's own account: %v", err)
	}
	if !account.Inherited() {
		t.Errorf("a non-root agent should inherit its own credential, got uid %d", account.UID)
	}
	if account.Home == "" {
		t.Error("no home resolved for the add-on account")
	}
}
