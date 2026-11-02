// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package providers

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	sdkinstall "github.com/railgrid/provider-sdk/install"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/rest"
	k8stesting "k8s.io/client-go/testing"

	providersv1alpha1 "github.com/railgrid/railgrid/apis/providers/v1alpha1"
	"github.com/railgrid/railgrid/pkg/hub/kcp/byolimits"
	"github.com/railgrid/railgrid/pkg/kcppaths"
)

func testConsumerPolicyConfig() OrgConsumerPolicyConfig {
	return OrgConsumerPolicyConfig{Quota: ConsumerQuotaLimits{MaxResourceTypes: 16, MaxObjectsPerResource: 4}, MaxConsumers: 2, MaxLiveBytes: 1 << 30, MaxOrganizations: 2}
}

func TestOrgConsumerPolicyValidatesAllocationBoundWithoutOverflow(t *testing.T) {
	good := testConsumerPolicyConfig()
	if err := good.Validate(1024); err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []string{"bytes too low", "zero consumers", "zero org capacity", "object product overflow", "consumer product overflow", "zero object size"} {
		t.Run(scenario, func(t *testing.T) {
			config := good
			objectBytes := int64(1024)
			switch scenario {
			case "bytes too low":
				config.MaxLiveBytes = 1
			case "zero consumers":
				config.MaxConsumers = 0
			case "zero org capacity":
				config.MaxOrganizations = 0
			case "object product overflow":
				objectBytes = math.MaxInt64
			case "consumer product overflow":
				config.MaxConsumers = math.MaxInt64
			case "zero object size":
				objectBytes = 0
			}
			if err := config.Validate(objectBytes); err == nil {
				t.Fatal("invalid or unbounded allocation accepted")
			}
		})
	}
	if orgProviderBaseQuota()["count/catalogentries.providers.railgrid.ai"] != "1" {
		t.Fatal("catalog object missing from provider allocation bound")
	}
}

type consumerPolicyFixture struct {
	policy                *OrgConsumerPolicy
	org, parent, consumer *dynamicfake.FakeDynamicClient
	clients               map[string]*dynamicfake.FakeDynamicClient
	provider              Provider
}

