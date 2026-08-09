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
	"github.com/faroshq/provider-app-studio/store"
)

func TestProjectEinoAssistantLifecycleAppendsIncrementalLiveContextUpdates(t *testing.T) {
	runState := newProjectEinoAssistantRunState()
	runState.SetToolPrompt("tool contract v1")
	req := projectAssistantRunRequest{
		Project: &aiv1alpha1.Project{
			ObjectMeta: metav1.ObjectMeta{Name: "demo", UID: "uid"},
			Spec:       aiv1alpha1.ProjectSpec{DisplayName: "Current project"},
		},
		Repository:        &ProjectRepositoryView{Ref: "current-repo", Status: projectRepositoryStatusReady, Ready: true},
		CollaborationMode: projectAssistantCollaborationModeDefault,
	}
	state := &adk.ChatModelAgentState{Messages: []*schema.Message{
		schema.SystemMessage(projectEinoAssistantProjectPromptPrefix + "stale project"),
		schema.SystemMessage(projectEinoAssistantSessionSnapshotPrefix + " stale snapshot"),
		schema.SystemMessage("Databricks guidance: stale tool contract"),
		schema.UserMessage("build it"),
		schema.AssistantMessage("", []schema.ToolCall{{ID: "call-1", Function: schema.FunctionCall{Name: "read_file", Arguments: `{"path":"src/App.tsx"}`}}}),
		schema.ToolMessage("file contents", "call-1"),
	}}
	lifecycle := projectEinoAssistantLifecycleMiddleware(req, runState).(*projectEinoAssistantLifecycle)

	_, first, err := lifecycle.BeforeModelRewriteState(context.Background(), state, &adk.ModelContext{})
	if err != nil {
		t.Fatal(err)
	}
	assertProjectEinoAssistantLiveContext(t, first.Messages, "Current project", "current-repo", "tool contract v1")
	assertProjectEinoAssistantConversationTail(t, first.Messages)

	lifecycle.req.Project.Spec.DisplayName = "Updated project"
	lifecycle.req.Repository = &ProjectRepositoryView{Ref: "updated-repo", Status: projectRepositoryStatusReady, Ready: true}
	runState.SetToolPrompt("tool contract v2")
	_, second, err := lifecycle.BeforeModelRewriteState(context.Background(), first, &adk.ModelContext{})
	if err != nil {
		t.Fatal(err)
	}
	joinedSecond := ""
	for _, message := range second.Messages {
		joinedSecond += message.Content + "\n"
	}
	for _, want := range []string{"Updated project", "updated-repo", "tool contract v2"} {
		if !strings.Contains(joinedSecond, want) {
			t.Fatalf("incremental live context missing %q", want)
		}
	}
	assertProjectEinoAssistantConversationTail(t, second.Messages)
	var initialPromptCount, updateCount int
	for _, message := range second.Messages {
		if strings.Contains(message.Content, "Collaboration mode: default") {
			initialPromptCount++
		}
		if strings.Contains(message.Content, "Context update since the previous model sample") {
			updateCount++
		}
	}
	if initialPromptCount != 1 || updateCount == 0 {
		t.Fatalf("incremental context counts: initial=%d updates=%d", initialPromptCount, updateCount)
	}

	// Replaying the same boundary without a live-section change must not grow
	// the conversation with another copy of the stable request prefix.
	before := len(second.Messages)
	_, third, err := lifecycle.BeforeModelRewriteState(context.Background(), second, &adk.ModelContext{})
	if err != nil {
		t.Fatal(err)
	}
	if len(third.Messages) != before {
		t.Fatalf("unchanged context appended messages: before=%d after=%d", before, len(third.Messages))
	}
}

