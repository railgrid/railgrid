// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Package api is the vibe-studio HTTP surface: session CRUD, submissions, and
// the SSE event projection. The coordinator here owns all I/O around the pure
// session machine: it folds the log, Applies commands, appends the produced
// events (optimistic, ordinal-CAS), and executes whatever NextAction names —
// engine turns and provisioning run async, everything else waits for the user.
package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"maps"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	vibev1alpha1 "github.com/faroshq/provider-vibe-studio/apis/vibe/v1alpha1"
	"github.com/faroshq/provider-vibe-studio/client"
	"github.com/faroshq/provider-vibe-studio/provision"
	"github.com/faroshq/provider-vibe-studio/session"
	"github.com/faroshq/provider-vibe-studio/store"
	"github.com/faroshq/provider-vibe-studio/tenant"
)

// Server carries the provider's HTTP state.
type Server struct {
	store  store.Store
	engine session.Engine

	// gql reaches tenant workspaces through the hub's GraphQL gateway as the
	// calling user. Nil disables kcp writes (tests, hub-less dev) — provision
	// then records the Project checkpoint as blocked instead of failing.
	gql *tenant.GraphQLClient

	// devTenant substitutes for the hub-injected tenant header in local dev.
	// Empty means the header is required.
	devTenant string

	// inflight single-flights per-session background work keyed by
	// "<kind>/<session>" (GET re-kicks are frequent; the work must not stack).
	inflightMu sync.Mutex
	inflight   map[string]bool

	// partials holds the in-progress assistant text of running studio turns,
	// keyed by session id. Transient, replica-local — the view surfaces it so
	// the portal streams replies; the durable message lands as an event when
	// the turn completes.
	partialsMu sync.Mutex
	partials   map[string]string
}

func (s *Server) appendPartial(id, delta string) {
	s.partialsMu.Lock()
	defer s.partialsMu.Unlock()
	if s.partials == nil {
		s.partials = map[string]string{}
	}
	s.partials[id] += delta
}

func (s *Server) takePartial(id string) {
	s.partialsMu.Lock()
	defer s.partialsMu.Unlock()
	delete(s.partials, id)
}

func (s *Server) partialFor(id string) string {
	s.partialsMu.Lock()
	defer s.partialsMu.Unlock()
	return s.partials[id]
}

func (s *Server) begin(key string) bool {
	s.inflightMu.Lock()
	defer s.inflightMu.Unlock()
	if s.inflight == nil {
		s.inflight = map[string]bool{}
	}
	if s.inflight[key] {
		return false
	}
	s.inflight[key] = true
	return true
}

func (s *Server) end(key string) {
	s.inflightMu.Lock()
	defer s.inflightMu.Unlock()
	delete(s.inflight, key)
}

// NewServer wires the API. engine is the model harness (ScriptedEngine in
// Phase 0); gql may be nil (no tenant-workspace writes); devTenant non-empty
// allows headerless local requests.
func NewServer(st store.Store, engine session.Engine, gql *tenant.GraphQLClient, devTenant string) *Server {
	return &Server{store: st, engine: engine, gql: gql, devTenant: devTenant}
}

// SetEngine swaps the model harness after construction (the Eino engine needs
// the Server's store/dataplane/gateway plumbing, so it is wired second).
func (s *Server) SetEngine(engine session.Engine) { s.engine = engine }

// callerAuth is the per-request identity provisioning acts with: the workspace
// cluster ID and the caller's bearer, both hub-verified. Never persisted —
// captured from the triggering request and used only for that request's
// follow-on work.
type callerAuth struct {
	clusterID string
	token     string
}

func callerAuthFromRequest(r *http.Request) callerAuth {
	auth := callerAuth{clusterID: strings.TrimSpace(r.Header.Get("X-Kedge-Cluster"))}
	const p = "Bearer "
	if h := r.Header.Get("Authorization"); len(h) > len(p) && strings.EqualFold(h[:len(p)], p) {
		auth.token = strings.TrimSpace(h[len(p):])
	}
	return auth
}

