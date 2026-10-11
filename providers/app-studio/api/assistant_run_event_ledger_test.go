// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"

	"github.com/railgrid/provider-app-studio/store"
)

func TestAssistantRunEventLedgerPersistsCallBeforeResultInOrder(t *testing.T) {
	ctx := context.Background()
	messageStore, scope := newAssistantRunEventLedgerTestStore(t, "run-order")
	ledger := newProjectAssistantRunEventLedger(messageStore, scope, "run-order")
	spec := projectAssistantToolSpec{Name: projectToolEditFile, Risk: projectAssistantToolRiskWrite}

	decision, err := ledger.BeginToolCall(ctx, "call-edit", spec, map[string]any{
		"path": "src/App.vue", "oldString": "old", "newString": "new",
	})
	if err != nil {
		t.Fatalf("BeginToolCall: %v", err)
	}
	if !decision.ShouldDispatch() {
		t.Fatalf("first call decision = %#v, want dispatch", decision)
	}
	events := listAssistantRunEventLedgerEvents(t, messageStore, scope, "run-order")
	if len(events) != 1 || events[0].Type != projectAssistantRunToolCallEventType || events[0].Sequence != 1 {
		t.Fatalf("events before dispatch = %#v, want one call event", events)
	}

	wantResult := `{"operation":"edit_file","path":"src/App.vue"}`
	outcome, err := ledger.FinishToolCall(ctx, decision.Token, wantResult, nil)
	if err != nil {
		t.Fatalf("FinishToolCall: %v", err)
	}
	if outcome.Result != wantResult || outcome.Failed {
		t.Fatalf("outcome = %#v, want exact successful result", outcome)
	}
	events = listAssistantRunEventLedgerEvents(t, messageStore, scope, "run-order")
	if len(events) != 2 || events[1].Type != projectAssistantRunToolResultEventType || events[1].Sequence != 2 {
		t.Fatalf("events after dispatch = %#v, want ordered call then result", events)
	}
	if events[0].CallID != events[1].CallID || events[0].ArgsDigest != events[1].ArgsDigest {
		t.Fatalf("call/result identity mismatch: %#v", events)
	}
}

func TestAssistantRunEventLedgerRetainsDurableResultTime(t *testing.T) {
	ctx := context.Background()
	messageStore, scope := newAssistantRunEventLedgerTestStore(t, "run-result-time")
	ledger := newProjectAssistantRunEventLedger(messageStore, scope, "run-result-time")
	spec := projectAssistantToolSpec{Name: projectToolExecCommand, Risk: projectAssistantToolRiskRead}
	args := map[string]any{"component": "workspace", "argv": []any{"go", "test"}}
	decision, err := ledger.BeginToolCall(ctx, "call-result-time", spec, args)
	if err != nil {
		t.Fatalf("BeginToolCall: %v", err)
	}
	outcome, err := ledger.FinishToolCall(ctx, decision.Token, `{"status":"succeeded","exitCode":0}`, nil)
	if err != nil {
		t.Fatalf("FinishToolCall: %v", err)
	}
	if outcome.ObservedAt.IsZero() {
		t.Fatal("settled outcome omitted its durable result timestamp")
	}
	canceledCtx, cancel := context.WithCancel(ctx)
	cancel()
	known, found, err := ledger.ToolCallOutcome(canceledCtx, "call-result-time")
	if err != nil || !found || !known.ObservedAt.Equal(outcome.ObservedAt) {
		t.Fatalf("known outcome after cancellation = %#v, found=%t, err=%v", known, found, err)
	}

	restarted := newProjectAssistantRunEventLedger(messageStore, scope, "run-result-time")
	_, replay, found, err := restarted.SettledToolCall(ctx, "call-result-time", projectToolExecCommand)
	if err != nil {
		t.Fatalf("SettledToolCall after restart: %v", err)
	}
	if !found || !replay.ObservedAt.Equal(outcome.ObservedAt) {
		t.Fatalf("replayed result timestamp = %v (found=%t), want durable timestamp %v", replay.ObservedAt, found, outcome.ObservedAt)
	}
}

func TestAssistantRunEventLedgerToolCallOutcomeRespectsContextWhileWaiting(t *testing.T) {
	ledger := newProjectAssistantRunEventLedger(nil, store.Scope{}, "run-lock-context")
	ledger.mu.Lock()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, found, err := ledger.ToolCallOutcome(ctx, "call-blocked")
	ledger.mu.Unlock()
	if found || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("ToolCallOutcome while ledger lock is held = found %t, err=%v; want context deadline", found, err)
	}
}

