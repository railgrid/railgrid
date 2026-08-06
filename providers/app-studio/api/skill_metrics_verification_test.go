// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package api

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestProjectAssistantSkillMetricsExposeOnlyFixedOutcomeLabels(t *testing.T) {
	projectAssistantSkillMetric("lifecycle", "create")
	// Unknown values must not create an exposition label containing a path,
	// tenant, package, or other request-derived value.
	projectAssistantSkillMetric("lifecycle", "tenant/org-a/package/review/demo")
	projectAssistantSkillMetric("tenant/org-a", "secret-value")

	response := httptest.NewRecorder()
	writeProjectAssistantSkillMetrics(response)
	body := response.Body.String()
	for _, line := range strings.Split(body, "\n") {
		if line == "" || strings.HasPrefix(line, "#") || !strings.Contains(line, "app_studio_assistant_skill_") {
			continue
		}
		start, end := strings.IndexByte(line, '{'), strings.IndexByte(line, '}')
		if start < 0 || end <= start {
			t.Fatalf("metric line has no outcome label set: %q", line)
		}
		labels := line[start+1 : end]
		// Keep the assertion explicit rather than relying on parser behavior:
		// no comma or second key may be rendered.
		if strings.Contains(labels, ",") || strings.Count(labels, "outcome=") != 1 || !strings.HasPrefix(labels, `outcome="`) || !strings.HasSuffix(labels, `"`) {
			t.Fatalf("metric line exposes non-fixed labels: %q", line)
		}
		if strings.Contains(labels, "/") || strings.Contains(labels, "secret") || strings.Contains(labels, "org-a") {
			t.Fatalf("metric line leaked request-derived data: %q", line)
		}
	}
	if strings.Contains(body, "tenant/org-a") || strings.Contains(body, "secret-value") {
		t.Fatalf("metrics exposition leaked unknown metric labels: %s", body)
	}
}
