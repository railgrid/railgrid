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
	"encoding/gob"
	"encoding/json"
	"path"
	"strings"
	"time"

	"github.com/faroshq/provider-app-studio/workspace"
)

const projectEinoAssistantSessionSnapshotKey = "appStudioProjectSnapshot"

type projectEinoAssistantSessionSnapshot struct {
	ProjectName       string   `json:"projectName"`
	DisplayName       string   `json:"displayName,omitempty"`
	RepoReady         bool     `json:"repoReady"`
	RepositoryRef     string   `json:"repositoryRef,omitempty"`
	RepositoryStatus  string   `json:"repositoryStatus,omitempty"`
	RepositoryMessage string   `json:"repositoryMessage,omitempty"`
	LastKnownBranch   string   `json:"lastKnownBranch"`
	LastFileSnapshot  []string `json:"lastFileSnapshot"`
	RecommendedChecks []string `json:"recommendedChecks,omitempty"`
	// DevelopmentComponents maps each of the bound template's development
	// component names to its contract: the workspace directory file sync
	// routes from ("." is the workspace root), plus the toolchain and start
	// command the sandbox executes it with. Application source outside every
	// listed directory never reaches the development sandbox, and source
	// written for a different toolchain than the one listed cannot run in it.
	DevelopmentComponents map[string]projectTemplateComponent `json:"developmentComponents,omitempty"`
	// CodingEnvironment is the server-owned contract for the active per-run
	// authoring/execution workspace. It is intentionally separate from
	// DevelopmentComponents: the latter describes a project's hosted preview
	// binding, while this environment is the private universal sandbox used by
	// the assistant turn itself.
	CodingEnvironment *projectEinoAssistantCodingEnvironment `json:"codingEnvironment,omitempty"`
	Memory            projectEinoAssistantSessionMemory      `json:"memory"`
	LastBuildRun      *projectEinoAssistantSessionBuild      `json:"lastBuildRun,omitempty"`
	ContextIssue      string                                 `json:"contextIssue,omitempty"`
}

// projectEinoAssistantCodingEnvironment is a server-authored, model-visible
// capability contract for the active per-run universal coding sandbox. Keep
// this contract small and declarative: it identifies where source and argv
// execution live, not a production runtime or a public preview endpoint.
type projectEinoAssistantCodingEnvironment struct {
	Kind              string   `json:"kind"`
	Status            string   `json:"status"`
	Template          string   `json:"template"`
	WorkspaceRoot     string   `json:"workspaceRoot"`
	ExecComponent     string   `json:"execComponent"`
	Toolchains        []string `json:"toolchains"`
	SourcePersistence string   `json:"sourcePersistence"`
	NetworkExposure   string   `json:"networkExposure"`
	PublicPreview     bool     `json:"publicPreview"`
}

type projectEinoAssistantSessionMemory struct {
	Goals        int `json:"goals"`
	Requirements int `json:"requirements"`
	Constraints  int `json:"constraints"`
}

type projectEinoAssistantSessionBuild struct {
	Name      string `json:"name,omitempty"`
	Phase     string `json:"phase,omitempty"`
	Branch    string `json:"branch,omitempty"`
	CommitSHA string `json:"commitSHA,omitempty"`
	CommitURL string `json:"commitURL,omitempty"`
	Message   string `json:"message,omitempty"`
	FileCount int64  `json:"fileCount,omitempty"`
}

func init() {
	gob.Register(projectEinoAssistantSessionSnapshot{})
}

func projectEinoAssistantSessionContextMessage(ctx context.Context, req projectAssistantRunRequest, runState *projectEinoAssistantRunState) (chatMessage, bool) {
	snapshot := projectEinoAssistantSnapshot(ctx, req, runState)
	if runState != nil {
		runState.SetSessionSnapshot(snapshot)
	}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		return chatMessage{}, false
	}
	return chatMessage{
		Role:    "system",
		Content: "Current project snapshot (authoritative for the start of this turn; LastFileSnapshot replaces an initial ls unless it contains +more or ContextIssue is non-empty):\n" + string(raw),
	}, true
}

func projectEinoAssistantSnapshot(ctx context.Context, req projectAssistantRunRequest, runState *projectEinoAssistantRunState) projectEinoAssistantSessionSnapshot {
	snapshot := projectEinoAssistantSessionSnapshot{
		LastFileSnapshot: []string{},
	}
	if req.Project != nil {
		snapshot.ProjectName = strings.TrimSpace(req.Project.Name)
		snapshot.DisplayName = strings.TrimSpace(req.Project.Spec.DisplayName)
		snapshot.Memory = projectEinoAssistantSessionMemory{
			Goals:        len(req.Project.Spec.Memory.Goals),
			Requirements: len(req.Project.Spec.Memory.Requirements),
			Constraints:  len(req.Project.Spec.Memory.Constraints),
		}
	}
	if req.Repository != nil {
		snapshot.RepoReady = req.Repository.Ready || req.Repository.Status == projectRepositoryStatusReady
		snapshot.RepositoryRef = strings.TrimSpace(req.Repository.Ref)
		snapshot.RepositoryStatus = strings.TrimSpace(req.Repository.Status)
		snapshot.RepositoryMessage = strings.TrimSpace(req.Repository.Message)
		if build := projectEinoAssistantLatestBuild(req.Repository.Commits); build != nil {
			snapshot.LastBuildRun = build
			snapshot.LastKnownBranch = strings.TrimSpace(build.Branch)
		}
	}
	files, issue := projectEinoAssistantWorkspaceSnapshot(ctx, req)
	snapshot.LastFileSnapshot = files
	snapshot.RecommendedChecks = projectAssistantRecommendedRuntimeChecks(files)
	snapshot.DevelopmentComponents = projectAssistantTemplateComponents(ctx, req)
	snapshot.CodingEnvironment = projectEinoAssistantCodingEnvironmentForRun(runState)
	snapshot.ContextIssue = issue
	return snapshot
}

