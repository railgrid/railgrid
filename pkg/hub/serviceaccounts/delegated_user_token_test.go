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
	"sync"
	"testing"
	"time"

	authnv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clienttesting "k8s.io/client-go/testing"
)

const (
	delegatedTestOrg      = "org"
	delegatedTestWS       = "workspace"
	delegatedTestProvider = "infrastructure"
)

var delegatedTestUser = Identity{User: "alice"}

// delegatedTokenReactor answers TokenRequests with a token that names the
// mint count, so a test can tell a cached token from a fresh one, and returns
// the issued expiry it was configured with.
func delegatedTokenReactor(m *Manager, ttl time.Duration) (*int, clienttesting.ReactionFunc) {
	mints := 0
	return &mints, func(action clienttesting.Action) (bool, runtime.Object, error) {
		mints++
		return true, &authnv1.TokenRequest{Status: authnv1.TokenRequestStatus{
			Token:               "delegated-" + strings.Repeat("x", mints),
			ExpirationTimestamp: metav1.NewTime(m.clock().Add(ttl)),
		}}, nil
	}
}

func TestIssueDelegatedUserTokenCreatesOneAccountBoundToMemberRole(t *testing.T) {
	m, cs := managerFor(t)
	defer resetTestClientset()
	var gotAudience []string
	var gotTTL int64
	cs.PrependReactor("create", "serviceaccounts/token", func(action clienttesting.Action) (bool, runtime.Object, error) {
		request := action.(clienttesting.CreateAction).GetObject().(*authnv1.TokenRequest)
		gotAudience = append([]string(nil), request.Spec.Audiences...)
		gotTTL = *request.Spec.ExpirationSeconds
		return true, &authnv1.TokenRequest{Status: authnv1.TokenRequestStatus{
			Token:               "delegated-token",
			ExpirationTimestamp: metav1.NewTime(time.Now().Add(WorkloadIdentityTokenTTL)),
		}}, nil
	})

	ctx := context.Background()
	token, expiry, err := m.IssueDelegatedUserToken(ctx, delegatedTestOrg, delegatedTestWS, delegatedTestUser, delegatedTestProvider)
	if err != nil {
		t.Fatalf("IssueDelegatedUserToken: %v", err)
	}
	if token != "delegated-token" || expiry.IsZero() {
		t.Fatalf("token/expiry = %q/%v", token, expiry)
	}
	if len(gotAudience) != 1 || gotAudience[0] != WorkloadIdentityTokenAudience {
		t.Errorf("TokenRequest audiences = %v, want [%q]", gotAudience, WorkloadIdentityTokenAudience)
	}
	if gotTTL != int64(WorkloadIdentityTokenTTL/time.Second) {
		t.Errorf("TokenRequest TTL = %d, want %d", gotTTL, int64(WorkloadIdentityTokenTTL/time.Second))
	}

	tenantPath := tenantPathFor(delegatedTestOrg, delegatedTestWS)
	name := DelegatedUserServiceAccountName(tenantPath, delegatedTestUser, delegatedTestProvider)
	if !strings.HasPrefix(name, delegatedUserNamePrefix) || len(name) > 63 {
		t.Fatalf("service account name %q is not a short faros-du-* label", name)
	}
	sa, err := cs.CoreV1().ServiceAccounts(Namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get delegated ServiceAccount: %v", err)
	}
	if sa.Labels[LabelDelegatedUser] != "true" || sa.Labels[LabelWorkloadIdentity] != "true" {
		t.Errorf("labels = %v, want delegated-user and workload-identity", sa.Labels)
	}
	if _, isUserManaged := sa.Labels[LabelFarosSA]; isUserManaged {
		t.Error("delegated ServiceAccount carries the user-managed SA label and would be listable/rotatable by tenants")
	}
	for key, want := range map[string]string{
		AnnotationWorkloadIdentityTenantPath: tenantPath,
		AnnotationDelegatedUser:              "alice",
		AnnotationDelegatedOrg:               delegatedTestOrg,
		AnnotationDelegatedWorkspace:         delegatedTestWS,
		AnnotationDelegatedProvider:          delegatedTestProvider,
	} {
		if got := sa.Annotations[key]; got != want {
			t.Errorf("annotation %q = %q, want %q", key, got, want)
		}
	}

	binding, err := cs.RbacV1().ClusterRoleBindings().Get(ctx, DelegatedUserBindingName(name), metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get delegated ClusterRoleBinding: %v", err)
	}
	if binding.RoleRef.Kind != "ClusterRole" || binding.RoleRef.Name != DelegatedUserClusterRole {
		t.Errorf("binding role = %+v, want ClusterRole %s (what workspace members hold today)", binding.RoleRef, DelegatedUserClusterRole)
	}
	if len(binding.Subjects) != 1 || binding.Subjects[0].Kind != "ServiceAccount" || binding.Subjects[0].Name != name || binding.Subjects[0].Namespace != Namespace {
		t.Errorf("binding subjects = %+v, want only the delegated ServiceAccount", binding.Subjects)
	}

	// Second issuance for the same tuple after the cache is dropped reuses
	// the account: nothing accumulates per request.
	m.delegated = nil
	if _, _, err := m.IssueDelegatedUserToken(ctx, delegatedTestOrg, delegatedTestWS, delegatedTestUser, delegatedTestProvider); err != nil {
		t.Fatalf("IssueDelegatedUserToken (repeat): %v", err)
	}
	list, err := cs.CoreV1().ServiceAccounts(Namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		t.Fatalf("list ServiceAccounts: %v", err)
	}
	if len(list.Items) != 1 {
		t.Fatalf("ServiceAccounts after two issuances = %d, want 1", len(list.Items))
	}
}

