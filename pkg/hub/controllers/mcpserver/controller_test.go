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

package mcpserver

import (
	"context"
	"slices"
	"strings"
	"testing"

	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	kcpfake "github.com/kcp-dev/sdk/client/clientset/versioned/fake"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	kubefake "k8s.io/client-go/kubernetes/fake"
	clienttesting "k8s.io/client-go/testing"

	farosv1alpha1 "github.com/faroshq/faros/apis/faros/v1alpha1"
	providersv1alpha1 "github.com/faroshq/faros/apis/providers/v1alpha1"
)

func newServer(name string, readOnly bool) *farosv1alpha1.MCPServer {
	return &farosv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: name, UID: types.UID("uid-" + name)},
		Spec:       farosv1alpha1.MCPServerSpec{ReadOnly: readOnly},
	}
}

func newBinding(name string, bound ...apisv1alpha2.BoundAPIResource) *apisv1alpha2.APIBinding {
	return &apisv1alpha2.APIBinding{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Status:     apisv1alpha2.APIBindingStatus{BoundResources: bound},
	}
}

func bound(group, resource string) apisv1alpha2.BoundAPIResource {
	return apisv1alpha2.BoundAPIResource{Group: group, Resource: resource}
}

// populatedTokenSecret is the token Secret as kcp's token controller leaves
// it, so ensureMCPIdentity's poll returns immediately.
func populatedTokenSecret(srvName string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: srvName + "-mcp-token", Namespace: mcpIdentityNamespace},
		Type:       corev1.SecretTypeServiceAccountToken,
		Data:       map[string][]byte{corev1.ServiceAccountTokenKey: []byte("tok")},
	}
}

func findRule(t *testing.T, rules []rbacv1.PolicyRule, group string, resource string) *rbacv1.PolicyRule {
	t.Helper()
	for i := range rules {
		if slices.Contains(rules[i].APIGroups, group) && slices.Contains(rules[i].Resources, resource) {
			return &rules[i]
		}
	}
	return nil
}

func assertNoWildcards(t *testing.T, rules []rbacv1.PolicyRule) {
	t.Helper()
	for _, r := range rules {
		if slices.Contains(r.APIGroups, "*") || slices.Contains(r.Resources, "*") || slices.Contains(r.Verbs, "*") || len(r.NonResourceURLs) > 0 {
			t.Fatalf("rule grants a wildcard or non-resource URL: %+v", r)
		}
	}
}

func TestBuildRules_MatchesBoundResources(t *testing.T) {
	rules := buildRules([]apisv1alpha2.BoundAPIResource{
		bound("code.faros.sh", "repositories"),
		bound("code.faros.sh", "connections"),
		bound("edges.faros.sh", "kubernetesclusters"),
		bound("infrastructure.faros.sh", "templates"),
		bound("infrastructure.faros.sh", "instances"),
	}, []ActionGrant{
		{Group: "databricks.faros.sh", Resource: "tables", Name: "query_table", ReadOnly: true}, // not bound: ignored
		{Group: "infrastructure.faros.sh", Resource: "instances", Name: "restart"},
	}, false)
	assertNoWildcards(t, rules)

	code := findRule(t, rules, "code.faros.sh", "repositories")
	if code == nil || !slices.Equal(code.Resources, []string{"connections", "repositories"}) {
		t.Fatalf("code rule = %+v, want sorted connections+repositories", code)
	}
	wantVerbs := []string{"get", "list", "watch", "create", "update", "patch", "delete"}
	if !slices.Equal(code.Verbs, wantVerbs) {
		t.Fatalf("code verbs = %v, want %v", code.Verbs, wantVerbs)
	}

	if r := findRule(t, rules, "edges.faros.sh", "kubernetesclusters"); r == nil {
		t.Fatal("missing edges rule")
	}
	var proxy bool
	for _, r := range rules {
		if slices.Contains(r.APIGroups, "edges.faros.sh") && slices.Equal(r.Verbs, []string{"proxy"}) {
			proxy = true
		}
	}
	if !proxy {
		t.Fatalf("missing proxy verb on edges resources: %+v", rules)
	}

	exec := findRule(t, rules, "infrastructure.faros.sh", "instances/exec")
	if exec == nil || !slices.Equal(exec.Verbs, []string{"create"}) {
		t.Fatalf("exec rule = %+v, want create", exec)
	}
	action := findRule(t, rules, "infrastructure.faros.sh", "instances/restart")
	if action == nil || !slices.Equal(action.Verbs, []string{"create"}) {
		t.Fatalf("action rule = %+v, want create", action)
	}
	if r := findRule(t, rules, "databricks.faros.sh", "tables/query_table"); r != nil {
		t.Fatalf("action for an unbound resource must not be granted: %+v", r)
	}

	lc := findRule(t, rules, "core.kcp.io", "logicalclusters")
	if lc == nil || !slices.Equal(lc.Verbs, []string{"get", "list", "watch"}) {
		t.Fatalf("logicalclusters rule = %+v, want read-only", lc)
	}
	for _, forbidden := range []string{"secrets", "serviceaccounts", "clusterroles", "clusterrolebindings", "apibindings"} {
		for _, r := range rules {
			if slices.Contains(r.Resources, forbidden) {
				t.Fatalf("rule must not grant %s: %+v", forbidden, r)
			}
		}
	}
}

