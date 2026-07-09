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
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/components"
	einomodel "github.com/cloudwego/eino/components/model"
	einoschema "github.com/cloudwego/eino/schema"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	k8sschema "k8s.io/apimachinery/pkg/runtime/schema"
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
		"plan_project_changes",
		"check_project_readiness",
		"prepare_project_deployment",
		"deploy_project_runtime",
		"get_runtime_status",
		"get_preview_url",
		"get_runtime_logs",
		"restart_runtime",
		"set_runtime_env",
		"ask_follow_up",
		"request_project_plan_approval",
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
	for _, name := range []string{
		"infrastructure__list_templates",
		"infrastructure__describe_template",
		"infrastructure__list_instances",
		"infrastructure__get_instance",
		"infrastructure__provision",
		projectToolDatabricksListTables,
		projectToolDatabricksDescribeTable,
	} {
		if !projectMCPToolAllowed(name) {
			t.Fatalf("%s should be allowed from the aggregate MCP infrastructure provider", name)
		}
	}
	if projectMCPToolAllowed("infrastructure__delete_instance") {
		t.Fatal("infrastructure__delete_instance should not be allowed from App Studio")
	}
	if projectMCPToolAllowed("databricks__import_table") {
		t.Fatal("databricks__import_table should not be allowed from App Studio")
	}
	if projectMCPToolAllowed("databricks__query_table") {
		t.Fatal("databricks__query_table should not be auto-allowed from App Studio")
	}
}

func TestProjectAssistantToolRegistryListsLocalToolsInOrder(t *testing.T) {
	registry := projectAssistantLocalToolRegistry(nil)
	got := projectChatToolNames(registry.ChatTools(false))
	want := []string{
		"list_project_files",
		"read_project_file",
		"search_project_files",
		"plan_project_changes",
		"check_project_readiness",
		"prepare_project_deployment",
		"deploy_project_runtime",
		"get_runtime_status",
		"get_preview_url",
		"get_runtime_logs",
		"restart_runtime",
		"set_runtime_env",
		"ask_follow_up",
		"request_project_plan_approval",
		"write_file",
		"apply_patch",
		"mkdir",
		"select_project_template",
		"hydrate_workspace",
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("tool names = %v, want %v", got, want)
	}

	all := projectChatToolNames(registry.ChatTools(true))
	if len(all) != len(want)+1 || all[len(all)-1] != "commit_project_files" {
		t.Fatalf("tool names with commit bridge = %v, want commit_project_files last", all)
	}
	if !registry.Has(" COMMIT_PROJECT_FILES ") {
		t.Fatal("registry should match tool names case-insensitively")
	}
	tool, ok := registry.Get("write_file")
	if !ok {
		t.Fatal("write_file missing from registry")
	}
	if got := tool.Spec().Risk; got != projectAssistantToolRiskWrite {
		t.Fatalf("write_file risk = %q, want %q", got, projectAssistantToolRiskWrite)
	}
}

func projectChatToolNames(tools []chatTool) []string {
	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		names = append(names, tool.Function.Name)
	}
	return names
}

func TestLoadProjectMCPToolsExposesCommitBridgeAndInfrastructureTools(t *testing.T) {
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
		fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"result":{"tools":[{"name":"code__commit_files","description":"Commit files","inputSchema":{"type":"object"}},{"name":"code__read_repository_file","description":"Read files","inputSchema":{"type":"object"}},{"name":"infrastructure__list_templates","description":"List templates","inputSchema":{"type":"object","properties":{"cloud":{"type":"string"}}}},{"name":"infrastructure__describe_template","description":"Describe template","inputSchema":{"type":"object","required":["name"],"properties":{"name":{"type":"string"}}}},{"name":"infrastructure__provision","description":"Provision template","inputSchema":{"type":"object","required":["template","name"],"properties":{"template":{"type":"string"},"name":{"type":"string"},"values":{"type":"object"}}}},{"name":"databricks__list_tables","description":"List tables","inputSchema":{"type":"object"}},{"name":"databricks__import_table","description":"Import table","inputSchema":{"type":"object"}},{"name":"infrastructure__delete_instance","description":"Delete instance","inputSchema":{"type":"object"}}]}}`)
	}))
	defer mcp.Close()

	server := NewWithWorkspace(nil, nil, workspace.NewFileStore(t.TempDir()), mcp.URL, false)
	tools, err := server.loadProjectMCPTools(
		httptest.NewRequest(http.MethodPost, "/", nil),
		identity{tenantPath: "root:org-a:ws-1", clusterID: "cluster-ws-1"},
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
	for _, want := range []string{
		"infrastructure__list_templates",
		"infrastructure__describe_template",
		"infrastructure__provision",
		projectToolDatabricksListTables,
	} {
		if !names[want] {
			t.Fatalf("tool names = %#v, want %s", names, want)
		}
	}
	if names["code__commit_files"] || names["code__read_repository_file"] {
		t.Fatalf("tool names = %#v, should not expose raw provider-code tools", names)
	}
	if names["infrastructure__delete_instance"] {
		t.Fatalf("tool names = %#v, should not expose destructive infrastructure tools", names)
	}
	if names["databricks__import_table"] {
		t.Fatalf("tool names = %#v, should not expose table import tools", names)
	}
}

func TestGenerateProjectAssistantStreamIncludesDiscoveredToolPromptOnFirstInput(t *testing.T) {
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
		fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"result":{"tools":[{"name":"code__commit_files","description":"Commit workspace files","inputSchema":{"type":"object"}}]}}`)
	}))
	defer mcp.Close()

	messages := store.NewMemoryStore()
	server := NewWithWorkspace(nil, messages, workspace.NewFileStore(t.TempDir()), mcp.URL, false)
	project := projectWithRepository("demo-repo", "demo", "github")
	project.Name = "demo"
	id := identity{tenantPath: "root:org-a:ws-1", clusterID: "cluster-ws-1", orgUUID: "org-a", workspaceUUID: "ws-1", user: "user@example.com"}
	settings := projectLLMSettings{Provider: defaultProjectLLMProvider, BaseURL: defaultProjectLLMBaseURL, Model: "test-model", APIKey: "test-key"}
	client := asclient.NewFromDynamic(projectSettingsDynamicClient{secret: projectLLMSettingsSecret(settings)})
	messageScope := projectMessageScope(id.orgUUID, id.workspaceUUID, project.Name)
	if err := appendProjectUserMessage(context.Background(), messages, messageScope, "ship the demo"); err != nil {
		t.Fatalf("appendProjectUserMessage returned error: %v", err)
	}
	model := &repositoryFlowEinoChatModel{Steps: []repositoryFlowEinoModelStep{{
		Message: einoschema.AssistantMessage("Ready.", nil),
	}}}
	setProjectAssistantModelForTest(server, model)

	reply, err := server.generateProjectAssistantStream(
		httptest.NewRequest(http.MethodPost, "/", nil),
		id,
		client,
		project,
		projectAssistantStreamCallbacks{},
	)
	if err != nil {
		t.Fatalf("generateProjectAssistantStream returned error: %v", err)
	}
	if reply != "Ready." {
		t.Fatalf("reply = %q, want Ready.", reply)
	}
	if len(model.Inputs) != 1 {
		t.Fatalf("Eino model request count = %d, want 1", len(model.Inputs))
	}
	var joined string
	for _, msg := range model.Inputs[0].Messages {
		joined += msg.Content + "\n"
	}
	if !strings.Contains(joined, "Available tools in this workspace") || !strings.Contains(joined, "commit_project_files") {
		t.Fatalf("first model input missing discovered tool prompt:\n%s", joined)
	}
	if projectChatToolsInclude(model.Inputs[0].Tools, projectToolCommitProjectFiles) {
		t.Fatalf("model tools = %#v, want commit_project_files deferred behind tool_search", model.Inputs[0].Tools)
	}
	if !projectChatToolsInclude(model.Inputs[0].Tools, "tool_search") {
		t.Fatalf("model tools = %#v, want tool_search for deferred commit bridge", model.Inputs[0].Tools)
	}
}

func TestGenerateProjectAssistantStreamDiscoversDatabricksToolsForDataTableQuestions(t *testing.T) {
	mcpCalls := 0
	mcp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mcpCalls++
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
		fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"result":{"tools":[{"name":"databricks__list_tables","description":"List imported tables","inputSchema":{"type":"object"}},{"name":"databricks__describe_table","description":"Describe a table ref","inputSchema":{"type":"object"}},{"name":"databricks__query_table","description":"Query a table ref","inputSchema":{"type":"object"}},{"name":"databricks__import_table","description":"Import a table ref","inputSchema":{"type":"object"}}]}}`)
	}))
	defer mcp.Close()

	messages := store.NewMemoryStore()
	server := NewWithWorkspace(nil, messages, workspace.NewFileStore(t.TempDir()), mcp.URL, false)
	project := projectWithRepository("demo-repo", "demo", "github")
	project.Name = "demo"
	id := identity{tenantPath: "root:org-a:ws-1", clusterID: "cluster-ws-1", orgUUID: "org-a", workspaceUUID: "ws-1", user: "user@example.com"}
	settings := projectLLMSettings{Provider: defaultProjectLLMProvider, BaseURL: defaultProjectLLMBaseURL, Model: "test-model", APIKey: "test-key"}
	client := asclient.NewFromDynamic(projectSettingsDynamicClient{secret: projectLLMSettingsSecret(settings)})
	messageScope := projectMessageScope(id.orgUUID, id.workspaceUUID, project.Name)
	if err := appendProjectUserMessage(context.Background(), messages, messageScope, "Can you query the sales.orders table and show me its columns?"); err != nil {
		t.Fatalf("appendProjectUserMessage returned error: %v", err)
	}
	model := &repositoryFlowEinoChatModel{Steps: []repositoryFlowEinoModelStep{{
		Message: einoschema.AssistantMessage("I can only query existing Databricks table refs.", nil),
	}}}
	setProjectAssistantModelForTest(server, model)
	server.assistantTurnRouter = func(context.Context, projectAssistantTurnRouteRequest) (projectAssistantTurnDecision, error) {
		return projectAssistantTurnDecision{
			Profile:              projectAssistantTurnProfileExploration,
			RequiresCurrentState: true,
			Confidence:           projectAssistantTurnConfidenceHigh,
		}, nil
	}

	reply, err := server.generateProjectAssistantStream(
		httptest.NewRequest(http.MethodPost, "/", nil),
		id,
		client,
		project,
		projectAssistantStreamCallbacks{},
	)
	if err != nil {
		t.Fatalf("generateProjectAssistantStream returned error: %v", err)
	}
	if reply != "I can only query existing Databricks table refs." {
		t.Fatalf("reply = %q, want Databricks guidance", reply)
	}
	if mcpCalls != 1 {
		t.Fatalf("MCP tools/list calls = %d, want 1", mcpCalls)
	}
	var joined string
	for _, msg := range model.Inputs[0].Messages {
		joined += msg.Content + "\n"
	}
	for _, want := range []string{projectToolDatabricksListTables, projectToolDatabricksDescribeTable} {
		if !strings.Contains(joined, want) {
			t.Fatalf("model input missing %s:\n%s", want, joined)
		}
	}
	for _, want := range []string{
		"existing imported kedge Table resources only",
		"tableRef",
		"provider-databricks",
		"Do not call provider backend URLs",
		"Do not generate application code that queries Databricks tableRefs",
		"do not embed Databricks credentials",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("model input missing Databricks tableRef guidance %q:\n%s", want, joined)
		}
	}
	for _, forbidden := range []string{
		"databricks__import_table",
		"databricks__query_table",
		"/services/providers/databricks",
	} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("model input should not include filtered Databricks capability %q:\n%s", forbidden, joined)
		}
	}
}

func TestGenerateProjectAssistantStreamFiltersDatabricksToolsOnUnrelatedImplementationTurn(t *testing.T) {
	mcpCalls := 0
	mcp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mcpCalls++
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
		fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"result":{"tools":[{"name":"code__commit_files","description":"Commit files","inputSchema":{"type":"object"}},{"name":"databricks__list_tables","description":"List imported tables","inputSchema":{"type":"object"}},{"name":"databricks__describe_table","description":"Describe a table ref","inputSchema":{"type":"object"}}]}}`)
	}))
	defer mcp.Close()

	messages := store.NewMemoryStore()
	server := NewWithWorkspace(nil, messages, workspace.NewFileStore(t.TempDir()), mcp.URL, false)
	project := projectWithRepository("demo-repo", "demo", "github")
	project.Name = "demo"
	id := identity{tenantPath: "root:org-a:ws-1", clusterID: "cluster-ws-1", orgUUID: "org-a", workspaceUUID: "ws-1", user: "user@example.com"}
	settings := projectLLMSettings{Provider: defaultProjectLLMProvider, BaseURL: defaultProjectLLMBaseURL, Model: "test-model", APIKey: "test-key"}
	client := asclient.NewFromDynamic(projectSettingsDynamicClient{secret: projectLLMSettingsSecret(settings)})
	messageScope := projectMessageScope(id.orgUUID, id.workspaceUUID, project.Name)
	if err := appendProjectUserMessage(context.Background(), messages, messageScope, "Fix the button styling and commit it."); err != nil {
		t.Fatalf("appendProjectUserMessage returned error: %v", err)
	}
	model := &repositoryFlowEinoChatModel{Steps: []repositoryFlowEinoModelStep{{
		Message: einoschema.AssistantMessage("I will update the styling.", nil),
	}}}
	setProjectAssistantModelForTest(server, model)
	server.assistantTurnRouter = func(context.Context, projectAssistantTurnRouteRequest) (projectAssistantTurnDecision, error) {
		return projectAssistantTurnDecision{
			Profile:          projectAssistantTurnProfileImplementation,
			RequestsMutation: true,
			Confidence:       projectAssistantTurnConfidenceHigh,
		}, nil
	}

	if _, err := server.generateProjectAssistantStream(
		httptest.NewRequest(http.MethodPost, "/", nil),
		id,
		client,
		project,
		projectAssistantStreamCallbacks{},
	); err != nil {
		t.Fatalf("generateProjectAssistantStream returned error: %v", err)
	}
	if mcpCalls != 1 {
		t.Fatalf("MCP tools/list calls = %d, want 1 for commit bridge discovery", mcpCalls)
	}
	var joined string
	for _, msg := range model.Inputs[0].Messages {
		joined += msg.Content + "\n"
	}
	if !strings.Contains(joined, projectToolCommitProjectFiles) {
		t.Fatalf("model input missing commit bridge guidance:\n%s", joined)
	}
	if strings.Contains(joined, projectToolDatabricksListTables) ||
		strings.Contains(joined, projectToolDatabricksDescribeTable) ||
		strings.Contains(joined, "Databricks guidance") {
		t.Fatalf("model input should not include Databricks tools for unrelated implementation turn:\n%s", joined)
	}
	if projectChatToolsInclude(model.Inputs[0].Tools, projectToolDatabricksListTables) ||
		projectChatToolsInclude(model.Inputs[0].Tools, projectToolDatabricksDescribeTable) {
		t.Fatalf("model tools should not include Databricks tools: %#v", model.Inputs[0].Tools)
	}
}

func TestGenerateProjectAssistantStreamSkipsDatabricksDiscoveryForGenericTables(t *testing.T) {
	mcp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("unexpected MCP request for generic table prompt: %s %s", r.Method, r.URL.Path)
	}))
	defer mcp.Close()

	messages := store.NewMemoryStore()
	server := NewWithWorkspace(nil, messages, workspace.NewFileStore(t.TempDir()), mcp.URL, false)
	project := projectWithRepository("demo-repo", "demo", "github")
	project.Name = "demo"
	id := identity{tenantPath: "root:org-a:ws-1", clusterID: "cluster-ws-1", orgUUID: "org-a", workspaceUUID: "ws-1", user: "user@example.com"}
	settings := projectLLMSettings{Provider: defaultProjectLLMProvider, BaseURL: defaultProjectLLMBaseURL, Model: "test-model", APIKey: "test-key"}
	client := asclient.NewFromDynamic(projectSettingsDynamicClient{secret: projectLLMSettingsSecret(settings)})
	messageScope := projectMessageScope(id.orgUUID, id.workspaceUUID, project.Name)
	if err := appendProjectUserMessage(context.Background(), messages, messageScope, "Render a table of todos in the app."); err != nil {
		t.Fatalf("appendProjectUserMessage returned error: %v", err)
	}
	model := &repositoryFlowEinoChatModel{Steps: []repositoryFlowEinoModelStep{{
		Message: einoschema.AssistantMessage("I will update the UI table.", nil),
	}}}
	setProjectAssistantModelForTest(server, model)
	server.assistantTurnRouter = func(context.Context, projectAssistantTurnRouteRequest) (projectAssistantTurnDecision, error) {
		return projectAssistantTurnDecision{
			Profile:              projectAssistantTurnProfileExploration,
			RequiresCurrentState: true,
			Confidence:           projectAssistantTurnConfidenceHigh,
		}, nil
	}

	if _, err := server.generateProjectAssistantStream(
		httptest.NewRequest(http.MethodPost, "/", nil),
		id,
		client,
		project,
		projectAssistantStreamCallbacks{},
	); err != nil {
		t.Fatalf("generateProjectAssistantStream returned error: %v", err)
	}
	var joined string
	for _, msg := range model.Inputs[0].Messages {
		joined += msg.Content + "\n"
	}
	if strings.Contains(joined, projectToolDatabricksListTables) || strings.Contains(joined, "Databricks guidance") {
		t.Fatalf("model input should not include filtered databricks tools:\n%s", joined)
	}
}

