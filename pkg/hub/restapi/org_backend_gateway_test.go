/*
Copyright 2026 The Railgrid Authors.
Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at http://www.apache.org/licenses/LICENSE-2.0
*/

package restapi

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/railgrid/railgrid/pkg/hub/serviceaccounts"
)

func TestOrgBackendApprovalRevocationOrReplacementCannotReviveOldTokens(t *testing.T) {
	f := newOrgBackendFixture(t)
	token := f.issue(t, orgBackendTestOrg, orgBackendTestWorkspace)
	calls := 0
	f.bindDispatcher(func(w http.ResponseWriter, _ *http.Request, _ serviceaccounts.OrgBackendClaims) {
		calls++
		w.WriteHeader(http.StatusNoContent)
	})
	path := "/clusters/" + orgBackendTestCluster + "/apis/vault.example/v1/secretsstores/one"
	if response := f.request(token, path); response.Code != http.StatusNoContent {
		t.Fatal(response.Code, response.Body.String())
	}
	store := f.mgr.orgTrustStore.(*memoryOrgTrustStore)
	if err := store.Delete(context.Background(), orgTrustKey(orgBackendTestOrg, "vault")); err != nil {
		t.Fatal(err)
	}
	if response := f.request(token, path); response.Code != http.StatusUnauthorized {
		t.Fatal(response.Code)
	}
	putOrgTrust(t, store, orgBackendTestOrg, "vault", "provider-cluster-vault", "replacement-revision")
	if response := f.request(token, path); response.Code != http.StatusUnauthorized || calls != 1 {
		t.Fatalf("old token revived: status=%d calls=%d", response.Code, calls)
	}
	fresh := f.issue(t, orgBackendTestOrg, orgBackendTestWorkspace)
	f.bindDispatcher(func(w http.ResponseWriter, _ *http.Request, _ serviceaccounts.OrgBackendClaims) {
		calls++
		w.WriteHeader(http.StatusNoContent)
	})
	if response := f.request(fresh, path); response.Code != http.StatusNoContent || calls != 2 {
		t.Fatal(response.Code, calls)
	}
}

func TestOrgBackendLegacyUnapprovedAndExpiredCredentialsFailClosed(t *testing.T) {
	f := newOrgBackendFixture(t)
	claims, err := f.mgr.currentOrgBackendClaims(context.Background(), orgBackendTestOrg, orgBackendTestWorkspace, orgBackendTestUser, "vault")
	if err != nil {
		t.Fatal(err)
	}
	f.bindDispatcher(func(http.ResponseWriter, *http.Request, serviceaccounts.OrgBackendClaims) {
		t.Fatal("retired credential reached native dispatcher")
	})
	legacy, _, err := serviceaccounts.MintOrgBackendToken(context.Background(), f.key, claims, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if response := f.request(legacy, "/clusters/"+orgBackendTestCluster+"/api/v1/configmaps"); response.Code != http.StatusUnauthorized {
		t.Fatal(response.Code)
	}
	claims.ExpiresAt = time.Now().Add(-time.Second).Unix()
	if _, approved, err := f.mgr.CurrentOrgTrust(context.Background(), claims); approved || err == nil {
		t.Fatal("expired authority accepted")
	}
	f.mgr.orgTrustStore = nil
	if _, _, err := f.mgr.IssueOrgBackendToken(context.Background(), orgBackendTestOrg, orgBackendTestWorkspace, serviceaccounts.Identity{User: orgBackendTestUser}, serviceaccounts.DelegatedProvider{Name: "vault", OrgUUID: orgBackendTestOrg}); err == nil {
		t.Fatal("identity minted without org approval store")
	}
}
