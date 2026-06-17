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

package store

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestMemoryStoreAssistantRunRoundTrip(t *testing.T) {
	s := NewMemoryStore()
	scope := Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: "demo"}
	run := AssistantRun{
		ID:         "run-1",
		Status:     AssistantRunStatusPendingPermission,
		RequestID:  "perm-1",
		Checkpoint: json.RawMessage(`{"tool":"write_file"}`),
		Audit:      json.RawMessage(`{"decisions":[{"decision":"allow"}]}`),
	}
	if err := s.SaveAssistantRun(context.Background(), scope, run); err != nil {
		t.Fatalf("SaveAssistantRun returned error: %v", err)
	}

	got, err := s.GetAssistantRun(context.Background(), scope, "run-1")
	if err != nil {
		t.Fatalf("GetAssistantRun returned error: %v", err)
	}
	if got.ID != run.ID || got.Status != run.Status || got.RequestID != run.RequestID {
		t.Fatalf("assistant run = %#v, want %#v", got, run)
	}
	if string(got.Checkpoint) != string(run.Checkpoint) {
		t.Fatalf("checkpoint = %s, want %s", got.Checkpoint, run.Checkpoint)
	}
	if string(got.Audit) != string(run.Audit) {
		t.Fatalf("audit = %s, want %s", got.Audit, run.Audit)
	}

	claimed, err := s.ClaimAssistantRun(context.Background(), scope, "run-1", "perm-1", got.CreatedAt.Add(time.Minute))
	if err != nil {
		t.Fatalf("ClaimAssistantRun returned error: %v", err)
	}
	if claimed.Status != AssistantRunStatusRunning {
		t.Fatalf("claimed status = %q, want %q", claimed.Status, AssistantRunStatusRunning)
	}
	if _, err := s.ClaimAssistantRun(context.Background(), scope, "run-1", "perm-1", got.CreatedAt.Add(time.Minute)); err == nil {
		t.Fatal("second ClaimAssistantRun returned nil error")
	}

	claimed.Status = AssistantRunStatusCompleted
	if err := s.SaveAssistantRun(context.Background(), scope, claimed); err != nil {
		t.Fatalf("SaveAssistantRun update returned error: %v", err)
	}
	updated, err := s.GetAssistantRun(context.Background(), scope, "run-1")
	if err != nil {
		t.Fatalf("GetAssistantRun update returned error: %v", err)
	}
	if updated.Status != AssistantRunStatusCompleted {
		t.Fatalf("status = %q, want %q", updated.Status, AssistantRunStatusCompleted)
	}
}
