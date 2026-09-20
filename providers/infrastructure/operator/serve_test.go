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

package operator

import (
	"context"
	"strings"
	"testing"

	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	clienttesting "k8s.io/client-go/testing"

	v1alpha1 "github.com/railgrid/provider-infrastructure/apis/v1alpha1"
	"github.com/railgrid/provider-infrastructure/networkpolicy"
)

// serveRBACConflictClient is a clientset whose serve ClusterRoleBinding
// survives the Delete (still terminating, or recreated by another actor) and
// therefore answers the follow-up Create with AlreadyExists. roleRefs is the
// roleRef each successive Get observes.
func serveRBACConflictClient(crbName, saName string, roleRefs ...string) *fake.Clientset {
	client := fake.NewSimpleClientset()
	gets := 0
	binding := func(roleRef string) *rbacv1.ClusterRoleBinding {
		return &rbacv1.ClusterRoleBinding{
			ObjectMeta: metav1.ObjectMeta{Name: crbName},
			RoleRef:    rbacv1.RoleRef{APIGroup: "rbac.authorization.k8s.io", Kind: "ClusterRole", Name: roleRef},
			Subjects:   []rbacv1.Subject{{Kind: "ServiceAccount", Name: saName, Namespace: ServeNamespace}},
		}
	}
	client.PrependReactor("get", "clusterrolebindings", func(clienttesting.Action) (bool, runtime.Object, error) {
		roleRef := roleRefs[min(gets, len(roleRefs)-1)]
		gets++
		return true, binding(roleRef), nil
	})
	client.PrependReactor("delete", "clusterrolebindings", func(clienttesting.Action) (bool, runtime.Object, error) {
		return true, nil, nil
	})
	client.PrependReactor("create", "clusterrolebindings", func(clienttesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewAlreadyExists(rbacv1.Resource("clusterrolebindings"), crbName)
	})
	return client
}

// A create that loses the race must not report success. Deleting a
// ClusterRoleBinding whose roleRef is wrong and recreating it is not atomic:
// the API server can still hold the old object and answer the Create with
// AlreadyExists. Ignoring that error left the serve ServiceAccount bound to the
// old, broader ClusterRole while the reconcile reported success, so the
// least-privilege role only took effect on some later pass — or never.
func TestEnsureServeRBACRejectsStaleBindingAfterCreateConflict(t *testing.T) {
	const saName = "infrastructure"
	crbName := "railgrid-infrastructure-serve-" + saName
	t.Setenv(serveClusterRoleEnv, "infrastructure-serve")

	client := serveRBACConflictClient(crbName, saName, "cluster-admin")
	err := ensureServeRBAC(context.Background(), client, saName)
	if err == nil {
		t.Fatal("ensureServeRBAC returned nil, want an error: the binding still carries the old cluster-admin roleRef")
	}
	if !strings.Contains(err.Error(), "cluster-admin") || !strings.Contains(err.Error(), "infrastructure-serve") {
		t.Fatalf("ensureServeRBAC error = %v, want it to name both the stale and the desired ClusterRole", err)
	}
}

// The same conflict is not an error once the surviving object carries the
// roleRef we wanted: another actor replaced the binding first and the desired
// state holds.
func TestEnsureServeRBACAcceptsCreateConflictWithDesiredRoleRef(t *testing.T) {
	const saName = "infrastructure"
	crbName := "railgrid-infrastructure-serve-" + saName
	t.Setenv(serveClusterRoleEnv, "infrastructure-serve")

	client := serveRBACConflictClient(crbName, saName, "cluster-admin", "infrastructure-serve")
	if err := ensureServeRBAC(context.Background(), client, saName); err != nil {
		t.Fatalf("ensureServeRBAC: %v, want nil once the surviving binding already has the desired roleRef", err)
	}
}

