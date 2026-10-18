// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package pullrequest

import (
	"context"
	"errors"
	"strings"
	"testing"

	codescheme "github.com/railgrid/provider-code/scheme"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"

	api "github.com/railgrid/provider-code/apis/v1alpha1"
	"github.com/railgrid/provider-code/backend"
	"github.com/railgrid/provider-code/snapshotstore"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

type imageForge struct {
	fakeForge
	calls []string
	fail  string
}

func (f *imageForge) UploadAttachment(_ context.Context, _ *api.Connection, _ backend.Credential, _ *api.Repository, a backend.Attachment) (string, error) {
	f.calls = append(f.calls, a.Path)
	if a.Path == f.fail {
		return "", errors.New("upload failed")
	}
	return "https://github.com/user-attachments/assets/" + a.Digest(), nil
}
func TestAttachmentRetryPersistsUploadsAndRewrites(t *testing.T) {
	store, err := snapshotstore.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	images := []backend.Attachment{{Path: "artifacts/screenshots/a.png", MediaType: "image/png", Data: append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 32)...)}, {Path: "artifacts/screenshots/b.png", MediaType: "image/png", Data: append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 33)...)}}
	ref := stage(t, store, "tenant", backend.Snapshot{Attachments: images})
	pr := &api.PullRequest{ObjectMeta: metav1.ObjectMeta{Name: "pr"}, Spec: api.PullRequestSpec{Body: "before\n![A](artifacts/screenshots/a.png)\n![B](artifacts/screenshots/b.png)\nafter", AttachmentRef: ref}}
	scheme := runtime.NewScheme()
	if err := api.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(pr).WithObjects(pr).Build()
	if err := c.Get(t.Context(), client.ObjectKeyFromObject(pr), pr); err != nil {
		t.Fatal(err)
	}
	r := &Reconciler{Snapshots: store}
	host := &imageForge{fail: images[1].Path}
	repo := &api.Repository{ObjectMeta: metav1.ObjectMeta{UID: "repo-1"}}
	if _, err := r.attachmentBody(t.Context(), c, host, nil, backend.Credential{}, repo, "tenant", pr); err == nil {
		t.Fatal("missing upload failure")
	}
	// Reload as a fresh reconcile would, proving the receipt is durable.
	if err := c.Get(t.Context(), client.ObjectKeyFromObject(pr), pr); err != nil {
		t.Fatal(err)
	}
	if len(pr.Status.UploadedAttachments) != 1 {
		t.Fatal("successful upload not persisted")
	}
	host.fail = ""
	body, err := r.attachmentBody(t.Context(), c, host, nil, backend.Credential{}, repo, "tenant", pr)
	if err != nil {
		t.Fatal(err)
	}
	if len(host.calls) != 3 || host.calls[2] != images[1].Path {
		t.Fatalf("retry re-uploaded completed image: %v", host.calls)
	}
	if strings.Contains(body, "artifacts/screenshots/") || !strings.HasPrefix(body, "before\n![A](https://github.com/") || !strings.HasSuffix(body, "after") {
		t.Fatalf("bad body: %s", body)
	}
	pr.Spec.Body = "![Missing](artifacts/screenshots/missing.png)"
	if _, err := r.attachmentBody(t.Context(), c, host, nil, backend.Credential{}, repo, "tenant", pr); err == nil {
		t.Fatal("missing screenshot accepted")
	}
	old := textDigest(pr)
	pr.Spec.Body = "changed report"
	if textDigest(pr) == old {
		t.Fatal("report-only update did not change digest")
	}
}