func TestIssueDelegatedUserTokenCachesAndRefreshesOnExpiry(t *testing.T) {
	m, cs := managerFor(t)
	defer resetTestClientset()
	now := time.Now()
	m.now = func() time.Time { return now }
	mints, reactor := delegatedTokenReactor(m, WorkloadIdentityTokenTTL)
	cs.PrependReactor("create", "serviceaccounts/token", reactor)
	ctx := context.Background()

	first, _, err := m.IssueDelegatedUserToken(ctx, delegatedTestOrg, delegatedTestWS, delegatedTestUser, delegatedTestProvider)
	if err != nil {
		t.Fatalf("first issue: %v", err)
	}
	second, _, err := m.IssueDelegatedUserToken(ctx, delegatedTestOrg, delegatedTestWS, delegatedTestUser, delegatedTestProvider)
	if err != nil {
		t.Fatalf("second issue: %v", err)
	}
	if first != second || *mints != 1 {
		t.Fatalf("back-to-back issuance minted %d tokens (%q, %q), want one cached token", *mints, first, second)
	}

	// A different user, workspace, or provider is a different identity and
	// must not share the cache entry.
	if _, _, err := m.IssueDelegatedUserToken(ctx, delegatedTestOrg, delegatedTestWS, Identity{User: "bob"}, delegatedTestProvider); err != nil {
		t.Fatalf("issue for bob: %v", err)
	}
	if _, _, err := m.IssueDelegatedUserToken(ctx, delegatedTestOrg, delegatedTestWS, delegatedTestUser, "vault"); err != nil {
		t.Fatalf("issue for vault: %v", err)
	}
	if *mints != 3 {
		t.Fatalf("mints after distinct tuples = %d, want 3", *mints)
	}

	// Past the cache TTL the token is re-minted even though it has not
	// expired yet.
	now = now.Add(DelegatedUserTokenCacheTTL + time.Second)
	third, _, err := m.IssueDelegatedUserToken(ctx, delegatedTestOrg, delegatedTestWS, delegatedTestUser, delegatedTestProvider)
	if err != nil {
		t.Fatalf("issue after cache TTL: %v", err)
	}
	if third == first || *mints != 4 {
		t.Fatalf("token after cache TTL = %q (mints %d), want a fresh one", third, *mints)
	}
}

