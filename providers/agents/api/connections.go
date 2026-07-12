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
		agentsv1alpha1.ConnectionTypeWebSearch, agentsv1alpha1.ConnectionTypeHTTP,
		agentsv1alpha1.ConnectionTypeTelegram, agentsv1alpha1.ConnectionTypeSlack,
		agentsv1alpha1.ConnectionTypeSMTP:
	default:
		writeStatus(w, http.StatusBadRequest, "BadRequest", "unsupported connection type "+req.Type)
		return
	}

	secretRef := connectionSecretName(req.Name)

	// Write the credential Secret first (best-effort create-or-update) so the
	// Connection references a populated Secret.
	if strings.TrimSpace(req.Secret) != "" {
		sec := &corev1.Secret{
			TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
			ObjectMeta: metav1.ObjectMeta{Name: secretRef, Namespace: llm.SecretNamespace},
			Type:       corev1.SecretTypeOpaque,
			StringData: map[string]string{"token": strings.TrimSpace(req.Secret)},
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
			Auth:        "secret",
			SecretRef:   secretRef,
			BaseURL:     req.BaseURL,
			Channel:     req.Channel,
			Config:      req.Config,
		},
	}
	out, err := c.Connections().Create(r.Context(), conn, metav1.CreateOptions{})
	if err != nil {
		writeResourceError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, out)
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
	case agentsv1alpha1.ConnectionTypeTelegram, agentsv1alpha1.ConnectionTypeSlack, agentsv1alpha1.ConnectionTypeSMTP:
	default:
		writeStatus(w, http.StatusBadRequest, "BadRequest", "test send is only for messaging connections (telegram, slack, smtp)")
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
