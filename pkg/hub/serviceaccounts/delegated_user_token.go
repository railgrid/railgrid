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

// Delegated user tokens.
//
// The hub backend proxy used to forward a caller's own hub bearer — an OIDC
// id token or the static admin token — to every provider it fronted. For a
// platform provider that is a (known, accepted) confused deputy. For an
// org-owned provider it is a credential leak: the provider runs in a tenant's
// cluster, so any org member who registers one receives the full hub token of
// every user in the org who touches it, and that token reaches every
// workspace and every REST endpoint the user can.
//
// Platform-provider delegation still uses a short-lived ServiceAccount token
// in the caller's tenant workspace. Organization-owned providers use the
// opaque hub-signed credential in org_backend_token.go instead: their runtime
// must not receive a native KCP bearer or role binding.
//
// The account is deterministic per (tenant, user, provider), so repeated
// requests reuse one ServiceAccount rather than accumulating. Nothing deletes
// them yet: a member leaving the workspace keeps an idle railgrid-du-* account
// bound to a role they no longer hold, though any token minted for it is dead
// within ten minutes. Garbage collection on Membership removal is a follow-up.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
	authnv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	corev1typed "k8s.io/client-go/kubernetes/typed/core/v1"
	rbacv1typed "k8s.io/client-go/kubernetes/typed/rbac/v1"

	"github.com/railgrid/railgrid/pkg/kcppaths"
)

const (
	// LabelDelegatedUser marks a legacy or platform ServiceAccount minted to
	// stand in for a human user at a provider. Organization-owned providers now
	// use OrgBackendClaims instead. Such accounts also carry
	// LabelWorkloadIdentity, so the online verifier treats them as hub-managed
	// and the user-facing ServiceAccount CRUD surface (LabelRailgridSA) never
	// lists or rotates them.
	LabelDelegatedUser = "railgrid.ai/delegated-user"

	// AnnotationDelegatedUser is the User CR name the account stands in for.
	// The tenant resolver surfaces it as X-Railgrid-User so a provider at the far
	// end attributes the call to the person, not to the account.
	AnnotationDelegatedUser = "railgrid.ai/delegated-user"
	// AnnotationDelegatedOrg and AnnotationDelegatedWorkspace record the tenant
	// the account was minted in. AnnotationWorkloadIdentityTenantPath carries
	// the same fact as a path and is what the verifier enforces; these exist so
	// an operator reading the object sees the tuple without parsing a path.
	AnnotationDelegatedOrg       = "railgrid.ai/delegated-org"
	AnnotationDelegatedWorkspace = "railgrid.ai/delegated-workspace"
	// AnnotationDelegatedProvider names the platform provider the token was
	// issued for. It participates in the account name, so a token minted for
	// one provider is a different identity from one minted for another.
	AnnotationDelegatedProvider = "railgrid.ai/delegated-provider"
	// AnnotationDelegatedProviderOrg is the Organization that owns the
	// provider the token was issued for, absent for a platform provider. An
	// org-owned provider may share its name with the platform provider it
	// shadows, so the name alone does not say which one holds the token; the
	// owner does, and it is covered by the proof.
	AnnotationDelegatedProviderOrg = "railgrid.ai/delegated-provider-org"

	// DelegatedUserClusterRole mirrors workspace data administration for platform
	// provider compatibility. BYO providers do not receive this native role.
	DelegatedUserClusterRole = WorkspaceMemberClusterRole

	// DelegatedUserTokenCacheTTL bounds how long an issued token is reused
	// before a fresh one is minted, independently of the token's own expiry.
	// Matches kcpResolverTTL in the tenant resolver so a revoked membership is
	// honoured on the same clock across both caches.
	DelegatedUserTokenCacheTTL = 5 * time.Minute

	// delegatedUserTokenExpirySlack keeps a cached token from being handed to
	// a provider moments before it expires mid-request.
	delegatedUserTokenExpirySlack = time.Minute

	delegatedUserNamePrefix         = "railgrid-du-" // retired organization-provider delegation
	platformDelegatedUserNamePrefix = "railgrid-pdu-"
	delegatedUserBindingInfix       = "-"
)

