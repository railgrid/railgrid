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
	"fmt"
	"io"
	"strings"

	approvaltool "github.com/cloudwego/eino-examples/adk/common/tool"
	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/middlewares/dynamictool/toolsearch"
	"github.com/cloudwego/eino/adk/middlewares/summarization"
	einomodel "github.com/cloudwego/eino/components/model"
	einotool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

const (
	projectEinoAssistantSummaryContextMessages = 128
	projectEinoAssistantSummaryContextTokens   = 24000
	projectEinoAssistantSummaryInstruction     = "Summarize this App Studio project session for the next builder turn. Preserve user requirements, accepted plans, files touched or inspected, unresolved questions, repository/runtime state, and any constraints. Keep it concise and operational."
	projectEinoAssistantNoOutputFallback       = "I couldn't produce a response for that turn. Please try again or rephrase the request, and I can continue from the current project context."

	// Bundle search is for App Studio's full product toolbox. Smaller injected
	// tool sets stay direct so focused permission/resume flows keep their shape.
	projectEinoAssistantBundleSearchMinTools = 4
)

type projectEinoAssistantEngine struct {
	server   *Server
	newModel projectEinoAssistantModelFactory
	newTools projectEinoAssistantToolsFactory
}

type projectEinoAssistantModelFactory func(
	context.Context,
	projectAssistantRunRequest,
	*projectEinoAssistantRunState,
) (einomodel.BaseChatModel, error)

type projectEinoAssistantToolsFactory func(
	context.Context,
	projectAssistantRunRequest,
	*projectEinoAssistantRunState,
) ([]einotool.BaseTool, error)

// NewEinoAssistantEngine returns the Eino-backed assistant engine. The App
// Studio assistant uses Eino's ChatModelAgent as the only chat/tool execution
// loop; App Studio adapters stay at model, tool, storage, and event boundaries.
func NewEinoAssistantEngine(server *Server) projectAssistantEngine {
	return projectEinoAssistantEngine{
		server:   server,
		newModel: newProjectEinoAssistantModelFactory(server),
		newTools: newProjectEinoAssistantToolsFactory(server),
	}
}

func (e projectEinoAssistantEngine) StreamProjectAssistant(
	ctx context.Context,
	req projectAssistantRunRequest,
) (projectAssistantRunResult, error) {
	if req.Project == nil {
		return projectAssistantRunResult{}, errors.New("project is required")
	}
	if e.newModel == nil {
		return projectAssistantRunResult{}, errors.New("eino model factory is not configured")
	}
	if e.newTools == nil {
		return projectAssistantRunResult{}, errors.New("eino tool factory is not configured")
	}

	req.TurnPolicy = normalizeProjectAssistantTurnPolicy(req.TurnPolicy, req.TurnProfile)
	req.TurnProfile = req.TurnPolicy.profile
	runState := newProjectEinoAssistantRunState()
	runState.SetTurnPolicy(req.TurnPolicy)
	runState.SetProjectRepositoryRef(projectEinoAssistantProjectRepositoryRef(req))
	// A new chat message starts a fresh run, so seed the plan-approval grant
	// that a previous turn earned. Without this the model re-requests approval
	// every turn even though the grant is meant to last until the next commit.
	if e.server != nil {
		if grant := e.server.loadProjectAssistantApprovedPlan(ctx, req.MessageScope); grant != nil {
			runState.ApprovePlan(*grant)
		}
	}

	checkpointID := newProjectAssistantRunID()
	checkpointStore := newProjectEinoAssistantCheckpointStore()
	turn := newProjectAssistantTurnItem(projectAssistantTurnMessage, req.Identity, req.Project.Name)
	return e.runProjectAssistantTurnLoop(ctx, req, runState, checkpointStore, checkpointID, []projectAssistantTurnItem{turn})
}

