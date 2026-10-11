/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package api

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic/fake"
	ktesting "k8s.io/client-go/testing"

	aiv1alpha1 "github.com/railgrid/provider-app-studio/apis/ai/v1alpha1"
	asclient "github.com/railgrid/provider-app-studio/client"
	"github.com/railgrid/provider-app-studio/tenant"
	"github.com/railgrid/provider-app-studio/workspace"
)

type projectAssistantRuntimeEnvRoundTripper func(*http.Request) (*http.Response, error)

func (f projectAssistantRuntimeEnvRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func runtimeProjectObject(t *testing.T, project *aiv1alpha1.Project) *unstructured.Unstructured {
	t.Helper()
	object, err := runtime.DefaultUnstructuredConverter.ToUnstructured(project)
	if err != nil {
		t.Fatalf("convert runtime project: %v", err)
	}
	resource := &unstructured.Unstructured{Object: object}
	resource.SetAPIVersion(aiv1alpha1.SchemeGroupVersion.String())
	resource.SetKind("Project")
	return resource
}

func waitForQueuedProjectRuntimeOperation(t *testing.T, server *Server, scope workspace.Scope) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		server.projectRuntimeOperationsMu.Lock()
		op := server.projectRuntimeOperations[scope]
		queued := op != nil && op.references > 1
		server.projectRuntimeOperationsMu.Unlock()
		if queued {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("runtime action did not queue behind the held project operation")
}

func TestSetRuntimeEnvSerializesWithOtherRuntimeMutations(t *testing.T) {
	ctx := context.Background()
	project := &aiv1alpha1.Project{ObjectMeta: metav1.ObjectMeta{Name: "demo", UID: "project-uid"}}
	project.Spec.Template = &aiv1alpha1.ProjectTemplateSpec{Name: "application"}
	template := applicationTemplateObject()
	unstructured.RemoveNestedField(template.Object, "spec", "development", "components", "backend")
	instance := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "infrastructure.railgrid.ai/v1alpha1",
		"kind":       "Instance",
		"metadata":   map[string]any{"name": projectTemplateInstanceName(project)},
	}}
	projectObject := runtimeProjectObject(t, project)
	dynamicClient := fake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		asclient.ProjectGVR:                        "ProjectList",
		templateResource.GVR:                       "TemplateList",
		tenant.InfrastructureInstancesResource.GVR: "InstanceList",
	}, projectObject, template, instance)
	secondTargetValidated := make(chan struct{})
	var instanceReads int
	var instanceReadsMu sync.Mutex
	dynamicClient.PrependReactor("get", "instances", func(ktesting.Action) (bool, runtime.Object, error) {
		instanceReadsMu.Lock()
		instanceReads++
		if instanceReads == 2 {
			close(secondTargetValidated)
		}
		instanceReadsMu.Unlock()
		return false, nil, nil
	})

	firstRequest := make(chan struct{})
	secondRequest := make(chan struct{})
	releaseFirst := make(chan struct{})
	var requestCount atomic.Int32
	server := testDataPlaneServer()
	server.sandboxDataPlaneClientFactory = func(time.Duration) *http.Client {
		return &http.Client{Transport: projectAssistantRuntimeEnvRoundTripper(func(req *http.Request) (*http.Response, error) {
			switch requestCount.Add(1) {
			case 1:
				close(firstRequest)
				<-releaseFirst
			default:
				close(secondRequest)
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{"ok":true}`)),
				Request:    req,
			}, nil
		})}
	}
	projectScope := workspace.Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-a", ProjectName: project.Name, ProjectUID: string(project.UID)}
	runCtx := projectAssistantWorkflowRunContext{
		Server: server, Client: asclient.NewFromDynamic(dynamicClient), Project: project,
		Identity: identity{clusterID: "rgl3jcl2cfl3xa5p", user: "alice"}, WorkspaceScope: projectScope,
	}
	noRestart := false
	call := setProjectAssistantRuntimeEnv(runCtx)
	results := make(chan error, 2)
	invoke := func(value string) {
		_, err := call(ctx, &projectAssistantRuntimeEnvToolInput{Env: map[string]string{"TEST_VALUE": value}, Restart: &noRestart})
		results <- err
	}
	go invoke("first")
	select {
	case <-firstRequest:
	case <-time.After(2 * time.Second):
		t.Fatal("first runtime environment request did not reach the data plane")
	}
	go invoke("second")
	waitForQueuedProjectRuntimeOperation(t, server, projectScope)
	select {
	case <-secondTargetValidated:
		close(releaseFirst)
		t.Fatal("second runtime target was resolved before it acquired the project operation")
	case <-secondRequest:
		close(releaseFirst)
		t.Fatal("second runtime environment request entered while the first held the project operation")
	case <-time.After(50 * time.Millisecond):
	}
	close(releaseFirst)
	select {
	case <-secondTargetValidated:
	case <-time.After(2 * time.Second):
		t.Fatal("second runtime target was not resolved after acquiring the project operation")
	}
	for range 2 {
		select {
		case err := <-results:
			if err != nil {
				t.Fatalf("set_runtime_env: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("serialized runtime environment request did not finish")
		}
	}
	select {
	case <-secondRequest:
	case <-time.After(2 * time.Second):
		t.Fatal("second runtime environment request did not run after the first released the operation")
	}
	if requestCount.Load() != 2 {
		t.Fatalf("data-plane request count = %d, want one request per serialized call", requestCount.Load())
	}
}

func TestSetRuntimeEnvRefreshesTargetAfterWaiting(t *testing.T) {
	ctx := context.Background()
	project := &aiv1alpha1.Project{ObjectMeta: metav1.ObjectMeta{Name: "demo", UID: "project-uid"}}
	project.Spec.Template = &aiv1alpha1.ProjectTemplateSpec{Name: "application"}
	template := applicationTemplateObject()
	unstructured.RemoveNestedField(template.Object, "spec", "development", "components", "backend")
	instance := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "infrastructure.railgrid.ai/v1alpha1",
		"kind":       "Instance",
		"metadata":   map[string]any{"name": projectTemplateInstanceName(project)},
	}}
	dynamicClient := fake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		asclient.ProjectGVR:                        "ProjectList",
		templateResource.GVR:                       "TemplateList",
		tenant.InfrastructureInstancesResource.GVR: "InstanceList",
	}, runtimeProjectObject(t, project), template, instance)
	secondTargetValidated := make(chan struct{})
	var instanceReads int
	var instanceReadsMu sync.Mutex
	dynamicClient.PrependReactor("get", "instances", func(ktesting.Action) (bool, runtime.Object, error) {
		instanceReadsMu.Lock()
		instanceReads++
		if instanceReads == 2 {
			close(secondTargetValidated)
		}
		instanceReadsMu.Unlock()
		return false, nil, nil
	})

	firstRequest := make(chan struct{})
	secondRequest := make(chan struct{})
	releaseFirst := make(chan struct{})
	var requestCount atomic.Int32
	server := testDataPlaneServer()
	server.sandboxDataPlaneClientFactory = func(time.Duration) *http.Client {
		return &http.Client{Transport: projectAssistantRuntimeEnvRoundTripper(func(req *http.Request) (*http.Response, error) {
			switch requestCount.Add(1) {
			case 1:
				close(firstRequest)
				<-releaseFirst
			default:
				if component := req.URL.Query().Get("component"); component != "newfrontend" {
					t.Errorf("queued runtime request used stale component %q after template change", component)
				}
				close(secondRequest)
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{"ok":true}`)),
				Request:    req,
			}, nil
		})}
	}
	projectScope := workspace.Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-a", ProjectName: project.Name, ProjectUID: string(project.UID)}
	runCtx := projectAssistantWorkflowRunContext{
		Server: server, Client: asclient.NewFromDynamic(dynamicClient), Project: project,
		Identity: identity{clusterID: "rgl3jcl2cfl3xa5p", user: "alice"}, WorkspaceScope: projectScope,
	}
	noRestart := false
	call := setProjectAssistantRuntimeEnv(runCtx)
	results := make(chan error, 2)
	invoke := func(value string) {
		_, err := call(ctx, &projectAssistantRuntimeEnvToolInput{Env: map[string]string{"TEST_VALUE": value}, Restart: &noRestart})
		results <- err
	}
	go invoke("first")
	select {
	case <-firstRequest:
	case <-time.After(2 * time.Second):
		t.Fatal("first runtime environment request did not reach the data plane")
	}
	go invoke("second")
	waitForQueuedProjectRuntimeOperation(t, server, projectScope)
	select {
	case <-secondTargetValidated:
		close(releaseFirst)
		t.Fatal("second runtime target was resolved before it acquired the project operation")
	case <-secondRequest:
		close(releaseFirst)
		t.Fatal("second runtime environment request entered while the first held the project operation")
	case <-time.After(50 * time.Millisecond):
	}

	oldComponent, _, err := unstructured.NestedMap(template.Object, "spec", "development", "components", "frontend")
	if err != nil {
		close(releaseFirst)
		t.Fatal(err)
	}
	unstructured.RemoveNestedField(template.Object, "spec", "development", "components", "frontend")
	if err := unstructured.SetNestedMap(template.Object, oldComponent, "spec", "development", "components", "newfrontend"); err != nil {
		close(releaseFirst)
		t.Fatal(err)
	}
	if _, err := dynamicClient.Resource(templateResource.GVR).Update(ctx, template, metav1.UpdateOptions{}); err != nil {
		close(releaseFirst)
		t.Fatal(err)
	}
	close(releaseFirst)

	for range 2 {
		select {
		case err := <-results:
			if err != nil {
				t.Fatalf("set_runtime_env: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("serialized runtime environment request did not finish")
		}
	}
	select {
	case <-secondTargetValidated:
	case <-time.After(2 * time.Second):
		t.Fatal("second runtime target was not resolved after acquiring the project operation")
	}
	select {
	case <-secondRequest:
	case <-time.After(2 * time.Second):
		t.Fatal("second runtime environment request did not run after the first released the operation")
	}
	if requestCount.Load() != 2 {
		t.Fatalf("data-plane request count = %d, want one request per serialized call", requestCount.Load())
	}
}

func TestQueuedRuntimeMutationsRejectReplacedOrDeletingProject(t *testing.T) {
	actions := []struct {
		name   string
		invoke func(context.Context, projectAssistantWorkflowRunContext) (*projectAssistantRuntimeWorkflowResult, error)
	}{
		{
			name: "restart",
			invoke: func(ctx context.Context, runCtx projectAssistantWorkflowRunContext) (*projectAssistantRuntimeWorkflowResult, error) {
				return restartProjectAssistantRuntime(runCtx)(ctx, &projectAssistantRuntimeRestartToolInput{})
			},
		},
		{
			name: "set environment",
			invoke: func(ctx context.Context, runCtx projectAssistantWorkflowRunContext) (*projectAssistantRuntimeWorkflowResult, error) {
				noRestart := false
				return setProjectAssistantRuntimeEnv(runCtx)(ctx, &projectAssistantRuntimeEnvToolInput{
					Env: map[string]string{"TEST_VALUE": "queued"}, Restart: &noRestart,
				})
			},
		},
	}
	projectChanges := []struct {
		name   string
		update func(*unstructured.Unstructured) error
	}{
		{
			name: "UID replaced",
			update: func(project *unstructured.Unstructured) error {
				return unstructured.SetNestedField(project.Object, "replacement-uid", "metadata", "uid")
			},
		},
		{
			name: "deletion started",
			update: func(project *unstructured.Unstructured) error {
				project.SetDeletionTimestamp(&metav1.Time{Time: time.Now().UTC()})
				return nil
			},
		},
	}
	for _, action := range actions {
		for _, change := range projectChanges {
			t.Run(action.name+"/"+change.name, func(t *testing.T) {
				ctx := context.Background()
				project := &aiv1alpha1.Project{ObjectMeta: metav1.ObjectMeta{Name: "demo", UID: "project-uid"}}
				project.Spec.Template = &aiv1alpha1.ProjectTemplateSpec{Name: "application"}
				dynamicClient := fake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
					asclient.ProjectGVR: "ProjectList",
				}, runtimeProjectObject(t, project))
				server := testDataPlaneServer()
				var requestCount atomic.Int32
				server.sandboxDataPlaneClientFactory = func(time.Duration) *http.Client {
					return &http.Client{Transport: projectAssistantRuntimeEnvRoundTripper(func(req *http.Request) (*http.Response, error) {
						requestCount.Add(1)
						return &http.Response{
							StatusCode: http.StatusOK,
							Header:     make(http.Header),
							Body:       io.NopCloser(strings.NewReader(`{"ok":true}`)),
							Request:    req,
						}, nil
					})}
				}
				scope := workspace.Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-a", ProjectName: project.Name, ProjectUID: string(project.UID)}
				runCtx := projectAssistantWorkflowRunContext{
					Server: server, Client: asclient.NewFromDynamic(dynamicClient), Project: project,
					Identity: identity{clusterID: "rgl3jcl2cfl3xa5p", user: "alice"}, WorkspaceScope: scope,
				}
				release, err := server.acquireProjectRuntimeOperation(ctx, scope)
				if err != nil {
					t.Fatalf("acquire project runtime operation: %v", err)
				}
				resultCh := make(chan struct {
					result *projectAssistantRuntimeWorkflowResult
					err    error
				}, 1)
				go func() {
					result, err := action.invoke(ctx, runCtx)
					resultCh <- struct {
						result *projectAssistantRuntimeWorkflowResult
						err    error
					}{result: result, err: err}
				}()
				waitForQueuedProjectRuntimeOperation(t, server, scope)

				projectResource, err := dynamicClient.Resource(asclient.ProjectGVR).Get(ctx, project.Name, metav1.GetOptions{})
				if err != nil {
					release()
					t.Fatalf("get project for update: %v", err)
				}
				if err := change.update(projectResource); err != nil {
					release()
					t.Fatalf("prepare project update: %v", err)
				}
				if _, err := dynamicClient.Resource(asclient.ProjectGVR).Update(ctx, projectResource, metav1.UpdateOptions{}); err != nil {
					release()
					t.Fatalf("update project: %v", err)
				}
				release()

				select {
				case call := <-resultCh:
					if call.err != nil {
						t.Fatalf("runtime action: %v", call.err)
					}
					if call.result == nil || call.result.Status != "unavailable" {
						t.Fatalf("runtime result = %#v, want unavailable", call.result)
					}
				case <-time.After(2 * time.Second):
					t.Fatal("queued runtime action did not finish")
				}
				if got := requestCount.Load(); got != 0 {
					t.Fatalf("data-plane request count = %d, want no request after project %s", got, change.name)
				}
			})
		}
	}
}

