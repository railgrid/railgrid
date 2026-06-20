/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package repositorycommit

import (
	"context"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	codev1alpha1 "github.com/faroshq/provider-code/apis/v1alpha1"
	"github.com/faroshq/provider-code/commitbundle"
	codescheme "github.com/faroshq/provider-code/scheme"
)

func TestBundleArrivalTimedOut(t *testing.T) {
	now := time.Unix(100, 0)
	if bundleArrivalTimedOut(nil, now) {
		t.Fatal("nil start unexpectedly timed out")
	}
	recent := metav1.NewTime(now.Add(-bundleArrivalTimeout + time.Second))
	if bundleArrivalTimedOut(&recent, now) {
		t.Fatal("recent start unexpectedly timed out")
	}
	old := metav1.NewTime(now.Add(-bundleArrivalTimeout))
	if !bundleArrivalTimedOut(&old, now) {
		t.Fatal("old start did not time out")
	}
}

func TestFailAndDeleteBundleRemovesStoredBundle(t *testing.T) {
	ctx := context.Background()
	store, err := commitbundle.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileStore returned error: %v", err)
	}
	ref, err := store.Put(ctx, "logical-cluster", []commitbundle.File{{Path: "index.html", Content: "<h1>demo</h1>"}})
	if err != nil {
		t.Fatalf("Put returned error: %v", err)
	}
	commit := &codev1alpha1.RepositoryCommit{
		ObjectMeta: metav1.ObjectMeta{Name: "demo-commit"},
		Spec: codev1alpha1.RepositoryCommitSpec{
			Source: codev1alpha1.RepositoryCommitSource{
				BundleRef: codev1alpha1.RepositoryCommitBundleReference{Name: ref.Name, Digest: ref.Digest},
			},
		},
	}
	c := fake.NewClientBuilder().
		WithScheme(codescheme.NewScheme()).
		WithStatusSubresource(&codev1alpha1.RepositoryCommit{}).
		WithObjects(commit).
		Build()
	r := &Reconciler{Bundles: store}

	if err := r.failAndDeleteBundle(ctx, c, commit, "missing connection", "logical-cluster", commit.Spec.Source.BundleRef); err != nil {
		t.Fatalf("failAndDeleteBundle returned error: %v", err)
	}
	var got codev1alpha1.RepositoryCommit
	if err := c.Get(ctx, client.ObjectKey{Name: commit.Name}, &got); err != nil {
		t.Fatalf("get RepositoryCommit returned error: %v", err)
	}
	if got.Status.Phase != codev1alpha1.RepositoryCommitPhaseFailed {
		t.Fatalf("phase = %q, want Failed", got.Status.Phase)
	}
	if _, err := store.Get(ctx, "logical-cluster", ref.Name, ref.Digest); err == nil {
		t.Fatal("bundle still exists after failed RepositoryCommit")
	} else if !commitbundle.IsNotFound(err) {
		t.Fatalf("Get returned %v, want not found", err)
	}
}

func TestFailUpdatesCurrentObjectStatus(t *testing.T) {
	ctx := context.Background()
	current := &codev1alpha1.RepositoryCommit{
		ObjectMeta: metav1.ObjectMeta{Name: "demo-commit", ResourceVersion: "2"},
		Status:     codev1alpha1.RepositoryCommitStatus{Phase: codev1alpha1.RepositoryCommitPhaseRunning},
	}
	c := &recordingStatusClient{Client: fake.NewClientBuilder().
		WithScheme(codescheme.NewScheme()).
		WithStatusSubresource(&codev1alpha1.RepositoryCommit{}).
		WithObjects(current).
		Build()}
	stale := current.DeepCopy()
	stale.ResourceVersion = "1"

	if err := (&Reconciler{}).fail(ctx, c, stale, "github failed"); err != nil {
		t.Fatalf("fail returned error: %v", err)
	}
	if c.updated == nil {
		t.Fatal("Status().Update was not called")
	}
	if got := c.updated.GetResourceVersion(); got != "2" {
		t.Fatalf("updated object resourceVersion = %q, want 2", got)
	}
}

func TestUpdateStatusIfChangedUpdatesCurrentObject(t *testing.T) {
	ctx := context.Background()
	current := &codev1alpha1.RepositoryCommit{
		ObjectMeta: metav1.ObjectMeta{Name: "demo-commit", ResourceVersion: "2"},
		Status:     codev1alpha1.RepositoryCommitStatus{Phase: codev1alpha1.RepositoryCommitPhasePending},
	}
	c := &recordingStatusClient{Client: fake.NewClientBuilder().
		WithScheme(codescheme.NewScheme()).
		WithStatusSubresource(&codev1alpha1.RepositoryCommit{}).
		WithObjects(current).
		Build()}
	stale := current.DeepCopy()
	stale.ResourceVersion = "1"
	stale.Status.Phase = codev1alpha1.RepositoryCommitPhaseRunning

	if err := updateStatusIfChanged(ctx, c, stale); err != nil {
		t.Fatalf("updateStatusIfChanged returned error: %v", err)
	}
	if c.updated == nil {
		t.Fatal("Status().Update was not called")
	}
	if got := c.updated.GetResourceVersion(); got != "2" {
		t.Fatalf("updated object resourceVersion = %q, want 2", got)
	}
}

type recordingStatusClient struct {
	client.Client
	updated client.Object
}

func (c *recordingStatusClient) Status() client.SubResourceWriter {
	return recordingStatusWriter{SubResourceWriter: c.Client.Status(), updated: &c.updated}
}

type recordingStatusWriter struct {
	client.SubResourceWriter
	updated *client.Object
}

func (w recordingStatusWriter) Update(ctx context.Context, obj client.Object, opts ...client.SubResourceUpdateOption) error {
	if copied, ok := obj.DeepCopyObject().(client.Object); ok {
		*w.updated = copied
	} else {
		*w.updated = obj
	}
	return w.SubResourceWriter.Update(ctx, obj, opts...)
}
