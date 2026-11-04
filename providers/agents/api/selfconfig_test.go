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
	"github.com/railgrid/provider-agents/internal/harnesspolicy"
	"github.com/railgrid/provider-agents/tools"
)

func TestSelfConfigGated(t *testing.T) {
	tests := []struct {
		approval    string
		interactive bool
		want        bool
	}{
		{"", true, false},
		{"", false, true}, // default Unattended: a trigger run's payload may be hostile
		{agentsv1alpha1.SelfConfigApprovalUnattended, false, true},
		{agentsv1alpha1.SelfConfigApprovalAlways, true, true},
		{agentsv1alpha1.SelfConfigApprovalNever, false, false},
	}
	for _, tt := range tests {
		agent := &agentsv1alpha1.Agent{Spec: agentsv1alpha1.AgentSpec{
			SelfConfiguration: &agentsv1alpha1.AgentSelfConfiguration{Enabled: true, Approval: tt.approval},
		}}
		if got := selfConfigGated(agent, tt.interactive); got != tt.want {
			t.Errorf("approval=%q interactive=%v: gated = %v, want %v", tt.approval, tt.interactive, got, tt.want)
		}
	}
}

// Every gated tool must be one the family actually ships, or approval would
// silently guard a name that no longer exists.
func TestSelfConfigMutatingToolsAreShipped(t *testing.T) {
	shipped := map[string]bool{}
	for _, n := range tools.SelfConfigToolNames {
		shipped[n] = true
	}
	for _, n := range tools.SelfConfigMutatingTools {
		if !shipped[n] {
			t.Errorf("approval gates %q, which the family does not ship", n)
		}
	}
}

// A spawned worker answers one question; it never rewrites the agent that
// spawned it.
func TestWorkersNeverSelfConfigure(t *testing.T) {
	for _, n := range tools.SelfConfigToolNames {
		if !workerExcludedTools[n] {
			t.Errorf("worker is not excluded from %q", n)
		}
	}
}

func TestHarnessRejectsSelfConfiguration(t *testing.T) {
	agent := &agentsv1alpha1.Agent{Spec: agentsv1alpha1.AgentSpec{
		SelfConfiguration: &agentsv1alpha1.AgentSelfConfiguration{Enabled: true},
	}}
	if reason, _ := harnesspolicy.UnsupportedFields(agent); reason != agentsv1alpha1.ReasonMeaninglessForHarness {
		t.Fatalf("harness agent with selfConfiguration: reason = %q, want %q", reason, agentsv1alpha1.ReasonMeaninglessForHarness)
	}
	agent.Spec.SelfConfiguration.Enabled = false
	if reason, _ := harnesspolicy.UnsupportedFields(agent); reason != "" {
		t.Fatalf("disabled selfConfiguration rejected: %q", reason)
	}
}
