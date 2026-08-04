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

func TestProjectAssistantActionDiagnosticExplainsMutationRecoveryWithoutLeakingInput(t *testing.T) {
	diagnostic := projectAssistantActionFeedDiagnostic(
		"edit-call",
		string(workspace.MutationErrorStale)+": secret source fragment",
	)
	if diagnostic == nil || diagnostic.Category != "validation" || !strings.Contains(diagnostic.Message, "reread") {
		t.Fatalf("diagnostic = %#v", diagnostic)
	}
	if strings.Contains(diagnostic.Message, "secret source fragment") {
		t.Fatalf("diagnostic leaked raw input: %#v", diagnostic)
	}
}

func TestProjectAssistantActionDiagnosticClassifiesMutationContextFailures(t *testing.T) {
	for _, failure := range []string{
		string(workspace.MutationErrorStale) + ` path="src/App.jsx"`,
		string(workspace.MutationErrorAmbiguous) + ` path="src/App.jsx"`,
	} {
		if got := projectAssistantActionDiagnosticCategory(failure); got != "validation" {
			t.Fatalf("diagnostic category for %q = %q, want validation", failure, got)
		}
	}
}

func TestProjectAssistantMutationRecoveryFailureRestoresPriorAndSeparatesGuidance(t *testing.T) {
	prior := projectAssistantActionFeedItem{
		ID:       "prior-mutation",
		Kind:     projectAssistantActionFeedItemEdit,
		Status:   projectAssistantActionFeedStatusFailed,
		Severity: projectAssistantActionFeedSeverityError,
		Title:    "File update failed",
		Diagnostic: &projectAssistantActionDiagnostic{
			Category:    "validation",
			Message:     "The create target already exists.",
			ReferenceID: "action-prior",
			Code:        string(workspace.MutationErrorTargetExists),
			Operation:   projectToolCreateFile,
			Path:        "src/App.vue",
			Guidance:    "Read the complete file, then retry with replace_file.",
		},
	}
	retry := projectAssistantActionFeedItem{
		ID:         "retry-mutation",
		Kind:       projectAssistantActionFeedItemEdit,
		Status:     projectAssistantActionFeedStatusRunning,
		Severity:   projectAssistantActionFeedSeverityNormal,
		RecoveryOf: prior.ID,
	}
	actions := reconcileProjectAssistantMutationRecovery([]projectAssistantActionFeedItem{prior, retry})
	if actions[0].Status != projectAssistantActionFeedStatusRetrying {
		t.Fatalf("prior after running retry = %#v, want retrying", actions[0])
	}

	retry.Status = projectAssistantActionFeedStatusRejected
	retry.Severity = projectAssistantActionFeedSeverityError
	actions = reconcileProjectAssistantMutationRecovery([]projectAssistantActionFeedItem{actions[0], retry})
	if actions[0].Status != projectAssistantActionFeedStatusFailed || actions[0].Severity != projectAssistantActionFeedSeverityError {
		t.Fatalf("prior after rejected retry = %#v, want failed/error", actions[0])
	}
	if actions[0].Diagnostic == nil || actions[0].Diagnostic.ReferenceID != "action-prior" ||
		actions[0].Diagnostic.Message == actions[0].Diagnostic.Guidance {
		t.Fatalf("prior diagnostic = %#v, want original distinct cause and guidance", actions[0].Diagnostic)
	}
	if actions[1].Status != projectAssistantActionFeedStatusRejected || actions[1].ID == actions[0].ID {
		t.Fatalf("retry action = %#v, want distinct rejected attempt", actions[1])
	}

	actions[0].Status = projectAssistantActionFeedStatusRetrying
	final := finalizeProjectAssistantActionFeed(actions, store.AssistantRunStatusCompleted)
	if final[0].Status == projectAssistantActionFeedStatusRetrying || final[0].Status != projectAssistantActionFeedStatusFailed ||
		final[0].Diagnostic == nil || final[0].Diagnostic.ReferenceID != "action-prior" {
		t.Fatalf("terminal actions = %#v, want prior failed with preserved diagnostic", final)
	}
}

