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
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/mux"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	aiv1alpha1 "github.com/faroshq/provider-app-studio/apis/ai/v1alpha1"
	"github.com/faroshq/provider-app-studio/bindings"
	asclient "github.com/faroshq/provider-app-studio/client"
	"github.com/faroshq/provider-app-studio/store"
)

// Multipart adds room for MIME headers and form boundaries while the stored
// bytes remain bounded by AttachmentMaxBytes. The MaxBytesReader is still
// required because a multipart parser's memory limit is not a body limit.
const attachmentMultipartOverhead = 256 << 10

const projectAttachmentAdmissionMaxAttempts = 3

var (
	errProjectAttachmentScopeConflict    = errors.New("project attachment scope conflicts with authenticated project")
	errProjectAttachmentScopeConvergence = errors.New("project attachment scope could not be persisted")
)

type attachmentReceiptResponse struct {
	ID          string     `json:"id"`
	Filename    string     `json:"filename"`
	ContentType string     `json:"contentType"`
	SizeBytes   int64      `json:"sizeBytes"`
	SHA256      string     `json:"sha256"`
	CreatedAt   time.Time  `json:"createdAt"`
	Draft       bool       `json:"draft,omitempty"`
	ExpiresAt   *time.Time `json:"expiresAt,omitempty"`
}

func attachmentReceipt(attachment store.Attachment) attachmentReceiptResponse {
	return attachmentReceiptResponse{
		ID:          attachment.ID,
		Filename:    attachment.Filename,
		ContentType: attachment.ContentType,
		SizeBytes:   attachment.SizeBytes,
		SHA256:      attachment.SHA256,
		CreatedAt:   attachment.CreatedAt.UTC(),
		Draft:       attachment.Draft,
		ExpiresAt:   attachment.ExpiresAt,
	}
}

func (s *Server) projectAttachmentStore(w http.ResponseWriter) (store.AttachmentStore, bool) {
	if s != nil && s.attachments != nil {
		return s.attachments, true
	}
	if s != nil && s.store != nil {
		if attachmentStore, ok := s.store.(store.AttachmentStore); ok {
			return attachmentStore, true
		}
	}
	writeStatus(w, http.StatusNotImplemented, "NotImplemented", "project attachment store is not configured on this provider")
	return nil, false
}

// bindProjectAssistantAttachment is the turn-admission adapter. The caller
// must convert the HTTP receipt into store.AttachmentReceipt, including its
// CreatedAt value, before invoking this method. Binding verifies every
// immutable field under the authenticated actor and atomically promotes a
// draft to retained storage; repeated identical binds are safe.
func (s *Server) bindProjectAssistantAttachment(ctx context.Context, id identity, project *aiv1alpha1.Project, receipt store.AttachmentReceipt) (store.Attachment, error) {
	if s == nil || project == nil {
		return store.Attachment{}, store.ErrAttachmentNotFound
	}
	attachmentStore := s.attachments
	if attachmentStore == nil && s.store != nil {
		attachmentStore, _ = s.store.(store.AttachmentStore)
	}
	if attachmentStore == nil {
		return store.Attachment{}, fmt.Errorf("project attachment store is not configured")
	}
	return attachmentStore.BindAttachment(ctx, projectMessageScope(id.orgUUID, id.workspaceUUID, project), receipt, id.user)
}

// bindProjectAssistantContentPartAttachments is the durable admission boundary
// for attachment-bearing turns. It verifies the complete set before promoting
// any draft, so a stale or forged receipt cannot leave a partially-bound turn.
func (s *Server) bindProjectAssistantContentPartAttachments(ctx context.Context, id identity, project *aiv1alpha1.Project, parts []projectAssistantContentPart) error {
	receipts := projectAssistantContentPartAttachmentReceipts(parts)
	if len(receipts) == 0 {
		return nil
	}
	return s.bindProjectAssistantContentPartAttachmentsForRun(ctx, id, project, parts, "legacy:"+receipts[0].ID)
}

