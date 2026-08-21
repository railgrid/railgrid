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
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/faroshq/provider-agents/engine"
	"github.com/faroshq/provider-agents/llm"
	"github.com/faroshq/provider-agents/store"
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
//   - A sweep (sweepStaleRuns, run at startup and on the scheduler tick) finds
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

	// staleRunGrace is how long a run may go without a status write before the
	// sweep treats it as abandoned. It must comfortably exceed the gap between
	// checkpoints of a healthy long-running run, or the sweep would fight live
	// work; a run that is genuinely alive rewrites its row every few rounds.
	staleRunGrace = 15 * time.Minute

	// maxRecoveryAttempts caps how many times a run may be recovered. A run that
	// crashes the provider would otherwise be resumed forever, taking the
	// provider down with it on every restart — a crash loop that looks like an
	// outage. After this it is failed and left for a human.
	maxRecoveryAttempts = 3

	// sweepBatch bounds one sweep pass so a large backlog is worked through over
	// several ticks instead of in one burst.
	sweepBatch = 50
)

// turnContextBudget is the wire-conversation budget for one turn: a fraction of
// the model's window, leaving room for the reply and for the estimate being a
// heuristic. Feeds engine.TurnConfig.ContextBudgetTokens.
func turnContextBudget(modelName string) int {
	return llm.ContextWindowFor(modelName) * turnContextBudgetPct / 100
}

// turnContextBudgetPct is deliberately looser than compaction's threshold:
// clipping a tool observation mid-turn loses information the model asked for, so
// it should be the last line of defense, after session compaction has had its
// chance.
const turnContextBudgetPct = 80

// checkpointRecorder returns the engine callback that persists a mid-run
// checkpoint. The run stays Running — this is a recovery point, not a pause.
//
// Best-effort: a failed write logs and the run continues. Losing a checkpoint
// costs recoverability, which is strictly better than failing a working run over
// a transient database error.
func (s *Server) checkpointRecorder(ctx context.Context, run taskRun, sessionID string) func(engine.Checkpoint) {
	scope, agentName := run.Scope, run.Agent.Name
	runID := run.RunID
	sourceName, notifyChannel := run.SourceName, run.NotifyChannel
	return func(ck engine.Checkpoint) {
		payload, err := json.Marshal(runCheckpoint{
			Engine: ck, SourceName: sourceName, NotifyChannel: notifyChannel,
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
		stored.UpdatedAt = time.Now().UTC()
		if err := s.store.SaveRun(ctx, scope, stored); err != nil {
			log.Printf("recovery: checkpointing run %s (agent %s, session %s): %v", runID, agentName, sessionID, err)
		}
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

// sweepStaleRuns handles runs left non-terminal by a crashed or restarted
// replica. Safe to call repeatedly; safe to call while other replicas are
// working, because a run executing here is skipped and ClaimRun settles races
// between replicas.
func (s *Server) sweepStaleRuns(ctx context.Context, resume recoveryRunner, notify recoveryNotifier) {
	cutoff := time.Now().UTC().Add(-staleRunGrace)
	stale, err := s.store.ListUnfinishedRuns(ctx,
		[]store.RunPhase{store.RunPhaseRunning, store.RunPhasePending}, cutoff, sweepBatch)
	if err != nil {
		log.Printf("recovery: listing unfinished runs: %v", err)
		return
	}
	var resumed, failed int
	for _, sr := range stale {
		// Executing here: not stale, however old the last status write is (a run
		// can sit inside one slow tool call for a long time).
		if s.liveRuns.has(sr.Run.ID) {
			continue
		}
		if s.recoverRun(ctx, sr, resume, notify) {
			resumed++
		} else {
			failed++
		}
	}
	if resumed+failed > 0 {
		log.Printf("recovery: swept %d stranded run(s) — %d resumed, %d failed", resumed+failed, resumed, failed)
	}
}

// recoverRun resumes one stranded run, or fails it when it cannot be resumed.
// Reports whether it was resumed.
func (s *Server) recoverRun(ctx context.Context, sr store.ScopedRun, resume recoveryRunner, notify recoveryNotifier) bool {
	run, scope := sr.Run, sr.Scope
	fail := func(reason string) bool {
		if won := s.finishRun(ctx, scope, run.ID, runOutcome{Phase: store.RunPhaseFailed, Message: reason}, time.Now().UTC()); won {
			s.publishRunEvent(scope, runEvent{ID: run.ID, Agent: run.AgentName, Trigger: run.Trigger, ParentRunID: run.ParentRunID, Phase: store.RunPhaseFailed})
			s.reportStrandedRun(ctx, sr, notify, reason)
		}
		return false
	}

	switch {
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