func (e projectEinoAssistantEngine) ResumeProjectAssistant(
	ctx context.Context,
	req projectAssistantRunRequest,
	resumeReq projectAssistantResumeRequest,
	state projectAssistantCheckpointState,
) (projectAssistantRunResult, error) {
	if req.Project == nil {
		return projectAssistantRunResult{}, errors.New("project is required")
	}
	if state.Eino == nil || len(state.Eino.Checkpoint) == 0 || strings.TrimSpace(state.Eino.CheckpointID) == "" || strings.TrimSpace(state.Eino.InterruptID) == "" {
		return projectAssistantRunResult{}, errors.New("eino checkpoint is required")
	}
	if e.newModel == nil {
		return projectAssistantRunResult{}, errors.New("eino model factory is not configured")
	}
	if e.newTools == nil {
		return projectAssistantRunResult{}, errors.New("eino tool factory is not configured")
	}

	runState := newProjectEinoAssistantRunState()
	runState.RestoreCheckpointState(state)
	runState.SetProjectRepositoryRef(projectEinoAssistantProjectRepositoryRef(projectAssistantRunRequest{
		Project:      req.Project,
		Continuation: &state,
	}))
	resumeRunReq := req
	resumeRunReq.Continuation = &state
	resumeRunReq.TurnPolicy = runState.TurnPolicy()
	resumeRunReq.TurnProfile = resumeRunReq.TurnPolicy.profile
	checkpointStore := newProjectEinoAssistantCheckpointStoreWithCheckpoint(state.Eino.CheckpointID, state.Eino.Checkpoint)
	turn := newProjectAssistantTurnItem(projectAssistantTurnResume, req.Identity, req.Project.Name)
	turn.RequestID = strings.TrimSpace(resumeReq.RequestID)
	turn.Decision = strings.TrimSpace(resumeReq.Decision)
	turn.Answer = strings.TrimSpace(resumeReq.Answer)
	turn.EditedArguments = cloneProjectAssistantToolArguments(resumeReq.EditedArguments)
	return e.runProjectAssistantTurnLoop(ctx, resumeRunReq, runState, checkpointStore, state.Eino.CheckpointID, []projectAssistantTurnItem{turn})
}

func (e projectEinoAssistantEngine) newAgent(ctx context.Context, req projectAssistantRunRequest, runState *projectEinoAssistantRunState) (adk.Agent, error) {
	tools, err := e.newTools(ctx, req, runState)
	if err != nil {
		return nil, err
	}
	chatModel, err := e.newModel(ctx, req, runState)
	if err != nil {
		return nil, err
	}
	staticTools, dynamicTools, err := projectEinoAssistantToolSearchSets(ctx, tools)
	if err != nil {
		return nil, err
	}
	var handlers []adk.ChatModelAgentMiddleware
	summaryMiddleware, err := summarization.New(ctx, &summarization.Config{
		Model: chatModel,
		Trigger: &summarization.TriggerCondition{
			ContextMessages: projectEinoAssistantSummaryContextMessages,
			ContextTokens:   projectEinoAssistantSummaryContextTokens,
		},
		UserInstruction: projectEinoAssistantSummaryInstruction,
		Finalize:        projectEinoAssistantFinalizeSummary,
	})
	if err != nil {
		return nil, fmt.Errorf("create eino summarization middleware: %w", err)
	}
	handlers = append(handlers, summaryMiddleware)
	if len(dynamicTools) > 0 {
		searchMiddleware, err := toolsearch.New(ctx, &toolsearch.Config{
			DynamicTools: dynamicTools,
		})
		if err != nil {
			return nil, fmt.Errorf("create eino tool search middleware: %w", err)
		}
		handlers = append(handlers, searchMiddleware)
	}
	agent, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Name:        "app-studio-project-assistant",
		Description: "Runs App Studio project assistant turns.",
		Model:       chatModel,
		Handlers:    handlers,
		ToolsConfig: adk.ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{
				Tools:               staticTools,
				UnknownToolsHandler: projectEinoUnknownToolHandler(req, runState),
				ExecuteSequentially: true,
			},
		},
		MaxIterations: maxAssistantToolTurns,
	})
	if err != nil {
		return nil, fmt.Errorf("create eino assistant agent: %w", err)
	}
	return agent, nil
}

func projectEinoAssistantFinalizeSummary(ctx context.Context, originalMessages []*schema.Message, summary *schema.Message) ([]*schema.Message, error) {
	if strings.TrimSpace(projectEinoAssistantSummaryText(summary)) == "" {
		summary = schema.AssistantMessage(projectEinoAssistantFallbackSummary(originalMessages), nil)
	}
	return summarization.DefaultFinalize(ctx, originalMessages, summary)
}

