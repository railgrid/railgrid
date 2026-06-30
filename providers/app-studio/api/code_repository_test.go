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
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic/fake"

	asclient "github.com/faroshq/provider-app-studio/client"
)

func TestProjectCreateReadinessRequiresValidatedGitConnection(t *testing.T) {
	client := newCodeRepositoryTestClient()

	readiness, err := projectCreateReadiness(context.Background(), client)
	if err != nil {
		t.Fatalf("projectCreateReadiness returned error: %v", err)
	}
	if readiness.GitConnection.Ready {
		t.Fatalf("GitConnection.Ready = true, want false")
	}
	if readiness.GitConnection.ConnectionRef != "" {
		t.Fatalf("GitConnection.ConnectionRef = %q, want empty", readiness.GitConnection.ConnectionRef)
	}
	if readiness.GitConnection.Message != "You need to connect to a Git account before you can continue" {
		t.Fatalf("GitConnection.Message = %q, want missing connection guidance", readiness.GitConnection.Message)
	}
}

func TestProjectCreateReadinessSelectsValidatedGitConnection(t *testing.T) {
	client := newCodeRepositoryTestClient(
		codeConnectionObjectWithValidated("github", metav1.ConditionTrue),
	)

	readiness, err := projectCreateReadiness(context.Background(), client)
	if err != nil {
		t.Fatalf("projectCreateReadiness returned error: %v", err)
	}
	if !readiness.GitConnection.Ready {
		t.Fatalf("GitConnection.Ready = false, want true")
	}
	if readiness.GitConnection.ConnectionRef != "github" {
		t.Fatalf("GitConnection.ConnectionRef = %q, want github", readiness.GitConnection.ConnectionRef)
	}
	if readiness.GitConnection.Message != "" {
		t.Fatalf("GitConnection.Message = %q, want empty", readiness.GitConnection.Message)
	}
}

func newCodeRepositoryTestClient(objects ...runtime.Object) *asclient.Client {
	return asclient.NewFromDynamic(fake.NewSimpleDynamicClientWithCustomListKinds(
		runtime.NewScheme(),
		map[schema.GroupVersionResource]string{
			codeConnectionsGVR: "ConnectionList",
		},
		objects...,
	))
}

func codeConnectionObjectWithValidated(name string, status metav1.ConditionStatus) *unstructured.Unstructured {
	u := &unstructured.Unstructured{
		Object: map[string]any{
			"status": map[string]any{
				"conditions": []any{
					map[string]any{"type": codeConditionValidated, "status": string(status)},
				},
			},
		},
	}
	u.SetAPIVersion(codeSchemeGroupVersion.String())
	u.SetKind("Connection")
	u.SetName(name)
	return u
}
