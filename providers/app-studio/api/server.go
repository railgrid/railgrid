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
	"context"
	"net/http"
	"sync"

	"github.com/gorilla/mux"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	aiv1alpha1 "github.com/faroshq/provider-app-studio/apis/ai/v1alpha1"
	asclient "github.com/faroshq/provider-app-studio/client"
	"github.com/faroshq/provider-app-studio/store"
	"github.com/faroshq/provider-app-studio/tenant"
	"github.com/faroshq/provider-app-studio/workspace"
)

// Server holds the dependencies the project handlers need. clients builds a
// per-(tenant, caller) dynamic client; store persists chat transcripts; hubBase
// locates the hub's MCP virtual workspace; mcpInsecureSkipTLSVerify relaxes TLS
// for dev MCP calls; workspaces stores project files owned by App Studio; and
// assistantEngine runs project assistant turns.
type Server struct {
	gql                          *tenant.GraphQLClient
	store                        store.Store
	workspaces                   *workspace.FileStore
	hubBase                      string
	mcpInsecureSkipTLSVerify     bool
	previewInsecureSkipTLSVerify bool
	assistantEngine              projectAssistantEngine
	// assistantThreadTitleGenerator is a test seam for the detached, one-shot
	// title request. Production leaves it nil and uses the connected project LLM.
	assistantThreadTitleGenerator func(context.Context, *asclient.Client, string) (string, error)
	assistantRunManager           *projectAssistantRunManager
	assistantSupervisor           *projectAssistantSupervisor
	assistantProjectionLocks      map[string]*assistantThreadProjectionLockEntry
	assistantThreadMirrors        map[string]struct{}
	developmentSyncLocks          map[string]*sync.Mutex
	developmentSyncTails          map[string]chan struct{}
	developmentSyncAfterMutation  func(identity, *aiv1alpha1.Project, string) error
	projectCreatePreflight        projectCreatePreflightGenerator
	// developmentSyncFailures records the most recent post-mutation sync
	// failure per project so verify_development_runtime can report it. A
	// failed background sync means the assistant's edits never reached the
	// sandbox; logging it alone made that invisible to the user AND to the
	// model, which would then diagnose a stale runtime as a code bug.
	developmentSyncFailures map[string]string
	// previewEdgeProbe + edgeReadyURLs implement the preview edge-readiness
	// gate (see preview_edge.go). Nil probe → the real HTTPS probe.
	previewEdgeProbe            func(context.Context, string) error
	edgeReadyURLs               edgeReadyURLsCache
	previewConsoleEnabled       bool
	previewConsoleStore         *previewConsoleStore
	previewConsoleSigner        *previewConsoleCapabilitySigner
	previewInspector            projectAssistantPreviewInspector
	previewInspectionResolveURL func(context.Context, identity, *aiv1alpha1.Project) (string, error)
	mu                          sync.Mutex
}

// New constructs a Server.
func New(gql *tenant.GraphQLClient, msgStore store.Store, hubBase string, mcpInsecureSkipTLSVerify bool) *Server {
	return NewWithWorkspace(gql, msgStore, nil, hubBase, mcpInsecureSkipTLSVerify)
}

// NewWithWorkspace constructs a Server with an explicit project workspace store.
func NewWithWorkspace(gql *tenant.GraphQLClient, msgStore store.Store, workspaces *workspace.FileStore, hubBase string, mcpInsecureSkipTLSVerify bool) *Server {
	return NewWithWorkspaceContext(context.Background(), gql, msgStore, workspaces, hubBase, mcpInsecureSkipTLSVerify)
}

// NewWithWorkspaceContext binds assistant workers to the provider lifecycle.
func NewWithWorkspaceContext(parent context.Context, gql *tenant.GraphQLClient, msgStore store.Store, workspaces *workspace.FileStore, hubBase string, mcpInsecureSkipTLSVerify bool) *Server {
	s := &Server{
		gql:                      gql,
		store:                    msgStore,
		workspaces:               workspaces,
		hubBase:                  hubBase,
		mcpInsecureSkipTLSVerify: mcpInsecureSkipTLSVerify,
	}
	s.assistantEngine = NewEinoAssistantEngine(s)
	s.assistantRunManager = newProjectAssistantRunManager()
	s.assistantSupervisor = newProjectAssistantSupervisor(parent, msgStore)
	return s
}

func (s *Server) Shutdown(ctx context.Context) { s.projectAssistantSupervisor().Shutdown(ctx) }

