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
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/eino/adk"
	einotool "github.com/cloudwego/eino/components/tool"

	"github.com/faroshq/provider-app-studio/store"
	"github.com/faroshq/provider-app-studio/workspace"
)

func TestAssistantRegistryExposesOnlyContextualWorkspacePatchMutation(t *testing.T) {
	registry := projectAssistantLocalToolRegistry(nil)
	for _, retired := range []string{"write_file", "mkdir", "hydrate_workspace"} {
		if registry.Has(retired) {
			t.Fatalf("retired tool %s remains registered", retired)
		}
	}
	spec, ok := registry.Spec(projectToolApplyPatch)
	if !ok {
		t.Fatal("apply_patch is not model-visible")
	}
	var schema map[string]any
	if err := json.Unmarshal(spec.Parameters, &schema); err != nil {
		t.Fatalf("decode apply_patch schema: %v", err)
	}
	properties, _ := schema["properties"].(map[string]any)
	if len(properties) != 1 || properties["patch"] == nil {
		t.Fatalf("apply_patch properties = %#v, want only patch", properties)
	}
	if got := strings.Join(projectToolStringList(schema["required"]), ","); got != "patch" {
		t.Fatalf("required = %q, want patch", got)
	}
	for _, forbidden := range []string{"oldText", "newText", "replaceAll"} {
		if strings.Contains(string(spec.Parameters), forbidden) {
			t.Fatalf("apply_patch schema still contains %q: %s", forbidden, spec.Parameters)
		}
	}
	for _, want := range []string{
		"Use exactly one outer *** Begin Patch / *** End Patch envelope",
		"Inside an Add File section, write only the new file content",
		"every content line must begin with '+'",
		"A leading '+' disambiguates literal lines that look like protocol markers",
		"must be exactly '@@' or '@@ <literal source line copied from the file>'",
		"Never emit Git/unified-diff line coordinates",
		"@@ -12,4 +12,5 @@",
		"do not repeat the anchor in the hunk body",
		"Use plain '@@' when changing the first line",
		"Put multiple hunks for the same file in source order",
		"*** Update File: src/App.jsx",
	} {
		if !strings.Contains(spec.Description, want) {
			t.Fatalf("apply_patch description missing %q: %s", want, spec.Description)
		}
	}
	for _, want := range []string{
		"Every Add File content line must begin with '+'",
		"Hunk headers are exactly '@@' or '@@ <literal source line>'",
		"numeric unified-diff coordinates are forbidden",
		"Move to cannot stand alone",
	} {
		if !strings.Contains(string(spec.Parameters), want) {
			t.Fatalf("apply_patch parameter schema missing %q: %s", want, spec.Parameters)
		}
	}
	for _, want := range []string{"*** Delete File: <path>", "*** Move to: <new path>", "Move to is not a standalone section"} {
		if !strings.Contains(spec.Description, want) {
			t.Fatalf("apply_patch description missing %q: %s", want, spec.Description)
		}
	}
}

func TestAssistantDeepInstructionIncludesAddFilePrefixGrammar(t *testing.T) {
	for _, want := range []string{
		"Inside an Add File section, write only the new file content",
		"every content line must begin with '+'",
		"A leading '+' disambiguates literal lines that look like protocol markers",
	} {
		if !strings.Contains(projectEinoAssistantV2DeepInstruction, want) {
			t.Fatalf("deep assistant instruction missing %q: %s", want, projectEinoAssistantV2DeepInstruction)
		}
	}
}

func TestAssistantNumericUnifiedDiffFailureReturnsTargetedRecovery(t *testing.T) {
	err := &workspace.PatchError{Code: workspace.PatchErrorInvalidPatch, Message: "line 3: numeric unified-diff hunk headers are not supported; use exactly '@@' or '@@ <literal source line>'"}
	result := projectEinoAssistantSafeToolFailureResult(projectToolApplyPatch, err)
	for _, want := range []string{
		"treats text after @@ as a literal source anchor",
		"never use line coordinates such as @@ -12,4 +12,5 @@",
	} {
		if !strings.Contains(result, want) {
			t.Fatalf("recovery missing %q: %s", want, result)
		}
	}
}

func TestAssistantAdvertisedContextualPatchExampleParses(t *testing.T) {
	const marker = "Valid edit example:\n"
	_, example, ok := strings.Cut(projectAssistantContextualPatchFormatInstruction, marker)
	if !ok {
		t.Fatalf("contextual patch instruction missing %q", marker)
	}
	example, _, ok = strings.Cut(example, "Valid move example:\n")
	if !ok {
		t.Fatal("contextual patch instruction is missing the move example")
	}
	example = strings.TrimSpace(example)
	paths, err := workspace.PatchPaths(example)
	if err != nil {
		t.Fatalf("advertised contextual patch example is invalid: %v\n%s", err, example)
	}
	if got := strings.Join(paths, ","); got != "src/App.jsx" {
		t.Fatalf("advertised example paths = %q, want src/App.jsx", got)
	}
}

