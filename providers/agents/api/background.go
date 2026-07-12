// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package api

// Background execution: the piece that makes schedules fire on their own
// clock and webhooks start runs without a user request.
//
// The provider's own service-account kubeconfig (from init) targets its kcp
// workspace. From there we read the agents APIExportEndpointSlice to discover
// the APIExport virtual-workspace URL, which serves every bound tenant
// workspace (wildcard /clusters/* for lists, /clusters/<id> for writes) plus
// the claimed core resources (Secrets — the model credentials). A small
// polling loop derives due schedules from CR state, claims each fire with an
// optimistic status update (conflict = another replica won), and submits a
// serializable Job to the executor (in-process pool today; swappable for a
// durable engine — see the executor package).

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/robfig/cron/v3"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	agentsv1alpha1 "github.com/faroshq/provider-agents/apis/v1alpha1"
	agentsclient "github.com/faroshq/provider-agents/client"
	"github.com/faroshq/provider-agents/channels"
	"github.com/faroshq/provider-agents/executor"
	"github.com/faroshq/provider-agents/llm"
	"github.com/faroshq/provider-agents/store"
)

var sliceGVR = schema.GroupVersionResource{Group: "apis.kcp.io", Version: "v1alpha1", Resource: "apiexportendpointslices"}

// maxConsecutiveFailures disables a schedule (with disabledReason) once
// back-to-back failures reach it.
const maxConsecutiveFailures = 5

// background owns the VW plumbing + scheduler policy.
type background struct {
	server *Server
	base   *rest.Config
	exec   executor.Executor

	vwURL    string
	wildcard dynamic.Interface

	interval time.Duration
	key      []byte // webhook HMAC key ("" → webhooks disabled)
}

// StartBackground wires and starts the background executor + scheduler loop.
// No-op (with a log line) when no provider kubeconfig is configured — the
// provider then serves per-request traffic only.
func (s *Server) StartBackground(ctx context.Context) {
	if s.cfg.ProviderKubeconfig == "" {
		log.Printf("background executor disabled (set KEDGE_PROVIDER_KUBECONFIG to enable autonomous schedules/webhooks)")
		return
	}
	base, err := clientcmd.BuildConfigFromFlags("", s.cfg.ProviderKubeconfig)
	if err != nil {
		log.Printf("background executor disabled: loading provider kubeconfig: %v", err)
		return
	}
	interval := 30 * time.Second
	if s.cfg.SchedulerInterval > 0 {
		interval = s.cfg.SchedulerInterval
	}
	bg := &background{server: s, base: base, interval: interval, key: s.webhookKeyBytes()}
	bg.exec = executor.NewInProcess(bg.handle, 4, 10*time.Minute)
	_ = bg.exec.Start(ctx)
	s.bg = bg
	go bg.loop(ctx)
	log.Printf("background executor started (interval %s)", interval)
}

// webhookKeyBytes resolves the webhook signing key: explicit env key, else
// derived from the provider kubeconfig contents (stable across restarts).
func (s *Server) webhookKeyBytes() []byte {
	if s.cfg.WebhookKey != "" {
		return []byte(s.cfg.WebhookKey)
	}
	if s.cfg.ProviderKubeconfig != "" {
		if b, err := os.ReadFile(s.cfg.ProviderKubeconfig); err == nil {
			sum := sha256.Sum256(append(b, []byte("kedge-agents-webhook")...))
			return sum[:]
		}
	}
	return nil
}

// webhookToken returns the HMAC token guarding a trigger's inbound URL.
func (s *Server) webhookToken(clusterID, name string) string {
	key := s.webhookKeyBytes()
	if len(key) == 0 {
		return ""
	}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(clusterID + "/" + name))
	return hex.EncodeToString(mac.Sum(nil))[:32]
}

// ---- virtual-workspace plumbing --------------------------------------------

