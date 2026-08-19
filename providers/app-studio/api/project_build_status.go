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

// Build verification. A template may declare a repository-owned GitHub Actions
// workflow that builds and pushes component images. The published packages
// ARE the build evidence: the Code provider's packages controller crawls them
// into Package CRs (name, image repository, and per-version tags + digests), which
// App Studio reads via the tenant client to answer "which components have a
// built image, and what is its digest". check_project_build turns that into a
// deterministic status the assistant polls, and launch (promote) consumes the
// same per-component digests.

package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path"
	"sort"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	aiv1alpha1 "github.com/faroshq/provider-app-studio/apis/ai/v1alpha1"
	asclient "github.com/faroshq/provider-app-studio/client"
)

const (
	projectToolCheckProjectBuild = "check_project_build"
	projectToolGetBuildLogs      = "get_build_logs"
	projectToolRebuildProject    = "rebuild_project"

	// projectBuildWorkflowFileName is the canonical compatibility workflow used
	// by App Studio projects created before templates declared repository CI.
	projectBuildWorkflowFileName = "faros-app-studio-build.yml"
	// projectLegacyBuildWorkflowFileName is the workflow used by older project
	// repositories. It is only attempted when the canonical workflow lookup or
	// dispatch returns an error; a successful canonical lookup with found=false
	// is authoritative and must not be masked by this compatibility path.
	projectLegacyBuildWorkflowFileName = "build.yaml"

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
		"repositoryRef": repositoryRef,
		"maxLogLines":   200,
	}
	if ref = strings.TrimSpace(ref); ref != "" {
		args["ref"] = ref
	}
	candidates, err := s.projectBuildWorkflowCandidates(ctx, id, p)
	if err != nil {
		return "", err
	}
	return s.callProjectBuildWorkflow(ctx, id, httpReq, projectToolCodeBuildStatus, args, candidates)
}

// rebuildProject re-runs the build workflow without a code change (retry a
// flaky/failed build) through the Code provider's rebuild tool.
func (s *Server) rebuildProject(ctx context.Context, id identity, p *aiv1alpha1.Project, httpReq *http.Request, ref string) (string, error) {
	repositoryRef, err := s.projectBuildRepositoryRef(id, p)
	if err != nil {
		return "", err
	}
	args := map[string]any{
		"repositoryRef": repositoryRef,
	}
	if ref = strings.TrimSpace(ref); ref != "" {
		args["ref"] = ref
	}
	candidates, err := s.projectBuildWorkflowCandidates(ctx, id, p)
	if err != nil {
		return "", err
	}
	return s.callProjectBuildWorkflow(ctx, id, httpReq, projectToolCodeRebuild, args, candidates)
}

func (s *Server) projectBuildWorkflowCandidates(ctx context.Context, id identity, p *aiv1alpha1.Project) ([]string, error) {
	declared := ""
	if p != nil && p.Spec.Template != nil && strings.TrimSpace(p.Spec.Template.Name) != "" {
		c, err := s.clientFor(id)
		if err != nil {
			return nil, err
		}
		info, err := fetchProjectTemplate(ctx, c, p.Spec.Template.Name)
		if err != nil {
			return nil, err
		}
		declared = info.BuildWorkflowPath
	}

	if declared != "" {
		return []string{path.Base(strings.TrimSpace(declared))}, nil
	}

	candidates := make([]string, 0, 2)
	seenFiles := map[string]struct{}{}
	for _, candidate := range []string{projectBuildWorkflowFileName, projectLegacyBuildWorkflowFileName} {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			continue
		}
		candidate = path.Base(candidate)
		if _, seen := seenFiles[candidate]; seen {
			continue
		}
		seenFiles[candidate] = struct{}{}
		candidates = append(candidates, candidate)
	}
	return candidates, nil
}

