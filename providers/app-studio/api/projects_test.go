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

package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"

	aiv1alpha1 "github.com/faroshq/provider-app-studio/apis/ai/v1alpha1"
	"github.com/faroshq/provider-app-studio/bindings"
	asclient "github.com/faroshq/provider-app-studio/client"
	"github.com/faroshq/provider-app-studio/store"
	"github.com/faroshq/provider-app-studio/workspace"
)

func TestProjectInitialBootstrapPromptDigestDoesNotExposePrompt(t *testing.T) {
	digest := projectInitialBootstrapPromptDigest("Build a todo app")
	if digest == projectInitialBootstrapPromptDigest("Build an unbounded platform") || digest == "Build a todo app" {
		t.Fatalf("prompt digest did not distinguish or conceal the creation prompt: %q", digest)
	}
}

func TestWriteProjectErrorMapsPreflightOutageToRetryableBadGateway(t *testing.T) {
	recorder := httptest.NewRecorder()
	writeProjectError(recorder, fmt.Errorf("%w: upstream returned 500", errProjectCreatePreflightUnavailable))
	if recorder.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d: %s", recorder.Code, http.StatusBadGateway, recorder.Body.String())
	}
	if recorder.Header().Get("Retry-After") != "2" || !strings.Contains(recorder.Body.String(), "temporarily unavailable") {
		t.Fatalf("response headers/body = %#v %s", recorder.Header(), recorder.Body.String())
	}
}

func TestProjectListViewPreservesWorkspaceFenceForThumbnailCapture(t *testing.T) {
	ctx := context.Background()
	id := identity{orgUUID: "org-a", workspaceUUID: "ws-a"}
	project := &aiv1alpha1.Project{ObjectMeta: metav1.ObjectMeta{Name: "demo", UID: "uid-a"}}
	workspaces := workspace.NewFileStore(t.TempDir())
	scope := projectWorkspaceScope(id, project)
	if _, err := workspaces.WriteFile(ctx, scope, workspace.WriteOptions{Path: "app.txt", Content: "updated\n"}); err != nil {
		t.Fatal(err)
	}
	revision, err := workspaces.SourceRevision(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}

	view := (&Server{workspaces: workspaces}).projectListView(ctx, nil, project, id)
	if view.SourceRevision != revision || revision <= 1 {
		t.Fatalf("source revision = %d, want workspace revision %d greater than initial fence", view.SourceRevision, revision)
	}
}

func TestCreateProjectPreflightTemplateCreatesBindingAndInstance(t *testing.T) {
	client := newProjectCreationTestClient(
		codeConnectionObjectWithValidated("github", metav1.ConditionTrue),
		applicationTemplateObject(),
	)
	preflight := &projectCreatePreflight{
		Naming:       projectNamingResult{DisplayName: "Customer Portal", RepositoryName: "customer-portal"},
		TemplateName: "application",
	}
	created, err := (&Server{actionsExternalURL: "https://actions.example.test"}).createProjectFromRequestWithPreflight(
		context.Background(),
		client,
		identity{user: "alice", orgUUID: "org-a", workspaceUUID: "ws-1", tenantPath: "root:org-a:ws-1"},
		CreateProjectRequest{ConnectionRef: "github", InferDevelopmentTemplate: true},
		nil,
		nil,
		preflight,
	)
	if err != nil {
		t.Fatalf("createProjectFromRequestWithPreflight: %v", err)
	}
	if created.Spec.Template == nil || created.Spec.Template.Name != "application" {
		t.Fatalf("created template = %+v, want application", created.Spec.Template)
	}
	if len(created.Spec.Environments) != 1 || len(created.Spec.Environments[0].Bindings) != 1 {
		t.Fatalf("created environments = %+v, want one development binding", created.Spec.Environments)
	}
	binding := created.Spec.Environments[0].Bindings[0]
	if binding.ResourceRef == nil || binding.ResourceRef.Name != created.Name+"-dev" {
		t.Fatalf("created binding = %+v, want %s-dev", binding, created.Name)
	}
	// Instances are materialized by the Project reconciler, not the handler.
	// The write contract here is a self-contained binding: the desired
	// instance must be derivable from the spec alone.
	want, gvr, err := bindings.Desired(created, binding)
	if err != nil {
		t.Fatalf("binding is not self-contained: %v", err)
	}
	if gvr.Resource != "instances" || gvr.Group != "infrastructure.faros.sh" {
		t.Fatalf("binding GVR = %v, want instances.infrastructure.faros.sh", gvr)
	}
	if want.GetName() != created.Name+"-dev" {
		t.Fatalf("desired instance name = %q, want %s-dev", want.GetName(), created.Name)
	}
}

