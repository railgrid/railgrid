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
	"strings"
	"testing"
	"time"

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

	"github.com/railgrid/provider-sdk/dataplane"
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

func TestRecoveryRepairsTerminalStoreOutcomeWithoutExecuting(t *testing.T) {
	for _, phase := range []store.RunPhase{store.RunPhaseSucceeded, store.RunPhaseFailed, store.RunPhaseAborted, store.RunPhasePendingApproval} {
		t.Run(string(phase), func(t *testing.T) {
			dyn := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())
			s, scope, run := newProjectionFixture(t, dyn, store.RunPhaseRunning)
			s.cfg.ProcessID = "recovery-process"
			s.projectRun(t.Context(), scope, run)
			run.Phase = phase
			run.Message = "durable outcome"
			if err := s.store.SaveRun(t.Context(), scope, run); err != nil {
				t.Fatal(err)
			}
			// Simulate an unavailable API server during terminal projection.
			fail := true
			dyn.PrependReactor("update", "runs", func(action ktesting.Action) (bool, runtime.Object, error) {
				if fail && action.GetSubresource() == "status" {
					return true, nil, errors.New("temporary API outage")
				}
				return false, nil, nil
			})
			if err := s.bg.RecoverRun(t.Context(), scope.ClusterID, scope.AgentName, run.ID); err == nil {
				t.Fatal("projection failure must reach the controller for retry")
			}
			fail = false
			for range 2 {
				if err := s.bg.RecoverRun(t.Context(), scope.ClusterID, scope.AgentName, run.ID); err != nil {
					t.Fatal(err)
				}
			}
			object := projectedObject(t, dyn, run.ID)
			got, _, _ := unstructured.NestedString(object.Object, "status", "phase")
			if got != string(phase) {
				t.Fatalf("projected phase=%s, want %s", got, phase)
			}
			stored, err := s.store.GetRun(t.Context(), scope, run.ID)
			if err != nil || stored.Attempt != run.Attempt || stored.ExecutionOwner != run.ExecutionOwner || stored.Phase != phase {
				t.Fatalf("recovery executed or changed the settled run: %+v, %v", stored, err)
			}
		})
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

// The row goes in before the object. The Run reconciler claims an object the
// moment the watch delivers it and its dispatch reads the row for the task, so
// an object that exists ahead of its row is a run that gets claimed and cannot
// be executed — which is how a webhook-fired run sat Pending in production.
func TestUnattendedRunProjectionWritesTheStoreRowBeforeTheObject(t *testing.T) {
	ctx := context.Background()
	dyn := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())
	s, scope, run := newProjectionFixture(t, dyn, store.RunPhasePending)
	run.ID = "new-unattended-run"
	created := false
	dyn.PrependReactor("create", "runs", func(ktesting.Action) (bool, runtime.Object, error) {
		if _, err := s.store.GetRun(ctx, scope, run.ID); err != nil {
			t.Fatalf("the Run object was created before its row: %v", err)
		}
		created = true
		return false, nil, nil
	})
	if err := s.saveNewRun(ctx, scope.ClusterID, scope, run); err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Fatal("the Run object was never created")
	}
	if phase, _, _ := unstructured.NestedString(projectedObject(t, dyn, run.ID).Object, "status", "phase"); phase != agentsv1alpha1.RunPhasePending {
		t.Fatalf("object phase=%q; want Pending", phase)
	}
}

