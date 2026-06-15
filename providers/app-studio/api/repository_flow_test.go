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
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"

	aiv1alpha1 "github.com/faroshq/provider-app-studio/apis/ai/v1alpha1"
	asclient "github.com/faroshq/provider-app-studio/client"
	"github.com/faroshq/provider-app-studio/store"
	"github.com/faroshq/provider-app-studio/workspace"
)

func TestParseProjectNamingResult(t *testing.T) {
	got, err := parseProjectNamingResult("```json\n{\"displayName\":\"Invoice Desk\",\"repositoryName\":\"invoice-desk\"}\n```")
	if err != nil {
		t.Fatalf("parseProjectNamingResult returned error: %v", err)
	}
	if got.DisplayName != "Invoice Desk" {
		t.Fatalf("DisplayName = %q, want Invoice Desk", got.DisplayName)
	}
	if got.RepositoryName != "invoice-desk" {
		t.Fatalf("RepositoryName = %q, want invoice-desk", got.RepositoryName)
	}
}

func TestDNS1123LabelWithSuffix(t *testing.T) {
	base := strings.Repeat("a", 80)
	got := dns1123LabelWithSuffix(base, "ABC123")
	if len(got) > 63 {
		t.Fatalf("label length = %d, want <= 63", len(got))
	}
	if !strings.HasSuffix(got, "-abc123") {
		t.Fatalf("label = %q, want suffix -abc123", got)
	}
}

func TestProjectToolAllowlistSeparatesWorkspaceAndGitTools(t *testing.T) {
	if projectMCPToolAllowed("code__commit_files") {
		t.Fatal("code__commit_files should not be directly model-callable")
	}
	if !projectMCPCommitToolAvailable("code__commit_files") {
		t.Fatal("code__commit_files should be discoverable as the internal commit bridge target")
	}
	if projectMCPCommitToolAvailable("other__commit_files") {
		t.Fatal("commit bridge should only be detected from the Code provider")
	}
	for _, name := range []string{
		"code__commit_files",
		"code__list_repository_files",
		"code__read_repository_file",
		"code__search_repository_files",
		"code__get_repository_commit",
		"code__write_file",
		"code__apply_patch",
		"code__mkdir",
		"code__commit_project_files",
	} {
		if projectMCPToolAllowed(name) {
			t.Fatalf("%s should not be allowed; project file inspection belongs to App Studio workspace tools", name)
		}
	}
	for _, name := range []string{
		"list_project_files",
		"read_project_file",
		"search_project_files",
		"write_file",
		"apply_patch",
		"mkdir",
		"commit_project_files",
	} {
		if !projectLocalToolAllowed(name) {
			t.Fatalf("%s should be allowed as an App Studio workspace-local tool", name)
		}
	}
	if projectMCPToolAllowed("code__delete_repository") {
		t.Fatal("delete_repository should not be allowed from App Studio")
	}
}