// projectEinoAssistantCodingEnvironmentForRun projects the active sandbox's
// server-owned execution contract into model context. The run sandbox is
// always provisioned from the universal template and exposes one component
// rooted at the project workspace; still, read the component and lifecycle
// status from the live sandbox so a missing or closed target fails closed.
func projectEinoAssistantCodingEnvironmentForRun(runState *projectEinoAssistantRunState) *projectEinoAssistantCodingEnvironment {
	if runState == nil {
		return nil
	}
	sandbox := runState.Sandbox()
	if sandbox == nil {
		eligibility := runState.SandboxEligibility()
		if eligibility == nil || !eligibility.Eligible {
			return nil
		}
		return &projectEinoAssistantCodingEnvironment{
			Kind: "assistant-run-sandbox", Status: "available", Template: projectAssistantRunSandboxDefaultTemplate,
			WorkspaceRoot: ".", ExecComponent: projectAssistantRunSandboxWorkspaceVerb,
			Toolchains: []string{"go", "node", "python"}, SourcePersistence: "project-workspace",
			NetworkExposure: "internal", PublicPreview: false,
		}
	}
	metadata := sandbox.metadataSnapshot()
	status := strings.ToLower(strings.TrimSpace(metadata.Status))
	if (status != "active" && status != "ready") || strings.TrimSpace(metadata.Template) != projectAssistantRunSandboxDefaultTemplate {
		return nil
	}
	now := time.Now().UTC()
	if (!metadata.HardExpiresAt.IsZero() && !now.Before(metadata.HardExpiresAt)) ||
		(!metadata.IdleExpiresAt.IsZero() && !now.Before(metadata.IdleExpiresAt)) {
		return nil
	}
	component, ok := sandbox.target.Components[projectAssistantRunSandboxWorkspaceVerb]
	if !ok || path.Clean(strings.TrimSpace(component.WorkspacePath)) != "." {
		return nil
	}
	return &projectEinoAssistantCodingEnvironment{
		Kind:              "assistant-run-sandbox",
		Status:            "ready",
		Template:          projectAssistantRunSandboxDefaultTemplate,
		WorkspaceRoot:     ".",
		ExecComponent:     projectAssistantRunSandboxWorkspaceVerb,
		Toolchains:        []string{"go", "node", "python"},
		SourcePersistence: "project-workspace",
		NetworkExposure:   "internal",
		PublicPreview:     false,
	}
}

// projectAssistantTemplateComponents reads the bound template's development
// component → workspacePath map for the turn snapshot. Best-effort: a project
// without a template, a missing client, or a failed catalog read yields nil —
// the snapshot then simply carries no directory contract instead of failing
// the turn.
func projectAssistantTemplateComponents(ctx context.Context, req projectAssistantRunRequest) map[string]projectTemplateComponent {
	if req.Client == nil || req.Project == nil || req.Project.Spec.Template == nil {
		return nil
	}
	name := strings.TrimSpace(req.Project.Spec.Template.Name)
	if name == "" {
		return nil
	}
	info, err := fetchProjectTemplate(ctx, req.Client, name)
	if err != nil {
		return nil
	}
	return info.Components
}

func projectEinoAssistantLatestBuild(commits []ProjectRepositoryCommitView) *projectEinoAssistantSessionBuild {
	for _, commit := range commits {
		if strings.TrimSpace(commit.Name) == "" {
			continue
		}
		return &projectEinoAssistantSessionBuild{
			Name:      strings.TrimSpace(commit.Name),
			Phase:     strings.TrimSpace(commit.Phase),
			Branch:    strings.TrimSpace(commit.Branch),
			CommitSHA: strings.TrimSpace(commit.CommitSHA),
			CommitURL: strings.TrimSpace(commit.CommitURL),
			Message:   strings.TrimSpace(commit.Message),
			FileCount: commit.FileCount,
		}
	}
	return nil
}

func projectEinoAssistantWorkspaceSnapshot(ctx context.Context, req projectAssistantRunRequest) ([]string, string) {
	if req.Workspace == nil {
		return []string{}, ""
	}
	files, err := req.Workspace.ListFiles(ctx, req.WorkspaceScope, workspace.ListOptions{Limit: boundedWorkflowFileLimit(0)})
	if err != nil {
		return []string{}, "workspace file snapshot unavailable: " + err.Error()
	}
	out := make([]string, 0, len(files.Files)+1)
	for _, file := range files.Files {
		if path := strings.TrimSpace(file.Path); path != "" {
			out = append(out, path)
		}
	}
	if files.Truncated {
		out = append(out, "+more")
	}
	return out, ""
}

func cloneProjectEinoAssistantSessionSnapshot(src *projectEinoAssistantSessionSnapshot) *projectEinoAssistantSessionSnapshot {
	if src == nil {
		return nil
	}
	out := *src
	out.LastFileSnapshot = append([]string(nil), src.LastFileSnapshot...)
	out.RecommendedChecks = append([]string(nil), src.RecommendedChecks...)
	if src.CodingEnvironment != nil {
		environment := *src.CodingEnvironment
		environment.Toolchains = append([]string(nil), src.CodingEnvironment.Toolchains...)
		out.CodingEnvironment = &environment
	}
	if src.LastBuildRun != nil {
		build := *src.LastBuildRun
		out.LastBuildRun = &build
	}
	return &out
}
