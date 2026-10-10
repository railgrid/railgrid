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
	"errors"
	"testing"
	"time"

	"github.com/go-logr/logr"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	tenancyv1alpha1 "github.com/railgrid/railgrid/apis/tenancy/v1alpha1"
	"github.com/railgrid/railgrid/pkg/hub/serviceaccounts"
)

const workloadTenantPath = "root:railgrid:tenants:org:workspace"

func workloadScope() serviceaccounts.WorkloadIdentityScope {
	return serviceaccounts.WorkloadIdentityScope{
		TenantPath: workloadTenantPath, Project: "shop", ProjectUID: "project-uid-1",
		Environment: "production", Instance: "shop-prod", IntegrationActions: true,
		ProviderResources: []serviceaccounts.ProviderResourceScope{{
			APIVersion: "databricks.railgrid.ai/v1alpha1", Kind: "Table",
			Resource: "tables", Name: "sales", Actions: []string{"query_table"},
		}},
	}
}

type fakeWorkloadScopeResolver struct {
	scope serviceaccounts.WorkloadIdentityScope
	err   error
}

func (f fakeWorkloadScopeResolver) ResolveRecord(context.Context, *tenancyv1alpha1.ScopedIdentity) (serviceaccounts.WorkloadIdentityScope, error) {
	return f.scope, f.err
}

