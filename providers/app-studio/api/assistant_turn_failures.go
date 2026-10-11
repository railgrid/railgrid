// Copyright 2026 The Railgrid Authors.
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
	"encoding/json"
	"errors"
	"reflect"
	"strings"

	"k8s.io/klog/v2"

	"github.com/railgrid/provider-app-studio/store"
)

// assistantThreadTurnMaxFailures bounds the failure details carried on a turn.
// failedItems always reports the full count.
const assistantThreadTurnMaxFailures = 5

// assistantThreadTurnFailure is the bounded, product-facing description of one
// tool item that ended failed. It reuses the item's existing public action
// title and diagnostic; no private tool output crosses into the turn.
type assistantThreadTurnFailure struct {
	ItemID      string `json:"itemID"`
	Title       string `json:"title,omitempty"`
	Category    string `json:"category,omitempty"`
	Message     string `json:"message,omitempty"`
	ReferenceID string `json:"referenceID,omitempty"`
}

// assistantThreadTurnView is the public turn read model for terminal turn
// events and the turn detail route. The failure summary is derived from the
// turn's durable tool items rather than stored on the turn row, so it needs no
// schema change and stays consistent across store implementations. It is
// additive: status keeps its meaning, and a completed turn whose steps failed
// is still completed.
type assistantThreadTurnView struct {
	store.AssistantTurn
	// FailedItems counts the turn's tool items whose status is failed. A
	// failure that a linked retry later repaired is recovered, not failed, and
	// a step the user declined at an approval prompt is rejected, not failed.
	FailedItems int `json:"failedItems,omitempty"`
	// RecoveredItems counts tool items that failed and were then repaired by
	// a linked (recoveryOf) retry in the same turn.
	RecoveredItems int `json:"recoveredItems,omitempty"`
	// RejectedItems counts tool items that did not run because the approval
	// they asked for was denied. The thread item reads failed; the action's
	// own status says rejected.
	RejectedItems int `json:"rejectedItems,omitempty"`
	// Failures describes up to assistantThreadTurnMaxFailures of the failed
	// items, in item order.
	Failures []assistantThreadTurnFailure `json:"failures,omitempty"`
}

// assistantThreadTurnToolItems keeps the latest state of each tool item in one
// turn, in first-seen order. The mirror maintains it as part of its
// reconstructed state, and the turn detail route rebuilds it from the turn's
// events, so both surfaces summarize the same durable items.
type assistantThreadTurnToolItems struct {
	order []string
	items map[string]assistantThreadItem
}

func assistantThreadToolItemType(itemType string) bool {
	return itemType == assistantThreadEventDynamicToolCall || itemType == assistantThreadEventModelInput
}

func (t *assistantThreadTurnToolItems) observe(itemID string, item assistantThreadItem) {
	if t == nil || !assistantThreadToolItemType(item.Type) {
		return
	}
	itemID = strings.TrimSpace(itemID)
	if itemID == "" {
		itemID = strings.TrimSpace(item.ID)
	}
	if itemID == "" {
		return
	}
	item.ID = itemID
	if t.items == nil {
		t.items = map[string]assistantThreadItem{}
	}
	if _, exists := t.items[itemID]; !exists {
		t.order = append(t.order, itemID)
	}
	t.items[itemID] = item
}

func (t *assistantThreadTurnToolItems) observeEvent(event store.AssistantThreadEvent) {
	if t == nil || event.ItemID == "" || (event.Type != assistantThreadEventItemStarted && event.Type != assistantThreadEventItemCompleted) {
		return
	}
	var envelope struct {
		Item assistantThreadItem `json:"item"`
	}
	if json.Unmarshal(event.Payload, &envelope) != nil {
		return
	}
	t.observe(event.ItemID, envelope.Item)
}

// list returns the tracked items as the read model presents them once the turn
// has ended with terminalStatus: an image model input still pending at the
// terminal boundary is closed exactly as materializeAssistantThreadItems does.
func (t *assistantThreadTurnToolItems) list(terminalStatus string) []assistantThreadItem {
	if t == nil || len(t.order) == 0 {
		return nil
	}
	items := make([]assistantThreadItem, 0, len(t.order))
	for _, itemID := range t.order {
		item := t.items[itemID]
		if terminalStatus != "" && item.Type == assistantThreadEventModelInput && assistantThreadModelInputItemIsInProgress(item) {
			repairMaterializedAssistantModelInput(&item, terminalStatus)
		}
		items = append(items, item)
	}
	return items
}

