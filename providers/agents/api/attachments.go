// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

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
	"strings"
	"time"

	"github.com/google/uuid"
	agentsv1alpha1 "github.com/railgrid/provider-agents/apis/v1alpha1"
	"github.com/railgrid/provider-agents/store"
	"github.com/railgrid/provider-sdk/attachments"
	"github.com/railgrid/provider-sdk/dataplane"
)

const agentAttachmentMultipartOverhead = 256 << 10

type agentAttachmentListResponse struct {
	Items             []store.AgentAttachmentReceipt `json:"items"`
	InputCapabilities *agentInputCapabilities        `json:"inputCapabilities,omitempty"`
}

func (s *Server) verifyAgentAttachmentReceipts(ctx context.Context, scope store.Scope, agentUID, sessionID, owner string, receipts []store.AgentAttachmentReceipt) error {
	if len(receipts) == 0 {
		return nil
	}
	attachmentStore, ok := s.store.(store.AgentAttachmentStore)
	if !ok {
		return errors.New("agent attachment storage is not configured")
	}
	if strings.TrimSpace(agentUID) == "" || strings.TrimSpace(sessionID) == "" || strings.TrimSpace(owner) == "" {
		return errors.New("agent attachment identity is incomplete")
	}
	for _, receipt := range receipts {
		item, err := attachmentStore.GetAgentAttachment(ctx, scope, agentUID, sessionID, owner, receipt.ID)
		if err != nil || !item.Draft || !attachments.ReceiptsMatch(receipt, item.Receipt()) {
			return store.ErrAgentAttachmentReceipt
		}
	}
	return nil
}

func (s *Server) agentAttachmentStore(w http.ResponseWriter) (store.AgentAttachmentStore, bool) {
	attachmentStore, ok := s.store.(store.AgentAttachmentStore)
	if !ok {
		writeStatus(w, http.StatusNotImplemented, "NotImplemented", "Agent attachment storage is not configured")
		return nil, false
	}
	return attachmentStore, true
}