func TestApplyProjectAssistantActionFeedUpdateClosesLinkedRetryOnFailure(t *testing.T) {
	for _, terminal := range []string{projectAssistantActionFeedStatusFailed, projectAssistantActionFeedStatusRejected} {
		t.Run(terminal, func(t *testing.T) {
			prior := projectAssistantActionFeedItem{
				ID:       "prior-" + terminal,
				Kind:     projectAssistantActionFeedItemEdit,
				Status:   projectAssistantActionFeedStatusFailed,
				Title:    "File update failed",
				Severity: projectAssistantActionFeedSeverityError,
				Diagnostic: &projectAssistantActionDiagnostic{
					Category:    "validation",
					Message:     "The source is stale.",
					ReferenceID: "action-" + terminal,
					Code:        string(workspace.MutationErrorStale),
					Operation:   projectToolEditFile,
					Path:        "src/App.vue",
					Guidance:    "Read the complete current file and retry.",
				},
			}
			actions := applyProjectAssistantActionFeedUpdate(nil, prior)
			actions = applyProjectAssistantActionFeedUpdate(actions, projectAssistantActionFeedItem{
				ID:         "retry-" + terminal,
				Kind:       projectAssistantActionFeedItemEdit,
				Status:     projectAssistantActionFeedStatusRunning,
				Title:      "Editing files",
				Severity:   projectAssistantActionFeedSeverityNormal,
				RecoveryOf: prior.ID,
			})
			if len(actions) != 2 || actions[0].Status != projectAssistantActionFeedStatusRetrying {
				t.Fatalf("running linked retry actions = %#v, want prior retrying plus retry", actions)
			}

			actions = applyProjectAssistantActionFeedUpdate(actions, projectAssistantActionFeedItem{
				ID:         "retry-" + terminal,
				Kind:       projectAssistantActionFeedItemEdit,
				Status:     terminal,
				Title:      "File update failed",
				Severity:   projectAssistantActionFeedSeverityError,
				RecoveryOf: prior.ID,
			})
			if len(actions) != 2 || actions[0].Status != projectAssistantActionFeedStatusFailed ||
				actions[0].Severity != projectAssistantActionFeedSeverityError || actions[0].Title != "Edit failed" {
				t.Fatalf("terminal linked retry prior = %#v, want failed/error and not retrying", actions[0])
			}
			if actions[0].Diagnostic == nil || actions[0].Diagnostic.ReferenceID != "action-"+terminal {
				t.Fatalf("terminal linked retry prior diagnostic = %#v, want original diagnostic", actions[0].Diagnostic)
			}
			if actions[1].Status != terminal || actions[1].RecoveryOf != prior.ID {
				t.Fatalf("terminal linked retry action = %#v, want %s linked to prior", actions[1], terminal)
			}
		})
	}
}

func TestProjectAssistantMutationRecoveryRejectsInvalidAndUnlinkedReferences(t *testing.T) {
	prior := projectAssistantActionFeedItem{
		ID:       "prior-unlinked",
		Kind:     projectAssistantActionFeedItemEdit,
		Status:   projectAssistantActionFeedStatusFailed,
		Title:    "Edit failed",
		Target:   "src/App.vue",
		Severity: projectAssistantActionFeedSeverityError,
	}
	missing := projectAssistantActionFeedItem{
		ID:         "retry-missing",
		Kind:       projectAssistantActionFeedItemEdit,
		Status:     projectAssistantActionFeedStatusRunning,
		Title:      "Retrying file update",
		Target:     "src/App.vue",
		Severity:   projectAssistantActionFeedSeverityAttention,
		RecoveryOf: "not-in-feed",
	}
	self := projectAssistantActionFeedItem{
		ID:         "retry-self",
		Kind:       projectAssistantActionFeedItemEdit,
		Status:     projectAssistantActionFeedStatusRunning,
		Title:      "Retrying file update",
		Target:     "src/App.vue",
		Severity:   projectAssistantActionFeedSeverityAttention,
		RecoveryOf: "retry-self",
	}
	pathOnly := projectAssistantActionFeedItem{
		ID:       "retry-path-only",
		Kind:     projectAssistantActionFeedItemEdit,
		Status:   projectAssistantActionFeedStatusRunning,
		Title:    "Editing files",
		Target:   "src/App.vue",
		Severity: projectAssistantActionFeedSeverityAttention,
	}
	actions := reconcileProjectAssistantMutationRecovery([]projectAssistantActionFeedItem{prior, missing, self, pathOnly})
	if actions[0].Status != projectAssistantActionFeedStatusFailed {
		t.Fatalf("unlinked prior status = %q, want failed", actions[0].Status)
	}
	for _, index := range []int{1, 2, 3} {
		if actions[index].RecoveryOf != "" {
			t.Fatalf("unlinked action %d retained recoveryOf %q", index, actions[index].RecoveryOf)
		}
		if actions[index].Status != projectAssistantActionFeedStatusRunning {
			t.Fatalf("unlinked action %d status = %q, want running", index, actions[index].Status)
		}
	}
}