func TestProjectEinoAssistantLifecycleReinjectsContextAfterCompactionWindowReset(t *testing.T) {
	runState := newProjectEinoAssistantRunState()
	runState.SetToolPrompt("tool contract")
	req := projectAssistantRunRequest{
		Project: &aiv1alpha1.Project{
			ObjectMeta: metav1.ObjectMeta{Name: "demo", UID: "uid"},
			Spec:       aiv1alpha1.ProjectSpec{DisplayName: "Current project"},
		},
		Repository:        &ProjectRepositoryView{Ref: "current-repo", Status: projectRepositoryStatusReady, Ready: true},
		CollaborationMode: projectAssistantCollaborationModeDefault,
	}
	lifecycle := projectEinoAssistantLifecycleMiddleware(req, runState).(*projectEinoAssistantLifecycle)
	state := &adk.ChatModelAgentState{Messages: []*schema.Message{schema.UserMessage("build it")}}

	_, initialized, err := lifecycle.BeforeModelRewriteState(context.Background(), state, &adk.ModelContext{})
	if err != nil {
		t.Fatal(err)
	}
	if initialized.Messages[0].Role != schema.System {
		t.Fatalf("initial live context role = %q, want system", initialized.Messages[0].Role)
	}

	// Simulate Codex-style compaction replacement: the previous live context
	// is gone and only the conversational checkpoint remains.
	runState.InvalidateModelContext()
	initialized.Messages = []*schema.Message{schema.UserMessage("Compacted conversation context:\ncontinue")}
	_, reinjected, err := lifecycle.BeforeModelRewriteState(context.Background(), initialized, &adk.ModelContext{})
	if err != nil {
		t.Fatal(err)
	}
	joined := ""
	var liveMessageCount int
	for _, message := range reinjected.Messages {
		joined += message.Content + "\n"
		if message.Role == schema.System && strings.HasPrefix(message.Content, projectEinoAssistantLiveContextPrefix) {
			liveMessageCount++
		}
	}
	for _, want := range []string{"Current project", "current-repo", "tool contract"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("reinjected live context missing %q: %s", want, joined)
		}
	}
	if liveMessageCount != 3 {
		t.Fatalf("reinjected live context message count = %d, want three canonical sections", liveMessageCount)
	}
	if reinjected.Messages[len(reinjected.Messages)-1].Content != "Compacted conversation context:\ncontinue" {
		t.Fatalf("compacted conversation tail = %#v, want preserved summary", reinjected.Messages[len(reinjected.Messages)-1])
	}
	var projectPromptCount int
	for _, message := range reinjected.Messages {
		if strings.Contains(message.Content, "Collaboration mode: default") {
			projectPromptCount++
		}
	}
	if projectPromptCount != 1 {
		t.Fatalf("reinjected project prompt count = %d, want one full prompt", projectPromptCount)
	}
}

func TestProjectEinoAssistantStableToolInfoOrderIsIndependentOfDiscoveryOrder(t *testing.T) {
	first := []*schema.ToolInfo{
		{Name: "mcp_zeta", Desc: "zeta"},
		{Name: "read_file", Desc: "read"},
		{Name: "mcp_alpha", Desc: "alpha"},
	}
	second := []*schema.ToolInfo{first[2], first[0], first[1]}
	orderedFirst := projectEinoAssistantStableToolInfos(first)
	orderedSecond := projectEinoAssistantStableToolInfos(second)
	if len(orderedFirst) != len(orderedSecond) {
		t.Fatalf("stable tool lengths = %d/%d", len(orderedFirst), len(orderedSecond))
	}
	for i := range orderedFirst {
		if orderedFirst[i].Name != orderedSecond[i].Name {
			t.Fatalf("stable tool order differs at %d: %q vs %q", i, orderedFirst[i].Name, orderedSecond[i].Name)
		}
	}
	for i, want := range []string{"mcp_alpha", "mcp_zeta", "read_file"} {
		if orderedFirst[i].Name != want {
			t.Fatalf("stable tool order = %#v, want %v", orderedFirst, []string{"mcp_alpha", "mcp_zeta", "read_file"})
		}
	}
}