// agentAttachments serves actor and session scoped upload drafts. The only
// bytes returned by this verb are the original upload; the immutable receipt
// remains the six-field SDK value used by attended chat.
func (s *Server) agentAttachments(w http.ResponseWriter, r *http.Request) {
	c, id, ok := s.requireClient(w, r)
	if !ok {
		return
	}
	gate, ok := gateFrom(r.Context())
	if !ok || gate.object == nil || gate.object.GetUID() == "" {
		writeStatus(w, http.StatusInternalServerError, "AgentIdentityUnavailable", "the gated Agent has no stable UID")
		return
	}
	if strings.TrimSpace(id.user) == "" {
		writeStatus(w, http.StatusUnauthorized, "Unauthorized", "an authenticated caller principal is required for Agent attachments")
		return
	}
	route, ok := dataplane.RouteFrom(r.Context())
	if !ok || strings.Contains(route.Tail, "/") {
		dataplane.WriteError(w, dataplane.ErrBadPath)
		return
	}
	attachmentStore, ok := s.agentAttachmentStore(w)
	if !ok {
		return
	}
	scope := id.scope(r.PathValue("name"))
	agentUID := string(gate.object.GetUID())
	tail := route.Tail

	if tail == "" {
		switch r.Method {
		case http.MethodGet:
			sessionID, valid := oneQueryValue(r, "sessionID")
			if !valid || !validAttachmentSessionID(sessionID) {
				writeStatus(w, http.StatusBadRequest, "BadRequest", "one valid sessionID query value is required")
				return
			}
			items, err := attachmentStore.ListAgentAttachments(r.Context(), scope, agentUID, sessionID, id.user, 500)
			if err != nil {
				writeStatus(w, http.StatusInternalServerError, "InternalError", "could not list Agent attachments")
				return
			}
			receipts := make([]store.AgentAttachmentReceipt, 0, len(items))
			for _, item := range items {
				receipts = append(receipts, item.Receipt())
			}
			response := agentAttachmentListResponse{Items: receipts}
			if agent, err := fromU[agentsv1alpha1.Agent](gate.object); err == nil {
				capabilities := s.agentConversationCapabilities(r.Context(), c, agent, id)
				response.InputCapabilities = &capabilities
			}
			writeJSON(w, http.StatusOK, response)
		case http.MethodPost:
			s.uploadAgentAttachment(w, r, attachmentStore, scope, agentUID, id)
		default:
			w.Header().Set("Allow", "GET, POST")
			writeStatus(w, http.StatusMethodNotAllowed, "MethodNotAllowed", "attachment collection answers GET and POST")
		}
		return
	}
	if err := attachments.ValidateAttachmentID(tail); err != nil {
		dataplane.WriteError(w, dataplane.ErrBadPath)
		return
	}
	sessionID, valid := oneQueryValue(r, "sessionID")
	if !valid || !validAttachmentSessionID(sessionID) {
		writeStatus(w, http.StatusBadRequest, "BadRequest", "one valid sessionID query value is required")
		return
	}
	switch r.Method {
	case http.MethodGet:
		item, err := attachmentStore.GetAgentAttachment(r.Context(), scope, agentUID, sessionID, id.user, tail)
		if err != nil {
			writeAgentAttachmentError(w, err)
			return
		}
		w.Header().Set("Cache-Control", "private, no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Type", item.ContentType)
		w.Header().Set("Content-Length", fmt.Sprint(len(item.Data)))
		w.Header().Set("ETag", `"`+item.SHA256+`"`)
		if value := mime.FormatMediaType("attachment", map[string]string{"filename": item.Filename}); value != "" {
			w.Header().Set("Content-Disposition", value)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(item.Data)
	case http.MethodDelete:
		if err := attachmentStore.DeleteAgentAttachment(r.Context(), scope, agentUID, sessionID, id.user, tail); err != nil {
			writeAgentAttachmentError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		w.Header().Set("Allow", "GET, DELETE")
		writeStatus(w, http.StatusMethodNotAllowed, "MethodNotAllowed", "an attachment detail answers GET and DELETE")
	}
}

func (s *Server) uploadAgentAttachment(w http.ResponseWriter, r *http.Request, attachmentStore store.AgentAttachmentStore, scope store.Scope, agentUID string, id identity) {
	maxBodyBytes := int64(attachments.AttachmentMaxBytes + agentAttachmentMultipartOverhead)
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			writeStatus(w, http.StatusRequestEntityTooLarge, "RequestEntityTooLarge", "attachment request exceeds the upload limit")
		} else {
			writeStatus(w, http.StatusBadRequest, "BadRequest", "invalid multipart attachment request")
		}
		return
	}
	if r.MultipartForm != nil {
		defer func() { _ = r.MultipartForm.RemoveAll() }()
	}
	sessionID, valid := oneFormValue(r, "sessionID")
	if !valid || !validAttachmentSessionID(sessionID) {
		writeStatus(w, http.StatusBadRequest, "BadRequest", "one valid sessionID multipart field is required")
		return
	}
	if raw, supplied := r.MultipartForm.Value["draft"]; supplied {
		if len(raw) != 1 || strings.TrimSpace(raw[0]) != "true" {
			writeStatus(w, http.StatusBadRequest, "BadRequest", "draft must be true; attachments are retained only by an admitted chat run")
			return
		}
	}
	if header := strings.TrimSpace(r.Header.Get("X-Railgrid-Attachment-Draft")); header != "" && header != "true" {
		writeStatus(w, http.StatusBadRequest, "BadRequest", "draft must be true; attachments are retained only by an admitted chat run")
		return
	}
	attachmentID := ""
	if values, supplied := r.MultipartForm.Value["clientAttachmentID"]; supplied {
		if len(values) != 1 {
			writeStatus(w, http.StatusBadRequest, "BadRequest", "clientAttachmentID must be supplied at most once")
			return
		}
		attachmentID = strings.TrimSpace(values[0])
	} else {
		attachmentID = "att-" + uuid.NewString()
	}
	if err := attachments.ValidateAttachmentID(attachmentID); err != nil {
		writeStatus(w, http.StatusBadRequest, "BadRequest", "clientAttachmentID is invalid")
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		writeStatus(w, http.StatusBadRequest, "BadRequest", "multipart field file is required")
		return
	}
	defer func() { _ = file.Close() }()
	data, err := io.ReadAll(io.LimitReader(file, attachments.AttachmentMaxBytes+1))
	if err != nil {
		writeStatus(w, http.StatusBadRequest, "BadRequest", "could not read attachment")
		return
	}
	if len(data) > attachments.AttachmentMaxBytes {
		writeStatus(w, http.StatusRequestEntityTooLarge, "RequestEntityTooLarge", "attachment exceeds the 25 MiB upload limit")
		return
	}
	contentType := detectAgentAttachmentContentType(header.Filename, header.Header.Get("Content-Type"), data)
	if err := attachments.ValidateAttachmentContent(header.Filename, contentType, data); err != nil {
		if strings.Contains(err.Error(), "maximum") {
			writeStatus(w, http.StatusRequestEntityTooLarge, "RequestEntityTooLarge", err.Error())
		} else {
			writeStatus(w, http.StatusBadRequest, "BadRequest", err.Error())
		}
		return
	}
	digest := sha256.Sum256(data)
	now := time.Now().UTC()
	expiresAt := now.Add(attachments.DefaultDraftRetention)
	item, created, err := attachmentStore.CreateAgentAttachment(r.Context(), scope, store.AgentAttachment{
		ID: attachmentID, AgentName: scope.AgentName, AgentUID: agentUID, SessionID: sessionID, ActorID: id.user,
		Filename: header.Filename, ContentType: contentType, SizeBytes: int64(len(data)), SHA256: hex.EncodeToString(digest[:]),
		Draft: true, CreatedAt: now, ExpiresAt: &expiresAt, Data: data,
	})
	if err != nil {
		writeAgentAttachmentError(w, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	w.Header().Set("Cache-Control", "private, no-store")
	writeJSON(w, status, item.Receipt())
}

func oneQueryValue(r *http.Request, key string) (string, bool) {
	values, exists := r.URL.Query()[key]
	if !exists || len(values) != 1 {
		return "", false
	}
	return strings.TrimSpace(values[0]), true
}

func oneFormValue(r *http.Request, key string) (string, bool) {
	if r.MultipartForm == nil {
		return "", false
	}
	values, exists := r.MultipartForm.Value[key]
	if !exists {
		return "", false
	}
	if len(values) != 1 {
		return "", false
	}
	return strings.TrimSpace(values[0]), true
}

func validAttachmentSessionID(sessionID string) bool {
	return sessionID != "" && len(sessionID) <= 128 && strings.TrimSpace(sessionID) == sessionID && !strings.ContainsAny(sessionID, "/\\\x00\r\n")
}

func detectAgentAttachmentContentType(filename, declared string, data []byte) string {
	declared = strings.TrimSpace(declared)
	candidate := ""
	if declared != "" {
		if parsed, _, err := mime.ParseMediaType(declared); err == nil && parsed != "application/octet-stream" && parsed != "binary/octet-stream" {
			candidate = declared
		}
	}
	if candidate == "" {
		if len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WEBP" {
			candidate = "image/webp"
		} else if detected := http.DetectContentType(data); detected != "application/octet-stream" {
			candidate = detected
		} else {
			candidate = mime.TypeByExtension(path.Ext(strings.TrimSpace(filename)))
		}
	}
	normalized, err := attachments.NormalizeAttachmentContentType(candidate)
	if err != nil {
		return "application/octet-stream"
	}
	ext := strings.ToLower(path.Ext(strings.TrimSpace(filename)))
	if attachments.AttachmentKind(normalized) == attachments.AttachmentKindText && ext != ".txt" && ext != ".md" {
		if byExtension, extErr := attachments.NormalizeAttachmentContentType(mime.TypeByExtension(ext)); extErr == nil && attachments.AttachmentKind(byExtension) == attachments.AttachmentKindFile {
			return byExtension
		}
		return "application/octet-stream"
	}
	return normalized
}

func writeAgentAttachmentError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrAgentAttachmentNotFound), errors.Is(err, store.ErrAgentAttachmentForbidden):
		writeStatus(w, http.StatusNotFound, "NotFound", "attachment not found")
	case errors.Is(err, store.ErrAgentAttachmentImmutable):
		writeStatus(w, http.StatusConflict, "Conflict", "attachment is immutable after its chat run is accepted")
	case errors.Is(err, store.ErrAgentAttachmentConflict), errors.Is(err, store.ErrAgentAttachmentQuota), errors.Is(err, store.ErrAgentAttachmentDraftExpired), errors.Is(err, store.ErrAgentAttachmentReceipt):
		writeStatus(w, http.StatusConflict, "Conflict", err.Error())
	default:
		writeStatus(w, http.StatusInternalServerError, "InternalError", "Agent attachment operation failed")
	}
}