func TestBuildRules_ReadOnlyStripsWriteVerbs(t *testing.T) {
	rules := buildRules([]apisv1alpha2.BoundAPIResource{
		bound("code.faros.sh", "repositories"),
		bound("edges.faros.sh", "kubernetesclusters"),
		bound("infrastructure.faros.sh", "instances"),
	}, []ActionGrant{
		{Group: "infrastructure.faros.sh", Resource: "instances", Name: "describe", ReadOnly: true},
		{Group: "infrastructure.faros.sh", Resource: "instances", Name: "restart"},
	}, true)
	assertNoWildcards(t, rules)

	for _, r := range rules {
		for _, v := range writeVerbs {
			if slices.Contains(r.Verbs, v) && !slices.Equal(r.Verbs, []string{"create"}) {
				t.Fatalf("readOnly rule carries write verb %q: %+v", v, r)
			}
		}
	}
	code := findRule(t, rules, "code.faros.sh", "repositories")
	if code == nil || !slices.Equal(code.Verbs, []string{"get", "list", "watch"}) {
		t.Fatalf("code verbs = %+v, want read-only", code)
	}
	if r := findRule(t, rules, "infrastructure.faros.sh", "instances/exec"); r != nil {
		t.Fatalf("readOnly must not grant exec: %+v", r)
	}
	if r := findRule(t, rules, "infrastructure.faros.sh", "instances/restart"); r != nil {
		t.Fatalf("readOnly must not grant a mutating action: %+v", r)
	}
	if r := findRule(t, rules, "infrastructure.faros.sh", "instances/describe"); r == nil {
		t.Fatal("readOnly should keep read-only actions")
	}
	// Only create rules allowed in readOnly are the SSAR plumbing and
	// read-only actions.
	for _, r := range rules {
		if slices.Equal(r.Verbs, []string{"create"}) {
			for _, res := range r.Resources {
				if res != "selfsubjectaccessreviews" && res != "instances/describe" {
					t.Fatalf("unexpected create grant in readOnly: %+v", r)
				}
			}
		}
	}
}

func TestBuildRules_EmptyBindingsStillYieldsRole(t *testing.T) {
	rules := buildRules(nil, nil, false)
	assertNoWildcards(t, rules)
	if findRule(t, rules, "core.kcp.io", "logicalclusters") == nil {
		t.Fatalf("baseline rules missing: %+v", rules)
	}
	for _, r := range rules {
		for _, g := range r.APIGroups {
			if g != "core.kcp.io" && g != "authorization.k8s.io" {
				t.Fatalf("no provider rule expected without bindings: %+v", r)
			}
		}
	}
}

