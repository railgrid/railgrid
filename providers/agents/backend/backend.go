// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

// Package backend is the seam between the run this provider owns and the turn
// something else executes.
//
// The provider owns the RUN: the durable queue (the Run object), the
// transcript, budgets and usage accounting, the approval inbox, channel
// delivery, cancellation and recovery. A Backend owns ONE TURN — asking a
// model, running the tools it calls, and reporting what came of it.
//
// Nothing about HOW a turn is executed crosses this line. The vocabulary here
// names conversation, tools, progress and outcome — which is what lets a second
// backend, a Claude Code or Codex harness on an edge, plug in without the
// provider's lifecycle learning that there are two. The single exception is
// Message.ToolCalls, which carries the structured assistant/tool pairing the
// durable transcript replays; see Message. The in-process implementation lives in
// backend/model.
//
// The engine package — today's in-process tool loop — declares these types by
// alias rather than defining its own, so the toolset the provider assembles and
// the progress it records cross the seam without being copied field by field.
// That is deliberate: the seam is the vocabulary, and the engine is one speaker
// of it, not the other way round.
package backend

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/cloudwego/eino/schema"
)

// Role constants for the conversation a turn runs over.
const (
	RoleSystem    = "system"
	RoleUser      = "user"
	RoleAssistant = "assistant"
	RoleTool      = "tool"
)

// Message is a role-tagged turn in the conversation a turn is given.
//
// ToolCalls is the one field that carries an Eino type across this line, and it
// is here because the provider's durable transcript replays structured
// assistant/tool pairs: narrowing the row at the seam would silently drop the
// pairing a model-backed replay depends on. A backend that does not run an
// in-process tool loop — a harness — ignores these fields, and nothing about
// HOW a turn executes is expressed by them.
type Message struct {
	Role       string            `json:"role"`
	Content    string            `json:"content,omitempty"`
	ToolCalls  []schema.ToolCall `json:"toolCalls,omitempty"`
	ToolCallID string            `json:"toolCallID,omitempty"`
	Name       string            `json:"name,omitempty"`
	ID         string            `json:"id,omitempty"`
	Sequence   int64             `json:"sequence,omitempty"`
	// Ephemeral marks generated context that has no durable transcript identity,
	// such as a tool-returned image follow-up. It must not become a durable user
	// request or enter a session replacement checkpoint.
	Ephemeral bool `json:"ephemeral,omitempty"`
}

// Param describes one tool parameter (a pragmatic subset of JSON schema).
type Param struct {
	Type     string // "string" | "integer" | "number" | "boolean" | "array" | "object"
	Desc     string
	Required bool
	Enum     []string
}

// Tool is one callable the turn may use. Exactly one of Params or JSONSchema
// describes the arguments: Params for the built-in families, JSONSchema (a raw
// JSON-schema document) for pass-through tools like MCP.
//
// The provider assembles the toolset — policy, approval gating and audit are
// already wrapped around each executor by the time a Tool crosses the seam — so
// a Backend calls what it is given and interprets nothing.
type Tool struct {
	Name       string
	Desc       string
	Params     map[string]Param
	JSONSchema map[string]any
	// Exec runs the tool with the model-provided JSON arguments and returns the
	// text observation fed back to the model. An error is fed back too (as an
	// error observation) rather than ending the turn — except a *GateError,
	// which parks it. Set this for text-only tools; tools that can return
	// images set ExecRich instead.
	Exec func(ctx context.Context, argsJSON string) (string, error)
	// ExecRich, when non-nil, is used in preference to Exec and may return
	// images (a camera snapshot) alongside text.
	ExecRich func(ctx context.Context, argsJSON string) (Observation, error)
}

// Image is binary image output from a tool, carried back to the model as vision
// input. Data is the raw, un-encoded image bytes.
type Image struct {
	MIMEType string // e.g. "image/jpeg"; defaults to image/jpeg when empty
	Data     []byte
}

// Observation is a rich tool result: a text observation plus any images.
type Observation struct {
	Text   string
	Images []Image
}

// GateError is returned by a tool executor to park the turn instead of
// producing an observation — the durable human-in-the-loop gate. The Backend
// stops, and the provider persists the Parked outcome so the run continues in
// place once the user decides.
//
// It lives at the seam because parking is the provider's mechanism, not one
// loop's: a harness backend gates a tool the same way and parks the same run.
type GateError struct {
	// Tool and Args identify the gated call exactly as the model requested it,
	// so the approval the user gives authorizes that call and no other.
	Tool string
	Args string
	// RequestID references the approval request (inbox item) awaiting the user.
	RequestID string
}

func (e *GateError) Error() string {
	return fmt.Sprintf("tool %q requires user approval (request %s)", e.Tool, e.RequestID)
}

// ToolEvent reports a completed tool call. ID is the model's tool-call id,
// correlating with EventSink.ToolStart.
type ToolEvent struct {
	ID       string
	Name     string
	Args     string // raw JSON arguments from the model
	Result   string // observation (or error text)
	Err      bool
	Duration time.Duration
}