// Register mounts the API routes.
func (s *Server) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/sessions", s.handleListSessions)
	mux.HandleFunc("POST /api/sessions", s.handleCreateSession)
	mux.HandleFunc("GET /api/sessions/{id}", s.handleGetSession)
	mux.HandleFunc("POST /api/sessions/{id}/submissions", s.handleSubmit)
	mux.HandleFunc("GET /api/sessions/{id}/events", s.handleEvents)
	mux.HandleFunc("GET /api/sessions/{id}/files", s.handleListFiles)
	mux.HandleFunc("GET /api/sessions/{id}/files/content", s.handleReadFile)
	mux.HandleFunc("PUT /api/sessions/{id}/files/content", s.handleWriteFile)
	mux.HandleFunc("GET /api/projects", s.handleListProjects)
	mux.HandleFunc("DELETE /api/sessions/{id}", s.handleDeleteSession)
	mux.HandleFunc("DELETE /api/projects/{name}", s.handleDeleteProject)
	mux.HandleFunc("GET /api/models", s.handleListModels)
	mux.HandleFunc("POST /api/models", s.handleCreateModel)
	mux.HandleFunc("DELETE /api/models/{name}", s.handleDeleteModel)
	mux.HandleFunc("POST /api/models/{name}/default", s.handleSetDefaultModel)
	mux.HandleFunc("GET /api/sessions/{id}/model", s.handleGetSessionModel)
	mux.HandleFunc("PUT /api/sessions/{id}/model", s.handleSetSessionModel)
	mux.HandleFunc("GET /api/sessions/{id}/promotion", s.handleGetPromotion)
	mux.HandleFunc("POST /api/sessions/{id}/promote", s.handlePromote)
}

// handleDeleteSession deletes the conversation KRM-first: remove the Session
// CR and let its finalizer purge the store while ownership GC takes the
// Project and instances. Sessions with no CR (old drafts, hub-less dev) are
// purged from the store directly.
func (s *Server) handleDeleteSession(w http.ResponseWriter, r *http.Request) {
	scope, err := s.scope(r)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
		return
	}
	id := r.PathValue("id")
	auth := callerAuthFromRequest(r)
	deletedCR := false
	if s.gql != nil && auth.clusterID != "" && auth.token != "" {
		if tenantScope, err := s.gql.For(auth.clusterID, auth.token); err == nil {
			if err := client.New(tenantScope).DeleteSession(r.Context(), id); err == nil {
				deletedCR = true
			}
		}
	}
	if !deletedCR {
		if err := s.store.PurgeSession(r.Context(), scope, id); err != nil {
			writeError(w, err)
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleDeleteProject deletes the Project CR as the caller; the reconciler's
// finalizer tears down its instances. The conversation survives as a draft.
func (s *Server) handleDeleteProject(w http.ResponseWriter, r *http.Request) {
	if _, err := s.scope(r); err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
		return
	}
	auth := callerAuthFromRequest(r)
	if s.gql == nil || auth.clusterID == "" || auth.token == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "no workspace identity on request"})
		return
	}
	tenantScope, err := s.gql.For(auth.clusterID, auth.token)
	if err != nil {
		writeError(w, err)
		return
	}
	if err := client.New(tenantScope).DeleteProject(r.Context(), r.PathValue("name")); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// projectListItem is the home page's card DTO — kube truth (the Project CR)
// joined with its session id for opening the chat.
type projectListItem struct {
	Name        string `json:"name"`
	DisplayName string `json:"displayName"`
	Template    string `json:"template,omitempty"`
	Phase       string `json:"phase,omitempty"`
	PreviewURL  string `json:"previewURL,omitempty"`
	SessionID   string `json:"sessionID,omitempty"`
	UpdatedAt   string `json:"updatedAt,omitempty"`
}

// handleListProjects lists the caller's Project CRs. This — not the session
// store — is the source of truth for "your apps": a session whose Project is
// gone is a draft/stale conversation, not an app.
func (s *Server) handleListProjects(w http.ResponseWriter, r *http.Request) {
	if _, err := s.scope(r); err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
		return
	}
	auth := callerAuthFromRequest(r)
	if s.gql == nil || auth.clusterID == "" || auth.token == "" {
		writeJSON(w, http.StatusOK, map[string]any{"items": []projectListItem{}})
		return
	}
	tenantScope, err := s.gql.For(auth.clusterID, auth.token)
	if err != nil {
		writeError(w, err)
		return
	}
	projects, err := client.New(tenantScope).ListProjects(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	items := make([]projectListItem, 0, len(projects))
	for _, p := range projects {
		item := projectListItem{
			Name:        p.Name,
			DisplayName: firstNonEmpty(p.Spec.DisplayName, p.Name),
			Phase:       p.Status.Phase,
			SessionID:   p.Labels["vibe.kedge.faros.sh/session"],
			UpdatedAt:   p.CreationTimestamp.UTC().Format(time.RFC3339),
		}
		if p.Spec.Template != nil {
			item.Template = p.Spec.Template.Name
		}
		if p.Status.UpdatedAt != nil {
			item.UpdatedAt = p.Status.UpdatedAt.UTC().Format(time.RFC3339)
		}
		for _, env := range p.Status.Environments {
			for _, b := range env.Bindings {
				if b.URL != "" {
					item.PreviewURL = b.URL
				}
			}
		}
		items = append(items, item)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

// handleListFiles returns the workspace file paths (the Code tab's tree).
func (s *Server) handleListFiles(w http.ResponseWriter, r *http.Request) {
	scope, err := s.scope(r)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
		return
	}
	paths, err := s.store.ListWorkspaceFiles(r.Context(), scope, r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	if paths == nil {
		paths = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": paths})
}

// handleWriteFile saves an edited workspace file and pushes it to the running
// sandbox — the same path the assistant's write_file tool takes, so hand
// edits and model edits are indistinguishable downstream.
func (s *Server) handleWriteFile(w http.ResponseWriter, r *http.Request) {
	scope, err := s.scope(r)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
		return
	}
	id := r.PathValue("id")
	var req struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.Path) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "body must be {\"path\": \"...\", \"content\": \"...\"}"})
		return
	}
	if len(req.Content) > store.MaxWorkspaceFileBytes {
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{
			"error": fmt.Sprintf("file exceeds %d bytes", store.MaxWorkspaceFileBytes),
		})
		return
	}
	if err := s.store.PutWorkspaceFiles(r.Context(), scope, id,
		[]store.WorkspaceFile{{Path: req.Path, Content: req.Content}}, time.Now()); err != nil {
		writeError(w, err)
		return
	}
	// Best effort: a save that lands in the workspace but not the sandbox is
	// reported, not failed — the file is safe and the next sync reconciles.
	synced, reason := s.syncOneFile(r.Context(), scope, id, callerAuthFromRequest(r), req.Path, req.Content)
	writeJSON(w, http.StatusOK, map[string]any{"saved": true, "synced": synced, "reason": reason})
}

