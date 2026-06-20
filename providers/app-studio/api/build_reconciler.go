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
	"io/fs"
	"net/http"
	"strings"

	"github.com/faroshq/provider-app-studio/workspace"
)

const (
	projectBuildConfigPath          = ".kedge/build.json"
	projectBuildWorkflowPath        = ".github/workflows/kedge-app-studio-build.yml"
	projectBuildArtifactPath        = ".kedge/build-artifact.json"
	projectBuildArtifactName        = "kedge-app-studio-build"
	projectBuildConfigCommitMessage = "chore(app-studio): configure Railpack build"
	projectBuildBuilderRailpack     = "railpack"
	projectBuildRailpackAction      = "iloveitaly/github-action-railpack@167ed71230addc378f3fb13122046c09f71c0e5f"
)

type projectBuildReconcileResponse struct {
	Status       string   `json:"status"`
	Builder      string   `json:"builder"`
	Profile      string   `json:"profile,omitempty"`
	Files        []string `json:"files,omitempty"`
	CommitResult string   `json:"commitResult,omitempty"`
	Reason       string   `json:"reason,omitempty"`
	Error        string   `json:"error,omitempty"`
}

type projectBuildConfigDocument struct {
	SchemaVersion string                    `json:"schemaVersion"`
	ManagedBy     string                    `json:"managedBy"`
	Builder       string                    `json:"builder"`
	Profile       string                    `json:"profile"`
	Railpack      projectBuildRailpackBlock `json:"railpack"`
	CI            projectBuildCIBlock       `json:"ci"`
	Image         projectBuildImageBlock    `json:"image"`
}

type projectBuildRailpackBlock struct {
	ActionRef string `json:"actionRef"`
	Context   string `json:"context"`
}

type projectBuildCIBlock struct {
	Provider     string `json:"provider"`
	WorkflowPath string `json:"workflowPath"`
	ArtifactName string `json:"artifactName"`
	ArtifactPath string `json:"artifactPath"`
}

type projectBuildImageBlock struct {
	Registry         string `json:"registry"`
	PackagePattern   string `json:"packagePattern"`
	TagPattern       string `json:"tagPattern"`
	DeploymentRef    string `json:"deploymentRef"`
	EvidenceContract string `json:"evidenceContract"`
}

func (s *Server) reconcileProjectBuildConfigAfterCommit(ctx context.Context, id identity, scope workspace.Scope, projectRepositoryRef, mcpEndpoint string, r *http.Request, args map[string]any, commitResult string) (string, error) {
	if projectBuildReconcileIsSelfCommit(args) {
		return commitResult, nil
	}
	if projectToolCallResultStatus(projectToolCodeCommitFiles, commitResult) != "succeeded" {
		return commitResult, nil
	}

	buildResult, err := s.reconcileProjectBuildConfig(ctx, id, scope, projectRepositoryRef, mcpEndpoint, r, args)
	if err != nil {
		return "", err
	}
	if buildResult == nil {
		return commitResult, nil
	}
	return projectCommitFilesBuildResponse(commitResult, buildResult), nil
}

