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
	"slices"
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
	"github.com/faroshq/provider-app-studio/bindings"
	asclient "github.com/faroshq/provider-app-studio/client"
	appskills "github.com/faroshq/provider-app-studio/skills"
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

type ProjectView struct {
	Name string `json:"name"`
	// UID is the immutable Kubernetes identity. Project names may be reused
	// after an asynchronous delete, so portal mutations must never key on name
	// alone.
	UID string `json:"uid"`
	// Deleting is derived from metadata.deletionTimestamp, not from the
	// controller's eventually-updated status phase. This keeps terminating
	// projects visibly locked while their finalizers complete.
	Deleting       bool                          `json:"deleting"`
	DisplayName    string                        `json:"displayName"`
	Description    string                        `json:"description,omitempty"`
	Phase          string                        `json:"phase,omitempty"`
	Template       string                        `json:"template,omitempty"`
	Repository     *ProjectRepositoryView        `json:"repository,omitempty"`
	Memory         aiv1alpha1.ProjectMemory      `json:"memory,omitempty"`
	Sharing        aiv1alpha1.ProjectSharingSpec `json:"sharing,omitempty"`
	Environments   []ProjectEnvironmentView      `json:"environments,omitempty"`
	CreatedAt      time.Time                     `json:"createdAt"`
	UpdatedAt      *time.Time                    `json:"updatedAt,omitempty"`
	SourceRevision uint64                        `json:"sourceRevision,omitempty"`
	Thumbnail      *ProjectThumbnailView         `json:"thumbnail,omitempty"`
}

type ProjectEnvironmentView struct {
	Name     string                       `json:"name"`
	Mode     string                       `json:"mode,omitempty"`
	Phase    string                       `json:"phase,omitempty"`
	Bindings []ProjectProviderBindingView `json:"bindings,omitempty"`
}

type ProjectProviderBindingView struct {
	Name           string                                       `json:"name"`
	Provider       string                                       `json:"provider,omitempty"`
	Kind           aiv1alpha1.ProjectBindingKind                `json:"kind,omitempty"`
	ResourceRef    *aiv1alpha1.ProjectProviderResourceReference `json:"resourceRef,omitempty"`
	AllowedActions []aiv1alpha1.ProjectProviderActionSpec       `json:"allowedActions,omitempty"`
	Phase          string                                       `json:"phase,omitempty"`
	URL            string                                       `json:"url,omitempty"`
	PreviewURL     string                                       `json:"previewURL,omitempty"`
	Outputs        map[string]string                            `json:"outputs,omitempty"`
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
	CommitsError  string                        `json:"commitsError,omitempty"`
	commitsErr    error
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

type projectToolCallStreamEvent struct {
	ID         string                        `json:"id"`
	Name       string                        `json:"name,omitempty"`
	Status     string                        `json:"status"`
	Arguments  string                        `json:"arguments,omitempty"`
	Summary    string                        `json:"summary,omitempty"`
	Error      string                        `json:"error,omitempty"`
	Exec       *projectAssistantExecMetadata `json:"exec,omitempty"`
	Permission *projectAssistantPermission   `json:"permission,omitempty"`
	FollowUp   *projectAssistantFollowUp     `json:"followUp,omitempty"`
	Checkpoint *projectAssistantCheckpoint   `json:"checkpoint,omitempty"`
	Mutation   *projectAssistantMutation     `json:"mutation,omitempty"`
	// RecoveryOf is a server-validated presentation correlation to a prior
	// failed mutation action. It never participates in workspace authorization
	// or mutation semantics.
	RecoveryOf string `json:"recoveryOf,omitempty"`
	// MutationError carries bounded operation-aware failure metadata for the
	// action feed. The model-facing result contains the same fields in a typed
	// envelope; this copy keeps live/reload projections deterministic.
	MutationError *projectAssistantMutationFailure `json:"mutationError,omitempty"`
	// PreviewInspection carries bounded failure classification for truthful
	// live and reload action-feed presentation. It never contains page output.
	PreviewInspection *projectAssistantPreviewInspectionAction `json:"previewInspection,omitempty"`
	Sequence          int                                      `json:"sequence,omitempty"`
}

type projectAssistantMutation struct {
	Operation     string   `json:"operation,omitempty"`
	Changed       bool     `json:"changed,omitempty"`
	Path          string   `json:"path,omitempty"`
	PreviousPath  string   `json:"previousPath,omitempty"`
	Paths         []string `json:"paths,omitempty"`
	Additions     int      `json:"additions,omitempty"`
	Deletions     int      `json:"deletions,omitempty"`
	Replacements  int      `json:"replacements,omitempty"`
	Diff          string   `json:"diff,omitempty"`
	DiffTruncated bool     `json:"diffTruncated,omitempty"`
	RecoveryOf    string   `json:"recoveryOf,omitempty"`
}

// projectAssistantMutationFailure is the bounded, server-owned failure
// contract for typed workspace mutations. Operation/path/guidance are
// presentation metadata; recoveryOf is only a correlation to another action.
type projectAssistantMutationFailure struct {
	Code       string `json:"code"`
	Operation  string `json:"operation"`
	Path       string `json:"path,omitempty"`
	Guidance   string `json:"guidance"`
	RecoveryOf string `json:"recoveryOf,omitempty"`
}

type projectAssistantMutationFailureResult struct {
	Status     string                          `json:"status"`
	Code       string                          `json:"code"`
	Operation  string                          `json:"operation"`
	Path       string                          `json:"path,omitempty"`
	Guidance   string                          `json:"guidance"`
	RecoveryOf string                          `json:"recoveryOf,omitempty"`
	Message    string                          `json:"message"`
	Error      projectAssistantMutationFailure `json:"error"`
}

const projectAPIInitializingMessage = "App Studio is still initializing for this workspace. Try again shortly."
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
	if errors.Is(err, errProjectCreatePreflightUnavailable) {
		w.Header().Set("Retry-After", "2")
		writeStatus(w, http.StatusBadGateway, "BadGateway", "Project planning is temporarily unavailable. Retry project creation shortly, or choose a development template explicitly.")
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
		project := &list.Items[i]
		out = append(out, s.projectListView(r.Context(), c, project, id))
	}
	writeJSON(w, http.StatusOK, ListResponse[ProjectView]{Items: out})
}

