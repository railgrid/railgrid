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

	"github.com/cloudwego/eino/adk"
	einotool "github.com/cloudwego/eino/components/tool"
)

// projectEinoAssistantLifecycle observes the two facts App Studio must enforce
// around source effects: a successful source mutation invalidates prior
// verification, and commit requires successful verification of the latest
// mutation. It deliberately does not infer phases or rewrite the model's tool
// catalog; Eino remains the owner of the conversational execution loop.
type projectEinoAssistantLifecycle struct {
	*adk.BaseChatModelAgentMiddleware

	runState *projectEinoAssistantRunState
}

func projectEinoAssistantLifecycleMiddleware(
	_ projectAssistantRunRequest,
	runState *projectEinoAssistantRunState,
) adk.ChatModelAgentMiddleware {
	return &projectEinoAssistantLifecycle{
		BaseChatModelAgentMiddleware: &adk.BaseChatModelAgentMiddleware{},
		runState:                     runState,
	}
}

func (m *projectEinoAssistantLifecycle) WrapInvokableToolCall(
	_ context.Context,
	endpoint adk.InvokableToolCallEndpoint,
	toolCtx *adk.ToolContext,
) (adk.InvokableToolCallEndpoint, error) {
	if toolCtx == nil {
		return endpoint, nil
	}
	name := projectToolBaseName(toolCtx.Name)
	return func(ctx context.Context, argumentsInJSON string, opts ...einotool.Option) (string, error) {
		if projectEinoAssistantCommitTool(name) && !m.commitResume(ctx) &&
			(m.runState == nil || !m.runState.SourceMutationVerified()) {
			return "Tool call failed: verify_development_runtime must successfully verify the latest workspace mutation before commit approval can be requested.", nil
		}

		result, err := endpoint(ctx, argumentsInJSON, opts...)
		if err != nil {
			if name == projectToolVerifyDevelopmentRuntime && m.runState != nil {
				m.runState.RecordDevelopmentVerification(false)
			}
			return result, err
		}
		switch {
		case projectEinoAssistantWorkspaceMutationTool(name) &&
			projectEinoAssistantSuccessfulWorkspaceMutationResult(name, result):
			if m.runState != nil {
				m.runState.RecordSourceMutation()
			}
		case name == projectToolVerifyDevelopmentRuntime:
			if m.runState != nil {
				m.runState.RecordDevelopmentVerificationResult(result)
			}
		}
		return result, nil
	}, nil
}

func (m *projectEinoAssistantLifecycle) commitResume(ctx context.Context) bool {
	wasInterrupted, hasState, state := einotool.GetInterruptState[*projectEinoPermissionInterruptState](ctx)
	return wasInterrupted && hasState && state != nil &&
		projectEinoAssistantCommitTool(strings.TrimSpace(state.ToolName))
}

func projectEinoAssistantVerificationReady(content string) bool {
	return projectEinoAssistantPhaseVerificationReady(content)
}
