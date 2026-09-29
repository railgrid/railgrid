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

package harness_test

import (
	"testing"

	"github.com/railgrid/railgrid/pkg/runner/harness"
	"github.com/railgrid/railgrid/pkg/runner/harness/claude"
	"github.com/railgrid/railgrid/pkg/runner/harness/codex"
)

// TestAdvertisedNamesMatchWhatTheAdaptersReport is the assertion that makes the
// table trustworthy: it is checked against the adapters themselves, so a harness
// that renames itself breaks this test rather than breaking dispatch.
func TestAdvertisedNamesMatchWhatTheAdaptersReport(t *testing.T) {
	for selector, want := range map[string]string{
		harness.SelectorClaude: claude.HarnessName,
		harness.SelectorCodex:  codex.HarnessName,
	} {
		if got := harness.AdvertisedName(selector); got != want {
			t.Errorf("AdvertisedName(%q) = %q, want %q (the adapter's own HarnessName)", selector, got, want)
		}
		if got := harness.SelectorFor(want); got != selector {
			t.Errorf("SelectorFor(%q) = %q, want %q", want, got, selector)
		}
	}
}

// TestClaudesTwoNamesActuallyDiffer guards the reason this table exists. If the
// two ever became equal, every caller that hand-wrote the mapping would look
// correct, and the next divergence would reintroduce the same class of bug.
func TestClaudesTwoNamesActuallyDiffer(t *testing.T) {
	if harness.AdvertisedName(harness.SelectorClaude) == harness.SelectorClaude {
		t.Fatal("Claude Code's selector and advertised name are equal; this table and its callers need revisiting")
	}
}

// TestAnUnknownHarnessIsReturnedUnchanged: a typo must reach the caller's own
// error message intact rather than becoming an empty field.
func TestAnUnknownHarnessIsReturnedUnchanged(t *testing.T) {
	if got := harness.AdvertisedName("gpt"); got != "gpt" {
		t.Errorf("AdvertisedName(unknown) = %q, want it unchanged", got)
	}
	if got := harness.SelectorFor("gpt"); got != "gpt" {
		t.Errorf("SelectorFor(unknown) = %q, want it unchanged", got)
	}
}
