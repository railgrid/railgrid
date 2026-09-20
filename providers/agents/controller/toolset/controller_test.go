// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package toolset

import (
	"context"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/api/meta"
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

const testToolset = "research"

func toolset(families, connections []string) *agentsv1alpha1.Toolset {
	ts := &agentsv1alpha1.Toolset{ObjectMeta: metav1.ObjectMeta{Name: testToolset}}
	ts.Spec.DisplayName = "Research"
	ts.Spec.Families = families
	ts.Spec.Connections = connections
	return ts
}

func conn(name string) *agentsv1alpha1.Connection {
	c := &agentsv1alpha1.Connection{ObjectMeta: metav1.ObjectMeta{Name: name}}
	c.Spec.Type = agentsv1alpha1.ConnectionTypeMCP
	return c
}

// agent returns an Agent linking the named toolsets from the given run class.
func agent(name, class string, toolsets ...string) *agentsv1alpha1.Agent {
	a := &agentsv1alpha1.Agent{ObjectMeta: metav1.ObjectMeta{Name: name}}
	a.Spec.DisplayName = name
	switch class {
	case "interactive":
		a.Spec.Tools.Interactive.Toolsets = toolsets
	case "background":
		a.Spec.Tools.Background.Toolsets = toolsets
	case "both":
		a.Spec.Tools.Interactive.Toolsets = toolsets
		a.Spec.Tools.Background.Toolsets = toolsets
	}
	return a
}

// reconcileToolset reconciles the toolset with the rest of the workspace
// present and returns it as stored.
func reconcileToolset(t *testing.T, ts *agentsv1alpha1.Toolset, rest ...client.Object) agentsv1alpha1.Toolset {
	t.Helper()
	objs := append([]client.Object{ts}, rest...)
	c := fake.NewClientBuilder().
		WithScheme(agentsscheme.NewScheme()).
		WithStatusSubresource(&agentsv1alpha1.Toolset{}).
		WithObjects(objs...).
		Build()
	r := &Reconciler{Manager: fakeManager{c: c}}
	req := mcreconcile.Request{ClusterName: "tenant-a", Request: reconcile.Request{NamespacedName: types.NamespacedName{Name: ts.Name}}}
	if _, err := r.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	var got agentsv1alpha1.Toolset
	if err := c.Get(context.Background(), client.ObjectKey{Name: ts.Name}, &got); err != nil {
		t.Fatal(err)
	}
	return got
}

func validated(t *testing.T, ts agentsv1alpha1.Toolset) metav1.Condition {
	t.Helper()
	c := meta.FindStatusCondition(ts.Status.Conditions, agentsv1alpha1.ConditionValidated)
	if c == nil {
		t.Fatalf("no %s condition: %+v", agentsv1alpha1.ConditionValidated, ts.Status.Conditions)
	}
	return *c
}

// ---- status.usedBy ----------------------------------------------------------

// The field has been on the API and the portal since the kind existed and
// nothing has ever written it: every toolset read "used by 0 agents" however
// many agents linked it.
func TestUsedByCountsLinkingAgents(t *testing.T) {
	got := reconcileToolset(t, toolset(nil, nil),
		agent("alice", "interactive", testToolset),
		agent("bob", "background", testToolset),
		agent("carol", "interactive", "something-else"),
	)
	if got.Status.UsedBy != 2 {
		t.Fatalf("usedBy = %d, want 2", got.Status.UsedBy)
	}
}

// One agent linking the same bundle from both run classes is one user, not
// two: the number answers "who would notice if this went away".
func TestUsedByCountsAnAgentOnce(t *testing.T) {
	got := reconcileToolset(t, toolset(nil, nil), agent("alice", "both", testToolset))
	if got.Status.UsedBy != 1 {
		t.Fatalf("usedBy = %d, want 1", got.Status.UsedBy)
	}
}

func TestUsedByIsZeroWithNoAgents(t *testing.T) {
	got := reconcileToolset(t, toolset(nil, nil), agent("alice", "interactive", "other"))
	if got.Status.UsedBy != 0 {
		t.Fatalf("usedBy = %d, want 0", got.Status.UsedBy)
	}
}

// An agent on its way out is not a user; a toolset held up by a tombstone
// would read as unsafe to delete forever.
func TestUsedByIgnoresDeletingAgents(t *testing.T) {
	a := agent("leaving", "interactive", testToolset)
	a.Finalizers = []string{"test.railgrid.ai/hold"}
	ts := reconcileToolset(t, toolset(nil, nil), a)
	if ts.Status.UsedBy != 1 {
		t.Fatalf("a live agent must count: usedBy = %d", ts.Status.UsedBy)
	}
	// Now delete it (the finalizer keeps the object around) and re-reconcile.
	base := fake.NewClientBuilder().
		WithScheme(agentsscheme.NewScheme()).
		WithStatusSubresource(&agentsv1alpha1.Toolset{}).
		WithObjects(toolset(nil, nil), a).
		Build()
	ctx := context.Background()
	if err := base.Delete(ctx, a); err != nil {
		t.Fatal(err)
	}
	r := &Reconciler{Manager: fakeManager{c: base}}
	req := mcreconcile.Request{ClusterName: "tenant-a", Request: reconcile.Request{NamespacedName: types.NamespacedName{Name: testToolset}}}
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatal(err)
	}
	var got agentsv1alpha1.Toolset
	if err := base.Get(ctx, client.ObjectKey{Name: testToolset}, &got); err != nil {
		t.Fatal(err)
	}
	if got.Status.UsedBy != 0 {
		t.Fatalf("a deleting agent must not count: usedBy = %d", got.Status.UsedBy)
	}
}