// callProjectBuildWorkflow tries workflow candidates in order. A declared
// template workflow is the only candidate; undeclared historical templates
// receive compatibility candidates. Fallback happens only on an error; every
// successful response, including found=false, is authoritative. args retains
// the caller's exact ref.
func (s *Server) callProjectBuildWorkflow(ctx context.Context, id identity, httpReq *http.Request, toolName string, args map[string]any, candidates []string) (string, error) {
	endpoint := s.mcpEndpoint(id.clusterID)
	errorsByCandidate := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		raw, err := callProjectMCPTool(ctx, endpoint, httpReq, id.tenantPath, s.mcpInsecureSkipTLSVerify, toolName, cloneProjectBuildWorkflowArgs(args, candidate))
		if err == nil {
			return raw, nil
		}
		errorsByCandidate = append(errorsByCandidate, fmt.Sprintf("workflow %q failed: %v", candidate, err))
	}
	return "", fmt.Errorf("build workflow candidates failed: %s", strings.Join(errorsByCandidate, "; "))
}

func cloneProjectBuildWorkflowArgs(args map[string]any, workflowFileName string) map[string]any {
	cloned := make(map[string]any, len(args)+1)
	for key, value := range args {
		cloned[key] = value
	}
	cloned["workflowFileName"] = workflowFileName
	return cloned
}

// componentImageRef is a component's published image for the reviewed source
// commit. The package crawler returns versions most-recent first, but recency
// is not provenance: a newer push for another commit must never be promoted.
type componentImageRef struct {
	Image  string // pullable reference "<imageRepository>@<digest>"
	Digest string
	Tag    string // a human-facing tag on that digest (e.g. "sha-<commit>")
}

// resolveProjectComponentImages reads the project repository's reviewed Git
// commit and published packages (Code provider Package CRs, labelled with the
// Repository ref), then maps each launchable component to the image version
// tagged for that exact commit. Components without an exact commit-tagged
// version are absent from the result (not yet built). Returns an empty map (not
// an error) when the project has no repository or no successful repository
// commit is available.
func (s *Server) resolveProjectComponentImages(ctx context.Context, c *asclient.Client, p *aiv1alpha1.Project, components []projectBuildComponent) (map[string]componentImageRef, error) {
	repoRef := projectLinkedRepositoryRef(p)
	if repoRef == "" || c == nil {
		return map[string]componentImageRef{}, nil
	}
	commitSHA, err := currentProjectRepositoryCommitSHA(ctx, c, repoRef)
	if err != nil {
		return nil, err
	}
	return s.resolveProjectComponentImagesForCommit(ctx, c, p, components, commitSHA)
}

// resolveProjectComponentImagesForCommit resolves package versions only after
// the caller has selected the authoritative reviewed commit. Keeping the
// commit selection separate lets checkProjectBuild expose that commit without
// issuing a second repository-commit list request.
func (s *Server) resolveProjectComponentImagesForCommit(ctx context.Context, c *asclient.Client, p *aiv1alpha1.Project, components []projectBuildComponent, commitSHA string) (map[string]componentImageRef, error) {
	repoRef := projectLinkedRepositoryRef(p)
	if repoRef == "" || c == nil || strings.TrimSpace(commitSHA) == "" {
		return map[string]componentImageRef{}, nil
	}
	list, err := c.Resource(codePackageResource, "").List(ctx, metav1.ListOptions{LabelSelector: codeLabelRepository + "=" + repoRef})
	if err != nil {
		return nil, fmt.Errorf("list published packages: %w", err)
	}
	return projectComponentImagesFromPackages(list.Items, repoRef, components, commitSHA), nil
}