func TestGenerateProjectAssistantStreamDiscoversInfrastructureTemplatesForInfraQuestions(t *testing.T) {
	mcpCalls := 0
	mcp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mcpCalls++
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
		fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"result":{"tools":[{"name":"infrastructure__list_templates","description":"List every template available in your workspace catalog","inputSchema":{"type":"object"}},{"name":"infrastructure__describe_template","description":"Return a template's metadata and JSON schema","inputSchema":{"type":"object","required":["name"],"properties":{"name":{"type":"string"}}}},{"name":"infrastructure__provision","description":"Provision a template instance","inputSchema":{"type":"object"}}]}}`)
	}))
	defer mcp.Close()

	messages := store.NewMemoryStore()
	server := NewWithWorkspace(nil, messages, workspace.NewFileStore(t.TempDir()), mcp.URL, false)
	project := projectWithRepository("demo-repo", "demo", "github")
	project.Name = "demo"
	id := identity{tenantPath: "root:org-a:ws-1", clusterID: "cluster-ws-1", orgUUID: "org-a", workspaceUUID: "ws-1", user: "user@example.com"}
	settings := projectLLMSettings{Provider: defaultProjectLLMProvider, BaseURL: defaultProjectLLMBaseURL, Model: "test-model", APIKey: "test-key"}
	client := asclient.NewFromDynamic(projectSettingsDynamicClient{secret: projectLLMSettingsSecret(settings)})
	messageScope := projectMessageScope(id.orgUUID, id.workspaceUUID, project.Name)
	if err := appendProjectUserMessage(context.Background(), messages, messageScope, "What infrastructure templates can I deploy?"); err != nil {
		t.Fatalf("appendProjectUserMessage returned error: %v", err)
	}
	model := &repositoryFlowEinoChatModel{Steps: []repositoryFlowEinoModelStep{{
		Message: einoschema.AssistantMessage("I will answer from the template catalog.", nil),
	}}}
	setProjectAssistantModelForTest(server, model)
	server.assistantTurnRouter = func(context.Context, projectAssistantTurnRouteRequest) (projectAssistantTurnDecision, error) {
		return projectAssistantTurnDecision{
			Profile:              projectAssistantTurnProfileExploration,
			RequiresCurrentState: true,
			Confidence:           projectAssistantTurnConfidenceHigh,
		}, nil
	}

	reply, err := server.generateProjectAssistantStream(
		httptest.NewRequest(http.MethodPost, "/", nil),
		id,
		client,
		project,
		projectAssistantStreamCallbacks{},
	)
	if err != nil {
		t.Fatalf("generateProjectAssistantStream returned error: %v", err)
	}
	if reply != "I will answer from the template catalog." {
		t.Fatalf("reply = %q, want template catalog answer", reply)
	}
	if mcpCalls != 1 {
		t.Fatalf("MCP tools/list calls = %d, want 1", mcpCalls)
	}
	var joined string
	for _, msg := range model.Inputs[0].Messages {
		joined += msg.Content + "\n"
	}
	for _, want := range []string{projectToolInfrastructureListTemplates, projectToolInfrastructureDescribeTemplate} {
		if !strings.Contains(joined, want) {
			t.Fatalf("model input missing %s:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, projectToolInfrastructureProvision) {
		t.Fatalf("exploration prompt should not expose provisioning:\n%s", joined)
	}
}

func TestGenerateProjectAssistantStreamHonorsRuntimeStateRouterDecision(t *testing.T) {
	mcpCalls := 0
	mcp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mcpCalls++
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"result":{"tools":[{"name":"code__commit_files","description":"Commit files","inputSchema":{"type":"object"}}]}}`)
	}))
	defer mcp.Close()

	messages := store.NewMemoryStore()
	server := NewWithWorkspace(nil, messages, workspace.NewFileStore(t.TempDir()), mcp.URL, false)
	server.assistantTurnRouter = func(context.Context, projectAssistantTurnRouteRequest) (projectAssistantTurnDecision, error) {
		return projectAssistantTurnDecision{
			Profile:              projectAssistantTurnProfileExploration,
			RequiresCurrentState: true,
			RequiresRuntimeState: true,
			Confidence:           projectAssistantTurnConfidenceHigh,
		}, nil
	}
	project := projectWithRepository("demo-repo", "demo", "github")
	project.Name = "demo"
	id := identity{tenantPath: "root:org-a:ws-1", clusterID: "cluster-ws-1", orgUUID: "org-a", workspaceUUID: "ws-1", user: "user@example.com"}
	settings := projectLLMSettings{Provider: defaultProjectLLMProvider, BaseURL: defaultProjectLLMBaseURL, Model: "test-model", APIKey: "test-key"}
	client := asclient.NewFromDynamic(projectSettingsDynamicClient{secret: projectLLMSettingsSecret(settings)})
	messageScope := projectMessageScope(id.orgUUID, id.workspaceUUID, project.Name)
	if err := appendProjectUserMessage(context.Background(), messages, messageScope, "Show me the current preview URL"); err != nil {
		t.Fatalf("appendProjectUserMessage returned error: %v", err)
	}
	model := &repositoryFlowEinoChatModel{Steps: []repositoryFlowEinoModelStep{{
		Message: einoschema.AssistantMessage("Preview status checked.", nil),
	}}}
	setProjectAssistantModelForTest(server, model)
	server.assistantTurnRouter = func(context.Context, projectAssistantTurnRouteRequest) (projectAssistantTurnDecision, error) {
		return projectAssistantTurnDecision{
			Profile:              projectAssistantTurnProfileExploration,
			RequiresCurrentState: true,
			RequiresRuntimeState: true,
			Confidence:           projectAssistantTurnConfidenceHigh,
		}, nil
	}

	reply, err := server.generateProjectAssistantStream(
		httptest.NewRequest(http.MethodPost, "/", nil),
		id,
		client,
		project,
		projectAssistantStreamCallbacks{},
	)
	if err != nil {
		t.Fatalf("generateProjectAssistantStream returned error: %v", err)
	}
	if reply != "Preview status checked." {
		t.Fatalf("reply = %q, want Preview status checked.", reply)
	}
	if mcpCalls != 0 {
		t.Fatalf("MCP tools/list calls = %d, want 0 for read-only runtime-state exploration", mcpCalls)
	}
	var joined string
	for _, msg := range model.Inputs[0].Messages {
		joined += msg.Content + "\n"
	}
	for _, want := range []string{projectToolGetRuntimeStatus, projectToolGetPreviewURL} {
		if !strings.Contains(joined, want) {
			t.Fatalf("model input missing %s:\n%s", want, joined)
		}
	}
	for _, unwanted := range []string{projectToolDeployProjectRuntime, projectToolWriteFile, projectToolCommitProjectFiles} {
		if strings.Contains(joined, unwanted) {
			t.Fatalf("model input unexpectedly mentions %s:\n%s", unwanted, joined)
		}
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

	prompt := projectSystemPrompt(project, repository, projectAssistantTurnProfileImplementation)
	for _, want := range []string{"check_project_readiness", "prepare_project_deployment", "deploy_project_runtime", "get_runtime_status", "get_preview_url", "list_project_files", "read_project_file", "search_project_files", "write_file", "apply_patch", "mkdir", "commit_project_files"} {
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
	if !strings.Contains(prompt, "Do not narrate tool calls") {
		t.Fatalf("prompt missing tool-call narration guidance:\n%s", prompt)
	}
	if !strings.Contains(prompt, "tool_search") || !strings.Contains(prompt, "select:") {
		t.Fatalf("prompt missing deferred tool_search guidance:\n%s", prompt)
	}
	if !strings.Contains(strings.ToLower(prompt), "before") || !strings.Contains(strings.ToLower(prompt), "inspect") {
		t.Fatalf("prompt does not require inspect-before-edit guidance:\n%s", prompt)
	}
}

func TestProjectAssistantDoesNotAdvertiseLegacyRuntimeCommandTools(t *testing.T) {
	registry := projectAssistantLocalToolRegistry(nil)
	toolNames := strings.Join(projectChatToolNames(registry.ChatTools(true)), "\n")
	project := projectWithRepository("demo-repo", "demo", "github")
	project.Name = "demo-project"
	prompt := projectSystemPrompt(project, &ProjectRepositoryView{
		Ref:    "demo-repo",
		Name:   "demo",
		Status: projectRepositoryStatusReady,
		Ready:  true,
	})
	combined := toolNames + "\n" + prompt
	for _, unwanted := range []string{
		"runtime_command",
		"verify_project_runtime",
		"preview_project_runtime",
	} {
		if strings.Contains(combined, unwanted) {
			t.Fatalf("App Studio should not advertise legacy runtime command tool %q:\n%s", unwanted, combined)
		}
	}
}

func TestProjectStatusTouchPatchPatchesAppStudioFieldsOnly(t *testing.T) {
	updatedAt := metav1.NewTime(time.Date(2026, 6, 14, 20, 0, 0, 0, time.UTC))
	data, err := projectStatusTouchPatch(updatedAt)
	if err != nil {
		t.Fatalf("projectStatusTouchPatch returned error: %v", err)
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
			name: "plan_project_changes",
			args: `{"includeFiles":true,"maxFiles":12}`,
			want: []string{"includeFiles true", "maxFiles 12"},
		},
		{
			name: "check_project_readiness",
			args: `{"includeFiles":true,"maxFiles":12}`,
			want: []string{"includeFiles true", "maxFiles 12"},
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

	workflowResult := `{"summary":"Plan project changes for Demo App.","files":["src/App.tsx"],"steps":["Inspect files","Commit after approval"]}`
	got = summarizeProjectToolResult("plan_project_changes", workflowResult)
	for _, want := range []string{"Plan project changes for Demo App.", "2 step(s)", "1 file(s): src/App.tsx"} {
		if !strings.Contains(got, want) {
			t.Fatalf("summary = %q, want %q", got, want)
		}
	}

	readinessResult := `{"status":"ready_to_verify","recommendedChecks":["build","test"],"files":["package.json","src/App.tsx"]}`
	got = summarizeProjectToolResult("check_project_readiness", readinessResult)
	for _, want := range []string{"status ready_to_verify", "checks build, test", "2 file(s): package.json, src/App.tsx"} {
		if !strings.Contains(got, want) {
			t.Fatalf("summary = %q, want %q", got, want)
		}
	}

}

func TestProjectAssistantMessageMetadataSafeActions(t *testing.T) {
	events := []projectToolCallStreamEvent{
		{ID: "call-1", Name: "code__commit_files", Status: "running"},
		{ID: "call-1", Status: "succeeded", Summary: "commit abc123"},
	}
	merged := upsertProjectToolCallStreamEvent(events[:1], events[1])
	metadata := projectAssistantMessageMetadata("", merged)
	if _, ok := metadata["toolCalls"]; ok {
		t.Fatalf("metadata = %#v, should not persist raw toolCalls", metadata)
	}
	actions := projectAssistantUIActionsFromMetadata(metadata[projectMessageMetadataAssistantActions])
	if len(actions) != 1 {
		t.Fatalf("assistant actions length = %d, want 1", len(actions))
	}
	if actions[0].Kind != projectAssistantUIActionCommit || actions[0].Status != "succeeded" || actions[0].Label != "Committed changes" {
		t.Fatalf("unexpected assistant action metadata: %#v", actions[0])
	}
}

func assertProjectAssistantMetadataDoesNotContain(t *testing.T, metadata map[string]any, forbidden ...string) {
	t.Helper()
	raw, err := json.Marshal(metadata)
	if err != nil {
		t.Fatalf("marshal metadata: %v", err)
	}
	payload := string(raw)
	for _, value := range forbidden {
		if strings.Contains(payload, value) {
			t.Fatalf("assistant metadata leaked %q in %s", value, payload)
		}
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

func TestProjectLocalToolRunsWorkspaceReadTool(t *testing.T) {
	workspaces := workspace.NewFileStore(t.TempDir())
	scope := workspace.Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: "demo"}
	if err := workspaces.ApplyFiles(context.Background(), scope, []workspace.File{
		{Path: "README.md", Content: "hello from App Studio workspace\n"},
	}); err != nil {
		t.Fatalf("ApplyFiles returned error: %v", err)
	}
	server := NewWithWorkspace(nil, nil, workspaces, "", false)

	tool, ok := server.projectAssistantToolRegistry().Get(projectToolReadProjectFile)
	if !ok {
		t.Fatal("read_project_file missing from registry")
	}
	resp, err := tool.Call(context.Background(), projectAssistantToolCallRequest{
		WorkspaceScope: scope,
		HTTPRequest:    httptest.NewRequest(http.MethodPost, "/", nil),
		Arguments:      map[string]any{"path": "README.md"},
	})
	if err != nil {
		t.Fatalf("read_project_file returned error: %v", err)
	}
	if !strings.Contains(resp, "hello from App Studio workspace") {
		t.Fatalf("tool response = %q, want workspace file content", resp)
	}
}

func TestProjectLocalToolRunsWorkspaceMutationTools(t *testing.T) {
	workspaces := workspace.NewFileStore(t.TempDir())
	scope := workspace.Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: "demo"}
	server := NewWithWorkspace(nil, nil, workspaces, "", false)

	for _, call := range []struct {
		name string
		args map[string]any
	}{
		{name: projectToolMkdir, args: map[string]any{"path": "src"}},
		{name: projectToolWriteFile, args: map[string]any{"path": "src/App.tsx", "content": "hello world\n"}},
		{name: projectToolApplyPatch, args: map[string]any{"path": "src/App.tsx", "oldText": "world", "newText": "Kedge"}},
	} {
		tool, ok := server.projectAssistantToolRegistry().Get(call.name)
		if !ok {
			t.Fatalf("%s missing from registry", call.name)
		}
		if _, err := tool.Call(context.Background(), projectAssistantToolCallRequest{
			WorkspaceScope: scope,
			HTTPRequest:    httptest.NewRequest(http.MethodPost, "/", nil),
			Arguments:      call.args,
		}); err != nil {
			t.Fatalf("%s returned error: %v", call.name, err)
		}
	}
	read, err := workspaces.ReadFile(context.Background(), scope, workspace.ReadOptions{Path: "src/App.tsx"})
	if err != nil {
		t.Fatalf("ReadFile returned error: %v", err)
	}
	if read.Content != "hello Kedge\n" {
		t.Fatalf("workspace content = %q", read.Content)
	}
}

func TestGenerateProjectAssistantStreamRequestsPermissionForWriteTool(t *testing.T) {
	settings := projectLLMSettings{
		Provider: defaultProjectLLMProvider,
		BaseURL:  defaultProjectLLMBaseURL,
		Model:    "test-model",
		APIKey:   "test-key",
	}
	client := asclient.NewFromDynamic(projectSettingsDynamicClient{secret: projectLLMSettingsSecret(settings)})
	messages := store.NewMemoryStore()
	messageScope := store.Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: "demo"}
	if err := appendProjectUserMessage(context.Background(), messages, messageScope, "write a file"); err != nil {
		t.Fatalf("appendProjectUserMessage returned error: %v", err)
	}
	workspaces := workspace.NewFileStore(t.TempDir())
	server := NewWithWorkspace(nil, messages, workspaces, "", false)
	model := &repositoryFlowEinoChatModel{Steps: []repositoryFlowEinoModelStep{{
		Message: einoschema.AssistantMessage("", []einoschema.ToolCall{{
			ID:   "call-write",
			Type: "function",
			Function: einoschema.FunctionCall{
				Name:      projectToolWriteFile,
				Arguments: `{"path":"src/App.tsx","content":"hello\n"}`,
			},
		}}),
	}}}
	setProjectAssistantModelForTest(server, model)
	project := projectWithRepository("demo-repo", "demo", "github")
	project.Name = "demo"

	var events []projectAssistantEvent
	_, err := server.generateProjectAssistantStream(
		httptest.NewRequest(http.MethodPost, "/", nil),
		identity{tenantPath: "root:org-a:ws-1", clusterID: "cluster-ws-1", orgUUID: "org-a", workspaceUUID: "ws-1"},
		client,
		project,
		projectAssistantStreamCallbacks{
			OnAssistantEvent: func(event projectAssistantEvent) {
				events = append(events, event)
			},
		},
	)
	var permissionErr *projectAssistantPermissionRequiredError
	if !errors.As(err, &permissionErr) {
		t.Fatalf("generateProjectAssistantStream error = %v, want permission required", err)
	}
	if permissionErr.RunID == "" || permissionErr.RequestID == "" {
		t.Fatalf("permission error missing ids: %#v", permissionErr)
	}
	if len(model.Inputs) != 1 {
		t.Fatalf("Eino model request count = %d, want 1", len(model.Inputs))
	}
	if _, err := workspaces.ReadFile(context.Background(), workspace.Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: "demo"}, workspace.ReadOptions{Path: "src/App.tsx"}); err == nil {
		t.Fatal("write_file ran before permission was approved")
	}

	var sawPermission, sawCheckpoint bool
	for _, event := range events {
		switch event.Type {
		case projectAssistantEventPermissionNeeded:
			sawPermission = true
			if event.Permission == nil || event.Permission.ID != permissionErr.RequestID || event.Permission.ToolName != "write_file" {
				t.Fatalf("permission event = %#v, want write_file request %q", event, permissionErr.RequestID)
			}
		case projectAssistantEventCheckpointSaved:
			sawCheckpoint = true
			if event.Checkpoint == nil || event.Checkpoint.ID != permissionErr.RunID {
				t.Fatalf("checkpoint event = %#v, want run %q", event, permissionErr.RunID)
			}
		}
	}
	if !sawPermission || !sawCheckpoint {
		t.Fatalf("events = %#v, want permission and checkpoint events", events)
	}
	run, err := messages.GetAssistantRun(context.Background(), messageScope, permissionErr.RunID)
	if err != nil {
		t.Fatalf("GetAssistantRun returned error: %v", err)
	}
	if run.Status != store.AssistantRunStatusPendingPermission || run.RequestID != permissionErr.RequestID {
		t.Fatalf("assistant run = %#v, want pending permission request", run)
	}
}

