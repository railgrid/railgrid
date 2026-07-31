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
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/mux"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"

	aiv1alpha1 "github.com/faroshq/provider-app-studio/apis/ai/v1alpha1"
	asclient "github.com/faroshq/provider-app-studio/client"
	"github.com/faroshq/provider-app-studio/store"
)

type CreateProjectRequest struct {
	Name                     string `json:"name,omitempty"`
	DisplayName              string `json:"displayName,omitempty"`
	Description              string `json:"description,omitempty"`
	Prompt                   string `json:"prompt,omitempty"`
	TemplateName             string `json:"templateName,omitempty"`
	InferDevelopmentTemplate bool   `json:"inferDevelopmentTemplate,omitempty"`
	ConnectionRef            string `json:"connectionRef,omitempty"`

	// ExistingRepositoryRef imports an existing Code Repository instead of
	// creating one: the project adopts the repository (claims it, never
	// deletes it) and the workspace is hydrated from its default branch
	// after creation.
	ExistingRepositoryRef string `json:"existingRepositoryRef,omitempty"`
}

type PatchProjectRequest struct {
	DisplayName *string                        `json:"displayName,omitempty"`
	Description *string                        `json:"description,omitempty"`
	Sharing     *aiv1alpha1.ProjectSharingSpec `json:"sharing,omitempty"`
}

type PatchProjectMemoryRequest struct {
	Goals        *[]string `json:"goals,omitempty"`
	Requirements *[]string `json:"requirements,omitempty"`
	Constraints  *[]string `json:"constraints,omitempty"`
}

type CreateProjectMessageRequest struct {
	Role             string `json:"role,omitempty"`
	Content          string `json:"content"`
	ClientRequestID  string `json:"clientRequestID,omitempty"`
	AssistantAction  string `json:"assistantAction,omitempty"`
	WorkItemID       string `json:"workItemID,omitempty"`
	WorkItemRevision int64  `json:"workItemRevision,omitempty"`
}

func projectInitialBootstrapPromptDigest(content string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(content)))
	return fmt.Sprintf("%x", sum[:])
}

// consumeProjectInitialBootstrap reserves the server-owned creation permit for
// one client request. The store scopes it to the immutable Project UID and
// binds it to the authenticated actor and original creation prompt.
func (s *Server) consumeProjectInitialBootstrap(ctx context.Context, scope store.Scope, actor, content, clientRequestID string) (bool, error) {
	if s == nil || s.store == nil || strings.TrimSpace(content) == "" {
		return false, nil
	}
	return s.store.ConsumeProjectBootstrapPermit(
		ctx,
		scope,
		strings.TrimSpace(actor),
		projectInitialBootstrapPromptDigest(content),
		strings.TrimSpace(clientRequestID),
		time.Now().UTC(),
	)
}

type projectAssistantAction string

const (
	projectAssistantActionAuto     projectAssistantAction = "auto"
	projectAssistantActionAsk      projectAssistantAction = "ask"
	projectAssistantActionBuild    projectAssistantAction = "build"
	projectAssistantActionContinue projectAssistantAction = "continue"
)

// assistantAction validates the explicit execution intent at the HTTP
// boundary. Omitting it lets the server route clear mutation requests into a
// WorkItem while ordinary conversation remains non-mutating.
func (r CreateProjectMessageRequest) assistantAction() (projectAssistantAction, error) {
	action := projectAssistantAction(strings.ToLower(strings.TrimSpace(r.AssistantAction)))
	if action == "" {
		action = projectAssistantActionAuto
	}
	switch action {
	case projectAssistantActionAuto:
		if strings.TrimSpace(r.WorkItemID) != "" || r.WorkItemRevision != 0 {
			return "", newValidationError("auto does not accept a work item")
		}
	case projectAssistantActionAsk:
		if strings.TrimSpace(r.WorkItemID) != "" || r.WorkItemRevision != 0 {
			return "", newValidationError("ask does not accept a work item")
		}
	case projectAssistantActionBuild:
		if strings.TrimSpace(r.WorkItemID) != "" || r.WorkItemRevision != 0 {
			return "", newValidationError("build creates a new work item")
		}
	case projectAssistantActionContinue:
		if strings.TrimSpace(r.WorkItemID) == "" || r.WorkItemRevision <= 0 {
			return "", newValidationError("continue requires workItemID and workItemRevision")
		}
	default:
		return "", newValidationError("assistantAction must be auto, ask, build, or continue")
	}
	return action, nil
}

type ProjectView struct {
	Name         string                        `json:"name"`
	DisplayName  string                        `json:"displayName"`
	Description  string                        `json:"description,omitempty"`
	Phase        string                        `json:"phase,omitempty"`
	Template     string                        `json:"template,omitempty"`
	Repository   *ProjectRepositoryView        `json:"repository,omitempty"`
	Memory       aiv1alpha1.ProjectMemory      `json:"memory,omitempty"`
	Sharing      aiv1alpha1.ProjectSharingSpec `json:"sharing,omitempty"`
	Environments []ProjectEnvironmentView      `json:"environments,omitempty"`
	CreatedAt    time.Time                     `json:"createdAt"`
	UpdatedAt    *time.Time                    `json:"updatedAt,omitempty"`
}

type ProjectEnvironmentView struct {
	Name     string                       `json:"name"`
	Mode     string                       `json:"mode,omitempty"`
	Phase    string                       `json:"phase,omitempty"`
	Bindings []ProjectProviderBindingView `json:"bindings,omitempty"`
}

type ProjectProviderBindingView struct {
	Name       string            `json:"name"`
	Provider   string            `json:"provider,omitempty"`
	Phase      string            `json:"phase,omitempty"`
	URL        string            `json:"url,omitempty"`
	PreviewURL string            `json:"previewURL,omitempty"`
	Outputs    map[string]string `json:"outputs,omitempty"`
}

type ProjectRepositoryView struct {
	Ref           string                        `json:"ref"`
	Name          string                        `json:"name,omitempty"`
	ConnectionRef string                        `json:"connectionRef,omitempty"`
	HTMLURL       string                        `json:"htmlURL,omitempty"`
	Status        string                        `json:"status,omitempty"`
	Message       string                        `json:"message,omitempty"`
	Ready         bool                          `json:"ready,omitempty"`
	Commits       []ProjectRepositoryCommitView `json:"commits,omitempty"`
}

type ProjectRepositoryCommitView struct {
	Name        string     `json:"name"`
	Phase       string     `json:"phase,omitempty"`
	Branch      string     `json:"branch,omitempty"`
	CommitSHA   string     `json:"commitSHA,omitempty"`
	CommitURL   string     `json:"commitURL,omitempty"`
	Message     string     `json:"message,omitempty"`
	FileCount   int64      `json:"fileCount,omitempty"`
	CreatedAt   time.Time  `json:"createdAt"`
	CompletedAt *time.Time `json:"completedAt,omitempty"`
}

type ProjectMessagesResponse struct {
	Items      []aiv1alpha1.ProjectMessage `json:"items"`
	NextCursor string                      `json:"nextCursor,omitempty"`
}

type projectToolCallStreamEvent struct {
	ID         string                      `json:"id"`
	Name       string                      `json:"name,omitempty"`
	Status     string                      `json:"status"`
	Arguments  string                      `json:"arguments,omitempty"`
	Summary    string                      `json:"summary,omitempty"`
	Error      string                      `json:"error,omitempty"`
	Permission *projectAssistantPermission `json:"permission,omitempty"`
	FollowUp   *projectAssistantFollowUp   `json:"followUp,omitempty"`
	Checkpoint *projectAssistantCheckpoint `json:"checkpoint,omitempty"`
	Mutation   *projectAssistantMutation   `json:"mutation,omitempty"`
	Sequence   int                         `json:"sequence,omitempty"`
}

type projectAssistantMutation struct {
	Path           string `json:"path,omitempty"`
	Additions      int    `json:"additions,omitempty"`
	Deletions      int    `json:"deletions,omitempty"`
	Replacements   int    `json:"replacements,omitempty"`
	Patch          string `json:"patch,omitempty"`
	PatchTruncated bool   `json:"patchTruncated,omitempty"`
}

const projectAPIInitializingMessage = "App Studio is still initializing for this workspace. Try again shortly."
const projectMessageMetadataStatus = "status"
const projectMessageMetadataAssistantActionFeed = "assistantActionFeed"
const projectMessageMetadataAssistantInterrupt = "assistantInterrupt"
const projectMessageStatusInterrupted = "interrupted"
const projectMessageStatusPendingPermission = "pending_permission"
const projectMessageStatusPendingInput = "pending_input"
const projectMessagePersistTimeout = 5 * time.Second

