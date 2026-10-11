/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package api

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	aiv1alpha1 "github.com/railgrid/provider-app-studio/apis/ai/v1alpha1"
	"github.com/railgrid/provider-app-studio/store"
	"github.com/railgrid/provider-app-studio/workspace"
)

type sourceFencePreviewInspector struct {
	observe func()
	calls   int
}

func (*sourceFencePreviewInspector) Health(context.Context) error { return nil }

func (f *sourceFencePreviewInspector) Inspect(context.Context, projectAssistantPreviewInspectionRequest) (projectAssistantPreviewInspectionResult, error) {
	f.calls++
	if f.observe != nil {
		f.observe()
	}
	return projectAssistantPreviewInspectionResult{
		Status: "succeeded", Assertions: []projectAssistantPreviewInspectionAssertionResult{{Passed: true}},
		Screenshot: &projectAssistantPreviewInspectionScreenshot{Base64: "cGljdHVyZQ=="},
	}, nil
}

// Start with a dirty private sandbox so verification must checkpoint and wait
// for synchronization before it can observe the browser or runtime.
func newSourceFencePreviewFixture(t *testing.T) (*Server, projectAssistantToolCallRequest, *sourceFencePreviewInspector, func()) {
	t.Helper()
	ctx := context.Background()
	files := workspace.NewFileStore(t.TempDir())
	scope := workspace.Scope{OrgUUID: "org", WorkspaceUUID: "ws", ProjectName: "shop", ProjectUID: "project-uid"}
	if err := files.ApplyFiles(ctx, scope, []workspace.File{{Path: "main.go", Content: "old\n"}}); err != nil {
		t.Fatal(err)
	}
	current, err := files.ReadFile(ctx, scope, workspace.ReadOptions{Path: "main.go", MaxBytes: workspace.MaxReadMaxBytes})
	if err != nil {
		t.Fatal(err)
	}
	revision, err := files.SourceRevision(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	if err := files.RecordPreviewCheckpoint(ctx, scope, workspace.SourceChange{SourceRevision: revision}); err != nil {
		t.Fatal(err)
	}
	oldDigest := projectSandboxSyncDigest([]projectSandboxSyncFile{{Path: "main.go", Content: "old\n"}})
	newDigest := projectSandboxSyncDigest([]projectSandboxSyncFile{{Path: "main.go", Content: "new\n"}})
	state := newProjectEinoAssistantRunState()
	state.SetTurnPolicy(projectAssistantTurnPolicyForProfile(projectAssistantTurnProfileImplementation))
	state.RecordSourceMutation()
	fake := &sandboxClientFake{response: projectAssistantSandboxWorkspaceResponse{
		SourceRevision: revision + 1, SourceDigest: newDigest,
		Changes: []projectAssistantSandboxWorkspaceChange{{
			Path: "main.go", Operation: string(workspace.ManagedFileReplace), Content: "new\n", ExpectedVersion: current.Version,
		}},
	}}
	project := &aiv1alpha1.Project{}
	project.Name, project.UID = "shop", "project-uid"
	project.Spec.Template = &aiv1alpha1.ProjectTemplateSpec{Name: "application"}
	id := identity{orgUUID: "org", workspaceUUID: "ws"}
	server := NewWithWorkspace(nil, store.NewMemoryStore(), files, "", false)
	server.tenantWorkspaces, server.tenantActors = defaultTestWorkspaces.lookup, defaultTestActors.lookup
	server.developmentSyncAfterMutation = func(identity, *aiv1alpha1.Project, string) error { return nil }
	inspector := &sourceFencePreviewInspector{}
	server.previewInspector = inspector
	server.previewInspectionResolveURL = func(context.Context, identity, *aiv1alpha1.Project) (string, error) {
		return "https://demo.preview.example/", nil
	}
	state.SetSandbox(&projectAssistantRunSandbox{
		server: server, client: fake, id: id, project: project, scope: scope,
		target: projectDevelopmentSyncTargetInfo{Resource: "instances", ResourceName: "run"}, runState: state,
		metadata: projectAssistantRunSandboxMetadata{
			Status: "active", SourceRevision: revision, SourceDigest: oldDigest,
			RemoteRevision: revision + 1, RemoteDigest: newDigest, RemoteCheckpointID: "baseline",
			RunID: "run", HardExpiresAt: time.Now().Add(time.Hour),
		},
	})
	mutate := func() {
		t.Helper()
		baseline, err := files.ReadFile(ctx, scope, workspace.ReadOptions{Path: "main.go", MaxBytes: workspace.MaxReadMaxBytes})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := files.ReplaceFile(ctx, scope, workspace.ReplaceOptions{Path: "main.go", Content: "sibling revision\n", ExpectedVersion: baseline.Version}); err != nil {
			t.Fatal(err)
		}
	}
	return server, projectAssistantToolCallRequest{Identity: id, Project: project, WorkspaceScope: scope, RunState: state}, inspector, mutate
}

func TestPreviewInspectionRejectsSourceChangesDuringVerification(t *testing.T) {
	for _, phase := range []string{"sync", "resolve", "inspect", "private-inspect", "unchanged"} {
		t.Run(phase, func(t *testing.T) {
			server, request, inspector, mutate := newSourceFencePreviewFixture(t)
			switch phase {
			case "sync":
				server.developmentSyncAfterMutation = func(identity, *aiv1alpha1.Project, string) error { mutate(); return nil }
			case "resolve":
				server.previewInspectionResolveURL = func(context.Context, identity, *aiv1alpha1.Project) (string, error) {
					mutate()
					return "https://demo.preview.example/", nil
				}
			case "inspect":
				inspector.observe = mutate
			case "private-inspect":
				inspector.observe = func() {
					sandbox := request.RunState.Sandbox()
					sandbox.mu.Lock()
					sandbox.metadata.RemoteDigest = "private-source-changed-during-observation"
					sandbox.mu.Unlock()
				}
			}
			result, err := server.inspectProjectDevelopmentPreviewResult(context.Background(), request, true)
			if err != nil {
				t.Fatal(err)
			}
			if phase == "unchanged" {
				if result.Status != "succeeded" || inspector.calls != 1 || result.Screenshot == nil {
					t.Fatalf("unchanged source lost valid evidence: %#v, calls=%d", result, inspector.calls)
				}
				return
			}
			if result.Status != "failed" || result.FailureKind != "not_current" || len(result.Assertions) != 0 || result.Screenshot != nil || result.InteractionEvidence {
				t.Fatalf("changed source retained positive evidence: %#v", result)
			}
			wantCalls := 0
			if phase == "inspect" || phase == "private-inspect" {
				wantCalls = 1
			}
			if inspector.calls != wantCalls {
				t.Fatalf("inspection calls = %d, want %d", inspector.calls, wantCalls)
			}
		})
	}
}

func TestPreviewSourceFenceCannotBeRelabeledByNewSandboxMetadata(t *testing.T) {
	server, request, _, mutate := newSourceFencePreviewFixture(t)
	if _, err := server.checkpointProjectAssistantRunSandboxIfDirty(context.Background(), request.RunState); err != nil {
		t.Fatal(err)
	}
	fence, err := server.captureProjectAssistantPreviewSourceFence(context.Background(), request.RunState, request.WorkspaceScope)
	if err != nil {
		t.Fatal(err)
	}
	mutate()
	sandbox := request.RunState.Sandbox()
	revision, err := server.workspaces.SourceRevision(context.Background(), request.WorkspaceScope)
	if err != nil {
		t.Fatal(err)
	}
	sandbox.mu.Lock()
	sandbox.metadata.SourceRevision = revision
	sandbox.metadata.SourceDigest = "new-snapshot"
	sandbox.metadata.RemoteDigest = "new-snapshot"
	sandbox.mu.Unlock()
	if err := fence.validate(context.Background()); !errors.Is(err, errProjectAssistantRunSandboxConflict) {
		t.Fatalf("new metadata relabeled old evidence: %v", err)
	}
}

func TestPreviewInspectionFencesSourceWithoutPrivateSandbox(t *testing.T) {
	for _, withState := range []bool{false, true} {
		t.Run(map[bool]string{false: "no-run-state", true: "lazy-sandbox"}[withState], func(t *testing.T) {
			server, request, inspector, mutate := newSourceFencePreviewFixture(t)
			request.RunState = nil
			if withState {
				request.RunState = newProjectEinoAssistantRunState()
			}
			inspector.observe = mutate
			result, err := server.inspectProjectDevelopmentPreviewResult(context.Background(), request, true)
			if err != nil {
				t.Fatal(err)
			}
			if result.Status != "failed" || result.FailureKind != "not_current" || inspector.calls != 1 {
				t.Fatalf("uninitialized sandbox bypassed source validation: %#v, calls=%d", result, inspector.calls)
			}
		})
	}
}

func TestPreviewInspectionRejectsStaleSharedPreviewWithoutPrivateSandbox(t *testing.T) {
	for _, withState := range []bool{false, true} {
		t.Run(map[bool]string{false: "no-run-state", true: "lazy-run-state"}[withState], func(t *testing.T) {
			server, request, inspector, mutate := newSourceFencePreviewFixture(t)
			request.RunState = nil
			if withState {
				request.RunState = newProjectEinoAssistantRunState()
			}
			mutate()

			result, err := server.inspectProjectDevelopmentPreviewResult(context.Background(), request, true)
			if err != nil {
				t.Fatal(err)
			}
			if result.Status != "failed" || result.FailureKind != "not_current" || inspector.calls != 0 || result.Screenshot != nil {
				t.Fatalf("stale shared preview produced positive inspection evidence: %#v, calls=%d", result, inspector.calls)
			}
			if !strings.Contains(result.Summary, "shared preview has not applied the current workspace source") {
				t.Fatalf("stale shared preview failure is not actionable: %q", result.Summary)
			}
		})
	}
}

func TestPreviewInspectionPropagatesCancellationDuringObservation(t *testing.T) {
	server, request, inspector, _ := newSourceFencePreviewFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	inspector.observe = cancel
	if _, err := server.inspectProjectDevelopmentPreviewResult(ctx, request, true); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation was mislabeled as source drift: %v", err)
	}
}