func TestIssueDelegatedUserTokenRefreshesBeforeShortExpiry(t *testing.T) {
	m, cs := managerFor(t)
	defer resetTestClientset()
	now := time.Now()
	m.now = func() time.Time { return now }
	// The API server caps the token to 2 minutes, well under the cache TTL:
	// the cache must follow the token's expiry, not its own clock.
	mints, reactor := delegatedTokenReactor(m, 2*time.Minute)
	cs.PrependReactor("create", "serviceaccounts/token", reactor)
	ctx := context.Background()

	if _, _, err := m.IssueDelegatedUserToken(ctx, delegatedTestOrg, delegatedTestWS, delegatedTestUser, delegatedTestProvider); err != nil {
		t.Fatalf("first issue: %v", err)
	}
	now = now.Add(90 * time.Second)
	if _, _, err := m.IssueDelegatedUserToken(ctx, delegatedTestOrg, delegatedTestWS, delegatedTestUser, delegatedTestProvider); err != nil {
		t.Fatalf("issue near expiry: %v", err)
	}
	if *mints != 2 {
		t.Fatalf("mints = %d, want a refresh once the token is within %v of expiry", *mints, delegatedUserTokenExpirySlack)
	}
}

func TestIssueDelegatedUserTokenRejectsOverlongTokenAndBadInput(t *testing.T) {
	m, cs := managerFor(t)
	defer resetTestClientset()
	cs.PrependReactor("create", "serviceaccounts/token", func(action clienttesting.Action) (bool, runtime.Object, error) {
		return true, &authnv1.TokenRequest{Status: authnv1.TokenRequestStatus{
			Token:               "too-long",
			ExpirationTimestamp: metav1.NewTime(time.Now().Add(WorkloadIdentityTokenTTL + 2*time.Second)),
		}}, nil
	})
	ctx := context.Background()
	if _, _, err := m.IssueDelegatedUserToken(ctx, delegatedTestOrg, delegatedTestWS, delegatedTestUser, delegatedTestProvider); err == nil {
		t.Fatal("accepted a token longer than policy allows")
	}
	for name, call := range map[string]func() error{
		"empty user": func() error {
			_, _, err := m.IssueDelegatedUserToken(ctx, delegatedTestOrg, delegatedTestWS, Identity{}, delegatedTestProvider)
			return err
		},
		"empty workspace": func() error {
			_, _, err := m.IssueDelegatedUserToken(ctx, delegatedTestOrg, "", delegatedTestUser, delegatedTestProvider)
			return err
		},
		"colon in org": func() error {
			_, _, err := m.IssueDelegatedUserToken(ctx, "org:evil", delegatedTestWS, delegatedTestUser, delegatedTestProvider)
			return err
		},
		"empty provider": func() error {
			_, _, err := m.IssueDelegatedUserToken(ctx, delegatedTestOrg, delegatedTestWS, delegatedTestUser, "")
			return err
		},
	} {
		if err := call(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestIssueDelegatedUserTokenRefusesForeignAccountOfSameName(t *testing.T) {
	tenantPath := tenantPathFor(delegatedTestOrg, delegatedTestWS)
	name := DelegatedUserServiceAccountName(tenantPath, delegatedTestUser, delegatedTestProvider)
	// A hand-made account squatting on the deterministic name, without the
	// hub's labels: never adopt it, never mint for it.
	m, _ := managerFor(t, &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: Namespace}})
	defer resetTestClientset()
	if _, _, err := m.IssueDelegatedUserToken(context.Background(), delegatedTestOrg, delegatedTestWS, delegatedTestUser, delegatedTestProvider); err == nil {
		t.Fatal("minted a token for a ServiceAccount the hub did not create")
	}
}

// hubMintedDelegatedServiceAccount is what the hub writes: the account plus
// the keyed proof it stamps once the object (and its UID) exists.
func hubMintedDelegatedServiceAccount(t *testing.T, tenantPath string, user Identity, uid types.UID) *corev1.ServiceAccount {
	t.Helper()
	name := DelegatedUserServiceAccountName(tenantPath, user, delegatedTestProvider)
	sa := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{
		Name: name, Namespace: Namespace, UID: uid,
		Labels:      map[string]string{LabelWorkloadIdentity: "true", LabelDelegatedUser: "true"},
		Annotations: delegatedUserAnnotations(tenantPath, delegatedTestOrg, delegatedTestWS, user, delegatedTestProvider),
	}}
	key, err := testProofKeySource.DelegatedProofKey(context.Background())
	if err != nil {
		t.Fatalf("test proof key: %v", err)
	}
	sa.Annotations[AnnotationDelegatedProof] = computeDelegatedProof(key, tenantPath, user, delegatedTestProvider, name, uid)
	return sa
}

