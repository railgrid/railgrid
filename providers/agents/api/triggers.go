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
	"net/http"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	agentsv1alpha1 "github.com/faroshq/provider-agents/apis/v1alpha1"
	"github.com/faroshq/provider-agents/channels"
	agentsclient "github.com/faroshq/provider-agents/client"
)

// deliverToNotifyChannel best-effort delivers a synchronous run's output to the
// agent channel named by role (a schedule/trigger's ChannelRef), else the
// agent's primary channel — mirroring what the background executor does for
// real events ([background.handle]). Without this, "Fire"/"Run now" only
// returned output to the UI and never pinged the channel, so a test never
// looked like a real event. No-op when the agent has no channel for that role.
func (s *Server) deliverToNotifyChannel(ctx context.Context, c *agentsclient.Client, agent *agentsv1alpha1.Agent, role, prefix, text string) {
	connName, ok := agent.Spec.ResolveChannelConnection(role)
	if !ok || strings.TrimSpace(text) == "" {
		return
	}
	conn, err := c.Connections().Get(ctx, connName, metav1.GetOptions{})
	if err != nil {
		return
	}
	msg := text
	if prefix != "" {
		msg = "[" + prefix + "] " + text
	}
	_ = channels.Send(ctx, channels.Message{
		Type:   conn.Spec.Type,
		Token:  s.connectionTokenCtx(ctx, c, connName),
		Target: conn.Spec.Channel,
		Config: conn.Spec.Config,
		Text:   truncate(msg, 3500),
	})
}

