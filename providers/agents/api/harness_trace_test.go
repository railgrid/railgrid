// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package api

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	agentsv1alpha1 "github.com/railgrid/provider-agents/apis/v1alpha1"
	"github.com/railgrid/provider-agents/backend"
	"github.com/railgrid/provider-agents/store"
)

type auditRecordingStore struct {
	store.Store
	fail        error
	contextErr  error
	hasDeadline bool
}

func (s *auditRecordingStore) AppendToolCall(ctx context.Context, scope store.Scope, call store.ToolCall) error {
	s.contextErr = ctx.Err()
	_, s.hasDeadline = ctx.Deadline()
	if s.fail != nil {
		return s.fail
	}
	return s.Store.AppendToolCall(ctx, scope, call)
}

func TestHarnessCompletionPersistsTraceAndCanceledToolEvidence(t *testing.T) {
	for _, failed := range []bool{false, true} {
		st := &auditRecordingStore{Store: store.NewMemoryStore()}
		s := &Server{store: st}
		scope := store.Scope{OrgUUID: "org", WorkspaceUUID: "ws", AgentName: "coder"}
		agent := &agentsv1alpha1.Agent{}
		agent.Name = scope.AgentName
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		sink := s.turnSink(ctx, taskRun{Scope: scope, Agent: agent, RunID: "run", Trigger: "chat"}, "session", time.Now(), agentsv1alpha1.AgentBackendHarness, nil)
		sink.ToolEnd(backend.ToolEvent{ID: "shell-1", Name: "shell", Args: `{"command":"printf TRACE_OK","token":"do-not-store"}`, Result: "TRACE_OK", Err: failed, Duration: 25 * time.Millisecond})
		calls, err := st.ListToolCalls(t.Context(), scope, "run")
		if err != nil || len(calls) != 1 {
			t.Fatalf("trace = %+v, error = %v", calls, err)
		}
		call := calls[0]
		if call.AgentName != "coder" || call.Trigger != "chat" || call.Tool != "shell" || call.Result != "TRACE_OK" || call.DurationMS != 25 {
			t.Fatalf("incomplete harness trace: %+v", call)
		}
		if strings.Contains(call.Args, "do-not-store") || !strings.Contains(call.Args, "printf TRACE_OK") {
			t.Fatalf("unexpected audit arguments: %s", call.Args)
		}
		if (call.Outcome == "error") != failed || (call.Error != "") != failed {
			t.Fatalf("wrong outcome: %+v", call)
		}
		if st.contextErr != nil || !st.hasDeadline {
			t.Fatalf("audit context = %v, bounded = %v", st.contextErr, st.hasDeadline)
		}
		page, err := st.ListMessages(t.Context(), scope, "session", 10, "")
		if err != nil || len(page.Items) != 1 || page.Items[0].Content != "TRACE_OK" {
			t.Fatalf("transcript = %+v, error = %v", page, err)
		}
	}
}

func TestHarnessAuditFailureFailsTurnAndModelSinkDoesNotDuplicateAudit(t *testing.T) {
	st := &auditRecordingStore{Store: store.NewMemoryStore(), fail: errors.New("audit unavailable")}
	s := &Server{store: st}
	agent := &agentsv1alpha1.Agent{}
	agent.Name = "coder"
	run := taskRun{Scope: store.Scope{OrgUUID: "org", WorkspaceUUID: "ws", AgentName: "coder"}, Agent: agent, RunID: "run"}
	sink := s.turnSink(t.Context(), run, "chat", time.Now(), agentsv1alpha1.AgentBackendHarness, nil)
	sink.ToolEnd(backend.ToolEvent{ID: "shell-1", Name: "shell", Result: "done"})
	if err := sink.Aborted(t.Context()); err == nil || !strings.Contains(err.Error(), "audit unavailable") {
		t.Fatalf("audit failure = %v", err)
	}
	modelSink := s.turnSink(t.Context(), run, "chat", time.Now(), agentsv1alpha1.AgentBackendModel, nil)
	modelSink.ToolEnd(backend.ToolEvent{ID: "tool-1", Name: "tool", Result: "done"})
	if err := modelSink.Aborted(t.Context()); err != nil {
		t.Fatalf("model sink duplicated wrapTool audit: %v", err)
	}
}
