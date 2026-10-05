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