// newAssistantThreadTurnView attaches the failure summary for toolItems to
// turn. Recovery is not inferred here: when a linked retry succeeds, the action
// feed already relabels the original failure as recovered (and a retry still
// open when the run ends is finalized as failed), so an item's own status is
// the whole signal.
func newAssistantThreadTurnView(turn store.AssistantTurn, toolItems *assistantThreadTurnToolItems) assistantThreadTurnView {
	view := assistantThreadTurnView{AssistantTurn: turn}
	terminalStatus := ""
	if turn.Status != store.AssistantTurnStatusInProgress {
		terminalStatus = string(turn.Status)
	}
	for _, item := range toolItems.list(terminalStatus) {
		var action projectAssistantActionFeedItem
		if len(item.Data) > 0 {
			_ = json.Unmarshal(item.Data, &action)
		}
		if item.Status != "failed" {
			if action.Status == projectAssistantActionFeedStatusRecovered {
				view.RecoveredItems++
			}
			continue
		}
		if action.Status == projectAssistantActionFeedStatusRejected {
			view.RejectedItems++
			continue
		}
		view.FailedItems++
		if len(view.Failures) >= assistantThreadTurnMaxFailures {
			continue
		}
		failure := assistantThreadTurnFailure{ItemID: item.ID, Title: action.Title}
		if failure.Title == "" {
			failure.Title = item.Content
		}
		if action.Diagnostic != nil {
			failure.Category = action.Diagnostic.Category
			failure.Message = action.Diagnostic.Message
			failure.ReferenceID = action.Diagnostic.ReferenceID
		}
		view.Failures = append(view.Failures, failure)
	}
	return view
}

// loadAssistantThreadTurnToolItems reads one turn's tool items from the
// durable event stream. It starts at the turn's turn.started event (an
// indexed lookup), stops the chronological scan at the next turn, then uses a
// bounded exact-turn lookup for any post-terminal receipt settlements. A turn
// without a turn.started event (older repair paths) is scanned from the
// beginning of the thread.
func (s *Server) loadAssistantThreadTurnToolItems(ctx context.Context, scope store.Scope, threadID, turnID string) (*assistantThreadTurnToolItems, error) {
	toolItems := &assistantThreadTurnToolItems{}
	after := int64(0)
	if start, err := s.store.GetAssistantThreadTurnStartSequence(ctx, scope, threadID, turnID); err == nil {
		after = start - 1
	} else if !errors.Is(err, store.ErrAssistantTurnNotFound) {
		return nil, err
	}
	seen := false
	terminalSeen := false
	terminalSequence := int64(0)
	stoppedAtNextTurn := false
	for page := 0; page < assistantThreadEventWindowMaxPages; page++ {
		events, err := s.store.ListAssistantThreadEvents(ctx, scope, threadID, after, assistantThreadEventWindowPageSize)
		if err != nil {
			return nil, err
		}
		for _, event := range events {
			if event.TurnID != turnID {
				if seen && event.TurnID != "" && event.Type == assistantThreadEventTurnStarted {
					stoppedAtNextTurn = true
					break
				}
				continue
			}
			seen = true
			switch event.Type {
			case assistantThreadEventTurnCompleted, assistantThreadEventTurnFailed, assistantThreadEventTurnInterrupted:
				terminalSeen = true
				if terminalSequence == 0 {
					terminalSequence = event.Sequence
				}
				continue
			}
			if terminalSeen && (event.Type != assistantThreadEventItemCompleted || event.ItemID == "" || !assistantThreadReceiptSettlementEvent(event)) {
				continue
			}
			if terminalSeen {
				prior, existed := toolItems.items[event.ItemID]
				if !assistantThreadReceiptSettlementCanProjectToolItem(event, turnID, prior, existed) {
					continue
				}
			}
			toolItems.observeEvent(event)
		}
		if stoppedAtNextTurn {
			break
		}
		if len(events) < assistantThreadEventWindowPageSize {
			break
		}
		after = events[len(events)-1].Sequence
	}
	if terminalSequence > 0 {
		candidate := cloneAssistantThreadTurnToolItems(toolItems)
		if err := s.loadAssistantThreadTurnReceiptSettlements(ctx, scope, threadID, turnID, terminalSequence, candidate); err != nil {
			klog.FromContext(ctx).Error(err, "assistant turn receipt settlement overlay unavailable; returning base projection", "threadID", threadID, "turnID", turnID)
		} else {
			toolItems = candidate
		}
	}
	return toolItems, nil
}

func cloneAssistantThreadTurnToolItems(source *assistantThreadTurnToolItems) *assistantThreadTurnToolItems {
	clone := &assistantThreadTurnToolItems{items: make(map[string]assistantThreadItem)}
	if source == nil {
		return clone
	}
	clone.order = append(clone.order, source.order...)
	for id, item := range source.items {
		clone.items[id] = item
	}
	return clone
}

func (s *Server) loadAssistantThreadTurnReceiptSettlements(ctx context.Context, scope store.Scope, threadID, turnID string, afterSequence int64, toolItems *assistantThreadTurnToolItems) error {
	for page := 0; page < assistantThreadEventWindowMaxPages; page++ {
		events, err := s.store.ListAssistantThreadTurnEventsAfter(ctx, scope, threadID, turnID, assistantThreadEventItemCompleted, afterSequence, assistantThreadEventWindowPageSize)
		if err != nil {
			return err
		}
		for _, event := range events {
			if event.Sequence <= afterSequence || event.Type != assistantThreadEventItemCompleted || event.TurnID != turnID {
				return errors.New("assistant turn receipt settlement lookup returned an out-of-order event")
			}
			afterSequence = event.Sequence
			if !assistantThreadReceiptSettlementEvent(event) {
				continue
			}
			prior, exists := toolItems.items[event.ItemID]
			if !assistantThreadReceiptSettlementCanProjectToolItem(event, turnID, prior, exists) {
				continue
			}
			toolItems.observeEvent(event)
		}
		if len(events) < assistantThreadEventWindowPageSize {
			return nil
		}
	}
	return errors.New("assistant turn receipt settlement history exceeded its bounded page window")
}

