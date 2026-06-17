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

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
)

type projectEinoAssistantEngine struct {
	body      projectEinoAssistantBody
	newRunner projectEinoAssistantRunnerFactory
}

type projectEinoAssistantBody func(
	context.Context,
	projectAssistantRunRequest,
	projectAssistantEventSink,
) (projectAssistantRunResult, error)

type projectEinoAssistantRunner interface {
	Run(context.Context, []adk.Message, ...adk.AgentRunOption) *adk.AsyncIterator[*adk.AgentEvent]
}

type projectEinoAssistantRunnerFactory func(context.Context, adk.Agent) projectEinoAssistantRunner

// NewEinoAssistantEngine returns the Eino-backed assistant engine. The App
// Studio chat and tool loop runs as the body of an Eino ADK agent so execution
// goes through Eino's runner pipeline.
func NewEinoAssistantEngine(server *Server) projectAssistantEngine {
	return projectEinoAssistantEngine{
		body:      newProjectEinoAssistantBody(server),
		newRunner: newProjectEinoAssistantRunner,
	}
}

func newProjectEinoAssistantBody(server *Server) projectEinoAssistantBody {
	return func(ctx context.Context, req projectAssistantRunRequest, sink projectAssistantEventSink) (projectAssistantRunResult, error) {
		_ = sink
		if server == nil {
			return projectAssistantRunResult{}, errors.New("server is not configured")
		}
		if err := ctx.Err(); err != nil {
			return projectAssistantRunResult{}, err
		}
		var (
			reply string
			err   error
		)
		if req.Continuation != nil {
			reply, err = server.runProjectAssistantChatLoopFromCheckpoint(ctx, req, *req.Continuation)
		} else {
			reply, err = server.runProjectAssistantChatLoop(ctx, req)
		}
		if err != nil {
			return projectAssistantRunResult{}, err
		}
		return projectAssistantRunResult{Content: reply}, nil
	}
}

func newProjectEinoAssistantRunner(ctx context.Context, agent adk.Agent) projectEinoAssistantRunner {
	return adk.NewRunner(ctx, adk.RunnerConfig{
		Agent:           agent,
		EnableStreaming: true,
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
	if e.body == nil {
		return projectAssistantRunResult{}, errors.New("assistant body is not configured")
	}
	if e.newRunner == nil {
		return projectAssistantRunResult{}, errors.New("eino runner is not configured")
	}
	agent := projectEinoAssistantAgent{
		body: e.body,
		req:  req,
		sink: sink,
	}
	runner := e.newRunner(ctx, agent)
	if runner == nil {
		return projectAssistantRunResult{}, errors.New("eino runner is not configured")
	}
	iter := runner.Run(ctx, []adk.Message{schema.UserMessage(projectEinoAssistantPrompt(req))})
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
			return projectAssistantRunResult{}, event.Err
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
		if msg != nil {
			result.Content = msg.Content
			receivedOutput = true
		}
	}
	if !receivedOutput {
		return projectAssistantRunResult{}, errors.New("eino runner completed without assistant output")
	}
	return result, nil
}

type projectEinoAssistantAgent struct {
	body projectEinoAssistantBody
	req  projectAssistantRunRequest
	sink projectAssistantEventSink
}

func (a projectEinoAssistantAgent) Name(context.Context) string {
	return "app-studio-project-assistant"
}

func (a projectEinoAssistantAgent) Description(context.Context) string {
	return "Runs App Studio project assistant turns."
}

func (a projectEinoAssistantAgent) Run(
	ctx context.Context,
	input *adk.AgentInput,
	options ...adk.AgentRunOption,
) *adk.AsyncIterator[*adk.AgentEvent] {
	_ = input
	_ = options
	iter, gen := adk.NewAsyncIteratorPair[*adk.AgentEvent]()
	go func() {
		defer gen.Close()
		if a.body == nil {
			gen.Send(&adk.AgentEvent{Err: errors.New("assistant body is not configured")})
			return
		}
		result, err := a.body(ctx, a.req, a.sink)
		if err != nil {
			gen.Send(&adk.AgentEvent{Err: err})
			return
		}
		gen.Send(&adk.AgentEvent{
			Output: &adk.AgentOutput{
				MessageOutput: &adk.MessageVariant{
					Message: schema.AssistantMessage(result.Content, nil),
					Role:    schema.Assistant,
				},
				CustomizedOutput: result,
			},
		})
	}()
	return iter
}

func projectEinoAssistantPrompt(req projectAssistantRunRequest) string {
	if req.Project != nil && req.Project.Name != "" {
		return "Run the App Studio project assistant for " + req.Project.Name + "."
	}
	return "Run the App Studio project assistant."
}
