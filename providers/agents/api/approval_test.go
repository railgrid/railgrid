// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	agentsv1alpha1 "github.com/faroshq/provider-agents/apis/v1alpha1"
	"github.com/faroshq/provider-agents/engine"
	"github.com/faroshq/provider-agents/store"
	"github.com/faroshq/provider-agents/tools"
)

func testScope() store.Scope {
	return store.Scope{OrgUUID: "org", WorkspaceUUID: "ws", AgentName: "ada"}
}

func testDeps(st store.Store, agent *agentsv1alpha1.Agent) tools.Deps {
	return tools.Deps{
		Store: st, Scope: testScope(), Agent: agent, CR: fakeCR{},
		ConnSecretName: connectionSecretName, RunID: "run-1",
	}
}

func mkAgent(autonomy string) *agentsv1alpha1.Agent {
	return &agentsv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: "ada"},
		Spec:       agentsv1alpha1.AgentSpec{DisplayName: "Ada", Autonomy: autonomy},
	}
}

// TestWrapToolGatesExecRich is the regression test for the approval/audit
// bypass: MCP tools (edges/infrastructure/code — the dangerous surface) expose
// only ExecRich, and the engine prefers it, so a wrapper that only wrapped Exec
// left them ungated and unaudited.
func TestWrapToolGatesExecRich(t *testing.T) {
	st := store.NewMemoryStore()
	s := &Server{store: st, events: newEventBus(), liveRuns: newRunRegistry()}
	agent := mkAgent(agentsv1alpha1.AutonomyAsk)

	executed := false
	richTool := engine.Tool{
		Name: "edges__pods_delete",
		ExecRich: func(context.Context, string) (engine.Observation, error) {
			executed = true
			return engine.Observation{Text: "deleted"}, nil
		},
	}

	wrapped := s.wrapTool(richTool, testDeps(st, agent), taskRun{}, agentsv1alpha1.RunTriggerChat, []string{"edges__*"})
	if wrapped.Exec != nil {
		t.Fatal("wrapped tool must expose a single execution path (ExecRich only)")
	}

	_, err := wrapped.ExecRich(context.Background(), `{"name":"api-0"}`)
	if executed {
		t.Fatal("gated tool executed without approval")
	}
	var interrupt *engine.InterruptError
	if !asInterruptErr(err, &interrupt) {
		t.Fatalf("want *engine.InterruptError, got %T: %v", err, err)
	}
	if interrupt.Tool != "edges__pods_delete" {
		t.Fatalf("interrupt tool = %q", interrupt.Tool)
	}

	// The pause must be recorded in the inbox (bound to the run) and audited.
	items, err := st.ListInbox(context.Background(), store.Scope{OrgUUID: "org", WorkspaceUUID: "ws"}, store.InboxStatePending)
	if err != nil || len(items) != 1 {
		t.Fatalf("inbox items = %d (err %v), want 1 pending approval", len(items), err)
	}
	if items[0].RunID != "run-1" {
		t.Fatalf("inbox item runID = %q, want run-1 (approval must bind to its run)", items[0].RunID)
	}
	calls, err := st.ListToolCalls(context.Background(), testScope(), "run-1")
	if err != nil || len(calls) != 1 || calls[0].Outcome != "pending_approval" {
		t.Fatalf("tool calls = %+v (err %v), want one pending_approval audit row", calls, err)
	}
}

// TestWrapToolAuditsExecRichResult verifies MCP-style tools land in the audit
// log with their full (redacted) args and result — the data the run trace view
// renders.
func TestWrapToolAuditsExecRichResult(t *testing.T) {
	st := store.NewMemoryStore()
	s := &Server{store: st, events: newEventBus(), liveRuns: newRunRegistry()}
	agent := mkAgent(agentsv1alpha1.AutonomyAsk)

	tool := engine.Tool{
		Name: "edges__pods_list",
		ExecRich: func(context.Context, string) (engine.Observation, error) {
			return engine.Observation{Text: "api-0 Running"}, nil
		},
	}
	wrapped := s.wrapTool(tool, testDeps(st, agent), taskRun{}, agentsv1alpha1.RunTriggerChat, nil)
	obs, err := wrapped.ExecRich(context.Background(), `{"namespace":"prod","token":"s3cret"}`)
	if err != nil || obs.Text != "api-0 Running" {
		t.Fatalf("exec = %+v, %v", obs, err)
	}
	calls, _ := st.ListToolCalls(context.Background(), testScope(), "run-1")
	if len(calls) != 1 {
		t.Fatalf("want 1 audit row, got %d", len(calls))
	}
	if calls[0].Result != "api-0 Running" || calls[0].Outcome != "ok" {
		t.Fatalf("audit row = %+v", calls[0])
	}
	if strings.Contains(calls[0].Args, "s3cret") {
		t.Fatalf("secret leaked into the audit log: %s", calls[0].Args)
	}
	if !strings.Contains(calls[0].Args, "prod") {
		t.Fatalf("non-secret args must survive redaction: %s", calls[0].Args)
	}
}

