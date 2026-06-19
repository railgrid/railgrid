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
	"strings"

	"github.com/faroshq/provider-app-studio/workspace"
)

type projectAssistantToolRegistry struct {
	tools  []projectAssistantTool
	byName map[string]projectAssistantTool
}

func newProjectAssistantToolRegistry(tools ...projectAssistantTool) projectAssistantToolRegistry {
	byName := map[string]projectAssistantTool{}
	ordered := make([]projectAssistantTool, 0, len(tools))
	for _, tool := range tools {
		if tool == nil {
			continue
		}
		spec := tool.Spec()
		key := projectAssistantToolKey(spec.Name)
		if key == "" {
			continue
		}
		if _, exists := byName[key]; exists {
			continue
		}
		byName[key] = tool
		ordered = append(ordered, tool)
	}
	return projectAssistantToolRegistry{tools: ordered, byName: byName}
}

func (r projectAssistantToolRegistry) Get(name string) (projectAssistantTool, bool) {
	tool, ok := r.byName[projectAssistantToolKey(name)]
	return tool, ok
}

func (r projectAssistantToolRegistry) Has(name string) bool {
	_, ok := r.Get(name)
	return ok
}

func (r projectAssistantToolRegistry) ChatTool(name string) (chatTool, bool) {
	tool, ok := r.Get(name)
	if !ok {
		return chatTool{}, false
	}
	return tool.Spec().chatTool(), true
}

func (r projectAssistantToolRegistry) ChatTools(includeCommitBridge bool) []chatTool {
	out := make([]chatTool, 0, len(r.tools))
	for _, tool := range r.tools {
		spec := tool.Spec()
		if spec.Risk == projectAssistantToolRiskCommit && !includeCommitBridge {
			continue
		}
		out = append(out, spec.chatTool())
	}
	return out
}

func (r projectAssistantToolRegistry) Tools(includeCommitBridge bool) []projectAssistantTool {
	out := make([]projectAssistantTool, 0, len(r.tools))
	for _, tool := range r.tools {
		spec := tool.Spec()
		if spec.Risk == projectAssistantToolRiskCommit && !includeCommitBridge {
			continue
		}
		out = append(out, tool)
	}
	return out
}

