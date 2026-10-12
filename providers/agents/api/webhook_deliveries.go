// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package api

// The inbound webhook, its routing to triggers, and its delivery sink.
//
// A URL used to belong to exactly one Trigger, so one GitHub webhook could
// drive exactly one agent task, and a second task on the same repository meant
// pasting a second URL into GitHub. The URL now belongs to a Webhook, and any
// number of Triggers route from it by naming it in spec.webhookRef. Every
// authenticated POST is offered to each of them; each judges it with its own
// pause switch, filter and task, and what each made of it is one ROUTE.
//
// Every authenticated delivery is recorded once, in the webhook's sink, with
// all of its routes — what came in, what the matcher read off it, and what
// became of it per trigger — capped per webhook (spec.deliveryRetention,
// default Config.TriggerDeliveryRetention) and read back through
// webhooks/{name}/deliveries. triggers/{name}/evaluate runs a candidate filter
// against a recorded delivery through the SAME matcher, so the portal's filter
// editor shows what production would decide. webhooks/{name}/run re-feeds a
// recorded delivery (or a spelled-out sample) to every routed trigger;
// triggers/{name}/run with a deliveryID re-feeds it to one.
//
// Only token-valid requests are recorded. The URL's HMAC is the webhook's
// whole credential, so a POST with a bad token is unauthenticated traffic, and
// writing a row per such request would let anyone who guessed the URL shape
// fill a tenant's sink.

import (
	"context"
	"crypto/hmac"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"

	agentsv1alpha1 "github.com/railgrid/provider-agents/apis/v1alpha1"
	agentsclient "github.com/railgrid/provider-agents/client"
	"github.com/railgrid/provider-agents/executor"
	"github.com/railgrid/provider-agents/internal/triggerfilter"
	"github.com/railgrid/provider-agents/store"
)

// webhookBodyLimit is how much of a delivery's body is read, and therefore
// how much the matcher sees and the sink keeps. GitHub's largest payloads are
// well under it; anything past it is cut, and the delivery says so.
const webhookBodyLimit = 64 * 1024

// lastDeliveryCoalesce is how often status.lastReceived/lastDelivery are
// rewritten for a run of deliveries with the same outcome and reason. A busy
// organization webhook can deliver several times a second; a status write per
// POST would fight the reconcilers for the object for no information gain,
// since the sink has every delivery anyway.
const lastDeliveryCoalesce = time.Minute

// lastDeliveryStampTimeout bounds the status stamps a delivery leaves running
// after its sender was answered (see deliverWebhookEvent).
const lastDeliveryStampTimeout = 10 * time.Second

// redeliveryLookback is how many earlier deliveries carrying the same sender
// id are consulted before an inbound event is routed (see priorFiredRoutes).
// A sender retries a handful of times, not hundreds.
const redeliveryLookback = 20

// redactedHeaderValue stands in for a recorded header whose value looks like
// a credential. The header's presence is kept so a filter on it still shows
// up in the editor; its value is not.
const redactedHeaderValue = "[redacted]"

// unroutedReason is the rollup reason of a delivery no trigger routes from.
const unroutedReason = "no trigger routes from this webhook"

// droppedHeaderPrefixes and droppedHeaders are request headers the sink never
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

// recordedHeaders is the subset of a delivery's request headers the sink
// keeps, values redacted where the name says they are credentials.
// Multi-valued headers are joined the way a sender would see them echoed.
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

// webhookRetention is the sink cap for one webhook: its own
// spec.deliveryRetention when set (0 keeps nothing), else the operator's
// default, else DefaultTriggerDeliveryRetention; bounded above either way.
func (s *Server) webhookRetention(wh *agentsv1alpha1.Webhook) int {
	n := s.cfg.TriggerDeliveryRetention
	if n <= 0 {
		n = DefaultTriggerDeliveryRetention
	}
	if wh != nil && wh.Spec.DeliveryRetention != nil {
		n = int(*wh.Spec.DeliveryRetention)
	}
	if n > MaxTriggerDeliveryRetention {
		n = MaxTriggerDeliveryRetention
	}
	if n < 0 {
		n = 0
	}
	return n
}

