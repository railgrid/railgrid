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
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/cloudwego/eino/schema"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/railgrid/provider-agents/llm"
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

// modelProbeRequest is the body of the `model-test` and `model-discover` verbs.
//
// A model credential is a Secret in the tenant's own workspace, written through
// kcp — so there is nothing left for this provider to serve about it except the
// one thing the browser must not do: hold the key long enough to call a third
// party with it. That probe needs an object to be addressed by, and the natural
// one is the AGENT that will use the credential: it is the thing whose ability
// to reach a model the caller is really asking about, it is a bound CR of this
// provider's own group, and `create` on agents/model-test is a grant a tenant
// can reason about. Secrets were the other candidate and are not usable: they
// are namespaced core objects, which the grammar does not address, and a verb on
// them would be a grant over every Secret shape in the workspace.
//
// Three shapes, in precedence order:
//
//	{}                          probe the agent's own primary credential
//	{"credential": "openai"}    probe that stored credential as it is saved
//	{"provider": …, "apiKey": …, "existingName": …}   probe an unsaved draft
type modelProbeRequest struct {
	// Credential names a stored model credential to probe as saved.
	Credential string `json:"credential,omitempty"`

	// The draft fields, used when Credential is empty and any of them is set.
	// ExistingName lets the editor re-probe a saved credential's key against a
	// changed model without the browser ever seeing the key.
	Provider     string `json:"provider,omitempty"`
	BaseURL      string `json:"baseURL,omitempty"`
	Model        string `json:"model,omitempty"`
	APIKey       string `json:"apiKey,omitempty"`
	ExistingName string `json:"existingName,omitempty"`
}

// resolveModelProbe turns the request body into the profile to probe, reading
// any referenced Secret AS THE CALLER through the tenant client.
func (s *Server) resolveModelProbe(w http.ResponseWriter, r *http.Request) (llm.Profile, bool) {
	c, _, ok := s.requireClient(w, r)
	if !ok {
		return llm.Profile{}, false
	}
	var req modelProbeRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	// An empty body is the "probe this agent's own model" form, so EOF is not
	// an error.
	if err := decoder.Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		writeStatus(w, http.StatusBadRequest, "BadRequest", "invalid model connection")
		return llm.Profile{}, false
	}

	name := strings.TrimSpace(req.Credential)
	if name == "" && req.Provider == "" && req.BaseURL == "" && req.APIKey == "" && req.ExistingName == "" {
		agent, err := c.Agents().Get(r.Context(), r.PathValue("name"), metav1.GetOptions{})
		if err != nil {
			writeResourceError(w, err)
			return llm.Profile{}, false
		}
		name = strings.TrimSpace(agent.Spec.Models[llm.PurposeChat])
		if name == "" {
			writeJSON(w, http.StatusOK, credentialTestResult{OK: false, Error: errNoCredential.Error()})
			return llm.Profile{}, false
		}
	}
	if name != "" {
		profile, err := llm.LoadCredential(r.Context(), c, name)
		if err != nil {
			writeJSON(w, http.StatusOK, credentialTestResult{OK: false, Error: "credential not configured: " + err.Error()})
			return llm.Profile{}, false
		}
		return profile, true
	}

	p, err := resolveCredentialDraft(r.Context(), c, credentialDraft{
		Profile:      llm.Profile{Provider: req.Provider, BaseURL: req.BaseURL, Model: req.Model, APIKey: req.APIKey},
		ExistingName: req.ExistingName,
	})
	if err != nil {
		writeStatus(w, http.StatusBadRequest, "BadRequest", err.Error())
		return llm.Profile{}, false
	}
	return p, true
}

// testModelCredential serves the `model-test` verb: a real chat round-trip
// against the model, so "verified" means the model answered and not merely that
// the endpoint resolved. Discovery is deliberately a separate verb and cannot
// mark a credential verified.
func (s *Server) testModelCredential(w http.ResponseWriter, r *http.Request) {
	p, ok := s.resolveModelProbe(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, verifyCredentialModel(r.Context(), p))
}

// discoverModelCredential serves the `model-discover` verb: GET {baseURL}/models
// with the key, so the editor can offer the model ids the endpoint actually
// serves.
func (s *Server) discoverModelCredential(w http.ResponseWriter, r *http.Request) {
	p, ok := s.resolveModelProbe(w, r)
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
