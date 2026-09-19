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
	"github.com/railgrid/provider-sdk/actionwire"
	"github.com/railgrid/provider-sdk/dataplane"
	"github.com/railgrid/provider-sdk/dataplane/conformance"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	ktesting "k8s.io/client-go/testing"
)

type callerFixture struct {
	client dynamic.Interface
	t      *testing.T
}

func (f callerFixture) For(cluster, token string) (dynamic.Interface, error) {
	if cluster != "tenant-id" || token != "caller-token" {
		f.t.Fatal("caller identity lost")
	}
	return f.client, nil
}

type backendFixture struct {
	backend.GitBackend
	backend.Collaboration
	calls int
	t     *testing.T
}

func (f *backendFixture) Name() string { return "github" }
func (f *backendFixture) BranchHead(_ context.Context, conn *api.Connection, cred backend.Credential, repo *api.Repository, branch string) (string, error) {
	f.calls++
	if conn.Name != "git" || cred.Token != "provider-secret" || repo.Name != "product" || branch != "main" {
		f.t.Fatal("backend received wrong binding")
	}
	return "1111111111111111111111111111111111111111", nil
}
func actionObject(t *testing.T, value any) *unstructured.Unstructured {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	obj := &unstructured.Unstructured{}
	if json.Unmarshal(data, &obj.Object) != nil {
		t.Fatal("bad fixture")
	}
	return obj
}
func TestRepositoryActionAuthorityAndReplacementFences(t *testing.T) {
	for _, action := range []string{"branch_head", "branches"} {
		t.Run(action, func(t *testing.T) { testRepositoryActionAuthority(t, action) })
	}
}
func testRepositoryActionAuthority(t *testing.T, actionName string) {
	for _, kind := range []string{"allowed", "invoke only", "denied", "repository replaced", "connection replaced", "spec changed", "tenant mismatch"} {
		t.Run(kind, func(t *testing.T) {
			// Gate 2 checks `create` on repositories/<action> and nothing else:
			// "invoke only" holds the retired grant and must be denied.
			grants := map[string]bool{"create": kind != "denied" && kind != "invoke only", "invoke": kind == "invoke only"}
			checked := map[string]bool{}
			succeeds := kind == "allowed"
			repo := &api.Repository{TypeMeta: metav1.TypeMeta{APIVersion: "code.railgrid.ai/v1alpha1", Kind: "Repository"}, ObjectMeta: metav1.ObjectMeta{Name: "product", UID: "repo-uid"}, Spec: api.RepositorySpec{ConnectionRef: "git", Name: "product"}, Status: api.RepositoryStatus{RepoID: "123"}}
			conn := &api.Connection{TypeMeta: metav1.TypeMeta{APIVersion: "code.railgrid.ai/v1alpha1", Kind: "Connection"}, ObjectMeta: metav1.ObjectMeta{Name: "git", UID: "conn-uid"}, Spec: api.ConnectionSpec{Provider: api.ProviderGitHub, Type: api.CredentialTypePAT, Owner: "example", SecretRef: api.LocalSecretReference{Name: "git-key"}}}
			caller := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), actionObject(t, repo))
			caller.PrependReactor("create", "selfsubjectaccessreviews", func(action ktesting.Action) (bool, runtime.Object, error) {
				object := action.(ktesting.CreateAction).GetObject().(*unstructured.Unstructured)
				attrs, _, _ := unstructured.NestedMap(object.Object, "spec", "resourceAttributes")
				if attrs["group"] != "code.railgrid.ai" || attrs["resource"] != "repositories" || attrs["name"] != "product" || attrs["subresource"] != actionName {
					t.Fatalf("incorrect permission: %#v", attrs)
				}
				verb, _ := attrs["verb"].(string)
				checked[verb] = true
				return true, &unstructured.Unstructured{Object: map[string]any{"status": map[string]any{"allowed": grants[verb]}}}, nil
			})
			if kind == "repository replaced" {
				repo.UID = "new-repo"
			}
			if kind == "connection replaced" {
				conn.UID = "new-conn"
			}
			if kind == "spec changed" {
				repo.Spec.Name = "other"
			}
			secret := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "Secret", "metadata": map[string]any{"name": "git-key", "namespace": "default"}, "data": map[string]any{"token": base64.StdEncoding.EncodeToString([]byte("provider-secret"))}}}
			provider := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), actionObject(t, repo), actionObject(t, conn), secret)
			backendFake := &backendFixture{t: t}
			registry := backend.NewRegistry()
			if err := registry.Register(backendFake); err != nil {
				t.Fatal(err)
			}
			server := New(callerFixture{client: caller, t: t}, func(_ context.Context, cluster, name string) (dynamic.Interface, error) {
				if cluster != "tenant-id" || name != "product" {
					t.Fatal("export crossed binding")
				}
				return provider, nil
			}, registry)
			body := []byte(`{"input":{"repository":"example/product","repositoryUID":"repo-uid","connectionUID":"conn-uid","branch":"main"}}`)
			request := httptest.NewRequest(http.MethodPost, "/actions/clusters/tenant-id/repositories/product/"+actionName+"/v1", bytes.NewReader(body))
			request.Header.Set("X-Railgrid-Cluster", "tenant-id")
			request.Header.Set("Authorization", "Bearer caller-token")
			request.Header.Set("X-Request-ID", "sdk-request")
			if kind == "tenant mismatch" {
				request.Header.Set("X-Railgrid-Cluster", "other")
			}
			response := httptest.NewRecorder()
			server.ServeHTTP(response, request)
			if kind != "tenant mismatch" && !checked["create"] {
				t.Fatal("gate 2 did not check verb create")
			}
			if checked["invoke"] {
				t.Fatal("gate 2 still checks the retired verb invoke")
			}
			var envelope actionwire.Envelope
			if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
				t.Fatal(err)
			}
			if envelope.RequestID != "sdk-request" || envelope.Provider != "code" || envelope.Action != actionName || envelope.ActionVersion != "v1" || envelope.ResourceRef.Name != "product" || envelope.ResourceRef.Kind != "Repository" || envelope.ResourceRef.Resource != "repositories" || envelope.ResourceRef.APIVersion != "code.railgrid.ai/v1alpha1" {
				t.Fatalf("invalid wire identity: %+v", envelope)
			}
			if succeeds {
				expected := `{"head":"1111111111111111111111111111111111111111"}`
				if actionName == "branches" {
					expected = `{"branches":["main","release/v1"],"nextPage":0}`
				}
				if string(envelope.Result) != expected || envelope.Error != nil {
					t.Fatalf("invalid result: %+v", envelope)
				}
			} else if envelope.Error == nil || envelope.Error.Message == "" || len(envelope.Result) != 0 {
				t.Fatalf("invalid failure: %+v", envelope)
			}
			switch {
			case succeeds:
				if response.Code != 200 || backendFake.calls != 1 {
					t.Fatalf("status=%d calls=%d body=%s", response.Code, backendFake.calls, response.Body.String())
				}
			case kind == "tenant mismatch":
				// The path is authoritative and a header that disagrees with
				// it is a self-contradictory request, not a denial
				// (docs/provider-actions.md, "The path cluster must equal the
				// header cluster").
				if response.Code != 400 || backendFake.calls != 0 {
					t.Fatalf("cluster mismatch status=%d calls=%d", response.Code, backendFake.calls)
				}
			default:
				if response.Code != 403 || backendFake.calls != 0 {
					t.Fatalf("denial status=%d calls=%d", response.Code, backendFake.calls)
				}
			}
			for _, action := range caller.Actions() {
				if action.GetResource().Resource == "secrets" {
					t.Fatal("caller used to read credentials")
				}
			}
			if !succeeds {
				for _, action := range provider.Actions() {
					if action.GetResource().Resource == "secrets" {
						t.Fatal("denied or changed binding reached credential lookup")
					}
				}
			}
		})
	}
}
func TestActionRejectsMalformedInputBeforeAuthority(t *testing.T) {
	server := admissionServer(t, true)
	for _, body := range []string{`{"input":{"unknown":true}}`, `{} {}`, `{`} {
		request := httptest.NewRequest("POST", "/actions/clusters/tenant-id/repositories/product/branch_head/v1", bytes.NewBufferString(body))
		request.Header.Set("X-Railgrid-Cluster", "tenant-id")
		request.Header.Set("Authorization", "Bearer caller-token")
		response := httptest.NewRecorder()
		server.ServeHTTP(response, request)
		if response.Code != 400 {
			t.Fatalf("malformed input status=%d", response.Code)
		}
	}
}

