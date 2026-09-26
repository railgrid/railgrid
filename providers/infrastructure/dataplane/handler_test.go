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

package dataplane

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	infrav1alpha1 "github.com/railgrid/provider-infrastructure/apis/v1alpha1"
	sdk "github.com/railgrid/provider-sdk/dataplane"
	"github.com/railgrid/provider-sdk/dataplane/conformance"
	"github.com/railgrid/provider-sdk/serve"
)

const (
	testWorkspace = "tenant-a"
	testNamespace = "tenant-a-default"

	// testUser is the caller kcp stamps on a granted request. There is no
	// bearer on a verb: the identity travels in requestheader headers and,
	// once serve's adapter has read them, in the request context.
	testUser = "alice@railgrid.test"
)

// testCaller is testUser as the adapter puts it in the context.
var testCaller = sdk.ProxiedIdentity{User: testUser, Groups: []string{"system:authenticated"}}

// asInstance restamps a template fixture as the flattened Instance kind. That
// is the only resource the data plane serves, so it is the only GVR the
// provider's client answers gate 1 on.
func asInstance(object *unstructured.Unstructured) *unstructured.Unstructured {
	out := object.DeepCopy()
	out.SetAPIVersion(instancesGVR.Group + "/" + instancesGVR.Version)
	out.SetKind("Instance")
	return out
}

// callersIn builds the provider caller factory the handler runs the gate
// through: the provider sees objects in cluster, and every access review
// about testUser for "get" on an instance is granted, so a routing test
// exercises routing rather than RBAC. (The verb grant itself is kcp's, before
// the request is ever forwarded here, and is not visible to the handler.)
func callersIn(cluster string, objects ...*unstructured.Unstructured) *conformance.FakeCallers {
	visible := make([]*unstructured.Unstructured, 0, len(objects))
	for _, object := range objects {
		visible = append(visible, asInstance(object))
	}
	return &conformance.FakeCallers{
		Cluster:   cluster,
		User:      testUser,
		Objects:   visible,
		ListKinds: map[schema.GroupVersionResource]string{instancesGVR: "InstanceList"},
		Allow: func(a conformance.Attributes) bool {
			return a.Verb == "get" &&
				a.Group == instancesGVR.Group &&
				a.Resource == instancesGVR.Resource &&
				a.Subresource == ""
		},
	}
}

type fakeContractGetter struct {
	contract *infrav1alpha1.TemplateDataPlane
	err      error
}

func (f *fakeContractGetter) For(context.Context, string) (*infrav1alpha1.TemplateDataPlane, error) {
	return f.contract, f.err
}

type fakeRuntime struct {
	host  string
	token string

	gotTokenNamespace string
	gotTokenName      string
}

type activityRuntime struct {
	*fakeRuntime
	calls int
	err   error
}

func (f *activityRuntime) RecordActivity(context.Context, *unstructured.Unstructured) error {
	f.calls++
	return f.err
}

func (f *fakeRuntime) Host() string { return f.host }
func (f *fakeRuntime) Transport() (http.RoundTripper, error) {
	return http.DefaultTransport, nil
}
func (f *fakeRuntime) ControlToken(_ context.Context, namespace, name string) (string, error) {
	f.gotTokenNamespace, f.gotTokenName = namespace, name
	return f.token, nil
}

func newTestHandler(t *testing.T, callers sdk.ProviderCallerFactory, rt Runtime) *Handler {
	t.Helper()
	return NewHandler(callers, &fakeContractGetter{contract: sandboxRunnerContract()}, rt)
}

// runnerCallers is the common fixture: one visible Instance in testWorkspace.
func runnerCallers() *conformance.FakeCallers {
	return callersIn(testWorkspace, runnerInstance(testNamespace))
}

// stamped builds the request the way it reaches the handler through serve's
// subresource adapter: the shard's identity headers, the same identity in
// the context, and the parsed route beside it. An empty user leaves the
// request anonymous, which the gate must refuse.
func stamped(method, target, user string, body io.Reader) *http.Request {
	r := httptest.NewRequest(method, target, body)
	ctx := r.Context()
	if user != "" {
		r.Header.Set(sdk.HeaderRemoteUser, user)
		r.Header.Add(sdk.HeaderRemoteGroup, "system:authenticated")
		identity := testCaller
		identity.User = user
		ctx = sdk.WithProxiedIdentity(ctx, identity)
	}
	if route, err := sdk.ParseSubresourceRequest(r); err == nil {
		ctx = sdk.WithRoute(ctx, route)
	}
	return r.WithContext(ctx)
}

