/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func (s *Server) restartProjectDevelopment(w http.ResponseWriter, r *http.Request) {
	c, id, p, ok := s.requireProjectWithClient(w, r)
	if !ok {
		return
	}
	target, err := s.projectDevelopmentTarget(r.Context(), c, p, id)
	if err != nil {
		writeStatus(w, http.StatusBadRequest, "BadRequest", err.Error())
		return
	}
	// Validate the instance exists in the workspace before reaching the data
	// plane, so a missing instance surfaces as 404 rather than a proxy error.
	if err := s.validateDevelopmentInstance(r.Context(), c, target); err != nil {
		writeRuntimeTargetError(w, err)
		return
	}
	// A ?component= query restricts the restart to one component; the default
	// restarts every declared component.
	components := target.sortedComponents()
	if requested := strings.TrimSpace(r.URL.Query().Get("component")); requested != "" {
		if _, ok := target.Components[requested]; !ok {
			writeStatus(w, http.StatusBadRequest, "BadRequest", "unknown development component "+requested)
			return
		}
		components = []string{requested}
	}
	results := map[string]json.RawMessage{}
	for _, component := range components {
		respBody, status, err := s.dataPlanePost(r.Context(), id, target.dataPlaneRefFor(component), dataPlaneVerbRestart, []byte(`{}`))
		if err != nil {
			writeStatus(w, http.StatusBadGateway, "BadGateway", "component "+component+": "+err.Error())
			return
		}
		if status < 200 || status >= 300 {
			writeStatus(w, http.StatusBadGateway, "BadGateway", fmt.Sprintf("component %s restart returned %d: %s", component, status, strings.TrimSpace(string(respBody))))
			return
		}
		results[component] = json.RawMessage(respBody)
	}
	writeJSON(w, http.StatusOK, results)
}

func (s *Server) logsProjectDevelopment(w http.ResponseWriter, r *http.Request) {
	c, id, p, ok := s.requireProjectWithClient(w, r)
	if !ok {
		return
	}
	target, err := s.projectDevelopmentTarget(r.Context(), c, p, id)
	if err != nil {
		writeStatus(w, http.StatusBadRequest, "BadRequest", err.Error())
		return
	}
	// Validate the instance exists in the workspace first (404 vs proxy error).
	if err := s.validateDevelopmentInstance(r.Context(), c, target); err != nil {
		writeRuntimeTargetError(w, err)
		return
	}
	// ?component= picks the component to stream; defaults to the first
	// declared component.
	component := strings.TrimSpace(r.URL.Query().Get("component"))
	if component == "" {
		component = target.sortedComponents()[0]
	} else if _, ok := target.Components[component]; !ok {
		writeStatus(w, http.StatusBadRequest, "BadRequest", "unknown development component "+component)
		return
	}
	// Stream logs from the infrastructure provider's data-plane subresource;
	// it owns the runtime credential and the control-token injection.
	if err := s.dataPlaneStream(r.Context(), id, target.dataPlaneRefFor(component), dataPlaneVerbLog, w); err != nil {
		// Headers may already be sent on a mid-stream failure; only safe to
		// write a status when nothing has been flushed yet.
		writeStatus(w, http.StatusBadGateway, "BadGateway", err.Error())
		return
	}
}

func (s *Server) statusProjectDevelopment(w http.ResponseWriter, r *http.Request) {
	c, id, p, ok := s.requireProjectWithClient(w, r)
	if !ok {
		return
	}
	target, err := s.projectDevelopmentTarget(r.Context(), c, p, id)
	if err != nil {
		writeStatus(w, http.StatusBadRequest, "BadRequest", err.Error())
		return
	}
	res, err := target.instanceResource()
	if err != nil {
		writeStatus(w, http.StatusBadRequest, "BadRequest", err.Error())
		return
	}
	obj, err := c.Resource(res, "").Get(r.Context(), target.ResourceName, metav1.GetOptions{})
	if err != nil {
		writeRuntimeTargetError(w, err)
		return
	}
	status, _ := obj.Object["status"].(map[string]any)
	writeJSON(w, http.StatusOK, status)
}

func writeRuntimeTargetError(w http.ResponseWriter, err error) {
	switch {
	case apierrors.IsNotFound(err):
		writeStatus(w, http.StatusNotFound, "NotFound", err.Error())
	case apierrors.IsForbidden(err):
		writeStatus(w, http.StatusForbidden, "Forbidden", err.Error())
	default:
		writeStatus(w, http.StatusConflict, "RuntimeNotReady", err.Error())
	}
}