func TestLoadProjectMCPToolsExposesCommitBridgeOnly(t *testing.T) {
	mcp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var envelope struct {
			Method string `json:"method"`
		}
		if err := json.NewDecoder(r.Body).Decode(&envelope); err != nil {
			t.Fatalf("decode MCP request: %v", err)
		}
		if envelope.Method != "tools/list" {
			t.Fatalf("method = %q, want tools/list", envelope.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"result":{"tools":[{"name":"code__commit_files","description":"Commit files","inputSchema":{"type":"object"}},{"name":"code__read_repository_file","description":"Read files","inputSchema":{"type":"object"}}]}}`)
	}))
	defer mcp.Close()

	server := NewWithWorkspace(nil, nil, workspace.NewFileStore(t.TempDir()), mcp.URL, false)
	tools, err := server.loadProjectMCPTools(
		httptest.NewRequest(http.MethodPost, "/", nil),
		identity{tenantPath: "root:org-a:ws-1"},
		projectLLMSettings{},
	)
	if err != nil {
		t.Fatalf("loadProjectMCPTools returned error: %v", err)
	}
	names := map[string]bool{}
	for _, tool := range tools {
		names[tool.Function.Name] = true
	}
	if !names["commit_project_files"] {
		t.Fatalf("tool names = %#v, want commit_project_files", names)
	}
	if names["code__commit_files"] || names["code__read_repository_file"] {
		t.Fatalf("tool names = %#v, should not expose raw provider-code tools", names)
	}
}

func TestProjectSystemPromptRequiresWorkspaceInspectBeforeEdit(t *testing.T) {
	project := projectWithRepository("demo-repo", "demo", "github")
	project.Name = "demo-project"
	project.Spec.DisplayName = "Demo Project"
	repository := &ProjectRepositoryView{
		Ref:    "demo-repo",
		Name:   "demo",
		Status: projectRepositoryStatusReady,
		Ready:  true,
	}

	prompt := projectSystemPrompt(project, repository)
	for _, want := range []string{"list_project_files", "read_project_file", "search_project_files", "write_file", "apply_patch", "mkdir", "commit_project_files"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
	}
	for _, unwanted := range []string{"list_repository_files", "read_repository_file", "search_repository_files", "code__write_file", "code__apply_patch"} {
		if strings.Contains(prompt, unwanted) {
			t.Fatalf("prompt should not direct file inspection through provider-code tool %q:\n%s", unwanted, prompt)
		}
	}
	if !strings.Contains(prompt, "provider-code only as the git-source boundary") {
		t.Fatalf("prompt missing provider-code boundary guidance:\n%s", prompt)
	}
	if !strings.Contains(strings.ToLower(prompt), "before") || !strings.Contains(strings.ToLower(prompt), "inspect") {
		t.Fatalf("prompt does not require inspect-before-edit guidance:\n%s", prompt)
	}
}

func TestProjectRuntimeViewDefaultsToNotConfigured(t *testing.T) {
	project := projectWithRepository("demo-repo", "demo", "github")

	view := projectView(context.Background(), nil, project)

	if view.Runtime == nil {
		t.Fatal("projectView Runtime is nil, want not-configured runtime view")
	}
	if view.Runtime.Status != projectRuntimeStatusNotConfigured {
		t.Fatalf("runtime status = %q, want %q", view.Runtime.Status, projectRuntimeStatusNotConfigured)
	}
	if view.Runtime.Ready {
		t.Fatal("runtime ready = true, want false for unconfigured runtime")
	}
	if !strings.Contains(view.Runtime.Message, "No runtime provider") {
		t.Fatalf("runtime message = %q, want missing runtime provider guidance", view.Runtime.Message)
	}
}

func TestProjectRuntimeViewUsesPluggableBinding(t *testing.T) {
	project := projectWithRepository("demo-repo", "demo", "github")
	project.Spec.Runtime = &aiv1alpha1.ProjectRuntimeBinding{
		ProviderRef: "runtime-kubernetes",
		Target:      "cloud-run",
		RuntimeRef:  "demo-runtime",
	}

	view := projectView(context.Background(), nil, project)

	if view.Runtime == nil {
		t.Fatal("projectView Runtime is nil, want runtime binding view")
	}
	if view.Runtime.ProviderRef != "runtime-kubernetes" {
		t.Fatalf("providerRef = %q, want runtime-kubernetes", view.Runtime.ProviderRef)
	}
	if view.Runtime.Target != "cloud-run" {
		t.Fatalf("target = %q, want cloud-run", view.Runtime.Target)
	}
	if view.Runtime.RuntimeRef != "demo-runtime" {
		t.Fatalf("runtimeRef = %q, want demo-runtime", view.Runtime.RuntimeRef)
	}
	if view.Runtime.Status != projectRuntimeStatusPending {
		t.Fatalf("runtime status = %q, want %q", view.Runtime.Status, projectRuntimeStatusPending)
	}
	if view.Runtime.Ready {
		t.Fatal("runtime ready = true, want false until runtime provider reports readiness")
	}
	if !strings.Contains(view.Runtime.Message, "runtime provider status is not available") {
		t.Fatalf("runtime message = %q, want provider status guidance", view.Runtime.Message)
	}
}

func TestProjectRuntimeViewUsesObservedStatus(t *testing.T) {
	project := projectWithRepository("demo-repo", "demo", "github")
	project.Spec.Runtime = &aiv1alpha1.ProjectRuntimeBinding{
		ProviderRef: "runtime-kubernetes",
		Target:      "kubernetes",
		RuntimeRef:  "demo-runtime",
	}
	project.Status.Runtime = &aiv1alpha1.ProjectRuntimeStatus{
		Phase:        "Ready",
		Message:      "Preview is serving",
		PreviewURL:   "https://preview.example.test/app",
		Ready:        true,
		Capabilities: []string{"logs", "preview"},
	}

	view := projectView(context.Background(), nil, project)

	if view.Runtime == nil {
		t.Fatal("projectView Runtime is nil, want runtime status view")
	}
	if view.Runtime.Status != "Ready" {
		t.Fatalf("runtime status = %q, want Ready", view.Runtime.Status)
	}
	if !view.Runtime.Ready {
		t.Fatal("runtime ready = false, want true")
	}
	if view.Runtime.Message != "Preview is serving" {
		t.Fatalf("runtime message = %q, want provider status message", view.Runtime.Message)
	}
	if view.Runtime.PreviewURL != "https://preview.example.test/app" {
		t.Fatalf("previewURL = %q, want safe preview URL", view.Runtime.PreviewURL)
	}
	if strings.Join(view.Runtime.Capabilities, ",") != "logs,preview" {
		t.Fatalf("capabilities = %#v, want logs and preview", view.Runtime.Capabilities)
	}
}

func TestProjectRuntimeViewDropsUnsafePreviewURL(t *testing.T) {
	project := projectWithRepository("demo-repo", "demo", "github")
	project.Spec.Runtime = &aiv1alpha1.ProjectRuntimeBinding{ProviderRef: "runtime-kubernetes"}
	project.Status.Runtime = &aiv1alpha1.ProjectRuntimeStatus{
		Phase:      "Ready",
		Ready:      true,
		PreviewURL: "javascript:alert(1)",
	}

	view := projectView(context.Background(), nil, project)

	if view.Runtime == nil {
		t.Fatal("projectView Runtime is nil, want runtime status view")
	}
	if view.Runtime.PreviewURL != "" {
		t.Fatalf("previewURL = %q, want unsafe URL stripped", view.Runtime.PreviewURL)
	}
}

func TestProjectStatusTouchPatchPatchesAppStudioFieldsOnly(t *testing.T) {
	updatedAt := metav1.NewTime(time.Date(2026, 6, 14, 20, 0, 0, 0, time.UTC))
	data, err := projectStatusTouchPatch(updatedAt)
	if err != nil {
		t.Fatalf("projectStatusTouchPatch returned error: %v", err)
	}
	if strings.Contains(string(data), "runtime") {
		t.Fatalf("status patch includes runtime and could overwrite provider-owned status: %s", string(data))
	}
	var decoded struct {
		Status struct {
			Phase     string      `json:"phase"`
			UpdatedAt metav1.Time `json:"updatedAt"`
		} `json:"status"`
	}
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal status patch: %v", err)
	}
	if decoded.Status.Phase != aiv1alpha1.ProjectPhaseReady {
		t.Fatalf("phase = %q, want Ready", decoded.Status.Phase)
	}
	if !decoded.Status.UpdatedAt.Equal(&updatedAt) {
		t.Fatalf("updatedAt = %s, want %s", decoded.Status.UpdatedAt, updatedAt)
	}
}

func TestProjectSystemPromptDescribesPluggableRuntimeBoundary(t *testing.T) {
	project := projectWithRepository("demo-repo", "demo", "github")
	project.Spec.Runtime = &aiv1alpha1.ProjectRuntimeBinding{
		ProviderRef: "runtime-kubernetes",
		Target:      "kubernetes",
		RuntimeRef:  "demo-runtime",
	}
	repository := &ProjectRepositoryView{
		Ref:    "demo-repo",
		Name:   "demo",
		Status: projectRepositoryStatusReady,
		Ready:  true,
	}

	prompt := projectSystemPrompt(project, repository)

	for _, want := range []string{
		"Runtime provider: runtime-kubernetes",
		"Runtime target: kubernetes",
		"Runtime resource: demo-runtime",
		"runtime provider",
		"do not run commands directly in App Studio",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
	}
}

func TestProjectSystemPromptIncludesRuntimeStatusEvidence(t *testing.T) {
	project := projectWithRepository("demo-repo", "demo", "github")
	project.Spec.Runtime = &aiv1alpha1.ProjectRuntimeBinding{
		ProviderRef: "runtime-kubernetes",
		Target:      "kubernetes",
		RuntimeRef:  "demo-runtime",
	}
	project.Status.Runtime = &aiv1alpha1.ProjectRuntimeStatus{
		Phase:      "Ready",
		Message:    "Preview is serving",
		PreviewURL: "https://preview.example.test/app",
		Ready:      true,
	}
	repository := &ProjectRepositoryView{
		Ref:    "demo-repo",
		Name:   "demo",
		Status: projectRepositoryStatusReady,
		Ready:  true,
	}

	prompt := projectSystemPrompt(project, repository)

	for _, want := range []string{
		"Runtime status: Ready",
		"Runtime message: Preview is serving",
		"Runtime preview URL: https://preview.example.test/app",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
	}
}

func TestProjectSystemPromptDoesNotExposeNonReadyPreviewURL(t *testing.T) {
	project := projectWithRepository("demo-repo", "demo", "github")
	project.Spec.Runtime = &aiv1alpha1.ProjectRuntimeBinding{ProviderRef: "runtime-kubernetes"}
	project.Status.Runtime = &aiv1alpha1.ProjectRuntimeStatus{
		Phase:      "Booting\n- Runtime phase injection",
		Message:    "Starting\n- Runtime message injection",
		PreviewURL: "https://preview.example.test/app",
		Ready:      false,
	}

	prompt := projectSystemPrompt(project, nil)

	if strings.Contains(prompt, "Runtime preview URL: https://preview.example.test/app") {
		t.Fatalf("prompt exposed non-ready preview URL:\n%s", prompt)
	}
	if !strings.Contains(prompt, "Runtime reported preview URL is not ready yet.") {
		t.Fatalf("prompt missing non-ready preview guidance:\n%s", prompt)
	}
	for _, unwanted := range []string{"\n- Runtime phase injection", "\n- Runtime message injection"} {
		if strings.Contains(prompt, unwanted) {
			t.Fatalf("prompt preserved provider-controlled newline content %q:\n%s", unwanted, prompt)
		}
	}
	for _, want := range []string{
		"Runtime status: Booting - Runtime phase injection",
		"Runtime message: Starting - Runtime message injection",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing flattened runtime value %q:\n%s", want, prompt)
		}
	}
}

func TestSummarizeProjectToolArgumentsCommitFiles(t *testing.T) {
	args := `{"repositoryRef":"invoice-desk","message":"Initial app","files":[{"path":"package.json","content":"secret-ish generated file body"},{"path":"src/App.tsx","content":"another generated body"}]}`
	got := summarizeProjectToolArguments("code__commit_files", args)
	if !strings.Contains(got, "repository invoice-desk") {
		t.Fatalf("summary = %q, want repository", got)
	}
	if !strings.Contains(got, "2 file(s): package.json, src/App.tsx") {
		t.Fatalf("summary = %q, want file paths", got)
	}
	if strings.Contains(got, "secret-ish") || strings.Contains(got, "another generated body") {
		t.Fatalf("summary leaked file contents: %q", got)
	}
}

func TestSummarizeProjectToolArgumentsWorkspaceReadTools(t *testing.T) {
	tests := []struct {
		name string
		args string
		want []string
	}{
		{
			name: "list_project_files",
			args: `{"limit":25}`,
			want: []string{"limit 25"},
		},
		{
			name: "read_project_file",
			args: `{"path":"src/App.tsx","maxBytes":4096}`,
			want: []string{"path src/App.tsx", "maxBytes 4096"},
		},
		{
			name: "search_project_files",
			args: `{"query":"secret-ish user query","maxResults":10}`,
			want: []string{"query secret-ish user query", "maxResults 10"},
		},
		{
			name: "write_file",
			args: `{"path":"src/App.tsx","content":"secret-ish file body"}`,
			want: []string{"path src/App.tsx", "20 bytes"},
		},
		{
			name: "apply_patch",
			args: `{"path":"src/App.tsx","oldText":"secret-ish old","newText":"secret-ish new","replaceAll":true}`,
			want: []string{"path src/App.tsx", "replaceAll"},
		},
		{
			name: "mkdir",
			args: `{"path":"src/components"}`,
			want: []string{"path src/components"},
		},
		{
			name: "commit_project_files",
			args: `{"repositoryRef":"demo","message":"Update app","paths":["src/App.tsx"]}`,
			want: []string{"repository demo", "message Update app", "1 file(s): src/App.tsx"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := summarizeProjectToolArguments(tt.name, tt.args)
			for _, want := range tt.want {
				if !strings.Contains(got, want) {
					t.Fatalf("summary = %q, want %q", got, want)
				}
			}
			if (tt.name == "write_file" || tt.name == "apply_patch") && strings.Contains(got, "secret-ish") {
				t.Fatalf("summary leaked content: %q", got)
			}
		})
	}
}

func TestSummarizeProjectToolResultWorkspaceReadTools(t *testing.T) {
	readResult := `{"path":"src/App.tsx","size":2048,"content":"secret-ish file body","truncated":true,"binary":false}`
	got := summarizeProjectToolResult("read_project_file", readResult)
	for _, want := range []string{"file src/App.tsx", "2048 bytes", "truncated"} {
		if !strings.Contains(got, want) {
			t.Fatalf("summary = %q, want %q", got, want)
		}
	}
	if strings.Contains(got, "secret-ish") {
		t.Fatalf("summary leaked file contents: %q", got)
	}

	searchResult := `{"totalCount":12,"truncated":true,"results":[{"path":"src/App.tsx"},{"path":"src/main.ts"},{"path":"README.md"}]}`
	got = summarizeProjectToolResult("search_project_files", searchResult)
	for _, want := range []string{"12 match(es)", "src/App.tsx, src/main.ts, README.md", "truncated"} {
		if !strings.Contains(got, want) {
			t.Fatalf("summary = %q, want %q", got, want)
		}
	}

	mutationResult := `{"operation":"apply_patch","path":"src/App.tsx","size":128,"replacements":2,"content":"secret-ish body"}`
	got = summarizeProjectToolResult("apply_patch", mutationResult)
	for _, want := range []string{"apply_patch", "src/App.tsx", "128 bytes", "2 replacement(s)"} {
		if !strings.Contains(got, want) {
			t.Fatalf("summary = %q, want %q", got, want)
		}
	}
	if strings.Contains(got, "secret-ish") {
		t.Fatalf("summary leaked content: %q", got)
	}
}

func TestProjectAssistantMessageMetadataToolCalls(t *testing.T) {
	events := []projectToolCallStreamEvent{
		{ID: "call-1", Name: "code__commit_files", Status: "running"},
		{ID: "call-1", Status: "succeeded", Summary: "commit abc123"},
	}
	merged := upsertProjectToolCallStreamEvent(events[:1], events[1])
	metadata := projectAssistantMessageMetadata("", merged)
	raw, ok := metadata[projectMessageMetadataToolCalls].([]projectToolCallStreamEvent)
	if !ok {
		t.Fatalf("tool call metadata type = %T, want []projectToolCallStreamEvent", metadata[projectMessageMetadataToolCalls])
	}
	if len(raw) != 1 {
		t.Fatalf("tool call metadata length = %d, want 1", len(raw))
	}
	if raw[0].Name != "code__commit_files" || raw[0].Status != "succeeded" || raw[0].Summary != "commit abc123" {
		t.Fatalf("unexpected merged tool call metadata: %#v", raw[0])
	}
}

func TestProjectToolCallResultStatusCommitFilesPending(t *testing.T) {
	result := `{"name":"demo-commit","phase":"Pending","files":["index.html"]}`
	if got := projectToolCallResultStatus("code__commit_files", result); got != "running" {
		t.Fatalf("status = %q, want running", got)
	}
	if got := projectToolCallResultStatus("code__commit_files", `{"phase":"Succeeded"}`); got != "succeeded" {
		t.Fatalf("status = %q, want succeeded", got)
	}
	if got := projectToolCallResultStatus("other_tool", result); got != "succeeded" {
		t.Fatalf("non-commit status = %q, want succeeded", got)
	}
}

func TestCallProjectMCPToolTreatsIsErrorAsFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"create RepositoryCommit: the server could not find the requested resource"}],"isError":true}}`)
	}))
	defer server.Close()

	req := httptest.NewRequest(http.MethodPost, "/", nil)
	resp, err := callProjectMCPTool(
		context.Background(),
		server.URL,
		req,
		"root:example:default",
		false,
		"code__commit_files",
		map[string]any{"repositoryRef": "demo"},
	)
	if err == nil {
		t.Fatalf("callProjectMCPTool returned nil error, response %q", resp)
	}
	if !strings.Contains(err.Error(), "create RepositoryCommit") {
		t.Fatalf("error = %q, want RepositoryCommit failure text", err.Error())
	}
}

