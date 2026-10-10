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
	"fmt"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"

	aiv1alpha1 "github.com/railgrid/provider-app-studio/apis/ai/v1alpha1"
	asclient "github.com/railgrid/provider-app-studio/client"
	"github.com/railgrid/provider-sdk/dataplane"
)

func TestMaterializeAutomaticProjectIntegrationsDiscoversActionsIdempotently(t *testing.T) {
	project := &aiv1alpha1.Project{
		TypeMeta:   metav1.TypeMeta{APIVersion: aiv1alpha1.SchemeGroupVersion.String(), Kind: "Project"},
		ObjectMeta: metav1.ObjectMeta{Name: "demo", UID: "project-uid"},
		Spec:       aiv1alpha1.ProjectSpec{DisplayName: "Demo"},
	}
	orders := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": databricksTableAPIVersion,
		"kind":       databricksTableKind,
		"metadata":   map[string]any{"name": "orders"},
	}}
	customers := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": databricksTableAPIVersion,
		"kind":       databricksTableKind,
		"metadata":   map[string]any{"name": "customers"},
	}}
	scheme := runtime.NewScheme()
	if err := aiv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add App Studio scheme: %v", err)
	}
	dyn := fake.NewSimpleDynamicClientWithCustomListKinds(scheme, map[schema.GroupVersionResource]string{
		asclient.ProjectGVR: "ProjectList", testDatabricksTableGVR: "TableList",
	}, project, orders, customers)
	for _, verb := range []string{"create", "update", "delete", "patch"} {
		verb := verb
		dyn.PrependReactor(verb, "tables", func(k8stesting.Action) (bool, runtime.Object, error) {
			t.Fatalf("automatic discovery mutated provider resource with %s", verb)
			return true, nil, nil
		})
	}
	server := &Server{tenantWorkspaces: defaultTestWorkspaces.lookup, tenantActors: defaultTestActors.lookup, tenantProviders: defaultTestProviders}
	server.integrationAccessReviewer = integrationTestAccessReview
	server.projectIdentityTokenFor = func(context.Context, identity, *aiv1alpha1.Project) (string, error) { return "project-token", nil }
	server.providerResourceDiscoveryResolver = func(_ context.Context, _ identity, provider, apiVersion, kind, resource string) (providerResourceDiscoveryResponse, error) {
		items := []providerResourceDiscoveryItem{{Metadata: providerResourceMetadata{Name: "orders"}}, {Metadata: providerResourceMetadata{Name: "customers"}}}
		return providerResourceDiscoveryResponse{APIVersion: apiVersion, Kind: kind, Resource: resource, Items: items}, nil
	}
	server.providerActionCatalogResolver = func(context.Context, identity) ([]providerCatalogEntry, error) {
		return []providerCatalogEntry{
			{
				Name: "databricks", Ready: true,
				Export: testDatabricksTableExport([]providerCatalogAction{
					{Name: "query_table", Version: "v1", SchemaDigest: testProjectActionSchemaDigest,
						Consent: providerCatalogActionConsent{Required: true}},
					{Name: "update_table", Version: "v1", SchemaDigest: "sha256:" + "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"},
					{Name: "deprecated", Version: "v1", SchemaDigest: testProjectActionSchemaDigest,
						Deprecation: &providerCatalogDeprecation{Deprecated: true}},
					{Name: "invalid", Version: "v1", SchemaDigest: "sha256:bad"},
				}),
			},
			{Name: "offline", Ready: false, Export: testDatabricksTableExport([]providerCatalogAction{{
				Name: "query_table", Version: "v1", SchemaDigest: testProjectActionSchemaDigest,
			}})},
		}, nil
	}
	c := asclient.NewFromDynamic(dyn)
	id := automaticIntegrationIdentity("alice@example.com")
	discovery := server.discoverAutomaticProjectIntegrations(context.Background(), c, id, project)
	candidates := availableProjectIntegrationCandidates(project, discovery)
	if len(candidates) != 2 || candidates[0].Actions[0].ID != "query_table/v1" || !candidates[0].Actions[0].Consent.Required {
		t.Fatalf("available candidates = %#v, want catalog actions with explicit consent metadata", candidates)
	}
	got, err := server.materializeAutomaticProjectIntegrations(context.Background(), c, id, project)
	if err != nil {
		t.Fatalf("materialize automatic integrations: %v", err)
	}
	if got == nil || len(got.Spec.Environments) != 1 || len(got.Spec.Environments[0].Bindings) != 2 {
		t.Fatalf("materialized project bindings = %#v, want one binding per accessible Table", got)
	}
	aliases := make(map[string]struct{}, 2)
	for _, binding := range got.Spec.Environments[0].Bindings {
		aliases[binding.Name] = struct{}{}
		if binding.Kind != aiv1alpha1.ProjectBindingKindProviderReference || binding.ResourceRef == nil {
			t.Fatalf("binding = %#v, want providerReference", binding)
		}
		if len(binding.AllowedActions) != 1 || binding.AllowedActions[0].Name != "update_table" {
			t.Fatalf("binding %q actions = %#v, want only the non-consent-required action", binding.Name, binding.AllowedActions)
		}
		for _, action := range binding.AllowedActions {
			if action.GrantedBy != automaticProviderActionGrantedBy || action.GrantedAt == nil || action.GrantedAt.IsZero() {
				t.Fatalf("automatic action audit = %#v, want server-owned GrantedBy/GrantedAt", action)
			}
		}
	}
	if len(aliases) != 2 {
		t.Fatalf("automatic aliases = %#v, want collision-safe unique aliases", aliases)
	}

	beforeDiscovery := got.DeepCopy()
	boundCandidates := availableProjectIntegrationCandidates(got, discovery)
	if len(boundCandidates) != 2 {
		t.Fatalf("bound candidates = %#v, want consent metadata retained for both saved resources", boundCandidates)
	}
	for _, candidate := range boundCandidates {
		if _, exists := aliases[candidate.Alias]; !exists || candidate.Environment != "development" {
			t.Fatalf("candidate lost saved binding identity: %#v", candidate)
		}
		if len(candidate.Actions) != 2 || !candidate.Actions[0].Consent.Required {
			t.Fatalf("candidate lost pending consent action: %#v", candidate.Actions)
		}
	}
	if !reflect.DeepEqual(beforeDiscovery, got) {
		t.Fatal("candidate discovery mutated saved grants")
	}

	second, err := server.materializeAutomaticProjectIntegrations(context.Background(), c, id, got)
	if err != nil {
		t.Fatalf("repeat automatic materialization: %v", err)
	}
	if !reflect.DeepEqual(got.Spec, second.Spec) {
		t.Fatalf("repeat materialization changed project spec:\nfirst=%#v\nsecond=%#v", got.Spec, second.Spec)
	}
}

