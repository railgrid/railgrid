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

	"github.com/railgrid/provider-agents/store"

	"github.com/railgrid/railgrid/pkg/runner"
)

func TestRunnerCleanupRunBindingIssueUsesFixedReasonPerFence(t *testing.T) {
	validRun := store.Run{AttemptID: "run-1", AgentUID: "agent-1"}
	validBinding := &store.RunnerCleanupBinding{
		ClusterID: "cluster-1", TaskID: "task-1", AttemptID: "run-1", AttemptEpoch: 1,
		AgentUID: "agent-1", BackendKey: "backend-1",
	}
	tests := []struct {
		name      string
		run       store.Run
		clusterID string
		binding   func() *store.RunnerCleanupBinding
		want      string
	}{
		{name: "valid", run: validRun, clusterID: "cluster-1", binding: func() *store.RunnerCleanupBinding { return validBinding }, want: ""},
		{name: "legacy absent run attempt with exact run binding", run: store.Run{ID: "run-1", AgentUID: "agent-1"}, clusterID: "cluster-1", binding: func() *store.RunnerCleanupBinding { return validBinding }, want: ""},
		{name: "run attempt absent", run: store.Run{AgentUID: "agent-1"}, clusterID: "cluster-1", binding: func() *store.RunnerCleanupBinding { return validBinding }, want: "run_attempt_unavailable"},
		{name: "legacy binding does not prove run attempt", run: store.Run{ID: "other-run", AgentUID: "agent-1"}, clusterID: "cluster-1", binding: func() *store.RunnerCleanupBinding { return validBinding }, want: "run_attempt_unavailable"},
		{name: "run and binding attempt differ", run: store.Run{AttemptID: "run-2", AgentUID: "agent-1"}, clusterID: "cluster-1", binding: func() *store.RunnerCleanupBinding { return validBinding }, want: "run_attempt_mismatch"},
		{name: "cluster differs", run: validRun, clusterID: "cluster-2", binding: func() *store.RunnerCleanupBinding { return validBinding }, want: "runner_cluster_mismatch"},
		{name: "binding incomplete", run: validRun, clusterID: "cluster-1", binding: func() *store.RunnerCleanupBinding { b := *validBinding; b.AttemptEpoch = 0; return &b }, want: "cleanup_binding_incomplete"},
		{name: "agent unavailable", run: store.Run{AttemptID: "run-1"}, clusterID: "cluster-1", binding: func() *store.RunnerCleanupBinding { return validBinding }, want: "agent_binding_unavailable"},
		{name: "agent differs", run: store.Run{AttemptID: "run-1", AgentUID: "agent-2"}, clusterID: "cluster-1", binding: func() *store.RunnerCleanupBinding { return validBinding }, want: "agent_binding_mismatch"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := runnerCleanupRunBindingIssue(tt.run, tt.clusterID, tt.binding()); got != tt.want {
				t.Fatalf("reason = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRunnerCleanupReceiptIssueUsesFixedReasonPerFence(t *testing.T) {
	binding := &store.RunnerCleanupBinding{TaskID: "task-1", AttemptID: "run-1", AttemptEpoch: 2}
	tests := []struct {
		name    string
		receipt runner.Receipt
		want    string
	}{
		{name: "valid", receipt: runner.Receipt{TaskID: "task-1", AttemptID: "run-1", AttemptEpoch: 2}},
		{name: "attempt differs", receipt: runner.Receipt{TaskID: "task-1", AttemptID: "run-2", AttemptEpoch: 2}, want: "runner_attempt_mismatch"},
		{name: "task differs", receipt: runner.Receipt{TaskID: "task-2", AttemptID: "run-1", AttemptEpoch: 2}, want: "runner_task_mismatch"},
		{name: "epoch differs", receipt: runner.Receipt{TaskID: "task-1", AttemptID: "run-1", AttemptEpoch: 3}, want: "runner_epoch_mismatch"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := runnerCleanupReceiptIssue(tt.receipt, binding); got != tt.want {
				t.Fatalf("reason = %q, want %q", got, tt.want)
			}
		})
	}
}
