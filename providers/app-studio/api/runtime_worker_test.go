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
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/faroshq/provider-app-studio/store"
	"github.com/faroshq/provider-app-studio/workspace"
)

func TestProjectRuntimeWorkerToolsAbsentByDefault(t *testing.T) {
	server := NewWithWorkspace(nil, store.NewMemoryStore(), workspace.NewFileStore(t.TempDir()), "", false)
	registry := server.projectAssistantToolRegistry()
	if registry.Has(projectToolRuntimeCommand) {
		t.Fatal("runtime_command tool registered without a runtime worker")
	}
	if projectLocalToolAllowed(projectToolRuntimeCommand) {
		t.Fatal("runtime_command is globally local-allowed without a runtime worker")
	}
	if tool := newProjectRuntimeCommandToolForRegistry(server); tool != nil {
		t.Fatalf("runtime tool = %#v, want nil without worker", tool)
	}
}

func TestProjectRuntimeWorkerCommandUnavailableWithoutWorker(t *testing.T) {
	server := NewWithWorkspace(nil, store.NewMemoryStore(), workspace.NewFileStore(t.TempDir()), "", false)
	id := identity{tenantPath: "root:org-a:ws-1", orgUUID: "org-a", workspaceUUID: "ws-1"}
	scope := projectWorkspaceScope(id, "demo")
	messages, err := server.resolveProjectToolCalls(
		context.Background(),
		id,
		nil,
		nil,
		scope,
		"",
		[]chatToolCall{{
			ID:   "call-runtime",
			Type: "function",
			Function: chatToolCallFunction{
				Name:      projectToolRuntimeCommand,
				Arguments: `{"command":["npm","test"],"timeoutSeconds":30}`,
			},
		}},
		httptest.NewRequest(http.MethodPost, "/", nil),
		nil,
	)
	if err != nil {
		t.Fatalf("resolveProjectToolCalls returned error: %v", err)
	}
	if len(messages) != 1 || !strings.Contains(messages[0].Content, `"status":"unavailable"`) || !strings.Contains(messages[0].Content, "runtime worker is not configured") {
		t.Fatalf("tool messages = %#v, want unavailable runtime worker response", messages)
	}
}

func TestProjectRuntimeWorkerRequiresApprovalWithWorker(t *testing.T) {
	messages := store.NewMemoryStore()
	server := NewWithWorkspace(nil, messages, workspace.NewFileStore(t.TempDir()), "", false)
	worker := &fakeProjectRuntimeWorker{handle: projectRuntimeHandle{ID: "runtime-1"}}
	server.runtimeWorker = worker
	registry := server.projectAssistantToolRegistry()
	tool, ok := registry.Get(projectToolRuntimeCommand)
	if !ok {
		t.Fatal("runtime_command tool missing with runtime worker")
	}
	if got := projectAssistantPermissionForTool(tool.Spec()); got != projectAssistantPermissionAsk {
		t.Fatalf("permission = %q, want ask", got)
	}
	id := identity{tenantPath: "root:org-a:ws-1", orgUUID: "org-a", workspaceUUID: "ws-1"}
	_, err := server.resolveProjectToolCallsWithPermissions(
		context.Background(),
		projectAssistantRunRequest{
			Identity:       id,
			HTTPRequest:    httptest.NewRequest(http.MethodPost, "/", nil),
			WorkspaceScope: projectWorkspaceScope(id, "demo"),
			MessageScope:   projectMessageScope(id.orgUUID, id.workspaceUUID, "demo"),
		},
		projectAssistantCheckpointState{},
		[]chatToolCall{{
			ID:   "call-runtime",
			Type: "function",
			Function: chatToolCallFunction{
				Name:      projectToolRuntimeCommand,
				Arguments: `{"command":["npm","test"],"timeoutSeconds":30}`,
			},
		}},
	)
	var permissionErr *projectAssistantPermissionRequiredError
	if !strings.Contains(projectAssistantPermissionReason(tool.Spec()), "runtime command") {
		t.Fatalf("permission reason = %q, want runtime command context", projectAssistantPermissionReason(tool.Spec()))
	}
	if !errors.As(err, &permissionErr) {
		t.Fatalf("resolveProjectToolCallsWithPermissions error = %v, want permission required", err)
	}
	if worker.calls != 0 {
		t.Fatalf("worker calls = %d, want no start before approval", worker.calls)
	}
}

