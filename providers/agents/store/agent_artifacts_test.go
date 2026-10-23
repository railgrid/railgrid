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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestMemoryToolCallTraceStripsMCPAppHTML(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryStore()
	scope := Scope{OrgUUID: "org", WorkspaceUUID: "workspace", AgentName: "analyst"}
	const html = "<html>must stay out of trace storage</html>"
	digest := sha256.Sum256([]byte(html))
	ref := &MCPAppReference{Provider: "reports", ResourceURI: "ui://railgrid-provider/reports/dWk6Ly9yZXBvcnRzL3JlcG9ydA", SHA256: hex.EncodeToString(digest[:])}
	source := SourceReference{
		Object: SourceObject{Version: "v1", Resource: "reports", Name: "latest", UID: "uid-1"},
		Access: []SourceAccess{{Version: "v1", Resource: "reports", Name: "latest", Verb: "get"}},
	}
	err := s.AppendToolCall(ctx, scope, ToolCall{
		ID: "audit-1", AgentName: scope.AgentName, RunID: "run-1", CallID: "call-1", SessionID: "chat",
		Tool: "reports__get", Outcome: "ok", Sources: []SourceReference{source},
		Presentation: &ProviderPresentation{Version: 1, Title: "Report", MCPAppReference: ref, MCPApp: &MCPAppResource{
			ResourceURI: ref.ResourceURI, MIMEType: MCPAppMIMEType, HTML: html, SHA256: ref.SHA256,
		}}, CreatedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	calls, err := s.ListToolCalls(ctx, scope, "run-1")
	if err != nil || len(calls) != 1 {
		t.Fatalf("list tool trace = %d, err=%v", len(calls), err)
	}
	encoded, err := json.Marshal(calls[0].Presentation)
	if err != nil || strings.Contains(string(encoded), html) || calls[0].Presentation.MCPApp != nil || calls[0].Presentation.MCPAppReference == nil {
		t.Fatalf("tool trace must retain only the content-free app receipt: %s, err=%v", encoded, err)
	}
}

func TestMemoryAgentArtifactRetentionIsPerOwner(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryStore()
	scope := Scope{OrgUUID: "org", WorkspaceUUID: "workspace", AgentName: "analyst"}
	for i := 0; i < MaxAgentArtifactsPerAgent; i++ {
		if err := s.SaveAgentArtifact(ctx, scope, quotaTestArtifact(i, "alice")); err != nil {
			t.Fatalf("save Alice artifact %d: %v", i, err)
		}
	}
	if err := s.SaveAgentArtifact(ctx, scope, quotaTestArtifact(MaxAgentArtifactsPerAgent, "alice")); !errors.Is(err, ErrAgentArtifactLimit) {
		t.Fatalf("Alice artifact over quota = %v, want ErrAgentArtifactLimit", err)
	}
	if err := s.SaveAgentArtifact(ctx, scope, quotaTestArtifact(0, "bob")); err != nil {
		t.Fatalf("Alice's artifacts consumed Bob's quota: %v", err)
	}
}

func quotaTestArtifact(id int, owner string) AgentArtifact {
	data := []byte("x")
	return AgentArtifact{
		ID: fmt.Sprintf("%s-%03d", owner, id), AgentName: "analyst", AgentUID: "agent-uid", Owner: owner,
		RunID: fmt.Sprintf("run-%s-%03d", owner, id), SessionID: "chat", ToolCallID: fmt.Sprintf("call-%s-%03d", owner, id),
		ToolName: "reports__get", Title: "report", MediaType: "text/plain; charset=utf-8",
		Digest: artifactDigest(data), Size: int64(len(data)), Data: data, CreatedAt: time.Now().UTC(),
	}
}
