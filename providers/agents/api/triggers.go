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
	req.Name = strings.TrimSpace(req.Name)
	req.AgentRef = strings.TrimSpace(req.AgentRef)
	req.Source = strings.TrimSpace(req.Source)
	if req.Name == "" || req.AgentRef == "" || req.Source == "" {
		writeStatus(w, http.StatusBadRequest, "BadRequest", "name, agentRef, and source are required")
		return
	}
	switch req.Source {
	case agentsv1alpha1.TriggerSourceWebhook, agentsv1alpha1.TriggerSourceChannel,
		agentsv1alpha1.TriggerSourceGitHub, agentsv1alpha1.TriggerSourceConnection,
		agentsv1alpha1.TriggerSourceEmail:
	default:
		writeStatus(w, http.StatusBadRequest, "BadRequest", "unsupported source "+req.Source)
		return
	}
	trig := &agentsv1alpha1.AgentTrigger{
		ObjectMeta: metav1.ObjectMeta{Name: req.Name},
		Spec: agentsv1alpha1.AgentTriggerSpec{
			AgentRef:      req.AgentRef,
			Source:        req.Source,
			ConnectionRef: req.ConnectionRef,
			Filter:        req.Filter,
			Task:          req.Task,
			Suspend:       req.Suspend,
		},
	}
	out, err := c.Triggers().Create(r.Context(), trig, metav1.CreateOptions{})
	if err != nil {
		writeResourceError(w, err)
		return
	}
	// Webhook-style sources get a token-guarded inbound URL (HMAC over
	// cluster/name — no state to store). Best-effort status write; the token
	// only works once the background executor is running.
	if req.Source == agentsv1alpha1.TriggerSourceWebhook || req.Source == agentsv1alpha1.TriggerSourceGitHub {
		if _, ok := s.requireIdentityCluster(w, id); ok {
			if token := s.webhookToken(id.clusterID, req.Name); token != "" {
				out.Status.WebhookPath = "/services/providers/agents/webhooks/triggers/" + id.clusterID + "/" + req.Name + "/" + token
				if updated, uerr := c.Triggers().UpdateStatus(r.Context(), out, metav1.UpdateOptions{}); uerr == nil {
					out = updated
				}
			}
		}
	}
	writeJSON(w, http.StatusCreated, out)
}

// requireIdentityCluster is a small guard so webhook URLs are only minted when
// the request carries a resolved cluster.
func (s *Server) requireIdentityCluster(_ http.ResponseWriter, id identity) (string, bool) {
	if id.clusterID == "" {
		return "", false
	}
	return id.clusterID, true
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

// runTriggerNow fires a trigger's task immediately (synchronous, as the calling
// user) with an optional payload — the way to test a trigger's agent + task
// before wiring the real event source. Mirrors schedule "run now".
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
	res, err := s.executeTask(r.Context(), taskRun{
		Creds: c, CR: clientCR{c}, Scope: id.scope(agent.Name), Agent: agent,
		SessionID: "trigger:" + name, Task: task, Trigger: agentsv1alpha1.RunTriggerEvent, SourceName: name,
		EdgesEndpoint: s.edgesEndpoint(id.clusterID), EdgesToken: id.token, EdgesInsecure: s.cfg.HubInsecure,
	})
	if err != nil {
		if s.credentialsError(err) {
			writeStatus(w, http.StatusBadRequest, "BadRequest", "no model configured — assign one on the Models tab")
			return
		}
		writeStatus(w, http.StatusBadGateway, "RunFailed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, res)
}
