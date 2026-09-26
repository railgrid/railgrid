// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package dataplane

import (
	"context"
	"errors"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"
)

// providerOnly is a ProviderCallerFactory whose bearer path is unreachable, so
// the test proves the proxied gate never needs a caller credential.
type providerOnly struct {
	client dynamic.Interface
	asked  int
}

func (p *providerOnly) For(string, string) (dynamic.Interface, error) {
	return nil, errors.New("For must not be called on the subresource path")
}

func (p *providerOnly) AsProvider(clusterID string) (dynamic.Interface, error) {
	p.asked++
	if clusterID != "1v98kgkp03uox9qw" {
		return nil, errors.New("wrong cluster")
	}
	return p.client, nil
}

func proxiedGateFixture(t *testing.T, allow bool) (*providerOnly, schema.GroupVersionResource) {
	t.Helper()
	gvr := schema.GroupVersionResource{Group: "edges.railgrid.ai", Version: "v1alpha1", Resource: "linuxservers"}
	edge := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "edges.railgrid.ai/v1alpha1", "kind": "LinuxServer",
		"metadata": map[string]any{"name": "edge-1", "uid": "edge-uid"},
	}}
	scheme := runtime.NewScheme()
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, map[schema.GroupVersionResource]string{
		gvr: "LinuxServerList",
	}, edge)
	// A SubjectAccessReview is answered by the fake as the review it was sent,
	// plus the verdict; the test also captures the subject it was asked about.
	client.PrependReactor("create", "subjectaccessreviews", func(action k8stesting.Action) (bool, runtime.Object, error) {
		obj := action.(k8stesting.CreateAction).GetObject().(*unstructured.Unstructured).DeepCopy()
		user, _, _ := unstructured.NestedString(obj.Object, "spec", "user")
		verb, _, _ := unstructured.NestedString(obj.Object, "spec", "resourceAttributes", "verb")
		if user != "alice" || verb != "get" {
			t.Errorf("access review asked about user=%q verb=%q, want alice/get", user, verb)
		}
		_ = unstructured.SetNestedField(obj.Object, allow, "status", "allowed")
		return true, obj, nil
	})
	return &providerOnly{client: client}, gvr
}

func TestProxiedGateDecidesVisibilityByAccessReviewAndReadsAsTheProvider(t *testing.T) {
	factory, gvr := proxiedGateFixture(t, true)
	ctx := WithProxiedIdentity(context.Background(), ProxiedIdentity{User: "alice", Groups: []string{"system:authenticated"}})
	req := Request{ClusterID: "1v98kgkp03uox9qw", Resource: "linuxservers", Name: "edge-1", Verb: "addon-credentials"}

	object, client, err := Gate(ctx, factory, gvr, req)
	if err != nil {
		t.Fatalf("Gate: %v", err)
	}
	if object.GetUID() != "edge-uid" || client == nil {
		t.Fatalf("object = %v, client = %v", object, client)
	}
	if factory.asked != 1 {
		t.Fatalf("AsProvider called %d times", factory.asked)
	}
}

func TestProxiedGateRefusesACallerWhoCannotSeeTheParent(t *testing.T) {
	factory, gvr := proxiedGateFixture(t, false)
	ctx := WithProxiedIdentity(context.Background(), ProxiedIdentity{User: "alice"})
	req := Request{ClusterID: "1v98kgkp03uox9qw", Resource: "linuxservers", Name: "edge-1", Verb: "addon-credentials"}
	if _, _, err := Gate(ctx, factory, gvr, req); !errors.Is(err, ErrDenied) {
		t.Fatalf("err = %v, want ErrDenied", err)
	}
}

var _ = metav1.Now // keep the import honest for the fixture's object metadata

// A foreign provider — a ServiceAccount from another logical cluster, forwarded
// through its own APIExport virtual workspace — is authorized by the claim kcp
// already enforced; the gate reads the parent as the provider and asks no
// SubjectAccessReview, which would refuse an identity with no RBAC here.
func TestProxiedGateTrustsTheClaimForAForeignProvider(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "edges.railgrid.ai", Version: "v1alpha1", Resource: "linuxservers"}
	edge := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "edges.railgrid.ai/v1alpha1", "kind": "LinuxServer",
		"metadata": map[string]any{"name": "edge-1", "uid": "edge-uid"},
	}}
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{gvr: "LinuxServerList"}, edge)
	client.PrependReactor("create", "subjectaccessreviews", func(k8stesting.Action) (bool, runtime.Object, error) {
		t.Fatal("a foreign provider's call must not be re-authorized by SubjectAccessReview; kcp enforced its claim")
		return true, nil, nil
	})
	factory := &providerOnly{client: client}
	req := Request{ClusterID: "1v98kgkp03uox9qw", Resource: "linuxservers", Name: "edge-1", Verb: "proxy"}
	identity := ProxiedIdentity{
		User:   "system:serviceaccount:default:provider",
		Groups: []string{"system:serviceaccounts", "system:authenticated"},
		Extra:  map[string][]string{ClusterNameExtra: {"3l7xqpgvrz4pwkbx"}, "authorization.kcp.io/warrant": {"{}"}},
	}
	object, cl, err := Gate(WithProxiedIdentity(context.Background(), identity), factory, gvr, req)
	if err != nil {
		t.Fatalf("Gate: %v", err)
	}
	if object.GetName() != "edge-1" || cl == nil {
		t.Fatalf("expected the parent read as the provider, got %v", object)
	}

	// The tenant's own ServiceAccount is not foreign: it goes through the
	// review like any other caller (and here the fake refuses it loudly).
	own := identity
	own.Extra = map[string][]string{ClusterNameExtra: {"1v98kgkp03uox9qw"}}
	if !own.IsForeignProvider(req.ClusterID) == false {
		t.Fatal("a ServiceAccount from the addressed cluster must not be treated as a foreign provider")
	}
}
