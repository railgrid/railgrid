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
	"strings"
	"testing"
	"time"

	"github.com/faroshq/provider-app-studio/store"
)

func TestProjectAssistantPublicWorkItemOmitsPlanGrantState(t *testing.T) {
	now := time.Now().UTC()
	item := store.AssistantWorkItem{
		ID:            "work-item-1",
		ProjectName:   "internal-project",
		ProjectUID:    "internal-project-uid",
		RootMessageID: "user-1",
		CreatedBy:     "alice",
		Status:        store.AssistantWorkItemStatusSuspended,
		StatusReason:  "interrupted",
		Revision:      3,
		PlanGrant:     json.RawMessage(`{"paths":["secret/path"]}`),
		GrantRevision: "secret-grant-revision",
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	raw, err := json.Marshal(projectAssistantWorkItemToAPI(item))
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"planGrant", "grantRevision", "projectName", "projectUID", "secret/path", "secret-grant-revision"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("public WorkItem contains internal value %q: %s", forbidden, raw)
		}
	}
	for _, required := range []string{`"id":"work-item-1"`, `"createdBy":"alice"`, `"revision":3`} {
		if !strings.Contains(string(raw), required) {
			t.Fatalf("public WorkItem is missing %s: %s", required, raw)
		}
	}
}

func TestDurableAskIsActorBoundDiscussionWithoutWorkItem(t *testing.T) {
	messages := store.NewMemoryStore()
	server := NewWithWorkspace(nil, messages, nil, "", false)
	scope := testProjectMessageScope("org-a", "workspace-a", "demo")

	started, err := server.startProjectAssistantRunDurably(
		context.Background(), scope, "alice", "What theme is active?", "ask-1",
		func(store.AssistantRun, store.Message, bool) error { return nil },
	)
	if err != nil {
		t.Fatalf("start Ask: %v", err)
	}
	if started.Run.Mode != store.AssistantRunModeDiscussion || started.Run.WorkItemID != "" {
		t.Fatalf("Ask run = %#v, want unlinked discussion", started.Run)
	}
	if started.User.ActorID != "alice" || started.User.WorkItemID != "" {
		t.Fatalf("Ask user = %#v, want actor-bound unlinked message", started.User)
	}
	assertProjectAssistantTurnOrder(t, started)
	items, err := messages.ListAssistantWorkItems(context.Background(), scope)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Fatalf("Ask created WorkItems: %#v", items)
	}
}