var errProjectAssistantMessageNotFound = errors.New("project assistant message not found")

type projectCreationStatusFunc func(string) error
type projectCreatePreflightGenerator func(context.Context, *asclient.Client, string, []projectDevelopmentTemplateView) (projectCreatePreflight, error)

func writeProjectError(w http.ResponseWriter, err error) {
	if isProjectAPIInitializingError(err) {
		w.Header().Set("Retry-After", "2")
		writeStatus(w, http.StatusServiceUnavailable, "ServiceUnavailable", projectAPIInitializingMessage)
		return
	}
	writeError(w, err)
}

func (s *Server) getProjectCreateReadiness(w http.ResponseWriter, r *http.Request) {
	c, _, ok := s.requireProjectClient(w, r)
	if !ok {
		return
	}
	readiness, err := projectCreateReadiness(r.Context(), c)
	if err != nil {
		writeProjectError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, readiness)
}

func isProjectAPIInitializingError(err error) bool {
	if err == nil {
		return false
	}
	low := strings.ToLower(err.Error())
	// kcp dynamic path: the Project API isn't served in the workspace yet.
	if apierrors.IsNotFound(err) && strings.Contains(low, "server could not find the requested resource") {
		return true
	}
	// GraphQL path: either the gateway has no schema for the workspace cluster
	// yet, or the schema lacks the Project type (APIBinding not established) —
	// both surface while the workspace is still being provisioned.
	return strings.Contains(low, "workspace initializing") ||
		strings.Contains(low, "cannot query field") ||
		strings.Contains(low, "unknown field")
}

func (s *Server) listProjects(w http.ResponseWriter, r *http.Request) {
	c, id, ok := s.requireProjectClient(w, r)
	if !ok {
		return
	}
	list, err := c.Projects().List(r.Context(), metav1.ListOptions{})
	if err != nil {
		writeProjectError(w, err)
		return
	}
	sort.Slice(list.Items, func(i, j int) bool {
		return projectUpdatedAt(&list.Items[i]).After(projectUpdatedAt(&list.Items[j]))
	})
	out := make([]ProjectView, 0, len(list.Items))
	for i := range list.Items {
		out = append(out, projectView(r.Context(), c, &list.Items[i], id))
	}
	writeJSON(w, http.StatusOK, ListResponse[ProjectView]{Items: out})
}

func (s *Server) createProject(w http.ResponseWriter, r *http.Request) {
	c, id, ok := s.requireProjectClient(w, r)
	if !ok {
		return
	}
	var req CreateProjectRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	created, err := s.createProjectFromRequest(r.Context(), c, id, req, nil, r)
	if err != nil {
		writeProjectError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, projectView(r.Context(), c, created, id))
}

func (s *Server) createProjectFromRequest(ctx context.Context, c *asclient.Client, id identity, req CreateProjectRequest, onStatus projectCreationStatusFunc, httpReq *http.Request) (*aiv1alpha1.Project, error) {
	return s.createProjectFromRequestWithPreflight(ctx, c, id, req, onStatus, httpReq, nil)
}

func (s *Server) createProjectFromRequestWithPreflight(ctx context.Context, c *asclient.Client, id identity, req CreateProjectRequest, onStatus projectCreationStatusFunc, httpReq *http.Request, preflight *projectCreatePreflight) (*aiv1alpha1.Project, error) {
	req.DisplayName = strings.TrimSpace(req.DisplayName)
	req.Description = strings.TrimSpace(req.Description)
	req.Prompt = strings.TrimSpace(req.Prompt)
	req.TemplateName = strings.TrimSpace(req.TemplateName)
	req.ConnectionRef = strings.TrimSpace(req.ConnectionRef)
	req.ExistingRepositoryRef = strings.TrimSpace(req.ExistingRepositoryRef)
	var selectedTemplate *projectTemplateInfo
	if req.TemplateName != "" {
		info, err := resolveProjectCreateTemplate(ctx, c, req.TemplateName, false)
		if err != nil {
			return nil, err
		}
		selectedTemplate = info
	}
	// Repository import resolves the repository up front: the claim guard
	// runs before anything is created, and the display name can default to
	// the repository's HOST name (spec.name) — "import my repo" shouldn't
	// demand a title.
	var adoptedPlan *projectRepositoryPlan
	if req.ExistingRepositoryRef != "" {
		plan, err := adoptProjectRepository(ctx, c, req.ExistingRepositoryRef)
		if err != nil {
			return nil, err
		}
		adoptedPlan = &plan
		if req.DisplayName == "" && req.Prompt == "" {
			req.DisplayName = plan.Name
		}
	}
	repoBase := slugifyProjectName(req.DisplayName)
	if preflight != nil {
		req.DisplayName = preflight.Naming.DisplayName
		repoBase = preflight.Naming.RepositoryName
	} else if req.Prompt != "" {
		if err := emitProjectCreationStatus(onStatus, "Planning project"); err != nil {
			return nil, err
		}
		var templates []projectDevelopmentTemplateView
		if selectedTemplate == nil && req.InferDevelopmentTemplate {
			var err error
			templates, err = listDevelopmentTemplateViews(ctx, c)
			if err != nil {
				// The request explicitly authorized eager provisioning. Do
				// not silently degrade that contract when the tenant catalog
				// is unavailable or the caller cannot read it.
				return nil, err
			}
		}
		generatePreflight := s.generateProjectCreatePreflight
		if s.projectCreatePreflight != nil {
			generatePreflight = s.projectCreatePreflight
		}
		generated, err := generatePreflight(ctx, c, req.Prompt, templates)
		if err != nil {
			return nil, err
		}
		preflight = &generated
		req.DisplayName = generated.Naming.DisplayName
		repoBase = generated.Naming.RepositoryName
	}
	if req.DisplayName == "" {
		return nil, newValidationError("displayName is required")
	}
	if selectedTemplate == nil && req.InferDevelopmentTemplate && preflight != nil && strings.TrimSpace(preflight.TemplateName) != "" {
		info, err := resolveProjectCreateTemplate(ctx, c, preflight.TemplateName, true)
		if err != nil {
			return nil, err
		}
		selectedTemplate = info
	}
	if err := emitProjectCreationStatus(onStatus, "Preparing project"); err != nil {
		return nil, err
	}
	name, err := projectName(ctx, c, req.Name, req.DisplayName)
	if err != nil {
		return nil, err
	}
	if err := emitProjectCreationStatus(onStatus, "Configuring repository"); err != nil {
		return nil, err
	}
	var repoPlan projectRepositoryPlan
	if adoptedPlan != nil {
		repoPlan = *adoptedPlan
	} else {
		repoPlan, err = s.prepareProjectRepository(ctx, c, req.ConnectionRef, repoBase, req.DisplayName, req.Description)
		if err != nil {
			return nil, err
		}
	}
	now := metav1.Now()
	p := &aiv1alpha1.Project{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Spec: defaultProjectSpec(name, req.DisplayName, req.Description, repoPlan.projectBinding()),
		Status: aiv1alpha1.ProjectStatus{
			Phase:     aiv1alpha1.ProjectPhaseReady,
			UpdatedAt: &now,
		},
	}
	if selectedTemplate != nil {
		if err := applyProjectDevelopmentTemplate(p, *selectedTemplate); err != nil {
			return nil, err
		}
	}
	if err := emitProjectCreationStatus(onStatus, "Creating project"); err != nil {
		return nil, err
	}
	created, err := c.Projects().Create(ctx, p, metav1.CreateOptions{})
	if err != nil {
		return nil, err
	}
	if req.Prompt != "" {
		if s.store == nil {
			s.cleanupCreatedProjectSetup(ctx, c, id, created)
			return nil, fmt.Errorf("project message store not configured")
		}
		if err := s.store.CreateProjectBootstrapPermit(ctx, projectMessageScope(id.orgUUID, id.workspaceUUID, created), id.user, projectInitialBootstrapPromptDigest(req.Prompt)); err != nil {
			s.cleanupCreatedProjectSetup(ctx, c, id, created)
			return nil, err
		}
	}
	if repoPlan.Adopted {
		if err := emitProjectCreationStatus(onStatus, "Importing repository"); err != nil {
			s.cleanupCreatedProjectSetup(ctx, c, id, created)
			return nil, err
		}
		if err := claimProjectRepository(ctx, c, created.Name, repoPlan); err != nil {
			s.cleanupCreatedProjectSetup(ctx, c, id, created)
			return nil, err
		}
	} else {
		if err := emitProjectCreationStatus(onStatus, "Creating repository"); err != nil {
			s.cleanupCreatedProjectSetup(ctx, c, id, created)
			return nil, err
		}
		if err := s.createProjectRepository(ctx, c, created.Name, repoPlan); err != nil {
			s.cleanupCreatedProjectSetup(ctx, c, id, created)
			return nil, err
		}
	}
	created, err = s.reconcileProjectLiveBindings(ctx, c, created, id)
	if err != nil {
		s.cleanupCreatedProjectSetup(ctx, c, id, created)
		return nil, err
	}
	updated, err := touchProjectStatus(ctx, c, created)
	if err != nil {
		s.cleanupCreatedProjectSetup(ctx, c, id, created)
		return nil, err
	}
	if repoPlan.Adopted && httpReq != nil {
		// Best-effort: pull the imported repository's tree into the fresh
		// workspace. A failure leaves a valid (empty) project the user can
		// hydrate again via /hydrate-workspace or the assistant.
		if err := emitProjectCreationStatus(onStatus, "Importing repository files"); err != nil {
			return updated, nil
		}
		if _, err := s.hydrateWorkspaceFromRepository(ctx, id, updated, httpReq, ""); err != nil {
			klog.V(1).Infof("repository import hydrate failed for project %s: %v", updated.Name, err)
			_ = emitProjectCreationStatus(onStatus, "Repository import incomplete — retry from project settings")
		}
	}
	return updated, nil
}

