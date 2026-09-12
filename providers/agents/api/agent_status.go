// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package api

// Agent.status is observed state the provider owns: phase (Ready unless
// something suspended the agent), lastRunAt, and the rolling-window usage.
// Nothing else writes it — without these stamps a freshly created agent
// stayed at status {} forever, even after successful runs, and callers polling
// for phase Ready never saw it.
//
// Three writers:
//   - applyAgentCreate stamps phase Ready as soon as the agent exists;
//   - the run path stamps lastRunAt (+ usage) when a run reaches a terminal
//     phase, through whichever CR accessor the run holds (user client or the
//     background virtual-workspace client);
//   - the background sweep stamps phase Ready on any agent that has no phase
//     yet (agents created before this existed, or whose create-time stamp
//     failed).

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	agentsv1alpha1 "github.com/faroshq/provider-agents/apis/v1alpha1"
	agentsclient "github.com/faroshq/provider-agents/client"
	"github.com/faroshq/provider-agents/store"
)

// agentStatusPatcher is implemented by the CR accessors that can write an
// Agent's status subresource. It is optional on tools.CRAccess so test fakes
// and read-only accessors need not implement it; the run path skips the
// stamp when the accessor cannot write.
type agentStatusPatcher interface {
	PatchAgentStatus(ctx context.Context, name string, status agentsv1alpha1.AgentStatus) error
}

var (
	_ agentStatusPatcher = clientCR{}
	_ agentStatusPatcher = vwCR{}
)

func (a clientCR) PatchAgentStatus(ctx context.Context, name string, status agentsv1alpha1.AgentStatus) error {
	return a.c.Agents().PatchStatus(ctx, name, status)
}

// PatchAgentStatus on the virtual-workspace client reads the object and PUTs
// its status subresource (the same claim-by-resourceVersion path the schedule
// reconciler uses), merging only the given fields.
func (a vwCR) PatchAgentStatus(ctx context.Context, name string, status agentsv1alpha1.AgentStatus) error {
	u, err := a.dyn.Resource(agentsclient.AgentGVR).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	fields, err := agentStatusFields(status)
	if err != nil {
		return err
	}
	if err := mergeStatusFields(u, fields); err != nil {
		return err
	}
	_, err = a.dyn.Resource(agentsclient.AgentGVR).UpdateStatus(ctx, u, metav1.UpdateOptions{})
	return err
}

// agentStatusFields renders a partial AgentStatus as the map of fields to
// merge (omitempty drops whatever was not set).
func agentStatusFields(status agentsv1alpha1.AgentStatus) (map[string]any, error) {
	data, err := json.Marshal(status)
	if err != nil {
		return nil, err
	}
	fields := map[string]any{}
	if err := json.Unmarshal(data, &fields); err != nil {
		return nil, err
	}
	return fields, nil
}

// mergeStatusFields merges fields into u's .status in place.
func mergeStatusFields(u *unstructured.Unstructured, fields map[string]any) error {
	st, _, _ := unstructured.NestedMap(u.Object, "status")
	if st == nil {
		st = map[string]any{}
	}
	for k, v := range fields {
		st[k] = v
	}
	return unstructured.SetNestedMap(u.Object, st, "status")
}

// agentRunStatus is the status stamp for a run that reached a terminal phase
// at `at`: lastRunAt, the rolling-window usage when known, and phase Ready
// when the agent has no phase yet (never clobbers Suspended).
func agentRunStatus(agent *agentsv1alpha1.Agent, at time.Time, window *store.Usage) agentsv1alpha1.AgentStatus {
	status := agentsv1alpha1.AgentStatus{LastRunAt: &metav1.Time{Time: at}}
	if agent.Status.Phase == "" {
		status.Phase = agentsv1alpha1.AgentPhaseReady
	}
	if window != nil && !window.WindowStart.IsZero() {
		status.Usage = &agentsv1alpha1.AgentUsageStatus{
			WindowStart: &metav1.Time{Time: window.WindowStart},
			Tokens:      window.InputTokens + window.OutputTokens,
			USD:         fmt.Sprintf("%.4f", float64(window.USDMicros)/1e6),
		}
	}
	return status
}

// recordAgentRun stamps the agent's status after a run reached a terminal
// phase. Best-effort and logged: the run's own record is the source of
// truth, this is the CR-level summary of it.
func (s *Server) recordAgentRun(ctx context.Context, cr any, agent *agentsv1alpha1.Agent, at time.Time, window *store.Usage) {
	p, ok := cr.(agentStatusPatcher)
	if !ok || agent == nil {
		return
	}
	if err := p.PatchAgentStatus(ctx, agent.Name, agentRunStatus(agent, at, window)); err != nil {
		log.Printf("agent %s: recording run in status: %v", agent.Name, err)
	}
}
