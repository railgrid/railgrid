// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package engine

import (
	"fmt"
	"unicode/utf8"

	"github.com/cloudwego/eino/schema"
)

// Within-turn context control.
//
// Session compaction (the api layer) handles conversations that grow across
// turns. This handles the other case: ONE turn whose tool observations are
// individually large — a research fan-out joining ten workers, a page fetched in
// full, a long log tail. Those observations live only in this turn's wire
// conversation, never in the transcript, so nothing outside the loop can see or
// shrink them.
//
// The approach is to clip the CONTENT of the oldest tool observations in place,
// never to remove messages. The OpenAI wire format requires every assistant
// tool_call to be answered by a matching tool message, so dropping one would
// make the next request invalid; shrinking one only costs detail the model has
// already had a chance to use.

const (
	// trimKeepRecentTools is how many of the newest tool observations are never
	// clipped. The most recent results are what the model is reasoning about now.
	trimKeepRecentTools = 4
	// trimStubChars is how much of a clipped observation survives — enough for the
	// model to remember what the call was about and that it can no longer rely on
	// the detail.
	trimStubChars = 400
)

// estimateMessageTokens approximates a wire message's cost, including a small
// per-message envelope allowance for role and tool-call metadata.
func estimateMessageTokens(m *schema.Message) int {
	if m == nil {
		return 0
	}
	n := estimateTokens(m.Content) + 8
	for _, tc := range m.ToolCalls {
		n += estimateTokens(tc.Function.Name) + estimateTokens(tc.Function.Arguments) + 8
	}
	return n
}

// estimateTokens is the same 4-bytes-per-token heuristic the llm package uses,
// duplicated here to keep the engine free of a dependency on it (the engine is
// provider-agnostic and SDK-portable).
func estimateTokens(s string) int {
	if s == "" {
		return 0
	}
	return (len(s) + 3) / 4
}

// trimConversation shrinks in place until its estimated size fits budget,
// clipping the oldest tool observations first. A budget of 0 disables trimming.
// It reports how many observations it clipped.
//
// Only tool messages are touched: the system prompt is policy, and user and
// assistant turns are the conversation itself. It escalates in two passes —
// first the observations outside the recent window, then, if that was not
// enough, the recent ones too, always sparing the single newest. Escalating
// matters because a turn can consist almost entirely of large results (a
// research fan-out, several full page fetches), and there the polite pass alone
// cannot get under the limit. If even that is not enough the turn proceeds
// oversized: the model's own context-length error is a better outcome than
// deleting the question it was asked.
func trimConversation(in []*schema.Message, budget int) int {
	if budget <= 0 || len(in) == 0 {
		return 0
	}
	total := 0
	for _, m := range in {
		total += estimateMessageTokens(m)
	}
	if total <= budget {
		return 0
	}

	// Clippable observations, oldest first.
	var idx []int
	for i, m := range in {
		if m != nil && m.Role == schema.Tool && len(m.Content) > trimStubChars {
			idx = append(idx, i)
		}
	}
	if len(idx) == 0 {
		return 0
	}

	clip := func(candidates []int) int {
		n := 0
		for _, i := range candidates {
			if total <= budget {
				break
			}
			m := in[i]
			if len(m.Content) <= trimStubChars {
				continue // already clipped in an earlier pass
			}
			before := estimateMessageTokens(m)
			m.Content = clipObservation(m.Content, m.ToolName)
			total -= before - estimateMessageTokens(m)
			n++
		}
		return n
	}

	// Pass 1: everything outside the recent window.
	clipped := 0
	if len(idx) > trimKeepRecentTools {
		clipped += clip(idx[:len(idx)-trimKeepRecentTools])
	}
	// Pass 2: still over budget — reach into the recent window as well, but never
	// touch the newest observation, which is what the model is reasoning about.
	if total > budget && len(idx) > 1 {
		clipped += clip(idx[:len(idx)-1])
	}
	return clipped
}

// clipObservation replaces an observation with its opening plus an explicit note
// that the rest was dropped, so the model treats the gap as missing information
// rather than as the whole answer.
func clipObservation(content, toolName string) string {
	head := content
	if len(head) > trimStubChars {
		// Don't split a UTF-8 rune.
		cut := trimStubChars
		for cut > 0 && !utf8.RuneStart(head[cut]) {
			cut--
		}
		head = head[:cut]
	}
	label := "this tool result"
	if toolName != "" {
		label = fmt.Sprintf("the %s result", toolName)
	}
	return fmt.Sprintf("%s\n\n[%s was shortened to keep this turn inside the model's context window. If you still need the detail, call the tool again for just the part you need.]", head, label)
}
