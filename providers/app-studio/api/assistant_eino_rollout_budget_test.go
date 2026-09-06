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
	"io"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/eino/adk"
	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"github.com/faroshq/provider-app-studio/store"
)

func TestProjectEinoAssistantRolloutBudgetRemindersFollowCodexBoundaries(t *testing.T) {
	ctx := context.Background()
	runState := newProjectEinoAssistantRunState()
	budget := newProjectEinoAssistantRolloutBudget(100, nil, nil, nil)
	runState.SetRolloutBudget(budget)
	lifecycle := projectEinoAssistantLifecycleMiddleware(projectAssistantRunRequest{}, runState)
	state := &adk.ChatModelAgentState{}

	if _, _, err := lifecycle.BeforeModelRewriteState(ctx, state, nil); err != nil {
		t.Fatal(err)
	}
	assertProjectAssistantRolloutBudgetMessage(t, state.Messages, 1, "100 weighted tokens")

	if _, _, err := lifecycle.BeforeModelRewriteState(ctx, state, nil); err != nil {
		t.Fatal(err)
	}
	assertProjectAssistantRolloutBudgetMessage(t, state.Messages, 1, "100 weighted tokens")

	if err := budget.RecordUsage(ctx, &schema.TokenUsage{CompletionTokens: 30}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := lifecycle.BeforeModelRewriteState(ctx, state, nil); err != nil {
		t.Fatal(err)
	}
	assertProjectAssistantRolloutBudgetMessage(t, state.Messages, 2, "70 weighted tokens")

	if err := budget.RearmAfterCompaction(ctx); err != nil {
		t.Fatal(err)
	}
	if _, _, err := lifecycle.BeforeModelRewriteState(ctx, state, nil); err != nil {
		t.Fatal(err)
	}
	assertProjectAssistantRolloutBudgetMessage(t, state.Messages, 3, "70 weighted tokens")
}

func TestProjectEinoAssistantRolloutBudgetWeightsCachedInputAndStopsAfterCompletedResponse(t *testing.T) {
	ctx := context.Background()
	budget := newProjectEinoAssistantRolloutBudget(20, nil, nil, nil)
	base := &projectAssistantRolloutBudgetTestModel{usages: []*schema.TokenUsage{
		{
			PromptTokens:       40,
			PromptTokenDetails: schema.PromptTokenDetails{CachedTokens: 20},
			CompletionTokens:   10,
		},
		{CompletionTokens: 5},
	}}
	model := projectEinoAssistantBudgetModel(base, budget)

	reader, err := model.Stream(ctx, []*schema.Message{schema.UserMessage("first")})
	if err != nil {
		t.Fatal(err)
	}
	message, err := reader.Recv()
	if err != nil || message == nil || message.Content != "response" {
		t.Fatalf("first response = %#v, %v", message, err)
	}
	if _, err := reader.Recv(); !errors.Is(err, io.EOF) {
		t.Fatalf("first response terminal error = %v, want EOF", err)
	}
	reader.Close()
	if got := budget.PendingReminder(); got == nil || got.RemainingTokens != 5 {
		t.Fatalf("remaining budget = %#v, want 5", got)
	}

	reader, err = model.Stream(ctx, []*schema.Message{schema.UserMessage("second")})
	if err != nil {
		t.Fatal(err)
	}
	message, err = reader.Recv()
	if err != nil || message == nil || message.Content != "response" {
		t.Fatalf("exhausting response = %#v, %v", message, err)
	}
	if _, err := reader.Recv(); !errors.Is(err, io.EOF) {
		t.Fatalf("exhausting response terminal error = %v, want EOF", err)
	}
	reader.Close()
	if err := budget.ExhaustionError(); !projectEinoAssistantRolloutBudgetExceeded(err) {
		t.Fatalf("budget exhaustion = %v, want session budget exceeded", err)
	}
	if _, err := model.Stream(ctx, []*schema.Message{schema.UserMessage("third")}); !projectEinoAssistantRolloutBudgetExceeded(err) {
		t.Fatalf("next sampling boundary error = %v, want session budget exceeded", err)
	}
}

func TestProjectEinoAssistantRolloutBudgetDisabledConfigurationOverridesRestoredState(t *testing.T) {
	restored := &projectAssistantRolloutBudgetState{
		LimitTokens:         100,
		SamplingTokenWeight: projectAssistantRolloutBudgetSamplingTokenWeight,
		PrefillTokenWeight:  projectAssistantRolloutBudgetPrefillTokenWeight,
		WeightedTokensUsed:  30,
	}
	if budget := newProjectEinoAssistantRolloutBudget(0, restored, nil, nil); budget != nil {
		t.Fatalf("budget = %#v, want nil when the configured budget is disabled", budget)
	}
}

func TestProjectEinoAssistantRolloutBudgetPersistsThroughAuditAndCheckpoint(t *testing.T) {
	ctx := context.Background()
	run := &store.AssistantRun{ID: "run-budget", CreatedAt: time.Now().UTC()}
	recorder := newProjectAssistantRunAuditRecorder(projectAssistantRunRequest{}, run, run.CreatedAt)
	memory := store.NewMemoryStore()
	scope := store.Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo", ProjectUID: "project-a"}
	if err := memory.SaveAssistantRun(ctx, scope, store.AssistantRun{ID: run.ID, Mode: store.AssistantRunModeDefault, Status: store.AssistantRunStatusRunning, CreatedAt: run.CreatedAt, UpdatedAt: run.CreatedAt}); err != nil {
		t.Fatal(err)
	}
	persistReminder := func(ctx context.Context, reminder *projectAssistantRolloutBudgetReminder) error {
		message := projectEinoAssistantRolloutBudgetMessage(reminder)
		return appendProjectAssistantConversationMessage(ctx, memory, scope, run.ID, "rollout-budget-run-budget-1-0", projectAssistantConversationRolloutBudget, chatMessage{Role: "system", Content: message.Content})
	}
	budget := newProjectEinoAssistantRolloutBudget(100, nil, recorder, persistReminder)
	runState := newProjectEinoAssistantRunState()
	runState.SetRolloutBudget(budget)

	reminder := budget.PendingReminder()
	if err := budget.DeliverReminder(ctx, reminder); err != nil {
		t.Fatal(err)
	}
	if err := budget.DeliverReminder(ctx, reminder); err != nil {
		t.Fatal(err)
	}
	items, err := memory.ListAssistantConversationItems(ctx, scope, 0, 10)
	if err != nil || len(items) != 1 || items[0].Type != projectAssistantConversationRolloutBudget {
		t.Fatalf("durable rollout budget items = %#v, %v", items, err)
	}
	conversation, err := loadProjectAssistantConversation(ctx, memory, scope)
	if err != nil || len(conversation) != 0 {
		t.Fatalf("new-turn conversation leaked run-scoped reminder = %#v, %v", conversation, err)
	}
	if err := budget.RecordUsage(ctx, &schema.TokenUsage{CompletionTokens: 30}); err != nil {
		t.Fatal(err)
	}
	if recorder.rolloutBudgetSnapshot() == nil {
		t.Fatal("audit did not retain rollout budget state")
	}

	restoredState := newProjectEinoAssistantRunState()
	restoredState.RestoreCheckpointState(runState.CheckpointState())
	restoredBudget := newProjectEinoAssistantRolloutBudget(999, restoredState.RestoredRolloutBudget(), nil, nil)
	if got := restoredBudget.PendingReminder(); got == nil || got.RemainingTokens != 70 {
		t.Fatalf("restored reminder = %#v, want 70 remaining", got)
	}
}

func TestProjectEinoAssistantRolloutBudgetIsPerRun(t *testing.T) {
	t.Setenv(projectAssistantRolloutBudgetTokensEnv, "100")
	ctx := context.Background()
	memory := store.NewMemoryStore()
	scope := store.Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo", ProjectUID: "project-a"}
	server := &Server{store: memory}

	runOne := store.AssistantRun{ID: "run-1", Mode: store.AssistantRunModeDefault, Status: store.AssistantRunStatusCompleted, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	if err := memory.SaveAssistantRun(ctx, scope, runOne); err != nil {
		t.Fatal(err)
	}
	stateOne := newProjectEinoAssistantRunState()
	if err := projectEinoAssistantConfigureRolloutBudget(ctx, server, projectAssistantRunRequest{MessageScope: scope, AssistantRun: &runOne}, stateOne, nil); err != nil {
		t.Fatal(err)
	}
	if err := stateOne.RolloutBudget().RecordUsage(ctx, &schema.TokenUsage{CompletionTokens: 30}); err != nil {
		t.Fatal(err)
	}

	runTwo := store.AssistantRun{ID: "run-2", Mode: store.AssistantRunModeDefault, Status: store.AssistantRunStatusRunning, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	if err := memory.SaveAssistantRun(ctx, scope, runTwo); err != nil {
		t.Fatal(err)
	}
	stateTwo := newProjectEinoAssistantRunState()
	if err := projectEinoAssistantConfigureRolloutBudget(ctx, server, projectAssistantRunRequest{MessageScope: scope, AssistantRun: &runTwo}, stateTwo, nil); err != nil {
		t.Fatal(err)
	}
	fresh := stateTwo.RolloutBudget().Snapshot()
	if fresh == nil || fresh.LimitTokens != 100 || fresh.WeightedTokensUsed != 0 {
		t.Fatalf("new-run rollout budget = %#v, want a fresh 0/100", fresh)
	}
}

func TestProjectEinoAssistantRolloutBudgetDefaultsToFinitePerRunLimit(t *testing.T) {
	t.Setenv(projectAssistantRolloutBudgetTokensEnv, "")
	ctx := context.Background()
	memory := store.NewMemoryStore()
	scope := store.Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo", ProjectUID: "project-a"}
	server := &Server{store: memory}
	run := store.AssistantRun{ID: "run-default", Mode: store.AssistantRunModeDefault, Status: store.AssistantRunStatusRunning, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	if err := memory.SaveAssistantRun(ctx, scope, run); err != nil {
		t.Fatal(err)
	}
	runState := newProjectEinoAssistantRunState()
	if err := projectEinoAssistantConfigureRolloutBudget(ctx, server, projectAssistantRunRequest{MessageScope: scope, AssistantRun: &run}, runState, nil); err != nil {
		t.Fatal(err)
	}
	budget := runState.RolloutBudget()
	if budget == nil {
		t.Fatal("default configuration must install a finite rollout budget")
	}
	if got := budget.Snapshot(); got.LimitTokens != projectAssistantDefaultRolloutBudgetTokens {
		t.Fatalf("default rollout budget limit = %d, want %d", got.LimitTokens, projectAssistantDefaultRolloutBudgetTokens)
	}

	t.Setenv(projectAssistantRolloutBudgetTokensEnv, "unlimited")
	unlimitedState := newProjectEinoAssistantRunState()
	if err := projectEinoAssistantConfigureRolloutBudget(ctx, server, projectAssistantRunRequest{MessageScope: scope, AssistantRun: &run}, unlimitedState, nil); err != nil {
		t.Fatal(err)
	}
	if unlimitedState.RolloutBudget() != nil {
		t.Fatal("explicit unlimited configuration must disable the rollout budget")
	}
}

func TestProjectEinoAssistantRolloutBudgetFailureShape(t *testing.T) {
	err := &projectAssistantSessionBudgetExceededError{LimitTokens: 100, WeightedTokensUsed: 101}
	if !projectEinoAssistantBudgetLimited(err) {
		t.Fatal("rollout budget exhaustion was not classified as budget limited")
	}
	if got := projectAssistantFailureKind(err); got != "session_budget" {
		t.Fatalf("failure kind = %q, want session_budget", got)
	}
	if got := projectAssistantFailureSummary(err, "session_budget"); got != errProjectAssistantSessionBudgetExceeded.Error() {
		t.Fatalf("failure summary = %q", got)
	}
	if projectEinoAssistantBudgetLimited(adk.ErrExceedMaxIterations) {
		t.Fatal("max iterations was incorrectly classified as rollout budget exhaustion")
	}
	if !projectEinoAssistantIterationLimited(adk.ErrExceedMaxIterations) {
		t.Fatal("max iterations was not classified as iteration limited")
	}
}

func assertProjectAssistantRolloutBudgetMessage(
	t *testing.T,
	messages []*schema.Message,
	wantCount int,
	wantText string,
) {
	t.Helper()
	count := 0
	var latest string
	for _, message := range messages {
		if message != nil && strings.Contains(message.Content, "<rollout_budget>") {
			count++
			latest = message.Content
		}
	}
	if count != wantCount {
		t.Fatalf("rollout budget message count = %d, want %d", count, wantCount)
	}
	if !strings.Contains(latest, wantText) {
		t.Fatalf("latest rollout budget message = %q, want %q", latest, wantText)
	}
}

type projectAssistantRolloutBudgetTestModel struct {
	usages []*schema.TokenUsage
	calls  int
}

func (m *projectAssistantRolloutBudgetTestModel) Generate(
	context.Context,
	[]*schema.Message,
	...einomodel.Option,
) (*schema.Message, error) {
	return m.response(), nil
}

func (m *projectAssistantRolloutBudgetTestModel) Stream(
	context.Context,
	[]*schema.Message,
	...einomodel.Option,
) (*schema.StreamReader[*schema.Message], error) {
	reader, writer := schema.Pipe[*schema.Message](1)
	message := m.response()
	go func() {
		defer writer.Close()
		writer.Send(message, nil)
	}()
	return reader, nil
}

func (m *projectAssistantRolloutBudgetTestModel) response() *schema.Message {
	usage := m.usages[m.calls]
	m.calls++
	message := schema.AssistantMessage("response", nil)
	message.ResponseMeta = &schema.ResponseMeta{FinishReason: "stop", Usage: usage}
	return message
}
