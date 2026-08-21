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
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/google/uuid"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	agentsv1alpha1 "github.com/faroshq/provider-agents/apis/v1alpha1"
	agentsclient "github.com/faroshq/provider-agents/client"
	"github.com/faroshq/provider-agents/engine"
	"github.com/faroshq/provider-agents/llm"
)

// chatHistoryLimit bounds how many prior messages are replayed into a turn.
const chatHistoryLimit = 40

// writeResourceError maps a tenant-API error onto an HTTP status. Validation
// and permission failures keep their own class — mapping them all to 502 made
// a user's own bad input look like an upstream outage.
func writeResourceError(w http.ResponseWriter, err error) {
	switch {
	case apierrors.IsNotFound(err):
		writeStatus(w, http.StatusNotFound, "NotFound", err.Error())
	case apierrors.IsAlreadyExists(err), apierrors.IsConflict(err):
		writeStatus(w, http.StatusConflict, "Conflict", err.Error())
	case apierrors.IsInvalid(err), apierrors.IsBadRequest(err):
		writeStatus(w, http.StatusBadRequest, "BadRequest", err.Error())
	case apierrors.IsForbidden(err):
		writeStatus(w, http.StatusForbidden, "Forbidden", err.Error())
	case apierrors.IsUnauthorized(err):
		writeStatus(w, http.StatusUnauthorized, "Unauthorized", err.Error())
	default:
		// The GraphQL gateway flattens admission errors into plain messages, so
		// sniff the well-known validation phrases before blaming the upstream.
		msg := strings.ToLower(err.Error())
		switch {
		case strings.Contains(msg, "is invalid") || strings.Contains(msg, "validation") ||
			strings.Contains(msg, "must be") || strings.Contains(msg, "required"):
			writeStatus(w, http.StatusBadRequest, "BadRequest", err.Error())
		case strings.Contains(msg, "forbidden") || strings.Contains(msg, "not allowed"):
			writeStatus(w, http.StatusForbidden, "Forbidden", err.Error())
		case strings.Contains(msg, "not found"):
			writeStatus(w, http.StatusNotFound, "NotFound", err.Error())
		default:
			writeStatus(w, http.StatusBadGateway, "UpstreamError", err.Error())
		}
	}
}

