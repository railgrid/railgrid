// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package engine

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"
	"github.com/railgrid/provider-agents/backend"
)

func TestUploadedInputsAreHydratedPerInvocationAndNotCheckpointed(t *testing.T) {
	const privateText = "private uploaded text that must never enter a checkpoint"
	image := []byte("private uploaded image bytes")
	receipt := backend.AttachmentReceipt{ID: "image-1", Filename: "screen.png", ContentType: "image/png", SizeBytes: int64(len(image)), SHA256: strings.Repeat("a", 64), CreatedAt: time.Now().UTC()}
	message := Message{ID: "user-1", Role: RoleUser, Content: "Inspect the upload", InputAttachments: []backend.AttachmentReceipt{receipt}, InputAttachmentOwner: "alice", InputAttachmentBindingID: "origin"}
	reads := 0
	hydrate := func(_ context.Context, m Message) (backend.UserInputContent, error) {
		reads++
		if m.ID != "user-1" || m.InputAttachmentOwner != "alice" || m.InputAttachmentBindingID != "origin" || len(m.InputAttachments) != 1 {
			t.Fatalf("input identity lost: %#v", m)
		}
		return backend.UserInputContent{Text: privateText, Images: []backend.Image{{MIMEType: "image/png", Data: image}}}, nil
	}
	model := &scriptedBackendModel{responses: [][]*schema.Message{
		{{Role: schema.Assistant, ToolCalls: []schema.ToolCall{{ID: "call", Function: schema.FunctionCall{Name: "ping", Arguments: "{}"}}}}},
		{{Role: schema.Assistant, Content: "done"}},
	}}
	var checkpoints []Checkpoint
	_, err := New().StreamTurnWithTools(context.Background(), model, []Message{message}, []Tool{{Name: "ping", Exec: func(context.Context, string) (string, error) { return "pong", nil }}}, TurnConfig{MaxIters: 3, CheckpointEvery: 1}, Callbacks{HydrateUserInput: hydrate, OnCheckpoint: func(ck Checkpoint) { checkpoints = append(checkpoints, ck) }})
	if err != nil {
		t.Fatal(err)
	}
	if reads != 2 || len(checkpoints) != 1 {
		t.Fatalf("reads/checkpoints = %d/%d", reads, len(checkpoints))
	}
	for _, call := range model.inputs {
		encoded, _ := json.Marshal(call)
		if !strings.Contains(string(encoded), privateText) || !strings.Contains(string(encoded), base64.StdEncoding.EncodeToString(image)) {
			t.Fatal("model did not receive hydrated text and image")
		}
	}
	encoded, err := json.Marshal(checkpoints[0])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), privateText) || strings.Contains(string(encoded), base64.StdEncoding.EncodeToString(image)) {
		t.Fatal("checkpoint retained uploaded bytes")
	}
	if !strings.Contains(string(encoded), "inputAttachments") || !strings.Contains(string(encoded), "alice") {
		t.Fatal("checkpoint lost pointer-only input identity")
	}
	resume := &scriptedBackendModel{responses: [][]*schema.Message{{{Role: schema.Assistant, Content: "resumed"}}}}
	_, err = New().ResumeTurnWithTools(context.Background(), resume, checkpoints[0], nil, TurnConfig{MaxIters: 3}, false, "", Callbacks{HydrateUserInput: hydrate})
	if err != nil || reads != 3 {
		t.Fatalf("resume = %v; reads=%d", err, reads)
	}
	denied := &scriptedBackendModel{}
	_, err = New().ResumeTurnWithTools(context.Background(), denied, checkpoints[0], nil, TurnConfig{MaxIters: 3}, false, "", Callbacks{HydrateUserInput: func(context.Context, Message) (backend.UserInputContent, error) {
		return backend.UserInputContent{}, errors.New("access revoked")
	}})
	if err == nil || denied.calls != 0 {
		t.Fatal("revoked upload reached a model call")
	}
}

func TestCompactionCannotDropLatestUploadedInputIdentity(t *testing.T) {
	anchor := Message{ID: "latest", Role: RoleUser, Content: "inspect", InputAttachmentOwner: "alice", InputAttachments: []backend.AttachmentReceipt{{ID: "upload"}}}
	stripped := anchor
	stripped.InputAttachments = nil
	if err := ensureLatestUserMessageRetained([]Message{stripped}, &anchor); err == nil {
		t.Fatal("compaction discarded uploaded input identity")
	}
	if err := ensureLatestUserMessageRetained([]Message{anchor}, &anchor); err != nil {
		t.Fatal(err)
	}
}
