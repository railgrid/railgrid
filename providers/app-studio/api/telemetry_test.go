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
	"errors"
	"strings"
	"sync"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"

	producttelemetry "github.com/faroshq/provider-sdk/telemetry"

	aiv1alpha1 "github.com/faroshq/provider-app-studio/apis/ai/v1alpha1"
	asclient "github.com/faroshq/provider-app-studio/client"
)

var errTelemetryTestFailure = errors.New("telemetry test failure")

type recordingTelemetryTracker struct {
	mu     sync.Mutex
	events []producttelemetry.Event
	err    error
}

func (t *recordingTelemetryTracker) Track(_ context.Context, event producttelemetry.Event) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	properties := make(map[string]any, len(event.Properties))
	for key, value := range event.Properties {
		properties[key] = value
	}
	event.Properties = properties
	t.events = append(t.events, event)
	return t.err
}

func (*recordingTelemetryTracker) Close() error { return nil }

func (t *recordingTelemetryTracker) snapshot() []producttelemetry.Event {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]producttelemetry.Event, len(t.events))
	copy(out, t.events)
	return out
}

func telemetryTestProject() *aiv1alpha1.Project {
	return &aiv1alpha1.Project{ObjectMeta: metav1.ObjectMeta{Name: "project-name", UID: "project-uid"}}
}

func telemetryTestIdentity() identity {
	return identity{orgUUID: "org-uid", workspaceUUID: "workspace-uid", user: "actor-uid"}
}

func TestProjectTelemetryEventsUseStableIDsAndDeclaredProperties(t *testing.T) {
	tracker := &recordingTelemetryTracker{}
	server := &Server{telemetry: tracker}
	id := telemetryTestIdentity()
	project := telemetryTestProject()

	server.trackProjectCreated(context.Background(), id, project)
	server.trackProjectPublished(context.Background(), id, project, "published")
	server.observeDevelopmentPreviewReady(context.Background(), id, project)

	events := tracker.snapshot()
	if len(events) != 3 {
		t.Fatalf("telemetry events = %d, want 3: %#v", len(events), events)
	}
	if got, want := events[0].Action, appStudioProjectCreatedAction; got != want {
		t.Fatalf("created action = %q, want %q", got, want)
	}
	if events[0].OrgID != id.orgUUID || events[0].WorkspaceID != id.workspaceUUID || events[0].ProjectID != string(project.UID) || events[0].Actor != id.user {
		t.Fatalf("created identity = %#v, want scope/project/actor IDs", events[0])
	}
	if len(events[0].Properties) != 1 || events[0].Properties["outcome"] != "success" {
		t.Fatalf("created properties = %#v, want only outcome=success", events[0].Properties)
	}
	if events[1].Action != appStudioProjectPublishedAction || events[1].Properties["outcome"] != "published" {
		t.Fatalf("published event = %#v", events[1])
	}
	if events[2].Action != appStudioPreviewReadyAction || events[2].Actor != "" || events[2].Properties["preview_kind"] != "development" || events[2].Properties["outcome"] != "ready" {
		t.Fatalf("preview event = %#v", events[2])
	}
}

func TestProjectTelemetryDoesNotEmitWithoutRequiredStableIdentity(t *testing.T) {
	tracker := &recordingTelemetryTracker{}
	server := &Server{telemetry: tracker}
	project := telemetryTestProject()

	server.trackProjectCreated(context.Background(), identity{orgUUID: "org", workspaceUUID: "workspace"}, project)
	server.trackProjectPublished(context.Background(), identity{orgUUID: "org", workspaceUUID: "workspace", user: "actor"}, &aiv1alpha1.Project{ObjectMeta: metav1.ObjectMeta{Name: "name"}}, "published")
	server.observeDevelopmentPreviewReady(context.Background(), identity{orgUUID: "org"}, project)

	if events := tracker.snapshot(); len(events) != 0 {
		t.Fatalf("telemetry events = %#v, want none without complete stable IDs", events)
	}
}

func TestProjectTelemetryCreationAndPublicationOnlyAcceptSuccessBoundaries(t *testing.T) {
	tracker := &recordingTelemetryTracker{}
	server := &Server{telemetry: tracker}
	id := telemetryTestIdentity()
	project := telemetryTestProject()

	server.trackProjectPublished(context.Background(), id, project, "failed")
	server.trackProjectPublished(context.Background(), id, project, "")
	if events := tracker.snapshot(); len(events) != 0 {
		t.Fatalf("failed publication emitted %#v", events)
	}

	server.trackProjectCreated(context.Background(), id, project)
	if events := tracker.snapshot(); len(events) != 1 || events[0].Properties["outcome"] != "success" {
		t.Fatalf("creation boundary events = %#v", events)
	}
}

