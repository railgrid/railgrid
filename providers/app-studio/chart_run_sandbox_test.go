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

// The controllers are leader-elected, so the chart no longer refuses a second
// replica for the shared Playwright Browser — that is a default and an
// operator's judgement now. Coding-sandbox claims are different: without
// distributed CAS two replicas can both believe they own a sandbox, so that
// one combination is still a hard refusal.
func TestChartAcceptsMultipleReplicasExceptForcedRunSandbox(t *testing.T) {
	helm, err := exec.LookPath("helm")
	if err != nil {
		t.Skip("helm is not installed")
	}
	render := func(values ...string) ([]byte, error) {
		args := []string{"template", "app-studio", "deploy/chart"}
		for _, value := range values {
			args = append(args, "--set", value)
		}
		return exec.Command(helm, args...).CombinedOutput()
	}

	output, err := render("replicaCount=2", "workspace.emptyDir=true")
	if err != nil {
		t.Fatalf("helm template rejected replicaCount=2: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "replicas: 2") {
		t.Fatalf("rendered deployment must carry replicas: 2:\n%s", output)
	}

	output, err = render("replicaCount=2", "assistant.runSandbox.mode=force", "workspace.emptyDir=true")
	if err == nil {
		t.Fatalf("helm template accepted replicaCount=2 with runSandbox.mode=force:\n%s", output)
	}
	if !strings.Contains(string(output), "coding sandbox claims have distributed CAS") {
		t.Fatalf("helm template must explain the coding-sandbox claim boundary: %v\n%s", err, output)
	}
}
