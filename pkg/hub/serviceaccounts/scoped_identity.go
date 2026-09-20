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

package serviceaccounts

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"time"

	authnv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// This file is THE minter. Every hub-issued scoped identity — the App Studio
// workload capability and every provider-asserted identity — materializes its
// ServiceAccount, ClusterRole and binding here and mints its token here.
// workload_identity.go computes the workload shape (its deterministic name,
// its identity annotations, its Project-derived rules) and then calls
// EnsureScopedIdentity; pkg/hub/identity computes the provider-asserted shape
// and calls the same function. There is deliberately no second place that
// creates a ServiceAccount for a runtime.

const (
	// ScopedIdentityMaxTokenTTL is the hard ceiling on any hub-minted scoped
	// identity token, whatever a caller asks for. A standing credential that
	// outlives a day is not a scoped identity.
	ScopedIdentityMaxTokenTTL = 24 * time.Hour

	// ScopedIdentityMinTokenTTL is the floor, and it is not a comfort number:
	// kcp's TokenRequest admission (like upstream Kubernetes) refuses
	// `spec.expirationSeconds` below ten minutes outright. A floor under that
	// would let a caller ask for a lifetime the minter can never satisfy, and
	// the refusal surfaces far from the cause — as a 502 identity_issue_failed
	// with no hint that the TTL was the problem. Keeping the hub's floor equal
	// to the API server's means everything this package admits is mintable.
	// It matches identity.DefaultWorkloadTTL, which is the same ten minutes
	// for the same reason.
	ScopedIdentityMinTokenTTL = 10 * time.Minute
)

// ScopedIdentityShape is everything the minter needs to materialize one
// identity. Callers compute it; this package never invents a rule or a name.
type ScopedIdentityShape struct {
	// ServiceAccount is the deterministic ServiceAccount name. The paired
	// ClusterRole and ClusterRoleBinding are named by
	// WorkloadIdentityRoleName(ServiceAccount).
	ServiceAccount string
	// Labels are stamped on all three objects. LabelWorkloadIdentity is added
	// automatically: it is what keeps these accounts out of the user-facing
	// service-account CRUD surface, which lists on LabelRailgridSA.
	Labels map[string]string
	// Annotations are stamped on the ServiceAccount only.
	Annotations map[string]string
	// ImmutableAnnotations are the annotation keys that identify the account.
	// Reconciling one to a different value is refused rather than silently
	// rebinding an existing account to another identity. Keys outside this set
	// reconcile freely (a scope marker tracks current grants and must follow
	// them).
	ImmutableAnnotations []string
	// Rules are the ClusterRole's rules, already policy-checked by the caller.
	Rules []rbacv1.PolicyRule
	// TokenTTL is the requested token lifetime, clamped to
	// [ScopedIdentityMinTokenTTL, ScopedIdentityMaxTokenTTL].
	TokenTTL time.Duration
}

// ScopedIdentityToken is a freshly minted, audience-bound capability. The
// token is never persisted by the hub — not in the ScopedIdentity record, not
// in an annotation, not in a Secret.
type ScopedIdentityToken struct {
	Token              string
	ExpiresAt          time.Time
	ServiceAccountName string
	ClusterRoleName    string
}

// ClusterClientFactory builds a kube client for one logical cluster, which is
// the /clusters/{…} segment: a logical cluster id or a workspace path. It is
// the seam pkg/hub/identity addresses tenant workspaces through, because a
// provider knows its caller's cluster id and not the (org, workspace) UUIDs
// the older child-workspace config builder is keyed on.
type ClusterClientFactory func(clusterID string) (kubernetes.Interface, error)

// NewClusterClientFactory returns a factory over the hub's kcp config.
func NewClusterClientFactory(kcpConfig *rest.Config, clusterURL func(host, cluster string) string) (ClusterClientFactory, error) {
	if kcpConfig == nil {
		return nil, fmt.Errorf("kcp config is required")
	}
	if clusterURL == nil {
		return nil, fmt.Errorf("cluster URL builder is required")
	}
	return func(clusterID string) (kubernetes.Interface, error) {
		if strings.TrimSpace(clusterID) == "" {
			return nil, fmt.Errorf("cluster id is required")
		}
		cfg := rest.CopyConfig(kcpConfig)
		cfg.Host = clusterURL(cfg.Host, clusterID)
		return kubernetes.NewForConfig(cfg)
	}, nil
}

