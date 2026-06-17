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
	"strings"

	"github.com/cloudwego/eino/adk"
	einomodel "github.com/cloudwego/eino/components/model"
	einotool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

type projectEinoAssistantEngine struct {
	server    *Server
	newModel  projectEinoAssistantModelFactory
	newTools  projectEinoAssistantToolsFactory
	newRunner projectEinoAssistantRunnerFactory
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

type projectEinoAssistantRunner interface {
	Run(context.Context, []adk.Message, ...adk.AgentRunOption) *adk.AsyncIterator[*adk.AgentEvent]
	ResumeWithParams(context.Context, string, *adk.ResumeParams, ...adk.AgentRunOption) (*adk.AsyncIterator[*adk.AgentEvent], error)
}

type projectEinoAssistantRunnerFactory func(context.Context, adk.Agent, adk.CheckPointStore) projectEinoAssistantRunner

// NewEinoAssistantEngine returns the Eino-backed assistant engine. The App
// Studio assistant uses Eino's ChatModelAgent as the only chat/tool execution
// loop; App Studio adapters stay at model, tool, storage, and event boundaries.
func NewEinoAssistantEngine(server *Server) projectAssistantEngine {
	return projectEinoAssistantEngine{
		server:    server,
		newModel:  newProjectEinoAssistantModelFactory(server),
		newTools:  newProjectEinoAssistantToolsFactory(server),
		newRunner: newProjectEinoAssistantRunner,
	}
}

func newProjectEinoAssistantRunner(ctx context.Context, agent adk.Agent, checkpointStore adk.CheckPointStore) projectEinoAssistantRunner {
	return adk.NewRunner(ctx, adk.RunnerConfig{
		Agent:           agent,
		EnableStreaming: true,
		CheckPointStore: checkpointStore,
	})
}

func (e projectEinoAssistantEngine) StreamProjectAssistant(
	ctx context.Context,
	req projectAssistantRunRequest,
	sink projectAssistantEventSink,
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
	if e.newRunner == nil {
		return projectAssistantRunResult{}, errors.New("eino runner is not configured")
	}
	_ = sink

	runState := newProjectEinoAssistantRunState()
	runState.SetProjectRepositoryRef(projectEinoAssistantProjectRepositoryRef(req))

	agent, err := e.newAgent(ctx, req, runState)
	if err != nil {
		return projectAssistantRunResult{}, err
	}
	input, err := projectEinoAssistantInputMessages(req, runState)
	if err != nil {
		return projectAssistantRunResult{}, err
	}
	checkpointID := newProjectAssistantRunID()
	checkpointStore := newProjectEinoAssistantCheckpointStore()
	runner := e.newRunner(ctx, agent, checkpointStore)
	if runner == nil {
		return projectAssistantRunResult{}, errors.New("eino runner is not configured")
	}
	iter := runner.Run(ctx, input, adk.WithCheckPointID(checkpointID))
	return e.drainProjectAssistantEvents(ctx, req, runState, checkpointStore, checkpointID, iter)
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
	if e.newRunner == nil {
		return projectAssistantRunResult{}, errors.New("eino runner is not configured")
	}

	runState := newProjectEinoAssistantRunState()
	runState.SetProjectRepositoryRef(projectEinoAssistantProjectRepositoryRef(projectAssistantRunRequest{
		Project:      req.Project,
		Continuation: &state,
	}))
	agent, err := e.newAgent(ctx, req, runState)
	if err != nil {
		return projectAssistantRunResult{}, err
	}
	decision, err := parseProjectAssistantPermissionDecision(resumeReq.Decision)
	if err != nil {
		return projectAssistantRunResult{}, err
	}
	checkpointStore := newProjectEinoAssistantCheckpointStoreWithCheckpoint(state.Eino.CheckpointID, state.Eino.Checkpoint)
	runner := e.newRunner(ctx, agent, checkpointStore)
	if runner == nil {
		return projectAssistantRunResult{}, errors.New("eino runner is not configured")
	}
	iter, err := runner.ResumeWithParams(ctx, state.Eino.CheckpointID, &adk.ResumeParams{
		Targets: map[string]any{
			state.Eino.InterruptID: &projectEinoPermissionResumeData{
				Decision:        decision,
				EditedArguments: cloneProjectAssistantToolArguments(resumeReq.EditedArguments),
			},
		},
	})
	if err != nil {
		return projectAssistantRunResult{}, err
	}
	return e.drainProjectAssistantEvents(ctx, req, runState, checkpointStore, state.Eino.CheckpointID, iter)
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
	agent, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Name:        "app-studio-project-assistant",
		Description: "Runs App Studio project assistant turns.",
		Model:       chatModel,
		ToolsConfig: adk.ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{
				Tools:               tools,
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

func (e projectEinoAssistantEngine) drainProjectAssistantEvents(
	ctx context.Context,
	req projectAssistantRunRequest,
	runState *projectEinoAssistantRunState,
	checkpointStore *projectEinoAssistantCheckpointStore,
	checkpointID string,
	iter *adk.AsyncIterator[*adk.AgentEvent],
) (projectAssistantRunResult, error) {
	if iter == nil {
		return projectAssistantRunResult{}, errors.New("eino runner returned no event stream")
	}
	var result projectAssistantRunResult
	receivedOutput := false
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
				return projectAssistantRunResult{Content: runState.ToolLoopFallback()}, nil
			}
			return projectAssistantRunResult{}, event.Err
		}
		if event.Action != nil && event.Action.Interrupted != nil {
			return projectAssistantRunResult{}, e.saveProjectAssistantInterrupt(ctx, req, runState, checkpointStore, checkpointID, event.Action.Interrupted)
		}
		if event.Output == nil {
			continue
		}
		if runResult, ok := event.Output.CustomizedOutput.(projectAssistantRunResult); ok {
			result = runResult
			receivedOutput = true
			continue
		}
		if event.Output.MessageOutput == nil {
			continue
		}
		msg, err := event.Output.MessageOutput.GetMessage()
		if err != nil {
			return projectAssistantRunResult{}, err
		}
		if msg != nil && msg.Role == schema.Assistant && strings.TrimSpace(msg.Content) != "" {
			result.Content = msg.Content
			receivedOutput = true
		}
	}
	if !receivedOutput {
		return projectAssistantRunResult{}, errors.New("eino runner completed without assistant output")
	}
	return result, nil
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
	info, interruptID, err := projectEinoPermissionInterruptInfoFromEvent(interrupted)
	if err != nil {
		return err
	}
	checkpoint, ok, err := checkpointStore.Get(ctx, checkpointID)
	if err != nil {
		return err
	}
	if !ok || len(checkpoint) == 0 {
		return errors.New("eino checkpoint was not saved for permission interrupt")
	}
	_, index, toolCalls := runState.ToolCallByID(info.ToolCallID, info.ToolName, info.ArgumentsInJSON)
	state := runState.CheckpointState()
	if len(state.ToolCalls) == 0 {
		state.ToolCalls = cloneProjectAssistantToolCalls(toolCalls)
	}
	state.CurrentIndex = index
	state.Eino = &projectAssistantEinoCheckpointState{
		CheckpointID: strings.TrimSpace(checkpointID),
		Checkpoint:   checkpoint,
		InterruptID:  interruptID,
		ToolCallID:   info.ToolCallID,
		ToolName:     info.ToolName,
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
	return permissionErr
}

func projectEinoPermissionInterruptInfoFromEvent(interrupted *adk.InterruptInfo) (*projectEinoPermissionInterruptInfo, string, error) {
	if interrupted == nil {
		return nil, "", errors.New("eino interrupt is missing")
	}
	for _, interruptCtx := range interrupted.InterruptContexts {
		if interruptCtx == nil {
			continue
		}
		switch info := interruptCtx.Info.(type) {
		case *projectEinoPermissionInterruptInfo:
			if info != nil {
				return info, strings.TrimSpace(interruptCtx.ID), nil
			}
		case projectEinoPermissionInterruptInfo:
			return &info, strings.TrimSpace(interruptCtx.ID), nil
		}
	}
	return nil, "", errors.New("eino interrupt did not include App Studio permission metadata")
}

func projectEinoAssistantInputMessages(req projectAssistantRunRequest, runState *projectEinoAssistantRunState) ([]adk.Message, error) {
	var chatMessages []chatMessage
	if req.Continuation != nil && len(req.Continuation.Messages) > 0 {
		chatMessages = cloneChatMessages(req.Continuation.Messages)
	} else {
		chatMessages = projectPromptMessages(req.Project, req.Repository, req.History)
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