func (s *Server) listAgents(w http.ResponseWriter, r *http.Request) {
	c, _, ok := s.requireClient(w, r)
	if !ok {
		return
	}
	list, err := c.Agents().List(r.Context(), metav1.ListOptions{})
	if err != nil {
		writeResourceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) getAgent(w http.ResponseWriter, r *http.Request) {
	c, _, ok := s.requireClient(w, r)
	if !ok {
		return
	}
	a, err := c.Agents().Get(r.Context(), r.PathValue("name"), metav1.GetOptions{})
	if err != nil {
		writeResourceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, a)
}

type createAgentRequest struct {
	Name            string `json:"name"`
	DisplayName     string `json:"displayName"`
	Description     string `json:"description,omitempty"`
	SystemPrompt    string `json:"systemPrompt,omitempty"`
	Autonomy        string `json:"autonomy,omitempty"`
	ModelCredential string `json:"modelCredential,omitempty"`
	// ModelFallbacks are additional credential names tried, in order, when the
	// primary model fails.
	ModelFallbacks []string `json:"modelFallbacks,omitempty"`
	// BudgetTokens caps tokens per month (0 = unlimited).
	BudgetTokens int64 `json:"budgetTokens,omitempty"`
	// BudgetUSD caps spend per month as a decimal string (empty = unlimited).
	BudgetUSD string `json:"budgetUSD,omitempty"`
	// Channels binds named messaging channels to the agent (primary + secondary
	// + …).
	Channels []channelInput `json:"channels,omitempty"`
	// InteractiveFamilies / BackgroundFamilies grant built-in tool families at
	// creation. Present so a preset can hand over a ready-to-use agent — a
	// research agent needs "web" and "spawn" to do the thing it was created for,
	// and making the user find the Tools section afterwards is how a capability
	// stays undiscovered. Unknown names are dropped; core is always granted.
	InteractiveFamilies []string `json:"interactiveFamilies,omitempty"`
	BackgroundFamilies  []string `json:"backgroundFamilies,omitempty"`
}

// channelInput is the REST shape of an agent channel binding.
type channelInput struct {
	Name          string `json:"name"`
	ConnectionRef string `json:"connectionRef"`
	Primary       bool   `json:"primary,omitempty"`
}

// normalizeChannels cleans user-supplied channel rows: trims, ignores wholly
// blank rows, rejects duplicate names, and guarantees exactly one primary (the
// first entry when none is marked, the first-marked when several are).
//
// A half-filled row is an error rather than a silent drop: dropping it made a
// save look successful while binding nothing, so the agent appeared configured
// but no channel ever reached it.
func normalizeChannels(in []channelInput) ([]agentsv1alpha1.AgentChannel, error) {
	seen := map[string]bool{}
	out := []agentsv1alpha1.AgentChannel{}
	for _, ci := range in {
		name := strings.TrimSpace(ci.Name)
		conn := strings.TrimSpace(ci.ConnectionRef)
		if name == "" && conn == "" {
			continue
		}
		if conn == "" {
			return nil, fmt.Errorf("channel %q has no connectionRef", name)
		}
		if name == "" {
			return nil, fmt.Errorf("the channel bound to connection %q has no name", conn)
		}
		if seen[name] {
			return nil, fmt.Errorf("duplicate channel name %q", name)
		}
		seen[name] = true
		out = append(out, agentsv1alpha1.AgentChannel{Name: name, ConnectionRef: conn, Primary: ci.Primary})
	}
	if len(out) > 0 {
		havePrimary := false
		for i := range out {
			if out[i].Primary && !havePrimary {
				havePrimary = true
			} else {
				out[i].Primary = false
			}
		}
		if !havePrimary {
			out[0].Primary = true
		}
	}
	return out, nil
}

// validateChannelUniqueness rejects binding a Connection that another agent
// already lists as a channel. Inbound routing maps a Connection to exactly one
// agent, so a Connection may back at most one agent's channels (the connection
// config["agent"] override remains the escape hatch for shared cases).
func (s *Server) validateChannelUniqueness(ctx context.Context, c *agentsclient.Client, selfName string, channels []agentsv1alpha1.AgentChannel) error {
	mine := map[string]bool{}
	for _, ch := range channels {
		mine[ch.ConnectionRef] = true
	}
	if len(mine) == 0 {
		return nil
	}
	list, err := c.Agents().List(ctx, metav1.ListOptions{})
	if err != nil {
		return err
	}
	for i := range list.Items {
		other := &list.Items[i]
		if other.Name == selfName {
			continue
		}
		for _, och := range other.Spec.Channels {
			if mine[och.ConnectionRef] {
				return fmt.Errorf("connection %q is already a channel of agent %q", och.ConnectionRef, other.Name)
			}
		}
	}
	return nil
}

func (s *Server) createAgent(w http.ResponseWriter, r *http.Request) {
	c, id, ok := s.requireClient(w, r)
	if !ok {
		return
	}
	var req createAgentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeStatus(w, http.StatusBadRequest, "BadRequest", "invalid JSON body: "+err.Error())
		return
	}
	out, created, err := s.applyAgentCreate(r.Context(), c, id, &req)
	if err != nil {
		writeUpdateError(w, err)
		return
	}
	if created {
		s.trackAgentCreated(r.Context(), id, out.Name)
	}
	writeJSON(w, http.StatusCreated, out)
}

