// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package api

// Projecting a run onto its Run CR.
//
// A run's transcript, tool trace and resume checkpoint are Postgres rows: high
// churn, unbounded, and of no interest to an API server. Its IDENTITY is not.
// Before the Run kind existed, a run could only be reached through a verb on
// its agent, which meant the platform could not list runs, could not watch
// them, could not authorize one of them separately from the rest, and could
// not garbage-collect them when the agent went away. So the object carries
// what a tenant needs to ASK about a run, and the store keeps what a run
// actually produced. That split is the projection carve-out in
// docs/provider-connectivity-contract.md.
//
// Every run write in this package goes through saveRun or saveNewRun. There is
// no discipline to remember and no call site that can forget: the store write
// and the projection are one call.
//
// The two differ on one question — is the object REQUIRED?
//
// For a run somebody is watching, no. The replica serving their request owns
// the execution, and a run that completes but whose object could not be updated
// is a reporting problem; failing the run because the API server was slow is a
// real one, and the second is worse. So saveRun logs and swallows, and the next
// transition re-converges the object because each write sends the whole desired
// status rather than a delta.
//
// For UNATTENDED work it is required, and saveNewRun is what enforces that.
// Nobody is holding a connection open for a schedule fire or an inbound
// message: the object is the only thing that will ever cause it to run, so a
// run recorded in Postgres with no object is not a reporting problem, it is
// work that silently never happens. The object is therefore written FIRST and
// its failure is returned to the producer, which has a retry path. The residual
// risk is inverted on purpose: an object with no row is visible, claimable and
// closable, whereas a row with no object is invisible to everything.
//
// It writes through the APIExport virtual workspace, as the PROVIDER, never as
// the caller. A Run's status is this provider's statement about its own
// execution: a chat run started by a user and a schedule firing at 3am must
// produce the same object, and neither should depend on what the person who
// started it may write.

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic"

	agentsv1alpha1 "github.com/railgrid/provider-agents/apis/v1alpha1"
	agentsclient "github.com/railgrid/provider-agents/client"
	"github.com/railgrid/provider-agents/store"
)

const (
	// projectionTimeout bounds one projection write. Short: this sits on the
	// run's critical path, and a slow API server must not hold a run open.
	projectionTimeout = 10 * time.Second
	// runInputPreviewMax bounds spec.inputPreview. The authoritative input is
	// the first transcript row; this is what makes `kubectl get run -o yaml`
	// say what the run is about.
	runInputPreviewMax = 2048
)

// saveRun writes a run to the store and projects it onto its Run CR.
//
// It replaces every direct store.SaveRun call in this package. The store write
// is authoritative and its error is returned; the projection is reporting and
// its error is logged.
func (s *Server) saveRun(ctx context.Context, scope store.Scope, run store.Run) error {
	if err := s.store.SaveRun(ctx, scope, run); err != nil {
		return err
	}
	s.projectRun(ctx, scope, run)
	return nil
}

// saveNewRun records a new UNATTENDED run: the object first, then the store
// row. Either failure is returned.
//
// Order is the point. The object is what will cause the run to execute — the
// reconciler claims it, and nothing else is watching — so writing the row first
// and dying before the object leaves work nobody will ever do. This way the
// worst case is an object with no row, which the reconciler finds, cannot
// dispatch, and closes; and the producer is told either way.
func (s *Server) saveNewRun(ctx context.Context, clusterID string, scope store.Scope, run store.Run) error {
	if err := s.projectRunTo(ctx, clusterID, run); err != nil {
		return fmt.Errorf("creating the Run object: %w", err)
	}
	if err := s.store.SaveRun(ctx, scope, run); err != nil {
		// Take the object back out rather than leave a run that can never be
		// executed. Best-effort: if this also fails the reconciler closes it
		// after MaxClaims, which is slower but not wrong.
		s.deleteRunObject(ctx, clusterID, run.ID)
		return err
	}
	return nil
}

// projectRun creates or updates the Run object for a stored run, best-effort.
// See the file comment for why an attended run's projection may fail quietly.
func (s *Server) projectRun(ctx context.Context, scope store.Scope, run store.Run) {
	if s == nil || s.bg == nil || run.ID == "" {
		// No virtual workspace (no provider kubeconfig; local dev against a
		// bare hub). The store is still authoritative and the provider still
		// works; there is simply no object to project onto.
		return
	}
	clusterID, ok := s.clusterForScope(ctx, scope)
	if !ok {
		return
	}
	if err := s.projectRunTo(ctx, clusterID, run); err != nil {
		log.Printf("agents: projecting run %s onto its Run object in %s: %v", run.ID, clusterID, err)
	}
}