func TestResolveProjectToolCallsRunsLocalWorkspaceTools(t *testing.T) {
	workspaces := workspace.NewFileStore(t.TempDir())
	scope := workspace.Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: "demo"}
	if err := workspaces.ApplyFiles(context.Background(), scope, []workspace.File{
		{Path: "README.md", Content: "hello from App Studio workspace\n"},
	}); err != nil {
		t.Fatalf("ApplyFiles returned error: %v", err)
	}
	server := NewWithWorkspace(nil, nil, workspaces, "", false)

	messages, err := server.resolveProjectToolCalls(
		context.Background(),
		identity{},
		scope,
		[]chatToolCall{{
			ID:   "call-1",
			Type: "function",
			Function: chatToolCallFunction{
				Name:      "read_project_file",
				Arguments: `{"path":"README.md"}`,
			},
		}},
		httptest.NewRequest(http.MethodPost, "/", nil),
		nil,
	)
	if err != nil {
		t.Fatalf("resolveProjectToolCalls returned error: %v", err)
	}
	if len(messages) != 1 || !strings.Contains(messages[0].Content, "hello from App Studio workspace") {
		t.Fatalf("tool messages = %#v, want workspace file content", messages)
	}
}

func TestResolveProjectToolCallsRunsLocalWorkspaceMutationTools(t *testing.T) {
	workspaces := workspace.NewFileStore(t.TempDir())
	scope := workspace.Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: "demo"}
	server := NewWithWorkspace(nil, nil, workspaces, "", false)

	messages, err := server.resolveProjectToolCalls(
		context.Background(),
		identity{},
		scope,
		[]chatToolCall{
			{
				ID:   "call-1",
				Type: "function",
				Function: chatToolCallFunction{
					Name:      "mkdir",
					Arguments: `{"path":"src"}`,
				},
			},
			{
				ID:   "call-2",
				Type: "function",
				Function: chatToolCallFunction{
					Name:      "write_file",
					Arguments: `{"path":"src/App.tsx","content":"hello world\n"}`,
				},
			},
			{
				ID:   "call-3",
				Type: "function",
				Function: chatToolCallFunction{
					Name:      "apply_patch",
					Arguments: `{"path":"src/App.tsx","oldText":"world","newText":"Kedge"}`,
				},
			},
		},
		httptest.NewRequest(http.MethodPost, "/", nil),
		nil,
	)
	if err != nil {
		t.Fatalf("resolveProjectToolCalls returned error: %v", err)
	}
	if len(messages) != 3 {
		t.Fatalf("tool message count = %d, want 3", len(messages))
	}
	read, err := workspaces.ReadFile(context.Background(), scope, workspace.ReadOptions{Path: "src/App.tsx"})
	if err != nil {
		t.Fatalf("ReadFile returned error: %v", err)
	}
	if read.Content != "hello Kedge\n" {
		t.Fatalf("workspace content = %q", read.Content)
	}
}