func projectEinoAssistantSummaryText(msg *schema.Message) string {
	if msg == nil || msg.Role != schema.Assistant {
		return ""
	}
	var parts []string
	for _, part := range msg.AssistantGenMultiContent {
		if part.Type == schema.ChatMessagePartTypeText && strings.TrimSpace(part.Text) != "" {
			parts = append(parts, strings.TrimSpace(part.Text))
		}
	}
	if len(parts) > 0 {
		return strings.Join(parts, "\n")
	}
	return strings.TrimSpace(msg.Content)
}

func projectEinoAssistantFallbackSummary(messages []*schema.Message) string {
	const maxMessages = 12
	var b strings.Builder
	b.WriteString("Summary unavailable; preserving recent App Studio context.")
	start := len(messages) - maxMessages
	if start < 0 {
		start = 0
	}
	for _, msg := range messages[start:] {
		content := truncateProjectToolInfo(projectEinoAssistantMessageText(msg))
		if content == "" {
			continue
		}
		b.WriteString("\n- ")
		b.WriteString(projectEinoAssistantMessageRole(msg))
		b.WriteString(": ")
		b.WriteString(content)
	}
	return b.String()
}

func projectEinoAssistantMessageText(msg *schema.Message) string {
	if msg == nil {
		return ""
	}
	switch msg.Role {
	case schema.Assistant:
		return projectEinoAssistantSummaryText(msg)
	default:
		return strings.TrimSpace(msg.Content)
	}
}

func projectEinoAssistantMessageRole(msg *schema.Message) string {
	if msg == nil {
		return "message"
	}
	return strings.ToLower(string(msg.Role))
}

func projectEinoAssistantToolSearchSets(ctx context.Context, tools []einotool.BaseTool) ([]einotool.BaseTool, []einotool.BaseTool, error) {
	infos := make([]*schema.ToolInfo, 0, len(tools))
	searchCandidateCount := 0
	staticTools := make([]einotool.BaseTool, 0, len(tools))
	dynamicTools := make([]einotool.BaseTool, 0, len(tools))
	for _, tool := range tools {
		if tool == nil {
			continue
		}
		info, err := tool.Info(ctx)
		if err != nil {
			return nil, nil, err
		}
		infos = append(infos, info)
		if projectEinoAssistantToolCanUseSearch(info) {
			searchCandidateCount++
		}
	}
	useBundleSearch := searchCandidateCount >= projectEinoAssistantBundleSearchMinTools
	infoIndex := 0
	for _, tool := range tools {
		if tool == nil {
			continue
		}
		info := infos[infoIndex]
		infoIndex++
		if projectEinoAssistantToolUsesSearch(info, useBundleSearch) {
			dynamicTools = append(dynamicTools, tool)
			continue
		}
		staticTools = append(staticTools, tool)
	}
	return staticTools, dynamicTools, nil
}

func projectEinoAssistantToolCanUseSearch(info *schema.ToolInfo) bool {
	if info == nil || info.Extra == nil {
		return false
	}
	bundle, _ := info.Extra["bundle"].(string)
	return projectAssistantToolBundle(bundle) != projectAssistantToolBundleCollaboration
}

func projectEinoAssistantToolUsesSearch(info *schema.ToolInfo, useBundleSearch bool) bool {
	if info == nil || info.Extra == nil {
		return false
	}
	return useBundleSearch && projectEinoAssistantToolCanUseSearch(info)
}

type projectEinoAssistantTurnOutcome struct {
	result         projectAssistantRunResult
	receivedOutput bool
	interrupt      *adk.InterruptInfo
}

