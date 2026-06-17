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

import "context"

type projectChatCompletionAssistantEngine struct {
	server *Server
}

func (e projectChatCompletionAssistantEngine) StreamProjectAssistant(
	ctx context.Context,
	req projectAssistantRunRequest,
	sink projectAssistantEventSink,
) (projectAssistantRunResult, error) {
	_ = sink
	if err := ctx.Err(); err != nil {
		return projectAssistantRunResult{}, err
	}
	reply, err := e.server.runProjectAssistantChatLoop(ctx, req)
	if err != nil {
		return projectAssistantRunResult{}, err
	}
	return projectAssistantRunResult{Content: reply}, nil
}
