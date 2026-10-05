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

	agentsv1alpha1 "github.com/railgrid/provider-agents/apis/v1alpha1"
	"github.com/railgrid/provider-agents/internal/harnesspolicy"
)

func TestHarnessCannotExecuteWithUnsupportedToolRestrictions(t *testing.T) {
	for _, trigger := range []string{"chat", "api", "schedule", "heartbeat", "trigger"} {
		for _, field := range []string{"families", "connections", "toolsets", "requireApproval"} {
			for _, background := range []bool{false, true} {
				t.Run(trigger+"/"+field, func(t *testing.T) {
					agent := &agentsv1alpha1.Agent{}
					agent.Spec.Backend.Type = agentsv1alpha1.AgentBackendHarness
					grant := &agent.Spec.Tools.Interactive
					if background {
						grant = &agent.Spec.Tools.Background
					}
					switch field {
					case "families":
						grant.Families = []string{"core"}
					case "connections":
						grant.Connections = []string{"restricted"}
					case "toolsets":
						grant.Toolsets = []string{"restricted"}
					case "requireApproval":
						grant.RequireApproval = []string{"*"}
					}
					// No store, runner factory, credential, or status conditions: the
					// execution gate must reject the spec before touching any of them.
					result, err := (&Server{}).runTurn(t.Context(), taskRun{Agent: agent, Trigger: trigger}, nil)
					if err == nil || !strings.Contains(err.Error(), "spec.tools has no effect") || result.RunID != "" {
						t.Fatalf("unsupported policy reached execution: result=%+v err=%v", result, err)
					}
				})
			}
		}
	}
}

func TestHarnessCannotExecuteWithUnsupportedAgentControls(t *testing.T) {
	for _, trigger := range []string{"chat", "api", "schedule", "heartbeat", "trigger"} {
		for _, tc := range []struct {
			name   string
			field  string
			mutate func(*agentsv1alpha1.Agent)
		}{
			{"suggest autonomy", "spec.autonomy", func(a *agentsv1alpha1.Agent) { a.Spec.Autonomy = agentsv1alpha1.AutonomySuggest }},
			{"auto autonomy", "spec.autonomy", func(a *agentsv1alpha1.Agent) { a.Spec.Autonomy = agentsv1alpha1.AutonomyAuto }},
			{"tool turn limit", "maxToolTurns", func(a *agentsv1alpha1.Agent) { a.Spec.Limits.MaxToolTurns = 1 }},
			{"delegates", "spec.delegates", func(a *agentsv1alpha1.Agent) { a.Spec.Delegates = []string{"reviewer"} }},
			{"spawn limit", "maxSpawnsPerRun", func(a *agentsv1alpha1.Agent) { a.Spec.Limits.MaxSpawnsPerRun = 1 }},
			{"concurrent spawn limit", "maxConcurrentSpawns", func(a *agentsv1alpha1.Agent) { a.Spec.Limits.MaxConcurrentSpawns = 1 }},
		} {
			t.Run(trigger+"/"+tc.name, func(t *testing.T) {
				agent := &agentsv1alpha1.Agent{}
				agent.Spec.Backend.Type = agentsv1alpha1.AgentBackendHarness
				tc.mutate(agent)
				// runTurn must refuse before looking at credentials, stores, or a
				// runner, for interactive and unattended execution alike.
				result, err := (&Server{}).runTurn(t.Context(), taskRun{Agent: agent, Trigger: trigger}, nil)
				if err == nil || !strings.Contains(err.Error(), tc.field) || result.RunID != "" {
					t.Fatalf("unsupported control reached execution: result=%+v err=%v; want refusal naming %s", result, err, tc.field)
				}
			})
		}
	}
}

func TestHarnessPolicyAllowsSupportedDefaultsAndMemory(t *testing.T) {
	for _, autonomy := range []string{"", agentsv1alpha1.AutonomyAsk} {
		agent := &agentsv1alpha1.Agent{}
		agent.Spec.Backend.Type = agentsv1alpha1.AgentBackendHarness
		agent.Spec.Autonomy = autonomy
		enabled := true
		agent.Spec.Memory = agentsv1alpha1.AgentMemoryPolicy{Enabled: &enabled, MaxNotes: 4}
		agent.Spec.Limits.TimeoutSeconds = 120
		if reason, message := harnesspolicy.UnsupportedFields(agent); reason != "" || message != "" {
			t.Fatalf("supported autonomy=%q memory/timeout settings rejected: %s: %s", autonomy, reason, message)
		}
	}
}