// syncOneFile pushes a single workspace file into the session's dev sandbox.
func (s *Server) syncOneFile(ctx context.Context, scope store.Scope, id string, auth callerAuth, path, content string) (bool, string) {
	state, err := s.foldSession(ctx, scope, id)
	if err != nil || state.ProjectName == "" {
		return false, "no development sandbox for this session yet"
	}
	if s.gql == nil || auth.clusterID == "" || auth.token == "" {
		return false, "no workspace identity on request"
	}
	tenantScope, err := s.gql.For(auth.clusterID, auth.token)
	if err != nil {
		return false, err.Error()
	}
	cl := client.New(tenantScope)
	p, err := cl.GetProject(ctx, state.ProjectName)
	if err != nil || p.Spec.Template == nil {
		return false, "project or template not found"
	}
	tmpl, err := cl.GetTemplate(ctx, p.Spec.Template.Name)
	if err != nil {
		return false, "template not readable"
	}
	info, err := provision.ParseDevInfo(tmpl)
	if err != nil || len(info.Components) == 0 {
		return false, "template declares no development components"
	}
	ref := provision.Ref{Resource: info.Resource, Name: state.ProjectName}
	pc := provision.NewClient(hubBaseURL(), auth.clusterID, auth.token, hubInsecure())
	if _, err := pc.SyncFiles(ctx, ref, info.Components,
		[]provision.File{{Path: path, Content: content}}); err != nil {
		return false, err.Error()
	}
	return true, ""
}

// handleReadFile returns one workspace file's content (read-only viewer).
func (s *Server) handleReadFile(w http.ResponseWriter, r *http.Request) {
	scope, err := s.scope(r)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
		return
	}
	path := r.URL.Query().Get("path")
	if path == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "path query parameter is required"})
		return
	}
	f, err := s.store.GetWorkspaceFile(r.Context(), scope, r.PathValue("id"), path)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, f)
}

// scope resolves the hub-verified tenant. The hub backend proxy injects
// X-Kedge-Tenant on every request it forwards.
func (s *Server) scope(r *http.Request) (store.Scope, error) {
	tenant := strings.TrimSpace(r.Header.Get("X-Kedge-Tenant"))
	if tenant == "" {
		tenant = s.devTenant
	}
	if tenant == "" {
		return store.Scope{}, errors.New("missing X-Kedge-Tenant")
	}
	return store.Scope{Tenant: tenant}, nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "session not found"})
	case errors.Is(err, session.ErrConflict), errors.Is(err, store.ErrOrdinalConflict):
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
	default:
		// Deliberately not err.Error(): internals stay in logs, not clients.
		log.Printf("api error: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
	}
}

func newID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// sessionView is the state snapshot the portal renders between events.
type sessionView struct {
	ID          string                                        `json:"id"`
	Phase       session.Phase                                 `json:"phase"`
	NextAction  session.Action                                `json:"nextAction"`
	ProjectName string                                        `json:"projectName,omitempty"`
	PreviewURL  string                                        `json:"previewURL,omitempty"`
	Partial     string                                        `json:"partial,omitempty"`
	Blueprint   *session.Blueprint                            `json:"blueprint,omitempty"`
	Questions   []session.Question                            `json:"questions,omitempty"`
	Checkpoints map[session.CheckpointName]session.Checkpoint `json:"checkpoints,omitempty"`
	LastOrdinal int64                                         `json:"lastOrdinal"`
}

