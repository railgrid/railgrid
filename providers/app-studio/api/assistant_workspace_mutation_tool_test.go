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
	"context"
	"encoding/json"
	"strings"
	"testing"

	einotool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"

	"github.com/railgrid/provider-app-studio/workspace"
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
	for _, want := range []string{
		`source text '\path' (one backslash)`,
		`JSON value '"\\path"'`,
		`source text '\\path' (two backslashes)`,
		`JSON value '"\\\\path"'`,
	} {
		if !strings.Contains(edit.Description, want) {
			t.Fatalf("edit_file description missing exact JSON example %q: %s", want, edit.Description)
		}
	}
	read, _ := registry.Spec(projectToolReadFile)
	for _, want := range []string{"UTF-8 source is shown literally", "collision-safe fence", "decoded from JSON once", "clipped/ranged results"} {
		if !strings.Contains(read.Description, want) {
			t.Fatalf("read_file description missing %q: %s", want, read.Description)
		}
	}
	var editSchema struct {
		Properties map[string]struct {
			Description string `json:"description"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(edit.Parameters, &editSchema); err != nil {
		t.Fatalf("decode edit_file schema: %v", err)
	}
	for name, want := range map[string]string{
		"oldString": "Exact source characters to find after JSON decoding",
		"newString": "Exact source characters to insert after JSON decoding; one intended source backslash is one U+005C character",
	} {
		if !strings.Contains(editSchema.Properties[name].Description, want) {
			t.Fatalf("edit_file %s schema description = %q, want %q", name, editSchema.Properties[name].Description, want)
		}
	}
}

func TestAssistantMutationArgumentFeedbackAndCorrectedRetry(t *testing.T) {
	spec, ok := projectAssistantLocalToolRegistry(nil).Spec(projectToolEditFile)
	if !ok {
		t.Fatal("edit_file schema is not registered")
	}
	typos := []struct {
		name string
		args map[string]any
		want string
	}{
		{
			name: "oldString typo",
			args: map[string]any{"path": "src/App.tsx", "old_string": "TOP_SECRET_OLD", "newString": "new"},
			want: "unexpected mutation argument \"old_string\"",
		},
		{
			name: "newString typo",
			args: map[string]any{"path": "src/App.tsx", "oldString": "old", "new_string": "TOP_SECRET_NEW"},
			want: "unexpected mutation argument \"new_string\"",
		},
	}
	for _, tt := range typos {
		t.Run(tt.name, func(t *testing.T) {
			validationErr := projectAssistantValidateGrantBearingToolArguments(spec, tt.args)
			if validationErr == nil {
				t.Fatal("invalid mutation arguments passed validation")
			}
			feedback, ok := projectAssistantLocalMutationArgumentFeedback(spec, validationErr, true)
			if !ok {
				t.Fatal("trusted local mutation schema did not produce feedback")
			}
			for _, want := range []string{
				tt.want,
				"Required fields: newString, oldString, path.",
				"Allowed fields: expectedVersion, newString, oldString, path, recoveryOf, replaceAll.",
				"Use exact field spelling and correct the arguments before retrying.",
			} {
				if !strings.Contains(feedback, want) {
					t.Fatalf("feedback %q does not contain %q", feedback, want)
				}
			}
			if strings.Contains(feedback, "TOP_SECRET_") {
				t.Fatalf("feedback echoed an argument value: %q", feedback)
			}
			again, ok := projectAssistantLocalMutationArgumentFeedback(spec, validationErr, true)
			if !ok || again != feedback {
				t.Fatalf("feedback is not deterministic: first %q, second %q, ok=%t", feedback, again, ok)
			}
		})
	}

	if err := projectAssistantValidateWorkspaceMutationArguments(projectToolEditFile, map[string]any{
		"path": "src/App.tsx", "oldString": "old", "newString": "new", "z_typo": "", "a_typo": "",
	}); err == nil || err.Error() != "unexpected mutation argument \"a_typo\"" {
		t.Fatalf("unknown-key validation = %v, want stable first key a_typo", err)
	}
	validationErr := projectAssistantValidateWorkspaceMutationArguments(projectToolEditFile, map[string]any{
		"path": "src/App.tsx", "old_string": "old", "newString": "new",
	})
	if validationErr == nil {
		t.Fatal("test arguments unexpectedly passed validation")
	}
	missingSchema := spec
	missingSchema.Parameters = nil
	if _, ok := projectAssistantLocalMutationArgumentFeedback(missingSchema, validationErr, true); ok {
		t.Fatal("schema-missing mutation received enriched feedback")
	}
	if _, ok := projectAssistantLocalMutationArgumentFeedback(spec, validationErr, false); ok {
		t.Fatal("same-name foreign MCP tool received enriched feedback")
	}
	foreignSchema := spec
	foreignSchema.Name = "mcp__edit_file"
	if _, ok := projectAssistantLocalMutationArgumentFeedback(foreignSchema, validationErr, true); ok {
		t.Fatal("unknown foreign tool received enriched feedback")
	}

	ctx := context.Background()
	h := newProjectAssistantV2ToolHarness(t, "invalid-mutation-arguments-feedback")
	defer h.server.Shutdown(ctx)
	calls := 0
	backend := projectAssistantToolFunc{
		spec: spec,
		call: func(context.Context, projectAssistantToolCallRequest) (string, error) {
			calls++
			return "{\"operation\":\"edit_file\",\"paths\":[\"src/App.tsx\"],\"additions\":1}", nil
		},
	}
	tool := projectEinoAssistantTool{server: h.server, tool: backend, req: h.req, runState: newProjectEinoAssistantRunState()}
	node, err := compose.NewToolNode(ctx, &compose.ToolsNodeConfig{Tools: []einotool.BaseTool{tool}, ExecuteSequentially: true})
	if err != nil {
		t.Fatalf("create tool node: %v", err)
	}
	invoke := func(callID, arguments string) string {
		t.Helper()
		messages, err := node.Invoke(ctx, schema.AssistantMessage("", []schema.ToolCall{{
			ID:       callID,
			Function: schema.FunctionCall{Name: projectToolEditFile, Arguments: arguments},
		}}))
		if err != nil {
			t.Fatalf("invoke %s: %v", callID, err)
		}
		if len(messages) != 1 {
			t.Fatalf("invoke %s returned %d messages, want one", callID, len(messages))
		}
		return messages[0].Content
	}

	invalidResult := invoke("call-invalid-edit", "{\"path\":\"src/App.tsx\",\"old_string\":\"TOP_SECRET_VALUE\",\"newString\":\"new\"}")
	if calls != 0 {
		t.Fatalf("invalid invocation dispatched %d times", calls)
	}
	if !strings.Contains(invalidResult, "invalid tool arguments") || !strings.Contains(invalidResult, "unexpected mutation argument \"old_string\"") {
		t.Fatalf("invalid invocation feedback = %q", invalidResult)
	}
	if strings.Contains(invalidResult, "TOP_SECRET_VALUE") {
		t.Fatalf("invalid invocation feedback echoed an argument value: %q", invalidResult)
	}
	longArguments := map[string]any{
		"path": "src/App.tsx", "oldString": "old", "newString": "TOP_SECRET_LONG_VALUE",
	}
	longArguments[strings.Repeat("misspelled_", 140)] = "hidden"
	longRaw, err := json.Marshal(longArguments)
	if err != nil {
		t.Fatal(err)
	}
	longResult := invoke("call-long-invalid-edit", string(longRaw))
	if calls != 0 {
		t.Fatalf("long invalid invocation dispatched %d times", calls)
	}
	if len(longResult) > projectToolInfoLimit {
		t.Fatalf("long invalid feedback length = %d, want <= %d", len(longResult), projectToolInfoLimit)
	}
	for _, want := range []string{
		"Required fields: newString, oldString, path.",
		"Allowed fields: expectedVersion, newString, oldString, path, recoveryOf, replaceAll.",
		"Use exact field spelling and correct the arguments before retrying.",
	} {
		if !strings.Contains(longResult, want) {
			t.Fatalf("long invalid feedback clipped guidance %q", want)
		}
	}
	if strings.Contains(longResult, "TOP_SECRET_LONG_VALUE") {
		t.Fatal("long invalid feedback echoed a sensitive argument value")
	}
	correctedResult := invoke("call-corrected-edit", "{\"path\":\"src/App.tsx\",\"oldString\":\"old\",\"newString\":\"new\"}")
	if calls != 1 || !strings.Contains(correctedResult, "\"operation\":\"edit_file\"") {
		t.Fatalf("corrected invocation result = %q after %d dispatches, want one successful dispatch", correctedResult, calls)
	}
}

func TestAssistantReadAndEditPreserveLiteralSourceCharacters(t *testing.T) {
	ctx := context.Background()
	files := workspace.NewFileStore(t.TempDir())
	scope := workspace.Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-a", ProjectName: "demo", ProjectUID: "uid-literals"}
	const marker = "__LITERAL_SOURCE_MARKER__"
	if _, err := files.WriteFile(ctx, scope, workspace.WriteOptions{Path: "literal-source.txt", Content: marker + "\n"}); err != nil {
		t.Fatal(err)
	}
	server := &Server{tenantWorkspaces: defaultTestWorkspaces.lookup, tenantActors: defaultTestActors.lookup, tenantProviders: defaultTestProviders, workspaces: files}
	registry := projectAssistantLocalToolRegistry(server)
	read, _ := registry.Get(projectToolReadFile)
	edit, _ := registry.Get(projectToolEditFile)
	activeRead := newProjectEinoAssistantServerTool(server, read, projectAssistantRunRequest{WorkspaceScope: scope}, newProjectEinoAssistantRunState())
	activeInfo, err := activeRead.Info(ctx)
	if err != nil {
		t.Fatalf("build active read_file tool info: %v", err)
	}
	if !strings.Contains(activeInfo.Desc, "UTF-8 source is shown literally") || !strings.Contains(activeInfo.Desc, "decoded from JSON once") {
		t.Fatalf("active model-facing read_file description = %q; want literal-source contract", activeInfo.Desc)
	}
	activeEdit := newProjectEinoAssistantServerTool(server, edit, projectAssistantRunRequest{WorkspaceScope: scope}, newProjectEinoAssistantRunState())
	activeEditInfo, err := activeEdit.Info(ctx)
	if err != nil {
		t.Fatalf("build active edit_file tool info: %v", err)
	}
	for _, want := range []string{`JSON value '"\\path"'`, `JSON value '"\\\\path"'`} {
		if !strings.Contains(activeEditInfo.Desc, want) {
			t.Fatalf("active model-facing edit_file description = %q; want explicit one-versus-two-backslash example %q", activeEditInfo.Desc, want)
		}
	}
	decodeRead := func(raw string) string {
		t.Helper()
		var result struct {
			Content string `json:"content"`
		}
		if err := json.Unmarshal([]byte(raw), &result); err != nil {
			t.Fatalf("decode read_file result: %v (%s)", err, raw)
		}
		return result.Content
	}
	readArgs := map[string]any{"file_path": "literal-source.txt", "offset": 1, "limit": 2000}
	before, err := read.Call(ctx, projectAssistantToolCallRequest{WorkspaceScope: scope, Arguments: readArgs})
	if err != nil {
		t.Fatalf("initial read_file: %v", err)
	}
	if got := decodeRead(before); got != marker+"\n" {
		t.Fatalf("initial read content = %q, want marker unchanged", got)
	}

	// This one string covers literal single and double backslashes, regex
	// escapes, a JSON-looking escape, quotes, Unicode, and HTML-like text.
	const replacement = `one=\alpha pair=\\beta regex=\d+\s+\w+ escape=\n <tag data-x="&">café 雪</tag>`
	mutation, err := edit.Call(ctx, projectAssistantToolCallRequest{WorkspaceScope: scope, Arguments: map[string]any{
		"path": "literal-source.txt", "oldString": marker, "newString": replacement,
	}})
	if err != nil {
		t.Fatalf("edit_file: %v (%s)", err, mutation)
	}
	after, err := read.Call(ctx, projectAssistantToolCallRequest{WorkspaceScope: scope, Arguments: readArgs})
	if err != nil {
		t.Fatalf("read_file after edit: %v", err)
	}
	if got, want := decodeRead(after), replacement+"\n"; got != want {
		t.Fatalf("read-back source bytes differ\n got: %q\nwant: %q", got, want)
	}
	if strings.Contains(after, `\u003c`) {
		t.Fatalf("source read JSON unexpectedly HTML-escaped literal text: %s", after)
	}
}

func TestAssistantOrdinaryWorkspaceMutationToolsPerformCreateEditDeleteMove(t *testing.T) {
	ctx := context.Background()
	files := workspace.NewFileStore(t.TempDir())
	scope := workspace.Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-a", ProjectName: "demo", ProjectUID: "uid-a"}
	server := &Server{tenantWorkspaces: defaultTestWorkspaces.lookup, tenantActors: defaultTestActors.lookup, tenantProviders: defaultTestProviders, workspaces: files}
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
	current, err = files.ReadFile(ctx, scope, workspace.ReadOptions{Path: "src/App.tsx"})
	if err != nil {
		t.Fatal(err)
	}
	recordProjectAssistantModelVisibleReadForTest(state, current.Path, current.Version, false)
	if result, err := call(projectToolMoveFile, map[string]any{"sourcePath": "src/App.tsx", "destinationPath": "src/Main.tsx", "expectedVersion": current.Version}, state, false); err != nil || result.Operation != projectToolMoveFile || result.PreviousPath != "src/App.tsx" {
		t.Fatalf("move = %#v, %v", result, err)
	}
	current, err = files.ReadFile(ctx, scope, workspace.ReadOptions{Path: "src/Main.tsx"})
	if err != nil {
		t.Fatal(err)
	}
	recordProjectAssistantModelVisibleReadForTest(state, current.Path, current.Version, false)
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
	server := &Server{tenantWorkspaces: defaultTestWorkspaces.lookup, tenantActors: defaultTestActors.lookup, tenantProviders: defaultTestProviders, workspaces: files}
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
