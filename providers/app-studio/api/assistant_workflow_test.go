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
	"strings"
	"testing"

	aiv1alpha1 "github.com/faroshq/provider-app-studio/apis/ai/v1alpha1"
	"github.com/faroshq/provider-app-studio/store"
	"github.com/faroshq/provider-app-studio/workspace"
)

func TestProjectAssistantWorkflowRegisteredReadOnly(t *testing.T) {
	server := NewWithWorkspace(nil, store.NewMemoryStore(), workspace.NewFileStore(t.TempDir()), "", false)
	registry := server.projectAssistantToolRegistry()
	tool, ok := registry.Get(projectToolPlanProjectChanges)
	if !ok {
		t.Fatal("plan_project_changes tool missing from registry")
	}
	spec := tool.Spec()
	if spec.Risk != projectAssistantToolRiskRead {
		t.Fatalf("risk = %q, want read", spec.Risk)
	}
	if got := projectAssistantPermissionForTool(spec); got != projectAssistantPermissionAllow {
		t.Fatalf("permission = %q, want allow", got)
	}
	if strings.TrimSpace(string(spec.Parameters)) == "" {
		t.Fatal("workflow tool parameters are empty")
	}
}

func TestProjectAssistantReadinessWorkflowRegisteredReadOnly(t *testing.T) {
	server := NewWithWorkspace(nil, store.NewMemoryStore(), workspace.NewFileStore(t.TempDir()), "", false)
	registry := server.projectAssistantToolRegistry()
	tool, ok := registry.Get(projectToolCheckProjectReadiness)
	if !ok {
		t.Fatal("check_project_readiness tool missing from registry")
	}
	spec := tool.Spec()
	if spec.Risk != projectAssistantToolRiskRead {
		t.Fatalf("risk = %q, want read", spec.Risk)
	}
	if got := projectAssistantPermissionForTool(spec); got != projectAssistantPermissionAllow {
		t.Fatalf("permission = %q, want allow", got)
	}
	if strings.TrimSpace(string(spec.Parameters)) == "" {
		t.Fatal("readiness workflow tool parameters are empty")
	}
}

func TestProjectAssistantWorkflowPlansFromMemoryRepositoryAndWorkspace(t *testing.T) {
	workspaces := workspace.NewFileStore(t.TempDir())
	server := NewWithWorkspace(nil, store.NewMemoryStore(), workspaces, "", false)
	project := projectWithRepository("demo-repo", "demo", "github")
	project.Name = "demo"
	project.Spec.DisplayName = "Demo App"
	project.Spec.Memory = aiv1alpha1.ProjectMemory{
		Goals:        []string{"ship a task tracker"},
		Requirements: []string{"persist tasks"},
		Constraints:  []string{"avoid external queues"},
	}
	id := identity{tenantPath: "root:org-a:ws-1", orgUUID: "org-a", workspaceUUID: "ws-1"}
	scope := projectWorkspaceScope(id, project.Name)
	if _, err := workspaces.WriteFile(context.Background(), scope, workspace.WriteOptions{Path: "src/App.tsx", Content: "export function App() { return null }\n"}); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}
	tool, ok := server.projectAssistantToolRegistry().Get(projectToolPlanProjectChanges)
	if !ok {
		t.Fatal("plan_project_changes tool missing from registry")
	}

	raw, err := tool.Call(context.Background(), projectAssistantToolCallRequest{
		Identity:       id,
		Project:        project,
		Repository:     &ProjectRepositoryView{Ref: "demo-repo", Name: "demo", Status: projectRepositoryStatusReady},
		WorkspaceScope: scope,
		Arguments:      map[string]any{"includeFiles": true},
	})
	if err != nil {
		t.Fatalf("Call returned error: %v", err)
	}
	if len(raw) > projectAssistantWorkflowMaxResultBytes {
		t.Fatalf("workflow result length = %d, want <= %d", len(raw), projectAssistantWorkflowMaxResultBytes)
	}
	var plan projectAssistantWorkflowPlan
	if err := json.Unmarshal([]byte(raw), &plan); err != nil {
		t.Fatalf("workflow result is not JSON: %v\n%s", err, raw)
	}
	if !strings.Contains(plan.Summary, "Demo App") {
		t.Fatalf("summary = %q, want project display name", plan.Summary)
	}
	if !containsString(plan.Goals, "ship a task tracker") || !containsString(plan.Requirements, "persist tasks") || !containsString(plan.Constraints, "avoid external queues") {
		t.Fatalf("plan memory = %#v, want project memory copied", plan)
	}
	if plan.Repository == nil || plan.Repository.Ref != "demo-repo" || plan.Repository.Status != projectRepositoryStatusReady {
		t.Fatalf("repository = %#v, want ready demo-repo", plan.Repository)
	}
	if !containsString(plan.Files, "src/App.tsx") {
		t.Fatalf("files = %#v, want workspace file", plan.Files)
	}
	if len(plan.Steps) == 0 {
		t.Fatalf("steps = %#v, want at least one deterministic next step", plan.Steps)
	}
}

