/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/mux"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	k8sschema "k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic/fake"
	kubernetesfake "k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"

	aiv1alpha1 "github.com/faroshq/provider-app-studio/apis/ai/v1alpha1"
	asclient "github.com/faroshq/provider-app-studio/client"
	"github.com/faroshq/provider-app-studio/store"
	"github.com/faroshq/provider-app-studio/workspace"
)

func TestDefaultProjectDevelopmentEnvironmentUsesInfrastructureBackedSandboxRunner(t *testing.T) {
	env := defaultProjectDevelopmentEnvironment("todo")
	if got, want := env.Name, "development"; got != want {
		t.Fatalf("Name = %q, want %q", got, want)
	}
	if got, want := env.Mode, aiv1alpha1.ProjectEnvironmentModeLive; got != want {
		t.Fatalf("Mode = %q, want %q", got, want)
	}
	if got := len(env.Bindings); got != 1 {
		t.Fatalf("bindings = %d, want 1", got)
	}
	binding := env.Bindings[0]
	if got, want := binding.Provider, "app-studio"; got != want {
		t.Fatalf("Provider = %q, want %q", got, want)
	}
	if got, want := binding.ResourceRef.APIVersion, "infrastructure.kedge.faros.sh/v1alpha1"; got != want {
		t.Fatalf("APIVersion = %q, want %q", got, want)
	}
	if got, want := binding.ResourceRef.Kind, "SandboxRunner"; got != want {
		t.Fatalf("Kind = %q, want %q", got, want)
	}
	if got, want := binding.ResourceRef.Resource, "sandboxrunners"; got != want {
		t.Fatalf("Resource = %q, want %q", got, want)
	}
	if got := binding.ResourceRef.Name; got != "" {
		t.Fatalf("ResourceRef.Name = %q, want empty derived name", got)
	}
	var values map[string]any
	if err := json.Unmarshal(binding.Values.Raw, &values); err != nil {
		t.Fatalf("unmarshal binding values: %v", err)
	}
	if _, ok := values["runtime"]; ok {
		t.Fatalf("binding values should not expose sandbox runtime defaults: %#v", values)
	}
	if _, ok := values["name"]; ok {
		t.Fatalf("binding values should not expose a concrete sandbox runner name: %#v", values)
	}
	if got, want := values["projectRef"], "todo"; got != want {
		t.Fatalf("binding values projectRef = %q, want %q", got, want)
	}
}

func TestRewritePreviewJavaScriptRootURLsRewritesRootAbsoluteStringConstants(t *testing.T) {
	basePath := "/services/providers/app-studio/api/projects/simply-done/preview/__kedge_preview/abc123/"
	raw := []byte(`const API_URL = '/api/todos';
fetch(API_URL);`)

	got := string(rewritePreviewResponseBody("application/javascript", basePath, raw))
	want := `const API_URL = '/services/providers/app-studio/api/projects/simply-done/preview/__kedge_preview/abc123/api/todos';
fetch(API_URL);`
	if got != want {
		t.Fatalf("rewritten JavaScript = %q, want %q", got, want)
	}
}

func TestRewritePreviewHTMLInjectsCredentialedPreviewFetchShim(t *testing.T) {
	basePath := "/services/providers/app-studio/api/projects/simply-done/preview/__kedge_preview/abc123/"
	raw := []byte(`<html><head><script src="/assets/app.js"></script></head><body></body></html>`)

	got := string(rewritePreviewResponseBody("text/html", basePath, raw))
	for _, want := range []string{
		`<script data-kedge-preview-credentials>`,
		`credentials = 'include'`,
		`NativeXHR.prototype.open`,
		`this.withCredentials = true`,
		`<script src="` + basePath + `assets/app.js"></script>`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("rewritten HTML missing %q: %s", want, got)
		}
	}
}

func TestProjectAssistantRuntimePreviewURLPrefersDevelopment(t *testing.T) {
	p := &aiv1alpha1.Project{
		ObjectMeta: metav1.ObjectMeta{Name: "todo"},
		Status: aiv1alpha1.ProjectStatus{
			Environments: []aiv1alpha1.ProjectEnvironmentStatus{
				{
					Name: "test",
					Bindings: []aiv1alpha1.ProjectProviderBindingStatus{{
						Name:       "web",
						PreviewURL: "/test",
					}},
				},
				{
					Name: "development",
					Mode: aiv1alpha1.ProjectEnvironmentModeLive,
					Bindings: []aiv1alpha1.ProjectProviderBindingStatus{{
						Name:       "dev",
						Provider:   "app-studio",
						PreviewURL: "/dev",
					}},
				},
			},
		},
	}
	if got, want := projectAssistantRuntimePreviewURL(p), "/dev"; got != want {
		t.Fatalf("preview URL = %q, want %q", got, want)
	}
}

func TestCreateProjectSpecIncludesDevelopmentEnvironment(t *testing.T) {
	spec := defaultProjectSpec("todo", "Todo", "Tasks", &aiv1alpha1.ProjectRepositoryBinding{RepositoryRef: "todo"})
	if got := len(spec.Environments); got != 1 {
		t.Fatalf("environments = %d, want 1", got)
	}
	if got, want := spec.Environments[0].Name, "development"; got != want {
		t.Fatalf("environment name = %q, want %q", got, want)
	}
}

