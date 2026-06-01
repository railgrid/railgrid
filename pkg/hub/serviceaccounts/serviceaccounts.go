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

// Package serviceaccounts implements roadmap step 9 (O-14): bot
// identity for child Workspaces via native kube
// `core/v1.ServiceAccount`s marked with kedge annotations.
//
// There is no wrapping kedge CRD; an SA is exactly a kube SA in the
// child workspace's `default` namespace, labelled and annotated to
// project our domain concerns:
//
//	labels:
//	  tenancy.kedge.faros.sh/kedge-sa: "true"
//	annotations:
//	  tenancy.kedge.faros.sh/display-name:           <human label>
//	  tenancy.kedge.faros.sh/role:                   admin|member
//	  tenancy.kedge.faros.sh/last-token-issued-at:   <RFC3339>
//
// Plus a ClusterRoleBinding owned by the SA that maps
// `system:serviceaccount:default:<sa-name>` to `cluster-admin`
// today; the binding target will switch to the documented
// `kedge:workspace:admin` / `kedge:workspace:member` ClusterRoles
// when those land. Until then the role annotation carries the user
// intent so the REST layer can reflect it without breaking the
// public contract.
//
// Tokens are minted via the kube TokenRequest API with audience
// `kedge` and a 1-year expiry. Revoke = delete the SA (kills all
// outstanding tokens; the ClusterRoleBinding GCs via owner ref) and
// recreate under the same UUID + annotations.
package serviceaccounts

import (
	"context"
	"fmt"
	"strings"
	"time"

	authnv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/google/uuid"
)

const (
	// Namespace is the kube namespace inside every child Workspace
	// where kedge ServiceAccounts live. The bootstrap controller
	// creates this namespace right after binding the kedge APIBinding
	// (see pkg/hub/kcp/bootstrap.go ensureDefaultNamespace), so it is
	// always present by the time SA endpoints can be reached.
	Namespace = "default"

	// LabelKedgeSA marks a kube ServiceAccount as a kedge-managed SA.
	// Cheap listing selector; used both by the REST list endpoint and
	// the soft-delete cascade if it ever wants to enumerate.
	LabelKedgeSA = "tenancy.kedge.faros.sh/kedge-sa"

	// AnnotationDisplayName carries the human-facing label. Editable
	// via PATCH; not unique.
	AnnotationDisplayName = "tenancy.kedge.faros.sh/display-name"

	// AnnotationRole carries the requested role ("admin" or
	// "member"). The hub maintains a ClusterRoleBinding consistent
	// with this value; the annotation is the source of truth for the
	// REST projection. PATCH on this triggers a CRB rewrite.
	AnnotationRole = "tenancy.kedge.faros.sh/role"

	// AnnotationLastTokenIssuedAt is RFC3339 — set on each successful
	// /tokens POST so the portal can render "last issued N days ago"
	// without scanning audit logs.
	AnnotationLastTokenIssuedAt = "tenancy.kedge.faros.sh/last-token-issued-at"

	// RoleAdmin / RoleMember are the only valid role values. Mirrors
	// Membership.role; validated at the REST boundary.
	RoleAdmin  = "admin"
	RoleMember = "member"

	// TokenAudience is the JWT audience claim we request for SA
	// tokens. kcp validates against this; the kedge proxy doesn't
	// inspect it.
	TokenAudience = "kedge"

	// DefaultTokenExpiry is the requested validity of a freshly
	// minted SA token. 1 year matches the doc's "rotation reminder
	// UI" cadence; admins can rotate sooner.
	DefaultTokenExpiry = 365 * 24 * time.Hour

	// crbNamePrefix is the prefix for the ClusterRoleBinding paired
	// with each SA. Suffixed by the SA's UUID so listing per-SA is
	// trivial.
	crbNamePrefix = "kedge-sa-"
)