func TestProjectAssistantReadinessWorkflowReportsContextTraceAndChecks(t *testing.T) {
	workspaces := workspace.NewFileStore(t.TempDir())
	server := NewWithWorkspace(nil, store.NewMemoryStore(), workspaces, "", false)
	project := projectWithRepository("demo-repo", "demo", "github")
	project.Name = "demo"
	project.Spec.DisplayName = "Demo App"
	project.Spec.Memory.Requirements = []string{"ship a tested build"}
	id := identity{tenantPath: "root:org-a:ws-1", orgUUID: "org-a", workspaceUUID: "ws-1"}
	scope := projectWorkspaceScope(id, project.Name)
	if _, err := workspaces.WriteFile(context.Background(), scope, workspace.WriteOptions{Path: "package.json", Content: `{"scripts":{"build":"vite build","test":"vitest"}}`}); err != nil {
		t.Fatalf("WriteFile package.json returned error: %v", err)
	}
	if _, err := workspaces.WriteFile(context.Background(), scope, workspace.WriteOptions{Path: "src/App.tsx", Content: "export function App() { return null }\n"}); err != nil {
		t.Fatalf("WriteFile src/App.tsx returned error: %v", err)
	}
	tool, ok := server.projectAssistantToolRegistry().Get(projectToolCheckProjectReadiness)
	if !ok {
		t.Fatal("check_project_readiness tool missing from registry")
	}

	raw, err := tool.Call(context.Background(), projectAssistantToolCallRequest{
		Identity:       id,
		Project:        project,
		Repository:     &ProjectRepositoryView{Ref: "demo-repo", Name: "demo", Status: projectRepositoryStatusReady},
		WorkspaceScope: scope,
		Arguments:      map[string]any{},
	})
	if err != nil {
		t.Fatalf("Call returned error: %v", err)
	}
	if len(raw) > projectAssistantWorkflowMaxResultBytes {
		t.Fatalf("workflow result length = %d, want <= %d", len(raw), projectAssistantWorkflowMaxResultBytes)
	}
	var readiness struct {
		Status            string               `json:"status"`
		Summary           string               `json:"summary"`
		RecommendedChecks []string             `json:"recommendedChecks"`
		Trace             []workflowTraceEntry `json:"trace"`
	}
	if err := json.Unmarshal([]byte(raw), &readiness); err != nil {
		t.Fatalf("workflow result is not JSON: %v\n%s", err, raw)
	}
	if readiness.Status != "ready_to_verify" {
		t.Fatalf("status = %q, want ready_to_verify", readiness.Status)
	}
	if !strings.Contains(readiness.Summary, "Demo App") {
		t.Fatalf("summary = %q, want project display name", readiness.Summary)
	}
	if !containsString(readiness.RecommendedChecks, "build") || !containsString(readiness.RecommendedChecks, "test") {
		t.Fatalf("recommended checks = %#v, want build and test", readiness.RecommendedChecks)
	}
	if !workflowTraceContains(readiness.Trace, "read-context") || !workflowTraceContains(readiness.Trace, "format-readiness") {
		t.Fatalf("trace = %#v, want read-context and format-readiness nodes", readiness.Trace)
	}
}

func TestProjectAssistantWorkflowDoesNotMutateWorkspace(t *testing.T) {
	workspaces := workspace.NewFileStore(t.TempDir())
	server := NewWithWorkspace(nil, store.NewMemoryStore(), workspaces, "", false)
	project := projectWithRepository("demo-repo", "demo", "github")
	project.Name = "demo"
	id := identity{tenantPath: "root:org-a:ws-1", orgUUID: "org-a", workspaceUUID: "ws-1"}
	scope := projectWorkspaceScope(id, project.Name)
	if _, err := workspaces.WriteFile(context.Background(), scope, workspace.WriteOptions{Path: "README.md", Content: "# Demo\n"}); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}
	before, err := workspaces.ListFiles(context.Background(), scope, workspace.ListOptions{Limit: 10})
	if err != nil {
		t.Fatalf("ListFiles before returned error: %v", err)
	}
	tool, ok := server.projectAssistantToolRegistry().Get(projectToolPlanProjectChanges)
	if !ok {
		t.Fatal("plan_project_changes tool missing from registry")
	}
	if _, err := tool.Call(context.Background(), projectAssistantToolCallRequest{
		Identity:       id,
		Project:        project,
		WorkspaceScope: scope,
		Arguments:      map[string]any{"includeFiles": true},
	}); err != nil {
		t.Fatalf("Call returned error: %v", err)
	}
	after, err := workspaces.ListFiles(context.Background(), scope, workspace.ListOptions{Limit: 10})
	if err != nil {
		t.Fatalf("ListFiles after returned error: %v", err)
	}
	if strings.Join(workflowTestFilePaths(before.Files), "\n") != strings.Join(workflowTestFilePaths(after.Files), "\n") {
		t.Fatalf("files changed from %#v to %#v", before.Files, after.Files)
	}
}