// projectRunTo is the projection itself, against a known cluster.
func (s *Server) projectRunTo(ctx context.Context, clusterID string, run store.Run) error {
	if s == nil || s.bg == nil {
		return fmt.Errorf("no virtual workspace: this provider has no tenant access")
	}
	if run.ID == "" || clusterID == "" {
		return fmt.Errorf("a run needs an id and a cluster to be projected")
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), projectionTimeout)
	defer cancel()

	dyn, err := s.bg.scoped(ctx, clusterID)
	if err != nil {
		return fmt.Errorf("reaching %s: %w", clusterID, err)
	}
	return s.writeRunObject(ctx, dyn, run)
}

// deleteRunObject removes a Run object this process created and then could not
// back with a store row.
func (s *Server) deleteRunObject(ctx context.Context, clusterID, runID string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), projectionTimeout)
	defer cancel()
	dyn, err := s.bg.scoped(ctx, clusterID)
	if err != nil {
		log.Printf("agents: removing the orphaned Run object %s in %s: %v", runID, clusterID, err)
		return
	}
	if err := dyn.Resource(agentsclient.RunGVR).Delete(ctx, runID, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
		log.Printf("agents: removing the orphaned Run object %s in %s: %v", runID, clusterID, err)
	}
}

// writeRunObject converges one Run object on the stored run.
func (s *Server) writeRunObject(ctx context.Context, dyn dynamic.Interface, run store.Run) error {
	runs := dyn.Resource(agentsclient.RunGVR)

	existing, err := runs.Get(ctx, run.ID, metav1.GetOptions{})
	switch {
	case apierrors.IsNotFound(err):
		created, cerr := s.createRunObject(ctx, dyn, run)
		if cerr != nil {
			return cerr
		}
		existing = created
	case err != nil:
		return fmt.Errorf("reading the Run object: %w", err)
	}

	// Status is a subresource, so spec is untouched here by construction —
	// which is what makes "spec is written once" true rather than merely
	// intended.
	desired := runStatusFor(run)
	current, _, _ := unstructured.NestedMap(existing.Object, "status")
	next, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&desired)
	if err != nil {
		return fmt.Errorf("encoding the Run status: %w", err)
	}
	if equalStatus(current, next) {
		return nil
	}
	existing.Object["status"] = next
	if _, err := runs.UpdateStatus(ctx, existing, metav1.UpdateOptions{}); err != nil {
		if apierrors.IsConflict(err) {
			// Somebody wrote first. The next transition sends the whole status
			// again, so there is nothing to merge and nothing to retry.
			return nil
		}
		return fmt.Errorf("writing the Run status: %w", err)
	}
	return nil
}

// createRunObject creates the Run for a stored run, owned by its Agent.
//
// The ownerReference is what makes deleting an agent discard its runs: kube
// garbage-collects the Run objects, and each one's finalizer purges the rows
// behind it. Without the UID there is no owner to point at, so a run whose
// agent cannot be read is created unowned rather than not at all — an
// unreferenced object is recoverable, a missing one is not.
func (s *Server) createRunObject(ctx context.Context, dyn dynamic.Interface, run store.Run) (*unstructured.Unstructured, error) {
	object := &agentsv1alpha1.Run{
		TypeMeta: metav1.TypeMeta{
			APIVersion: agentsv1alpha1.SchemeGroupVersion.String(),
			Kind:       "Run",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: run.ID,
			Labels: map[string]string{
				// The label is what makes "this agent's runs" a server-side
				// list rather than a client-side filter over every run in the
				// workspace.
				LabelAgent:   run.AgentName,
				LabelTrigger: run.Trigger,
			},
		},
		Spec: agentsv1alpha1.RunSpec{
			AgentRef:       run.AgentName,
			Trigger:        run.Trigger,
			SessionID:      run.SessionID,
			ParentRunRef:   run.ParentRunID,
			IdempotencyKey: run.IdempotencyKey,
			InputPreview:   safeTruncate(strings.TrimSpace(run.Input), runInputPreviewMax),
		},
	}
	if d := run.Delivery; d != nil {
		// The delivery target is part of the REQUEST, so it is projected onto
		// spec: a process that picks this run up after a restart has to know
		// where the answer goes, and the goroutine that knew is exactly what a
		// crash destroys.
		object.Spec.Delivery = &agentsv1alpha1.RunDelivery{
			Kind:          d.Kind,
			SourceName:    d.SourceName,
			ReplyTarget:   d.ReplyTarget,
			NotifyChannel: d.NotifyChannel,
		}
	}
	if agent, err := dyn.Resource(agentsclient.AgentGVR).Get(ctx, run.AgentName, metav1.GetOptions{}); err == nil {
		object.OwnerReferences = []metav1.OwnerReference{{
			APIVersion: agentsv1alpha1.SchemeGroupVersion.String(),
			Kind:       "Agent",
			Name:       agent.GetName(),
			UID:        agent.GetUID(),
		}}
	} else {
		log.Printf("agents: run %s has no owner reference: reading agent %s: %v", run.ID, run.AgentName, err)
	}

	raw, err := runtime.DefaultUnstructuredConverter.ToUnstructured(object)
	if err != nil {
		return nil, fmt.Errorf("encoding the Run object: %w", err)
	}
	created, err := dyn.Resource(agentsclient.RunGVR).Create(ctx, &unstructured.Unstructured{Object: raw}, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		// Two replicas projected the same transition. Read the winner.
		return dyn.Resource(agentsclient.RunGVR).Get(ctx, run.ID, metav1.GetOptions{})
	}
	if err != nil {
		return nil, fmt.Errorf("creating the Run object: %w", err)
	}
	return created, nil
}