func TestCreateProjectTelemetryEmitsOnlyAfterAllSetupSucceeds(t *testing.T) {
	newClient := func(t *testing.T, failTouch bool) (*asclient.Client, *recordingTelemetryTracker) {
		t.Helper()
		dynamicClient := newProjectCreationTestDynamicClient(codeConnectionObjectWithValidated("github", metav1.ConditionTrue))
		dynamicClient.PrependReactor("create", "projects", func(action k8stesting.Action) (bool, runtime.Object, error) {
			object := action.(k8stesting.CreateAction).GetObject()
			object.(metav1.Object).SetUID("project-uid-created")
			return false, nil, nil
		})
		if failTouch {
			dynamicClient.PrependReactor("patch", "projects", func(k8stesting.Action) (bool, runtime.Object, error) {
				return true, nil, errTelemetryTestFailure
			})
		}
		return asclient.NewFromDynamic(dynamicClient), &recordingTelemetryTracker{}
	}

	preflight := &projectCreatePreflight{Naming: projectNamingResult{DisplayName: "Customer Portal", RepositoryName: "customer-portal"}}
	id := telemetryTestIdentity()

	client, tracker := newClient(t, false)
	server := &Server{telemetry: tracker}
	created, err := server.createProjectFromRequestWithPreflight(
		context.Background(), client, id,
		CreateProjectRequest{ConnectionRef: "github"}, nil, nil, preflight,
	)
	if err != nil {
		t.Fatalf("successful project creation returned error: %v", err)
	}
	if created == nil || len(tracker.snapshot()) != 1 || tracker.snapshot()[0].Action != appStudioProjectCreatedAction {
		t.Fatalf("successful create telemetry = %#v, want one created event", tracker.snapshot())
	}

	client, tracker = newClient(t, true)
	server = &Server{telemetry: tracker}
	if _, err := server.createProjectFromRequestWithPreflight(
		context.Background(), client, id,
		CreateProjectRequest{ConnectionRef: "github"}, nil, nil, preflight,
	); !errors.Is(err, errTelemetryTestFailure) {
		t.Fatalf("touch failure = %v, want setup error", err)
	}
	if events := tracker.snapshot(); len(events) != 0 {
		t.Fatalf("touch failure emitted telemetry = %#v, want none", events)
	}

	client, tracker = newClient(t, false)
	server = &Server{telemetry: tracker}
	if _, err := server.createProjectFromRequestWithPreflight(
		context.Background(), client, id,
		CreateProjectRequest{Prompt: "create this", ConnectionRef: "github"}, nil, nil, preflight,
	); err == nil {
		t.Fatal("creation without message store unexpectedly succeeded")
	}
	if events := tracker.snapshot(); len(events) != 0 {
		t.Fatalf("rollback failure emitted telemetry = %#v, want none", events)
	}
}

func TestPromoteProjectTelemetryDoesNotEmitWhenProjectUpdateFails(t *testing.T) {
	project := projectForPromoteWithRepository("shop", "repo-a")
	project.UID = "project-uid-promote"
	commitSHA := strings.Repeat("a", 40)
	commit := releaseCommitForTest("release", "repo-a", "Succeeded", commitSHA, metav1.Now().Time)
	packages := []*unstructured.Unstructured{
		projectBuildPackageForTest("repo-a", "frontend", "ghcr.io/acme/shop/frontend", map[string]any{"digest": "sha256:front", "tags": []any{"sha-" + commitSHA}}),
		projectBuildPackageForTest("repo-a", "backend", "ghcr.io/acme/shop/backend", map[string]any{"digest": "sha256:back", "tags": []any{"sha-" + commitSHA}}),
	}
	client := newProjectBuildProvenanceClient(project, []*unstructured.Unstructured{commit}, packages)
	persisted, err := client.Projects().Get(context.Background(), project.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get project: %v", err)
	}
	dynamicClient, ok := client.Dynamic().(*dynamicfake.FakeDynamicClient)
	if !ok {
		t.Fatal("project client is not backed by a fake dynamic client")
	}
	dynamicClient.PrependReactor("update", "projects", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errTelemetryTestFailure
	})

	tracker := &recordingTelemetryTracker{}
	server := &Server{telemetry: tracker}
	_, _, err = server.promoteProjectWithSelection(
		context.Background(), client, telemetryTestIdentity(), persisted, nil, nil, "", false,
	)
	if !errors.Is(err, errTelemetryTestFailure) {
		t.Fatalf("promotion update failure = %v, want update error", err)
	}
	if events := tracker.snapshot(); len(events) != 0 {
		t.Fatalf("promotion update failure emitted telemetry = %#v, want none", events)
	}
}

func TestProjectTelemetryPreviewReadyDeduplicatesConcurrentObservation(t *testing.T) {
	tracker := &recordingTelemetryTracker{}
	server := &Server{telemetry: tracker}
	id := telemetryTestIdentity()
	project := telemetryTestProject()

	const callers = 32
	var wg sync.WaitGroup
	wg.Add(callers)
	for range callers {
		go func() {
			defer wg.Done()
			server.observeDevelopmentPreviewReady(context.Background(), id, project)
		}()
	}
	wg.Wait()

	events := tracker.snapshot()
	if len(events) != 1 {
		t.Fatalf("concurrent preview observations emitted %d events, want one: %#v", len(events), events)
	}
	if events[0].ProjectID != string(project.UID) || events[0].Properties["outcome"] != "ready" {
		t.Fatalf("deduplicated preview event = %#v", events[0])
	}
}

func TestProjectTelemetryPreviewReadyRetriesAfterTrackerFailure(t *testing.T) {
	tracker := &recordingTelemetryTracker{err: errTelemetryTestFailure}
	server := &Server{telemetry: tracker}
	id := telemetryTestIdentity()
	project := telemetryTestProject()

	server.observeDevelopmentPreviewReady(context.Background(), id, project)
	if events := tracker.snapshot(); len(events) != 1 {
		t.Fatalf("failed preview observation events = %d, want one attempted event", len(events))
	}

	tracker.mu.Lock()
	tracker.err = nil
	tracker.mu.Unlock()
	server.observeDevelopmentPreviewReady(context.Background(), id, project)
	if events := tracker.snapshot(); len(events) != 2 {
		t.Fatalf("preview retry events = %d, want two attempts", len(events))
	}
}

func TestNewServerDefaultsToNoopTelemetry(t *testing.T) {
	server := NewWithWorkspaceContext(context.Background(), nil, nil, nil, "", false)
	defer server.Shutdown(context.Background())
	if _, ok := server.telemetry.(producttelemetry.NoopTracker); !ok {
		t.Fatalf("default telemetry = %T, want telemetry.NoopTracker", server.telemetry)
	}
}