func TestProjectAssistantWorkflowBoundsLargeResultAsJSON(t *testing.T) {
	server := NewWithWorkspace(nil, store.NewMemoryStore(), workspace.NewFileStore(t.TempDir()), "", false)
	project := projectWithRepository("demo-repo", "demo", "github")
	project.Name = "demo"
	project.Spec.DisplayName = strings.Repeat("Demo App ", 80)
	for i := 0; i < 80; i++ {
		project.Spec.Memory.Goals = append(project.Spec.Memory.Goals, strings.Repeat("goal ", 80))
		project.Spec.Memory.Requirements = append(project.Spec.Memory.Requirements, strings.Repeat("requirement ", 80))
		project.Spec.Memory.Constraints = append(project.Spec.Memory.Constraints, strings.Repeat("constraint ", 80))
	}
	tool, ok := server.projectAssistantToolRegistry().Get(projectToolPlanProjectChanges)
	if !ok {
		t.Fatal("plan_project_changes tool missing from registry")
	}

	raw, err := tool.Call(context.Background(), projectAssistantToolCallRequest{
		Project:   project,
		Arguments: map[string]any{"includeFiles": false},
	})
	if err != nil {
		t.Fatalf("Call returned error: %v", err)
	}
	if len(raw) > projectAssistantWorkflowMaxResultBytes {
		t.Fatalf("workflow result length = %d, want <= %d", len(raw), projectAssistantWorkflowMaxResultBytes)
	}
	var plan projectAssistantWorkflowPlan
	if err := json.Unmarshal([]byte(raw), &plan); err != nil {
		t.Fatalf("bounded workflow result is not JSON: %v\n%s", err, raw)
	}
	if len(plan.Steps) == 0 {
		t.Fatalf("steps = %#v, want bounded guidance", plan.Steps)
	}
}

func TestProjectAssistantRuntimeVerificationWorkflowRegistersWithRuntimeWorker(t *testing.T) {
	server := NewWithWorkspace(nil, store.NewMemoryStore(), workspace.NewFileStore(t.TempDir()), "", false)
	worker := &fakeProjectRuntimeWorker{handle: projectRuntimeHandle{ID: "runtime-1"}}
	server.runtimeWorker = worker
	registry := server.projectAssistantToolRegistry()
	tool, ok := registry.Get(projectToolVerifyProjectRuntime)
	if !ok {
		t.Fatal("verify_project_runtime tool missing with runtime worker")
	}
	spec := tool.Spec()
	if spec.Risk != projectAssistantToolRiskRuntime {
		t.Fatalf("risk = %q, want runtime", spec.Risk)
	}
	if got := projectAssistantPermissionForTool(spec); got != projectAssistantPermissionAsk {
		t.Fatalf("permission = %q, want ask", got)
	}
	if strings.TrimSpace(string(spec.Parameters)) == "" {
		t.Fatal("runtime verification workflow tool parameters are empty")
	}
}

