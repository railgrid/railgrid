// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/railgrid/provider-agents/store"
	"github.com/railgrid/provider-agents/tenant"
)

func TestSharedDefaultSessionCanContinueAfterAnotherCallersAttachment(t *testing.T) {
	s := newChatToolServer(t, "The conversation can continue.")
	scope := store.Scope{OrgUUID: "o", WorkspaceUUID: "w", AgentName: "scout"}
	receipt := store.AgentAttachmentReceipt{ID: "foreign-file", Filename: "private.txt", ContentType: "text/plain", SizeBytes: 6, SHA256: strings.Repeat("a", 64)}
	message := store.Message{ID: "foreign-turn", AgentName: "scout", SessionID: "default", RunID: "foreign-run", Role: "user", Content: "An earlier upload", CreatedAt: time.Now().UTC(), Metadata: map[string]any{
		"attachmentOwner": "other-uploader", "contentParts": []agentChatContentPart{{Type: "attachment", Attachment: &receipt}},
	}}
	if err := s.store.AppendMessage(t.Context(), scope, message); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	req := chatToolRequest("tools/call", map[string]any{"name": "chat", "arguments": map[string]any{"agent": "scout", "message": "Continue without that file"}}, true)
	req.Header.Set(headerRailgridUser, "second-caller")
	s.MCPHandler().ServeHTTP(rec, req)
	_, result := chatFrames(t, rpcMessages(t, rec))
	if result == nil || result["isError"] == true {
		t.Fatalf("shared session refused: %v", result)
	}
	sc, _ := result["structuredContent"].(map[string]any)
	data, _ := sc["data"].(map[string]any)
	if sc["event"] != "done" || data["status"] != "completed" {
		t.Fatalf("shared session failed: %v", sc)
	}
}

func TestAttachmentIdentityFailureLeavesDecisionAndRunRetryable(t *testing.T) {
	ctx := context.Background()
	s := &Server{store: store.NewMemoryStore()}
	scope := store.Scope{OrgUUID: "org", WorkspaceUUID: "ws", AgentName: "helper"}
	checkpoint, _ := json.Marshal(runCheckpoint{AttachmentOwner: "alice", InboxID: "decision"})
	now := time.Now().UTC()
	run := store.Run{ID: "paused", AgentName: "helper", SessionID: "session", Phase: store.RunPhasePendingApproval, Checkpoint: checkpoint, CreatedAt: now, UpdatedAt: now}
	if err := s.store.SaveRun(ctx, scope, run); err != nil {
		t.Fatal(err)
	}
	item := store.InboxItem{ID: "decision", AgentName: "helper", RunID: run.ID, Kind: store.InboxKindApproval, State: store.InboxStatePending, CreatedAt: now}
	if err := s.store.AddInboxItem(ctx, scope, item); err != nil {
		t.Fatal(err)
	}
	var lookups atomic.Int32
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lookups.Add(1)
		http.Error(w, "temporarily unavailable", http.StatusServiceUnavailable)
	}))
	defer hub.Close()
	s.tenant = tenant.NewClient(hub.URL, false)
	access := runAccess{ClusterID: "cluster", CallerUser: "browser-uuid", HubToken: "test-token"}
	if _, err := s.prepareInboxAttachmentAccess(ctx, scope, item, store.InboxStateApproved, access); err == nil {
		t.Fatal("unavailable identity should leave decision unconsumed")
	}
	if _, err := s.resumeRun(ctx, scope, run.ID, access, resumeIntent{FromPhase: store.RunPhasePendingApproval}); err == nil {
		t.Fatal("unavailable identity should not claim run")
	}
	current, err := s.store.GetRun(ctx, scope, run.ID)
	if err != nil || current.Phase != store.RunPhasePendingApproval || current.Attempt != run.Attempt {
		t.Fatalf("run was claimed/closed: %+v %v", current, err)
	}
	decision, err := s.store.GetInboxItem(ctx, scope, item.ID)
	if err != nil || decision.State != store.InboxStatePending {
		t.Fatalf("decision consumed: %+v %v", decision, err)
	}
	if _, err := s.prepareInboxAttachmentAccess(ctx, scope, item, store.InboxStateDenied, access); err != nil {
		t.Fatalf("identity outage blocked a safe denial: %v", err)
	}
	if lookups.Load() != 2 {
		t.Fatalf("expected pre-resolution and pre-claim checks, got %d", lookups.Load())
	}
}

func TestDifferentApproverDropsAttachmentBytesButKeepsAcceptedRecovery(t *testing.T) {
	_, run, message := attachmentModelFixture(t)
	checkpoint := runCheckpoint{AttachmentOwner: "alice", AttachmentRefs: message.InputAttachments}
	owner, refs, _, _, _, err := resumeAgentInputReferences(checkpoint, "bob", "bob", false)
	if err != nil || owner != "" || len(refs) != 0 {
		t.Fatalf("foreign approver failed instead of redacting: %q %v %v", owner, refs, err)
	}
	owner, refs, _, _, _, err = resumeAgentInputReferences(checkpoint, "", "", true)
	if err != nil || owner != "alice" || len(refs) != len(message.InputAttachments) {
		t.Fatalf("accepted recovery lost its binding: %q %v %v", owner, refs, err)
	}
	run.CallerUser = "alice"
	run.HubToken = "unverified-browser-token"
	if currentAttachmentPrincipal(run) != "" {
		t.Fatal("unverified browser identity became upload authority")
	}
}
