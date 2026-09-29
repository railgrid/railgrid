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

package cmd

import (
	"strings"
	"testing"
	"text/template"

	"github.com/railgrid/railgrid/pkg/agent"
	"github.com/railgrid/railgrid/pkg/agent/tunnel"
)

func renderUnit(t *testing.T, data systemdUnitData) string {
	t.Helper()
	tmpl, err := template.New("unit").Parse(systemdUnitTemplate)
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	if err := tmpl.Execute(&b, data); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

// TestJoinServerUnitCarriesSvcPolicyFlags: `railgrid agent join --type server`
// accepts --svc-allow-cidr / --svc-policy, so the unit it installs must run
// the agent with them. Dropping them silently downgrades an operator's
// "enforce" to the built-in default with no allow list.
func TestJoinServerUnitCarriesSvcPolicyFlags(t *testing.T) {
	opts := &agent.Options{
		EdgeName:        "edge-1",
		Type:            agent.AgentTypeServer,
		HubURL:          "https://hub.example",
		Token:           "join-token",
		SvcAllowedCIDRs: []string{"192.168.1.0/24", "10.0.0.0/8"},
		SvcPolicy:       string(tunnel.SvcPolicyEnforce),
	}
	data, err := joinServerUnitData(opts, "/usr/local/bin/railgrid", "")
	if err != nil {
		t.Fatal(err)
	}
	unit := renderUnit(t, data)
	for _, want := range []string{
		"agent run",
		"--svc-allow-cidr 192.168.1.0/24",
		"--svc-allow-cidr 10.0.0.0/8",
		"--svc-policy enforce",
	} {
		if !strings.Contains(unit, want) {
			t.Errorf("rendered unit lacks %q:\n%s", want, unit)
		}
	}
}

// TestJoinServerUnitOmitsDefaultPolicy: like `agent install`, the policy is
// only rendered when it differs from the built-in default, so installs pick
// up the next release's default flip without a reinstall.
func TestJoinServerUnitOmitsDefaultPolicy(t *testing.T) {
	opts := &agent.Options{
		EdgeName:  "edge-1",
		Type:      agent.AgentTypeServer,
		HubURL:    "https://hub.example",
		Token:     "join-token",
		SvcPolicy: string(tunnel.DefaultSvcPolicy),
	}
	data, err := joinServerUnitData(opts, "/usr/local/bin/railgrid", "")
	if err != nil {
		t.Fatal(err)
	}
	if unit := renderUnit(t, data); strings.Contains(unit, "--svc-policy") {
		t.Errorf("default policy must not be pinned into the unit:\n%s", unit)
	}
}

// TestJoinServerUnitRejectsBadPolicyAndCIDR: invalid values fail the join
// rather than being written into a unit that then fails to start.
func TestJoinServerUnitRejectsBadPolicyAndCIDR(t *testing.T) {
	base := agent.Options{EdgeName: "edge-1", Type: agent.AgentTypeServer, HubURL: "https://hub.example", Token: "t"}

	bad := base
	bad.SvcPolicy = "sometimes"
	if _, err := joinServerUnitData(&bad, "/bin/railgrid", ""); err == nil {
		t.Error("invalid --svc-policy was accepted")
	}

	bad = base
	bad.SvcAllowedCIDRs = []string{"not-a-cidr"}
	if _, err := joinServerUnitData(&bad, "/bin/railgrid", ""); err == nil {
		t.Error("invalid --svc-allow-cidr was accepted")
	}
}

// TestJoinServerUnitCarriesTheHarnessSelection: --harness is what SEEDS the
// machine's harness offering. If the installer dropped it, an operator who
// installed with --harness none would get a unit that seeds auto instead — the
// exact opposite of what they asked for.
func TestJoinServerUnitCarriesTheHarnessSelection(t *testing.T) {
	opts := &agent.Options{
		EdgeName: "edge-1",
		Type:     agent.AgentTypeServer,
		HubURL:   "https://hub.example",
		Token:    "join-token",
		Harness:  "none",
	}
	data, err := joinServerUnitData(opts, "/usr/local/bin/railgrid", "")
	if err != nil {
		t.Fatal(err)
	}
	if unit := renderUnit(t, data); !strings.Contains(unit, "--harness none") {
		t.Errorf("rendered unit lacks --harness none:\n%s", unit)
	}

	opts.Harness = "claude,codex"
	data, err = joinServerUnitData(opts, "/usr/local/bin/railgrid", "")
	if err != nil {
		t.Fatal(err)
	}
	if unit := renderUnit(t, data); !strings.Contains(unit, "--harness claude,codex") {
		t.Errorf("rendered unit lacks the explicit harness list:\n%s", unit)
	}
}

// TestJoinServerUnitOmitsTheDefaultHarness: an install that said nothing about
// harnesses should pick up a later change to the default without a reinstall, so
// "auto" is not written into the unit.
func TestJoinServerUnitOmitsTheDefaultHarness(t *testing.T) {
	for name, harness := range map[string]string{"unset": "", "explicitly auto": "auto"} {
		opts := &agent.Options{
			EdgeName: "edge-1", Type: agent.AgentTypeServer, HubURL: "https://hub.example", Token: "t",
			Harness: harness,
		}
		data, err := joinServerUnitData(opts, "/usr/local/bin/railgrid", "")
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if unit := renderUnit(t, data); strings.Contains(unit, "--harness") {
			t.Errorf("%s: unit pins the default harness selection:\n%s", name, unit)
		}
	}
}

// TestHarnessInstallRefusesATypo: an install is the last moment an operator is
// watching, so a harness name that would silently offer nothing fails here.
func TestHarnessInstallRefusesATypo(t *testing.T) {
	opts := &agent.Options{
		EdgeName: "edge-1", Type: agent.AgentTypeServer, HubURL: "https://hub.example", Token: "t",
		Harness: "cluade",
	}
	if _, err := joinServerUnitData(opts, "/usr/local/bin/railgrid", ""); err == nil {
		t.Fatal("a misspelled --harness was accepted")
	}
	if _, err := validateHarnessInstall("cluade"); err == nil {
		t.Error("validateHarnessInstall accepted an unknown harness")
	}
	if got, err := validateHarnessInstall("auto"); err != nil || got != "" {
		t.Errorf("validateHarnessInstall(auto) = %q, %v; want the default to render nothing", got, err)
	}
}
