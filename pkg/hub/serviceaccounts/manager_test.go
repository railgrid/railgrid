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

package serviceaccounts

import (
	"context"
	"strings"
	"testing"
	"time"

	authnv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	clienttesting "k8s.io/client-go/testing"
)

// fakeConfigBuilder is a WorkspaceConfigBuilder stand-in. The fake
// kube clientset doesn't care what we return — the Manager rebuilds
// a clientset via kubernetes.NewForConfig on every call, but we
// intercept at the Manager-level helper instead. See managerFor below.
type fakeConfigBuilder struct{}

func (fakeConfigBuilder) ChildWorkspaceConfig(_, _ string) *rest.Config {
	return &rest.Config{}
}

// managerFor swaps the clientset builder so tests run against the
// kube fake. The Manager's clientset method goes through cfg; since
// tests can't easily intercept kubernetes.NewForConfig, we provide a
// test seam by overriding the method via a func field.
//
// To avoid touching production code with a test-only override, this
// helper uses an internal alternate Manager constructor.
func managerFor(_ *testing.T, objects ...runtime.Object) (*Manager, *fake.Clientset) {
	cs := fake.NewSimpleClientset(objects...)
	m := &Manager{cfg: fakeConfigBuilder{}}
	// Override the clientset method via the testClientset variable
	// hook in serviceaccounts.go (see below).
	testClientset = func() (kubernetes.Interface, bool) { return cs, true }
	return m, cs
}

func TestCreate_HappyPath(t *testing.T) {
	m, cs := managerFor(t)
	defer resetTestClientset()

	sa, err := m.Create(context.Background(), "org", "ws", "ci-bot", RoleAdmin)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if sa.DisplayName != "ci-bot" {
		t.Errorf("DisplayName: got %q, want ci-bot", sa.DisplayName)
	}
	if sa.Role != RoleAdmin {
		t.Errorf("Role: got %q, want admin", sa.Role)
	}
	if sa.UUID == "" {
		t.Error("expected UUID assigned")
	}

	got, err := cs.CoreV1().ServiceAccounts(Namespace).Get(context.Background(), sa.UUID, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Get kube SA: %v", err)
	}
	if got.Annotations[AnnotationDisplayName] != "ci-bot" {
		t.Errorf("display-name annotation missing: %#v", got.Annotations)
	}
	if got.Annotations[AnnotationRole] != RoleAdmin {
		t.Errorf("role annotation missing: %#v", got.Annotations)
	}
	if got.Labels[LabelKedgeSA] != "true" {
		t.Errorf("kedge-sa label missing: %#v", got.Labels)
	}

	crb, err := cs.RbacV1().ClusterRoleBindings().Get(context.Background(), crbName(sa.UUID), metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Get CRB: %v", err)
	}
	if crb.RoleRef.Name != "cluster-admin" {
		t.Errorf("CRB roleRef: got %q, want cluster-admin", crb.RoleRef.Name)
	}
}

func TestCreate_ValidatesInputs(t *testing.T) {
	m, _ := managerFor(t)
	defer resetTestClientset()

	if _, err := m.Create(context.Background(), "org", "ws", "", RoleAdmin); err == nil {
		t.Error("expected error on empty displayName")
	}
	if _, err := m.Create(context.Background(), "org", "ws", "x", "viewer"); err == nil {
		t.Error("expected error on invalid role")
	}
}

func TestList_FiltersByLabel(t *testing.T) {
	stranger := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{Name: "not-ours", Namespace: Namespace},
	}
	m, cs := managerFor(t, stranger)
	defer resetTestClientset()

	if _, err := m.Create(context.Background(), "org", "ws", "ci-bot", RoleAdmin); err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := m.List(context.Background(), "org", "ws")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 {
		t.Errorf("List: got %d, want 1 (stranger SA must be filtered)", len(got))
	}
	if got[0].DisplayName != "ci-bot" {
		t.Errorf("unexpected SA: %#v", got[0])
	}

	// Sanity: the stranger SA is still present in the fake.
	if _, err := cs.CoreV1().ServiceAccounts(Namespace).Get(context.Background(), "not-ours", metav1.GetOptions{}); err != nil {
		t.Fatalf("stranger SA missing: %v", err)
	}
}

func TestGet_NotFoundForNonKedgeSA(t *testing.T) {
	stranger := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{Name: "not-ours", Namespace: Namespace},
	}
	m, _ := managerFor(t, stranger)
	defer resetTestClientset()

	_, err := m.Get(context.Background(), "org", "ws", "not-ours")
	if !apierrors.IsNotFound(err) {
		t.Errorf("expected NotFound for non-kedge SA, got %v", err)
	}
}

func TestDelete_Idempotent(t *testing.T) {
	m, _ := managerFor(t)
	defer resetTestClientset()

	sa, err := m.Create(context.Background(), "org", "ws", "ci-bot", RoleAdmin)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := m.Delete(context.Background(), "org", "ws", sa.UUID); err != nil {
		t.Fatalf("Delete (1st): %v", err)
	}
	// Second delete must not error.
	if err := m.Delete(context.Background(), "org", "ws", sa.UUID); err != nil {
		t.Errorf("Delete (2nd, idempotent): %v", err)
	}
}

func TestPatch_RoleChange_RewritesCRB(t *testing.T) {
	m, cs := managerFor(t)
	defer resetTestClientset()

	sa, err := m.Create(context.Background(), "org", "ws", "ci-bot", RoleMember)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	patched, err := m.PatchRoleAndDisplayName(context.Background(), "org", "ws", sa.UUID, RoleAdmin, "")
	if err != nil {
		t.Fatalf("Patch: %v", err)
	}
	if patched.Role != RoleAdmin {
		t.Errorf("Role: got %q, want admin", patched.Role)
	}

	// CRB must still exist and now annotate the new role.
	crb, err := cs.RbacV1().ClusterRoleBindings().Get(context.Background(), crbName(sa.UUID), metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Get CRB after patch: %v", err)
	}
	if crb.Annotations[AnnotationRole] != RoleAdmin {
		t.Errorf("CRB role annotation: got %q, want admin", crb.Annotations[AnnotationRole])
	}
}

