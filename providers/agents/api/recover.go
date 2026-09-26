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
	"encoding/json"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/railgrid/provider-agents/engine"
	"github.com/railgrid/provider-agents/llm"
	"github.com/railgrid/provider-agents/store"
)

// Run recovery. Runs execute in this process, so a deploy or a crash used to
// leave rows stuck in Running forever: nothing swept them, and the only cure was
// a human noticing and force-cancelling (POST /api/runs/{id}/cancel).
//
// Two halves fix it:
//
//   - Periodic checkpoints (checkpointRecorder, wired into every run) persist a
//     resumable snapshot every few tool-call rounds while the phase stays
//     Running. The mechanism is the one approval gates already used; this just
//     takes it on a timer instead of only when a human is asked.
//   - A sweep (sweepStaleRuns, run at startup and on the background executor's
//     discovery/recovery tick) finds
//     runs that are Running or Pending, are not executing on THIS replica, and
//     have not been touched for a while. With a checkpoint they are re-queued for
//     resume; without one they are failed honestly.
//
// PendingApproval is deliberately NOT swept: such a run is waiting for a person,
// not stranded, and no amount of time makes it stale.
const (
	// checkpointEveryIterations is how many tool-call rounds pass between
	// checkpoints. Every round would double the write volume of a tool-heavy run
	// for little gain; every fourth bounds the lost work to a few tool calls.
	checkpointEveryIterations = 4

	// maxRecoveryAttempts caps how many times a run may be recovered. A run that
	// crashes the provider would otherwise be resumed forever, taking the
	// provider down with it on every restart — a crash loop that looks like an
	// outage. After this it is failed and left for a human.
	// maxRecoveryAttempts caps how many times a run may be recovered. It is the
	// store-side counterpart of the Run reconciler's MaxClaims: that one bounds
	// how often a run may be PICKED UP, this one how often it may be RESUMED,
	// and a run that crashes whatever touches it has to be stopped by both.
	maxRecoveryAttempts = 3
)

// turnContextBudget is the wire-conversation budget for one turn: a fraction of
// the model's window, leaving room for the reply and for the estimate being a
// heuristic. Feeds engine.TurnConfig.ContextBudgetTokens.
func turnContextBudget(modelName string) int {
	return llm.ContextWindowFor(modelName) * turnContextBudgetPct / 100
}

// turnContextBudgetPct triggers structured history compaction before the model
// window fills, reserving room for output and token-estimation error. The same
// pressure check applies to fresh requests and later tool rounds.
const turnContextBudgetPct = 80

// checkpointRecorder returns the engine callback that persists a mid-run
// checkpoint. The run stays Running — this is a recovery point, not a pause.
//
// Best-effort: a failed write logs and the run continues. Losing a checkpoint
// costs recoverability, which is strictly better than failing a working run over
// a transient database error.
func (s *Server) checkpointRecorder(ctx context.Context, run taskRun, sessionID string, worked ...func() int64) func(engine.Checkpoint) {
	scope, agentName := run.Scope, run.Agent.Name
	runID := run.RunID
	sourceName, notifyChannel := run.SourceName, run.NotifyChannel
	return func(ck engine.Checkpoint) {
		workedMS := int64(0)
		if len(worked) > 0 && worked[0] != nil {
			workedMS = worked[0]()
		}
		payload, err := json.Marshal(runCheckpoint{
			Engine: ck, SourceName: sourceName, NotifyChannel: notifyChannel,
			WorkedDurationMS: workedMS,
		})
		if err != nil {
			return
		}
		stored, err := s.store.GetRun(ctx, scope, runID)
		if err != nil {
			return
		}
		// Only a Running run gets a recovery checkpoint. If something else already
		// moved the phase (a cancel, an approval gate), leave it alone.
		if stored.Phase != store.RunPhaseRunning {
			return
		}
		stored.Checkpoint = payload
		if len(worked) > 0 && worked[0] != nil {
			value := workedMS
			if value < 0 {
				value = 0
			}
			// A checkpoint callback is an explicit provider measurement boundary,
			// so persist a non-nil zero as measured zero too. Historical rows that
			// predate this callback keep a nil value.
			stored.WorkedDurationMS = &value
		}
		stored.UpdatedAt = time.Now().UTC()
		if err := s.saveRun(ctx, scope, stored); err != nil {
			log.Printf("recovery: checkpointing run %s (agent %s, session %s): %v", runID, agentName, sessionID, err)
		}
	}
}

