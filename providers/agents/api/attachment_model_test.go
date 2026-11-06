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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	agents "github.com/railgrid/provider-agents/apis/v1alpha1"
	"github.com/railgrid/provider-agents/backend"
	"github.com/railgrid/provider-agents/store"
	"github.com/railgrid/provider-agents/tenant"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func attachmentModelFixture(t *testing.T) (*Server, taskRun, backend.Message) {
	t.Helper()
	ctx := context.Background()
	st := store.NewMemoryStore()
	scope := store.Scope{OrgUUID: "org", WorkspaceUUID: "ws", AgentName: "helper"}
	now := time.Now().UTC().Truncate(time.Microsecond)
	expires := now.Add(time.Hour)
	inputs := []struct {
		name, media string
		data        []byte
	}{
		{"notes.txt", "text/plain", []byte("hello € world")},
		{"screen.png", "image/png", []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 0}},
		{"report.pdf", "application/pdf", []byte("%PDF-1.4 opaque-pdf-bytes")},
		{"large.txt", "text/plain", []byte(strings.Repeat("large-file-data ", 4096))},
	}
	var refs []store.AgentAttachmentReceipt
	for _, input := range inputs {
		digest := sha256.Sum256(input.data)
		item, _, err := st.CreateAgentAttachment(ctx, scope, store.AgentAttachment{ID: input.name, AgentName: "helper", AgentUID: "uid", SessionID: "session", ActorID: "alice", Filename: input.name, ContentType: input.media, SizeBytes: int64(len(input.data)), SHA256: hex.EncodeToString(digest[:]), Draft: true, CreatedAt: now, ExpiresAt: &expires, Data: input.data})
		if err != nil {
			t.Fatal(err)
		}
		refs = append(refs, item.Receipt())
	}
	message := store.Message{ID: "origin-message", AgentName: "helper", SessionID: "session", RunID: "origin", Role: "user", Content: "inspect files", CreatedAt: now}
	record := store.Run{ID: "origin", AgentName: "helper", SessionID: "session", Phase: store.RunPhaseRunning, CreatedAt: now, UpdatedAt: now}
	if err := st.CreateAgentChatRun(ctx, scope, "uid", message, record, refs, "alice", now); err != nil {
		t.Fatal(err)
	}
	run := taskRun{Scope: scope, Agent: &agents.Agent{ObjectMeta: metav1.ObjectMeta{Name: "helper", UID: types.UID("uid")}}, RunID: "later", CallerUser: "alice", AttachmentOwner: "alice"}
	return &Server{store: st}, run, backend.Message{ID: "origin-message", Role: backend.RoleUser, Content: "inspect files", InputAttachments: refs, InputAttachmentOwner: "alice", InputAttachmentBindingID: "origin"}
}

func TestModelUploadProjectionIsReadableButOpaqueFilesStayMetadataOnly(t *testing.T) {
	s, run, message := attachmentModelFixture(t)
	content, err := s.agentAttachmentInputHydrator(run, "session", "gpt-4o")(context.Background(), message)
	if err != nil {
		t.Fatal(err)
	}
	if len(content.Images) != 1 || !strings.Contains(content.Text, "hello € world") || !strings.Contains(content.Text, "read_chat_attachment") || !strings.Contains(content.Text, "report.pdf") {
		t.Fatalf("projection = %#v", content)
	}
	if strings.Contains(content.Text, "opaque-pdf-bytes") || strings.Contains(content.Text, strings.Repeat("large-file-data ", 3)) {
		t.Fatal("opaque or large file bytes were injected inline")
	}
	if _, err := s.agentAttachmentInputHydrator(run, "session", "gpt-5-nano")(context.Background(), message); err == nil {
		t.Fatal("known nonvision model accepted an image")
	}
	wrongBinding := message
	wrongBinding.InputAttachmentBindingID = "different"
	if _, err := s.agentAttachmentInputHydrator(run, "session", "gpt-4o")(context.Background(), wrongBinding); err == nil {
		t.Fatal("wrong originating run binding accepted")
	}
	run.CallerUser = "bob"
	if content, err := s.agentAttachmentInputHydrator(run, "session", "gpt-4o")(context.Background(), message); err != nil || len(content.Images) != 0 || strings.Contains(content.Text, "hello") || !strings.Contains(content.Text, "metadata only") {
		t.Fatalf("foreign attachment history was not metadata only: %+v, %v", content, err)
	}
}

