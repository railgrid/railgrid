/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package identity

import (
	"context"
	"testing"
	"time"

	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	tenancyv1alpha1 "github.com/railgrid/railgrid/apis/tenancy/v1alpha1"
	"github.com/railgrid/railgrid/pkg/hub/serviceaccounts"
)

const workloadTenantPath = "root:railgrid:tenants:org:workspace"

func workloadScope() serviceaccounts.WorkloadIdentityScope {
	return serviceaccounts.WorkloadIdentityScope{
		TenantPath: workloadTenantPath, Project: "shop", ProjectUID: "project-uid-1",
		Environment: "production", Instance: "shop-prod",
		ProviderResources: []serviceaccounts.ProviderResourceScope{{
			APIVersion: "databricks.railgrid.ai/v1alpha1", Kind: "Table",
			Resource: "tables", Name: "sales", Actions: []string{"query_table"},
		}},
	}
}

// The App Studio exchange contract: the account it gets is the same account
// the old minter produced, with the same name, the same identity annotations
// (the tenant resolver reads them back) and the same GET-plus-action rules.
// Only the bookkeeping around it is new.
func TestEnsureWorkloadKeepsTheAppStudioAccountContract(t *testing.T) {
	service, records, cs, owners := testService(t)
	owners.present["Project/shop"] = "project-uid-1"
	ctx := context.Background()

	token, err := service.EnsureWorkload(ctx, workloadTenantPath, workloadScope(), "runtime")
	if err != nil {
		t.Fatalf("EnsureWorkload: %v", err)
	}
	if want := serviceaccounts.WorkloadServiceAccountName(workloadScope()); token.ServiceAccount != want {
		t.Fatalf("account = %q, want the unchanged deterministic workload name %q", token.ServiceAccount, want)
	}
	if token.TokenType != "Bearer" || token.Token == "" || token.ExpiresAt.IsZero() {
		t.Fatalf("exchange response shape changed: %#v", token)
	}
	if ttl := time.Until(token.ExpiresAt); ttl > serviceaccounts.WorkloadIdentityTokenTTL+time.Minute {
		t.Fatalf("workload token lifetime %s exceeds the 10 minute policy", ttl)
	}

	account, err := cs.CoreV1().ServiceAccounts("default").Get(ctx, token.ServiceAccount, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get workload ServiceAccount: %v", err)
	}
	for key, want := range map[string]string{
		serviceaccounts.AnnotationWorkloadIdentityTenantPath:  workloadTenantPath,
		serviceaccounts.AnnotationWorkloadIdentityProject:     "shop",
		serviceaccounts.AnnotationWorkloadIdentityProjectUID:  "project-uid-1",
		serviceaccounts.AnnotationWorkloadIdentityEnvironment: "production",
		serviceaccounts.AnnotationWorkloadIdentityInstance:    "shop-prod",
		serviceaccounts.AnnotationWorkloadIdentityScope:       serviceaccounts.WorkloadIdentityScopeMarker(workloadScope()),
	} {
		if account.Annotations[key] != want {
			t.Fatalf("annotation %q = %q, want %q", key, account.Annotations[key], want)
		}
	}
	if account.Labels[serviceaccounts.LabelWorkloadIdentity] != "true" {
		t.Fatal("the workload label is what keeps this account out of the user-facing SA surface")
	}

	role, err := cs.RbacV1().ClusterRoles().Get(ctx, serviceaccounts.WorkloadIdentityRoleName(token.ServiceAccount), metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get workload ClusterRole: %v", err)
	}
	if len(role.Rules) != 3 {
		t.Fatalf("workload rules = %#v, want project get + table get + action create", role.Rules)
	}

	// What IS new: the identity is recorded, owned by the Project, and
	// therefore collectable. Nothing collected a workload identity before.
	record, ok := records.items[token.Name]
	if !ok {
		t.Fatal("the workload identity was not recorded")
	}
	if record.Spec.Attestation.Mode != tenancyv1alpha1.ScopedIdentityAttestationWorkload {
		t.Fatalf("attestation mode = %q", record.Spec.Attestation.Mode)
	}
	if record.Spec.Owner.Kind != "Project" || record.Spec.Owner.Name != "shop" || record.Spec.Owner.UID != "project-uid-1" {
		t.Fatalf("workload owner = %#v, want the Project", record.Spec.Owner)
	}
}

func TestWorkloadIdentityIsCollectedWithItsProject(t *testing.T) {
	service, records, cs, owners := testService(t)
	owners.present["Project/shop"] = "project-uid-1"
	ctx := context.Background()
	token, err := service.EnsureWorkload(ctx, workloadTenantPath, workloadScope(), "runtime")
	if err != nil {
		t.Fatalf("EnsureWorkload: %v", err)
	}

	delete(owners.present, "Project/shop")
	result, err := NewReconciler(ReconcilerOptions{Service: service, Logger: logr.Discard()}).Sweep(ctx)
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if result.Collected != 1 {
		t.Fatalf("a deleted Project did not collect its workload identity: %+v", result)
	}
	if len(records.items) != 0 {
		t.Fatalf("record survived: %v", records.items)
	}
	assertIdentityGone(t, ctx, cs, token.ServiceAccount)
}

// One Project with two environments gets two accounts and two records, and
// releasing the Project releases both.
func TestOneProjectCanHoldSeveralWorkloadAccounts(t *testing.T) {
	service, records, _, owners := testService(t)
	owners.present["Project/shop"] = "project-uid-1"
	ctx := context.Background()

	production := workloadScope()
	development := workloadScope()
	development.Environment = "development"
	development.Instance = "shop-dev"

	first, err := service.EnsureWorkload(ctx, workloadTenantPath, production, "runtime")
	if err != nil {
		t.Fatalf("EnsureWorkload(production): %v", err)
	}
	second, err := service.EnsureWorkload(ctx, workloadTenantPath, development, "runtime")
	if err != nil {
		t.Fatalf("EnsureWorkload(development): %v", err)
	}
	if first.ServiceAccount == second.ServiceAccount || first.Name == second.Name {
		t.Fatal("two environments collapsed onto one identity")
	}
	if len(records.items) != 2 {
		t.Fatalf("records = %d, want 2", len(records.items))
	}

	owner := Owner{
		Provider: "app-studio", Kind: "Project", Group: "ai.railgrid.ai",
		Version: "v1alpha1", Resource: "projects", Name: "shop", UID: "project-uid-1",
	}
	if err := service.Release(ctx, workloadTenantPath, owner); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if len(records.items) != 0 {
		t.Fatalf("releasing the owner left %d records", len(records.items))
	}
}