func TestAssistantAdvertisedContextualMoveExampleParses(t *testing.T) {
	_, example, ok := strings.Cut(projectAssistantContextualPatchFormatInstruction, "Valid move example:\n")
	if !ok {
		t.Fatal("contextual patch instruction is missing the move example")
	}
	paths, err := workspace.PatchPaths(strings.TrimSpace(example))
	if err != nil {
		t.Fatalf("advertised contextual move example is invalid: %v\n%s", err, example)
	}
	if got := strings.Join(paths, ","); got != "src/new.jsx,src/old.jsx" {
		t.Fatalf("advertised move paths = %q, want old and new paths", got)
	}
}

func TestAssistantGenericInvalidPatchRecoveryUsesSupportedOperations(t *testing.T) {
	err := &workspace.PatchError{Code: workspace.PatchErrorInvalidPatch, Message: "the last line must be '*** End Patch'"}
	result := projectEinoAssistantSafeToolFailureResult(projectToolApplyPatch, err)
	for _, want := range []string{"Start with Add File, Update File, or Delete File", "put Move to immediately below Update File"} {
		if !strings.Contains(result, want) {
			t.Fatalf("recovery missing %q: %s", want, result)
		}
	}
}

func TestAssistantNestedAddFileProtocolRecoveryIsTargeted(t *testing.T) {
	err := &workspace.PatchError{Code: workspace.PatchErrorInvalidPatch, Message: "line 4: Add File content cannot contain hunk headers or nested patch envelopes"}
	result := projectEinoAssistantSafeToolFailureResult(projectToolApplyPatch, err)
	for _, want := range []string{
		"return one outer *** Begin Patch / *** End Patch envelope",
		"Under Add File, emit only the new file content",
		"no @@ lines and no nested Begin/End markers",
		"Every content line must begin with '+'",
	} {
		if !strings.Contains(result, want) {
			t.Fatalf("recovery missing %q: %s", want, result)
		}
	}
}

func TestAssistantMissingContextRecoveryExplainsAnchorCursor(t *testing.T) {
	err := &workspace.PatchError{Code: workspace.PatchErrorContextNotFound, Path: "src/App.jsx", Hunk: 1, Message: "hunk context was not found after line 1"}
	result := projectEinoAssistantSafeToolFailureResult(projectToolApplyPatch, err)
	for _, want := range []string{
		"positions the hunk after that unchanged line",
		"must not be repeated in the hunk body",
		"use plain @@ when changing the first line",
	} {
		if !strings.Contains(result, want) {
			t.Fatalf("recovery missing %q: %s", want, result)
		}
	}
}

func TestAssistantMissingContextRecoverySurvivesLongExpectedLines(t *testing.T) {
	err := &workspace.PatchError{
		Code:    workspace.PatchErrorContextNotFound,
		Path:    "src/App.jsx",
		Hunk:    1,
		Message: "failed to find the expected lines after line 1:\n" + strings.Repeat("long stale source line ", 100),
	}
	result := projectEinoAssistantSafeToolFailureResult(projectToolApplyPatch, err)
	if len(result) > projectToolInfoLimit {
		t.Fatalf("failure result length = %d, want <= %d", len(result), projectToolInfoLimit)
	}
	for _, want := range []string{"failed to find the expected lines", "Recovery: reread the named file", "use plain @@ when changing the first line"} {
		if !strings.Contains(result, want) {
			t.Fatalf("long-context recovery missing %q: %s", want, result)
		}
	}
}