// Exercise the reconciler, not just its Markdown helper: create, retry,
// unchanged observation and a new report against the same commit.
func TestReconcilePublishesReportChangesWithoutMovingHead(t *testing.T) {
	store, err := snapshotstore.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	image := backend.Attachment{Path: "artifacts/screenshots/a.png", MediaType: "image/png", Data: append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 32)...)}
	ref := stage(t, store, "tenant", backend.Snapshot{Attachments: []backend.Attachment{image}})
	pr := resource(&api.PullRequestHead{Commit: headA, BaseCommit: headB, Tree: tree, BundleRef: ref})
	pr.Name = "pr"
	pr.Generation = 1
	pr.Status.Head = headA
	pr.Spec.Body = "![A](artifacts/screenshots/a.png)"
	pr.Spec.AttachmentRef = ref
	repo := &api.Repository{ObjectMeta: metav1.ObjectMeta{Name: "product", UID: "repo"}, Spec: api.RepositorySpec{ConnectionRef: "conn"}}
	conn := &api.Connection{ObjectMeta: metav1.ObjectMeta{Name: "conn"}, Spec: api.ConnectionSpec{Type: api.CredentialTypePAT, Provider: api.ProviderGitHub, SecretRef: api.LocalSecretReference{Name: "credential", Namespace: "default", Key: "token"}}}
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "credential", Namespace: "default"}, Data: map[string][]byte{"token": []byte("test-token")}}
	c := fake.NewClientBuilder().WithScheme(codescheme.NewScheme()).WithStatusSubresource(pr).WithObjects(pr, repo, conn, secret).Build()
	host := &reconcileImageForge{}
	registry := backend.NewRegistry()
	if err := registry.Register(host); err != nil {
		t.Fatal(err)
	}
	r := &Reconciler{Manager: imageManager{c: c}, Backends: registry, Snapshots: store}
	req := mcreconcile.Request{ClusterName: "tenant", Request: reconcile.Request{NamespacedName: types.NamespacedName{Name: "pr"}}}
	if _, err := r.Reconcile(t.Context(), req); err != nil {
		t.Fatal(err)
	}
	if host.created != 1 || len(host.calls) != 1 || !strings.Contains(host.body, "https://github.com/user-attachments/") {
		t.Fatalf("not published: %+v", host)
	}
	updates := host.updates
	if _, err := r.Reconcile(t.Context(), req); err != nil {
		t.Fatal(err)
	}
	if host.updates != updates || len(host.calls) != 1 {
		t.Fatal("unchanged reconcile republished")
	}
	if err := c.Get(t.Context(), client.ObjectKey{Name: "pr"}, pr); err != nil {
		t.Fatal(err)
	}
	pr.Spec.Body = "Verified again.\n![A](artifacts/screenshots/a.png)"
	pr.Generation++
	if err := c.Update(t.Context(), pr); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(t.Context(), req); err != nil {
		t.Fatal(err)
	}
	if host.updates != updates+1 || len(host.calls) != 1 || !strings.HasPrefix(host.body, "Verified again.") {
		t.Fatal("same-head report was not applied with cached image")
	}
	if len(host.published) != 0 {
		t.Fatal("report update moved branch")
	}
	// Simulate the first text change on a resource from before this feature.
	if err := c.Get(t.Context(), client.ObjectKey{Name: "pr"}, pr); err != nil {
		t.Fatal(err)
	}
	pr.Spec.Body = "Updated without screenshots."
	pr.Spec.AttachmentRef = ""
	pr.Generation++
	if err := c.Update(t.Context(), pr); err != nil {
		t.Fatal(err)
	}
	pr.Status.PublishedTextDigest = ""
	if err := c.Status().Update(t.Context(), pr); err != nil {
		t.Fatal(err)
	}
	host.failUpdate = true
	if _, err := r.Reconcile(t.Context(), req); err == nil {
		t.Fatal("missing update failure")
	}
	host.failUpdate = false
	before := host.updates
	if _, err := r.Reconcile(t.Context(), req); err != nil {
		t.Fatal(err)
	}
	if host.updates != before+1 || host.body != "Updated without screenshots." {
		t.Fatal("upgrade baseline swallowed failed update")
	}

}

type reconcileImageForge struct {
	imageForge
	backend.GitBackend
	updates    int
	failUpdate bool
	body       string
}

func (f *reconcileImageForge) Name() string { return "github" }
func (f *reconcileImageForge) ReadPullRequest(context.Context, *api.Connection, backend.Credential, *api.Repository, int) (*backend.PullRequest, error) {
	return f.pr, nil
}
func (f *reconcileImageForge) UpdatePullRequest(_ context.Context, _ *api.Connection, _ backend.Credential, _ *api.Repository, _ int, in backend.PullRequestInput) (*backend.PullRequest, error) {
	f.updates++
	if f.failUpdate {
		return nil, errors.New("GitHub unavailable")
	}
	f.body = in.Body
	return f.pr, nil
}

type imageManager struct {
	mcmanager.Manager
	c client.Client
}

func (m imageManager) GetCluster(context.Context, multicluster.ClusterName) (cluster.Cluster, error) {
	return imageCluster{c: m.c}, nil
}

type imageCluster struct {
	cluster.Cluster
	c client.Client
}

func (c imageCluster) GetClient() client.Client { return c.c }