func TestStreamProjectAssistantPersistsPermissionTimelineMessage(t *testing.T) {
	settings := projectLLMSettings{
		Provider: defaultProjectLLMProvider,
		BaseURL:  defaultProjectLLMBaseURL,
		Model:    "test-model",
		APIKey:   "test-key",
	}
	client := asclient.NewFromDynamic(projectSettingsDynamicClient{secret: projectLLMSettingsSecret(settings)})
	messages := store.NewMemoryStore()
	id := identity{tenantPath: "root:org-a:ws-1", clusterID: "cluster-ws-1", orgUUID: "org-a", workspaceUUID: "ws-1"}
	messageScope := projectMessageScope(id.orgUUID, id.workspaceUUID, "demo")
	if err := appendProjectUserMessage(context.Background(), messages, messageScope, "write a file"); err != nil {
		t.Fatalf("appendProjectUserMessage returned error: %v", err)
	}
	server := NewWithWorkspace(nil, messages, workspace.NewFileStore(t.TempDir()), "", false)
	setProjectAssistantModelForTest(server, &repositoryFlowEinoChatModel{Steps: []repositoryFlowEinoModelStep{{
		Message: einoschema.AssistantMessage("", []einoschema.ToolCall{{
			ID:   "call-write",
			Type: "function",
			Function: einoschema.FunctionCall{
				Name:      projectToolWriteFile,
				Arguments: `{"path":"src/App.tsx","content":"hello\n"}`,
			},
		}}),
	}}})
	project := projectWithRepository("demo-repo", "demo", "github")
	project.Name = "demo"
	rr := httptest.NewRecorder()
	flusher, ok := startProjectMessageStream(rr)
	if !ok {
		t.Fatal("response recorder did not support streaming")
	}

	server.streamProjectAssistant(
		rr,
		flusher,
		httptest.NewRequest(http.MethodPost, "/", nil),
		client,
		id,
		project,
		messages,
		"",
	)

	recent, err := messages.LoadRecentMessages(context.Background(), messageScope, 10)
	if err != nil {
		t.Fatalf("LoadRecentMessages returned error: %v", err)
	}
	var assistant store.Message
	for _, msg := range recent {
		if msg.Role == aiv1alpha1.ProjectMessageRoleAssistant {
			assistant = msg
		}
	}
	if assistant.ID == "" {
		t.Fatalf("messages = %#v, want persisted assistant permission message", recent)
	}
	if assistant.Metadata[projectMessageMetadataStatus] != projectMessageStatusPendingPermission {
		t.Fatalf("assistant metadata = %#v, want pending permission status", assistant.Metadata)
	}
	if _, ok := assistant.Metadata["toolCalls"]; ok {
		t.Fatalf("assistant metadata = %#v, should not persist raw toolCalls", assistant.Metadata)
	}
	actions := projectAssistantUIActionsFromMetadata(assistant.Metadata[projectMessageMetadataAssistantActions])
	if len(actions) != 1 || actions[0].Status != "awaiting_approval" || actions[0].Kind != projectAssistantUIActionEdit {
		t.Fatalf("assistant actions = %#v, want pending edit disclosure", actions)
	}
	// Tool-level transparency: the action names the tool and its summarized
	// arguments (path + byte count — never the file contents).
	if actions[0].Tool != projectToolWriteFile || !strings.Contains(actions[0].Arguments, "src/App.tsx") {
		t.Fatalf("assistant action disclosure = %#v, want tool name and summarized arguments", actions[0])
	}
	interrupt := projectAssistantUIInterruptFromMetadata(assistant.Metadata[projectMessageMetadataAssistantInterrupt])
	if interrupt == nil || interrupt.Status != "pending" || interrupt.Action == nil || interrupt.Action.RunID == "" || interrupt.Action.RequestID == "" {
		t.Fatalf("assistant interrupt = %#v, want pending resume handle", interrupt)
	}
	assertProjectAssistantMetadataDoesNotContain(t, assistant.Metadata, "hello", "permission_required", "waiting_for_permission")
}

func TestStreamProjectAssistantPersistsPermissionTimelineAfterStreamWriteFailure(t *testing.T) {
	settings := projectLLMSettings{
		Provider: defaultProjectLLMProvider,
		BaseURL:  defaultProjectLLMBaseURL,
		Model:    "test-model",
		APIKey:   "test-key",
	}
	client := asclient.NewFromDynamic(projectSettingsDynamicClient{secret: projectLLMSettingsSecret(settings)})
	messages := store.NewMemoryStore()
	id := identity{tenantPath: "root:org-a:ws-1", clusterID: "cluster-ws-1", orgUUID: "org-a", workspaceUUID: "ws-1"}
	messageScope := projectMessageScope(id.orgUUID, id.workspaceUUID, "demo")
	if err := appendProjectUserMessage(context.Background(), messages, messageScope, "write a file"); err != nil {
		t.Fatalf("appendProjectUserMessage returned error: %v", err)
	}
	server := NewWithWorkspace(nil, messages, workspace.NewFileStore(t.TempDir()), "", false)
	setProjectAssistantModelForTest(server, &repositoryFlowEinoChatModel{Steps: []repositoryFlowEinoModelStep{{
		Message: einoschema.AssistantMessage("", []einoschema.ToolCall{{
			ID:   "call-write",
			Type: "function",
			Function: einoschema.FunctionCall{
				Name:      projectToolWriteFile,
				Arguments: `{"path":"src/App.tsx","content":"hello\n"}`,
			},
		}}),
	}}})
	project := projectWithRepository("demo-repo", "demo", "github")
	project.Name = "demo"
	stream := &failingProjectStreamResponseWriter{header: http.Header{}}

	server.streamProjectAssistant(
		stream,
		stream,
		httptest.NewRequest(http.MethodPost, "/", nil),
		client,
		id,
		project,
		messages,
		"",
	)

	recent, err := messages.LoadRecentMessages(context.Background(), messageScope, 10)
	if err != nil {
		t.Fatalf("LoadRecentMessages returned error: %v", err)
	}
	var assistant store.Message
	for _, msg := range recent {
		if msg.Role == aiv1alpha1.ProjectMessageRoleAssistant {
			assistant = msg
		}
	}
	if assistant.ID == "" {
		t.Fatalf("messages = %#v, want persisted assistant permission message", recent)
	}
	if _, ok := assistant.Metadata["toolCalls"]; ok {
		t.Fatalf("assistant metadata = %#v, should not persist raw toolCalls", assistant.Metadata)
	}
	actions := projectAssistantUIActionsFromMetadata(assistant.Metadata[projectMessageMetadataAssistantActions])
	interrupt := projectAssistantUIInterruptFromMetadata(assistant.Metadata[projectMessageMetadataAssistantInterrupt])
	if assistant.Metadata[projectMessageMetadataStatus] != projectMessageStatusPendingPermission || len(actions) != 1 || actions[0].Status != "awaiting_approval" || interrupt == nil || interrupt.Action == nil {
		t.Fatalf("assistant metadata = %#v, want pending permission with checkpoint after stream write failure", assistant.Metadata)
	}
	assertProjectAssistantMetadataDoesNotContain(t, assistant.Metadata, "hello", "permission_required", "waiting_for_permission")
}

type projectAssistantPermissionFixture struct {
	Client        *asclient.Client
	PermissionErr *projectAssistantPermissionRequiredError
	Permission    projectAssistantPermission
	Checkpoint    projectAssistantCheckpoint
	LLMRequests   *[]chatCompletionRequest
}

type chatCompletionRequest struct {
	Messages   []chatMessage
	Tools      []chatTool
	ToolChoice string
}

type chatStreamingCall struct {
	Index        int
	ID           string
	Type         string
	ExtraContent map[string]any
	Function     struct {
		Name      string
		Arguments string
	}
}

type repositoryFlowEinoModelStep struct {
	Message *einoschema.Message
	Err     error
	Inspect func([]*einoschema.Message)
}

type repositoryFlowEinoChatModel struct {
	Steps  []repositoryFlowEinoModelStep
	Inputs []chatCompletionRequest
}

func (m *repositoryFlowEinoChatModel) Generate(ctx context.Context, input []*einoschema.Message, opts ...einomodel.Option) (*einoschema.Message, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	common := einomodel.GetCommonOptions(nil, opts...)
	m.Inputs = append(m.Inputs, chatCompletionRequest{
		Messages:   projectEinoMessagesToChat(input),
		Tools:      projectTestChatTools(common.Tools),
		ToolChoice: projectTestToolChoice(common.ToolChoice, len(common.Tools)),
	})
	ctx = callbacks.EnsureRunInfo(ctx, "repository-flow-test-model", components.ComponentOfChatModel)
	ctx = callbacks.OnStart(ctx, &einomodel.CallbackInput{
		Messages:   input,
		Tools:      common.Tools,
		ToolChoice: common.ToolChoice,
	})

	index := len(m.Inputs) - 1
	step := repositoryFlowEinoModelStep{Message: einoschema.AssistantMessage("Done.", nil)}
	if index < len(m.Steps) {
		step = m.Steps[index]
	}
	if step.Inspect != nil {
		step.Inspect(input)
	}
	if step.Err != nil {
		callbacks.OnError(ctx, step.Err)
		return nil, step.Err
	}
	if step.Message == nil {
		step.Message = einoschema.AssistantMessage("", nil)
	}
	callbacks.OnEnd(ctx, &einomodel.CallbackOutput{Message: step.Message})
	return step.Message, nil
}

func (m *repositoryFlowEinoChatModel) Stream(ctx context.Context, input []*einoschema.Message, opts ...einomodel.Option) (*einoschema.StreamReader[*einoschema.Message], error) {
	msg, err := m.Generate(ctx, input, opts...)
	if err != nil {
		return nil, err
	}
	return einoschema.StreamReaderFromArray([]*einoschema.Message{msg}), nil
}

func projectTestChatTools(infos []*einoschema.ToolInfo) []chatTool {
	out := make([]chatTool, 0, len(infos))
	for _, info := range infos {
		if info == nil || strings.TrimSpace(info.Name) == "" {
			continue
		}
		out = append(out, chatTool{
			Type: "function",
			Function: chatToolFunction{
				Name:        strings.TrimSpace(info.Name),
				Description: strings.TrimSpace(info.Desc),
			},
		})
	}
	return out
}

func projectTestToolChoice(choice *einoschema.ToolChoice, toolCount int) string {
	if choice == nil {
		if toolCount > 0 {
			return "auto"
		}
		return ""
	}
	switch *choice {
	case einoschema.ToolChoiceForbidden:
		return "none"
	case einoschema.ToolChoiceForced:
		return "required"
	case einoschema.ToolChoiceAllowed:
		if toolCount > 0 {
			return "auto"
		}
	}
	return ""
}

func projectEinoToolCallFromStreamingForTest(call chatStreamingCall) einoschema.ToolCall {
	index := call.Index
	extra := map[string]any(nil)
	if len(call.ExtraContent) > 0 {
		extra = map[string]any{}
		for key, value := range call.ExtraContent {
			extra[key] = value
		}
	}
	toolType := strings.TrimSpace(call.Type)
	if toolType == "" {
		toolType = "function"
	}
	return einoschema.ToolCall{
		Index: &index,
		ID:    call.ID,
		Type:  toolType,
		Function: einoschema.FunctionCall{
			Name:      call.Function.Name,
			Arguments: call.Function.Arguments,
		},
		Extra: extra,
	}
}

func projectEinoToolCallsFromStreamingForTest(calls []chatStreamingCall) []einoschema.ToolCall {
	out := make([]einoschema.ToolCall, 0, len(calls))
	for _, call := range calls {
		out = append(out, projectEinoToolCallFromStreamingForTest(call))
	}
	return out
}

func setProjectAssistantModelForTest(server *Server, model einomodel.BaseChatModel) {
	server.mu.Lock()
	defer server.mu.Unlock()
	server.assistantEngine = projectEinoAssistantEngine{
		server: server,
		newModel: func(context.Context, projectAssistantRunRequest, *projectEinoAssistantRunState) (einomodel.BaseChatModel, error) {
			return model, nil
		},
		newTools: newProjectEinoAssistantToolsFactory(server),
	}
	server.assistantTurnRouter = projectAssistantFallbackTurnRouter
}

func startEinoPermissionForTest(
	t *testing.T,
	server *Server,
	messages store.Store,
	id identity,
	project *aiv1alpha1.Project,
	prompt string,
	finalContent string,
	calls ...chatStreamingCall,
) projectAssistantPermissionFixture {
	t.Helper()
	if strings.TrimSpace(finalContent) == "" {
		finalContent = "Approval completed."
	}
	model := &repositoryFlowEinoChatModel{Steps: []repositoryFlowEinoModelStep{
		{Message: einoschema.AssistantMessage("", projectEinoToolCallsFromStreamingForTest(calls))},
		{Message: einoschema.AssistantMessage(finalContent, nil)},
	}}
	setProjectAssistantModelForTest(server, model)

	settings := projectLLMSettings{Provider: defaultProjectLLMProvider, BaseURL: defaultProjectLLMBaseURL, Model: "test-model", APIKey: "test-key"}
	client := asclient.NewFromDynamic(projectSettingsDynamicClient{secret: projectLLMSettingsSecret(settings)})
	messageScope := projectMessageScope(id.orgUUID, id.workspaceUUID, project.Name)
	if strings.TrimSpace(prompt) == "" {
		prompt = "approve tool"
	}
	if err := appendProjectUserMessage(context.Background(), messages, messageScope, prompt); err != nil {
		t.Fatalf("appendProjectUserMessage returned error: %v", err)
	}
	var permission projectAssistantPermission
	var checkpoint projectAssistantCheckpoint
	_, err := server.generateProjectAssistantStream(
		httptest.NewRequest(http.MethodPost, "/", nil),
		id,
		client,
		project,
		projectAssistantStreamCallbacks{
			OnAssistantEvent: func(event projectAssistantEvent) {
				switch event.Type {
				case projectAssistantEventPermissionNeeded:
					if event.Permission != nil {
						permission = *event.Permission
					}
				case projectAssistantEventCheckpointSaved:
					if event.Checkpoint != nil {
						checkpoint = *event.Checkpoint
					}
				}
			},
		},
	)
	var permissionErr *projectAssistantPermissionRequiredError
	if !errors.As(err, &permissionErr) {
		t.Fatalf("generateProjectAssistantStream error = %v, want permission required", err)
	}
	if permission.ID == "" || checkpoint.ID == "" {
		t.Fatalf("permission=%#v checkpoint=%#v, want captured Eino permission events", permission, checkpoint)
	}
	return projectAssistantPermissionFixture{
		Client:        client,
		PermissionErr: permissionErr,
		Permission:    permission,
		Checkpoint:    checkpoint,
		LLMRequests:   &model.Inputs,
	}
}

func TestResumeProjectAssistantRunApprovesPendingTool(t *testing.T) {
	messages := store.NewMemoryStore()
	workspaces := workspace.NewFileStore(t.TempDir())
	server := NewWithWorkspace(nil, messages, workspaces, "", false)
	project := projectWithRepository("demo-repo", "demo", "github")
	project.Name = "demo"
	id := identity{tenantPath: "root:org-a:ws-1", clusterID: "cluster-ws-1", orgUUID: "org-a", workspaceUUID: "ws-1"}
	messageScope := projectMessageScope(id.orgUUID, id.workspaceUUID, project.Name)
	workspaceScope := projectWorkspaceScope(id, project.Name)
	call := chatStreamingCall{Index: 0, ID: "call-write", Type: "function"}
	call.Function.Name = projectToolWriteFile
	call.Function.Arguments = `{"path":"src/App.tsx","content":"approved\n"}`
	fixture := startEinoPermissionForTest(t, server, messages, id, project, "write src/app", "I wrote src/App.tsx.", call)
	permissionErr := fixture.PermissionErr
	permission := fixture.Permission
	checkpoint := fixture.Checkpoint
	assistantMessageID := "msg-assistant"
	if err := appendProjectAssistantMessage(context.Background(), messages, messageScope, assistantMessageID, "", projectAssistantMessageMetadata(projectMessageStatusPendingPermission, []projectToolCallStreamEvent{{
		ID:         call.ID,
		Name:       call.Function.Name,
		Status:     "permission_required",
		Arguments:  "path src/App.tsx, 9 bytes",
		Summary:    permission.Reason,
		Permission: &permission,
		Checkpoint: &checkpoint,
	}})); err != nil {
		t.Fatalf("appendProjectAssistantMessage returned error: %v", err)
	}

	resp, err := server.resumeProjectAssistantRunWithRepositoryAndClient(
		context.Background(),
		httptest.NewRequest(http.MethodPost, "/", nil),
		id,
		fixture.Client,
		project,
		&ProjectRepositoryView{Ref: "demo-repo", Name: "demo", Status: projectRepositoryStatusReady},
		permissionErr.RunID,
		projectAssistantResumeRequest{
			RequestID:          permissionErr.RequestID,
			Decision:           string(projectAssistantPermissionAllow),
			AssistantMessageID: assistantMessageID,
		},
	)
	if err != nil {
		t.Fatalf("resumeProjectAssistantRun returned error: %v", err)
	}
	if resp.Status != store.AssistantRunStatusCompleted || resp.ToolCall == nil || resp.ToolCall.Status != "succeeded" {
		t.Fatalf("resume response = %#v, want completed succeeded tool call", resp)
	}
	read, err := workspaces.ReadFile(context.Background(), workspaceScope, workspace.ReadOptions{Path: "src/App.tsx"})
	if err != nil {
		t.Fatalf("ReadFile returned error: %v", err)
	}
	if read.Content != "approved\n" {
		t.Fatalf("content = %q, want approved write", read.Content)
	}
	run, err := messages.GetAssistantRun(context.Background(), messageScope, permissionErr.RunID)
	if err != nil {
		t.Fatalf("GetAssistantRun returned error: %v", err)
	}
	if run.Status != store.AssistantRunStatusCompleted {
		t.Fatalf("run status = %q, want completed", run.Status)
	}
	audit := decodeProjectAssistantRunAudit(t, run.Audit)
	if len(audit.Decisions) != 1 || audit.Decisions[0].Decision != projectAssistantPermissionAllow || audit.Decisions[0].Actor != id.user || audit.Decisions[0].Result == "" {
		t.Fatalf("audit = %#v, want allow decision with actor and result", audit)
	}
	updatedMessage, err := server.findProjectMessage(context.Background(), messageScope, assistantMessageID)
	if err != nil {
		t.Fatalf("findProjectMessage returned error: %v", err)
	}
	if _, ok := updatedMessage.Metadata[projectMessageMetadataStatus]; ok {
		t.Fatalf("assistant metadata = %#v, want pending status cleared", updatedMessage.Metadata)
	}
	if _, ok := updatedMessage.Metadata["toolCalls"]; ok {
		t.Fatalf("assistant metadata = %#v, should not persist raw toolCalls", updatedMessage.Metadata)
	}
	updatedActions := projectAssistantUIActionsFromMetadata(updatedMessage.Metadata[projectMessageMetadataAssistantActions])
	if len(updatedActions) != 1 || updatedActions[0].Status != "succeeded" {
		t.Fatalf("updated actions = %#v, want persisted succeeded action", updatedActions)
	}
	if interrupt := projectAssistantUIInterruptFromMetadata(updatedMessage.Metadata[projectMessageMetadataAssistantInterrupt]); interrupt != nil {
		t.Fatalf("assistant interrupt = %#v, want cleared after approval", interrupt)
	}
}

