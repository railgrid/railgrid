// Copyright 2026 The Faros Authors.
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
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/components"
	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	aiv1alpha1 "github.com/faroshq/provider-app-studio/apis/ai/v1alpha1"
	"github.com/faroshq/provider-app-studio/store"
)

const projectEinoAssistantCompactionTestMessages = 128

type projectEinoAssistantCompactionTestModel struct {
	input          []*schema.Message
	maxTokens      int
	tools          []*schema.ToolInfo
	deferredTools  []*schema.ToolInfo
	toolSearchTool *schema.ToolInfo
	toolChoice     *schema.ToolChoice
	response       *schema.Message
	err            error
}

type projectEinoAssistantCompactionOverflowModel struct {
	maxMessages int
	inputs      [][]*schema.Message
	calls       int
}

type projectEinoAssistantNormalOverflowModel struct {
	maxMessages int
	inputs      [][]*schema.Message
}

func (m *projectEinoAssistantNormalOverflowModel) Generate(
	ctx context.Context,
	input []*schema.Message,
	_ ...einomodel.Option,
) (*schema.Message, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.inputs = append(m.inputs, append([]*schema.Message(nil), input...))
	if len(input) > m.maxMessages {
		return nil, errors.New("maximum context length exceeded")
	}
	return schema.AssistantMessage("recovered", nil), nil
}

func (m *projectEinoAssistantNormalOverflowModel) Stream(
	ctx context.Context,
	input []*schema.Message,
	opts ...einomodel.Option,
) (*schema.StreamReader[*schema.Message], error) {
	message, err := m.Generate(ctx, input, opts...)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.Message{message}), nil
}

func TestProjectEinoAssistantNormalModelOverflowRetriesBoundedReduction(t *testing.T) {
	base := &projectEinoAssistantNormalOverflowModel{maxMessages: 3}
	main, _ := projectEinoAssistantModels(base, projectAssistantRunRequest{}, newProjectEinoAssistantRunState())
	input := []*schema.Message{
		schema.SystemMessage("system authority"),
		schema.UserMessage("old request"),
		schema.AssistantMessage("", []schema.ToolCall{{ID: "call-1", Function: schema.FunctionCall{Name: projectToolReadFile}}}),
		schema.ToolMessage(`{"path":"old","complete":false}`, "call-1"),
		schema.UserMessage("latest request"),
		schema.SystemMessage("ephemeral progress reminder"),
	}
	reader, err := main.Stream(context.Background(), input)
	if err != nil {
		t.Fatalf("normal model overflow recovery: %v", err)
	}
	defer reader.Close()
	message, err := reader.Recv()
	if err != nil || message.Content != "recovered" {
		t.Fatalf("recovered stream = %#v, err=%v", message, err)
	}
	if len(base.inputs) != 3 {
		t.Fatalf("normal model calls = %d, want initial plus two bounded reductions", len(base.inputs))
	}
	last := base.inputs[len(base.inputs)-1]
	if len(last) != 3 || last[0].Role != schema.System || last[1].Content != "latest request" || last[2].Content != "ephemeral progress reminder" {
		t.Fatalf("reduced normal input = %#v, want system authority, latest request, and ephemeral reminder", last)
	}
}

func TestProjectEinoAssistantNormalModelOverflowHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	base := &projectEinoAssistantNormalOverflowModel{maxMessages: 0}
	model := projectEinoAssistantModelWithContextRecovery(base)
	_, err := model.Generate(ctx, []*schema.Message{schema.UserMessage("request")})
	if !errors.Is(err, context.Canceled) || len(base.inputs) != 0 {
		t.Fatalf("canceled recovery = %v with %d calls, want context cancellation before retry", err, len(base.inputs))
	}
}

func (m *projectEinoAssistantCompactionOverflowModel) Generate(
	ctx context.Context,
	input []*schema.Message,
	_ ...einomodel.Option,
) (*schema.Message, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.calls++
	m.inputs = append(m.inputs, append([]*schema.Message(nil), input...))
	if len(input) > m.maxMessages {
		return nil, &projectEinoAssistantContextWindowExceededError{Cause: errors.New("maximum context length exceeded")}
	}
	return schema.AssistantMessage("compacted", nil), nil
}

func (*projectEinoAssistantCompactionOverflowModel) Stream(
	context.Context,
	[]*schema.Message,
	...einomodel.Option,
) (*schema.StreamReader[*schema.Message], error) {
	return nil, errors.New("unexpected compaction stream")
}

