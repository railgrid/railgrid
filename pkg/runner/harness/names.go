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

package harness

// There are deliberately TWO names for a harness, and conflating them has
// already produced two separate bugs, so the mapping lives here and nowhere
// else.
//
//   - The SELECTOR name is what a human writes to choose one: `--harness claude`
//     on the agent, `spec.harness.enabled: [claude]` on an edge. It names the
//     thing you install, and it is short because people type it.
//   - The ADVERTISED name is what the harness calls itself in the runner's
//     capabilities response: `claude-code`. It is the product's own name, and it
//     is what `StartRequest.RequiredHarness` is matched against, because that
//     field exists to pin what actually answered.
//
// They differ only for Claude Code, which is exactly why it goes wrong: code
// that assumes they are equal works for Codex and silently fails for Claude —
// once as "no Claude runner is ever ready", once as a dispatch refused for a
// harness mismatch that did not exist.
const (
	// SelectorClaude is how a person selects headless Claude Code.
	SelectorClaude = "claude"
	// SelectorCodex is how a person selects the Codex app-server.
	SelectorCodex = "codex"
)

// Selectors are every harness a railgrid agent can supervise, in the order a
// port allocator should consider them.
var Selectors = []string{SelectorClaude, SelectorCodex} //nolint:gochecknoglobals

// advertisedBySelector maps a selector to the name its harness reports. It is
// the one table; AdvertisedName and SelectorFor are both views of it.
var advertisedBySelector = map[string]string{ //nolint:gochecknoglobals // immutable name table
	SelectorClaude: "claude-code",
	SelectorCodex:  "codex",
}

// AdvertisedName returns the name the harness selected by selector reports in a
// capabilities response, which is what RequiredHarness must be set to.
//
// An unknown selector comes back unchanged rather than empty: the caller's own
// "unknown harness" message is clearer than a blank field, and inventing a name
// would only move the failure to the runner.
func AdvertisedName(selector string) string {
	if advertised, ok := advertisedBySelector[selector]; ok {
		return advertised
	}
	return selector
}

// SelectorFor is the inverse: the selector a person would write for a harness
// that advertised this name. It is how a reader turns a runner's capabilities
// answer back into the field they have to edit.
func SelectorFor(advertised string) string {
	for selector, name := range advertisedBySelector {
		if name == advertised {
			return selector
		}
	}
	return advertised
}
