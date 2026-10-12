// Copyright 2026 The Railgrid Authors.
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
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/google/uuid"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	agentsv1alpha1 "github.com/railgrid/provider-agents/apis/v1alpha1"
	"github.com/railgrid/provider-agents/channels"
	agentsclient "github.com/railgrid/provider-agents/client"
	"github.com/railgrid/provider-agents/internal/triggerfilter"
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
		// Uncapped: channels.Send splits a long answer across messages, so
		// clipping here would throw most of it away first.
		Text: msg,
	})
}

type createTriggerRequest struct {
	Name     string `json:"name"`
	AgentRef string `json:"agentRef"`
	// WebhookRef names the Webhook the trigger routes from.
	WebhookRef string            `json:"webhookRef"`
	Filter     map[string]string `json:"filter,omitempty"`
	Task       string            `json:"task,omitempty"`
	Suspend    bool              `json:"suspend,omitempty"`
	// ChannelRef routes this trigger's output to a named agent channel; empty
	// means the agent's primary channel.
	ChannelRef string `json:"channelRef,omitempty"`
}

// checkFilterKeys refuses a filter with a key the matcher does not know. The
// matcher itself ignores such a key (fail-open, so a filter written for a
// future source never blocks every event), which makes a typo a filter that
// silently lets everything through; a writer that can answer synchronously
// says so instead. A kube write cannot be refused this way, so the Trigger
// reconciler reports the same thing as Validated=False/UnknownFilterKey.
func checkFilterKeys(filter map[string]string) error {
	unknown := triggerfilter.UnknownKeys(filter)
	if len(unknown) == 0 {
		return nil
	}
	return errBadRequest(fmt.Sprintf("unknown filter key %s (known: %s)",
		strings.Join(unknown, ", "), strings.Join(triggerfilter.KnownKeys, ", ")))
}

// applyTriggerCreate validates the request and creates the trigger. The
// trigger owns no URL: it routes from the Webhook it names, whose reconciler
// mints the inbound path. Used by the MCP create_trigger tool.
func (s *Server) applyTriggerCreate(ctx context.Context, c *agentsclient.Client, req *createTriggerRequest) (*agentsv1alpha1.Trigger, error) {
	req.Name = strings.TrimSpace(req.Name)
	req.AgentRef = strings.TrimSpace(req.AgentRef)
	req.WebhookRef = strings.TrimSpace(req.WebhookRef)
	if req.Name == "" || req.AgentRef == "" || req.WebhookRef == "" {
		return nil, errBadRequest("name, agentRef, and webhookRef are required")
	}
	if err := checkFilterKeys(req.Filter); err != nil {
		return nil, err
	}
	trig := &agentsv1alpha1.Trigger{
		ObjectMeta: metav1.ObjectMeta{Name: req.Name},
		Spec: agentsv1alpha1.TriggerSpec{
			AgentRef:   req.AgentRef,
			WebhookRef: req.WebhookRef,
			Filter:     req.Filter,
			Task:       req.Task,
			Suspend:    req.Suspend,
			ChannelRef: strings.TrimSpace(req.ChannelRef),
		},
	}
	return c.Triggers().Create(ctx, trig, metav1.CreateOptions{})
}

// updateTriggerRequest patches an existing trigger. All fields are optional
// pointers so callers change only what they send (task edits, pause/resume,
// a new filter, or a move to another webhook).
type updateTriggerRequest struct {
	Task       *string            `json:"task,omitempty"`
	WebhookRef *string            `json:"webhookRef,omitempty"`
	Filter     *map[string]string `json:"filter,omitempty"`
	Suspend    *bool              `json:"suspend,omitempty"`
	ChannelRef *string            `json:"channelRef,omitempty"`
}

// applyTriggerUpdate reads the trigger, applies the patch fields that are
// present, and writes it back. Used by the MCP update_trigger tool.
func (s *Server) applyTriggerUpdate(ctx context.Context, c *agentsclient.Client, name string, req *updateTriggerRequest) (*agentsv1alpha1.Trigger, error) {
	name = strings.TrimSpace(name)
	trig, err := c.Triggers().Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	if req.Task != nil {
		trig.Spec.Task = *req.Task
	}
	if req.WebhookRef != nil {
		ref := strings.TrimSpace(*req.WebhookRef)
		if ref == "" {
			return nil, errBadRequest("webhookRef cannot be empty: a trigger routes from a webhook")
		}
		trig.Spec.WebhookRef = ref
	}
	if req.Filter != nil {
		if err := checkFilterKeys(*req.Filter); err != nil {
			return nil, err
		}
		trig.Spec.Filter = *req.Filter
	}
	if req.Suspend != nil {
		trig.Spec.Suspend = *req.Suspend
	}
	if req.ChannelRef != nil {
		trig.Spec.ChannelRef = strings.TrimSpace(*req.ChannelRef)
	}
	return c.Triggers().Update(ctx, trig, metav1.UpdateOptions{})
}