func projectAssistantContentPartAttachmentReceipts(parts []projectAssistantContentPart) []store.AttachmentReceipt {
	receipts := make([]store.AttachmentReceipt, 0)
	for _, part := range parts {
		if part.Type != projectAssistantContentPartAttachmentType || part.Attachment == nil {
			continue
		}
		receipts = append(receipts, store.AttachmentReceipt{
			ID: part.Attachment.ID, Filename: part.Attachment.Filename, ContentType: part.Attachment.ContentType,
			SizeBytes: part.Attachment.SizeBytes, SHA256: part.Attachment.SHA256, CreatedAt: part.Attachment.CreatedAt,
		})
	}
	return receipts
}

func (s *Server) verifyProjectAssistantContentPartAttachments(ctx context.Context, id identity, project *aiv1alpha1.Project, parts []projectAssistantContentPart) error {
	if s == nil || project == nil {
		return newValidationError("project attachments are unavailable")
	}
	attachmentStore := s.attachments
	if attachmentStore == nil && s.store != nil {
		attachmentStore, _ = s.store.(store.AttachmentStore)
	}
	receipts := projectAssistantContentPartAttachmentReceipts(parts)
	if len(receipts) == 0 {
		return nil
	}
	if attachmentStore == nil {
		return fmt.Errorf("project attachment store is not configured")
	}
	scope := projectMessageScope(id.orgUUID, id.workspaceUUID, project)
	for _, receipt := range receipts {
		if _, err := store.VerifyAttachmentReceipt(ctx, attachmentStore, scope, receipt, id.user); err != nil {
			return newValidationError("an attachment is unavailable or does not match its upload receipt")
		}
	}
	return nil
}

func (s *Server) bindProjectAssistantContentPartAttachmentsForRun(ctx context.Context, id identity, project *aiv1alpha1.Project, parts []projectAssistantContentPart, bindingID string) error {
	if err := s.verifyProjectAssistantContentPartAttachments(ctx, id, project, parts); err != nil {
		return err
	}
	receipts := projectAssistantContentPartAttachmentReceipts(parts)
	if len(receipts) == 0 {
		return nil
	}
	attachmentStore, ok := s.store.(store.AttachmentStore)
	if s.attachments != nil {
		attachmentStore, ok = s.attachments, true
	}
	if !ok {
		return fmt.Errorf("project attachment store is not configured")
	}
	if _, err := attachmentStore.BindAttachments(ctx, projectMessageScope(id.orgUUID, id.workspaceUUID, project), receipts, id.user, bindingID); err != nil {
		return fmt.Errorf("bind project attachments: %w", err)
	}
	return nil
}

func (s *Server) rollbackProjectAssistantAttachmentBinding(ctx context.Context, id identity, project *aiv1alpha1.Project, bindingID string) error {
	attachmentStore, ok := s.store.(store.AttachmentStore)
	if s.attachments != nil {
		attachmentStore, ok = s.attachments, true
	}
	if !ok {
		return nil
	}
	return attachmentStore.RollbackAttachmentBinding(ctx, projectMessageScope(id.orgUUID, id.workspaceUUID, project), bindingID)
}

// projectAssistantAttachmentReader returns the bounded model adapter for the
// same scoped store used by upload and turn admission. Keeping construction
// here prevents a future worker from accidentally using an unscoped backend.
func (s *Server) projectAssistantAttachmentReader() projectAssistantAttachmentReader {
	if s == nil {
		return nil
	}
	attachmentStore := s.attachments
	if attachmentStore == nil && s.store != nil {
		attachmentStore, _ = s.store.(store.AttachmentStore)
	}
	return newProjectAssistantStoreAttachmentReader(attachmentStore)
}