func doRequest(h http.Handler, method, target string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, stamped(method, target, testUser, nil))
	return rec
}

// verbPath is the kube path of a verb on an instance, rendered by the SDK so
// the tests cannot spell the grammar differently from the callers.
func verbPath(t testing.TB, cluster, resource, name, component, verb string) string {
	t.Helper()
	p, err := sdk.SubresourcePath(instancesGVR.Group, instancesGVR.Version, sdk.Request{
		ClusterID: cluster, Resource: resource, Name: name, Component: component, Verb: verb,
	})
	if err != nil {
		t.Fatalf("SubresourcePath(%s/%s/%s?component=%s): %v", cluster, name, verb, component, err)
	}
	return p
}

func dataplaneURL(verb string) string {
	return "/clusters/" + testWorkspace + "/apis/" + instancesGVR.Group + "/" + instancesGVR.Version + "/instances/" + testNamespace + "/" + verb
}

func componentURL(cluster, name, component, verb string) string {
	return "/clusters/" + cluster + "/apis/" + instancesGVR.Group + "/" + instancesGVR.Version + "/instances/" + name + "/" + verb + "?" + sdk.ComponentQuery + "=" + component
}

func TestVerbPathsMatchTheSDK(t *testing.T) {
	if got, want := dataplaneURL("log"), verbPath(t, testWorkspace, "instances", testNamespace, "", "log"); got != want {
		t.Errorf("dataplaneURL = %q, want %q", got, want)
	}
	if got, want := componentURL("ws", "shop", "backend", "sync"), verbPath(t, "ws", "instances", "shop", "backend", "sync"); got != want {
		t.Errorf("componentURL = %q, want %q", got, want)
	}
}

func TestHandlerProxiesControlVerb(t *testing.T) {
	var gotPath, gotControlToken, gotAuth string
	var gotIdentity []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotControlToken = r.Header.Get(controlTokenHeader)
		gotAuth = r.Header.Get("Authorization")
		for name := range r.Header {
			if strings.HasPrefix(name, "X-Remote-") || strings.HasPrefix(name, "X-Railgrid-") || name == sdk.HeaderHops {
				gotIdentity = append(gotIdentity, name)
			}
		}
		_, _ = io.WriteString(w, "log line\n")
	}))
	defer upstream.Close()

	rt := &fakeRuntime{host: upstream.URL, token: "control-secret-token"}
	req := stamped(http.MethodGet, dataplaneURL("log"), testUser, nil)
	// Everything a shard or the hub proxy might stamp, as it would arrive.
	req.Header.Set(sdk.HeaderRemoteExtraPrefix+"Authentication.kcp.io%2Fcluster-name", "somecluster")
	req.Header.Set(sdk.HeaderHops, "2")
	req.Header.Set(sdk.HeaderCluster, testWorkspace)
	req.Header.Set(sdk.HeaderUser, testUser)
	rec := httptest.NewRecorder()
	newTestHandler(t, runnerCallers(), rt).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != "log line\n" {
		t.Errorf("body = %q, want %q", rec.Body.String(), "log line\n")
	}
	wantPath := "/api/v1/namespaces/" + testNamespace + "/services/" + testNamespace + "-control:control/proxy/logs"
	if gotPath != wantPath {
		t.Errorf("upstream path = %q, want %q", gotPath, wantPath)
	}
	if gotControlToken != "control-secret-token" {
		t.Errorf("control token header = %q, want injected token", gotControlToken)
	}
	if gotAuth != "" {
		t.Errorf("an Authorization header reached the runtime: %q", gotAuth)
	}
	if len(gotIdentity) != 0 {
		// The stamped identity is the provider's to read, not the runtime's:
		// a verb never carries the caller past this hop.
		t.Errorf("caller identity headers reached the runtime: %v", gotIdentity)
	}
	if rt.gotTokenName != testNamespace+"-control" || rt.gotTokenNamespace != testNamespace {
		t.Errorf("control token read from %s/%s, want %s/%s-control", rt.gotTokenNamespace, rt.gotTokenName, testNamespace, testNamespace)
	}
}

