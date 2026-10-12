// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package api

// The inbound trigger webhook and its delivery log.
//
// A Trigger used to leave exactly one trace: the Run it fired. A delivery the
// filter dropped was acked with a reason the SENDER saw and nobody else, a
// delivery that arrived while the trigger was paused answered 409 into the
// void, and a trigger whose filter dropped everything looked, from its status,
// identical to one nobody called. Every authenticated delivery is now recorded
// — what came in, what the matcher read off it, and what became of it — capped
// per trigger (spec.deliveryRetention, default Config.TriggerDeliveryRetention)
// and read back through two verbs: triggers/{name}/deliveries lists the log,
// and triggers/{name}/evaluate runs a candidate filter against a recorded
// delivery through the SAME matcher the webhook uses, so the portal's filter
// editor shows what production would decide. triggers/{name}/run with a
// deliveryID re-feeds a recorded delivery through the trigger.
//
// Only token-valid requests are recorded. The URL's HMAC is the trigger's
// whole credential, so a POST with a bad token is unauthenticated traffic, and
// writing a row per such request would let anyone who guessed the URL shape
// fill a tenant's log.

import (
	"context"
	"crypto/hmac"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"

	agentsv1alpha1 "github.com/railgrid/provider-agents/apis/v1alpha1"
	agentsclient "github.com/railgrid/provider-agents/client"
	"github.com/railgrid/provider-agents/executor"
	"github.com/railgrid/provider-agents/internal/triggerfilter"
	"github.com/railgrid/provider-agents/store"
)

// webhookBodyLimit is how much of a delivery's body is read, and therefore
// how much the matcher sees and the log keeps. GitHub's largest payloads are
// well under it; anything past it is cut, and the delivery says so.
const webhookBodyLimit = 64 * 1024

// lastDeliveryCoalesce is how often status.lastReceived/lastDelivery are
// rewritten for a run of deliveries with the same outcome and reason. A busy
// organization webhook can deliver several times a second; a status write per
// POST would fight the Trigger reconciler for the object for no information
// gain, since the log has every delivery anyway.
const lastDeliveryCoalesce = time.Minute

// redactedHeaderValue stands in for a recorded header whose value looks like
// a credential. The header's presence is kept so a filter on it still shows
// up in the editor; its value is not.
const redactedHeaderValue = "[redacted]"

// droppedHeaderPrefixes and droppedHeaders are request headers the log never
// keeps: transport and hub plumbing a sender did not write, and credentials.
var (
	droppedHeaderPrefixes = []string{"X-Remote-", "X-Railgrid-", "X-Forwarded-", "Proxy-", "Sec-"}
	droppedHeaders        = map[string]bool{
		"Authorization": true, "Cookie": true, "Set-Cookie": true, "Forwarded": true, "Via": true,
		"Connection": true, "Keep-Alive": true, "Transfer-Encoding": true, "Upgrade": true,
		"Content-Length": true, "Accept-Encoding": true, "X-Real-Ip": true, "X-Request-Start": true,
	}
	// redactedHeaderMarkers are substrings of a header name that mark its
	// value as a credential. GitHub's X-Hub-Signature-256 is the common case.
	redactedHeaderMarkers = []string{"signature", "secret", "token", "auth", "key", "password", "credential", "hmac"}
)

// recordedHeaders is the subset of a delivery's request headers the log keeps,
// values redacted where the name says they are credentials. Multi-valued
// headers are joined the way a sender would see them echoed.
func recordedHeaders(h http.Header) map[string]string {
	out := map[string]string{}
	for name, values := range h {
		canonical := http.CanonicalHeaderKey(name)
		if droppedHeaders[canonical] {
			continue
		}
		dropped := false
		for _, prefix := range droppedHeaderPrefixes {
			if strings.HasPrefix(canonical, prefix) {
				dropped = true
				break
			}
		}
		if dropped {
			continue
		}
		value := strings.Join(values, ", ")
		lower := strings.ToLower(canonical)
		for _, marker := range redactedHeaderMarkers {
			if strings.Contains(lower, marker) {
				value = redactedHeaderValue
				break
			}
		}
		out[canonical] = value
	}
	return out
}