func TestResumeProjectAssistantRunIgnoresStaleAssistantMessageID(t *testing.T) {
	messages := store.NewMemoryStore()
	workspaces := workspace.NewFileStore(t.TempDir())
	server := NewWithWorkspace(nil, messages, workspaces, "", false)
	project := projectWithRepository("demo-repo", "demo", "github")
	project.Name = "demo"
	id := identity{tenantPath: "root:org-a:ws-1", clusterID: "cluster-ws-1", orgUUID: "org-a", workspaceUUID: "ws-1"}
	workspaceScope := projectWorkspaceScope(id, project.Name)
	call := chatStreamingCall{Index: 0, ID: "call-write", Type: "function"}
	call.Function.Name = projectToolWriteFile
	call.Function.Arguments = `{"path":"src/App.tsx","content":"approved\n"}`
	fixture := startEinoPermissionForTest(t, server, messages, id, project, "write src/app", "I wrote src/App.tsx.", call)
	permissionErr := fixture.PermissionErr

	resp, err := server.resumeProjectAssistantRunWithRepositoryAndClient(
		context.Background(),
		httptest.NewRequest(http.MethodPost, "/", nil),
		id,
		fixture.Client,
		project,
		&ProjectRepositoryView{Ref: "demo-repo", Name: "demo", Status: projectRepositoryStatusReady},
		permissionErr.RunID,
		projectAssistantResumeRequest{
			RequestID:          permissionErr.RequestID,
			Decision:           string(projectAssistantPermissionAllow),
			AssistantMessageID: "missing-assistant-message",
		},
	)
	if err != nil {
		t.Fatalf("resumeProjectAssistantRun returned error: %v", err)
	}
	if resp.Status != store.AssistantRunStatusCompleted || resp.ToolCall == nil || resp.ToolCall.Status != "succeeded" {
		t.Fatalf("resume response = %#v, want completed succeeded tool call", resp)
	}
	read, err := workspaces.ReadFile(context.Background(), workspaceScope, workspace.ReadOptions{Path: "src/App.tsx"})
	if err != nil {
		t.Fatalf("ReadFile returned error: %v", err)
	}
	if read.Content != "approved\n" {
		t.Fatalf("content = %q, want approved write", read.Content)
	}
}

func TestResumeProjectAssistantRunIgnoresMismatchedAssistantMessageID(t *testing.T) {
	messages := store.NewMemoryStore()
	workspaces := workspace.NewFileStore(t.TempDir())
	server := NewWithWorkspace(nil, messages, workspaces, "", false)
	project := projectWithRepository("demo-repo", "demo", "github")
	project.Name = "demo"
	id := identity{tenantPath: "root:org-a:ws-1", clusterID: "cluster-ws-1", orgUUID: "org-a", workspaceUUID: "ws-1"}
	messageScope := projectMessageScope(id.orgUUID, id.workspaceUUID, project.Name)
	workspaceScope := projectWorkspaceScope(id, project.Name)
	call := chatStreamingCall{Index: 0, ID: "call-write", Type: "function"}
	call.Function.Name = projectToolWriteFile
	call.Function.Arguments = `{"path":"src/App.tsx","content":"approved\n"}`
	fixture := startEinoPermissionForTest(t, server, messages, id, project, "write src/app", "I wrote src/App.tsx.", call)
	permissionErr := fixture.PermissionErr
	otherMessageID := "msg-other-assistant"
	otherPermission := projectAssistantPermission{ID: "perm-other", ToolCallID: "call-other", ToolName: projectToolWriteFile, Reason: "other permission"}
	otherCheckpoint := projectAssistantCheckpoint{ID: "run-other", Reason: "waiting_for_permission"}
	otherMetadata := projectAssistantMessageMetadata(projectMessageStatusPendingPermission, []projectToolCallStreamEvent{{
		ID:         "call-other",
		Name:       projectToolWriteFile,
		Status:     "permission_required",
		Summary:    otherPermission.Reason,
		Permission: &otherPermission,
		Checkpoint: &otherCheckpoint,
	}})
	if err := appendProjectAssistantMessage(context.Background(), messages, messageScope, otherMessageID, "other pending message", otherMetadata); err != nil {
		t.Fatalf("appendProjectAssistantMessage returned error: %v", err)
	}

	resp, err := server.resumeProjectAssistantRunWithRepositoryAndClient(
		context.Background(),
		httptest.NewRequest(http.MethodPost, "/", nil),
		id,
		fixture.Client,
		project,
		&ProjectRepositoryView{Ref: "demo-repo", Name: "demo", Status: projectRepositoryStatusReady},
		permissionErr.RunID,
		projectAssistantResumeRequest{
			RequestID:          permissionErr.RequestID,
			Decision:           string(projectAssistantPermissionAllow),
			AssistantMessageID: otherMessageID,
		},
	)
	if err != nil {
		t.Fatalf("resumeProjectAssistantRun returned error: %v", err)
	}
	if resp.Status != store.AssistantRunStatusCompleted || resp.ToolCall == nil || resp.ToolCall.Status != "succeeded" {
		t.Fatalf("resume response = %#v, want completed succeeded tool call", resp)
	}
	read, err := workspaces.ReadFile(context.Background(), workspaceScope, workspace.ReadOptions{Path: "src/App.tsx"})
	if err != nil {
		t.Fatalf("ReadFile returned error: %v", err)
	}
	if read.Content != "approved\n" {
		t.Fatalf("content = %q, want approved write", read.Content)
	}
	otherMessage, err := server.findProjectMessage(context.Background(), messageScope, otherMessageID)
	if err != nil {
		t.Fatalf("findProjectMessage returned error: %v", err)
	}
	if otherMessage.Metadata[projectMessageMetadataStatus] != projectMessageStatusPendingPermission {
		t.Fatalf("other message metadata = %#v, want pending status unchanged", otherMessage.Metadata)
	}
	actions := projectAssistantUIActionsFromMetadata(otherMessage.Metadata[projectMessageMetadataAssistantActions])
	if len(actions) != 1 || actions[0].ID != "call-other" || actions[0].Status != "awaiting_approval" {
		t.Fatalf("other message actions = %#v, want unrelated pending action unchanged", actions)
	}
	interrupt := projectAssistantUIInterruptFromMetadata(otherMessage.Metadata[projectMessageMetadataAssistantInterrupt])
	if interrupt == nil || interrupt.Action == nil || interrupt.Action.RunID != "run-other" {
		t.Fatalf("other message interrupt = %#v, want unrelated pending interrupt unchanged", interrupt)
	}
}

func TestResumeProjectAssistantRunDeniesPendingToolAndUpdatesMessage(t *testing.T) {
	messages := store.NewMemoryStore()
	server := NewWithWorkspace(nil, messages, workspace.NewFileStore(t.TempDir()), "", false)
	project := projectWithRepository("demo-repo", "demo", "github")
	project.Name = "demo"
	id := identity{tenantPath: "root:org-a:ws-1", clusterID: "cluster-ws-1", orgUUID: "org-a", workspaceUUID: "ws-1"}
	messageScope := projectMessageScope(id.orgUUID, id.workspaceUUID, project.Name)
	call := chatStreamingCall{Index: 0, ID: "call-write", Type: "function"}
	call.Function.Name = projectToolWriteFile
	call.Function.Arguments = `{"path":"src/App.tsx","content":"denied\n"}`
	fixture := startEinoPermissionForTest(t, server, messages, id, project, "write src/app", "Denied the write.", call)
	permissionErr := fixture.PermissionErr
	permission := fixture.Permission
	checkpoint := fixture.Checkpoint
	assistantMessageID := "msg-assistant-denied"
	if err := appendProjectAssistantMessage(context.Background(), messages, messageScope, assistantMessageID, "", projectAssistantMessageMetadata(projectMessageStatusPendingPermission, []projectToolCallStreamEvent{{
		ID:         call.ID,
		Name:       call.Function.Name,
		Status:     "permission_required",
		Arguments:  "path src/App.tsx, 7 bytes",
		Summary:    permission.Reason,
		Permission: &permission,
		Checkpoint: &checkpoint,
	}})); err != nil {
		t.Fatalf("appendProjectAssistantMessage returned error: %v", err)
	}

	resp, err := server.resumeProjectAssistantRunWithRepositoryAndClient(
		context.Background(),
		httptest.NewRequest(http.MethodPost, "/", nil),
		id,
		fixture.Client,
		project,
		&ProjectRepositoryView{Ref: "demo-repo", Name: "demo", Status: projectRepositoryStatusReady},
		permissionErr.RunID,
		projectAssistantResumeRequest{
			RequestID:          permissionErr.RequestID,
			Decision:           string(projectAssistantPermissionDeny),
			AssistantMessageID: assistantMessageID,
		},
	)
	if err != nil {
		t.Fatalf("resumeProjectAssistantRun returned error: %v", err)
	}
	if resp.Status != store.AssistantRunStatusCompleted || resp.ToolCall == nil || resp.ToolCall.Status != "rejected" {
		t.Fatalf("resume response = %#v, want completed rejected tool call", resp)
	}
	updatedMessage, err := server.findProjectMessage(context.Background(), messageScope, assistantMessageID)
	if err != nil {
		t.Fatalf("findProjectMessage returned error: %v", err)
	}
	if _, ok := updatedMessage.Metadata[projectMessageMetadataStatus]; ok {
		t.Fatalf("assistant metadata = %#v, want pending status cleared", updatedMessage.Metadata)
	}
	if _, ok := updatedMessage.Metadata["toolCalls"]; ok {
		t.Fatalf("assistant metadata = %#v, should not persist raw toolCalls", updatedMessage.Metadata)
	}
	updatedActions := projectAssistantUIActionsFromMetadata(updatedMessage.Metadata[projectMessageMetadataAssistantActions])
	if len(updatedActions) != 1 || updatedActions[0].Status != "rejected" {
		t.Fatalf("updated actions = %#v, want persisted rejected action", updatedActions)
	}
	if interrupt := projectAssistantUIInterruptFromMetadata(updatedMessage.Metadata[projectMessageMetadataAssistantInterrupt]); interrupt != nil {
		t.Fatalf("assistant interrupt = %#v, want cleared after denial", interrupt)
	}
}

func TestResumeProjectAssistantRunAnswersFollowUpAndUpdatesMessage(t *testing.T) {
	settings := projectLLMSettings{Provider: defaultProjectLLMProvider, BaseURL: defaultProjectLLMBaseURL, Model: "test-model", APIKey: "test-key"}
	client := asclient.NewFromDynamic(projectSettingsDynamicClient{secret: projectLLMSettingsSecret(settings)})
	messages := store.NewMemoryStore()
	server := NewWithWorkspace(nil, messages, workspace.NewFileStore(t.TempDir()), "", false)
	model := &repositoryFlowEinoChatModel{Steps: []repositoryFlowEinoModelStep{
		{Message: einoschema.AssistantMessage("", []einoschema.ToolCall{{
			ID:   "call-follow-up",
			Type: "function",
			Function: einoschema.FunctionCall{
				Name:      projectToolAskFollowUp,
				Arguments: `{"questions":["What audience should this app target?"]}`,
			},
		}})},
		{Message: einoschema.AssistantMessage("Thanks, I can build that.", nil)},
	}}
	setProjectAssistantModelForTest(server, model)
	project := projectWithRepository("demo-repo", "demo", "github")
	project.Name = "demo"
	id := identity{tenantPath: "root:org-a:ws-1", clusterID: "cluster-ws-1", orgUUID: "org-a", workspaceUUID: "ws-1", user: "user@example.com"}
	messageScope := projectMessageScope(id.orgUUID, id.workspaceUUID, project.Name)
	if err := appendProjectUserMessage(context.Background(), messages, messageScope, "build an app"); err != nil {
		t.Fatalf("appendProjectUserMessage returned error: %v", err)
	}
	var followUp projectAssistantFollowUp
	var checkpoint projectAssistantCheckpoint
	_, err := server.generateProjectAssistantStream(
		httptest.NewRequest(http.MethodPost, "/", nil),
		id,
		client,
		project,
		projectAssistantStreamCallbacks{
			OnAssistantEvent: func(event projectAssistantEvent) {
				switch event.Type {
				case projectAssistantEventInputNeeded:
					if event.FollowUp != nil {
						followUp = *event.FollowUp
					}
				case projectAssistantEventCheckpointSaved:
					if event.Checkpoint != nil {
						checkpoint = *event.Checkpoint
					}
				}
			},
		},
	)
	var inputErr *projectAssistantInputRequiredError
	if !errors.As(err, &inputErr) {
		t.Fatalf("generateProjectAssistantStream error = %v, want input required", err)
	}
	if inputErr.RunID == "" || inputErr.RequestID == "" || followUp.ID == "" || checkpoint.ID == "" {
		t.Fatalf("input error=%#v followUp=%#v checkpoint=%#v, want resumable follow-up", inputErr, followUp, checkpoint)
	}
	assistantMessageID := "msg-assistant-follow-up"
	if err := appendProjectAssistantMessage(context.Background(), messages, messageScope, assistantMessageID, "", projectAssistantMessageMetadata(projectMessageStatusPendingInput, []projectToolCallStreamEvent{{
		ID:         followUp.ToolCallID,
		Name:       projectToolAskFollowUp,
		Status:     "input_required",
		Summary:    followUp.Prompt,
		FollowUp:   &followUp,
		Checkpoint: &checkpoint,
	}})); err != nil {
		t.Fatalf("appendProjectAssistantMessage returned error: %v", err)
	}
	pendingMsg, err := server.findProjectMessage(context.Background(), messageScope, assistantMessageID)
	if err != nil {
		t.Fatalf("GetMessage returned error: %v", err)
	}
	if got := projectAssistantUIInterruptFromMetadata(pendingMsg.Metadata[projectMessageMetadataAssistantInterrupt]); got == nil || got.Kind != "follow_up" || got.Status != "pending" {
		t.Fatalf("pending interrupt = %#v, want pending follow-up", got)
	}

	resp, err := server.resumeProjectAssistantRunWithRepositoryAndClient(
		context.Background(),
		httptest.NewRequest(http.MethodPost, "/", nil),
		id,
		client,
		project,
		&ProjectRepositoryView{Ref: "demo-repo", Name: "demo", Status: projectRepositoryStatusReady},
		inputErr.RunID,
		projectAssistantResumeRequest{
			RequestID:          inputErr.RequestID,
			Answer:             "Solo founders.",
			AssistantMessageID: assistantMessageID,
		},
	)
	if err != nil {
		t.Fatalf("resumeProjectAssistantRun returned error: %v", err)
	}
	if resp.Status != store.AssistantRunStatusCompleted || resp.AssistantMessage == nil || resp.AssistantMessage.Content != "Thanks, I can build that." {
		t.Fatalf("resume response = %#v, want completed assistant continuation", resp)
	}
	updatedMsg, err := server.findProjectMessage(context.Background(), messageScope, assistantMessageID)
	if err != nil {
		t.Fatalf("GetMessage after resume returned error: %v", err)
	}
	if interrupt := projectAssistantUIInterruptFromMetadata(updatedMsg.Metadata[projectMessageMetadataAssistantInterrupt]); interrupt != nil {
		t.Fatalf("updated interrupt = %#v, want resolved follow-up removed", interrupt)
	}
	run, err := messages.GetAssistantRun(context.Background(), messageScope, inputErr.RunID)
	if err != nil {
		t.Fatalf("GetAssistantRun returned error: %v", err)
	}
	if run.Status != store.AssistantRunStatusCompleted {
		t.Fatalf("run status = %q, want completed", run.Status)
	}
}