func TestEnsureProviderServePropagatesPlatformPreviewBridgeJWKS(t *testing.T) {
	const jwks = `{"keys":[{"kid":"current"}]}`
	t.Setenv("RAILGRID_PREVIEW_BRIDGE_VERIFICATION_JWKS", "  "+jwks+"  ")
	client := fake.NewSimpleClientset()
	provider := &v1alpha1.InfrastructureProvider{
		ObjectMeta: metav1.ObjectMeta{Name: "test-infrastructure"},
		Spec: v1alpha1.InfrastructureProviderSpec{
			Provider: v1alpha1.ProviderServeSpec{
				Image: v1alpha1.ImageSpec{Repository: "example.test/infrastructure", Tag: "test"},
			},
		},
	}

	if err := EnsureProviderServe(context.Background(), client, provider, []byte("provider-kubeconfig"), nil, nil); err != nil {
		t.Fatalf("EnsureProviderServe: %v", err)
	}
	deployment, err := client.AppsV1().Deployments(ServeNamespace).Get(
		context.Background(),
		provider.Name,
		metav1.GetOptions{},
	)
	if err != nil {
		t.Fatalf("get managed provider Deployment: %v", err)
	}
	for _, env := range deployment.Spec.Template.Spec.Containers[0].Env {
		if env.Name == "RAILGRID_PREVIEW_BRIDGE_VERIFICATION_JWKS" {
			if env.Value != jwks {
				t.Errorf("verification JWKS = %q, want trimmed platform value %q", env.Value, jwks)
			}
			return
		}
	}
	t.Error("managed provider Deployment lacks RAILGRID_PREVIEW_BRIDGE_VERIFICATION_JWKS")
}

// The tenant isolation policy is configured on the operator (chart values),
// not the CR, and must reach the serve Deployment verbatim; unset variables
// stay unset so the serve binary keeps its defaults.
func TestEnsureProviderServePropagatesTenantNetworkPolicy(t *testing.T) {
	t.Setenv(networkpolicy.EnvEnabled, "true")
	t.Setenv(networkpolicy.EnvAllowedNamespaces, " kube-system,monitoring ")
	t.Setenv(networkpolicy.EnvAllowedCIDRs, "")
	client := fake.NewSimpleClientset()
	provider := &v1alpha1.InfrastructureProvider{
		ObjectMeta: metav1.ObjectMeta{Name: "test-infrastructure"},
		Spec: v1alpha1.InfrastructureProviderSpec{
			Provider: v1alpha1.ProviderServeSpec{
				Image: v1alpha1.ImageSpec{Repository: "example.test/infrastructure", Tag: "test"},
			},
		},
	}

	if err := EnsureProviderServe(context.Background(), client, provider, []byte("provider-kubeconfig"), nil, nil); err != nil {
		t.Fatalf("EnsureProviderServe: %v", err)
	}
	deployment, err := client.AppsV1().Deployments(ServeNamespace).Get(context.Background(), provider.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get managed provider Deployment: %v", err)
	}
	got := map[string]string{}
	for _, env := range deployment.Spec.Template.Spec.Containers[0].Env {
		got[env.Name] = env.Value
	}
	if got[networkpolicy.EnvEnabled] != "true" || got[networkpolicy.EnvAllowedNamespaces] != "kube-system,monitoring" {
		t.Errorf("serve env = %v, want the operator's tenant network policy settings", got)
	}
	if _, ok := got[networkpolicy.EnvAllowedCIDRs]; ok {
		t.Errorf("empty %s was propagated", networkpolicy.EnvAllowedCIDRs)
	}
}