func (s *Server) listTriggers(w http.ResponseWriter, r *http.Request) {
	c, _, ok := s.requireClient(w, r)
	if !ok {
		return
	}
	list, err := c.Triggers().List(r.Context(), metav1.ListOptions{})
	if err != nil {
		writeResourceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

type createTriggerRequest struct {
	Name          string            `json:"name"`
	AgentRef      string            `json:"agentRef"`
	Source        string            `json:"source"` // webhook | channel | github | connection | email
	ConnectionRef string            `json:"connectionRef,omitempty"`
	Filter        map[string]string `json:"filter,omitempty"`
	Task          string            `json:"task,omitempty"`
	Suspend       bool              `json:"suspend,omitempty"`
	// ChannelRef routes this trigger's output to a named agent channel; empty
	// means the agent's primary channel.
	ChannelRef string `json:"channelRef,omitempty"`
}

func (s *Server) createTrigger(w http.ResponseWriter, r *http.Request) {
	c, id, ok := s.requireClient(w, r)
	if !ok {
		return
	}
	var req createTriggerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeStatus(w, http.StatusBadRequest, "BadRequest", "invalid JSON body: "+err.Error())
		return
	}
	out, err := s.applyTriggerCreate(r.Context(), c, id.clusterID, &req)
	if err != nil {
		writeUpdateError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

// applyTriggerCreate validates the request, creates the trigger, and mints its
// inbound webhook URL. Shared by the REST handler and the MCP create_trigger
// tool. clusterID may be empty (no webhook URL is minted then).
func (s *Server) applyTriggerCreate(ctx context.Context, c *agentsclient.Client, clusterID string, req *createTriggerRequest) (*agentsv1alpha1.Trigger, error) {
	req.Name = strings.TrimSpace(req.Name)
	req.AgentRef = strings.TrimSpace(req.AgentRef)
	req.Source = strings.TrimSpace(req.Source)
	if req.Name == "" || req.AgentRef == "" || req.Source == "" {
		return nil, errBadRequest("name, agentRef, and source are required")
	}
	switch req.Source {
	case agentsv1alpha1.TriggerSourceWebhook, agentsv1alpha1.TriggerSourceGitHub:
	default:
		return nil, errBadRequest("unsupported source " + req.Source + " (use webhook or github)")
	}
	trig := &agentsv1alpha1.Trigger{
		ObjectMeta: metav1.ObjectMeta{Name: req.Name},
		Spec: agentsv1alpha1.TriggerSpec{
			AgentRef:      req.AgentRef,
			Source:        req.Source,
			ConnectionRef: req.ConnectionRef,
			Filter:        req.Filter,
			Task:          req.Task,
			Suspend:       req.Suspend,
			ChannelRef:    strings.TrimSpace(req.ChannelRef),
		},
	}
	out, err := c.Triggers().Create(ctx, trig, metav1.CreateOptions{})
	if err != nil {
		return nil, err
	}
	// Webhook-style sources get a token-guarded inbound URL (HMAC over
	// cluster/name — no state to store). Best-effort status write; the token
	// only works once the background executor is running.
	if clusterID != "" {
		if token := s.webhookToken(clusterID, req.Name); token != "" {
			out.Status.WebhookPath = webhookPath(clusterID, req.Name, token)
			if updated, uerr := c.Triggers().UpdateStatus(ctx, out, metav1.UpdateOptions{}); uerr == nil {
				out = updated
			}
		}
	}
	return out, nil
}

// webhookPath is the inbound URL a webhook-style trigger listens on.
func webhookPath(clusterID, name, token string) string {
	return "/services/providers/agents/webhooks/triggers/" + clusterID + "/" + name + "/" + token
}

// updateTriggerRequest patches an existing trigger. All fields are optional
// pointers so callers change only what they send (task edits, pause/resume,
// rewire the connection, or repoint the source).
type updateTriggerRequest struct {
	Task          *string            `json:"task,omitempty"`
	Source        *string            `json:"source,omitempty"`
	ConnectionRef *string            `json:"connectionRef,omitempty"`
	Filter        *map[string]string `json:"filter,omitempty"`
	Suspend       *bool              `json:"suspend,omitempty"`
	ChannelRef    *string            `json:"channelRef,omitempty"`
}

func (s *Server) updateTrigger(w http.ResponseWriter, r *http.Request) {
	c, id, ok := s.requireClient(w, r)
	if !ok {
		return
	}
	var req updateTriggerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeStatus(w, http.StatusBadRequest, "BadRequest", "invalid JSON body: "+err.Error())
		return
	}
	out, err := s.applyTriggerUpdate(r.Context(), c, id.clusterID, r.PathValue("name"), &req)
	if err != nil {
		writeUpdateError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// applyTriggerUpdate reads the trigger, applies the patch fields that are
// present, writes it back, and keeps the inbound webhook URL in sync. Shared by
// the REST handler and the MCP update_trigger tool.
func (s *Server) applyTriggerUpdate(ctx context.Context, c *agentsclient.Client, clusterID, name string, req *updateTriggerRequest) (*agentsv1alpha1.Trigger, error) {
	name = strings.TrimSpace(name)
	trig, err := c.Triggers().Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	if req.Task != nil {
		trig.Spec.Task = *req.Task
	}
	if req.ConnectionRef != nil {
		trig.Spec.ConnectionRef = strings.TrimSpace(*req.ConnectionRef)
	}
	if req.Filter != nil {
		trig.Spec.Filter = *req.Filter
	}
	if req.Suspend != nil {
		trig.Spec.Suspend = *req.Suspend
	}
	if req.ChannelRef != nil {
		trig.Spec.ChannelRef = strings.TrimSpace(*req.ChannelRef)
	}
	if req.Source != nil {
		src := strings.TrimSpace(*req.Source)
		switch src {
		case agentsv1alpha1.TriggerSourceWebhook, agentsv1alpha1.TriggerSourceGitHub:
			trig.Spec.Source = src
		default:
			return nil, errBadRequest("unsupported source " + src + " (use webhook or github)")
		}
	}
	out, err := c.Triggers().Update(ctx, trig, metav1.UpdateOptions{})
	if err != nil {
		return nil, err
	}
	// Keep the inbound webhook URL in sync: webhook-style sources need one; a
	// source changed away from them should drop it.
	wantsWebhook := out.Spec.Source == agentsv1alpha1.TriggerSourceWebhook || out.Spec.Source == agentsv1alpha1.TriggerSourceGitHub
	switch {
	case wantsWebhook && out.Status.WebhookPath == "" && clusterID != "":
		if token := s.webhookToken(clusterID, name); token != "" {
			out.Status.WebhookPath = webhookPath(clusterID, name, token)
			if updated, uerr := c.Triggers().UpdateStatus(ctx, out, metav1.UpdateOptions{}); uerr == nil {
				out = updated
			}
		}
	case !wantsWebhook && out.Status.WebhookPath != "":
		out.Status.WebhookPath = ""
		if updated, uerr := c.Triggers().UpdateStatus(ctx, out, metav1.UpdateOptions{}); uerr == nil {
			out = updated
		}
	}
	return out, nil
}

func (s *Server) deleteTrigger(w http.ResponseWriter, r *http.Request) {
	c, _, ok := s.requireClient(w, r)
	if !ok {
		return
	}
	if err := c.Triggers().Delete(r.Context(), r.PathValue("name"), metav1.DeleteOptions{}); err != nil {
		writeResourceError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// runTriggerNow fires a trigger's task immediately as the calling user with an
// optional payload, asynchronously: 202 + runID, output delivered to the
// trigger's channel like a real event. Mirrors schedule "run now".
func (s *Server) runTriggerNow(w http.ResponseWriter, r *http.Request) {
	c, id, ok := s.requireClient(w, r)
	if !ok {
		return
	}
	name := r.PathValue("name")
	trig, err := c.Triggers().Get(r.Context(), name, metav1.GetOptions{})
	if err != nil {
		writeResourceError(w, err)
		return
	}
	agent, err := c.Agents().Get(r.Context(), trig.Spec.AgentRef, metav1.GetOptions{})
	if err != nil {
		writeResourceError(w, err)
		return
	}
	// Optional test payload appended to the task so the run can react to it.
	var payload map[string]any
	_ = json.NewDecoder(r.Body).Decode(&payload)
	task := trig.Spec.Task
	if len(payload) > 0 {
		if b, err := json.Marshal(payload); err == nil {
			task += "\n\nEvent payload:\n" + string(b)
		}
	}
	if strings.TrimSpace(task) == "" {
		writeStatus(w, http.StatusBadRequest, "BadRequest", "this trigger has no task to run")
		return
	}
	runID := s.startDetachedRun(r, c, id, agent, taskRun{
		SessionID: "trigger:" + name, Task: task, Trigger: agentsv1alpha1.RunTriggerEvent, SourceName: name,
		NotifyChannel: trig.Spec.ChannelRef,
	})
	writeJSON(w, http.StatusAccepted, map[string]string{"runID": runID})
}
