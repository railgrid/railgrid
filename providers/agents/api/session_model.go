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
	"encoding/json"
	"net/http"
	"strings"

	agentsv1alpha1 "github.com/railgrid/provider-agents/apis/v1alpha1"
	"github.com/railgrid/provider-agents/llm"
	"github.com/railgrid/provider-agents/store"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// sessionModelSettings uses the existing per-agent session subresource. It
// changes conversation preferences only; the Agent and credential CRs are never
// patched. The active run pins its own immutable copy when it is admitted.
func (s *Server) sessionModelSettings(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodDelete {
		s.deleteSession(w, r)
		return
	}
	c, id, ok := s.requireClient(w, r)
	if !ok {
		return
	}
	name, session := r.PathValue("name"), r.PathValue("tail")
	if session == "" || len(session) > 253 {
		writeStatus(w, 400, "BadRequest", "invalid session ID")
		return
	}
	agent, err := c.Agents().Get(r.Context(), name, metav1.GetOptions{})
	if err != nil {
		writeResourceError(w, err)
		return
	}
	if agent.Spec.HarnessBacked() {
		writeStatus(w, 400, "BadRequest", "Chat model preferences require a model-backed agent")
		return
	}
	settings, err := s.store.GetSessionModelSettings(r.Context(), id.scope(name), session)
	if err != nil {
		writeStatus(w, 500, "InternalError", "Could not load chat model preferences")
		return
	}
	if r.Method == http.MethodPatch {
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16384)).Decode(&settings); err != nil {
			writeStatus(w, 400, "BadRequest", "invalid model preferences")
			return
		}
		if len(settings.ModelCredential) > 253 || len(settings.ReasoningEffortByModel) > 64 {
			writeStatus(w, 400, "BadRequest", "model preferences exceed limits")
			return
		}
		for model, effort := range settings.ReasoningEffortByModel {
			if len(model) > 253 || len(effort) > 32 {
				writeStatus(w, 400, "BadRequest", "invalid reasoning preference")
				return
			}
		}
	}
	settings.ModelCredential = strings.TrimSpace(settings.ModelCredential)
	selected := settings.ModelCredential
	if selected == "" {
		selected = agent.Spec.ModelCredentialFor(llm.PurposeChat)
	}
	options := []string{}
	defaultEffort := ""
	if selected != "" {
		credential, err := c.GetModelCredential(r.Context(), selected)
		if err != nil && (r.Method == http.MethodPatch || !apierrors.IsNotFound(err)) {
			writeResourceError(w, err)
			return
		}
		if err == nil {
			if r.Method == http.MethodPatch && agentsv1alpha1.IsHarnessProvider(credential.Spec.Provider) {
				writeStatus(w, 400, "BadRequest", "Select a chat model credential")
				return
			}
			options = llm.ReasoningEfforts(credential.Spec.Provider, credential.Spec.BaseURL, credential.Spec.Model)
			defaultEffort = llm.DefaultReasoningEffort(credential.Spec.Model)
		}
	}
	effort := settings.ReasoningEffortByModel[selected]
	supported := effort == ""
	for _, candidate := range options {
		if candidate == effort {
			supported = true
		}
	}
	if r.Method == http.MethodPatch {
		if !supported {
			writeStatus(w, 400, "BadRequest", "Reasoning effort is not supported by the selected model")
			return
		}
		settings.ModelCredential = strings.TrimSpace(settings.ModelCredential)
		if err := s.store.PutSessionModelSettings(r.Context(), id.scope(name), session, settings); err != nil {
			writeStatus(w, 500, "InternalError", "Could not save chat model preferences")
			return
		}
	}
	writeJSON(w, 200, struct {
		store.SessionModelSettings
		SelectedCredential string   `json:"selectedCredential"`
		EffortOptions      []string `json:"effortOptions"`
		DefaultEffort      string   `json:"defaultEffort"`
	}{settings, selected, options, defaultEffort})
}

// resolveChatModelSettings pins the conversation choice at admission. Recovery
// and approval resumes supply the persisted snapshot instead of reading prefs.
func (s *Server) resolveChatModelSettings(ctx context.Context, run *taskRun) error {
	if run.ModelSettings != nil || run.Trigger != agentsv1alpha1.RunTriggerChat || run.Agent.Spec.HarnessBacked() {
		return nil
	}
	prefs, err := s.store.GetSessionModelSettings(ctx, run.Scope, run.SessionID)
	if err != nil {
		return err
	}
	credential := prefs.ModelCredential
	if credential == "" {
		credential = run.Agent.Spec.ModelCredentialFor(llm.PurposeChat)
	}
	effort := prefs.ReasoningEffortByModel[credential]
	if effort != "" {
		profile, err := llm.LoadCredential(ctx, run.Creds, credential)
		if err != nil {
			return err
		}
		if err := llm.ValidateReasoningEffort(profile.Provider, profile.BaseURL, profile.Model, effort); err != nil {
			return err
		}
	}
	run.ModelSettings = &store.ModelSettings{ModelCredential: credential, ReasoningEffort: effort}
	return nil
}
