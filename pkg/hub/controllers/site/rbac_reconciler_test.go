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

package site

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kedgev1alpha1 "github.com/faroshq/faros-kedge/apis/kedge/v1alpha1"
	"github.com/faroshq/faros-kedge/utils/testfakes"
)

func newRBACScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := kedgev1alpha1.AddToScheme(s); err != nil {
		t.Fatalf("adding kedge scheme: %v", err)
	}
	if err := corev1.AddToScheme(s); err != nil {
		t.Fatalf("adding corev1 scheme: %v", err)
	}
	if err := rbacv1.AddToScheme(s); err != nil {
		t.Fatalf("adding rbacv1 scheme: %v", err)
	}
	return s
}

const testHubURL = "https://hub.example.com"

func newRBACReconciler(c client.Client) *RBACReconciler {
	return &RBACReconciler{
		mgr:            testfakes.NewManager(c),
		hubExternalURL: testHubURL,
	}
}

func TestRBACReconciler_SiteNotFound(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(newRBACScheme(t)).Build()
	r := newRBACReconciler(c)

	result, err := r.Reconcile(context.Background(), testfakes.NewRequest("test-cluster", "", "ghost"))
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if result.RequeueAfter != 0 {
		t.Errorf("expected empty Result for not-found site, got: %+v", result)
	}
}

func TestRBACReconciler_FirstReconcile_TokenNotYetPopulated(t *testing.T) {
	scheme := newRBACScheme(t)
	site := &kedgev1alpha1.Site{
		ObjectMeta: metav1.ObjectMeta{Name: "site-alpha", UID: "uid-alpha"},
	}
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(site).
		WithObjects(site).
		Build()
	r := newRBACReconciler(c)

	result, err := r.Reconcile(context.Background(), testfakes.NewRequest("test-cluster", "", "site-alpha"))
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	// Token not yet populated — should requeue.
	if result.RequeueAfter != 2*time.Second {
		t.Errorf("expected RequeueAfter 2s while waiting for token, got: %v", result.RequeueAfter)
	}

	// Namespace should have been created.
	var ns corev1.Namespace
	if err := c.Get(context.Background(), types.NamespacedName{Name: siteNamespace}, &ns); err != nil {
		t.Errorf("namespace %q not created: %v", siteNamespace, err)
	}

	// ServiceAccount should have been created.
	var sa corev1.ServiceAccount
	if err := c.Get(context.Background(), types.NamespacedName{Name: "site-site-alpha", Namespace: siteNamespace}, &sa); err != nil {
		t.Errorf("service account not created: %v", err)
	}

	// ClusterRole should have been created.
	var cr rbacv1.ClusterRole
	if err := c.Get(context.Background(), types.NamespacedName{Name: siteAgentClusterRole}, &cr); err != nil {
		t.Errorf("cluster role not created: %v", err)
	}

	// ClusterRoleBinding should have been created.
	var crb rbacv1.ClusterRoleBinding
	if err := c.Get(context.Background(), types.NamespacedName{Name: "kedge-site-site-site-alpha"}, &crb); err != nil {
		t.Errorf("cluster role binding not created: %v", err)
	}

	// Token secret should have been created with correct type.
	var tokenSecret corev1.Secret
	if err := c.Get(context.Background(), types.NamespacedName{Name: "site-site-alpha-token", Namespace: siteNamespace}, &tokenSecret); err != nil {
		t.Errorf("token secret not created: %v", err)
	}
	if tokenSecret.Type != corev1.SecretTypeServiceAccountToken {
		t.Errorf("expected SecretTypeServiceAccountToken, got: %q", tokenSecret.Type)
	}
}