func TestProjectAssistantMutationRecoveryRequiresCompatibleIdentity(t *testing.T) {
	state := newProjectEinoAssistantRunState()
	createArgs := map[string]any{"path": "./src/App.vue", "content": "new"}
	createRef := state.RecordMutationRecoveryReferenceForMutation("failed-create", projectToolCreateFile, createArgs)
	if createRef == "" {
		t.Fatal("create recovery reference is empty")
	}
	for _, tc := range []struct {
		name string
		tool string
		args map[string]any
		want bool
	}{
		{name: "same create", tool: projectToolCreateFile, args: map[string]any{"path": "src/App.vue", "recoveryOf": createRef}, want: true},
		{name: "create to replace repair", tool: projectToolReplaceFile, args: map[string]any{"path": "src/App.vue", "expectedVersion": "sha256:current", "content": "new", "recoveryOf": createRef}, want: true},
		{name: "create to edit repair", tool: projectToolEditFile, args: map[string]any{"path": "src/App.vue", "oldString": "old", "newString": "new", "expectedVersion": "sha256:current", "recoveryOf": createRef}, want: true},
		{name: "wrong path", tool: projectToolCreateFile, args: map[string]any{"path": "src/Other.vue", "content": "new", "recoveryOf": createRef}, want: false},
		{name: "incompatible delete", tool: projectToolDeleteFile, args: map[string]any{"path": "src/App.vue", "expectedVersion": "sha256:current", "recoveryOf": createRef}, want: false},
	} {
		want := ""
		if tc.want {
			want = createRef
		}
		if got := projectAssistantValidatedMutationRecoveryOf(state, tc.args, tc.tool); got != want {
			t.Fatalf("%s recovery = %q, want compatible=%t", tc.name, got, tc.want)
		}
	}

	moveArgs := map[string]any{"sourcePath": "src/App.vue", "destinationPath": "src/Renamed.vue"}
	moveRef := state.RecordMutationRecoveryReferenceForMutation("failed-move", projectToolMoveFile, moveArgs)
	if moveRef == "" {
		t.Fatal("move recovery reference is empty")
	}
	if got := projectAssistantValidatedMutationRecoveryOf(state, map[string]any{
		"sourcePath":      "./src/App.vue",
		"destinationPath": "src/Other.vue",
		"recoveryOf":      moveRef,
	}, projectToolMoveFile); got != moveRef {
		t.Fatalf("move recovery = %q, want same-source move reference", got)
	}
	if got := projectAssistantValidatedMutationRecoveryOf(state, map[string]any{
		"sourcePath":      "src/Other.vue",
		"destinationPath": "src/Renamed.vue",
		"recoveryOf":      moveRef,
	}, projectToolMoveFile); got != "" {
		t.Fatalf("different-source move recovery = %q, want rejected", got)
	}

	checkpoint := state.CheckpointState()
	identity, ok := checkpoint.MutationRecoveryIdentities[createRef]
	if !ok || identity.Operation != "create" || identity.Target != "src/App.vue" {
		t.Fatalf("checkpoint create identity = %#v, want canonical create identity", checkpoint.MutationRecoveryIdentities)
	}
	restored := newProjectEinoAssistantRunState()
	restored.RestoreCheckpointState(checkpoint)
	if got := projectAssistantValidatedMutationRecoveryOf(restored, map[string]any{
		"path":       "src/App.vue",
		"content":    "new",
		"recoveryOf": createRef,
	}, projectToolCreateFile); got != createRef {
		t.Fatalf("restored recovery = %q, want %q", got, createRef)
	}
	stripped := projectAssistantAttachMutationRecoveryOf(
		projectToolCreateFile,
		`{"operation":"create_file","status":"succeeded","recoveryOf":"forged"}`,
		"",
	)
	if strings.Contains(stripped, "forged") {
		t.Fatalf("unvalidated result retained recovery reference: %s", stripped)
	}
}

