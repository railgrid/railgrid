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
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	api "github.com/railgrid/provider-code/apis/v1alpha1"
	"github.com/railgrid/provider-code/backend"
	"github.com/railgrid/provider-sdk/actionwire"
	"github.com/railgrid/provider-sdk/dataplane"
	"github.com/railgrid/provider-sdk/dataplane/conformance"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

// The one tenant workspace the fixtures know, as a kcp logical-cluster ID, and
// the caller kcp stamps on a granted request.
const (
	testCluster = "tenant-id"
	testUser    = "alice@railgrid.test"
)

var testListKinds = map[schema.GroupVersionResource]string{
	repositories: "RepositoryList",
	connections:  "ConnectionList",
	{Group: "code.railgrid.ai", Version: "v1alpha1", Resource: "repositorycommits"}: "RepositoryCommitList",
	{Group: "", Version: "v1", Resource: "secrets"}:                                 "SecretList",
}

// kubePath is the one grammar an action is reached on: the custom-subresource
// path a kcp shard forwards.
func kubePath(cluster, resource, name, verb string) string {
	return "/clusters/" + cluster + "/apis/" + repositories.Group + "/" + repositories.Version + "/" + resource + "/" + name + "/" + verb
}

// actionRequest builds a request the way serve's subresource adapter hands one
// to this handler: the caller stamped in requestheader headers AND in the
// context, and the parsed route — with the declared contract version restored
// — beside it. No bearer travels: an action never has one. An empty user
// leaves the request unstamped, which is what the adapter refuses with 401.
func actionRequest(method, path string, body io.Reader, user string) *http.Request {
	r := httptest.NewRequest(method, path, body)
	r.Header.Set("Content-Type", "application/json")
	ctx := r.Context()
	if user != "" {
		r.Header.Set(dataplane.HeaderRemoteUser, user)
		r.Header.Add(dataplane.HeaderRemoteGroup, "system:authenticated")
		ctx = dataplane.WithProxiedIdentity(ctx, dataplane.ProxiedIdentity{User: user, Groups: []string{"system:authenticated"}})
	}
	if route, err := dataplane.ParseSubresourceRequest(r); err == nil {
		route.Request.Version = ContractVersion
		ctx = dataplane.WithRoute(ctx, route)
	}
	return r.WithContext(ctx)
}

// allowGet is the gate's one question, answered for the named objects: may
// the caller `get` {resource}/{name}. The verb itself is kcp's to authorize
// before it ever forwards the request, so no fixture answers for it.
func allowGet(resource string, names ...string) func(conformance.Attributes) bool {
	return func(a conformance.Attributes) bool {
		if a.Verb != "get" || a.Group != repositories.Group || a.Resource != resource || a.Subresource != "" {
			return false
		}
		for _, name := range names {
			if a.Name == name {
				return true
			}
		}
		return false
	}
}

// newCallers is the provider's caller factory for tests: what the provider can
// read in testCluster, and what the caller may see there.
func newCallers(allow func(conformance.Attributes) bool, objects ...*unstructured.Unstructured) *conformance.FakeCallers {
	return &conformance.FakeCallers{
		Cluster:   testCluster,
		User:      testUser,
		Objects:   objects,
		ListKinds: testListKinds,
		Allow:     allow,
	}
}

// providerClient is the client the server acts through in testCluster, for
// asserting what the provider did and did not read or write.
func providerClient(t *testing.T, callers *conformance.FakeCallers) *dynamicfake.FakeDynamicClient {
	t.Helper()
	client, err := callers.AsProvider(testCluster)
	if err != nil {
		t.Fatal(err)
	}
	return client.(*dynamicfake.FakeDynamicClient)
}

