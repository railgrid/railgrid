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

package main

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestChartAccessProxyImageFollowsRelease guards the access gate against
// drifting behind the provider: a released chart (packaged with --app-version
// vX.Y.Z by provider-release.yaml) must point the gate at the same release's
// railgrid-access-proxy in BOTH the legacy serve Deployment and the
// operator-managed InfrastructureProvider CR, while a chart that is not a
// release package (the in-repo chart, a sha-<commit> main build) leaves it to
// the binary, because no gate image is published for it.
func TestChartAccessProxyImageFollowsRelease(t *testing.T) {
	helm, err := exec.LookPath("helm")
	if err != nil {
		t.Skip("helm is not installed")
	}
	pkgDir := t.TempDir()
	pack := func(version, appVersion string) string {
		t.Helper()
		if out, err := exec.Command(helm, "package", "deploy/chart",
			"--version", version, "--app-version", appVersion, "--destination", pkgDir).CombinedOutput(); err != nil {
			t.Fatalf("helm package: %v\n%s", err, out)
		}
		return filepath.Join(pkgDir, "railgrid-infrastructure-provider-"+version+".tgz")
	}
	released := pack("0.3.0", "v0.3.0")
	mainBuild := pack("0.0.0-main", "sha-0123abc")

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
		return "- name: RAILGRID_ACCESS_PROXY_IMAGE\n              value: \"" + image + "\""
	}
	crValue := func(image string) string { return "accessProxyImage: \"" + image + "\"" }
	const releaseImage = "ghcr.io/railgrid/railgrid-access-proxy:v0.3.0"

	t.Run("unreleased charts leave the binary default", func(t *testing.T) {
		for _, chart := range []string{"deploy/chart", mainBuild} {
			if out := render(t, chart); strings.Contains(out, "- name: RAILGRID_ACCESS_PROXY_IMAGE") {
				t.Fatalf("%s must not set RAILGRID_ACCESS_PROXY_IMAGE", chart)
			}
			out := render(t, chart, "operator.enabled=true")
			if strings.Contains(out, "accessProxyImage: \"") {
				t.Fatalf("%s must not stamp publishing.accessProxyImage on the InfrastructureProvider CR", chart)
			}
			if !strings.Contains(out, "  publishing:\n    baseDomain: \"\"\n    hubURL:") {
				t.Fatalf("%s must still render the InfrastructureProvider publishing block\n%s", chart, out)
			}
		}
	})

	t.Run("released chart pins the release gate in legacy mode", func(t *testing.T) {
		if out := render(t, released); !strings.Contains(out, envValue(releaseImage)) {
			t.Fatalf("want RAILGRID_ACCESS_PROXY_IMAGE=%s\n%s", releaseImage, out)
		}
	})

	t.Run("released chart pins the release gate in operator mode", func(t *testing.T) {
		if out := render(t, released, "operator.enabled=true"); !strings.Contains(out, crValue(releaseImage)) {
			t.Fatalf("want InfrastructureProvider spec.publishing.accessProxyImage=%s\n%s", releaseImage, out)
		}
	})

	t.Run("explicit accessProxyImage wins", func(t *testing.T) {
		pinned := "ghcr.io/railgrid/railgrid-access-proxy@sha256:" + strings.Repeat("b", 64)
		out := render(t, released, "publishing.accessProxyImage="+pinned)
		if strings.Contains(out, releaseImage) || !strings.Contains(out, envValue(pinned)) {
			t.Fatalf("explicit publishing.accessProxyImage must override the release default in legacy mode\n%s", out)
		}
		out = render(t, released, "operator.enabled=true", "operator.publishing.accessProxyImage="+pinned)
		if strings.Contains(out, crValue(releaseImage)) || !strings.Contains(out, crValue(pinned)) {
			t.Fatalf("explicit operator.publishing.accessProxyImage must override the release default in operator mode\n%s", out)
		}
	})

	t.Run("repository override", func(t *testing.T) {
		const mirrored = "mirror.example/railgrid-access-proxy:v0.3.0"
		if out := render(t, released, "publishing.accessProxyImageRepository=mirror.example/railgrid-access-proxy"); !strings.Contains(out, envValue(mirrored)) {
			t.Fatalf("publishing.accessProxyImageRepository must feed the release default\n%s", out)
		}
		out := render(t, released, "operator.enabled=true", "operator.publishing.accessProxyImageRepository=mirror.example/railgrid-access-proxy")
		if !strings.Contains(out, crValue(mirrored)) {
			t.Fatalf("operator.publishing.accessProxyImageRepository must feed the release default\n%s", out)
		}
	})
}