func viewOf(state session.SessionState) sessionView {
	return sessionView{
		ID:          state.ID,
		Phase:       state.Phase,
		NextAction:  session.NextAction(state),
		ProjectName: state.ProjectName,
		PreviewURL:  state.PreviewURL,
		Blueprint:   state.Blueprint,
		Questions:   state.PendingQuestions,
		Checkpoints: state.Checkpoints,
		LastOrdinal: state.LastOrdinal,
	}
}

// foldSession loads and folds one session's full log.
func (s *Server) foldSession(ctx context.Context, scope store.Scope, id string) (session.SessionState, error) {
	events, err := s.store.ListEvents(ctx, scope, id, 0, 0)
	if err != nil {
		return session.SessionState{}, err
	}
	state := session.Fold(events)
	if state.ID == "" {
		// Session row exists but the create event hasn't landed — treat as
		// not found rather than exposing a half-open session.
		if len(events) == 0 {
			return session.SessionState{}, store.ErrNotFound
		}
	}
	return state, nil
}

// submitCmd folds the session, Applies cmd, and appends the events, retrying
// once on a concurrent append. Returns the post-command state.
func (s *Server) submitCmd(ctx context.Context, scope store.Scope, id string, cmd session.Command) (session.SessionState, error) {
	_, isCreate := cmd.(session.CmdCreate)
	for attempt := 0; ; attempt++ {
		state, err := s.foldSession(ctx, scope, id)
		if err != nil {
			// The create command legitimately finds an empty log — the
			// session row exists, the first events are what it appends.
			if !(isCreate && errors.Is(err, store.ErrNotFound)) {
				return session.SessionState{}, err
			}
			state = session.SessionState{}
		}
		events, err := session.Apply(state, cmd, time.Now())
		if err != nil {
			return session.SessionState{}, err
		}
		last, err := s.store.AppendEvents(ctx, scope, id, state.LastOrdinal, events)
		if errors.Is(err, store.ErrOrdinalConflict) && attempt < 2 {
			continue
		}
		if err != nil {
			return session.SessionState{}, err
		}
		for i := range events {
			events[i].Ordinal = last - int64(len(events)-1-i)
			state = session.Evolve(state, events[i])
		}
		if err := s.store.TouchSession(ctx, scope, id, state.Phase, time.Now()); err != nil {
			log.Printf("touch session %s: %v", id, err)
		}
		return state, nil
	}
}