func TestVerifyDelegatedUserAnnotationsAcceptsHubMintedAndRejectsTampered(t *testing.T) {
	ctx := context.Background()
	tenantPath := tenantPathFor(delegatedTestOrg, delegatedTestWS)
	good := hubMintedDelegatedServiceAccount(t, tenantPath, delegatedTestUser, "uid-good")
	if err := verifyWorkloadServiceAccountAnnotations(ctx, good, tenantPath, testProofKeySource); err != nil {
		t.Fatalf("hub-minted delegated account rejected: %v", err)
	}
	identity, err := DelegatedUserFromServiceAccount(ctx, testProofKeySource, good)
	if err != nil || identity.User != "alice" {
		t.Fatalf("DelegatedUserFromServiceAccount = %+v, %v", identity, err)
	}

	otherTenant := good.DeepCopy()
	otherTenant.Annotations[AnnotationDelegatedWorkspace] = "elsewhere"
	if err := verifyWorkloadServiceAccountAnnotations(ctx, otherTenant, tenantPath, testProofKeySource); err == nil {
		t.Error("accepted a delegated account whose workspace annotation disagrees with its tenant path")
	}
	noUser := good.DeepCopy()
	delete(noUser.Annotations, AnnotationDelegatedUser)
	if err := verifyWorkloadServiceAccountAnnotations(ctx, noUser, tenantPath, testProofKeySource); err == nil {
		t.Error("accepted a delegated account with no user annotation")
	}
	if err := verifyWorkloadServiceAccountAnnotations(ctx, good, "root:faros:tenants:org:other", testProofKeySource); err == nil {
		t.Error("accepted a delegated account for a tenant it was not minted in")
	}
	// A workload identity is still held to the full Project tuple.
	notDelegated := good.DeepCopy()
	delete(notDelegated.Labels, LabelDelegatedUser)
	if err := verifyWorkloadServiceAccountAnnotations(ctx, notDelegated, tenantPath, testProofKeySource); err == nil {
		t.Error("a non-delegated account with only delegated annotations passed the workload check")
	}
}