func (s *Server) listProjectAssistantAttachments(w http.ResponseWriter, r *http.Request) {
	_, id, project, ok := s.requireProjectWithClient(w, r)
	if !ok {
		return
	}
	attachmentStore, ok := s.projectAttachmentStore(w)
	if !ok {
		return
	}
	attachments, err := attachmentStore.ListAttachments(r.Context(), projectMessageScope(id.orgUUID, id.workspaceUUID, project))
	if err != nil {
		writeStatus(w, http.StatusInternalServerError, "InternalError", "list project attachments: "+err.Error())
		return
	}
	items := make([]attachmentReceiptResponse, 0, len(attachments))
	for _, attachment := range attachments {
		if attachment.ActorID != id.user {
			continue
		}
		items = append(items, attachmentReceipt(attachment))
	}
	writeJSON(w, http.StatusOK, ListResponse[attachmentReceiptResponse]{Items: items})
}

func (s *Server) createProjectAssistantAttachment(w http.ResponseWriter, r *http.Request) {
	c, id, project, ok := s.requireProjectWithClient(w, r)
	if !ok {
		return
	}
	if id.user == "" {
		writeStatus(w, http.StatusUnauthorized, "Unauthorized", "caller identity missing — the hub did not provide X-Faros-User")
		return
	}
	if project.DeletionTimestamp != nil {
		writeStatus(w, http.StatusConflict, "Conflict", "project is being deleted")
		return
	}
	attachmentStore, ok := s.projectAttachmentStore(w)
	if !ok {
		return
	}
	project, err := s.ensureProjectAttachmentAdmission(r.Context(), c, id, project)
	if err != nil {
		s.writeProjectAttachmentAdmissionError(w, err)
		return
	}

	maxRequestBytes := int64(store.AttachmentMaxBytes + attachmentMultipartOverhead)
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBytes)
	if err := r.ParseMultipartForm(maxRequestBytes); err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			writeStatus(w, http.StatusRequestEntityTooLarge, "RequestEntityTooLarge", "attachment request exceeds the upload limit")
		} else {
			writeStatus(w, http.StatusBadRequest, "BadRequest", "invalid multipart attachment request: "+err.Error())
		}
		return
	}
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}
	attachmentID, err := parseClientAttachmentID(r)
	if err != nil {
		writeStatus(w, http.StatusBadRequest, "BadRequest", err.Error())
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		writeStatus(w, http.StatusBadRequest, "BadRequest", "multipart field file is required")
		return
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, store.AttachmentMaxBytes+1))
	if err != nil {
		writeStatus(w, http.StatusBadRequest, "BadRequest", "read attachment: "+err.Error())
		return
	}
	if len(data) > store.AttachmentMaxBytes {
		writeStatus(w, http.StatusRequestEntityTooLarge, "RequestEntityTooLarge", fmt.Sprintf("attachment exceeds the %d-byte limit", store.AttachmentMaxBytes))
		return
	}
	contentType := detectProjectAttachmentContentType(header.Filename, header.Header.Get("Content-Type"), data)
	contentType, err = store.NormalizeAttachmentContentType(contentType)
	if err != nil {
		writeStatus(w, http.StatusBadRequest, "BadRequest", err.Error())
		return
	}
	if err := store.ValidateAttachmentContent(header.Filename, contentType, data); err != nil {
		if strings.Contains(err.Error(), "maximum") {
			writeStatus(w, http.StatusRequestEntityTooLarge, "RequestEntityTooLarge", err.Error())
		} else {
			writeStatus(w, http.StatusBadRequest, "BadRequest", err.Error())
		}
		return
	}
	digest := sha256.Sum256(data)
	now := time.Now().UTC()
	draft, err := parseAttachmentDraft(r)
	if err != nil {
		writeStatus(w, http.StatusBadRequest, "BadRequest", err.Error())
		return
	}
	expires := now.Add(s.attachmentRetention())
	expiresAt := &expires
	created, newlyCreated, err := store.CreateAttachmentIdempotent(r.Context(), attachmentStore, projectMessageScope(id.orgUUID, id.workspaceUUID, project), store.Attachment{
		ID:          attachmentID,
		ActorID:     id.user,
		Filename:    header.Filename,
		ContentType: contentType,
		SizeBytes:   int64(len(data)),
		SHA256:      hex.EncodeToString(digest[:]),
		Draft:       draft,
		CreatedAt:   now,
		ExpiresAt:   expiresAt,
		Data:        data,
	})
	if err != nil {
		if errors.Is(err, store.ErrAttachmentConflict) || errors.Is(err, store.ErrAttachmentQuotaExceeded) || errors.Is(err, store.ErrAttachmentProjectDeleted) {
			writeStatus(w, http.StatusConflict, "Conflict", err.Error())
		} else {
			writeStatus(w, http.StatusInternalServerError, "InternalError", "create project attachment: "+err.Error())
		}
		return
	}
	w.Header().Set("Location", "/api/projects/"+mux.Vars(r)["project"]+"/assistant/attachments/"+created.ID)
	if newlyCreated {
		writeJSON(w, http.StatusCreated, attachmentReceipt(created))
		return
	}
	writeJSON(w, http.StatusOK, attachmentReceipt(created))
}