func (s *Server) handleListSessions(w http.ResponseWriter, r *http.Request) {
	scope, err := s.scope(r)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
		return
	}
	list, err := s.store.ListSessions(r.Context(), scope, 100)
	if err != nil {
		writeError(w, err)
		return
	}
	if list == nil {
		list = []store.SessionRecord{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": list})
}

func (s *Server) handleCreateSession(w http.ResponseWriter, r *http.Request) {
	scope, err := s.scope(r)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
		return
	}
	var req struct {
		Input string `json:"input"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.Input) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "body must be {\"input\": \"...\"}"})
		return
	}
	id := newID()
	now := time.Now()
	if err := s.store.CreateSession(r.Context(), scope, id, store.Preview(req.Input), now); err != nil {
		writeError(w, err)
		return
	}
	state, err := s.submitCmd(r.Context(), scope, id, session.CmdCreate{
		SessionID: id, SubmissionID: newID(), Input: strings.TrimSpace(req.Input),
	})
	if err != nil {
		writeError(w, err)
		return
	}
	auth := callerAuthFromRequest(r)
	// The Session CR is the conversation's control-plane object: its
	// finalizer purges the store on deletion, its status mirrors the log.
	go s.ensureSessionCR(scope, auth, id, strings.TrimSpace(req.Input))
	s.advanceAsync(scope, id, auth)
	writeJSON(w, http.StatusCreated, viewOf(state))
}

func (s *Server) handleGetSession(w http.ResponseWriter, r *http.Request) {
	scope, err := s.scope(r)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
		return
	}
	state, err := s.foldSession(r.Context(), scope, r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	// Provisioning, preview backfill, and seed retries are the Session
	// reconciler's job now — it runs as the session's own ServiceAccount and
	// retries on its own schedule, so a view is mostly a read.
	//
	// The exception is a turn the session already owes: the reconciler queues
	// the first build turn when provisioning finishes (and a crashed replica
	// can leave input pending), but only this process runs the engine. Kick
	// it from the view so owed work starts without the user having to type
	// something to wake it. Single-flighted, and advance() CASes on
	// turn.started anyway, so a poll every couple of seconds is harmless.
	switch session.NextAction(state) {
	case session.ActionRunStudioTurn, session.ActionRunIntakeTurn:
		s.advanceAsync(scope, state.ID, callerAuthFromRequest(r))
	}
	view := viewOf(state)
	if state.ActiveTurnID != "" {
		view.Partial = s.partialFor(state.ID)
	}
	writeJSON(w, http.StatusOK, view)
}

// handleSubmit accepts the user-facing submission kinds. The op vocabulary is
// deliberately small: input | answers | approve.
func (s *Server) handleSubmit(w http.ResponseWriter, r *http.Request) {
	scope, err := s.scope(r)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
		return
	}
	id := r.PathValue("id")
	var req struct {
		Kind    string            `json:"kind"`
		Text    string            `json:"text,omitempty"`
		Answers map[string]string `json:"answers,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json body"})
		return
	}
	subID := newID()
	var cmd session.Command
	switch req.Kind {
	case "input":
		cmd = session.CmdUserInput{SubmissionID: subID, Text: strings.TrimSpace(req.Text)}
	case "answers":
		cmd = session.CmdWizardAnswers{SubmissionID: subID, Answers: req.Answers}
	case "approve":
		cmd = session.CmdApproveBlueprint{SubmissionID: subID}
	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("unknown submission kind %q", req.Kind)})
		return
	}
	state, err := s.submitCmd(r.Context(), scope, id, cmd)
	if err != nil {
		writeError(w, err)
		return
	}
	auth := callerAuthFromRequest(r)
	if req.Kind == "approve" {
		// Approval writes down what the user asked for — the Session and
		// Project spec — as the caller, because only they can read the
		// Template catalog and their code Connection. Everything after this
		// (sandbox, scaffold, git, preview) converges in the Session
		// reconciler under the session's own identity.
		if err := s.recordApproval(r.Context(), scope, id, state, auth); err != nil {
			log.Printf("recording approval for session %s: %v", id, err)
			if cerr := s.setCheckpoint(r.Context(), scope, id, session.Checkpoint{
				Name: session.CheckpointTemplate, State: session.CheckpointError,
				Reason: "could not create the project: " + err.Error(),
			}); cerr != nil {
				log.Printf("checkpoint for session %s: %v", id, cerr)
			}
		}
	}
	s.advanceAsync(scope, id, auth)
	writeJSON(w, http.StatusAccepted, viewOf(state))
}

// advanceAsync executes owed work off the request goroutine. auth is the
// triggering caller's identity — provisioning acts as that user.
func (s *Server) advanceAsync(scope store.Scope, id string, auth callerAuth) {
	// Single-flighted per session: the view kicks this on every poll, and one
	// runner per session is all the log's turn CAS would let through anyway.
	key := "advance/" + id
	if !s.begin(key) {
		return
	}
	go func() {
		defer s.end(key)
		// Detached from the request context: turns outlive the HTTP call.
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		if err := s.advance(ctx, scope, id, auth); err != nil {
			log.Printf("advance session %s: %v", id, err)
		}
	}()
}

// advance loops NextAction until the session waits on the user. Concurrency
// safety comes from the append CAS: if two replicas race, one CmdTurnStarted
// loses the append and backs off.
func (s *Server) advance(ctx context.Context, scope store.Scope, id string, auth callerAuth) error {
	for {
		state, err := s.foldSession(ctx, scope, id)
		if err != nil {
			return err
		}
		switch session.NextAction(state) {
		case session.ActionRunIntakeTurn:
			if err := s.runIntakeTurn(ctx, scope, id, state, auth); err != nil {
				return err
			}
		case session.ActionRunStudioTurn:
			if err := s.runStudioTurn(ctx, scope, id, state, auth); err != nil {
				return err
			}
		case session.ActionRunProvision:
			// Owned by the Session reconciler; nothing for the HTTP path.
			return nil
		default:
			return nil
		}
	}
}

// runTurn brackets fn between turn.started and turn.completed/failed.
func (s *Server) runTurn(ctx context.Context, scope store.Scope, id string, state session.SessionState, fn func(ctx context.Context, state session.SessionState, turnSubmission string) error) error {
	turnID := newID()
	// Reuse the submission that queued the pending work so events correlate.
	subID := lastSubmission(state)
	if _, err := s.submitCmd(ctx, scope, id, session.CmdTurnStarted{SubmissionID: subID, TurnID: turnID}); err != nil {
		if errors.Is(err, session.ErrConflict) {
			return nil // another replica took the turn
		}
		return err
	}
	if err := fn(ctx, state, subID); err != nil {
		_, ferr := s.submitCmd(ctx, scope, id, session.CmdTurnFailed{TurnID: turnID, Reason: err.Error()})
		if ferr != nil {
			return errors.Join(err, ferr)
		}
		return err
	}
	_, err := s.submitCmd(ctx, scope, id, session.CmdTurnCompleted{TurnID: turnID})
	return err
}