func TestProjectAssistantRuntimeVerificationWorkflowStartsPresetChecks(t *testing.T) {
	server := NewWithWorkspace(nil, store.NewMemoryStore(), workspace.NewFileStore(t.TempDir()), "", false)
	worker := &recordingProjectRuntimeWorker{
		handles: []projectRuntimeHandle{{ID: "runtime-build"}, {ID: "runtime-test"}},
	}
	server.runtimeWorker = worker
	project := projectWithRepository("demo-repo", "demo", "github")
	project.Name = "demo"
	id := identity{tenantPath: "root:org-a:ws-1", orgUUID: "org-a", workspaceUUID: "ws-1"}
	scope := projectWorkspaceScope(id, project.Name)
	tool, ok := server.projectAssistantToolRegistry().Get(projectToolVerifyProjectRuntime)
	if !ok {
		t.Fatal("verify_project_runtime tool missing with runtime worker")
	}

	raw, err := tool.Call(context.Background(), projectAssistantToolCallRequest{
		Identity:       id,
		Project:        project,
		Repository:     &ProjectRepositoryView{Ref: "demo-repo", Name: "demo", Status: projectRepositoryStatusReady},
		WorkspaceScope: scope,
		Arguments: map[string]any{
			"checks":         []any{"build", "test"},
			"timeoutSeconds": 30,
		},
	})
	if err != nil {
		t.Fatalf("Call returned error: %v", err)
	}
	if len(raw) > projectAssistantWorkflowMaxResultBytes {
		t.Fatalf("workflow result length = %d, want <= %d", len(raw), projectAssistantWorkflowMaxResultBytes)
	}
	var verification struct {
		Status string `json:"status"`
		Checks []struct {
			Name    string   `json:"name"`
			Command []string `json:"command"`
			ID      string   `json:"id"`
			Status  string   `json:"status"`
		} `json:"checks"`
		Trace []workflowTraceEntry `json:"trace"`
	}
	if err := json.Unmarshal([]byte(raw), &verification); err != nil {
		t.Fatalf("workflow result is not JSON: %v\n%s", err, raw)
	}
	if verification.Status != "started" {
		t.Fatalf("status = %q, want started", verification.Status)
	}
	if len(verification.Checks) != 2 {
		t.Fatalf("checks = %#v, want build and test", verification.Checks)
	}
	if strings.Join(verification.Checks[0].Command, " ") != "npm run build" || strings.Join(verification.Checks[1].Command, " ") != "npm test" {
		t.Fatalf("checks = %#v, want preset npm build/test commands", verification.Checks)
	}
	if len(worker.requests) != 2 {
		t.Fatalf("worker requests = %#v, want 2", worker.requests)
	}
	if strings.Join(worker.requests[0].Command, " ") != "npm run build" || strings.Join(worker.requests[1].Command, " ") != "npm test" {
		t.Fatalf("worker requests = %#v, want preset npm build/test commands", worker.requests)
	}
	if worker.requests[0].TimeoutSeconds != 30 || worker.requests[1].TimeoutSeconds != 30 {
		t.Fatalf("worker requests = %#v, want requested timeout", worker.requests)
	}
	if !workflowTraceContains(verification.Trace, "start-runtime-checks") {
		t.Fatalf("trace = %#v, want start-runtime-checks node", verification.Trace)
	}
}

func TestProjectAssistantRuntimeVerificationWorkflowRejectsUnknownChecks(t *testing.T) {
	server := NewWithWorkspace(nil, store.NewMemoryStore(), workspace.NewFileStore(t.TempDir()), "", false)
	worker := &recordingProjectRuntimeWorker{handles: []projectRuntimeHandle{{ID: "runtime-1"}}}
	server.runtimeWorker = worker
	tool, ok := server.projectAssistantToolRegistry().Get(projectToolVerifyProjectRuntime)
	if !ok {
		t.Fatal("verify_project_runtime tool missing with runtime worker")
	}

	_, err := tool.Call(context.Background(), projectAssistantToolCallRequest{
		Arguments: map[string]any{"checks": []any{"deploy"}},
	})
	if err == nil || !strings.Contains(err.Error(), "unsupported runtime verification check") {
		t.Fatalf("Call error = %v, want unsupported check validation", err)
	}
	if len(worker.requests) != 0 {
		t.Fatalf("worker requests = %#v, want no runtime starts", worker.requests)
	}
}

func TestProjectAssistantRuntimeVerificationWorkflowRejectsMissingOrMalformedChecks(t *testing.T) {
	tests := []struct {
		name string
		args map[string]any
		want string
	}{
		{
			name: "missing",
			args: map[string]any{},
			want: "requires at least one check",
		},
		{
			name: "empty",
			args: map[string]any{"checks": []any{}},
			want: "requires at least one check",
		},
		{
			name: "string",
			args: map[string]any{"checks": "test"},
			want: "checks must be an array",
		},
		{
			name: "non string",
			args: map[string]any{"checks": []any{"build", float64(1)}},
			want: "check 1 must be a string",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := NewWithWorkspace(nil, store.NewMemoryStore(), workspace.NewFileStore(t.TempDir()), "", false)
			worker := &recordingProjectRuntimeWorker{handles: []projectRuntimeHandle{{ID: "runtime-1"}}}
			server.runtimeWorker = worker
			tool, ok := server.projectAssistantToolRegistry().Get(projectToolVerifyProjectRuntime)
			if !ok {
				t.Fatal("verify_project_runtime tool missing with runtime worker")
			}

			_, err := tool.Call(context.Background(), projectAssistantToolCallRequest{Arguments: tt.args})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Call error = %v, want %q", err, tt.want)
			}
			if len(worker.requests) != 0 {
				t.Fatalf("worker requests = %#v, want no runtime starts", worker.requests)
			}
		})
	}
}