// cancelCheckInterval throttles the durable-cancel read: the engine asks before
// every model round and every tool call, and a burst of quick tool calls
// should not turn into a burst of row reads.
const cancelCheckInterval = time.Second

// cancelRequestedError is what a run ends with when a cancel arrived through
// the store rather than through its context (see Server.cancelRun). It reads
// as context.Canceled to errors.Is so every existing terminal path — Aborted
// phase, "Stopped before it finished" in the chat — treats it as the person's
// decision it is, not as a crash.
type cancelRequestedError struct{}

func (cancelRequestedError) Error() string        { return "cancelled by user" }
func (cancelRequestedError) Is(target error) bool { return target == context.Canceled }

// cancelCheck returns the engine.Callbacks.CheckAbort hook for one run: it
// reads the run row's CancelRequested flag (throttled) and, when set, cancels
// the run's own context — so the terminal bookkeeping in executeTask/resumeRun
// records Aborted exactly as a local cancel does — and ends the turn.
//
// Best-effort on the read: a store hiccup must not fail a working run, so an
// unreadable row means "not cancelled" until the next check.
func (s *Server) cancelCheck(scope store.Scope, runID string) func(context.Context) error {
	var last time.Time
	return func(ctx context.Context) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if now := time.Now(); now.Sub(last) < cancelCheckInterval {
			return nil
		} else {
			last = now
		}
		stored, err := s.store.GetRun(ctx, scope, runID)
		if err != nil || !stored.CancelRequested {
			return nil
		}
		s.liveRuns.cancel(runID)
		return cancelRequestedError{}
	}
}

// recoveryRunner resumes a recovered run. The sweep cannot build tenant access on
// its own — that lives in the background executor, which owns the virtual
// workspace — so StartBackground installs this and the sweep calls it. Nil means
// resume is unavailable and stale runs are only failed, never resumed.
type recoveryRunner func(ctx context.Context, scoped store.ScopedRun, clusterID string) error

// recoveryNotifier tells whoever was waiting that a run is not coming back.
//
// Closing a stranded run silently is the worst outcome available: someone asked a
// question in a chat, the process died before it could answer, and without this
// they wait forever with no way to tell "still thinking" from "dead". Injected
// alongside recoveryRunner because delivery needs the same tenant access.
type recoveryNotifier func(ctx context.Context, scoped store.ScopedRun, clusterID, text string) error