// applyAgentCreate validates the request and creates the agent. Shared by the
// REST handler and the MCP create_agent tool. The bool reports whether this
// successful apply won the durable, tenant-scoped creation claim. The
// create-or-apply API does not expose whether it took its create or update
// branch, so the claim is the authoritative cross-request/process boundary.
func (s *Server) applyAgentCreate(ctx context.Context, c *agentsclient.Client, id identity, req *createAgentRequest) (*agentsv1alpha1.Agent, bool, error) {
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		return nil, false, errBadRequest("name is required")
	}
	if strings.TrimSpace(req.DisplayName) == "" {
		req.DisplayName = req.Name
	}
	a := &agentsv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: req.Name},
		Spec: agentsv1alpha1.AgentSpec{
			DisplayName:  req.DisplayName,
			Description:  req.Description,
			SystemPrompt: req.SystemPrompt,
			Autonomy:     req.Autonomy,
		},
	}
	if cred := strings.TrimSpace(req.ModelCredential); cred != "" {
		a.Spec.Models = map[string]string{"chat": cred}
	}
	if fb := trimmedList(req.ModelFallbacks); len(fb) > 0 {
		a.Spec.ModelFallbacks = fb
	}
	if req.BudgetTokens > 0 || strings.TrimSpace(req.BudgetUSD) != "" {
		a.Spec.Budget = &agentsv1alpha1.AgentBudget{Window: "month", TokenLimit: req.BudgetTokens, USDLimit: strings.TrimSpace(req.BudgetUSD)}
	}
	if len(req.InteractiveFamilies) > 0 {
		a.Spec.Tools.Interactive.Families = normalizeFamilies(req.InteractiveFamilies)
	}
	if len(req.BackgroundFamilies) > 0 {
		a.Spec.Tools.Background.Families = normalizeFamilies(req.BackgroundFamilies)
	}
	if len(req.Channels) > 0 {
		chans, err := normalizeChannels(req.Channels)
		if err != nil {
			return nil, false, errBadRequest(err.Error())
		}
		if err := s.validateChannelUniqueness(ctx, c, req.Name, chans); err != nil {
			return nil, false, errConflict(err.Error())
		}
		a.Spec.Channels = chans
	}
	// Only an authoritative NotFound read makes this apply a creation candidate.
	// A readable existing resource is an update, including an agent created
	// before telemetry was enabled. For any other read failure, the generic
	// apply mutation cannot distinguish an existing resource from a missing one,
	// so conservatively suppress telemetry rather than emit a false positive.
	// The durable claim below still serializes concurrent NotFound requests.
	_, getErr := c.Agents().Get(ctx, req.Name, metav1.GetOptions{})
	candidate := apierrors.IsNotFound(getErr)
	out, err := c.Agents().Create(ctx, a, metav1.CreateOptions{})
	if err != nil || !candidate {
		return out, false, err
	}
	if s == nil || s.store == nil {
		// Telemetry is best effort and must never change a successful product
		// operation. Without the durable claim store, suppress the event rather
		// than falling back to a process-local race-prone dedupe.
		return out, false, nil
	}
	created, claimErr := s.store.ClaimAgentCreation(ctx, id.scope(req.Name))
	if claimErr != nil {
		return out, false, nil
	}
	return out, created, nil
}

// knownToolFamilies are the grantable built-in families (core is always on).
var knownToolFamilies = map[string]bool{"core": true, "web": true, "github": true, "mcp": true, "edges": true, "files": true, "spawn": true}

