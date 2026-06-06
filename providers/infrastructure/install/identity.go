/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package install

// Runtime identity bootstrap.
//
// MintRuntimeIdentity creates the ServiceAccount + Role + RoleBinding
// the serve subcommand uses, then mints a TokenRequest bearer. The
// returned RuntimeIdentity carries the SA's namespace + name + token,
// plus the server URL the serve mode connects to (the in-cluster
// kcp front-proxy URL for now; the APIExport virtual-workspace URL
// once SeedKroCluster discovers it).
//
// The RBAC is intentionally narrow:
//
//   - read access to platform Templates + per-template CRs across
//     bound tenant workspaces (via the APIExport virtual workspace)
//   - manage rights on Templates' status (the Template controller
//     patches status on every reconcile)
//   - read on APIExport, APIResourceSchema, CachedResource so the
//     Template controller's apiexport.go can list-then-update
//
// Cluster-admin operations (CRD apply, APIResourceSchema mint, etc.)
// stay in init's own privilege scope. The serve mode never needs
// them because init has already done that work.

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	authnv1 "k8s.io/api/authentication/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// RuntimeServiceAccountName is the well-known SA name the runtime
// uses. Hardcoded so init's RoleBinding and serve's kubeconfig
// reference the same identity without configuration.
const RuntimeServiceAccountName = "infrastructure-runtime"

// RuntimeServiceAccountNamespace is the namespace the SA lives in
// inside the provider workspace. Reusing "default" keeps the install
// flow trivial — every kcp workspace ships the default namespace.
const RuntimeServiceAccountNamespace = "default"

// RuntimeRoleName is the (Cluster)Role the SA is bound to. Cluster-
// scoped because the Template controller reads + patches Template
// status across the workspace, not in any single namespace.
const RuntimeRoleName = "infrastructure-runtime"

// RuntimeTokenExpirationSeconds is the TokenRequest TTL. One hour by
// default; the serve subcommand needs a rotation goroutine that
// re-mints before this expires. For PR C bring-up we use 24h to keep
// dev iteration cheap; rotation is a follow-up.
const RuntimeTokenExpirationSeconds int64 = 24 * 60 * 60

// RuntimeIdentity is what MintRuntimeIdentity returns to the caller.
// Carries everything WriteKubeconfig needs to assemble a usable
// kubeconfig: server URL, CA data, token, identity name.
type RuntimeIdentity struct {
	// Server is the apiserver URL the kubeconfig points at. For now
	// the in-cluster kcp front-proxy URL is the right choice (PR C);
	// the APIExport virtual workspace URL gets stitched in by the
	// caller once it's discovered.
	Server string

	// CAData is the apiserver's CA cert in PEM form, used to verify
	// the connection. Pulled from the admin rest.Config.
	CAData []byte

	// Token is the minted bearer. Short-lived; rotation TBD.
	Token string

	// ServiceAccount + Namespace echo back the identity for callers
	// that want to embed them in audit logs or Secret labels.
	ServiceAccount string
	Namespace      string
}

// MintRuntimeIdentity provisions the runtime SA + RBAC and mints a
// bearer for it. Idempotent on SA + role + binding creation.
func MintRuntimeIdentity(ctx context.Context, adminConfig *rest.Config) (*RuntimeIdentity, error) {
	cs, err := kubernetes.NewForConfig(adminConfig)
	if err != nil {
		return nil, fmt.Errorf("kubernetes client: %w", err)
	}

	if err := ensureServiceAccount(ctx, cs); err != nil {
		return nil, fmt.Errorf("ensure SA: %w", err)
	}
	if err := ensureClusterRole(ctx, cs); err != nil {
		return nil, fmt.Errorf("ensure role: %w", err)
	}
	if err := ensureClusterRoleBinding(ctx, cs); err != nil {
		return nil, fmt.Errorf("ensure binding: %w", err)
	}

	// TokenRequest is a subresource of ServiceAccount. The minted
	// token is bound to this SA's audiences + lifetime. We pass no
	// `audiences` so kcp uses its default (the apiserver itself),
	// which is what the in-cluster front-proxy expects.
	tr, err := cs.CoreV1().
		ServiceAccounts(RuntimeServiceAccountNamespace).
		CreateToken(ctx, RuntimeServiceAccountName, &authnv1.TokenRequest{
			Spec: authnv1.TokenRequestSpec{
				ExpirationSeconds: func() *int64 { v := RuntimeTokenExpirationSeconds; return &v }(),
			},
		}, metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("create token: %w", err)
	}

	return &RuntimeIdentity{
		Server:         adminConfig.Host,
		CAData:         adminConfig.CAData,
		Token:          tr.Status.Token,
		ServiceAccount: RuntimeServiceAccountName,
		Namespace:      RuntimeServiceAccountNamespace,
	}, nil
}

func ensureServiceAccount(ctx context.Context, cs kubernetes.Interface) error {
	_, err := cs.CoreV1().
		ServiceAccounts(RuntimeServiceAccountNamespace).
		Get(ctx, RuntimeServiceAccountName, metav1.GetOptions{})
	if err == nil {
		return nil
	}
	if !apierrors.IsNotFound(err) {
		return err
	}
	_, err = cs.CoreV1().
		ServiceAccounts(RuntimeServiceAccountNamespace).
		Create(ctx, &corev1.ServiceAccount{
			ObjectMeta: metav1.ObjectMeta{
				Name:      RuntimeServiceAccountName,
				Namespace: RuntimeServiceAccountNamespace,
			},
		}, metav1.CreateOptions{})
	if err != nil && !apierrors.IsAlreadyExists(err) {
		return err
	}
	return nil
}