func (s *Server) projectAssistantSupervisor() *projectAssistantSupervisor {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.assistantSupervisor == nil {
		s.assistantSupervisor = newProjectAssistantSupervisor(context.Background(), s.store)
	}
	return s.assistantSupervisor
}

func (s *Server) projectAssistantEngine() projectAssistantEngine {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.assistantEngine == nil {
		s.assistantEngine = NewEinoAssistantEngine(s)
	}
	return s.assistantEngine
}

func (s *Server) projectAssistantRunManager() *projectAssistantRunManager {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.assistantRunManager == nil {
		s.assistantRunManager = newProjectAssistantRunManager()
	}
	return s.assistantRunManager
}

func (s *Server) developmentSyncLock(id identity, project *aiv1alpha1.Project) *sync.Mutex {
	key := developmentSyncFailureKey(id, project)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.developmentSyncLocks == nil {
		s.developmentSyncLocks = map[string]*sync.Mutex{}
	}
	lock := s.developmentSyncLocks[key]
	if lock == nil {
		lock = &sync.Mutex{}
		s.developmentSyncLocks[key] = lock
	}
	return lock
}

// Register mounts the project routes onto r. The hub backend proxy strips the
// /services/providers/app-studio prefix, so paths are registered bare.
func (s *Server) Register(r *mux.Router) {
	r.HandleFunc("/metrics", s.previewConsoleMetrics).Methods(http.MethodGet)
	r.HandleFunc("/api/projects", s.listProjects).Methods(http.MethodGet)
	r.HandleFunc("/api/projects", s.createProject).Methods(http.MethodPost)
	r.HandleFunc("/api/projects/create-readiness", s.getProjectCreateReadiness).Methods(http.MethodGet)
	r.HandleFunc("/api/projects/development-templates", s.listDevelopmentTemplates).Methods(http.MethodGet)
	r.HandleFunc("/api/projects/import-repositories", s.listImportRepositories).Methods(http.MethodGet)
	r.HandleFunc("/api/projects/llm-settings", s.getProjectLLMSettings).Methods(http.MethodGet)
	r.HandleFunc("/api/projects/llm-settings", s.patchProjectLLMSettings).Methods(http.MethodPatch)
	r.HandleFunc("/api/projects/{project}", s.getProject).Methods(http.MethodGet)
	r.HandleFunc("/api/projects/{project}", s.patchProject).Methods(http.MethodPatch)
	r.HandleFunc("/api/projects/{project}", s.deleteProject).Methods(http.MethodDelete)
	r.HandleFunc("/api/projects/{project}/assistant/threads", s.listProjectAssistantThreads).Methods(http.MethodGet)
	r.HandleFunc("/api/projects/{project}/assistant/skills", s.getProjectAssistantSkills).Methods(http.MethodGet)
	r.HandleFunc("/api/projects/{project}/assistant/skills/detail", s.getProjectAssistantSkillDetailByID).Methods(http.MethodGet)
	r.HandleFunc("/api/projects/{project}/assistant/skills/project", s.createProjectAssistantSkill).Methods(http.MethodPost)
	r.HandleFunc("/api/projects/{project}/assistant/skills/project/import", s.importProjectAssistantSkill).Methods(http.MethodPost)
	r.HandleFunc("/api/projects/{project}/assistant/skills/activation", s.setProjectAssistantSkillActivation).Methods(http.MethodPost)
	r.HandleFunc("/api/projects/{project}/assistant/skills/project/{packageName:.*}/export", s.exportProjectAssistantSkill).Methods(http.MethodGet)
	r.HandleFunc("/api/projects/{project}/assistant/skills/project/{packageName:.*}", s.getProjectAssistantSkillDetail).Methods(http.MethodGet)
	r.HandleFunc("/api/projects/{project}/assistant/skills/project/{packageName:.*}", s.updateProjectAssistantSkill).Methods(http.MethodPut)
	r.HandleFunc("/api/projects/{project}/assistant/skills/project/{packageName:.*}", s.deleteProjectAssistantSkill).Methods(http.MethodDelete)
	r.HandleFunc("/api/projects/{project}/assistant/threads", s.createProjectAssistantThread).Methods(http.MethodPost)
	r.HandleFunc("/api/projects/{project}/assistant/threads/{thread}", s.patchProjectAssistantThread).Methods(http.MethodPatch)
	r.HandleFunc("/api/projects/{project}/assistant/threads/{thread}", s.deleteProjectAssistantThread).Methods(http.MethodDelete)
	r.HandleFunc("/api/projects/{project}/assistant/threads/{thread}/items", s.listProjectAssistantThreadItems).Methods(http.MethodGet)
	r.HandleFunc("/api/projects/{project}/assistant/threads/{thread}/events", s.streamProjectAssistantThreadEvents).Methods(http.MethodGet)
	r.HandleFunc("/api/projects/{project}/assistant/threads/{thread}/turns", s.startProjectAssistantThreadTurn).Methods(http.MethodPost)
	r.HandleFunc("/api/projects/{project}/assistant/threads/{thread}/reviews", s.startProjectAssistantThreadReview).Methods(http.MethodPost)
	r.HandleFunc("/api/projects/{project}/assistant/threads/{thread}/turns/active", s.activeProjectAssistantThreadTurn).Methods(http.MethodGet)
	r.HandleFunc("/api/projects/{project}/assistant/threads/{thread}/turns/{turn}/steer", s.steerProjectAssistantThreadTurn).Methods(http.MethodPost)
	r.HandleFunc("/api/projects/{project}/assistant/threads/{thread}/turns/{turn}/interrupt", s.interruptProjectAssistantThreadTurn).Methods(http.MethodPost)
	r.HandleFunc("/api/projects/{project}/assistant/threads/{thread}/turns/{turn}/approval", s.respondProjectAssistantThreadTurn).Methods(http.MethodPost)
	r.HandleFunc("/api/projects/{project}/assistant/threads/{thread}/turns/{turn}/input", s.respondProjectAssistantThreadTurn).Methods(http.MethodPost)
	r.HandleFunc("/api/projects/{project}/template", s.putProjectTemplate).Methods(http.MethodPut)
	r.HandleFunc("/api/projects/{project}/promotion", s.getProjectPromotion).Methods(http.MethodGet)
	r.HandleFunc("/api/projects/{project}/checkpoints", s.getProjectCheckpoints).Methods(http.MethodGet)
	r.HandleFunc("/api/projects/{project}/promote", s.promoteProjectHandler).Methods(http.MethodPost)
	r.HandleFunc("/api/projects/{project}/hydrate-workspace", s.hydrateProjectWorkspace).Methods(http.MethodPost)
	r.HandleFunc("/api/projects/{project}/sync-development", s.syncProjectDevelopment).Methods(http.MethodPost)
	r.HandleFunc("/api/projects/{project}/restart-development", s.restartProjectDevelopment).Methods(http.MethodPost)
	r.HandleFunc("/api/projects/{project}/development-logs", s.logsProjectDevelopment).Methods(http.MethodGet)
	r.HandleFunc("/api/projects/{project}/development-status", s.statusProjectDevelopment).Methods(http.MethodGet)
	r.HandleFunc("/api/projects/{project}/authorize-development-preview", s.authorizeProjectDevelopmentPreview).Methods(http.MethodPost)
	r.HandleFunc("/api/projects/{project}/preview-console/sessions", s.createProjectPreviewConsoleSession).Methods(http.MethodPost)
	r.HandleFunc("/api/projects/{project}/preview-console/sessions/{session}/events", s.appendProjectPreviewConsoleEvents).Methods(http.MethodPost)
	r.HandleFunc("/api/projects/{project}/preview-console/sessions/{session}", s.deleteProjectPreviewConsoleSession).Methods(http.MethodDelete)
	r.HandleFunc("/api/projects/{project}/assistant/approval-mode", s.getProjectAssistantApprovalMode).Methods(http.MethodGet)
	r.HandleFunc("/api/projects/{project}/assistant/approval-mode", s.patchProjectAssistantApprovalMode).Methods(http.MethodPatch)
	r.HandleFunc("/api/projects/{project}/memory", s.getProjectMemory).Methods(http.MethodGet)
	r.HandleFunc("/api/projects/{project}/memory", s.patchProjectMemory).Methods(http.MethodPatch)
}

// clientFor builds a workspace-scoped client acting as the caller, talking to
// the hub's GraphQL gateway for the caller's current workspace cluster.
func (s *Server) clientFor(id identity) (*asclient.Client, error) {
	scope, err := s.gql.For(id.clusterID, id.token)
	if err != nil {
		return nil, err
	}
	return asclient.NewFromGraphQL(scope), nil
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
	if s.gql == nil {
		writeStatus(w, http.StatusNotImplemented, "NotImplemented", "tenant GraphQL client not configured — provider has no hub URL")
		return nil, identity{}, false
	}
	if id.clusterID == "" {
		writeStatus(w, http.StatusBadRequest, "BadRequest", "no workspace cluster on request (X-Kedge-Cluster missing) — the hub did not resolve a cluster for this workspace")
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