func TestProjectAssistantMutationRecoveryIdentitySurvivesCheckpointRestart(t *testing.T) {
	state := newProjectEinoAssistantRunState()
	cases := []struct {
		name       string
		tool       string
		args       map[string]any
		wantFamily string
		wantTarget string
		valid      []struct {
			tool string
			args map[string]any
		}
		invalid struct {
			tool string
			args map[string]any
		}
	}{
		{
			name:       "create",
			tool:       projectToolCreateFile,
			args:       map[string]any{"path": "./src/Create.vue", "content": "new"},
			wantFamily: "create",
			wantTarget: "src/Create.vue",
			valid: []struct {
				tool string
				args map[string]any
			}{
				{tool: projectToolCreateFile, args: map[string]any{"path": "src/Create.vue"}},
				{tool: projectToolReplaceFile, args: map[string]any{"path": "src/Create.vue"}},
				{tool: projectToolEditFile, args: map[string]any{"path": "src/Create.vue"}},
			},
			invalid: struct {
				tool string
				args map[string]any
			}{tool: projectToolDeleteFile, args: map[string]any{"path": "src/Create.vue"}},
		},
		{
			name:       "edit",
			tool:       projectToolEditFile,
			args:       map[string]any{"path": "src/Edit.vue", "oldString": "old", "newString": "new"},
			wantFamily: "edit",
			wantTarget: "src/Edit.vue",
			valid: []struct {
				tool string
				args map[string]any
			}{
				{tool: projectToolEditFile, args: map[string]any{"path": "src/Edit.vue"}},
				{tool: projectToolReplaceFile, args: map[string]any{"path": "src/Edit.vue"}},
			},
			invalid: struct {
				tool string
				args map[string]any
			}{tool: projectToolCreateFile, args: map[string]any{"path": "src/Edit.vue"}},
		},
		{
			name:       "delete",
			tool:       projectToolDeleteFile,
			args:       map[string]any{"path": "src/Delete.vue"},
			wantFamily: "delete",
			wantTarget: "src/Delete.vue",
			valid: []struct {
				tool string
				args map[string]any
			}{
				{tool: projectToolDeleteFile, args: map[string]any{"path": "src/Delete.vue"}},
			},
			invalid: struct {
				tool string
				args map[string]any
			}{tool: projectToolEditFile, args: map[string]any{"path": "src/Delete.vue"}},
		},
		{
			name:       "move",
			tool:       projectToolMoveFile,
			args:       map[string]any{"sourcePath": "./src/Move.vue", "destinationPath": "src/Renamed.vue"},
			wantFamily: "move",
			wantTarget: "src/Move.vue",
			valid: []struct {
				tool string
				args map[string]any
			}{
				{tool: projectToolMoveFile, args: map[string]any{"sourcePath": "src/Move.vue", "destinationPath": "src/Other.vue"}},
			},
			invalid: struct {
				tool string
				args map[string]any
			}{tool: projectToolMoveFile, args: map[string]any{"sourcePath": "src/Other.vue", "destinationPath": "src/Renamed.vue"}},
		},
	}

	refs := make(map[string]string, len(cases))
	for _, tc := range cases {
		ref := state.RecordMutationRecoveryReferenceForMutation("failed-"+tc.name, tc.tool, tc.args)
		if ref == "" {
			t.Fatalf("%s recovery reference is empty", tc.name)
		}
		refs[tc.name] = ref
	}
	checkpoint := state.CheckpointState()
	checkpoint.MutationRecoveryRefs = append(checkpoint.MutationRecoveryRefs, "legacy-ref-without-identity")
	for _, tc := range cases {
		identity, ok := checkpoint.MutationRecoveryIdentities[refs[tc.name]]
		if !ok || identity.Operation != tc.wantFamily || identity.Target != tc.wantTarget {
			t.Fatalf("%s checkpoint identity = %#v, want family=%q target=%q", tc.name, identity, tc.wantFamily, tc.wantTarget)
		}
	}

	restarted := newProjectEinoAssistantRunState()
	restarted.RestoreCheckpointState(checkpoint)
	if got := projectAssistantValidatedMutationRecoveryOf(restarted, map[string]any{
		"path":       "src/Create.vue",
		"recoveryOf": "legacy-ref-without-identity",
	}, projectToolCreateFile); got != "" {
		t.Fatalf("legacy recovery without checkpoint identity = %q, want rejected", got)
	}
	for _, tc := range cases {
		ref := refs[tc.name]
		for _, valid := range tc.valid {
			args := make(map[string]any, len(valid.args)+1)
			for key, value := range valid.args {
				args[key] = value
			}
			args["recoveryOf"] = ref
			if got := projectAssistantValidatedMutationRecoveryOf(restarted, args, valid.tool); got != ref {
				t.Fatalf("%s compatible %s recovery = %q, want %q", tc.name, valid.tool, got, ref)
			}
		}
		invalidArgs := make(map[string]any, len(tc.invalid.args)+1)
		for key, value := range tc.invalid.args {
			invalidArgs[key] = value
		}
		invalidArgs["recoveryOf"] = ref
		if got := projectAssistantValidatedMutationRecoveryOf(restarted, invalidArgs, tc.invalid.tool); got != "" {
			t.Fatalf("%s incompatible %s recovery = %q, want rejected", tc.name, tc.invalid.tool, got)
		}
	}
}