func readSecrets(client *dynamicfake.FakeDynamicClient) bool {
	for _, action := range client.Actions() {
		if action.GetResource().Resource == "secrets" {
			return true
		}
	}
	return false
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
func (f *backendFixture) ListBranches(_ context.Context, conn *api.Connection, cred backend.Credential, repo *api.Repository, page int) (*backend.BranchPage, error) {
	f.calls++
	if conn.Name != "git" || cred.Token != "provider-secret" || repo.Name != "product" || page != 0 {
		f.t.Fatal("branch listing lost repository binding")
	}
	return &backend.BranchPage{Branches: []string{"main", "release/v1"}}, nil
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

func testRepository() *api.Repository {
	return &api.Repository{TypeMeta: metav1.TypeMeta{APIVersion: "code.railgrid.ai/v1alpha1", Kind: "Repository"}, ObjectMeta: metav1.ObjectMeta{Name: "product", UID: "repo-uid"}, Spec: api.RepositorySpec{ConnectionRef: "git", Name: "product"}, Status: api.RepositoryStatus{RepoID: "123", CloneURL: "https://github.com/example/product.git"}}
}

func testConnection() *api.Connection {
	return &api.Connection{TypeMeta: metav1.TypeMeta{APIVersion: "code.railgrid.ai/v1alpha1", Kind: "Connection"}, ObjectMeta: metav1.ObjectMeta{Name: "git", UID: "conn-uid"}, Spec: api.ConnectionSpec{Provider: api.ProviderGitHub, Type: api.CredentialTypePAT, Owner: "example", SecretRef: api.LocalSecretReference{Name: "git-key"}}, Status: api.ConnectionStatus{Login: "example"}}
}

func testSecret() *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "Secret", "metadata": map[string]any{"name": "git-key", "namespace": "default"}, "data": map[string]any{"token": base64.StdEncoding.EncodeToString([]byte("provider-secret"))}}}
}

func TestRepositoryActionAuthorityAndReplacementFences(t *testing.T) {
	for _, action := range []string{"branch-head", "branches"} {
		t.Run(action, func(t *testing.T) { testRepositoryActionAuthority(t, action) })
	}
}

// The gate asks exactly one thing about the caller — may they `get` the
// Repository — and everything after it acts as the provider. The input then
// pins what the caller saw (UIDs, owner/name) against what the provider read:
// a replaced Repository or Connection, or a slug that no longer matches, is
// refused before the credential Secret is ever opened.
func testRepositoryActionAuthority(t *testing.T, actionName string) {
	for _, kind := range []string{"allowed", "denied", "repository replaced", "connection replaced", "spec changed"} {
		t.Run(kind, func(t *testing.T) {
			succeeds := kind == "allowed"
			repo, conn := testRepository(), testConnection()
			if kind == "repository replaced" {
				repo.UID = "new-repo"
			}
			if kind == "connection replaced" {
				conn.UID = "new-conn"
			}
			if kind == "spec changed" {
				repo.Spec.Name = "other"
			}
			reviewed := map[string]bool{}
			callers := newCallers(func(a conformance.Attributes) bool {
				if a.Group != "code.railgrid.ai" || a.Resource != "repositories" || a.Name != "product" || a.Subresource != "" {
					t.Fatalf("gate asked about the wrong object: %#v", a)
				}
				reviewed[a.Verb] = true
				return a.Verb == "get" && kind != "denied"
			}, actionObject(t, repo), actionObject(t, conn), testSecret())
			backendFake := &backendFixture{t: t}
			registry := backend.NewRegistry()
			if err := registry.Register(backendFake); err != nil {
				t.Fatal(err)
			}
			server := New(callers, registry)
			body := []byte(`{"input":{"repository":"example/product","repositoryUID":"repo-uid","connectionUID":"conn-uid","branch":"main"}}`)
			request := actionRequest(http.MethodPost, kubePath(testCluster, "repositories", "product", actionName), bytes.NewReader(body), testUser)
			request.Header.Set("X-Request-ID", "sdk-request")
			response := httptest.NewRecorder()
			server.ServeHTTP(response, request)
			if !reviewed["get"] {
				t.Fatal("the gate did not review get on the repository")
			}
			if len(reviewed) != 1 {
				t.Fatalf("the gate reviewed more than visibility: %v (the verb is kcp's to authorize)", reviewed)
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
			provider := providerClient(t, callers)
			switch {
			case succeeds:
				if response.Code != 200 || backendFake.calls != 1 {
					t.Fatalf("status=%d calls=%d body=%s", response.Code, backendFake.calls, response.Body.String())
				}
			case kind == "denied":
				// A caller who cannot see the Repository learns nothing,
				// not even that it exists: the contract's 404.
				if response.Code != 404 || backendFake.calls != 0 {
					t.Fatalf("denial status=%d calls=%d", response.Code, backendFake.calls)
				}
			default:
				if response.Code != 403 || backendFake.calls != 0 {
					t.Fatalf("replaced binding status=%d calls=%d", response.Code, backendFake.calls)
				}
			}
			if !succeeds && readSecrets(provider) {
				t.Fatal("denied or changed binding reached credential lookup")
			}
		})
	}
}

// A request that did not come through serve's adapter carries no route and
// addresses nothing, whatever its URL says.
func TestActionWithoutParsedRouteIsRefused(t *testing.T) {
	server := admissionServer(t, true)
	request := httptest.NewRequest(http.MethodPost, kubePath(testCluster, "repositories", "product", "branch-head"), strings.NewReader(`{"input":{}}`))
	request.Header.Set(dataplane.HeaderRemoteUser, testUser)
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("no route: status=%d, want 400", response.Code)
	}
}

