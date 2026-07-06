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
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	einoschema "github.com/cloudwego/eino/schema"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	aiv1alpha1 "github.com/faroshq/provider-app-studio/apis/ai/v1alpha1"
	asclient "github.com/faroshq/provider-app-studio/client"
	"github.com/faroshq/provider-app-studio/store"
	"github.com/faroshq/provider-app-studio/workspace"
)

const (
	projectLLMSecretName           = "kedge-projects-llm"
	projectLLMSecretNamespace      = "default"
	defaultProjectLLMProvider      = "openai-compatible"
	defaultProjectLLMBaseURL       = "https://api.openai.com/v1"
	defaultProjectLLMGoogleBaseURL = "https://generativelanguage.googleapis.com"
	defaultProjectLLMModel         = "gpt-5.4"
	projectLLMProviderGoogle       = "google-ai-studio"
	projectLLMGoogleCloudScope     = "https://www.googleapis.com/auth/cloud-platform"

	// maxAssistantToolTurns bounds how many tool-call/round-trips a single
	// assistant generation may take before the run returns a progress summary.
	// It is intentionally high enough for app scaffolding that writes many
	// files, while still guarding against models that loop on tool calls.
	maxAssistantToolTurns            = 32
	projectToolInfoLimit             = 1000
	projectMCPCallTimeout            = 2 * time.Minute
	projectCommitProjectFilesMax     = 500
	projectCommitProjectFilesMaxSize = 16 * 1024 * 1024
)

const (
	projectToolListProjectFiles               = "list_project_files"
	projectToolReadProjectFile                = "read_project_file"
	projectToolSearchProjectFiles             = "search_project_files"
	projectToolPlanProjectChanges             = "plan_project_changes"
	projectToolCheckProjectReadiness          = "check_project_readiness"
	projectToolPrepareProjectDeployment       = "prepare_project_deployment"
	projectToolDeployProjectRuntime           = "deploy_project_runtime"
	projectToolGetRuntimeStatus               = "get_runtime_status"
	projectToolGetPreviewURL                  = "get_preview_url"
	projectToolGetRuntimeLogs                 = "get_runtime_logs"
	projectToolRestartRuntime                 = "restart_runtime"
	projectToolSetRuntimeEnv                  = "set_runtime_env"
	projectToolAskFollowUp                    = "ask_follow_up"
	projectToolRequestProjectPlanApproval     = "request_project_plan_approval"
	projectToolWriteFile                      = "write_file"
	projectToolApplyPatch                     = "apply_patch"
	projectToolMkdir                          = "mkdir"
	projectToolSelectTemplate                 = "select_project_template"
	projectToolHydrateWorkspace               = "hydrate_workspace"
	projectToolCommitProjectFiles             = "commit_project_files"
	projectToolCommitFiles                    = "commit_files"
	projectToolCodeCommitFiles                = "code__commit_files"
	projectToolInfrastructureListTemplates    = "infrastructure__list_templates"
	projectToolInfrastructureDescribeTemplate = "infrastructure__describe_template"
	projectToolInfrastructureProvision        = "infrastructure__provision"
	projectToolInfrastructureListInstances    = "infrastructure__list_instances"
	projectToolInfrastructureGetInstance      = "infrastructure__get_instance"
	projectToolDatabricksListTables           = "databricks__list_tables"
	projectToolDatabricksDescribeTable        = "databricks__describe_table"
)

var (
	errProjectLLMNotConfigured = errors.New("project LLM API key is not configured")
	secretGVR                  = schema.GroupVersionResource{Version: "v1", Resource: "secrets"}
)

type ProjectLLMSettingsView struct {
	Provider   string `json:"provider"`
	BaseURL    string `json:"baseURL"`
	Model      string `json:"model"`
	Configured bool   `json:"configured"`
}

type PatchProjectLLMSettingsRequest struct {
	Provider *string `json:"provider,omitempty"`
	BaseURL  *string `json:"baseURL,omitempty"`
	Model    *string `json:"model,omitempty"`
	APIKey   *string `json:"apiKey,omitempty"`
}

type projectLLMSettings struct {
	Provider string
	BaseURL  string
	Model    string
	APIKey   string
}

type googleServiceAccountCredential struct {
	Type        string `json:"type"`
	ProjectID   string `json:"project_id"`
	ClientEmail string `json:"client_email"`
	PrivateKey  string `json:"private_key"`
	TokenURI    string `json:"token_uri"`
}