func TestResumeProjectAssistantRunRejectsEmptyFollowUpBeforeClaimingRun(t *testing.T) {
	settings := projectLLMSettings{Provider: defaultProjectLLMProvider, BaseURL: defaultProjectLLMBaseURL, Model: "test-model", APIKey: "test-key"}
	client := asclient.NewFromDynamic(projectSettingsDynamicClient{secret: projectLLMSettingsSecret(settings)})
	messages := store.NewMemoryStore()
	server := NewWithWorkspace(nil, messages, workspace.NewFileStore(t.TempDir()), "", false)
	model := &repositoryFlowEinoChatModel{Steps: []repositoryFlowEinoModelStep{
		{Message: einoschema.AssistantMessage("", []einoschema.ToolCall{{
			ID:   "call-follow-up",
			Type: "function",
			Function: einoschema.FunctionCall{
				Name:      projectToolAskFollowUp,
				Arguments: `{"questions":["What audience should this app target?"]}`,
			},
		}})},
	}}
	setProjectAssistantModelForTest(server, model)
	project := projectWithRepository("demo-repo", "demo", "github")
	project.Name = "demo"
	id := identity{tenantPath: "root:org-a:ws-1", clusterID: "cluster-ws-1", orgUUID: "org-a", workspaceUUID: "ws-1", user: "user@example.com"}
	messageScope := projectMessageScope(id.orgUUID, id.workspaceUUID, project.Name)
	if err := appendProjectUserMessage(context.Background(), messages, messageScope, "build an app"); err != nil {
		t.Fatalf("appendProjectUserMessage returned error: %v", err)
	}
	_, err := server.generateProjectAssistantStream(
		httptest.NewRequest(http.MethodPost, "/", nil),
		id,
		client,
		project,
		projectAssistantStreamCallbacks{},
	)
	var inputErr *projectAssistantInputRequiredError
	if !errors.As(err, &inputErr) {
		t.Fatalf("generateProjectAssistantStream error = %v, want input required", err)
	}

	_, err = server.resumeProjectAssistantRunWithRepositoryAndClient(
		context.Background(),
		httptest.NewRequest(http.MethodPost, "/", nil),
		id,
		client,
		project,
		&ProjectRepositoryView{Ref: "demo-repo", Name: "demo", Status: projectRepositoryStatusReady},
		inputErr.RunID,
		projectAssistantResumeRequest{
			RequestID: inputErr.RequestID,
			Answer:    "   ",
		},
	)
	if err == nil || !strings.Contains(err.Error(), "answer is required") {
		t.Fatalf("resumeProjectAssistantRun error = %v, want answer required", err)
	}
	run, err := messages.GetAssistantRun(context.Background(), messageScope, inputErr.RunID)
	if err != nil {
		t.Fatalf("GetAssistantRun returned error: %v", err)
	}
	if run.Status != store.AssistantRunStatusPendingInput {
		t.Fatalf("run status = %q, want pending input", run.Status)
	}
}

func TestResumeProjectAssistantRunClearsStaleFollowUpMessageWhenRunAlreadyClaimed(t *testing.T) {
	settings := projectLLMSettings{Provider: defaultProjectLLMProvider, BaseURL: defaultProjectLLMBaseURL, Model: "test-model", APIKey: "test-key"}
	client := asclient.NewFromDynamic(projectSettingsDynamicClient{secret: projectLLMSettingsSecret(settings)})
	messages := store.NewMemoryStore()
	server := NewWithWorkspace(nil, messages, workspace.NewFileStore(t.TempDir()), "", false)
	project := projectWithRepository("demo-repo", "demo", "github")
	project.Name = "demo"
	id := identity{tenantPath: "root:org-a:ws-1", clusterID: "cluster-ws-1", orgUUID: "org-a", workspaceUUID: "ws-1", user: "user@example.com"}
	messageScope := projectMessageScope(id.orgUUID, id.workspaceUUID, project.Name)
	runID := "run-stale-follow-up"
	requestID := "input-stale-follow-up"
	assistantMessageID := "msg-stale-follow-up"

	checkpointState := projectAssistantCheckpointState{
		Eino: &projectAssistantEinoCheckpointState{
			CheckpointID:  runID,
			Checkpoint:    []byte("checkpoint"),
			InterruptID:   requestID,
			InterruptType: projectAssistantInterruptTypeFollowUp,
			ToolCallID:    "call-follow-up",
			ToolName:      projectToolAskFollowUp,
		},
	}
	rawCheckpoint, err := json.Marshal(checkpointState)
	if err != nil {
		t.Fatalf("marshal checkpoint returned error: %v", err)
	}
	if err := messages.SaveAssistantRun(context.Background(), messageScope, store.AssistantRun{
		ID:         runID,
		Status:     store.AssistantRunStatusRunning,
		RequestID:  requestID,
		Checkpoint: rawCheckpoint,
	}); err != nil {
		t.Fatalf("SaveAssistantRun returned error: %v", err)
	}
	followUp := projectAssistantFollowUp{
		ID:         requestID,
		ToolCallID: "call-follow-up",
		Questions:  []string{"What audience should this app target?"},
		Prompt:     "I need one detail before continuing.",
	}
	checkpoint := projectAssistantCheckpoint{ID: runID, Reason: "waiting_for_input"}
	if err := appendProjectAssistantMessage(context.Background(), messages, messageScope, assistantMessageID, "", projectAssistantMessageMetadata(projectMessageStatusPendingInput, []projectToolCallStreamEvent{{
		ID:         followUp.ToolCallID,
		Name:       projectToolAskFollowUp,
		Status:     "input_required",
		Summary:    followUp.Prompt,
		FollowUp:   &followUp,
		Checkpoint: &checkpoint,
	}})); err != nil {
		t.Fatalf("appendProjectAssistantMessage returned error: %v", err)
	}
	pendingMsg, err := server.findProjectMessage(context.Background(), messageScope, assistantMessageID)
	if err != nil {
		t.Fatalf("findProjectMessage returned error: %v", err)
	}
	if got := projectAssistantUIInterruptFromMetadata(pendingMsg.Metadata[projectMessageMetadataAssistantInterrupt]); got == nil || got.Kind != "follow_up" || got.Status != "pending" {
		t.Fatalf("pending interrupt = %#v, want pending follow-up", got)
	}

	_, err = server.resumeProjectAssistantRunWithRepositoryAndClient(
		context.Background(),
		httptest.NewRequest(http.MethodPost, "/", nil),
		id,
		client,
		project,
		&ProjectRepositoryView{Ref: "demo-repo", Name: "demo", Status: projectRepositoryStatusReady},
		runID,
		projectAssistantResumeRequest{
			RequestID:          requestID,
			Answer:             "Solo founders.",
			AssistantMessageID: assistantMessageID,
		},
	)
	if err == nil || !strings.Contains(err.Error(), "not waiting") {
		t.Fatalf("resumeProjectAssistantRun error = %v, want not waiting", err)
	}
	updatedMsg, err := server.findProjectMessage(context.Background(), messageScope, assistantMessageID)
	if err != nil {
		t.Fatalf("findProjectMessage after resume returned error: %v", err)
	}
	if _, ok := updatedMsg.Metadata[projectMessageMetadataStatus]; ok {
		t.Fatalf("assistant metadata = %#v, want pending status cleared", updatedMsg.Metadata)
	}
	if interrupt := projectAssistantUIInterruptFromMetadata(updatedMsg.Metadata[projectMessageMetadataAssistantInterrupt]); interrupt != nil {
		t.Fatalf("assistant interrupt = %#v, want cleared stale follow-up", interrupt)
	}
	run, err := messages.GetAssistantRun(context.Background(), messageScope, runID)
	if err != nil {
		t.Fatalf("GetAssistantRun returned error: %v", err)
	}
	if run.Status != store.AssistantRunStatusRunning {
		t.Fatalf("run status = %q, want running", run.Status)
	}
}

func TestResumeProjectAssistantRunCompletesRunWhenContinuationLLMFailsAfterTool(t *testing.T) {
	settings := projectLLMSettings{Provider: defaultProjectLLMProvider, BaseURL: defaultProjectLLMBaseURL, Model: "test-model", APIKey: "test-key"}
	client := asclient.NewFromDynamic(projectSettingsDynamicClient{secret: projectLLMSettingsSecret(settings)})
	messages := store.NewMemoryStore()
	workspaces := workspace.NewFileStore(t.TempDir())
	server := NewWithWorkspace(nil, messages, workspaces, "", false)
	model := &repositoryFlowEinoChatModel{Steps: []repositoryFlowEinoModelStep{
		{Message: einoschema.AssistantMessage("", []einoschema.ToolCall{{
			ID:   "call-write",
			Type: "function",
			Function: einoschema.FunctionCall{
				Name:      projectToolWriteFile,
				Arguments: `{"path":"src/App.tsx","content":"approved\n"}`,
			},
		}})},
		{Err: errors.New("continuation model failed")},
	}}
	setProjectAssistantModelForTest(server, model)
	project := projectWithRepository("demo-repo", "demo", "github")
	project.Name = "demo"
	id := identity{tenantPath: "root:org-a:ws-1", clusterID: "cluster-ws-1", orgUUID: "org-a", workspaceUUID: "ws-1", user: "user@example.com"}
	messageScope := projectMessageScope(id.orgUUID, id.workspaceUUID, project.Name)
	if err := appendProjectUserMessage(context.Background(), messages, messageScope, "write src/app"); err != nil {
		t.Fatalf("appendProjectUserMessage returned error: %v", err)
	}
	_, err := server.generateProjectAssistantStream(
		httptest.NewRequest(http.MethodPost, "/", nil),
		id,
		client,
		project,
		projectAssistantStreamCallbacks{},
	)
	var permissionErr *projectAssistantPermissionRequiredError
	if !errors.As(err, &permissionErr) {
		t.Fatalf("generateProjectAssistantStream error = %v, want permission required", err)
	}

	_, err = server.resumeProjectAssistantRunWithRepositoryAndClient(
		context.Background(),
		httptest.NewRequest(http.MethodPost, "/", nil),
		id,
		client,
		project,
		&ProjectRepositoryView{Ref: "demo-repo", Name: "demo", Status: projectRepositoryStatusReady},
		permissionErr.RunID,
		projectAssistantResumeRequest{RequestID: permissionErr.RequestID, Decision: string(projectAssistantPermissionAllow)},
	)
	if err == nil || !strings.Contains(err.Error(), "continuation model failed") {
		t.Fatalf("resumeProjectAssistantRun error = %v, want continuation decode failure", err)
	}
	read, err := workspaces.ReadFile(context.Background(), projectWorkspaceScope(id, project.Name), workspace.ReadOptions{Path: "src/App.tsx"})
	if err != nil {
		t.Fatalf("ReadFile returned error: %v", err)
	}
	if read.Content != "approved\n" {
		t.Fatalf("content = %q, want approved write before continuation failure", read.Content)
	}
	run, err := messages.GetAssistantRun(context.Background(), messageScope, permissionErr.RunID)
	if err != nil {
		t.Fatalf("GetAssistantRun returned error: %v", err)
	}
	if run.Status != store.AssistantRunStatusCompleted {
		t.Fatalf("run status = %q, want completed after continuation failure", run.Status)
	}
	audit := decodeProjectAssistantRunAudit(t, run.Audit)
	if len(audit.Decisions) != 1 {
		t.Fatalf("audit = %#v, want one approval decision", audit)
	}
	decision := audit.Decisions[0]
	if decision.Decision != projectAssistantPermissionAllow || decision.Actor != id.user || decision.ToolName != projectToolWriteFile || !strings.Contains(decision.Error, "continuation model failed") {
		t.Fatalf("audit decision = %#v, want approved write with continuation failure", decision)
	}
}

func TestAbortProjectAssistantRunMarksPendingRunAborted(t *testing.T) {
	for _, status := range []store.AssistantRunStatus{
		store.AssistantRunStatusPendingPermission,
		store.AssistantRunStatusPendingInput,
	} {
		t.Run(string(status), func(t *testing.T) {
			messages := store.NewMemoryStore()
			server := NewWithWorkspace(nil, messages, workspace.NewFileStore(t.TempDir()), "", false)
			project := projectWithRepository("demo-repo", "demo", "github")
			project.Name = "demo"
			id := identity{tenantPath: "root:org-a:ws-1", clusterID: "cluster-ws-1", orgUUID: "org-a", workspaceUUID: "ws-1"}
			messageScope := projectMessageScope(id.orgUUID, id.workspaceUUID, project.Name)
			run := store.AssistantRun{
				ID:         "run-1",
				Status:     status,
				RequestID:  "req-1",
				Checkpoint: json.RawMessage(`{"toolCall":{"id":"call-1"}}`),
			}
			if err := messages.SaveAssistantRun(context.Background(), messageScope, run); err != nil {
				t.Fatalf("SaveAssistantRun returned error: %v", err)
			}

			resp, err := server.abortProjectAssistantRun(context.Background(), id, project, "run-1")
			if err != nil {
				t.Fatalf("abortProjectAssistantRun returned error: %v", err)
			}
			if resp.Status != store.AssistantRunStatusAborted {
				t.Fatalf("abort response = %#v, want aborted", resp)
			}
			got, err := messages.GetAssistantRun(context.Background(), messageScope, "run-1")
			if err != nil {
				t.Fatalf("GetAssistantRun returned error: %v", err)
			}
			if got.Status != store.AssistantRunStatusAborted {
				t.Fatalf("run status = %q, want aborted", got.Status)
			}
			audit := decodeProjectAssistantRunAudit(t, got.Audit)
			if len(audit.Decisions) != 1 || audit.Decisions[0].Decision != projectAssistantPermissionDeny || audit.Decisions[0].Error != "aborted by user" {
				t.Fatalf("audit = %#v, want abort decision", audit)
			}
		})
	}
}

func TestAbortProjectAssistantRunClearsPendingAssistantMessageMetadata(t *testing.T) {
	messages := store.NewMemoryStore()
	server := NewWithWorkspace(nil, messages, workspace.NewFileStore(t.TempDir()), "", false)
	project := projectWithRepository("demo-repo", "demo", "github")
	project.Name = "demo"
	id := identity{tenantPath: "root:org-a:ws-1", clusterID: "cluster-ws-1", orgUUID: "org-a", workspaceUUID: "ws-1"}
	messageScope := projectMessageScope(id.orgUUID, id.workspaceUUID, project.Name)
	permission := projectAssistantPermission{
		ID:       "req-1",
		ToolName: projectToolWriteFile,
		Reason:   "Write src/App.tsx",
	}
	checkpoint := projectAssistantCheckpoint{ID: "run-1", Reason: "waiting_for_permission"}
	run := store.AssistantRun{
		ID:         "run-1",
		Status:     store.AssistantRunStatusPendingPermission,
		RequestID:  "req-1",
		Checkpoint: json.RawMessage(`{"toolCall":{"id":"call-1"}}`),
	}
	if err := messages.SaveAssistantRun(context.Background(), messageScope, run); err != nil {
		t.Fatalf("SaveAssistantRun returned error: %v", err)
	}
	assistantMessageID := "msg-pending-abort"
	if err := appendProjectAssistantMessage(context.Background(), messages, messageScope, assistantMessageID, "", projectAssistantMessageMetadata(projectMessageStatusPendingPermission, []projectToolCallStreamEvent{{
		ID:         "call-1",
		Name:       projectToolWriteFile,
		Status:     "permission_required",
		Summary:    permission.Reason,
		Permission: &permission,
		Checkpoint: &checkpoint,
	}})); err != nil {
		t.Fatalf("appendProjectAssistantMessage returned error: %v", err)
	}

	resp, err := server.abortProjectAssistantRun(context.Background(), id, project, "run-1")
	if err != nil {
		t.Fatalf("abortProjectAssistantRun returned error: %v", err)
	}
	if resp.Status != store.AssistantRunStatusAborted {
		t.Fatalf("abort response = %#v, want aborted", resp)
	}
	updatedMessage, err := server.findProjectMessage(context.Background(), messageScope, assistantMessageID)
	if err != nil {
		t.Fatalf("findProjectMessage returned error: %v", err)
	}
	if _, ok := updatedMessage.Metadata[projectMessageMetadataStatus]; ok {
		t.Fatalf("assistant metadata = %#v, want pending status cleared", updatedMessage.Metadata)
	}
	if interrupt := projectAssistantUIInterruptFromMetadata(updatedMessage.Metadata[projectMessageMetadataAssistantInterrupt]); interrupt != nil {
		t.Fatalf("assistant interrupt = %#v, want cleared after abort", interrupt)
	}
}