func TestAutomaticIntegrationIntervalUnionUsesPerCallIntervals(t *testing.T) {
	start := time.Unix(100, 0)
	intervals := []automaticIntegrationTimeInterval{
		{started: start, ended: start.Add(2 * time.Second)},
		{started: start.Add(time.Second), ended: start.Add(3 * time.Second)},
		{started: start.Add(4 * time.Second), ended: start.Add(5 * time.Second)},
	}
	if got, want := automaticIntegrationIntervalUnion(intervals), 4*time.Second; got != want {
		t.Fatalf("per-call interval union = %s, want %s", got, want)
	}
}

func TestAutomaticIntegrationReviewServiceTimingSubtractsRateLimiterWait(t *testing.T) {
	if got := automaticIntegrationReviewServiceDuration(120*time.Millisecond, projectAssistantRateLimiterWaitSummary{total: 45 * time.Millisecond}); got != 75*time.Millisecond {
		t.Fatalf("review service duration = %s, want 75ms after subtracting limiter wait", got)
	}
	if got := automaticIntegrationReviewServiceDuration(20*time.Millisecond, projectAssistantRateLimiterWaitSummary{total: 25 * time.Millisecond}); got != 0 {
		t.Fatalf("review service duration with an overcounted wait = %s, want clamped zero", got)
	}
	if got := automaticIntegrationReviewServiceDuration(-time.Millisecond, projectAssistantRateLimiterWaitSummary{}); got != 0 {
		t.Fatalf("negative elapsed review duration = %s, want clamped zero", got)
	}

	summary := automaticIntegrationServiceDurationSummaryFor([]time.Duration{
		50 * time.Millisecond,
		10 * time.Millisecond,
		40 * time.Millisecond,
		20 * time.Millisecond,
		30 * time.Millisecond,
	})
	if summary.calls != 5 || summary.p50 != 30*time.Millisecond || summary.p95 != 50*time.Millisecond || summary.max != 50*time.Millisecond {
		t.Fatalf("review service distribution = %#v, want count=5 p50=30ms p95=max=50ms", summary)
	}
}

func TestAutomaticIntegrationResourceRecordsReviewServiceTimeWithoutWaitMetadata(t *testing.T) {
	server := &Server{}
	resource := automaticIntegrationTestCatalogResource([]automaticProviderCatalogAction{{
		name: "query_table", version: "v1", schemaDigest: testProjectActionSchemaDigest,
		catalog: providerCatalogAction{Name: "query_table", Version: "v1", SchemaDigest: testProjectActionSchemaDigest},
	}})
	server.providerResourceDiscoveryResolver = func(_ context.Context, _ identity, _ string, apiVersion, kind, resource string) (providerResourceDiscoveryResponse, error) {
		return providerResourceDiscoveryResponse{
			APIVersion: apiVersion, Kind: kind, Resource: resource,
			Items: []providerResourceDiscoveryItem{{Metadata: providerResourceMetadata{Name: "orders"}}},
		}, nil
	}
	server.integrationAccessReviewer = func(ctx context.Context, _ identity, _ dataplane.ResourceAttributes) (bool, error) {
		observation, ok := ctx.Value(projectAssistantRateLimiterWaitContextKey{}).(*projectAssistantRateLimiterWaitObservation)
		if !ok || observation == nil {
			t.Fatal("authorization review did not receive its private limiter observation")
		}
		observation.observe(10 * time.Millisecond)
		return true, nil
	}

	_, success, metrics := server.discoverAutomaticIntegrationResource(context.Background(), automaticIntegrationIdentity("alice@example.com"), resource)
	if !success {
		t.Fatal("automatic integration resource discovery failed")
	}
	if len(metrics.parentReviewServiceDurations) != 1 || len(metrics.actionReviewServiceDurations) != 1 {
		t.Fatalf("per-review service samples = parent %d, action %d; want one each", len(metrics.parentReviewServiceDurations), len(metrics.actionReviewServiceDurations))
	}
	if metrics.parentReviewWaits.calls != 1 || metrics.actionReviewWaits.calls != 1 ||
		metrics.parentReviewServiceDurations[0] != 0 || metrics.actionReviewServiceDurations[0] != 0 {
		t.Fatalf("review service/wait metrics = parent service %v wait %#v, action service %v wait %#v; expected overcounted waits to clamp service to zero",
			metrics.parentReviewServiceDurations, metrics.parentReviewWaits, metrics.actionReviewServiceDurations, metrics.actionReviewWaits)
	}
}

