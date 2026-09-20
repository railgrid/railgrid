// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package actions

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	api "github.com/railgrid/provider-code/apis/v1alpha1"
	"github.com/railgrid/provider-code/backend"
	"github.com/railgrid/provider-code/commitbundle"
	"github.com/railgrid/provider-sdk/actionwire"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	ktesting "k8s.io/client-go/testing"
)

// commitFixture wires the two clients an action sees — the caller's, which
// both gates run through, and the provider's own export client, which writes
// the RepositoryCommit — over a real on-disk bundle store.
type commitFixture struct {
	server   *Server
	provider *dynamicfake.FakeDynamicClient
	bundles  *commitbundle.FileStore
	granted  map[string]bool
	checked  map[string]string
}

func newCommitFixture(t *testing.T) *commitFixture {
	t.Helper()
	repo := &api.Repository{
		TypeMeta:   metav1.TypeMeta{APIVersion: "code.railgrid.ai/v1alpha1", Kind: "Repository"},
		ObjectMeta: metav1.ObjectMeta{Name: "product", UID: "repo-uid"},
		Spec:       api.RepositorySpec{ConnectionRef: "git", Name: "product"},
		Status:     api.RepositoryStatus{RepoID: "123"},
	}
	listKinds := map[schema.GroupVersionResource]string{
		{Group: "code.railgrid.ai", Version: "v1alpha1", Resource: "repositories"}:      "RepositoryList",
		{Group: "code.railgrid.ai", Version: "v1alpha1", Resource: "repositorycommits"}: "RepositoryCommitList",
	}
	f := &commitFixture{
		granted: map[string]bool{"create": true},
		checked: map[string]string{},
	}
	caller := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), listKinds, actionObject(t, repo))
	caller.PrependReactor("create", "selfsubjectaccessreviews", func(action ktesting.Action) (bool, runtime.Object, error) {
		object := action.(ktesting.CreateAction).GetObject().(*unstructured.Unstructured)
		attrs, _, _ := unstructured.NestedMap(object.Object, "spec", "resourceAttributes")
		verb, _ := attrs["verb"].(string)
		subresource, _ := attrs["subresource"].(string)
		f.checked[verb] = subresource
		return true, &unstructured.Unstructured{Object: map[string]any{"status": map[string]any{"allowed": f.granted[verb]}}}, nil
	})

	f.provider = dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), listKinds, actionObject(t, repo))
	// kcp stamps the logical cluster on everything it serves; the executor
	// refuses to leave a commit behind whose bundle it cannot scope.
	f.provider.PrependReactor("create", "repositorycommits", func(action ktesting.Action) (bool, runtime.Object, error) {
		object := action.(ktesting.CreateAction).GetObject().(*unstructured.Unstructured)
		annotations := object.GetAnnotations()
		if annotations == nil {
			annotations = map[string]string{}
		}
		annotations["kcp.io/cluster"] = "tenant-id"
		object.SetAnnotations(annotations)
		object.SetUID("commit-uid")
		return false, nil, nil
	})

	store, err := commitbundle.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f.bundles = store
	f.server = New(callerFixture{client: caller, t: t}, func(_ context.Context, _ string, _ schema.GroupVersionResource, _ string) (dynamic.Interface, error) {
		return f.provider, nil
	}, backend.NewRegistry())
	f.server.Bundles = store
	return f
}

func (f *commitFixture) invoke(t *testing.T, verb string, input map[string]any) (*httptest.ResponseRecorder, actionwire.Envelope) {
	t.Helper()
	body, err := json.Marshal(map[string]any{"input": input})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/actions/clusters/tenant-id/repositories/product/"+verb+"/v1", bytes.NewReader(body))
	request.Header.Set("X-Railgrid-Cluster", "tenant-id")
	request.Header.Set("Authorization", "Bearer caller-token")
	response := httptest.NewRecorder()
	f.server.ServeHTTP(response, request)
	var envelope actionwire.Envelope
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("response is not an action envelope: %s", response.Body.String())
	}
	return response, envelope
}

