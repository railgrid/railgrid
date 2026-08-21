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

package telemetryreceiver

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestGrafanaDashboardJSONAndQueriesAreAggregateOnly(t *testing.T) {
	raw, err := os.ReadFile("../../deploy/charts/faros-telemetry/dashboards/faros-product-analytics.json")
	if err != nil {
		t.Fatal(err)
	}
	var dashboard struct {
		Panels []struct {
			Targets []struct {
				SQL string `json:"rawSql"`
			} `json:"targets"`
		} `json:"panels"`
	}
	if err := json.Unmarshal(raw, &dashboard); err != nil {
		t.Fatalf("dashboard JSON: %v", err)
	}
	if len(dashboard.Panels) != 5 {
		t.Fatalf("dashboard panels = %d, want 5", len(dashboard.Panels))
	}
	for _, panel := range dashboard.Panels {
		for _, target := range panel.Targets {
			lower := strings.ToLower(target.SQL)
			for _, forbidden := range []string{"tenant_id", "installation_id", "unique_hash", "source", "faros_telemetry_events"} {
				if strings.Contains(lower, forbidden) {
					t.Fatalf("dashboard query exposes forbidden field %q: %s", forbidden, target.SQL)
				}
			}
		}
	}
}
