// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package api

// Background execution: the tenant access and job plumbing behind everything
// the provider does without a user request — schedule fires, trigger
// webhooks, channel messages, Discord gateway bots, run recovery.
//
// The provider's own service-account kubeconfig (from init) targets its kcp
// workspace. From there we read the agents APIExportEndpointSlice to discover
// the APIExport virtual-workspace URLs, which serve every bound tenant
// workspace (/clusters/<id> for per-tenant reads and writes) plus the claimed
// core resources (Secrets — the model credentials). The inbound HTTP paths
// (trigger webhooks, channel webhooks, OAuth callbacks, service-to-service
// invoke) address a tenant workspace through these; the executor handler runs
// each job through the same clients.
//
// What DECIDES to act — which schedule is due, which connection needs its
// webhook secret repaired or its OAuth token refreshed, which Discord bot
// should be online, which agent lacks a phase — is not here any more. Those
// are the multicluster-runtime reconcilers under controller/, driven by
// watches on the CRs rather than by a timer over every tenant; see
// controller_manager.go. What remains on a slow tick is only what has no
// watch to hang off: re-reading the endpoint slice (a shard added to the
// platform, an endpoint URL that changed) and the run recovery sweep, which
// walks Postgres rows, not CRs.
//
// The slice advertises ONE ENDPOINT PER KCP SHARD, and each endpoint only
// serves the tenant workspaces bound on that shard. Addressing a single URL
// therefore makes every tenant on the other shards unreachable — silently,
// since the wrong shard answers a cluster-scoped read with an error the HTTP
// path would report as "not found". So we hold every shard's URL, remember
// which shard serves each tenant cluster, and probe the shards for a cluster
// we have not seen.

import (
	"context"
	"crypto/hmac"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	agentsv1alpha1 "github.com/railgrid/provider-agents/apis/v1alpha1"
	"github.com/railgrid/provider-sdk/identityclient"

	"github.com/railgrid/provider-agents/channels"
	agentsclient "github.com/railgrid/provider-agents/client"
	"github.com/railgrid/provider-agents/executor"
	"github.com/railgrid/provider-agents/internal/webhookpath"
	"github.com/railgrid/provider-agents/llm"
	"github.com/railgrid/provider-agents/store"
)

var sliceGVR = schema.GroupVersionResource{Group: "apis.kcp.io", Version: "v1alpha1", Resource: "apiexportendpointslices"}

// maxConsecutiveFailures disables a schedule (with disabledReason) once
// back-to-back failures reach it.
const maxConsecutiveFailures = 5

// vwShard is one APIExport virtual-workspace endpoint — one kcp shard — and
// the wildcard client bound to it.
type vwShard struct {
	url      string
	wildcard dynamic.Interface
}

// background owns the VW plumbing + scheduler policy.
type background struct {
	server *Server
	base   *rest.Config
	exec   executor.Executor

	// mu guards shards + clusterShard: the discovery tick rebuilds them while
	// HTTP handlers (webhooks, OAuth callbacks) and Discord gateway callbacks
	// read them.
	mu     sync.RWMutex
	shards []*vwShard
	// clusterShard maps a tenant logical cluster to the URL of the shard that
	// serves it, learned from wildcard lists and endpoint probes.
	clusterShard map[string]string

	key []byte // webhook HMAC key ("" → webhooks disabled)

	// identities holds one hub-minted, TTL'd identity per (cluster, agent).
	// See agentidentity.go.
	identities *agentIdentities

	discord *DiscordGateway // Discord gateway bots (inbound chat)

	// seen de-duplicates inbound channel deliveries (Slack event_id, Telegram
	// update_id, Discord message id) so a platform retry does not run twice.
	seen *inboundDedup

	// scopedFn, when set, replaces scoped() — tests inject a fake dynamic
	// client per cluster instead of dialling a virtual workspace.
	scopedFn func(ctx context.Context, clusterID string) (dynamic.Interface, error)
}

// StartBackground wires and starts the background executor and the slow
// discovery/recovery tick. The reconcilers that decide what to run are started
// separately by the controller manager (see ControllerDeps). No-op (with a log
// line) when no provider kubeconfig is configured — the provider then serves
// per-request traffic only.
func (s *Server) StartBackground(ctx context.Context) {
	if s.cfg.ProviderKubeconfig == "" {
		log.Printf("background executor disabled (set RAILGRID_PROVIDER_KUBECONFIG to enable autonomous schedules/webhooks)")
		return
	}
	base, err := clientcmd.BuildConfigFromFlags("", s.cfg.ProviderKubeconfig)
	if err != nil {
		log.Printf("background executor disabled: loading provider kubeconfig: %v", err)
		return
	}
	// The hub identity service mints each agent's scoped, TTL'd credential.
	// Without it (no hub URL, no provider token) unattended runs still work —
	// they simply have no identity, so instance-backed tools report that per
	// tool instead of the run failing.
	var identities *agentIdentities
	if client, ierr := identityclient.New(identityclient.Options{
		HubURL:   s.cfg.HubURL,
		Provider: providerName,
		Insecure: &s.cfg.HubInsecure,
	}); ierr != nil {
		log.Printf("background executor: agent identities unavailable (%v); instance-backed tools are disabled in unattended runs", ierr)
	} else {
		identities = newAgentIdentities(client)
	}

	bg := &background{server: s, base: base, key: s.webhookKeyBytes(), identities: identities,
		seen: newInboundDedup(inboundDedupTTL, inboundDedupMax)}
	bg.exec = executor.NewInProcess(bg.handle, 4, 10*time.Minute)
	bg.discord = newDiscordGateway(bg)
	_ = bg.exec.Start(ctx)
	s.bg = bg
	go bg.run(ctx)
	log.Printf("background executor started")
}