func (m *projectEinoAssistantCompactionTestModel) Generate(
	ctx context.Context,
	input []*schema.Message,
	opts ...einomodel.Option,
) (*schema.Message, error) {
	ctx = callbacks.EnsureRunInfo(ctx, "compaction-test-model", components.ComponentOfChatModel)
	ctx = callbacks.OnStart(ctx, &einomodel.CallbackInput{Messages: input})
	m.input = append([]*schema.Message(nil), input...)
	options := einomodel.GetCommonOptions(nil, opts...)
	if options.MaxTokens != nil {
		m.maxTokens = *options.MaxTokens
	}
	m.tools = options.Tools
	m.deferredTools = options.DeferredTools
	m.toolSearchTool = options.ToolSearchTool
	m.toolChoice = options.ToolChoice
	if m.response != nil || m.err != nil {
		if m.err != nil {
			callbacks.OnError(ctx, m.err)
		} else {
			callbacks.OnEnd(ctx, &einomodel.CallbackOutput{Message: m.response})
		}
		return m.response, m.err
	}
	response := schema.AssistantMessage("Keep the implementation state and continue with focused verification.", nil)
	callbacks.OnEnd(ctx, &einomodel.CallbackOutput{Message: response})
	return response, nil
}

func (*projectEinoAssistantCompactionTestModel) Stream(
	context.Context,
	[]*schema.Message,
	...einomodel.Option,
) (*schema.StreamReader[*schema.Message], error) {
	return nil, errors.New("unexpected compaction stream")
}

func TestProjectEinoAssistantCompactionPersistsExactReplacementHistory(t *testing.T) {
	t.Setenv(projectEinoAssistantModelContextTokensEnv, "128")
	ctx := context.Background()
	memoryStore := store.NewMemoryStore()
	scope := store.Scope{OrgUUID: "org", WorkspaceUUID: "workspace", ProjectName: "demo", ProjectUID: "uid"}
	req := projectAssistantRunRequest{
		Project: &aiv1alpha1.Project{
			ObjectMeta: metav1.ObjectMeta{Name: "demo", UID: "uid"},
			Spec:       aiv1alpha1.ProjectSpec{DisplayName: "Demo"},
		},
		MessageScope:      scope,
		AssistantRun:      &store.AssistantRun{ID: "run-1"},
		CollaborationMode: projectAssistantCollaborationModeDefault,
	}
	model := &projectEinoAssistantCompactionTestModel{}
	runState := newProjectEinoAssistantRunState()
	runState.SetToolPrompt("Current tool contract")
	middleware, err := projectEinoAssistantCompactionMiddleware(ctx, model, &Server{store: memoryStore}, req, runState)
	if err != nil {
		t.Fatal(err)
	}
	original := make([]*schema.Message, 0, projectEinoAssistantCompactionTestMessages+1)
	for index := 0; index <= projectEinoAssistantCompactionTestMessages; index++ {
		original = append(original, schema.UserMessage("user request"))
	}
	state := &adk.ChatModelAgentState{Messages: original}
	_, compacted, err := middleware.BeforeModelRewriteState(ctx, state, &adk.ModelContext{})
	if err != nil {
		t.Fatal(err)
	}
	if model.maxTokens != 4096 {
		t.Fatalf("compaction max tokens = %d, want 4096", model.maxTokens)
	}
	if len(model.input) != len(original)+1 || model.input[len(model.input)-1].Content != projectEinoAssistantCompactionPrompt {
		t.Fatalf("compaction model input did not preserve exact history followed by checkpoint prompt")
	}
	projection, err := loadProjectAssistantConversationProjection(ctx, memoryStore, scope)
	if err != nil {
		t.Fatal(err)
	}
	if projection.compactionCheckpoint == nil {
		t.Fatal("durable compaction checkpoint is missing")
	}
	want := projectEinoMessagesToChat(compacted.Messages)
	if !reflect.DeepEqual(projection.messages, want) {
		t.Fatalf("persisted replacement history differs from finalized model history\ngot:  %#v\nwant: %#v", projection.messages, want)
	}
	if got := runState.ModelContextGeneration(); got != 1 {
		t.Fatalf("model context generation = %d, want one invalidation after compaction", got)
	}
}

