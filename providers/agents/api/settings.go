// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package api

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"

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