// webhookKeyBytes and webhookToken bind this Server's configuration to the
// shared derivation in internal/webhookpath. The derivation itself lives there
// because the Trigger reconciler mints the same URLs for writers that never
// reach this layer, and the URL IS the credential: two copies that disagree by
// a byte mint two different URLs for one trigger, and the one already pasted
// into GitHub silently stops working. One implementation, one set of golden
// vectors.
//
// These stay as methods rather than being inlined at the ~6 call sites because
// the key resolution reads a file, and routing every caller through the Server
// keeps that off the request path's mind.
func (s *Server) webhookKeyBytes() []byte {
	return webhookpath.Key(s.cfg.WebhookKey, s.cfg.ProviderKubeconfig)
}

// webhookToken returns the HMAC token guarding a trigger's inbound URL.
func (s *Server) webhookToken(clusterID, name string) string {
	return webhookpath.Token(s.webhookKeyBytes(), clusterID, name)
}

// ---- virtual-workspace plumbing --------------------------------------------

// ensureVW discovers (and re-discovers) the APIExport VW endpoints from the
// endpoint slice in the provider workspace — one per kcp shard. Existing shard
// clients are preserved across calls; only new URLs build a client.
func (b *background) ensureVW(ctx context.Context) error {
	if b == nil || b.base == nil {
		// No provider kubeconfig: there is nothing to discover and nothing to
		// discover it with. Reported rather than panicked because shardFor now
		// calls this on demand, from request paths.
		return fmt.Errorf("no provider kubeconfig: the APIExport virtual workspace cannot be discovered")
	}
	dyn, err := dynamic.NewForConfig(b.base)
	if err != nil {
		return err
	}
	u, err := dyn.Resource(sliceGVR).Get(ctx, apiExportNameForSlice, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("reading APIExportEndpointSlice %q: %w", apiExportNameForSlice, err)
	}
	urls, err := sliceEndpointURLs(u)
	if err != nil {
		return err
	}
	seen := make(map[string]bool, len(urls))
	for _, url := range urls {
		seen[url] = true
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	existing := make(map[string]*vwShard, len(b.shards))
	for _, s := range b.shards {
		existing[s.url] = s
	}
	shards := make([]*vwShard, 0, len(urls))
	var added []string
	for _, url := range urls {
		if s, ok := existing[url]; ok {
			shards = append(shards, s)
			continue
		}
		wc := rest.CopyConfig(b.base)
		wc.Host = url + "/clusters/*"
		wildcard, err := dynamic.NewForConfig(wc)
		if err != nil {
			return err
		}
		shards = append(shards, &vwShard{url: url, wildcard: wildcard})
		added = append(added, url)
	}
	if len(added) > 0 || len(shards) != len(b.shards) {
		log.Printf("background: using %d APIExport virtual workspace endpoint(s): %s", len(shards), strings.Join(urls, ", "))
	}
	b.shards = shards
	// Forget cluster→shard bindings whose shard is gone, so they re-resolve
	// against the current endpoint set instead of pinning a dead URL.
	for cluster, url := range b.clusterShard {
		if !seen[url] {
			delete(b.clusterShard, cluster)
		}
	}
	return nil
}

// sliceEndpointURLs reads EVERY endpoint URL off an APIExportEndpointSlice,
// trimmed and de-duplicated in slice order. kcp publishes one endpoint per
// shard, so taking only the first silently drops every tenant workspace bound
// on the other shards.
func sliceEndpointURLs(u *unstructured.Unstructured) ([]string, error) {
	endpoints, _, _ := unstructured.NestedSlice(u.Object, "status", "endpoints")
	if len(endpoints) == 0 {
		return nil, fmt.Errorf("endpoint slice %q has no endpoints yet", apiExportNameForSlice)
	}
	urls := make([]string, 0, len(endpoints))
	seen := map[string]bool{}
	for _, e := range endpoints {
		m, _ := e.(map[string]any)
		raw, _ := m["url"].(string)
		url := strings.TrimRight(strings.TrimSpace(raw), "/")
		if url == "" || seen[url] {
			continue
		}
		seen[url] = true
		urls = append(urls, url)
	}
	if len(urls) == 0 {
		return nil, fmt.Errorf("endpoint slice %q has no endpoint with a url", apiExportNameForSlice)
	}
	return urls, nil
}

// ready reports whether at least one VW endpoint has been discovered.
func (b *background) ready() bool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.shards) > 0
}

