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
	"strings"

	"github.com/faroshq/provider-app-studio/workspace"
)

const projectEinoAssistantSessionSnapshotKey = "appStudioProjectSnapshot"

type projectEinoAssistantSessionSnapshot struct {
	ProjectName       string                            `json:"projectName"`
	DisplayName       string                            `json:"displayName,omitempty"`
	RepoReady         bool                              `json:"repoReady"`
	RepositoryRef     string                            `json:"repositoryRef,omitempty"`
	RepositoryStatus  string                            `json:"repositoryStatus,omitempty"`
	RepositoryMessage string                            `json:"repositoryMessage,omitempty"`
	LastKnownBranch   string                            `json:"lastKnownBranch"`
	LastFileSnapshot  []string                          `json:"lastFileSnapshot"`
	RecommendedChecks []string                          `json:"recommendedChecks,omitempty"`
	Memory            projectEinoAssistantSessionMemory `json:"memory"`
	LastBuildRun      *projectEinoAssistantSessionBuild `json:"lastBuildRun,omitempty"`
	ContextIssue      string                            `json:"contextIssue,omitempty"`
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
	snapshot := projectEinoAssistantSnapshot(ctx, req)
	if runState != nil {
		runState.SetSessionSnapshot(snapshot)
	}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		return chatMessage{}, false
	}
	return chatMessage{
		Role:    "system",
		Content: "Current project snapshot:\n" + string(raw),
	}, true
}

func projectEinoAssistantSnapshot(ctx context.Context, req projectAssistantRunRequest) projectEinoAssistantSessionSnapshot {
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
	snapshot.ContextIssue = issue
	return snapshot
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
	if src.LastBuildRun != nil {
		build := *src.LastBuildRun
		out.LastBuildRun = &build
	}
	return &out
}
