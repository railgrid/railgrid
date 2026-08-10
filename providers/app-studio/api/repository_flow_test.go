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
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

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
		"exec_command",
		"ask_follow_up",
		"create_file",
		"replace_file",
		"edit_file",
		"delete_file",
		"move_file",
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
	for _, mutation := range []string{projectToolCreateFile, projectToolReplaceFile, projectToolEditFile, projectToolDeleteFile, projectToolMoveFile} {
		if !registry.Has(mutation) {
			t.Fatalf("App Studio mutation tool %q is missing", mutation)
		}
	}
	followUp, ok := registry.Get(projectToolAskFollowUp)
	if !ok {
		t.Fatalf("App Studio input tool %q is missing", projectToolAskFollowUp)
	}
	for _, want := range []string{
		"Request user input for one to three short questions and wait for the response",
		"make reasonable assumptions and continue",
	} {
		if !strings.Contains(followUp.Spec().Description, want) {
			t.Fatalf("ask_follow_up description missing %q: %s", want, followUp.Spec().Description)
		}
	}
}

func TestProjectEinoFollowUpToolResultRequiresForwardProgress(t *testing.T) {
	result := projectEinoFollowUpToolResult(map[string]projectAssistantFollowUpAnswer{
		"scope": {Answers: []string{"Full storefront (Recommended)"}},
	})
	decoded := map[string]any{}
	if err := json.Unmarshal([]byte(result), &decoded); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"answers": map[string]any{"scope": map[string]any{"answers": []any{"Full storefront (Recommended)"}}}}
	if !reflect.DeepEqual(decoded, want) {
		t.Fatalf("follow-up result = %#v, want %#v", decoded, want)
	}
}