// Identity is the caller a delegated token stands in for.
type Identity struct {
	// User is the User CR name — the value the backend proxy injects as
	// X-Railgrid-User.
	User string
}

// DelegatedProvider identifies the provider a delegated token was issued
// to: its name and, for an org-owned provider, the Organization that owns it
// (empty for a platform provider).
type DelegatedProvider struct {
	Name    string
	OrgUUID string
}

// PlatformProvider is the DelegatedProvider for a platform provider.
func PlatformProvider(name string) DelegatedProvider { return DelegatedProvider{Name: name} }

// DelegatedIdentity is what a verified delegated token establishes: the
// person it stands in for and the provider it was issued to.
type DelegatedIdentity struct {
	User     string
	Provider DelegatedProvider
	// Legacy is true for an account signed before the provider's owner was
	// part of the proof (proof v1). Provider.OrgUUID is then unknown rather
	// than "platform"; callers that authorize by provider must resolve it the
	// way the proxy did when it issued the token.
	Legacy bool
}

// DelegatedUserServiceAccountName returns the stable, DNS-safe ServiceAccount
// name for a (tenant, user, platform provider) tuple. See
// delegatedServiceAccountName.
func DelegatedUserServiceAccountName(tenantPath string, user Identity, providerName string) string {
	return delegatedServiceAccountName(tenantPath, user, PlatformProvider(providerName))
}

// IsRetiredDelegationUsername reports whether an authenticated KCP identity is
// in the old native delegated-user namespace. The prefix is globally retired:
// old organization-provider credentials had workspace-admin authority, and
// old platform credentials must be reminted under the new platform prefix.
func IsRetiredDelegationUsername(username string) bool {
	return strings.HasPrefix(username, "system:serviceaccount:"+Namespace+":"+delegatedUserNamePrefix)
}