func (s *Server) reconcileProjectBuildConfig(ctx context.Context, id identity, scope workspace.Scope, projectRepositoryRef, mcpEndpoint string, r *http.Request, args map[string]any) (*projectBuildReconcileResponse, error) {
	fileList, err := s.workspaces.ListFiles(ctx, scope, workspace.ListOptions{Limit: workspace.MaxListLimit})
	if err != nil {
		return nil, err
	}
	profile := projectBuildProfile(fileList)
	desired := projectManagedBuildFiles(profile)
	changed := make([]workspace.File, 0, len(desired))

	for _, f := range desired {
		read, err := s.workspaces.ReadFile(ctx, scope, workspace.ReadOptions{Path: f.Path, MaxBytes: workspace.MaxWriteBytes})
		switch {
		case err == nil && !read.Binary && !read.Truncated && read.Content == f.Content:
			continue
		case err == nil:
			changed = append(changed, f)
		case errors.Is(err, fs.ErrNotExist):
			changed = append(changed, f)
		default:
			return nil, err
		}
	}

	if len(changed) == 0 {
		return &projectBuildReconcileResponse{
			Status:  "current",
			Builder: projectBuildBuilderRailpack,
			Profile: profile,
			Reason:  "managed build configuration is already current",
		}, nil
	}

	files := make([]map[string]string, 0, len(changed))
	paths := make([]string, 0, len(changed))
	for _, f := range changed {
		files = append(files, map[string]string{"path": f.Path, "content": f.Content})
		paths = append(paths, f.Path)
	}

	commitArgs := map[string]any{
		"repositoryRef": projectRepositoryRef,
		"message":       projectBuildConfigCommitMessage,
		"files":         files,
	}
	if branch := projectToolString(args["branch"]); branch != "" {
		commitArgs["branch"] = branch
	}
	resp, err := callProjectMCPTool(ctx, mcpEndpoint, r, id.tenantPath, s.mcpInsecureSkipTLSVerify, projectToolCodeCommitFiles, commitArgs)
	if err != nil {
		return &projectBuildReconcileResponse{
			Status:  "failed",
			Builder: projectBuildBuilderRailpack,
			Profile: profile,
			Files:   paths,
			Error:   err.Error(),
		}, nil
	}
	status := projectToolCallResultStatus(projectToolCodeCommitFiles, resp)
	if status != "succeeded" {
		return &projectBuildReconcileResponse{
			Status:       status,
			Builder:      projectBuildBuilderRailpack,
			Profile:      profile,
			Files:        paths,
			CommitResult: resp,
		}, nil
	}
	for _, f := range changed {
		if _, err := s.workspaces.WriteFile(ctx, scope, workspace.WriteOptions{Path: f.Path, Content: f.Content}); err != nil {
			return nil, err
		}
	}
	return &projectBuildReconcileResponse{
		Status:       "committed",
		Builder:      projectBuildBuilderRailpack,
		Profile:      profile,
		Files:        paths,
		CommitResult: resp,
	}, nil
}

func projectBuildReconcileIsSelfCommit(args map[string]any) bool {
	if projectToolString(args["message"]) == projectBuildConfigCommitMessage {
		return true
	}
	paths := projectToolStringList(args["paths"])
	if len(paths) == 0 {
		return false
	}
	for _, p := range paths {
		clean, err := workspace.CleanProjectPath(p)
		if err != nil || !projectManagedBuildPath(clean) {
			return false
		}
	}
	return true
}

func projectManagedBuildPath(path string) bool {
	switch path {
	case projectBuildConfigPath, projectBuildWorkflowPath:
		return true
	default:
		return false
	}
}

func projectManagedBuildFiles(profile string) []workspace.File {
	return []workspace.File{
		{Path: projectBuildConfigPath, Content: projectBuildConfigJSON(profile)},
		{Path: projectBuildWorkflowPath, Content: projectBuildWorkflowYAML()},
	}
}

func projectBuildProfile(fileList workspace.FileList) string {
	paths := map[string]struct{}{}
	for _, f := range fileList.Files {
		paths[f.Path] = struct{}{}
	}
	switch {
	case projectHasPath(paths, "package.json"):
		return "node"
	case projectHasPath(paths, "pyproject.toml"), projectHasPath(paths, "requirements.txt"):
		return "python"
	case projectHasPath(paths, "go.mod"):
		return "go"
	case projectHasPath(paths, "Cargo.toml"):
		return "rust"
	case projectHasPath(paths, "composer.json"):
		return "php"
	case projectHasPath(paths, "pom.xml"), projectHasPath(paths, "build.gradle"), projectHasPath(paths, "build.gradle.kts"), projectHasPath(paths, "gradlew"):
		return "java"
	case projectHasPath(paths, "index.html"):
		return "static"
	default:
		return "auto"
	}
}

func projectHasPath(paths map[string]struct{}, path string) bool {
	_, ok := paths[path]
	return ok
}

func projectBuildConfigJSON(profile string) string {
	doc := projectBuildConfigDocument{
		SchemaVersion: "app-studio.build/v1alpha1",
		ManagedBy:     "app-studio",
		Builder:       projectBuildBuilderRailpack,
		Profile:       profile,
		Railpack: projectBuildRailpackBlock{
			ActionRef: projectBuildRailpackAction,
			Context:   ".",
		},
		CI: projectBuildCIBlock{
			Provider:     "github-actions",
			WorkflowPath: projectBuildWorkflowPath,
			ArtifactName: projectBuildArtifactName,
			ArtifactPath: projectBuildArtifactPath,
		},
		Image: projectBuildImageBlock{
			Registry:         "ghcr.io",
			PackagePattern:   "ghcr.io/{owner}/{repo}",
			TagPattern:       "sha-{commitSHA}",
			DeploymentRef:    "digest",
			EvidenceContract: projectBuildArtifactPath,
		},
	}
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return "{}\n"
	}
	return string(out) + "\n"
}