func TestAssistantRunEventLedgerDuplicateResultKeepsFirstObservedAt(t *testing.T) {
	ctx := context.Background()
	messageStore, scope := newAssistantRunEventLedgerTestStore(t, "run-duplicate-result-time")
	ledger := newProjectAssistantRunEventLedger(messageStore, scope, "run-duplicate-result-time")
	spec := projectAssistantToolSpec{Name: projectToolExecCommand, Risk: projectAssistantToolRiskRead}
	decision, err := ledger.BeginToolCall(ctx, "call-duplicate-result-time", spec, map[string]any{
		"component": "workspace",
		"argv":      []any{"go", "test"},
	})
	if err != nil {
		t.Fatalf("BeginToolCall: %v", err)
	}
	firstOutcome, err := ledger.FinishToolCall(ctx, decision.Token, `{"status":"succeeded","exitCode":0}`, nil)
	if err != nil {
		t.Fatalf("FinishToolCall: %v", err)
	}
	events := listAssistantRunEventLedgerEvents(t, messageStore, scope, "run-duplicate-result-time")
	if len(events) != 2 || events[1].Type != projectAssistantRunToolResultEventType {
		t.Fatalf("durable events = %#v, want call and result", events)
	}
	duplicate := events[1]
	duplicate.Sequence = 0
	duplicate.CreatedAt = duplicate.CreatedAt.Add(time.Minute)
	if _, err := messageStore.AppendAssistantRunEvent(ctx, scope, duplicate, 2); err != nil {
		t.Fatalf("append legacy duplicate result: %v", err)
	}

	restarted := newProjectAssistantRunEventLedger(messageStore, scope, "run-duplicate-result-time")
	outcome, found, err := restarted.ToolCallOutcome(ctx, "call-duplicate-result-time")
	if err != nil {
		t.Fatalf("ToolCallOutcome after duplicate result: %v", err)
	}
	if !found || outcome.Result != firstOutcome.Result || !outcome.ObservedAt.Equal(firstOutcome.ObservedAt) {
		t.Fatalf("duplicate result outcome = %#v (found=%t), want original result timestamp %v", outcome, found, firstOutcome.ObservedAt)
	}
}

func TestAssistantRunEventLedgerDispatchedReceiptUsesAdmittedArguments(t *testing.T) {
	ctx := context.Background()
	messageStore, scope := newAssistantRunEventLedgerTestStore(t, "run-dispatched-receipt")
	ledger := newProjectAssistantRunEventLedger(messageStore, scope, "run-dispatched-receipt")
	spec := projectAssistantToolSpec{Name: projectToolExecCommand, Risk: projectAssistantToolRiskRead}
	requested := map[string]any{"component": "backend", "argv": []any{"go", "test"}}
	admitted := map[string]any{"component": "backend", "argv": []any{"go", "test", "./..."}}
	if _, err := ledger.RecordToolRequest(ctx, "call-normalized", spec, requested); err != nil {
		t.Fatalf("RecordToolRequest: %v", err)
	}
	decision, err := ledger.BeginToolCall(ctx, "call-normalized", spec, admitted)
	if err != nil {
		t.Fatalf("BeginToolCall: %v", err)
	}
	wantResult := `{"status":"succeeded","exitCode":0}`
	if _, err := ledger.FinishToolCall(ctx, decision.Token, wantResult, nil); err != nil {
		t.Fatalf("FinishToolCall: %v", err)
	}

	restarted := newProjectAssistantRunEventLedger(messageStore, scope, "run-dispatched-receipt")
	args, outcome, found, err := restarted.DispatchedSettledToolCall(ctx, "call-normalized", projectToolExecCommand)
	if err != nil || !found {
		t.Fatalf("DispatchedSettledToolCall = found %t, err=%v; want validated receipt", found, err)
	}
	if !reflect.DeepEqual(args, admitted) || outcome.Result != wantResult || outcome.ObservedAt.IsZero() {
		t.Fatalf("receipt args/outcome = %#v, %#v; want admitted args and durable result time", args, outcome)
	}

	legacyStore, legacyScope := newAssistantRunEventLedgerTestStore(t, "run-legacy-dispatched-receipt")
	legacy := newProjectAssistantRunEventLedger(legacyStore, legacyScope, "run-legacy-dispatched-receipt")
	legacyDecision, err := legacy.BeginToolCall(ctx, "call-legacy", spec, admitted)
	if err != nil {
		t.Fatalf("legacy BeginToolCall: %v", err)
	}
	if _, err := legacy.FinishToolCall(ctx, legacyDecision.Token, wantResult, nil); err != nil {
		t.Fatalf("legacy FinishToolCall: %v", err)
	}
	legacyRestarted := newProjectAssistantRunEventLedger(legacyStore, legacyScope, "run-legacy-dispatched-receipt")
	legacyArgs, _, legacyFound, err := legacyRestarted.DispatchedSettledToolCall(ctx, "call-legacy", projectToolExecCommand)
	if err != nil || !legacyFound || !reflect.DeepEqual(legacyArgs, admitted) {
		t.Fatalf("legacy receipt = %#v, found=%t, err=%v; want validated tool_call-only receipt", legacyArgs, legacyFound, err)
	}
}

func TestAssistantRunEventLedgerDispatchedReceiptRequiresDispatch(t *testing.T) {
	ctx := context.Background()
	messageStore, scope := newAssistantRunEventLedgerTestStore(t, "run-request-only-result")
	ledger := newProjectAssistantRunEventLedger(messageStore, scope, "run-request-only-result")
	spec := projectAssistantToolSpec{Name: projectToolExecCommand, Risk: projectAssistantToolRiskRead}
	args := map[string]any{"component": "workspace", "argv": []any{"go", "test"}}
	request, err := ledger.RecordToolRequest(ctx, "call-not-dispatched", spec, args)
	if err != nil {
		t.Fatalf("RecordToolRequest: %v", err)
	}
	if _, err := ledger.FinishToolCall(ctx, request.Token, `{"status":"canceled"}`, context.Canceled); err != nil {
		t.Fatalf("FinishToolCall before dispatch: %v", err)
	}
	if _, _, found, err := ledger.DispatchedSettledToolCall(ctx, "call-not-dispatched", projectToolExecCommand); err != nil || found {
		t.Fatalf("dispatched receipt lookup = found %t, err=%v; want no dispatch proof", found, err)
	}
}

