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
	"strings"

	einomodel "github.com/cloudwego/eino/components/model"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	agentsv1alpha1 "github.com/faroshq/provider-agents/apis/v1alpha1"
	"github.com/faroshq/provider-agents/engine"
	"github.com/faroshq/provider-agents/llm"
)

// chatHistoryLimit bounds how many prior messages are replayed into a turn.
const chatHistoryLimit = 40

func writeResourceError(w http.ResponseWriter, err error) {
	switch {
	case apierrors.IsNotFound(err):
		writeStatus(w, http.StatusNotFound, "NotFound", err.Error())
	case apierrors.IsAlreadyExists(err):
		writeStatus(w, http.StatusConflict, "Conflict", err.Error())
	case apierrors.IsConflict(err):
		writeStatus(w, http.StatusConflict, "Conflict", err.Error())
	default:
		writeStatus(w, http.StatusBadGateway, "UpstreamError", err.Error())
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
	// BudgetTokens caps tokens per month (0 = unlimited).
	BudgetTokens int64 `json:"budgetTokens,omitempty"`
	// BudgetUSD caps spend per month as a decimal string (empty = unlimited).
	BudgetUSD string `json:"budgetUSD,omitempty"`
	// NotifyConnection names the messaging Connection background runs deliver
	// output/alerts to.
	NotifyConnection string `json:"notifyConnection,omitempty"`
}

func (s *Server) createAgent(w http.ResponseWriter, r *http.Request) {
	c, _, ok := s.requireClient(w, r)
	if !ok {
		return
	}
	var req createAgentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeStatus(w, http.StatusBadRequest, "BadRequest", "invalid JSON body: "+err.Error())
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		writeStatus(w, http.StatusBadRequest, "BadRequest", "name is required")
		return
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
	if req.BudgetTokens > 0 || strings.TrimSpace(req.BudgetUSD) != "" {
		a.Spec.Budget = &agentsv1alpha1.AgentBudget{Window: "month", TokenLimit: req.BudgetTokens, USDLimit: strings.TrimSpace(req.BudgetUSD)}
	}
	a.Spec.DefaultNotifyConnection = strings.TrimSpace(req.NotifyConnection)
	out, err := c.Agents().Create(r.Context(), a, metav1.CreateOptions{})
	if err != nil {
		writeResourceError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

// knownToolFamilies are the grantable built-in families (core is always on).
var knownToolFamilies = map[string]bool{"core": true, "web": true, "github": true, "mcp": true, "edges": true, "files": true}

// normalizeFamilies keeps only recognized families, always includes core, and
// de-duplicates — so the stored grant is clean regardless of UI input.
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
	ModelCredential  *string   `json:"modelCredential,omitempty"`
	SystemPrompt     *string   `json:"systemPrompt,omitempty"`
	Autonomy         *string   `json:"autonomy,omitempty"`
	BudgetTokens     *int64    `json:"budgetTokens,omitempty"`
	BudgetUSD        *string   `json:"budgetUSD,omitempty"`
	NotifyConnection *string   `json:"notifyConnection,omitempty"`
	Delegates        *[]string `json:"delegates,omitempty"`
	DisplayName      *string   `json:"displayName,omitempty"`
	// Tool grants per run class. When present, they replace the agent's current
	// families for that class (core is always implied server-side).
	InteractiveFamilies *[]string `json:"interactiveFamilies,omitempty"`
	BackgroundFamilies  *[]string `json:"backgroundFamilies,omitempty"`
}

// updateAgent patches mutable agent fields — notably the assigned model
// credential, so a user can reassign an agent to a different credential.
func (s *Server) updateAgent(w http.ResponseWriter, r *http.Request) {
	c, _, ok := s.requireClient(w, r)
	if !ok {
		return
	}
	name := r.PathValue("name")
	agent, err := c.Agents().Get(r.Context(), name, metav1.GetOptions{})
	if err != nil {
		writeResourceError(w, err)
		return
	}
	var req updateAgentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeStatus(w, http.StatusBadRequest, "BadRequest", "invalid JSON body: "+err.Error())
		return
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
	if req.SystemPrompt != nil {
		agent.Spec.SystemPrompt = *req.SystemPrompt
	}
	if req.Autonomy != nil {
		agent.Spec.Autonomy = *req.Autonomy
	}
	if req.NotifyConnection != nil {
		agent.Spec.DefaultNotifyConnection = strings.TrimSpace(*req.NotifyConnection)
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
	out, err := c.Agents().Update(r.Context(), agent, metav1.UpdateOptions{})
	if err != nil {
		writeResourceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
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
	page, err := s.store.ListMessages(r.Context(), id.scope(name), session, 100, r.URL.Query().Get("cursor"))
	if err != nil {
		writeStatus(w, http.StatusInternalServerError, "InternalError", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, page)
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
	sse := func(event string, payload any) {
		b, _ := json.Marshal(payload)
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, b)
		flusher.Flush()
	}

	res, err := s.executeTask(r.Context(), taskRun{
		Creds: c, CR: clientCR{c}, Scope: id.scope(name), Agent: agent,
		SessionID: req.SessionID, Task: req.Message, Trigger: agentsv1alpha1.RunTriggerChat,
		EdgesEndpoint: s.edgesEndpoint(id.clusterID), EdgesToken: id.token, EdgesInsecure: s.cfg.HubInsecure,
		OnDelta: func(delta string) {
			sse("delta", map[string]string{"text": delta})
		},
		OnTool: func(ev engine.ToolEvent) {
			sse("tool", map[string]any{
				"name": ev.Name, "args": clipArgs(ev.Args), "result": clipArgs(ev.Result),
				"error": ev.Err, "durationMS": ev.Duration.Milliseconds(),
			})
		},
	})
	if err != nil {
		if s.credentialsError(err) {
			sse("error", map[string]string{"message": "no model configured — open Model settings to add one"})
		} else {
			sse("error", map[string]string{"message": err.Error()})
		}
		return
	}
	sse("done", map[string]any{
		"runID":   res.RunID,
		"content": res.Content,
		"usage":   map[string]int64{"inputTokens": res.Usage.InputTokens, "outputTokens": res.Usage.OutputTokens},
	})
}

// edgesEndpoint is the hub's aggregate MCP virtual endpoint for a workspace
// cluster — the edges tool family (kube + SSH tools) dials it as the calling
// user. Uses the conventional "default" MCPServer; empty when the hub URL or
// cluster is unknown.
func (s *Server) edgesEndpoint(clusterID string) string {
	if s.cfg.HubURL == "" || clusterID == "" {
		return ""
	}
	return strings.TrimRight(s.cfg.HubURL, "/") + "/services/mcpserver/" + clusterID + "/apis/kedge.faros.sh/v1alpha1/mcpservers/default/mcp"
}

// errNoCredential signals that an agent has no model credential assigned.
var errNoCredential = errors.New("this agent has no model credential assigned — pick one on the Models tab")

// buildChatModelCtx resolves the agent's assigned named model credential and
// builds the Eino model from it. Agents reference a credential by name in
// spec.models["chat"]; the credential is its own Secret (kedge-agents-model-<name>).
func (s *Server) buildChatModelCtx(ctx context.Context, creds llm.SecretGetter, agent *agentsv1alpha1.Agent) (einomodel.BaseChatModel, error) {
	cred := strings.TrimSpace(agent.Spec.Models["chat"])
	if cred == "" {
		return nil, errNoCredential
	}
	profile, err := llm.LoadCredential(ctx, creds, cred)
	if err != nil {
		return nil, err
	}
	return llm.BuildModel(ctx, profile)
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
