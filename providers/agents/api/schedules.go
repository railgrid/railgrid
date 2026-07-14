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
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	agentsv1alpha1 "github.com/faroshq/provider-agents/apis/v1alpha1"
)

func (s *Server) listSchedules(w http.ResponseWriter, r *http.Request) {
	c, _, ok := s.requireClient(w, r)
	if !ok {
		return
	}
	list, err := c.Schedules().List(r.Context(), metav1.ListOptions{})
	if err != nil {
		writeResourceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

type createScheduleRequest struct {
	Name      string `json:"name"`
	AgentRef  string `json:"agentRef"`
	Type      string `json:"type"` // cron | wakeup | heartbeat
	Schedule  string `json:"schedule,omitempty"`
	TimeZone  string `json:"timeZone,omitempty"`
	RunAt     string `json:"runAt,omitempty"`
	Task      string `json:"task,omitempty"`
	Checklist string `json:"checklist,omitempty"`
	Suspend   bool   `json:"suspend,omitempty"`
}

func (s *Server) createSchedule(w http.ResponseWriter, r *http.Request) {
	c, _, ok := s.requireClient(w, r)
	if !ok {
		return
	}
	var req createScheduleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeStatus(w, http.StatusBadRequest, "BadRequest", "invalid JSON body: "+err.Error())
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	req.AgentRef = strings.TrimSpace(req.AgentRef)
	req.Type = strings.TrimSpace(req.Type)
	if req.Name == "" || req.AgentRef == "" || req.Type == "" {
		writeStatus(w, http.StatusBadRequest, "BadRequest", "name, agentRef, and type are required")
		return
	}
	switch req.Type {
	case agentsv1alpha1.ScheduleTypeCron, agentsv1alpha1.ScheduleTypeHeartbeat:
		if strings.TrimSpace(req.Schedule) == "" {
			writeStatus(w, http.StatusBadRequest, "BadRequest", "a cron schedule is required for cron/heartbeat types")
			return
		}
	case agentsv1alpha1.ScheduleTypeWakeup:
		if strings.TrimSpace(req.RunAt) == "" {
			writeStatus(w, http.StatusBadRequest, "BadRequest", "runAt is required for wakeup type")
			return
		}
	default:
		writeStatus(w, http.StatusBadRequest, "BadRequest", "type must be cron, wakeup, or heartbeat")
		return
	}

	sched := &agentsv1alpha1.Schedule{
		ObjectMeta: metav1.ObjectMeta{Name: req.Name},
		Spec: agentsv1alpha1.ScheduleSpec{
			AgentRef:  req.AgentRef,
			Type:      req.Type,
			Schedule:  req.Schedule,
			TimeZone:  req.TimeZone,
			Task:      req.Task,
			Checklist: req.Checklist,
			Suspend:   req.Suspend,
		},
	}
	if req.RunAt != "" {
		t, err := time.Parse(time.RFC3339, req.RunAt)
		if err != nil {
			writeStatus(w, http.StatusBadRequest, "BadRequest", "runAt must be RFC3339 (e.g. 2026-07-13T09:00:00Z): "+err.Error())
			return
		}
		mt := metav1.NewTime(t)
		sched.Spec.RunAt = &mt
	}
	out, err := c.Schedules().Create(r.Context(), sched, metav1.CreateOptions{})
	if err != nil {
		writeResourceError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

// updateScheduleRequest patches an existing schedule. Pointer fields let the
// caller change only what they send (retime the cron, edit the task, pause).
type updateScheduleRequest struct {
	Schedule  *string `json:"schedule,omitempty"`
	TimeZone  *string `json:"timeZone,omitempty"`
	RunAt     *string `json:"runAt,omitempty"`
	Task      *string `json:"task,omitempty"`
	Checklist *string `json:"checklist,omitempty"`
	Suspend   *bool   `json:"suspend,omitempty"`
}

func (s *Server) updateSchedule(w http.ResponseWriter, r *http.Request) {
	c, _, ok := s.requireClient(w, r)
	if !ok {
		return
	}
	name := r.PathValue("name")
	sched, err := c.Schedules().Get(r.Context(), name, metav1.GetOptions{})
	if err != nil {
		writeResourceError(w, err)
		return
	}
	var req updateScheduleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeStatus(w, http.StatusBadRequest, "BadRequest", "invalid JSON body: "+err.Error())
		return
	}
	if req.Schedule != nil {
		sched.Spec.Schedule = strings.TrimSpace(*req.Schedule)
	}
	if req.TimeZone != nil {
		sched.Spec.TimeZone = strings.TrimSpace(*req.TimeZone)
	}
	if req.Task != nil {
		sched.Spec.Task = *req.Task
	}
	if req.Checklist != nil {
		sched.Spec.Checklist = *req.Checklist
	}
	if req.Suspend != nil {
		sched.Spec.Suspend = *req.Suspend
	}
	if req.RunAt != nil {
		if v := strings.TrimSpace(*req.RunAt); v != "" {
			t, perr := time.Parse(time.RFC3339, v)
			if perr != nil {
				writeStatus(w, http.StatusBadRequest, "BadRequest", "runAt must be RFC3339: "+perr.Error())
				return
			}
			mt := metav1.NewTime(t)
			sched.Spec.RunAt = &mt
		} else {
			sched.Spec.RunAt = nil
		}
	}
	out, err := c.Schedules().Update(r.Context(), sched, metav1.UpdateOptions{})
	if err != nil {
		writeResourceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) deleteSchedule(w http.ResponseWriter, r *http.Request) {
	c, _, ok := s.requireClient(w, r)
	if !ok {
		return
	}
	if err := c.Schedules().Delete(r.Context(), r.PathValue("name"), metav1.DeleteOptions{}); err != nil {
		writeResourceError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// runScheduleNow executes a schedule's task immediately (synchronous, as the
// calling user). This is how a user tests a schedule before relying on the
// background scheduler to fire it. Heartbeats run their checklist; cron/wakeup
// run their task.
func (s *Server) runScheduleNow(w http.ResponseWriter, r *http.Request) {
	c, id, ok := s.requireClient(w, r)
	if !ok {
		return
	}
	name := r.PathValue("name")
	sched, err := c.Schedules().Get(r.Context(), name, metav1.GetOptions{})
	if err != nil {
		writeResourceError(w, err)
		return
	}
	agent, err := c.Agents().Get(r.Context(), sched.Spec.AgentRef, metav1.GetOptions{})
	if err != nil {
		writeResourceError(w, err)
		return
	}
	task := sched.Spec.Task
	trigger := agentsv1alpha1.RunTriggerSchedule
	if sched.Spec.Type == agentsv1alpha1.ScheduleTypeHeartbeat {
		trigger = agentsv1alpha1.RunTriggerHeartbeat
		task = "Review this standing checklist and report only if something is actionable:\n\n" + sched.Spec.Checklist
	}
	if strings.TrimSpace(task) == "" {
		writeStatus(w, http.StatusBadRequest, "BadRequest", "this schedule has no task/checklist to run")
		return
	}
	res, err := s.executeTask(r.Context(), taskRun{
		Creds: c, CR: clientCR{c}, Scope: id.scope(agent.Name), Agent: agent,
		SessionID: "schedule:" + name, Task: task, Trigger: trigger, SourceName: name,
		EdgesEndpoint: s.edgesEndpoint(id.clusterID), EdgesToken: id.token, EdgesInsecure: s.cfg.HubInsecure,
	})
	if err != nil {
		if s.credentialsError(err) {
			writeStatus(w, http.StatusBadRequest, "BadRequest", "no model configured — open Model settings to add one")
			return
		}
		writeStatus(w, http.StatusBadGateway, "RunFailed", err.Error())
		return
	}
	// Deliver to the agent's notify channel so "Run now" fires like a real run.
	s.deliverToNotifyChannel(r, c, agent, name, res.Content)
	writeJSON(w, http.StatusOK, res)
}
