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
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/faroshq/provider-agents/llm"
)

// modelCredential is a named, reusable set of model credentials the user
// creates once and assigns to one or more agents. Each is its own Secret
// (kedge-agents-model-<name>), so credentials can be shared and managed
// independently. The API key is never returned on reads.
type modelCredential struct {
	Name      string `json:"name"`
	Provider  string `json:"provider,omitempty"`
	BaseURL   string `json:"baseURL,omitempty"`
	Model     string `json:"model,omitempty"`
	HasAPIKey bool   `json:"hasAPIKey"`
	// APIKey is write-only (accepted on POST, never returned).
	APIKey string `json:"apiKey,omitempty"`
}

// listCredentials returns all named model credentials in the workspace (keys
// redacted).
func (s *Server) listCredentials(w http.ResponseWriter, r *http.Request) {
	c, _, ok := s.requireClient(w, r)
	if !ok {
		return
	}
	secrets, err := c.ListSecrets(r.Context(), llm.SecretNamespace)
	if err != nil {
		writeResourceError(w, err)
		return
	}
	out := []modelCredential{}
	for i := range secrets {
		sec := &secrets[i]
		if !strings.HasPrefix(sec.Name, llm.ModelCredentialPrefix) {
			continue
		}
		get := func(k string) string {
			if v, okk := sec.Data[k]; okk {
				return strings.TrimSpace(string(v))
			}
			return ""
		}
		out = append(out, modelCredential{
			Name:      strings.TrimPrefix(sec.Name, llm.ModelCredentialPrefix),
			Provider:  get("provider"),
			BaseURL:   get("baseURL"),
			Model:     get("model"),
			HasAPIKey: get("apiKey") != "",
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

// createCredential writes a named model-credential Secret (create-or-update).
func (s *Server) createCredential(w http.ResponseWriter, r *http.Request) {
	c, _, ok := s.requireClient(w, r)
	if !ok {
		return
	}
	var req modelCredential
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeStatus(w, http.StatusBadRequest, "BadRequest", "invalid JSON body: "+err.Error())
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	req.Provider = strings.TrimSpace(req.Provider)
	req.BaseURL = strings.TrimSpace(req.BaseURL)
	req.Model = strings.TrimSpace(req.Model)
	req.APIKey = strings.TrimSpace(req.APIKey)
	if req.Name == "" {
		writeStatus(w, http.StatusBadRequest, "BadRequest", "name is required")
		return
	}
	if req.Provider == "" {
		req.Provider = llm.ProviderOpenAICompatible
	}
	if req.Model == "" {
		writeStatus(w, http.StatusBadRequest, "BadRequest", "model is required")
		return
	}
	// Preserve an existing key when updating without a new one.
	apiKey := req.APIKey
	if apiKey == "" {
		if existing, err := c.GetSecret(r.Context(), llm.SecretNamespace, llm.CredentialSecretName(req.Name)); err == nil {
			if v, okk := existing.Data["apiKey"]; okk {
				apiKey = string(v)
			}
		}
		if apiKey == "" {
			writeStatus(w, http.StatusBadRequest, "BadRequest", "apiKey is required")
			return
		}
	}
	sec := &corev1.Secret{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
		ObjectMeta: metav1.ObjectMeta{Name: llm.CredentialSecretName(req.Name), Namespace: llm.SecretNamespace},
		Type:       corev1.SecretTypeOpaque,
		StringData: map[string]string{"provider": req.Provider, "baseURL": req.BaseURL, "model": req.Model, "apiKey": apiKey},
	}
	if _, err := c.ApplySecret(r.Context(), sec); err != nil {
		writeResourceError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, modelCredential{
		Name: req.Name, Provider: req.Provider, BaseURL: req.BaseURL, Model: req.Model, HasAPIKey: true,
	})
}

// credentialTestResult is the outcome of a live credential health probe.
type credentialTestResult struct {
	OK        bool     `json:"ok"`
	LatencyMS int64    `json:"latencyMS"`
	Error     string   `json:"error,omitempty"`
	Models    []string `json:"models,omitempty"` // ids the endpoint serves (discovery)
}

// testCredential health-checks a named credential by calling the provider's
// GET {baseURL}/models with the stored key. This is a cheap, token-free probe
// that both verifies the key works and discovers the models the endpoint serves
// (returned for the "pick a model" UX). Reports latency either way.
func (s *Server) testCredential(w http.ResponseWriter, r *http.Request) {
	c, _, ok := s.requireClient(w, r)
	if !ok {
		return
	}
	name := r.PathValue("name")
	profile, err := llm.LoadCredential(r.Context(), c, name)
	if err != nil {
		writeJSON(w, http.StatusOK, credentialTestResult{OK: false, Error: "credential not configured: " + err.Error()})
		return
	}
	models, latency, perr := probeOpenAIModels(r.Context(), profile.BaseURL, profile.APIKey)
	if perr != nil {
		writeJSON(w, http.StatusOK, credentialTestResult{OK: false, LatencyMS: latency.Milliseconds(), Error: perr.Error()})
		return
	}
	writeJSON(w, http.StatusOK, credentialTestResult{OK: true, LatencyMS: latency.Milliseconds(), Models: models})
}

// probeOpenAIModels calls GET {baseURL}/models and returns the served model ids
// plus the round-trip latency. A non-2xx status or transport error is returned
// as err (with latency still measured for the health badge).
func probeOpenAIModels(ctx context.Context, baseURL, apiKey string) ([]string, time.Duration, error) {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if base == "" {
		base = "https://api.openai.com/v1"
	}
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/models", nil)
	if err != nil {
		return nil, 0, err
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	start := time.Now()
	resp, err := http.DefaultClient.Do(req)
	latency := time.Since(start)
	if err != nil {
		return nil, latency, err
	}
	defer resp.Body.Close() //nolint:errcheck
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := strings.TrimSpace(string(body))
		if len(msg) > 200 {
			msg = msg[:200]
		}
		return nil, latency, &probeError{status: resp.StatusCode, msg: msg}
	}
	var parsed struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		// 2xx but unexpected body — the endpoint is reachable, just not a
		// standard /models list. Treat as healthy with no discovered models.
		return nil, latency, nil
	}
	ids := make([]string, 0, len(parsed.Data))
	for _, m := range parsed.Data {
		if id := strings.TrimSpace(m.ID); id != "" {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids, latency, nil
}

type probeError struct {
	status int
	msg    string
}

func (e *probeError) Error() string {
	if e.msg != "" {
		return "endpoint returned HTTP " + strconv.Itoa(e.status) + ": " + e.msg
	}
	return "endpoint returned HTTP " + strconv.Itoa(e.status)
}

// modelCatalog returns the curated pricing + capability catalog (public — no
// tenant data, just reference data for the Models UI).
func (s *Server) modelCatalog(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"items": llm.Catalog()})
}

func (s *Server) deleteCredential(w http.ResponseWriter, r *http.Request) {
	c, _, ok := s.requireClient(w, r)
	if !ok {
		return
	}
	name := r.PathValue("name")
	if err := c.DeleteSecret(r.Context(), llm.SecretNamespace, llm.CredentialSecretName(name)); err != nil {
		writeResourceError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
