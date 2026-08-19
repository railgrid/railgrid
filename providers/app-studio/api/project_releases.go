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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	aiv1alpha1 "github.com/faroshq/provider-app-studio/apis/ai/v1alpha1"
	asclient "github.com/faroshq/provider-app-studio/client"
)

// projectReleaseMaxItems matches the Code provider's bounded Package version
// mirror. A release page is useful only while its exact commit-tagged package
// evidence can still be observed; older commits fall out of that mirror and
// are therefore not advertised as deployable.
const projectReleaseMaxItems = 100

// projectReleaseView is the immutable release evidence exposed to the portal.
// Components intentionally reuse projectBuildCheckComponent so build status
// and release history cannot drift into two subtly different image contracts.
type projectReleaseView struct {
	Name        string                       `json:"name,omitempty"`
	Phase       string                       `json:"phase,omitempty"`
	Branch      string                       `json:"branch,omitempty"`
	CommitSHA   string                       `json:"commitSHA,omitempty"`
	CommitURL   string                       `json:"commitURL,omitempty"`
	Message     string                       `json:"message,omitempty"`
	CreatedAt   *time.Time                   `json:"createdAt,omitempty"`
	CompletedAt *time.Time                   `json:"completedAt,omitempty"`
	ReleaseID   string                       `json:"releaseID,omitempty"`
	Deployable  bool                         `json:"deployable"`
	Live        bool                         `json:"live"`
	Missing     []string                     `json:"missing"`
	Components  []projectBuildCheckComponent `json:"components"`
}

type projectReleasesResponse struct {
	Items []projectReleaseView `json:"items"`
}