// When the object cannot be written the row is taken back out: a Pending row
// with no object is a run nothing will ever claim, and the producer is told so
// it can retry the whole thing.
func TestUnattendedRunProjectionRemovesTheRowWhenTheObjectFails(t *testing.T) {
	ctx := context.Background()
	dyn := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())
	s, scope, run := newProjectionFixture(t, dyn, store.RunPhasePending)
	run.ID = "new-unattended-run"
	dyn.PrependReactor("create", "runs", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("virtual workspace unavailable")
	})
	err := s.saveNewRun(ctx, scope.ClusterID, scope, run)
	if err == nil || !strings.Contains(err.Error(), "virtual workspace unavailable") {
		t.Fatalf("err = %v, want the object write's failure", err)
	}
	if _, err := s.store.GetRun(ctx, scope, run.ID); err == nil {
		t.Fatal("the row survived a failed object write")
	}
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
	run.Checkpoint = []byte(`{"inboxID":"inbox-approval","tool":"shell","args":"{\"command\":\"pwd\",\"token\":\"secret\"}","engine":{"messages":[]}}`)
	if err := s.store.SaveRun(context.Background(), scope, run); err != nil {
		t.Fatal(err)
	}
	s.projectRun(context.Background(), scope, run)
	checked := false
	_, _ = s.resumeRun(context.Background(), scope, run.ID, runAccess{CR: projectionResumeCR{check: func() {
		checked = true
		phase, _, _ := unstructured.NestedString(projectedObject(t, dyn, run.ID).Object, "status", "phase")
		if phase != agentsv1alpha1.RunPhaseRunning {
			t.Fatalf("resumed phase=%q before continuation; want Running", phase)
		}
		page, err := s.store.ListMessages(t.Context(), scope, run.SessionID, 10, "")
		if err != nil || len(page.Items) != 1 {
			t.Fatalf("want one permission receipt before continuation, got %+v, %v", page, err)
		}
		receipt := page.Items[0]
		if receipt.ID != "permission-inbox-approval" || receipt.RunID != run.ID ||
			receipt.Metadata["permissionDecision"] != "approved" || receipt.Metadata["tool"] != "shell" ||
			receipt.Metadata["args"] != `{"command":"pwd","token":"[redacted]"}` {
			t.Fatalf("unexpected permission receipt: %+v", receipt)
		}
		if len(engineMessagesFromHistory(page.Items)) != 0 {
			t.Fatal("permission receipt appeared in model history")
		}
	}}}, resumeIntent{
		Approval: true, Approve: true, FromPhase: store.RunPhasePendingApproval,
		InboxItem: store.InboxItem{ID: "inbox-approval", Kind: store.InboxKindApproval, State: store.InboxStateApproved},
	})
	if !checked {
		t.Fatal("resume did not reach its continuation boundary")
	}
}

func TestQuestionResumeRecordsAnswerInConversation(t *testing.T) {
	dyn := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())
	s, scope, run := newProjectionFixture(t, dyn, store.RunPhasePendingApproval)
	run.Checkpoint = []byte(`{"inboxID":"inbox-question","engine":{"messages":[]}}`)
	if err := s.store.SaveRun(t.Context(), scope, run); err != nil {
		t.Fatal(err)
	}
	item := store.InboxItem{
		ID: "inbox-question", AgentName: run.AgentName, RunID: run.ID,
		Kind: store.InboxKindQuestion, Prompt: "Where should the note go?",
		Response: "Line guide",
		Payload: map[string]any{
			"questions": []map[string]any{{"id": "location", "prompt": "Where should the note go?"}},
			"answers":   map[string]*string{"location": new("Line guide")},
		},
	}
	checked := false
	started := false
	access := runAccess{CallerUser: "reviewer", CR: projectionResumeCR{checkContext: func(ctx context.Context) {
		identity, ok := dataplane.ProxiedIdentityFrom(ctx)
		if !ok || identity.User != "reviewer" || ctx.Err() != nil {
			t.Fatalf("resume lost caller identity or inherited request cancellation: %v, %v", identity, ctx.Err())
		}
	}, check: func() {
		checked = true
		if !started {
			t.Fatal("continuation began before the stream identified the run")
		}
	}}}
	ctx, cancel := context.WithCancel(dataplane.WithProxiedIdentity(t.Context(), dataplane.ProxiedIdentity{User: "reviewer"}))
	cancel()
	_, err := s.continueInboxRun(ctx, scope, item, access, true, item.Response, func(event string, data map[string]any) {
		if started || event != "start" || data["runID"] != run.ID || data["sessionID"] != run.SessionID || data["response"] != item.Response {
			t.Fatalf("unexpected resume frame: %q, %v", event, data)
		}
		started = true
		page, err := s.store.ListMessages(t.Context(), scope, run.SessionID, 20, "")
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Items) != 1 {
			t.Fatalf("got %d transcript messages, want the reply", len(page.Items))
		}
		reply := page.Items[0]
		if reply.Role != "user" || reply.Content != "Line guide" || reply.RunID != run.ID ||
			reply.Metadata["questionPrompt"] != item.Prompt || reply.Metadata["actorID"] != "reviewer" ||
			reply.Metadata["questions"] == nil || reply.Metadata["answers"] == nil {
			t.Fatalf("unexpected question reply: %+v", reply)
		}
	})
	if err == nil || !checked {
		t.Fatalf("question resume did not reach the fixture's failing continuation boundary: %v", err)
	}
}

type projectionResumeCR struct {
	fakeCR
	check        func()
	checkContext func(context.Context)
}