func TestAssistantRunEventLedgerPersistsRequestBeforeValidationAndReplaysRejection(t *testing.T) {
	ctx := context.Background()
	messageStore, scope := newAssistantRunEventLedgerTestStore(t, "run-preflight-rejection")
	ledger := newProjectAssistantRunEventLedger(messageStore, scope, "run-preflight-rejection")
	spec := projectAssistantToolSpec{Name: projectToolEditFile, Risk: projectAssistantToolRiskWrite}
	args := map[string]any{"path": "src/App.vue", "oldString": "old", "newString": "new"}

	request, err := ledger.RecordToolRequest(ctx, "call-invalid-edit", spec, args)
	if err != nil {
		t.Fatal(err)
	}
	events := listAssistantRunEventLedgerEvents(t, messageStore, scope, "run-preflight-rejection")
	if len(events) != 1 || events[0].Type != projectAssistantRunToolRequestEventType {
		t.Fatalf("pre-validation events = %#v, want one durable request", events)
	}
	wantResult := "Tool call failed: stale_source"
	if _, err := ledger.FinishToolCall(ctx, request.Token, wantResult, errors.New("stale_source")); err != nil {
		t.Fatal(err)
	}
	events = listAssistantRunEventLedgerEvents(t, messageStore, scope, "run-preflight-rejection")
	if len(events) != 2 || events[1].Type != projectAssistantRunToolResultEventType {
		t.Fatalf("rejected events = %#v, want request and result", events)
	}

	restarted := newProjectAssistantRunEventLedger(messageStore, scope, "run-preflight-rejection")
	replay, err := restarted.RecordToolRequest(ctx, "call-invalid-edit", spec, args)
	if err != nil || replay.Replay == nil || replay.Replay.Result != wantResult {
		t.Fatalf("rejected request replay = %#v, err=%v", replay, err)
	}
}

func TestAssistantRunEventLedgerSeparatesRequestedAndAdmittedArguments(t *testing.T) {
	ctx := context.Background()
	messageStore, scope := newAssistantRunEventLedgerTestStore(t, "run-normalized-admission")
	ledger := newProjectAssistantRunEventLedger(messageStore, scope, "run-normalized-admission")
	spec := projectAssistantToolSpec{Name: projectToolCommitProjectFiles, Risk: projectAssistantToolRiskCommit}
	original := map[string]any{"message": "Commit app"}
	normalized := map[string]any{"message": "Commit app", "paths": []string{"package.json"}, "workspaceDigest": "sha256:workspace"}

	if _, err := ledger.RecordToolRequest(ctx, "call-commit", spec, original); err != nil {
		t.Fatal(err)
	}
	admitted, err := ledger.BeginToolCall(ctx, "call-commit", spec, normalized)
	if err != nil || !admitted.ShouldDispatch() {
		t.Fatalf("admitted normalized call = %#v, err=%v", admitted, err)
	}
	if _, err := ledger.FinishToolCall(ctx, admitted.Token, `{"status":"succeeded"}`, nil); err != nil {
		t.Fatal(err)
	}
	events := listAssistantRunEventLedgerEvents(t, messageStore, scope, "run-normalized-admission")
	if len(events) != 3 || events[0].Type != projectAssistantRunToolRequestEventType || events[1].Type != projectAssistantRunToolCallEventType || events[2].Type != projectAssistantRunToolResultEventType {
		t.Fatalf("events = %#v, want request, admission, result", events)
	}
}

func TestAssistantRunEventLedgerReplaysExactCompletedCall(t *testing.T) {
	ctx := context.Background()
	messageStore, scope := newAssistantRunEventLedgerTestStore(t, "run-replay")
	spec := projectAssistantToolSpec{Name: projectToolReadFile, Risk: projectAssistantToolRiskRead}
	ledger := newProjectAssistantRunEventLedger(messageStore, scope, "run-replay")

	decision, err := ledger.BeginToolCall(ctx, "call-read", spec, map[string]any{"path": "src/App.vue", "limit": 200})
	if err != nil {
		t.Fatalf("BeginToolCall: %v", err)
	}
	wantResult := "Tool call failed: exact model-visible failure"
	wantError := errors.New("exact model-visible failure")
	if _, err := ledger.FinishToolCall(ctx, decision.Token, wantResult, wantError); err != nil {
		t.Fatalf("FinishToolCall: %v", err)
	}

	// Reconstruct from durable state and provide the same arguments in a
	// different map insertion order. Canonical JSON makes this the same call.
	restarted := newProjectAssistantRunEventLedger(messageStore, scope, "run-replay")
	replay, err := restarted.BeginToolCall(ctx, "call-read", spec, map[string]any{"limit": 200, "path": "src/App.vue"})
	if err != nil {
		t.Fatalf("BeginToolCall replay: %v", err)
	}
	if replay.ShouldDispatch() || replay.Replay == nil {
		t.Fatalf("replay decision = %#v, want durable replay", replay)
	}
	if replay.Replay.Result != wantResult || replay.Replay.Error != wantError.Error() || !replay.Replay.Failed {
		t.Fatalf("replayed outcome = %#v, want exact result and error", replay.Replay)
	}
	result, invokeErr := replay.Replay.InvokeResult()
	if result != wantResult || invokeErr == nil || invokeErr.Error() != wantError.Error() {
		t.Fatalf("InvokeResult = (%q, %v), want exact persisted values", result, invokeErr)
	}
	if events := listAssistantRunEventLedgerEvents(t, messageStore, scope, "run-replay"); len(events) != 2 {
		t.Fatalf("replay appended events: %#v", events)
	}
	items, err := messageStore.ListAssistantConversationItems(ctx, scope, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("conversation items = %#v, want call and result", items)
	}
	var toolMessage chatMessage
	if err := json.Unmarshal(items[1].Payload, &toolMessage); err != nil {
		t.Fatal(err)
	}
	if toolMessage.Content != wantResult {
		t.Fatalf("model-visible failure = %q, want exact result without duplicated backend error", toolMessage.Content)
	}
}