// ensureProjectAttachmentAdmission makes the Project metadata that protects
// attachment cleanup authoritative before an upload can create storage. API
// project creation already supplies these fields, but Projects can also be
// created directly through KCP. In that case this boundary adopts the object
// to the authenticated tenant only when its existing annotations are empty or
// agree with the caller; a conflicting annotation is never overwritten.
//
// Update is retried only after a fresh read. This preserves concurrent spec or
// status changes and makes a conflict safe: if another writer installed the
// same identity/finalizer, the upload proceeds; if it installed a different
// identity, the upload is rejected. The returned object is always the
// re-read, persisted Project rather than the caller's stale copy.
func (s *Server) ensureProjectAttachmentAdmission(ctx context.Context, c *asclient.Client, id identity, project *aiv1alpha1.Project) (*aiv1alpha1.Project, error) {
	if c == nil || project == nil {
		return nil, fmt.Errorf("%w: project client is unavailable", errProjectAttachmentScopeConvergence)
	}
	org := strings.TrimSpace(id.orgUUID)
	workspace := strings.TrimSpace(id.workspaceUUID)
	name := strings.TrimSpace(project.Name)
	if org == "" || workspace == "" || name == "" || strings.TrimSpace(string(project.UID)) == "" {
		return nil, newValidationError("project attachment scope requires an authenticated organization, workspace, project name, and Project UID")
	}

	for attempt := 0; attempt < projectAttachmentAdmissionMaxAttempts; attempt++ {
		refreshed, err := c.Projects().Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return nil, fmt.Errorf("%w: re-read Project %q: %v", errProjectAttachmentScopeConvergence, name, err)
		}
		current := refreshed
		if err := validateProjectAttachmentAdmissionIdentity(current, org, workspace, name); err != nil {
			return nil, err
		}
		if projectAttachmentAdmissionReady(current, org, workspace) {
			return current, nil
		}

		next := current.DeepCopy()
		annotations := next.GetAnnotations()
		if annotations == nil {
			annotations = map[string]string{}
			next.SetAnnotations(annotations)
		}
		changed := false
		if annotations[bindings.OrgUUIDAnnotation] != org {
			annotations[bindings.OrgUUIDAnnotation] = org
			changed = true
		}
		if annotations[bindings.WorkspaceUUIDAnnotation] != workspace {
			annotations[bindings.WorkspaceUUIDAnnotation] = workspace
			changed = true
		}
		if !slices.Contains(next.Finalizers, store.AttachmentStorageFinalizer) {
			next.Finalizers = append(next.Finalizers, store.AttachmentStorageFinalizer)
			changed = true
		}
		if !changed {
			return current, nil
		}
		if _, err := c.Projects().Update(ctx, next, metav1.UpdateOptions{}); err != nil {
			if apierrors.IsConflict(err) {
				continue
			}
			return nil, fmt.Errorf("%w: update Project %q: %v", errProjectAttachmentScopeConvergence, name, err)
		}

		// Do not trust an Update response as proof that the cleanup guard is
		// durable. A concurrent writer/controller may have changed the object
		// between the update and this request's next storage operation.
		persisted, err := c.Projects().Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			if apierrors.IsConflict(err) {
				continue
			}
			return nil, fmt.Errorf("%w: verify Project %q after update: %v", errProjectAttachmentScopeConvergence, name, err)
		}
		current = persisted
		if err := validateProjectAttachmentAdmissionIdentity(current, org, workspace, name); err != nil {
			return nil, err
		}
		if projectAttachmentAdmissionReady(current, org, workspace) {
			return current, nil
		}
	}
	return nil, fmt.Errorf("%w: Project %q update retry budget exhausted", errProjectAttachmentScopeConflict, name)
}

