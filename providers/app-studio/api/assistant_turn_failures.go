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
	"strings"

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
// indexed lookup) and stops at the turn's terminal event or the next turn,
// because turns in one thread are serialized. The read is bounded like the
// history window; a turn without a turn.started event (older repair paths) is
// scanned from the beginning of the thread.
func (s *Server) loadAssistantThreadTurnToolItems(ctx context.Context, scope store.Scope, threadID, turnID string) (*assistantThreadTurnToolItems, error) {
	toolItems := &assistantThreadTurnToolItems{}
	after := int64(0)
	if start, err := s.store.GetAssistantThreadTurnStartSequence(ctx, scope, threadID, turnID); err == nil {
		after = start - 1
	} else if !errors.Is(err, store.ErrAssistantTurnNotFound) {
		return nil, err
	}
	seen := false
	for page := 0; page < assistantThreadEventWindowMaxPages; page++ {
		events, err := s.store.ListAssistantThreadEvents(ctx, scope, threadID, after, assistantThreadEventWindowPageSize)
		if err != nil {
			return nil, err
		}
		for _, event := range events {
			if event.TurnID != turnID {
				if seen && event.TurnID != "" && event.Type == assistantThreadEventTurnStarted {
					return toolItems, nil
				}
				continue
			}
			seen = true
			switch event.Type {
			case assistantThreadEventTurnCompleted, assistantThreadEventTurnFailed, assistantThreadEventTurnInterrupted:
				return toolItems, nil
			}
			toolItems.observeEvent(event)
		}
		if len(events) < assistantThreadEventWindowPageSize {
			return toolItems, nil
		}
		after = events[len(events)-1].Sequence
	}
	return toolItems, nil
}