type chatMessage struct {
	Role       string         `json:"role"`
	Content    string         `json:"content,omitempty"`
	Name       string         `json:"name,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
	ToolCalls  []chatToolCall `json:"tool_calls,omitempty"`
}

type chatTool struct {
	Type     string           `json:"type"`
	Function chatToolFunction `json:"function"`
}

type chatToolFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

type chatToolCall struct {
	ID           string               `json:"id"`
	Type         string               `json:"type"`
	Function     chatToolCallFunction `json:"function"`
	ExtraContent map[string]any       `json:"extra_content,omitempty"`
}

type chatToolCallFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type projectAssistantReply struct {
	Content   string
	ToolCalls []chatToolCall
}

type projectAssistantStreamCallbacks struct {
	OnChunk          func(string)
	OnStatus         func(string)
	OnToolCall       func(projectToolCallStreamEvent)
	OnAssistantEvent func(projectAssistantEvent)
}

type projectNamingResult struct {
	DisplayName    string
	RepositoryName string
}

type projectMCPTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

func (s *Server) getProjectLLMSettings(w http.ResponseWriter, r *http.Request) {
	c, _, ok := s.requireProjectClient(w, r)
	if !ok {
		return
	}
	settings, err := readProjectLLMSettings(r.Context(), c)
	if err != nil {
		writeProjectError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, settings.view())
}

func (s *Server) patchProjectLLMSettings(w http.ResponseWriter, r *http.Request) {
	// The hub used to gate this on the kedge "admin" membership role. The
	// provider acts as the caller, so the workspace Secret's own RBAC is the
	// authority: a non-admin caller's Update is rejected by the apiserver.
	c, _, ok := s.requireProjectClient(w, r)
	if !ok {
		return
	}
	var req PatchProjectLLMSettingsRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	settings, err := readProjectLLMSettings(r.Context(), c)
	if err != nil {
		writeProjectError(w, err)
		return
	}
	if req.Provider != nil {
		settings.Provider = strings.TrimSpace(*req.Provider)
		if settings.Provider == "" {
			settings.Provider = defaultProjectLLMProvider
		}
	}
	if req.BaseURL != nil {
		baseURL, err := normalizeLLMBaseURL(*req.BaseURL)
		if err != nil {
			writeProjectError(w, err)
			return
		}
		settings.BaseURL = baseURL
	}
	if req.Model != nil {
		settings.Model = strings.TrimSpace(*req.Model)
		if settings.Model == "" {
			writeProjectError(w, newValidationError("model cannot be empty"))
			return
		}
	}
	if req.APIKey != nil {
		settings.APIKey = strings.TrimSpace(*req.APIKey)
	}
	if err := normalizeProjectLLMSettings(&settings); err != nil {
		writeProjectError(w, err)
		return
	}
	if err := writeProjectLLMSettings(r.Context(), c, settings); err != nil {
		writeProjectError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, settings.view())
}

func (s *Server) generateProjectAssistantStream(
	r *http.Request,
	id identity,
	c *asclient.Client,
	p *aiv1alpha1.Project,
	callbacks projectAssistantStreamCallbacks,
) (string, error) {
	ctx := r.Context()
	if s.store == nil {
		return "", fmt.Errorf("project message store not configured")
	}
	settings, err := readProjectLLMSettings(ctx, c)
	if err != nil {
		return "", err
	}
	if err := normalizeProjectLLMSettings(&settings); err != nil {
		return "", err
	}
	if strings.TrimSpace(settings.APIKey) == "" {
		return "", errProjectLLMNotConfigured
	}
	if id.orgUUID == "" || id.workspaceUUID == "" {
		return "", errors.New("tenant context missing")
	}
	turn := newProjectAssistantTurnItem(projectAssistantTurnMessage, id, p.Name)
	ctx, finishTurn := s.projectAssistantRunManager().Begin(ctx, turn)
	defer finishTurn()
	r = r.WithContext(ctx)
	recent, err := s.store.LoadRecentMessages(ctx, projectMessageScope(id.orgUUID, id.workspaceUUID, p.Name), 24)
	if err != nil {
		return "", err
	}
	p = projectWithLiveBindingStatus(ctx, c, p, id)
	turnDecision, err := s.projectAssistantTurnRouter()(ctx, projectAssistantTurnRouteRequest{
		LLM:     settings,
		History: recent,
	})
	if err != nil {
		return "", err
	}
	turnPolicy := projectAssistantTurnPolicyForDecision(turnDecision)
	req := projectAssistantRunRequest{
		Identity:                 id,
		HTTPRequest:              r,
		Client:                   c,
		Project:                  p,
		Repository:               projectRepositoryView(ctx, c, p),
		WorkspaceScope:           projectWorkspaceScope(id, p.Name),
		Workspace:                s.workspaces,
		MessageScope:             projectMessageScope(id.orgUUID, id.workspaceUUID, p.Name),
		LLM:                      settings,
		History:                  recent,
		MCPBaseURL:               s.hubBase,
		MCPInsecureSkipTLSVerify: s.mcpInsecureSkipTLSVerify,
		AutoApproveActions:       s.autoApproveAssistantActions(),
		StreamCallbacks:          callbacks,
		TurnProfile:              turnPolicy.profile,
		TurnPolicy:               turnPolicy,
	}
	result, err := s.projectAssistantEngine().StreamProjectAssistant(ctx, req)
	if err != nil {
		return "", err
	}
	return result.Content, nil
}

func projectRepeatedToolLoopFallback(toolMessages []chatMessage) string {
	return projectToolLoopFallback(toolMessages, "repeated the same action")
}

func projectCommitToolReply(toolMessages []chatMessage) (string, bool) {
	for i := len(toolMessages) - 1; i >= 0; i-- {
		msg := toolMessages[i]
		if projectToolBaseName(msg.Name) != projectToolCommitProjectFiles {
			continue
		}
		status := projectToolMessageStatus(msg)
		summary := summarizeProjectToolResult(msg.Name, msg.Content)
		summary = strings.TrimSpace(strings.TrimPrefix(summary, "Tool call failed:"))

		var b strings.Builder
		switch status {
		case "failed":
			b.WriteString("I could not commit the workspace files to the managed git source.")
		case "running":
			b.WriteString("The repository commit request was created, but it is still running.")
		default:
			b.WriteString("Committed the workspace files to the managed git source.")
		}
		if summary != "" {
			b.WriteString(" Last action result: ")
			b.WriteString(summary)
			b.WriteString(".")
		}
		return b.String(), true
	}
	return "", false
}

func projectToolMessageStatus(msg chatMessage) string {
	if strings.HasPrefix(strings.TrimSpace(msg.Content), "Tool call failed:") {
		return "failed"
	}
	return projectToolCallResultStatus(msg.Name, msg.Content)
}

func projectToolLoopFallback(toolMessages []chatMessage, reason string) string {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "could not finish using tools"
	}
	summaries := make([]string, 0, len(toolMessages))
	for _, msg := range toolMessages {
		name := strings.TrimSpace(msg.Name)
		if name == "" {
			continue
		}
		if summary := summarizeProjectToolResult(name, msg.Content); summary != "" {
			summaries = append(summaries, name+": "+summary)
			continue
		}
		summaries = append(summaries, name)
	}

	var b strings.Builder
	if len(summaries) > 0 {
		if len(summaries) == 1 && strings.HasPrefix(summaries[0], projectToolReadProjectFile+": ") {
			b.WriteString("I inspected ")
			b.WriteString(strings.TrimPrefix(summaries[0], projectToolReadProjectFile+": "))
		} else if len(summaries) == 1 {
			b.WriteString("I used the latest project tool result: ")
			b.WriteString(summaries[0])
		} else {
			b.WriteString("I used the latest project tool results")
		}
		b.WriteString(". ")
	} else {
		b.WriteString("I used the available project tools. ")
	}
	if reason == "kept requesting actions" {
		b.WriteString("The turn ended before I could produce a complete final answer, but I can continue from the current project state.")
	} else {
		b.WriteString("The turn ended before I could produce a complete final answer, but I can continue from that context.")
	}
	if len(summaries) > 1 {
		b.WriteString(" Recent results: ")
		b.WriteString(strings.Join(summaries, "; "))
		b.WriteString(".")
	}
	return b.String()
}

func (s *Server) generateProjectNaming(ctx context.Context, c *asclient.Client, prompt string) (projectNamingResult, error) {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return projectNamingResult{}, newValidationError("prompt is required")
	}
	settings, err := readProjectLLMSettings(ctx, c)
	if err != nil {
		return projectNamingResult{}, err
	}
	if err := normalizeProjectLLMSettings(&settings); err != nil {
		return projectNamingResult{}, err
	}
	if strings.TrimSpace(settings.APIKey) == "" {
		return projectNamingResult{}, errProjectLLMNotConfigured
	}

	model, err := newProjectEinoChatModel(ctx, settings)
	if err != nil {
		return projectNamingResult{}, err
	}
	reply, err := model.Generate(ctx, []*einoschema.Message{
		einoschema.SystemMessage("Generate concise app project names. Return only JSON with string fields displayName and repositoryName. " +
			"displayName should be 2-5 words, human-readable, and no longer than 64 characters. " +
			"repositoryName must be derived from displayName and must already satisfy DNS-1123 label rules: lowercase a-z, 0-9, hyphen only; starts and ends with alphanumeric; max 63 characters."),
		einoschema.UserMessage("Prompt:\n" + prompt),
	}, projectTemperatureOptions(settings.Model, 0.1)...)
	if err != nil {
		return projectNamingResult{}, err
	}
	if reply == nil {
		return projectNamingResult{}, errors.New("LLM naming response was empty")
	}
	out, err := parseProjectNamingResult(reply.Content)
	if err != nil {
		return projectNamingResult{}, err
	}
	out.DisplayName = strings.TrimSpace(out.DisplayName)
	if out.DisplayName == "" {
		return projectNamingResult{}, errors.New("LLM naming response omitted displayName")
	}
	if len(out.DisplayName) > 64 {
		out.DisplayName = strings.TrimSpace(out.DisplayName[:64])
	}
	out.RepositoryName = dns1123Label(out.RepositoryName)
	if out.RepositoryName == "" {
		return projectNamingResult{}, errors.New("LLM naming response did not produce a valid repositoryName")
	}
	return out, nil
}

func parseProjectNamingResult(content string) (projectNamingResult, error) {
	content = strings.TrimSpace(content)
	if strings.HasPrefix(content, "```") {
		content = strings.TrimPrefix(content, "```json")
		content = strings.TrimPrefix(content, "```")
		content = strings.TrimSuffix(content, "```")
		content = strings.TrimSpace(content)
	}
	if start := strings.Index(content, "{"); start >= 0 {
		if end := strings.LastIndex(content, "}"); end > start {
			content = content[start : end+1]
		}
	}
	var decoded struct {
		DisplayName    string `json:"displayName"`
		RepositoryName string `json:"repositoryName"`
	}
	if err := json.Unmarshal([]byte(content), &decoded); err != nil {
		return projectNamingResult{}, fmt.Errorf("decode LLM naming response: %w", err)
	}
	return projectNamingResult{
		DisplayName:    decoded.DisplayName,
		RepositoryName: decoded.RepositoryName,
	}, nil
}

func projectWorkspaceScope(id identity, projectName string) workspace.Scope {
	return workspace.Scope{
		OrgUUID:       id.orgUUID,
		WorkspaceUUID: id.workspaceUUID,
		ProjectName:   projectName,
	}
}

func projectLinkedRepositoryRef(p *aiv1alpha1.Project) string {
	if p == nil || p.Spec.Repository == nil {
		return ""
	}
	return strings.TrimSpace(p.Spec.Repository.RepositoryRef)
}