func TestQueuedRuntimeMutationsPropagateCancellationDuringProjectRefresh(t *testing.T) {
	actions := []struct {
		name   string
		invoke func(context.Context, projectAssistantWorkflowRunContext) (*projectAssistantRuntimeWorkflowResult, error)
	}{
		{
			name: "restart",
			invoke: func(ctx context.Context, runCtx projectAssistantWorkflowRunContext) (*projectAssistantRuntimeWorkflowResult, error) {
				return restartProjectAssistantRuntime(runCtx)(ctx, &projectAssistantRuntimeRestartToolInput{})
			},
		},
		{
			name: "set environment",
			invoke: func(ctx context.Context, runCtx projectAssistantWorkflowRunContext) (*projectAssistantRuntimeWorkflowResult, error) {
				noRestart := false
				return setProjectAssistantRuntimeEnv(runCtx)(ctx, &projectAssistantRuntimeEnvToolInput{
					Env: map[string]string{"TEST_VALUE": "queued"}, Restart: &noRestart,
				})
			},
		},
	}
	for _, action := range actions {
		t.Run(action.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			project := &aiv1alpha1.Project{ObjectMeta: metav1.ObjectMeta{Name: "demo", UID: "project-uid"}}
			project.Spec.Template = &aiv1alpha1.ProjectTemplateSpec{Name: "application"}
			dynamicClient := fake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
				asclient.ProjectGVR: "ProjectList",
			}, runtimeProjectObject(t, project))
			dynamicClient.PrependReactor("get", "projects", func(ktesting.Action) (bool, runtime.Object, error) {
				cancel()
				return true, nil, context.Canceled
			})
			server := testDataPlaneServer()
			var requestCount atomic.Int32
			server.sandboxDataPlaneClientFactory = func(time.Duration) *http.Client {
				return &http.Client{Transport: projectAssistantRuntimeEnvRoundTripper(func(req *http.Request) (*http.Response, error) {
					requestCount.Add(1)
					return &http.Response{
						StatusCode: http.StatusOK,
						Header:     make(http.Header),
						Body:       io.NopCloser(strings.NewReader(`{"ok":true}`)),
						Request:    req,
					}, nil
				})}
			}
			scope := workspace.Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-a", ProjectName: project.Name, ProjectUID: string(project.UID)}
			runCtx := projectAssistantWorkflowRunContext{
				Server: server, Client: asclient.NewFromDynamic(dynamicClient), Project: project,
				Identity: identity{clusterID: "rgl3jcl2cfl3xa5p", user: "alice"}, WorkspaceScope: scope,
			}
			release, err := server.acquireProjectRuntimeOperation(ctx, scope)
			if err != nil {
				t.Fatalf("acquire project runtime operation: %v", err)
			}
			resultCh := make(chan struct {
				result *projectAssistantRuntimeWorkflowResult
				err    error
			}, 1)
			go func() {
				result, err := action.invoke(ctx, runCtx)
				resultCh <- struct {
					result *projectAssistantRuntimeWorkflowResult
					err    error
				}{result: result, err: err}
			}()
			waitForQueuedProjectRuntimeOperation(t, server, scope)
			release()

			select {
			case call := <-resultCh:
				if !errors.Is(call.err, context.Canceled) || call.result != nil {
					t.Fatalf("runtime action = (%#v, %v), want nil result and context.Canceled", call.result, call.err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("queued runtime action did not finish after cancellation")
			}
			if got := requestCount.Load(); got != 0 {
				t.Fatalf("data-plane request count = %d, want no request after cancellation", got)
			}
		})
	}
}