func resolveProjectCreateTemplate(ctx context.Context, c *asclient.Client, name string, inferred bool) (*projectTemplateInfo, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, nil
	}
	info, err := fetchProjectTemplate(ctx, c, name)
	if err != nil {
		if apierrors.IsNotFound(err) {
			if inferred {
				// The model chose from a catalog snapshot that raced deletion.
				// Fall back unbound so the full assistant can clarify.
				klog.V(1).Infof("inferred development template %q disappeared before project creation; creating project without a template", name)
				return nil, nil
			}
			return nil, newValidationError(fmt.Sprintf("development template %q was not found", name))
		}
		// Authentication, authorization, transport, and API availability
		// failures are operational errors, not evidence that an inferred
		// choice is invalid. Surface them instead of silently losing eager
		// provisioning.
		return nil, err
	}
	if err := validateProjectDevelopmentTemplate(info); err != nil {
		if inferred {
			// A malformed or no-longer-development-capable catalog entry is
			// not safe to provision. Preserve the unbound fallback.
			klog.V(1).Infof("inferred development template %q is not development-capable; creating project without a template: %v", name, err)
			return nil, nil
		}
		return nil, newValidationError(fmt.Sprintf("development template %q cannot back a development environment: %v", name, err))
	}
	return &info, nil
}

func emitProjectCreationStatus(onStatus projectCreationStatusFunc, status string) error {
	if onStatus == nil {
		return nil
	}
	return onStatus(status)
}

func (s *Server) cleanupCreatedProjectSetup(ctx context.Context, c *asclient.Client, id identity, p *aiv1alpha1.Project) {
	if c == nil || p == nil {
		return
	}
	if s.store != nil && strings.TrimSpace(string(p.UID)) != "" {
		_ = s.store.DeleteProjectMessages(ctx, projectMessageScope(id.orgUUID, id.workspaceUUID, p))
	}
	_ = s.deleteProjectProviderResources(ctx, c, p, id)
	if p.Spec.Repository != nil {
		if ref := strings.TrimSpace(p.Spec.Repository.RepositoryRef); ref != "" {
			// Only touch a repository THIS project owns (its claim label
			// matches). A failed adopt leaves the claim unset or on another
			// project — in either case the repository is not ours to delete
			// or release. An ADOPTED repository is never deleted; only
			// repositories App Studio created for this project are torn
			// down with it.
			if repo, err := c.Resource(codeRepositoryResource, "").Get(ctx, ref, metav1.GetOptions{}); err == nil &&
				strings.TrimSpace(repo.GetLabels()[projectRepositoryProjectLabel]) == strings.TrimSpace(p.Name) {
				if repositoryAdopted(repo) {
					_ = releaseProjectRepository(ctx, c, ref)
				} else {
					_ = c.Resource(codeRepositoryResource, "").Delete(ctx, ref, metav1.DeleteOptions{})
				}
			}
		}
	}
	if name := strings.TrimSpace(p.Name); name != "" {
		_ = c.Projects().Delete(ctx, name, metav1.DeleteOptions{})
	}
}

func (s *Server) getProject(w http.ResponseWriter, r *http.Request) {
	c, id, p, ok := s.requireProjectWithClient(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, projectView(r.Context(), c, p, id))
}

func (s *Server) patchProject(w http.ResponseWriter, r *http.Request) {
	c, id, p, ok := s.requireProjectWithClient(w, r)
	if !ok {
		return
	}
	var req PatchProjectRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	changed, err := applyProjectPatchRequest(p, req)
	if err != nil {
		writeProjectError(w, err)
		return
	}
	if !changed {
		writeProjectError(w, newValidationError("PATCH body must set displayName, description, or sharing"))
		return
	}
	updated, err := c.Projects().Update(r.Context(), p, metav1.UpdateOptions{})
	if err != nil {
		writeProjectError(w, err)
		return
	}
	updated, err = touchProjectStatus(r.Context(), c, updated)
	if err != nil {
		writeProjectError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, projectView(r.Context(), c, updated, id))
}

func applyProjectPatchRequest(p *aiv1alpha1.Project, req PatchProjectRequest) (bool, error) {
	changed := false
	if req.DisplayName != nil {
		displayName := strings.TrimSpace(*req.DisplayName)
		if displayName == "" {
			return false, newValidationError("displayName cannot be empty")
		}
		p.Spec.DisplayName = displayName
		changed = true
	}
	if req.Description != nil {
		p.Spec.Description = strings.TrimSpace(*req.Description)
		changed = true
	}
	if req.Sharing != nil {
		sharing, err := normalizeProjectSharingSpec(*req.Sharing)
		if err != nil {
			return false, err
		}
		p.Spec.Sharing = sharing
		changed = true
	}
	return changed, nil
}

func normalizeProjectSharingSpec(sharing aiv1alpha1.ProjectSharingSpec) (aiv1alpha1.ProjectSharingSpec, error) {
	if sharing.Preview.Mode == "" {
		sharing.Preview.Mode = aiv1alpha1.ProjectSharingModePrivate
	}
	if sharing.Publishing.Mode == "" {
		sharing.Publishing.Mode = aiv1alpha1.ProjectSharingModePrivate
	}
	if !validProjectSharingMode(sharing.Preview.Mode) {
		return aiv1alpha1.ProjectSharingSpec{}, newValidationError("sharing.preview.mode must be private, shared, or public")
	}
	if !validProjectSharingMode(sharing.Publishing.Mode) {
		return aiv1alpha1.ProjectSharingSpec{}, newValidationError("sharing.publishing.mode must be private, shared, or public")
	}
	return sharing, nil
}

func effectiveProjectSharingSpec(sharing aiv1alpha1.ProjectSharingSpec) aiv1alpha1.ProjectSharingSpec {
	normalized, err := normalizeProjectSharingSpec(sharing)
	if err != nil {
		return privateProjectSharingSpec()
	}
	return normalized
}

func validProjectSharingMode(mode aiv1alpha1.ProjectSharingMode) bool {
	switch mode {
	case aiv1alpha1.ProjectSharingModePrivate, aiv1alpha1.ProjectSharingModeShared, aiv1alpha1.ProjectSharingModePublic:
		return true
	default:
		return false
	}
}