func TestAskContextExcludesEarlierWorkItemTodos(t *testing.T) {
	messages := store.NewMemoryStore()
	server := NewWithWorkspace(nil, messages, nil, "", false)
	scope := testProjectMessageScope("org-a", "workspace-a", "demo")

	priorDiscussion, err := server.startProjectAssistantRunDurably(
		context.Background(), scope, "alice", "Remember the accessible blue theme", "ask-prior",
		func(store.AssistantRun, store.Message, bool) error { return nil },
	)
	if err != nil {
		t.Fatal(err)
	}
	quote, err := server.startProjectAssistantBuildRunDurably(
		context.Background(), scope, "alice", "Add quote submission", "build-quote",
		func(store.AssistantRun, store.Message, bool) error { return nil },
	)
	if err != nil {
		t.Fatal(err)
	}
	completeWorkItemRunForTest(t, messages, scope, quote.Run)
	theme, err := server.startProjectAssistantBuildRunDurably(
		context.Background(), scope, "alice", "Switch the application theme", "build-theme",
		func(store.AssistantRun, store.Message, bool) error { return nil },
	)
	if err != nil {
		t.Fatal(err)
	}
	completeWorkItemRunForTest(t, messages, scope, theme.Run)
	ask, err := server.startProjectAssistantRunDurably(
		context.Background(), scope, "alice", "What theme is active?", "ask-theme",
		func(store.AssistantRun, store.Message, bool) error { return nil },
	)
	if err != nil {
		t.Fatal(err)
	}

	history, err := server.loadProjectAssistantTurnMessages(context.Background(), scope, ask.Run, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 4 || history[0].ID != priorDiscussion.User.ID || history[2].ID != ask.User.ID {
		t.Fatalf("Ask history = %#v, want prior discussion plus current question", history)
	}
	for _, message := range history {
		if message.WorkItemID != "" {
			t.Fatalf("Ask history leaked WorkItem message: %#v", message)
		}
	}
}

func completeWorkItemRunForTest(t *testing.T, messages store.Store, scope store.Scope, run store.AssistantRun) {
	t.Helper()
	item, err := messages.GetAssistantWorkItem(context.Background(), scope, run.WorkItemID)
	if err != nil {
		t.Fatal(err)
	}
	run.Status = store.AssistantRunStatusCompleted
	run.Revision++
	if err := messages.TransitionWorkItemAndRun(
		context.Background(), scope, item.ID, item.Revision, run,
		store.AssistantWorkItemStatusCompleted, "completed", time.Now().UTC(),
	); err != nil {
		t.Fatal(err)
	}
}

func TestDurableContinueResumesSelectedActorWorkItem(t *testing.T) {
	messages := store.NewMemoryStore()
	server := NewWithWorkspace(nil, messages, nil, "", false)
	scope := testProjectMessageScope("org-a", "workspace-a", "demo")
	built, err := server.startProjectAssistantBuildRunDurably(context.Background(), scope, "alice", "Implement dark mode", "build-1", func(store.AssistantRun, store.Message, bool) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	item, err := messages.GetAssistantWorkItem(context.Background(), scope, built.Run.WorkItemID)
	if err != nil {
		t.Fatal(err)
	}
	built.Run.Status = store.AssistantRunStatusInterrupted
	built.Run.Revision++
	if err := messages.TransitionWorkItemAndRun(context.Background(), scope, item.ID, item.Revision, built.Run, store.AssistantWorkItemStatusSuspended, "interrupted", time.Now().UTC()); err != nil {
		t.Fatalf("suspend WorkItem: %v", err)
	}
	item, err = messages.GetAssistantWorkItem(context.Background(), scope, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	continued, err := server.startProjectAssistantContinueRunDurably(context.Background(), scope, item.ID, "alice", item.Revision, "Continue", "continue-1", func(store.AssistantRun, store.Message, bool) error { return nil })
	if err != nil {
		t.Fatalf("start Continue: %v", err)
	}
	if continued.Run.Mode != store.AssistantRunModeContinue || continued.Run.WorkItemID != item.ID || continued.User.ActorID != "alice" {
		t.Fatalf("continue = %#v / %#v", continued.Run, continued.User)
	}
	assertProjectAssistantTurnOrder(t, continued)
	if _, err := server.startProjectAssistantContinueRunDurably(context.Background(), scope, item.ID, "bob", item.Revision, "Continue", "continue-2", func(store.AssistantRun, store.Message, bool) error { return nil }); !errors.Is(err, store.ErrAssistantWorkItemConflict) {
		t.Fatalf("wrong actor continuation error = %v, want work item conflict", err)
	}
}

func TestDurableBuildCreatesRootedActorBoundWorkItem(t *testing.T) {
	messages := store.NewMemoryStore()
	server := NewWithWorkspace(nil, messages, nil, "", false)
	scope := testProjectMessageScope("org-a", "workspace-a", "demo")

	started, err := server.startProjectAssistantBuildRunDurably(
		context.Background(), scope, "alice", "Implement dark mode", "build-1",
		func(store.AssistantRun, store.Message, bool) error { return nil },
	)
	if err != nil {
		t.Fatalf("start Build: %v", err)
	}
	if started.Run.Mode != store.AssistantRunModeNew || started.Run.WorkItemID == "" {
		t.Fatalf("Build run = %#v, want new WorkItem run", started.Run)
	}
	item, err := messages.GetAssistantWorkItem(context.Background(), scope, started.Run.WorkItemID)
	if err != nil {
		t.Fatal(err)
	}
	if item.CreatedBy != "alice" || item.RootMessageID != started.User.ID || item.ActiveRunID != started.Run.ID {
		t.Fatalf("WorkItem = %#v, want rooted actor-bound active run", item)
	}
	if started.User.WorkItemID != item.ID || started.Assistant.WorkItemID != item.ID {
		t.Fatalf("messages are not linked to WorkItem %q: user=%#v assistant=%#v", item.ID, started.User, started.Assistant)
	}
	assertProjectAssistantTurnOrder(t, started)
}

func assertProjectAssistantTurnOrder(t *testing.T, started projectAssistantDurableStartResult) {
	t.Helper()
	if !started.User.CreatedAt.Before(started.Assistant.CreatedAt) {
		t.Fatalf("turn timestamps = user %v, assistant %v; want user before assistant", started.User.CreatedAt, started.Assistant.CreatedAt)
	}
}

func TestWorkItemRunControlRejectsDifferentActor(t *testing.T) {
	messages := store.NewMemoryStore()
	server := NewWithWorkspace(nil, messages, nil, "", false)
	scope := testProjectMessageScope("org-a", "workspace-a", "demo")
	started, err := server.startProjectAssistantBuildRunDurably(
		context.Background(), scope, "alice", "Implement dark mode", "build-1",
		func(store.AssistantRun, store.Message, bool) error { return nil },
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := server.authorizeProjectAssistantRunActor(context.Background(), scope, started.Run, "bob", false); !errors.Is(err, store.ErrAssistantRunNotFound) {
		t.Fatalf("different actor authorization = %v, want not found", err)
	}
	if err := server.authorizeProjectAssistantRunActor(context.Background(), scope, started.Run, "alice", false); err != nil {
		t.Fatalf("creator authorization = %v", err)
	}
}

func TestRecreatedProjectNameCannotSeeOldProjectUIDWorkItem(t *testing.T) {
	messages := store.NewMemoryStore()
	server := NewWithWorkspace(nil, messages, nil, "", false)
	oldScope := store.Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo", ProjectUID: "uid-old"}
	newScope := store.Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo", ProjectUID: "uid-new"}

	started, err := server.startProjectAssistantBuildRunDurably(
		context.Background(), oldScope, "alice", "Implement quote submission", "build-old",
		func(store.AssistantRun, store.Message, bool) error { return nil },
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := messages.GetAssistantWorkItem(context.Background(), newScope, started.Run.WorkItemID); !errors.Is(err, store.ErrAssistantWorkItemNotFound) {
		t.Fatalf("new Project UID loaded old WorkItem: %v", err)
	}
}
