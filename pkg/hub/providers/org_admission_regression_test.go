// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package providers

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	clienttesting "k8s.io/client-go/testing"

	"github.com/railgrid/railgrid/pkg/kcppaths"
)

func TestBootstrapAdmissionReservesCapacityForOtherOrganizations(t *testing.T) {
	limiter := newOrgBootstrapAdmission()
	releaseA, ok := limiter.acquire("a")
	if !ok {
		t.Fatal("first request rejected")
	}
	defer releaseA()
	releaseA2, ok := limiter.acquire("a")
	if !ok {
		t.Fatal("second request rejected")
	}
	defer releaseA2()
	if _, ok := limiter.acquire("a"); ok {
		t.Fatal("one org exceeded its budget")
	}
	releaseB, ok := limiter.acquire("b")
	if !ok {
		t.Fatal("org a starved org b")
	}
	releaseB()
	releaseB() // Safe even when a cancellation and completion both release.
	if _, exists := limiter.organizations["b"]; exists {
		t.Fatal("idle org retained")
	}
	releases := []func(){}
	for _, org := range []string{"c", "d", "e", "f", "g", "h"} {
		release, ok := limiter.acquire(org)
		if !ok {
			t.Fatal("global capacity exhausted too soon")
		}
		releases = append(releases, release)
	}
	if _, ok := limiter.acquire("i"); ok {
		t.Fatal("global limit exceeded")
	}
	for _, release := range releases {
		release()
	}
}

func TestGroupOwnershipConcurrentBootstrapsAndUIDFencedRelease(t *testing.T) {
	ctx := context.Background()
	client := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())
	declaration := quickstartBootstrap(t)
	client.PrependReactor("create", "configmaps", func(a clienttesting.Action) (bool, runtime.Object, error) {
		a.(clienttesting.CreateAction).GetObject().(*unstructured.Unstructured).SetResourceVersion("1")
		return false, nil, nil
	})
	client.PrependReactor("update", "configmaps", func(a clienttesting.Action) (bool, runtime.Object, error) {
		next := a.(clienttesting.UpdateAction).GetObject().(*unstructured.Unstructured)
		previous, err := client.Tracker().Get(a.GetResource(), a.GetNamespace(), next.GetName())
		if err != nil {
			return true, nil, err
		}
		if previous.(*unstructured.Unstructured).GetResourceVersion() != next.GetResourceVersion() {
			return true, nil, apierrors.NewConflict(a.GetResource().GroupResource(), next.GetName(), fmt.Errorf("stale version"))
		}
		version, _ := strconv.Atoi(next.GetResourceVersion())
		next.SetResourceVersion(strconv.Itoa(version + 1))
		return false, nil, nil
	})
	identities := []OrgBootstrapIdentity{
		{OrgUUID: "a", Cluster: "one", RegistrationUID: "uid-one"},
		{OrgUUID: "a", Cluster: "two", RegistrationUID: "uid-two"},
	}
	results := make(chan int, 2)
	var start sync.WaitGroup
	start.Add(1)
	for i := range identities {
		go func(i int) {
			start.Wait()
			err := reserveOrgProviderGroups(ctx, client, nil, identities[i], declaration)
			if err == nil {
				results <- i
			} else if apierrors.IsConflict(err) {
				results <- -1
			} else {
				t.Errorf("reserve: %v", err)
				results <- -1
			}
		}(i)
	}
	start.Done()
	a, b := <-results, <-results
	if (a < 0) == (b < 0) {
		t.Fatalf("wanted one owner, got %d %d", a, b)
	}
	winner := a
	if winner < 0 {
		winner = b
	}
	owner := identities[winner]
	if err := reserveOrgProviderGroups(ctx, client, nil, owner, declaration); err != nil {
		t.Fatal("idempotent retry:", err)
	}
	if err := releaseOrgProviderGroups(ctx, client, owner.OrgUUID, owner.Cluster, "stale-uid"); err != nil {
		t.Fatal(err)
	}
	loser := identities[1-winner]
	if err := reserveOrgProviderGroups(ctx, client, nil, loser, declaration); !apierrors.IsConflict(err) {
		t.Fatalf("stale cleanup released owner: %v", err)
	}
	if err := releaseOrgProviderGroups(ctx, client, owner.OrgUUID, owner.Cluster, owner.RegistrationUID); err != nil {
		t.Fatal(err)
	}
	if err := reserveOrgProviderGroups(ctx, client, nil, loser, declaration); err != nil {
		t.Fatal(err)
	}
	loser.OrgUUID = "other-org"
	if err := reserveOrgProviderGroups(ctx, client, nil, loser, declaration); err != nil {
		t.Fatal("group ownership leaked across orgs:", err)
	}
}