// normalizeFamilies keeps only recognized families, always includes core, and
// de-duplicates — so the stored grant is clean regardless of UI input.
// trimmedList trims each entry and drops blanks and duplicates, preserving
// order. Used for ordered name lists like model fallbacks.
func trimmedList(in []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

func normalizeFamilies(in []string) []string {
	seen := map[string]bool{"core": true}
	out := []string{"core"}
	for _, f := range in {
		f = strings.TrimSpace(f)
		if knownToolFamilies[f] && !seen[f] {
			seen[f] = true
			out = append(out, f)
		}
	}
	return out
}

type updateAgentRequest struct {
	ModelCredential *string   `json:"modelCredential,omitempty"`
	ModelFallbacks  *[]string `json:"modelFallbacks,omitempty"`
	SystemPrompt    *string   `json:"systemPrompt,omitempty"`
	Description     *string   `json:"description,omitempty"`
	Autonomy        *string   `json:"autonomy,omitempty"`
	BudgetTokens    *int64    `json:"budgetTokens,omitempty"`
	BudgetUSD       *string   `json:"budgetUSD,omitempty"`
	// MaxToolTurns caps tool-call iterations per run (0 = provider default).
	MaxToolTurns *int32 `json:"maxToolTurns,omitempty"`
	// TimeoutSeconds bounds a run's wall clock (0 = provider default).
	TimeoutSeconds *int32 `json:"timeoutSeconds,omitempty"`
	// Channels replaces the agent's whole channel list when present.
	Channels    *[]channelInput `json:"channels,omitempty"`
	Delegates   *[]string       `json:"delegates,omitempty"`
	DisplayName *string         `json:"displayName,omitempty"`
	// Tool grants per run class. When present, they replace the agent's current
	// families for that class (core is always implied server-side).
	InteractiveFamilies *[]string `json:"interactiveFamilies,omitempty"`
	BackgroundFamilies  *[]string `json:"backgroundFamilies,omitempty"`
	// Linked shared Toolsets per run class.
	InteractiveToolsets *[]string `json:"interactiveToolsets,omitempty"`
	BackgroundToolsets  *[]string `json:"backgroundToolsets,omitempty"`
	// Directly-granted tool Connections per run class (tools wired straight to
	// the agent, not via a Toolset).
	InteractiveConnections *[]string `json:"interactiveConnections,omitempty"`
	BackgroundConnections  *[]string `json:"backgroundConnections,omitempty"`
}

// requestError is a caller-input failure produced outside the tenant API (so
// writeResourceError cannot classify it). It keeps its HTTP class so the REST
// handler and the MCP tools report the same condition consistently.
type requestError struct {
	code   int
	reason string
	msg    string
}

func (e *requestError) Error() string { return e.msg }

func errBadRequest(msg string) error { return &requestError{http.StatusBadRequest, "BadRequest", msg} }
func errConflict(msg string) error   { return &requestError{http.StatusConflict, "Conflict", msg} }

// writeUpdateError maps requestError to its own class before falling back to
// the tenant-API mapping.
func writeUpdateError(w http.ResponseWriter, err error) {
	if re, ok := errors.AsType[*requestError](err); ok {
		writeStatus(w, re.code, re.reason, re.msg)
		return
	}
	writeResourceError(w, err)
}

// updateAgent patches mutable agent fields — notably the assigned model
// credential, so a user can reassign an agent to a different credential.
func (s *Server) updateAgent(w http.ResponseWriter, r *http.Request) {
	c, _, ok := s.requireClient(w, r)
	if !ok {
		return
	}
	var req updateAgentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeStatus(w, http.StatusBadRequest, "BadRequest", "invalid JSON body: "+err.Error())
		return
	}
	out, err := s.applyAgentUpdate(r.Context(), c, r.PathValue("name"), &req)
	if err != nil {
		writeUpdateError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// applyAgentUpdate reads the agent, applies the patch fields that are present,
// and writes it back. Shared by the REST handler and the MCP update_agent tool
// so both surfaces have identical semantics: absent fields are untouched, list
// fields replace wholesale, and core is always re-added to family grants.
func (s *Server) applyAgentUpdate(ctx context.Context, c *agentsclient.Client, name string, req *updateAgentRequest) (*agentsv1alpha1.Agent, error) {
	agent, err := c.Agents().Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	if req.ModelCredential != nil {
		cred := strings.TrimSpace(*req.ModelCredential)
		if agent.Spec.Models == nil {
			agent.Spec.Models = map[string]string{}
		}
		if cred == "" {
			delete(agent.Spec.Models, "chat")
		} else {
			agent.Spec.Models["chat"] = cred
		}
	}
	if req.ModelFallbacks != nil {
		agent.Spec.ModelFallbacks = trimmedList(*req.ModelFallbacks)
	}
	if req.SystemPrompt != nil {
		agent.Spec.SystemPrompt = *req.SystemPrompt
	}
	if req.Description != nil {
		agent.Spec.Description = strings.TrimSpace(*req.Description)
	}
	if req.Autonomy != nil {
		agent.Spec.Autonomy = *req.Autonomy
	}
	if req.MaxToolTurns != nil {
		agent.Spec.Limits.MaxToolTurns = *req.MaxToolTurns
	}
	if req.TimeoutSeconds != nil {
		agent.Spec.Limits.TimeoutSeconds = *req.TimeoutSeconds
	}
	if req.Channels != nil {
		chans, err := normalizeChannels(*req.Channels)
		if err != nil {
			return nil, errBadRequest(err.Error())
		}
		if err := s.validateChannelUniqueness(ctx, c, name, chans); err != nil {
			return nil, errConflict(err.Error())
		}
		agent.Spec.Channels = chans
	}
	if req.Delegates != nil {
		out := make([]string, 0, len(*req.Delegates))
		for _, d := range *req.Delegates {
			if d = strings.TrimSpace(d); d != "" && d != name {
				out = append(out, d)
			}
		}
		agent.Spec.Delegates = out
	}
	if req.DisplayName != nil {
		agent.Spec.DisplayName = strings.TrimSpace(*req.DisplayName)
	}
	if req.InteractiveFamilies != nil {
		agent.Spec.Tools.Interactive.Families = normalizeFamilies(*req.InteractiveFamilies)
	}
	if req.BackgroundFamilies != nil {
		agent.Spec.Tools.Background.Families = normalizeFamilies(*req.BackgroundFamilies)
	}
	if req.InteractiveToolsets != nil {
		agent.Spec.Tools.Interactive.Toolsets = *req.InteractiveToolsets
	}
	if req.BackgroundToolsets != nil {
		agent.Spec.Tools.Background.Toolsets = *req.BackgroundToolsets
	}
	if req.InteractiveConnections != nil {
		agent.Spec.Tools.Interactive.Connections = *req.InteractiveConnections
	}
	if req.BackgroundConnections != nil {
		agent.Spec.Tools.Background.Connections = *req.BackgroundConnections
	}
	if req.BudgetTokens != nil || req.BudgetUSD != nil {
		if agent.Spec.Budget == nil {
			agent.Spec.Budget = &agentsv1alpha1.AgentBudget{Window: "month"}
		}
		if req.BudgetTokens != nil {
			agent.Spec.Budget.TokenLimit = *req.BudgetTokens
		}
		if req.BudgetUSD != nil {
			agent.Spec.Budget.USDLimit = strings.TrimSpace(*req.BudgetUSD)
		}
		// A fully-zeroed budget means "remove the cap".
		if agent.Spec.Budget.TokenLimit == 0 && agent.Spec.Budget.USDLimit == "" {
			agent.Spec.Budget = nil
		}
	}
	return c.Agents().Update(ctx, agent, metav1.UpdateOptions{})
}

func (s *Server) deleteAgent(w http.ResponseWriter, r *http.Request) {
	c, id, ok := s.requireClient(w, r)
	if !ok {
		return
	}
	name := r.PathValue("name")
	if err := c.Agents().Delete(r.Context(), name, metav1.DeleteOptions{}); err != nil {
		writeResourceError(w, err)
		return
	}
	// Best-effort teardown of the agent's store data.
	_ = s.store.DeleteAgentData(r.Context(), id.scope(name), name)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) listMessages(w http.ResponseWriter, r *http.Request) {
	_, id, ok := s.requireClient(w, r)
	if !ok {
		return
	}
	name := r.PathValue("name")
	session := r.URL.Query().Get("session")
	limit := 100
	if v, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && v > 0 {
		limit = min(v, 500)
	}
	// Messages carry runID, and tool-role messages carry metadata.tool/args, so
	// a reloaded session can rebuild its tool cards rather than showing a flat
	// wall of text.
	page, err := s.store.ListMessages(r.Context(), id.scope(name), session, limit, r.URL.Query().Get("cursor"))
	if err != nil {
		writeStatus(w, http.StatusInternalServerError, "InternalError", err.Error())
		return
	}
	writeList(w, page.Items, map[string]any{"nextCursor": page.NextCursor})
}

// listSessions returns the agent's chat threads (most-recently-active first)
// so the portal can list them and let the user resume one after a refresh.
func (s *Server) listSessions(w http.ResponseWriter, r *http.Request) {
	_, id, ok := s.requireClient(w, r)
	if !ok {
		return
	}
	name := r.PathValue("name")
	sessions, err := s.store.ListSessions(r.Context(), id.scope(name), 100)
	if err != nil {
		writeStatus(w, http.StatusInternalServerError, "InternalError", err.Error())
		return
	}
	writeList(w, sessions)
}

type chatRequest struct {
	Message   string `json:"message"`
	SessionID string `json:"sessionID,omitempty"`
}

// chat runs one assistant turn and streams the reply over Server-Sent Events
// (events: "run", "delta", "done", "error"), reusing the shared executeTask
// path with an SSE delta callback.
func (s *Server) chat(w http.ResponseWriter, r *http.Request) {
	c, id, ok := s.requireClient(w, r)
	if !ok {
		return
	}
	name := r.PathValue("name")

	var req chatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeStatus(w, http.StatusBadRequest, "BadRequest", "invalid JSON body: "+err.Error())
		return
	}
	req.Message = strings.TrimSpace(req.Message)
	if req.Message == "" {
		writeStatus(w, http.StatusBadRequest, "BadRequest", "message is required")
		return
	}
	if req.SessionID == "" {
		req.SessionID = "default"
	}

	agent, err := c.Agents().Get(r.Context(), name, metav1.GetOptions{})
	if err != nil {
		writeResourceError(w, err)
		return
	}

	flusher, isFlusher := w.(http.Flusher)
	if !isFlusher {
		writeStatus(w, http.StatusInternalServerError, "InternalError", "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	// The run's lifetime is deliberately decoupled from this stream. A research
	// fan-out or a long tool chain can outlast the browser tab that started it,
	// and cancelling the work because the reader went away loses minutes of real
	// spend for no reason. So: writes stop when the client disappears, but the run
	// keeps going, finishes, and lands in the session transcript and on the run
	// record — where reopening the chat, or GET /api/runs/{id}, will find it.
	//
	// The user can still stop it deliberately: executeTask registers the run for
	// POST /api/runs/{id}/cancel.
	runCtx, clientGone := detachedStreamContext(r)

	seq := 0
	sse := func(event string, payload any) {
		if clientGone() {
			return
		}
		seq++
		b, _ := json.Marshal(payload)
		fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", seq, event, b)
		flusher.Flush()
	}

	// The runID goes out first so a dropped stream can reconcile against
	// GET /api/runs/{id}.
	runID := uuid.NewString()
	sse("start", map[string]string{"runID": runID, "sessionID": req.SessionID})

	res, err := s.executeTask(runCtx, taskRun{
		Creds: c, CR: clientCR{c}, Scope: id.scope(name), Agent: agent,
		RunID:     runID,
		SessionID: req.SessionID, Task: req.Message, Trigger: agentsv1alpha1.RunTriggerChat,
		EdgesEndpoint: s.edgesEndpoint(id.clusterID), HubToken: id.token, EdgesInsecure: s.cfg.HubInsecure,
		// ClusterID addresses the tenant workspace on the data plane — without
		// it an instance-backed tool (self-hosted search, a browser instance)
		// has no way to compose its URL.
		ClusterID: id.clusterID,
		OnDelta: func(delta string) {
			sse("delta", map[string]string{"text": delta})
		},
		OnToolStart: func(callID, toolName, args string) {
			sse("tool_start", map[string]any{"id": callID, "name": toolName, "args": redactArgs(args)})
		},
		OnTool: func(ev engine.ToolEvent) {
			sse("tool_end", map[string]any{
				"id": ev.ID, "name": ev.Name, "args": redactArgs(ev.Args), "result": safeTruncate(ev.Result, 8*1024),
				"error": ev.Err, "durationMS": ev.Duration.Milliseconds(),
			})
		},
	})
	if err != nil {
		if s.credentialsError(err) {
			sse("error", map[string]string{"runID": runID, "message": "no model configured — open Model settings to add one"})
		} else {
			sse("error", map[string]string{"runID": runID, "message": err.Error()})
		}
		return
	}
	// Paused on an approval gate: the portal renders an approval card; the run
	// resumes via the inbox resolution (watch /api/events for the outcome).
	if res.Pending != nil {
		sse("approval_required", map[string]any{
			"runID": runID, "inboxID": res.Pending.InboxID,
			"tool": res.Pending.Tool, "args": redactArgs(res.Pending.Args),
			"content": res.Content,
		})
		return
	}
	sse("done", map[string]any{
		"runID":   runID,
		"content": res.Content,
		"usage": map[string]int64{
			"inputTokens": res.Usage.InputTokens, "outputTokens": res.Usage.OutputTokens,
			"usdMicros": res.Usage.USDMicros,
		},
	})
}

// deleteSession wipes one chat session's transcript.
func (s *Server) deleteSession(w http.ResponseWriter, r *http.Request) {
	_, id, ok := s.requireClient(w, r)
	if !ok {
		return
	}
	name := r.PathValue("name")
	session := r.PathValue("session")
	if err := s.store.DeleteSession(r.Context(), id.scope(name), session); err != nil {
		writeStatus(w, http.StatusInternalServerError, "InternalError", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// edgesEndpoint is the hub's aggregate MCP virtual endpoint for a workspace
// cluster — the edges tool family (kube + SSH tools) dials it as the calling
// user. Uses the conventional "default" MCPServer; empty when the hub URL or
// cluster is unknown.
func (s *Server) edgesEndpoint(clusterID string) string {
	if s.cfg.HubURL == "" || clusterID == "" {
		return ""
	}
	return strings.TrimRight(s.cfg.HubURL, "/") + "/services/mcpserver/" + clusterID + "/apis/faros.sh/v1alpha1/mcpservers/default/mcp"
}

// errNoCredential signals that an agent has no model credential assigned.
var errNoCredential = errors.New("this agent has no model credential assigned — pick one on the Models tab")

// buildChatModelCtx builds the model for an ordinary (chat-purpose) run.
func (s *Server) buildChatModelCtx(ctx context.Context, creds llm.SecretGetter, agent *agentsv1alpha1.Agent) (einomodel.BaseChatModel, error) {
	return s.buildModelForPurpose(ctx, creds, agent, llm.PurposeChat)
}

// buildModelForPurpose resolves the agent's named model credential for a run
// purpose and builds the Eino model from it. Agents reference a credential by
// name in spec.models[purpose]; the credential is its own Secret
// (faros-agents-model-<name>). A purpose the agent did not map falls back to
// "chat", so mapping only "chat" keeps working everywhere.
func (s *Server) buildModelForPurpose(ctx context.Context, creds llm.SecretGetter, agent *agentsv1alpha1.Agent, purpose string) (einomodel.BaseChatModel, error) {
	primary := strings.TrimSpace(agent.Spec.Models[purpose])
	if primary == "" {
		primary = strings.TrimSpace(agent.Spec.Models[llm.PurposeChat])
	}
	if primary == "" {
		return nil, errNoCredential
	}
	// Primary first, then the ordered fallbacks. Skip blanks and duplicates.
	names := []string{primary}
	seen := map[string]bool{primary: true}
	for _, f := range agent.Spec.ModelFallbacks {
		f = strings.TrimSpace(f)
		if f == "" || seen[f] {
			continue
		}
		seen[f] = true
		names = append(names, f)
	}

	var members []einomodel.BaseChatModel
	var built []string
	var lastErr error
	for _, name := range names {
		profile, err := llm.LoadCredential(ctx, creds, name)
		if err != nil {
			lastErr = err
			continue
		}
		m, err := llm.BuildModel(ctx, profile)
		if err != nil {
			lastErr = err
			continue
		}
		members = append(members, m)
		built = append(built, name)
	}
	if len(members) == 0 {
		if lastErr != nil {
			return nil, lastErr
		}
		return nil, errNoCredential
	}
	return llm.NewFallbackModel(members, built), nil
}

// primaryModelName resolves the model id of the agent's primary chat credential
// for cost attribution. Best-effort: returns "" when unresolvable (cost then
// falls back to 0 rather than erroring the run).
func (s *Server) primaryModelName(ctx context.Context, creds llm.SecretGetter, agent *agentsv1alpha1.Agent) string {
	return s.modelNameForPurpose(ctx, creds, agent, llm.PurposeChat)
}

// modelNameForPurpose resolves the model id behind a run purpose, following the
// same purpose → chat fallback as buildModelForPurpose so cost attribution and
// context-window sizing name the model that will actually be called.
func (s *Server) modelNameForPurpose(ctx context.Context, creds llm.SecretGetter, agent *agentsv1alpha1.Agent, purpose string) string {
	name := strings.TrimSpace(agent.Spec.Models[purpose])
	if name == "" {
		name = strings.TrimSpace(agent.Spec.Models[llm.PurposeChat])
	}
	if name == "" {
		return ""
	}
	p, err := llm.LoadCredential(ctx, creds, name)
	if err != nil {
		return ""
	}
	return p.Model
}

// credentialsError reports whether err is a missing/invalid model-credentials
// condition (Secret not found or profile unconfigured), so callers can show a
// "configure a model" hint instead of a raw error.
func (s *Server) credentialsError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, llm.ErrNotConfigured) || errors.Is(err, errNoCredential) {
		return true
	}
	m := strings.ToLower(err.Error())
	return strings.Contains(m, "not found") && strings.Contains(m, llm.ModelCredentialPrefix)
}