func TestProjectEinoAssistantCompactionContextOverflowEvictsOldestHistory(t *testing.T) {
	base := &projectEinoAssistantCompactionOverflowModel{maxMessages: 3}
	model := &projectEinoAssistantCompactionIsolatedModel{BaseChatModel: base}
	input := []*schema.Message{
		schema.SystemMessage("stable instructions"),
		schema.UserMessage("old request"),
		schema.AssistantMessage("old answer", nil),
		schema.UserMessage("latest request"),
		schema.UserMessage(projectEinoAssistantCompactionPrompt),
	}
	response, err := model.Generate(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if response == nil || response.Content != "compacted" {
		t.Fatalf("compaction response = %#v, want successful retry", response)
	}
	if base.calls != 3 {
		t.Fatalf("context recovery calls = %d, want initial call plus two bounded evictions", base.calls)
	}
	if len(base.inputs) != 3 {
		t.Fatalf("captured inputs = %d, want one per recovery attempt", len(base.inputs))
	}
	last := base.inputs[len(base.inputs)-1]
	if len(last) != 3 || last[0].Role != schema.System || last[1].Content != "latest request" || last[2].Content != projectEinoAssistantCompactionPrompt {
		t.Fatalf("trimmed compaction input = %#v, want system/latest request/checkpoint prompt", last)
	}
}

func TestProjectEinoAssistantCompactionContextOverflowHonorsCancellation(t *testing.T) {
	base := &projectEinoAssistantCompactionOverflowModel{maxMessages: 0}
	model := &projectEinoAssistantCompactionIsolatedModel{BaseChatModel: base}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := model.Generate(ctx, []*schema.Message{schema.UserMessage("request")})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled compaction error = %v, want context.Canceled", err)
	}
	if base.calls != 0 {
		t.Fatalf("canceled compaction calls = %d, want zero", base.calls)
	}
}

func TestProjectEinoAssistantCompactionModelExcludesMainTurnStatefulWrappers(t *testing.T) {
	base := &projectEinoAssistantCompactionTestModel{response: schema.AssistantMessage("compacted", nil)}
	runState := newProjectEinoAssistantRunState()
	mainModel, compactionModel := projectEinoAssistantModels(base, projectAssistantRunRequest{}, runState)
	if _, ok := mainModel.(*projectEinoAssistantProgressReminderModel); !ok {
		t.Fatalf("main model = %T, want progress-reminder outer wrapper", mainModel)
	}
	if _, ok := compactionModel.(*projectEinoAssistantBoundedModel); !ok {
		t.Fatalf("compaction model = %T, want bounded provider without main-turn wrappers", compactionModel)
	}
	response, err := compactionModel.Generate(context.Background(), []*schema.Message{schema.UserMessage("summarize")})
	if err != nil {
		t.Fatal(err)
	}
	if response == nil || response.Content != "compacted" {
		t.Fatalf("compaction response = %#v", response)
	}
	if runState.CurrentModelCallOrdinal() != 0 || runState.AcceptedProgressCount() != 0 {
		t.Fatalf("compaction mutated main-turn progress state: ordinal=%d accepted=%d", runState.CurrentModelCallOrdinal(), runState.AcceptedProgressCount())
	}
}

func TestProjectEinoAssistantContextOverflowAndCancellationAreNotTransientRetries(t *testing.T) {
	for _, err := range []error{
		&projectEinoAssistantContextWindowExceededError{},
		context.Canceled,
		adk.ErrStreamCanceled,
	} {
		if projectEinoAssistantShouldRetryModelError(err) {
			t.Fatalf("error %v was classified as transient retry", err)
		}
	}
}

