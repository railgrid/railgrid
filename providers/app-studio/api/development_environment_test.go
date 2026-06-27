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
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	k8sschema "k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic/fake"

	aiv1alpha1 "github.com/faroshq/provider-app-studio/apis/ai/v1alpha1"
	asclient "github.com/faroshq/provider-app-studio/client"
	"github.com/faroshq/provider-app-studio/previewtoken"
	"github.com/faroshq/provider-app-studio/store"
	"github.com/faroshq/provider-app-studio/workspace"
)

func TestDefaultProjectDevelopmentEnvironmentUsesInfrastructureBackedSandboxRunner(t *testing.T) {
	t.Setenv("APP_STUDIO_PREVIEW_BASE_DOMAIN", "")
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

func TestDefaultProjectDevelopmentEnvironmentDoesNotExposeSandboxPreviewHTTPRoute(t *testing.T) {
	setPreviewHTTPRouteEnv(t)

	env := defaultProjectDevelopmentEnvironment("todo")
	if got := len(env.Bindings); got != 1 {
		t.Fatalf("bindings = %d, want only sandbox runner", got)
	}
	if got, want := env.Bindings[0].ResourceRef.Kind, "SandboxRunner"; got != want {
		t.Fatalf("Kind = %q, want %q", got, want)
	}
	var values map[string]any
	if err := json.Unmarshal(env.Bindings[0].Values.Raw, &values); err != nil {
		t.Fatalf("unmarshal binding values: %v", err)
	}
	if got, want := values["projectRef"], "todo"; got != want {
		t.Fatalf("projectRef = %q, want %q", got, want)
	}
	if _, ok := values["previewRoute"]; ok {
		t.Fatalf("default tenant binding values exposed previewRoute config: %#v", values)
	}
}

func TestEnsureProjectProviderResourceOverwritesSandboxRunnerPreviewRouteConfig(t *testing.T) {
	setPreviewHTTPRouteEnv(t)
	id := identity{tenantPath: "root:kedge:tenants:org-a:ws-1"}
	project := &aiv1alpha1.Project{ObjectMeta: metav1.ObjectMeta{Name: "todo"}}
	binding := defaultSandboxRunnerBinding(project.Name)
	binding.Values = projectDeploymentJSONValues(map[string]any{
		"name":       "tenant-chosen-name",
		"projectRef": "other-project",
		"previewRoute": map[string]any{
			"host": "evil.example.net",
			"parentGateway": map[string]any{
				"name":      "evil-gateway",
				"namespace": "evil-system",
			},
			"backend": map[string]any{
				"namespace":   "kube-system",
				"serviceName": "kube-dns",
				"servicePort": 53,
			},
		},
	})
	client := asclient.NewFromDynamic(fake.NewSimpleDynamicClient(runtime.NewScheme()))

	got, err := ensureProjectProviderResource(context.Background(), client, project, binding, id)
	if err != nil {
		t.Fatalf("ensureProjectProviderResource returned error: %v", err)
	}
	spec := got.Object["spec"].(map[string]any)
	runnerName := sandboxRunnerResourceName(id.tenantPath, project.Name)
	if got, want := spec["name"], runnerName; got != want {
		t.Fatalf("spec.name = %q, want %q", got, want)
	}
	if got, want := spec["projectRef"], project.Name; got != want {
		t.Fatalf("spec.projectRef = %q, want %q", got, want)
	}
	if got, want := spec["previewRouteEnabled"], true; got != want {
		t.Fatalf("spec.previewRouteEnabled = %v, want %v", got, want)
	}
	route, ok := spec["previewRoute"].(map[string]any)
	if !ok {
		t.Fatalf("spec.previewRoute = %#v, want object", spec["previewRoute"])
	}
	if got, want := route["host"], runnerName+".preview.example.com"; got != want {
		t.Fatalf("previewRoute.host = %q, want %q", got, want)
	}
	if got, want := route["channel"], "development-preview"; got != want {
		t.Fatalf("previewRoute.channel = %q, want %q", got, want)
	}
	if got, want := route["accessMode"], "private"; got != want {
		t.Fatalf("previewRoute.accessMode = %q, want %q", got, want)
	}
	parentGateway, ok := route["parentGateway"].(map[string]any)
	if !ok {
		t.Fatalf("previewRoute.parentGateway = %#v, want object", route["parentGateway"])
	}
	if got, want := parentGateway["namespace"], "preview-system"; got != want {
		t.Fatalf("previewRoute.parentGateway.namespace = %q, want %q", got, want)
	}
	if got, want := parentGateway["name"], "kedge-preview"; got != want {
		t.Fatalf("previewRoute.parentGateway.name = %q, want %q", got, want)
	}
	if got, want := parentGateway["sectionName"], "https"; got != want {
		t.Fatalf("previewRoute.parentGateway.sectionName = %q, want %q", got, want)
	}
	backend, ok := route["backend"].(map[string]any)
	if !ok {
		t.Fatalf("previewRoute.backend = %#v, want object", route["backend"])
	}
	if got, want := backend["namespace"], "preview-system"; got != want {
		t.Fatalf("previewRoute.backend.namespace = %q, want %q", got, want)
	}
	if got, want := backend["serviceName"], "preview-gateway"; got != want {
		t.Fatalf("previewRoute.backend.serviceName = %q, want %q", got, want)
	}
	if got, want := backend["servicePort"], int64(9443); got != want {
		t.Fatalf("previewRoute.backend.servicePort = %#v, want %#v", got, want)
	}
	if _, ok := route["target"]; ok {
		t.Fatalf("previewRoute.target = %#v, want omitted because runtime target comes from SandboxRunner status", route["target"])
	}
	if _, ok := route["projectRef"]; ok {
		t.Fatalf("previewRoute.projectRef = %#v, want omitted", route["projectRef"])
	}
}

func TestEnsureProjectProviderResourceDisablesSandboxRunnerPreviewRouteWithoutPlatformConfig(t *testing.T) {
	t.Setenv("APP_STUDIO_PREVIEW_BASE_DOMAIN", "")
	id := identity{tenantPath: "root:kedge:tenants:org-a:ws-1"}
	project := &aiv1alpha1.Project{ObjectMeta: metav1.ObjectMeta{Name: "todo"}}
	binding := defaultSandboxRunnerBinding(project.Name)
	binding.Values = projectDeploymentJSONValues(map[string]any{
		"previewRouteEnabled": true,
		"previewRoute": map[string]any{
			"host": "evil.example.net",
			"backend": map[string]any{
				"namespace":   "kube-system",
				"serviceName": "kube-dns",
			},
		},
	})
	client := asclient.NewFromDynamic(fake.NewSimpleDynamicClient(runtime.NewScheme()))

	got, err := ensureProjectProviderResource(context.Background(), client, project, binding, id)
	if err != nil {
		t.Fatalf("ensureProjectProviderResource returned error: %v", err)
	}
	spec := got.Object["spec"].(map[string]any)
	if got, want := spec["previewRouteEnabled"], false; got != want {
		t.Fatalf("spec.previewRouteEnabled = %v, want %v", got, want)
	}
	if _, ok := spec["previewRoute"]; ok {
		t.Fatalf("spec.previewRoute = %#v, want removed when platform preview routing is disabled", spec["previewRoute"])
	}
}

func TestReconcileProjectLiveBindingsIgnoresSandboxPreviewHTTPRouteBinding(t *testing.T) {
	setPreviewHTTPRouteEnv(t)
	id := identity{tenantPath: "root:kedge:tenants:org-a:ws-1"}
	project := &aiv1alpha1.Project{
		ObjectMeta: metav1.ObjectMeta{Name: "todo"},
		Spec: aiv1alpha1.ProjectSpec{
			Environments: []aiv1alpha1.ProjectEnvironmentSpec{{
				Name: "development",
				Mode: aiv1alpha1.ProjectEnvironmentModeLive,
				Bindings: []aiv1alpha1.ProjectProviderBindingSpec{
					defaultSandboxRunnerBinding("todo"),
					{
						Name:     "preview-route",
						Provider: "app-studio",
						Kind:     aiv1alpha1.ProjectBindingKindProviderResource,
						ResourceRef: &aiv1alpha1.ProjectProviderResourceReference{
							APIVersion: "infrastructure.kedge.faros.sh/v1alpha1",
							Kind:       "SandboxPreviewHTTPRoute",
							Resource:   "sandboxpreviewhttproutes",
						},
						Values: projectDeploymentJSONValues(map[string]any{
							"name": "tenant-route",
							"backend": map[string]any{
								"namespace":   "kube-system",
								"serviceName": "kube-dns",
								"servicePort": 53,
							},
						}),
					},
				},
			}},
		},
	}
	client := asclient.NewFromDynamic(fake.NewSimpleDynamicClient(runtime.NewScheme(), &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "ai.kedge.faros.sh/v1alpha1",
		"kind":       "Project",
		"metadata": map[string]any{
			"name": project.Name,
		},
	}}))

	if _, err := (&Server{}).reconcileProjectLiveBindings(context.Background(), client, project, id); err != nil {
		t.Fatalf("reconcileProjectLiveBindings returned error: %v", err)
	}
	if _, err := client.Dynamic().Resource(infrastructureSandboxRunnerGVR()).Get(context.Background(), sandboxRunnerResourceName(id.tenantPath, project.Name), metav1.GetOptions{}); err != nil {
		t.Fatalf("get SandboxRunner: %v", err)
	}
	if _, err := client.Dynamic().Resource(infrastructureSandboxPreviewHTTPRouteGVR()).Get(context.Background(), "tenant-route", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("SandboxPreviewHTTPRoute get error = %v, want not found", err)
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
	t.Setenv("APP_STUDIO_PREVIEW_BASE_DOMAIN", "")
	spec := defaultProjectSpec("todo", "Todo", "Tasks", &aiv1alpha1.ProjectRepositoryBinding{RepositoryRef: "todo"})
	if got := len(spec.Environments); got != 1 {
		t.Fatalf("environments = %d, want 1", got)
	}
	if got, want := spec.Environments[0].Name, "development"; got != want {
		t.Fatalf("environment name = %q, want %q", got, want)
	}
	if got, want := spec.Sharing.Preview.Mode, aiv1alpha1.ProjectSharingModePrivate; got != want {
		t.Fatalf("preview sharing mode = %q, want %q", got, want)
	}
	if got, want := spec.Sharing.Publishing.Mode, aiv1alpha1.ProjectSharingModePrivate; got != want {
		t.Fatalf("publishing sharing mode = %q, want %q", got, want)
	}
}