// inboundDelivery is one event on its way through a webhook: the request as
// the URL received it, a recorded delivery being replayed, or a sample a
// caller spelled out.
type inboundDelivery struct {
	Headers http.Header
	Body    []byte
	// Truncated says the body hit webhookBodyLimit.
	Truncated bool
	// SenderID is the sender's own id for the event (X-GitHub-Delivery).
	SenderID string
	// ReplayOf names the recorded delivery this one re-feeds, when it is a
	// replay rather than a fresh POST.
	ReplayOf string
	// Inbound says the delivery arrived at the URL from the sender, as opposed
	// to a replay or a sample a caller spelled out. Only an inbound delivery
	// can be a redelivery of one already handled (see priorFiredRoutes).
	Inbound bool
}

// routedTrigger is one Trigger a delivery is offered to: the object as listed,
// for the status stamp, and its typed form.
type routedTrigger struct {
	u    *unstructured.Unstructured
	trig *agentsv1alpha1.Trigger
}

// webhookDeliveryResult is what became of a delivery, as the sender and the
// webhooks/run verb are told.
type webhookDeliveryResult struct {
	// ID is the sink row the delivery was recorded under; empty when the
	// webhook keeps no sink.
	ID string `json:"deliveryID,omitempty"`
	// Outcome is the rollup: fired, failed, unrouted or filtered.
	Outcome string `json:"status"`
	Reason  string `json:"reason,omitempty"`
	// Routes is what each routed trigger made of it.
	Routes []store.WebhookRoute `json:"routes"`
}

// deliveryResult is what one trigger made of a delivery replayed through it
// alone (triggers/{name}/run with a deliveryID).
type deliveryResult struct {
	// ID is the sink row the replay was recorded under.
	ID      string `json:"deliveryID,omitempty"`
	Outcome string `json:"status"`
	Reason  string `json:"reason,omitempty"`
	RunID   string `json:"runID,omitempty"`
}

// routeToTrigger runs one delivery through one trigger: the pause and disable
// switches, spec.filter, the task, and the run submission. It is the one path
// for a real POST and for every replay, so the sink never says something the
// webhook would not have done.
func (s *Server) routeToTrigger(ctx context.Context, cluster string, trig *agentsv1alpha1.Trigger, in inboundDelivery) store.WebhookRoute {
	route := store.WebhookRoute{Trigger: trig.Name, Outcome: agentsv1alpha1.TriggerDeliveryFired}
	switch {
	case trig.Spec.Suspend:
		route.Outcome, route.Reason = agentsv1alpha1.TriggerDeliverySuspended, "trigger is paused"
	case trig.Status.DisabledReason != "":
		route.Outcome, route.Reason = agentsv1alpha1.TriggerDeliveryDisabled, trig.Status.DisabledReason
	default:
		if reason, ok := triggerfilter.Allows(trig.Spec.Filter, in.Headers, in.Body); !ok {
			route.Outcome, route.Reason = agentsv1alpha1.TriggerDeliveryFiltered, reason
		}
	}
	if route.Outcome != agentsv1alpha1.TriggerDeliveryFired {
		return route
	}
	task := trig.Spec.Task
	if len(strings.TrimSpace(string(in.Body))) > 0 {
		// The payload is whatever the sender chose to POST. It is evidence for
		// the task, never part of it — quarantine it so an embedded "ignore
		// your instructions and ..." reads as data to the model.
		task += "\n\n" + quarantinePayload("webhook trigger "+trig.Name, map[string]string{
			"eventType":   triggerfilter.EventType(in.Headers, in.Body),
			"contentType": in.Headers.Get("Content-Type"),
		}, string(in.Body))
	}
	if strings.TrimSpace(task) == "" {
		route.Outcome, route.Reason = agentsv1alpha1.TriggerDeliveryNoTask, "trigger has no task"
		return route
	}
	// A replay is "run it again", not "continue": it gets a session of its
	// own rather than appending to the transcript of the run the original
	// delivery started. Keying on the sender's id is for the sender's own
	// redeliveries, which priorFiredRoutes already folds away.
	sessionKey := in.SenderID
	if in.ReplayOf != "" {
		sessionKey = ""
	}
	runID, err := s.bg.submitRun(ctx, executor.Job{
		ID:            fmt.Sprintf("%s/%s/%d", cluster, trig.Name, time.Now().UnixNano()),
		Kind:          executor.KindTrigger,
		ClusterID:     cluster,
		SourceName:    trig.Name,
		AgentRef:      trig.Spec.AgentRef,
		Task:          task,
		Trigger:       agentsv1alpha1.RunTriggerEvent,
		SessionID:     triggerSessionID(trig.Name, sessionKey),
		NotifyChannel: trig.Spec.ChannelRef,
	})
	if err != nil {
		route.Outcome, route.Reason = agentsv1alpha1.TriggerDeliveryFailed, err.Error()
	} else {
		route.RunID = runID
	}
	return route
}