func TestHandlerProxyVerbAppendsCallerPath(t *testing.T) {
	var gotPath string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
	}))
	defer upstream.Close()

	h := newTestHandler(t, runnerCallers(), &fakeRuntime{host: upstream.URL})
	rec := doRequest(h, http.MethodGet, dataplaneURL("proxy")+"/assets/app.js")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	wantPath := "/api/v1/namespaces/" + testNamespace + "/services/" + testNamespace + "-preview:preview/proxy/assets/app.js"
	if gotPath != wantPath {
		t.Errorf("upstream path = %q, want %q", gotPath, wantPath)
	}
}

// The caller's query string must survive the rewrite. The Director replaces
// only URL.Path, so RawQuery rides along on the cloned request — but nothing
// enforced that, and the agents provider's self-hosted search puts its entire
// request in the query (`?q=…&format=json`). Dropping it would turn every
// search into an empty-but-successful response, which reads as "no results"
// rather than as a bug.
func TestHandlerProxyVerbPreservesQueryString(t *testing.T) {
	var gotQuery string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
	}))
	defer upstream.Close()

	h := newTestHandler(t, runnerCallers(), &fakeRuntime{host: upstream.URL})
	rec := doRequest(h, http.MethodGet, dataplaneURL("proxy")+"/search?q=ada+lovelace&format=json")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if gotQuery != "q=ada+lovelace&format=json" {
		t.Errorf("upstream query = %q, want the caller's verbatim", gotQuery)
	}
}

// The component travels as ?component= on the kube path. It addresses the
// object, not the verb, so it must not reach the upstream — while the rest of
// the query string still arrives untouched and in the caller's order.
func TestHandlerStripsComponentFromForwardedQuery(t *testing.T) {
	var gotQuery, gotPath string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery = r.URL.Path, r.URL.RawQuery
	}))
	defer upstream.Close()

	ns := "ws-default"
	h := NewHandler(callersIn("ws", applicationInstance(ns)), &fakeContractGetter{contract: applicationContract()}, &fakeRuntime{host: upstream.URL})
	for raw, want := range map[string]string{
		"?component=frontend&follow=true&tail=50":  "follow=true&tail=50",
		"?follow=true&component=frontend&tail=50":  "follow=true&tail=50",
		"?follow=true&tail=50&component=frontend":  "follow=true&tail=50",
		"?component=frontend":                      "",
		"?%63omponent=frontend&z=1&a=2":            "z=1&a=2",
		"?component=frontend&component=frontend&x": "", // refused by the parser: no route, 400
	} {
		target := "/clusters/ws/apis/" + instancesGVR.Group + "/" + instancesGVR.Version + "/instances/shop/log" + raw
		rec := doRequest(h, http.MethodGet, target)
		if strings.Count(raw, "component=") > 1 {
			if rec.Code != http.StatusBadRequest {
				t.Errorf("%s: status = %d, want 400 for a repeated component", raw, rec.Code)
			}
			continue
		}
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status = %d, want 200 (body %q)", raw, rec.Code, rec.Body.String())
		}
		if gotQuery != want {
			t.Errorf("%s: upstream query = %q, want %q", raw, gotQuery, want)
		}
		if wantPath := "/api/v1/namespaces/" + ns + "/services/shop-frontend-control:control/proxy/logs"; gotPath != wantPath {
			t.Errorf("%s: upstream path = %q, want %q", raw, gotPath, wantPath)
		}
	}
}

func TestHandlerProxiesSandboxPreviewWithoutRouteGate(t *testing.T) {
	// The public preview HTTPRoute is now created declaratively by the
	// SandboxRunner RGD (same as the application template), so the data-plane
	// proxy has no per-request route-reconciliation gate — a sandbox proxy
	// request reverse-proxies straight to the runtime preview Service.
	var gotPath string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
	}))
	defer upstream.Close()

	h := newTestHandler(t, runnerCallers(), &fakeRuntime{host: upstream.URL})
	rec := doRequest(h, http.MethodGet, dataplaneURL("proxy")+"/assets/app.js")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if gotPath == "" {
		t.Fatal("request was not proxied")
	}
}

