// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

// Package conformance holds the data-plane contract's shared test suite and
// the fake it runs against.
//
// It is separate from provider-sdk/dataplane on purpose: a provider binary
// links the server-kit, and the server-kit must not drag "testing" or the
// client-go fakes in with it. Everything here is imported from _test.go files
// only.
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

// ForeignCluster is the stand-in for "some other tenant's workspace" in the
// foreign-cluster check. It is a valid logical-cluster ID that no fixture
// should also use as its own cluster.
const ForeignCluster = "0conformanceforei"

// Attributes are the SelfSubjectAccessReview attributes gate 2 asks about.
// FakeCallers hands them to its Allow func so a test can grant one verb and
// refuse another without standing up an API server.
type Attributes struct {
	Group       string
	Version     string
	Resource    string
	Subresource string
	Name        string
	Verb        string
}

// FakeCallers is a dataplane.CallerFactory for tests: a provider builds its
// handler with one of these instead of the real dataplane.Callers, then hands
// it to Test.
//
// Exactly one (Cluster, Token) pair is real. For any other cluster or token
// the returned client sees no objects and allows nothing, which is what makes
// "a token for workspace A cannot reach workspace B" observable without two
// live workspaces: gate 1 answers NotFound and the handler denies.
type FakeCallers struct {
	// Cluster is the logical cluster whose objects exist.
	Cluster string
	// Token is the only bearer treated as the tenant's caller.
	Token string
	// Objects are visible in Cluster to Token.
	Objects []*unstructured.Unstructured
	// ListKinds maps each object's GVR to its list kind, as the dynamic fake
	// requires. The SelfSubjectAccessReview mapping is added automatically.
	ListKinds map[schema.GroupVersionResource]string
	// Allow decides gate 2. A nil Allow refuses everything.
	Allow func(Attributes) bool

	mu      sync.Mutex
	clients map[string]dynamic.Interface
}

var _ dataplane.CallerFactory = (*FakeCallers)(nil)

// For implements dataplane.CallerFactory.
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

	f.mu.Lock()
	defer f.mu.Unlock()
	key := clusterID + "\x00" + token
	if f.clients == nil {
		f.clients = map[string]dynamic.Interface{}
	}
	if client, ok := f.clients[key]; ok {
		return client, nil
	}

	tenant := clusterID == f.Cluster && token == f.Token
	ssar := dataplane.SelfSubjectAccessReviews()

	listKinds := map[schema.GroupVersionResource]string{ssar: "SelfSubjectAccessReviewList"}
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
	client.PrependReactor("create", ssar.Resource, func(action k8stesting.Action) (bool, runtime.Object, error) {
		create, ok := action.(k8stesting.CreateAction)
		if !ok {
			return false, nil, nil
		}
		review, ok := create.GetObject().(*unstructured.Unstructured)
		if !ok {
			return false, nil, nil
		}
		attributes, _, _ := unstructured.NestedStringMap(review.Object, "spec", "resourceAttributes")
		allowed := false
		if tenant && f.Allow != nil {
			allowed = f.Allow(Attributes{
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
	})

	f.clients[key] = client
	return client, nil
}
