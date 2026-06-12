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

package restapi

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

	aiv1alpha1 "github.com/faroshq/faros-kedge/apis/ai/v1alpha1"
	kedgeclient "github.com/faroshq/faros-kedge/pkg/client"
	projectstore "github.com/faroshq/faros-kedge/providers/projects/store"
)

type CreateProjectRequest struct {
	Name        string `json:"name,omitempty"`
	DisplayName string `json:"displayName"`
	Description string `json:"description,omitempty"`
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
	Memory      aiv1alpha1.ProjectMemory `json:"memory,omitempty"`
	CreatedAt   time.Time                `json:"createdAt"`
	UpdatedAt   *time.Time               `json:"updatedAt,omitempty"`
}

type ProjectMessagesResponse struct {
	Items      []aiv1alpha1.ProjectMessage `json:"items"`
	NextCursor string                      `json:"nextCursor,omitempty"`
}

type projectMessageStreamEvent struct {
	Type               string `json:"type"`
	AssistantMessageID string `json:"assistantMessageID,omitempty"`
	Content            string `json:"content,omitempty"`
	Error              string `json:"error,omitempty"`
}

const projectAPIInitializingMessage = "App Studio is still initializing for this workspace. Try again shortly."
const projectMessageMetadataStatus = "status"
const projectMessageStatusInterrupted = "interrupted"
const projectMessagePersistTimeout = 5 * time.Second

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

func (h *Handler) listProjects(w http.ResponseWriter, r *http.Request) {
	c, ok := h.requireProjectClient(w, r)
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
		out = append(out, projectView(&list.Items[i]))
	}
	writeJSON(w, http.StatusOK, ListResponse[ProjectView]{Items: out})
}

func (h *Handler) createProject(w http.ResponseWriter, r *http.Request) {
	c, ok := h.requireProjectClient(w, r)
	if !ok {
		return
	}
	var req CreateProjectRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	req.DisplayName = strings.TrimSpace(req.DisplayName)
	req.Description = strings.TrimSpace(req.Description)
	if req.DisplayName == "" {
		writeProjectError(w, newValidationError("displayName is required"))
		return
	}
	name, err := h.projectName(r.Context(), c, req.Name, req.DisplayName)
	if err != nil {
		writeProjectError(w, err)
		return
	}
	now := metav1.Now()
	p := &aiv1alpha1.Project{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: aiv1alpha1.ProjectSpec{
			DisplayName: req.DisplayName,
			Description: req.Description,
			Memory:      emptyProjectMemory(),
		},
		Status: aiv1alpha1.ProjectStatus{
			Phase:     aiv1alpha1.ProjectPhaseReady,
			UpdatedAt: &now,
		},
	}
	created, err := c.Projects().Create(r.Context(), p, metav1.CreateOptions{})
	if err != nil {
		writeProjectError(w, err)
		return
	}
	updated, err := h.touchProjectStatus(r.Context(), c, created)
	if err != nil {
		writeProjectError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, projectView(updated))
}

func (h *Handler) getProject(w http.ResponseWriter, r *http.Request) {
	p, ok := h.requireProject(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, projectView(p))
}

func (h *Handler) patchProject(w http.ResponseWriter, r *http.Request) {
	c, p, ok := h.requireProjectWithClient(w, r)
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
	updated, err = h.touchProjectStatus(r.Context(), c, updated)
	if err != nil {
		writeProjectError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, projectView(updated))
}

