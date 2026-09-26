// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package conformance

import (
	"fmt"
	"sync"

	"github.com/railgrid/provider-sdk/dataplane"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"
)

// ForeignCluster is a logical-cluster ID the suite addresses to prove that a
// caller's standing in one workspace reaches nothing in another. A fixture
// must not use it as its own Cluster.
const ForeignCluster = "0conformanceforei"

// StrangerUser is the identity the suite stamps onto a request that must be
// denied: a real, authenticated user who simply cannot see the object. A
// fixture's Allow must not grant it anything (the fake refuses it before
// Allow is consulted).
const StrangerUser = "conformance-stranger@railgrid.test"

// Attributes are the resourceAttributes of one access review, plus the user
// it was asked about.
type Attributes struct {
	User        string
	Group       string
	Version     string
	Resource    string
	Subresource string
	Name        string
	Verb        string
}

// FakeCallers is a dataplane.ProviderCallerFactory for tests.
//
// AsProvider(Cluster) sees Objects; AsProvider(any other cluster) sees
// nothing, so a request for a foreign workspace is denied for want of the
// object. Every SubjectAccessReview the provider client creates is answered by
// Allow — for User only; StrangerUser and any other identity are refused
// outright — so a test states its grants as a function of the attributes kcp
// would see. For (the MCP class) still answers a caller-credentialed client
// for (Cluster, Token), for MCP tools under test.
type FakeCallers struct {
	// Cluster is the one tenant workspace the fake knows.
	Cluster string
	// User is the caller the suite stamps on a granted request.
	User string
	// Token is the one bearer For accepts (MCP class only).
	Token string
	// Objects are what the provider (and, for MCP, the caller) can read in
	// Cluster.
	Objects []*unstructured.Unstructured
	// ListKinds maps each resource the handler may list to its List kind, as
	// the dynamic fake requires.
	ListKinds map[schema.GroupVersionResource]string
	// Allow decides every access review about User. Nil grants nothing.
	Allow func(Attributes) bool

	mu      sync.Mutex
	clients map[string]dynamic.Interface
}

var _ dataplane.ProviderCallerFactory = (*FakeCallers)(nil)

// AsProvider implements dataplane.ProviderCallerFactory.
func (f *FakeCallers) AsProvider(clusterID string) (dynamic.Interface, error) {
	if f == nil {
		return nil, fmt.Errorf("conformance: fake caller factory is unavailable")
	}
	if !dataplane.IsClusterID(clusterID) {
		return nil, fmt.Errorf("conformance: %q is not a kcp logical-cluster ID", clusterID)
	}
	return f.client("provider\x00"+clusterID, clusterID == f.Cluster, true), nil
}

// For implements dataplane.CallerFactory, for MCP tools that still act with
// the caller's own bearer.
func (f *FakeCallers) For(clusterID, token string) (dynamic.Interface, error) {
	if f == nil {
		return nil, fmt.Errorf("conformance: fake caller factory is unavailable")
	}
	if !dataplane.IsClusterID(clusterID) {
		return nil, fmt.Errorf("conformance: %q is not a kcp logical-cluster ID", clusterID)
	}
	if token == "" {
		return nil, fmt.Errorf("%w: cannot act on the tenant's behalf", dataplane.ErrNoBearer)
	}
	return f.client("caller\x00"+clusterID+"\x00"+token, clusterID == f.Cluster && token == f.Token, false), nil
}

func (f *FakeCallers) client(key string, tenant, provider bool) dynamic.Interface {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.clients == nil {
		f.clients = map[string]dynamic.Interface{}
	}
	if client, ok := f.clients[key]; ok {
		return client
	}
	sar := dataplane.SubjectAccessReviews()
	ssar := dataplane.SelfSubjectAccessReviews()
	listKinds := map[schema.GroupVersionResource]string{
		sar:  "SubjectAccessReviewList",
		ssar: "SelfSubjectAccessReviewList",
	}
	for gvr, kind := range f.ListKinds {
		listKinds[gvr] = kind
	}
	var objects []runtime.Object
	if tenant {
		for _, object := range f.Objects {
			objects = append(objects, object.DeepCopy())
		}
	}
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), listKinds, objects...)
	answer := func(action k8stesting.Action, self bool) (bool, runtime.Object, error) {
		create, ok := action.(k8stesting.CreateAction)
		if !ok {
			return false, nil, nil
		}
		review, ok := create.GetObject().(*unstructured.Unstructured)
		if !ok {
			return false, nil, nil
		}
		attributes, _, _ := unstructured.NestedStringMap(review.Object, "spec", "resourceAttributes")
		user := f.User
		if !self {
			user, _, _ = unstructured.NestedString(review.Object, "spec", "user")
		}
		allowed := false
		if tenant && f.Allow != nil && user == f.User && (provider || !self || true) {
			allowed = f.Allow(Attributes{
				User:        user,
				Group:       attributes["group"],
				Version:     attributes["version"],
				Resource:    attributes["resource"],
				Subresource: attributes["subresource"],
				Name:        attributes["name"],
				Verb:        attributes["verb"],
			})
		}
		answered := review.DeepCopy()
		_ = unstructured.SetNestedField(answered.Object, allowed, "status", "allowed")
		return true, answered, nil
	}
	client.PrependReactor("create", sar.Resource, func(action k8stesting.Action) (bool, runtime.Object, error) {
		return answer(action, false)
	})
	client.PrependReactor("create", ssar.Resource, func(action k8stesting.Action) (bool, runtime.Object, error) {
		return answer(action, true)
	})
	f.clients[key] = client
	return client
}