func (s *Server) createProject(w http.ResponseWriter, r *http.Request) {
	c, id, ok := s.requireProjectClient(w, r)
	if !ok {
		return
	}
	var req CreateProjectRequest
	if !decodeStrictJSON(w, r, &req) {
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
	// Shared services: make sure the workspace's Studio exists so the search
	// backend is warm before the assistant's first turn. Best-effort.
	s.ensureStudio(ctx, c, id)
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
	} else if req.Prompt != "" && !(req.DisplayName != "" && selectedTemplate != nil) {
		// Skip inference when the caller already committed both a name and a
		// template — the wizard's blueprint step (POST /api/projects/plan)
		// already ran the preflight, so re-running it here would double the
		// LLM round-trip (a visible stall on "Planning project") and clobber
		// the name the user just confirmed. Only infer when something is
		// genuinely missing.
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
	finalizers := s.projectFinalizersForCreate(id)
	p := &aiv1alpha1.Project{
		ObjectMeta: metav1.ObjectMeta{
			Name:       name,
			Finalizers: finalizers,
			// Bridge to the workspace/store keyspace: the Project reconciler
			// only knows the cluster, but workspace scopes are keyed by the
			// org/workspace UUIDs the hub derives from the tenant path.
			// Commit convergence reads these back.
			Annotations: map[string]string{
				"ai.faros.sh/org-uuid":       id.orgUUID,
				"ai.faros.sh/workspace-uuid": id.workspaceUUID,
			},
		},
		Spec: defaultProjectSpec(name, req.DisplayName, req.Description, repoPlan.projectBinding()),
		Status: aiv1alpha1.ProjectStatus{
			Phase:     aiv1alpha1.ProjectPhaseReady,
			UpdatedAt: &now,
		},
	}
	if selectedTemplate != nil {
		if err := s.applyProjectDevelopmentTemplateWithIdentity(p, *selectedTemplate, id); err != nil {
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
		// Adoption stays caller-side: it claims an EXISTING Repository CR,
		// which needs the importing user's view and immediate feedback.
		if err := emitProjectCreationStatus(onStatus, "Importing repository"); err != nil {
			s.cleanupCreatedProjectSetup(ctx, c, id, created)
			return nil, err
		}
		if err := claimProjectRepository(ctx, c, created.Name, repoPlan); err != nil {
			s.cleanupCreatedProjectSetup(ctx, c, id, created)
			return nil, err
		}
	} else if err := emitProjectCreationStatus(onStatus, "Creating repository"); err != nil {
		// Non-adopted repositories are created by the Project reconciler
		// converging spec.repository (autoInit creates the repo on the git
		// host) — no inline creation here.
		s.cleanupCreatedProjectSetup(ctx, c, id, created)
		return nil, err
	}
	// Wizard step: attach the template's scaffold so the project opens on a
	// runnable placeholder. Skipped for adopted repos (they hydrate from the
	// imported tree below). Best-effort — never fails creation.
	if !repoPlan.Adopted && selectedTemplate != nil {
		s.emitScaffoldSeed(ctx, id, created, *selectedTemplate, onStatus, c)
	}
	// Instances are provisioned by the Project reconciler converging the spec
	// just written; the immediate response reports them Pending and the
	// status mirror catches up.
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

// projectFinalizersForCreate protects the short interval between a Project
// create response and its first controller reconcile. API-created Projects
// already know their tenant annotations, so installing the attachment
// finalizer here ensures a direct delete cannot bypass blob cleanup. Projects
// created through KCP do not get a finalizer until the controller has verified
// the same scope.
func (s *Server) projectFinalizersForCreate(id identity) []string {
	if s == nil || strings.TrimSpace(id.orgUUID) == "" || strings.TrimSpace(id.workspaceUUID) == "" {
		return nil
	}
	if s.attachments == nil {
		if _, ok := s.store.(store.AttachmentStore); !ok {
			return nil
		}
	}
	return []string{store.AttachmentStorageFinalizer}
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
	if err := validateProjectTemplateSelection(info); err != nil {
		// A platform-owned Template is never a valid project choice. This is a
		// server-side validation boundary for both explicit requests and model
		// or preflight-inferred names; do not fall back to an unbound project.
		return nil, newValidationError(err.Error())
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
	view := s.projectViewWithSourceRevision(r.Context(), c, p, id)
	writeJSON(w, http.StatusOK, s.projectViewWithThumbnail(r.Context(), view, id, p))
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
	view := s.projectViewWithSourceRevision(r.Context(), c, updated, id)
	writeJSON(w, http.StatusOK, s.projectViewWithThumbnail(r.Context(), view, id, updated))
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
	sharing.Preview.Mode = normalizedProjectPreviewSharingMode(sharing.Preview.Mode)
	if sharing.Publishing.Mode == "" {
		sharing.Publishing.Mode = aiv1alpha1.ProjectSharingModePrivate
	}
	if !validProjectPreviewSharingMode(sharing.Preview.Mode) {
		return aiv1alpha1.ProjectSharingSpec{}, newValidationError("sharing.preview.mode must be private or public")
	}
	if !validProjectSharingMode(sharing.Publishing.Mode) {
		return aiv1alpha1.ProjectSharingSpec{}, newValidationError("sharing.publishing.mode must be private, shared, or public")
	}
	return sharing, nil
}

func normalizedProjectPreviewSharingMode(mode aiv1alpha1.ProjectSharingMode) aiv1alpha1.ProjectSharingMode {
	return bindings.NormalizePreviewSharingMode(mode)
}

func validProjectPreviewSharingMode(mode aiv1alpha1.ProjectSharingMode) bool {
	return mode == aiv1alpha1.ProjectSharingModePrivate || mode == aiv1alpha1.ProjectSharingModePublic
}

func effectiveProjectPreviewAccess(mode aiv1alpha1.ProjectSharingMode) string {
	return bindings.PreviewAccessForMode(mode)
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

func (s *Server) reserveProjectExternalOperation(
	w http.ResponseWriter,
	ctx context.Context,
	id identity,
	p *aiv1alpha1.Project,
	action string,
) (func(), bool) {
	scope := projectMessageScope(id.orgUUID, id.workspaceUUID, p)
	if s.store != nil {
		if latest, err := s.store.LatestAssistantRun(ctx, scope); err == nil {
			if err := s.reconcileOrphanedProjectAssistantRun(ctx, scope, latest.ID); err != nil {
				writeStatus(w, http.StatusInternalServerError, "InternalError", "check assistant activity: "+err.Error())
				return nil, false
			}
		} else if !errors.Is(err, store.ErrAssistantRunNotFound) {
			writeStatus(w, http.StatusInternalServerError, "InternalError", "check assistant activity: "+err.Error())
			return nil, false
		}
	}
	release, err := s.projectAssistantSupervisor().Reserve(scope)
	if err != nil {
		if errors.Is(err, store.ErrAssistantRunConflict) {
			writeStatus(w, http.StatusConflict, "Conflict", "wait for or stop the active assistant run before "+action)
			return nil, false
		}
		writeProjectError(w, err)
		return nil, false
	}
	if s.store != nil {
		run, latestErr := s.store.LatestAssistantRun(ctx, scope)
		switch {
		case latestErr == nil && !assistantRunTerminal(run.Status):
			release()
			writeStatus(w, http.StatusConflict, "Conflict", "resolve or stop the active assistant run before "+action)
			return nil, false
		case latestErr != nil && !errors.Is(latestErr, store.ErrAssistantRunNotFound):
			release()
			writeStatus(w, http.StatusInternalServerError, "InternalError", "check assistant activity: "+latestErr.Error())
			return nil, false
		}
	}
	return release, true
}

func (s *Server) deleteProject(w http.ResponseWriter, r *http.Request) {
	c, id, ok := s.requireProjectClient(w, r)
	if !ok {
		return
	}
	name := mux.Vars(r)["project"]
	expectedUID := strings.TrimSpace(r.URL.Query().Get("uid"))
	if expectedUID == "" {
		writeStatus(w, http.StatusBadRequest, "BadRequest", "project UID is required; refresh the project list and try again")
		return
	}
	p, err := c.Projects().Get(r.Context(), name, metav1.GetOptions{})
	if err != nil {
		writeProjectError(w, err)
		return
	}
	if string(p.UID) != expectedUID {
		writeStatus(w, http.StatusConflict, "Conflict", "project identity changed; refresh the project list before deleting")
		return
	}
	messageScope := projectMessageScope(id.orgUUID, id.workspaceUUID, p)
	s.forgetProjectThumbnailCapture(id, p)
	releaseAssistantReservation, ok := s.reserveProjectExternalOperation(w, r.Context(), id, p, "deleting this project")
	if !ok {
		return
	}
	defer releaseAssistantReservation()
	// Cached coding environments are intentionally retained after a terminal
	// assistant turn, but remain owned by the Project. Delete the exact cache
	// before the Project CR so pre-ownerReference caches cannot become orphans.
	// New caches also carry a Project ownerReference as the controller/kubectl
	// deletion backstop.
	if err := s.deleteProjectAssistantRunSandboxCache(r.Context(), c, id, p); err != nil {
		writeProjectError(w, err)
		return
	}
	// Instances are torn down by the Project reconciler's finalizer when the
	// CR below is deleted (ownerReferences cover the no-controller case).
	// App-access RBAC grants reference the instance by name only and become
	// inert once it is gone; the share dialog can always clean strays.
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
	const attachmentCleanupFinalizer = store.AttachmentStorageFinalizer
	if !slices.Contains(p.Finalizers, attachmentCleanupFinalizer) {
		next := p.DeepCopy()
		next.Finalizers = append(next.Finalizers, attachmentCleanupFinalizer)
		p, err = c.Projects().Update(r.Context(), next, metav1.UpdateOptions{})
		if err != nil {
			writeProjectError(w, err)
			return
		}
	}
	uid := p.UID
	if err := c.Projects().Delete(r.Context(), name, metav1.DeleteOptions{
		Preconditions: &metav1.Preconditions{UID: &uid},
	}); err != nil {
		writeProjectError(w, err)
		return
	}
	if s.store != nil {
		cleanupCtx, cancelCleanup := detachedProjectPersistenceContext(r.Context())
		if err := s.store.DeleteProjectMessages(cleanupCtx, messageScope); err != nil {
			cancelCleanup()
			writeStatus(w, http.StatusInternalServerError, "InternalError", "delete project assistant data: "+err.Error())
			return
		}
		cancelCleanup()
	}
	// Attachment storage is an optional capability layered beside the message
	// store. The normal production wiring uses one store and the call above is
	// atomic with message cleanup; keep this explicit fallback for tests or
	// deployments that provide a separate attachment backend.
	if s.attachments != nil && !s.attachmentsInStore {
		cleanupCtx, cancelCleanup := detachedProjectPersistenceContext(r.Context())
		if err := s.attachments.DeleteProjectAttachments(cleanupCtx, messageScope); err != nil {
			cancelCleanup()
			writeStatus(w, http.StatusInternalServerError, "InternalError", "delete project attachments: "+err.Error())
			return
		}
		cancelCleanup()
	}
	current, err := c.Projects().Get(r.Context(), name, metav1.GetOptions{})
	if err != nil {
		if !apierrors.IsNotFound(err) {
			writeProjectError(w, err)
			return
		}
		current = nil
	}
	if current != nil && slices.Contains(current.Finalizers, attachmentCleanupFinalizer) {
		next := current.DeepCopy()
		next.Finalizers = slices.DeleteFunc(next.Finalizers, func(value string) bool { return value == attachmentCleanupFinalizer })
		if _, err := c.Projects().Update(r.Context(), next, metav1.UpdateOptions{}); err != nil {
			writeProjectError(w, err)
			return
		}
	}
	if thumbnailStore, ok := s.projectThumbnailStore(); ok {
		cleanupCtx, cancelCleanup := detachedProjectPersistenceContext(r.Context())
		if err := thumbnailStore.DeleteProjectThumbnail(cleanupCtx, messageScope); err != nil {
			klog.FromContext(r.Context()).Error(err, "delete project thumbnail", "project", name)
		}
		cancelCleanup()
	}
	if s.workspaces != nil {
		cleanupCtx, cancelCleanup := detachedProjectPersistenceContext(r.Context())
		if err := s.workspaces.DeleteSnapshots(cleanupCtx, projectWorkspaceScope(id, p)); err != nil {
			klog.FromContext(r.Context()).Error(err, "delete project workspace snapshots", "project", name)
		}
		cancelCleanup()
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) resumeProjectAssistant(w http.ResponseWriter, r *http.Request) {
	c, id, p, ok := s.requireProjectWithClient(w, r)
	if !ok {
		return
	}
	var req projectAssistantResumeRequest
	if !decodeStrictJSON(w, r, &req) {
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
	req.AssistantMessageID = run.ActiveMessageID
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
			transitionErr := accumulator.SetStatus(context.Background(), status)
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
		if errors.Is(resumeErr, context.Canceled) || errors.Is(context.Cause(ctx), context.Canceled) {
			_ = accumulator.UpdateRun(context.Background(), func(current *store.AssistantRun) {
				current.AbortReason = store.AssistantRunAbortReasonInterrupted
			})
			transitionTerminal(store.AssistantRunStatusInterrupted, "interrupted", resumeErr)
			return
		}
		if projectEinoAssistantIterationLimited(resumeErr) {
			_ = accumulator.UpdateRun(context.Background(), func(current *store.AssistantRun) {
				current.AbortReason = store.AssistantRunAbortReasonIterationLimited
				current.Error = projectAssistantRunErrorJSON(resumeErr, "max_iterations_exceeded")
			})
			transitionTerminal(store.AssistantRunStatusFailed, "iteration_limited", resumeErr)
			return
		}
		if projectEinoAssistantBudgetLimited(resumeErr) {
			_ = accumulator.UpdateRun(context.Background(), func(current *store.AssistantRun) {
				current.AbortReason = store.AssistantRunAbortReasonBudgetLimited
				current.Error = projectAssistantRunErrorJSON(resumeErr, "session_budget_exceeded")
			})
			transitionTerminal(store.AssistantRunStatusFailed, "budget_limited", resumeErr)
			return
		}
		if resumeErr != nil {
			_ = accumulator.UpdateRun(context.Background(), func(current *store.AssistantRun) {
				current.Error = projectAssistantRunErrorJSON(resumeErr, projectAssistantRunErrorInfo(resumeErr))
			})
			transitionTerminal(store.AssistantRunStatusFailed, "failed", resumeErr)
			return
		}
		_ = accumulator.SetStatus(context.Background(), resp.Status)
	}); err != nil {
		writeProjectError(w, err)
		return
	}
	s.projectAssistantSupervisor().log("resume", scope, run)
	if threadID := strings.TrimSpace(mux.Vars(r)["thread"]); threadID != "" {
		turn, turnErr := s.store.GetAssistantTurn(r.Context(), scope, threadID, runID)
		if turnErr != nil {
			s.writeAssistantThreadError(w, turnErr)
			return
		}
		s.startAssistantThreadMirror(scope, threadID, turn, run)
		writeJSON(w, http.StatusAccepted, turn)
		return
	}
	writeJSON(w, http.StatusAccepted, projectAssistantRunSnapshotToAPI(projectAssistantRunSnapshot{Run: run, Message: message}))
}

// reattachProjectAssistantPendingRun restores the in-memory lifecycle owner for
// a durable checkpoint after a provider restart.
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

func (s *Server) authorizeProjectAssistantRunActor(ctx context.Context, scope store.Scope, run store.AssistantRun, actor string, _ bool) error {
	origin, err := s.findProjectMessage(ctx, scope, run.UserMessageID)
	if err != nil || origin.ActorID != actor {
		return store.ErrAssistantRunNotFound
	}
	return nil
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
	// ThreadID lets the model boundary recover the latest prior image receipt
	// without placing thread routing state in the provider-facing request.
	ThreadID                 string
	InitialApprovedPlan      *projectAssistantApprovedPlan
	SkillSnapshot            *appskills.Snapshot
	SelectedSkills           []projectAssistantSkillReceipt
	SelectedContextResources []projectAssistantContextResourceReceipt
	ContentParts             []projectAssistantContentPart
}

func ptrProjectAssistantApprovedPlan(plan projectAssistantApprovedPlan) *projectAssistantApprovedPlan {
	return &plan
}

func projectAssistantStoredContent(reply, streamed string) string {
	if strings.TrimSpace(reply) != "" {
		return reply
	}
	return streamed
}

func projectAssistantToolCallsRequireDevelopmentSync(toolCalls []projectToolCallStreamEvent) bool {
	for _, toolCall := range toolCalls {
		if toolCall.Status != "succeeded" || !shouldSyncDevelopmentAfterTool(toolCall.Name) {
			continue
		}
		// Ordinary workspace tools report whether the operation changed source.
		// A successful idempotent write is still a successful tool call, but it
		// must not trigger a source sync or preview refresh. Keep treating a
		// missing mutation projection as changed for older durable transitions;
		// newly emitted mutation results always include Changed explicitly.
		if projectAssistantWorkspaceMutationTool(toolCall.Name) && toolCall.Mutation != nil && !toolCall.Mutation.Changed {
			continue
		}
		return true
	}
	return false
}

func detachedProjectPersistenceContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), projectMessagePersistTimeout)
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
		if interrupt != nil {
			metadata[projectMessageMetadataAssistantInterrupt] = interrupt
		}
	} else if response.Status == store.AssistantRunStatusPendingInput {
		if interrupt != nil {
			metadata[projectMessageMetadataAssistantInterrupt] = interrupt
		}
	} else {
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
	status := metadata[projectAssistantMetadataWorkingStatus]
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
		metadata[projectAssistantMetadataWorkingStatus] = status
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
	actions := projectAssistantActionFeedUpdatesFromToolCalls(events)
	return filterProjectAssistantActionFeedItems(reconcileProjectAssistantMutationRecovery(actions))
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
		return filterProjectAssistantActionFeedItems(reconcileProjectAssistantMutationRecovery(typed))
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return nil
	}
	var out []projectAssistantActionFeedItem
	if err := json.Unmarshal(data, &out); err != nil {
		return nil
	}
	return filterProjectAssistantActionFeedItems(reconcileProjectAssistantMutationRecovery(out))
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
		return filterProjectAssistantActionFeedItems(reconcileProjectAssistantMutationRecovery(upsertProjectAssistantActionFeedItem(actions, action)))
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
	if next.MediaKind == "" {
		next.MediaKind = existing.MediaKind
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
	if next.RecoveryOf == "" {
		next.RecoveryOf = existing.RecoveryOf
	}
	if next.Diagnostic == nil {
		next.Diagnostic = existing.Diagnostic
	}
	next.Exec = mergeProjectAssistantExecMetadata(existing.Exec, next.Exec)
	return next
}

// reconcileProjectAssistantMutationRecovery updates only an explicitly linked
// prior failed mutation. It never infers a relationship from matching paths;
// missing, malformed, or cross-run references are cleared and ignored.
func reconcileProjectAssistantMutationRecovery(actions []projectAssistantActionFeedItem) []projectAssistantActionFeedItem {
	if len(actions) == 0 {
		return actions
	}
	byID := make(map[string]int, len(actions))
	for index := range actions {
		if actions[index].ID != "" {
			byID[actions[index].ID] = index
		}
	}
	for index := range actions {
		action := &actions[index]
		if action.RecoveryOf == "" {
			continue
		}
		priorIndex, ok := byID[action.RecoveryOf]
		if !ok || priorIndex == index {
			action.RecoveryOf = ""
			continue
		}
		prior := &actions[priorIndex]
		if prior.Kind != projectAssistantActionFeedItemEdit ||
			(prior.Status != projectAssistantActionFeedStatusFailed && prior.Status != projectAssistantActionFeedStatusRetrying) {
			action.RecoveryOf = ""
			continue
		}
		switch action.Status {
		case projectAssistantActionFeedStatusRunning, projectAssistantActionFeedStatusWaiting:
			prior.Status = projectAssistantActionFeedStatusRetrying
			prior.Severity = projectAssistantActionFeedSeverityAttention
			prior.Title = projectAssistantActionFeedItemTitle(prior.Kind, prior.Status)
		case projectAssistantActionFeedStatusSucceeded:
			prior.Status = projectAssistantActionFeedStatusRecovered
			prior.Severity = projectAssistantActionFeedSeverityNormal
			prior.Title = projectAssistantActionFeedItemTitle(prior.Kind, prior.Status)
		case projectAssistantActionFeedStatusFailed, projectAssistantActionFeedStatusRejected:
			// A linked retry that terminates unsuccessfully must not leave the
			// original action looking active. Keep its original diagnostic and
			// reference so the failed repair remains explainable.
			prior.Status = projectAssistantActionFeedStatusFailed
			prior.Severity = projectAssistantActionFeedSeverityError
			prior.Title = projectAssistantActionFeedItemTitle(prior.Kind, prior.Status)
		}
	}
	return actions
}

// finalizeProjectAssistantActionFeed closes lifecycle entries left open by an
// interrupted engine segment. A terminal turn must never publish an action as
// still running or waiting for approval.
func finalizeProjectAssistantActionFeed(actions []projectAssistantActionFeedItem, runStatus store.AssistantRunStatus) []projectAssistantActionFeedItem {
	for i := range actions {
		if actions[i].Status == projectAssistantActionFeedStatusRetrying {
			if assistantRunTerminal(runStatus) {
				// A terminal run without a successful linked retry is still a
				// failed mutation. Preserve the original diagnostic/reference;
				// only synthesize one when legacy metadata had none.
				actions[i].Status = projectAssistantActionFeedStatusFailed
				actions[i].Severity = projectAssistantActionFeedSeverityError
				if actions[i].Diagnostic == nil {
					actions[i].Diagnostic = projectAssistantActionFeedDiagnostic(actions[i].ID, "")
				}
				actions[i].Title = projectAssistantActionFeedItemTitle(actions[i].Kind, actions[i].Status)
			}
			continue
		}
		if actions[i].Status != projectAssistantActionFeedStatusRunning && actions[i].Status != projectAssistantActionFeedStatusWaiting {
			continue
		}
		if !assistantRunTerminal(runStatus) {
			// Pending permission/input runs still own an unresolved action. Keep
			// its waiting state until the user resumes or rejects it.
			continue
		}
		actions[i].Status = projectAssistantActionFeedStatusFailed
		actions[i].Severity = projectAssistantActionFeedSeverityError
		actions[i].Diagnostic = projectAssistantActionFeedDiagnostic(actions[i].ID, "")
		actions[i].Title = projectAssistantActionFeedItemTitle(actions[i].Kind, actions[i].Status)
	}
	if assistantRunTerminal(runStatus) {
		for i := range actions {
			if actions[i].Exec == nil {
				continue
			}
			if projectAssistantExecStatusTerminal(actions[i].Exec.Status) {
				continue
			}
			switch actions[i].Status {
			case projectAssistantActionFeedStatusSucceeded:
				actions[i].Exec.Status = "succeeded"
			case projectAssistantActionFeedStatusFailed, projectAssistantActionFeedStatusRejected:
				// The public exec contract intentionally uses failed for both a
				// command failure and a user rejection; "rejected" is not a
				// terminal process state exposed by the portal disclosure parser.
				actions[i].Exec.Status = "failed"
			}
		}
	}
	return filterProjectAssistantActionFeedItems(actions)
}

func projectAssistantExecStatusTerminal(status string) bool {
	switch strings.TrimSpace(status) {
	case "succeeded", "failed", "timed_out", "canceled", "cancelled", "blocked", "error":
		return true
	default:
		return false
	}
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
	// Permission callbacks carry their execution disclosure on Permission.Exec,
	// while terminal tool callbacks carry it directly on Exec. Normalize both
	// locations before merging so checkpoint resume cannot drop the approved
	// argv/authority contract even when the terminal callback has no arguments.
	existingExec := existing.Exec
	if existingExec == nil && existing.Permission != nil {
		existingExec = existing.Permission.Exec
	}
	nextExec := next.Exec
	if nextExec == nil && next.Permission != nil {
		nextExec = next.Permission.Exec
	}
	next.Exec = mergeProjectAssistantExecMetadata(existingExec, nextExec)
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
	if next.RecoveryOf == "" {
		next.RecoveryOf = existing.RecoveryOf
	}
	if next.MutationError == nil {
		next.MutationError = existing.MutationError
	}
	if next.PreviewInspection == nil {
		next.PreviewInspection = existing.PreviewInspection
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
		UID:          string(p.UID),
		Deleting:     p.DeletionTimestamp != nil,
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

// projectViewWithSourceRevision adds the owner workspace's optimistic-lock
// fence to a project response. Project PATCH does not mutate source, so its
// response must preserve the same authority that GET exposes; otherwise the
// portal loses the revision when it replaces its selected project model.
func (s *Server) projectViewWithSourceRevision(ctx context.Context, c *asclient.Client, p *aiv1alpha1.Project, id identity) ProjectView {
	view := projectView(ctx, c, p, id)
	if s == nil || s.workspaces == nil || p == nil {
		return view
	}
	if revision, err := s.workspaces.SourceRevision(ctx, projectWorkspaceScope(id, p)); err == nil {
		view.SourceRevision = revision
	}
	return view
}

// projectListView includes the workspace revision before thumbnail
// reconciliation. The capture scheduler uses that revision to prove the
// development runtime is serving the exact committed source shown by the
// gallery image.
func (s *Server) projectListView(ctx context.Context, c *asclient.Client, p *aiv1alpha1.Project, id identity) ProjectView {
	view := s.projectViewWithSourceRevision(ctx, c, p, id)
	return s.projectViewWithThumbnail(ctx, view, id, p)
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
			Name:           spec.Name,
			Provider:       firstNonEmpty(st.Provider, spec.Provider),
			Kind:           spec.Kind,
			ResourceRef:    spec.ResourceRef,
			AllowedActions: append([]aiv1alpha1.ProjectProviderActionSpec(nil), spec.AllowedActions...),
			Phase:          st.Phase,
			URL:            st.URL,
			PreviewURL:     st.PreviewURL,
			Outputs:        st.Outputs,
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
// acceptance criteria from the initial execution plan. Write authority remains
// the separate user-derived, run-local creation grant. The
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