func TestHubRemovesLegacyBindGrantWithoutChangingRuntimeAuthority(t *testing.T) {
	ctx := context.Background()
	client := newRBACDynamicFake()
	name := "railgrid:providers:bind:planner.example"
	for _, resource := range []schema.GroupVersionResource{clusterRoleGVR, clusterRoleBindingGVR} {
		kind := "ClusterRole"
		if resource == clusterRoleBindingGVR {
			kind = "ClusterRoleBinding"
		}
		object := &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "rbac.authorization.k8s.io/v1", "kind": kind,
			"metadata": map[string]any{"name": name},
		}}
		if _, err := client.Resource(resource).Create(ctx, object, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	for range 2 {
		if err := ensureOrgProviderSA(ctx, client, "planner"); err != nil {
			t.Fatal(err)
		}
	}
	for _, resource := range []schema.GroupVersionResource{clusterRoleGVR, clusterRoleBindingGVR} {
		if _, err := client.Resource(resource).Get(ctx, name, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
			t.Fatalf("legacy %s retained: %v", resource.Resource, err)
		}
	}
	// Reservations live in the pre-existing Hub-only namespace, not provider storage.
	if kcppaths.HubSystemNamespace == ProviderSANamespace {
		t.Fatal("coordination is in provider namespace")
	}
}

type blockedBootstrapBody struct {
	started, resume chan struct{}
	once            sync.Once
}

func (b *blockedBootstrapBody) Read([]byte) (int, error) {
	b.once.Do(func() { close(b.started) })
	<-b.resume
	return 0, io.EOF
}
func (*blockedBootstrapBody) Close() error { return nil }

func TestBootstrapHandlerSlowOrganizationCannotConsumeAllSlots(t *testing.T) {
	handler := newOrgProviderBootstrapHandler(nil, nil, "", func(_ context.Context, request *http.Request, _ string) (OrgBootstrapIdentity, error) {
		return OrgBootstrapIdentity{OrgUUID: request.Header.Get("Test-Authenticated-Org")}, nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	t.Cleanup(cancel)
	done := make(chan struct{}, 2)
	for range 2 {
		body := &blockedBootstrapBody{started: make(chan struct{}), resume: make(chan struct{})}
		defer close(body.resume)
		request := httptest.NewRequest(http.MethodPost, "/api/providers/planner/bootstrap", nil).WithContext(ctx)
		request.Body = body
		request.Header.Set("Test-Authenticated-Org", "a")
		go func() { handler.ServeHTTP(httptest.NewRecorder(), request); done <- struct{}{} }()
		select {
		case <-body.started:
		case <-ctx.Done():
			t.Fatal("request did not reach bounded body reader")
		}
	}
	for _, scenario := range []struct {
		org  string
		want int
	}{{"a", http.StatusTooManyRequests}, {"b", http.StatusBadRequest}} {
		request := httptest.NewRequest(http.MethodPost, "/api/providers/planner/bootstrap", strings.NewReader("invalid-json"))
		request.Header.Set("Test-Authenticated-Org", scenario.org)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != scenario.want {
			t.Fatalf("org %s status=%d want=%d", scenario.org, response.Code, scenario.want)
		}
	}
	// Deferred body releases let both original requests terminate.
	t.Cleanup(func() {
		for range 2 {
			select {
			case <-done:
			case <-ctx.Done():
				t.Error("body reader did not stop")
			}
		}
	})
}

func TestGroupReservationsReclaimOnlyRemovedRegistrations(t *testing.T) {
	ctx := context.Background()
	client := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())
	registrations := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{workspaceGVR: "WorkspaceList"})
	owner := OrgBootstrapIdentity{OrgUUID: "org", Cluster: "old-cluster", RegistrationUID: "old-uid"}
	declaration := quickstartBootstrap(t)
	if err := reserveOrgProviderGroups(ctx, client, nil, owner, declaration); err != nil {
		t.Fatal(err)
	}
	replacement := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "tenancy.kcp.io/v1alpha1", "kind": "Workspace",
		"metadata": map[string]any{"name": "replacement", "uid": "new-uid"}, "spec": map[string]any{"cluster": "new-cluster"},
	}}
	if _, err := registrations.Resource(workspaceGVR).Create(ctx, replacement, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	next := OrgBootstrapIdentity{OrgUUID: "org", Cluster: "new-cluster", RegistrationUID: "new-uid"}
	if err := reserveOrgProviderGroups(ctx, client, registrations, next, declaration); err != nil {
		t.Fatal("removed owner was not reclaimed:", err)
	}
	if err := reserveOrgProviderGroups(ctx, client, registrations, owner, declaration); err == nil {
		t.Fatal("removed caller reserved groups again")
	}
	replacement.SetAnnotations(map[string]string{"providers.railgrid.ai/registration-deleting": "new-uid"})
	if _, err := registrations.Resource(workspaceGVR).Update(ctx, replacement, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := reserveOrgProviderGroups(ctx, client, registrations, next, declaration); err == nil {
		t.Fatal("deleting caller reserved groups")
	}
}