// ensureVW discovers (and re-discovers) the APIExport VW URL from the
// endpoint slice in the provider workspace.
func (b *background) ensureVW(ctx context.Context) error {
	dyn, err := dynamic.NewForConfig(b.base)
	if err != nil {
		return err
	}
	u, err := dyn.Resource(sliceGVR).Get(ctx, apiExportNameForSlice, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("reading APIExportEndpointSlice %q: %w", apiExportNameForSlice, err)
	}
	endpoints, _, _ := unstructured.NestedSlice(u.Object, "status", "endpoints")
	if len(endpoints) == 0 {
		return fmt.Errorf("endpoint slice %q has no endpoints yet", apiExportNameForSlice)
	}
	first, _ := endpoints[0].(map[string]any)
	url, _ := first["url"].(string)
	if url == "" {
		return fmt.Errorf("endpoint slice %q endpoint has no url", apiExportNameForSlice)
	}
	if url == b.vwURL && b.wildcard != nil {
		return nil
	}
	wc := rest.CopyConfig(b.base)
	wc.Host = strings.TrimRight(url, "/") + "/clusters/*"
	wildcard, err := dynamic.NewForConfig(wc)
	if err != nil {
		return err
	}
	b.vwURL = strings.TrimRight(url, "/")
	b.wildcard = wildcard
	log.Printf("background: using APIExport virtual workspace %s", b.vwURL)
	return nil
}

// apiExportNameForSlice is the slice name (same as the export by convention).
const apiExportNameForSlice = "agents.kedge.faros.sh"

// scoped returns a dynamic client bound to one tenant logical cluster.
func (b *background) scoped(clusterID string) (dynamic.Interface, error) {
	c := rest.CopyConfig(b.base)
	c.Host = b.vwURL + "/clusters/" + clusterID
	return dynamic.NewForConfig(c)
}

// vwSecrets adapts a scoped dynamic client to llm.SecretGetter so background
// runs read model credentials through the APIExport claim.
type vwSecrets struct{ dyn dynamic.Interface }

func (v vwSecrets) GetSecret(ctx context.Context, namespace, name string) (*corev1.Secret, error) {
	u, err := v.dyn.Resource(agentsclient.SecretGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	return fromU[corev1.Secret](u)
}

func fromU[T any](u *unstructured.Unstructured) (*T, error) {
	var out T
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ---- scheduler policy -------------------------------------------------------

func (b *background) loop(ctx context.Context) {
	t := time.NewTicker(b.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := b.ensureVW(ctx); err != nil {
				log.Printf("background: virtual workspace not ready: %v", err)
				continue
			}
			b.tick(ctx)
		}
	}
}

func (b *background) tick(ctx context.Context) {
	list, err := b.wildcard.Resource(agentsclient.AgentScheduleGVR).List(ctx, metav1.ListOptions{})
	if err != nil {
		log.Printf("background: listing schedules: %v", err)
		return
	}
	now := time.Now().UTC()
	for i := range list.Items {
		item := &list.Items[i]
		if err := b.process(ctx, item, now); err != nil {
			log.Printf("background: schedule %s/%s: %v", item.GetAnnotations()["kcp.io/cluster"], item.GetName(), err)
		}
	}
}

func (b *background) process(ctx context.Context, u *unstructured.Unstructured, now time.Time) error {
	clusterID := u.GetAnnotations()["kcp.io/cluster"]
	if clusterID == "" {
		return fmt.Errorf("no kcp.io/cluster annotation")
	}
	sched, err := fromU[agentsv1alpha1.AgentSchedule](u)
	if err != nil {
		return err
	}
	if sched.Spec.Suspend || sched.Status.DisabledReason != "" {
		return nil
	}

	fire, next, permErr := scheduleDue(sched, now)
	if permErr != nil {
		return b.updateStatus(ctx, clusterID, u, map[string]any{"disabledReason": permErr.Error()})
	}
	if !fire {
		// Initialize nextRun on first sight so the UI shows it.
		if sched.Status.NextRun == nil && !next.IsZero() {
			return b.updateStatus(ctx, clusterID, u, map[string]any{"nextRun": next.Format(time.RFC3339)})
		}
		return nil
	}

	// Claim: advance lastRun/nextRun with the listed resourceVersion. A
	// conflict means another replica claimed this fire — skip silently.
	claim := map[string]any{"lastRun": now.Format(time.RFC3339)}
	if !next.IsZero() {
		claim["nextRun"] = next.Format(time.RFC3339)
	}
	if err := b.updateStatus(ctx, clusterID, u, claim); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "conflict") {
			return nil
		}
		return fmt.Errorf("claiming: %w", err)
	}

	trigger := agentsv1alpha1.RunTriggerSchedule
	task := sched.Spec.Task
	switch sched.Spec.Type {
	case agentsv1alpha1.ScheduleTypeHeartbeat:
		trigger = agentsv1alpha1.RunTriggerHeartbeat
		task = heartbeatPrompt(sched.Spec.Checklist)
	case agentsv1alpha1.ScheduleTypeWakeup:
		trigger = agentsv1alpha1.RunTriggerWakeup
	}
	if strings.TrimSpace(task) == "" {
		return b.updateStatus(ctx, clusterID, u, map[string]any{"disabledReason": "schedule has no task/checklist"})
	}

	return b.exec.Submit(ctx, executor.Job{
		ID:         fmt.Sprintf("%s/%s/%d", clusterID, sched.Name, now.Unix()),
		Kind:       executor.KindSchedule,
		ClusterID:  clusterID,
		SourceName: sched.Name,
		AgentRef:   sched.Spec.AgentRef,
		Task:       task,
		Trigger:    trigger,
		SessionID:  "schedule:" + sched.Name,
	})
}