func newConsumerPolicyFixture(t *testing.T) consumerPolicyFixture {
	t.Helper()
	newClient := func(objects ...runtime.Object) *dynamicfake.FakeDynamicClient {
		listKinds := map[schema.GroupVersionResource]string{workspaceGVR: "WorkspaceList", orgQuotaGVR: "ResourceQuotaList", orgConsumerLeaseGVR: "LeaseList", orgConsumerBindingGVR: "APIBindingList", logicalClusterGVR: "LogicalClusterList"}
		for _, entry := range []struct{ group, resource string }{
			{"", "secrets"}, {"", "configmaps"}, {"", "serviceaccounts"}, {"", "namespaces"},
			{"rbac.authorization.k8s.io", "roles"}, {"rbac.authorization.k8s.io", "rolebindings"},
			{"rbac.authorization.k8s.io", "clusterroles"}, {"rbac.authorization.k8s.io", "clusterrolebindings"},
			{"coordination.k8s.io", "leases"}, {"railgrid.ai", "mcpservers"},
			{"apis.kcp.io", "apiresourceschemas"}, {"apis.kcp.io", "apiexports"}, {"apis.kcp.io", "apiexportendpointslices"},
			{"apiextensions.k8s.io", "customresourcedefinitions"}, {"providers.railgrid.ai", "catalogentries"},
			{"planner.example", "ordinary"}, {"code.example", "repositories"}, {"existing.example", "hidden"},
			{"planner.example", "newkind"},
		} {
			version := "v1"
			if entry.group == "planner.example" || entry.group == "code.example" || entry.group == "existing.example" || entry.group == "railgrid.ai" {
				version = "v1alpha1"
			}
			gvr := schema.GroupVersionResource{Group: entry.group, Version: version, Resource: entry.resource}
			listKinds[gvr] = strings.ToUpper(entry.resource[:1]) + entry.resource[1:] + "List"
		}
		return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), listKinds, objects...)
	}
	workspace := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "tenancy.kcp.io/v1alpha1", "kind": "Workspace", "metadata": map[string]any{"name": "team-a"}, "spec": map[string]any{"cluster": "cluster-a", "type": map[string]any{"name": "workspace"}}, "status": map[string]any{"phase": "Ready"}}}
	org := newClient(workspace)
	source := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "tenancy.kcp.io/v1alpha1", "kind": "Workspace", "metadata": map[string]any{"name": "planner", "annotations": map[string]any{OrgProviderBootstrapPolicyAnnotation: OrgProviderBootstrapPolicy}}, "spec": map[string]any{"cluster": "planner-cluster"}, "status": map[string]any{"phase": "Ready"}}}
	parent := newClient(source)
	logicalCluster := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "core.kcp.io/v1alpha1", "kind": "LogicalCluster", "metadata": map[string]any{"name": "cluster", "annotations": map[string]any{"kcp.io/cluster": "cluster-a", "kcp.io/path": kcppaths.WorkspacePath("org-a", "team-a")}}}}
	consumer := newClient(logicalCluster)
	mockOrgQuotaController(parent)
	mockOrgQuotaController(consumer)
	registry := NewRegistry()
	provider := Provider{Name: "planner", OrgUUID: "org-a", APIExportPath: kcppaths.OrgProviderPath("org-a", "planner"), APIExportName: "planner", Requires: []providersv1alpha1.ProviderRequirement{{Provider: "code", Group: "code.example", Resources: []providersv1alpha1.ProviderRequiredResource{{Name: "repositories", Verbs: []providersv1alpha1.ProviderRequiredVerb{"create"}}}}}}
	provider.CatalogEntryCluster = "planner-cluster"
	provider.WorkspaceCluster = "planner-cluster"
	registry.Upsert(provider)
	dependency := Provider{Name: "code", APIExportPath: kcppaths.ProviderPath("code"), APIExportName: "code"}
	registry.Upsert(dependency)
	existing := Provider{Name: "existing", APIExportPath: kcppaths.ProviderPath("existing"), APIExportName: "existing"}
	registry.Upsert(existing)
	clients := map[string]*dynamicfake.FakeDynamicClient{kcppaths.OrgPath("org-a"): org, kcppaths.OrgProvidersParent("org-a"): parent, kcppaths.WorkspacePath("org-a", "team-a"): consumer, "cluster-a": consumer}
	for _, entry := range []struct {
		provider        Provider
		group, resource string
	}{{provider, "planner.example", "ordinary"}, {dependency, "code.example", "repositories"}, {existing, "existing.example", "hidden"}, {Provider{APIExportPath: kcppaths.SystemControllers, APIExportName: "core.railgrid.ai"}, "railgrid.ai", "mcpservers"}} {
		sch := consumerSchema("v1."+entry.resource+"."+entry.group, entry.resource, "Cluster")
		_ = unstructured.SetNestedField(sch.Object, entry.group, "spec", "group")
		resource := consumerExportResource(entry.resource, sch.GetName())
		resource["group"] = entry.group
		export := consumerExport(resource)
		export.SetName(entry.provider.APIExportName)
		quota := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "ResourceQuota", "metadata": map[string]any{"name": orgQuotaName, "namespace": "admin", "annotations": map[string]any{"experimental.quota.kcp.io/cluster-scoped": "true"}}, "spec": map[string]any{"hard": map[string]any{"count/configmaps": "1"}}, "status": map[string]any{"hard": map[string]any{"count/configmaps": "1"}, "used": map[string]any{"count/configmaps": "0"}}}}
		clients[entry.provider.APIExportPath] = newClient(export, sch, quota)
	}
	for _, entry := range []Provider{existing, {Name: "railgrid", APIExportPath: kcppaths.SystemControllers, APIExportName: "core.railgrid.ai"}} {
		binding := policyBinding(entry.Name, entry.APIExportPath, entry.APIExportName)
		if err := consumer.Tracker().Create(orgConsumerBindingGVR, binding, ""); err != nil {
			t.Fatal(err)
		}
	}
	policy, err := NewOrgConsumerPolicy(&rest.Config{Host: "https://hub.example"}, registry, testConsumerPolicyConfig(), 1024)
	if err != nil {
		t.Fatal(err)
	}
	policy.clientFor = func(path string) (dynamic.Interface, error) {
		client := clients[path]
		if client == nil {
			return nil, fmt.Errorf("unexpected target %s", path)
		}
		return client, nil
	}
	policy.discoveryFor = func(string) (discovery.DiscoveryInterface, error) {
		return consumerPolicyDiscovery{}, nil
	}
	policy.auditClientFor = nil
	policy.SetConsumerMarkerReadiness(func(context.Context, string) error { return nil })
	return consumerPolicyFixture{policy: policy, org: org, parent: parent, consumer: consumer, clients: clients, provider: provider}
}

type consumerPolicyDiscovery struct {
	discovery.DiscoveryInterface
	omit map[schema.GroupResource]bool
}