func TestPreviewInspectionPropagatesCancellationBeforeCheckpointAndDuringSync(t *testing.T) {
	for _, phase := range []string{"checkpoint", "sync"} {
		t.Run(phase, func(t *testing.T) {
			server, request, inspector, _ := newSourceFencePreviewFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if phase == "checkpoint" {
				cancel()
			} else {
				server.developmentSyncAfterMutation = func(identity, *aiv1alpha1.Project, string) error {
					cancel()
					return nil
				}
			}
			if _, err := server.inspectProjectDevelopmentPreviewResult(ctx, request, true); !errors.Is(err, context.Canceled) {
				t.Fatalf("cancellation during %s was mislabeled as source drift: %v", phase, err)
			}
			if inspector.calls != 0 {
				t.Fatalf("canceled verification still inspected the browser %d times", inspector.calls)
			}
		})
	}
}

func TestPreviewInspectionRejectsLazyPrivateMutationDuringObservation(t *testing.T) {
	server, request, inspector, _ := newSourceFencePreviewFixture(t)
	sandbox := request.RunState.Sandbox()
	request.RunState = newProjectEinoAssistantRunState()
	inspector.observe = func() {
		sandbox.mu.Lock()
		sandbox.metadata.RemoteDigest = "private-mutation-without-checkpoint"
		sandbox.mu.Unlock()
		request.RunState.SetSandbox(sandbox)
	}
	result, err := server.inspectProjectDevelopmentPreviewResult(context.Background(), request, true)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "failed" || result.FailureKind != "not_current" {
		t.Fatalf("late private sandbox initialization bypassed source validation: %#v", result)
	}
}

