// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package api

import (
	"context"
	"testing"

	"github.com/faroshq/provider-app-studio/store"
)

func TestProjectAssistantApprovalModeIsCapturedPerRun(t *testing.T) {
	ctx := context.Background()
	messages := store.NewMemoryStore()
	server := &Server{store: messages}
	scope := store.Scope{
		OrgUUID:       "org-a",
		WorkspaceUUID: "workspace-a",
		ProjectName:   "demo",
		ProjectUID:    "project-uid",
	}
	if _, err := messages.SetAssistantApprovalPreference(ctx, scope, store.AssistantApprovalPreference{
		ActorID: "alice",
		Mode:    store.AssistantApprovalModeAutoApprove,
	}); err != nil {
		t.Fatal(err)
	}

	run := store.AssistantRun{}
	if err := server.captureProjectAssistantApprovalMode(ctx, scope, "alice", &run); err != nil {
		t.Fatal(err)
	}
	if run.ApprovalMode != store.AssistantApprovalModeAutoApprove {
		t.Fatalf("captured approval mode = %q", run.ApprovalMode)
	}

	if _, err := messages.SetAssistantApprovalPreference(ctx, scope, store.AssistantApprovalPreference{
		ActorID: "alice",
		Mode:    store.AssistantApprovalModeAlwaysAsk,
	}); err != nil {
		t.Fatal(err)
	}
	if got := projectAssistantApprovalModeFromRun(run); got != store.AssistantApprovalModeAutoApprove {
		t.Fatalf("run approval mode after preference change = %q, want immutable snapshot", got)
	}
}

func TestProjectAssistantApprovalModeDefaultsToOnRequest(t *testing.T) {
	ctx := context.Background()
	messages := store.NewMemoryStore()
	server := &Server{store: messages}
	scope := store.Scope{
		OrgUUID:       "org-a",
		WorkspaceUUID: "workspace-a",
		ProjectName:   "demo",
		ProjectUID:    "project-uid",
	}
	run := store.AssistantRun{}
	if err := server.captureProjectAssistantApprovalMode(ctx, scope, "alice", &run); err != nil {
		t.Fatal(err)
	}
	if run.ApprovalMode != store.AssistantApprovalModeOnRequest {
		t.Fatalf("captured default approval mode = %q, want on request", run.ApprovalMode)
	}
	if got := projectAssistantApprovalModeFromRun(store.AssistantRun{}); got != store.AssistantApprovalModeOnRequest {
		t.Fatalf("legacy run default approval mode = %q, want on-request fallback", got)
	}
}