func TestGenerateProjectAssistantStreamStopsOfferingToolsAfterRepeatedToolLoop(t *testing.T) {
	reply, requests, err := runRepeatedWriteFileAssistantStream(t, func(w http.ResponseWriter) {
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"I updated src/App.tsx and stopped before repeating the same action.\"}}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	})
	if err != nil {
		t.Fatalf("generateProjectAssistantStream returned error: %v", err)
	}
	if !strings.Contains(reply, "I updated src/App.tsx") {
		t.Fatalf("reply = %q, want final text answer", reply)
	}
	if len(requests) != 3 {
		t.Fatalf("LLM request count = %d, want 3", len(requests))
	}
	if len(requests[2].Tools) != 0 || requests[2].ToolChoice != "" {
		t.Fatalf("final request offered tools: tools=%d tool_choice=%q", len(requests[2].Tools), requests[2].ToolChoice)
	}
}

func TestGenerateProjectAssistantStreamFallsBackWhenFinalNoToolResponseIsEmpty(t *testing.T) {
	reply, requests, err := runRepeatedWriteFileAssistantStream(t, func(w http.ResponseWriter) {
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{}}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	})
	if err != nil {
		t.Fatalf("generateProjectAssistantStream returned error: %v", err)
	}
	if !strings.Contains(reply, "repeated the same action") || !strings.Contains(reply, "write_file") || !strings.Contains(reply, "src/App.tsx") {
		t.Fatalf("reply = %q, want repeated action fallback", reply)
	}
	if len(requests) != 3 {
		t.Fatalf("LLM request count = %d, want 3", len(requests))
	}
	if len(requests[2].Tools) != 0 || requests[2].ToolChoice != "" {
		t.Fatalf("final request offered tools: tools=%d tool_choice=%q", len(requests[2].Tools), requests[2].ToolChoice)
	}
}

func TestGenerateProjectAssistantStreamFallsBackWhenFinalToolLimitResponseHasOnlyToolCalls(t *testing.T) {
	reply, requests, err := runUniqueWriteFileAssistantStream(t, func(w http.ResponseWriter) {
		writeProjectTestToolCallChunk(t, w, "call-final", "write_file", `{"path":"src/final.tsx","content":"final\n"}`)
	})
	if err != nil {
		t.Fatalf("generateProjectAssistantStream returned error: %v", err)
	}
	if !strings.Contains(reply, "kept requesting actions") || !strings.Contains(reply, "write_file") || !strings.Contains(reply, "src/file-7.tsx") {
		t.Fatalf("reply = %q, want tool-limit fallback", reply)
	}
	if len(requests) != maxAssistantToolTurns {
		t.Fatalf("LLM request count = %d, want %d", len(requests), maxAssistantToolTurns)
	}
	if len(requests[len(requests)-1].Tools) != 0 || requests[len(requests)-1].ToolChoice != "" {
		t.Fatalf("final request offered tools: tools=%d tool_choice=%q", len(requests[len(requests)-1].Tools), requests[len(requests)-1].ToolChoice)
	}
}

func TestProjectPromptMessagesCollapsesConsecutiveDuplicateUserMessages(t *testing.T) {
	project := projectWithRepository("demo-repo", "demo", "github")
	history := []store.Message{
		{Role: aiv1alpha1.ProjectMessageRoleUser, Content: "Make an app"},
		{Role: aiv1alpha1.ProjectMessageRoleUser, Content: "Make an app"},
		{Role: aiv1alpha1.ProjectMessageRoleUser, Content: " Make an app "},
		{Role: aiv1alpha1.ProjectMessageRoleAssistant, Content: "I inspected the workspace."},
		{Role: aiv1alpha1.ProjectMessageRoleUser, Content: "Make an app"},
	}

	messages := projectPromptMessages(project, nil, history)

	var userMessages []string
	for _, msg := range messages {
		if msg.Role == aiv1alpha1.ProjectMessageRoleUser {
			userMessages = append(userMessages, msg.Content)
		}
	}
	if len(userMessages) != 2 {
		t.Fatalf("user prompt count = %d, want 2: %#v", len(userMessages), userMessages)
	}
	for _, got := range userMessages {
		if got != "Make an app" {
			t.Fatalf("user prompt = %q, want normalized prompt", got)
		}
	}
}

func TestGenerateProjectAssistantStreamReturnsCommitProjectFilesResult(t *testing.T) {
	var llmRequests []chatCompletionRequest
	llm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req chatCompletionRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode LLM request: %v", err)
		}
		llmRequests = append(llmRequests, req)
		w.Header().Set("Content-Type", "text/event-stream")
		switch len(llmRequests) {
		case 1:
			writeProjectTestToolCallChunk(t, w, "call-write", "write_file", `{"path":"index.html","content":"hello\n"}`)
		case 2:
			writeProjectTestToolCallChunk(t, w, "call-commit", "commit_project_files", `{"repositoryRef":"demo-repo","paths":["index.html"],"message":"Initial app"}`)
		default:
			t.Fatalf("unexpected LLM request after commit handoff: tools=%d tool_choice=%q", len(req.Tools), req.ToolChoice)
		}
	}))
	defer llm.Close()

	var commitCalls int
	mcp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var envelope struct {
			Method string `json:"method"`
			Params struct {
				Name string `json:"name"`
			} `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&envelope); err != nil {
			t.Fatalf("decode MCP request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		switch envelope.Method {
		case "tools/list":
			fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"result":{"tools":[{"name":"code__commit_files","description":"commit files"}]}}`)
		case "tools/call":
			commitCalls++
			if envelope.Params.Name != "code__commit_files" {
				t.Fatalf("unexpected MCP tool call: %#v", envelope)
			}
			fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"result":{"structuredContent":{"phase":"Succeeded","files":["index.html"],"commitSHA":"abcdef1234567890"}}}`)
		default:
			t.Fatalf("unexpected MCP request method %q", envelope.Method)
		}
	}))
	defer mcp.Close()

	reply, requests, err := runProjectAssistantStreamWithLLMAndHub(t, llm.URL, mcp.URL, &llmRequests)
	if err != nil {
		t.Fatalf("generateProjectAssistantStream returned error: %v", err)
	}
	if !strings.Contains(reply, "Committed the workspace files") || !strings.Contains(reply, "commit abcdef123456") {
		t.Fatalf("reply = %q, want deterministic commit success", reply)
	}
	if commitCalls != 1 {
		t.Fatalf("commit call count = %d, want 1", commitCalls)
	}
	if len(requests) != 2 {
		t.Fatalf("LLM request count = %d, want 2", len(requests))
	}
}

func TestGenerateProjectAssistantStreamReportsCommitProjectFilesFailure(t *testing.T) {
	var llmRequests []chatCompletionRequest
	llm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req chatCompletionRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode LLM request: %v", err)
		}
		llmRequests = append(llmRequests, req)
		w.Header().Set("Content-Type", "text/event-stream")
		switch len(llmRequests) {
		case 1:
			writeProjectTestToolCallChunk(t, w, "call-write", "write_file", `{"path":"index.html","content":"hello\n"}`)
		case 2:
			writeProjectTestToolCallChunk(t, w, "call-commit", "commit_project_files", `{"repositoryRef":"demo-repo","paths":["index.html"],"message":"Initial app"}`)
		default:
			t.Fatalf("unexpected LLM request after failed commit handoff: tools=%d tool_choice=%q", len(req.Tools), req.ToolChoice)
		}
	}))
	defer llm.Close()

	var commitCalls int
	mcp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var envelope struct {
			Method string `json:"method"`
			Params struct {
				Name string `json:"name"`
			} `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&envelope); err != nil {
			t.Fatalf("decode MCP request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		switch envelope.Method {
		case "tools/list":
			fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"result":{"tools":[{"name":"code__commit_files","description":"commit files"}]}}`)
		case "tools/call":
			commitCalls++
			if envelope.Params.Name != "code__commit_files" {
				t.Fatalf("unexpected MCP tool call: %#v", envelope)
			}
			fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"result":{"isError":true,"content":[{"type":"text","text":"RepositoryCommit failed: bundle not found"}]}}`)
		default:
			t.Fatalf("unexpected MCP request method %q", envelope.Method)
		}
	}))
	defer mcp.Close()

	reply, requests, err := runProjectAssistantStreamWithLLMAndHub(t, llm.URL, mcp.URL, &llmRequests)
	if err != nil {
		t.Fatalf("generateProjectAssistantStream returned error: %v", err)
	}
	if !strings.Contains(reply, "could not commit") || !strings.Contains(reply, "bundle not found") {
		t.Fatalf("reply = %q, want deterministic commit failure", reply)
	}
	if strings.Contains(reply, "Committed the workspace files") {
		t.Fatalf("reply = %q, should not claim commit success", reply)
	}
	if commitCalls != 1 {
		t.Fatalf("commit call count = %d, want 1", commitCalls)
	}
	if len(requests) != 2 {
		t.Fatalf("LLM request count = %d, want 2", len(requests))
	}
}

