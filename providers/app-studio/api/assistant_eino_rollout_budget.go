/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"sync"

	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

const (
	projectAssistantRolloutBudgetSamplingTokenWeight = 1.0
	projectAssistantRolloutBudgetPrefillTokenWeight  = 0.25
)

var errProjectAssistantSessionBudgetExceeded = errors.New("assistant exhausted the configured rollout token budget")

type projectAssistantSessionBudgetExceededError struct {
	LimitTokens        int64
	WeightedTokensUsed float64
}

func (e *projectAssistantSessionBudgetExceededError) Error() string {
	return errProjectAssistantSessionBudgetExceeded.Error()
}

func (e *projectAssistantSessionBudgetExceededError) Unwrap() error {
	return errProjectAssistantSessionBudgetExceeded
}

type projectAssistantRolloutBudgetState struct {
	LimitTokens               int64   `json:"limitTokens"`
	ReminderAtRemaining       []int64 `json:"reminderAtRemainingTokens,omitempty"`
	SamplingTokenWeight       float64 `json:"samplingTokenWeight"`
	PrefillTokenWeight        float64 `json:"prefillTokenWeight"`
	WeightedTokensUsed        float64 `json:"weightedTokensUsed,omitempty"`
	WindowID                  uint64  `json:"windowID,omitempty"`
	DeliveredWindowID         uint64  `json:"deliveredWindowID,omitempty"`
	DeliveredReminderIndex    int     `json:"deliveredReminderIndex,omitempty"`
	ReminderDelivered         bool    `json:"reminderDelivered,omitempty"`
	Exhausted                 bool    `json:"exhausted,omitempty"`
	MissingUsageResponseCount int     `json:"missingUsageResponseCount,omitempty"`
}

type projectAssistantRolloutBudgetReminder struct {
	RemainingTokens int64
	ReminderIndex   int
	WindowID        uint64
}

type projectEinoAssistantRolloutBudget struct {
	mu              sync.Mutex
	state           projectAssistantRolloutBudgetState
	recorder        *projectAssistantRunAuditRecorder
	persistReminder func(context.Context, *projectAssistantRolloutBudgetReminder) error
	persistState    func(context.Context, projectAssistantRolloutBudgetState) error
}

func newProjectEinoAssistantRolloutBudget(
	limitTokens int64,
	restored *projectAssistantRolloutBudgetState,
	recorder *projectAssistantRunAuditRecorder,
	persistReminder func(context.Context, *projectAssistantRolloutBudgetReminder) error,
) *projectEinoAssistantRolloutBudget {
	if limitTokens <= 0 {
		return nil
	}
	state := projectAssistantRolloutBudgetState{
		LimitTokens:         limitTokens,
		SamplingTokenWeight: projectAssistantRolloutBudgetSamplingTokenWeight,
		PrefillTokenWeight:  projectAssistantRolloutBudgetPrefillTokenWeight,
		WindowID:            1,
	}
	state.ReminderAtRemaining = projectAssistantRolloutBudgetThresholds(limitTokens)
	if restored != nil && restored.LimitTokens > 0 {
		state = cloneProjectAssistantRolloutBudgetState(*restored)
	}
	return &projectEinoAssistantRolloutBudget{
		state:           state,
		recorder:        recorder,
		persistReminder: persistReminder,
	}
}

func projectAssistantRolloutBudgetThresholds(limit int64) []int64 {
	if limit <= 1 {
		return nil
	}
	thresholds := []int64{limit * 3 / 4, limit / 2, limit / 4}
	out := make([]int64, 0, len(thresholds))
	for _, threshold := range thresholds {
		if threshold <= 0 || threshold >= limit {
			continue
		}
		if len(out) > 0 && out[len(out)-1] == threshold {
			continue
		}
		out = append(out, threshold)
	}
	return out
}

func cloneProjectAssistantRolloutBudgetState(state projectAssistantRolloutBudgetState) projectAssistantRolloutBudgetState {
	state.ReminderAtRemaining = append([]int64(nil), state.ReminderAtRemaining...)
	return state
}