func (e projectEinoAssistantEngine) runProjectAssistantTurnLoop(
	ctx context.Context,
	req projectAssistantRunRequest,
	runState *projectEinoAssistantRunState,
	checkpointStore *projectEinoAssistantCheckpointStore,
	checkpointID string,
	items []projectAssistantTurnItem,
) (projectAssistantRunResult, error) {
	outcome := &projectEinoAssistantTurnOutcome{}
	if e.server != nil {
		projectEinoAssistantEnsureToolDiscovery(ctx, e.server, req, runState)
	}
	loop := adk.NewTurnLoop[projectAssistantTurnItem, *schema.Message](adk.TurnLoopConfig[projectAssistantTurnItem, *schema.Message]{
		GenInput: func(loopCtx context.Context, _ *adk.TurnLoop[projectAssistantTurnItem, *schema.Message], items []projectAssistantTurnItem) (*adk.GenInputResult[projectAssistantTurnItem, *schema.Message], error) {
			if len(items) == 0 {
				return nil, errors.New("eino turn loop received no work")
			}
			input, err := projectEinoAssistantInputMessages(loopCtx, req, runState)
			if err != nil {
				return nil, err
			}
			return &adk.GenInputResult[projectAssistantTurnItem, *schema.Message]{
				RunCtx: loopCtx,
				Input: &adk.TypedAgentInput[*schema.Message]{
					Messages:        input,
					EnableStreaming: true,
				},
				Consumed:  append([]projectAssistantTurnItem(nil), items[:1]...),
				Remaining: append([]projectAssistantTurnItem(nil), items[1:]...),
				RunOpts:   projectEinoAssistantRunOptions(req, runState),
			}, nil
		},
		GenResume: func(loopCtx context.Context, _ *adk.TurnLoop[projectAssistantTurnItem, *schema.Message], interruptedItems, unhandledItems, newItems []projectAssistantTurnItem) (*adk.GenResumeResult[projectAssistantTurnItem, *schema.Message], error) {
			resumeItem, remainingNewItems, ok := projectEinoAssistantResumeTurnItem(newItems)
			if !ok {
				return nil, errors.New("eino turn loop resume requires an approval decision")
			}
			if strings.TrimSpace(req.Continuation.Eino.InterruptID) == "" {
				return nil, errors.New("eino interrupt id is required for resume")
			}
			resumeData, err := projectEinoAssistantResumeData(req.Continuation.Eino.InterruptType, resumeItem)
			if err != nil {
				return nil, err
			}
			remaining := make([]projectAssistantTurnItem, 0, len(unhandledItems)+len(remainingNewItems))
			remaining = append(remaining, unhandledItems...)
			remaining = append(remaining, remainingNewItems...)
			return &adk.GenResumeResult[projectAssistantTurnItem, *schema.Message]{
				RunCtx: loopCtx,
				ResumeParams: &adk.ResumeParams{
					Targets: map[string]any{
						req.Continuation.Eino.InterruptID: resumeData,
					},
				},
				Consumed:  append([]projectAssistantTurnItem(nil), interruptedItems...),
				Remaining: remaining,
				RunOpts:   projectEinoAssistantRunOptions(req, runState),
			}, nil
		},
		PrepareAgent: func(agentCtx context.Context, _ *adk.TurnLoop[projectAssistantTurnItem, *schema.Message], _ []projectAssistantTurnItem) (adk.TypedAgent[*schema.Message], error) {
			return e.newAgent(agentCtx, req, runState)
		},
		OnAgentEvents: func(eventCtx context.Context, tc *adk.TurnContext[projectAssistantTurnItem, *schema.Message], iter *adk.AsyncIterator[*adk.TypedAgentEvent[*schema.Message]]) error {
			return e.collectProjectAssistantTurnEvents(eventCtx, tc, iter, req, runState, outcome)
		},
		Store:        checkpointStore,
		CheckpointID: checkpointID,
	})
	for _, item := range items {
		loop.Push(item)
	}
	loop.Run(ctx)
	exit := loop.Wait()
	if exit.CheckpointErr != nil {
		return projectAssistantRunResult{}, exit.CheckpointErr
	}
	if outcome.interrupt != nil {
		return projectAssistantRunResult{}, e.saveProjectAssistantInterrupt(ctx, req, runState, checkpointStore, checkpointID, outcome.interrupt)
	}
	if exit.ExitReason != nil {
		return projectAssistantRunResult{}, exit.ExitReason
	}
	if !outcome.receivedOutput {
		return projectAssistantRunResult{Content: projectEinoAssistantNoOutputFallback}, nil
	}
	return outcome.result, nil
}