// lastSubmission is a best-effort correlation id for engine-driven events.
// Phase 0 keeps it simple; the events still carry their own submission ids.
func lastSubmission(session.SessionState) string { return "" }

func turnContext(scope store.Scope, id string, auth callerAuth) session.TurnContext {
	return session.TurnContext{Tenant: scope.Tenant, ClusterID: auth.clusterID, Token: auth.token, SessionID: id}
}

// ensureSessionCR creates (or refreshes) the Session CR for a store session.
// Best effort: hub-less dev has no CR, and the store remains authoritative
// for the conversation either way.
func (s *Server) ensureSessionCR(scope store.Scope, auth callerAuth, id, intent string) *vibev1alpha1.Session {
	if s.gql == nil || auth.clusterID == "" || auth.token == "" {
		return nil
	}
	tenantScope, err := s.gql.For(auth.clusterID, auth.token)
	if err != nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cl := client.New(tenantScope)
	if existing, err := cl.GetSession(ctx, id); err == nil {
		return existing
	}
	sess := &vibev1alpha1.Session{}
	sess.Name = id
	sess.Annotations = map[string]string{vibev1alpha1.SessionTenantAnnotation: scope.Tenant}
	sess.Spec = vibev1alpha1.SessionSpec{Intent: intent}
	created, err := cl.ApplySession(ctx, sess)
	if err != nil {
		log.Printf("create session CR %s: %v", id, err)
		return nil
	}
	return created
}

// activitySink persists tool activity as turn.activity events (best effort —
// a failed append must never abort the turn itself).
func (s *Server) activitySink(ctx context.Context, scope store.Scope, id string) func(session.ToolActivityData) {
	return func(a session.ToolActivityData) {
		if _, err := s.submitCmd(ctx, scope, id, session.CmdToolActivity{Activity: a}); err != nil {
			log.Printf("record activity for session %s: %v", id, err)
		}
	}
}

func (s *Server) runIntakeTurn(ctx context.Context, scope store.Scope, id string, state session.SessionState, auth callerAuth) error {
	input, answers := state.PendingInput, state.PendingAnswers
	return s.runTurn(ctx, scope, id, state, func(ctx context.Context, state session.SessionState, subID string) error {
		tc := turnContext(scope, id, auth)
		tc.OnActivity = s.activitySink(ctx, scope, id)
		bp, err := s.engine.IntakeTurn(ctx, tc, state, input, answers)
		if err != nil {
			return err
		}
		_, err = s.submitCmd(ctx, scope, id, session.CmdBlueprintProposed{SubmissionID: subID, Blueprint: bp})
		return err
	})
}

func (s *Server) runStudioTurn(ctx context.Context, scope store.Scope, id string, state session.SessionState, auth callerAuth) error {
	input := state.PendingInput
	return s.runTurn(ctx, scope, id, state, func(ctx context.Context, state session.SessionState, subID string) error {
		defer s.takePartial(id)
		tc := turnContext(scope, id, auth)
		tc.OnDelta = func(delta string) { s.appendPartial(id, delta) }
		tc.OnActivity = s.activitySink(ctx, scope, id)
		reply, err := s.engine.StudioTurn(ctx, tc, state, input)
		if err != nil {
			return err
		}
		_, err = s.submitCmd(ctx, scope, id, session.CmdAssistantMessage{SubmissionID: subID, Text: reply})
		return err
	})
}

// setCheckpoint records a checkpoint only when it differs from the folded
// state, so resumable provision passes don't spam the event log.
func (s *Server) setCheckpoint(ctx context.Context, scope store.Scope, id string, cp session.Checkpoint) error {
	state, err := s.foldSession(ctx, scope, id)
	if err != nil {
		return err
	}
	if cur, ok := state.Checkpoints[cp.Name]; ok && cur == cp {
		return nil
	}
	_, err = s.submitCmd(ctx, scope, id, session.CmdCheckpointUpdated{Checkpoint: cp})
	return err
}