// WorkspaceConfigBuilder produces a rest.Config targeting a specific
// child Workspace. Pulled out as an interface so the REST handler
// package can take a thin seam and so tests can plug a fake without
// importing the kcp bootstrapper.
//
// Implemented by the kcp Bootstrapper (see
// pkg/hub/kcp/bootstrap.go ChildWorkspaceConfig — added in this PR).
type WorkspaceConfigBuilder interface {
	ChildWorkspaceConfig(orgUUID, wsUUID string) *rest.Config
}

// Manager is the per-Workspace CRUD surface for kedge ServiceAccounts.
// One Manager handles all Workspaces; each call builds a fresh kube
// clientset targeting the requested (orgUUID, wsUUID).
type Manager struct {
	cfg WorkspaceConfigBuilder
}

// NewManager constructs a Manager backed by the given workspace
// config builder.
func NewManager(cfg WorkspaceConfigBuilder) *Manager {
	return &Manager{cfg: cfg}
}

// SA is the REST-layer projection of a kedge ServiceAccount. The
// underlying object is a kube SA + a ClusterRoleBinding; callers see
// the union as one resource.
type SA struct {
	// UUID is metadata.name on both the kube SA and the
	// ClusterRoleBinding (with the crbNamePrefix). UUID v4, assigned
	// by the hub at create time per O-1.
	UUID string `json:"uuid"`

	// DisplayName is the human-facing label, taken from the
	// AnnotationDisplayName annotation. Editable.
	DisplayName string `json:"displayName"`

	// Role is "admin" or "member". Source of truth is the
	// AnnotationRole annotation; the ClusterRoleBinding is kept in
	// sync by the Manager.
	Role string `json:"role"`

	// CreatedAt mirrors metadata.creationTimestamp on the underlying
	// kube SA. Read-only.
	CreatedAt time.Time `json:"createdAt"`

	// LastTokenIssuedAt mirrors the AnnotationLastTokenIssuedAt
	// annotation; nil when no token has ever been issued.
	LastTokenIssuedAt *time.Time `json:"lastTokenIssuedAt,omitempty"`
}

// Token is the response shape from POST .../tokens. The actual token
// string is in Token; ExpiresAt is what the kube TokenRequest API
// returned (may be capped below what we requested by cluster policy).
//
// There is intentionally no Get path for the token string; callers
// store it themselves.
type Token struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expiresAt"`
}

// Create writes a new kedge SA in the given Workspace. The hub
// assigns a UUID; displayName and role are taken from input.
func (m *Manager) Create(ctx context.Context, orgUUID, wsUUID, displayName, role string) (*SA, error) {
	if err := validateRole(role); err != nil {
		return nil, err
	}
	if displayName == "" {
		return nil, fmt.Errorf("displayName is required")
	}

	cs, err := m.clientset(orgUUID, wsUUID)
	if err != nil {
		return nil, err
	}

	saUUID := uuid.NewString()
	sa := buildSAObject(saUUID, displayName, role, nil)

	created, err := cs.CoreV1().ServiceAccounts(Namespace).Create(ctx, sa, metav1.CreateOptions{})
	if err != nil {
		// UUID collision is vanishingly unlikely; if it happens, fail
		// hard and let the caller retry rather than masking the bug.
		return nil, fmt.Errorf("creating ServiceAccount: %w", err)
	}

	// CRB ownerRef lets kube garbage-collect it when the SA is
	// deleted. ownerRef must use the freshly-created SA's UID.
	crb := buildCRB(saUUID, created.UID, role)
	if _, err := cs.RbacV1().ClusterRoleBindings().Create(ctx, crb, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		// Roll back the SA so we don't leave a dangling resource
		// pointing at no CRB; if even rollback fails, surface both.
		if delErr := cs.CoreV1().ServiceAccounts(Namespace).Delete(ctx, sa.Name, metav1.DeleteOptions{}); delErr != nil {
			return nil, fmt.Errorf("creating ClusterRoleBinding failed (%w) and rolling back SA failed (%v)", err, delErr)
		}
		return nil, fmt.Errorf("creating ClusterRoleBinding: %w", err)
	}

	return projectSA(created), nil
}