func projectEinoAssistantResumeTurnItem(items []projectAssistantTurnItem) (projectAssistantTurnItem, []projectAssistantTurnItem, bool) {
	remaining := make([]projectAssistantTurnItem, 0, len(items))
	for i := len(items) - 1; i >= 0; i-- {
		if items[i].Kind == projectAssistantTurnResume {
			remaining = append(remaining, items[:i]...)
			remaining = append(remaining, items[i+1:]...)
			return items[i], remaining, true
		}
	}
	return projectAssistantTurnItem{}, items, false
}

func projectEinoAssistantResumeData(interruptType string, item projectAssistantTurnItem) (any, error) {
	switch strings.TrimSpace(interruptType) {
	case projectAssistantInterruptTypePermission:
		decision, err := parseProjectAssistantPermissionDecision(item.Decision)
		if err != nil {
			return nil, err
		}
		return &projectEinoPermissionResumeData{
			Decision:        decision,
			EditedArguments: cloneProjectAssistantToolArguments(item.EditedArguments),
		}, nil
	case projectAssistantInterruptTypeApproval:
		decision, err := parseProjectAssistantPermissionDecision(item.Decision)
		if err != nil {
			return nil, err
		}
		if decision == projectAssistantPermissionAllow {
			return &approvaltool.ApprovalResult{Approved: true}, nil
		}
		reason := "denied by user"
		return &approvaltool.ApprovalResult{Approved: false, DisapproveReason: &reason}, nil
	case projectAssistantInterruptTypeFollowUp:
		answer := strings.TrimSpace(item.Answer)
		if answer == "" {
			return nil, newValidationError("answer is required")
		}
		return &projectEinoFollowUpResumeData{Answer: answer}, nil
	default:
		return nil, fmt.Errorf("unsupported eino interrupt type %q", interruptType)
	}
}

func (e projectEinoAssistantEngine) collectProjectAssistantTurnEvents(
	eventCtx context.Context,
	tc *adk.TurnContext[projectAssistantTurnItem, *schema.Message],
	iter *adk.AsyncIterator[*adk.TypedAgentEvent[*schema.Message]],
	req projectAssistantRunRequest,
	runState *projectEinoAssistantRunState,
	outcome *projectEinoAssistantTurnOutcome,
) error {
	if iter == nil {
		return errors.New("eino turn loop returned no event stream")
	}
	for {
		event, ok := iter.Next()
		if !ok {
			break
		}
		if event == nil {
			continue
		}
		if event.Err != nil {
			if projectEinoAssistantMaxIterationsExceeded(event.Err) {
				outcome.result = projectAssistantRunResult{Content: e.projectAssistantToolLoopFinalAnswer(eventCtx, req, runState)}
				outcome.receivedOutput = true
				tc.Loop.Stop()
				return nil
			}
			return event.Err
		}
		if event.Action != nil && event.Action.Interrupted != nil {
			outcome.interrupt = event.Action.Interrupted
			return nil
		}
		if event.Output == nil {
			continue
		}
		if runResult, ok := event.Output.CustomizedOutput.(projectAssistantRunResult); ok {
			outcome.result = runResult
			outcome.receivedOutput = true
			continue
		}
		messageOutput := event.Output.MessageOutput
		if messageOutput == nil {
			continue
		}
		msg, err := projectEinoAssistantMessageOutput(eventCtx, messageOutput, req.StreamCallbacks)
		if err != nil {
			return err
		}
		role := messageOutput.Role
		if role == "" && msg != nil {
			role = msg.Role
		}
		if msg != nil && role == schema.Assistant {
			content := projectEinoAssistantSummaryText(msg)
			if strings.TrimSpace(content) == "" {
				continue
			}
			outcome.result.Content = content
			outcome.receivedOutput = true
		}
	}
	if outcome.interrupt == nil {
		tc.Loop.Stop()
	}
	return nil
}