func TestAssistantV2ToolCatalogIsStableForCollaborationMode(t *testing.T) {
	server := NewWithWorkspace(nil, store.NewMemoryStore(), workspace.NewFileStore(t.TempDir()), "", false)
	tests := []struct {
		name      string
		mode      projectAssistantCollaborationMode
		profile   projectAssistantTurnProfile
		wantPatch bool
	}{
		{name: "default", mode: projectAssistantCollaborationModeDefault, profile: projectAssistantTurnProfileImplementation, wantPatch: true},
		{name: "plan", mode: projectAssistantCollaborationModePlan, profile: projectAssistantTurnProfileDebugging},
		{name: "review", mode: projectAssistantCollaborationModeReview, profile: projectAssistantTurnProfileDebugging},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			run := store.AssistantRun{Mode: store.AssistantRunMode(tt.mode)}
			req := projectAssistantRunRequest{
				AssistantRun:      &run,
				CollaborationMode: tt.mode,
				TurnProfile:       tt.profile,
				TurnPolicy:        projectAssistantTurnPolicyForProfile(tt.profile),
			}
			state := newProjectEinoAssistantRunState()
			state.SetToolDiscovery(projectEinoAssistantToolDiscovery{})
			tools, err := newProjectEinoAssistantToolsFactory(server)(context.Background(), req, state)
			if err != nil {
				t.Fatal(err)
			}
			names := map[string]bool{}
			for _, tool := range tools {
				info, err := tool.Info(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				names[projectToolBaseName(info.Name)] = true
			}
			if names[projectToolApplyPatch] != tt.wantPatch {
				t.Fatalf("tools = %#v, apply_patch presence = %t, want %t", names, names[projectToolApplyPatch], tt.wantPatch)
			}
			if got, want := names[projectToolAskFollowUp], projectAssistantCollaborationModeReadOnly(tt.mode); got != want {
				t.Fatalf("tools = %#v, ask_follow_up presence = %t, want %t", names, got, want)
			}
			for _, retired := range []string{"write_file", "mkdir", "hydrate_workspace", "request_project_plan_approval"} {
				if names[retired] {
					t.Fatalf("tools = %#v, retired %s remains model-visible", names, retired)
				}
			}
			if projectAssistantCollaborationModeReadOnly(tt.mode) {
				for _, effect := range []string{
					projectToolApplyPatch,
					projectToolSelectTemplate,
					projectToolRestartRuntime,
					projectToolSetRuntimeEnv,
					projectToolCommitProjectFiles,
				} {
					if names[effect] {
						t.Fatalf("read-only tools = %#v, effect %s remains visible", names, effect)
					}
				}
			}
		})
	}
}

func TestAssistantV2MutationAdmissionRejectsStoppedRun(t *testing.T) {
	ctx := context.Background()
	messages := store.NewMemoryStore()
	server := NewWithWorkspace(nil, messages, workspace.NewFileStore(t.TempDir()), "", false)
	scope := store.Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo", ProjectUID: "project-1"}
	now := time.Now().UTC()
	run := store.AssistantRun{
		ID: "run-1", Mode: store.AssistantRunModeDefault, Status: store.AssistantRunStatusRunning,
		ClientRequestID: "request-1", UserMessageID: "user-1", ActiveMessageID: "assistant-1",
		Revision: 1, CreatedAt: now, UpdatedAt: now,
	}
	user := store.Message{ID: run.UserMessageID, Role: "user", ActorID: "actor-1", Content: "edit it", CreatedAt: now, UpdatedAt: now}
	assistant := store.Message{ID: run.ActiveMessageID, Role: "assistant", CreatedAt: now, UpdatedAt: now}
	if err := bindProjectAssistantStartRequest(&run, user.ActorID, user.Content); err != nil {
		t.Fatal(err)
	}
	created, err := messages.CreateAssistantRun(ctx, scope, user, assistant, run)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := server.projectAssistantSupervisor().Attach(scope, created, assistant); err != nil {
		t.Fatal(err)
	}
	tool := projectEinoAssistantTool{
		server: server,
		req: projectAssistantRunRequest{
			Identity: identity{user: user.ActorID}, MessageScope: scope,
			CollaborationMode: projectAssistantCollaborationModeDefault, AssistantRun: &created,
		},
		runState: newProjectEinoAssistantRunState(),
	}
	spec := projectAssistantToolSpec{Name: projectToolApplyPatch, Risk: projectAssistantToolRiskWrite}
	if err := tool.admitMutation(ctx, spec); err != nil {
		t.Fatalf("admit running mutation: %v", err)
	}
	if _, stopped, err := server.projectAssistantSupervisor().Stop(scope, created.ID); err != nil || !stopped {
		t.Fatalf("Stop = %v, %v", stopped, err)
	}
	if err := tool.admitMutation(ctx, spec); !errors.Is(err, store.ErrAssistantRunConflict) {
		t.Fatalf("admit stopped mutation = %v, want run conflict", err)
	}
}

func TestAssistantV2LifecycleDoesNotRequireVerificationBeforeCommit(t *testing.T) {
	state := newProjectEinoAssistantRunState()
	state.RecordSourceMutation()
	lifecycle := projectEinoAssistantLifecycleMiddleware(projectAssistantRunRequest{}, state).(*projectEinoAssistantLifecycle)
	called := false
	wrapped, err := lifecycle.WrapInvokableToolCall(
		context.Background(),
		func(context.Context, string, ...einotool.Option) (string, error) {
			called = true
			return `{"status":"succeeded"}`, nil
		},
		&adk.ToolContext{Name: projectToolCommitProjectFiles},
	)
	if err != nil {
		t.Fatal(err)
	}
	result, err := wrapped(context.Background(), `{}`)
	if err != nil {
		t.Fatal(err)
	}
	if !called || result != `{"status":"succeeded"}` {
		t.Fatalf("commit result = %q, endpoint called = %v", result, called)
	}
}

