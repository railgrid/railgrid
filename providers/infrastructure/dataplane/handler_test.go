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
)

const (
	testWorkspace = "tenant-a"
	testNamespace = "tenant-a-default"
)

// callerToken is the only bearer the fake caller factory treats as the
// tenant's caller; anything else sees an empty workspace, which is how
// "workspace A's token cannot reach workspace B" is observable here.
const callerToken = "caller-token"

// asInstance restamps a template fixture as the flattened Instance kind. That
// is the only resource the data plane serves, so it is the only GVR the
// caller's client answers gate 1 on.
func asInstance(object *unstructured.Unstructured) *unstructured.Unstructured {
	out := object.DeepCopy()
	out.SetAPIVersion(instancesGVR.Group + "/" + instancesGVR.Version)
	out.SetKind("Instance")
	return out
}

// callersIn builds the caller factory the handler runs both gates through.
// deny names the verbs gate 2 refuses; everything else on instances/* is
// granted, so a routing test exercises routing rather than RBAC.
func callersIn(cluster string, deny []string, objects ...*unstructured.Unstructured) *conformance.FakeCallers {
	refused := map[string]bool{}
	for _, verb := range deny {
		refused[verb] = true
	}
	visible := make([]*unstructured.Unstructured, 0, len(objects))
	for _, object := range objects {
		visible = append(visible, asInstance(object))
	}
	return &conformance.FakeCallers{
		Cluster:   cluster,
		Token:     callerToken,
		Objects:   visible,
		ListKinds: map[schema.GroupVersionResource]string{instancesGVR: "InstanceList"},
		Allow: func(a conformance.Attributes) bool {
			return a.Verb == sdk.SSARVerb &&
				a.Group == instancesGVR.Group &&
				a.Resource == instancesGVR.Resource &&
				a.Subresource != "" && !refused[a.Subresource]
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

func newTestHandler(t *testing.T, callers sdk.CallerFactory, rt Runtime) *Handler {
	t.Helper()
	return NewHandler(callers, &fakeContractGetter{contract: sandboxRunnerContract()}, rt)
}

// runnerCallers is the common fixture: one visible Instance in testWorkspace
// with every verb granted.
func runnerCallers() *conformance.FakeCallers {
	return callersIn(testWorkspace, nil, runnerInstance(testNamespace))
}

func doRequest(h *Handler, method, target string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, nil)
	req.Header.Set("Authorization", "Bearer "+callerToken)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func dataplaneURL(verb string) string {
	return PathPrefix + "clusters/" + testWorkspace + "/instances/" + testNamespace + "/" + verb
}

func TestHandlerProxiesControlVerb(t *testing.T) {
	var gotPath, gotControlToken, gotAuth string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotControlToken = r.Header.Get(controlTokenHeader)
		gotAuth = r.Header.Get("Authorization")
		_, _ = io.WriteString(w, "log line\n")
	}))
	defer upstream.Close()

	rt := &fakeRuntime{host: upstream.URL, token: "control-secret-token"}
	rec := doRequest(newTestHandler(t, runnerCallers(), rt), http.MethodGet, dataplaneURL("log"))

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
		t.Errorf("caller Authorization leaked to runtime: %q", gotAuth)
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
	rec := doRequest(h, http.MethodGet, dataplaneURL("status"))
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
	rec := doRequest(h, http.MethodGet, dataplaneURL("status"))
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
	rec := doRequest(h, http.MethodGet, dataplaneURL("status"))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 (body %q)", rec.Code, rec.Body.String())
	}
	if rt.calls != 1 {
		t.Fatalf("activity calls = %d, want 1", rt.calls)
	}
}

func TestHandlerRejectsMissingToken(t *testing.T) {
	h := newTestHandler(t, runnerCallers(), &fakeRuntime{host: "http://unused"})
	req := httptest.NewRequest(http.MethodGet, dataplaneURL("log"), nil) // no Authorization
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

// A gate failure is a 404 whichever gate refused and whatever the underlying
// Kubernetes status was. A caller that may not use a verb must not be able to
// tell an object that exists from one that does not: the whole point of the
// contract's non-disclosing default.
func TestHandlerDeniesWithoutDisclosure(t *testing.T) {
	for _, tc := range []struct {
		name    string
		callers *conformance.FakeCallers
		path    string
	}{
		{
			name:    "no such instance",
			callers: callersIn(testWorkspace, nil),
			path:    dataplaneURL("log"),
		},
		{
			name:    "instance belongs to another workspace",
			callers: callersIn("otherworkspace", nil, runnerInstance(testNamespace)),
			path:    dataplaneURL("log"),
		},
		{
			name:    "verb is not granted",
			callers: callersIn(testWorkspace, []string{"log"}, runnerInstance(testNamespace)),
			path:    dataplaneURL("log"),
		},
		{
			name:    "resource is not served",
			callers: runnerCallers(),
			path:    PathPrefix + "clusters/" + testWorkspace + "/sandboxrunners/" + testNamespace + "/log",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newTestHandler(t, tc.callers, &fakeRuntime{host: "http://unused"})
			rec := doRequest(h, http.MethodGet, tc.path)
			if rec.Code != http.StatusNotFound {
				t.Fatalf("status = %d, want 404 (body %q)", rec.Code, rec.Body.String())
			}
			if strings.Contains(rec.Body.String(), testNamespace) {
				t.Errorf("denial body names the object: %q", rec.Body.String())
			}
		})
	}
}

