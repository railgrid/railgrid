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
	asclient "github.com/faroshq/provider-app-studio/client"
	"github.com/faroshq/provider-app-studio/store"
	"github.com/faroshq/provider-app-studio/workspace"
)

func TestProjectAssistantTurnDecisionForStreamStartUsesPrecomputedDecision(t *testing.T) {
	want := projectAssistantTurnDecision{
		Profile:              projectAssistantTurnProfileImplementation,
		RequiresCurrentState: true,
		RequestsMutation:     true,
		Confidence:           projectAssistantTurnConfidenceHigh,
	}
	called := false
	got, err := projectAssistantTurnDecisionForStreamStart(context.Background(), func(context.Context, projectAssistantTurnRouteRequest) (projectAssistantTurnDecision, error) {
		called = true
		return projectAssistantTurnDecision{}, nil
	}, projectAssistantTurnRouteRequest{}, &projectAssistantStreamStart{TurnDecision: &want})
	if err != nil {
		t.Fatalf("projectAssistantTurnDecisionForStreamStart returned error: %v", err)
	}
	if called {
		t.Fatal("stream start invoked the ordinary router despite a precomputed decision")
	}
	if got != want {
		t.Fatalf("decision = %#v, want %#v", got, want)
	}
}

func TestProjectInitialBootstrapPromptDigestDoesNotExposePrompt(t *testing.T) {
	digest := projectInitialBootstrapPromptDigest("Build a todo app")
	if digest == projectInitialBootstrapPromptDigest("Build an unbounded platform") || digest == "Build a todo app" {
		t.Fatalf("prompt digest did not distinguish or conceal the creation prompt: %q", digest)
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
	created, err := (&Server{}).createProjectFromRequestWithPreflight(
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
	applicationGVR := schema.GroupVersionResource{
		Group: "infrastructure.kedge.faros.sh", Version: "v1alpha1", Resource: "applications",
	}
	if _, err := client.Resource(providerBindingResource(applicationGVR, "Application"), "").Get(
		context.Background(), created.Name+"-dev", metav1.GetOptions{},
	); err != nil {
		t.Fatalf("development instance was not reconciled: %v", err)
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
		store: store.NewMemoryStore(),
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
	applicationGVR := schema.GroupVersionResource{
		Group: "infrastructure.kedge.faros.sh", Version: "v1alpha1", Resource: "applications",
	}
	if _, err := client.Resource(providerBindingResource(applicationGVR, "Application"), "").Get(
		context.Background(), created.Name+"-dev", metav1.GetOptions{},
	); err != nil {
		t.Fatalf("development instance was not reconciled: %v", err)
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
	created, err := (&Server{}).createProjectFromRequestWithPreflight(
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
	created, err := (&Server{}).createProjectFromRequestWithPreflight(
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
	created, err := (&Server{}).createProjectFromRequestWithPreflight(
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
				Group: "infrastructure.kedge.faros.sh", Version: "v1alpha1", Resource: "applications",
			}: "ApplicationList",
		},
		objects...,
	)
}

func TestGenerateProjectAssistantStreamWithStartBypassesRouter(t *testing.T) {
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
	server.assistantTurnRouter = func(context.Context, projectAssistantTurnRouteRequest) (projectAssistantTurnDecision, error) {
		t.Fatal("ordinary router should not run for a fresh stream preflight")
		return projectAssistantTurnDecision{}, nil
	}
	engine := &capturingProjectAssistantEngine{}
	server.assistantEngine = engine
	settings := projectLLMSettings{Provider: defaultProjectLLMProvider, BaseURL: defaultProjectLLMBaseURL, Model: "test-model", APIKey: "test-key"}
	client := asclient.NewFromDynamic(projectSettingsDynamicClient{secret: projectLLMSettingsSecret(settings)})
	decision := projectAssistantTurnDecision{Profile: projectAssistantTurnProfileImplementation, RequestsMutation: true, Confidence: projectAssistantTurnConfidenceHigh}
	start := &projectAssistantStreamStart{TurnDecision: &decision, InitialApprovedPlan: ptrProjectAssistantApprovedPlan(projectAssistantInitialCreationPlan())}
	_, err := server.generateProjectAssistantStreamWithStart(httptest.NewRequest(http.MethodPost, "/", nil), id, client, project, projectAssistantStreamCallbacks{}, start)
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