func projectEinoAssistantMessageOutput(
	ctx context.Context,
	output *adk.TypedMessageVariant[*schema.Message],
	streamCallbacks projectAssistantStreamCallbacks,
) (*schema.Message, error) {
	if output == nil {
		return nil, nil
	}
	if !output.IsStreaming {
		return output.Message, nil
	}
	if output.MessageStream == nil {
		return nil, errors.New("eino assistant stream event missing message stream")
	}
	defer output.MessageStream.Close()

	var chunks []*schema.Message
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		msg, err := output.MessageStream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		if msg == nil {
			continue
		}
		chunks = append(chunks, msg)
		if output.Role == schema.Assistant && streamCallbacks.OnChunk != nil && msg.Content != "" {
			streamCallbacks.OnChunk(msg.Content)
		}
	}
	msg, err := schema.ConcatMessages(chunks)
	if err != nil {
		return nil, err
	}
	return msg, nil
}

func (e projectEinoAssistantEngine) projectAssistantToolLoopFinalAnswer(
	ctx context.Context,
	req projectAssistantRunRequest,
	runState *projectEinoAssistantRunState,
) string {
	fallback := runState.ToolLoopFallback()
	if e.newModel == nil {
		return fallback
	}
	input, err := projectChatMessagesToEino(runState.ToolLoopFinalAnswerMessages())
	if err != nil {
		return fallback
	}
	chatModel, err := e.newModel(ctx, req, runState)
	if err != nil {
		return fallback
	}
	msg, err := chatModel.Generate(ctx, input, einomodel.WithToolChoice(schema.ToolChoiceForbidden))
	if err != nil || msg == nil {
		return fallback
	}
	if strings.TrimSpace(msg.Content) == "" || len(msg.ToolCalls) > 0 {
		return fallback
	}
	return msg.Content
}

func (e projectEinoAssistantEngine) saveProjectAssistantInterrupt(
	ctx context.Context,
	req projectAssistantRunRequest,
	runState *projectEinoAssistantRunState,
	checkpointStore *projectEinoAssistantCheckpointStore,
	checkpointID string,
	interrupted *adk.InterruptInfo,
) error {
	if e.server == nil {
		return errors.New("server is not configured for permission checkpoints")
	}
	checkpoint, ok, err := checkpointStore.Get(ctx, checkpointID)
	if err != nil {
		return err
	}
	if !ok || len(checkpoint) == 0 {
		return errors.New("eino checkpoint was not saved for permission interrupt")
	}
	if info, interruptID, ok := projectEinoPermissionInterruptInfoFromEvent(interrupted); ok {
		return e.saveProjectAssistantPermissionInterrupt(ctx, req, runState, checkpoint, checkpointID, interruptID, projectAssistantInterruptTypePermission, info)
	}
	if info, interruptID, ok := projectEinoApprovalInterruptInfoFromEvent(interrupted); ok {
		return e.saveProjectAssistantPermissionInterrupt(ctx, req, runState, checkpoint, checkpointID, interruptID, projectAssistantInterruptTypeApproval, info)
	}
	if info, interruptID, ok := projectEinoFollowUpInterruptInfoFromEvent(interrupted); ok {
		return e.saveProjectAssistantFollowUpInterrupt(ctx, req, runState, checkpoint, checkpointID, interruptID, info)
	}
	return errors.New("eino interrupt did not include App Studio metadata")
}

func (e projectEinoAssistantEngine) saveProjectAssistantPermissionInterrupt(
	ctx context.Context,
	req projectAssistantRunRequest,
	runState *projectEinoAssistantRunState,
	checkpoint []byte,
	checkpointID string,
	interruptID string,
	interruptType string,
	info *projectEinoPermissionInterruptInfo,
) error {
	_, index, toolCalls := runState.ToolCallByID(info.ToolCallID, info.ToolName, info.ArgumentsInJSON)
	state := runState.CheckpointState()
	if len(state.ToolCalls) == 0 {
		state.ToolCalls = cloneProjectAssistantToolCalls(toolCalls)
	}
	state.CurrentIndex = index
	state.Eino = &projectAssistantEinoCheckpointState{
		CheckpointID:  strings.TrimSpace(checkpointID),
		Checkpoint:    checkpoint,
		InterruptID:   interruptID,
		InterruptType: interruptType,
		ToolCallID:    info.ToolCallID,
		ToolName:      info.ToolName,
	}
	permissionErr, permission, checkpointEvent, err := e.server.saveProjectAssistantEinoPermissionCheckpoint(ctx, req, state, info)
	if err != nil {
		return err
	}
	if req.StreamCallbacks.OnAssistantEvent != nil {
		req.StreamCallbacks.OnAssistantEvent(projectAssistantEvent{
			Type:       projectAssistantEventPermissionNeeded,
			Permission: &permission,
		})
		req.StreamCallbacks.OnAssistantEvent(projectAssistantEvent{
			Type:       projectAssistantEventCheckpointSaved,
			Checkpoint: &checkpointEvent,
		})
	}
	if info.Risk == projectAssistantToolRiskPlan {
		emitProjectAssistantBuilderEvent(req.StreamCallbacks, projectAssistantBuilderEventView(projectBuilderEventPlanReady))
	}
	return permissionErr
}

