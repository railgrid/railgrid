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
	"errors"
	"net/http"
	"reflect"
	"sync"
	"testing"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/util/flowcontrol"

	"github.com/railgrid/provider-sdk/dataplane"

	asclient "github.com/railgrid/provider-app-studio/client"
)

type projectAssistantWorkerReadCall struct {
	cluster string
	limiter flowcontrol.RateLimiter
	client  dynamic.Interface
}

type projectAssistantWorkerReadCallerFactory struct {
	mu        sync.Mutex
	calls     []projectAssistantWorkerReadCall
	clientFor func(string, flowcontrol.RateLimiter) dynamic.Interface
}

type projectAssistantWorkerReadContextCallerFactory struct {
	*projectAssistantWorkerReadCallerFactory
	observedContextValue any
}

type projectAssistantWorkerReadContextKey struct{}

func (factory *projectAssistantWorkerReadContextCallerFactory) AsProviderWithRateLimiterContext(ctx context.Context, clusterID string, limiter flowcontrol.RateLimiter) (dynamic.Interface, error) {
	factory.observedContextValue = ctx.Value(projectAssistantWorkerReadContextKey{})
	return factory.AsProviderWithRateLimiter(clusterID, limiter)
}

func (*projectAssistantWorkerReadCallerFactory) For(string, string) (dynamic.Interface, error) {
	return nil, errors.New("unexpected caller credential client")
}

func (factory *projectAssistantWorkerReadCallerFactory) AsProvider(clusterID string) (dynamic.Interface, error) {
	return factory.AsProviderWithRateLimiter(clusterID, nil)
}

func (factory *projectAssistantWorkerReadCallerFactory) AsProviderWithRateLimiter(clusterID string, limiter flowcontrol.RateLimiter) (dynamic.Interface, error) {
	var dynamicClient dynamic.Interface
	if factory.clientFor != nil {
		dynamicClient = factory.clientFor(clusterID, limiter)
		if dynamicClient == nil {
			return nil, errors.New("test provider client factory returned nil")
		}
	} else {
		dynamicClient = dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())
	}
	factory.mu.Lock()
	factory.calls = append(factory.calls, projectAssistantWorkerReadCall{cluster: clusterID, limiter: limiter, client: dynamicClient})
	factory.mu.Unlock()
	return dynamicClient, nil
}

func (*projectAssistantWorkerReadCallerFactory) ExportVerbURL(context.Context, schema.GroupVersionResource, dataplane.Request) (string, error) {
	return "", errors.New("unexpected cross-provider verb URL")
}

func (*projectAssistantWorkerReadCallerFactory) ProviderHTTPClient() (*http.Client, error) {
	return nil, errors.New("unexpected provider HTTP client")
}

func (factory *projectAssistantWorkerReadCallerFactory) snapshot() []projectAssistantWorkerReadCall {
	factory.mu.Lock()
	defer factory.mu.Unlock()
	return append([]projectAssistantWorkerReadCall(nil), factory.calls...)
}

type projectAssistantWorkerReadFallbackCallerFactory struct{}

func (*projectAssistantWorkerReadFallbackCallerFactory) For(string, string) (dynamic.Interface, error) {
	return nil, errors.New("unexpected caller credential client")
}

func (*projectAssistantWorkerReadFallbackCallerFactory) AsProvider(string) (dynamic.Interface, error) {
	return nil, errors.New("unexpected provider client")
}

func (*projectAssistantWorkerReadFallbackCallerFactory) ExportVerbURL(context.Context, schema.GroupVersionResource, dataplane.Request) (string, error) {
	return "", errors.New("unexpected cross-provider verb URL")
}

func (*projectAssistantWorkerReadFallbackCallerFactory) ProviderHTTPClient() (*http.Client, error) {
	return nil, errors.New("unexpected provider HTTP client")
}

func TestProjectAssistantWorkerReadClientsShareServerBudgetAndKeepWorkspaceScopes(t *testing.T) {
	factory := &projectAssistantWorkerReadCallerFactory{}
	server := &Server{callers: factory}
	caller := &dataplane.ProxiedIdentity{
		User:   "alice",
		Groups: []string{"developers"},
		Extra:  map[string][]string{"example.dev/team": {"blue"}},
	}
	gateClient := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())
	idA := identity{clusterID: "tenant-a", user: "alice", caller: caller, provider: gateClient}
	idB := identity{clusterID: "tenant-b", user: "alice", caller: caller, provider: gateClient}
	fallbackA := asclient.NewFromDynamic(dynamicfake.NewSimpleDynamicClient(runtime.NewScheme()))
	fallbackB := asclient.NewFromDynamic(dynamicfake.NewSimpleDynamicClient(runtime.NewScheme()))

	workerA, err := server.projectAssistantWorkerReadClient(context.Background(), idA, fallbackA)
	if err != nil {
		t.Fatal(err)
	}
	workerB, err := server.projectAssistantWorkerReadClient(context.Background(), idB, fallbackB)
	if err != nil {
		t.Fatal(err)
	}
	if workerA == fallbackA || workerB == fallbackB || workerA.Dynamic() == workerB.Dynamic() {
		t.Fatal("each worker should receive its own client, distinct from the request client")
	}
	if idA.provider != gateClient || idB.provider != gateClient || idA.caller != caller || idB.caller != caller || idA.user != "alice" || idB.user != "alice" {
		t.Fatal("deriving worker read clients changed the Gate identity or caller")
	}

	calls := factory.snapshot()
	if len(calls) != 2 || calls[0].cluster != "tenant-a" || calls[1].cluster != "tenant-b" {
		t.Fatalf("provider worker scopes = %#v", calls)
	}
	if calls[0].limiter == nil || calls[0].limiter != calls[1].limiter {
		t.Fatalf("workers did not share one server-wide limiter: %#v", calls)
	}
	if got := calls[0].limiter.QPS(); got != projectAssistantWorkerReadQPS {
		t.Fatalf("worker read QPS = %v, want %d", got, projectAssistantWorkerReadQPS)
	}
	for token := 0; token < projectAssistantWorkerReadBurst; token++ {
		if !calls[0].limiter.TryAccept() {
			t.Fatalf("shared worker budget stopped before burst token %d", token+1)
		}
	}
	if calls[1].limiter.TryAccept() {
		t.Fatal("shared worker budget exceeded its configured burst")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := calls[1].limiter.Wait(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled worker read wait = %v, want context.Canceled", err)
	}

	var reviewed identity
	server.integrationAccessReviewer = func(_ context.Context, id identity, _ dataplane.ResourceAttributes) (bool, error) {
		reviewed = id
		return true, nil
	}
	allowed, err := server.authorizeCaller(context.Background(), idA, dataplane.ResourceAttributes{Verb: "get"})
	if err != nil || !allowed {
		t.Fatalf("fresh caller review = %v, %v", allowed, err)
	}
	if reviewed.provider != gateClient || reviewed.caller != caller || reviewed.clusterID != idA.clusterID || !reflect.DeepEqual(reviewed.caller.Groups, []string{"developers"}) {
		t.Fatalf("caller review did not retain request identity and Gate client: %#v", reviewed)
	}
}