func TestModelUploadUsesVerifiedPrincipalInsteadOfBrowserUserID(t *testing.T) {
	s, run, message := attachmentModelFixture(t)
	run.CallerUser = "browser-user-uuid"
	run.AttachmentCaller = "alice"
	if _, err := s.agentAttachmentInputHydrator(run, "session", "gpt-4o")(context.Background(), message); err != nil {
		t.Fatalf("verified upload principal rejected: %v", err)
	}
	if _, err := s.agentAttachmentReadTool(context.Background(), run, "session").Exec(context.Background(), `{"attachmentID":"notes.txt"}`); err != nil {
		t.Fatalf("verified text read rejected: %v", err)
	}
	run.AttachmentCaller = "bob"
	if content, err := s.agentAttachmentInputHydrator(run, "session", "gpt-4o")(context.Background(), message); err != nil || len(content.Images) != 0 || strings.Contains(content.Text, "hello") || !strings.Contains(content.Text, "metadata only") {
		t.Fatalf("foreign verified principal received bytes: %+v, %v", content, err)
	}
}

func TestTextReadResolvesCurrentPrincipalAfterReceiptLeavesReplayHistory(t *testing.T) {
	s, run, _ := attachmentModelFixture(t)
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/clusters/cluster-a/apis/authentication.k8s.io/v1/selfsubjectreviews" {
			t.Errorf("unexpected identity request: %s %s", r.Method, r.URL.Path)
		}
		principal := "bob"
		if r.Header.Get("Authorization") == "Bearer alice-token" {
			principal = "alice"
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"apiVersion": "authentication.k8s.io/v1", "kind": "SelfSubjectReview", "status": map[string]any{"userInfo": map[string]any{"username": principal}}})
	}))
	defer hub.Close()
	s.tenant = tenant.NewClient(hub.URL, false)
	run.ClusterID, run.CallerUser, run.HubToken = "cluster-a", "browser-user-uuid", "alice-token"
	run.AttachmentOwner = ""
	if body, err := s.agentAttachmentReadTool(context.Background(), run, "session").Exec(context.Background(), `{"attachmentID":"notes.txt"}`); err != nil || !strings.Contains(body, "hello") {
		t.Fatalf("read with verified current principal: %s, %v", body, err)
	}
	run.HubToken = "bob-token"
	if _, err := s.agentAttachmentReadTool(context.Background(), run, "session").Exec(context.Background(), `{"attachmentID":"notes.txt"}`); err == nil {
		t.Fatal("different verified principal reopened an older upload")
	}
}

func TestBoundTextReadIsUTF8AlignedAndDoesNotReturnOpaqueBytes(t *testing.T) {
	s, run, _ := attachmentModelFixture(t)
	tool := s.agentAttachmentReadTool(context.Background(), run, "session")
	result, err := tool.Exec(context.Background(), `{"attachmentID":"notes.txt","offset":6,"limit":1}`)
	if err != nil {
		t.Fatal(err)
	}
	var read struct {
		Content    string `json:"content"`
		NextOffset int64  `json:"nextOffset"`
	}
	if err := json.Unmarshal([]byte(result), &read); err != nil {
		t.Fatal(err)
	}
	if read.Content != "€" || read.NextOffset != 9 {
		t.Fatalf("UTF-8 range = %#v", read)
	}
	if _, err := tool.Exec(context.Background(), `{"attachmentID":"notes.txt","offset":7,"limit":1}`); err == nil {
		t.Fatal("mid-character offset accepted")
	}
	if _, err := tool.Exec(context.Background(), `{"attachmentID":"notes.txt","limit":65537}`); err == nil {
		t.Fatal("unbounded read accepted")
	}
	for _, id := range []string{"report.pdf", "screen.png"} {
		result, err := tool.Exec(context.Background(), `{"attachmentID":"`+id+`"}`)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(result, "opaque-pdf-bytes") || strings.Contains(result, "iVBOR") {
			t.Fatal("read tool returned opaque/image bytes")
		}
	}
	if _, err := s.agentAttachmentReadTool(context.Background(), run, "other-session").Exec(context.Background(), `{"attachmentID":"notes.txt"}`); err == nil {
		t.Fatal("cross-session upload read accepted")
	}
}
