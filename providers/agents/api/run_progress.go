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
	"strings"
	"time"

	"github.com/google/uuid"

	agentsv1alpha1 "github.com/faroshq/provider-agents/apis/v1alpha1"
	"github.com/faroshq/provider-agents/engine"
	"github.com/faroshq/provider-agents/store"
)

// progressPersistTimeout bounds writes that must survive cancellation (for
// example, a tool result or terminal error). The detached parent preserves the
// evidence after the run context is canceled, while the timeout keeps a stalled
// store from retaining a goroutine indefinitely.
const progressPersistTimeout = 10 * time.Second

// boundedPersistContext detaches a durable presentation write from a canceled
// run while retaining a finite deadline for the store operation.
func boundedPersistContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), progressPersistTimeout)
}

// appendProgressMessage persists a transcript presentation row using the same
// bounded cancellation policy for commentary, tool, final, and terminal rows.
func (s *Server) appendProgressMessage(ctx context.Context, scope store.Scope, message store.Message) {
	persistCtx, cancel := boundedPersistContext(ctx)
	defer cancel()
	_ = s.store.AppendMessage(persistCtx, scope, message)
}

// turnProgressTracker accumulates only work that completed inside the current
// model/tool callback. It is deliberately separate from wall-clock run time:
// approval and recovery pauses must not be presented as work, and a checkpoint
// can carry this accumulator across a resume without replaying completed
// segments.
type turnProgressTracker struct {
	worked time.Duration
	// workedKnown distinguishes a measured zero from a run for which no
	// model/tool timing evidence has been observed yet. This matters for old
	// runs and setup failures, where elapsed wall time is not work evidence.
	workedKnown bool
	partial     strings.Builder
	final       string
	gotFinal    bool
}

func newTurnProgressTracker(workedMS int64) *turnProgressTracker {
	return newTurnProgressTrackerState(workedMS, workedMS > 0)
}

func newTurnProgressTrackerState(workedMS int64, known bool) *turnProgressTracker {
	if workedMS < 0 {
		workedMS = 0
	}
	return &turnProgressTracker{worked: time.Duration(workedMS) * time.Millisecond, workedKnown: known}
}

func (t *turnProgressTracker) delta(value string) {
	if value != "" {
		t.partial.WriteString(value)
	}
}

func (t *turnProgressTracker) assistant(message engine.AssistantMessage) {
	if t != nil {
		// The engine emits one callback for every response attempt, including a
		// zero-duration attempt. Seeing that callback is authoritative evidence
		// that measured work was observed, even when it measured zero.
		t.workedKnown = true
	}
	if message.Duration > 0 {
		t.worked += message.Duration
	}
	if !message.Complete {
		// Deltas from a failed stream remain unclassified and are rendered by the
		// terminal marker as partial output. They are never promoted to a final
		// answer merely because the model callback ended.
		return
	}
	if message.HasToolCalls {
		// The completed commentary response has its own persisted row. Any
		// streamed deltas are now classified, so the unclassified buffer can be
		// reused for the next response.
		t.partial.Reset()
		return
	}
	t.final = message.Content
	t.gotFinal = true
	t.partial.Reset()
}

func (t *turnProgressTracker) tool(event engine.ToolEvent) {
	if t != nil {
		t.workedKnown = true
	}
	if event.Duration > 0 {
		t.worked += event.Duration
	}
}

func (t *turnProgressTracker) durationMS() int64 {
	if t == nil || t.worked <= 0 {
		return 0
	}
	return t.worked.Milliseconds()
}

// workedDurationMS returns the measured accumulator when timing evidence is
// known. A nil result is intentional for historical/setup-only runs; callers
// must not turn their wall-clock timestamps into a fabricated work duration.
func (t *turnProgressTracker) workedDurationMS() *int64 {
	if t == nil || !t.workedKnown {
		return nil
	}
	value := t.durationMS()
	return &value
}

func (t *turnProgressTracker) partialText() string {
	if t == nil {
		return ""
	}
	return t.partial.String()
}

// finalText returns the last model response when a complete assistant boundary
// was observed. A loop that stops at its tool-call limit returns an explicit
// standalone notice without a final callback; retain that server-produced text
// instead of persisting an empty answer in that case.
func (t *turnProgressTracker) finalText(fallback string) string {
	if t != nil && t.gotFinal {
		return t.final
	}
	return fallback
}

