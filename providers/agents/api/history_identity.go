// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package api

import (
	"fmt"
	"strings"

	"github.com/railgrid/provider-agents/engine"
	"github.com/railgrid/provider-agents/store"
)

// enrichRunHistoryIdentities restores durable transcript identity on messages
// produced during the current engine turn. The model-facing history contains
// messages emitted after the API callback ran, so those messages have no store
// IDs until they are matched to rows persisted by this run. Matching is always
// run-scoped to avoid binding a parallel run's messages by coincidental tool
// call ID or content.
func enrichRunHistoryIdentities(history []engine.Message, rows []store.Message, runID string) ([]engine.Message, error) {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return nil, fmt.Errorf("run ID is required to resolve transcript identities")
	}

	rowByID := make(map[string]store.Message, len(rows))
	sameRunUsers := make([]store.Message, 0, 1)
	sameRunCalls := make([]store.Message, 0, 2)
	sameRunResults := make([]store.Message, 0, 2)
	for _, row := range rows {
		id := strings.TrimSpace(row.ID)
		if id != "" {
			if _, exists := rowByID[id]; exists {
				return nil, fmt.Errorf("transcript contains duplicate source message ID %q", id)
			}
			rowByID[id] = row
		}
		if row.RunID != runID {
			continue
		}
		switch row.Role {
		case "user":
			sameRunUsers = append(sameRunUsers, row)
		case "assistant":
			if len(historyToolCalls(row)) > 0 {
				sameRunCalls = append(sameRunCalls, row)
			}
		case "tool":
			if messageToolCallID(row) != "" {
				sameRunResults = append(sameRunResults, row)
			}
		}
	}

	out := append([]engine.Message(nil), history...)
	latestUserWithoutID := -1
	for i := range out {
		if out[i].Role == engine.RoleUser && strings.TrimSpace(out[i].ID) == "" && !out[i].Ephemeral && !isCompactionSummary(out[i]) {
			latestUserWithoutID = i
		}
	}

	for i := range out {
		message := &out[i]
		if id := strings.TrimSpace(message.ID); id != "" {
			row, ok := rowByID[id]
			if !ok {
				return nil, fmt.Errorf("history message %d refers to unknown source message ID %q", i, id)
			}
			if message.Sequence != 0 && row.Sequence != 0 && message.Sequence != row.Sequence {
				return nil, fmt.Errorf("history message %d source sequence %d does not match stored sequence %d", i, message.Sequence, row.Sequence)
			}
			if message.Sequence == 0 {
				message.Sequence = row.Sequence
			}
			continue
		}

		switch message.Role {
		case engine.RoleUser:
			if message.Ephemeral {
				continue
			}
			matches := matchingRunUserRows(sameRunUsers, message.Content)
			if len(matches) > 1 {
				return nil, fmt.Errorf("history message %d ambiguously matches %d user rows from run %q", i, len(matches), runID)
			}
			if len(matches) == 1 {
				applySourceIdentity(message, matches[0])
			} else if i == latestUserWithoutID && len(sameRunUsers) > 0 {
				return nil, fmt.Errorf("latest unbound user message does not match the persisted request for run %q", runID)
			}
		case engine.RoleAssistant:
			if len(message.ToolCalls) == 0 {
				continue
			}
			matches := matchingRunAssistantCallRows(sameRunCalls, *message)
			if len(matches) != 1 {
				return nil, fmt.Errorf("history message %d has %d matching assistant tool-call rows for run %q", i, len(matches), runID)
			}
			applySourceIdentity(message, matches[0])
		case engine.RoleTool:
			callID := strings.TrimSpace(message.ToolCallID)
			if callID == "" {
				return nil, fmt.Errorf("history message %d has a tool result without a call ID", i)
			}
			matches := matchingRunToolRows(sameRunResults, callID)
			if len(matches) > 1 {
				return nil, fmt.Errorf("history message %d ambiguously matches %d tool results for run %q", i, len(matches), runID)
			}
			if len(matches) == 1 {
				if matches[0].Content != message.Content {
					return nil, fmt.Errorf("history message %d content does not match its persisted tool result", i)
				}
				applySourceIdentity(message, matches[0])
			} else if message.Content != unavailableToolResult {
				return nil, fmt.Errorf("history message %d has no persisted tool result for call %q in run %q", i, callID, runID)
			}
		}
	}

	return out, nil
}

func matchingRunUserRows(rows []store.Message, content string) []store.Message {
	var matches []store.Message
	for _, row := range rows {
		if row.Content == content {
			matches = append(matches, row)
		}
	}
	return matches
}

func matchingRunAssistantCallRows(rows []store.Message, message engine.Message) []store.Message {
	want := make([]string, 0, len(message.ToolCalls))
	seen := make(map[string]bool, len(message.ToolCalls))
	for _, call := range message.ToolCalls {
		id := strings.TrimSpace(call.ID)
		if id == "" || seen[id] {
			return nil
		}
		seen[id] = true
		want = append(want, id)
	}
	var matches []store.Message
	for _, row := range rows {
		calls := historyToolCalls(row)
		if len(calls) != len(want) || row.Content != message.Content {
			continue
		}
		match := true
		for i, call := range calls {
			if strings.TrimSpace(call.ID) != want[i] {
				match = false
				break
			}
		}
		if match {
			matches = append(matches, row)
		}
	}
	return matches
}

func matchingRunToolRows(rows []store.Message, callID string) []store.Message {
	var matches []store.Message
	for _, row := range rows {
		if messageToolCallID(row) == callID {
			matches = append(matches, row)
		}
	}
	return matches
}

func applySourceIdentity(message *engine.Message, row store.Message) {
	message.ID = row.ID
	if message.Sequence == 0 {
		message.Sequence = row.Sequence
	}
}