func TestProjectCommitToolReplyReportsRunningCommit(t *testing.T) {
	reply, ok := projectCommitToolReply([]chatMessage{{
		Role:    "tool",
		Name:    "commit_project_files",
		Content: `{"name":"demo-commit","phase":"Running","files":["index.html"]}`,
	}})
	if !ok {
		t.Fatal("projectCommitToolReply returned ok=false")
	}
	if !strings.Contains(reply, "still running") || !strings.Contains(reply, "request demo-commit") {
		t.Fatalf("reply = %q, want running commit result", reply)
	}
	if strings.Contains(reply, "Committed the workspace files") {
		t.Fatalf("reply = %q, should not claim commit success", reply)
	}
}

func TestProjectAssistantStoredContentPrefersFinalReply(t *testing.T) {
	got := projectAssistantStoredContent("Committed index.html.", "I will inspect the project files.")
	if got != "Committed index.html." {
		t.Fatalf("stored content = %q, want final reply", got)
	}
}

func TestProjectAssistantUnstreamedContentAppendsDistinctFinalReply(t *testing.T) {
	got := projectAssistantUnstreamedContent("Committed index.html.", "I will inspect the project files.")
	if got != "\n\nCommitted index.html." {
		t.Fatalf("unstreamed content = %q, want final reply chunk", got)
	}
	if duplicate := projectAssistantUnstreamedContent("Committed index.html.", "Committed index.html."); duplicate != "" {
		t.Fatalf("duplicate unstreamed content = %q, want empty", duplicate)
	}
}

