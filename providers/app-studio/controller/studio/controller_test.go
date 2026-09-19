/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package studio

import (
	"context"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/controller-runtime/pkg/event"

	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"
)

// Instances arrive on the manager's own informer now, so what matters is that
// the Studio claims only the ones it stamped: a project's instance and a
// tenant's own must wake nothing here.
func TestStudiosForInstanceUsesStudioLabel(t *testing.T) {
	handler := studiosForInstance()("cluster-a", nil)
	owners := func(obj *unstructured.Unstructured) []string {
		queue := &recordingQueue{}
		handler.Create(context.Background(), event.CreateEvent{Object: obj}, queue)
		return queue.names
	}
	owned := &unstructured.Unstructured{Object: map[string]any{"metadata": map[string]any{
		"name":   SearchInstanceName,
		"labels": map[string]any{templateLabel: searchTemplate, studioLabel: "default"},
	}}}
	if got := owners(owned); len(got) != 1 || got[0] != "default" {
		t.Fatalf("owned instance → %v, want [default]", got)
	}
	foreign := &unstructured.Unstructured{Object: map[string]any{"metadata": map[string]any{"name": "shop-dev"}}}
	if got := owners(foreign); len(got) != 0 {
		t.Fatalf("project instance → %v, want none", got)
	}
}

// recordingQueue captures what a handler enqueues.
type recordingQueue struct {
	workqueue.TypedRateLimitingInterface[mcreconcile.Request]
	names []string
}

func (q *recordingQueue) Add(item mcreconcile.Request) { q.names = append(q.names, item.Name) }
