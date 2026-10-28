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
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/railgrid/provider-sdk/dataplane"

	aiv1alpha1 "github.com/railgrid/provider-app-studio/apis/ai/v1alpha1"
	"github.com/railgrid/provider-app-studio/workspace"
	"github.com/railgrid/provider-sdk/mcpcode"
)

func TestProjectAssistantHTTPToolPortLocalReadDoesNotRequireProjectIdentity(t *testing.T) {
	ctx := context.Background()
	files := workspace.NewFileStore(t.TempDir())
	server := NewWithWorkspace(nil, nil, files, "", false)
	identityCalls := 0
	server.projectIdentityTokenFor = func(context.Context, identity, *aiv1alpha1.Project) (string, error) {
		identityCalls++
		return "", errors.New("project identity exceeds the 64-rule limit")
	}
	server.hubToken = "provider-token"

	id := identity{
		orgUUID: "org-a", workspaceUUID: "workspace-a", tenant: "root:tenant-a",
		clusterID: "cluster-a", user: "alice",
	}
	project := &aiv1alpha1.Project{ObjectMeta: metav1.ObjectMeta{Name: "demo", UID: "project-uid-demo"}}
	scope := projectWorkspaceScope(id, project)
	if _, err := files.CreateFile(ctx, scope, workspace.CreateOptions{Path: "src/main.go", Content: "package demo\n"}); err != nil {
		t.Fatalf("seed local project file: %v", err)
	}

	baseTool, ok := projectAssistantLocalToolRegistry(server).Get(projectToolReadFile)
	if !ok {
		t.Fatal("read_file tool is missing")
	}
	if baseTool.Spec().RequiresProjectMCPIdentity {
		t.Fatal("local read_file unexpectedly requires a Project MCP identity")
	}
	tool := projectAssistantToolFunc{
		spec: baseTool.Spec(),
		call: func(callCtx context.Context, req projectAssistantToolCallRequest) (string, error) {
			if req.HTTPRequest != nil {
				t.Fatal("local read_file received an MCP HTTP request")
			}
			return baseTool.Call(callCtx, req)
		},
	}
	request := httptest.NewRequest(http.MethodPost, "/assistant", nil)
	request.Header.Set("Authorization", "Bearer caller-token")
	request.Header.Set(dataplane.HeaderActionProof, "short-lived-proof")
	port := projectAssistantHTTPToolPort{server: server, request: request}
	result, err := port.Invoke(ctx, tool, projectAssistantToolCallRequest{
		Identity: id, Project: project, WorkspaceScope: scope,
		Arguments: map[string]any{"file_path": "src/main.go"},
	})
	if err != nil {
		t.Fatalf("Invoke local read_file: %v", err)
	}
	if !strings.Contains(result, "package demo") {
		t.Fatalf("read_file result = %q, want local file content", result)
	}
	if identityCalls != 0 {
		t.Fatalf("Project identity service calls = %d, want none for local read_file", identityCalls)
	}
}

