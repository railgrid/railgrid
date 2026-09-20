// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package savedview

import (
	"context"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrlfake "sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"

	kueryv1alpha1 "github.com/railgrid/provider-kuery/apis/v1alpha1"
)

const testCluster = "1ngen6o0so3jwz2h"

func fixture(t *testing.T, views ...*kueryv1alpha1.SavedView) (*Reconciler, client.Client) {
	t.Helper()
	scheme := runtime.NewScheme()
	utilruntime.Must(kueryv1alpha1.AddToScheme(scheme))
	builder := ctrlfake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&kueryv1alpha1.SavedView{})
	for _, view := range views {
		builder = builder.WithObjects(view)
	}
	cl := builder.Build()
	return &Reconciler{clusterClient: func(context.Context, multicluster.ClusterName) (client.Client, error) {
		return cl, nil
	}}, cl
}

func request(name string) mcreconcile.Request {
	return mcreconcile.Request{
		ClusterName: multicluster.ClusterName(testCluster),
		Request:     reconcile.Request{NamespacedName: client.ObjectKey{Name: name}},
	}
}

func viewWith(name, query string) *kueryv1alpha1.SavedView {
	view := &kueryv1alpha1.SavedView{
		ObjectMeta: metav1.ObjectMeta{Name: name, Generation: 1},
		Spec:       kueryv1alpha1.SavedViewSpec{DisplayName: name},
	}
	if query != "" {
		view.Spec.Query = runtime.RawExtension{Raw: []byte(query)}
	}
	return view
}

func readyCondition(t *testing.T, cl client.Client, name string) *metav1.Condition {
	t.Helper()
	var got kueryv1alpha1.SavedView
	if err := cl.Get(context.Background(), client.ObjectKey{Name: name}, &got); err != nil {
		t.Fatalf("reading back %s: %v", name, err)
	}
	if got.Status.ObservedGeneration != got.Generation {
		t.Fatalf("observedGeneration = %d, want %d", got.Status.ObservedGeneration, got.Generation)
	}
	condition := meta.FindStatusCondition(got.Status.Conditions, kueryv1alpha1.ConditionReady)
	if condition == nil {
		t.Fatalf("%s has no Ready condition", name)
	}
	return condition
}

// A view whose query the engine will accept is Ready. This is the whole job:
// the CRD cannot express the QuerySpec shape (it is recursive), so the
// reconciler is where a tenant finds out whether their view will run — at save
// time, not at run time.
func TestValidQueryIsReady(t *testing.T) {
	r, cl := fixture(t, viewWith("fleet", `{"limit":10,"filter":{"objects":[{"namespace":"default"}]}}`))
	if _, err := r.Reconcile(context.Background(), request("fleet")); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	condition := readyCondition(t, cl, "fleet")
	if condition.Status != metav1.ConditionTrue || condition.Reason != kueryv1alpha1.ReasonQueryValid {
		t.Fatalf("condition = %+v, want True/QueryValid", condition)
	}
}

// A view with no query at all means "the whole fleet" — the engine's own
// default — and is a legitimate saved view.
func TestEmptyQueryIsReady(t *testing.T) {
	r, cl := fixture(t, viewWith("everything", ""))
	if _, err := r.Reconcile(context.Background(), request("everything")); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got := readyCondition(t, cl, "everything"); got.Status != metav1.ConditionTrue {
		t.Fatalf("condition = %+v, want True", got)
	}
}

// An invalid query is NotReady with a message that names the member, because
// that message is the only thing the tenant sees.
func TestInvalidQueryIsNotReadyAndSaysWhy(t *testing.T) {
	r, cl := fixture(t, viewWith("typo", `{"filter":{"object":[]}}`))
	if _, err := r.Reconcile(context.Background(), request("typo")); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	condition := readyCondition(t, cl, "typo")
	if condition.Status != metav1.ConditionFalse || condition.Reason != kueryv1alpha1.ReasonQueryInvalid {
		t.Fatalf("condition = %+v, want False/QueryInvalid", condition)
	}
	if !strings.Contains(condition.Message, "query.filter.object") {
		t.Fatalf("message %q does not name the offending member", condition.Message)
	}
}

// A deleted view is not an error: nothing of the provider's outlives it.
func TestMissingViewIsNotAnError(t *testing.T) {
	r, _ := fixture(t)
	if _, err := r.Reconcile(context.Background(), request("gone")); err != nil {
		t.Fatalf("Reconcile of a deleted view: %v", err)
	}
}

// Reconciling an unchanged view writes nothing, so a watch does not feed
// itself.
func TestUnchangedViewIsNotRewritten(t *testing.T) {
	r, cl := fixture(t, viewWith("stable", `{"limit":1}`))
	if _, err := r.Reconcile(context.Background(), request("stable")); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	var first kueryv1alpha1.SavedView
	if err := cl.Get(context.Background(), client.ObjectKey{Name: "stable"}, &first); err != nil {
		t.Fatal(err)
	}

	if _, err := r.Reconcile(context.Background(), request("stable")); err != nil {
		t.Fatalf("second Reconcile: %v", err)
	}
	var second kueryv1alpha1.SavedView
	if err := cl.Get(context.Background(), client.ObjectKey{Name: "stable"}, &second); err != nil {
		t.Fatal(err)
	}
	if first.ResourceVersion != second.ResourceVersion {
		t.Fatalf("an unchanged view was rewritten (%s → %s)", first.ResourceVersion, second.ResourceVersion)
	}
}
