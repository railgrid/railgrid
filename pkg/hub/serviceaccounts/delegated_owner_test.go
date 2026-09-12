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
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// TestDelegatedIdentityCarriesProviderOwner: an org-owned provider's account
// records its owner org, under the proof, and gets a different account from
// the platform provider of the same name.
func TestDelegatedIdentityCarriesProviderOwner(t *testing.T) {
	m, cs := managerFor(t)
	defer resetTestClientset()
	_, reactor := delegatedTokenReactor(m, WorkloadIdentityTokenTTL)
	cs.PrependReactor("create", "serviceaccounts/token", reactor)
	ctx := context.Background()

	orgOwned := DelegatedProvider{Name: delegatedTestProvider, OrgUUID: "owner-org"}
	if _, _, err := m.IssueDelegatedUserToken(ctx, delegatedTestOrg, delegatedTestWS, delegatedTestUser, orgOwned); err != nil {
		t.Fatalf("issue org-owned: %v", err)
	}
	if _, _, err := m.IssueDelegatedUserToken(ctx, delegatedTestOrg, delegatedTestWS, delegatedTestUser, PlatformProvider(delegatedTestProvider)); err != nil {
		t.Fatalf("issue platform: %v", err)
	}
	tenantPath := tenantPathFor(delegatedTestOrg, delegatedTestWS)
	orgName := delegatedServiceAccountName(tenantPath, delegatedTestUser, orgOwned)
	platformName := DelegatedUserServiceAccountName(tenantPath, delegatedTestUser, delegatedTestProvider)
	if orgName == platformName {
		t.Fatal("org-owned and platform provider of the same name share one delegated account")
	}

	for name, want := range map[string]DelegatedProvider{orgName: orgOwned, platformName: PlatformProvider(delegatedTestProvider)} {
		sa, err := cs.CoreV1().ServiceAccounts(Namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			t.Fatalf("get %s: %v", name, err)
		}
		id, err := DelegatedIdentityFromServiceAccount(ctx, testProofKeySource, sa)
		if err != nil {
			t.Fatalf("verify %s: %v", name, err)
		}
		if id.Provider != want || id.Legacy || id.User != delegatedTestUser.User {
			t.Fatalf("identity of %s = %+v, want provider %+v, not legacy", name, id, want)
		}
	}

	// Editing the owner annotation — to claim to be the platform provider, or
	// another org's — breaks the proof.
	sa, _ := cs.CoreV1().ServiceAccounts(Namespace).Get(ctx, orgName, metav1.GetOptions{})
	for _, forged := range []string{"", "other-org"} {
		tampered := sa.DeepCopy()
		if forged == "" {
			delete(tampered.Annotations, AnnotationDelegatedProviderOrg)
		} else {
			tampered.Annotations[AnnotationDelegatedProviderOrg] = forged
		}
		if _, err := DelegatedIdentityFromServiceAccount(ctx, testProofKeySource, tampered); err == nil {
			t.Errorf("owner annotation %q was accepted after tampering", forged)
		}
	}
}

// legacyV1Account builds an account exactly as a hub before proof v2 signed
// it: no version annotation, v1 MAC over (tenant, user, provider).
func legacyV1Account(t *testing.T, tenantPath string, uid types.UID) *corev1.ServiceAccount {
	t.Helper()
	name := DelegatedUserServiceAccountName(tenantPath, delegatedTestUser, delegatedTestProvider)
	sa := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{
		Name: name, Namespace: Namespace, UID: uid,
		Labels:      map[string]string{LabelWorkloadIdentity: "true", LabelDelegatedUser: "true"},
		Annotations: delegatedUserAnnotations(tenantPath, delegatedTestOrg, delegatedTestWS, delegatedTestUser, PlatformProvider(delegatedTestProvider)),
	}}
	key, err := testProofKeySource.DelegatedProofKey(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	sa.Annotations[AnnotationDelegatedProof] = computeDelegatedProof(key, "", tenantPath, delegatedTestUser, PlatformProvider(delegatedTestProvider), name, uid)
	return sa
}

// TestLegacyV1ProofVerifiesAsLegacyOnly: a v1 account still verifies (its
// tokens outlive the upgrade by up to their TTL) but is marked Legacy, and a
// v1 account that claims an owner org is not hub-made.
func TestLegacyV1ProofVerifiesAsLegacyOnly(t *testing.T) {
	ctx := context.Background()
	tenantPath := tenantPathFor(delegatedTestOrg, delegatedTestWS)
	sa := legacyV1Account(t, tenantPath, "uid-v1")
	id, err := DelegatedIdentityFromServiceAccount(ctx, testProofKeySource, sa)
	if err != nil {
		t.Fatalf("v1 account rejected: %v", err)
	}
	if !id.Legacy || id.Provider.Name != delegatedTestProvider || id.Provider.OrgUUID != "" {
		t.Fatalf("v1 identity = %+v, want legacy with no owner", id)
	}

	claimsOwner := sa.DeepCopy()
	claimsOwner.Annotations[AnnotationDelegatedProviderOrg] = "owner-org"
	if _, err := DelegatedIdentityFromServiceAccount(ctx, testProofKeySource, claimsOwner); err == nil {
		t.Fatal("v1 proof with an owner-org annotation was accepted")
	}
	unknownVersion := sa.DeepCopy()
	unknownVersion.Annotations[AnnotationDelegatedProofVersion] = "9"
	if _, err := DelegatedIdentityFromServiceAccount(ctx, testProofKeySource, unknownVersion); err == nil {
		t.Fatal("unknown proof version was accepted")
	}
}

// TestEnsureUpgradesLegacyAccountInPlace: minting for a platform provider
// whose v1 account already exists re-signs that account with proof v2 and
// keeps its UID, so tokens issued before the upgrade keep working.
func TestEnsureUpgradesLegacyAccountInPlace(t *testing.T) {
	m, cs := managerFor(t)
	defer resetTestClientset()
	_, reactor := delegatedTokenReactor(m, WorkloadIdentityTokenTTL)
	cs.PrependReactor("create", "serviceaccounts/token", reactor)
	ctx := context.Background()
	tenantPath := tenantPathFor(delegatedTestOrg, delegatedTestWS)

	legacy := legacyV1Account(t, tenantPath, "uid-legacy")
	if _, err := cs.CoreV1().ServiceAccounts(Namespace).Create(ctx, legacy, metav1.CreateOptions{}); err != nil {
		t.Fatalf("seed legacy account: %v", err)
	}
	m.now = func() time.Time { return time.Now() }
	if _, _, err := m.IssueDelegatedUserToken(ctx, delegatedTestOrg, delegatedTestWS, delegatedTestUser, PlatformProvider(delegatedTestProvider)); err != nil {
		t.Fatalf("issue over legacy account: %v", err)
	}
	got, err := cs.CoreV1().ServiceAccounts(Namespace).Get(ctx, legacy.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get account: %v", err)
	}
	if got.UID != "uid-legacy" {
		t.Fatalf("account UID changed to %q: legacy tokens would die", got.UID)
	}
	if got.Annotations[AnnotationDelegatedProofVersion] != delegatedProofVersionCurrent {
		t.Fatalf("account not upgraded: %v", got.Annotations)
	}
	id, err := DelegatedIdentityFromServiceAccount(ctx, testProofKeySource, got)
	if err != nil || id.Legacy {
		t.Fatalf("upgraded account identity = %+v, %v", id, err)
	}
}