// rollupReason is the one-line reason the sink row and the sender get for a
// delivery that did not fire: the failing route's error, "nobody routes from
// this webhook", or — for a delivery every trigger declined — the one route's
// reason when there is one route, else a count.
func rollupReason(outcome string, routes []store.WebhookRoute) string {
	switch outcome {
	case agentsv1alpha1.TriggerDeliveryFailed:
		for _, r := range routes {
			if r.Outcome == agentsv1alpha1.TriggerDeliveryFailed {
				return r.Reason
			}
		}
	case store.WebhookDeliveryUnrouted:
		return unroutedReason
	case agentsv1alpha1.TriggerDeliveryFiltered:
		if len(routes) == 1 {
			return routes[0].Reason
		}
		return fmt.Sprintf("none of %d triggers fired", len(routes))
	}
	return ""
}

// deliverWebhookEvent offers one delivery to every given trigger, records it
// once in the webhook's sink with every route, stamps status on the webhook
// and on each routed trigger, and tells the portal.
func (s *Server) deliverWebhookEvent(ctx context.Context, cluster string, dyn dynamic.Interface, whU *unstructured.Unstructured, wh *agentsv1alpha1.Webhook, triggers []routedTrigger, in inboundDelivery) webhookDeliveryResult {
	scope := s.bg.scopeFor(ctx, cluster, "")
	prior := s.priorFiredRoutes(ctx, scope, wh, in)
	routes := make([]store.WebhookRoute, 0, len(triggers))
	for _, rt := range triggers {
		if runID, done := prior[rt.trig.Name]; done {
			routes = append(routes, store.WebhookRoute{
				Trigger: rt.trig.Name, Outcome: agentsv1alpha1.TriggerDeliveryFired, RunID: runID,
				Reason: "already fired for this delivery; the run is the earlier one",
			})
			continue
		}
		routes = append(routes, s.routeToTrigger(ctx, cluster, rt.trig, in))
	}
	res := webhookDeliveryResult{Outcome: store.WebhookDeliveryRollup(routes), Routes: routes}
	res.Reason = rollupReason(res.Outcome, routes)

	now := time.Now().UTC()
	res.ID = s.recordWebhookDelivery(ctx, scope, wh, now, res.Outcome, res.Reason, routes, in)

	fired := int32(0)
	for _, r := range routes {
		if r.Outcome == agentsv1alpha1.TriggerDeliveryFired {
			fired++
		}
	}
	summary := map[string]any{"routed": int64(len(routes)), "fired": int64(fired)}
	// The status stamps are off the sender's request: up to one write per
	// routed trigger plus one on the webhook, each a kcp round trip, and
	// GitHub times a delivery out at ten seconds. The sender is answered as
	// soon as the sink has the delivery; the stamps finish on their own,
	// bounded, with the request's values but not its cancellation.
	stampCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), lastDeliveryStampTimeout)
	s.stamps.Add(1)
	go func() {
		defer s.stamps.Done()
		defer cancel()
		stampLastDelivery(stampCtx, dyn, agentsclient.WebhookGVR, whU.GetName(), now, res.Outcome, res.Reason, res.ID, summary)
		for i, rt := range triggers {
			stampLastDelivery(stampCtx, dyn, agentsclient.TriggerGVR, rt.u.GetName(), now, routes[i].Outcome, routes[i].Reason, res.ID, nil)
		}
	}()
	s.publishWebhookDelivery(scope, wh.Name, res, triggers)
	return res
}

// priorFiredRoutes answers "which of this webhook's triggers already fired
// for this very delivery?" — the sender's delivery id (X-GitHub-Delivery) is
// the key. A redelivery is the normal case with 1:n routing, not an edge: if
// trigger A fired and trigger B failed, the sender got 503 and sends the same
// event again, and A must not start a second run for it. Nothing is folded
// for a replay (the caller asked for it again), for a sample, or when the
// sender gave no id or the webhook keeps no sink to look in.
func (s *Server) priorFiredRoutes(ctx context.Context, scope store.Scope, wh *agentsv1alpha1.Webhook, in inboundDelivery) map[string]string {
	senderID := strings.TrimSpace(in.SenderID)
	if !in.Inbound || in.ReplayOf != "" || senderID == "" || s.webhookRetention(wh) <= 0 {
		return nil
	}
	rows, err := s.store.ListWebhookDeliveries(ctx, scope, string(wh.UID), store.WebhookDeliveryQuery{DeliveryID: senderID, Limit: redeliveryLookback})
	if err != nil {
		// Not knowing is not a reason to drop the event; worst case is the
		// duplicate run this lookup exists to prevent, said in the log.
		log.Printf("agents: looking up earlier deliveries %q of webhook %s: %v", senderID, wh.Name, err)
		return nil
	}
	fired := map[string]string{}
	for _, row := range rows {
		if row.ReplayOf != "" {
			continue // a replay is the user's doing, not the sender's
		}
		for _, r := range row.Routes {
			if r.Outcome == agentsv1alpha1.TriggerDeliveryFired && r.RunID != "" {
				if _, seen := fired[r.Trigger]; !seen {
					fired[r.Trigger] = r.RunID
				}
			}
		}
	}
	return fired
}