func TestProjectEinoAssistantCompactionToolOnlyResponsePersistsInertCheckpoint(t *testing.T) {
	t.Setenv(projectEinoAssistantModelContextTokensEnv, "128")
	ctx := context.Background()
	memoryStore := store.NewMemoryStore()
	scope := store.Scope{OrgUUID: "org", WorkspaceUUID: "workspace", ProjectName: "demo", ProjectUID: "uid"}
	run := &store.AssistantRun{ID: "run-1"}
	recorder := newProjectAssistantRunAuditRecorder(projectAssistantRunRequest{}, run, time.Now().UTC())
	req := projectAssistantRunRequest{
		Project: &aiv1alpha1.Project{
			ObjectMeta: metav1.ObjectMeta{Name: "demo", UID: "uid"},
			Spec:       aiv1alpha1.ProjectSpec{DisplayName: "Demo"},
		},
		MessageScope:      scope,
		AssistantRun:      run,
		CollaborationMode: projectAssistantCollaborationModeDefault,
		auditRecorder:     recorder,
	}
	model := &projectEinoAssistantCompactionTestModel{response: schema.AssistantMessage("", []schema.ToolCall{{
		ID: "must-not-dispatch",
		Function: schema.FunctionCall{
			Name:      "edit_file",
			Arguments: `{"patch":"must not persist"}`,
		},
	}})}
	middleware, err := projectEinoAssistantCompactionMiddleware(ctx, model, &Server{store: memoryStore}, req, newProjectEinoAssistantRunState())
	if err != nil {
		t.Fatal(err)
	}
	original := make([]*schema.Message, projectEinoAssistantCompactionTestMessages+1)
	for index := range original {
		original[index] = schema.UserMessage("user request")
	}
	_, compacted, err := middleware.BeforeModelRewriteState(ctx, &adk.ChatModelAgentState{Messages: original}, &adk.ModelContext{})
	if err != nil {
		t.Fatal(err)
	}
	last := compacted.Messages[len(compacted.Messages)-1]
	if last.Role != schema.User || last.Content != projectEinoAssistantCompactionSummaryContent("") {
		t.Fatalf("tool-only replacement summary = %#v, want prefix-only user checkpoint", last)
	}
	for _, message := range compacted.Messages {
		if len(message.ToolCalls) != 0 || strings.Contains(message.Content, "must not persist") {
			t.Fatalf("tool-only compaction output leaked into replacement history: %#v", message)
		}
	}

	items, err := memoryStore.ListAssistantConversationItems(ctx, scope, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Type != projectAssistantConversationCompaction {
		t.Fatalf("conversation items = %#v, want only the inert compaction checkpoint", items)
	}
	if strings.Contains(string(items[0].Payload), "must-not-dispatch") || strings.Contains(string(items[0].Payload), "must not persist") {
		t.Fatalf("compaction checkpoint persisted ignored tool output: %s", items[0].Payload)
	}

	var audit projectAssistantRunAudit
	if err := json.Unmarshal(run.Audit, &audit); err != nil {
		t.Fatal(err)
	}
	if len(audit.Compactions) != 1 || audit.Compactions[0].Status != "completed" || audit.Compactions[0].IgnoredToolCallCount != 1 {
		t.Fatalf("compaction audit = %#v, want completed checkpoint with one ignored tool call", audit.Compactions)
	}
}

func TestProjectEinoAssistantFinalizeCompactionUsesAdaptiveUserBudget(t *testing.T) {
	canonical := []*schema.Message{schema.SystemMessage(strings.Repeat("c", 4000))}
	summary := schema.AssistantMessage(strings.Repeat("s", 4000), nil)
	budget := projectEinoAssistantCompactionUserTokenBudget(canonical, summary.Content, 2000)
	if budget >= projectEinoAssistantCompactionContextTokens() || budget <= 0 {
		t.Fatalf("adaptive user budget = %d, want positive budget below the active model window", budget)
	}
	original := []*schema.Message{
		schema.SystemMessage("stale system instruction"),
		schema.UserMessage("real user request"),
		{
			Role:    schema.User,
			Content: "Workspace mutation evidence:\nsynthetic evidence",
			Extra: map[string]any{
				projectEinoAssistantSyntheticMessageKindKey: projectEinoAssistantWorkspaceMutationEvidenceKind,
			},
		},
		schema.UserMessage(projectEinoAssistantCompactionSummaryPrefix + "\nold summary"),
		schema.ToolMessage("tool evidence", "call-1"),
	}
	finalized, err := projectEinoAssistantFinalizeCompaction(canonical, original, summary, 2000)
	if err != nil {
		t.Fatal(err)
	}
	joined := ""
	for _, message := range finalized {
		joined += message.Content + "\n"
	}
	for _, excluded := range []string{"stale system instruction", "synthetic evidence", "old summary", "tool evidence"} {
		if strings.Contains(joined, excluded) {
			t.Fatalf("finalized history retained excluded content %q", excluded)
		}
	}
	if !strings.Contains(joined, "real user request") || !strings.Contains(joined, summary.Content) {
		t.Fatalf("finalized history lost user request or generated summary")
	}
	last := finalized[len(finalized)-1]
	if last.Role != schema.User || !strings.HasPrefix(last.Content, projectEinoAssistantCompactionSummaryPrefix+"\n") {
		t.Fatalf("final compaction message = %#v, want Codex-prefixed user summary", last)
	}
}

func TestProjectEinoAssistantRecentUserMessagesKeepsNewestFirstAndMarksTruncation(t *testing.T) {
	newest := strings.Repeat("n", 100)
	retained := projectEinoAssistantRecentUserMessages([]*schema.Message{
		schema.UserMessage("older instruction"),
		schema.UserMessage(newest),
	}, 20)
	if len(retained) != 1 {
		t.Fatalf("retained messages = %#v, want only newest instruction", retained)
	}
	if !strings.HasSuffix(retained[0].Content, "[... user message truncated during compaction ...]") {
		t.Fatalf("truncated newest message = %q, want explicit marker", retained[0].Content)
	}
}

func TestProjectEinoAssistantFinalizeCompactionRejectsInvalidSummary(t *testing.T) {
	tests := []*schema.Message{
		nil,
		schema.UserMessage("summary"),
	}
	for _, summary := range tests {
		if _, err := projectEinoAssistantFinalizeCompaction(nil, nil, summary, 0); err == nil {
			t.Fatalf("summary %#v unexpectedly accepted", summary)
		}
	}
}

func TestProjectEinoAssistantFinalizeCompactionIgnoresModelToolCalls(t *testing.T) {
	tests := []struct {
		name        string
		summary     *schema.Message
		wantSummary string
	}{
		{
			name:        "text and tool call retains only text",
			summary:     schema.AssistantMessage("durable handoff", []schema.ToolCall{{ID: "call-1"}}),
			wantSummary: "durable handoff",
		},
		{
			name:        "tool call only produces prefix-only checkpoint",
			summary:     schema.AssistantMessage("", []schema.ToolCall{{ID: "call-1"}}),
			wantSummary: "",
		},
		{
			name:        "empty assistant produces prefix-only checkpoint",
			summary:     schema.AssistantMessage("", nil),
			wantSummary: "",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			finalized, err := projectEinoAssistantFinalizeCompaction(
				[]*schema.Message{schema.SystemMessage("canonical")},
				[]*schema.Message{schema.UserMessage("latest user request")},
				test.summary,
				0,
			)
			if err != nil {
				t.Fatal(err)
			}
			for _, message := range finalized {
				if len(message.ToolCalls) != 0 {
					t.Fatalf("replacement history retained compaction tool calls: %#v", message.ToolCalls)
				}
			}
			last := finalized[len(finalized)-1]
			if got, want := last.Content, projectEinoAssistantCompactionSummaryContent(test.wantSummary); got != want {
				t.Fatalf("summary checkpoint = %q, want %q", got, want)
			}
		})
	}
}