func TestCreateProjectLivePathListsCatalogCallsPreflightOnceAndCreatesInstance(t *testing.T) {
	dynamicClient := newProjectCreationTestDynamicClient(
		codeConnectionObjectWithValidated("github", metav1.ConditionTrue),
		applicationTemplateObject(),
	)
	dynamicClient.PrependReactor("create", "projects", func(action k8stesting.Action) (bool, runtime.Object, error) {
		object := action.(k8stesting.CreateAction).GetObject()
		object.(metav1.Object).SetUID("project-uid-customer-portal")
		return false, nil, nil
	})
	client := asclient.NewFromDynamic(dynamicClient)
	calls := 0
	server := &Server{
		actionsExternalURL: "https://actions.example.test",
		store:              store.NewMemoryStore(),
		projectCreatePreflight: func(_ context.Context, _ *asclient.Client, prompt string, templates []projectDevelopmentTemplateView) (projectCreatePreflight, error) {
			calls++
			if prompt != "Build a frontend and backend customer portal." {
				t.Fatalf("preflight prompt = %q", prompt)
			}
			if len(templates) != 1 || templates[0].Name != "application" {
				t.Fatalf("preflight templates = %+v, want live application catalog entry", templates)
			}
			return projectCreatePreflight{
				Naming:       projectNamingResult{DisplayName: "Customer Portal", RepositoryName: "customer-portal"},
				TemplateName: "application",
			}, nil
		},
	}
	server.SetReplicaRouting("replica-a", "10.0.0.1:8091", "internal-token")
	created, err := server.createProjectFromRequest(
		context.Background(),
		client,
		identity{user: "alice", orgUUID: "org-a", workspaceUUID: "ws-1", tenantPath: "root:org-a:ws-1"},
		CreateProjectRequest{
			Prompt:                   "Build a frontend and backend customer portal.",
			ConnectionRef:            "github",
			InferDevelopmentTemplate: true,
		},
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("createProjectFromRequest: %v", err)
	}
	if calls != 1 {
		t.Fatalf("preflight calls = %d, want exactly one", calls)
	}
	if created.Spec.Template == nil || created.Spec.Template.Name != "application" {
		t.Fatalf("created template = %+v, want application", created.Spec.Template)
	}
	// Instances are materialized by the Project reconciler, not the handler:
	// assert the spec-only contract (a self-contained development binding).
	if len(created.Spec.Environments) != 1 || len(created.Spec.Environments[0].Bindings) != 1 {
		t.Fatalf("created environments = %+v, want one development binding", created.Spec.Environments)
	}
	want, gvr, err := bindings.Desired(created, created.Spec.Environments[0].Bindings[0])
	if err != nil {
		t.Fatalf("binding is not self-contained: %v", err)
	}
	if gvr.Resource != "instances" || want.GetName() != created.Name+"-dev" {
		t.Fatalf("desired instance = %s/%s, want instances/%s-dev", gvr.Resource, want.GetName(), created.Name)
	}
}

func TestCreateProjectLivePathSurfacesCatalogListErrorBeforePreflight(t *testing.T) {
	dynamicClient := fake.NewSimpleDynamicClientWithCustomListKinds(
		runtime.NewScheme(),
		map[schema.GroupVersionResource]string{
			asclient.ProjectGVR: "ProjectList",
			templatesGVR:        "TemplateList",
		},
	)
	forbidden := apierrors.NewForbidden(
		schema.GroupResource{Group: templatesGVR.Group, Resource: templatesGVR.Resource},
		"",
		nil,
	)
	dynamicClient.PrependReactor("list", "templates", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, forbidden
	})
	calls := 0
	server := &Server{
		projectCreatePreflight: func(context.Context, *asclient.Client, string, []projectDevelopmentTemplateView) (projectCreatePreflight, error) {
			calls++
			return projectCreatePreflight{}, nil
		},
	}
	_, err := server.createProjectFromRequest(
		context.Background(),
		asclient.NewFromDynamic(dynamicClient),
		identity{user: "alice", orgUUID: "org-a", workspaceUUID: "ws-1", tenantPath: "root:org-a:ws-1"},
		CreateProjectRequest{
			Prompt:                   "Build a customer portal.",
			InferDevelopmentTemplate: true,
		},
		nil,
		nil,
	)
	if !apierrors.IsForbidden(err) {
		t.Fatalf("catalog list error = %v, want forbidden surfaced", err)
	}
	if calls != 0 {
		t.Fatalf("preflight calls = %d, want none when catalog listing fails", calls)
	}
	projects, listErr := asclient.NewFromDynamic(dynamicClient).Projects().List(context.Background(), metav1.ListOptions{})
	if listErr != nil {
		t.Fatalf("list projects: %v", listErr)
	}
	if len(projects.Items) != 0 {
		t.Fatalf("projects = %+v, want none after catalog failure", projects.Items)
	}
}

