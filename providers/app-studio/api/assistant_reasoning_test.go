// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package api

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/railgrid/provider-app-studio/store"
	"github.com/railgrid/provider-app-studio/workspace"
)

func TestProjectAssistantReasoningResolutionPrecedence(t *testing.T) {
	tests := []struct {
		name        string
		requested   *string
		preferences map[string]string
		wantConfig  string
		wantRequest *string
	}{
		{name: "saved default", wantConfig: "medium"},
		{name: "thread preference", preferences: map[string]string{"model-a": "high"}, wantConfig: "high"},
		{name: "explicit effort overrides preference", requested: reasoningEffortPointer("none"), preferences: map[string]string{"model-a": "high"}, wantConfig: "none", wantRequest: reasoningEffortPointer("none")},
		{name: "explicit empty chooses saved default", requested: reasoningEffortPointer(""), preferences: map[string]string{"model-a": "high"}, wantConfig: "medium", wantRequest: reasoningEffortPointer("")},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			settings := projectLLMSettings{Provider: defaultProjectLLMProvider, Model: "gpt-6-luna", ReasoningEffort: "medium"}
			snapshot, err := resolveProjectAssistantReasoningForTurn(&settings, tc.requested, tc.preferences, "model-a")
			if err != nil {
				t.Fatalf("resolve reasoning: %v", err)
			}
			if settings.ReasoningEffort != tc.wantConfig || snapshot.ConfiguredEffort != tc.wantConfig {
				t.Fatalf("configured effort = %q, snapshot = %q, want %q", settings.ReasoningEffort, snapshot.ConfiguredEffort, tc.wantConfig)
			}
			if !sameOptionalString(snapshot.RequestedEffort, tc.wantRequest) {
				t.Fatalf("requested effort snapshot = %#v, want %#v", snapshot.RequestedEffort, tc.wantRequest)
			}
			if snapshot.EffectiveEffort != tc.wantConfig || snapshot.Transport != projectLLMTransportResponses {
				t.Fatalf("effective reasoning snapshot = %#v", snapshot)
			}
		})
	}
}

func TestProjectAssistantReasoningResolutionRejectsNonCanonicalOrUnsupportedEffort(t *testing.T) {
	tests := []struct {
		name        string
		settings    projectLLMSettings
		requested   *string
		preferences map[string]string
	}{
		{
			name:      "whitespace is not normalized",
			settings:  projectLLMSettings{Provider: defaultProjectLLMProvider, Model: "gpt-6-luna"},
			requested: reasoningEffortPointer(" high "),
		},
		{
			name:      "unknown effort",
			settings:  projectLLMSettings{Provider: defaultProjectLLMProvider, Model: "gpt-6-luna"},
			requested: reasoningEffortPointer("extreme"),
		},
		{
			name:      "custom endpoint",
			settings:  projectLLMSettings{Provider: defaultProjectLLMProvider, BaseURL: "https://gateway.example.test/v1", Model: "gpt-6-luna"},
			requested: reasoningEffortPointer("high"),
		},
		{
			name:     "unsupported saved thread preference",
			settings: projectLLMSettings{Provider: defaultProjectLLMProvider, Model: "gpt-6-luna"},
			// A formerly valid per-thread preference can outlive model edits.
			preferences: map[string]string{"model-a": " high "},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := resolveProjectAssistantReasoningForTurn(&tc.settings, tc.requested, tc.preferences, "model-a"); err == nil {
				t.Fatal("invalid reasoning effort unexpectedly accepted")
			}
		})
	}
}