// applyProject applies the Project CR the blueprint describes: the runtime
// binding carries the fully-resolved instance GVR + values, so the Project
// reconciler can lifecycle the instance without ever reading Templates — the
// spec is self-contained and the controller stays deterministic. Idempotent
// (create-or-update via the gateway applyYaml).
func (s *Server) applyProject(ctx context.Context, cl *client.Client, sessionID string, bp session.Blueprint, info provision.DevInfo, name, connection string, owner *vibev1alpha1.Session) error {
	values := map[string]any{}
	maps.Copy(values, bp.Values)
	values["name"] = name
	values[templateKedgeModeField] = templateKedgeModeDevelopment
	raw, err := json.Marshal(values)
	if err != nil {
		return fmt.Errorf("encoding instance values: %w", err)
	}
	binding := vibev1alpha1.ProjectProviderBindingSpec{
		Name:     vibev1alpha1.BindingRuntime,
		Template: bp.Template.Name,
		Provider: "infrastructure",
		Kind:     vibev1alpha1.ProjectBindingKindProviderResource,
		ResourceRef: &vibev1alpha1.ProjectProviderResourceReference{
			Name:       name,
			APIVersion: info.Group + "/" + info.Version,
			Kind:       info.Kind,
			Resource:   info.Resource,
		},
		Values: runtime.RawExtension{Raw: raw},
	}
	bindings := []vibev1alpha1.ProjectProviderBindingSpec{binding}

	p := &vibev1alpha1.Project{}
	p.Name = name
	p.Labels = map[string]string{"vibe.kedge.faros.sh/session": sessionID}
	if owner != nil && owner.UID != "" {
		p.OwnerReferences = []metav1.OwnerReference{{
			APIVersion: vibev1alpha1.SchemeGroupVersion.String(),
			Kind:       "Session",
			Name:       owner.Name,
			UID:        owner.UID,
		}}
	}
	var repoBinding *vibev1alpha1.ProjectRepositoryBinding
	if connection != "" {
		// The Project reconciler converges the Repository CR from this
		// binding (autoInit creates the repo on the host); the provision
		// flow seeds it once with the scaffold.
		repoBinding = &vibev1alpha1.ProjectRepositoryBinding{
			RepositoryRef: name, Name: name, ConnectionRef: connection,
		}
		// Where the connection keeps its token, resolved as the caller. The
		// reconciler mints the registry pull credential from it — production
		// runs private images and would otherwise sit in ErrImagePull.
		if ref, login, err := cl.GitToken(ctx, connection); err != nil {
			log.Printf("resolving the git token for %s: %v", name, err)
		} else if ref != nil {
			repoBinding.TokenSecret = ref
			repoBinding.Login = login
		}
	}
	// Copy the template's development contract onto the spec so the Session
	// reconciler can provision without reading Templates (they ride virtual
	// storage with their own identity — a self-contained spec keeps the
	// control loop dependency-free).
	var development *vibev1alpha1.ProjectDevelopment
	if len(info.Components) > 0 || info.ScaffoldRepository != "" {
		development = &vibev1alpha1.ProjectDevelopment{}
		for _, name := range sortedComponentNames(info.Components) {
			development.Components = append(development.Components,
				vibev1alpha1.ProjectComponent{
					Name: name, Path: info.Components[name], ImageInput: info.ImageInputs[name],
				})
		}
		if info.ScaffoldRepository != "" {
			development.Scaffold = &vibev1alpha1.ProjectScaffold{
				Repository: info.ScaffoldRepository, Ref: info.ScaffoldRef,
			}
		}
	}
	p.Spec = vibev1alpha1.ProjectSpec{
		DisplayName: firstNonEmpty(bp.Title, "New app"),
		Description: bp.Summary,
		Repository:  repoBinding,
		Development: development,
		Template:    &vibev1alpha1.ProjectTemplateSpec{Name: bp.Template.Name},
		Environments: []vibev1alpha1.ProjectEnvironmentSpec{
			{
				Name:     vibev1alpha1.DevelopmentEnvironment,
				Mode:     vibev1alpha1.ProjectEnvironmentModeLive,
				Bindings: bindings,
			},
		},
	}
	// A re-apply must never un-ship a promoted app: carry any production
	// environment forward untouched (promote owns it, the wizard doesn't).
	if existing, gerr := cl.GetProject(ctx, name); gerr == nil {
		for _, env := range existing.Spec.Environments {
			if env.Name == productionEnvironment {
				p.Spec.Environments = append(p.Spec.Environments, env)
			}
		}
	}
	_, err = cl.ApplyProject(ctx, p)
	return err
}

// searchTemplate is the infrastructure Template backing web search: a private
// SearXNG instance with the JSON API on, reachable only over the data plane.
// One per WORKSPACE, owned by the Studio singleton — a search index has no
// per-project state, so a per-project instance ran N identical pods.
const searchTemplate = "searxng"

// Instance kedgeMode contract (providers/infrastructure/apis: KedgeModeField).
const (
	templateKedgeModeField       = "kedgeMode"
	templateKedgeModeDevelopment = "development"
	templateKedgeModeProduction  = "production"
)

