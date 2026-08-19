/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package providers

import (
	"strings"
	"testing"
)

type fakeIdentities map[string]string

func (f fakeIdentities) ResolveIdentityHash(_ string, exportName string) string { return f[exportName] }

func baseSelfHosting() *SelfHosting {
	return &SelfHosting{
		Supported:    true,
		ChartRepo:    "oci://ghcr.io/faroshq/charts",
		ChartName:    "faros-quickstart-provider",
		ChartVersion: "0.1.4",
	}
}

func baseOptions() InstallOptions {
	return InstallOptions{
		ProviderName:  "quickstart",
		WorkspacePath: "root:faros:tenants:org1:providers:quickstart",
		HubURL:        "https://hub.example.com",
	}
}

func TestRenderInstallInstructions(t *testing.T) {
	got := RenderInstallInstructions(baseSelfHosting(), baseOptions())

	if got.Namespace != "faros-provider-quickstart" {
		t.Errorf("Namespace = %q, want the defaulted faros-provider-<name>", got.Namespace)
	}
	if got.ReleaseName != "quickstart" {
		t.Errorf("ReleaseName = %q, want the provider name", got.ReleaseName)
	}
	if len(got.Steps) != 3 {
		t.Fatalf("want 3 steps (namespace, secret, install), got %d", len(got.Steps))
	}
	if len(got.Warnings) != 0 {
		t.Errorf("fully-specified provider produced warnings: %v", got.Warnings)
	}

	install := got.Steps[2].Command
	for _, want := range []string{
		"helm upgrade --install quickstart oci://ghcr.io/faroshq/charts/faros-quickstart-provider",
		"--version 0.1.4",
		"--namespace faros-provider-quickstart",
		"--set hub.url=https://hub.example.com",
		"--set providerKubeconfig.secretName=" + KubeconfigSecretName,
		"--set catalogEntry.enabled=true",
	} {
		if !strings.Contains(install, want) {
			t.Errorf("install command missing %q\ngot:\n%s", want, install)
		}
	}

	// The chart reads this exact data key; a mismatch yields a pod that starts
	// and then cannot reach kcp.
	if !strings.Contains(got.Steps[1].Command, "--from-file="+KubeconfigSecretKey+"=") {
		t.Errorf("secret step must use the %q data key, got:\n%s", KubeconfigSecretKey, got.Steps[1].Command)
	}
}

func TestRenderInstallInstructionsResolvesIdentityHash(t *testing.T) {
	sh := baseSelfHosting()
	sh.RequiredValues = []SelfHostingValue{
		{Name: "apiExport.edgesIdentityHash", IdentityFor: "edges.providers.faros.sh"},
	}
	opts := baseOptions()
	opts.Identities = fakeIdentities{"edges.providers.faros.sh": "abc123"}

	got := RenderInstallInstructions(sh, opts)
	if !strings.Contains(got.Steps[2].Command, "--set apiExport.edgesIdentityHash=abc123") {
		t.Errorf("identity hash not substituted:\n%s", got.Steps[2].Command)
	}
	if len(got.Warnings) != 0 {
		t.Errorf("resolved identity should not warn: %v", got.Warnings)
	}
	for _, v := range got.Values {
		if v.Name == "apiExport.edgesIdentityHash" && v.Unresolved {
			t.Error("resolved identity marked Unresolved")
		}
	}
}

// An unresolved identity hash must be loud: it produces a provider that binds
// successfully and then silently sees none of the resources it claimed.
func TestRenderInstallInstructionsWarnsOnUnresolvedIdentity(t *testing.T) {
	sh := baseSelfHosting()
	sh.RequiredValues = []SelfHostingValue{
		{Name: "apiExport.edgesIdentityHash", IdentityFor: "edges.providers.faros.sh"},
	}
	// No resolver wired at all — the hub could not look it up.
	got := RenderInstallInstructions(sh, baseOptions())

	if len(got.Warnings) == 0 {
		t.Fatal("unresolved identity hash produced no warning")
	}
	var found bool
	for _, v := range got.Values {
		if v.Name == "apiExport.edgesIdentityHash" {
			found = true
			if !v.Unresolved {
				t.Error("unresolved identity not marked Unresolved")
			}
		}
	}
	if !found {
		t.Error("declared value missing from rendered values")
	}
}

