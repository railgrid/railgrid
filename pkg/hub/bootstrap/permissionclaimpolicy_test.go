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

package bootstrap

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	discoveryfake "k8s.io/client-go/discovery/fake"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/rest"
	clienttesting "k8s.io/client-go/testing"
	"k8s.io/klog/v2"
	"k8s.io/klog/v2/ktesting"
)

// servingDiscovery answers as a kcp that has kcp-dev/kcp#4385 merged.
func servingDiscovery() discovery.DiscoveryInterface {
	client := &discoveryfake.FakeDiscovery{Fake: &clienttesting.Fake{}}
	client.Resources = []*metav1.APIResourceList{{
		GroupVersion: PermissionClaimPolicyGroupVersion,
		APIResources: []metav1.APIResource{{
			Name:       PermissionClaimPolicyResource,
			Kind:       "PermissionClaimPolicy",
			Namespaced: false,
			Verbs:      metav1.Verbs{"get", "list", "create", "update"},
		}},
	}}
	return client
}

// absentDiscovery answers as today's kcp: the group/version is not served, and
// ServerResourcesForGroupVersion reports that as a NotFound.
func absentDiscovery() discovery.DiscoveryInterface {
	return &discoveryfake.FakeDiscovery{Fake: &clienttesting.Fake{}}
}

// erroringDiscovery cannot answer at all. "I could not tell" must not be read
// as "it is not there".
type erroringDiscovery struct {
	discovery.DiscoveryInterface
	err error
}

func (d erroringDiscovery) ServerResourcesForGroupVersion(string) (*metav1.APIResourceList, error) {
	return nil, d.err
}

func fakeDynamic(objects ...runtime.Object) *dynamicfake.FakeDynamicClient {
	scheme := runtime.NewScheme()
	listKinds := map[schema.GroupVersionResource]string{
		permissionClaimPolicyGVR: "PermissionClaimPolicyList",
	}
	return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, listKinds, objects...)
}

// contextWithLogs returns a context carrying a logger that buffers everything
// written to it, plus a reader for that buffer: the "log once" behaviour is
// part of the contract, so the test has to be able to count the lines.
func contextWithLogs(t *testing.T) (context.Context, func() string) {
	t.Helper()
	logger := ktesting.NewLogger(t, ktesting.NewConfig(ktesting.BufferLogs(true)))
	sink, ok := logger.GetSink().(ktesting.Underlier)
	if !ok {
		t.Fatal("ktesting logger does not expose its buffer")
	}
	return klog.NewContext(context.Background(), logger), func() string { return sink.GetBuffer().String() }
}

func TestPermissionClaimPolicyServed(t *testing.T) {
	served, err := PermissionClaimPolicyServed(servingDiscovery())
	if err != nil || !served {
		t.Fatalf("serving cluster: served=%v err=%v", served, err)
	}

	served, err = PermissionClaimPolicyServed(absentDiscovery())
	if err != nil || served {
		t.Fatalf("cluster without the API: served=%v err=%v", served, err)
	}

	// A group that exists but does not carry the resource is still "not served".
	partial := &discoveryfake.FakeDiscovery{Fake: &clienttesting.Fake{}}
	partial.Resources = []*metav1.APIResourceList{{
		GroupVersion: PermissionClaimPolicyGroupVersion,
		APIResources: []metav1.APIResource{{Name: "somethingelse"}},
	}}
	served, err = PermissionClaimPolicyServed(partial)
	if err != nil || served {
		t.Fatalf("group without the resource: served=%v err=%v", served, err)
	}

	boom := errors.New("connection refused")
	if _, err := PermissionClaimPolicyServed(erroringDiscovery{err: boom}); !errors.Is(err, boom) {
		t.Fatalf("a discovery failure must propagate, got %v", err)
	}

	// kcp not serving the group at all surfaces as NotFound, which is the
	// expected answer today, not a failure.
	notFound := apierrors.NewNotFound(schema.GroupResource{Group: "admin.kcp.io", Resource: "permissionclaimpolicies"}, "")
	if served, err := PermissionClaimPolicyServed(erroringDiscovery{err: notFound}); err != nil || served {
		t.Fatalf("NotFound discovery: served=%v err=%v", served, err)
	}

	// A real kcp front proxy answers a path no virtual workspace claims with
	// 403, not 404, so "the admin virtual workspace is not there" arrives as
	// Forbidden. Reading that as an error is fatal to the hub, which is what
	// crash-looped it against a released kcp.
	unresolved := apierrors.NewForbidden(schema.GroupResource{}, "", errors.New(
		`User "railgrid-e2e-admin" cannot get path "/services/admin/clusters/root/apis/admin.kcp.io/v1alpha1": Path not resolved to a valid virtual workspace`))
	if served, err := PermissionClaimPolicyServed(erroringDiscovery{err: unresolved}); err != nil || served {
		t.Fatalf("unresolved virtual workspace: served=%v err=%v", served, err)
	}

	// A denial from a kcp that DOES serve the group is a credential problem and
	// must not be read as absence: skipping the policy would leave the API
	// groups it reserves unreserved.
	denied := apierrors.NewForbidden(
		schema.GroupResource{Group: "admin.kcp.io", Resource: "permissionclaimpolicies"}, "",
		errors.New(`User "someone" cannot list resource "permissionclaimpolicies"`))
	if _, err := PermissionClaimPolicyServed(erroringDiscovery{err: denied}); err == nil {
		t.Fatal("an RBAC denial must propagate rather than look like an absent API")
	}
}