func (b *projectEinoAssistantRolloutBudget) Snapshot() *projectAssistantRolloutBudgetState {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	state := cloneProjectAssistantRolloutBudgetState(b.state)
	return &state
}

func (b *projectEinoAssistantRolloutBudget) ExhaustionError() error {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.state.Exhausted {
		return nil
	}
	return &projectAssistantSessionBudgetExceededError{
		LimitTokens:        b.state.LimitTokens,
		WeightedTokensUsed: b.state.WeightedTokensUsed,
	}
}

func (b *projectEinoAssistantRolloutBudget) PendingReminder() *projectAssistantRolloutBudgetReminder {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.state.Exhausted || b.state.LimitTokens <= 0 {
		return nil
	}
	remaining := int64(math.Floor(math.Max(0, float64(b.state.LimitTokens)-b.state.WeightedTokensUsed)))
	index := 0
	for _, threshold := range b.state.ReminderAtRemaining {
		if remaining <= threshold {
			index++
		}
	}
	if b.state.ReminderDelivered &&
		b.state.DeliveredWindowID == b.state.WindowID &&
		b.state.DeliveredReminderIndex >= index {
		return nil
	}
	return &projectAssistantRolloutBudgetReminder{
		RemainingTokens: remaining,
		ReminderIndex:   index,
		WindowID:        b.state.WindowID,
	}
}

func (b *projectEinoAssistantRolloutBudget) DeliverReminder(
	ctx context.Context,
	reminder *projectAssistantRolloutBudgetReminder,
) error {
	if b == nil || reminder == nil {
		return nil
	}
	if b.persistReminder != nil {
		if err := b.persistReminder(ctx, reminder); err != nil {
			return err
		}
	}
	b.mu.Lock()
	if reminder.WindowID != b.state.WindowID {
		b.mu.Unlock()
		return nil
	}
	b.state.ReminderDelivered = true
	b.state.DeliveredWindowID = reminder.WindowID
	b.state.DeliveredReminderIndex = reminder.ReminderIndex
	state := cloneProjectAssistantRolloutBudgetState(b.state)
	b.mu.Unlock()
	return b.persist(ctx, state)
}

func (b *projectEinoAssistantRolloutBudget) RearmAfterCompaction(ctx context.Context) error {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	b.state.WindowID++
	b.state.ReminderDelivered = false
	b.state.DeliveredWindowID = 0
	b.state.DeliveredReminderIndex = 0
	state := cloneProjectAssistantRolloutBudgetState(b.state)
	b.mu.Unlock()
	return b.persist(ctx, state)
}

func (b *projectEinoAssistantRolloutBudget) RecordUsage(ctx context.Context, usage *schema.TokenUsage) error {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	if usage == nil {
		b.state.MissingUsageResponseCount++
	} else {
		nonCachedInput := max(usage.PromptTokens-usage.PromptTokenDetails.CachedTokens, 0)
		b.state.WeightedTokensUsed += float64(max(usage.CompletionTokens, 0))*b.state.SamplingTokenWeight +
			float64(nonCachedInput)*b.state.PrefillTokenWeight
		b.state.Exhausted = b.state.WeightedTokensUsed >= float64(b.state.LimitTokens)
	}
	state := cloneProjectAssistantRolloutBudgetState(b.state)
	b.mu.Unlock()
	if err := b.persist(ctx, state); err != nil {
		return err
	}
	if state.Exhausted {
		return &projectAssistantSessionBudgetExceededError{
			LimitTokens:        state.LimitTokens,
			WeightedTokensUsed: state.WeightedTokensUsed,
		}
	}
	return nil
}

func (b *projectEinoAssistantRolloutBudget) persist(ctx context.Context, state projectAssistantRolloutBudgetState) error {
	if b == nil {
		return nil
	}
	var recorderErr error
	if b.recorder != nil {
		recorderErr = b.recorder.recordRolloutBudget(ctx, state)
	}
	var conversationErr error
	if b.persistState != nil {
		conversationErr = b.persistState(ctx, state)
	}
	return errors.Join(recorderErr, conversationErr)
}