func TestProjectEinoAssistantCompactionRetryMatchesTransportSemantics(t *testing.T) {
	var statuses []string
	req := projectAssistantRunRequest{
		LLM: projectLLMSettings{MaxRetries: 3, MaxRetriesConfigured: true, RetryBackoff: 10 * time.Millisecond},
		StreamCallbacks: projectAssistantStreamCallbacks{OnStatus: func(status string) {
			statuses = append(statuses, status)
		}},
	}
	retry := projectEinoAssistantCompactionRetryConfig(req)
	if !retry.ShouldRetry(context.Background(), nil, io.ErrUnexpectedEOF) {
		t.Fatal("pre-output transport failure was not retried")
	}
	if retry.ShouldRetry(context.Background(), schema.AssistantMessage("partial", nil), io.ErrUnexpectedEOF) {
		t.Fatal("partial assistant output was retried")
	}
	if retry.ShouldRetry(context.Background(), nil, errors.New("semantic failure")) {
		t.Fatal("non-transport failure was retried")
	}
	if got := retry.BackoffFunc(context.Background(), 2, nil, io.ErrUnexpectedEOF); got != 20*time.Millisecond {
		t.Fatalf("second compaction retry backoff = %s, want 20ms", got)
	}
	if len(statuses) != 1 || statuses[0] != "Model connection was interrupted; reconnecting 2/3" {
		t.Fatalf("retry statuses = %#v", statuses)
	}
}

