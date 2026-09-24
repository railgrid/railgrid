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

package bootstrap

import (
	"context"
	"reflect"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic/fake"
	clienttesting "k8s.io/client-go/testing"

	tenancyv1alpha1 "github.com/railgrid/railgrid/apis/tenancy/v1alpha1"
)

func migrationOrg(name string, legacy bool) *unstructured.Unstructured {
	org := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "tenants.railgrid.ai/v1alpha1", "kind": "Organization",
		"metadata": map[string]any{"name": name, "labels": map[string]any{"existing": "keep"}},
		"spec":     map[string]any{"displayName": name},
	}}
	if legacy {
		_ = unstructured.SetNestedMap(org.Object, map[string]any{"name": "stable-uuid", "user": "alice"}, "spec", "initialWorkspace")
	}
	return org
}

func TestPreserveInitialWorkspaceRequests(t *testing.T) {
	pending := migrationOrg("pending", true)
	complete := migrationOrg("complete", true)
	status := map[string]any{"conditions": []any{map[string]any{"type": "InitialWorkspaceInitialized", "status": "True"}}}
	_ = unstructured.SetNestedMap(complete.Object, status, "status")
	legacy := migrationOrg("legacy", false)
	dyn := fake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		organizationMigrationGVR: "OrganizationList",
	}, pending, complete, legacy)
	ctx := context.Background()
	if err := PreserveInitialWorkspaceRequests(ctx, dyn); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"pending", "complete"} {
		got, err := dyn.Resource(organizationMigrationGVR).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if got.GetLabels()[tenancyv1alpha1.OrganizationCreatorLabel] != "alice" || got.GetLabels()["existing"] != "keep" ||
			got.GetAnnotations()[initialWorkspaceMigrationAnnotation] != "stable-uuid" ||
			got.GetAnnotations()[tenancyv1alpha1.OrganizationBootstrapAnnotation] != tenancyv1alpha1.OrganizationBootstrapVersion {
			t.Fatalf("lost migration metadata: %#v", got.Object)
		}
		if name == "complete" && !reflect.DeepEqual(got.Object["status"], status) {
			t.Fatalf("changed completion status: %#v", got.Object["status"])
		}
	}
	got, err := dyn.Resource(organizationMigrationGVR).Get(ctx, "legacy", metav1.GetOptions{})
	if err != nil || !reflect.DeepEqual(got, legacy) {
		t.Fatalf("legacy org changed: got=%#v err=%v", got, err)
	}
	dyn.ClearActions()
	if err := PreserveInitialWorkspaceRequests(ctx, dyn); err != nil {
		t.Fatal(err)
	}
	for _, action := range dyn.Actions() {
		if action.GetVerb() == "patch" || action.GetVerb() == "update" {
			t.Fatal("migration repeated a write after restart")
		}
	}
}

func TestPreserveInitialWorkspaceRequestsStopsOnConflict(t *testing.T) {
	org := migrationOrg("conflicting", true)
	org.SetAnnotations(map[string]string{initialWorkspaceMigrationAnnotation: "different-uuid"})
	dyn := fake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		organizationMigrationGVR: "OrganizationList",
	}, org)
	if err := PreserveInitialWorkspaceRequests(context.Background(), dyn); err == nil {
		t.Fatal("must stop schema upgrade instead of losing the existing workspace identity")
	}
}

func TestPreserveInitialWorkspaceRequestsFreshInstallAndListFailure(t *testing.T) {
	for _, missing := range []bool{true, false} {
		dyn := fake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
			organizationMigrationGVR: "OrganizationList",
		})
		dyn.PrependReactor("list", "organizations", func(clienttesting.Action) (bool, runtime.Object, error) {
			if missing {
				return true, nil, apierrors.NewNotFound(organizationMigrationGVR.GroupResource(), "")
			}
			return true, nil, apierrors.NewServiceUnavailable("unavailable")
		})
		err := PreserveInitialWorkspaceRequests(context.Background(), dyn)
		if (err == nil) != missing {
			t.Fatalf("missing=%v: unexpected error %v", missing, err)
		}
	}
}