func TestAutomaticIntegrationDiscoveryDeduplicatesActionReviewAcrossVersions(t *testing.T) {
	server := &Server{}
	resource := automaticIntegrationTestCatalogResource([]automaticProviderCatalogAction{
		{name: "query_table", version: "v1", schemaDigest: testProjectActionSchemaDigest, catalog: providerCatalogAction{Name: "query_table", Version: "v1", SchemaDigest: testProjectActionSchemaDigest}},
		{name: "query_table", version: "v2", schemaDigest: "sha256:abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789", catalog: providerCatalogAction{Name: "query_table", Version: "v2", SchemaDigest: "sha256:abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"}},
	})
	server.providerResourceDiscoveryResolver = func(_ context.Context, _ identity, _ string, apiVersion, kind, resource string) (providerResourceDiscoveryResponse, error) {
		return providerResourceDiscoveryResponse{
			APIVersion: apiVersion, Kind: kind, Resource: resource,
			Items: []providerResourceDiscoveryItem{
				{Metadata: providerResourceMetadata{Name: "orders", UID: "table-uid", ResourceVersion: "7"}},
				{Metadata: providerResourceMetadata{Name: "customers", UID: "customer-uid", ResourceVersion: "8"}},
			},
		}, nil
	}
	var reviews []dataplane.ResourceAttributes
	server.integrationAccessReviewer = func(_ context.Context, got identity, attrs dataplane.ResourceAttributes) (bool, error) {
		if got.user != "alice@example.com" || got.caller == nil || got.caller.User != "alice@example.com" {
			t.Fatalf("review identity was not preserved")
		}
		reviews = append(reviews, attrs)
		return true, nil
	}

	discovery, success, metrics := server.discoverAutomaticIntegrationResource(context.Background(), automaticIntegrationIdentity("alice@example.com"), resource)
	if !success {
		t.Fatalf("resource discovery failed: %#v", discovery.status.Issues)
	}
	if len(reviews) != 4 || reviews[0].Verb != "get" || reviews[1].Verb != "create" || reviews[1].Subresource != "query_table" || reviews[2].Verb != "get" || reviews[3].Verb != "create" || reviews[3].Subresource != "query_table" {
		t.Fatalf("review coordinates = %#v, want one parent read and one version-independent action review per object", reviews)
	}
	if metrics.metadataRequests != 1 || metrics.metadataItems != 2 || metrics.parentReviewCalls != 2 || metrics.actionReviewCalls != 2 || metrics.actionReviewDuplicateVersionsSkipped != 2 {
		t.Fatalf("discovery metrics = %#v, want one list, fresh reviews per object, and one skipped duplicate version per object", metrics)
	}
	if len(discovery.targets) != 2 {
		t.Fatalf("targets = %#v, want both discovered resources", discovery.targets)
	}
	for _, target := range discovery.targets {
		if target.ref.Name == "orders" && (target.uid != "table-uid" || target.resourceVersion != "7") {
			t.Fatalf("orders metadata = %#v, want UID and resource version preserved", target)
		}
		if len(target.actions) != 2 || target.actions[0].Version != "v1" || target.actions[0].SchemaDigest != testProjectActionSchemaDigest || target.actions[1].Version != "v2" || target.actions[1].SchemaDigest != "sha256:abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789" {
			t.Fatalf("versioned action grants = %#v, want both versions with their own schema digests", target.actions)
		}
		if len(target.catalogActions) != 2 || target.catalogActions[0].ID != "query_table/v1" || target.catalogActions[1].ID != "query_table/v2" {
			t.Fatalf("versioned catalog actions = %#v, want both versioned catalog digests retained", target.catalogActions)
		}
	}
}

