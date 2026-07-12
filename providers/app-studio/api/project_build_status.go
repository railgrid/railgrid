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

// Build verification. The per-component build workflow (build_reconciler.go)
// builds and pushes one container image per component to the registry — it
// writes nothing back into the repository. The published packages ARE the
// build evidence: the Code provider's packages controller crawls them into
// Package CRs (name, image repository, and per-version tags + digests), which
// App Studio reads via the tenant client to answer "which components have a
// built image, and what is its digest". check_project_build turns that into a
// deterministic status the assistant polls, and launch (promote) consumes the
// same per-component digests.

package api

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	aiv1alpha1 "github.com/faroshq/provider-app-studio/apis/ai/v1alpha1"
	asclient "github.com/faroshq/provider-app-studio/client"
)

const (
	projectToolCheckProjectBuild = "check_project_build"
	projectToolGetBuildLogs      = "get_build_logs"
	projectToolRebuildProject    = "rebuild_project"

	// projectBuildWorkflowFileName is the basename of the build workflow the
	// scaffold commits — the Actions API addresses workflows by file name.
	projectBuildWorkflowFileName = "kedge-app-studio-build.yml"

	// projectToolCodeBuildStatus / projectToolCodeRebuild are the Code
	// provider's Actions tools exposed through the tenant MCP federation.
	projectToolCodeBuildStatus = "code__build_status"
	projectToolCodeRebuild     = "code__rebuild"
)

// projectBuildRepositoryRef returns the project's Code repository ref, or a
// validation error when none is bound / no workspace cluster is addressable.
func (s *Server) projectBuildRepositoryRef(id identity, p *aiv1alpha1.Project) (string, error) {
	repositoryRef := ""
	if p != nil && p.Spec.Repository != nil {
		repositoryRef = strings.TrimSpace(p.Spec.Repository.RepositoryRef)
	}
	if repositoryRef == "" {
		return "", newValidationError("project has no Code repository")
	}
	if strings.TrimSpace(id.clusterID) == "" {
		return "", newValidationError("no workspace cluster on request — cannot address the tenant MCP endpoint")
	}
	return repositoryRef, nil
}

// getProjectBuildLogs reads the latest CI build run (optionally for a commit)
// through the Code provider's build_status tool: run status/conclusion, each
// job's outcome, and failed jobs' log tails — so the assistant can see WHY a
// build failed, not just that it did.
func (s *Server) getProjectBuildLogs(ctx context.Context, id identity, p *aiv1alpha1.Project, httpReq *http.Request, ref string) (string, error) {
	repositoryRef, err := s.projectBuildRepositoryRef(id, p)
	if err != nil {
		return "", err
	}
	args := map[string]any{
		"repositoryRef":    repositoryRef,
		"workflowFileName": projectBuildWorkflowFileName,
		"maxLogLines":      200,
	}
	if ref = strings.TrimSpace(ref); ref != "" {
		args["ref"] = ref
	}
	return callProjectMCPTool(ctx, s.mcpEndpoint(id.clusterID), httpReq, id.tenantPath, s.mcpInsecureSkipTLSVerify, projectToolCodeBuildStatus, args)
}

// rebuildProject re-runs the build workflow without a code change (retry a
// flaky/failed build) through the Code provider's rebuild tool.
func (s *Server) rebuildProject(ctx context.Context, id identity, p *aiv1alpha1.Project, httpReq *http.Request, ref string) (string, error) {
	repositoryRef, err := s.projectBuildRepositoryRef(id, p)
	if err != nil {
		return "", err
	}
	args := map[string]any{
		"repositoryRef":    repositoryRef,
		"workflowFileName": projectBuildWorkflowFileName,
	}
	if ref = strings.TrimSpace(ref); ref != "" {
		args["ref"] = ref
	}
	return callProjectMCPTool(ctx, s.mcpEndpoint(id.clusterID), httpReq, id.tenantPath, s.mcpInsecureSkipTLSVerify, projectToolCodeRebuild, args)
}

// componentImageRef is a component's most-recent published image.
type componentImageRef struct {
	Image  string // pullable reference "<imageRepository>@<digest>"
	Digest string
	Tag    string // a human-facing tag on that digest (e.g. "sha-<commit>")
}

// resolveProjectComponentImages reads the project repository's published
// packages (Code provider Package CRs, labelled with the Repository ref) and
// maps each launchable component to its most-recent image. Components with no
// published package are absent from the result (not yet built). Returns an
// empty map (not an error) when the project has no repository.
func (s *Server) resolveProjectComponentImages(ctx context.Context, c *asclient.Client, p *aiv1alpha1.Project, components []projectBuildComponent) (map[string]componentImageRef, error) {
	repoRef := projectLinkedRepositoryRef(p)
	if repoRef == "" || c == nil {
		return map[string]componentImageRef{}, nil
	}
	list, err := c.Resource(codePackageResource, "").List(ctx, metav1.ListOptions{LabelSelector: codeLabelRepository + "=" + repoRef})
	if err != nil {
		return nil, fmt.Errorf("list published packages: %w", err)
	}

	out := make(map[string]componentImageRef, len(components))
	for _, comp := range components {
		pkg := findPackageForComponent(list.Items, comp.Name)
		if pkg == nil {
			continue
		}
		imageRepo, _, _ := unstructured.NestedString(pkg.Object, "status", "imageRepository")
		digest, tag := latestPackageVersion(pkg)
		if strings.TrimSpace(imageRepo) == "" || strings.TrimSpace(digest) == "" {
			continue
		}
		out[comp.Name] = componentImageRef{
			Image:  imageRepo + "@" + digest,
			Digest: digest,
			Tag:    tag,
		}
	}
	return out, nil
}