// The whole point of the action: the two gates pass, the bundle lands in the
// provider's own store, a RepositoryCommit pointing at it is created, and the
// caller is told which object to watch — never the file contents back.
func TestCommitActionCreatesRepositoryCommit(t *testing.T) {
	f := newCommitFixture(t)
	response, envelope := f.invoke(t, Commit, map[string]any{
		"repositoryUID": "repo-uid",
		"message":       "Initial app",
		"files": []any{
			map[string]any{"path": "README.md", "content": "hello"},
			map[string]any{"path": "logo.png", "content": base64.StdEncoding.EncodeToString([]byte{0x00, 0x01}), "encoding": "base64"},
			map[string]any{"path": "old.txt", "delete": true},
		},
	})
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", response.Code, response.Body.String())
	}
	if f.checked["create"] != Commit {
		t.Fatalf("gate 2 asked about repositories/%q, want repositories/%s", f.checked["create"], Commit)
	}
	var result commitOutput
	if err := json.Unmarshal(envelope.Result, &result); err != nil {
		t.Fatal(err)
	}
	if result.Commit.Name == "" || result.Commit.UID != "commit-uid" {
		t.Fatalf("result does not name the commit to watch: %+v", result)
	}

	created, err := f.provider.Resource(schema.GroupVersionResource{Group: "code.railgrid.ai", Version: "v1alpha1", Resource: "repositorycommits"}).
		Get(context.Background(), result.Commit.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ref, _, _ := unstructured.NestedString(created.Object, "spec", "source", "bundleRef", "name")
	digest, _, _ := unstructured.NestedString(created.Object, "spec", "source", "bundleRef", "digest")
	if ref == "" || digest == "" {
		t.Fatalf("RepositoryCommit carries no bundle pointer: %#v", created.Object)
	}
	if _, found, _ := unstructured.NestedFieldNoCopy(created.Object, "spec", "files"); found {
		t.Fatal("file contents leaked into the RepositoryCommit spec")
	}
	bundle, err := f.bundles.Get(context.Background(), "tenant-id", ref, digest)
	if err != nil {
		t.Fatalf("bundle is not readable under the commit's cluster scope: %v", err)
	}
	if len(bundle.Files) != 3 {
		t.Fatalf("bundle carries %d files, want 3", len(bundle.Files))
	}
}

// A body too large to declare goes through stage_commit_bundle, and the
// handle it returns is the one thing commit needs afterwards.
func TestStagedBundleCommits(t *testing.T) {
	f := newCommitFixture(t)
	_, envelope := f.invoke(t, StageCommitBundle, map[string]any{
		"repositoryUID": "repo-uid",
		"files":         []any{map[string]any{"path": "app/main.go", "content": "package main"}},
	})
	var staged stageCommitBundleOutput
	if err := json.Unmarshal(envelope.Result, &staged); err != nil {
		t.Fatal(err)
	}
	if staged.BundleRef == "" || staged.BundleDigest == "" || staged.FileCount != 1 {
		t.Fatalf("staging returned no usable handle: %+v", staged)
	}
	if f.checked["create"] != StageCommitBundle {
		t.Fatalf("staging was gated on repositories/%q", f.checked["create"])
	}

	response, envelope := f.invoke(t, Commit, map[string]any{
		"repositoryUID": "repo-uid",
		"message":       "Staged app",
		"bundleRef":     staged.BundleRef,
		"bundleDigest":  staged.BundleDigest,
	})
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", response.Code, response.Body.String())
	}
	var result commitOutput
	if err := json.Unmarshal(envelope.Result, &result); err != nil {
		t.Fatal(err)
	}
	if result.Commit.Name == "" {
		t.Fatalf("staged commit was not created: %+v", result)
	}
}

// A handle nobody staged, a UID that does not match what gate 1 returned, and
// a body naming both sources are all caller bugs, and none of them leaves a
// RepositoryCommit behind.
func TestCommitActionRefusals(t *testing.T) {
	for name, input := range map[string]map[string]any{
		"unknown bundle": {"repositoryUID": "repo-uid", "bundleRef": "bundle-does-not-exist"},
		"wrong repository UID": {
			"repositoryUID": "someone-elses-repo",
			"files":         []any{map[string]any{"path": "a.txt", "content": "a"}},
		},
		"two sources": {
			"repositoryUID": "repo-uid",
			"bundleRef":     "bundle-1",
			"files":         []any{map[string]any{"path": "a.txt", "content": "a"}},
		},
		"no source": {"repositoryUID": "repo-uid"},
	} {
		t.Run(name, func(t *testing.T) {
			f := newCommitFixture(t)
			response, envelope := f.invoke(t, Commit, input)
			if response.Code/100 == 2 {
				t.Fatalf("status = %d, want a refusal", response.Code)
			}
			if envelope.Error == nil || envelope.Error.Code == "" {
				t.Fatalf("refusal carries no typed code: %s", response.Body.String())
			}
			list, err := f.provider.Resource(schema.GroupVersionResource{Group: "code.railgrid.ai", Version: "v1alpha1", Resource: "repositorycommits"}).
				List(context.Background(), metav1.ListOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if len(list.Items) != 0 {
				t.Fatalf("a refused commit left %d RepositoryCommit(s) behind", len(list.Items))
			}
		})
	}
}

// Gate 2 is per action: a grant to commit is not a grant to stage, and a
// caller with neither gets nothing.
func TestCommitActionDeniedWithoutGrant(t *testing.T) {
	f := newCommitFixture(t)
	f.granted = map[string]bool{}
	response, _ := f.invoke(t, Commit, map[string]any{
		"repositoryUID": "repo-uid",
		"files":         []any{map[string]any{"path": "a.txt", "content": "a"}},
	})
	if response.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", response.Code)
	}
}