func TestAutomaticIntegrationDiscoveryReviewsFailClosed(t *testing.T) {
	tests := []struct {
		name             string
		cancelBeforeList bool
		listError        error
		review           func(context.CancelFunc, dataplane.ResourceAttributes) (bool, error)
		wantParentCalls  int
		wantActionCalls  int
		wantCanceled     bool
	}{
		{
			name:      "list error",
			listError: errors.New("list failed"),
		},
		{
			name:             "canceled list",
			cancelBeforeList: true,
			wantCanceled:     true,
		},
		{
			name: "parent denied",
			review: func(_ context.CancelFunc, attrs dataplane.ResourceAttributes) (bool, error) {
				return attrs.Verb != "get", nil
			},
			wantParentCalls: 1,
		},
		{
			name: "parent review error",
			review: func(_ context.CancelFunc, attrs dataplane.ResourceAttributes) (bool, error) {
				if attrs.Verb == "get" {
					return false, errors.New("review failed")
				}
				return true, nil
			},
			wantParentCalls: 1,
		},
		{
			name: "action denied",
			review: func(_ context.CancelFunc, attrs dataplane.ResourceAttributes) (bool, error) {
				return attrs.Verb == "get", nil
			},
			wantParentCalls: 1,
			wantActionCalls: 1,
		},
		{
			name: "action review error",
			review: func(_ context.CancelFunc, attrs dataplane.ResourceAttributes) (bool, error) {
				if attrs.Verb == "create" {
					return false, errors.New("review failed")
				}
				return true, nil
			},
			wantParentCalls: 1,
			wantActionCalls: 1,
		},
		{
			name: "action cancellation after allow",
			review: func(cancel context.CancelFunc, attrs dataplane.ResourceAttributes) (bool, error) {
				if attrs.Verb == "create" {
					cancel()
				}
				return true, nil
			},
			wantParentCalls: 1,
			wantActionCalls: 1,
			wantCanceled:    true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			server := &Server{}
			resource := automaticIntegrationTestCatalogResource([]automaticProviderCatalogAction{
				{name: "query_table", version: "v1", schemaDigest: testProjectActionSchemaDigest, catalog: providerCatalogAction{Name: "query_table", Version: "v1", SchemaDigest: testProjectActionSchemaDigest}},
				{name: "query_table", version: "v2", schemaDigest: "sha256:abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789", catalog: providerCatalogAction{Name: "query_table", Version: "v2", SchemaDigest: "sha256:abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"}},
			})
			server.providerResourceDiscoveryResolver = func(_ context.Context, _ identity, _ string, apiVersion, kind, resource string) (providerResourceDiscoveryResponse, error) {
				if tc.listError != nil {
					return providerResourceDiscoveryResponse{}, tc.listError
				}
				return providerResourceDiscoveryResponse{
					APIVersion: apiVersion, Kind: kind, Resource: resource,
					Items: []providerResourceDiscoveryItem{{Metadata: providerResourceMetadata{Name: "orders"}}},
				}, nil
			}
			parentCalls, actionCalls := 0, 0
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			server.integrationAccessReviewer = func(_ context.Context, _ identity, attrs dataplane.ResourceAttributes) (bool, error) {
				switch attrs.Verb {
				case "get":
					parentCalls++
				case "create":
					actionCalls++
				}
				if tc.review == nil {
					return true, nil
				}
				return tc.review(cancel, attrs)
			}
			if tc.cancelBeforeList {
				cancel()
			}

			discovery, _, metrics := server.discoverAutomaticIntegrationResource(ctx, automaticIntegrationIdentity("alice@example.com"), resource)
			if parentCalls != tc.wantParentCalls || actionCalls != tc.wantActionCalls {
				t.Fatalf("review calls parent=%d action=%d, want parent=%d action=%d", parentCalls, actionCalls, tc.wantParentCalls, tc.wantActionCalls)
			}
			if len(discovery.targets) != 0 {
				t.Fatalf("denied, errored, or canceled discovery produced targets: %#v", discovery.targets)
			}
			if metrics.canceled != tc.wantCanceled {
				t.Fatalf("canceled metric = %t, want %t", metrics.canceled, tc.wantCanceled)
			}
			if tc.wantParentCalls == 0 && metrics.parentReviewCalls != 0 || tc.wantActionCalls == 0 && metrics.actionReviewCalls != 0 {
				t.Fatalf("review counters = parent %d/action %d, unexpected call", metrics.parentReviewCalls, metrics.actionReviewCalls)
			}
			if metrics.canceled && (metrics.parentReviewAllows > 0 && metrics.actionReviewErrors == 0) {
				t.Fatalf("cancellation produced an allowed action without an error: %#v", metrics)
			}
		})
	}
}

func automaticIntegrationTestCatalogResource(actions []automaticProviderCatalogAction) automaticProviderCatalogResource {
	return automaticProviderCatalogResource{
		provider: "databricks", apiVersion: databricksTableAPIVersion, kind: databricksTableKind,
		resource: databricksTableResource, gvr: testDatabricksTableGVR, actions: actions,
	}
}