func TestProjectDevelopmentSyncTargetReadsSandboxBindingName(t *testing.T) {
	p := &aiv1alpha1.Project{
		ObjectMeta: metav1.ObjectMeta{Name: "todo"},
		Spec: aiv1alpha1.ProjectSpec{
			Environments: []aiv1alpha1.ProjectEnvironmentSpec{defaultProjectDevelopmentEnvironment("todo")},
		},
	}
	id := identity{tenantPath: "root:kedge:tenants:org-a:ws-1"}
	target, ok := projectDevelopmentSyncTarget(p, id)
	if !ok {
		t.Fatal("projectDevelopmentSyncTarget returned !ok")
	}
	if got, want := target.Provider, "app-studio"; got != want {
		t.Fatalf("Provider = %q, want %q", got, want)
	}
	if got, want := target.EnvironmentName, "development"; got != want {
		t.Fatalf("EnvironmentName = %q, want %q", got, want)
	}
	if got, want := target.BindingName, "dev"; got != want {
		t.Fatalf("BindingName = %q, want %q", got, want)
	}
	if got, want := target.ResourceName, sandboxRunnerResourceName(id.tenantPath, "todo"); got != want {
		t.Fatalf("ResourceName = %q, want %q", got, want)
	}
}

func TestProjectAssistantPreviewRefreshNeededUsesSuccessfulMutatingToolCalls(t *testing.T) {
	server := NewWithWorkspace(nil, nil, nil, "http://hub.example", false)
	if !server.projectAssistantPreviewRefreshNeeded(context.Background(), workspace.Scope{}, "", false, []projectToolCallStreamEvent{{
		Name:   projectToolWriteFile,
		Status: "succeeded",
	}}) {
		t.Fatal("preview refresh = false, want true after successful workspace mutation")
	}
	if server.projectAssistantPreviewRefreshNeeded(context.Background(), workspace.Scope{}, "", false, []projectToolCallStreamEvent{{
		Name:   projectToolWriteFile,
		Status: "failed",
	}}) {
		t.Fatal("preview refresh = true, want false after failed workspace mutation")
	}
	if server.projectAssistantPreviewRefreshNeeded(context.Background(), workspace.Scope{}, "", false, []projectToolCallStreamEvent{{
		Name:   projectToolReadProjectFile,
		Status: "succeeded",
	}}) {
		t.Fatal("preview refresh = true, want false after read-only tool")
	}
}