func TestAssistantContextualPatchSupportsDeleteAndMove(t *testing.T) {
	for name, patch := range map[string]string{
		"delete": "*** Begin Patch\n*** Delete File: old.txt\n*** End Patch",
		"move":   "*** Begin Patch\n*** Update File: old.txt\n*** Move to: new.txt\n@@\n-old\n+new\n*** End Patch",
	} {
		t.Run(name, func(t *testing.T) {
			workspaces := workspace.NewFileStore(t.TempDir())
			scope := workspace.Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: "demo", ProjectUID: "test-project-uid"}
			writeTestWorkspaceFiles(t, context.Background(), workspaces, scope, []workspace.File{{Path: "old.txt", Content: "old\n"}})
			tool, ok := projectAssistantLocalToolRegistry(&Server{workspaces: workspaces}).Get(projectToolApplyPatch)
			if !ok {
				t.Fatal("apply_patch tool was not registered")
			}
			if _, err := tool.Call(context.Background(), projectAssistantToolCallRequest{
				WorkspaceScope: scope,
				Arguments:      map[string]any{"patch": patch},
			}); err != nil {
				t.Fatal(err)
			}
			if _, err := workspaces.ReadFile(context.Background(), scope, workspace.ReadOptions{Path: "old.txt"}); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("old.txt still exists: %v", err)
			}
			if name == "move" {
				read, err := workspaces.ReadFile(context.Background(), scope, workspace.ReadOptions{Path: "new.txt"})
				if err != nil || read.Content != "new\n" {
					t.Fatalf("moved content = %q, err=%v", read.Content, err)
				}
			}
		})
	}
}

func TestAssistantContextualPatchUsesLiveWorkspaceAndDoesNotCreateLegacySnapshot(t *testing.T) {
	workspaces := workspace.NewFileStore(t.TempDir())
	scope := workspace.Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: "demo", ProjectUID: "test-project-uid"}
	writeTestWorkspaceFiles(t, context.Background(), workspaces, scope, []workspace.File{{Path: "src/app.js", Content: "export const theme = 'light'\n"}})
	tool, ok := projectAssistantLocalToolRegistry(&Server{workspaces: workspaces}).Get(projectToolApplyPatch)
	if !ok {
		t.Fatal("apply_patch tool was not registered")
	}
	patch := `*** Begin Patch
*** Update File: src/app.js
@@
-export const theme = 'light'
+export const theme = 'dark'
*** Add File: src/new.js
+export const created = true
*** End Patch`
	req := projectAssistantToolCallRequest{
		WorkspaceScope: scope,
		AssistantRunID: "run-patch",
		Arguments:      map[string]any{"patch": patch},
	}
	resultJSON, err := tool.Call(context.Background(), req)
	if err != nil {
		t.Fatalf("patch returned error: %v", err)
	}
	var result workspace.MutationResult
	if err := json.Unmarshal([]byte(resultJSON), &result); err != nil {
		t.Fatalf("decode mutation result: %v", err)
	}
	if result.Operation != "apply_patch" || len(result.Files) != 2 || strings.Join(result.Paths, ",") != "src/app.js,src/new.js" {
		t.Fatalf("mutation result = %#v", result)
	}
	read, err := workspaces.ReadFile(context.Background(), scope, workspace.ReadOptions{Path: "src/app.js"})
	if err != nil || read.Content != "export const theme = 'dark'\n" {
		t.Fatalf("patched source = %#v, err = %v", read, err)
	}
	created, err := workspaces.ReadFile(context.Background(), scope, workspace.ReadOptions{Path: "src/new.js"})
	if err != nil || created.Content != "export const created = true\n" {
		t.Fatalf("patched Add File target = %#v, err = %v", created, err)
	}
}

func TestAssistantContextualPatchAllowsAddWithoutPriorRead(t *testing.T) {
	workspaces := workspace.NewFileStore(t.TempDir())
	scope := workspace.Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: "demo", ProjectUID: "test-project-uid"}
	tool, ok := projectAssistantLocalToolRegistry(&Server{workspaces: workspaces}).Get(projectToolApplyPatch)
	if !ok {
		t.Fatal("apply_patch tool was not registered")
	}
	_, err := tool.Call(context.Background(), projectAssistantToolCallRequest{
		WorkspaceScope: scope,
		AssistantRunID: "run-add",
		Arguments: map[string]any{"patch": `*** Begin Patch
*** Add File: nested/app.js
+export default true
*** End Patch`},
	})
	if err != nil {
		t.Fatalf("Add File without prior read returned error: %v", err)
	}
}
