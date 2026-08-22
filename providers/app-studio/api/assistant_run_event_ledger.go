// Copyright 2026 The Faros Authors.
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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"sync"

	"github.com/cloudwego/eino/adk"

	"github.com/faroshq/provider-app-studio/store"
)

const (
	projectAssistantRunToolRequestEventType = "tool_request"
	projectAssistantRunToolCallEventType    = "tool_call"
	projectAssistantRunToolResultEventType  = "tool_result"
	projectAssistantRunEventPageSize        = 500
)

var (
	errProjectAssistantRunToolCallIDConflict = errors.New("assistant run tool call id conflicts with durable input")
	errProjectAssistantRunIncompleteEffect   = errors.New("assistant run contains an incomplete effectful tool call")
	errProjectAssistantRunIncompleteNonRead  = errors.New("assistant run contains an incomplete non-read tool call")
	errProjectAssistantRunToolLedgerCorrupt  = errors.New("assistant run tool ledger is corrupt")
)

// projectAssistantRunEventLedger makes the append-only AssistantRunEvent log
// the durable idempotency boundary for v2 tool dispatch. BeginToolCall must
// complete before a backend is invoked; FinishToolCall is called only after the
// invocation has produced the exact result or error that the model will see.
//
// The mutex intentionally covers both state reconstruction and each CAS append.
// A single run-scoped ledger can therefore admit concurrent Eino tool calls
// without racing its expected sequence. CAS conflicts caused by another server
// process are resolved by refreshing the durable log and re-evaluating the call.
type projectAssistantRunEventLedger struct {
	store store.Store
	scope store.Scope
	runID string

	mu                 sync.Mutex
	loaded             bool
	lastSequence       int64
	calls              map[string]*projectAssistantRunToolCallState
	latestPlanSnapshot *projectAssistantPlanSnapshot
}

type projectAssistantRunToolCallState struct {
	ToolName          string
	ArgsDigest        string
	Arguments         json.RawMessage
	RequestArgsDigest string
	Read              bool
	Effect            bool
	Requested         bool
	Dispatched        bool
	Attempts          int
	Outcome           *projectAssistantRunToolCallOutcome
}

// projectAssistantRunToolCallToken binds a post-dispatch result to the exact
// durable call event that authorized the dispatch.
type projectAssistantRunToolCallToken struct {
	CallID     string
	ToolName   string
	ArgsDigest string
	Read       bool
	Effect     bool
}

// projectAssistantRunToolCallDecision tells the caller either to dispatch with
// Token or to return Replay without touching the tool backend.
type projectAssistantRunToolCallDecision struct {
	Token  projectAssistantRunToolCallToken
	Replay *projectAssistantRunToolCallOutcome
}

func (d projectAssistantRunToolCallDecision) ShouldDispatch() bool {
	return d.Replay == nil
}

// projectAssistantRunToolCallOutcome preserves the exact model-visible values.
// Failed distinguishes a successful empty result from an error with empty text.
type projectAssistantRunToolCallOutcome struct {
	Result       string
	Error        string
	Failed       bool
	Canceled     bool
	Disposition  projectAssistantToolDisposition
	PlanSnapshot *projectAssistantPlanSnapshot
}

type projectAssistantToolDisposition string

const (
	projectAssistantToolDispositionSucceeded projectAssistantToolDisposition = "succeeded"
	projectAssistantToolDispositionFailed    projectAssistantToolDisposition = "failed"
)

func (o projectAssistantRunToolCallOutcome) Succeeded() bool {
	return o.Disposition == projectAssistantToolDispositionSucceeded
}

func (o projectAssistantRunToolCallOutcome) InvokeResult() (string, error) {
	if !o.Failed || o.Canceled {
		return o.Result, nil
	}
	return o.Result, errors.New(o.Error)
}

type projectAssistantRunToolCallPayload struct {
	Arguments json.RawMessage `json:"arguments"`
	Read      bool            `json:"read"`
	Effect    bool            `json:"effect"`
	Attempt   int             `json:"attempt"`
}

type projectAssistantRunToolResultPayload struct {
	Result       string                          `json:"result"`
	Error        string                          `json:"error"`
	Failed       bool                            `json:"failed"`
	Canceled     bool                            `json:"canceled,omitempty"`
	Disposition  projectAssistantToolDisposition `json:"disposition,omitempty"`
	PlanSnapshot *projectAssistantPlanSnapshot   `json:"planSnapshot,omitempty"`
}

// projectAssistantRunToolCancellation identifies errors produced by a
// server-owned run stop, as opposed to a business interrupt such as an
// approval/follow-up checkpoint. Eino's CancelError and stream-cancelled
// sentinel do not carry a useful model-facing result and must never be copied
// into the durable conversation transcript.
func projectAssistantRunToolCancellation(err error) bool {
	if err == nil {
		return false
	}
	// At the tool ledger boundary Eino can surface its internal interrupt
	// signal rather than the public CancelError, after the invocation context
	// has already been detached for durable persistence. Internal graph state is
	// never a model-facing tool failure; approval/input interrupts happen before
	// an effectful tool is admitted to this result path.
	if strings.HasPrefix(strings.TrimSpace(err.Error()), "interrupt signal:") {
		return true
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, adk.ErrStreamCanceled) {
		return true
	}
	var cancelErr *adk.CancelError
	return errors.As(err, &cancelErr)
}

