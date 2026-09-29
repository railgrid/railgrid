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

package api

import (
	"encoding/json"
	"strings"
	"testing"

	agentsv1alpha1 "github.com/railgrid/provider-agents/apis/v1alpha1"
	"github.com/railgrid/provider-agents/store"
)

// TestASummaryReportsWhichBackendRanTheTurn: the two backends fail in different
// places, and a list that does not say which one ran a turn sends a reader
// looking at a model endpoint for a problem that is on a machine.
func TestASummaryReportsWhichBackendRanTheTurn(t *testing.T) {
	harnessed := summarize(store.Run{ID: "r1", Backend: agentsv1alpha1.AgentBackendHarness})
	if harnessed.Backend != agentsv1alpha1.AgentBackendHarness {
		t.Errorf("summary backend = %q, want %q", harnessed.Backend, agentsv1alpha1.AgentBackendHarness)
	}

	// A row written before an agent could have a backend carries nothing, and
	// must stay absent rather than be guessed at: "model" is the API's default,
	// not a fact this row recorded.
	legacy := summarize(store.Run{ID: "r2"})
	if legacy.Backend != "" {
		t.Errorf("an unrecorded backend was reported as %q", legacy.Backend)
	}
	body, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), `"backend"`) {
		t.Errorf("an absent backend was serialized: %s", body)
	}
}

// TestOnlyAHarnessRunCarriesEdgeCoordinates: on an in-process run the attempt
// and session name nothing, so an empty block would invite a reader to go
// looking for a machine that was never involved.
func TestOnlyAHarnessRunCarriesEdgeCoordinates(t *testing.T) {
	for name, run := range map[string]store.Run{
		"in-process":                        {ID: "r1", Backend: agentsv1alpha1.AgentBackendModel, AttemptID: "r1", HarnessSessionID: "s1"},
		"unrecorded":                        {ID: "r2", AttemptID: "r2"},
		"harness with nothing reported yet": {ID: "r3", Backend: agentsv1alpha1.AgentBackendHarness},
	} {
		t.Run(name, func(t *testing.T) {
			detail := runDetail{runSummary: summarize(run)}
			if run.Backend == agentsv1alpha1.AgentBackendHarness && (run.AttemptID != "" || run.HarnessSessionID != "") {
				t.Fatal("test fixture is not the case it claims to be")
			}
			if detail.Harness != nil {
				t.Errorf("%s run carries edge coordinates: %+v", name, detail.Harness)
			}
		})
	}
}