func TestCreateProjectInvalidInferredTemplateFallsBackUnbound(t *testing.T) {
	client := newProjectCreationTestClient(
		codeConnectionObjectWithValidated("github", metav1.ConditionTrue),
		applicationTemplateObject(),
	)
	preflight := &projectCreatePreflight{
		Naming:       projectNamingResult{DisplayName: "Customer Portal", RepositoryName: "customer-portal"},
		TemplateName: "invented-template",
	}
	created, err := (&Server{actionsExternalURL: "https://actions.example.test"}).createProjectFromRequestWithPreflight(
		context.Background(),
		client,
		identity{user: "alice", orgUUID: "org-a", workspaceUUID: "ws-1", tenantPath: "root:org-a:ws-1"},
		CreateProjectRequest{ConnectionRef: "github", InferDevelopmentTemplate: true},
		nil,
		nil,
		preflight,
	)
	if err != nil {
		t.Fatalf("createProjectFromRequestWithPreflight: %v", err)
	}
	if created.Spec.Template != nil || len(created.Spec.Environments[0].Bindings) != 0 {
		t.Fatalf("created project = %+v, want safe unbound fallback", created.Spec)
	}
}

func TestCreateProjectPreflightTemplateRequiresExplicitInferenceAuthorization(t *testing.T) {
	client := newProjectCreationTestClient(
		codeConnectionObjectWithValidated("github", metav1.ConditionTrue),
		applicationTemplateObject(),
	)
	preflight := &projectCreatePreflight{
		Naming:       projectNamingResult{DisplayName: "Customer Portal", RepositoryName: "customer-portal"},
		TemplateName: "application",
	}
	created, err := (&Server{actionsExternalURL: "https://actions.example.test"}).createProjectFromRequestWithPreflight(
		context.Background(),
		client,
		identity{user: "alice", orgUUID: "org-a", workspaceUUID: "ws-1", tenantPath: "root:org-a:ws-1"},
		CreateProjectRequest{ConnectionRef: "github"},
		nil,
		nil,
		preflight,
	)
	if err != nil {
		t.Fatalf("createProjectFromRequestWithPreflight: %v", err)
	}
	if created.Spec.Template != nil || len(created.Spec.Environments[0].Bindings) != 0 {
		t.Fatalf("created project = %+v, want no inferred template without explicit request authorization", created.Spec)
	}
}

func TestCreateProjectExplicitTemplateTakesPrecedenceOverPreflight(t *testing.T) {
	client := newProjectCreationTestClient(
		codeConnectionObjectWithValidated("github", metav1.ConditionTrue),
		applicationTemplateObject(),
	)
	preflight := &projectCreatePreflight{
		Naming:       projectNamingResult{DisplayName: "Customer Portal", RepositoryName: "customer-portal"},
		TemplateName: "invented-template",
	}
	created, err := (&Server{actionsExternalURL: "https://actions.example.test"}).createProjectFromRequestWithPreflight(
		context.Background(),
		client,
		identity{user: "alice", orgUUID: "org-a", workspaceUUID: "ws-1", tenantPath: "root:org-a:ws-1"},
		CreateProjectRequest{
			ConnectionRef:            "github",
			TemplateName:             "application",
			InferDevelopmentTemplate: true,
		},
		nil,
		nil,
		preflight,
	)
	if err != nil {
		t.Fatalf("createProjectFromRequestWithPreflight: %v", err)
	}
	if created.Spec.Template == nil || created.Spec.Template.Name != "application" {
		t.Fatalf("created template = %+v, want explicit application template", created.Spec.Template)
	}
}