func (s *Server) commitProjectWorkspaceFiles(ctx context.Context, id identity, scope workspace.Scope, projectRepositoryRef, mcpEndpoint string, r *http.Request, args map[string]any) (string, error) {
	projectRepositoryRef = strings.TrimSpace(projectRepositoryRef)
	if projectRepositoryRef == "" {
		return "", errors.New("project repository is not configured")
	}
	repositoryRef := projectToolString(args["repositoryRef"])
	if repositoryRef == "" {
		return "", errors.New("repositoryRef is required")
	}
	if repositoryRef != projectRepositoryRef {
		return "", fmt.Errorf("repositoryRef %q does not match this Project's repository %q", repositoryRef, projectRepositoryRef)
	}
	paths := projectToolStringList(args["paths"])
	if len(paths) == 0 {
		return "", errors.New("at least one path is required")
	}
	if len(paths) > projectCommitProjectFilesMax {
		return "", fmt.Errorf("too many paths: %d > %d", len(paths), projectCommitProjectFilesMax)
	}
	cleanPaths := make([]string, 0, len(paths))
	seen := map[string]struct{}{}
	for _, p := range paths {
		clean, err := workspace.CleanProjectPath(p)
		if err != nil {
			return "", err
		}
		if _, ok := seen[clean]; ok {
			continue
		}
		seen[clean] = struct{}{}
		cleanPaths = append(cleanPaths, clean)
	}
	files := make([]map[string]string, 0, len(cleanPaths))
	var totalBytes int64
	for _, p := range cleanPaths {
		read, err := s.workspaces.ReadFile(ctx, scope, workspace.ReadOptions{Path: p, MaxBytes: workspace.MaxWriteBytes})
		if err != nil {
			return "", err
		}
		if read.Binary {
			return "", fmt.Errorf("file %q is binary and cannot be committed through code__commit_files", read.Path)
		}
		if read.Truncated {
			return "", fmt.Errorf("file %q is too large to commit through commit_project_files", read.Path)
		}
		totalBytes += int64(len([]byte(read.Content)))
		if totalBytes > projectCommitProjectFilesMaxSize {
			return "", fmt.Errorf("commit_project_files payload is too large: %d > %d bytes", totalBytes, projectCommitProjectFilesMaxSize)
		}
		files = append(files, map[string]string{"path": read.Path, "content": read.Content})
	}
	if len(files) == 0 {
		return "", errors.New("no files to commit")
	}
	commitArgs := map[string]any{
		"repositoryRef": projectRepositoryRef,
		"files":         files,
	}
	if message := projectToolString(args["message"]); message != "" {
		commitArgs["message"] = message
	}
	if branch := projectToolString(args["branch"]); branch != "" {
		commitArgs["branch"] = branch
	}
	resp, err := callProjectMCPTool(ctx, mcpEndpoint, r, id.tenantPath, s.mcpInsecureSkipTLSVerify, projectToolCodeCommitFiles, commitArgs)
	if err != nil {
		return "", err
	}
	return s.reconcileProjectBuildConfigAfterCommit(ctx, id, scope, projectRepositoryRef, mcpEndpoint, r, args, resp)
}

func ensureProjectToolCallIDs(toolCalls []chatToolCall) {
	for i := range toolCalls {
		if toolCalls[i].ID == "" {
			toolCalls[i].ID = fmt.Sprintf("tool-%d", i+1)
		}
	}
}

func summarizeProjectToolArguments(name, raw string) string {
	if strings.TrimSpace(raw) == "" {
		return ""
	}
	args := map[string]any{}
	if err := json.Unmarshal([]byte(raw), &args); err != nil {
		return "unparseable arguments"
	}
	return summarizeProjectToolArgumentsMap(name, args)
}

func summarizeProjectToolArgumentsMap(name string, args map[string]any) string {
	if len(args) == 0 {
		return ""
	}
	switch projectToolBaseName(name) {
	case projectToolCommitFiles, projectToolCommitProjectFiles:
		parts := []string{}
		if repo := projectToolString(args["repositoryRef"]); repo != "" {
			parts = append(parts, "repository "+repo)
		}
		if branch := projectToolString(args["branch"]); branch != "" {
			parts = append(parts, "branch "+branch)
		}
		if message := projectToolString(args["message"]); message != "" {
			parts = append(parts, "message "+message)
		}
		paths := projectToolFilePaths(args["files"])
		if len(paths) == 0 {
			paths = projectToolStringList(args["paths"])
		}
		if len(paths) > 0 {
			parts = append(parts, fmt.Sprintf("%d file(s): %s", len(paths), summarizeProjectToolList(paths, 5)))
		}
		return truncateProjectToolInfo(strings.Join(parts, "; "))
	case projectToolListProjectFiles:
		return summarizeProjectToolKeyValues(args, []string{"limit"})
	case projectToolReadProjectFile:
		return summarizeProjectToolKeyValues(args, []string{"path", "maxBytes"})
	case projectToolSearchProjectFiles:
		return summarizeProjectToolKeyValues(args, []string{"query", "maxResults"})
	case projectToolPlanProjectChanges, projectToolCheckProjectReadiness, projectToolPrepareProjectDeployment:
		return summarizeProjectPlanningWorkflowArgs(args)
	case projectToolDeployProjectRuntime:
		return summarizeProjectToolKeyValues(args, []string{"targetRef", "appName", "image", "port", "intent"})
	case projectToolGetRuntimeStatus, projectToolGetPreviewURL, projectToolRestartRuntime:
		return ""
	case projectToolGetRuntimeLogs:
		return summarizeProjectToolKeyValues(args, []string{"tailLines"})
	case projectToolSetRuntimeEnv:
		if env, ok := args["env"].(map[string]any); ok && len(env) > 0 {
			names := make([]string, 0, len(env))
			for name := range env {
				names = append(names, name)
			}
			sort.Strings(names)
			return truncateProjectToolInfo(fmt.Sprintf("%d variable(s): %s", len(names), summarizeProjectToolList(names, 5)))
		}
		return ""
	case projectToolAskFollowUp:
		if questions := projectToolStringList(args["questions"]); len(questions) > 0 {
			return truncateProjectToolInfo(fmt.Sprintf("%d question(s): %s", len(questions), summarizeProjectToolList(questions, 3)))
		}
		return ""
	case projectToolRequestProjectPlanApproval:
		parts := []string{}
		if summary := projectToolString(args["summary"]); summary != "" {
			parts = append(parts, summary)
		}
		if paths := projectToolStringList(args["targetPaths"]); len(paths) > 0 {
			parts = append(parts, fmt.Sprintf("%d target path(s): %s", len(paths), summarizeProjectToolList(paths, 5)))
		}
		return truncateProjectToolInfo(strings.Join(parts, "; "))
	case projectToolWriteFile:
		return summarizeProjectMutationArgs(args, []string{"path"}, true)
	case projectToolApplyPatch:
		return summarizeProjectMutationArgs(args, []string{"path"}, false)
	case projectToolMkdir:
		return summarizeProjectToolKeyValues(args, []string{"path"})
	}
	raw, err := json.Marshal(args)
	if err != nil {
		return ""
	}
	return truncateProjectToolInfo(string(raw))
}

func summarizeProjectToolResult(name, result string) string {
	result = strings.TrimSpace(result)
	if result == "" {
		return ""
	}
	decoded := map[string]any{}
	if err := json.Unmarshal([]byte(result), &decoded); err == nil {
		switch projectToolBaseName(name) {
		case projectToolCommitFiles, projectToolCommitProjectFiles:
			parts := []string{}
			if sha := projectToolString(decoded["commitSHA"]); sha != "" {
				if len(sha) > 12 {
					sha = sha[:12]
				}
				parts = append(parts, "commit "+sha)
			} else if reqName := projectToolString(decoded["name"]); reqName != "" {
				parts = append(parts, "request "+reqName)
			}
			if phase := projectToolString(decoded["phase"]); phase != "" {
				parts = append(parts, "phase "+phase)
			}
			if branch := projectToolString(decoded["branch"]); branch != "" {
				parts = append(parts, "branch "+branch)
			}
			if files := projectToolStringList(decoded["files"]); len(files) > 0 {
				parts = append(parts, fmt.Sprintf("%d file(s): %s", len(files), summarizeProjectToolList(files, 5)))
			}
			if len(parts) > 0 {
				return truncateProjectToolInfo(strings.Join(parts, "; "))
			}
		case projectToolListProjectFiles:
			return summarizeWorkspaceListResult(decoded)
		case projectToolReadProjectFile:
			return summarizeWorkspaceReadResult(decoded)
		case projectToolSearchProjectFiles:
			return summarizeWorkspaceSearchResult(decoded)
		case projectToolPlanProjectChanges:
			return summarizeProjectPlanningWorkflowResult(decoded)
		case projectToolRequestProjectPlanApproval:
			parts := []string{}
			if status := projectToolString(decoded["status"]); status != "" {
				parts = append(parts, "status "+status)
			}
			if summary := projectToolString(decoded["summary"]); summary != "" {
				parts = append(parts, summary)
			}
			if paths := projectToolStringList(decoded["targetPaths"]); len(paths) > 0 {
				parts = append(parts, fmt.Sprintf("%d target path(s): %s", len(paths), summarizeProjectToolList(paths, 5)))
			}
			if len(parts) > 0 {
				return truncateProjectToolInfo(strings.Join(parts, "; "))
			}
		case projectToolCheckProjectReadiness, projectToolPrepareProjectDeployment:
			return summarizeProjectReadinessWorkflowResult(decoded)
		case projectToolDeployProjectRuntime, projectToolGetRuntimeStatus, projectToolGetPreviewURL, projectToolRestartRuntime, projectToolSetRuntimeEnv:
			return summarizeProjectRuntimeWorkflowResult(decoded)
		case projectToolGetRuntimeLogs:
			if lines := projectToolStringList(decoded["lines"]); len(lines) > 0 {
				return truncateProjectToolInfo(fmt.Sprintf("%d log line(s)", len(lines)))
			}
			if summary := projectToolString(decoded["summary"]); summary != "" {
				return truncateProjectToolInfo(summary)
			}
		case projectToolAskFollowUp:
			if answer := projectToolString(decoded["answer"]); answer != "" {
				return truncateProjectToolInfo("answered: " + answer)
			}
		case projectToolWriteFile, projectToolApplyPatch, projectToolMkdir:
			return summarizeWorkspaceMutationResult(decoded)
		}
		if message := projectToolString(decoded["message"]); message != "" {
			return truncateProjectToolInfo(message)
		}
	}
	firstLine := strings.TrimSpace(strings.Split(result, "\n")[0])
	return truncateProjectToolInfo(firstLine)
}