func (d consumerPolicyDiscovery) ServerPreferredResources() ([]*metav1.APIResourceList, error) {
	byVersion := map[string][]metav1.APIResource{}
	for _, entry := range []struct{ group, version, resource string }{
		{"", "v1", "secrets"}, {"", "v1", "configmaps"}, {"", "v1", "serviceaccounts"}, {"", "v1", "namespaces"},
		{"rbac.authorization.k8s.io", "v1", "roles"}, {"rbac.authorization.k8s.io", "v1", "rolebindings"},
		{"rbac.authorization.k8s.io", "v1", "clusterroles"}, {"rbac.authorization.k8s.io", "v1", "clusterrolebindings"},
		{"coordination.k8s.io", "v1", "leases"}, {"railgrid.ai", "v1alpha1", "mcpservers"},
		{"apis.kcp.io", "v1alpha1", "apiresourceschemas"}, {"apis.kcp.io", "v1alpha2", "apiexports"}, {"apis.kcp.io", "v1alpha1", "apiexportendpointslices"},
		{"apiextensions.k8s.io", "v1", "customresourcedefinitions"}, {"providers.railgrid.ai", "v1alpha1", "catalogentries"},
		{"planner.example", "v1alpha1", "ordinary"}, {"code.example", "v1alpha1", "repositories"}, {"existing.example", "v1alpha1", "hidden"},
		{"planner.example", "v1alpha1", "newkind"},
	} {
		if d.omit[schema.GroupResource{Group: entry.group, Resource: entry.resource}] {
			continue
		}
		gv := entry.version
		if entry.group != "" {
			gv = entry.group + "/" + entry.version
		}
		byVersion[gv] = append(byVersion[gv], metav1.APIResource{Name: entry.resource})
	}
	out := make([]*metav1.APIResourceList, 0, len(byVersion))
	for gv, resources := range byVersion {
		out = append(out, &metav1.APIResourceList{GroupVersion: gv, APIResources: resources})
	}
	return out, nil
}