func TestCreateProjectExplicitTemplateFailsClosedBeforeCreation(t *testing.T) {
	client := newProjectCreationTestClient(
		codeConnectionObjectWithValidated("github", metav1.ConditionTrue),
		applicationTemplateObject(),
	)
	_, err := (&Server{}).createProjectFromRequest(
		context.Background(),
		client,
		identity{user: "alice", orgUUID: "org-a", workspaceUUID: "ws-1", tenantPath: "root:org-a:ws-1"},
		CreateProjectRequest{
			DisplayName:   "Customer Portal",
			TemplateName:  "invented-template",
			ConnectionRef: "github",
		},
		nil,
		nil,
	)
	if err == nil || !strings.Contains(err.Error(), `development template "invented-template" was not found`) {
		t.Fatalf("error = %v, want explicit missing-template validation", err)
	}
	projects, listErr := client.Projects().List(context.Background(), metav1.ListOptions{})
	if listErr != nil {
		t.Fatalf("list projects: %v", listErr)
	}
	if len(projects.Items) != 0 {
		t.Fatalf("projects = %+v, want none after explicit validation failure", projects.Items)
	}
	repositories, listErr := client.Resource(codeRepositoryResource, "").List(context.Background(), metav1.ListOptions{})
	if listErr != nil {
		t.Fatalf("list repositories: %v", listErr)
	}
	if len(repositories.Items) != 0 {
		t.Fatalf("repositories = %+v, want none after explicit validation failure", repositories.Items)
	}
}

func TestResolveProjectCreateTemplateRejectsPlatformOwnedExplicitAndInferred(t *testing.T) {
	platformOwned := applicationTemplateObject()
	platformOwned.SetName("universal-coding-sandbox")
	platformOwned.SetLabels(map[string]string{projectTemplatePlatformOwnedLabel: projectTemplatePlatformOwnedValue})
	client := newProjectCreationTestClient(platformOwned)

	for _, inferred := range []bool{false, true} {
		name := "explicit"
		if inferred {
			name = "inferred"
		}
		t.Run(name, func(t *testing.T) {
			info, err := resolveProjectCreateTemplate(
				context.Background(),
				client,
				"universal-coding-sandbox",
				inferred,
			)
			if info != nil {
				t.Fatalf("resolved info = %#v, want no selectable template", info)
			}
			var validationErr *ValidationError
			if !errors.As(err, &validationErr) {
				t.Fatalf("error = %v, want ValidationError", err)
			}
			if !strings.Contains(validationErr.Error(), "platform-owned") {
				t.Fatalf("validation error = %q, want platform-owned rejection", validationErr)
			}
			recorder := httptest.NewRecorder()
			writeProjectError(recorder, err)
			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("HTTP status = %d, want 400: %s", recorder.Code, recorder.Body.String())
			}
		})
	}

	projects, err := client.Projects().List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatalf("list projects after platform-owned rejection: %v", err)
	}
	if len(projects.Items) != 0 {
		t.Fatalf("projects = %+v, want none after validation rejection", projects.Items)
	}
}

