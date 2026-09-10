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
	"testing"
	"time"

	agentsv1alpha1 "github.com/faroshq/provider-agents/apis/v1alpha1"
	"github.com/faroshq/provider-agents/engine"
	"github.com/faroshq/provider-agents/store"
)

func TestTurnProgressTrackerKeepsFailedModelOutputUnclassified(t *testing.T) {
	tracker := newTurnProgressTracker(120)
	tracker.delta("partial answer")
	tracker.assistant(engine.AssistantMessage{
		Content: "partial answer", Complete: false, Duration: 50 * time.Millisecond,
	})

	if got := tracker.durationMS(); got != 170 {
		t.Fatalf("duration after failed model segment = %dms, want 170ms", got)
	}
	if got := tracker.partialText(); got != "partial answer" {
		t.Fatalf("partial output = %q, want to preserve failed output", got)
	}
	if tracker.gotFinal {
		t.Fatal("failed model segment must not be classified as final")
	}

	tracker.assistant(engine.AssistantMessage{
		Content: "planning", HasToolCalls: true, Complete: true, Duration: 80 * time.Millisecond,
	})
	tracker.tool(engine.ToolEvent{Duration: 25 * time.Millisecond})
	tracker.assistant(engine.AssistantMessage{
		Content: "answer", Complete: true, Duration: 90 * time.Millisecond,
	})

	if got := tracker.durationMS(); got != 365 {
		t.Fatalf("cumulative active duration = %dms, want 365ms", got)
	}
	if !tracker.gotFinal || tracker.final != "answer" {
		t.Fatalf("final response = %q (gotFinal=%v), want answer", tracker.final, tracker.gotFinal)
	}
	if got := tracker.partialText(); got != "" {
		t.Fatalf("classified output left in partial buffer: %q", got)
	}
}

func TestTurnMetadataNeverAdvertisesUnknownPhaseAsSuccess(t *testing.T) {
	if got := turnStatusForRunPhase(store.RunPhase("future")); got != "failed" {
		t.Fatalf("unknown run phase status = %q, want failed", got)
	}
}

func TestSummarizeKeepsElapsedAndWorkedDurationsIndependent(t *testing.T) {
	started := time.Date(2026, 9, 10, 10, 0, 0, 0, time.UTC)
	finished := started.Add(30 * time.Minute)
	worked := int64(3400)
	got := summarize(store.Run{
		ID: "run-1", AgentName: "scout", Trigger: "chat", Phase: store.RunPhaseSucceeded,
		CreatedAt: started, StartedAt: &started, FinishedAt: &finished, WorkedDurationMS: &worked,
	})
	if got.DurationMS != int64((30 * time.Minute).Milliseconds()) {
		t.Fatalf("elapsed duration = %dms, want 1800000ms", got.DurationMS)
	}
	if got.WorkedDurationMS == nil || *got.WorkedDurationMS != worked {
		t.Fatalf("worked duration = %v, want pointer to %d", got.WorkedDurationMS, worked)
	}

	unknown := summarize(store.Run{ID: "legacy", AgentName: "scout", Trigger: "chat", Phase: store.RunPhaseFailed})
	if unknown.WorkedDurationMS != nil {
		t.Fatalf("legacy worked duration = %v, want nil", unknown.WorkedDurationMS)
	}
	zero := int64(0)
	measuredZero := summarize(store.Run{ID: "zero", AgentName: "scout", Trigger: "chat", Phase: store.RunPhaseSucceeded, WorkedDurationMS: &zero})
	if measuredZero.WorkedDurationMS == nil || *measuredZero.WorkedDurationMS != 0 {
		t.Fatalf("measured zero worked duration = %v, want pointer to zero", measuredZero.WorkedDurationMS)
	}
}

