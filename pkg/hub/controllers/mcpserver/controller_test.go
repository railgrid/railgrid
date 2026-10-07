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

	"github.com/railgrid/provider-sdk/dataplane"

	railgridv1alpha1 "github.com/railgrid/railgrid/apis/railgrid/v1alpha1"
)

func newServer(name string, readOnly bool) *railgridv1alpha1.MCPServer {
	return &railgridv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: name, UID: types.UID("uid-" + name)},
		Spec:       railgridv1alpha1.MCPServerSpec{ReadOnly: readOnly},
	}
}

func newBinding(name string, bound ...BoundResource) *apisv1alpha2.APIBinding {
	apiBinding := &apisv1alpha2.APIBinding{ObjectMeta: metav1.ObjectMeta{Name: name}}
	if len(bound) == 0 {
		return apiBinding
	}
	apiBinding.Spec.Reference.Export = &apisv1alpha2.ExportBindingReference{
		Path: bound[0].APIExportPath,
		Name: bound[0].APIExportName,
	}
	apiBinding.Status.Phase = apisv1alpha2.APIBindingPhaseBound
	apiBinding.Status.BoundResources = make([]apisv1alpha2.BoundAPIResource, 0, len(bound))
	for _, resource := range bound {
		apiBinding.Status.BoundResources = append(apiBinding.Status.BoundResources, resource.BoundAPIResource)
	}
	return apiBinding
}

func bound(group, resource string) BoundResource {
	path, name := testExportIdentity(group)
	return BoundResource{
		BoundAPIResource: apisv1alpha2.BoundAPIResource{Group: group, Resource: resource},
		APIExportPath:    path,
		APIExportName:    name,
	}
}

func testExportIdentity(group string) (string, string) {
	provider := "demo"
	switch group {
	case "edges.railgrid.ai":
		provider = "edges"
	case "agents.railgrid.ai":
		provider = "agents"
	case "infrastructure.railgrid.ai":
		provider = "infrastructure"
	case "code.railgrid.ai":
		provider = "code"
	}
	return "root:railgrid:providers:" + provider, provider + ".providers.railgrid.ai"
}

func testSubresourceGrant(group, resource, name string, readOnly bool) SubresourceGrant {
	path, exportName := testExportIdentity(group)
	return SubresourceGrant{
		Group: group, Resource: resource, Name: name, ReadOnly: readOnly,
		APIExportPath: path, APIExportName: exportName,
	}
}

func testDataPlaneGrants() []SubresourceGrant {
	var grants []SubresourceGrant
	for resource, names := range map[string][]string{
		"kubernetesclusters": {"k8s", "ssh", "mcp"},
		"linuxservers":       {"k8s", "ssh"},
		"services":           {"proxy", "mcp"},
	} {
		for _, name := range names {
			grant := testSubresourceGrant("edges.railgrid.ai", resource, name, false)
			grant.AllowPrivilegedWrite = true
			grants = append(grants, grant)
		}
	}
	for _, name := range []string{"test", "discover"} {
		grants = append(grants, testSubresourceGrant("agents.railgrid.ai", "modelcredentials", name, false))
	}
	grants = append(grants, testSubresourceGrant("infrastructure.railgrid.ai", "instances", "exec", false))
	return grants
}