// Gate 2 is per verb, not per object: granting one verb must not carry any
// other verb on the same instance, and a component verb collapses onto the
// instance-level subresource so one grant covers both spellings.
func TestHandlerGatesEveryVerbSeparately(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer upstream.Close()

	callers := callersIn(testWorkspace, []string{"restart"}, runnerInstance(testNamespace))
	h := newTestHandler(t, callers, &fakeRuntime{host: upstream.URL})

	if rec := doRequest(h, http.MethodPost, dataplaneURL("sync")); rec.Code != http.StatusOK {
		t.Fatalf("granted sync: status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
	if rec := doRequest(h, http.MethodPost, dataplaneURL("restart")); rec.Code != http.StatusNotFound {
		t.Fatalf("ungranted restart: status = %d, want 404", rec.Code)
	}
}

// A request whose path cluster disagrees with the hub-injected header is
// self-contradictory: the path wins, and the request is refused outright
// rather than served against either reading.
func TestHandlerRefusesClusterHeaderMismatch(t *testing.T) {
	h := newTestHandler(t, runnerCallers(), &fakeRuntime{host: "http://unused"})
	req := httptest.NewRequest(http.MethodGet, dataplaneURL("log"), nil)
	req.Header.Set("Authorization", "Bearer "+callerToken)
	req.Header.Set(sdk.HeaderCluster, "someotherworkspc")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
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
	h := newTestHandler(t, callersIn(testWorkspace, nil, instance), &fakeRuntime{host: "http://unused"})
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
	h := NewHandler(callersIn("ws", nil, applicationInstance(ns)), &fakeContractGetter{contract: applicationContract()}, rt)

	rec := doRequest(h, http.MethodPost, PathPrefix+"clusters/ws/instances/shop/components/backend/sync")
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
	rec = doRequest(h, http.MethodGet, PathPrefix+"clusters/ws/instances/shop/components/backend/sync")
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET component sync: status = %d, want 405", rec.Code)
	}

	// Unknown component is a conflict (contract mismatch), not a proxy.
	rec = doRequest(h, http.MethodPost, PathPrefix+"clusters/ws/instances/shop/components/worker/sync")
	if rec.Code != http.StatusMethodNotAllowed && rec.Code != http.StatusConflict {
		t.Errorf("unknown component: status = %d, want 405/409", rec.Code)
	}
}

// The grammar and both gates are the SDK's, so the contract's own suite is
// what proves this provider serves them: granted verb 200, missing bearer
// 401, header/path cluster mismatch 400, foreign cluster denied, ungranted
// verb denied, malformed path 400. It runs against the real handler, which is
// exactly what serve.New mounts under /dataplane/.
func TestDataPlaneConformance(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer upstream.Close()

	// "restart" is the verb the fake refuses; "sync" is the one it grants.
	callers := callersIn(testWorkspace, []string{"restart"}, runnerInstance(testNamespace))
	h := newTestHandler(t, callers, &fakeRuntime{host: upstream.URL})

	conformance.Test(t, h, conformance.Fixtures{
		Callers:     callers,
		GrantedPath: dataplaneURL("sync"),
		DeniedPath:  dataplaneURL("restart"),
		MalformedPaths: []string{
			PathPrefix + "clusters/" + testWorkspace + "/instances/../" + testNamespace + "/sync",
			PathPrefix + "clusters/" + testWorkspace + "/instances//" + testNamespace + "/sync",
			PathPrefix + "clusters/root:railgrid:orgs:acme/instances/" + testNamespace + "/sync",
			PathPrefix + "clusters/" + testWorkspace + "/instances/" + testNamespace + "/components/sync",
		},
		// A data-plane verb is a proxy, not an action: it has no actionwire
		// envelope and no {"input": …} body to decode strictly.
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
					"log":    map[string]any{"servicePath": "status.controlServiceRef", "port": "control", "upstreamPath": "/logs", "methods": []any{"GET"}, "stream": true},
					"status": map[string]any{"fromStatus": true},
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
	if st, ok := got.Endpoints["status"]; !ok || !st.FromStatus {
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