func TestProjectAssistantReasoningSnapshotPreservesRequestAndLegacyBehavior(t *testing.T) {
	explicitEmpty := ""
	run := store.AssistantRun{}
	want := &projectAssistantAuditReasoning{
		RequestedEffort:  &explicitEmpty,
		ConfiguredEffort: "",
		EffectiveEffort:  "",
		Transport:        projectLLMTransportChatCompletions,
	}
	if err := bindProjectAssistantStartReasoningAudit(&run, want); err != nil {
		t.Fatalf("bind reasoning snapshot: %v", err)
	}
	got := projectAssistantReasoningFromRunAudit(run)
	if got == nil || got.RequestedEffort == nil || *got.RequestedEffort != "" || got.Transport != want.Transport {
		t.Fatalf("decoded reasoning snapshot = %#v, want explicit empty request", got)
	}

	settings := projectLLMSettings{ReasoningEffort: "high", ReasoningTransport: projectLLMTransportResponses}
	applyProjectAssistantReasoningSnapshot(&settings, run)
	if settings.ReasoningEffort != "" || settings.ReasoningTransport != projectLLMTransportChatCompletions {
		t.Fatalf("restored explicit model default = %#v", settings)
	}

	legacy := store.AssistantRun{Audit: json.RawMessage(`{"modelID":"model-a"}`)}
	settings = projectLLMSettings{ReasoningEffort: "high", ReasoningTransport: projectLLMTransportResponses}
	applyProjectAssistantReasoningSnapshot(&settings, legacy)
	if settings.ReasoningEffort != "" || settings.ReasoningTransport != "" {
		t.Fatalf("legacy run inherited new reasoning settings: %#v", settings)
	}
	legacySettings := projectLLMSettings{
		Provider: defaultProjectLLMProvider, BaseURL: "https://api.openai.com/v1", Model: "gpt-6-luna",
		ReasoningEffort: "high", APIKey: "test-credential",
	}
	applyProjectAssistantReasoningSnapshot(&legacySettings, legacy)
	if err := normalizeProjectLLMSettings(&legacySettings); err != nil {
		t.Fatalf("normalize legacy settings after clearing current default: %v", err)
	}
	if legacySettings.ReasoningEffort != "" {
		t.Fatalf("legacy settings retained newly added default effort %q", legacySettings.ReasoningEffort)
	}
}

func TestProjectAssistantReasoningAutomaticLegacyEffortRestoresConfiguredValue(t *testing.T) {
	settings := projectLLMSettings{
		Provider: defaultProjectLLMProvider, BaseURL: "https://gateway.example.test/v1", Model: "gpt-5.6-luna",
	}
	snapshot, err := resolveProjectAssistantReasoningForTurn(&settings, nil, nil, "model-a")
	if err != nil {
		t.Fatalf("resolve automatic effort: %v", err)
	}
	if snapshot.ConfiguredEffort != "" || snapshot.EffectiveEffort != "none" || snapshot.Transport != projectLLMTransportChatCompletions {
		t.Fatalf("automatic reasoning snapshot = %#v", snapshot)
	}

	run := store.AssistantRun{}
	if err := bindProjectAssistantStartReasoningAudit(&run, snapshot); err != nil {
		t.Fatalf("bind automatic reasoning snapshot: %v", err)
	}
	restored := projectLLMSettings{Provider: settings.Provider, BaseURL: settings.BaseURL, Model: settings.Model, ReasoningEffort: "high"}
	restored.APIKey = "test-credential"
	applyProjectAssistantReasoningSnapshot(&restored, run)
	if err := normalizeProjectLLMSettings(&restored); err != nil {
		t.Fatalf("normalize restored automatic settings: %v", err)
	}
	if restored.ReasoningEffort != "" || projectLLMTransport(restored) != projectLLMTransportChatCompletions || projectLLMEffectiveReasoningEffort(restored) != "none" {
		t.Fatalf("restored automatic reasoning settings = %#v", restored)
	}
}