func buildTestRules(bound []BoundResource, grants []SubresourceGrant, readOnly bool) []rbacv1.PolicyRule {
	allGrants := append(testDataPlaneGrants(), grants...)
	return buildRules(bound, allGrants, readOnly)
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
	rules := buildTestRules([]BoundResource{
		bound("code.railgrid.ai", "repositories"),
		bound("code.railgrid.ai", "connections"),
		bound("edges.railgrid.ai", "kubernetesclusters"),
		bound("infrastructure.railgrid.ai", "templates"),
		bound("infrastructure.railgrid.ai", "instances"),
	}, []SubresourceGrant{
		testSubresourceGrant("demo.railgrid.ai", "widgets", "inspect", true), // not bound: ignored
		testSubresourceGrant("infrastructure.railgrid.ai", "instances", "restart", false),
	}, false)
	assertNoWildcards(t, rules)

	code := findRule(t, rules, "code.railgrid.ai", "repositories")
	if code == nil || !slices.Equal(code.Resources, []string{"connections", "repositories"}) {
		t.Fatalf("code rule = %+v, want sorted connections+repositories", code)
	}
	wantVerbs := []string{"get", "list", "watch", "create", "update", "patch", "delete"}
	if !slices.Equal(code.Verbs, wantVerbs) {
		t.Fatalf("code verbs = %v, want %v", code.Verbs, wantVerbs)
	}

	if r := findRule(t, rules, "edges.railgrid.ai", "kubernetesclusters"); r == nil {
		t.Fatal("missing edges rule")
	}
	// The edges data plane is kcp custom subresources {resource}/{verb}; the
	// grant is the wildcard on the coordinate (kcp maps the HTTP method onto
	// the RBAC verb), never a bare verb on the object.
	k8s := findRule(t, rules, "edges.railgrid.ai", "kubernetesclusters/k8s")
	if k8s == nil || !slices.Equal(k8s.Verbs, dataplane.SubresourceVerbs) ||
		!slices.Equal(k8s.Resources, []string{"kubernetesclusters/k8s", "kubernetesclusters/mcp", "kubernetesclusters/ssh"}) {
		t.Fatalf("edges data-plane rule = %+v, want every kcp verb on kubernetesclusters/{k8s,ssh,mcp}", k8s)
	}
	for _, r := range rules {
		if slices.Contains(r.APIGroups, "edges.railgrid.ai") && slices.Contains(r.Verbs, "proxy") {
			t.Fatalf("retired wildcard proxy verb granted: %+v", r)
		}
	}

	exec := findRule(t, rules, "infrastructure.railgrid.ai", "instances/exec")
	if exec == nil || !slices.Equal(exec.Verbs, dataplane.SubresourceVerbs) {
		t.Fatalf("exec rule = %+v, want every kcp verb", exec)
	}
	action := findRule(t, rules, "infrastructure.railgrid.ai", "instances/restart")
	if action == nil || !slices.Equal(action.Verbs, dataplane.SubresourceVerbs) {
		t.Fatalf("action rule = %+v, want every kcp verb", action)
	}
	if r := findRule(t, rules, "demo.railgrid.ai", "widgets/inspect"); r != nil {
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

// The infrastructure data plane serves exec for instances only; every other
// resource bound from the same group (templates, and anything the APIExport
// grows later) must not pick up an /exec grant just by sharing the group.
func TestBuildRules_DataPlaneSubresourcesAreResourceScoped(t *testing.T) {
	rules := buildTestRules([]BoundResource{
		bound("infrastructure.railgrid.ai", "templates"),
		bound("infrastructure.railgrid.ai", "instances"),
		bound("infrastructure.railgrid.ai", "executions"),
	}, nil, false)
	assertNoWildcards(t, rules)

	if r := findRule(t, rules, "infrastructure.railgrid.ai", "instances/exec"); r == nil {
		t.Fatalf("instances/exec not granted: %+v", rules)
	}
	for _, res := range []string{"templates/exec", "executions/exec"} {
		if r := findRule(t, rules, "infrastructure.railgrid.ai", res); r != nil {
			t.Fatalf("%s must not be granted: %+v", res, r)
		}
	}

	// Each edge kind gets exactly the verbs its data plane serves, and a kind
	// the tunnel does not serve (macosservers) gets no data-plane grant.
	edges := buildTestRules([]BoundResource{
		bound("edges.railgrid.ai", "kubernetesclusters"),
		bound("edges.railgrid.ai", "linuxservers"),
		bound("edges.railgrid.ai", "macosservers"),
		bound("edges.railgrid.ai", "services"),
	}, nil, false)
	want := []string{
		"kubernetesclusters/k8s", "kubernetesclusters/mcp", "kubernetesclusters/ssh",
		"linuxservers/k8s", "linuxservers/ssh", "services/mcp", "services/proxy",
	}
	for _, key := range []string{"kubernetesclusters/k8s", "linuxservers/k8s", "services/proxy"} {
		r := findRule(t, edges, "edges.railgrid.ai", key)
		if r == nil || !slices.Equal(r.Verbs, dataplane.SubresourceVerbs) || !slices.Equal(r.Resources, want) {
			t.Fatalf("%s rule = %+v, want every kcp verb on %v", key, r, want)
		}
	}
	for _, r := range edges {
		for _, res := range r.Resources {
			if strings.HasPrefix(res, "macosservers/") {
				t.Fatalf("macosservers must get no data-plane grant: %+v", r)
			}
		}
	}
}

// The agents data plane's probe verbs are a grant on modelcredentials and
// nothing else. Agents used to carry them as agents/model-test and
// agents/model-discover, which meant an MCP token holding "may test a model
// credential" held it on the AGENT — the same object chat and run hang off.
// Now the credential is an object of its own and the grant says so, and no
// other agents.railgrid.ai resource picks up a subresource by sharing the
// group.
func TestBuildRules_AgentsGrantsOnlyModelCredentialProbes(t *testing.T) {
	rules := buildTestRules([]BoundResource{
		bound("agents.railgrid.ai", "agents"),
		bound("agents.railgrid.ai", "modelcredentials"),
		bound("agents.railgrid.ai", "runs"),
		bound("agents.railgrid.ai", "connections"),
	}, nil, false)
	assertNoWildcards(t, rules)

	probe := findRule(t, rules, "agents.railgrid.ai", "modelcredentials/test")
	if probe == nil || !slices.Equal(probe.Verbs, dataplane.SubresourceVerbs) ||
		!slices.Equal(probe.Resources, []string{"modelcredentials/discover", "modelcredentials/test"}) {
		t.Fatalf("agents data-plane rule = %+v, want every kcp verb on modelcredentials/{test,discover}", probe)
	}
	// The retired coordinates, and everything else the data plane serves that
	// an MCP token has no business invoking.
	for _, res := range []string{
		"agents/model-test", "agents/model-discover",
		"agents/chat", "agents/run", "agents/inbox-resolve",
		"connections/test", "runs/cancel",
	} {
		if r := findRule(t, rules, "agents.railgrid.ai", res); r != nil {
			t.Fatalf("%s must not be granted: %+v", res, r)
		}
	}

	// readOnly servers invoke nothing: a probe is still a call out to a third
	// party with the tenant's key.
	ro := buildTestRules([]BoundResource{
		bound("agents.railgrid.ai", "modelcredentials"),
	}, nil, true)
	if r := findRule(t, ro, "agents.railgrid.ai", "modelcredentials/test"); r != nil {
		t.Fatalf("readOnly server must not get a probe grant: %+v", r)
	}
}

// With the instance resource unbound the group's data-plane grant yields
// nothing at all, rather than falling back to whatever else is bound.
func TestBuildRules_ExecSkippedWhenTheInstanceResourceIsNotBound(t *testing.T) {
	rules := buildTestRules([]BoundResource{
		bound("infrastructure.railgrid.ai", "templates"),
	}, nil, false)
	for _, r := range rules {
		for _, res := range r.Resources {
			if strings.HasSuffix(res, "/exec") {
				t.Fatalf("exec granted without instances bound: %+v", r)
			}
		}
	}
}

func TestBuildRules_ReadOnlyStripsWriteVerbs(t *testing.T) {
	rules := buildTestRules([]BoundResource{
		bound("code.railgrid.ai", "repositories"),
		bound("edges.railgrid.ai", "kubernetesclusters"),
		bound("infrastructure.railgrid.ai", "instances"),
	}, []SubresourceGrant{
		testSubresourceGrant("infrastructure.railgrid.ai", "instances", "describe", true),
		testSubresourceGrant("infrastructure.railgrid.ai", "instances", "restart", false),
	}, true)
	assertNoWildcards(t, rules)

	for _, r := range rules {
		// A verb coordinate ({resource}/{verb}) is an invocation grant and
		// carries every kcp verb whatever the server's mode; only rules on
		// the objects themselves are stripped to reads.
		if slices.Equal(r.Verbs, dataplane.SubresourceVerbs) {
			for _, res := range r.Resources {
				if !strings.Contains(res, "/") {
					t.Fatalf("readOnly rule grants every verb on an object, not a coordinate: %+v", r)
				}
			}
			continue
		}
		for _, v := range writeVerbs {
			// A bare create is a review (SelfSubjectAccessReview), not a write
			// on a bound object.
			if slices.Contains(r.Verbs, v) && !slices.Equal(r.Verbs, []string{"create"}) {
				t.Fatalf("readOnly rule carries write verb %q: %+v", v, r)
			}
		}
	}
	code := findRule(t, rules, "code.railgrid.ai", "repositories")
	if code == nil || !slices.Equal(code.Verbs, []string{"get", "list", "watch"}) {
		t.Fatalf("code verbs = %+v, want read-only", code)
	}
	if r := findRule(t, rules, "infrastructure.railgrid.ai", "instances/exec"); r != nil {
		t.Fatalf("readOnly must not grant exec: %+v", r)
	}
	if r := findRule(t, rules, "infrastructure.railgrid.ai", "instances/restart"); r != nil {
		t.Fatalf("readOnly must not grant a mutating action: %+v", r)
	}
	if r := findRule(t, rules, "infrastructure.railgrid.ai", "instances/describe"); r == nil {
		t.Fatal("readOnly should keep read-only actions")
	}
	// The edges tunnel serves kubectl delete, exec and SSH shells under the
	// one "proxy" verb, so a readOnly server must not hold it on any edge.
	for _, r := range rules {
		if slices.Contains(r.Verbs, "proxy") {
			t.Fatalf("readOnly must not grant the edge data plane: %+v", r)
		}
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
	rules := buildTestRules(nil, nil, false)
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

func TestEnsureMCPIdentity_NeverBindsClusterAdmin(t *testing.T) {
	ctx := context.Background()
	srv := newServer("default", false)
	kube := kubefake.NewSimpleClientset(populatedTokenSecret(srv.Name))
	kcp := kcpfake.NewSimpleClientset(newBinding("code", bound("code.railgrid.ai", "repositories")))

	r := &Reconciler{}
	rules, err := r.desiredRules(ctx, kcp, srv, "root:railgrid:tenants:org-a:workspace-a")
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
	if crb.RoleRef.Name != "railgrid:mcpserver:default" || crb.RoleRef.Kind != "ClusterRole" {
		t.Fatalf("roleRef = %+v", crb.RoleRef)
	}
	if len(crb.OwnerReferences) != 1 || crb.OwnerReferences[0].UID != srv.UID {
		t.Fatalf("binding not owned by the MCPServer: %+v", crb.OwnerReferences)
	}
	role, err := kube.RbacV1().ClusterRoles().Get(ctx, "railgrid:mcpserver:default", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get role: %v", err)
	}
	if len(role.OwnerReferences) != 1 || role.OwnerReferences[0].UID != srv.UID {
		t.Fatalf("role not owned by the MCPServer: %+v", role.OwnerReferences)
	}
	assertNoWildcards(t, role.Rules)
	if findRule(t, role.Rules, "code.railgrid.ai", "repositories") == nil {
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

	if err := ensureMCPRBAC(ctx, kube, srv, owner, "default-mcp", buildTestRules(nil, nil, false)); err != nil {
		t.Fatalf("ensureMCPRBAC: %v", err)
	}
	if deleted != 1 || created != 1 {
		t.Fatalf("deleted=%d created=%d, want 1/1", deleted, created)
	}
	crb, err := kube.RbacV1().ClusterRoleBindings().Get(ctx, "default-mcp", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get binding: %v", err)
	}
	if crb.RoleRef.Name != "railgrid:mcpserver:default" {
		t.Fatalf("roleRef = %+v, want generated role", crb.RoleRef)
	}
	if len(crb.OwnerReferences) != 1 || crb.OwnerReferences[0].UID != srv.UID {
		t.Fatalf("recreated binding not owned by the MCPServer: %+v", crb.OwnerReferences)
	}

	// A second pass with the correct roleRef is a no-op on the binding.
	if err := ensureMCPRBAC(ctx, kube, srv, owner, "default-mcp", buildTestRules(nil, nil, false)); err != nil {
		t.Fatalf("second ensureMCPRBAC: %v", err)
	}
	if deleted != 1 || created != 1 {
		t.Fatalf("second pass touched the binding: deleted=%d created=%d", deleted, created)
	}
}

// A binding that already points at the generated role still has to converge:
// an extra subject holds the MCPServer's permissions, a wrong subject breaks
// the token, and a missing owner reference leaves the binding behind after the
// MCPServer is gone.
func TestEnsureMCPRBAC_ConvergesBindingSubjectsAndOwnership(t *testing.T) {
	ctx := context.Background()
	srv := newServer("default", false)
	owner := metav1.OwnerReference{Kind: "MCPServer", Name: srv.Name, UID: srv.UID}
	roleRef := rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: "railgrid:mcpserver:default"}
	drifted := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "default-mcp"},
		Subjects: []rbacv1.Subject{
			{Kind: "ServiceAccount", Name: "default-mcp", Namespace: mcpIdentityNamespace},
			{Kind: "User", APIGroup: rbacv1.GroupName, Name: "attacker@example.com"},
		},
		RoleRef: roleRef,
	}
	kube := kubefake.NewSimpleClientset(drifted)
	var deleted, created int
	kube.PrependReactor("delete", "clusterrolebindings", func(clienttesting.Action) (bool, runtime.Object, error) {
		deleted++
		return false, nil, nil
	})
	kube.PrependReactor("create", "clusterrolebindings", func(clienttesting.Action) (bool, runtime.Object, error) {
		created++
		return false, nil, nil
	})

	if err := ensureMCPRBAC(ctx, kube, srv, owner, "default-mcp", buildTestRules(nil, nil, false)); err != nil {
		t.Fatalf("ensureMCPRBAC: %v", err)
	}
	if deleted != 0 || created != 0 {
		t.Fatalf("a matching roleRef must be converged in place: deleted=%d created=%d", deleted, created)
	}
	got, err := kube.RbacV1().ClusterRoleBindings().Get(ctx, "default-mcp", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get binding: %v", err)
	}
	want := []rbacv1.Subject{{Kind: "ServiceAccount", Name: "default-mcp", Namespace: mcpIdentityNamespace}}
	if !slices.Equal(got.Subjects, want) {
		t.Fatalf("subjects = %+v, want only the MCPServer ServiceAccount", got.Subjects)
	}
	if len(got.OwnerReferences) != 1 || got.OwnerReferences[0].UID != srv.UID {
		t.Fatalf("owner references not restored: %+v", got.OwnerReferences)
	}

	// A wrong subject (right kind, wrong identity) is corrected too.
	got.Subjects = []rbacv1.Subject{{Kind: "ServiceAccount", Name: "other-mcp", Namespace: "elsewhere"}}
	if _, err := kube.RbacV1().ClusterRoleBindings().Update(ctx, got, metav1.UpdateOptions{}); err != nil {
		t.Fatalf("update binding: %v", err)
	}
	if err := ensureMCPRBAC(ctx, kube, srv, owner, "default-mcp", buildTestRules(nil, nil, false)); err != nil {
		t.Fatalf("second ensureMCPRBAC: %v", err)
	}
	got, err = kube.RbacV1().ClusterRoleBindings().Get(ctx, "default-mcp", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get binding: %v", err)
	}
	if !slices.Equal(got.Subjects, want) {
		t.Fatalf("subjects = %+v, want the MCPServer ServiceAccount restored", got.Subjects)
	}
}

// The generated role is only garbage-collected through its owner reference, so
// a role whose ownership drifted has to be repaired even when its rules match.
func TestEnsureMCPRBAC_ReconcilesClusterRoleOwnership(t *testing.T) {
	ctx := context.Background()
	srv := newServer("default", false)
	owner := metav1.OwnerReference{Kind: "MCPServer", Name: srv.Name, UID: srv.UID}
	rules := buildTestRules(nil, nil, false)
	orphaned := &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{Name: "railgrid:mcpserver:default"},
		Rules:      rules,
	}
	kube := kubefake.NewSimpleClientset(orphaned)

	if err := ensureMCPRBAC(ctx, kube, srv, owner, "default-mcp", rules); err != nil {
		t.Fatalf("ensureMCPRBAC: %v", err)
	}
	role, err := kube.RbacV1().ClusterRoles().Get(ctx, "railgrid:mcpserver:default", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get role: %v", err)
	}
	if len(role.OwnerReferences) != 1 || role.OwnerReferences[0].UID != srv.UID {
		t.Fatalf("role ownership not reconciled: %+v", role.OwnerReferences)
	}

	// A foreign owner reference is replaced, not appended to.
	role.OwnerReferences = []metav1.OwnerReference{{Kind: "MCPServer", Name: "someone-else", UID: types.UID("uid-other")}}
	if _, err := kube.RbacV1().ClusterRoles().Update(ctx, role, metav1.UpdateOptions{}); err != nil {
		t.Fatalf("update role: %v", err)
	}
	if err := ensureMCPRBAC(ctx, kube, srv, owner, "default-mcp", rules); err != nil {
		t.Fatalf("second ensureMCPRBAC: %v", err)
	}
	role, err = kube.RbacV1().ClusterRoles().Get(ctx, "railgrid:mcpserver:default", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get role: %v", err)
	}
	if len(role.OwnerReferences) != 1 || role.OwnerReferences[0].UID != srv.UID {
		t.Fatalf("foreign owner not replaced: %+v", role.OwnerReferences)
	}
}

func TestEnsureMCPRBAC_SecondReconcileAddsRulesForNewBinding(t *testing.T) {
	ctx := context.Background()
	srv := newServer("default", false)
	owner := metav1.OwnerReference{Kind: "MCPServer", Name: srv.Name, UID: srv.UID}
	var kube kubernetes.Interface = kubefake.NewSimpleClientset()
	kcp := kcpfake.NewSimpleClientset(newBinding("code", bound("code.railgrid.ai", "repositories")))
	r := &Reconciler{}

	reconcileRBAC := func() *rbacv1.ClusterRole {
		t.Helper()
		rules, err := r.desiredRules(ctx, kcp, srv, "root:railgrid:tenants:org-a:workspace-a")
		if err != nil {
			t.Fatalf("desiredRules: %v", err)
		}
		if err := ensureMCPRBAC(ctx, kube, srv, owner, "default-mcp", rules); err != nil {
			t.Fatalf("ensureMCPRBAC: %v", err)
		}
		role, err := kube.RbacV1().ClusterRoles().Get(ctx, "railgrid:mcpserver:default", metav1.GetOptions{})
		if err != nil {
			t.Fatalf("get role: %v", err)
		}
		return role
	}

	role := reconcileRBAC()
	if findRule(t, role.Rules, "edges.railgrid.ai", "kubernetesclusters") != nil {
		t.Fatalf("edges granted before the binding exists: %+v", role.Rules)
	}

	// A provider is enabled: its APIBinding appears with bound resources.
	if _, err := kcp.ApisV1alpha2().APIBindings().Create(ctx, newBinding("edges", bound("edges.railgrid.ai", "kubernetesclusters")), metav1.CreateOptions{}); err != nil {
		t.Fatalf("create binding: %v", err)
	}
	role = reconcileRBAC()
	if findRule(t, role.Rules, "edges.railgrid.ai", "kubernetesclusters") == nil {
		t.Fatalf("edges not granted after the binding appeared: %+v", role.Rules)
	}
	if findRule(t, role.Rules, "code.railgrid.ai", "repositories") == nil {
		t.Fatalf("existing grant lost: %+v", role.Rules)
	}
	assertNoWildcards(t, role.Rules)
}

func TestListBoundResources(t *testing.T) {
	kcp := kcpfake.NewSimpleClientset(
		newBinding("a", bound("code.railgrid.ai", "repositories"), bound("code.railgrid.ai", "connections")),
		newBinding("pending"), // not yet bound
	)
	got, err := listBoundResources(context.Background(), kcp, "root:railgrid:tenants:org-a:workspace-a")
	if err != nil {
		t.Fatalf("listBoundResources: %v", err)
	}
	var names []string
	for _, b := range got {
		names = append(names, b.Group+"/"+b.Resource)
	}
	slices.Sort(names)
	if want := "code.railgrid.ai/connections,code.railgrid.ai/repositories"; strings.Join(names, ",") != want {
		t.Fatalf("bound = %v, want %s", names, want)
	}
}

// TestBuildRules_EdgeHostsAreReadOnly: the generated role widens itself as a
// provider's APIExport grows, which is right for ordinary provider objects and
// wrong for the edge host kinds. `spec.harness` lives on them, so UPDATING one
// can ask a specific machine to start running a coding harness — arbitrary code
// execution on somebody's laptop or build box. An MCPServer token must not hold
// that write, even when the tenant has bound the resource, and it must not
// arrive through a catalog action either. Reads stay granted: listing edges and
// their status is most of what the edge tools do.
func TestBuildRules_EdgeHostsAreReadOnly(t *testing.T) {
	rules := buildTestRules([]BoundResource{
		bound("edges.railgrid.ai", "linuxservers"),
		bound("edges.railgrid.ai", "macosservers"),
		bound("edges.railgrid.ai", "kubernetesclusters"),
		bound("edges.railgrid.ai", "services"),
	}, []SubresourceGrant{
		testSubresourceGrant("edges.railgrid.ai", "linuxservers", "enable_harness", false),
	}, false)
	assertNoWildcards(t, rules)

	// Only the OBJECT's verbs are under test. A data-plane subresource grant
	// (kubernetesclusters/k8s and friends) uses "create" because kcp maps the
	// HTTP method onto the verb, and it proxies to the machine rather than
	// patching the edge — so it cannot set spec.harness and is not what this
	// denial is about.
	for _, r := range rules {
		for _, resource := range r.Resources {
			if strings.Contains(resource, "/") || !privilegedWriteResources["edges.railgrid.ai"][resource] {
				continue
			}
			for _, verb := range r.Verbs {
				if slices.Contains(writeVerbs, verb) {
					t.Fatalf("a generated MCPServer role granted %q on %q: %+v", verb, resource, r)
				}
			}
		}
	}

	if r := findRule(t, rules, "edges.railgrid.ai", "linuxservers"); r == nil {
		t.Fatal("denying the harness write also denied reading edges")
	} else if !slices.Contains(r.Verbs, "get") || !slices.Contains(r.Verbs, "list") {
		t.Fatalf("edge reads were dropped: %+v", r)
	}
	writable := findRule(t, rules, "edges.railgrid.ai", "services")
	if writable == nil {
		t.Fatal("services lost its rule")
	}
	if !slices.Contains(writable.Verbs, "create") {
		t.Fatalf("an unrelated bound resource lost its writes: %+v", writable)
	}
}
