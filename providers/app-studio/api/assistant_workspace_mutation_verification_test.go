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
	"errors"
	"strings"
	"testing"

	"github.com/faroshq/provider-app-studio/workspace"
)

func TestProjectAssistantNoOpMutationsDoNotAdvanceWorkspaceRevision(t *testing.T) {
	ctx := context.Background()
	store := workspace.NewFileStore(t.TempDir())
	scope := workspace.Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-a", ProjectName: "demo", ProjectUID: "uid-a"}
	if _, err := store.WriteFile(ctx, scope, workspace.WriteOptions{Path: "src/app.ts", Content: "one\n"}); err != nil {
		t.Fatal(err)
	}
	before, err := store.SourceRevision(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	current, err := store.ReadFile(ctx, scope, workspace.ReadOptions{Path: "src/app.ts"})
	if err != nil {
		t.Fatal(err)
	}
	write, err := store.ReplaceFile(ctx, scope, workspace.ReplaceOptions{Path: "src/app.ts", Content: "one\n", ExpectedVersion: current.Version})
	if err != nil {
		t.Fatal(err)
	}
	if write.Changed {
		t.Fatal("identical write reported changed=true")
	}
	if _, err := store.EditFile(ctx, scope, workspace.EditOptions{Path: "src/app.ts", OldString: "one", NewString: "one", ExpectedVersion: current.Version}); err == nil {
		t.Fatal("identity edit returned nil error")
	} else {
		var mutationErr *workspace.MutationError
		if !errors.As(err, &mutationErr) || mutationErr.Code != workspace.MutationErrorNoChanges {
			t.Fatalf("identity edit error = %v, want no_changes", err)
		}
	}
	after, err := store.SourceRevision(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatalf("source revision after no-op mutations = %d, want unchanged %d", after, before)
	}
}

func TestProjectAssistantNoOpMutationDoesNotRequireDevelopmentSync(t *testing.T) {
	result := `{"operation":"replace_file","changed":false,"path":"src/app.ts"}`
	event := projectToolCallStreamEvent{
		Name:     projectToolReplaceFile,
		Status:   "succeeded",
		Mutation: projectAssistantMutationFromResult(projectToolReplaceFile, result),
	}
	if event.Mutation == nil || event.Mutation.Changed {
		t.Fatalf("decoded no-op mutation = %#v, want changed=false", event.Mutation)
	}
	server := NewWithWorkspace(nil, nil, workspace.NewFileStore(t.TempDir()), "", false)
	if server.projectAssistantPreviewRefreshNeeded(context.Background(), workspace.Scope{}, "", false, []projectToolCallStreamEvent{event}) {
		t.Fatal("no-op mutation requested development/preview synchronization")
	}
	if got := strings.Join(event.Mutation.Paths, ","); got != "" {
		t.Fatalf("no-op mutation paths = %q, want empty", got)
	}
}