func TestProjectViewDefaultsMissingSharingToPrivate(t *testing.T) {
	project := &aiv1alpha1.Project{
		ObjectMeta: metav1.ObjectMeta{Name: "todo"},
		Spec: aiv1alpha1.ProjectSpec{
			DisplayName: "Todo",
		},
	}
	view := projectView(context.Background(), nil, project, identity{})
	if got, want := view.Sharing.Preview.Mode, aiv1alpha1.ProjectSharingModePrivate; got != want {
		t.Fatalf("preview sharing mode = %q, want %q", got, want)
	}
	if got, want := view.Sharing.Publishing.Mode, aiv1alpha1.ProjectSharingModePrivate; got != want {
		t.Fatalf("publishing sharing mode = %q, want %q", got, want)
	}
}

func TestApplyProjectPatchRequestPersistsSharing(t *testing.T) {
	project := &aiv1alpha1.Project{
		ObjectMeta: metav1.ObjectMeta{Name: "todo"},
		Spec:       defaultProjectSpec("todo", "Todo", "Tasks", nil),
	}
	changed, err := applyProjectPatchRequest(project, PatchProjectRequest{
		Sharing: &aiv1alpha1.ProjectSharingSpec{
			Preview: aiv1alpha1.ProjectSharingPolicy{
				Mode: aiv1alpha1.ProjectSharingModeShared,
			},
			Publishing: aiv1alpha1.ProjectSharingPolicy{
				Mode: aiv1alpha1.ProjectSharingModePublic,
			},
		},
	})
	if err != nil {
		t.Fatalf("applyProjectPatchRequest returned error: %v", err)
	}
	if !changed {
		t.Fatal("changed = false, want true")
	}
	if got, want := project.Spec.Sharing.Preview.Mode, aiv1alpha1.ProjectSharingModeShared; got != want {
		t.Fatalf("preview sharing mode = %q, want %q", got, want)
	}
	if got, want := project.Spec.Sharing.Publishing.Mode, aiv1alpha1.ProjectSharingModePublic; got != want {
		t.Fatalf("publishing sharing mode = %q, want %q", got, want)
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

// newSandboxDataPlaneHub stands up a fake hub backend-proxy endpoint for the
// infrastructure provider's SandboxRunner data plane. The handler is invoked
// with the verb parsed from the path (.../sandboxrunners/<name>/<verb>[/tail]).
// Returns the base URL to set as the server's hubBase.
func newSandboxDataPlaneHub(t *testing.T, handler func(verb string, w http.ResponseWriter, r *http.Request)) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		verb := ""
		for i, p := range parts {
			if p == sandboxRunnersResource && i+2 < len(parts) {
				verb = parts[i+2]
				break
			}
		}
		handler(verb, w, r)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// readyDataPlaneHub returns a hub whose proxy verb answers 200 (preview ready).
func readyDataPlaneHub(t *testing.T) string {
	return newSandboxDataPlaneHub(t, func(_ string, w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
}

// notFoundDataPlaneHub returns a hub whose proxy verb answers 404 (no service).
func notFoundDataPlaneHub(t *testing.T) string {
	return newSandboxDataPlaneHub(t, func(_ string, w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
}

func TestSyncProjectDevelopmentTargetPostsWorkspaceFilesToRuntime(t *testing.T) {
	var gotVerb, gotAuth string
	var gotFiles []map[string]string
	id := identity{tenantPath: "root:kedge:tenants:org-a:ws-1", orgUUID: "org-a", workspaceUUID: "ws-1", clusterID: "cluster-a", token: "caller-token"}
	runnerName := sandboxRunnerResourceName(id.tenantPath, "todo")
	hub := newSandboxDataPlaneHub(t, func(verb string, w http.ResponseWriter, r *http.Request) {
		gotVerb = verb
		gotAuth = r.Header.Get("Authorization")
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
	})

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
	server := NewWithWorkspace(nil, nil, workspaces, hub, false)
	target, ok := projectDevelopmentSyncTarget(project, id)
	if !ok {
		t.Fatal("projectDevelopmentSyncTarget returned !ok")
	}
	result, err := server.syncProjectDevelopmentTarget(context.Background(), client, id, project, target)
	if err != nil {
		t.Fatalf("syncProjectDevelopmentTarget returned error: %v", err)
	}
	if gotVerb != dataPlaneVerbSync {
		t.Fatalf("data-plane verb = %q, want %q", gotVerb, dataPlaneVerbSync)
	}
	if gotAuth != "Bearer caller-token" {
		t.Fatalf("forwarded Authorization = %q, want the caller's bearer token", gotAuth)
	}
	if len(gotFiles) != 1 || gotFiles[0]["path"] != "src/App.tsx" || gotFiles[0]["content"] != "hello\n" {
		t.Fatalf("files = %#v, want src/App.tsx content", gotFiles)
	}
	var decoded struct {
		Phase   string   `json:"phase"`
		Changed []string `json:"changed"`
	}
	if err := json.Unmarshal(result, &decoded); err != nil {
		t.Fatalf("decode sync result: %v", err)
	}
	if got, want := decoded.Phase, "Synced"; got != want {
		t.Fatalf("phase = %q, want %q", got, want)
	}
	if got, want := decoded.Changed, []string{"src/App.tsx"}; len(got) != 1 || got[0] != want[0] {
		t.Fatalf("changed = %#v, want %#v", got, want)
	}
}

func TestAuthorizeProjectDevelopmentPreviewTargetGetsSignedHTTPRouteHostURL(t *testing.T) {
	setPreviewHTTPRouteEnv(t)
	id := identity{tenantPath: "root:kedge:tenants:org-a:ws-1", orgUUID: "org-a", workspaceUUID: "ws-1", clusterID: "cluster-a", user: "user@example.com", token: "caller-token"}
	runnerName := sandboxRunnerResourceName(id.tenantPath, "todo")
	project := &aiv1alpha1.Project{
		ObjectMeta: metav1.ObjectMeta{Name: "todo"},
		Spec: aiv1alpha1.ProjectSpec{
			Environments: []aiv1alpha1.ProjectEnvironmentSpec{defaultProjectDevelopmentEnvironment("todo")},
		},
	}
	client := asclient.NewFromDynamic(fake.NewSimpleDynamicClient(
		runtime.NewScheme(),
		testSandboxRunnerWithPreviewRoute(runnerName, project.Name, "https://"+runnerName+".preview.example.com/"),
	))
	server := NewWithWorkspace(nil, nil, nil, readyDataPlaneHub(t), false)
	server.previewSigner = previewtoken.NewSigner([]byte("test-secret"))
	got, err := server.authorizeProjectDevelopmentPreviewTarget(context.Background(), client, id, project, projectDevelopmentSyncTargetInfo{ResourceName: runnerName})
	if err != nil {
		t.Fatalf("authorizeProjectDevelopmentPreviewTarget returned error: %v", err)
	}
	if !got.Ready {
		t.Fatalf("ready = false, want true: %#v", got)
	}
	parsed, err := url.Parse(got.PreviewURL)
	if err != nil {
		t.Fatalf("parse preview URL: %v", err)
	}
	if parsed.Scheme != "https" || parsed.Host != runnerName+".preview.example.com" || parsed.Path != "/" {
		t.Fatalf("previewURL = %q, want signed root URL on preview host", got.PreviewURL)
	}
	if strings.Contains(got.PreviewURL, "/services/providers/app-studio/") {
		t.Fatalf("previewURL = %q, must not use legacy provider path", got.PreviewURL)
	}
	if got.PreviewTokenExpiresAt == "" {
		t.Fatal("PreviewTokenExpiresAt is empty, want signed token expiry")
	}
	payload, err := server.previewSigner.Verify(parsed.Query().Get(previewtoken.QueryParam))
	if err != nil {
		t.Fatalf("verify preview token: %v", err)
	}
	if got, want := payload.ProjectName, project.Name; got != want {
		t.Fatalf("token ProjectName = %q, want %q", got, want)
	}
	if got, want := payload.TenantPath, id.tenantPath; got != want {
		t.Fatalf("token TenantPath = %q, want %q", got, want)
	}
	if got, want := payload.ResourceName, runnerName; got != want {
		t.Fatalf("token ResourceName = %q, want %q", got, want)
	}
	if got, want := payload.Host, runnerName+".preview.example.com"; got != want {
		t.Fatalf("token Host = %q, want %q", got, want)
	}
	if got, want := payload.RuntimeNamespace, runnerName; got != want {
		t.Fatalf("token RuntimeNamespace = %q, want %q", got, want)
	}
	if got, want := payload.PreviewServiceName, runnerName+"-preview"; got != want {
		t.Fatalf("token PreviewServiceName = %q, want %q", got, want)
	}
	if got, want := payload.PreviewPortName, "preview"; got != want {
		t.Fatalf("token PreviewPortName = %q, want %q", got, want)
	}
	if got, want := payload.AccessMode, string(aiv1alpha1.ProjectSharingModePrivate); got != want {
		t.Fatalf("token AccessMode = %q, want %q", got, want)
	}
	if got, want := payload.Subject, "user@example.com"; got != want {
		t.Fatalf("token Subject = %q, want %q", got, want)
	}
	expiresAt, err := time.Parse(time.RFC3339, got.PreviewTokenExpiresAt)
	if err != nil {
		t.Fatalf("parse PreviewTokenExpiresAt: %v", err)
	}
	if got, want := expiresAt.Unix(), payload.ExpiresAt; got != want {
		t.Fatalf("PreviewTokenExpiresAt unix = %d, want %d", got, want)
	}
}

func TestAuthorizeProjectDevelopmentPreviewTargetAddsConfiguredPublicPort(t *testing.T) {
	setPreviewHTTPRouteEnv(t)
	t.Setenv("APP_STUDIO_PREVIEW_PUBLIC_PORT", "10443")
	id := identity{tenantPath: "root:kedge:tenants:org-a:ws-1", orgUUID: "org-a", workspaceUUID: "ws-1", clusterID: "cluster-a", user: "user@example.com", token: "caller-token"}
	runnerName := sandboxRunnerResourceName(id.tenantPath, "todo")
	project := &aiv1alpha1.Project{
		ObjectMeta: metav1.ObjectMeta{Name: "todo"},
		Spec: aiv1alpha1.ProjectSpec{
			Environments: []aiv1alpha1.ProjectEnvironmentSpec{defaultProjectDevelopmentEnvironment("todo")},
		},
	}
	client := asclient.NewFromDynamic(fake.NewSimpleDynamicClient(
		runtime.NewScheme(),
		testSandboxRunnerWithPreviewRoute(runnerName, project.Name, "https://"+runnerName+".preview.example.com/"),
	))
	server := NewWithWorkspace(nil, nil, nil, readyDataPlaneHub(t), false)
	server.previewSigner = previewtoken.NewSigner([]byte("test-secret"))

	got, err := server.authorizeProjectDevelopmentPreviewTarget(context.Background(), client, id, project, projectDevelopmentSyncTargetInfo{ResourceName: runnerName})
	if err != nil {
		t.Fatalf("authorizeProjectDevelopmentPreviewTarget returned error: %v", err)
	}
	parsed, err := url.Parse(got.PreviewURL)
	if err != nil {
		t.Fatalf("parse preview URL: %v", err)
	}
	if got, want := parsed.Host, runnerName+".preview.example.com:10443"; got != want {
		t.Fatalf("preview URL host = %q, want %q", got, want)
	}
	payload, err := server.previewSigner.Verify(parsed.Query().Get(previewtoken.QueryParam))
	if err != nil {
		t.Fatalf("verify preview token: %v", err)
	}
	if got, want := payload.Host, runnerName+".preview.example.com"; got != want {
		t.Fatalf("token Host = %q, want normalized host without localdev port %q", got, want)
	}
}

func TestAuthorizeProjectDevelopmentPreviewTargetRejectsForgedSandboxRunnerPreviewRouteHost(t *testing.T) {
	setPreviewHTTPRouteEnv(t)
	id := identity{tenantPath: "root:kedge:tenants:org-a:ws-1", clusterID: "cluster-a", token: "caller-token"}
	runnerName := sandboxRunnerResourceName(id.tenantPath, "todo")
	project := &aiv1alpha1.Project{
		ObjectMeta: metav1.ObjectMeta{Name: "todo"},
		Spec: aiv1alpha1.ProjectSpec{
			Environments: []aiv1alpha1.ProjectEnvironmentSpec{defaultProjectDevelopmentEnvironment("todo")},
		},
	}
	client := asclient.NewFromDynamic(fake.NewSimpleDynamicClient(
		runtime.NewScheme(),
		testSandboxRunnerWithPreviewRoute(runnerName, project.Name, "https://evil.example.net/"),
	))
	server := NewWithWorkspace(nil, nil, nil, readyDataPlaneHub(t), false)
	server.previewSigner = previewtoken.NewSigner([]byte("test-secret"))

	if _, err := server.authorizeProjectDevelopmentPreviewTarget(context.Background(), client, id, project, projectDevelopmentSyncTargetInfo{ResourceName: runnerName}); err == nil || !strings.Contains(err.Error(), "does not match expected host") {
		t.Fatalf("authorizeProjectDevelopmentPreviewTarget error = %v, want expected host rejection", err)
	}
}

func TestAuthorizeProjectDevelopmentPreviewTargetRejectsForgedSandboxRunnerPreviewRouteNamespace(t *testing.T) {
	setPreviewHTTPRouteEnv(t)
	id := identity{tenantPath: "root:kedge:tenants:org-a:ws-1", clusterID: "cluster-a", token: "caller-token"}
	runnerName := sandboxRunnerResourceName(id.tenantPath, "todo")
	project := &aiv1alpha1.Project{
		ObjectMeta: metav1.ObjectMeta{Name: "todo"},
		Spec: aiv1alpha1.ProjectSpec{
			Environments: []aiv1alpha1.ProjectEnvironmentSpec{defaultProjectDevelopmentEnvironment("todo")},
		},
	}
	runner := testSandboxRunnerWithPreviewRoute(runnerName, project.Name, "https://"+runnerName+".preview.example.com/")
	if err := unstructured.SetNestedField(runner.Object, "kube-system", "status", "previewRoute", "httpRouteRef", "namespace"); err != nil {
		t.Fatalf("set forged route namespace: %v", err)
	}
	client := asclient.NewFromDynamic(fake.NewSimpleDynamicClient(runtime.NewScheme(), runner))
	server := NewWithWorkspace(nil, nil, nil, readyDataPlaneHub(t), false)
	server.previewSigner = previewtoken.NewSigner([]byte("test-secret"))

	if _, err := server.authorizeProjectDevelopmentPreviewTarget(context.Background(), client, id, project, projectDevelopmentSyncTargetInfo{ResourceName: runnerName}); err == nil || !strings.Contains(err.Error(), "does not match expected namespace") {
		t.Fatalf("authorizeProjectDevelopmentPreviewTarget error = %v, want expected namespace rejection", err)
	}
}

func TestAuthorizeProjectDevelopmentPreviewTargetRequiresSandboxRunnerPreviewRouteURL(t *testing.T) {
	setPreviewHTTPRouteEnv(t)
	id := identity{tenantPath: "root:kedge:tenants:org-a:ws-1", clusterID: "cluster-a", token: "caller-token"}
	runnerName := sandboxRunnerResourceName(id.tenantPath, "todo")
	project := &aiv1alpha1.Project{
		ObjectMeta: metav1.ObjectMeta{Name: "todo"},
		Spec: aiv1alpha1.ProjectSpec{
			Environments: []aiv1alpha1.ProjectEnvironmentSpec{defaultProjectDevelopmentEnvironment("todo")},
		},
	}
	client := asclient.NewFromDynamic(fake.NewSimpleDynamicClient(
		runtime.NewScheme(),
		testSandboxRunnerWithPreviewRoute(runnerName, project.Name, ""),
	))
	server := NewWithWorkspace(nil, nil, nil, readyDataPlaneHub(t), false)
	got, err := server.authorizeProjectDevelopmentPreviewTarget(context.Background(), client, id, project, projectDevelopmentSyncTargetInfo{ResourceName: runnerName})
	if err != nil {
		t.Fatalf("authorizeProjectDevelopmentPreviewTarget returned error: %v", err)
	}
	if got.Ready {
		t.Fatalf("ready = true, want false: %#v", got)
	}
	if got.PreviewURL != "" {
		t.Fatalf("previewURL = %q, want empty while HTTPRoute has no status.url", got.PreviewURL)
	}
	if got.Reason != "sandbox_preview_route_not_ready" {
		t.Fatalf("reason = %q, want sandbox_preview_route_not_ready", got.Reason)
	}
}

func TestAuthorizeProjectDevelopmentPreviewTargetReturnsNotReady(t *testing.T) {
	id := identity{tenantPath: "root:kedge:tenants:org-a:ws-1", clusterID: "cluster-a", token: "caller-token"}
	runnerName := sandboxRunnerResourceName(id.tenantPath, "todo")
	project := &aiv1alpha1.Project{ObjectMeta: metav1.ObjectMeta{Name: "todo"}}
	client := asclient.NewFromDynamic(fake.NewSimpleDynamicClient(runtime.NewScheme(), testSandboxRunner(runnerName, project.Name)))
	server := NewWithWorkspace(nil, nil, nil, notFoundDataPlaneHub(t), false)
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

func TestAuthorizeProjectDevelopmentPreviewTargetWaitsForRuntimeServiceProxy(t *testing.T) {
	id := identity{tenantPath: "root:kedge:tenants:org-a:ws-1", clusterID: "cluster-a", token: "caller-token"}
	runnerName := sandboxRunnerResourceName(id.tenantPath, "todo")
	hub := newSandboxDataPlaneHub(t, func(verb string, w http.ResponseWriter, _ *http.Request) {
		if verb != dataPlaneVerbProxy {
			t.Fatalf("readiness verb = %q, want %q", verb, dataPlaneVerbProxy)
		}
		// The infra data-plane proxy surfaces the runtime service-proxy's own
		// 503 + Kubernetes Status body while the runner is still starting.
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprint(w, `{"kind":"Status","apiVersion":"v1","status":"Failure","message":"error trying to reach service: dial tcp 10.244.0.55:3000: connect: connection refused","reason":"ServiceUnavailable","code":503}`)
	})

	project := &aiv1alpha1.Project{ObjectMeta: metav1.ObjectMeta{Name: "todo"}}
	client := asclient.NewFromDynamic(fake.NewSimpleDynamicClient(runtime.NewScheme(), testSandboxRunner(runnerName, project.Name)))
	server := NewWithWorkspace(nil, nil, nil, hub, false)
	got, err := server.authorizeProjectDevelopmentPreviewTarget(context.Background(), client, id, project, projectDevelopmentSyncTargetInfo{ResourceName: runnerName})
	if err != nil {
		t.Fatalf("authorizeProjectDevelopmentPreviewTarget returned error: %v", err)
	}
	if got.Ready {
		t.Fatalf("ready = true, want false while service proxy returns runtime startup status: %#v", got)
	}
	if got.PreviewURL != "" {
		t.Fatalf("previewURL = %q, want empty while not ready", got.PreviewURL)
	}
	if got.Reason != "runtime_starting" {
		t.Fatalf("reason = %q, want runtime_starting", got.Reason)
	}
}

func TestDeleteProjectProviderResourcesRemovesInfrastructureSandboxRunner(t *testing.T) {
	t.Setenv("APP_STUDIO_PREVIEW_BASE_DOMAIN", "")
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
			"phase":      "Running",
			"previewURL": "/services/providers/app-studio/api/projects/todo/preview/",
		},
	}}
	client := asclient.NewFromDynamic(fake.NewSimpleDynamicClient(runtime.NewScheme(), devEnv))
	view := projectView(context.Background(), client, project, id)
	if len(view.Environments) != 1 || len(view.Environments[0].Bindings) != 1 {
		t.Fatalf("view environments = %#v, want one development binding", view.Environments)
	}
	binding := view.Environments[0].Bindings[0]
	if got := binding.PreviewURL; got != "" {
		t.Fatalf("PreviewURL = %q, want empty for SandboxRunner binding even with stale runner status.previewURL", got)
	}
	if got, want := view.Environments[0].Phase, "Running"; got != want {
		t.Fatalf("environment phase = %q, want %q", got, want)
	}
}

func TestProjectViewDerivesSandboxRunnerPhaseFromKROReadyCondition(t *testing.T) {
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
			"conditions": []any{
				map[string]any{
					"type":   "Ready",
					"status": "True",
					"reason": "Ready",
				},
			},
			"state": "ACTIVE",
		},
	}}
	client := asclient.NewFromDynamic(fake.NewSimpleDynamicClient(runtime.NewScheme(), devEnv))
	view := projectView(context.Background(), client, project, id)
	if len(view.Environments) != 1 || len(view.Environments[0].Bindings) != 1 {
		t.Fatalf("view environments = %#v, want one development binding", view.Environments)
	}
	binding := view.Environments[0].Bindings[0]
	if got, want := binding.Phase, "Ready"; got != want {
		t.Fatalf("binding phase = %q, want %q", got, want)
	}
	if got, want := view.Environments[0].Phase, "Ready"; got != want {
		t.Fatalf("environment phase = %q, want %q", got, want)
	}
}

func TestGenerateProjectAssistantStreamUsesLiveBindingStatusOverlay(t *testing.T) {
	setPreviewHTTPRouteEnv(t)
	project := &aiv1alpha1.Project{
		ObjectMeta: metav1.ObjectMeta{Name: "todo"},
		Spec:       defaultProjectSpec("todo", "Todo", "", nil),
	}
	id := identity{tenantPath: "root:kedge:tenants:org-a:ws-1", orgUUID: "org-a", workspaceUUID: "ws-1"}
	runnerName := sandboxRunnerResourceName(id.tenantPath, "todo")
	client := asclient.NewFromDynamic(fake.NewSimpleDynamicClient(
		runtime.NewScheme(),
		testSandboxRunnerWithPreviewRoute(runnerName, project.Name, "https://"+runnerName+".preview.example.com/"),
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
	if got, want := engine.previewURL, "https://"+runnerName+".preview.example.com/"; got != want {
		t.Fatalf("engine project preview URL = %q, want %q", got, want)
	}
}

func TestProjectViewOverlaysSandboxRunnerPreviewRouteStatus(t *testing.T) {
	setPreviewHTTPRouteEnv(t)
	project := &aiv1alpha1.Project{
		ObjectMeta: metav1.ObjectMeta{Name: "todo"},
		Spec:       defaultProjectSpec("todo", "Todo", "", nil),
	}
	id := identity{tenantPath: "root:kedge:tenants:org-a:ws-1"}
	runnerName := sandboxRunnerResourceName(id.tenantPath, "todo")
	client := asclient.NewFromDynamic(fake.NewSimpleDynamicClient(
		runtime.NewScheme(),
		testSandboxRunnerWithPreviewRoute(runnerName, project.Name, "https://"+runnerName+".preview.example.com/"),
	))
	view := projectView(context.Background(), client, project, id)
	if len(view.Environments) != 1 || len(view.Environments[0].Bindings) != 1 {
		t.Fatalf("view environments = %#v, want one sandbox runner binding", view.Environments)
	}
	if got, want := view.Environments[0].Bindings[0].PreviewURL, "https://"+runnerName+".preview.example.com/"; got != want {
		t.Fatalf("PreviewURL = %q, want %q", got, want)
	}
}

func TestRuntimeTargetFromInstanceUsesSplitPreviewAndControlServices(t *testing.T) {
	runnerName := "kedge-sandbox-0123456789abcdef"
	runner := testSandboxRunner(runnerName, "todo")
	client := asclient.NewFromDynamic(fake.NewSimpleDynamicClient(runtime.NewScheme(), runner))

	target, _, err := (&Server{}).runtimeTargetForProject(context.Background(), client, runnerName)
	if err != nil {
		t.Fatalf("runtimeTargetForProject: %v", err)
	}

	if got, want := target.Preview.Name, runnerName+"-preview"; got != want {
		t.Fatalf("Preview.Name = %q, want %q", got, want)
	}
	if got, want := target.Control.Name, runnerName+"-control"; got != want {
		t.Fatalf("Control.Name = %q, want %q", got, want)
	}
	if got, want := target.ControlSecret.Name, runnerName+"-control"; got != want {
		t.Fatalf("ControlSecret.Name = %q, want %q", got, want)
	}
}

func TestRuntimeTargetFromInstanceRejectsForgedRuntimeRefs(t *testing.T) {
	runnerName := "kedge-sandbox-0123456789abcdef"
	tests := []struct {
		name    string
		fields  []string
		value   string
		wantErr string
	}{
		{
			name:    "preview service",
			fields:  []string{"status", "previewServiceRef", "name"},
			value:   "kube-dns",
			wantErr: "previewServiceRef",
		},
		{
			name:    "control service",
			fields:  []string{"status", "controlServiceRef", "name"},
			value:   "kube-dns",
			wantErr: "controlServiceRef",
		},
		{
			name:    "control secret",
			fields:  []string{"status", "controlSecretRef", "name"},
			value:   "other-secret",
			wantErr: "controlSecretRef",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := testSandboxRunner(runnerName, "todo")
			if err := unstructured.SetNestedField(runner.Object, tt.value, tt.fields...); err != nil {
				t.Fatalf("set forged ref: %v", err)
			}

			_, err := runtimeTargetFromInstance(runner)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("runtimeTargetFromInstance error = %v, want containing %q", err, tt.wantErr)
			}
		})
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
				"name":      name + "-preview",
				"portName":  "preview",
			},
			"controlServiceRef": map[string]any{
				"namespace": name,
				"name":      name + "-control",
				"portName":  "control",
			},
			"controlSecretRef": map[string]any{
				"namespace": name,
				"name":      name + "-control",
			},
		},
	}}
}

