/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Package api serves the App Studio projects REST + LLM surface. It runs in
// the standalone provider binary: the hub's backend proxy forwards
// /services/providers/app-studio/* here (stripping that prefix), injecting the
// verified X-Kedge-Tenant/X-Kedge-User headers and forwarding the caller's
// bearer token. Every request therefore acts as the calling user against the
// tenant's kcp workspace — there is no provider service-account escalation.
package api

import (
	"net/http"

	"github.com/gorilla/mux"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	aiv1alpha1 "github.com/faroshq/provider-app-studio/apis/ai/v1alpha1"
	asclient "github.com/faroshq/provider-app-studio/client"
	"github.com/faroshq/provider-app-studio/store"
	"github.com/faroshq/provider-app-studio/tenant"
)

// Server holds the dependencies the project handlers need. clients builds a
// per-(tenant, caller) dynamic client; store persists chat transcripts; hubBase
// locates the hub's MCP virtual workspace; mcpInsecureSkipTLSVerify relaxes TLS
// for dev MCP calls.
type Server struct {
	clients                  *tenant.ClientFactory
	store                    store.Store
	hubBase                  string
	mcpInsecureSkipTLSVerify bool
}

// New constructs a Server.
func New(clients *tenant.ClientFactory, msgStore store.Store, hubBase string, mcpInsecureSkipTLSVerify bool) *Server {
	return &Server{
		clients:                  clients,
		store:                    msgStore,
		hubBase:                  hubBase,
		mcpInsecureSkipTLSVerify: mcpInsecureSkipTLSVerify,
	}
}

// Register mounts the project routes onto r. The hub backend proxy strips the
// /services/providers/app-studio prefix, so paths are registered bare.
func (s *Server) Register(r *mux.Router) {
	r.HandleFunc("/api/projects", s.listProjects).Methods(http.MethodGet)
	r.HandleFunc("/api/projects", s.createProject).Methods(http.MethodPost)
	r.HandleFunc("/api/projects/stream", s.createProjectStartStream).Methods(http.MethodPost)
	r.HandleFunc("/api/projects/llm-settings", s.getProjectLLMSettings).Methods(http.MethodGet)
	r.HandleFunc("/api/projects/llm-settings", s.patchProjectLLMSettings).Methods(http.MethodPatch)
	r.HandleFunc("/api/projects/{project}", s.getProject).Methods(http.MethodGet)
	r.HandleFunc("/api/projects/{project}", s.patchProject).Methods(http.MethodPatch)
	r.HandleFunc("/api/projects/{project}", s.deleteProject).Methods(http.MethodDelete)
	r.HandleFunc("/api/projects/{project}/messages", s.listProjectMessages).Methods(http.MethodGet)
	r.HandleFunc("/api/projects/{project}/messages/stream", s.createProjectMessageStream).Methods(http.MethodPost)
	r.HandleFunc("/api/projects/{project}/memory", s.getProjectMemory).Methods(http.MethodGet)
	r.HandleFunc("/api/projects/{project}/memory", s.patchProjectMemory).Methods(http.MethodPatch)
}

// clientFor builds a workspace-scoped client acting as the caller.
func (s *Server) clientFor(id identity) (*asclient.Client, error) {
	dyn, err := s.clients.For(id.tenantPath, id.token)
	if err != nil {
		return nil, err
	}
	return asclient.NewFromDynamic(dyn), nil
}

// requireProjectClient resolves the caller identity and a workspace-scoped
// client. Endpoints under /api/projects always require a workspace.
func (s *Server) requireProjectClient(w http.ResponseWriter, r *http.Request) (*asclient.Client, identity, bool) {
	id, ok := identityFromRequest(w, r)
	if !ok {
		return nil, identity{}, false
	}
	if id.workspaceUUID == "" {
		writeStatus(w, http.StatusBadRequest, "BadRequest", "a workspace is required for this endpoint — select an organization and workspace first")
		return nil, identity{}, false
	}
	if s.clients == nil {
		writeStatus(w, http.StatusNotImplemented, "NotImplemented", "tenant client factory not configured — provider has no kubeconfig")
		return nil, identity{}, false
	}
	c, err := s.clientFor(id)
	if err != nil {
		writeStatus(w, http.StatusInternalServerError, "InternalError", "creating project client: "+err.Error())
		return nil, identity{}, false
	}
	return c, id, true
}

// requireProjectWithClient additionally fetches the named Project.
func (s *Server) requireProjectWithClient(w http.ResponseWriter, r *http.Request) (*asclient.Client, identity, *aiv1alpha1.Project, bool) {
	c, id, ok := s.requireProjectClient(w, r)
	if !ok {
		return nil, identity{}, nil, false
	}
	name := mux.Vars(r)["project"]
	p, err := c.Projects().Get(r.Context(), name, metav1.GetOptions{})
	if err != nil {
		writeProjectError(w, err)
		return nil, identity{}, nil, false
	}
	return c, id, p, true
}

// requireProject fetches the named Project, discarding the client/identity.
func (s *Server) requireProject(w http.ResponseWriter, r *http.Request) (*aiv1alpha1.Project, bool) {
	_, _, p, ok := s.requireProjectWithClient(w, r)
	return p, ok
}

// requireStore guards against a nil message store.
func (s *Server) requireStore(w http.ResponseWriter) (store.Store, bool) {
	if s.store == nil {
		writeStatus(w, http.StatusNotImplemented, "NotImplemented", "project message store not configured on this provider")
		return nil, false
	}
	return s.store, true
}
