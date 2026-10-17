// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package api

import (
	"testing"

	agentsv1alpha1 "github.com/railgrid/provider-agents/apis/v1alpha1"
	"github.com/railgrid/provider-agents/store"
)

func TestHarnessSettingsSnapshot(t *testing.T) {
	agent := &agentsv1alpha1.Agent{}
	agent.Spec.Backend.Type = "harness"
	agent.Spec.Backend.Harness = &agentsv1alpha1.AgentHarnessBackend{Model: "gpt-6-luna", ReasoningEffort: "max"}
	snapshot := resolveHarnessSettings(agent, nil)
	if snapshot.Model != "gpt-6-luna" || snapshot.ReasoningEffort != "max" {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	agent.Spec.Backend.Harness.Model = "changed"
	agent.Spec.Backend.Harness.ReasoningEffort = "low"
	restored := resolveHarnessSettings(agent, snapshot)
	if *restored != *snapshot || restored == snapshot {
		t.Fatal("snapshot was not preserved as an independent copy")
	}
	empty := resolveHarnessSettings(agent, &store.HarnessSettings{})
	if empty.Model != "" || empty.ReasoningEffort != "" {
		t.Fatal("explicit harness defaults inherited new Agent settings")
	}
}