// heartbeatPrompt wraps a standing checklist so quiet heartbeats stay quiet.
func heartbeatPrompt(checklist string) string {
	return "Review this standing checklist. If nothing needs attention, reply with exactly OK and nothing else. " +
		"If something is actionable, report it concisely:\n\n" + checklist
}

// scheduleDue decides whether a schedule fires now and what its next fire time
// is. permErr marks unrecoverable spec problems (bad cron, wakeup w/o runAt).
func scheduleDue(sched *agentsv1alpha1.AgentSchedule, now time.Time) (fire bool, next time.Time, permErr error) {
	switch sched.Spec.Type {
	case agentsv1alpha1.ScheduleTypeCron, agentsv1alpha1.ScheduleTypeHeartbeat:
		loc := time.UTC
		if tz := strings.TrimSpace(sched.Spec.TimeZone); tz != "" {
			l, err := time.LoadLocation(tz)
			if err != nil {
				return false, time.Time{}, fmt.Errorf("invalid timeZone %q: %v", tz, err)
			}
			loc = l
		}
		expr, err := cron.ParseStandard(sched.Spec.Schedule)
		if err != nil {
			return false, time.Time{}, fmt.Errorf("invalid cron %q: %v", sched.Spec.Schedule, err)
		}
		nextFromNow := expr.Next(now.In(loc)).UTC()
		if sched.Status.NextRun == nil {
			return false, nextFromNow, nil
		}
		if !now.Before(sched.Status.NextRun.Time) {
			return true, nextFromNow, nil
		}
		return false, time.Time{}, nil
	case agentsv1alpha1.ScheduleTypeWakeup:
		if sched.Status.LastRun != nil {
			return false, time.Time{}, nil // one-shot already fired
		}
		if sched.Spec.RunAt == nil {
			return false, time.Time{}, fmt.Errorf("wakeup schedule has no runAt")
		}
		if !now.Before(sched.Spec.RunAt.Time) {
			return true, time.Time{}, nil
		}
		return false, sched.Spec.RunAt.Time.UTC(), nil
	default:
		return false, time.Time{}, fmt.Errorf("unknown schedule type %q", sched.Spec.Type)
	}
}

// updateStatus merges fields into .status and PUTs the status subresource in
// the object's cluster, using the object's resourceVersion (optimistic claim).
func (b *background) updateStatus(ctx context.Context, clusterID string, u *unstructured.Unstructured, fields map[string]any) error {
	dyn, err := b.scoped(clusterID)
	if err != nil {
		return err
	}
	obj := u.DeepCopy()
	status, _, _ := unstructured.NestedMap(obj.Object, "status")
	if status == nil {
		status = map[string]any{}
	}
	for k, v := range fields {
		status[k] = v
	}
	if err := unstructured.SetNestedMap(obj.Object, status, "status"); err != nil {
		return err
	}
	_, err = dyn.Resource(agentsclient.AgentScheduleGVR).UpdateStatus(ctx, obj, metav1.UpdateOptions{})
	return err
}