// EnsureScopedIdentity reconciles the ServiceAccount, ClusterRole and
// ClusterRoleBinding described by shape in cs, then mints a fresh
// audience-bound TokenRequest for the account.
//
// It deliberately does not go through Create/IssueToken: those keep their
// legacy human-managed semantics and their cluster-admin binding.
func EnsureScopedIdentity(ctx context.Context, cs kubernetes.Interface, shape ScopedIdentityShape) (*ScopedIdentityToken, error) {
	if cs == nil {
		return nil, fmt.Errorf("workspace client is required")
	}
	if strings.TrimSpace(shape.ServiceAccount) == "" {
		return nil, fmt.Errorf("scoped identity ServiceAccount name is required")
	}
	if err := ensureScopedServiceAccount(ctx, cs, shape); err != nil {
		return nil, err
	}
	roleName := WorkloadIdentityRoleName(shape.ServiceAccount)
	if err := ensureScopedClusterRole(ctx, cs, roleName, shape); err != nil {
		return nil, err
	}
	if err := ensureWorkloadClusterRoleBinding(ctx, cs, roleName, roleName, shape.ServiceAccount); err != nil {
		return nil, err
	}
	token, err := mintScopedIdentityToken(ctx, cs, shape.ServiceAccount, shape.TokenTTL)
	if err != nil {
		return nil, err
	}
	token.ClusterRoleName = roleName
	return token, nil
}

// DeleteScopedIdentity removes the ServiceAccount, ClusterRole and binding for
// serviceAccount. Deleting the ServiceAccount is what revokes every
// outstanding token for it, so it is done first and its failure is fatal;
// leftover RBAC without a subject grants nothing, so a missing role or binding
// is not an error.
func DeleteScopedIdentity(ctx context.Context, cs kubernetes.Interface, serviceAccount string) error {
	if cs == nil {
		return fmt.Errorf("workspace client is required")
	}
	if strings.TrimSpace(serviceAccount) == "" {
		return fmt.Errorf("scoped identity ServiceAccount name is required")
	}
	if err := cs.CoreV1().ServiceAccounts(Namespace).Delete(ctx, serviceAccount, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("deleting scoped identity ServiceAccount: %w", err)
	}
	roleName := WorkloadIdentityRoleName(serviceAccount)
	if err := cs.RbacV1().ClusterRoleBindings().Delete(ctx, roleName, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("deleting scoped identity ClusterRoleBinding: %w", err)
	}
	if err := cs.RbacV1().ClusterRoles().Delete(ctx, roleName, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("deleting scoped identity ClusterRole: %w", err)
	}
	return nil
}

// ClampScopedIdentityTTL bounds a requested token lifetime. Zero or negative
// means defaultTTL.
//
// Both bounds clamp rather than refuse, and the floor clamps UP: a caller
// asking for five minutes is issued a ten-minute token, not an error and not
// an unmintable five-minute request. That is deliberate — the alternative,
// passing a below-floor value through to the TokenRequest, produced a 502 from
// kcp's admission ("may not specify a duration less than 10 minutes") that
// named neither the floor nor the caller's TTL. A token that lives longer than
// asked for is the safe direction here: the ceiling still caps it at
// ScopedIdentityMaxTokenTTL, and holders refresh on a timer rather than
// counting on expiry.
func ClampScopedIdentityTTL(requested, defaultTTL time.Duration) time.Duration {
	ttl := requested
	if ttl <= 0 {
		ttl = defaultTTL
	}
	if ttl < ScopedIdentityMinTokenTTL {
		ttl = ScopedIdentityMinTokenTTL
	}
	if ttl > ScopedIdentityMaxTokenTTL {
		ttl = ScopedIdentityMaxTokenTTL
	}
	return ttl
}

func mintScopedIdentityToken(ctx context.Context, cs kubernetes.Interface, name string, ttl time.Duration) (*ScopedIdentityToken, error) {
	ttl = ClampScopedIdentityTTL(ttl, WorkloadIdentityTokenTTL)
	expirationSeconds := int64(ttl / time.Second)
	request := &authnv1.TokenRequest{Spec: authnv1.TokenRequestSpec{
		Audiences:         []string{WorkloadIdentityTokenAudience},
		ExpirationSeconds: &expirationSeconds,
	}}
	issued, err := cs.CoreV1().ServiceAccounts(Namespace).CreateToken(ctx, name, request, metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("issuing scoped identity token: %w", err)
	}
	if strings.TrimSpace(issued.Status.Token) == "" || issued.Status.ExpirationTimestamp.IsZero() {
		return nil, fmt.Errorf("scoped identity token response was incomplete")
	}
	// A misbehaving API server must not turn this path into a longer-lived
	// credential than policy allows. A SHORTER expiration is valid, because
	// cluster admission may cap TokenRequest TTLs.
	if issued.Status.ExpirationTimestamp.After(time.Now().Add(ttl + time.Second)) {
		return nil, fmt.Errorf("scoped identity token exceeds requested lifetime")
	}
	return &ScopedIdentityToken{
		Token:              issued.Status.Token,
		ExpiresAt:          issued.Status.ExpirationTimestamp.Time,
		ServiceAccountName: name,
	}, nil
}