func TestCreateProjectRejectsPlatformOwnedExplicitAndInferred(t *testing.T) {
	for _, inferred := range []bool{false, true} {
		name := "explicit"
		if inferred {
			name = "inferred"
		}
		t.Run(name, func(t *testing.T) {
			platformOwned := applicationTemplateObject()
			platformOwned.SetName("universal-coding-sandbox")
			platformOwned.SetLabels(map[string]string{projectTemplatePlatformOwnedLabel: projectTemplatePlatformOwnedValue})
			client := newProjectCreationTestClient(platformOwned)
			server := &Server{}
			id := identity{user: "alice", orgUUID: "org-a", workspaceUUID: "ws-1", tenantPath: "root:faros:tenants:org-a:ws-1"}
			var (
				created *aiv1alpha1.Project
				err     error
			)
			if inferred {
				created, err = server.createProjectFromRequestWithPreflight(
					context.Background(),
					client,
					id,
					CreateProjectRequest{InferDevelopmentTemplate: true},
					nil,
					nil,
					&projectCreatePreflight{
						Naming:       projectNamingResult{DisplayName: "Customer Portal", RepositoryName: "customer-portal"},
						TemplateName: "universal-coding-sandbox",
					},
				)
			} else {
				created, err = server.createProjectFromRequest(
					context.Background(),
					client,
					id,
					CreateProjectRequest{DisplayName: "Customer Portal", TemplateName: "universal-coding-sandbox"},
					nil,
					nil,
				)
			}
			if created != nil {
				t.Fatalf("created project = %#v, want no project", created)
			}
			var validationErr *ValidationError
			if !errors.As(err, &validationErr) || !strings.Contains(validationErr.Error(), "platform-owned") {
				t.Fatalf("create error = %v, want platform-owned ValidationError", err)
			}
			recorder := httptest.NewRecorder()
			writeProjectError(recorder, err)
			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("HTTP status = %d, want 400: %s", recorder.Code, recorder.Body.String())
			}
			projects, listErr := client.Projects().List(context.Background(), metav1.ListOptions{})
			if listErr != nil {
				t.Fatalf("list projects after rejection: %v", listErr)
			}
			if len(projects.Items) != 0 {
				t.Fatalf("projects = %+v, want none after platform-owned rejection", projects.Items)
			}
		})
	}
}

func TestResolveProjectCreateTemplateRequiresDevelopmentComponents(t *testing.T) {
	productionOnly := applicationTemplateObject()
	productionOnly.SetName("database")
	delete(productionOnly.Object["spec"].(map[string]any), "development")
	client := newProjectCreationTestClient(productionOnly)

	if _, err := resolveProjectCreateTemplate(context.Background(), client, "database", false); err == nil ||
		!strings.Contains(err.Error(), "declares no development components") {
		t.Fatalf("explicit production-only template error = %v, want development-component validation", err)
	}
	info, err := resolveProjectCreateTemplate(context.Background(), client, "database", true)
	if err != nil {
		t.Fatalf("inferred production-only template returned error: %v", err)
	}
	if info != nil {
		t.Fatalf("inferred production-only template = %+v, want safe unbound fallback", info)
	}
}

func TestResolveProjectCreateTemplateSurfacesOperationalErrors(t *testing.T) {
	dynamicClient := fake.NewSimpleDynamicClientWithCustomListKinds(
		runtime.NewScheme(),
		map[schema.GroupVersionResource]string{templatesGVR: "TemplateList"},
	)
	forbidden := apierrors.NewForbidden(
		schema.GroupResource{Group: templatesGVR.Group, Resource: templatesGVR.Resource},
		"application",
		nil,
	)
	dynamicClient.PrependReactor("get", "templates", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, forbidden
	})
	_, err := resolveProjectCreateTemplate(
		context.Background(),
		asclient.NewFromDynamic(dynamicClient),
		"application",
		true,
	)
	if !apierrors.IsForbidden(err) {
		t.Fatalf("inferred forbidden error = %v, want the operational error surfaced", err)
	}
}

func newProjectCreationTestClient(objects ...runtime.Object) *asclient.Client {
	return asclient.NewFromDynamic(newProjectCreationTestDynamicClient(objects...))
}

func newProjectCreationTestDynamicClient(objects ...runtime.Object) *fake.FakeDynamicClient {
	return fake.NewSimpleDynamicClientWithCustomListKinds(
		runtime.NewScheme(),
		map[schema.GroupVersionResource]string{
			asclient.ProjectGVR: "ProjectList",
			templatesGVR:        "TemplateList",
			codeConnectionsGVR:  "ConnectionList",
			codeRepositoriesGVR: "RepositoryList",
			{
				Group: "infrastructure.faros.sh", Version: "v1alpha1", Resource: "instances",
			}: "InstanceList",
		},
		objects...,
	)
}

