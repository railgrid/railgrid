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

package store

import (
	"strings"
	"testing"
)

func TestAssistantReviewModeMigrationWidensOnlyCollaborationModeConstraints(t *testing.T) {
	joined := strings.Join(assistantReviewModeSchemaStatements(), "\n")
	for _, want := range []string{
		"app_studio_assistant_runs",
		"app_studio_assistant_turns",
		"a.attname = 'mode'",
		"CHECK (mode IN ('default','plan','review'))",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("review-mode migration missing %q", want)
		}
	}
	if strings.Contains(joined, "a.attname = 'approval_mode'") {
		t.Fatal("review-mode migration targets approval constraints")
	}
	if assistantReviewModeSchemaVersion != "assistant-review-mode-v1" {
		t.Fatalf("review-mode schema version = %q", assistantReviewModeSchemaVersion)
	}
}