func ensureScopedServiceAccount(ctx context.Context, cs kubernetes.Interface, shape ScopedIdentityShape) error {
	sas := cs.CoreV1().ServiceAccounts(Namespace)
	labels := scopedLabels(shape.Labels)
	sa, err := sas.Get(ctx, shape.ServiceAccount, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = sas.Create(ctx, &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{
			Name:        shape.ServiceAccount,
			Namespace:   Namespace,
			Labels:      labels,
			Annotations: shape.Annotations,
		}}, metav1.CreateOptions{})
		if err != nil && !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("creating scoped identity ServiceAccount: %w", err)
		}
		if err == nil {
			return nil
		}
		sa, err = sas.Get(ctx, shape.ServiceAccount, metav1.GetOptions{})
	}
	if err != nil {
		return fmt.Errorf("getting scoped identity ServiceAccount: %w", err)
	}
	// The deterministic name is a hash of the identity tuple, so a name
	// collision means a hand-made object is sitting on it. Refuse rather than
	// adopt: adopting would hand the tuple's token to whatever that object is
	// already bound to.
	if sa.Labels[LabelWorkloadIdentity] != "true" {
		return fmt.Errorf("ServiceAccount %q is not a hub-managed scoped identity", shape.ServiceAccount)
	}
	immutable := map[string]bool{}
	for _, key := range shape.ImmutableAnnotations {
		immutable[key] = true
	}
	for key, want := range shape.Annotations {
		if !immutable[key] {
			continue
		}
		if existing, ok := sa.Annotations[key]; ok && existing != "" && existing != want {
			return fmt.Errorf("ServiceAccount %q is already bound to a different scoped identity", shape.ServiceAccount)
		}
	}

	updated := sa.DeepCopy()
	changed := false
	if updated.Labels == nil {
		updated.Labels = map[string]string{}
	}
	for key, value := range labels {
		if updated.Labels[key] != value {
			updated.Labels[key] = value
			changed = true
		}
	}
	if updated.Annotations == nil && len(shape.Annotations) > 0 {
		updated.Annotations = map[string]string{}
	}
	for key, value := range shape.Annotations {
		if updated.Annotations[key] != value {
			updated.Annotations[key] = value
			changed = true
		}
	}
	if !changed {
		return nil
	}
	if _, err := sas.Update(ctx, updated, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("reconciling scoped identity ServiceAccount: %w", err)
	}
	return nil
}

func ensureScopedClusterRole(ctx context.Context, cs kubernetes.Interface, roleName string, shape ScopedIdentityShape) error {
	labels := scopedLabels(shape.Labels)
	wantRules := shape.Rules
	if wantRules == nil {
		wantRules = []rbacv1.PolicyRule{}
	}
	roles := cs.RbacV1().ClusterRoles()
	role, err := roles.Get(ctx, roleName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = roles.Create(ctx, &rbacv1.ClusterRole{
			ObjectMeta: metav1.ObjectMeta{Name: roleName, Labels: labels},
			Rules:      wantRules,
		}, metav1.CreateOptions{})
		if err != nil && !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("creating scoped identity ClusterRole: %w", err)
		}
		if err == nil {
			return nil
		}
		role, err = roles.Get(ctx, roleName, metav1.GetOptions{})
	}
	if err != nil {
		return fmt.Errorf("getting scoped identity ClusterRole: %w", err)
	}
	// Rules ARE reconciled, unlike the provider-side EnsureIdentity this
	// replaces: a revoked grant has to shrink the role, or revocation would
	// only ever mean "delete the whole identity".
	updated := role.DeepCopy()
	changed := false
	if !reflect.DeepEqual(updated.Rules, wantRules) {
		updated.Rules = wantRules
		changed = true
	}
	if updated.Labels == nil {
		updated.Labels = map[string]string{}
	}
	for key, value := range labels {
		if updated.Labels[key] != value {
			updated.Labels[key] = value
			changed = true
		}
	}
	if !changed {
		return nil
	}
	if _, err := roles.Update(ctx, updated, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("reconciling scoped identity ClusterRole: %w", err)
	}
	return nil
}

func scopedLabels(extra map[string]string) map[string]string {
	labels := map[string]string{LabelWorkloadIdentity: "true"}
	for key, value := range extra {
		if key == LabelWorkloadIdentity {
			continue
		}
		labels[key] = value
	}
	return labels
}