func TestProjectAssistantReasoningReplayUsesStoredSnapshot(t *testing.T) {
	ctx := context.Background()
	messages := store.NewMemoryStore()
	server := NewWithWorkspace(nil, messages, workspace.NewFileStore(t.TempDir()), "", false)
	server.tenantWorkspaces = defaultTestWorkspaces.lookup
	server.tenantActors = defaultTestActors.lookup
	scope := store.Scope{OrgUUID: "org", WorkspaceUUID: "workspace", ProjectName: "demo", ProjectUID: "uid"}
	thread := store.AssistantThread{ID: "thread-reasoning-replay", ActorID: "alice", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	if _, err := messages.CreateAssistantThread(ctx, scope, thread, nil); err != nil {
		t.Fatal(err)
	}
	original := &projectAssistantAuditReasoning{
		ConfiguredEffort: "medium", EffectiveEffort: "medium", Transport: projectLLMTransportResponses,
	}
	selection := projectAssistantDurableSkillSelection{ModelID: "model-a", ModelRevisionID: "revision-a", Reasoning: original}
	start := func(store.AssistantRun, store.Message, bool) error { return nil }
	first, err := server.startProjectAssistantRunDurablyForThread(ctx, scope, thread.ID, "alice", "hello", "reasoning-replay-1", store.AssistantRunModeDefault, selection, start)
	if err != nil || !first.Started {
		t.Fatalf("first durable start = %#v, %v", first, err)
	}

	// A changed model default or thread preference is resolved only for a new
	// admission. An omitted retry keeps the original audit snapshot.
	selection.Reasoning = &projectAssistantAuditReasoning{
		ConfiguredEffort: "high", EffectiveEffort: "high", Transport: projectLLMTransportResponses,
	}
	replay, err := server.startProjectAssistantRunDurablyForThread(ctx, scope, thread.ID, "alice", "hello", "reasoning-replay-1", store.AssistantRunModeDefault, selection, start)
	if err != nil || replay.Started || replay.Run.ID != first.Run.ID {
		t.Fatalf("omitted reasoning replay = %#v, %v", replay, err)
	}
	stored := projectAssistantReasoningFromRunAudit(replay.Run)
	if stored == nil || stored.ConfiguredEffort != "medium" || stored.EffectiveEffort != "medium" {
		t.Fatalf("replay changed stored reasoning snapshot: %#v", stored)
	}

	if err := validateProjectAssistantExplicitReasoningReplay(replay.Run, reasoningEffortPointer("high")); !errors.Is(err, store.ErrAssistantRunConflict) {
		t.Fatalf("conflicting explicit replay error = %v", err)
	}
}

func TestProjectAssistantReasoningExplicitReplayComparesRequestPresence(t *testing.T) {
	explicitEmpty := ""
	run := store.AssistantRun{}
	if err := bindProjectAssistantStartReasoningAudit(&run, &projectAssistantAuditReasoning{
		RequestedEffort: &explicitEmpty, ConfiguredEffort: "medium", EffectiveEffort: "medium", Transport: projectLLMTransportResponses,
	}); err != nil {
		t.Fatal(err)
	}
	if err := validateProjectAssistantExplicitReasoningReplay(run, reasoningEffortPointer("")); err != nil {
		t.Fatalf("same explicit empty replay rejected: %v", err)
	}
	if err := validateProjectAssistantExplicitReasoningReplay(run, nil); err != nil {
		t.Fatalf("omitted retry rejected: %v", err)
	}
	if err := validateProjectAssistantExplicitReasoningReplay(run, reasoningEffortPointer("medium")); !errors.Is(err, store.ErrAssistantRunConflict) {
		t.Fatalf("different explicit effort error = %v", err)
	}
	legacy := store.AssistantRun{Audit: json.RawMessage(`{"modelID":"model-a"}`)}
	if err := validateProjectAssistantExplicitReasoningReplay(legacy, reasoningEffortPointer("high")); !errors.Is(err, store.ErrAssistantRunConflict) {
		t.Fatalf("explicit choice against legacy run error = %v", err)
	}
}

func reasoningEffortPointer(value string) *string { return &value }

func sameOptionalString(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}