func projectCommitFilesBuildResponse(commitResult string, buildResult *projectBuildReconcileResponse) string {
	payload := map[string]any{}
	if err := json.Unmarshal([]byte(commitResult), &payload); err != nil {
		if phase := projectCommitResultPhase(commitResult); phase != "" {
			payload["phase"] = phase
		}
		payload["commitResult"] = commitResult
	}
	payload["buildConfiguration"] = buildResult
	out, err := json.Marshal(payload)
	if err != nil {
		return commitResult
	}
	return string(out)
}

func projectCommitResultPhase(commitResult string) string {
	var parsed struct {
		Phase string `json:"phase"`
	}
	if err := json.Unmarshal([]byte(commitResult), &parsed); err == nil && strings.TrimSpace(parsed.Phase) != "" {
		return parsed.Phase
	}
	switch projectToolCallResultStatus(projectToolCodeCommitFiles, commitResult) {
	case "succeeded":
		return "Succeeded"
	case "running":
		return "Running"
	case "failed":
		return "Failed"
	default:
		return ""
	}
}

func projectBuildWorkflowYAML() string {
	lines := []string{
		"name: App Studio Build",
		"",
		"on:",
		"  push:",
		"    branches:",
		"      - \"**\"",
		"  workflow_dispatch:",
		"",
		"permissions:",
		"  contents: read",
		"  packages: write",
		"",
		"jobs:",
		"  build:",
		"    name: Build OCI image with Railpack",
		"    runs-on: ubuntu-latest",
		"    steps:",
		"      - name: Check out repository",
		"        uses: actions/checkout@v4",
		"",
		"      - name: Compute image coordinates",
		"        id: image",
		"        shell: bash",
		"        run: |",
		"          image=\"ghcr.io/${GITHUB_REPOSITORY,,}\"",
		"          tag=\"sha-${GITHUB_SHA}\"",
		"          echo \"name=${image}\" >> \"$GITHUB_OUTPUT\"",
		"          echo \"tag=${tag}\" >> \"$GITHUB_OUTPUT\"",
		"",
		"      - name: Log in to GitHub Container Registry",
		"        uses: docker/login-action@v3",
		"        with:",
		"          registry: ghcr.io",
		"          username: ${{ github.actor }}",
		"          password: ${{ secrets.GITHUB_TOKEN }}",
		"",
		"      - name: Build and push image with Railpack",
		"        uses: " + projectBuildRailpackAction,
		"        with:",
		"          context: .",
		"          push: true",
		"          cache: true",
		"          cache_tag: ${{ steps.image.outputs.name }}:buildcache",
		"          tags: |",
		"            ${{ steps.image.outputs.name }}:${{ steps.image.outputs.tag }}",
		"",
		"      - name: Capture build evidence",
		"        shell: bash",
		"        env:",
		"          BUILD_ARTIFACT_PATH: " + projectBuildArtifactPath,
		"        run: |",
		"          set -euo pipefail",
		"          image=\"${{ steps.image.outputs.name }}\"",
		"          tag=\"${{ steps.image.outputs.tag }}\"",
		"          digest=\"$(docker buildx imagetools inspect \"${image}:${tag}\" --format '{{json .Manifest.Digest}}' | tr -d '\"')\"",
		"          mkdir -p .kedge",
		"          cat > \"$BUILD_ARTIFACT_PATH\" <<JSON",
		"          {",
		"            \"commit\": \"${GITHUB_SHA}\",",
		"            \"image\": \"${image}@${digest}\",",
		"            \"digest\": \"${digest}\",",
		"            \"tag\": \"${tag}\",",
		"            \"registry\": \"ghcr.io\",",
		"            \"package\": \"${image}\",",
		"            \"builder\": \"railpack\"",
		"          }",
		"          JSON",
		"",
		"      - name: Upload build evidence",
		"        uses: actions/upload-artifact@v4",
		"        with:",
		"          name: " + projectBuildArtifactName,
		"          path: " + projectBuildArtifactPath,
		"          if-no-files-found: error",
	}
	return strings.Join(lines, "\n") + "\n"
}