// snapshotShards returns the current shard set for lock-free iteration.
func (b *background) snapshotShards() []*vwShard {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return append([]*vwShard(nil), b.shards...)
}

// rememberCluster records which shard serves a tenant cluster.
func (b *background) rememberCluster(clusterID, shardURL string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.clusterShard == nil {
		b.clusterShard = map[string]string{}
	}
	b.clusterShard[clusterID] = shardURL
}

// shardFor resolves which shard's VW serves a tenant cluster. It answers from
// cache once any list has seen the cluster; otherwise it probes each endpoint,
// because the inbound paths (trigger webhooks, channel webhooks, OAuth
// callbacks) can name a cluster before a list has surfaced it.
func (b *background) shardFor(ctx context.Context, clusterID string) (string, error) {
	b.mu.RLock()
	url, cached := b.clusterShard[clusterID]
	b.mu.RUnlock()
	if cached {
		return url, nil
	}
	shards := b.snapshotShards()
	if len(shards) == 0 {
		// Nothing discovered yet, or everything went away. Re-read the slice
		// here rather than on a timer: this is the moment the answer is
		// actually needed, and failing without trying would make the provider
		// wait out an interval for no reason.
		if err := b.ensureVW(ctx); err != nil {
			return "", fmt.Errorf("no APIExport virtual workspace endpoint: %w", err)
		}
		shards = b.snapshotShards()
	}
	switch len(shards) {
	case 0:
		return "", fmt.Errorf("no APIExport virtual workspace endpoint discovered yet")
	case 1:
		return shards[0].url, nil
	}
	for _, s := range shards {
		c := rest.CopyConfig(b.base)
		c.Host = s.url + "/clusters/" + clusterID
		dyn, err := dynamic.NewForConfig(c)
		if err != nil {
			continue
		}
		// A shard that does not host this logical cluster rejects the read;
		// the one that does answers (an empty list is still an answer).
		if _, err := dyn.Resource(agentsclient.AgentGVR).List(ctx, metav1.ListOptions{Limit: 1}); err != nil {
			continue
		}
		b.rememberCluster(clusterID, s.url)
		return s.url, nil
	}
	return "", fmt.Errorf("tenant workspace %q is not served by any of the %d APIExport virtual workspace endpoints", clusterID, len(shards))
}

// agentToken returns the agent's hub-minted identity token, scoped to exactly
// the Instances this agent references. Returns "" on failure — the caller
// degrades to a run without instance-backed tools, which reports a clear
// message per tool, rather than failing the whole run.
func (b *background) agentToken(ctx context.Context, dyn dynamic.Interface, cluster, agentName string) string {
	if b.identities == nil {
		return ""
	}
	u, err := dyn.Resource(agentsclient.AgentGVR).Get(ctx, agentName, metav1.GetOptions{})
	if err != nil {
		log.Printf("background: agent %q identity unavailable, instance-backed tools disabled for this run: %v", agentName, err)
		return ""
	}
	agent, err := fromU[agentsv1alpha1.Agent](u)
	if err != nil {
		log.Printf("background: agent %q identity unavailable: %v", agentName, err)
		return ""
	}
	// The UID is read here, from the object the hub will verify against: an
	// identity is owned by a specific Agent, so an agent deleted and recreated
	// under the same name does not inherit the old one's access.
	return b.identities.token(ctx, dyn, cluster, agent)
}

// ReleaseAgentIdentity revokes an agent's hub-minted identity. The Agent
// reconciler's purge finalizer calls it alongside the store teardown: both are
// "this agent is gone, take its things away", and leaving the identity for the
// hub's own sweep would keep a usable token alive for up to a full TTL after
// the agent stopped existing.
func (b *background) ReleaseAgentIdentity(ctx context.Context, clusterID, agentName string) error {
	return b.identities.release(ctx, clusterID, agentName)
}

// apiExportNameForSlice is the slice name (same as the export by convention).
const apiExportNameForSlice = "agents.railgrid.ai"

// providerName is how this provider's CatalogEntry registers it, and therefore
// how the hub identity service knows who is asking.
const providerName = "agents"

// scoped returns a dynamic client bound to one tenant logical cluster, on
// whichever shard's VW actually serves it.
func (b *background) scoped(ctx context.Context, clusterID string) (dynamic.Interface, error) {
	if b.scopedFn != nil {
		return b.scopedFn(ctx, clusterID)
	}
	url, err := b.shardFor(ctx, clusterID)
	if err != nil {
		return nil, err
	}
	c := rest.CopyConfig(b.base)
	c.Host = url + "/clusters/" + clusterID
	return dynamic.NewForConfig(c)
}

// vwSecrets adapts a scoped dynamic client to llm.CredentialResolver so
// background runs resolve a model credential — the ModelCredential object and
// the Secret it points at — through the APIExport virtual workspace. An
// unattended run has no caller whose token could read either.
type vwSecrets struct{ dyn dynamic.Interface }