// delegatedServiceAccountName returns the stable, DNS-safe ServiceAccount
// name for one (tenant, user, provider) tuple. Hash-only for the same reason
// WorkloadServiceAccountName is: user names are email-shaped and provider
// names are tenant-chosen, and neither belongs in an RBAC object name. The
// owner org joins the hash only for org-owned providers, so a platform
// provider's account keeps the name it had before owners were recorded and
// is upgraded in place.
func delegatedServiceAccountName(tenantPath string, user Identity, provider DelegatedProvider) string {
	parts := []string{tenantPath, user.User, provider.Name}
	prefix := delegatedUserNamePrefix
	if provider.OrgUUID != "" {
		parts = append(parts, "org="+provider.OrgUUID)
	} else {
		prefix = platformDelegatedUserNamePrefix
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return prefix + hex.EncodeToString(sum[:20])
}

// DelegatedUserBindingName returns the ClusterRoleBinding name paired with a
// delegated ServiceAccount.
func DelegatedUserBindingName(serviceAccountName string) string {
	// Keep the historical object name so reconciliation replaces its immutable
	// RoleRef instead of leaving an older, broader binding alongside the new one.
	return serviceAccountName + delegatedUserBindingInfix + "cluster-admin"
}

type delegatedTokenKey struct {
	orgUUID, wsUUID, user, provider, providerOrg string
}

// String is the singleflight key. Every component is NUL-free by
// validateDelegatedInputs, so distinct tuples cannot collide on one string.
func (k delegatedTokenKey) String() string {
	return strings.Join([]string{k.orgUUID, k.wsUUID, k.user, k.provider, k.providerOrg}, "\x00")
}

type delegatedTokenEntry struct {
	token     string
	expiresAt time.Time
	// refreshAt is when the cache stops serving this entry: the earlier of
	// the cache TTL and the token's expiry minus slack.
	refreshAt time.Time
}

// IssueDelegatedUserToken returns a short-lived ServiceAccount token that
// represents user in the tenant workspace (orgUUID, wsUUID) towards a platform
// provider. The account is created on first use, bound
// to DelegatedUserClusterRole, and reused thereafter; tokens are cached in
// memory per (tenant, user, provider) for DelegatedUserTokenCacheTTL and
// re-minted before they expire.
//
// The caller is responsible for having authenticated user and verified their
// membership in the workspace: this method mints on the hub's own authority
// and does not re-check either.
func (m *Manager) IssueDelegatedUserToken(ctx context.Context, orgUUID, wsUUID string, user Identity, provider DelegatedProvider) (string, time.Time, error) {
	if provider.OrgUUID != "" {
		return "", time.Time{}, fmt.Errorf("native delegated ServiceAccount tokens are disabled for organization-owned providers")
	}
	if err := validateDelegatedInputs(orgUUID, wsUUID, user, provider); err != nil {
		return "", time.Time{}, err
	}
	key := delegatedTokenKey{orgUUID: orgUUID, wsUUID: wsUUID, user: user.User, provider: provider.Name, providerOrg: provider.OrgUUID}

	if token, expiresAt, ok := m.cachedDelegatedToken(key); ok {
		return token, expiresAt, nil
	}

	// Deduplicate the mint per tuple. Without this every concurrent request
	// for the same (org, workspace, user, provider) that misses the cache
	// mints its own token and reconciles the same ServiceAccount and
	// ClusterRoleBinding in parallel — a burst of TokenRequests and writes
	// against one object for a result they all share. singleflight collapses
	// the burst into one mint whose result every waiter receives.
	type mintResult struct {
		token     string
		expiresAt time.Time
	}
	value, err, _ := m.delegatedFlight.Do(key.String(), func() (any, error) {
		// The winner re-checks the cache: a request that queued behind an
		// in-flight mint for a *different* key may still find a fresh entry.
		if token, expiresAt, ok := m.cachedDelegatedToken(key); ok {
			return mintResult{token: token, expiresAt: expiresAt}, nil
		}
		token, expiresAt, err := m.mintDelegatedUserToken(ctx, key, orgUUID, wsUUID, user, provider)
		if err != nil {
			return nil, err
		}
		return mintResult{token: token, expiresAt: expiresAt}, nil
	})
	if err != nil {
		return "", time.Time{}, err
	}
	result := value.(mintResult)
	return result.token, result.expiresAt, nil
}

// cachedDelegatedToken returns a still-usable cached token for key.
func (m *Manager) cachedDelegatedToken(key delegatedTokenKey) (string, time.Time, bool) {
	now := m.clock()
	m.delegatedMu.Lock()
	defer m.delegatedMu.Unlock()
	if entry, ok := m.delegated[key]; ok && now.Before(entry.refreshAt) {
		return entry.token, entry.expiresAt, true
	}
	return "", time.Time{}, false
}

// RevokeLegacyOrgDelegations removes every ServiceAccount in the retired
// native delegation namespace from one team workspace, along with its paired
// broad binding. Platform delegations now use a separate prefix. Workspace
// enumeration is intentionally left to explicit operator tooling; startup
// does not scan tenant-controlled inventories.
func RevokeLegacyOrgDelegations(ctx context.Context, client kubernetes.Interface) error {
	if client == nil {
		return fmt.Errorf("legacy organization delegation cleanup requires a Kubernetes client")
	}
	const pageSize int64 = 100
	continueToken := ""
	for {
		page, err := client.CoreV1().ServiceAccounts(Namespace).List(ctx, metav1.ListOptions{Limit: pageSize, Continue: continueToken})
		if err != nil {
			return fmt.Errorf("listing ServiceAccounts for legacy organization delegation cleanup: %w", err)
		}
		for i := range page.Items {
			sa := &page.Items[i]
			if !strings.HasPrefix(sa.Name, delegatedUserNamePrefix) {
				continue
			}
			if err := deleteLegacyDelegation(ctx, client, sa); err != nil {
				return fmt.Errorf("revoking reserved delegated ServiceAccount %q: %w", sa.Name, err)
			}
		}
		continueToken = page.GetContinue()
		if continueToken == "" {
			return nil
		}
	}
}

func deleteLegacyDelegation(ctx context.Context, client kubernetes.Interface, sa *corev1.ServiceAccount) error {
	if sa.UID == "" {
		return fmt.Errorf("reserved ServiceAccount has no UID; refusing an unguarded delete")
	}
	// Remove the paired binding first. Otherwise a tenant could recreate the
	// deterministic SA name while a finalizer keeps this name-based subject
	// bound to the old broad role.
	bindingName := DelegatedUserBindingName(sa.Name)
	bindings := client.RbacV1().ClusterRoleBindings()
	binding, err := bindings.Get(ctx, bindingName, metav1.GetOptions{})
	if err == nil && bindingHasDelegatedSubject(binding, sa.Name) {
		if binding.UID == "" {
			return fmt.Errorf("paired ClusterRoleBinding has no UID; refusing an unguarded delete")
		}
		if err := bindings.Delete(ctx, bindingName, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &binding.UID}}); err != nil && !apierrors.IsNotFound(err) && !apierrors.IsConflict(err) {
			return fmt.Errorf("deleting paired ClusterRoleBinding: %w", err)
		}
		if err := confirmBindingDeleted(ctx, bindings, bindingName, binding.UID); err != nil {
			return err
		}
	} else if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("getting paired ClusterRoleBinding: %w", err)
	}

	sas := client.CoreV1().ServiceAccounts(Namespace)
	if err := sas.Delete(ctx, sa.Name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &sa.UID}}); err != nil && !apierrors.IsNotFound(err) && !apierrors.IsConflict(err) {
		return fmt.Errorf("deleting ServiceAccount: %w", err)
	}
	return confirmServiceAccountDeleted(ctx, sas, sa.Name, sa.UID)
}