// List returns every kedge SA in the Workspace.
func (m *Manager) List(ctx context.Context, orgUUID, wsUUID string) ([]SA, error) {
	cs, err := m.clientset(orgUUID, wsUUID)
	if err != nil {
		return nil, err
	}
	list, err := cs.CoreV1().ServiceAccounts(Namespace).List(ctx, metav1.ListOptions{
		LabelSelector: LabelKedgeSA + "=true",
	})
	if err != nil {
		return nil, fmt.Errorf("listing ServiceAccounts: %w", err)
	}
	out := make([]SA, 0, len(list.Items))
	for i := range list.Items {
		out = append(out, *projectSA(&list.Items[i]))
	}
	return out, nil
}

// Get returns a single kedge SA by UUID, or a Kubernetes NotFound
// error if it doesn't exist (so the handler can translate to 404).
func (m *Manager) Get(ctx context.Context, orgUUID, wsUUID, saUUID string) (*SA, error) {
	cs, err := m.clientset(orgUUID, wsUUID)
	if err != nil {
		return nil, err
	}
	sa, err := cs.CoreV1().ServiceAccounts(Namespace).Get(ctx, saUUID, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	if !isKedgeSA(sa) {
		// Found a kube SA with the requested name but it's not one
		// we manage; surface as NotFound so the surface is uniform.
		return nil, apierrors.NewNotFound(schema.GroupResource{Group: "", Resource: "serviceaccounts"}, saUUID)
	}
	return projectSA(sa), nil
}

// Delete removes the SA + its ClusterRoleBinding. Idempotent on
// NotFound. The CRB owner-ref makes the CRB removal automatic via
// kube GC, but we delete it explicitly too so the operation is
// synchronous from the caller's perspective.
func (m *Manager) Delete(ctx context.Context, orgUUID, wsUUID, saUUID string) error {
	cs, err := m.clientset(orgUUID, wsUUID)
	if err != nil {
		return err
	}
	if err := cs.CoreV1().ServiceAccounts(Namespace).Delete(ctx, saUUID, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("deleting ServiceAccount: %w", err)
	}
	if err := cs.RbacV1().ClusterRoleBindings().Delete(ctx, crbName(saUUID), metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("deleting ClusterRoleBinding: %w", err)
	}
	return nil
}

// PatchRoleAndDisplayName applies a partial update. Pass empty
// strings to leave a field unchanged. Role changes rewrite the CRB
// to point at the new role's ClusterRole.
func (m *Manager) PatchRoleAndDisplayName(ctx context.Context, orgUUID, wsUUID, saUUID, newRole, newDisplayName string) (*SA, error) {
	if newRole != "" {
		if err := validateRole(newRole); err != nil {
			return nil, err
		}
	}
	cs, err := m.clientset(orgUUID, wsUUID)
	if err != nil {
		return nil, err
	}
	sa, err := cs.CoreV1().ServiceAccounts(Namespace).Get(ctx, saUUID, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	if !isKedgeSA(sa) {
		return nil, apierrors.NewNotFound(schema.GroupResource{Group: "", Resource: "serviceaccounts"}, saUUID)
	}

	annotated := sa.DeepCopy()
	if annotated.Annotations == nil {
		annotated.Annotations = map[string]string{}
	}
	roleChanged := false
	if newRole != "" && annotated.Annotations[AnnotationRole] != newRole {
		annotated.Annotations[AnnotationRole] = newRole
		roleChanged = true
	}
	if newDisplayName != "" {
		annotated.Annotations[AnnotationDisplayName] = newDisplayName
	}
	updated, err := cs.CoreV1().ServiceAccounts(Namespace).Update(ctx, annotated, metav1.UpdateOptions{})
	if err != nil {
		return nil, fmt.Errorf("updating ServiceAccount annotations: %w", err)
	}

	if roleChanged {
		// Rewrite the CRB to point at the new role's ClusterRole.
		// Simpler than patching subjects + roleRef in place; the GC
		// owner ref keeps cleanup correct.
		if err := cs.RbacV1().ClusterRoleBindings().Delete(ctx, crbName(saUUID), metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("deleting stale ClusterRoleBinding during role change: %w", err)
		}
		crb := buildCRB(saUUID, updated.UID, newRole)
		if _, err := cs.RbacV1().ClusterRoleBindings().Create(ctx, crb, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
			return nil, fmt.Errorf("creating ClusterRoleBinding for new role: %w", err)
		}
	}

	return projectSA(updated), nil
}

// IssueToken mints a fresh kube SA token via TokenRequest with
// audience `kedge` and the default expiry. Stamps the
// last-token-issued-at annotation on success.
func (m *Manager) IssueToken(ctx context.Context, orgUUID, wsUUID, saUUID string) (*Token, error) {
	cs, err := m.clientset(orgUUID, wsUUID)
	if err != nil {
		return nil, err
	}

	// Confirm the SA exists and is one of ours before we mint anything.
	sa, err := cs.CoreV1().ServiceAccounts(Namespace).Get(ctx, saUUID, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	if !isKedgeSA(sa) {
		return nil, apierrors.NewNotFound(schema.GroupResource{Group: "", Resource: "serviceaccounts"}, saUUID)
	}

	expirySeconds := int64(DefaultTokenExpiry.Seconds())
	tr := &authnv1.TokenRequest{
		Spec: authnv1.TokenRequestSpec{
			Audiences:         []string{TokenAudience},
			ExpirationSeconds: &expirySeconds,
		},
	}
	got, err := cs.CoreV1().ServiceAccounts(Namespace).CreateToken(ctx, saUUID, tr, metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("issuing token: %w", err)
	}

	// Stamp the annotation. Best effort: don't fail the user-facing
	// issuance if the annotation write loses a race.
	now := time.Now().UTC().Format(time.RFC3339)
	annotated := sa.DeepCopy()
	if annotated.Annotations == nil {
		annotated.Annotations = map[string]string{}
	}
	annotated.Annotations[AnnotationLastTokenIssuedAt] = now
	_, _ = cs.CoreV1().ServiceAccounts(Namespace).Update(ctx, annotated, metav1.UpdateOptions{})

	return &Token{
		Token:     got.Status.Token,
		ExpiresAt: got.Status.ExpirationTimestamp.Time,
	}, nil
}

// RevokeTokens invalidates every outstanding token for the SA by
// deleting and recreating the SA under the same UUID + annotations.
// The new SA gets a new UID, which the TokenReview path uses to
// validate signatures, so old tokens fail validation. The CRB owner
// ref points at the old UID; we recreate it pointing at the new one.
func (m *Manager) RevokeTokens(ctx context.Context, orgUUID, wsUUID, saUUID string) error {
	cs, err := m.clientset(orgUUID, wsUUID)
	if err != nil {
		return err
	}
	existing, err := cs.CoreV1().ServiceAccounts(Namespace).Get(ctx, saUUID, metav1.GetOptions{})
	if err != nil {
		return err
	}
	if !isKedgeSA(existing) {
		return apierrors.NewNotFound(schema.GroupResource{Group: "", Resource: "serviceaccounts"}, saUUID)
	}

	// Snapshot the annotations + role for re-creation.
	displayName := existing.Annotations[AnnotationDisplayName]
	role := existing.Annotations[AnnotationRole]
	lastIssued := existing.Annotations[AnnotationLastTokenIssuedAt]

	if err := cs.RbacV1().ClusterRoleBindings().Delete(ctx, crbName(saUUID), metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("deleting ClusterRoleBinding during revoke: %w", err)
	}
	if err := cs.CoreV1().ServiceAccounts(Namespace).Delete(ctx, saUUID, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("deleting ServiceAccount during revoke: %w", err)
	}

	extraAnnos := map[string]string{}
	if lastIssued != "" {
		extraAnnos[AnnotationLastTokenIssuedAt] = lastIssued
	}
	sa := buildSAObject(saUUID, displayName, role, extraAnnos)
	created, err := cs.CoreV1().ServiceAccounts(Namespace).Create(ctx, sa, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("recreating ServiceAccount after revoke: %w", err)
	}
	crb := buildCRB(saUUID, created.UID, role)
	if _, err := cs.RbacV1().ClusterRoleBindings().Create(ctx, crb, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("recreating ClusterRoleBinding after revoke: %w", err)
	}
	return nil
}

// ===== internal helpers =====

func (m *Manager) clientset(orgUUID, wsUUID string) (kubernetes.Interface, error) {
	// Test seam: when a fake clientset is registered via
	// testClientset (see manager_test.go) the Manager skips the real
	// kubernetes.NewForConfig path and returns the fake. Production
	// code never sets testClientset.
	if testClientset != nil {
		if cs, ok := testClientset(); ok {
			return cs, nil
		}
	}
	cfg := m.cfg.ChildWorkspaceConfig(orgUUID, wsUUID)
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("building kube clientset for workspace %s/%s: %w", orgUUID, wsUUID, err)
	}
	return cs, nil
}

// testClientset is the test seam used by manager_test.go to swap in
// a kube fake clientset without involving rest.Config. nil in
// production; populated and reset by tests.
//
// Returns (clientset, true) when a test wants to override; (nil, false)
// otherwise.
var testClientset func() (kubernetes.Interface, bool)

// resetTestClientset clears the test seam. Tests call this in a
// defer so a failed assertion doesn't leak state into the next test.
func resetTestClientset() { testClientset = nil }

// buildSAObject constructs the kube SA we want to write. Any extra
// annotations are merged into the standard set.
func buildSAObject(saUUID, displayName, role string, extraAnnos map[string]string) *corev1.ServiceAccount {
	annos := map[string]string{
		AnnotationDisplayName: displayName,
		AnnotationRole:        role,
	}
	for k, v := range extraAnnos {
		annos[k] = v
	}
	return &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:        saUUID,
			Namespace:   Namespace,
			Labels:      map[string]string{LabelKedgeSA: "true"},
			Annotations: annos,
		},
	}
}

