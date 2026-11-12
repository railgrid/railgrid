// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package run

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"

	agentsv1alpha1 "github.com/railgrid/provider-agents/apis/v1alpha1"
)

func TestTerminalRunWaitsForChildrenOnTheWatchNotATimer(t *testing.T) {
	for name, tc := range map[string]struct {
		err     error
		requeue bool
	}{
		"children still settling": {fmt.Errorf("child Run c1 is still active: %w", ErrAwaitingChildRuns), false},
		"several children settling": {errors.Join(
			fmt.Errorf("c1: %w", ErrAwaitingChildRuns), fmt.Errorf("c2: %w", ErrAwaitingChildRuns)), false},
		"a real failure among them": {errors.Join(
			fmt.Errorf("c1: %w", ErrAwaitingChildRuns), errors.New("listing descendants failed")), true},
	} {
		t.Run(name, func(t *testing.T) {
			r := &Reconciler{ProcessID: "test", ReconcileDescendants: func(context.Context, string, string, string) error { return tc.err }}
			terminal := newRun("terminal", func(run *agentsv1alpha1.Run) { run.Status.Phase = agentsv1alpha1.RunPhaseAborted })
			_, result := run(t, r, terminal)
			if got := result.RequeueAfter == descendantReconcileRetry; got != tc.requeue {
				t.Fatalf("requeue=%s, want timer=%t", result.RequeueAfter, tc.requeue)
			}
		})
	}
}

func TestSettledChildEnqueuesItsParent(t *testing.T) {
	active := newRun("child", func(run *agentsv1alpha1.Run) {
		run.Spec.ParentRunRef = "parent"
		run.Status.Phase = agentsv1alpha1.RunPhaseRunning
	})
	settled := active.DeepCopy()
	settled.Status.Phase = agentsv1alpha1.RunPhaseAborted
	if !childSettled.Update(event.UpdateEvent{ObjectOld: active, ObjectNew: settled}) {
		t.Fatal("a child reaching a terminal phase was filtered out")
	}
	if childSettled.Update(event.UpdateEvent{ObjectOld: settled, ObjectNew: settled.DeepCopy()}) {
		t.Fatal("an already terminal child passed the filter")
	}
	orphan := settled.DeepCopy()
	orphan.Spec.ParentRunRef = ""
	if childSettled.Update(event.UpdateEvent{ObjectOld: active, ObjectNew: orphan}) {
		t.Fatal("a Run without a parent passed the filter")
	}

	q := workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[mcreconcile.Request]())
	defer q.ShutDown()
	parentOfSettledChild()("tenant-a", nil).Update(context.Background(), event.UpdateEvent{ObjectOld: active, ObjectNew: settled}, q)
	if q.Len() != 1 {
		t.Fatalf("enqueued %d requests, want the parent", q.Len())
	}
	got, _ := q.Get()
	want := mcreconcile.Request{Request: reconcile.Request{NamespacedName: types.NamespacedName{Name: "parent"}}, ClusterName: "tenant-a"}
	if got != want {
		t.Fatalf("enqueued %+v, want %+v", got, want)
	}
}
