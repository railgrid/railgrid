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

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	agentsv1alpha1 "github.com/faroshq/provider-agents/apis/v1alpha1"
)

// Toolsets are workspace-shared bundles of tool grants (families + connections
// + approval) that many agents link. CRUD here mirrors connections/schedules.

func (s *Server) listToolsets(w http.ResponseWriter, r *http.Request) {
	c, _, ok := s.requireClient(w, r)
	if !ok {
		return
	}
	list, err := c.Toolsets().List(r.Context(), metav1.ListOptions{})
	if err != nil {
		writeResourceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

type toolsetRequest struct {
	Name            string   `json:"name"`
	DisplayName     string   `json:"displayName,omitempty"`
	Description     string   `json:"description,omitempty"`
	Families        []string `json:"families,omitempty"`
	Connections     []string `json:"connections,omitempty"`
	RequireApproval []string `json:"requireApproval,omitempty"`
}

func (s *Server) createToolset(w http.ResponseWriter, r *http.Request) {
	c, _, ok := s.requireClient(w, r)
	if !ok {
		return
	}
	var req toolsetRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeStatus(w, http.StatusBadRequest, "BadRequest", "invalid JSON body: "+err.Error())
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		writeStatus(w, http.StatusBadRequest, "BadRequest", "name is required")
		return
	}
	ts := &agentsv1alpha1.Toolset{
		ObjectMeta: metav1.ObjectMeta{Name: req.Name},
		Spec: agentsv1alpha1.ToolsetSpec{
			DisplayName:     req.DisplayName,
			Description:     req.Description,
			Families:        req.Families,
			Connections:     req.Connections,
			RequireApproval: req.RequireApproval,
		},
	}
	out, err := c.Toolsets().Create(r.Context(), ts, metav1.CreateOptions{})
	if err != nil {
		writeResourceError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

// updateToolsetRequest patches an existing toolset; pointer fields let the
// caller change only what they send.
type updateToolsetRequest struct {
	DisplayName     *string   `json:"displayName,omitempty"`
	Description     *string   `json:"description,omitempty"`
	Families        *[]string `json:"families,omitempty"`
	Connections     *[]string `json:"connections,omitempty"`
	RequireApproval *[]string `json:"requireApproval,omitempty"`
}

func (s *Server) updateToolset(w http.ResponseWriter, r *http.Request) {
	c, _, ok := s.requireClient(w, r)
	if !ok {
		return
	}
	name := r.PathValue("name")
	ts, err := c.Toolsets().Get(r.Context(), name, metav1.GetOptions{})
	if err != nil {
		writeResourceError(w, err)
		return
	}
	var req updateToolsetRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeStatus(w, http.StatusBadRequest, "BadRequest", "invalid JSON body: "+err.Error())
		return
	}
	if req.DisplayName != nil {
		ts.Spec.DisplayName = strings.TrimSpace(*req.DisplayName)
	}
	if req.Description != nil {
		ts.Spec.Description = *req.Description
	}
	if req.Families != nil {
		ts.Spec.Families = *req.Families
	}
	if req.Connections != nil {
		ts.Spec.Connections = *req.Connections
	}
	if req.RequireApproval != nil {
		ts.Spec.RequireApproval = *req.RequireApproval
	}
	out, err := c.Toolsets().Update(r.Context(), ts, metav1.UpdateOptions{})
	if err != nil {
		writeResourceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) deleteToolset(w http.ResponseWriter, r *http.Request) {
	c, _, ok := s.requireClient(w, r)
	if !ok {
		return
	}
	if err := c.Toolsets().Delete(r.Context(), r.PathValue("name"), metav1.DeleteOptions{}); err != nil {
		writeResourceError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