// sortedComponentNames keeps the Project spec's component order stable so a
// re-apply is a no-op rather than a spec churn.
func sortedComponentNames(components map[string]string) []string {
	out := make([]string, 0, len(components))
	for k := range components {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// firstNonEmpty returns the first value with content.
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// projectName derives a DNS-safe, unique CR name from the blueprint title and
// session id (e.g. "barber-shop-booking-80024202").
func projectName(title, sessionID string) string {
	slug := strings.ToLower(title)
	slug = strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			return r
		default:
			return '-'
		}
	}, slug)
	for strings.Contains(slug, "--") {
		slug = strings.ReplaceAll(slug, "--", "-")
	}
	slug = strings.Trim(slug, "-")
	if len(slug) > 40 {
		slug = strings.Trim(slug[:40], "-")
	}
	if slug == "" {
		slug = "app"
	}
	suffix := sessionID
	if len(suffix) > 8 {
		suffix = suffix[:8]
	}
	return slug + "-" + suffix
}

// handleEvents streams the session log. With Accept: text/event-stream it
// serves SSE from ?after= onward (poll-based, so it behaves identically over
// Postgres with multiple replicas); otherwise it returns JSON.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	scope, err := s.scope(r)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
		return
	}
	id := r.PathValue("id")
	after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)

	if !strings.Contains(r.Header.Get("Accept"), "text/event-stream") {
		events, err := s.store.ListEvents(r.Context(), scope, id, after, 500)
		if err != nil {
			writeError(w, err)
			return
		}
		if events == nil {
			events = []session.Event{}
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": events})
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "streaming unsupported"})
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	poll := time.NewTicker(250 * time.Millisecond)
	defer poll.Stop()
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()

	cursor := after
	for {
		select {
		case <-r.Context().Done():
			return
		case <-heartbeat.C:
			fmt.Fprint(w, ": keepalive\n\n")
			flusher.Flush()
		case <-poll.C:
			events, err := s.store.ListEvents(r.Context(), scope, id, cursor, 100)
			if err != nil {
				return
			}
			for _, e := range events {
				payload, err := json.Marshal(e)
				if err != nil {
					continue
				}
				fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", e.Ordinal, e.Type, payload)
				cursor = e.Ordinal
			}
			if len(events) > 0 {
				flusher.Flush()
			}
		}
	}
}

// recordApproval turns an approved blueprint into spec: the Session CR, the
// Project CR (template binding, repository binding, and the development
// contract the reconciler needs), and the Session's projectRef. It does the
// reads only the caller can do — the Template catalog and the code
// Connection — and nothing long-running; the Session reconciler takes it
// from here.
func (s *Server) recordApproval(ctx context.Context, scope store.Scope, id string, state session.SessionState, auth callerAuth) error {
	if state.Blueprint == nil {
		return fmt.Errorf("session has no blueprint")
	}
	if s.gql == nil || auth.clusterID == "" || auth.token == "" {
		return fmt.Errorf("no workspace identity on the approving request")
	}
	tenantScope, err := s.gql.For(auth.clusterID, auth.token)
	if err != nil {
		return err
	}
	cl := client.New(tenantScope)
	bp := *state.Blueprint

	tmpl, err := cl.GetTemplate(ctx, bp.Template.Name)
	if err != nil {
		return fmt.Errorf("reading template %q: %w", bp.Template.Name, err)
	}
	info, err := provision.ParseDevInfo(tmpl)
	if err != nil {
		return fmt.Errorf("template %q: %w", bp.Template.Name, err)
	}

	connection, err := cl.FirstValidatedConnection(ctx)
	if err != nil {
		log.Printf("listing code connections for session %s: %v", id, err)
	}

	// Keep only values the template actually declares. A model drafting a
	// blueprint invents app-requirement fields ("features", "pages") that
	// are meaningful to the conversation but are not infrastructure inputs;
	// the full draft stays in the blueprint event, the spec stays honest.
	bp.Values = provision.FilterValues(bp.Values, provision.InputSchemas(tmpl))

	name := projectName(bp.Title, id)
	sessCR := s.ensureSessionCR(scope, auth, id, bp.Summary)
	if err := s.applyProject(ctx, cl, id, bp, info, name, connection, sessCR); err != nil {
		return err
	}
	if _, err := s.submitCmd(ctx, scope, id, session.CmdProjectCreated{Name: name}); err != nil &&
		!errors.Is(err, session.ErrConflict) {
		return err
	}
	// The projectRef is what tells the Session reconciler there is work.
	if sessCR != nil {
		sessCR.Spec.ProjectRef = &vibev1alpha1.SessionProjectRef{Name: name}
		if _, err := cl.ApplySession(ctx, sessCR); err != nil {
			return fmt.Errorf("setting projectRef: %w", err)
		}
	}
	return nil
}