func TestHandlerStatusVerbServedFromCR(t *testing.T) {
	h := newTestHandler(t, runnerCallers(), &fakeRuntime{host: "http://unused"})
	rec := doRequest(h, http.MethodGet, dataplaneURL("runtime-status"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "runtimeNamespace") {
		t.Errorf("status body missing runtimeNamespace: %s", rec.Body.String())
	}
}

func TestHandlerRecordsActivityAfterAuthorization(t *testing.T) {
	rt := &activityRuntime{fakeRuntime: &fakeRuntime{host: "http://unused"}}
	h := NewHandler(runnerCallers(), &fakeContractGetter{contract: sandboxRunnerContract()}, rt)
	rec := doRequest(h, http.MethodGet, dataplaneURL("runtime-status"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
	if rt.calls != 1 {
		t.Fatalf("activity calls = %d, want 1", rt.calls)
	}
}

func TestHandlerFailsClosedWhenActivityMarkerCannotBeWritten(t *testing.T) {
	rt := &activityRuntime{fakeRuntime: &fakeRuntime{host: "http://unused"}, err: context.DeadlineExceeded}
	h := NewHandler(runnerCallers(), &fakeContractGetter{contract: sandboxRunnerContract()}, rt)
	rec := doRequest(h, http.MethodGet, dataplaneURL("runtime-status"))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 (body %q)", rec.Code, rec.Body.String())
	}
	if rt.calls != 1 {
		t.Fatalf("activity calls = %d, want 1", rt.calls)
	}
}

// A request with no stamped caller has nobody to authorize. Anonymous is not
// a fallback and the provider's own identity is never a substitute.
func TestHandlerRejectsMissingCaller(t *testing.T) {
	h := newTestHandler(t, runnerCallers(), &fakeRuntime{host: "http://unused"})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, stamped(http.MethodGet, dataplaneURL("log"), "", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

// A request that did not come through serve's subresource adapter carries no
// parsed route, and nothing else is entitled to say what it addresses — not
// even a well-formed URL.
func TestHandlerRefusesRequestWithoutRoute(t *testing.T) {
	h := newTestHandler(t, runnerCallers(), &fakeRuntime{host: "http://unused"})
	req := httptest.NewRequest(http.MethodGet, dataplaneURL("log"), nil)
	req.Header.Set(sdk.HeaderRemoteUser, testUser)
	req = req.WithContext(sdk.WithProxiedIdentity(req.Context(), sdk.ProxiedIdentity{User: testUser}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

// A gate failure is a 404 whatever refused and whatever the underlying
// Kubernetes status was. A caller who may not use a verb must not be able to
// tell an object that exists from one that does not: the whole point of the
// contract's non-disclosing default.
func TestHandlerDeniesWithoutDisclosure(t *testing.T) {
	for _, tc := range []struct {
		name    string
		callers *conformance.FakeCallers
		user    string
		path    string
	}{
		{
			name:    "no such instance",
			callers: callersIn(testWorkspace),
			user:    testUser,
			path:    dataplaneURL("log"),
		},
		{
			name:    "instance belongs to another workspace",
			callers: callersIn("otherworkspace", runnerInstance(testNamespace)),
			user:    testUser,
			path:    dataplaneURL("log"),
		},
		{
			name:    "caller cannot see the instance",
			callers: runnerCallers(),
			user:    conformance.StrangerUser,
			path:    dataplaneURL("log"),
		},
		{
			name:    "resource is not served",
			callers: runnerCallers(),
			user:    testUser,
			path:    "/clusters/" + testWorkspace + "/apis/" + instancesGVR.Group + "/" + instancesGVR.Version + "/sandboxrunners/" + testNamespace + "/log",
		},
		{
			name:    "group is not ours",
			callers: runnerCallers(),
			user:    testUser,
			path:    "/clusters/" + testWorkspace + "/apis/other.railgrid.ai/" + instancesGVR.Version + "/instances/" + testNamespace + "/log",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newTestHandler(t, tc.callers, &fakeRuntime{host: "http://unused"})
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, stamped(http.MethodGet, tc.path, tc.user, nil))
			if rec.Code != http.StatusNotFound {
				t.Fatalf("status = %d, want 404 (body %q)", rec.Code, rec.Body.String())
			}
			if strings.Contains(rec.Body.String(), testNamespace) {
				t.Errorf("denial body names the object: %q", rec.Body.String())
			}
		})
	}
}

// An object on its way out is denied: a verb must not run against something
// already being deleted, and the answer is the same non-disclosing 404.
func TestHandlerDeniesDeletingInstance(t *testing.T) {
	instance := runnerInstance(testNamespace)
	if err := unstructured.SetNestedField(instance.Object, "2026-01-01T00:00:00Z", "metadata", "deletionTimestamp"); err != nil {
		t.Fatal(err)
	}
	h := newTestHandler(t, callersIn(testWorkspace, instance), &fakeRuntime{host: "http://unused"})
	rec := doRequest(h, http.MethodGet, dataplaneURL("runtime-status"))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (body %q)", rec.Code, rec.Body.String())
	}
}

func TestHandlerMethodNotAllowed(t *testing.T) {
	h := newTestHandler(t, runnerCallers(), &fakeRuntime{host: "http://unused"})
	rec := doRequest(h, http.MethodPost, dataplaneURL("log")) // log is GET-only
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
}

func TestHandlerNamespaceEscapeIsConflict(t *testing.T) {
	instance := runnerInstance(testNamespace)
	unstructured.SetNestedField(instance.Object, "kube-system", "status", "controlServiceRef", "namespace") //nolint:errcheck
	h := newTestHandler(t, callersIn(testWorkspace, instance), &fakeRuntime{host: "http://unused"})
	rec := doRequest(h, http.MethodGet, dataplaneURL("log"))
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rec.Code)
	}
}

func TestHandlerUnknownVerb(t *testing.T) {
	h := newTestHandler(t, runnerCallers(), &fakeRuntime{host: "http://unused"})
	rec := doRequest(h, http.MethodGet, dataplaneURL("exec"))
	// exec is a reserved component-only capability and never falls through to
	// the generic endpoint method resolver.
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestHandlerUnavailableWhenDepsNil(t *testing.T) {
	h := NewHandler(nil, nil, nil)
	rec := doRequest(h, http.MethodGet, dataplaneURL("log"))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
}

func TestHandlerProxiesComponentVerb(t *testing.T) {
	var gotPath, gotControlToken string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotControlToken = r.Header.Get(controlTokenHeader)
	}))
	defer upstream.Close()

	ns := "ws-default"
	rt := &fakeRuntime{host: upstream.URL, token: "control-secret-token"}
	h := NewHandler(callersIn("ws", applicationInstance(ns)), &fakeContractGetter{contract: applicationContract()}, rt)

	rec := doRequest(h, http.MethodPost, componentURL("ws", "shop", "backend", "sync"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
	wantPath := "/api/v1/namespaces/" + ns + "/services/shop-backend-control:control/proxy/sync"
	if gotPath != wantPath {
		t.Errorf("upstream path = %q, want %q", gotPath, wantPath)
	}
	if gotControlToken != "control-secret-token" {
		t.Errorf("control token header = %q, want injected token", gotControlToken)
	}

	// Method allowlist applies per component verb.
	rec = doRequest(h, http.MethodGet, componentURL("ws", "shop", "backend", "sync"))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET component sync: status = %d, want 405", rec.Code)
	}

	// Unknown component is a conflict (contract mismatch), not a proxy.
	rec = doRequest(h, http.MethodPost, componentURL("ws", "shop", "worker", "sync"))
	if rec.Code != http.StatusMethodNotAllowed && rec.Code != http.StatusConflict {
		t.Errorf("unknown component: status = %d, want 405/409", rec.Code)
	}

	// The old path spelling is not a component address: kcp would route
	// "components" as a subresource of its own, and this handler never sees
	// it as anything but a verb it does not serve.
	rec = doRequest(h, http.MethodPost, "/clusters/ws/apis/"+instancesGVR.Group+"/"+instancesGVR.Version+"/instances/shop/components/backend/sync")
	if rec.Code != http.StatusNotFound && rec.Code != http.StatusConflict && rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("path-form component: status = %d, want a refusal", rec.Code)
	}
}

