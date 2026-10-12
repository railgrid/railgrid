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

package appauth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kcp-dev/sdk/apis/core"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/rest"
	k8stesting "k8s.io/client-go/testing"
	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"

	providersv1alpha1 "github.com/railgrid/railgrid/apis/providers/v1alpha1"
)

func TestAppAccessGrantMigrationReconcilesCurrentProjectGrantsAndReplays(t *testing.T) {
	client := brokerTestClient(brokerTestInstanceUID)
	brokerSeedLegacyGrant(t, client, brokerTestProjectUID, "member", "railgrid:member@example.test")
	handler := brokerTestHandler(client, true)
	clusterID := "tenant-cluster-id"
	baseClientFactory := handler.clients
	var gotCluster string
	handler.clients = func(cluster string) (dynamic.Interface, error) {
		gotCluster = cluster
		return baseClientFactory(cluster)
	}
	reconciler := &appAccessGrantMigrationReconciler{handler: handler}
	if err := reconciler.reconcileInstance(context.Background(), clusterID, "app-demo-prod"); err != nil {
		t.Fatal(err)
	}
	if gotCluster != clusterID {
		t.Fatalf("migration used cluster %q, want %q", gotCluster, clusterID)
	}
	role, err := client.Resource(appAccessClusterRoleGVR).Get(context.Background(), appAccessRoleName("app-demo-prod"), metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !appAccessRoleStateCurrent(role, brokerTestProject(client), brokerTestInstance(client), appAccessGrantRequest{
		Group: "infrastructure.railgrid.ai", Resource: "instances", Name: "app-demo-prod",
		UID: brokerTestInstanceUID, Project: "demo", ProjectUID: brokerTestProjectUID,
	}) {
		t.Fatalf("migrated role is not current: labels=%#v owners=%#v", role.GetLabels(), role.GetOwnerReferences())
	}
	binding, err := client.Resource(appAccessBindingGVR).Get(context.Background(), appAccessBindingName("app-demo-prod", "member"), metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(binding.GetOwnerReferences()) != 2 || binding.GetLabels()[appAccessInstanceUID] != brokerTestInstanceUID {
		t.Fatalf("migrated binding metadata = labels %#v owners %#v", binding.GetLabels(), binding.GetOwnerReferences())
	}
	identityRoleName := appAccessIdentityRoleName(appAccessGrantRequest{Name: "app-demo-prod", UID: brokerTestInstanceUID, ProjectUID: brokerTestProjectUID})
	if _, err := client.Resource(appAccessClusterRoleGVR).Get(context.Background(), identityRoleName, metav1.GetOptions{}); err != nil {
		t.Fatalf("migration did not create the UID-fenced identity role: %v", err)
	}
	identityBinding, err := client.Resource(appAccessBindingGVR).Get(context.Background(), appAccessIdentityBindingName(appAccessGrantRequest{
		Name: "app-demo-prod", UID: brokerTestInstanceUID, ProjectUID: brokerTestProjectUID,
	}, "member"), metav1.GetOptions{})
	if err != nil || bindingSubjectUser(identityBinding) != "railgrid:member@example.test" {
		t.Fatalf("migrated identity binding = %#v err=%v", identityBinding, err)
	}

	// A new reconciler after a restart replays persisted state and performs no
	// additional RBAC writes once the upgrade has converged.
	before := len(client.Actions())
	if err := (&appAccessGrantMigrationReconciler{handler: handler}).reconcileInstance(context.Background(), clusterID, "app-demo-prod"); err != nil {
		t.Fatal(err)
	}
	for _, action := range client.Actions()[before:] {
		if action.GetVerb() == "create" || action.GetVerb() == "update" || action.GetVerb() == "delete" {
			t.Fatalf("restart replay rewrote converged grants: %#v", action)
		}
	}
}

func TestAppAccessGrantMigrationUsesExactTenantCluster(t *testing.T) {
	const (
		clusterOne  = "tenant-cluster-one"
		clusterTwo  = "tenant-cluster-two"
		projectOne  = "project-one-uid"
		projectTwo  = "project-two-uid"
		instanceOne = "instance-one-uid"
		instanceTwo = "instance-two-uid"
	)
	first := brokerTestClientFor(projectOne, instanceOne)
	second := brokerTestClientFor(projectTwo, instanceTwo)
	brokerSeedLegacyGrant(t, first, projectOne, "member", "railgrid:first@example.test")
	brokerSeedLegacyGrant(t, second, projectTwo, "member", "railgrid:second@example.test")
	firstBefore := len(first.Actions())
	secondBefore := len(second.Actions())

	handler := brokerTestHandler(first, true)
	clients := map[string]*dynamicfake.FakeDynamicClient{
		clusterOne: first,
		clusterTwo: second,
	}
	handler.clients = func(cluster string) (dynamic.Interface, error) {
		client := clients[cluster]
		if client == nil {
			return nil, errors.New("unexpected tenant cluster " + cluster)
		}
		return client, nil
	}
	reconciler := &appAccessGrantMigrationReconciler{handler: handler}
	if err := reconciler.reconcileInstance(context.Background(), clusterOne, "app-demo-prod"); err != nil {
		t.Fatal(err)
	}
	assertNoRBACWrites(t, second.Actions()[secondBefore:])
	secondLegacyRole, err := second.Resource(appAccessClusterRoleGVR).Get(context.Background(), appAccessRoleName("app-demo-prod"), metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if secondLegacyRole.GetLabels()[appAccessProjectUID] != projectTwo || secondLegacyRole.GetLabels()[appAccessInstanceUID] != "" || len(secondLegacyRole.GetOwnerReferences()) != 0 {
		t.Fatalf("reconciling the first cluster changed the second cluster's legacy role: labels=%#v owners=%#v", secondLegacyRole.GetLabels(), secondLegacyRole.GetOwnerReferences())
	}
	secondLegacyBinding, err := second.Resource(appAccessBindingGVR).Get(context.Background(), appAccessBindingName("app-demo-prod", "member"), metav1.GetOptions{})
	if err != nil || bindingSubjectUser(secondLegacyBinding) != "railgrid:second@example.test" {
		t.Fatalf("second cluster legacy approval changed: binding=%#v err=%v", secondLegacyBinding, err)
	}
	if _, err := second.Resource(appAccessClusterRoleGVR).Get(context.Background(), appAccessIdentityRoleName(appAccessGrantRequest{Name: "app-demo-prod", UID: instanceTwo, ProjectUID: projectTwo}), metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("reconciling the first cluster created the second cluster's identity role: err=%v", err)
	}

	if err := reconciler.reconcileInstance(context.Background(), clusterTwo, "app-demo-prod"); err != nil {
		t.Fatal(err)
	}
	assertIdentityGrantSubject := func(testClient *dynamicfake.FakeDynamicClient, projectUID, instanceUID, subject string) {
		t.Helper()
		req := appAccessGrantRequest{Name: "app-demo-prod", ProjectUID: projectUID, UID: instanceUID}
		roleName := appAccessIdentityRoleName(req)
		if _, err := testClient.Resource(appAccessClusterRoleGVR).Get(context.Background(), roleName, metav1.GetOptions{}); err != nil {
			t.Fatalf("identity role %q missing: %v", roleName, err)
		}
		binding, err := testClient.Resource(appAccessBindingGVR).Get(context.Background(), appAccessIdentityBindingName(req, "member"), metav1.GetOptions{})
		if err != nil || bindingSubjectUser(binding) != subject {
			t.Fatalf("identity binding for %s/%s = %#v, want subject %q; err=%v", projectUID, instanceUID, binding, subject, err)
		}
	}
	assertIdentityGrantSubject(first, projectOne, instanceOne, "railgrid:first@example.test")
	assertIdentityGrantSubject(second, projectTwo, instanceTwo, "railgrid:second@example.test")
	if len(first.Actions()) == firstBefore || len(second.Actions()) == secondBefore {
		t.Fatalf("both tenant clusters should have independently migrated their grants: first actions=%d second actions=%d", len(first.Actions())-firstBefore, len(second.Actions())-secondBefore)
	}
}

func TestAppAccessGrantMigrationRetriesTemporaryRBACFailure(t *testing.T) {
	client := brokerTestClient(brokerTestInstanceUID)
	brokerSeedLegacyGrant(t, client, brokerTestProjectUID, "member", "railgrid:member@example.test")
	failOnce := true
	client.PrependReactor("update", "clusterroles", func(k8stesting.Action) (bool, runtime.Object, error) {
		if !failOnce {
			return false, nil, nil
		}
		failOnce = false
		return true, nil, errors.New("temporary RBAC write failure")
	})
	reconciler := &appAccessGrantMigrationReconciler{handler: brokerTestHandler(client, true)}
	if err := reconciler.reconcileInstance(context.Background(), "tenant-cluster-id", "app-demo-prod"); err == nil {
		t.Fatal("first migration succeeded despite injected RBAC failure")
	}
	if err := reconciler.reconcileInstance(context.Background(), "tenant-cluster-id", "app-demo-prod"); err != nil {
		t.Fatalf("migration did not recover on retry: %v", err)
	}
	if _, err := client.Resource(appAccessClusterRoleGVR).Get(context.Background(), appAccessIdentityRoleName(appAccessGrantRequest{
		Name: "app-demo-prod", UID: brokerTestInstanceUID, ProjectUID: brokerTestProjectUID,
	}), metav1.GetOptions{}); err != nil {
		t.Fatalf("retry did not create identity role: %v", err)
	}
}

func TestAppAccessGrantMigrationIgnoresForeignOrReplacementOwners(t *testing.T) {
	t.Run("foreign instance owner", func(t *testing.T) {
		client := brokerTestClient(brokerTestInstanceUID)
		brokerSeedLegacyGrant(t, client, brokerTestProjectUID, "member", "railgrid:member@example.test")
		instance, err := client.Resource(appAccessMigrationInstanceGVR).Get(context.Background(), "app-demo-prod", metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		instance.SetOwnerReferences([]metav1.OwnerReference{{APIVersion: "ai.railgrid.ai/v1alpha1", Kind: "Project", Name: "demo", UID: "foreign-project-uid"}})
		if _, err := client.Resource(appAccessMigrationInstanceGVR).Update(context.Background(), instance, metav1.UpdateOptions{}); err != nil {
			t.Fatal(err)
		}
		before := len(client.Actions())
		if err := (&appAccessGrantMigrationReconciler{handler: brokerTestHandler(client, true)}).reconcileInstance(context.Background(), "tenant-cluster-id", "app-demo-prod"); err != nil {
			t.Fatal(err)
		}
		assertNoRBACWrites(t, client.Actions()[before:])
	})

	t.Run("same Instance UID under replacement Project", func(t *testing.T) {
		const replacementProjectUID = "replacement-project-uid"
		client := brokerTestClientFor(replacementProjectUID, brokerTestInstanceUID)
		brokerSeedLegacyGrant(t, client, brokerTestProjectUID, "former-member", "railgrid:former@example.test")
		staleRole, err := client.Resource(appAccessClusterRoleGVR).Get(context.Background(), appAccessRoleName("app-demo-prod"), metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		staleRole.SetUID(types.UID("legacy-role-uid"))
		staleRole.SetResourceVersion("17")
		if _, err := client.Resource(appAccessClusterRoleGVR).Update(context.Background(), staleRole, metav1.UpdateOptions{}); err != nil {
			t.Fatal(err)
		}
		staleBinding, err := client.Resource(appAccessBindingGVR).Get(context.Background(), appAccessBindingName("app-demo-prod", "former-member"), metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		staleBinding.SetUID(types.UID("legacy-binding-uid"))
		staleBinding.SetResourceVersion("18")
		if _, err := client.Resource(appAccessBindingGVR).Update(context.Background(), staleBinding, metav1.UpdateOptions{}); err != nil {
			t.Fatal(err)
		}
		before := len(client.Actions())
		if err := (&appAccessGrantMigrationReconciler{handler: brokerTestHandler(client, true)}).reconcileInstance(context.Background(), "tenant-cluster-id", "app-demo-prod"); err != nil {
			t.Fatal(err)
		}
		if _, err := client.Resource(appAccessClusterRoleGVR).Get(context.Background(), appAccessRoleName("app-demo-prod"), metav1.GetOptions{}); !apierrors.IsNotFound(err) {
			t.Fatalf("stale name-based access role still grants the replacement: err=%v", err)
		}
		if _, err := client.Resource(appAccessBindingGVR).Get(context.Background(), appAccessBindingName("app-demo-prod", "former-member"), metav1.GetOptions{}); !apierrors.IsNotFound(err) {
			t.Fatalf("stale name-based access binding still grants the replacement: err=%v", err)
		}
		if _, err := client.Resource(appAccessClusterRoleGVR).Get(context.Background(), appAccessIdentityRoleName(appAccessGrantRequest{
			Name: "app-demo-prod", UID: brokerTestInstanceUID, ProjectUID: replacementProjectUID,
		}), metav1.GetOptions{}); !apierrors.IsNotFound(err) {
			t.Fatalf("stale grant was adopted into the replacement's UID-fenced role: err=%v", err)
		}
		deleteCount := 0
		for _, action := range client.Actions()[before:] {
			if action.GetVerb() != "delete" {
				continue
			}
			deleteCount++
			deleteAction, ok := action.(k8stesting.DeleteAction)
			if !ok {
				t.Fatalf("delete action type = %T, want DeleteActionWithOptions", action)
			}
			preconditions := deleteAction.GetDeleteOptions().Preconditions
			if preconditions == nil || preconditions.UID == nil || preconditions.ResourceVersion == nil {
				t.Fatalf("stale grant delete lacks UID/resourceVersion preconditions: %#v", preconditions)
			}
		}
		if deleteCount != 2 {
			t.Fatalf("stale role and binding delete count = %d, want 2", deleteCount)
		}
	})

	t.Run("missing Project UID is not adopted or deleted", func(t *testing.T) {
		const replacementProjectUID = "replacement-project-uid"
		client := brokerTestClientFor(replacementProjectUID, brokerTestInstanceUID)
		brokerSeedLegacyGrant(t, client, brokerTestProjectUID, "former-member", "railgrid:former@example.test")
		role, err := client.Resource(appAccessClusterRoleGVR).Get(context.Background(), appAccessRoleName("app-demo-prod"), metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		roleLabels := role.GetLabels()
		delete(roleLabels, appAccessProjectUID)
		role.SetLabels(roleLabels)
		if _, err := client.Resource(appAccessClusterRoleGVR).Update(context.Background(), role, metav1.UpdateOptions{}); err != nil {
			t.Fatal(err)
		}
		binding, err := client.Resource(appAccessBindingGVR).Get(context.Background(), appAccessBindingName("app-demo-prod", "former-member"), metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		bindingLabels := binding.GetLabels()
		delete(bindingLabels, appAccessProjectUID)
		binding.SetLabels(bindingLabels)
		if _, err := client.Resource(appAccessBindingGVR).Update(context.Background(), binding, metav1.UpdateOptions{}); err != nil {
			t.Fatal(err)
		}
		before := len(client.Actions())
		if err := (&appAccessGrantMigrationReconciler{handler: brokerTestHandler(client, true)}).reconcileInstance(context.Background(), "tenant-cluster-id", "app-demo-prod"); err != nil {
			t.Fatal(err)
		}
		assertNoRBACWrites(t, client.Actions()[before:])
		if _, err := client.Resource(appAccessClusterRoleGVR).Get(context.Background(), appAccessRoleName("app-demo-prod"), metav1.GetOptions{}); err != nil {
			t.Fatalf("unowned legacy role was removed: %v", err)
		}
		if _, err := client.Resource(appAccessBindingGVR).Get(context.Background(), appAccessBindingName("app-demo-prod", "former-member"), metav1.GetOptions{}); err != nil {
			t.Fatalf("unowned legacy binding was removed: %v", err)
		}
	})
}

func TestAppAccessGrantMigrationSkipsDeletingTargets(t *testing.T) {
	project, instance := brokerTestProject(brokerTestClient(brokerTestInstanceUID)), brokerTestInstance(brokerTestClient(brokerTestInstanceUID))
	now := metav1.Now()
	instance.SetDeletionTimestamp(&now)
	if _, ok := appAccessMigrationRequest(project, instance); ok {
		t.Fatal("deleting Instance remained eligible for migration")
	}
	instance.SetDeletionTimestamp(nil)
	project.SetDeletionTimestamp(&now)
	if _, ok := appAccessMigrationRequest(project, instance); ok {
		t.Fatal("deleting Project remained eligible for migration")
	}
}

func TestAppAccessMigrationProviderPathAndExportScope(t *testing.T) {
	for _, path := range []string{
		"root:railgrid:providers:infrastructure",
		"root:railgrid:tenants:org-123:providers:infrastructure",
	} {
		if !appAccessMigrationProviderPath(path, "infrastructure") {
			t.Errorf("trusted Infrastructure provider path %q rejected", path)
		}
	}
	for _, path := range []string{
		"root:railgrid:system:controllers",
		"root:railgrid:tenants:org-123:workspaces:ws-1",
		"root:railgrid:tenants:org-123:providers:other",
		"root:railgrid:tenants:bad/path:providers:infrastructure",
	} {
		if appAccessMigrationProviderPath(path, "infrastructure") {
			t.Errorf("untrusted provider CatalogEntry path %q accepted", path)
		}
	}
	for _, tc := range []struct {
		name     string
		group    string
		resource string
		want     bool
	}{
		{name: appAccessMigrationExportName, group: appAccessMigrationInstanceGVK.Group, resource: "instances", want: true},
		{name: appAccessMigrationExportName, group: "other.railgrid.ai", resource: "instances"},
		{name: appAccessMigrationExportName, group: appAccessMigrationInstanceGVK.Group, resource: "templates"},
		{name: "other.providers.railgrid.ai", group: appAccessMigrationInstanceGVK.Group, resource: "instances"},
	} {
		export := &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "apis.kcp.io/v1alpha2", "kind": "APIExport",
			"metadata": map[string]any{"name": tc.name},
			"spec":     map[string]any{"resources": []any{map[string]any{"group": tc.group, "name": tc.resource}}},
		}}
		if got := apiExportServesInfrastructureInstances(export); got != tc.want {
			t.Errorf("APIExport %s %s/%s accepted=%v, want %v", tc.name, tc.group, tc.resource, got, tc.want)
		}
	}
}

func TestAppAccessMigrationRetriesAuthoritativeExportMismatches(t *testing.T) {
	const (
		clusterID    = "provider-cluster-id"
		providerPath = "root:railgrid:providers:infrastructure"
		logicalPath  = "/clusters/provider-cluster-id/apis/core.kcp.io/v1alpha1/logicalclusters/cluster"
		exportPath   = "/clusters/root:railgrid:providers:infrastructure/apis/apis.kcp.io/v1alpha2/apiexports/infrastructure.providers.railgrid.ai"
		slicePath    = "/clusters/root:railgrid:providers:infrastructure/apis/apis.kcp.io/v1alpha1/apiexportendpointslices/infrastructure"
	)
	tests := []struct {
		name    string
		prepare func(*bool, *string, *string)
	}{
		{
			name: "APIExport missing Instances",
			prepare: func(hasInstances *bool, _ *string, _ *string) {
				*hasInstances = false
			},
		},
		{
			name: "endpoint slice references another export",
			prepare: func(_ *bool, exportName *string, _ *string) {
				*exportName = "other.providers.railgrid.ai"
			},
		},
		{
			name: "endpoint slice references another provider path",
			prepare: func(_ *bool, _ *string, path *string) {
				*path = "root:railgrid:tenants:other:providers:infrastructure"
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			hasInstances := true
			sliceExportName := appAccessMigrationExportName
			sliceExportPath := providerPath
			tc.prepare(&hasInstances, &sliceExportName, &sliceExportPath)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				var object map[string]any
				switch request.URL.Path {
				case logicalPath:
					object = map[string]any{
						"apiVersion": "core.kcp.io/v1alpha1", "kind": "LogicalCluster",
						"metadata": map[string]any{"name": "cluster", "annotations": map[string]any{core.LogicalClusterPathAnnotationKey: providerPath}},
					}
				case exportPath:
					resources := []any{}
					if hasInstances {
						resources = append(resources, map[string]any{"group": appAccessMigrationInstanceGVK.Group, "name": "instances"})
					}
					object = map[string]any{
						"apiVersion": "apis.kcp.io/v1alpha2", "kind": "APIExport",
						"metadata": map[string]any{"name": appAccessMigrationExportName}, "spec": map[string]any{"resources": resources},
					}
				case slicePath:
					object = map[string]any{
						"apiVersion": "apis.kcp.io/v1alpha1", "kind": "APIExportEndpointSlice",
						"metadata": map[string]any{"name": appAccessMigrationEndpointSlice},
						"spec":     map[string]any{"export": map[string]any{"name": sliceExportName, "path": sliceExportPath}},
					}
				default:
					http.NotFound(w, request)
					return
				}
				if err := json.NewEncoder(w).Encode(object); err != nil {
					t.Errorf("encode mock response: %v", err)
				}
			}))
			defer server.Close()

			source := &appAccessGrantMigrationSource{kcpConfig: &rest.Config{Host: server.URL}}
			entry := &providersv1alpha1.CatalogEntry{
				ObjectMeta: metav1.ObjectMeta{Name: "infrastructure"},
				Spec: providersv1alpha1.CatalogEntrySpec{Export: &providersv1alpha1.ProviderExport{
					Name: appAccessMigrationExportName,
					Resources: []providersv1alpha1.ProviderExportResource{{
						Name: "instances", APIVersion: "infrastructure.railgrid.ai/v1alpha1", Kind: "Instance",
					}},
				}},
			}
			key := appAccessMigrationScopeKey{cluster: clusterID, name: entry.Name}
			if _, eligible, err := source.verifiedDescriptor(context.Background(), key, entry); err == nil || eligible {
				t.Fatalf("authoritative mismatch eligible=%v err=%v, want retryable error", eligible, err)
			}

			hasInstances = true
			sliceExportName = appAccessMigrationExportName
			sliceExportPath = providerPath
			descriptor, eligible, err := source.verifiedDescriptor(context.Background(), key, entry)
			if err != nil || !eligible {
				t.Fatalf("corrected authoritative export eligible=%v err=%v", eligible, err)
			}
			if descriptor.key != key || descriptor.path != providerPath || descriptor.exportName != appAccessMigrationExportName {
				t.Fatalf("verified descriptor = %#v", descriptor)
			}
		})
	}
}

func TestAppAccessMigrationChildFailureRequeuesAndRestartsManager(t *testing.T) {
	leaderCtx, cancelLeader := context.WithCancel(context.Background())
	defer cancelLeader()
	queue := workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[mcreconcile.Request]())
	defer queue.ShutDown()
	failures := &appAccessMigrationFailureSource{pending: map[appAccessMigrationScopeKey]struct{}{}}
	descriptor := appAccessMigrationDescriptor{key: appAccessMigrationScopeKey{cluster: "tenant-cluster-id", name: "infrastructure"}, path: "root:railgrid:providers:infrastructure", exportName: appAccessMigrationExportName}
	started := make(chan struct{}, 2)
	finishFailedManager := make(chan struct{})
	drainObserved := make(chan struct{}, 1)
	factoryCalls := 0
	source := &appAccessGrantMigrationSource{
		leaderCtx: leaderCtx,
		children:  map[appAccessMigrationScopeKey]*appAccessMigrationChild{},
		failures:  failures,
		childManagerFactory: func(appAccessMigrationDescriptor) (migrationManager, error) {
			factoryCalls++
			switch factoryCalls {
			case 1:
				return nil, errors.New("temporary child manager setup failure")
			case 2:
				return fakeMigrationManager{started: started, run: func(context.Context) error {
					<-finishFailedManager
					return errors.New("temporary manager runtime failure")
				}}, nil
			default:
				return fakeMigrationManager{started: started, run: func(ctx context.Context) error {
					<-ctx.Done()
					drainObserved <- struct{}{}
					return nil
				}}, nil
			}
		},
	}
	if err := source.ensureChild(descriptor); err == nil {
		t.Fatal("synchronous manager setup error was not returned to the CatalogEntry reconciler")
	}
	if err := source.ensureChild(descriptor); err != nil {
		t.Fatal(err)
	}
	<-started
	source.mu.Lock()
	failedChild := source.children[descriptor.key]
	source.mu.Unlock()
	if failedChild == nil {
		t.Fatal("running child manager handle is missing")
	}
	close(finishFailedManager)
	<-failedChild.done
	failures.mu.Lock()
	pending := len(failures.pending)
	failures.mu.Unlock()
	if pending != 1 {
		t.Fatalf("child failure pending keys = %d, want one before source startup", pending)
	}
	if err := failures.Start(leaderCtx, queue); err != nil {
		t.Fatal(err)
	}
	request := make(chan mcreconcile.Request, 1)
	go func() {
		item, shutdown := queue.Get()
		if !shutdown {
			request <- item
			queue.Done(item)
			queue.Forget(item)
		}
	}()
	select {
	case got := <-request:
		if got.ClusterName != multicluster.ClusterName(descriptor.key.cluster) || got.Name != descriptor.key.name {
			t.Fatalf("runtime failure requeued %#v", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("runtime manager failure did not requeue the persisted CatalogEntry")
	}
	if err := source.ensureChild(descriptor); err != nil {
		t.Fatalf("manager did not resume after requeue: %v", err)
	}
	<-started
	cancelLeader()
	if err := source.stopAll(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-drainObserved:
	case <-time.After(5 * time.Second):
		t.Fatal("parent shutdown returned before child manager drained")
	}
}

type fakeMigrationManager struct {
	started chan<- struct{}
	run     func(context.Context) error
}

func (m fakeMigrationManager) Start(ctx context.Context) error {
	m.started <- struct{}{}
	return m.run(ctx)
}

func brokerTestProject(client *dynamicfake.FakeDynamicClient) *unstructured.Unstructured {
	project, err := client.Resource(appAccessProjectGVR).Get(context.Background(), "demo", metav1.GetOptions{})
	if err != nil {
		panic(err)
	}
	return project
}

func brokerTestInstance(client *dynamicfake.FakeDynamicClient) *unstructured.Unstructured {
	instance, err := client.Resource(appAccessMigrationInstanceGVR).Get(context.Background(), "app-demo-prod", metav1.GetOptions{})
	if err != nil {
		panic(err)
	}
	return instance
}

func assertNoRBACWrites(t *testing.T, actions []k8stesting.Action) {
	t.Helper()
	for _, action := range actions {
		if action.GetResource().Group == "rbac.authorization.k8s.io" && (action.GetVerb() == "create" || action.GetVerb() == "update" || action.GetVerb() == "delete") {
			t.Fatalf("unexpected RBAC write: %#v", action)
		}
	}
}
