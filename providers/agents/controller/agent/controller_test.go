// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package agent

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"

	agentsv1alpha1 "github.com/railgrid/provider-agents/apis/v1alpha1"
	agentsscheme "github.com/railgrid/provider-agents/scheme"
)

type fakeManager struct {
	mcmanager.Manager
	c client.Client
}

func (m fakeManager) GetCluster(context.Context, multicluster.ClusterName) (cluster.Cluster, error) {
	return fakeCluster{c: m.c}, nil
}

type fakeCluster struct {
	cluster.Cluster
	c client.Client
}

func (c fakeCluster) GetClient() client.Client { return c.c }

func reconcileAgent(t *testing.T, agent *agentsv1alpha1.Agent) agentsv1alpha1.Agent {
	t.Helper()
	c := fake.NewClientBuilder().
		WithScheme(agentsscheme.NewScheme()).
		WithStatusSubresource(&agentsv1alpha1.Agent{}).
		WithObjects(agent).
		Build()
	r := &Reconciler{Manager: fakeManager{c: c}}
	req := mcreconcile.Request{ClusterName: "tenant-a", Request: reconcile.Request{NamespacedName: types.NamespacedName{Name: agent.Name}}}
	if _, err := r.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	var got agentsv1alpha1.Agent
	if err := c.Get(context.Background(), client.ObjectKey{Name: agent.Name}, &got); err != nil {
		t.Fatal(err)
	}
	return got
}

// An agent with no phase — created before the create handler stamped one, or
// whose stamp failed — reads as Ready after one reconcile.
func TestReconcileStampsReadyOnEmptyPhase(t *testing.T) {
	got := reconcileAgent(t, &agentsv1alpha1.Agent{ObjectMeta: metav1.ObjectMeta{Name: "helper"}})
	if got.Status.Phase != agentsv1alpha1.AgentPhaseReady {
		t.Fatalf("phase = %q, want Ready", got.Status.Phase)
	}
}

// Any other phase is somebody else's decision (budget suspension, a manual
// pause) and must be left alone.
func TestReconcileLeavesNonEmptyPhaseAlone(t *testing.T) {
	a := &agentsv1alpha1.Agent{ObjectMeta: metav1.ObjectMeta{Name: "paused"}}
	a.Status.Phase = "Suspended"
	a.Status.SuspendedReason = "budget exceeded"
	got := reconcileAgent(t, a)
	if got.Status.Phase != "Suspended" || got.Status.SuspendedReason != "budget exceeded" {
		t.Fatalf("status changed: %+v", got.Status)
	}
}

// A missing agent (deleted between the event and the reconcile) is not an error.
func TestReconcileIgnoresMissingAgent(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(agentsscheme.NewScheme()).Build()
	r := &Reconciler{Manager: fakeManager{c: c}}
	req := mcreconcile.Request{ClusterName: "tenant-a", Request: reconcile.Request{NamespacedName: types.NamespacedName{Name: "gone"}}}
	if _, err := r.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("a missing agent must not error: %v", err)
	}
}