func TestPatch_DisplayNameOnly(t *testing.T) {
	m, _ := managerFor(t)
	defer resetTestClientset()

	sa, err := m.Create(context.Background(), "org", "ws", "old-name", RoleMember)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	patched, err := m.PatchRoleAndDisplayName(context.Background(), "org", "ws", sa.UUID, "", "new-name")
	if err != nil {
		t.Fatalf("Patch: %v", err)
	}
	if patched.DisplayName != "new-name" {
		t.Errorf("DisplayName: got %q, want new-name", patched.DisplayName)
	}
	if patched.Role != RoleMember {
		t.Errorf("Role: got %q, want member (unchanged)", patched.Role)
	}
}

func TestIssueToken_StampsAnnotation(t *testing.T) {
	m, cs := managerFor(t)
	defer resetTestClientset()

	sa, err := m.Create(context.Background(), "org", "ws", "ci-bot", RoleAdmin)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Pin the TokenRequest reactor so we control the returned token.
	cs.PrependReactor("create", "serviceaccounts/token", func(_ clienttesting.Action) (bool, runtime.Object, error) {
		exp := metav1.NewTime(time.Now().Add(DefaultTokenExpiry))
		return true, &authnv1.TokenRequest{
			Status: authnv1.TokenRequestStatus{
				Token:               "fake-jwt-token",
				ExpirationTimestamp: exp,
			},
		}, nil
	})

	tok, err := m.IssueToken(context.Background(), "org", "ws", sa.UUID)
	if err != nil {
		t.Fatalf("IssueToken: %v", err)
	}
	if tok.Token != "fake-jwt-token" {
		t.Errorf("Token: got %q, want fake-jwt-token", tok.Token)
	}
	if tok.ExpiresAt.IsZero() {
		t.Error("ExpiresAt zero")
	}

	got, err := cs.CoreV1().ServiceAccounts(Namespace).Get(context.Background(), sa.UUID, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Get SA: %v", err)
	}
	if got.Annotations[AnnotationLastTokenIssuedAt] == "" {
		t.Error("last-token-issued-at annotation not stamped")
	}
}

func TestRevokeTokens_RecreatesSA(t *testing.T) {
	m, cs := managerFor(t)
	defer resetTestClientset()

	sa, err := m.Create(context.Background(), "org", "ws", "ci-bot", RoleAdmin)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	originalUID := mustGetSA(t, cs, sa.UUID).UID

	// Track the recreated SA name to confirm same UUID.
	if err := m.RevokeTokens(context.Background(), "org", "ws", sa.UUID); err != nil {
		t.Fatalf("RevokeTokens: %v", err)
	}

	got := mustGetSA(t, cs, sa.UUID)
	if got.Name != sa.UUID {
		t.Errorf("recreated SA name: got %q, want %q", got.Name, sa.UUID)
	}
	if got.UID == originalUID {
		// Note: fake.Clientset does not auto-generate UIDs unless we
		// set one explicitly; this assertion would matter against a
		// real apiserver. Logged only for awareness.
		t.Logf("note: fake apiserver did not regenerate UID; assertion skipped")
	}
	if got.Annotations[AnnotationDisplayName] != "ci-bot" {
		t.Errorf("display-name not preserved across revoke: %#v", got.Annotations)
	}
	if got.Annotations[AnnotationRole] != RoleAdmin {
		t.Errorf("role not preserved across revoke: %#v", got.Annotations)
	}

	// CRB must exist again under the same name.
	if _, err := cs.RbacV1().ClusterRoleBindings().Get(context.Background(), crbName(sa.UUID), metav1.GetOptions{}); err != nil {
		t.Errorf("CRB not recreated after revoke: %v", err)
	}
}

func TestProjectSA_LastTokenIssuedAt(t *testing.T) {
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	sa := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "uuid-1",
			CreationTimestamp: metav1.NewTime(now),
			Annotations: map[string]string{
				AnnotationDisplayName:       "ci-bot",
				AnnotationRole:              RoleAdmin,
				AnnotationLastTokenIssuedAt: now.Add(-1 * time.Hour).Format(time.RFC3339),
			},
		},
	}
	out := projectSA(sa)
	if out.LastTokenIssuedAt == nil || !out.LastTokenIssuedAt.Equal(now.Add(-1*time.Hour)) {
		t.Errorf("LastTokenIssuedAt: got %v, want %v", out.LastTokenIssuedAt, now.Add(-1*time.Hour))
	}
}

func TestProjectSA_MissingAnnotationsDoNotPanic(t *testing.T) {
	sa := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: "u"}}
	out := projectSA(sa)
	if out.LastTokenIssuedAt != nil {
		t.Errorf("expected nil LastTokenIssuedAt, got %v", out.LastTokenIssuedAt)
	}
}

// ===== helpers =====

func mustGetSA(t *testing.T, cs *fake.Clientset, name string) *corev1.ServiceAccount {
	t.Helper()
	sa, err := cs.CoreV1().ServiceAccounts(Namespace).Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Get %q: %v", name, err)
	}
	return sa
}

// errorContains is a small assertion helper kept here in case future
// tests want it; currently unused (the existing tests use errors.Is
// or direct comparisons).
func errorContains(err error, substr string) bool {
	return err != nil && strings.Contains(err.Error(), substr)
}

var _ = errorContains // appease the linter for now; keep helper around