func TestSyncProjectDevelopmentTargetPostsWorkspaceFilesToRuntime(t *testing.T) {
	var gotControlToken string
	var gotFiles []map[string]string
	id := identity{tenantPath: "root:kedge:tenants:org-a:ws-1", orgUUID: "org-a", workspaceUUID: "ws-1", token: "caller-token"}
	runnerName := sandboxRunnerResourceName(id.tenantPath, "todo")
	runtimeAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.URL.Path, "/api/v1/namespaces/"+runnerName+"/services/"+runnerName+":control/proxy/sync"; got != want {
			t.Fatalf("path = %q, want %q", got, want)
		}
		gotControlToken = r.Header.Get("X-Sandbox-Control-Token")
		var body struct {
			Files   []map[string]string `json:"files"`
			Restart string              `json:"restart"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode sync request: %v", err)
		}
		if got, want := body.Restart, "auto"; got != want {
			t.Fatalf("restart = %q, want %q", got, want)
		}
		gotFiles = body.Files
		fmt.Fprint(w, `{"phase":"Synced","changed":["src/App.tsx"]}`)
	}))
	defer runtimeAPI.Close()

	workspaces := workspace.NewFileStore(t.TempDir())
	project := &aiv1alpha1.Project{
		ObjectMeta: metav1.ObjectMeta{Name: "todo"},
		Spec: aiv1alpha1.ProjectSpec{
			Environments: []aiv1alpha1.ProjectEnvironmentSpec{defaultProjectDevelopmentEnvironment("todo")},
		},
	}
	scope := projectWorkspaceScope(id, project.Name)
	if err := workspaces.ApplyFiles(context.Background(), scope, []workspace.File{{Path: "src/App.tsx", Content: "hello\n"}}); err != nil {
		t.Fatalf("ApplyFiles returned error: %v", err)
	}

	client := asclient.NewFromDynamic(fake.NewSimpleDynamicClient(runtime.NewScheme(), testSandboxRunner(runnerName, project.Name)))
	server := NewWithWorkspace(nil, nil, workspaces, "http://hub.example", false)
	server.runtimeConfig = &rest.Config{Host: runtimeAPI.URL}
	server.runtimeClient = kubernetesfake.NewSimpleClientset(testRuntimeControlSecret(runnerName, "runtime-token"))
	server.previewSigner = newPreviewSigner([]byte("test-secret"))
	target, ok := projectDevelopmentSyncTarget(project, id)
	if !ok {
		t.Fatal("projectDevelopmentSyncTarget returned !ok")
	}
	result, err := server.syncProjectDevelopmentTarget(context.Background(), client, id, project, target)
	if err != nil {
		t.Fatalf("syncProjectDevelopmentTarget returned error: %v", err)
	}
	if got, want := gotControlToken, "runtime-token"; got != want {
		t.Fatalf("X-Sandbox-Control-Token = %q, want %q", got, want)
	}
	if len(gotFiles) != 1 || gotFiles[0]["path"] != "src/App.tsx" || gotFiles[0]["content"] != "hello\n" {
		t.Fatalf("files = %#v, want src/App.tsx content", gotFiles)
	}
	var decoded struct {
		PreviewURL            string `json:"previewURL"`
		PreviewTokenExpiresAt string `json:"previewTokenExpiresAt"`
	}
	if err := json.Unmarshal(result, &decoded); err != nil {
		t.Fatalf("decode sync result: %v", err)
	}
	if !strings.HasPrefix(decoded.PreviewURL, "/services/providers/app-studio/api/projects/todo/preview/?kedgePreviewToken=") {
		t.Fatalf("previewURL = %q, want app-studio project preview URL", decoded.PreviewURL)
	}
	if decoded.PreviewTokenExpiresAt == "" {
		t.Fatal("previewTokenExpiresAt is empty, want signed token expiry")
	}
}

func TestAuthorizeProjectDevelopmentPreviewTargetGetsSignedAppStudioURL(t *testing.T) {
	id := identity{tenantPath: "root:kedge:tenants:org-a:ws-1", orgUUID: "org-a", workspaceUUID: "ws-1", token: "caller-token"}
	runnerName := sandboxRunnerResourceName(id.tenantPath, "todo")
	project := &aiv1alpha1.Project{ObjectMeta: metav1.ObjectMeta{Name: "todo"}}
	client := asclient.NewFromDynamic(fake.NewSimpleDynamicClient(runtime.NewScheme(), testSandboxRunner(runnerName, project.Name)))
	server := NewWithWorkspace(nil, nil, nil, "http://hub.example", false)
	server.runtimeClient = kubernetesfake.NewSimpleClientset(testReadyPreviewEndpoints(runnerName))
	server.previewSigner = newPreviewSigner([]byte("test-secret"))
	got, err := server.authorizeProjectDevelopmentPreviewTarget(context.Background(), client, id, project, projectDevelopmentSyncTargetInfo{ResourceName: runnerName})
	if err != nil {
		t.Fatalf("authorizeProjectDevelopmentPreviewTarget returned error: %v", err)
	}
	if !got.Ready {
		t.Fatalf("ready = false, want true: %#v", got)
	}
	if !strings.HasPrefix(got.PreviewURL, "/services/providers/app-studio/api/projects/todo/preview/?kedgePreviewToken=") {
		t.Fatalf("previewURL = %q, want app-studio project preview URL", got.PreviewURL)
	}
	if got.PreviewTokenExpiresAt == "" {
		t.Fatal("PreviewTokenExpiresAt is empty, want signed token expiry")
	}
	parsed, err := url.Parse(got.PreviewURL)
	if err != nil {
		t.Fatalf("parse preview URL: %v", err)
	}
	payload, err := server.previewSigner.verify(parsed.Query().Get(previewTokenQuery), project.Name)
	if err != nil {
		t.Fatalf("verify preview token: %v", err)
	}
	if got, want := payload.SandboxRunner, runnerName; got != want {
		t.Fatalf("token SandboxRunner = %q, want %q", got, want)
	}
	expiresAt, err := time.Parse(time.RFC3339, got.PreviewTokenExpiresAt)
	if err != nil {
		t.Fatalf("parse PreviewTokenExpiresAt: %v", err)
	}
	if got, want := expiresAt.Unix(), payload.ExpiresAt; got != want {
		t.Fatalf("PreviewTokenExpiresAt unix = %d, want %d", got, want)
	}
}

func TestAuthorizeProjectDevelopmentPreviewTargetReturnsNotReady(t *testing.T) {
	id := identity{tenantPath: "root:kedge:tenants:org-a:ws-1", token: "caller-token"}
	runnerName := sandboxRunnerResourceName(id.tenantPath, "todo")
	project := &aiv1alpha1.Project{ObjectMeta: metav1.ObjectMeta{Name: "todo"}}
	client := asclient.NewFromDynamic(fake.NewSimpleDynamicClient(runtime.NewScheme(), testSandboxRunner(runnerName, project.Name)))
	server := NewWithWorkspace(nil, nil, nil, "http://hub.example", false)
	server.runtimeClient = kubernetesfake.NewSimpleClientset()
	server.previewSigner = newPreviewSigner([]byte("test-secret"))
	got, err := server.authorizeProjectDevelopmentPreviewTarget(context.Background(), client, id, project, projectDevelopmentSyncTargetInfo{ResourceName: runnerName})
	if err != nil {
		t.Fatalf("authorizeProjectDevelopmentPreviewTarget returned error: %v", err)
	}
	if got.Ready {
		t.Fatalf("ready = true, want false: %#v", got)
	}
	if got.PreviewURL != "" {
		t.Fatalf("previewURL = %q, want empty while not ready", got.PreviewURL)
	}
	if got.Reason != "service_not_found" {
		t.Fatalf("reason = %q, want service_not_found", got.Reason)
	}
}

func TestPreviewProjectDevelopmentRendersStartingPageForRuntimeServiceUnavailable(t *testing.T) {
	runtimeAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.URL.Path, "/api/v1/namespaces/runtime-ns/services/runtime-svc:preview/proxy/"; got != want {
			t.Fatalf("runtime proxy path = %q, want %q", got, want)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprint(w, `{"kind":"Status","apiVersion":"v1","status":"Failure","message":"error trying to reach service: dial tcp 10.244.0.55:3000: connect: connection refused","reason":"ServiceUnavailable","code":503}`)
	}))
	defer runtimeAPI.Close()

	server := NewWithWorkspace(nil, nil, nil, "http://hub.example", false)
	server.runtimeConfig = &rest.Config{Host: runtimeAPI.URL}
	server.previewSigner = newPreviewSigner([]byte("test-secret"))
	token, _, err := server.previewSigner.sign(previewTokenPayload{
		TenantPath:         "root:kedge:tenants:org-a:ws-1",
		Project:            "todo",
		RuntimeNamespace:   "runtime-ns",
		PreviewServiceName: "runtime-svc",
		PreviewPortName:    "preview",
		SandboxRunner:      "runtime-svc",
	})
	if err != nil {
		t.Fatalf("sign preview token: %v", err)
	}
	scope := previewTokenScope(token)
	req := httptest.NewRequest(http.MethodGet, "/api/projects/todo/preview/"+previewScopePrefix+"/"+scope+"/", nil)
	req.AddCookie(&http.Cookie{Name: previewCookieName("todo", scope), Value: token})
	resp := httptest.NewRecorder()
	router := mux.NewRouter()
	server.Register(router)

	router.ServeHTTP(resp, req)

	if got, want := resp.Code, http.StatusServiceUnavailable; got != want {
		t.Fatalf("status = %d, want %d", got, want)
	}
	body := resp.Body.String()
	if !strings.Contains(resp.Header().Get("Content-Type"), "text/html") {
		t.Fatalf("Content-Type = %q, want text/html", resp.Header().Get("Content-Type"))
	}
	if !strings.Contains(body, "Preview is starting") {
		t.Fatalf("body = %q, want friendly preview starting page", body)
	}
	if strings.Contains(body, "dial tcp") || strings.Contains(body, `"kind":"Status"`) {
		t.Fatalf("body leaked raw Kubernetes proxy error: %s", body)
	}
}

func TestPreviewTokenRedirectSetsSecureCookie(t *testing.T) {
	server := NewWithWorkspace(nil, nil, nil, "http://hub.example", false)
	server.previewSigner = newPreviewSigner([]byte("test-secret"))
	token, _, err := server.previewSigner.sign(previewTokenPayload{
		TenantPath:         "root:kedge:tenants:org-a:ws-1",
		Project:            "todo",
		RuntimeNamespace:   "runtime-ns",
		PreviewServiceName: "runtime-svc",
		PreviewPortName:    "preview",
		SandboxRunner:      "runtime-svc",
	})
	if err != nil {
		t.Fatalf("sign preview token: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "https://kedge.example.test/api/projects/todo/preview/?"+previewTokenQuery+"="+url.QueryEscape(token), nil)
	resp := httptest.NewRecorder()
	router := mux.NewRouter()
	server.Register(router)

	router.ServeHTTP(resp, req)

	if got, want := resp.Code, http.StatusFound; got != want {
		t.Fatalf("status = %d, want %d", got, want)
	}
	cookies := resp.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("cookies = %d, want 1", len(cookies))
	}
	if !cookies[0].Secure {
		t.Fatalf("preview token cookie Secure = false, want true")
	}
	if !cookies[0].HttpOnly {
		t.Fatalf("preview token cookie HttpOnly = false, want true")
	}
	if got, want := cookies[0].SameSite, http.SameSiteNoneMode; got != want {
		t.Fatalf("preview token cookie SameSite = %v, want %v", got, want)
	}
}

func TestPreviewProjectDevelopmentRejectsScopedRequestWithoutCookieAfterTokenRedirect(t *testing.T) {
	runtimeAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("runtime proxy should not be reached without preview cookie; path = %q", r.URL.Path)
	}))
	defer runtimeAPI.Close()

	server := NewWithWorkspace(nil, nil, nil, "http://hub.example", false)
	server.runtimeConfig = &rest.Config{Host: runtimeAPI.URL}
	server.previewSigner = newPreviewSigner([]byte("test-secret"))
	token, _, err := server.previewSigner.sign(previewTokenPayload{
		TenantPath:         "root:kedge:tenants:org-a:ws-1",
		Project:            "todo",
		RuntimeNamespace:   "runtime-ns",
		PreviewServiceName: "runtime-svc",
		PreviewPortName:    "preview",
		SandboxRunner:      "runtime-svc",
	})
	if err != nil {
		t.Fatalf("sign preview token: %v", err)
	}
	router := mux.NewRouter()
	server.Register(router)

	authReq := httptest.NewRequest(http.MethodGet, "https://kedge.example.test/api/projects/todo/preview/?"+previewTokenQuery+"="+url.QueryEscape(token), nil)
	authResp := httptest.NewRecorder()
	router.ServeHTTP(authResp, authReq)
	if got, want := authResp.Code, http.StatusFound; got != want {
		t.Fatalf("authorization status = %d, want %d", got, want)
	}

	scope := previewTokenScope(token)
	req := httptest.NewRequest(http.MethodPost, "/api/projects/todo/preview/"+previewScopePrefix+"/"+scope+"/api/todos", strings.NewReader(`{"title":"Buy milk"}`))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()

	router.ServeHTTP(resp, req)

	if got, want := resp.Code, http.StatusUnauthorized; got != want {
		t.Fatalf("status = %d, want %d; body = %s", got, want, resp.Body.String())
	}
}

func TestPreviewProjectDevelopmentSupportsSandboxedCORSScopeRequests(t *testing.T) {
	var runtimeCalls atomic.Int32
	runtimeAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		runtimeCalls.Add(1)
		if got, want := r.Method, http.MethodGet; got != want {
			t.Fatalf("runtime method = %q, want %q", got, want)
		}
		if got, want := r.URL.Path, "/api/v1/namespaces/runtime-ns/services/runtime-svc:preview/proxy/api/todos"; got != want {
			t.Fatalf("runtime proxy path = %q, want %q", got, want)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `[]`)
	}))
	defer runtimeAPI.Close()

	server := NewWithWorkspace(nil, nil, nil, "http://hub.example", false)
	server.runtimeConfig = &rest.Config{Host: runtimeAPI.URL}
	server.previewSigner = newPreviewSigner([]byte("test-secret"))
	token, _, err := server.previewSigner.sign(previewTokenPayload{
		TenantPath:         "root:kedge:tenants:org-a:ws-1",
		Project:            "todo",
		RuntimeNamespace:   "runtime-ns",
		PreviewServiceName: "runtime-svc",
		PreviewPortName:    "preview",
		SandboxRunner:      "runtime-svc",
	})
	if err != nil {
		t.Fatalf("sign preview token: %v", err)
	}
	router := mux.NewRouter()
	server.Register(router)
	authReq := httptest.NewRequest(http.MethodGet, "https://kedge.example.test/api/projects/todo/preview/?"+previewTokenQuery+"="+url.QueryEscape(token), nil)
	authResp := httptest.NewRecorder()
	router.ServeHTTP(authResp, authReq)
	if got, want := authResp.Code, http.StatusFound; got != want {
		t.Fatalf("authorization status = %d, want %d", got, want)
	}

	scope := previewTokenScope(token)
	scopedPath := "/api/projects/todo/preview/" + previewScopePrefix + "/" + scope + "/api/todos"
	getReq := httptest.NewRequest(http.MethodGet, scopedPath, nil)
	getReq.AddCookie(authResp.Result().Cookies()[0])
	getReq.Header.Set("Origin", "null")
	getResp := httptest.NewRecorder()
	router.ServeHTTP(getResp, getReq)
	if got, want := getResp.Code, http.StatusOK; got != want {
		t.Fatalf("GET status = %d, want %d; body = %s", got, want, getResp.Body.String())
	}
	if got, want := getResp.Header().Get("Access-Control-Allow-Origin"), "null"; got != want {
		t.Fatalf("GET Access-Control-Allow-Origin = %q, want %q", got, want)
	}
	if got, want := getResp.Header().Get("Access-Control-Allow-Credentials"), "true"; got != want {
		t.Fatalf("GET Access-Control-Allow-Credentials = %q, want %q", got, want)
	}
	if got, want := len(getResp.Result().Header.Values("Access-Control-Allow-Origin")), 1; got != want {
		t.Fatalf("GET Access-Control-Allow-Origin header count = %d, want %d", got, want)
	}

	preflightReq := httptest.NewRequest(http.MethodOptions, scopedPath, nil)
	preflightReq.Header.Set("Origin", "null")
	preflightReq.Header.Set("Access-Control-Request-Method", http.MethodPost)
	preflightReq.Header.Set("Access-Control-Request-Headers", "content-type")
	preflightResp := httptest.NewRecorder()
	router.ServeHTTP(preflightResp, preflightReq)
	if got, want := preflightResp.Code, http.StatusNoContent; got != want {
		t.Fatalf("OPTIONS status = %d, want %d; body = %s", got, want, preflightResp.Body.String())
	}
	if got, want := preflightResp.Header().Get("Access-Control-Allow-Origin"), "null"; got != want {
		t.Fatalf("OPTIONS Access-Control-Allow-Origin = %q, want %q", got, want)
	}
	if got := strings.ToLower(preflightResp.Header().Get("Access-Control-Allow-Headers")); got != "content-type" {
		t.Fatalf("OPTIONS Access-Control-Allow-Headers = %q, want content-type", got)
	}
	if got := preflightResp.Header().Get("Access-Control-Allow-Methods"); !strings.Contains(got, http.MethodPost) {
		t.Fatalf("OPTIONS Access-Control-Allow-Methods = %q, want POST", got)
	}
	if got, want := preflightResp.Header().Get("Access-Control-Allow-Credentials"), "true"; got != want {
		t.Fatalf("OPTIONS Access-Control-Allow-Credentials = %q, want %q", got, want)
	}
	if got, want := runtimeCalls.Load(), int32(1); got != want {
		t.Fatalf("runtime calls = %d, want %d", got, want)
	}
}

func TestReadPreviewRewriteBodyRejectsOversizedResponses(t *testing.T) {
	body := strings.NewReader(strings.Repeat("x", previewRewriteBodyLimit+1))
	if _, err := readPreviewRewriteBody(body); err == nil {
		t.Fatal("readPreviewRewriteBody returned nil error for oversized body")
	}
}

func TestRuntimeTargetRejectsStatusRefsOutsideExpectedSandboxRunner(t *testing.T) {
	runnerName := "kedge-sandbox-1234567890abcdef"
	obj := testSandboxRunner(runnerName, "todo")
	if err := unstructured.SetNestedStringMap(obj.Object, map[string]string{
		"namespace": "kube-system",
		"name":      "stolen",
	}, "status", "controlSecretRef"); err != nil {
		t.Fatalf("set controlSecretRef: %v", err)
	}
	if _, err := runtimeTargetFromInstance(obj); err == nil {
		t.Fatal("runtimeTargetFromInstance returned nil error for forged status refs")
	}
}

func TestRuntimeTargetAcceptsKROPrefixedRuntimeNamespace(t *testing.T) {
	runnerName := "kedge-sandbox-1234567890abcdef"
	clusterID := "1z5cyn8ghmwpxk8v"
	runtimeNamespace := clusterID + "-" + runnerName
	obj := testSandboxRunner(runnerName, "todo")
	obj.SetAnnotations(map[string]string{"kcp.io/cluster": clusterID})
	if err := unstructured.SetNestedField(obj.Object, runtimeNamespace, "status", "runtimeNamespace"); err != nil {
		t.Fatalf("set runtimeNamespace: %v", err)
	}
	for _, field := range []string{"previewServiceRef", "controlServiceRef", "controlSecretRef"} {
		if err := unstructured.SetNestedField(obj.Object, runtimeNamespace, "status", field, "namespace"); err != nil {
			t.Fatalf("set %s namespace: %v", field, err)
		}
	}

	target, err := runtimeTargetFromInstance(obj)
	if err != nil {
		t.Fatalf("runtimeTargetFromInstance returned error: %v", err)
	}
	if got, want := target.Preview.Namespace, runtimeNamespace; got != want {
		t.Fatalf("Preview.Namespace = %q, want %q", got, want)
	}
	if got, want := target.Control.Namespace, runtimeNamespace; got != want {
		t.Fatalf("Control.Namespace = %q, want %q", got, want)
	}
	if got, want := target.ControlSecret.Namespace, runtimeNamespace; got != want {
		t.Fatalf("ControlSecret.Namespace = %q, want %q", got, want)
	}
}

func TestReconcileProjectLiveBindingsCreatesInfrastructureSandboxRunner(t *testing.T) {
	client := asclient.NewFromDynamic(fake.NewSimpleDynamicClient(runtime.NewScheme(), &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "ai.kedge.faros.sh/v1alpha1",
			"kind":       "Project",
			"metadata": map[string]any{
				"name": "todo",
			},
			"spec": map[string]any{
				"displayName": "Todo",
				"environments": []any{map[string]any{
					"name": "development",
					"mode": "live",
					"bindings": []any{map[string]any{
						"name":     "dev",
						"provider": "app-studio",
						"kind":     "providerResource",
						"resourceRef": map[string]any{
							"apiVersion": "infrastructure.kedge.faros.sh/v1alpha1",
							"kind":       "SandboxRunner",
							"resource":   "sandboxrunners",
						},
						"values": map[string]any{
							"projectRef": "todo",
						},
					}},
				}},
			},
		},
	}))
	project := &aiv1alpha1.Project{
		ObjectMeta: metav1.ObjectMeta{Name: "todo"},
		Spec:       defaultProjectSpec("todo", "Todo", "", nil),
	}
	server := NewWithWorkspace(nil, nil, nil, "http://hub.example", false)
	id := identity{tenantPath: "root:kedge:tenants:org-a:ws-1"}
	runnerName := sandboxRunnerResourceName(id.tenantPath, "todo")
	if _, err := server.reconcileProjectLiveBindings(context.Background(), client, project, id); err != nil {
		t.Fatalf("reconcileProjectLiveBindings returned error: %v", err)
	}
	obj, err := client.Dynamic().Resource(infrastructureSandboxRunnerGVR()).Get(context.Background(), runnerName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get SandboxRunner returned error: %v", err)
	}
	if got, _, _ := unstructured.NestedString(obj.Object, "spec", "name"); got != runnerName {
		t.Fatalf("spec.name = %q, want %q", got, runnerName)
	}
	if got, _, _ := unstructured.NestedString(obj.Object, "spec", "projectRef"); got != "todo" {
		t.Fatalf("spec.projectRef = %q, want todo", got)
	}
	if got, _, _ := unstructured.NestedString(obj.Object, "spec", "runnerImage"); got != sandboxRunnerImage() {
		t.Fatalf("runnerImage = %q, want %q", got, sandboxRunnerImage())
	}
	if got, _, _ := unstructured.NestedString(obj.Object, "spec", "tokenGeneratorImage"); got != sandboxTokenGeneratorImage() {
		t.Fatalf("tokenGeneratorImage = %q, want %q", got, sandboxTokenGeneratorImage())
	}
}

func TestDeleteProjectProviderResourcesRemovesInfrastructureSandboxRunner(t *testing.T) {
	project := &aiv1alpha1.Project{
		ObjectMeta: metav1.ObjectMeta{Name: "todo"},
		Spec:       defaultProjectSpec("todo", "Todo", "", nil),
	}
	id := identity{tenantPath: "root:kedge:tenants:org-a:ws-1"}
	runnerName := sandboxRunnerResourceName(id.tenantPath, "todo")
	devEnv := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "infrastructure.kedge.faros.sh/v1alpha1",
		"kind":       "SandboxRunner",
		"metadata": map[string]any{
			"name": runnerName,
		},
	}}
	client := asclient.NewFromDynamic(fake.NewSimpleDynamicClient(runtime.NewScheme(), devEnv))
	server := NewWithWorkspace(nil, nil, nil, "http://hub.example", false)
	if err := server.deleteProjectProviderResources(context.Background(), client, project, id); err != nil {
		t.Fatalf("deleteProjectProviderResources returned error: %v", err)
	}
	if _, err := client.Dynamic().Resource(infrastructureSandboxRunnerGVR()).Get(context.Background(), runnerName, metav1.GetOptions{}); err == nil {
		t.Fatal("infrastructure SandboxRunner still exists after project provider cleanup")
	}
}

func TestDeleteProjectProviderResourcesRemovesSandboxRuntimeNamespace(t *testing.T) {
	project := &aiv1alpha1.Project{
		ObjectMeta: metav1.ObjectMeta{Name: "todo"},
		Spec:       defaultProjectSpec("todo", "Todo", "", nil),
	}
	id := identity{tenantPath: "root:kedge:tenants:org-a:ws-1"}
	runnerName := sandboxRunnerResourceName(id.tenantPath, "todo")
	runtimeNamespace := "cluster-a-" + runnerName
	runner := testSandboxRunner(runnerName, "todo")
	runner.SetAnnotations(map[string]string{kcpClusterAnnotation: "cluster-a"})
	if err := unstructured.SetNestedField(runner.Object, runtimeNamespace, "status", "runtimeNamespace"); err != nil {
		t.Fatalf("set runtime namespace: %v", err)
	}
	for _, field := range []string{"previewServiceRef", "controlServiceRef", "controlSecretRef"} {
		if err := unstructured.SetNestedField(runner.Object, runtimeNamespace, "status", field, "namespace"); err != nil {
			t.Fatalf("set %s namespace: %v", field, err)
		}
	}
	client := asclient.NewFromDynamic(fake.NewSimpleDynamicClient(runtime.NewScheme(), runner))
	server := NewWithWorkspace(nil, nil, nil, "http://hub.example", false)
	server.runtimeClient = kubernetesfake.NewSimpleClientset(&corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: runtimeNamespace},
	})

	if err := server.deleteProjectProviderResources(context.Background(), client, project, id); err != nil {
		t.Fatalf("deleteProjectProviderResources returned error: %v", err)
	}
	if _, err := client.Dynamic().Resource(infrastructureSandboxRunnerGVR()).Get(context.Background(), runnerName, metav1.GetOptions{}); err == nil {
		t.Fatal("infrastructure SandboxRunner still exists after project provider cleanup")
	}
	if _, err := server.runtimeClient.CoreV1().Namespaces().Get(context.Background(), runtimeNamespace, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("runtime namespace get error = %v, want not found", err)
	}
}

func TestDeleteProjectProviderResourcesRejectsForgedSandboxRuntimeNamespace(t *testing.T) {
	project := &aiv1alpha1.Project{
		ObjectMeta: metav1.ObjectMeta{Name: "todo"},
		Spec:       defaultProjectSpec("todo", "Todo", "", nil),
	}
	id := identity{tenantPath: "root:kedge:tenants:org-a:ws-1"}
	runnerName := sandboxRunnerResourceName(id.tenantPath, "todo")
	runner := testSandboxRunner(runnerName, "todo")
	runner.SetAnnotations(map[string]string{kcpClusterAnnotation: "cluster-a"})
	if err := unstructured.SetNestedField(runner.Object, "kube-system", "status", "runtimeNamespace"); err != nil {
		t.Fatalf("set forged runtime namespace: %v", err)
	}
	client := asclient.NewFromDynamic(fake.NewSimpleDynamicClient(runtime.NewScheme(), runner))
	server := NewWithWorkspace(nil, nil, nil, "http://hub.example", false)
	server.runtimeClient = kubernetesfake.NewSimpleClientset(&corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: "kube-system"},
	})

	if err := server.deleteProjectProviderResources(context.Background(), client, project, id); err == nil {
		t.Fatal("deleteProjectProviderResources returned nil for forged runtime namespace")
	}
	if _, err := server.runtimeClient.CoreV1().Namespaces().Get(context.Background(), "kube-system", metav1.GetOptions{}); err != nil {
		t.Fatalf("forged namespace was deleted or unreadable: %v", err)
	}
}

func TestProjectViewOverlaysSandboxPreviewStatus(t *testing.T) {
	project := &aiv1alpha1.Project{
		ObjectMeta: metav1.ObjectMeta{Name: "todo"},
		Spec:       defaultProjectSpec("todo", "Todo", "", nil),
	}
	id := identity{tenantPath: "root:kedge:tenants:org-a:ws-1"}
	runnerName := sandboxRunnerResourceName(id.tenantPath, "todo")
	devEnv := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "infrastructure.kedge.faros.sh/v1alpha1",
		"kind":       "SandboxRunner",
		"metadata": map[string]any{
			"name": runnerName,
		},
		"status": map[string]any{
			"phase": "Running",
		},
	}}
	client := asclient.NewFromDynamic(fake.NewSimpleDynamicClient(runtime.NewScheme(), devEnv))
	view := projectView(context.Background(), client, project, id)
	if len(view.Environments) != 1 || len(view.Environments[0].Bindings) != 1 {
		t.Fatalf("view environments = %#v, want one development binding", view.Environments)
	}
	binding := view.Environments[0].Bindings[0]
	if got, want := binding.PreviewURL, "/services/providers/app-studio/api/projects/todo/preview/"; got != want {
		t.Fatalf("PreviewURL = %q, want %q", got, want)
	}
	if got, want := view.Environments[0].Phase, "Running"; got != want {
		t.Fatalf("environment phase = %q, want %q", got, want)
	}
}

func TestGenerateProjectAssistantStreamUsesLiveBindingStatusOverlay(t *testing.T) {
	project := &aiv1alpha1.Project{
		ObjectMeta: metav1.ObjectMeta{Name: "todo"},
		Spec:       defaultProjectSpec("todo", "Todo", "", nil),
	}
	id := identity{tenantPath: "root:kedge:tenants:org-a:ws-1", orgUUID: "org-a", workspaceUUID: "ws-1"}
	runnerName := sandboxRunnerResourceName(id.tenantPath, "todo")
	devEnv := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "infrastructure.kedge.faros.sh/v1alpha1",
		"kind":       "SandboxRunner",
		"metadata": map[string]any{
			"name": runnerName,
		},
		"status": map[string]any{
			"phase": "Running",
		},
	}}
	client := asclient.NewFromDynamic(fake.NewSimpleDynamicClient(
		runtime.NewScheme(),
		devEnv,
		projectLLMSettingsSecret(projectLLMSettings{Provider: defaultProjectLLMProvider, BaseURL: "http://llm.example", Model: "test-model", APIKey: "test-key"}),
	))
	engine := &previewOverlayProbeEngine{}
	server := NewWithWorkspace(nil, store.NewMemoryStore(), workspace.NewFileStore(t.TempDir()), "http://hub.example", false)
	server.assistantEngine = engine

	_, err := server.generateProjectAssistantStream(
		httptest.NewRequest(http.MethodPost, "/", nil),
		id,
		client,
		project,
		projectAssistantStreamCallbacks{},
	)
	if err != nil {
		t.Fatalf("generateProjectAssistantStream returned error: %v", err)
	}
	if got, want := engine.previewURL, "/services/providers/app-studio/api/projects/todo/preview/"; got != want {
		t.Fatalf("engine project preview URL = %q, want %q", got, want)
	}
}

func testSandboxRunner(name, project string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "infrastructure.kedge.faros.sh/v1alpha1",
		"kind":       "SandboxRunner",
		"metadata": map[string]any{
			"name": name,
		},
		"spec": map[string]any{
			"name":       name,
			"projectRef": project,
		},
		"status": map[string]any{
			"phase":            "Running",
			"runtimeNamespace": name,
			"previewServiceRef": map[string]any{
				"namespace": name,
				"name":      name,
				"portName":  "preview",
			},
			"controlServiceRef": map[string]any{
				"namespace": name,
				"name":      name,
				"portName":  "control",
			},
			"controlSecretRef": map[string]any{
				"namespace": name,
				"name":      name + "-control",
			},
		},
	}}
}

func testRuntimeControlSecret(name, token string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: name,
			Name:      name + "-control",
		},
		Data: map[string][]byte{"token": []byte(token)},
	}
}

func testReadyPreviewEndpoints(name string) *corev1.Endpoints {
	return &corev1.Endpoints{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: name,
			Name:      name,
		},
		Subsets: []corev1.EndpointSubset{{
			Addresses: []corev1.EndpointAddress{{IP: "10.0.0.10"}},
			Ports:     []corev1.EndpointPort{{Name: "preview", Port: 3000}},
		}},
	}
}

type previewOverlayProbeEngine struct {
	previewURL string
}

func (e *previewOverlayProbeEngine) StreamProjectAssistant(_ context.Context, req projectAssistantRunRequest) (projectAssistantRunResult, error) {
	e.previewURL = projectAssistantRuntimePreviewURL(req.Project)
	return projectAssistantRunResult{Content: "ok"}, nil
}

func (e *previewOverlayProbeEngine) ResumeProjectAssistant(context.Context, projectAssistantRunRequest, projectAssistantResumeRequest, projectAssistantCheckpointState) (projectAssistantRunResult, error) {
	return projectAssistantRunResult{}, fmt.Errorf("unexpected resume")
}

func infrastructureSandboxRunnerGVR() k8sschema.GroupVersionResource {
	return k8sschema.GroupVersionResource{
		Group:    "infrastructure.kedge.faros.sh",
		Version:  "v1alpha1",
		Resource: "sandboxrunners",
	}
}