func TestOrgConsumerPolicyPrepareEnableAllowsOnlyUnrecordedProposedFamiliesAbsentFromDiscovery(t *testing.T) {
	f := newConsumerPolicyFixture(t)
	mockConsumerQuotaControllerWithStatus(t, f.consumer, map[string]bool{
		"count/ordinary.planner.example":  true,
		"count/repositories.code.example": true,
	}, nil, nil)
	f.policy.discoveryFor = func(string) (discovery.DiscoveryInterface, error) {
		return consumerPolicyDiscovery{omit: map[schema.GroupResource]bool{
			{Group: "planner.example", Resource: "ordinary"}:  true,
			{Group: "code.example", Resource: "repositories"}: true,
		}}, nil
	}
	if err := f.policy.PrepareEnable(context.Background(), "org-a", "team-a", f.provider); err != nil {
		t.Fatalf("first Enable rejected absent, unrecorded provider/dependency APIs: %v", err)
	}
	hard := consumerQuotaForTest(t, f.consumer)
	for _, key := range []string{"count/ordinary.planner.example", "count/repositories.code.example"} {
		if hard[key] != "4" {
			t.Errorf("new family %s was not reserved: %v", key, hard)
		}
	}
	quota, err := f.consumer.Resource(orgQuotaGVR).Namespace("admin").Get(context.Background(), OrgConsumerQuotaName, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	used, _, _ := unstructured.NestedStringMap(quota.Object, "status", "used")
	for _, key := range []string{"count/ordinary.planner.example", "count/repositories.code.example"} {
		if _, found := used[key]; found {
			t.Errorf("unserved family %s unexpectedly has status usage: %v", key, used)
		}
	}
}

func TestOrgConsumerPolicyWaitsForMissingMandatoryOrServedQuotaCounters(t *testing.T) {
	for _, scenario := range []string{"served proposed family", "builtin family"} {
		t.Run(scenario, func(t *testing.T) {
			f := newConsumerPolicyFixture(t)
			key := "count/ordinary.planner.example"
			if scenario == "builtin family" {
				key = "count/configmaps"
			}
			mockConsumerQuotaControllerWithStatus(t, f.consumer, map[string]bool{key: true}, nil, nil)
			ctx, cancel := context.WithTimeout(context.Background(), 75*time.Millisecond)
			defer cancel()
			if err := f.policy.PrepareEnable(ctx, "org-a", "team-a", f.provider); err == nil {
				t.Fatal("quota preparation returned without an initialized mandatory counter")
			}
			hard := consumerQuotaForTest(t, f.consumer)
			if hard[key] == "" {
				t.Fatalf("full hard quota was not persisted before waiting: %v", hard)
			}
		})
	}
}

func TestPendingNewQuotaStatusAllowsOnlyMissingUsage(t *testing.T) {
	resource := schema.GroupResource{Group: "planner.example", Resource: "ordinary"}
	key := consumerCountKey(resource)
	t.Run("missing usage", func(t *testing.T) {
		f := newConsumerPolicyFixture(t)
		mockConsumerQuotaControllerWithStatus(t, f.consumer, map[string]bool{key: true}, nil, nil)
		if err := ensureOrgConsumerQuotaWithPendingStatus(context.Background(), f.consumer, []schema.GroupResource{resource}, f.policy.config.Quota, map[schema.GroupResource]bool{resource: true}); err != nil {
			t.Fatalf("pending unserved family rejected despite complete mandatory status: %v", err)
		}
	})
	for _, used := range []string{"", "invalid", "-1", "5"} {
		t.Run("used="+used, func(t *testing.T) {
			f := newConsumerPolicyFixture(t)
			mockConsumerQuotaControllerWithStatus(t, f.consumer, nil, map[string]string{key: used}, nil)
			if err := ensureOrgConsumerQuotaWithPendingStatus(context.Background(), f.consumer, []schema.GroupResource{resource}, f.policy.config.Quota, map[schema.GroupResource]bool{resource: true}); err == nil {
				t.Fatal("malformed, negative, or over-limit observed usage accepted for pending family")
			}
		})
	}
}

func mockConsumerQuotaControllerWithStatus(t *testing.T, client *dynamicfake.FakeDynamicClient, omitUsed map[string]bool, usedOverrides, hardOverrides map[string]string) {
	t.Helper()
	client.PrependReactor("get", "resourcequotas", func(action k8stesting.Action) (bool, runtime.Object, error) {
		object, err := client.Tracker().Get(orgQuotaGVR, action.GetNamespace(), action.(k8stesting.GetAction).GetName())
		if err != nil {
			return true, nil, err
		}
		quota := object.(*unstructured.Unstructured)
		hard, _, _ := unstructured.NestedStringMap(quota.Object, "spec", "hard")
		observedHard := map[string]string{}
		used := map[string]string{}
		for key, value := range hard {
			observedHard[key] = value
			if !omitUsed[key] {
				used[key] = "0"
			}
		}
		for key, value := range hardOverrides {
			observedHard[key] = value
		}
		for key, value := range usedOverrides {
			used[key] = value
		}
		_ = unstructured.SetNestedStringMap(quota.Object, observedHard, "status", "hard")
		_ = unstructured.SetNestedStringMap(quota.Object, used, "status", "used")
		return true, quota, nil
	})
}

func TestOrgConsumerPolicyPrepareEnableFailsClosedForMissingPreviouslyManagedFamilies(t *testing.T) {
	for _, scenario := range []string{"bound family", "retired quota family", "builtin family", "malformed quota"} {
		t.Run(scenario, func(t *testing.T) {
			f := newConsumerPolicyFixture(t)
			omit := map[schema.GroupResource]bool{}
			switch scenario {
			case "bound family":
				omit[schema.GroupResource{Group: "existing.example", Resource: "hidden"}] = true
			case "retired quota family":
				quota := consumerQuotaFixture(map[string]any{"count/retired.retired.example": "4"})
				if err := f.consumer.Tracker().Create(orgQuotaGVR, quota, "admin"); err != nil {
					t.Fatal(err)
				}
				omit[schema.GroupResource{Group: "retired.example", Resource: "retired"}] = true
			case "builtin family":
				omit[schema.GroupResource{Resource: "configmaps"}] = true
			case "malformed quota":
				quota := consumerQuotaFixture(map[string]any{"count/configmaps": "0"})
				if err := f.consumer.Tracker().Create(orgQuotaGVR, quota, "admin"); err != nil {
					t.Fatal(err)
				}
			}
			if len(omit) != 0 {
				f.policy.discoveryFor = func(string) (discovery.DiscoveryInterface, error) {
					return consumerPolicyDiscovery{omit: omit}, nil
				}
			}
			if err := f.policy.PrepareEnable(context.Background(), "org-a", "team-a", f.provider); err == nil {
				t.Fatal("missing previously managed or malformed quota family was accepted")
			}
		})
	}
}

func TestOrgConsumerPolicyRejectsMalformedPriorQuotaStatusHistory(t *testing.T) {
	for _, scenario := range []string{"orphan usage", "orphan hard status", "malformed usage map", "malformed hard map"} {
		t.Run(scenario, func(t *testing.T) {
			f := newConsumerPolicyFixture(t)
			quota := consumerQuotaFixture(nil)
			switch scenario {
			case "orphan usage":
				_ = unstructured.SetNestedStringMap(quota.Object, map[string]string{"count/ghost.example": "0"}, "status", "used")
			case "orphan hard status":
				_ = unstructured.SetNestedStringMap(quota.Object, map[string]string{"count/ghost.example": "4"}, "status", "hard")
			case "malformed usage map":
				quota.Object["status"] = map[string]any{"used": "not-a-map"}
			case "malformed hard map":
				quota.Object["status"] = map[string]any{"hard": "not-a-map"}
			}
			if err := f.consumer.Tracker().Create(orgQuotaGVR, quota, "admin"); err != nil {
				t.Fatal(err)
			}
			f.consumer.PrependReactor("get", "resourcequotas", func(action k8stesting.Action) (bool, runtime.Object, error) {
				object, err := f.consumer.Tracker().Get(orgQuotaGVR, action.GetNamespace(), action.(k8stesting.GetAction).GetName())
				return true, object, err
			})
			if _, err := f.policy.priorConsumerQuotaResources(context.Background(), f.consumer); err == nil {
				t.Fatal("malformed prior quota history was accepted")
			}
		})
	}
}

func TestOrgConsumerPolicyReservationOnlyRetryDoesNotTreatFamilyAsNew(t *testing.T) {
	f := newConsumerPolicyFixture(t)
	if err := f.policy.PrepareEnable(context.Background(), "org-a", "team-a", f.provider); err != nil {
		t.Fatal(err)
	}
	// PrepareEnable intentionally reserves quota before the REST handler writes
	// the APIBinding. Until that lifecycle gains compensation, an interrupted
	// request leaves an ambiguous historical key; retry must remain fail-closed.
	f.policy.discoveryFor = func(string) (discovery.DiscoveryInterface, error) {
		return consumerPolicyDiscovery{omit: map[schema.GroupResource]bool{{Group: "planner.example", Resource: "ordinary"}: true}}, nil
	}
	if err := f.policy.PrepareEnable(context.Background(), "org-a", "team-a", f.provider); err == nil {
		t.Fatal("reservation-only retry was incorrectly treated as a new family")
	}
}

func consumerQuotaFixture(hard map[string]any) *unstructured.Unstructured {
	fullHard := map[string]any{}
	for _, resource := range orgConsumerBuiltinResources {
		fullHard[consumerCountKey(resource)] = "4"
	}
	for key, value := range hard {
		fullHard[key] = value
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "ResourceQuota",
		"metadata": map[string]any{"name": OrgConsumerQuotaName, "namespace": "admin", "annotations": map[string]any{"experimental.quota.kcp.io/cluster-scoped": "true"}},
		"spec":     map[string]any{"hard": fullHard},
	}}
}