func (s *Server) deleteProject(w http.ResponseWriter, r *http.Request) {
	c, id, ok := s.requireProjectClient(w, r)
	if !ok {
		return
	}
	name := mux.Vars(r)["project"]
	p, err := c.Projects().Get(r.Context(), name, metav1.GetOptions{})
	if err != nil {
		writeProjectError(w, err)
		return
	}
	if err := s.deleteProjectProviderResources(r.Context(), c, p, id); err != nil {
		writeProjectError(w, err)
		return
	}
	// Repositories deliberately SURVIVE project deletion — git is the durable
	// source of truth, and deleting a workspace UI concept must never destroy
	// the user's code. Deletion only releases the claim on a repository this
	// project owns, so the repository becomes importable again.
	if p.Spec.Repository != nil {
		if ref := strings.TrimSpace(p.Spec.Repository.RepositoryRef); ref != "" {
			if repo, err := c.Resource(codeRepositoryResource, "").Get(r.Context(), ref, metav1.GetOptions{}); err == nil &&
				strings.TrimSpace(repo.GetLabels()[projectRepositoryProjectLabel]) == strings.TrimSpace(p.Name) {
				if err := releaseProjectRepository(r.Context(), c, ref); err != nil {
					klog.FromContext(r.Context()).Error(err, "release project repository claim", "project", name, "repository", ref)
				}
			}
		}
	}
	if err := c.Projects().Delete(r.Context(), name, metav1.DeleteOptions{}); err != nil {
		writeProjectError(w, err)
		return
	}
	if s.store != nil {
		cleanupCtx, cancelCleanup := detachedProjectPersistenceContext(r.Context())
		if err := s.store.DeleteProjectMessages(cleanupCtx, projectMessageScope(id.orgUUID, id.workspaceUUID, p)); err != nil {
			klog.FromContext(r.Context()).Error(err, "delete project messages", "project", name)
		}
		cancelCleanup()
	}
	if s.workspaces != nil {
		cleanupCtx, cancelCleanup := detachedProjectPersistenceContext(r.Context())
		if err := s.workspaces.DeleteSnapshots(cleanupCtx, projectWorkspaceScope(id, p.Name)); err != nil {
			klog.FromContext(r.Context()).Error(err, "delete project workspace snapshots", "project", name)
		}
		cancelCleanup()
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) listProjectMessages(w http.ResponseWriter, r *http.Request) {
	_, id, p, ok := s.requireProjectWithClient(w, r)
	if !ok {
		return
	}
	msgStore, ok := s.requireStore(w)
	if !ok {
		return
	}
	limit := listLimitFromRequest(r)
	cursor := strings.TrimSpace(r.URL.Query().Get("cursor"))
	page, err := msgStore.ListMessages(r.Context(), projectMessageScope(id.orgUUID, id.workspaceUUID, p), limit, cursor)
	if err != nil {
		writeProjectError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, ProjectMessagesResponse{
		Items:      projectMessagesToAPI(page.Items),
		NextCursor: page.NextCursor,
	})
}

func (s *Server) resumeProjectAssistant(w http.ResponseWriter, r *http.Request) {
	c, id, p, ok := s.requireProjectWithClient(w, r)
	if !ok {
		return
	}
	var req projectAssistantResumeRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	scope := projectMessageScope(id.orgUUID, id.workspaceUUID, p)
	runID := mux.Vars(r)["run"]
	run, err := s.store.GetAssistantRun(r.Context(), scope, runID)
	if err != nil {
		writeProjectError(w, err)
		return
	}
	if run.Status != store.AssistantRunStatusPendingPermission && run.Status != store.AssistantRunStatusPendingInput {
		writeProjectError(w, newValidationError("assistant run is not waiting for input"))
		return
	}
	if err := s.authorizeProjectAssistantRunActor(r.Context(), scope, run, id.user, true); err != nil {
		writeStatus(w, http.StatusNotFound, "NotFound", "assistant run not found")
		return
	}
	if strings.TrimSpace(run.ActiveMessageID) == "" {
		writeStatus(w, http.StatusNotFound, "NotFound", "assistant run not found")
		return
	}
	if _, _, err := preflightProjectAssistantResume(run, req); err != nil {
		writeProjectError(w, err)
		return
	}
	message, err := s.findProjectMessage(r.Context(), scope, run.ActiveMessageID)
	if err != nil {
		writeProjectError(w, err)
		return
	}
	supervisor := s.projectAssistantSupervisor()
	if err := supervisor.Start(r.Context(), scope, run, message, func(ctx context.Context, accumulator *projectAssistantSnapshotAccumulator) {
		transitionTerminal := func(status store.AssistantRunStatus, reason string, cause error) {
			var transitionErr error
			if run.WorkItemID == "" {
				transitionErr = accumulator.SetStatus(context.Background(), status)
			} else {
				itemStatus := store.AssistantWorkItemStatusSuspended
				if status == store.AssistantRunStatusCompleted {
					itemStatus = store.AssistantWorkItemStatusCompleted
				}
				transitionErr = accumulator.TransitionWorkItemTerminal(context.Background(), status, itemStatus, reason, func(committed *store.AssistantRun, current *store.Message) {
					current.Metadata = projectAssistantDurableMetadataFromExisting(*committed, projectAssistantRunDisplayStatus(status, "Working"), false, current.Metadata)
				})
			}
			committed := run
			if current, ok := accumulator.CommittedRun(); ok {
				committed = current
			}
			if cause != nil {
				logProjectAssistantFailure(ctx, "resume_failed", scope, committed, cause)
			}
			if transitionErr != nil {
				logProjectAssistantFailure(ctx, "resume_terminal_transition_failed", scope, committed, transitionErr)
			}
		}
		resp, resumeErr := s.resumeProjectAssistantRunWithRepositoryAndClient(ctx, r.Clone(ctx), id, c, p, projectRepositoryView(ctx, c, p), runID, req)
		if resumeErr == nil && resp.Status == store.AssistantRunStatusCompleted {
			transitionTerminal(store.AssistantRunStatusCompleted, string(store.AssistantRunStatusCompleted), nil)
			return
		}
		if resumeErr == nil && resp.Status == store.AssistantRunStatusInterrupted {
			reason := strings.TrimSpace(resp.SuspensionReason)
			if reason == "" {
				reason = "objective incomplete"
			}
			transitionTerminal(store.AssistantRunStatusInterrupted, reason, nil)
			return
		}
		if errors.Is(resumeErr, context.Canceled) || errors.Is(context.Cause(ctx), context.Canceled) {
			transitionTerminal(store.AssistantRunStatusAborted, "aborted", resumeErr)
			return
		}
		if resumeErr != nil {
			transitionTerminal(store.AssistantRunStatusFailed, projectAssistantWorkItemFailureReason(resumeErr), resumeErr)
			return
		}
		_ = accumulator.SetStatus(context.Background(), resp.Status)
	}); err != nil {
		writeProjectError(w, err)
		return
	}
	s.projectAssistantSupervisor().log("resume", scope, run)
	writeJSON(w, http.StatusAccepted, projectAssistantRunSnapshotToAPI(projectAssistantRunSnapshot{Run: run, Message: message}))
}

func (s *Server) stopProjectAssistant(w http.ResponseWriter, r *http.Request) {
	_, id, p, ok := s.requireProjectWithClient(w, r)
	if !ok {
		return
	}
	scope := projectMessageScope(id.orgUUID, id.workspaceUUID, p)
	runID := mux.Vars(r)["run"]
	var request struct {
		ClientRequestID string `json:"clientRequestID"`
	}
	if !decodeJSON(w, r, &request) {
		return
	}
	request.ClientRequestID = strings.TrimSpace(request.ClientRequestID)
	if request.ClientRequestID == "" {
		writeProjectError(w, newValidationError("clientRequestID is required"))
		return
	}
	existingRun, err := s.store.GetAssistantRun(r.Context(), scope, runID)
	if err != nil || s.authorizeProjectAssistantRunActor(r.Context(), scope, existingRun, id.user, false) != nil {
		writeStatus(w, http.StatusNotFound, "NotFound", "assistant run not found")
		return
	}
	if existingRun.Status == store.AssistantRunStatusPendingPermission || existingRun.Status == store.AssistantRunStatusPendingInput {
		if err := s.reattachProjectAssistantPendingRun(r.Context(), scope, existingRun); err != nil {
			writeProjectError(w, err)
			return
		}
	}
	if found, bindErr := s.projectAssistantSupervisor().BindStopRequest(r.Context(), scope, runID, id.user, request.ClientRequestID); found {
		if bindErr != nil {
			writeProjectError(w, bindErr)
			return
		}
	} else {
		if err := bindProjectAssistantStopRequest(&existingRun, id.user, request.ClientRequestID); err != nil {
			writeProjectError(w, err)
			return
		}
		existingRun.UpdatedAt = time.Now().UTC()
		if err := s.store.SaveAssistantRun(r.Context(), scope, existingRun); err != nil {
			writeProjectError(w, err)
			return
		}
	}
	run, found, err := s.projectAssistantSupervisor().Stop(scope, runID)
	if err != nil {
		writeProjectError(w, err)
		return
	}
	if !found {
		run, err = s.store.GetAssistantRun(r.Context(), scope, runID)
		if err != nil {
			writeProjectError(w, err)
			return
		}
		if !assistantRunTerminal(run.Status) {
			writeStatus(w, http.StatusConflict, "Conflict", "assistant run is not active on this provider")
			return
		}
	}
	writeJSON(w, http.StatusAccepted, projectAssistantResumeResponse{
		RunID:     run.ID,
		RequestID: run.RequestID,
		Status:    run.Status,
	})
}

// reattachProjectAssistantPendingRun restores the in-memory lifecycle owner for
// a durable checkpoint after a provider restart. Stop can then use the same
// atomic run, WorkItem, and assistant-message transition as an in-process run.
func (s *Server) reattachProjectAssistantPendingRun(ctx context.Context, scope store.Scope, run store.AssistantRun) error {
	if run.Status != store.AssistantRunStatusPendingPermission && run.Status != store.AssistantRunStatusPendingInput {
		return newValidationError("assistant run is not waiting for input")
	}
	if strings.TrimSpace(run.ActiveMessageID) == "" {
		return store.ErrAssistantRunNotFound
	}
	message, err := s.findProjectMessage(ctx, scope, run.ActiveMessageID)
	if err != nil {
		return err
	}
	_, err = s.projectAssistantSupervisor().Attach(scope, run, message)
	return err
}

func (s *Server) authorizeProjectAssistantRunActor(ctx context.Context, scope store.Scope, run store.AssistantRun, actor string, requireActiveGrant bool) error {
	origin, err := s.findProjectMessage(ctx, scope, run.UserMessageID)
	if err != nil || origin.ActorID != actor {
		return store.ErrAssistantRunNotFound
	}
	if run.WorkItemID == "" {
		return nil
	}
	item, err := s.store.GetAssistantWorkItem(ctx, scope, run.WorkItemID)
	if err != nil || item.CreatedBy != actor {
		return store.ErrAssistantWorkItemNotFound
	}
	if requireActiveGrant && (item.ActiveRunID != run.ID || item.Status != store.AssistantWorkItemStatusActive || item.GrantRevision != run.ExpectedGrantRevision) {
		return store.ErrAssistantWorkItemConflict
	}
	return nil
}

func (s *Server) listProjectAssistantWorkItems(w http.ResponseWriter, r *http.Request) {
	_, id, p, ok := s.requireProjectWithClient(w, r)
	if !ok {
		return
	}
	items, err := s.store.ListAssistantWorkItems(r.Context(), projectMessageScope(id.orgUUID, id.workspaceUUID, p))
	if err != nil {
		writeProjectError(w, err)
		return
	}
	owned := make([]projectAssistantWorkItemView, 0, len(items))
	for _, item := range items {
		if item.CreatedBy == id.user {
			owned = append(owned, projectAssistantWorkItemToAPI(item))
		}
	}
	writeJSON(w, http.StatusOK, owned)
}

func (s *Server) getProjectAssistantWorkItem(w http.ResponseWriter, r *http.Request) {
	_, id, p, ok := s.requireProjectWithClient(w, r)
	if !ok {
		return
	}
	item, err := s.store.GetAssistantWorkItem(r.Context(), projectMessageScope(id.orgUUID, id.workspaceUUID, p), mux.Vars(r)["workItem"])
	if errors.Is(err, store.ErrAssistantWorkItemNotFound) || (err == nil && item.CreatedBy != id.user) {
		writeStatus(w, http.StatusNotFound, "NotFound", "assistant work item not found")
		return
	}
	if err != nil {
		writeProjectError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, projectAssistantWorkItemToAPI(item))
}

func (s *Server) cancelProjectAssistantWorkItem(w http.ResponseWriter, r *http.Request) {
	_, id, p, ok := s.requireProjectWithClient(w, r)
	if !ok {
		return
	}
	var request struct {
		Revision        int64  `json:"revision"`
		ClientRequestID string `json:"clientRequestID"`
	}
	if !decodeJSON(w, r, &request) {
		return
	}
	request.ClientRequestID = strings.TrimSpace(request.ClientRequestID)
	if request.ClientRequestID == "" {
		writeProjectError(w, newValidationError("clientRequestID is required"))
		return
	}
	scope := projectMessageScope(id.orgUUID, id.workspaceUUID, p)
	item, err := s.store.GetAssistantWorkItem(r.Context(), scope, mux.Vars(r)["workItem"])
	if errors.Is(err, store.ErrAssistantWorkItemNotFound) || (err == nil && item.CreatedBy != id.user) {
		writeStatus(w, http.StatusNotFound, "NotFound", "assistant work item not found")
		return
	}
	if err != nil {
		writeProjectError(w, err)
		return
	}
	if item.Status == store.AssistantWorkItemStatusCancelled && item.Revision == request.Revision+1 {
		if err := validateProjectAssistantCancelReplay(item, id.user, request.ClientRequestID, request.Revision); err != nil {
			writeProjectError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, projectAssistantWorkItemToAPI(item))
		return
	}
	if request.Revision != item.Revision || item.Status != store.AssistantWorkItemStatusSuspended || item.ActiveRunID != "" {
		writeStatus(w, http.StatusConflict, "Conflict", "assistant work item is not cancellable at that revision")
		return
	}
	item.Status = store.AssistantWorkItemStatusCancelled
	item.StatusReason = "cancelled by user"
	receipt, err := encodeProjectAssistantCancelReceipt(projectAssistantCancelRequestReceipt(id.user, item.ID, request.ClientRequestID, request.Revision))
	if err != nil {
		writeProjectError(w, err)
		return
	}
	item.CancellationReceipt = receipt
	item.PlanGrant = nil
	item.GrantRevision = ""
	item.Revision = request.Revision + 1
	item.UpdatedAt = time.Now().UTC()
	if err := s.store.CompareAndSwapAssistantWorkItem(r.Context(), scope, item, request.Revision); err != nil {
		if errors.Is(err, store.ErrAssistantWorkItemConflict) {
			current, getErr := s.store.GetAssistantWorkItem(r.Context(), scope, item.ID)
			if getErr == nil && current.CreatedBy == id.user && current.Status == store.AssistantWorkItemStatusCancelled && current.Revision == request.Revision+1 &&
				validateProjectAssistantCancelReplay(current, id.user, request.ClientRequestID, request.Revision) == nil {
				writeJSON(w, http.StatusOK, projectAssistantWorkItemToAPI(current))
				return
			}
			writeStatus(w, http.StatusConflict, "Conflict", "assistant work item changed")
			return
		}
		writeProjectError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, projectAssistantWorkItemToAPI(item))
}

// projectAssistantWorkItemView deliberately excludes the encrypted plan grant
// and its internal revision token. Clients select work by the public WorkItem
// revision; grant authorization remains entirely server-side.
type projectAssistantWorkItemView struct {
	ID            string                        `json:"id"`
	RootMessageID string                        `json:"rootMessageID"`
	CreatedBy     string                        `json:"createdBy"`
	Status        store.AssistantWorkItemStatus `json:"status"`
	StatusReason  string                        `json:"statusReason,omitempty"`
	Revision      int64                         `json:"revision"`
	ActiveRunID   string                        `json:"activeRunID,omitempty"`
	CreatedAt     time.Time                     `json:"createdAt"`
	UpdatedAt     time.Time                     `json:"updatedAt"`
}

func projectAssistantWorkItemToAPI(item store.AssistantWorkItem) projectAssistantWorkItemView {
	return projectAssistantWorkItemView{
		ID:            item.ID,
		RootMessageID: item.RootMessageID,
		CreatedBy:     item.CreatedBy,
		Status:        item.Status,
		StatusReason:  item.StatusReason,
		Revision:      item.Revision,
		ActiveRunID:   item.ActiveRunID,
		CreatedAt:     item.CreatedAt,
		UpdatedAt:     item.UpdatedAt,
	}
}

func projectAssistantClearPendingInterruptMetadata(message *store.Message, runID string) {
	if message == nil || strings.TrimSpace(runID) == "" {
		return
	}
	interrupt := projectAssistantUIInterruptFromMetadata(message.Metadata[projectMessageMetadataAssistantInterrupt])
	if interrupt == nil || interrupt.Action == nil || interrupt.Action.RunID != runID {
		return
	}
	metadata := cloneAnyMap(message.Metadata)
	delete(metadata, projectMessageMetadataStatus)
	delete(metadata, projectMessageMetadataAssistantInterrupt)
	message.Metadata = metadata
}

func appendProjectUserMessage(ctx context.Context, msgStore store.Store, scope store.Scope, content string) error {
	now := time.Now().UTC()
	return msgStore.AppendMessage(ctx, scope, store.Message{
		ID:        newMessageID(),
		Role:      aiv1alpha1.ProjectMessageRoleUser,
		Content:   content,
		CreatedAt: now,
		UpdatedAt: now,
	})
}

type projectAssistantStreamStart struct {
	TurnDecision        *projectAssistantTurnDecision
	InitialApprovedPlan *projectAssistantApprovedPlan
}

func ptrProjectAssistantApprovedPlan(plan projectAssistantApprovedPlan) *projectAssistantApprovedPlan {
	return &plan
}

func projectAssistantTurnDecisionForStreamStart(ctx context.Context, router projectAssistantTurnRouter, req projectAssistantTurnRouteRequest, start *projectAssistantStreamStart) (projectAssistantTurnDecision, error) {
	if start != nil && start.TurnDecision != nil {
		return *start.TurnDecision, nil
	}
	return router(ctx, req)
}

func projectAssistantStoredContent(reply, streamed string) string {
	if strings.TrimSpace(reply) != "" {
		return reply
	}
	return streamed
}

func projectAssistantToolCallsRequireDevelopmentSync(toolCalls []projectToolCallStreamEvent) bool {
	for _, toolCall := range toolCalls {
		if toolCall.Status == "succeeded" && shouldSyncDevelopmentAfterTool(toolCall.Name) {
			return true
		}
	}
	return false
}

func projectAssistantGenerationFailureMessage(err error) string {
	detail := "assistant generation failed"
	if err != nil {
		detail = strings.TrimSpace(err.Error())
	}
	if projectAssistantLLMRateLimitError(detail) {
		return "assistant generation failed: The LLM provider is rate limited. Please wait a moment and try again."
	}
	detail = projectAssistantStripEinoTrace(detail)
	if detail == "" {
		detail = "assistant generation failed"
	}
	if strings.HasPrefix(detail, "assistant generation failed") {
		return detail
	}
	return "assistant generation failed: " + detail
}

func projectAssistantLLMRateLimitError(detail string) bool {
	detail = strings.ToLower(detail)
	return strings.Contains(detail, "429") ||
		strings.Contains(detail, "too many requests") ||
		strings.Contains(detail, "resource_exhausted") ||
		strings.Contains(detail, "resource exhausted")
}

func projectAssistantStripEinoTrace(detail string) string {
	detail = strings.TrimSpace(detail)
	if before, _, found := strings.Cut(detail, "------------------------"); found {
		detail = strings.TrimSpace(before)
	}
	detail = strings.TrimPrefix(detail, "[NodeRunError]")
	return strings.TrimSpace(detail)
}

func shouldPersistInterruptedProjectAssistant(ctx context.Context, err, streamErr error, streamed string) bool {
	return strings.TrimSpace(streamed) != "" && (streamErr != nil || errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled))
}

func detachedProjectPersistenceContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), projectMessagePersistTimeout)
}

func appendInterruptedProjectAssistantMessage(ctx context.Context, msgStore store.Store, scope store.Scope, id, content string) error {
	if strings.TrimSpace(content) == "" {
		return nil
	}
	persistCtx, cancel := detachedProjectPersistenceContext(ctx)
	defer cancel()
	return appendProjectAssistantMessage(persistCtx, msgStore, scope, id, content, projectAssistantMessageMetadata(projectMessageStatusInterrupted, nil))
}

func appendProjectAssistantMessage(ctx context.Context, msgStore store.Store, scope store.Scope, id, content string, metadata map[string]any) error {
	now := time.Now().UTC()
	return msgStore.AppendMessage(ctx, scope, store.Message{
		ID:        id,
		Role:      aiv1alpha1.ProjectMessageRoleAssistant,
		Content:   content,
		Metadata:  cloneAnyMap(metadata),
		CreatedAt: now,
		UpdatedAt: now,
	})
}

func (s *Server) updateProjectAssistantPermissionMessage(
	ctx context.Context,
	scope store.Scope,
	assistantMessageID string,
	response projectAssistantResumeResponse,
) error {
	if s == nil || s.store == nil || assistantMessageID == "" {
		return nil
	}
	msg, err := s.findProjectMessage(ctx, scope, assistantMessageID)
	if err != nil {
		if errors.Is(err, errProjectAssistantMessageNotFound) {
			return nil
		}
		return err
	}
	if msg.Role != aiv1alpha1.ProjectMessageRoleAssistant {
		return nil
	}
	metadata := cloneAnyMap(msg.Metadata)
	actions := projectAssistantActionFeedFromMetadata(metadata[projectMessageMetadataAssistantActionFeed])
	interrupt := projectAssistantUIInterruptFromMetadata(metadata[projectMessageMetadataAssistantInterrupt])
	if !projectAssistantPermissionMessageMatchesResume(metadata, interrupt, response) {
		return nil
	}
	if response.ToolCall != nil {
		actions = applyProjectAssistantActionFeedUpdate(actions, projectAssistantActionFeedItemFromToolCall(*response.ToolCall))
	}
	if response.Permission != nil {
		actions = applyProjectAssistantActionFeedUpdate(actions, projectAssistantActionFeedItemFromPermission(*response.Permission))
	}
	if response.FollowUp != nil {
		actions = applyProjectAssistantActionFeedUpdate(actions, projectAssistantActionFeedItemFromFollowUp(*response.FollowUp))
	}
	if response.Checkpoint != nil && response.Permission != nil {
		interrupt = projectAssistantUIInterruptRequestFromPermissionCheckpoint("", *response.Permission, *response.Checkpoint)
	}
	if response.Checkpoint != nil && response.FollowUp != nil {
		interrupt = projectAssistantUIInterruptRequestFromFollowUpCheckpoint("", *response.FollowUp, *response.Checkpoint)
	}
	if response.Status == store.AssistantRunStatusPendingPermission {
		metadata[projectMessageMetadataStatus] = projectMessageStatusPendingPermission
		if interrupt != nil {
			metadata[projectMessageMetadataAssistantInterrupt] = interrupt
		}
	} else if response.Status == store.AssistantRunStatusPendingInput {
		metadata[projectMessageMetadataStatus] = projectMessageStatusPendingInput
		if interrupt != nil {
			metadata[projectMessageMetadataAssistantInterrupt] = interrupt
		}
	} else {
		delete(metadata, projectMessageMetadataStatus)
		delete(metadata, projectMessageMetadataAssistantInterrupt)
	}
	if response.Progress != nil {
		metadata[projectAssistantMetadataProgress] = *response.Progress
	}
	if displayStatus := projectAssistantRunDisplayStatus(response.Status, ""); displayStatus != "" {
		metadata[projectAssistantMetadataWorkingStatus] = displayStatus
	}
	content := msg.Content
	if strings.TrimSpace(response.AssistantContent) != "" {
		content = response.AssistantContent
	}
	if len(actions) > 0 {
		metadata[projectMessageMetadataAssistantActionFeed] = actions
	} else {
		delete(metadata, projectMessageMetadataAssistantActionFeed)
	}
	if accumulator := s.projectAssistantSupervisor().accumulatorForActiveMessage(scope, msg.ID); accumulator != nil {
		return accumulator.UpdateMessage(ctx, content, metadata)
	}
	now := time.Now().UTC()
	return s.store.AppendMessage(ctx, scope, store.Message{
		ID:        msg.ID,
		Role:      msg.Role,
		Content:   content,
		Metadata:  metadata,
		CreatedAt: msg.CreatedAt,
		UpdatedAt: now,
	})
}