// TestDelegatedIdentityRejectsTenantForgedServiceAccount is the privilege
// escalation this proof exists to close.
//
// A delegated ServiceAccount lives in namespace "default" of the tenant's own
// team workspace, and the bootstrap binds every workspace member to
// cluster-admin there. An ordinary member can therefore create exactly the
// object the hub would have created — right name (it is a hash of the tenant
// path, the user, and the provider, all public), right label, all four
// identity annotations naming whoever they choose — mint a token for it with
// TokenRequest, and hand it to the provider proxy. Before the proof, every
// check the hub made compared inputs that member had written, so the far end
// received X-Faros-User naming the victim.
func TestDelegatedIdentityRejectsTenantForgedServiceAccount(t *testing.T) {
	ctx := context.Background()
	tenantPath := tenantPathFor(delegatedTestOrg, delegatedTestWS)
	victim := Identity{User: "victim@example.com"}

	// Built exactly as a malicious workspace member would build it, and given
	// a UID as the apiserver would on create. Everything here is genuine
	// except that no hub ever touched it.
	forged := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{
		Name:      DelegatedUserServiceAccountName(tenantPath, victim, delegatedTestProvider),
		Namespace: Namespace,
		UID:       "uid-attacker-made",
		Labels:    map[string]string{LabelWorkloadIdentity: "true", LabelDelegatedUser: "true"},
		Annotations: delegatedUserAnnotations(
			tenantPath, delegatedTestOrg, delegatedTestWS, victim, delegatedTestProvider),
	}}

	if err := verifyWorkloadServiceAccountAnnotations(ctx, forged, tenantPath, testProofKeySource); err == nil {
		t.Error("a tenant-forged delegated ServiceAccount passed workload verification")
	}
	if identity, err := DelegatedUserFromServiceAccount(ctx, testProofKeySource, forged); err == nil {
		t.Errorf("a tenant-forged delegated ServiceAccount resolved as %q", identity.User)
	}

	// Nor can the attacker lift a valid proof off a real account: the MAC
	// covers the user, the provider, the object name, and the object UID.
	legitimate := hubMintedDelegatedServiceAccount(t, tenantPath, delegatedTestUser, "uid-legit")
	stolen := forged.DeepCopy()
	stolen.Annotations[AnnotationDelegatedProof] = legitimate.Annotations[AnnotationDelegatedProof]
	if _, err := DelegatedUserFromServiceAccount(ctx, testProofKeySource, stolen); err == nil {
		t.Error("a proof copied from another account was accepted")
	}

	// Nor can they take over a real account by recreating it under the same
	// name: the UID is part of the MAC, so the copied proof dies with the
	// original object.
	recreated := legitimate.DeepCopy()
	recreated.UID = "uid-attacker-recreated"
	if _, err := DelegatedUserFromServiceAccount(ctx, testProofKeySource, recreated); err == nil {
		t.Error("a delegated account recreated under a new UID kept its proof")
	}

	// Rewriting the user on a real account invalidates its proof too.
	rewritten := legitimate.DeepCopy()
	rewritten.Annotations[AnnotationDelegatedUser] = victim.User
	if identity, err := DelegatedUserFromServiceAccount(ctx, testProofKeySource, rewritten); err == nil {
		t.Errorf("a rewritten user annotation resolved as %q", identity.User)
	}

	// And the label alone must not open a path that skips the proof: with no
	// key source configured the identity is refused outright rather than
	// falling through to some other reading of the object.
	if err := verifyWorkloadServiceAccountAnnotations(ctx, legitimate, tenantPath, nil); err == nil {
		t.Error("a delegated account was accepted with no proof key source configured")
	}

	// Positive control: the hub's own account still resolves.
	if identity, err := DelegatedUserFromServiceAccount(ctx, testProofKeySource, legitimate); err != nil || identity.User != delegatedTestUser.User {
		t.Fatalf("hub-minted account did not resolve: %+v, %v", identity, err)
	}
}