type projectAssistantRunToolCanceledResult struct {
	Status  string `json:"status"`
	Summary string `json:"summary"`
}

// projectAssistantRunToolCanceledResultJSON intentionally contains no
// command argv, session output, or framework error text. The result remains
// valid bounded JSON so a canceled run can be replayed into later history
// without leaking a partial command response or invalid transcript content.
func projectAssistantRunToolCanceledResultJSON(toolName string, args json.RawMessage) string {
	if projectToolBaseName(toolName) == projectToolExecCommand {
		component := ""
		var input projectAssistantExecCommandInput
		if err := json.Unmarshal(args, &input); err == nil {
			component = truncateProjectToolInfo(strings.TrimSpace(input.Component))
		}
		encoded, err := json.Marshal(projectAssistantExecCommandResult{
			Status:    "canceled",
			Summary:   "Command execution was canceled before a terminal result was recorded.",
			Component: component,
		})
		if err == nil {
			return string(encoded)
		}
	}
	encoded, err := json.Marshal(projectAssistantRunToolCanceledResult{
		Status:  "canceled",
		Summary: "Tool execution was canceled before a terminal result was recorded.",
	})
	if err != nil {
		return `{"status":"canceled","summary":"Tool execution was canceled."}`
	}
	return string(encoded)
}

func newProjectAssistantRunEventLedger(
	messageStore store.Store,
	scope store.Scope,
	runID string,
) *projectAssistantRunEventLedger {
	return &projectAssistantRunEventLedger{
		store: messageStore,
		scope: scope,
		runID: strings.TrimSpace(runID),
		calls: map[string]*projectAssistantRunToolCallState{},
	}
}

