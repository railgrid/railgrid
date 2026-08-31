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
	if readiness.GitConnection.Status != projectCreateGitStatusConnectionMissing {
		t.Fatalf("GitConnection.Status = %q, want %q", readiness.GitConnection.Status, projectCreateGitStatusConnectionMissing)
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
	if readiness.GitConnection.Status != projectCreateGitStatusReady {
		t.Fatalf("GitConnection.Status = %q, want %q", readiness.GitConnection.Status, projectCreateGitStatusReady)
	}
	if readiness.GitConnection.ConnectionRef != "github" {
		t.Fatalf("GitConnection.ConnectionRef = %q, want github", readiness.GitConnection.ConnectionRef)
	}
	if readiness.GitConnection.Message != "" {
		t.Fatalf("GitConnection.Message = %q, want empty", readiness.GitConnection.Message)
	}
}

func TestProjectCreateReadinessReportsConnectionValidation(t *testing.T) {
	client := newCodeRepositoryTestClient(codeConnectionObjectWithValidated("github", metav1.ConditionFalse))

	readiness, err := projectCreateReadiness(context.Background(), client)
	if err != nil {
		t.Fatalf("projectCreateReadiness returned error: %v", err)
	}
	if readiness.GitConnection.Status != projectCreateGitStatusValidating {
		t.Fatalf("GitConnection.Status = %q, want %q", readiness.GitConnection.Status, projectCreateGitStatusValidating)
	}
	if readiness.GitConnection.Message != "Your Git connection is still validating" {
		t.Fatalf("GitConnection.Message = %q, want validation guidance", readiness.GitConnection.Message)
	}
}

func TestProjectCreateReadinessReportsTerminalConnectionFailure(t *testing.T) {
	client := newCodeRepositoryTestClient(codeConnectionObjectWithValidationCondition(
		"github",
		metav1.ConditionFalse,
		codeConnectionReasonValidationFailed,
		"credential rejected by GitHub",
		true,
	))

	readiness, err := projectCreateReadiness(context.Background(), client)
	if err != nil {
		t.Fatalf("projectCreateReadiness returned error: %v", err)
	}
	if readiness.GitConnection.Status != projectCreateGitStatusFailed {
		t.Fatalf("GitConnection.Status = %q, want %q", readiness.GitConnection.Status, projectCreateGitStatusFailed)
	}
	if readiness.GitConnection.ConnectionRef != "github" {
		t.Fatalf("GitConnection.ConnectionRef = %q, want github", readiness.GitConnection.ConnectionRef)
	}
	if readiness.GitConnection.Message != "credential rejected by GitHub" {
		t.Fatalf("GitConnection.Message = %q, want provider validation detail", readiness.GitConnection.Message)
	}
}

func TestProjectCreateReadinessKeepsTransientConnectionPending(t *testing.T) {
	client := newCodeRepositoryTestClient(codeConnectionObjectWithValidationCondition(
		"github",
		metav1.ConditionFalse,
		codeConnectionReasonCredentialUnavailable,
		"credential Secret is not visible yet",
		true,
	))

	readiness, err := projectCreateReadiness(context.Background(), client)
	if err != nil {
		t.Fatalf("projectCreateReadiness returned error: %v", err)
	}
	if readiness.GitConnection.Status != projectCreateGitStatusValidating {
		t.Fatalf("GitConnection.Status = %q, want %q", readiness.GitConnection.Status, projectCreateGitStatusValidating)
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
	return codeConnectionObjectWithValidationCondition(name, status, "", "", false)
}

func codeConnectionObjectWithValidationCondition(name string, status metav1.ConditionStatus, reason, message string, reconciled bool) *unstructured.Unstructured {
	condition := map[string]any{"type": codeConditionValidated, "status": string(status)}
	if reason != "" {
		condition["reason"] = reason
	}
	if message != "" {
		condition["message"] = message
	}
	u := &unstructured.Unstructured{
		Object: map[string]any{
			"status": map[string]any{
				"conditions": []any{
					condition,
				},
			},
		},
	}
	u.SetAPIVersion(codeSchemeGroupVersion.String())
	u.SetKind("Connection")
	u.SetName(name)
	u.SetGeneration(1)
	if reconciled {
		_ = unstructured.SetNestedField(u.Object, int64(1), "status", "observedGeneration")
	}
	return u
}