// TestApprovalGrantAuthorizesExactCall covers the resume contract: an approval
// authorizes exactly one call with the exact arguments the user saw — not any
// later call of the same tool with different arguments.
func TestApprovalGrantAuthorizesExactCall(t *testing.T) {
	st := store.NewMemoryStore()
	s := &Server{store: st, events: newEventBus(), liveRuns: newRunRegistry()}
	agent := mkAgent(agentsv1alpha1.AutonomyAsk)
	approvedArgs := `{"repo":"acme/site","number":42}`

	calls := 0
	tool := engine.Tool{
		Name: "gh__merge_pr",
		ExecRich: func(_ context.Context, args string) (engine.Observation, error) {
			calls++
			return engine.Observation{Text: "merged " + args}, nil
		},
	}
	used := false
	run := taskRun{ApproveTool: "gh__merge_pr", ApproveArgs: approvedArgs, approveUsed: &used}
	wrapped := s.wrapTool(tool, testDeps(st, agent), run, agentsv1alpha1.RunTriggerChat, []string{"*"})

	// The approved call goes through.
	if _, err := wrapped.ExecRich(context.Background(), approvedArgs); err != nil {
		t.Fatalf("approved call was blocked: %v", err)
	}
	if calls != 1 {
		t.Fatalf("approved call did not execute (calls=%d)", calls)
	}
	// A second call — even of the same tool — is gated again.
	if _, err := wrapped.ExecRich(context.Background(), approvedArgs); err == nil {
		t.Fatal("approval was reusable; it must authorize exactly one call")
	}
	// Different arguments never ride an approval granted for other arguments.
	used2 := false
	run2 := taskRun{ApproveTool: "gh__merge_pr", ApproveArgs: approvedArgs, approveUsed: &used2}
	wrapped2 := s.wrapTool(tool, testDeps(st, agent), run2, agentsv1alpha1.RunTriggerChat, []string{"*"})
	if _, err := wrapped2.ExecRich(context.Background(), `{"repo":"acme/site","number":99}`); err == nil {
		t.Fatal("approval for PR 42 authorized a call for PR 99")
	}
	if calls != 1 {
		t.Fatalf("unapproved calls executed (calls=%d)", calls)
	}
}

