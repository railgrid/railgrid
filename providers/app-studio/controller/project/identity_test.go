/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package project

import (
	"context"
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	aiv1alpha1 "github.com/faroshq/provider-app-studio/apis/ai/v1alpha1"
)

func TestNewProjectIdentityMCPGrant(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := rbacv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	p := &aiv1alpha1.Project{ObjectMeta: metav1.ObjectMeta{Name: "demo", UID: "project-uid"}}
	name := identityName(p.Name)
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name + "-token", Namespace: "default"}, Data: map[string][]byte{corev1.ServiceAccountTokenKey: []byte("test-token")}}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(secret).Build()
	r := &Reconciler{}
	ctx := context.Background()
	if token, err := r.ensureIdentity(ctx, c, p); err != nil || token != "test-token" {
		t.Fatalf("ensureIdentity = %q, %v", token, err)
	}
	role := &rbacv1.ClusterRole{}
	if err := c.Get(ctx, types.NamespacedName{Name: name}, role); err != nil {
		t.Fatal(err)
	}
	want := rbacv1.PolicyRule{APIGroups: []string{"faros.sh"}, Resources: []string{"mcpservers"}, ResourceNames: []string{"default"}, Verbs: []string{"use"}}
	found := 0
	for _, rule := range role.Rules {
		if reflect.DeepEqual(rule.APIGroups, []string{"faros.sh"}) {
			found++
			if !reflect.DeepEqual(rule, want) {
				t.Fatalf("unexpected MCP grant: %#v", rule)
			}
		}
	}
	if found != 1 {
		t.Fatalf("MCP grants = %d, want one", found)
	}
	binding := &rbacv1.ClusterRoleBinding{}
	if err := c.Get(ctx, types.NamespacedName{Name: name}, binding); err != nil {
		t.Fatal(err)
	}
	if binding.RoleRef.Name != name || !reflect.DeepEqual(binding.Subjects, []rbacv1.Subject{{Kind: "ServiceAccount", Name: name, Namespace: "default"}}) {
		t.Fatalf("wrong identity binding: %#v", binding)
	}
	// Existing roles remain outside the forward-only provisioning contract.
	role.Rules = nil
	if err := c.Update(ctx, role); err != nil {
		t.Fatal(err)
	}
	if _, err := r.ensureIdentity(ctx, c, p); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(ctx, types.NamespacedName{Name: name}, role); err != nil {
		t.Fatal(err)
	}
	if len(role.Rules) != 0 {
		t.Fatal("existing identity was backfilled")
	}
}