// AssistantMessage is one model response attempt within a turn. Complete is
// false when the response failed mid-stream, in which case Content may be
// partial and must not be treated as an answer. Duration is the active model
// time for this response.
type AssistantMessage struct {
	Content      string
	HasToolCalls bool
	ToolCalls    []schema.ToolCall
	Complete     bool
	Duration     time.Duration
}

// Limits bound one turn. Zero values mean "the backend's default", so a caller
// can pass only what it cares about.
type Limits struct {
	// MaxToolTurns caps tool-call rounds.
	MaxToolTurns int
	// ContextBudgetTokens caps the estimated size of the conversation sent to
	// the model. 0 disables the check.
	ContextBudgetTokens int
	// CheckpointEvery is how many rounds pass between EventSink.Checkpoint
	// offers. 0 disables mid-turn checkpointing, and with it recovery of a run
	// whose process dies.
	CheckpointEvery int
}

// Run identifies the durable run a turn belongs to. The provider owns it
// entirely; a Backend reads it to label and route the work it does on the run's
// behalf, and to answer Cancel.
type Run struct {
	// ID is the run's durable identity — the Run object's name and the key of
	// its rows in the provider store.
	ID string
	// SessionID groups the run's transcript.
	SessionID string
	// Agent is the agent the run executes as.
	Agent string
	// Trigger is what started the run (chat, schedule, channel, spawn, …).
	Trigger string
}

// Input is a fresh turn's material: the conversation to answer, the tools that
// may be called while answering it, and what bounds the attempt.
type Input struct {
	Messages []Message
	Tools    []Tool
	Limits   Limits
}

// Answer continues a turn that parked.
type Answer struct {
	// State is the opaque resume state from the Parked outcome, handed back
	// verbatim.
	State json.RawMessage
	// Decided reports whether there is a verdict to apply at all. An approval
	// resume has one; a recovery resume — a run picked up after its process
	// died — does not: its state holds no gated call, so there is nothing to
	// approve or deny and the turn simply carries on.
	Decided bool
	// Approved is the user's verdict when Decided, and Note the reason they gave
	// for a denial, fed back as an observation so the model can react.
	Approved bool
	Note     string
	// Tools and Limits are re-supplied because the provider rebuilds them for
	// the resuming turn: a connection may have been re-pointed, or a limit
	// raised, since the run parked.
	Tools  []Tool
	Limits Limits
}

// Status is how a turn ended.
type Status string

const (
	// StatusCompleted: the turn finished and produced an answer.
	StatusCompleted Status = "completed"
	// StatusParked: the turn stopped because it needs a person. Outcome.Parked
	// says what is being asked and carries the state to resume from.
	StatusParked Status = "parked"
	// StatusFailed: the turn could not be completed. The error is returned
	// alongside the Outcome.
	StatusFailed Status = "failed"
	// StatusCancelled: the turn was stopped — a user's cancel, or the run's
	// deadline — rather than failing. The provider records it as aborted, not
	// as a fault.
	StatusCancelled Status = "cancelled"
)

// Parked is a turn that stopped needing a human.
//
// There are two ways that happens and they are not the same question. A TOOL
// GATE parked a call the provider wrapped, and what resolves it is a verdict on
// that exact call. A QUESTION was asked by the turn itself — a harness's
// request-user-input — and what resolves it is an answer. Tool/Args describe the
// first, Question the second, and exactly one of them is set.
type Parked struct {
	// Tool and Args are the gated call exactly as it was requested. They are
	// what the user is shown and what an approval authorizes — one call, those
	// arguments. Empty when the park was a question.
	Tool string
	Args string
	// Question is what the turn is asking, when it parked on a question rather
	// than on a gate. The provider posts it to the user's inbox and the answer
	// comes back as Answer.Note.
	Question string
	// RequestID references the approval request (inbox item) awaiting the user.
	// A backend that parks on a gate it wrapped already has one; a backend that
	// parks on a Question leaves it empty and the provider files the inbox item,
	// because reaching the store is the provider's job and not the seam's.
	RequestID string
	// State is the backend's own resume state, opaque to the provider, which
	// stores it with the run and hands it back as Answer.State. The in-process
	// loop puts its conversation snapshot here; a harness backend would put
	// whatever identifies the session it left open.
	State json.RawMessage
}

// Tokens is a token count.
type Tokens struct {
	InputTokens  int64
	OutputTokens int64
}

// Cost is a token count and what it cost, in USD micros. A backend that cannot
// price its own work leaves CostMicros zero rather than inventing a number.
type Cost struct {
	Tokens
	CostMicros int64
}