func bindingHasDelegatedSubject(binding *rbacv1.ClusterRoleBinding, name string) bool {
	if binding == nil {
		return false
	}
	for _, subject := range binding.Subjects {
		if subject.Kind == rbacv1.ServiceAccountKind && subject.Namespace == Namespace && subject.Name == name {
			return true
		}
	}
	return false
}

const legacyDeleteConfirmAttempts = 5

func confirmServiceAccountDeleted(ctx context.Context, sas corev1typed.ServiceAccountInterface, name string, uid types.UID) error {
	for attempt := 0; attempt < legacyDeleteConfirmAttempts; attempt++ {
		current, err := sas.Get(ctx, name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("confirming ServiceAccount deletion: %w", err)
		}
		if current.UID != uid {
			return fmt.Errorf("reserved ServiceAccount %q was recreated during deletion", name)
		}
		if current.DeletionTimestamp != nil && len(current.Finalizers) > 0 {
			current = current.DeepCopy()
			current.Finalizers = nil
			if _, updateErr := sas.Update(ctx, current, metav1.UpdateOptions{}); updateErr != nil && !apierrors.IsConflict(updateErr) && !apierrors.IsNotFound(updateErr) {
				return fmt.Errorf("clearing finalizers from reserved ServiceAccount: %w", updateErr)
			}
		}
		if err := waitLegacyDelete(ctx); err != nil {
			return err
		}
	}
	return fmt.Errorf("ServiceAccount %q remained after bounded deletion confirmation", name)
}