func TestInstallPermissionClaimPolicySkipsWhenTheAPIIsAbsent(t *testing.T) {
	permissionClaimPolicyAbsentOnce = sync.Once{}
	ctx, logs := contextWithLogs(t)
	dynamicClient := fakeDynamic()

	for range 3 {
		if err := InstallPermissionClaimPolicy(ctx, absentDiscovery(), dynamicClient); err != nil {
			t.Fatalf("InstallPermissionClaimPolicy: %v", err)
		}
	}

	// Nothing was written: today's kcp is untouched.
	for _, action := range dynamicClient.Actions() {
		if action.GetResource() == permissionClaimPolicyGVR {
			t.Fatalf("policy was applied against a cluster that does not serve the API: %#v", action)
		}
	}

	// And the operator was told exactly once, however many replicas or startup
	// retries reach this path.
	if got := strings.Count(logs(), "this kcp does not serve the API"); got != 1 {
		t.Fatalf("expected the skip to be logged once across 3 calls, logged %d times:\n%s", got, logs())
	}
}

func TestInstallPermissionClaimPolicyAppliesWhenTheAPIIsServed(t *testing.T) {
	ctx, _ := contextWithLogs(t)
	dynamicClient := fakeDynamic()

	if err := InstallPermissionClaimPolicy(ctx, servingDiscovery(), dynamicClient); err != nil {
		t.Fatalf("InstallPermissionClaimPolicy: %v", err)
	}

	want, err := permissionClaimPolicyObject()
	if err != nil {
		t.Fatalf("permissionClaimPolicyObject: %v", err)
	}
	got, err := dynamicClient.Resource(permissionClaimPolicyGVR).Get(ctx, want.GetName(), metav1.GetOptions{})
	if err != nil {
		t.Fatalf("policy was not created: %v", err)
	}
	if got.GetAPIVersion() != PermissionClaimPolicyGroupVersion || got.GetKind() != "PermissionClaimPolicy" {
		t.Fatalf("applied %s %s, want %s PermissionClaimPolicy", got.GetAPIVersion(), got.GetKind(), PermissionClaimPolicyGroupVersion)
	}

	claims, found, err := unstructured.NestedSlice(got.Object, "spec", "claims")
	if err != nil || !found || len(claims) == 0 {
		t.Fatalf("applied policy carries no spec.claims: found=%v err=%v", found, err)
	}
	subjects, found, err := unstructured.NestedSlice(got.Object, "spec", "providers")
	if err != nil || !found || len(subjects) == 0 {
		t.Fatalf("applied policy carries no spec.providers: found=%v err=%v", found, err)
	}
}

