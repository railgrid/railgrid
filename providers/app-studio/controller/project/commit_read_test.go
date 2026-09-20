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
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

// The project identity holds the composition's unnamed list/watch on
// repositorycommits, never a named get on a commit that did not exist when the
// identity was minted. A forbidden Get must therefore fall back to the list,
// and a commit missing from that list must still read as NotFound.
func TestReadRepositoryCommitFallsBackToListWhenGetIsForbidden(t *testing.T) {
	scheme := runtime.NewScheme()
	scheme.AddKnownTypeWithName(repositoryCommitGVK, &unstructured.Unstructured{})
	scheme.AddKnownTypeWithName(repositoryCommitGVK.GroupVersion().WithKind("RepositoryCommitList"), &unstructured.UnstructuredList{})
	gr := schema.GroupResource{Group: repositoryCommitGVK.Group, Resource: "repositorycommits"}
	gets := 0
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithRuntimeObjects(repositoryCommitObject("commit-1", "Succeeded", "abc123")).
		WithInterceptorFuncs(interceptor.Funcs{
			Get: func(context.Context, client.WithWatch, client.ObjectKey, client.Object, ...client.GetOption) error {
				gets++
				return apierrors.NewForbidden(gr, "commit-1", nil)
			},
		}).Build()

	got, err := readRepositoryCommit(context.Background(), c, "commit-1")
	if err != nil {
		t.Fatalf("readRepositoryCommit: %v", err)
	}
	if sha, _, _ := unstructured.NestedString(got.Object, "status", "commitSHA"); sha != "abc123" {
		t.Fatalf("read commit sha = %q, want the listed object", sha)
	}
	if gets != 1 {
		t.Fatalf("Get attempts = %d, want exactly one before the fallback", gets)
	}

	_, err = readRepositoryCommit(context.Background(), c, "commit-gone")
	if !apierrors.IsNotFound(err) {
		t.Fatalf("missing commit error = %v, want NotFound so the pending pointer is cleared", err)
	}
}