func TestAutomaticIntegrationDiscoveryConcurrencyBudgetIsSharedAcrossTurns(t *testing.T) {
	const resourceCount = 8
	resources := make([]providerCatalogExportResource, 0, resourceCount)
	for index := 0; index < resourceCount; index++ {
		resources = append(resources, providerCatalogExportResource{
			Name:       fmt.Sprintf("items-%d", index),
			APIVersion: "startup.example/v1",
			Kind:       fmt.Sprintf("Item%d", index),
			Actions: []providerCatalogAction{{
				Name: "inspect", Version: "v1", SchemaDigest: testProjectActionSchemaDigest,
			}},
		})
	}
	catalog := []providerCatalogEntry{{
		Name: "startup-test", Ready: true,
		Export: &providerCatalogExport{Name: "startup-test.providers.railgrid.ai", Resources: resources},
	}}
	server := &Server{tenantWorkspaces: defaultTestWorkspaces.lookup, tenantActors: defaultTestActors.lookup, tenantProviders: defaultTestProviders}
	server.providerActionCatalogResolver = func(context.Context, identity) ([]providerCatalogEntry, error) {
		return catalog, nil
	}
	server.integrationAccessReviewer = func(_ context.Context, _ identity, attrs dataplane.ResourceAttributes) (bool, error) {
		return attrs.Verb == "get" || attrs.Verb == "create", nil
	}
	var active, maximum int32
	reachedBudget := make(chan struct{})
	release := make(chan struct{})
	var reachedOnce sync.Once
	server.providerResourceDiscoveryResolver = func(ctx context.Context, _ identity, _ string, apiVersion, kind, resource string) (providerResourceDiscoveryResponse, error) {
		current := atomic.AddInt32(&active, 1)
		for previous := atomic.LoadInt32(&maximum); current > previous; previous = atomic.LoadInt32(&maximum) {
			if atomic.CompareAndSwapInt32(&maximum, previous, current) {
				break
			}
		}
		if current >= integrationDiscoveryConcurrency {
			reachedOnce.Do(func() { close(reachedBudget) })
		}
		select {
		case <-release:
		case <-ctx.Done():
			atomic.AddInt32(&active, -1)
			return providerResourceDiscoveryResponse{}, ctx.Err()
		}
		atomic.AddInt32(&active, -1)
		return providerResourceDiscoveryResponse{
			APIVersion: apiVersion, Kind: kind, Resource: resource,
			Items: []providerResourceDiscoveryItem{{Metadata: providerResourceMetadata{Name: "item"}}},
		}, nil
	}
	c := asclient.NewFromDynamic(fake.NewSimpleDynamicClient(runtime.NewScheme()))
	project := &aiv1alpha1.Project{ObjectMeta: metav1.ObjectMeta{Name: "demo", UID: "project-uid"}}
	id := automaticIntegrationIdentity("alice@example.com")
	done := make(chan struct{}, 2)
	for range 2 {
		go func() {
			server.discoverAutomaticProjectIntegrations(context.Background(), c, id, project)
			done <- struct{}{}
		}()
	}
	select {
	case <-reachedBudget:
	case <-time.After(2 * time.Second):
		close(release)
		<-done
		<-done
		t.Fatal("discovery workers did not reach the shared concurrency budget")
	}
	// Leave the first wave blocked long enough for an incorrectly per-request
	// semaphore to admit the second turn's workers as well.
	time.Sleep(50 * time.Millisecond)
	gotMaximum := atomic.LoadInt32(&maximum)
	close(release)
	<-done
	<-done
	if gotMaximum != integrationDiscoveryConcurrency {
		t.Fatalf("concurrent provider resource requests = %d, want shared cap %d", gotMaximum, integrationDiscoveryConcurrency)
	}
}

func TestMaterializeAutomaticProjectIntegrationsPreservesRevocations(t *testing.T) {
	revokedAt := metav1.Now()
	project := projectWithTableIntegration(true)
	project.Spec.Environments[0].Bindings[0].AllowedActions[0].RevokedBy = "operator@example.com"
	project.Spec.Environments[0].Bindings[0].AllowedActions[0].RevokedAt = &revokedAt
	project.Spec.Environments[0].Bindings[0].AllowedActions[0].SchemaDigest = "sha256:" + "fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210"
	project.Spec.Environments[0].Bindings[0].AllowedActions[0].GrantedBy = "alice@example.com"
	server, c := automaticIntegrationTestServer(t, project, []string{"orders"})
	got, err := server.materializeAutomaticProjectIntegrations(context.Background(), c, automaticIntegrationIdentity("bob@example.com"), project)
	if err != nil {
		t.Fatalf("materialize automatic integrations: %v", err)
	}
	binding := got.Spec.Environments[0].Bindings[0]
	if len(binding.AllowedActions) != 2 {
		t.Fatalf("actions after discovery = %#v, want revoked action plus newly discovered action", binding.AllowedActions)
	}
	var foundRevoked bool
	for _, action := range binding.AllowedActions {
		if action.Name != projectIntegrationActionQueryTable {
			continue
		}
		foundRevoked = true
		if !action.Revoked || action.RevokedBy != "operator@example.com" || action.RevokedAt == nil || action.SchemaDigest == testProjectActionSchemaDigest {
			t.Fatalf("revocation was overwritten by automatic discovery: %#v", action)
		}
	}
	if !foundRevoked {
		t.Fatal("existing revoked action disappeared")
	}
}

