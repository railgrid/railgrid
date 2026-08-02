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
	"net/http"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	agentsv1alpha1 "github.com/faroshq/provider-agents/apis/v1alpha1"
	"github.com/faroshq/provider-agents/channels"
	agentsclient "github.com/faroshq/provider-agents/client"
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
	out, err := s.applyConnectionCreate(r.Context(), c, &req)
	if err != nil {
		writeUpdateError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

// applyConnectionCreate writes the credential Secret and creates the
// Connection. Shared by the REST handler and the MCP create_connection tool.
// The pasted secret is write-only — it is never read back on any surface.
func (s *Server) applyConnectionCreate(ctx context.Context, c *agentsclient.Client, req *createConnectionRequest) (*agentsv1alpha1.Connection, error) {
	req.Name = strings.TrimSpace(req.Name)
	req.Type = strings.TrimSpace(req.Type)
	if req.Name == "" || req.Type == "" {
		return nil, errBadRequest("name and type are required")
	}
	switch req.Type {
	case agentsv1alpha1.ConnectionTypeGitHub, agentsv1alpha1.ConnectionTypeMCP,
		agentsv1alpha1.ConnectionTypeWebSearch, agentsv1alpha1.ConnectionTypeEdges,
		agentsv1alpha1.ConnectionTypeHTTP,
		agentsv1alpha1.ConnectionTypeTelegram, agentsv1alpha1.ConnectionTypeSlack,
		agentsv1alpha1.ConnectionTypeSMTP, agentsv1alpha1.ConnectionTypeDiscord:
	default:
		return nil, errBadRequest("unsupported connection type " + req.Type)
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
				return nil, errBadRequest("oauth connections need clientID and clientSecret, or a platform OAuth app configured for " + provider)
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
		if _, err := c.ApplySecret(ctx, sec); err != nil {
			return nil, err
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
			return nil, errBadRequest("oauthProvider is required (github, google, or slack)")
		}
		conn.Spec.OAuth = &agentsv1alpha1.ConnectionOAuth{Provider: provider, Scopes: req.OAuthScopes}
	}
	return c.Connections().Create(ctx, conn, metav1.CreateOptions{})
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
	var req updateConnectionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeStatus(w, http.StatusBadRequest, "BadRequest", "invalid JSON body: "+err.Error())
		return
	}
	out, err := applyConnectionUpdate(r.Context(), c, r.PathValue("name"), &req)
	if err != nil {
		writeUpdateError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// applyConnectionUpdate patches the connection and, when a new secret is given,
// rotates the credential in place. Shared by the REST handler and the MCP
// update_connection tool.
func applyConnectionUpdate(ctx context.Context, c *agentsclient.Client, name string, req *updateConnectionRequest) (*agentsv1alpha1.Connection, error) {
	name = strings.TrimSpace(name)
	conn, err := c.Connections().Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, err
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
	out, err := c.Connections().Update(ctx, conn, metav1.UpdateOptions{})
	if err != nil {
		return nil, err
	}
	// Rotate the token when a new secret is provided, preserving any other keys
	// already in the Secret (e.g. OAuth client_id/client_secret).
	if req.Secret != nil && strings.TrimSpace(*req.Secret) != "" {
		data := map[string]string{"token": strings.TrimSpace(*req.Secret)}
		if existing, gerr := c.GetSecret(ctx, llm.SecretNamespace, connectionSecretName(name)); gerr == nil {
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
		if _, serr := c.ApplySecret(ctx, sec); serr != nil {
			return nil, serr
		}
	}
	return out, nil
}

// testConnection sends a test message through a messaging connection
// (telegram/slack/smtp) so the user can verify the credential works.
func (s *Server) testConnection(w http.ResponseWriter, r *http.Request) {
	c, _, ok := s.requireClient(w, r)
	if !ok {
		return
	}
	if err := sendConnectionTest(r.Context(), c, r.PathValue("name")); err != nil {
		writeUpdateError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "sent"})
}

// sendConnectionTest delivers a test message through a messaging connection so
// the user can verify the credential works. Shared by the REST handler and the
// MCP test_connection tool.
func sendConnectionTest(ctx context.Context, c *agentsclient.Client, name string) error {
	name = strings.TrimSpace(name)
	conn, err := c.Connections().Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	switch conn.Spec.Type {
	case agentsv1alpha1.ConnectionTypeTelegram, agentsv1alpha1.ConnectionTypeSlack,
		agentsv1alpha1.ConnectionTypeSMTP, agentsv1alpha1.ConnectionTypeDiscord:
	default:
		return errBadRequest("test send is only for messaging connections (telegram, slack, smtp, discord)")
	}
	token := ""
	if sec, serr := c.GetSecret(ctx, llm.SecretNamespace, connectionSecretName(name)); serr == nil {
		if v, okk := sec.Data["token"]; okk {
			token = string(v)
		}
	}
	if err := channels.Send(ctx, channels.Message{
		Type:   conn.Spec.Type,
		Token:  token,
		Target: conn.Spec.Channel,
		Config: conn.Spec.Config,
		Text:   "✅ Test message from your kedge agents — this connection works.",
	}); err != nil {
		return &requestError{http.StatusBadGateway, "SendFailed", err.Error()}
	}
	return nil
}

func (s *Server) deleteConnection(w http.ResponseWriter, r *http.Request) {
	c, _, ok := s.requireClient(w, r)
	if !ok {
		return
	}
	if err := deleteConnectionAndSecret(r.Context(), c, r.PathValue("name")); err != nil {
		writeUpdateError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// deleteConnectionAndSecret removes the Connection and, best-effort, its
// credential Secret. Shared by the REST handler and the MCP delete_connection
// tool so a credential is never orphaned by one surface.
func deleteConnectionAndSecret(ctx context.Context, c *agentsclient.Client, name string) error {
	name = strings.TrimSpace(name)
	if err := c.Connections().Delete(ctx, name, metav1.DeleteOptions{}); err != nil {
		return err
	}
	_ = c.DeleteSecret(ctx, llm.SecretNamespace, connectionSecretName(name))
	return nil
}
