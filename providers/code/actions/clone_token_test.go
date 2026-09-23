// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package actions

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	api "github.com/railgrid/provider-code/apis/v1alpha1"
	"github.com/railgrid/provider-code/backend"
	"github.com/railgrid/provider-sdk/dataplane"
	"github.com/railgrid/provider-sdk/dataplane/conformance"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

// A grant is per (resource, action), and mint_clone_token is bound to a
// Repository: a caller who may clone one repository has been told nothing
// about any other repository the same Connection reaches, and a caller who may
// read branches may not take a credential away with it.
func TestCloneTokenIsGrantedPerRepositoryAction(t *testing.T) {
	const cluster = "tenant-id"
	repo := &api.Repository{
		TypeMeta:   metav1.TypeMeta{APIVersion: "code.railgrid.ai/v1alpha1", Kind: "Repository"},
		ObjectMeta: metav1.ObjectMeta{Name: "product", UID: "repo-uid"},
		Spec:       api.RepositorySpec{ConnectionRef: "git", Name: "product"},
		Status:     api.RepositoryStatus{RepoID: "123", CloneURL: "https://github.com/example/product.git"},
	}
	conn := &api.Connection{
		TypeMeta:   metav1.TypeMeta{APIVersion: "code.railgrid.ai/v1alpha1", Kind: "Connection"},
		ObjectMeta: metav1.ObjectMeta{Name: "git", UID: "conn-uid"},
		Spec:       api.ConnectionSpec{Provider: api.ProviderGitHub, Type: api.CredentialTypePAT, Owner: "example", SecretRef: api.LocalSecretReference{Name: "git-key"}},
	}
	secret := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "Secret",
		"metadata": map[string]any{"name": "git-key", "namespace": "default"},
		"data":     map[string]any{"token": base64.StdEncoding.EncodeToString([]byte("provider-secret"))},
	}}

	newServer := func(allow func(conformance.Attributes) bool) (*Server, *conformance.FakeCallers, *dynamicfake.FakeDynamicClient) {
		provider := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), actionObject(t, repo), actionObject(t, conn), secret)
		callers := &conformance.FakeCallers{
			Cluster:   cluster,
			Token:     "caller-token",
			Objects:   []*unstructured.Unstructured{actionObject(t, repo)},
			ListKinds: map[schema.GroupVersionResource]string{repositories: "RepositoryList"},
			Allow:     allow,
		}
		server := New(callers, func(context.Context, string, schema.GroupVersionResource, string) (dynamic.Interface, error) {
			return provider, nil
		}, backend.NewRegistry())
		return server, callers, provider
	}

	path := "/actions/clusters/" + cluster + "/repositories/product/" + MintCloneToken + "/v1"
	body := `{"input":{"repositoryUID":"repo-uid","connectionUID":"conn-uid"}}`
	invoke := func(server *Server) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		request.Header.Set("Authorization", "Bearer caller-token")
		request.Header.Set(dataplane.HeaderCluster, cluster)
		recorder := httptest.NewRecorder()
		server.ServeHTTP(recorder, request)
		return recorder
	}

	// A grant on another repository action does not reach this one.
	readOnly, _, provider := newServer(func(a conformance.Attributes) bool {
		return a.Verb == dataplane.SSARVerb && a.Resource == "repositories" && a.Subresource == "branches"
	})
	if recorder := invoke(readOnly); recorder.Code == http.StatusOK {
		t.Fatalf("a branches grant minted a clone token: %s", recorder.Body.String())
	}
	for _, action := range provider.Actions() {
		if action.GetResource().Resource == "secrets" {
			t.Fatal("a denied caller reached the credential Secret")
		}
	}

	// The matching grant does, and what comes back is a clone credential.
	granted, callers, _ := newServer(func(a conformance.Attributes) bool {
		return a.Verb == dataplane.SSARVerb && a.Resource == "repositories" && a.Subresource == MintCloneToken
	})
	recorder := invoke(granted)
	if recorder.Code != http.StatusOK {
		t.Fatalf("granted mint: got %d, body %s", recorder.Code, recorder.Body.String())
	}
	var envelope struct {
		Result CloneTokenOutput `json:"result"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode envelope: %v (body %s)", err, recorder.Body.String())
	}
	if envelope.Result.RemoteURL != "https://github.com/example/product.git" || strings.Contains(envelope.Result.RemoteURL, "@") {
		t.Fatalf("clone remote = %q", envelope.Result.RemoteURL)
	}
	if envelope.Result.Username != "x-access-token" || envelope.Result.Token != "provider-secret" {
		t.Fatalf("clone credential = %#v", envelope.Result)
	}
	// A PAT cannot be narrowed by any GitHub API, so the action says so rather
	// than implying a read-only token it did not issue.
	if envelope.Result.Scoped {
		t.Fatal("a PAT-backed credential was reported as scoped")
	}
	// The credential is the provider's to read, never the caller's: the whole
	// reason it sits behind an action is that the consumer must not hold it.
	callerClient, err := callers.For(cluster, callers.Token)
	if err != nil {
		t.Fatal(err)
	}
	for _, action := range callerClient.(*dynamicfake.FakeDynamicClient).Actions() {
		if action.GetResource().Resource == "secrets" {
			t.Fatal("the caller's own identity read the credential Secret")
		}
	}
}

// The identity fences are the same ones every repository action has: what gate
// 1 returned must still be what the provider reads with its own identity.
func TestCloneTokenPinsRepositoryAndConnectionIdentity(t *testing.T) {
	const cluster = "tenant-id"
	for _, test := range []struct {
		name  string
		input string
	}{
		{"repository replaced", `{"repositoryUID":"other-repo","connectionUID":"conn-uid"}`},
		{"connection replaced", `{"repositoryUID":"repo-uid","connectionUID":"other-conn"}`},
		{"unpinned", `{"repositoryUID":"repo-uid"}`},
		{"different upstream repository", `{"repositoryUID":"repo-uid","connectionUID":"conn-uid","repository":"example/other"}`},
		{"unknown member", `{"repositoryUID":"repo-uid","connectionUID":"conn-uid","branch":"main"}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			repo := &api.Repository{
				TypeMeta:   metav1.TypeMeta{APIVersion: "code.railgrid.ai/v1alpha1", Kind: "Repository"},
				ObjectMeta: metav1.ObjectMeta{Name: "product", UID: "repo-uid"},
				Spec:       api.RepositorySpec{ConnectionRef: "git", Name: "product"},
				Status:     api.RepositoryStatus{RepoID: "123", CloneURL: "https://github.com/example/product.git"},
			}
			conn := &api.Connection{
				TypeMeta:   metav1.TypeMeta{APIVersion: "code.railgrid.ai/v1alpha1", Kind: "Connection"},
				ObjectMeta: metav1.ObjectMeta{Name: "git", UID: "conn-uid"},
				Spec:       api.ConnectionSpec{Provider: api.ProviderGitHub, Type: api.CredentialTypePAT, Owner: "example", SecretRef: api.LocalSecretReference{Name: "git-key"}},
			}
			secret := &unstructured.Unstructured{Object: map[string]any{
				"apiVersion": "v1", "kind": "Secret",
				"metadata": map[string]any{"name": "git-key", "namespace": "default"},
				"data":     map[string]any{"token": base64.StdEncoding.EncodeToString([]byte("provider-secret"))},
			}}
			provider := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), actionObject(t, repo), actionObject(t, conn), secret)
			callers := &conformance.FakeCallers{
				Cluster:   cluster,
				Token:     "caller-token",
				Objects:   []*unstructured.Unstructured{actionObject(t, repo)},
				ListKinds: map[schema.GroupVersionResource]string{repositories: "RepositoryList"},
				Allow: func(a conformance.Attributes) bool {
					return a.Verb == dataplane.SSARVerb && a.Resource == "repositories" && a.Subresource == MintCloneToken
				},
			}
			server := New(callers, func(context.Context, string, schema.GroupVersionResource, string) (dynamic.Interface, error) {
				return provider, nil
			}, backend.NewRegistry())

			request := httptest.NewRequest(http.MethodPost, "/actions/clusters/"+cluster+"/repositories/product/"+MintCloneToken+"/v1", strings.NewReader(`{"input":`+test.input+`}`))
			request.Header.Set("Authorization", "Bearer caller-token")
			request.Header.Set(dataplane.HeaderCluster, cluster)
			recorder := httptest.NewRecorder()
			server.ServeHTTP(recorder, request)
			if recorder.Code == http.StatusOK {
				t.Fatalf("%s produced a credential: %s", test.name, recorder.Body.String())
			}
		})
	}
}