func TestNativeBrowserFreshnessRejectsSourceChangedDuringSync(t *testing.T) {
	server, request, _, mutate := newSourceFencePreviewFixture(t)
	server.developmentSyncAfterMutation = func(identity, *aiv1alpha1.Project, string) error { mutate(); return nil }
	if err := server.ensureProjectAssistantPreviewCurrent(context.Background(), request); !errors.Is(err, errProjectAssistantRunSandboxConflict) {
		t.Fatalf("native browser accepted a changed source snapshot: %v", err)
	}
}

func TestNativeBrowserCancellationDuringFreshnessPreflightIsNotSuccess(t *testing.T) {
	state := &nativeBrowserOriginGuardTestState{}
	server, request := newNativeBrowserOriginGuardTestServer(t, state)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server.previewInspectionResolveURL = func(context.Context, identity, *aiv1alpha1.Project) (string, error) {
		cancel()
		return "https://demo.preview.example/", nil
	}
	if result, err := server.callProjectAssistantNativeBrowserTool(ctx, request, "browser_click", projectAssistantToolRiskRuntime); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled preflight became a successful tool receipt: result=%q, err=%v", result, err)
	}
	if state.toolCalls["browser_click"] != 0 {
		t.Fatalf("canceled preflight dispatched a click %d times", state.toolCalls["browser_click"])
	}
}

func TestNativeBrowserCancellationDuringSafetyPreflightIsNotSuccess(t *testing.T) {
	state := &nativeBrowserOriginGuardTestState{}
	server, request := newNativeBrowserOriginGuardTestServer(t, state)
	warmNativeBrowserOriginGuardTestSession(t, server, request)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	state.cancelNextSnapshot = cancel
	if result, err := server.callProjectAssistantNativeBrowserTool(ctx, request, "browser_click", projectAssistantToolRiskRuntime); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled safety preflight became a successful tool receipt: result=%q, err=%v", result, err)
	}
	if state.toolCalls["browser_click"] != 0 {
		t.Fatalf("canceled safety preflight dispatched a click %d times", state.toolCalls["browser_click"])
	}
}
