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
	"net/http"
)

// projectAssistantToolPort keeps HTTP transport at the App Studio orchestration
// boundary. Eino receives capability-scoped tool metadata and invokes tools
// through this port; it never receives the caller's HTTP request.
type projectAssistantToolPort interface {
	DiscoverMCP(context.Context, identity, projectLLMSettings) ([]projectAssistantTool, bool, error)
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

func (p projectAssistantHTTPToolPort) DiscoverMCP(ctx context.Context, id identity, settings projectLLMSettings) ([]projectAssistantTool, bool, error) {
	if p.server == nil || p.request == nil {
		return nil, false, errors.New("App Studio tool transport is not configured")
	}
	return p.server.loadProjectMCPAssistantTools(p.request.WithContext(ctx), id, settings)
}

func (p projectAssistantHTTPToolPort) Invoke(ctx context.Context, tool projectAssistantTool, req projectAssistantToolCallRequest) (string, error) {
	if p.request == nil {
		return "", errors.New("App Studio tool transport is not configured")
	}
	if tool == nil {
		return "", errors.New("App Studio tool is required")
	}
	req.HTTPRequest = p.request.WithContext(ctx)
	return tool.Call(ctx, req)
}