func (c projectionResumeCR) GetAgent(ctx context.Context, _ string) (*agentsv1alpha1.Agent, error) {
	if c.checkContext != nil {
		c.checkContext(ctx)
	}
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

func TestQuestionGroupResolution(t *testing.T) {
	s, scope, run := newProjectionFixture(t, dynamicfake.NewSimpleDynamicClient(runtime.NewScheme()), store.RunPhasePendingApproval)
	item := store.InboxItem{
		ID: "question-group", AgentName: run.AgentName, RunID: run.ID, Kind: store.InboxKindQuestion, State: store.InboxStatePending,
		Payload: map[string]any{"questions": []map[string]any{
			{"id": "a", "prompt": "Where?"}, {"id": "b", "prompt": "Who?"}, {"id": "c", "prompt": "When?"},
		}},
	}
	run.Checkpoint = []byte(`{"inboxID":"question-group"}`)
	if err := s.store.SaveRun(t.Context(), scope, run); err != nil {
		t.Fatal(err)
	}
	if err := s.store.AddInboxItem(t.Context(), scope, item); err != nil {
		t.Fatal(err)
	}
	answers := map[string]*string{"a": new("README"), "b": nil, "c": new("Monday")}
	response, err := formatQuestionAnswers(item, answers)
	if err != nil || response != "Where?\nREADME\n\nWho?\n(skipped by the user)\n\nWhen?\nMonday" {
		t.Fatalf("formatted response = %q, err = %v", response, err)
	}
	resolved, err := s.resolveInboxDecision(t.Context(), scope, item.ID, store.InboxStateAnswered, response, time.Now(), map[string]any{"answers": answers})
	if err != nil || resolved.Response != response || resolved.Payload["answers"] == nil {
		t.Fatalf("resolved = %+v, err = %v", resolved, err)
	}
	retry, err := s.resolveInboxDecision(t.Context(), scope, item.ID, store.InboxStateAnswered, "second reply", time.Now())
	if err != nil || retry.Response != response {
		t.Fatalf("retry did not return the original reply: %+v, %v", retry, err)
	}
	if _, err := s.resolveInboxDecision(t.Context(), scope, item.ID, store.InboxStateDenied, "via channel", time.Now()); !errors.Is(err, store.ErrInboxAlreadyResolved) {
		t.Fatalf("late channel dismissal: %v", err)
	}
	run.Checkpoint = []byte(`{"inboxID":"next-question"}`)
	if err := s.store.SaveRun(t.Context(), scope, run); err != nil {
		t.Fatal(err)
	}
	if _, err := s.store.ClaimRun(t.Context(), scope, run.ID, "retry", time.Now(), item.ID); err == nil {
		t.Fatal("stale answer claimed a different checkpoint")
	}
	if _, err := s.resolveInboxDecision(t.Context(), scope, item.ID, store.InboxStateAnswered, "second reply", time.Now()); !errors.Is(err, store.ErrInboxAlreadyResolved) {
		t.Fatalf("stale reply retry: %v", err)
	}
	delete(answers, "b")
	answers["wrong"] = nil
	if _, err := formatQuestionAnswers(item, answers); err == nil {
		t.Fatal("unknown question ID accepted")
	}
}

type answerDuringCleanupStore struct {
	store.Store
	t *testing.T
}

func (s answerDuringCleanupStore) GetInboxItem(ctx context.Context, scope store.Scope, id string) (store.InboxItem, error) {
	item, err := s.Store.GetInboxItem(ctx, scope, id)
	if err == nil {
		_, answerErr := s.ResolveInboxItem(ctx, scope, id, store.InboxStateAnswered, "User reply", time.Now(), store.InboxResolution{
			QuestionPayload: map[string]any{"answers": map[string]string{"a": "User reply"}},
		})
		if answerErr != nil {
			s.t.Fatal(answerErr)
		}
	}
	return item, err
}

func TestClosingParkedQuestionPreservesConcurrentReply(t *testing.T) {
	s, scope, run := newProjectionFixture(t, dynamicfake.NewSimpleDynamicClient(runtime.NewScheme()), store.RunPhasePendingApproval)
	run.Checkpoint = []byte(`{"inboxID":"race-question"}`)
	item := store.InboxItem{ID: "race-question", AgentName: run.AgentName, RunID: run.ID, Kind: store.InboxKindQuestion, State: store.InboxStatePending}
	original := s.store
	if err := original.AddInboxItem(t.Context(), scope, item); err != nil {
		t.Fatal(err)
	}
	s.store = answerDuringCleanupStore{Store: original, t: t}
	s.closeParkedInboxItem(t.Context(), scope, run, store.RunPhaseAborted, time.Now())
	got, err := original.GetInboxItem(t.Context(), scope, item.ID)
	if err != nil || got.Response != "User reply" || got.Payload["answers"] == nil {
		t.Fatalf("concurrent answer was lost: %+v, %v", got, err)
	}
}