func TestGenerateProjectAssistantStreamWithStartUsesInitialCreationGrant(t *testing.T) {
	messages := store.NewMemoryStore()
	workspaces := workspace.NewFileStore(t.TempDir())
	server := NewWithWorkspace(nil, messages, workspaces, "", false)
	id := identity{orgUUID: "org-a", workspaceUUID: "ws-1", tenantPath: "root:org-a:ws-1"}
	project := &aiv1alpha1.Project{}
	project.Name = "demo"
	project.UID = "test-project-uid-demo"
	if err := appendProjectUserMessage(context.Background(), messages, testProjectMessageScope(id.orgUUID, id.workspaceUUID, project.Name), "Build a todo app"); err != nil {
		t.Fatalf("append user message: %v", err)
	}
	engine := &capturingProjectAssistantEngine{}
	server.assistantEngine = engine
	settings := projectLLMSettings{Provider: defaultProjectLLMProvider, BaseURL: defaultProjectLLMBaseURL, Model: "test-model", APIKey: "test-key"}
	client := asclient.NewFromDynamic(projectSettingsDynamicClient{secret: projectLLMSettingsSecret(settings)})
	start := &projectAssistantStreamStart{InitialApprovedPlan: ptrProjectAssistantApprovedPlan(projectAssistantInitialCreationPlan())}
	request := httptest.NewRequest(http.MethodPost, "/", nil)
	request = request.WithContext(context.WithValue(request.Context(), projectAssistantSupervisorRunContextKey{}, store.AssistantRun{
		ID: "run-initial", Mode: store.AssistantRunModeDefault, Status: store.AssistantRunStatusRunning,
	}))
	_, err := server.generateProjectAssistantStreamWithStart(request, id, client, project, projectAssistantStreamCallbacks{}, start)
	if err != nil {
		t.Fatalf("generateProjectAssistantStreamWithStart returned error: %v", err)
	}
	if engine.req.TurnPolicy.profile != projectAssistantTurnProfileImplementation {
		t.Fatalf("turn policy = %#v, want implementation", engine.req.TurnPolicy)
	}
	if engine.req.InitialApprovedPlan == nil {
		t.Fatal("initial stream request omitted the run-local creation grant")
	}
}

func TestReserveProjectExternalOperationRejectsActiveAssistant(t *testing.T) {
	messages := store.NewMemoryStore()
	server := NewWithWorkspace(nil, messages, workspace.NewFileStore(t.TempDir()), "", false)
	id := identity{orgUUID: "org-a", workspaceUUID: "ws-1", user: "alice"}
	project := &aiv1alpha1.Project{}
	project.Name = "demo"
	project.UID = "project-uid-demo"
	scope := projectMessageScope(id.orgUUID, id.workspaceUUID, project)
	started, err := server.startProjectAssistantRunDurablyWithMode(
		context.Background(),
		scope,
		id.user,
		"fix the app",
		"external-operation-gate",
		store.AssistantRunModeDefault,
		func(run store.AssistantRun, assistant store.Message, _ bool) error {
			_, attachErr := server.projectAssistantSupervisor().Attach(scope, run, assistant)
			return attachErr
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { server.projectAssistantSupervisor().Abort(scope, started.Run.ID) })

	recorder := httptest.NewRecorder()
	release, ok := server.reserveProjectExternalOperation(
		recorder,
		context.Background(),
		id,
		project,
		"loading the workspace from git",
	)
	if release != nil {
		release()
	}
	if ok || recorder.Code != http.StatusConflict {
		t.Fatalf("operation reservation = (%t, %d, %q), want conflict", ok, recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "active assistant run") {
		t.Fatalf("conflict body = %q, want active assistant run guidance", recorder.Body.String())
	}
}

type capturingProjectAssistantEngine struct {
	req projectAssistantRunRequest
}

func (e *capturingProjectAssistantEngine) StreamProjectAssistant(_ context.Context, req projectAssistantRunRequest) (projectAssistantRunResult, error) {
	e.req = req
	return projectAssistantRunResult{Content: "done"}, nil
}

func (*capturingProjectAssistantEngine) ResumeProjectAssistant(context.Context, projectAssistantRunRequest, projectAssistantResumeRequest, projectAssistantCheckpointState) (projectAssistantRunResult, error) {
	return projectAssistantRunResult{}, nil
}

func TestAppendUniqueProjectMemoryEntries(t *testing.T) {
	got := appendUniqueProjectMemoryEntries(
		[]string{"Keep the existing goal", "  Preserve spacing after trim  ", "Keep the existing goal"},
		[]string{"Preserve spacing after trim", "Add a verified preview", "", " Add a verified preview "},
	)
	want := []string{"Keep the existing goal", "Preserve spacing after trim", "Add a verified preview"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("appendUniqueProjectMemoryEntries() = %#v, want %#v", got, want)
	}
}