func TestConsumerInventoryResourcesKeepsMandatoryFamiliesStrict(t *testing.T) {
	builtin := schema.GroupResource{Resource: "configmaps"}
	resources, optional := consumerInventoryResources([]schema.GroupResource{builtin}, nil, nil)
	if optional[builtin] {
		t.Fatal("a proposed builtin family was marked optional")
	}
	found := false
	for _, resource := range resources {
		found = found || resource == builtin
	}
	if !found {
		t.Fatal("mandatory builtin family missing from inventory")
	}
}

func policyBinding(name, path, exportName string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{"apiVersion": "apis.kcp.io/v1alpha2", "kind": "APIBinding", "metadata": map[string]any{"name": name}, "spec": map[string]any{"reference": map[string]any{"export": map[string]any{"path": path, "name": exportName}}}, "status": map[string]any{"phase": "Bound"}}}
}

func TestOrgConsumerPolicyPrepareEnableBoundsOwnDependenciesAndExistingKinds(t *testing.T) {
	f := newConsumerPolicyFixture(t)
	if err := f.policy.PrepareEnable(context.Background(), "org-a", "team-a", f.provider); err != nil {
		t.Fatal(err)
	}
	lc, err := f.consumer.Tracker().Get(logicalClusterGVR, "", "cluster")
	if err != nil || lc.(*unstructured.Unstructured).GetAnnotations()[byolimits.ConsumerQuotaRequiredAnnotation] != byolimits.ConsumerQuotaRequiredValue {
		t.Fatalf("consumer LogicalCluster was not persistently marked for quota enforcement: lc=%v err=%v", lc, err)
	}
	hard := consumerQuotaForTest(t, f.consumer)
	for _, key := range []string{"count/ordinary.planner.example", "count/repositories.code.example", "count/hidden.existing.example", "count/mcpservers.railgrid.ai"} {
		if hard[key] != "4" {
			t.Errorf("unbounded reachable kind %s: %v", key, hard)
		}
	}
	for _, action := range f.consumer.Actions() {
		if action.GetResource() == orgConsumerBindingGVR && action.GetVerb() != "list" {
			t.Fatal("preflight mutated binding or granted access")
		}
	}
}

func TestOrgConsumerPolicyMarkerReadinessFailsBeforeInventoryAndQuotaWrites(t *testing.T) {
	for _, scenario := range []string{"unset", "unavailable", "canceled"} {
		t.Run(scenario, func(t *testing.T) {
			f := newConsumerPolicyFixture(t)
			ctx := context.Background()
			switch scenario {
			case "unset":
				f.policy.SetConsumerMarkerReadiness(nil)
			case "unavailable":
				f.policy.SetConsumerMarkerReadiness(func(context.Context, string) error { return fmt.Errorf("guard cache unavailable") })
			case "canceled":
				f.policy.SetConsumerMarkerReadiness(func(ctx context.Context, _ string) error {
					waitCtx, cancel := context.WithCancel(ctx)
					cancel()
					return waitCtx.Err()
				})
			}
			if err := f.policy.PrepareEnable(ctx, "org-a", "team-a", f.provider); err == nil {
				t.Fatal("consumer preparation proceeded without observed native guard marker")
			}
			lc, err := f.consumer.Tracker().Get(logicalClusterGVR, "", "cluster")
			if err != nil || lc.(*unstructured.Unstructured).GetAnnotations()[byolimits.ConsumerQuotaRequiredAnnotation] != byolimits.ConsumerQuotaRequiredValue {
				t.Fatalf("trusted marker was not persisted before readiness wait: lc=%v err=%v", lc, err)
			}
			for _, action := range f.consumer.Actions() {
				if action.GetResource() == orgQuotaGVR || action.GetResource() == orgConsumerBindingGVR || action.GetVerb() == "list" {
					t.Fatalf("preparation inspected or changed storage before marker readiness: %s %s", action.GetVerb(), action.GetResource())
				}
			}
			for _, action := range f.parent.Actions() {
				if action.GetResource() == orgConsumerLeaseGVR && action.GetVerb() != "get" {
					t.Fatalf("preparation changed allocation before marker readiness: %s", action.GetVerb())
				}
			}
		})
	}
}

