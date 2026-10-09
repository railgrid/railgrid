/*
Copyright 2026 The Railgrid Authors.

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
	"net/http"

	aiv1alpha1 "github.com/railgrid/provider-app-studio/apis/ai/v1alpha1"
)

// projectAssistantToolPort keeps HTTP transport at the App Studio orchestration
// boundary. Eino receives capability-scoped tool metadata and invokes tools
// through this port; it never receives the caller's HTTP request.
type projectAssistantToolPort interface {
	DiscoverMCP(context.Context, identity, *aiv1alpha1.Project, projectLLMSettings) ([]projectAssistantTool, bool, error)
	Invoke(context.Context, projectAssistantTool, projectAssistantToolCallRequest) (string, error)
}

type projectAssistantHTTPToolPort struct {
	server  *Server
	request *http.Request
}

func newProjectAssistantHTTPToolPort(server *Server, request *http.Request) projectAssistantToolPort {
	if server == nil || request == nil {
		return nil
	}
	return projectAssistantHTTPToolPort{server: server, request: request}
}

func (p projectAssistantHTTPToolPort) DiscoverMCP(ctx context.Context, id identity, project *aiv1alpha1.Project, settings projectLLMSettings) ([]projectAssistantTool, bool, error) {
	if p.server == nil || p.request == nil {
		return nil, false, errors.New("the App Studio tool transport is not configured")
	}
	return p.server.loadProjectMCPAssistantTools(p.request.WithContext(ctx), id, project, settings)
}

func (p projectAssistantHTTPToolPort) Invoke(ctx context.Context, tool projectAssistantTool, req projectAssistantToolCallRequest) (string, error) {
	if p.request == nil {
		return "", errors.New("the App Studio tool transport is not configured")
	}
	if tool == nil {
		return "", errors.New("an App Studio tool is required")
	}
	// Every aggregate MCP call a tool makes is authenticated as the Project's
	// own hub-minted identity; a tool that cannot obtain one does not run.
	httpReq, err := p.server.projectMCPRequest(ctx, p.request, req.Identity, req.Project)
	if err != nil {
		return "", err
	}
	req.HTTPRequest = httpReq
	return tool.Call(ctx, req)
}
