// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestPostgres_AgentArtifactAndSourceReceiptRoundTrip(t *testing.T) {
	ctx := context.Background()
	ps, _ := openLegacyHistorySchema(t)
	if err := ps.EnsureSchema(ctx); err != nil {
		t.Fatalf("ensure isolated schema: %v", err)
	}
	scope := Scope{
		OrgUUID: "org-" + uuid.NewString(), WorkspaceUUID: "ws-" + uuid.NewString(), AgentName: "artifact-test",
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	data := []byte(`{"rows":[{"id":7}]}`)
	input := []byte(`{"query":"latest"}`)
	source := SourceReference{
		Object: SourceObject{Version: "v1", Resource: "reports", Name: "latest", UID: "report-uid"},
		Access: []SourceAccess{{Version: "v1", Resource: "reports", Name: "latest", Verb: "get"}},
	}
	html := "<main>latest report</main>"
	resourceURI := "ui://railgrid-provider/reports/dWk6Ly9yZXBvcnRzL3JlcG9ydA"
	ref := &MCPAppReference{Provider: "reports", ResourceURI: resourceURI, SHA256: artifactDigest([]byte(html))}
	app := &MCPAppResource{
		ResourceURI: resourceURI, MIMEType: MCPAppMIMEType,
		HTML: html, SHA256: artifactDigest([]byte(html)),
	}
	tracePresentation := &ProviderPresentation{Version: 1, Title: "Latest report", MCPAppReference: ref, MCPApp: app}
	presentation := &ProviderPresentation{Version: 1, Title: "Latest report", MCPAppReference: ref, MCPApp: app}
	callID := "model-call-" + uuid.NewString()
	toolCall := ToolCall{
		ID: "audit-" + uuid.NewString(), CallID: callID, AgentName: scope.AgentName, RunID: "run-" + uuid.NewString(),
		SessionID: "chat", Trigger: "chat", Tool: "edges__report", Args: `{"query":"latest"}`,
		Result: string(data), Sources: []SourceReference{source}, Presentation: tracePresentation,
		Outcome: "ok", CreatedAt: now,
	}
	if err := ps.AppendToolCall(ctx, scope, toolCall); err != nil {
		t.Fatalf("append tool receipt: %v", err)
	}
	calls, err := ps.ListToolCalls(ctx, scope, toolCall.RunID)
	if err != nil || len(calls) != 1 {
		t.Fatalf("list tool receipts = %d, err=%v", len(calls), err)
	}
	gotCall := calls[0]
	if gotCall.CallID != callID || gotCall.SessionID != "chat" || len(gotCall.Sources) != 1 || gotCall.Sources[0].Object.UID != source.Object.UID ||
		gotCall.Presentation == nil || gotCall.Presentation.MCPAppReference == nil || gotCall.Presentation.MCPApp != nil {
		t.Fatalf("tool receipt provenance did not round-trip: %+v", gotCall)
	}
	encodedTrace, err := json.Marshal(gotCall.Presentation)
	if err != nil || string(encodedTrace) == "" || strings.Contains(string(encodedTrace), html) {
		t.Fatalf("tool trace must retain only the content-free app reference: %s, err=%v", encodedTrace, err)
	}

	message := Message{
		ID: "tool-result-" + uuid.NewString(), AgentName: scope.AgentName, SessionID: "chat", RunID: toolCall.RunID,
		Role: "tool", Content: string(data), Metadata: map[string]any{"toolCallID": callID}, CreatedAt: now,
	}
	if err := ps.AppendMessage(ctx, scope, message); err != nil {
		t.Fatalf("append transcript result: %v", err)
	}
	gotMessage, found, err := ps.GetToolResult(ctx, scope, toolCall.RunID, "chat", callID)
	if err != nil || !found || gotMessage.Content != string(data) || gotMessage.Metadata["toolCallID"] != callID {
		t.Fatalf("get exact transcript result = %+v, found=%t, err=%v", gotMessage, found, err)
	}

	artifact := AgentArtifact{
		ID: uuid.NewString(), AgentName: scope.AgentName, AgentUID: "agent-uid", Owner: "caller@example.test",
		RunID: toolCall.RunID, SessionID: "chat", ToolCallID: callID, ToolName: toolCall.Tool, Title: "Latest report",
		MediaType: "application/json", Input: input, InputDigest: artifactDigest(input), InputSize: int64(len(input)),
		Digest: artifactDigest(data), Size: int64(len(data)), Data: data,
		Sources: []SourceReference{source}, Presentation: presentation, CreatedAt: now,
	}
	if err := ps.SaveAgentArtifact(ctx, scope, artifact); err != nil {
		t.Fatalf("save artifact: %v", err)
	}
	items, err := ps.ListAgentArtifacts(ctx, scope, artifact.AgentUID, artifact.Owner, 10)
	if err != nil || len(items) != 1 || items[0].Digest != artifact.Digest || items[0].InputDigest != artifact.InputDigest ||
		items[0].Sources[0].Object.UID != source.Object.UID {
		t.Fatalf("list artifact metadata = %+v, err=%v", items, err)
	}
	got, found, err := ps.GetAgentArtifact(ctx, scope, artifact.AgentUID, artifact.Owner, artifact.ID)
	if err != nil || !found || string(got.Data) != string(data) || string(got.Input) != string(input) ||
		got.Presentation == nil || got.Presentation.MCPAppReference == nil || got.Presentation.MCPApp != nil ||
		got.Sources[0].Object.UID != source.Object.UID {
		t.Fatalf("get artifact = %+v, found=%t, err=%v", got, found, err)
	}
	encoded, err := json.Marshal(got.Presentation)
	if err != nil || len(encoded) == 0 || strings.Contains(string(encoded), html) {
		t.Fatalf("saved artifact must retain only the content-free app receipt: %s, err=%v", string(encoded), err)
	}
	deleted, err := ps.DeleteAgentArtifact(ctx, scope, artifact.AgentUID, artifact.Owner, artifact.ID)
	if err != nil || !deleted {
		t.Fatalf("delete artifact = %t, err=%v", deleted, err)
	}
	if _, found, err := ps.GetAgentArtifact(ctx, scope, artifact.AgentUID, artifact.Owner, artifact.ID); err != nil || found {
		t.Fatalf("artifact after delete found=%t, err=%v", found, err)
	}
}

func TestPostgres_AgentArtifactRetentionIsPerOwner(t *testing.T) {
	ctx := context.Background()
	ps, _ := openLegacyHistorySchema(t)
	if err := ps.EnsureSchema(ctx); err != nil {
		t.Fatalf("ensure isolated schema: %v", err)
	}
	scope := Scope{OrgUUID: "org-" + uuid.NewString(), WorkspaceUUID: "ws-" + uuid.NewString(), AgentName: "analyst"}
	for i := 0; i < MaxAgentArtifactsPerAgent; i++ {
		if err := ps.SaveAgentArtifact(ctx, scope, quotaTestArtifact(i, "alice")); err != nil {
			t.Fatalf("save Alice artifact %d: %v", i, err)
		}
	}
	if err := ps.SaveAgentArtifact(ctx, scope, quotaTestArtifact(MaxAgentArtifactsPerAgent, "alice")); !errors.Is(err, ErrAgentArtifactLimit) {
		t.Fatalf("Alice artifact over quota = %v, want ErrAgentArtifactLimit", err)
	}
	if err := ps.SaveAgentArtifact(ctx, scope, quotaTestArtifact(0, "bob")); err != nil {
		t.Fatalf("Alice's artifacts consumed Bob's quota: %v", err)
	}
}
