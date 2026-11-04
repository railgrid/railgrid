// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package kcp

import (
	"context"
	"strconv"
	"sync"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/fake"
	clienttesting "k8s.io/client-go/testing"

	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	rbacvalidation "k8s.io/component-helpers/auth/rbac/validation"

	"github.com/railgrid/railgrid/pkg/hub/serviceaccounts"
)

func tenantBinding(group string) unstructured.Unstructured {
	return unstructured.Unstructured{Object: map[string]any{"apiVersion": "apis.kcp.io/v1alpha2", "kind": "APIBinding", "metadata": map[string]any{"name": "provider"}, "status": map[string]any{"phase": "Bound", "boundResources": []any{map[string]any{"group": group, "resource": "boards"}}}}}
}

func TestTenantAdminManagesBoundProviderDataWithoutInfrastructureAuthority(t *testing.T) {
	bindings := []unstructured.Unstructured{tenantBinding("planner.example"), tenantBinding("railgrid.ai"), tenantBinding("apis.kcp.io"), tenantBinding("dataplane.railgrid.ai"), tenantBinding("admin.railgrid.ai"), tenantBinding("tenants.railgrid.ai")}
	rules := tenantAdminRules(bindings)
	if allowed, _ := rbacvalidation.Covers(rules, []rbacv1.PolicyRule{{NonResourceURLs: []string{"/"}, Verbs: []string{"access"}}}); !allowed {
		t.Fatal("human tenant administrator cannot pass kcp workspace content gate")
	}
	for _, tc := range []struct {
		group, resource, verb string
		allowed               bool
	}{
		{"planner.example", "boards", "create", true}, {"planner.example", "boards/run", "create", true}, {"planner.example", "boards/status", "update", true},
		{"railgrid.ai", "mcpservers", "create", true}, {"unbound.example", "objects", "create", false},
		{"", "namespaces", "create", true}, {"", "secrets", "get", true}, {"", "serviceaccounts/token", "create", true},
		{"apis.kcp.io", "apiexports", "create", false}, {"apis.kcp.io", "apiresourceschemas", "create", false}, {"apis.kcp.io", "apibindings", "create", false},
		{"apiextensions.k8s.io", "customresourcedefinitions", "create", false}, {"admissionregistration.k8s.io", "mutatingwebhookconfigurations", "create", false},
		{"admissionregistration.k8s.io", "validatingadmissionpolicies", "delete", false}, {"apiregistration.k8s.io", "apiservices", "create", false},
		{"dataplane.railgrid.ai", "dataplaneendpointslices/status", "update", false}, {"admin.railgrid.ai", "providers", "create", false},
		{"tenants.railgrid.ai", "memberships", "create", false}, {"", "resourcequotas", "delete", false},
		{"core.kcp.io", "logicalclusters", "update", false}, {"", "users", "impersonate", false},
		{"rbac.authorization.k8s.io", "roles", "create", true}, {"rbac.authorization.k8s.io", "clusterrolebindings", "create", true},
		{"rbac.authorization.k8s.io", "clusterroles", "escalate", false}, {"rbac.authorization.k8s.io", "clusterroles", "bind", false},
	} {
		allowed, _ := rbacvalidation.Covers(rules, []rbacv1.PolicyRule{{APIGroups: []string{tc.group}, Resources: []string{tc.resource}, Verbs: []string{tc.verb}}})
		if allowed != tc.allowed {
			t.Errorf("%s %s/%s allowed=%v want=%v", tc.verb, tc.group, tc.resource, allowed, tc.allowed)
		}
	}
	// Kubernetes RBAC's escalation check uses Covers against the caller's rules.
	// Having create/update on RBAC objects cannot delegate cluster-admin.
	if covered, _ := rbacvalidation.Covers(rules, []rbacv1.PolicyRule{{APIGroups: []string{"*"}, Resources: []string{"*"}, Verbs: []string{"*"}}}); covered {
		t.Fatal("tenant admin can escalate to cluster-admin")
	}
}

