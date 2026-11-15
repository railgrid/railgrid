// Copyright 2026 The Railgrid Authors.
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

package model

import (
	"testing"

	"github.com/railgrid/provider-agents/backend"
	"github.com/railgrid/provider-agents/engine"
)

func TestUsageCompletenessAndCostBasis(t *testing.T) {
	for _, tc := range []struct {
		name     string
		evidence engine.UsageEvidence
		want     backend.UsageCompleteness
	}{
		{"unavailable", engine.UsageEvidence{}, backend.UsageUnavailable},
		{"reported none", engine.UsageEvidence{Responses: 2}, backend.UsageUnavailable},
		{"partial", engine.UsageEvidence{Responses: 2, ReportedResponses: 1}, backend.UsagePartial},
		{"complete", engine.UsageEvidence{Responses: 2, ReportedResponses: 2}, backend.UsageComplete},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := usageCompleteness(tc.evidence); got != tc.want {
				t.Fatalf("usageCompleteness(%+v) = %q, want %q", tc.evidence, got, tc.want)
			}
		})
	}
	if got := costBasis("gpt-4o", true); got != backend.CostBasisCatalogEstimate {
		t.Fatalf("known model cost basis = %q", got)
	}
	if got := costBasis("gpt-4o", false); got != backend.CostBasisUnavailable {
		t.Fatalf("unreported usage cost basis = %q", got)
	}
	if got := costBasis("unknown-model", true); got != backend.CostBasisUnavailable {
		t.Fatalf("unknown model cost basis = %q", got)
	}
}
