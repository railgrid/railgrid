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

package api

import (
	"context"
	"reflect"
	"sync"
	"testing"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/util/flowcontrol"

	"github.com/railgrid/provider-sdk/dataplane"
)

type integrationDiscoveryBudgetCall struct {
	clusterID string
	limiter   flowcontrol.RateLimiter
}

type integrationDiscoveryBudgetTestCallers struct {
	*testCallers

	mu    sync.Mutex
	calls []integrationDiscoveryBudgetCall
}

type integrationDiscoveryContextTestCallers struct {
	*integrationDiscoveryBudgetTestCallers
	observedContextValue any
}

type integrationDiscoveryContextKey struct{}

func (callers *integrationDiscoveryContextTestCallers) AsProviderWithRateLimiterContext(ctx context.Context, clusterID string, limiter flowcontrol.RateLimiter) (dynamic.Interface, error) {
	callers.observedContextValue = ctx.Value(integrationDiscoveryContextKey{})
	return callers.AsProviderWithRateLimiter(clusterID, limiter)
}

func (callers *integrationDiscoveryBudgetTestCallers) AsProviderWithRateLimiter(clusterID string, limiter flowcontrol.RateLimiter) (dynamic.Interface, error) {
	callers.mu.Lock()
	callers.calls = append(callers.calls, integrationDiscoveryBudgetCall{clusterID: clusterID, limiter: limiter})
	callers.mu.Unlock()
	return fake.NewSimpleDynamicClient(runtime.NewScheme()), nil
}

func (callers *integrationDiscoveryBudgetTestCallers) snapshot() []integrationDiscoveryBudgetCall {
	callers.mu.Lock()
	defer callers.mu.Unlock()
	return append([]integrationDiscoveryBudgetCall(nil), callers.calls...)
}

func TestAutomaticIntegrationDiscoveryRateLimiterIsSharedPerServer(t *testing.T) {
	callers := &integrationDiscoveryBudgetTestCallers{testCallers: newTestCallers(nil, "")}
	server := &Server{callers: callers}

	callerProof := &dataplane.ProxiedIdentity{
		User:   "alice@example.test",
		Groups: []string{"developers"},
		Extra:  map[string][]string{"example.dev/team": {"blue"}},
	}
	first := identity{
		tenant: "cluster-a", clusterID: "cluster-a", workspacePath: "root:railgrid:tenants:org-a:team-a",
		orgUUID: "org-a", workspaceUUID: "team-a", user: callerProof.User, userLabel: "Alice",
		caller: callerProof, actionProof: "action-proof-a", provider: fake.NewSimpleDynamicClient(runtime.NewScheme()),
	}
	firstReview, err := server.integrationDiscoveryIdentity(context.Background(), first)
	if err != nil {
		t.Fatalf("first review identity: %v", err)
	}
	if firstReview.provider == first.provider {
		t.Fatal("review identity retained the original caller provider client")
	}
	wantFirst := first
	wantFirst.provider = firstReview.provider
	if !reflect.DeepEqual(firstReview, wantFirst) {
		t.Fatalf("first review identity changed caller proof or tenant scope: got %#v, want %#v", firstReview, wantFirst)
	}

	second := identity{
		tenant: "cluster-b", clusterID: "cluster-b", workspacePath: "root:railgrid:tenants:org-b:team-b",
		orgUUID: "org-b", workspaceUUID: "team-b", user: "bob@example.test",
		caller:      &dataplane.ProxiedIdentity{User: "bob@example.test", Groups: []string{"operators"}},
		actionProof: "action-proof-b",
	}
	secondReview, err := server.integrationDiscoveryIdentity(context.Background(), second)
	if err != nil {
		t.Fatalf("second review identity: %v", err)
	}
	wantSecond := second
	wantSecond.provider = secondReview.provider
	if !reflect.DeepEqual(secondReview, wantSecond) {
		t.Fatalf("second review identity changed caller proof or tenant scope: got %#v, want %#v", secondReview, wantSecond)
	}

	calls := callers.snapshot()
	if len(calls) != 2 {
		t.Fatalf("provider review clients = %d, want 2", len(calls))
	}
	if calls[0].clusterID != first.clusterID || calls[1].clusterID != second.clusterID {
		t.Fatalf("review client clusters = %#v, want %q and %q", calls, first.clusterID, second.clusterID)
	}
	limiter := calls[0].limiter
	if limiter == nil {
		t.Fatal("first review client received no shared limiter")
	}
	if calls[1].limiter != limiter {
		t.Fatal("review clients from separate turns/tenants did not receive the same Server limiter")
	}
	if got := limiter.QPS(); got != float32(integrationDiscoveryQPS) {
		t.Fatalf("shared discovery QPS = %v, want %d", got, integrationDiscoveryQPS)
	}
	for token := 0; token < integrationDiscoveryBurst; token++ {
		if !limiter.TryAccept() {
			t.Fatalf("shared discovery limiter stopped before burst token %d", token+1)
		}
	}
	if limiter.TryAccept() {
		t.Fatalf("shared discovery limiter accepted more than its configured burst of %d", integrationDiscoveryBurst)
	}

	otherCallers := &integrationDiscoveryBudgetTestCallers{testCallers: newTestCallers(nil, "")}
	otherServer := &Server{callers: otherCallers}
	if _, err := otherServer.integrationDiscoveryIdentity(context.Background(), first); err != nil {
		t.Fatalf("other Server review identity: %v", err)
	}
	otherCalls := otherCallers.snapshot()
	if len(otherCalls) != 1 || otherCalls[0].limiter == nil {
		t.Fatalf("other Server review clients = %#v, want one independent limiter", otherCalls)
	}
	if otherCalls[0].limiter == limiter {
		t.Fatal("separate Server instances unexpectedly share a discovery limiter")
	}
	if got := otherCalls[0].limiter.QPS(); got != float32(integrationDiscoveryQPS) {
		t.Fatalf("other Server discovery QPS = %v, want %d", got, integrationDiscoveryQPS)
	}
}

func TestAutomaticIntegrationDiscoveryPassesRequestContextToContextAwareFactory(t *testing.T) {
	legacyFactory := &integrationDiscoveryBudgetTestCallers{testCallers: newTestCallers(nil, "")}
	factory := &integrationDiscoveryContextTestCallers{integrationDiscoveryBudgetTestCallers: legacyFactory}
	server := &Server{callers: factory}
	ctx := context.WithValue(context.Background(), integrationDiscoveryContextKey{}, "discovery-context")
	_, err := server.integrationDiscoveryIdentity(ctx, identity{clusterID: "tenant-a"})
	if err != nil {
		t.Fatal(err)
	}
	if factory.observedContextValue != "discovery-context" {
		t.Fatalf("factory observed context value = %v, want discovery-context", factory.observedContextValue)
	}
	calls := legacyFactory.snapshot()
	if len(calls) != 1 || calls[0].clusterID != "tenant-a" || calls[0].limiter == nil {
		t.Fatalf("context-aware discovery factory call = %#v", calls)
	}
}
