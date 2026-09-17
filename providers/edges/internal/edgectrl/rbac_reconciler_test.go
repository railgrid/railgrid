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

package edgectrl

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	edgesv1alpha1 "github.com/railgrid/provider-edges/apis/v1alpha1"
)

const (
	testEdgeName = "build"
	testEdgeGVR  = "edges.railgrid.ai/v1alpha1"
)

func rbacTestOwner(kind, name, uid string) metav1.OwnerReference {
	controller := true
	blockDeletion := true
	return metav1.OwnerReference{
		APIVersion:         testEdgeGVR,
		Kind:               kind,
		Name:               name,
		UID:                types.UID(uid),
		Controller:         &controller,
		BlockOwnerDeletion: &blockDeletion,
	}
}

func newRBACTestClient(t *testing.T, objects ...client.Object) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add core scheme: %v", err)
	}
	if err := rbacv1.AddToScheme(scheme); err != nil {
		t.Fatalf("add rbac scheme: %v", err)
	}
	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
}

func TestCredentialsAreDisjointAcrossKindsForTheSameEdgeName(t *testing.T) {
	ctx := context.Background()
	kinds := []struct {
		kind     string
		resource string
		uid      string
	}{
		{kind: "KubernetesCluster", resource: edgesv1alpha1.KubernetesClusterResource, uid: "k8s-uid"},
		{kind: "LinuxServer", resource: edgesv1alpha1.LinuxServerResource, uid: "linux-uid"},
		{kind: "MacOSServer", resource: edgesv1alpha1.MacOSServerResource, uid: "mac-uid"},
	}

	seen := map[string]string{}
	for _, k := range kinds {
		name := edgesv1alpha1.EdgeCredentialName(k.resource, testEdgeName)
		if other, ok := seen[name]; ok {
			t.Fatalf("%s and %s credential names collide: %q", other, k.kind, name)
		}
		seen[name] = k.kind
	}

	// Provision every kind in one workspace, as the reconcilers would for three
	// same-named edges: each must get its own credentials without conflict.
	c := newRBACTestClient(t)
	for _, k := range kinds {
		name := edgesv1alpha1.EdgeCredentialName(k.resource, testEdgeName)
		owner := rbacTestOwner(k.kind, testEdgeName, k.uid)
		if err := ensureServiceAccount(ctx, c, name, owner); err != nil {
			t.Fatalf("create %s ServiceAccount: %v", k.kind, err)
		}
		if err := ensureClusterRoleBinding(ctx, c, name, owner); err != nil {
			t.Fatalf("create %s ClusterRoleBinding: %v", k.kind, err)
		}
		r := &RBACReconciler{gvr: edgesv1alpha1.SchemeGroupVersion.WithResource(k.resource)}
		if err := r.ensureEdgeProxyGrant(ctx, c, name, testEdgeName, owner); err != nil {
			t.Fatalf("create %s proxy grant: %v", k.kind, err)
		}
	}

	for _, k := range kinds {
		name := edgesv1alpha1.EdgeCredentialName(k.resource, testEdgeName)
		var sa corev1.ServiceAccount
		if err := c.Get(ctx, client.ObjectKey{Namespace: edgeNamespace, Name: name}, &sa); err != nil {
			t.Fatalf("get ServiceAccount %q: %v", name, err)
		}
		if len(sa.OwnerReferences) != 1 || sa.OwnerReferences[0].Kind != k.kind || sa.OwnerReferences[0].Name != testEdgeName {
			t.Errorf("ServiceAccount %q owner references = %+v, want the same-named %s", name, sa.OwnerReferences, k.kind)
		}
		var binding rbacv1.ClusterRoleBinding
		if err := c.Get(ctx, client.ObjectKey{Name: "railgrid-edge-" + name}, &binding); err != nil {
			t.Fatalf("get agent binding %q: %v", name, err)
		}
		if len(binding.Subjects) != 1 || binding.Subjects[0].Name != name || binding.Subjects[0].Namespace != edgeNamespace {
			t.Errorf("agent binding %q subjects = %+v, want ServiceAccount %s/%s", name, binding.Subjects, edgeNamespace, name)
		}
		var grant rbacv1.ClusterRole
		if err := c.Get(ctx, client.ObjectKey{Name: "railgrid-edge-proxy-" + name}, &grant); err != nil {
			t.Fatalf("get proxy grant %q: %v", name, err)
		}
		if len(grant.Rules) != 1 || len(grant.Rules[0].Resources) != 1 || grant.Rules[0].Resources[0] != k.resource ||
			len(grant.Rules[0].ResourceNames) != 1 || grant.Rules[0].ResourceNames[0] != testEdgeName {
			t.Errorf("proxy grant %q rules = %+v, want only %s/%q", name, grant.Rules, k.resource, testEdgeName)
		}
		var proxyBinding rbacv1.ClusterRoleBinding
		if err := c.Get(ctx, client.ObjectKey{Name: "railgrid-edge-proxy-" + name}, &proxyBinding); err != nil {
			t.Fatalf("get proxy binding %q: %v", name, err)
		}
		if len(proxyBinding.Subjects) != 1 || proxyBinding.Subjects[0].Name != name || proxyBinding.Subjects[0].Namespace != edgeNamespace {
			t.Errorf("proxy binding %q subjects = %+v, want ServiceAccount %s/%s", name, proxyBinding.Subjects, edgeNamespace, name)
		}
	}
}

