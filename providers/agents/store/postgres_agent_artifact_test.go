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
	app := &MCPAppResource{
		ResourceURI: "ui://railgrid-provider/report/abc", MIMEType: MCPAppMIMEType,
		HTML: html, SHA256: artifactDigest([]byte(html)),
	}
	presentation := &ProviderPresentation{Version: 1, Title: "Latest report", MCPApp: app}
	callID := "model-call-" + uuid.NewString()
	toolCall := ToolCall{
		ID: "audit-" + uuid.NewString(), CallID: callID, AgentName: scope.AgentName, RunID: "run-" + uuid.NewString(),
		SessionID: "chat", Trigger: "chat", Tool: "edges__report", Args: `{"query":"latest"}`,
		Result: string(data), Sources: []SourceReference{source}, Presentation: presentation,
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
		gotCall.Presentation == nil || gotCall.Presentation.MCPApp == nil || gotCall.Presentation.MCPApp.HTML != html {
		t.Fatalf("tool receipt provenance did not round-trip: %+v", gotCall)
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
		got.Presentation == nil || got.Presentation.MCPApp == nil || got.Presentation.MCPApp.HTML != html ||
		got.Sources[0].Object.UID != source.Object.UID {
		t.Fatalf("get artifact = %+v, found=%t, err=%v", got, found, err)
	}
	encoded, err := json.Marshal(got.Presentation)
	if err != nil || len(encoded) == 0 {
		t.Fatalf("marshal presentation after reopen: %s, err=%v", string(encoded), err)
	}
	deleted, err := ps.DeleteAgentArtifact(ctx, scope, artifact.AgentUID, artifact.Owner, artifact.ID)
	if err != nil || !deleted {
		t.Fatalf("delete artifact = %t, err=%v", deleted, err)
	}
	if _, found, err := ps.GetAgentArtifact(ctx, scope, artifact.AgentUID, artifact.Owner, artifact.ID); err != nil || found {
		t.Fatalf("artifact after delete found=%t, err=%v", found, err)
	}
}
