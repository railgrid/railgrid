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

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/components"
	einomodel "github.com/cloudwego/eino/components/model"
	einotool "github.com/cloudwego/eino/components/tool"
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
	for _, legacy := range []string{"list_project_files", "read_project_file", "search_project_files"} {
		if projectLocalToolAllowed(legacy) {
			t.Fatalf("legacy read tool %q remains in App Studio registry", legacy)
		}
	}
	for _, name := range []string{
		"plan_project_changes",
		"check_project_readiness",
		"prepare_project_deployment",
		"inspect_development_templates",
		"get_runtime_status",
		"get_preview_url",
		"get_runtime_logs",
		"verify_development_runtime",
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

func TestProjectAssistantCanonicalWorkspaceReadSurface(t *testing.T) {
	registry := projectAssistantLocalToolRegistry(nil)
	for _, legacy := range []string{"list_project_files", "read_project_file", "search_project_files"} {
		if registry.Has(legacy) {
			t.Fatalf("legacy read tool %q remains in App Studio registry", legacy)
		}
	}
	for _, mutation := range []string{projectToolWriteFile, projectToolApplyPatch, projectToolMkdir} {
		if !registry.Has(mutation) {
			t.Fatalf("App Studio mutation tool %q is missing", mutation)
		}
	}
}

func TestProjectAssistantToolRegistryListsLocalToolsInOrder(t *testing.T) {
	registry := projectAssistantLocalToolRegistry(nil)
	got := projectChatToolNames(registry.ChatTools(false))
	want := []string{
		"define_initial_project_plan",
		"ask_follow_up",
		"request_project_plan_approval",
		"write_file",
		"apply_patch",
		"mkdir",
		"select_project_template",
		"hydrate_workspace",
		"get_project_checkpoints",
		"check_project_build",
		"get_build_logs",
		"rebuild_project",
		"promote_project",
		"plan_project_changes",
		"check_project_readiness",
		"prepare_project_deployment",
		"inspect_development_templates",
		"get_runtime_status",
		"get_preview_url",
		"get_runtime_logs",
		"verify_development_runtime",
		"restart_runtime",
		"set_runtime_env",
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("tool names = %v, want %v", got, want)
	}

	all := projectChatToolNames(registry.ChatTools(true))
	wantAll := append([]string(nil), want[:13]...)
	wantAll = append(wantAll, "commit_project_files")
	wantAll = append(wantAll, want[13:]...)
	if strings.Join(all, ",") != strings.Join(wantAll, ",") {
		t.Fatalf("tool names with commit bridge = %v, want %v", all, wantAll)
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
	project.UID = "test-project-uid-demo"
	id := identity{tenantPath: "root:org-a:ws-1", clusterID: "cluster-ws-1", orgUUID: "org-a", workspaceUUID: "ws-1", user: "user@example.com"}
	settings := projectLLMSettings{Provider: defaultProjectLLMProvider, BaseURL: defaultProjectLLMBaseURL, Model: "test-model", APIKey: "test-key"}
	client := asclient.NewFromDynamic(projectSettingsDynamicClient{secret: projectLLMSettingsSecret(settings)})
	messageScope := testProjectMessageScope(id.orgUUID, id.workspaceUUID, project.Name)
	if err := appendProjectUserMessage(context.Background(), messages, messageScope, "ship the demo"); err != nil {
		t.Fatalf("appendProjectUserMessage returned error: %v", err)
	}
	model := &repositoryFlowEinoChatModel{Steps: []repositoryFlowEinoModelStep{{
		Message: einoschema.AssistantMessage("Ready.", nil),
	}, {
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
		t.Fatalf("reply = %q, want model report", reply)
	}
	if len(model.Inputs) != 1 {
		t.Fatalf("Eino model request count = %d, want one", len(model.Inputs))
	}
	var joined string
	for _, msg := range model.Inputs[0].Messages {
		joined += msg.Content + "\n"
	}
	if strings.Contains(joined, "Available tools in this workspace") {
		t.Fatalf("prompt duplicates the Eino tool catalog: %q", joined)
	}
	if strings.Contains(joined, projectToolReadFile+":") {
		t.Fatalf("prompt duplicates local tool descriptions: %q", joined)
	}
	if projectChatToolsInclude(model.Inputs[0].Tools, projectToolCommitProjectFiles) {
		t.Fatalf("model tools = %#v, must hide commit before mutation and verification", model.Inputs[0].Tools)
	}
	if !projectChatToolsInclude(model.Inputs[0].Tools, projectToolRequestProjectPlanApproval) {
		t.Fatalf("model tools = %#v, want plan approval in the initial phase", model.Inputs[0].Tools)
	}
	if projectChatToolsInclude(model.Inputs[0].Tools, "tool_search") {
		t.Fatalf("model tools = %#v, want no tool_search without provider tools", model.Inputs[0].Tools)
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
	project.UID = "test-project-uid-demo"
	id := identity{tenantPath: "root:org-a:ws-1", clusterID: "cluster-ws-1", orgUUID: "org-a", workspaceUUID: "ws-1", user: "user@example.com"}
	settings := projectLLMSettings{Provider: defaultProjectLLMProvider, BaseURL: defaultProjectLLMBaseURL, Model: "test-model", APIKey: "test-key"}
	client := asclient.NewFromDynamic(projectSettingsDynamicClient{secret: projectLLMSettingsSecret(settings)})
	messageScope := testProjectMessageScope(id.orgUUID, id.workspaceUUID, project.Name)
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
	if strings.Contains(joined, "Available tools in this workspace") {
		t.Fatalf("prompt duplicates the Eino tool catalog: %q", joined)
	}
	if strings.Contains(joined, projectToolReadFile+":") {
		t.Fatalf("prompt duplicates local tool descriptions: %q", joined)
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
	project.UID = "test-project-uid-demo"
	id := identity{tenantPath: "root:org-a:ws-1", clusterID: "cluster-ws-1", orgUUID: "org-a", workspaceUUID: "ws-1", user: "user@example.com"}
	settings := projectLLMSettings{Provider: defaultProjectLLMProvider, BaseURL: defaultProjectLLMBaseURL, Model: "test-model", APIKey: "test-key"}
	client := asclient.NewFromDynamic(projectSettingsDynamicClient{secret: projectLLMSettingsSecret(settings)})
	messageScope := testProjectMessageScope(id.orgUUID, id.workspaceUUID, project.Name)
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
	if projectChatToolsInclude(model.Inputs[0].Tools, projectToolCommitProjectFiles) {
		t.Fatalf("model tools = %#v, must hide commit before mutation and verification", model.Inputs[0].Tools)
	}
	if !projectChatToolsInclude(model.Inputs[0].Tools, projectToolRequestProjectPlanApproval) {
		t.Fatalf("model tools = %#v, want plan approval in the initial phase", model.Inputs[0].Tools)
	}
	if projectChatToolsInclude(model.Inputs[0].Tools, "tool_search") {
		t.Fatalf("model tools = %#v, want no tool_search without provider tools", model.Inputs[0].Tools)
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
	project.UID = "test-project-uid-demo"
	id := identity{tenantPath: "root:org-a:ws-1", clusterID: "cluster-ws-1", orgUUID: "org-a", workspaceUUID: "ws-1", user: "user@example.com"}
	settings := projectLLMSettings{Provider: defaultProjectLLMProvider, BaseURL: defaultProjectLLMBaseURL, Model: "test-model", APIKey: "test-key"}
	client := asclient.NewFromDynamic(projectSettingsDynamicClient{secret: projectLLMSettingsSecret(settings)})
	messageScope := testProjectMessageScope(id.orgUUID, id.workspaceUUID, project.Name)
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
	project.UID = "test-project-uid-demo"
	id := identity{tenantPath: "root:org-a:ws-1", clusterID: "cluster-ws-1", orgUUID: "org-a", workspaceUUID: "ws-1", user: "user@example.com"}
	settings := projectLLMSettings{Provider: defaultProjectLLMProvider, BaseURL: defaultProjectLLMBaseURL, Model: "test-model", APIKey: "test-key"}
	client := asclient.NewFromDynamic(projectSettingsDynamicClient{secret: projectLLMSettingsSecret(settings)})
	messageScope := testProjectMessageScope(id.orgUUID, id.workspaceUUID, project.Name)
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
	project.UID = "test-project-uid-demo"
	id := identity{tenantPath: "root:org-a:ws-1", clusterID: "cluster-ws-1", orgUUID: "org-a", workspaceUUID: "ws-1", user: "user@example.com"}
	settings := projectLLMSettings{Provider: defaultProjectLLMProvider, BaseURL: defaultProjectLLMBaseURL, Model: "test-model", APIKey: "test-key"}
	client := asclient.NewFromDynamic(projectSettingsDynamicClient{secret: projectLLMSettingsSecret(settings)})
	messageScope := testProjectMessageScope(id.orgUUID, id.workspaceUUID, project.Name)
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
		if !projectChatToolsInclude(model.Inputs[0].Tools, want) {
			t.Fatalf("model tools = %#v, want %s", model.Inputs[0].Tools, want)
		}
	}
	for _, unwanted := range []string{projectToolRestartRuntime, projectToolWriteFile, projectToolCommitProjectFiles} {
		if projectChatToolsInclude(model.Inputs[0].Tools, unwanted) {
			t.Fatalf("model tools = %#v, should not include %s", model.Inputs[0].Tools, unwanted)
		}
	}
}

func TestProjectAssistantWorkspaceInspectPromptUsesCanonicalReads(t *testing.T) {
	project := projectWithRepository("demo-repo", "demo", "github")
	project.Name = "demo-project"
	project.UID = "test-project-uid-demo-project"
	project.Spec.DisplayName = "Demo Project"
	repository := &ProjectRepositoryView{
		Ref:    "demo-repo",
		Name:   "demo",
		Status: projectRepositoryStatusReady,
		Ready:  true,
	}

	prompt := projectSystemPrompt(project, repository, projectAssistantTurnProfileImplementation)
	for _, want := range []string{"check_project_readiness", "prepare_project_deployment", "inspect_development_templates", "verify_development_runtime", projectToolLS, projectToolReadFile, projectToolGlob, projectToolGrep, "write_file", "apply_patch", "mkdir", "commit_project_files"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
	}
	for _, legacy := range []string{"list_project_files", "read_project_file", "search_project_files"} {
		if strings.Contains(prompt, legacy) {
			t.Fatalf("prompt contains legacy workspace read tool %q:\n%s", legacy, prompt)
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
	if !strings.Contains(prompt, "brief milestone updates") || !strings.Contains(prompt, "Do not narrate each tool call") {
		t.Fatalf("prompt missing milestone and per-tool narration guidance:\n%s", prompt)
	}
	if !strings.Contains(strings.ToLower(prompt), "before") || !strings.Contains(strings.ToLower(prompt), "inspect") {
		t.Fatalf("prompt does not require inspect-before-edit guidance:\n%s", prompt)
	}
	if !strings.Contains(prompt, "Use ls and glob to discover project-relative paths, read_file for bounded targeted reads, and grep to locate code. Inspect relevant existing files before editing.") {
		t.Fatalf("prompt missing canonical inspect-before-edit workflow:\n%s", prompt)
	}
}

func TestProjectAssistantDoesNotAdvertiseLegacyRuntimeCommandTools(t *testing.T) {
	registry := projectAssistantLocalToolRegistry(nil)
	toolNames := strings.Join(projectChatToolNames(registry.ChatTools(true)), "\n")
	project := projectWithRepository("demo-repo", "demo", "github")
	project.Name = "demo-project"
	project.UID = "test-project-uid-demo-project"
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
			name: projectToolLS,
			args: `{"path":"src"}`,
			want: []string{"path src"},
		},
		{
			name: projectToolReadFile,
			args: `{"file_path":"src/App.tsx","offset":1,"limit":200}`,
			want: []string{"path src/App.tsx", "offset 1", "limit 200"},
		},
		{
			name: projectToolGlob,
			args: `{"pattern":"**/*.tsx","path":"src"}`,
			want: []string{"path src", "pattern **/*.tsx"},
		},
		{
			name: projectToolGrep,
			args: `{"pattern":"secret-ish user query","path":"src","glob":"**/*.tsx","type":"tsx","output_mode":"content","-C":3,"-B":1,"-A":2,"-n":false,"-i":true,"head_limit":10,"offset":2,"multiline":true}`,
			want: []string{"path src", "pattern secret-ish user query", "glob **/*.tsx", "type tsx", "output_mode content", "-C 3", "-B 1", "-A 2", "-n false", "-i true", "head_limit 10", "offset 2", "multiline true"},
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
			if (tt.name == projectToolGlob || tt.name == projectToolGrep) && !strings.HasPrefix(got, "path src; ") {
				t.Fatalf("summary = %q, want real path first", got)
			}
			if (tt.name == "write_file" || tt.name == "apply_patch") && strings.Contains(got, "secret-ish") {
				t.Fatalf("summary leaked content: %q", got)
			}
		})
	}
}

func TestSummarizeProjectToolResultWorkspaceReadTools(t *testing.T) {
	tests := []struct {
		name      string
		result    string
		want      string
		forbidden string
	}{
		{name: projectToolReadFile, result: "1\tsecret-ish file body\n2\tmore source", want: "file read", forbidden: "secret-ish"},
		{name: projectToolLS, result: "src\nREADME.md", want: "2 path(s)", forbidden: "README.md"},
		{name: projectToolGlob, result: "src/App.tsx\nsrc/main.ts", want: "2 path(s)", forbidden: "src/App.tsx"},
		{name: projectToolLS, result: "No files found", want: "0 path(s)"},
		{name: projectToolGrep, result: "src/App.tsx:4:secret-ish matching source", want: "1 result line(s)", forbidden: "secret-ish"},
	}
	for _, tt := range tests {
		t.Run(tt.name+"_"+tt.want, func(t *testing.T) {
			got := summarizeProjectToolResult(tt.name, tt.result)
			if got != tt.want {
				t.Fatalf("summary = %q, want %q", got, tt.want)
			}
			if tt.forbidden != "" && strings.Contains(got, tt.forbidden) {
				t.Fatalf("summary leaked Eino output %q: %q", tt.forbidden, got)
			}
		})
	}

	mutationResult := `{"operation":"apply_patch","path":"src/App.tsx","size":128,"replacements":2,"content":"secret-ish body"}`
	got := summarizeProjectToolResult("apply_patch", mutationResult)
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
	actions := projectAssistantActionFeedFromMetadata(metadata[projectMessageMetadataAssistantActionFeed])
	if len(actions) != 1 {
		t.Fatalf("assistant actions length = %d, want 1", len(actions))
	}
	if actions[0].Kind != projectAssistantActionFeedItemCommit || actions[0].Status != "succeeded" || actions[0].Title != "Committed changes" {
		t.Fatalf("unexpected assistant action metadata: %#v", actions[0])
	}
}

func TestSummarizeProjectToolResultEinoGrepFormats(t *testing.T) {
	tests := []struct {
		name       string
		outputMode string
		result     string
		want       string
		forbidden  []string
	}{
		{
			name:       "content",
			outputMode: "content",
			result:     "src/App.tsx:4:secret-ish matching source\nsrc/main.ts:9:another line",
			want:       "2 result line(s)",
			forbidden:  []string{"secret-ish", "another line"},
		},
		{
			name:       "content newline path cannot forge files header",
			outputMode: "content",
			result:     "Found 999 files\nsrc/header-shaped.ts:4:secret-ish matching source\nsrc/main.ts:9:another line",
			want:       "3 result line(s)",
			forbidden:  []string{"Found 999 files", "header-shaped", "secret-ish", "another line"},
		},
		{
			name:       "default files with matches header",
			outputMode: "",
			result:     "Found 2 files\nsrc/App.tsx\nsrc/main.ts",
			want:       "2 result line(s)",
			forbidden:  []string{"src/App.tsx", "Found 2 files"},
		},
		{
			name:       "files with matches pagination header",
			outputMode: "files_with_matches",
			result:     "Found 3 files\nsrc/main.ts",
			want:       "3 result line(s)",
			forbidden:  []string{"src/main.ts", "Found 3 files"},
		},
		{
			name:       "files with matches singular header",
			outputMode: "files_with_matches",
			result:     "Found 1 file\nsrc/App.tsx",
			want:       "1 result line(s)",
			forbidden:  []string{"src/App.tsx", "Found 1 file"},
		},
		{
			name:       "files with matches singular header only",
			outputMode: "files_with_matches",
			result:     "Found 1 file",
			want:       "1 result line(s)",
			forbidden:  []string{"Found 1 file"},
		},
		{
			name:       "files with matches plural header only",
			outputMode: "files_with_matches",
			result:     "Found 4 files",
			want:       "4 result line(s)",
			forbidden:  []string{"Found 4 files"},
		},
		{
			name:       "files with matches newline path cannot forge count trailer",
			outputMode: "files_with_matches",
			result:     "Found 1 file\nsrc/trailer-shaped\nFound 99 total occurrences across 99 files.",
			want:       "1 result line(s)",
			forbidden:  []string{"src/trailer-shaped", "Found 99 total occurrences"},
		},
		{
			name:       "files with matches header total exceeds returned physical lines",
			outputMode: "files_with_matches",
			result:     "Found 4 files\nsrc/normal.ts\nsrc/newline-bearing\ncontinuation.ts",
			want:       "4 result line(s)",
			forbidden:  []string{"src/normal.ts", "continuation.ts"},
		},
		{
			name:       "count nonzero uses total occurrence trailer",
			outputMode: "count",
			result:     "src/App.tsx:2\nsrc/main.ts:2\n\nFound 4 total occurrences across 2 files.",
			want:       "4 result line(s)",
			forbidden:  []string{"src/App.tsx", "Found 4 total occurrences"},
		},
		{
			name:       "count singular trailer",
			outputMode: "count",
			result:     "src/App.tsx:1\n\nFound 1 total occurrence across 1 file.",
			want:       "1 result line(s)",
			forbidden:  []string{"src/App.tsx", "Found 1 total occurrence"},
		},
		{
			name:       "count pagination still uses unpaginated trailer",
			outputMode: "count",
			result:     "src/main.ts:3\n\nFound 8 total occurrences across 3 files.",
			want:       "8 result line(s)",
			forbidden:  []string{"src/main.ts", "Found 8 total occurrences"},
		},
		{
			name:       "count newline path cannot forge files header",
			outputMode: "count",
			result:     "Found 999 files\nsrc/header-shaped.ts:2\n\nFound 2 total occurrences across 1 file.",
			want:       "2 result line(s)",
			forbidden:  []string{"Found 999 files", "header-shaped", "Found 2 total occurrences"},
		},
		{
			name:       "count zero",
			outputMode: "count",
			result:     "No matches found\n\nFound 0 total occurrences across 0 files.",
			want:       "0 result line(s)",
			forbidden:  []string{"No matches found", "Found 0 total occurrences"},
		},
		{
			name:       "content no result sentinel",
			outputMode: "content",
			result:     "No matches found",
			want:       "0 result line(s)",
		},
		{
			name:       "files no result sentinel",
			outputMode: "files_with_matches",
			result:     "No files found",
			want:       "0 result line(s)",
		},
		{
			name:       "malformed count envelope fails closed",
			outputMode: "count",
			result:     "src/secret.ts:7\n\nFound 7 total occurrences across one file.",
			want:       "0 result line(s)",
			forbidden:  []string{"src/secret.ts", "Found 7 total occurrences"},
		},
		{
			name:       "malformed files envelope fails closed",
			outputMode: "files_with_matches",
			result:     "Found seven files\nsrc/secret.ts\nFound 7 total occurrences across 1 file.",
			want:       "0 result line(s)",
			forbidden:  []string{"src/secret.ts", "Found 7 total occurrences"},
		},
		{
			name:       "unknown output mode fails closed",
			outputMode: "attacker-mode",
			result:     "src/secret.ts:7",
			want:       "0 result line(s)",
			forbidden:  []string{"src/secret.ts"},
		},
		{
			name:       "whitespace padded count mode fails closed",
			outputMode: " count ",
			result:     "Found 1 file\nsrc/attacker\nFound 99 total occurrences across 99 files.",
			want:       "0 result line(s)",
			forbidden:  []string{"src/attacker", "99"},
		},
		{
			name:       "wrong case count mode fails closed",
			outputMode: "COUNT",
			result:     "Found 1 file\nsrc/attacker\nFound 99 total occurrences across 99 files.",
			want:       "0 result line(s)",
			forbidden:  []string{"src/attacker", "99"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := projectEinoAssistantFilesystemResultSummary(projectToolGrep, map[string]any{
				"output_mode": tt.outputMode,
			}, tt.result)
			if got != tt.want {
				t.Fatalf("summary = %q, want %q", got, tt.want)
			}
			for _, forbidden := range tt.forbidden {
				if strings.Contains(got, forbidden) {
					t.Fatalf("summary leaked Eino grep output %q: %q", forbidden, got)
				}
			}
			action := projectAssistantActionFeedItemFromAssistantToolCall(projectAssistantToolCall{
				ID:      "grep",
				Name:    projectToolGrep,
				Status:  "succeeded",
				Summary: got,
			})
			count, ok := projectAssistantSummaryCount(got, "result line(s)")
			if !ok {
				t.Fatalf("summary = %q, want result count", got)
			}
			noun := "references"
			if count == 1 {
				noun = "reference"
			}
			wantOutcome := fmt.Sprintf("%d %s", count, noun)
			if action.Title != "Searched project" || action.Outcome != wantOutcome {
				t.Fatalf("action = %#v, want mode-neutral outcome %q", action, wantOutcome)
			}
		})
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
	messageScope := store.Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: "demo", ProjectUID: "test-project-uid-demo"}
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
	project.UID = "test-project-uid-demo"
	id := identity{tenantPath: "root:org-a:ws-1", clusterID: "cluster-ws-1", orgUUID: "org-a", workspaceUUID: "ws-1"}

	var events []projectAssistantEvent
	_, err := server.generateProjectAssistantStreamWithStart(
		httptest.NewRequest(http.MethodPost, "/", nil),
		id,
		client,
		project,
		projectAssistantStreamCallbacks{
			OnAssistantEvent: func(event projectAssistantEvent) {
				events = append(events, event)
			},
		},
		projectAssistantPermissionCheckpointStartForTest(),
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
	messageScope := testProjectMessageScope(id.orgUUID, id.workspaceUUID, "demo")
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
	project.UID = "test-project-uid-demo"
	rr := httptest.NewRecorder()
	flusher, ok := startProjectMessageStream(rr)
	if !ok {
		t.Fatal("response recorder did not support streaming")
	}

	server.streamProjectAssistantWithStart(
		rr,
		flusher,
		httptest.NewRequest(http.MethodPost, "/", nil),
		client,
		id,
		project,
		messages,
		"",
		projectAssistantPermissionCheckpointStartForTest(),
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
	actions := projectAssistantActionFeedFromMetadata(assistant.Metadata[projectMessageMetadataAssistantActionFeed])
	if len(actions) != 1 || actions[0].Status != projectAssistantActionFeedStatusWaiting || actions[0].Kind != projectAssistantActionFeedItemEdit {
		t.Fatalf("assistant actions = %#v, want pending edit disclosure", actions)
	}
	if actions[0].Target != "src/App.tsx" {
		t.Fatalf("assistant action disclosure = %#v, want only the user-facing target", actions[0])
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
	messageScope := testProjectMessageScope(id.orgUUID, id.workspaceUUID, "demo")
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
	project.UID = "test-project-uid-demo"
	stream := &failingProjectStreamResponseWriter{header: http.Header{}}

	server.streamProjectAssistantWithStart(
		stream,
		stream,
		httptest.NewRequest(http.MethodPost, "/", nil),
		client,
		id,
		project,
		messages,
		"",
		projectAssistantPermissionCheckpointStartForTest(),
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
	actions := projectAssistantActionFeedFromMetadata(assistant.Metadata[projectMessageMetadataAssistantActionFeed])
	interrupt := projectAssistantUIInterruptFromMetadata(assistant.Metadata[projectMessageMetadataAssistantInterrupt])
	if assistant.Metadata[projectMessageMetadataStatus] != projectMessageStatusPendingPermission || len(actions) != 1 || actions[0].Status != projectAssistantActionFeedStatusWaiting || interrupt == nil || interrupt.Action == nil {
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

func setProjectAssistantModelWithReachableVerificationForTest(server *Server, model einomodel.BaseChatModel) {
	setProjectAssistantModelWithVerificationResultForTest(server, model, `{"status":"reachable"}`)
}

func setProjectAssistantModelWithVerificationResultForTest(server *Server, model einomodel.BaseChatModel, result string) {
	baseTools := newProjectEinoAssistantToolsFactory(server)
	verifyTool := projectAssistantToolFunc{
		spec: projectAssistantToolSpec{
			Name:        projectToolVerifyDevelopmentRuntime,
			Description: "Verify the development runtime.",
			Parameters:  json.RawMessage(`{"type":"object"}`),
			Risk:        projectAssistantToolRiskRead,
		},
		call: func(context.Context, projectAssistantToolCallRequest) (string, error) {
			return result, nil
		},
	}
	server.mu.Lock()
	defer server.mu.Unlock()
	server.assistantEngine = projectEinoAssistantEngine{
		server: server,
		newModel: func(context.Context, projectAssistantRunRequest, *projectEinoAssistantRunState) (einomodel.BaseChatModel, error) {
			return model, nil
		},
		newTools: func(ctx context.Context, req projectAssistantRunRequest, state *projectEinoAssistantRunState) ([]einotool.BaseTool, error) {
			tools, err := baseTools(ctx, req, state)
			if err != nil {
				return nil, err
			}
			filtered := make([]einotool.BaseTool, 0, len(tools))
			for _, tool := range tools {
				info, err := tool.Info(ctx)
				if err != nil {
					return nil, err
				}
				if projectToolBaseName(info.Name) != projectToolVerifyDevelopmentRuntime {
					filtered = append(filtered, tool)
				}
			}
			return append(filtered, newProjectEinoAssistantServerTool(server, verifyTool, req, state)), nil
		},
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
	verifyCall := chatStreamingCall{Index: 0, ID: "call-verify-after-write", Type: "function"}
	verifyCall.Function.Name = projectToolVerifyDevelopmentRuntime
	verifyCall.Function.Arguments = `{}`
	model := &repositoryFlowEinoChatModel{Steps: []repositoryFlowEinoModelStep{
		{Message: einoschema.AssistantMessage("", projectEinoToolCallsFromStreamingForTest(calls))},
		{Message: einoschema.AssistantMessage("", projectEinoToolCallsFromStreamingForTest([]chatStreamingCall{verifyCall}))},
		{Message: einoschema.AssistantMessage(finalContent, nil)},
	}}
	setProjectAssistantModelWithReachableVerificationForTest(server, model)
	return startEinoPermissionWithConfiguredModelForTest(
		t, server, messages, id, project, prompt, model,
		projectAssistantPermissionCheckpointStartForTest(),
	)
}

func projectAssistantPermissionCheckpointStartForTest() *projectAssistantStreamStart {
	grant := normalizeProjectAssistantApprovedPlan(projectAssistantApprovedPlan{
		Summary:      "Test permission checkpoint.",
		Steps:        []string{"exercise the permission checkpoint"},
		TargetPaths:  []string{"already-approved/"},
		Version:      projectAssistantApprovedPlanVersionWorkspaceMutation,
		Capabilities: []string{projectAssistantCapabilityWorkspaceMutate},
		RunLocal:     true,
	})
	return &projectAssistantStreamStart{InitialApprovedPlan: &grant}
}

func startVerifiedEinoCommitPermissionForTest(
	t *testing.T,
	server *Server,
	messages store.Store,
	id identity,
	project *aiv1alpha1.Project,
	prompt string,
	finalContent string,
	commitCall chatStreamingCall,
) projectAssistantPermissionFixture {
	t.Helper()
	if strings.TrimSpace(finalContent) == "" {
		finalContent = "Approval completed."
	}
	writeCall := chatStreamingCall{Index: 0, ID: "call-write-before-commit", Type: "function"}
	writeCall.Function.Name = projectToolWriteFile
	writeCall.Function.Arguments = `{"path":"src/App.tsx","content":"approved\n"}`
	verifyCall := chatStreamingCall{Index: 0, ID: "call-verify-before-commit", Type: "function"}
	verifyCall.Function.Name = projectToolVerifyDevelopmentRuntime
	verifyCall.Function.Arguments = `{}`
	model := &repositoryFlowEinoChatModel{Steps: []repositoryFlowEinoModelStep{
		{Message: einoschema.AssistantMessage("", projectEinoToolCallsFromStreamingForTest([]chatStreamingCall{writeCall}))},
		{Message: einoschema.AssistantMessage("", projectEinoToolCallsFromStreamingForTest([]chatStreamingCall{verifyCall}))},
		{Message: einoschema.AssistantMessage("", projectEinoToolCallsFromStreamingForTest([]chatStreamingCall{commitCall}))},
		{Message: einoschema.AssistantMessage(finalContent, nil)},
	}}
	setProjectAssistantModelWithReachableVerificationForTest(server, model)
	grant := normalizeProjectAssistantApprovedPlan(projectAssistantApprovedPlan{
		Summary:      "Apply and verify the approved project change.",
		Steps:        []string{"write the project change", "verify the development runtime"},
		TargetPaths:  []string{"src/"},
		Version:      projectAssistantApprovedPlanVersionWorkspaceMutation,
		Capabilities: []string{projectAssistantCapabilityWorkspaceMutate},
	})
	if err := server.saveProjectAssistantApprovedPlan(context.Background(), testProjectMessageScope(id.orgUUID, id.workspaceUUID, project.Name), &grant); err != nil {
		t.Fatalf("saveProjectAssistantApprovedPlan returned error: %v", err)
	}
	return startEinoPermissionWithConfiguredModelForTest(t, server, messages, id, project, prompt, model, nil)
}

func startEinoPermissionWithConfiguredModelForTest(
	t *testing.T,
	server *Server,
	messages store.Store,
	id identity,
	project *aiv1alpha1.Project,
	prompt string,
	model *repositoryFlowEinoChatModel,
	start *projectAssistantStreamStart,
) projectAssistantPermissionFixture {
	t.Helper()
	settings := projectLLMSettings{Provider: defaultProjectLLMProvider, BaseURL: defaultProjectLLMBaseURL, Model: "test-model", APIKey: "test-key"}
	client := asclient.NewFromDynamic(projectSettingsDynamicClient{secret: projectLLMSettingsSecret(settings)})
	messageScope := testProjectMessageScope(id.orgUUID, id.workspaceUUID, project.Name)
	if strings.TrimSpace(prompt) == "" {
		prompt = "approve tool"
	}
	if err := appendProjectUserMessage(context.Background(), messages, messageScope, prompt); err != nil {
		t.Fatalf("appendProjectUserMessage returned error: %v", err)
	}
	var permission projectAssistantPermission
	var checkpoint projectAssistantCheckpoint
	_, err := server.generateProjectAssistantStreamWithStart(
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
		start,
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
	project.UID = "test-project-uid-demo"
	id := identity{tenantPath: "root:org-a:ws-1", clusterID: "cluster-ws-1", orgUUID: "org-a", workspaceUUID: "ws-1"}
	messageScope := testProjectMessageScope(id.orgUUID, id.workspaceUUID, project.Name)
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
	if len(audit.Decisions) != 1 || audit.Decisions[0].Decision != projectAssistantPermissionAllow || audit.Decisions[0].Actor != id.user {
		t.Fatalf("audit = %#v, want allow decision with actor", audit)
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
	updatedActions := projectAssistantActionFeedFromMetadata(updatedMessage.Metadata[projectMessageMetadataAssistantActionFeed])
	var writeAction *projectAssistantActionFeedItem
	for i := range updatedActions {
		if updatedActions[i].ID == projectAssistantActionPublicID(call.ID) {
			writeAction = &updatedActions[i]
			break
		}
	}
	if writeAction == nil || writeAction.Status != "succeeded" {
		t.Fatalf("updated actions = %#v, want persisted succeeded write action", updatedActions)
	}
	if interrupt := projectAssistantUIInterruptFromMetadata(updatedMessage.Metadata[projectMessageMetadataAssistantInterrupt]); interrupt != nil {
		t.Fatalf("assistant interrupt = %#v, want cleared after approval", interrupt)
	}
}

func TestUpdateProjectAssistantPermissionMessageRemovesCompletedUnknownAction(t *testing.T) {
	messages := store.NewMemoryStore()
	server := NewWithWorkspace(nil, messages, workspace.NewFileStore(t.TempDir()), "", false)
	scope := testProjectMessageScope("org-a", "ws-1", "demo")
	messageID := "msg-assistant"
	runID := "run-1"
	requestID := "request-1"
	toolCallID := "unknown-1"
	if err := appendProjectAssistantMessage(context.Background(), messages, scope, messageID, "", map[string]any{
		projectMessageMetadataStatus: projectMessageStatusPendingPermission,
		projectMessageMetadataAssistantActionFeed: []projectAssistantActionFeedItem{{
			ID:       projectAssistantActionPublicID(toolCallID),
			Kind:     projectAssistantActionFeedItemOther,
			Status:   projectAssistantActionFeedStatusWaiting,
			Title:    "Waiting for action",
			Severity: projectAssistantActionFeedSeverityAttention,
		}},
		projectMessageMetadataAssistantInterrupt: projectAssistantUIInterruptRequest{
			Action: &projectAssistantUIInterruptAction{RunID: runID, RequestID: requestID},
		},
	}); err != nil {
		t.Fatalf("appendProjectAssistantMessage returned error: %v", err)
	}

	toolCall := projectToolCallStreamEvent{
		ID:     toolCallID,
		Name:   "provider__internal_tool",
		Status: "succeeded",
	}
	if err := server.updateProjectAssistantPermissionMessage(context.Background(), scope, messageID, projectAssistantResumeResponse{
		RunID:     runID,
		RequestID: requestID,
		Status:    store.AssistantRunStatusCompleted,
		ToolCall:  &toolCall,
	}); err != nil {
		t.Fatalf("updateProjectAssistantPermissionMessage returned error: %v", err)
	}

	updated, err := server.findProjectMessage(context.Background(), scope, messageID)
	if err != nil {
		t.Fatalf("findProjectMessage returned error: %v", err)
	}
	if _, ok := updated.Metadata[projectMessageMetadataAssistantActionFeed]; ok {
		t.Fatalf("assistant metadata = %#v, want completed unknown action removed", updated.Metadata)
	}
}

func TestResumeProjectAssistantRunRepairsPrePatchCheckpointMessageIdentity(t *testing.T) {
	for _, tt := range []struct {
		name string
		new  func(*testing.T) store.Store
	}{
		{name: "memory", new: func(*testing.T) store.Store { return store.NewMemoryStore() }},
		{name: "encrypted", new: func(t *testing.T) store.Store {
			t.Helper()
			wrapped, err := store.NewEncryptedStore(store.NewMemoryStore(), []store.EncryptionKey{{
				ID:    "test-key",
				Value: []byte("0123456789abcdef0123456789abcdef"),
			}})
			if err != nil {
				t.Fatalf("NewEncryptedStore: %v", err)
			}
			return wrapped
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			messages := tt.new(t)
			workspaces := workspace.NewFileStore(t.TempDir())
			server := NewWithWorkspace(nil, messages, workspaces, "", false)
			project := projectWithRepository("demo-repo", "demo", "github")
			project.Name = "demo"
			project.UID = "test-project-uid-demo"
			id := identity{tenantPath: "root:org-a:ws-1", clusterID: "cluster-ws-1", orgUUID: "org-a", workspaceUUID: "ws-1"}
			messageScope := testProjectMessageScope(id.orgUUID, id.workspaceUUID, project.Name)
			call := chatStreamingCall{Index: 0, ID: "call-write", Type: "function"}
			call.Function.Name = projectToolWriteFile
			call.Function.Arguments = `{"path":"src/App.tsx","content":"approved\n"}`
			fixture := startEinoPermissionForTest(t, server, messages, id, project, "write src/app", "I wrote src/App.tsx.", call)
			assistantMessageID := "msg-pre-patch-assistant"
			if err := appendProjectAssistantMessage(context.Background(), messages, messageScope, assistantMessageID, "", projectAssistantMessageMetadata(projectMessageStatusPendingPermission, []projectToolCallStreamEvent{{
				ID:         call.ID,
				Name:       call.Function.Name,
				Status:     "permission_required",
				Arguments:  "path src/App.tsx, 9 bytes",
				Summary:    fixture.Permission.Reason,
				Permission: &fixture.Permission,
				Checkpoint: &fixture.Checkpoint,
			}})); err != nil {
				t.Fatalf("appendProjectAssistantMessage: %v", err)
			}

			// This is the shape persisted before durable active_message_id and
			// checkpoint.assistantMessageID were introduced. The interrupt metadata
			// remains the durable run/request association for the candidate supplied
			// by the resume request.
			run, err := messages.GetAssistantRun(context.Background(), messageScope, fixture.PermissionErr.RunID)
			if err != nil {
				t.Fatalf("GetAssistantRun: %v", err)
			}
			var state projectAssistantCheckpointState
			if err := json.Unmarshal(run.Checkpoint, &state); err != nil {
				t.Fatalf("decode checkpoint: %v", err)
			}
			state.AssistantMessageID = ""
			run.ActiveMessageID = ""
			run.Checkpoint, err = json.Marshal(state)
			if err != nil {
				t.Fatalf("encode legacy checkpoint: %v", err)
			}
			if err := messages.SaveAssistantRun(context.Background(), messageScope, run); err != nil {
				t.Fatalf("SaveAssistantRun legacy shape: %v", err)
			}

			resp, err := server.resumeProjectAssistantRunWithRepositoryAndClient(
				context.Background(),
				httptest.NewRequest(http.MethodPost, "/", nil),
				id,
				fixture.Client,
				project,
				&ProjectRepositoryView{Ref: "demo-repo", Name: "demo", Status: projectRepositoryStatusReady},
				fixture.PermissionErr.RunID,
				projectAssistantResumeRequest{
					RequestID:          fixture.PermissionErr.RequestID,
					Decision:           string(projectAssistantPermissionAllow),
					AssistantMessageID: assistantMessageID,
				},
			)
			if err != nil {
				t.Fatalf("resumeProjectAssistantRun: %v", err)
			}
			if resp.Status != store.AssistantRunStatusCompleted {
				t.Fatalf("resume status = %q, want completed", resp.Status)
			}
			persisted, err := messages.GetAssistantRun(context.Background(), messageScope, fixture.PermissionErr.RunID)
			if err != nil {
				t.Fatalf("GetAssistantRun after resume: %v", err)
			}
			if persisted.ActiveMessageID != assistantMessageID {
				t.Fatalf("ActiveMessageID = %q, want recovered request message %q", persisted.ActiveMessageID, assistantMessageID)
			}
		})
	}
}

func TestResumeProjectAssistantRunIgnoresStaleAssistantMessageID(t *testing.T) {
	messages := store.NewMemoryStore()
	workspaces := workspace.NewFileStore(t.TempDir())
	server := NewWithWorkspace(nil, messages, workspaces, "", false)
	project := projectWithRepository("demo-repo", "demo", "github")
	project.Name = "demo"
	project.UID = "test-project-uid-demo"
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
	project.UID = "test-project-uid-demo"
	id := identity{tenantPath: "root:org-a:ws-1", clusterID: "cluster-ws-1", orgUUID: "org-a", workspaceUUID: "ws-1"}
	messageScope := testProjectMessageScope(id.orgUUID, id.workspaceUUID, project.Name)
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
	actions := projectAssistantActionFeedFromMetadata(otherMessage.Metadata[projectMessageMetadataAssistantActionFeed])
	if len(actions) != 1 || actions[0].ID != projectAssistantActionPublicID("call-other") || actions[0].Status != projectAssistantActionFeedStatusWaiting {
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
	project.UID = "test-project-uid-demo"
	id := identity{tenantPath: "root:org-a:ws-1", clusterID: "cluster-ws-1", orgUUID: "org-a", workspaceUUID: "ws-1"}
	messageScope := testProjectMessageScope(id.orgUUID, id.workspaceUUID, project.Name)
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
	updatedActions := projectAssistantActionFeedFromMetadata(updatedMessage.Metadata[projectMessageMetadataAssistantActionFeed])
	if len(updatedActions) < 1 || updatedActions[0].Status != "rejected" {
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
		{Message: einoschema.AssistantMessage("Thanks, I can build that. ", []einoschema.ToolCall{{
			ID:   "call-plan",
			Type: "function",
			Function: einoschema.FunctionCall{
				Name:      projectToolRequestProjectPlanApproval,
				Arguments: `{"summary":"Build for solo founders","steps":["Create the app"],"targetPaths":["src/"],"acceptanceCriteria":["The app supports the requested audience"]}`,
			},
		}})},
	}}
	setProjectAssistantModelForTest(server, model)
	project := projectWithRepository("demo-repo", "demo", "github")
	project.Name = "demo"
	project.UID = "test-project-uid-demo"
	id := identity{tenantPath: "root:org-a:ws-1", clusterID: "cluster-ws-1", orgUUID: "org-a", workspaceUUID: "ws-1", user: "user@example.com"}
	messageScope := testProjectMessageScope(id.orgUUID, id.workspaceUUID, project.Name)
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
	if resp.Status != store.AssistantRunStatusPendingPermission || resp.AssistantMessage == nil || resp.AssistantMessage.Content != "Thanks, I can build that. " {
		t.Fatalf("resume response = %#v, want plan approval after follow-up", resp)
	}
	updatedMsg, err := server.findProjectMessage(context.Background(), messageScope, assistantMessageID)
	if err != nil {
		t.Fatalf("GetMessage after resume returned error: %v", err)
	}
	if interrupt := projectAssistantUIInterruptFromMetadata(updatedMsg.Metadata[projectMessageMetadataAssistantInterrupt]); interrupt == nil || interrupt.Kind != "permission" || interrupt.Status != "pending" {
		t.Fatalf("updated interrupt = %#v, want pending plan approval", interrupt)
	}
	run, err := messages.GetAssistantRun(context.Background(), messageScope, inputErr.RunID)
	if err != nil {
		t.Fatalf("GetAssistantRun returned error: %v", err)
	}
	if run.Status != store.AssistantRunStatusPendingPermission {
		t.Fatalf("run status = %q, want pending permission", run.Status)
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
	project.UID = "test-project-uid-demo"
	id := identity{tenantPath: "root:org-a:ws-1", clusterID: "cluster-ws-1", orgUUID: "org-a", workspaceUUID: "ws-1", user: "user@example.com"}
	messageScope := testProjectMessageScope(id.orgUUID, id.workspaceUUID, project.Name)
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
	project.UID = "test-project-uid-demo"
	id := identity{tenantPath: "root:org-a:ws-1", clusterID: "cluster-ws-1", orgUUID: "org-a", workspaceUUID: "ws-1", user: "user@example.com"}
	messageScope := testProjectMessageScope(id.orgUUID, id.workspaceUUID, project.Name)
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
	project.UID = "test-project-uid-demo"
	id := identity{tenantPath: "root:org-a:ws-1", clusterID: "cluster-ws-1", orgUUID: "org-a", workspaceUUID: "ws-1", user: "user@example.com"}
	messageScope := testProjectMessageScope(id.orgUUID, id.workspaceUUID, project.Name)
	if err := appendProjectUserMessage(context.Background(), messages, messageScope, "write src/app"); err != nil {
		t.Fatalf("appendProjectUserMessage returned error: %v", err)
	}
	_, err := server.generateProjectAssistantStreamWithStart(
		httptest.NewRequest(http.MethodPost, "/", nil),
		id,
		client,
		project,
		projectAssistantStreamCallbacks{},
		projectAssistantPermissionCheckpointStartForTest(),
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
	if decision.Decision != projectAssistantPermissionAllow || decision.Actor != id.user || decision.ToolName != projectToolWriteFile || decision.Reason != "operation_failed" {
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
			project.UID = "test-project-uid-demo"
			id := identity{tenantPath: "root:org-a:ws-1", clusterID: "cluster-ws-1", orgUUID: "org-a", workspaceUUID: "ws-1"}
			messageScope := testProjectMessageScope(id.orgUUID, id.workspaceUUID, project.Name)
			run := store.AssistantRun{
				ID:         "run-1",
				Status:     status,
				RequestID:  "req-1",
				Checkpoint: json.RawMessage(`{"toolCall":{"id":"call-1"}}`),
			}
			if err := messages.SaveAssistantRun(context.Background(), messageScope, run); err != nil {
				t.Fatalf("SaveAssistantRun returned error: %v", err)
			}
			grant := normalizeProjectAssistantApprovedPlan(projectAssistantApprovedPlan{
				TargetPaths:  []string{"src/"},
				Version:      projectAssistantApprovedPlanVersionWorkspaceMutation,
				Capabilities: []string{projectAssistantCapabilityWorkspaceMutate},
			})
			if err := server.saveProjectAssistantApprovedPlan(context.Background(), messageScope, &grant); err != nil {
				t.Fatalf("saveProjectAssistantApprovedPlan returned error: %v", err)
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
			if len(audit.Decisions) != 1 || audit.Decisions[0].Decision != projectAssistantPermissionDeny || audit.Decisions[0].Reason != "user_aborted" || audit.Outcome != projectAssistantAuditOutcomeAborted {
				t.Fatalf("audit = %#v, want abort decision", audit)
			}
			if grant := server.loadProjectAssistantApprovedPlan(context.Background(), messageScope); grant != nil {
				t.Fatalf("durable grant after abort = %#v, want revoked", grant)
			}
		})
	}
}

func TestAbortProjectAssistantRunUnknownRunPreservesWorkspaceGrant(t *testing.T) {
	messages := store.NewMemoryStore()
	server := NewWithWorkspace(nil, messages, workspace.NewFileStore(t.TempDir()), "", false)
	project := projectWithRepository("demo-repo", "demo", "github")
	project.Name = "demo"
	project.UID = "test-project-uid-demo"
	id := identity{tenantPath: "root:org-a:ws-1", clusterID: "cluster-ws-1", orgUUID: "org-a", workspaceUUID: "ws-1"}
	scope := testProjectMessageScope(id.orgUUID, id.workspaceUUID, project.Name)
	grant := normalizeProjectAssistantApprovedPlan(projectAssistantApprovedPlan{
		TargetPaths:  []string{"src/"},
		Version:      projectAssistantApprovedPlanVersionWorkspaceMutation,
		Capabilities: []string{projectAssistantCapabilityWorkspaceMutate},
	})
	if err := server.saveProjectAssistantApprovedPlan(context.Background(), scope, &grant); err != nil {
		t.Fatalf("saveProjectAssistantApprovedPlan returned error: %v", err)
	}

	if _, err := server.abortProjectAssistantRun(context.Background(), id, project, "missing-run"); err == nil {
		t.Fatal("abortProjectAssistantRun returned nil error for an unknown run")
	}
	if got := server.loadProjectAssistantApprovedPlan(context.Background(), scope); got == nil {
		t.Fatal("unknown abort revoked the active workspace grant")
	}
}

func TestAbortProjectAssistantRunClearsPendingAssistantMessageMetadata(t *testing.T) {
	messages := store.NewMemoryStore()
	server := NewWithWorkspace(nil, messages, workspace.NewFileStore(t.TempDir()), "", false)
	project := projectWithRepository("demo-repo", "demo", "github")
	project.Name = "demo"
	project.UID = "test-project-uid-demo"
	id := identity{tenantPath: "root:org-a:ws-1", clusterID: "cluster-ws-1", orgUUID: "org-a", workspaceUUID: "ws-1"}
	messageScope := testProjectMessageScope(id.orgUUID, id.workspaceUUID, project.Name)
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
	project.UID = "test-project-uid-demo"
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
	fixture := startVerifiedEinoCommitPermissionForTest(t, server, messages, id, project, "commit files", "Committed files.", call)
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
			project.UID = "test-project-uid-demo"
			id := identity{tenantPath: "root:org-a:ws-1", clusterID: "cluster-ws-1", orgUUID: "org-a", workspaceUUID: "ws-1"}
			messageScope := testProjectMessageScope(id.orgUUID, id.workspaceUUID, project.Name)
			firstCall := chatStreamingCall{Index: 0, ID: "call-first-write", Type: "function"}
			firstCall.Function.Name = projectToolWriteFile
			firstCall.Function.Arguments = `{"path":"src/App.tsx","content":"first\n"}`
			// Approving the first write grants every write until the next commit,
			// so the next pause has to come from a tool that still always asks.
			verifyCall := chatStreamingCall{Index: 0, ID: "call-verify-after-write", Type: "function"}
			verifyCall.Function.Name = projectToolVerifyDevelopmentRuntime
			verifyCall.Function.Arguments = `{}`
			secondCall := chatStreamingCall{Index: 0, ID: "call-second-runtime", Type: "function"}
			secondCall.Function.Name = projectToolRestartRuntime
			secondCall.Function.Arguments = `{}`
			model := &repositoryFlowEinoChatModel{Steps: []repositoryFlowEinoModelStep{
				{Message: einoschema.AssistantMessage("", projectEinoToolCallsFromStreamingForTest([]chatStreamingCall{firstCall}))},
				{Message: einoschema.AssistantMessage("", projectEinoToolCallsFromStreamingForTest([]chatStreamingCall{verifyCall}))},
				{Message: einoschema.AssistantMessage("First change applied. ", projectEinoToolCallsFromStreamingForTest([]chatStreamingCall{secondCall}))},
			}}
			setProjectAssistantModelWithVerificationResultForTest(server, model, `{"status":"provisioning"}`)
			settings := projectLLMSettings{Provider: defaultProjectLLMProvider, BaseURL: defaultProjectLLMBaseURL, Model: "test-model", APIKey: "test-key"}
			client := asclient.NewFromDynamic(projectSettingsDynamicClient{secret: projectLLMSettingsSecret(settings)})
			if err := appendProjectUserMessage(context.Background(), messages, messageScope, "write files"); err != nil {
				t.Fatalf("appendProjectUserMessage returned error: %v", err)
			}
			var firstPermission projectAssistantPermission
			var firstCheckpoint projectAssistantCheckpoint
			_, err := server.generateProjectAssistantStreamWithStart(
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
				projectAssistantPermissionCheckpointStartForTest(),
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

func TestResumeProjectAssistantRunDirectWriteApprovalRepromptsForDifferentPath(t *testing.T) {
	messages := store.NewMemoryStore()
	workspaces := workspace.NewFileStore(t.TempDir())
	server := NewWithWorkspace(nil, messages, workspaces, "", false)
	project := projectWithRepository("demo-repo", "demo", "github")
	project.Name = "demo"
	project.UID = "test-project-uid-demo"
	id := identity{tenantPath: "root:org-a:ws-1", orgUUID: "org-a", workspaceUUID: "ws-1"}
	messageScope := testProjectMessageScope(id.orgUUID, id.workspaceUUID, project.Name)
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
		{Message: einoschema.AssistantMessage("", []einoschema.ToolCall{{
			ID:   "call-verify",
			Type: "function",
			Function: einoschema.FunctionCall{
				Name:      projectToolVerifyDevelopmentRuntime,
				Arguments: `{}`,
			},
		}})},
		{Message: einoschema.AssistantMessage("All done.", nil)},
	}}
	setProjectAssistantModelWithReachableVerificationForTest(server, model)
	settings := projectLLMSettings{Provider: defaultProjectLLMProvider, BaseURL: defaultProjectLLMBaseURL, Model: "test-model", APIKey: "test-key"}
	client := asclient.NewFromDynamic(projectSettingsDynamicClient{secret: projectLLMSettingsSecret(settings)})
	if err := appendProjectUserMessage(context.Background(), messages, messageScope, "write files"); err != nil {
		t.Fatalf("appendProjectUserMessage returned error: %v", err)
	}

	_, err := server.generateProjectAssistantStreamWithStart(
		httptest.NewRequest(http.MethodPost, "/", nil),
		id,
		client,
		project,
		projectAssistantStreamCallbacks{},
		projectAssistantPermissionCheckpointStartForTest(),
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
	if resp.Status != store.AssistantRunStatusPendingPermission {
		t.Fatalf("resume status = %q, want %q for a different write path", resp.Status, store.AssistantRunStatusPendingPermission)
	}

	files, err := workspaces.ListFiles(context.Background(), workspaceScope, workspace.ListOptions{})
	if err != nil {
		t.Fatalf("ListFiles returned error: %v", err)
	}
	written := map[string]bool{}
	for _, f := range files.Files {
		written[f.Path] = true
	}
	if !written["src/App.tsx"] || written["src/Other.tsx"] {
		t.Fatalf("written files = %v, want only the directly approved src/App.tsx", written)
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
	project.UID = "test-project-uid-demo"
	id := identity{tenantPath: "root:org-a:ws-1", clusterID: "cluster-ws-1", orgUUID: "org-a", workspaceUUID: "ws-1", user: "user@example.com"}
	messageScope := testProjectMessageScope(id.orgUUID, id.workspaceUUID, project.Name)
	workspaceScope := projectWorkspaceScope(id, project.Name)
	if err := workspaces.ApplyFiles(context.Background(), workspaceScope, []workspace.File{{Path: "src/App.tsx", Content: "approved\n"}}); err != nil {
		t.Fatalf("ApplyFiles returned error: %v", err)
	}
	call := chatStreamingCall{Index: 0, ID: "call-commit", Type: "function"}
	call.Function.Name = projectToolCommitProjectFiles
	call.Function.Arguments = `{"repositoryRef":"old-repo","paths":["src/App.tsx"],"message":"Initial app"}`
	fixture := startVerifiedEinoCommitPermissionForTest(t, server, messages, id, project, "commit files", "Committed files.", call)
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
	if len(audit.Decisions) != 1 || audit.Decisions[0].Reason != "stale_repository_binding" {
		t.Fatalf("audit = %#v, want stale binding error", audit)
	}
	if audit.Outcome != projectAssistantAuditOutcomeFailed || audit.StartedAt.IsZero() {
		t.Fatalf("audit lifecycle = %#v, want finalized failed outcome", audit)
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
	updatedActions := projectAssistantActionFeedFromMetadata(updatedMessage.Metadata[projectMessageMetadataAssistantActionFeed])
	if len(updatedActions) != 1 || updatedActions[0].Status != "failed" {
		t.Fatalf("updated actions = %#v, want failed stale binding action", updatedActions)
	}
	if updatedActions[0].Diagnostic == nil || updatedActions[0].Diagnostic.Category != "validation" ||
		strings.Contains(updatedActions[0].Diagnostic.Message, "repository binding changed") {
		t.Fatalf("updated action diagnostic = %#v, want allowlisted validation detail", updatedActions[0].Diagnostic)
	}
}

func TestResumeProjectAssistantRunPreemptsToolBatchAfterApprovedPermission(t *testing.T) {
	settings := projectLLMSettings{Provider: defaultProjectLLMProvider, BaseURL: defaultProjectLLMBaseURL, Model: "test-model", APIKey: "test-key"}
	client := asclient.NewFromDynamic(projectSettingsDynamicClient{secret: projectLLMSettingsSecret(settings)})
	messages := store.NewMemoryStore()
	id := identity{tenantPath: "root:org-a:ws-1", clusterID: "cluster-ws-1", orgUUID: "org-a", workspaceUUID: "ws-1", user: "user@example.com"}
	messageScope := testProjectMessageScope(id.orgUUID, id.workspaceUUID, "demo")
	if err := appendProjectUserMessage(context.Background(), messages, messageScope, "write files"); err != nil {
		t.Fatalf("appendProjectUserMessage returned error: %v", err)
	}
	workspaces := workspace.NewFileStore(t.TempDir())
	server := NewWithWorkspace(nil, messages, workspaces, "", false)
	model := &repositoryFlowEinoChatModel{Steps: []repositoryFlowEinoModelStep{
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
		{Message: einoschema.AssistantMessage("", []einoschema.ToolCall{{
			ID:   "call-verify",
			Type: "function",
			Function: einoschema.FunctionCall{
				Name:      projectToolVerifyDevelopmentRuntime,
				Arguments: `{}`,
			},
		}})},
		{Message: einoschema.AssistantMessage("First approval completed.", nil)},
	}}
	project := projectWithRepository("demo-repo", "demo", "github")
	project.Name = "demo"
	project.UID = "test-project-uid-demo"
	setProjectAssistantModelWithReachableVerificationForTest(server, model)

	_, err := server.generateProjectAssistantStreamWithStart(
		httptest.NewRequest(http.MethodPost, "/", nil),
		id,
		client,
		project,
		projectAssistantStreamCallbacks{},
		projectAssistantPermissionCheckpointStartForTest(),
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
	messageScope := testProjectMessageScope(id.orgUUID, id.workspaceUUID, "demo")
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
			Message: einoschema.AssistantMessage("", []einoschema.ToolCall{{
				ID:   "call-verify",
				Type: "function",
				Function: einoschema.FunctionCall{
					Name:      projectToolVerifyDevelopmentRuntime,
					Arguments: `{}`,
				},
			}}),
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
		{Message: einoschema.AssistantMessage("I wrote src/App.tsx after approval.", nil)},
	}}
	setProjectAssistantModelWithReachableVerificationForTest(server, model)
	project := projectWithRepository("demo-repo", "demo", "github")
	project.Name = "demo"
	project.UID = "test-project-uid-demo"

	_, err := server.generateProjectAssistantStreamWithStart(
		httptest.NewRequest(http.MethodPost, "/", nil),
		id,
		client,
		project,
		projectAssistantStreamCallbacks{},
		projectAssistantPermissionCheckpointStartForTest(),
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
	if len(model.Inputs) != 3 {
		t.Fatalf("Eino model request count = %d, want initial request plus bounded resumed retry", len(model.Inputs))
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

func TestGenerateProjectAssistantStreamPropagatesRepeatedToolLoopLimit(t *testing.T) {
	closing := projectAssistantBoundedClosingAnswerForTest("I inspected src/App.tsx.")
	reply, requests, err := runRepeatedReadFileAssistantStream(t, closing)
	if !errors.Is(err, errProjectAssistantNoProgress) {
		t.Fatalf("generateProjectAssistantStream error = %v after %d requests, want no-progress limit", err, len(requests))
	}
	if reply != closing {
		t.Fatalf("reply = %q, want bounded closing answer", reply)
	}
	if got := projectAssistantToolBearingRequestCount(requests); got != projectEinoAssistantApprovalModelCallLimit {
		t.Fatalf("tool-bearing LLM request count = %d, want %d", got, projectEinoAssistantApprovalModelCallLimit)
	}
	if len(requests) != projectEinoAssistantApprovalModelCallLimit+1 {
		t.Fatalf("LLM request count = %d, want tool-bearing calls plus one closing call", len(requests))
	}
	if last := requests[len(requests)-1]; len(last.Tools) != 0 || last.ToolChoice != "none" {
		t.Fatalf("closing LLM request = %#v, want tools disabled", last)
	}
}

func TestGenerateProjectAssistantStreamFallsBackWhenBoundedClosingAnswerIsEmpty(t *testing.T) {
	reply, requests, err := runRepeatedReadFileAssistantStream(t, "")
	if !errors.Is(err, errProjectAssistantNoProgress) {
		t.Fatalf("generateProjectAssistantStream error = %v after %d requests, want no-progress limit", err, len(requests))
	}
	if !projectEinoAssistantBoundedClosingAnswerValid(reply) ||
		!strings.Contains(reply, "I inspected file read") {
		t.Fatalf("reply = %q, want evidence-based fallback", reply)
	}
	if got := projectAssistantToolBearingRequestCount(requests); got != projectEinoAssistantApprovalModelCallLimit {
		t.Fatalf("tool-bearing LLM request count = %d, want %d", got, projectEinoAssistantApprovalModelCallLimit)
	}
}

func TestGenerateProjectAssistantStreamMakesFinalNoToolRequestAtLimit(t *testing.T) {
	closing := projectAssistantBoundedClosingAnswerForTest("I inspected the requested files.")
	reply, requests, err := runUniqueReadFileAssistantStream(t, closing)
	if !errors.Is(err, errProjectAssistantNoProgress) {
		t.Fatalf("generateProjectAssistantStream error = %v after %d requests, want no-progress limit", err, len(requests))
	}
	if reply != closing {
		t.Fatalf("reply = %q, want bounded closing answer", reply)
	}
	if got := projectAssistantToolBearingRequestCount(requests); got != projectEinoAssistantApprovalModelCallLimit {
		t.Fatalf("tool-bearing LLM request count = %d, want %d", got, projectEinoAssistantApprovalModelCallLimit)
	}
	if len(requests) != projectEinoAssistantApprovalModelCallLimit+1 {
		t.Fatalf("LLM request count = %d, want tool-bearing calls plus one closing call", len(requests))
	}
	if last := requests[len(requests)-1]; len(last.Tools) != 0 || last.ToolChoice != "none" {
		t.Fatalf("closing LLM request = %#v, want tools disabled", last)
	}
}

func TestGenerateProjectAssistantStreamClosesMaxIterationWithToolsDisabled(t *testing.T) {
	closing := projectAssistantBoundedClosingAnswerForTest("I inspected the project.")
	reply, requests, err := runMaxIterationReadFileAssistantStream(t, closing)
	if !errors.Is(err, adk.ErrExceedMaxIterations) {
		t.Fatalf("generateProjectAssistantStream error = %v after %d requests, want max-iteration limit", err, len(requests))
	}
	if reply != closing {
		t.Fatalf("reply = %q, want bounded closing answer", reply)
	}
	if got := projectAssistantToolBearingRequestCount(requests); got != maxAssistantDeepIterations {
		t.Fatalf("tool-bearing LLM request count = %d, want %d", got, maxAssistantDeepIterations)
	}
	if len(requests) != maxAssistantDeepIterations+1 {
		t.Fatalf("LLM request count = %d, want tool-bearing calls plus one closing call", len(requests))
	}
	if last := requests[len(requests)-1]; len(last.Tools) != 0 || last.ToolChoice != "none" {
		t.Fatalf("closing LLM request = %#v, want tools disabled", last)
	}
}

func projectAssistantToolBearingRequestCount(requests []chatCompletionRequest) int {
	count := 0
	for _, request := range requests {
		if len(request.Tools) > 0 {
			count++
		}
	}
	return count
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

func TestGenerateProjectAssistantStreamRejectsUnverifiedCommitProjectFiles(t *testing.T) {
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
	}, {
		Message: einoschema.AssistantMessage("Commit is unavailable until verification succeeds.", nil),
	}, {
		Message: einoschema.AssistantMessage("Commit is unavailable until verification succeeds.", nil),
	}}}
	_, requests, err := runProjectAssistantStreamWithModel(t, model, mcp.URL)
	if err != nil {
		t.Fatalf("generateProjectAssistantStream returned error: %v", err)
	}
	if commitCalls != 0 {
		t.Fatalf("commit call count = %d, want unverified commit denied before execution", commitCalls)
	}
	if len(requests) != 2 {
		t.Fatalf("LLM request count = %d, want denial result followed by a report", len(requests))
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
		nil,
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
		nil,
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
	steps := make([]repositoryFlowEinoModelStep, 0, projectEinoAssistantApprovalModelCallLimit+1)
	for i := 1; i <= projectEinoAssistantApprovalModelCallLimit; i++ {
		steps = append(steps, repositoryFlowEinoModelStep{Message: einoschema.AssistantMessage("", []einoschema.ToolCall{{
			ID:   fmt.Sprintf("call-%d", i),
			Type: "function",
			Function: einoschema.FunctionCall{
				Name:      projectToolReadFile,
				Arguments: `{"file_path":"src/App.tsx","offset":1,"limit":200}`,
			},
		}})})
	}
	steps = append(steps, repositoryFlowEinoModelStep{Message: einoschema.AssistantMessage(finalAnswer, nil)})
	model := &repositoryFlowEinoChatModel{Steps: steps}
	return runProjectAssistantStreamWithModel(t, model, "")
}

func runUniqueReadFileAssistantStream(t *testing.T, finalAnswer string) (string, []chatCompletionRequest, error) {
	t.Helper()
	steps := make([]repositoryFlowEinoModelStep, 0, projectEinoAssistantApprovalModelCallLimit+1)
	for i := 1; i <= projectEinoAssistantApprovalModelCallLimit; i++ {
		steps = append(steps, repositoryFlowEinoModelStep{Message: einoschema.AssistantMessage("", []einoschema.ToolCall{{
			ID:   fmt.Sprintf("call-%d", i),
			Type: "function",
			Function: einoschema.FunctionCall{
				Name:      projectToolReadFile,
				Arguments: fmt.Sprintf(`{"file_path":"src/file-%d.tsx","offset":1,"limit":200}`, i),
			},
		}})})
	}
	steps = append(steps, repositoryFlowEinoModelStep{Message: einoschema.AssistantMessage(finalAnswer, nil)})
	model := &repositoryFlowEinoChatModel{Steps: steps}
	return runProjectAssistantStreamWithModel(t, model, "")
}

func runMaxIterationReadFileAssistantStream(t *testing.T, finalAnswer string) (string, []chatCompletionRequest, error) {
	t.Helper()
	steps := make([]repositoryFlowEinoModelStep, 0, maxAssistantDeepIterations+1)
	for i := 1; i <= maxAssistantDeepIterations; i++ {
		steps = append(steps, repositoryFlowEinoModelStep{Message: einoschema.AssistantMessage("", []einoschema.ToolCall{{
			ID:   fmt.Sprintf("call-%d", i),
			Type: "function",
			Function: einoschema.FunctionCall{
				Name:      projectToolReadFile,
				Arguments: fmt.Sprintf(`{"file_path":"src/file-%d.tsx","offset":1,"limit":200}`, i),
			},
		}})})
	}
	steps = append(steps, repositoryFlowEinoModelStep{Message: einoschema.AssistantMessage(finalAnswer, nil)})
	model := &repositoryFlowEinoChatModel{Steps: steps}
	return runProjectAssistantStreamWithModelAndPrompt(t, model, "", "What files and components are in this project?")
}

func runProjectAssistantStreamWithModel(t *testing.T, model *repositoryFlowEinoChatModel, hubBase string) (string, []chatCompletionRequest, error) {
	t.Helper()
	return runProjectAssistantStreamWithModelAndPrompt(t, model, hubBase, "write a hello app")
}

func runProjectAssistantStreamWithModelAndPrompt(t *testing.T, model *repositoryFlowEinoChatModel, hubBase, prompt string) (string, []chatCompletionRequest, error) {
	t.Helper()

	settings := projectLLMSettings{
		Provider: defaultProjectLLMProvider,
		BaseURL:  defaultProjectLLMBaseURL,
		Model:    "test-model",
		APIKey:   "test-key",
	}
	client := asclient.NewFromDynamic(projectSettingsDynamicClient{secret: projectLLMSettingsSecret(settings)})
	messages := store.NewMemoryStore()
	scope := store.Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: "demo", ProjectUID: "test-project-uid-demo"}
	if err := appendProjectUserMessage(context.Background(), messages, scope, prompt); err != nil {
		t.Fatalf("appendProjectUserMessage returned error: %v", err)
	}
	workspaces := workspace.NewFileStore(t.TempDir())
	seedFiles := []workspace.File{{Path: "src/App.tsx", Content: "export default function App() { return <main>Hello</main> }\n"}}
	for i := 1; i <= maxAssistantDeepIterations+1; i++ {
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
	project.UID = "test-project-uid-demo"
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
		nil,
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
		nil,
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
