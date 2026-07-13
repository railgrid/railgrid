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
	"net/http"
	"strings"

	aiv1alpha1 "github.com/faroshq/provider-app-studio/apis/ai/v1alpha1"
	asclient "github.com/faroshq/provider-app-studio/client"
)

// Project lifecycle checkpoints — the four gates a project passes through on
// its way to a running production deployment. They are computed on demand from
// the backing Code- and Infrastructure-provider resources (there is no
// persisted status); the portal renders them as header chips and the assistant
// reads them (get_project_checkpoints) to know what to do next.
const (
	projectCheckpointTemplate   = "template"
	projectCheckpointGit        = "git"
	projectCheckpointCI         = "ci"
	projectCheckpointProduction = "production"

	// checkpoint states.
	projectCheckpointStateDone    = "done"    // satisfied
	projectCheckpointStatePending = "pending" // not yet satisfied; in progress or awaiting an action
	projectCheckpointStateBlocked = "blocked" // cannot progress until an earlier checkpoint or an external prerequisite is met
	projectCheckpointStateError   = "error"   // failed and needs attention

	// remediation kinds — how the checkpoint is advanced.
	projectCheckpointFixAuto   = "auto"   // the assistant can advance it by calling Tool
	projectCheckpointFixManual = "manual" // a human must act (click a button, grant an OAuth scope)

	projectToolGetCheckpoints = "get_project_checkpoints"
)

// projectCheckpointRemediation tells a consumer how to advance a checkpoint:
// whether the assistant can do it (Tool names the assistant tool to call) or a
// human must (ActionURL / Message describe the manual step).
type projectCheckpointRemediation struct {
	Kind      string `json:"kind"`
	Tool      string `json:"tool,omitempty"`
	ActionURL string `json:"actionUrl,omitempty"`
	Message   string `json:"message,omitempty"`
}

type projectCheckpoint struct {
	Key         string                        `json:"key"`
	Label       string                        `json:"label"`
	State       string                        `json:"state"`
	Reason      string                        `json:"reason,omitempty"`
	Remediation *projectCheckpointRemediation `json:"remediation,omitempty"`
}

type projectCheckpointsResponse struct {
	Items []projectCheckpoint `json:"items"`
}

// projectCheckpoints assembles the four lifecycle checkpoints from the
// project's current backing resources. It reuses the same primitives the
// portal already reads (repository view, build check, production binding) so
// the chips, the promotion form, and the assistant never disagree.
func (s *Server) projectCheckpoints(ctx context.Context, c *asclient.Client, id identity, p *aiv1alpha1.Project) projectCheckpointsResponse {
	templateName := ""
	if p.Spec.Template != nil {
		templateName = strings.TrimSpace(p.Spec.Template.Name)
	}
	repo := projectRepositoryView(ctx, c, p)

	template := s.checkpointTemplate(templateName)
	git := s.checkpointGit(repo)
	ci := s.checkpointCI(repo, git.State)

	// The build check is the promotion gate; compute it once and share it
	// between the CI-context reason and the production checkpoint. It is a
	// no-op ("unsupported") for template-less projects.
	build, err := s.checkProjectBuild(ctx, c, id, p)
	if err != nil {
		build = projectBuildCheckResult{Status: "unavailable", Note: err.Error()}
	}
	production := s.checkpointProduction(ctx, c, id, p, templateName, build)

	return projectCheckpointsResponse{Items: []projectCheckpoint{template, git, ci, production}}
}

func (s *Server) checkpointTemplate(templateName string) projectCheckpoint {
	cp := projectCheckpoint{Key: projectCheckpointTemplate, Label: "Template"}
	if templateName != "" {
		cp.State = projectCheckpointStateDone
		cp.Reason = "Bound to template " + templateName + "."
		return cp
	}
	cp.State = projectCheckpointStatePending
	cp.Reason = "No template selected yet."
	cp.Remediation = &projectCheckpointRemediation{
		Kind:    projectCheckpointFixAuto,
		Tool:    projectToolSelectTemplate,
		Message: "Select a development template for this project.",
	}
	return cp
}