func TestTurnProgressFinalFallbackKeepsToolLimitNoticeSeparateFromCommentary(t *testing.T) {
	tracker := newTurnProgressTracker(0)
	tracker.assistant(engine.AssistantMessage{Content: "planning", HasToolCalls: true, Complete: true})

	const notice = "[stopped: reached the tool-call limit for one turn]"
	if got := tracker.finalText(notice); got != notice {
		t.Fatalf("final fallback = %q, want standalone limit notice", got)
	}
	if got := tracker.finalText("unexpected final answer"); got != "unexpected final answer" {
		t.Fatalf("fallback should be selected when no final boundary was observed, got %q", got)
	}

	// executeTask feeds this value to the persisted final row. Verify the
	// presentation answer remains the standalone notice rather than replaying
	// commentary a second time in the answer body.
	ctx := context.Background()
	st := store.NewMemoryStore()
	s := &Server{store: st}
	scope := store.Scope{OrgUUID: "org", WorkspaceUUID: "ws", AgentName: "scout"}
	startedAt := time.Date(2026, 9, 10, 10, 0, 0, 0, time.UTC)
	s.appendTurnFinal(ctx, scope, taskRun{RunID: "run-1"}, "chat", startedAt, startedAt.Add(time.Second), tracker, tracker.finalText(notice))
	page, err := st.ListMessages(ctx, scope, "chat", 10, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].Content != notice {
		t.Fatalf("persisted final content = %+v, want standalone notice", page.Items)
	}
}

func TestTurnProgressTrackerAccumulatesActiveTimeAcrossRepeatedCheckpoints(t *testing.T) {
	tracker := newTurnProgressTracker(0)
	tracker.assistant(engine.AssistantMessage{Content: "plan", HasToolCalls: true, Complete: true, Duration: 80 * time.Millisecond})
	tracker.tool(engine.ToolEvent{Duration: 20 * time.Millisecond})

	// Approval pauses and provider restarts hand the persisted accumulator to a
	// fresh tracker. Round-tripping the checkpoint models the durable boundary
	// without adding wall-clock time for either pause.
	for iter, segment := range []struct {
		model time.Duration
		tool  time.Duration
	}{
		{model: 70 * time.Millisecond, tool: 30 * time.Millisecond},
		{model: 40 * time.Millisecond, tool: 10 * time.Millisecond},
	} {
		payload, err := json.Marshal(runCheckpoint{
			Engine:           engine.Checkpoint{Iter: iter + 1},
			WorkedDurationMS: tracker.durationMS(),
		})
		if err != nil {
			t.Fatal(err)
		}
		var checkpoint runCheckpoint
		if err := json.Unmarshal(payload, &checkpoint); err != nil {
			t.Fatal(err)
		}
		tracker = newTurnProgressTracker(checkpoint.WorkedDurationMS)
		tracker.assistant(engine.AssistantMessage{Content: "plan", HasToolCalls: true, Complete: true, Duration: segment.model})
		tracker.tool(engine.ToolEvent{Duration: segment.tool})
	}

	tracker = newTurnProgressTracker(tracker.durationMS())
	tracker.assistant(engine.AssistantMessage{Content: "final answer", Complete: true, Duration: 90 * time.Millisecond})
	if got := tracker.durationMS(); got != 340 {
		t.Fatalf("repeated-resume active duration = %dms, want 340ms", got)
	}
	if !tracker.gotFinal || tracker.final != "final answer" {
		t.Fatalf("resumed final response = %q (gotFinal=%v)", tracker.final, tracker.gotFinal)
	}
}

func TestAppendTurnTerminalPersistsCanceledPartialOutputAndTiming(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemoryStore()
	s := &Server{store: st}
	scope := store.Scope{OrgUUID: "org", WorkspaceUUID: "ws", AgentName: "scout"}
	run := taskRun{RunID: "run-1"}
	tracker := newTurnProgressTracker(0)
	tracker.delta("partial answer")
	tracker.assistant(engine.AssistantMessage{
		Content: "partial answer", Complete: false, Duration: 73 * time.Millisecond,
	})
	startedAt := time.Date(2026, 9, 10, 10, 0, 0, 0, time.FixedZone("PDT", -7*60*60))
	createdAt := startedAt.Add(time.Second)

	s.appendTurnTerminal(ctx, scope, run, "chat", startedAt, createdAt, tracker, "aborted", "", "cancelled by user")
	page, err := st.ListMessages(ctx, scope, "chat", 10, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("terminal messages = %d, want one", len(page.Items))
	}
	message := page.Items[0]
	if message.Content != "partial answer" {
		t.Fatalf("canceled terminal content = %q, want partial answer", message.Content)
	}
	if got := message.Metadata["turnStatus"]; got != "aborted" {
		t.Fatalf("turnStatus = %#v, want aborted", got)
	}
	if got := message.Metadata["durationMS"]; got != int64(73) {
		t.Fatalf("durationMS = %#v, want 73", got)
	}
	if got := message.Metadata["turnError"]; got != "cancelled by user" {
		t.Fatalf("turnError = %#v, want cancellation reason", got)
	}
	if got := message.Metadata["startedAt"]; got != "2026-09-10T17:00:00Z" {
		t.Fatalf("startedAt = %#v, want UTC server timestamp", got)
	}
}