func TestResumeProjectAssistantRunClaimsBeforeCommitSideEffects(t *testing.T) {
	var sourceCommitCalls atomic.Int32
	var buildConfigCommitCalls atomic.Int32
	commitEntered := make(chan struct{})
	releaseCommit := make(chan struct{})
	mcp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var envelope struct {
			Method string `json:"method"`
			Params struct {
				Name      string `json:"name"`
				Arguments struct {
					Message string `json:"message"`
				} `json:"arguments"`
			} `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&envelope); err != nil {
			t.Fatalf("decode MCP request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		switch envelope.Method {
		case "tools/call":
			if envelope.Params.Name != "code__commit_files" {
				t.Fatalf("unexpected MCP tool call: %#v", envelope)
			}
			switch envelope.Params.Arguments.Message {
			case "Initial app":
				if sourceCommitCalls.Add(1) == 1 {
					close(commitEntered)
				}
				<-releaseCommit
				fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"result":{"structuredContent":{"phase":"Succeeded","files":["src/App.tsx"],"commitSHA":"abcdef1234567890"}}}`)
			case projectBuildConfigCommitMessage:
				buildConfigCommitCalls.Add(1)
				fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"result":{"structuredContent":{"phase":"Succeeded","files":[".kedge/build.json",".github/workflows/kedge-app-studio-build.yml"],"commitSHA":"buildabcdef123456"}}}`)
			default:
				t.Fatalf("unexpected commit message: %#v", envelope.Params.Arguments)
			}
		default:
			fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"result":{"tools":[{"name":"code__commit_files","description":"commit files"}]}}`)
		}
	}))
	defer mcp.Close()
	releasedCommit := false
	defer func() {
		if !releasedCommit {
			close(releaseCommit)
		}
	}()

	messages := store.NewMemoryStore()
	workspaces := workspace.NewFileStore(t.TempDir())
	server := NewWithWorkspace(nil, messages, workspaces, mcp.URL, false)
	project := projectWithRepository("demo-repo", "demo", "github")
	project.Name = "demo"
	id := identity{tenantPath: "root:org-a:ws-1", clusterID: "cluster-ws-1", orgUUID: "org-a", workspaceUUID: "ws-1", user: "user@example.com"}
	workspaceScope := projectWorkspaceScope(id, project.Name)
	if err := workspaces.ApplyFiles(context.Background(), workspaceScope, []workspace.File{
		{Path: "package.json", Content: `{"scripts":{"build":"vite build"}}` + "\n"},
		{Path: "src/App.tsx", Content: "approved\n"},
	}); err != nil {
		t.Fatalf("ApplyFiles returned error: %v", err)
	}
	call := chatStreamingCall{Index: 0, ID: "call-commit", Type: "function"}
	call.Function.Name = projectToolCommitProjectFiles
	call.Function.Arguments = `{"repositoryRef":"demo-repo","paths":["src/App.tsx"],"message":"Initial app"}`
	fixture := startEinoPermissionForTest(t, server, messages, id, project, "commit files", "Committed files.", call)
	permissionErr := fixture.PermissionErr

	firstErr := make(chan error, 1)
	go func() {
		_, err := server.resumeProjectAssistantRunWithRepositoryAndClient(
			context.Background(),
			httptest.NewRequest(http.MethodPost, "/", nil),
			id,
			fixture.Client,
			project,
			&ProjectRepositoryView{Ref: "demo-repo", Name: "demo", Status: projectRepositoryStatusReady},
			permissionErr.RunID,
			projectAssistantResumeRequest{RequestID: permissionErr.RequestID, Decision: string(projectAssistantPermissionAllow)},
		)
		firstErr <- err
	}()
	select {
	case <-commitEntered:
	case <-time.After(3 * time.Second):
		t.Fatal("first resume did not reach source commit call")
	}

	_, err := server.resumeProjectAssistantRunWithRepositoryAndClient(
		context.Background(),
		httptest.NewRequest(http.MethodPost, "/", nil),
		id,
		fixture.Client,
		project,
		&ProjectRepositoryView{Ref: "demo-repo", Name: "demo", Status: projectRepositoryStatusReady},
		permissionErr.RunID,
		projectAssistantResumeRequest{RequestID: permissionErr.RequestID, Decision: string(projectAssistantPermissionAllow)},
	)
	if err == nil {
		t.Fatal("second resume returned nil error")
	}
	close(releaseCommit)
	releasedCommit = true
	if err := <-firstErr; err != nil {
		t.Fatalf("first resume returned error: %v", err)
	}
	if got := sourceCommitCalls.Load(); got != 1 {
		t.Fatalf("source commit call count = %d, want 1", got)
	}
	if got := buildConfigCommitCalls.Load(); got != 1 {
		t.Fatalf("build config commit call count = %d, want 1", got)
	}
}

func TestResumeProjectAssistantRunPersistsAssistantTextBeforeNextPause(t *testing.T) {
	for _, tt := range []struct {
		name      string
		staleID   bool
		wantFresh bool
	}{
		{name: "valid assistant message", staleID: false, wantFresh: false},
		{name: "stale assistant message", staleID: true, wantFresh: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			messages := store.NewMemoryStore()
			workspaces := workspace.NewFileStore(t.TempDir())
			server := NewWithWorkspace(nil, messages, workspaces, "", false)
			project := projectWithRepository("demo-repo", "demo", "github")
			project.Name = "demo"
			id := identity{tenantPath: "root:org-a:ws-1", clusterID: "cluster-ws-1", orgUUID: "org-a", workspaceUUID: "ws-1"}
			messageScope := projectMessageScope(id.orgUUID, id.workspaceUUID, project.Name)
			firstCall := chatStreamingCall{Index: 0, ID: "call-first-write", Type: "function"}
			firstCall.Function.Name = projectToolWriteFile
			firstCall.Function.Arguments = `{"path":"src/App.tsx","content":"first\n"}`
			// Approving the first write grants every write until the next commit,
			// so the next pause has to come from a tool that still always asks.
			secondCall := chatStreamingCall{Index: 0, ID: "call-second-runtime", Type: "function"}
			secondCall.Function.Name = projectToolDeployProjectRuntime
			secondCall.Function.Arguments = `{"targetRef":"runtime-a","image":"ghcr.io/demo/app:latest","port":8080,"intent":"preview"}`
			model := &repositoryFlowEinoChatModel{Steps: []repositoryFlowEinoModelStep{
				{Message: einoschema.AssistantMessage("", projectEinoToolCallsFromStreamingForTest([]chatStreamingCall{firstCall}))},
				{Message: einoschema.AssistantMessage("First change applied. ", projectEinoToolCallsFromStreamingForTest([]chatStreamingCall{secondCall}))},
			}}
			setProjectAssistantModelForTest(server, model)
			settings := projectLLMSettings{Provider: defaultProjectLLMProvider, BaseURL: defaultProjectLLMBaseURL, Model: "test-model", APIKey: "test-key"}
			client := asclient.NewFromDynamic(projectSettingsDynamicClient{secret: projectLLMSettingsSecret(settings)})
			if err := appendProjectUserMessage(context.Background(), messages, messageScope, "write files"); err != nil {
				t.Fatalf("appendProjectUserMessage returned error: %v", err)
			}
			var firstPermission projectAssistantPermission
			var firstCheckpoint projectAssistantCheckpoint
			_, err := server.generateProjectAssistantStream(
				httptest.NewRequest(http.MethodPost, "/", nil),
				id,
				client,
				project,
				projectAssistantStreamCallbacks{
					OnAssistantEvent: func(event projectAssistantEvent) {
						switch event.Type {
						case projectAssistantEventPermissionNeeded:
							if event.Permission != nil {
								firstPermission = *event.Permission
							}
						case projectAssistantEventCheckpointSaved:
							if event.Checkpoint != nil {
								firstCheckpoint = *event.Checkpoint
							}
						}
					},
				},
			)
			var permissionErr *projectAssistantPermissionRequiredError
			if !errors.As(err, &permissionErr) {
				t.Fatalf("generateProjectAssistantStream error = %v, want permission required", err)
			}
			assistantMessageID := "msg-assistant-two-step"
			if err := appendProjectAssistantMessage(context.Background(), messages, messageScope, assistantMessageID, "", projectAssistantMessageMetadata(projectMessageStatusPendingPermission, []projectToolCallStreamEvent{{
				ID:         firstCall.ID,
				Name:       firstCall.Function.Name,
				Status:     "permission_required",
				Summary:    firstPermission.Reason,
				Permission: &firstPermission,
				Checkpoint: &firstCheckpoint,
			}})); err != nil {
				t.Fatalf("appendProjectAssistantMessage returned error: %v", err)
			}
			resumeAssistantMessageID := assistantMessageID
			if tt.staleID {
				resumeAssistantMessageID = "missing-assistant-message"
			}

			resp, err := server.resumeProjectAssistantRunWithRepositoryAndClient(
				context.Background(),
				httptest.NewRequest(http.MethodPost, "/", nil),
				id,
				client,
				project,
				&ProjectRepositoryView{Ref: "demo-repo", Name: "demo", Status: projectRepositoryStatusReady},
				permissionErr.RunID,
				projectAssistantResumeRequest{
					RequestID:          permissionErr.RequestID,
					Decision:           string(projectAssistantPermissionAllow),
					AssistantMessageID: resumeAssistantMessageID,
				},
			)
			if err != nil {
				t.Fatalf("resumeProjectAssistantRun returned error: %v", err)
			}
			if resp.Status != store.AssistantRunStatusPendingPermission || resp.AssistantMessage == nil || resp.AssistantMessage.Content != "First change applied. " {
				t.Fatalf("resume response = %#v, want pending permission with preserved assistant text", resp)
			}
			updatedMessage, err := server.findProjectMessage(context.Background(), messageScope, resp.AssistantMessage.ID)
			if err != nil {
				t.Fatalf("findProjectMessage returned error: %v", err)
			}
			if updatedMessage.Content != "First change applied. " {
				t.Fatalf("assistant content = %q, want preserved resumed text", updatedMessage.Content)
			}
			if tt.wantFresh && updatedMessage.ID == assistantMessageID {
				t.Fatalf("assistant message id = %q, want fresh message for stale resume id", updatedMessage.ID)
			}
			if !tt.wantFresh && updatedMessage.ID != assistantMessageID {
				t.Fatalf("assistant message id = %q, want existing message %q", updatedMessage.ID, assistantMessageID)
			}
			if interrupt := projectAssistantUIInterruptFromMetadata(updatedMessage.Metadata[projectMessageMetadataAssistantInterrupt]); interrupt == nil || interrupt.Status != "pending" || interrupt.Action == nil || interrupt.Action.RunID != resp.RunID {
				t.Fatalf("assistant interrupt = %#v, want pending next approval", interrupt)
			}
		})
	}
}

func TestResumeProjectAssistantRunAllowingWriteDoesNotRePromptLaterWrites(t *testing.T) {
	messages := store.NewMemoryStore()
	workspaces := workspace.NewFileStore(t.TempDir())
	server := NewWithWorkspace(nil, messages, workspaces, "", false)
	project := projectWithRepository("demo-repo", "demo", "github")
	project.Name = "demo"
	id := identity{tenantPath: "root:org-a:ws-1", orgUUID: "org-a", workspaceUUID: "ws-1"}
	messageScope := projectMessageScope(id.orgUUID, id.workspaceUUID, project.Name)
	workspaceScope := projectWorkspaceScope(id, project.Name)

	firstCall := chatStreamingCall{Index: 0, ID: "call-first-write", Type: "function"}
	firstCall.Function.Name = projectToolWriteFile
	firstCall.Function.Arguments = `{"path":"src/App.tsx","content":"first\n"}`
	secondCall := chatStreamingCall{Index: 0, ID: "call-second-write", Type: "function"}
	secondCall.Function.Name = projectToolWriteFile
	secondCall.Function.Arguments = `{"path":"src/Other.tsx","content":"second\n"}`
	model := &repositoryFlowEinoChatModel{Steps: []repositoryFlowEinoModelStep{
		{Message: einoschema.AssistantMessage("", projectEinoToolCallsFromStreamingForTest([]chatStreamingCall{firstCall}))},
		{Message: einoschema.AssistantMessage("Applied both changes. ", projectEinoToolCallsFromStreamingForTest([]chatStreamingCall{secondCall}))},
		{Message: einoschema.AssistantMessage("All done.", nil)},
	}}
	setProjectAssistantModelForTest(server, model)
	settings := projectLLMSettings{Provider: defaultProjectLLMProvider, BaseURL: defaultProjectLLMBaseURL, Model: "test-model", APIKey: "test-key"}
	client := asclient.NewFromDynamic(projectSettingsDynamicClient{secret: projectLLMSettingsSecret(settings)})
	if err := appendProjectUserMessage(context.Background(), messages, messageScope, "write files"); err != nil {
		t.Fatalf("appendProjectUserMessage returned error: %v", err)
	}

	_, err := server.generateProjectAssistantStream(
		httptest.NewRequest(http.MethodPost, "/", nil),
		id,
		client,
		project,
		projectAssistantStreamCallbacks{},
	)
	var permissionErr *projectAssistantPermissionRequiredError
	if !errors.As(err, &permissionErr) {
		t.Fatalf("generateProjectAssistantStream error = %v, want permission required", err)
	}

	resp, err := server.resumeProjectAssistantRunWithRepositoryAndClient(
		context.Background(),
		httptest.NewRequest(http.MethodPost, "/", nil),
		id,
		client,
		project,
		&ProjectRepositoryView{Ref: "demo-repo", Name: "demo", Status: projectRepositoryStatusReady},
		permissionErr.RunID,
		projectAssistantResumeRequest{
			RequestID: permissionErr.RequestID,
			Decision:  string(projectAssistantPermissionAllow),
		},
	)
	if err != nil {
		t.Fatalf("resumeProjectAssistantRun returned error: %v", err)
	}
	if resp.Status != store.AssistantRunStatusCompleted {
		t.Fatalf("resume status = %q, want %q (second write should auto-approve)", resp.Status, store.AssistantRunStatusCompleted)
	}

	files, err := workspaces.ListFiles(context.Background(), workspaceScope, workspace.ListOptions{})
	if err != nil {
		t.Fatalf("ListFiles returned error: %v", err)
	}
	written := map[string]bool{}
	for _, f := range files.Files {
		written[f.Path] = true
	}
	if !written["src/App.tsx"] || !written["src/Other.tsx"] {
		t.Fatalf("written files = %v, want both src/App.tsx and src/Other.tsx", written)
	}
}

func TestResumeProjectAssistantRunRejectsStaleRepositoryBinding(t *testing.T) {
	var sawCommit bool
	mcp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var envelope struct {
			Method string `json:"method"`
		}
		if err := json.NewDecoder(r.Body).Decode(&envelope); err != nil {
			t.Fatalf("decode MCP request: %v", err)
		}
		if envelope.Method == "tools/call" {
			sawCommit = true
		}
		switch envelope.Method {
		case "tools/call":
			fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"result":{"structuredContent":{"phase":"Succeeded","files":["src/App.tsx"],"commitSHA":"abcdef1234567890"}}}`)
		default:
			fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"result":{"tools":[{"name":"code__commit_files","description":"commit files"}]}}`)
		}
	}))
	defer mcp.Close()

	messages := store.NewMemoryStore()
	workspaces := workspace.NewFileStore(t.TempDir())
	server := NewWithWorkspace(nil, messages, workspaces, mcp.URL, false)
	project := projectWithRepository("old-repo", "demo", "github")
	project.Name = "demo"
	id := identity{tenantPath: "root:org-a:ws-1", clusterID: "cluster-ws-1", orgUUID: "org-a", workspaceUUID: "ws-1", user: "user@example.com"}
	messageScope := projectMessageScope(id.orgUUID, id.workspaceUUID, project.Name)
	workspaceScope := projectWorkspaceScope(id, project.Name)
	if err := workspaces.ApplyFiles(context.Background(), workspaceScope, []workspace.File{{Path: "src/App.tsx", Content: "approved\n"}}); err != nil {
		t.Fatalf("ApplyFiles returned error: %v", err)
	}
	call := chatStreamingCall{Index: 0, ID: "call-commit", Type: "function"}
	call.Function.Name = projectToolCommitProjectFiles
	call.Function.Arguments = `{"repositoryRef":"old-repo","paths":["src/App.tsx"],"message":"Initial app"}`
	fixture := startEinoPermissionForTest(t, server, messages, id, project, "commit files", "Committed files.", call)
	permissionErr := fixture.PermissionErr
	permission := fixture.Permission
	checkpoint := fixture.Checkpoint
	assistantMessageID := "msg-assistant-stale"
	if err := appendProjectAssistantMessage(context.Background(), messages, messageScope, assistantMessageID, "", projectAssistantMessageMetadata(projectMessageStatusPendingPermission, []projectToolCallStreamEvent{{
		ID:         call.ID,
		Name:       call.Function.Name,
		Status:     "permission_required",
		Arguments:  "repositoryRef old-repo, 1 file(s): src/App.tsx",
		Summary:    permission.Reason,
		Permission: &permission,
		Checkpoint: &checkpoint,
	}})); err != nil {
		t.Fatalf("appendProjectAssistantMessage returned error: %v", err)
	}
	project.Spec.Repository.RepositoryRef = "new-repo"

	_, err := server.resumeProjectAssistantRunWithRepositoryAndClient(
		context.Background(),
		httptest.NewRequest(http.MethodPost, "/", nil),
		id,
		fixture.Client,
		project,
		&ProjectRepositoryView{Ref: "new-repo", Name: "demo", Status: projectRepositoryStatusReady},
		permissionErr.RunID,
		projectAssistantResumeRequest{
			RequestID:          permissionErr.RequestID,
			Decision:           string(projectAssistantPermissionAllow),
			AssistantMessageID: assistantMessageID,
		},
	)
	if err == nil || !strings.Contains(err.Error(), "repository binding changed") {
		t.Fatalf("resumeProjectAssistantRun error = %v, want stale repository binding", err)
	}
	if sawCommit {
		t.Fatal("stale approval reached provider-code commit")
	}
	run, err := messages.GetAssistantRun(context.Background(), messageScope, permissionErr.RunID)
	if err != nil {
		t.Fatalf("GetAssistantRun returned error: %v", err)
	}
	if run.Status != store.AssistantRunStatusCompleted {
		t.Fatalf("run status = %q, want completed stale checkpoint", run.Status)
	}
	audit := decodeProjectAssistantRunAudit(t, run.Audit)
	if len(audit.Decisions) != 1 || !strings.Contains(audit.Decisions[0].Error, "repository binding changed") {
		t.Fatalf("audit = %#v, want stale binding error", audit)
	}
	updatedMessage, err := server.findProjectMessage(context.Background(), messageScope, assistantMessageID)
	if err != nil {
		t.Fatalf("findProjectMessage returned error: %v", err)
	}
	if _, ok := updatedMessage.Metadata[projectMessageMetadataStatus]; ok {
		t.Fatalf("assistant metadata = %#v, want pending status cleared", updatedMessage.Metadata)
	}
	if _, ok := updatedMessage.Metadata["toolCalls"]; ok {
		t.Fatalf("assistant metadata = %#v, should not persist raw toolCalls", updatedMessage.Metadata)
	}
	updatedActions := projectAssistantUIActionsFromMetadata(updatedMessage.Metadata[projectMessageMetadataAssistantActions])
	if len(updatedActions) != 1 || updatedActions[0].Status != "failed" {
		t.Fatalf("updated actions = %#v, want failed stale binding action", updatedActions)
	}
	// Failed tools disclose their reason: the user sees WHY the commit did
	// not happen instead of an opaque "Commit failed".
	if !strings.Contains(updatedActions[0].Detail, "repository binding changed") {
		t.Fatalf("updated action detail = %q, want the stale-binding reason disclosed", updatedActions[0].Detail)
	}
}

