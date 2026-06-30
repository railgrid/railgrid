/*
Copyright 2026 The Faros Authors.

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
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	infrav1alpha1 "github.com/faroshq/provider-infrastructure/apis/v1alpha1"
)

const testNamespace = "kedge-sandbox-a1c31ddaaaa007d4"

type fakeInstanceGetter struct {
	instance *unstructured.Unstructured
	err      error

	gotWorkspace string
	gotToken     string
	gotResource  string
	gotName      string
}

func (f *fakeInstanceGetter) Get(_ context.Context, ws, token, resource, name string) (*unstructured.Unstructured, error) {
	f.gotWorkspace, f.gotToken, f.gotResource, f.gotName = ws, token, resource, name
	if f.err != nil {
		return nil, f.err
	}
	return f.instance, nil
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

func (f *fakeRuntime) Host() string { return f.host }
func (f *fakeRuntime) Transport() (http.RoundTripper, error) {
	return http.DefaultTransport, nil
}
func (f *fakeRuntime) ControlToken(_ context.Context, namespace, name string) (string, error) {
	f.gotTokenNamespace, f.gotTokenName = namespace, name
	return f.token, nil
}

type fakePreviewRouteManager struct {
	err error

	calls int
	got   *unstructured.Unstructured
}

func (f *fakePreviewRouteManager) Ensure(_ context.Context, instance *unstructured.Unstructured) error {
	f.calls++
	f.got = instance
	return f.err
}

func newTestHandler(t *testing.T, ig *fakeInstanceGetter, rt *fakeRuntime) *Handler {
	t.Helper()
	return NewHandler(ig, &fakeContractGetter{contract: sandboxRunnerContract()}, rt)
}

func doRequest(h *Handler, method, target string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, nil)
	req.Header.Set("Authorization", "Bearer caller-token")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func dataplaneURL(verb string) string {
	return PathPrefix + "clusters/root:kedge:orgs:acme/sandboxrunners/" + testNamespace + "/" + verb
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

	ig := &fakeInstanceGetter{instance: runnerInstance(testNamespace)}
	rt := &fakeRuntime{host: upstream.URL, token: "control-secret-token"}
	rec := doRequest(newTestHandler(t, ig, rt), http.MethodGet, dataplaneURL("log"))

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
	// Authz used the path workspace + caller token.
	if ig.gotWorkspace != "root:kedge:orgs:acme" || ig.gotToken != "caller-token" {
		t.Errorf("authz used ws=%q token=%q, want acme/caller-token", ig.gotWorkspace, ig.gotToken)
	}
	if ig.gotResource != "sandboxrunners" || ig.gotName != testNamespace {
		t.Errorf("authz used resource=%q name=%q", ig.gotResource, ig.gotName)
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

	h := newTestHandler(t, &fakeInstanceGetter{instance: runnerInstance(testNamespace)}, &fakeRuntime{host: upstream.URL})
	rec := doRequest(h, http.MethodGet, dataplaneURL("proxy")+"/assets/app.js")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	wantPath := "/api/v1/namespaces/" + testNamespace + "/services/" + testNamespace + "-preview:preview/proxy/assets/app.js"
	if gotPath != wantPath {
		t.Errorf("upstream path = %q, want %q", gotPath, wantPath)
	}
}

func TestHandlerEnsuresPreviewRouteBeforeProxyingSandboxPreview(t *testing.T) {
	var gotPath string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
	}))
	defer upstream.Close()

	manager := &fakePreviewRouteManager{}
	h := NewHandler(
		&fakeInstanceGetter{instance: runnerInstance(testNamespace)},
		&fakeContractGetter{contract: sandboxRunnerContract()},
		&fakeRuntime{host: upstream.URL},
		WithPreviewRouteManager(manager),
	)
	rec := doRequest(h, http.MethodGet, dataplaneURL("proxy")+"/assets/app.js")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if manager.calls != 1 {
		t.Fatalf("preview route ensure calls = %d, want 1", manager.calls)
	}
	if manager.got == nil || manager.got.GetName() != "kedge-sandbox-a1c31ddaaaa007d4" {
		t.Fatalf("preview route manager got instance %#v", manager.got)
	}
	if gotPath == "" {
		t.Fatal("request was not proxied after preview route ensure")
	}
}

func TestHandlerReportsPreviewRouteNotReady(t *testing.T) {
	h := NewHandler(
		&fakeInstanceGetter{instance: runnerInstance(testNamespace)},
		&fakeContractGetter{contract: sandboxRunnerContract()},
		&fakeRuntime{host: "http://unused"},
		WithPreviewRouteManager(&fakePreviewRouteManager{err: previewRouteNotReady("waiting for ResolvedRefs")}),
	)
	rec := doRequest(h, http.MethodGet, dataplaneURL("proxy"))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
}

func TestHandlerFailsClosedWhenPreviewRouteValidationFails(t *testing.T) {
	h := NewHandler(
		&fakeInstanceGetter{instance: runnerInstance(testNamespace)},
		&fakeContractGetter{contract: sandboxRunnerContract()},
		&fakeRuntime{host: "http://unused"},
		WithPreviewRouteManager(&fakePreviewRouteManager{err: errors.New("unexpected backend")}),
	)
	rec := doRequest(h, http.MethodGet, dataplaneURL("proxy"))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
}

func TestHandlerStatusVerbServedFromCR(t *testing.T) {
	h := newTestHandler(t, &fakeInstanceGetter{instance: runnerInstance(testNamespace)}, &fakeRuntime{host: "http://unused"})
	rec := doRequest(h, http.MethodGet, dataplaneURL("status"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "runtimeNamespace") {
		t.Errorf("status body missing runtimeNamespace: %s", rec.Body.String())
	}
}

func TestHandlerRejectsMissingToken(t *testing.T) {
	h := newTestHandler(t, &fakeInstanceGetter{instance: runnerInstance(testNamespace)}, &fakeRuntime{host: "http://unused"})
	req := httptest.NewRequest(http.MethodGet, dataplaneURL("log"), nil) // no Authorization
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestHandlerForwardsAuthzDenial(t *testing.T) {
	denied := apierrors.NewForbidden(schema.GroupResource{Group: "infrastructure.kedge.faros.sh", Resource: "sandboxrunners"}, testNamespace, nil)
	h := newTestHandler(t, &fakeInstanceGetter{err: denied}, &fakeRuntime{host: "http://unused"})
	rec := doRequest(h, http.MethodGet, dataplaneURL("log"))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
}

func TestHandlerForwardsNotFound(t *testing.T) {
	missing := apierrors.NewNotFound(schema.GroupResource{Group: "infrastructure.kedge.faros.sh", Resource: "sandboxrunners"}, testNamespace)
	h := newTestHandler(t, &fakeInstanceGetter{err: missing}, &fakeRuntime{host: "http://unused"})
	rec := doRequest(h, http.MethodGet, dataplaneURL("log"))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestHandlerMethodNotAllowed(t *testing.T) {
	h := newTestHandler(t, &fakeInstanceGetter{instance: runnerInstance(testNamespace)}, &fakeRuntime{host: "http://unused"})
	rec := doRequest(h, http.MethodPost, dataplaneURL("log")) // log is GET-only
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
}

func TestHandlerNamespaceEscapeIsConflict(t *testing.T) {
	instance := runnerInstance(testNamespace)
	unstructured.SetNestedField(instance.Object, "kube-system", "status", "controlServiceRef", "namespace") //nolint:errcheck
	h := newTestHandler(t, &fakeInstanceGetter{instance: instance}, &fakeRuntime{host: "http://unused"})
	rec := doRequest(h, http.MethodGet, dataplaneURL("log"))
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rec.Code)
	}
}

func TestHandlerUnknownVerb(t *testing.T) {
	h := newTestHandler(t, &fakeInstanceGetter{instance: runnerInstance(testNamespace)}, &fakeRuntime{host: "http://unused"})
	rec := doRequest(h, http.MethodGet, dataplaneURL("exec"))
	// exec is undeclared: MethodAllowed returns false => 405.
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
}

func TestHandlerUnavailableWhenDepsNil(t *testing.T) {
	h := NewHandler(nil, nil, nil)
	rec := doRequest(h, http.MethodGet, dataplaneURL("log"))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
}

func TestParsePath(t *testing.T) {
	for _, tc := range []struct {
		path string
		want request
		ok   bool
	}{
		{
			path: PathPrefix + "clusters/root:kedge:orgs:acme/sandboxrunners/r1/log",
			want: request{workspace: "root:kedge:orgs:acme", resource: "sandboxrunners", name: "r1", verb: "log"},
			ok:   true,
		},
		{
			path: PathPrefix + "clusters/ws/sandboxrunners/r1/proxy/assets/app.js",
			want: request{workspace: "ws", resource: "sandboxrunners", name: "r1", verb: "proxy", callerPath: "/assets/app.js"},
			ok:   true,
		},
		{path: PathPrefix + "clusters/ws/sandboxrunners/r1", ok: false},   // no verb
		{path: "/other/clusters/ws/sandboxrunners/r1/log", ok: false},     // wrong prefix
		{path: PathPrefix + "clusters//sandboxrunners/r1/log", ok: false}, // empty ws
	} {
		got, ok := parsePath(tc.path)
		if ok != tc.ok {
			t.Errorf("parsePath(%q) ok = %v, want %v", tc.path, ok, tc.ok)
			continue
		}
		if ok && got != tc.want {
			t.Errorf("parsePath(%q) = %+v, want %+v", tc.path, got, tc.want)
		}
	}
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
