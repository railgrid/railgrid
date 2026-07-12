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

	aiv1alpha1 "github.com/faroshq/provider-app-studio/apis/ai/v1alpha1"
	"github.com/faroshq/provider-app-studio/workspace"
)

const (
	projectBuildConfigPath          = ".kedge/build.json"
	projectBuildWorkflowPath        = ".github/workflows/kedge-app-studio-build.yml"
	projectBuildConfigCommitMessage = "chore(app-studio): configure Railpack build"
	projectBuildBuilderRailpack     = "railpack"
	projectBuildRailpackAction      = "iloveitaly/github-action-railpack@167ed71230addc378f3fb13122046c09f71c0e5f"

	// projectBuildConfigSchema is the per-component build contract: one image
	// per template development component, keyed by the production imageInput it
	// feeds on launch.
	projectBuildConfigSchema = "app-studio.build/v1alpha2"
)

type projectBuildReconcileResponse struct {
	Status       string   `json:"status"`
	Builder      string   `json:"builder"`
	Template     string   `json:"template,omitempty"`
	Components   []string `json:"components,omitempty"`
	Files        []string `json:"files,omitempty"`
	CommitResult string   `json:"commitResult,omitempty"`
	Reason       string   `json:"reason,omitempty"`
	Error        string   `json:"error,omitempty"`
}

type projectBuildCIBlock struct {
	Provider     string `json:"provider"`
	WorkflowPath string `json:"workflowPath"`
}

// ensureProjectBuildConfig proactively writes the build workflow into the
// project's git repository so a build is wired in as soon as the project has a
// template and a repository — not only after the user happens to commit app
// code. Idempotent (a no-op when the managed files are already current) and a
// no-op when the project has no repository yet or no launchable components.
// httpReq carries the caller's Authorization for the git commit.
func (s *Server) ensureProjectBuildConfig(ctx context.Context, id identity, p *aiv1alpha1.Project, httpReq *http.Request) (*projectBuildReconcileResponse, error) {
	repoRef := projectLinkedRepositoryRef(p)
	if repoRef == "" || strings.TrimSpace(id.clusterID) == "" {
		return nil, nil
	}
	scope := projectWorkspaceScope(id, p.Name)
	return s.reconcileProjectBuildConfig(ctx, id, scope, p, repoRef, s.mcpEndpoint(id.clusterID), httpReq, map[string]any{})
}

func (s *Server) reconcileProjectBuildConfig(ctx context.Context, id identity, scope workspace.Scope, project *aiv1alpha1.Project, projectRepositoryRef, mcpEndpoint string, r *http.Request, args map[string]any) (*projectBuildReconcileResponse, error) {
	// A template-backed project builds one image per launchable development
	// component (build context = the component's workspacePath). Template-less
	// (legacy) projects keep the single whole-repo image derived from a
	// detected language profile.
	template, components, cerr := s.resolveProjectBuildComponents(ctx, id, project)
	if cerr != nil {
		// The project names a template but its build components could not be
		// resolved: skip rather than emit a wrong single-image build. The
		// source commit already succeeded; the assistant sees the reason.
		return &projectBuildReconcileResponse{
			Status:   "skipped",
			Builder:  projectBuildBuilderRailpack,
			Template: template,
			Reason:   "template build components could not be resolved: " + cerr.Error(),
		}, nil
	}

	// Only template-backed projects with launchable components build: there is
	// one image per component. A project with no such components (no template
	// yet) has nothing to build.
	if len(components) == 0 {
		return nil, nil
	}
	desired := projectManagedBuildFilesComponents(template, components)
	componentNames := make([]string, 0, len(components))
	for _, c := range components {
		componentNames = append(componentNames, c.Name)
	}
	// stamp decorates every response with the shared build context (template,
	// component names) so callers see which shape the config targets.
	stamp := func(resp *projectBuildReconcileResponse) *projectBuildReconcileResponse {
		resp.Builder = projectBuildBuilderRailpack
		resp.Template = template
		resp.Components = componentNames
		return resp
	}

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
		return stamp(&projectBuildReconcileResponse{
			Status: "current",
			Reason: "managed build configuration is already current",
		}), nil
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
		return stamp(&projectBuildReconcileResponse{
			Status: "failed",
			Files:  paths,
			Error:  err.Error(),
		}), nil
	}
	status := projectToolCallResultStatus(projectToolCodeCommitFiles, resp)
	if status != "succeeded" {
		return stamp(&projectBuildReconcileResponse{
			Status:       status,
			Files:        paths,
			CommitResult: resp,
		}), nil
	}
	for _, f := range changed {
		if _, err := s.workspaces.WriteFile(ctx, scope, workspace.WriteOptions{Path: f.Path, Content: f.Content}); err != nil {
			return nil, err
		}
	}
	return stamp(&projectBuildReconcileResponse{
		Status:       "committed",
		Files:        paths,
		CommitResult: resp,
	}), nil
}