func summarizeProjectMutationArgs(args map[string]any, keys []string, includeContentBytes bool) string {
	parts := []string{}
	if summary := summarizeProjectToolKeyValues(args, keys); summary != "" {
		parts = append(parts, summary)
	}
	if includeContentBytes {
		if content, ok := args["content"].(string); ok {
			parts = append(parts, fmt.Sprintf("%d bytes", len([]byte(content))))
		}
	}
	if projectToolBool(args["replaceAll"]) {
		parts = append(parts, "replaceAll")
	}
	return truncateProjectToolInfo(strings.Join(parts, "; "))
}

func summarizeProjectToolKeyValues(args map[string]any, keys []string) string {
	parts := []string{}
	for _, key := range keys {
		switch key {
		case "maxBytes", "maxResults", "limit":
			if n, ok := projectToolNumber(args[key]); ok {
				parts = append(parts, fmt.Sprintf("%s %d", key, n))
			}
		default:
			if value := projectToolString(args[key]); value != "" {
				parts = append(parts, key+" "+value)
			}
		}
	}
	return truncateProjectToolInfo(strings.Join(parts, "; "))
}

func summarizeProjectPlanningWorkflowArgs(args map[string]any) string {
	parts := []string{}
	if includeFiles, ok := args["includeFiles"].(bool); ok {
		parts = append(parts, fmt.Sprintf("includeFiles %t", includeFiles))
	}
	if n, ok := projectToolNumber(args["maxFiles"]); ok {
		parts = append(parts, fmt.Sprintf("maxFiles %d", n))
	}
	return truncateProjectToolInfo(strings.Join(parts, "; "))
}

func summarizeWorkspaceMutationResult(decoded map[string]any) string {
	parts := []string{}
	if op := projectToolString(decoded["operation"]); op != "" {
		parts = append(parts, op)
	}
	if path := projectToolString(decoded["path"]); path != "" {
		parts = append(parts, path)
	}
	if size, ok := projectToolNumber(decoded["size"]); ok {
		parts = append(parts, fmt.Sprintf("%d bytes", size))
	}
	if replacements, ok := projectToolNumber(decoded["replacements"]); ok {
		parts = append(parts, fmt.Sprintf("%d replacement(s)", replacements))
	}
	return truncateProjectToolInfo(strings.Join(parts, "; "))
}

func summarizeProjectPlanningWorkflowResult(decoded map[string]any) string {
	parts := []string{}
	if summary := projectToolString(decoded["summary"]); summary != "" {
		parts = append(parts, summary)
	}
	if steps, ok := decoded["steps"].([]any); ok && len(steps) > 0 {
		parts = append(parts, fmt.Sprintf("%d step(s)", len(steps)))
	}
	if files := projectToolStringList(decoded["files"]); len(files) > 0 {
		parts = append(parts, fmt.Sprintf("%d file(s): %s", len(files), summarizeProjectToolList(files, 5)))
	}
	if len(parts) == 0 {
		return ""
	}
	return truncateProjectToolInfo(strings.Join(parts, "; "))
}

func summarizeProjectReadinessWorkflowResult(decoded map[string]any) string {
	parts := []string{}
	if status := projectToolString(decoded["status"]); status != "" {
		parts = append(parts, "status "+status)
	}
	if checks := projectToolStringList(decoded["recommendedChecks"]); len(checks) > 0 {
		parts = append(parts, "checks "+summarizeProjectToolList(checks, 4))
	}
	if files := projectToolStringList(decoded["files"]); len(files) > 0 {
		parts = append(parts, fmt.Sprintf("%d file(s): %s", len(files), summarizeProjectToolList(files, 5)))
	}
	if len(parts) == 0 {
		return ""
	}
	return truncateProjectToolInfo(strings.Join(parts, "; "))
}

func summarizeProjectRuntimeWorkflowResult(decoded map[string]any) string {
	parts := []string{}
	if status := projectToolString(decoded["status"]); status != "" {
		parts = append(parts, "status "+status)
	}
	if previewURL := projectToolString(decoded["previewURL"]); previewURL != "" {
		parts = append(parts, "preview "+previewURL)
	}
	if blockers := projectToolStringList(decoded["blockers"]); len(blockers) > 0 {
		parts = append(parts, "blockers "+summarizeProjectToolList(blockers, 3))
	}
	if len(parts) == 0 {
		return ""
	}
	return truncateProjectToolInfo(strings.Join(parts, "; "))
}

func summarizeWorkspaceListResult(decoded map[string]any) string {
	files := projectToolObjectPaths(decoded["files"])
	parts := []string{}
	parts = append(parts, fmt.Sprintf("%d path(s)", len(files)))
	if len(files) > 0 {
		parts = append(parts, summarizeProjectToolList(files, 5))
	}
	if projectToolBool(decoded["truncated"]) {
		parts = append(parts, "truncated")
	}
	return truncateProjectToolInfo(strings.Join(parts, "; "))
}

func summarizeWorkspaceReadResult(decoded map[string]any) string {
	parts := []string{}
	if path := projectToolString(decoded["path"]); path != "" {
		parts = append(parts, "file "+path)
	}
	if size, ok := projectToolNumber(decoded["size"]); ok {
		parts = append(parts, fmt.Sprintf("%d bytes", size))
	}
	if projectToolBool(decoded["binary"]) {
		parts = append(parts, "binary")
	}
	if projectToolBool(decoded["truncated"]) {
		parts = append(parts, "truncated")
	}
	return truncateProjectToolInfo(strings.Join(parts, "; "))
}

func summarizeWorkspaceSearchResult(decoded map[string]any) string {
	parts := []string{}
	if total, ok := projectToolNumber(decoded["totalCount"]); ok {
		parts = append(parts, fmt.Sprintf("%d match(es)", total))
	}
	paths := projectToolObjectPaths(decoded["results"])
	if len(paths) > 0 {
		parts = append(parts, summarizeProjectToolList(paths, 5))
	}
	if projectToolBool(decoded["incomplete"]) {
		parts = append(parts, "incomplete")
	}
	if projectToolBool(decoded["truncated"]) {
		parts = append(parts, "truncated")
	}
	return truncateProjectToolInfo(strings.Join(parts, "; "))
}

func projectToolCallResultStatus(name, result string) string {
	baseName := projectToolBaseName(name)
	if baseName != projectToolCommitFiles && baseName != projectToolCommitProjectFiles {
		return "succeeded"
	}
	decoded := map[string]any{}
	if err := json.Unmarshal([]byte(strings.TrimSpace(result)), &decoded); err != nil {
		return "succeeded"
	}
	switch strings.ToLower(projectToolString(decoded["phase"])) {
	case "pending", "running":
		return "running"
	case "failed":
		return "failed"
	default:
		return "succeeded"
	}
}

func projectToolBaseName(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	if idx := strings.LastIndex(name, "__"); idx >= 0 {
		return name[idx+2:]
	}
	return name
}

func projectToolString(value any) string {
	switch v := value.(type) {
	case string:
		return strings.TrimSpace(v)
	default:
		return ""
	}
}

func projectToolRawString(value any) (string, bool) {
	v, ok := value.(string)
	return v, ok
}

func projectToolFilePaths(value any) []string {
	items, ok := value.([]any)
	if !ok {
		return nil
	}
	paths := make([]string, 0, len(items))
	for _, item := range items {
		obj, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if path := projectToolString(obj["path"]); path != "" {
			paths = append(paths, path)
		}
	}
	return paths
}

func projectToolStringList(value any) []string {
	items, ok := value.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		if value := projectToolString(item); value != "" {
			out = append(out, value)
		}
	}
	return out
}

func projectToolObjectPaths(value any) []string {
	items, ok := value.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		obj, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if path := projectToolString(obj["path"]); path != "" {
			out = append(out, path)
		}
	}
	return out
}

func projectToolNumber(value any) (int64, bool) {
	switch v := value.(type) {
	case float64:
		return int64(v), true
	case int:
		return int64(v), true
	case int64:
		return v, true
	case json.Number:
		n, err := v.Int64()
		return n, err == nil
	default:
		return 0, false
	}
}