// findPackageForComponent picks the Package whose host package name identifies
// the component — the build publishes "<repo>/<component>", so the package name
// is the component name or ends with "/<component>".
func findPackageForComponent(items []unstructured.Unstructured, component string) *unstructured.Unstructured {
	suffix := "/" + component
	for i := range items {
		name, _, _ := unstructured.NestedString(items[i].Object, "status", "packageName")
		name = strings.TrimSpace(name)
		if name == component || strings.HasSuffix(name, suffix) {
			return &items[i]
		}
	}
	return nil
}

// latestPackageVersion returns the digest and a representative tag of a
// package's most-recent version (the crawler records versions most-recent
// first), preferring a non-"latest" tag so the commit tag surfaces.
func latestPackageVersion(pkg *unstructured.Unstructured) (digest, tag string) {
	versions, _, _ := unstructured.NestedSlice(pkg.Object, "status", "versions")
	if len(versions) == 0 {
		return "", ""
	}
	v, ok := versions[0].(map[string]any)
	if !ok {
		return "", ""
	}
	digest, _ = v["digest"].(string)
	tags, _ := v["tags"].([]any)
	for _, t := range tags {
		ts, _ := t.(string)
		if ts != "" && ts != "latest" {
			return digest, ts
		}
	}
	if len(tags) > 0 {
		ts, _ := tags[0].(string)
		return digest, ts
	}
	return digest, ""
}

// projectBuildCheckComponent is one launchable component's build state.
type projectBuildCheckComponent struct {
	Name       string `json:"name"`
	ImageInput string `json:"imageInput"`
	Built      bool   `json:"built"`
	Image      string `json:"image,omitempty"`
	Digest     string `json:"digest,omitempty"`
	Tag        string `json:"tag,omitempty"`
}

// projectBuildCheckResult is the deterministic build status the assistant polls.
type projectBuildCheckResult struct {
	// Status is one of: built (every launchable component has a published
	// image), incomplete (some do), none (none published yet), or unsupported
	// (template-less project).
	Status     string                       `json:"status"`
	Components []projectBuildCheckComponent `json:"components,omitempty"`
	Missing    []string                     `json:"missing,omitempty"`
	Note       string                       `json:"note"`
}

// checkProjectBuild reports which of the project template's launchable
// components have a published image. It is the build-doctor's read primitive
// and launch's precondition.
func (s *Server) checkProjectBuild(ctx context.Context, c *asclient.Client, id identity, p *aiv1alpha1.Project) (projectBuildCheckResult, error) {
	if c == nil {
		var err error
		if c, err = s.clientFor(id); err != nil {
			return projectBuildCheckResult{}, err
		}
	}
	if p == nil || p.Spec.Template == nil || strings.TrimSpace(p.Spec.Template.Name) == "" {
		return projectBuildCheckResult{
			Status: "unsupported",
			Note:   "this project is not backed by a template with launchable build components; select a template (e.g. application or simple-webapp) before building for launch",
		}, nil
	}
	info, err := fetchProjectTemplate(ctx, c, p.Spec.Template.Name)
	if err != nil {
		return projectBuildCheckResult{}, err
	}
	components := projectBuildComponents(info)
	if len(components) == 0 {
		return projectBuildCheckResult{
			Status: "unsupported",
			Note:   "the project's template declares no launchable build components",
		}, nil
	}

	images, err := s.resolveProjectComponentImages(ctx, c, p, components)
	if err != nil {
		return projectBuildCheckResult{}, err
	}

	result := projectBuildCheckResult{Components: make([]projectBuildCheckComponent, 0, len(components))}
	builtCount := 0
	for _, comp := range components {
		row := projectBuildCheckComponent{Name: comp.Name, ImageInput: comp.ImageInput}
		if img, ok := images[comp.Name]; ok && img.Image != "" {
			row.Built = true
			row.Image = img.Image
			row.Digest = img.Digest
			row.Tag = img.Tag
			builtCount++
		} else {
			result.Missing = append(result.Missing, comp.Name)
		}
		result.Components = append(result.Components, row)
	}
	sort.Strings(result.Missing)

	switch {
	case builtCount == len(components):
		result.Status = "built"
		result.Note = fmt.Sprintf("all %d component image(s) are published; the project can be promoted to production", builtCount)
	case builtCount > 0:
		result.Status = "incomplete"
		result.Note = fmt.Sprintf("%d of %d component images are published (missing: %s). GitHub Actions builds each component on commit and publishes it to the registry; check the repository's Actions tab — the missing builds may be running or may have failed", builtCount, len(components), strings.Join(result.Missing, ", "))
	default:
		result.Status = "none"
		result.Note = "no component images are published yet. Committing your app runs a GitHub Actions build that publishes one container image per component to the registry; they appear here once built. If images never appear, check the repository's Actions tab (Actions and package publishing must be enabled)."
	}
	return result, nil
}