func TestAssistantToolPersistsFailureAndReturnsItToModel(t *testing.T) {
	ctx := context.Background()
	messageStore, scope := newAssistantRunEventLedgerTestStore(t, "run-model-failure")
	ledger := newProjectAssistantRunEventLedger(messageStore, scope, "run-model-failure")
	spec := projectAssistantToolSpec{Name: projectToolEditFile, Risk: projectAssistantToolRiskWrite}
	args := map[string]any{"path": "src/App.vue", "oldString": "old", "newString": "new"}
	decision, err := ledger.BeginToolCall(ctx, "call-edit", spec, args)
	if err != nil {
		t.Fatal(err)
	}
	tool := projectEinoAssistantTool{
		req:      projectAssistantRunRequest{eventLedger: ledger},
		runState: newProjectEinoAssistantRunState(),
	}
	wantResult := "Tool call failed: stale_source: expected current source"
	result, err := tool.finishDurableToolFailureForModel(ctx, decision, wantResult, errors.New("stale_source: expected current source"))
	if err != nil || result != wantResult {
		t.Fatalf("finish failure = (%q, %v), want model-visible result", result, err)
	}

	restarted := newProjectAssistantRunEventLedger(messageStore, scope, "run-model-failure")
	replay, err := restarted.BeginToolCall(ctx, "call-edit", spec, args)
	if err != nil || replay.Replay == nil || !replay.Replay.Failed {
		t.Fatalf("durable failure replay = %#v, err=%v", replay, err)
	}
	tool.req.eventLedger = restarted
	result, err = tool.replayDurableToolCall(ctx, "call-edit", spec, args, *replay.Replay)
	if err != nil || result != wantResult {
		t.Fatalf("model failure replay = (%q, %v), want failed result returned to model", result, err)
	}
}

func TestAssistantRunEventLedgerConversationCallFailureDoesNotAuthorizeEffect(t *testing.T) {
	ctx := context.Background()
	base, scope := newAssistantRunEventLedgerTestStore(t, "run-call-repair")
	messageStore := &failingConversationItemStore{Store: base, failType: projectAssistantConversationToolCall, remaining: 1}
	ledger := newProjectAssistantRunEventLedger(messageStore, scope, "run-call-repair")
	spec := projectAssistantToolSpec{Name: projectToolEditFile, Risk: projectAssistantToolRiskWrite}
	args := map[string]any{"path": "src/App.vue", "oldString": "old", "newString": "new"}

	if _, err := ledger.BeginToolCall(ctx, "call-edit", spec, args); err == nil {
		t.Fatal("BeginToolCall succeeded despite conversation append failure")
	}
	if events := listAssistantRunEventLedgerEvents(t, base, scope, "run-call-repair"); len(events) != 0 {
		t.Fatalf("failed conversation append authorized an effect: %#v", events)
	}

	decision, err := ledger.BeginToolCall(ctx, "call-edit", spec, args)
	if err != nil || !decision.ShouldDispatch() {
		t.Fatalf("repaired BeginToolCall = %#v, err=%v; want dispatch", decision, err)
	}
	if events := listAssistantRunEventLedgerEvents(t, base, scope, "run-call-repair"); len(events) != 1 {
		t.Fatalf("repaired call events = %#v, want one authorization", events)
	}
}