func runRepeatedWriteFileAssistantStream(t *testing.T, finalNoToolResponse func(http.ResponseWriter)) (string, []chatCompletionRequest, error) {
	t.Helper()

	var requests []chatCompletionRequest
	llm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req chatCompletionRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode LLM request: %v", err)
		}
		requests = append(requests, req)
		w.Header().Set("Content-Type", "text/event-stream")
		if len(requests) > 2 && len(req.Tools) == 0 && finalNoToolResponse != nil {
			finalNoToolResponse(w)
			return
		}
		callID := fmt.Sprintf("call-%d", len(requests))
		writeProjectTestToolCallChunk(t, w, callID, "write_file", `{"path":"src/App.tsx","content":"hello world\n"}`)
	}))
	defer llm.Close()

	return runProjectAssistantStreamWithLLM(t, llm.URL, &requests)
}

func runUniqueWriteFileAssistantStream(t *testing.T, finalNoToolResponse func(http.ResponseWriter)) (string, []chatCompletionRequest, error) {
	t.Helper()

	var requests []chatCompletionRequest
	llm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req chatCompletionRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode LLM request: %v", err)
		}
		requests = append(requests, req)
		w.Header().Set("Content-Type", "text/event-stream")
		if len(req.Tools) == 0 && finalNoToolResponse != nil {
			finalNoToolResponse(w)
			return
		}
		n := len(requests)
		writeProjectTestToolCallChunk(t, w, fmt.Sprintf("call-%d", n), "write_file", fmt.Sprintf(`{"path":"src/file-%d.tsx","content":"hello %d\n"}`, n, n))
	}))
	defer llm.Close()

	return runProjectAssistantStreamWithLLM(t, llm.URL, &requests)
}

func writeProjectTestToolCallChunk(t *testing.T, w http.ResponseWriter, id, name, arguments string) {
	t.Helper()

	chunk := chatCompletionStreamResponse{
		Choices: []chatCompletionStreamChoice{{}},
	}
	chunk.Choices[0].Delta.ToolCalls = []chatStreamingCall{{
		Index: 0,
		ID:    id,
		Type:  "function",
	}}
	chunk.Choices[0].Delta.ToolCalls[0].Function.Name = name
	chunk.Choices[0].Delta.ToolCalls[0].Function.Arguments = arguments
	raw, err := json.Marshal(chunk)
	if err != nil {
		t.Fatalf("marshal LLM stream chunk: %v", err)
	}
	fmt.Fprintf(w, "data: %s\n\n", raw)
	fmt.Fprint(w, "data: [DONE]\n\n")
}

func runProjectAssistantStreamWithLLM(t *testing.T, llmURL string, requests *[]chatCompletionRequest) (string, []chatCompletionRequest, error) {
	return runProjectAssistantStreamWithLLMAndHub(t, llmURL, "", requests)
}

func runProjectAssistantStreamWithLLMAndHub(t *testing.T, llmURL string, hubBase string, requests *[]chatCompletionRequest) (string, []chatCompletionRequest, error) {
	t.Helper()

	settings := projectLLMSettings{
		Provider: defaultProjectLLMProvider,
		BaseURL:  llmURL,
		Model:    "test-model",
		APIKey:   "test-key",
	}
	client := asclient.NewFromDynamic(projectSettingsDynamicClient{secret: projectLLMSettingsSecret(settings)})
	messages := store.NewMemoryStore()
	scope := store.Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: "demo"}
	if err := appendProjectUserMessage(context.Background(), messages, scope, "write a hello app"); err != nil {
		t.Fatalf("appendProjectUserMessage returned error: %v", err)
	}
	workspaces := workspace.NewFileStore(t.TempDir())
	server := NewWithWorkspace(nil, messages, workspaces, hubBase, false)
	project := projectWithRepository("demo-repo", "demo", "github")
	project.Name = "demo"
	project.Spec.DisplayName = "Demo"

	reply, err := server.generateProjectAssistantStream(
		httptest.NewRequest(http.MethodPost, "/", nil),
		identity{tenantPath: "root:org-a:ws-1", orgUUID: "org-a", workspaceUUID: "ws-1"},
		client,
		project,
		projectAssistantStreamCallbacks{},
	)
	return reply, *requests, err
}

func TestProjectRepeatedToolLoopFallbackSummarizesLastToolResult(t *testing.T) {
	got := projectRepeatedToolLoopFallback([]chatMessage{{
		Role:    "tool",
		Name:    "write_file",
		Content: `{"operation":"write_file","path":"src/App.tsx","size":12}`,
	}})
	for _, want := range []string{"repeated the same action", "Last action result", "write_file", "src/App.tsx", "12 bytes"} {
		if !strings.Contains(got, want) {
			t.Fatalf("fallback = %q, want %q", got, want)
		}
	}
	if strings.Contains(got, "completed action") {
		t.Fatalf("fallback = %q, should not claim the action completed", got)
	}
}

type projectSettingsDynamicClient struct {
	secret *unstructured.Unstructured
}

func (c projectSettingsDynamicClient) Resource(gvr schema.GroupVersionResource) dynamic.NamespaceableResourceInterface {
	return projectSettingsDynamicResource{gvr: gvr, secret: c.secret}
}

type projectSettingsDynamicResource struct {
	dynamic.ResourceInterface
	gvr       schema.GroupVersionResource
	namespace string
	secret    *unstructured.Unstructured
}

func (r projectSettingsDynamicResource) Namespace(namespace string) dynamic.ResourceInterface {
	r.namespace = namespace
	return r
}

func (r projectSettingsDynamicResource) Get(_ context.Context, name string, _ metav1.GetOptions, _ ...string) (*unstructured.Unstructured, error) {
	if r.gvr == secretGVR && r.namespace == projectLLMSecretNamespace && name == projectLLMSecretName && r.secret != nil {
		return r.secret.DeepCopy(), nil
	}
	return nil, apierrors.NewNotFound(schema.GroupResource{Group: r.gvr.Group, Resource: r.gvr.Resource}, name)
}