func TestOrgConsumerPolicyPrepareEnableRejectsInactiveSourceDespiteStaleCatalogAndExport(t *testing.T) {
	for _, scenario := range []string{"missing", "terminating", "unmarked", "not ready", "bad cluster", "root cluster", "replaced cluster", "changed registry export", "wrong source path"} {
		t.Run(scenario, func(t *testing.T) {
			f := newConsumerPolicyFixture(t)
			object, err := f.parent.Tracker().Get(workspaceGVR, "", "planner")
			if err != nil {
				t.Fatal(err)
			}
			source := object.(*unstructured.Unstructured)
			switch scenario {
			case "missing":
				if err := f.parent.Tracker().Delete(workspaceGVR, "", "planner"); err != nil {
					t.Fatal(err)
				}
			case "terminating":
				now := metav1.Now()
				source.SetDeletionTimestamp(&now)
				source.SetFinalizers([]string{"cleanup"})
			case "unmarked":
				source.SetAnnotations(nil)
			case "not ready":
				_ = unstructured.SetNestedField(source.Object, "Scheduling", "status", "phase")
			case "bad cluster":
				_ = unstructured.SetNestedField(source.Object, "root:other", "spec", "cluster")
			case "root cluster":
				_ = unstructured.SetNestedField(source.Object, "root", "spec", "cluster")
			case "replaced cluster":
				_ = unstructured.SetNestedField(source.Object, "replacement", "spec", "cluster")
			case "changed registry export":
				current := f.provider
				current.APIExportName = "replacement"
				f.policy.registry.Upsert(current)
			case "wrong source path":
				f.provider.APIExportPath = kcppaths.OrgProviderPath("org-b", "planner")
			}
			if scenario != "missing" {
				if err := f.parent.Tracker().Update(workspaceGVR, source, ""); err != nil {
					t.Fatal(err)
				}
			}
			if err := f.policy.PrepareEnable(context.Background(), "org-a", "team-a", f.provider); err == nil {
				t.Fatal("stale source enabled despite readable old APIExport")
			}
			for _, client := range f.clients {
				for _, action := range client.Actions() {
					if action.GetVerb() == "create" || action.GetVerb() == "update" || action.GetVerb() == "patch" || action.GetVerb() == "delete" {
						t.Fatalf("failed source check mutated %s via %s", action.GetResource(), action.GetVerb())
					}
				}
			}
		})
	}
}