func (v vwSecrets) GetSecret(ctx context.Context, namespace, name string) (*corev1.Secret, error) {
	u, err := v.dyn.Resource(agentsclient.SecretGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	return fromU[corev1.Secret](u)
}

func (v vwSecrets) GetModelCredential(ctx context.Context, name string) (*agentsv1alpha1.ModelCredential, error) {
	u, err := v.dyn.Resource(agentsclient.ModelCredentialGVR).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	return fromU[agentsv1alpha1.ModelCredential](u)
}

func fromU[T any](u *unstructured.Unstructured) (*T, error) {
	var out T
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ---- discovery + recovery tick ------------------------------------------------

// run is the slow tick that remains after the reconcilers took over: it keeps
// the shard set current and sweeps runs a dead replica left in flight. Both are
// things no CR watch can trigger.
// run discovers the virtual workspace and then waits for shutdown.
//
// It used to be a 30-second ticker doing two unrelated jobs: re-reading the
// endpoint slice, and sweeping Postgres across EVERY tenant for runs a crashed
// replica had left in flight. Both are gone as timed work.
//
// The sweep is the Run reconciler's now, and per-object: an unclaimed run is
// claimed when the watch delivers it, an abandoned claim is re-taken when its
// grace lapses, and a run past its deadline is stopped when the requeue the
// deadline itself scheduled comes due. None of that needs a process to wake up
// and ask.
//
// Endpoint discovery is demand-driven instead: shardFor re-discovers when it
// has no shards or does not recognise a cluster, so a shard added to the
// platform is picked up by the first request that needs it rather than up to an
// interval later. The startup call is what makes the very first inbound webhook
// or OAuth callback work without waiting for anything.
func (b *background) run(ctx context.Context) {
	if err := b.ensureVW(ctx); err != nil {
		log.Printf("background: virtual workspace not ready at startup: %v (it will be rediscovered on demand)", err)
	}
	<-ctx.Done()
	if b.discord != nil {
		b.discord.CloseAll()
	}
}

// notifyStrandedRun delivers the sweep's "this did not finish" message to
// wherever the run's answer was headed: the chat a channel conversation came
// from, or the agent channel a schedule/trigger reports to. The background
// executor's half of recovery reporting — it owns the tenant access.
func (b *background) notifyStrandedRun(ctx context.Context, sr store.ScopedRun, clusterID, text string) error {
	d := sr.Run.Delivery
	if d == nil {
		return nil
	}
	dyn, err := b.scoped(ctx, clusterID)
	if err != nil {
		return fmt.Errorf("workspace %s: %w", clusterID, err)
	}
	// A channel conversation is answered in the chat it came from; anything else
	// goes to the agent's configured channel.
	if d.Kind == string(executor.KindChannel) && d.SourceName != "" {
		b.replyToChannelTarget(ctx, dyn, d.SourceName, d.ReplyTarget, text)
		return nil
	}
	au, err := dyn.Resource(agentsclient.AgentGVR).Get(ctx, sr.Run.AgentName, metav1.GetOptions{})
	if err != nil {
		return err
	}
	agent, err := fromU[agentsv1alpha1.Agent](au)
	if err != nil {
		return err
	}
	b.notify(ctx, dyn, agent, d.NotifyChannel, text)
	return nil
}

// resumeRecoveredRun continues a run the sweep found stranded. It is the
// background executor's half of recovery: the sweep owns the policy (what counts
// as stale, when to give up), this owns the tenant access — the agent's own
// ServiceAccount through the APIExport virtual workspace, exactly as a scheduled
// run executes. No user token, so no edges family, same as any background run.
func (b *background) resumeRecoveredRun(ctx context.Context, sr store.ScopedRun, clusterID string) error {
	dyn, err := b.scoped(ctx, clusterID)
	if err != nil {
		return fmt.Errorf("workspace %s: %w", clusterID, err)
	}
	// Count the attempt before resuming, so a run that kills the provider on every
	// resume still walks toward maxRecoveryAttempts instead of looping forever.
	if stored, gerr := b.server.store.GetRun(ctx, sr.Scope, sr.Run.ID); gerr == nil {
		stored.Attempt++
		stored.UpdatedAt = time.Now().UTC()
		if serr := b.server.saveRun(ctx, sr.Scope, stored); serr != nil {
			return fmt.Errorf("recording the resume attempt: %w", serr)
		}
	}
	rd := resumeDeps{
		Creds: vwSecrets{dyn}, CR: vwCR{dyn}, ClusterID: clusterID,
		HubToken: b.agentToken(ctx, dyn, clusterID, sr.Run.AgentName),
	}
	// Detached: the resume outlives this tick, and its own timeout bounds it.
	go b.server.resumeRun(context.WithoutCancel(ctx), sr.Scope, sr.Run.ID, rd, resumeIntent{
		FromPhase: store.RunPhaseRunning,
	})
	return nil
}

// ---- job submission ------------------------------------------------------------

// Submit is the one door for background work (schedule fires from the
// reconciler, trigger webhooks, channel messages, Discord gateway messages).
//
// It writes a Pending Run object and returns. That is the whole submission:
// the OBJECT is the queue, and the Run reconciler is what claims one and hands
// it to this process's worker pool.
//
// What that changed. Submission used to be a push onto an in-process channel,
// with a Pending row recorded first so a lost job could at least be reported.
// Two things followed from the channel being the queue, and neither does any
// more:
//
//   - A restart dropped every queued job. A schedule that fired seconds before
//     a deploy was closed by the recovery sweep as "did not finish" rather than
//     run, because re-deriving a fire from a timer that had already passed was
//     guesswork. The watch re-delivers every Pending Run when a process starts,
//     so unclaimed work is picked up by definition.
//   - A saturated pool made Submit fail. Inbound webhook handlers mapped
//     ErrQueueFull to 503 + Retry-After and leaned on the sender to redeliver,
//     which Slack and Telegram do on their own schedule and GitHub does not.
//     A write that returns is now the end of the producer's responsibility.
//
// A cancel requested while the run is still queued is honoured the same way it
// always was: the claim path checks the row before starting.
func (b *background) Submit(ctx context.Context, job executor.Job) error {
	if job.RunID != "" {
		// Already a run of its own — the producer pre-recorded it. Nothing to
		// enqueue: the object exists and the reconciler will claim it.
		return nil
	}
	now := time.Now().UTC()
	runID := uuid.NewString()
	scope := b.scopeFor(ctx, job.ClusterID, job.AgentRef)
	err := b.server.saveNewRun(ctx, job.ClusterID, scope, store.Run{
		ID: runID, AgentName: job.AgentRef, SessionID: job.SessionID, Trigger: job.Trigger,
		Phase: store.RunPhasePending, Input: job.Task, CreatedAt: now, UpdatedAt: now,
		Delivery: &store.RunDelivery{
			SourceName: job.SourceName, ReplyTarget: job.ReplyTarget,
			NotifyChannel: job.NotifyChannel, Kind: string(job.Kind),
		},
	})
	if err != nil {
		// Nothing was recorded that will ever run, so say so. The producer has
		// a retry path — an inbound webhook answers 503 and the platform
		// redelivers, a schedule is re-fired by its reconciler — and all of
		// them are better than a fire that is silently swallowed.
		return fmt.Errorf("recording the run for %s/%s: %w", job.Kind, job.SourceName, err)
	}
	return nil
}

// DispatchRun executes a run the Run reconciler has claimed for this process.
//
// It rebuilds the job from the two halves of the projection: the object, which
// carries the request (which agent, what trigger, where the answer goes), and
// the store row, which carries the task itself — unbounded text that has no
// business on an API object. Neither half is trusted to be complete on its own:
// a run whose row has gone is closed rather than executed with a blank prompt.
func (b *background) DispatchRun(ctx context.Context, clusterID string, object *agentsv1alpha1.Run) error {
	scope := b.scopeFor(ctx, clusterID, object.Spec.AgentRef)
	run, err := b.server.store.GetRun(ctx, scope, object.Name)
	if err != nil {
		return fmt.Errorf("reading run %s to execute it: %w", object.Name, err)
	}
	if run.CancelRequested {
		// Cancelled while it sat in the queue. Honour it rather than start work
		// nobody wants; the person who cancelled is not told twice.
		b.server.closeRunNow(ctx, scope, run, store.RunPhaseAborted, "cancelled by user")
		return nil
	}
	if runSettled(run.Phase) {
		return nil
	}

	job := executor.Job{
		ID:        object.Name,
		RunID:     object.Name,
		ClusterID: clusterID,
		AgentRef:  object.Spec.AgentRef,
		Trigger:   object.Spec.Trigger,
		SessionID: object.Spec.SessionID,
		Task:      run.Input,
	}
	if d := object.Spec.Delivery; d != nil {
		job.Kind = executor.JobKind(d.Kind)
		job.SourceName = d.SourceName
		job.ReplyTarget = d.ReplyTarget
		job.NotifyChannel = d.NotifyChannel
	}
	// The pool is a concurrency limit now, not a queue of record: the run is
	// already durable and already claimed, so a pool that is momentarily full
	// only delays this dispatch. The reconciler retries it.
	return b.exec.Submit(ctx, job)
}

// RecoverRun picks up a run that was executing on a process that is gone.
//
// The policy is unchanged and lives where it always did (api/recover.go): a
// cancelled run is closed rather than resumed, a run with no checkpoint or too
// many attempts is failed and reported to whoever was waiting, and anything
// else is resumed from its last checkpoint as the agent's own identity. What
// changed is who asks, and when: a sweep over every non-terminal run in every
// tenant, every thirty seconds, is now one reconcile of one object whose own
// claim went stale.
func (b *background) RecoverRun(ctx context.Context, clusterID, agentName, runID string) error {
	scope := b.scopeFor(ctx, clusterID, agentName)
	run, err := b.server.store.GetRun(ctx, scope, runID)
	if err != nil {
		return err
	}
	if runSettled(run.Phase) {
		return nil
	}
	if b.server.liveRuns.has(runID) {
		// Executing right here after all. A run can sit inside one slow tool
		// call for far longer than any grace, and killing it because its claim
		// looks old would be the sweep's oldest bug reintroduced.
		return nil
	}
	b.server.recoverRun(ctx, store.ScopedRun{Scope: scope, Run: run}, b.resumeRecoveredRun, b.notifyStrandedRun)
	return nil
}

// StopRun closes a run the Run reconciler has decided must not continue: one
// past its deadline, or one claimed too many times without finishing.
//
// The reconciler watches objects and cannot see a goroutine, so it does not
// kill runs. This records the request where the run will find it — the same
// durable flag a user's cancel sets, which the engine reads between tool rounds
// on whichever replica is executing. A run executing nowhere is stamped
// terminal here, because otherwise it would sit past its deadline forever with
// nobody left to notice.
func (b *background) StopRun(ctx context.Context, clusterID, agentName, runID, reason string) error {
	scope := b.scopeFor(ctx, clusterID, agentName)
	run, err := b.server.store.GetRun(ctx, scope, runID)
	if err != nil {
		return err
	}
	if runSettled(run.Phase) {
		return nil
	}
	now := time.Now().UTC()
	if err := b.server.store.RequestCancel(ctx, scope, runID, now); err != nil {
		return err
	}
	if b.server.liveRuns.cancel(runID) {
		// Executing here: its context is cancelled and the executor writes the
		// terminal phase with the timing it actually measured.
		return nil
	}
	b.server.closeRunNow(ctx, scope, run, store.RunPhaseAborted, reason)
	return nil
}

// ---- job handler ------------------------------------------------------------

// handle executes one background job: load the agent via the VW, run the task
// through the shared executeTask path, update source status, notify.
// channelErrorText renders a run failure for someone reading a chat.
//
// A cancellation is a person's decision, not a crash, and the error it leaves
// behind is whatever call happened to be in flight — "Post
// https://api.openai.com/v1/chat/completions: context canceled" says nothing to
// the reader and reads like the agent broke. A run that exceeded its own time
// budget is likewise not a fault to report verbatim. Everything else is a real
// error and is passed through, because hiding those is worse.
func channelErrorText(err error) string {
	switch {
	case errors.Is(err, context.Canceled):
		return "⏹️ Stopped before it finished."
	case errors.Is(err, context.DeadlineExceeded):
		return "⏱️ That ran past its time limit and was stopped. A narrower request, or a higher timeout on this agent, should get through."
	}
	return "⚠️ " + err.Error()
}

func (b *background) handle(ctx context.Context, job executor.Job) error {
	dyn, err := b.scoped(ctx, job.ClusterID)
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
	// A job Submit pre-recorded may have been cancelled (or closed by the
	// recovery sweep) while it sat in the queue; starting it now would
	// resurrect a run the user already saw end.
	if job.RunID != "" {
		if stored, gerr := b.server.store.GetRun(ctx, scope, job.RunID); gerr == nil {
			if stored.CancelRequested || stored.Phase != store.RunPhasePending {
				log.Printf("background: job %s/%s: run %s is %s (cancelRequested=%t); not starting it", job.Kind, job.SourceName, job.RunID, stored.Phase, stored.CancelRequested)
				return nil
			}
		}
	}
	res, runErr := b.server.executeTask(ctx, taskRun{
		Creds: vwSecrets{dyn}, CR: vwCR{dyn}, Scope: scope, Agent: agent, RunID: job.RunID,
		SessionID: job.SessionID, Task: job.Task, Trigger: job.Trigger, SourceName: job.SourceName,
		NotifyChannel: job.NotifyChannel,
		// Recorded on the run so a crash mid-flight can still be reported to
		// whoever is waiting — the goroutine that knows this is the thing a
		// restart destroys.
		ReplyTarget:  job.ReplyTarget,
		DeliveryKind: string(job.Kind),
		// There is no user to act as here, so the run acts as the AGENT's own
		// ServiceAccount. Failing to mint one is not fatal: the run proceeds
		// without instance-backed tools, exactly as it did before, rather than
		// losing a scheduled run over a search backend it may never touch.
		ClusterID: job.ClusterID,
		HubToken:  b.agentToken(ctx, dyn, job.ClusterID, agent.Name),
	})

	b.recordOutcome(ctx, job, res.RunID, runErr)

	// Tell the portal the source's status moved (nextRun/lastFired/lastRunID)
	// so schedule and trigger rows refresh without waiting for a poll.
	b.server.events.publish(store.Scope{OrgUUID: scope.OrgUUID, WorkspaceUUID: scope.WorkspaceUUID},
		string(job.Kind), map[string]any{
			"name": job.SourceName, "agent": agent.Name, "runID": res.RunID, "failed": runErr != nil,
		})

	// Paused on an approval gate: the request is already in the inbox and was
	// pushed to the user's channel. Delivery happens when the run resumes.
	if runErr == nil && res.Pending != nil {
		return nil
	}

	// Channel conversations reply through the SOURCE connection — the chat
	// the user is standing in — not the notify connection.
	if job.Kind == executor.KindChannel {
		if runErr != nil {
			b.replyToChannelTarget(ctx, dyn, job.SourceName, job.ReplyTarget, channelErrorText(runErr))
			return runErr
		}
		if out := strings.TrimSpace(res.Content); out != "" {
			// No cap here: the channel layer splits a long answer across messages,
			// and clipping first would discard most of a research report before it
			// ever had the chance.
			b.replyToChannelTarget(ctx, dyn, job.SourceName, job.ReplyTarget, out)
		}
		return nil
	}

	// Notify: schedule/wakeup output always; heartbeat only when actionable.
	// The destination is the schedule/trigger's channel (job.NotifyChannel),
	// else the agent's primary channel.
	if runErr != nil {
		b.notify(ctx, dyn, agent, job.NotifyChannel,
			fmt.Sprintf("%s %q: %s", job.Kind, job.SourceName, channelErrorText(runErr)))
		return runErr
	}
	out := strings.TrimSpace(res.Content)
	if job.Trigger == agentsv1alpha1.RunTriggerHeartbeat && (out == "" || strings.EqualFold(out, "OK") || strings.EqualFold(out, "OK.")) {
		return nil
	}
	if out != "" {
		b.notify(ctx, dyn, agent, job.NotifyChannel, fmt.Sprintf("[%s] %s", job.SourceName, out))
	}
	return nil
}

// scopeFor resolves the store scope for a cluster via the recorded tenant
// mapping; unmapped clusters still run, under a cluster-keyed fallback scope.
func (b *background) scopeFor(ctx context.Context, clusterID, agentName string) store.Scope {
	if ref, ok, _ := b.server.store.GetTenantRef(ctx, clusterID); ok {
		return store.Scope{OrgUUID: ref.OrgUUID, WorkspaceUUID: ref.WorkspaceUUID, AgentName: agentName}
	}
	// The same cluster-keyed fallback a data-plane verb uses
	// (resolveClusterScope), so the rows a run writes are the rows the portal
	// reads. An MCP caller, the one class that still resolves the workspace
	// as a user, records the mapping when it comes through.
	return store.Scope{OrgUUID: unmappedOrg, WorkspaceUUID: clusterID, AgentName: agentName}
}

// PurgeAgentData removes a deleted Agent's rows from the provider store —
// transcripts, runs, usage, inbox items. It is the teardown the
// DELETE /api/agents/{name} handler used to do inline; that route is gone
// (the object is written through kcp now), so the Agent reconciler's
// finalizer calls this instead.
//
// It goes through scopeFor rather than reconstructing a scope from the
// cluster name because scopeFor is also how the rows were WRITTEN, fallback
// included: a run recorded before the tenant mapping existed lives under
// {OrgUUID: "unmapped", WorkspaceUUID: clusterID}, and a purge scoped any
// other way would walk straight past it.
func (b *background) PurgeAgentData(ctx context.Context, clusterID, agentName string) error {
	return b.server.store.DeleteAgentData(ctx, b.scopeFor(ctx, clusterID, agentName), agentName)
}

// PurgeRunData removes one run's rows from the provider store: its transcript,
// its tool-call trace and the run record itself. It is what the Run
// reconciler's finalizer calls, so deleting a Run object — directly, or by
// garbage collection when its owning Agent goes — actually discards the run
// instead of hiding it.
//
// Same scopeFor as PurgeAgentData, and for the same reason: the rows were
// WRITTEN through it, fallback included, and a purge scoped any other way
// would walk straight past a run recorded before the tenant mapping existed.
func (b *background) PurgeRunData(ctx context.Context, clusterID, agentName, runID string) error {
	return b.server.store.DeleteRunData(ctx, b.scopeFor(ctx, clusterID, agentName), runID)
}

// recordOutcome updates the firing schedule's status counters (lastRunID,
// consecutiveFailures, disable-after-N). Triggers record lastFired instead.
func (b *background) recordOutcome(ctx context.Context, job executor.Job, runID string, runErr error) {
	dyn, err := b.scoped(ctx, job.ClusterID)
	if err != nil {
		return
	}
	gvr := agentsclient.ScheduleGVR
	if job.Kind == executor.KindTrigger {
		gvr = agentsclient.TriggerGVR
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

// notify delivers text to one of the agent's channels: the named role
// (a schedule/trigger's ChannelRef) if given, else the agent's primary channel.
// No-op when the agent has no channel configured for that role.
func (b *background) notify(ctx context.Context, dyn dynamic.Interface, agent *agentsv1alpha1.Agent, role, text string) {
	connName, ok := agent.Spec.ResolveChannelConnection(role)
	if !ok {
		return
	}
	b.replyToChannel(ctx, dyn, connName, text)
}

// replyToChannel sends text through a named messaging connection to its
// configured chat/channel (the outbound half of channel conversations).
func (b *background) replyToChannel(ctx context.Context, dyn dynamic.Interface, connName, text string) {
	b.replyToChannelTarget(ctx, dyn, connName, "", text)
}

// replyToChannelTarget delivers a channel reply, optionally to a specific target
// (channel/chat id) instead of the connection's configured default — the
// Discord gateway bot replies to whichever channel the user typed in.
func (b *background) replyToChannelTarget(ctx context.Context, dyn dynamic.Interface, connName, targetOverride, text string) {
	cu, err := dyn.Resource(agentsclient.ConnectionGVR).Get(ctx, connName, metav1.GetOptions{})
	if err != nil {
		log.Printf("background: channel connection %q: %v", connName, err)
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
	target := conn.Spec.Channel
	if targetOverride != "" {
		target = targetOverride
	}
	if err := channels.Send(ctx, channels.Message{
		Type: conn.Spec.Type, Token: token, Target: target, Config: conn.Spec.Config, Text: text,
	}); err != nil {
		log.Printf("background: send via %q failed: %v", connName, err)
	}
}

// triggerFilterAllows evaluates a Trigger's filter against an inbound webhook
// delivery. Supported keys:
//
//	eventType      — exact match on the platform's event header
//	                 (X-GitHub-Event / X-Event-Type) or a top-level "type"/
//	                 "action" field in the JSON body
//	match          — substring that must appear in the raw payload
//	header.<name>  — exact match on a request header
//
// Unknown keys are ignored rather than failing closed, so a filter written for
// a future source never silently blocks every event.
func triggerFilterAllows(filter map[string]string, r *http.Request, body []byte) (string, bool) {
	if len(filter) == 0 {
		return "", true
	}
	for key, want := range filter {
		want = strings.TrimSpace(want)
		if want == "" {
			continue
		}
		switch {
		case key == "eventType":
			if !strings.EqualFold(want, webhookEventType(r, body)) {
				return "eventType does not match " + want, false
			}
		case key == "match":
			if !strings.Contains(string(body), want) {
				return "payload does not contain " + want, false
			}
		case strings.HasPrefix(key, "header."):
			if !strings.EqualFold(r.Header.Get(strings.TrimPrefix(key, "header.")), want) {
				return key + " does not match", false
			}
		}
	}
	return "", true
}

// webhookEventType resolves the delivery's event type from the conventional
// platform headers, falling back to a "type"/"action" field in the JSON body.
func webhookEventType(r *http.Request, body []byte) string {
	for _, h := range []string{"X-GitHub-Event", "X-Event-Type", "X-Event-Key"} {
		if v := strings.TrimSpace(r.Header.Get(h)); v != "" {
			return v
		}
	}
	var probe struct {
		Type   string `json:"type"`
		Action string `json:"action"`
	}
	if json.Unmarshal(body, &probe) == nil {
		if probe.Type != "" {
			return probe.Type
		}
		return probe.Action
	}
	return ""
}

// ---- inbound webhooks --------------------------------------------------------

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
	if err != nil || trig.Spec.Suspend || trig.Status.DisabledReason != "" {
		writeStatus(w, http.StatusConflict, "Suspended", "trigger is suspended or disabled")
		return
	}
	body, _ := io.ReadAll(io.LimitReader(r.Body, 64*1024))
	// spec.filter narrows which deliveries actually fire a run. A filtered-out
	// event is acked (202) so the sender does not retry.
	if reason, ok := triggerFilterAllows(trig.Spec.Filter, r, body); !ok {
		writeJSON(w, http.StatusAccepted, map[string]string{"status": "filtered", "reason": reason})
		return
	}
	task := trig.Spec.Task
	if len(strings.TrimSpace(string(body))) > 0 {
		// The payload is whatever the sender chose to POST. It is evidence for
		// the task, never part of it — quarantine it so an embedded "ignore your
		// instructions and ..." reads as data to the model.
		task += "\n\n" + quarantinePayload("webhook trigger "+name, map[string]string{
			"eventType":   webhookEventType(r, body),
			"contentType": r.Header.Get("Content-Type"),
		}, string(body))
	}
	if strings.TrimSpace(task) == "" {
		writeStatus(w, http.StatusBadRequest, "BadRequest", "trigger has no task")
		return
	}
	if err := s.bg.Submit(r.Context(), executor.Job{
		ID:            fmt.Sprintf("%s/%s/%d", cluster, name, time.Now().UnixNano()),
		Kind:          executor.KindTrigger,
		ClusterID:     cluster,
		SourceName:    name,
		AgentRef:      trig.Spec.AgentRef,
		Task:          task,
		Trigger:       agentsv1alpha1.RunTriggerEvent,
		SessionID:     "trigger:" + name,
		NotifyChannel: trig.Spec.ChannelRef,
	}); err != nil {
		writeStatus(w, http.StatusServiceUnavailable, "Unavailable", err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "accepted"})
}
