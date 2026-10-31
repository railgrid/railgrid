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
	"errors"
	"maps"
	"net/http"
	"strings"

	agentsv1alpha1 "github.com/railgrid/provider-agents/apis/v1alpha1"
	agentsclient "github.com/railgrid/provider-agents/client"
	"github.com/railgrid/provider-agents/llm"
	"github.com/railgrid/provider-agents/store"
	"github.com/railgrid/provider-sdk/dataplane"
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
	previousSelected := strings.TrimSpace(settings.ModelCredential)
	previousEfforts := maps.Clone(settings.ReasoningEffortByModel)
	var patch struct {
		ModelCredential        *string           `json:"modelCredential"`
		ReasoningEffortByModel map[string]string `json:"reasoningEffortByModel"`
	}
	if r.Method == http.MethodPatch {
		// Keep the submitted delta separate from the stored map. Callers may
		// update only the selected credential's effort; non-selected values are
		// retained from storage and never accepted from request payloads.
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16384)).Decode(&patch); err != nil {
			writeStatus(w, 400, "BadRequest", "invalid model preferences")
			return
		}
		if patch.ModelCredential != nil {
			settings.ModelCredential = *patch.ModelCredential
		}
		if (patch.ModelCredential != nil && len(*patch.ModelCredential) > 253) || len(patch.ReasoningEffortByModel) > 64 {
			writeStatus(w, 400, "BadRequest", "model preferences exceed limits")
			return
		}
		for model, effort := range patch.ReasoningEffortByModel {
			if len(model) > 253 || len(effort) > 32 {
				writeStatus(w, 400, "BadRequest", "invalid reasoning preference")
				return
			}
		}
	}
	settings.ModelCredential = strings.TrimSpace(settings.ModelCredential)
	selected := settings.ModelCredential
	explicitSelection := selected != ""
	if previousSelected == "" {
		previousSelected = agent.Spec.ModelCredentialFor(llm.PurposeChat)
	}
	if selected == "" {
		selected = agent.Spec.ModelCredentialFor(llm.PurposeChat)
	}
	if r.Method == http.MethodPatch && selected != "" {
		if effort, submitted := patch.ReasoningEffortByModel[selected]; submitted {
			settings.ReasoningEffortByModel = maps.Clone(settings.ReasoningEffortByModel)
			if effort == "" {
				delete(settings.ReasoningEffortByModel, selected)
			} else {
				if settings.ReasoningEffortByModel == nil {
					settings.ReasoningEffortByModel = make(map[string]string, 1)
				}
				settings.ReasoningEffortByModel[selected] = effort
			}
		}
		if len(settings.ReasoningEffortByModel) > 64 {
			writeStatus(w, 400, "BadRequest", "model preferences exceed limits")
			return
		}
	}
	options := []string{}
	defaultEffort := ""
	if selected != "" {
		// An explicit session choice is a separate capability from access to
		// the Agent that owns this session. Authorize it before reading the
		// credential as the provider, so a caller cannot use a guessed name as
		// a credential-existence oracle. The Agent's configured default keeps
		// its existing semantics: access to the Agent already exposes that
		// default for this Agent's chat runs.
		if explicitSelection && !s.authorizeSessionModelCredential(w, r, selected) {
			return
		}
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
			// A model switch can restore a remembered effort whose target
			// connection's advertised capabilities have since changed. Clear
			// only that stale value when the request left the exact stored value
			// in place; a newly supplied unsupported value remains a 400.
			previousEffort, wasStored := previousEfforts[selected]
			if selected != previousSelected && wasStored && effort == previousEffort {
				delete(settings.ReasoningEffortByModel, selected)
			} else {
				writeStatus(w, 400, "BadRequest", "Reasoning effort is not supported by the selected model")
				return
			}
		}
		settings.ModelCredential = strings.TrimSpace(settings.ModelCredential)
		if err := s.store.PutSessionModelSettings(r.Context(), id.scope(name), session, settings); err != nil {
			if errors.Is(err, store.ErrSessionModelLimit) {
				writeStatus(w, http.StatusTooManyRequests, "TooManyRequests", "Chat model preference limit reached; delete an unused chat and try again")
				return
			}
			writeStatus(w, 500, "InternalError", "Could not save chat model preferences")
			return
		}
	}
	// The persisted map can contain preferences for several credentials, but
	// this response is authorized only for the selected credential. Return a
	// copy containing that entry alone; keep the complete map in settings for
	// future authorized selections.
	responseSettings := settings
	responseSettings.ReasoningEffortByModel = make(map[string]string, 1)
	if selectedEffort, exists := settings.ReasoningEffortByModel[selected]; exists {
		responseSettings.ReasoningEffortByModel[selected] = selectedEffort
	}
	writeJSON(w, 200, struct {
		store.SessionModelSettings
		SelectedCredential string   `json:"selectedCredential"`
		EffortOptions      []string `json:"effortOptions"`
		DefaultEffort      string   `json:"defaultEffort"`
	}{responseSettings, selected, options, defaultEffort})
}