func validateProjectAttachmentAdmissionIdentity(project *aiv1alpha1.Project, org, workspace, name string) error {
	if project == nil || strings.TrimSpace(project.Name) != name || strings.TrimSpace(string(project.UID)) == "" {
		return fmt.Errorf("%w: Project identity is incomplete", errProjectAttachmentScopeConvergence)
	}
	if !project.DeletionTimestamp.IsZero() {
		return fmt.Errorf("%w: Project %q is being deleted", errProjectAttachmentScopeConflict, name)
	}
	annotations := project.GetAnnotations()
	if annotated := strings.TrimSpace(annotations[bindings.OrgUUIDAnnotation]); annotated != "" && annotated != org {
		return fmt.Errorf("%w: organization annotation does not match the authenticated tenant", errProjectAttachmentScopeConflict)
	}
	if annotated := strings.TrimSpace(annotations[bindings.WorkspaceUUIDAnnotation]); annotated != "" && annotated != workspace {
		return fmt.Errorf("%w: workspace annotation does not match the authenticated tenant", errProjectAttachmentScopeConflict)
	}
	return nil
}

func projectAttachmentAdmissionReady(project *aiv1alpha1.Project, org, workspace string) bool {
	if project == nil || strings.TrimSpace(string(project.UID)) == "" || !slices.Contains(project.Finalizers, store.AttachmentStorageFinalizer) {
		return false
	}
	annotations := project.GetAnnotations()
	return strings.TrimSpace(annotations[bindings.OrgUUIDAnnotation]) == org &&
		strings.TrimSpace(annotations[bindings.WorkspaceUUIDAnnotation]) == workspace
}

func (s *Server) writeProjectAttachmentAdmissionError(w http.ResponseWriter, err error) {
	var validationErr *ValidationError
	switch {
	case errors.Is(err, errProjectAttachmentScopeConflict), apierrors.IsConflict(err):
		writeStatus(w, http.StatusConflict, "Conflict", "project attachment scope changed; refresh the project and retry")
	case errors.As(err, &validationErr):
		writeProjectError(w, err)
	default:
		writeStatus(w, http.StatusInternalServerError, "InternalError", "prepare project attachment scope: "+err.Error())
	}
}

func parseClientAttachmentID(r *http.Request) (string, error) {
	if r == nil || r.MultipartForm == nil {
		return "", fmt.Errorf("multipart attachment form is required")
	}
	values, supplied := r.MultipartForm.Value["clientAttachmentID"]
	if !supplied {
		return "att-" + uuid.NewString(), nil
	}
	if len(values) != 1 {
		return "", fmt.Errorf("clientAttachmentID must be supplied at most once")
	}
	id := strings.TrimSpace(values[0])
	if err := store.ValidateAttachmentID(id); err != nil {
		return "", fmt.Errorf("clientAttachmentID: %w", err)
	}
	return id, nil
}

