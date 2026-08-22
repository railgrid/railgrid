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
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic/fake"
	clienttesting "k8s.io/client-go/testing"

	infrav1alpha1 "github.com/faroshq/provider-infrastructure/apis/v1alpha1"
)

func TestRecordActivityPatchesRuntimeAnnotation(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "infrastructure.faros.sh", Version: "v1alpha1", Resource: "instances"}
	runtimeObject := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "infrastructure.faros.sh/v1alpha1",
		"kind":       "Instance",
		"metadata": map[string]any{
			"name":      "sandbox",
			"namespace": "runtime-ns",
		},
	}}
	client := fake.NewSimpleDynamicClient(runtime.NewScheme(), runtimeObject)
	patchCalls := 0
	client.PrependReactor("patch", "instances", func(action clienttesting.Action) (bool, runtime.Object, error) {
		patchCalls++
		if patchCalls == 1 {
			return true, nil, apierrors.NewConflict(gvr.GroupResource(), "sandbox", nil)
		}
		return false, nil, nil
	})
	rt := &restRuntime{dynamicClient: client}
	instance := &unstructured.Unstructured{Object: map[string]any{
		"status": map[string]any{
			"runtimeNamespace": "runtime-ns",
			"runtimeRef": map[string]any{
				"apiVersion": gvr.GroupVersion().String(),
				"resource":   gvr.Resource,
				"namespace":  "runtime-ns",
				"name":       "sandbox",
			},
		},
	}}

	if err := rt.RecordActivity(context.Background(), instance); err != nil {
		t.Fatalf("RecordActivity() error = %v", err)
	}
	if patchCalls != 2 {
		t.Fatalf("patch calls = %d, want one conflict retry", patchCalls)
	}
	got, err := client.Resource(gvr).Namespace("runtime-ns").Get(context.Background(), "sandbox", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get runtime object: %v", err)
	}
	value := got.GetAnnotations()[infrav1alpha1.FarosLastActivityAnnotation]
	if value == "" {
		t.Fatal("activity annotation is empty")
	}
	if _, err := time.Parse(time.RFC3339Nano, value); err != nil {
		t.Fatalf("activity annotation %q is not RFC3339Nano: %v", value, err)
	}
}