// assistantThreadReceiptSettlementItem accepts only a canonical, sequence-bearing
// exec receipt projection. Such events are written after the server validates
// the exact dispatched call and its durable terminal result.
func assistantThreadReceiptSettlementItem(event store.AssistantThreadEvent, turnID string) (assistantThreadItem, projectAssistantActionFeedItem, bool) {
	if !assistantThreadReceiptSettlementEvent(event) || event.TurnID != turnID || event.ItemID == "" {
		return assistantThreadItem{}, projectAssistantActionFeedItem{}, false
	}
	var envelope struct {
		Item assistantThreadItem `json:"item"`
	}
	if json.Unmarshal(event.Payload, &envelope) != nil {
		return assistantThreadItem{}, projectAssistantActionFeedItem{}, false
	}
	item := envelope.Item
	if item.ID != event.ItemID || item.TurnID != turnID || item.Type != assistantThreadEventDynamicToolCall ||
		item.AssistantMessageID == "" || item.Sequence < 1 {
		return assistantThreadItem{}, projectAssistantActionFeedItem{}, false
	}
	var action projectAssistantActionFeedItem
	if json.Unmarshal(item.Data, &action) != nil || action.ID == "" || action.Exec == nil ||
		int64(action.Sequence) != item.Sequence || !projectAssistantToolCallCanSettleStopping(action.Exec.Status) ||
		!projectAssistantExecCommandReceiptObserved(action.Exec) ||
		item.ID != assistantThreadDynamicToolItemID(item.AssistantMessageID, action.ID) ||
		item.Content != action.Title || item.Status != projectAssistantThreadToolItemStatus(action.Status) {
		return assistantThreadItem{}, projectAssistantActionFeedItem{}, false
	}
	return item, action, true
}

func assistantThreadReceiptSettlementCanCreateToolItem(event store.AssistantThreadEvent, turnID string) bool {
	_, _, ok := assistantThreadReceiptSettlementItem(event, turnID)
	return ok
}

func assistantThreadReceiptSettlementCanProjectToolItem(event store.AssistantThreadEvent, turnID string, prior assistantThreadItem, exists bool) bool {
	settled, settledAction, ok := assistantThreadReceiptSettlementItem(event, turnID)
	if !ok {
		return false
	}
	if !exists {
		return true
	}
	if prior.ID != settled.ID || prior.TurnID != turnID || prior.Type != assistantThreadEventDynamicToolCall ||
		prior.AssistantMessageID != settled.AssistantMessageID || prior.Sequence != settled.Sequence {
		return false
	}
	var priorAction projectAssistantActionFeedItem
	if json.Unmarshal(prior.Data, &priorAction) != nil {
		return false
	}
	expectedPriorStatus := projectAssistantThreadToolItemStatus(priorAction.Status)
	if priorAction.Status == projectAssistantActionFeedStatusRunning {
		expectedPriorStatus = "in_progress"
	}
	if priorAction.ID == "" || priorAction.ID != settledAction.ID ||
		priorAction.Exec == nil || int64(priorAction.Sequence) != prior.Sequence || prior.Content != priorAction.Title ||
		prior.Status != expectedPriorStatus ||
		!projectAssistantExecActionIdentityMatches(priorAction.Exec, settledAction.Exec) {
		return false
	}
	if reflect.DeepEqual(priorAction, settledAction) && prior.Status == settled.Status && prior.Content == settled.Content {
		return true
	}
	if prior.Status == "in_progress" && priorAction.Status == projectAssistantActionFeedStatusRunning &&
		!projectAssistantExecStatusTerminal(priorAction.Exec.Status) {
		return true
	}
	return prior.Status == "failed" && projectAssistantActionFeedHasUnobservedExecResult(priorAction)
}

func projectAssistantInterruptedExecActionAlreadySettled(metadata map[string]any, event projectToolCallStreamEvent) bool {
	_, settled := projectAssistantInterruptedExecSettledAction(metadata, event)
	return settled
}

func projectAssistantInterruptedExecSettledAction(metadata map[string]any, event projectToolCallStreamEvent) (projectAssistantActionFeedItem, bool) {
	want := projectAssistantActionFeedItemFromToolCall(event)
	if want.Exec == nil || want.ID == "" {
		return projectAssistantActionFeedItem{}, false
	}
	for _, action := range projectAssistantActionFeedFromMetadata(metadata[projectMessageMetadataAssistantActionFeed]) {
		if action.ID != want.ID || action.Sequence < 1 {
			continue
		}
		want.Sequence = action.Sequence
		if reflect.DeepEqual(action, want) {
			return action, true
		}
	}
	return projectAssistantActionFeedItem{}, false
}