func TestProjectAssistantWorkerReadBudgetIsServerLocalAndFallsBackWithoutFactorySupport(t *testing.T) {
	first := &Server{callers: &projectAssistantWorkerReadCallerFactory{}}
	second := &Server{callers: &projectAssistantWorkerReadCallerFactory{}}
	firstLimiter := first.assistantWorkerReadBudget.rateLimiter()
	secondLimiter := second.assistantWorkerReadBudget.rateLimiter()
	if firstLimiter == secondLimiter {
		t.Fatal("separate Servers shared one worker read budget")
	}

	fallback := asclient.NewFromDynamic(dynamicfake.NewSimpleDynamicClient(runtime.NewScheme()))
	server := &Server{callers: &projectAssistantWorkerReadFallbackCallerFactory{}}
	got, err := server.projectAssistantWorkerReadClient(context.Background(), identity{clusterID: "tenant-a"}, fallback)
	if err != nil {
		t.Fatal(err)
	}
	if got != fallback {
		t.Fatal("factory without shared-limiter support should preserve the existing request client")
	}
	got, err = server.projectAssistantWorkerReadClient(context.Background(), identity{}, fallback)
	if err != nil || got != fallback {
		t.Fatalf("unsupported worker factory with no cluster ID should preserve fallback: client=%v, err=%v", got == fallback, err)
	}
	currentProjectFallback := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())
	currentProjectClient, err := server.projectAssistantCurrentProjectReadClient(context.Background(), identity{}, currentProjectFallback)
	if err != nil || currentProjectClient != currentProjectFallback {
		t.Fatalf("unsupported current Project factory with no cluster ID should preserve fallback: client=%v, err=%v", currentProjectClient == currentProjectFallback, err)
	}
	seamFactory := &projectAssistantWorkerReadCallerFactory{}
	seamServer := &Server{
		callers: seamFactory,
		projectClientFor: func(identity) (*asclient.Client, error) {
			return fallback, nil
		},
	}
	got, err = seamServer.projectAssistantWorkerReadClient(context.Background(), identity{clusterID: "tenant-a"}, fallback)
	if err != nil || got != fallback || len(seamFactory.snapshot()) != 0 {
		t.Fatalf("projectClientFor test seam fallback = %v, %v; factory calls=%d", got == fallback, err, len(seamFactory.snapshot()))
	}
}

func TestProjectAssistantWorkerReadClientPassesRequestContextToContextAwareFactory(t *testing.T) {
	factory := &projectAssistantWorkerReadContextCallerFactory{
		projectAssistantWorkerReadCallerFactory: &projectAssistantWorkerReadCallerFactory{},
	}
	server := &Server{callers: factory}
	fallback := asclient.NewFromDynamic(dynamicfake.NewSimpleDynamicClient(runtime.NewScheme()))
	ctx := context.WithValue(context.Background(), projectAssistantWorkerReadContextKey{}, "request-context")

	got, err := server.projectAssistantWorkerReadClient(ctx, identity{clusterID: "tenant-a"}, fallback)
	if err != nil {
		t.Fatal(err)
	}
	if got == fallback {
		t.Fatal("context-aware factory was not used")
	}
	if factory.observedContextValue != "request-context" {
		t.Fatalf("factory observed context value = %v, want request-context", factory.observedContextValue)
	}
	if calls := factory.snapshot(); len(calls) != 1 || calls[0].cluster != "tenant-a" || calls[0].limiter == nil {
		t.Fatalf("context-aware factory call = %#v", calls)
	}

	provider, err := server.projectAssistantCurrentProjectReadClient(ctx, identity{clusterID: "tenant-b"}, dynamicfake.NewSimpleDynamicClient(runtime.NewScheme()))
	if err != nil || provider == nil {
		t.Fatalf("current Project client = %v, %v", provider, err)
	}
	if factory.observedContextValue != "request-context" {
		t.Fatalf("current Project factory observed context value = %v, want request-context", factory.observedContextValue)
	}
}
