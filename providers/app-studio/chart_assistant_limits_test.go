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

// An operator override may drop assistant.limits entirely. That must leave the
// provider on its own finite defaults rather than failing to render, so the
// template reads the block through the nil-safe parenthesized form. Values
// that are present must still reach the container as env vars.
func TestChartAssistantLimitsToleratesAMissingBlock(t *testing.T) {
	helm, err := exec.LookPath("helm")
	if err != nil {
		t.Skip("helm is not installed")
	}
	render := func(t *testing.T, args ...string) string {
		t.Helper()
		full := append([]string{"template", "app-studio", "deploy/chart"}, args...)
		output, err := exec.Command(helm, full...).CombinedOutput()
		if err != nil {
			t.Fatalf("helm template %v: %v\n%s", args, err, output)
		}
		return string(output)
	}

	limitEnvVars := []string{
		"name: APP_STUDIO_ASSISTANT_MAX_ITERATIONS",
		"name: APP_STUDIO_ASSISTANT_ROLLOUT_BUDGET_TOKENS",
		"name: APP_STUDIO_ORG_MONTHLY_USD_CAP",
	}

	// The shipped values leave every limit empty, so the provider defaults
	// apply and no env var is emitted.
	for _, env := range limitEnvVars {
		if strings.Contains(render(t), env) {
			t.Fatalf("default chart emitted %s; empty values must leave the provider defaults in force", env)
		}
	}

	// An override that nulls the whole block, or replaces assistant without
	// it, must render rather than fault on the missing map.
	for _, args := range [][]string{
		{"--set", "assistant.limits=null"},
		{"--set", "assistant.limits.maxIterations=null", "--set", "assistant.limits.rolloutBudgetTokens=null", "--set", "assistant.limits.orgMonthlyUSDCap=null"},
		{"--set-json", `assistant={"runSandbox":{"mode":"off","developmentMode":false}}`},
	} {
		rendered := render(t, args...)
		for _, env := range limitEnvVars {
			if strings.Contains(rendered, env) {
				t.Fatalf("override %v emitted %s; a missing limits block must emit no env vars", args, env)
			}
		}
	}

	// Configured limits still reach the container.
	configured := render(t,
		"--set", "assistant.limits.maxIterations=50",
		"--set", "assistant.limits.rolloutBudgetTokens=1000",
		"--set", "assistant.limits.orgMonthlyUSDCap=25.5",
	)
	for _, want := range []string{
		"name: APP_STUDIO_ASSISTANT_MAX_ITERATIONS\n              value: \"50\"",
		"name: APP_STUDIO_ASSISTANT_ROLLOUT_BUDGET_TOKENS\n              value: \"1000\"",
		"name: APP_STUDIO_ORG_MONTHLY_USD_CAP\n              value: \"25.5\"",
	} {
		if !strings.Contains(configured, want) {
			t.Fatalf("configured chart is missing %q", want)
		}
	}
}
