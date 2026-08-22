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

package instance

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic/fake"
)

func runSandboxTokenPod(name, namespace, instanceName, templateName string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "Pod",
		"metadata": map[string]any{
			"name":      name,
			"namespace": namespace,
			"labels": map[string]any{
				runSandboxNameLabel:      templateName,
				runSandboxComponentLabel: runSandboxDevComponent,
				runSandboxManagedByLabel: runSandboxManagedBy,
				runSandboxJobNameLabel:   instanceName + "-dev-token",
			},
		},
	}}
}

func runSandboxTokenJob(name, namespace, instanceName, templateName string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "batch/v1",
		"kind":       "Job",
		"metadata": map[string]any{
			"name":      name,
			"namespace": namespace,
			"labels": map[string]any{
				runSandboxNameLabel:      templateName,
				runSandboxComponentLabel: runSandboxDevComponent,
				runSandboxManagedByLabel: runSandboxManagedBy,
			},
		},
	}}
}

func runSandboxTokenSecret(name, namespace, templateName string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "Secret",
		"metadata": map[string]any{
			"name":      name,
			"namespace": namespace,
			"labels": map[string]any{
				runSandboxNameLabel:      templateName,
				runSandboxComponentLabel: runSandboxDevComponent,
				runSandboxManagedByLabel: runSandboxManagedBy,
			},
		},
	}}
}

func TestCleanupRunSandboxTokenPodsIsExactAndTemplateScoped(t *testing.T) {
	ctx := context.Background()
	const (
		namespace    = "faros-sandbox-tenant-default"
		instanceName = "run-123"
	)
	matching := runSandboxTokenPod("run-123-dev-token-abc", namespace, instanceName, runSandboxTemplateName)
	wrongJob := runSandboxTokenPod("other-dev-token-abc", namespace, "other", runSandboxTemplateName)
	nonRun := runSandboxTokenPod("run-123-dev-token-app", namespace, instanceName, "worker")
	client := fake.NewSimpleDynamicClient(runtime.NewScheme(), matching, wrongJob, nonRun)

	done, err := cleanupRunSandboxTokenPods(ctx, client, runSandboxTemplateName, namespace, instanceName)
	if err != nil {
		t.Fatalf("cleanupRunSandboxTokenPods() error = %v", err)
	}
	if done {
		t.Fatal("cleanup reported done before a matching pod deletion was observed")
	}
	if _, err := client.Resource(podsGVR).Namespace(namespace).Get(ctx, matching.GetName(), metav1.GetOptions{}); err == nil {
		t.Fatal("matching token pod still exists after deletion request")
	}
	if _, err := client.Resource(podsGVR).Namespace(namespace).Get(ctx, wrongJob.GetName(), metav1.GetOptions{}); err != nil {
		t.Fatalf("wrong-job pod was affected: %v", err)
	}
	if _, err := client.Resource(podsGVR).Namespace(namespace).Get(ctx, nonRun.GetName(), metav1.GetOptions{}); err != nil {
		t.Fatalf("non-run pod was affected: %v", err)
	}

	done, err = cleanupRunSandboxTokenPods(ctx, client, runSandboxTemplateName, namespace, instanceName)
	if err != nil {
		t.Fatalf("second cleanupRunSandboxTokenPods() error = %v", err)
	}
	if !done {
		t.Fatal("cleanup did not report complete after matching pod disappeared")
	}
}

func TestCleanupRunSandboxTokenPodsSkipsNonRunTemplates(t *testing.T) {
	ctx := context.Background()
	const namespace = "faros-sandbox-tenant-default"
	matching := runSandboxTokenPod("run-123-dev-token-abc", namespace, "run-123", runSandboxTemplateName)
	client := fake.NewSimpleDynamicClient(runtime.NewScheme(), matching)

	done, err := cleanupRunSandboxTokenPods(ctx, client, "worker", namespace, "run-123")
	if err != nil {
		t.Fatalf("cleanupRunSandboxTokenPods() error = %v", err)
	}
	if !done {
		t.Fatal("non-run template cleanup should be a no-op")
	}
	if _, err := client.Resource(podsGVR).Namespace(namespace).Get(ctx, matching.GetName(), metav1.GetOptions{}); err != nil {
		t.Fatalf("non-run cleanup deleted run pod: %v", err)
	}
}

func TestCleanupRunSandboxTokenResourcesIsExactAndComplete(t *testing.T) {
	ctx := context.Background()
	const (
		namespace    = "faros-sandbox-tenant-default"
		instanceName = "run-123"
	)
	matchingPod := runSandboxTokenPod("run-123-dev-token-abc", namespace, instanceName, runSandboxTemplateName)
	matchingJob := runSandboxTokenJob(instanceName+"-dev-token", namespace, instanceName, runSandboxTemplateName)
	matchingSecret := runSandboxTokenSecret(instanceName+"-dev-control", namespace, runSandboxTemplateName)
	wrongJob := runSandboxTokenJob("other-dev-token", namespace, "other", runSandboxTemplateName)
	wrongSecret := runSandboxTokenSecret("other-dev-control", namespace, runSandboxTemplateName)
	client := fake.NewSimpleDynamicClient(runtime.NewScheme(), matchingPod, matchingJob, matchingSecret, wrongJob, wrongSecret)

	done, err := cleanupRunSandboxTokenResources(ctx, client, runSandboxTemplateName, namespace, instanceName)
	if err != nil {
		t.Fatalf("cleanupRunSandboxTokenResources() error = %v", err)
	}
	if done {
		t.Fatal("cleanup reported done before matching token resources disappeared")
	}
	for _, resource := range []struct {
		gvr  schema.GroupVersionResource
		name string
	}{
		{podsGVR, matchingPod.GetName()},
		{jobsGVR, matchingJob.GetName()},
		{secretsGVR, matchingSecret.GetName()},
	} {
		if _, err := client.Resource(resource.gvr).Namespace(namespace).Get(ctx, resource.name, metav1.GetOptions{}); err == nil {
			t.Fatalf("matching token resource %s/%s still exists", resource.gvr.Resource, resource.name)
		}
	}
	for _, resource := range []struct {
		gvr  schema.GroupVersionResource
		name string
	}{
		{jobsGVR, wrongJob.GetName()},
		{secretsGVR, wrongSecret.GetName()},
	} {
		if _, err := client.Resource(resource.gvr).Namespace(namespace).Get(ctx, resource.name, metav1.GetOptions{}); err != nil {
			t.Fatalf("unrelated token resource %s/%s was affected: %v", resource.gvr.Resource, resource.name, err)
		}
	}

	done, err = cleanupRunSandboxTokenResources(ctx, client, runSandboxTemplateName, namespace, instanceName)
	if err != nil {
		t.Fatalf("second cleanupRunSandboxTokenResources() error = %v", err)
	}
	if !done {
		t.Fatal("cleanup did not report complete after matching resources disappeared")
	}
}