// recordWebhookDelivery writes the sink row and returns its id, or "" when the
// webhook keeps no sink or the write failed. The sink is an aid, not the
// event: a delivery that fired is not un-fired because its row failed, and one
// that was filtered is still acked. A failure is said in the provider log.
func (s *Server) recordWebhookDelivery(ctx context.Context, scope store.Scope, wh *agentsv1alpha1.Webhook, now time.Time, outcome, reason string, routes []store.WebhookRoute, in inboundDelivery) string {
	retain := s.webhookRetention(wh)
	if retain <= 0 {
		return ""
	}
	extracted := triggerfilter.Extract(in.Headers, in.Body)
	body := string(in.Body)
	if wh.Spec.RecordBodies != nil && !*wh.Spec.RecordBodies {
		body = ""
	}
	entry := store.WebhookDelivery{
		ID:            uuid.NewString(),
		WebhookName:   wh.Name,
		WebhookUID:    string(wh.UID),
		DeliveryID:    strings.TrimSpace(in.SenderID),
		ReceivedAt:    now,
		Outcome:       outcome,
		Reason:        reason,
		EventType:     extracted.EventType,
		Action:        extracted.Action,
		Repository:    extracted.Repository,
		ContentType:   in.Headers.Get("Content-Type"),
		Headers:       recordedHeaders(in.Headers),
		Body:          body,
		BodyTruncated: in.Truncated,
		ReplayOf:      in.ReplayOf,
		Routes:        routes,
	}
	if err := s.store.RecordWebhookDelivery(ctx, scope, entry, retain); err != nil {
		log.Printf("agents: recording delivery for webhook %s: %v", wh.Name, err)
		return ""
	}
	return entry.ID
}

// publishWebhookDelivery tells the portal a delivery landed. The event bus is
// workspace-wide but every stream is per agent and drops an event that names
// none (see streamEvents), so the event is published once per agent whose
// trigger was routed, carrying only that agent's routes: a stream must not
// learn a neighbouring agent's trigger names or runs. A delivery nobody routes
// from names no agent and reaches no stream; the sink still has it.
func (s *Server) publishWebhookDelivery(scope store.Scope, webhook string, res webhookDeliveryResult, triggers []routedTrigger) {
	byAgent := map[string][]map[string]any{}
	var agents []string
	for i, rt := range triggers {
		agent := rt.trig.Spec.AgentRef
		if _, seen := byAgent[agent]; !seen {
			agents = append(agents, agent)
		}
		r := res.Routes[i]
		byAgent[agent] = append(byAgent[agent], map[string]any{"trigger": r.Trigger, "outcome": r.Outcome, "runID": r.RunID})
	}
	for _, agent := range agents {
		s.events.publish(store.Scope{OrgUUID: scope.OrgUUID, WorkspaceUUID: scope.WorkspaceUUID}, "webhook-delivery", map[string]any{
			"webhook": webhook, "agent": agent, "id": res.ID, "outcome": res.Outcome, "routes": byAgent[agent],
		})
	}
}