// authorizeSessionModelCredential checks the current stamped caller's get
// permission on an explicitly selected credential. The session's Agent gate
// only authorizes that Agent; it does not imply visibility of another object.
func (s *Server) authorizeSessionModelCredential(w http.ResponseWriter, r *http.Request, name string) bool {
	gate, ok := gateFrom(r.Context())
	if !ok || gate.provider == nil {
		writeStatus(w, http.StatusInternalServerError, "InternalError", "the data-plane gate resolved no provider client for this request")
		return false
	}
	identity, ok := dataplane.ProxiedIdentityFrom(r.Context())
	if !ok || identity.User == "" {
		writeStatus(w, http.StatusUnauthorized, "Unauthorized", "no caller identity")
		return false
	}

	allowed, err := dataplane.Authorize(r.Context(), gate.provider, identity, dataplane.ResourceAttributes{
		Group: agentsv1alpha1.GroupName, Version: agentsv1alpha1.Version,
		Resource: "modelcredentials", Name: name, Verb: "get",
	})
	if err != nil {
		writeStatus(w, http.StatusInternalServerError, "InternalError", "Could not verify model credential access")
		return false
	}
	if !allowed {
		// Keep denial indistinguishable from an absent credential.
		dataplane.WriteError(w, dataplane.ErrDenied)
		return false
	}
	return true
}

// authorizeFreshChatModelCredential rechecks an explicit preference on every
// fresh chat admission. A data-plane chat has the stamped caller and provider
// client in its request context; MCP chat instead carries the caller's scoped
// credential resolver, whose ModelCredential read is itself the authorization
// check. A run with an already-pinned ModelSettings snapshot does not call this
// helper when it resumes.
func (s *Server) authorizeFreshChatModelCredential(ctx context.Context, run *taskRun, name string) (*agentsv1alpha1.ModelCredential, error) {
	if gate, ok := gateFrom(ctx); ok {
		identity, ok := dataplane.ProxiedIdentityFrom(ctx)
		if !ok || identity.User == "" {
			return nil, sessionModelCredentialNotFound()
		}
		allowed, err := dataplane.Authorize(ctx, gate.provider, identity, dataplane.ResourceAttributes{
			Group: agentsv1alpha1.GroupName, Version: agentsv1alpha1.Version,
			Resource: "modelcredentials", Name: name, Verb: "get",
		})
		if err != nil {
			return nil, &requestError{code: http.StatusInternalServerError, reason: "InternalError", msg: "Could not verify model credential access"}
		}
		if !allowed {
			return nil, sessionModelCredentialNotFound()
		}
		object, err := gate.provider.Resource(agentsclient.ModelCredentialGVR).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return nil, err
		}
		return fromU[agentsv1alpha1.ModelCredential](object)
	}

	// The only other fresh chat transport is MCP, where the client uses the
	// current caller's bearer. Requiring the object read here makes selection
	// permission effective even when the preference has no reasoning override
	// and would otherwise skip credential resolution at admission.
	if run.HubToken == "" || run.Creds == nil {
		return nil, sessionModelCredentialNotFound()
	}
	credential, err := run.Creds.GetModelCredential(ctx, name)
	if apierrors.IsNotFound(err) || apierrors.IsForbidden(err) {
		return nil, sessionModelCredentialNotFound()
	}
	if err != nil {
		return nil, err
	}
	return credential, nil
}

func sessionModelCredentialNotFound() error {
	return &requestError{code: http.StatusNotFound, reason: "NotFound", msg: "model credential not found or not visible"}
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
	var selectedCredential *agentsv1alpha1.ModelCredential
	if strings.TrimSpace(prefs.ModelCredential) != "" {
		selectedCredential, err = s.authorizeFreshChatModelCredential(ctx, run, credential)
		if err != nil {
			return err
		}
	}
	effort := prefs.ReasoningEffortByModel[credential]
	if selectedCredential == nil && credential != "" {
		if run.Creds != nil {
			selectedCredential, err = run.Creds.GetModelCredential(ctx, credential)
			if err != nil {
				// Agent defaults have always been resolved by buildModelForPurpose,
				// which skips an unreadable primary and tries its configured
				// fallbacks. Keep that behavior while still pinning transport when
				// the primary's metadata is available. An explicit chat choice is a
				// separate capability and must remain fail-closed.
				if strings.TrimSpace(prefs.ModelCredential) != "" || ctx.Err() != nil {
					return err
				}
				selectedCredential = nil
			}
		} else {
			return llm.ErrNotConfigured
		}
	}
	// Keep the selected preference attached to its credential even when its
	// metadata read fails transiently. buildModelForPurpose applies settings
	// only to this exact credential, where BuildModel validates the effort;
	// configured fallbacks never inherit it.
	settings := &store.ModelSettings{ModelCredential: credential, ReasoningEffort: effort}
	if selectedCredential != nil {
		// Admission needs the endpoint/model metadata, not the Secret. Pin
		// before any Pending record is written so recovery retains the wire
		// protocol even if the Agent or routing defaults change later.
		profile := llm.Profile{
			Provider: selectedCredential.Spec.Provider, BaseURL: selectedCredential.Spec.BaseURL,
			Model: selectedCredential.Spec.Model, ReasoningEffort: effort,
		}
		if err := llm.ValidateReasoningEffort(profile.Provider, profile.BaseURL, profile.Model, effort); err != nil {
			return err
		}
		settings.ReasoningEffort = llm.EffectiveReasoningEffort(profile)
		settings.Transport = llm.ModelTransport(profile)
	}
	run.ModelSettings = settings
	return nil
}
