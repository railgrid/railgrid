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
	"reflect"
	"sort"
	"strings"
	"testing"
)

func TestProjectEinoAssistantToolSearchMatchesSeveralCapabilities(t *testing.T) {
	specs := []projectAssistantToolSpec{
		{Name: "browser_snapshot", Description: "Capture an accessibility snapshot of the preview page."},
		{Name: "browser_console_messages", Description: "Return browser console messages."},
		{Name: "browser_network_requests", Description: "Return network requests from the browser preview."},
		{Name: "browser_take_screenshot", Description: "Take a screenshot of the preview."},
		{Name: "browser_click", Description: "Click an element in the browser."},
		{Name: "provider_database_query", Description: "Read matching database records."},
	}
	query := "native browser Playwright preview snapshot console messages network requests current project"
	matches := projectEinoAssistantSearchDynamicToolSpecs(specs, query, 5)
	names := make([]string, 0, len(matches))
	for _, match := range matches {
		names = append(names, match.Name)
	}
	sort.Strings(names)
	want := []string{"browser_console_messages", "browser_network_requests", "browser_snapshot"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("capability matches = %v, want %v", names, want)
	}
}

func TestProjectEinoAssistantToolSearchRankingKeepsExactNamesFirst(t *testing.T) {
	specs := []projectAssistantToolSpec{
		{Name: "browser_snapshot_helper", Description: "A snapshot console network helper."},
		{Name: "browser_snapshot", Description: "Capture an accessibility snapshot."},
		{Name: "browser_console_messages", Description: "Return console messages."},
	}
	matches := projectEinoAssistantSearchDynamicToolSpecs(specs, "  SELECT: browser_snapshot  ", 1)
	if len(matches) != 1 || matches[0].Name != "browser_snapshot" {
		t.Fatalf("exact selection = %#v", matches)
	}
	matches = projectEinoAssistantSearchDynamicToolSpecs(specs, "find browser_snapshot with console messages", 5)
	if len(matches) == 0 || matches[0].Name != "browser_snapshot" {
		t.Fatalf("exact name ranked below partial words: %#v", matches)
	}
}

func TestProjectEinoAssistantToolSearchRankingIgnoresGenericWordsForUnknownCapabilities(t *testing.T) {
	specs := []projectAssistantToolSpec{{Name: "browser_snapshot", Description: "Inspect the current project browser preview."}}
	for _, query := range []string{"", " ", "the current project tools", "native browser nonexistent_capability"} {
		if matches := projectEinoAssistantSearchDynamicToolSpecs(specs, query, 5); len(matches) != 0 {
			t.Errorf("query %q unexpectedly matches %#v", query, matches)
		}
	}
	if matches := projectEinoAssistantSearchDynamicToolSpecs(specs, "browser", 5); len(matches) != 1 {
		t.Fatalf("category search lost browser capability: %#v", matches)
	}
}

func TestProjectEinoAssistantToolSearchRankingUsesUniqueTermsAndNames(t *testing.T) {
	specs := []projectAssistantToolSpec{
		{Name: "browser_snapshot", Description: strings.Repeat("雪", 250), Risk: projectAssistantToolRiskRead},
		{Name: "BROWSER_SNAPSHOT", Description: "Duplicate name."},
		{Name: "browser_console_messages", Description: "Return console messages."},
	}
	one := projectEinoAssistantToolSearchTermScore("snapshot console messages", "browser_snapshot", "", "")
	repeated := projectEinoAssistantToolSearchTermScore("snapshot snapshot snapshot console messages", "browser_snapshot", "", "")
	if one != repeated {
		t.Fatalf("repeated words inflate rank: %d vs %d", one, repeated)
	}
	matches := projectEinoAssistantSearchDynamicToolSpecs(specs, "browser_snapshot", 1)
	if len(matches) != 1 || matches[0].Risk != string(projectAssistantToolRiskRead) {
		t.Fatalf("duplicate consumed search quota or replaced the first contract: %#v", matches)
	}
	if len([]rune(matches[0].Summary)) > projectEinoAssistantToolSearchSummaryMaxRunes {
		t.Fatal("Unicode summary exceeds its bound")
	}
}