// ---- job handler ------------------------------------------------------------

// handle executes one background job: load the agent via the VW, run the task
// through the shared executeTask path, update source status, notify.
func (b *background) handle(ctx context.Context, job executor.Job) error {
	dyn, err := b.scoped(job.ClusterID)
	if err != nil {
		return err
	}
	au, err := dyn.Resource(agentsclient.AgentGVR).Get(ctx, job.AgentRef, metav1.GetOptions{})
	if err != nil {
		b.recordOutcome(ctx, job, "", fmt.Errorf("agent %q: %w", job.AgentRef, err))
		return fmt.Errorf("agent %q: %w", job.AgentRef, err)
	}
	agent, err := fromU[agentsv1alpha1.Agent](au)
	if err != nil {
		return err
	}

	scope := b.scopeFor(ctx, job.ClusterID, agent.Name)
	res, runErr := b.server.executeTask(ctx, vwSecrets{dyn}, scope, agent, job.SessionID, job.Task, job.Trigger, job.SourceName, nil)

	b.recordOutcome(ctx, job, res.RunID, runErr)

	// Notify: schedule/wakeup output always; heartbeat only when actionable.
	if runErr != nil {
		b.notify(ctx, dyn, agent, fmt.Sprintf("⚠️ %s %q failed: %v", job.Kind, job.SourceName, runErr))
		return runErr
	}
	out := strings.TrimSpace(res.Content)
	if job.Trigger == agentsv1alpha1.RunTriggerHeartbeat && (out == "" || strings.EqualFold(out, "OK") || strings.EqualFold(out, "OK.")) {
		return nil
	}
	if out != "" {
		b.notify(ctx, dyn, agent, fmt.Sprintf("[%s] %s", job.SourceName, truncate(out, 3500)))
	}
	return nil
}

// scopeFor resolves the store scope for a cluster via the recorded tenant
// mapping; unmapped clusters still run, under a cluster-keyed fallback scope.
func (b *background) scopeFor(ctx context.Context, clusterID, agentName string) store.Scope {
	if ref, ok, _ := b.server.store.GetTenantRef(ctx, clusterID); ok {
		return store.Scope{OrgUUID: ref.OrgUUID, WorkspaceUUID: ref.WorkspaceUUID, AgentName: agentName}
	}
	log.Printf("background: no tenant mapping for cluster %s yet — run recorded under fallback scope (open the agents UI once to map it)", clusterID)
	return store.Scope{OrgUUID: "unmapped", WorkspaceUUID: clusterID, AgentName: agentName}
}

// recordOutcome updates the firing schedule's status counters (lastRunID,
// consecutiveFailures, disable-after-N). Triggers record lastFired instead.
func (b *background) recordOutcome(ctx context.Context, job executor.Job, runID string, runErr error) {
	dyn, err := b.scoped(job.ClusterID)
	if err != nil {
		return
	}
	gvr := agentsclient.AgentScheduleGVR
	if job.Kind == executor.KindTrigger {
		gvr = agentsclient.AgentTriggerGVR
	}
	u, err := dyn.Resource(gvr).Get(ctx, job.SourceName, metav1.GetOptions{})
	if err != nil {
		return
	}
	status, _, _ := unstructured.NestedMap(u.Object, "status")
	if status == nil {
		status = map[string]any{}
	}
	if job.Kind == executor.KindTrigger {
		status["lastFired"] = time.Now().UTC().Format(time.RFC3339)
	}
	if runID != "" {
		status["lastRunID"] = runID
	}
	failures := int64(0)
	if f, ok := status["consecutiveFailures"].(int64); ok {
		failures = f
	}
	if runErr != nil {
		failures++
		status["consecutiveFailures"] = failures
		if failures >= maxConsecutiveFailures {
			status["disabledReason"] = fmt.Sprintf("disabled after %d consecutive failures: %v", failures, runErr)
		}
	} else {
		status["consecutiveFailures"] = int64(0)
	}
	_ = unstructured.SetNestedMap(u.Object, status, "status")
	_, _ = dyn.Resource(gvr).UpdateStatus(ctx, u, metav1.UpdateOptions{})
}