func (e projectEinoAssistantEngine) saveProjectAssistantFollowUpInterrupt(
	ctx context.Context,
	req projectAssistantRunRequest,
	runState *projectEinoAssistantRunState,
	checkpoint []byte,
	checkpointID string,
	interruptID string,
	info *projectEinoFollowUpInterruptInfo,
) error {
	_, index, toolCalls := runState.ToolCallByID(info.ToolCallID, projectToolAskFollowUp, projectEinoToolArgumentsString(map[string]any{"questions": info.Questions}))
	state := runState.CheckpointState()
	if len(state.ToolCalls) == 0 {
		state.ToolCalls = cloneProjectAssistantToolCalls(toolCalls)
	}
	state.CurrentIndex = index
	state.Eino = &projectAssistantEinoCheckpointState{
		CheckpointID:  strings.TrimSpace(checkpointID),
		Checkpoint:    checkpoint,
		InterruptID:   interruptID,
		InterruptType: projectAssistantInterruptTypeFollowUp,
		ToolCallID:    info.ToolCallID,
		ToolName:      projectToolAskFollowUp,
	}
	inputErr, followUp, checkpointEvent, err := e.server.saveProjectAssistantEinoFollowUpCheckpoint(ctx, req, state, info)
	if err != nil {
		return err
	}
	if req.StreamCallbacks.OnAssistantEvent != nil {
		req.StreamCallbacks.OnAssistantEvent(projectAssistantEvent{
			Type:     projectAssistantEventInputNeeded,
			FollowUp: &followUp,
		})
		req.StreamCallbacks.OnAssistantEvent(projectAssistantEvent{
			Type:       projectAssistantEventCheckpointSaved,
			Checkpoint: &checkpointEvent,
		})
	}
	return inputErr
}

func projectEinoPermissionInterruptInfoFromEvent(interrupted *adk.InterruptInfo) (*projectEinoPermissionInterruptInfo, string, bool) {
	if interrupted == nil {
		return nil, "", false
	}
	for _, interruptCtx := range interrupted.InterruptContexts {
		if interruptCtx == nil {
			continue
		}
		switch info := interruptCtx.Info.(type) {
		case *projectEinoPermissionInterruptInfo:
			if info != nil {
				return info, strings.TrimSpace(interruptCtx.ID), true
			}
		case projectEinoPermissionInterruptInfo:
			return &info, strings.TrimSpace(interruptCtx.ID), true
		}
	}
	return nil, "", false
}

func projectEinoApprovalInterruptInfoFromEvent(interrupted *adk.InterruptInfo) (*projectEinoPermissionInterruptInfo, string, bool) {
	if interrupted == nil {
		return nil, "", false
	}
	for _, interruptCtx := range interrupted.InterruptContexts {
		if interruptCtx == nil {
			continue
		}
		switch info := interruptCtx.Info.(type) {
		case *approvaltool.ApprovalInfo:
			if info != nil {
				return projectEinoPermissionInterruptInfoForApproval(info), strings.TrimSpace(interruptCtx.ID), true
			}
		case approvaltool.ApprovalInfo:
			return projectEinoPermissionInterruptInfoForApproval(&info), strings.TrimSpace(interruptCtx.ID), true
		}
	}
	return nil, "", false
}