func TestProjectAssistantToolRegistryListsLocalToolsInOrder(t *testing.T) {
	registry := projectAssistantLocalToolRegistry(nil)
	got := projectChatToolNames(registry.ChatTools(false))
	want := []string{
		"load_skill",
		"read_skill_resource",
		"define_initial_project_plan",
		"ask_follow_up",
		"read_file",
		"web_search",
		"web_fetch",
		"create_file",
		"replace_file",
		"edit_file",
		"delete_file",
		"move_file",
		"select_project_template",
		"get_project_checkpoints",
		"check_project_build",
		"get_build_logs",
		"rebuild_project",
		"promote_project",
		"inspect_development_preview",
		"interact_development_preview",
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
		"exec_command",
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("tool names = %v, want %v", got, want)
	}

	all := projectChatToolNames(registry.ChatTools(true))
	wantAll := append([]string(nil), want[:18]...)
	wantAll = append(wantAll, "commit_project_files")
	wantAll = append(wantAll, want[18:]...)
	if strings.Join(all, ",") != strings.Join(wantAll, ",") {
		t.Fatalf("tool names with commit bridge = %v, want %v", all, wantAll)
	}
	if !registry.Has(" COMMIT_PROJECT_FILES ") {
		t.Fatal("registry should match tool names case-insensitively")
	}
	tool, ok := registry.Get(projectToolEditFile)
	if !ok {
		t.Fatal("edit_file missing from registry")
	}
	if got := tool.Spec().Risk; got != projectAssistantToolRiskWrite {
		t.Fatalf("edit_file risk = %q, want %q", got, projectAssistantToolRiskWrite)
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

	reply, err := generateRepositoryFlowBuildAssistantForTest(t, server,
		httptest.NewRequest(http.MethodPost, "/", nil),
		id,
		client,
		project,
		projectAssistantStreamCallbacks{},
		nil,
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
	if !projectChatToolsInclude(model.Inputs[0].Tools, projectToolCommitProjectFiles) {
		t.Fatalf("model tools = %#v, want the V2 commit tool advertised; invocation validates current-run mutation and verification evidence", model.Inputs[0].Tools)
	}
	if !projectChatToolsInclude(model.Inputs[0].Tools, projectToolDefineInitialProjectPlan) {
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

	reply, err := generateRepositoryFlowBuildAssistantForTest(t, server,
		httptest.NewRequest(http.MethodPost, "/", nil),
		id,
		client,
		project,
		projectAssistantStreamCallbacks{},
		nil,
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
		"server-side provider-neutral Kedge Actions SDK",
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

	prompt := projectSystemPromptForMode(project, repository, projectAssistantCollaborationModeDefault, false)
	for _, want := range []string{
		"Use the current project snapshot first, then bounded reads and searches",
		"Repair-or-stop cadence after a failed preview/API/network/console/provider observation",
		"at most one targeted fresh read/search answering a new question",
		"one bounded repair attempt using authorized version-checked mutations",
		"rerun the original failed observation once",
		"Never claim recovery without later success evidence from rerunning that same observation",
		"The source-mutation tools are create_file, replace_file, edit_file, delete_file, and move_file",
		"use verify_development_runtime only when that evidence is relevant",
		"Never call commit_project_files unless the user explicitly requested repository persistence",
	} {
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
	for _, want := range []string{
		"## User-visible progress",
		"assistant preamble immediately before a substantial action group is user-visible inline commentary",
		"normal assistant response remains the terminal final answer",
		"Before the first substantial action group, give one concise preamble",
		"approximately 60 seconds",
		"completing a meaningful plan phase",
		"new evidence changes the approach",
		"you encounter a blocker",
		"before and after lengthy verification",
		"when there is no natural tool-adjacent preamble",
		"Do not duplicate the same update in report_progress and inline commentary",
		"Skip progress for trivial reads",
		"does not end or interrupt the turn",
		"If report_progress is unavailable, continue without it",
		"one or two concise sentences",
		"use it as the sole authority for checklist state in non-trivial Default mode work",
		"report_progress is only user-facing commentary; it never updates or replaces the checklist",
		"Every model-authored checklist change must be a full-list write_todos update",
		"Immediately after defining or receiving a plan, write the full list with evidence-grounded statuses",
		"Before moving to another phase, write the full list again; mark a step completed only when current direct evidence supports it",
		"For blocked or unfinished work, use pending (a non-complete status) and never invent a blocked status",
		"After verification changes completion evidence, immediately write the full list again",
		"Immediately before the terminal response, write the full list one final time",
		"Runtime readiness, HTTP 200, and preview reachability are evidence only for those narrow conditions",
		"cannot alone complete implementation or application-behavior steps",
		"Do not infer broader completion from them or any other indirect status",
		"Do not name tools in user-visible progress, expose hidden reasoning",
		"raw arguments, raw results, logs, or secrets",
		"do not repeat the plan, checklist, or status UI",
		"Keep tool-adjacent commentary outcome-oriented",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing milestone and per-tool narration guidance %q:\n%s", want, prompt)
		}
	}
	if !strings.Contains(prompt, "Diagnose reported defects from current evidence before editing") {
		t.Fatalf("prompt missing evidence-before-edit guidance:\n%s", prompt)
	}
}

func TestProjectAssistantDoesNotAdvertiseLegacyRuntimeCommandTools(t *testing.T) {
	registry := projectAssistantLocalToolRegistry(nil)
	toolNames := strings.Join(projectChatToolNames(registry.ChatTools(true)), "\n")
	project := projectWithRepository("demo-repo", "demo", "github")
	project.Name = "demo-project"
	project.UID = "test-project-uid-demo-project"
	prompt := projectSystemPromptForMode(project, &ProjectRepositoryView{
		Ref:    "demo-repo",
		Name:   "demo",
		Status: projectRepositoryStatusReady,
		Ready:  true,
	}, projectAssistantCollaborationModeDefault, false)
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
		{name: "edit_file", args: `{"path":"src/App.tsx","oldString":"secret-ish","newString":"new"}`, want: []string{"path src/App.tsx"}},
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
			if tt.name == "edit_file" && strings.Contains(got, "secret-ish") {
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

	mutationResult := `{"operation":"edit_file","path":"src/App.tsx","additions":2,"deletions":1,"replacements":1,"diff":"secret-ish body"}`
	got := summarizeProjectToolResult("edit_file", mutationResult)
	for _, want := range []string{"edit_file", "src/App.tsx", "+2", "-1", "1 replacement(s)"} {
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

func TestProjectLocalToolRunsCreateFile(t *testing.T) {
	workspaces := workspace.NewFileStore(t.TempDir())
	scope := workspace.Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: "demo", ProjectUID: "test-project-uid"}
	server := NewWithWorkspace(nil, nil, workspaces, "", false)

	tool, ok := server.projectAssistantToolRegistry().Get(projectToolCreateFile)
	if !ok {
		t.Fatalf("%s missing from registry", projectToolCreateFile)
	}
	if _, err := tool.Call(context.Background(), projectAssistantToolCallRequest{
		WorkspaceScope: scope,
		HTTPRequest:    httptest.NewRequest(http.MethodPost, "/", nil),
		Arguments:      map[string]any{"path": "src/App.tsx", "content": "test\n"},
	}); err != nil {
		t.Fatalf("%s returned error: %v", projectToolCreateFile, err)
	}
	read, err := workspaces.ReadFile(context.Background(), scope, workspace.ReadOptions{Path: "src/App.tsx"})
	if err != nil {
		t.Fatalf("ReadFile returned error: %v", err)
	}
	if read.Content != "test\n" {
		t.Fatalf("workspace content = %q", read.Content)
	}
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
	Steps    []repositoryFlowEinoModelStep
	Inputs   []chatCompletionRequest
	nextStep int
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

	if len(input) > 0 && input[len(input)-1] != nil && input[len(input)-1].Role == einoschema.User &&
		input[len(input)-1].Content == projectEinoAssistantCompactionPrompt {
		message := einoschema.AssistantMessage("The assistant is inspecting project files and should continue from the latest completed tool result.", nil)
		callbacks.OnEnd(ctx, &einomodel.CallbackOutput{Message: message})
		return message, nil
	}
	index := m.nextStep
	m.nextStep++
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
	if msg.ResponseMeta == nil {
		finishReason := "stop"
		if len(msg.ToolCalls) > 0 {
			finishReason = "tool_calls"
		}
		msg.ResponseMeta = &einoschema.ResponseMeta{FinishReason: finishReason}
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
}

func setProjectAssistantModelWithReadyVerificationForTest(server *Server, model einomodel.BaseChatModel) {
	setProjectAssistantModelWithVerificationResultForTest(server, model, `{"status":"ready"}`)
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
}

func setRepositoryFlowApprovalModeForTest(t *testing.T, server *Server, scope store.Scope, actor string, mode store.AssistantApprovalMode) {
	t.Helper()
	if _, err := server.store.SetAssistantApprovalPreference(context.Background(), scope, store.AssistantApprovalPreference{
		ActorID: actor,
		Mode:    mode,
	}); err != nil {
		t.Fatalf("SetAssistantApprovalPreference: %v", err)
	}
}

func generateRepositoryFlowBuildAssistantForTest(
	t *testing.T,
	server *Server,
	r *http.Request,
	id identity,
	client *asclient.Client,
	project *aiv1alpha1.Project,
	callbacks projectAssistantStreamCallbacks,
	start *projectAssistantStreamStart,
) (string, error) {
	return generateRepositoryFlowAssistantForModeTest(t, server, r, id, client, project, callbacks, start, store.AssistantRunModeDefault)
}

func generateRepositoryFlowAssistantForModeTest(
	t *testing.T,
	server *Server,
	r *http.Request,
	id identity,
	client *asclient.Client,
	project *aiv1alpha1.Project,
	callbacks projectAssistantStreamCallbacks,
	start *projectAssistantStreamStart,
	mode store.AssistantRunMode,
) (string, error) {
	t.Helper()
	if strings.TrimSpace(id.user) == "" {
		id.user = "user@example.com"
	}
	scope := testProjectMessageScope(id.orgUUID, id.workspaceUUID, project.Name)
	setRepositoryFlowApprovalModeForTest(t, server, scope, id.user, store.AssistantApprovalModeAlwaysAsk)
	prompt := "Exercise the repository flow."
	if recent, err := server.store.LoadRecentMessages(context.Background(), scope, 1); err == nil && len(recent) == 1 && strings.TrimSpace(recent[0].Content) != "" {
		prompt = recent[0].Content
	}
	started, err := server.startProjectAssistantRunDurablyWithMode(
		context.Background(),
		scope,
		id.user,
		prompt,
		"repository-flow-direct-"+newMessageID(),
		mode,
		func(run store.AssistantRun, assistant store.Message, _ bool) error {
			_, attachErr := server.projectAssistantSupervisor().Attach(scope, run, assistant)
			return attachErr
		},
	)
	if err != nil {
		t.Fatalf("start repository-flow build run: %v", err)
	}
	ctx := context.WithValue(r.Context(), projectAssistantSupervisorRunContextKey{}, started.Run)
	return server.generateProjectAssistantStreamWithStart(r.WithContext(ctx), id, client, project, callbacks, start)
}

func resumeRepositoryFlowAssistantForTest(server *Server, ctx context.Context, r *http.Request, id identity, client *asclient.Client, project *aiv1alpha1.Project, repository *ProjectRepositoryView, runID string, req projectAssistantResumeRequest) (projectAssistantResumeResponse, error) {
	scope := projectMessageScope(id.orgUUID, id.workspaceUUID, project)
	run, err := server.store.GetAssistantRun(ctx, scope, runID)
	if err != nil {
		return projectAssistantResumeResponse{}, err
	}
	if _, _, err := preflightProjectAssistantResume(run, req); err != nil {
		return projectAssistantResumeResponse{}, err
	}
	return server.resumeProjectAssistantRunWithRepositoryAndClient(ctx, r, id, client, project, repository, runID, req)
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
		projectAssistantMetadataWorkingStatus: projectMessageStatusPendingPermission,
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

func TestResumeProjectAssistantRunAnswersFollowUpAndUpdatesMessage(t *testing.T) {
	settings := projectLLMSettings{Provider: defaultProjectLLMProvider, BaseURL: defaultProjectLLMBaseURL, Model: "test-model", APIKey: "test-key"}
	client := asclient.NewFromDynamic(projectSettingsDynamicClient{secret: projectLLMSettingsSecret(settings)})
	messages := store.NewMemoryStore()
	server := NewWithWorkspace(nil, messages, workspace.NewFileStore(t.TempDir()), "", false)
	var resumedModelInput []*einoschema.Message
	model := &repositoryFlowEinoChatModel{Steps: []repositoryFlowEinoModelStep{
		{Message: einoschema.AssistantMessage("", []einoschema.ToolCall{{
			ID:   "call-follow-up",
			Type: "function",
			Function: einoschema.FunctionCall{
				Name:      projectToolAskFollowUp,
				Arguments: `{"questions":[{"id":"audience","header":"Audience","question":"What audience should this app target?","options":[{"label":"Solo founders (Recommended)","description":"Optimize the first version for individual founders."},{"label":"Sales teams","description":"Include collaboration-oriented workflows."}]}]}`,
			},
		}})},
		{Inspect: func(input []*einoschema.Message) {
			resumedModelInput = append([]*einoschema.Message(nil), input...)
		}, Message: einoschema.AssistantMessage("Thanks, I can build that. ", []einoschema.ToolCall{{
			ID:   "call-plan",
			Type: "function",
			Function: einoschema.FunctionCall{
				Name:      projectToolDefineInitialProjectPlan,
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
	_, err := generateRepositoryFlowAssistantForModeTest(t, server,
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
		nil,
		store.AssistantRunModePlan,
	)
	var inputErr *projectAssistantInputRequiredError
	if !errors.As(err, &inputErr) {
		t.Fatalf("generateProjectAssistantStream error = %v, want input required", err)
	}
	if inputErr.RunID == "" || inputErr.RequestID == "" || followUp.ID == "" || checkpoint.ID == "" {
		t.Fatalf("input error=%#v followUp=%#v checkpoint=%#v, want resumable follow-up", inputErr, followUp, checkpoint)
	}
	if len(followUp.Questions) != 1 || followUp.Questions[0].ID != "audience" || len(followUp.Questions[0].Options) != 2 || !followUp.Questions[0].IsOther {
		t.Fatalf("follow-up questions = %#v, want Codex-style structured question", followUp.Questions)
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

	resp, err := resumeRepositoryFlowAssistantForTest(server,
		context.Background(),
		httptest.NewRequest(http.MethodPost, "/", nil),
		id,
		client,
		project,
		&ProjectRepositoryView{Ref: "demo-repo", Name: "demo", Status: projectRepositoryStatusReady},
		inputErr.RunID,
		projectAssistantResumeRequest{
			RequestID: inputErr.RequestID,
			Answers: map[string]projectAssistantFollowUpAnswer{
				"audience": {Answers: []string{"Solo founders (Recommended)"}},
			},
			AssistantMessageID: assistantMessageID,
		},
	)
	if err != nil {
		t.Fatalf("resumeProjectAssistantRun returned error: %v", err)
	}
	var followUpResult map[string]any
	for _, message := range resumedModelInput {
		if message == nil || message.Role != einoschema.Tool || message.ToolCallID != "call-follow-up" || !json.Valid([]byte(message.Content)) {
			continue
		}
		if err := json.Unmarshal([]byte(message.Content), &followUpResult); err != nil {
			t.Fatal(err)
		}
		break
	}
	wantFollowUpResult := map[string]any{"answers": map[string]any{"audience": map[string]any{"answers": []any{"Solo founders (Recommended)"}}}}
	if !reflect.DeepEqual(followUpResult, wantFollowUpResult) {
		t.Fatalf("follow-up function output = %#v, want %#v", followUpResult, wantFollowUpResult)
	}
	if resp.Status != store.AssistantRunStatusCompleted || resp.AssistantMessage == nil || resp.AssistantContent != "Done." {
		t.Fatalf("resume response = %#v, want completed V2 plan update after follow-up", resp)
	}
	progress, ok := projectAssistantProgressSnapshotFromMetadata(resp.AssistantMessage.Metadata[projectAssistantMetadataProgress])
	if ok && (len(progress.Messages) != 1 || progress.Messages[0] != "Thanks, I can build that.") {
		t.Fatalf("resume progress = %#v, want one sanitized tool-adjacent commentary", progress)
	}
	updatedMsg, err := server.findProjectMessage(context.Background(), messageScope, assistantMessageID)
	if err != nil {
		t.Fatalf("GetMessage after resume returned error: %v", err)
	}
	if interrupt := projectAssistantUIInterruptFromMetadata(updatedMsg.Metadata[projectMessageMetadataAssistantInterrupt]); interrupt != nil {
		t.Fatalf("updated interrupt = %#v, want completed follow-up cleared", interrupt)
	}
	run, err := messages.GetAssistantRun(context.Background(), messageScope, inputErr.RunID)
	if err != nil {
		t.Fatalf("GetAssistantRun returned error: %v", err)
	}
	if run.Status != store.AssistantRunStatusRunning {
		t.Fatalf("run status = %q, want running until the supervisor commits the terminal response", run.Status)
	}
	conversation, err := loadProjectAssistantConversation(context.Background(), messages, messageScope)
	if err != nil {
		t.Fatal(err)
	}
	if len(conversation) == 0 || conversation[len(conversation)-1].Role != "assistant" || conversation[len(conversation)-1].Content != "Done." {
		t.Fatalf("resumed final response was not persisted in canonical conversation history: %#v", conversation)
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
	_, err := generateRepositoryFlowAssistantForModeTest(t, server,
		httptest.NewRequest(http.MethodPost, "/", nil),
		id,
		client,
		project,
		projectAssistantStreamCallbacks{},
		nil,
		store.AssistantRunModePlan,
	)
	var inputErr *projectAssistantInputRequiredError
	if !errors.As(err, &inputErr) {
		t.Fatalf("generateProjectAssistantStream error = %v, want input required", err)
	}

	for _, attempt := range []struct {
		name      string
		request   projectAssistantResumeRequest
		wantError string
	}{
		{
			name:      "empty answer",
			request:   projectAssistantResumeRequest{RequestID: inputErr.RequestID, Answer: "   "},
			wantError: "answer is required",
		},
		{
			name:      "mismatched request",
			request:   projectAssistantResumeRequest{RequestID: "input-other", Answer: "Solo founders."},
			wantError: "not waiting for this request",
		},
	} {
		t.Run(attempt.name, func(t *testing.T) {
			_, err := resumeRepositoryFlowAssistantForTest(server,
				context.Background(),
				httptest.NewRequest(http.MethodPost, "/", nil),
				id,
				client,
				project,
				&ProjectRepositoryView{Ref: "demo-repo", Name: "demo", Status: projectRepositoryStatusReady},
				inputErr.RunID,
				attempt.request,
			)
			if err == nil || !strings.Contains(err.Error(), attempt.wantError) {
				t.Fatalf("resumeProjectAssistantRun error = %v, want %q", err, attempt.wantError)
			}
			run, err := messages.GetAssistantRun(context.Background(), messageScope, inputErr.RunID)
			if err != nil {
				t.Fatalf("GetAssistantRun returned error: %v", err)
			}
			if run.Status != store.AssistantRunStatusPendingInput {
				t.Fatalf("run status = %q, want pending input", run.Status)
			}
		})
	}
}

func TestResumeProjectAssistantRunClearsStaleFollowUpInterruptWhenRunAlreadyClaimed(t *testing.T) {
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
	now := time.Now().UTC()
	user := store.Message{ID: "msg-user-stale-follow-up", Role: aiv1alpha1.ProjectMessageRoleUser, ActorID: id.user, Content: "build an app", CreatedAt: now, UpdatedAt: now}
	activeAssistant := store.Message{ID: "msg-active-stale-follow-up", Role: aiv1alpha1.ProjectMessageRoleAssistant, CreatedAt: now, UpdatedAt: now}
	createdRun, err := messages.CreateAssistantRun(context.Background(), messageScope, user, activeAssistant, store.AssistantRun{
		ID:              runID,
		Mode:            store.AssistantRunModePlan,
		Status:          store.AssistantRunStatusPendingInput,
		ClientRequestID: "request-stale-follow-up",
		UserMessageID:   user.ID,
		ActiveMessageID: activeAssistant.ID,
		Revision:        1,
		RequestID:       requestID,
		Checkpoint:      rawCheckpoint,
		CreatedAt:       now,
		UpdatedAt:       now,
	})
	if err != nil {
		t.Fatalf("CreateAssistantRun returned error: %v", err)
	}
	accumulator, err := server.projectAssistantSupervisor().Attach(messageScope, createdRun, activeAssistant)
	if err != nil {
		t.Fatalf("Attach returned error: %v", err)
	}
	if _, err := accumulator.ClaimPending(context.Background(), requestID); err != nil {
		t.Fatalf("ClaimPending returned error: %v", err)
	}
	followUp := projectAssistantFollowUp{
		ID:         requestID,
		ToolCallID: "call-follow-up",
		Questions: []projectAssistantFollowUpQuestion{{
			ID:       "audience",
			Header:   "Audience",
			Question: "What audience should this app target?",
			IsOther:  true,
		}},
		Prompt: "I need one detail before continuing.",
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

	_, err = resumeRepositoryFlowAssistantForTest(server,
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
	if status, ok := updatedMsg.Metadata[projectAssistantMetadataWorkingStatus]; !ok || status != projectMessageStatusPendingInput {
		t.Fatalf("assistant metadata = %#v, want stale pending status preserved after rejected resume", updatedMsg.Metadata)
	}
	if interrupt := projectAssistantUIInterruptFromMetadata(updatedMsg.Metadata[projectMessageMetadataAssistantInterrupt]); interrupt != nil {
		t.Fatalf("assistant interrupt = %#v, want stale follow-up cleared after rejected resume", interrupt)
	}
	run, err := messages.GetAssistantRun(context.Background(), messageScope, runID)
	if err != nil {
		t.Fatalf("GetAssistantRun returned error: %v", err)
	}
	if run.Status != store.AssistantRunStatusRunning {
		t.Fatalf("run status = %q, want running", run.Status)
	}
}

func TestGenerateProjectAssistantStreamContinuesRepeatedToolLoopUntilModelAnswer(t *testing.T) {
	closing := "I inspected src/App.tsx."
	reply, requests, err := runRepeatedReadFileAssistantStream(t, closing)
	if err != nil {
		t.Fatalf("generateProjectAssistantStream error = %v after %d repeated requests", err, len(requests))
	}
	if reply != closing {
		t.Fatalf("reply = %q, want model-authored closing answer", reply)
	}
	if got := projectAssistantToolBearingRequestCount(requests); got != 13 {
		t.Fatalf("tool-bearing LLM request count = %d, want 12 repeated reads plus one closing turn", got)
	}
	if len(requests) != 13 {
		t.Fatalf("LLM request count = %d, want 12 repeated reads plus one closing turn", len(requests))
	}
}

func TestGenerateProjectAssistantStreamDoesNotManufactureAnswerAfterRepeatedToolLoop(t *testing.T) {
	reply, requests, err := runRepeatedReadFileAssistantStream(t, "")
	if !errors.Is(err, errProjectAssistantNoOutput) {
		t.Fatalf("generateProjectAssistantStream error = %v after %d requests, want no-output error", err, len(requests))
	}
	if reply != "" {
		t.Fatalf("reply = %q, want no manufactured assistant response", reply)
	}
	if got := projectAssistantToolBearingRequestCount(requests); got != 13 {
		t.Fatalf("tool-bearing LLM request count = %d, want 12 repeated reads plus one empty closing turn", got)
	}
}

func TestGenerateProjectAssistantStreamAllowsProductiveDistinctReads(t *testing.T) {
	closing := "I inspected the requested files."
	reply, requests, err := runUniqueReadFileAssistantStream(t, closing)
	if err != nil {
		t.Fatalf("generateProjectAssistantStream error = %v after %d productive requests", err, len(requests))
	}
	if reply != closing {
		t.Fatalf("reply = %q, want model-authored closing answer", reply)
	}
	if got := projectAssistantToolBearingRequestCount(requests); got != 13 {
		t.Fatalf("tool-bearing LLM request count = %d, want 12 read turns plus one normal closing turn", got)
	}
	if len(requests) != 13 {
		t.Fatalf("LLM request count = %d, want 12 tool-bearing calls plus one normal closing call", len(requests))
	}
}

func TestGenerateProjectAssistantStreamAllowsOneBatchedDuplicateReadCycle(t *testing.T) {
	closing := "I finished inspecting the project."
	reply, requests, err := runBatchedDuplicateReadAssistantStream(t, closing)
	if err != nil {
		t.Fatalf("generateProjectAssistantStream error = %v after %d requests", err, len(requests))
	}
	if reply != closing {
		t.Fatalf("reply = %q, want model-authored closing answer", reply)
	}
	if len(requests) != 3 {
		t.Fatalf("LLM request count = %d, want initial reads, one duplicate batch, and closing response", len(requests))
	}
}

const projectAssistantTestFiniteIterationCeiling = 100

func TestGenerateProjectAssistantStreamClosesMaxIterationsWithoutOutOfBandModelCall(t *testing.T) {
	closing := "I inspected the project."
	reply, requests, err := runMaxIterationReadFileAssistantStream(t, closing)
	if !projectEinoAssistantMaxIterationsExceeded(err) {
		t.Fatalf("generateProjectAssistantStream error = %v after %d requests, want Eino max-iterations limit", err, len(requests))
	}
	if reply != "" {
		t.Fatalf("reply = %q, want no manufactured assistant response", reply)
	}
	if got := projectAssistantToolBearingRequestCount(requests); got != projectAssistantTestFiniteIterationCeiling {
		t.Fatalf("tool-bearing LLM request count = %d, want %d", got, projectAssistantTestFiniteIterationCeiling)
	}
	if len(requests) != projectAssistantTestFiniteIterationCeiling {
		t.Fatalf("LLM request count = %d, want the explicit test iteration ceiling", len(requests))
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

	messages := projectPromptMessagesForCollaborationMode(project, nil, history, projectAssistantCollaborationModeDefault, false)

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
	scope := workspace.Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: "demo", ProjectUID: "test-project-uid"}
	writeTestWorkspaceFiles(t, context.Background(), workspaces, scope, []workspace.File{{Path: "index.html", Content: "hello\n"}})
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

func TestCommitProjectWorkspaceFilesSendsDeletedPaths(t *testing.T) {
	var gotFiles []map[string]string
	var gotDeletePaths []string
	mcp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var envelope struct {
			Method string `json:"method"`
			Params struct {
				Name      string `json:"name"`
				Arguments struct {
					Files       []map[string]string `json:"files"`
					DeletePaths []string            `json:"deletePaths"`
				} `json:"arguments"`
			} `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&envelope); err != nil {
			t.Fatal(err)
		}
		if envelope.Method != "tools/call" || envelope.Params.Name != "code__commit_files" {
			t.Fatalf("unexpected MCP request: %#v", envelope)
		}
		gotFiles = envelope.Params.Arguments.Files
		gotDeletePaths = envelope.Params.Arguments.DeletePaths
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"result":{"structuredContent":{"phase":"Succeeded","files":["src/new.ts","src/old.ts"],"commitSHA":"abcdef"}}}`)
	}))
	defer mcp.Close()

	ctx := context.Background()
	workspaces := workspace.NewFileStore(t.TempDir())
	scope := workspace.Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: "demo", ProjectUID: "test-project-uid"}
	writeTestWorkspaceFiles(t, ctx, workspaces, scope, []workspace.File{
		{Path: "src/old.ts", Content: "old\n"},
		{Path: "src/new.ts", Content: "new\n"},
	})
	readOld, err := workspaces.ReadFile(ctx, scope, workspace.ReadOptions{Path: "src/old.ts"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := workspaces.DeleteFile(ctx, scope, workspace.DeleteOptions{Path: "src/old.ts", ExpectedVersion: readOld.Version}); err != nil {
		t.Fatal(err)
	}
	server := NewWithWorkspace(nil, nil, workspaces, mcp.URL, false)
	if _, err := server.commitProjectWorkspaceFiles(
		ctx,
		identity{tenantPath: "root:org-a:ws-1", clusterID: "cluster-ws-1", orgUUID: "org-a", workspaceUUID: "ws-1"},
		scope,
		nil,
		"demo-repo",
		mcp.URL,
		httptest.NewRequest(http.MethodPost, "/", nil),
		map[string]any{"repositoryRef": "demo-repo", "paths": []any{"src/old.ts", "src/new.ts"}},
	); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotDeletePaths, []string{"src/old.ts"}) {
		t.Fatalf("deletePaths = %v", gotDeletePaths)
	}
	if len(gotFiles) != 1 || gotFiles[0]["path"] != "src/new.ts" || gotFiles[0]["content"] != "new\n" {
		t.Fatalf("files = %#v", gotFiles)
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
	scope := workspace.Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: "demo", ProjectUID: "test-project-uid"}
	writeTestWorkspaceFiles(t, context.Background(), workspaces, scope, []workspace.File{{Path: "index.html", Content: "hello\n"}})
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

func TestProjectAssistantStoredContentPrefersFinalReply(t *testing.T) {
	got := projectAssistantStoredContent("Committed index.html.", "I will inspect the project files.")
	if got != "Committed index.html." {
		t.Fatalf("stored content = %q, want final reply", got)
	}
}

func runRepeatedReadFileAssistantStream(t *testing.T, finalAnswer string) (string, []chatCompletionRequest, error) {
	t.Helper()
	t.Setenv(projectAssistantMaxIterationsEnv, strconv.Itoa(projectAssistantTestFiniteIterationCeiling))
	const repeatedReads = 12
	steps := make([]repositoryFlowEinoModelStep, 0, repeatedReads+1)
	for i := 1; i <= repeatedReads; i++ {
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
	const productiveReads = 12
	steps := make([]repositoryFlowEinoModelStep, 0, productiveReads+1)
	for i := 1; i <= productiveReads; i++ {
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

func runBatchedDuplicateReadAssistantStream(t *testing.T, finalAnswer string) (string, []chatCompletionRequest, error) {
	t.Helper()
	paths := []string{"src/App.tsx", "src/file-1.tsx", "src/file-2.tsx"}
	batch := func(prefix string) *einoschema.Message {
		calls := make([]einoschema.ToolCall, 0, len(paths))
		for index, path := range paths {
			calls = append(calls, einoschema.ToolCall{
				ID:   fmt.Sprintf("%s-%d", prefix, index),
				Type: "function",
				Function: einoschema.FunctionCall{
					Name:      projectToolReadFile,
					Arguments: fmt.Sprintf(`{"file_path":%q,"offset":1,"limit":200}`, path),
				},
			})
		}
		return einoschema.AssistantMessage("", calls)
	}
	model := &repositoryFlowEinoChatModel{Steps: []repositoryFlowEinoModelStep{
		{Message: batch("initial")},
		{Message: batch("duplicate")},
		{Message: einoschema.AssistantMessage(finalAnswer, nil)},
	}}
	return runProjectAssistantStreamWithModel(t, model, "")
}

func runMaxIterationReadFileAssistantStream(t *testing.T, finalAnswer string) (string, []chatCompletionRequest, error) {
	t.Helper()
	t.Setenv(projectAssistantMaxIterationsEnv, strconv.Itoa(projectAssistantTestFiniteIterationCeiling))
	steps := make([]repositoryFlowEinoModelStep, 0, projectAssistantTestFiniteIterationCeiling+2)
	for i := 1; i <= projectAssistantTestFiniteIterationCeiling+1; i++ {
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
	for i := 1; i <= projectAssistantTestFiniteIterationCeiling+1; i++ {
		seedFiles = append(seedFiles, workspace.File{
			Path:    fmt.Sprintf("src/file-%d.tsx", i),
			Content: fmt.Sprintf("export const value%d = %d\n", i, i),
		})
	}
	writeTestWorkspaceFiles(t, context.Background(), workspaces, workspace.Scope{
		OrgUUID:       scope.OrgUUID,
		WorkspaceUUID: scope.WorkspaceUUID,
		ProjectName:   scope.ProjectName,
		ProjectUID:    "test-project-uid-demo",
	}, seedFiles)
	server := NewWithWorkspace(nil, messages, workspaces, hubBase, false)
	setProjectAssistantModelForTest(server, model)
	project := projectWithRepository("demo-repo", "demo", "github")
	project.Name = "demo"
	project.UID = "test-project-uid-demo"
	project.Spec.DisplayName = "Demo"

	reply, err := generateRepositoryFlowBuildAssistantForTest(t, server,
		httptest.NewRequest(http.MethodPost, "/", nil),
		identity{tenantPath: "root:org-a:ws-1", clusterID: "cluster-ws-1", orgUUID: "org-a", workspaceUUID: "ws-1", user: "user@example.com"},
		client,
		project,
		projectAssistantStreamCallbacks{},
		nil,
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
	scope := workspace.Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: "demo", ProjectUID: "test-project-uid"}
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
	writeTestWorkspaceFiles(t, context.Background(), workspaces, scope, files)
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

func TestProjectRepositoryViewFiltersCommitsWhenBackendIgnoresSelector(t *testing.T) {
	project := projectWithRepository("demo-repo", "demo", "github")
	matching := codeRepositoryCommitObject("matching", "demo-repo", "Succeeded", "abc123", "Demo", 1, time.Now().UTC())
	unrelated := codeRepositoryCommitObject("unrelated", "other-repo", "Succeeded", "zzz999", "Other", 1, time.Now().UTC())
	spoofed := codeRepositoryCommitObject("spoofed", "other-repo", "Succeeded", "bad999", "Spoofed", 1, time.Now().UTC())
	spoofed.SetLabels(map[string]string{codeLabelRepository: "demo-repo"})

	view := projectRepositoryViewFromResources(
		context.Background(),
		project,
		codeObjectGetter(codeRepositoryObject("demo-repo", "demo", "github", true), codeConnectionObject("github")),
		func(context.Context, k8sschema.GroupVersionResource, metav1.ListOptions) (*unstructured.UnstructuredList, error) {
			return &unstructured.UnstructuredList{Items: []unstructured.Unstructured{*matching, *unrelated, *spoofed}}, nil
		},
	)
	if view == nil {
		t.Fatal("projectRepositoryView returned nil")
	}
	if len(view.Commits) != 1 || view.Commits[0].Name != "matching" {
		t.Fatalf("commits = %#v, want only matching repository commit", view.Commits)
	}
}

func TestProjectRepositoryViewPreservesCommitListFailure(t *testing.T) {
	project := projectWithRepository("demo-repo", "demo", "github")
	wantErr := errors.New("commit discovery unavailable")
	view := projectRepositoryViewFromResources(
		context.Background(),
		project,
		codeObjectGetter(codeRepositoryObject("demo-repo", "demo", "github", true), codeConnectionObject("github")),
		func(context.Context, k8sschema.GroupVersionResource, metav1.ListOptions) (*unstructured.UnstructuredList, error) {
			return nil, wantErr
		},
	)
	if view == nil {
		t.Fatal("projectRepositoryView returned nil")
	}
	if !errors.Is(view.commitsErr, wantErr) {
		t.Fatalf("commitsErr = %v, want %v", view.commitsErr, wantErr)
	}
	cp := (&Server{}).checkpointCI(view, projectCheckpointStateDone)
	if cp.State != projectCheckpointStateError || cp.Reason != "Could not read repository commit history." {
		t.Fatalf("checkpoint = %#v, want commit-history error", cp)
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