func TestRBACReconciler_TokenPopulated_KubeconfigSecretCreated(t *testing.T) {
	scheme := newRBACScheme(t)
	site := &kedgev1alpha1.Site{
		ObjectMeta: metav1.ObjectMeta{Name: "site-beta", UID: "uid-beta"},
	}
	// Pre-populate the token secret so the reconciler proceeds past the requeue.
	tokenSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "site-site-beta-token",
			Namespace: siteNamespace,
			Annotations: map[string]string{
				"kubernetes.io/service-account.name": "site-site-beta",
			},
		},
		Type: corev1.SecretTypeServiceAccountToken,
		Data: map[string][]byte{"token": []byte("real-token-value")},
	}
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: siteNamespace}}

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(site).
		WithObjects(site, ns, tokenSecret).
		Build()
	r := newRBACReconciler(c)

	result, err := r.Reconcile(context.Background(), testfakes.NewRequest("test-cluster", "", "site-beta"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RequeueAfter != 0 {
		t.Errorf("expected empty result after token available, got: %+v", result)
	}

	// Kubeconfig secret should be created.
	var kubecfgSecret corev1.Secret
	if err := c.Get(context.Background(), types.NamespacedName{Name: "site-site-beta-kubeconfig", Namespace: siteNamespace}, &kubecfgSecret); err != nil {
		t.Fatalf("kubeconfig secret not created: %v", err)
	}
	if len(kubecfgSecret.Data["kubeconfig"]) == 0 {
		t.Error("kubeconfig key is empty in kubeconfig secret")
	}
	if string(kubecfgSecret.Data["token"]) != "real-token-value" {
		t.Errorf("token key mismatch: %q", kubecfgSecret.Data["token"])
	}
	if string(kubecfgSecret.Data["server"]) != testHubURL {
		t.Errorf("server key mismatch: %q", kubecfgSecret.Data["server"])
	}

	// Site status should have CredentialsSecretRef set.
	var updatedSite kedgev1alpha1.Site
	if err := c.Get(context.Background(), types.NamespacedName{Name: "site-beta"}, &updatedSite); err != nil {
		t.Fatalf("get site: %v", err)
	}
	expectedRef := siteNamespace + "/site-site-beta-kubeconfig"
	if updatedSite.Status.CredentialsSecretRef != expectedRef {
		t.Errorf("expected CredentialsSecretRef=%q, got=%q", expectedRef, updatedSite.Status.CredentialsSecretRef)
	}
}

func TestRBACReconciler_Idempotent(t *testing.T) {
	scheme := newRBACScheme(t)
	site := &kedgev1alpha1.Site{
		ObjectMeta: metav1.ObjectMeta{Name: "site-gamma", UID: "uid-gamma"},
	}
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: siteNamespace}}
	tokenSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "site-site-gamma-token",
			Namespace: siteNamespace,
		},
		Type: corev1.SecretTypeServiceAccountToken,
		Data: map[string][]byte{"token": []byte("tok")},
	}
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(site).
		WithObjects(site, ns, tokenSecret).
		Build()
	r := newRBACReconciler(c)

	req := testfakes.NewRequest("c", "", "site-gamma")
	if _, err := r.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("first reconcile: %v", err)
	}
	// Second reconcile must not error (no duplicate creates).
	if _, err := r.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("second reconcile: %v", err)
	}
}

// TestRBACReconciler_MissingOwnerRef verifies that ensureOwnerRef patches a
// pre-existing object that lacks the site owner reference.
func TestRBACReconciler_MissingOwnerRef(t *testing.T) {
	scheme := newRBACScheme(t)
	site := &kedgev1alpha1.Site{
		ObjectMeta: metav1.ObjectMeta{Name: "site-delta", UID: "uid-delta"},
	}
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: siteNamespace}}
	// Pre-create SA without owner reference to simulate adoption path.
	existingSA := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "site-site-delta",
			Namespace: siteNamespace,
		},
	}
	tokenSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "site-site-delta-token",
			Namespace: siteNamespace,
		},
		Type: corev1.SecretTypeServiceAccountToken,
		Data: map[string][]byte{"token": []byte("tok")},
	}

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(site).
		WithObjects(site, ns, existingSA, tokenSecret).
		Build()
	r := newRBACReconciler(c)

	if _, err := r.Reconcile(context.Background(), testfakes.NewRequest("c", "", "site-delta")); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	// SA should now have the owner reference patched in.
	var sa corev1.ServiceAccount
	if err := c.Get(context.Background(), types.NamespacedName{Name: "site-site-delta", Namespace: siteNamespace}, &sa); err != nil {
		t.Fatalf("get SA: %v", err)
	}
	found := false
	for _, ref := range sa.OwnerReferences {
		if ref.UID == site.UID {
			found = true
		}
	}
	if !found {
		t.Errorf("expected owner reference for site UID %q to be patched onto SA, refs: %+v", site.UID, sa.OwnerReferences)
	}
}

// ── pure function tests ───────────────────────────────────────────────────────

func TestRulesEqual(t *testing.T) {
	r1 := desiredAgentRules()
	r2 := desiredAgentRules()
	if !rulesEqual(r1, r2) {
		t.Error("identical rules not equal")
	}
	r2[0].Verbs = append(r2[0].Verbs, "extra-verb")
	if rulesEqual(r1, r2) {
		t.Error("expected rules to differ after mutation")
	}
}

func TestSlicesEqual(t *testing.T) {
	if !slicesEqual([]string{"a", "b"}, []string{"a", "b"}) {
		t.Error("identical slices not equal")
	}
	if slicesEqual([]string{"a"}, []string{"b"}) {
		t.Error("different slices reported equal")
	}
	if slicesEqual([]string{"a"}, []string{"a", "b"}) {
		t.Error("different-length slices reported equal")
	}
	if !slicesEqual(nil, nil) {
		t.Error("nil slices not equal")
	}
}
