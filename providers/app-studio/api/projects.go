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
	Error              string                      `json:"error,omitempty"`
	ToolCall           *projectToolCallStreamEvent `json:"toolCall,omitempty"`
}

type projectToolCallStreamEvent struct {
	ID        string `json:"id"`
	Name      string `json:"name,omitempty"`
	Status    string `json:"status"`
	Arguments string `json:"arguments,omitempty"`
	Summary   string `json:"summary,omitempty"`
	Error     string `json:"error,omitempty"`
}

const projectAPIInitializingMessage = "App Studio is still initializing for this workspace. Try again shortly."
const projectMessageMetadataStatus = "status"
const projectMessageMetadataToolCalls = "toolCalls"
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
	req.DisplayName = strings.TrimSpace(req.DisplayName)
	req.Description = strings.TrimSpace(req.Description)
	req.Prompt = strings.TrimSpace(req.Prompt)
	req.ConnectionRef = strings.TrimSpace(req.ConnectionRef)
	repoBase := slugifyProjectName(req.DisplayName)
	if req.Prompt != "" {
		naming, err := s.generateProjectNaming(r.Context(), c, req.Prompt)
		if err != nil {
			writeProjectError(w, err)
			return
		}
		req.DisplayName = naming.DisplayName
		repoBase = naming.RepositoryName
	}
	if req.DisplayName == "" {
		writeProjectError(w, newValidationError("displayName is required"))
		return
	}
	name, err := projectName(r.Context(), c, req.Name, req.DisplayName)
	if err != nil {
		writeProjectError(w, err)
		return
	}
	repoPlan, err := s.prepareProjectRepository(r.Context(), c, req.ConnectionRef, repoBase, req.DisplayName, req.Description)
	if err != nil {
		writeProjectError(w, err)
		return
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
	created, err := c.Projects().Create(r.Context(), p, metav1.CreateOptions{})
	if err != nil {
		writeProjectError(w, err)
		return
	}
	if err := s.createProjectRepository(r.Context(), c, created.Name, repoPlan); err != nil {
		_ = c.Projects().Delete(r.Context(), created.Name, metav1.DeleteOptions{})
		writeProjectError(w, err)
		return
	}
	updated, err := touchProjectStatus(r.Context(), c, created)
	if err != nil {
		writeProjectError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, projectView(r.Context(), c, updated))
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
	if s.store != nil {
		if err := s.store.DeleteProjectMessages(r.Context(), projectMessageScope(id.orgUUID, id.workspaceUUID, name)); err != nil {
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

	now := metav1.Now()
	userID := newMessageID()
	userMsg := store.Message{
		ID:        userID,
		Role:      aiv1alpha1.ProjectMessageRoleUser,
		Content:   req.Content,
		CreatedAt: now.UTC(),
		UpdatedAt: now.UTC(),
	}
	if err := msgStore.AppendMessage(r.Context(), projectMessageScope(id.orgUUID, id.workspaceUUID, p.Name), userMsg); err != nil {
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
	var streamedToolCalls []projectToolCallStreamEvent
	scope := projectMessageScope(id.orgUUID, id.workspaceUUID, p.Name)

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
	streamToolCall := func(toolCall projectToolCallStreamEvent) {
		if streamErr != nil {
			return
		}
		if toolCall.ID == "" || toolCall.Status == "" {
			return
		}
		streamedToolCalls = upsertProjectToolCallStreamEvent(streamedToolCalls, toolCall)
		streamErr = writeProjectMessageStreamEvent(w, flusher, projectMessageStreamEvent{
			Type:               "tool_call",
			AssistantMessageID: assistantID,
			ToolCall:           &toolCall,
		})
	}

	reply, err := s.generateProjectAssistantStream(r, id, c, p, projectAssistantStreamCallbacks{
		OnChunk:    streamChunk,
		OnToolCall: streamToolCall,
	})
	if err != nil {
		if shouldPersistInterruptedProjectAssistant(r.Context(), err, streamErr, assistantContent.String()) {
			persistErr := appendInterruptedProjectAssistantMessage(r.Context(), msgStore, scope, assistantID, assistantContent.String())
			if persistErr != nil && streamErr == nil {
				_ = writeProjectMessageStreamEvent(w, flusher, projectMessageStreamEvent{
					Type:  "error",
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
		_ = appendInterruptedProjectAssistantMessage(r.Context(), msgStore, scope, assistantID, assistantReply)
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

	if err := appendProjectAssistantMessage(r.Context(), msgStore, scope, assistantID, assistantReply, projectAssistantMessageMetadata("", streamedToolCalls)); err != nil {
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

func projectAssistantMessageMetadata(status string, toolCalls []projectToolCallStreamEvent) map[string]any {
	metadata := map[string]any{}
	if status != "" {
		metadata[projectMessageMetadataStatus] = status
	}
	if len(toolCalls) > 0 {
		metadata[projectMessageMetadataToolCalls] = toolCalls
	}
	if len(metadata) == 0 {
		return nil
	}
	return metadata
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
	p.Status.Phase = aiv1alpha1.ProjectPhaseReady
	p.Status.UpdatedAt = &now
	return c.Projects().UpdateStatus(ctx, p, metav1.UpdateOptions{})
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