// recoverRun resumes one stranded run, or fails it when it cannot be resumed.
// Reports whether it was resumed.
//
// It is called by the Run reconciler, once, for a run whose claim went stale —
// not by a sweep. The policy below is unchanged; what went away is the timer
// that used to scan every non-terminal run in every tenant looking for work
// this function could already describe object by object.
func (s *Server) recoverRun(ctx context.Context, sr store.ScopedRun, resume recoveryRunner, notify recoveryNotifier) bool {
	run, scope := sr.Run, sr.Scope
	close := func(phase store.RunPhase, reason string, tell bool) bool {
		now := time.Now().UTC()
		startedAt := run.CreatedAt
		if run.StartedAt != nil {
			startedAt = *run.StartedAt
		}
		if startedAt.IsZero() {
			startedAt = now
		}
		persistCtx, cancelPersist := boundedPersistContext(ctx)
		defer cancelPersist()
		tracker := trackerForStored(run)
		s.appendTurnTerminal(persistCtx, scope, taskRunForStored(run), run.SessionID, startedAt, now, tracker, turnStatusForRunPhase(phase), "", reason)
		s.finishRun(persistCtx, scope, run.ID, runOutcome{Phase: phase, Message: reason, WorkedDurationMS: tracker.workedDurationMS()}, now)
		s.publishRunEvent(scope, runEvent{ID: run.ID, Agent: run.AgentName, Trigger: run.Trigger, ParentRunID: run.ParentRunID, Phase: phase})
		if tell {
			s.reportStrandedRun(ctx, sr, notify, reason)
		}
		return false
	}
	fail := func(reason string) bool { return close(store.RunPhaseFailed, reason, true) }

	switch {
	case runSettled(run.Phase):
		// Settled is settled. The phase filter used to live in the sweep that
		// selected which runs to look at; now that this is called per object,
		// it belongs here — a run waiting on a human for a week is not
		// stranded, and failing it would throw away an approval somebody is
		// about to give.
		return false
	case run.CancelRequested:
		// Someone asked for this run to stop while no replica could act on it
		// (queued on a process that died, or the flag landed after the crash).
		// Honour the request rather than resuming work nobody wants; the person
		// who cancelled is not told twice.
		return close(store.RunPhaseAborted, "cancelled by user", false)
	case scope.OrgUUID == "" || scope.WorkspaceUUID == "":
		// A run whose scope was never recorded cannot be addressed again.
		return fail("the provider restarted while this run was in progress, and its workspace could not be resolved to resume it")
	case len(run.Checkpoint) == 0:
		return fail("the provider restarted while this run was in progress; it had not reached a checkpoint, so it could not be resumed")
	case run.Attempt >= maxRecoveryAttempts:
		return fail("the provider restarted while this run was in progress; it has already been resumed " +
			strconv.Itoa(run.Attempt) + " times without finishing, so it will not be retried again")
	case resume == nil:
		return fail("the provider restarted while this run was in progress, and background execution is not configured, so it could not be resumed")
	}

	clusterID, ok, err := s.store.FindClusterForScope(ctx, scope.OrgUUID, scope.WorkspaceUUID)
	if err != nil || !ok {
		return fail("the provider restarted while this run was in progress, and its workspace mapping is missing, so it could not be resumed")
	}
	if err := resume(ctx, sr, clusterID); err != nil {
		log.Printf("recovery: resuming run %s (agent %s): %v", run.ID, run.AgentName, err)
		return fail("the provider restarted while this run was in progress, and resuming it failed: " + err.Error())
	}
	return true
}

// reportStrandedRun tells the chat or channel a dead run was answering that it is
// not coming. Best-effort and deliberately quiet on failure: the run is already
// closed, and a delivery problem must not stop the sweep working through the rest
// of the batch.
//
// Only runs that recorded a delivery target are reported. A spawned worker or a
// delegated child has none — it answers its parent in memory, and the parent's own
// failure is what the user hears about.
func (s *Server) reportStrandedRun(ctx context.Context, sr store.ScopedRun, notify recoveryNotifier, reason string) {
	if notify == nil || sr.Run.Delivery == nil {
		return
	}
	d := sr.Run.Delivery
	if d.SourceName == "" && d.NotifyChannel == "" {
		return
	}
	clusterID, ok, err := s.store.FindClusterForScope(ctx, sr.Scope.OrgUUID, sr.Scope.WorkspaceUUID)
	if err != nil || !ok {
		return
	}
	// Say what happened and what to do about it. "Failed" alone would leave the
	// reader guessing whether re-asking is safe.
	text := "⚠️ I lost that request when this service restarted, and it did not finish. " +
		"Nothing was completed, so it is safe to ask again."
	if ask := strings.TrimSpace(sr.Run.Input); ask != "" {
		text += "\n\nYou asked: " + safeTruncate(strings.Join(strings.Fields(ask), " "), 300)
	}
	if err := notify(ctx, sr, clusterID, text); err != nil {
		log.Printf("recovery: telling %s about stranded run %s: %v", d.SourceName, sr.Run.ID, err)
	}
	_ = reason
}