func projectAssistantToolKey(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

func (s *Server) projectAssistantToolRegistry() projectAssistantToolRegistry {
	return projectAssistantLocalToolRegistry(s)
}

func projectAssistantLocalToolRegistry(server *Server) projectAssistantToolRegistry {
	return newProjectAssistantToolRegistry(
		projectAssistantToolFunc{
			spec: projectAssistantToolSpec{
				Name:        projectToolListProjectFiles,
				Description: "List files in the App Studio project workspace. Use this before editing an existing project.",
				Parameters:  json.RawMessage(fmt.Sprintf(`{"type":"object","properties":{"limit":{"type":"integer","minimum":1,"maximum":%d,"description":"Maximum number of file paths to return."}}}`, workspace.MaxListLimit)),
				Risk:        projectAssistantToolRiskRead,
			},
			call: func(ctx context.Context, req projectAssistantToolCallRequest) (string, error) {
				s, err := projectAssistantToolServer(server)
				if err != nil {
					return "", err
				}
				return projectAssistantToolJSONResult(s.workspaces.ListFiles(ctx, req.WorkspaceScope, workspace.ListOptions{Limit: projectToolInt(req.Arguments["limit"])}))
			},
		},
		projectAssistantToolFunc{
			spec: projectAssistantToolSpec{
				Name:        projectToolReadProjectFile,
				Description: "Read a bounded UTF-8 text file from the App Studio project workspace.",
				Parameters:  json.RawMessage(fmt.Sprintf(`{"type":"object","properties":{"path":{"type":"string","description":"Project-relative file path."},"maxBytes":{"type":"integer","minimum":1,"maximum":%d,"description":"Maximum bytes to return."}},"required":["path"]}`, workspace.MaxReadMaxBytes)),
				Risk:        projectAssistantToolRiskRead,
			},
			call: func(ctx context.Context, req projectAssistantToolCallRequest) (string, error) {
				s, err := projectAssistantToolServer(server)
				if err != nil {
					return "", err
				}
				return projectAssistantToolJSONResult(s.workspaces.ReadFile(ctx, req.WorkspaceScope, workspace.ReadOptions{
					Path:     projectToolString(req.Arguments["path"]),
					MaxBytes: projectToolInt(req.Arguments["maxBytes"]),
				}))
			},
		},
		projectAssistantToolFunc{
			spec: projectAssistantToolSpec{
				Name:        projectToolSearchProjectFiles,
				Description: "Search text files in the App Studio project workspace and return bounded path/fragments results.",
				Parameters:  json.RawMessage(fmt.Sprintf(`{"type":"object","properties":{"query":{"type":"string","description":"Text to search for."},"maxResults":{"type":"integer","minimum":1,"maximum":%d,"description":"Maximum matching files to return."}},"required":["query"]}`, workspace.MaxSearchLimit)),
				Risk:        projectAssistantToolRiskRead,
			},
			call: func(ctx context.Context, req projectAssistantToolCallRequest) (string, error) {
				s, err := projectAssistantToolServer(server)
				if err != nil {
					return "", err
				}
				return projectAssistantToolJSONResult(s.workspaces.SearchFiles(ctx, req.WorkspaceScope, workspace.SearchOptions{
					Query:      projectToolString(req.Arguments["query"]),
					MaxResults: projectToolInt(req.Arguments["maxResults"]),
				}))
			},
		},
		newProjectAssistantWorkflowTool(server),
		newProjectAssistantReadinessWorkflowTool(server),
		projectAssistantToolFunc{
			spec: projectAssistantToolSpec{
				Name:        projectToolAskFollowUp,
				Description: "Ask the user concise follow-up questions when App Studio needs missing product or implementation details before continuing.",
				Parameters:  json.RawMessage(`{"type":"object","properties":{"questions":{"type":"array","items":{"type":"string"},"minItems":1,"maxItems":3,"description":"Concise questions the user should answer before the assistant continues."}},"required":["questions"]}`),
				Risk:        projectAssistantToolRiskInput,
			},
			call: func(context.Context, projectAssistantToolCallRequest) (string, error) {
				return "", errors.New("follow-up questions are handled by the Eino assistant interrupt")
			},
		},
		projectAssistantToolFunc{
			spec: projectAssistantToolSpec{
				Name:        projectToolRequestProjectPlanApproval,
				Description: "Present a batch source-edit plan for user approval. After approval, App Studio may autonomously edit only the approved target paths until commit_project_files asks for final approval.",
				Parameters:  json.RawMessage(`{"type":"object","properties":{"summary":{"type":"string","description":"Short summary of the intended source changes."},"steps":{"type":"array","items":{"type":"string"},"minItems":1,"maxItems":12,"description":"Concrete implementation steps."},"targetPaths":{"type":"array","items":{"type":"string"},"minItems":1,"maxItems":50,"description":"Project-relative files or directories this plan is allowed to edit. Directories must end with /."},"allowedOperations":{"type":"array","items":{"type":"string","enum":["write_file","apply_patch","mkdir"]},"minItems":1,"maxItems":3,"description":"Workspace edit tools allowed by this plan."},"acceptanceCriteria":{"type":"array","items":{"type":"string"},"maxItems":12,"description":"Checks or outcomes that should be true before requesting commit."}},"required":["summary","steps","targetPaths","allowedOperations"]}`),
				Risk:        projectAssistantToolRiskPlan,
			},
			call: func(context.Context, projectAssistantToolCallRequest) (string, error) {
				return "", errors.New("plan approval is handled by the assistant run state")
			},
		},
		projectAssistantToolFunc{
			spec: projectAssistantToolSpec{
				Name:        projectToolWriteFile,
				Description: "Write a complete UTF-8 text file into the App Studio project workspace.",
				Parameters:  json.RawMessage(fmt.Sprintf(`{"type":"object","properties":{"path":{"type":"string","description":"Project-relative file path."},"content":{"type":"string","description":"Complete UTF-8 text content to write. Maximum %d bytes."}},"required":["path","content"]}`, workspace.MaxWriteBytes)),
				Risk:        projectAssistantToolRiskWrite,
			},
			call: func(ctx context.Context, req projectAssistantToolCallRequest) (string, error) {
				s, err := projectAssistantToolServer(server)
				if err != nil {
					return "", err
				}
				content, _ := projectToolRawString(req.Arguments["content"])
				return projectAssistantToolJSONResult(s.workspaces.WriteFile(ctx, req.WorkspaceScope, workspace.WriteOptions{
					Path:    projectToolString(req.Arguments["path"]),
					Content: content,
				}))
			},
		},
		projectAssistantToolFunc{
			spec: projectAssistantToolSpec{
				Name:        projectToolApplyPatch,
				Description: "Apply an exact text replacement to one App Studio workspace file. oldText must match exactly once unless replaceAll is true.",
				Parameters:  json.RawMessage(fmt.Sprintf(`{"type":"object","properties":{"path":{"type":"string","description":"Project-relative file path."},"oldText":{"type":"string","description":"Exact text to replace. Maximum patch payload %d bytes with newText."},"newText":{"type":"string","description":"Replacement text."},"replaceAll":{"type":"boolean","description":"Replace every exact match instead of requiring one match."}},"required":["path","oldText","newText"]}`, workspace.MaxPatchBytes)),
				Risk:        projectAssistantToolRiskWrite,
			},
			call: func(ctx context.Context, req projectAssistantToolCallRequest) (string, error) {
				s, err := projectAssistantToolServer(server)
				if err != nil {
					return "", err
				}
				oldText, _ := projectToolRawString(req.Arguments["oldText"])
				newText, _ := projectToolRawString(req.Arguments["newText"])
				return projectAssistantToolJSONResult(s.workspaces.ApplyPatch(ctx, req.WorkspaceScope, workspace.PatchOptions{
					Path:       projectToolString(req.Arguments["path"]),
					OldText:    oldText,
					NewText:    newText,
					ReplaceAll: projectToolBool(req.Arguments["replaceAll"]),
				}))
			},
		},
		projectAssistantToolFunc{
			spec: projectAssistantToolSpec{
				Name:        projectToolMkdir,
				Description: "Create a directory in the App Studio project workspace for later file writes.",
				Parameters:  json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","description":"Project-relative directory path."}},"required":["path"]}`),
				Risk:        projectAssistantToolRiskWrite,
			},
			call: func(ctx context.Context, req projectAssistantToolCallRequest) (string, error) {
				s, err := projectAssistantToolServer(server)
				if err != nil {
					return "", err
				}
				return projectAssistantToolJSONResult(s.workspaces.Mkdir(ctx, req.WorkspaceScope, workspace.MkdirOptions{Path: projectToolString(req.Arguments["path"])}))
			},
		},
		projectAssistantToolFunc{
			spec: projectAssistantToolSpec{
				Name:        projectToolCommitProjectFiles,
				Description: "Commit selected App Studio workspace text files to the managed git source through the Code provider.",
				Parameters:  json.RawMessage(fmt.Sprintf(`{"type":"object","properties":{"repositoryRef":{"type":"string","description":"Managed Code provider Repository resource name."},"paths":{"type":"array","items":{"type":"string"},"minItems":1,"maxItems":%d,"description":"Project-relative workspace file paths to commit."},"message":{"type":"string","description":"Commit message."},"branch":{"type":"string","description":"Optional branch override."}},"required":["repositoryRef","paths"]}`, projectCommitProjectFilesMax)),
				Risk:        projectAssistantToolRiskCommit,
			},
			call: func(ctx context.Context, req projectAssistantToolCallRequest) (string, error) {
				s, err := projectAssistantToolServer(server)
				if err != nil {
					return "", err
				}
				return s.commitProjectWorkspaceFiles(ctx, req.Identity, req.WorkspaceScope, req.ProjectRepositoryRef, req.MCPEndpoint, req.HTTPRequest, req.Arguments)
			},
		},
	)
}

func projectAssistantToolServer(server *Server) (*Server, error) {
	if server == nil || server.workspaces == nil {
		return nil, errors.New("project workspace store is not configured")
	}
	return server, nil
}