// The App Studio exchange contract: the account it gets is the same account
// the old minter produced, with the same name, the same identity annotations
// (the tenant resolver reads them back), and exact Project-gateway plus
// current provider-action rules.
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
	if len(role.Rules) != 4 {
		t.Fatalf("workload rules = %#v, want Project get/gateway + table get/action", role.Rules)
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

func TestSweepReconcilesLegacyWorkloadIdentityFromCurrentProjectGrants(t *testing.T) {
	service, records, cs, owners := testService(t)
	owners.present["Project/shop"] = "project-uid-1"
	ctx := context.Background()

	legacyScope := workloadScope()
	legacyScope.IntegrationActions = false
	legacy, err := service.EnsureWorkload(ctx, workloadTenantPath, legacyScope, "runtime")
	if err != nil {
		t.Fatalf("EnsureWorkload(legacy): %v", err)
	}
	legacyRoleName := serviceaccounts.WorkloadIdentityRoleName(legacy.ServiceAccount)
	role, err := cs.RbacV1().ClusterRoles().Get(ctx, legacyRoleName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get legacy ClusterRole: %v", err)
	}
	for _, rule := range role.Rules {
		if containsRule(rule, "ai.railgrid.ai", "projects/integration-actions", "shop") {
			t.Fatal("legacy identity unexpectedly already has the gateway grant")
		}
	}

	currentScope := workloadScope()
	result, err := NewReconciler(ReconcilerOptions{
		Service: service, WorkloadScopes: fakeWorkloadScopeResolver{scope: currentScope}, Logger: logr.Discard(),
	}).Sweep(ctx)
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if result.Rematerial != 1 || result.Failed != 0 {
		t.Fatalf("legacy workload identity was not reconciled: %+v", result)
	}
	role, err = cs.RbacV1().ClusterRoles().Get(ctx, legacyRoleName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get reconciled ClusterRole: %v", err)
	}
	if !hasRule(role.Rules, "ai.railgrid.ai", "projects/integration-actions", "shop", "create") {
		t.Fatalf("current Project integration gateway grant is absent: %#v", role.Rules)
	}
	if _, ok := records.items[legacy.Name]; !ok {
		t.Fatal("legacy workload record was lost during reconciliation")
	}
}

func TestSweepRemovesRevokedWorkloadActionAndUnusedGateway(t *testing.T) {
	service, _, cs, owners := testService(t)
	owners.present["Project/shop"] = "project-uid-1"
	ctx := context.Background()

	granted := workloadScope()
	granted.ProviderResources[0].Actions = []string{"query_table", "update_table"}
	token, err := service.EnsureWorkload(ctx, workloadTenantPath, granted, "runtime")
	if err != nil {
		t.Fatalf("EnsureWorkload: %v", err)
	}
	reconciler := func(scope serviceaccounts.WorkloadIdentityScope) *Reconciler {
		return NewReconciler(ReconcilerOptions{
			Service: service, WorkloadScopes: fakeWorkloadScopeResolver{scope: scope}, Logger: logr.Discard(),
		})
	}
	roleName := serviceaccounts.WorkloadIdentityRoleName(token.ServiceAccount)

	partiallyRevoked := workloadScope()
	result, err := reconciler(partiallyRevoked).Sweep(ctx)
	if err != nil {
		t.Fatalf("Sweep(partial revoke): %v", err)
	}
	if result.Rematerial != 1 {
		t.Fatalf("partially revoked action was not reconciled: %+v", result)
	}
	role, err := cs.RbacV1().ClusterRoles().Get(ctx, roleName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get narrowed ClusterRole: %v", err)
	}
	if !hasRule(role.Rules, "databricks.railgrid.ai", "tables/query_table", "sales", "create") || hasRule(role.Rules, "databricks.railgrid.ai", "tables/update_table", "sales", "create") {
		t.Fatalf("grant reconciliation did not retain only the active action: %#v", role.Rules)
	}
	if !hasRule(role.Rules, "ai.railgrid.ai", "projects/integration-actions", "shop", "create") {
		t.Fatalf("active integration gateway grant was removed with one action: %#v", role.Rules)
	}

	noIntegrations := serviceaccounts.WorkloadIdentityScope{
		TenantPath: workloadTenantPath, Project: "shop", ProjectUID: "project-uid-1",
		Environment: "production", Instance: "shop-prod",
		ProviderResources: []serviceaccounts.ProviderResourceScope{{
			APIVersion: "infrastructure.railgrid.ai/v1alpha1", Kind: "Application",
			Resource: "applications", Name: "shop-prod",
		}},
	}
	result, err = reconciler(noIntegrations).Sweep(ctx)
	if err != nil {
		t.Fatalf("Sweep(all revoked): %v", err)
	}
	if result.Rematerial != 1 {
		t.Fatalf("fully revoked integration scope was not reconciled: %+v", result)
	}
	role, err = cs.RbacV1().ClusterRoles().Get(ctx, roleName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get fully narrowed ClusterRole: %v", err)
	}
	if len(role.Rules) != 1 || role.Rules[0].Resources[0] != "applications" || role.Rules[0].ResourceNames[0] != "shop-prod" {
		t.Fatalf("fully revoked integrations left gateway or foreign-resource access: %#v", role.Rules)
	}
}

func TestSweepCollectsWorkloadIdentityWhenProjectEnvironmentOrRuntimeIsRemoved(t *testing.T) {
	service, records, cs, owners := testService(t)
	owners.present["Project/shop"] = "project-uid-1"
	ctx := context.Background()
	token, err := service.EnsureWorkload(ctx, workloadTenantPath, workloadScope(), "runtime")
	if err != nil {
		t.Fatalf("EnsureWorkload: %v", err)
	}

	resolver := fakeWorkloadScopeResolver{err: ErrWorkloadScopeRevoked}
	result, err := NewReconciler(ReconcilerOptions{
		Service: service, WorkloadScopes: resolver, Logger: logr.Discard(),
	}).Sweep(ctx)
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if result.Collected != 1 || result.Failed != 0 {
		t.Fatalf("revoked environment/runtime was not collected: %+v", result)
	}
	if _, ok := records.items[token.Name]; ok {
		t.Fatal("revoked workload identity record survived")
	}
	assertIdentityGone(t, ctx, cs, token.ServiceAccount)
}

func TestSweepPreservesWorkloadIdentityOnTransientProjectReadFailure(t *testing.T) {
	service, records, cs, owners := testService(t)
	owners.present["Project/shop"] = "project-uid-1"
	ctx := context.Background()
	token, err := service.EnsureWorkload(ctx, workloadTenantPath, workloadScope(), "runtime")
	if err != nil {
		t.Fatalf("EnsureWorkload: %v", err)
	}

	resolver := fakeWorkloadScopeResolver{err: errors.New("temporary Project API read failure")}
	result, err := NewReconciler(ReconcilerOptions{
		Service: service, WorkloadScopes: resolver, Logger: logr.Discard(),
	}).Sweep(ctx)
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if result.Failed != 1 || result.Collected != 0 {
		t.Fatalf("transient read failure should preserve and retry the identity: %+v", result)
	}
	if _, ok := records.items[token.Name]; !ok {
		t.Fatal("workload identity record was removed after a transient read failure")
	}
	if _, err := cs.CoreV1().ServiceAccounts("default").Get(ctx, token.ServiceAccount, metav1.GetOptions{}); err != nil {
		t.Fatalf("workload ServiceAccount was removed after a transient read failure: %v", err)
	}
	role, err := cs.RbacV1().ClusterRoles().Get(ctx, serviceaccounts.WorkloadIdentityRoleName(token.ServiceAccount), metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get preserved ClusterRole: %v", err)
	}
	if !hasRule(role.Rules, "databricks.railgrid.ai", "tables/query_table", "sales", "create") {
		t.Fatalf("transient read failure unexpectedly removed the last known permissions: %#v", role.Rules)
	}
}

func TestProjectRecreatedWithSameNameCollectsOldWorkloadIdentity(t *testing.T) {
	service, records, cs, owners := testService(t)
	ctx := context.Background()
	owners.present["Project/shop"] = "project-uid-old"
	oldScope := workloadScope()
	oldScope.ProjectUID = "project-uid-old"
	oldToken, err := service.EnsureWorkload(ctx, workloadTenantPath, oldScope, "runtime")
	if err != nil {
		t.Fatalf("EnsureWorkload(old Project): %v", err)
	}

	// The Project name remains "shop", but kcp assigns the recreated Project a
	// new UID. The old identity must be collected before the new owner is minted.
	owners.present["Project/shop"] = "project-uid-new"
	result, err := NewReconciler(ReconcilerOptions{Service: service, Logger: logr.Discard()}).Sweep(ctx)
	if err != nil {
		t.Fatalf("Sweep after Project recreation: %v", err)
	}
	if result.Collected != 1 || len(records.items) != 0 {
		t.Fatalf("old Project identity survived name reuse: result=%+v records=%d", result, len(records.items))
	}
	assertIdentityGone(t, ctx, cs, oldToken.ServiceAccount)

	newScope := oldScope
	newScope.ProjectUID = "project-uid-new"
	newToken, err := service.EnsureWorkload(ctx, workloadTenantPath, newScope, "runtime")
	if err != nil {
		t.Fatalf("EnsureWorkload(recreated Project): %v", err)
	}
	if newToken.ServiceAccount == oldToken.ServiceAccount || newToken.Name == oldToken.Name {
		t.Fatalf("recreated Project reused the old workload identity: old=%#v new=%#v", oldToken, newToken)
	}
}

func hasRule(rules []rbacv1.PolicyRule, group, resource, name, verb string) bool {
	for _, rule := range rules {
		if containsRule(rule, group, resource, name) {
			for _, gotVerb := range rule.Verbs {
				if gotVerb == verb || gotVerb == "*" {
					return true
				}
			}
		}
	}
	return false
}

func containsRule(rule rbacv1.PolicyRule, group, resource, name string) bool {
	groupMatches, resourceMatches, nameMatches := false, false, false
	for _, got := range rule.APIGroups {
		groupMatches = groupMatches || got == group
	}
	for _, got := range rule.Resources {
		resourceMatches = resourceMatches || got == resource
	}
	for _, got := range rule.ResourceNames {
		nameMatches = nameMatches || got == name
	}
	return groupMatches && resourceMatches && nameMatches
}