func confirmBindingDeleted(ctx context.Context, bindings rbacv1typed.ClusterRoleBindingInterface, name string, uid types.UID) error {
	for attempt := 0; attempt < legacyDeleteConfirmAttempts; attempt++ {
		current, err := bindings.Get(ctx, name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("confirming ClusterRoleBinding deletion: %w", err)
		}
		if current.UID != uid {
			return fmt.Errorf("paired ClusterRoleBinding %q was recreated during deletion", name)
		}
		if current.DeletionTimestamp != nil && len(current.Finalizers) > 0 {
			current = current.DeepCopy()
			current.Finalizers = nil
			if _, updateErr := bindings.Update(ctx, current, metav1.UpdateOptions{}); updateErr != nil && !apierrors.IsConflict(updateErr) && !apierrors.IsNotFound(updateErr) {
				return fmt.Errorf("clearing finalizers from paired ClusterRoleBinding: %w", updateErr)
			}
		}
		if err := waitLegacyDelete(ctx); err != nil {
			return err
		}
	}
	return fmt.Errorf("ClusterRoleBinding %q remained after bounded deletion confirmation", name)
}

func waitLegacyDelete(ctx context.Context) error {
	timer := time.NewTimer(10 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// mintDelegatedUserToken does the work IssueDelegatedUserToken deduplicates:
// reconcile the account and its binding, then issue and cache a token.
func (m *Manager) mintDelegatedUserToken(ctx context.Context, key delegatedTokenKey, orgUUID, wsUUID string, user Identity, provider DelegatedProvider) (string, time.Time, error) {
	now := m.clock()
	proofKey, err := delegatedProofKey(ctx, m.proofKeys)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("delegated user token cannot be minted: %w", err)
	}
	cs, err := m.clientset(orgUUID, wsUUID)
	if err != nil {
		return "", time.Time{}, err
	}
	tenantPath := tenantPathFor(orgUUID, wsUUID)
	name := delegatedServiceAccountName(tenantPath, user, provider)
	if err := ensureDelegatedUserServiceAccount(ctx, cs, proofKey, name, tenantPath, orgUUID, wsUUID, user, provider); err != nil {
		return "", time.Time{}, err
	}
	if err := ensureWorkloadClusterRoleBinding(ctx, cs, DelegatedUserBindingName(name), DelegatedUserClusterRole, name); err != nil {
		return "", time.Time{}, err
	}

	sa, err := cs.CoreV1().ServiceAccounts(Namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return "", time.Time{}, fmt.Errorf("getting delegated account for token audience: %w", err)
	}
	audience, err := delegatedTokenAudienceAt(ctx, m.proofKeys, sa, now)
	if err != nil {
		return "", time.Time{}, err
	}
	expirationSeconds := int64(WorkloadIdentityTokenTTL / time.Second)
	issued, err := cs.CoreV1().ServiceAccounts(Namespace).CreateToken(ctx, name, &authnv1.TokenRequest{Spec: authnv1.TokenRequestSpec{
		Audiences:         []string{WorkloadIdentityTokenAudience, audience},
		ExpirationSeconds: &expirationSeconds,
	}}, metav1.CreateOptions{})
	if err != nil {
		return "", time.Time{}, fmt.Errorf("issuing delegated user token: %w", err)
	}
	if strings.TrimSpace(issued.Status.Token) == "" || issued.Status.ExpirationTimestamp.IsZero() {
		return "", time.Time{}, fmt.Errorf("delegated user token response was incomplete")
	}
	expiresAt := issued.Status.ExpirationTimestamp.Time
	// Same guard as the workload identity: the API server must not be able to
	// turn this into a longer-lived credential than policy allows.
	if expiresAt.After(now.Add(WorkloadIdentityTokenTTL + time.Second)) {
		return "", time.Time{}, fmt.Errorf("delegated user token exceeds maximum lifetime")
	}

	refreshAt := now.Add(DelegatedUserTokenCacheTTL)
	if byExpiry := expiresAt.Add(-delegatedUserTokenExpirySlack); byExpiry.Before(refreshAt) {
		refreshAt = byExpiry
	}
	m.delegatedMu.Lock()
	if m.delegated == nil {
		m.delegated = map[delegatedTokenKey]delegatedTokenEntry{}
	}
	m.delegated[key] = delegatedTokenEntry{token: issued.Status.Token, expiresAt: expiresAt, refreshAt: refreshAt}
	m.delegatedMu.Unlock()
	return issued.Status.Token, expiresAt, nil
}

// IsDelegatedUserServiceAccount reports whether sa was minted by
// IssueDelegatedUserToken.
func IsDelegatedUserServiceAccount(sa *corev1.ServiceAccount) bool {
	return sa != nil && sa.Labels[LabelDelegatedUser] == "true"
}

// DelegatedUserFromServiceAccount returns the human identity a verified
// delegated ServiceAccount stands in for.
//
// SECURITY: everything on this object is tenant-writable except the proof.
// The account lives in namespace "default" of the tenant's own team
// workspace, where the tenant data role allows ServiceAccount management. An ordinary
// member can create a ServiceAccount there, apply LabelDelegatedUser, fill in
// all four identity annotations naming anyone they like, give it the name the
// hub would have derived (the name is a hash of public inputs), and mint a
// token for it with TokenRequest. Every consistency check over those fields
// compares inputs that same member wrote, so none of them prove anything.
// AnnotationDelegatedProof is the sole field they cannot produce — a MAC over
// the identity tuple and the ServiceAccount UID, keyed by a secret held in a
// workspace no tenant identity can reach. Do not add an identity path here
// that does not require it.
func DelegatedUserFromServiceAccount(ctx context.Context, keys ProofKeySource, sa *corev1.ServiceAccount) (Identity, error) {
	id, err := DelegatedIdentityFromServiceAccount(ctx, keys, sa)
	if err != nil {
		return Identity{}, err
	}
	return Identity{User: id.User}, nil
}

// DelegatedIdentityFromServiceAccount verifies a delegated account and returns
// the person and the provider it was minted for. Every field it returns is
// covered by the hub's keyed proof.
func DelegatedIdentityFromServiceAccount(ctx context.Context, keys ProofKeySource, sa *corev1.ServiceAccount) (DelegatedIdentity, error) {
	if !IsDelegatedUserServiceAccount(sa) {
		return DelegatedIdentity{}, fmt.Errorf("ServiceAccount is not a delegated user identity")
	}
	key, err := delegatedProofKey(ctx, keys)
	if err != nil {
		return DelegatedIdentity{}, err
	}
	if err := verifyDelegatedUserAnnotations(key, sa); err != nil {
		return DelegatedIdentity{}, err
	}
	return DelegatedIdentity{
		User: sa.Annotations[AnnotationDelegatedUser],
		Provider: DelegatedProvider{
			Name:    sa.Annotations[AnnotationDelegatedProvider],
			OrgUUID: sa.Annotations[AnnotationDelegatedProviderOrg],
		},
		Legacy: sa.Annotations[AnnotationDelegatedProofVersion] != delegatedProofVersionCurrent,
	}, nil
}

// verifyDelegatedUserAnnotations checks the shape of a delegated identity and
// then the one thing that actually establishes it: the hub's keyed proof.
// The shape checks are hygiene — they keep a malformed annotation out of a
// response header — not authorization. The proof is the authorization.
func verifyDelegatedUserAnnotations(key []byte, sa *corev1.ServiceAccount) error {
	for _, annotation := range []string{
		AnnotationDelegatedUser,
		AnnotationDelegatedOrg,
		AnnotationDelegatedWorkspace,
		AnnotationDelegatedProvider,
	} {
		value := strings.TrimSpace(sa.Annotations[annotation])
		if value == "" || strings.ContainsAny(value, "\r\n\x00") {
			return fmt.Errorf("delegated ServiceAccount has incomplete identity annotations")
		}
	}
	// The path annotation is what the verifier enforced; the org/workspace
	// annotations must agree with it or the object has been tampered with.
	if tenantPathFor(sa.Annotations[AnnotationDelegatedOrg], sa.Annotations[AnnotationDelegatedWorkspace]) != sa.Annotations[AnnotationWorkloadIdentityTenantPath] {
		return fmt.Errorf("delegated ServiceAccount tenant annotations disagree")
	}
	return verifyDelegatedProof(key, sa)
}

func validateDelegatedInputs(orgUUID, wsUUID string, user Identity, provider DelegatedProvider) error {
	if strings.ContainsAny(provider.OrgUUID, "\r\n\x00:") {
		return fmt.Errorf("provider owner org contains prohibited characters")
	}
	for name, value := range map[string]string{
		"orgUUID":      orgUUID,
		"wsUUID":       wsUUID,
		"user":         user.User,
		"providerName": provider.Name,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s is required", name)
		}
		if strings.ContainsAny(value, "\r\n\x00") {
			return fmt.Errorf("%s contains prohibited characters", name)
		}
	}
	// Org and workspace become path segments of the tenant path; a colon in
	// either would let one tuple spell another's path.
	if strings.ContainsAny(orgUUID+wsUUID, ":") {
		return fmt.Errorf("orgUUID and wsUUID must not contain ':'")
	}
	return nil
}