// getProjectReleases handles GET /api/projects/{project}/releases. Release
// history is read from the project's RepositoryCommit records and the Code
// Package mirror; no client-provided image reference participates in the
// result.
func (s *Server) getProjectReleases(w http.ResponseWriter, r *http.Request) {
	c, _, p, ok := s.requireProjectWithClient(w, r)
	if !ok {
		return
	}
	response, err := s.projectReleases(r.Context(), c, p)
	if err != nil {
		writeProjectError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) projectReleases(ctx context.Context, c *asclient.Client, p *aiv1alpha1.Project) (projectReleasesResponse, error) {
	response := projectReleasesResponse{Items: []projectReleaseView{}}
	repositoryRef := projectLinkedRepositoryRef(p)
	if c == nil || repositoryRef == "" {
		return response, nil
	}

	commits, err := listProjectRepositoryCommits(ctx, c, repositoryRef)
	if err != nil {
		return projectReleasesResponse{}, err
	}
	// A release identity must be a completed repository commit with a full
	// object ID. Failed, in-progress, empty, abbreviated, and cross-repository
	// records are not releases and never appear as selectable history.
	releaseCommits := commits[:0]
	for i := range commits {
		phase, _, _ := unstructured.NestedString(commits[i].Object, "status", "phase")
		sha, _, _ := unstructured.NestedString(commits[i].Object, "status", "commitSHA")
		if strings.TrimSpace(phase) != "Succeeded" || !isFullGitCommitSHA(sha) {
			continue
		}
		releaseCommits = append(releaseCommits, commits[i])
	}
	commits = releaseCommits

	var components []projectBuildComponent
	if p != nil && p.Spec.Template != nil && strings.TrimSpace(p.Spec.Template.Name) != "" {
		info, err := fetchProjectTemplate(ctx, c, p.Spec.Template.Name)
		if err != nil {
			return projectReleasesResponse{}, err
		}
		components = projectBuildComponents(info)
	}

	var packages []unstructured.Unstructured
	if len(components) > 0 {
		list, err := c.Resource(codePackageResource, "").List(ctx, metav1.ListOptions{LabelSelector: codeLabelRepository + "=" + repositoryRef})
		if err != nil {
			return projectReleasesResponse{}, fmt.Errorf("list published packages: %w", err)
		}
		packages = list.Items
	}

	if len(commits) > projectReleaseMaxItems {
		commits = commits[:projectReleaseMaxItems]
	}
	response.Items = make([]projectReleaseView, 0, len(commits))
	for i := range commits {
		commit := &commits[i]
		view := projectReleaseViewFromCommit(commit, components)
		if view.Phase == "Succeeded" && view.CommitSHA != "" && len(components) > 0 {
			images := projectComponentImagesFromPackages(packages, repositoryRef, components, view.CommitSHA)
			view.Components, view.Missing = projectReleaseComponents(components, images)
			view.Deployable = len(view.Missing) == 0
			if view.Deployable {
				view.ReleaseID = projectReleaseID(repositoryRef, view.CommitSHA, view.Components)
			}
			view.Live = view.Deployable && projectReleaseMatchesProduction(p, view.Components)
		}
		response.Items = append(response.Items, view)
	}
	return response, nil
}

type projectReleaseEvidenceComponent struct {
	Name       string `json:"name"`
	ImageInput string `json:"imageInput"`
	Image      string `json:"image"`
	Digest     string `json:"digest"`
}

type projectReleaseEvidence struct {
	RepositoryRef string                            `json:"repositoryRef"`
	CommitSHA     string                            `json:"commitSHA"`
	Components    []projectReleaseEvidenceComponent `json:"components"`
}

// projectReleaseID is a server-owned content identity for one complete
// release. It includes the repository and commit identity plus the exact
// sorted component image refs/digests, so a package tag being repointed after
// GET /releases cannot retain the old promotion identity.
func projectReleaseID(repositoryRef, commitSHA string, components []projectBuildCheckComponent) string {
	repositoryRef = strings.TrimSpace(repositoryRef)
	commitSHA = strings.TrimSpace(commitSHA)
	if repositoryRef == "" || !isFullGitCommitSHA(commitSHA) || len(components) == 0 {
		return ""
	}
	evidence := make([]projectReleaseEvidenceComponent, 0, len(components))
	for _, component := range components {
		if !component.Built || strings.TrimSpace(component.Name) == "" || strings.TrimSpace(component.ImageInput) == "" || strings.TrimSpace(component.Image) == "" || strings.TrimSpace(component.Digest) == "" {
			return ""
		}
		evidence = append(evidence, projectReleaseEvidenceComponent{
			Name:       strings.TrimSpace(component.Name),
			ImageInput: strings.TrimSpace(component.ImageInput),
			Image:      strings.TrimSpace(component.Image),
			Digest:     strings.TrimSpace(component.Digest),
		})
	}
	sort.Slice(evidence, func(i, j int) bool {
		if evidence[i].Name != evidence[j].Name {
			return evidence[i].Name < evidence[j].Name
		}
		if evidence[i].ImageInput != evidence[j].ImageInput {
			return evidence[i].ImageInput < evidence[j].ImageInput
		}
		return evidence[i].Image < evidence[j].Image
	})
	canonical := projectReleaseEvidence{RepositoryRef: repositoryRef, CommitSHA: commitSHA, Components: evidence}
	raw, err := json.Marshal(canonical)
	if err != nil {
		return ""
	}
	digest := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(digest[:])
}

// listProjectRepositoryCommits applies both the label and spec repository
// checks. The selector is only a transport optimization: callers must not
// trust it as an authorization or ownership boundary.
func listProjectRepositoryCommits(ctx context.Context, c *asclient.Client, repositoryRef string) ([]unstructured.Unstructured, error) {
	repositoryRef = strings.TrimSpace(repositoryRef)
	if c == nil || repositoryRef == "" {
		return nil, nil
	}
	list, err := c.Resource(codeRepositoryCommitResource, "").List(ctx, metav1.ListOptions{LabelSelector: codeLabelRepository + "=" + repositoryRef})
	if err != nil {
		return nil, fmt.Errorf("list repository commits: %w", err)
	}
	commits := make([]unstructured.Unstructured, 0, len(list.Items))
	for i := range list.Items {
		if repositoryCommitBelongsToRepository(&list.Items[i], repositoryRef) {
			commits = append(commits, list.Items[i])
		}
	}
	sort.SliceStable(commits, func(i, j int) bool {
		return newerRepositoryCommit(&commits[i], &commits[j])
	})
	return commits, nil
}

// projectRepositoryCommitForSHA validates an explicit project commit target.
// It intentionally scans the complete commit history rather than the bounded
// project view or release response so source restoration and promotion share
// the same repository-ownership boundary.
func projectRepositoryCommitForSHA(ctx context.Context, c *asclient.Client, repositoryRef, requestedSHA string) (*unstructured.Unstructured, error) {
	repositoryRef = strings.TrimSpace(repositoryRef)
	requestedSHA = strings.TrimSpace(requestedSHA)
	if repositoryRef == "" {
		return nil, newValidationError("project has no Code repository")
	}
	if requestedSHA == "" {
		return nil, newValidationError("commitSHA is required")
	}
	if !isFullGitCommitSHA(requestedSHA) {
		return nil, newValidationError("commitSHA must be a full 40- or 64-character hexadecimal Git object ID")
	}
	commits, err := listProjectRepositoryCommits(ctx, c, repositoryRef)
	if err != nil {
		return nil, err
	}
	for i := range commits {
		commit := &commits[i]
		phase, _, _ := unstructured.NestedString(commit.Object, "status", "phase")
		sha, _, _ := unstructured.NestedString(commit.Object, "status", "commitSHA")
		if strings.TrimSpace(phase) == "Succeeded" && strings.TrimSpace(sha) == requestedSHA {
			return commit, nil
		}
	}
	return nil, newValidationError(fmt.Sprintf("commit %q is not a successful commit for this project's repository", requestedSHA))
}

func isFullGitCommitSHA(value string) bool {
	value = strings.TrimSpace(value)
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, char := range value {
		if !((char >= '0' && char <= '9') || (char >= 'a' && char <= 'f') || (char >= 'A' && char <= 'F')) {
			return false
		}
	}
	return true
}

func projectReleaseViewFromCommit(commit *unstructured.Unstructured, components []projectBuildComponent) projectReleaseView {
	commitView := projectRepositoryCommitView(commit)
	view := projectReleaseView{
		Name:        commitView.Name,
		Phase:       strings.TrimSpace(commitView.Phase),
		Branch:      strings.TrimSpace(commitView.Branch),
		CommitSHA:   strings.TrimSpace(commitView.CommitSHA),
		CommitURL:   strings.TrimSpace(commitView.CommitURL),
		Message:     strings.TrimSpace(commitView.Message),
		CreatedAt:   projectReleaseTimePtr(commitView.CreatedAt),
		CompletedAt: commitView.CompletedAt,
		Missing:     make([]string, 0, len(components)),
	}
	view.Components, view.Missing = projectReleaseComponents(components, nil)
	return view
}

func projectReleaseTimePtr(value time.Time) *time.Time {
	if value.IsZero() {
		return nil
	}
	value = value.UTC()
	return &value
}

func projectReleaseComponents(components []projectBuildComponent, images map[string]componentImageRef) ([]projectBuildCheckComponent, []string) {
	evidence := make([]projectBuildCheckComponent, 0, len(components))
	missing := make([]string, 0, len(components))
	for _, component := range components {
		row := projectBuildCheckComponent{Name: component.Name, ImageInput: component.ImageInput}
		if image, ok := images[component.Name]; ok && image.Image != "" {
			row.Built = true
			row.Image = image.Image
			row.Digest = image.Digest
			row.Tag = image.Tag
		} else {
			missing = append(missing, component.Name)
		}
		evidence = append(evidence, row)
	}
	sort.Strings(missing)
	return evidence, missing
}

func projectReleaseMatchesProduction(p *aiv1alpha1.Project, components []projectBuildCheckComponent) bool {
	binding := findProjectProductionBinding(p)
	if binding == nil || len(components) == 0 {
		return false
	}
	values := projectBindingValues(binding)
	if values == nil {
		return false
	}
	for _, component := range components {
		if !component.Built || strings.TrimSpace(component.ImageInput) == "" {
			return false
		}
		current, ok := values[component.ImageInput].(string)
		if !ok || strings.TrimSpace(current) != strings.TrimSpace(component.Image) {
			return false
		}
	}
	return true
}