func TestTrackerForStoredRestoresCheckpointTimingForTerminalFallback(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemoryStore()
	s := &Server{store: st}
	scope := store.Scope{OrgUUID: "org", WorkspaceUUID: "ws", AgentName: "scout"}
	startedAt := time.Date(2026, 9, 10, 10, 0, 0, 0, time.UTC)
	checkpoint, err := json.Marshal(runCheckpoint{
		Engine:           engine.Checkpoint{Iter: 4},
		WorkedDurationMS: 1300,
	})
	if err != nil {
		t.Fatal(err)
	}
	run := store.Run{
		ID: "run-1", AgentName: "scout", SessionID: "chat", Phase: store.RunPhaseRunning,
		Checkpoint: checkpoint, CreatedAt: startedAt, StartedAt: &startedAt,
	}
	tracker := trackerForStored(run)
	s.appendTurnTerminal(ctx, scope, taskRunForStored(run), run.SessionID, startedAt, startedAt.Add(time.Second), tracker, "failed", "", "resume failed")

	page, err := st.ListMessages(ctx, scope, "chat", 10, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("terminal messages = %d, want one", len(page.Items))
	}
	if got := page.Items[0].Metadata["durationMS"]; got != int64(1300) {
		t.Fatalf("restored durationMS = %#v, want 1300", got)
	}
}

type recordingStore struct {
	store.Store
	appendErrs      []error
	appendDeadlines []bool
}

func (s *recordingStore) AppendMessage(ctx context.Context, scope store.Scope, message store.Message) error {
	_, hasDeadline := ctx.Deadline()
	s.appendErrs = append(s.appendErrs, ctx.Err())
	s.appendDeadlines = append(s.appendDeadlines, hasDeadline)
	return s.Store.AppendMessage(ctx, scope, message)
}

func TestRunCallbacksPersistCanceledToolWithDetachedBoundedContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	base := store.NewMemoryStore()
	recorded := &recordingStore{Store: base}
	s := &Server{store: recorded}
	scope := store.Scope{OrgUUID: "org", WorkspaceUUID: "ws", AgentName: "scout"}
	agent := &agentsv1alpha1.Agent{}
	agent.Name = "scout"
	callbacks := s.runCallbacks(ctx, taskRun{
		RunID: "run-1", Scope: scope, Agent: agent,
	}, "chat", time.Now().UTC(), newTurnProgressTracker(0))

	callbacks.OnTool(engine.ToolEvent{
		ID: "tool-1", Name: "repo_search", Args: `{}`, Result: "partial result", Duration: 25 * time.Millisecond,
	})
	if len(recorded.appendErrs) != 1 {
		t.Fatalf("AppendMessage calls = %d, want one tool row", len(recorded.appendErrs))
	}
	if err := recorded.appendErrs[0]; err != nil {
		t.Fatalf("tool persistence inherited canceled context: %v", err)
	}
	if !recorded.appendDeadlines[0] {
		t.Fatal("tool persistence context has no bounded deadline")
	}
	page, err := base.ListMessages(context.Background(), scope, "chat", 10, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].Role != "tool" || page.Items[0].Content != "partial result" {
		t.Fatalf("persisted tool row = %+v, want canceled tool evidence", page.Items)
	}
}