func TestEnsureProviderServeBindsServeRoleFromEnv(t *testing.T) {
	client := fake.NewSimpleClientset()
	provider := &v1alpha1.InfrastructureProvider{
		ObjectMeta: metav1.ObjectMeta{Name: "infrastructure"},
		Spec: v1alpha1.InfrastructureProviderSpec{
			Provider: v1alpha1.ProviderServeSpec{
				Image: v1alpha1.ImageSpec{Repository: "example.test/infrastructure", Tag: "test"},
			},
		},
	}
	crbName := "railgrid-infrastructure-serve-" + provider.Name
	roleOf := func(t *testing.T) string {
		t.Helper()
		crb, err := client.RbacV1().ClusterRoleBindings().Get(context.Background(), crbName, metav1.GetOptions{})
		if err != nil {
			t.Fatalf("get serve ClusterRoleBinding: %v", err)
		}
		if len(crb.Subjects) != 1 || crb.Subjects[0].Name != provider.Name || crb.Subjects[0].Namespace != ServeNamespace {
			t.Fatalf("serve ClusterRoleBinding subjects = %+v, want the serve ServiceAccount", crb.Subjects)
		}
		return crb.RoleRef.Name
	}

	// Unset: the pre-chart-role behaviour, cluster-admin.
	t.Setenv(serveClusterRoleEnv, "")
	if err := EnsureProviderServe(context.Background(), client, provider, []byte("provider-kubeconfig"), nil, nil); err != nil {
		t.Fatalf("EnsureProviderServe: %v", err)
	}
	if got := roleOf(t); got != "cluster-admin" {
		t.Fatalf("roleRef with env unset = %q, want cluster-admin", got)
	}

	// Set (what the chart does with operator.clusterAdmin=false): the existing
	// binding is replaced because roleRef is immutable.
	t.Setenv(serveClusterRoleEnv, "infrastructure-serve")
	if err := EnsureProviderServe(context.Background(), client, provider, []byte("provider-kubeconfig"), nil, nil); err != nil {
		t.Fatalf("EnsureProviderServe: %v", err)
	}
	if got := roleOf(t); got != "infrastructure-serve" {
		t.Fatalf("roleRef with env set = %q, want infrastructure-serve", got)
	}

	// Unchanged: idempotent, the binding is left alone.
	if err := EnsureProviderServe(context.Background(), client, provider, []byte("provider-kubeconfig"), nil, nil); err != nil {
		t.Fatalf("EnsureProviderServe: %v", err)
	}
	if got := roleOf(t); got != "infrastructure-serve" {
		t.Fatalf("roleRef after repeat = %q, want infrastructure-serve", got)
	}

	// An explicit runtime kubeconfig never creates in-cluster RBAC.
	if err := EnsureProviderServe(context.Background(), client, provider, []byte("provider-kubeconfig"), []byte("runtime-kubeconfig"), nil); err != nil {
		t.Fatalf("EnsureProviderServe: %v", err)
	}
	if got := roleOf(t); got != "infrastructure-serve" {
		t.Fatalf("roleRef with explicit runtime = %q, want untouched infrastructure-serve", got)
	}
}