// headersFromRecorded rebuilds an http.Header from a recorded delivery, for
// re-running the matcher against it.
func headersFromRecorded(m map[string]string) http.Header {
	h := http.Header{}
	for k, v := range m {
		h.Set(k, v)
	}
	return h
}

// deliveryRetention is the log cap for one trigger: its own
// spec.deliveryRetention when set (0 keeps nothing), else the operator's
// default, else DefaultTriggerDeliveryRetention; bounded above either way.
func (s *Server) deliveryRetention(trig *agentsv1alpha1.Trigger) int {
	n := s.cfg.TriggerDeliveryRetention
	if n <= 0 {
		n = DefaultTriggerDeliveryRetention
	}
	if trig != nil && trig.Spec.DeliveryRetention != nil {
		n = int(*trig.Spec.DeliveryRetention)
	}
	if n > MaxTriggerDeliveryRetention {
		n = MaxTriggerDeliveryRetention
	}
	if n < 0 {
		n = 0
	}
	return n
}

// triggerDelivery is one inbound event on its way through a trigger: the
// request as the webhook received it, or a recorded delivery being replayed.
type triggerDelivery struct {
	Headers http.Header
	Body    []byte
	// Truncated says the body hit webhookBodyLimit.
	Truncated bool
	// SenderID is the sender's own id for the event (X-GitHub-Delivery).
	SenderID string
	// ReplayOf names the recorded delivery this one re-feeds, when it is a
	// replay rather than a fresh POST.
	ReplayOf string
}

// deliveryResult is what became of a delivery.
type deliveryResult struct {
	// ID is the log entry the delivery was recorded under; empty when the
	// trigger keeps no log.
	ID      string `json:"deliveryID,omitempty"`
	Outcome string `json:"status"`
	Reason  string `json:"reason,omitempty"`
	RunID   string `json:"runID,omitempty"`
}