func TestMaterializeAutomaticProjectIntegrationsRetriesConflictAndPreservesConcurrentSpec(t *testing.T) {
	project := &aiv1alpha1.Project{
		TypeMeta:   metav1.TypeMeta{APIVersion: aiv1alpha1.SchemeGroupVersion.String(), Kind: "Project"},
		ObjectMeta: metav1.ObjectMeta{Name: "demo", UID: "project-uid"},
		Spec:       aiv1alpha1.ProjectSpec{DisplayName: "Demo"},
	}
	server, c := automaticIntegrationTestServer(t, project, []string{"orders"})
	dyn, ok := c.Dynamic().(*fake.FakeDynamicClient)
	if !ok {
		t.Fatal("dynamic client is not a fake dynamic client")
	}

	latest := project.DeepCopy()
	latest.ResourceVersion = "2"
	latest.Spec.Memory.Constraints = []string{"preserve concurrent edit"}
	var updateCalls, getCalls int
	dyn.PrependReactor("update", "projects", func(k8stesting.Action) (bool, runtime.Object, error) {
		updateCalls++
		if updateCalls == 1 {
			return true, nil, apierrors.NewConflict(
				schema.GroupResource{Group: aiv1alpha1.GroupName, Resource: "projects"},
				project.Name,
				errors.New("the object has been modified"),
			)
		}
		return false, nil, nil
	})
	dyn.PrependReactor("get", "projects", func(k8stesting.Action) (bool, runtime.Object, error) {
		getCalls++
		if getCalls == 1 {
			return true, latest.DeepCopy(), nil
		}
		return false, nil, nil
	})

	got, err := server.materializeAutomaticProjectIntegrations(context.Background(), c, automaticIntegrationIdentity("alice@example.com"), project)
	if err != nil {
		t.Fatalf("materialize automatic integrations after conflict: %v", err)
	}
	if updateCalls != 2 {
		t.Fatalf("project update calls = %d, want one conflict followed by one retry", updateCalls)
	}
	if getCalls != 1 {
		t.Fatalf("project get calls after conflict = %d, want one fresh read", getCalls)
	}
	if got == nil || len(got.Spec.Environments) != 1 || len(got.Spec.Environments[0].Bindings) != 1 {
		t.Fatalf("retried project bindings = %#v, want one persisted automatic binding", got)
	}
	if !reflect.DeepEqual(got.Spec.Memory.Constraints, latest.Spec.Memory.Constraints) {
		t.Fatalf("concurrent project spec change = %#v, want %#v", got.Spec.Memory.Constraints, latest.Spec.Memory.Constraints)
	}
	binding := got.Spec.Environments[0].Bindings[0]
	if binding.Provider != projectIntegrationProviderDatabricks || binding.ResourceRef == nil || binding.ResourceRef.Name != "orders" {
		t.Fatalf("retried automatic binding = %#v, want databricks orders reference", binding)
	}
}

func TestMaterializeAutomaticProjectIntegrationsConflictWithExistingBindingSkipsRetryUpdate(t *testing.T) {
	project := &aiv1alpha1.Project{
		TypeMeta:   metav1.TypeMeta{APIVersion: aiv1alpha1.SchemeGroupVersion.String(), Kind: "Project"},
		ObjectMeta: metav1.ObjectMeta{Name: "demo", UID: "project-uid"},
		Spec:       aiv1alpha1.ProjectSpec{DisplayName: "Demo"},
	}
	server, c := automaticIntegrationTestServer(t, project, []string{"orders"})
	dyn, ok := c.Dynamic().(*fake.FakeDynamicClient)
	if !ok {
		t.Fatal("dynamic client is not a fake dynamic client")
	}
	latest := projectWithTableIntegration(false)
	latest.ResourceVersion = "2"
	latest.Spec.Memory.Constraints = []string{"preserve concurrent edit"}
	grantedAt := metav1.Now()
	latestBinding := &latest.Spec.Environments[0].Bindings[0]
	latestBinding.AllowedActions[0].GrantedBy = automaticProviderActionGrantedBy
	latestBinding.AllowedActions[0].GrantedAt = &grantedAt
	latestBinding.AllowedActions = append(latestBinding.AllowedActions, aiv1alpha1.ProjectProviderActionSpec{
		Name:         "update_table",
		Version:      "v1",
		SchemaDigest: "sha256:" + "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789",
		GrantedBy:    automaticProviderActionGrantedBy,
		GrantedAt:    &grantedAt,
	})
	var updateCalls, getCalls int
	dyn.PrependReactor("update", "projects", func(k8stesting.Action) (bool, runtime.Object, error) {
		updateCalls++
		return true, nil, apierrors.NewConflict(
			schema.GroupResource{Group: aiv1alpha1.GroupName, Resource: "projects"},
			project.Name,
			errors.New("the object has been modified"),
		)
	})
	dyn.PrependReactor("get", "projects", func(k8stesting.Action) (bool, runtime.Object, error) {
		getCalls++
		return true, latest.DeepCopy(), nil
	})

	got, err := server.materializeAutomaticProjectIntegrations(context.Background(), c, automaticIntegrationIdentity("alice@example.com"), project)
	if err != nil {
		t.Fatalf("materialize automatic integrations with existing binding: %v", err)
	}
	if updateCalls != 1 {
		t.Fatalf("project update calls = %d, want no retry update after fresh object is already current", updateCalls)
	}
	if getCalls != 1 {
		t.Fatalf("project get calls after conflict = %d, want one fresh read", getCalls)
	}
	if !reflect.DeepEqual(got.Spec.Memory.Constraints, latest.Spec.Memory.Constraints) {
		t.Fatalf("latest concurrent spec change = %#v, want %#v", got.Spec.Memory.Constraints, latest.Spec.Memory.Constraints)
	}
	if len(got.Spec.Environments) != 1 || len(got.Spec.Environments[0].Bindings) != 1 || len(got.Spec.Environments[0].Bindings[0].AllowedActions) != 2 {
		t.Fatalf("latest automatic binding = %#v, want existing binding with two actions", got.Spec.Environments)
	}
}

