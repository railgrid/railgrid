// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package llm

import "testing"

func TestLookupModel(t *testing.T) {
	cases := []struct {
		in     string
		wantID string
		wantOK bool
	}{
		{"gpt-4o", "gpt-4o", true},
		{"GPT-4o", "gpt-4o", true},
		{"openai/gpt-4o", "gpt-4o", true},
		{"gpt-4o-2024-08-06", "gpt-4o", true},          // date suffix → prefix match
		{"gpt-4o-mini-2024-07-18", "gpt-4o-mini", true}, // must prefer the longer id
		{"openrouter/anthropic/claude-sonnet-4", "claude-sonnet-4", true},
		{"totally-unknown-model", "", false},
		{"", "", false},
	}
	for _, c := range cases {
		mi, ok := LookupModel(c.in)
		if ok != c.wantOK {
			t.Errorf("LookupModel(%q) ok=%v, want %v", c.in, ok, c.wantOK)
			continue
		}
		if ok && mi.ID != c.wantID {
			t.Errorf("LookupModel(%q) = %q, want %q", c.in, mi.ID, c.wantID)
		}
	}
}

func TestCostMicros(t *testing.T) {
	// gpt-4o: $2.50 / 1M input, $10.00 / 1M output.
	// 1M input + 1M output = $2.50 + $10.00 = $12.50 = 12_500_000 micros.
	if got := CostMicros("gpt-4o", 1_000_000, 1_000_000); got != 12_500_000 {
		t.Errorf("CostMicros(gpt-4o, 1M, 1M) = %d, want 12500000", got)
	}
	// 1000 input tokens on gpt-4o = 1000 * 2.50 = 2500 micros = $0.0025.
	if got := CostMicros("gpt-4o", 1000, 0); got != 2500 {
		t.Errorf("CostMicros(gpt-4o, 1000, 0) = %d, want 2500", got)
	}
	// Unknown model → 0 (never fabricate a price).
	if got := CostMicros("mystery-model", 1000, 1000); got != 0 {
		t.Errorf("CostMicros(unknown) = %d, want 0", got)
	}
}
