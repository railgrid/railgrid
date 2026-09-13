// Copyright 2026 The Faros Authors.
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

	api "github.com/faroshq/provider-code/apis/v1alpha1"
	"github.com/faroshq/provider-code/backend"
	"github.com/faroshq/provider-sdk/actionwire"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
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
	for _, kind := range []string{"allowed", "denied", "repository replaced", "connection replaced", "spec changed", "tenant mismatch"} {
		t.Run(kind, func(t *testing.T) {
			repo := &api.Repository{TypeMeta: metav1.TypeMeta{APIVersion: "code.faros.sh/v1alpha1", Kind: "Repository"}, ObjectMeta: metav1.ObjectMeta{Name: "product", UID: "repo-uid"}, Spec: api.RepositorySpec{ConnectionRef: "git", Name: "product"}, Status: api.RepositoryStatus{RepoID: "123"}}
			conn := &api.Connection{TypeMeta: metav1.TypeMeta{APIVersion: "code.faros.sh/v1alpha1", Kind: "Connection"}, ObjectMeta: metav1.ObjectMeta{Name: "git", UID: "conn-uid"}, Spec: api.ConnectionSpec{Provider: api.ProviderGitHub, Type: api.CredentialTypePAT, Owner: "example", SecretRef: api.LocalSecretReference{Name: "git-key"}}}
			caller := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), actionObject(t, repo))
			caller.PrependReactor("create", "selfsubjectaccessreviews", func(action ktesting.Action) (bool, runtime.Object, error) {
				object := action.(ktesting.CreateAction).GetObject().(*unstructured.Unstructured)
				attrs, _, _ := unstructured.NestedMap(object.Object, "spec", "resourceAttributes")
				if attrs["group"] != "code.faros.sh" || attrs["resource"] != "repositories" || attrs["name"] != "product" || attrs["verb"] != "invoke" || attrs["subresource"] != "branch_head" {
					t.Fatalf("incorrect permission: %#v", attrs)
				}
				return true, &unstructured.Unstructured{Object: map[string]any{"status": map[string]any{"allowed": kind != "denied"}}}, nil
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
			request := httptest.NewRequest(http.MethodPost, "/actions/clusters/tenant-id/repositories/product/branch_head/v1", bytes.NewReader(body))
			request.Header.Set("X-Faros-Cluster", "tenant-id")
			request.Header.Set("Authorization", "Bearer caller-token")
			request.Header.Set("X-Request-ID", "sdk-request")
			if kind == "tenant mismatch" {
				request.Header.Set("X-Faros-Cluster", "other")
			}
			response := httptest.NewRecorder()
			server.ServeHTTP(response, request)
			var envelope actionwire.Envelope
			if kind != "tenant mismatch" {
				if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
					t.Fatal(err)
				}
				if envelope.RequestID != "sdk-request" || envelope.Provider != "code" || envelope.Action != "branch_head" || envelope.ActionVersion != "v1" || envelope.ResourceRef.Name != "product" || envelope.ResourceRef.Kind != "Repository" || envelope.ResourceRef.Resource != "repositories" || envelope.ResourceRef.APIVersion != "code.faros.sh/v1alpha1" {
					t.Fatalf("invalid wire identity: %+v", envelope)
				}
				if kind == "allowed" {
					if string(envelope.Result) != `{"head":"1111111111111111111111111111111111111111"}` || envelope.Error != nil {
						t.Fatalf("invalid result: %+v", envelope)
					}
				} else if envelope.Error == nil || envelope.Error.Message == "" || len(envelope.Result) != 0 {
					t.Fatalf("invalid failure: %+v", envelope)
				}
			}
			if kind == "allowed" {
				if response.Code != 200 || backendFake.calls != 1 {
					t.Fatalf("status=%d calls=%d body=%s", response.Code, backendFake.calls, response.Body.String())
				}
			} else {
				if response.Code != 403 || backendFake.calls != 0 {
					t.Fatalf("denial status=%d calls=%d", response.Code, backendFake.calls)
				}
			}
			for _, action := range caller.Actions() {
				if action.GetResource().Resource == "secrets" {
					t.Fatal("caller used to read credentials")
				}
			}
			if kind != "allowed" {
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
		request.Header.Set("X-Faros-Cluster", "tenant-id")
		request.Header.Set("Authorization", "Bearer caller-token")
		response := httptest.NewRecorder()
		server.ServeHTTP(response, request)
		if response.Code != 400 {
			t.Fatalf("malformed input status=%d", response.Code)
		}
	}
}