func TestActionGrantsFromSpec(t *testing.T) {
	got := actionGrantsFromSpec([]providersv1alpha1.ProviderActionSpec{
		{ID: "query_table/v1", ReadOnly: true, BoundResource: providersv1alpha1.ProviderActionBoundResource{APIVersion: "databricks.faros.sh/v1alpha1", Resource: "tables"}},
		{ID: "bad", BoundResource: providersv1alpha1.ProviderActionBoundResource{APIVersion: "x/v1"}}, // no resource
	})
	want := []ActionGrant{{Group: "databricks.faros.sh", Resource: "tables", Name: "query_table", ReadOnly: true}}
	if !slices.Equal(got, want) {
		t.Fatalf("grants = %+v, want %+v", got, want)
	}
}

func TestEnsureMCPIdentity_NeverBindsClusterAdmin(t *testing.T) {
	ctx := context.Background()
	srv := newServer("default", false)
	kube := kubefake.NewSimpleClientset(populatedTokenSecret(srv.Name))
	kcp := kcpfake.NewSimpleClientset(newBinding("code", bound("code.faros.sh", "repositories")))

	r := &Reconciler{actionGrants: func(context.Context) ([]ActionGrant, error) { return nil, nil }}
	rules, err := r.desiredRules(ctx, kcp, srv)
	if err != nil {
		t.Fatalf("desiredRules: %v", err)
	}
	ref, token, ready, err := ensureMCPIdentity(ctx, kube, srv, rules)
	if err != nil {
		t.Fatalf("ensureMCPIdentity: %v", err)
	}
	if !ready || token != "tok" || ref == nil || ref.Name != "default-mcp-token" {
		t.Fatalf("ref=%v token=%q ready=%v", ref, token, ready)
	}

	crb, err := kube.RbacV1().ClusterRoleBindings().Get(ctx, "default-mcp", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get binding: %v", err)
	}
	if crb.RoleRef.Name == "cluster-admin" {
		t.Fatal("binding references cluster-admin")
	}
	if crb.RoleRef.Name != "faros:mcpserver:default" || crb.RoleRef.Kind != "ClusterRole" {
		t.Fatalf("roleRef = %+v", crb.RoleRef)
	}
	if len(crb.OwnerReferences) != 1 || crb.OwnerReferences[0].UID != srv.UID {
		t.Fatalf("binding not owned by the MCPServer: %+v", crb.OwnerReferences)
	}
	role, err := kube.RbacV1().ClusterRoles().Get(ctx, "faros:mcpserver:default", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get role: %v", err)
	}
	if len(role.OwnerReferences) != 1 || role.OwnerReferences[0].UID != srv.UID {
		t.Fatalf("role not owned by the MCPServer: %+v", role.OwnerReferences)
	}
	assertNoWildcards(t, role.Rules)
	if findRule(t, role.Rules, "code.faros.sh", "repositories") == nil {
		t.Fatalf("role rules = %+v, want repositories", role.Rules)
	}
}

func TestEnsureMCPRBAC_ReplacesClusterAdminBinding(t *testing.T) {
	ctx := context.Background()
	srv := newServer("default", false)
	owner := metav1.OwnerReference{Kind: "MCPServer", Name: srv.Name, UID: srv.UID}
	legacy := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "default-mcp"},
		Subjects:   []rbacv1.Subject{{Kind: "ServiceAccount", Name: "default-mcp", Namespace: mcpIdentityNamespace}},
		RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: "cluster-admin"},
	}
	kube := kubefake.NewSimpleClientset(legacy)
	var deleted, created int
	kube.PrependReactor("delete", "clusterrolebindings", func(clienttesting.Action) (bool, runtime.Object, error) {
		deleted++
		return false, nil, nil
	})
	kube.PrependReactor("create", "clusterrolebindings", func(clienttesting.Action) (bool, runtime.Object, error) {
		created++
		return false, nil, nil
	})

	if err := ensureMCPRBAC(ctx, kube, srv, owner, "default-mcp", buildRules(nil, nil, false)); err != nil {
		t.Fatalf("ensureMCPRBAC: %v", err)
	}
	if deleted != 1 || created != 1 {
		t.Fatalf("deleted=%d created=%d, want 1/1", deleted, created)
	}
	crb, err := kube.RbacV1().ClusterRoleBindings().Get(ctx, "default-mcp", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get binding: %v", err)
	}
	if crb.RoleRef.Name != "faros:mcpserver:default" {
		t.Fatalf("roleRef = %+v, want generated role", crb.RoleRef)
	}
	if len(crb.OwnerReferences) != 1 || crb.OwnerReferences[0].UID != srv.UID {
		t.Fatalf("recreated binding not owned by the MCPServer: %+v", crb.OwnerReferences)
	}

	// A second pass with the correct roleRef is a no-op on the binding.
	if err := ensureMCPRBAC(ctx, kube, srv, owner, "default-mcp", buildRules(nil, nil, false)); err != nil {
		t.Fatalf("second ensureMCPRBAC: %v", err)
	}
	if deleted != 1 || created != 1 {
		t.Fatalf("second pass touched the binding: deleted=%d created=%d", deleted, created)
	}
}