func TestResumeProjectAssistantRunPreemptsToolBatchAfterApprovedPermission(t *testing.T) {
	settings := projectLLMSettings{Provider: defaultProjectLLMProvider, BaseURL: defaultProjectLLMBaseURL, Model: "test-model", APIKey: "test-key"}
	client := asclient.NewFromDynamic(projectSettingsDynamicClient{secret: projectLLMSettingsSecret(settings)})
	messages := store.NewMemoryStore()
	id := identity{tenantPath: "root:org-a:ws-1", clusterID: "cluster-ws-1", orgUUID: "org-a", workspaceUUID: "ws-1", user: "user@example.com"}
	messageScope := projectMessageScope(id.orgUUID, id.workspaceUUID, "demo")
	if err := appendProjectUserMessage(context.Background(), messages, messageScope, "write files"); err != nil {
		t.Fatalf("appendProjectUserMessage returned error: %v", err)
	}
	workspaces := workspace.NewFileStore(t.TempDir())
	server := NewWithWorkspace(nil, messages, workspaces, "", false)
	setProjectAssistantModelForTest(server, &repositoryFlowEinoChatModel{Steps: []repositoryFlowEinoModelStep{
		{Message: einoschema.AssistantMessage("", []einoschema.ToolCall{
			{
				ID:   "call-one",
				Type: "function",
				Function: einoschema.FunctionCall{
					Name:      projectToolWriteFile,
					Arguments: `{"path":"one.txt","content":"one\n"}`,
				},
			},
			{
				ID:   "call-two",
				Type: "function",
				Function: einoschema.FunctionCall{
					Name:      projectToolWriteFile,
					Arguments: `{"path":"two.txt","content":"two\n"}`,
				},
			},
		})},
		{Message: einoschema.AssistantMessage("First approval completed.", nil)},
	}})
	project := projectWithRepository("demo-repo", "demo", "github")
	project.Name = "demo"

	_, err := server.generateProjectAssistantStream(
		httptest.NewRequest(http.MethodPost, "/", nil),
		id,
		client,
		project,
		projectAssistantStreamCallbacks{},
	)
	var permissionErr *projectAssistantPermissionRequiredError
	if !errors.As(err, &permissionErr) {
		t.Fatalf("generateProjectAssistantStream error = %v, want permission required", err)
	}
	first, err := server.resumeProjectAssistantRunWithRepositoryAndClient(
		context.Background(),
		httptest.NewRequest(http.MethodPost, "/", nil),
		id,
		client,
		project,
		&ProjectRepositoryView{Ref: "demo-repo", Name: "demo", Status: projectRepositoryStatusReady},
		permissionErr.RunID,
		projectAssistantResumeRequest{RequestID: permissionErr.RequestID, Decision: string(projectAssistantPermissionAllow)},
	)
	if err != nil {
		t.Fatalf("first resumeProjectAssistantRun returned error: %v", err)
	}
	if first.Status != store.AssistantRunStatusCompleted || first.Permission != nil {
		t.Fatalf("first resume response = %#v, want completed run after first approved Eino resume", first)
	}
	if _, err := workspaces.ReadFile(context.Background(), projectWorkspaceScope(id, project.Name), workspace.ReadOptions{Path: "one.txt"}); err != nil {
		t.Fatalf("one.txt was not written after first approval: %v", err)
	}
	if _, err := workspaces.ReadFile(context.Background(), projectWorkspaceScope(id, project.Name), workspace.ReadOptions{Path: "two.txt"}); err == nil {
		t.Fatal("two.txt was written after only the first approval")
	}
	run, err := messages.GetAssistantRun(context.Background(), messageScope, permissionErr.RunID)
	if err != nil {
		t.Fatalf("GetAssistantRun returned error: %v", err)
	}
	audit := decodeProjectAssistantRunAudit(t, run.Audit)
	if len(audit.Decisions) != 1 || audit.Decisions[0].ToolCallID != "call-one" {
		t.Fatalf("audit decisions = %#v, want one approval for the preempting tool", audit.Decisions)
	}
}

func TestResumeProjectAssistantRunContinuesLLMAfterApprovedPermission(t *testing.T) {
	settings := projectLLMSettings{Provider: defaultProjectLLMProvider, BaseURL: defaultProjectLLMBaseURL, Model: "test-model", APIKey: "test-key"}
	client := asclient.NewFromDynamic(projectSettingsDynamicClient{secret: projectLLMSettingsSecret(settings)})
	messages := store.NewMemoryStore()
	id := identity{tenantPath: "root:org-a:ws-1", clusterID: "cluster-ws-1", orgUUID: "org-a", workspaceUUID: "ws-1", user: "user@example.com"}
	messageScope := projectMessageScope(id.orgUUID, id.workspaceUUID, "demo")
	if err := appendProjectUserMessage(context.Background(), messages, messageScope, "write src/app"); err != nil {
		t.Fatalf("appendProjectUserMessage returned error: %v", err)
	}
	workspaces := workspace.NewFileStore(t.TempDir())
	server := NewWithWorkspace(nil, messages, workspaces, "", false)
	model := &repositoryFlowEinoChatModel{Steps: []repositoryFlowEinoModelStep{
		{Message: einoschema.AssistantMessage("", []einoschema.ToolCall{{
			ID:   "call-write",
			Type: "function",
			Function: einoschema.FunctionCall{
				Name:      projectToolWriteFile,
				Arguments: `{"path":"src/App.tsx","content":"approved\n"}`,
			},
		}})},
		{
			Message: einoschema.AssistantMessage("I wrote src/App.tsx after approval.", nil),
			Inspect: func(input []*einoschema.Message) {
				messages := projectEinoMessagesToChat(input)
				var sawAssistantCall, sawToolResult bool
				for _, msg := range messages {
					if msg.Role == aiv1alpha1.ProjectMessageRoleAssistant && len(msg.ToolCalls) == 1 && msg.ToolCalls[0].ID == "call-write" {
						sawAssistantCall = true
					}
					if msg.Role == "tool" && msg.ToolCallID == "call-write" && strings.Contains(msg.Content, "src/App.tsx") {
						sawToolResult = true
					}
				}
				if !sawAssistantCall || !sawToolResult {
					t.Fatalf("resume Eino messages = %#v, want approved tool call and result context", messages)
				}
			},
		},
	}}
	setProjectAssistantModelForTest(server, model)
	project := projectWithRepository("demo-repo", "demo", "github")
	project.Name = "demo"

	_, err := server.generateProjectAssistantStream(
		httptest.NewRequest(http.MethodPost, "/", nil),
		id,
		client,
		project,
		projectAssistantStreamCallbacks{},
	)
	var permissionErr *projectAssistantPermissionRequiredError
	if !errors.As(err, &permissionErr) {
		t.Fatalf("generateProjectAssistantStream error = %v, want permission required", err)
	}
	run, err := messages.GetAssistantRun(context.Background(), messageScope, permissionErr.RunID)
	if err != nil {
		t.Fatalf("GetAssistantRun returned error: %v", err)
	}
	var checkpoint projectAssistantCheckpointState
	if err := json.Unmarshal(run.Checkpoint, &checkpoint); err != nil {
		t.Fatalf("decode checkpoint returned error: %v", err)
	}
	if checkpoint.Eino == nil || len(checkpoint.Eino.Checkpoint) == 0 || checkpoint.Eino.InterruptID == "" {
		t.Fatalf("checkpoint eino state = %#v, want Eino checkpoint for resume", checkpoint.Eino)
	}
	checkpoint.ToolCalls = nil
	checkpoint.CurrentIndex = 0
	rawCheckpoint, err := json.Marshal(checkpoint)
	if err != nil {
		t.Fatalf("encode stripped checkpoint returned error: %v", err)
	}
	run.Checkpoint = rawCheckpoint
	if err := messages.SaveAssistantRun(context.Background(), messageScope, run); err != nil {
		t.Fatalf("SaveAssistantRun returned error: %v", err)
	}

	resp, err := server.resumeProjectAssistantRunWithRepositoryAndClient(
		context.Background(),
		httptest.NewRequest(http.MethodPost, "/", nil),
		id,
		client,
		project,
		&ProjectRepositoryView{Ref: "demo-repo", Name: "demo", Status: projectRepositoryStatusReady},
		permissionErr.RunID,
		projectAssistantResumeRequest{RequestID: permissionErr.RequestID, Decision: string(projectAssistantPermissionAllow)},
	)
	if err != nil {
		t.Fatalf("resumeProjectAssistantRunWithRepositoryAndClient returned error: %v", err)
	}
	if resp.Status != store.AssistantRunStatusCompleted {
		t.Fatalf("resume response = %#v, want completed", resp)
	}
	if resp.AssistantMessage == nil || resp.AssistantMessage.Content != "I wrote src/App.tsx after approval." {
		t.Fatalf("assistant message = %#v, want continuation response", resp.AssistantMessage)
	}
	if len(model.Inputs) != 2 {
		t.Fatalf("Eino model request count = %d, want initial request plus resumed continuation", len(model.Inputs))
	}
	read, err := workspaces.ReadFile(context.Background(), projectWorkspaceScope(id, project.Name), workspace.ReadOptions{Path: "src/App.tsx"})
	if err != nil {
		t.Fatalf("ReadFile returned error: %v", err)
	}
	if read.Content != "approved\n" {
		t.Fatalf("content = %q, want approved write", read.Content)
	}
	recent, err := messages.LoadRecentMessages(context.Background(), messageScope, 10)
	if err != nil {
		t.Fatalf("LoadRecentMessages returned error: %v", err)
	}
	var sawContinuation bool
	for _, msg := range recent {
		if msg.Role == aiv1alpha1.ProjectMessageRoleAssistant && msg.Content == "I wrote src/App.tsx after approval." {
			sawContinuation = true
		}
	}
	if !sawContinuation {
		t.Fatalf("messages = %#v, want persisted resumed assistant continuation", recent)
	}
}

func TestGenerateProjectAssistantStreamSynthesizesFinalAnswerAfterRepeatedToolLoop(t *testing.T) {
	reply, requests, err := runRepeatedReadFileAssistantStream(t, "I inspected src/App.tsx and can continue from the profile page context.")
	if err != nil {
		t.Fatalf("generateProjectAssistantStream returned error: %v", err)
	}
	if reply != "I inspected src/App.tsx and can continue from the profile page context." {
		t.Fatalf("reply = %q, want synthesized final answer", reply)
	}
	if strings.Contains(reply, "repeated the same action") || strings.Contains(reply, "Last action result") {
		t.Fatalf("reply = %q, should not expose internal loop fallback", reply)
	}
	if len(requests) != maxAssistantToolTurns+1 {
		t.Fatalf("LLM request count = %d, want %d", len(requests), maxAssistantToolTurns+1)
	}
	finalRequest := requests[len(requests)-1]
	if len(finalRequest.Tools) != 0 || finalRequest.ToolChoice != "none" {
		t.Fatalf("final Eino model request tools: tools=%d tool_choice=%q, want no-tool final answer request", len(finalRequest.Tools), finalRequest.ToolChoice)
	}
}

func TestGenerateProjectAssistantStreamFallsBackWhenFinalNoToolAnswerIsEmpty(t *testing.T) {
	reply, requests, err := runRepeatedReadFileAssistantStream(t, "")
	if err != nil {
		t.Fatalf("generateProjectAssistantStream returned error: %v", err)
	}
	if strings.Contains(reply, "repeated the same action") || strings.Contains(reply, "Last action result") {
		t.Fatalf("reply = %q, should not expose internal loop fallback", reply)
	}
	if !strings.Contains(reply, "I inspected") || !strings.Contains(reply, "src/App.tsx") {
		t.Fatalf("reply = %q, want user-facing fallback from tool result", reply)
	}
	if len(requests) != maxAssistantToolTurns+1 {
		t.Fatalf("LLM request count = %d, want %d", len(requests), maxAssistantToolTurns+1)
	}
}

