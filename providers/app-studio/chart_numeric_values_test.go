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
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Helm decodes bare YAML integers from a values file as float64, so the
// shipped default of 1073741824 used to render as "1.073741824e+09" and the
// provider refused to start ("attachment quota must be a non-negative byte
// count or IEC value"). The assistant limits had the same latent fault: a
// budget of 1000000 rendered as "1e+06", which the integer parsers silently
// replaced with their defaults. Every numeric env var must reach the
// container as plain digits; IEC strings, sentinels and zero must pass
// through; a null or empty value must emit no env var at all.
//
// The overrides are fed through a values file, not --set, because --set
// parses integers as int64 and never reproduces the float64 path that Flux
// and helm -f take.
func TestChartNumericValuesRenderAsDigits(t *testing.T) {
	helm, err := exec.LookPath("helm")
	if err != nil {
		t.Skip("helm is not installed")
	}
	render := func(t *testing.T, values string) string {
		t.Helper()
		args := []string{"template", "app-studio", "deploy/chart"}
		if values != "" {
			path := filepath.Join(t.TempDir(), "values.yaml")
			if err := os.WriteFile(path, []byte(values), 0o600); err != nil {
				t.Fatal(err)
			}
			args = append(args, "-f", path)
		}
		output, err := exec.Command(helm, args...).CombinedOutput()
		if err != nil {
			t.Fatalf("helm template with values %q: %v\n%s", values, err, output)
		}
		return string(output)
	}
	envLine := func(name, value string) string {
		return "name: " + name + "\n              value: " + value
	}

	const (
		quotaEnv      = "APP_STUDIO_ATTACHMENT_WORKSPACE_QUOTA_BYTES"
		iterationsEnv = "APP_STUDIO_ASSISTANT_MAX_ITERATIONS"
		tokensEnv     = "APP_STUDIO_ASSISTANT_ROLLOUT_BUDGET_TOKENS"
		usdCapEnv     = "APP_STUDIO_ORG_MONTHLY_USD_CAP"
	)

	for _, tc := range []struct {
		name   string
		values string
		want   []string
	}{
		{
			name: "shipped default",
			want: []string{envLine(quotaEnv, `"1073741824"`)},
		},
		{
			name:   "large integers from a values file",
			values: "store:\n  attachmentWorkspaceQuotaBytes: 5368709120\nassistant:\n  limits:\n    maxIterations: 1000000\n    rolloutBudgetTokens: 2000000\n    orgMonthlyUSDCap: 1000000\n",
			want: []string{
				envLine(quotaEnv, `"5368709120"`),
				envLine(iterationsEnv, `"1000000"`),
				envLine(tokensEnv, `"2000000"`),
				envLine(usdCapEnv, `"1000000"`),
			},
		},
		{
			name:   "strings pass through",
			values: "store:\n  attachmentWorkspaceQuotaBytes: \"1Gi\"\nassistant:\n  limits:\n    maxIterations: unlimited\n    orgMonthlyUSDCap: \"$25.50\"\n",
			want: []string{
				envLine(quotaEnv, `"1Gi"`),
				envLine(iterationsEnv, `"unlimited"`),
				envLine(usdCapEnv, `"$25.50"`),
			},
		},
		{
			name:   "fractional cap keeps its decimals",
			values: "assistant:\n  limits:\n    orgMonthlyUSDCap: 25.5\n",
			want:   []string{envLine(usdCapEnv, `"25.5"`)},
		},
		{
			name:   "zero disables the quota",
			values: "store:\n  attachmentWorkspaceQuotaBytes: 0\n",
			want:   []string{envLine(quotaEnv, `"0"`)},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rendered := render(t, tc.values)
			for _, want := range tc.want {
				if !strings.Contains(rendered, want) {
					name := strings.TrimPrefix(strings.SplitN(want, "\n", 2)[0], "name: ")
					t.Errorf("rendered manifest lacks %q; got:\n%s", want, excerpt(rendered, name))
				}
			}
			for _, name := range []string{quotaEnv, iterationsEnv, tokensEnv, usdCapEnv} {
				if got := excerpt(rendered, name); strings.Contains(got, "e+") {
					t.Errorf("%s rendered in scientific notation:\n%s", name, got)
				}
			}
		})
	}

	for _, values := range []string{
		"store:\n  attachmentWorkspaceQuotaBytes: null\n",
		"store:\n  attachmentWorkspaceQuotaBytes: \"\"\n",
	} {
		if rendered := render(t, values); strings.Contains(rendered, quotaEnv) {
			t.Errorf("values %q emitted %s; an unset quota must leave the provider default in force\n%s", values, quotaEnv, excerpt(rendered, quotaEnv))
		}
	}
}

// excerpt returns the line containing marker plus the following line, so a
// failure shows the rendered env var rather than the whole manifest.
func excerpt(rendered, marker string) string {
	lines := strings.Split(rendered, "\n")
	for i, line := range lines {
		if strings.Contains(line, marker) && i+1 < len(lines) {
			return line + "\n" + lines[i+1]
		}
	}
	return "(" + marker + " not rendered)"
}