func projectAssistantPermissionMessageMatchesResume(metadata map[string]any, interrupt *projectAssistantUIInterruptRequest, response projectAssistantResumeResponse) bool {
	status := metadata[projectMessageMetadataStatus]
	if status != projectMessageStatusPendingPermission && status != projectMessageStatusPendingInput {
		return false
	}
	if response.RunID == "" || interrupt == nil || interrupt.Action == nil {
		return false
	}
	if interrupt.Action.RunID == response.RunID {
		return true
	}
	if response.RequestID != "" && interrupt.Action.RequestID == response.RequestID {
		return true
	}
	return false
}

func (s *Server) findProjectMessage(ctx context.Context, scope store.Scope, id string) (store.Message, error) {
	cursor := ""
	for {
		page, err := s.store.ListMessages(ctx, scope, 250, cursor)
		if err != nil {
			return store.Message{}, err
		}
		for _, msg := range page.Items {
			if msg.ID == id {
				return msg, nil
			}
		}
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}
	return store.Message{}, fmt.Errorf("%w: %q", errProjectAssistantMessageNotFound, id)
}

func projectAssistantMessageMetadata(status string, toolCalls []projectToolCallStreamEvent) map[string]any {
	metadata := map[string]any{}
	if status != "" {
		metadata[projectMessageMetadataStatus] = status
	}
	if actions := projectAssistantActionFeedFromToolCalls(toolCalls); len(actions) > 0 {
		metadata[projectMessageMetadataAssistantActionFeed] = actions
	}
	if interrupt := projectAssistantUIInterruptFromToolCalls(toolCalls); interrupt != nil {
		metadata[projectMessageMetadataAssistantInterrupt] = interrupt
	}
	if len(metadata) == 0 {
		return nil
	}
	return metadata
}

