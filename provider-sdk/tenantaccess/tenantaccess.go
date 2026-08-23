/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package tenantaccess gives the background reconcilers their own identity in
// a tenant workspace, and a client that acts as that identity against the
// workspace's real API surface — NOT the provider's claimed virtual
// workspace.
//
// Why this exists: a permission claim on another provider's resources must
// pin that provider's APIExport identityHash, and an export can pin exactly
// one identity per claimed resource — for every consumer at once. The moment
// one workspace binds a self-hosted copy of a dependency while another binds
// the platform copy, no single pin is correct, and kcp silently stops serving
// the claimed resources to whoever mismatches. Acting as a workspace-local
// ServiceAccount through the workspace's own bindings sidesteps identity
// entirely: the workspace serves whichever copy it binds, and RBAC — not
// claim identity — authorizes the access.
//
// The ServiceAccount, its RBAC, and its token Secret are still provisioned
// over the claimed VW: those are built-in types (no identityHash), so their
// claims work in every deployment mix.
package tenantaccess

import (
	"context"
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// Namespace is where identity objects live. kcp workspaces have a
	// "default" namespace; the platform's other providers put per-identity
	// objects there too.
	Namespace = "default"
	// tokenWait bounds how long one reconcile waits for the token controller
	// to populate the Secret before requeueing.
	tokenWait = 15 * time.Second
)

// TokenSecretName is the conventional name of an identity's token Secret.
func TokenSecretName(name string) string { return name + "-token" }

// EnsureIdentity provisions (idempotently) a ServiceAccount, a ClusterRole
// with the given rules, the binding, and the token Secret, all owned by refs;
// it returns the token once the token controller has filled it in. An empty
// token with a nil error means "not ready yet, requeue".
//
// c must be a client on the workspace where the identity lives — the claimed
// VW client works, because every object here is a built-in type.
func EnsureIdentity(ctx context.Context, c client.Client, name string, refs []metav1.OwnerReference, rules []rbacv1.PolicyRule) (string, error) {
	sa := &corev1.ServiceAccount{}
	sa.Name = name
	sa.Namespace = Namespace
	sa.OwnerReferences = refs
	if err := createIfAbsent(ctx, c, sa); err != nil {
		return "", fmt.Errorf("ServiceAccount: %w", err)
	}

	// Instances and repositories are cluster-scoped, so this must be a
	// ClusterRole.
	role := &rbacv1.ClusterRole{}
	role.Name = name
	role.OwnerReferences = refs
	role.Rules = rules
	if err := createIfAbsent(ctx, c, role); err != nil {
		return "", fmt.Errorf("ClusterRole: %w", err)
	}

	binding := &rbacv1.ClusterRoleBinding{}
	binding.Name = name
	binding.OwnerReferences = refs
	binding.RoleRef = rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: name}
	binding.Subjects = []rbacv1.Subject{{Kind: "ServiceAccount", Name: name, Namespace: Namespace}}
	if err := createIfAbsent(ctx, c, binding); err != nil {
		return "", fmt.Errorf("ClusterRoleBinding: %w", err)
	}

	// kcp has no TokenRequest subresource, so the usable token comes from a
	// legacy service-account-token Secret the token controller fills in.
	secretName := TokenSecretName(name)
	sec := &corev1.Secret{}
	sec.Name = secretName
	sec.Namespace = Namespace
	sec.OwnerReferences = refs
	sec.Type = corev1.SecretTypeServiceAccountToken
	sec.Annotations = map[string]string{corev1.ServiceAccountNameKey: name}
	if err := createIfAbsent(ctx, c, sec); err != nil {
		return "", fmt.Errorf("token Secret: %w", err)
	}

	deadline := time.Now().Add(tokenWait)
	for {
		got := &corev1.Secret{}
		if err := c.Get(ctx, types.NamespacedName{Namespace: Namespace, Name: secretName}, got); err == nil {
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

// NewClient builds a controller-runtime client on one workspace cluster,
// authenticating as the given bearer token via the hub front proxy —
// {hubBase}/clusters/{clusterID}, the same base every provider already uses
// for MCP and GraphQL calls. insecure relaxes TLS for in-cluster hub certs
// (the FAROS_HUB_INSECURE knob).
func NewClient(hubBase, clusterID, token string, insecure bool) (client.Client, error) {
	if hubBase == "" {
		return nil, fmt.Errorf("hub base URL is empty (FAROS_HUB_URL)")
	}
	if clusterID == "" {
		return nil, fmt.Errorf("workspace cluster id is empty")
	}
	if token == "" {
		return nil, fmt.Errorf("identity token is empty")
	}
	cfg := &rest.Config{
		Host:        strings.TrimRight(hubBase, "/") + "/clusters/" + clusterID,
		BearerToken: token,
	}
	if insecure {
		cfg.TLSClientConfig = rest.TLSClientConfig{Insecure: true}
	}
	// The reconcilers exchange only unstructured objects and built-in types
	// over this client; the default scheme covers both.
	return client.New(cfg, client.Options{})
}

func createIfAbsent(ctx context.Context, c client.Client, obj client.Object) error {
	if err := c.Create(ctx, obj); err != nil && !apierrors.IsAlreadyExists(err) {
		return err
	}
	return nil
}