func TestOrgConsumerPolicyNeedsPreparationForExistingAllocations(t *testing.T) {
	f := newConsumerPolicyFixture(t)
	ctx := context.Background()
	if needed, err := f.policy.NeedsPreparation(ctx, "org-a", "team-a", f.provider); err != nil || !needed {
		t.Fatalf("org provider not gated: needed=%v err=%v", needed, err)
	}
	platform := Provider{Name: "code"}
	if needed, err := f.policy.NeedsPreparation(ctx, "org-a", "team-a", platform); err != nil || needed {
		t.Fatalf("unallocated platform consumer changed: needed=%v err=%v", needed, err)
	}
	if err := f.policy.PrepareEnable(ctx, "org-a", "team-a", f.provider); err != nil {
		t.Fatal(err)
	}
	if needed, err := f.policy.NeedsPreparation(ctx, "org-a", "team-a", platform); err != nil || !needed {
		t.Fatalf("allocated platform consumer not gated: needed=%v err=%v", needed, err)
	}
	object, err := f.parent.Tracker().Get(orgConsumerLeaseGVR, "admin", "consumer-cluster-a")
	if err != nil {
		t.Fatal(err)
	}
	allocation := object.(*unstructured.Unstructured)
	annotations := allocation.GetAnnotations()
	annotations[orgConsumerAllocationPath] = kcppaths.WorkspacePath("org-b", "team-a")
	allocation.SetAnnotations(annotations)
	if err := f.parent.Tracker().Update(orgConsumerLeaseGVR, allocation, "admin"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.policy.NeedsPreparation(ctx, "org-a", "team-a", platform); err == nil {
		t.Fatal("mismatched allocation bypassed preparation")
	}
}

func TestOrgConsumerPolicyPrepareEnableRejectsForeignOrUnknownBinding(t *testing.T) {
	f := newConsumerPolicyFixture(t)
	foreign := policyBinding("foreign", kcppaths.OrgProviderPath("org-b", "other"), "other")
	if err := f.consumer.Tracker().Create(orgConsumerBindingGVR, foreign, ""); err != nil {
		t.Fatal(err)
	}
	if err := f.policy.PrepareEnable(context.Background(), "org-a", "team-a", f.provider); err == nil {
		t.Fatal("foreign consumer binding accepted")
	}
	if _, err := f.parent.Tracker().Get(orgConsumerLeaseGVR, "admin", "consumer-cluster-a"); err == nil {
		t.Fatal("invalid preflight consumed an allocation")
	}
}

func TestOrgConsumerPolicyRejectsIncompleteInventories(t *testing.T) {
	for _, operation := range []string{"enable bindings", "delete workspaces", "delete bindings"} {
		t.Run(operation, func(t *testing.T) {
			f := newConsumerPolicyFixture(t)
			client, resource := f.consumer, "apibindings"
			if operation == "delete workspaces" {
				client, resource = f.org, "workspaces"
			}
			client.PrependReactor("list", resource, func(k8stesting.Action) (bool, runtime.Object, error) {
				list := &unstructured.UnstructuredList{}
				list.SetContinue("more-objects")
				return true, list, nil
			})
			var err error
			if operation == "enable bindings" {
				err = f.policy.PrepareEnable(context.Background(), "org-a", "team-a", f.provider)
			} else {
				err = f.policy.CheckDelete(context.Background(), "org-a", "planner", "planner")
			}
			if err == nil {
				t.Fatal("incomplete inventory authorized lifecycle operation")
			}
		})
	}
}

func proposedBootstrap() sdkinstall.OrganizationBootstrap {
	sch := consumerSchema("v2.newkind.planner.example", "newkind", "Namespaced")
	export := consumerExport(consumerExportResource("newkind", sch.GetName()))
	entry := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "providers.railgrid.ai/v1alpha1", "kind": "CatalogEntry", "metadata": map[string]any{"name": "planner"}, "spec": map[string]any{"export": map[string]any{"name": "planner"}}}}
	return sdkinstall.OrganizationBootstrap{APIExport: export, CatalogEntry: entry, Schemas: []*unstructured.Unstructured{sch}}
}