func detectProjectAttachmentContentType(filename, declared string, data []byte) string {
	declared = strings.TrimSpace(declared)
	if declared != "" {
		if parsed, _, err := mime.ParseMediaType(declared); err == nil && parsed != "application/octet-stream" && parsed != "binary/octet-stream" {
			return declared
		}
	}
	if len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WEBP" {
		return "image/webp"
	}
	if detected := http.DetectContentType(data); detected != "application/octet-stream" {
		return detected
	}
	switch strings.ToLower(path.Ext(strings.TrimSpace(filename))) {
	case ".md":
		return "text/markdown"
	case ".txt":
		return "text/plain"
	default:
		return declared
	}
}

func parseAttachmentDraft(r *http.Request) (bool, error) {
	raw := strings.TrimSpace(r.FormValue("draft"))
	if raw == "" {
		raw = strings.TrimSpace(r.Header.Get("X-Faros-Attachment-Draft"))
	}
	if raw == "" {
		return true, nil
	}
	draft, err := strconv.ParseBool(raw)
	if err != nil {
		return false, fmt.Errorf("draft must be a boolean")
	}
	if !draft {
		return false, fmt.Errorf("permanent attachment upload is not supported; attachments are retained only by an admitted assistant turn")
	}
	return true, nil
}

func (s *Server) getProjectAssistantAttachment(w http.ResponseWriter, r *http.Request) {
	_, id, project, ok := s.requireProjectWithClient(w, r)
	if !ok {
		return
	}
	attachmentStore, ok := s.projectAttachmentStore(w)
	if !ok {
		return
	}
	attachment, err := attachmentStore.GetAttachment(r.Context(), projectMessageScope(id.orgUUID, id.workspaceUUID, project), mux.Vars(r)["attachment"])
	if err != nil {
		if errors.Is(err, store.ErrAttachmentNotFound) {
			writeStatus(w, http.StatusNotFound, "NotFound", "attachment not found")
		} else {
			writeStatus(w, http.StatusInternalServerError, "InternalError", "get project attachment: "+err.Error())
		}
		return
	}
	if attachment.ActorID != id.user {
		writeStatus(w, http.StatusNotFound, "NotFound", "attachment not found")
		return
	}
	w.Header().Set("Content-Type", attachment.ContentType)
	w.Header().Set("Content-Length", strconv.FormatInt(attachment.SizeBytes, 10))
	w.Header().Set("ETag", `"`+attachment.SHA256+`"`)
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": attachment.Filename}))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(attachment.Data)
}

func (s *Server) deleteProjectAssistantAttachment(w http.ResponseWriter, r *http.Request) {
	_, id, project, ok := s.requireProjectWithClient(w, r)
	if !ok {
		return
	}
	if id.user == "" {
		writeStatus(w, http.StatusUnauthorized, "Unauthorized", "caller identity missing — the hub did not provide X-Faros-User")
		return
	}
	attachmentStore, ok := s.projectAttachmentStore(w)
	if !ok {
		return
	}
	err := attachmentStore.DeleteAttachment(r.Context(), projectMessageScope(id.orgUUID, id.workspaceUUID, project), mux.Vars(r)["attachment"], id.user)
	switch {
	case err == nil:
		w.WriteHeader(http.StatusNoContent)
	case errors.Is(err, store.ErrAttachmentNotFound):
		writeStatus(w, http.StatusNotFound, "NotFound", "attachment not found")
	case errors.Is(err, store.ErrAttachmentForbidden):
		writeStatus(w, http.StatusForbidden, "Forbidden", "attachment belongs to another caller")
	case errors.Is(err, store.ErrAttachmentImmutable):
		writeStatus(w, http.StatusConflict, "Conflict", "attachment cannot be deleted")
	default:
		writeStatus(w, http.StatusInternalServerError, "InternalError", "delete project attachment: "+err.Error())
	}
}