// Labels the provider stamps on a Run so the common listings are server-side.
const (
	LabelAgent   = "agents.railgrid.ai/agent"
	LabelTrigger = "agents.railgrid.ai/trigger"
)

// runStatusFor is the whole desired status for a stored run. It is computed
// from the store row and nothing else, so any write converges the object
// regardless of what it held before.
func runStatusFor(run store.Run) agentsv1alpha1.RunStatus {
	status := agentsv1alpha1.RunStatus{
		Phase:   string(run.Phase),
		Message: safeTruncate(run.Message, 2048),
	}
	if run.StartedAt != nil {
		at := metav1.NewTime(*run.StartedAt)
		status.StartedAt = &at
	}
	if run.FinishedAt != nil {
		at := metav1.NewTime(*run.FinishedAt)
		status.FinishedAt = &at
	}
	if run.SessionID != "" {
		status.TranscriptRef = &agentsv1alpha1.RunTranscriptRef{SessionID: run.SessionID}
	}
	if run.InputTokens > 0 || run.OutputTokens > 0 || run.USDMicros > 0 || run.WorkedDurationMS != nil {
		status.Usage = &agentsv1alpha1.RunUsage{
			InputTokens:      run.InputTokens,
			OutputTokens:     run.OutputTokens,
			USDMicros:        run.USDMicros,
			USD:              formatUSDMicros(run.USDMicros),
			WorkedDurationMS: run.WorkedDurationMS,
		}
		if run.StartedAt != nil && run.FinishedAt != nil {
			status.Usage.DurationMS = run.FinishedAt.Sub(*run.StartedAt).Milliseconds()
		}
	}
	return status
}

// formatUSDMicros renders micros as a human-readable dollar amount, matching
// what an Agent's usage status carries so the two read the same.
func formatUSDMicros(micros int64) string {
	if micros == 0 {
		return ""
	}
	return fmt.Sprintf("%.4f", float64(micros)/1e6)
}

// equalJSON compares two already-unstructured maps. json.Marshal is
// deterministic for maps (Go sorts keys), so this is a stable structural
// comparison without reaching for reflect.DeepEqual on interface-typed numbers
// that may or may not have round-tripped through float64.
func equalJSON(a, b map[string]any) bool {
	left, lerr := json.Marshal(a)
	right, rerr := json.Marshal(b)
	if lerr != nil || rerr != nil {
		return false
	}
	return string(left) == string(right)
}

// equalStatus reports whether a projection would be a no-op. Skipping an
// identical write matters more than it looks: every status update is a watch
// event, and a run that checkpoints every few iterations would otherwise wake
// the reconciler and every portal subscriber for nothing.
func equalStatus(current, next map[string]any) bool {
	if current == nil {
		return false
	}
	// Owner and conditions are written by the reconciler, not by the
	// projection, so they are carried over rather than compared.
	for _, key := range []string{"owner", "conditions", "deadlineAt", "observedGeneration"} {
		if value, ok := current[key]; ok {
			next[key] = value
		}
	}
	return equalJSON(current, next)
}

// clusterForScope maps a store scope back to the logical cluster whose virtual
// workspace can write its objects.
//
// It is a store lookup, memoized: a scope's cluster does not change, and the
// projection runs on every run transition, so re-reading it each time would put
// a query on the critical path of every checkpoint.
func (s *Server) clusterForScope(ctx context.Context, scope store.Scope) (string, bool) {
	if scope.OrgUUID == "" || scope.WorkspaceUUID == "" {
		return "", false
	}
	key := scope.OrgUUID + "|" + scope.WorkspaceUUID
	if cached, ok := s.scopeClusters.get(key); ok {
		return cached, true
	}
	clusterID, ok, err := s.store.FindClusterForScope(ctx, scope.OrgUUID, scope.WorkspaceUUID)
	if err != nil || !ok || clusterID == "" {
		return "", false
	}
	s.scopeClusters.put(key, clusterID)
	return clusterID, true
}

// scopeClusterCache memoizes scope → logical cluster. Unbounded growth is not
// a concern: one entry per workspace this replica has executed a run for.
type scopeClusterCache struct {
	mu      sync.Mutex
	entries map[string]string
}

func newScopeClusterCache() *scopeClusterCache {
	return &scopeClusterCache{entries: map[string]string{}}
}

func (c *scopeClusterCache) get(key string) (string, bool) {
	if c == nil {
		return "", false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.entries[key]
	return v, ok
}

func (c *scopeClusterCache) put(key, value string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[key] = value
}