// projectComponentImagesFromPackages resolves every launchable component from
// one already-fetched Package mirror. Keeping the package list outside the
// per-commit loop is important for the releases endpoint: the Code provider
// bounds each package's version mirror at 100, while a release page can show
// many historical commits without multiplying tenant API calls.
func projectComponentImagesFromPackages(items []unstructured.Unstructured, repoRef string, components []projectBuildComponent, commitSHA string) map[string]componentImageRef {
	repoRef = strings.TrimSpace(repoRef)
	commitSHA = strings.TrimSpace(commitSHA)
	if repoRef == "" || commitSHA == "" {
		return map[string]componentImageRef{}
	}

	out := make(map[string]componentImageRef, len(components))
	for _, comp := range components {
		pkg := findPackageForComponentInRepository(items, comp.Name, repoRef)
		if pkg == nil {
			continue
		}
		imageRepo, _, _ := unstructured.NestedString(pkg.Object, "status", "imageRepository")
		imageRepo = strings.TrimSpace(imageRepo)
		digest, tag := packageVersionForCommit(pkg, commitSHA)
		if !validImmutableImageRepository(imageRepo) || !validImmutablePackageDigest(digest) {
			continue
		}
		out[comp.Name] = componentImageRef{
			Image:  imageRepo + "@" + digest,
			Digest: digest,
			Tag:    tag,
		}
	}
	return out
}

// Package versions are registry observations, not user-supplied image refs.
// A release may only carry an image repository plus a digest-like algorithm
// reference; bare tags and values containing whitespace are never deployable.
// The Code API permits registry digest algorithms beyond sha256, so validate
// the immutable shape without unnecessarily narrowing the algorithm vocabulary.
func validImmutableImageRepository(value string) bool {
	value = strings.TrimSpace(value)
	return value != "" && !strings.ContainsAny(value, "@ \t\r\n")
}

func validImmutablePackageDigest(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || strings.ContainsAny(value, "@/ \t\r\n") {
		return false
	}
	algorithm, encoded, ok := strings.Cut(value, ":")
	return ok && strings.TrimSpace(algorithm) != "" && strings.TrimSpace(encoded) != ""
}

// currentProjectRepositoryCommitSHA returns the newest successful
// RepositoryCommit for the project's repository. RepositoryCommit is the
// Code provider's durable commit record and its status.commitSHA is the
// source revision the user reviewed. Both the label and spec.repositoryRef
// are checked because a list transport may return objects outside its selector
// (or stale objects may have inconsistent metadata).
func currentProjectRepositoryCommitSHA(ctx context.Context, c *asclient.Client, repositoryRef string) (string, error) {
	repositoryRef = strings.TrimSpace(repositoryRef)
	if c == nil || repositoryRef == "" {
		return "", nil
	}
	list, err := c.Resource(codeRepositoryCommitResource, "").List(ctx, metav1.ListOptions{LabelSelector: codeLabelRepository + "=" + repositoryRef})
	if err != nil {
		return "", fmt.Errorf("list repository commits: %w", err)
	}
	var selected *unstructured.Unstructured
	for i := range list.Items {
		item := &list.Items[i]
		if !repositoryCommitBelongsToRepository(item, repositoryRef) {
			continue
		}
		phase, _, _ := unstructured.NestedString(item.Object, "status", "phase")
		if strings.TrimSpace(phase) != "Succeeded" {
			continue
		}
		if selected == nil || newerRepositoryCommit(item, selected) {
			selected = item
		}
	}
	if selected == nil {
		return "", nil
	}
	commitSHA, _, _ := unstructured.NestedString(selected.Object, "status", "commitSHA")
	return strings.TrimSpace(commitSHA), nil
}

func repositoryCommitBelongsToRepository(commit *unstructured.Unstructured, repositoryRef string) bool {
	if commit == nil || strings.TrimSpace(repositoryRef) == "" {
		return false
	}
	if commit.GetLabels()[codeLabelRepository] != repositoryRef {
		return false
	}
	specRef, _, _ := unstructured.NestedString(commit.Object, "spec", "repositoryRef")
	return strings.TrimSpace(specRef) == repositoryRef
}