func (f *backendFixture) ListBranches(_ context.Context, conn *api.Connection, cred backend.Credential, repo *api.Repository, page int) (*backend.BranchPage, error) {
	f.calls++
	if conn.Name != "git" || cred.Token != "provider-secret" || repo.Name != "product" || page != 0 {
		f.t.Fatal("branch listing lost repository binding")
	}
	return &backend.BranchPage{Branches: []string{"main", "release/v1"}}, nil
}

// TestRepositoryActionsConformance drives the real handler through the shared
// data-plane contract suite: granted verb 200, missing bearer 401,
// path/header cluster mismatch 400, foreign cluster denied, ungranted verb
// denied, malformed path 400, oversized input 413, unknown input field 400.
func TestRepositoryActionsConformance(t *testing.T) {
	const cluster = "tenant-id"
	repo := &api.Repository{TypeMeta: metav1.TypeMeta{APIVersion: "code.railgrid.ai/v1alpha1", Kind: "Repository"}, ObjectMeta: metav1.ObjectMeta{Name: "product", UID: "repo-uid"}, Spec: api.RepositorySpec{ConnectionRef: "git", Name: "product"}, Status: api.RepositoryStatus{RepoID: "123"}}
	conn := &api.Connection{TypeMeta: metav1.TypeMeta{APIVersion: "code.railgrid.ai/v1alpha1", Kind: "Connection"}, ObjectMeta: metav1.ObjectMeta{Name: "git", UID: "conn-uid"}, Spec: api.ConnectionSpec{Provider: api.ProviderGitHub, Type: api.CredentialTypePAT, Owner: "example", SecretRef: api.LocalSecretReference{Name: "git-key"}}}
	secret := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "Secret", "metadata": map[string]any{"name": "git-key", "namespace": "default"}, "data": map[string]any{"token": base64.StdEncoding.EncodeToString([]byte("provider-secret"))}}}
	provider := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), actionObject(t, repo), actionObject(t, conn), secret)

	callers := &conformance.FakeCallers{
		Cluster:   cluster,
		Token:     "caller-token",
		Objects:   []*unstructured.Unstructured{actionObject(t, repo)},
		ListKinds: map[schema.GroupVersionResource]string{repositories: "RepositoryList"},
		// Only branch_head is granted; every other subresource is the
		// suite's "ungranted verb".
		Allow: func(a conformance.Attributes) bool {
			return a.Verb == dataplane.SSARVerb && a.Resource == "repositories" && a.Subresource == "branch_head"
		},
	}
	registry := backend.NewRegistry()
	if err := registry.Register(&backendFixture{t: t}); err != nil {
		t.Fatal(err)
	}
	server := New(callers, func(context.Context, string, string) (dynamic.Interface, error) { return provider, nil }, registry)

	conformance.Test(t, server, conformance.Fixtures{
		Callers:     callers,
		GrantedPath: "/actions/clusters/" + cluster + "/repositories/product/branch_head/v1",
		DeniedPath:  "/actions/clusters/" + cluster + "/repositories/product/branches/v1",
		MalformedPaths: []string{
			"/actions/clusters/" + cluster + "/repositories/../branch_head/v1",
			"/actions/clusters/" + cluster + "/repositories/product//v1",
		},
		Body:          `{"input":{"repository":"example/product","repositoryUID":"repo-uid","connectionUID":"conn-uid","branch":"main"}}`,
		MaxInputBytes: 64 << 10,
		// A repository-bound action answers a denial with 403: gate 1 already
		// proved the caller can see the object, so 404 would only confuse.
		DeniedStatus:   403,
		ExpectEnvelope: true,
	})
}
