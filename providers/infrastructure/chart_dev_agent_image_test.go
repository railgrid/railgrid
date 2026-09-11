/*
Copyright 2026 The Faros Authors.

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

package main

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestChartDevAgentImageFollowsRelease guards the dev-agent staleness fix: a
// released chart (packaged with --app-version vX.Y.Z by provider-release.yaml)
// must point FAROS_DEV_AGENT_IMAGE at the same release's faros-dev-agent in
// BOTH the legacy serve Deployment (init + serve containers) and the
// operator-managed InfrastructureProvider CR, while the unreleased in-repo
// chart leaves it to the binary (local kind/Tilt side-load :latest).
func TestChartDevAgentImageFollowsRelease(t *testing.T) {
	helm, err := exec.LookPath("helm")
	if err != nil {
		t.Skip("helm is not installed")
	}
	pkgDir := t.TempDir()
	if out, err := exec.Command(helm, "package", "deploy/chart",
		"--version", "0.1.20", "--app-version", "v0.1.20", "--destination", pkgDir).CombinedOutput(); err != nil {
		t.Fatalf("helm package: %v\n%s", err, out)
	}
	released := filepath.Join(pkgDir, "faros-infrastructure-provider-0.1.20.tgz")

	render := func(t *testing.T, chart string, values ...string) string {
		t.Helper()
		args := []string{"template", "infrastructure", chart}
		for _, value := range values {
			args = append(args, "--set", value)
		}
		out, err := exec.Command(helm, args...).CombinedOutput()
		if err != nil {
			t.Fatalf("helm template %v: %v\n%s", values, err, out)
		}
		return string(out)
	}
	envValue := func(image string) string {
		return "- name: FAROS_DEV_AGENT_IMAGE\n              value: \"" + image + "\""
	}
	const releaseImage = "ghcr.io/faroshq/faros-dev-agent:v0.1.20"
	legacyBootstrap := []string{"bootstrap.enabled=true", "bootstrap.kcpKubeconfigSecretRef.name=kcp"}

	t.Run("in-repo chart leaves the binary default", func(t *testing.T) {
		if out := render(t, "deploy/chart", legacyBootstrap...); strings.Contains(out, "- name: FAROS_DEV_AGENT_IMAGE") {
			t.Fatalf("unreleased chart must not set FAROS_DEV_AGENT_IMAGE (local flows side-load :latest)")
		}
		if out := render(t, "deploy/chart", "operator.enabled=true"); strings.Contains(out, "agentImage: \"") {
			t.Fatalf("unreleased chart must not stamp development.agentImage on the InfrastructureProvider CR")
		}
	})

	t.Run("released chart pins the release dev agent in legacy mode", func(t *testing.T) {
		out := render(t, released, legacyBootstrap...)
		if got := strings.Count(out, envValue(releaseImage)); got != 2 {
			t.Fatalf("want FAROS_DEV_AGENT_IMAGE=%s on the init and serve containers, found %d\n%s", releaseImage, got, out)
		}
	})

	t.Run("released chart pins the release dev agent in operator mode", func(t *testing.T) {
		out := render(t, released, "operator.enabled=true")
		if !strings.Contains(out, "agentImage: \""+releaseImage+"\"") {
			t.Fatalf("want InfrastructureProvider spec.development.agentImage=%s\n%s", releaseImage, out)
		}
	})

	t.Run("explicit agentImage wins", func(t *testing.T) {
		pinned := "ghcr.io/faroshq/faros-dev-agent@sha256:" + strings.Repeat("b", 64)
		out := render(t, released, append(legacyBootstrap, "development.agentImage="+pinned)...)
		if strings.Contains(out, releaseImage) || strings.Count(out, envValue(pinned)) != 2 {
			t.Fatalf("explicit development.agentImage must override the release default in legacy mode\n%s", out)
		}
		out = render(t, released, "operator.enabled=true", "development.agentImage="+pinned)
		if strings.Contains(out, releaseImage) || !strings.Contains(out, "agentImage: \""+pinned+"\"") {
			t.Fatalf("explicit development.agentImage must override the release default in operator mode\n%s", out)
		}
	})

	t.Run("repository override", func(t *testing.T) {
		out := render(t, released, "development.agentImageRepository=mirror.example/faros-dev-agent")
		if !strings.Contains(out, envValue("mirror.example/faros-dev-agent:v0.1.20")) {
			t.Fatalf("development.agentImageRepository must feed the release default\n%s", out)
		}
	})

	t.Run("coding sandbox still requires an explicit digest", func(t *testing.T) {
		out, err := exec.Command(helm, "template", "infrastructure", released,
			"--set", "codingSandbox.enabled=true",
			"--set", "development.images.universal=ghcr.io/faroshq/faros-universal-dev@sha256:"+strings.Repeat("a", 64),
		).CombinedOutput()
		if err == nil || !strings.Contains(string(out), "requires development.agentImage") {
			t.Fatalf("the release tag default must not satisfy the coding sandbox digest gate: %v\n%s", err, out)
		}
	})
}

func TestReportedVersionFollowsReleaseBuild(t *testing.T) {
	original := buildVersion
	t.Cleanup(func() { buildVersion = original })

	buildVersion = "v0.1.20"
	if got := reportedVersion(); got != "v0.1.20" {
		t.Fatalf("release build reportedVersion = %q, want v0.1.20", got)
	}
	buildVersion = "dev"
	if got := reportedVersion(); got != heartbeatVersion {
		t.Fatalf("dev build reportedVersion = %q, want %q", got, heartbeatVersion)
	}
}
