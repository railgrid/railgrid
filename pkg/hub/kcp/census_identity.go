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

package kcp

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"

	"github.com/railgrid/railgrid/pkg/kcppaths"
)

// Census identity constants. The census controller authenticates as this
// ServiceAccount (home in root:railgrid:system:metering) — NOT the hub kcp admin
// credential — so it can read only tenancy.kcp.io/workspaces (through the census
// APIExport VW) and write MembershipReports (through the metering-platform VW).
const (
	censusServiceAccountName      = "railgrid-census"
	censusServiceAccountNamespace = "default"
	censusRoleName                = "railgrid-census"
	censusTokenSecretName         = "railgrid-census-token"

	censusExportName           = "census.railgrid.ai"
	meteringPlatformExportName = "metering-platform"
)

// logicalClusterGVR addresses the per-workspace LogicalCluster singleton.
var logicalClusterGVRlocal = schema.GroupVersionResource{
	Group: "core.kcp.io", Version: "v1alpha1", Resource: "logicalclusters",
}

// ensureCensusIdentity provisions the least-privileged census ServiceAccount +
// ClusterRole + binding in the metering workspace and mints a long-lived token
// for it, storing the token on the Bootstrapper (b.censusToken). It also resolves
// the :platform workspace's logical-cluster id (b.platformClusterID) that the
// census writes MembershipReports into via the metering-platform VW. Idempotent.
//
// Validated live: this identity can list workspaces across bound orgs via the
// census APIExport VW and create MembershipReports via the metering-platform VW,
// with no kcp-admin rights.
func (b *Bootstrapper) ensureCensusIdentity(ctx context.Context) error {
	meteringConfig := configForPath(b.config, kcppaths.SystemMetering)
	cs, err := kubernetes.NewForConfig(meteringConfig)
	if err != nil {
		return fmt.Errorf("census identity: kubernetes client: %w", err)
	}

	if err := ensureCensusServiceAccount(ctx, cs); err != nil {
		return fmt.Errorf("census identity: service account: %w", err)
	}
	if err := ensureCensusClusterRole(ctx, cs); err != nil {
		return fmt.Errorf("census identity: cluster role: %w", err)
	}
	if err := ensureCensusClusterRoleBinding(ctx, cs); err != nil {
		return fmt.Errorf("census identity: cluster role binding: %w", err)
	}
	token, err := ensureCensusToken(ctx, cs)
	if err != nil {
		return fmt.Errorf("census identity: token: %w", err)
	}
	b.censusToken = token

	// Resolve the :platform logical-cluster id — the census writes reports into
	// that cluster through the metering-platform VW, addressing it by id.
	platformDyn, err := dynamic.NewForConfig(configForPath(b.config, kcppaths.SystemMeteringPlatform))
	if err != nil {
		return fmt.Errorf("census identity: platform client: %w", err)
	}
	lc, err := platformDyn.Resource(logicalClusterGVRlocal).Get(ctx, "cluster", metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("census identity: reading :platform LogicalCluster: %w", err)
	}
	id := lc.GetAnnotations()["kcp.io/cluster"]
	if id == "" {
		return fmt.Errorf("census identity: :platform LogicalCluster has no kcp.io/cluster annotation")
	}
	b.platformClusterID = id
	return nil
}

func ensureCensusServiceAccount(ctx context.Context, cs kubernetes.Interface) error {
	_, err := cs.CoreV1().ServiceAccounts(censusServiceAccountNamespace).Get(ctx, censusServiceAccountName, metav1.GetOptions{})
	if err == nil {
		return nil
	}
	if !apierrors.IsNotFound(err) {
		return err
	}
	_, err = cs.CoreV1().ServiceAccounts(censusServiceAccountNamespace).Create(ctx, &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{Name: censusServiceAccountName, Namespace: censusServiceAccountNamespace},
	}, metav1.CreateOptions{})
	if err != nil && !apierrors.IsAlreadyExists(err) {
		return err
	}
	return nil
}

