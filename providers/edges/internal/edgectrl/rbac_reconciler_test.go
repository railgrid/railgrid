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
)

const (
	testEdgeName = "build"
	testEdgeGVR  = "edges.faros.sh/v1alpha1"
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

func TestLinuxAndMacOSCredentialsAreDisjointForTheSameEdgeName(t *testing.T) {
	ctx := context.Background()
	linuxOwner := rbacTestOwner("LinuxServer", testEdgeName, "linux-uid")
	macOwner := rbacTestOwner("MacOSServer", testEdgeName, "mac-uid")
	linuxName := edgeCredentialName("LinuxServer", testEdgeName)
	macName := edgeCredentialName("MacOSServer", testEdgeName)
	if linuxName == macName {
		t.Fatalf("LinuxServer and MacOSServer credential names collide: %q", linuxName)
	}

	c := newRBACTestClient(t)
	if err := ensureServiceAccount(ctx, c, linuxName, linuxOwner); err != nil {
		t.Fatalf("create Linux ServiceAccount: %v", err)
	}
	if err := ensureServiceAccount(ctx, c, macName, macOwner); err != nil {
		t.Fatalf("create MacOSServer ServiceAccount: %v", err)
	}
	if err := ensureClusterRoleBinding(ctx, c, linuxName, linuxOwner); err != nil {
		t.Fatalf("create Linux ClusterRoleBinding: %v", err)
	}
	if err := ensureClusterRoleBinding(ctx, c, macName, macOwner); err != nil {
		t.Fatalf("create MacOSServer ClusterRoleBinding: %v", err)
	}

	linux := &RBACReconciler{gvr: schema.GroupVersionResource{Group: "edges.faros.sh", Version: "v1alpha1", Resource: "linuxservers"}}
	mac := &RBACReconciler{gvr: schema.GroupVersionResource{Group: "edges.faros.sh", Version: "v1alpha1", Resource: "macosservers"}}
	if err := linux.ensureEdgeProxyGrant(ctx, c, linuxName, testEdgeName, linuxOwner); err != nil {
		t.Fatalf("create Linux proxy grant: %v", err)
	}
	if err := mac.ensureEdgeProxyGrant(ctx, c, macName, testEdgeName, macOwner); err != nil {
		t.Fatalf("create MacOSServer proxy grant: %v", err)
	}

	for _, tc := range []struct {
		name     string
		resource string
	}{
		{name: linuxName, resource: "linuxservers"},
		{name: macName, resource: "macosservers"},
	} {
		var sa corev1.ServiceAccount
		if err := c.Get(ctx, client.ObjectKey{Namespace: edgeNamespace, Name: tc.name}, &sa); err != nil {
			t.Fatalf("get ServiceAccount %q: %v", tc.name, err)
		}
		if len(sa.OwnerReferences) != 1 || sa.OwnerReferences[0].Name != testEdgeName {
			t.Errorf("ServiceAccount %q owner references = %+v, want the same-named edge", tc.name, sa.OwnerReferences)
		}
		var binding rbacv1.ClusterRoleBinding
		if err := c.Get(ctx, client.ObjectKey{Name: "faros-edge-" + tc.name}, &binding); err != nil {
			t.Fatalf("get agent binding %q: %v", tc.name, err)
		}
		if len(binding.Subjects) != 1 || binding.Subjects[0].Name != tc.name || binding.Subjects[0].Namespace != edgeNamespace {
			t.Errorf("agent binding %q subjects = %+v, want ServiceAccount %s/%s", tc.name, binding.Subjects, edgeNamespace, tc.name)
		}
		var grant rbacv1.ClusterRole
		if err := c.Get(ctx, client.ObjectKey{Name: "faros-edge-proxy-" + tc.name}, &grant); err != nil {
			t.Fatalf("get proxy grant %q: %v", tc.name, err)
		}
		if len(grant.Rules) != 1 || len(grant.Rules[0].Resources) != 1 || grant.Rules[0].Resources[0] != tc.resource ||
			len(grant.Rules[0].ResourceNames) != 1 || grant.Rules[0].ResourceNames[0] != testEdgeName {
			t.Errorf("proxy grant %q rules = %+v, want only %s/%q", tc.name, grant.Rules, tc.resource, testEdgeName)
		}
		var proxyBinding rbacv1.ClusterRoleBinding
		if err := c.Get(ctx, client.ObjectKey{Name: "faros-edge-proxy-" + tc.name}, &proxyBinding); err != nil {
			t.Fatalf("get proxy binding %q: %v", tc.name, err)
		}
		if len(proxyBinding.Subjects) != 1 || proxyBinding.Subjects[0].Name != tc.name || proxyBinding.Subjects[0].Namespace != edgeNamespace {
			t.Errorf("proxy binding %q subjects = %+v, want ServiceAccount %s/%s", tc.name, proxyBinding.Subjects, edgeNamespace, tc.name)
		}
	}
}

func TestCredentialHelpersRefuseAResourceControlledByAnotherEdge(t *testing.T) {
	ctx := context.Background()
	foreignOwner := rbacTestOwner("LinuxServer", testEdgeName, "linux-uid")
	macOwner := rbacTestOwner("MacOSServer", testEdgeName, "mac-uid")
	macName := edgeCredentialName("MacOSServer", testEdgeName)
	macGrantName := "faros-edge-proxy-" + macName

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
				Name: "faros-edge-" + macName, OwnerReferences: []metav1.OwnerReference{foreignOwner},
			}},
			call: func(c client.Client) error { return ensureClusterRoleBinding(ctx, c, macName, macOwner) },
		},
		{
			name: "proxy role",
			obj: &rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{
				Name: macGrantName, OwnerReferences: []metav1.OwnerReference{foreignOwner},
			}},
			call: func(c client.Client) error {
				r := &RBACReconciler{gvr: schema.GroupVersionResource{Group: "edges.faros.sh", Version: "v1alpha1", Resource: "macosservers"}}
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