// RecordToolRequest durably captures the model-authored call before argument,
// policy, or permission validation. It does not authorize backend dispatch;
// BeginToolCall records that separate admission boundary after validation.
func (l *projectAssistantRunEventLedger) RecordToolRequest(
	ctx context.Context,
	callID string,
	spec projectAssistantToolSpec,
	args any,
) (projectAssistantRunToolCallDecision, error) {
	callID = strings.TrimSpace(callID)
	toolName := projectAssistantToolKey(spec.Name)
	if l == nil || l.store == nil || strings.TrimSpace(l.runID) == "" {
		return projectAssistantRunToolCallDecision{}, fmt.Errorf("assistant run tool ledger is not configured")
	}
	if callID == "" || toolName == "" {
		return projectAssistantRunToolCallDecision{}, fmt.Errorf("assistant run tool call id and name are required")
	}
	canonicalArgs, digest, err := projectAssistantRunToolCallDigest(toolName, args)
	if err != nil {
		return projectAssistantRunToolCallDecision{}, err
	}
	token := projectAssistantRunToolCallToken{
		CallID:     callID,
		ToolName:   toolName,
		ArgsDigest: digest,
		Read:       spec.Risk == projectAssistantToolRiskRead,
		Effect:     projectAssistantToolHasEffect(spec),
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	for {
		if err := l.refreshLocked(ctx); err != nil {
			return projectAssistantRunToolCallDecision{}, err
		}
		state := l.calls[callID]
		if state != nil {
			if err := projectAssistantRunToolRequestStateMatches(state, token); err != nil {
				return projectAssistantRunToolCallDecision{}, err
			}
			if state.Outcome != nil {
				outcome := *state.Outcome
				if err := l.ensureToolConversationLocked(ctx, callID, toolName, canonicalArgs, max(state.Attempts, 1), &outcome); err != nil {
					return projectAssistantRunToolCallDecision{}, err
				}
				return projectAssistantRunToolCallDecision{Replay: &outcome}, nil
			}
			if err := l.appendToolCallConversationLocked(ctx, callID, toolName, canonicalArgs, max(state.Attempts, 1)); err != nil {
				return projectAssistantRunToolCallDecision{}, err
			}
			return projectAssistantRunToolCallDecision{Token: token}, nil
		}

		payload, err := json.Marshal(projectAssistantRunToolCallPayload{
			Arguments: canonicalArgs,
			Read:      token.Read,
			Effect:    token.Effect,
		})
		if err != nil {
			return projectAssistantRunToolCallDecision{}, fmt.Errorf("encode assistant run tool request event: %w", err)
		}
		if err := l.appendToolCallConversationLocked(ctx, callID, toolName, canonicalArgs, 1); err != nil {
			return projectAssistantRunToolCallDecision{}, err
		}
		event := store.AssistantRunEvent{
			RunID:      l.runID,
			Type:       projectAssistantRunToolRequestEventType,
			CallID:     callID,
			ToolName:   toolName,
			ArgsDigest: digest,
			Payload:    payload,
		}
		saved, err := l.store.AppendAssistantRunEvent(ctx, l.scope, event, l.lastSequence)
		if errors.Is(err, store.ErrAssistantRunEventConflict) {
			continue
		}
		if err != nil {
			return projectAssistantRunToolCallDecision{}, fmt.Errorf("append assistant run tool request event: %w", err)
		}
		if err := l.applyEventLocked(saved); err != nil {
			return projectAssistantRunToolCallDecision{}, err
		}
		return projectAssistantRunToolCallDecision{Token: token}, nil
	}
}

func projectAssistantRunToolRequestStateMatches(state *projectAssistantRunToolCallState, token projectAssistantRunToolCallToken) error {
	digest := state.RequestArgsDigest
	if digest == "" {
		digest = state.ArgsDigest
	}
	if state.ToolName != token.ToolName || digest != token.ArgsDigest {
		return fmt.Errorf(
			"%w: call %q was already recorded as %s (%s)",
			errProjectAssistantRunToolCallIDConflict,
			token.CallID,
			state.ToolName,
			digest,
		)
	}
	if state.Read != token.Read || state.Effect != token.Effect {
		return fmt.Errorf("%w: call %q changed risk classification", errProjectAssistantRunToolCallIDConflict, token.CallID)
	}
	return nil
}

func projectAssistantRunToolCallStateMatches(state *projectAssistantRunToolCallState, token projectAssistantRunToolCallToken) error {
	if state.ToolName != token.ToolName || state.ArgsDigest != token.ArgsDigest {
		return fmt.Errorf(
			"%w: call %q was already admitted as %s (%s)",
			errProjectAssistantRunToolCallIDConflict,
			token.CallID,
			state.ToolName,
			state.ArgsDigest,
		)
	}
	if state.Read != token.Read || state.Effect != token.Effect {
		return fmt.Errorf("%w: call %q changed risk classification", errProjectAssistantRunToolCallIDConflict, token.CallID)
	}
	return nil
}

// BeginToolCall durably records a call before dispatch. A completed exact call
// is replayed. Reusing an ID for different input is rejected. An interrupted
// read may be retried because it has no side effect; an interrupted effect is
// failed closed because dispatch may already have happened.
func (l *projectAssistantRunEventLedger) BeginToolCall(
	ctx context.Context,
	callID string,
	spec projectAssistantToolSpec,
	args map[string]any,
) (projectAssistantRunToolCallDecision, error) {
	callID = strings.TrimSpace(callID)
	toolName := projectAssistantToolKey(spec.Name)
	if l == nil || l.store == nil || strings.TrimSpace(l.runID) == "" {
		return projectAssistantRunToolCallDecision{}, fmt.Errorf("assistant run tool ledger is not configured")
	}
	if callID == "" || toolName == "" {
		return projectAssistantRunToolCallDecision{}, fmt.Errorf("assistant run tool call id and name are required")
	}
	canonicalArgs, digest, err := projectAssistantRunToolCallDigest(toolName, args)
	if err != nil {
		return projectAssistantRunToolCallDecision{}, err
	}
	token := projectAssistantRunToolCallToken{
		CallID:     callID,
		ToolName:   toolName,
		ArgsDigest: digest,
		Read:       spec.Risk == projectAssistantToolRiskRead,
		Effect:     projectAssistantToolHasEffect(spec),
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	for {
		if err := l.refreshLocked(ctx); err != nil {
			return projectAssistantRunToolCallDecision{}, err
		}
		state := l.calls[callID]
		if state != nil {
			if state.Requested && !state.Dispatched {
				if state.ToolName != token.ToolName || state.Read != token.Read || state.Effect != token.Effect {
					return projectAssistantRunToolCallDecision{}, fmt.Errorf(
						"%w: call %q changed tool identity or risk classification",
						errProjectAssistantRunToolCallIDConflict,
						callID,
					)
				}
			} else if err := projectAssistantRunToolCallStateMatches(state, token); err != nil {
				return projectAssistantRunToolCallDecision{}, err
			}
			if state.Outcome != nil {
				outcome := *state.Outcome
				if err := l.ensureToolConversationLocked(ctx, callID, toolName, canonicalArgs, max(state.Attempts, 1), &outcome); err != nil {
					return projectAssistantRunToolCallDecision{}, err
				}
				return projectAssistantRunToolCallDecision{Replay: &outcome}, nil
			}
			if state.Requested && !state.Dispatched {
				payload, err := json.Marshal(projectAssistantRunToolCallPayload{
					Arguments: canonicalArgs,
					Read:      token.Read,
					Effect:    token.Effect,
					Attempt:   1,
				})
				if err != nil {
					return projectAssistantRunToolCallDecision{}, fmt.Errorf("encode assistant run tool call event: %w", err)
				}
				event := store.AssistantRunEvent{
					RunID:      l.runID,
					Type:       projectAssistantRunToolCallEventType,
					CallID:     callID,
					ToolName:   toolName,
					ArgsDigest: digest,
					Payload:    payload,
				}
				saved, err := l.store.AppendAssistantRunEvent(ctx, l.scope, event, l.lastSequence)
				if errors.Is(err, store.ErrAssistantRunEventConflict) {
					continue
				}
				if err != nil {
					return projectAssistantRunToolCallDecision{}, fmt.Errorf("append assistant run tool call event: %w", err)
				}
				if err := l.applyEventLocked(saved); err != nil {
					return projectAssistantRunToolCallDecision{}, err
				}
				return projectAssistantRunToolCallDecision{Token: token}, nil
			}
			if err := l.appendToolCallConversationLocked(ctx, callID, toolName, canonicalArgs, state.Attempts); err != nil {
				return projectAssistantRunToolCallDecision{}, err
			}
			if state.Effect {
				return projectAssistantRunToolCallDecision{}, fmt.Errorf(
					"%w: %s call %q may already have been dispatched",
					errProjectAssistantRunIncompleteEffect,
					state.ToolName,
					callID,
				)
			}
			if !state.Read {
				return projectAssistantRunToolCallDecision{}, fmt.Errorf(
					"%w: %s call %q has no durable result",
					errProjectAssistantRunIncompleteNonRead,
					state.ToolName,
					callID,
				)
			}
		}

		attempt := 1
		if state != nil {
			attempt = state.Attempts + 1
		}
		payload, err := json.Marshal(projectAssistantRunToolCallPayload{
			Arguments: canonicalArgs,
			Read:      token.Read,
			Effect:    token.Effect,
			Attempt:   attempt,
		})
		if err != nil {
			return projectAssistantRunToolCallDecision{}, fmt.Errorf("encode assistant run tool call event: %w", err)
		}
		// Persist the model-visible call before the event that authorizes backend
		// dispatch. If the event CAS fails, no effect was authorized and a retry
		// can idempotently repair this same item before trying again.
		if err := l.appendToolCallConversationLocked(ctx, callID, toolName, canonicalArgs, attempt); err != nil {
			return projectAssistantRunToolCallDecision{}, err
		}
		event := store.AssistantRunEvent{
			RunID:      l.runID,
			Type:       projectAssistantRunToolCallEventType,
			CallID:     callID,
			ToolName:   toolName,
			ArgsDigest: digest,
			Payload:    payload,
		}
		saved, err := l.store.AppendAssistantRunEvent(ctx, l.scope, event, l.lastSequence)
		if errors.Is(err, store.ErrAssistantRunEventConflict) {
			// Another process won the sequence. Refresh and make the durable
			// state decide whether this call is now a replay or a conflict.
			continue
		}
		if err != nil {
			return projectAssistantRunToolCallDecision{}, fmt.Errorf("append assistant run tool call event: %w", err)
		}
		if err := l.applyEventLocked(saved); err != nil {
			return projectAssistantRunToolCallDecision{}, err
		}
		return projectAssistantRunToolCallDecision{Token: token}, nil
	}
}

// FinishToolCall durably records the exact model-visible outcome after
// dispatch. The first completion is authoritative if retryable reads overlap.
func (l *projectAssistantRunEventLedger) FinishToolCall(
	ctx context.Context,
	token projectAssistantRunToolCallToken,
	result string,
	invokeErr error,
) (projectAssistantRunToolCallOutcome, error) {
	return l.finishToolCall(ctx, token, result, invokeErr, nil)
}

// FinishToolCallWithPlan atomically settles a write_todos result together with
// the sanitized App-owned projection used by lifecycle/replay consumers.
func (l *projectAssistantRunEventLedger) FinishToolCallWithPlan(
	ctx context.Context,
	token projectAssistantRunToolCallToken,
	result string,
	invokeErr error,
	plan *projectAssistantPlanSnapshot,
) (projectAssistantRunToolCallOutcome, error) {
	return l.finishToolCall(ctx, token, result, invokeErr, plan)
}

func (l *projectAssistantRunEventLedger) finishToolCall(
	ctx context.Context,
	token projectAssistantRunToolCallToken,
	result string,
	invokeErr error,
	plan *projectAssistantPlanSnapshot,
) (projectAssistantRunToolCallOutcome, error) {
	if l == nil || l.store == nil || strings.TrimSpace(l.runID) == "" {
		return projectAssistantRunToolCallOutcome{}, fmt.Errorf("assistant run tool ledger is not configured")
	}
	persistCtx, cancelPersist := detachedProjectPersistenceContext(ctx)
	defer cancelPersist()

	l.mu.Lock()
	defer l.mu.Unlock()
	for {
		if err := l.refreshLocked(persistCtx); err != nil {
			return projectAssistantRunToolCallOutcome{}, err
		}
		state := l.calls[strings.TrimSpace(token.CallID)]
		if state == nil {
			return projectAssistantRunToolCallOutcome{}, fmt.Errorf(
				"%w: result for call %q has no preceding call event",
				errProjectAssistantRunToolLedgerCorrupt,
				token.CallID,
			)
		}
		if state.ToolName != token.ToolName || state.ArgsDigest != token.ArgsDigest ||
			state.Read != token.Read || state.Effect != token.Effect {
			return projectAssistantRunToolCallOutcome{}, fmt.Errorf(
				"%w: result token for call %q does not match durable input",
				errProjectAssistantRunToolCallIDConflict,
				token.CallID,
			)
		}
		if state.Outcome != nil {
			persisted := *state.Outcome
			if err := l.appendToolResultConversationLocked(persistCtx, token.CallID, token.ToolName, persisted); err != nil {
				return projectAssistantRunToolCallOutcome{}, err
			}
			return persisted, nil
		}
		persistResult, persistErr, persistPlan := result, invokeErr, plan
		canceled := projectAssistantRunToolCancellation(invokeErr)
		if canceled {
			// Eino cancellation errors can contain an entire interrupt/checkpoint
			// graph. They are control-plane details, not model-facing tool
			// output. Replace them before either durable event or conversation
			// projection is written, and suppress a plan snapshot from a tool
			// that did not complete successfully.
			persistResult = projectAssistantRunToolCanceledResultJSON(token.ToolName, state.Arguments)
			persistErr = nil
			persistPlan = nil
		}
		outcome := projectAssistantRunToolCallOutcome{
			Result:      persistResult,
			Disposition: projectAssistantToolResultDisposition(token.ToolName, persistResult, persistErr),
			Canceled:    canceled,
		}
		if persistErr != nil {
			outcome.Error = persistErr.Error()
			outcome.Failed = true
		}
		if canceled {
			// A canceled effect is not a success, even though its clean JSON
			// result is deliberately returned to Eino without rethrowing the
			// framework cancellation error.
			outcome.Failed = true
			outcome.Disposition = projectAssistantToolDispositionFailed
		}
		if persistPlan != nil && projectToolBaseName(token.ToolName) != projectEinoAssistantWriteTodosTool {
			return projectAssistantRunToolCallOutcome{}, errors.New("assistant run tool plan snapshot is only valid for write_todos")
		}
		if persistPlan != nil && !outcome.Succeeded() {
			return projectAssistantRunToolCallOutcome{}, errors.New("assistant run tool plan snapshot requires a successful result")
		}
		if outcome.Succeeded() && persistPlan != nil {
			if !projectAssistantPlanSnapshotValid(*persistPlan) {
				return projectAssistantRunToolCallOutcome{}, errors.New("assistant run tool plan snapshot is invalid")
			}
			copy := cloneProjectAssistantPlanSnapshot(*persistPlan)
			outcome.PlanSnapshot = &copy
		}
		payload, err := json.Marshal(projectAssistantRunToolResultPayload{
			Result:       outcome.Result,
			Error:        outcome.Error,
			Failed:       outcome.Failed,
			Canceled:     outcome.Canceled,
			Disposition:  outcome.Disposition,
			PlanSnapshot: outcome.PlanSnapshot,
		})
		if err != nil {
			return projectAssistantRunToolCallOutcome{}, fmt.Errorf("encode assistant run tool result event: %w", err)
		}
		event := store.AssistantRunEvent{
			RunID:      l.runID,
			Type:       projectAssistantRunToolResultEventType,
			CallID:     token.CallID,
			ToolName:   token.ToolName,
			ArgsDigest: token.ArgsDigest,
			Payload:    payload,
		}
		saved, err := l.store.AppendAssistantRunEvent(persistCtx, l.scope, event, l.lastSequence)
		if errors.Is(err, store.ErrAssistantRunEventConflict) {
			continue
		}
		if err != nil {
			return projectAssistantRunToolCallOutcome{}, fmt.Errorf("append assistant run tool result event: %w", err)
		}
		if err := l.applyEventLocked(saved); err != nil {
			return projectAssistantRunToolCallOutcome{}, err
		}
		if err := l.appendToolResultConversationLocked(persistCtx, token.CallID, token.ToolName, outcome); err != nil {
			return projectAssistantRunToolCallOutcome{}, err
		}
		return outcome, nil
	}
}

func (l *projectAssistantRunEventLedger) ensureToolConversationLocked(
	ctx context.Context,
	callID string,
	toolName string,
	canonicalArgs json.RawMessage,
	attempt int,
	outcome *projectAssistantRunToolCallOutcome,
) error {
	if err := l.appendToolCallConversationLocked(ctx, callID, toolName, canonicalArgs, attempt); err != nil {
		return err
	}
	if outcome == nil {
		return nil
	}
	return l.appendToolResultConversationLocked(ctx, callID, toolName, *outcome)
}

func (l *projectAssistantRunEventLedger) appendToolCallConversationLocked(
	ctx context.Context,
	callID string,
	toolName string,
	canonicalArgs json.RawMessage,
	attempt int,
) error {
	if attempt <= 0 {
		return fmt.Errorf("append assistant conversation tool call: invalid attempt %d", attempt)
	}
	err := appendProjectAssistantConversationMessage(ctx, l.store, l.scope, l.runID,
		projectAssistantConversationToolCallItemID(l.runID, callID, attempt), projectAssistantConversationToolCall,
		chatMessage{Role: "assistant", ToolCalls: []chatToolCall{{
			ID: callID, Type: "function", Function: chatToolCallFunction{Name: toolName, Arguments: string(canonicalArgs)},
		}}},
	)
	if err != nil {
		return fmt.Errorf("append assistant conversation tool call: %w", err)
	}
	return nil
}

func (l *projectAssistantRunEventLedger) appendToolResultConversationLocked(
	ctx context.Context,
	callID string,
	toolName string,
	outcome projectAssistantRunToolCallOutcome,
) error {
	modelResult := outcome.Result
	if strings.TrimSpace(modelResult) == "" && outcome.Failed {
		modelResult = outcome.Error
	}
	err := appendProjectAssistantConversationMessage(ctx, l.store, l.scope, l.runID,
		projectAssistantConversationToolResultItemID(l.runID, callID), projectAssistantConversationToolResult,
		chatMessage{Role: "tool", Name: toolName, ToolCallID: callID, Content: modelResult},
	)
	if err != nil {
		return fmt.Errorf("append assistant conversation tool result: %w", err)
	}
	return nil
}

func (l *projectAssistantRunEventLedger) refreshLocked(ctx context.Context) error {
	if !l.loaded {
		l.calls = map[string]*projectAssistantRunToolCallState{}
		l.lastSequence = 0
		l.loaded = true
	}
	for {
		events, err := l.store.ListAssistantRunEvents(
			ctx,
			l.scope,
			l.runID,
			l.lastSequence,
			projectAssistantRunEventPageSize,
		)
		if err != nil {
			return fmt.Errorf("load assistant run tool ledger: %w", err)
		}
		if len(events) == 0 {
			return nil
		}
		for _, event := range events {
			if event.Sequence != l.lastSequence+1 {
				return fmt.Errorf(
					"%w: event sequence advanced from %d to %d",
					errProjectAssistantRunToolLedgerCorrupt,
					l.lastSequence,
					event.Sequence,
				)
			}
			if err := l.applyEventLocked(event); err != nil {
				return err
			}
		}
		if len(events) < projectAssistantRunEventPageSize {
			return nil
		}
	}
}

func (l *projectAssistantRunEventLedger) applyEventLocked(event store.AssistantRunEvent) error {
	if event.Sequence != l.lastSequence+1 {
		return fmt.Errorf(
			"%w: event sequence advanced from %d to %d",
			errProjectAssistantRunToolLedgerCorrupt,
			l.lastSequence,
			event.Sequence,
		)
	}
	if event.Type != projectAssistantRunToolRequestEventType &&
		event.Type != projectAssistantRunToolCallEventType &&
		event.Type != projectAssistantRunToolResultEventType {
		l.lastSequence = event.Sequence
		return nil
	}
	callID := strings.TrimSpace(event.CallID)
	toolName := projectAssistantToolKey(event.ToolName)
	digest := strings.TrimSpace(event.ArgsDigest)
	if callID == "" || toolName == "" || digest == "" {
		return fmt.Errorf("%w: sequence %d is missing tool identity", errProjectAssistantRunToolLedgerCorrupt, event.Sequence)
	}
	state := l.calls[callID]
	switch event.Type {
	case projectAssistantRunToolRequestEventType:
		var payload projectAssistantRunToolCallPayload
		if err := json.Unmarshal(event.Payload, &payload); err != nil || !json.Valid(payload.Arguments) || payload.Attempt != 0 {
			return fmt.Errorf("%w: invalid tool request payload at sequence %d", errProjectAssistantRunToolLedgerCorrupt, event.Sequence)
		}
		_, persistedDigest, err := projectAssistantRunToolCallDigest(toolName, payload.Arguments)
		if err != nil || persistedDigest != digest {
			return fmt.Errorf("%w: tool request digest mismatch at sequence %d", errProjectAssistantRunToolLedgerCorrupt, event.Sequence)
		}
		if state == nil {
			l.calls[callID] = &projectAssistantRunToolCallState{
				ToolName:          toolName,
				ArgsDigest:        digest,
				Arguments:         append(json.RawMessage(nil), payload.Arguments...),
				RequestArgsDigest: digest,
				Read:              payload.Read,
				Effect:            payload.Effect,
				Requested:         true,
			}
		} else if state.ToolName != toolName ||
			(state.RequestArgsDigest != "" && state.RequestArgsDigest != digest) ||
			state.Read != payload.Read || state.Effect != payload.Effect {
			return fmt.Errorf("%w: call %q has conflicting durable requests", errProjectAssistantRunToolLedgerCorrupt, callID)
		} else {
			state.Requested = true
		}
	case projectAssistantRunToolCallEventType:
		var payload projectAssistantRunToolCallPayload
		if err := json.Unmarshal(event.Payload, &payload); err != nil || !json.Valid(payload.Arguments) || payload.Attempt < 1 {
			return fmt.Errorf("%w: invalid tool call payload at sequence %d", errProjectAssistantRunToolLedgerCorrupt, event.Sequence)
		}
		_, persistedDigest, err := projectAssistantRunToolCallDigest(toolName, payload.Arguments)
		if err != nil || persistedDigest != digest {
			return fmt.Errorf("%w: tool call digest mismatch at sequence %d", errProjectAssistantRunToolLedgerCorrupt, event.Sequence)
		}
		if state == nil {
			l.calls[callID] = &projectAssistantRunToolCallState{
				ToolName:   toolName,
				ArgsDigest: digest,
				Arguments:  append(json.RawMessage(nil), payload.Arguments...),
				Read:       payload.Read,
				Effect:     payload.Effect,
				Dispatched: true,
				Attempts:   payload.Attempt,
			}
		} else if !state.Dispatched && state.Requested && state.Outcome == nil && payload.Attempt == 1 {
			if state.ToolName != toolName || state.Read != payload.Read || state.Effect != payload.Effect {
				return fmt.Errorf("%w: call %q changed tool identity or risk classification", errProjectAssistantRunToolLedgerCorrupt, callID)
			}
			state.ArgsDigest = digest
			state.Arguments = append(json.RawMessage(nil), payload.Arguments...)
			state.Dispatched = true
			state.Attempts = payload.Attempt
		} else if state.ToolName != toolName || state.ArgsDigest != digest || state.Read != payload.Read || state.Effect != payload.Effect {
			return fmt.Errorf("%w: call %q has conflicting durable inputs", errProjectAssistantRunToolLedgerCorrupt, callID)
		} else if state.Outcome != nil || !state.Read || payload.Attempt != state.Attempts+1 {
			return fmt.Errorf("%w: call %q has an invalid retry event", errProjectAssistantRunToolLedgerCorrupt, callID)
		} else {
			state.Attempts = payload.Attempt
		}
	case projectAssistantRunToolResultEventType:
		if state == nil {
			return fmt.Errorf("%w: result for call %q precedes its call event", errProjectAssistantRunToolLedgerCorrupt, callID)
		}
		if state.ToolName != toolName || state.ArgsDigest != digest {
			return fmt.Errorf("%w: result for call %q does not match durable input", errProjectAssistantRunToolLedgerCorrupt, callID)
		}
		var payload projectAssistantRunToolResultPayload
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			return fmt.Errorf("%w: invalid tool result payload at sequence %d", errProjectAssistantRunToolLedgerCorrupt, event.Sequence)
		}
		outcome := projectAssistantRunToolCallOutcome{
			Result:       payload.Result,
			Error:        payload.Error,
			Failed:       payload.Failed,
			Canceled:     payload.Canceled,
			Disposition:  payload.Disposition,
			PlanSnapshot: payload.PlanSnapshot,
		}
		if outcome.Canceled {
			if outcome.PlanSnapshot != nil {
				return fmt.Errorf("%w: canceled tool result at sequence %d contains a plan snapshot", errProjectAssistantRunToolLedgerCorrupt, event.Sequence)
			}
			// Reconstruct the bounded cancellation result from the durable tool
			// request rather than trusting a potentially partial or stale payload.
			outcome.Result = projectAssistantRunToolCanceledResultJSON(toolName, state.Arguments)
			outcome.Error = ""
			outcome.Failed = true
			outcome.Disposition = projectAssistantToolDispositionFailed
		}
		if outcome.PlanSnapshot != nil && !projectAssistantPlanSnapshotValid(*outcome.PlanSnapshot) {
			return fmt.Errorf("%w: invalid plan snapshot at sequence %d", errProjectAssistantRunToolLedgerCorrupt, event.Sequence)
		}
		// Events written before typed settlement are read compatibly, but every
		// newly written result persists the semantic disposition explicitly.
		if outcome.Disposition == "" {
			var invokeErr error
			if outcome.Failed {
				invokeErr = errors.New(outcome.Error)
			}
			outcome.Disposition = projectAssistantToolResultDisposition(toolName, outcome.Result, invokeErr)
		}
		if outcome.PlanSnapshot != nil {
			if projectToolBaseName(toolName) != projectEinoAssistantWriteTodosTool || outcome.Failed || !outcome.Succeeded() {
				return fmt.Errorf("%w: invalid plan snapshot tool result at sequence %d", errProjectAssistantRunToolLedgerCorrupt, event.Sequence)
			}
		}
		if state.Outcome != nil && !reflect.DeepEqual(*state.Outcome, outcome) {
			return fmt.Errorf("%w: call %q has conflicting durable results", errProjectAssistantRunToolLedgerCorrupt, callID)
		}
		state.Outcome = &outcome
		copy := cloneProjectAssistantPlanSnapshotIfPresent(outcome.PlanSnapshot)
		if copy != nil {
			l.latestPlanSnapshot = copy
		}
	}
	// Advance only after the complete event has validated and been applied.
	// A corrupt durable event must remain the next event on every refresh so
	// this ledger fails closed instead of silently skipping past it.
	l.lastSequence = event.Sequence
	return nil
}

// LatestPlanSnapshot reconstructs the latest successful App-owned write_todos
// projection from the durable run ledger. It intentionally refreshes the
// append-only event stream before returning and clones the result so callers
// can hydrate a new run state without sharing ledger memory.
func (l *projectAssistantRunEventLedger) LatestPlanSnapshot(ctx context.Context) (projectAssistantPlanSnapshot, bool, error) {
	if l == nil {
		return projectAssistantPlanSnapshot{}, false, nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.refreshLocked(ctx); err != nil {
		return projectAssistantPlanSnapshot{}, false, err
	}
	if l.latestPlanSnapshot == nil {
		return projectAssistantPlanSnapshot{}, false, nil
	}
	return cloneProjectAssistantPlanSnapshot(*l.latestPlanSnapshot), true, nil
}

func cloneProjectAssistantPlanSnapshotIfPresent(plan *projectAssistantPlanSnapshot) *projectAssistantPlanSnapshot {
	if plan == nil {
		return nil
	}
	copy := cloneProjectAssistantPlanSnapshot(*plan)
	return &copy
}

func (l *projectAssistantRunEventLedger) SettledToolCall(
	ctx context.Context,
	callID string,
	toolName string,
) (map[string]any, projectAssistantRunToolCallOutcome, bool, error) {
	if l == nil {
		return nil, projectAssistantRunToolCallOutcome{}, false, nil
	}
	callID = strings.TrimSpace(callID)
	toolName = projectAssistantToolKey(toolName)
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.refreshLocked(ctx); err != nil {
		return nil, projectAssistantRunToolCallOutcome{}, false, err
	}
	state := l.calls[callID]
	if state == nil || state.Outcome == nil {
		return nil, projectAssistantRunToolCallOutcome{}, false, nil
	}
	if state.ToolName != toolName {
		return nil, projectAssistantRunToolCallOutcome{}, false, fmt.Errorf(
			"%w: call %q was already recorded as %s",
			errProjectAssistantRunToolCallIDConflict,
			callID,
			state.ToolName,
		)
	}
	args := map[string]any{}
	if err := json.Unmarshal(state.Arguments, &args); err != nil {
		return nil, projectAssistantRunToolCallOutcome{}, false, fmt.Errorf("%w: call %q arguments are invalid", errProjectAssistantRunToolLedgerCorrupt, callID)
	}
	return args, *state.Outcome, true, nil
}

func (l *projectAssistantRunEventLedger) ToolCallOutcome(ctx context.Context, callID string) (projectAssistantRunToolCallOutcome, bool, error) {
	if l == nil {
		return projectAssistantRunToolCallOutcome{}, false, nil
	}
	callID = strings.TrimSpace(callID)
	l.mu.Lock()
	defer l.mu.Unlock()
	// FinishToolCall applies the durable result to this ledger before returning.
	// Consume that settled outcome without consulting a caller context that may
	// already be cancelled immediately after the external side effect succeeds.
	if state := l.calls[callID]; state != nil && state.Outcome != nil {
		return *state.Outcome, true, nil
	}
	if err := l.refreshLocked(ctx); err != nil {
		return projectAssistantRunToolCallOutcome{}, false, err
	}
	state := l.calls[callID]
	if state == nil || state.Outcome == nil {
		return projectAssistantRunToolCallOutcome{}, false, nil
	}
	return *state.Outcome, true, nil
}

// RecoverDanglingToolCall resolves the missing tool-result message that Eino's
// patchtoolcalls middleware found in restored history. The durable ledger is
// authoritative: settled outcomes are replayed exactly, reads may be retried,
// and any effect that may have crossed the dispatch boundary fails closed.
func (l *projectAssistantRunEventLedger) RecoverDanglingToolCall(ctx context.Context, callID, toolName string) (string, error) {
	if l == nil {
		return "", errors.New("assistant run tool ledger is not configured for recovery")
	}
	callID = strings.TrimSpace(callID)
	toolName = projectAssistantToolKey(toolName)
	if callID == "" || toolName == "" {
		return "", fmt.Errorf("%w: dangling tool call identity is incomplete", errProjectAssistantRunToolLedgerCorrupt)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.refreshLocked(ctx); err != nil {
		return "", err
	}
	state := l.calls[callID]
	if state == nil {
		return "", fmt.Errorf("%w: dangling call %q has no durable ledger entry", errProjectAssistantRunToolLedgerCorrupt, callID)
	}
	if state.ToolName != toolName {
		return "", fmt.Errorf("%w: call %q was recorded as %s", errProjectAssistantRunToolCallIDConflict, callID, state.ToolName)
	}
	if state.Outcome != nil {
		if state.Outcome.Result != "" || !state.Outcome.Failed {
			return state.Outcome.Result, nil
		}
		return "Tool call failed: " + state.Outcome.Error, nil
	}
	if !state.Dispatched {
		return "Tool call was not durably admitted and was not dispatched. Submit a new call if it is still needed.", nil
	}
	if state.Effect {
		return "", fmt.Errorf("%w: %s call %q may already have been dispatched", errProjectAssistantRunIncompleteEffect, state.ToolName, callID)
	}
	if !state.Read {
		return "", fmt.Errorf("%w: %s call %q has no durable result", errProjectAssistantRunIncompleteNonRead, state.ToolName, callID)
	}
	return "The prior read result was not recorded. It is safe to issue a new read call if the information is still needed.", nil
}

func projectAssistantRunToolCallDigest(toolName string, args any) (json.RawMessage, string, error) {
	toolName = projectAssistantToolKey(toolName)
	if toolName == "" {
		return nil, "", fmt.Errorf("assistant run tool name is required")
	}
	canonicalArgs, err := projectAssistantCanonicalJSON(args)
	if err != nil {
		return nil, "", fmt.Errorf("canonicalize %s tool arguments: %w", toolName, err)
	}
	digest := sha256.Sum256(append(append([]byte(toolName), 0), canonicalArgs...))
	return canonicalArgs, "sha256:" + hex.EncodeToString(digest[:]), nil
}

func projectAssistantCanonicalJSON(value any) (json.RawMessage, error) {
	if raw, ok := value.(json.RawMessage); ok {
		var decoded any
		decoder := json.NewDecoder(strings.NewReader(string(raw)))
		decoder.UseNumber()
		if err := decoder.Decode(&decoded); err != nil {
			return nil, err
		}
		var trailing any
		if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
			if err != nil {
				return nil, err
			}
			return nil, fmt.Errorf("multiple JSON values are not allowed")
		}
		value = decoded
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	if !json.Valid(raw) {
		return nil, fmt.Errorf("arguments are not valid JSON")
	}
	return json.RawMessage(raw), nil
}