// stampLastDelivery writes status.lastReceived and status.lastDelivery on a
// Webhook or a Trigger, coalesced: a delivery whose outcome and reason match
// the stored summary refreshes it at most once per lastDeliveryCoalesce.
// extra adds fields to the summary (the webhook's routed/fired counts).
//
// The object is read fresh and the two fields are merge-patched onto the
// status subresource, never a whole-status update from the copy the request
// was routed with: the reconcilers write the same subresource (routes, the
// path, Validated), and an update from a stale copy would either conflict
// and drop the stamp or, worse, win and revert theirs. A merge patch touches
// only these keys. Best-effort still — the sink has the delivery regardless.
func stampLastDelivery(ctx context.Context, dyn dynamic.Interface, gvr schema.GroupVersionResource, name string, now time.Time, outcome, reason, id string, extra map[string]any) {
	if dyn == nil || name == "" {
		return
	}
	u, err := dyn.Resource(gvr).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		log.Printf("agents: stamping lastDelivery on %s %s: %v", gvr.Resource, name, err)
		return
	}
	lastOutcome, _, _ := unstructured.NestedString(u.Object, "status", "lastDelivery", "outcome")
	lastReason, _, _ := unstructured.NestedString(u.Object, "status", "lastDelivery", "reason")
	lastReceived, _, _ := unstructured.NestedString(u.Object, "status", "lastReceived")
	if received, err := time.Parse(time.RFC3339, lastReceived); err == nil &&
		lastOutcome == outcome && lastReason == reason && now.Sub(received) < lastDeliveryCoalesce {
		return
	}
	stamp := now.Format(time.RFC3339)
	// Every summary key is spelled, absent ones as null: a JSON merge patch
	// merges objects key by key, so a reason left out would otherwise survive
	// from the previous summary.
	summary := map[string]any{"time": stamp, "outcome": outcome, "reason": nil, "deliveryID": nil, "routed": nil, "fired": nil}
	if reason != "" {
		summary["reason"] = reason
	}
	if id != "" {
		summary["deliveryID"] = id
	}
	for k, v := range extra {
		summary[k] = v
	}
	patch, err := json.Marshal(map[string]any{"status": map[string]any{"lastReceived": stamp, "lastDelivery": summary}})
	if err != nil {
		log.Printf("agents: stamping lastDelivery on %s %s: %v", gvr.Resource, name, err)
		return
	}
	if _, err := dyn.Resource(gvr).Patch(ctx, name, types.MergePatchType, patch, metav1.PatchOptions{}, "status"); err != nil {
		log.Printf("agents: stamping lastDelivery on %s %s: %v", gvr.Resource, name, err)
	}
}

// routedTriggers reads the Triggers a delivery is offered to: the ones the
// Webhook reconciler listed in status.routes (every Trigger whose
// spec.webhookRef names the webhook, kept current from a watch), fetched by
// name and sorted so routes are recorded in a stable order. Routing from the
// reconciled table rather than a LIST of every Trigger in the workspace keeps
// the cost of an inbound POST proportional to the webhook's own fan-out, not
// the tenant's trigger count — and the URL holder sets the POST rate. A route
// whose Trigger is gone since the table was written, or is on its way out, is
// skipped; the next reconcile drops it from the table.
func routedTriggers(ctx context.Context, dyn dynamic.Interface, wh *agentsv1alpha1.Webhook) ([]routedTrigger, error) {
	out := make([]routedTrigger, 0, len(wh.Status.Routes))
	for _, route := range wh.Status.Routes {
		name := strings.TrimSpace(route.Name)
		if name == "" {
			continue
		}
		u, err := dyn.Resource(agentsclient.TriggerGVR).Get(ctx, name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if u.GetDeletionTimestamp() != nil {
			continue
		}
		trig, err := fromU[agentsv1alpha1.Trigger](u)
		if err != nil || strings.TrimSpace(trig.Spec.WebhookRef) != wh.Name {
			continue
		}
		out = append(out, routedTrigger{u: u, trig: trig})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].trig.Name < out[j].trig.Name })
	return out, nil
}

// writeDeliveryAnswer answers a delivery the way a sender needs: 202 for
// anything it must not retry — fired, filtered, unrouted — and 503 when a
// route failed, because only then was something lost that a redelivery would
// recover. The body is the result either way, so the failing route's reason
// and the sink row are both in it.
func writeDeliveryAnswer(w http.ResponseWriter, res webhookDeliveryResult) {
	if res.Outcome == agentsv1alpha1.TriggerDeliveryFailed {
		writeJSON(w, http.StatusServiceUnavailable, res)
		return
	}
	writeJSON(w, http.StatusAccepted, res)
}

// ---- the inbound webhook ----------------------------------------------------