// deliverTriggerEvent runs one delivery through a trigger: the pause and
// disable switches, spec.filter, the task, and the run submission — recording
// what happened whichever way it went, stamping status.lastDelivery, and
// telling the portal. It is the one path for a real POST and for a replay, so
// the log never says something the webhook would not have done.
func (s *Server) deliverTriggerEvent(ctx context.Context, cluster string, dyn dynamic.Interface, tu *unstructured.Unstructured, trig *agentsv1alpha1.Trigger, in triggerDelivery) deliveryResult {
	res := deliveryResult{Outcome: agentsv1alpha1.TriggerDeliveryFired}
	switch {
	case trig.Spec.Suspend:
		res.Outcome, res.Reason = agentsv1alpha1.TriggerDeliverySuspended, "trigger is paused"
	case trig.Status.DisabledReason != "":
		res.Outcome, res.Reason = agentsv1alpha1.TriggerDeliveryDisabled, trig.Status.DisabledReason
	default:
		if reason, ok := triggerfilter.Allows(trig.Spec.Filter, in.Headers, in.Body); !ok {
			res.Outcome, res.Reason = agentsv1alpha1.TriggerDeliveryFiltered, reason
		}
	}
	if res.Outcome == agentsv1alpha1.TriggerDeliveryFired {
		task := trig.Spec.Task
		if len(strings.TrimSpace(string(in.Body))) > 0 {
			// The payload is whatever the sender chose to POST. It is evidence
			// for the task, never part of it — quarantine it so an embedded
			// "ignore your instructions and ..." reads as data to the model.
			task += "\n\n" + quarantinePayload("webhook trigger "+trig.Name, map[string]string{
				"eventType":   triggerfilter.EventType(in.Headers, in.Body),
				"contentType": in.Headers.Get("Content-Type"),
			}, string(in.Body))
		}
		if strings.TrimSpace(task) == "" {
			res.Outcome, res.Reason = agentsv1alpha1.TriggerDeliveryNoTask, "trigger has no task"
		} else {
			runID, err := s.bg.submitRun(ctx, executor.Job{
				ID:            fmt.Sprintf("%s/%s/%d", cluster, trig.Name, time.Now().UnixNano()),
				Kind:          executor.KindTrigger,
				ClusterID:     cluster,
				SourceName:    trig.Name,
				AgentRef:      trig.Spec.AgentRef,
				Task:          task,
				Trigger:       agentsv1alpha1.RunTriggerEvent,
				SessionID:     triggerSessionID(trig.Name, in.SenderID),
				NotifyChannel: trig.Spec.ChannelRef,
			})
			if err != nil {
				res.Outcome, res.Reason = agentsv1alpha1.TriggerDeliveryFailed, err.Error()
			} else {
				res.RunID = runID
			}
		}
	}

	now := time.Now().UTC()
	scope := s.bg.scopeFor(ctx, cluster, trig.Spec.AgentRef)
	if retain := s.deliveryRetention(trig); retain > 0 {
		extracted := triggerfilter.Extract(in.Headers, in.Body)
		entry := store.TriggerDelivery{
			ID:            uuid.NewString(),
			TriggerName:   trig.Name,
			TriggerUID:    string(trig.UID),
			DeliveryID:    strings.TrimSpace(in.SenderID),
			ReceivedAt:    now,
			Outcome:       res.Outcome,
			Reason:        res.Reason,
			EventType:     extracted.EventType,
			Action:        extracted.Action,
			Repository:    extracted.Repository,
			ContentType:   in.Headers.Get("Content-Type"),
			Headers:       recordedHeaders(in.Headers),
			Body:          string(in.Body),
			BodyTruncated: in.Truncated,
			RunID:         res.RunID,
			ReplayOf:      in.ReplayOf,
		}
		if err := s.store.RecordTriggerDelivery(ctx, scope, entry, retain); err != nil {
			// The log is an aid, not the event: a delivery that fired is not
			// un-fired because its row failed, and one that was filtered is
			// still acked. Say so in the provider log and move on.
			log.Printf("agents: recording delivery for trigger %s/%s: %v", cluster, trig.Name, err)
		} else {
			res.ID = entry.ID
		}
	}
	s.stampLastDelivery(ctx, dyn, tu, trig, now, res)
	s.events.publish(store.Scope{OrgUUID: scope.OrgUUID, WorkspaceUUID: scope.WorkspaceUUID}, "trigger-delivery", map[string]any{
		"name": trig.Name, "agent": trig.Spec.AgentRef, "id": res.ID, "outcome": res.Outcome, "runID": res.RunID,
	})
	return res
}

// stampLastDelivery writes status.lastReceived and status.lastDelivery,
// coalesced: a delivery whose outcome and reason match the stored summary
// refreshes it at most once per lastDeliveryCoalesce. Best-effort — the
// reconciler may be writing the same object, and the log has the delivery
// regardless.
func (s *Server) stampLastDelivery(ctx context.Context, dyn dynamic.Interface, tu *unstructured.Unstructured, trig *agentsv1alpha1.Trigger, now time.Time, res deliveryResult) {
	if dyn == nil || tu == nil {
		return
	}
	if last := trig.Status.LastDelivery; last != nil && trig.Status.LastReceived != nil &&
		last.Outcome == res.Outcome && last.Reason == res.Reason &&
		now.Sub(trig.Status.LastReceived.Time) < lastDeliveryCoalesce {
		return
	}
	status, _, _ := unstructured.NestedMap(tu.Object, "status")
	if status == nil {
		status = map[string]any{}
	}
	stamp := now.Format(time.RFC3339)
	status["lastReceived"] = stamp
	summary := map[string]any{"time": stamp, "outcome": res.Outcome}
	if res.Reason != "" {
		summary["reason"] = res.Reason
	}
	if res.ID != "" {
		summary["deliveryID"] = res.ID
	}
	status["lastDelivery"] = summary
	_ = unstructured.SetNestedMap(tu.Object, status, "status")
	if _, err := dyn.Resource(agentsclient.TriggerGVR).UpdateStatus(ctx, tu, metav1.UpdateOptions{}); err != nil {
		log.Printf("agents: stamping lastDelivery on trigger %s: %v", trig.Name, err)
	}
}