// The provider kubeconfig the operator mounts is also the heartbeat bearer:
// the SDK reads it from RAILGRID_PROVIDER_KUBECONFIG. Without that variable serve
// beat unauthenticated, an enforcing hub answered 401, and the provider went
// stale — which took every consumer of /ui/providers/infrastructure down with
// it.
func TestEnsureProviderServeWiresHeartbeatCredential(t *testing.T) {
	client := fake.NewSimpleClientset()
	provider := &v1alpha1.InfrastructureProvider{
		ObjectMeta: metav1.ObjectMeta{Name: "test-infrastructure"},
		Spec: v1alpha1.InfrastructureProviderSpec{
			Hub: v1alpha1.HubSpec{URL: "https://heartbeat-hub.internal"},
			Provider: v1alpha1.ProviderServeSpec{
				Image: v1alpha1.ImageSpec{Repository: "example.test/infrastructure", Tag: "test"},
			},
		},
	}
	if err := EnsureProviderServe(context.Background(), client, provider, []byte("provider-kubeconfig"), nil, nil); err != nil {
		t.Fatalf("EnsureProviderServe: %v", err)
	}
	deployment, err := client.AppsV1().Deployments(ServeNamespace).Get(context.Background(), provider.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	env := map[string]string{}
	for _, variable := range deployment.Spec.Template.Spec.Containers[0].Env {
		env[variable.Name] = variable.Value
	}
	if env["RAILGRID_PROVIDER_KUBECONFIG"] != providerKubeconfigMount {
		t.Errorf("RAILGRID_PROVIDER_KUBECONFIG = %q, want %q", env["RAILGRID_PROVIDER_KUBECONFIG"], providerKubeconfigMount)
	}
	// serve reads exactly one kubeconfig name. The retired provider-specific
	// one, and the root-scoped retarget hint that went with it, must not be
	// handed to it any more.
	for _, retired := range []string{"INFRASTRUCTURE_KUBECONFIG", "INFRASTRUCTURE_WORKSPACE_PATH"} {
		if value, set := env[retired]; set {
			t.Errorf("%s = %q, want it unset: serve reads only RAILGRID_PROVIDER_KUBECONFIG", retired, value)
		}
	}
	if _, set := env["RAILGRID_HUB_TOKEN"]; set {
		t.Errorf("RAILGRID_HUB_TOKEN set without spec.hub.tokenSecret")
	}
}

func TestEnsureProviderServePropagatesPlatformPublishingConfig(t *testing.T) {
	client := fake.NewSimpleClientset()
	provider := &v1alpha1.InfrastructureProvider{
		ObjectMeta: metav1.ObjectMeta{Name: "test-infrastructure"},
		Spec: v1alpha1.InfrastructureProviderSpec{
			Hub: v1alpha1.HubSpec{URL: "https://heartbeat-hub.internal"},
			Provider: v1alpha1.ProviderServeSpec{
				Image: v1alpha1.ImageSpec{Repository: "example.test/infrastructure", Tag: "test"},
			},
			Application: v1alpha1.ApplicationSpec{BaseDomain: "legacy.example.test"},
			Publishing: v1alpha1.PublishingSpec{
				BaseDomain: "apps.example.test", AccessProxyImage: "example.test/access-proxy@sha256:deadbeef",
				HubURL: "https://access-hub.internal", HubInsecure: true, PublicScheme: "https", PublicPort: 10443,
				Gateway: v1alpha1.GatewayRef{Name: "shared", Namespace: "gateway-system"},
			},
		},
	}
	if err := EnsureProviderServe(context.Background(), client, provider, []byte("provider-kubeconfig"), nil, nil); err != nil {
		t.Fatalf("EnsureProviderServe: %v", err)
	}
	deployment, err := client.AppsV1().Deployments(ServeNamespace).Get(context.Background(), provider.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	env := map[string]string{}
	counts := map[string]int{}
	for _, variable := range deployment.Spec.Template.Spec.Containers[0].Env {
		env[variable.Name] = variable.Value
		counts[variable.Name]++
	}
	want := map[string]string{
		"RAILGRID_APP_BASE_DOMAIN":      "apps.example.test",
		"RAILGRID_ACCESS_PROXY_IMAGE":   "example.test/access-proxy@sha256:deadbeef",
		"RAILGRID_ACCESS_HUB_URL":       "https://access-hub.internal",
		"RAILGRID_ACCESS_HUB_INSECURE":  "true",
		"RAILGRID_ACCESS_PUBLIC_SCHEME": "https",
		"RAILGRID_APP_PUBLIC_PORT":      "10443",
		"RAILGRID_GATEWAY_NAME":         "shared",
		"RAILGRID_GATEWAY_NAMESPACE":    "gateway-system",
	}
	for name, value := range want {
		if env[name] != value {
			t.Errorf("%s = %q, want %q", name, env[name], value)
		}
		if counts[name] != 1 {
			t.Errorf("%s occurs %d times, want once", name, counts[name])
		}
	}
}

func TestEnsureProviderServePropagatesCodingSandboxConfig(t *testing.T) {
	client := fake.NewSimpleClientset()
	provider := &v1alpha1.InfrastructureProvider{
		ObjectMeta: metav1.ObjectMeta{Name: "test-infrastructure"},
		Spec: v1alpha1.InfrastructureProviderSpec{
			CodingSandbox: v1alpha1.CodingSandboxSpec{Enabled: true},
			Development: v1alpha1.DevelopmentSpec{
				AgentImage: "example.test/dev-agent@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
				Images: map[string]string{
					"universal": "example.test/universal@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				},
			},
			Provider: v1alpha1.ProviderServeSpec{
				Image: v1alpha1.ImageSpec{Repository: "example.test/infrastructure", Tag: "test"},
			},
		},
	}
	if err := EnsureProviderServe(context.Background(), client, provider, []byte("provider-kubeconfig"), nil, nil); err != nil {
		t.Fatalf("EnsureProviderServe: %v", err)
	}
	deployment, err := client.AppsV1().Deployments(ServeNamespace).Get(context.Background(), provider.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	env := map[string]string{}
	for _, variable := range deployment.Spec.Template.Spec.Containers[0].Env {
		env[variable.Name] = variable.Value
	}
	if got := env["RAILGRID_CODING_SANDBOX_ENABLED"]; got != "true" {
		t.Errorf("RAILGRID_CODING_SANDBOX_ENABLED = %q, want true", got)
	}
	if got := env["RAILGRID_DEV_IMAGE_UNIVERSAL"]; got != provider.Spec.Development.Images["universal"] {
		t.Errorf("RAILGRID_DEV_IMAGE_UNIVERSAL = %q, want %q", got, provider.Spec.Development.Images["universal"])
	}
	if got := env["RAILGRID_DEV_AGENT_IMAGE"]; got != provider.Spec.Development.AgentImage {
		t.Errorf("RAILGRID_DEV_AGENT_IMAGE = %q, want %q", got, provider.Spec.Development.AgentImage)
	}
}

// With the INFRASTRUCTURE_WORKSPACE_PATH hint gone, the operator — not serve —
// is what makes a supplied root-scoped kubeconfig point at the provider
// workspace. It scopes the copy it replicates, so the credential serve mounts
// already terminates at /clusters/<providerWorkspace>.
func TestEnsureProviderServeScopesReplicatedKubeconfigToWorkspace(t *testing.T) {
	client := fake.NewSimpleClientset()
	provider := &v1alpha1.InfrastructureProvider{
		ObjectMeta: metav1.ObjectMeta{Name: "test-infrastructure"},
		Spec: v1alpha1.InfrastructureProviderSpec{
			ProviderWorkspace: "root:railgrid:providers:infrastructure",
			Provider: v1alpha1.ProviderServeSpec{
				Image: v1alpha1.ImageSpec{Repository: "example.test/infrastructure", Tag: "test"},
			},
		},
	}
	rootKubeconfig := []byte(`apiVersion: v1
kind: Config
clusters:
- name: kcp
  cluster:
    server: https://kcp.example/clusters/root
contexts:
- name: kcp
  context: {cluster: kcp, user: admin}
current-context: kcp
users:
- name: admin
  user: {token: t}
`)
	if err := EnsureProviderServe(context.Background(), client, provider, rootKubeconfig, nil, nil); err != nil {
		t.Fatalf("EnsureProviderServe: %v", err)
	}
	secret, err := client.CoreV1().Secrets(ServeNamespace).Get(context.Background(), provider.Name+"-provider-kubeconfig", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	want := "https://kcp.example/clusters/root:railgrid:providers:infrastructure"
	if !strings.Contains(string(secret.Data["kubeconfig"]), want) {
		t.Fatalf("replicated kubeconfig = %s, want server %s", secret.Data["kubeconfig"], want)
	}
}
