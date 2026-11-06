// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package api

import (
	"testing"

	"github.com/railgrid/provider-agents/store"
)

func TestPublicRichTranscriptKeepsProseAndChipsWithoutExecutionMarkers(t *testing.T) {
	parts := []agentChatContentPart{{Type: "text", Text: "Review this file. "}, {Type: "skill", SkillID: "workspace:review"}}
	internal := store.Message{Role: "user", Content: "Review this file.\nInternal input marker: {\"skillID\":\"workspace:review\"}", Metadata: map[string]any{"contentParts": parts}}
	legacy := store.Message{Role: "user", Content: "Legacy plain message"}
	public := publicTranscriptMessages([]store.Message{internal, legacy})
	if public[0].Content != "Review this file." || public[0].Metadata["contentParts"] == nil {
		t.Fatalf("public rich message = %#v", public[0])
	}
	if internal.Content == public[0].Content || legacy.Content != public[1].Content {
		t.Fatal("display projection mutated execution history or a legacy message")
	}
	onlyChip := store.Message{Role: "user", Content: "Internal input marker", Metadata: map[string]any{"contentParts": []agentChatContentPart{{Type: "skill", SkillID: "workspace:review"}}}}
	if got := publicTranscriptMessages([]store.Message{onlyChip})[0]; got.Content != "" || got.Metadata["contentParts"] == nil {
		t.Fatalf("chip-only public message = %#v", got)
	}
}