func projectToolInt(value any) int {
	n, ok := projectToolNumber(value)
	if !ok || n <= 0 {
		return 0
	}
	if n > int64(^uint(0)>>1) {
		return int(^uint(0) >> 1)
	}
	return int(n)
}

func projectToolBool(value any) bool {
	v, ok := value.(bool)
	return ok && v
}

func summarizeProjectToolList(values []string, limit int) string {
	if len(values) == 0 {
		return ""
	}
	if limit <= 0 || len(values) <= limit {
		return strings.Join(values, ", ")
	}
	return strings.Join(values[:limit], ", ") + fmt.Sprintf(", +%d more", len(values)-limit)
}

func truncateProjectToolInfo(value string) string {
	value = strings.TrimSpace(value)
	if len(value) <= projectToolInfoLimit {
		return value
	}
	if projectToolInfoLimit <= 3 {
		return value[:projectToolInfoLimit]
	}
	return strings.TrimSpace(value[:projectToolInfoLimit-3]) + "..."
}

func (s *Server) loadProjectMCPTools(r *http.Request, id identity, settings projectLLMSettings) ([]chatTool, error) {
	if id.tenantPath == "" {
		return nil, errors.New("tenant context missing")
	}
	registry := s.projectAssistantToolRegistry()
	out := registry.ChatTools(false)
	mcpTools, codeCommitAvailable, err := s.loadProjectMCPAssistantTools(r, id, settings)
	if err != nil {
		return out, err
	}
	if codeCommitAvailable {
		if tool, ok := registry.ChatTool(projectToolCommitProjectFiles); ok {
			out = append(out, tool)
		}
	}
	for _, tool := range mcpTools {
		out = append(out, tool.Spec().chatTool())
	}
	return out, nil
}

func (s *Server) loadProjectMCPAssistantTools(r *http.Request, id identity, _ projectLLMSettings) ([]projectAssistantTool, bool, error) {
	if id.tenantPath == "" {
		return nil, false, errors.New("tenant context missing")
	}
	if id.clusterID == "" {
		return nil, false, errors.New("no workspace cluster on request (X-Kedge-Cluster missing) — cannot address the tenant MCP endpoint")
	}
	mcpEndpoint := s.mcpEndpoint(id.clusterID)
	tools, err := fetchProjectMCPTools(r.Context(), mcpEndpoint, r, id.tenantPath, s.mcpInsecureSkipTLSVerify)
	if err != nil {
		return nil, false, err
	}
	codeCommitAvailable := false
	for _, t := range tools {
		if projectMCPCommitToolAvailable(t.Name) {
			codeCommitAvailable = true
			break
		}
	}
	return projectAssistantMCPToolsForSpecs(tools, s.mcpInsecureSkipTLSVerify), codeCommitAvailable, nil
}

// mcpEndpoint returns the hub's unified MCPServer virtual-workspace endpoint for
// the given tenant logical-cluster ID. The provider always reaches MCP through
// the hub (KEDGE_HUB_URL), not its own host. The workspace MUST be addressed by
// logical-cluster ID (the hub-injected X-Kedge-Cluster), never by workspace
// path: the hub proxy's membership gate rejects path-form /clusters/<root:...>
// addressing with a 403 ("address workspaces by cluster ID, not by path").
func (s *Server) mcpEndpoint(clusterID string) string {
	return mcpServerURL(s.hubBase, clusterID, "default")
}

// mcpServerURL mirrors pkg/apiurl.MCPServerURL in the kedge monorepo:
// {hub}/services/mcpserver/{cluster}/apis/kedge.faros.sh/v1alpha1/mcpservers/{name}/mcp
// cluster is the workspace's logical-cluster ID, never its path.
func mcpServerURL(hubBase, cluster, mcpServerName string) string {
	return strings.TrimRight(hubBase, "/") +
		fmt.Sprintf("/services/mcpserver/%s/apis/kedge.faros.sh/v1alpha1/mcpservers/%s/mcp", cluster, mcpServerName)
}

func fetchProjectMCPTools(ctx context.Context, endpoint string, r *http.Request, tenantPath string, skipTLSVerify bool) ([]projectMCPTool, error) {
	params := []byte(`{}`)
	body, err := projectMCPRequest(ctx, endpoint, "tools/list", params, r, tenantPath, skipTLSVerify)
	if err != nil {
		return nil, err
	}
	var envelope struct {
		Tools []projectMCPTool `json:"tools"`
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, fmt.Errorf("decode tools/list response: %w", err)
	}
	if envelope.Error != nil {
		return nil, fmt.Errorf("provider MCP error %d: %s", envelope.Error.Code, envelope.Error.Message)
	}
	return envelope.Tools, nil
}

func callProjectMCPTool(ctx context.Context, endpoint string, r *http.Request, tenantPath string, skipTLSVerify bool, name string, args map[string]any) (string, error) {
	params, err := json.Marshal(map[string]any{
		"name":      name,
		"arguments": args,
	})
	if err != nil {
		return "", fmt.Errorf("encode tool args: %w", err)
	}
	body, err := projectMCPRequest(ctx, endpoint, "tools/call", params, r, tenantPath, skipTLSVerify)
	if err != nil {
		return "", err
	}
	var result struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		StructuredContent json.RawMessage `json:"structuredContent,omitempty"`
		IsError           bool            `json:"isError,omitempty"`
		ErrorMessage      string          `json:"error"`
	}
	if err := json.Unmarshal(body, &result); err == nil {
		textParts := make([]string, 0, len(result.Content))
		for _, item := range result.Content {
			if item.Type == "text" && item.Text != "" {
				textParts = append(textParts, item.Text)
			}
		}
		if result.IsError {
			if result.ErrorMessage != "" {
				return "", errors.New(result.ErrorMessage)
			}
			if len(textParts) > 0 {
				return "", errors.New(strings.Join(textParts, "\n"))
			}
			if len(result.StructuredContent) > 0 {
				return "", errors.New(string(result.StructuredContent))
			}
			return "", errors.New("tool call returned an error")
		}
		if len(textParts) > 0 {
			return strings.Join(textParts, "\n"), nil
		}
		if len(result.StructuredContent) > 0 {
			return string(result.StructuredContent), nil
		}
	}
	return string(body), nil
}

func projectMCPRequest(ctx context.Context, endpoint, method string, paramsJSON json.RawMessage, r *http.Request, tenantPath string, skipTLSVerify bool) (json.RawMessage, error) {
	env := map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  method,
		"params":  json.RawMessage(paramsJSON),
	}
	payload, err := json.Marshal(env)
	if err != nil {
		return nil, fmt.Errorf("encode MCP request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("new MCP request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if auth := r.Header.Get("Authorization"); strings.TrimSpace(auth) != "" {
		req.Header.Set("Authorization", auth)
	}
	if tenantPath != "" {
		req.Header.Set("X-Kedge-Tenant", tenantPath)
	}

	transport := projectMCPTransport(skipTLSVerify)
	client := &http.Client{Timeout: projectMCPCallTimeout, Transport: transport}
	resp, err := client.Do(req)
	if err != nil && projectMCPShouldRetryInsecure(endpoint, err, skipTLSVerify) {
		transport = projectMCPTransport(true)
		client = &http.Client{Timeout: projectMCPCallTimeout, Transport: transport}
		resp, err = client.Do(req)
	}
	if err != nil {
		return nil, fmt.Errorf("POST %s: %w", endpoint, err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, fmt.Errorf("read MCP body: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("MCP endpoint returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	raw := body
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		parsed, ok := firstSSELine(raw)
		if !ok {
			return nil, errors.New("MCP response had no SSE data")
		}
		raw = parsed
	}
	var envelope struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, fmt.Errorf("decode MCP JSON-RPC envelope: %w", err)
	}
	if envelope.Error != nil {
		return nil, fmt.Errorf("provider MCP error %d: %s", envelope.Error.Code, envelope.Error.Message)
	}
	return envelope.Result, nil
}

func projectMCPTransport(insecureSkipVerify bool) http.RoundTripper {
	if !insecureSkipVerify {
		return http.DefaultTransport
	}

	if baseTransport, ok := http.DefaultTransport.(*http.Transport); ok {
		clone := baseTransport.Clone()
		clone.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // dev-only
		return clone
	}

	return &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}} //nolint:gosec // dev-only
}

func projectMCPShouldRetryInsecure(endpoint string, err error, skipTLSVerify bool) bool {
	if skipTLSVerify {
		return false
	}
	if !isLocalhostEndpointForMCP(endpoint) {
		return false
	}
	var certErr *tls.CertificateVerificationError
	if errors.As(err, &certErr) {
		var unknownAuthority x509.UnknownAuthorityError
		if errors.As(certErr.Err, &unknownAuthority) {
			return true
		}
	}
	var unknownAuthority *x509.UnknownAuthorityError
	if errors.As(err, &unknownAuthority) {
		return true
	}
	var certInvalid *x509.CertificateInvalidError
	if errors.As(err, &certInvalid) {
		return true
	}
	var hostErr *x509.HostnameError
	if errors.As(err, &hostErr) {
		return true
	}
	errMsg := strings.ToLower(err.Error())
	return strings.Contains(errMsg, "unknown certificate authority") ||
		strings.Contains(errMsg, "certificate verification") ||
		strings.Contains(errMsg, "bad certificate") ||
		strings.Contains(errMsg, "certificate is not valid")
}

