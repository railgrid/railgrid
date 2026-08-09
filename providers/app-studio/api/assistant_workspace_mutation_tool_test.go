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
	"strings"
	"testing"

	"github.com/faroshq/provider-app-studio/workspace"
)

func TestAssistantRegistryExposesStrictOrdinaryWorkspaceMutationTools(t *testing.T) {
	registry := projectAssistantLocalToolRegistry(nil)
	for _, name := range []string{projectToolCreateFile, projectToolReplaceFile, projectToolEditFile, projectToolDeleteFile, projectToolMoveFile} {
		spec, ok := registry.Spec(name)
		if !ok {
			t.Fatalf("%s is not model-visible", name)
		}
		var schema map[string]any
		if err := json.Unmarshal(spec.Parameters, &schema); err != nil {
			t.Fatalf("decode %s schema: %v", name, err)
		}
		if schema["additionalProperties"] != false {
			t.Fatalf("%s schema permits additional properties: %s", name, spec.Parameters)
		}
	}
	if registry.Has("apply_patch") {
		t.Fatal("retired textual mutation tool remains model-visible")
	}
	create, _ := registry.Spec(projectToolCreateFile)
	if !strings.Contains(create.Description, "create-only") || !strings.Contains(create.Description, "replace_file") {
		t.Fatalf("create_file description does not describe create-only cutover: %s", create.Description)
	}
	replace, _ := registry.Spec(projectToolReplaceFile)
	if !strings.Contains(replace.Description, "expectedVersion") || !strings.Contains(string(replace.Parameters), "expectedVersion") {
		t.Fatalf("replace_file contract missing expectedVersion: %#v", replace)
	}
	edit, _ := registry.Spec(projectToolEditFile)
	for _, want := range []string{"oldString", "newString", "replaceAll", "current file", "expectedVersion", "stale or ambiguous"} {
		if !strings.Contains(string(edit.Parameters)+edit.Description, want) {
			t.Fatalf("edit_file contract missing %q", want)
		}
	}
}

func TestAssistantOrdinaryWorkspaceMutationToolsPerformCreateEditDeleteMove(t *testing.T) {
	ctx := context.Background()
	files := workspace.NewFileStore(t.TempDir())
	scope := workspace.Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-a", ProjectName: "demo", ProjectUID: "uid-a"}
	server := &Server{workspaces: files}
	registry := projectAssistantLocalToolRegistry(server)
	call := func(name string, args map[string]any, state *projectEinoAssistantRunState, initial bool) (workspace.MutationResult, error) {
		tool, ok := registry.Get(name)
		if !ok {
			t.Fatalf("%s missing from registry", name)
		}
		raw, err := tool.Call(ctx, projectAssistantToolCallRequest{WorkspaceScope: scope, InitialBuild: initial, RunState: state, Arguments: args})
		var result workspace.MutationResult
		if raw != "" {
			if decodeErr := json.Unmarshal([]byte(raw), &result); decodeErr != nil {
				t.Fatalf("decode %s result: %v (%s)", name, decodeErr, raw)
			}
		}
		return result, err
	}
	if result, err := call(projectToolCreateFile, map[string]any{"path": "src/App.tsx", "content": "old\n"}, nil, false); err != nil || result.Operation != projectToolCreateFile {
		t.Fatalf("initial create = %#v, %v", result, err)
	}
	state := newProjectEinoAssistantRunState()
	if result, err := call(projectToolEditFile, map[string]any{"path": "src/App.tsx", "oldString": "old", "newString": "new"}, state, false); err != nil || result.Operation != projectToolEditFile || result.Diff == "" {
		t.Fatalf("current-file edit = %#v, %v", result, err)
	}
	if result, err := call(projectToolEditFile, map[string]any{"path": "src/App.tsx", "oldString": "new", "newString": "newer"}, state, false); err != nil || result.Operation != projectToolEditFile || result.Diff == "" {
		t.Fatalf("second current-file edit = %#v, %v", result, err)
	}
	current, err := files.ReadFile(ctx, scope, workspace.ReadOptions{Path: "src/App.tsx"})
	if err != nil {
		t.Fatal(err)
	}
	state.RecordObservedReadFileVersion(current.Path, current.Version)
	current, err = files.ReadFile(ctx, scope, workspace.ReadOptions{Path: "src/App.tsx"})
	if err != nil {
		t.Fatal(err)
	}
	state.RecordObservedReadFileVersion(current.Path, current.Version)
	if result, err := call(projectToolMoveFile, map[string]any{"sourcePath": "src/App.tsx", "destinationPath": "src/Main.tsx", "expectedVersion": current.Version}, state, false); err != nil || result.Operation != projectToolMoveFile || result.PreviousPath != "src/App.tsx" {
		t.Fatalf("move = %#v, %v", result, err)
	}
	current, err = files.ReadFile(ctx, scope, workspace.ReadOptions{Path: "src/Main.tsx"})
	if err != nil {
		t.Fatal(err)
	}
	state.RecordObservedReadFileVersion(current.Path, current.Version)
	if result, err := call(projectToolDeleteFile, map[string]any{"path": "src/Main.tsx", "expectedVersion": current.Version}, state, false); err != nil || result.Operation != projectToolDeleteFile {
		t.Fatalf("delete = %#v, %v", result, err)
	}
	if _, err := files.ReadFile(ctx, scope, workspace.ReadOptions{Path: "src/Main.tsx"}); err == nil {
		t.Fatal("deleted file still exists")
	}
}

func TestAssistantOrdinaryMutationToolsRejectUnsafeOrStaleEdits(t *testing.T) {
	ctx := context.Background()
	files := workspace.NewFileStore(t.TempDir())
	scope := workspace.Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-a", ProjectName: "demo", ProjectUID: "uid-a"}
	server := &Server{workspaces: files}
	registry := projectAssistantLocalToolRegistry(server)
	edit, _ := registry.Get(projectToolEditFile)
	if _, err := edit.Call(ctx, projectAssistantToolCallRequest{WorkspaceScope: scope, RunState: newProjectEinoAssistantRunState(), Arguments: map[string]any{"path": "../secret", "oldString": "old", "newString": "new", "expectedVersion": "sha256:test"}}); err == nil {
		t.Fatal("unsafe edit path accepted")
	}
	if _, err := files.WriteFile(ctx, scope, workspace.WriteOptions{Path: "same.txt", Content: "one\none\n"}); err != nil {
		t.Fatal(err)
	}
	state := newProjectEinoAssistantRunState()
	current, err := files.ReadFile(ctx, scope, workspace.ReadOptions{Path: "same.txt"})
	if err != nil {
		t.Fatal(err)
	}
	state.RecordObservedReadFileVersion(current.Path, current.Version)
	if _, err := edit.Call(ctx, projectAssistantToolCallRequest{WorkspaceScope: scope, RunState: state, Arguments: map[string]any{"path": "same.txt", "oldString": "one", "newString": "two", "expectedVersion": current.Version}}); err == nil {
		t.Fatal("ambiguous edit accepted without replaceAll")
	}
	if _, err := edit.Call(ctx, projectAssistantToolCallRequest{WorkspaceScope: scope, RunState: state, Arguments: map[string]any{"path": "same.txt", "oldString": "missing", "newString": "two", "expectedVersion": current.Version}}); err == nil {
		t.Fatal("stale edit accepted")
	}
}