// newServer mounts the handler the way main does — through serve.New with
// the coordinates this provider's own manifest declares — so the suite drives
// the adapter (path parsing, declaration check, identity headers) and the
// handler together, exactly as a kcp shard reaches them.
func newServer(t *testing.T, h *Handler) http.Handler {
	t.Helper()
	subresources, err := serve.SubresourcesFromCatalogEntryFile("../manifest.yaml")
	if err != nil {
		t.Fatalf("subresources from manifest: %v", err)
	}
	server, err := serve.New(serve.Options{
		Name:         "infrastructure",
		Readiness:    http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }),
		DataPlane:    h,
		Subresources: subresources,
	})
	if err != nil {
		t.Fatalf("serve.New refused this provider's layout: %v", err)
	}
	return server
}

// The grammar and the gate are the SDK's, so the contract's own suite is what
// proves this provider serves them: granted verb 200, no stamped caller 401, a
// caller who cannot see the object denied, foreign cluster denied, undeclared
// verb not served, malformed path refused. It runs against the whole server
// serve.New builds, with the real handler behind the real declaration.
func TestDataPlaneConformance(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer upstream.Close()

	callers := runnerCallers()
	h := newTestHandler(t, callers, &fakeRuntime{host: upstream.URL})
	apis := "/clusters/" + testWorkspace + "/apis/" + instancesGVR.Group + "/" + instancesGVR.Version

	conformance.Test(t, newServer(t, h), conformance.Fixtures{
		Callers:     callers,
		GrantedPath: dataplaneURL("sync"),
		// A verb this provider never declared: the adapter refuses it before
		// the handler could answer, because the declaration is the contract.
		DeniedPath: dataplaneURL("shout"),
		MalformedPaths: []string{
			apis + "/instances/../" + testNamespace + "/sync",
			apis + "/instances//" + testNamespace + "/sync",
			"/clusters/root:railgrid:orgs:acme/apis/" + instancesGVR.Group + "/" + instancesGVR.Version + "/instances/" + testNamespace + "/sync",
			dataplaneURL("status"),
			dataplaneURL("sync") + "?component=a&component=b",
			dataplaneURL("sync") + "?component=..",
		},
		// A data-plane verb is a proxy, not an action: it has no actionwire
		// envelope and no {"input": …} body to decode strictly.
		SkipStrictBody: true,
	})
}