func TestAssistantRunEventLedgerReplayRepairsMissingConversationResult(t *testing.T) {
	ctx := context.Background()
	base, scope := newAssistantRunEventLedgerTestStore(t, "run-result-repair")
	messageStore := &failingConversationItemStore{Store: base, failType: projectAssistantConversationToolResult, remaining: 1}
	ledger := newProjectAssistantRunEventLedger(messageStore, scope, "run-result-repair")
	spec := projectAssistantToolSpec{Name: projectToolReadFile, Risk: projectAssistantToolRiskRead}
	decision, err := ledger.BeginToolCall(ctx, "call-read", spec, map[string]any{"path": "README.md"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.FinishToolCall(ctx, decision.Token, "contents", nil); err == nil {
		t.Fatal("FinishToolCall succeeded despite conversation result append failure")
	}
	if events := listAssistantRunEventLedgerEvents(t, base, scope, "run-result-repair"); len(events) != 2 {
		t.Fatalf("durable ledger did not retain completed outcome: %#v", events)
	}

	repaired, err := ledger.FinishToolCall(ctx, decision.Token, "ignored retry value", errors.New("ignored retry error"))
	if err != nil || repaired.Result != "contents" || repaired.Failed {
		t.Fatalf("repaired result = %#v, err=%v", repaired, err)
	}
	items, err := base.ListAssistantConversationItems(ctx, scope, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[1].Type != projectAssistantConversationToolResult {
		t.Fatalf("repaired conversation items = %#v", items)
	}
}

func TestAssistantRunEventLedgerRejectsConflictingCallID(t *testing.T) {
	ctx := context.Background()
	messageStore, scope := newAssistantRunEventLedgerTestStore(t, "run-conflict")
	ledger := newProjectAssistantRunEventLedger(messageStore, scope, "run-conflict")
	spec := projectAssistantToolSpec{Name: projectToolReadFile, Risk: projectAssistantToolRiskRead}
	if _, err := ledger.BeginToolCall(ctx, "call-1", spec, map[string]any{"path": "src/App.vue"}); err != nil {
		t.Fatalf("BeginToolCall: %v", err)
	}

	_, err := ledger.BeginToolCall(ctx, "call-1", spec, map[string]any{"path": "src/main.ts"})
	if !errors.Is(err, errProjectAssistantRunToolCallIDConflict) {
		t.Fatalf("conflicting call error = %v, want errProjectAssistantRunToolCallIDConflict", err)
	}
	if events := listAssistantRunEventLedgerEvents(t, messageStore, scope, "run-conflict"); len(events) != 1 {
		t.Fatalf("conflict appended events: %#v", events)
	}
}

func TestAssistantRunEventLedgerRetriesIncompleteReadButFailsClosedForEffect(t *testing.T) {
	ctx := context.Background()
	messageStore, scope := newAssistantRunEventLedgerTestStore(t, "run-incomplete")
	readSpec := projectAssistantToolSpec{Name: projectToolReadFile, Risk: projectAssistantToolRiskRead}
	effectSpec := projectAssistantToolSpec{Name: projectToolEditFile, Risk: projectAssistantToolRiskWrite}

	firstRead := newProjectAssistantRunEventLedger(messageStore, scope, "run-incomplete")
	if _, err := firstRead.BeginToolCall(ctx, "call-read", readSpec, map[string]any{"path": "README.md"}); err != nil {
		t.Fatalf("begin first read: %v", err)
	}
	restartedRead := newProjectAssistantRunEventLedger(messageStore, scope, "run-incomplete")
	retry, err := restartedRead.BeginToolCall(ctx, "call-read", readSpec, map[string]any{"path": "README.md"})
	if err != nil {
		t.Fatalf("retry incomplete read: %v", err)
	}
	if !retry.ShouldDispatch() {
		t.Fatalf("incomplete read decision = %#v, want dispatch retry", retry)
	}

	firstEffect := newProjectAssistantRunEventLedger(messageStore, scope, "run-incomplete")
	if _, err := firstEffect.BeginToolCall(ctx, "call-effect", effectSpec, map[string]any{"path": "a", "oldString": "x", "newString": "y"}); err != nil {
		t.Fatalf("begin effect: %v", err)
	}
	restartedEffect := newProjectAssistantRunEventLedger(messageStore, scope, "run-incomplete")
	_, err = restartedEffect.BeginToolCall(ctx, "call-effect", effectSpec, map[string]any{"path": "a", "oldString": "x", "newString": "y"})
	if !errors.Is(err, errProjectAssistantRunIncompleteEffect) {
		t.Fatalf("incomplete effect error = %v, want errProjectAssistantRunIncompleteEffect", err)
	}

	events := listAssistantRunEventLedgerEvents(t, messageStore, scope, "run-incomplete")
	if len(events) != 3 {
		t.Fatalf("events = %#v, want two read attempts and one effect call", events)
	}
	var attempts []int
	for _, event := range events {
		if event.CallID != "call-read" || event.Type != projectAssistantRunToolCallEventType {
			continue
		}
		var payload projectAssistantRunToolCallPayload
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			t.Fatalf("decode call payload: %v", err)
		}
		attempts = append(attempts, payload.Attempt)
	}
	if fmt.Sprint(attempts) != "[1 2]" {
		t.Fatalf("read attempts = %v, want [1 2]", attempts)
	}
}

func TestAssistantRecoveryMiddlewareReplaysSettledOutcomeAndFailsClosedForDanglingEffect(t *testing.T) {
	ctx := context.Background()
	messageStore, scope := newAssistantRunEventLedgerTestStore(t, "run-recovery-patch")
	ledger := newProjectAssistantRunEventLedger(messageStore, scope, "run-recovery-patch")
	readSpec := projectAssistantToolSpec{Name: projectToolReadFile, Risk: projectAssistantToolRiskRead}
	read, err := ledger.BeginToolCall(ctx, "call-read-settled", readSpec, map[string]any{"path": "README.md"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.FinishToolCall(ctx, read.Token, "durable contents", nil); err != nil {
		t.Fatal(err)
	}
	middleware, err := projectEinoAssistantToolCallsMiddleware(ctx, newProjectAssistantRunEventLedger(messageStore, scope, "run-recovery-patch"))
	if err != nil {
		t.Fatal(err)
	}
	_, rewritten, err := middleware.BeforeModelRewriteState(ctx, &adk.ChatModelAgentState{Messages: []*schema.Message{
		schema.AssistantMessage("", []schema.ToolCall{{ID: "call-read-settled", Type: "function", Function: schema.FunctionCall{Name: projectToolReadFile, Arguments: `{"path":"README.md"}`}}}),
	}}, &adk.ModelContext{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rewritten.Messages) != 2 || rewritten.Messages[1].Role != schema.Tool || rewritten.Messages[1].Content != "durable contents" {
		t.Fatalf("patched settled messages = %#v", rewritten.Messages)
	}

	effectSpec := projectAssistantToolSpec{Name: projectToolEditFile, Risk: projectAssistantToolRiskWrite}
	if _, err := ledger.BeginToolCall(ctx, "call-effect-dangling", effectSpec, map[string]any{"path": "src/App.tsx", "oldString": "old", "newString": "new"}); err != nil {
		t.Fatal(err)
	}
	_, _, err = middleware.BeforeModelRewriteState(ctx, &adk.ChatModelAgentState{Messages: []*schema.Message{
		schema.AssistantMessage("", []schema.ToolCall{{ID: "call-effect-dangling", Type: "function", Function: schema.FunctionCall{Name: projectToolEditFile, Arguments: `{}`}}}),
	}}, &adk.ModelContext{})
	if !errors.Is(err, errProjectAssistantRunIncompleteEffect) {
		t.Fatalf("dangling effect recovery error = %v, want incomplete-effect failure", err)
	}
}

func TestAssistantRunEventLedgerCorruptionRemainsSticky(t *testing.T) {
	ctx := context.Background()
	messageStore, scope := newAssistantRunEventLedgerTestStore(t, "run-corrupt")
	arguments, digest, err := projectAssistantRunToolCallDigest(projectToolReadFile, map[string]any{"path": "README.md"})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(projectAssistantRunToolCallPayload{
		Arguments: arguments,
		Read:      true,
		Attempt:   0,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := messageStore.AppendAssistantRunEvent(ctx, scope, store.AssistantRunEvent{
		RunID:      "run-corrupt",
		Type:       projectAssistantRunToolCallEventType,
		CallID:     "call-corrupt",
		ToolName:   projectToolReadFile,
		ArgsDigest: digest,
		Payload:    payload,
	}, 0); err != nil {
		t.Fatal(err)
	}
	ledger := newProjectAssistantRunEventLedger(messageStore, scope, "run-corrupt")
	for attempt := 0; attempt < 2; attempt++ {
		_, err := ledger.BeginToolCall(ctx, "call-new", projectAssistantToolSpec{Name: projectToolReadFile, Risk: projectAssistantToolRiskRead}, map[string]any{"path": "src/App.tsx"})
		if !errors.Is(err, errProjectAssistantRunToolLedgerCorrupt) {
			t.Fatalf("attempt %d error = %v, want sticky ledger corruption", attempt+1, err)
		}
	}
	if events := listAssistantRunEventLedgerEvents(t, messageStore, scope, "run-corrupt"); len(events) != 1 {
		t.Fatalf("corrupt ledger appended later events: %#v", events)
	}
}

func TestAssistantRunEventLedgerFinishesAfterCallerCancellation(t *testing.T) {
	messageStore, scope := newAssistantRunEventLedgerTestStore(t, "run-cancelled-finish")
	ledger := newProjectAssistantRunEventLedger(messageStore, scope, "run-cancelled-finish")
	spec := projectAssistantToolSpec{Name: projectToolReadFile, Risk: projectAssistantToolRiskRead}

	success, err := ledger.BeginToolCall(context.Background(), "call-success", spec, map[string]any{"path": "README.md"})
	if err != nil {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ledger.FinishToolCall(cancelled, success.Token, "read result", nil); err != nil {
		t.Fatalf("persist successful result after cancellation: %v", err)
	}
	if outcome, ok, err := ledger.ToolCallOutcome(cancelled, success.Token.CallID); err != nil || !ok || !outcome.Succeeded() {
		t.Fatalf("consume successful result after cancellation = (%#v, %v, %v), want settled success", outcome, ok, err)
	}

	failure, err := ledger.BeginToolCall(context.Background(), "call-failure", spec, map[string]any{"path": "missing.txt"})
	if err != nil {
		t.Fatal(err)
	}
	cancelled, cancel = context.WithCancel(context.Background())
	cancel()
	backendErr := errors.New("backend read failed")
	if _, err := ledger.FinishToolCall(cancelled, failure.Token, "model-visible failure", backendErr); err != nil {
		t.Fatalf("persist failed result after cancellation: %v", err)
	}

	restarted := newProjectAssistantRunEventLedger(messageStore, scope, "run-cancelled-finish")
	replay, err := restarted.BeginToolCall(context.Background(), "call-failure", spec, map[string]any{"path": "missing.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if replay.Replay == nil || replay.Replay.Error != backendErr.Error() || replay.Replay.Result != "model-visible failure" {
		t.Fatalf("cancelled failure replay = %#v, want exact durable outcome", replay)
	}
}

func TestAssistantRunEventLedgerNormalizesCanceledExecResult(t *testing.T) {
	ctx := context.Background()
	messageStore, scope := newAssistantRunEventLedgerTestStore(t, "run-canceled-exec")
	ledger := newProjectAssistantRunEventLedger(messageStore, scope, "run-canceled-exec")
	spec := projectAssistantToolSpec{Name: projectToolExecCommand, Risk: projectAssistantToolRiskRuntime}
	args := map[string]any{
		"component":      "workspace",
		"argv":           []string{"sh", "-c", "sleep 45; echo SHOULD_NOT_PRINT"},
		"timeoutSeconds": 75,
	}

	decision, err := ledger.BeginToolCall(ctx, "call-canceled-exec", spec, args)
	if err != nil {
		t.Fatal(err)
	}
	canceledCtx, cancel := context.WithCancel(ctx)
	cancel()
	outcome, err := ledger.FinishToolCall(canceledCtx, decision.Token, "partial output must not survive", errors.New("interrupt signal: unbounded internal checkpoint SHOULD_NOT_PRINT"))
	if err != nil {
		t.Fatalf("FinishToolCall cancellation: %v", err)
	}
	if !outcome.Canceled || !outcome.Failed || outcome.Error != "" {
		t.Fatalf("canceled outcome = %#v, want bounded canceled failure without framework error", outcome)
	}
	if len(outcome.Result) > 512 || strings.Contains(outcome.Result, "SHOULD_NOT_PRINT") || strings.Contains(outcome.Result, "interrupt signal") {
		t.Fatalf("canceled result is unbounded or leaked command state: %q", outcome.Result)
	}
	var result projectAssistantExecCommandResult
	if err := json.Unmarshal([]byte(outcome.Result), &result); err != nil {
		t.Fatalf("canceled result is not valid JSON: %v\n%s", err, outcome.Result)
	}
	if result.Status != "outcome_unknown" || result.Component != "workspace" {
		t.Fatalf("canceled result = %#v, want unresolved workspace outcome", result)
	}

	events := listAssistantRunEventLedgerEvents(t, messageStore, scope, "run-canceled-exec")
	if len(events) != 2 {
		t.Fatalf("canceled events = %#v, want call and result", events)
	}
	var payload projectAssistantRunToolResultPayload
	if err := json.Unmarshal(events[1].Payload, &payload); err != nil {
		t.Fatalf("decode canceled event: %v", err)
	}
	if !payload.Canceled || !payload.Failed || payload.Error != "" || payload.Result != outcome.Result {
		t.Fatalf("canceled event payload = %#v, want normalized durable result", payload)
	}

	items, err := messageStore.ListAssistantConversationItems(ctx, scope, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("conversation items = %#v, want call and result", items)
	}
	var toolMessage chatMessage
	if err := json.Unmarshal(items[1].Payload, &toolMessage); err != nil {
		t.Fatalf("canceled conversation payload is invalid JSON: %v", err)
	}
	if toolMessage.Content != outcome.Result {
		t.Fatalf("canceled conversation result = %q, want %q", toolMessage.Content, outcome.Result)
	}

	restarted := newProjectAssistantRunEventLedger(messageStore, scope, "run-canceled-exec")
	replay, err := restarted.BeginToolCall(ctx, "call-canceled-exec", spec, args)
	if err != nil || replay.Replay == nil {
		t.Fatalf("canceled replay = %#v, err=%v", replay, err)
	}
	replayedResult, replayErr := replay.Replay.InvokeResult()
	if replayErr != nil || replayedResult != outcome.Result || !replay.Replay.Canceled {
		t.Fatalf("canceled InvokeResult = (%q, %v), replay=%#v", replayedResult, replayErr, replay.Replay)
	}
}

func TestAssistantRunEventLedgerSerializesConcurrentCASAppends(t *testing.T) {
	ctx := context.Background()
	messageStore, scope := newAssistantRunEventLedgerTestStore(t, "run-concurrent")
	const callCount = 24

	var wg sync.WaitGroup
	errs := make(chan error, callCount)
	for index := 0; index < callCount; index++ {
		index := index
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Separate instances exercise durable CAS refresh as well as each
			// recorder's own mutex.
			ledger := newProjectAssistantRunEventLedger(messageStore, scope, "run-concurrent")
			decision, err := ledger.BeginToolCall(
				ctx,
				fmt.Sprintf("call-%02d", index),
				projectAssistantToolSpec{Name: projectToolReadFile, Risk: projectAssistantToolRiskRead},
				map[string]any{"path": fmt.Sprintf("src/%02d.ts", index)},
			)
			if err != nil {
				errs <- err
				return
			}
			if _, err := ledger.FinishToolCall(ctx, decision.Token, fmt.Sprintf("result-%02d", index), nil); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent ledger call: %v", err)
	}

	events := listAssistantRunEventLedgerEvents(t, messageStore, scope, "run-concurrent")
	if len(events) != callCount*2 {
		t.Fatalf("event count = %d, want %d", len(events), callCount*2)
	}
	callSequence := map[string]int64{}
	for index, event := range events {
		if event.Sequence != int64(index+1) {
			t.Fatalf("event %d sequence = %d, want %d", index, event.Sequence, index+1)
		}
		switch event.Type {
		case projectAssistantRunToolCallEventType:
			callSequence[event.CallID] = event.Sequence
		case projectAssistantRunToolResultEventType:
			if callSequence[event.CallID] == 0 || callSequence[event.CallID] >= event.Sequence {
				t.Fatalf("result event %#v did not follow its call", event)
			}
		default:
			t.Fatalf("unexpected event type %q", event.Type)
		}
	}
}

func newAssistantRunEventLedgerTestStore(t *testing.T, runID string) (*store.MemoryStore, store.Scope) {
	t.Helper()
	messageStore := store.NewMemoryStore()
	scope := store.Scope{
		OrgUUID:       "org-a",
		WorkspaceUUID: "workspace-a",
		ProjectName:   "demo",
		ProjectUID:    "project-a",
	}
	now := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	if err := messageStore.SaveAssistantRun(context.Background(), scope, store.AssistantRun{
		ID:        runID,
		Mode:      store.AssistantRunModeDefault,
		Status:    store.AssistantRunStatusRunning,
		CreatedAt: now,
		UpdatedAt: now,
	}); err != nil {
		t.Fatalf("SaveAssistantRun: %v", err)
	}
	return messageStore, scope
}

func listAssistantRunEventLedgerEvents(
	t *testing.T,
	messageStore store.Store,
	scope store.Scope,
	runID string,
) []store.AssistantRunEvent {
	t.Helper()
	events, err := messageStore.ListAssistantRunEvents(context.Background(), scope, runID, 0, 500)
	if err != nil {
		t.Fatalf("ListAssistantRunEvents: %v", err)
	}
	return events
}

type failingConversationItemStore struct {
	store.Store
	mu        sync.Mutex
	failType  string
	remaining int
}

func (s *failingConversationItemStore) AppendAssistantConversationItem(
	ctx context.Context,
	scope store.Scope,
	item store.AssistantConversationItem,
) (store.AssistantConversationItem, error) {
	s.mu.Lock()
	if item.Type == s.failType && s.remaining > 0 {
		s.remaining--
		s.mu.Unlock()
		return store.AssistantConversationItem{}, errors.New("injected conversation append failure")
	}
	s.mu.Unlock()
	return s.Store.AppendAssistantConversationItem(ctx, scope, item)
}

func TestAssistantRunLedgerSemanticFailureTelemetryPreservesInvocationReplay(t *testing.T) {
	for _, tc := range []struct {
		name, result string
		invokeErr    error
		failed       bool
	}{
		{name: "successful", result: `{"status":"succeeded","exitCode":0}`},
		{name: "nonzero", result: `{"status":"failed","exitCode":7}`, failed: true},
		{name: "stale-zero", result: `{"status":"stale","sourceStale":true,"sourceRevision":19,"exitCode":0}`, failed: true},
		{name: "blocked", result: `{"status":"blocked","blockers":["invalid workdir"]}`, failed: true},
		{name: "uncertain", result: `{"status":"outcome_unknown"}`, failed: true},
		{name: "transport", result: "Tool call failed", invokeErr: errors.New("connection lost"), failed: true},
		{name: "empty-error", invokeErr: errors.New(""), failed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			run := "semantic-" + tc.name
			messages, scope := newAssistantRunEventLedgerTestStore(t, run)
			ledger := newProjectAssistantRunEventLedger(messages, scope, run)
			spec := projectAssistantToolSpec{Name: projectToolExecCommand, Risk: projectAssistantToolRiskRuntime}
			args := map[string]any{"component": "api", "argv": []string{"node", "--version"}}
			decision, err := ledger.BeginToolCall(ctx, "call-1", spec, args)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := ledger.FinishToolCall(ctx, decision.Token, tc.result, tc.invokeErr); err != nil {
				t.Fatal(err)
			}
			events := listAssistantRunEventLedgerEvents(t, messages, scope, run)
			var payload projectAssistantRunToolResultPayload
			for _, event := range events {
				if event.Type == projectAssistantRunToolResultEventType {
					if err := json.Unmarshal(event.Payload, &payload); err != nil {
						t.Fatal(err)
					}
				}
			}
			if payload.Failed != (tc.invokeErr != nil) || (payload.Disposition == projectAssistantToolDispositionFailed) != tc.failed {
				t.Fatalf("invocation/semantic failure dimensions = %+v", payload)
			}
			// A fresh process reads the persisted event. Typed failures must still
			// return their exact JSON without becoming a transport exception.
			restarted := newProjectAssistantRunEventLedger(messages, scope, run)
			replayed, err := restarted.BeginToolCall(ctx, "call-1", spec, args)
			if err != nil || replayed.Replay == nil {
				t.Fatalf("replay = %+v, %v", replayed, err)
			}
			result, invokeErr := replayed.Replay.InvokeResult()
			if result != tc.result || (invokeErr != nil) != (tc.invokeErr != nil) {
				t.Fatalf("replayed invocation = %q, %v", result, invokeErr)
			}
			if invokeErr != nil && invokeErr.Error() != tc.invokeErr.Error() {
				t.Fatal("invocation error changed")
			}
		})
	}
}

func TestAssistantRunToolResultPayloadLegacyFailureMeaning(t *testing.T) {
	for _, legacy := range []string{
		`{"result":"{\"status\":\"blocked\"}","failed":false,"disposition":"failed"}`,
		`{"result":"failure","error":"","failed":true,"disposition":"failed"}`,
	} {
		var payload projectAssistantRunToolResultPayload
		if err := json.Unmarshal([]byte(legacy), &payload); err != nil {
			t.Fatal(err)
		}
		result, err := payload.outcome().InvokeResult()
		if result != payload.Result || (err != nil) != payload.Failed {
			t.Fatalf("legacy replay changed: %q, %v", result, err)
		}
	}
}