func isLocalhostEndpointForMCP(endpoint string) bool {
	u, err := url.Parse(endpoint)
	if err != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	return host == "localhost" || host == "127.0.0.1" || host == "::1" || strings.HasSuffix(host, ".localhost")
}

func firstSSELine(body []byte) (json.RawMessage, bool) {
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "data: ") {
			return json.RawMessage(strings.TrimPrefix(line, "data: ")), true
		}
	}
	return nil, false
}

func readProjectLLMSettings(ctx context.Context, c *asclient.Client) (projectLLMSettings, error) {
	settings := defaultProjectLLMSettings()
	secret, err := c.Resource(secretResource, projectLLMSecretNamespace).Get(ctx, projectLLMSecretName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return settings, nil
	}
	if err != nil {
		return settings, err
	}
	if v := secretDataValue(secret, "provider"); v != "" {
		settings.Provider = v
	}
	if v := secretDataValue(secret, "baseURL"); v != "" {
		settings.BaseURL = v
	}
	if v := secretDataValue(secret, "model"); v != "" {
		settings.Model = v
	}
	settings.APIKey = secretDataValue(secret, "apiKey")
	return settings, nil
}

func writeProjectLLMSettings(ctx context.Context, c *asclient.Client, settings projectLLMSettings) error {
	secret := projectLLMSettingsSecret(settings)
	existing, err := c.Resource(secretResource, projectLLMSecretNamespace).Get(ctx, projectLLMSecretName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = c.Resource(secretResource, projectLLMSecretNamespace).Create(ctx, secret, metav1.CreateOptions{})
		return err
	}
	if err != nil {
		return err
	}
	secret.SetResourceVersion(existing.GetResourceVersion())
	_, err = c.Resource(secretResource, projectLLMSecretNamespace).Update(ctx, secret, metav1.UpdateOptions{})
	return err
}

func defaultProjectLLMSettings() projectLLMSettings {
	return projectLLMSettings{
		Provider: defaultProjectLLMProvider,
		BaseURL:  defaultProjectLLMBaseURL,
		Model:    defaultProjectLLMModel,
	}
}

func normalizeProjectLLMSettings(settings *projectLLMSettings) error {
	settings.Provider = strings.TrimSpace(settings.Provider)
	if settings.Provider == "" {
		settings.Provider = defaultProjectLLMProvider
	}
	settings.APIKey = strings.TrimSpace(settings.APIKey)
	settings.BaseURL = strings.TrimSpace(settings.BaseURL)
	if settings.BaseURL == "" {
		settings.BaseURL = defaultProjectLLMBaseURL
	}
	googleCredential, usesGoogleServiceAccount, err := googleServiceAccountCredentialFromJSON(settings.APIKey)
	if err != nil && strings.EqualFold(settings.Provider, projectLLMProviderGoogle) {
		return err
	}
	if strings.EqualFold(settings.Provider, projectLLMProviderGoogle) {
		switch {
		case usesGoogleServiceAccount && isDefaultGoogleBaseURLCandidate(settings.BaseURL):
			settings.BaseURL = defaultProjectLLMGoogleCloudBaseURL(googleCredential.ProjectID)
		case !usesGoogleServiceAccount && isGenericOpenAIBaseURL(settings.BaseURL):
			settings.BaseURL = defaultProjectLLMGoogleBaseURL
		}
	}
	baseURL, err := normalizeLLMBaseURL(settings.BaseURL)
	if err != nil {
		return err
	}
	settings.BaseURL = baseURL
	if err := validateProjectLLMAPIKey(settings.Provider, settings.APIKey); err != nil {
		return err
	}
	if strings.TrimSpace(settings.Model) == "" {
		return newValidationError("model cannot be empty")
	}
	return nil
}

func isGenericOpenAIBaseURL(raw string) bool {
	return strings.EqualFold(strings.TrimRight(strings.TrimSpace(raw), "/"), defaultProjectLLMBaseURL)
}

func isDefaultGoogleBaseURLCandidate(raw string) bool {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	return raw == "" ||
		strings.EqualFold(raw, defaultProjectLLMBaseURL) ||
		strings.EqualFold(raw, defaultProjectLLMGoogleBaseURL)
}

func defaultProjectLLMGoogleCloudBaseURL(projectID string) string {
	return "https://aiplatform.googleapis.com"
}

func validateProjectLLMAPIKey(provider, apiKey string) error {
	if !strings.EqualFold(strings.TrimSpace(provider), projectLLMProviderGoogle) {
		return nil
	}
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return nil
	}
	if _, _, err := googleServiceAccountCredentialFromJSON(apiKey); err != nil {
		return err
	}
	if _, ok, _ := googleServiceAccountCredentialFromJSON(apiKey); ok {
		return nil
	}
	if looksLikeJWTOrOAuthToken(apiKey) {
		return newValidationError("Google Gemini settings require a Gemini API key string or service-account JSON credential, not an OAuth/JWT token")
	}
	return nil
}

func googleServiceAccountCredentialFromJSON(raw string) (googleServiceAccountCredential, bool, error) {
	raw = strings.TrimSpace(raw)
	if !strings.HasPrefix(raw, "{") {
		return googleServiceAccountCredential{}, false, nil
	}
	var credential googleServiceAccountCredential
	if err := json.Unmarshal([]byte(raw), &credential); err != nil {
		return googleServiceAccountCredential{}, true, newValidationError("Google service-account JSON credential is not valid JSON")
	}
	if !strings.EqualFold(strings.TrimSpace(credential.Type), "service_account") &&
		strings.TrimSpace(credential.ClientEmail) == "" &&
		strings.TrimSpace(credential.PrivateKey) == "" {
		return googleServiceAccountCredential{}, true, newValidationError("Google credentials must be a Gemini API key string or a service-account JSON credential")
	}
	missing := []string{}
	if strings.TrimSpace(credential.ProjectID) == "" {
		missing = append(missing, "project_id")
	}
	if strings.TrimSpace(credential.ClientEmail) == "" {
		missing = append(missing, "client_email")
	}
	if strings.TrimSpace(credential.PrivateKey) == "" {
		missing = append(missing, "private_key")
	}
	if strings.TrimSpace(credential.TokenURI) == "" {
		missing = append(missing, "token_uri")
	}
	if len(missing) > 0 {
		return googleServiceAccountCredential{}, true, newValidationError("Google service-account JSON credential is missing " + strings.Join(missing, ", "))
	}
	return credential, true, nil
}

func looksLikeJWTOrOAuthToken(raw string) bool {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return false
	}
	if strings.HasPrefix(raw, "ya29.") {
		return true
	}
	if strings.Count(raw, ".") != 2 {
		return false
	}
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return false
	}
	for _, part := range parts {
		if part == "" {
			return false
		}
	}
	headerBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return false
	}
	var header map[string]any
	if err := json.Unmarshal(headerBytes, &header); err != nil {
		return false
	}
	typ := strings.TrimSpace(fmt.Sprint(header["typ"]))
	_, hasAlg := header["alg"]
	_, hasKeyID := header["kid"]
	return strings.EqualFold(typ, "JWT") || hasAlg || hasKeyID
}

func (s projectLLMSettings) view() ProjectLLMSettingsView {
	return ProjectLLMSettingsView{
		Provider:   s.Provider,
		BaseURL:    s.BaseURL,
		Model:      s.Model,
		Configured: strings.TrimSpace(s.APIKey) != "",
	}
}

func projectLLMSettingsSecret(settings projectLLMSettings) *unstructured.Unstructured {
	data := map[string]interface{}{
		"provider": encodeSecretValue(settings.Provider),
		"baseURL":  encodeSecretValue(settings.BaseURL),
		"model":    encodeSecretValue(settings.Model),
	}
	if strings.TrimSpace(settings.APIKey) != "" {
		data["apiKey"] = encodeSecretValue(settings.APIKey)
	}
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "v1",
		"kind":       "Secret",
		"metadata": map[string]interface{}{
			"name":      projectLLMSecretName,
			"namespace": projectLLMSecretNamespace,
		},
		"type": "Opaque",
		"data": data,
	}}
}

