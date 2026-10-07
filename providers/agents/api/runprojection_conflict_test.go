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
	"errors"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	ktesting "k8s.io/client-go/testing"

	agentsv1alpha1 "github.com/railgrid/provider-agents/apis/v1alpha1"
	agentsclient "github.com/railgrid/provider-agents/client"
	"github.com/railgrid/provider-agents/store"
)

func TestRunProjectionRetriesTheCreateFinalizerConflict(t *testing.T) {
	ctx := context.Background()
	dyn := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())
	s, scope, run := newProjectionFixture(t, dyn, store.RunPhaseRunning)
	attempts := 0
	dyn.PrependReactor("update", "runs", func(action ktesting.Action) (bool, runtime.Object, error) {
		if action.GetSubresource() != "status" {
			return false, nil, nil
		}
		attempts++
		if attempts > 1 {
			return false, nil, nil
		}
		// The real reconciler adds its finalizer immediately after creation,
		// invalidating the resourceVersion returned by the Create response.
		object, err := dyn.Tracker().Get(agentsclient.RunGVR, "", run.ID)
		if err != nil {
			t.Fatal(err)
		}
		changed := object.(*unstructured.Unstructured).DeepCopy()
		changed.SetFinalizers([]string{"agents.railgrid.ai/run-data"})
		if err := dyn.Tracker().Update(agentsclient.RunGVR, changed, ""); err != nil {
			t.Fatal(err)
		}
		return true, nil, projectionConflict(run.ID)
	})
	s.projectRun(ctx, scope, run)
	object := projectedObject(t, dyn, run.ID)
	if phase, _, _ := unstructured.NestedString(object.Object, "status", "phase"); phase != string(run.Phase) || attempts != 2 {
		t.Fatalf("phase=%q attempts=%d; want Running projected on retry", phase, attempts)
	}
	if len(object.GetFinalizers()) != 1 {
		t.Fatal("the concurrent finalizer was discarded")
	}
}

func TestRunProjectionConflictAdoptsTheCurrentTerminalRow(t *testing.T) {
	for _, phase := range []store.RunPhase{store.RunPhaseSucceeded, store.RunPhaseFailed, store.RunPhaseAborted} {
		t.Run(string(phase), func(t *testing.T) {
			ctx := context.Background()
			dyn := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())
			s, scope, run := newProjectionFixture(t, dyn, store.RunPhaseRunning)
			attempts := 0
			dyn.PrependReactor("update", "runs", func(action ktesting.Action) (bool, runtime.Object, error) {
				if action.GetSubresource() != "status" {
					return false, nil, nil
				}
				attempts++
				if attempts > 1 {
					return false, nil, nil
				}
				terminal := run
				terminal.Phase, terminal.UpdatedAt, terminal.FinishedAt = phase, projectedFinish, &projectedFinish
				if err := s.store.SaveRun(ctx, scope, terminal); err != nil {
					t.Fatal(err)
				}
				tracked, err := dyn.Tracker().Get(agentsclient.RunGVR, "", run.ID)
				if err != nil {
					t.Fatal(err)
				}
				object := tracked.(*unstructured.Unstructured).DeepCopy()
				status, err := runtime.DefaultUnstructuredConverter.ToUnstructured(ptr(runStatusFor(terminal)))
				if err != nil {
					t.Fatal(err)
				}
				object.Object["status"] = status
				if err := dyn.Tracker().Update(agentsclient.RunGVR, object, ""); err != nil {
					t.Fatal(err)
				}
				return true, nil, projectionConflict(run.ID)
			})
			s.projectRun(ctx, scope, run)
			object := projectedObject(t, dyn, run.ID)
			if got, _, _ := unstructured.NestedString(object.Object, "status", "phase"); got != string(phase) {
				t.Fatalf("terminal %s rolled back to %s", phase, got)
			}
			if attempts != 1 {
				t.Fatalf("attempted %d status writes; latest terminal should be a no-op", attempts)
			}
		})
	}
}

func TestRunProjectionUsesCurrentRowBeforeTheFirstWrite(t *testing.T) {
	ctx := context.Background()
	dyn := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())
	s, scope, captured := newProjectionFixture(t, dyn, store.RunPhaseRunning)
	latest := captured
	latest.Phase, latest.FinishedAt = store.RunPhaseSucceeded, &projectedFinish
	if err := s.store.SaveRun(ctx, scope, latest); err != nil {
		t.Fatal(err)
	}
	s.projectRun(ctx, scope, captured)
	if phase, _, _ := unstructured.NestedString(projectedObject(t, dyn, captured.ID).Object, "status", "phase"); phase != string(latest.Phase) {
		t.Fatalf("phase=%s; projection replayed the captured Running snapshot", phase)
	}
}