// tenantPathFor is the workspace path the tenant resolver composes from
// X-Railgrid-Org / X-Railgrid-Workspace, which the verifier compares against
// AnnotationWorkloadIdentityTenantPath.
func tenantPathFor(orgUUID, wsUUID string) string {
	return kcppaths.WorkspacePath(orgUUID, wsUUID)
}

func delegatedUserAnnotations(tenantPath, orgUUID, wsUUID string, user Identity, provider DelegatedProvider) map[string]string {
	out := map[string]string{
		AnnotationWorkloadIdentityTenantPath: tenantPath,
		AnnotationDelegatedUser:              user.User,
		AnnotationDelegatedOrg:               orgUUID,
		AnnotationDelegatedWorkspace:         wsUUID,
		AnnotationDelegatedProvider:          provider.Name,
	}
	if provider.OrgUUID != "" {
		out[AnnotationDelegatedProviderOrg] = provider.OrgUUID
	}
	return out
}

// ensureDelegatedServiceAccountAttempts bounds the create/replace loop. Each
// iteration makes progress (create, or delete an unproven object), so a small
// number covers replicas racing on the same tuple.
const ensureDelegatedServiceAccountAttempts = 4

func ensureDelegatedUserServiceAccount(ctx context.Context, cs kubernetes.Interface, proofKey []byte, name, tenantPath, orgUUID, wsUUID string, user Identity, provider DelegatedProvider) error {
	sas := cs.CoreV1().ServiceAccounts(Namespace)
	want := delegatedUserAnnotations(tenantPath, orgUUID, wsUUID, user, provider)
	for attempt := 0; attempt < ensureDelegatedServiceAccountAttempts; attempt++ {
		sa, err := sas.Get(ctx, name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			created, createErr := sas.Create(ctx, &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: Namespace,
				Labels: map[string]string{
					LabelWorkloadIdentity: "true",
					LabelDelegatedUser:    "true",
				},
				Annotations: want,
			}}, metav1.CreateOptions{})
			if apierrors.IsAlreadyExists(createErr) {
				// Lost a race with another replica; re-read and evaluate it.
				continue
			}
			if createErr != nil {
				return fmt.Errorf("creating delegated ServiceAccount: %w", createErr)
			}
			// The proof binds the UID, which only exists once the object does.
			return stampDelegatedProof(ctx, sas, created, proofKey, tenantPath, user, provider.Name)
		}
		if err != nil {
			return fmt.Errorf("getting delegated ServiceAccount: %w", err)
		}
		// Every annotation participates in the name, so an existing account
		// that disagrees on any of them is not ours — refuse rather than adopt
		// it. The name is a hash of the tuple, so this only trips on a
		// hand-made object.
		if !IsDelegatedUserServiceAccount(sa) || sa.Labels[LabelWorkloadIdentity] != "true" {
			return fmt.Errorf("ServiceAccount %q exists and is not a delegated user identity", name)
		}
		for key, value := range want {
			if sa.Annotations[key] != value {
				return fmt.Errorf("ServiceAccount %q is bound to a different delegated identity", name)
			}
		}
		if sa.Annotations[AnnotationDelegatedProviderOrg] != provider.OrgUUID {
			return fmt.Errorf("ServiceAccount %q is bound to a different delegated identity", name)
		}
		if verifyDelegatedProof(proofKey, sa) == nil {
			if sa.Annotations[AnnotationDelegatedProofVersion] == delegatedProofVersionCurrent {
				return nil
			}
			// A v1 account the hub itself signed for this exact tuple (only a
			// platform provider's account keeps its name across versions).
			// Re-sign it in place: the UID is unchanged, so tokens already
			// issued against it stay valid and now verify with the owner in
			// the proof.
			upgraded := sa.DeepCopy()
			if err := SignDelegatedUserServiceAccount(proofKey, upgraded, tenantPath, user, provider.Name); err != nil {
				return err
			}
			if _, err := sas.Update(ctx, upgraded, metav1.UpdateOptions{}); err != nil {
				if apierrors.IsConflict(err) {
					continue
				}
				return fmt.Errorf("upgrading delegated ServiceAccount proof: %w", err)
			}
			return nil
		}
		// The object describes the right identity but carries no proof from
		// this hub. It is either a squatter — a tenant member can create this
		// exact object and mint a token for it, then wait for the hub to bless
		// it — or an account left by a key that no longer applies. Either way
		// it must not be adopted: stamping a proof onto it would validate a
		// token the attacker already holds. Deleting it changes the UID, and
		// the UID is in the MAC, so every token issued against the old object
		// dies with it.
		if err := sas.Delete(ctx, name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{
			UID:             &sa.UID,
			ResourceVersion: &sa.ResourceVersion,
		}}); err != nil && !apierrors.IsNotFound(err) && !apierrors.IsConflict(err) {
			return fmt.Errorf("replacing unproven delegated ServiceAccount %q: %w", name, err)
		}
	}
	return fmt.Errorf("delegated ServiceAccount %q could not be reconciled", name)
}