func testSandboxRunnerWithPreviewRoute(name, project, rawURL string) *unstructured.Unstructured {
	runner := testSandboxRunner(name, project)
	host := ""
	if parsed, err := url.Parse(rawURL); err == nil && parsed.Host != "" {
		host = parsed.Host
	}
	if host == "" {
		host = previewtoken.NormalizeHost(rawURL)
	}
	if err := unstructured.SetNestedField(runner.Object, map[string]any{
		"phase": "Ready",
		"host":  host,
		"url":   rawURL,
		"httpRouteRef": map[string]any{
			"name":      name,
			"namespace": sandboxPreviewHTTPRouteNamespace,
		},
		"backend": map[string]any{
			"namespace":   "tenant-controlled",
			"serviceName": "tenant-backend",
			"servicePort": int64(1),
		},
	}, "status", "previewRoute"); err != nil {
		panic(err)
	}
	return runner
}

func setPreviewHTTPRouteEnv(t *testing.T) {
	t.Helper()
	t.Setenv("APP_STUDIO_PREVIEW_BASE_DOMAIN", "preview.example.com")
	t.Setenv("APP_STUDIO_PREVIEW_HTTPROUTE_PARENT_GATEWAY_NAME", "kedge-preview")
	t.Setenv("APP_STUDIO_PREVIEW_HTTPROUTE_PARENT_GATEWAY_NAMESPACE", "preview-system")
	t.Setenv("APP_STUDIO_PREVIEW_HTTPROUTE_PARENT_GATEWAY_SECTION_NAME", "https")
	t.Setenv("APP_STUDIO_PREVIEW_BACKEND_NAMESPACE", "preview-system")
	t.Setenv("APP_STUDIO_PREVIEW_BACKEND_SERVICE_NAME", "preview-gateway")
	t.Setenv("APP_STUDIO_PREVIEW_BACKEND_SERVICE_PORT", "9443")
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

func infrastructureSandboxPreviewHTTPRouteGVR() k8sschema.GroupVersionResource {
	return k8sschema.GroupVersionResource{
		Group:    "infrastructure.kedge.faros.sh",
		Version:  "v1alpha1",
		Resource: "sandboxpreviewhttproutes",
	}
}
