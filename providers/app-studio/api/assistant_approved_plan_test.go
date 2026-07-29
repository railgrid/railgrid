/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package api

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/faroshq/provider-app-studio/store"
)

func TestProjectAssistantInitialCreationPlanCannotPersistAcrossTurns(t *testing.T) {
	server := &Server{store: store.NewMemoryStore()}
	scope := store.Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-a", ProjectName: "demo", ProjectUID: "test-project-uid-demo"}
	initial := projectAssistantInitialCreationPlan()
	if projectAssistantApprovedPlanAllowsWrite(&initial, projectToolSelectTemplate, map[string]any{"template": "simple-webapp"}) {
		t.Fatal("initial creation plan authorized template selection")
	}
	merged := mergeProjectAssistantApprovedPlans(initial, projectAssistantApprovedPlan{
		Summary:      "Model restated the initial plan",
		Version:      projectAssistantApprovedPlanVersionWorkspaceMutation,
		Capabilities: []string{projectAssistantCapabilityWorkspaceMutate},
	})

	if err := server.saveProjectAssistantApprovedPlan(context.Background(), scope, &merged); err != nil {
		t.Fatalf("saveProjectAssistantApprovedPlan returned error: %v", err)
	}
	if got := server.loadProjectAssistantApprovedPlan(context.Background(), scope); got != nil {
		t.Fatalf("persisted run-local initial plan = %#v, want nil", got)
	}
}

func TestProjectAssistantRetiredGrantCannotBeRestoredFromPendingCheckpoints(t *testing.T) {
	server := &Server{store: store.NewMemoryStore()}
	scope := store.Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-a", ProjectName: "demo", ProjectUID: "test-project-uid-demo"}
	plan := normalizeProjectAssistantApprovedPlan(projectAssistantApprovedPlan{
		Summary:        "Edit the application.",
		Version:        projectAssistantApprovedPlanVersionWorkspaceMutation,
		Capabilities:   []string{projectAssistantCapabilityWorkspaceMutate},
		AllowAllWrites: true,
		ApprovedAt:     time.Now().Add(-time.Minute),
	})
	if err := server.saveProjectAssistantApprovedPlan(context.Background(), scope, &plan); err != nil {
		t.Fatalf("saveProjectAssistantApprovedPlan returned error: %v", err)
	}
	_, grantRevision, err := server.loadProjectAssistantApprovedPlanGrant(context.Background(), scope)
	if err != nil {
		t.Fatalf("loadProjectAssistantApprovedPlanGrant returned error: %v", err)
	}
	checkpoints := []projectAssistantCheckpointState{
		{ApprovedPlan: cloneProjectAssistantApprovedPlan(&plan), ApprovedPlanGrantRevision: grantRevision},
		{ApprovedPlan: cloneProjectAssistantApprovedPlan(&plan), ApprovedPlanGrantRevision: grantRevision},
	}
	if err := server.clearProjectAssistantApprovedPlan(context.Background(), scope); err != nil {
		t.Fatalf("clearProjectAssistantApprovedPlan returned error: %v", err)
	}

	for i, checkpoint := range checkpoints {
		got, _, err := server.projectAssistantApprovedPlanForCheckpointResume(
			context.Background(),
			scope,
			checkpoint.ApprovedPlan,
			checkpoint.ApprovedPlanGrantRevision,
		)
		if !errors.Is(err, errProjectAssistantCheckpointGrantStale) || got != nil {
			t.Fatalf("checkpoint %d grant = %#v, error = %v; want stale rejection", i, got, err)
		}
	}
}

func TestProjectAssistantOldCheckpointCannotInheritReplacementGrant(t *testing.T) {
	server := &Server{store: store.NewMemoryStore()}
	scope := store.Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-a", ProjectName: "demo", ProjectUID: "test-project-uid-demo"}
	first := normalizeProjectAssistantApprovedPlan(projectAssistantApprovedPlan{
		Version:      projectAssistantApprovedPlanVersionWorkspaceMutation,
		Capabilities: []string{projectAssistantCapabilityWorkspaceMutate},
		TargetPaths:  []string{"src/"},
	})
	if err := server.saveProjectAssistantApprovedPlan(context.Background(), scope, &first); err != nil {
		t.Fatalf("save first grant returned error: %v", err)
	}
	_, firstRevision, err := server.loadProjectAssistantApprovedPlanGrant(context.Background(), scope)
	if err != nil {
		t.Fatalf("load first grant returned error: %v", err)
	}
	if err := server.clearProjectAssistantApprovedPlan(context.Background(), scope); err != nil {
		t.Fatalf("clear first grant returned error: %v", err)
	}
	second := normalizeProjectAssistantApprovedPlan(projectAssistantApprovedPlan{
		Version:      projectAssistantApprovedPlanVersionWorkspaceMutation,
		Capabilities: []string{projectAssistantCapabilityWorkspaceMutate},
		TargetPaths:  []string{"app/"},
	})
	if err := server.saveProjectAssistantApprovedPlan(context.Background(), scope, &second); err != nil {
		t.Fatalf("save replacement grant returned error: %v", err)
	}

	for _, checkpointPlan := range []*projectAssistantApprovedPlan{&first, nil} {
		got, _, err := server.projectAssistantApprovedPlanForCheckpointResume(
			context.Background(),
			scope,
			checkpointPlan,
			firstRevision,
		)
		if !errors.Is(err, errProjectAssistantCheckpointGrantStale) || got != nil {
			t.Fatalf("replacement reconciliation grant = %#v, error = %v; want stale rejection", got, err)
		}
	}
}

