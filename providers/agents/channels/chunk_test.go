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
	"testing"
)

// The bug this exists for: a research answer runs tens of thousands of
// characters, Discord accepts 2000, and truncating meant the reader got the
// first 8% with nothing to say the rest existed.
func TestChunkMessage(t *testing.T) {
	t.Run("short text is one piece", func(t *testing.T) {
		got := chunkMessage("hello", 2000)
		if len(got) != 1 || got[0] != "hello" {
			t.Fatalf("got %q", got)
		}
	})

	t.Run("no limit means no splitting", func(t *testing.T) {
		long := strings.Repeat("x", 50000)
		if got := chunkMessage(long, 0); len(got) != 1 {
			t.Fatalf("got %d pieces, want 1 — email has no practical limit", len(got))
		}
	})

	t.Run("nothing is lost", func(t *testing.T) {
		// Paragraphs of prose, well over the Discord limit.
		var b strings.Builder
		for i := range 200 {
			b.WriteString("Paragraph ")
			b.WriteString(strings.Repeat("word ", 20))
			b.WriteString("marker")
			b.WriteString(string(rune('A' + i%26)))
			b.WriteString(".\n\n")
		}
		text := b.String()
		parts := chunkMessage(text, 2000)
		if len(parts) < 5 {
			t.Fatalf("expected several pieces, got %d", len(parts))
		}
		for i, p := range parts {
			if len(p) > 2000 {
				t.Fatalf("piece %d is %d chars, over the limit", i, len(p))
			}
		}
		// Every word survives, in order. Whitespace at the joins is normalized by
		// the split, so compare on the content.
		want := strings.Join(strings.Fields(text), " ")
		got := strings.Join(strings.Fields(strings.Join(parts, "\n\n")), " ")
		if got != want {
			t.Fatalf("content changed across the split\nwant %d chars\ngot  %d chars", len(want), len(got))
		}
	})

	t.Run("splits on a paragraph boundary when there is one", func(t *testing.T) {
		text := strings.Repeat("a", 1500) + "\n\n" + strings.Repeat("b", 1500)
		parts := chunkMessage(text, 2000)
		if len(parts) != 2 {
			t.Fatalf("got %d pieces, want 2", len(parts))
		}
		if strings.Contains(parts[0], "b") || strings.Contains(parts[1], "a") {
			t.Fatal("the split should fall on the blank line between the blocks")
		}
	})

	t.Run("falls back to a sentence end", func(t *testing.T) {
		text := strings.Repeat("word ", 300) + "End of it. " + strings.Repeat("more ", 300)
		parts := chunkMessage(text, 2000)
		if !strings.HasSuffix(strings.TrimSpace(parts[0]), "End of it.") {
			t.Fatalf("first piece should end at the sentence: %q", parts[0][max(0, len(parts[0])-40):])
		}
	})

	t.Run("hard-cuts unbroken text without splitting a rune", func(t *testing.T) {
		// No spaces, no newlines, multi-byte throughout.
		text := strings.Repeat("é", 3000)
		parts := chunkMessage(text, 2000)
		if len(parts) < 2 {
			t.Fatal("expected a hard cut")
		}
		for i, p := range parts {
			if !utf8ValidString(p) {
				t.Fatalf("piece %d is not valid UTF-8 — a rune was split", i)
			}
		}
	})
}

func utf8ValidString(s string) bool {
	for _, r := range s {
		if r == '�' {
			return false
		}
	}
	return true
}

func TestMessageLimit(t *testing.T) {
	// The real caps: exceeding them is an API error, not a soft truncation.
	if messageLimit("discord") != 2000 {
		t.Fatal("discord caps at 2000")
	}
	if messageLimit("telegram") != 4096 {
		t.Fatal("telegram caps at 4096")
	}
	// Email has no practical limit, so it is never split.
	if messageLimit("smtp") != 0 {
		t.Fatal("smtp should not be chunked")
	}
}