func TestResolveProjectToolCallsCommitsWorkspaceFilesThroughCodeProvider(t *testing.T) {
	var sawCommit bool
	mcp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var envelope struct {
			Method string `json:"method"`
			Params struct {
				Name      string `json:"name"`
				Arguments struct {
					RepositoryRef string `json:"repositoryRef"`
					Message       string `json:"message"`
					Files         []struct {
						Path    string `json:"path"`
						Content string `json:"content"`
					} `json:"files"`
				} `json:"arguments"`
			} `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&envelope); err != nil {
			t.Fatalf("decode MCP request: %v", err)
		}
		if envelope.Method != "tools/call" || envelope.Params.Name != "code__commit_files" {
			t.Fatalf("unexpected MCP request: %#v", envelope)
		}
		if envelope.Params.Arguments.RepositoryRef != "demo-repo" || envelope.Params.Arguments.Message != "Update app" {
			t.Fatalf("unexpected commit args: %#v", envelope.Params.Arguments)
		}
		if len(envelope.Params.Arguments.Files) != 1 || envelope.Params.Arguments.Files[0].Path != "src/App.tsx" || envelope.Params.Arguments.Files[0].Content != "committed from workspace\n" {
			t.Fatalf("unexpected commit files: %#v", envelope.Params.Arguments.Files)
		}
		sawCommit = true
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"result":{"structuredContent":{"phase":"Succeeded","files":["src/App.tsx"],"commitSHA":"abcdef1234567890"}}}`)
	}))
	defer mcp.Close()

	workspaces := workspace.NewFileStore(t.TempDir())
	scope := workspace.Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: "demo"}
	if err := workspaces.ApplyFiles(context.Background(), scope, []workspace.File{
		{Path: "src/App.tsx", Content: "committed from workspace\n"},
	}); err != nil {
		t.Fatalf("ApplyFiles returned error: %v", err)
	}
	server := NewWithWorkspace(nil, nil, workspaces, mcp.URL, false)

	messages, err := server.resolveProjectToolCalls(
		context.Background(),
		identity{tenantPath: "root:org-a:ws-1", orgUUID: "org-a", workspaceUUID: "ws-1"},
		scope,
		[]chatToolCall{{
			ID:   "call-1",
			Type: "function",
			Function: chatToolCallFunction{
				Name:      "commit_project_files",
				Arguments: `{"repositoryRef":"demo-repo","message":"Update app","paths":["src/App.tsx","src//App.tsx"]}`,
			},
		}},
		httptest.NewRequest(http.MethodPost, "/", nil),
		nil,
	)
	if err != nil {
		t.Fatalf("resolveProjectToolCalls returned error: %v", err)
	}
	if !sawCommit {
		t.Fatal("MCP server did not receive commit call")
	}
	if len(messages) != 1 || !strings.Contains(messages[0].Content, "abcdef1234567890") {
		t.Fatalf("tool messages = %#v, want commit response", messages)
	}
}

func TestResolveProjectToolCallsRejectsDirectCodeCommitFiles(t *testing.T) {
	var sawMCP bool
	mcp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		sawMCP = true
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer mcp.Close()
	workspaces := workspace.NewFileStore(t.TempDir())
	scope := workspace.Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: "demo"}
	server := NewWithWorkspace(nil, nil, workspaces, mcp.URL, false)

	messages, err := server.resolveProjectToolCalls(
		context.Background(),
		identity{tenantPath: "root:org-a:ws-1", orgUUID: "org-a", workspaceUUID: "ws-1"},
		scope,
		[]chatToolCall{{
			ID:   "call-1",
			Type: "function",
			Function: chatToolCallFunction{
				Name:      "code__commit_files",
				Arguments: `{"repositoryRef":"demo","files":[{"path":"README.md","content":"committed through provider-code\n"}]}`,
			},
		}},
		httptest.NewRequest(http.MethodPost, "/", nil),
		nil,
	)
	if err != nil {
		t.Fatalf("resolveProjectToolCalls returned error: %v", err)
	}
	if sawMCP {
		t.Fatal("direct code__commit_files reached provider-code MCP endpoint")
	}
	if len(messages) != 1 || !strings.Contains(messages[0].Content, "disallowed tool name") {
		t.Fatalf("tool messages = %#v, want disallowed tool failure", messages)
	}
	if _, err := workspaces.ReadFile(context.Background(), scope, workspace.ReadOptions{Path: "README.md"}); err == nil {
		t.Fatal("ReadFile returned nil error, want no mirrored file")
	}
}

func TestCommitProjectWorkspaceFilesBoundsPayloadBeforeProviderCode(t *testing.T) {
	var sawMCP bool
	mcp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		sawMCP = true
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer mcp.Close()
	workspaces := workspace.NewFileStore(t.TempDir())
	scope := workspace.Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: "demo"}
	server := NewWithWorkspace(nil, nil, workspaces, mcp.URL, false)

	tooManyPaths := make([]any, 0, projectCommitProjectFilesMax+1)
	for i := 0; i < projectCommitProjectFilesMax+1; i++ {
		tooManyPaths = append(tooManyPaths, fmt.Sprintf("src/file-%03d.txt", i))
	}
	if _, err := server.commitProjectWorkspaceFiles(
		context.Background(),
		identity{tenantPath: "root:org-a:ws-1"},
		scope,
		mcp.URL,
		httptest.NewRequest(http.MethodPost, "/", nil),
		map[string]any{"repositoryRef": "demo", "paths": tooManyPaths},
	); err == nil || !strings.Contains(err.Error(), "too many paths") {
		t.Fatalf("too many paths error = %v, want bounded path count", err)
	}

	files := make([]workspace.File, 0, 65)
	paths := make([]any, 0, 65)
	for i := 0; i < 65; i++ {
		path := fmt.Sprintf("src/large-%02d.txt", i)
		files = append(files, workspace.File{Path: path, Content: strings.Repeat("x", workspace.MaxWriteBytes)})
		paths = append(paths, path)
	}
	if err := workspaces.ApplyFiles(context.Background(), scope, files); err != nil {
		t.Fatalf("ApplyFiles returned error: %v", err)
	}
	if _, err := server.commitProjectWorkspaceFiles(
		context.Background(),
		identity{tenantPath: "root:org-a:ws-1"},
		scope,
		mcp.URL,
		httptest.NewRequest(http.MethodPost, "/", nil),
		map[string]any{"repositoryRef": "demo", "paths": paths},
	); err == nil || !strings.Contains(err.Error(), "payload is too large") {
		t.Fatalf("payload size error = %v, want bounded aggregate size", err)
	}
	if sawMCP {
		t.Fatal("commit_project_files called provider-code after local bounds failure")
	}
}

func TestProjectStreamingTimeoutsFitLongRunningGenerations(t *testing.T) {
	if timeout := projectLLMStreamClient().Timeout; timeout != 0 {
		t.Fatalf("streaming HTTP client timeout = %s, want no whole-response timeout", timeout)
	}
	if projectMCPCallTimeout <= 75*time.Second {
		t.Fatalf("MCP call timeout = %s, want longer than commit_files reconciliation wait", projectMCPCallTimeout)
	}
}