func TestUnattendedRunProjectionCreatesObjectBeforeItsStoreRow(t *testing.T) {
	ctx := context.Background()
	dyn := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())
	s, scope, run := newProjectionFixture(t, dyn, store.RunPhasePending)
	run.ID = "new-unattended-run"
	s.store = projectionAdmissionStore{Store: s.store, check: func() {
		object := projectedObject(t, dyn, run.ID)
		if phase, _, _ := unstructured.NestedString(object.Object, "status", "phase"); phase != agentsv1alpha1.RunPhasePending {
			t.Fatalf("object phase before row=%q; want Pending", phase)
		}
	}}
	if err := s.saveNewRun(ctx, scope.ClusterID, scope, run); err != nil {
		t.Fatal(err)
	}
	if _, err := s.store.GetRun(ctx, scope, run.ID); err != nil {
		t.Fatal(err)
	}
}

type projectionAdmissionStore struct {
	store.Store
	check func()
}

func (s projectionAdmissionStore) SaveRun(ctx context.Context, scope store.Scope, run store.Run) error {
	s.check()
	return s.Store.SaveRun(ctx, scope, run)
}

func TestRunProjectionNeverReopensTerminalOrRequeuesStartedObjects(t *testing.T) {
	for _, current := range []string{agentsv1alpha1.RunPhaseSucceeded, agentsv1alpha1.RunPhaseFailed, agentsv1alpha1.RunPhaseAborted, agentsv1alpha1.RunPhaseRunning} {
		t.Run(current, func(t *testing.T) {
			dyn := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())
			capturedPhase := store.RunPhaseRunning
			if current == agentsv1alpha1.RunPhaseRunning {
				capturedPhase = store.RunPhasePending
			}
			s, _, run := newProjectionFixture(t, dyn, capturedPhase)
			object := &unstructured.Unstructured{Object: map[string]any{
				"apiVersion": agentsv1alpha1.SchemeGroupVersion.String(), "kind": "Run",
				"metadata": map[string]any{"name": run.ID}, "status": map[string]any{"phase": current},
			}}
			if _, err := dyn.Resource(agentsclient.RunGVR).Create(context.Background(), object, metav1.CreateOptions{}); err != nil {
				t.Fatal(err)
			}
			if err := s.writeRunObject(context.Background(), dyn, run, nil); err != nil {
				t.Fatal(err)
			}
			if got, _, _ := unstructured.NestedString(projectedObject(t, dyn, run.ID).Object, "status", "phase"); got != current {
				t.Fatalf("phase %s rolled back to %s", current, got)
			}
		})
	}
}

func TestRunProjectionPreservesAllControllerClaimFields(t *testing.T) {
	dyn := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())
	s, scope, run := newProjectionFixture(t, dyn, store.RunPhaseRunning)
	current := map[string]any{
		"phase": "Pending", "owner": "leader-1", "claimedAt": "2026-09-19T12:00:00Z", "attempt": int64(2),
		"deadlineAt": "2026-09-19T12:10:00Z", "observedGeneration": int64(1),
		"conditions": []any{map[string]any{"type": "Validated", "status": "True"}},
	}
	object := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": agentsv1alpha1.SchemeGroupVersion.String(), "kind": "Run",
		"metadata": map[string]any{"name": run.ID}, "status": current,
	}}
	if _, err := dyn.Resource(agentsclient.RunGVR).Create(context.Background(), object, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	s.projectRun(context.Background(), scope, run)
	got, _, _ := unstructured.NestedMap(projectedObject(t, dyn, run.ID).Object, "status")
	for _, key := range []string{"owner", "claimedAt", "attempt", "deadlineAt", "observedGeneration", "conditions"} {
		if !equalJSON(map[string]any{key: current[key]}, map[string]any{key: got[key]}) {
			t.Errorf("controller field %s changed: %v -> %v", key, current[key], got[key])
		}
	}
}

