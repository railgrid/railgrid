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

// Helm decodes bare YAML integers as float64, so the shipped default of
// 1073741824 used to render as "1.073741824e+09" and the provider refused to
// start ("attachment quota must be a non-negative byte count or IEC value").
// Integers must reach the container as plain digits; IEC strings and zero
// must pass through; a null must emit no env var at all.
func TestChartAttachmentQuotaRendersIntegersAsDigits(t *testing.T) {
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
	const envName = "name: APP_STUDIO_ATTACHMENT_WORKSPACE_QUOTA_BYTES"
	envLine := func(value string) string {
		return envName + "\n              value: " + value
	}

	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{name: "shipped default", want: envLine(`"1073741824"`)},
		{name: "large integer override", args: []string{"--set", "store.attachmentWorkspaceQuotaBytes=5368709120"}, want: envLine(`"5368709120"`)},
		{name: "iec string override", args: []string{"--set-string", "store.attachmentWorkspaceQuotaBytes=1Gi"}, want: envLine(`"1Gi"`)},
		{name: "zero disables the limit", args: []string{"--set", "store.attachmentWorkspaceQuotaBytes=0"}, want: envLine(`"0"`)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rendered := render(t, tc.args...)
			if !strings.Contains(rendered, tc.want) {
				t.Fatalf("render %v lacks %q\n%s", tc.args, tc.want, excerpt(rendered, envName))
			}
			if strings.Contains(rendered, "e+") && strings.Contains(excerpt(rendered, envName), "e+") {
				t.Fatalf("render %v emitted a float in scientific notation\n%s", tc.args, excerpt(rendered, envName))
			}
		})
	}

	for _, args := range [][]string{
		{"--set", "store.attachmentWorkspaceQuotaBytes=null"},
		{"--set-string", "store.attachmentWorkspaceQuotaBytes="},
	} {
		if rendered := render(t, args...); strings.Contains(rendered, envName) {
			t.Fatalf("override %v emitted %s; an unset quota must leave the provider default in force\n%s", args, envName, excerpt(rendered, envName))
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
	return "(env var not rendered)"
}
