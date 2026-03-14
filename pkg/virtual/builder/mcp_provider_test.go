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

package builder

import (
	"context"
	"testing"
)

// newSingleEdgeProvider creates a KedgeEdgeProvider for a single edge with the
// given ConnManager state.
func newSingleEdgeProvider(cluster, edgeName string, cm *ConnManager) *KedgeEdgeProvider {
	return &KedgeEdgeProvider{
		cluster:         cluster,
		edgeName:        edgeName,
		edgeConnManager: cm,
		edgeProxyBase:   "https://kedge.example.com/services/edges-proxy",
		bearerToken:     "test-token",
	}
}

// TestGetTargets_returnsEdgeWhenConnected verifies that GetTargets returns the
// single fixed edgeName when its tunnel is registered in ConnManager.
func TestGetTargets_returnsEdgeWhenConnected(t *testing.T) {
	const (
		cluster  = "root:kedge:user-default"
		edgeName = "my-edge"
	)

	cm := NewConnManager()
	cm.Store(edgeConnKey(cluster, edgeName), nil)

	provider := newSingleEdgeProvider(cluster, edgeName, cm)

	targets, err := provider.GetTargets(context.Background())
	if err != nil {
		t.Fatalf("GetTargets returned unexpected error: %v", err)
	}

	if len(targets) != 1 {
		t.Fatalf("expected 1 target, got %d: %v", len(targets), targets)
	}
	if targets[0] != edgeName {
		t.Errorf("expected target %q, got %q", edgeName, targets[0])
	}
}

// TestGetTargets_emptyWhenNotConnected verifies that GetTargets returns an empty
// slice when the edge's tunnel is not registered in ConnManager.
func TestGetTargets_emptyWhenNotConnected(t *testing.T) {
	const (
		cluster  = "root:kedge:user-default"
		edgeName = "my-edge"
	)

	cm := NewConnManager() // empty — no tunnels

	provider := newSingleEdgeProvider(cluster, edgeName, cm)

	targets, err := provider.GetTargets(context.Background())
	if err != nil {
		t.Fatalf("GetTargets returned unexpected error: %v", err)
	}

	if len(targets) != 0 {
		t.Fatalf("expected 0 targets, got %d: %v", len(targets), targets)
	}
}

// TestGetTargets_ignoresOtherEdges verifies that only the provider's own edge
// is returned even if other edges are connected in ConnManager.
func TestGetTargets_ignoresOtherEdges(t *testing.T) {
	const (
		cluster     = "root:kedge:user-default"
		edgeName    = "my-edge"
		anotherEdge = "other-edge"
	)

	cm := NewConnManager()
	// Register a different edge but NOT the provider's edge.
	cm.Store(edgeConnKey(cluster, anotherEdge), nil)

	provider := newSingleEdgeProvider(cluster, edgeName, cm)

	targets, err := provider.GetTargets(context.Background())
	if err != nil {
		t.Fatalf("GetTargets returned unexpected error: %v", err)
	}

	if len(targets) != 0 {
		t.Fatalf("expected 0 targets (own edge not connected), got %d: %v", len(targets), targets)
	}
}

// TestGetDerivedKubernetes_correctURL verifies that GetDerivedKubernetes builds a
// Kubernetes client whose Host matches the expected edges-proxy URL format.
func TestGetDerivedKubernetes_correctURL(t *testing.T) {
	const (
		cluster       = "root:kedge:user-default"
		edgeName      = "myedge"
		edgeProxyBase = "https://kedge.example.com/services/edges-proxy"
		bearerToken   = "user-bearer-token"
	)

	cm := NewConnManager()

	provider := &KedgeEdgeProvider{
		cluster:         cluster,
		edgeName:        edgeName,
		edgeConnManager: cm,
		edgeProxyBase:   edgeProxyBase,
		bearerToken:     bearerToken,
	}

	k8s, err := provider.GetDerivedKubernetes(context.Background(), edgeName)
	if err != nil {
		t.Fatalf("GetDerivedKubernetes returned unexpected error: %v", err)
	}
	if k8s == nil {
		t.Fatal("GetDerivedKubernetes returned nil Kubernetes")
	}

	// The expected URL matches the format described in KedgeEdgeProvider.GetDerivedKubernetes.
	expectedURL := edgeProxyBase + "/clusters/" + cluster + "/apis/kedge.faros.sh/v1alpha1/edges/" + edgeName + "/k8s"

	// Retrieve the REST config from the Kubernetes client to inspect Host.
	restCfg := k8s.RESTConfig()
	if restCfg == nil {
		t.Fatal("k8s.RESTConfig() returned nil")
	}

	if restCfg.Host != expectedURL {
		t.Errorf("expected Host to be %q, got %q", expectedURL, restCfg.Host)
	}
}

// TestGetDefaultTarget asserts that GetDefaultTarget returns the fixed edge name.
func TestGetDefaultTarget(t *testing.T) {
	provider := &KedgeEdgeProvider{edgeName: "my-edge"}
	if got := provider.GetDefaultTarget(); got != "my-edge" {
		t.Errorf("GetDefaultTarget() = %q; want %q", got, "my-edge")
	}
}

// TestGetTargetParameterName asserts that GetTargetParameterName returns "cluster".
func TestGetTargetParameterName(t *testing.T) {
	provider := &KedgeEdgeProvider{}
	if got := provider.GetTargetParameterName(); got != "cluster" {
		t.Errorf("GetTargetParameterName() = %q; want %q", got, "cluster")
	}
}
