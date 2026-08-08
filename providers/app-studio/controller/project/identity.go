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
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	aiv1alpha1 "github.com/faroshq/provider-app-studio/apis/ai/v1alpha1"
)

// Per-project ServiceAccount identity.
//
// Repository commits run in this controller, long after the request that
// caused the workspace edits — so they cannot borrow the editing user's
// bearer (request-scoped, and the same trap the agents provider hit).
// Instead each project gets its own ServiceAccount in the tenant workspace,
// and the controller uses its token for hub MCP calls.
//
// SCOPE, STATED PLAINLY: this token can read the workspace's infrastructure
// resources and read/write its code resources, it does not expire, and
// revoking it means deleting the ServiceAccount — which happens
// automatically, because the objects are owned by the Project and
// garbage-collected with it.

const (
	// identityNamespace is where the SA and its token Secret live. kcp
	// workspaces have a "default" namespace; the platform's other providers
	// put per-identity objects there too.
	identityNamespace = "default"
	// tokenWait bounds how long one reconcile waits for the token controller
	// to populate the Secret before requeueing.
	tokenWait = 15 * time.Second
)

func identityName(project string) string    { return "kedge-appstudio-" + project }
func tokenSecretName(project string) string { return "kedge-appstudio-" + project + "-token" }

// projectOwnerRef points identity objects at the Project so kcp
// garbage-collects them with it.
func projectOwnerRef(p *aiv1alpha1.Project) metav1.OwnerReference {
	controller := true
	return metav1.OwnerReference{
		APIVersion: aiv1alpha1.SchemeGroupVersion.String(),
		Kind:       "Project",
		Name:       p.Name,
		UID:        p.UID,
		Controller: &controller,
	}
}

// ensureIdentity provisions (idempotently) the project's ServiceAccount, a
// ClusterRole letting it drive the project's code resources, the binding,
// and the token Secret; it returns the token once the controller has filled
// it in. An empty token with a nil error means "not ready yet, requeue".
func (r *Reconciler) ensureIdentity(ctx context.Context, c client.Client, p *aiv1alpha1.Project) (string, error) {
	name := identityName(p.Name)
	refs := []metav1.OwnerReference{projectOwnerRef(p)}

	sa := &corev1.ServiceAccount{}
	sa.Name = name
	sa.Namespace = identityNamespace
	sa.OwnerReferences = refs
	if err := createIfAbsent(ctx, c, sa); err != nil {
		return "", fmt.Errorf("project ServiceAccount: %w", err)
	}

	// Instances and repositories are cluster-scoped (per-template CRDs and
	// code.kedge.faros.sh are), so this must be a ClusterRole.
	role := &rbacv1.ClusterRole{}
	role.Name = name
	role.OwnerReferences = refs
	role.Rules = []rbacv1.PolicyRule{
		{
			APIGroups: []string{"infrastructure.kedge.faros.sh"},
			Resources: []string{"*"},
			Verbs:     []string{"get", "list", "watch"},
		},
		{
			APIGroups: []string{"code.kedge.faros.sh"},
			Resources: []string{"*"},
			Verbs:     []string{"get", "list", "watch", "create", "update", "patch"},
		},
	}
	if err := createIfAbsent(ctx, c, role); err != nil {
		return "", fmt.Errorf("project ClusterRole: %w", err)
	}

	binding := &rbacv1.ClusterRoleBinding{}
	binding.Name = name
	binding.OwnerReferences = refs
	binding.RoleRef = rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: name}
	binding.Subjects = []rbacv1.Subject{{Kind: "ServiceAccount", Name: name, Namespace: identityNamespace}}
	if err := createIfAbsent(ctx, c, binding); err != nil {
		return "", fmt.Errorf("project ClusterRoleBinding: %w", err)
	}

	// kcp has no TokenRequest subresource, so the usable token comes from a
	// legacy service-account-token Secret the token controller fills in.
	secretName := tokenSecretName(p.Name)
	sec := &corev1.Secret{}
	sec.Name = secretName
	sec.Namespace = identityNamespace
	sec.OwnerReferences = refs
	sec.Type = corev1.SecretTypeServiceAccountToken
	sec.Annotations = map[string]string{corev1.ServiceAccountNameKey: name}
	if err := createIfAbsent(ctx, c, sec); err != nil {
		return "", fmt.Errorf("project token Secret: %w", err)
	}

	deadline := time.Now().Add(tokenWait)
	for {
		got := &corev1.Secret{}
		if err := c.Get(ctx, types.NamespacedName{Namespace: identityNamespace, Name: secretName}, got); err == nil {
			if t := got.Data[corev1.ServiceAccountTokenKey]; len(t) > 0 {
				return string(t), nil
			}
		}
		if time.Now().After(deadline) {
			return "", nil // not ready; caller requeues
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}

func createIfAbsent(ctx context.Context, c client.Client, obj client.Object) error {
	if err := c.Create(ctx, obj); err != nil && !apierrors.IsAlreadyExists(err) {
		return err
	}
	return nil
}