// webhookInbound feeds a Webhook from an external POST. The URL embeds an HMAC
// token (no tenant headers required): the hub forwards anonymous calls to the
// provider with identity headers stripped, so the token is the auth. Runs
// execute asynchronously.
func (s *Server) webhookInbound(w http.ResponseWriter, r *http.Request) {
	cluster, name, token := r.PathValue("cluster"), r.PathValue("name"), r.PathValue("token")
	expected := s.webhookToken(cluster, name)
	if expected == "" || s.bg == nil || !s.bg.readyFor(r.Context()) {
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
	whU, err := dyn.Resource(agentsclient.WebhookGVR).Get(r.Context(), name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		writeStatus(w, http.StatusNotFound, "NotFound", "webhook not found")
		return
	}
	if err != nil {
		writeStatus(w, http.StatusInternalServerError, "InternalError", err.Error())
		return
	}
	wh, err := fromU[agentsv1alpha1.Webhook](whU)
	if err != nil {
		writeStatus(w, http.StatusInternalServerError, "InternalError", "webhook object is not a Webhook: "+err.Error())
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, webhookBodyLimit+1))
	if err != nil {
		// A body cut short by the connection is not the event: judged by a
		// filter and recorded, it would be a false "filtered" the sender never
		// retries. Refuse it; nothing is recorded.
		writeStatus(w, http.StatusBadRequest, "BadRequest", "reading the request body: "+err.Error())
		return
	}
	truncated := len(body) > webhookBodyLimit
	if truncated {
		body = body[:webhookBodyLimit]
	}
	triggers, err := routedTriggers(r.Context(), dyn, wh)
	if err != nil {
		// Nothing was routed, so nothing is recorded; the sender redelivers.
		writeStatus(w, http.StatusServiceUnavailable, "Unavailable", "reading the webhook's triggers: "+err.Error())
		return
	}
	res := s.deliverWebhookEvent(r.Context(), cluster, dyn, whU, wh, triggers, inboundDelivery{
		Headers: r.Header, Body: body, Truncated: truncated, SenderID: r.Header.Get("X-GitHub-Delivery"), Inbound: true,
	})
	writeDeliveryAnswer(w, res)
}

// ---- the verbs --------------------------------------------------------------

// gatedObject is the object the data-plane gate read for this request, typed.
func gatedObject[T any](r *http.Request, uid func(*T) string) (*unstructured.Unstructured, *T, bool) {
	gate, ok := gateFrom(r.Context())
	if !ok || gate.object == nil {
		return nil, nil, false
	}
	obj, err := fromU[T](gate.object)
	if err != nil || uid(obj) == "" {
		return nil, nil, false
	}
	return gate.object, obj, true
}

// gatedTrigger is the Trigger the data-plane gate read for this request.
func gatedTrigger(r *http.Request) (*unstructured.Unstructured, *agentsv1alpha1.Trigger, bool) {
	return gatedObject(r, func(t *agentsv1alpha1.Trigger) string { return string(t.UID) })
}

// gatedWebhook is the Webhook the data-plane gate read for this request.
func gatedWebhook(r *http.Request) (*unstructured.Unstructured, *agentsv1alpha1.Webhook, bool) {
	return gatedObject(r, func(w *agentsv1alpha1.Webhook) string { return string(w.UID) })
}

// maxDeliveriesPage caps one deliveries listing.
const maxDeliveriesPage = 200

// listWebhookDeliveries serves GET webhooks/{name}/deliveries — the sink,
// newest first, bodies cut to a preview, narrowed by outcome or by routed
// trigger — and GET webhooks/{name}/deliveries/{id}, one delivery whole. The
// webhook's UID keys the sink, so a recreated webhook does not inherit its
// predecessor's.
func (s *Server) listWebhookDeliveries(w http.ResponseWriter, r *http.Request) {
	_, id, ok := s.requireClient(w, r)
	if !ok {
		return
	}
	_, wh, ok := gatedWebhook(r)
	if !ok {
		writeStatus(w, http.StatusInternalServerError, "InternalError", "the gate resolved no Webhook for this request")
		return
	}
	scope := id.scope("")
	if tail := strings.TrimSpace(r.PathValue("tail")); tail != "" {
		if strings.Contains(tail, "/") {
			writeStatus(w, http.StatusBadRequest, "BadRequest", "deliveries takes one delivery id")
			return
		}
		d, found, err := s.store.GetWebhookDelivery(r.Context(), scope, string(wh.UID), tail)
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
	q := store.WebhookDeliveryQuery{
		Outcome: strings.TrimSpace(r.URL.Query().Get("outcome")),
		Trigger: strings.TrimSpace(r.URL.Query().Get("trigger")),
		Limit:   50,
	}
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			writeStatus(w, http.StatusBadRequest, "BadRequest", "limit must be a positive integer")
			return
		}
		q.Limit = min(n, maxDeliveriesPage)
	}
	items, err := s.store.ListWebhookDeliveries(r.Context(), scope, string(wh.UID), q)
	if err != nil {
		writeStatus(w, http.StatusInternalServerError, "InternalError", err.Error())
		return
	}
	writeList(w, items, map[string]any{"retention": s.webhookRetention(wh)})
}