func TestProjectEinoAssistantLifecycleRefreshesModelAndExecutableToolSnapshotTogether(t *testing.T) {
	server := New(nil, store.NewMemoryStore(), "", false)
	defer server.Shutdown(context.Background())
	runState := newProjectEinoAssistantRunState()
	req := projectAssistantRunRequest{
		Project:           &aiv1alpha1.Project{ObjectMeta: metav1.ObjectMeta{Name: "demo", UID: "uid"}},
		CollaborationMode: projectAssistantCollaborationModeDefault,
	}
	toolV1 := projectAssistantToolFunc{spec: projectAssistantToolSpec{Name: "mcp_alpha", Description: "alpha", Risk: projectAssistantToolRiskRead}}
	toolV2 := projectAssistantToolFunc{spec: projectAssistantToolSpec{Name: "mcp_beta", Description: "beta", Risk: projectAssistantToolRiskRead}}
	runState.SetToolDiscovery(projectEinoAssistantToolDiscovery{MCPTools: []projectAssistantTool{toolV1}, Prompt: "alpha"})
	lifecycle := projectEinoAssistantLifecycleMiddleware(req, runState, server).(*projectEinoAssistantLifecycle)
	state := &adk.ChatModelAgentState{ToolInfos: []*schema.ToolInfo{{Name: "write_todos"}}}
	if err := lifecycle.refreshExecutableToolContext(context.Background(), state, &adk.ModelContext{}); err != nil {
		t.Fatal(err)
	}
	assertProjectEinoAssistantToolInfoPresence(t, state.ToolInfos, "write_todos", true)
	assertProjectEinoAssistantToolInfoPresence(t, state.ToolInfos, "mcp_alpha", true)

	runState.SetToolDiscovery(projectEinoAssistantToolDiscovery{MCPTools: []projectAssistantTool{toolV2}, Prompt: "beta"})
	modelCtx := &adk.ModelContext{}
	if err := lifecycle.refreshExecutableToolContext(context.Background(), state, modelCtx); err != nil {
		t.Fatal(err)
	}
	assertProjectEinoAssistantToolInfoPresence(t, state.ToolInfos, "write_todos", true)
	assertProjectEinoAssistantToolInfoPresence(t, state.ToolInfos, "mcp_alpha", false)
	assertProjectEinoAssistantToolInfoPresence(t, state.ToolInfos, "mcp_beta", true)
	assertProjectEinoAssistantToolInfoPresence(t, modelCtx.Tools, "mcp_beta", true)
	if _, ok := projectEinoAssistantCurrentDynamicTool(server, req, runState, "mcp_alpha"); ok {
		t.Fatal("withdrawn mcp_alpha remained executable")
	}
	if tool, ok := projectEinoAssistantCurrentDynamicTool(server, req, runState, "mcp_beta"); !ok || !tool.availableInCurrentDiscovery() {
		t.Fatalf("current mcp_beta executable = (%#v, %v), want available", tool, ok)
	}
	staleWrapper := projectEinoAssistantTool{server: server, tool: projectAssistantToolFunc{spec: projectAssistantToolSpec{Name: "mcp_beta", Description: "stale", Risk: projectAssistantToolRiskRead}}, req: req, runState: runState, discoveredMCPBound: true}
	current, ok := staleWrapper.currentDiscoveryTool()
	if !ok || current.Spec().Description != "beta" {
		t.Fatalf("current same-name tool = (%#v, %v), want refreshed beta implementation", current, ok)
	}
}

func assertProjectEinoAssistantToolInfoPresence(t *testing.T, infos []*schema.ToolInfo, name string, want bool) {
	t.Helper()
	found := false
	for _, info := range infos {
		if info != nil && info.Name == name {
			found = true
			break
		}
	}
	if found != want {
		t.Fatalf("tool %q present = %v, want %v in %#v", name, found, want, infos)
	}
}

func assertProjectEinoAssistantLiveContext(t *testing.T, messages []*schema.Message, displayName, repositoryRef, toolPrompt string) {
	t.Helper()
	if len(messages) < 6 {
		t.Fatalf("model messages = %#v, want live context plus conversation", messages)
	}
	joined := ""
	for _, message := range messages[:3] {
		if message.Role != schema.System || !strings.HasPrefix(message.Content, projectEinoAssistantLiveContextPrefix) {
			t.Fatalf("live context prefix = %#v, want tagged system message", message)
		}
		joined += message.Content + "\n"
	}
	for _, want := range []string{displayName, repositoryRef, toolPrompt} {
		if !strings.Contains(joined, want) {
			t.Fatalf("live context %q missing %q", joined, want)
		}
	}
}

func assertProjectEinoAssistantConversationTail(t *testing.T, messages []*schema.Message) {
	t.Helper()
	if len(messages) < 3 {
		t.Fatalf("model messages = %#v, want conversation tail", messages)
	}
	tail := messages[len(messages)-3:]
	if tail[0].Role != schema.User || tail[0].Content != "build it" ||
		tail[1].Role != schema.Assistant || len(tail[1].ToolCalls) != 1 || tail[1].ToolCalls[0].ID != "call-1" ||
		tail[2].Role != schema.Tool || tail[2].ToolCallID != "call-1" || tail[2].Content != "file contents" {
		t.Fatalf("conversation tail = %#v, want unchanged user/tool sequence", tail)
	}
}
