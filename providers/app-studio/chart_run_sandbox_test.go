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
	"strings"
	"testing"
)

func TestChartRunSandboxLegacyBooleanMigration(t *testing.T) {
	helm, err := exec.LookPath("helm")
	if err != nil {
		t.Skip("helm is not installed")
	}
	render := func(t *testing.T, values ...string) string {
		t.Helper()
		args := []string{"template", "app-studio", "deploy/chart"}
		for _, value := range values {
			args = append(args, "--set", value)
		}
		output, err := exec.Command(helm, args...).CombinedOutput()
		if err != nil {
			t.Fatalf("helm template: %v\n%s", err, output)
		}
		return string(output)
	}

	defaultChart := render(t)
	if !strings.Contains(defaultChart, "name: APP_STUDIO_RUN_SANDBOX_MODE\n              value: \"off\"") || strings.Contains(defaultChart, "name: APP_STUDIO_RUN_SANDBOX\n") {
		t.Fatalf("default chart must explicitly select mode=off without the legacy boolean")
	}

	legacy := render(t, "assistant.runSandbox.enabled=true")
	if strings.Contains(legacy, "name: APP_STUDIO_RUN_SANDBOX_MODE\n") || !strings.Contains(legacy, "name: APP_STUDIO_RUN_SANDBOX\n              value: \"true\"") {
		t.Fatalf("deprecated enabled=true must reach startup without default mode=off masking its byo-only migration")
	}

	explicit := render(t, "assistant.runSandbox.mode=byo-only", "assistant.runSandbox.enabled=true")
	if !strings.Contains(explicit, "name: APP_STUDIO_RUN_SANDBOX_MODE\n              value: \"byo-only\"") {
		t.Fatalf("an explicit non-off mode must remain authoritative")
	}
}

func TestChartRejectsMultipleReplicasForSharedBrowser(t *testing.T) {
	helm, err := exec.LookPath("helm")
	if err != nil {
		t.Skip("helm is not installed")
	}

	output, err := exec.Command(
		helm,
		"template", "app-studio", "deploy/chart",
		"--set", "replicaCount=2",
		"--set", "workspace.emptyDir=true",
	).CombinedOutput()
	if err == nil {
		t.Fatalf("helm template unexpectedly accepted replicaCount=2 with an emptyDir workspace")
	}
	if !strings.Contains(string(output), "shared Playwright Browser is single-session and its session ownership is process-local") {
		t.Fatalf("helm template must explain the shared-browser replica boundary: %v\n%s", err, output)
	}
}
