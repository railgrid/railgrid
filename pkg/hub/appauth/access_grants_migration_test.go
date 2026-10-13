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
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/rest"
	k8stesting "k8s.io/client-go/testing"
	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"

	providersv1alpha1 "github.com/railgrid/railgrid/apis/providers/v1alpha1"
)

func TestAppAccessGrantMigrationReconcilesCurrentProjectGrantsAndReplays(t *testing.T) {
	client := brokerTestClient(brokerTestInstanceUID)
	brokerSeedLegacyGrant(t, client, brokerTestProjectUID, "member", "railgrid:member@example.test")
	client.PrependReactor("update", "clusterrolebindings", func(action k8stesting.Action) (bool, runtime.Object, error) {
		updated := action.(k8stesting.UpdateAction).GetObject().(*unstructured.Unstructured)
		oldObject, err := client.Tracker().Get(appAccessBindingGVR, "", updated.GetName())
		if err != nil {
			return true, nil, err
		}
		oldBinding := oldObject.(*unstructured.Unstructured)
		oldRoleRef, _, _ := unstructured.NestedMap(oldBinding.Object, "roleRef")
		newRoleRef, _, _ := unstructured.NestedMap(updated.Object, "roleRef")
		if !equality.Semantic.DeepEqual(oldRoleRef, newRoleRef) {
			return true, nil, apierrors.NewInvalid(schema.GroupKind{Group: appAccessBindingGVR.Group, Kind: "ClusterRoleBinding"}, updated.GetName(), field.ErrorList{
				field.Forbidden(field.NewPath("roleRef"), "field is immutable"),
			})
		}
		return false, nil, nil
	})
	beforeMigration := len(client.Actions())
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
	request := appAccessGrantRequest{
		Group: "infrastructure.railgrid.ai", Resource: "instances", Name: "app-demo-prod",
		UID: brokerTestInstanceUID, Project: "demo", ProjectUID: brokerTestProjectUID,
	}
	roleName := appAccessUIDRoleName(request)
	role, err := client.Resource(appAccessClusterRoleGVR).Get(context.Background(), roleName, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !appAccessRoleStateCurrent(role, brokerTestProject(client), brokerTestInstance(client), request) {
		t.Fatalf("migrated role is not current: labels=%#v owners=%#v", role.GetLabels(), role.GetOwnerReferences())
	}
	if role.GetAnnotations()[appAccessMigrationAnnotation] != appAccessMigrationMarker(brokerTestProject(client), brokerTestInstance(client), request) {
		t.Fatalf("durable migration marker missing: %#v", role.GetAnnotations())
	}
	legacyRole, err := client.Resource(appAccessClusterRoleGVR).Get(context.Background(), appAccessRoleName("app-demo-prod"), metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	legacyRules, _, _ := unstructured.NestedSlice(legacyRole.Object, "rules")
	if len(legacyRules) != 0 {
		t.Fatalf("legacy role remained active after migration: %#v", legacyRules)
	}
	legacyBindingName := appAccessBindingName("app-demo-prod", "member")
	if _, err := client.Resource(appAccessBindingGVR).Get(context.Background(), legacyBindingName, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("old immutable-roleRef binding still exists after migration: %v", err)
	}
	bindingName := appAccessUIDBindingName(request, "member", appAccessGrantSourcePublishing)
	binding, err := client.Resource(appAccessBindingGVR).Get(context.Background(), bindingName, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(binding.GetOwnerReferences()) != 2 || binding.GetLabels()[appAccessInstanceUID] != brokerTestInstanceUID ||
		binding.GetLabels()[appAccessSourceLabel] != appAccessGrantSourcePublishing || !validRoleReference(binding, roleName) {
		t.Fatalf("migrated binding metadata = labels %#v owners %#v", binding.GetLabels(), binding.GetOwnerReferences())
	}
	newBindingCreatedAt, oldBindingDeletedAt := -1, -1
	for i, action := range client.Actions()[beforeMigration:] {
		if action.GetResource().Resource != appAccessBindingGVR.Resource {
			continue
		}
		if create, ok := action.(k8stesting.CreateAction); ok && create.GetObject().(*unstructured.Unstructured).GetName() == bindingName {
			newBindingCreatedAt = i
		}
		if delete, ok := action.(k8stesting.DeleteAction); ok && delete.GetName() == legacyBindingName {
			oldBindingDeletedAt = i
		}
	}
	if newBindingCreatedAt < 0 || oldBindingDeletedAt < 0 || newBindingCreatedAt >= oldBindingDeletedAt {
		t.Fatalf("migration did not create the new binding before deleting the immutable old binding: create=%d delete=%d", newBindingCreatedAt, oldBindingDeletedAt)
	}
	identityFence, ok := authenticationAppAccessSubresource(request.ProjectUID, request.UID)
	if !ok || brokerRoleHasResource(role, "instances/"+identityFence) {
		t.Fatalf("publishing migration created identity authorization: %#v", role.Object["rules"])
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

func TestAppAccessGrantMigrationFencesStaleUIDRoleWithoutLegacyRole(t *testing.T) {
	client := brokerTestClientFor("replacement-project-uid", "replacement-instance-uid")
	currentProject, currentInstance := brokerTestProject(client), brokerTestInstance(client)
	oldProject := currentProject.DeepCopy()
	oldProject.SetUID(types.UID("old-project-uid"))
	oldInstance := currentInstance.DeepCopy()
	oldInstance.SetUID(types.UID("old-instance-uid"))
	oldInstance.SetOwnerReferences([]metav1.OwnerReference{{
		APIVersion: "ai.railgrid.ai/v1alpha1", Kind: "Project", Name: oldProject.GetName(), UID: oldProject.GetUID(),
	}})
	oldReq := appAccessGrantRequest{
		Group: "infrastructure.railgrid.ai", Resource: "instances", Name: "app-demo-prod",
		Project: oldProject.GetName(), ProjectUID: string(oldProject.GetUID()), UID: string(oldInstance.GetUID()),
	}
	staleRole := newAppAccessRole(oldProject, oldInstance, oldReq)
	staleRole.SetUID(types.UID("stale-access-role-uid"))
	staleRole.SetResourceVersion("47")
	if _, err := client.Resource(appAccessClusterRoleGVR).Create(context.Background(), staleRole, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	staleBinding := newAppAccessBinding(oldProject, oldInstance, oldReq, IdentityAccount{
		User: "member", RBACIdentity: "railgrid:member@example.test",
	}, appAccessUIDRoleName(oldReq), appAccessGrantSourcePublishing)
	staleBinding.SetUID(types.UID("stale-access-binding-uid"))
	staleBinding.SetResourceVersion("48")
	if _, err := client.Resource(appAccessBindingGVR).Create(context.Background(), staleBinding, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}

	before := len(client.Actions())
	if err := (&appAccessGrantMigrationReconciler{handler: brokerTestHandler(client, true)}).reconcileInstance(context.Background(), "tenant-cluster-id", "app-demo-prod"); err != nil {
		t.Fatalf("replacement migration failed to fence stale access role: %v", err)
	}
	persistedRole, err := client.Resource(appAccessClusterRoleGVR).Get(context.Background(), staleRole.GetName(), metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	staleRules, _, _ := unstructured.NestedSlice(persistedRole.Object, "rules")
	if len(staleRules) != 0 || persistedRole.GetLabels()[appAccessProjectUID] != string(oldProject.GetUID()) ||
		persistedRole.GetLabels()[appAccessInstanceUID] != string(oldInstance.GetUID()) || persistedRole.GetUID() != staleRole.GetUID() {
		t.Fatalf("stale role was not safely tombstoned without adoption: labels=%#v rules=%#v uid=%q", persistedRole.GetLabels(), staleRules, persistedRole.GetUID())
	}
	persistedBinding, err := client.Resource(appAccessBindingGVR).Get(context.Background(), staleBinding.GetName(), metav1.GetOptions{})
	if err != nil || !validRoleReference(persistedBinding, staleRole.GetName()) {
		t.Fatalf("old binding should remain attached to the inert stale role: binding=%#v err=%v", persistedBinding, err)
	}
	if _, err := client.Resource(appAccessClusterRoleGVR).Get(context.Background(), appAccessRoleName("app-demo-prod"), metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("test unexpectedly has a static legacy role: %v", err)
	}
	roleFenceWrite := false
	roleListFound := false
	for _, action := range client.Actions()[before:] {
		if action.GetVerb() == "list" && action.GetResource().Resource == appAccessClusterRoleGVR.Resource {
			roleListFound = true
			if action.(k8stesting.ListAction).GetListRestrictions().Labels.String() == "" ||
				action.(k8stesting.ListAction).GetListRestrictions().Labels.String() == "*" {
				t.Fatalf("stale UID role scan was not app/Project scoped: %#v", action)
			}
		}
		if action.GetVerb() == "list" && action.GetResource().Resource == appAccessBindingGVR.Resource {
			t.Fatalf("stale UID fencing performed a workspace binding scan: %#v", action)
		}
		if action.GetVerb() == "update" && action.GetResource().Resource == appAccessClusterRoleGVR.Resource {
			updated := action.(k8stesting.UpdateAction).GetObject().(*unstructured.Unstructured)
			if updated.GetName() == staleRole.GetName() && updated.GetUID() == staleRole.GetUID() && updated.GetResourceVersion() == "47" {
				roleFenceWrite = true
			}
		}
		if action.GetVerb() == "delete" && action.GetResource().Resource == appAccessBindingGVR.Resource {
			t.Fatalf("stale binding was deleted instead of rendered inert by its role: %#v", action)
		}
	}
	if !roleFenceWrite || !roleListFound {
		t.Fatal("stale UID role was not fenced with its original UID and resourceVersion")
	}
}

func TestAppAccessGrantMigrationPreservesPublishingAndAuthenticationSources(t *testing.T) {
	client := brokerTestClient(brokerTestInstanceUID)
	brokerSeedLegacyGrant(t, client, brokerTestProjectUID, "member", "railgrid:member@example.test")
	request := appAccessGrantRequest{
		Group: "infrastructure.railgrid.ai", Resource: "instances", Name: "app-demo-prod",
		Project: "demo", ProjectUID: brokerTestProjectUID, UID: brokerTestInstanceUID,
	}
	project, instance := brokerTestProject(client), brokerTestInstance(client)
	account := IdentityAccount{User: "member", RBACIdentity: "railgrid:member@example.test"}
	currentAuthRole := newAppAccessRoleForSource(project, instance, request, appAccessGrantSourceAuthentication)
	if _, err := client.Resource(appAccessClusterRoleGVR).Create(context.Background(), currentAuthRole, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	currentAuthBinding := newAppAccessBinding(project, instance, request, account,
		appAccessUIDRoleNameForSource(request, appAccessGrantSourceAuthentication), appAccessGrantSourceAuthentication)
	if _, err := client.Resource(appAccessBindingGVR).Create(context.Background(), currentAuthBinding, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := (&appAccessGrantMigrationReconciler{handler: brokerTestHandler(client, true)}).reconcileInstance(context.Background(), "tenant-cluster-id", request.Name); err != nil {
		t.Fatal(err)
	}
	identityFence, ok := authenticationAppAccessSubresource(request.ProjectUID, request.UID)
	if !ok {
		t.Fatal("could not construct authentication fence")
	}
	for _, source := range []string{appAccessGrantSourcePublishing, appAccessGrantSourceAuthentication} {
		bindingName := appAccessUIDBindingName(request, "member", source)
		binding, err := client.Resource(appAccessBindingGVR).Get(context.Background(), bindingName, metav1.GetOptions{})
		roleName := appAccessUIDRoleNameForSource(request, source)
		if err != nil || binding.GetLabels()[appAccessSourceLabel] != source || !validRoleReference(binding, roleName) {
			t.Fatalf("%s grant was not migrated independently: binding=%#v err=%v", source, binding, err)
		}
		role, err := client.Resource(appAccessClusterRoleGVR).Get(context.Background(), roleName, metav1.GetOptions{})
		if err != nil {
			t.Fatalf("%s role missing: %v", source, err)
		}
		if brokerRoleHasResource(role, "instances/"+identityFence) != (source == appAccessGrantSourceAuthentication) {
			t.Fatalf("%s role has wrong authentication permission: %#v", source, role.Object["rules"])
		}
	}
	if !brokerSARAllowed(client, account.RBACIdentity, request.Group, request.Resource+"/"+identityFence, request.Name, AccessVerb) {
		t.Fatal("the existing UID-fenced authentication grant lost its identity authorization")
	}
	if _, err := client.Resource(appAccessClusterRoleGVR).Get(context.Background(), appAccessIdentityRoleName(request), metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("migration created a separate identity role: %v", err)
	}
	if _, err := client.Resource(appAccessBindingGVR).Get(context.Background(), appAccessBindingName(request.Name, "member"), metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("deployed unsourced publishing binding survived migration: %v", err)
	}
}

func TestAppAccessGrantMigrationLeavesUnsupportedSourceLabeledStaticBindingInert(t *testing.T) {
	client := brokerTestClient(brokerTestInstanceUID)
	brokerSeedLegacyGrant(t, client, brokerTestProjectUID, "member", "railgrid:member@example.test")
	request := appAccessGrantRequest{
		Group: "infrastructure.railgrid.ai", Resource: "instances", Name: "app-demo-prod",
		Project: "demo", ProjectUID: brokerTestProjectUID, UID: brokerTestInstanceUID,
	}
	unsupported := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "rbac.authorization.k8s.io/v1", "kind": "ClusterRoleBinding",
		"metadata": map[string]any{"name": appAccessBindingNameForSource(request.Name, "member", appAccessGrantSourceAuthentication), "labels": map[string]any{
			appAccessLabel: request.Name, appAccessProject: request.Project, appAccessProjectUID: request.ProjectUID,
			appAccessUserLabel: "member", appAccessSourceLabel: appAccessGrantSourceAuthentication,
		}},
		"subjects": []any{map[string]any{"kind": "User", "apiGroup": "rbac.authorization.k8s.io", "name": "railgrid:member@example.test"}},
		"roleRef":  map[string]any{"apiGroup": "rbac.authorization.k8s.io", "kind": "ClusterRole", "name": appAccessRoleName(request.Name)},
	}}
	if _, err := client.Resource(appAccessBindingGVR).Create(context.Background(), unsupported, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, managed := appAccessMigrationBindingSource(unsupported, brokerTestProject(client), brokerTestInstance(client), request); managed {
		t.Fatal("unsupported source-labeled static binding was recognized as a deployed legacy grant")
	}

	if err := (&appAccessGrantMigrationReconciler{handler: brokerTestHandler(client, true)}).reconcileInstance(context.Background(), "tenant-cluster-id", request.Name); err != nil {
		t.Fatal(err)
	}
	persisted, err := client.Resource(appAccessBindingGVR).Get(context.Background(), unsupported.GetName(), metav1.GetOptions{})
	if err != nil || !validRoleReference(persisted, appAccessRoleName(request.Name)) ||
		persisted.GetLabels()[appAccessSourceLabel] != appAccessGrantSourceAuthentication {
		t.Fatalf("unsupported source-labeled binding was adopted or removed: binding=%#v err=%v", persisted, err)
	}
	legacyRole, err := client.Resource(appAccessClusterRoleGVR).Get(context.Background(), appAccessRoleName(request.Name), metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	legacyRules, _, _ := unstructured.NestedSlice(legacyRole.Object, "rules")
	if len(legacyRules) != 0 {
		t.Fatalf("unsupported binding remained effective through the static role: %#v", legacyRules)
	}
	if _, err := client.Resource(appAccessClusterRoleGVR).Get(context.Background(), appAccessUIDRoleNameForSource(request, appAccessGrantSourceAuthentication), metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("migration created an authentication role from an unsupported binding: %v", err)
	}
	if _, err := client.Resource(appAccessBindingGVR).Get(context.Background(), appAccessUIDBindingName(request, "member", appAccessGrantSourceAuthentication), metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("migration created an authentication binding from an unsupported binding: %v", err)
	}
	identityFence, ok := authenticationAppAccessSubresource(request.ProjectUID, request.UID)
	if !ok || brokerSARAllowed(client, "railgrid:member@example.test", request.Group, request.Resource+"/"+identityFence, request.Name, AccessVerb) {
		t.Fatal("unsupported source-labeled legacy binding authorized authentication")
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
	factoryCalls := map[string]int{}
	handler.clients = func(cluster string) (dynamic.Interface, error) {
		factoryCalls[cluster]++
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
	secondRules, _, _ := unstructured.NestedSlice(secondLegacyRole.Object, "rules")
	if len(secondRules) != 2 || secondLegacyRole.GetLabels()[appAccessProjectUID] != projectTwo || secondLegacyRole.GetLabels()[appAccessInstanceUID] != "" || len(secondLegacyRole.GetOwnerReferences()) != 0 {
		t.Fatalf("reconciling the first cluster changed the second cluster's legacy role: labels=%#v owners=%#v rules=%#v", secondLegacyRole.GetLabels(), secondLegacyRole.GetOwnerReferences(), secondRules)
	}
	secondLegacyBinding, err := second.Resource(appAccessBindingGVR).Get(context.Background(), appAccessBindingName("app-demo-prod", "member"), metav1.GetOptions{})
	if err != nil || bindingSubjectUser(secondLegacyBinding) != "railgrid:second@example.test" || !validRoleReference(secondLegacyBinding, appAccessRoleName("app-demo-prod")) {
		t.Fatalf("second cluster legacy approval changed: binding=%#v err=%v", secondLegacyBinding, err)
	}
	if _, err := second.Resource(appAccessClusterRoleGVR).Get(context.Background(), appAccessUIDRoleNameForSource(appAccessGrantRequest{Name: "app-demo-prod", UID: instanceTwo, ProjectUID: projectTwo}, appAccessGrantSourcePublishing), metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("reconciling the first cluster created the second cluster's primary role: err=%v", err)
	}

	if err := reconciler.reconcileInstance(context.Background(), clusterTwo, "app-demo-prod"); err != nil {
		t.Fatal(err)
	}
	assertPrimaryGrantSubject := func(testClient *dynamicfake.FakeDynamicClient, projectUID, instanceUID, subject string) {
		t.Helper()
		req := appAccessGrantRequest{Name: "app-demo-prod", ProjectUID: projectUID, UID: instanceUID}
		roleName := appAccessUIDRoleNameForSource(req, appAccessGrantSourcePublishing)
		if _, err := testClient.Resource(appAccessClusterRoleGVR).Get(context.Background(), roleName, metav1.GetOptions{}); err != nil {
			t.Fatalf("primary role %q missing: %v", roleName, err)
		}
		binding, err := testClient.Resource(appAccessBindingGVR).Get(context.Background(), appAccessUIDBindingName(req, "member", appAccessGrantSourcePublishing), metav1.GetOptions{})
		if err != nil || bindingSubjectUser(binding) != subject {
			t.Fatalf("primary binding for %s/%s = %#v, want subject %q; err=%v", projectUID, instanceUID, binding, subject, err)
		}
	}
	assertPrimaryGrantSubject(first, projectOne, instanceOne, "railgrid:first@example.test")
	assertPrimaryGrantSubject(second, projectTwo, instanceTwo, "railgrid:second@example.test")
	if err := reconciler.reconcileInstance(context.Background(), clusterOne, "app-demo-prod"); err != nil {
		t.Fatalf("restarted reconciliation failed: %v", err)
	}
	if factoryCalls[clusterOne] != 1 || factoryCalls[clusterTwo] != 1 {
		t.Fatalf("workspace clients were recreated for repeated reconciliation: %#v", factoryCalls)
	}
	if len(first.Actions()) == firstBefore || len(second.Actions()) == secondBefore {
		t.Fatalf("both tenant clusters should have independently migrated their grants: first actions=%d second actions=%d", len(first.Actions())-firstBefore, len(second.Actions())-secondBefore)
	}
}

func TestAppAccessMigrationSkipsStatusUpdatesAndAcceptsOwnershipChanges(t *testing.T) {
	p := appAccessMigrationInstancePredicate()
	managed := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "infrastructure.railgrid.ai/v1alpha1", "kind": "Instance",
		"metadata": map[string]any{"name": "app-demo-prod", "labels": map[string]any{appAccessProject: "demo"}, "generation": int64(3)},
		"status":   map[string]any{"phase": "ready"},
	}}
	statusOnly := managed.DeepCopy()
	statusOnly.Object["status"] = map[string]any{"phase": "provisioning"}
	if p.Update(event.UpdateEvent{ObjectOld: managed, ObjectNew: statusOnly}) {
		t.Fatal("status-only Instance update should not trigger a tenant RBAC scan")
	}
	specChange := managed.DeepCopy()
	specChange.SetGeneration(4)
	if !p.Update(event.UpdateEvent{ObjectOld: managed, ObjectNew: specChange}) {
		t.Fatal("Instance generation change did not trigger migration reconciliation")
	}
	ownerChange := managed.DeepCopy()
	ownerChange.SetOwnerReferences([]metav1.OwnerReference{{APIVersion: "ai.railgrid.ai/v1alpha1", Kind: "Project", Name: "demo", UID: "project-uid"}})
	if !p.Update(event.UpdateEvent{ObjectOld: managed, ObjectNew: ownerChange}) {
		t.Fatal("Instance owner change did not trigger migration reconciliation")
	}
	labelChange := managed.DeepCopy()
	labelChange.SetLabels(map[string]string{appAccessProject: "other"})
	if !p.Update(event.UpdateEvent{ObjectOld: managed, ObjectNew: labelChange}) {
		t.Fatal("managed Project label change did not trigger migration reconciliation")
	}
	unmanaged := managed.DeepCopy()
	unmanaged.GetLabels()[appAccessProject] = ""
	if p.Update(event.UpdateEvent{ObjectOld: unmanaged, ObjectNew: statusOnly}) {
		t.Fatal("unmanaged Instance status update triggered a migration scan")
	}
}

func TestAppAccessGrantMigrationContinuesPastMalformedBindingAndBoundsLists(t *testing.T) {
	client := brokerTestClient(brokerTestInstanceUID)
	brokerSeedLegacyGrant(t, client, brokerTestProjectUID, "member", "railgrid:member@example.test")
	malformed := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "rbac.authorization.k8s.io/v1", "kind": "ClusterRoleBinding",
		"metadata": map[string]any{"name": "foreign-name", "labels": map[string]any{
			appAccessLabel: "app-demo-prod", appAccessProject: "demo", appAccessProjectUID: brokerTestProjectUID,
			appAccessUserLabel: "stranger",
		}},
		"subjects": []any{map[string]any{"kind": "User", "apiGroup": "rbac.authorization.k8s.io", "name": "railgrid:stranger@example.test"}},
		"roleRef":  map[string]any{"apiGroup": "rbac.authorization.k8s.io", "kind": "ClusterRole", "name": appAccessRoleName("app-demo-prod")},
	}}
	if _, err := client.Resource(appAccessBindingGVR).Create(context.Background(), malformed, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	before := len(client.Actions())
	if err := (&appAccessGrantMigrationReconciler{handler: brokerTestHandler(client, true)}).reconcileInstance(context.Background(), "tenant-cluster-id", "app-demo-prod"); err != nil {
		t.Fatalf("malformed grant prevented valid migration from converging: %v", err)
	}
	activeRoleName := appAccessUIDRoleName(appAccessGrantRequest{Name: "app-demo-prod", UID: brokerTestInstanceUID, ProjectUID: brokerTestProjectUID})
	validBinding, err := client.Resource(appAccessBindingGVR).Get(context.Background(), appAccessUIDBindingName(appAccessGrantRequest{
		Name: "app-demo-prod", UID: brokerTestInstanceUID, ProjectUID: brokerTestProjectUID,
	}, "member", appAccessGrantSourcePublishing), metav1.GetOptions{})
	if err != nil || !validRoleReference(validBinding, activeRoleName) || validBinding.GetLabels()[appAccessSourceLabel] != appAccessGrantSourcePublishing {
		t.Fatalf("valid binding was not migrated: %#v err=%v", validBinding, err)
	}
	malformedAfter, err := client.Resource(appAccessBindingGVR).Get(context.Background(), "foreign-name", metav1.GetOptions{})
	if err != nil || !validRoleReference(malformedAfter, appAccessRoleName("app-demo-prod")) {
		t.Fatalf("malformed binding was mutated: %#v err=%v", malformedAfter, err)
	}
	legacyRole, err := client.Resource(appAccessClusterRoleGVR).Get(context.Background(), appAccessRoleName("app-demo-prod"), metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	legacyRules, _, _ := unstructured.NestedSlice(legacyRole.Object, "rules")
	if len(legacyRules) != 0 {
		t.Fatalf("malformed retained binding remained effective through legacy role: %#v", legacyRules)
	}
	listCount := 0
	for _, action := range client.Actions()[before:] {
		if action.GetVerb() != "list" || action.GetResource().Resource != appAccessBindingGVR.Resource {
			continue
		}
		listCount++
		listAction, ok := action.(interface{ GetListOptions() metav1.ListOptions })
		if !ok {
			t.Fatalf("list action %T does not expose ListOptions", action)
		}
		options := listAction.GetListOptions()
		restrictions := action.(k8stesting.ListAction).GetListRestrictions()
		if options.LabelSelector == "" || restrictions.Labels.String() == "" || restrictions.Labels.String() == "*" {
			t.Fatalf("migration list is not bounded/app-scoped: %#v", options)
		}
	}
	if listCount != 1 {
		t.Fatalf("migration performed %d ClusterRoleBinding lists, want one app-scoped bounded list", listCount)
	}
}

func TestAppAccessGrantMigrationFollowsContinueTokens(t *testing.T) {
	client := brokerTestClient(brokerTestInstanceUID)
	brokerSeedLegacyGrant(t, client, brokerTestProjectUID, "member", "railgrid:member@example.test")
	legacyBinding, err := client.Resource(appAccessBindingGVR).Get(context.Background(), appAccessBindingName("app-demo-prod", "member"), metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	listCalls := 0
	client.PrependReactor("list", "clusterrolebindings", func(k8stesting.Action) (bool, runtime.Object, error) {
		listCalls++
		list := &unstructured.UnstructuredList{Object: map[string]any{
			"apiVersion": "rbac.authorization.k8s.io/v1", "kind": "ClusterRoleBindingList",
			"metadata": map[string]any{},
		}}
		if listCalls == 1 {
			list.SetContinue("next-page")
			list.Items = []unstructured.Unstructured{*legacyBinding.DeepCopy()}
		}
		return true, list, nil
	})
	if err := (&appAccessGrantMigrationReconciler{handler: brokerTestHandler(client, true)}).reconcileInstance(context.Background(), "tenant-cluster-id", "app-demo-prod"); err != nil {
		t.Fatal(err)
	}
	if listCalls != 2 {
		t.Fatalf("migration made %d paginated list calls, want two", listCalls)
	}
	binding, err := client.Resource(appAccessBindingGVR).Get(context.Background(), appAccessUIDBindingName(appAccessGrantRequest{
		Name: "app-demo-prod", ProjectUID: brokerTestProjectUID, UID: brokerTestInstanceUID,
	}, "member", appAccessGrantSourcePublishing), metav1.GetOptions{})
	if err != nil || !validRoleReference(binding, appAccessUIDRoleName(appAccessGrantRequest{
		Name: "app-demo-prod", ProjectUID: brokerTestProjectUID, UID: brokerTestInstanceUID,
	})) {
		t.Fatalf("grant from first page was not migrated: %#v err=%v", binding, err)
	}
}

func TestAppAccessLegacyTombstoneLeavesUnlabelledForeignBindingsInert(t *testing.T) {
	const replacementProjectUID = "replacement-project-uid"
	client := brokerTestClientFor(replacementProjectUID, brokerTestInstanceUID)
	brokerSeedLegacyGrant(t, client, brokerTestProjectUID, "former-member", "railgrid:former@example.test")
	foreign := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "rbac.authorization.k8s.io/v1", "kind": "ClusterRoleBinding",
		"metadata": map[string]any{"name": "unlabelled-foreign-binding"},
		"subjects": []any{map[string]any{"kind": "User", "apiGroup": "rbac.authorization.k8s.io", "name": "foreign-user"}},
		"roleRef":  map[string]any{"apiGroup": "rbac.authorization.k8s.io", "kind": "ClusterRole", "name": appAccessRoleName("app-demo-prod")},
	}}
	if _, err := client.Resource(appAccessBindingGVR).Create(context.Background(), foreign, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	before := len(client.Actions())
	if err := (&appAccessGrantMigrationReconciler{handler: brokerTestHandler(client, true)}).reconcileInstance(context.Background(), "tenant-cluster-id", "app-demo-prod"); err != nil {
		t.Fatal(err)
	}
	foreignAfter, err := client.Resource(appAccessBindingGVR).Get(context.Background(), foreign.GetName(), metav1.GetOptions{})
	if err != nil || !validRoleReference(foreignAfter, appAccessRoleName("app-demo-prod")) || bindingSubjectUser(foreignAfter) != "foreign-user" {
		t.Fatalf("foreign binding was adopted or removed: %#v err=%v", foreignAfter, err)
	}
	legacyRole, err := client.Resource(appAccessClusterRoleGVR).Get(context.Background(), appAccessRoleName("app-demo-prod"), metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	rules, _, _ := unstructured.NestedSlice(legacyRole.Object, "rules")
	if len(rules) != 0 {
		t.Fatalf("unlabelled foreign binding remains privileged after same-name Project recreation: %#v", rules)
	}
	for _, action := range client.Actions()[before:] {
		if action.GetVerb() == "list" && action.GetResource().Resource == appAccessBindingGVR.Resource {
			t.Fatal("stale Project role cleanup performed a workspace-wide binding list")
		}
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
	if _, err := client.Resource(appAccessClusterRoleGVR).Get(context.Background(), appAccessUIDRoleNameForSource(appAccessGrantRequest{
		Name: "app-demo-prod", UID: brokerTestInstanceUID, ProjectUID: brokerTestProjectUID,
	}, appAccessGrantSourcePublishing), metav1.GetOptions{}); err != nil {
		t.Fatalf("retry did not create publishing primary role: %v", err)
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
		staleRole, err = client.Resource(appAccessClusterRoleGVR).Get(context.Background(), appAccessRoleName("app-demo-prod"), metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		staleRules, _, _ := unstructured.NestedSlice(staleRole.Object, "rules")
		if len(staleRules) != 0 {
			t.Fatalf("stale static role still grants the replacement: %#v", staleRules)
		}
		staleGrant, err := client.Resource(appAccessBindingGVR).Get(context.Background(), appAccessBindingName("app-demo-prod", "former-member"), metav1.GetOptions{})
		if err != nil || !validRoleReference(staleGrant, appAccessRoleName("app-demo-prod")) {
			t.Fatalf("stale managed binding should remain attached to inert role: %#v err=%v", staleGrant, err)
		}
		if _, err := client.Resource(appAccessClusterRoleGVR).Get(context.Background(), appAccessIdentityRoleName(appAccessGrantRequest{
			Name: "app-demo-prod", UID: brokerTestInstanceUID, ProjectUID: replacementProjectUID,
		}), metav1.GetOptions{}); !apierrors.IsNotFound(err) {
			t.Fatalf("stale grant was adopted into the replacement's UID-fenced role: err=%v", err)
		}
		fencedRoleUpdate := false
		for _, action := range client.Actions()[before:] {
			if action.GetVerb() != "update" || action.GetResource().Resource != "clusterroles" {
				continue
			}
			updateAction, ok := action.(k8stesting.UpdateAction)
			if !ok {
				t.Fatalf("role update action type = %T, want UpdateAction", action)
			}
			updated := updateAction.GetObject().(*unstructured.Unstructured)
			updatedRules, _, _ := unstructured.NestedSlice(updated.Object, "rules")
			if updated.GetName() == appAccessRoleName("app-demo-prod") && len(updatedRules) == 0 && updated.GetResourceVersion() != "" {
				fencedRoleUpdate = true
			}
		}
		if !fencedRoleUpdate {
			t.Fatal("stale legacy role was not fenced by a resourceVersion-bearing empty-rules update")
		}
	})

	t.Run("missing Project UID is tombstoned but never adopted", func(t *testing.T) {
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
		role.SetUID(types.UID("legacy-role-without-project-uid"))
		role.SetResourceVersion("41")
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
		persistedRole, err := client.Resource(appAccessClusterRoleGVR).Get(context.Background(), appAccessRoleName("app-demo-prod"), metav1.GetOptions{})
		if err != nil {
			t.Fatalf("legacy role should be retained as an inert tombstone: %v", err)
		}
		roleRules, _, _ := unstructured.NestedSlice(persistedRole.Object, "rules")
		if len(roleRules) != 0 || persistedRole.GetLabels()[appAccessProjectUID] != "" {
			t.Fatalf("missing-UID role was relabeled or left active: labels=%#v rules=%#v", persistedRole.GetLabels(), roleRules)
		}
		if _, err := client.Resource(appAccessBindingGVR).Get(context.Background(), appAccessBindingName("app-demo-prod", "former-member"), metav1.GetOptions{}); err != nil {
			t.Fatalf("unowned legacy binding was removed: %v", err)
		}
		if _, err := client.Resource(appAccessClusterRoleGVR).Get(context.Background(), appAccessUIDRoleName(appAccessGrantRequest{
			Name: "app-demo-prod", UID: brokerTestInstanceUID, ProjectUID: replacementProjectUID,
		}), metav1.GetOptions{}); !apierrors.IsNotFound(err) {
			t.Fatalf("missing-UID grant was copied into replacement role: %v", err)
		}
		for _, action := range client.Actions()[before:] {
			if action.GetVerb() == "update" && action.GetResource().Resource == "clusterroles" {
				updated := action.(k8stesting.UpdateAction).GetObject().(*unstructured.Unstructured)
				if updated.GetResourceVersion() != "41" {
					t.Fatalf("tombstone update lost optimistic concurrency resourceVersion: %q", updated.GetResourceVersion())
				}
			}
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