func TestMaterializeAutomaticProjectIntegrationsNonConflictUpdateErrorDoesNotRetry(t *testing.T) {
	server, project, dyn := automaticIntegrationFailureFixture(t)
	updateErr := errors.New("project update unavailable")
	var updateCalls, getCalls int
	dyn.PrependReactor("update", "projects", func(k8stesting.Action) (bool, runtime.Object, error) {
		updateCalls++
		return true, nil, updateErr
	})
	dyn.PrependReactor("get", "projects", func(k8stesting.Action) (bool, runtime.Object, error) {
		getCalls++
		return false, nil, nil
	})

	got, err := server.materializeAutomaticProjectIntegrations(context.Background(), asclient.NewFromDynamic(dyn), automaticIntegrationIdentity("alice@example.com"), project)
	if err == nil || !strings.HasPrefix(err.Error(), "persist automatic provider integrations: ") {
		t.Fatalf("non-conflict update result = project %v, err %v; want wrapped persistence error", got, err)
	}
	if !errors.Is(err, updateErr) {
		t.Fatalf("wrapped update error = %v, want original error", err)
	}
	if updateCalls != 1 {
		t.Fatalf("project update calls = %d, want immediate failure without retry", updateCalls)
	}
	if getCalls != 0 {
		t.Fatalf("project get calls after non-conflict error = %d, want no reload", getCalls)
	}
}

func TestMaterializeAutomaticProjectIntegrationsStopsAfterConflictAttemptLimit(t *testing.T) {
	server, project, dyn := automaticIntegrationFailureFixture(t)
	latest := project.DeepCopy()
	latest.ResourceVersion = "2"
	conflictErr := apierrors.NewConflict(
		schema.GroupResource{Group: aiv1alpha1.GroupName, Resource: "projects"},
		project.Name,
		errors.New("the object has been modified"),
	)
	var updateCalls, getCalls int
	dyn.PrependReactor("update", "projects", func(k8stesting.Action) (bool, runtime.Object, error) {
		updateCalls++
		return true, nil, conflictErr
	})
	dyn.PrependReactor("get", "projects", func(k8stesting.Action) (bool, runtime.Object, error) {
		getCalls++
		return true, latest.DeepCopy(), nil
	})

	got, err := server.materializeAutomaticProjectIntegrations(context.Background(), asclient.NewFromDynamic(dyn), automaticIntegrationIdentity("alice@example.com"), project)
	if got != nil || err == nil || !strings.HasPrefix(err.Error(), "persist automatic provider integrations: ") {
		t.Fatalf("repeated conflict result = project %v, err %v; want wrapped conflict", got, err)
	}
	if !apierrors.IsConflict(err) {
		t.Fatalf("repeated conflict error = %v, want conflict", err)
	}
	if updateCalls != automaticIntegrationUpdateAttempts {
		t.Fatalf("project update calls = %d, want bounded attempt count %d", updateCalls, automaticIntegrationUpdateAttempts)
	}
	if getCalls != automaticIntegrationUpdateAttempts-1 {
		t.Fatalf("project get calls after repeated conflicts = %d, want %d", getCalls, automaticIntegrationUpdateAttempts-1)
	}
}

func TestMaterializeAutomaticProjectIntegrationsCatalogAndListFailuresAreBestEffort(t *testing.T) {
	project := &aiv1alpha1.Project{ObjectMeta: metav1.ObjectMeta{Name: "demo"}, Spec: aiv1alpha1.ProjectSpec{DisplayName: "Demo"}}
	scheme := runtime.NewScheme()
	if err := aiv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add App Studio scheme: %v", err)
	}
	dyn := fake.NewSimpleDynamicClientWithCustomListKinds(scheme, map[schema.GroupVersionResource]string{asclient.ProjectGVR: "ProjectList", testDatabricksTableGVR: "TableList"}, project)
	c := asclient.NewFromDynamic(dyn)
	server := &Server{tenantWorkspaces: defaultTestWorkspaces.lookup, tenantActors: defaultTestActors.lookup, tenantProviders: defaultTestProviders}
	server.integrationAccessReviewer = integrationTestAccessReview
	server.projectIdentityTokenFor = func(context.Context, identity, *aiv1alpha1.Project) (string, error) { return "project-token", nil }
	server.providerActionCatalogResolver = func(context.Context, identity) ([]providerCatalogEntry, error) {
		return nil, errors.New("catalog unavailable")
	}
	got, err := server.materializeAutomaticProjectIntegrations(context.Background(), c, automaticIntegrationIdentity("alice@example.com"), project)
	if err != nil || got != project {
		t.Fatalf("catalog failure result = project %p, err %v; want unchanged actionless turn state", got, err)
	}
	server.providerActionCatalogResolver = func(context.Context, identity) ([]providerCatalogEntry, error) {
		return []providerCatalogEntry{{Name: "databricks", Ready: true, Export: testDatabricksTableExport([]providerCatalogAction{{
			Name: "query_table", Version: "v1", SchemaDigest: testProjectActionSchemaDigest,
		}})}}, nil
	}
	server.providerResourceDiscoveryResolver = func(context.Context, identity, string, string, string, string) (providerResourceDiscoveryResponse, error) {
		return providerResourceDiscoveryResponse{}, errors.New("provider resource list unavailable")
	}
	got, err = server.materializeAutomaticProjectIntegrations(context.Background(), c, automaticIntegrationIdentity("alice@example.com"), project)
	if err != nil || got != project {
		t.Fatalf("resource-list failure result = project %p, err %v; want unchanged actionless turn state", got, err)
	}
}

