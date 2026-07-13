// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package api

import "testing"

func TestParseTelegramUpdate(t *testing.T) {
	text, chat := parseTelegramUpdate([]byte(`{"message":{"text":"hello","chat":{"id":123456},"from":{"is_bot":false}}}`))
	if text != "hello" || chat != "123456" {
		t.Fatalf("got (%q,%q)", text, chat)
	}
	// Bot-authored messages are dropped (loop protection).
	if text, _ := parseTelegramUpdate([]byte(`{"message":{"text":"hi","chat":{"id":1},"from":{"is_bot":true}}}`)); text != "" {
		t.Fatal("bot message must be ignored")
	}
	// Non-text updates (photos, joins) are dropped.
	if text, _ := parseTelegramUpdate([]byte(`{"message":{"chat":{"id":1},"from":{"is_bot":false}}}`)); text != "" {
		t.Fatal("non-text update must be ignored")
	}
	if text, _ := parseTelegramUpdate([]byte(`garbage`)); text != "" {
		t.Fatal("garbage must be ignored")
	}
}

func TestParseSlackEvent(t *testing.T) {
	text, ch := parseSlackEvent([]byte(`{"type":"event_callback","event":{"type":"message","text":"hey","channel":"C123"}}`))
	if text != "hey" || ch != "C123" {
		t.Fatalf("got (%q,%q)", text, ch)
	}
	// Our own bot replies come back through the Events API — must be ignored.
	if text, _ := parseSlackEvent([]byte(`{"type":"event_callback","event":{"type":"message","text":"echo","channel":"C123","bot_id":"B1"}}`)); text != "" {
		t.Fatal("bot message must be ignored")
	}
	// Subtyped messages (edits, joins) ignored.
	if text, _ := parseSlackEvent([]byte(`{"type":"event_callback","event":{"type":"message","subtype":"message_changed","text":"x","channel":"C123"}}`)); text != "" {
		t.Fatal("subtyped message must be ignored")
	}
}
