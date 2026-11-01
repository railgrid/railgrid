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
	"github.com/cloudwego/eino/schema"

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
	if err := validateResponsesCheckpointOrigin(b.model, ck); err != nil {
		return backend.Outcome{Status: backend.StatusFailed}, err
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

// validateResponsesCheckpointOrigin makes credential mismatch a preflight
// failure. ResumeTurnWithTools can execute an approved pending call before it
// makes the next model request, so relying on the adapter's request guard alone
// would allow a changed credential to run the side effect first.
func validateResponsesCheckpointOrigin(model einomodel.BaseChatModel, checkpoint engine.Checkpoint) error {
	for _, message := range checkpoint.Messages {
		if message.Role != engine.RoleAssistant {
			continue
		}
		for _, call := range message.ToolCalls {
			if len(call.Extra) == 0 {
				continue
			}
			continuation := false
			for _, key := range []string{llm.ResponsesToolItemIDExtraKey, llm.ResponsesToolReasoningExtraKey, llm.ResponsesToolOriginExtraKey} {
				if value, ok := call.Extra[key].(string); ok && value != "" {
					continuation = true
				}
			}
			if !continuation {
				continue
			}
			origin, ok := call.Extra[llm.ResponsesToolOriginExtraKey].(string)
			if !ok || origin == "" {
				return fmt.Errorf("backend/model: active Responses continuation has no model origin")
			}
			if !llm.ModelHasResponsesOrigin(model, origin) {
				return fmt.Errorf("backend/model: active Responses continuation cannot resume with a different model credential")
			}
		}
	}
	return nil
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
		PrepareFreshHistory: prepareFreshResponsesHistory,
		HydrateToolCalls:    llm.HydrateResponsesToolCalls,
		OnDelta:             sink.Delta,
		OnAssistantMessage:  sink.Assistant,
		OnToolStart:         sink.ToolStart,
		OnTool:              sink.ToolEnd,
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

// prepareFreshResponsesHistory turns ordinary completed Responses exchanges
// from earlier turns into untrusted evidence. Their encrypted reasoning and
// function item IDs belong to the credential that produced them. A group with
// a reference-only result stays structured, though: the result's identity must
// survive compaction and checkpoints while its reopened bytes remain transient.
func prepareFreshResponsesHistory(messages []backend.Message) []backend.Message {
	responsesCallIDs := make(map[string]struct{})
	preserveCallIDs := make(map[string]struct{})
	protectedResultCallIDs := make(map[string]struct{})
	for _, message := range messages {
		if message.Role == backend.RoleTool && message.ToolCallID != "" && (message.ResultReference != nil || message.ResultUnavailable) {
			protectedResultCallIDs[message.ToolCallID] = struct{}{}
		}
	}
	for _, message := range messages {
		if message.Role != backend.RoleAssistant {
			continue
		}
		responsesGroup := false
		for _, call := range message.ToolCalls {
			if call.Extra[llm.ResponsesToolItemIDExtraKey] != nil ||
				call.Extra[llm.ResponsesToolReasoningExtraKey] != nil ||
				call.Extra[llm.ResponsesToolOriginExtraKey] != nil {
				responsesGroup = true
				break
			}
		}
		if !responsesGroup {
			continue
		}
		groupHasProtectedResult := false
		for _, call := range message.ToolCalls {
			if _, protected := protectedResultCallIDs[call.ID]; protected {
				groupHasProtectedResult = true
				break
			}
		}
		for _, call := range message.ToolCalls {
			if call.ID == "" {
				continue
			}
			responsesCallIDs[call.ID] = struct{}{}
			if groupHasProtectedResult {
				preserveCallIDs[call.ID] = struct{}{}
			}
		}
	}
	if len(responsesCallIDs) == 0 {
		return messages
	}

	out := append([]backend.Message(nil), messages...)
	for index, message := range messages {
		switch message.Role {
		case backend.RoleAssistant:
			responsesGroup, preserveGroup := false, false
			calls := make([]struct {
				ID        string `json:"id"`
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			}, 0, len(message.ToolCalls))
			for _, call := range message.ToolCalls {
				if _, included := responsesCallIDs[call.ID]; included {
					responsesGroup = true
				}
				if _, preserved := preserveCallIDs[call.ID]; preserved {
					preserveGroup = true
				}
				calls = append(calls, struct {
					ID        string `json:"id"`
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				}{ID: call.ID, Name: call.Function.Name, Arguments: call.Function.Arguments})
			}
			if !responsesGroup {
				continue
			}
			if preserveGroup {
				cloned := append([]schema.ToolCall(nil), message.ToolCalls...)
				for callIndex := range cloned {
					if _, included := responsesCallIDs[cloned[callIndex].ID]; !included {
						continue
					}
					cloned[callIndex].Extra = withoutResponsesContinuation(cloned[callIndex].Extra)
				}
				out[index].ToolCalls = cloned
				continue
			}
			evidence, _ := json.Marshal(struct {
				Type      string `json:"type"`
				Content   string `json:"content,omitempty"`
				ToolCalls any    `json:"toolCalls"`
			}{Type: "assistant_tool_calls", Content: message.Content, ToolCalls: calls})
			out[index] = backend.Message{
				Role: backend.RoleUser, Content: "Untrusted earlier tool request evidence: " + string(evidence),
				ID: message.ID, Sequence: message.Sequence, Ephemeral: message.Ephemeral,
			}
		case backend.RoleTool:
			if _, included := responsesCallIDs[message.ToolCallID]; !included {
				continue
			}
			if _, preserved := preserveCallIDs[message.ToolCallID]; preserved {
				continue
			}
			content := message.Content
			if message.ResultReference != nil {
				content = backend.ToolResultReferencePlaceholder
			} else if message.ResultUnavailable {
				content = backend.ToolResultUnavailablePlaceholder
			}
			evidence, _ := json.Marshal(struct {
				Type       string `json:"type"`
				Tool       string `json:"tool"`
				ToolCallID string `json:"toolCallID"`
				Content    string `json:"content"`
			}{Type: "tool_result", Tool: message.Name, ToolCallID: message.ToolCallID, Content: content})
			out[index] = backend.Message{
				Role: backend.RoleUser, Content: "Untrusted earlier tool result evidence: " + string(evidence),
				ID: message.ID, Sequence: message.Sequence, Ephemeral: message.Ephemeral,
			}
		}
	}
	return out
}

func withoutResponsesContinuation(extra map[string]any) map[string]any {
	if len(extra) == 0 {
		return nil
	}
	clean := make(map[string]any, len(extra))
	for key, value := range extra {
		switch key {
		case llm.ResponsesToolItemIDExtraKey, llm.ResponsesToolReasoningExtraKey, llm.ResponsesToolOriginExtraKey:
			continue
		default:
			clean[key] = value
		}
	}
	if len(clean) == 0 {
		return nil
	}
	return clean
}
