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

	v1alpha1 "github.com/faroshq/provider-infrastructure/apis/v1alpha1"
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
	crbName := "faros-infrastructure-serve-" + saName
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
	crbName := "faros-infrastructure-serve-" + saName
	t.Setenv(serveClusterRoleEnv, "infrastructure-serve")

	client := serveRBACConflictClient(crbName, saName, "cluster-admin", "infrastructure-serve")
	if err := ensureServeRBAC(context.Background(), client, saName); err != nil {
		t.Fatalf("ensureServeRBAC: %v, want nil once the surviving binding already has the desired roleRef", err)
	}
}

func TestEnsureProviderServePropagatesPlatformPreviewBridgeJWKS(t *testing.T) {
	const jwks = `{"keys":[{"kid":"current"}]}`
	t.Setenv("FAROS_PREVIEW_BRIDGE_VERIFICATION_JWKS", "  "+jwks+"  ")
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
		if env.Name == "FAROS_PREVIEW_BRIDGE_VERIFICATION_JWKS" {
			if env.Value != jwks {
				t.Errorf("verification JWKS = %q, want trimmed platform value %q", env.Value, jwks)
			}
			return
		}
	}
	t.Error("managed provider Deployment lacks FAROS_PREVIEW_BRIDGE_VERIFICATION_JWKS")
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
	crbName := "faros-infrastructure-serve-" + provider.Name
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
		"FAROS_APP_BASE_DOMAIN":      "apps.example.test",
		"FAROS_ACCESS_PROXY_IMAGE":   "example.test/access-proxy@sha256:deadbeef",
		"FAROS_ACCESS_HUB_URL":       "https://access-hub.internal",
		"FAROS_ACCESS_HUB_INSECURE":  "true",
		"FAROS_ACCESS_PUBLIC_SCHEME": "https",
		"FAROS_APP_PUBLIC_PORT":      "10443",
		"FAROS_GATEWAY_NAME":         "shared",
		"FAROS_GATEWAY_NAMESPACE":    "gateway-system",
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
	if got := env["FAROS_CODING_SANDBOX_ENABLED"]; got != "true" {
		t.Errorf("FAROS_CODING_SANDBOX_ENABLED = %q, want true", got)
	}
	if got := env["FAROS_DEV_IMAGE_UNIVERSAL"]; got != provider.Spec.Development.Images["universal"] {
		t.Errorf("FAROS_DEV_IMAGE_UNIVERSAL = %q, want %q", got, provider.Spec.Development.Images["universal"])
	}
	if got := env["FAROS_DEV_AGENT_IMAGE"]; got != provider.Spec.Development.AgentImage {
		t.Errorf("FAROS_DEV_AGENT_IMAGE = %q, want %q", got, provider.Spec.Development.AgentImage)
	}
}