func TestProjectEinoAssistantCompactionModelIsolatesAgentCallbacks(t *testing.T) {
	callbackCount := 0
	handler := callbacks.NewHandlerBuilder().OnStartFn(func(ctx context.Context, _ *callbacks.RunInfo, _ callbacks.CallbackInput) context.Context {
		callbackCount++
		return ctx
	}).Build()
	ctx := callbacks.InitCallbacks(context.Background(), &callbacks.RunInfo{
		Name:      "main-agent",
		Type:      "test",
		Component: components.ComponentOfChatModel,
	}, handler)
	base := &projectEinoAssistantCompactionTestModel{}
	isolated := &projectEinoAssistantCompactionIsolatedModel{BaseChatModel: base, forbidToolChoice: true}
	forced := schema.ToolChoiceForced
	contaminatedTool := &schema.ToolInfo{Name: "must_not_reach_compaction"}
	if _, err := isolated.Generate(
		ctx,
		[]*schema.Message{schema.UserMessage("compact")},
		einomodel.WithMaxTokens(1234),
		einomodel.WithTools([]*schema.ToolInfo{contaminatedTool}),
		einomodel.WithDeferredTools([]*schema.ToolInfo{contaminatedTool}),
		einomodel.WithToolSearchTool(contaminatedTool),
		einomodel.WithToolChoice(forced),
	); err != nil {
		t.Fatal(err)
	}
	if callbackCount != 0 {
		t.Fatalf("main-agent callback count = %d, want zero for internal compaction sampling", callbackCount)
	}
	if base.maxTokens != 1234 {
		t.Fatalf("compaction max tokens = %d, want preserved 1234", base.maxTokens)
	}
	if base.tools == nil || len(base.tools) != 0 {
		t.Fatalf("compaction tools = %#v, want explicit empty tool set", base.tools)
	}
	if base.deferredTools == nil || len(base.deferredTools) != 0 {
		t.Fatalf("compaction deferred tools = %#v, want explicit empty tool set", base.deferredTools)
	}
	if base.toolSearchTool != nil {
		t.Fatalf("compaction tool search = %#v, want nil", base.toolSearchTool)
	}
	if base.toolChoice == nil || *base.toolChoice != schema.ToolChoiceForbidden {
		t.Fatalf("compaction tool choice = %#v, want forbidden", base.toolChoice)
	}
}

func TestProjectEinoAssistantCompactionToolChoiceCompatibility(t *testing.T) {
	tests := []struct {
		name     string
		settings projectLLMSettings
		want     bool
	}{
		{
			name: "OpenCode Zen DeepSeek V4 supports explicit none",
			settings: projectLLMSettings{
				BaseURL: "https://opencode.ai/zen/v1",
				Model:   "deepseek-v4-flash",
			},
			want: true,
		},
		{
			name: "direct DeepSeek V4 omits incompatible field",
			settings: projectLLMSettings{
				BaseURL: "https://api.deepseek.com/v1",
				Model:   "deepseek/deepseek-v4-flash",
			},
			want: false,
		},
		{
			name: "direct earlier DeepSeek keeps explicit none",
			settings: projectLLMSettings{
				BaseURL: "https://api.deepseek.com/v1",
				Model:   "deepseek-chat",
			},
			want: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := projectEinoAssistantCompactionSupportsForbiddenToolChoice(tt.settings); got != tt.want {
				t.Fatalf("supports forbidden tool choice = %t, want %t", got, tt.want)
			}
		})
	}
}

func TestProjectEinoAssistantCompactionOpenAIPayloadHasNoTools(t *testing.T) {
	for _, tt := range []struct {
		name                  string
		forbidToolChoice      bool
		contaminateToolChoice bool
		wantToolChoice        bool
	}{
		{
			name:                  "explicitly forbidden",
			forbidToolChoice:      true,
			contaminateToolChoice: true,
			wantToolChoice:        true,
		},
		{name: "DeepSeek V4 thinking compatibility", forbidToolChoice: false, wantToolChoice: false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var requestBody map[string]any
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
					t.Errorf("decode request: %v", err)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"id":"chatcmpl-test","object":"chat.completion","created":1,"model":"deepseek-v4-flash","choices":[{"index":0,"message":{"role":"assistant","content":"summary"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
			}))
			defer server.Close()

			base, err := newProjectEinoOpenAIChatModel(context.Background(), projectLLMSettings{
				BaseURL: server.URL,
				Model:   "deepseek-v4-flash",
				APIKey:  "test-key",
			})
			if err != nil {
				t.Fatal(err)
			}
			model := &projectEinoAssistantCompactionIsolatedModel{
				BaseChatModel:    base,
				forbidToolChoice: tt.forbidToolChoice,
			}
			contaminatedTool := &schema.ToolInfo{Name: "must_not_reach_compaction"}
			opts := []einomodel.Option{
				einomodel.WithTools([]*schema.ToolInfo{contaminatedTool}),
				einomodel.WithDeferredTools([]*schema.ToolInfo{contaminatedTool}),
				einomodel.WithToolSearchTool(contaminatedTool),
			}
			if tt.contaminateToolChoice {
				opts = append(opts, einomodel.WithToolChoice(schema.ToolChoiceForced))
			}
			if _, err := model.Generate(context.Background(), []*schema.Message{schema.UserMessage("compact")}, opts...); err != nil {
				t.Fatal(err)
			}
			if _, ok := requestBody["tools"]; ok {
				t.Fatalf("compaction payload tools = %#v, want omitted", requestBody["tools"])
			}
			got, ok := requestBody["tool_choice"]
			if tt.wantToolChoice {
				if !ok || got != "none" {
					t.Fatalf("compaction payload tool_choice = %#v, want none", got)
				}
			} else if ok {
				t.Fatalf("compaction payload tool_choice = %#v, want omitted", got)
			}
		})
	}
}