func TestProjectAssistantRuntimeVerificationWorkflowReturnsPartialResultWhenStartFails(t *testing.T) {
	server := NewWithWorkspace(nil, store.NewMemoryStore(), workspace.NewFileStore(t.TempDir()), "", false)
	longWorkerError := "worker capacity exhausted " + strings.Repeat("x", projectAssistantWorkflowMaxResultBytes)
	worker := &recordingProjectRuntimeWorker{
		handles: []projectRuntimeHandle{{ID: "runtime-build"}},
		errors:  []error{nil, errors.New(longWorkerError)},
	}
	server.runtimeWorker = worker
	project := projectWithRepository("demo-repo", "demo", "github")
	project.Name = "demo"
	id := identity{tenantPath: "root:org-a:ws-1", orgUUID: "org-a", workspaceUUID: "ws-1"}
	scope := projectWorkspaceScope(id, project.Name)
	tool, ok := server.projectAssistantToolRegistry().Get(projectToolVerifyProjectRuntime)
	if !ok {
		t.Fatal("verify_project_runtime tool missing with runtime worker")
	}

	raw, err := tool.Call(context.Background(), projectAssistantToolCallRequest{
		Identity:       id,
		Project:        project,
		WorkspaceScope: scope,
		Arguments: map[string]any{
			"checks": []any{"build", "test"},
		},
	})
	if err != nil {
		t.Fatalf("Call returned error: %v", err)
	}
	if len(raw) > projectAssistantWorkflowMaxResultBytes {
		t.Fatalf("workflow result length = %d, want <= %d", len(raw), projectAssistantWorkflowMaxResultBytes)
	}
	var verification struct {
		Status string `json:"status"`
		Checks []struct {
			Name    string `json:"name"`
			ID      string `json:"id"`
			Status  string `json:"status"`
			Message string `json:"message"`
		} `json:"checks"`
	}
	if err := json.Unmarshal([]byte(raw), &verification); err != nil {
		t.Fatalf("workflow result is not JSON: %v\n%s", err, raw)
	}
	if verification.Status != "failed" {
		t.Fatalf("status = %q, want failed", verification.Status)
	}
	if len(verification.Checks) != 2 {
		t.Fatalf("checks = %#v, want started build and failed test", verification.Checks)
	}
	if verification.Checks[0].Name != "build" || verification.Checks[0].ID != "runtime-build" || verification.Checks[0].Status != "started" {
		t.Fatalf("first check = %#v, want started build handle", verification.Checks[0])
	}
	if verification.Checks[1].Name != "test" || verification.Checks[1].Status != "failed" || !strings.Contains(verification.Checks[1].Message, "worker capacity exhausted") {
		t.Fatalf("second check = %#v, want failed test with worker error", verification.Checks[1])
	}
	if len(verification.Checks[1].Message) > 260 {
		t.Fatalf("worker error message length = %d, want bounded message", len(verification.Checks[1].Message))
	}
}

func workflowTestFilePaths(files []workspace.FileInfo) []string {
	out := make([]string, 0, len(files))
	for _, file := range files {
		out = append(out, file.Path)
	}
	return out
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

type workflowTraceEntry struct {
	Node     string `json:"node"`
	Status   string `json:"status"`
	Duration int64  `json:"durationMs"`
}

func workflowTraceContains(values []workflowTraceEntry, node string) bool {
	for _, value := range values {
		if value.Node == node && value.Status == "ok" {
			return true
		}
	}
	return false
}

type recordingProjectRuntimeWorker struct {
	handles  []projectRuntimeHandle
	errors   []error
	requests []projectRuntimeRequest
}

func (w *recordingProjectRuntimeWorker) Start(_ context.Context, req projectRuntimeRequest) (projectRuntimeHandle, error) {
	w.requests = append(w.requests, req)
	if len(w.errors) > 0 {
		err := w.errors[0]
		w.errors = w.errors[1:]
		if err != nil {
			return projectRuntimeHandle{}, err
		}
	}
	if len(w.handles) == 0 {
		return projectRuntimeHandle{}, nil
	}
	handle := w.handles[0]
	w.handles = w.handles[1:]
	return handle, nil
}