func TestProjectAssistantMutationDiagnosticUsesConciseCauseAndRepairGuidance(t *testing.T) {
	diagnostic := projectAssistantActionFeedMutationDiagnostic(
		"create-failure",
		projectToolCreateFile,
		&projectAssistantMutation{Operation: projectToolCreateFile, Path: "src/App.vue"},
		&projectAssistantMutationFailure{
			Code:      string(workspace.MutationErrorTargetExists),
			Operation: projectToolCreateFile,
			Path:      "src/App.vue",
			Guidance:  "Read the complete file, then retry with replace_file.",
		},
		"",
	)
	if diagnostic == nil || diagnostic.Message != "The create target already exists." ||
		diagnostic.Guidance == "" || diagnostic.Message == diagnostic.Guidance {
		t.Fatalf("diagnostic = %#v, want concise cause plus distinct guidance", diagnostic)
	}
}

func TestProjectAssistantMutationDiagnosticUsesOperationSpecificRecovery(t *testing.T) {
	tests := []struct {
		name         string
		operation    string
		code         workspace.MutationErrorCode
		wantMessage  string
		wantGuidance string
	}{
		{
			name:         "replace stale",
			operation:    projectToolReplaceFile,
			code:         workspace.MutationErrorStale,
			wantMessage:  "The file changed before this update was applied.",
			wantGuidance: "expectedVersion",
		},
		{
			name:         "edit ambiguous",
			operation:    projectToolEditFile,
			code:         workspace.MutationErrorAmbiguous,
			wantMessage:  "The requested text matched multiple locations.",
			wantGuidance: "replaceAll",
		},
		{
			name:         "delete missing",
			operation:    projectToolDeleteFile,
			code:         workspace.MutationErrorTargetNotFound,
			wantMessage:  "The source file no longer exists.",
			wantGuidance: "existing source path",
		},
		{
			name:         "move destination exists",
			operation:    projectToolMoveFile,
			code:         workspace.MutationErrorTargetExists,
			wantMessage:  "The move destination already exists.",
			wantGuidance: "different destination",
		},
		{
			name:         "missing version",
			operation:    projectToolEditFile,
			code:         workspace.MutationErrorVersionRequired,
			wantMessage:  "This mutation needs the file's current version.",
			wantGuidance: "complete current file",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			diagnostic := projectAssistantActionFeedMutationDiagnostic(
				"mutation-"+tt.name,
				tt.operation,
				&projectAssistantMutation{Operation: tt.operation, Path: "src/App.vue"},
				&projectAssistantMutationFailure{Code: string(tt.code), Operation: tt.operation, Path: "src/App.vue"},
				"",
			)
			if diagnostic == nil || diagnostic.Code != string(tt.code) || diagnostic.Operation != tt.operation || diagnostic.Path != "src/App.vue" {
				t.Fatalf("diagnostic = %#v, want bounded operation/code/path", diagnostic)
			}
			if diagnostic.Message != tt.wantMessage || diagnostic.Guidance == "" || !strings.Contains(diagnostic.Guidance, tt.wantGuidance) || diagnostic.Message == diagnostic.Guidance {
				t.Fatalf("diagnostic = %#v, want operation-specific message and guidance containing %q", diagnostic, tt.wantGuidance)
			}
		})
	}
}