func TestActionRejectsMalformedInputBeforeAuthority(t *testing.T) {
	server := admissionServer(t, true)
	for _, body := range []string{`{"input":{"unknown":true}}`, `{} {}`, `{`} {
		request := actionRequest(http.MethodPost, kubePath(testCluster, "repositories", "product", "branch-head"), bytes.NewBufferString(body), testUser)
		response := httptest.NewRecorder()
		server.ServeHTTP(response, request)
		if response.Code != 400 {
			t.Fatalf("malformed input status=%d", response.Code)
		}
	}
}

// TestRepositoryActionsConformance drives the real handler through the shared
// data-plane contract suite: granted verb 200, no stamped caller 401, a caller
// who cannot see the object denied, foreign cluster denied, undeclared verb
// not served, malformed path 400, oversized input 413, unknown input field 400.
func TestRepositoryActionsConformance(t *testing.T) {
	callers := newCallers(allowGet("repositories", "product"), actionObject(t, testRepository()), actionObject(t, testConnection()), testSecret())
	registry := backend.NewRegistry()
	if err := registry.Register(&backendFixture{t: t}); err != nil {
		t.Fatal(err)
	}
	server := New(callers, registry)

	conformance.Test(t, server, conformance.Fixtures{
		Callers:       callers,
		GrantedPath:   kubePath(testCluster, "repositories", "product", "branch-head"),
		DeniedPath:    kubePath(testCluster, "repositories", "product", "delete-everything"),
		ActionVersion: ContractVersion,
		MalformedPaths: []string{
			"/clusters/" + testCluster + "/apis/code.railgrid.ai/v1alpha1/repositories/../branch-head",
			"/clusters/" + testCluster + "/apis/code.railgrid.ai/v1alpha1/repositories/product//branch-head",
			"/clusters/root:railgrid:tenants:acme/apis/code.railgrid.ai/v1alpha1/repositories/product/branch-head",
			kubePath(testCluster, "repositories", "product", "status"),
		},
		Body:           `{"input":{"repository":"example/product","repositoryUID":"repo-uid","connectionUID":"conn-uid","branch":"main"}}`,
		MaxInputBytes:  64 << 10,
		ExpectEnvelope: true,
	})
}

