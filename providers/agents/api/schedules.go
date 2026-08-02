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
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	agentsv1alpha1 "github.com/faroshq/provider-agents/apis/v1alpha1"
	agentsclient "github.com/faroshq/provider-agents/client"
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
	// ChannelRef routes this schedule's output to a named agent channel; empty
	// means the agent's primary channel.
	ChannelRef string `json:"channelRef,omitempty"`
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
	out, err := applyScheduleCreate(r.Context(), c, &req)
	if err != nil {
		writeUpdateError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

// applyScheduleCreate validates the request and creates the schedule. Shared by
// the REST handler and the MCP create_schedule tool so both surfaces validate
// identically.
func applyScheduleCreate(ctx context.Context, c *agentsclient.Client, req *createScheduleRequest) (*agentsv1alpha1.Schedule, error) {
	req.Name = strings.TrimSpace(req.Name)
	req.AgentRef = strings.TrimSpace(req.AgentRef)
	req.Type = strings.TrimSpace(req.Type)
	if req.Name == "" || req.AgentRef == "" || req.Type == "" {
		return nil, errBadRequest("name, agentRef, and type are required")
	}
	switch req.Type {
	case agentsv1alpha1.ScheduleTypeCron, agentsv1alpha1.ScheduleTypeHeartbeat:
		if strings.TrimSpace(req.Schedule) == "" {
			return nil, errBadRequest("a cron schedule is required for cron/heartbeat types")
		}
	case agentsv1alpha1.ScheduleTypeWakeup:
		if strings.TrimSpace(req.RunAt) == "" {
			return nil, errBadRequest("runAt is required for wakeup type")
		}
	default:
		return nil, errBadRequest("type must be cron, wakeup, or heartbeat")
	}

	sched := &agentsv1alpha1.Schedule{
		ObjectMeta: metav1.ObjectMeta{Name: req.Name},
		Spec: agentsv1alpha1.ScheduleSpec{
			AgentRef:   req.AgentRef,
			Type:       req.Type,
			Schedule:   req.Schedule,
			TimeZone:   req.TimeZone,
			Task:       req.Task,
			Checklist:  req.Checklist,
			Suspend:    req.Suspend,
			ChannelRef: strings.TrimSpace(req.ChannelRef),
		},
	}
	if req.RunAt != "" {
		t, err := time.Parse(time.RFC3339, req.RunAt)
		if err != nil {
			return nil, errBadRequest("runAt must be RFC3339 (e.g. 2026-07-13T09:00:00Z): " + err.Error())
		}
		mt := metav1.NewTime(t)
		sched.Spec.RunAt = &mt
	}
	return c.Schedules().Create(ctx, sched, metav1.CreateOptions{})
}

// updateScheduleRequest patches an existing schedule. Pointer fields let the
// caller change only what they send (retime the cron, edit the task, pause).
type updateScheduleRequest struct {
	Schedule   *string `json:"schedule,omitempty"`
	TimeZone   *string `json:"timeZone,omitempty"`
	RunAt      *string `json:"runAt,omitempty"`
	Task       *string `json:"task,omitempty"`
	Checklist  *string `json:"checklist,omitempty"`
	Suspend    *bool   `json:"suspend,omitempty"`
	ChannelRef *string `json:"channelRef,omitempty"`
}

func (s *Server) updateSchedule(w http.ResponseWriter, r *http.Request) {
	c, _, ok := s.requireClient(w, r)
	if !ok {
		return
	}
	var req updateScheduleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeStatus(w, http.StatusBadRequest, "BadRequest", "invalid JSON body: "+err.Error())
		return
	}
	out, err := applyScheduleUpdate(r.Context(), c, r.PathValue("name"), &req)
	if err != nil {
		writeUpdateError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// applyScheduleUpdate reads the schedule, applies the patch fields that are
// present, and writes it back. Shared by the REST handler and the MCP
// update_schedule tool so both surfaces have identical semantics: absent fields
// are untouched, and an empty runAt clears the one-shot fire time.
func applyScheduleUpdate(ctx context.Context, c *agentsclient.Client, name string, req *updateScheduleRequest) (*agentsv1alpha1.Schedule, error) {
	sched, err := c.Schedules().Get(ctx, strings.TrimSpace(name), metav1.GetOptions{})
	if err != nil {
		return nil, err
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
	if req.ChannelRef != nil {
		sched.Spec.ChannelRef = strings.TrimSpace(*req.ChannelRef)
	}
	if req.RunAt != nil {
		if v := strings.TrimSpace(*req.RunAt); v != "" {
			t, perr := time.Parse(time.RFC3339, v)
			if perr != nil {
				return nil, errBadRequest("runAt must be RFC3339: " + perr.Error())
			}
			mt := metav1.NewTime(t)
			sched.Spec.RunAt = &mt
		} else {
			sched.Spec.RunAt = nil
		}
	}
	return c.Schedules().Update(ctx, sched, metav1.UpdateOptions{})
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

// runScheduleNow fires a schedule's task immediately as the calling user,
// asynchronously: it returns 202 with the runID and the run executes in the
// background — follow it via GET /api/runs/{id} or the /api/events stream.
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
	runID := s.startDetachedRun(r, c, id, agent, taskRun{
		SessionID: "schedule:" + name, Task: task, Trigger: trigger, SourceName: name,
		NotifyChannel: sched.Spec.ChannelRef,
	})
	writeJSON(w, http.StatusAccepted, map[string]string{"runID": runID})
}