// The same suite against the bare handler: it reads the context serve's
// adapter would have set, so it must behave identically without the adapter
// in front of it (the suite stamps both the headers and the context).
func TestDataPlaneConformanceBareHandler(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer upstream.Close()

	callers := runnerCallers()
	h := newTestHandler(t, callers, &fakeRuntime{host: upstream.URL})

	conformance.Test(t, h, conformance.Fixtures{
		Callers:     callers,
		GrantedPath: dataplaneURL("sync"),
		// Without the adapter's table the handler itself refuses a verb the
		// template contract does not declare; the suite accepts 404 or the
		// denied status, and this one is a 409 from the resolver, so name a
		// resource the handler does not serve instead.
		DeniedPath: "/clusters/" + testWorkspace + "/apis/" + instancesGVR.Group + "/" + instancesGVR.Version + "/sandboxrunners/" + testNamespace + "/sync",
		MalformedPaths: []string{
			"/clusters/" + testWorkspace + "/apis/" + instancesGVR.Group + "/" + instancesGVR.Version + "/instances/../" + testNamespace + "/sync",
			"/clusters/root:railgrid:orgs:acme/apis/" + instancesGVR.Group + "/" + instancesGVR.Version + "/instances/" + testNamespace + "/sync",
		},
		SkipStrictBody: true,
	})
}

func TestDataPlaneFromTemplate(t *testing.T) {
	tmpl := &unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{"name": "sandbox-runner"},
		"spec": map[string]any{
			"instanceCRD": map[string]any{"resource": "sandboxrunners"},
			"dataPlane": map[string]any{
				"runtimeNamespacePath": "status.runtimeNamespace",
				"tokenSecretPath":      "status.controlSecretRef",
				"endpoints": map[string]any{
					"log":            map[string]any{"servicePath": "status.controlServiceRef", "port": "control", "upstreamPath": "/logs", "methods": []any{"GET"}, "stream": true},
					"runtime-status": map[string]any{"fromStatus": true},
				},
			},
		},
	}}
	got, err := dataPlaneFromTemplate(tmpl)
	if err != nil {
		t.Fatalf("dataPlaneFromTemplate error: %v", err)
	}
	if got == nil {
		t.Fatal("dataPlaneFromTemplate returned nil contract")
	}
	if got.RuntimeNamespacePath != "status.runtimeNamespace" || got.TokenSecretPath != "status.controlSecretRef" {
		t.Errorf("contract paths = %q / %q", got.RuntimeNamespacePath, got.TokenSecretPath)
	}
	logEp, ok := got.Endpoints["log"]
	if !ok || logEp.Port != "control" || !logEp.Stream || logEp.UpstreamPath != "/logs" {
		t.Errorf("log endpoint decoded wrong: %+v", logEp)
	}
	if st, ok := got.Endpoints["runtime-status"]; !ok || !st.FromStatus {
		t.Errorf("status endpoint decoded wrong: %+v", st)
	}
}

func TestDataPlaneFromTemplateAbsent(t *testing.T) {
	tmpl := &unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{"name": "redis"},
		"spec":     map[string]any{"instanceCRD": map[string]any{"resource": "redises"}},
	}}
	got, err := dataPlaneFromTemplate(tmpl)
	if err != nil {
		t.Fatalf("dataPlaneFromTemplate error: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil contract for a template with no dataPlane, got %+v", got)
	}
}