// trackerForStored rebuilds the presentation accumulator from a checkpoint.
// Invalid or absent checkpoint metadata is treated as zero rather than trusting
// arbitrary JSON, while valid worked duration survives setup/recovery failures.
func trackerForStored(run store.Run) *turnProgressTracker {
	if len(run.Checkpoint) > 0 {
		var checkpoint runCheckpoint
		if err := json.Unmarshal(run.Checkpoint, &checkpoint); err == nil {
			if workedMS, known := checkpointWorkedDuration(run.Checkpoint, checkpoint); known {
				return newTurnProgressTrackerState(workedMS, true)
			}
		}
	}
	if run.WorkedDurationMS != nil {
		return newTurnProgressTrackerState(*run.WorkedDurationMS, true)
	}
	return newTurnProgressTracker(0)
}

// checkpointWorkedDuration recognizes both the current field and an explicit
// zero in a checkpoint JSON object. Older checkpoints have no field and must
// remain unknown rather than being upgraded to measured zero.
func checkpointWorkedDuration(raw []byte, checkpoint runCheckpoint) (int64, bool) {
	if checkpoint.WorkedDurationMS != 0 {
		return checkpoint.WorkedDurationMS, true
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err == nil {
		if _, ok := fields["workedDurationMS"]; ok {
			return 0, true
		}
	}
	return 0, false
}

// turnMetadata is the persisted presentation boundary for an assistant turn.
// The metadata is intentionally flat because old stores already persist an
// arbitrary JSON object here and providers can read it without a migration.
func turnMetadata(phase, status string, startedAt time.Time, durationMS, segmentDurationMS int64, turnError string) map[string]any {
	meta := map[string]any{
		"turnPhase":  phase,
		"turnStatus": status,
		"startedAt":  startedAt.UTC().Format(time.RFC3339Nano),
	}
	if durationMS > 0 {
		meta["durationMS"] = durationMS
	}
	if segmentDurationMS > 0 {
		meta["segmentDurationMS"] = segmentDurationMS
	}
	if turnError != "" {
		meta["turnError"] = safeTruncate(turnError, 4*1024)
	}
	return meta
}

func turnStatusForRunPhase(phase store.RunPhase) string {
	switch phase {
	case store.RunPhasePending:
		return "pending"
	case store.RunPhaseRunning:
		return "running"
	case store.RunPhasePendingApproval:
		return "waiting"
	case store.RunPhaseSucceeded:
		return "completed"
	case store.RunPhaseAborted:
		return "aborted"
	case store.RunPhaseFailed:
		return "failed"
	default:
		// Unknown phases are never presented as a successful turn.
		return "failed"
	}
}

func taskRunForStored(run store.Run) taskRun {
	agent := &agentsv1alpha1.Agent{}
	agent.Name = run.AgentName
	return taskRun{RunID: run.ID, SessionID: run.SessionID, Agent: agent}
}

func (s *Server) appendTurnTerminal(ctx context.Context, scope store.Scope, run taskRun, sessionID string, startedAt, createdAt time.Time, tracker *turnProgressTracker, status string, content, turnError string) {
	if tracker == nil {
		tracker = newTurnProgressTracker(0)
	}
	if content == "" {
		content = tracker.partialText()
	}
	agentName := ""
	if run.Agent != nil {
		agentName = run.Agent.Name
	}
	s.appendProgressMessage(ctx, scope, store.Message{
		ID: uuid.NewString(), AgentName: agentName, SessionID: sessionID, RunID: run.RunID,
		Role: "assistant", Content: safeTruncate(content, maxStoredOutput),
		Metadata:  turnMetadata("terminal", status, startedAt, tracker.durationMS(), 0, turnError),
		CreatedAt: createdAt,
	})
}

func (s *Server) appendTurnFinal(ctx context.Context, scope store.Scope, run taskRun, sessionID string, startedAt, createdAt time.Time, tracker *turnProgressTracker, content string) {
	if tracker == nil {
		tracker = newTurnProgressTracker(0)
	}
	agentName := ""
	if run.Agent != nil {
		agentName = run.Agent.Name
	}
	s.appendProgressMessage(ctx, scope, store.Message{
		ID: uuid.NewString(), AgentName: agentName, SessionID: sessionID, RunID: run.RunID,
		Role: "assistant", Content: safeTruncate(content, maxStoredOutput),
		Metadata:  turnMetadata("final", "completed", startedAt, tracker.durationMS(), 0, ""),
		CreatedAt: createdAt,
	})
}
