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
	dynamicClient := fake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		templateResource.GVR:                       "TemplateList",
		tenant.InfrastructureInstancesResource.GVR: "InstanceList",
	}, template, instance)
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
	select {
	case <-secondTargetValidated:
	case <-time.After(2 * time.Second):
		close(releaseFirst)
		t.Fatal("second runtime target was not resolved")
	}
	select {
	case <-secondRequest:
		close(releaseFirst)
		t.Fatal("second runtime environment request entered while the first held the project operation")
	case <-time.After(50 * time.Millisecond):
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
	case <-secondRequest:
	case <-time.After(2 * time.Second):
		t.Fatal("second runtime environment request did not run after the first released the operation")
	}
	if requestCount.Load() != 2 {
		t.Fatalf("data-plane request count = %d, want one request per serialized call", requestCount.Load())
	}
}