// webhookRunRequest is the body of webhooks/{name}/run: a recorded delivery
// to replay, or a sample spelled out.
type webhookRunRequest struct {
	DeliveryID string            `json:"deliveryID,omitempty"`
	Headers    map[string]string `json:"headers,omitempty"`
	// Body is the sample payload: a JSON string is taken as the raw body, any
	// other JSON value as its own encoding, so {"body": {...}} works too.
	Body json.RawMessage `json:"body,omitempty"`
}

// runWebhook serves POST webhooks/{name}/run: feed one delivery to every
// trigger routing from the webhook, exactly as a POST to its URL would be —
// recorded in the sink, stamped on status, answered 202/503. A replay points
// at the delivery it re-fed.
func (s *Server) runWebhook(w http.ResponseWriter, r *http.Request) {
	_, id, ok := s.requireClient(w, r)
	if !ok {
		return
	}
	whU, wh, ok := gatedWebhook(r)
	if !ok {
		writeStatus(w, http.StatusInternalServerError, "InternalError", "the gate resolved no Webhook for this request")
		return
	}
	var req webhookRunRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, webhookBodyLimit*2)).Decode(&req); err != nil {
		writeStatus(w, http.StatusBadRequest, "BadRequest", "body must be JSON: "+err.Error())
		return
	}
	req.DeliveryID = strings.TrimSpace(req.DeliveryID)
	sample := len(req.Headers) > 0 || len(req.Body) > 0
	switch {
	case req.DeliveryID != "" && sample:
		writeStatus(w, http.StatusBadRequest, "BadRequest", "give deliveryID or headers/body, not both")
		return
	case req.DeliveryID == "" && !sample:
		writeStatus(w, http.StatusBadRequest, "BadRequest", "name a recorded delivery (deliveryID) or spell one out (headers, body)")
		return
	}
	if s.bg == nil || !s.bg.readyFor(r.Context()) {
		writeStatus(w, http.StatusServiceUnavailable, "Unavailable", "background executor is not running on this provider")
		return
	}
	var in inboundDelivery
	if req.DeliveryID != "" {
		d, found, err := s.store.GetWebhookDelivery(r.Context(), id.scope(""), string(wh.UID), req.DeliveryID)
		if err != nil {
			writeStatus(w, http.StatusInternalServerError, "InternalError", err.Error())
			return
		}
		if !found {
			writeStatus(w, http.StatusNotFound, "NotFound", "no such delivery")
			return
		}
		in = inboundDelivery{Headers: headersFromRecorded(d.Headers), Body: []byte(d.Body), Truncated: d.BodyTruncated,
			SenderID: d.DeliveryID, ReplayOf: d.ID}
	} else {
		body := []byte(req.Body)
		var text string
		if json.Unmarshal(req.Body, &text) == nil {
			body = []byte(text)
		}
		truncated := len(body) > webhookBodyLimit
		if truncated {
			body = body[:webhookBodyLimit]
		}
		headers := headersFromRecorded(req.Headers)
		in = inboundDelivery{Headers: headers, Body: body, Truncated: truncated, SenderID: headers.Get("X-GitHub-Delivery")}
	}
	dyn, err := s.bg.scoped(r.Context(), id.clusterID)
	if err != nil {
		writeStatus(w, http.StatusInternalServerError, "InternalError", err.Error())
		return
	}
	triggers, err := routedTriggers(r.Context(), dyn, wh)
	if err != nil {
		writeStatus(w, http.StatusServiceUnavailable, "Unavailable", "listing the webhook's triggers: "+err.Error())
		return
	}
	writeDeliveryAnswer(w, s.deliverWebhookEvent(r.Context(), id.clusterID, dyn, whU, wh, triggers, in))
}

