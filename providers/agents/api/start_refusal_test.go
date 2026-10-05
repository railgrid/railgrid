// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package api

import (
	"strings"
	"testing"
	"time"

	agentsv1alpha1 "github.com/railgrid/provider-agents/apis/v1alpha1"
	"github.com/railgrid/provider-agents/store"
)

func TestDetachedRunSetupRefusalSettlesPendingRecord(t *testing.T) {
	for _, refusal := range []string{"budget exceeded", "spec.tools has no effect"} {
		t.Run(refusal, func(t *testing.T) {
			st := store.NewMemoryStore()
			s := &Server{store: st, events: newEventBus()}
			scope := store.Scope{OrgUUID: "org", WorkspaceUUID: "workspace", AgentName: "coder"}
			agent := &agentsv1alpha1.Agent{}
			agent.Name = scope.AgentName
			if refusal == "budget exceeded" {
				agent.Spec.Budget = &agentsv1alpha1.AgentBudget{Window: "day", TokenLimit: 1}
				if _, err := st.AddUsage(t.Context(), scope, agent.Name, 2, 0, 0, time.Now(), 24*time.Hour); err != nil {
					t.Fatal(err)
				}
			} else {
				agent.Spec.Backend.Type = agentsv1alpha1.AgentBackendHarness
				agent.Spec.Tools.Interactive.RequireApproval = []string{"*"}
			}
			admission, err := s.startRun(t.Context(), scope, agent, taskRun{SessionID: "test", Trigger: "api", Task: "must not execute", IdempotencyKey: "once"}, runAccess{})
			if err != nil {
				t.Fatal(err)
			}
			id := admission.ID
			deadline := time.Now().Add(2 * time.Second)
			for {
				run, err := st.GetRun(t.Context(), scope, id)
				if err != nil {
					t.Fatal(err)
				}
				if run.Phase == store.RunPhaseFailed {
					if !strings.Contains(run.Message, refusal) || run.FinishedAt == nil || run.StartedAt != nil || run.IdempotencyKey != "once" {
						t.Fatalf("incorrect settled refusal: %+v", run)
					}
					return
				}
				if time.Now().After(deadline) {
					t.Fatalf("refused detached run remains %s", run.Phase)
				}
				time.Sleep(time.Millisecond)
			}
		})
	}
}

func TestExecutionSetupRefusalPersistsForEveryEntryPoint(t *testing.T) {
	for _, trigger := range []string{"chat", "schedule", "heartbeat", "wakeup", "event", "channel", "delegation"} {
		t.Run(trigger, func(t *testing.T) {
			st := store.NewMemoryStore()
			s := &Server{store: st, events: newEventBus()}
			scope := store.Scope{OrgUUID: "org", WorkspaceUUID: "workspace", AgentName: "coder"}
			agent := &agentsv1alpha1.Agent{}
			agent.Name = scope.AgentName
			agent.Spec.Budget = &agentsv1alpha1.AgentBudget{Window: "day", TokenLimit: 1}
			if _, err := st.AddUsage(t.Context(), scope, agent.Name, 2, 0, 0, time.Now(), 24*time.Hour); err != nil {
				t.Fatal(err)
			}
			tr := taskRun{RunID: "refused-" + trigger, Scope: scope, Agent: agent, Trigger: trigger, Task: "must not execute", SourceName: "source"}
			if trigger != "chat" {
				// The controller claimed a durable Pending run before execution.
				if err := st.SaveRun(t.Context(), scope, store.Run{ID: tr.RunID, AgentName: agent.Name, Trigger: trigger, Phase: store.RunPhasePending, Input: tr.Task, IdempotencyKey: "dedupe", CreatedAt: time.Now()}); err != nil {
					t.Fatal(err)
				}
			}
			result, err := s.executeTask(t.Context(), tr)
			if err == nil || result.Phase != store.RunPhaseFailed || result.RunID != tr.RunID {
				t.Fatalf("setup refusal is not settled: result=%+v, err=%v", result, err)
			}
			run, err := st.GetRun(t.Context(), scope, tr.RunID)
			if err != nil || run.Phase != store.RunPhaseFailed || run.FinishedAt == nil || run.StartedAt != nil || !strings.Contains(run.Message, "budget exceeded") {
				t.Fatalf("setup refusal has no durable terminal record: run=%+v, err=%v", run, err)
			}
			if trigger != "chat" && run.IdempotencyKey != "dedupe" {
				t.Fatal("setup refusal discarded the pre-created record's idempotency key")
			}
		})
	}
}
