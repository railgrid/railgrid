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
	"fmt"
	"net/http"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
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
	// SigningSecret is the platform's request-verification secret for inbound
	// messaging: the Slack app signing secret. Telegram connections get one
	// generated (the webhook secret_token) and ignore this field. Write-only.
	SigningSecret string `json:"signingSecret,omitempty"`
	// Auth: "secret" (default, pasted token) or "oauth" (Connect flow using an
	// OAuth app the user brings).
	Auth string `json:"auth,omitempty"`
	// OAuth app credentials + provider (github|google|slack) for auth: oauth.
	OAuthProvider string   `json:"oauthProvider,omitempty"`
	OAuthScopes   []string `json:"oauthScopes,omitempty"`
	ClientID      string   `json:"clientID,omitempty"`
	ClientSecret  string   `json:"clientSecret,omitempty"`
}

func connectionSecretName(conn string) string { return "faros-agents-conn-" + conn }

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
	// Inbound verification material. Slack: the app signing secret the user
	// pastes. Telegram: a secret_token we choose and register with setWebhook,
	// so every connection is born verifiable.
	switch req.Type {
	case agentsv1alpha1.ConnectionTypeSlack:
		if v := strings.TrimSpace(req.SigningSecret); v != "" {
			secretData[signingSecretKey] = v
		}
	case agentsv1alpha1.ConnectionTypeTelegram:
		generated, err := newSigningSecret()
		if err != nil {
			return nil, err
		}
		secretData[signingSecretKey] = generated
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
	// SigningSecret sets or rotates the Slack app signing secret (write-only).
	// This is how a Slack connection created before signature verification
	// existed becomes usable for inbound again.
	SigningSecret *string `json:"signingSecret,omitempty"`
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
	updates := map[string]string{}
	// Rotate the token when a new secret is provided, preserving any other keys
	// already in the Secret (e.g. OAuth client_id/client_secret).
	if req.Secret != nil && strings.TrimSpace(*req.Secret) != "" {
		updates["token"] = strings.TrimSpace(*req.Secret)
	}
	if req.SigningSecret != nil && strings.TrimSpace(*req.SigningSecret) != "" {
		if conn.Spec.Type != agentsv1alpha1.ConnectionTypeSlack {
			return nil, errBadRequest("signingSecret applies to slack connections only (telegram secret tokens are generated)")
		}
		updates[signingSecretKey] = strings.TrimSpace(*req.SigningSecret)
	}
	out, err := c.Connections().Update(ctx, conn, metav1.UpdateOptions{})
	if err != nil {
		return nil, err
	}
	if len(updates) > 0 {
		if err := mergeConnectionSecret(ctx, c, name, updates); err != nil {
			return nil, err
		}
	}
	// The reconcile loop parked the connection in Error while it had no
	// signing secret; adding one is exactly the fix it asked for.
	if _, ok := updates[signingSecretKey]; ok && out.Status.Message == connectionSigningSecretMissingMessage {
		out.Status.Phase, out.Status.Message = "Ready", ""
		if updated, serr := c.Connections().UpdateStatus(ctx, out, metav1.UpdateOptions{}); serr == nil {
			out = updated
		}
	}
	return out, nil
}

// mergeConnectionSecret writes keys into the connection Secret as the caller,
// keeping every key it does not mention.
//
// The read is load-bearing, not an optimisation: the apply below sends the
// merged map as the Secret's whole StringData, so any key missing from it is
// dropped. Treating a failed read as "no keys yet" would therefore let a
// transient gateway or apiserver blip turn "add a signing secret" into "erase
// the bot token and the OAuth client_id/client_secret". Only NotFound — no
// Secret exists yet — legitimately means "start from empty"; every other error
// aborts before anything is written.
func mergeConnectionSecret(ctx context.Context, c *agentsclient.Client, name string, updates map[string]string) error {
	data := map[string]string{}
	existing, gerr := c.GetSecret(ctx, llm.SecretNamespace, connectionSecretName(name))
	switch {
	case gerr == nil:
		for k, v := range existing.Data {
			data[k] = string(v)
		}
	case !apierrors.IsNotFound(gerr):
		return fmt.Errorf("read connection secret for %q before merging: %w", name, gerr)
	}
	for k, v := range updates {
		data[k] = v
	}
	sec := &corev1.Secret{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
		ObjectMeta: metav1.ObjectMeta{Name: connectionSecretName(name), Namespace: llm.SecretNamespace},
		Type:       corev1.SecretTypeOpaque,
		StringData: data,
	}
	_, err := c.ApplySecret(ctx, sec)
	return err
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
		Text:   "✅ Test message from your faros agents — this connection works.",
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