func projectAssistantActionFeedFromToolCalls(events []projectToolCallStreamEvent) []projectAssistantActionFeedItem {
	return filterProjectAssistantActionFeedItems(projectAssistantActionFeedUpdatesFromToolCalls(events))
}

func projectAssistantActionFeedUpdatesFromToolCalls(events []projectToolCallStreamEvent) []projectAssistantActionFeedItem {
	if len(events) == 0 {
		return nil
	}
	actions := make([]projectAssistantActionFeedItem, 0, len(events))
	for _, event := range events {
		if event.ID == "" || event.Status == "" || projectToolBaseName(event.Name) == projectEinoAssistantWriteTodosTool {
			continue
		}
		actions = upsertProjectAssistantActionFeedItem(actions, projectAssistantActionFeedItemFromToolCall(event))
	}
	if len(actions) == 0 {
		return nil
	}
	return actions
}

func projectAssistantUIInterruptFromToolCalls(events []projectToolCallStreamEvent) *projectAssistantUIInterruptRequest {
	for i := len(events) - 1; i >= 0; i-- {
		event := events[i]
		if event.Status == "input_required" && event.FollowUp != nil && event.Checkpoint != nil {
			return projectAssistantUIInterruptRequestFromFollowUpCheckpoint("", *event.FollowUp, *event.Checkpoint)
		}
		if event.Status != "permission_required" || event.Permission == nil || event.Checkpoint == nil {
			continue
		}
		return projectAssistantUIInterruptRequestFromPermissionCheckpoint("", *event.Permission, *event.Checkpoint)
	}
	return nil
}

func projectAssistantActionFeedFromMetadata(raw any) []projectAssistantActionFeedItem {
	if raw == nil {
		return nil
	}
	if typed, ok := raw.([]projectAssistantActionFeedItem); ok {
		return filterProjectAssistantActionFeedItems(typed)
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return nil
	}
	var out []projectAssistantActionFeedItem
	if err := json.Unmarshal(data, &out); err != nil {
		return nil
	}
	return filterProjectAssistantActionFeedItems(out)
}

func filterProjectAssistantActionFeedItems(items []projectAssistantActionFeedItem) []projectAssistantActionFeedItem {
	filtered := make([]projectAssistantActionFeedItem, 0, len(items))
	for _, item := range items {
		if projectAssistantActionFeedItemVisible(item) {
			filtered = append(filtered, item)
		}
	}
	return filtered
}

func projectAssistantActionFeedItemVisible(item projectAssistantActionFeedItem) bool {
	if item.Kind != projectAssistantActionFeedItemOther {
		return true
	}
	return item.Status == projectAssistantActionFeedStatusWaiting ||
		item.Status == projectAssistantActionFeedStatusFailed ||
		item.Status == projectAssistantActionFeedStatusRejected
}

func projectAssistantUIInterruptFromMetadata(raw any) *projectAssistantUIInterruptRequest {
	if raw == nil {
		return nil
	}
	if typed, ok := raw.(*projectAssistantUIInterruptRequest); ok {
		if typed == nil {
			return nil
		}
		copy := *typed
		return &copy
	}
	if typed, ok := raw.(projectAssistantUIInterruptRequest); ok {
		return &typed
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return nil
	}
	var out projectAssistantUIInterruptRequest
	if err := json.Unmarshal(data, &out); err != nil {
		return nil
	}
	if out.InterruptID == "" && out.Action == nil {
		return nil
	}
	return &out
}

func upsertProjectAssistantActionFeedItem(actions []projectAssistantActionFeedItem, action projectAssistantActionFeedItem) []projectAssistantActionFeedItem {
	if action.ID == "" {
		return actions
	}
	for i := range actions {
		if actions[i].ID == action.ID {
			actions[i] = mergeProjectAssistantActionFeedItem(actions[i], action)
			return actions
		}
	}
	return append(actions, action)
}

func applyProjectAssistantActionFeedUpdate(actions []projectAssistantActionFeedItem, action projectAssistantActionFeedItem) []projectAssistantActionFeedItem {
	if projectAssistantActionFeedItemVisible(action) {
		return upsertProjectAssistantActionFeedItem(actions, action)
	}
	filtered := actions[:0]
	for _, existing := range actions {
		if existing.ID != action.ID {
			filtered = append(filtered, existing)
		}
	}
	return filtered
}

func mergeProjectAssistantActionFeedItem(existing, next projectAssistantActionFeedItem) projectAssistantActionFeedItem {
	if next.Kind == "" {
		next.Kind = existing.Kind
	}
	if next.Status == "" {
		next.Status = existing.Status
	}
	if next.Title == "" {
		next.Title = existing.Title
	}
	if next.Target == "" {
		next.Target = existing.Target
	}
	if next.Outcome == "" {
		next.Outcome = existing.Outcome
	}
	if next.Count == 0 {
		next.Count = existing.Count
	}
	if next.Severity == "" {
		next.Severity = existing.Severity
	}
	if next.GroupKey == "" {
		next.GroupKey = existing.GroupKey
	}
	if next.GroupTitle == "" {
		next.GroupTitle = existing.GroupTitle
	}
	if next.Sequence == 0 {
		next.Sequence = existing.Sequence
	}
	if next.Diagnostic == nil {
		next.Diagnostic = existing.Diagnostic
	}
	return next
}

func sanitizeProjectToolCallStreamEventsForMetadata(events []projectToolCallStreamEvent) []projectToolCallStreamEvent {
	if len(events) == 0 {
		return nil
	}
	out := make([]projectToolCallStreamEvent, 0, len(events))
	for _, event := range events {
		if event.Permission != nil {
			permission := *event.Permission
			permission.Input = nil
			event.Permission = &permission
		}
		out = append(out, event)
	}
	return out
}

func projectToolCallStreamEventsFromMetadata(raw any) []projectToolCallStreamEvent {
	if raw == nil {
		return nil
	}
	if typed, ok := raw.([]projectToolCallStreamEvent); ok {
		return append([]projectToolCallStreamEvent(nil), typed...)
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return nil
	}
	var out []projectToolCallStreamEvent
	if err := json.Unmarshal(data, &out); err != nil {
		return nil
	}
	return out
}

func upsertProjectToolCallStreamEvent(events []projectToolCallStreamEvent, event projectToolCallStreamEvent) []projectToolCallStreamEvent {
	for i := range events {
		if events[i].ID == event.ID {
			events[i] = mergeProjectToolCallStreamEvent(events[i], event)
			return events
		}
	}
	return append(events, event)
}

func mergeProjectToolCallStreamEvent(existing, next projectToolCallStreamEvent) projectToolCallStreamEvent {
	if next.Name == "" {
		next.Name = existing.Name
	}
	if next.Arguments == "" {
		next.Arguments = existing.Arguments
	}
	if next.Summary == "" {
		next.Summary = existing.Summary
	}
	if next.Error == "" {
		next.Error = existing.Error
	}
	if next.Permission == nil {
		next.Permission = existing.Permission
	}
	if next.FollowUp == nil {
		next.FollowUp = existing.FollowUp
	}
	if next.Checkpoint == nil {
		next.Checkpoint = existing.Checkpoint
	}
	if next.Sequence == 0 {
		next.Sequence = existing.Sequence
	}
	if next.Mutation == nil {
		next.Mutation = existing.Mutation
	}
	return next
}

func (s *Server) getProjectMemory(w http.ResponseWriter, r *http.Request) {
	p, ok := s.requireProject(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, p.Spec.Memory)
}

func (s *Server) patchProjectMemory(w http.ResponseWriter, r *http.Request) {
	c, _, p, ok := s.requireProjectWithClient(w, r)
	if !ok {
		return
	}
	var req PatchProjectMemoryRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	changed := false
	if req.Goals != nil {
		p.Spec.Memory.Goals = append([]string(nil), (*req.Goals)...)
		changed = true
	}
	if req.Requirements != nil {
		p.Spec.Memory.Requirements = append([]string(nil), (*req.Requirements)...)
		changed = true
	}
	if req.Constraints != nil {
		p.Spec.Memory.Constraints = append([]string(nil), (*req.Constraints)...)
		changed = true
	}
	if !changed {
		writeProjectError(w, newValidationError("PATCH body must set at least one memory field"))
		return
	}
	updated, err := c.Projects().Update(r.Context(), p, metav1.UpdateOptions{})
	if err != nil {
		writeProjectError(w, err)
		return
	}
	updated, err = touchProjectStatus(r.Context(), c, updated)
	if err != nil {
		writeProjectError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, updated.Spec.Memory)
}

