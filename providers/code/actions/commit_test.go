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

	"github.com/railgrid/provider-code/backend"
	"github.com/railgrid/provider-code/commitbundle"
	"github.com/railgrid/provider-sdk/actionwire"
	"github.com/railgrid/provider-sdk/dataplane/conformance"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	ktesting "k8s.io/client-go/testing"
)

// commitFixture wires the one client an action acts through — the provider's
// own, through its export virtual workspace, which the gate reads with and the
// commit verbs write the RepositoryCommit with — over a real on-disk bundle
// store. visible says whether the stamped caller may see the Repository.
type commitFixture struct {
	server   *Server
	callers  *conformance.FakeCallers
	provider *dynamicfake.FakeDynamicClient
	bundles  *commitbundle.FileStore
	visible  bool
}

func newCommitFixture(t *testing.T) *commitFixture {
	t.Helper()
	f := &commitFixture{visible: true}
	f.callers = newCallers(func(a conformance.Attributes) bool {
		return f.visible && allowGet("repositories", "product")(a)
	}, actionObject(t, testRepository()))
	f.provider = providerClient(t, f.callers)
	// kcp stamps the logical cluster on everything it serves; the executor
	// refuses to leave a commit behind whose bundle it cannot scope.
	f.provider.PrependReactor("create", "repositorycommits", func(action ktesting.Action) (bool, runtime.Object, error) {
		object := action.(ktesting.CreateAction).GetObject().(*unstructured.Unstructured)
		annotations := object.GetAnnotations()
		if annotations == nil {
			annotations = map[string]string{}
		}
		annotations["kcp.io/cluster"] = testCluster
		object.SetAnnotations(annotations)
		object.SetUID("commit-uid")
		return false, nil, nil
	})

	store, err := commitbundle.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f.bundles = store
	f.server = New(f.callers, backend.NewRegistry())
	f.server.Bundles = store
	return f
}

func (f *commitFixture) invoke(t *testing.T, verb string, input map[string]any) (*httptest.ResponseRecorder, actionwire.Envelope) {
	t.Helper()
	body, err := json.Marshal(map[string]any{"input": input})
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	f.server.ServeHTTP(response, actionRequest(http.MethodPost, kubePath(testCluster, "repositories", "product", verb), bytes.NewReader(body), testUser))
	var envelope actionwire.Envelope
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("response is not an action envelope: %s", response.Body.String())
	}
	return response, envelope
}

var repositoryCommits = schema.GroupVersionResource{Group: "code.railgrid.ai", Version: "v1alpha1", Resource: "repositorycommits"}

// The whole point of the action: the gate passes, the bundle lands in the
// provider's own store, a RepositoryCommit pointing at it is created AS THE
// PROVIDER, and the caller is told which object to watch — never the file
// contents back.
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
	var result commitOutput
	if err := json.Unmarshal(envelope.Result, &result); err != nil {
		t.Fatal(err)
	}
	if result.Commit.Name == "" || result.Commit.UID != "commit-uid" {
		t.Fatalf("result does not name the commit to watch: %+v", result)
	}

	created, err := f.provider.Resource(repositoryCommits).Get(context.Background(), result.Commit.Name, metav1.GetOptions{})
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
	bundle, err := f.bundles.Get(context.Background(), testCluster, ref, digest)
	if err != nil {
		t.Fatalf("bundle is not readable under the commit's cluster scope: %v", err)
	}
	if len(bundle.Files) != 3 {
		t.Fatalf("bundle carries %d files, want 3", len(bundle.Files))
	}
}

// A body too large to declare goes through stage-commit-bundle, and the
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

// A handle nobody staged, a UID that does not match what the gate returned,
// and a body naming both sources are all caller bugs, and none of them leaves
// a RepositoryCommit behind.
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
			list, err := f.provider.Resource(repositoryCommits).List(context.Background(), metav1.ListOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if len(list.Items) != 0 {
				t.Fatalf("a refused commit left %d RepositoryCommit(s) behind", len(list.Items))
			}
		})
	}
}

// The gate is visibility: a caller who cannot see the Repository gets the
// contract's non-disclosing 404, and nothing is written.
func TestCommitActionDeniedWithoutVisibility(t *testing.T) {
	f := newCommitFixture(t)
	f.visible = false
	response, _ := f.invoke(t, Commit, map[string]any{
		"repositoryUID": "repo-uid",
		"files":         []any{map[string]any{"path": "a.txt", "content": "a"}},
	})
	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", response.Code)
	}
	list, err := f.provider.Resource(repositoryCommits).List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 0 {
		t.Fatalf("a denied commit left %d RepositoryCommit(s) behind", len(list.Items))
	}
}