func TestProjectRuntimeWorkerStartsInjectedWorker(t *testing.T) {
	worker := &fakeProjectRuntimeWorker{handle: projectRuntimeHandle{ID: "runtime-1"}}
	tool := newProjectRuntimeCommandTool(worker)
	id := identity{tenantPath: "root:org-a:ws-1", orgUUID: "org-a", workspaceUUID: "ws-1"}
	scope := projectWorkspaceScope(id, "demo")
	raw, err := tool.Call(context.Background(), projectAssistantToolCallRequest{
		Identity:       id,
		WorkspaceScope: scope,
		Arguments: map[string]any{
			"command":        []any{"npm", "test"},
			"timeoutSeconds": 30,
		},
	})
	if err != nil {
		t.Fatalf("Call returned error: %v", err)
	}
	var result projectRuntimeCommandResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatalf("decode runtime result: %v", err)
	}
	if result.Status != "started" || result.ID != "runtime-1" {
		t.Fatalf("result = %#v, want started runtime-1", result)
	}
	if worker.calls != 1 {
		t.Fatalf("worker calls = %d, want 1", worker.calls)
	}
	if strings.Join(worker.request.Command, " ") != "npm test" || worker.request.TimeoutSeconds != 30 || worker.request.WorkspaceScope != scope {
		t.Fatalf("worker request = %#v, want mapped command, timeout, and scope", worker.request)
	}
}

func TestProjectRuntimeWorkerPreservesCommandArgs(t *testing.T) {
	worker := &fakeProjectRuntimeWorker{handle: projectRuntimeHandle{ID: "runtime-1"}}
	tool := newProjectRuntimeCommandTool(worker)
	_, err := tool.Call(context.Background(), projectAssistantToolCallRequest{
		Arguments: map[string]any{"command": []any{"printf", " %s\n"}},
	})
	if err != nil {
		t.Fatalf("Call returned error: %v", err)
	}
	if len(worker.request.Command) != 2 || worker.request.Command[1] != " %s\n" {
		t.Fatalf("command = %#v, want padded format string preserved exactly", worker.request.Command)
	}
}

func TestProjectRuntimeWorkerRejectsMalformedCommandArgs(t *testing.T) {
	tests := []struct {
		name string
		args map[string]any
		want string
	}{
		{
			name: "non string",
			args: map[string]any{"command": []any{"npm", float64(1), "test"}},
			want: "argument 1 must be a string",
		},
		{
			name: "empty string",
			args: map[string]any{"command": []any{"npm", " ", "test"}},
			want: "argument 1 cannot be empty",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			worker := &fakeProjectRuntimeWorker{handle: projectRuntimeHandle{ID: "runtime-1"}}
			tool := newProjectRuntimeCommandTool(worker)
			_, err := tool.Call(context.Background(), projectAssistantToolCallRequest{Arguments: tt.args})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Call error = %v, want %q", err, tt.want)
			}
			if worker.calls != 0 {
				t.Fatalf("worker calls = %d, want no start for malformed command", worker.calls)
			}
		})
	}
}

type fakeProjectRuntimeWorker struct {
	handle  projectRuntimeHandle
	request projectRuntimeRequest
	calls   int
}

func (w *fakeProjectRuntimeWorker) Start(_ context.Context, req projectRuntimeRequest) (projectRuntimeHandle, error) {
	w.calls++
	w.request = req
	return w.handle, nil
}