func projectName(ctx context.Context, c *asclient.Client, requested, displayName string) (string, error) {
	if requested != "" {
		name := slugifyProjectName(requested)
		if name != requested {
			return "", newValidationError("name must be a valid DNS label")
		}
		return name, nil
	}
	base := slugifyProjectName(displayName)
	if base == "" {
		base = "project"
	}
	if len(base) > 48 {
		base = strings.Trim(base[:48], "-")
	}
	for i := 0; i < 5; i++ {
		name := base
		if i > 0 {
			name = fmt.Sprintf("%s-%s", base, uuid.NewString()[:6])
		}
		if _, err := c.Projects().Get(ctx, name, metav1.GetOptions{}); apierrors.IsNotFound(err) {
			return name, nil
		} else if err != nil {
			return "", err
		}
	}
	return fmt.Sprintf("%s-%s", base, uuid.NewString()[:8]), nil
}

func touchProjectStatus(ctx context.Context, c *asclient.Client, p *aiv1alpha1.Project) (*aiv1alpha1.Project, error) {
	now := metav1.Now()
	data, err := projectStatusTouchPatch(now)
	if err != nil {
		return nil, err
	}
	return c.Projects().Patch(ctx, p.Name, types.MergePatchType, data, metav1.PatchOptions{}, "status")
}

func projectStatusTouchPatch(now metav1.Time) ([]byte, error) {
	patch := struct {
		Status struct {
			Phase     string      `json:"phase"`
			UpdatedAt metav1.Time `json:"updatedAt"`
		} `json:"status"`
	}{}
	patch.Status.Phase = aiv1alpha1.ProjectPhaseReady
	patch.Status.UpdatedAt = now
	return json.Marshal(patch)
}

func projectView(ctx context.Context, c *asclient.Client, p *aiv1alpha1.Project, id identity) ProjectView {
	p = projectWithLiveBindingStatus(ctx, c, p, id)
	view := ProjectView{
		Name:         p.Name,
		DisplayName:  p.Spec.DisplayName,
		Description:  p.Spec.Description,
		Phase:        p.Status.Phase,
		Repository:   projectRepositoryView(ctx, c, p),
		Memory:       p.Spec.Memory,
		Sharing:      effectiveProjectSharingSpec(p.Spec.Sharing),
		Environments: projectEnvironmentViews(p),
		CreatedAt:    p.CreationTimestamp.Time,
	}
	if p.Spec.Template != nil {
		view.Template = strings.TrimSpace(p.Spec.Template.Name)
	}
	if p.Status.UpdatedAt != nil {
		t := p.Status.UpdatedAt.Time
		view.UpdatedAt = &t
	}
	return view
}

func projectEnvironmentViews(p *aiv1alpha1.Project) []ProjectEnvironmentView {
	statusByName := map[string]aiv1alpha1.ProjectEnvironmentStatus{}
	for _, st := range p.Status.Environments {
		statusByName[st.Name] = st
	}
	views := make([]ProjectEnvironmentView, 0, len(p.Spec.Environments))
	for _, spec := range p.Spec.Environments {
		st := statusByName[spec.Name]
		mode := string(spec.Mode)
		if mode == "" && st.Mode != "" {
			mode = string(st.Mode)
		}
		if mode == "" {
			mode = string(aiv1alpha1.ProjectEnvironmentModeArtifact)
		}
		view := ProjectEnvironmentView{
			Name:     spec.Name,
			Mode:     mode,
			Phase:    st.Phase,
			Bindings: projectProviderBindingViews(spec.Bindings, st.Bindings),
		}
		views = append(views, view)
		delete(statusByName, spec.Name)
	}
	for _, st := range statusByName {
		mode := string(st.Mode)
		if mode == "" {
			mode = string(aiv1alpha1.ProjectEnvironmentModeArtifact)
		}
		views = append(views, ProjectEnvironmentView{
			Name:     st.Name,
			Mode:     mode,
			Phase:    st.Phase,
			Bindings: projectProviderBindingViews(nil, st.Bindings),
		})
	}
	return views
}

func projectProviderBindingViews(specs []aiv1alpha1.ProjectProviderBindingSpec, statuses []aiv1alpha1.ProjectProviderBindingStatus) []ProjectProviderBindingView {
	statusByName := map[string]aiv1alpha1.ProjectProviderBindingStatus{}
	for _, st := range statuses {
		statusByName[st.Name] = st
	}
	views := make([]ProjectProviderBindingView, 0, len(specs)+len(statuses))
	for _, spec := range specs {
		st := statusByName[spec.Name]
		views = append(views, ProjectProviderBindingView{
			Name:       spec.Name,
			Provider:   firstNonEmpty(st.Provider, spec.Provider),
			Phase:      st.Phase,
			URL:        st.URL,
			PreviewURL: st.PreviewURL,
			Outputs:    st.Outputs,
		})
		delete(statusByName, spec.Name)
	}
	for _, st := range statusByName {
		views = append(views, ProjectProviderBindingView{
			Name:       st.Name,
			Provider:   st.Provider,
			Phase:      st.Phase,
			URL:        st.URL,
			PreviewURL: st.PreviewURL,
			Outputs:    st.Outputs,
		})
	}
	return views
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func projectUpdatedAt(p *aiv1alpha1.Project) time.Time {
	if p.Status.UpdatedAt != nil {
		return p.Status.UpdatedAt.Time
	}
	return p.CreationTimestamp.Time
}

func emptyProjectMemory() aiv1alpha1.ProjectMemory {
	return aiv1alpha1.ProjectMemory{
		Goals:        []string{},
		Requirements: []string{},
		Constraints:  []string{},
	}
}

var invalidProjectNameChars = regexp.MustCompile(`[^a-z0-9-]+`)

func slugifyProjectName(str string) string {
	str = strings.ToLower(strings.TrimSpace(str))
	str = invalidProjectNameChars.ReplaceAllString(str, "-")
	str = strings.Trim(str, "-")
	for strings.Contains(str, "--") {
		str = strings.ReplaceAll(str, "--", "-")
	}
	if len(str) > 63 {
		str = strings.Trim(str[:63], "-")
	}
	return str
}

const initialProjectMemoryUpdateAttempts = 3

// persistInitialProjectMemory records the user's original creation goal and the
// acceptance criteria from the auto-authorized initial execution plan. The
// update is additive: users may edit project memory independently, so conflicts
// are resolved by re-reading and merging rather than overwriting their values.
func persistInitialProjectPlanMemory(
	ctx context.Context,
	c *asclient.Client,
	project *aiv1alpha1.Project,
	goal string,
	requirements []string,
) error {
	if c == nil || project == nil {
		return errors.New("project client and project are required")
	}
	goal = strings.TrimSpace(goal)
	requirements = normalizeProjectMemoryEntries(requirements)
	if goal == "" || len(requirements) == 0 {
		return errors.New("initial project goal and requirements are required")
	}

	current := project.DeepCopy()
	var lastErr error
	for attempt := 0; attempt < initialProjectMemoryUpdateAttempts; attempt++ {
		current.Spec.Memory.Goals = appendUniqueProjectMemoryEntries(current.Spec.Memory.Goals, []string{goal})
		current.Spec.Memory.Requirements = appendUniqueProjectMemoryEntries(current.Spec.Memory.Requirements, requirements)
		updated, err := c.Projects().Update(ctx, current, metav1.UpdateOptions{})
		if err == nil {
			*project = *updated
			return nil
		}
		lastErr = err
		if !apierrors.IsConflict(err) {
			break
		}
		current, err = c.Projects().Get(ctx, project.Name, metav1.GetOptions{})
		if err != nil {
			return fmt.Errorf("reload project memory after conflict: %w", err)
		}
	}
	return fmt.Errorf("persist initial project memory: %w", lastErr)
}

func appendUniqueProjectMemoryEntries(existing, additions []string) []string {
	out := normalizeProjectMemoryEntries(existing)
	seen := make(map[string]struct{}, len(out)+len(additions))
	for _, entry := range out {
		seen[entry] = struct{}{}
	}
	for _, entry := range normalizeProjectMemoryEntries(additions) {
		if _, ok := seen[entry]; ok {
			continue
		}
		seen[entry] = struct{}{}
		out = append(out, entry)
	}
	return out
}

func normalizeProjectMemoryEntries(entries []string) []string {
	out := make([]string, 0, len(entries))
	seen := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		if _, ok := seen[entry]; ok {
			continue
		}
		seen[entry] = struct{}{}
		out = append(out, entry)
	}
	return out
}

func newMessageID() string {
	return "msg-" + uuid.NewString()
}
