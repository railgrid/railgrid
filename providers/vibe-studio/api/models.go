// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0

package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	vibev1alpha1 "github.com/faroshq/provider-vibe-studio/apis/vibe/v1alpha1"
	"github.com/faroshq/provider-vibe-studio/client"
)

// Models are CRs (spec) + Secrets (the key). The API writes both as the
// caller; the picker in the studio assigns one per session, changeable at any
// time — the next turn uses it.

const modelSecretPrefix = "vibe-model-"

// modelView is the picker/menu DTO. It never carries the API key.
type modelView struct {
	Name        string `json:"name"`
	DisplayName string `json:"displayName,omitempty"`
	Provider    string `json:"provider,omitempty"`
	BaseURL     string `json:"baseURL,omitempty"`
	Model       string `json:"model"`
	Default     bool   `json:"default,omitempty"`
}

func modelViewOf(m vibev1alpha1.Model) modelView {
	return modelView{
		Name:        m.Name,
		DisplayName: firstNonEmpty(m.Spec.DisplayName, m.Name),
		Provider:    m.Spec.Provider,
		BaseURL:     m.Spec.BaseURL,
		Model:       m.Spec.Model,
		Default:     m.Annotations[vibev1alpha1.ModelDefaultAnnotation] == "true",
	}
}

// callerClient resolves a tenant-scoped client for the request, or writes the
// appropriate error and returns nil.
func (s *Server) callerClient(w http.ResponseWriter, r *http.Request) *client.Client {
	if _, err := s.scope(r); err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
		return nil
	}
	auth := callerAuthFromRequest(r)
	if s.gql == nil || auth.clusterID == "" || auth.token == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "no workspace identity on request"})
		return nil
	}
	tenantScope, err := s.gql.For(auth.clusterID, auth.token)
	if err != nil {
		writeError(w, err)
		return nil
	}
	cl := client.New(tenantScope)
	// First authenticated request from a workspace brings its Studio into
	// being, so the shared services are warm before the first project. At
	// most one extra read per process per workspace.
	go s.ensureStudio(context.WithoutCancel(r.Context()), cl, auth.clusterID)
	return cl
}

func (s *Server) handleListModels(w http.ResponseWriter, r *http.Request) {
	cl := s.callerClient(w, r)
	if cl == nil {
		return
	}
	models, err := cl.ListModels(r.Context())
	if errors.Is(err, client.ErrResourceUnavailable) {
		// The Models API isn't served in this workspace yet. Answer with an
		// empty list plus the fix, so the menu can say what to do instead of
		// failing.
		writeJSON(w, http.StatusOK, map[string]any{
			"items":     []modelView{},
			"available": false,
			"reason":    "The Models API isn’t installed in this workspace yet. Run the vibe-studio-init step, then disable and re-enable Vibe Studio for this workspace.",
		})
		return
	}
	if err != nil {
		writeError(w, err)
		return
	}
	items := make([]modelView, 0, len(models))
	for _, m := range models {
		items = append(items, modelViewOf(m))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "available": true})
}