func TestProjectAssistantActionFeedAssistantToolCallUsesMutationOperationContext(t *testing.T) {
	for _, tc := range []struct {
		name         string
		error        string
		wantMessage  string
		wantGuidance string
	}{
		{
			name:         projectToolCreateFile,
			error:        string(workspace.MutationErrorTargetExists) + ": target already exists",
			wantMessage:  "The create target already exists.",
			wantGuidance: "replace_file",
		},
		{
			name:         projectToolMoveFile,
			error:        string(workspace.MutationErrorTargetExists) + ": destination file already exists",
			wantMessage:  "The move destination already exists.",
			wantGuidance: "different destination",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			item := projectAssistantActionFeedItemFromAssistantToolCall(projectAssistantToolCall{
				ID:     "assistant-" + tc.name,
				Name:   tc.name,
				Status: "failed",
				Error:  tc.error,
			})
			if item.Diagnostic == nil || item.Diagnostic.Operation != tc.name || item.Diagnostic.Code != string(workspace.MutationErrorTargetExists) {
				t.Fatalf("item = %#v, want mutation operation/code diagnostic", item)
			}
			if item.Diagnostic.Message != tc.wantMessage || !strings.Contains(item.Diagnostic.Guidance, tc.wantGuidance) || item.Diagnostic.Message == item.Diagnostic.Guidance {
				t.Fatalf("diagnostic = %#v, want operation-specific cause and guidance", item.Diagnostic)
			}
		})
	}
}

func TestProjectAssistantActionFeedExplainsTypedPreviewFailures(t *testing.T) {
	for _, tt := range []struct {
		name         string
		preview      projectAssistantPreviewInspectionAction
		wantStatus   string
		wantSeverity string
		wantTitle    string
		wantCategory string
		wantCode     string
		wantMessage  string
	}{
		{
			name:         "assertion mismatch",
			preview:      projectAssistantPreviewInspectionAction{FailureKind: "assertion", AssertionCount: 6, FailedAssertionCount: 3},
			wantStatus:   projectAssistantActionFeedStatusFailed,
			wantSeverity: projectAssistantActionFeedSeverityAttention,
			wantTitle:    "Preview assertions did not match",
			wantCategory: "validation",
			wantCode:     "preview_assertion_mismatch",
			wantMessage:  "3 of 6 preview assertions did not match.",
		},
		{
			name:         "application error",
			preview:      projectAssistantPreviewInspectionAction{FailureKind: "application"},
			wantStatus:   projectAssistantActionFeedStatusFailed,
			wantSeverity: projectAssistantActionFeedSeverityError,
			wantTitle:    "Preview rendered with application errors",
			wantCategory: "runtime",
			wantCode:     "preview_application_error",
			wantMessage:  "The preview rendered, but the browser detected application errors.",
		},
		{
			name:         "navigation failure",
			preview:      projectAssistantPreviewInspectionAction{FailureKind: "navigation"},
			wantStatus:   projectAssistantActionFeedStatusFailed,
			wantSeverity: projectAssistantActionFeedSeverityError,
			wantTitle:    "Preview could not be opened",
			wantCategory: "runtime",
			wantCode:     "preview_navigation_failed",
			wantMessage:  "The browser could not open the development preview.",
		},
		{
			name:         "worker unavailable",
			preview:      projectAssistantPreviewInspectionAction{FailureKind: "worker_unavailable"},
			wantStatus:   projectAssistantActionFeedStatusFailed,
			wantSeverity: projectAssistantActionFeedSeverityError,
			wantTitle:    "Preview inspection unavailable",
			wantCategory: "runtime",
			wantCode:     "preview_worker_unavailable",
			wantMessage:  "The browser inspection service was unavailable.",
		},
		{
			name:         "preview not current",
			preview:      projectAssistantPreviewInspectionAction{FailureKind: "not_current"},
			wantStatus:   projectAssistantActionFeedStatusWaiting,
			wantSeverity: projectAssistantActionFeedSeverityAttention,
			wantTitle:    "Waiting for the latest preview",
			wantCategory: "runtime",
			wantCode:     "preview_not_current",
			wantMessage:  "The latest workspace changes had not reached the development preview yet.",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			item := projectAssistantActionFeedItemFromToolCall(projectToolCallStreamEvent{
				ID:                "inspect-call",
				Name:              projectToolInspectDevelopmentPreview,
				Status:            "failed",
				PreviewInspection: &tt.preview,
				Sequence:          1,
			})
			if item.Status != tt.wantStatus || item.Severity != tt.wantSeverity || item.Title != tt.wantTitle {
				t.Fatalf("item = %#v", item)
			}
			if item.Diagnostic == nil || item.Diagnostic.Category != tt.wantCategory || item.Diagnostic.Code != tt.wantCode ||
				item.Diagnostic.Message != tt.wantMessage || item.Diagnostic.Operation != projectToolInspectDevelopmentPreview {
				t.Fatalf("diagnostic = %#v", item.Diagnostic)
			}
		})
	}
}