func TestResolveInboxDecisionRequiresDisclosedObjectBeforeApproval(t *testing.T) {
	tests := []struct {
		name     string
		kind     store.InboxItemKind
		payload  map[string]any
		state    store.InboxItemState
		wantCode int
	}{
		{name: "missing tool", payload: map[string]any{"args": `{}`}, state: store.InboxStateApproved, wantCode: http.StatusConflict},
		{name: "missing arguments", payload: map[string]any{"tool": "notify"}, state: store.InboxStateApproved, wantCode: http.StatusConflict},
		{name: "malformed arguments", payload: map[string]any{"tool": "notify", "args": `{`}, state: store.InboxStateApproved, wantCode: http.StatusConflict},
		{name: "null arguments", payload: map[string]any{"tool": "notify", "args": `null`}, state: store.InboxStateApproved, wantCode: http.StatusConflict},
		{name: "array arguments", payload: map[string]any{"tool": "notify", "args": `[]`}, state: store.InboxStateApproved, wantCode: http.StatusConflict},
		{name: "scalar arguments", payload: map[string]any{"tool": "notify", "args": `true`}, state: store.InboxStateApproved, wantCode: http.StatusConflict},
		{name: "empty object is valid", payload: map[string]any{"tool": "notify", "args": `{}`}, state: store.InboxStateApproved},
		{name: "denial remains available", payload: map[string]any{}, state: store.InboxStateDenied},
		{name: "non-approval kind keeps existing decision behavior", kind: store.InboxKindQuestion, payload: map[string]any{}, state: store.InboxStateApproved},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			st := store.NewMemoryStore()
			s := &Server{store: st}
			scope := store.Scope{OrgUUID: "org", WorkspaceUUID: "ws"}
			now := time.Now().UTC()
			kind := tc.kind
			if kind == "" {
				kind = store.InboxKindApproval
			}
			if err := st.AddInboxItem(ctx, scope, store.InboxItem{
				ID: "i1", Kind: kind, State: store.InboxStatePending,
				Payload: tc.payload, CreatedAt: now, UpdatedAt: now,
			}); err != nil {
				t.Fatal(err)
			}

			_, err := s.resolveInboxDecision(ctx, scope, "i1", tc.state, "", now.Add(time.Second))
			if tc.wantCode == 0 {
				if err != nil {
					t.Fatalf("resolve: %v", err)
				}
				got, getErr := st.GetInboxItem(ctx, scope, "i1")
				if getErr != nil || got.State != tc.state {
					t.Fatalf("state = %q (err %v), want %q", got.State, getErr, tc.state)
				}
				return
			}

			if err == nil {
				t.Fatal("approval unexpectedly resolved without a complete disclosure")
			}
			got, getErr := st.GetInboxItem(ctx, scope, "i1")
			if getErr != nil || got.State != store.InboxStatePending {
				t.Fatalf("invalid approval mutated inbox state to %q (err %v)", got.State, getErr)
			}
			w := httptest.NewRecorder()
			writeUpdateError(w, err)
			if w.Code != tc.wantCode {
				t.Fatalf("status = %d, want %d: %s", w.Code, tc.wantCode, w.Body.String())
			}
			var body map[string]any
			if decodeErr := json.Unmarshal(w.Body.Bytes(), &body); decodeErr != nil {
				t.Fatal(decodeErr)
			}
			if body["reason"] != "ApprovalDisclosureUnavailable" {
				t.Fatalf("reason = %#v, want ApprovalDisclosureUnavailable", body["reason"])
			}
		})
	}
}