// Usage is what a turn consumed, and it comes back through the seam because a
// harness reports cost differently from an in-process loop: one counts tokens
// against a published price list, the other is told what it was charged.
type Usage struct {
	// Total is the RUN's cumulative consumption, which is what the run record
	// reports.
	Total Cost
	// Billed is the part THIS turn added, which is what the rolling budget
	// window is charged. They differ on a resume: a resumed turn's total
	// includes what the run had already spent when it parked, and that was
	// billed then.
	Billed Cost
}

// Outcome is what one turn came to.
type Outcome struct {
	// Status is the discriminator; everything below is filled as it allows.
	Status Status
	// Text is everything the turn produced, model commentary included. It is
	// what a channel reply sends and what the parent of a spawned worker reads.
	// On a failed or cancelled turn it is empty — the provider has the partial
	// output it streamed.
	Text string
	// Output is Text without the trailing sources block, and Sources the
	// locators listed in it. Both go on the run record, so a programmatic reader
	// gets the answer and its citations as structure. A backend that reports
	// sources natively fills these directly; SplitSources does it for prose.
	Output  string
	Sources []string
	// Final is the presentation answer for the latest model boundary: the last
	// model response, or a standalone notice when the turn ran out of its
	// tool-call allowance. The portal shows this rather than Text so completed
	// commentary is not repeated as the answer.
	Final string
	// Usage is what the turn consumed.
	Usage Usage
	// Parked is set exactly when Status is StatusParked.
	Parked *Parked
	// Result is set on a completed REPOSITORY attempt — a turn that ran in a
	// fresh checkout and exported a Git result — once the backend has fetched
	// and verified what the runner reported. Nil for a conversational turn.
	Result *RepositoryResult
	// Artifacts are the verified artifact bytes behind Result, for the provider
	// to store: a reader of the run (the coordinator that dispatched it) fetches
	// them from the provider, not from the runner. Empty unless Result is set.
	Artifacts []Artifact
}

// RepositoryResult is what a repository attempt produced, as the provider
// records it on the run: the commit the runner snapshotted on top of the
// approved base, or the fact that nothing changed.
type RepositoryResult struct {
	// BaseCommit is the approved base the attempt ran against, as requested.
	BaseCommit string
	// Commit and Tree are the snapshot the runner exported; empty when
	// NoChanges.
	Commit string
	Tree   string
	// NoChanges reports that the worktree still matched the base commit, in
	// which case there is no bundle.
	NoChanges bool
	// ResultDigest is the sha256 (hex) of the git-result.json bytes.
	ResultDigest string
	// BundleDigest is the sha256 (hex) of the bundle bytes, and BundleSize its
	// length; both absent when NoChanges.
	BundleDigest string
	BundleSize   int64
}

// Artifact is one verified artifact's bytes and the facts a reader is given
// about it.
type Artifact struct {
	Name      string
	Digest    string // sha256 hex of Data
	MediaType string
	Size      int64
	Data      []byte
}

// EventSink is the provider's half of a turn in flight: progress out, and the
// one question a Backend must ask before it acts.
//
// The shapes are the ones the portal's SSE stream already speaks
// (start/delta/tool_start/tool_end/approval_required/done/error), so they are a
// contract rather than an internal detail.
type EventSink interface {
	// Delta receives assistant content as it streams.
	Delta(text string)
	// Assistant reports one complete model response attempt, before its tool
	// calls are executed. A failed attempt is still reported (Complete false) so
	// the time it took is accounted for without its partial text being taken for
	// an answer.
	Assistant(AssistantMessage)
	// ToolStart is called when a tool call begins executing.
	ToolStart(id, name, args string)
	// ToolEnd is called when a tool call completes or fails.
	ToolEnd(ToolEvent)
	// Checkpoint offers resume state from a point where no tool call is
	// half-executed. The provider persists it so a run whose process dies
	// continues from here instead of losing the work; it is the same opaque
	// state a Parked outcome carries.
	Checkpoint(state json.RawMessage)
	// Aborted is consulted before every model round and before every tool call
	// — the two points where nothing is half-done. A non-nil error ends the turn
	// with that error, which is how a cancellation that did not arrive through
	// ctx (a durable flag another replica wrote) stops a turn cleanly.
	Aborted(ctx context.Context) error
}

// Backend executes turns for a run. The agents provider owns the run — the
// queue, the transcript, budgets, the inbox, channel delivery, cancellation —
// and a Backend owns only what happens during one turn.
type Backend interface {
	// Turn answers a fresh conversation.
	Turn(ctx context.Context, r *Run, in Input, sink EventSink) (Outcome, error)
	// Continue picks a parked turn back up.
	Continue(ctx context.Context, r *Run, answer Answer, sink EventSink) (Outcome, error)
	// Cancel stops a turn that is in flight. It must return promptly: the
	// provider calls it from whoever asked for the cancel, alongside cancelling
	// the run's context. For a backend that executes in this process the context
	// IS the mechanism and there is nothing else to do; one that left a turn
	// running elsewhere has to go and stop it.
	Cancel(ctx context.Context, r *Run) error
}