func TestCredentialHelpersRefuseAResourceControlledByAnotherEdge(t *testing.T) {
	ctx := context.Background()
	foreignOwner := rbacTestOwner("LinuxServer", testEdgeName, "linux-uid")
	macOwner := rbacTestOwner("MacOSServer", testEdgeName, "mac-uid")
	macName := edgesv1alpha1.EdgeCredentialName(edgesv1alpha1.MacOSServerResource, testEdgeName)
	macGrantName := "railgrid-edge-proxy-" + macName

	cases := []struct {
		name string
		obj  client.Object
		call func(client.Client) error
	}{
		{
			name: "service account",
			obj: &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{
				Name: macName, Namespace: edgeNamespace, OwnerReferences: []metav1.OwnerReference{foreignOwner},
			}},
			call: func(c client.Client) error { return ensureServiceAccount(ctx, c, macName, macOwner) },
		},
		{
			name: "agent role binding",
			obj: &rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{
				Name: "railgrid-edge-" + macName, OwnerReferences: []metav1.OwnerReference{foreignOwner},
			}},
			call: func(c client.Client) error { return ensureClusterRoleBinding(ctx, c, macName, macOwner) },
		},
		{
			name: "proxy role",
			obj: &rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{
				Name: macGrantName, OwnerReferences: []metav1.OwnerReference{foreignOwner},
			}},
			call: func(c client.Client) error {
				r := &RBACReconciler{gvr: schema.GroupVersionResource{Group: "edges.railgrid.ai", Version: "v1alpha1", Resource: "macosservers"}}
				return r.ensureEdgeProxyGrant(ctx, c, macName, testEdgeName, macOwner)
			},
		},
		{
			name: "token secret",
			obj: &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
				Name: macName + "-token", Namespace: edgeNamespace, OwnerReferences: []metav1.OwnerReference{foreignOwner},
			}},
			call: func(c client.Client) error {
				return ensureTokenSecret(ctx, c, macName+"-token", macName, macOwner)
			},
		},
		{
			name: "kubeconfig secret",
			obj: &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
				Name: macName + "-kubeconfig", Namespace: edgeNamespace, OwnerReferences: []metav1.OwnerReference{foreignOwner},
			}},
			call: func(c client.Client) error {
				r := &RBACReconciler{hubExternalURL: "https://hub.invalid"}
				return r.ensureKubeconfigSecret(ctx, c, macName+"-kubeconfig", testEdgeName, "token", macOwner)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := newRBACTestClient(t, tc.obj)
			err := tc.call(c)
			if err == nil || !strings.Contains(err.Error(), "already controlled") {
				t.Fatalf("helper error = %v, want an already-controlled refusal", err)
			}
			if tc.name == "proxy role" {
				var role rbacv1.ClusterRole
				if getErr := c.Get(ctx, client.ObjectKey{Name: macGrantName}, &role); getErr != nil {
					t.Fatalf("get foreign proxy role after refusal: %v", getErr)
				}
				if len(role.Rules) != 0 {
					t.Fatalf("foreign proxy role rules mutated before owner refusal: %+v", role.Rules)
				}
			}
		})
	}
}

// TestAgentRulesGrantAddonsReadOnly: the agent watches Addons and reports on
// them, and that is ALL. Creating an Addon is the privileged act that turns a
// machine into a code-execution host; an agent that could create one could
// enrol itself, which would make the tenant's half of the trust model
// meaningless.
func TestAgentRulesGrantAddonsReadOnly(t *testing.T) {
	var objects, statuses *rbacv1.PolicyRule
	for i, rule := range desiredAgentRules() {
		if len(rule.APIGroups) != 1 || rule.APIGroups[0] != "edges.railgrid.ai" {
			continue
		}
		for _, resource := range rule.Resources {
			switch resource {
			case "addons":
				objects = &desiredAgentRules()[i]
			case "addons/status":
				statuses = &desiredAgentRules()[i]
			}
		}
	}

	if objects == nil {
		t.Fatal("the agent ClusterRole does not grant addons at all; the add-on manager cannot watch")
	}
	if len(objects.Resources) != 1 || objects.Resources[0] != "addons" {
		t.Fatalf("the addons rule covers more than addons: %v", objects.Resources)
	}
	for _, verb := range objects.Verbs {
		switch verb {
		case "get", "list", "watch":
		default:
			t.Errorf("the agent is granted %q on addons; it must be read-only", verb)
		}
	}

	if statuses == nil {
		t.Fatal("the agent cannot report addon status")
	}
	for _, verb := range statuses.Verbs {
		switch verb {
		case "get", "update", "patch":
		default:
			t.Errorf("the agent is granted %q on addons/status", verb)
		}
	}
}
