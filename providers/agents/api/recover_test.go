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
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	agentsv1alpha1 "github.com/faroshq/provider-agents/apis/v1alpha1"
	"github.com/faroshq/provider-agents/engine"
	"github.com/faroshq/provider-agents/store"
)

// recoverFixture is a server with a mapped tenant, so FindClusterForScope can
// resolve a workspace the way a real deployment does.
type recoverFixture struct {
	s     *Server
	scope store.Scope
}

func newRecoverFixture(t *testing.T) *recoverFixture {
	t.Helper()
	f := &recoverFixture{
		s:     &Server{store: store.NewMemoryStore(), events: newEventBus(), liveRuns: newRunRegistry()},
		scope: store.Scope{OrgUUID: "o", WorkspaceUUID: "w", AgentName: "scout"},
	}
	if err := f.s.store.SaveTenantRef(context.Background(), "cluster-1", store.TenantRef{
		OrgUUID: "o", WorkspaceUUID: "w", UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	return f
}

// seedRun writes a run with the given phase and age. checkpointed adds a
// recovery checkpoint of the kind checkpointRecorder writes.
func (f *recoverFixture) seedRun(t *testing.T, id string, phase store.RunPhase, age time.Duration, checkpointed bool, attempt int) store.Run {
	t.Helper()
	at := time.Now().UTC().Add(-age)
	run := store.Run{
		ID: id, AgentName: "scout", SessionID: "chat", Trigger: agentsv1alpha1.RunTriggerChat,
		Phase: phase, Attempt: attempt, Input: "do the thing", CreatedAt: at, UpdatedAt: at, StartedAt: &at,
	}
	if checkpointed {
		payload, err := json.Marshal(runCheckpoint{Engine: engine.Checkpoint{
			Messages: []engine.CheckpointMessage{{Role: "user", Content: "do the thing"}},
			Iter:     4,
		}})
		if err != nil {
			t.Fatal(err)
		}
		run.Checkpoint = payload
	}
	if err := f.s.store.SaveRun(context.Background(), f.scope, run); err != nil {
		t.Fatal(err)
	}
	return run
}

func (f *recoverFixture) phaseOf(t *testing.T, id string) store.Run {
	t.Helper()
	run, err := f.s.store.GetRun(context.Background(), f.scope, id)
	if err != nil {
		t.Fatal(err)
	}
	return run
}

func TestSweepFailsRunsWithNoCheckpoint(t *testing.T) {
	ctx := context.Background()
	f := newRecoverFixture(t)
	f.seedRun(t, "r1", store.RunPhaseRunning, time.Hour, false, 0)

	f.s.sweepStaleRuns(ctx, func(context.Context, store.ScopedRun, string) error {
		t.Fatal("a run with no checkpoint must not be resumed")
		return nil
	}, nil)

	got := f.phaseOf(t, "r1")
	if got.Phase != store.RunPhaseFailed {
		t.Fatalf("phase = %s, want Failed", got.Phase)
	}
	// The message has to explain itself: this run's owner sees it in the UI with
	// no other clue about what happened.
	if !strings.Contains(got.Message, "restarted") {
		t.Fatalf("message = %q, should say the provider restarted", got.Message)
	}
	if got.FinishedAt == nil {
		t.Fatal("a failed run must be stamped finished, or it stays stale forever")
	}
}

func TestSweepResumesCheckpointedRuns(t *testing.T) {
	ctx := context.Background()
	f := newRecoverFixture(t)
	f.seedRun(t, "r1", store.RunPhaseRunning, time.Hour, true, 0)

	var gotCluster string
	var gotRun string
	f.s.sweepStaleRuns(ctx, func(_ context.Context, sr store.ScopedRun, clusterID string) error {
		gotCluster, gotRun = clusterID, sr.Run.ID
		return nil
	}, nil)

	if gotRun != "r1" {
		t.Fatalf("resumed %q, want r1", gotRun)
	}
	// The resume path needs the logical cluster, which only the reverse tenant
	// mapping can supply.
	if gotCluster != "cluster-1" {
		t.Fatalf("clusterID = %q, want cluster-1 from the tenant mapping", gotCluster)
	}
	if got := f.phaseOf(t, "r1"); got.Phase == store.RunPhaseFailed {
		t.Fatal("a resumable run must not be failed")
	}
}

func TestSweepLeavesFreshAndLiveRunsAlone(t *testing.T) {
	ctx := context.Background()
	f := newRecoverFixture(t)

	// Recently updated: still within the grace period.
	f.seedRun(t, "fresh", store.RunPhaseRunning, time.Minute, true, 0)
	// Old, but executing on this replica — a run can sit in one slow tool call
	// for far longer than the grace period.
	f.seedRun(t, "live", store.RunPhaseRunning, time.Hour, true, 0)
	f.s.liveRuns.register("live", func() {})
	defer f.s.liveRuns.unregister("live")

	resumed := map[string]bool{}
	f.s.sweepStaleRuns(ctx, func(_ context.Context, sr store.ScopedRun, _ string) error {
		resumed[sr.Run.ID] = true
		return nil
	}, nil)

	if resumed["fresh"] {
		t.Fatal("a run inside the grace period is not stale")
	}
	if resumed["live"] {
		t.Fatal("a run executing on this replica must be left alone")
	}
	for _, id := range []string{"fresh", "live"} {
		if got := f.phaseOf(t, id); got.Phase != store.RunPhaseRunning {
			t.Fatalf("run %s moved to %s; it should still be Running", id, got.Phase)
		}
	}
}

func TestSweepIgnoresPendingApproval(t *testing.T) {
	ctx := context.Background()
	f := newRecoverFixture(t)
	// Waiting on a human for a week is normal, not stranded.
	f.seedRun(t, "gate", store.RunPhasePendingApproval, 7*24*time.Hour, true, 0)

	f.s.sweepStaleRuns(ctx, func(context.Context, store.ScopedRun, string) error {
		t.Fatal("an approval-gated run must never be auto-resumed — it is waiting for a person")
		return nil
	}, nil)

	if got := f.phaseOf(t, "gate"); got.Phase != store.RunPhasePendingApproval {
		t.Fatalf("phase = %s, want PendingApproval untouched", got.Phase)
	}
}

func TestSweepGivesUpAfterRepeatedAttempts(t *testing.T) {
	ctx := context.Background()
	f := newRecoverFixture(t)
	f.seedRun(t, "loop", store.RunPhaseRunning, time.Hour, true, maxRecoveryAttempts)

	f.s.sweepStaleRuns(ctx, func(context.Context, store.ScopedRun, string) error {
		t.Fatal("a run that has exhausted its attempts must not be resumed again")
		return nil
	}, nil)

	got := f.phaseOf(t, "loop")
	if got.Phase != store.RunPhaseFailed {
		t.Fatalf("phase = %s, want Failed", got.Phase)
	}
	// A run that crashes the provider on every resume would otherwise take the
	// provider down on every restart.
	if !strings.Contains(got.Message, "will not be retried") {
		t.Fatalf("message = %q, should say it gave up", got.Message)
	}
}

func TestSweepFailsWhenResumeIsUnavailableOrFails(t *testing.T) {
	ctx := context.Background()

	t.Run("no background execution configured", func(t *testing.T) {
		f := newRecoverFixture(t)
		f.seedRun(t, "r1", store.RunPhaseRunning, time.Hour, true, 0)
		f.s.sweepStaleRuns(ctx, nil, nil)
		got := f.phaseOf(t, "r1")
		if got.Phase != store.RunPhaseFailed {
			t.Fatalf("phase = %s, want Failed", got.Phase)
		}
		if !strings.Contains(got.Message, "background execution is not configured") {
			t.Fatalf("message = %q, should name the missing capability", got.Message)
		}
	})

	t.Run("the resume itself fails", func(t *testing.T) {
		f := newRecoverFixture(t)
		f.seedRun(t, "r1", store.RunPhaseRunning, time.Hour, true, 0)
		f.s.sweepStaleRuns(ctx, func(context.Context, store.ScopedRun, string) error {
			return errors.New("virtual workspace unreachable")
		}, nil)
		got := f.phaseOf(t, "r1")
		if got.Phase != store.RunPhaseFailed {
			t.Fatalf("phase = %s, want Failed", got.Phase)
		}
		if !strings.Contains(got.Message, "virtual workspace unreachable") {
			t.Fatalf("message = %q, should carry the underlying reason", got.Message)
		}
	})

	t.Run("the workspace has no cluster mapping", func(t *testing.T) {
		f := newRecoverFixture(t)
		// A scope with no recorded tenant mapping cannot be addressed again.
		other := store.Scope{OrgUUID: "o2", WorkspaceUUID: "w2", AgentName: "scout"}
		at := time.Now().UTC().Add(-time.Hour)
		payload, _ := json.Marshal(runCheckpoint{Engine: engine.Checkpoint{
			Messages: []engine.CheckpointMessage{{Role: "user", Content: "x"}}}})
		if err := f.s.store.SaveRun(ctx, other, store.Run{
			ID: "r2", AgentName: "scout", Phase: store.RunPhaseRunning, Checkpoint: payload,
			CreatedAt: at, UpdatedAt: at,
		}); err != nil {
			t.Fatal(err)
		}
		f.s.sweepStaleRuns(ctx, func(context.Context, store.ScopedRun, string) error {
			t.Fatal("must not attempt a resume without a cluster")
			return nil
		}, nil)
		run, err := f.s.store.GetRun(ctx, other, "r2")
		if err != nil {
			t.Fatal(err)
		}
		if run.Phase != store.RunPhaseFailed || !strings.Contains(run.Message, "mapping is missing") {
			t.Fatalf("phase = %s message = %q", run.Phase, run.Message)
		}
	})
}

func TestSweepFailsPendingRunsThatNeverStarted(t *testing.T) {
	ctx := context.Background()
	f := newRecoverFixture(t)
	// startDetachedRun pre-writes Pending before its goroutine begins; a crash in
	// that window leaves a run that never ran and has no checkpoint.
	f.seedRun(t, "never", store.RunPhasePending, time.Hour, false, 0)

	f.s.sweepStaleRuns(ctx, func(context.Context, store.ScopedRun, string) error {
		t.Fatal("a Pending run has no checkpoint to resume from")
		return nil
	}, nil)

	if got := f.phaseOf(t, "never"); got.Phase != store.RunPhaseFailed {
		t.Fatalf("phase = %s, want Failed", got.Phase)
	}
}

func TestCheckpointRecorderPersistsWhileRunning(t *testing.T) {
	ctx := context.Background()
	f := newRecoverFixture(t)
	f.seedRun(t, "r1", store.RunPhaseRunning, 0, false, 0)

	agent := &agentsv1alpha1.Agent{ObjectMeta: metav1.ObjectMeta{Name: "scout"}}
	run := taskRun{Scope: f.scope, Agent: agent, RunID: "r1", SourceName: "daily", NotifyChannel: "ops"}
	record := f.s.checkpointRecorder(ctx, run, "chat")

	record(engine.Checkpoint{
		Messages: []engine.CheckpointMessage{{Role: "user", Content: "hello"}},
		Iter:     4,
	})

	got := f.phaseOf(t, "r1")
	if len(got.Checkpoint) == 0 {
		t.Fatal("expected a checkpoint to be persisted")
	}
	// The phase must NOT move: this is a recovery point, not a pause.
	if got.Phase != store.RunPhaseRunning {
		t.Fatalf("phase = %s, want Running", got.Phase)
	}
	var ck runCheckpoint
	if err := json.Unmarshal(got.Checkpoint, &ck); err != nil {
		t.Fatal(err)
	}
	if ck.Engine.Iter != 4 || len(ck.Engine.Messages) != 1 {
		t.Fatalf("checkpoint = %+v", ck.Engine)
	}
	// Delivery context has to survive, or a recovered background run finishes
	// and has nowhere to report.
	if ck.SourceName != "daily" || ck.NotifyChannel != "ops" {
		t.Fatalf("checkpoint lost its delivery target: source=%q channel=%q", ck.SourceName, ck.NotifyChannel)
	}
	// A recovery checkpoint records no gated call.
	if ck.Tool != "" || ck.InboxID != "" {
		t.Fatalf("recovery checkpoint should carry no approval state, got tool=%q inbox=%q", ck.Tool, ck.InboxID)
	}

	t.Run("a run that already left Running is not overwritten", func(t *testing.T) {
		stored := f.phaseOf(t, "r1")
		stored.Phase = store.RunPhasePendingApproval
		stored.Checkpoint = []byte(`{"tool":"github__merge"}`)
		if err := f.s.store.SaveRun(ctx, f.scope, stored); err != nil {
			t.Fatal(err)
		}
		record(engine.Checkpoint{Messages: []engine.CheckpointMessage{{Role: "user", Content: "later"}}, Iter: 8})
		after := f.phaseOf(t, "r1")
		if !strings.Contains(string(after.Checkpoint), "github__merge") {
			t.Fatal("a recovery checkpoint must not clobber an approval checkpoint")
		}
	})
}

func TestListUnfinishedRunsOrdersAndFilters(t *testing.T) {
	ctx := context.Background()
	f := newRecoverFixture(t)
	f.seedRun(t, "old", store.RunPhaseRunning, 3*time.Hour, true, 0)
	f.seedRun(t, "newer", store.RunPhaseRunning, 2*time.Hour, true, 0)
	f.seedRun(t, "done", store.RunPhaseSucceeded, 3*time.Hour, false, 0)
	f.seedRun(t, "recent", store.RunPhaseRunning, time.Second, true, 0)

	got, err := f.s.store.ListUnfinishedRuns(ctx,
		[]store.RunPhase{store.RunPhaseRunning, store.RunPhasePending},
		time.Now().UTC().Add(-time.Hour), 50)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, sr := range got {
		ids = append(ids, sr.Run.ID)
		// Each row carries its own scope: the sweep has no request context.
		if sr.Scope.OrgUUID != "o" || sr.Scope.WorkspaceUUID != "w" {
			t.Fatalf("run %s came back with scope %+v", sr.Run.ID, sr.Scope)
		}
	}
	// Oldest first, so the longest-stranded run is handled before the batch limit
	// is reached; terminal and recent runs excluded.
	if strings.Join(ids, ",") != "old,newer" {
		t.Fatalf("ids = %v, want [old newer]", ids)
	}
}

// The failure mode that prompted this: a user asks a question in a chat, the
// provider restarts mid-run, and the sweep closes the run — silently. From the
// chat it is indistinguishable from the agent still thinking, forever.
func TestSweepTellsTheWaiterTheRunDied(t *testing.T) {
	ctx := context.Background()

	seedWithDelivery := func(t *testing.T, f *recoverFixture, id string, d *store.RunDelivery) {
		t.Helper()
		at := time.Now().UTC().Add(-time.Hour)
		if err := f.s.store.SaveRun(ctx, f.scope, store.Run{
			ID: id, AgentName: "scout", SessionID: "discord:discord-dev:123",
			Trigger: agentsv1alpha1.RunTriggerChannel, Phase: store.RunPhaseRunning,
			Input: "research vertical gardens in lithuania", Delivery: d,
			CreatedAt: at, UpdatedAt: at,
		}); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("a channel run is reported back to its chat", func(t *testing.T) {
		f := newRecoverFixture(t)
		seedWithDelivery(t, f, "r1", &store.RunDelivery{
			SourceName: "discord-dev", ReplyTarget: "1474867054824919153", Kind: "channel",
		})

		var gotCluster, gotText string
		var gotRun string
		f.s.sweepStaleRuns(ctx, nil, func(_ context.Context, sr store.ScopedRun, clusterID, text string) error {
			gotCluster, gotText, gotRun = clusterID, text, sr.Run.ID
			return nil
		})

		if gotRun != "r1" {
			t.Fatalf("notified about %q, want r1", gotRun)
		}
		if gotCluster != "cluster-1" {
			t.Fatalf("clusterID = %q, want the mapped cluster", gotCluster)
		}
		// It has to say what happened AND whether re-asking is safe — "failed"
		// alone leaves the reader guessing.
		if !strings.Contains(gotText, "restarted") {
			t.Fatalf("message should say why: %q", gotText)
		}
		if !strings.Contains(gotText, "safe to ask again") {
			t.Fatalf("message should say what to do next: %q", gotText)
		}
		// Echo the question so a busy chat has context for the apology.
		if !strings.Contains(gotText, "vertical gardens") {
			t.Fatalf("message should quote the request: %q", gotText)
		}
		if f.phaseOf(t, "r1").Phase != store.RunPhaseFailed {
			t.Fatal("the run should still be closed out")
		}
	})

	t.Run("a run with nowhere to report is not reported", func(t *testing.T) {
		f := newRecoverFixture(t)
		// A spawned worker answers its parent in memory and has no channel; the
		// parent's own failure is what the user hears about.
		seedWithDelivery(t, f, "r1", nil)
		f.s.sweepStaleRuns(ctx, nil, func(context.Context, store.ScopedRun, string, string) error {
			t.Fatal("nothing was waiting on this run in a channel")
			return nil
		})
	})

	t.Run("a delivery failure does not stop the sweep", func(t *testing.T) {
		f := newRecoverFixture(t)
		seedWithDelivery(t, f, "r1", &store.RunDelivery{SourceName: "discord-dev", Kind: "channel"})
		seedWithDelivery(t, f, "r2", &store.RunDelivery{SourceName: "discord-dev", Kind: "channel"})
		f.s.sweepStaleRuns(ctx, nil, func(context.Context, store.ScopedRun, string, string) error {
			return errors.New("discord unreachable")
		})
		// Both runs are still closed: the run record is the source of truth, and a
		// chat that cannot be reached must not leave rows stuck Running forever.
		for _, id := range []string{"r1", "r2"} {
			if f.phaseOf(t, id).Phase != store.RunPhaseFailed {
				t.Fatalf("run %s should be Failed despite the delivery error", id)
			}
		}
	})
}

// Cancelling a run from the UI used to surface whatever HTTP call happened to be
// in flight — "Post https://api.openai.com/v1/chat/completions: context
// canceled" — into the user's chat, which reads like the agent crashed rather
// than like the stop they just asked for.
func TestChannelErrorText(t *testing.T) {
	t.Run("a cancel reads as a stop", func(t *testing.T) {
		// Wrapped the way it actually arrives: engine wraps the SDK, which wraps
		// the HTTP error, which wraps context.Canceled.
		err := fmt.Errorf("engine: start stream: %w",
			fmt.Errorf(`Post "https://api.openai.com/v1/chat/completions": %w`, context.Canceled))
		got := channelErrorText(err)
		if !strings.Contains(got, "Stopped") {
			t.Fatalf("got %q, want a plain stop message", got)
		}
		// None of the internals leak.
		for _, leak := range []string{"context canceled", "openai.com", "engine:"} {
			if strings.Contains(got, leak) {
				t.Fatalf("message leaks %q: %s", leak, got)
			}
		}
	})

	t.Run("a timeout says what to do about it", func(t *testing.T) {
		got := channelErrorText(fmt.Errorf("engine: %w", context.DeadlineExceeded))
		if !strings.Contains(got, "time limit") {
			t.Fatalf("got %q", got)
		}
	})

	t.Run("a real error is passed through", func(t *testing.T) {
		// Hiding genuine failures would be worse than an ugly message.
		got := channelErrorText(errors.New("model credential is invalid"))
		if !strings.Contains(got, "model credential is invalid") {
			t.Fatalf("got %q, want the real reason", got)
		}
	})
}