// resolveProjectBuildComponents returns the bound template name and its
// launchable build components. A template-less project yields ("", nil, nil)
// so the caller skips (nothing to build). A project that names a template whose
// build components cannot be resolved yields a non-nil error so the caller
// surfaces it rather than silently skipping.
func (s *Server) resolveProjectBuildComponents(ctx context.Context, id identity, project *aiv1alpha1.Project) (string, []projectBuildComponent, error) {
	if project == nil || project.Spec.Template == nil {
		return "", nil, nil
	}
	name := strings.TrimSpace(project.Spec.Template.Name)
	if name == "" {
		return "", nil, nil
	}
	c, err := s.clientFor(id)
	if err != nil {
		return name, nil, err
	}
	info, err := fetchProjectTemplate(ctx, c, name)
	if err != nil {
		return name, nil, err
	}
	return name, projectBuildComponents(info), nil
}

func projectManagedBuildFilesComponents(template string, components []projectBuildComponent) []workspace.File {
	return []workspace.File{
		{Path: projectBuildConfigPath, Content: projectBuildConfigJSONComponents(template, components)},
		{Path: projectBuildWorkflowPath, Content: projectBuildWorkflowYAMLComponents(components)},
	}
}

type projectBuildConfigComponent struct {
	Name           string `json:"name"`
	Context        string `json:"context"`
	ImageInput     string `json:"imageInput"`
	PackagePattern string `json:"packagePattern"`
}

type projectBuildConfigDocumentV2 struct {
	SchemaVersion  string                        `json:"schemaVersion"`
	ManagedBy      string                        `json:"managedBy"`
	Builder        string                        `json:"builder"`
	Template       string                        `json:"template"`
	RailpackAction string                        `json:"railpackAction"`
	CI             projectBuildCIBlock           `json:"ci"`
	Registry       string                        `json:"registry"`
	TagPattern     string                        `json:"tagPattern"`
	Components     []projectBuildConfigComponent `json:"components"`
}

// projectBuildComponentPackagePattern is the ghcr.io package a component's
// image is published under: one repository per component so tiers never
// collide. {owner}/{repo} resolve at build time from GITHUB_REPOSITORY.
func projectBuildComponentPackagePattern(component string) string {
	return "ghcr.io/{owner}/{repo}/" + component
}

func projectBuildConfigJSONComponents(template string, components []projectBuildComponent) string {
	doc := projectBuildConfigDocumentV2{
		SchemaVersion:  projectBuildConfigSchema,
		ManagedBy:      "app-studio",
		Builder:        projectBuildBuilderRailpack,
		Template:       template,
		RailpackAction: projectBuildRailpackAction,
		CI: projectBuildCIBlock{
			Provider:     "github-actions",
			WorkflowPath: projectBuildWorkflowPath,
		},
		Registry:   "ghcr.io",
		TagPattern: "sha-{commitSHA}",
	}
	doc.Components = make([]projectBuildConfigComponent, 0, len(components))
	for _, c := range components {
		doc.Components = append(doc.Components, projectBuildConfigComponent{
			Name:           c.Name,
			Context:        c.Context,
			ImageInput:     c.ImageInput,
			PackagePattern: projectBuildComponentPackagePattern(c.Name),
		})
	}
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return "{}\n"
	}
	return string(out) + "\n"
}

// projectBuildWorkflowYAMLComponents renders the per-component build workflow:
// a matrix job builds and pushes one Railpack image per component (context =
// its workspace subdirectory) to ghcr, tagged sha-<commit>. That is all it
// does — the published packages are the source of truth, read back through the
// Code provider's Package resources; nothing is written into the repository.
func projectBuildWorkflowYAMLComponents(components []projectBuildComponent) string {
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
		"    name: Build ${{ matrix.component }} image with Railpack",
		"    runs-on: ubuntu-latest",
		"    strategy:",
		"      fail-fast: false",
		"      matrix:",
		"        include:",
	}
	for _, c := range components {
		lines = append(lines,
			"          - component: "+projectBuildYAMLQuote(c.Name),
			"            context: "+projectBuildYAMLQuote(c.Context),
		)
	}
	lines = append(lines,
		"    steps:",
		"      - name: Check out repository",
		"        uses: actions/checkout@v4",
		"",
		"      - name: Compute image coordinates",
		"        id: image",
		"        shell: bash",
		"        run: |",
		"          repo=\"${GITHUB_REPOSITORY,,}\"",
		"          image=\"ghcr.io/${repo}/${{ matrix.component }}\"",
		"          echo \"name=${image}\" >> \"$GITHUB_OUTPUT\"",
		"          echo \"tag=sha-${GITHUB_SHA}\" >> \"$GITHUB_OUTPUT\"",
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
		"          context: ${{ matrix.context }}",
		"          push: true",
		"          cache: true",
		"          cache_tag: ${{ steps.image.outputs.name }}:buildcache",
		"          tags: |",
		"            ${{ steps.image.outputs.name }}:${{ steps.image.outputs.tag }}",
		"            ${{ steps.image.outputs.name }}:latest",
	)
	return strings.Join(lines, "\n") + "\n"
}

// projectBuildYAMLQuote double-quotes a matrix scalar so values like "." (a
// single-component context) parse as strings, not YAML nulls or floats.
func projectBuildYAMLQuote(v string) string {
	return "\"" + strings.ReplaceAll(v, "\"", "\\\"") + "\""
}


