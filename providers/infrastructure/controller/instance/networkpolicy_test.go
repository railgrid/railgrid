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

package instance

import (
	"context"
	"testing"
	"time"

	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic/fake"
	clienttesting "k8s.io/client-go/testing"

	"github.com/railgrid/provider-infrastructure/kro"
	"github.com/railgrid/provider-infrastructure/networkpolicy"
)

const (
	netpolTenant    = "1abcdefg"
	netpolNamespace = "1abcdefg-default"
)

var enabledNetworkPolicy = networkpolicy.Config{
	Enabled:           true,
	GatewayNamespace:  "cfgate-system",
	AllowedNamespaces: []string{"kube-system"},
	AllowedCIDRs:      []string{"10.0.0.0/16"},
}

func netpolTestController(cfg networkpolicy.Config, objects ...runtime.Object) (*Controller, *fake.FakeDynamicClient) {
	client := fake.NewSimpleDynamicClient(runtime.NewScheme(), objects...)
	return &Controller{cfg: Config{Runtime: client, NetworkPolicy: cfg}}, client
}

func runtimeNamespace(name string, uid types.UID, labels map[string]any) *unstructured.Unstructured {
	metadata := map[string]any{"name": name, "uid": string(uid)}
	if labels != nil {
		metadata["labels"] = labels
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "Namespace",
		"metadata":   metadata,
	}}
}

func mustUnstructuredPolicy(t *testing.T, np *networkingv1.NetworkPolicy) *unstructured.Unstructured {
	t.Helper()
	obj, err := toUnstructuredNetworkPolicy(np)
	if err != nil {
		t.Fatal(err)
	}
	return obj
}

func getPolicy(t *testing.T, client *fake.FakeDynamicClient) (*networkingv1.NetworkPolicy, bool) {
	t.Helper()
	obj, err := client.Resource(networkPolicyGVR).Namespace(netpolNamespace).Get(context.Background(), networkpolicy.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, false
	}
	if err != nil {
		t.Fatalf("get networkpolicy: %v", err)
	}
	np := &networkingv1.NetworkPolicy{}
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(obj.Object, np); err != nil {
		t.Fatal(err)
	}
	return np, true
}

func countActions(client *fake.FakeDynamicClient, verb, resource string) int {
	n := 0
	for _, action := range client.Actions() {
		if action.GetVerb() == verb && action.GetResource().Resource == resource {
			n++
		}
	}
	return n
}

func TestEnsureNamespaceCreatesLabelledNamespaceAndPolicy(t *testing.T) {
	c, client := netpolTestController(enabledNetworkPolicy)

	if err := c.ensureNamespace(context.Background(), netpolNamespace, netpolTenant); err != nil {
		t.Fatalf("ensureNamespace() error = %v", err)
	}

	ns, err := client.Resource(namespaceGVR).Get(context.Background(), netpolNamespace, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get namespace: %v", err)
	}
	if got := ns.GetLabels(); got[kro.LabelTenant] != kro.LabelTenantValue(netpolTenant) || got[kro.LabelManagedBy] != kro.ManagedByValue {
		t.Fatalf("namespace labels = %v, want the tenant and managed-by labels", got)
	}
	np, ok := getPolicy(t, client)
	if !ok {
		t.Fatal("isolation policy was not created")
	}
	want := networkpolicy.Build(netpolNamespace, netpolTenant, enabledNetworkPolicy)
	if !equality.Semantic.DeepEqual(np.Spec, want.Spec) || !networkpolicy.IsProviderOwned(np.Labels) {
		t.Fatalf("policy = %+v, want %+v", np, want)
	}
}