// Incomplete metadata must still produce instructions: the alternative leaves
// the user with a live credential and nothing telling them what to do next.
func TestRenderInstallInstructionsWithoutChartStillRenders(t *testing.T) {
	got := RenderInstallInstructions(&SelfHosting{Supported: true}, baseOptions())

	if len(got.Steps) != 3 {
		t.Fatalf("want 3 steps even without chart coordinates, got %d", len(got.Steps))
	}
	if len(got.Warnings) == 0 {
		t.Error("missing chart coordinates produced no warning")
	}
}

func TestRenderInstallInstructionsWarnsOnMissingHubURL(t *testing.T) {
	opts := baseOptions()
	opts.HubURL = ""
	got := RenderInstallInstructions(baseSelfHosting(), opts)

	if len(got.Warnings) == 0 {
		t.Error("missing hub URL produced no warning")
	}
	if !strings.Contains(got.Steps[2].Command, "hub.url=<hub-url>") {
		t.Errorf("expected a visible placeholder for hub.url:\n%s", got.Steps[2].Command)
	}
}

// Placeholders let a provider's static recipe reference per-installation facts
// it cannot know when authored — the infrastructure provider needs the org's
// own workspace path passed as a Helm value.
func TestRenderInstallInstructionsExpandsPlaceholders(t *testing.T) {
	sh := baseSelfHosting()
	sh.RequiredValues = []SelfHostingValue{
		{Name: "bootstrap.workspacePath", Value: "{{workspacePath}}"},
		{Name: "bootstrap.kcpKubeconfigSecretRef.name", Value: "{{kubeconfigSecret}}"},
		{Name: "bootstrap.kcpKubeconfigSecretRef.key", Value: "{{kubeconfigSecretKey}}"},
	}
	got := RenderInstallInstructions(sh, baseOptions())

	cmd := got.Steps[2].Command
	for _, want := range []string{
		"--set bootstrap.workspacePath=root:faros:tenants:org1:providers:quickstart",
		"--set bootstrap.kcpKubeconfigSecretRef.name=" + KubeconfigSecretName,
		"--set bootstrap.kcpKubeconfigSecretRef.key=" + KubeconfigSecretKey,
	} {
		if !strings.Contains(cmd, want) {
			t.Errorf("missing %q in:\n%s", want, cmd)
		}
	}
	if strings.Contains(cmd, "{{") {
		t.Errorf("unsubstituted placeholder left in command:\n%s", cmd)
	}
	if len(got.Warnings) != 0 {
		t.Errorf("literal values should not warn: %v", got.Warnings)
	}
}

// {{hubURL}} lets a recipe reuse the address the hub already knows, instead of
// making every installer look it up and type it in.
func TestRenderInstallInstructionsExpandsHubURL(t *testing.T) {
	sh := baseSelfHosting()
	sh.RequiredValues = []SelfHostingValue{{Name: "hub.externalURL", Value: "{{hubURL}}"}}

	got := RenderInstallInstructions(sh, baseOptions())
	if !strings.Contains(got.Steps[2].Command, "--set hub.externalURL=https://hub.example.com") {
		t.Errorf("hub URL not substituted:\n%s", got.Steps[2].Command)
	}
	if len(got.Warnings) != 0 {
		t.Errorf("derived value should not warn: %v", got.Warnings)
	}
}