func TestRunProjectionReturnsExhaustedConflicts(t *testing.T) {
	dyn := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())
	s, _, run := newProjectionFixture(t, dyn, store.RunPhaseRunning)
	attempts := 0
	dyn.PrependReactor("update", "runs", func(action ktesting.Action) (bool, runtime.Object, error) {
		if action.GetSubresource() != "status" {
			return false, nil, nil
		}
		attempts++
		return true, nil, projectionConflict(run.ID)
	})
	if err := s.writeRunObject(context.Background(), dyn, run, nil); !apierrors.IsConflict(err) {
		t.Fatalf("error=%v; want the exhausted conflict reported", err)
	}
	if attempts != 5 {
		t.Fatalf("attempts=%d; want bounded default retry", attempts)
	}
}

func TestRunProjectionStopsRetriesAtItsContextDeadline(t *testing.T) {
	dyn := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())
	s, _, run := newProjectionFixture(t, dyn, store.RunPhaseRunning)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	attempts := 0
	dyn.PrependReactor("update", "runs", func(action ktesting.Action) (bool, runtime.Object, error) {
		if action.GetSubresource() != "status" {
			return false, nil, nil
		}
		attempts++
		cancel()
		return true, nil, projectionConflict(run.ID)
	})
	if err := s.writeRunObject(ctx, dyn, run, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("error=%v; want canceled projection stopped", err)
	}
	if attempts != 1 {
		t.Fatalf("attempts=%d after cancellation", attempts)
	}
}

func TestApprovalResumeProjectsClaimedRunningBeforeContinuing(t *testing.T) {
	dyn := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())
	s, scope, run := newProjectionFixture(t, dyn, store.RunPhasePendingApproval)
	run.Checkpoint = []byte(`{"engine":{"messages":[]}}`)
	if err := s.store.SaveRun(context.Background(), scope, run); err != nil {
		t.Fatal(err)
	}
	s.projectRun(context.Background(), scope, run)
	checked := false
	s.resumeRun(context.Background(), scope, run.ID, runAccess{CR: projectionResumeCR{check: func() {
		checked = true
		phase, _, _ := unstructured.NestedString(projectedObject(t, dyn, run.ID).Object, "status", "phase")
		if phase != agentsv1alpha1.RunPhaseRunning {
			t.Fatalf("resumed phase=%q before continuation; want Running", phase)
		}
	}}}, resumeIntent{Approval: true, Approve: true, FromPhase: store.RunPhasePendingApproval})
	if !checked {
		t.Fatal("resume did not reach its continuation boundary")
	}
}

type projectionResumeCR struct {
	fakeCR
	check func()
}

func (c projectionResumeCR) GetAgent(context.Context, string) (*agentsv1alpha1.Agent, error) {
	c.check()
	return nil, errors.New("stop test after the projection boundary")
}

func newProjectionFixture(t *testing.T, dyn dynamic.Interface, phase store.RunPhase) (*Server, store.Scope, store.Run) {
	t.Helper()
	scope := store.Scope{OrgUUID: "projection-org", WorkspaceUUID: "projection-workspace", AgentName: "scout", ClusterID: "projection-cluster"}
	run := store.Run{ID: "projection-run", AgentName: scope.AgentName, SessionID: "chat", Trigger: "chat", Phase: phase, CreatedAt: projectedStart, UpdatedAt: projectedStart, StartedAt: &projectedStart}
	s := &Server{store: store.NewMemoryStore()}
	s.bg = &background{server: s, scopedFn: func(context.Context, string) (dynamic.Interface, error) { return dyn, nil }}
	if err := s.store.SaveTenantRef(context.Background(), scope.ClusterID, store.TenantRef{OrgUUID: scope.OrgUUID, WorkspaceUUID: scope.WorkspaceUUID, UpdatedAt: projectedStart}); err != nil {
		t.Fatal(err)
	}
	if err := s.store.SaveRun(context.Background(), scope, run); err != nil {
		t.Fatal(err)
	}
	return s, scope, run
}

func projectedObject(t *testing.T, dyn dynamic.Interface, id string) *unstructured.Unstructured {
	t.Helper()
	object, err := dyn.Resource(agentsclient.RunGVR).Get(context.Background(), id, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return object
}

func projectionConflict(id string) error {
	return apierrors.NewConflict(schema.GroupResource{Group: agentsclient.RunGVR.Group, Resource: agentsclient.RunGVR.Resource}, id, errors.New("concurrent Run write"))
}
