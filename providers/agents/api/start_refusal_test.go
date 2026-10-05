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
			id := s.startRun(t.Context(), scope, agent, taskRun{SessionID: "test", Trigger: "api", Task: "must not execute", IdempotencyKey: "once"}, runAccess{})
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