func ensureCensusClusterRole(ctx context.Context, cs kubernetes.Interface) error {
	want := &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{Name: censusRoleName},
		Rules: []rbacv1.PolicyRule{
			// The kcp APIExport VW authorizer gates every call on a SAR for
			// {apiexports/content, name: <export>}. The census reaches TWO VWs:
			// census.railgrid.ai (read workspaces) and metering-platform
			// (write MembershipReports).
			{
				APIGroups:     []string{"apis.kcp.io"},
				Resources:     []string{"apiexports/content"},
				ResourceNames: []string{censusExportName, meteringPlatformExportName},
				Verbs:         []string{"*"},
			},
			// Discovery of the VW URLs (endpointslices) + client-go typed discovery.
			{
				APIGroups: []string{"apis.kcp.io"},
				Resources: []string{"apiexports", "apiresourceschemas", "apiexportendpointslices", "apibindings"},
				Verbs:     []string{"get", "list", "watch"},
			},
			// The claimed read payload.
			{
				APIGroups: []string{"tenancy.kcp.io"},
				Resources: []string{"workspaces"},
				Verbs:     []string{"get", "list", "watch"},
			},
			// The write payload (through the metering-platform VW into :platform).
			{
				APIGroups: []string{"metering.contrib.kcp.io"},
				Resources: []string{"membershipreports"},
				Verbs:     []string{"get", "list", "watch", "create", "update", "delete"},
			},
			// Resolve the :platform LogicalCluster id (bootstrap uses admin, but the
			// role also carries it so the identity is self-contained if reused).
			{
				APIGroups: []string{"core.kcp.io"},
				Resources: []string{"logicalclusters"},
				Verbs:     []string{"get", "list", "watch"},
			},
			{
				NonResourceURLs: []string{"/api", "/api/*", "/apis", "/apis/*", "/version", "/openapi", "/openapi/*", "/healthz", "/livez", "/readyz"},
				Verbs:           []string{"get"},
			},
		},
	}
	existing, err := cs.RbacV1().ClusterRoles().Get(ctx, censusRoleName, metav1.GetOptions{})
	if err == nil {
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

func ensureCensusClusterRoleBinding(ctx context.Context, cs kubernetes.Interface) error {
	want := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: censusRoleName},
		RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: censusRoleName},
		Subjects: []rbacv1.Subject{{
			Kind:      rbacv1.ServiceAccountKind,
			Name:      censusServiceAccountName,
			Namespace: censusServiceAccountNamespace,
		}},
	}
	existing, err := cs.RbacV1().ClusterRoleBindings().Get(ctx, censusRoleName, metav1.GetOptions{})
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

// ensureCensusToken creates a kubernetes.io/service-account-token Secret for the
// census SA and waits for kcp's token controller to populate a non-expiring
// bearer, returning it. Reusing the Secret returns the same token across re-runs.
func ensureCensusToken(ctx context.Context, cs kubernetes.Interface) (string, error) {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:        censusTokenSecretName,
			Namespace:   censusServiceAccountNamespace,
			Annotations: map[string]string{corev1.ServiceAccountNameKey: censusServiceAccountName},
		},
		Type: corev1.SecretTypeServiceAccountToken,
	}
	if _, err := cs.CoreV1().Secrets(censusServiceAccountNamespace).Create(ctx, secret, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return "", fmt.Errorf("creating census token Secret: %w", err)
	}
	var token string
	if err := wait.PollUntilContextTimeout(ctx, 500*time.Millisecond, 30*time.Second, true, func(ctx context.Context) (bool, error) {
		got, err := cs.CoreV1().Secrets(censusServiceAccountNamespace).Get(ctx, censusTokenSecretName, metav1.GetOptions{})
		if err != nil {
			return false, nil
		}
		if t := got.Data[corev1.ServiceAccountTokenKey]; len(t) > 0 {
			token = string(t)
			return true, nil
		}
		return false, nil
	}); err != nil {
		return "", fmt.Errorf("waiting for census token: %w", err)
	}
	return token, nil
}
