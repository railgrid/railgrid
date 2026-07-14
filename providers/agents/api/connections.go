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
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	agentsv1alpha1 "github.com/faroshq/provider-agents/apis/v1alpha1"
	"github.com/faroshq/provider-agents/channels"
	"github.com/faroshq/provider-agents/llm"
)

func (s *Server) listConnections(w http.ResponseWriter, r *http.Request) {
	c, _, ok := s.requireClient(w, r)
	if !ok {
		return
	}
	list, err := c.Connections().List(r.Context(), metav1.ListOptions{})
	if err != nil {
		writeResourceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

type createConnectionRequest struct {
	Name        string            `json:"name"`
	Type        string            `json:"type"`
	DisplayName string            `json:"displayName,omitempty"`
	BaseURL     string            `json:"baseURL,omitempty"`
	Channel     string            `json:"channel,omitempty"`
	Config      map[string]string `json:"config,omitempty"`
	// Secret is the connection credential (PAT, API key, bot token). Written to
	// a per-connection Secret; never returned on reads.
	Secret string `json:"secret,omitempty"`
	// Auth: "secret" (default, pasted token) or "oauth" (Connect flow using an
	// OAuth app the user brings).
	Auth string `json:"auth,omitempty"`
	// OAuth app credentials + provider (github|google|slack) for auth: oauth.
	OAuthProvider string   `json:"oauthProvider,omitempty"`
	OAuthScopes   []string `json:"oauthScopes,omitempty"`
	ClientID      string   `json:"clientID,omitempty"`
	ClientSecret  string   `json:"clientSecret,omitempty"`
}

func connectionSecretName(conn string) string { return "kedge-agents-conn-" + conn }

func (s *Server) createConnection(w http.ResponseWriter, r *http.Request) {
	c, _, ok := s.requireClient(w, r)
	if !ok {
		return
	}
	var req createConnectionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeStatus(w, http.StatusBadRequest, "BadRequest", "invalid JSON body: "+err.Error())
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	req.Type = strings.TrimSpace(req.Type)
	if req.Name == "" || req.Type == "" {
		writeStatus(w, http.StatusBadRequest, "BadRequest", "name and type are required")
		return
	}
	switch req.Type {
	case agentsv1alpha1.ConnectionTypeGitHub, agentsv1alpha1.ConnectionTypeMCP,
		agentsv1alpha1.ConnectionTypeWebSearch, agentsv1alpha1.ConnectionTypeEdges,
		agentsv1alpha1.ConnectionTypeHTTP,
		agentsv1alpha1.ConnectionTypeTelegram, agentsv1alpha1.ConnectionTypeSlack,
		agentsv1alpha1.ConnectionTypeSMTP, agentsv1alpha1.ConnectionTypeDiscord:
	default:
		writeStatus(w, http.StatusBadRequest, "BadRequest", "unsupported connection type "+req.Type)
		return
	}

	secretRef := connectionSecretName(req.Name)

	// Write the credential Secret first so the Connection references a
	// populated Secret. Token auth stores the pasted secret; OAuth stores the
	// user's OAuth-app credentials (the Connect flow adds the tokens later).
	auth := strings.TrimSpace(req.Auth)
	if auth == "" {
		auth = "secret"
	}
	secretData := map[string]string{}
	if strings.TrimSpace(req.Secret) != "" {
		secretData["token"] = strings.TrimSpace(req.Secret)
	}
	if auth == "oauth" {
		provider := strings.TrimSpace(req.OAuthProvider)
		if provider == "" && req.Type == agentsv1alpha1.ConnectionTypeGitHub {
			provider = "github"
		}
		_, platformApp := s.platformOAuthApp(provider)
		if strings.TrimSpace(req.ClientID) == "" || strings.TrimSpace(req.ClientSecret) == "" {
			// Allowed when the operator configured a platform OAuth app for this
			// provider (mirrors the code provider's env-configured app).
			if !platformApp {
				writeStatus(w, http.StatusBadRequest, "BadRequest", "oauth connections need clientID and clientSecret, or a platform OAuth app configured for "+provider)
				return
			}
		} else {
			secretData["client_id"] = strings.TrimSpace(req.ClientID)
			secretData["client_secret"] = strings.TrimSpace(req.ClientSecret)
		}
	}
	if len(secretData) > 0 {
		sec := &corev1.Secret{
			TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
			ObjectMeta: metav1.ObjectMeta{Name: secretRef, Namespace: llm.SecretNamespace},
			Type:       corev1.SecretTypeOpaque,
			StringData: secretData,
		}
		if _, err := c.ApplySecret(r.Context(), sec); err != nil {
			writeResourceError(w, err)
			return
		}
	}

	conn := &agentsv1alpha1.Connection{
		ObjectMeta: metav1.ObjectMeta{Name: req.Name},
		Spec: agentsv1alpha1.ConnectionSpec{
			Type:        req.Type,
			DisplayName: req.DisplayName,
			Auth:        auth,
			SecretRef:   secretRef,
			BaseURL:     req.BaseURL,
			Channel:     req.Channel,
			Config:      req.Config,
		},
	}
	if auth == "oauth" {
		provider := strings.TrimSpace(req.OAuthProvider)
		if provider == "" && req.Type == agentsv1alpha1.ConnectionTypeGitHub {
			provider = "github"
		}
		if provider == "" {
			writeStatus(w, http.StatusBadRequest, "BadRequest", "oauthProvider is required (github, google, or slack)")
			return
		}
		conn.Spec.OAuth = &agentsv1alpha1.ConnectionOAuth{Provider: provider, Scopes: req.OAuthScopes}
	}
	out, err := c.Connections().Create(r.Context(), conn, metav1.CreateOptions{})
	if err != nil {
		writeResourceError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

// updateConnectionRequest patches an existing connection. Pointer fields mean
// callers change only what they send (rename, update the webhook URL / target,
// rotate the token). A non-empty Secret rotates the credential; empty keeps it.
type updateConnectionRequest struct {
	DisplayName *string            `json:"displayName,omitempty"`
	BaseURL     *string            `json:"baseURL,omitempty"`
	Channel     *string            `json:"channel,omitempty"`
	Config      *map[string]string `json:"config,omitempty"`
	Secret      *string            `json:"secret,omitempty"`
}

func (s *Server) updateConnection(w http.ResponseWriter, r *http.Request) {
	c, _, ok := s.requireClient(w, r)
	if !ok {
		return
	}
	name := r.PathValue("name")
	conn, err := c.Connections().Get(r.Context(), name, metav1.GetOptions{})
	if err != nil {
		writeResourceError(w, err)
		return
	}
	var req updateConnectionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeStatus(w, http.StatusBadRequest, "BadRequest", "invalid JSON body: "+err.Error())
		return
	}
	if req.DisplayName != nil {
		conn.Spec.DisplayName = strings.TrimSpace(*req.DisplayName)
	}
	if req.BaseURL != nil {
		conn.Spec.BaseURL = strings.TrimSpace(*req.BaseURL)
	}
	if req.Channel != nil {
		conn.Spec.Channel = strings.TrimSpace(*req.Channel)
	}
	if req.Config != nil {
		conn.Spec.Config = *req.Config
	}
	out, err := c.Connections().Update(r.Context(), conn, metav1.UpdateOptions{})
	if err != nil {
		writeResourceError(w, err)
		return
	}
	// Rotate the token when a new secret is provided, preserving any other keys
	// already in the Secret (e.g. OAuth client_id/client_secret).
	if req.Secret != nil && strings.TrimSpace(*req.Secret) != "" {
		data := map[string]string{"token": strings.TrimSpace(*req.Secret)}
		if existing, gerr := c.GetSecret(r.Context(), llm.SecretNamespace, connectionSecretName(name)); gerr == nil {
			for k, v := range existing.Data {
				if k != "token" {
					data[k] = string(v)
				}
			}
		}
		sec := &corev1.Secret{
			TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
			ObjectMeta: metav1.ObjectMeta{Name: connectionSecretName(name), Namespace: llm.SecretNamespace},
			Type:       corev1.SecretTypeOpaque,
			StringData: data,
		}
		if _, serr := c.ApplySecret(r.Context(), sec); serr != nil {
			writeResourceError(w, serr)
			return
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// testConnection sends a test message through a messaging connection
// (telegram/slack/smtp) so the user can verify the credential works.
func (s *Server) testConnection(w http.ResponseWriter, r *http.Request) {
	c, _, ok := s.requireClient(w, r)
	if !ok {
		return
	}
	name := r.PathValue("name")
	conn, err := c.Connections().Get(r.Context(), name, metav1.GetOptions{})
	if err != nil {
		writeResourceError(w, err)
		return
	}
	switch conn.Spec.Type {
	case agentsv1alpha1.ConnectionTypeTelegram, agentsv1alpha1.ConnectionTypeSlack,
		agentsv1alpha1.ConnectionTypeSMTP, agentsv1alpha1.ConnectionTypeDiscord:
	default:
		writeStatus(w, http.StatusBadRequest, "BadRequest", "test send is only for messaging connections (telegram, slack, smtp, discord)")
		return
	}
	token := ""
	if sec, serr := c.GetSecret(r.Context(), llm.SecretNamespace, connectionSecretName(name)); serr == nil {
		if v, okk := sec.Data["token"]; okk {
			token = string(v)
		}
	}
	if err := channels.Send(r.Context(), channels.Message{
		Type:   conn.Spec.Type,
		Token:  token,
		Target: conn.Spec.Channel,
		Config: conn.Spec.Config,
		Text:   "✅ Test message from your kedge agents — this connection works.",
	}); err != nil {
		writeStatus(w, http.StatusBadGateway, "SendFailed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "sent"})
}

func (s *Server) deleteConnection(w http.ResponseWriter, r *http.Request) {
	c, _, ok := s.requireClient(w, r)
	if !ok {
		return
	}
	name := r.PathValue("name")
	if err := c.Connections().Delete(r.Context(), name, metav1.DeleteOptions{}); err != nil {
		writeResourceError(w, err)
		return
	}
	// Best-effort delete of the credential Secret.
	_ = c.DeleteSecret(r.Context(), llm.SecretNamespace, connectionSecretName(name))
	w.WriteHeader(http.StatusNoContent)
}
