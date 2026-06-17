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
	Name          string `json:"name,omitempty"`
	DisplayName   string `json:"displayName,omitempty"`
	Description   string `json:"description,omitempty"`
	Prompt        string `json:"prompt,omitempty"`
	ConnectionRef string `json:"connectionRef,omitempty"`
}

type PatchProjectRequest struct {
	DisplayName *string `json:"displayName,omitempty"`
	Description *string `json:"description,omitempty"`
}

type PatchProjectMemoryRequest struct {
	Goals        *[]string `json:"goals,omitempty"`
	Requirements *[]string `json:"requirements,omitempty"`
	Constraints  *[]string `json:"constraints,omitempty"`
}

type CreateProjectMessageRequest struct {
	Role    string `json:"role,omitempty"`
	Content string `json:"content"`
}

type ProjectView struct {
	Name        string                   `json:"name"`
	DisplayName string                   `json:"displayName"`
	Description string                   `json:"description,omitempty"`
	Phase       string                   `json:"phase,omitempty"`
	Repository  *ProjectRepositoryView   `json:"repository,omitempty"`
	Memory      aiv1alpha1.ProjectMemory `json:"memory,omitempty"`
	CreatedAt   time.Time                `json:"createdAt"`
	UpdatedAt   *time.Time               `json:"updatedAt,omitempty"`
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

type projectMessageStreamEvent struct {
	Type               string                      `json:"type"`
	AssistantMessageID string                      `json:"assistantMessageID,omitempty"`
	Content            string                      `json:"content,omitempty"`
	Status             string                      `json:"status,omitempty"`
	Error              string                      `json:"error,omitempty"`
	Project            *ProjectView                `json:"project,omitempty"`
	ToolCall           *projectToolCallStreamEvent `json:"toolCall,omitempty"`
	Permission         *projectAssistantPermission `json:"permission,omitempty"`
	Checkpoint         *projectAssistantCheckpoint `json:"checkpoint,omitempty"`
}

type projectToolCallStreamEvent struct {
	ID         string                      `json:"id"`
	Name       string                      `json:"name,omitempty"`
	Status     string                      `json:"status"`
	Arguments  string                      `json:"arguments,omitempty"`
	Summary    string                      `json:"summary,omitempty"`
	Error      string                      `json:"error,omitempty"`
	Permission *projectAssistantPermission `json:"permission,omitempty"`
	Checkpoint *projectAssistantCheckpoint `json:"checkpoint,omitempty"`
}

const projectAPIInitializingMessage = "App Studio is still initializing for this workspace. Try again shortly."
const projectMessageMetadataStatus = "status"
const projectMessageMetadataToolCalls = "toolCalls"
const projectMessageStatusInterrupted = "interrupted"
const projectMessageStatusPendingPermission = "pending_permission"
const projectMessagePersistTimeout = 5 * time.Second

var errProjectAssistantMessageNotFound = errors.New("project assistant message not found")

type projectCreationStatusFunc func(string) error

func writeProjectError(w http.ResponseWriter, err error) {
	if isProjectAPIInitializingError(err) {
		w.Header().Set("Retry-After", "2")
		writeStatus(w, http.StatusServiceUnavailable, "ServiceUnavailable", projectAPIInitializingMessage)
		return
	}
	writeError(w, err)
}

func isProjectAPIInitializingError(err error) bool {
	if !apierrors.IsNotFound(err) {
		return false
	}
	return strings.Contains(strings.ToLower(err.Error()), "server could not find the requested resource")
}

func (s *Server) listProjects(w http.ResponseWriter, r *http.Request) {
	c, _, ok := s.requireProjectClient(w, r)
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
		out = append(out, projectView(r.Context(), c, &list.Items[i]))
	}
	writeJSON(w, http.StatusOK, ListResponse[ProjectView]{Items: out})
}

func (s *Server) createProject(w http.ResponseWriter, r *http.Request) {
	c, _, ok := s.requireProjectClient(w, r)
	if !ok {
		return
	}
	var req CreateProjectRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	created, err := s.createProjectFromRequest(r.Context(), c, req, nil)
	if err != nil {
		writeProjectError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, projectView(r.Context(), c, created))
}