// ---- the Validated condition ------------------------------------------------

func TestValidatedTrueOnASoundBundle(t *testing.T) {
	cond := validated(t, reconcileToolset(t, toolset([]string{"core", "web"}, []string{"gh"}), conn("gh")))
	if cond.Status != metav1.ConditionTrue || cond.Reason != agentsv1alpha1.ReasonValidated {
		t.Fatalf("condition = %s/%s (%q), want True/Validated", cond.Status, cond.Reason, cond.Message)
	}
}

// A family nobody recognizes grants nothing; api/toolset.go merges it in and
// then never matches it, so the agent is quietly short a tool.
func TestValidatedRejectsUnknownFamily(t *testing.T) {
	cond := validated(t, reconcileToolset(t, toolset([]string{"core", "wheb"}, nil)))
	if cond.Status != metav1.ConditionFalse || cond.Reason != agentsv1alpha1.ReasonUnknownToolFamily {
		t.Fatalf("condition = %s/%s, want False/UnknownToolFamily", cond.Status, cond.Reason)
	}
	if !strings.Contains(cond.Message, "wheb") {
		t.Fatalf("message must name the offending value: %q", cond.Message)
	}
}

// A bundle is allowed to be families-only, or connections-only, or both. A
// toolset's grant is merged into an agent's, so unlike an agent's own grant it
// has no reason to insist on "core".
func TestValidatedAllowsAFamiliesGrantWithoutCore(t *testing.T) {
	cond := validated(t, reconcileToolset(t, toolset([]string{"web"}, nil)))
	if cond.Status != metav1.ConditionTrue {
		t.Fatalf("condition = %s/%s (%q), want True", cond.Status, cond.Reason, cond.Message)
	}
}

// A connection deleted out from under the bundle is the common case, and the
// one with no other symptom: the tools simply stop appearing.
func TestValidatedRejectsUnknownConnection(t *testing.T) {
	cond := validated(t, reconcileToolset(t, toolset(nil, []string{"ghost"})))
	if cond.Status != metav1.ConditionFalse || cond.Reason != agentsv1alpha1.ReasonUnknownConnectionRef {
		t.Fatalf("condition = %s/%s, want False/UnknownConnectionRef", cond.Status, cond.Reason)
	}
	if !strings.Contains(cond.Message, "ghost") {
		t.Fatalf("message must name the connection: %q", cond.Message)
	}
}

func TestValidatedRejectsEmptyEntries(t *testing.T) {
	if cond := validated(t, reconcileToolset(t, toolset([]string{""}, nil))); cond.Reason != agentsv1alpha1.ReasonInvalidSpec {
		t.Fatalf("empty family: reason = %s, want InvalidSpec", cond.Reason)
	}
	if cond := validated(t, reconcileToolset(t, toolset(nil, []string{"  "}))); cond.Reason != agentsv1alpha1.ReasonInvalidSpec {
		t.Fatalf("empty connection: reason = %s, want InvalidSpec", cond.Reason)
	}
}

// ---- reconcile mechanics ----------------------------------------------------

// A settled toolset must not be rewritten: the condition's LastTransitionTime
// is what a status watch keys on, and rewriting it every pass would wake every
// watcher in the workspace forever.
func TestReconcileIsIdempotent(t *testing.T) {
	c := fake.NewClientBuilder().
		WithScheme(agentsscheme.NewScheme()).
		WithStatusSubresource(&agentsv1alpha1.Toolset{}).
		WithObjects(toolset([]string{"core"}, nil), agent("alice", "interactive", testToolset)).
		Build()
	r := &Reconciler{Manager: fakeManager{c: c}}
	req := mcreconcile.Request{ClusterName: "tenant-a", Request: reconcile.Request{NamespacedName: types.NamespacedName{Name: testToolset}}}
	ctx := context.Background()
	for i := range 2 {
		if _, err := r.Reconcile(ctx, req); err != nil {
			t.Fatalf("reconcile %d: %v", i, err)
		}
	}
	var first agentsv1alpha1.Toolset
	if err := c.Get(ctx, client.ObjectKey{Name: testToolset}, &first); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatal(err)
	}
	var second agentsv1alpha1.Toolset
	if err := c.Get(ctx, client.ObjectKey{Name: testToolset}, &second); err != nil {
		t.Fatal(err)
	}
	if second.ResourceVersion != first.ResourceVersion {
		t.Fatalf("a settled toolset was written again (rv %s -> %s)", first.ResourceVersion, second.ResourceVersion)
	}
}

// A toolset deleted between the event and the reconcile is not an error.
func TestReconcileIgnoresMissingToolset(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(agentsscheme.NewScheme()).Build()
	r := &Reconciler{Manager: fakeManager{c: c}}
	req := mcreconcile.Request{ClusterName: "tenant-a", Request: reconcile.Request{NamespacedName: types.NamespacedName{Name: "gone"}}}
	if _, err := r.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("a missing toolset must not error: %v", err)
	}
}