func secretDataValue(secret *unstructured.Unstructured, key string) string {
	data, _, _ := unstructured.NestedStringMap(secret.Object, "data")
	if encoded := data[key]; encoded != "" {
		decoded, err := base64.StdEncoding.DecodeString(encoded)
		if err == nil {
			return string(decoded)
		}
	}
	stringData, _, _ := unstructured.NestedStringMap(secret.Object, "stringData")
	return stringData[key]
}

func encodeSecretValue(value string) string {
	return base64.StdEncoding.EncodeToString([]byte(value))
}

func normalizeLLMBaseURL(raw string) (string, error) {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	if raw == "" {
		raw = defaultProjectLLMBaseURL
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", newValidationError("baseURL must be an absolute HTTP(S) URL")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", newValidationError("baseURL must use http or https")
	}
	u.Path = normalizeLLMBasePath(u.Path, strings.ToLower(u.Host))
	return strings.TrimRight(u.String(), "/"), nil
}

func normalizeLLMBasePath(path string, host string) string {
	path = strings.TrimRight(strings.TrimSpace(path), "/")
	if strings.Contains(host, "generativelanguage.googleapis.com") {
		return ""
	}
	if strings.Contains(host, "aiplatform.googleapis.com") {
		lowerPath := strings.ToLower(path)
		if strings.Contains(lowerPath, "/endpoints/openapi") {
			return strings.TrimRight(path[:strings.Index(lowerPath, "/endpoints/openapi")], "/")
		}
	}
	return path
}

func projectPromptMessages(p *aiv1alpha1.Project, repository *ProjectRepositoryView, history []store.Message) []chatMessage {
	return projectPromptMessagesForProfile(p, repository, history, classifyProjectAssistantTurnProfile(history))
}

func projectPromptMessagesForProfile(p *aiv1alpha1.Project, repository *ProjectRepositoryView, history []store.Message, profile projectAssistantTurnProfile) []chatMessage {
	messages := []chatMessage{{Role: "system", Content: projectSystemPrompt(p, repository, profile)}}
	var lastRole, lastContent string
	for _, m := range history {
		if m.Role != aiv1alpha1.ProjectMessageRoleUser && m.Role != aiv1alpha1.ProjectMessageRoleAssistant {
			continue
		}
		content := strings.TrimSpace(m.Content)
		if content == "" {
			continue
		}
		if m.Role == aiv1alpha1.ProjectMessageRoleUser && lastRole == aiv1alpha1.ProjectMessageRoleUser && lastContent == content {
			continue
		}
		messages = append(messages, chatMessage{Role: m.Role, Content: content})
		lastRole = m.Role
		lastContent = content
	}
	return messages
}

func projectSystemPrompt(p *aiv1alpha1.Project, repository *ProjectRepositoryView, profiles ...projectAssistantTurnProfile) string {
	profile := projectAssistantTurnProfileDiscussion
	if len(profiles) > 0 {
		profile = normalizeProjectAssistantTurnProfile(profiles[0])
	}
	var b strings.Builder
	b.WriteString("You are the assistant for a persistent Kedge Project workspace. ")
	b.WriteString("Help the user reason about and build the application represented by this Project. ")
	b.WriteString("Do not narrate tool calls or say what tool you will call next in assistant prose; App Studio shows tool progress through its status and tool summary UI. ")
	b.WriteString("Do not claim that you changed files or deployed resources unless a tool result or other evidence supports it. ")
	b.WriteString("Do not invent App Studio product capabilities, UI tabs, cloud providers, infrastructure templates, setup flows, deployment targets, or integrations. ")
	b.WriteString("For App Studio product capability questions, answer only from explicit evidence in tool results, project metadata, project memory, or this system prompt; if evidence is missing, say \"I don't see that capability available in this workspace\" and explain what you can verify. ")
	b.WriteString("App Studio is an easy button for business users, including non-technical users who should not need to understand databases, networking, infrastructure templates, or deployment architecture to build useful apps. ")
	b.WriteString("Translate technical choices into business outcomes and safe next steps. ")
	b.WriteString("When a live development sandbox exists, assume App Studio source changes run in that sandbox; separate development sandbox guidance from production launch guidance. ")
	b.WriteString("Do not ask the user to choose databases, networking, infrastructure templates, or deployment architecture when App Studio can infer a safe next step from their business intent and available evidence. ")
	b.WriteString("When requirements are unclear, ask concise follow-up questions instead of guessing.\n\n")
	b.WriteString("Conversation mode: " + string(profile) + "\n")
	b.WriteString("Project metadata:\n")
	b.WriteString("- Name: " + p.Name + "\n")
	if p.Spec.Template != nil && strings.TrimSpace(p.Spec.Template.Name) != "" {
		b.WriteString("- Development template: " + strings.TrimSpace(p.Spec.Template.Name) + " (the development environment runs this infrastructure template in development mode; source directories map to its declared components, so keep new code under the component directories)\n")
	} else {
		b.WriteString("- Development template: NONE — the project has no development environment yet, so nothing runs and no preview exists until one is bound. Binding a template is your FIRST implementation step: translate the user's business intent into requirements yourself, pick the matching template via infrastructure__list_templates / infrastructure__describe_template, and bind it with select_project_template. Only templates that declare development components qualify. Template selection is INDEPENDENT of repository provisioning — never wait for the repository to bind a template; repository state only gates committing files, not template selection or workspace edits.\n")
	}
	b.WriteString("- Display name: " + p.Spec.DisplayName + "\n")
	if strings.TrimSpace(p.Spec.Description) != "" {
		b.WriteString("- Description: " + p.Spec.Description + "\n")
	}
	if repo := p.Spec.Repository; repo != nil && strings.TrimSpace(repo.RepositoryRef) != "" {
		repoRef := strings.TrimSpace(repo.RepositoryRef)
		b.WriteString("\nSource repository:\n")
		b.WriteString("- Repository resource: " + repoRef + "\n")
		if repoName := strings.TrimSpace(repo.Name); repoName != "" {
			b.WriteString("- Repository name: " + repoName + "\n")
		}
		if connectionRef := strings.TrimSpace(repo.ConnectionRef); connectionRef != "" {
			b.WriteString("- Connection: " + connectionRef + "\n")
		}
		if repository != nil && repository.Status != "" && repository.Status != projectRepositoryStatusReady && repository.Status != projectRepositoryStatusProvisioning {
			b.WriteString("- Repository status: " + repository.Status + "\n")
			if strings.TrimSpace(repository.Message) != "" {
				b.WriteString("- Repository issue: " + repository.Message + "\n")
			}
			b.WriteString("Do not attempt to commit files until the user restores the missing Code repository or connection.\n")
		} else {
			appendProjectAssistantModePrompt(&b, profile, repoRef)
		}
	}
	b.WriteString("\nProject memory:\n")
	appendMemoryList(&b, "Goals", p.Spec.Memory.Goals)
	appendMemoryList(&b, "Requirements", p.Spec.Memory.Requirements)
	appendMemoryList(&b, "Constraints", p.Spec.Memory.Constraints)
	return b.String()
}

func appendProjectAssistantModePrompt(b *strings.Builder, profile projectAssistantTurnProfile, repoRef string) {
	switch normalizeProjectAssistantTurnProfile(profile) {
	case projectAssistantTurnProfileDiscussion:
		b.WriteString("Answer exploratory or conceptual questions directly from the conversation and project memory. Do not inspect current workspace state unless the user asks a current-state question or asks to change/debug the app.\n")
	case projectAssistantTurnProfileGuidance:
		b.WriteString("Give practical guidance, recommendations, and tradeoffs. Do not claim to know current file or runtime state unless tool evidence is available; ask the user for missing context in plain language when needed.\n")
	case projectAssistantTurnProfileExploration:
		b.WriteString("Use read-only App Studio workflow, workspace-read, and aggregate MCP infrastructure discovery tools when current project state or available infrastructure templates are needed. Prefer plan_project_changes, check_project_readiness, list_project_files, read_project_file, search_project_files, infrastructure__list_templates, infrastructure__describe_template, infrastructure__list_instances, and infrastructure__get_instance for bounded inspection. Treat infrastructure templates as capability evidence, not as a menu the user must operate. Before deciding whether a template fits, describe the template and consult the template's agent.usage guidance when that field is available. ")
		appendProjectAssistantTemplateFitPrompt(b)
		b.WriteString("Do not edit, deploy, provision, or commit.\n")
	case projectAssistantTurnProfileDebugging:
		b.WriteString("Diagnose in read-only mode. Use check_project_readiness, list_project_files, read_project_file, search_project_files, get_runtime_status, and get_preview_url as needed. Do not mutate files, deploy runtime resources, or commit unless the user explicitly asks you to fix the issue.\n")
	case projectAssistantTurnProfileDebugFix:
		b.WriteString("First diagnose the issue with read-only workflow, workspace, and runtime status tools. ")
		appendProjectAssistantBuilderPrompt(b, repoRef)
	case projectAssistantTurnProfileImplementation:
		appendProjectAssistantBuilderPrompt(b, repoRef)
	}
}