func TestGenerateProjectAssistantStreamFallsBackWhenFinalToolLimitResponseHasOnlyToolCalls(t *testing.T) {
	reply, requests, err := runUniqueReadFileAssistantStream(t, "I inspected the requested files and can continue from the latest one.")
	if err != nil {
		t.Fatalf("generateProjectAssistantStream returned error: %v", err)
	}
	if reply != "I inspected the requested files and can continue from the latest one." {
		t.Fatalf("reply = %q, want synthesized final answer", reply)
	}
	if len(requests) != maxAssistantToolTurns+1 {
		t.Fatalf("LLM request count = %d, want %d", len(requests), maxAssistantToolTurns+1)
	}
	finalRequest := requests[len(requests)-1]
	if len(finalRequest.Tools) != 0 || finalRequest.ToolChoice != "none" {
		t.Fatalf("final Eino model request tools: tools=%d tool_choice=%q, want no-tool final answer request", len(finalRequest.Tools), finalRequest.ToolChoice)
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

func TestGenerateProjectAssistantStreamRequestsPermissionForCommitProjectFiles(t *testing.T) {
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

	model := &repositoryFlowEinoChatModel{Steps: []repositoryFlowEinoModelStep{{
		Message: einoschema.AssistantMessage("", []einoschema.ToolCall{{
			ID:   "call-commit",
			Type: "function",
			Function: einoschema.FunctionCall{
				Name:      projectToolCommitProjectFiles,
				Arguments: `{"repositoryRef":"demo-repo","paths":["index.html"],"message":"Initial app"}`,
			},
		}}),
	}}}
	_, requests, err := runProjectAssistantStreamWithModel(t, model, mcp.URL)
	var permissionErr *projectAssistantPermissionRequiredError
	if !errors.As(err, &permissionErr) {
		t.Fatalf("generateProjectAssistantStream error = %v, want permission required", err)
	}
	if permissionErr.ToolName != "commit_project_files" {
		t.Fatalf("permission error = %#v, want commit_project_files", permissionErr)
	}
	if commitCalls != 0 {
		t.Fatalf("commit call count = %d, want no commit before approval", commitCalls)
	}
	if len(requests) != 1 {
		t.Fatalf("LLM request count = %d, want 1", len(requests))
	}
}

func TestCommitProjectWorkspaceFilesReportsProviderFailure(t *testing.T) {
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
		if envelope.Method != "tools/call" {
			t.Fatalf("unexpected MCP request method %q", envelope.Method)
		}
		commitCalls++
		if envelope.Params.Name != "code__commit_files" {
			t.Fatalf("unexpected MCP tool call: %#v", envelope)
		}
		fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"result":{"isError":true,"content":[{"type":"text","text":"RepositoryCommit failed: bundle not found"}]}}`)
	}))
	defer mcp.Close()

	workspaces := workspace.NewFileStore(t.TempDir())
	scope := workspace.Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: "demo"}
	if err := workspaces.ApplyFiles(context.Background(), scope, []workspace.File{{Path: "index.html", Content: "hello\n"}}); err != nil {
		t.Fatalf("ApplyFiles returned error: %v", err)
	}
	server := NewWithWorkspace(nil, nil, workspaces, mcp.URL, false)
	_, err := server.commitProjectWorkspaceFiles(
		context.Background(),
		identity{tenantPath: "root:org-a:ws-1", clusterID: "cluster-ws-1", orgUUID: "org-a", workspaceUUID: "ws-1"},
		scope,
		"demo-repo",
		mcp.URL,
		httptest.NewRequest(http.MethodPost, "/", nil),
		map[string]any{"repositoryRef": "demo-repo", "paths": []any{"index.html"}, "message": "Initial app"},
	)
	if err == nil || !strings.Contains(err.Error(), "bundle not found") {
		t.Fatalf("commitProjectWorkspaceFiles error = %v, want commit failure", err)
	}
	if commitCalls != 1 {
		t.Fatalf("commit call count = %d, want 1", commitCalls)
	}
}

func TestCommitProjectWorkspaceFilesRejectsRepositoryMismatch(t *testing.T) {
	var sawCommit bool
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
			sawCommit = true
			fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"result":{"structuredContent":{"phase":"Succeeded","files":["index.html"],"commitSHA":"abcdef1234567890"}}}`)
		default:
			t.Fatalf("unexpected MCP request method %q", envelope.Method)
		}
	}))
	defer mcp.Close()

	workspaces := workspace.NewFileStore(t.TempDir())
	scope := workspace.Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: "demo"}
	if err := workspaces.ApplyFiles(context.Background(), scope, []workspace.File{{Path: "index.html", Content: "hello\n"}}); err != nil {
		t.Fatalf("ApplyFiles returned error: %v", err)
	}
	server := NewWithWorkspace(nil, nil, workspaces, mcp.URL, false)
	_, err := server.commitProjectWorkspaceFiles(
		context.Background(),
		identity{tenantPath: "root:org-a:ws-1", clusterID: "cluster-ws-1", orgUUID: "org-a", workspaceUUID: "ws-1"},
		scope,
		"demo-repo",
		mcp.URL,
		httptest.NewRequest(http.MethodPost, "/", nil),
		map[string]any{"repositoryRef": "other-repo", "paths": []any{"index.html"}, "message": "Initial app"},
	)
	if sawCommit {
		t.Fatal("commit_project_files reached provider-code for a repository outside the Project binding")
	}
	if err == nil || !strings.Contains(err.Error(), "does not match this Project") {
		t.Fatalf("commitProjectWorkspaceFiles error = %v, want deterministic repository mismatch failure", err)
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

func runRepeatedReadFileAssistantStream(t *testing.T, finalAnswer string) (string, []chatCompletionRequest, error) {
	t.Helper()
	steps := make([]repositoryFlowEinoModelStep, 0, maxAssistantToolTurns+1)
	for i := 1; i <= maxAssistantToolTurns; i++ {
		steps = append(steps, repositoryFlowEinoModelStep{Message: einoschema.AssistantMessage("", []einoschema.ToolCall{{
			ID:   fmt.Sprintf("call-%d", i),
			Type: "function",
			Function: einoschema.FunctionCall{
				Name:      projectToolReadProjectFile,
				Arguments: `{"path":"src/App.tsx"}`,
			},
		}})})
	}
	steps = append(steps, repositoryFlowEinoModelStep{Message: einoschema.AssistantMessage(finalAnswer, nil)})
	model := &repositoryFlowEinoChatModel{Steps: steps}
	return runProjectAssistantStreamWithModel(t, model, "")
}

func runUniqueReadFileAssistantStream(t *testing.T, finalAnswer string) (string, []chatCompletionRequest, error) {
	t.Helper()
	steps := make([]repositoryFlowEinoModelStep, 0, maxAssistantToolTurns+1)
	for i := 1; i <= maxAssistantToolTurns; i++ {
		steps = append(steps, repositoryFlowEinoModelStep{Message: einoschema.AssistantMessage("", []einoschema.ToolCall{{
			ID:   fmt.Sprintf("call-%d", i),
			Type: "function",
			Function: einoschema.FunctionCall{
				Name:      projectToolReadProjectFile,
				Arguments: fmt.Sprintf(`{"path":"src/file-%d.tsx"}`, i),
			},
		}})})
	}
	steps = append(steps, repositoryFlowEinoModelStep{Message: einoschema.AssistantMessage(finalAnswer, nil)})
	model := &repositoryFlowEinoChatModel{Steps: steps}
	return runProjectAssistantStreamWithModel(t, model, "")
}

func runProjectAssistantStreamWithModel(t *testing.T, model *repositoryFlowEinoChatModel, hubBase string) (string, []chatCompletionRequest, error) {
	t.Helper()

	settings := projectLLMSettings{
		Provider: defaultProjectLLMProvider,
		BaseURL:  defaultProjectLLMBaseURL,
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
	seedFiles := []workspace.File{{Path: "src/App.tsx", Content: "export default function App() { return <main>Hello</main> }\n"}}
	for i := 1; i <= maxAssistantToolTurns; i++ {
		seedFiles = append(seedFiles, workspace.File{
			Path:    fmt.Sprintf("src/file-%d.tsx", i),
			Content: fmt.Sprintf("export const value%d = %d\n", i, i),
		})
	}
	if err := workspaces.ApplyFiles(context.Background(), workspace.Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: "demo"}, seedFiles); err != nil {
		t.Fatalf("seed workspace files: %v", err)
	}
	server := NewWithWorkspace(nil, messages, workspaces, hubBase, false)
	setProjectAssistantModelForTest(server, model)
	project := projectWithRepository("demo-repo", "demo", "github")
	project.Name = "demo"
	project.Spec.DisplayName = "Demo"

	reply, err := server.generateProjectAssistantStream(
		httptest.NewRequest(http.MethodPost, "/", nil),
		identity{tenantPath: "root:org-a:ws-1", clusterID: "cluster-ws-1", orgUUID: "org-a", workspaceUUID: "ws-1"},
		client,
		project,
		projectAssistantStreamCallbacks{},
	)
	return reply, model.Inputs, err
}

func decodeProjectAssistantRunAudit(t *testing.T, raw []byte) projectAssistantRunAudit {
	t.Helper()
	var audit projectAssistantRunAudit
	if err := json.Unmarshal(raw, &audit); err != nil {
		t.Fatalf("decode assistant run audit: %v", err)
	}
	return audit
}

func TestProjectRepeatedToolLoopFallbackSummarizesLastToolResult(t *testing.T) {
	got := projectRepeatedToolLoopFallback([]chatMessage{{
		Role:    "tool",
		Name:    "write_file",
		Content: `{"operation":"write_file","path":"src/App.tsx","size":12}`,
	}})
	for _, want := range []string{"latest project tool result", "write_file", "src/App.tsx", "12 bytes"} {
		if !strings.Contains(got, want) {
			t.Fatalf("fallback = %q, want %q", got, want)
		}
	}
	for _, unwanted := range []string{"repeated the same action", "Last action result", "completed action"} {
		if strings.Contains(got, unwanted) {
			t.Fatalf("fallback = %q, should not contain %q", got, unwanted)
		}
	}
}

func TestProjectToolLoopFallbackDoesNotAskForManualContinuation(t *testing.T) {
	got := projectToolLoopFallback([]chatMessage{{
		Role:    "tool",
		Name:    "write_file",
		Content: `{"operation":"write_file","path":"postcss.config.js","size":80}`,
	}}, "kept requesting actions")
	for _, want := range []string{"latest project tool result", "write_file", "postcss.config.js", "80 bytes"} {
		if !strings.Contains(got, want) {
			t.Fatalf("fallback = %q, want %q", got, want)
		}
	}
	for _, unwanted := range []string{"Please ask me to continue", "I stopped because", "hit the per-turn action limit", "Last action result"} {
		if strings.Contains(got, unwanted) {
			t.Fatalf("fallback = %q, should not contain %q", got, unwanted)
		}
	}
}

type projectSettingsDynamicClient struct {
	secret *unstructured.Unstructured
}

func (c projectSettingsDynamicClient) Resource(gvr k8sschema.GroupVersionResource) dynamic.NamespaceableResourceInterface {
	return projectSettingsDynamicResource{gvr: gvr, secret: c.secret}
}

type projectSettingsDynamicResource struct {
	dynamic.ResourceInterface
	gvr       k8sschema.GroupVersionResource
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
	return nil, apierrors.NewNotFound(k8sschema.GroupResource{Group: r.gvr.Group, Resource: r.gvr.Resource}, name)
}

func TestCommitProjectWorkspaceFilesCommitsThroughCodeProvider(t *testing.T) {
	var calls []struct {
		message string
		files   []struct {
			Path    string `json:"path"`
			Content string `json:"content"`
		}
	}
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
		if envelope.Params.Arguments.RepositoryRef != "demo-repo" {
			t.Fatalf("unexpected commit args: %#v", envelope.Params.Arguments)
		}
		calls = append(calls, struct {
			message string
			files   []struct {
				Path    string `json:"path"`
				Content string `json:"content"`
			}
		}{message: envelope.Params.Arguments.Message, files: envelope.Params.Arguments.Files})
		switch len(calls) {
		case 1:
			if envelope.Params.Arguments.Message != "Update app" {
				t.Fatalf("first commit message = %q, want Update app", envelope.Params.Arguments.Message)
			}
			if len(envelope.Params.Arguments.Files) != 1 || envelope.Params.Arguments.Files[0].Path != "src/App.tsx" || envelope.Params.Arguments.Files[0].Content != "committed from workspace\n" {
				t.Fatalf("unexpected source commit files: %#v", envelope.Params.Arguments.Files)
			}
			fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"result":{"structuredContent":{"phase":"Succeeded","files":["src/App.tsx"],"commitSHA":"abcdef1234567890"}}}`)
		case 2:
			if envelope.Params.Arguments.Message != projectBuildConfigCommitMessage {
				t.Fatalf("second commit message = %q, want build config message", envelope.Params.Arguments.Message)
			}
			if len(envelope.Params.Arguments.Files) != 2 {
				t.Fatalf("build config commit files = %#v, want build config and workflow", envelope.Params.Arguments.Files)
			}
			seen := map[string]string{}
			for _, f := range envelope.Params.Arguments.Files {
				seen[f.Path] = f.Content
			}
			if !strings.Contains(seen[projectBuildConfigPath], `"builder": "railpack"`) {
				t.Fatalf("build config content = %q, want railpack builder", seen[projectBuildConfigPath])
			}
			if !strings.Contains(seen[projectBuildWorkflowPath], projectBuildRailpackAction) {
				t.Fatalf("workflow content = %q, want railpack action", seen[projectBuildWorkflowPath])
			}
			fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"result":{"structuredContent":{"phase":"Succeeded","files":[".kedge/build.json",".github/workflows/kedge-app-studio-build.yml"],"commitSHA":"buildabcdef123456"}}}`)
		default:
			t.Fatalf("unexpected extra commit call: %#v", envelope.Params.Arguments)
		}
	}))
	defer mcp.Close()

	workspaces := workspace.NewFileStore(t.TempDir())
	scope := workspace.Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: "demo"}
	if err := workspaces.ApplyFiles(context.Background(), scope, []workspace.File{
		{Path: "package.json", Content: `{"scripts":{"build":"vite build"}}` + "\n"},
		{Path: "src/App.tsx", Content: "committed from workspace\n"},
	}); err != nil {
		t.Fatalf("ApplyFiles returned error: %v", err)
	}
	server := NewWithWorkspace(nil, nil, workspaces, mcp.URL, false)

	resp, err := server.commitProjectWorkspaceFiles(
		context.Background(),
		identity{tenantPath: "root:org-a:ws-1", clusterID: "cluster-ws-1", orgUUID: "org-a", workspaceUUID: "ws-1"},
		scope,
		"demo-repo",
		mcp.URL,
		httptest.NewRequest(http.MethodPost, "/", nil),
		map[string]any{
			"repositoryRef": "demo-repo",
			"message":       "Update app",
			"paths":         []any{"src/App.tsx", "src//App.tsx"},
		},
	)
	if err != nil {
		t.Fatalf("commitProjectWorkspaceFiles returned error: %v", err)
	}
	if len(calls) != 2 {
		t.Fatalf("MCP commit calls = %d, want source commit plus build config commit", len(calls))
	}
	if !strings.Contains(resp, "abcdef1234567890") || !strings.Contains(resp, "buildConfiguration") || !strings.Contains(resp, "buildabcdef123456") {
		t.Fatalf("tool response = %s, want source commit and build configuration evidence", resp)
	}
	decoded := map[string]any{}
	if err := json.Unmarshal([]byte(resp), &decoded); err != nil {
		t.Fatalf("decode tool response: %v", err)
	}
	if decoded["commitSHA"] != "abcdef1234567890" {
		t.Fatalf("commitSHA = %#v, want original source commit SHA preserved at top level", decoded["commitSHA"])
	}
	if _, ok := decoded["commitResult"]; ok {
		t.Fatalf("tool response includes nested commitResult = %#v, want original commit response shape preserved", decoded["commitResult"])
	}
	read, err := workspaces.ReadFile(context.Background(), scope, workspace.ReadOptions{Path: projectBuildWorkflowPath, MaxBytes: workspace.MaxWriteBytes})
	if err != nil {
		t.Fatalf("generated workflow read returned error: %v", err)
	}
	if !strings.Contains(read.Content, "Railpack") {
		t.Fatalf("generated workflow = %q, want Railpack workflow", read.Content)
	}
}

func TestCommitProjectWorkspaceFilesSkipsBuildConfigCommitWhenCurrent(t *testing.T) {
	var commitCalls int
	mcp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var envelope struct {
			Method string `json:"method"`
			Params struct {
				Name      string `json:"name"`
				Arguments struct {
					Message string `json:"message"`
				} `json:"arguments"`
			} `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&envelope); err != nil {
			t.Fatalf("decode MCP request: %v", err)
		}
		if envelope.Method != "tools/call" || envelope.Params.Name != "code__commit_files" {
			t.Fatalf("unexpected MCP request: %#v", envelope)
		}
		if envelope.Params.Arguments.Message == projectBuildConfigCommitMessage {
			t.Fatal("build config was committed even though managed files were current")
		}
		commitCalls++
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"result":{"structuredContent":{"phase":"Succeeded","files":["src/App.tsx"],"commitSHA":"abcdef1234567890"}}}`)
	}))
	defer mcp.Close()

	workspaces := workspace.NewFileStore(t.TempDir())
	scope := workspace.Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: "demo"}
	files := []workspace.File{
		{Path: "package.json", Content: `{"scripts":{"build":"vite build"}}` + "\n"},
		{Path: "src/App.tsx", Content: "committed from workspace\n"},
	}
	files = append(files, projectManagedBuildFiles("node")...)
	if err := workspaces.ApplyFiles(context.Background(), scope, files); err != nil {
		t.Fatalf("ApplyFiles returned error: %v", err)
	}
	server := NewWithWorkspace(nil, nil, workspaces, mcp.URL, false)

	resp, err := server.commitProjectWorkspaceFiles(
		context.Background(),
		identity{tenantPath: "root:org-a:ws-1", clusterID: "cluster-ws-1", orgUUID: "org-a", workspaceUUID: "ws-1"},
		scope,
		"demo-repo",
		mcp.URL,
		httptest.NewRequest(http.MethodPost, "/", nil),
		map[string]any{
			"repositoryRef": "demo-repo",
			"message":       "Update app",
			"paths":         []any{"src/App.tsx"},
		},
	)
	if err != nil {
		t.Fatalf("commitProjectWorkspaceFiles returned error: %v", err)
	}
	if commitCalls != 1 {
		t.Fatalf("MCP commit calls = %d, want only the source commit", commitCalls)
	}
	if !strings.Contains(resp, `"status":"current"`) {
		t.Fatalf("tool response = %s, want current build configuration status", resp)
	}
}

func TestCommitProjectWorkspaceFilesRetriesBuildConfigWhenBuildCommitFails(t *testing.T) {
	var buildCommitCalls int
	mcp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var envelope struct {
			Method string `json:"method"`
			Params struct {
				Name      string `json:"name"`
				Arguments struct {
					Message string `json:"message"`
				} `json:"arguments"`
			} `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&envelope); err != nil {
			t.Fatalf("decode MCP request: %v", err)
		}
		if envelope.Method != "tools/call" || envelope.Params.Name != "code__commit_files" {
			t.Fatalf("unexpected MCP request: %#v", envelope)
		}
		w.Header().Set("Content-Type", "application/json")
		if envelope.Params.Arguments.Message == projectBuildConfigCommitMessage {
			buildCommitCalls++
			fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"result":{"structuredContent":{"phase":"Failed","message":"registry denied"}}}`)
			return
		}
		fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"result":{"structuredContent":{"phase":"Succeeded","files":["src/App.tsx"],"commitSHA":"abcdef1234567890"}}}`)
	}))
	defer mcp.Close()

	workspaces := workspace.NewFileStore(t.TempDir())
	scope := workspace.Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: "demo"}
	if err := workspaces.ApplyFiles(context.Background(), scope, []workspace.File{
		{Path: "package.json", Content: `{"scripts":{"build":"vite build"}}` + "\n"},
		{Path: "src/App.tsx", Content: "committed from workspace\n"},
	}); err != nil {
		t.Fatalf("ApplyFiles returned error: %v", err)
	}
	server := NewWithWorkspace(nil, nil, workspaces, mcp.URL, false)
	args := map[string]any{
		"repositoryRef": "demo-repo",
		"message":       "Update app",
		"paths":         []any{"src/App.tsx"},
	}

	for i := 0; i < 2; i++ {
		resp, err := server.commitProjectWorkspaceFiles(
			context.Background(),
			identity{tenantPath: "root:org-a:ws-1", clusterID: "cluster-ws-1", orgUUID: "org-a", workspaceUUID: "ws-1"},
			scope,
			"demo-repo",
			mcp.URL,
			httptest.NewRequest(http.MethodPost, "/", nil),
			args,
		)
		if err != nil {
			t.Fatalf("commitProjectWorkspaceFiles run %d returned error: %v", i+1, err)
		}
		if !strings.Contains(resp, `"status":"failed"`) {
			t.Fatalf("tool response = %s, want failed build configuration status", resp)
		}
		if _, err := workspaces.ReadFile(context.Background(), scope, workspace.ReadOptions{Path: projectBuildConfigPath, MaxBytes: workspace.MaxWriteBytes}); err == nil {
			t.Fatalf("managed build config was written to workspace after failed build commit on run %d", i+1)
		}
	}
	if buildCommitCalls != 2 {
		t.Fatalf("build commit calls = %d, want retry after failed build commit", buildCommitCalls)
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
		identity{tenantPath: "root:org-a:ws-1", clusterID: "cluster-ws-1"},
		scope,
		"demo",
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
		identity{tenantPath: "root:org-a:ws-1", clusterID: "cluster-ws-1"},
		scope,
		"demo",
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

func TestProjectMCPTimeoutFitsLongRunningOperations(t *testing.T) {
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
	return func(_ context.Context, gvr k8sschema.GroupVersionResource, name string) (*unstructured.Unstructured, error) {
		if obj := items[codeObjectKey(gvr, name)]; obj != nil {
			return obj, nil
		}
		return nil, apierrors.NewNotFound(k8sschema.GroupResource{Group: gvr.Group, Resource: gvr.Resource}, name)
	}
}

type failingProjectStreamResponseWriter struct {
	header http.Header
}

func (w *failingProjectStreamResponseWriter) Header() http.Header {
	return w.header
}

func (w *failingProjectStreamResponseWriter) Write([]byte) (int, error) {
	return 0, errors.New("stream write failed")
}

func (w *failingProjectStreamResponseWriter) WriteHeader(int) {}

func (w *failingProjectStreamResponseWriter) Flush() {}

func codeObjectLister(objects ...*unstructured.Unstructured) codeResourceLister {
	return func(_ context.Context, gvr k8sschema.GroupVersionResource, opts metav1.ListOptions) (*unstructured.UnstructuredList, error) {
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

func codeObjectGVR(obj *unstructured.Unstructured) (k8sschema.GroupVersionResource, bool) {
	switch obj.GetKind() {
	case "Connection":
		return codeConnectionsGVR, true
	case "Repository":
		return codeRepositoriesGVR, true
	case "RepositoryCommit":
		return codeRepositoryCommitsGVR, true
	default:
		return k8sschema.GroupVersionResource{}, false
	}
}

func codeObjectKey(gvr k8sschema.GroupVersionResource, name string) string {
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
