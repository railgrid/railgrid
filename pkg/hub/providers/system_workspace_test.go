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

package providers

import (
	"context"
	"errors"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	adminv1alpha1 "github.com/railgrid/railgrid/apis/admin/v1alpha1"
)

func systemParent(objs ...runtime.Object) *dynamicfake.FakeDynamicClient {
	return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{workspaceGVR: "WorkspaceList"}, objs...)
}

func workspaceObject(name, typeName, phase string, annotations map[string]any) *unstructured.Unstructured {
	meta := map[string]any{"name": name}
	if annotations != nil {
		meta["annotations"] = annotations
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "tenancy.kcp.io/v1alpha1",
		"kind":       "Workspace",
		"metadata":   meta,
		"spec":       map[string]any{"cluster": "cluster-" + name, "type": map[string]any{"name": typeName, "path": "root"}},
		"status":     map[string]any{"phase": phase},
	}}
}

func ownedWorkspace(name, phase string) *unstructured.Unstructured {
	return workspaceObject(name, adminv1alpha1.SystemWorkspaceTypeName, phase,
		map[string]any{adminv1alpha1.SystemWorkspaceAnnotation: name})
}

func TestEnsureSystemWorkspaceRefusesReservedNames(t *testing.T) {
	for _, name := range []string{"controllers", "providers", "tenants"} {
		parent := systemParent(workspaceObject(name, "universal", "Ready", nil))
		_, err := ensureSystemWorkspace(context.Background(), parent, name, time.Millisecond, 50*time.Millisecond)
		if !errors.Is(err, ErrSystemWorkspaceNameReserved) {
			t.Errorf("%s: got %v, want ErrSystemWorkspaceNameReserved", name, err)
		}
		if len(parent.Actions()) != 0 {
			t.Errorf("%s: a reserved name reached kcp: %v", name, parent.Actions())
		}
	}
}

func TestEnsureSystemWorkspaceCreatesAnOwnedWorkspaceOfTheSystemType(t *testing.T) {
	parent := systemParent()
	// The fake never moves the workspace to Ready, so the wait times out; what
	// is under test is the object that was created.
	if _, err := ensureSystemWorkspace(context.Background(), parent, "switchboard", time.Millisecond, 20*time.Millisecond); err == nil {
		t.Fatal("expected the wait for Ready to time out against a fake")
	}
	got, err := parent.Resource(workspaceGVR).Get(context.Background(), "switchboard", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("workspace was not created: %v", err)
	}
	if !ownedSystemWorkspace(got, "switchboard") {
		t.Errorf("created workspace is not marked as owned: annotations=%v spec=%v", got.GetAnnotations(), got.Object["spec"])
	}
}

func TestEnsureSystemWorkspaceReturnsTheClusterOfAnOwnedReadyWorkspace(t *testing.T) {
	parent := systemParent(ownedWorkspace("switchboard", "Ready"))
	cluster, err := ensureSystemWorkspace(context.Background(), parent, "switchboard", time.Millisecond, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if cluster != "cluster-switchboard" {
		t.Errorf("cluster = %q", cluster)
	}
}

func TestEnsureSystemWorkspaceLeavesAWorkspaceItDoesNotOwn(t *testing.T) {
	cases := map[string]*unstructured.Unstructured{
		"made by hand, no annotation": workspaceObject("metering", "universal", "Ready", nil),
		"annotated for another name": workspaceObject("metering", adminv1alpha1.SystemWorkspaceTypeName, "Ready",
			map[string]any{adminv1alpha1.SystemWorkspaceAnnotation: "other"}),
		"annotated but of another type": workspaceObject("metering", "universal", "Ready",
			map[string]any{adminv1alpha1.SystemWorkspaceAnnotation: "metering"}),
	}
	for name, existing := range cases {
		t.Run(name, func(t *testing.T) {
			parent := systemParent(existing)
			_, err := ensureSystemWorkspace(context.Background(), parent, "metering", time.Millisecond, time.Second)
			if !errors.Is(err, ErrSystemWorkspaceNotOwned) {
				t.Fatalf("got %v, want ErrSystemWorkspaceNotOwned", err)
			}
			for _, a := range parent.Actions() {
				if a.GetVerb() == "update" || a.GetVerb() == "patch" || a.GetVerb() == "delete" {
					t.Errorf("the workspace was written: %s", a.GetVerb())
				}
			}
		})
	}
}

func TestDeleteSystemWorkspaceOnlyDeletesWhatItOwns(t *testing.T) {
	ctx := context.Background()
	exists := func(t *testing.T, parent *dynamicfake.FakeDynamicClient, name string) bool {
		t.Helper()
		_, err := parent.Resource(workspaceGVR).Get(ctx, name, metav1.GetOptions{})
		if err != nil && !apierrors.IsNotFound(err) {
			t.Fatal(err)
		}
		return err == nil
	}

	t.Run("owned", func(t *testing.T) {
		parent := systemParent(ownedWorkspace("switchboard", "Ready"))
		if err := deleteSystemWorkspace(ctx, parent, "switchboard"); err != nil {
			t.Fatal(err)
		}
		if exists(t, parent, "switchboard") {
			t.Error("an owned workspace was not deleted")
		}
	})
	t.Run("not owned", func(t *testing.T) {
		parent := systemParent(workspaceObject("metering", "universal", "Ready", nil))
		if err := deleteSystemWorkspace(ctx, parent, "metering"); err != nil {
			t.Fatal(err)
		}
		if !exists(t, parent, "metering") {
			t.Error("a workspace the hub does not own was deleted")
		}
	})
	t.Run("reserved, even if someone annotated it", func(t *testing.T) {
		parent := systemParent(ownedWorkspace("tenants", "Ready"))
		if err := deleteSystemWorkspace(ctx, parent, "tenants"); err != nil {
			t.Fatal(err)
		}
		if !exists(t, parent, "tenants") {
			t.Error("a reserved workspace was deleted")
		}
	})
	t.Run("missing", func(t *testing.T) {
		if err := deleteSystemWorkspace(ctx, systemParent(), "gone"); err != nil {
			t.Errorf("deleting a missing workspace: %v", err)
		}
	})
}
