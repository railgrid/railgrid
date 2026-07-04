/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package github

import (
	"strings"
	"testing"
)

func TestIsBinaryContent(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  []byte
		want bool
	}{
		{"empty is text", nil, false},
		{"plain text", []byte("package main\n"), false},
		{"utf8 multibyte", []byte("héllo → wörld"), false},
		{"nul byte", []byte{0x89, 'P', 'N', 'G', 0x00}, true},
		{"invalid utf8", []byte{0xff, 0xfe, 0x41}, true},
	} {
		if got := isBinaryContent(tc.raw); got != tc.want {
			t.Errorf("%s: isBinaryContent = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestAppendSkipBounds(t *testing.T) {
	var skipped []string
	for i := range 300 {
		skipped = appendSkip(skipped, strings.Repeat("x", 3)+"-"+string(rune('a'+i%26)))
	}
	if len(skipped) != 100 {
		t.Fatalf("skipped length = %d, want 100 (bounded)", len(skipped))
	}
	if skipped[99] != "(more paths skipped)" {
		t.Errorf("last entry = %q, want the overflow marker", skipped[99])
	}
}
