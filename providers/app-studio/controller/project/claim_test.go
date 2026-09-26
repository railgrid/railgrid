/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package project

// Which socket each call goes down.
//
// App Studio's APIExport claims instances, repositories and repositorycommits
// with no identityHash, so kcp serves them into this provider's own virtual
// workspace in every consumer workspace that accepted the claim. Everything
// that is an ordinary Kubernetes verb on those kinds therefore rides the
// manager's client. What stays on the tenant path, as the hub-minted project
// identity, is what a claim cannot grant: a bearer another provider's HTTP
// data plane will accept, and the APIBinding read that addresses it.
//
// These tests hold that line from both sides: the claimed kinds must be
// reachable with NO tenant client at all, and the action call must still
// present the identity.

import (
	"context"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	aiv1alpha1 "github.com/railgrid/provider-app-studio/apis/ai/v1alpha1"
	"github.com/railgrid/provider-app-studio/bindings"
)

// The commit protocol end to end: the RepositoryCommit is created by the Code
// provider behind a VERB App Studio calls as itself through its export
// virtual workspace (no tenant-path client, no project bearer, no APIBinding
// read), while following the commit to settlement is an ordinary Get of a
// claimed kind on the manager's client.
func TestCommitFollowsTheClaimedRepositoryCommitWithoutTheTenantPath(t *testing.T) {
	env := newCommitTestEnv(t, nil, repositoryCommitObject("commit-1", "Running", ""))

	dirty, err := env.commit()
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	if !dirty {
		t.Fatal("first pass settled; the commit is only recorded as pending until its CR says Succeeded")
	}
	if env.commits() != 1 {
		t.Fatalf("verb invocations = %d, want 1 — the commit is asked for as the provider", env.commits())
	}
	if name, ok := env.pendingCommit(); !ok || name != "commit-1" {
		t.Fatalf("pending commit = %q/%v, want commit-1", name, ok)
	}

	// The commit lands on the CLAIMED kind, through the manager's client.
	env.setRepositoryCommit("commit-1", "Succeeded", "feedfacefeedface")
	if dirty, err = env.commit(); err != nil {
		t.Fatalf("settling commit: %v", err)
	}
	if dirty {
		t.Fatal("workspace is still dirty after the commit succeeded")
	}
	if _, ok := env.pendingCommit(); ok {
		t.Fatal("the pending pointer survived a settled commit")
	}
	if len(env.notified) != 1 || env.notified[0].CommitSHA != "feedfacefeedface" {
		t.Fatalf("settlement notifications = %+v, want one for the landed SHA", env.notified)
	}
}

// The teardown regression this whole path is shaped around: an unserved kind
// must never read as "already gone". A Project whose workspace has not
// accepted the claim keeps its finalizer, keeps its live instances, and is
// retried — the failure mode being avoided is releasing the finalizer over
// objects nobody could see.
func TestTeardownKeepsTheFinalizerWhenTheClaimIsUnaccepted(t *testing.T) {
	repositories := schema.GroupKind{Group: repositoryGVK.Group, Kind: repositoryGVK.Kind}
	c := fake.NewClientBuilder().WithScheme(teardownScheme(t)).
		WithInterceptorFuncs(interceptor.Funcs{
			Get: func(context.Context, client.WithWatch, client.ObjectKey, client.Object, ...client.GetOption) error {
				return &meta.NoKindMatchError{GroupKind: repositories, SearchedVersions: []string{repositoryGVK.Version}}
			},
		}).Build()

	err := (&Reconciler{}).settleRepository(context.Background(), c, deletingProject(false))
	if err == nil {
		t.Fatal("settleRepository succeeded against a workspace that does not serve repositories; the finalizer would be released over a live repository")
	}
	if !strings.Contains(err.Error(), "demo-repo") {
		t.Fatalf("settleRepository error = %v, want it to name the repository it could not settle", err)
	}
}

// The same question asked of the helper the teardown and the convergence paths
// both branch on. NotFound is a real answer about an object; a RESTMapper miss
// is an answer about the workspace, and the two must not be confused.
func TestBindingStatusStaysPendingWhenInstancesAreUnserved(t *testing.T) {
	instances := schema.GroupKind{Group: "infrastructure.railgrid.ai", Kind: "Instance"}
	noMatch := &meta.NoKindMatchError{GroupKind: instances, SearchedVersions: []string{"v1alpha1"}}
	c := fake.NewClientBuilder().WithScheme(runtime.NewScheme()).
		WithInterceptorFuncs(interceptor.Funcs{
			Get: func(context.Context, client.WithWatch, client.ObjectKey, client.Object, ...client.GetOption) error {
				return noMatch
			},
		}).Build()

	p := &aiv1alpha1.Project{
		ObjectMeta: metav1.ObjectMeta{Name: "demo", UID: "project-uid"},
		Spec:       aiv1alpha1.ProjectSpec{Template: &aiv1alpha1.ProjectTemplateSpec{Name: "application"}},
	}
	b := binding(projectDevelopmentBindingName)
	b.ResourceRef.Name = "demo-dev"
	b.Values = runtime.RawExtension{Raw: []byte(`{"name":"demo-dev"}`)}
	_, err := (&Reconciler{}).ensureInstance(context.Background(), c, p, b)
	if err == nil {
		t.Fatal("ensureInstance succeeded against a workspace that does not serve instances")
	}
	// The reconcile loop reads this as transient, not as invalid spec: the
	// binding is marked pending and retried rather than failed for good.
	if bindings.IsInvalidBinding(err) {
		t.Fatalf("an unaccepted claim reads as an invalid binding (%v); it would stop being retried", err)
	}
	if got := bindings.StatusFromObject(b, nil); got.Phase == "" {
		t.Fatalf("binding status for an unconverged instance = %+v, want a phase the portal can show", got)
	}
}

// teardownScheme registers the dependency kinds a teardown touches.
func teardownScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	scheme.AddKnownTypeWithName(repositoryGVK, &unstructured.Unstructured{})
	scheme.AddKnownTypeWithName(repositoryGVK.GroupVersion().WithKind("RepositoryList"), &unstructured.UnstructuredList{})
	return scheme
}