func TestProjectRepositoryViewDegradedStates(t *testing.T) {
	project := projectWithRepository("demo-repo", "demo", "github")

	tests := []struct {
		name       string
		objects    []*unstructured.Unstructured
		wantStatus string
		wantReady  bool
	}{
		{
			name:       "repository missing",
			wantStatus: projectRepositoryStatusRepositoryMissing,
		},
		{
			name: "connection missing",
			objects: []*unstructured.Unstructured{
				codeRepositoryObject("demo-repo", "demo", "github", false),
			},
			wantStatus: projectRepositoryStatusConnectionMissing,
		},
		{
			name: "ready",
			objects: []*unstructured.Unstructured{
				codeRepositoryObject("demo-repo", "demo", "github", true),
				codeConnectionObject("github"),
			},
			wantStatus: projectRepositoryStatusReady,
			wantReady:  true,
		},
		{
			name: "repository reconcile failed",
			objects: []*unstructured.Unstructured{
				codeRepositoryObjectWithReadyCondition("demo-repo", "demo", "github", "False", "Error", "credential revoked"),
				codeConnectionObject("github"),
			},
			wantStatus: projectRepositoryStatusFailed,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			view := projectRepositoryViewFromGetter(context.Background(), project, codeObjectGetter(tt.objects...))
			if view == nil {
				t.Fatal("projectRepositoryView returned nil")
			}
			if view.Status != tt.wantStatus {
				t.Fatalf("Status = %q, want %q", view.Status, tt.wantStatus)
			}
			if view.Ready != tt.wantReady {
				t.Fatalf("Ready = %t, want %t", view.Ready, tt.wantReady)
			}
		})
	}
}

func TestProjectRepositoryViewIncludesCommits(t *testing.T) {
	project := projectWithRepository("demo-repo", "demo", "github")
	older := codeRepositoryCommitObject("older", "demo-repo", "Succeeded", "abc123", "Initial app", 2, time.Date(2026, 6, 12, 12, 0, 0, 0, time.UTC))
	newer := codeRepositoryCommitObject("newer", "demo-repo", "Running", "", "Update app", 0, time.Date(2026, 6, 13, 12, 0, 0, 0, time.UTC))

	view := projectRepositoryViewFromResources(
		context.Background(),
		project,
		codeObjectGetter(codeRepositoryObject("demo-repo", "demo", "github", true), codeConnectionObject("github")),
		codeObjectLister(older, newer, codeRepositoryCommitObject("other", "other-repo", "Succeeded", "zzz", "Other", 1, time.Date(2026, 6, 14, 12, 0, 0, 0, time.UTC))),
	)
	if view == nil {
		t.Fatal("projectRepositoryView returned nil")
	}
	if len(view.Commits) != 2 {
		t.Fatalf("Commits length = %d, want 2", len(view.Commits))
	}
	if view.Commits[0].Name != "newer" || view.Commits[1].Name != "older" {
		t.Fatalf("commits not sorted newest first: %#v", view.Commits)
	}
	if view.Commits[1].CommitSHA != "abc123" || view.Commits[1].FileCount != 2 || view.Commits[1].Message != "Initial app" {
		t.Fatalf("unexpected commit view: %#v", view.Commits[1])
	}
}

func projectWithRepository(ref, name, connectionRef string) *aiv1alpha1.Project {
	return &aiv1alpha1.Project{
		Spec: aiv1alpha1.ProjectSpec{
			Repository: &aiv1alpha1.ProjectRepositoryBinding{
				RepositoryRef: ref,
				Name:          name,
				ConnectionRef: connectionRef,
			},
		},
	}
}

func codeObjectGetter(objects ...*unstructured.Unstructured) codeResourceGetter {
	items := map[string]*unstructured.Unstructured{}
	for _, obj := range objects {
		gvr, ok := codeObjectGVR(obj)
		if !ok {
			continue
		}
		items[codeObjectKey(gvr, obj.GetName())] = obj
	}
	return func(_ context.Context, gvr schema.GroupVersionResource, name string) (*unstructured.Unstructured, error) {
		if obj := items[codeObjectKey(gvr, name)]; obj != nil {
			return obj, nil
		}
		return nil, apierrors.NewNotFound(schema.GroupResource{Group: gvr.Group, Resource: gvr.Resource}, name)
	}
}

func codeObjectLister(objects ...*unstructured.Unstructured) codeResourceLister {
	return func(_ context.Context, gvr schema.GroupVersionResource, opts metav1.ListOptions) (*unstructured.UnstructuredList, error) {
		selector := labels.Everything()
		if opts.LabelSelector != "" {
			parsed, err := labels.Parse(opts.LabelSelector)
			if err != nil {
				return nil, err
			}
			selector = parsed
		}
		list := &unstructured.UnstructuredList{}
		for _, obj := range objects {
			objGVR, ok := codeObjectGVR(obj)
			if !ok || objGVR != gvr || !selector.Matches(labels.Set(obj.GetLabels())) {
				continue
			}
			list.Items = append(list.Items, *obj)
		}
		return list, nil
	}
}

func codeObjectGVR(obj *unstructured.Unstructured) (schema.GroupVersionResource, bool) {
	switch obj.GetKind() {
	case "Connection":
		return codeConnectionsGVR, true
	case "Repository":
		return codeRepositoriesGVR, true
	case "RepositoryCommit":
		return codeRepositoryCommitsGVR, true
	default:
		return schema.GroupVersionResource{}, false
	}
}

func codeObjectKey(gvr schema.GroupVersionResource, name string) string {
	return gvr.Group + "/" + gvr.Resource + "/" + name
}

func codeRepositoryObject(name, repoName, connectionRef string, ready bool) *unstructured.Unstructured {
	status := ""
	if ready {
		status = "True"
	}
	return codeRepositoryObjectWithReadyCondition(name, repoName, connectionRef, status, "", "")
}

func codeRepositoryObjectWithReadyCondition(name, repoName, connectionRef, status, reason, message string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{
		Object: map[string]any{
			"spec": map[string]any{
				"name":          repoName,
				"connectionRef": connectionRef,
			},
		},
	}
	u.SetAPIVersion(codeSchemeGroupVersion.String())
	u.SetKind("Repository")
	u.SetName(name)
	if status != "" {
		u.Object["status"] = map[string]any{
			"conditions": []any{
				map[string]any{"type": codeConditionReady, "status": status, "reason": reason, "message": message},
			},
		}
	}
	return u
}

func codeConnectionObject(name string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetAPIVersion(codeSchemeGroupVersion.String())
	u.SetKind("Connection")
	u.SetName(name)
	return u
}

func codeRepositoryCommitObject(name, repositoryRef, phase, sha, message string, fileCount int64, created time.Time) *unstructured.Unstructured {
	u := &unstructured.Unstructured{
		Object: map[string]any{
			"spec": map[string]any{
				"repositoryRef": repositoryRef,
				"message":       message,
			},
			"status": map[string]any{
				"phase":     phase,
				"commitSHA": sha,
				"source": map[string]any{
					"fileCount": fileCount,
				},
			},
		},
	}
	u.SetAPIVersion(codeSchemeGroupVersion.String())
	u.SetKind("RepositoryCommit")
	u.SetName(name)
	u.SetLabels(map[string]string{codeLabelRepository: repositoryRef})
	u.SetCreationTimestamp(metav1.NewTime(created))
	return u
}
