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
)

type projectEinoAssistantEngine struct {
	body   projectAssistantEngine
	runner *adk.Runner
}

// NewEinoAssistantEngine returns an Eino-backed assistant engine construction
// proof. Later stack slices route normal assistant execution through this
// runtime.
func NewEinoAssistantEngine(server *Server) projectAssistantEngine {
	return projectEinoAssistantEngine{
		body:   projectChatCompletionAssistantEngine{server: server},
		runner: adk.NewRunner(context.Background(), adk.RunnerConfig{EnableStreaming: true}),
	}
}

func (e projectEinoAssistantEngine) StreamProjectAssistant(
	ctx context.Context,
	req projectAssistantRunRequest,
	sink projectAssistantEventSink,
) (projectAssistantRunResult, error) {
	if req.Project == nil {
		return projectAssistantRunResult{}, errors.New("project is required")
	}
	if e.runner == nil {
		return projectAssistantRunResult{}, errors.New("eino runner is not configured")
	}
	if e.body == nil {
		return projectAssistantRunResult{}, errors.New("assistant body is not configured")
	}
	return e.body.StreamProjectAssistant(ctx, req, sink)
}