func TestOrgConsumerPolicyBootstrapPreflightsAllocatedConsumersBeforePublication(t *testing.T) {
	f := newConsumerPolicyFixture(t)
	if err := f.policy.PrepareEnable(context.Background(), "org-a", "team-a", f.provider); err != nil {
		t.Fatal(err)
	}
	mockConsumerQuotaControllerWithStatus(t, f.consumer, map[string]bool{"count/newkind.planner.example": true}, nil, nil)
	f.policy.discoveryFor = func(string) (discovery.DiscoveryInterface, error) {
		return consumerPolicyDiscovery{omit: map[schema.GroupResource]bool{{Group: "planner.example", Resource: "newkind"}: true}}, nil
	}
	if err := f.policy.PrepareBootstrap(context.Background(), "org-a", "planner", proposedBootstrap()); err != nil {
		t.Fatal(err)
	}
	hard := consumerQuotaForTest(t, f.consumer)
	if hard["count/newkind.planner.example"] != "4" || hard["count/ordinary.planner.example"] != "4" {
		t.Fatalf("upgrade lost old/new allocation: %v", hard)
	}
	quota, err := f.consumer.Resource(orgQuotaGVR).Namespace("admin").Get(context.Background(), OrgConsumerQuotaName, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	used, _, _ := unstructured.NestedStringMap(quota.Object, "status", "used")
	if _, found := used["count/newkind.planner.example"]; found {
		t.Fatalf("new unserved family unexpectedly has initialized usage: %v", used)
	}
	if err := f.policy.PrepareBootstrap(context.Background(), "org-a", "planner", proposedBootstrap()); err == nil {
		t.Fatal("reservation-only bootstrap retry was incorrectly treated as a new family")
	}
	for _, client := range f.clients {
		for _, action := range client.Actions() {
			if action.GetResource() == apiExportGVR && (action.GetVerb() == "create" || action.GetVerb() == "update" || action.GetVerb() == "patch") {
				t.Fatal("quota preflight published export")
			}
		}
	}
}

func TestOrgConsumerPolicyBootstrapRejectsReplacedConsumerAndFailedInventory(t *testing.T) {
	for _, scenario := range []string{"replaced workspace", "allocation list failure", "foreign annotation", "capacity overflow"} {
		t.Run(scenario, func(t *testing.T) {
			f := newConsumerPolicyFixture(t)
			if err := f.policy.PrepareEnable(context.Background(), "org-a", "team-a", f.provider); err != nil {
				t.Fatal(err)
			}
			switch scenario {
			case "replaced workspace":
				object, err := f.org.Tracker().Get(workspaceGVR, "", "team-a")
				if err != nil {
					t.Fatal(err)
				}
				ws := object.(*unstructured.Unstructured)
				_ = unstructured.SetNestedField(ws.Object, "replacement", "spec", "cluster")
				if err := f.org.Tracker().Update(workspaceGVR, ws, ""); err != nil {
					t.Fatal(err)
				}
			case "allocation list failure":
				f.parent.PrependReactor("list", "leases", func(k8stesting.Action) (bool, runtime.Object, error) {
					return true, nil, fmt.Errorf("inventory unavailable")
				})
			case "foreign annotation":
				object, err := f.parent.Tracker().Get(orgConsumerLeaseGVR, "admin", "consumer-cluster-a")
				if err != nil {
					t.Fatal(err)
				}
				lease := object.(*unstructured.Unstructured)
				annotations := lease.GetAnnotations()
				annotations[orgConsumerAllocationOrg] = "org-b"
				lease.SetAnnotations(annotations)
				if err := f.parent.Tracker().Update(orgConsumerLeaseGVR, lease, "admin"); err != nil {
					t.Fatal(err)
				}
			case "capacity overflow":
				f.policy.config.MaxConsumers = 1
				object, err := f.parent.Tracker().Get(orgConsumerLeaseGVR, "admin", "consumer-cluster-a")
				if err != nil {
					t.Fatal(err)
				}
				lease := object.(*unstructured.Unstructured)
				lease.SetName("consumer-extra")
				if err := f.parent.Tracker().Create(orgConsumerLeaseGVR, lease, "admin"); err != nil {
					t.Fatal(err)
				}
			}
			if err := f.policy.PrepareBootstrap(context.Background(), "org-a", "planner", proposedBootstrap()); err == nil {
				t.Fatal("unsafe consumer inventory accepted")
			}
			if consumerQuotaForTest(t, f.consumer)["count/newkind.planner.example"] != "" {
				t.Fatal("failed inventory changed consumer quota")
			}
		})
	}
}

func TestOrgConsumerPolicyDeleteWaitsForAllReferencingBindings(t *testing.T) {
	for _, phase := range []string{"Bound", "Binding", "NotReady", "terminating"} {
		t.Run(phase, func(t *testing.T) {
			f := newConsumerPolicyFixture(t)
			binding := policyBinding("planner", f.provider.APIExportPath, f.provider.APIExportName)
			_ = unstructured.SetNestedField(binding.Object, phase, "status", "phase")
			if phase == "terminating" {
				now := metav1.Now()
				binding.SetDeletionTimestamp(&now)
				binding.SetFinalizers([]string{"cleanup"})
			}
			if err := f.consumer.Tracker().Create(orgConsumerBindingGVR, binding, ""); err != nil {
				t.Fatal(err)
			}
			if err := f.policy.CheckDelete(context.Background(), "org-a", "planner", "planner"); !errors.Is(err, ErrOrgProviderInUse) {
				t.Fatalf("referenced export delete allowed: %v", err)
			}
			if err := f.policy.CheckDelete(context.Background(), "org-a", "planner", "renamed-export"); !errors.Is(err, ErrOrgProviderInUse) {
				t.Fatalf("export rename bypassed deletion audit: %v", err)
			}
			if err := f.consumer.Tracker().Delete(orgConsumerBindingGVR, "", "planner"); err != nil {
				t.Fatal(err)
			}
			if err := f.policy.CheckDelete(context.Background(), "org-a", "planner", "planner"); err != nil {
				t.Fatalf("fully removed binding still blocked: %v", err)
			}
		})
	}
}

func TestOrgConsumerPolicyLockIsPerOrgBoundedCancelableAndReclaimed(t *testing.T) {
	f := newConsumerPolicyFixture(t)
	a, err := f.policy.Lock(context.Background(), "org-a")
	if err != nil {
		t.Fatal(err)
	}
	b, err := f.policy.Lock(context.Background(), "org-b")
	if err != nil {
		t.Fatal("busy org blocked other org:", err)
	}
	if unlock, err := f.policy.Lock(context.Background(), "org-c"); err == nil {
		unlock()
		t.Fatal("unbounded lifecycle lock map")
	}
	if unlock, err := f.policy.Lock(context.Background(), "org-a"); err == nil {
		if unlock != nil {
			unlock()
		}
		t.Fatal("same-org concurrent work admitted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := f.policy.Lock(ctx, "org-c"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled work accepted: %v", err)
	}
	a()
	a()
	b()
	c, err := f.policy.Lock(context.Background(), "org-c")
	if err != nil {
		t.Fatal(err)
	}
	c()
	if len(f.policy.locks) != 0 {
		t.Fatal("idle organization names leaked lifecycle state")
	}
}
