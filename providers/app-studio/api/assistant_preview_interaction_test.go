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

package api

import (
	"strings"
	"testing"
)

func TestBrowserMCPParseNodesCapturesRefs(t *testing.T) {
	nodes := browserMCPParseAccessibilityNodes(browserMCPExtractSnapshotTree(browserMCPSampleSnapshot))
	byName := map[string]browserMCPNode{}
	for _, n := range nodes {
		byName[n.name] = n
	}
	if got := byName["Sign in"]; got.role != "button" || got.ref != "e5" {
		t.Fatalf("button node = %+v, want role=button ref=e5", got)
	}
	if got := byName["Email"]; got.role != "textbox" || got.ref != "e3" {
		t.Fatalf("textbox node = %+v, want role=textbox ref=e3", got)
	}
}

func TestFindInteractionTarget(t *testing.T) {
	tree := browserMCPExtractSnapshotTree(browserMCPSampleSnapshot)

	node, ok := findInteractionTarget(tree, projectAssistantPreviewInteractionStep{Action: "click", Role: "button", Name: "Sign in"})
	if !ok || node.ref != "e5" {
		t.Fatalf("click target = %+v ok=%v, want ref=e5", node, ok)
	}
	// Role-only match returns the first of that role.
	node, ok = findInteractionTarget(tree, projectAssistantPreviewInteractionStep{Action: "type", Role: "textbox"})
	if !ok || node.ref != "e3" {
		t.Fatalf("textbox target = %+v ok=%v, want first textbox ref=e3", node, ok)
	}
	// Name-only (substring) match across roles.
	node, ok = findInteractionTarget(tree, projectAssistantPreviewInteractionStep{Action: "click", Name: "Back to"})
	if !ok || node.ref != "e6" {
		t.Fatalf("name-only target = %+v ok=%v, want ref=e6", node, ok)
	}
	// No match.
	if _, ok := findInteractionTarget(tree, projectAssistantPreviewInteractionStep{Action: "click", Role: "checkbox"}); ok {
		t.Fatal("checkbox unexpectedly matched")
	}
}

func TestProjectAssistantPreviewInteractionStepsValidation(t *testing.T) {
	// Valid script parses.
	steps, err := projectAssistantPreviewInteractionSteps([]any{
		map[string]any{"action": "type", "role": "textbox", "name": "Email", "value": "a@b.c"},
		map[string]any{"action": "click", "role": "button", "name": "Sign in"},
		map[string]any{"action": "press", "key": "Enter"},
	})
	if err != nil {
		t.Fatalf("valid steps rejected: %v", err)
	}
	if len(steps) != 3 || steps[0].Action != "type" || steps[2].Key != "Enter" {
		t.Fatalf("parsed steps = %+v", steps)
	}

	bad := []struct {
		name  string
		value any
	}{
		{"empty", []any{}},
		{"nil", nil},
		{"press without key", []any{map[string]any{"action": "press"}}},
		{"click without target", []any{map[string]any{"action": "click"}}},
		{"select without values", []any{map[string]any{"action": "select", "role": "combobox", "name": "Country"}}},
		{"unknown action", []any{map[string]any{"action": "scroll"}}},
		{"unknown field", []any{map[string]any{"action": "click", "role": "button", "bogus": 1}}},
	}
	for _, tc := range bad {
		if _, err := projectAssistantPreviewInteractionSteps(tc.value); err == nil {
			t.Fatalf("%s: expected validation error", tc.name)
		}
	}
}

func TestProjectAssistantPreviewInteractionStepsCap(t *testing.T) {
	many := make([]any, projectAssistantPreviewInteractionMaxSteps+1)
	for i := range many {
		many[i] = map[string]any{"action": "press", "key": "Tab"}
	}
	_, err := projectAssistantPreviewInteractionSteps(many)
	if err == nil || !strings.Contains(err.Error(), "at most") {
		t.Fatalf("step cap not enforced: %v", err)
	}
}
