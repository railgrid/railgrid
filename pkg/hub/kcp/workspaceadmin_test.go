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

package kcp

import (
	"context"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/fake"
)

func newCRBClient(t *testing.T) dynamic.Interface {
	t.Helper()
	return fake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		clusterRoleBindingGVR: "ClusterRoleBindingList",
	})
}

func crbSubjects(t *testing.T, c dynamic.Interface, name string) []string {
	t.Helper()
	got, err := c.Resource(clusterRoleBindingGVR).Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get %s: %v", name, err)
	}
	subjects, _, _ := unstructured.NestedSlice(got.Object, "subjects")
	var names []string
	for _, s := range subjects {
		names = append(names, s.(map[string]interface{})["name"].(string))
	}
	return names
}

func crbMissing(t *testing.T, c dynamic.Interface, name string) bool {
	t.Helper()
	_, err := c.Resource(clusterRoleBindingGVR).Get(context.Background(), name, metav1.GetOptions{})
	return apierrors.IsNotFound(err)
}

// Regression: the hub used to keep one shared binding per workspace and
// overwrite its only subject on every grant, so granting bob revoked alice.
func TestEnsureWorkspaceAdmin_GrantsAreAdditive(t *testing.T) {
	c := newCRBClient(t)
	ctx := context.Background()
	for _, id := range []string{"faros:alice@example.com", "faros:bob@example.com", "faros:alice@example.com"} {
		if err := ensureWorkspaceAdmin(ctx, c, id); err != nil {
			t.Fatalf("ensure %s: %v", id, err)
		}
	}
	for _, id := range []string{"faros:alice@example.com", "faros:bob@example.com"} {
		if got := crbSubjects(t, c, userAdminCRBName(id)); len(got) != 1 || got[0] != id {
			t.Errorf("binding for %s: subjects %v", id, got)
		}
	}
	if userAdminCRBName("faros:alice@example.com") == userAdminCRBName("faros:bob@example.com") {
		t.Error("per-user binding names collide")
	}
}

func TestEnsureWorkspaceAdmin_MigratesLegacySharedBinding(t *testing.T) {
	c := newCRBClient(t)
	ctx := context.Background()
	legacy := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "rbac.authorization.k8s.io/v1",
		"kind":       "ClusterRoleBinding",
		"metadata":   map[string]interface{}{"name": legacyWorkspaceAdminCRB},
		"roleRef":    map[string]interface{}{"apiGroup": "rbac.authorization.k8s.io", "kind": "ClusterRole", "name": "cluster-admin"},
		"subjects": []interface{}{
			map[string]interface{}{"apiGroup": "rbac.authorization.k8s.io", "kind": "User", "name": "faros:carol@example.com"},
		},
	}}
	if _, err := c.Resource(clusterRoleBindingGVR).Create(ctx, legacy, metav1.CreateOptions{}); err != nil {
		t.Fatalf("seed legacy: %v", err)
	}
	if err := ensureWorkspaceAdmin(ctx, c, "faros:dave@example.com"); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	// carol, who only held access through the shared binding, keeps it.
	if got := crbSubjects(t, c, userAdminCRBName("faros:carol@example.com")); len(got) != 1 || got[0] != "faros:carol@example.com" {
		t.Errorf("carol not migrated: %v", got)
	}
	if got := crbSubjects(t, c, userAdminCRBName("faros:dave@example.com")); len(got) != 1 || got[0] != "faros:dave@example.com" {
		t.Errorf("dave not granted: %v", got)
	}
	if !crbMissing(t, c, legacyWorkspaceAdminCRB) {
		t.Error("legacy shared binding still present")
	}
}

func TestRevokeWorkspaceAdmin_OnlyTouchesThatUser(t *testing.T) {
	c := newCRBClient(t)
	ctx := context.Background()
	for _, id := range []string{"faros:alice@example.com", "faros:bob@example.com"} {
		if err := ensureWorkspaceAdmin(ctx, c, id); err != nil {
			t.Fatalf("ensure %s: %v", id, err)
		}
	}
	if err := revokeWorkspaceAdmin(ctx, c, "faros:bob@example.com"); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if !crbMissing(t, c, userAdminCRBName("faros:bob@example.com")) {
		t.Error("bob still bound")
	}
	if got := crbSubjects(t, c, userAdminCRBName("faros:alice@example.com")); len(got) != 1 {
		t.Errorf("alice lost her binding: %v", got)
	}
	// Idempotent on NotFound.
	if err := revokeWorkspaceAdmin(ctx, c, "faros:bob@example.com"); err != nil {
		t.Errorf("second revoke: %v", err)
	}
}