func TestProjectAssistantHTTPToolPortMCPUsesOnlyProjectIdentity(t *testing.T) {
	var gotAuthorization, gotUser, gotTenant string
	var gotActionProof string
	mcp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuthorization = r.Header.Get("Authorization")
		gotUser = r.Header.Get(dataplane.HeaderUser)
		gotTenant = r.Header.Get(dataplane.HeaderTenant)
		gotActionProof = r.Header.Get(dataplane.HeaderActionProof)
		// The aggregate serves code mode: the named tool arrives inside
		// execute, and its result goes back inside the envelope.
		var request struct {
			Params struct {
				Name      string `json:"name"`
				Arguments struct {
					Code string `json:"code"`
				} `json:"arguments"`
			} `json:"params"`
		}
		_ = json.NewDecoder(r.Body).Decode(&request)
		tool, _, ok := mcpcode.ParseCallCode(request.Params.Arguments.Code)
		if request.Params.Name != mcpcode.ToolExecute || !ok || tool != projectToolAgentsListAgents {
			t.Errorf("aggregate request = %#v, want execute of %s", request.Params, projectToolAgentsListAgents)
		}
		result, _ := mcpcode.EncodeCallResult(tool, json.RawMessage(`{"content":[{"type":"text","text":"mcp result"}]}`))
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":1,"result":%s}`, result)
	}))
	t.Cleanup(mcp.Close)

	identityCalls := 0
	server := &Server{hubToken: "provider-token"}
	server.projectIdentityTokenFor = func(_ context.Context, _ identity, project *aiv1alpha1.Project) (string, error) {
		identityCalls++
		if project == nil || project.Name != "demo" {
			t.Fatalf("Project identity requested for %#v, want demo", project)
		}
		return "project-token", nil
	}
	tools := projectAssistantMCPToolsForSpecs([]projectMCPTool{{Name: projectToolAgentsListAgents}})
	if len(tools) != 1 || tools[0] == nil {
		t.Fatalf("constructed %d agents MCP tools, want one", len(tools))
	}
	tool := tools[0]
	if !tool.Spec().RequiresProjectMCPIdentity {
		t.Fatalf("discovered MCP tool spec %#v lacks the Project identity capability", tool.Spec())
	}

	id := identity{
		orgUUID: "org-a", workspaceUUID: "workspace-a", tenant: "root:tenant-a",
		clusterID: "cluster-a", user: "alice",
	}
	project := &aiv1alpha1.Project{ObjectMeta: metav1.ObjectMeta{Name: "demo", UID: "project-uid-demo"}}
	request := httptest.NewRequest(http.MethodPost, "/assistant", nil)
	request.Header.Set("Authorization", "Bearer caller-token")
	request.Header.Set(dataplane.HeaderActionProof, "short-lived-proof")
	port := projectAssistantHTTPToolPort{server: server, request: request}
	result, err := port.Invoke(context.Background(), tool, projectAssistantToolCallRequest{
		Identity: id, Project: project, MCPEndpoint: mcp.URL,
		Arguments: map[string]any{},
	})
	if err != nil {
		t.Fatalf("Invoke MCP tool: %v", err)
	}
	if result != "mcp result" {
		t.Fatalf("MCP result = %q, want response text", result)
	}
	if gotAuthorization != "Bearer project-token" {
		t.Fatalf("MCP Authorization = %q, want Project identity", gotAuthorization)
	}
	if gotUser != "alice" || gotTenant != id.tenant {
		t.Fatalf("MCP scope headers user=%q tenant=%q, want caller label and tenant", gotUser, gotTenant)
	}
	if gotActionProof != "" {
		t.Fatalf("MCP forwarded hub action proof %q", gotActionProof)
	}
	if identityCalls != 1 {
		t.Fatalf("Project identity service calls = %d, want one", identityCalls)
	}
	if got := request.Header.Get("Authorization"); got != "Bearer caller-token" {
		t.Fatalf("Invoke mutated inbound Authorization to %q", got)
	}
}

func TestProjectAssistantHTTPToolPortMCPFailsClosedWhenProjectIdentityIsUnavailable(t *testing.T) {
	mcpCalls := 0
	mcp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mcpCalls++
		_, _ = fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"should not run"}]}}`)
	}))
	t.Cleanup(mcp.Close)

	identityCalls := 0
	server := &Server{hubToken: "provider-token"}
	server.projectIdentityTokenFor = func(context.Context, identity, *aiv1alpha1.Project) (string, error) {
		identityCalls++
		return "", errors.New("too many Project rules")
	}
	tools := projectAssistantMCPToolsForSpecs([]projectMCPTool{{Name: projectToolAgentsListAgents}})
	if len(tools) != 1 {
		t.Fatalf("constructed %d MCP tools, want one", len(tools))
	}
	request := httptest.NewRequest(http.MethodPost, "/assistant", nil)
	request.Header.Set("Authorization", "Bearer caller-token")
	port := projectAssistantHTTPToolPort{server: server, request: request}
	_, err := port.Invoke(context.Background(), tools[0], projectAssistantToolCallRequest{
		Identity:    identity{tenant: "root:tenant-a", clusterID: "cluster-a", user: "alice"},
		Project:     &aiv1alpha1.Project{ObjectMeta: metav1.ObjectMeta{Name: "demo"}},
		MCPEndpoint: mcp.URL,
	})
	if err == nil || !strings.Contains(err.Error(), "too many Project rules") {
		t.Fatalf("Invoke MCP tool error = %v, want Project identity refusal", err)
	}
	if identityCalls != 1 || mcpCalls != 0 {
		t.Fatalf("Project identity calls=%d MCP calls=%d, want one refusal and no MCP call", identityCalls, mcpCalls)
	}
}

func TestProjectAssistantToolSpecsDeclareProjectMCPTransport(t *testing.T) {
	for _, name := range []string{projectToolAgentsListAgents, projectToolInfrastructureListTemplates, projectToolDatabricksListTables} {
		spec, ok := projectAssistantMCPToolSpec(projectMCPTool{Name: name})
		if !ok || !spec.RequiresProjectMCPIdentity {
			t.Errorf("MCP tool spec %q = %#v, ok=%v; want Project identity capability", name, spec, ok)
		}
	}

	registry := projectAssistantLocalToolRegistry(nil)
	for _, name := range []string{projectToolGetBuildLogs, projectToolRebuildProject} {
		spec, ok := registry.Spec(name)
		if !ok || !spec.RequiresProjectMCPIdentity {
			t.Errorf("local registry tool %q = %#v, ok=%v; its implementation calls MCP", name, spec, ok)
		}
	}
	if spec, ok := registry.Spec(projectToolReadFile); !ok || spec.RequiresProjectMCPIdentity {
		t.Errorf("local file tool %q = %#v, ok=%v; want no aggregate MCP identity requirement", projectToolReadFile, spec, ok)
	}
}
