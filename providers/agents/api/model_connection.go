// Copyright 2026 The Faros Authors.
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
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/cloudwego/eino/schema"
	"github.com/faroshq/provider-agents/llm"
)

type credentialDraft struct {
	llm.Profile
	ExistingName string `json:"existingName,omitempty"`
}

// resolveCredentialDraft never writes a Secret, and never sends an existing
// credential to a newly supplied endpoint. The caller supplies a tenant client.
func resolveCredentialDraft(ctx context.Context, c llm.SecretGetter, draft credentialDraft) (llm.Profile, error) {
	p := draft.Profile
	p.Provider = strings.TrimSpace(p.Provider)
	if p.Provider == "" {
		p.Provider = llm.ProviderOpenAICompatible
	}
	p.BaseURL = strings.TrimRight(strings.TrimSpace(p.BaseURL), "/")
	if p.BaseURL == "" {
		p.BaseURL = "https://api.openai.com/v1"
	}
	u, err := url.Parse(p.BaseURL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return llm.Profile{}, errors.New("enter a valid HTTP or HTTPS API endpoint without credentials, query, or fragment")
	}
	p.APIKey = strings.TrimSpace(p.APIKey)
	p.Model = strings.TrimSpace(p.Model)
	if p.APIKey == "" && draft.ExistingName != "" {
		stored, err := llm.LoadCredential(ctx, c, draft.ExistingName)
		if err != nil {
			return llm.Profile{}, err
		}
		if p.Provider != stored.Provider || p.BaseURL != strings.TrimRight(stored.BaseURL, "/") {
			return llm.Profile{}, errors.New("enter a credential before testing or discovering models from a changed provider or endpoint")
		}
		p.APIKey = stored.APIKey
	}
	if p.APIKey == "" {
		return llm.Profile{}, errors.New("enter an API key")
	}
	return p, nil
}

func (s *Server) credentialDraft(w http.ResponseWriter, r *http.Request) (llm.Profile, bool) {
	c, _, ok := s.requireClient(w, r)
	if !ok {
		return llm.Profile{}, false
	}
	var draft credentialDraft
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&draft); err != nil {
		writeStatus(w, http.StatusBadRequest, "BadRequest", "invalid model connection")
		return llm.Profile{}, false
	}
	p, err := resolveCredentialDraft(r.Context(), c, draft)
	if err != nil {
		writeStatus(w, http.StatusBadRequest, "BadRequest", err.Error())
		return llm.Profile{}, false
	}
	return p, true
}

func (s *Server) testCredentialDraft(w http.ResponseWriter, r *http.Request) {
	p, ok := s.credentialDraft(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, verifyCredentialModel(r.Context(), p))
}

func (s *Server) discoverCredentialDraft(w http.ResponseWriter, r *http.Request) {
	p, ok := s.credentialDraft(w, r)
	if !ok {
		return
	}
	models, latency, err := probeOpenAIModels(r.Context(), p.BaseURL, p.APIKey)
	result := credentialTestResult{OK: err == nil, Models: models, LatencyMS: latency.Milliseconds()}
	if err != nil {
		result.Error = "Could not find models. Check the endpoint and credential, or enter a model ID manually."
	}
	writeJSON(w, http.StatusOK, result)
}

func verifyCredentialModel(ctx context.Context, profile llm.Profile) credentialTestResult {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	start := time.Now()
	model, err := llm.BuildModel(ctx, profile)
	if err == nil {
		var response *schema.Message
		response, err = model.Generate(ctx, []*schema.Message{schema.UserMessage("Reply with OK to confirm this model connection.")})
		if err == nil && response == nil {
			err = errors.New("provider returned no response")
		}
	}
	result := credentialTestResult{OK: err == nil, LatencyMS: time.Since(start).Milliseconds()}
	// Upstream errors can echo request headers. Return recovery copy, never keys.
	if err != nil {
		result.Error = "The model did not respond successfully. Check the endpoint, model ID, and credential permissions, then retry."
	}
	if ctx.Err() != nil {
		result.Error = "Connection test timed out. Check the endpoint and retry."
		result.OK = false
	}
	return result
}
