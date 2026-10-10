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

package api

import (
	"context"
	"reflect"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic/fake"
	clienttesting "k8s.io/client-go/testing"

	aiv1alpha1 "github.com/railgrid/provider-app-studio/apis/ai/v1alpha1"
	asclient "github.com/railgrid/provider-app-studio/client"
)

func workerRuntimeProjectionProject() *aiv1alpha1.Project {
	return &aiv1alpha1.Project{
		ObjectMeta: metav1.ObjectMeta{Name: "lab", UID: "lab-uid"},
		Spec: aiv1alpha1.ProjectSpec{Environments: []aiv1alpha1.ProjectEnvironmentSpec{{
			Name: "development", Mode: aiv1alpha1.ProjectEnvironmentModeLive,
			Bindings: []aiv1alpha1.ProjectProviderBindingSpec{
				{Name: "dev", Provider: "infrastructure", Kind: aiv1alpha1.ProjectBindingKindProviderResource,
					ResourceRef: &aiv1alpha1.ProjectProviderResourceReference{APIVersion: "infrastructure.railgrid.ai/v1alpha1", Kind: "Instance", Resource: "instances"}},
				{Name: "data", Provider: "external", Kind: aiv1alpha1.ProjectBindingKindProviderReference,
					ResourceRef: &aiv1alpha1.ProjectProviderResourceReference{APIVersion: "external.example/v1", Kind: "Table", Resource: "tables", Name: "sales"}},
			},
		}}},
		Status: aiv1alpha1.ProjectStatus{Environments: []aiv1alpha1.ProjectEnvironmentStatus{{
			Name: "development", Mode: aiv1alpha1.ProjectEnvironmentModeLive, Phase: "Pending",
			Bindings: []aiv1alpha1.ProjectProviderBindingStatus{
				{Name: "dev", Provider: "infrastructure", Phase: "Pending", PreviewURL: "https://old-runtime.example"},
				{Name: "data", Provider: "external", Phase: "Ready", Outputs: map[string]string{"url": "https://reference.example"}},
				{Name: "removed", Phase: "Ready"},
			},
		}}},
	}
}

func TestWorkerRuntimeProjectionReadsOnlyOwnedResources(t *testing.T) {
	p := workerRuntimeProjectionProject()
	original := p.DeepCopy()
	dyn := fake.NewSimpleDynamicClient(runtime.NewScheme())
	var reads []string
	dyn.PrependReactor("get", "*", func(action clienttesting.Action) (bool, runtime.Object, error) {
		get := action.(clienttesting.GetAction)
		reads = append(reads, action.GetResource().Resource+"/"+get.GetName())
		if action.GetResource().Resource != "instances" {
			t.Fatalf("startup read optional integration: %s", action.GetResource())
		}
		return true, &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "infrastructure.railgrid.ai/v1alpha1", "kind": "Instance",
			"metadata": map[string]any{"name": get.GetName()},
			"status":   map[string]any{"phase": "Ready", "outputs": map[string]any{"previewURL": "https://fresh-runtime.example"}},
		}}, nil
	})
	next := projectWithLiveRuntimeBindingStatus(context.Background(), asclient.NewFromDynamic(dyn), p, identity{})
	if len(reads) != 1 {
		t.Fatalf("startup reads = %v, want one owned resource", reads)
	}
	if !reflect.DeepEqual(p, original) || !reflect.DeepEqual(next.Spec, p.Spec) {
		t.Fatal("runtime projection mutated the original project or desired state")
	}
	statuses := next.Status.Environments[0].Bindings
	if len(statuses) != 2 || statuses[0].Phase != "Ready" || projectAssistantRuntimePreviewURL(next) != "https://fresh-runtime.example" {
		t.Fatalf("runtime status = %#v, want fresh owned status and retained reference", statuses)
	}
	if !reflect.DeepEqual(statuses[1], p.Status.Environments[0].Bindings[1]) {
		t.Fatalf("integration mirror changed: %#v", statuses[1])
	}
	statuses[1].Outputs["url"] = "changed"
	if p.Status.Environments[0].Bindings[1].Outputs["url"] != "https://reference.example" {
		t.Fatal("runtime projection shares mutable status with the original project")
	}
}

func TestWorkerRuntimeProjectionClearsMissingOwnedRuntime(t *testing.T) {
	p := workerRuntimeProjectionProject()
	dyn := fake.NewSimpleDynamicClient(runtime.NewScheme())
	next := projectWithLiveRuntimeBindingStatus(context.Background(), asclient.NewFromDynamic(dyn), p, identity{})
	status := next.Status.Environments[0].Bindings[0]
	if status.Phase != "Pending" || status.PreviewURL != "" || status.URL != "" || len(status.Outputs) != 0 {
		t.Fatalf("missing runtime status = %#v, want pending without stale URLs", status)
	}
}

func TestWorkerRuntimeProjectionSkipsUnboundAndNonLiveResources(t *testing.T) {
	p := workerRuntimeProjectionProject()
	p.Spec.Environments[0].Mode = aiv1alpha1.ProjectEnvironmentModeArtifact
	dyn := fake.NewSimpleDynamicClient(runtime.NewScheme())
	if next := projectWithLiveRuntimeBindingStatus(context.Background(), asclient.NewFromDynamic(dyn), p, identity{}); next != p || len(dyn.Actions()) != 0 {
		t.Fatal("non-live environment triggered reads or changed the project")
	}
	if next := projectWithLiveRuntimeBindingStatus(context.Background(), nil, p, identity{}); next != p {
		t.Fatal("nil client should preserve the project")
	}
}
