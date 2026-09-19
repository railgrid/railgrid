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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	tenancyv1alpha1 "github.com/railgrid/railgrid/apis/tenancy/v1alpha1"
	"github.com/railgrid/railgrid/pkg/hub/serviceaccounts"
)

func TestSweepCollectsIdentitiesWhoseOwnerIsGone(t *testing.T) {
	service, records, cs, owners := testService(t)
	ctx := context.Background()
	token, err := service.Ensure(ctx, agentRequest(), tenancyv1alpha1.ScopedIdentityAttestationProvider, "subject")
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	reconciler := NewReconciler(ReconcilerOptions{Service: service, Logger: logr.Discard()})

	// The owner is still there: nothing is collected, and a live token window
	// means nothing is rewritten either.
	if result := mustSweep(t, reconciler, ctx); result.Collected != 0 || result.SkippedWait != 1 {
		t.Fatalf("a live owner was disturbed: %+v", result)
	}

	// The Agent is deleted. This is the case NOTHING collected before §10:
	// the agents provider minted a non-expiring ServiceAccount and no code
	// path ever removed it.
	owners.present = map[string]string{}
	result := mustSweep(t, reconciler, ctx)
	if result.Collected != 1 {
		t.Fatalf("orphan not collected: %+v", result)
	}
	if len(records.items) != 0 {
		t.Fatalf("record survived collection: %v", records.items)
	}
	assertIdentityGone(t, ctx, cs, token.ServiceAccount)
}

func TestSweepLeavesRecordsAloneWhenTheWorkspaceCannotBeReached(t *testing.T) {
	service, records, cs, owners := testService(t)
	ctx := context.Background()
	token, err := service.Ensure(ctx, agentRequest(), tenancyv1alpha1.ScopedIdentityAttestationProvider, "subject")
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	// A probe error is not a deleted owner. Collecting here would revoke a
	// live agent's credential because kcp hiccuped.
	owners.err = errors.New("connection refused")
	result := mustSweep(t, NewReconciler(ReconcilerOptions{Service: service, Logger: logr.Discard()}), ctx)
	if result.Collected != 0 || result.Failed != 1 {
		t.Fatalf("a probe error was treated as a deletion: %+v", result)
	}
	if len(records.items) != 1 {
		t.Fatal("record was collected on a transient probe error")
	}
	if _, err := cs.CoreV1().ServiceAccounts("default").Get(ctx, token.ServiceAccount, metav1.GetOptions{}); err != nil {
		t.Fatalf("account was removed on a transient probe error: %v", err)
	}
}

func TestSweepRematerializesWhenTheTokenWindowHasLapsed(t *testing.T) {
	service, records, cs, _ := testService(t)
	ctx := context.Background()
	token, err := service.Ensure(ctx, agentRequest(), tenancyv1alpha1.ScopedIdentityAttestationProvider, "subject")
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	// The holder stopped refreshing and somebody widened the ClusterRole out
	// of band. The next sweep after the window lapses puts it back: the record
	// is the source of truth for what the identity may do.
	record := records.items[token.Name]
	lapsed := metav1.NewTime(time.Now().Add(-time.Minute))
	record.Status.ExpiresAt = &lapsed
	role, err := cs.RbacV1().ClusterRoles().Get(ctx, serviceaccounts.WorkloadIdentityRoleName(token.ServiceAccount), metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get ClusterRole: %v", err)
	}
	role.Rules[0].Verbs = []string{"get", "update", "delete"}
	if _, err := cs.RbacV1().ClusterRoles().Update(ctx, role, metav1.UpdateOptions{}); err != nil {
		t.Fatalf("widening ClusterRole: %v", err)
	}

	result := mustSweep(t, NewReconciler(ReconcilerOptions{Service: service, Logger: logr.Discard()}), ctx)
	if result.Rematerial != 1 {
		t.Fatalf("lapsed identity was not re-materialized: %+v", result)
	}
	role, err = cs.RbacV1().ClusterRoles().Get(ctx, serviceaccounts.WorkloadIdentityRoleName(token.ServiceAccount), metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get ClusterRole: %v", err)
	}
	if len(role.Rules[0].Verbs) != 1 || role.Rules[0].Verbs[0] != "get" {
		t.Fatalf("out-of-band widening survived reconciliation: %v", role.Rules[0].Verbs)
	}
}

func mustSweep(t *testing.T, reconciler *Reconciler, ctx context.Context) SweepResult {
	t.Helper()
	result, err := reconciler.Sweep(ctx)
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	return result
}
