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
	"encoding/json"
	"strings"
	"testing"
)

func TestProjectAssistantActionFeedReadHidesExecutionMechanics(t *testing.T) {
	item := projectAssistantActionFeedItemFromToolCall(projectToolCallStreamEvent{
		ID:        "read-1",
		Name:      projectToolReadFile,
		Status:    "succeeded",
		Arguments: "path src/App.vue; offset 120; limit 200",
		Summary:   "file read",
	})
	if item.Title != "Read file" || item.Target != "src/App.vue" || item.Status != projectAssistantActionFeedStatusSucceeded {
		t.Fatalf("item = %#v, want a completed user-facing file read", item)
	}
	data, err := json.Marshal(item)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"read_file", "offset", "limit", "120", "200"} {
		if strings.Contains(string(data), forbidden) {
			t.Fatalf("item JSON = %s, must not contain %q", data, forbidden)
		}
	}
}

func TestProjectAssistantActionFeedSuppressesTodosAndFailsClosed(t *testing.T) {
	feed := projectAssistantActionFeedFromToolCalls([]projectToolCallStreamEvent{
		{ID: "todo-1", Name: projectEinoAssistantWriteTodosTool, Status: "succeeded", Arguments: `{"todos":[{"content":"secret"}]}`},
		{ID: "unknown-1", Name: "provider__internal_tool", Status: "succeeded", Arguments: `{"token":"secret"}`, Summary: "secret result"},
		{ID: "unknown-2", Name: "provider__failing_tool", Status: "failed", Error: "secret provider failure"},
	})
	if len(feed) != 1 || feed[0].Status != projectAssistantActionFeedStatusFailed ||
		feed[0].Title != "Action failed" || feed[0].Diagnostic == nil {
		t.Fatalf("feed = %#v, want only the failed unknown action", feed)
	}
	data, err := json.Marshal(feed)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "secret") || strings.Contains(string(data), "internal_tool") ||
		strings.Contains(string(data), "failing_tool") || strings.Contains(string(data), "write_todos") {
		t.Fatalf("feed JSON leaked internal data: %s", data)
	}
}

func TestApplyProjectAssistantActionFeedUpdateRemovesInvisibleTerminalAction(t *testing.T) {
	actions := []projectAssistantActionFeedItem{{
		ID:     "unknown-1",
		Kind:   projectAssistantActionFeedItemOther,
		Status: projectAssistantActionFeedStatusWaiting,
		Title:  "Waiting for action",
	}}
	actions = applyProjectAssistantActionFeedUpdate(actions, projectAssistantActionFeedItem{
		ID:     "unknown-1",
		Kind:   projectAssistantActionFeedItemOther,
		Status: projectAssistantActionFeedStatusSucceeded,
		Title:  "Completed action",
	})
	if len(actions) != 0 {
		t.Fatalf("actions = %#v, want terminal unknown action removed", actions)
	}
}

func TestProjectAssistantActionFeedUsesAllowlistedDiagnostics(t *testing.T) {
	item := projectAssistantActionFeedItemFromToolCall(projectToolCallStreamEvent{
		ID:     "tool-with-secret-id-token",
		Name:   projectToolVerifyDevelopmentRuntime,
		Status: "failed",
		Error:  "preview timed out with bearer secret-token",
	})
	if item.Status != projectAssistantActionFeedStatusFailed || item.Severity != projectAssistantActionFeedSeverityError ||
		item.Diagnostic == nil || item.Diagnostic.Category != "timeout" {
		t.Fatalf("item = %#v, want failed timeout diagnostic", item)
	}
	data, err := json.Marshal(item)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "bearer") || strings.Contains(string(data), "secret-token") ||
		strings.Contains(item.ID, "secret") || strings.Contains(item.Diagnostic.ReferenceID, "secret") ||
		strings.Contains(string(data), "tool-with-secret-id-token") {
		t.Fatalf("diagnostic leaked raw failure data: %s", data)
	}
}

func TestProjectAssistantActionPublicIDIsStableAndRejectsEmptyInput(t *testing.T) {
	first := projectAssistantActionPublicID("provider-call-1")
	if first == "" || first != projectAssistantActionPublicID("provider-call-1") || first == "provider-call-1" {
		t.Fatalf("public ID = %q, want stable pseudonymous value", first)
	}
	if got := projectAssistantActionPublicID(" "); got != "" {
		t.Fatalf("empty public ID = %q, want empty", got)
	}
}

func TestProjectAssistantActionFeedMinimalDisclosureHidesTargetAndOutcome(t *testing.T) {
	previous := projectAssistantToolDisclosureMinimal
	projectAssistantToolDisclosureMinimal = true
	t.Cleanup(func() { projectAssistantToolDisclosureMinimal = previous })

	item := projectAssistantActionFeedItemFromToolCall(projectToolCallStreamEvent{
		ID:        "write-1",
		Name:      projectToolWriteFile,
		Status:    "succeeded",
		Arguments: "path src/App.vue; 42 bytes",
		Summary:   "file updated",
	})
	if item.Title != "Edited files" || item.Target != "" || item.Outcome != "" || item.GroupKey != "" {
		t.Fatalf("minimal item = %#v, want only generic presentation", item)
	}
}