// TestIssueDelegatedUserTokenStampsProofAndReplacesSquatter covers the mint
// side: the hub writes a proof, and it refuses to bless an unproven account
// that a member pre-created and already holds a token for. Adopting one would
// hand the attacker's existing token the victim's identity, so the account is
// replaced — the new UID makes the old token dead.
func TestIssueDelegatedUserTokenStampsProofAndReplacesSquatter(t *testing.T) {
	ctx := context.Background()
	tenantPath := tenantPathFor(delegatedTestOrg, delegatedTestWS)
	name := DelegatedUserServiceAccountName(tenantPath, delegatedTestUser, delegatedTestProvider)

	// The squatter: the object a member would create, with a UID they hold a
	// token against, and no proof.
	squatter := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{
		Name: name, Namespace: Namespace, UID: "uid-squatter",
		Labels: map[string]string{LabelWorkloadIdentity: "true", LabelDelegatedUser: "true"},
		Annotations: delegatedUserAnnotations(
			tenantPath, delegatedTestOrg, delegatedTestWS, delegatedTestUser, delegatedTestProvider),
	}}
	m, cs := managerFor(t, squatter)
	defer resetTestClientset()
	cs.PrependReactor("create", "serviceaccounts/token", func(clienttesting.Action) (bool, runtime.Object, error) {
		return true, &authnv1.TokenRequest{Status: authnv1.TokenRequestStatus{
			Token:               "delegated-token",
			ExpirationTimestamp: metav1.NewTime(time.Now().Add(WorkloadIdentityTokenTTL)),
		}}, nil
	})

	if _, _, err := m.IssueDelegatedUserToken(ctx, delegatedTestOrg, delegatedTestWS, delegatedTestUser, delegatedTestProvider); err != nil {
		t.Fatalf("IssueDelegatedUserToken: %v", err)
	}
	sa, err := cs.CoreV1().ServiceAccounts(Namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get delegated ServiceAccount: %v", err)
	}
	if sa.UID == squatter.UID {
		t.Fatal("the hub adopted the pre-created account; a token the attacker already holds now resolves as the victim")
	}
	key, err := testProofKeySource.DelegatedProofKey(ctx)
	if err != nil {
		t.Fatalf("test proof key: %v", err)
	}
	if err := verifyDelegatedProof(key, sa); err != nil {
		t.Fatalf("hub-minted account carries no valid proof: %v", err)
	}
	if _, err := DelegatedUserFromServiceAccount(ctx, testProofKeySource, sa); err != nil {
		t.Fatalf("hub-minted account does not resolve: %v", err)
	}
}

// TestIssueDelegatedUserTokenFailsClosedWithoutProofKey: a Manager that cannot
// sign must not mint an account whose identity nothing can later establish.
func TestIssueDelegatedUserTokenFailsClosedWithoutProofKey(t *testing.T) {
	m, _ := managerFor(t)
	defer resetTestClientset()
	m.proofKeys = nil
	if _, _, err := m.IssueDelegatedUserToken(context.Background(), delegatedTestOrg, delegatedTestWS, delegatedTestUser, delegatedTestProvider); err == nil {
		t.Fatal("minted a delegated token with no proof key source")
	}
}

// TestIssueDelegatedUserTokenMintsOnceUnderConcurrency: concurrent callers for
// one tuple share a single mint instead of each issuing their own token and
// racing on the same ServiceAccount and binding.
func TestIssueDelegatedUserTokenMintsOnceUnderConcurrency(t *testing.T) {
	m, cs := managerFor(t)
	defer resetTestClientset()

	var mu sync.Mutex
	mints := 0
	release := make(chan struct{})
	cs.PrependReactor("create", "serviceaccounts/token", func(clienttesting.Action) (bool, runtime.Object, error) {
		mu.Lock()
		mints++
		mu.Unlock()
		// Hold the mint open until every caller has arrived, so a
		// non-deduplicated implementation cannot pass by being fast enough
		// for later callers to hit the cache.
		<-release
		return true, &authnv1.TokenRequest{Status: authnv1.TokenRequestStatus{
			Token:               "delegated-token",
			ExpirationTimestamp: metav1.NewTime(time.Now().Add(WorkloadIdentityTokenTTL)),
		}}, nil
	})

	const callers = 8
	ctx := context.Background()
	tokens := make([]string, callers)
	errs := make([]error, callers)
	var wg sync.WaitGroup
	for i := range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tokens[i], _, errs[i] = m.IssueDelegatedUserToken(ctx, delegatedTestOrg, delegatedTestWS, delegatedTestUser, delegatedTestProvider)
		}()
	}
	// Give every caller time to reach the singleflight before the one that
	// won it completes.
	time.Sleep(100 * time.Millisecond)
	close(release)
	wg.Wait()

	for i := range callers {
		if errs[i] != nil {
			t.Fatalf("caller %d: %v", i, errs[i])
		}
		if tokens[i] != "delegated-token" {
			t.Fatalf("caller %d got token %q", i, tokens[i])
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if mints != 1 {
		t.Fatalf("concurrent callers for one tuple minted %d tokens, want 1", mints)
	}
}