func appendProjectAssistantBuilderPrompt(b *strings.Builder, repoRef string) {
	b.WriteString("Use check_project_readiness before mutating or verifying existing work so repository, memory, workspace context, and recommended checks come from the App Studio graph workflow. ")
	b.WriteString("When a named App Studio tool is deferred, load it first with tool_search using select:<tool_name>, then call the loaded tool. ")
	b.WriteString("Use prepare_project_deployment before discussing deployment handoff so build artifact readiness, blockers, and runtime handoff constraints come from the App Studio graph workflow. ")
	b.WriteString("Use deploy_project_runtime, get_runtime_status, and get_preview_url only as App Studio runtime graph workflows; they return structured not_configured blockers until a tenant RuntimeTarget exists. ")
	b.WriteString("For supporting infrastructure, use infrastructure__list_templates before naming any available template, infrastructure__describe_template before recommending values, and infrastructure__provision only after the user explicitly asks to create supporting infrastructure and the permission flow approves the call. ")
	appendProjectAssistantTemplateFitPrompt(b)
	b.WriteString("When the user asks for a supporting capability such as persistent data, first decide whether the current sandbox app can satisfy the development need before provisioning infrastructure. ")
	b.WriteString("Do not recommend a full application or runtime template just to satisfy a smaller need like persistent data, and do not duplicate App Studio's sandbox runtime unless the user is explicitly moving toward a production launch. ")
	b.WriteString("For existing projects, inspect relevant files in the App Studio workspace before editing: use list_project_files to discover paths, read_project_file for targeted files, and search_project_files when you need to locate code. ")
	b.WriteString("When requirements are unclear during implementation, call ask_follow_up with at most three concise questions instead of guessing. ")
	b.WriteString("Before source edits, call request_project_plan_approval with a concise batch plan, target path envelope, allowed edit operations, and acceptance criteria; after approval, keep workspace edits inside that envelope. ")
	b.WriteString("Do not give the user manual copy/paste file replacement instructions when App Studio edit tools are available; request approval and apply the change in the workspace instead. ")
	b.WriteString("Prefer small App Studio workspace mutations with write_file, apply_patch, and mkdir instead of rewriting a whole project. ")
	b.WriteString("After workspace mutations, commit the changed source/config files to the managed git source with commit_project_files using repositoryRef \"" + repoRef + "\". ")
	b.WriteString("Use provider-code only as the git-source boundary; do not use provider-code tools to inspect or mutate the live App Studio workspace. ")
	b.WriteString("The tool creates a visible RepositoryCommit request; use concise commit messages and include every generated source/config file needed for the app to run. ")
	b.WriteString("Do not paste large file contents into user-facing answers; summarize what you inspected instead. ")
	b.WriteString("Do not create another repository for this Project unless the user explicitly asks for a different repository.\n")
}

func appendProjectAssistantTemplateFitPrompt(b *strings.Builder) {
	b.WriteString("When infrastructure__describe_template returns provider-authored guidance, consult the template's agent.usage guidance and treat agent.usage as the provider-authored operating contract for the template. ")
	b.WriteString("Use it to decide the user outcome the template satisfies, the prerequisites it assumes, and whether it provisions a narrow supporting capability or a broader app/runtime stack that may duplicate App Studio's development sandbox. ")
	b.WriteString("Do not recommend a template merely because it contains one thing the user asked for. ")
	b.WriteString("For example, if the user asks for persistent todo data while already working in an App Studio sandbox, do not recommend the application template just because it includes Postgres. ")
	b.WriteString("Its agent.usage says it deploys a full 3-tier web app from frontend and backend container images behind one URL, so it is a production-style app deployment template, not a simple add a database to my sandbox app option. ")
	b.WriteString("Explain template fit in business terms, and call out when a template includes more than the user asked for. ")
}

func projectMCPToolsPrompt(tools []chatTool) string {
	if len(tools) == 0 {
		return "No tools were discovered for this workspace."
	}
	var b strings.Builder
	b.WriteString("Available tools in this workspace:\n")
	hasDatabricksTools := false
	for _, tool := range tools {
		desc := strings.TrimSpace(tool.Function.Description)
		if desc == "" {
			desc = "(no description)"
		}
		b.WriteString("- " + tool.Function.Name + ": " + desc + "\n")
		switch strings.TrimSpace(tool.Function.Name) {
		case projectToolDatabricksListTables, projectToolDatabricksDescribeTable:
			hasDatabricksTools = true
		}
	}
	if hasDatabricksTools {
		b.WriteString("Databricks guidance: use existing imported kedge Table resources only. ")
		b.WriteString("Refer to them by tableRef when designing app data models, inspecting cached table metadata, or asking the user which imported table to use through provider-databricks. ")
		b.WriteString("Do not call provider backend URLs from generated code. ")
		b.WriteString("Do not generate application code that queries Databricks tableRefs yet; no App Studio runtime data-access bridge is available in this workspace. ")
		b.WriteString("Do not create or import Databricks tables from App Studio, and do not embed Databricks credentials or raw warehouse auth config in generated code.\n")
	}
	return b.String()
}

func projectMCPToolsFailurePrompt(err error) string {
	if err == nil {
		return ""
	}
	return "External MCP tool discovery failed for this workspace: " + err.Error() + ". Tell the user that git-source tools are unavailable in this session, but App Studio workspace file tools may still be available."
}

func projectToolAllowed(name string) bool {
	return projectLocalToolAllowed(name) || projectMCPToolAllowed(name)
}

func projectLocalToolAllowed(name string) bool {
	return projectAssistantLocalToolRegistry(nil).Has(name)
}

func projectMCPToolAllowed(name string) bool {
	_, ok := projectAssistantMCPToolSpec(projectMCPTool{Name: name})
	return ok
}

func projectMCPCommitToolAvailable(name string) bool {
	return strings.EqualFold(strings.TrimSpace(name), projectToolCodeCommitFiles)
}

func projectMCPToolBaseName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	for _, sep := range []string{"__", ":", "/", "."} {
		if idx := strings.LastIndex(name, sep); idx >= 0 && idx+len(sep) < len(name) {
			name = name[idx+len(sep):]
		}
	}
	return strings.TrimSpace(name)
}

func projectAssistantMCPToolsForSpecs(tools []projectMCPTool, skipTLSVerify ...bool) []projectAssistantTool {
	out := make([]projectAssistantTool, 0, len(tools))
	insecureSkipTLSVerify := false
	if len(skipTLSVerify) > 0 {
		insecureSkipTLSVerify = skipTLSVerify[0]
	}
	for _, tool := range tools {
		spec, ok := projectAssistantMCPToolSpec(tool)
		if !ok {
			continue
		}
		toolSpec := spec
		out = append(out, projectAssistantToolFunc{
			spec: toolSpec,
			call: func(ctx context.Context, req projectAssistantToolCallRequest) (string, error) {
				if req.HTTPRequest == nil {
					return "", errors.New("HTTP request is required for aggregate MCP tools")
				}
				return callProjectMCPTool(ctx, req.MCPEndpoint, req.HTTPRequest, req.Identity.tenantPath, insecureSkipTLSVerify, toolSpec.Name, req.Arguments)
			},
		})
	}
	return out
}

func projectAssistantMCPToolSpec(tool projectMCPTool) (projectAssistantToolSpec, bool) {
	name := strings.TrimSpace(tool.Name)
	if name == "" {
		return projectAssistantToolSpec{}, false
	}
	risk := projectAssistantToolRiskRead
	switch name {
	case projectToolInfrastructureListTemplates,
		projectToolInfrastructureDescribeTemplate,
		projectToolInfrastructureListInstances,
		projectToolInfrastructureGetInstance:
	case projectToolInfrastructureProvision:
		risk = projectAssistantToolRiskRuntime
	case projectToolDatabricksListTables,
		projectToolDatabricksDescribeTable:
		risk = projectAssistantToolRiskRead
	default:
		return projectAssistantToolSpec{}, false
	}
	description := strings.TrimSpace(tool.Description)
	if description == "" {
		description = "Call the aggregate MCP tool " + name + "."
	}
	params := tool.InputSchema
	if len(params) == 0 || strings.TrimSpace(string(params)) == "" {
		params = json.RawMessage(`{"type":"object"}`)
	}
	return projectAssistantToolSpec{
		Name:        name,
		Description: description,
		Parameters:  params,
		Risk:        risk,
	}, true
}

func appendMemoryList(b *strings.Builder, label string, items []string) {
	b.WriteString(label + ":\n")
	if len(items) == 0 {
		b.WriteString("- none\n")
		return
	}
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item != "" {
			b.WriteString("- " + item + "\n")
		}
	}
}
