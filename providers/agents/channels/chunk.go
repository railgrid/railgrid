// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package channels

import (
	"strings"
	"unicode/utf8"
)

// Long answers are split across messages rather than cut off.
//
// Every chat backend caps one message (Discord at 2000 characters, Telegram at
// 4096), and truncating there is fine for "the deploy finished" and ruinous for
// anything substantial: a research answer runs tens of thousands of characters,
// so the reader would get the first few percent and no indication the rest ever
// existed. Splitting is the only honest option — the agent did the work, and the
// person asked for it.
//
// The split prefers a paragraph break, then a line break, then a sentence, and
// only falls back to a hard cut when a single run of text has none of those. It
// never splits a UTF-8 rune.

// chunkMessage splits text into pieces of at most limit characters, breaking at
// the most natural boundary available. A limit of 0 or text that already fits
// returns a single piece.
func chunkMessage(text string, limit int) []string {
	if limit <= 0 || len(text) <= limit {
		return []string{text}
	}
	var out []string
	rest := text
	for len(rest) > limit {
		cut := breakPoint(rest, limit)
		out = append(out, strings.TrimRight(rest[:cut], " \n"))
		rest = strings.TrimLeft(rest[cut:], "\n")
	}
	if strings.TrimSpace(rest) != "" {
		out = append(out, rest)
	}
	return out
}

// breakPoint picks where to cut the first limit bytes of s, preferring a
// paragraph break, then a newline, then a sentence end. Anything found in the
// last 40% of the window is close enough to the limit to be worth using; a
// boundary earlier than that would waste most of a message.
func breakPoint(s string, limit int) int {
	window := s[:limit]
	minCut := limit * 3 / 5
	for _, sep := range []string{"\n\n", "\n", ". ", "! ", "? "} {
		if i := strings.LastIndex(window, sep); i >= minCut {
			return i + len(sep)
		}
	}
	// No natural boundary: cut at the limit without splitting a rune.
	cut := limit
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	if cut == 0 {
		cut = limit
	}
	return cut
}