// evaluateTriggerFilterRequest is the body of triggers/{name}/evaluate: a
// candidate filter, and the delivery to judge it against — a recorded one by
// id, or a request spelled out.
type evaluateTriggerFilterRequest struct {
	// Filter is the candidate spec.filter. Nil means the trigger's own.
	Filter *map[string]string `json:"filter,omitempty"`
	// DeliveryID names a recorded delivery of the trigger's webhook.
	DeliveryID string `json:"deliveryID,omitempty"`
	// Request is a delivery spelled out, for a sample the sink does not hold.
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

// triggerWebhookDelivery reads one delivery from the sink of the trigger's
// webhook, answering 404 itself when the webhook or the delivery is gone.
func (s *Server) triggerWebhookDelivery(w http.ResponseWriter, r *http.Request, c *agentsclient.Client, id identity, trig *agentsv1alpha1.Trigger, deliveryID string) (store.WebhookDelivery, bool) {
	ref := strings.TrimSpace(trig.Spec.WebhookRef)
	if ref == "" {
		writeStatus(w, http.StatusNotFound, "NotFound", "the trigger names no webhook, so it has no deliveries")
		return store.WebhookDelivery{}, false
	}
	wh, err := c.Webhooks().Get(r.Context(), ref, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		writeStatus(w, http.StatusNotFound, "NotFound", "the trigger's webhook "+ref+" does not exist")
		return store.WebhookDelivery{}, false
	}
	if err != nil {
		writeStatus(w, http.StatusInternalServerError, "InternalError", err.Error())
		return store.WebhookDelivery{}, false
	}
	d, found, err := s.store.GetWebhookDelivery(r.Context(), id.scope(""), string(wh.UID), deliveryID)
	if err != nil {
		writeStatus(w, http.StatusInternalServerError, "InternalError", err.Error())
		return store.WebhookDelivery{}, false
	}
	if !found {
		writeStatus(w, http.StatusNotFound, "NotFound", "no such delivery")
		return store.WebhookDelivery{}, false
	}
	return d, true
}

// evaluateTriggerFilter serves POST triggers/{name}/evaluate. It changes
// nothing: it runs the matcher the webhook runs and reports per key, which
// is what lets the portal paint a filter green or red as it is typed.
func (s *Server) evaluateTriggerFilter(w http.ResponseWriter, r *http.Request) {
	c, id, ok := s.requireClient(w, r)
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
		d, ok := s.triggerWebhookDelivery(w, r, c, id, trig, strings.TrimSpace(req.DeliveryID))
		if !ok {
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

// replayTriggerDelivery re-feeds a recorded delivery of the trigger's webhook
// through THIS trigger alone, as it stands now — pause switch, filter, task —
// and records the result as a new sink row whose only route is this trigger,
// pointing at the delivery it replayed. It is the "I fixed the filter, run
// that one" button; the webhook's other triggers are not consulted.
func (s *Server) replayTriggerDelivery(w http.ResponseWriter, r *http.Request, c *agentsclient.Client, id identity, tu *unstructured.Unstructured, trig *agentsv1alpha1.Trigger, deliveryID string) {
	if s.bg == nil || !s.bg.readyFor(r.Context()) {
		writeStatus(w, http.StatusServiceUnavailable, "Unavailable", "background executor is not running on this provider")
		return
	}
	d, ok := s.triggerWebhookDelivery(w, r, c, id, trig, deliveryID)
	if !ok {
		return
	}
	dyn, err := s.bg.scoped(r.Context(), id.clusterID)
	if err != nil {
		writeStatus(w, http.StatusInternalServerError, "InternalError", err.Error())
		return
	}
	whU, err := dyn.Resource(agentsclient.WebhookGVR).Get(r.Context(), strings.TrimSpace(trig.Spec.WebhookRef), metav1.GetOptions{})
	if err != nil {
		writeResourceError(w, err)
		return
	}
	wh, err := fromU[agentsv1alpha1.Webhook](whU)
	if err != nil {
		writeStatus(w, http.StatusInternalServerError, "InternalError", "webhook object is not a Webhook: "+err.Error())
		return
	}
	res := s.deliverWebhookEvent(r.Context(), id.clusterID, dyn, whU, wh, []routedTrigger{{u: tu, trig: trig}}, inboundDelivery{
		Headers: headersFromRecorded(d.Headers), Body: []byte(d.Body), Truncated: d.BodyTruncated,
		SenderID: d.DeliveryID, ReplayOf: d.ID,
	})
	route := res.Routes[0]
	out := deliveryResult{ID: res.ID, Outcome: route.Outcome, Reason: route.Reason, RunID: route.RunID}
	switch route.Outcome {
	case agentsv1alpha1.TriggerDeliveryFired:
		writeJSON(w, http.StatusAccepted, out)
	case agentsv1alpha1.TriggerDeliveryFailed:
		writeJSON(w, http.StatusServiceUnavailable, out)
	default:
		// Not an error: the caller asked what the trigger would do with this
		// delivery, and the answer is "not fire", with the reason.
		writeJSON(w, http.StatusOK, out)
	}
}