// stampDelegatedProof writes the hub's proof onto a freshly created account.
// If the write fails the account is removed rather than left unproven: an
// unproven account is useless to the hub and, left behind, is one the next
// mint has to delete anyway.
func stampDelegatedProof(ctx context.Context, sas corev1typed.ServiceAccountInterface, sa *corev1.ServiceAccount, proofKey []byte, tenantPath string, user Identity, providerName string) error {
	if sa.UID == "" {
		return fmt.Errorf("delegated ServiceAccount %q was created without a UID", sa.Name)
	}
	sa = sa.DeepCopy()
	if err := SignDelegatedUserServiceAccount(proofKey, sa, tenantPath, user, providerName); err != nil {
		return err
	}
	if _, err := sas.Update(ctx, sa, metav1.UpdateOptions{}); err != nil {
		_ = sas.Delete(ctx, sa.Name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &sa.UID}})
		return fmt.Errorf("stamping delegated ServiceAccount proof: %w", err)
	}
	return nil
}

// clock is the time source for the token cache; tests override it.
func (m *Manager) clock() time.Time {
	if m.now != nil {
		return m.now()
	}
	return time.Now()
}

// delegatedCache is embedded in Manager so one Manager caches across requests.
type delegatedCache struct {
	delegatedMu sync.Mutex
	delegated   map[delegatedTokenKey]delegatedTokenEntry
	// delegatedFlight collapses concurrent cache misses for one tuple into a
	// single mint. Process-local: it removes the stampede within a replica,
	// which is where it happens — a burst is one user's browser opening
	// several provider requests at once, and those all land on one replica.
	delegatedFlight singleflight.Group
	now             func() time.Time
}