func TestProjectEinoAssistantCompactionFailuresWriteNoCheckpoint(t *testing.T) {
	t.Setenv(projectEinoAssistantModelContextTokensEnv, "128")
	tests := []struct {
		name     string
		response *schema.Message
		err      error
	}{
		{name: "wrong role", response: schema.UserMessage("summary")},
		{name: "provider failure", err: errors.New("provider failed")},
		{name: "cancellation", err: context.Canceled},
		{name: "partial transport failure", response: schema.AssistantMessage("partial", nil), err: io.ErrUnexpectedEOF},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			memoryStore := store.NewMemoryStore()
			scope := store.Scope{OrgUUID: "org", WorkspaceUUID: "workspace", ProjectName: "demo", ProjectUID: "uid"}
			req := projectAssistantRunRequest{
				Project: &aiv1alpha1.Project{
					ObjectMeta: metav1.ObjectMeta{Name: "demo", UID: "uid"},
					Spec:       aiv1alpha1.ProjectSpec{DisplayName: "Demo"},
				},
				MessageScope:      scope,
				AssistantRun:      &store.AssistantRun{ID: "run-1"},
				CollaborationMode: projectAssistantCollaborationModeDefault,
				LLM:               projectLLMSettings{MaxRetriesConfigured: true},
			}
			model := &projectEinoAssistantCompactionTestModel{response: test.response, err: test.err}
			middleware, err := projectEinoAssistantCompactionMiddleware(ctx, model, &Server{store: memoryStore}, req, newProjectEinoAssistantRunState())
			if err != nil {
				t.Fatal(err)
			}
			original := make([]*schema.Message, projectEinoAssistantCompactionTestMessages+1)
			for index := range original {
				original[index] = schema.UserMessage("user request")
			}
			_, _, err = middleware.BeforeModelRewriteState(ctx, &adk.ChatModelAgentState{Messages: original}, &adk.ModelContext{})
			if err == nil {
				t.Fatal("compaction unexpectedly succeeded")
			}
			projection, loadErr := loadProjectAssistantConversationProjection(ctx, memoryStore, scope)
			if loadErr != nil {
				t.Fatal(loadErr)
			}
			if projection.compactionCheckpoint != nil {
				t.Fatalf("failed compaction persisted checkpoint %#v", projection.compactionCheckpoint)
			}
		})
	}
}

func TestProjectEinoAssistantFinalProseOverThresholdEndsWithoutCompaction(t *testing.T) {
	final := strings.Repeat("final response ", 9000)
	model := &repositoryFlowEinoChatModel{Steps: []repositoryFlowEinoModelStep{{
		Message: schema.AssistantMessage(final, nil),
	}}}
	reply, requests, err := runProjectAssistantStreamWithModel(t, model, "")
	if err != nil {
		t.Fatal(err)
	}
	if reply != strings.TrimSpace(final) {
		t.Fatalf("final reply length = %d, want %d", len(reply), len(strings.TrimSpace(final)))
	}
	if len(requests) != 1 {
		t.Fatalf("model request count = %d, want one final-prose request with no compaction", len(requests))
	}
	for _, message := range requests[0].Messages {
		if message.Content == projectEinoAssistantCompactionPrompt {
			t.Fatal("final prose triggered a compaction continuation")
		}
	}
}