func TestChannelApprovalReportsUnavailableDisclosureWithoutMutating(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemoryStore()
	s := &Server{store: st}
	scope := store.Scope{OrgUUID: "org", WorkspaceUUID: "ws", AgentName: "ada"}
	now := time.Now().UTC()
	if err := st.AddInboxItem(ctx, scope, store.InboxItem{
		ID: "i1", AgentName: "ada", Kind: store.InboxKindApproval, State: store.InboxStatePending,
		Prompt: "Allow the tool?", Payload: map[string]any{"tool": "notify", "args": `not-json secret-value`},
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/", nil)
	listed, handled := s.channelCommand(req, scope, nil, "", mkAgent(agentsv1alpha1.AutonomyAsk), "", "/inbox")
	if !handled || !strings.Contains(listed, "Allow the tool?") {
		t.Fatalf("inbox disclosure = %q", listed)
	}
	if strings.Contains(listed, "secret-value") {
		t.Fatalf("malformed arguments were rendered back to the channel: %q", listed)
	}

	reply, handled := s.channelCommand(req, scope, nil, "", mkAgent(agentsv1alpha1.AutonomyAsk), "", "/approve 1")
	if !handled || !strings.Contains(reply, approvalDisclosureUnavailableMessage) {
		t.Fatalf("approve reply = %q", reply)
	}
	item, err := st.GetInboxItem(ctx, scope, "i1")
	if err != nil || item.State != store.InboxStatePending {
		t.Fatalf("invalid channel approval mutated inbox state to %q (err %v)", item.State, err)
	}

	if reply, handled = s.channelCommand(req, scope, nil, "", mkAgent(agentsv1alpha1.AutonomyAsk), "", "/deny 1"); !handled || !strings.Contains(reply, "Denied") {
		t.Fatalf("deny reply = %q", reply)
	}
	item, err = st.GetInboxItem(ctx, scope, "i1")
	if err != nil || item.State != store.InboxStateDenied {
		t.Fatalf("denial state = %q (err %v), want denied", item.State, err)
	}
}

// TestAutonomyGating checks the three postures now that spec.autonomy is
// enforced rather than stored and ignored.
func TestAutonomyGating(t *testing.T) {
	tests := []struct {
		autonomy    string
		approval    []string
		tool        string
		wantBlocked bool
	}{
		{agentsv1alpha1.AutonomyAuto, []string{"*"}, "edges__pods_delete", false},
		{agentsv1alpha1.AutonomySuggest, nil, "edges__pods_delete", true},
		{agentsv1alpha1.AutonomySuggest, nil, "notify", false}, // exempt: it's how the agent reaches the user
		{agentsv1alpha1.AutonomyAsk, []string{"edges__*"}, "edges__pods_delete", true},
		{agentsv1alpha1.AutonomyAsk, []string{"edges__*"}, "web_fetch", false},
	}
	for _, tc := range tests {
		st := store.NewMemoryStore()
		s := &Server{store: st, events: newEventBus(), liveRuns: newRunRegistry()}
		agent := mkAgent(tc.autonomy)
		grant := agentsv1alpha1.ToolGrant{RequireApproval: tc.approval}
		switch tc.autonomy {
		case agentsv1alpha1.AutonomySuggest:
			grant.RequireApproval = []string{"*"}
		case agentsv1alpha1.AutonomyAuto:
			grant.RequireApproval = nil
		}
		executed := false
		tool := engine.Tool{Name: tc.tool, ExecRich: func(context.Context, string) (engine.Observation, error) {
			executed = true
			return engine.Observation{Text: "ok"}, nil
		}}
		wrapped := s.wrapTool(tool, testDeps(st, agent), taskRun{}, agentsv1alpha1.RunTriggerChat, grant.RequireApproval)
		_, _ = wrapped.ExecRich(context.Background(), `{}`)
		if tc.wantBlocked && executed {
			t.Errorf("autonomy=%s tool=%s: executed but should have been gated", tc.autonomy, tc.tool)
		}
		if !tc.wantBlocked && !executed {
			t.Errorf("autonomy=%s tool=%s: gated but should have run", tc.autonomy, tc.tool)
		}
	}
}

func TestRedactArgs(t *testing.T) {
	tests := []struct {
		in         string
		wantHas    []string
		wantAbsent []string
	}{
		{`{"token":"abc","path":"/tmp"}`, []string{"/tmp", "[redacted]"}, []string{"abc"}},
		{`{"nested":{"api_key":"k1","keep":"yes"}}`, []string{"yes", "[redacted]"}, []string{"k1"}},
		{`{"Authorization":"Bearer x"}`, []string{"[redacted]"}, []string{"Bearer x"}},
		{`not json at all`, []string{"not json at all"}, nil},
		{``, nil, nil},
	}
	for _, tc := range tests {
		got := redactArgs(tc.in)
		for _, want := range tc.wantHas {
			if !strings.Contains(got, want) {
				t.Errorf("redactArgs(%q) = %q, want it to contain %q", tc.in, got, want)
			}
		}
		for _, absent := range tc.wantAbsent {
			if strings.Contains(got, absent) {
				t.Errorf("redactArgs(%q) = %q, must not contain %q", tc.in, got, absent)
			}
		}
	}
}

func TestSafeTruncateDoesNotSplitRunes(t *testing.T) {
	s := strings.Repeat("é", 100) // 2 bytes per rune
	got := safeTruncate(s, 51)
	if !strings.HasSuffix(got, "…[truncated]") {
		t.Fatalf("missing truncation marker: %q", got)
	}
	body := strings.TrimSuffix(got, "…[truncated]")
	if !utf8ValidString(body) {
		t.Fatalf("truncation split a rune: %q", body)
	}
	if short := safeTruncate("abc", 10); short != "abc" {
		t.Fatalf("short strings must pass through unchanged, got %q", short)
	}
}

func utf8ValidString(s string) bool {
	for _, r := range s {
		if r == 0xFFFD {
			return false
		}
	}
	return true
}

// asInterruptErr unwraps an engine interrupt without importing errors in the
// test body (mirrors the engine's own helper).
func asInterruptErr(err error, out **engine.InterruptError) bool {
	ie, ok := err.(*engine.InterruptError)
	if ok {
		*out = ie
	}
	return ok
}