// notify delivers text to the agent's default notify connection, if set.
func (b *background) notify(ctx context.Context, dyn dynamic.Interface, agent *agentsv1alpha1.Agent, text string) {
	connName := strings.TrimSpace(agent.Spec.DefaultNotifyConnection)
	if connName == "" {
		return
	}
	cu, err := dyn.Resource(agentsclient.ConnectionGVR).Get(ctx, connName, metav1.GetOptions{})
	if err != nil {
		log.Printf("background: notify connection %q: %v", connName, err)
		return
	}
	conn, err := fromU[agentsv1alpha1.Connection](cu)
	if err != nil {
		return
	}
	token := ""
	if sec, err := (vwSecrets{dyn}).GetSecret(ctx, llm.SecretNamespace, connectionSecretName(connName)); err == nil {
		if v, ok := sec.Data["token"]; ok {
			token = string(v)
		}
	}
	if err := channels.Send(ctx, channels.Message{
		Type: conn.Spec.Type, Token: token, Target: conn.Spec.Channel, Config: conn.Spec.Config, Text: text,
	}); err != nil {
		log.Printf("background: notify via %q failed: %v", connName, err)
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// ---- inbound webhooks --------------------------------------------------------

// webhookTrigger fires an event trigger from an external POST. The URL embeds
// an HMAC token (no tenant headers required): the hub forwards anonymous
// calls to the provider with identity headers stripped, so the token is the
// auth. Responds 202 and executes asynchronously.
func (s *Server) webhookTrigger(w http.ResponseWriter, r *http.Request) {
	cluster, name, token := r.PathValue("cluster"), r.PathValue("name"), r.PathValue("token")
	expected := s.webhookToken(cluster, name)
	if expected == "" || s.bg == nil || s.bg.wildcard == nil {
		writeStatus(w, http.StatusServiceUnavailable, "Unavailable", "background executor is not running on this provider")
		return
	}
	if !hmac.Equal([]byte(expected), []byte(token)) {
		writeStatus(w, http.StatusForbidden, "Forbidden", "invalid webhook token")
		return
	}
	dyn, err := s.bg.scoped(cluster)
	if err != nil {
		writeStatus(w, http.StatusInternalServerError, "InternalError", err.Error())
		return
	}
	tu, err := dyn.Resource(agentsclient.AgentTriggerGVR).Get(r.Context(), name, metav1.GetOptions{})
	if err != nil {
		writeStatus(w, http.StatusNotFound, "NotFound", "trigger not found")
		return
	}
	trig, err := fromU[agentsv1alpha1.AgentTrigger](tu)
	if err != nil || trig.Spec.Suspend || trig.Status.DisabledReason != "" {
		writeStatus(w, http.StatusConflict, "Suspended", "trigger is suspended or disabled")
		return
	}
	body, _ := io.ReadAll(io.LimitReader(r.Body, 64*1024))
	task := trig.Spec.Task
	if len(strings.TrimSpace(string(body))) > 0 {
		task += "\n\nEvent payload:\n" + string(body)
	}
	if strings.TrimSpace(task) == "" {
		writeStatus(w, http.StatusBadRequest, "BadRequest", "trigger has no task")
		return
	}
	if err := s.bg.exec.Submit(r.Context(), executor.Job{
		ID:         fmt.Sprintf("%s/%s/%d", cluster, name, time.Now().UnixNano()),
		Kind:       executor.KindTrigger,
		ClusterID:  cluster,
		SourceName: name,
		AgentRef:   trig.Spec.AgentRef,
		Task:       task,
		Trigger:    agentsv1alpha1.RunTriggerEvent,
		SessionID:  "trigger:" + name,
	}); err != nil {
		writeStatus(w, http.StatusServiceUnavailable, "Unavailable", err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "accepted"})
}
