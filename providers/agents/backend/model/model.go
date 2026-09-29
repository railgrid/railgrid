// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

// Package model runs a turn in this process: an Eino chat model driven by the
// engine's tool-call loop.
//
// It is the first implementation of the backend seam and, until a harness
// backend exists, the only one. Everything Eino-shaped stops here: the seam
// above it speaks conversation, tools, progress and outcome, and the engine
// below it speaks the same vocabulary over a chat model.
package model

import (
	"context"
	"encoding/json"
	"fmt"

	einomodel "github.com/cloudwego/eino/components/model"

	"github.com/railgrid/provider-agents/backend"
	"github.com/railgrid/provider-agents/engine"
	"github.com/railgrid/provider-agents/llm"
)

// Backend executes a turn as a model call loop in this process.
//
// It is built per turn because what it needs is resolved per turn: the chat
// model comes from the agent's credential for that turn's purpose, and the model
// id is what prices the tokens it spends.
type Backend struct {
	engine *engine.Engine
	model  einomodel.BaseChatModel
	// modelName is the model id the turn runs on. Cost is computed here, not by
	// the provider, because pricing is the backend's knowledge: a harness is told
	// what it was charged, an in-process loop counts tokens against a catalog.
	modelName string
	// compact replaces an over-budget history. It is the MODEL backend's concern
	// and not the seam's: a harness holds its own session and compacts it the way
	// that product compacts things, so there is nothing here for the provider to
	// do on its behalf. nil means an over-budget turn fails rather than silently
	// dropping the oldest tool results.
	compact engine.ContextCompactionFunc
}

var _ backend.Backend = (*Backend)(nil)

// New builds a Backend for one turn over model, whose id is modelName (empty
// when it could not be resolved, in which case the turn costs zero rather than a
// fabricated number).
func New(e *engine.Engine, model einomodel.BaseChatModel, modelName string, compact engine.ContextCompactionFunc) *Backend {
	return &Backend{engine: e, model: model, modelName: modelName, compact: compact}
}

// Turn answers a fresh conversation.
func (b *Backend) Turn(ctx context.Context, _ *backend.Run, in backend.Input, sink backend.EventSink) (backend.Outcome, error) {
	res, err := b.engine.StreamTurnWithTools(ctx, b.model, in.Messages, in.Tools, b.turnConfig(in.Limits), callbacks(sink))
	return b.outcome(ctx, res, err, backend.Tokens{})
}

// Continue picks a parked turn back up from the state it left behind.
func (b *Backend) Continue(ctx context.Context, _ *backend.Run, answer backend.Answer, sink backend.EventSink) (backend.Outcome, error) {
	var ck engine.Checkpoint
	if err := json.Unmarshal(answer.State, &ck); err != nil {
		return backend.Outcome{Status: backend.StatusFailed}, fmt.Errorf("reading the turn's resume state: %w", err)
	}
	// The verdict, when there is one. An undecided resume is a run picked up
	// after its process died: its state holds no gated call, so the engine has
	// nothing to apply the (absent) decision to.
	res, err := b.engine.ResumeTurnWithTools(ctx, b.model, ck, answer.Tools, b.turnConfig(answer.Limits),
		answer.Decided && answer.Approved, answer.Note, callbacks(sink))
	// The resumed loop's usage accumulator starts from the checkpoint, so what
	// the run had already spent before it parked is the part not to bill again.
	return b.outcome(ctx, res, err, ck.Usage)
}

// Cancel is a no-op: this backend executes in the caller's process, so the run
// context the provider cancels — and the durable flag it surfaces through
// EventSink.Aborted — are the whole mechanism. A backend that left a turn
// running elsewhere is what this method exists for.
func (b *Backend) Cancel(context.Context, *backend.Run) error { return nil }

// outcome maps one engine result onto the seam. prior is what the run had spent
// before this turn began — zero for a fresh turn, the checkpoint's accumulator
// for a resumed one.
func (b *Backend) outcome(ctx context.Context, res engine.Result, err error, prior backend.Tokens) (backend.Outcome, error) {
	if err != nil {
		// A turn stopped on purpose is not a fault. The provider records it as
		// aborted rather than failed, and the classification belongs here because
		// this is what knows whether the turn was interrupted or broke: the
		// context it ran under is the one that was cancelled.
		status := backend.StatusFailed
		if ctx.Err() != nil {
			status = backend.StatusCancelled
		}
		return backend.Outcome{Status: status}, err
	}

	usage := backend.Usage{
		Total:  b.cost(res.Usage),
		Billed: b.cost(billable(res.Usage, prior)),
	}
	if res.Interrupt != nil {
		state, merr := json.Marshal(res.Interrupt.Checkpoint)
		if merr != nil {
			// Without resumable state the gate would strand the run: no approval
			// could ever continue it. Fail the turn instead, which at least ends
			// it somewhere a person can see.
			return backend.Outcome{Status: backend.StatusFailed, Usage: usage},
				fmt.Errorf("recording the turn's resume state: %w", merr)
		}
		return backend.Outcome{
			Status: backend.StatusParked,
			Text:   res.Content,
			Usage:  usage,
			Parked: &backend.Parked{
				Tool: res.Interrupt.Tool, Args: res.Interrupt.Args,
				RequestID: res.Interrupt.RequestID, State: state,
			},
		}, nil
	}

	output, sources := backend.SplitSources(res.Content)
	return backend.Outcome{
		Status: backend.StatusCompleted,
		Text:   res.Content, Output: output, Sources: sources,
		Final: res.FinalContent,
		Usage: usage,
	}, nil
}

// cost prices a token count against the model the turn ran on. An unknown model
// costs zero rather than a guess.
func (b *Backend) cost(t backend.Tokens) backend.Cost {
	return backend.Cost{Tokens: t, CostMicros: llm.CostMicros(b.modelName, t.InputTokens, t.OutputTokens)}
}

// billable is what this turn added on top of what the run had already spent.
// Clamped at zero: a provider that reports usage inconsistently across a resume
// must not produce a credit.
func billable(total, prior backend.Tokens) backend.Tokens {
	return backend.Tokens{
		InputTokens:  max(total.InputTokens-prior.InputTokens, 0),
		OutputTokens: max(total.OutputTokens-prior.OutputTokens, 0),
	}
}

func (b *Backend) turnConfig(l backend.Limits) engine.TurnConfig {
	return engine.TurnConfig{
		MaxIters:            l.MaxToolTurns,
		ContextBudgetTokens: l.ContextBudgetTokens,
		ContextCompactor:    b.compact,
		CheckpointEvery:     l.CheckpointEvery,
	}
}

// callbacks wires the engine's progress hooks onto the sink. No field is
// translated: the engine speaks the seam's types (see the engine package
// comment), so this is only about which name calls which.
func callbacks(sink backend.EventSink) engine.Callbacks {
	return engine.Callbacks{
		OnDelta:            sink.Delta,
		OnAssistantMessage: sink.Assistant,
		OnToolStart:        sink.ToolStart,
		OnTool:             sink.ToolEnd,
		OnCheckpoint: func(ck engine.Checkpoint) {
			state, err := json.Marshal(ck)
			if err != nil {
				// A checkpoint that cannot be serialized costs recoverability,
				// which is strictly better than failing a working turn over it.
				return
			}
			sink.Checkpoint(state)
		},
		CheckAbort: sink.Aborted,
	}
}