func newerRepositoryCommit(candidate, selected *unstructured.Unstructured) bool {
	if candidate == nil {
		return false
	}
	if selected == nil {
		return true
	}
	candidateTime := candidate.GetCreationTimestamp().Time
	selectedTime := selected.GetCreationTimestamp().Time
	if candidateTime.After(selectedTime) {
		return true
	}
	if candidateTime.Before(selectedTime) {
		return false
	}
	// Creation timestamps can be absent in synthetic/test objects. Keep the
	// choice deterministic in that case without treating list order as
	// provenance.
	return candidate.GetName() > selected.GetName()
}

// findPackageForComponentInRepository narrows the package match to the
// project repository before looking at the host package name. The GraphQL
// gateway normally applies the label selector server-side, but a transport
// can return extra objects (and stale Package objects can have inconsistent
// metadata), so both the label and spec.repositoryRef are required here.
func findPackageForComponentInRepository(items []unstructured.Unstructured, component, repositoryRef string) *unstructured.Unstructured {
	for i := range items {
		if !packageBelongsToRepository(&items[i], repositoryRef) {
			continue
		}
		if pkg := findPackageForComponent(items[i:i+1], component); pkg != nil {
			return pkg
		}
	}
	return nil
}

func packageBelongsToRepository(pkg *unstructured.Unstructured, repositoryRef string) bool {
	if pkg == nil || repositoryRef == "" {
		return false
	}
	if pkg.GetLabels()[codeLabelRepository] != repositoryRef {
		return false
	}
	specRef, _, _ := unstructured.NestedString(pkg.Object, "spec", "repositoryRef")
	return specRef == repositoryRef
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

// packageVersionForCommit returns the immutable digest whose tag is exactly
// the build workflow's sha-<GITHUB_SHA> tag for commitSHA. It scans every
// package version rather than assuming status.versions[0] is the requested
// build; the crawler's ordering only describes recency.
func packageVersionForCommit(pkg *unstructured.Unstructured, commitSHA string) (digest, tag string) {
	if pkg == nil {
		return "", ""
	}
	commitSHA = strings.TrimSpace(commitSHA)
	if commitSHA == "" {
		return "", ""
	}
	wantTag := "sha-" + commitSHA
	versions, _, _ := unstructured.NestedSlice(pkg.Object, "status", "versions")
	for _, raw := range versions {
		version, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		candidateDigest, _ := version["digest"].(string)
		candidateDigest = strings.TrimSpace(candidateDigest)
		if candidateDigest == "" {
			continue
		}
		for _, rawTag := range packageVersionTags(version["tags"]) {
			if rawTag == wantTag {
				return candidateDigest, rawTag
			}
		}
	}
	return "", ""
}

func packageVersionTags(raw any) []string {
	switch tags := raw.(type) {
	case []any:
		out := make([]string, 0, len(tags))
		for _, rawTag := range tags {
			if tag, ok := rawTag.(string); ok {
				out = append(out, tag)
			}
		}
		return out
	case []string:
		return tags
	default:
		return nil
	}
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

// projectBuildRunObservation is explanatory evidence from the Code provider.
// Artifact presence below remains authoritative: a completed CI run does not
// make a release promotable until every exact-commit Package is observable.
type projectBuildRunObservation struct {
	Found      bool                            `json:"found"`
	RunID      int64                           `json:"runID,omitempty"`
	URL        string                          `json:"url,omitempty"`
	HeadSHA    string                          `json:"headSHA,omitempty"`
	Status     string                          `json:"status,omitempty"`
	Conclusion string                          `json:"conclusion,omitempty"`
	Jobs       []projectBuildRunJobObservation `json:"jobs,omitempty"`
}

type projectBuildRunJobObservation struct {
	Name       string `json:"name,omitempty"`
	Status     string `json:"status,omitempty"`
	Conclusion string `json:"conclusion,omitempty"`
	FailureLog string `json:"failureLog,omitempty"`
}

type projectBuildRunCacheEntry struct {
	run       *projectBuildRunObservation
	errorText string
	expiresAt time.Time
}

type projectBuildRunInflight struct {
	done chan struct{}
}

const (
	projectBuildRunCacheTTL      = 8 * time.Second
	projectBuildRunErrorCacheTTL = 3 * time.Second
	projectBuildRunLookupTimeout = 12 * time.Second
)

// projectBuildCheckResult is the deterministic build status the assistant polls.
type projectBuildCheckResult struct {
	// Status is one of: built (every launchable component has a published
	// image), incomplete (some do), none (none published yet), or unsupported
	// (template-less project).
	Status string `json:"status"`
	// CommitSHA is the exact reviewed repository commit whose sha-<commit>
	// package tags were used for the component image plan.
	CommitSHA  string                       `json:"commitSHA,omitempty"`
	Components []projectBuildCheckComponent `json:"components,omitempty"`
	Missing    []string                     `json:"missing,omitempty"`
	Note       string                       `json:"note"`
	Run        *projectBuildRunObservation  `json:"run,omitempty"`
	RunError   string                       `json:"runError,omitempty"`
}

// observeProjectBuildRun returns a short-lived, singleflight CI observation.
// Failures are values rather than request failures so already-observed package
// artifacts stay visible and authoritative when GitHub/Code is unavailable.
func (s *Server) observeProjectBuildRun(ctx context.Context, id identity, p *aiv1alpha1.Project, r *http.Request, commitSHA string) (*projectBuildRunObservation, string) {
	commitSHA = strings.TrimSpace(commitSHA)
	repositoryRef := projectLinkedRepositoryRef(p)
	if commitSHA == "" || repositoryRef == "" || strings.TrimSpace(id.clusterID) == "" {
		return nil, ""
	}
	candidates, err := s.projectBuildWorkflowCandidates(ctx, id, p)
	if err != nil {
		return nil, "Build status temporarily unavailable."
	}
	key := strings.Join([]string{id.tenantPath, id.clusterID, id.user, repositoryRef, strings.Join(candidates, "\x1f"), commitSHA}, "\x00")

	for {
		now := time.Now()
		s.mu.Lock()
		if entry, ok := s.projectBuildRunCache[key]; ok && now.Before(entry.expiresAt) {
			s.mu.Unlock()
			return entry.run, entry.errorText
		}
		if pending := s.projectBuildRunInflight[key]; pending != nil {
			done := pending.done
			s.mu.Unlock()
			select {
			case <-done:
				continue
			case <-ctx.Done():
				return nil, "Build status temporarily unavailable."
			}
		}
		if s.projectBuildRunCache == nil {
			s.projectBuildRunCache = map[string]projectBuildRunCacheEntry{}
		} else {
			// This cache is process-local and keyed by immutable commits. Sweep
			// expired entries when admitting a new lookup so long-lived providers
			// do not retain every release ever observed.
			for cacheKey, entry := range s.projectBuildRunCache {
				if !now.Before(entry.expiresAt) {
					delete(s.projectBuildRunCache, cacheKey)
				}
			}
		}
		if s.projectBuildRunInflight == nil {
			s.projectBuildRunInflight = map[string]*projectBuildRunInflight{}
		}
		pending := &projectBuildRunInflight{done: make(chan struct{})}
		s.projectBuildRunInflight[key] = pending
		s.mu.Unlock()

		lookupCtx, cancel := context.WithTimeout(ctx, projectBuildRunLookupTimeout)
		resolver := s.projectBuildRunResolver
		if resolver == nil {
			resolver = s.fetchProjectBuildRun
		}
		run, err := resolver(lookupCtx, id, p, r, commitSHA)
		cancel()
		errorText := ""
		ttl := projectBuildRunCacheTTL
		if err != nil {
			errorText = "Build status temporarily unavailable."
			ttl = projectBuildRunErrorCacheTTL
			run = nil
		}

		s.mu.Lock()
		s.projectBuildRunCache[key] = projectBuildRunCacheEntry{run: run, errorText: errorText, expiresAt: time.Now().Add(ttl)}
		delete(s.projectBuildRunInflight, key)
		close(pending.done)
		s.mu.Unlock()
		return run, errorText
	}
}

func (s *Server) fetchProjectBuildRun(ctx context.Context, id identity, p *aiv1alpha1.Project, r *http.Request, commitSHA string) (*projectBuildRunObservation, error) {
	raw, err := s.getProjectBuildLogs(ctx, id, p, r, commitSHA)
	if err != nil {
		return nil, err
	}
	var wire struct {
		Found      bool                            `json:"found"`
		RunID      int64                           `json:"runID,omitempty"`
		HTMLURL    string                          `json:"htmlURL,omitempty"`
		HeadSHA    string                          `json:"headSHA,omitempty"`
		Status     string                          `json:"status,omitempty"`
		Conclusion string                          `json:"conclusion,omitempty"`
		Jobs       []projectBuildRunJobObservation `json:"jobs,omitempty"`
	}
	if err := json.Unmarshal([]byte(raw), &wire); err != nil {
		return nil, fmt.Errorf("decode Code build status: %w", err)
	}
	return &projectBuildRunObservation{
		Found: wire.Found, RunID: wire.RunID, URL: strings.TrimSpace(wire.HTMLURL),
		HeadSHA: strings.TrimSpace(wire.HeadSHA), Status: strings.TrimSpace(wire.Status),
		Conclusion: strings.TrimSpace(wire.Conclusion), Jobs: wire.Jobs,
	}, nil
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

	commitSHA, err := currentProjectRepositoryCommitSHA(ctx, c, projectLinkedRepositoryRef(p))
	if err != nil {
		return projectBuildCheckResult{}, err
	}
	return s.checkProjectBuildForCommit(ctx, c, p, components, commitSHA)
}

// checkProjectBuildForCommit evaluates one already-selected repository
// commit. It deliberately never consults RepositoryCommit history itself, so
// explicit historical promotion cannot silently drift back to the newest
// successful commit.
func (s *Server) checkProjectBuildForCommit(ctx context.Context, c *asclient.Client, p *aiv1alpha1.Project, components []projectBuildComponent, commitSHA string) (projectBuildCheckResult, error) {
	commitSHA = strings.TrimSpace(commitSHA)
	images, err := s.resolveProjectComponentImagesForCommit(ctx, c, p, components, commitSHA)
	if err != nil {
		return projectBuildCheckResult{}, err
	}

	result := projectBuildCheckResult{CommitSHA: commitSHA, Components: make([]projectBuildCheckComponent, 0, len(components))}
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
		result.Note = fmt.Sprintf("%d of %d component images are published (missing: %s). If the template declares repository-owned CI, check its Actions workflow; the missing images may still be building or the workflow may have failed", builtCount, len(components), strings.Join(result.Missing, ", "))
	default:
		result.Status = "none"
		if commitSHA == "" {
			result.Note = "no successful repository commit is available to identify the reviewed source revision; commit the project before promoting"
		} else {
			result.Note = fmt.Sprintf("no component images are published for reviewed commit %s yet. If the template declares repository-owned CI, its GitHub Actions workflow publishes one container image per component; they appear here once built. If images never appear, check the repository's Actions tab (Actions and package publishing must be enabled)", commitSHA)
		}
	}
	return result, nil
}

// checkProjectBuildAtCommit resolves the template's launchable component set
// and then evaluates only the supplied commit. Promotion uses this helper
// after revalidating RepositoryCommit ownership, ensuring GET release evidence
// or client image values can never become promotion authority.
func (s *Server) checkProjectBuildAtCommit(ctx context.Context, c *asclient.Client, p *aiv1alpha1.Project, commitSHA string) (projectBuildCheckResult, error) {
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
	return s.checkProjectBuildForCommit(ctx, c, p, components, commitSHA)
}