// A placeholder the hub cannot fill must not be pasted into a command as if it
// were a real value.
func TestRenderInstallInstructionsFlagsUnfilledPlaceholder(t *testing.T) {
	sh := baseSelfHosting()
	sh.RequiredValues = []SelfHostingValue{{Name: "hub.externalURL", Value: "{{hubURL}}"}}
	opts := baseOptions()
	opts.HubURL = "" // hub has no external URL configured

	got := RenderInstallInstructions(sh, opts)
	if strings.Contains(got.Steps[2].Command, "{{hubURL}}") {
		t.Errorf("unfilled placeholder leaked into the command:\n%s", got.Steps[2].Command)
	}
	var flagged bool
	for _, v := range got.Values {
		if v.Name == "hub.externalURL" && v.Unresolved {
			flagged = true
		}
	}
	if !flagged {
		t.Error("unfilled placeholder not marked Unresolved")
	}
}

// A recipe published before the placeholders existed (or one that forgot them)
// must not push a lookup onto the user for a value the hub already holds.
func TestRenderInstallInstructionsFillsHubKnownValues(t *testing.T) {
	sh := baseSelfHosting()
	sh.RequiredValues = []SelfHostingValue{
		{Name: "hub.externalURL"},                       // no value declared
		{Name: "bootstrap.kcpKubeconfigSecretRef.name"}, // no value declared
		{Name: "bootstrap.kcpKubeconfigSecretRef.key"},  // no value declared
	}
	got := RenderInstallInstructions(sh, baseOptions())

	cmd := got.Steps[2].Command
	for _, want := range []string{
		"--set hub.externalURL=https://hub.example.com",
		"--set bootstrap.kcpKubeconfigSecretRef.name=" + KubeconfigSecretName,
		"--set bootstrap.kcpKubeconfigSecretRef.key=" + KubeconfigSecretKey,
	} {
		if !strings.Contains(cmd, want) {
			t.Errorf("missing %q in:\n%s", want, cmd)
		}
	}
	if len(got.Warnings) != 0 {
		t.Errorf("hub-known values should not warn: %v", got.Warnings)
	}
	if strings.Contains(cmd, "<value>") {
		t.Errorf("hub-known value left as a placeholder:\n%s", cmd)
	}
}

// A value the hub genuinely cannot know must still be asked for.
func TestRenderInstallInstructionsStillAsksForUnknownValues(t *testing.T) {
	sh := baseSelfHosting()
	sh.RequiredValues = []SelfHostingValue{{Name: "databricks.workspaceHost"}}

	got := RenderInstallInstructions(sh, baseOptions())
	if len(got.Warnings) == 0 {
		t.Error("provider-specific value produced no warning")
	}
	if !strings.Contains(got.Steps[2].Command, "databricks.workspaceHost=<value>") {
		t.Errorf("expected a visible placeholder:\n%s", got.Steps[2].Command)
	}
}

func TestSelfHostingInstallable(t *testing.T) {
	for _, tc := range []struct {
		name string
		sh   *SelfHosting
		want bool
	}{
		{name: "nil", sh: nil},
		{name: "not supported", sh: &SelfHosting{ChartRepo: "oci://r", ChartName: "c"}},
		{name: "supported but no chart", sh: &SelfHosting{Supported: true}},
		{name: "supported, repo only", sh: &SelfHosting{Supported: true, ChartRepo: "oci://r"}},
		{name: "complete", sh: &SelfHosting{Supported: true, ChartRepo: "oci://r", ChartName: "c"}, want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.sh.Installable(); got != tc.want {
				t.Errorf("Installable() = %v, want %v", got, tc.want)
			}
		})
	}
}

// Generated commands get pasted into a shell, so a value carrying shell
// metacharacters must survive the trip.
func TestShellQuote(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"simple", "simple"},
		{"https://hub.example.com", "https://hub.example.com"},
		{"faros-provider-x", "faros-provider-x"},
		{"", "''"},
		{"has space", "'has space'"},
		{"semi;rm -rf /", "'semi;rm -rf /'"},
		{"it's", `'it'\''s'`},
	} {
		if got := shellQuote(tc.in); got != tc.want {
			t.Errorf("shellQuote(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
