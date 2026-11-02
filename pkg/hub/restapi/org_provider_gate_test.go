// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");

package restapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	tenancyv1alpha1 "github.com/railgrid/railgrid/apis/tenancy/v1alpha1"
	"github.com/railgrid/railgrid/pkg/hub/kcp"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestOrgProviderEnrollmentGatePreservesListAndBlocksRegistration(t *testing.T) {
	mgr, ops, _ := newTestManager(t, &tenancyv1alpha1.Organization{ObjectMeta: metav1.ObjectMeta{Name: "org-a"}})
	if err := ops.EnsureOrgMembership(t.Context(), "org-a", "alice", "admin"); err != nil {
		t.Fatal(err)
	}
	orgOps := &fakeOrgProviderOps{workspaces: []kcp.OrgProviderWorkspace{{Name: "vault", Cluster: "vault-cluster"}}}
	creds := &fakeCredMinter{}
	checks := 0
	mgr.WithOrgProviders(orgOps, creds).WithOrgProviderAvailability(func(context.Context) error {
		checks++
		return fmt.Errorf("private quota diagnostic")
	})
	server := newTestServer(t, mgr, adminTC("alice", "org-a", ""))
	defer server.Close()

	response, err := http.Get(server.URL + "/api/orgs/org-a/providers")
	if err != nil {
		t.Fatal(err)
	}
	var listed ListOrgProvidersResponse
	if err := json.NewDecoder(response.Body).Decode(&listed); err != nil {
		_ = response.Body.Close()
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK || len(listed.Items) != 1 || listed.Items[0].Name != "vault" {
		t.Fatalf("list status=%d items=%+v", response.StatusCode, listed.Items)
	}
	if listed.Enrollment == nil || listed.Enrollment.Available || listed.Enrollment.Reason != orgProviderEnrollmentUnavailableReason {
		t.Fatalf("unexpected enrollment status: %+v", listed.Enrollment)
	}
	if strings.Contains(listed.Enrollment.Reason, "private") {
		t.Fatalf("read status leaked internal detail: %q", listed.Enrollment.Reason)
	}

	status, _ := postJSON(t, server.URL+"/api/orgs/org-a/providers", RegisterOrgProviderRequest{Name: "new-provider"})
	if status != http.StatusServiceUnavailable {
		t.Fatalf("registration status=%d want %d", status, http.StatusServiceUnavailable)
	}
	if checks != 2 || len(orgOps.registered) != 0 || creds.minted != 0 {
		t.Fatalf("gate checks=%d registration=%v minted=%d", checks, orgOps.registered, creds.minted)
	}
}