// ---- the inbound webhook ----------------------------------------------------

// webhookTrigger fires an event trigger from an external POST. The URL embeds
// an HMAC token (no tenant headers required): the hub forwards anonymous
// calls to the provider with identity headers stripped, so the token is the
// auth. Responds 202 and executes asynchronously.
func (s *Server) webhookTrigger(w http.ResponseWriter, r *http.Request) {
	cluster, name, token := r.PathValue("cluster"), r.PathValue("name"), r.PathValue("token")
	expected := s.webhookToken(cluster, name)
	if expected == "" || s.bg == nil || !s.bg.ready() {
		writeStatus(w, http.StatusServiceUnavailable, "Unavailable", "background executor is not running on this provider")
		return
	}
	if !hmac.Equal([]byte(expected), []byte(token)) {
		writeStatus(w, http.StatusForbidden, "Forbidden", "invalid webhook token")
		return
	}
	dyn, err := s.bg.scoped(r.Context(), cluster)
	if err != nil {
		writeStatus(w, http.StatusInternalServerError, "InternalError", err.Error())
		return
	}
	tu, err := dyn.Resource(agentsclient.TriggerGVR).Get(r.Context(), name, metav1.GetOptions{})
	if err != nil {
		writeStatus(w, http.StatusNotFound, "NotFound", "trigger not found")
		return
	}
	trig, err := fromU[agentsv1alpha1.Trigger](tu)
	if err != nil {
		writeStatus(w, http.StatusInternalServerError, "InternalError", "trigger object is not a Trigger: "+err.Error())
		return
	}
	body, _ := io.ReadAll(io.LimitReader(r.Body, webhookBodyLimit+1))
	truncated := len(body) > webhookBodyLimit
	if truncated {
		body = body[:webhookBodyLimit]
	}
	res := s.deliverTriggerEvent(r.Context(), cluster, dyn, tu, trig, triggerDelivery{
		Headers: r.Header, Body: body, Truncated: truncated, SenderID: r.Header.Get("X-GitHub-Delivery"),
	})
	switch res.Outcome {
	case agentsv1alpha1.TriggerDeliverySuspended, agentsv1alpha1.TriggerDeliveryDisabled:
		writeStatus(w, http.StatusConflict, "Suspended", "trigger is suspended or disabled")
	case agentsv1alpha1.TriggerDeliveryNoTask:
		writeStatus(w, http.StatusBadRequest, "BadRequest", "trigger has no task")
	case agentsv1alpha1.TriggerDeliveryFailed:
		// Nothing was recorded that will ever run; the platform redelivers.
		writeStatus(w, http.StatusServiceUnavailable, "Unavailable", res.Reason)
	case agentsv1alpha1.TriggerDeliveryFiltered:
		// A filtered-out event is acked (202) so the sender does not retry.
		writeJSON(w, http.StatusAccepted, res)
	default:
		writeJSON(w, http.StatusAccepted, res)
	}
}

// ---- the verbs --------------------------------------------------------------

// gatedTrigger is the Trigger the data-plane gate read for this request.
func gatedTrigger(r *http.Request) (*unstructured.Unstructured, *agentsv1alpha1.Trigger, bool) {
	gate, ok := gateFrom(r.Context())
	if !ok || gate.object == nil {
		return nil, nil, false
	}
	trig, err := fromU[agentsv1alpha1.Trigger](gate.object)
	if err != nil || trig.UID == "" {
		return nil, nil, false
	}
	return gate.object, trig, true
}