func (s *Server) checkpointGit(repo *ProjectRepositoryView) projectCheckpoint {
	cp := projectCheckpoint{Key: projectCheckpointGit, Label: "Git"}
	if repo == nil || strings.TrimSpace(repo.Ref) == "" {
		cp.State = projectCheckpointStatePending
		cp.Reason = "No repository connected."
		cp.Remediation = &projectCheckpointRemediation{
			Kind:    projectCheckpointFixAuto,
			Tool:    projectToolHydrateWorkspace,
			Message: "Connect a Git repository to hold the project's source.",
		}
		return cp
	}
	switch repo.Status {
	case projectRepositoryStatusReady:
		cp.State = projectCheckpointStateDone
		cp.Reason = "Repository " + repoDisplayName(repo) + " is connected."
	case projectRepositoryStatusConnectionMissing:
		cp.State = projectCheckpointStateBlocked
		cp.Reason = firstNonEmpty(repo.Message, "The repository has no Code connection.")
		cp.Remediation = &projectCheckpointRemediation{
			Kind:    projectCheckpointFixManual,
			Message: "Connect a Git provider (grant access) to establish the repository.",
		}
	case projectRepositoryStatusFailed:
		cp.State = projectCheckpointStateError
		cp.Reason = firstNonEmpty(repo.Message, "The repository failed to reconcile.")
		cp.Remediation = &projectCheckpointRemediation{
			Kind:    projectCheckpointFixManual,
			Message: "Resolve the repository error, then retry.",
		}
	default: // Provisioning / Unavailable / RepositoryMissing / empty
		cp.State = projectCheckpointStatePending
		cp.Reason = firstNonEmpty(repo.Message, "Repository is provisioning.")
	}
	return cp
}

// checkpointCI reports whether CI is established in the repository. App Studio
// commits generated GitHub Actions workflow(s) alongside the source, so a
// successful RepositoryCommit is the "CI committed in git" signal.
func (s *Server) checkpointCI(repo *ProjectRepositoryView, gitState string) projectCheckpoint {
	cp := projectCheckpoint{Key: projectCheckpointCI, Label: "CI"}
	if gitState != projectCheckpointStateDone {
		cp.State = projectCheckpointStateBlocked
		cp.Reason = "Connect a repository before committing CI."
		return cp
	}
	if repo != nil {
		for _, commit := range repo.Commits {
			if commit.Phase == "Succeeded" {
				cp.State = projectCheckpointStateDone
				cp.Reason = "CI workflow committed to the repository."
				return cp
			}
		}
	}
	cp.State = projectCheckpointStatePending
	cp.Reason = "No commit has landed yet; committing the project adds the CI workflow."
	cp.Remediation = &projectCheckpointRemediation{
		Kind:    projectCheckpointFixAuto,
		Tool:    projectToolCommitProjectFiles,
		Message: "Commit the project to establish CI (grant the connection 'workflow' scope if the commit is rejected).",
	}
	return cp
}

func (s *Server) checkpointProduction(ctx context.Context, c *asclient.Client, id identity, p *aiv1alpha1.Project, templateName string, build projectBuildCheckResult) projectCheckpoint {
	cp := projectCheckpoint{Key: projectCheckpointProduction, Label: "Production"}
	if templateName == "" {
		cp.State = projectCheckpointStateBlocked
		cp.Reason = "Select a template before promoting to production."
		return cp
	}

	if prod := findProjectProductionBinding(p); prod != nil {
		st := projectProviderBindingStatus(ctx, c, p, *prod, id)
		if strings.EqualFold(st.Phase, "Ready") {
			cp.State = projectCheckpointStateDone
			cp.Reason = firstNonEmpty(prodRunningReason(st), "Production is running.")
			return cp
		}
		cp.State = projectCheckpointStatePending
		cp.Reason = firstNonEmpty(st.Phase, "Production is deploying.")
		return cp
	}

	// Never promoted. Promotion is a manual, user-initiated action, but it is
	// only offered once the build is green.
	if build.Status == "built" {
		cp.State = projectCheckpointStatePending
		cp.Reason = "Build is green; ready to promote to production."
		cp.Remediation = &projectCheckpointRemediation{
			Kind:    projectCheckpointFixManual,
			Message: "Click \"Promote to production\" to launch the production instance.",
		}
		return cp
	}
	cp.State = projectCheckpointStateBlocked
	cp.Reason = firstNonEmpty(build.Note, "The build must be green before promoting.")
	cp.Remediation = &projectCheckpointRemediation{
		Kind:    projectCheckpointFixAuto,
		Tool:    projectToolCheckProjectBuild,
		Message: "Get the build green (commit/rebuild), then promote.",
	}
	return cp
}

func prodRunningReason(st aiv1alpha1.ProjectProviderBindingStatus) string {
	if url := strings.TrimSpace(st.URL); url != "" {
		return "Production is running at " + url + "."
	}
	return ""
}

func repoDisplayName(repo *ProjectRepositoryView) string {
	if repo == nil {
		return ""
	}
	return firstNonEmpty(strings.TrimSpace(repo.Name), strings.TrimSpace(repo.Ref))
}

// getProjectCheckpoints is GET /api/projects/{project}/checkpoints — the portal
// polls it to render the lifecycle chips in the project header.
func (s *Server) getProjectCheckpoints(w http.ResponseWriter, r *http.Request) {
	c, id, p, ok := s.requireProjectWithClient(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, s.projectCheckpoints(r.Context(), c, id, p))
}
