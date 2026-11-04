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
	"strings"
	"testing"

	aiv1alpha1 "github.com/railgrid/provider-app-studio/apis/ai/v1alpha1"
	"github.com/railgrid/provider-app-studio/client"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic/fake"
)

func TestRetiredSnapshotCannotBeReactivatedByStudioSpec(t *testing.T) {
	id := "0123456789abcdef0123456789abcdef"
	name := aiv1alpha1.ModelProviderConnectionSecretPrefix + id
	studio := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": aiv1alpha1.SchemeGroupVersion.String(), "kind": "Studio", "metadata": map[string]any{"name": "studio", "uid": "studio-uid"},
		"status": map[string]any{"retiredModelCredentials": []any{map[string]any{"secretName": name, "secretUID": "old-key-uid"}}},
	}}
	makeSecret := func(uid string) *unstructured.Unstructured {
		return &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "v1", "kind": "Secret", "metadata": map[string]any{"name": name, "namespace": "default", "uid": uid, "resourceVersion": "1",
				"labels":          map[string]any{aiv1alpha1.ModelProviderConnectionSecretOwnerLabel: aiv1alpha1.ModelProviderConnectionSecretOwner},
				"annotations":     map[string]any{aiv1alpha1.ModelProviderConnectionGeneratedSecretIDAnnotation: id},
				"ownerReferences": []any{map[string]any{"apiVersion": aiv1alpha1.SchemeGroupVersion.String(), "kind": "Studio", "name": "studio", "uid": "studio-uid"}},
			}, "immutable": true, "data": map[string]any{"apiKey": encodeSecretValue("synthetic-key")},
		}}
	}
	for _, uid := range []string{"old-key-uid", "new-key-uid"} {
		t.Run(uid, func(t *testing.T) {
			dynamic := fake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{studioResource.GVR: "StudioList", secretGVR: "SecretList"}, studio.DeepCopy(), makeSecret(uid))
			snapshot := &aiv1alpha1.StudioLLMConnectionSnapshot{ConnectionRef: "deleted", ConnectionUID: "deleted-uid", Provider: "openai-compatible", BaseURL: "https://api.openai.com/v1", SecretRef: &aiv1alpha1.StudioLLMSecretRef{Name: name}, SecretKey: "apiKey", SecretUID: uid, SecretResourceVersion: "1", ConnectionFingerprint: "fingerprint", GeneratedSecretID: id}
			key, err := readConnectionSnapshotCredential(context.Background(), client.NewFromDynamic(dynamic), snapshot)
			if uid == "old-key-uid" {
				if err == nil || !strings.Contains(err.Error(), "retired") || key != "" {
					t.Fatal("a retired key identity was reactivated")
				}
			} else if err != nil || key != "synthetic-key" {
				t.Fatal("retirement incorrectly matched a different Secret UID")
			}
		})
	}
}