// handleCreateModel writes the API key into a Secret and the rest into a
// Model CR. Marking a model default clears the flag on the others.
func (s *Server) handleCreateModel(w http.ResponseWriter, r *http.Request) {
	cl := s.callerClient(w, r)
	if cl == nil {
		return
	}
	var req struct {
		DisplayName string `json:"displayName"`
		Provider    string `json:"provider"`
		BaseURL     string `json:"baseURL"`
		Model       string `json:"model"`
		APIKey      string `json:"apiKey"`
		Default     bool   `json:"default"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json body"})
		return
	}
	if strings.TrimSpace(req.Model) == "" || strings.TrimSpace(req.APIKey) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "model and apiKey are required"})
		return
	}
	name := modelObjectName(firstNonEmpty(req.DisplayName, req.Model))
	secretName := modelSecretPrefix + name
	if err := cl.ApplySecret(r.Context(), llmSecretNamespace, secretName, map[string]string{"apiKey": req.APIKey}); err != nil {
		writeError(w, err)
		return
	}
	m := &vibev1alpha1.Model{}
	m.Name = name
	if req.Default {
		m.Annotations = map[string]string{vibev1alpha1.ModelDefaultAnnotation: "true"}
	}
	m.Spec = vibev1alpha1.ModelSpec{
		DisplayName: firstNonEmpty(req.DisplayName, req.Model),
		Provider:    firstNonEmpty(req.Provider, "openai-compatible"),
		BaseURL:     firstNonEmpty(req.BaseURL, "https://api.openai.com/v1"),
		Model:       strings.TrimSpace(req.Model),
		SecretRef: vibev1alpha1.ModelSecretReference{
			Name: secretName, Namespace: llmSecretNamespace, Key: "apiKey",
		},
	}
	created, err := cl.ApplyModel(r.Context(), m)
	if errors.Is(err, client.ErrResourceUnavailable) {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": "The Models API isn’t installed in this workspace yet. Run the vibe-studio-init step, then disable and re-enable Vibe Studio for this workspace.",
		})
		return
	}
	if err != nil {
		writeError(w, err)
		return
	}
	if req.Default {
		s.clearOtherDefaults(r.Context(), cl, name)
	}
	writeJSON(w, http.StatusCreated, modelViewOf(*created))
}

// handleDeleteModel removes the Model and its key Secret.
func (s *Server) handleDeleteModel(w http.ResponseWriter, r *http.Request) {
	cl := s.callerClient(w, r)
	if cl == nil {
		return
	}
	name := r.PathValue("name")
	if m, err := cl.GetModel(r.Context(), name); err == nil && m.Spec.SecretRef.Name != "" {
		ns := firstNonEmpty(m.Spec.SecretRef.Namespace, llmSecretNamespace)
		_ = cl.DeleteSecret(r.Context(), ns, m.Spec.SecretRef.Name)
	}
	if err := cl.DeleteModel(r.Context(), name); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleSetDefaultModel marks one model as the workspace default.
func (s *Server) handleSetDefaultModel(w http.ResponseWriter, r *http.Request) {
	cl := s.callerClient(w, r)
	if cl == nil {
		return
	}
	name := r.PathValue("name")
	m, err := cl.GetModel(r.Context(), name)
	if err != nil {
		writeError(w, err)
		return
	}
	if m.Annotations == nil {
		m.Annotations = map[string]string{}
	}
	m.Annotations[vibev1alpha1.ModelDefaultAnnotation] = "true"
	if _, err := cl.ApplyModel(r.Context(), m); err != nil {
		writeError(w, err)
		return
	}
	s.clearOtherDefaults(r.Context(), cl, name)
	writeJSON(w, http.StatusOK, modelViewOf(*m))
}

// handleGetSessionModel reports the session's assigned model ("" = default).
func (s *Server) handleGetSessionModel(w http.ResponseWriter, r *http.Request) {
	cl := s.callerClient(w, r)
	if cl == nil {
		return
	}
	sess, err := cl.GetSession(r.Context(), r.PathValue("id"))
	if err != nil {
		// No Session CR yet (older sessions) — the workspace default applies.
		writeJSON(w, http.StatusOK, map[string]string{"model": ""})
		return
	}
	name := ""
	if sess.Spec.ModelRef != nil {
		name = sess.Spec.ModelRef.Name
	}
	writeJSON(w, http.StatusOK, map[string]string{"model": name})
}

// handleSetSessionModel assigns (or clears) a session's model. This is the
// per-project choice: it writes Session.spec.modelRef, so the next turn uses
// it and kubectl shows it.
func (s *Server) handleSetSessionModel(w http.ResponseWriter, r *http.Request) {
	cl := s.callerClient(w, r)
	if cl == nil {
		return
	}
	var req struct {
		Model string `json:"model"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json body"})
		return
	}
	sess, err := cl.GetSession(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	if strings.TrimSpace(req.Model) == "" {
		sess.Spec.ModelRef = nil
	} else {
		sess.Spec.ModelRef = &vibev1alpha1.SessionModelRef{Name: req.Model}
	}
	if _, err := cl.ApplySession(r.Context(), sess); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) clearOtherDefaults(ctx context.Context, cl *client.Client, keep string) {
	models, err := cl.ListModels(ctx)
	if err != nil {
		return
	}
	for i := range models {
		m := models[i]
		if m.Name == keep || m.Annotations[vibev1alpha1.ModelDefaultAnnotation] != "true" {
			continue
		}
		delete(m.Annotations, vibev1alpha1.ModelDefaultAnnotation)
		if _, err := cl.ApplyModel(ctx, &m); err != nil {
			continue
		}
	}
}

var modelNameInvalid = regexp.MustCompile(`[^a-z0-9-]+`)

// modelObjectName slugifies a label into a DNS-safe CR name.
func modelObjectName(label string) string {
	n := modelNameInvalid.ReplaceAllString(strings.ToLower(label), "-")
	n = strings.Trim(n, "-")
	for strings.Contains(n, "--") {
		n = strings.ReplaceAll(n, "--", "-")
	}
	if len(n) > 50 {
		n = strings.Trim(n[:50], "-")
	}
	if n == "" {
		n = "model"
	}
	return n
}

// touchModelUsed stamps status.lastUsedAt (best effort, for the menu).
func touchModelUsed(ctx context.Context, cl *client.Client, name string, now metav1.Time) {
	m, err := cl.GetModel(ctx, name)
	if err != nil {
		return
	}
	m.Status.LastUsedAt = &now
	_, _ = cl.ApplyModel(ctx, m)
}

var _ = fmt.Sprintf