// runTriggerNow fires a trigger's task immediately with an optional payload,
// asynchronously: 202 + runID, output delivered to the
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
	// Two bodies are understood. {"deliveryID": "<id>"} replays a recorded
	// delivery of the trigger's webhook through this trigger alone, as it
	// stands — filter included — which is the editor's "run that one again"
	// after a filter fix. Anything else is
	// an optional test payload appended to the task so the run can react to
	// it, bypassing the filter: the caller is firing the trigger, not asking
	// whether it would fire.
	var payload map[string]any
	_ = json.NewDecoder(io.LimitReader(r.Body, webhookBodyLimit)).Decode(&payload)
	if raw, ok := payload["deliveryID"].(string); ok && strings.TrimSpace(raw) != "" && len(payload) == 1 {
		tu, gated, ok := gatedTrigger(r)
		if !ok {
			writeStatus(w, http.StatusInternalServerError, "InternalError", "the gate resolved no Trigger for this request")
			return
		}
		s.replayTriggerDelivery(w, r, c, id, tu, gated, strings.TrimSpace(raw))
		return
	}
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
	admission, err := s.startDetachedRun(r, c, id, agent, taskRun{
		SessionID: triggerSessionID(name, ""), Task: task, Trigger: agentsv1alpha1.RunTriggerEvent, SourceName: name,
		NotifyChannel: trig.Spec.ChannelRef,
	})
	if err != nil {
		writeStatus(w, http.StatusServiceUnavailable, "ServiceUnavailable", "the run could not be recorded; retry later")
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"runID": admission.ID})
}

// triggerSessionID names the transcript session for one firing of a trigger.
//
// One session per delivery, not per trigger. Every event is an independent
// piece of work — a pull request to review, a deploy to look at — and the runs
// have nothing to say to each other. Sharing a session made each firing
// replay every earlier one: the transcript grew by a full event payload per
// run and the harness prompt was assembled from the lot, until a trigger that
// had fired a few dozen times could not fit a request under the runner's 2MiB
// limit and disabled itself on the failures. A shared session also let two
// deliveries dispatched together collide on one harness session.
//
// deliveryID is the platform's own id for the event (GitHub's
// X-GitHub-Delivery) when the sender gave one, so a redelivery of the same
// event lands in the same session; otherwise the firing gets a fresh id.
func triggerSessionID(name, deliveryID string) string {
	deliveryID = strings.TrimSpace(deliveryID)
	if deliveryID == "" {
		deliveryID = uuid.NewString()
	}
	return "trigger:" + name + "/" + deliveryID
}

// enableTrigger clears the disabling the provider applied after repeated
// failures — status.disabledReason and the consecutive-failure count — so the
// trigger fires again. It is the recovery path for a trigger whose runs failed
// for a reason that has since been fixed; a trigger disabled on a permanent
// error will be disabled again by whatever disabled it. spec.suspend is a
// separate, user-authored switch and is left alone.
func (s *Server) enableTrigger(w http.ResponseWriter, r *http.Request) {
	c, _, ok := s.requireClient(w, r)
	if !ok {
		return
	}
	name := r.PathValue("name")
	trig, err := c.Triggers().Get(r.Context(), name, metav1.GetOptions{})
	if err != nil {
		writeResourceError(w, err)
		return
	}
	if trig.Status.DisabledReason == "" && trig.Status.ConsecutiveFailures == 0 {
		writeJSON(w, http.StatusOK, trig)
		return
	}
	// Cleared by patching the fields to null: a status update built from the
	// typed object omits zero values and would leave the stored ones in place.
	if err := c.Triggers().ClearStatusFields(r.Context(), name, "disabledReason", "consecutiveFailures"); err != nil {
		writeResourceError(w, err)
		return
	}
	out, err := c.Triggers().Get(r.Context(), name, metav1.GetOptions{})
	if err != nil {
		writeResourceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}