// maxDeliveriesPage caps one deliveries listing.
const maxDeliveriesPage = 200

// listTriggerDeliveries serves GET triggers/{name}/deliveries — the log,
// newest first, bodies cut to a preview — and GET
// triggers/{name}/deliveries/{id}, one delivery whole. The trigger's UID keys
// the log, so a recreated trigger does not inherit its predecessor's.
func (s *Server) listTriggerDeliveries(w http.ResponseWriter, r *http.Request) {
	_, id, ok := s.requireClient(w, r)
	if !ok {
		return
	}
	_, trig, ok := gatedTrigger(r)
	if !ok {
		writeStatus(w, http.StatusInternalServerError, "InternalError", "the gate resolved no Trigger for this request")
		return
	}
	scope := id.scope(trig.Spec.AgentRef)
	if tail := strings.TrimSpace(r.PathValue("tail")); tail != "" {
		if strings.Contains(tail, "/") {
			writeStatus(w, http.StatusBadRequest, "BadRequest", "deliveries takes one delivery id")
			return
		}
		d, found, err := s.store.GetTriggerDelivery(r.Context(), scope, string(trig.UID), tail)
		if err != nil {
			writeStatus(w, http.StatusInternalServerError, "InternalError", err.Error())
			return
		}
		if !found {
			writeStatus(w, http.StatusNotFound, "NotFound", "no such delivery")
			return
		}
		writeJSON(w, http.StatusOK, d)
		return
	}
	q := store.TriggerDeliveryQuery{Outcome: strings.TrimSpace(r.URL.Query().Get("outcome")), Limit: 50}
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		n := 0
		if _, err := fmt.Sscanf(raw, "%d", &n); err != nil || n <= 0 {
			writeStatus(w, http.StatusBadRequest, "BadRequest", "limit must be a positive integer")
			return
		}
		q.Limit = min(n, maxDeliveriesPage)
	}
	items, err := s.store.ListTriggerDeliveries(r.Context(), scope, string(trig.UID), q)
	if err != nil {
		writeStatus(w, http.StatusInternalServerError, "InternalError", err.Error())
		return
	}
	writeList(w, items, map[string]any{"retention": s.deliveryRetention(trig)})
}

// evaluateTriggerFilterRequest is the body of triggers/{name}/evaluate: a
// candidate filter, and the delivery to judge it against — a recorded one by
// id, or a request spelled out.
type evaluateTriggerFilterRequest struct {
	// Filter is the candidate spec.filter. Nil means the trigger's own.
	Filter *map[string]string `json:"filter,omitempty"`
	// DeliveryID names a recorded delivery of this trigger.
	DeliveryID string `json:"deliveryID,omitempty"`
	// Request is a delivery spelled out, for a sample the log does not hold.
	Request *evaluateTriggerFilterSample `json:"request,omitempty"`
}

type evaluateTriggerFilterSample struct {
	Headers map[string]string `json:"headers,omitempty"`
	Body    string            `json:"body,omitempty"`
}

// evaluateTriggerFilterResponse is the verdict, with the keys the delivery
// was judged on and which delivery it was.
type evaluateTriggerFilterResponse struct {
	triggerfilter.Verdict
	DeliveryID string `json:"deliveryID,omitempty"`
	// Filter is the filter that was evaluated, as the matcher saw it.
	Filter map[string]string `json:"filter"`
}

