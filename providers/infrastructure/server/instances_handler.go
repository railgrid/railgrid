// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/faroshq/faros-kedge/providers/infrastructure/kro"
	"github.com/faroshq/faros-kedge/providers/infrastructure/tenant"
)

type instanceListResponse struct {
	Items []kro.Instance `json:"items"`
}

// handleInstances multiplexes the POST /api/instances + GET /api/instances
// cases off the same prefix. Sub-resource paths (/api/instances/{name})
// route to handleInstanceItem in server.go's mux wiring.
func (s *Server) handleInstances(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		s.createInstance(w, r)
	case http.MethodGet:
		s.listInstances(w, r)
	default:
		writeJSONError(w, http.StatusMethodNotAllowed, ReasonBadRequest, "method not allowed")
	}
}

// handleInstanceItem covers GET / DELETE /api/instances/{name}.
func (s *Server) handleInstanceItem(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/api/instances/")
	if name == "" || strings.Contains(name, "/") {
		writeJSONError(w, http.StatusBadRequest, ReasonBadRequest, "instance name required and must be single-segment")
		return
	}
	switch r.Method {
	case http.MethodGet:
		s.getInstance(w, r, name)
	case http.MethodDelete:
		s.deleteInstance(w, r, name)
	default:
		writeJSONError(w, http.StatusMethodNotAllowed, ReasonBadRequest, "method not allowed")
	}
}

func (s *Server) createInstance(w http.ResponseWriter, r *http.Request) {
	tenantPath := tenantFromRequest(r)
	if tenantPath == "" {
		writeJSONError(w, http.StatusBadRequest, ReasonTenantMissing,
			"X-Kedge-Tenant header required (set by hub backend proxy after auth)")
		return
	}
	user := userFromRequest(r)

	var req kro.CreateInstanceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, ReasonBadRequest, "decode body: "+err.Error())
		return
	}
	if req.TemplateName == "" || req.Name == "" {
		writeJSONError(w, http.StatusBadRequest, ReasonBadRequest, "templateName and name are required")
		return
	}
	if !isDNSSubdomain(req.Name) {
		writeJSONError(w, http.StatusBadRequest, ReasonBadRequest,
			"instance name must be a DNS-1123 subdomain (lowercase alnum + - + .)")
		return
	}

	// Resolve template first — gives back the GVR + the schema we
	// would have validated against. (Schema validation itself is
	// currently best-effort on the server; the portal's ajv catch is
	// authoritative until we add a real JSON-schema validator here.)
	t, err := s.kro.GetTemplate(r.Context(), req.TemplateName, req.TemplateVersion)
	if err != nil {
		if errors.Is(err, kro.ErrTemplateNotFound) {
			writeJSONError(w, http.StatusNotFound, ReasonTemplateNotFound, err.Error())
			return
		}
		writeJSONError(w, http.StatusBadGateway, ReasonUpstream, err.Error())
		return
	}

	// Resolve creds from the tenant workspace. We do this BEFORE
	// the kro write so a missing/forbidden secret 404s cheaply.
	// Skip when no tenant factory is configured (dev-mode without
	// a mounted kedge-provider-kubeconfig).
	var creds map[string][]byte
	if s.tenant != nil {
		creds, err = tenant.ResolveCloudCredentials(r.Context(), s.tenant, tenantPath)
		if err != nil {
			switch {
			case errors.Is(err, tenant.ErrCredentialsMissing):
				writeJSONError(w, http.StatusNotFound, ReasonCloudCredentialsMissing,
					"create a `cloud-credentials` Secret in your workspace's `default` namespace; see docs/credentials.md")
				return
			case errors.Is(err, tenant.ErrAPIBindingMissing):
				writeJSONError(w, http.StatusForbidden, ReasonAPIBindingMissing,
					"this provider is not enabled in your workspace; click Enable in the kedge portal")
				return
			default:
				writeJSONError(w, http.StatusBadGateway, ReasonUpstream, err.Error())
				return
			}
		}
	}

	inst, err := s.kro.CreateInstance(r.Context(), kro.CreateInstanceParams{
		TenantPath:   tenantPath,
		User:         user,
		Template:     *t,
		InstanceName: req.Name,
		Values:       req.Values,
		Credentials:  creds,
	})
	if err != nil {
		if apierrors.IsAlreadyExists(err) {
			writeJSONError(w, http.StatusConflict, ReasonConflict,
				"instance "+req.Name+" already exists; pick a different name")
			return
		}
		writeJSONError(w, http.StatusBadGateway, ReasonUpstream, "kro create: "+err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, inst)
}

func (s *Server) listInstances(w http.ResponseWriter, r *http.Request) {
	tenantPath := tenantFromRequest(r)
	if tenantPath == "" {
		writeJSONError(w, http.StatusBadRequest, ReasonTenantMissing, "X-Kedge-Tenant header required")
		return
	}
	instances, err := s.kro.ListInstances(r.Context(), tenantPath)
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, ReasonUpstream, "kro list: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, instanceListResponse{Items: instances})
}

func (s *Server) getInstance(w http.ResponseWriter, r *http.Request, name string) {
	tenantPath := tenantFromRequest(r)
	if tenantPath == "" {
		writeJSONError(w, http.StatusBadRequest, ReasonTenantMissing, "X-Kedge-Tenant header required")
		return
	}
	inst, err := s.kro.GetInstance(r.Context(), tenantPath, name)
	if err != nil {
		if errors.Is(err, kro.ErrInstanceNotFound) {
			writeJSONError(w, http.StatusNotFound, ReasonInstanceNotFound, "instance "+name+" not found")
			return
		}
		writeJSONError(w, http.StatusBadGateway, ReasonUpstream, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, inst)
}

func (s *Server) deleteInstance(w http.ResponseWriter, r *http.Request, name string) {
	tenantPath := tenantFromRequest(r)
	if tenantPath == "" {
		writeJSONError(w, http.StatusBadRequest, ReasonTenantMissing, "X-Kedge-Tenant header required")
		return
	}
	if err := s.kro.DeleteInstance(r.Context(), tenantPath, name); err != nil {
		if errors.Is(err, kro.ErrInstanceNotFound) {
			writeJSONError(w, http.StatusNotFound, ReasonInstanceNotFound, "instance "+name+" not found")
			return
		}
		writeJSONError(w, http.StatusBadGateway, ReasonUpstream, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// isDNSSubdomain is the same predicate kubernetes uses for resource
// names. We pre-validate so the user gets a 400 with a friendly
// message rather than a kubernetes-style "ResourceName invalid" mess.
func isDNSSubdomain(name string) bool {
	if name == "" || len(name) > 253 {
		return false
	}
	for i, r := range name {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9':
		case r == '-' || r == '.':
		default:
			_ = i
			return false
		}
	}
	// Must start and end with alphanumeric.
	first, last := name[0], name[len(name)-1]
	if !((first >= 'a' && first <= 'z') || (first >= '0' && first <= '9')) {
		return false
	}
	if !((last >= 'a' && last <= 'z') || (last >= '0' && last <= '9')) {
		return false
	}
	return true
}