// buildCRB constructs the ClusterRoleBinding for an SA. Role today
// always maps to `cluster-admin`; will switch to
// `kedge:workspace:admin` / `kedge:workspace:member` once those
// ClusterRoles are bootstrapped (open follow-up flagged in the
// package doc).
func buildCRB(saUUID string, saUID types.UID, role string) *rbacv1.ClusterRoleBinding {
	return &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: crbName(saUUID),
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion:         "v1",
				Kind:               "ServiceAccount",
				Name:               saUUID,
				UID:                saUID,
				BlockOwnerDeletion: ptr(true),
				Controller:         ptr(true),
			}},
			Labels: map[string]string{LabelKedgeSA: "true"},
			Annotations: map[string]string{
				AnnotationRole: role,
			},
		},
		Subjects: []rbacv1.Subject{{
			Kind:      "ServiceAccount",
			Name:      saUUID,
			Namespace: Namespace,
		}},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "ClusterRole",
			Name:     "cluster-admin",
		},
	}
}

func crbName(saUUID string) string { return crbNamePrefix + saUUID }

// projectSA collapses a kube ServiceAccount into the REST view.
func projectSA(sa *corev1.ServiceAccount) *SA {
	out := &SA{
		UUID:        sa.Name,
		DisplayName: sa.Annotations[AnnotationDisplayName],
		Role:        sa.Annotations[AnnotationRole],
		CreatedAt:   sa.CreationTimestamp.Time,
	}
	if raw := sa.Annotations[AnnotationLastTokenIssuedAt]; raw != "" {
		if t, err := time.Parse(time.RFC3339, raw); err == nil {
			out.LastTokenIssuedAt = &t
		}
	}
	return out
}

func isKedgeSA(sa *corev1.ServiceAccount) bool {
	if sa == nil {
		return false
	}
	if v, ok := sa.Labels[LabelKedgeSA]; ok && strings.EqualFold(v, "true") {
		return true
	}
	return false
}

func validateRole(role string) error {
	switch role {
	case RoleAdmin, RoleMember:
		return nil
	}
	return fmt.Errorf("invalid role %q (want %s or %s)", role, RoleAdmin, RoleMember)
}

func ptr[T any](v T) *T { return &v }
