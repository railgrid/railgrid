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
	"strings"
	"testing"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	aiv1alpha1 "github.com/faroshq/provider-app-studio/apis/ai/v1alpha1"
	"github.com/faroshq/provider-app-studio/workspace"
)

func TestProjectAssistantWorkspaceInstructionsLoadRootDocument(t *testing.T) {
	ctx := context.Background()
	workspaces := workspace.NewFileStore(t.TempDir())
	scope := workspace.Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: "demo", ProjectUID: "project-uid"}
	writeTestWorkspaceFiles(t, ctx, workspaces, scope, []workspace.File{
		{Path: "AGENTS.md", Content: "root guidance"},
		{Path: "services/AGENTS.md", Content: "nested guidance must be ignored"},
	})

	prompt, ok := projectAssistantWorkspaceInstructions(ctx, workspaces, scope)
	if !ok {
		t.Fatal("workspace instructions were not loaded")
	}
	for _, want := range []string{
		"Direct system and user instructions take precedence",
		"for the entire project filesystem",
		"# AGENTS.md instructions for the App Studio project root",
		"root guidance",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("workspace instruction prompt missing %q:\n%s", want, prompt)
		}
	}
	if strings.Contains(prompt, "nested guidance must be ignored") {
		t.Fatalf("workspace instruction prompt loaded a nested AGENTS.md:\n%s", prompt)
	}
}

func TestProjectAssistantWorkspaceInstructionsAreOptionalAndBounded(t *testing.T) {
	ctx := context.Background()
	workspaces := workspace.NewFileStore(t.TempDir())
	scope := workspace.Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: "demo", ProjectUID: "project-uid"}
	if prompt, ok := projectAssistantWorkspaceInstructions(ctx, workspaces, scope); ok || prompt != "" {
		t.Fatalf("missing workspace instructions = (%q, %v), want empty", prompt, ok)
	}

	writeTestWorkspaceFiles(t, ctx, workspaces, scope, []workspace.File{{
		Path:    "AGENTS.md",
		Content: strings.Repeat("a", projectAssistantInstructionsMaxBytes+1024),
	}})
	prompt, ok := projectAssistantWorkspaceInstructions(ctx, workspaces, scope)
	if !ok || !strings.Contains(prompt, "was truncated") {
		t.Fatalf("oversized workspace instructions were not loaded with a truncation marker: ok=%v", ok)
	}
	const startMarker = "<INSTRUCTIONS>\n"
	const endMarker = "\n</INSTRUCTIONS>"
	start := strings.Index(prompt, startMarker)
	end := strings.Index(prompt, endMarker)
	if start < 0 || end < start {
		t.Fatalf("bounded prompt is missing instruction markers: %q", prompt)
	}
	payload := prompt[start+len(startMarker) : end]
	if len([]byte(payload)) != projectAssistantInstructionsMaxBytes {
		t.Fatalf("bounded prompt retained %d payload bytes, want %d", len([]byte(payload)), projectAssistantInstructionsMaxBytes)
	}
}

func TestProjectAssistantLifecycleRefreshesWorkspaceInstructions(t *testing.T) {
	ctx := context.Background()
	workspaces := workspace.NewFileStore(t.TempDir())
	scope := workspace.Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: "demo", ProjectUID: "project-uid"}
	writeTestWorkspaceFiles(t, ctx, workspaces, scope, []workspace.File{{Path: "AGENTS.md", Content: "first guidance"}})
	runState := newProjectEinoAssistantRunState()
	req := projectAssistantRunRequest{
		Project:           &aiv1alpha1.Project{ObjectMeta: metav1.ObjectMeta{Name: "demo", UID: "project-uid"}},
		Workspace:         workspaces,
		WorkspaceScope:    scope,
		CollaborationMode: projectAssistantCollaborationModeDefault,
	}
	lifecycle := projectEinoAssistantLifecycleMiddleware(req, runState).(*projectEinoAssistantLifecycle)
	state := &adk.ChatModelAgentState{Messages: []*schema.Message{schema.UserMessage("build it")}}

	_, first, err := lifecycle.BeforeModelRewriteState(ctx, state, &adk.ModelContext{})
	if err != nil {
		t.Fatal(err)
	}
	if !projectAssistantMessagesContain(first.Messages, "first guidance") {
		t.Fatalf("initial model context did not contain AGENTS.md: %#v", first.Messages)
	}
	if _, err := workspaces.WriteFile(ctx, scope, workspace.WriteOptions{Path: "AGENTS.md", Content: "second guidance"}); err != nil {
		t.Fatal(err)
	}

	_, second, err := lifecycle.BeforeModelRewriteState(ctx, first, &adk.ModelContext{})
	if err != nil {
		t.Fatal(err)
	}
	if !projectAssistantMessagesContain(second.Messages, "Section: workspace_instructions\nApp Studio automatically loaded AGENTS.md") ||
		!projectAssistantMessagesContain(second.Messages, "second guidance") {
		t.Fatalf("updated model context did not refresh AGENTS.md: %#v", second.Messages)
	}
}

func projectAssistantMessagesContain(messages []*schema.Message, want string) bool {
	for _, message := range messages {
		if message != nil && strings.Contains(message.Content, want) {
			return true
		}
	}
	return false
}
