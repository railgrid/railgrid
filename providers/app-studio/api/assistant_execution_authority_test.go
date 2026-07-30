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
	"testing"
	"time"

	"github.com/faroshq/provider-app-studio/store"
)

func TestProjectEinoMutationRequiresDurableOrExplicitTestAuthority(t *testing.T) {
	spec := projectAssistantToolSpec{Risk: projectAssistantToolRiskWrite}
	production := projectEinoAssistantTool{server: &Server{}, req: projectAssistantRunRequest{}}
	if err := production.admitMutation(context.Background(), spec); err != store.ErrAssistantWorkItemConflict {
		t.Fatalf("unmanaged mutation admission = %v, want WorkItem conflict", err)
	}

	authority := &projectAssistantExplicitTestAuthority{}
	testAdapter := projectEinoAssistantTool{req: projectAssistantRunRequest{executionAuthority: authority}}
	if err := testAdapter.admitMutation(context.Background(), spec); err != nil {
		t.Fatalf("explicit test authority admission = %v", err)
	}
	if authority.admissions != 1 {
		t.Fatalf("explicit test authority admissions = %d, want 1", authority.admissions)
	}
}

func TestProjectAssistantExecutionAuthorityPersistsDiscussionAuditWithoutWorkItem(t *testing.T) {
	messages := store.NewMemoryStore()
	server := NewWithWorkspace(nil, messages, nil, "", false)
	scope := testProjectMessageScope("org-a", "workspace-a", "demo")
	now := time.Now().UTC()
	user := store.Message{ID: "user-1", Role: "user", ActorID: "alice", Content: "What changed?", CreatedAt: now, UpdatedAt: now}
	assistant := store.Message{ID: "assistant-1", Role: "assistant", Content: "", CreatedAt: now, UpdatedAt: now}
	run := store.AssistantRun{
		ID:              "run-1",
		Mode:            store.AssistantRunModeDiscussion,
		Status:          store.AssistantRunStatusRunning,
		ClientRequestID: "request-1",
		UserMessageID:   user.ID,
		ActiveMessageID: assistant.ID,
		Revision:        1,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	created, err := messages.CreateAssistantRun(context.Background(), scope, user, assistant, run)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := server.projectAssistantSupervisor().Attach(scope, created, assistant); err != nil {
		t.Fatal(err)
	}
	authority := projectAssistantServerExecutionAuthority{server: server, req: projectAssistantRunRequest{
		Identity: identity{user: "alice"}, MessageScope: scope, AssistantRun: &created,
	}}
	if err := authority.PersistAudit(context.Background(), []byte(`{"outcome":"succeeded"}`)); err != nil {
		t.Fatalf("PersistAudit discussion run: %v", err)
	}
	persisted, err := messages.GetAssistantRun(context.Background(), scope, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if string(persisted.Audit) != `{"outcome":"succeeded"}` || persisted.WorkItemID != "" {
		t.Fatalf("discussion audit run = %#v", persisted)
	}
	other := created
	other.ID = "run-other"
	if err := authority.PersistRun(context.Background(), other); err != store.ErrAssistantRunConflict {
		t.Fatalf("PersistRun for unbound run = %v, want run conflict", err)
	}
}

type projectAssistantExplicitTestAuthority struct {
	admissions int
}

func (*projectAssistantExplicitTestAuthority) Load(context.Context) (projectAssistantExecutionAuthorityState, error) {
	return projectAssistantExecutionAuthorityState{}, nil
}

func (*projectAssistantExplicitTestAuthority) PersistApprovedPlan(context.Context, *projectAssistantApprovedPlan, string) (string, error) {
	return "test-grant", nil
}

func (*projectAssistantExplicitTestAuthority) RetireApprovedPlan(context.Context, string) (string, error) {
	return "test-tombstone", nil
}

func (*projectAssistantExplicitTestAuthority) PersistInitialExecutionPlan(context.Context, *projectAssistantApprovedPlan) (string, error) {
	return "test-execution-plan", nil
}

func (*projectAssistantExplicitTestAuthority) PromoteAdaptiveRun(context.Context) (store.AssistantRun, error) {
	return store.AssistantRun{}, store.ErrAssistantWorkItemConflict
}

func (a *projectAssistantExplicitTestAuthority) AdmitMutation(context.Context) error {
	a.admissions++
	return nil
}

func (*projectAssistantExplicitTestAuthority) PersistRun(context.Context, store.AssistantRun) error {
	return nil
}

func (*projectAssistantExplicitTestAuthority) PersistAudit(context.Context, []byte) error {
	return nil
}
