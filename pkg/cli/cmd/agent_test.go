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

// TestJoinServerUnitCarriesAddonFlags: --allow-addon / --addon-user are the
// machine owner's half of the add-on trust model. If the installer dropped
// them, the installed agent would run with an empty allow list and every Addon
// for this edge would sit Blocked while the operator believed they opted in.
func TestJoinServerUnitCarriesAddonFlags(t *testing.T) {
	opts := &agent.Options{
		EdgeName:      "edge-1",
		Type:          agent.AgentTypeServer,
		HubURL:        "https://hub.example",
		Token:         "join-token",
		AllowedAddons: []string{"runner"},
		AddonUser:     "railgrid-runner",
	}
	data, err := joinServerUnitData(opts, "/usr/local/bin/railgrid", "")
	if err != nil {
		t.Fatal(err)
	}
	unit := renderUnit(t, data)
	for _, want := range []string{"--allow-addon runner", "--addon-user railgrid-runner"} {
		if !strings.Contains(unit, want) {
			t.Errorf("rendered unit lacks %q:\n%s", want, unit)
		}
	}
}

// TestJoinServerUnitOmitsAddonFlagsWhenUnset: an install that never mentioned
// add-ons must produce a unit that runs none. A stray --addon-user alone is not
// an opt-in and must not appear either.
func TestJoinServerUnitOmitsAddonFlagsWhenUnset(t *testing.T) {
	for name, opts := range map[string]*agent.Options{
		"nothing set": {
			EdgeName: "edge-1", Type: agent.AgentTypeServer, HubURL: "https://hub.example", Token: "t",
		},
		"user without an allow list": {
			EdgeName: "edge-1", Type: agent.AgentTypeServer, HubURL: "https://hub.example", Token: "t",
			AddonUser: "railgrid-runner",
		},
	} {
		data, err := joinServerUnitData(opts, "/usr/local/bin/railgrid", "")
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		unit := renderUnit(t, data)
		if strings.Contains(unit, "--allow-addon") || strings.Contains(unit, "--addon-user") {
			t.Errorf("%s: unit carries add-on flags:\n%s", name, unit)
		}
	}
}

// TestAddonInstallRequiresAnAccount: the systemd unit runs the agent as ROOT
// (there is no User=), so allowing an add-on without naming a non-root account
// would install a unit that refuses to start. Fail at install time, where the
// operator is still watching, with a message that names the missing flag.
func TestAddonInstallRequiresAnAccount(t *testing.T) {
	opts := &agent.Options{
		EdgeName: "edge-1", Type: agent.AgentTypeServer, HubURL: "https://hub.example", Token: "t",
		AllowedAddons: []string{"runner"},
	}
	_, err := joinServerUnitData(opts, "/usr/local/bin/railgrid", "")
	if err == nil {
		t.Fatal("--allow-addon without --addon-user was accepted for a root systemd install")
	}
	if !strings.Contains(err.Error(), "--addon-user") {
		t.Errorf("error does not name the missing flag: %v", err)
	}

	if _, err := validateAddonInstall([]string{"not-a-real-addon"}, "railgrid-runner"); err == nil {
		t.Error("an unknown add-on type was accepted")
	}
}
