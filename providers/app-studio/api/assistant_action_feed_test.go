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

	"github.com/faroshq/provider-app-studio/store"
	"github.com/faroshq/provider-app-studio/workspace"
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

func TestProjectAssistantActionFeedPreservesSkippedRead(t *testing.T) {
	item := projectAssistantActionFeedItemFromToolCall(projectToolCallStreamEvent{
		ID:        "read-skipped",
		Name:      projectToolReadFile,
		Status:    "skipped",
		Arguments: "path src/App.vue",
		Summary:   "Skipped an unchanged duplicate read.",
	})
	if item.Status != projectAssistantActionFeedStatusSkipped ||
		item.Title != "Skipped duplicate read" ||
		item.Severity != projectAssistantActionFeedSeverityNormal {
		t.Fatalf("skipped read item = %#v", item)
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

func TestFinalizeProjectAssistantActionFeedClosesOutstandingActions(t *testing.T) {
	waiting := []projectAssistantActionFeedItem{{
		ID:         "action-1",
		Kind:       projectAssistantActionFeedItemRun,
		Status:     projectAssistantActionFeedStatusWaiting,
		Title:      "Restarting development runtime",
		Severity:   projectAssistantActionFeedSeverityAttention,
		Diagnostic: projectAssistantActionFeedDiagnostic("action-1", "old failure"),
	}}
	completed := finalizeProjectAssistantActionFeed(append([]projectAssistantActionFeedItem(nil), waiting...), store.AssistantRunStatusCompleted)
	if len(completed) != 1 || completed[0].Status != projectAssistantActionFeedStatusSucceeded || completed[0].Title != "Ran checks" || completed[0].Severity != projectAssistantActionFeedSeverityNormal || completed[0].Diagnostic != nil {
		t.Fatalf("completed actions = %#v, want a closed successful action", completed)
	}
	failed := finalizeProjectAssistantActionFeed(append([]projectAssistantActionFeedItem(nil), waiting...), store.AssistantRunStatusFailed)
	if len(failed) != 1 || failed[0].Status != projectAssistantActionFeedStatusFailed || failed[0].Title != "Run failed" || failed[0].Severity != projectAssistantActionFeedSeverityError || failed[0].Diagnostic == nil {
		t.Fatalf("failed actions = %#v, want a closed failed action", failed)
	}
}

func TestProjectAssistantResumeToolCallDoesNotUseUnrelatedFallback(t *testing.T) {
	events := []projectToolCallStreamEvent{{
		ID:     "later-preview-call",
		Name:   projectToolInspectDevelopmentPreview,
		Status: "failed",
	}}
	if got := projectAssistantResumeToolCall(events, "approved-restart-call"); got != nil {
		t.Fatalf("resume tool call = %#v, want no unrelated fallback", got)
	}
	if got := projectAssistantResumeToolNameWithFallback(nil, projectToolRestartRuntime); got != projectToolRestartRuntime {
		t.Fatalf("resume tool name = %q, want checkpoint tool %q", got, projectToolRestartRuntime)
	}
}

func TestProjectAssistantCheckpointToolIdentityFallsBackToCurrentToolCall(t *testing.T) {
	state := projectAssistantCheckpointState{
		CurrentIndex: 0,
		ToolCalls: []chatToolCall{{
			ID: "approved-restart-call",
			Function: chatToolCallFunction{
				Name: projectToolRestartRuntime,
			},
		}},
		Eino: &projectAssistantEinoCheckpointState{},
	}
	if got := projectAssistantCheckpointToolCallID(state); got != "approved-restart-call" {
		t.Fatalf("checkpoint tool call ID = %q, want generic checkpoint ID", got)
	}
	if got := projectAssistantCheckpointToolName(state); got != projectToolRestartRuntime {
		t.Fatalf("checkpoint tool name = %q, want %q", got, projectToolRestartRuntime)
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

func TestProjectAssistantActionDiagnosticClassifiesReplanAsPermission(t *testing.T) {
	for _, failure := range []string{
		"plan approval required: requested write is outside the active approved plan",
		"initial execution plan revision required: requested write is outside the active target paths",
	} {
		if got := projectAssistantActionDiagnosticCategory(failure); got != "permission" {
			t.Fatalf("diagnostic category for %q = %q, want permission", failure, got)
		}
	}
}

func TestProjectAssistantActionDiagnosticExplainsPatchRecoveryWithoutLeakingInput(t *testing.T) {
	diagnostic := projectAssistantActionFeedDiagnostic(
		"patch-call",
		string(workspace.PatchErrorStrategyChange)+": secret source fragment",
	)
	if diagnostic == nil || diagnostic.Category != "validation" || !strings.Contains(diagnostic.Message, "must be revised") {
		t.Fatalf("diagnostic = %#v", diagnostic)
	}
	if strings.Contains(diagnostic.Message, "secret source fragment") {
		t.Fatalf("diagnostic leaked raw input: %#v", diagnostic)
	}
}

func TestProjectAssistantActionDiagnosticClassifiesPatchContextFailures(t *testing.T) {
	for _, failure := range []string{
		string(workspace.PatchErrorContextNotFound) + ` path="src/App.jsx"`,
		string(workspace.PatchErrorContextAmbiguous) + ` path="src/App.jsx"`,
	} {
		if got := projectAssistantActionDiagnosticCategory(failure); got != "validation" {
			t.Fatalf("diagnostic category for %q = %q, want validation", failure, got)
		}
	}
}

func TestProjectAssistantActionFeedExplainsTypedPreviewFailure(t *testing.T) {
	item := projectAssistantActionFeedItemFromToolCall(projectToolCallStreamEvent{
		ID:     "inspect-call",
		Name:   projectToolInspectDevelopmentPreview,
		Status: "failed",
	})
	if item.Diagnostic == nil || item.Diagnostic.Category != "runtime" ||
		!strings.Contains(item.Diagnostic.Message, "did not verify the preview") {
		t.Fatalf("diagnostic = %#v", item.Diagnostic)
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
		Name:      projectToolApplyPatch,
		Status:    "succeeded",
		Arguments: "path src/App.vue; 42 bytes",
		Summary:   "file updated",
	})
	if item.Title != "Edited files" || item.Target != "" || item.Outcome != "" || item.GroupKey != "" {
		t.Fatalf("minimal item = %#v, want only generic presentation", item)
	}
}