func (s *Server) createProjectFromRequest(ctx context.Context, c *asclient.Client, req CreateProjectRequest, onStatus projectCreationStatusFunc) (*aiv1alpha1.Project, error) {
	req.DisplayName = strings.TrimSpace(req.DisplayName)
	req.Description = strings.TrimSpace(req.Description)
	req.Prompt = strings.TrimSpace(req.Prompt)
	req.ConnectionRef = strings.TrimSpace(req.ConnectionRef)
	repoBase := slugifyProjectName(req.DisplayName)
	if req.Prompt != "" {
		if err := emitProjectCreationStatus(onStatus, "Naming project"); err != nil {
			return nil, err
		}
		naming, err := s.generateProjectNaming(ctx, c, req.Prompt)
		if err != nil {
			return nil, err
		}
		req.DisplayName = naming.DisplayName
		repoBase = naming.RepositoryName
	}
	if req.DisplayName == "" {
		return nil, newValidationError("displayName is required")
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
	repoPlan, err := s.prepareProjectRepository(ctx, c, req.ConnectionRef, repoBase, req.DisplayName, req.Description)
	if err != nil {
		return nil, err
	}
	now := metav1.Now()
	p := &aiv1alpha1.Project{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Spec: aiv1alpha1.ProjectSpec{
			DisplayName: req.DisplayName,
			Description: req.Description,
			Repository:  repoPlan.projectBinding(),
			Memory:      emptyProjectMemory(),
		},
		Status: aiv1alpha1.ProjectStatus{
			Phase:     aiv1alpha1.ProjectPhaseReady,
			UpdatedAt: &now,
		},
	}
	if err := emitProjectCreationStatus(onStatus, "Creating project"); err != nil {
		return nil, err
	}
	created, err := c.Projects().Create(ctx, p, metav1.CreateOptions{})
	if err != nil {
		return nil, err
	}
	if err := emitProjectCreationStatus(onStatus, "Creating repository"); err != nil {
		cleanupCreatedProjectSetup(ctx, c, created)
		return nil, err
	}
	if err := s.createProjectRepository(ctx, c, created.Name, repoPlan); err != nil {
		cleanupCreatedProjectSetup(ctx, c, created)
		return nil, err
	}
	updated, err := touchProjectStatus(ctx, c, created)
	if err != nil {
		cleanupCreatedProjectSetup(ctx, c, created)
		return nil, err
	}
	return updated, nil
}

func emitProjectCreationStatus(onStatus projectCreationStatusFunc, status string) error {
	if onStatus == nil {
		return nil
	}
	return onStatus(status)
}

func cleanupCreatedProjectSetup(ctx context.Context, c *asclient.Client, p *aiv1alpha1.Project) {
	if c == nil || p == nil {
		return
	}
	if p.Spec.Repository != nil {
		if ref := strings.TrimSpace(p.Spec.Repository.RepositoryRef); ref != "" {
			_ = c.Dynamic().Resource(codeRepositoriesGVR).Delete(ctx, ref, metav1.DeleteOptions{})
		}
	}
	if name := strings.TrimSpace(p.Name); name != "" {
		_ = c.Projects().Delete(ctx, name, metav1.DeleteOptions{})
	}
}

func (s *Server) getProject(w http.ResponseWriter, r *http.Request) {
	c, _, p, ok := s.requireProjectWithClient(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, projectView(r.Context(), c, p))
}

func (s *Server) patchProject(w http.ResponseWriter, r *http.Request) {
	c, _, p, ok := s.requireProjectWithClient(w, r)
	if !ok {
		return
	}
	var req PatchProjectRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	changed := false
	if req.DisplayName != nil {
		displayName := strings.TrimSpace(*req.DisplayName)
		if displayName == "" {
			writeProjectError(w, newValidationError("displayName cannot be empty"))
			return
		}
		p.Spec.DisplayName = displayName
		changed = true
	}
	if req.Description != nil {
		p.Spec.Description = strings.TrimSpace(*req.Description)
		changed = true
	}
	if !changed {
		writeProjectError(w, newValidationError("PATCH body must set displayName or description"))
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
	writeJSON(w, http.StatusOK, projectView(r.Context(), c, updated))
}

func (s *Server) deleteProject(w http.ResponseWriter, r *http.Request) {
	c, id, ok := s.requireProjectClient(w, r)
	if !ok {
		return
	}
	name := mux.Vars(r)["project"]
	if err := c.Projects().Delete(r.Context(), name, metav1.DeleteOptions{}); err != nil {
		writeProjectError(w, err)
		return
	}
	if s.store != nil {
		if err := s.store.DeleteProjectMessages(r.Context(), projectMessageScope(id.orgUUID, id.workspaceUUID, name)); err != nil {
			klog.FromContext(r.Context()).Error(err, "delete project messages", "project", name)
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) listProjectMessages(w http.ResponseWriter, r *http.Request) {
	c, id, p, ok := s.requireProjectWithClient(w, r)
	if !ok {
		return
	}
	msgStore, ok := s.requireStore(w)
	if !ok {
		return
	}
	limit := listLimitFromRequest(r)
	cursor := strings.TrimSpace(r.URL.Query().Get("cursor"))
	if err := s.migrateLegacyProjectMessages(r.Context(), c, id.orgUUID, id.workspaceUUID, p); err != nil {
		writeProjectError(w, err)
		return
	}
	page, err := msgStore.ListMessages(r.Context(), projectMessageScope(id.orgUUID, id.workspaceUUID, p.Name), limit, cursor)
	if err != nil {
		writeProjectError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, ProjectMessagesResponse{
		Items:      projectMessagesToAPI(page.Items),
		NextCursor: page.NextCursor,
	})
}

func (s *Server) createProjectStartStream(w http.ResponseWriter, r *http.Request) {
	c, id, ok := s.requireProjectClient(w, r)
	if !ok {
		return
	}
	var req CreateProjectRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	req.Prompt = strings.TrimSpace(req.Prompt)
	if req.Prompt == "" {
		writeProjectError(w, newValidationError("prompt is required"))
		return
	}
	msgStore, ok := s.requireStore(w)
	if !ok {
		return
	}
	flusher, ok := startProjectMessageStream(w)
	if !ok {
		return
	}
	writeStreamError := func(err error) {
		_ = writeProjectMessageStreamEvent(w, flusher, projectMessageStreamEvent{
			Type:  string(projectAssistantEventRunFailed),
			Error: err.Error(),
		})
	}
	writeStatus := func(status string) error {
		return writeProjectMessageStreamEvent(w, flusher, projectMessageStreamEvent{
			Type:   "status",
			Status: status,
		})
	}

	if err := writeStatus("Starting"); err != nil {
		return
	}
	created, err := s.createProjectFromRequest(r.Context(), c, req, writeStatus)
	if err != nil {
		writeStreamError(err)
		return
	}
	if err := appendProjectUserMessage(r.Context(), msgStore, projectMessageScope(id.orgUUID, id.workspaceUUID, created.Name), req.Prompt); err != nil {
		cleanupCreatedProjectSetup(r.Context(), c, created)
		writeStreamError(err)
		return
	}
	view := projectView(r.Context(), c, created)
	if err := writeProjectMessageStreamEvent(w, flusher, projectMessageStreamEvent{
		Type:    "project",
		Status:  "Project ready",
		Project: &view,
	}); err != nil {
		return
	}
	if err := writeStatus("Working"); err != nil {
		return
	}
	s.streamProjectAssistant(w, flusher, r, c, id, created, msgStore)
}

func (s *Server) createProjectMessageStream(w http.ResponseWriter, r *http.Request) {
	c, id, p, ok := s.requireProjectWithClient(w, r)
	if !ok {
		return
	}
	var req CreateProjectMessageRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	req.Role = strings.TrimSpace(req.Role)
	if req.Role == "" {
		req.Role = aiv1alpha1.ProjectMessageRoleUser
	}
	if req.Role != aiv1alpha1.ProjectMessageRoleUser {
		writeProjectError(w, newValidationError("role must be user"))
		return
	}
	req.Content = strings.TrimSpace(req.Content)
	if req.Content == "" {
		writeProjectError(w, newValidationError("content is required"))
		return
	}

	msgStore, ok := s.requireStore(w)
	if !ok {
		return
	}
	if err := s.migrateLegacyProjectMessages(r.Context(), c, id.orgUUID, id.workspaceUUID, p); err != nil {
		writeProjectError(w, err)
		return
	}
	if err := appendProjectUserMessage(r.Context(), msgStore, projectMessageScope(id.orgUUID, id.workspaceUUID, p.Name), req.Content); err != nil {
		writeProjectError(w, err)
		return
	}

	flusher, ok := startProjectMessageStream(w)
	if !ok {
		return
	}
	s.streamProjectAssistant(w, flusher, r, c, id, p, msgStore)
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
	resp, err := s.resumeProjectAssistantRunWithRepositoryAndClient(r.Context(), r, id, c, p, projectRepositoryView(r.Context(), c, p), mux.Vars(r)["run"], req)
	if err != nil {
		writeProjectError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) abortProjectAssistant(w http.ResponseWriter, r *http.Request) {
	_, id, p, ok := s.requireProjectWithClient(w, r)
	if !ok {
		return
	}
	resp, err := s.abortProjectAssistantRun(r.Context(), id, p, mux.Vars(r)["run"])
	if err != nil {
		writeProjectError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func startProjectMessageStream(w http.ResponseWriter) (http.Flusher, bool) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeStatus(w, http.StatusInternalServerError, "InternalError", "streaming unsupported")
		return nil, false
	}
	return flusher, true
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

func (s *Server) streamProjectAssistant(
	w http.ResponseWriter,
	flusher http.Flusher,
	r *http.Request,
	c *asclient.Client,
	id identity,
	p *aiv1alpha1.Project,
	msgStore store.Store,
) {
	assistantID := newMessageID()
	assistantContent := &strings.Builder{}
	var streamErr error
	var streamedToolCalls []projectToolCallStreamEvent
	var pendingPermissionToolCallID string
	scope := projectMessageScope(id.orgUUID, id.workspaceUUID, p.Name)
	streamWriter := projectAssistantStreamWriter{
		assistantID: assistantID,
		write: func(event projectMessageStreamEvent) error {
			return writeProjectMessageStreamEvent(w, flusher, event)
		},
	}
	emitAssistantEvent := func(event projectAssistantEvent) {
		switch event.Type {
		case projectAssistantEventPermissionNeeded:
			if event.Permission != nil && event.Permission.ToolCallID != "" {
				pendingPermissionToolCallID = event.Permission.ToolCallID
				streamedToolCalls = upsertProjectToolCallStreamEvent(streamedToolCalls, projectToolCallStreamEvent{
					ID:         event.Permission.ToolCallID,
					Name:       event.Permission.ToolName,
					Status:     "permission_required",
					Summary:    event.Permission.Reason,
					Permission: event.Permission,
				})
			}
		case projectAssistantEventCheckpointSaved:
			if event.Checkpoint != nil && pendingPermissionToolCallID != "" {
				streamedToolCalls = upsertProjectToolCallStreamEvent(streamedToolCalls, projectToolCallStreamEvent{
					ID:         pendingPermissionToolCallID,
					Status:     "permission_required",
					Checkpoint: event.Checkpoint,
				})
			}
		}
		if streamErr != nil {
			return
		}
		streamErr = streamWriter.EmitProjectAssistantEvent(r.Context(), event)
	}

	streamChunk := func(chunk string) {
		if streamErr != nil {
			return
		}
		if chunk == "" {
			return
		}
		assistantContent.WriteString(chunk)
		emitAssistantEvent(projectAssistantEvent{
			Type:  projectAssistantEventMessageDelta,
			Delta: chunk,
		})
	}
	streamStatus := func(status string) {
		if streamErr != nil {
			return
		}
		if status == "" {
			return
		}
		emitAssistantEvent(projectAssistantEvent{
			Type:   projectAssistantEventStatus,
			Status: status,
		})
	}
	streamToolCall := func(toolCall projectToolCallStreamEvent) {
		if toolCall.ID == "" || toolCall.Status == "" {
			return
		}
		streamedToolCalls = upsertProjectToolCallStreamEvent(streamedToolCalls, toolCall)
		if streamErr != nil {
			return
		}
		emitAssistantEvent(projectAssistantEvent{
			Type: projectAssistantEventTypeForToolCallStatus(toolCall.Status),
			ToolCall: &projectAssistantToolCall{
				ID:        toolCall.ID,
				Name:      toolCall.Name,
				Status:    toolCall.Status,
				Arguments: toolCall.Arguments,
				Summary:   toolCall.Summary,
				Error:     toolCall.Error,
			},
		})
	}

	reply, err := s.generateProjectAssistantStream(r, id, c, p, projectAssistantStreamCallbacks{
		OnChunk:          streamChunk,
		OnStatus:         streamStatus,
		OnToolCall:       streamToolCall,
		OnAssistantEvent: emitAssistantEvent,
	})
	if err != nil {
		var permissionErr *projectAssistantPermissionRequiredError
		if errors.As(err, &permissionErr) {
			persistCtx, cancel := detachedProjectPersistenceContext(r.Context())
			defer cancel()
			if err := appendProjectAssistantMessage(persistCtx, msgStore, scope, assistantID, assistantContent.String(), projectAssistantMessageMetadata(projectMessageStatusPendingPermission, streamedToolCalls)); err != nil {
				if streamErr == nil {
					_ = streamWriter.EmitProjectAssistantEvent(r.Context(), projectAssistantEvent{
						Type:  projectAssistantEventRunFailed,
						Error: "assistant persistence failed: " + err.Error(),
					})
				}
			}
			return
		}
		if shouldPersistInterruptedProjectAssistant(r.Context(), err, streamErr, assistantContent.String()) {
			persistErr := appendInterruptedProjectAssistantMessage(r.Context(), msgStore, scope, assistantID, assistantContent.String())
			if persistErr != nil && streamErr == nil {
				_ = streamWriter.EmitProjectAssistantEvent(r.Context(), projectAssistantEvent{
					Type:  projectAssistantEventRunFailed,
					Error: "assistant persistence failed: " + persistErr.Error(),
				})
			}
			return
		}
		if streamErr != nil {
			_ = appendInterruptedProjectAssistantMessage(r.Context(), msgStore, scope, assistantID, assistantContent.String())
			return
		}
		if errors.Is(err, errProjectLLMNotConfigured) {
			_ = streamWriter.EmitProjectAssistantEvent(r.Context(), projectAssistantEvent{
				Type:  projectAssistantEventRunFailed,
				Error: err.Error(),
			})
			return
		}
		_ = streamWriter.EmitProjectAssistantEvent(r.Context(), projectAssistantEvent{
			Type:  projectAssistantEventRunFailed,
			Error: "assistant generation failed: " + err.Error(),
		})
		return
	}
	if streamErr != nil {
		_ = appendInterruptedProjectAssistantMessage(r.Context(), msgStore, scope, assistantID, assistantContent.String())
		return
	}
	assistantReply := projectAssistantStoredContent(reply, assistantContent.String())
	if strings.TrimSpace(assistantReply) == "" {
		_ = streamWriter.EmitProjectAssistantEvent(r.Context(), projectAssistantEvent{
			Type:  projectAssistantEventRunFailed,
			Error: "assistant generation returned an empty response",
		})
		return
	}
	if pending := projectAssistantUnstreamedContent(assistantReply, assistantContent.String()); pending != "" {
		streamChunk(pending)
		if streamErr != nil {
			_ = appendInterruptedProjectAssistantMessage(r.Context(), msgStore, scope, assistantID, assistantContent.String())
			return
		}
	}

	if err := appendProjectAssistantMessage(r.Context(), msgStore, scope, assistantID, assistantReply, projectAssistantMessageMetadata("", streamedToolCalls)); err != nil {
		_ = streamWriter.EmitProjectAssistantEvent(r.Context(), projectAssistantEvent{
			Type:  projectAssistantEventRunFailed,
			Error: "assistant persistence failed: " + err.Error(),
		})
		return
	}
	if err := streamWriter.EmitProjectAssistantEvent(r.Context(), projectAssistantEvent{Type: projectAssistantEventRunFinished}); err != nil {
		return
	}
}

func projectAssistantStoredContent(reply, streamed string) string {
	if strings.TrimSpace(reply) != "" {
		return reply
	}
	return streamed
}

func projectAssistantUnstreamedContent(reply, streamed string) string {
	reply = strings.TrimSpace(reply)
	if reply == "" {
		return ""
	}
	streamed = strings.TrimSpace(streamed)
	if streamed == "" {
		return reply
	}
	if strings.Contains(streamed, reply) {
		return ""
	}
	return "\n\n" + reply
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
	toolCalls := projectToolCallStreamEventsFromMetadata(metadata[projectMessageMetadataToolCalls])
	if !projectAssistantPermissionMessageMatchesResume(metadata, toolCalls, response) {
		return nil
	}
	if response.ToolCall != nil {
		toolCalls = upsertProjectToolCallStreamEvent(toolCalls, *response.ToolCall)
	}
	if response.Permission != nil {
		toolCalls = upsertProjectToolCallStreamEvent(toolCalls, projectToolCallStreamEvent{
			ID:         response.Permission.ToolCallID,
			Name:       response.Permission.ToolName,
			Status:     "permission_required",
			Summary:    response.Permission.Reason,
			Permission: response.Permission,
		})
	}
	if response.Checkpoint != nil && response.Permission != nil {
		toolCalls = upsertProjectToolCallStreamEvent(toolCalls, projectToolCallStreamEvent{
			ID:         response.Permission.ToolCallID,
			Status:     "permission_required",
			Checkpoint: response.Checkpoint,
		})
	}
	if response.Status == store.AssistantRunStatusPendingPermission {
		metadata[projectMessageMetadataStatus] = projectMessageStatusPendingPermission
	} else {
		delete(metadata, projectMessageMetadataStatus)
	}
	metadata[projectMessageMetadataToolCalls] = sanitizeProjectToolCallStreamEventsForMetadata(toolCalls)
	now := time.Now().UTC()
	return s.store.AppendMessage(ctx, scope, store.Message{
		ID:        msg.ID,
		Role:      msg.Role,
		Content:   msg.Content,
		Metadata:  metadata,
		CreatedAt: msg.CreatedAt,
		UpdatedAt: now,
	})
}

func projectAssistantPermissionMessageMatchesResume(metadata map[string]any, toolCalls []projectToolCallStreamEvent, response projectAssistantResumeResponse) bool {
	if metadata[projectMessageMetadataStatus] != projectMessageStatusPendingPermission {
		return false
	}
	if response.RunID == "" || len(toolCalls) == 0 {
		return false
	}
	toolIDs := map[string]struct{}{}
	if response.ToolCall != nil && response.ToolCall.ID != "" {
		toolIDs[response.ToolCall.ID] = struct{}{}
	}
	if response.Permission != nil && response.Permission.ToolCallID != "" {
		toolIDs[response.Permission.ToolCallID] = struct{}{}
	}
	for _, event := range toolCalls {
		if event.Checkpoint == nil || event.Checkpoint.ID != response.RunID {
			continue
		}
		if len(toolIDs) == 0 {
			return true
		}
		if _, ok := toolIDs[event.ID]; ok {
			return true
		}
		if event.Permission != nil {
			if _, ok := toolIDs[event.Permission.ToolCallID]; ok {
				return true
			}
		}
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
	if len(toolCalls) > 0 {
		metadata[projectMessageMetadataToolCalls] = sanitizeProjectToolCallStreamEventsForMetadata(toolCalls)
	}
	if len(metadata) == 0 {
		return nil
	}
	return metadata
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
	if next.Checkpoint == nil {
		next.Checkpoint = existing.Checkpoint
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

func projectView(ctx context.Context, c *asclient.Client, p *aiv1alpha1.Project) ProjectView {
	view := ProjectView{
		Name:        p.Name,
		DisplayName: p.Spec.DisplayName,
		Description: p.Spec.Description,
		Phase:       p.Status.Phase,
		Repository:  projectRepositoryView(ctx, c, p),
		Memory:      p.Spec.Memory,
		CreatedAt:   p.CreationTimestamp.Time,
	}
	if p.Status.UpdatedAt != nil {
		t := p.Status.UpdatedAt.Time
		view.UpdatedAt = &t
	}
	return view
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

func newMessageID() string {
	return "msg-" + uuid.NewString()
}

func writeProjectMessageStreamEvent(w http.ResponseWriter, flusher http.Flusher, event projectMessageStreamEvent) error {
	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	if _, err = fmt.Fprint(w, "event: ", event.Type, "\n"); err != nil {
		return err
	}
	if _, err = fmt.Fprint(w, "data: ", string(data), "\n\n"); err != nil {
		return err
	}
	flusher.Flush()
	return nil
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