func ensureClusterRole(ctx context.Context, cs kubernetes.Interface) error {
	want := &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{Name: RuntimeRoleName},
		Rules: []rbacv1.PolicyRule{
			// Templates — read + status patch. The Template
			// controller patches status on every reconcile pass.
			{
				APIGroups: []string{"infrastructure.kedge.faros.sh"},
				Resources: []string{"templates"},
				Verbs:     []string{"get", "list", "watch"},
			},
			{
				APIGroups: []string{"infrastructure.kedge.faros.sh"},
				Resources: []string{"templates/status"},
				Verbs:     []string{"get", "patch", "update"},
			},
			// Per-template kinds — wildcarded because the kinds are
			// runtime-defined (Redis, Postgres, etc.). Read across
			// the APIExport VW so the future kro backend can see
			// every tenant's Instance CRs.
			{
				APIGroups: []string{"infrastructure.kedge.faros.sh"},
				Resources: []string{"*"},
				Verbs:     []string{"get", "list", "watch", "patch", "update"},
			},
			// kcp resources the Template controller has to touch.
			// apiexportendpointslices is added for the kro-multicluster
			// kcp-apiexport provider, which reads the slice to discover
			// the APIExport's virtual-workspace URL.
			//
			// apibindings: the kcp-apiexport provider sets up an
			// APIBinding informer through the VW to enumerate every
			// kcp logical cluster that has bound the APIExport — that's
			// how it discovers tenant workspaces dynamically.
			{
				APIGroups: []string{"apis.kcp.io"},
				Resources: []string{"apiexports", "apiresourceschemas", "apiexportendpointslices", "apibindings"},
				Verbs:     []string{"get", "list", "watch", "update", "create"},
			},
			// apiexports/content is the kcp VW authorizer's gate (see
			// kcp/pkg/virtual/apiexport/authorizer/content.go). Every
			// call against the APIExport's VW URL runs a SAR for
			// {resource: apiexports/content, name: <export-name>}
			// before any per-resource RBAC kicks in. Without this the
			// runtime SA hits 403 on /api and /apis discovery even
			// though it has discovery non-resource URL access.
			{
				APIGroups:     []string{"apis.kcp.io"},
				Resources:     []string{"apiexports/content"},
				ResourceNames: []string{APIExportName},
				Verbs:         []string{"*"},
			},
			{
				APIGroups: []string{"cache.kcp.io"},
				Resources: []string{"cachedresources"},
				Verbs:     []string{"get", "list", "watch"},
			},
			// CRD reads so the controller can check Established
			// condition without trying to write CRDs (init owns
			// CRD writes).
			{
				APIGroups: []string{"apiextensions.k8s.io"},
				Resources: []string{"customresourcedefinitions"},
				Verbs:     []string{"get", "list", "watch", "create", "update"},
			},
		},
	}
	// API discovery non-resource URLs. Required for the kcp-apiexport
	// provider's client-go to do server-groups + version discovery
	// against the VW URL ("/api", "/apis", etc.) before it can
	// construct any typed informer. Inlined here so the runtime SA's
	// permission boundary stays in one place — alternative would be a
	// second binding to system:discovery.
	want.Rules = append(want.Rules, rbacv1.PolicyRule{
		NonResourceURLs: []string{"/api", "/api/*", "/apis", "/apis/*", "/version", "/openapi", "/openapi/*", "/healthz", "/livez", "/readyz"},
		Verbs:           []string{"get"},
	})

	existing, err := cs.RbacV1().ClusterRoles().Get(ctx, RuntimeRoleName, metav1.GetOptions{})
	if err == nil {
		// Idempotent update — overwrite rules so any change to the
		// embedded definition above takes effect on the next init.
		existing.Rules = want.Rules
		_, err = cs.RbacV1().ClusterRoles().Update(ctx, existing, metav1.UpdateOptions{})
		return err
	}
	if !apierrors.IsNotFound(err) {
		return err
	}
	_, err = cs.RbacV1().ClusterRoles().Create(ctx, want, metav1.CreateOptions{})
	if err != nil && !apierrors.IsAlreadyExists(err) {
		return err
	}
	return nil
}

func ensureClusterRoleBinding(ctx context.Context, cs kubernetes.Interface) error {
	want := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: RuntimeRoleName},
		RoleRef: rbacv1.RoleRef{
			APIGroup: rbacv1.GroupName,
			Kind:     "ClusterRole",
			Name:     RuntimeRoleName,
		},
		Subjects: []rbacv1.Subject{
			{
				Kind:      rbacv1.ServiceAccountKind,
				Name:      RuntimeServiceAccountName,
				Namespace: RuntimeServiceAccountNamespace,
			},
		},
	}
	existing, err := cs.RbacV1().ClusterRoleBindings().Get(ctx, RuntimeRoleName, metav1.GetOptions{})
	if err == nil {
		existing.RoleRef = want.RoleRef
		existing.Subjects = want.Subjects
		_, err = cs.RbacV1().ClusterRoleBindings().Update(ctx, existing, metav1.UpdateOptions{})
		return err
	}
	if !apierrors.IsNotFound(err) {
		return err
	}
	_, err = cs.RbacV1().ClusterRoleBindings().Create(ctx, want, metav1.CreateOptions{})
	if err != nil && !apierrors.IsAlreadyExists(err) {
		return err
	}
	return nil
}