func TestEnsureMCPRBAC_SecondReconcileAddsRulesForNewBinding(t *testing.T) {
	ctx := context.Background()
	srv := newServer("default", false)
	owner := metav1.OwnerReference{Kind: "MCPServer", Name: srv.Name, UID: srv.UID}
	var kube kubernetes.Interface = kubefake.NewSimpleClientset()
	kcp := kcpfake.NewSimpleClientset(newBinding("code", bound("code.faros.sh", "repositories")))
	r := &Reconciler{actionGrants: func(context.Context) ([]ActionGrant, error) { return nil, nil }}

	reconcileRBAC := func() *rbacv1.ClusterRole {
		t.Helper()
		rules, err := r.desiredRules(ctx, kcp, srv)
		if err != nil {
			t.Fatalf("desiredRules: %v", err)
		}
		if err := ensureMCPRBAC(ctx, kube, srv, owner, "default-mcp", rules); err != nil {
			t.Fatalf("ensureMCPRBAC: %v", err)
		}
		role, err := kube.RbacV1().ClusterRoles().Get(ctx, "faros:mcpserver:default", metav1.GetOptions{})
		if err != nil {
			t.Fatalf("get role: %v", err)
		}
		return role
	}

	role := reconcileRBAC()
	if findRule(t, role.Rules, "edges.faros.sh", "kubernetesclusters") != nil {
		t.Fatalf("edges granted before the binding exists: %+v", role.Rules)
	}

	// A provider is enabled: its APIBinding appears with bound resources.
	if _, err := kcp.ApisV1alpha2().APIBindings().Create(ctx, newBinding("edges", bound("edges.faros.sh", "kubernetesclusters")), metav1.CreateOptions{}); err != nil {
		t.Fatalf("create binding: %v", err)
	}
	role = reconcileRBAC()
	if findRule(t, role.Rules, "edges.faros.sh", "kubernetesclusters") == nil {
		t.Fatalf("edges not granted after the binding appeared: %+v", role.Rules)
	}
	if findRule(t, role.Rules, "code.faros.sh", "repositories") == nil {
		t.Fatalf("existing grant lost: %+v", role.Rules)
	}
	assertNoWildcards(t, role.Rules)
}

func TestListBoundResources(t *testing.T) {
	kcp := kcpfake.NewSimpleClientset(
		newBinding("a", bound("code.faros.sh", "repositories"), bound("code.faros.sh", "connections")),
		newBinding("pending"), // not yet bound
	)
	got, err := listBoundResources(context.Background(), kcp)
	if err != nil {
		t.Fatalf("listBoundResources: %v", err)
	}
	var names []string
	for _, b := range got {
		names = append(names, b.Group+"/"+b.Resource)
	}
	slices.Sort(names)
	if want := "code.faros.sh/connections,code.faros.sh/repositories"; strings.Join(names, ",") != want {
		t.Fatalf("bound = %v, want %s", names, want)
	}
}