// evaluateTriggerFilter serves POST triggers/{name}/evaluate. It changes
// nothing: it runs the matcher the webhook runs and reports per key, which
// is what lets the portal paint a filter green or red as it is typed.
func (s *Server) evaluateTriggerFilter(w http.ResponseWriter, r *http.Request) {
	_, id, ok := s.requireClient(w, r)
	if !ok {
		return
	}
	_, trig, ok := gatedTrigger(r)
	if !ok {
		writeStatus(w, http.StatusInternalServerError, "InternalError", "the gate resolved no Trigger for this request")
		return
	}
	var req evaluateTriggerFilterRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, webhookBodyLimit*2)).Decode(&req); err != nil {
		writeStatus(w, http.StatusBadRequest, "BadRequest", "body must be JSON: "+err.Error())
		return
	}
	filter := trig.Spec.Filter
	if req.Filter != nil {
		filter = *req.Filter
	}
	if filter == nil {
		filter = map[string]string{}
	}
	var headers http.Header
	var body []byte
	switch {
	case strings.TrimSpace(req.DeliveryID) != "" && req.Request != nil:
		writeStatus(w, http.StatusBadRequest, "BadRequest", "give deliveryID or request, not both")
		return
	case strings.TrimSpace(req.DeliveryID) != "":
		d, found, err := s.store.GetTriggerDelivery(r.Context(), id.scope(trig.Spec.AgentRef), string(trig.UID), strings.TrimSpace(req.DeliveryID))
		if err != nil {
			writeStatus(w, http.StatusInternalServerError, "InternalError", err.Error())
			return
		}
		if !found {
			writeStatus(w, http.StatusNotFound, "NotFound", "no such delivery")
			return
		}
		headers, body = headersFromRecorded(d.Headers), []byte(d.Body)
	case req.Request != nil:
		headers, body = headersFromRecorded(req.Request.Headers), []byte(req.Request.Body)
	default:
		writeStatus(w, http.StatusBadRequest, "BadRequest", "name a recorded delivery (deliveryID) or spell one out (request)")
		return
	}
	writeJSON(w, http.StatusOK, evaluateTriggerFilterResponse{
		Verdict:    triggerfilter.Evaluate(filter, headers, body),
		DeliveryID: strings.TrimSpace(req.DeliveryID),
		Filter:     filter,
	})
}

// replayTriggerDelivery re-feeds a recorded delivery through the trigger as
// it stands now — pause switch, filter, task — exactly as a redelivery from
// the sender would be, and records the result as a new delivery pointing at
// the one it replayed. It is the "I fixed the filter, run that one" button.
func (s *Server) replayTriggerDelivery(w http.ResponseWriter, r *http.Request, id identity, tu *unstructured.Unstructured, trig *agentsv1alpha1.Trigger, deliveryID string) {
	if s.bg == nil || !s.bg.ready() {
		writeStatus(w, http.StatusServiceUnavailable, "Unavailable", "background executor is not running on this provider")
		return
	}
	d, found, err := s.store.GetTriggerDelivery(r.Context(), id.scope(trig.Spec.AgentRef), string(trig.UID), deliveryID)
	if err != nil {
		writeStatus(w, http.StatusInternalServerError, "InternalError", err.Error())
		return
	}
	if !found {
		writeStatus(w, http.StatusNotFound, "NotFound", "no such delivery")
		return
	}
	dyn, err := s.bg.scoped(r.Context(), id.clusterID)
	if err != nil {
		writeStatus(w, http.StatusInternalServerError, "InternalError", err.Error())
		return
	}
	res := s.deliverTriggerEvent(r.Context(), id.clusterID, dyn, tu, trig, triggerDelivery{
		Headers: headersFromRecorded(d.Headers), Body: []byte(d.Body), Truncated: d.BodyTruncated,
		SenderID: d.DeliveryID, ReplayOf: d.ID,
	})
	switch res.Outcome {
	case agentsv1alpha1.TriggerDeliveryFired:
		writeJSON(w, http.StatusAccepted, res)
	case agentsv1alpha1.TriggerDeliveryFailed:
		writeStatus(w, http.StatusServiceUnavailable, "Unavailable", res.Reason)
	default:
		// Not an error: the caller asked what the trigger would do with this
		// delivery, and the answer is "not fire", with the reason.
		writeJSON(w, http.StatusOK, res)
	}
}
