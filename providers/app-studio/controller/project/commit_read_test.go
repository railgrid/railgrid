/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package project

import (
	"context"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/railgrid/provider-app-studio/workspace"
)

// commitClientWith builds a client that serves RepositoryCommits the way the
// manager's client does once the APIExport's claim on them is accepted.
func commitClientWith(objects ...runtime.Object) *fake.ClientBuilder {
	scheme := runtime.NewScheme()
	scheme.AddKnownTypeWithName(repositoryCommitGVK, &unstructured.Unstructured{})
	scheme.AddKnownTypeWithName(repositoryCommitGVK.GroupVersion().WithKind("RepositoryCommitList"), &unstructured.UnstructuredList{})
	return fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(objects...)
}

// repositorycommits are a CLAIMED kind: the export asks for get, list and
// watch on them with no identityHash, and kcp resolves that per consumer
// workspace. So one Get is the whole read.
//
// It used to be a Get plus a fallback to the composition's unnamed list,
// because the project identity could hold no named grant on a commit that did
// not exist when it was last minted and the Get came back 403. Nothing about
// the claim is conditioned on a name, so that shape is gone — and with it the
// list that a busy workspace made expensive.
func TestReadRepositoryCommitIsOneGetThroughTheManagerClient(t *testing.T) {
	gets, lists := 0, 0
	c := commitClientWith(repositoryCommitObject("commit-1", "Succeeded", "abc123")).
		WithInterceptorFuncs(interceptor.Funcs{
			Get: func(ctx context.Context, wc client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				gets++
				return wc.Get(ctx, key, obj, opts...)
			},
			List: func(ctx context.Context, wc client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
				lists++
				return wc.List(ctx, list, opts...)
			},
		}).Build()

	got, err := readRepositoryCommit(context.Background(), c, "commit-1")
	if err != nil {
		t.Fatalf("readRepositoryCommit: %v", err)
	}
	if sha, _, _ := unstructured.NestedString(got.Object, "status", "commitSHA"); sha != "abc123" {
		t.Fatalf("read commit sha = %q, want abc123", sha)
	}
	if gets != 1 || lists != 0 {
		t.Fatalf("reads = %d Get / %d List, want exactly one Get and no list", gets, lists)
	}

	// A commit the Code provider no longer has still reads as NotFound, which
	// is what lets the caller clear the pending pointer and resend.
	if _, err := readRepositoryCommit(context.Background(), c, "commit-gone"); !apierrors.IsNotFound(err) {
		t.Fatalf("missing commit error = %v, want NotFound so the pending pointer is cleared", err)
	}
}

// A workspace that has not accepted the claim is NOT a workspace whose commit
// vanished. kcp answers an unserved kind with a RESTMapper miss, and reading
// that as "gone" would clear the pending pointer and resend a commit that is
// still in flight — the duplicate-commit shape this loop exists to avoid.
//
// The degradation is the one an unreachable workspace has always had: the
// pointer survives, the error is returned, the controller's backoff retries,
// and the pass settles itself the moment the binding accepts the claim.
func TestResolvePendingCommitKeepsThePointerWhenTheClaimIsUnaccepted(t *testing.T) {
	noMatch := &meta.NoKindMatchError{
		GroupKind:        schema.GroupKind{Group: repositoryCommitGVK.Group, Kind: repositoryCommitGVK.Kind},
		SearchedVersions: []string{repositoryCommitGVK.Version},
	}
	c := commitClientWith().WithInterceptorFuncs(interceptor.Funcs{
		Get: func(context.Context, client.WithWatch, client.ObjectKey, client.Object, ...client.GetOption) error {
			return noMatch
		},
	}).Build()

	// A nil Workspace store is safe precisely because nothing on this path may
	// clear the pending record: reaching clearPendingCommit would panic, which
	// is a sharper assertion than any mock.
	r := &Reconciler{}
	resolved, err := r.resolvePendingCommit(context.Background(), c, nil,
		workspace.Scope{ProjectName: "demo"},
		workspace.PendingCommit{Name: "commit-1", RepositoryRef: "demo-repo"})
	if err == nil {
		t.Fatal("resolvePendingCommit returned no error for a kind the workspace does not serve")
	}
	if resolved {
		t.Fatal("resolvePendingCommit reported the commit resolved; the pending pointer would have been dropped")
	}
	if apierrors.IsNotFound(err) {
		t.Fatalf("an unserved kind reads as NotFound (%v); the caller would clear the pointer and resend", err)
	}
}