func TestTenantAdminRefreshMigratesManagedBindingsAndRevokesRemovedGroups(t *testing.T) {
	ctx := context.Background()
	client := newCRBClient(t)
	binding := tenantBinding("planner.example")
	if _, err := client.Resource(apiBindingGVR).Create(ctx, &binding, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	managed := userAdminCRB("alice")
	_ = unstructured.SetNestedField(managed.Object, "cluster-admin", "roleRef", "name")
	if _, err := client.Resource(clusterRoleBindingGVR).Create(ctx, managed, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	unmanaged := managed.DeepCopy()
	unmanaged.SetName("existing-user-grant")
	unmanaged.SetAnnotations(nil)
	if _, err := client.Resource(clusterRoleBindingGVR).Create(ctx, unmanaged, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := ensureTenantAdminRole(ctx, client); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{managed.GetName(): TenantAdminRoleName, unmanaged.GetName(): "cluster-admin"} {
		actual, err := client.Resource(clusterRoleBindingGVR).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		role, _, _ := unstructured.NestedString(actual.Object, "roleRef", "name")
		if role != want {
			t.Errorf("binding %s role=%s want=%s", name, role, want)
		}
	}
	readRules := func() []rbacv1.PolicyRule {
		object, err := client.Resource(tenantAdminRoleGVR).Get(ctx, TenantAdminRoleName, metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		var role rbacv1.ClusterRole
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(object.Object, &role); err != nil {
			t.Fatal(err)
		}
		return role.Rules
	}
	wanted := []rbacv1.PolicyRule{{APIGroups: []string{"planner.example"}, Resources: []string{"boards/run"}, Verbs: []string{"create"}}}
	if allowed, _ := rbacvalidation.Covers(readRules(), wanted); !allowed {
		t.Fatal("bound provider verb unavailable")
	}
	if err := client.Resource(apiBindingGVR).Delete(ctx, binding.GetName(), metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := ensureTenantAdminRole(ctx, client); err != nil {
		t.Fatal(err)
	}
	if allowed, _ := rbacvalidation.Covers(readRules(), wanted); allowed {
		t.Fatal("removed provider retains tenant-admin grant")
	}
}

func TestTenantAdminMigrationCoversManagedCredentialsAndLeavesScopedIdentities(t *testing.T) {
	ctx := context.Background()
	client := newCRBClient(t)
	cases := []struct{ name, sa, label, role, want string }{
		{"railgrid-sa-human-uuid", "human-uuid", serviceaccounts.LabelRailgridSA, "cluster-admin", TenantAdminRoleName},
		{serviceaccounts.DelegatedUserBindingName("railgrid-du-delegated"), "railgrid-du-delegated", serviceaccounts.LabelWorkloadIdentity, "cluster-admin", TenantAdminRoleName},
		{"custom-grant", "human-uuid", serviceaccounts.LabelRailgridSA, "cluster-admin", "cluster-admin"},
		{"narrow-workload", "railgrid-wi-scoped", serviceaccounts.LabelWorkloadIdentity, "scoped-reader", "scoped-reader"},
	}
	for _, tc := range cases {
		binding := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "rbac.authorization.k8s.io/v1", "kind": "ClusterRoleBinding", "metadata": map[string]any{"name": tc.name, "labels": map[string]any{tc.label: "true"}}, "roleRef": map[string]any{"apiGroup": "rbac.authorization.k8s.io", "kind": "ClusterRole", "name": tc.role}, "subjects": []any{map[string]any{"kind": "ServiceAccount", "name": tc.sa, "namespace": serviceaccounts.Namespace}}}}
		if _, err := client.Resource(clusterRoleBindingGVR).Create(ctx, binding, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	if err := ensureTenantAdminRole(ctx, client); err != nil {
		t.Fatal(err)
	}
	for _, tc := range cases {
		binding, err := client.Resource(clusterRoleBindingGVR).Get(ctx, tc.name, metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		actual, _, _ := unstructured.NestedString(binding.Object, "roleRef", "name")
		if actual != tc.want {
			t.Errorf("%s role=%s want=%s", tc.name, actual, tc.want)
		}
	}
}

func TestTerminatingProviderRetainsFinalizerCleanup(t *testing.T) {
	b := tenantBinding("notes.example.com")
	wants := []rbacv1.PolicyRule{{APIGroups: []string{"notes.example.com"}, Resources: []string{"boards"}, Verbs: []string{"get", "patch"}}}
	if ok, _ := rbacvalidation.Covers(tenantAdminRules([]unstructured.Unstructured{b}), wants); !ok {
		t.Fatal("control: bound provider not accessible")
	}
	now := metav1.Now()
	b.SetDeletionTimestamp(&now)
	if ok, _ := rbacvalidation.Covers(tenantAdminRules([]unstructured.Unstructured{b}), wants); !ok {
		t.Fatal("terminating APIBinding retains its APIs but tenant administrator cannot inspect or patch a stuck finalizer")
	}
}

type bindingListBlocker struct {
	dynamic.Interface
	seen, resume chan struct{}
	once         *sync.Once
}
type bindingResourceBlocker struct {
	dynamic.NamespaceableResourceInterface
	seen, resume chan struct{}
	once         *sync.Once
}

func (c bindingListBlocker) Resource(gvr schema.GroupVersionResource) dynamic.NamespaceableResourceInterface {
	r := c.Interface.Resource(gvr)
	if gvr == apiBindingGVR {
		return bindingResourceBlocker{r, c.seen, c.resume, c.once}
	}
	return r
}
func (r bindingResourceBlocker) List(ctx context.Context, opts metav1.ListOptions) (*unstructured.UnstructuredList, error) {
	list, err := r.NamespaceableResourceInterface.List(ctx, opts)
	r.once.Do(func() {
		close(r.seen)
		select {
		case <-r.resume:
		case <-ctx.Done():
		}
	})
	return list, err
}
func TestTenantRoleRefreshDoesNotOverwriteNewerBinding(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	cl := newCRBClient(t)
	installRoleVersionChecks(t, cl)
	if err := ensureTenantAdminRole(ctx, cl); err != nil {
		t.Fatal(err)
	}
	a := tenantBinding("a.example")
	a.SetName("a")
	if _, err := cl.Resource(apiBindingGVR).Create(ctx, &a, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	seen, resume := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() { done <- ensureTenantAdminRole(ctx, bindingListBlocker{cl, seen, resume, &sync.Once{}}) }()
	select {
	case <-seen:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	b := tenantBinding("b.example")
	b.SetName("b")
	if _, err := cl.Resource(apiBindingGVR).Create(ctx, &b, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := ensureTenantAdminRole(ctx, cl); err != nil {
		t.Fatal(err)
	}
	close(resume)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	obj, err := cl.Resource(tenantAdminRoleGVR).Get(ctx, TenantAdminRoleName, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var role rbacv1.ClusterRole
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(obj.Object, &role); err != nil {
		t.Fatal(err)
	}
	if ok, _ := rbacvalidation.Covers(role.Rules, []rbacv1.PolicyRule{{APIGroups: []string{"b.example"}, Resources: []string{"boards"}, Verbs: []string{"get"}}}); !ok {
		t.Fatal("stale login refresh overwrote a completed binding event refresh and revoked the new provider group")
	}
}

// The dynamic fake has no resource-version enforcement. Model the API server's
// optimistic update semantics so the stale writer has to retry its inputs.
func installRoleVersionChecks(t *testing.T, client dynamic.Interface) {
	t.Helper()
	cl := client.(*fake.FakeDynamicClient)
	version := 0
	cl.PrependReactor("create", "clusterroles", func(action clienttesting.Action) (bool, runtime.Object, error) {
		obj := action.(clienttesting.CreateAction).GetObject().(*unstructured.Unstructured).DeepCopy()
		version++
		obj.SetResourceVersion(strconv.Itoa(version))
		err := cl.Tracker().Create(tenantAdminRoleGVR, obj, "")
		return true, obj, err
	})
	cl.PrependReactor("update", "clusterroles", func(action clienttesting.Action) (bool, runtime.Object, error) {
		obj := action.(clienttesting.UpdateAction).GetObject().(*unstructured.Unstructured).DeepCopy()
		current, err := cl.Tracker().Get(tenantAdminRoleGVR, "", obj.GetName())
		if err != nil {
			return true, nil, err
		}
		if obj.GetResourceVersion() != current.(*unstructured.Unstructured).GetResourceVersion() {
			return true, nil, apierrors.NewConflict(tenantAdminRoleGVR.GroupResource(), obj.GetName(), nil)
		}
		version++
		obj.SetResourceVersion(strconv.Itoa(version))
		err = cl.Tracker().Update(tenantAdminRoleGVR, obj, "")
		return true, obj, err
	})
}