func TestProjectAssistantStaleWriterCannotOverwriteRetirementTombstone(t *testing.T) {
	server := &Server{store: store.NewMemoryStore()}
	scope := store.Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-a", ProjectName: "demo", ProjectUID: "test-project-uid-demo"}
	plan := normalizeProjectAssistantApprovedPlan(projectAssistantApprovedPlan{
		Version:      projectAssistantApprovedPlanVersionWorkspaceMutation,
		Capabilities: []string{projectAssistantCapabilityWorkspaceMutate},
		TargetPaths:  []string{"src/"},
	})
	if err := server.saveProjectAssistantApprovedPlan(context.Background(), scope, &plan); err != nil {
		t.Fatalf("saveProjectAssistantApprovedPlan returned error: %v", err)
	}
	_, staleRevision, err := server.loadProjectAssistantApprovedPlanGrant(context.Background(), scope)
	if err != nil {
		t.Fatalf("loadProjectAssistantApprovedPlanGrant returned error: %v", err)
	}
	if err := server.clearProjectAssistantApprovedPlan(context.Background(), scope); err != nil {
		t.Fatalf("clearProjectAssistantApprovedPlan returned error: %v", err)
	}
	_, tombstoneRevision, err := server.loadProjectAssistantApprovedPlanGrant(context.Background(), scope)
	if err != nil {
		t.Fatalf("load tombstone returned error: %v", err)
	}

	if _, err := server.persistProjectAssistantApprovedPlan(
		context.Background(),
		scope,
		&plan,
		staleRevision,
	); !errors.Is(err, store.ErrAssistantRunConflict) {
		t.Fatalf("stale grant write error = %v, want version conflict", err)
	}
	got, revision, err := server.loadProjectAssistantApprovedPlanGrant(context.Background(), scope)
	if err != nil {
		t.Fatalf("load grant after stale write returned error: %v", err)
	}
	if got != nil || revision != tombstoneRevision {
		t.Fatalf("grant after stale write = %#v revision %q, want tombstone revision %q", got, revision, tombstoneRevision)
	}
}

func TestProjectAssistantOperationOnlyGrantBecomesRetiredTombstone(t *testing.T) {
	messages := store.NewMemoryStore()
	server := &Server{store: messages}
	scope := store.Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-a", ProjectName: "demo", ProjectUID: "test-project-uid-demo"}
	raw := json.RawMessage(`{"operations":["write_file"],"targetPaths":["src/"]}`)
	if err := messages.SaveAssistantRun(context.Background(), scope, store.AssistantRun{
		ID:         projectAssistantApprovedPlanGrantRunID,
		Status:     store.AssistantRunStatusCompleted,
		Checkpoint: raw,
	}); err != nil {
		t.Fatalf("save legacy grant returned error: %v", err)
	}

	got, revision, err := server.loadProjectAssistantApprovedPlanGrant(context.Background(), scope)
	if err != nil {
		t.Fatalf("load operation-only grant returned error: %v", err)
	}
	if got != nil || revision == "" {
		t.Fatalf("operation-only grant = %#v revision %q, want retired tombstone", got, revision)
	}
	persisted, err := messages.GetAssistantRun(context.Background(), scope, projectAssistantApprovedPlanGrantRunID)
	if err != nil {
		t.Fatalf("GetAssistantRun returned error: %v", err)
	}
	if persisted.RequestID != revision {
		t.Fatalf("persisted revision = %q, want %q", persisted.RequestID, revision)
	}
	var record projectAssistantApprovedPlanGrantRecord
	if err := json.Unmarshal(persisted.Checkpoint, &record); err != nil {
		t.Fatalf("decode tombstone returned error: %v", err)
	}
	if record.Plan != nil {
		t.Fatalf("persisted tombstone plan = %#v, want nil", record.Plan)
	}
}

func TestProjectAssistantCheckpointGrantValidationFailsClosedOnStoreError(t *testing.T) {
	plan := normalizeProjectAssistantApprovedPlan(projectAssistantApprovedPlan{
		Version:      projectAssistantApprovedPlanVersionWorkspaceMutation,
		Capabilities: []string{projectAssistantCapabilityWorkspaceMutate},
		TargetPaths:  []string{"src/"},
	})
	server := &Server{store: failingProjectAssistantGrantReadStore{
		Store: store.NewMemoryStore(),
		err:   errors.New("database unavailable"),
	}}
	scope := store.Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-a", ProjectName: "demo", ProjectUID: "test-project-uid-demo"}

	if got, _, err := server.projectAssistantApprovedPlanForCheckpointResume(context.Background(), scope, &plan, "revision-a"); err == nil || got != nil {
		t.Fatalf("checkpoint grant = %#v, error = %v; want nil and store error", got, err)
	}
}

type failingProjectAssistantGrantReadStore struct {
	store.Store
	err error
}

func (s failingProjectAssistantGrantReadStore) GetAssistantRun(context.Context, store.Scope, string) (store.AssistantRun, error) {
	return store.AssistantRun{}, s.err
}
