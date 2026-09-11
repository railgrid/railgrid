// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package tenantaccess

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrlfake "sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func identityScheme() *runtime.Scheme {
	scheme := runtime.NewScheme()
	utilruntime.Must(corev1.AddToScheme(scheme))
	utilruntime.Must(rbacv1.AddToScheme(scheme))
	return scheme
}

// readyToken pre-populates the identity's token Secret so EnsureIdentity
// returns immediately instead of waiting on a token controller.
func readyToken(name, token string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: TokenSecretName(name), Namespace: Namespace},
		Type:       corev1.SecretTypeServiceAccountToken,
		Data:       map[string][]byte{corev1.ServiceAccountTokenKey: []byte(token)},
	}
}

// Providers claim only create on the RBAC types and existing bindings never
// gain verbs, so EnsureIdentity must never try to update an existing
// ClusterRole — a pre-existing role with different rules is left alone and
// the call still succeeds.
func TestEnsureIdentityDoesNotUpdateExistingClusterRole(t *testing.T) {
	const name = "faros-test-identity"
	owner := metav1.OwnerReference{APIVersion: "apis.kcp.io/v1alpha2", Kind: "APIBinding", Name: "test", UID: "b-1"}
	oldRules := []rbacv1.PolicyRule{{APIGroups: []string{"edges.faros.sh"}, Resources: []string{"kubernetesclusters"}, Verbs: []string{"get"}}}
	stale := &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{Name: name, OwnerReferences: []metav1.OwnerReference{owner}},
		Rules:      oldRules,
	}
	cl := ctrlfake.NewClientBuilder().WithScheme(identityScheme()).
		WithObjects(stale, readyToken(name, "tok")).Build()
	before := &rbacv1.ClusterRole{}
	if err := cl.Get(context.Background(), client.ObjectKey{Name: name}, before); err != nil {
		t.Fatal(err)
	}

	newRules := []rbacv1.PolicyRule{{APIGroups: []string{"edges.faros.sh"}, Resources: []string{"kubernetesclusters"}, Verbs: []string{"get", "list", "watch"}}}
	token, err := EnsureIdentity(context.Background(), cl, name, []metav1.OwnerReference{owner}, newRules)
	if err != nil {
		t.Fatalf("EnsureIdentity: %v", err)
	}
	if token != "tok" {
		t.Fatalf("token = %q, want tok", token)
	}

	after := &rbacv1.ClusterRole{}
	if err := cl.Get(context.Background(), client.ObjectKey{Name: name}, after); err != nil {
		t.Fatal(err)
	}
	if !equality.Semantic.DeepEqual(after.Rules, oldRules) || before.ResourceVersion != after.ResourceVersion {
		t.Fatalf("existing ClusterRole was rewritten (rv %s -> %s, rules %+v); EnsureIdentity must be create-only", before.ResourceVersion, after.ResourceVersion, after.Rules)
	}
	if err := cl.Get(context.Background(), client.ObjectKey{Name: name}, &rbacv1.ClusterRoleBinding{}); err != nil {
		t.Fatalf("ClusterRoleBinding missing: %v", err)
	}
	if err := cl.Get(context.Background(), client.ObjectKey{Name: name, Namespace: Namespace}, &corev1.ServiceAccount{}); err != nil {
		t.Fatalf("ServiceAccount missing: %v", err)
	}
}

// EnsureGrant is the create-only way to widen an existing identity: a fresh
// ClusterRole + binding to the identity SA, idempotent across passes.
func TestEnsureGrantBindsIdentityAndIsIdempotent(t *testing.T) {
	const identity = "faros-test-identity"
	const grant = "faros-test-identity-edgeproxy"
	owner := metav1.OwnerReference{APIVersion: "apis.kcp.io/v1alpha2", Kind: "APIBinding", Name: "test", UID: "b-1"}
	rules := []rbacv1.PolicyRule{{APIGroups: []string{"edges.faros.sh"}, Resources: []string{"kubernetesclusters"}, Verbs: []string{"proxy"}}}
	cl := ctrlfake.NewClientBuilder().WithScheme(identityScheme()).Build()

	for pass := 1; pass <= 2; pass++ {
		if err := EnsureGrant(context.Background(), cl, grant, identity, []metav1.OwnerReference{owner}, rules); err != nil {
			t.Fatalf("EnsureGrant pass %d: %v", pass, err)
		}
	}

	role := &rbacv1.ClusterRole{}
	if err := cl.Get(context.Background(), client.ObjectKey{Name: grant}, role); err != nil {
		t.Fatalf("grant ClusterRole: %v", err)
	}
	if !equality.Semantic.DeepEqual(role.Rules, rules) {
		t.Fatalf("grant rules = %+v, want %+v", role.Rules, rules)
	}
	if len(role.OwnerReferences) != 1 || role.OwnerReferences[0].UID != "b-1" {
		t.Fatalf("grant must carry the owner refs, got %+v", role.OwnerReferences)
	}
	crb := &rbacv1.ClusterRoleBinding{}
	if err := cl.Get(context.Background(), client.ObjectKey{Name: grant}, crb); err != nil {
		t.Fatalf("grant ClusterRoleBinding: %v", err)
	}
	if crb.RoleRef.Name != grant || crb.RoleRef.Kind != "ClusterRole" {
		t.Fatalf("binding roleRef = %+v, want ClusterRole %s", crb.RoleRef, grant)
	}
	want := []rbacv1.Subject{{Kind: "ServiceAccount", Name: identity, Namespace: Namespace}}
	if !equality.Semantic.DeepEqual(crb.Subjects, want) {
		t.Fatalf("binding subjects = %+v, want %+v", crb.Subjects, want)
	}
}