func (h *Handler) deleteProject(w http.ResponseWriter, r *http.Request) {
	c, ok := h.requireProjectClient(w, r)
	if !ok {
		return
	}
	name := mux.Vars(r)["project"]
	if h.mgr.projectMessages != nil {
		tc, ok := h.requireTenantContext(w, r, true, false)
		if !ok {
			return
		}
		if err := h.mgr.projectMessages.DeleteProjectMessages(r.Context(), projectMessageScope(tc.OrgUUID, tc.WorkspaceUUID, name)); err != nil {
			writeStatus(w, http.StatusInternalServerError, "InternalError", "deleting project messages: "+err.Error())
			return
		}
	}
	if err := c.Projects().Delete(r.Context(), name, metav1.DeleteOptions{}); err != nil {
		writeProjectError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) listProjectMessages(w http.ResponseWriter, r *http.Request) {
	c, p, ok := h.requireProjectWithClient(w, r)
	if !ok {
		return
	}
	store, ok := h.requireProjectMessagesStore(w)
	if !ok {
		return
	}
	tc, ok := h.requireTenantContext(w, r, true, false)
	if !ok {
		return
	}
	limit := listLimitFromRequest(r)
	cursor := strings.TrimSpace(r.URL.Query().Get("cursor"))
	if err := h.migrateLegacyProjectMessages(r.Context(), c, tc.OrgUUID, tc.WorkspaceUUID, p); err != nil {
		writeProjectError(w, err)
		return
	}
	page, err := store.ListMessages(r.Context(), projectMessageScope(tc.OrgUUID, tc.WorkspaceUUID, p.Name), limit, cursor)
	if err != nil {
		writeProjectError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, ProjectMessagesResponse{
		Items:      projectMessagesToAPI(page.Items),
		NextCursor: page.NextCursor,
	})
}

func (h *Handler) createProjectMessageStream(w http.ResponseWriter, r *http.Request) {
	c, p, ok := h.requireProjectWithClient(w, r)
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

	store, ok := h.requireProjectMessagesStore(w)
	if !ok {
		return
	}
	tc, ok := h.requireTenantContext(w, r, true, false)
	if !ok {
		return
	}
	if err := h.migrateLegacyProjectMessages(r.Context(), c, tc.OrgUUID, tc.WorkspaceUUID, p); err != nil {
		writeProjectError(w, err)
		return
	}

	now := metav1.Now()
	userID := newMessageID()
	userMsg := projectstore.Message{
		ID:        userID,
		Role:      aiv1alpha1.ProjectMessageRoleUser,
		Content:   req.Content,
		CreatedAt: now.Time.UTC(),
		UpdatedAt: now.Time.UTC(),
	}
	if err := store.AppendMessage(r.Context(), projectMessageScope(tc.OrgUUID, tc.WorkspaceUUID, p.Name), userMsg); err != nil {
		writeProjectError(w, err)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeStatus(w, http.StatusInternalServerError, "InternalError", "streaming unsupported")
		return
	}

	assistantID := newMessageID()
	assistantContent := &strings.Builder{}
	var streamErr error
	scope := projectMessageScope(tc.OrgUUID, tc.WorkspaceUUID, p.Name)

	streamChunk := func(chunk string) {
		if streamErr != nil {
			return
		}
		if chunk == "" {
			return
		}
		assistantContent.WriteString(chunk)
		streamErr = writeProjectMessageStreamEvent(w, flusher, projectMessageStreamEvent{
			Type:               "chunk",
			AssistantMessageID: assistantID,
			Content:            chunk,
		})
	}

	reply, err := h.generateProjectAssistantStream(r, c, p, streamChunk)
	if err != nil {
		if shouldPersistInterruptedProjectAssistant(r.Context(), err, streamErr, assistantContent.String()) {
			persistErr := appendInterruptedProjectAssistantMessage(r.Context(), store, scope, assistantID, assistantContent.String())
			if persistErr != nil && streamErr == nil {
				_ = writeProjectMessageStreamEvent(w, flusher, projectMessageStreamEvent{
					Type:  "error",
					Error: "assistant persistence failed: " + persistErr.Error(),
				})
			}
			return
		}
		if streamErr != nil {
			_ = appendInterruptedProjectAssistantMessage(r.Context(), store, scope, assistantID, assistantContent.String())
			return
		}
		if errors.Is(err, errProjectLLMNotConfigured) {
			_ = writeProjectMessageStreamEvent(w, flusher, projectMessageStreamEvent{
				Type:  "error",
				Error: err.Error(),
			})
			return
		}
		_ = writeProjectMessageStreamEvent(w, flusher, projectMessageStreamEvent{
			Type:  "error",
			Error: "assistant generation failed: " + err.Error(),
		})
		return
	}
	if streamErr != nil {
		assistantReply := projectAssistantStoredContent(reply, assistantContent.String())
		_ = appendInterruptedProjectAssistantMessage(r.Context(), store, scope, assistantID, assistantReply)
		return
	}
	assistantReply := projectAssistantStoredContent(reply, assistantContent.String())
	if strings.TrimSpace(assistantReply) == "" {
		_ = writeProjectMessageStreamEvent(w, flusher, projectMessageStreamEvent{
			Type:  "error",
			Error: "assistant generation returned an empty response",
		})
		return
	}

	if err := appendProjectAssistantMessage(r.Context(), store, scope, assistantID, assistantReply, nil); err != nil {
		_ = writeProjectMessageStreamEvent(w, flusher, projectMessageStreamEvent{
			Type:  "error",
			Error: "assistant persistence failed: " + err.Error(),
		})
		return
	}
	if err := writeProjectMessageStreamEvent(w, flusher, projectMessageStreamEvent{
		Type:               "done",
		AssistantMessageID: assistantID,
	}); err != nil {
		writeStatus(w, http.StatusInternalServerError, "InternalError", err.Error())
		return
	}
}

func projectAssistantStoredContent(reply, streamed string) string {
	if strings.TrimSpace(streamed) != "" {
		return streamed
	}
	return reply
}

func shouldPersistInterruptedProjectAssistant(ctx context.Context, err, streamErr error, streamed string) bool {
	return strings.TrimSpace(streamed) != "" && (streamErr != nil || errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled))
}

func detachedProjectPersistenceContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), projectMessagePersistTimeout)
}

func appendInterruptedProjectAssistantMessage(ctx context.Context, store projectstore.Store, scope projectstore.Scope, id, content string) error {
	if strings.TrimSpace(content) == "" {
		return nil
	}
	persistCtx, cancel := detachedProjectPersistenceContext(ctx)
	defer cancel()
	return appendProjectAssistantMessage(persistCtx, store, scope, id, content, map[string]any{
		projectMessageMetadataStatus: projectMessageStatusInterrupted,
	})
}

func appendProjectAssistantMessage(ctx context.Context, store projectstore.Store, scope projectstore.Scope, id, content string, metadata map[string]any) error {
	now := time.Now().UTC()
	return store.AppendMessage(ctx, scope, projectstore.Message{
		ID:        id,
		Role:      aiv1alpha1.ProjectMessageRoleAssistant,
		Content:   content,
		Metadata:  cloneAnyMap(metadata),
		CreatedAt: now,
		UpdatedAt: now,
	})
}

func (h *Handler) getProjectMemory(w http.ResponseWriter, r *http.Request) {
	p, ok := h.requireProject(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, p.Spec.Memory)
}

func (h *Handler) patchProjectMemory(w http.ResponseWriter, r *http.Request) {
	c, p, ok := h.requireProjectWithClient(w, r)
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
	updated, err = h.touchProjectStatus(r.Context(), c, updated)
	if err != nil {
		writeProjectError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, updated.Spec.Memory)
}

func (h *Handler) requireProject(w http.ResponseWriter, r *http.Request) (*aiv1alpha1.Project, bool) {
	_, p, ok := h.requireProjectWithClient(w, r)
	return p, ok
}

func (h *Handler) requireProjectWithClient(w http.ResponseWriter, r *http.Request) (*kedgeclient.Client, *aiv1alpha1.Project, bool) {
	c, ok := h.requireProjectClient(w, r)
	if !ok {
		return nil, nil, false
	}
	name := mux.Vars(r)["project"]
	p, err := c.Projects().Get(r.Context(), name, metav1.GetOptions{})
	if err != nil {
		writeProjectError(w, err)
		return nil, nil, false
	}
	return c, p, true
}

func (h *Handler) requireProjectClient(w http.ResponseWriter, r *http.Request) (*kedgeclient.Client, bool) {
	tc, ok := h.requireTenantContext(w, r, true, false)
	if !ok {
		return nil, false
	}
	if h.mgr.projectClients == nil {
		writeStatus(w, http.StatusNotImplemented, "NotImplemented", "project client factory not wired on this hub")
		return nil, false
	}
	c, err := h.mgr.projectClients(r.Context(), tc.OrgUUID, tc.WorkspaceUUID)
	if err != nil {
		writeStatus(w, http.StatusInternalServerError, "InternalError", "creating project client: "+err.Error())
		return nil, false
	}
	return c, true
}

func (h *Handler) projectName(ctx context.Context, c *kedgeclient.Client, requested, displayName string) (string, error) {
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

func (h *Handler) touchProjectStatus(ctx context.Context, c *kedgeclient.Client, p *aiv1alpha1.Project) (*aiv1alpha1.Project, error) {
	now := metav1.Now()
	p.Status.Phase = aiv1alpha1.ProjectPhaseReady
	p.Status.UpdatedAt = &now
	return c.Projects().UpdateStatus(ctx, p, metav1.UpdateOptions{})
}

func projectView(p *aiv1alpha1.Project) ProjectView {
	view := ProjectView{
		Name:        p.Name,
		DisplayName: p.Spec.DisplayName,
		Description: p.Spec.Description,
		Phase:       p.Status.Phase,
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

func slugifyProjectName(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = invalidProjectNameChars.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	for strings.Contains(s, "--") {
		s = strings.ReplaceAll(s, "--", "-")
	}
	if len(s) > 63 {
		s = strings.Trim(s[:63], "-")
	}
	return s
}