func automaticIntegrationTestServer(t *testing.T, project *aiv1alpha1.Project, names []string) (*Server, *asclient.Client) {
	t.Helper()
	objects := []runtime.Object{project}
	for _, name := range names {
		objects = append(objects, &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": databricksTableAPIVersion, "kind": databricksTableKind,
			"metadata": map[string]any{"name": name},
		}})
	}
	scheme := runtime.NewScheme()
	if err := aiv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add App Studio scheme: %v", err)
	}
	dyn := fake.NewSimpleDynamicClientWithCustomListKinds(scheme, map[schema.GroupVersionResource]string{
		asclient.ProjectGVR: "ProjectList", testDatabricksTableGVR: "TableList",
	}, objects...)
	server := &Server{tenantWorkspaces: defaultTestWorkspaces.lookup, tenantActors: defaultTestActors.lookup, tenantProviders: defaultTestProviders}
	server.integrationAccessReviewer = integrationTestAccessReview
	server.projectIdentityTokenFor = func(context.Context, identity, *aiv1alpha1.Project) (string, error) { return "project-token", nil }
	server.providerResourceDiscoveryResolver = func(_ context.Context, _ identity, _ string, apiVersion, kind, resource string) (providerResourceDiscoveryResponse, error) {
		items := make([]providerResourceDiscoveryItem, 0, len(names))
		for _, name := range names {
			items = append(items, providerResourceDiscoveryItem{Metadata: providerResourceMetadata{Name: name}})
		}
		return providerResourceDiscoveryResponse{APIVersion: apiVersion, Kind: kind, Resource: resource, Items: items}, nil
	}
	server.providerActionCatalogResolver = func(context.Context, identity) ([]providerCatalogEntry, error) {
		return []providerCatalogEntry{{Name: projectIntegrationProviderDatabricks, Ready: true, Export: testDatabricksTableExport([]providerCatalogAction{
			{Name: projectIntegrationActionQueryTable, Version: projectIntegrationActionVersionV1, SchemaDigest: testProjectActionSchemaDigest},
			{Name: "update_table", Version: "v1", SchemaDigest: "sha256:" + "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"},
		})}}, nil
	}
	return server, asclient.NewFromDynamic(dyn)
}

func automaticIntegrationIdentity(user string) identity {
	return identity{
		clusterID: "cluster-a", tenant: "cluster-a", user: user,
		caller: &dataplane.ProxiedIdentity{User: user},
	}
}

func automaticIntegrationFailureFixture(t *testing.T) (*Server, *aiv1alpha1.Project, *fake.FakeDynamicClient) {
	t.Helper()
	project := &aiv1alpha1.Project{
		TypeMeta:   metav1.TypeMeta{APIVersion: aiv1alpha1.SchemeGroupVersion.String(), Kind: "Project"},
		ObjectMeta: metav1.ObjectMeta{Name: "demo", UID: "project-uid"},
		Spec:       aiv1alpha1.ProjectSpec{DisplayName: "Demo"},
	}
	server, c := automaticIntegrationTestServer(t, project, []string{"orders"})
	dyn, ok := c.Dynamic().(*fake.FakeDynamicClient)
	if !ok {
		t.Fatal("dynamic client is not a fake dynamic client")
	}
	return server, project, dyn
}

func TestAvailableIntegrationCandidatesRetainEnvironmentAndAlias(t *testing.T) {
	project := projectWithTableIntegration(true)
	development := project.Spec.Environments[0]
	development.Name = "development"
	development.Bindings[0].Name = "dev-orders"
	production := *development.DeepCopy()
	production.Name = "production"
	production.Bindings[0].Name = "prod-orders"
	project.Spec.Environments = []aiv1alpha1.ProjectEnvironmentSpec{development, production}
	before := project.DeepCopy()
	binding := development.Bindings[0]
	discovery := automaticIntegrationDiscovery{targets: []automaticIntegrationTarget{{
		provider: binding.Provider, ref: binding.ResourceRef,
		catalogActions: []providerCatalogAction{{Name: "query_table", Version: "v1", SchemaDigest: testProjectActionSchemaDigest, Consent: providerCatalogActionConsent{Required: true}}},
	}}}
	candidates := availableProjectIntegrationCandidates(project, discovery)
	if len(candidates) != 2 || candidates[0].Environment != "development" || candidates[0].Alias != "dev-orders" || candidates[1].Environment != "production" || candidates[1].Alias != "prod-orders" {
		t.Fatalf("candidates = %#v, want each saved environment and alias", candidates)
	}
	if !reflect.DeepEqual(before, project) {
		t.Fatal("candidate discovery mutated the Project")
	}
}