func projectEinoPermissionInterruptInfoForApproval(info *approvaltool.ApprovalInfo) *projectEinoPermissionInterruptInfo {
	if info == nil {
		return nil
	}
	spec, ok := projectAssistantWorkflowToolSpec(info.ToolName)
	if !ok {
		spec = projectAssistantToolSpec{Name: strings.TrimSpace(info.ToolName)}
	}
	return &projectEinoPermissionInterruptInfo{
		ToolName:        spec.Name,
		ArgumentsInJSON: strings.TrimSpace(info.ArgumentsInJSON),
		Reason:          projectAssistantPermissionReason(spec),
		Risk:            spec.Risk,
	}
}

func projectEinoFollowUpInterruptInfoFromEvent(interrupted *adk.InterruptInfo) (*projectEinoFollowUpInterruptInfo, string, bool) {
	if interrupted == nil {
		return nil, "", false
	}
	for _, interruptCtx := range interrupted.InterruptContexts {
		if interruptCtx == nil {
			continue
		}
		switch info := interruptCtx.Info.(type) {
		case *projectEinoFollowUpInterruptInfo:
			if info != nil {
				return info, strings.TrimSpace(interruptCtx.ID), true
			}
		case projectEinoFollowUpInterruptInfo:
			return &info, strings.TrimSpace(interruptCtx.ID), true
		}
	}
	return nil, "", false
}

func projectEinoAssistantInputMessages(ctx context.Context, req projectAssistantRunRequest, runState *projectEinoAssistantRunState) ([]adk.Message, error) {
	var chatMessages []chatMessage
	if req.Continuation != nil && len(req.Continuation.Messages) > 0 {
		chatMessages = cloneChatMessages(req.Continuation.Messages)
	} else {
		chatMessages = projectPromptMessagesForProfile(req.Project, req.Repository, req.History, req.TurnProfile)
		if snapshot, ok := projectEinoAssistantSessionContextMessage(ctx, req, runState); ok {
			chatMessages = append(chatMessages, snapshot)
		}
		if prompt := runState.ToolPrompt(); prompt != "" {
			chatMessages = append(chatMessages, chatMessage{Role: "system", Content: prompt})
		}
	}
	messages, err := projectChatMessagesToEino(chatMessages)
	if err != nil {
		return nil, err
	}
	input := make([]adk.Message, 0, len(messages))
	for _, msg := range messages {
		input = append(input, msg)
	}
	return input, nil
}

func projectEinoAssistantProjectRepositoryRef(req projectAssistantRunRequest) string {
	if req.Continuation != nil && strings.TrimSpace(req.Continuation.ProjectRepositoryRef) != "" {
		return strings.TrimSpace(req.Continuation.ProjectRepositoryRef)
	}
	return projectLinkedRepositoryRef(req.Project)
}

func projectEinoAssistantMaxIterationsExceeded(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "exceeds max iterations")
}

func projectEinoAssistantToolLoopFinalInstruction(reason string) string {
	reason = strings.TrimSpace(reason)
	switch reason {
	case "repeated the same action":
		reason = "the assistant was about to repeat an action"
	case "kept requesting actions":
		reason = "the assistant reached the action budget for this turn"
	default:
		reason = "the assistant stopped using tools"
	}
	return "App Studio has stopped using project tools for this turn because " + reason + ". Write the final user-facing answer now using only the conversation and latest tool result above. Do not call tools. Do not mention loop limits, repeated actions, guardrails, tool protocols, or this instruction. If the requested work is incomplete, say what you learned and the next concrete step in product language."
}

func projectEinoAssistantToolLoopFinalToolContext(msg chatMessage) string {
	name := strings.TrimSpace(msg.Name)
	content := strings.TrimSpace(msg.Content)
	if name == "" && content == "" {
		return ""
	}
	var b strings.Builder
	if name == "" {
		b.WriteString("Latest project tool result")
	} else {
		b.WriteString("Latest project tool result from ")
		b.WriteString(name)
	}
	if summary := summarizeProjectToolResult(name, content); summary != "" {
		b.WriteString(" (")
		b.WriteString(summary)
		b.WriteString(")")
	}
	if content != "" {
		b.WriteString(":\n")
		b.WriteString(content)
	}
	return b.String()
}