func TestEnsureNamespaceBackfillsMissingTenantLabels(t *testing.T) {
	existing := runtimeNamespace(netpolNamespace, "uid-1", map[string]any{"team": "platform"})
	c, client := netpolTestController(enabledNetworkPolicy, existing)

	if err := c.ensureNamespace(context.Background(), netpolNamespace, netpolTenant); err != nil {
		t.Fatalf("ensureNamespace() error = %v", err)
	}
	ns, err := client.Resource(namespaceGVR).Get(context.Background(), netpolNamespace, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	labels := ns.GetLabels()
	if labels[kro.LabelTenant] != kro.LabelTenantValue(netpolTenant) || labels[kro.LabelManagedBy] != kro.ManagedByValue || labels["team"] != "platform" {
		t.Fatalf("namespace labels = %v, want the tenant labels added and team kept", labels)
	}
}

func TestEnsureNamespaceLeavesForeignNamespaceLabels(t *testing.T) {
	existing := runtimeNamespace(netpolNamespace, "uid-1", map[string]any{kro.LabelManagedBy: "someone-else"})
	c, client := netpolTestController(enabledNetworkPolicy, existing)

	if err := c.ensureNamespace(context.Background(), netpolNamespace, netpolTenant); err != nil {
		t.Fatalf("ensureNamespace() error = %v", err)
	}
	if n := countActions(client, "patch", "namespaces"); n != 0 {
		t.Fatalf("namespace patched %d times, want a foreign namespace left alone", n)
	}
}

func TestEnsureNamespaceDoesNotLabelWhenDisabled(t *testing.T) {
	existing := runtimeNamespace(netpolNamespace, "uid-1", nil)
	c, client := netpolTestController(networkpolicy.Config{}, existing)

	if err := c.ensureNamespace(context.Background(), netpolNamespace, netpolTenant); err != nil {
		t.Fatalf("ensureNamespace() error = %v", err)
	}
	if n := countActions(client, "patch", "namespaces"); n != 0 {
		t.Fatalf("namespace patched %d times with the policy disabled", n)
	}
	if _, ok := getPolicy(t, client); ok {
		t.Fatal("policy created with the feature disabled")
	}
}

func TestApplyTenantNetworkPolicyConvergesDriftKeepingForeignLabels(t *testing.T) {
	drifted := networkpolicy.Build(netpolNamespace, netpolTenant, networkpolicy.Config{Enabled: true})
	drifted.Spec.Ingress = nil
	drifted.Labels = map[string]string{"team": "platform"}
	drifted.Annotations = map[string]string{"note": "hand-edited"}
	c, client := netpolTestController(enabledNetworkPolicy, mustUnstructuredPolicy(t, drifted))

	if err := c.applyTenantNetworkPolicy(context.Background(), netpolNamespace, netpolTenant); err != nil {
		t.Fatalf("applyTenantNetworkPolicy() error = %v", err)
	}
	np, _ := getPolicy(t, client)
	want := networkpolicy.Build(netpolNamespace, netpolTenant, enabledNetworkPolicy)
	if !equality.Semantic.DeepEqual(np.Spec, want.Spec) {
		t.Fatalf("spec = %+v, want %+v", np.Spec, want.Spec)
	}
	if !networkpolicy.IsProviderOwned(np.Labels) || np.Labels["team"] != "platform" || np.Annotations["note"] != "hand-edited" {
		t.Fatalf("metadata = labels %v annotations %v, want ours added and foreign kept", np.Labels, np.Annotations)
	}

	// Converged: a second pass writes nothing.
	updates := countActions(client, "update", "networkpolicies")
	if err := c.applyTenantNetworkPolicy(context.Background(), netpolNamespace, netpolTenant); err != nil {
		t.Fatalf("second applyTenantNetworkPolicy() error = %v", err)
	}
	if n := countActions(client, "update", "networkpolicies"); n != updates {
		t.Fatalf("converged policy updated again (%d updates, want %d)", n, updates)
	}
}

func TestApplyTenantNetworkPolicyErrorFailsTheReconcile(t *testing.T) {
	c, client := netpolTestController(enabledNetworkPolicy)
	client.PrependReactor("create", "networkpolicies", func(clienttesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(networkPolicyGVR.GroupResource(), networkpolicy.Name, nil)
	})

	if err := c.ensureNamespace(context.Background(), netpolNamespace, netpolTenant); !apierrors.IsForbidden(err) {
		t.Fatalf("ensureNamespace() error = %v, want the Forbidden create", err)
	}
	if _, ok := c.networkPolicySynced.Load(netpolNamespace); ok {
		t.Fatal("a failed apply was cached as converged")
	}
}

func TestRemoveTenantNetworkPolicyWhenDisabled(t *testing.T) {
	owned := mustUnstructuredPolicy(t, networkpolicy.Build(netpolNamespace, netpolTenant, enabledNetworkPolicy))
	c, client := netpolTestController(networkpolicy.Config{}, owned)

	if err := c.ensureTenantNetworkPolicy(context.Background(), netpolNamespace, netpolTenant, "uid-1"); err != nil {
		t.Fatalf("ensureTenantNetworkPolicy() error = %v", err)
	}
	if _, ok := getPolicy(t, client); ok {
		t.Fatal("provider-owned policy survived disabling")
	}
}

func TestRemoveTenantNetworkPolicyLeavesForeignPolicy(t *testing.T) {
	foreign := networkpolicy.Build(netpolNamespace, netpolTenant, enabledNetworkPolicy)
	foreign.Labels = map[string]string{"team": "platform"}
	c, client := netpolTestController(networkpolicy.Config{}, mustUnstructuredPolicy(t, foreign))

	if err := c.ensureTenantNetworkPolicy(context.Background(), netpolNamespace, netpolTenant, "uid-1"); err != nil {
		t.Fatalf("ensureTenantNetworkPolicy() error = %v", err)
	}
	if _, ok := getPolicy(t, client); !ok {
		t.Fatal("a policy without the ownership labels was deleted")
	}
}

func TestRemoveTenantNetworkPolicyIgnoresErrors(t *testing.T) {
	c, client := netpolTestController(networkpolicy.Config{})
	client.PrependReactor("get", "networkpolicies", func(clienttesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(networkPolicyGVR.GroupResource(), networkpolicy.Name, nil)
	})

	if err := c.ensureTenantNetworkPolicy(context.Background(), netpolNamespace, netpolTenant, "uid-1"); err != nil {
		t.Fatalf("disabled ensureTenantNetworkPolicy() error = %v, want removal to be best-effort", err)
	}
}

func TestEnsureTenantNetworkPolicyCachesPerNamespaceUID(t *testing.T) {
	ctx := context.Background()
	c, client := netpolTestController(enabledNetworkPolicy)
	deletePolicy := func() {
		t.Helper()
		if err := client.Resource(networkPolicyGVR).Namespace(netpolNamespace).Delete(ctx, networkpolicy.Name, metav1.DeleteOptions{}); err != nil {
			t.Fatal(err)
		}
	}

	if err := c.ensureTenantNetworkPolicy(ctx, netpolNamespace, netpolTenant, "uid-1"); err != nil {
		t.Fatal(err)
	}
	deletePolicy()

	// Same namespace instance inside the resync window: skipped.
	if err := c.ensureTenantNetworkPolicy(ctx, netpolNamespace, netpolTenant, "uid-1"); err != nil {
		t.Fatal(err)
	}
	if _, ok := getPolicy(t, client); ok {
		t.Fatal("cached namespace was re-read inside the resync window")
	}

	// The namespace was recreated: converged at once.
	if err := c.ensureTenantNetworkPolicy(ctx, netpolNamespace, netpolTenant, "uid-2"); err != nil {
		t.Fatal(err)
	}
	if _, ok := getPolicy(t, client); !ok {
		t.Fatal("recreated namespace did not get its policy")
	}

	// The resync window passed: a deleted policy is restored.
	deletePolicy()
	c.networkPolicySynced.Store(netpolNamespace, networkPolicySync{namespaceUID: "uid-2", at: time.Now().Add(-networkPolicyResync - time.Second)})
	if err := c.ensureTenantNetworkPolicy(ctx, netpolNamespace, netpolTenant, "uid-2"); err != nil {
		t.Fatal(err)
	}
	if _, ok := getPolicy(t, client); !ok {
		t.Fatal("policy was not restored after the resync window")
	}
}