func TestInstallPermissionClaimPolicyIsIdempotent(t *testing.T) {
	ctx, _ := contextWithLogs(t)
	dynamicClient := fakeDynamic()

	// A sibling replica got there first with the same content.
	desired, err := permissionClaimPolicyObject()
	if err != nil {
		t.Fatalf("permissionClaimPolicyObject: %v", err)
	}
	if _, err := dynamicClient.Resource(permissionClaimPolicyGVR).Create(ctx, desired.DeepCopy(), metav1.CreateOptions{}); err != nil {
		t.Fatalf("seeding the existing policy: %v", err)
	}

	for range 2 {
		if err := InstallPermissionClaimPolicy(ctx, servingDiscovery(), dynamicClient); err != nil {
			t.Fatalf("InstallPermissionClaimPolicy: %v", err)
		}
	}

	list, err := dynamicClient.Resource(permissionClaimPolicyGVR).List(ctx, metav1.ListOptions{})
	if err != nil {
		t.Fatalf("listing policies: %v", err)
	}
	if len(list.Items) != 1 {
		t.Fatalf("expected exactly one policy, got %d", len(list.Items))
	}
}

func TestInstallPermissionClaimPolicyPropagatesDiscoveryFailures(t *testing.T) {
	ctx, _ := contextWithLogs(t)
	boom := errors.New("kcp is not up yet")
	err := InstallPermissionClaimPolicy(ctx, erroringDiscovery{err: boom}, fakeDynamic())
	if !errors.Is(err, boom) {
		t.Fatalf("a discovery failure must fail the install, got %v", err)
	}
}

// The embedded file is generated (hack/generate-permission-claim-policy.mjs).
// This is the hub's half of that contract: whatever the generator writes has to
// decode into the object the apply expects.
func TestEmbeddedPermissionClaimPolicyDecodes(t *testing.T) {
	policy, err := permissionClaimPolicyObject()
	if err != nil {
		t.Fatalf("permissionClaimPolicyObject: %v", err)
	}
	if policy.GetName() == "" {
		t.Fatal("the generated policy has no metadata.name")
	}
	claims, found, err := unstructured.NestedSlice(policy.Object, "spec", "claims")
	if err != nil || !found {
		t.Fatalf("spec.claims: found=%v err=%v", found, err)
	}
	for _, raw := range claims {
		rule, ok := raw.(map[string]any)
		if !ok {
			t.Fatalf("spec.claims entry is %T, want a mapping", raw)
		}
		if claimer, _ := rule["claimer"].(string); claimer == "" {
			t.Fatalf("spec.claims entry has no claimer: %#v", rule)
		}
		groups, _ := rule["groups"].([]any)
		if len(groups) == 0 {
			t.Fatalf("spec.claims entry has no groups: %#v", rule)
		}
	}
}

func TestPermissionClaimPolicyTargetConfigAddressesTheAdminVirtualWorkspace(t *testing.T) {
	if got := PermissionClaimPolicyTargetConfig(nil); got != nil {
		t.Fatalf("a nil kcp config must stay nil, got %#v", got)
	}
	// A PermissionClaimPolicy is installation-wide and is only writable through
	// the admin virtual workspace. Verified against kcp-dev/kcp#4388: a create
	// against <base>/services/admin/clusters/root is accepted with the hub's own
	// kcp admin credentials, and the object reads back unchanged.
	for name, host := range map[string]string{
		"plain base":           "https://kcp.example.com:6443",
		"already in a cluster": "https://kcp.example.com:6443/clusters/root:railgrid",
		"trailing slash":       "https://kcp.example.com:6443/",
	} {
		t.Run(name, func(t *testing.T) {
			in := &rest.Config{Host: host, BearerToken: "t"}
			out := PermissionClaimPolicyTargetConfig(in)
			if out == in {
				t.Fatal("the caller's config must not be shared with the apply path")
			}
			want := "https://kcp.example.com:6443" + AdminVirtualWorkspacePath
			if out.Host != want {
				t.Fatalf("target host = %q, want %q", out.Host, want)
			}
			if out.BearerToken != in.BearerToken {
				t.Fatalf("credentials were dropped: %+v", out)
			}
			if in.Host != host {
				t.Fatalf("the caller's config was mutated: %q", in.Host)
			}
		})
	}
}