func TestProjectAssistantPreviewDiagnosticSurvivesMetadataRoundTripWithoutPageOutput(t *testing.T) {
	preview := projectAssistantPreviewInspectionResult{
		Status:      "failed",
		FailureKind: "assertion",
		Snapshot:    "hostile rendered page output",
		Console:     []projectAssistantPreviewInspectionConsoleEvent{{Level: "error", Message: "secret console output"}},
		Assertions: []projectAssistantPreviewInspectionAssertionResult{
			{projectAssistantPreviewInspectionAssertion: projectAssistantPreviewInspectionAssertion{Kind: "text_present", Text: "secret assertion"}, Passed: true},
			{projectAssistantPreviewInspectionAssertion: projectAssistantPreviewInspectionAssertion{Kind: "role_present", Role: "button", Name: "secret name"}, Passed: false},
		},
	}
	action := projectAssistantActionFeedItemFromToolCall(projectToolCallStreamEvent{
		ID:                "inspect-round-trip",
		Name:              projectToolInspectDevelopmentPreview,
		Status:            "failed",
		PreviewInspection: projectAssistantPreviewInspectionActionFromResult(preview),
		Sequence:          1,
	})
	raw, err := json.Marshal([]projectAssistantActionFeedItem{action})
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"hostile rendered page output", "secret console output", "secret assertion", "secret name"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("action feed leaked %q: %s", forbidden, raw)
		}
	}
	var metadata any
	if err := json.Unmarshal(raw, &metadata); err != nil {
		t.Fatal(err)
	}
	reloaded := projectAssistantActionFeedFromMetadata(metadata)
	if len(reloaded) != 1 || reloaded[0].Diagnostic == nil || reloaded[0].Diagnostic.Code != "preview_assertion_mismatch" ||
		reloaded[0].Diagnostic.Message != "1 of 2 preview assertions did not match." {
		t.Fatalf("reloaded action feed = %#v", reloaded)
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
		Name:      projectToolEditFile,
		Status:    "succeeded",
		Arguments: "path src/App.vue; 42 bytes",
		Summary:   "file updated",
	})
	if item.Title != "Edited files" || item.Target != "" || item.Outcome != "" || item.GroupKey != "" {
		t.Fatalf("minimal item = %#v, want only generic presentation", item)
	}
}

func TestProjectAssistantActionFeedExecCarriesStructuredResultWithoutOutput(t *testing.T) {
	item := projectAssistantActionFeedItemFromToolCall(projectToolCallStreamEvent{
		ID:     "exec-1",
		Name:   projectToolExecCommand,
		Status: "failed",
		Exec: &projectAssistantExecMetadata{
			Component:       "backend",
			Argv:            []string{"go", "test", "./..."},
			Workdir:         "internal",
			TimeoutSeconds:  30,
			NetworkProfile:  "application-runtime",
			WritebackPolicy: "runtime-workspace-only",
			Status:          "failed",
			Summary:         "Command failed in component \"backend\".",
			ExitCode:        func() *int { value := 2; return &value }(),
			DurationMS:      123,
		},
	})
	if item.Exec == nil || item.Exec.Component != "backend" || item.Exec.Status != "failed" || item.Exec.ExitCode == nil || *item.Exec.ExitCode != 2 || item.Exec.DurationMS != 123 {
		t.Fatalf("exec action item = %#v", item)
	}
	data, err := json.Marshal(item)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "stdout") || strings.Contains(string(data), "stderr") {
		t.Fatalf("exec action item exposed raw output fields: %s", data)
	}
}