func projectEinoAssistantRolloutBudgetMessage(reminder *projectAssistantRolloutBudgetReminder) *schema.Message {
	if reminder == nil {
		return nil
	}
	return schema.SystemMessage(fmt.Sprintf(
		"<rollout_budget>\nYou have %d weighted tokens left in this App Studio run.\n</rollout_budget>",
		reminder.RemainingTokens,
	))
}

type projectEinoAssistantRolloutBudgetModel struct {
	einomodel.BaseChatModel
	budget *projectEinoAssistantRolloutBudget
}

func projectEinoAssistantBudgetModel(
	base einomodel.BaseChatModel,
	budget *projectEinoAssistantRolloutBudget,
) einomodel.BaseChatModel {
	if base == nil || budget == nil {
		return base
	}
	return &projectEinoAssistantRolloutBudgetModel{BaseChatModel: base, budget: budget}
}

func (m *projectEinoAssistantRolloutBudgetModel) Generate(
	ctx context.Context,
	input []*schema.Message,
	opts ...einomodel.Option,
) (*schema.Message, error) {
	if err := m.budget.ExhaustionError(); err != nil {
		return nil, err
	}
	message, err := m.BaseChatModel.Generate(ctx, input, opts...)
	if err != nil {
		return message, err
	}
	if err := m.budget.RecordUsage(ctx, projectEinoAssistantMessageUsage(message)); err != nil {
		if !projectEinoAssistantRolloutBudgetExceeded(err) {
			return nil, err
		}
	}
	return message, nil
}

func (m *projectEinoAssistantRolloutBudgetModel) Stream(
	ctx context.Context,
	input []*schema.Message,
	opts ...einomodel.Option,
) (*schema.StreamReader[*schema.Message], error) {
	if err := m.budget.ExhaustionError(); err != nil {
		return nil, err
	}
	source, err := m.BaseChatModel.Stream(ctx, input, opts...)
	if err != nil {
		return nil, err
	}
	reader, writer := schema.Pipe[*schema.Message](1)
	go func() {
		defer source.Close()
		defer writer.Close()
		var usage *schema.TokenUsage
		for {
			message, receiveErr := source.Recv()
			if errors.Is(receiveErr, io.EOF) {
				if usageErr := m.budget.RecordUsage(ctx, usage); usageErr != nil {
					if !projectEinoAssistantRolloutBudgetExceeded(usageErr) {
						writer.Send(nil, usageErr)
					}
				}
				return
			}
			if receiveErr != nil {
				writer.Send(nil, receiveErr)
				return
			}
			if current := projectEinoAssistantMessageUsage(message); current != nil {
				usage = current
			}
			writer.Send(message, nil)
		}
	}()
	return reader, nil
}

func projectEinoAssistantMessageUsage(message *schema.Message) *schema.TokenUsage {
	if message == nil || message.ResponseMeta == nil || message.ResponseMeta.Usage == nil {
		return nil
	}
	usage := *message.ResponseMeta.Usage
	return &usage
}

func projectEinoAssistantRolloutBudgetExceeded(err error) bool {
	return errors.Is(err, errProjectAssistantSessionBudgetExceeded)
}

// projectEinoAssistantBudgetLimited reports every bounded-spend exit: the
// per-run weighted-token budget and the organization monthly USD cap. Both
// terminate the run as failed with the budget_limited abort reason.
func projectEinoAssistantBudgetLimited(err error) bool {
	return projectEinoAssistantRolloutBudgetExceeded(err) || projectEinoAssistantOrgSpendCapExceeded(err)
}

// projectAssistantBudgetLimitedErrorInfo names which budget stopped the run
// in the terminal run error so the portal can explain it to the user.
func projectAssistantBudgetLimitedErrorInfo(err error) string {
	if projectEinoAssistantOrgSpendCapExceeded(err) {
		return "org_spend_cap_exceeded"
	}
	return "session_budget_exceeded"
}

func projectEinoAssistantIterationLimited(err error) bool {
	return projectEinoAssistantMaxIterationsExceeded(err)
}
