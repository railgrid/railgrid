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

// A representative Playwright MCP browser_snapshot result: the "Page URL" /
// "Page Title" preamble plus a fenced YAML accessibility tree.
const browserMCPSampleSnapshot = "- Page URL: https://demo.preview.example/admin\n" +
	"- Page Title: Vireo Admin\n" +
	"- Page Snapshot:\n" +
	"```yaml\n" +
	"- generic [ref=e1]:\n" +
	"  - heading \"Admin Login\" [level=1] [ref=e2]\n" +
	"  - textbox \"Email\" [ref=e3]\n" +
	"  - textbox \"Password\" [ref=e4]\n" +
	"  - button \"Sign in\" [ref=e5]\n" +
	"  - link \"Back to store\" [ref=e6]\n" +
	"```\n"

func TestBrowserMCPParseFieldsAndTree(t *testing.T) {
	if got := browserMCPParseField(browserMCPSampleSnapshot, "Page URL", "fallback"); got != "https://demo.preview.example/admin" {
		t.Fatalf("Page URL = %q", got)
	}
	if got := browserMCPParseField(browserMCPSampleSnapshot, "Page Title", ""); got != "Vireo Admin" {
		t.Fatalf("Page Title = %q", got)
	}
	if got := browserMCPParseField(browserMCPSampleSnapshot, "Absent", "fallback"); got != "fallback" {
		t.Fatalf("missing field should fall back, got %q", got)
	}
	tree := browserMCPExtractSnapshotTree(browserMCPSampleSnapshot)
	if want := "- generic [ref=e1]:"; tree == "" || tree[:len(want)] != want {
		t.Fatalf("extracted tree = %q", tree)
	}
	nodes := browserMCPParseAccessibilityNodes(tree)
	if len(nodes) != 6 {
		t.Fatalf("parsed %d nodes, want 6: %+v", len(nodes), nodes)
	}
	if nodes[1].role != "heading" || nodes[1].name != "Admin Login" {
		t.Fatalf("node[1] = %+v", nodes[1])
	}
}

func TestBrowserMCPEvaluateAssertion(t *testing.T) {
	tree := browserMCPExtractSnapshotTree(browserMCPSampleSnapshot)
	nodes := browserMCPParseAccessibilityNodes(tree)
	intp := func(n int) *int { return &n }

	cases := []struct {
		name      string
		assertion projectAssistantPreviewInspectionAssertion
		want      bool
		wantCount int
	}{
		{"text present in a name", projectAssistantPreviewInspectionAssertion{Kind: "text_present", Text: "Admin Login"}, true, 1},
		{"text absent", projectAssistantPreviewInspectionAssertion{Kind: "text_present", Text: "Checkout"}, false, 0},
		{"role present", projectAssistantPreviewInspectionAssertion{Kind: "role_present", Role: "button"}, true, 1},
		{"role present by name", projectAssistantPreviewInspectionAssertion{Kind: "role_present", Role: "button", Name: "Sign in"}, true, 1},
		{"role present missing", projectAssistantPreviewInspectionAssertion{Kind: "role_present", Role: "checkbox"}, false, 0},
		{"role count in range", projectAssistantPreviewInspectionAssertion{Kind: "role_count", Role: "textbox", Min: intp(2), Max: intp(2)}, true, 2},
		{"role count under min", projectAssistantPreviewInspectionAssertion{Kind: "role_count", Role: "textbox", Min: intp(3)}, false, 2},
		{"role count over max", projectAssistantPreviewInspectionAssertion{Kind: "role_count", Role: "textbox", Max: intp(1)}, false, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := browserMCPEvaluateAssertion(tc.assertion, tree, nodes)
			if got.Passed != tc.want {
				t.Fatalf("Passed = %v, want %v (message %q)", got.Passed, tc.want, got.Message)
			}
			if got.ActualCount == nil || *got.ActualCount != tc.wantCount {
				t.Fatalf("ActualCount = %v, want %d", got.ActualCount, tc.wantCount)
			}
		})
	}
}

func TestBrowserMCPTextPresentFallsBackToRawSnapshot(t *testing.T) {
	// "Back to store" is a link name; a substring only present in raw copy still
	// counts via the snapshot-containment fallback (non-exact).
	tree := browserMCPExtractSnapshotTree(browserMCPSampleSnapshot)
	nodes := browserMCPParseAccessibilityNodes(tree)
	got := browserMCPEvaluateAssertion(projectAssistantPreviewInspectionAssertion{Kind: "text_present", Text: "ref=e1"}, tree, nodes)
	if !got.Passed {
		t.Fatalf("raw-snapshot containment should pass, message %q", got.Message)
	}
}

func TestBrowserMCPParseConsole(t *testing.T) {
	events := browserMCPParseConsole("[LOG] booted\n[ERROR] boom\nplain line\n")
	if len(events) != 3 {
		t.Fatalf("parsed %d events, want 3: %+v", len(events), events)
	}
	if events[0].Level != "log" || events[0].Message != "booted" {
		t.Fatalf("event[0] = %+v", events[0])
	}
	if events[1].Level != "error" || events[1].Message != "boom" {
		t.Fatalf("event[1] = %+v", events[1])
	}
	if events[2].Level != "log" || events[2].Message != "plain line" {
		t.Fatalf("event[2] = %+v", events[2])
	}
}

func TestBrowserMCPNavigationSummarySkipsHeadingsAndKeepsError(t *testing.T) {
	text := "### Result\n### Error\nError: page.goto: net::ERR_CONNECTION_REFUSED at https://preview.example/" + strings.Repeat(" details", 80)
	got := browserMCPNavigationSummary(text, "fallback")
	if strings.HasPrefix(got, "#") || strings.HasPrefix(got, "Result") {
		t.Fatalf("navigation summary retained Markdown scaffolding: %q", got)
	}
	if !strings.Contains(got, "ERR_CONNECTION_REFUSED") {
		t.Fatalf("navigation summary lost substantive error: %q", got)
	}
	if len([]rune(got)) > browserMCPNavigationSummaryMaxChars {
		t.Fatalf("navigation summary length = %d, want <= %d", len([]rune(got)), browserMCPNavigationSummaryMaxChars)
	}
}

func TestBrowserMCPNavigationSummaryFallsBackWhenResponseHasNoDetail(t *testing.T) {
	if got := browserMCPNavigationSummary("### Result\n### Error\n", "the preview did not load"); got != "the preview did not load" {
		t.Fatalf("heading-only navigation summary = %q", got)
	}
}