// A connection-bound action is gated on the Connection, not on any
// Repository that uses it: a caller who can see every repository but not the
// Connection cannot mint a pull credential — which is the whole reason the
// credential moved behind an action instead of staying a Secret read.
func TestConnectionActionIsGatedOnTheConnection(t *testing.T) {
	newServer := func(allow func(conformance.Attributes) bool) (*Server, *conformance.FakeCallers) {
		callers := newCallers(allow, actionObject(t, testRepository()), actionObject(t, testConnection()), testSecret())
		registry := backend.NewRegistry()
		if err := registry.Register(&backendFixture{t: t}); err != nil {
			t.Fatal(err)
		}
		return New(callers, registry), callers
	}

	path := kubePath(testCluster, "connections", "git", MintRegistryToken)
	body := `{"input":{"connectionUID":"conn-uid"}}`

	// Seeing the repositories says nothing about the Connection.
	repoOnly, callers := newServer(allowGet("repositories", "product"))
	recorder := httptest.NewRecorder()
	repoOnly.ServeHTTP(recorder, actionRequest(http.MethodPost, path, strings.NewReader(body), testUser))
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("a caller who cannot see the connection got %d: %s", recorder.Code, recorder.Body.String())
	}
	if readSecrets(providerClient(t, callers)) {
		t.Fatal("a denied caller reached the credential Secret")
	}

	// Seeing the Connection does, and what comes back is a pull credential for
	// the connection's registry — never the Connection's own Secret contents
	// under some other name.
	granted, _ := newServer(allowGet("connections", "git"))
	recorder = httptest.NewRecorder()
	granted.ServeHTTP(recorder, actionRequest(http.MethodPost, path, strings.NewReader(body), testUser))
	if recorder.Code != http.StatusOK {
		t.Fatalf("granted mint: got %d, body %s", recorder.Code, recorder.Body.String())
	}
	var envelope struct {
		Result RegistryTokenOutput `json:"result"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode envelope: %v (body %s)", err, recorder.Body.String())
	}
	if envelope.Result.Registry != "ghcr.io" || envelope.Result.Username != "example" || envelope.Result.Token == "" {
		t.Fatalf("registry credential = %#v", envelope.Result)
	}
	// A PAT cannot be narrowed by any GitHub API, so the action says so
	// rather than implying a least-privilege token it did not issue.
	if envelope.Result.Scoped {
		t.Fatal("a PAT-backed credential was reported as scoped")
	}
}

// A foreign provider — another provider's ServiceAccount, forwarded through
// its own export virtual workspace because the tenant accepted its claim on
// this verb — is not reviewed in the tenant workspace, where it has no RBAC:
// the claim kcp already enforced is the authorization, and the action runs.
func TestForeignProviderIsAdmittedOnItsClaim(t *testing.T) {
	callers := newCallers(func(conformance.Attributes) bool {
		t.Fatal("a foreign provider must not be access-reviewed in the tenant workspace")
		return false
	}, actionObject(t, testRepository()), actionObject(t, testConnection()), testSecret())
	registry := backend.NewRegistry()
	if err := registry.Register(&backendFixture{t: t}); err != nil {
		t.Fatal(err)
	}
	server := New(callers, registry)

	request := actionRequest(http.MethodPost, kubePath(testCluster, "repositories", "product", "branch-head"),
		strings.NewReader(`{"input":{"repository":"example/product","repositoryUID":"repo-uid","connectionUID":"conn-uid","branch":"main"}}`), "")
	foreign := dataplane.ProxiedIdentity{
		User:   "system:serviceaccount:default:app-studio",
		Groups: []string{"system:serviceaccounts", "system:authenticated"},
		Extra:  map[string][]string{dataplane.ClusterNameExtra: {"0appstudiocluster"}},
	}
	request.Header.Set(dataplane.HeaderRemoteUser, foreign.User)
	request = request.WithContext(dataplane.WithProxiedIdentity(request.Context(), foreign))
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("foreign provider: got %d, body %s", response.Code, response.Body.String())
	}
}