func TestProjectEinoAssistantCheckpointedInputKeepsOnlyConversationalPayload(t *testing.T) {
	replacement := []chatMessage{
		{Role: "system", Content: projectEinoAssistantV2DeepInstruction},
		{Role: "system", Content: projectEinoAssistantProjectPromptPrefix + "stale project metadata"},
		{Role: "system", Content: projectEinoAssistantSessionSnapshotPrefix + " stale snapshot"},
		{Role: "system", Content: "Databricks guidance: stale tool contract"},
		{Role: "user", Content: "latest genuine user message"},
		{Role: "assistant", ToolCalls: []chatToolCall{{ID: "call-1", Function: chatToolCallFunction{Name: "read_file", Arguments: `{"path":"src/App.tsx"}`}}}},
		{Role: "tool", Name: "read_file", ToolCallID: "call-1", Content: "file contents"},
		{Role: "user", Content: projectEinoAssistantCompactionSummaryPrefix + "\ncheckpoint summary"},
	}
	want := cloneChatMessages(replacement[4:])
	want[1].ToolCalls[0].Type = "function"
	runState := newProjectEinoAssistantRunState()
	runState.SetToolPrompt("new tool prompt that must not move the checkpoint summary")
	input, err := projectEinoAssistantInputMessages(context.Background(), projectAssistantRunRequest{
		Conversation:             replacement,
		ConversationCheckpointed: true,
	}, runState, false)
	if err != nil {
		t.Fatal(err)
	}
	if got := projectEinoMessagesToChat(input); !reflect.DeepEqual(got, want) {
		t.Fatalf("conversation payload = %#v, want %#v", got, want)
	}
}

func TestProjectEinoAssistantMutationEvidencePreservesToolContinuationOrdering(t *testing.T) {
	evidence := schema.UserMessage(projectEinoAssistantWorkspaceMutationEvidencePrefix + " updated src/App.tsx")
	evidence.Extra = map[string]any{
		projectEinoAssistantSyntheticMessageKindKey: projectEinoAssistantWorkspaceMutationEvidenceKind,
		"reasoning-content":                         "hidden provider reasoning must not persist",
	}
	chatEvidence := projectEinoMessagesToChat([]*schema.Message{evidence})
	if len(chatEvidence) != 1 || len(chatEvidence[0].Extra) != 1 || chatEvidence[0].Extra["reasoning-content"] != nil {
		t.Fatalf("durable message metadata retained provider-owned extra data: %#v", chatEvidence)
	}
	messages := []*schema.Message{
		schema.AssistantMessage("", []schema.ToolCall{{ID: "mutation-1"}}),
		schema.ToolMessage(`{"operation":"edit_file"}`, "mutation-1"),
		evidence,
	}
	if !projectEinoAssistantLastNonSystemMessageIsTool(messages) {
		t.Fatal("synthetic mutation evidence hid the completed tool continuation")
	}
	genuine := schema.UserMessage(projectEinoAssistantWorkspaceMutationEvidencePrefix + " this is my genuine instruction")
	retained := projectEinoAssistantRecentUserMessages([]*schema.Message{genuine}, 100)
	if len(retained) != 1 || retained[0].Content != genuine.Content {
		t.Fatalf("genuine user message with evidence-like prefix was not retained: %#v", retained)
	}

	roundTripped, err := projectChatMessagesToEino(chatEvidence)
	if err != nil {
		t.Fatal(err)
	}
	if len(roundTripped) != 1 || !projectEinoAssistantSyntheticWorkspaceMutationEvidence(roundTripped[0]) {
		t.Fatalf("synthetic mutation provenance was lost across durable chat conversion: %#v", roundTripped)
	}
	if retained := projectEinoAssistantRecentUserMessages(roundTripped, 100); len(retained) != 0 {
		t.Fatalf("round-tripped synthetic evidence was retained as a genuine user message: %#v", retained)
	}
}

func TestProjectEinoAssistantCompactionRuntimeResumesWindowChain(t *testing.T) {
	checkpoint := &projectAssistantConversationCompactionCheckpoint{
		WindowNumber:  4,
		FirstWindowID: "window-first",
		WindowID:      "window-current",
	}
	runtime := newProjectEinoAssistantCompactionRuntime(nil, checkpoint)
	if err := runtime.begin(context.Background(), 100); err != nil {
		t.Fatal(err)
	}
	attempt, ok := runtime.activeAttempt()
	if !ok {
		t.Fatal("active compaction attempt is missing")
	}
	if attempt.windowNumber != 5 || attempt.firstWindowID != "window-first" || attempt.previousWindowID != "window-current" {
		t.Fatalf("resumed window chain = %#v", attempt)
	}
}
